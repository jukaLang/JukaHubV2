package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"net"
	"strings"
	"testing"
)

func smbTestHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The server's NTLMv2 check against MS-NLMP 4.2.4 (user "User", domain
// "Domain", password "Password", time 0).
func TestSMBServeNTLMv2Vectors(t *testing.T) {
	key := smb2NTOWFv2("Password", "User", "Domain")
	if !bytes.Equal(key, smbTestHex(t, "0c868a403bfd7a93a3001ef22ef02e3f")) {
		t.Fatalf("NTOWFv2 = %x", key)
	}
	challenge := smbTestHex(t, "0123456789abcdef")
	var ti []byte
	ti = smbSrvAvPair(ti, 2, smb2UTF16("Domain"))
	ti = smbSrvAvPair(ti, 1, smb2UTF16("Server"))
	ti = smbSrvAvPair(ti, 0, nil)
	nt, _, _ := smb2NTLMv2Response(key, challenge, smbTestHex(t, "aaaaaaaaaaaaaaaa"), 0, ti)
	if !bytes.Equal(nt[:16], smbTestHex(t, "68cd0ab851e51c96aabc927bebef6a1c")) {
		t.Fatalf("NTProofStr = %x", nt[:16])
	}
	base, ok := smbSrvNtlmV2Check("Password", "User", "Domain", challenge, nt)
	if !ok || !bytes.Equal(base, smbTestHex(t, "8de40ccadbc14a82f15cb0ad0de95ca3")) {
		t.Fatalf("session base key %x, ok %v", base, ok)
	}
	if _, ok := smbSrvNtlmV2Check("password", "User", "Domain", challenge, nt); ok {
		t.Fatal("a wrong password passed")
	}
}

// A full sign-in: this package's SMB client against the server, with key
// exchange; wrong passwords and anonymous sign-ins are refused.
func TestSMBServeNTLMRoundTrip(t *testing.T) {
	n, err := newSmbSrvNtlm("JUKAHUB")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := smb2ParseNtlmChallenge(n.challengeMsg(smb2NtlmClientFlags, smb2FileTimeNow()))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := smb2NtlmTimestamp(ch.targetInfo); !ok || ch.flags&smb2NtlmKeyExch == 0 {
		t.Fatalf("challenge flags %08x, timestamp %v", ch.flags, ok)
	}
	msg, key, err := smb2NtlmAuthenticateMsg(ch, "JukaHub", "k7fm2qxa9rte", "WORKGROUP", "PC")
	if err != nil {
		t.Fatal(err)
	}
	auth, err := n.verify(msg, smbServeUser, "k7fm2qxa9rte")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(auth.sessionKey, key) {
		t.Fatalf("session key %x, want %x", auth.sessionKey, key)
	}
	if _, err := n.verify(msg, smbServeUser, "k7fm2qxa9rtf"); err == nil {
		t.Fatal("a wrong password passed")
	}
	if _, err := n.verify(msg, "someone", "k7fm2qxa9rte"); err == nil {
		t.Fatal("another user passed")
	}
	anon, _, err := smb2NtlmAuthenticateMsg(ch, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.verify(anon, smbServeUser, "k7fm2qxa9rte"); err == nil {
		t.Fatal("an anonymous sign-in passed")
	}
}

// smbTestConn is a connection with one signed-in session and the card share
// connected as tree 1.
func smbTestConn(t *testing.T, key []byte) *smbSrvConn {
	t.Helper()
	signer, err := smb2NewSigner(smb2Dialect210, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := newSmbSrvConn(&smbService{}, nil, nil, "192.168.1.2", 1, "JUKAHUB", [16]byte{})
	c.negotiated, c.dialect = true, smb2Dialect210
	c.sessions[9] = &smbSrvSession{id: 9, valid: true, signer: signer, trees: map[uint32]*smbSrvTree{1: {id: 1}}}
	return c
}

// smbTestReq is a request header for session 9, tree 1, followed by body.
func smbTestReq(cmd uint16, body []byte) []byte {
	m := make([]byte, smb2HeaderLen, smb2HeaderLen+len(body))
	copy(m, "\xfeSMB")
	binary.LittleEndian.PutUint16(m[4:], smb2HeaderLen)
	binary.LittleEndian.PutUint16(m[12:], cmd)
	binary.LittleEndian.PutUint64(m[24:], 3)
	binary.LittleEndian.PutUint32(m[36:], 1)
	binary.LittleEndian.PutUint64(m[40:], 9)
	return append(m, body...)
}

// Requests must be signed with the session key; replies are signed, each
// over its own bytes in a compound, and a changed byte fails the check.
func TestSMBServeSigning(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 16)
	c := smbTestConn(t, key)
	client, _ := smb2NewSigner(smb2Dialect210, key, nil)
	echo := smbTestReq(smbSrvCmdEcho, smbSrvU16Body(4, 4))
	client.sign(echo)
	out, keep := c.handleFrame(echo)
	if !keep || len(out) < smb2HeaderLen || binary.LittleEndian.Uint32(out[8:]) != 0 ||
		binary.LittleEndian.Uint32(out[16:])&smb2FlagSigned == 0 || !client.verify(append([]byte(nil), out...)) {
		t.Fatalf("signed ECHO: keep %v, reply % x", keep, out)
	}
	out, _ = c.handleFrame(smbTestReq(smbSrvCmdEcho, smbSrvU16Body(4, 4)))
	if binary.LittleEndian.Uint32(out[8:]) != smb2StatusAccessDenied {
		t.Fatalf("unsigned request: status %08x", binary.LittleEndian.Uint32(out[8:]))
	}
	req := &smbSrvReq{cmd: smbSrvCmdEcho, mid: 5}
	frame := c.assemble([]smbSrvOut{
		{req: req, body: smbSrvU16Body(4, 4), signer: c.sessions[9].signer},
		{req: req, status: smb2StatusNotSupported, signer: c.sessions[9].signer},
	})
	msgs, ok := smbSrvSplitChain(frame)
	if !ok || len(msgs) != 2 {
		t.Fatalf("compound reply did not split: %v %d", ok, len(msgs))
	}
	for i, m := range msgs {
		if !client.verify(append([]byte(nil), m...)) {
			t.Fatalf("compound reply %d: bad signature", i)
		}
	}
	bad := append([]byte(nil), msgs[1]...)
	bad[len(bad)-1] ^= 1
	if client.verify(bad) {
		t.Fatal("a changed reply verified")
	}
}

// Paths: backslashes, the share root, the default data stream; "..",
// other streams, wildcards and control characters are refused.
func TestSMBServeCleanPath(t *testing.T) {
	good := map[string]string{
		"": ".", `\`: ".", `Roms\GBA\Game.gba`: "Roms/GBA/Game.gba", `\Saves\`: "Saves",
		`file.txt::$DATA`: "file.txt", `.hidden`: ".hidden",
	}
	for in, want := range good {
		if got, st := smbSrvCleanPath(in); st != 0 || got != want {
			t.Errorf("%q -> %q (%08x), want %q", in, got, st, want)
		}
	}
	for _, in := range []string{`..`, `Roms\..\..\etc`, `a\.\b`, `a\\b`, `x:stream`, `x:Zone.Identifier:$DATA`,
		"a\x00b", `*.gba`, `a|b`, strings.Repeat("n", 256)} {
		if got, st := smbSrvCleanPath(in); st == 0 {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
	for _, c := range []struct {
		pat, name string
		want      bool
	}{
		{"*", "x", true}, {"*.GBA", "game.gba", true}, {"game.gb?", "Game.gba", true},
		{"x*", "game", false}, {"desktop.ini", "Desktop.INI", true}, {"a*b*c", "aXbYc", true}, {"a*b*c", "aXbY", false},
	} {
		if got := smbSrvMatch(c.pat, c.name); got != c.want {
			t.Errorf("match(%q, %q) = %v", c.pat, c.name, got)
		}
	}
}

// Malformed input ends the connection or fails the request; it never
// reads past a message.
func TestSMBServeHostileFrames(t *testing.T) {
	if _, err := smbSrvReadFrame(bytes.NewReader([]byte{0, 0x7f, 0xff, 0xff})); err == nil {
		t.Error("a 8 MiB frame length was accepted")
	}
	if _, ok := smbSrvParseHeader([]byte("\xfeSMB\x40\x00")); ok {
		t.Error("a truncated header parsed")
	}
	m := smbTestReq(smbSrvCmdEcho, smbSrvU16Body(4, 16))
	for _, next := range []uint32{200, 66, 8} {
		binary.LittleEndian.PutUint32(m[20:], next)
		if _, ok := smbSrvSplitChain(m); ok {
			t.Errorf("NextCommand %d accepted", next)
		}
	}
	key := bytes.Repeat([]byte{3}, 16)
	c := smbTestConn(t, key)
	client, _ := smb2NewSigner(smb2Dialect210, key, nil)
	create := make([]byte, 57)
	binary.LittleEndian.PutUint16(create, 57)
	binary.LittleEndian.PutUint16(create[44:], 500) // name offset past the message
	binary.LittleEndian.PutUint16(create[46:], 20)
	req := smbTestReq(smb2CmdCreate, create)
	client.sign(req)
	out, keep := c.handleFrame(req)
	if !keep || binary.LittleEndian.Uint32(out[8:]) != smb2StatusInvalidParameter {
		t.Errorf("CREATE with a bad name offset: keep %v, reply % x", keep, out)
	}
	fresh := newSmbSrvConn(&smbService{}, nil, nil, "192.168.1.2", 1, "JUKAHUB", [16]byte{})
	if _, keep := fresh.handleFrame(smbTestReq(smbSrvCmdEcho, smbSrvU16Body(4, 4))); keep {
		t.Error("a request before NEGOTIATE kept the connection")
	}
	for _, ip := range []string{"8.8.8.8", "100.128.0.1"} {
		if smbServePeerAllowed(net.ParseIP(ip)) {
			t.Errorf("%s allowed", ip)
		}
	}
	for _, ip := range []string{"192.168.1.9", "10.0.0.2", "127.0.0.1", "100.101.102.103", "fe80::1"} {
		if !smbServePeerAllowed(net.ParseIP(ip)) {
			t.Errorf("%s refused", ip)
		}
	}
}

// A NetBIOS name query for the device gets the LAN address; other names
// get nothing.
func TestSMBServeNBNS(t *testing.T) {
	enc := func(name string, suffix byte) []byte {
		raw := []byte(name + strings.Repeat(" ", 15-len(name)) + string(rune(suffix)))
		out := []byte{0x20}
		for _, b := range raw {
			out = append(out, 'A'+b>>4, 'A'+b&0xf)
		}
		return append(out, 0)
	}
	query := func(name string) []byte {
		q := []byte{0x12, 0x34, 0x01, 0x10, 0, 1, 0, 0, 0, 0, 0, 0}
		q = append(q, enc(name, 0x20)...)
		return append(q, 0, 0x20, 0, 1)
	}
	ip := func() net.IP { return net.IPv4(192, 168, 1, 5) }
	a := smbSrvNBNSAnswer(query("JUKAHUB"), "jukahub", ip)
	if len(a) != 62 || a[0] != 0x12 || a[1] != 0x34 || !bytes.Equal(a[58:], []byte{192, 168, 1, 5}) {
		t.Fatalf("answer % x", a)
	}
	if smbSrvNBNSAnswer(query("OTHERBOX"), "JUKAHUB", ip) != nil {
		t.Error("answered for another name")
	}
	if smbServeNetBIOSName("TrimUI Smart Pro (Juka)") != "TRIMUI-SMART-PR" || smbServeNetBIOSName("  ") != "JUKAHUB" {
		t.Errorf("NetBIOS names: %q %q", smbServeNetBIOSName("TrimUI Smart Pro (Juka)"), smbServeNetBIOSName("  "))
	}
}
