package main

// smb2_ntlm.go — the sign-in part of the SMB client (smb2.go): NTLMv2
// (MS-NLMP) inside SPNEGO (RFC 4178), the way Windows, Samba (Synology,
// QNAP, TrueNAS, routers) and macOS file sharing all accept it.
//
//   - MD4 (RFC 1320) is written out here: NTLM hashes the password with it
//     and golang.org/x/crypto/md4 is not vendored.
//   - An empty user name signs in anonymously (MS-NLMP 3.2.5.1.2: empty NT
//     response, one zero byte of LM response); smbDial falls back to the
//     "guest" account when the server refuses that.
//   - The time stamp in the NTLMv2 answer is the server's own
//     (MsvAvTimestamp) whenever it sends one, so a handheld whose clock was
//     never set (no RTC battery, no NTP yet) still signs in.
//   - Key exchange (NTLMSSP_NEGOTIATE_KEY_EXCH) picks a random session key
//     and sends it RC4-wrapped, as Windows does; without it the session key
//     is the NTLMv2 session base key.
//   - No MIC and no SPNEGO mechListMIC are sent (the MIC is optional; a
//     client without it is what Samba and Windows treat as "old NTLM").

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"encoding/binary"
	"fmt"
	"math/bits"
	"strings"
	"time"
	"unicode/utf16"
)

// NTLMSSP negotiate flags (MS-NLMP 2.2.2.5).
const (
	smb2NtlmUnicode       = 0x00000001
	smb2NtlmRequestTarget = 0x00000004
	smb2NtlmSign          = 0x00000010
	smb2NtlmNTLM          = 0x00000200
	smb2NtlmAnonymous     = 0x00000800
	smb2NtlmAlwaysSign    = 0x00008000
	smb2NtlmESS           = 0x00080000
	smb2NtlmTargetInfo    = 0x00800000
	smb2NtlmVersion       = 0x02000000
	smb2Ntlm128           = 0x20000000
	smb2NtlmKeyExch       = 0x40000000
	smb2Ntlm56            = 0x80000000

	// smb2NtlmClientFlags is what this client asks for in NEGOTIATE.
	smb2NtlmClientFlags = smb2NtlmUnicode | smb2NtlmRequestTarget | smb2NtlmSign |
		smb2NtlmNTLM | smb2NtlmAlwaysSign | smb2NtlmESS | smb2NtlmVersion |
		smb2Ntlm128 | smb2NtlmKeyExch | smb2Ntlm56

	// smb2FileTimeEpoch is 1970-01-01 in FILETIME units (100 ns since 1601).
	smb2FileTimeEpoch = 116444736000000000
)

// smb2NtlmVersionBytes is the VERSION structure sent with NTLM messages
// (Windows 10.0 build 19041, NTLM revision 15).
var smb2NtlmVersionBytes = []byte{10, 0, 0x61, 0x4a, 0, 0, 0, 0x0f}

// ASN.1 DER object identifiers for SPNEGO and NTLMSSP, tag and length
// included.
var (
	smb2OIDSpnego = []byte{0x06, 0x06, 0x2b, 0x06, 0x01, 0x05, 0x05, 0x02}
	smb2OIDNTLM   = []byte{0x06, 0x0a, 0x2b, 0x06, 0x01, 0x04, 0x01, 0x82, 0x37, 0x02, 0x02, 0x0a}
)

// smb2MD4 is MD4 (RFC 1320).
func smb2MD4(msg []byte) [16]byte {
	a, b, c, d := uint32(0x67452301), uint32(0xefcdab89), uint32(0x98badcfe), uint32(0x10325476)
	padded := make([]byte, 0, len(msg)+72)
	padded = append(padded, msg...)
	padded = append(padded, 0x80)
	for len(padded)%64 != 56 {
		padded = append(padded, 0)
	}
	padded = binary.LittleEndian.AppendUint64(padded, uint64(len(msg))*8)
	var x [16]uint32
	for off := 0; off < len(padded); off += 64 {
		for i := range 16 {
			x[i] = binary.LittleEndian.Uint32(padded[off+4*i:])
		}
		aa, bb, cc, dd := a, b, c, d
		for _, i := range [4]int{0, 4, 8, 12} {
			a = bits.RotateLeft32(a+((b&c)|(^b&d))+x[i], 3)
			d = bits.RotateLeft32(d+((a&b)|(^a&c))+x[i+1], 7)
			c = bits.RotateLeft32(c+((d&a)|(^d&b))+x[i+2], 11)
			b = bits.RotateLeft32(b+((c&d)|(^c&a))+x[i+3], 19)
		}
		for _, i := range [4]int{0, 1, 2, 3} {
			a = bits.RotateLeft32(a+((b&c)|(b&d)|(c&d))+x[i]+0x5a827999, 3)
			d = bits.RotateLeft32(d+((a&b)|(a&c)|(b&c))+x[i+4]+0x5a827999, 5)
			c = bits.RotateLeft32(c+((d&a)|(d&b)|(a&b))+x[i+8]+0x5a827999, 9)
			b = bits.RotateLeft32(b+((c&d)|(c&a)|(d&a))+x[i+12]+0x5a827999, 13)
		}
		for _, i := range [4]int{0, 2, 1, 3} {
			a = bits.RotateLeft32(a+(b^c^d)+x[i]+0x6ed9eba1, 3)
			d = bits.RotateLeft32(d+(a^b^c)+x[i+8]+0x6ed9eba1, 9)
			c = bits.RotateLeft32(c+(d^a^b)+x[i+4]+0x6ed9eba1, 11)
			b = bits.RotateLeft32(b+(c^d^a)+x[i+12]+0x6ed9eba1, 15)
		}
		a += aa
		b += bb
		c += cc
		d += dd
	}
	var out [16]byte
	binary.LittleEndian.PutUint32(out[0:], a)
	binary.LittleEndian.PutUint32(out[4:], b)
	binary.LittleEndian.PutUint32(out[8:], c)
	binary.LittleEndian.PutUint32(out[12:], d)
	return out
}

// smb2UTF16 encodes s as UTF-16LE without a terminator.
func smb2UTF16(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, v := range u {
		binary.LittleEndian.PutUint16(b[2*i:], v)
	}
	return b
}

// smb2FromUTF16 decodes UTF-16LE (an odd trailing byte is dropped; bad
// surrogates become U+FFFD).
func smb2FromUTF16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

func smb2HMACMD5(key []byte, parts ...[]byte) []byte {
	h := hmac.New(md5.New, key)
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// smb2NTOWFv1 is MD4(UNICODE(password)) (MS-NLMP 3.3.1).
func smb2NTOWFv1(password string) []byte {
	h := smb2MD4(smb2UTF16(password))
	return h[:]
}

// smb2NTOWFv2 is HMAC_MD5(NTOWFv1, UNICODE(Uppercase(user) + domain))
// (MS-NLMP 3.3.2).
func smb2NTOWFv2(password, user, domain string) []byte {
	return smb2HMACMD5(smb2NTOWFv1(password), smb2UTF16(strings.ToUpper(user)+domain))
}

// smb2NTLMv2Response computes the NTLMv2 and LMv2 responses and the session
// base key (MS-NLMP 3.3.2). targetInfo is the AV pair list from CHALLENGE,
// passed through unchanged; timestamp is FILETIME.
func smb2NTLMv2Response(responseKeyNT, serverChallenge, clientChallenge []byte, timestamp uint64, targetInfo []byte) (nt, lm, sessionBaseKey []byte) {
	temp := make([]byte, 0, 32+len(targetInfo))
	temp = append(temp, 1, 1, 0, 0, 0, 0, 0, 0)
	temp = binary.LittleEndian.AppendUint64(temp, timestamp)
	temp = append(temp, clientChallenge...)
	temp = append(temp, 0, 0, 0, 0)
	temp = append(temp, targetInfo...)
	temp = append(temp, 0, 0, 0, 0)
	proof := smb2HMACMD5(responseKeyNT, serverChallenge, temp)
	nt = make([]byte, 0, len(proof)+len(temp))
	nt = append(nt, proof...)
	nt = append(nt, temp...)
	lm = append(smb2HMACMD5(responseKeyNT, serverChallenge, clientChallenge), clientChallenge...)
	sessionBaseKey = smb2HMACMD5(responseKeyNT, proof)
	return nt, lm, sessionBaseKey
}

// smb2NtlmNegotiateMsg builds NTLMSSP NEGOTIATE (type 1).
func smb2NtlmNegotiateMsg(flags uint32) []byte {
	b := make([]byte, 40)
	copy(b, "NTLMSSP\x00")
	binary.LittleEndian.PutUint32(b[8:], 1)
	binary.LittleEndian.PutUint32(b[12:], flags)
	// Empty domain and workstation fields point at the end of the message.
	binary.LittleEndian.PutUint32(b[20:], 40)
	binary.LittleEndian.PutUint32(b[28:], 40)
	copy(b[32:], smb2NtlmVersionBytes)
	return b
}

// smb2NtlmChallenge is the part of CHALLENGE (type 2) the client needs.
type smb2NtlmChallenge struct {
	flags      uint32
	challenge  []byte
	targetInfo []byte
}

// smb2ParseNtlmChallenge parses CHALLENGE; every length and offset is
// checked against the message.
func smb2ParseNtlmChallenge(b []byte) (*smb2NtlmChallenge, error) {
	if len(b) < 48 || string(b[:8]) != "NTLMSSP\x00" || binary.LittleEndian.Uint32(b[8:]) != 2 {
		return nil, smb2Bad("NTLM challenge")
	}
	ch := &smb2NtlmChallenge{
		flags:     binary.LittleEndian.Uint32(b[20:]),
		challenge: append([]byte(nil), b[24:32]...),
	}
	tiLen := uint64(binary.LittleEndian.Uint16(b[40:]))
	tiOff := uint64(binary.LittleEndian.Uint32(b[44:]))
	if tiLen > 0 {
		if tiOff < 48 || tiOff > uint64(len(b)) || tiLen > uint64(len(b))-tiOff {
			return nil, smb2Bad("NTLM target info")
		}
		ch.targetInfo = append([]byte(nil), b[tiOff:tiOff+tiLen]...)
	}
	return ch, nil
}

// smb2NtlmTimestamp returns MsvAvTimestamp (AvId 7) from an AV pair list.
func smb2NtlmTimestamp(ti []byte) (uint64, bool) {
	for len(ti) >= 4 {
		id := binary.LittleEndian.Uint16(ti)
		n := int(binary.LittleEndian.Uint16(ti[2:]))
		if id == 0 || n > len(ti)-4 {
			break
		}
		if id == 7 && n == 8 {
			return binary.LittleEndian.Uint64(ti[4:]), true
		}
		ti = ti[4+n:]
	}
	return 0, false
}

// smb2FileTimeNow is the current time as FILETIME.
func smb2FileTimeNow() uint64 {
	return uint64(time.Now().UnixNano()/100) + smb2FileTimeEpoch
}

// smb2NtlmAuthenticateMsg builds AUTHENTICATE (type 3) for ch and returns it
// with the exported session key (nil for an anonymous sign-in).
func smb2NtlmAuthenticateMsg(ch *smb2NtlmChallenge, user, password, domain, workstation string) (msg, sessionKey []byte, err error) {
	flags := ch.flags & smb2NtlmClientFlags
	flags |= smb2NtlmUnicode
	anonymous := user == "" && password == ""
	var lm, nt, encKey []byte
	if anonymous {
		flags |= smb2NtlmAnonymous
		flags &^= smb2NtlmKeyExch
		lm = []byte{0}
		domain, workstation = "", ""
	} else {
		cc := make([]byte, 8)
		if _, err := rand.Read(cc); err != nil {
			return nil, nil, err
		}
		ts, haveTS := smb2NtlmTimestamp(ch.targetInfo)
		if !haveTS {
			ts = smb2FileTimeNow()
		}
		key := smb2NTOWFv2(password, user, domain)
		var base []byte
		nt, lm, base = smb2NTLMv2Response(key, ch.challenge, cc, ts, ch.targetInfo)
		if haveTS {
			// MS-NLMP 3.1.5.1.2: with a server time stamp the LM response is Z(24).
			lm = make([]byte, 24)
		}
		sessionKey = base
		if flags&smb2NtlmKeyExch != 0 {
			exported := make([]byte, 16)
			if _, err := rand.Read(exported); err != nil {
				return nil, nil, err
			}
			rc, err := rc4.NewCipher(base)
			if err != nil {
				return nil, nil, err
			}
			encKey = make([]byte, 16)
			rc.XORKeyStream(encKey, exported)
			sessionKey = exported
		}
	}
	fields := [][]byte{lm, nt, smb2UTF16(domain), smb2UTF16(user), smb2UTF16(workstation), encKey}
	const head = 88 // fixed part + VERSION + MIC (left zero)
	size := head
	for _, f := range fields {
		size += len(f)
	}
	if size > 0xffff {
		return nil, nil, fmt.Errorf("%w: user name or domain too long", errSMBAuth)
	}
	msg = make([]byte, head, size)
	copy(msg, "NTLMSSP\x00")
	binary.LittleEndian.PutUint32(msg[8:], 3)
	for i, f := range fields {
		at := 12 + 8*i // Lm, Nt, Domain, User, Workstation, EncryptedRandomSessionKey
		binary.LittleEndian.PutUint16(msg[at:], uint16(len(f)))
		binary.LittleEndian.PutUint16(msg[at+2:], uint16(len(f)))
		binary.LittleEndian.PutUint32(msg[at+4:], uint32(len(msg)))
		msg = append(msg, f...)
	}
	binary.LittleEndian.PutUint32(msg[60:], flags)
	copy(msg[64:], smb2NtlmVersionBytes)
	return msg, sessionKey, nil
}

// smb2DERLen encodes an ASN.1 DER length.
func smb2DERLen(n int) []byte {
	switch {
	case n < 0x80:
		return []byte{byte(n)}
	case n < 0x100:
		return []byte{0x81, byte(n)}
	case n < 0x10000:
		return []byte{0x82, byte(n >> 8), byte(n)}
	default:
		return []byte{0x83, byte(n >> 16), byte(n >> 8), byte(n)}
	}
}

// smb2DER wraps parts in one DER element with the given tag.
func smb2DER(tag byte, parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	l := smb2DERLen(n)
	out := make([]byte, 0, 1+len(l)+n)
	out = append(out, tag)
	out = append(out, l...)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// smb2DERNext splits the first DER element off b.
func smb2DERNext(b []byte) (tag byte, content, rest []byte, err error) {
	if len(b) < 2 {
		return 0, nil, nil, smb2Bad("SPNEGO element")
	}
	tag = b[0]
	l := int(b[1])
	i := 2
	if l&0x80 != 0 {
		n := l & 0x7f
		if n == 0 || n > 3 || len(b) < 2+n {
			return 0, nil, nil, smb2Bad("SPNEGO length")
		}
		l = 0
		for _, x := range b[2 : 2+n] {
			l = l<<8 | int(x)
		}
		i = 2 + n
	}
	if l > len(b)-i {
		return 0, nil, nil, smb2Bad("SPNEGO length")
	}
	return tag, b[i : i+l], b[i+l:], nil
}

// smb2SpnegoInit wraps NTLM NEGOTIATE in a GSS-API NegTokenInit.
func smb2SpnegoInit(ntlmNegotiate []byte) []byte {
	mechTypes := smb2DER(0xa0, smb2DER(0x30, smb2OIDNTLM))
	mechToken := smb2DER(0xa2, smb2DER(0x04, ntlmNegotiate))
	return smb2DER(0x60, smb2OIDSpnego, smb2DER(0xa0, smb2DER(0x30, mechTypes, mechToken)))
}

// smb2SpnegoResp wraps NTLM AUTHENTICATE in a NegTokenResp.
func smb2SpnegoResp(ntlmAuthenticate []byte) []byte {
	return smb2DER(0xa1, smb2DER(0x30, smb2DER(0xa2, smb2DER(0x04, ntlmAuthenticate))))
}

// smb2SpnegoToken returns the mechanism token from the server's
// NegTokenResp (or a bare NTLMSSP message), and whether the server rejected
// the sign-in (negState reject).
func smb2SpnegoToken(b []byte) (token []byte, rejected bool, err error) {
	if len(b) >= 8 && string(b[:8]) == "NTLMSSP\x00" {
		return b, false, nil
	}
	tag, content, _, err := smb2DERNext(b)
	if err != nil {
		return nil, false, err
	}
	if tag != 0xa1 {
		return nil, false, smb2Bad("SPNEGO reply")
	}
	tag, seq, _, err := smb2DERNext(content)
	if err != nil {
		return nil, false, err
	}
	if tag != 0x30 {
		return nil, false, smb2Bad("SPNEGO reply")
	}
	for len(seq) > 0 {
		t, v, rest, err := smb2DERNext(seq)
		if err != nil {
			return nil, false, err
		}
		switch t {
		case 0xa0: // negState ENUMERATED
			if et, ev, _, err := smb2DERNext(v); err == nil && et == 0x0a && len(ev) == 1 && ev[0] == 2 {
				rejected = true
			}
		case 0xa2: // responseToken OCTET STRING
			ot, ov, _, err := smb2DERNext(v)
			if err != nil {
				return nil, false, err
			}
			if ot == 0x04 {
				token = ov
			}
		}
		seq = rest
	}
	return token, rejected, nil
}
