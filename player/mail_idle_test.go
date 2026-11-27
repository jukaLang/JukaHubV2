package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── A loopback IMAP server ──────────────────────────────────────────────
//
// It speaks just enough IMAP for the real client: a greeting with the
// capabilities, LOGIN, EXAMINE INBOX, IDLE (with "+", pushed EXISTS and DONE)
// and LOGOUT. TLS with a certificate for 127.0.0.1, trusted through
// mailTestRoots, so the whole mail path (dial, handshake, sign-in, IDLE) is
// exercised, not a stub.

type fakeIMAP struct {
	ln   net.Listener
	caps string // the capability list it advertises ("IMAP4rev1 IDLE" or without)

	mu     sync.Mutex
	pushes []chan struct{}
	conns  atomic.Int32 // accepted connections, ever
	open   atomic.Int32 // open right now
	idles  atomic.Int32 // IDLE commands received

	idleSeen chan struct{}
}

func newFakeIMAP(t *testing.T, caps string) *fakeIMAP {
	t.Helper()
	cert := idleTestCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeIMAP{ln: ln, caps: caps, idleSeen: make(chan struct{}, 16)}
	pool := x509.NewCertPool()
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse the test certificate: %v", err)
	}
	pool.AddCert(leaf)
	prevRoots := mailTestRoots
	mailTestRoots = func() *x509.CertPool { return pool }
	go s.serve()
	t.Cleanup(func() {
		mailTestRoots = prevRoots
		_ = ln.Close()
	})
	return s
}

func (s *fakeIMAP) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *fakeIMAP) account(t *testing.T) mailcalAccount {
	return mailcalAccount{
		ID:       "idle-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Email:    "me@example.com",
		IMAPHost: "127.0.0.1",
		IMAPPort: s.port(),
		IMAPSec:  mailcalTLS,
		Password: "pw",
	}
}

func (s *fakeIMAP) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

// pushNew tells every waiting connection that a message arrived.
func (s *fakeIMAP) pushNew() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.pushes {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *fakeIMAP) handle(c net.Conn) {
	defer c.Close()
	push := make(chan struct{}, 1)
	s.mu.Lock()
	s.pushes = append(s.pushes, push)
	s.mu.Unlock()
	s.conns.Add(1)
	s.open.Add(1)
	defer s.open.Add(-1)

	w := bufio.NewWriter(c)
	fmt.Fprintf(w, "* OK [CAPABILITY IMAP4rev1 %s] loopback ready\r\n", s.caps)
	w.Flush()
	out := make(chan string, 8)
	go func() {
		r := bufio.NewReader(c)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				close(out)
				return
			}
			out <- strings.TrimRight(line, "\r\n")
		}
	}()
	idleTag := ""
	for {
		var line string
		select {
		case <-push:
			fmt.Fprint(w, "* 2 EXISTS\r\n")
			w.Flush()
			continue
		case l, ok := <-out:
			if !ok {
				return
			}
			line = l
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) == 1 && strings.EqualFold(fields[0], "DONE") {
			fmt.Fprintf(w, "%s OK IDLE completed\r\n", idleTag)
			w.Flush()
			idleTag = ""
			continue
		}
		if len(fields) < 2 {
			continue
		}
		tag, cmd := fields[0], strings.ToUpper(fields[1])
		switch cmd {
		case "CAPABILITY":
			fmt.Fprintf(w, "* CAPABILITY IMAP4rev1 %s\r\n%s OK CAPABILITY completed\r\n", s.caps, tag)
		case "LOGIN":
			fmt.Fprintf(w, "%s OK LOGIN completed\r\n", tag)
		case "EXAMINE", "SELECT":
			fmt.Fprint(w, "* 1 EXISTS\r\n* OK [UIDVALIDITY 7] UIDs valid\r\n")
			fmt.Fprintf(w, "%s OK [READ-ONLY] %s completed\r\n", tag, cmd)
		case "IDLE":
			idleTag = tag
			s.idles.Add(1)
			select {
			case s.idleSeen <- struct{}{}:
			default:
			}
			fmt.Fprint(w, "+ idling\r\n")
		case "LOGOUT":
			fmt.Fprintf(w, "* BYE loopback signing off\r\n%s OK LOGOUT completed\r\n", tag)
			w.Flush()
			return
		default:
			fmt.Fprintf(w, "%s BAD not here\r\n", tag)
		}
		w.Flush()
	}
}

func (s *fakeIMAP) waitIdle(t *testing.T) {
	t.Helper()
	select {
	case <-s.idleSeen:
	case <-time.After(10 * time.Second):
		t.Fatal("the client never sent IDLE")
	}
}

// waitIdleOrError waits for IDLE, but fails with the session's own error if it
// gave up first.
func (s *fakeIMAP) waitIdleOrError(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case <-s.idleSeen:
	case err := <-done:
		t.Fatalf("the session ended before it waited in IDLE: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the client never sent IDLE")
	}
}

func (s *fakeIMAP) waitOpen(t *testing.T, n int32) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s.open.Load() == n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("open connections = %d, want %d", s.open.Load(), n)
}

// idleTestCert makes a certificate for 127.0.0.1.
func idleTestCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// ── Tests ───────────────────────────────────────────────────────────────

// TestMailIdleNotifiesWithinSecondsOfNewMail is the feature's happy path: the
// connection waits in IDLE, the server pushes an EXISTS, and the sync (which
// notifies) is asked for at once — then IDLE is armed again for the next one.
func TestMailIdleNotifiesWithinSecondsOfNewMail(t *testing.T) {
	s := newFakeIMAP(t, "IDLE")
	a := s.account(t)
	got := make(chan string, 4)
	prev := mailIdleSync
	mailIdleSync = func(a mailcalAccount) { got <- a.ID }
	t.Cleanup(func() { mailIdleSync = prev })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mailIdleSession(ctx, a) }()
	t.Cleanup(cancel)

	s.waitIdleOrError(t, done)
	start := time.Now()
	s.pushNew()
	select {
	case id := <-got:
		if id != a.ID {
			t.Fatalf("notified for %q, want %q", id, a.ID)
		}
		t.Logf("EXISTS → sync request: %s", time.Since(start))
	case <-time.After(5 * time.Second):
		t.Fatal("no sync request within 5 s of the server's EXISTS")
	}
	s.waitIdle(t) // DONE was sent and IDLE re-armed

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("the session ended with %v, want a clean stop", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not stop when its context ended")
	}
	if n := s.idles.Load(); n < 2 {
		t.Fatalf("IDLE was sent %d times, want the renew after the first push", n)
	}
}

// TestMailIdleRenewsBeforeTheServerTimesOut is the 25-minute renewal: a window
// ends, DONE goes out, and a new IDLE starts on the same connection.
func TestMailIdleRenewsBeforeTheServerTimesOut(t *testing.T) {
	s := newFakeIMAP(t, "IDLE")
	a := s.account(t)
	prev := mailIdleRenew
	mailIdleRenew = 200 * time.Millisecond
	t.Cleanup(func() { mailIdleRenew = prev })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mailIdleSession(ctx, a) }()
	t.Cleanup(cancel)

	s.waitIdleOrError(t, done)
	s.waitIdle(t) // the renewal's own IDLE
	if n := s.idles.Load(); n < 2 {
		t.Fatalf("IDLE was sent %d times after one renewal", n)
	}
	if n := s.conns.Load(); n != 1 {
		t.Fatalf("the renewal opened %d connections, want the same one", n)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not stop")
	}
}

// TestMailIdleServerWithoutIdleFallsBackToPolling: a server that does not
// advertise IDLE ends the session with the sentinel, asks for no sync, and the
// supervisor does not dial it again until its settings change.
func TestMailIdleServerWithoutIdleFallsBackToPolling(t *testing.T) {
	s := newFakeIMAP(t, "")
	a := s.account(t)
	addIdleTestAccount(t, a)
	synced := make(chan string, 4)
	prevSync, prevFg := mailIdleSync, mailIdleForeground
	mailIdleSync = func(a mailcalAccount) { synced <- a.ID }
	mailIdleForeground = func() bool { return true }
	t.Cleanup(func() { mailIdleSync, mailIdleForeground = prevSync, prevFg })

	mailIdleReconcile()
	// The waiter discovers there is no IDLE and stops asking.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mailIdle.mu.Lock()
		_, no := mailIdle.unsupported[a.ID]
		mailIdle.mu.Unlock()
		if no {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mailIdle.mu.Lock()
	_, no := mailIdle.unsupported[a.ID]
	mailIdle.mu.Unlock()
	if !no {
		t.Fatal("the account was not recorded as having no IDLE")
	}
	for i := 0; i < 3; i++ {
		mailIdleReconcile()
		time.Sleep(20 * time.Millisecond)
	}
	if n := s.conns.Load(); n != 1 {
		t.Fatalf("a server without IDLE was dialled %d times, want once (polling covers it)", n)
	}
	select {
	case id := <-synced:
		t.Fatalf("a server without IDLE asked for a sync (%s)", id)
	default:
	}
}

// TestMailIdleDropsOnSleepAndComesBackOnWake is the lifecycle rule: the
// supervisor holds one connection per enabled account, closes it when the app
// sleeps and opens a new one on wake.
func TestMailIdleDropsOnSleepAndComesBackOnWake(t *testing.T) {
	s := newFakeIMAP(t, "IDLE")
	a := s.account(t)
	addIdleTestAccount(t, a)
	var awake atomic.Bool
	awake.Store(true)
	prevFg, prevOn, prevSync := mailIdleForeground, mailIdleOnline, mailIdleSync
	mailIdleForeground = func() bool { return awake.Load() }
	mailIdleOnline = func() bool { return true }
	mailIdleSync = func(a mailcalAccount) {}
	t.Cleanup(func() {
		mailIdleForeground, mailIdleOnline, mailIdleSync = prevFg, prevOn, prevSync
		mailIdle.stopAll()
	})

	mailIdleReconcile()
	s.waitOpen(t, 1)
	s.waitIdle(t)

	awake.Store(false) // the screen went off / the app slept
	mailIdleReconcile()
	s.waitOpen(t, 0)

	awake.Store(true) // wake
	mailIdleReconcile()
	s.waitOpen(t, 1)
	s.waitIdle(t)
	if n := s.conns.Load(); n != 2 {
		t.Fatalf("connections = %d, want the first plus the one after wake", n)
	}
}

// addIdleTestAccount puts an account in the real store for the supervisor, and
// takes it out again.
func addIdleTestAccount(t *testing.T, a mailcalAccount) {
	t.Helper()
	if err := mailcalSettingsUpdate(func(s *mailcalSettings) {
		out := s.Accounts[:0]
		for _, x := range s.Accounts {
			if x.ID != a.ID {
				out = append(out, x)
			}
		}
		s.Accounts = append(out, a)
	}); err != nil {
		t.Fatalf("save the test account: %v", err)
	}
	t.Cleanup(func() {
		_ = mailcalSettingsUpdate(func(s *mailcalSettings) {
			out := s.Accounts[:0]
			for _, x := range s.Accounts {
				if x.ID != a.ID {
					out = append(out, x)
				}
			}
			s.Accounts = out
		})
	})
}

// stopAll ends every waiter and forgets the accounts' IDLE verdicts.
func (g *mailIdleGroup) stopAll() {
	g.mu.Lock()
	for id, w := range g.waiters {
		w.cancel()
		delete(g.waiters, id)
	}
	g.unsupported = map[string]string{}
	g.mu.Unlock()
}
