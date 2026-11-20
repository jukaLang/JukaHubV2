package main

import (
	"bytes"
	"crypto/ecdh"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"testing"
)

// TestJukaPadVectors checks the handheld's side of the protocol against
// `python3 player/ctlmode/jukapad_receiver.py --vectors`: the two must agree
// byte for byte (fixed keys are RFC 7748's Alice and Bob).
func TestJukaPadVectors(t *testing.T) {
	h := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	rep := func(b byte, n int) []byte { return bytes.Repeat([]byte{b}, n) }
	check := func(name string, got []byte, want string) {
		t.Helper()
		if hex.EncodeToString(got) != want {
			t.Errorf("%s = %x, want %s", name, got, want)
		}
	}
	a, err := ecdh.X25519().NewPrivateKey(h("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ecdh.X25519().NewPrivateKey(h("5dab087e624a8a4b79e17f8b83800ee66f3bb1292618b6fd1c2f8b27ff88e0eb"))
	if err != nil {
		t.Fatal(err)
	}
	pka, pkb := a.PublicKey().Bytes(), b.PublicKey().Bytes()
	z, err := a.ECDH(b.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	hid, rid, pid := rep(1, 16), rep(2, 16), rep(3, 8)
	na, nb := rep(4, 32), rep(5, 32)

	check("commit", jpadCommit(pkb, pka, nb), "65e4ce47150af284b43932ed419896a737e89e5e8c16e5122ec15ecf99a65167")
	if code := jpadPairCode(pka, pkb, na, nb); code != "375742" {
		t.Errorf("code = %s, want 375742", code)
	}
	ltk, err := jpadDeriveLTK(z, na, nb, hid, rid, pka, pkb)
	if err != nil {
		t.Fatal(err)
	}
	check("ltk", ltk, "68b894725f74398cc5caa2e05883fd61ad6d94d7ec0f50ea424fdbe67c0c7389")
	check("confirmH", jpadConfirmTag(ltk, "h", pid), "538917b8c18760ed564347ae47086fbe")
	check("confirmR", jpadConfirmTag(ltk, "r", pid), "6bca50ae549b93c368936434d2a72221")

	na2, nb2 := rep(6, 16), rep(7, 16)
	check("proofR", jpadProofTag(ltk, "r", hid, rid, na2, nb2), "87a3adbe2b6a853aef31de7d5c367662")
	check("proofH", jpadProofTag(ltk, "h", hid, rid, na2, nb2), "1d9db1e042cd617b49acd01308730548")
	sk := jpadSessionKey(ltk, hid, rid, na2, nb2)
	check("sessionKey", sk, "2b74764c7b337a0daac2fb826ba89593e9fa72478ba3656f7c91d04931346f6a")
	sid := jpadSessionID(sk)
	if sid != 3195980933 {
		t.Errorf("sid = %d, want 3195980933", sid)
	}

	const stateHex = "4a50013085cc7ebe0700000001240000ff7f00802efbe110ff03ecb124d20847b0c3"
	st := ctlPadState{Buttons: ctlBtnA | ctlBtnUp | ctlBtnGuide, LX: 32767, LY: -32768, RX: -1234, RY: 4321, LT: 255, RT: 3}
	check("state", jpadPackState(sk, sid, 7, st), stateHex)
	if gotSID, seq, got, ok := jpadUnpackState(sk, h(stateHex)); !ok || gotSID != sid || seq != 7 || got != st {
		t.Errorf("state decode = %v %d %d %+v", ok, gotSID, seq, got)
	}
	bad := h(stateHex)
	bad[12] ^= 1
	if _, _, _, ok := jpadUnpackState(sk, bad); ok {
		t.Error("a changed state packet passed its MAC")
	}

	const feedbackHex = "4a50013185cc7ebe09000000c8112c01c9341e9cd2bf89cd"
	fb := jpadFeedbackMsg{SID: sid, Seq: 9, Large: 200, Small: 17, MS: 300}
	check("feedback", jpadPackFeedback(sk, fb), feedbackHex)
	if got, ok := jpadUnpackFeedback(sk, h(feedbackHex)); !ok || got != fb {
		t.Errorf("feedback decode = %v %+v", ok, got)
	}
	const byeHex = "4a50013e85cc7ebe08000000422c6ac4cc71a0c5"
	check("byeH", jpadPackBye(sk, jpadByeH, sid, 8), byeHex)
	if _, _, ok := jpadUnpackBye(sk, jpadByeR, h(byeHex)); ok {
		t.Error("the handheld's own BYE passed as the receiver's")
	}

	dsu := ctlPadState{Buttons: ctlBtnA | ctlBtnUp | ctlBtnStart | ctlBtnRB | ctlBtnGuide | ctlBtnRT,
		LX: 32767, LY: -32768, RX: -12345, RY: 4000, LT: 200}
	pkt := dsuDataPacket(0x11223344, [6]byte{0x02, 0x4A, 0x55, 0x4B, 0x41, 0x01}, 5, 5, dsu, 123456789)
	if len(pkt) != dsuDataLen {
		t.Errorf("DSU data packet is %d bytes, want %d", len(pkt), dsuDataLen)
	}
	// Xbox A is the bottom face button: Cross, 0x40 in byte 17 and the second
	// face-button pressure (cemuhook's "B", named the Nintendo way).
	check("dsu", pkt, "44535553e90354004aae1e3a443322110200100000020100024a554b4101050105000000184a0100ffff5071"+
		"000000ff00ff0000ff0000c800000000000000000000000015cd5b07000000000000000000000000000000000000"+
		"00000000000000000000")
}

// TestDSURequest checks a client's subscription parses and a bad CRC does not.
func TestDSURequest(t *testing.T) {
	le := binary.LittleEndian
	req := []byte("DSUC")
	req = le.AppendUint16(req, dsuProtocol)
	req = le.AppendUint16(req, 12)
	req = le.AppendUint32(req, 0)
	req = le.AppendUint32(req, 42)
	req = le.AppendUint32(req, dsuMsgData)
	req = append(req, 1, 0, 0, 0, 0, 0, 0, 0)
	le.PutUint32(req[8:], crc32.ChecksumIEEE(req))
	msg, body, ok := dsuParseRequest(req)
	if !ok || msg != dsuMsgData || len(body) != 8 || body[0] != 1 {
		t.Fatalf("parse = %v %#x %v", ok, msg, body)
	}
	req[20] ^= 2
	if _, _, ok := dsuParseRequest(req); ok {
		t.Error("a request with a wrong CRC was accepted")
	}
}
