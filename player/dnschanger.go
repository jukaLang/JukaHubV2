package main

// dnschanger.go — Settings → Network → DNS: which DNS servers turn names into
// addresses.
//
// Automatic, the default, leaves the network's own servers alone (the
// router's, handed out by DHCP). A public service (Cloudflare, Google, Quad9,
// AdGuard, Cloudflare for Families, OpenDNS) or up to three servers typed in
// (a Pi-hole at home) replaces them:
//
//   - on the handheld (Linux, JukaHub running as root) for the whole system:
//     resolv.conf names the chosen servers under a marker line, the network's
//     own file waits in data/dns-resolv.json, and the worker writes the choice
//     back each time DHCP rewrites the file (dnschanger_linux.go). Automatic,
//     safe mode and a quit put the network's own back; a crash is undone at
//     the next start. Games, ports, Android apps and JukaHub all follow it;
//   - everywhere, for JukaHub's own lookups: the net package's resolver dials
//     the chosen servers (dnsResolverDial), falling over to the next one when
//     one stops answering. That is all a PC gets — Windows' DNS is the PC's
//     own setting — and all a Linux without root gets;
//   - a VPN whose configuration names DNS servers wins while it runs (inside
//     the tunnel those are the ones that work, and they keep names private to
//     the VPN); the choice comes back when it stops.
//
// After a change, and when the network changes, the chosen servers are asked
// for a name once; when none answers here the row says so (some networks
// block outside DNS). "Find the fastest DNS" times every choice from here and
// offers them fastest first.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	dnsModeKey    = "dnsMode"
	dnsServersKey = "dnsServers"
	dnsModeCustom = "custom"
	// dnsMaxServers is how many resolv.conf's readers take.
	dnsMaxServers = 3
	// dnsBackupFile is the network's own resolv.conf, kept aside in the data
	// folder while the choice is in place (dnschanger_linux.go).
	dnsBackupFile = "dns-resolv.json"
	// dnsKeepEvery is the worker's pace while a choice is set or a file of
	// ours may still be in place: DHCP rewrites resolv.conf on every lease.
	dnsKeepEvery = 3 * time.Second
	// dnsSlowEvery is its pace while the choice covers JukaHub's own lookups
	// only: nothing to keep, just the VPN and the network to notice.
	dnsSlowEvery = 10 * time.Second
	// dnsIdleEvery is its pace on Automatic with nothing to put back.
	dnsIdleEvery = time.Minute
	// dnsRecheckEvery is how soon servers that did not answer are asked again.
	dnsRecheckEvery = 30 * time.Second
	// dnsRouteBackAfter is how long JukaHub's own lookups stay on a later
	// server after the first one stopped answering, before trying it again.
	dnsRouteBackAfter = 2 * time.Minute
	// dnsCheckName is the name the answer check asks for: reserved for
	// examples, so no filtering service blocks it.
	dnsCheckName = "example.com."
)

// dnsPreset is one of the public services the row offers.
type dnsPreset struct {
	ID      string
	Label   string
	Note    string
	Servers []netip.Addr
}

var dnsPresets = []dnsPreset{
	{ID: "cloudflare", Label: "Cloudflare", Note: "1.1.1.1 · fast and private", Servers: dnsAddrs("1.1.1.1", "1.0.0.1")},
	{ID: "google", Label: "Google", Note: "8.8.8.8 · Google Public DNS", Servers: dnsAddrs("8.8.8.8", "8.8.4.4")},
	{ID: "quad9", Label: "Quad9", Note: "9.9.9.9 · blocks known malware sites", Servers: dnsAddrs("9.9.9.9", "149.112.112.112")},
	{ID: "adguard", Label: "AdGuard", Note: "94.140.14.14 · blocks ads and trackers", Servers: dnsAddrs("94.140.14.14", "94.140.15.15")},
	{ID: "family", Label: "Cloudflare for Families", Note: "1.1.1.3 · blocks malware and adult sites", Servers: dnsAddrs("1.1.1.3", "1.0.0.3")},
	{ID: "opendns", Label: "OpenDNS", Note: "208.67.222.222 · Cisco OpenDNS", Servers: dnsAddrs("208.67.222.222", "208.67.220.220")},
}

func dnsAddrs(list ...string) []netip.Addr {
	out := make([]netip.Addr, len(list))
	for i, s := range list {
		out[i] = netip.MustParseAddr(s)
	}
	return out
}

func dnsPresetByID(id string) *dnsPreset {
	for i := range dnsPresets {
		if dnsPresets[i].ID == id {
			return &dnsPresets[i]
		}
	}
	return nil
}

// dnsSysResult is what the platform found when it made the system match.
type dnsSysResult struct {
	// Applied: the chosen servers are the system's.
	Applied bool
	// ByVPN: the VPN's DNS servers are in place; the choice waits.
	ByVPN bool
	// Pending: the file was being written; looked at again next time.
	Pending bool
	// Err says why the system's servers could not be changed.
	Err string
	// Network is the network's own servers, as last seen.
	Network []netip.Addr
}

// dnsHealth is the last answer check of the chosen servers.
type dnsHealth struct {
	key     string // the servers and network it was for
	at      time.Time
	checked bool
	ok      bool
	rtt     time.Duration
	fails   int  // checks in a row without an answer, for this key
	noNet   bool // there was no network to ask over
}

// dnsMgr is the DNS changer's state. The main thread publishes the setting;
// the worker writes the rest; both under mu.
var dnsMgr struct {
	mu      sync.Mutex
	ready   bool // the data folder is known (dnsStartup)
	started bool
	wake    chan struct{}
	dataDir string

	mode      string
	want      []netip.Addr // nil: the network's own
	customErr string       // why the typed servers are not used
	safe      bool

	sys    dnsSysResult
	vpnDNS bool
	looked bool // the worker has looked since the last change
	health dnsHealth
	warned string // the health key a toast was shown for
}

// dnsQuitting stops the worker touching the system once the quit step has
// put the network's own back.
var dnsQuitting atomic.Bool

// ── Settings values ──────────────────────────────────────────────────────

// dnsModeIn is the stored choice: "" (Automatic), a preset id or "custom".
// Anything else reads as Automatic.
func dnsModeIn(c *Config) string {
	if c == nil {
		return ""
	}
	mode := strings.ToLower(strings.TrimSpace(customStringValue(c.Variables.Custom, dnsModeKey)))
	if mode == dnsModeCustom || dnsPresetByID(mode) != nil {
		return mode
	}
	return ""
}

func dnsCustomText(c *Config) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(customStringValue(c.Variables.Custom, dnsServersKey))
}

// dnsWantedIn is the servers the settings ask for, nil for the network's
// own, and why typed servers are not used.
func dnsWantedIn(c *Config) ([]netip.Addr, string) {
	switch mode := dnsModeIn(c); mode {
	case "":
		return nil, ""
	case dnsModeCustom:
		return dnsWantedInCustom(c)
	default:
		return append([]netip.Addr(nil), dnsPresetByID(mode).Servers...), ""
	}
}

// dnsWantedInCustom is dnsWantedIn as if Custom were chosen.
func dnsWantedInCustom(c *Config) ([]netip.Addr, string) {
	addrs, err := dnsParseServers(dnsCustomText(c))
	switch {
	case err != nil:
		return nil, err.Error()
	case len(addrs) == 0:
		return nil, "none typed yet"
	}
	return addrs, ""
}

// dnsParseServers reads typed servers: IPv4 or IPv6 addresses separated by
// spaces, commas or semicolons, at most three. ":53" is accepted and dropped
// (resolv.conf has no ports); any other port, a link-local address (it needs
// a zone resolv.conf readers do not all take), an unspecified, multicast or
// broadcast address is refused. The messages are i18n patterns.
func dnsParseServers(text string) ([]netip.Addr, error) {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	var out []netip.Addr
	for _, f := range fields {
		a, err := netip.ParseAddr(f)
		if err != nil {
			ap, perr := netip.ParseAddrPort(f)
			switch {
			case perr != nil:
				return nil, fmt.Errorf("%s is not an IP address", f)
			case ap.Port() != 53:
				return nil, fmt.Errorf("%s: DNS uses port 53; type just the address", f)
			}
			a = ap.Addr()
		}
		a = a.Unmap()
		switch {
		case a.Zone() != "", a.IsLinkLocalUnicast():
			return nil, fmt.Errorf("%s: a link-local address cannot be used", f)
		case a.IsUnspecified(), a.IsMulticast(), a == netip.AddrFrom4([4]byte{255, 255, 255, 255}):
			return nil, fmt.Errorf("%s cannot be a DNS server", f)
		}
		dup := false
		for _, have := range out {
			dup = dup || have == a
		}
		if dup {
			continue
		}
		if len(out) == dnsMaxServers {
			return nil, fmt.Errorf("type up to %d servers", dnsMaxServers)
		}
		out = append(out, a)
	}
	return out, nil
}

// dnsJoin writes servers the way the setting stores them.
func dnsJoin(addrs []netip.Addr) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = a.String()
	}
	return strings.Join(parts, " ")
}

// dnsJoinComma writes servers for a sentence.
func dnsJoinComma(addrs []netip.Addr) string {
	return strings.ReplaceAll(dnsJoin(addrs), " ", ", ")
}

func dnsSameAddrs(a, b []netip.Addr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ── Publishing and the worker ────────────────────────────────────────────

func init() {
	// Every lookup through the net package's own resolver dials through
	// dnsResolverDial, which only redirects while a choice is in force. Set
	// here, before any goroutine can resolve a name.
	net.DefaultResolver.Dial = dnsResolverDial
	deferStartupTask("dns", func() { dnsStartup(appConfig) })
}

// dnsStartup learns the data folder, publishes the saved choice and starts
// the worker, which also undoes what a crash left behind. A deferred startup
// task.
func dnsStartup(c *Config) {
	dir, err := P().DataDir()
	if err != nil {
		logWarn("dns", "no data folder for the network's DNS settings: %v", err)
	}
	dnsMgr.mu.Lock()
	dnsMgr.dataDir = dir
	dnsMgr.ready = true
	dnsMgr.mu.Unlock()
	if c != nil {
		dnsPublish(c, false)
		return
	}
	// No settings to read: the worker still puts back what a crash left.
	dnsEnsureWorker()
	dnsWake()
}

// dnsPublish hands the setting to the worker (main thread: Settings, a
// config reload, an import). explicit marks a user's change.
func dnsPublish(c *Config, explicit bool) {
	if c == nil {
		return
	}
	mode := dnsModeIn(c)
	want, why := dnsWantedIn(c)
	safe := !safeModeAllows(SafeFeatureNetwork)
	dnsMgr.mu.Lock()
	changed := !dnsSameAddrs(dnsMgr.want, want) || dnsMgr.safe != safe
	dnsMgr.mode, dnsMgr.want, dnsMgr.customErr, dnsMgr.safe = mode, want, why, safe
	if changed {
		dnsMgr.looked = false
		dnsMgr.health = dnsHealth{}
		dnsMgr.warned = ""
	}
	ready := dnsMgr.ready
	dnsMgr.mu.Unlock()
	if changed && explicit {
		logInfo("dns", "DNS set to %s", setOr(dnsJoin(want), "the network's own"))
	}
	if ready {
		dnsEnsureWorker()
		dnsWake()
	}
}

func dnsEnsureWorker() {
	dnsMgr.mu.Lock()
	defer dnsMgr.mu.Unlock()
	if dnsMgr.started {
		return
	}
	dnsMgr.started = true
	dnsMgr.wake = make(chan struct{}, 1)
	go dnsWorker(dnsMgr.wake)
}

func dnsWake() {
	dnsMgr.mu.Lock()
	ch := dnsMgr.wake
	dnsMgr.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func dnsWorker(wake chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("dns", "worker stopped: %v", r)
			// The next change starts a new one.
			dnsMgr.mu.Lock()
			dnsMgr.started, dnsMgr.wake = false, nil
			dnsMgr.mu.Unlock()
		}
	}()
	for !dnsQuitting.Load() {
		pace := dnsTick()
		timer := time.NewTimer(pace)
		select {
		case <-wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// dnsTick makes the system and JukaHub's own lookups match the setting once,
// checks that the chosen servers answer, and says when to look again.
func dnsTick() time.Duration {
	dnsMgr.mu.Lock()
	want, safe, dir := dnsMgr.want, dnsMgr.safe, dnsMgr.dataDir
	dnsMgr.mu.Unlock()
	if safe {
		want = nil
	}
	vpnDNS := dnsVPNInUse()
	sys := dnsSystemReconcile(dir, want)
	if dnsQuitting.Load() {
		return time.Hour
	}
	redirect := want
	if vpnDNS || sys.ByVPN {
		redirect = nil
	}
	if dnsSetRoute(redirect) {
		// The connectivity probe asks by name: let it look again now.
		netNudge()
	}

	dnsMgr.mu.Lock()
	moved := dnsMgr.sys.Applied != sys.Applied || dnsMgr.sys.ByVPN != sys.ByVPN || dnsMgr.sys.Err != sys.Err
	dnsMgr.sys, dnsMgr.vpnDNS, dnsMgr.looked = sys, vpnDNS, true
	health := dnsMgr.health
	dnsMgr.mu.Unlock()
	if moved && sys.Err != "" && want != nil {
		logWarn("dns", "the system keeps its own DNS servers: %s", sys.Err)
	}

	if redirect == nil {
		if health.checked || health.noNet {
			dnsMgr.mu.Lock()
			dnsMgr.health = dnsHealth{}
			dnsMgr.mu.Unlock()
		}
	} else {
		dnsCheckIfDue(redirect, health)
	}

	switch {
	case dnsSystemHolds(), want != nil && sys.Err == "" && dnsSystemSupported():
		// resolv.conf to keep: DHCP rewrites it on every lease.
		return dnsKeepEvery
	case want != nil:
		// Only JukaHub's own lookups follow the choice (Windows, no root):
		// the VPN and the network are looked at less often.
		return dnsSlowEvery
	}
	return dnsIdleEvery
}

// dnsCheckIfDue asks the chosen servers for a name after a change, when the
// network changed, and again a while after they did not answer.
func dnsCheckIfDue(servers []netip.Addr, last dnsHealth) {
	uplink := dnsUplinkSignature()
	if hotspotHoldsRadio() {
		// The radio is this device's own network: nothing outside to ask.
		uplink = ""
	}
	key := dnsJoin(servers) + "|" + uplink
	switch {
	case uplink == "":
		if !last.noNet || last.key != key {
			dnsMgr.mu.Lock()
			dnsMgr.health = dnsHealth{key: key, at: time.Now(), noNet: true}
			dnsMgr.mu.Unlock()
		}
		return
	case last.key == key && last.checked && (last.ok || time.Since(last.at) < dnsRecheckEvery):
		return
	}
	ok, rtt := dnsCheckServers(servers)
	h := dnsHealth{key: key, at: time.Now(), checked: true, ok: ok, rtt: rtt}
	if !ok {
		h.fails = 1
		if last.key == key {
			h.fails = last.fails + 1
		}
	}
	dnsMgr.mu.Lock()
	if !dnsSameAddrs(dnsMgr.want, servers) {
		// Changed while the check ran: the next tick checks the new ones.
		dnsMgr.mu.Unlock()
		return
	}
	dnsMgr.health = h
	// The toast waits for a second miss: just after a join the network may
	// not pass anything through yet. The row says so at once.
	toast := h.fails >= 2 && dnsMgr.warned != key
	if toast {
		dnsMgr.warned = key
	}
	dnsMgr.mu.Unlock()
	if toast {
		logWarn("dns", "no answer from %s on this network", dnsJoin(servers))
		msg := "DNS: no answer from " + servers[0].String() + " on this network"
		tbxPost(func() { ShowToast(msg, ToastKindWarning) })
	}
}

// dnsUplinkSignature names the device's routable addresses, so a move to
// another network (or onto one) is noticed; "" when there is none. IPv6
// counts by its /64: temporary addresses rotate within it every day.
func dnsUplinkSignature() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	var parts []string
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || !ipn.IP.IsGlobalUnicast() {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipn.IP)
		if !ok {
			continue
		}
		if ip = ip.Unmap(); ip.Is6() {
			parts = append(parts, netip.PrefixFrom(ip, 64).Masked().String())
			continue
		}
		parts = append(parts, ip.String())
	}
	sort.Strings(parts)
	parts = slices.Compact(parts)
	return strings.Join(parts, ",")
}

// dnsVPNInUse reports whether a running VPN has its own DNS servers in
// place: the daemon says so on the handheld; on Windows WireGuard sets the
// configuration's servers whenever the tunnel runs.
func dnsVPNInUse() bool {
	v := vpnSnapshot()
	p := v.Probe
	if !p.Running {
		return false
	}
	return p.DNS || (!vpnPlatformAuto() && v.Config != nil && len(v.Config.DNS) > 0)
}

// ── JukaHub's own lookups ────────────────────────────────────────────────

// dnsRoute is where JukaHub's own lookups go while a choice is in force.
type dnsRoute struct {
	servers []string // "address:53", in the order chosen
	addrs   []netip.Addr
	pref    atomic.Uint32 // the server in use
	movedAt atomic.Int64  // when pref last moved on (Unix ns)
}

var dnsRouteNow atomic.Pointer[dnsRoute]

// dnsSetRoute points JukaHub's own lookups at servers (nil: the system's)
// and reports whether that changed anything.
//
// Windows' system resolver never dials, so a redirect needs the net
// package's own one, and PreferGo goes with the route. That write races with
// lookups reading the flag — a data race the race detector would report if a
// test ever ran the worker beside lookups, though harmless: it is one bool,
// read once per lookup, and a lookup keeps the resolver it started with. The
// net package offers no other switch (GODEBUG=netdns is read once).
func dnsSetRoute(servers []netip.Addr) bool {
	cur := dnsRouteNow.Load()
	if cur == nil && len(servers) == 0 {
		return false
	}
	if cur != nil && dnsSameAddrs(cur.addrs, servers) {
		return false
	}
	var next *dnsRoute
	if len(servers) > 0 {
		next = &dnsRoute{addrs: append([]netip.Addr(nil), servers...)}
		for _, a := range servers {
			next.servers = append(next.servers, netip.AddrPortFrom(a, 53).String())
		}
	}
	dnsRouteNow.Store(next)
	net.DefaultResolver.PreferGo = next != nil
	return true
}

// pick is the preferred server: back to the first a while after it stopped
// answering.
func (r *dnsRoute) pick() uint32 {
	i := r.pref.Load()
	if i != 0 && time.Since(time.Unix(0, r.movedAt.Load())) > dnsRouteBackAfter {
		r.pref.CompareAndSwap(i, 0)
		i = r.pref.Load()
	}
	return i % uint32(len(r.servers))
}

// failed moves on from server i, once, however many lookups saw it fail. The
// time goes first, so a pick between the two never sees the new server with
// the old time (and moves straight back).
func (r *dnsRoute) failed(i uint32) {
	if len(r.servers) < 2 {
		return
	}
	r.movedAt.Store(time.Now().UnixNano())
	r.pref.CompareAndSwap(i, (i+1)%uint32(len(r.servers)))
}

// dnsRouteSlowRead is how long a read must have waited before its timeout
// counts against the server: a caller's own short deadline is not the
// server's fault.
const dnsRouteSlowRead = 2 * time.Second

// dnsResolverDial is net.DefaultResolver's Dial. Without a route it dials
// what the resolver asked for, exactly as the net package does by itself, and
// so it does when the resolver asks for one of ours (resolv.conf names them
// on the handheld): its own turn through its servers does the failing over.
// Anything else (a PC's own servers) goes to the preferred chosen server; one
// that does not answer in time or refuses — or, over UDP, answers SERVFAIL or
// REFUSED — moves the next try on to the next server.
func dnsResolverDial(ctx context.Context, network, address string) (net.Conn, error) {
	var d net.Dialer
	r := dnsRouteNow.Load()
	if r == nil || len(r.servers) == 0 || slices.Contains(r.servers, address) {
		return d.DialContext(ctx, network, address)
	}
	i := r.pick()
	conn, err := d.DialContext(ctx, network, r.servers[i])
	if err != nil {
		if ctx.Err() == nil {
			r.failed(i)
		}
		return nil, err
	}
	// The resolver frames a PacketConn as UDP and anything else as a
	// stream, so a UDP socket must stay a PacketConn.
	if u, ok := conn.(*net.UDPConn); ok {
		return &dnsUDPConn{UDPConn: u, route: r, index: i, at: time.Now()}, nil
	}
	return &dnsStreamConn{Conn: conn, route: r, index: i, at: time.Now()}, nil
}

// dnsReadFailed reports whether a failed read counts against the server: a
// refusal always, a timeout only after a real wait.
func dnsReadFailed(err error, since time.Time) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return time.Since(since) >= dnsRouteSlowRead
	}
	return true
}

type dnsUDPConn struct {
	*net.UDPConn
	route *dnsRoute
	index uint32
	at    time.Time
}

func (c *dnsUDPConn) Read(b []byte) (int, error) {
	n, err := c.UDPConn.Read(b)
	switch {
	case err != nil:
		if dnsReadFailed(err, c.at) {
			c.route.failed(c.index)
		}
	case n >= 4 && b[2]&0x80 != 0 && (b[3]&0x0f == 2 || b[3]&0x0f == 5):
		// A response (QR set) saying SERVFAIL or REFUSED: the resolver
		// tries its next server, and so does the next lookup here.
		c.route.failed(c.index)
	}
	return n, err
}

type dnsStreamConn struct {
	net.Conn
	route *dnsRoute
	index uint32
	at    time.Time
}

func (c *dnsStreamConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err != nil && dnsReadFailed(err, c.at) {
		c.route.failed(c.index)
	}
	return n, err
}

// ── Asking a server ──────────────────────────────────────────────────────

// dnsAsk times one IPv4 lookup of name (fully qualified) through one server.
// A server that says the name does not exist has answered.
func dnsAsk(ctx context.Context, server netip.Addr, name string) (time.Duration, error) {
	addr := netip.AddrPortFrom(server, 53).String()
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}}
	start := time.Now()
	_, err := r.LookupNetIP(ctx, "ip4", name)
	took := time.Since(start)
	// "No such host" from a server is an answer; one from the hosts file
	// alone (no server named) is not.
	var dnsErr *net.DNSError
	if err != nil && errors.As(err, &dnsErr) && dnsErr.IsNotFound && dnsErr.Server != "" {
		err = nil
	}
	return took, err
}

// dnsCheckServers asks every server at once and reports whether any
// answered, and the quickest answer.
func dnsCheckServers(servers []netip.Addr) (bool, time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type answer struct {
		took time.Duration
		err  error
	}
	answers := make(chan answer, len(servers))
	for _, s := range servers {
		go func(s netip.Addr) {
			took, err := dnsAsk(ctx, s, dnsCheckName)
			answers <- answer{took, err}
		}(s)
	}
	ok, best := false, time.Duration(0)
	for range servers {
		a := <-answers
		if a.err == nil && (!ok || a.took < best) {
			ok, best = true, a.took
		}
	}
	return ok, best
}

// ── What the rows show ───────────────────────────────────────────────────

type dnsView struct {
	mode      string
	want      []netip.Addr
	customErr string
	safe      bool
	sys       dnsSysResult
	vpnDNS    bool
	looked    bool
	health    dnsHealth
}

func dnsSnapshot() dnsView {
	dnsMgr.mu.Lock()
	defer dnsMgr.mu.Unlock()
	return dnsView{
		mode: dnsMgr.mode, want: dnsMgr.want, customErr: dnsMgr.customErr, safe: dnsMgr.safe,
		sys: dnsMgr.sys, vpnDNS: dnsMgr.vpnDNS, looked: dnsMgr.looked, health: dnsMgr.health,
	}
}

// dnsChoiceName is the choice in a word, for toasts.
func dnsChoiceName(c *Config) string {
	switch mode := dnsModeIn(c); mode {
	case "":
		return "Automatic"
	case dnsModeCustom:
		if addrs, why := dnsWantedInCustom(c); why == "" {
			return dnsJoinComma(addrs)
		}
		return "Custom"
	default:
		return dnsPresetByID(mode).Label
	}
}

func dnsOptions(c *Config) []setOption {
	opts := []setOption{{Value: "", Label: "Automatic", Note: "Your network's own servers, from the router"}}
	for _, p := range dnsPresets {
		opts = append(opts, setOption{Value: p.ID, Label: p.Label, Note: p.Note})
	}
	note := "Up to three servers of your own, like a Pi-hole"
	if text := dnsCustomText(c); text != "" {
		note = text
	}
	return append(opts, setOption{Value: dnsModeCustom, Label: "Custom", Note: note})
}

// dnsStepOptions are the stops Left/Right walk through: Custom only while
// it has usable servers or is the choice, so stepping never stops at a
// keyboard. A opens the full list (dnsOptions).
func dnsStepOptions(c *Config) []setOption {
	opts := dnsOptions(c)
	if _, why := dnsWantedInCustom(c); why != "" && dnsModeIn(c) != dnsModeCustom {
		opts = opts[:len(opts)-1]
	}
	return opts
}

func dnsCurrent(c *Config) int {
	mode := dnsModeIn(c)
	for i, o := range dnsStepOptions(c) {
		if o.Value == mode {
			return i
		}
	}
	return 0
}

// dnsChoose applies a stop. Custom opens the keyboard instead while nothing
// usable is typed, and when it is chosen again (to change the servers).
func dnsChoose(c *Config, value string) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case value == dnsModeCustom:
		if _, why := dnsWantedInCustom(c); why != "" || dnsModeIn(c) == dnsModeCustom {
			dnsOpenCustomPrompt(c)
			return
		}
	case value != "" && dnsPresetByID(value) == nil:
		return
	}
	setStoreString(c, dnsModeKey, value)
	dnsPublish(c, true)
	setToast("DNS: " + dnsChoiceName(c))
}

// dnsOpenCustomPrompt asks for servers of one's own. Submitting them
// switches to Custom; an emptied field goes back to Automatic.
func dnsOpenCustomPrompt(c *Config) { dnsOpenCustomPromptWith(c, dnsCustomText(c)) }

// dnsOpenCustomPromptWith opens the prompt seeded with text; a submit that
// does not parse opens it again with what was typed, after the toast.
func dnsOpenCustomPromptWith(c *Config, seed string) {
	tbxOpenPrompt("DNS servers", "Up to three, like 192.168.1.2 1.1.1.1", seed, func(text string) {
		addrs, err := dnsParseServers(text)
		if err != nil {
			ShowToast("DNS: "+err.Error(), ToastKindError)
			// Next frame: the keyboard finishes closing first.
			tbxPost(func() { dnsOpenCustomPromptWith(c, text) })
			return
		}
		if len(addrs) == 0 {
			setStoreString(c, dnsServersKey, "")
			if dnsModeIn(c) == dnsModeCustom {
				setStoreString(c, dnsModeKey, "")
			}
			dnsPublish(c, true)
			setToast("DNS: " + dnsChoiceName(c))
			return
		}
		setStoreString(c, dnsServersKey, dnsJoin(addrs))
		setStoreString(c, dnsModeKey, dnsModeCustom)
		dnsPublish(c, true)
		setToast("DNS: " + dnsJoinComma(addrs))
	})
}

// dnsRowDetail is the DNS row's second line: what is in force, and where.
func dnsRowDetail(*Config) string {
	v := dnsSnapshot()
	list := dnsJoinComma(v.want)
	switch {
	case v.mode == dnsModeCustom && v.customErr != "":
		return "The network's own servers are used; the typed ones are not usable: " + v.customErr
	case v.want == nil:
		if len(v.sys.Network) > 0 {
			return "Your network's own: " + dnsJoinComma(v.sys.Network)
		}
		return "Your network's own servers, from the router"
	case v.safe:
		return "Safe mode: the network's own servers for this start"
	case v.vpnDNS || v.sys.ByVPN:
		return "The VPN's DNS while the VPN is on; " + list + " after it"
	case !v.looked:
		return "Changing to " + list + "…"
	case v.health.checked && !v.health.ok:
		return "No answer from " + list + " here: this network may block outside DNS, or it is offline"
	}
	// The answer time joins only lines that end in a fixed phrase, so the
	// translation patterns still see each part whole (i18n.go).
	where, timed := "", false
	switch {
	case v.sys.Applied:
		where, timed = " for the whole device: games, apps and JukaHub", true
	case !dnsSystemSupported() && IsWindows():
		where, timed = " for JukaHub; Windows keeps the PC's own DNS", true
	case !dnsSystemSupported():
		where, timed = " for JukaHub; the system keeps its own DNS", true
	case v.sys.Err != "":
		where = " for JukaHub only: " + v.sys.Err
	case v.sys.Pending:
		where = " for JukaHub; the system's follows in a moment"
	}
	if timed && v.health.checked && v.health.ok {
		where += fmt.Sprintf(" · answers in %d ms", v.health.rtt.Milliseconds())
	}
	return list + where
}

func dnsCustomVisible(c *Config) bool { return dnsModeIn(c) == dnsModeCustom }

func dnsCustomValue(c *Config) string { return setOr(dnsCustomText(c), "Not set") }

// The Settings rows (setmodel.go, Network).

func dnsChoiceRow() setRow {
	return setRow{
		ID: "dns", Label: "DNS", Kind: setKindChoice,
		Detail:     dnsRowDetail,
		Options:    dnsStepOptions,
		Current:    dnsCurrent,
		Choose:     dnsChoose,
		Sheet:      dnsOptions,
		SheetValue: dnsModeIn,
		SheetTitle: "DNS servers",
	}
}

func dnsCustomRow() setRow {
	return setRow{
		ID: "dnsCustom", Label: "Custom DNS servers", Kind: setKindAction,
		Detail:  setStatic("Type up to three addresses: a Pi-hole, your router, any DNS service"),
		Visible: dnsCustomVisible,
		Value:   dnsCustomValue,
		Open:    dnsOpenCustomPrompt,
	}
}

func dnsFastestRow() setRow {
	return setRow{
		ID: "dnsFastest", Label: "Find the fastest DNS", Kind: setKindAction,
		Detail: setStatic("Times every choice from here, fastest first; pick one to use it"),
		Value:  dnsBenchValue,
		Open:   dnsBenchOpen,
	}
}

// ── Find the fastest DNS ─────────────────────────────────────────────────

// dnsBenchNames are what each server is asked for: names every service has
// cached, so the time is the trip, not the service's own lookup.
var dnsBenchNames = []string{"google.com.", "wikipedia.org.", "github.com."}

// dnsBenchResult is one choice's timing.
type dnsBenchResult struct {
	Value    string // the choice it selects
	Label    string
	Server   netip.Addr
	Answered int
	Median   time.Duration
}

var dnsBench atomic.Bool // a run is under way

func dnsBenchValue(*Config) string {
	if dnsBench.Load() {
		return "Timing…"
	}
	return ""
}

// dnsBenchOpen times every choice from here off the render thread, then
// offers them fastest first.
func dnsBenchOpen(c *Config) {
	if hotspotHoldsRadio() {
		setToast("Connect to a network first")
		return
	}
	if !dnsBench.CompareAndSwap(false, true) {
		setToast("Still timing the DNS servers…")
		return
	}
	setToast("Timing the DNS servers from here…")
	cands := dnsBenchCandidates(c)
	go func() {
		defer dnsBench.Store(false)
		if dnsUplinkSignature() == "" {
			tbxPost(func() { setToast("Connect to a network first") })
			return
		}
		results := dnsBenchRun(cands)
		tbxPost(func() { dnsBenchShow(results) })
	}()
}

// dnsBenchCandidates is every preset's first server, the network's own
// first server when it is known, and the first typed one.
func dnsBenchCandidates(c *Config) []dnsBenchResult {
	var out []dnsBenchResult
	if nets := dnsSnapshot().sys.Network; len(nets) > 0 {
		out = append(out, dnsBenchResult{Value: "", Label: "Your network", Server: nets[0]})
	}
	for _, p := range dnsPresets {
		out = append(out, dnsBenchResult{Value: p.ID, Label: p.Label, Server: p.Servers[0]})
	}
	if addrs, why := dnsWantedInCustom(c); why == "" {
		out = append(out, dnsBenchResult{Value: dnsModeCustom, Label: "Custom", Server: addrs[0]})
	}
	return out
}

// dnsBenchRun asks every candidate for each name, the candidates side by
// side and each one's names in turn, and sorts them: most answers first,
// then the quickest median.
func dnsBenchRun(cands []dnsBenchResult) []dnsBenchResult {
	var wg sync.WaitGroup
	for i := range cands {
		wg.Add(1)
		go func(r *dnsBenchResult) {
			defer wg.Done()
			var times []time.Duration
			for _, name := range dnsBenchNames {
				ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
				took, err := dnsAsk(ctx, r.Server, name)
				cancel()
				if err == nil {
					times = append(times, took)
				}
			}
			r.Answered = len(times)
			if len(times) > 0 {
				sort.Slice(times, func(a, b int) bool { return times[a] < times[b] })
				r.Median = times[len(times)/2]
			}
		}(&cands[i])
	}
	wg.Wait()
	sort.SliceStable(cands, func(a, b int) bool {
		if cands[a].Answered != cands[b].Answered {
			return cands[a].Answered > cands[b].Answered
		}
		return cands[a].Median < cands[b].Median
	})
	return cands
}

// dnsBenchShow offers the timings (main thread), or names the fastest when
// Settings was left while they ran, or something else is open over it.
func dnsBenchShow(results []dnsBenchResult) {
	if len(results) == 0 || results[0].Answered == 0 {
		ShowToast("No DNS server answered: check the connection", ToastKindWarning)
		return
	}
	best := results[0]
	c := appConfig
	if c == nil || setHub.scene == "" || setHubSceneName(c) != setHub.scene || setHub.sheet.open || virtualKeyboardActive {
		ShowToast(fmt.Sprintf("Fastest DNS here: %s, %d ms", best.Label, best.Median.Milliseconds()), ToastKindInfo)
		return
	}
	opts := make([]setOption, 0, len(results))
	for _, r := range results {
		note := fmt.Sprintf("%d ms · %s", r.Median.Milliseconds(), r.Server)
		switch {
		case r.Answered == 0:
			note = "No answer · " + r.Server.String()
		case r.Answered < len(dnsBenchNames):
			note += fmt.Sprintf(" · %d of %d answered", r.Answered, len(dnsBenchNames))
		}
		opts = append(opts, setOption{Value: r.Value, Label: r.Label, Note: note})
	}
	setSheetOpen(&setHub.sheet, "Fastest DNS here", "Times from this device on this network; pick one to use it",
		opts, dnsModeIn(c), func(value string) {
			if value == dnsModeIn(c) {
				// Already the choice (Custom would open the keyboard).
				setToast("DNS: " + dnsChoiceName(c))
				return
			}
			dnsChoose(c, value)
			setHub.dirty = true
		})
}

// ── Quitting ─────────────────────────────────────────────────────────────

// dnsShutdownStepAdd puts back the network's own DNS on the quit teardown
// (shutdown.go), beside the caches, while a file of ours may be in place —
// or may be by then: the worker keeps writing a choice until the step stops
// it.
func dnsShutdownStepAdd(steps []shutdownStep) []shutdownStep {
	dnsMgr.mu.Lock()
	wanted := dnsMgr.started && dnsMgr.want != nil && !dnsMgr.safe
	dnsMgr.mu.Unlock()
	if !dnsSystemSupported() || !(wanted || dnsSystemHolds()) {
		return steps
	}
	return append(steps, shutdownStep{label: "Putting the network's DNS back", group: 2, timeout: time.Second, fn: dnsShutdownStep})
}

func dnsShutdownStep() (string, error) {
	dnsQuitting.Store(true)
	dnsMgr.mu.Lock()
	dir := dnsMgr.dataDir
	dnsMgr.mu.Unlock()
	return dnsSystemRelease(dir)
}
