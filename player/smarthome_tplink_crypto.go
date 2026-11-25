package main

// smarthome_tplink_crypto.go — the three wire encodings of TP-Link's Kasa and
// Tapo devices, as plain functions (smarthome_tplink.go speaks them):
//
//   - The old Kasa "autokey" XOR: every byte is XORed with the previous cipher
//     byte, the first one with 171. Over TCP 9999 a four-byte big-endian
//     length goes in front; the UDP discovery query has none.
//   - KLAP, which newer Kasa hardware and current Tapo firmware speak over
//     HTTP. Two handshakes prove that both ends know the hash of the TP-Link
//     ID (MD5-based on Kasa, "v1"; SHA-256-based on Tapo, "v2"); every request
//     after them is AES-128-CBC with a key, IV and signature made from both
//     random seeds and that hash. The IV's last four bytes are a sequence
//     number that goes up by one per request and travels in the URL.
//   - Tapo's older "secure passthrough": JukaHub sends an RSA public key with
//     the handshake and the device answers with an AES-128 key and IV
//     encrypted to it; requests are then AES-128-CBC under that key and IV,
//     base64 inside a JSON envelope.
//
// These are the derivations the open-source python-kasa library uses (the
// library Home Assistant's TP-Link integration is built on). The known
// answers in smarthome_tplink_test.go were computed independently with
// Python's hashlib and the cryptography package.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sync"
)

// tplinkXORSeed is the autokey cipher's first key byte.
const tplinkXORSeed = 171

// tplinkXOREncrypt is the autokey cipher, without the length prefix.
func tplinkXOREncrypt(plain []byte) []byte {
	out := make([]byte, len(plain))
	key := byte(tplinkXORSeed)
	for i, b := range plain {
		key ^= b
		out[i] = key
	}
	return out
}

// tplinkXORDecrypt undoes tplinkXOREncrypt.
func tplinkXORDecrypt(data []byte) []byte {
	out := make([]byte, len(data))
	key := byte(tplinkXORSeed)
	for i, c := range data {
		out[i] = key ^ c
		key = c
	}
	return out
}

// tplinkXORFrame is a TCP request: the length, then the cipher text.
func tplinkXORFrame(plain []byte) []byte {
	out := make([]byte, 4, 4+len(plain))
	binary.BigEndian.PutUint32(out, uint32(len(plain)))
	return append(out, tplinkXOREncrypt(plain)...)
}

// tplinkSHA256 hashes the parts one after another.
func tplinkSHA256(parts ...[]byte) []byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// tplinkKLAPAuthHash is the hash of a TP-Link ID a KLAP device checks: v1
// (Kasa) is md5(md5(user) + md5(password)), v2 (Tapo and the newest Kasa)
// sha256(sha1(user) + sha1(password)).
func tplinkKLAPAuthHash(v2 bool, user, pass string) []byte {
	if v2 {
		u := sha1.Sum([]byte(user))
		p := sha1.Sum([]byte(pass))
		return tplinkSHA256(u[:], p[:])
	}
	u := md5.Sum([]byte(user))
	p := md5.Sum([]byte(pass))
	h := md5.Sum(append(u[:], p[:]...))
	return h[:]
}

// tplinkKLAPServerHash is what the device answers handshake1 with: proof that
// it knows the hash.
func tplinkKLAPServerHash(v2 bool, local, remote, auth []byte) []byte {
	if v2 {
		return tplinkSHA256(local, remote, auth)
	}
	return tplinkSHA256(local, auth)
}

// tplinkKLAPClientHash is what handshake2 sends: JukaHub's proof.
func tplinkKLAPClientHash(v2 bool, local, remote, auth []byte) []byte {
	if v2 {
		return tplinkSHA256(remote, local, auth)
	}
	return tplinkSHA256(remote, auth)
}

// tplinkKLAPSession is one KLAP session's key material and sequence number.
type tplinkKLAPSession struct {
	key []byte // AES-128 key
	iv  []byte // the IV's first 12 bytes; the sequence number fills the rest
	sig []byte // signature prefix
	seq int32
}

// newTPLinkKLAPSession derives the session from both seeds and the hash the
// handshake agreed on.
func newTPLinkKLAPSession(local, remote, auth []byte) *tplinkKLAPSession {
	full := tplinkSHA256([]byte("iv"), local, remote, auth)
	return &tplinkKLAPSession{
		key: tplinkSHA256([]byte("lsk"), local, remote, auth)[:16],
		iv:  full[:12],
		sig: tplinkSHA256([]byte("ldk"), local, remote, auth)[:28],
		seq: int32(binary.BigEndian.Uint32(full[28:32])),
	}
}

// ivFor is the full IV of request seq.
func (s *tplinkKLAPSession) ivFor(seq int32) []byte {
	iv := make([]byte, aes.BlockSize)
	copy(iv, s.iv)
	binary.BigEndian.PutUint32(iv[12:], uint32(seq))
	return iv
}

// encrypt seals one request: the sequence number goes up first, and the body
// is the SHA-256 signature followed by the cipher text.
func (s *tplinkKLAPSession) encrypt(plain []byte) ([]byte, int32, error) {
	s.seq++
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, 0, err
	}
	padded := tplinkPKCS7Pad(plain)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, s.ivFor(s.seq)).CryptBlocks(ct, padded)
	var seq [4]byte
	binary.BigEndian.PutUint32(seq[:], uint32(s.seq))
	body := tplinkSHA256(s.sig, seq[:], ct)
	return append(body, ct...), s.seq, nil
}

// decrypt opens the answer to request seq (its signature is not checked: the
// answer could only be made with the session key).
func (s *tplinkKLAPSession) decrypt(body []byte, seq int32) ([]byte, error) {
	if len(body) < 32+aes.BlockSize || (len(body)-32)%aes.BlockSize != 0 {
		return nil, errors.New("the device's answer is cut short")
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	pt := make([]byte, len(body)-32)
	cipher.NewCBCDecrypter(block, s.ivFor(seq)).CryptBlocks(pt, body[32:])
	return tplinkPKCS7Unpad(pt)
}

// tplinkPKCS7Pad pads to whole AES blocks.
func tplinkPKCS7Pad(b []byte) []byte {
	n := aes.BlockSize - len(b)%aes.BlockSize
	return append(append(make([]byte, 0, len(b)+n), b...), bytes.Repeat([]byte{byte(n)}, n)...)
}

// tplinkPKCS7Unpad removes the padding, refusing any that is malformed.
func tplinkPKCS7Unpad(b []byte) ([]byte, error) {
	if len(b) == 0 || len(b)%aes.BlockSize != 0 {
		return nil, errors.New("the device's answer is not whole blocks")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > aes.BlockSize || n > len(b) {
		return nil, errors.New("the device's answer has bad padding")
	}
	for _, c := range b[len(b)-n:] {
		if int(c) != n {
			return nil, errors.New("the device's answer has bad padding")
		}
	}
	return b[:len(b)-n], nil
}

// tplinkAESSession is a secure-passthrough session: one key and one IV, each
// message encrypted from the start of the chain.
type tplinkAESSession struct {
	key, iv []byte
}

// encrypt is the base64 of the padded cipher text.
func (s *tplinkAESSession) encrypt(plain []byte) (string, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", err
	}
	padded := tplinkPKCS7Pad(plain)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, s.iv).CryptBlocks(ct, padded)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// decrypt opens a base64 answer.
func (s *tplinkAESSession) decrypt(b64 string) ([]byte, error) {
	ct, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, errors.New("the device's answer is not base64")
	}
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return nil, errors.New("the device's answer is not whole blocks")
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, s.iv).CryptBlocks(pt, ct)
	return tplinkPKCS7Unpad(pt)
}

// tplinkAESCredentials are the login fields of secure passthrough: the user
// is base64 of the hex SHA-1 of the TP-Link ID; the password base64 as typed
// ("password", login version 1) or of its hex SHA-1 ("password2", version 2).
func tplinkAESCredentials(v2 bool, user, pass string) map[string]string {
	hexSHA1 := func(s string) string {
		h := sha1.Sum([]byte(s))
		return hex.EncodeToString(h[:])
	}
	out := map[string]string{"username": base64.StdEncoding.EncodeToString([]byte(hexSHA1(user)))}
	if v2 {
		out["password2"] = base64.StdEncoding.EncodeToString([]byte(hexSHA1(pass)))
	} else {
		out["password"] = base64.StdEncoding.EncodeToString([]byte(pass))
	}
	return out
}

// tplinkRSA is the key pair the handshakes and the discovery probe send:
// 1024-bit, as the Tapo app's, made once (it only ever receives session keys).
var tplinkRSA struct {
	once sync.Once
	key  *rsa.PrivateKey
	pem  string
	err  error
}

// tplinkRSAKey is the key and its public half in the form the devices read:
// one base64 line between the PEM markers.
func tplinkRSAKey() (*rsa.PrivateKey, string, error) {
	tplinkRSA.once.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 1024)
		if err != nil {
			tplinkRSA.err = err
			return
		}
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			tplinkRSA.err = err
			return
		}
		tplinkRSA.key = key
		tplinkRSA.pem = "-----BEGIN PUBLIC KEY-----\n" + base64.StdEncoding.EncodeToString(der) + "\n-----END PUBLIC KEY-----\n"
	})
	return tplinkRSA.key, tplinkRSA.pem, tplinkRSA.err
}

// tplinkAESSessionFrom opens the handshake's answer: base64 of the session's
// key and IV, encrypted to JukaHub's public key.
func tplinkAESSessionFrom(key *rsa.PrivateKey, b64 string) (*tplinkAESSession, error) {
	ct, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, errors.New("the device's handshake key is not base64")
	}
	plain, err := rsa.DecryptPKCS1v15(rand.Reader, key, ct)
	if err != nil || len(plain) < 32 {
		return nil, errors.New("the device's handshake key could not be read")
	}
	return &tplinkAESSession{key: append([]byte(nil), plain[:16]...), iv: append([]byte(nil), plain[16:32]...)}, nil
}
