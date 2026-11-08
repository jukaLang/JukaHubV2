package main

// peer_test.go — the peer layer's trust: the pairing code both screens show,
// the commitment that keeps a device in the middle from choosing it, the key
// pins on both ends of a connection, discovery answers and the stored
// identity. One test uses a loopback TLS listener; nothing else touches the
// network.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// peerTestState gives a test its own data folder and a clean peer state, and
// puts everything back afterwards.
func peerTestState(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	prevDir := peerDirOverride
	prevEnabled := peerEnabledFlag.Load()
	peerState.mu.Lock()
	prevLoaded, prevErr, prevSelf := peerState.loaded, peerState.loadErr, peerState.self
	prevProbed, prevExists := peerState.probed, peerState.probedExists
	prevPeersLoaded, prevPeers := peerState.peersLoaded, peerState.peers
	prevSeen, prevUnpaired := peerState.seen, peerState.unpairedThere
	peerState.loaded, peerState.loadErr, peerState.self = false, nil, peerIdentity{}
	peerState.probed, peerState.probedExists = false, false
	peerState.peersLoaded, peerState.peers = false, nil
	peerState.seen, peerState.unpairedThere = nil, nil
	peerState.mu.Unlock()
	peerDirOverride = dir
	t.Cleanup(func() {
		peerDirOverride = prevDir
		peerEnabledFlag.Store(prevEnabled)
		peerState.mu.Lock()
		peerState.loaded, peerState.loadErr, peerState.self = prevLoaded, prevErr, prevSelf
		peerState.probed, peerState.probedExists = prevProbed, prevExists
		peerState.peersLoaded, peerState.peers = prevPeersLoaded, prevPeers
		peerState.seen, peerState.unpairedThere = prevSeen, prevUnpaired
		peerState.mu.Unlock()
		peerClientsReset()
		// Handlers post toasts for the main loop; none of them may run in a
		// later test.
		tbxUIQueue.mu.Lock()
		tbxUIQueue.fns = nil
		tbxUIQueue.mu.Unlock()
	})
}

// peerPairTestReset starts a test with no pairing in progress.
func peerPairTestReset(t *testing.T) {
	t.Helper()
	peerPair.mu.Lock()
	prevIn, prevOut, prevSeq, prevHello := peerPair.in, peerPair.out, peerPair.seq, peerPair.lastHello
	peerPair.in, peerPair.out, peerPair.lastHello = nil, nil, time.Time{}
	peerPair.mu.Unlock()
	t.Cleanup(func() {
		peerPair.mu.Lock()
		peerPair.in, peerPair.out, peerPair.seq, peerPair.lastHello = prevIn, prevOut, prevSeq, prevHello
		peerPair.mu.Unlock()
	})
}

// peerTestIdentity is a fresh device identity.
func peerTestIdentity(t *testing.T, name string) peerIdentity {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	id, err := peerIdentityFromSeed(seed, name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// peerTestUse makes self this device's identity.
func peerTestUse(self peerIdentity) {
	peerState.mu.Lock()
	peerState.self, peerState.loaded, peerState.loadErr = self, true, nil
	peerState.probed, peerState.probedExists = true, true
	peerState.mu.Unlock()
}

// peerTestRequest is a request the way the TLS listener hands it over: with
// the caller's certificate in the connection state.
func peerTestRequest(t *testing.T, caller peerIdentity, method, target string, body interface{}) *http.Request {
	t.Helper()
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, target, r)
	cert, err := x509.ParseCertificate(caller.cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	return req
}

func TestPeerPairCodeAgreesAndBindsBothKeys(t *testing.T) {
	a := peerTestIdentity(t, "Handheld A")
	b := peerTestIdentity(t, "Handheld B")
	c := peerTestIdentity(t, "Somebody else")
	nI := bytes.Repeat([]byte{1}, peerPairNonceSize)
	nR := bytes.Repeat([]byte{2}, peerPairNonceSize)

	onA := peerPairCode(a.id, b.id, nI, nR)
	onB := peerPairCode(a.id, b.id, nI, nR)
	if len(onA) != 6 || onA != onB {
		t.Fatalf("codes %q and %q, want the same six digits on both screens", onA, onB)
	}
	if _, err := strconv.Atoi(onA); err != nil {
		t.Fatalf("code %q is not all digits", onA)
	}
	if got := peerFormatCode(onA); len(got) != 7 || got[3] != ' ' {
		t.Fatalf("shown as %q, want \"123 456\"", got)
	}
	// A device in the middle has its own key: every transcript it could be
	// part of gives another code, and so does swapping the roles or nonces.
	for i, other := range []string{
		peerPairCode(c.id, b.id, nI, nR),
		peerPairCode(a.id, c.id, nI, nR),
		peerPairCode(b.id, a.id, nI, nR),
		peerPairCode(a.id, b.id, nR, nI),
	} {
		if other == onA {
			t.Errorf("variant %d gave the same code %q", i, other)
		}
	}
	if peerPairCode("not-hex", b.id, nI, nR) != "" {
		t.Fatal("a malformed id produced a code")
	}
}

func TestPeerPairCommitBindsTheNonce(t *testing.T) {
	nonce := bytes.Repeat([]byte{7}, peerPairNonceSize)
	commit := peerPairCommit(nonce)
	if !bytes.Equal(commit, peerPairCommit(append([]byte(nil), nonce...))) {
		t.Fatal("the commitment is not deterministic")
	}
	tampered := append([]byte(nil), nonce...)
	tampered[0] ^= 1
	if bytes.Equal(commit, peerPairCommit(tampered)) {
		t.Fatal("a changed nonce matched the commitment")
	}
}

// TestPeerPairingThroughTheHandlers runs the responder's side of a pairing:
// a reveal that does not match the commitment ends the session, a correct one
// puts the same code on screen that the initiator computes, and the key is
// stored only once both users confirmed.
func TestPeerPairingThroughTheHandlers(t *testing.T) {
	peerTestState(t)
	peerPairTestReset(t)
	responder := peerTestIdentity(t, "Brick")
	initiator := peerTestIdentity(t, "Smart Pro")
	peerTestUse(responder)
	peerEnabledFlag.Store(true)
	h := peerHandler()
	send := func(path string, body interface{}) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, peerTestRequest(t, initiator, http.MethodPost, path, body))
		return rec
	}
	hello := func(nonce []byte) []byte {
		peerPair.mu.Lock()
		peerPair.lastHello = time.Time{}
		peerPair.mu.Unlock()
		rec := send("/peer/v1/pair/hello", peerPairHelloReq{V: 1, Commit: hex.EncodeToString(peerPairCommit(nonce)), Name: "Smart Pro"})
		if rec.Code != http.StatusOK {
			t.Fatalf("hello: %d %s", rec.Code, rec.Body.String())
		}
		var resp peerPairHelloResp
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		nonceR, err := hex.DecodeString(resp.Nonce)
		if err != nil || len(nonceR) != peerPairNonceSize {
			t.Fatalf("responder nonce %q", resp.Nonce)
		}
		if resp.Name != "Brick" {
			t.Errorf("responder named itself %q", resp.Name)
		}
		return nonceR
	}

	nI := bytes.Repeat([]byte{3}, peerPairNonceSize)
	hello(nI)
	bad := append([]byte(nil), nI...)
	bad[5] ^= 0x80
	if rec := send("/peer/v1/pair/reveal", peerPairRevealReq{Nonce: hex.EncodeToString(bad)}); rec.Code != http.StatusBadRequest {
		t.Fatalf("a reveal that breaks the commitment answered %d", rec.Code)
	}
	if rec := send("/peer/v1/pair/reveal", peerPairRevealReq{Nonce: hex.EncodeToString(nI)}); rec.Code == http.StatusOK {
		t.Fatal("the session went on after a broken commitment")
	}
	if in, _ := peerPairViews(); in.live {
		t.Fatal("the tampered session is still live")
	}

	nR := hello(nI)
	if rec := send("/peer/v1/pair/reveal", peerPairRevealReq{Nonce: hex.EncodeToString(nI)}); rec.Code != http.StatusOK {
		t.Fatalf("reveal: %d %s", rec.Code, rec.Body.String())
	}
	in, _ := peerPairViews()
	if want := peerPairCode(initiator.id, responder.id, nI, nR); in.code != want || in.stage != peerPairCompare {
		t.Fatalf("responder shows %q (stage %d), initiator computes %q", in.code, in.stage, want)
	}

	confirm := func() string {
		rec := send("/peer/v1/pair/confirm", peerPairConfirmReq{Accept: true})
		if rec.Code != http.StatusOK {
			t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
		}
		var resp peerPairConfirmResp
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp.State
	}
	if state := confirm(); state != "waiting" {
		t.Fatalf("the initiator's confirm answered %q before this user did", state)
	}
	if _, ok := peerPairedRecord(initiator.id); ok {
		t.Fatal("the key was stored before this user confirmed")
	}
	peerPairAnswerIncoming(true)
	rec, ok := peerPairedRecord(initiator.id)
	if !ok || rec.Name != "Smart Pro" {
		t.Fatalf("after both confirmed the key is not stored: %+v %v", rec, ok)
	}
	if state := confirm(); state != "accepted" {
		t.Fatalf("the initiator's next confirm answered %q, want accepted", state)
	}
}

func TestPeerClientAndListenerPinKeys(t *testing.T) {
	a := peerTestIdentity(t, "A")
	b := peerTestIdentity(t, "B")
	c := peerTestIdentity(t, "C")
	pinned := peerClientTLS(a, b.id, nil)
	if err := pinned.VerifyPeerCertificate([][]byte{b.cert.Certificate[0]}, nil); err != nil {
		t.Fatalf("the paired key was refused: %v", err)
	}
	if err := pinned.VerifyPeerCertificate([][]byte{c.cert.Certificate[0]}, nil); !errors.Is(err, errPeerWrongKey) {
		t.Fatalf("another key was accepted: %v", err)
	}
	if err := pinned.VerifyPeerCertificate(nil, nil); err == nil {
		t.Fatal("a server without a certificate was accepted")
	}
	var seen peerKeySeen
	open := peerClientTLS(a, "", &seen)
	if err := open.VerifyPeerCertificate([][]byte{c.cert.Certificate[0]}, nil); err != nil || seen.get() != c.id {
		t.Fatalf("unpinned check: %v, saw %q, want %q", err, seen.get(), c.id)
	}
	srv := peerServerTLS(b.cert)
	if srv.ClientAuth != tls.RequireAnyClientCert || srv.MinVersion != tls.VersionTLS13 {
		t.Fatal("the listener does not ask for a client key over TLS 1.3")
	}
	if err := srv.VerifyPeerCertificate(nil, nil); err == nil {
		t.Fatal("the listener accepted a caller with no key")
	}
	if id, err := peerCertFingerprint(a.cert.Certificate[0]); err != nil || id != a.id {
		t.Fatalf("fingerprint %q (%v), want %q", id, err, a.id)
	}
}

// TestPeerTLSRefusesUnpairedAndWrongKeys runs the real listener stack on
// loopback: a paired key gets in, an unpaired one is refused, and a client
// pinned to another key never talks to the server at all.
func TestPeerTLSRefusesUnpairedAndWrongKeys(t *testing.T) {
	peerTestState(t)
	server := peerTestIdentity(t, "Server")
	paired := peerTestIdentity(t, "Paired")
	stranger := peerTestIdentity(t, "Stranger")
	peerTestUse(server)
	peerAddPaired(peerRecord{ID: paired.id, Name: "Paired"})
	peerEnabledFlag.Store(true)

	srv := httptest.NewUnstartedServer(peerHandler())
	srv.TLS = peerServerTLS(server.cert)
	srv.StartTLS()
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	get := func(client *http.Client) (int, error) {
		defer client.CloseIdleConnections()
		resp, err := client.Get("https://" + addr + "/peer/v1/activity")
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}
	if code, err := get(peerNewClient(paired, server.id, nil)); err != nil || code != http.StatusOK {
		t.Fatalf("paired key: %d %v, want 200", code, err)
	}
	if code, err := get(peerNewClient(stranger, server.id, nil)); err != nil || code != http.StatusForbidden {
		t.Fatalf("unpaired key: %d %v, want 403", code, err)
	}
	if _, err := get(peerNewClient(paired, stranger.id, nil)); err == nil {
		t.Fatal("a client pinned to another key accepted the server")
	}
}

func TestPeerParseBeacon(t *testing.T) {
	self := peerTestIdentity(t, "X")
	from := &net.UDPAddr{IP: net.ParseIP("192.168.1.42"), Port: 50000}
	good := peerBeaconReply{V: 1, App: "JukaHub", ID: self.id, Name: "Juka's\x07 Brick", Model: "TrimUI Brick",
		Port: 8322, Share: true, NRev: "abc", Play: "Zelda", Sys: "GBA"}
	data, err := json.Marshal(good)
	if err != nil {
		t.Fatal(err)
	}
	dev, ok := peerParseBeacon(data, from)
	if !ok {
		t.Fatal("a well-formed answer was refused")
	}
	// The address comes from where the answer came from, never the payload.
	if dev.Addr != "192.168.1.42:8322" || dev.IP != "192.168.1.42" {
		t.Errorf("address %q / %q", dev.Addr, dev.IP)
	}
	if dev.Name != "Juka's Brick" || dev.ID != self.id || !dev.Share || dev.Play != "Zelda" {
		t.Errorf("parsed %+v", dev)
	}

	mutate := func(fn func(r *peerBeaconReply)) []byte {
		r := good
		fn(&r)
		out, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	bad := map[string][]byte{
		"other app":   mutate(func(r *peerBeaconReply) { r.App = "Other" }),
		"version":     mutate(func(r *peerBeaconReply) { r.V = 2 }),
		"short id":    mutate(func(r *peerBeaconReply) { r.ID = "abc" }),
		"non-hex id":  mutate(func(r *peerBeaconReply) { r.ID = "g" + self.id[1:] }),
		"no port":     mutate(func(r *peerBeaconReply) { r.Port = 0 }),
		"big port":    mutate(func(r *peerBeaconReply) { r.Port = 70000 }),
		"not json":    []byte("JUKAPEER?1"),
		"empty":       nil,
		"too long":    bytes.Repeat([]byte{' '}, peerReplyMax+1),
		"array json":  []byte("[1,2,3]"),
		"string json": []byte(`"hello"`),
	}
	for name, payload := range bad {
		if _, ok := peerParseBeacon(payload, from); ok {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, ok := peerParseBeacon(data, nil); ok {
		t.Error("an answer with no sender was accepted")
	}
	// A nameless device still gets a name to show.
	unnamed := mutate(func(r *peerBeaconReply) { r.Name = "\x01\x02" })
	if dev, ok := peerParseBeacon(unnamed, from); !ok || dev.Name == "" {
		t.Errorf("unnamed device: %+v %v", dev, ok)
	}
}

func TestPeerIdentityPersistsAndIsReplacedWhenUnreadable(t *testing.T) {
	dir := t.TempDir()
	first, err := peerLoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !peerValidID(first.id) || first.name == "" {
		t.Fatalf("new identity %q named %q", first.id, first.name)
	}
	second, err := peerLoadIdentity(dir)
	if err != nil || second.id != first.id {
		t.Fatalf("a second load gave %q (%v), want the same key %q", second.id, err, first.id)
	}
	path := filepath.Join(dir, "identity.json")
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("identity.json is readable by others: %v", info.Mode().Perm())
		}
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path + ".bak")
	third, err := peerLoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if third.id == first.id {
		t.Fatal("an unreadable identity was not replaced")
	}
}

func TestPeerPairedListPersists(t *testing.T) {
	peerTestState(t)
	a := peerTestIdentity(t, "A")
	peerAddPaired(peerRecord{ID: a.id, Name: "Brick\x00 one"})
	if got := peerPairedCount(); got != 1 {
		t.Fatalf("%d paired, want 1", got)
	}
	loaded := peerLoadPeers(peerDataDir())
	if len(loaded) != 1 || loaded[0].ID != a.id || loaded[0].Name != "Brick one" {
		t.Fatalf("stored list %+v", loaded)
	}
	// Garbage ids never come back from the file.
	data := []byte(`{"version":1,"peers":[{"id":"zz","name":"bad"},{"id":"` + a.id + `","name":"ok"},{"id":"` + a.id + `","name":"dup"}]}`)
	if err := os.WriteFile(filepath.Join(peerDataDir(), "peers.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := peerLoadPeers(peerDataDir()); len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("loaded %+v, want the one valid entry", got)
	}
}
