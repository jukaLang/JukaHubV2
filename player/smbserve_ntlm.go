package main

// smbserve_ntlm.go — the sign-in half of the SMB file server (smbserve.go):
// NTLMv2 (MS-NLMP) inside SPNEGO (RFC 4178), the server side of what
// smb2_ntlm.go does as a client, built on its MD4, HMAC-MD5, UTF-16 and DER
// helpers.
//
//   - Only NTLMv2 signs in. An NTLMv1/LM answer (24 bytes), an anonymous
//     one (empty) and a guest are refused like a wrong password.
//   - The user name is compared without regard to case and the domain is
//     ignored — but the NTLMv2 proof is keyed with the domain the client
//     typed, so it is tried as sent, upper-cased and empty (what Samba does
//     for clients that send one domain and hash with another).
//   - The proof is compared with hmac.Equal (constant time).
//   - KEY_EXCH: the exported session key is RC4-unwrapped with the session
//     base key; without it the session key is the base key. Either way it
//     keys SMB 2.x signing (HMAC-SHA256).
//   - The CHALLENGE carries the server's time (MsvAvTimestamp), so the
//     client never needs this device's clock to be right.
//   - No SPNEGO mechListMIC is sent back and the NTLM MIC is not checked:
//     signing is required on every session, so a relayed or altered sign-in
//     still ends without the session key.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rc4"
	"encoding/binary"
	"errors"
	"strings"
)

const (
	smbSrvNtlmSeal       = 0x00000020
	smbSrvNtlmTargetSrv  = 0x00020000
	smbSrvNtlmMinNTResp  = 48
	smbSrvNtlmMaxMessage = 8 << 10
)

// errSMBServeLogon is any refused sign-in; the client only ever sees
// STATUS_LOGON_FAILURE.
var errSMBServeLogon = errors.New("smb server: sign-in refused")

// smbSrvNtlm is one sign-in between its CHALLENGE and its AUTHENTICATE.
type smbSrvNtlm struct {
	challenge [8]byte
	flags     uint32
	name      string
}

// smbSrvAuth is a sign-in that passed.
type smbSrvAuth struct {
	user       string
	domain     string
	sessionKey []byte
}

// smbSrvNegotiateToken is the security buffer of the NEGOTIATE reply: a
// GSS-API NegTokenInit that offers NTLMSSP only.
func smbSrvNegotiateToken() []byte {
	mechTypes := smb2DER(0xa0, smb2DER(0x30, smb2OIDNTLM))
	return smb2DER(0x60, smb2OIDSpnego, smb2DER(0xa0, smb2DER(0x30, mechTypes)))
}

// smbSrvSpnegoResp is a NegTokenResp: negState (0 accept-completed,
// 1 accept-incomplete), NTLMSSP as the chosen mechanism when withMech is
// set, and tok as the response token when there is one.
func smbSrvSpnegoResp(state byte, withMech bool, tok []byte) []byte {
	parts := [][]byte{smb2DER(0xa0, smb2DER(0x0a, []byte{state}))}
	if withMech {
		parts = append(parts, smb2DER(0xa1, smb2OIDNTLM))
	}
	if len(tok) > 0 {
		parts = append(parts, smb2DER(0xa2, smb2DER(0x04, tok)))
	}
	return smb2DER(0xa1, smb2DER(0x30, parts...))
}

// smbSrvUnwrapToken takes the NTLMSSP message out of a SESSION_SETUP
// security buffer: a bare NTLMSSP message, a GSS-API NegTokenInit or a
// NegTokenResp. spnego says whether the reply must be wrapped too. A token
// of another mechanism (a Kerberos ticket sent optimistically) comes back
// as nil with no error: the server then names NTLMSSP and waits.
func smbSrvUnwrapToken(b []byte) (tok []byte, spnego bool, err error) {
	if len(b) >= 8 && string(b[:8]) == "NTLMSSP\x00" {
		return b, false, nil
	}
	tag, content, _, derr := smb2DERNext(b)
	if derr != nil {
		return nil, true, derr
	}
	switch tag {
	case 0x60:
		// InitialContextToken: the SPNEGO OID, then NegTokenInit as [0].
		oidTag, _, rest, e1 := smb2DERNext(content)
		if e1 != nil || oidTag != 0x06 {
			return nil, true, smb2Bad("SPNEGO init")
		}
		initTag, inner, _, e2 := smb2DERNext(rest)
		if e2 != nil || initTag != 0xa0 {
			return nil, true, smb2Bad("SPNEGO init")
		}
		content = inner
	case 0xa1:
	default:
		return nil, true, smb2Bad("SPNEGO token")
	}
	seqTag, seq, _, serr := smb2DERNext(content)
	if serr != nil || seqTag != 0x30 {
		return nil, true, smb2Bad("SPNEGO sequence")
	}
	for len(seq) > 0 {
		t, v, rest, e := smb2DERNext(seq)
		if e != nil {
			return nil, true, e
		}
		if t == 0xa2 {
			if ot, ov, _, oe := smb2DERNext(v); oe == nil && ot == 0x04 {
				tok = ov
			}
		}
		seq = rest
	}
	if len(tok) < 12 || string(tok[:8]) != "NTLMSSP\x00" {
		return nil, true, nil
	}
	return tok, true, nil
}

// smbSrvNtlmType is the message type of an NTLMSSP message (0 for none).
func smbSrvNtlmType(tok []byte) uint32 {
	if len(tok) < 12 || string(tok[:8]) != "NTLMSSP\x00" {
		return 0
	}
	return binary.LittleEndian.Uint32(tok[8:])
}

// smbSrvAvPair appends one MsvAv pair.
func smbSrvAvPair(b []byte, id uint16, v []byte) []byte {
	b = binary.LittleEndian.AppendUint16(b, id)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(v)))
	return append(b, v...)
}

// newSmbSrvNtlm starts a sign-in with a fresh random server challenge.
func newSmbSrvNtlm(name string) (*smbSrvNtlm, error) {
	n := &smbSrvNtlm{name: name}
	if _, err := rand.Read(n.challenge[:]); err != nil {
		return nil, err
	}
	return n, nil
}

// challengeMsg builds CHALLENGE (type 2) for a NEGOTIATE that asked for
// clientFlags; now is the server time as FILETIME (MsvAvTimestamp).
func (n *smbSrvNtlm) challengeMsg(clientFlags uint32, now uint64) []byte {
	flags := uint32(smb2NtlmUnicode | smb2NtlmRequestTarget | smb2NtlmNTLM | smb2NtlmAlwaysSign |
		smb2NtlmESS | smbSrvNtlmTargetSrv | smb2NtlmTargetInfo | smb2NtlmVersion)
	flags |= clientFlags & (smb2NtlmSign | smbSrvNtlmSeal | smb2Ntlm128 | smb2Ntlm56 | smb2NtlmKeyExch)
	n.flags = flags
	upper := smb2UTF16(strings.ToUpper(n.name))
	lower := smb2UTF16(strings.ToLower(n.name))
	var ts [8]byte
	binary.LittleEndian.PutUint64(ts[:], now)
	ti := make([]byte, 0, 4*len(upper)+48)
	ti = smbSrvAvPair(ti, 2, upper) // MsvAvNbDomainName
	ti = smbSrvAvPair(ti, 1, upper) // MsvAvNbComputerName
	ti = smbSrvAvPair(ti, 4, lower) // MsvAvDnsDomainName
	ti = smbSrvAvPair(ti, 3, lower) // MsvAvDnsComputerName
	ti = smbSrvAvPair(ti, 7, ts[:]) // MsvAvTimestamp
	ti = smbSrvAvPair(ti, 0, nil)   // MsvAvEOL
	msg := make([]byte, 56, 56+len(upper)+len(ti))
	copy(msg, "NTLMSSP\x00")
	binary.LittleEndian.PutUint32(msg[8:], 2)
	binary.LittleEndian.PutUint16(msg[12:], uint16(len(upper)))
	binary.LittleEndian.PutUint16(msg[14:], uint16(len(upper)))
	binary.LittleEndian.PutUint32(msg[16:], 56)
	binary.LittleEndian.PutUint32(msg[20:], flags)
	copy(msg[24:32], n.challenge[:])
	binary.LittleEndian.PutUint16(msg[40:], uint16(len(ti)))
	binary.LittleEndian.PutUint16(msg[42:], uint16(len(ti)))
	binary.LittleEndian.PutUint32(msg[44:], uint32(56+len(upper)))
	copy(msg[48:56], smb2NtlmVersionBytes)
	msg = append(msg, upper...)
	return append(msg, ti...)
}

// smbSrvNtlmField returns one security buffer (Len, MaxLen, Offset at at)
// of an NTLMSSP message, checked against the message.
func smbSrvNtlmField(msg []byte, at int) ([]byte, bool) {
	if at+8 > len(msg) {
		return nil, false
	}
	n := uint64(binary.LittleEndian.Uint16(msg[at:]))
	off := uint64(binary.LittleEndian.Uint32(msg[at+4:]))
	if n == 0 {
		return nil, true
	}
	if off > uint64(len(msg)) || n > uint64(len(msg))-off {
		return nil, false
	}
	return msg[off : off+n], true
}

// smbSrvNtlmV2Check checks an NTLMv2 response (NTProofStr then the client's
// blob) for password, user and domain against the server challenge, and
// returns the session base key when it matches.
func smbSrvNtlmV2Check(password, user, domain string, challenge, nt []byte) ([]byte, bool) {
	if len(nt) < smbSrvNtlmMinNTResp || nt[16] != 1 || nt[17] != 1 {
		return nil, false
	}
	proof, blob := nt[:16], nt[16:]
	key := smb2NTOWFv2(password, user, domain)
	if !hmac.Equal(smb2HMACMD5(key, challenge, blob), proof) {
		return nil, false
	}
	return smb2HMACMD5(key, proof), true
}

// verify checks AUTHENTICATE (type 3) against the one account (user,
// password) and returns the session key. Every refusal is errSMBServeLogon.
func (n *smbSrvNtlm) verify(msg []byte, user, password string) (*smbSrvAuth, error) {
	if len(msg) < 64 || len(msg) > smbSrvNtlmMaxMessage || smbSrvNtlmType(msg) != 3 || password == "" {
		return nil, errSMBServeLogon
	}
	nt, ok1 := smbSrvNtlmField(msg, 20)
	domB, ok2 := smbSrvNtlmField(msg, 28)
	userB, ok3 := smbSrvNtlmField(msg, 36)
	encKey, ok4 := smbSrvNtlmField(msg, 52)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil, errSMBServeLogon
	}
	authFlags := binary.LittleEndian.Uint32(msg[60:])
	if authFlags&smb2NtlmUnicode == 0 || authFlags&smb2NtlmAnonymous != 0 || len(nt) < smbSrvNtlmMinNTResp {
		return nil, errSMBServeLogon
	}
	name := smb2FromUTF16(userB)
	domain := smb2FromUTF16(domB)
	if !strings.EqualFold(strings.TrimSpace(name), user) {
		return nil, errSMBServeLogon
	}
	var base []byte
	tried := map[string]bool{}
	for _, d := range []string{domain, strings.ToUpper(domain), ""} {
		if tried[d] {
			continue
		}
		tried[d] = true
		if k, ok := smbSrvNtlmV2Check(password, name, d, n.challenge[:], nt); ok {
			base = k
			break
		}
	}
	if base == nil {
		return nil, errSMBServeLogon
	}
	key := base
	if authFlags&n.flags&smb2NtlmKeyExch != 0 {
		if len(encKey) != 16 {
			return nil, errSMBServeLogon
		}
		rc, err := rc4.NewCipher(base)
		if err != nil {
			return nil, errSMBServeLogon
		}
		key = make([]byte, 16)
		rc.XORKeyStream(key, encKey)
	}
	return &smbSrvAuth{user: name, domain: domain, sessionKey: key}, nil
}
