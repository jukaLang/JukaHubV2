package main

// wg_noise.go — WireGuard's handshake (Noise_IKpsk2_25519_ChaChaPoly_BLAKE2s,
// whitepaper §5.4) for the VPN (vpn.go, vpnd_linux.go).
//
// JukaHub is always the client of one VPN server, so this is the protocol for
// exactly one peer: no peer table, no allowed-IPs trie and no cookie
// generator (a client is never the side under load). It does answer a
// handshake the server starts — the responder half — because a server with
// traffic for an idle client may start one.
//
// The primitives come from golang.org/x/crypto (BLAKE2s, ChaCha20-Poly1305,
// XChaCha20-Poly1305 for cookies) and crypto/ecdh (X25519). Every message was
// checked against the Linux kernel's WireGuard: tmp/wgref/kernel_interop.py
// plays both roles against it in a throwaway network namespace, and
// wg_test.go replays the recorded exchanges byte for byte.

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"hash"
	"time"

	"golang.org/x/crypto/blake2s"
	"golang.org/x/crypto/chacha20poly1305"
)

// Message types and sizes (whitepaper §5.4.2–§5.4.7).
const (
	wgMsgInitiation = 1
	wgMsgResponse   = 2
	wgMsgCookie     = 3
	wgMsgData       = 4

	wgInitiationSize = 148
	wgResponseSize   = 92
	wgCookieSize     = 64
	wgDataHeaderSize = 16
	wgTagSize        = chacha20poly1305.Overhead
	wgKeepaliveSize  = wgDataHeaderSize + wgTagSize
)

// The protocol's limits and timers (whitepaper §6.1).
const (
	wgRekeyAfterMessages  = uint64(1) << 60
	wgRejectAfterMessages = ^uint64(0) - (1 << 13)
	wgRekeyAfterTime      = 120 * time.Second
	wgRejectAfterTime     = 180 * time.Second
	wgRekeyAttemptTime    = 90 * time.Second
	wgRekeyTimeout        = 5 * time.Second
	wgKeepaliveTimeout    = 10 * time.Second
	wgCookieLifetime      = 120 * time.Second
	// wgInitiationMinGap is the fastest the peer may start handshakes (50 a
	// second, as the kernel enforces): a quicker one is a flood.
	wgInitiationMinGap = time.Second / 50
	// wgTAI64Base is TAI64's label for 1970 plus the 10 s TAI-UTC offset that
	// the kernel and wireguard-go both add to the handshake timestamp.
	wgTAI64Base = uint64(0x400000000000000a)
)

var (
	wgConstruction = []byte("Noise_IKpsk2_25519_ChaChaPoly_BLAKE2s")
	wgIdentifier   = []byte("WireGuard v1 zx2c4 Jason@zx2c4.com")
	wgLabelMAC1    = []byte("mac1----")
	wgLabelCookie  = []byte("cookie--")

	// The chaining key and hash every handshake starts from.
	wgInitialChainKey = blake2s.Sum256(wgConstruction)
	wgInitialHash     = wgHash(wgInitialChainKey[:], wgIdentifier)
)

var (
	errWGBadMessage = errors.New("wireguard: message does not authenticate")
	errWGUnexpected = errors.New("wireguard: message does not belong to a handshake in progress")
	errWGReplay     = errors.New("wireguard: replayed or stale handshake")
	errWGFlood      = errors.New("wireguard: handshakes arriving too fast")
	errWGBadKey     = errors.New("wireguard: unusable X25519 key")
)

// wgHash is HASH: BLAKE2s-256 of the parts in order.
func wgHash(parts ...[]byte) [32]byte {
	h, _ := blake2s.New256(nil)
	for _, p := range parts {
		h.Write(p)
	}
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// wgMAC is MAC: keyed BLAKE2s with a 16-byte output. key is 32 bytes (mac1)
// or a 16-byte cookie (mac2); New128 accepts both.
func wgMAC(key, data []byte) [16]byte {
	var out [16]byte
	h, err := blake2s.New128(key)
	if err != nil {
		return out
	}
	h.Write(data)
	h.Sum(out[:0])
	return out
}

func wgNewHash() hash.Hash {
	h, _ := blake2s.New256(nil)
	return h
}

// wgHMAC is HMAC-BLAKE2s of the parts in order.
func wgHMAC(key []byte, parts ...[]byte) [32]byte {
	m := hmac.New(wgNewHash, key)
	for _, p := range parts {
		m.Write(p)
	}
	var out [32]byte
	m.Sum(out[:0])
	return out
}

// wgKDF is Noise's HKDF over HMAC-BLAKE2s with n (1 to 3) outputs.
func wgKDF(ck [32]byte, ikm []byte, n int) (t1, t2, t3 [32]byte) {
	t0 := wgHMAC(ck[:], ikm)
	t1 = wgHMAC(t0[:], []byte{1})
	if n > 1 {
		t2 = wgHMAC(t0[:], t1[:], []byte{2})
	}
	if n > 2 {
		t3 = wgHMAC(t0[:], t2[:], []byte{3})
	}
	return t1, t2, t3
}

// wgNonce is the 96-bit AEAD nonce: four zero bytes, then the counter.
func wgNonce(counter uint64) [chacha20poly1305.NonceSize]byte {
	var n [chacha20poly1305.NonceSize]byte
	binary.LittleEndian.PutUint64(n[4:], counter)
	return n
}

// wgSeal is AEAD for the handshake fields (their counter is always 0).
func wgSeal(key [32]byte, plaintext, ad []byte) []byte {
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil
	}
	n := wgNonce(0)
	return aead.Seal(nil, n[:], plaintext, ad)
}

// wgOpen undoes wgSeal.
func wgOpen(key [32]byte, ciphertext, ad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil, err
	}
	n := wgNonce(0)
	return aead.Open(nil, n[:], ciphertext, ad)
}

// wgDH is X25519. crypto/ecdh refuses an all-zero result (a low-order public
// key), which WireGuard requires.
func wgDH(priv *ecdh.PrivateKey, pub []byte) ([32]byte, error) {
	var out [32]byte
	if priv == nil {
		return out, errWGBadKey
	}
	p, err := ecdh.X25519().NewPublicKey(pub)
	if err != nil {
		return out, errWGBadKey
	}
	shared, err := priv.ECDH(p)
	if err != nil {
		return out, errWGBadKey
	}
	copy(out[:], shared)
	return out, nil
}

// wgTAI64N is the handshake timestamp for t: TAI64 seconds and nanoseconds
// rounded down to 2^24 ns, the way the kernel whitens them.
func wgTAI64N(t time.Time) [12]byte {
	var ts [12]byte
	secs := t.Unix()
	if secs < 0 {
		secs = 0
	}
	binary.BigEndian.PutUint64(ts[:8], wgTAI64Base+uint64(secs))
	binary.BigEndian.PutUint32(ts[8:], uint32(t.Nanosecond())&^(1<<24-1))
	return ts
}

// wgIdentity is the static half of a tunnel: our key, the server's, the
// optional preshared key, and the values derived from them once.
type wgIdentity struct {
	priv      *ecdh.PrivateKey
	public    [32]byte
	peer      [32]byte
	psk       [32]byte
	ss        [32]byte // DH(our static, peer static)
	mac1Peer  [32]byte // keys the mac1 of what we send
	mac1Self  [32]byte // checks the mac1 of what we receive
	cookieKey [32]byte // opens the peer's cookie replies
}

func newWGIdentity(priv, peer, psk [32]byte) (*wgIdentity, error) {
	key, err := ecdh.X25519().NewPrivateKey(priv[:])
	if err != nil {
		return nil, errWGBadKey
	}
	id := &wgIdentity{priv: key, peer: peer, psk: psk}
	copy(id.public[:], key.PublicKey().Bytes())
	ss, err := wgDH(key, peer[:])
	if err != nil {
		return nil, errors.New("the server's PublicKey is not a usable key")
	}
	id.ss = ss
	id.mac1Peer = wgHash(wgLabelMAC1, peer[:])
	id.mac1Self = wgHash(wgLabelMAC1, id.public[:])
	id.cookieKey = wgHash(wgLabelCookie, peer[:])
	return id, nil
}

// wgHandshakeState is where the handshake in progress stands.
type wgHandshakeState int

const (
	wgHSZeroed             wgHandshakeState = iota
	wgHSInitiationCreated                   // we started one and wait for the answer
	wgHSInitiationConsumed                  // the peer started one and we owe the answer
)

// wgNoise is the handshake state for the one peer. The device (wg_device.go)
// owns it and calls it under its lock.
type wgNoise struct {
	id *wgIdentity

	state       wgHandshakeState
	hash        [32]byte
	chainKey    [32]byte
	ephemeral   *ecdh.PrivateKey
	localIndex  uint32
	remoteIndex uint32
	remoteEph   [32]byte

	// latestTimestamp is the newest initiation timestamp the peer sent, and
	// lastConsumed when we took it: an older or equal one is a replay, and
	// one within wgInitiationMinGap a flood.
	latestTimestamp [12]byte
	lastConsumed    time.Time

	// cookie is the peer's last cookie (mac2 key) and cookieAt when it came;
	// lastMAC1 is the mac1 of our last handshake message, which a cookie
	// reply is bound to, and lastSentIndex that message's sender index.
	cookie        [16]byte
	cookieAt      time.Time
	lastMAC1      [16]byte
	lastSentIndex uint32
	sentHandshake bool
}

// zero forgets the handshake in progress (not the replay and cookie state,
// which outlive handshakes).
func (n *wgNoise) zero() {
	n.state = wgHSZeroed
	n.hash = [32]byte{}
	n.chainKey = [32]byte{}
	n.ephemeral = nil
	n.remoteEph = [32]byte{}
}

// addMACs fills mac1 (keyed with the peer's public key) and, while the peer's
// cookie is fresh, mac2 into the last 32 bytes of a handshake message.
func (n *wgNoise) addMACs(msg []byte, index uint32, now time.Time) {
	m1 := len(msg) - 32
	mac1 := wgMAC(n.id.mac1Peer[:], msg[:m1])
	copy(msg[m1:m1+16], mac1[:])
	var mac2 [16]byte
	if !n.cookieAt.IsZero() && now.Sub(n.cookieAt) < wgCookieLifetime {
		mac2 = wgMAC(n.cookie[:], msg[:m1+16])
	}
	copy(msg[m1+16:], mac2[:])
	n.lastMAC1 = mac1
	n.lastSentIndex = index
	n.sentHandshake = true
}

// mac1OK checks the mac1 of a handshake message addressed to us.
func (n *wgNoise) mac1OK(msg []byte) bool {
	m1 := len(msg) - 32
	want := wgMAC(n.id.mac1Self[:], msg[:m1])
	return subtle.ConstantTimeCompare(want[:], msg[m1:m1+16]) == 1
}

// initiation builds a handshake initiation from the ephemeral key, sender
// index and timestamp it is given (the device draws fresh ones; the tests
// replay recorded ones).
func (n *wgNoise) initiation(eph *ecdh.PrivateKey, index uint32, ts [12]byte, now time.Time) ([]byte, error) {
	if eph == nil {
		return nil, errWGBadKey
	}
	id := n.id
	msg := make([]byte, wgInitiationSize)
	binary.LittleEndian.PutUint32(msg[0:4], wgMsgInitiation)
	binary.LittleEndian.PutUint32(msg[4:8], index)

	ck := wgInitialChainKey
	h := wgHash(wgInitialHash[:], id.peer[:])
	ephPub := eph.PublicKey().Bytes()
	copy(msg[8:40], ephPub)
	h = wgHash(h[:], ephPub)
	ck, _, _ = wgKDF(ck, ephPub, 1)
	es, err := wgDH(eph, id.peer[:])
	if err != nil {
		return nil, err
	}
	var k [32]byte
	ck, k, _ = wgKDF(ck, es[:], 2)
	encStatic := wgSeal(k, id.public[:], h[:])
	copy(msg[40:88], encStatic)
	h = wgHash(h[:], encStatic)
	ck, k, _ = wgKDF(ck, id.ss[:], 2)
	encTS := wgSeal(k, ts[:], h[:])
	copy(msg[88:116], encTS)
	h = wgHash(h[:], encTS)

	n.addMACs(msg, index, now)
	n.state = wgHSInitiationCreated
	n.hash, n.chainKey = h, ck
	n.ephemeral = eph
	n.localIndex = index
	return msg, nil
}

// takeResponse checks the peer's answer to our initiation and returns the
// session's keys and both indices. The caller has checked mac1.
func (n *wgNoise) takeResponse(msg []byte) (send, recv [32]byte, local, remote uint32, err error) {
	if n.state != wgHSInitiationCreated || n.ephemeral == nil ||
		binary.LittleEndian.Uint32(msg[8:12]) != n.localIndex {
		err = errWGUnexpected
		return
	}
	id := n.id
	ck, h := n.chainKey, n.hash
	eph := msg[12:44]
	h = wgHash(h[:], eph)
	ck, _, _ = wgKDF(ck, eph, 1)
	ee, err1 := wgDH(n.ephemeral, eph)
	se, err2 := wgDH(id.priv, eph)
	if err1 != nil || err2 != nil {
		err = errWGBadMessage
		return
	}
	ck, _, _ = wgKDF(ck, ee[:], 1)
	ck, _, _ = wgKDF(ck, se[:], 1)
	var tau, k [32]byte
	ck, tau, k = wgKDF(ck, id.psk[:], 3)
	h = wgHash(h[:], tau[:])
	if _, e := wgOpen(k, msg[44:60], h[:]); e != nil {
		err = errWGBadMessage
		return
	}
	send, recv, _ = wgKDF(ck, nil, 2)
	local = n.localIndex
	remote = binary.LittleEndian.Uint32(msg[4:8])
	n.zero()
	return send, recv, local, remote, nil
}

// takeInitiation checks a handshake the peer started: it must come from our
// peer's static key, carry a newer timestamp than the last one, and not
// arrive in a flood. The caller has checked mac1.
func (n *wgNoise) takeInitiation(msg []byte, now time.Time) error {
	id := n.id
	ck := wgInitialChainKey
	h := wgHash(wgInitialHash[:], id.public[:])
	eph := msg[8:40]
	h = wgHash(h[:], eph)
	ck, _, _ = wgKDF(ck, eph, 1)
	es, err := wgDH(id.priv, eph)
	if err != nil {
		return errWGBadMessage
	}
	var k [32]byte
	ck, k, _ = wgKDF(ck, es[:], 2)
	static, err := wgOpen(k, msg[40:88], h[:])
	if err != nil || subtle.ConstantTimeCompare(static, id.peer[:]) != 1 {
		return errWGBadMessage
	}
	h = wgHash(h[:], msg[40:88])
	ck, k, _ = wgKDF(ck, id.ss[:], 2)
	ts, err := wgOpen(k, msg[88:116], h[:])
	if err != nil || len(ts) != len(n.latestTimestamp) {
		return errWGBadMessage
	}
	h = wgHash(h[:], msg[88:116])
	if bytes.Compare(ts, n.latestTimestamp[:]) <= 0 {
		return errWGReplay
	}
	if !n.lastConsumed.IsZero() && now.Sub(n.lastConsumed) < wgInitiationMinGap {
		return errWGFlood
	}
	copy(n.latestTimestamp[:], ts)
	n.lastConsumed = now
	n.state = wgHSInitiationConsumed
	n.hash, n.chainKey = h, ck
	n.ephemeral = nil
	copy(n.remoteEph[:], eph)
	n.remoteIndex = binary.LittleEndian.Uint32(msg[4:8])
	return nil
}

// response answers the initiation takeInitiation accepted, with the ephemeral
// key and sender index it is given, and returns the session's keys.
func (n *wgNoise) response(eph *ecdh.PrivateKey, index uint32, now time.Time) (msg []byte, send, recv [32]byte, remote uint32, err error) {
	if n.state != wgHSInitiationConsumed || eph == nil {
		err = errWGUnexpected
		return
	}
	id := n.id
	msg = make([]byte, wgResponseSize)
	binary.LittleEndian.PutUint32(msg[0:4], wgMsgResponse)
	binary.LittleEndian.PutUint32(msg[4:8], index)
	binary.LittleEndian.PutUint32(msg[8:12], n.remoteIndex)

	ck, h := n.chainKey, n.hash
	ephPub := eph.PublicKey().Bytes()
	copy(msg[12:44], ephPub)
	h = wgHash(h[:], ephPub)
	ck, _, _ = wgKDF(ck, ephPub, 1)
	ee, err1 := wgDH(eph, n.remoteEph[:])
	se, err2 := wgDH(eph, id.peer[:])
	if err1 != nil || err2 != nil {
		return nil, send, recv, 0, errWGBadMessage
	}
	ck, _, _ = wgKDF(ck, ee[:], 1)
	ck, _, _ = wgKDF(ck, se[:], 1)
	var tau, k [32]byte
	ck, tau, k = wgKDF(ck, id.psk[:], 3)
	h = wgHash(h[:], tau[:])
	copy(msg[44:60], wgSeal(k, nil, h[:]))

	n.addMACs(msg, index, now)
	recv, send, _ = wgKDF(ck, nil, 2)
	remote = n.remoteIndex
	n.zero()
	return msg, send, recv, remote, nil
}

// takeCookie stores the cookie in a cookie reply to our last handshake
// message; the next initiation carries mac2 with it.
func (n *wgNoise) takeCookie(msg []byte, now time.Time) error {
	if !n.sentHandshake || binary.LittleEndian.Uint32(msg[4:8]) != n.lastSentIndex {
		return errWGUnexpected
	}
	aead, err := chacha20poly1305.NewX(n.id.cookieKey[:])
	if err != nil {
		return err
	}
	cookie, err := aead.Open(nil, msg[8:32], msg[32:64], n.lastMAC1[:])
	if err != nil || len(cookie) != len(n.cookie) {
		return errWGBadMessage
	}
	copy(n.cookie[:], cookie)
	n.cookieAt = now
	return nil
}
