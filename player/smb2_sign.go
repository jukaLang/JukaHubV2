package main

// smb2_sign.go — message signing and encryption for the SMB client
// (smb2.go), MS-SMB2 3.1.4.1 / 3.1.4.3 / 3.2.5.3.1:
//
//   - SMB 2.0.2 and 2.1 sign with HMAC-SHA256 keyed by the session key;
//   - SMB 3.0 and 3.0.2 sign with AES-128-CMAC (RFC 4493, written out over
//     crypto/aes) keyed by KDF(SessionKey, "SMB2AESCMAC", "SmbSign");
//   - SMB 3.1.1 signs with AES-CMAC keyed by KDF(SessionKey,
//     "SMBSigningKey", preauth hash) and, when a server or share demands
//     it, encrypts with AES-128-GCM (keys "SMBC2SCipherKey" /
//     "SMBS2CCipherKey") in a TRANSFORM_HEADER.
//
// The KDF is NIST SP 800-108 counter mode with HMAC-SHA256 (r = 32,
// L = 128). AES-CCM (the only cipher of SMB 3.0/3.0.2) is not supported;
// such a server gets errSMBEncryptionRequired.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// smb2KDF is SP800-108 counter-mode HMAC-SHA256 with a 128-bit output.
// label and context are passed with their trailing NUL, as MS-SMB2 lists
// them ("SMB2AESCMAC\x00", "SmbSign\x00").
func smb2KDF(ki, label, context []byte) []byte {
	h := hmac.New(sha256.New, ki)
	h.Write([]byte{0, 0, 0, 1})
	h.Write(label)
	h.Write([]byte{0})
	h.Write(context)
	h.Write([]byte{0, 0, 0, 128})
	return h.Sum(nil)[:16]
}

// smb2CMACDouble is the GF(2^128) doubling of RFC 4493 2.3.
func smb2CMACDouble(in [16]byte) [16]byte {
	var out [16]byte
	for i := 0; i < 15; i++ {
		out[i] = in[i]<<1 | in[i+1]>>7
	}
	out[15] = in[15] << 1
	if in[0]&0x80 != 0 {
		out[15] ^= 0x87
	}
	return out
}

// smb2CMAC is AES-CMAC (RFC 4493) of msg with the AES block blk.
func smb2CMAC(blk cipher.Block, msg []byte) [16]byte {
	var l [16]byte
	blk.Encrypt(l[:], l[:])
	k1 := smb2CMACDouble(l)
	k2 := smb2CMACDouble(k1)
	n := (len(msg) + 15) / 16
	complete := n > 0 && len(msg)%16 == 0
	if n == 0 {
		n = 1
	}
	var last [16]byte
	if complete {
		copy(last[:], msg[(n-1)*16:])
		for i := range last {
			last[i] ^= k1[i]
		}
	} else {
		r := msg[(n-1)*16:]
		copy(last[:], r)
		last[len(r)] = 0x80
		for i := range last {
			last[i] ^= k2[i]
		}
	}
	var x [16]byte
	for blkN := 0; blkN < n-1; blkN++ {
		m := msg[blkN*16 : blkN*16+16]
		for i := range x {
			x[i] ^= m[i]
		}
		blk.Encrypt(x[:], x[:])
	}
	for i := range x {
		x[i] ^= last[i]
	}
	blk.Encrypt(x[:], x[:])
	return x
}

// smb2Signer signs and checks SMB2 messages of one session.
type smb2Signer struct {
	hmacKey []byte       // SMB 2.0.2 / 2.1
	cmac    cipher.Block // SMB 3.x
}

// smb2NewSigner derives the signing key for dialect from the session key
// (and, for 3.1.1, the session's preauth integrity hash).
func smb2NewSigner(dialect uint16, sessionKey, preauth []byte) (*smb2Signer, error) {
	switch {
	case dialect < smb2Dialect300:
		return &smb2Signer{hmacKey: append([]byte(nil), sessionKey...)}, nil
	case dialect == smb2Dialect311:
		blk, err := aes.NewCipher(smb2KDF(sessionKey, []byte("SMBSigningKey\x00"), preauth))
		if err != nil {
			return nil, err
		}
		return &smb2Signer{cmac: blk}, nil
	default:
		blk, err := aes.NewCipher(smb2KDF(sessionKey, []byte("SMB2AESCMAC\x00"), []byte("SmbSign\x00")))
		if err != nil {
			return nil, err
		}
		return &smb2Signer{cmac: blk}, nil
	}
}

// sum computes the signature of msg, whose signature field must be zero.
func (s *smb2Signer) sum(msg []byte) [16]byte {
	if s.cmac != nil {
		return smb2CMAC(s.cmac, msg)
	}
	h := hmac.New(sha256.New, s.hmacKey)
	h.Write(msg)
	var out [16]byte
	copy(out[:], h.Sum(nil))
	return out
}

// sign sets SMB2_FLAGS_SIGNED and the signature of an outgoing message.
func (s *smb2Signer) sign(msg []byte) {
	binary.LittleEndian.PutUint32(msg[16:], binary.LittleEndian.Uint32(msg[16:])|smb2FlagSigned)
	clear(msg[48:64])
	sig := s.sum(msg)
	copy(msg[48:64], sig[:])
}

// verify checks the signature of a received message. The signature field
// is zeroed in place.
func (s *smb2Signer) verify(msg []byte) bool {
	var got [16]byte
	copy(got[:], msg[48:64])
	clear(msg[48:64])
	want := s.sum(msg)
	return hmac.Equal(got[:], want[:])
}

// smb2Crypt holds the AES-128-GCM keys of an SMB 3.1.1 session.
type smb2Crypt struct {
	enc  cipher.AEAD // client to server
	dec  cipher.AEAD // server to client
	salt [4]byte     // random tail of every nonce
	ctr  uint64      // nonce counter; never repeats within the session
}

// smb2NewCrypt derives the 3.1.1 cipher keys (MS-SMB2 3.2.5.3.1).
func smb2NewCrypt(sessionKey, preauth []byte) (*smb2Crypt, error) {
	mk := func(label string) (cipher.AEAD, error) {
		blk, err := aes.NewCipher(smb2KDF(sessionKey, []byte(label), preauth))
		if err != nil {
			return nil, err
		}
		return cipher.NewGCM(blk)
	}
	enc, err := mk("SMBC2SCipherKey\x00")
	if err != nil {
		return nil, err
	}
	dec, err := mk("SMBS2CCipherKey\x00")
	if err != nil {
		return nil, err
	}
	c := &smb2Crypt{enc: enc, dec: dec}
	if _, err := rand.Read(c.salt[:]); err != nil {
		return nil, err
	}
	return c, nil
}

// seal wraps msg in a TRANSFORM_HEADER (52 bytes) for sessionID.
func (c *smb2Crypt) seal(msg []byte, sessionID uint64) []byte {
	out := make([]byte, smb2TransformLen, smb2TransformLen+len(msg)+16)
	copy(out, "\xfdSMB")
	c.ctr++
	binary.LittleEndian.PutUint64(out[20:], c.ctr)
	copy(out[28:32], c.salt[:])
	binary.LittleEndian.PutUint32(out[36:], uint32(len(msg)))
	binary.LittleEndian.PutUint16(out[42:], 1) // Flags: encrypted
	binary.LittleEndian.PutUint64(out[44:], sessionID)
	sealed := c.enc.Seal(out[smb2TransformLen:smb2TransformLen], out[20:32], msg, out[20:smb2TransformLen])
	copy(out[4:20], sealed[len(msg):])
	return out[:smb2TransformLen+len(msg)]
}

// open checks and decrypts a TRANSFORM_HEADER message for sessionID.
func (c *smb2Crypt) open(frame []byte, sessionID uint64) ([]byte, error) {
	if len(frame) < smb2TransformLen+smb2HeaderLen {
		return nil, smb2Bad("short encrypted message")
	}
	size := uint64(binary.LittleEndian.Uint32(frame[36:]))
	if binary.LittleEndian.Uint16(frame[42:]) != 1 || binary.LittleEndian.Uint64(frame[44:]) != sessionID ||
		size != uint64(len(frame)-smb2TransformLen) {
		return nil, smb2Bad("encrypted message header")
	}
	buf := make([]byte, 0, len(frame)-smb2TransformLen+16)
	buf = append(buf, frame[smb2TransformLen:]...)
	buf = append(buf, frame[4:20]...)
	plain, err := c.dec.Open(buf[:0], frame[20:32], buf, frame[20:smb2TransformLen])
	if err != nil {
		return nil, fmt.Errorf("%w: encrypted reply did not decrypt", errSMBSignature)
	}
	return plain, nil
}
