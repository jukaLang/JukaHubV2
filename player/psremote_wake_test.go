package main

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// psWakeTestSeams gives a test a system-picked source port on the loopback
// address, an empty memory of what consoles said, the default worker timings
// and no wake in flight, and puts everything back after.
func psWakeTestSeams(t *testing.T) {
	t.Helper()
	prevPorts, prevOverride := psWakeLocalPorts, psWakePortOverride
	prevListen, prevAddrs, prevTimings := psWakeListenIP, psWakeAddrOverride, psWakeWorkerTimings
	psWakeLocalPorts = [2]int{0, 0}
	psWakeListenIP = net.IPv4(127, 0, 0, 1)
	psWakeAddrOverride = nil
	psWakeWorkerTimings = psWakeOptions{}
	psSeen.mu.Lock()
	prevSeen, prevDone := psSeen.m, psSeen.scanDone
	psSeen.m, psSeen.scanDone = nil, time.Time{}
	psSeen.mu.Unlock()
	t.Cleanup(func() {
		psWakeLocalPorts, psWakePortOverride = prevPorts, prevOverride
		psWakeListenIP, psWakeAddrOverride, psWakeWorkerTimings = prevListen, prevAddrs, prevTimings
		psSeen.mu.Lock()
		psSeen.m, psSeen.scanDone = prevSeen, prevDone
		psSeen.mu.Unlock()
		psWakeState.mu.Lock()
		psWakeState.run = nil
		psWakeState.mu.Unlock()
	})
}

// psWakeTestOptions are the wake timings shrunk for a test. The reach is a
// whole second: a fake console on a loaded runner can be slow to answer
// first, and every loop returns as soon as it hears what it waits for.
func psWakeTestOptions() psWakeOptions {
	return psWakeOptions{
		Timeout: 3 * time.Second,
		Reach:   time.Second,
		Poll:    20 * time.Millisecond,
		Resend:  60 * time.Millisecond,
		Settle:  5 * time.Millisecond,
	}
}

// psWakeTestConsole is a paired PS5 on the loopback address.
func psWakeTestConsole() psConsole {
	return psConsole{
		Host:      "127.0.0.1",
		Name:      "Test PS5",
		PS5:       true,
		Target:    psTargetPS5_1,
		RegistKey: "1a2b3c4d",
		RPKey:     "AAAAAAAAAAAAAAAAAAAAAA==",
		ServerMAC: "a1b2c3d4e5f6",
	}
}

// psFakeReply is a PS5's discovery answer in the given state.
func psFakeReply(state, hostID string) []byte {
	status := "HTTP/1.1 620 Server Standby"
	if state == "ready" {
		status = "HTTP/1.1 200 Ok"
	}
	if hostID == "" {
		hostID = "A1B2C3D4E5F6"
	}
	return []byte(status + "\nhost-id:" + hostID + "\nhost-type:PS5\nhost-name:Test PS5\n" +
		"host-request-port:997\ndevice-discovery-protocol-version:00030010\nsystem-version:08050001\n")
}

// psFakeConsole is a console on 127.0.0.1 that answers SRCH and records
// WAKEUP, the way a PS5 in rest mode does.
type psFakeConsole struct {
	conn *net.UDPConn

	mu sync.Mutex
	// Set before it starts.
	silent     bool   // answers nothing at all
	readyNow   bool   // is on already
	neverWakes bool   // stays in rest mode whatever it is sent
	readyAfter int    // standby answers after the first wake before it is ready
	hostID     string // the MAC it names ("" = the test console's)
	// Recorded.
	wakes     []string
	probes    int
	sinceWake int
}

func startPSFakeConsole(t *testing.T, setup func(f *psFakeConsole)) *psFakeConsole {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skipf("no loopback UDP here: %v", err)
	}
	f := &psFakeConsole{conn: conn}
	if setup != nil {
		setup(f)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if reply := f.answer(string(buf[:n])); reply != nil {
				_, _ = conn.WriteToUDP(reply, from)
			}
		}
	}()
	prev := psWakePortOverride
	psWakePortOverride = conn.LocalAddr().(*net.UDPAddr).Port
	t.Cleanup(func() {
		psWakePortOverride = prev
		_ = conn.Close()
		<-done
	})
	return f
}

// addr is where the fake listens, for psWakeAddrOverride.
func (f *psFakeConsole) addr() string {
	return "127.0.0.1:" + strconv.Itoa(f.conn.LocalAddr().(*net.UDPAddr).Port)
}

func (f *psFakeConsole) answer(msg string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasPrefix(msg, "WAKEUP * HTTP/1.1\n"):
		f.wakes = append(f.wakes, msg)
		return nil
	case strings.HasPrefix(msg, "SRCH * HTTP/1.1\n"):
		f.probes++
		if f.silent {
			return nil
		}
		state := "standby"
		switch {
		case f.readyNow:
			state = "ready"
		case len(f.wakes) > 0 && !f.neverWakes:
			f.sinceWake++
			if f.sinceWake > f.readyAfter {
				state = "ready"
			}
		}
		return psFakeReply(state, f.hostID)
	}
	return nil
}

func (f *psFakeConsole) wakesSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.wakes...)
}

func (f *psFakeConsole) probesSeen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probes
}

// psWaitFor polls cond until it holds or within has passed: the fake's reader
// runs on its own goroutine, so what it recorded is read with a deadline.
func psWaitFor(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// psToastNow is the toast on screen.
func psToastNow() string {
	currentToastMu.Lock()
	defer currentToastMu.Unlock()
	return currentToast.Message
}

// psTbxQueued counts the closures waiting for the main loop.
func psTbxQueued() int {
	tbxUIQueue.mu.Lock()
	defer tbxUIQueue.mu.Unlock()
	return len(tbxUIQueue.fns)
}

// psStartTestWorker registers a wake for c and runs its worker, returning a
// channel closed when the worker is done.
func psStartTestWorker(t *testing.T, c psConsole, play bool) (*psWakeRun, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	run := &psWakeRun{host: c.Host, label: c.label(), play: play, started: time.Now(), cancel: cancel}
	if !psWakeBegin(run) {
		cancel()
		t.Fatal("a wake was already running")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		psWakeWorker(ctx, run, c)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return run, done
}

// psWaitDone waits for a worker.
func psWaitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the wake worker never finished")
	}
}

// psShrinkWorker gives the panel's wake test timings.
func psShrinkWorker() {
	psWakeWorkerTimings = psWakeTestOptions()
	psWakeWorkerTimings.Reach = 300 * time.Millisecond
}

// The wake signal is chiaki's, byte for byte: a console that ignores any other
// spelling must not be able to tell JukaHub from chiaki-ng.
func TestPSWakePacketIsChiakisWakeup(t *testing.T) {
	head := "WAKEUP * HTTP/1.1\nclient-type:vr\nauth-type:R\nmodel:w\napp-type:r\nuser-credential:439041101\n"
	if got, want := string(psWakePacket(439041101, true)), head+"device-discovery-protocol-version:00030010\n\x00"; got != want {
		t.Errorf("PS5 packet = %q\nwant        %q", got, want)
	}
	if got, want := string(psWakePacket(439041101, false)), head+"device-discovery-protocol-version:00020020\n\x00"; got != want {
		t.Errorf("PS4 packet = %q\nwant        %q", got, want)
	}
}

// The credential is the stored key's hex read as one number, as chiaki-ng
// reads it; a key that is not 1–8 hex characters cannot wake anything.
func TestPSWakeCredential(t *testing.T) {
	for key, want := range map[string]uint64{
		"1a2b3c4d": 439041101,
		"1A2B3C4D": 439041101,
		" 0f ":     15,
		"ffffffff": 4294967295,
	} {
		got, err := psWakeCredential(key)
		if err != nil || got != want {
			t.Errorf("psWakeCredential(%q) = %d, %v; want %d", key, got, err, want)
		}
	}
	for _, key := range []string{"", "123456789", "12g4", "0x1a2b"} {
		if _, err := psWakeCredential(key); !errors.Is(err, errPSWakeBadKey) {
			t.Errorf("psWakeCredential(%q) = %v, want errPSWakeBadKey", key, err)
		}
	}
}

func TestPSNormalizeMAC(t *testing.T) {
	for in, want := range map[string]string{
		"A1B2C3D4E5F6":      "a1b2c3d4e5f6",
		"a1:b2:c3:d4:e5:f6": "a1b2c3d4e5f6",
		"A1-B2-C3-D4-E5-F6": "a1b2c3d4e5f6",
		"000000000000":      "",
		"a1b2c3":            "",
		"g1b2c3d4e5f6":      "",
		"":                  "",
	} {
		if got := psNormalizeMAC(in); got != want {
			t.Errorf("psNormalizeMAC(%q) = %q, want %q", in, got, want)
		}
	}
}

// Discovery keeps the console's MAC: it is what follows a console to a new
// DHCP address.
func TestPSDiscoveryReadsHostID(t *testing.T) {
	payload := []byte("HTTP/1.1 620 Server Standby\r\nhost-id:A1B2C3D4E5F6\r\nhost-type:PS5\r\n" +
		"host-name:Living Room\r\ndevice-discovery-protocol-version:00030010\r\nsystem-version:08050001\r\n")
	d, ok := psParseDiscoveryResponse(payload, &net.UDPAddr{IP: net.IPv4(192, 168, 1, 33), Port: psRemotePortPS5})
	if !ok {
		t.Fatal("a standby answer was not understood")
	}
	if d.HostID != "A1B2C3D4E5F6" || psNormalizeMAC(d.HostID) != "a1b2c3d4e5f6" {
		t.Errorf("HostID = %q", d.HostID)
	}
	if d.State != "standby" || !d.PS5 || d.Host != "192.168.1.33" {
		t.Errorf("parsed %+v", d)
	}
}

// A console in rest mode gets the wake signal with its credential, is asked
// until it answers ready, and the panel remembers that it is on.
func TestPSWakeAndWaitWakesARestingConsole(t *testing.T) {
	psWakeTestSeams(t)
	f := startPSFakeConsole(t, func(f *psFakeConsole) { f.readyAfter = 2 })
	var stages []psWakeStage
	opts := psWakeTestOptions()
	opts.Progress = func(stage psWakeStage, _ time.Duration) { stages = append(stages, stage) }

	d, err := psWakeAndWait(context.Background(), psWakeTestConsole(), opts)
	if err != nil {
		t.Fatalf("psWakeAndWait: %v", err)
	}
	if d.State != "ready" {
		t.Fatalf("returned a console in state %q", d.State)
	}
	// The console only reports ready after it recorded a wake, so the record
	// is complete here.
	wakes := f.wakesSeen()
	if len(wakes) == 0 {
		t.Fatal("no wake signal reached the console")
	}
	if want := string(psWakePacket(0x1a2b3c4d, true)); wakes[0] != want {
		t.Errorf("wake signal = %q, want %q", wakes[0], want)
	}
	if !strings.Contains(wakes[0], "user-credential:439041101\n") {
		t.Errorf("the credential is not the key's hex in decimal: %q", wakes[0])
	}
	if len(stages) != 3 || stages[0] != psWakeLooking || stages[1] != psWakeWaking || stages[2] != psWakeStarting {
		t.Errorf("stages = %v, want looking, waking, starting", stages)
	}
	if state, _ := psConsoleSeenNow("127.0.0.1", time.Now()); state != psRowReady {
		t.Errorf("the panel remembers state %v, want ready", state)
	}
}

// A console that is on already is not woken and does not wait for a settle.
func TestPSWakeAndWaitLeavesAReadyConsoleAlone(t *testing.T) {
	psWakeTestSeams(t)
	f := startPSFakeConsole(t, func(f *psFakeConsole) { f.readyNow = true })
	var stages []psWakeStage
	opts := psWakeTestOptions()
	opts.Reach = 2 * time.Second
	opts.Settle = 3 * time.Second // a settle here would show in the time taken
	opts.Progress = func(stage psWakeStage, _ time.Duration) { stages = append(stages, stage) }
	began := time.Now()
	if _, err := psWakeAndWait(context.Background(), psWakeTestConsole(), opts); err != nil {
		t.Fatalf("psWakeAndWait: %v", err)
	}
	if took := time.Since(began); took >= time.Second {
		t.Errorf("a ready console took %v", took)
	}
	if n := len(f.wakesSeen()); n != 0 {
		t.Errorf("a ready console was sent %d wake signals", n)
	}
	if len(stages) != 1 || stages[0] != psWakeLooking {
		t.Errorf("stages = %v, want only looking", stages)
	}
}

// Silence is "not on this network", reported after the reach window instead
// of a minute's wait, and an address that never answered is never sent the
// credential.
func TestPSWakeAndWaitNeverWakesSilence(t *testing.T) {
	psWakeTestSeams(t)
	f := startPSFakeConsole(t, func(f *psFakeConsole) { f.silent = true })
	opts := psWakeTestOptions()
	opts.Reach = 300 * time.Millisecond
	began := time.Now()
	_, err := psWakeAndWait(context.Background(), psWakeTestConsole(), opts)
	if !errors.Is(err, errPSWakeUnreachable) {
		t.Fatalf("err = %v, want errPSWakeUnreachable", err)
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("took %v for a 300 ms reach window", took)
	}
	psWaitFor(t, "the probes", time.Second, func() bool { return f.probesSeen() > 0 })
	time.Sleep(50 * time.Millisecond)
	if n := len(f.wakesSeen()); n != 0 {
		t.Errorf("sent %d wake signals to an address that never answered", n)
	}
	if state, _ := psConsoleSeenNow("127.0.0.1", time.Now()); state == psRowReady || state == psRowResting {
		t.Errorf("silence was remembered as %v", state)
	}
}

// An address where another console answers — the lease moved on — is not this
// console, and it never gets this console's credential.
func TestPSWakeAndWaitLeavesAnotherConsoleAlone(t *testing.T) {
	psWakeTestSeams(t)
	f := startPSFakeConsole(t, func(f *psFakeConsole) { f.hostID = "0A0B0C0D0E0F" })
	opts := psWakeTestOptions()
	opts.Reach = 300 * time.Millisecond
	_, err := psWakeAndWait(context.Background(), psWakeTestConsole(), opts)
	if !errors.Is(err, errPSWakeMoved) {
		t.Fatalf("err = %v, want errPSWakeMoved", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(f.wakesSeen()); n != 0 {
		t.Errorf("another console was sent %d wake signals", n)
	}
	if state, _ := psConsoleSeenNow("127.0.0.1", time.Now()); state == psRowResting || state == psRowReady {
		t.Errorf("another console's answer was remembered as this one's: %v", state)
	}
	// The same answer without a stored MAC cannot be told apart, and is woken.
	c := psWakeTestConsole()
	c.ServerMAC = ""
	f.mu.Lock()
	f.readyAfter = 0
	f.mu.Unlock()
	if _, err := psWakeAndWait(context.Background(), c, psWakeTestOptions()); err != nil {
		t.Fatalf("without a stored MAC: %v", err)
	}
}

// A console that answers but never comes up times out, and is sent the
// signal again while it is waited on (it is a datagram).
func TestPSWakeAndWaitTimesOutAndResends(t *testing.T) {
	psWakeTestSeams(t)
	f := startPSFakeConsole(t, func(f *psFakeConsole) { f.neverWakes = true })
	opts := psWakeTestOptions()
	opts.Timeout = time.Second
	opts.Reach = time.Second
	opts.Resend = 50 * time.Millisecond
	_, err := psWakeAndWait(context.Background(), psWakeTestConsole(), opts)
	if !errors.Is(err, errPSWakeTimeout) {
		t.Fatalf("err = %v, want errPSWakeTimeout", err)
	}
	psWaitFor(t, "three wake signals", time.Second, func() bool { return len(f.wakesSeen()) >= 3 })
	if state, _ := psConsoleSeenNow("127.0.0.1", time.Now()); state != psRowResting {
		t.Errorf("the panel remembers %v, want resting", state)
	}
}

// B stops the wait at once.
func TestPSWakeAndWaitStopsOnCancel(t *testing.T) {
	psWakeTestSeams(t)
	startPSFakeConsole(t, func(f *psFakeConsole) { f.neverWakes = true })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(60*time.Millisecond, cancel)
	began := time.Now()
	_, err := psWakeAndWait(ctx, psWakeTestConsole(), psWakeTestOptions())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if took := time.Since(began); took > time.Second {
		t.Errorf("a cancel took %v to land", took)
	}
}

// A key that cannot be a credential is refused before anything is sent.
func TestPSWakeAndWaitRefusesABadKey(t *testing.T) {
	psWakeTestSeams(t)
	f := startPSFakeConsole(t, nil)
	c := psWakeTestConsole()
	c.RegistKey = "not-hex"
	if _, err := psWakeAndWait(context.Background(), c, psWakeTestOptions()); !errors.Is(err, errPSWakeBadKey) {
		t.Fatalf("err = %v, want errPSWakeBadKey", err)
	}
	time.Sleep(20 * time.Millisecond)
	if f.probesSeen() != 0 || len(f.wakesSeen()) != 0 {
		t.Error("something was sent with a bad pairing key")
	}
	if psWakeNotHome(errPSWakeBadKey) || psWakeNotHome(errPSWakeTimeout) || psWakeNotHome(context.Canceled) {
		t.Error("a bad key, a timeout or a stop was taken for a console that is away")
	}
	if !psWakeNotHome(errPSWakeUnreachable) || !psWakeNotHome(errPSWakeMoved) || !psWakeNotHome(errors.New("find ps5.lan: no such host")) {
		t.Error("silence, a moved lease or a name that does not resolve was not taken for away")
	}
}

// The credential goes to one console's address or nowhere: never to the
// unspecified address, a multicast group or a broadcast address.
func TestPSWakeTargetRefusesWhatReachesMany(t *testing.T) {
	psWakeTestSeams(t)
	for _, host := range []string{"255.255.255.255", "224.0.0.251", "0.0.0.0", "::", "ff02::1"} {
		if addr, err := psWakeTarget(host, true); err == nil {
			t.Errorf("psWakeTarget(%q) = %v, want a refusal", host, addr)
		}
	}
	for _, host := range []string{"192.0.2.10", "2001:db8::1"} {
		if _, err := psWakeTarget(host, true); err != nil {
			t.Errorf("psWakeTarget(%q): %v", host, err)
		}
	}
	// The directed broadcast of a network this machine is on, when it has one.
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil || len(ipNet.Mask) != net.IPv4len {
				continue
			}
			if ones, _ := ipNet.Mask.Size(); ones >= 31 {
				continue
			}
			broadcast := make(net.IP, net.IPv4len)
			for i := range broadcast {
				broadcast[i] = ipNet.IP.To4()[i] | ^ipNet.Mask[i]
			}
			if _, err := psWakeTarget(broadcast.String(), false); err == nil {
				t.Errorf("the directed broadcast %s was accepted", broadcast)
			}
			return
		}
	}
}

// A paired console follows its MAC to a new address and keeps its keys; a
// name stays a name; two consoles can swap; nothing lands on an address
// another paired console still holds; one entry per MAC survives; a MAC that
// answers from two addresses is not followed.
func TestPSRehomeConsolesFollowsTheMAC(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	keyed := func(host, mac string, pairedAt time.Time) psConsole {
		return psConsole{Host: host, Name: "PS5", PS5: true, RegistKey: "1a2b3c4d",
			RPKey: "AAAAAAAAAAAAAAAAAAAAAA==", ServerMAC: mac, PairedAt: pairedAt}
	}
	seen := func(host, id string) psDiscovered { return psDiscovered{Host: host, HostID: id, State: "standby"} }

	moved, changed := psRehomeConsoles([]psConsole{keyed("192.168.1.20", "a1b2c3d4e5f6", t0)},
		[]psDiscovered{seen("192.168.1.33", "A1B2C3D4E5F6")})
	if !changed || len(moved) != 1 || moved[0].Host != "192.168.1.33" || moved[0].RegistKey != "1a2b3c4d" {
		t.Fatalf("moved = %+v, changed %v", moved, changed)
	}
	if _, changed := psRehomeConsoles(moved, []psDiscovered{seen("192.168.1.33", "A1B2C3D4E5F6")}); changed {
		t.Error("a console at its own address was changed")
	}
	if _, changed := psRehomeConsoles(moved, []psDiscovered{{Host: "192.168.1.50", State: "ready"}}); changed {
		t.Error("an answer without a host-id moved something")
	}

	named, changed := psRehomeConsoles([]psConsole{keyed("ps5.local", "a1b2c3d4e5f6", t0)},
		[]psDiscovered{seen("192.168.1.33", "A1B2C3D4E5F6")})
	if changed || named[0].Host != "ps5.local" {
		t.Errorf("a console paired by name was moved to %q", named[0].Host)
	}

	swapped, changed := psRehomeConsoles(
		[]psConsole{keyed("192.168.1.20", "a1b2c3d4e5f6", t0), keyed("192.168.1.21", "0a0b0c0d0e0f", t0)},
		[]psDiscovered{seen("192.168.1.21", "A1B2C3D4E5F6"), seen("192.168.1.20", "0A0B0C0D0E0F")})
	if !changed || swapped[0].Host != "192.168.1.21" || swapped[1].Host != "192.168.1.20" {
		t.Errorf("swap = %q, %q", swapped[0].Host, swapped[1].Host)
	}

	held, changed := psRehomeConsoles(
		[]psConsole{keyed("192.168.1.20", "a1b2c3d4e5f6", t0), keyed("192.168.1.33", "0a0b0c0d0e0f", t0)},
		[]psDiscovered{seen("192.168.1.33", "A1B2C3D4E5F6")})
	if changed || held[0].Host != "192.168.1.20" || held[1].Host != "192.168.1.33" {
		t.Errorf("a move onto another paired console's address went through: %+v", held)
	}

	newer := keyed("192.168.1.40", "a1b2c3d4e5f6", t0.Add(time.Hour))
	newer.RegistKey = "5e6f7a8b"
	single, changed := psRehomeConsoles([]psConsole{keyed("192.168.1.20", "a1b2c3d4e5f6", t0), newer},
		[]psDiscovered{seen("192.168.1.33", "A1B2C3D4E5F6")})
	if !changed || len(single) != 1 || single[0].Host != "192.168.1.33" || single[0].RegistKey != "5e6f7a8b" {
		t.Errorf("duplicates = %+v, want the newest pairing alone at the new address", single)
	}

	// Files from before pairings were timed: the entry added last wins.
	later := keyed("192.168.1.41", "a1b2c3d4e5f6", time.Time{})
	later.RegistKey = "0badcafe"
	tie, changed := psRehomeConsoles([]psConsole{keyed("192.168.1.20", "a1b2c3d4e5f6", time.Time{}), later},
		[]psDiscovered{seen("192.168.1.33", "A1B2C3D4E5F6")})
	if !changed || len(tie) != 1 || tie[0].RegistKey != "0badcafe" {
		t.Errorf("tie = %+v, want the entry added last", tie)
	}

	leftover, changed := psRehomeConsoles(
		[]psConsole{keyed("192.168.1.20", "a1b2c3d4e5f6", t0), {Host: "192.168.1.33", Name: "old"}},
		[]psDiscovered{seen("192.168.1.33", "A1B2C3D4E5F6")})
	if !changed || len(leftover) != 1 || leftover[0].Host != "192.168.1.33" || !leftover[0].paired() {
		t.Errorf("leftover = %+v, want the paired console alone at the new address", leftover)
	}

	if _, changed := psRehomeConsoles([]psConsole{keyed("192.168.1.20", "a1b2c3d4e5f6", t0)},
		[]psDiscovered{seen("192.168.1.33", "A1B2C3D4E5F6"), seen("192.168.1.34", "A1B2C3D4E5F6")}); changed {
		t.Error("a MAC that answered from two addresses was followed")
	}
}

// psWakeTestHostsFile points psremote.json at a fresh temporary file.
func psWakeTestHostsFile(t *testing.T) {
	t.Helper()
	prevPath := psHostsPathOverride
	psHostsPathOverride = filepath.Join(t.TempDir(), "psremote.json")
	t.Cleanup(func() { psHostsPathOverride = prevPath })
}

// The panel's sweep moves the stored pairing, so the next stream goes to the
// address the console answers at.
func TestPSNoteScanDoneMovesTheStoredPairing(t *testing.T) {
	psWakeTestSeams(t)
	psWakeTestHostsFile(t)
	c := psWakeTestConsole()
	c.Host = "192.168.1.20"
	if err := savePSHosts(psHostsFile{Hosts: []psConsole{c}}); err != nil {
		t.Fatal(err)
	}
	psNoteScanDone([]psDiscovered{{Host: "192.168.1.33", HostID: "A1B2C3D4E5F6", State: "standby", PS5: true}}, true)
	hosts := loadPSHosts().Hosts
	if len(hosts) != 1 || hosts[0].Host != "192.168.1.33" || hosts[0].RegistKey != c.RegistKey || hosts[0].RPKey != c.RPKey {
		t.Fatalf("stored = %+v", hosts)
	}
	if state, _ := psConsoleSeenNow("192.168.1.33", time.Now()); state != psRowResting {
		t.Errorf("the moved console reads %v, want resting", state)
	}
}

// A console paired under a host name keeps the name, and its row reads what
// the sweep heard from its MAC.
func TestPSNoteScanDoneRemembersAConsolePairedByName(t *testing.T) {
	psWakeTestSeams(t)
	psWakeTestHostsFile(t)
	c := psWakeTestConsole()
	c.Host = "ps5.lan"
	if err := savePSHosts(psHostsFile{Hosts: []psConsole{c}}); err != nil {
		t.Fatal(err)
	}
	psNoteScanDone([]psDiscovered{{Host: "192.168.1.33", HostID: "A1B2C3D4E5F6", State: "standby", PS5: true}}, true)
	if state, _ := psConsoleSeenNow("ps5.lan", time.Now()); state != psRowResting {
		t.Errorf("the named console reads %v, want resting", state)
	}
	if hosts := loadPSHosts().Hosts; len(hosts) != 1 || hosts[0].Host != "ps5.lan" {
		t.Errorf("stored = %+v, want the name kept", hosts)
	}
}

// A sweep that moves a pairing waits for psHostsMu and reads the file only
// then: a pairing saved meanwhile survives the move.
func TestPSRehomeStoredWaitsForTheHostsLock(t *testing.T) {
	psWakeTestSeams(t)
	psWakeTestHostsFile(t)
	c := psWakeTestConsole()
	c.Host = "192.168.1.20"
	if err := savePSHosts(psHostsFile{Hosts: []psConsole{c}}); err != nil {
		t.Fatal(err)
	}

	psHostsMu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		psRehomeStored([]psDiscovered{{Host: "192.168.1.33", HostID: "A1B2C3D4E5F6", State: "standby", PS5: true}})
	}()
	// A pairing lands while the sweep waits for the lock.
	state := loadPSHosts()
	state.Hosts = append(state.Hosts, psConsole{Host: "192.168.1.40", Name: "Den", RegistKey: "5e6f7a8b",
		RPKey: "AAAAAAAAAAAAAAAAAAAAAA==", ServerMAC: "0a0b0c0d0e0f"})
	err := savePSHostsLocked(state)
	select {
	case <-done:
		psHostsMu.Unlock()
		t.Fatal("the sweep did not wait for psHostsMu")
	default:
	}
	psHostsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	psWaitDone(t, done)

	byHost := map[string]psConsole{}
	for _, h := range loadPSHosts().Hosts {
		byHost[h.Host] = h
	}
	if moved, ok := byHost["192.168.1.33"]; !ok || moved.RegistKey != c.RegistKey {
		t.Errorf("the console did not move with its keys: %+v", byHost)
	}
	if _, ok := byHost["192.168.1.40"]; !ok {
		t.Errorf("the pairing saved meanwhile was lost: %+v", byHost)
	}
	if _, ok := byHost["192.168.1.20"]; ok {
		t.Errorf("the old address is still stored: %+v", byHost)
	}
}

// What a row says follows the sweeps and probes: nothing heard, resting, not
// heard by the last sweep, ready again, and missed by a wake.
func TestPSConsoleSeenNowFollowsTheSweeps(t *testing.T) {
	psWakeTestSeams(t)
	const host = "192.168.1.20"
	if state, _ := psConsoleSeenNow(host, time.Now()); state != psRowUnknown {
		t.Fatalf("before any sweep: %v", state)
	}
	psNoteScanDone(nil, false)
	if state, _ := psConsoleSeenNow(host, time.Now()); state != psRowUnknown {
		t.Fatalf("a sweep that did not run marked it %v", state)
	}
	psNoteScanDone([]psDiscovered{{Host: host, State: "standby"}}, true)
	if state, _ := psConsoleSeenNow(host, time.Now()); state != psRowResting {
		t.Fatalf("after a standby answer: %v", state)
	}
	if state, _ := psConsoleSeenNow("192.168.1.21", time.Now()); state != psRowSilent {
		t.Fatalf("a console the sweep did not hear: %v", state)
	}
	time.Sleep(2 * time.Millisecond)
	psNoteScanDone(nil, true)
	if state, _ := psConsoleSeenNow(host, time.Now()); state != psRowSilent {
		t.Fatalf("after a sweep that did not hear it: %v", state)
	}
	psNoteState(host, psDiscovered{Host: host, State: "ready"})
	if state, _ := psConsoleSeenNow(host, time.Now()); state != psRowReady {
		t.Fatalf("after a ready probe: %v", state)
	}
	psMarkMissed(host)
	if state, _ := psConsoleSeenNow(host, time.Now()); state != psRowMissed {
		t.Fatalf("after a missed wake: %v", state)
	}
	if state, _ := psConsoleSeenNow(host, time.Now().Add(psSeenFresh+time.Second)); state == psRowMissed || state == psRowReady {
		t.Fatalf("an old answer still counts: %v", state)
	}
}

// A paired console's row says what A will do; the console being woken keeps A
// (which says it is already waking) and its footer offers only the stop.
func TestPSPanelPairedRowFollowsTheConsole(t *testing.T) {
	psWakeTestSeams(t)
	const host = "192.168.1.20"
	if detail, status, action := psPanelPairedRow(host, 0); status != "Play" || action != psActionPlay ||
		!strings.Contains(detail, "Ready to stream · "+psQualityPresets[0].Label) {
		t.Errorf("unknown console: %q %q %v", detail, status, action)
	}
	psNoteState(host, psDiscovered{Host: host, State: "standby"})
	if detail, status, action := psPanelPairedRow(host, 0); status != "Wake" || action != psActionPlay || !strings.Contains(detail, "rest mode") {
		t.Errorf("resting console: %q %q %v", detail, status, action)
	}
	psMarkMissed(host)
	if _, status, action := psPanelPairedRow(host, 0); status != "Try anyway" || action != psActionPlay {
		t.Errorf("missed console: %q %v", status, action)
	}
	run := &psWakeRun{host: host, label: "PS5", play: true, started: time.Now().Add(-12 * time.Second),
		stage: psWakeWaking, cancel: func() {}}
	if !psWakeBegin(run) {
		t.Fatal("a wake was already running")
	}
	if psWakeBegin(&psWakeRun{host: "other", cancel: func() {}}) {
		t.Error("a second wake started next to the first")
	}
	detail, status, action := psPanelPairedRow(host, 0)
	if status != "…" || action != psActionPlay || !strings.Contains(detail, "12 s") {
		t.Errorf("waking console: %q %q %v", detail, status, action)
	}
	rows := []chiakiRow{{host: host, action: psActionPlay}, {host: "192.168.1.21", action: psActionPlay}}
	if hints := psPanelConsoleHints(rows, 0); strings.Contains(hints, "Forget") {
		t.Errorf("the console being woken offers %q", hints)
	}
	if hints := psPanelConsoleHints(rows, 1); !strings.Contains(hints, "Forget") {
		t.Errorf("another console's footer lost its actions: %q", hints)
	}
	psWakeFinish(run)
	if _, ok := psWakeRunning(); ok {
		t.Error("the finished wake is still running")
	}
}

// A failed wake says what to do first and stays short enough for a toast.
func TestPSWakeFailureTextActsFirst(t *testing.T) {
	ps5 := psConsole{Host: "192.168.1.20", Name: "Living Room", PS5: true}
	text := psWakeFailureText(ps5, errPSWakeUnreachable, true)
	act, fix := strings.Index(text, "connect anyway"), strings.Index(text, "Enable Turning On PS5 from Network")
	if act < 0 || fix < 0 || act > fix || !strings.Contains(text, "Living Room") {
		t.Errorf("unreachable text %q: the action must come first, then the setting", text)
	}
	if n := utf8.RuneCountInString(text); n > 150 {
		t.Errorf("unreachable text is %d characters: %q", n, text)
	}
	if text := psWakeFailureText(ps5, errPSWakeUnreachable, false); strings.Contains(text, "connect anyway") {
		t.Errorf("a wake without a stream offers to connect: %q", text)
	}
	ps4 := psConsole{Host: "192.168.1.21", Name: "Den", Target: psTargetPS4FW10}
	if text := psWakeFailureText(ps4, errPSWakeTimeout, false); !strings.Contains(text, "Enable Turning On PS4 from Network") {
		t.Errorf("PS4 timeout text = %q", text)
	}
	if text := psWakeFailureText(ps5, errPSWakeMoved, true); !strings.Contains(text, "192.168.1.20") {
		t.Errorf("moved text = %q", text)
	}
}

// A stop and a result never both count: whichever takes the run first wins.
func TestPSWakeClaimAndCancel(t *testing.T) {
	psWakeTestSeams(t)
	done := &psWakeRun{host: "a", label: "A · PS5", cancel: func() {}}
	if !psWakeBegin(done) || !psWakeClaim(done) {
		t.Fatal("a finished wake's result did not count")
	}
	if _, ok := psWakeRunning(); ok {
		t.Error("a claimed wake is still running")
	}
	stopped := &psWakeRun{host: "b", label: "B · PS5", cancel: func() {}}
	if !psWakeBegin(stopped) {
		t.Fatal("a wake could not start")
	}
	if !psWakeCancelRunning() {
		t.Fatal("B found nothing to stop")
	}
	if psWakeClaim(stopped) {
		t.Error("the result of a stopped wake still counted")
	}
	if psWakeCancelRunning() {
		t.Error("B stopped a wake twice")
	}
}

// X on a resting console wakes it, says so once, and lets go of the run.
func TestPSWakeWorkerWakesWithoutStreaming(t *testing.T) {
	psWakeTestSeams(t)
	psShrinkWorker()
	f := startPSFakeConsole(t, func(f *psFakeConsole) { f.readyAfter = 1 })
	c := psWakeTestConsole()
	_, done := psStartTestWorker(t, c, false)
	psWaitDone(t, done)
	if _, ok := psWakeRunning(); ok {
		t.Error("the wake is still running after its worker ended")
	}
	if len(f.wakesSeen()) == 0 {
		t.Error("the console was not woken")
	}
	if got, want := psToastNow(), c.label()+" is on"; got != want {
		t.Errorf("toast = %q, want %q", got, want)
	}
}

// A console with an internet address that does not answer at home is asked —
// and woken — there.
func TestPSWakeWorkerAsksTheInternetAddress(t *testing.T) {
	psWakeTestSeams(t)
	psShrinkWorker()
	home := startPSFakeConsole(t, func(f *psFakeConsole) { f.silent = true })
	away := startPSFakeConsole(t, func(f *psFakeConsole) { f.readyAfter = 1 })
	psWakeAddrOverride = map[string]string{"home.test": home.addr(), "remote.test": away.addr()}
	c := psWakeTestConsole()
	c.Host, c.RemoteHost = "home.test", "remote.test"
	_, done := psStartTestWorker(t, c, false)
	psWaitDone(t, done)
	if n := len(home.wakesSeen()); n != 0 {
		t.Errorf("the silent home address was sent %d wake signals", n)
	}
	if len(away.wakesSeen()) == 0 {
		t.Error("the console was not woken at its internet address")
	}
	if got, want := psToastNow(), c.label()+" is on"; got != want {
		t.Errorf("toast = %q, want %q", got, want)
	}
}

// Play's stream starts on the main loop, and the run stays registered until
// that frame, so no second wake can start in between. With the panel closed
// meanwhile nothing streams.
func TestPSWakeWorkerStreamsOnlyFromTheMainLoop(t *testing.T) {
	psWakeTestSeams(t)
	psShrinkWorker()
	prevPanel := chiakiPanel
	chiakiPanel = chiakiPanelState{}
	t.Cleanup(func() { chiakiPanel = prevPanel })
	tbxDrainUI()
	startPSFakeConsole(t, func(f *psFakeConsole) { f.readyNow = true })
	c := psWakeTestConsole()
	_, done := psStartTestWorker(t, c, true)
	psWaitDone(t, done)
	psWaitFor(t, "the stream to be handed to the main loop", time.Second, func() bool { return psTbxQueued() > 0 })
	if _, ok := psWakeRunning(); !ok {
		t.Error("the run was let go before the main loop took the result")
	}
	tbxDrainUI()
	if _, ok := psWakeRunning(); ok {
		t.Error("the run is still registered after the main loop took it")
	}
	if got, want := psToastNow(), c.label()+" is on — open PS Remote Play to stream"; got != want {
		t.Errorf("toast = %q, want %q", got, want)
	}
}

// B during a wake is the last word: the worker announces nothing after it.
func TestPSWakeWorkerStopIsQuiet(t *testing.T) {
	psWakeTestSeams(t)
	psShrinkWorker()
	startPSFakeConsole(t, func(f *psFakeConsole) { f.neverWakes = true })
	c := psWakeTestConsole()
	_, done := psStartTestWorker(t, c, false)
	psWaitFor(t, "the wake signal", 2*time.Second, func() bool {
		run, ok := psWakeRunning()
		return ok && run.stage == psWakeWaking
	})
	if !psWakeCancelRunning() {
		t.Fatal("B found nothing to stop")
	}
	stopped := psToastNow()
	psWaitDone(t, done)
	if got := psToastNow(); got != stopped || !strings.HasPrefix(got, "Stopped waiting") {
		t.Errorf("after B the toast is %q (was %q)", got, stopped)
	}
}
