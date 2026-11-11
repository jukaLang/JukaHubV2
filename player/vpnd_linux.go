//go:build linux

package main

// vpnd_linux.go — the VPN's tunnel process on Linux: `JukaHub --vpn-daemon`,
// started by vpn_linux.go with the profile on stdin.
//
// It is a process of its own, not a goroutine in the app, because the tunnel
// has to outlive the app's own process: JukaHub quits to hand the screen to PS
// Remote Play (launch.sh's screen handoff) and restarts after an update, and a
// stream away from home is exactly what a VPN is for. It stops when told to
// (SIGTERM), and on its own when launch.sh — the JukaHub session — is gone, so
// leaving JukaHub for the stock launcher leaves no tunnel behind.
//
// What it does, as root:
//
//   - creates the TUN interface jukavpn0 (loading the tun module or making
//     /dev/net/tun when the firmware has the driver but not the node), gives it
//     the profile's addresses and MTU, and runs WireGuard on it (wg_device.go);
//   - routes 0.0.0.0/1 and 128.0.0.0/1 (and ::/1, 8000::/1) into it: more
//     specific than the default route, so everything goes through the tunnel
//     without the default route being touched, while the Wi-Fi network's own
//     subnet route is more specific still and the LAN — Remote Play at home,
//     the phone companion, FTP — stays reachable. Routes are only ever added
//     beside what is there, never in place of it, and they belong to the
//     interface: if this process dies, they go with it and the device falls
//     back to its normal connection;
//   - routes a family the tunnel has no address for (usually IPv6) to
//     "unreachable", so it cannot go around the VPN and programs fall back to
//     the other family at once; and keeps broadcast and multicast on the
//     Wi-Fi, so LAN play and device discovery still find the neighbours;
//   - sends its own UDP packets with SO_BINDTODEVICE on the interface the
//     default route uses, so they leave by Wi-Fi instead of looping into the
//     tunnel (bound again when the network changes or the driver comes back);
//   - points resolv.conf at the profile's DNS servers, keeping the original in
//     the state folder so it comes back when the VPN stops — even after a crash
//     (vpndRecoverDNS runs at the next start and in the app);
//   - publishes its state once a second in status.json in the run folder (in
//     RAM, /tmp), which the app reads.
//
// tmp/wgref/route_check.py and route_check2.py check the route and address
// messages built here byte for byte, unreachable routes, routes added beside
// existing ones, broadcast kept on the uplink and the SO_BINDTODEVICE escape,
// in a network namespace.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	vpndTUNName      = "jukavpn0"
	vpndStatusFile   = "status.json"
	vpndLockFile     = "vpnd.lock"
	vpndRoutesFile   = "routes.json"
	vpndClockFile    = "vpn-clock"
	vpndResolvBackup = "vpn-resolv.json"
	vpndResolvMarker = "# JukaHub VPN: the previous resolv.conf comes back when the VPN stops\n"
	vpndPayloadMax   = 128 << 10
	vpndEventsKept   = 24
	// vpndClockStep is how far past the newest handshake timestamp the stored
	// floor is moved.
	vpndClockStep = 60
)

// vpndLocalCast is what stays on the Wi-Fi when all of IPv4 goes through the
// tunnel: multicast and the limited broadcast, which LAN discovery uses.
var vpndLocalCast = []netip.Prefix{netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("255.255.255.255/32")}

// vpnDaemonStatus is what the daemon publishes and the app reads.
type vpnDaemonStatus struct {
	PID   int    `json:"pid"`
	Hash  string `json:"hash"`
	State string `json:"state"` // starting, waiting, up, error, stopped
	Error string `json:"error,omitempty"`
	Fatal bool   `json:"fatal,omitempty"`
	// Warning is something the user should know while the tunnel is up.
	Warning string `json:"warning,omitempty"`
	Iface   string `json:"iface,omitempty"`
	// Uplink is the interface the tunnel's own packets leave by.
	Uplink    string   `json:"uplink,omitempty"`
	Endpoint  string   `json:"endpoint,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
	DNS       bool     `json:"dns,omitempty"`
	// HandshakeAgo is seconds since the last handshake (-1: none yet);
	// Unanswered how long the current attempt has waited for the server.
	HandshakeAgo int64            `json:"handshakeAgo"`
	Unanswered   int64            `json:"unanswered,omitempty"`
	GaveUp       bool             `json:"gaveUp,omitempty"`
	Rx           uint64           `json:"rx"`
	Tx           uint64           `json:"tx"`
	Events       []vpnDaemonEvent `json:"events,omitempty"`
	// Seq grows with every write, so the app can tell a hung daemon.
	Seq uint64 `json:"seq"`
}

// vpnDaemonEvent is one line of the daemon's story, numbered so the app logs
// each one once.
type vpnDaemonEvent struct {
	Seq  uint64 `json:"seq"`
	Text string `json:"text"`
	Warn bool   `json:"warn,omitempty"`
}

// vpndRoutesRecord is what the daemon added outside its own interface, kept
// in the run folder as it goes so vpndRecoverRoutes can take it away after a
// crash (the interface's own routes go with the interface).
type vpndRoutesRecord struct {
	Reject []netip.Prefix  `json:"reject,omitempty"`
	Cast   []vpndCastRoute `json:"cast,omitempty"`
}

// vpndCastRoute is a broadcast or multicast route kept on the uplink.
type vpndCastRoute struct {
	Prefix netip.Prefix `json:"prefix"`
	Index  int          `json:"index"`
}

type vpnDaemon struct {
	runDir, stateDir string
	anchor           int
	anchorStart      string
	hash             string

	mu     sync.Mutex // status, seq and writes; never held while calling into dev
	status vpnDaemonStatus
	seq    uint64
	writes uint64

	cfg         *wgConfig
	dev         *wgDevice
	tun         *os.File
	conn        *net.UDPConn
	dns         vpndDNS
	clock       vpndClock
	reject      []netip.Prefix  // unreachable routes this run added
	cast        []vpndCastRoute // broadcast/multicast routes this run added
	castWanted  bool
	castWarned  bool
	bindFailed  string // the uplink a bind last failed on, reported once
	uplink      string
	uplinkIndex int
	uplinkUp    bool
	endpoint6   bool
	lock        *os.File
	dnsWarned   bool
	lastLookup  string
}

// vpndMain runs the daemon and returns its exit status.
func vpndMain(args []string) int {
	vpndCloseInherited()
	// The tunnel is the last thing the out-of-memory killer should pick: all
	// traffic waits on it.
	_ = os.WriteFile("/proc/self/oom_score_adj", []byte("-800"), 0o644)
	fs := flag.NewFlagSet("vpn-daemon", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Bool("vpn-daemon", true, "")
	runDir := fs.String("vpn-run-dir", "", "")
	stateDir := fs.String("vpn-state-dir", "", "")
	anchor := fs.Int("vpn-anchor", 0, "")
	if err := fs.Parse(args); err != nil || *runDir == "" || *stateDir == "" {
		fmt.Fprintln(os.Stderr, "vpn daemon: missing --vpn-run-dir or --vpn-state-dir")
		return 2
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, vpndPayloadMax+1))
	if err != nil || len(raw) > vpndPayloadMax {
		fmt.Fprintln(os.Stderr, "vpn daemon: cannot read the profile from stdin")
		return 2
	}
	d := &vpnDaemon{runDir: *runDir, stateDir: *stateDir, anchor: *anchor, hash: vpnPayloadHash(raw)}
	return d.run(raw)
}

func (d *vpnDaemon) run(raw []byte) int {
	if err := vpndPrepareRunDir(d.runDir); err != nil {
		fmt.Fprintln(os.Stderr, "vpn daemon:", err)
		return 1
	}
	if !d.takeLock() {
		fmt.Fprintln(os.Stderr, "vpn daemon: another one is already running")
		return 3
	}
	// What a run that died left behind goes first — before anything here can
	// fail and tidy up — so the original DNS settings are what this run keeps
	// aside.
	recoveredDNS := vpndRecoverDNS(d.stateDir)
	vpndRecoverRoutes(d.runDir)
	d.status = vpnDaemonStatus{PID: os.Getpid(), Hash: d.hash, State: "starting", HandshakeAgo: -1}
	d.writeStatus()

	var payload vpnPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return d.fail("the VPN settings JukaHub sent are unreadable", true)
	}
	cfg, err := wgParseConfig(payload.Config)
	if err != nil {
		return d.fail("the WireGuard configuration: "+err.Error(), true)
	}
	d.cfg = cfg
	if os.Geteuid() != 0 {
		return d.fail("the VPN needs JukaHub to run as root, which the TrimUI firmware does", true)
	}
	if d.anchor > 1 {
		if d.anchorStart = vpndProcStart(d.anchor); d.anchorStart == "" {
			// The session that asked for the tunnel is already over.
			d.setState("stopped")
			return 0
		}
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	if recoveredDNS {
		d.event("put back the DNS settings a previous run left behind", false)
	}
	d.dns.setup(d.stateDir, cfg.DNS, cfg.DNSSearch)
	if err := vpndEnsureTUN(); err != nil {
		return d.fail(err.Error(), true)
	}
	ep, ok := d.resolveEndpoint(sigs)
	if !ok {
		d.setState("stopped")
		return 0
	}
	if code, failed := d.bringUp(payload, ep); failed {
		return code
	}

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for n := 1; ; n++ {
		select {
		case <-sigs:
			d.event("stopping", false)
			d.teardown()
			d.setState("stopped")
			return 0
		case <-tick.C:
		}
		if !d.anchorAlive() {
			d.event("JukaHub has closed; the VPN stops with it", false)
			d.teardown()
			d.setState("stopped")
			return 0
		}
		if n%3 == 0 {
			d.checkUplink()
		}
		if n%5 == 0 {
			d.checkDNS()
		}
		d.writeStatus()
	}
}

// bringUp builds the tunnel. On failure it has torn down what it built and
// reported; failed is then true and code the exit status.
func (d *vpnDaemon) bringUp(payload vpnPayload, ep netip.AddrPort) (code int, failed bool) {
	cfg := d.cfg
	tun, name, err := vpndOpenTUN(vpndTUNName)
	if err != nil {
		return d.fail(fmt.Sprintf("cannot create the tunnel interface: %v", err), true), true
	}
	d.tun = tun
	link, err := net.InterfaceByName(name)
	if err == nil {
		err = vpndLinkUp(name, cfg.mtu())
	}
	if err != nil {
		return d.fail(fmt.Sprintf("cannot bring the tunnel interface up: %v", err), true), true
	}
	// A firmware may start new interfaces with IPv6 off; the tunnel is ours to
	// switch on. A v6 address that still will not take is left out, and the
	// plan then refuses IPv6 instead of carrying it.
	if _, has6 := cfg.families(); has6 {
		_ = os.WriteFile("/proc/sys/net/ipv6/conf/"+name+"/disable_ipv6", []byte("0"), 0o644)
	}
	used := *cfg
	used.Addresses = nil
	var addrs []string
	for _, p := range cfg.Addresses {
		if err := vpndAddAddress(link.Index, p); err != nil {
			if p.Addr().Is6() {
				d.event(fmt.Sprintf("IPv6 address %s not set (%v); IPv6 is refused while the VPN is on", p, err), true)
				continue
			}
			return d.fail(fmt.Sprintf("cannot give the tunnel its address %s: %v", p, err), true), true
		}
		used.Addresses = append(used.Addresses, p)
		addrs = append(addrs, p.String())
	}
	if len(used.Addresses) == 0 {
		return d.fail("the tunnel could not take any of the configuration's addresses", true), true
	}
	plan := used.routePlan(payload.RouteAll)

	d.endpoint6 = ep.Addr().Is6()
	conn, err := vpndListenUDP(d.endpoint6, cfg.ListenPort)
	if err != nil {
		return d.fail(fmt.Sprintf("cannot open the tunnel's UDP socket: %v", err), false), true
	}
	d.conn = conn
	d.checkUplink()

	reject := plan.Reject
	for _, p := range plan.Routes {
		if _, err := vpndRoute(unix.RTM_NEWROUTE, p, link.Index, unix.RTN_UNICAST); err != nil {
			if !p.Addr().Is6() {
				return d.fail(fmt.Sprintf("cannot route %s into the tunnel: %v", p, err), true), true
			}
			// IPv6 that will not route through the tunnel is refused instead,
			// so it cannot go around it.
			reject = append(reject, p)
			continue
		}
		if p.Bits() < 4 && p.Contains(netip.MustParseAddr("255.255.255.255")) {
			d.castWanted = true
		}
	}
	d.addReject(reject)
	d.keepCastLocal()
	if d.checkDNS(); len(cfg.DNS) > 0 && !d.dns.active {
		d.event("the DNS servers stay as they were for now; the VPN's are set as soon as resolv.conf can be written", true)
	}
	id, err := newWGIdentity(cfg.PrivateKey, cfg.PeerKey, cfg.PresharedKey)
	if err != nil {
		return d.fail(err.Error(), true), true
	}
	d.clock.load(filepath.Join(d.stateDir, vpndClockFile))
	localPort := uint16(0)
	if la, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		localPort = uint16(la.Port)
	}
	d.dev = newWGDevice(wgDeviceConfig{
		Identity:  id,
		Endpoint:  ep,
		Allowed:   plan.Allowed,
		Keepalive: time.Duration(cfg.Keepalive) * time.Second,
		MTU:       cfg.mtu(),
		LocalPort: localPort,
		Timestamp: d.clock.stamp,
		Logf:      d.devLog,
	}, tun, conn, rand.Reader)

	d.mu.Lock()
	d.status.State = "up"
	d.status.Error = ""
	d.status.Iface = name
	d.status.Addresses = addrs
	d.mu.Unlock()
	how := "the networks the configuration lists"
	if payload.RouteAll {
		how = "everything"
	}
	d.event(fmt.Sprintf("%s is up; %s goes through %s", name, how, ep), false)
	d.dev.Start()
	d.writeStatus()
	return 0, false
}

// addReject routes prefixes to "unreachable", recording each one it added.
func (d *vpnDaemon) addReject(prefixes []netip.Prefix) {
	for _, p := range prefixes {
		created, err := vpndRoute(unix.RTM_NEWROUTE, p, 0, unix.RTN_UNREACHABLE)
		if err == nil {
			if created {
				d.reject = append(d.reject, p)
				d.saveRoutes()
			}
			continue
		}
		if errors.Is(err, syscall.EAFNOSUPPORT) {
			continue // no IPv6 on this kernel: nothing can leak over it
		}
		d.event(fmt.Sprintf("cannot refuse %s (%v); IPv6 may go around the VPN", p, err), true)
		d.mu.Lock()
		d.status.Warning = "IPv6 may go around the VPN on this firmware"
		d.mu.Unlock()
	}
}

// keepCastLocal routes multicast and the limited broadcast by the uplink while
// all of IPv4 goes into the tunnel: sent to the server, LAN play and device
// discovery would find nobody. It runs at every uplink check: the kernel
// drops these routes with the interface's address (a Wi-Fi reconnect), and
// they follow the uplink to another interface.
func (d *vpnDaemon) keepCastLocal() {
	if !d.castWanted || d.uplinkIndex == 0 {
		return
	}
	changed := false
	kept := d.cast[:0]
	for _, r := range d.cast {
		if r.Index != d.uplinkIndex {
			_, _ = vpndRoute(unix.RTM_DELROUTE, r.Prefix, r.Index, unix.RTN_UNICAST)
			changed = true
			continue
		}
		kept = append(kept, r)
	}
	d.cast = kept
	for _, p := range vpndLocalCast {
		created, err := vpndRoute(unix.RTM_NEWROUTE, p, d.uplinkIndex, unix.RTN_UNICAST)
		if err != nil {
			if !d.castWarned {
				d.castWarned = true
				d.event(fmt.Sprintf("cannot keep %s on the local network yet: %v", p, err), true)
			}
			continue
		}
		if !created {
			continue
		}
		known := false
		for _, r := range d.cast {
			known = known || r.Prefix == p
		}
		if !known {
			d.cast = append(d.cast, vpndCastRoute{Prefix: p, Index: d.uplinkIndex})
			changed = true
		}
	}
	if changed {
		d.saveRoutes()
	}
}

// saveRoutes writes the record of added routes (tmpfs).
func (d *vpnDaemon) saveRoutes() {
	path := filepath.Join(d.runDir, vpndRoutesFile)
	if len(d.reject) == 0 && len(d.cast) == 0 {
		_ = os.Remove(path)
		return
	}
	if data, err := json.Marshal(vpndRoutesRecord{Reject: d.reject, Cast: d.cast}); err == nil {
		_ = os.WriteFile(path, data, 0o600)
	}
}

// resolveEndpoint looks the server up, waiting for the network when it is
// not there yet (the VPN starts with JukaHub, often before Wi-Fi). It reports
// false when told to stop meanwhile. Until it answers, nothing is routed: the
// device's traffic goes out directly for that long.
func (d *vpnDaemon) resolveEndpoint(sigs <-chan os.Signal) (netip.AddrPort, bool) {
	host, port := d.cfg.EndpointHost, d.cfg.EndpointPort
	if a, err := netip.ParseAddr(host); err == nil {
		return netip.AddrPortFrom(a.Unmap(), port), true
	}
	online := func() bool { return vpndUplink(false, vpndTUNName) != "" || vpndUplink(true, vpndTUNName) != "" }
	wait := 2 * time.Second
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		cancel()
		if err == nil && len(addrs) > 0 {
			pick := addrs[0]
			for _, a := range addrs {
				// Handhelds' networks are IPv4; an IPv6 server address is a
				// fallback, not a preference.
				if a.Unmap().Is4() {
					pick = a
					break
				}
			}
			return netip.AddrPortFrom(pick.Unmap(), port), true
		}
		msg := fmt.Sprintf("waiting for the network: cannot look up %s yet", host)
		d.mu.Lock()
		d.status.State, d.status.Error = "waiting", msg
		d.mu.Unlock()
		if msg != d.lastLookup {
			d.lastLookup = msg
			d.event(msg, false)
		}
		d.writeStatus()
		// Wait in seconds: a stop, the session ending or the network
		// appearing ends the wait early.
		wasOnline := online()
		for end := time.Now().Add(wait); time.Now().Before(end); {
			select {
			case <-sigs:
				return netip.AddrPort{}, false
			case <-time.After(time.Second):
			}
			if !d.anchorAlive() {
				return netip.AddrPort{}, false
			}
			d.writeStatus() // still alive: the app replaces a silent daemon
			if !wasOnline && online() {
				break
			}
		}
		if wait *= 2; wait > 30*time.Second {
			wait = 30 * time.Second
		}
	}
}

// teardown undoes bringUp: DNS first (names resolve again the moment the
// tunnel goes), the routes that are not the interface's, then the socket and
// the interface, whose routes go with it.
func (d *vpnDaemon) teardown() {
	if d.dev != nil {
		d.dev.Close()
	}
	d.dns.restore()
	for _, p := range d.reject {
		_, _ = vpndRoute(unix.RTM_DELROUTE, p, 0, unix.RTN_UNREACHABLE)
	}
	for _, r := range d.cast {
		_, _ = vpndRoute(unix.RTM_DELROUTE, r.Prefix, r.Index, unix.RTN_UNICAST)
	}
	d.reject, d.cast = nil, nil
	d.saveRoutes()
	if d.conn != nil {
		_ = d.conn.Close()
	}
	if d.tun != nil {
		_ = d.tun.Close()
	}
	d.clock.finish()
}

// fail reports a reason the tunnel cannot run and returns the exit status.
// fatal says retrying will not help until the profile or the device changes.
func (d *vpnDaemon) fail(msg string, fatal bool) int {
	d.teardown()
	d.mu.Lock()
	d.status.State, d.status.Error, d.status.Fatal = "error", msg, fatal
	d.mu.Unlock()
	d.event(msg, true)
	d.writeStatus()
	fmt.Fprintln(os.Stderr, "vpn:", msg)
	return 1
}

func (d *vpnDaemon) setState(state string) {
	d.mu.Lock()
	d.status.State = state
	d.mu.Unlock()
	d.writeStatus()
}

func (d *vpnDaemon) event(text string, warn bool) {
	d.mu.Lock()
	d.seq++
	d.status.Events = append(d.status.Events, vpnDaemonEvent{Seq: d.seq, Text: text, Warn: warn})
	if n := len(d.status.Events); n > vpndEventsKept {
		d.status.Events = append([]vpnDaemonEvent(nil), d.status.Events[n-vpndEventsKept:]...)
	}
	d.mu.Unlock()
}

// devLog is the device's logger: its lines become events.
func (d *vpnDaemon) devLog(format string, args ...any) {
	d.event(fmt.Sprintf(format, args...), false)
}

// writeStatus publishes the state atomically (a rename in tmpfs).
func (d *vpnDaemon) writeStatus() {
	var st wgStats
	if d.dev != nil {
		st = d.dev.Stats()
	}
	now := time.Now()
	d.mu.Lock()
	d.writes++
	s := d.status
	s.Seq = d.writes
	s.Uplink = d.uplink
	s.DNS = d.dns.active
	if d.dev != nil {
		s.HandshakeAgo = -1
		if !st.LastHandshake.IsZero() {
			s.HandshakeAgo = int64(now.Sub(st.LastHandshake) / time.Second)
		}
		s.Unanswered = 0
		if !st.UnansweredSince.IsZero() {
			s.Unanswered = int64(now.Sub(st.UnansweredSince) / time.Second)
		}
		s.GaveUp = st.GaveUp
		s.Rx, s.Tx = st.Rx, st.Tx
		s.Endpoint = st.Endpoint.String()
	}
	data, err := json.Marshal(s)
	d.mu.Unlock()
	if err != nil {
		return
	}
	path := filepath.Join(d.runDir, vpndStatusFile)
	if os.WriteFile(path+".tmp", data, 0o600) == nil {
		_ = os.Rename(path+".tmp", path)
	}
}

// checkUplink follows the default route. The socket is bound again when the
// interface changes — by index: a Wi-Fi driver that comes back registers a new
// interface under the old name — and whenever the network returns, and a
// handshake then proves the path, so the tunnel recovers in seconds.
func (d *vpnDaemon) checkUplink() {
	if d.conn == nil {
		return
	}
	name := vpndUplink(d.endpoint6, vpndTUNName)
	index := 0
	if name != "" {
		if ifc, err := net.InterfaceByName(name); err == nil {
			index = ifc.Index
		}
	}
	if index == 0 {
		d.uplinkUp = false
		return
	}
	if index != d.uplinkIndex || !d.uplinkUp {
		if err := vpndBindToDevice(d.conn, name); err != nil {
			if key := fmt.Sprintf("%s/%d", name, index); key != d.bindFailed {
				d.bindFailed = key
				d.event(fmt.Sprintf("cannot use %s for the tunnel: %v", name, err), true)
			}
			return
		}
		d.bindFailed = ""
		if d.uplink != "" && d.uplink != name {
			d.event(fmt.Sprintf("the network moved from %s to %s; reconnecting", d.uplink, name), false)
		}
		d.uplink, d.uplinkIndex, d.uplinkUp = name, index, true
		if d.dev != nil {
			d.dev.Kick()
		}
	}
	d.keepCastLocal()
}

// checkDNS sets the VPN's DNS servers once resolv.conf can take them, and puts
// them back when DHCP rewrote the file.
func (d *vpnDaemon) checkDNS() {
	if len(d.cfg.DNS) == 0 {
		return
	}
	if !d.dns.active {
		if err := d.dns.apply(); err == nil {
			d.event("names are now looked up through the VPN", false)
		}
		return
	}
	if d.dns.check() && !d.dnsWarned {
		d.dnsWarned = true
		d.event("the network rewrote the DNS settings; the VPN's are back in place", false)
	}
}

func (d *vpnDaemon) anchorAlive() bool {
	return d.anchor <= 1 || vpndProcStart(d.anchor) == d.anchorStart
}

// takeLock makes sure one daemon runs at a time.
func (d *vpnDaemon) takeLock() bool {
	f, err := os.OpenFile(filepath.Join(d.runDir, vpndLockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return false
	}
	d.lock = f
	return true
}

// ── The system's side ───────────────────────────────────────────────────

// vpndCloseInherited closes every descriptor the daemon inherited from
// JukaHub. C libraries (the display, input and sound stacks) open some
// without close-on-exec, and a tunnel process that kept them would hold an
// input grab or the sound device after JukaHub is gone. Everything Go opens
// is close-on-exec, so a descriptor without the flag can only be inherited.
func vpndCloseInherited() {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return
	}
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil || fd <= 2 {
			continue
		}
		if flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil && flags&unix.FD_CLOEXEC == 0 {
			_ = unix.Close(fd)
		}
	}
}

// vpndPrepareRunDir makes the run folder ours: a real directory (not a link
// someone left in /tmp), owned by this user and closed to others.
func vpndPrepareRunDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%s belongs to another user", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// vpndProcStart is a process's start time (field 22 of /proc/PID/stat), which
// tells a live process from a later one that reused its pid; "" when gone.
func vpndProcStart(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return ""
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 || f[0] == "Z" {
		return ""
	}
	return f[19]
}

// vpndEnsureTUN makes /dev/net/tun usable: loading the module when there is
// one (the system's modprobe only, with a clean environment and a time
// limit), making the node when the driver is there without it.
func vpndEnsureTUN() error {
	if _, err := os.Stat("/dev/net/tun"); err == nil {
		return nil
	}
	for _, mp := range []string{"/sbin/modprobe", "/usr/sbin/modprobe", "/bin/modprobe", "/usr/bin/modprobe"} {
		if fi, err := os.Stat(mp); err != nil || fi.IsDir() {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, mp, "tun")
		cmd.Env = []string{"PATH=/sbin:/usr/sbin:/bin:/usr/bin"}
		_ = cmd.Run()
		cancel()
		break
	}
	if _, err := os.Stat("/dev/net/tun"); err == nil {
		return nil
	}
	misc, _ := os.ReadFile("/proc/misc")
	found := false
	for _, line := range strings.Split(string(misc), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "200" && f[1] == "tun" {
			found = true
		}
	}
	if !found {
		return errors.New("this firmware's kernel has no TUN driver, which the VPN needs")
	}
	_ = os.MkdirAll("/dev/net", 0o755)
	if err := unix.Mknod("/dev/net/tun", unix.S_IFCHR|0o666, int(unix.Mkdev(10, 200))); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("cannot create /dev/net/tun: %v", err)
	}
	return nil
}

// vpndOpenTUN creates the tunnel interface. The descriptor is non-blocking so
// Go's poller runs it and Close ends a pending Read.
func vpndOpenTUN(name string) (*os.File, string, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	ifr, err := unix.NewIfreq(name)
	if err == nil {
		ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
		err = unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr)
	}
	if err == nil {
		err = unix.SetNonblock(fd, true)
	}
	if err != nil {
		_ = unix.Close(fd)
		return nil, "", err
	}
	return os.NewFile(uintptr(fd), "/dev/net/tun"), ifr.Name(), nil
}

// vpndLinkUp sets the MTU and brings the interface up.
func vpndLinkUp(name string, mtu int) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return err
	}
	ifr.SetUint32(uint32(mtu))
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFMTU, ifr); err != nil {
		return fmt.Errorf("MTU %d: %v", mtu, err)
	}
	if ifr, err = unix.NewIfreq(name); err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return err
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
}

// vpndAttr appends one rtnetlink attribute, padded to 4 bytes.
func vpndAttr(b []byte, typ uint16, data []byte) []byte {
	var hdr [unix.SizeofRtAttr]byte
	binary.NativeEndian.PutUint16(hdr[0:2], uint16(unix.SizeofRtAttr+len(data)))
	binary.NativeEndian.PutUint16(hdr[2:4], typ)
	b = append(b, hdr[:]...)
	b = append(b, data...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// vpndNetlink sends one rtnetlink request and waits for the kernel's answer.
func vpndNetlink(msgType, flags uint16, body []byte) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 3})
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	const seq = 0x4a554b41
	msg := make([]byte, unix.NLMSG_HDRLEN, unix.NLMSG_HDRLEN+len(body))
	binary.NativeEndian.PutUint32(msg[0:4], uint32(unix.NLMSG_HDRLEN+len(body)))
	binary.NativeEndian.PutUint16(msg[4:6], msgType)
	binary.NativeEndian.PutUint16(msg[6:8], flags|unix.NLM_F_REQUEST|unix.NLM_F_ACK)
	binary.NativeEndian.PutUint32(msg[8:12], seq)
	msg = append(msg, body...)
	if err := unix.Sendto(fd, msg, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	buf := make([]byte, 8192)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			return err
		}
		msgs, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if m.Header.Seq != seq || m.Header.Type != unix.NLMSG_ERROR {
				continue
			}
			if len(m.Data) < 4 {
				return errors.New("short netlink answer")
			}
			if code := int32(binary.NativeEndian.Uint32(m.Data[:4])); code != 0 {
				return syscall.Errno(-code)
			}
			return nil
		}
	}
}

// vpndAddAddress gives the interface an address (RTM_NEWADDR). IPv6 skips
// duplicate address detection: nothing else is on a tunnel.
func vpndAddAddress(ifindex int, p netip.Prefix) error {
	family, flags := byte(unix.AF_INET), byte(0)
	if p.Addr().Is6() {
		family, flags = unix.AF_INET6, unix.IFA_F_NODAD
	}
	body := make([]byte, unix.SizeofIfAddrmsg)
	body[0], body[1], body[2], body[3] = family, byte(p.Bits()), flags, unix.RT_SCOPE_UNIVERSE
	binary.NativeEndian.PutUint32(body[4:8], uint32(ifindex))
	addr := p.Addr().AsSlice()
	body = vpndAttr(body, unix.IFA_LOCAL, addr)
	body = vpndAttr(body, unix.IFA_ADDRESS, addr)
	return vpndNetlink(unix.RTM_NEWADDR, unix.NLM_F_CREATE|unix.NLM_F_REPLACE, body)
}

// vpndRoute adds (RTM_NEWROUTE) or removes (RTM_DELROUTE) a main-table route
// for p: through interface ifindex, or — with typ RTN_UNREACHABLE and no
// interface — to nowhere. A route is only ever added beside what is there,
// never replacing it, so the LAN's own route survives and is back by itself
// when the tunnel goes. created reports whether this call made the route (an
// identical one already there is not an error, and is not ours to remove).
func vpndRoute(op uint16, p netip.Prefix, ifindex int, typ byte) (created bool, err error) {
	family, scope := byte(unix.AF_INET), byte(unix.RT_SCOPE_LINK)
	if p.Addr().Is6() {
		family = unix.AF_INET6
	}
	if p.Addr().Is6() || typ != unix.RTN_UNICAST {
		scope = unix.RT_SCOPE_UNIVERSE
	}
	body := make([]byte, unix.SizeofRtMsg)
	body[0], body[1] = family, byte(p.Bits())
	body[4], body[5], body[6], body[7] = unix.RT_TABLE_MAIN, unix.RTPROT_BOOT, scope, typ
	body = vpndAttr(body, unix.RTA_DST, p.Masked().Addr().AsSlice())
	if ifindex > 0 {
		var oif [4]byte
		binary.NativeEndian.PutUint32(oif[:], uint32(ifindex))
		body = vpndAttr(body, unix.RTA_OIF, oif[:])
	}
	if op != unix.RTM_NEWROUTE {
		return false, vpndNetlink(op, 0, body)
	}
	err = vpndNetlink(op, unix.NLM_F_CREATE, body)
	if errors.Is(err, syscall.EEXIST) {
		return false, nil
	}
	return err == nil, err
}

// vpndRecoverRoutes removes the routes a run that died added outside its
// interface (the interface's own went with it).
func vpndRecoverRoutes(runDir string) {
	path := filepath.Join(runDir, vpndRoutesFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var rec vpndRoutesRecord
	if json.Unmarshal(data, &rec) == nil {
		for _, p := range rec.Reject {
			_, _ = vpndRoute(unix.RTM_DELROUTE, p, 0, unix.RTN_UNREACHABLE)
		}
		for _, r := range rec.Cast {
			_, _ = vpndRoute(unix.RTM_DELROUTE, r.Prefix, r.Index, unix.RTN_UNICAST)
		}
	}
	_ = os.Remove(path)
}

// vpndUplink is the interface the default route leaves by, the tunnel aside;
// "" when there is none (no Wi-Fi yet).
func vpndUplink(v6 bool, tun string) string {
	if v6 {
		data, _ := os.ReadFile("/proc/net/ipv6_route")
		return vpndUplink6(string(data), tun)
	}
	data, _ := os.ReadFile("/proc/net/route")
	return vpndUplink4(string(data), tun)
}

func vpndUplink4(table, tun string) string {
	best, bestMetric := "", uint64(0)
	for _, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 || f[0] == tun || f[0] == "lo" || f[0] == "*" || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(f[3], 16, 32)
		if err != nil || flags&unix.RTF_UP == 0 || flags&unix.RTF_REJECT != 0 {
			continue
		}
		metric, _ := strconv.ParseUint(f[6], 10, 64)
		if best == "" || metric < bestMetric {
			best, bestMetric = f[0], metric
		}
	}
	return best
}

func vpndUplink6(table, tun string) string {
	best, bestMetric := "", uint64(0)
	for _, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		if len(f) < 10 || f[9] == tun || f[9] == "lo" || f[1] != "00" || strings.Trim(f[0], "0") != "" {
			continue
		}
		flags, err := strconv.ParseUint(f[8], 16, 32)
		if err != nil || flags&unix.RTF_UP == 0 || flags&unix.RTF_REJECT != 0 {
			continue
		}
		metric, _ := strconv.ParseUint(f[5], 16, 64)
		if best == "" || metric < bestMetric {
			best, bestMetric = f[9], metric
		}
	}
	return best
}

// vpndListenUDP opens the tunnel's socket; checkUplink binds it to the uplink.
func vpndListenUDP(v6 bool, port int) (*net.UDPConn, error) {
	network := "udp4"
	if v6 {
		network = "udp6"
	}
	pc, err := net.ListenPacket(network, ":"+strconv.Itoa(port))
	if err != nil {
		return nil, err
	}
	conn, ok := pc.(*net.UDPConn)
	if !ok {
		_ = pc.Close()
		return nil, errors.New("not a UDP socket")
	}
	return conn, nil
}

// vpndBindToDevice sends the socket's packets out of iface whatever the
// routes say (SO_BINDTODEVICE).
func vpndBindToDevice(conn *net.UDPConn, iface string) error {
	rc, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) {
		serr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
	}); err != nil {
		return err
	}
	return serr
}

// vpndSyncWrite writes a small file through a temporary one: written, flushed
// and renamed over the old one, so a power cut leaves the old or the new.
func vpndSyncWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// ── DNS ──────────────────────────────────────────────────────────────────

// vpndDNS points resolv.conf at the tunnel's DNS servers while the VPN runs.
// The file is written in place (DHCP rewrites it all the time; on TrimUI it
// is usually a link into /tmp), and the original waits in the state folder,
// so a crash or a power cut can still be undone at the next start.
type vpndDNS struct {
	stateDir string
	servers  []netip.Addr
	search   []string
	path     string
	original []byte
	written  []byte
	active   bool
}

type vpndResolvSaved struct {
	Path     string `json:"path"`
	Original string `json:"original"`
}

func vpndResolvPath() (string, error) { return filepath.EvalSymlinks("/etc/resolv.conf") }

// vpndReadResolv reads resolv.conf twice, a moment apart, and only trusts a
// read the two agree on: DHCP truncates the file and then appends to it.
func vpndReadResolv(path string) ([]byte, bool) {
	a, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	time.Sleep(100 * time.Millisecond)
	b, err := os.ReadFile(path)
	return a, err == nil && bytes.Equal(a, b)
}

// vpndResolvUsable is a resolv.conf worth putting back: the network's own,
// with a server in it, not one this VPN wrote.
func vpndResolvUsable(b []byte) bool {
	return !bytes.HasPrefix(b, []byte(vpndResolvMarker)) && bytes.Contains(b, []byte("nameserver"))
}

// vpndGatewayResolv is the original of last resort when the real one is lost
// (a resolv.conf still carrying the marker and no saved copy): the default
// gateway, which answers DNS on nearly every home network.
func vpndGatewayResolv() []byte {
	data, _ := os.ReadFile("/proc/net/route")
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" || f[0] == vpndTUNName {
			continue
		}
		if gw, err := strconv.ParseUint(f[2], 16, 32); err == nil && gw != 0 {
			var a [4]byte
			binary.LittleEndian.PutUint32(a[:], uint32(gw))
			return []byte("nameserver " + netip.AddrFrom4(a).String() + "\n")
		}
	}
	return nil
}

func (r *vpndDNS) setup(stateDir string, servers []netip.Addr, search []string) {
	r.stateDir, r.servers, r.search = stateDir, servers, search
}

// apply writes the VPN's servers, keeping the network's file aside first.
func (r *vpndDNS) apply() error {
	path, err := vpndResolvPath()
	if err != nil {
		return err
	}
	// One that cannot be written is not worth a copy on the card every 5 s.
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_ = f.Close()
	orig, stable := vpndReadResolv(path)
	switch {
	case !stable:
		return errors.New("resolv.conf is being written")
	case !vpndResolvUsable(orig) && bytes.HasPrefix(orig, []byte(vpndResolvMarker)):
		if orig = vpndGatewayResolv(); orig == nil {
			return errors.New("the network's DNS settings are lost")
		}
	case !vpndResolvUsable(orig):
		return errors.New("resolv.conf has no DNS server yet")
	}
	var b strings.Builder
	b.WriteString(vpndResolvMarker)
	for i, s := range r.servers {
		if i == 3 {
			break // the resolver reads three
		}
		b.WriteString("nameserver " + s.String() + "\n")
	}
	if len(r.search) > 0 {
		b.WriteString("search " + strings.Join(r.search, " ") + "\n")
	}
	r.path, r.original, r.written = path, orig, []byte(b.String())
	if err := r.save(); err != nil {
		return fmt.Errorf("cannot keep the original aside: %v", err)
	}
	if err := vpndWriteInPlace(path, r.written); err != nil {
		// The network's own goes back; the saved copy stays if it cannot.
		if vpndWriteInPlace(path, orig) == nil {
			_ = os.Remove(filepath.Join(r.stateDir, vpndResolvBackup))
		}
		return err
	}
	r.active = true
	return nil
}

func (r *vpndDNS) save() error {
	data, err := json.Marshal(vpndResolvSaved{Path: r.path, Original: string(r.original)})
	if err != nil {
		return err
	}
	return vpndSyncWrite(filepath.Join(r.stateDir, vpndResolvBackup), data)
}

// check puts the VPN's servers back when DHCP rewrote the file, keeping what
// DHCP wrote as the new original. It reports whether it had to.
func (r *vpndDNS) check() bool {
	cur, stable := vpndReadResolv(r.path)
	if !stable || bytes.Equal(cur, r.written) {
		return false
	}
	if vpndResolvUsable(cur) {
		r.original = cur
		_ = r.save()
	}
	return vpndWriteInPlace(r.path, r.written) == nil
}

func (r *vpndDNS) restore() {
	if !r.active {
		return
	}
	r.active = false
	if cur, err := os.ReadFile(r.path); err == nil && vpndResolvOurs(cur) {
		if vpndWriteInPlace(r.path, r.original) != nil {
			return // the saved copy stays for vpndRecoverDNS
		}
	}
	_ = os.Remove(filepath.Join(r.stateDir, vpndResolvBackup))
}

// vpndResolvOurs is a resolv.conf the VPN wrote — or emptied, when a write
// of ours failed half way.
func vpndResolvOurs(b []byte) bool {
	return bytes.HasPrefix(b, []byte(vpndResolvMarker)) || len(bytes.TrimSpace(b)) == 0
}

// vpndRecoverDNS puts back the resolv.conf a run that died left behind. Only
// the real resolv.conf, and only while it still carries the VPN's marker.
func vpndRecoverDNS(stateDir string) bool {
	backup := filepath.Join(stateDir, vpndResolvBackup)
	data, err := os.ReadFile(backup)
	if err != nil {
		return false
	}
	var saved vpndResolvSaved
	path, perr := vpndResolvPath()
	if json.Unmarshal(data, &saved) != nil || perr != nil || saved.Path != path {
		_ = os.Remove(backup)
		return false
	}
	cur, err := os.ReadFile(path)
	if err == nil && vpndResolvOurs(cur) {
		if vpndWriteInPlace(path, []byte(saved.Original)) != nil {
			return false
		}
		_ = os.Remove(backup)
		return true
	}
	_ = os.Remove(backup)
	return false
}

// vpndWriteInPlace rewrites a file keeping its inode and mode.
func vpndWriteInPlace(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// ── Handshake clock ──────────────────────────────────────────────────────

// vpndClock makes handshake timestamps that only ever grow. A server drops an
// initiation whose timestamp is not newer than the last one it took from this
// key, and a handheld without a clock battery starts in the past until it
// reaches a time server: with the wall clock alone, a VPN that worked
// yesterday would be refused this morning. So every timestamp also stays
// above a floor kept on the card: moved vpndClockStep seconds ahead whenever
// a timestamp reaches it (written in the background, never while the tunnel
// waits), and set just past the last timestamp when the tunnel stops.
type vpndClock struct {
	mu      sync.Mutex
	path    string
	floor   uint64    // TAI64 seconds every timestamp stays above
	base    uint64    // the floor when this run started
	start   time.Time // when this run started (monotonic)
	last    [12]byte
	pending uint64 // a floor waiting to be written; 0: none
	writing bool
}

func (c *vpndClock) load(path string) {
	c.path = path
	if data, err := os.ReadFile(path); err == nil {
		if n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
			c.floor = n
		}
	}
	c.base = c.floor
	c.start = time.Now()
}

func (c *vpndClock) stamp() [12]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	ts := wgTAI64N(now)
	if mono := c.base + uint64(now.Sub(c.start)/time.Second) + 1; binary.BigEndian.Uint64(ts[:8]) < mono {
		binary.BigEndian.PutUint64(ts[:8], mono)
	}
	if bytes.Compare(ts[:], c.last[:]) <= 0 {
		ts = c.last
		nanos := binary.BigEndian.Uint32(ts[8:]) + 1<<24
		if nanos >= 1e9 {
			binary.BigEndian.PutUint64(ts[:8], binary.BigEndian.Uint64(ts[:8])+1)
			nanos = 0
		}
		binary.BigEndian.PutUint32(ts[8:], nanos)
	}
	c.last = ts
	if secs := binary.BigEndian.Uint64(ts[:8]); secs >= c.floor && c.path != "" {
		c.floor = secs + vpndClockStep
		c.pending = c.floor
		if !c.writing {
			c.writing = true
			go c.persist()
		}
	}
	return ts
}

// persist writes pending floors until there are none.
func (c *vpndClock) persist() {
	for {
		c.mu.Lock()
		v := c.pending
		c.pending = 0
		if v == 0 {
			c.writing = false
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		_ = vpndSyncWrite(c.path, []byte(strconv.FormatUint(v, 10)+"\n"))
	}
}

// finish stores a floor just past the last timestamp sent, so a clean restart
// does not run ahead of the clock. A write already under way takes it along.
func (c *vpndClock) finish() {
	c.mu.Lock()
	secs := binary.BigEndian.Uint64(c.last[:8])
	if secs == 0 || c.path == "" {
		c.mu.Unlock()
		return
	}
	c.pending = secs + 1
	busy := c.writing
	c.writing = true
	c.mu.Unlock()
	if !busy {
		c.persist()
		return
	}
	for i := 0; i < 100; i++ {
		c.mu.Lock()
		done := !c.writing
		c.mu.Unlock()
		if done {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
