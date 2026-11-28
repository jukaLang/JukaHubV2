package main

// ctlmode_wifi_proto.go — JukaPad, the controller protocol between the
// handheld and the receiver on a PC (player/ctlmode/jukapad_receiver.py):
// the packets and the key schedule, without sockets. ctlmode_wifi.go runs it.
// The receiver implements the same thing in Python; the two change together
// (ctlmode_wifi_test.go checks this side against the receiver's vectors).
//
// Transport: UDP. The receiver listens on 8320; the handheld uses 8323 (any
// port when that one is taken; 8321 and 8322 belong to Nearby devices), and
// serves the receiver's download page on TCP 8324. Every packet starts with
// "JP", the version (1) and a type byte; integers are little-endian; a name
// is one length byte and at most 32 bytes of UTF-8. "H" is the handheld,
// "R" the receiver; hid and rid are their random 16-byte ids, kept for good.
//
// Discovery (not secret, not authenticated):
//
//	01 PROBE      H→R  hid name — to 255.255.255.255, each interface's
//	                   directed broadcast and the last address of every paired
//	                   PC, once a second while nothing is connected
//	02 ANNOUNCE   R→H  rid hid flags name — the answer (flags bit 0: R holds a
//	                   key for hid). A receiver started with --tsp IP also
//	                   sends one to IP:8323 every 2 s with hid all zero; the
//	                   handheld answers that with a unicast PROBE, so networks
//	                   that drop broadcasts still work.
//
// Pairing, the first time (H initiates; R commits first; numeric comparison
// as in Bluetooth's Secure Simple Pairing):
//
//	10 PAIR_HELLO   H→R  pid hid PKa name   (pid: 8 random bytes)
//	11 PAIR_COMMIT  R→H  pid rid PKb Cb     Cb = SHA-256("jukapad-commit-v1" PKb PKa Nb)
//	12 PAIR_NONCE   H→R  pid Na             (32 bytes, sent only after Cb)
//	13 PAIR_REVEAL  R→H  pid Nb             H checks Cb
//	                both show code = SHA-256("jukapad-code-v1" PKa PKb Na Nb)[0:4]
//	                as a big-endian number mod 1 000 000, six digits
//	14 PAIR_CONFIRM H→R  pid HMAC(LTK, "jukapad-confirm-h" pid)[0:16] — the
//	                     handheld's user said the codes match
//	15 PAIR_DONE    R→H  pid HMAC(LTK, "jukapad-confirm-r" pid)[0:16] — the
//	                     PC's user did too; R has stored the key, H stores it
//	16 PAIR_FAIL    either  pid reason (1 refused, 2 busy, 3 tampered,
//	                     4 timed out)
//
// PKa/PKb are fresh X25519 keys; Z = X25519(a, PKb); LTK = HKDF-SHA256(Z,
// salt Na‖Nb, info "jukapad-ltk-v1" hid rid PKa PKb, 32 bytes). Every request
// is resent until answered and both sides answer duplicates with the same
// reply. R fixes Nb (in Cb) before it sees Na, and H sends Na only after Cb,
// so a man in the middle has to fix its nonces in both halves before it can
// know either code: the screens match by chance one time in a million, and
// each try needs both users to say yes. PAIR_FAIL is not authenticated: a
// stranger on the network can cancel a pairing, nothing more.
//
// Reconnecting to a paired PC (no prompt; fresh nonces both ways):
//
//	20 HELLO      H→R  hid rid Na                (16 bytes)
//	21 CHALLENGE  R→H  rid hid Na Nb proofR       proofR = HMAC(LTK, "jukapad-proof-r" hid rid Na Nb)[0:16]
//	22 NOT_PAIRED R→H  rid hid                    R has forgotten hid: pair again
//	23 PROOF      H→R  hid Nb proofH              proofH likewise with "jukapad-proof-h"
//	SK  = HMAC(LTK, "jukapad-session-v1" hid rid Na Nb)
//	sid = HMAC(SK, "jukapad-sid")[0:4] little-endian (1 if 0)
//
// The session (each packet ends with HMAC-SHA256(SK, everything before)[0:8];
// each direction counts its own sequence and accepts only increasing ones):
//
//	30 STATE      H→R  sid seq buttons(u32) LX LY RX RY(i16) LT RT(u8) mac — 34 bytes;
//	                   sent on every change and repeated (every 50 ms for a
//	                   second after a change or while anything is held, every
//	                   250 ms at rest). R releases everything after 1 s without one.
//	31 FEEDBACK   R→H  sid seq large small(u8) ms(u16) mac — 24 bytes; rumble
//	                   (ms 0 = motors off) and the keepalive: sent when the
//	                   session starts, when the motors change, every 200 ms
//	                   while they run and every second otherwise. H calls the
//	                   link down after 3 s without one.
//	3E BYE        H→R  sid seq mac — 20 bytes; R releases everything
//	3F BYE        R→H  sid seq mac — the receiver is closing
//
// Buttons are ctlBtn* bits; sticks use SDL's convention (+Y down).

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	jpadVersion      = 1
	jpadReceiverPort = 8320
	jpadHandheldPort = 8323
	jpadHTTPPort     = 8324
)

// Packet types.
const (
	jpadProbe       byte = 0x01
	jpadAnnounce    byte = 0x02
	jpadPairHello   byte = 0x10
	jpadPairCommit  byte = 0x11
	jpadPairNonce   byte = 0x12
	jpadPairReveal  byte = 0x13
	jpadPairConfirm byte = 0x14
	jpadPairDone    byte = 0x15
	jpadPairFail    byte = 0x16
	jpadHello       byte = 0x20
	jpadChallenge   byte = 0x21
	jpadNotPaired   byte = 0x22
	jpadProof       byte = 0x23
	jpadState       byte = 0x30
	jpadFeedback    byte = 0x31
	jpadByeH        byte = 0x3E
	jpadByeR        byte = 0x3F
)

// PAIR_FAIL reasons.
const (
	jpadFailRefused byte = 1
	jpadFailBusy    byte = 2
	jpadFailTamper  byte = 3
	jpadFailTimeout byte = 4
)

const (
	jpadIDLen        = 16
	jpadPIDLen       = 8
	jpadKeyLen       = 32
	jpadPairNonceLen = 32
	jpadNonceLen     = 16
	jpadTagLen       = 16
	jpadMACLen       = 8
	jpadStateLen     = 34
	jpadFeedbackLen  = 24
	jpadByeLen       = 20
	jpadNameMax      = 32
)

func jpadHeader(t byte) []byte { return []byte{'J', 'P', jpadVersion, t} }

// jpadType is a packet's type, or 0 when it is not JukaPad version 1.
func jpadType(pkt []byte) byte {
	if len(pkt) < 4 || pkt[0] != 'J' || pkt[1] != 'P' || pkt[2] != jpadVersion {
		return 0
	}
	return pkt[3]
}

func jpadCat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// jpadCopy returns a copy of b[off:off+n] (nil when b is too short).
func jpadCopy(b []byte, off, n int) []byte {
	if off < 0 || off+n > len(b) {
		return nil
	}
	return append([]byte(nil), b[off:off+n]...)
}

// jpadPackName is a name on the wire: a length byte and at most 32 bytes,
// cut on a character boundary.
func jpadPackName(name string) []byte {
	raw := []byte(name)
	if len(raw) > jpadNameMax {
		raw = raw[:jpadNameMax]
		for len(raw) > 0 && !utf8.Valid(raw) {
			raw = raw[:len(raw)-1]
		}
	}
	return append([]byte{byte(len(raw))}, raw...)
}

// jpadReadName reads a name at off; control characters are dropped.
func jpadReadName(b []byte, off int) string {
	if off >= len(b) {
		return ""
	}
	n := int(b[off])
	end := off + 1 + n
	if end > len(b) {
		end = len(b)
	}
	s := strings.ToValidUTF8(string(b[off+1:end]), "?")
	s = strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > jpadNameMax {
		s = string(r[:jpadNameMax])
	}
	return s
}

// ── Key schedule ─────────────────────────────────────────────────────────

func jpadSHA(parts ...[]byte) []byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

func jpadHMAC(key []byte, parts ...[]byte) []byte {
	m := hmac.New(sha256.New, key)
	for _, p := range parts {
		m.Write(p)
	}
	return m.Sum(nil)
}

// jpadCommit is the receiver's commitment to its pairing nonce.
func jpadCommit(pkb, pka, nb []byte) []byte {
	return jpadSHA([]byte("jukapad-commit-v1"), pkb, pka, nb)
}

// jpadPairCode is the six digits both screens show.
func jpadPairCode(pka, pkb, na, nb []byte) string {
	sum := jpadSHA([]byte("jukapad-code-v1"), pka, pkb, na, nb)
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(sum[:4])%1000000)
}

// jpadDeriveLTK is the long-term key a pairing leaves on both sides.
func jpadDeriveLTK(z, na, nb, hid, rid, pka, pkb []byte) ([]byte, error) {
	info := jpadCat([]byte("jukapad-ltk-v1"), hid, rid, pka, pkb)
	return hkdf.Key(sha256.New, z, jpadCat(na, nb), string(info), jpadKeyLen)
}

// jpadConfirmTag proves a side holds the new key once its user said yes
// (who is "h" for the handheld, "r" for the receiver).
func jpadConfirmTag(ltk []byte, who string, pid []byte) []byte {
	return jpadHMAC(ltk, []byte("jukapad-confirm-"+who), pid)[:jpadTagLen]
}

// jpadProofTag is a side's proof of the paired key on reconnect.
func jpadProofTag(ltk []byte, who string, hid, rid, na, nb []byte) []byte {
	return jpadHMAC(ltk, []byte("jukapad-proof-"+who), hid, rid, na, nb)[:jpadTagLen]
}

// jpadSessionKey keys one connection.
func jpadSessionKey(ltk, hid, rid, na, nb []byte) []byte {
	return jpadHMAC(ltk, []byte("jukapad-session-v1"), hid, rid, na, nb)
}

// jpadSessionID names a connection in every session packet.
func jpadSessionID(sk []byte) uint32 {
	sid := binary.LittleEndian.Uint32(jpadHMAC(sk, []byte("jukapad-sid"))[:4])
	if sid == 0 {
		sid = 1
	}
	return sid
}

// jpadFormatCode is a code the way it is shown: "123 456".
func jpadFormatCode(code string) string {
	if len(code) == 6 {
		return code[:3] + " " + code[3:]
	}
	return code
}

// ── Discovery and pairing packets ────────────────────────────────────────

func jpadPackProbe(hid []byte, name string) []byte {
	return jpadCat(jpadHeader(jpadProbe), hid, jpadPackName(name))
}

// jpadAnnounceMsg is a receiver's answer to a probe.
type jpadAnnounceMsg struct {
	RID, HID []byte
	Paired   bool
	Name     string
}

func jpadParseAnnounce(pkt []byte) (jpadAnnounceMsg, bool) {
	if jpadType(pkt) != jpadAnnounce || len(pkt) < 4+2*jpadIDLen+2 {
		return jpadAnnounceMsg{}, false
	}
	return jpadAnnounceMsg{
		RID:    jpadCopy(pkt, 4, jpadIDLen),
		HID:    jpadCopy(pkt, 4+jpadIDLen, jpadIDLen),
		Paired: pkt[4+2*jpadIDLen]&1 != 0,
		Name:   jpadReadName(pkt, 5+2*jpadIDLen),
	}, true
}

func jpadPackPairHello(pid, hid, pka []byte, name string) []byte {
	return jpadCat(jpadHeader(jpadPairHello), pid, hid, pka, jpadPackName(name))
}

// jpadCommitMsg is PAIR_COMMIT.
type jpadCommitMsg struct {
	PID, RID, PKB, C []byte
}

func jpadParseCommit(pkt []byte) (jpadCommitMsg, bool) {
	if jpadType(pkt) != jpadPairCommit || len(pkt) < 4+jpadPIDLen+jpadIDLen+2*jpadKeyLen {
		return jpadCommitMsg{}, false
	}
	off := 4
	m := jpadCommitMsg{PID: jpadCopy(pkt, off, jpadPIDLen)}
	off += jpadPIDLen
	m.RID = jpadCopy(pkt, off, jpadIDLen)
	off += jpadIDLen
	m.PKB = jpadCopy(pkt, off, jpadKeyLen)
	off += jpadKeyLen
	m.C = jpadCopy(pkt, off, jpadKeyLen)
	return m, true
}

func jpadPackPairNonce(pid, na []byte) []byte {
	return jpadCat(jpadHeader(jpadPairNonce), pid, na)
}

// jpadParsePID splits a pairing packet of type t into its pid and the n
// bytes after it (PAIR_REVEAL: Nb; PAIR_DONE: the tag; PAIR_FAIL: reason).
func jpadParsePID(pkt []byte, t byte, n int) (pid, rest []byte, ok bool) {
	if jpadType(pkt) != t || len(pkt) < 4+jpadPIDLen+n {
		return nil, nil, false
	}
	return jpadCopy(pkt, 4, jpadPIDLen), jpadCopy(pkt, 4+jpadPIDLen, n), true
}

func jpadPackPairConfirm(pid, tag []byte) []byte {
	return jpadCat(jpadHeader(jpadPairConfirm), pid, tag)
}

func jpadPackPairFail(pid []byte, reason byte) []byte {
	return jpadCat(jpadHeader(jpadPairFail), pid, []byte{reason})
}

// ── Reconnect packets ────────────────────────────────────────────────────

func jpadPackHello(hid, rid, na []byte) []byte {
	return jpadCat(jpadHeader(jpadHello), hid, rid, na)
}

// jpadChallengeMsg is CHALLENGE.
type jpadChallengeMsg struct {
	RID, HID, NA, NB, Proof []byte
}

func jpadParseChallenge(pkt []byte) (jpadChallengeMsg, bool) {
	if jpadType(pkt) != jpadChallenge || len(pkt) < 4+2*jpadIDLen+2*jpadNonceLen+jpadTagLen {
		return jpadChallengeMsg{}, false
	}
	return jpadChallengeMsg{
		RID:   jpadCopy(pkt, 4, jpadIDLen),
		HID:   jpadCopy(pkt, 4+jpadIDLen, jpadIDLen),
		NA:    jpadCopy(pkt, 4+2*jpadIDLen, jpadNonceLen),
		NB:    jpadCopy(pkt, 4+2*jpadIDLen+jpadNonceLen, jpadNonceLen),
		Proof: jpadCopy(pkt, 4+2*jpadIDLen+2*jpadNonceLen, jpadTagLen),
	}, true
}

// jpadParseNotPaired returns the rid and hid of a NOT_PAIRED.
func jpadParseNotPaired(pkt []byte) (rid, hid []byte, ok bool) {
	if jpadType(pkt) != jpadNotPaired || len(pkt) < 4+2*jpadIDLen {
		return nil, nil, false
	}
	return jpadCopy(pkt, 4, jpadIDLen), jpadCopy(pkt, 4+jpadIDLen, jpadIDLen), true
}

func jpadPackProof(hid, nb, tag []byte) []byte {
	return jpadCat(jpadHeader(jpadProof), hid, nb, tag)
}

// ── Session packets ──────────────────────────────────────────────────────

// jpadSeal appends the session MAC.
func jpadSeal(sk, body []byte) []byte {
	return append(body, jpadHMAC(sk, body)[:jpadMACLen]...)
}

// jpadOpen checks a session packet of type t and length n and returns its
// fields after the header (sid first), MAC removed.
func jpadOpen(sk, pkt []byte, t byte, n int) ([]byte, bool) {
	if len(pkt) != n || jpadType(pkt) != t {
		return nil, false
	}
	body, mac := pkt[:n-jpadMACLen], pkt[n-jpadMACLen:]
	if !hmac.Equal(jpadHMAC(sk, body)[:jpadMACLen], mac) {
		return nil, false
	}
	return body[4:], true
}

// jpadPacketSID is the session id of a session packet (0 when too short).
func jpadPacketSID(pkt []byte) uint32 {
	if len(pkt) < 8 {
		return 0
	}
	return binary.LittleEndian.Uint32(pkt[4:8])
}

func jpadPackState(sk []byte, sid, seq uint32, st ctlPadState) []byte {
	b := make([]byte, 0, jpadStateLen)
	b = append(b, jpadHeader(jpadState)...)
	b = binary.LittleEndian.AppendUint32(b, sid)
	b = binary.LittleEndian.AppendUint32(b, seq)
	b = binary.LittleEndian.AppendUint32(b, st.Buttons)
	for _, v := range [4]int16{st.LX, st.LY, st.RX, st.RY} {
		b = binary.LittleEndian.AppendUint16(b, uint16(v))
	}
	b = append(b, st.LT, st.RT)
	return jpadSeal(sk, b)
}

// jpadUnpackState is the receiver's side of STATE (the tests use it).
func jpadUnpackState(sk, pkt []byte) (sid, seq uint32, st ctlPadState, ok bool) {
	f, ok := jpadOpen(sk, pkt, jpadState, jpadStateLen)
	if !ok {
		return 0, 0, ctlPadState{}, false
	}
	le := binary.LittleEndian
	st = ctlPadState{
		Buttons: le.Uint32(f[8:]),
		LX:      int16(le.Uint16(f[12:])),
		LY:      int16(le.Uint16(f[14:])),
		RX:      int16(le.Uint16(f[16:])),
		RY:      int16(le.Uint16(f[18:])),
		LT:      f[20],
		RT:      f[21],
	}
	return le.Uint32(f), le.Uint32(f[4:]), st, true
}

// jpadFeedbackMsg is FEEDBACK: rumble and the receiver's keepalive.
type jpadFeedbackMsg struct {
	SID, Seq     uint32
	Large, Small uint8
	MS           uint16
}

// Tests call this seam directly; it is kept for their table cases.
func jpadPackFeedback(sk []byte, m jpadFeedbackMsg) []byte {
	b := make([]byte, 0, jpadFeedbackLen)
	b = append(b, jpadHeader(jpadFeedback)...)
	b = binary.LittleEndian.AppendUint32(b, m.SID)
	b = binary.LittleEndian.AppendUint32(b, m.Seq)
	b = append(b, m.Large, m.Small)
	b = binary.LittleEndian.AppendUint16(b, m.MS)
	return jpadSeal(sk, b)
}

func jpadUnpackFeedback(sk, pkt []byte) (jpadFeedbackMsg, bool) {
	f, ok := jpadOpen(sk, pkt, jpadFeedback, jpadFeedbackLen)
	if !ok {
		return jpadFeedbackMsg{}, false
	}
	le := binary.LittleEndian
	return jpadFeedbackMsg{SID: le.Uint32(f), Seq: le.Uint32(f[4:]), Large: f[8], Small: f[9], MS: le.Uint16(f[10:])}, true
}

// jpadPackBye is BYE of type t (jpadByeH from the handheld, jpadByeR from
// the receiver).
func jpadPackBye(sk []byte, t byte, sid, seq uint32) []byte {
	b := make([]byte, 0, jpadByeLen)
	b = append(b, jpadHeader(t)...)
	b = binary.LittleEndian.AppendUint32(b, sid)
	b = binary.LittleEndian.AppendUint32(b, seq)
	return jpadSeal(sk, b)
}

func jpadUnpackBye(sk []byte, t byte, pkt []byte) (sid, seq uint32, ok bool) {
	f, ok := jpadOpen(sk, pkt, t, jpadByeLen)
	if !ok {
		return 0, 0, false
	}
	return binary.LittleEndian.Uint32(f), binary.LittleEndian.Uint32(f[4:]), true
}
