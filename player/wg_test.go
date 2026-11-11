package main

// wg_test.go — the VPN's WireGuard (wg_noise.go, wg_device.go, wg_config.go)
// against the Linux kernel's. The transcript below was recorded by
// tmp/wgref/kernel_interop.py, which plays both roles against kernel
// WireGuard in a throwaway network namespace; JukaHub's code, fed the same
// keys, ephemerals, indices and timestamps, must produce the same bytes and
// open what the kernel sent.

import (
	"bytes"
	"crypto/ecdh"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// Recorded by tmp/wgref/kernel_interop.py against the Linux kernel's
// WireGuard (6.8): every value below is what the kernel sent or accepted.
var wgKernelTranscript = map[string]string{
	"client_public":              "e243bea5690f618f278c1c3896edc253d112fb2a9c22e86f328b8927f1b6492d",
	"client_static":              "582b3969b205d72df94d724a0e8c8beb64e190318fac0573e245ee2880603f54",
	"initiator.ephemeral":        "30db5d95da65e9daf4c5d1cf992c186a18121380a2ae7a259f42207e02dd1740",
	"initiator.initiation":       "01000000443322110f155a6fdd473f1e5fe3cc35bd76e3943c7de49a802b5362b8281c70c2165a7e48f459f24e4933493b6d26cef98c1219cb1a3a0f8d328cfb90e6bcf3cd1b1ee3f6bcc36ebad35fea639734fb98bdd7b3f7c75f69be8c596ee06c4c8623dbff53bb4a171f009e03dc64b8fe7a1a7a65a8b4cd9af4007607b5b2b54fb800000000000000000000000000000000",
	"initiator.keepalive_sealed": "04000000aeb42716010000000000000069165ab63158eeb4b4a211d0d694e5f3",
	"initiator.ping":             "4500002c12340000400153d50a6300020a630001080049e54a4b00016a756b616875622076706e2070696e67",
	"initiator.ping_sealed":      "04000000aeb4271600000000000000005334eaade3a6dadb9846e3b80335ab1b668d9474542829d03a7ccc982055e80af94f0e4ff1b8ac5e3ee451545c80d2085595d6cc550fd746464e22c5d036ffb0",
	"initiator.recv_key":         "378724086632e17df62d4a8302f5064467bc870718ca2cebbe6a6d17094df635",
	"initiator.reply_plain":      "4500002c5c7f00004001098a0a6300010a630002000051e54a4b00016a756b616875622076706e2070696e67",
	"initiator.reply_sealed":     "040000004433221100000000000000007165a2f4e58bef08ad1b678ce791c187ef30ba127f79b721cd5f20bf702e6643673ddbc5d8c3774712001591d0f3cb261e605599ee18246a447fafa13dceefe9",
	"initiator.response":         "02000000aeb42716443322116dc4b430d2f80a5fd6d99cc7e8f3e02b4663f440bd0d6525c7cf14d1f750f82ec4ba2aa1308d204d33d2dd1a22c3fc7cb2e8d47084df5ea256fe3abf9386774500000000000000000000000000000000",
	"initiator.send_key":         "e55a00b7ae331957ac2451391079dc8ef8c43357a90d63d000121d8771f90ec1",
	"initiator.sender_index":     "11223344",
	"initiator.timestamp":        "400000006af3b00a0c000000",
	"psk":                        "163c4d3e56273f92609dabce4a3e3384f77bc23048f1101710088740473d016b",
	"responder.answer":           "4500003322220000401143d00a6300020a630001270f15b3001f000068656c6c6f206261636b2066726f6d204a756b61487562",
	"responder.answer_sealed":    "0400000020accd3e0000000000000000b12010a6006537ee81097ac51c5f2eb3b4b36e6f3e90ceed4a16d02ed1d86642c71e58aed4c59bf309dbaa91c763671cd37eefadb010df428ed2dad604b4942d5e080db89c3870c3f1d0bb5131a329c6",
	"responder.data_plain":       "45000036af6a4000401176840a6300010a63000215b3270f0022d34168656c6c6f2066726f6d20746865206b65726e656c2073696465",
	"responder.data_sealed":      "04000000887766550000000000000000fd28b6df9520cbf99ebec883e42372b3132f15f0b538fb081e73cecd1c04c619998be683543cea23c3053e6600fb1704321a599270ad54b031361cd98d765b899ddb8be941a0bbf60bbc4c9c69b3cdd8",
	"responder.ephemeral":        "70f1960c02c930f93acc2cbd3ce5d17b7634476e484c6a81d9f34bf2da13767d",
	"responder.initiation":       "0100000020accd3eb7a233366ddec78fe818979bef9d3418c52c72a902b14d5ffa2ec331b8352b3ea718ab36aacb53d3388d84465a36de491cb595156b1335d68d23a8c8c2bc33bb8e67f8e52378c40af59e19980fcbeecd5b1098401f712a4e47f65fd5c17a27b7b53095756db04ff90c7d7acb37b67db0e4fa3e7dd09a6a046fefdd4100000000000000000000000000000000",
	"responder.recv_key":         "040f744d277ebcb9d1d0eb7d9c40e596071574893f6db4c52726e76c40f03fae",
	"responder.response":         "020000008877665520accd3e7cb51b7ef5f31afe12dfdfd2237d799221a5f7656de6e5e618827d75aa9f0265897eb8e06819f59ea13843fd37650009e15de202a3761d945864e480d6a8dbdf00000000000000000000000000000000",
	"responder.send_key":         "723821a7613de32377c4e9fdba82b499fd4fa6227f6d943f1536859dbb4dcd3d",
	"responder.sender_index":     "55667788",
	"responder.timestamp":        "400000006af3ffa715000000",
	"server_public":              "ea3db821f9c27e26c473753c80df19c3409bfcda88bac811fc660582ddf7830a",
	"server_static":              "080250613b1a31d0e80e48e3c3629771ef244e6f1fdf33a6c33a78f5ecbb225a",
}

func wgTranscriptBytes(t *testing.T, key string) []byte {
	t.Helper()
	v, ok := wgKernelTranscript[key]
	if !ok {
		t.Fatalf("the transcript has no %s", key)
	}
	b, err := hex.DecodeString(v)
	if err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	return b
}

func wgTranscriptKey(t *testing.T, key string) [32]byte {
	t.Helper()
	var k [32]byte
	copy(k[:], wgTranscriptBytes(t, key))
	return k
}

func wgTranscriptIndex(t *testing.T, key string) uint32 {
	t.Helper()
	return binary.BigEndian.Uint32(wgTranscriptBytes(t, key))
}

// wgOpenData opens a transport message with kp and cuts the padding off by
// the IP header's own length.
func wgOpenData(t *testing.T, kp *wgKeypair, msg []byte) []byte {
	t.Helper()
	nonce := wgNonce(binary.LittleEndian.Uint64(msg[8:16]))
	plain, err := kp.recv.Open(nil, nonce[:], msg[wgDataHeaderSize:], nil)
	if err != nil {
		t.Fatalf("message does not open: %v", err)
	}
	_, _, n, ok := wgPacketAddrs(plain)
	if !ok {
		t.Fatalf("not an IP packet: %x", plain)
	}
	return plain[:n]
}

// TestWGKernelInitiator: the handshake JukaHub starts.
func TestWGKernelInitiator(t *testing.T) {
	id, err := newWGIdentity(wgTranscriptKey(t, "client_static"), wgTranscriptKey(t, "server_public"), [32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(id.public[:]); got != wgKernelTranscript["client_public"] {
		t.Fatalf("public key %s, want %s", got, wgKernelTranscript["client_public"])
	}
	eph, err := ecdh.X25519().NewPrivateKey(wgTranscriptBytes(t, "initiator.ephemeral"))
	if err != nil {
		t.Fatal(err)
	}
	var ts [12]byte
	copy(ts[:], wgTranscriptBytes(t, "initiator.timestamp"))
	n := wgNoise{id: id}
	now := time.Now()
	init, err := n.initiation(eph, wgTranscriptIndex(t, "initiator.sender_index"), ts, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := wgTranscriptBytes(t, "initiator.initiation"); !bytes.Equal(init, want) {
		t.Fatalf("initiation differs from the one the kernel accepted:\n got %x\nwant %x", init, want)
	}
	resp := wgTranscriptBytes(t, "initiator.response")
	if !n.mac1OK(resp) {
		t.Fatal("the kernel's response fails mac1")
	}
	send, recv, local, remote, err := n.takeResponse(resp)
	if err != nil {
		t.Fatal(err)
	}
	if send != wgTranscriptKey(t, "initiator.send_key") || recv != wgTranscriptKey(t, "initiator.recv_key") {
		t.Fatal("session keys differ from the kernel's")
	}
	if _, _, _, _, err := n.takeResponse(resp); err == nil {
		t.Fatal("a response was taken twice")
	}
	kp, err := newWGKeypair(send, recv, local, remote, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sealData(kp, 0, wgTranscriptBytes(t, "initiator.ping"), wgDefaultMTU, nil), wgTranscriptBytes(t, "initiator.ping_sealed"); !bytes.Equal(got, want) {
		t.Fatalf("sealed ping differs:\n got %x\nwant %x", got, want)
	}
	if got, want := sealData(kp, 1, nil, wgDefaultMTU, nil), wgTranscriptBytes(t, "initiator.keepalive_sealed"); !bytes.Equal(got, want) {
		t.Fatalf("keepalive differs:\n got %x\nwant %x", got, want)
	}
	if got := wgOpenData(t, kp, wgTranscriptBytes(t, "initiator.reply_sealed")); !bytes.Equal(got, wgTranscriptBytes(t, "initiator.reply_plain")) {
		t.Fatalf("the kernel's echo reply opened as %x", got)
	}
}

// TestWGKernelResponder: the handshake the server (here the kernel, with a
// preshared key) starts, answered by JukaHub.
func TestWGKernelResponder(t *testing.T) {
	id, err := newWGIdentity(wgTranscriptKey(t, "client_static"), wgTranscriptKey(t, "server_public"), wgTranscriptKey(t, "psk"))
	if err != nil {
		t.Fatal(err)
	}
	init := wgTranscriptBytes(t, "responder.initiation")
	n := wgNoise{id: id}
	now := time.Now()
	if !n.mac1OK(init) {
		t.Fatal("the kernel's initiation fails mac1")
	}
	if err := n.takeInitiation(init, now); err != nil {
		t.Fatal(err)
	}
	eph, err := ecdh.X25519().NewPrivateKey(wgTranscriptBytes(t, "responder.ephemeral"))
	if err != nil {
		t.Fatal(err)
	}
	index := wgTranscriptIndex(t, "responder.sender_index")
	resp, send, recv, remote, err := n.response(eph, index, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := wgTranscriptBytes(t, "responder.response"); !bytes.Equal(resp, want) {
		t.Fatalf("response differs from the one the kernel accepted:\n got %x\nwant %x", resp, want)
	}
	if send != wgTranscriptKey(t, "responder.send_key") || recv != wgTranscriptKey(t, "responder.recv_key") {
		t.Fatal("session keys differ from the kernel's")
	}
	kp, err := newWGKeypair(send, recv, index, remote, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := wgOpenData(t, kp, wgTranscriptBytes(t, "responder.data_sealed")); !bytes.Equal(got, wgTranscriptBytes(t, "responder.data_plain")) {
		t.Fatalf("the kernel's datagram opened as %x", got)
	}
	if got, want := sealData(kp, 0, wgTranscriptBytes(t, "responder.answer"), wgDefaultMTU, nil), wgTranscriptBytes(t, "responder.answer_sealed"); !bytes.Equal(got, want) {
		t.Fatalf("sealed answer differs:\n got %x\nwant %x", got, want)
	}
	if err := n.takeInitiation(init, now.Add(time.Second)); err != errWGReplay {
		t.Fatalf("a replayed initiation gave %v, want errWGReplay", err)
	}
}

type wgTestWire struct{ sent [][]byte }

func (w *wgTestWire) ReadFromUDPAddrPort([]byte) (int, netip.AddrPort, error) {
	return 0, netip.AddrPort{}, io.EOF
}

func (w *wgTestWire) WriteToUDPAddrPort(b []byte, _ netip.AddrPort) (int, error) {
	w.sent = append(w.sent, append([]byte(nil), b...))
	return len(b), nil
}

type wgTestTUN struct{ got [][]byte }

func (t *wgTestTUN) Read([]byte) (int, error) { return 0, io.EOF }

func (t *wgTestTUN) Write(p []byte) (int, error) {
	t.got = append(t.got, append([]byte(nil), p...))
	return len(p), nil
}

// wgTestPacket is an IPv4 UDP datagram.
func wgTestPacket(src, dst, payload string) []byte {
	p := make([]byte, 28+len(payload))
	p[0], p[8], p[9] = 0x45, 64, 17
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	s, d := netip.MustParseAddr(src).As4(), netip.MustParseAddr(dst).As4()
	copy(p[12:16], s[:])
	copy(p[16:20], d[:])
	binary.BigEndian.PutUint16(p[20:22], 4000)
	binary.BigEndian.PutUint16(p[22:24], 5000)
	binary.BigEndian.PutUint16(p[24:26], uint16(8+len(payload)))
	copy(p[28:], payload)
	return p
}

// TestWGDeviceRoundTrip runs two devices against each other in memory: a
// packet sent before there is a session waits for the handshake, the
// answered session is confirmed by the first data, a replay is dropped, and
// packets flow both ways.
func TestWGDeviceRoundTrip(t *testing.T) {
	idA, errA := newWGIdentity(wgTranscriptKey(t, "client_static"), wgTranscriptKey(t, "server_public"), [32]byte{})
	idB, errB := newWGIdentity(wgTranscriptKey(t, "server_static"), wgTranscriptKey(t, "client_public"), [32]byte{})
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	epA, epB := netip.MustParseAddrPort("192.0.2.1:40000"), netip.MustParseAddrPort("198.51.100.7:51820")
	all := []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}
	stamp := func() [12]byte { return wgTAI64N(time.Now()) }
	wireA, wireB, tunA, tunB := &wgTestWire{}, &wgTestWire{}, &wgTestTUN{}, &wgTestTUN{}
	a := newWGDevice(wgDeviceConfig{Identity: idA, Endpoint: epB, Allowed: all, MTU: wgDefaultMTU, Timestamp: stamp}, tunA, wireA, crand.Reader)
	b := newWGDevice(wgDeviceConfig{Identity: idB, Endpoint: epA, Allowed: all, MTU: wgDefaultMTU, Timestamp: stamp}, tunB, wireB, crand.Reader)
	deliver := func(msgs [][]byte, to *wgDevice, src netip.AddrPort) {
		for _, m := range msgs {
			to.receive(m, src, make([]byte, 0, wgBufSize))
		}
	}
	take := func(w *wgTestWire) [][]byte {
		s := w.sent
		w.sent = nil
		return s
	}

	ping := wgTestPacket("10.99.0.2", "10.99.0.1", "ping")
	a.sendPacket(ping, nil)
	deliver(take(wireA), b, epA) // the initiation; b answers
	deliver(take(wireB), a, epB) // the response; a sends what waited
	data := take(wireA)
	deliver(data, b, epA)
	deliver(data, b, epA) // the same message again
	if len(tunB.got) != 1 || !bytes.Equal(tunB.got[0], ping) {
		t.Fatalf("b's tunnel got %x, want one %x", tunB.got, ping)
	}
	pong := wgTestPacket("10.99.0.1", "10.99.0.2", "pong")
	b.sendPacket(pong, nil)
	deliver(take(wireB), a, epB)
	if len(tunA.got) != 1 || !bytes.Equal(tunA.got[0], pong) {
		t.Fatalf("a's tunnel got %x, want one %x", tunA.got, pong)
	}
	if st := a.Stats(); st.LastHandshake.IsZero() || !st.UnansweredSince.IsZero() {
		t.Fatalf("a's stats after the handshake: %+v", st)
	}
}

// TestWGReplayWindow: each counter once, and nothing older than the window.
func TestWGReplayWindow(t *testing.T) {
	var r wgReplay
	for _, c := range []struct {
		counter uint64
		want    bool
	}{
		{0, true}, {0, false}, {5, true}, {3, true}, {5, false},
		{10000, true}, {9000, true}, {100, false}, {wgRejectAfterMessages, false},
	} {
		if got := r.accept(c.counter); got != c.want {
			t.Fatalf("accept(%d) = %v, want %v", c.counter, got, c.want)
		}
	}
}

// TestWGParseConfig: a provider-style file with CRLF, comments, a script and
// a domain in DNS; what it routes; and the mistakes people make.
func TestWGParseConfig(t *testing.T) {
	priv := wgTranscriptKey(t, "client_static")
	peer := wgTranscriptKey(t, "server_public")
	good := strings.Join([]string{
		"[Interface]", "# from the provider", "PrivateKey = " + base64.StdEncoding.EncodeToString(priv[:]),
		"Address = 10.64.1.2/32, fc00:bbbb::2/128", "DNS = 10.64.0.1, home.lan", "PostUp = iptables -A FORWARD -j ACCEPT",
		"", "[Peer]", "PublicKey = " + base64.StdEncoding.EncodeToString(peer[:]),
		"AllowedIPs = 0.0.0.0/0, ::/0", "Endpoint = [2001:db8::1]:51820", "PersistentKeepalive = 25", "",
	}, "\r\n")
	c, err := wgParseConfig(good)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Addresses) != 2 || c.DNS[0].String() != "10.64.0.1" || c.DNSSearch[0] != "home.lan" ||
		c.EndpointHost != "2001:db8::1" || c.EndpointPort != 51820 || c.Keepalive != 25 || c.Skipped[0] != "PostUp" {
		t.Fatalf("parsed %+v", c)
	}
	if c.publicKey() != base64.StdEncoding.EncodeToString(wgTranscriptBytes(t, "client_public")) {
		t.Fatal("public key does not match the private key")
	}
	plan := c.routePlan(false)
	if wgJoinPrefixes(plan.Routes) != "0.0.0.0/1, 128.0.0.0/1, ::/1, 8000::/1" || len(plan.Reject) != 0 {
		t.Fatalf("plan %+v", plan)
	}
	quick := c.quickText(true)
	if strings.Contains(quick, "PostUp") || !strings.Contains(quick, "AllowedIPs = 0.0.0.0/1, 128.0.0.0/1, ::/1, 8000::/1\n") {
		t.Fatalf("normalised text:\n%s", quick)
	}
	if again, err := wgParseConfig(quick); err != nil || again.Endpoint != c.Endpoint {
		t.Fatalf("normalised text does not parse back: %v", err)
	}
	if own := c.quickText(false); !strings.Contains(own, "AllowedIPs = 0.0.0.0/1, 128.0.0.0/1, ::/1, 8000::/1\n") {
		t.Fatalf("a /0 must be written as its halves:\n%s", own)
	}
	v4 := strings.Replace(good, "Address = 10.64.1.2/32, fc00:bbbb::2/128", "Address = 10.64.1.2", 1)
	if c4, err := wgParseConfig(v4); err != nil || wgJoinPrefixes(c4.routePlan(true).Reject) != "::/1, 8000::/1" ||
		len(c4.routePlan(false).Allowed) != 1 {
		t.Fatalf("an IPv4-only tunnel must refuse IPv6 and route only IPv4: %v", err)
	}
	for name, text := range map[string]string{
		"no private key":  strings.Replace(good, "PrivateKey", "# PrivateKey", 1),
		"bad key":         strings.Replace(good, base64.StdEncoding.EncodeToString(peer[:]), "c2hvcnQ=", 1),
		"two peers":       good + "[Peer]\r\nPublicKey = " + base64.StdEncoding.EncodeToString(peer[:]) + "\r\n",
		"no endpoint":     strings.Replace(good, "Endpoint", "# Endpoint", 1),
		"bad endpoint":    strings.Replace(good, "[2001:db8::1]:51820", "vpn.example.com", 1),
		"no section":      "PrivateKey = x\n",
		"not a key line":  strings.Replace(good, "PersistentKeepalive = 25", "PersistentKeepalive", 1),
		"empty key":       strings.Replace(good, "PersistentKeepalive = 25", "= 25", 1),
		"IPv6 under 1280": strings.Replace(good, "[Interface]", "[Interface]\r\nMTU = 1200", 1),
	} {
		if _, err := wgParseConfig(text); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
