package main

// psremote_wake.go — turn a resting PS4 or PS5 on from the handheld.
//
// A console in rest mode keeps answering discovery (psremote_discovery.go)
// with "620 Server Standby" as long as its network stays up in rest mode, and
// it listens for one more datagram on the same port: chiaki's WAKEUP. That
// packet carries the pairing's registration key as its credential — the 1–8
// hex characters JukaHub stores as regist_key, read as a hexadecimal number
// and written out in decimal, exactly as chiaki-ng's DiscoveryManager does —
// so only a device that paired with the console can wake it.
//
// What this file adds on top of the packet:
//
//   - psWakeAndWait asks the console how it is (a unicast SRCH), wakes it when
//     it answers from rest mode, keeps asking until it reports ready and then
//     gives it a moment more, because Remote Play comes up a little after
//     discovery does. A console that answers nothing at all within a few
//     seconds is reported as unreachable instead of being waited on for a
//     minute, and one whose answer names another console's MAC is reported as
//     moved;
//   - the panel's memory of what each console last answered, so a resting
//     console's row says so and Play knows whether it can stream at once;
//   - following a paired console to a new address: discovery names the
//     console's MAC (host-id) and the pairing stored it (server_mac), so a
//     router that hands the console a new DHCP lease no longer strands its
//     keys on the old address;
//   - the panel's side of it: Play wakes a resting console and then streams,
//     X only wakes it, B stops waiting, and a Controllers row leads to the
//     Bluetooth devices page. A console with an internet address is asked
//     there too when it does not answer at home.
//
// The credential is only ever sent to one console's own address, never
// broadcast, and only right after that address answered as this console: an
// address that stays silent, or where another console answers, never gets it.
// The local port comes from chiaki's discovery range (9303–9319) when one is
// free, as chiaki's own socket does, and from the system otherwise.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The wake timings. A PS5 comes out of rest mode in 10–20 s and a PS4 can take
// half a minute, so a minute covers both. Silence for psWakeReach means nothing
// listens at that address at all. A console that answered ready within
// psWakeFreshReady is streamed without asking again, and anything older than
// psSeenFresh says nothing about the console now.
const (
	psWakeTimeout    = 60 * time.Second
	psWakeReach      = 6 * time.Second
	psWakePoll       = time.Second
	psWakeResend     = 5 * time.Second
	psWakeSettle     = 3 * time.Second
	psWakeFreshReady = 30 * time.Second
	psSeenFresh      = 2 * time.Minute
)

// psActionControllers opens the Bluetooth devices page from the panel. It is
// numbered apart from the iota list in psremote_ui.go, so a row added there can
// never take its value.
const psActionControllers psPanelAction = 100

// psWakeLocalPorts is the source-port range a wake socket tries first: chiaki's
// CHIAKI_DISCOVERY_PORT_LOCAL_MIN/MAX. Tests set {0, 0}: a port the system
// picks.
var psWakeLocalPorts = [2]int{9303, 9319}

// psWakePortOverride sends every probe and wake to this port instead of the
// console's own (987 or 9302). Test seam.
var psWakePortOverride int

// psWakeListenIP is the address a wake socket binds; nil binds every address,
// as chiaki's socket does. Tests bind the loopback address.
var psWakeListenIP net.IP

// psWakeAddrOverride sends what is meant for a host to another "ip:port".
// Test seam: it gives a console's home and internet addresses two different
// fake consoles.
var psWakeAddrOverride map[string]string

// psWakeWorkerTimings replaces the panel wake's timings (zero values keep the
// defaults). Test seam.
var psWakeWorkerTimings psWakeOptions

var (
	// errPSWakeUnreachable: nothing answered at the console's address.
	errPSWakeUnreachable = errors.New("the console did not answer on this network")
	// errPSWakeTimeout: it answered, but never as ready.
	errPSWakeTimeout = errors.New("the console did not come out of rest mode in time")
	// errPSWakeMoved: another console answers at this address (a DHCP lease
	// that moved on). It is never sent this console's credential.
	errPSWakeMoved = errors.New("another console answers at this address")
	// errPSWakeBadKey: the stored pairing key cannot be a wake credential.
	errPSWakeBadKey = errors.New("the saved pairing key cannot wake the console — pair it again")
)

// psWakeStage is how far a wake has come.
type psWakeStage int

const (
	psWakeLooking  psWakeStage = iota // asking the console how it is
	psWakeWaking                      // the wake signal is out; it is coming up
	psWakeStarting                    // it is on; Remote Play needs a moment more
)

// psWakeOptions tunes one wake. Zero values take the defaults above; tests
// shorten them. A negative Settle skips the settle.
type psWakeOptions struct {
	Timeout  time.Duration
	Reach    time.Duration
	Poll     time.Duration
	Resend   time.Duration
	Settle   time.Duration
	Progress func(stage psWakeStage, elapsed time.Duration)
}

func (o psWakeOptions) withDefaults() psWakeOptions {
	if o.Timeout <= 0 {
		o.Timeout = psWakeTimeout
	}
	if o.Reach <= 0 {
		o.Reach = psWakeReach
	}
	if o.Reach > o.Timeout {
		o.Reach = o.Timeout
	}
	if o.Poll <= 0 {
		o.Poll = psWakePoll
	}
	if o.Resend <= 0 {
		o.Resend = psWakeResend
	}
	if o.Settle == 0 {
		o.Settle = psWakeSettle
	}
	return o
}

// ── The packet ───────────────────────────────────────────────────────────

// psWakePacket is chiaki's WAKEUP datagram, byte for byte: the same headers in
// the same order (chiaki_discovery_packet_fmt) and the trailing NUL chiaki
// sends as part of the packet.
func psWakePacket(credential uint64, ps5 bool) []byte {
	version := psProtoVersionPS4
	if ps5 {
		version = psProtoVersionPS5
	}
	return []byte("WAKEUP * HTTP/1.1\n" +
		"client-type:vr\n" +
		"auth-type:R\n" +
		"model:w\n" +
		"app-type:r\n" +
		"user-credential:" + strconv.FormatUint(credential, 10) + "\n" +
		"device-discovery-protocol-version:" + version + "\n\x00")
}

// psWakeCredential turns a stored regist_key into the wake credential: chiaki
// reads the key's ASCII hex as one base-16 number (QString::toULongLong with
// base 16) and refuses a key longer than eight characters.
func psWakeCredential(registKey string) (uint64, error) {
	key := strings.TrimSpace(registKey)
	if !psRegistKeyValid(key) {
		return 0, errPSWakeBadKey
	}
	credential, err := strconv.ParseUint(key, 16, 64)
	if err != nil {
		return 0, errPSWakeBadKey
	}
	return credential, nil
}

// psConsoleIsPS5 reports whether a console speaks the PS5 protocol.
func psConsoleIsPS5(c psConsole) bool {
	return c.PS5 || c.Target >= psTargetPS5
}

// psNormalizeMAC is a console MAC in one spelling: twelve lowercase hex digits,
// from the discovery header ("A1B2C3D4E5F6") or the stored pairing
// ("a1b2c3d4e5f6", "a1:b2:…"). Anything else, and the all-zero address, is "".
func psNormalizeMAC(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r == ':' || r == '-' || r == '.':
			continue
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
			b.WriteRune(r)
		case r >= 'A' && r <= 'F':
			b.WriteRune(r + ('a' - 'A'))
		default:
			return ""
		}
	}
	mac := b.String()
	if len(mac) != 12 || strings.Trim(mac, "0") == "" {
		return ""
	}
	return mac
}

// psWakeSameConsole reports whether an answer can be this console's: when the
// pairing kept the console's MAC and the answer names one, they must agree.
func psWakeSameConsole(c psConsole, d psDiscovered) bool {
	want, got := psNormalizeMAC(c.ServerMAC), psNormalizeMAC(d.HostID)
	return want == "" || got == "" || want == got
}

// ── The socket ───────────────────────────────────────────────────────────

// psWakeTarget is the console's discovery address for its protocol. A host
// name resolves the way a stream's does, an IPv6 internet address included.
// An address that reaches more than one machine is refused: the credential
// goes to one console or nowhere.
func psWakeTarget(host string, ps5 bool) (*net.UDPAddr, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, errors.New("no console address")
	}
	port := psRemotePortPS4
	if ps5 {
		port = psRemotePortPS5
	}
	if psWakePortOverride > 0 {
		port = psWakePortOverride
	}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	if override, ok := psWakeAddrOverride[host]; ok {
		address = override
	}
	// "udp" rather than "udp4": IPv4 still wins for a name that has both.
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", host, err)
	}
	if !psWakeUnicast(addr.IP) {
		return nil, fmt.Errorf("%s is not the address of one console", host)
	}
	return addr, nil
}

// psWakeUnicast reports whether ip names one machine: not the unspecified
// address, not a multicast group, not the limited broadcast and not the
// directed broadcast of a network this device is on (Go switches broadcast on
// for every UDP socket, so nothing else would stop it).
func psWakeUnicast(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.Equal(net.IPv4bcast) {
		return false
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return true
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return true
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil {
				continue
			}
			mask := ipNet.Mask
			if len(mask) == net.IPv6len {
				mask = mask[12:]
			}
			if len(mask) != net.IPv4len {
				continue
			}
			if ones, _ := mask.Size(); ones >= 31 {
				// A /31 or /32 has no broadcast address.
				continue
			}
			base := ipNet.IP.To4()
			broadcast := make(net.IP, net.IPv4len)
			for i := 0; i < net.IPv4len; i++ {
				broadcast[i] = base[i] | ^mask[i]
			}
			if ip4.Equal(broadcast) {
				return false
			}
		}
	}
	return true
}

// psWakeListen opens the socket a wake talks through, in addr's family, on a
// port from chiaki's range when one is free.
func psWakeListen(addr *net.UDPAddr) (*net.UDPConn, error) {
	network := "udp4"
	local := psWakeListenIP
	if addr != nil && addr.IP.To4() == nil {
		network = "udp6"
		if local.To4() != nil {
			// A test's IPv4 loopback cannot bind an IPv6 socket.
			local = nil
		}
	}
	low, high := psWakeLocalPorts[0], psWakeLocalPorts[1]
	for port := low; port > 0 && port <= high; port++ {
		if conn, err := net.ListenUDP(network, &net.UDPAddr{IP: local, Port: port}); err == nil {
			return conn, nil
		}
	}
	return net.ListenUDP(network, &net.UDPAddr{IP: local})
}

// psProbeOnce sends one SRCH to the console and waits up to wait for its
// answer. Replies from any other address are ignored. A send or read failure
// (no route yet, the socket closed by a cancel) is an unanswered probe.
func psProbeOnce(conn *net.UDPConn, addr *net.UDPAddr, ps5 bool, wait time.Duration) (psDiscovered, bool) {
	version := psProtoVersionPS4
	if ps5 {
		version = psProtoVersionPS5
	}
	if _, err := conn.WriteToUDP(psDiscoveryPacket(version), addr); err != nil {
		return psDiscovered{}, false
	}
	deadline := time.Now().Add(wait)
	buf := make([]byte, 2048)
	for {
		if err := conn.SetReadDeadline(deadline); err != nil {
			return psDiscovered{}, false
		}
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return psDiscovered{}, false
		}
		if from == nil || !from.IP.Equal(addr.IP) {
			continue
		}
		if d, ok := psParseDiscoveryResponse(buf[:n], from); ok {
			return d, true
		}
	}
}

// psSleepCtx waits d, or less when ctx ends first; false means it ended.
func psSleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// ── Wake and wait ────────────────────────────────────────────────────────

// psWakeAndWait brings a paired console to the point where a stream can start.
// It asks the console how it is; a ready console returns at once. A resting
// one gets the wake signal, again at most every opts.Resend while it keeps
// answering from rest mode, and is asked every opts.Poll until it answers
// ready, after which it gets opts.Settle before the answer is returned. The
// signal only ever follows an answer from this console at this address.
// Silence for opts.Reach ends in errPSWakeUnreachable, an address where only
// another console answers in errPSWakeMoved, and a console that answers but
// never as ready in errPSWakeTimeout after opts.Timeout. Cancelling ctx ends it
// at once with ctx's error.
func psWakeAndWait(ctx context.Context, c psConsole, opts psWakeOptions) (psDiscovered, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	opts = opts.withDefaults()
	credential, err := psWakeCredential(c.RegistKey)
	if err != nil {
		return psDiscovered{}, err
	}
	ps5 := psConsoleIsPS5(c)
	addr, err := psWakeTarget(c.Host, ps5)
	if err != nil {
		return psDiscovered{}, err
	}
	conn, err := psWakeListen(addr)
	if err != nil {
		return psDiscovered{}, fmt.Errorf("open the wake socket: %w", err)
	}
	defer conn.Close()
	// A cancel closes the socket, which ends a read that is still waiting.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	start := time.Now()
	stage := psWakeLooking
	report := func(next psWakeStage) {
		stage = next
		if opts.Progress != nil {
			opts.Progress(next, time.Since(start))
		}
	}
	report(psWakeLooking)
	answered, foreign := false, false
	var lastWake time.Time
	for {
		if err := ctx.Err(); err != nil {
			return psDiscovered{}, err
		}
		elapsed := time.Since(start)
		if !answered && elapsed >= opts.Reach {
			if foreign {
				return psDiscovered{}, errPSWakeMoved
			}
			return psDiscovered{}, errPSWakeUnreachable
		}
		if elapsed >= opts.Timeout {
			return psDiscovered{}, errPSWakeTimeout
		}
		round := time.Now()
		d, ok := psProbeOnce(conn, addr, ps5, opts.Poll)
		if err := ctx.Err(); err != nil {
			return psDiscovered{}, err
		}
		if ok && !psWakeSameConsole(c, d) {
			// Another console answers here: it is not this one, and it is not
			// sent this one's credential.
			ok, foreign = false, true
		}
		if ok {
			answered = true
			psNoteState(c.Host, d)
			if d.State == "ready" {
				if !lastWake.IsZero() && opts.Settle > 0 {
					report(psWakeStarting)
					if !psSleepCtx(ctx, opts.Settle) {
						return psDiscovered{}, ctx.Err()
					}
				}
				return d, nil
			}
			// It answered from rest mode. The signal is a datagram, so it goes
			// again every few seconds while the console comes up.
			if lastWake.IsZero() || time.Since(lastWake) >= opts.Resend {
				if _, err := conn.WriteToUDP(psWakePacket(credential, ps5), addr); err != nil {
					if ctx.Err() != nil {
						return psDiscovered{}, ctx.Err()
					}
					logDebug("psremote", "wake signal to %s not sent: %v", c.Host, err)
				} else {
					lastWake = time.Now()
					if stage != psWakeWaking {
						report(psWakeWaking)
					}
				}
			}
		}
		if rest := opts.Poll - time.Since(round); rest > 0 && !psSleepCtx(ctx, rest) {
			return psDiscovered{}, ctx.Err()
		}
	}
}

// psWakeNotHome reports whether err means the console was not reached at that
// address — silence, another console there, a name that does not resolve on
// this network — as opposed to a stop, a console that answered but never came
// up, or a pairing key that cannot wake anything.
func psWakeNotHome(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, errPSWakeTimeout) && !errors.Is(err, errPSWakeBadKey)
}

// ── What each console last said ──────────────────────────────────────────

// psSeenEntry is a console's last answer, and when a wake last found nothing at
// its address (zero: it has not).
type psSeenEntry struct {
	state  string
	at     time.Time
	missed time.Time
}

// psSeen is that memory per address, and when the panel's last sweep finished.
// It lives in memory only: rest mode is a fact about now.
var psSeen struct {
	mu       sync.Mutex
	m        map[string]psSeenEntry
	scanDone time.Time
}

// psSeenMax bounds the memory; a LAN has a handful of consoles.
const psSeenMax = 64

// psSeenPutLocked records an answer, which also clears a miss. psSeen.mu held.
func psSeenPutLocked(host, state string, at time.Time) {
	if psSeen.m == nil || len(psSeen.m) >= psSeenMax {
		psSeen.m = make(map[string]psSeenEntry, 8)
	}
	psSeen.m[host] = psSeenEntry{state: state, at: at}
}

// psNoteState records what a console answered to a probe, under the address
// it was asked at (a typed host name) and the one it answered from.
func psNoteState(host string, d psDiscovered) {
	host = strings.TrimSpace(host)
	now := time.Now()
	psSeen.mu.Lock()
	defer psSeen.mu.Unlock()
	if host != "" {
		psSeenPutLocked(host, d.State, now)
	}
	if from := strings.TrimSpace(d.Host); from != "" && from != host {
		psSeenPutLocked(from, d.State, now)
	}
}

// psNoteScanDone records a finished sweep: what answered, and — when the sweep
// really ran — that every console which stayed silent is one to wake first. A
// paired console that answered from a new address is moved there first, and
// one paired under a host name is remembered under that name too (its row
// looks the name up; the sweep heard it at an IP). The panel's scan calls it
// with every result, an empty one too.
func psNoteScanDone(found []psDiscovered, swept bool) {
	withMAC := false
	for _, d := range found {
		withMAC = withMAC || psNormalizeMAC(d.HostID) != ""
	}
	var named map[string]string
	if withMAC {
		named = psRehomeStored(found)
	}
	now := time.Now()
	psSeen.mu.Lock()
	defer psSeen.mu.Unlock()
	for _, d := range found {
		if host := strings.TrimSpace(d.Host); host != "" {
			psSeenPutLocked(host, d.State, now)
		}
	}
	for host, state := range named {
		psSeenPutLocked(host, state, now)
	}
	if swept {
		psSeen.scanDone = now
	}
}

// psMarkMissed records that a wake found nothing at host. The row then offers
// to connect anyway, which is the old behaviour and what a console behind a
// VPN or a port forward (whose discovery never answers) needs.
func psMarkMissed(host string) {
	host = strings.TrimSpace(host)
	if host == "" {
		return
	}
	psSeen.mu.Lock()
	defer psSeen.mu.Unlock()
	if psSeen.m == nil {
		psSeen.m = make(map[string]psSeenEntry, 8)
	}
	entry := psSeen.m[host]
	entry.missed = time.Now()
	psSeen.m[host] = entry
}

// psRowState is what the panel can say about a paired console right now.
type psRowState int

const (
	psRowUnknown psRowState = iota // nothing heard yet (the first sweep may be running)
	psRowReady                     // answered ready
	psRowResting                   // answered from rest mode
	psRowSilent                    // the last sweep did not hear it
	psRowMissed                    // a wake found nothing there
)

// psConsoleSeenNow classifies host and says when it last answered.
func psConsoleSeenNow(host string, now time.Time) (psRowState, time.Time) {
	psSeen.mu.Lock()
	defer psSeen.mu.Unlock()
	entry, ok := psSeen.m[strings.TrimSpace(host)]
	if ok && !entry.missed.IsZero() && now.Sub(entry.missed) < psSeenFresh {
		return psRowMissed, entry.at
	}
	sweptLately := !psSeen.scanDone.IsZero() && now.Sub(psSeen.scanDone) < psSeenFresh
	if !ok || entry.at.IsZero() || now.Sub(entry.at) >= psSeenFresh {
		if sweptLately {
			return psRowSilent, time.Time{}
		}
		return psRowUnknown, time.Time{}
	}
	if sweptLately && entry.at.Before(psSeen.scanDone) {
		// It answered once, but the sweep since then did not hear it.
		return psRowSilent, entry.at
	}
	switch entry.state {
	case "ready":
		return psRowReady, entry.at
	case "standby":
		return psRowResting, entry.at
	}
	return psRowUnknown, entry.at
}

// ── A console that moved ─────────────────────────────────────────────────

// psAnswersByMAC is each console MAC a sweep heard, with its answer. A MAC that
// answered from two addresses in one sweep is left out: following it would
// follow whichever answer sorted first — a stale lease, or a spoof.
func psAnswersByMAC(found []psDiscovered) map[string]psDiscovered {
	byMAC := map[string]psDiscovered{}
	ambiguous := map[string]bool{}
	for _, d := range found {
		mac := psNormalizeMAC(d.HostID)
		addr := strings.TrimSpace(d.Host)
		if mac == "" || addr == "" {
			continue
		}
		if prev, dup := byMAC[mac]; dup {
			if strings.TrimSpace(prev.Host) != addr {
				ambiguous[mac] = true
			}
			continue
		}
		byMAC[mac] = d
	}
	for mac := range ambiguous {
		delete(byMAC, mac)
	}
	return byMAC
}

// psRehomeConsoles follows paired consoles to the address their MAC answered
// from, keeping their keys: a new DHCP lease used to leave the pairing on the
// old address, where nothing answers. At most one entry per MAC survives (the
// newest pairing, or on a tie the entry added last; older entries for the same
// console are stale addresses). A console paired under a host name keeps the
// name, which DNS already follows. A move onto an address another paired
// console still holds is skipped, so a list never has two rows for one
// address; a keyless leftover there goes.
func psRehomeConsoles(hosts []psConsole, found []psDiscovered) ([]psConsole, bool) {
	answers := psAnswersByMAC(found)
	if len(answers) == 0 {
		return hosts, false
	}
	moves := map[int]string{}
	drop := map[int]bool{}
	// carry is what the user set on a dropped entry (its internet address,
	// its login passcode: psremote_console_opts.go), for the entry kept.
	carry := map[int]psConsole{}
	for mac, d := range answers {
		addr := strings.TrimSpace(d.Host)
		keep := -1
		named := false
		for i, c := range hosts {
			if !c.paired() || psNormalizeMAC(c.ServerMAC) != mac {
				continue
			}
			if net.ParseIP(strings.TrimSpace(c.Host)) == nil {
				named = true
				break
			}
			if keep < 0 || !c.PairedAt.Before(hosts[keep].PairedAt) {
				keep = i
			}
		}
		if named || keep < 0 {
			continue
		}
		for i, c := range hosts {
			if i != keep && c.paired() && psNormalizeMAC(c.ServerMAC) == mac {
				drop[i] = true
				kept := carry[keep]
				if kept.RemoteHost == "" {
					kept.RemoteHost = c.RemoteHost
				}
				if kept.LoginPIN == "" {
					kept.LoginPIN = c.LoginPIN
				}
				carry[keep] = kept
			}
		}
		if strings.TrimSpace(hosts[keep].Host) != addr {
			moves[keep] = addr
		}
	}
	// Moves that would land on an address another console keeps are dropped,
	// and the check repeats: a cancelled move can block one that relied on it.
	for blocked := true; blocked; {
		blocked = false
		for i, addr := range moves {
			for j, c := range hosts {
				if j == i || drop[j] || strings.TrimSpace(c.Host) != addr {
					continue
				}
				if _, leaving := moves[j]; leaving {
					continue
				}
				if !c.paired() {
					drop[j] = true
					continue
				}
				delete(moves, i)
				blocked = true
				break
			}
		}
	}
	if len(moves) == 0 && len(drop) == 0 {
		return hosts, false
	}
	out := make([]psConsole, 0, len(hosts))
	for i, c := range hosts {
		if drop[i] {
			continue
		}
		if addr, ok := moves[i]; ok {
			c.Host = addr
		}
		if extra, ok := carry[i]; ok {
			if c.RemoteHost == "" {
				c.RemoteHost = extra.RemoteHost
			}
			if c.LoginPIN == "" {
				c.LoginPIN = extra.LoginPIN
			}
		}
		out = append(out, c)
	}
	return out, true
}

// psRehomeStored applies psRehomeConsoles to psremote.json. It holds psHostsMu
// from the load to the save, so a pairing or a Forget saved meanwhile is
// neither lost nor undone, and it only writes when a console really moved. It
// returns what the sweep heard from consoles paired under a host name, keyed by
// that name (psNoteScanDone remembers it for their rows).
func psRehomeStored(found []psDiscovered) map[string]string {
	psHostsMu.Lock()
	defer psHostsMu.Unlock()
	state := loadPSHosts()
	hosts, changed := psRehomeConsoles(state.Hosts, found)
	if changed {
		state.Hosts = hosts
		if err := savePSHostsLocked(state); err != nil {
			logWarn("psremote", "following a console to its new address failed: %v", err)
		} else {
			logInfo("psremote", "a paired console answered from a new address; its pairing moved with it")
		}
	}
	return psNamedAnswers(hosts, found)
}

// psNamedAnswers is what a sweep heard from the consoles in hosts that are
// paired under a host name, keyed by the name; nil when there are none.
func psNamedAnswers(hosts []psConsole, found []psDiscovered) map[string]string {
	answers := psAnswersByMAC(found)
	var named map[string]string
	for _, c := range hosts {
		host := strings.TrimSpace(c.Host)
		if host == "" || net.ParseIP(host) != nil {
			continue
		}
		d, ok := answers[psNormalizeMAC(c.ServerMAC)]
		if !ok {
			continue
		}
		if named == nil {
			named = map[string]string{}
		}
		named[host] = d.State
	}
	return named
}

// ── The panel's wake ─────────────────────────────────────────────────────

// psWakeRun is the one wake the panel has in flight. host, label, play and
// started never change after it starts; stage is written under psWakeState.mu.
type psWakeRun struct {
	host    string
	label   string
	play    bool
	started time.Time
	stage   psWakeStage
	cancel  context.CancelFunc
	// stopped is B: the result, whatever it is, no longer matters. It is set
	// under psWakeState.mu, where psWakeClaim reads it.
	stopped atomic.Bool
}

// psWakeView is a copy of a run for drawing.
type psWakeView struct {
	host    string
	label   string
	play    bool
	started time.Time
	stage   psWakeStage
}

var psWakeState struct {
	mu  sync.Mutex
	run *psWakeRun
}

// psWakeBegin makes run the panel's wake; false when one is already running.
func psWakeBegin(run *psWakeRun) bool {
	psWakeState.mu.Lock()
	defer psWakeState.mu.Unlock()
	if psWakeState.run != nil {
		return false
	}
	psWakeState.run = run
	return true
}

func psWakeSetStage(run *psWakeRun, stage psWakeStage) {
	psWakeState.mu.Lock()
	run.stage = stage
	psWakeState.mu.Unlock()
}

// psWakeFinish lets go of run, unless a newer one has taken its place.
func psWakeFinish(run *psWakeRun) {
	psWakeState.mu.Lock()
	if psWakeState.run == run {
		psWakeState.run = nil
	}
	psWakeState.mu.Unlock()
}

// psWakeClaim lets go of run for its result and reports whether that result
// still counts: false when B stopped the run first. The check and the release
// share psWakeState.mu with psWakeCancelRunning, so a stop and a result are
// never both announced.
func psWakeClaim(run *psWakeRun) bool {
	psWakeState.mu.Lock()
	defer psWakeState.mu.Unlock()
	if psWakeState.run == run {
		psWakeState.run = nil
	}
	return !run.stopped.Load()
}

// psWakeRunning is the running wake, if there is one.
func psWakeRunning() (psWakeView, bool) {
	psWakeState.mu.Lock()
	defer psWakeState.mu.Unlock()
	run := psWakeState.run
	if run == nil {
		return psWakeView{}, false
	}
	return psWakeView{host: run.host, label: run.label, play: run.play, started: run.started, stage: run.stage}, true
}

// psWakeCancelRunning is B while a wake is running: it stops waiting and
// reports whether there was anything to stop. The console may still come on
// if the signal already went out, and the toast says so.
func psWakeCancelRunning() bool {
	psWakeState.mu.Lock()
	run := psWakeState.run
	psWakeState.run = nil
	stage := psWakeLooking
	if run != nil {
		stage = run.stage
		run.stopped.Store(true)
	}
	psWakeState.mu.Unlock()
	if run == nil {
		return false
	}
	run.cancel()
	PlayBackSound()
	if stage == psWakeLooking {
		ShowToast("Stopped — "+run.label+" was not woken", ToastKindInfo)
	} else {
		ShowToast("Stopped waiting — "+run.label+" may still turn on", ToastKindInfo)
	}
	return true
}

// psPanelPlayOrWake is A on a paired console. One that answered ready a moment
// ago streams at once, and so does one a wake found nothing for (the user is
// asking to connect anyway). Every other console is asked first and woken when
// it rests; the stream starts by itself once it is up. A on the console being
// woken only says so.
func psPanelPlayOrWake(c psConsole) {
	if !c.paired() {
		ShowToast("Pair this console first", ToastKindWarning)
		return
	}
	// A missing chiaki-stream is installed first; this press is made again
	// once it is there (psremote_helpers_install.go).
	if psHelpersHoldPlay(c) {
		return
	}
	if !psRemoteEngine().installed() {
		ShowToast("The streaming helper is missing — "+psRuntimeInstallHint(), ToastKindWarning)
		return
	}
	if run, ok := psWakeRunning(); ok {
		psWakeBusyToast(run, c)
		return
	}
	state, at := psConsoleSeenNow(c.Host, time.Now())
	if state == psRowMissed || (state == psRowReady && time.Since(at) < psWakeFreshReady) {
		// psPanelPlay closes the panel itself once the stream is under way: it
		// reads the quality, volume and Account-ID from the open panel.
		psPanelPlay(c)
		return
	}
	psPanelStartWake(c, true)
}

// psPanelWakeFocused is X: wake the focused console without streaming, for a
// console that should be on for something else (a download, the TV).
func psPanelWakeFocused() {
	rows := chiakiRows()
	if chiakiPanel.cursor < 0 || chiakiPanel.cursor >= len(rows) {
		return
	}
	host := rows[chiakiPanel.cursor].host
	if host == "" {
		return
	}
	c, ok := psPanelConsole(host)
	if !ok || !c.paired() {
		ShowToast("Pair this console first — waking it needs its pairing key", ToastKindInfo)
		return
	}
	if run, ok := psWakeRunning(); ok {
		psWakeBusyToast(run, c)
		return
	}
	psPanelStartWake(c, false)
}

// psWakeBusyToast answers a second press while a wake runs.
func psWakeBusyToast(run psWakeView, c psConsole) {
	stop := hintSentence("%s stops it", "B", "Stop")
	if run.host == c.Host {
		ShowToast("Already waking "+run.label+" — "+stop, ToastKindInfo)
		return
	}
	ShowToast("Waking "+run.label+" first — "+stop, ToastKindInfo)
}

// psPanelStartWake runs a wake in the background; play starts the stream when
// the console is up.
func psPanelStartWake(c psConsole, play bool) {
	ctx, cancel := context.WithCancel(context.Background())
	run := &psWakeRun{host: c.Host, label: c.label(), play: play, started: time.Now(), cancel: cancel}
	if !psWakeBegin(run) {
		cancel()
		return
	}
	PlayActivateSound()
	go psWakeWorker(ctx, run, c)
}

// psWakeReachAway is the silence that means "not at home" for a console with
// an internet address: the wake then asks at that address (it answers there
// when the router forwards its discovery port), and the stream goes there
// (psChooseStreamHost) whether it answered or not.
const psWakeReachAway = 2500 * time.Millisecond

// psWakeWorker is the goroutine behind psPanelStartWake. The stream itself is
// started on the main loop (tbxPost): it closes the panel and quits JukaHub,
// and only when the panel is still up on the console list — a user who closed
// it or is typing meanwhile gets a toast instead of a screen yanked away.
func psWakeWorker(ctx context.Context, run *psWakeRun, c psConsole) {
	woke := false
	opts := psWakeWorkerTimings
	opts.Progress = func(stage psWakeStage, _ time.Duration) {
		woke = woke || stage != psWakeLooking
		psWakeSetStage(run, stage)
	}
	if !run.play {
		// Only waking: nothing starts, so there is no Remote Play to wait for.
		opts.Settle = -1
	}
	away := strings.TrimSpace(c.RemoteHost) != ""
	if away && opts.Reach <= 0 {
		opts.Reach = psWakeReachAway
	}
	_, err := psWakeAndWait(ctx, c, opts)
	if away && psWakeNotHome(err) {
		// Not on this network. Where the router forwards the discovery port
		// (UDP 9302 for a PS5, 987 for a PS4) the console answers at its
		// internet address too, and a resting one is woken there before the
		// stream goes out. The credential only ever wakes the console:
		// streaming needs the session key, which never leaves the handheld.
		remote := c
		remote.Host = strings.TrimSpace(c.RemoteHost)
		remoteOpts := opts
		remoteOpts.Reach = psWakeWorkerTimings.Reach
		_, err = psWakeAndWait(ctx, remote, remoteOpts)
	}
	run.cancel()
	// Not at home and not answering at the internet address either (its router
	// does not forward the discovery port): Play streams there anyway, as it
	// always did.
	streamAway := away && run.play && psWakeNotHome(err)
	if !run.play || (err != nil && !streamAway) {
		if psWakeClaim(run) {
			psWakeAnnounce(run, c, err, woke)
		}
		return
	}
	if streamAway {
		// The next Play within a few minutes goes straight to the stream.
		psMarkMissed(c.Host)
	}
	on := err == nil
	tbxPost(func() {
		// The run is let go only now: until this frame A and X still find it
		// running, so no second wake starts in between, and B still stops it.
		if !psWakeClaim(run) {
			return
		}
		if !chiakiPanel.open || chiakiPanel.page != psPageConsoles || virtualKeyboardActive {
			switch {
			case on && chiakiPanel.open:
				ShowToast(run.label+" is on — "+hintSentence("press %s on it to stream", "A", "Play"), ToastKindSuccess)
			case on:
				ShowToast(run.label+" is on — open PS Remote Play to stream", ToastKindSuccess)
			}
			return
		}
		console, ok := psPanelConsole(c.Host)
		if !ok || !console.paired() {
			// Forgotten (Y) while it was waking: not streamed.
			if on {
				ShowToast(run.label+" is on", ToastKindSuccess)
			}
			return
		}
		psPanelPlay(console)
	})
}

// psWakeAnnounce is the one toast of a wake that starts no stream: the console
// is on, or why it is not.
func psWakeAnnounce(run *psWakeRun, c psConsole, err error, woke bool) {
	switch {
	case err == nil && woke:
		ShowToast(run.label+" is on", ToastKindSuccess)
	case err == nil:
		ShowToast(run.label+" is already on", ToastKindInfo)
	case errors.Is(err, errPSWakeMoved):
		// A sweep finds where it went and moves the pairing there by its MAC.
		ShowToast(psWakeFailureText(c, err, run.play), ToastKindWarning)
		tbxPost(func() {
			if chiakiPanel.open {
				psPanelScan()
			}
		})
	default:
		if psWakeNotHome(err) {
			psMarkMissed(c.Host)
		}
		ShowToast(psWakeFailureText(c, err, run.play), ToastKindWarning)
	}
}

// psWakeFailureText says why a wake ended and what to do, the action first: a
// toast shows about 170 characters, and these stay under 150 for a console
// with an everyday name.
func psWakeFailureText(c psConsole, err error, play bool) string {
	name := c.label()
	setting := psRestModeSwitch(psConsoleKind(c))
	switch {
	case errors.Is(err, errPSWakeMoved):
		return name + " is not at " + strings.TrimSpace(c.Host) + " any more — looking for it on this network."
	case errors.Is(err, errPSWakeUnreachable):
		fix := "To wake it, turn on " + setting + " in its rest mode settings."
		if play {
			return name + " did not answer — " + hintSentence("press %s to connect anyway.", "A", "Play") + " " + fix
		}
		return name + " did not answer. " + fix
	case errors.Is(err, errPSWakeTimeout):
		return name + " did not come out of rest mode within a minute. Check " + setting + "."
	}
	return "Could not wake " + name + " — " + firstLine(err.Error())
}

// psRestModeSwitch is the console's own name for the setting a wake needs. It
// sits under Features Available in Rest Mode, next to Stay Connected to the
// Internet, which it needs as well (docs/REMOTE_PLAY.md has both menu paths).
func psRestModeSwitch(kind int) string {
	switch kind {
	case 5:
		return "Enable Turning On PS5 from Network"
	case 4:
		return "Enable Turning On PS4 from Network"
	}
	return "Enable Turning On from Network"
}

// ── Rows and hints ───────────────────────────────────────────────────────

// psPanelPairedRow is a paired console's line: what it is doing and what A
// does about it.
func psPanelPairedRow(host string, quality int) (string, string, psPanelAction) {
	if run, ok := psWakeRunning(); ok && run.host == host {
		// A here says it is already waking (psPanelPlayOrWake); B stops it.
		return psWakeRowDetail(run, time.Now()), "…", psActionPlay
	}
	state, _ := psConsoleSeenNow(host, time.Now())
	switch state {
	case psRowResting:
		return hintSentence("In rest mode · %s wakes it and starts the stream", "A", "Play"), "Wake", psActionPlay
	case psRowSilent:
		return hintSentence("Not answering on this network · %s tries to wake it first", "A", "Play"), "Play", psActionPlay
	case psRowMissed:
		return hintSentence("Did not answer · %s connects anyway", "A", "Play"), "Try anyway", psActionPlay
	}
	return "Ready to stream · " + psQualityPresets[clampQuality(quality)].Label, "Play", psActionPlay
}

// psWakeRowDetail is the line of a console being woken.
func psWakeRowDetail(run psWakeView, now time.Time) string {
	var text string
	switch run.stage {
	case psWakeWaking:
		text = "Waking it from rest mode… " + itoa(int(now.Sub(run.started)/time.Second)) + " s"
	case psWakeStarting:
		text = "It is on"
		if run.play {
			text = "It is on — starting the stream…"
		}
	default:
		text = "Looking for it on this network…"
	}
	return text + " · " + hintSentence("%s stops", "B", "Stop")
}

// psPanelConsoleHints is the console list's footer, following the focused row:
// a console offers Play, Wake and Forget, and while a wake runs B stops it.
func psPanelConsoleHints(rows []chiakiRow, cursor int) string {
	back := FooterHint{"B", "Close"}
	run, waking := psWakeRunning()
	switch {
	case psHelpersPressHeld():
		// A Play or a PIN prompt waits for the helpers' install
		// (psremote_helpers_install.go); B lets go of it first.
		back = FooterHint{"B", "Stop waiting"}
	case waking:
		back = FooterHint{"B", "Stop waking"}
	}
	if cursor < 0 || cursor >= len(rows) {
		return hintLine(FooterHint{"A", "Select"}, back)
	}
	if waking && rows[cursor].host != "" && rows[cursor].host == run.host {
		// The console being woken: B is the one thing to do.
		return hintLine(back)
	}
	// Start (O on a keyboard) opens the console's options: its internet
	// address and login passcode (psremote_console_opts.go).
	options := FooterHint{"Start / O", "Options"}
	switch rows[cursor].action {
	case psActionPlay:
		return hintLine(FooterHint{"A", "Play"}, FooterHint{"X", "Wake"}, FooterHint{"Y", "Forget"}, options, back)
	case psActionPair:
		return hintLine(FooterHint{"A", "Pair"}, options, back)
	case psActionNone:
		// A does nothing on a status line (a pairing, a scan, an install).
		return hintLine(back)
	case psActionInstallHelpers:
		// "Install" or "Retry", as the row's status says.
		return hintLine(FooterHint{"A", rows[cursor].status}, back)
	}
	return hintLine(FooterHint{"A", "Select"}, back)
}

// psControllersRows is the panel's line about controllers: which pads a stream
// gets, the way to pair one, and — while Bluetooth is on — its cost on this
// handheld, whose Bluetooth shares the radio with Wi-Fi (TrimUI's own notes
// say to turn it off for streaming). The warning leads, so a long pad name is
// what gets cut, not the advice. Only where Remote Play runs.
func psControllersRows() []chiakiRow {
	if !psRemoteSupported() {
		return nil
	}
	detail := "Built-in controls"
	if pads := strings.TrimSpace(btControllerSummary()); pads != "" {
		detail = "Built-in + " + pads
	}
	if on, known := btRadioCachedState(); known && on {
		detail = "Bluetooth on: may stutter, try 540p30 · " + detail
	} else {
		detail += " · " + hintSentence("%s pairs a Bluetooth pad", "A", "Select")
	}
	return []chiakiRow{{label: "Controllers", detail: detail, status: "Bluetooth", action: psActionControllers}}
}

// psPanelOpenControllers leaves the panel for the Bluetooth devices page.
func psPanelOpenControllers() {
	closeChiakiPanel()
	btScreenOpen(appConfig)
}
