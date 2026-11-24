package main

// conncheck.go — the one-tap connection check (Tools → Connectivity →
// Connection check, and Network tools → Probes): Wi-Fi, the router, name
// lookups (DNS), the internet and secure (HTTPS) sites, tested in that order,
// then one sentence saying where the connection fails and what to do about it.
//
// Every probe it needs already existed — the Wi-Fi status, the default route,
// the neighbour table, the generate_204 probe that also spots a captive portal
// — but someone whose downloads fail had to know which tile to try first and
// how to read its answer. The check runs them in order and reads the answers
// together: a router that ignores probes while the internet answers is not the
// problem, and an internet that answers by address while names fail is DNS,
// not "no internet".
//
// The run is a scene task of its screen (conncheck_ui.go): leaving the screen
// stops it, and the next visit runs it again. Its state lives under
// connCheck.mu and the screen copies it out every frame.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// connStepID names one step, in the order they run.
type connStepID int

const (
	connStepNet connStepID = iota
	connStepRouter
	connStepDNS
	connStepInternet
	connStepSecure
	connStepCount
)

// connStepState is a step's outcome so far.
type connStepState int

const (
	connPending connStepState = iota
	connRunning
	connOK
	connWarn
	connFail
	connSkipped
)

// connStep is one step's result.
type connStep struct {
	id      connStepID
	title   string
	icon    string
	state   connStepState
	summary string   // the row's line
	facts   []string // the details sheet's lines
	fix     string   // what to do, when the connection fails here
}

// connRun is one whole check.
type connRun struct {
	seq      uint64
	running  bool
	started  time.Time
	finished time.Time
	steps    [connStepCount]connStep
	// verdict is the headline and advice the sentence under it; tone is the
	// state that decided them (connOK, connWarn or connFail).
	verdict string
	advice  string
	tone    connStepState
	// wifiFix says the fix is on the Wi-Fi page (no network, the radio off, a
	// sign-in page); clockFix that it is the date and time.
	wifiFix  bool
	clockFix bool
}

// connCheck is the last run. The worker writes it under mu; the screen reads
// a copy (connCheckSnapshot).
var connCheck struct {
	mu  sync.Mutex
	seq uint64
	run connRun
}

// The names and addresses the check asks. connDNSName is the host the
// connectivity probe's first endpoint lives on, so a name that resolves here
// is the one the next step needs; the two public resolvers answer TCP on 443
// and 53, which is how "the internet answers by address" is told apart from
// "the internet does not answer".
const (
	connDNSName     = "connectivitycheck.gstatic.com"
	connSecureURL   = "https://www.gstatic.com/generate_204"
	connPublicHostA = "1.1.1.1"
	connPublicHostB = "8.8.8.8"
)

// connStepTitle is a step's name and glyph. The first step is "Wi-Fi" on the
// handheld, which has nothing else, and "Network" on a computer that may be
// on a cable.
func connStepTitle(id connStepID) (string, string) {
	switch id {
	case connStepNet:
		if IsTrimuiDevice() {
			return "Wi-Fi", "wifi"
		}
		return "Network", "wifi"
	case connStepRouter:
		return "Router", "lan"
	case connStepDNS:
		return "Name lookups (DNS)", "dns"
	case connStepInternet:
		return "Internet", "globe"
	case connStepSecure:
		return "Secure sites (HTTPS)", "lock"
	}
	return "", ""
}

// connNewStep is a step that has not run yet.
func connNewStep(id connStepID) connStep {
	title, icon := connStepTitle(id)
	return connStep{id: id, title: title, icon: icon, state: connPending}
}

// connCheckSnapshot copies the last run. The facts slices are never written
// after a step is published, so sharing them is safe.
func connCheckSnapshot() connRun {
	connCheck.mu.Lock()
	defer connCheck.mu.Unlock()
	return connCheck.run
}

// connCheckRunning reports whether a check is under way.
func connCheckRunning() bool {
	connCheck.mu.Lock()
	defer connCheck.mu.Unlock()
	return connCheck.run.running
}

// connCheckStart starts a fresh check on the current scene's task scope. A
// run still going is superseded: its late answers are dropped by seq.
func connCheckStart() bool {
	connCheck.mu.Lock()
	connCheck.seq++
	seq := connCheck.seq
	run := connRun{seq: seq, running: true, started: time.Now()}
	for i := range run.steps {
		run.steps[i] = connNewStep(connStepID(i))
	}
	connCheck.run = run
	connCheck.mu.Unlock()
	started := runSceneTask(func(ctx context.Context) { connCheckWork(ctx, seq) })
	if !started {
		connCheck.mu.Lock()
		if connCheck.run.seq == seq {
			connCheck.run.running = false
			connCheck.run.verdict = "The check could not start"
			connCheck.run.tone = connWarn
		}
		connCheck.mu.Unlock()
	}
	return started
}

// connPut publishes one step of run seq. It reports false when a newer run
// replaced this one.
func connPut(seq uint64, st connStep) bool {
	connCheck.mu.Lock()
	defer connCheck.mu.Unlock()
	if connCheck.run.seq != seq {
		return false
	}
	connCheck.run.steps[st.id] = st
	return true
}

// connMark sets one step of run seq to state with a working line.
func connMark(seq uint64, id connStepID, state connStepState, summary string) {
	connCheck.mu.Lock()
	defer connCheck.mu.Unlock()
	if connCheck.run.seq != seq {
		return
	}
	connCheck.run.steps[id].state = state
	connCheck.run.steps[id].summary = summary
}

// connCheckWork runs the steps in order and publishes each as it lands.
func connCheckWork(ctx context.Context, seq uint64) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("conncheck", "the check stopped: %v", r)
			connCheck.mu.Lock()
			if connCheck.run.seq == seq {
				connCheck.run.running = false
				connCheck.run.verdict = "The check stopped unexpectedly"
				connCheck.run.advice = "Run it again; if it keeps stopping, send a bug report from Tools."
				connCheck.run.tone = connWarn
			}
			connCheck.mu.Unlock()
		}
	}()
	stopped := func() bool {
		if ctx.Err() == nil {
			return false
		}
		connCheck.mu.Lock()
		if connCheck.run.seq == seq {
			connCheck.run.running = false
			connCheck.run.verdict = "Check stopped"
			connCheck.run.tone = connWarn
		}
		connCheck.mu.Unlock()
		return true
	}

	connMark(seq, connStepNet, connRunning, "Reading this device's connection…")
	netStep, ip := connProbeNetwork()
	if !connPut(seq, netStep) || stopped() {
		return
	}
	if netStep.state == connFail {
		for id := connStepRouter; id < connStepCount; id++ {
			st := connNewStep(id)
			st.state, st.summary = connSkipped, "Needs a network first"
			connPut(seq, st)
		}
		connCheckFinish(seq)
		return
	}

	connMark(seq, connStepRouter, connRunning, "Knocking on the router…")
	if !connPut(seq, connProbeRouter(ctx, ip)) || stopped() {
		return
	}

	connMark(seq, connStepDNS, connRunning, "Looking up "+connDNSName+"…")
	dnsStep := connProbeDNS(ctx)
	if !connPut(seq, dnsStep) || stopped() {
		return
	}

	connMark(seq, connStepInternet, connRunning, "Asking the internet…")
	inetStep := connProbeInternet(ctx, dnsStep.state == connOK)
	if !connPut(seq, inetStep) || stopped() {
		return
	}

	if inetStep.state != connOK {
		st := connNewStep(connStepSecure)
		st.state, st.summary = connSkipped, "Needs the internet first"
		connPut(seq, st)
	} else {
		connMark(seq, connStepSecure, connRunning, "Opening a secure connection…")
		if !connPut(seq, connProbeSecure(ctx)) || stopped() {
			return
		}
	}
	connCheckFinish(seq)
}

// connCheckFinish reads the steps together and writes the verdict.
func connCheckFinish(seq uint64) {
	if connCheckFinishLocked(seq) {
		trophyEvent(tkNetTool)
	}
}

// connCheckFinishLocked writes the verdict of run seq under the lock; false
// when a newer run replaced it.
func connCheckFinishLocked(seq uint64) bool {
	connCheck.mu.Lock()
	defer connCheck.mu.Unlock()
	run := &connCheck.run
	if run.seq != seq {
		return false
	}
	run.running = false
	run.finished = time.Now()
	connReconcile(&run.steps)
	run.verdict, run.advice, run.tone = connVerdict(run.steps)
	run.wifiFix, run.clockFix = false, false
	for _, st := range run.steps {
		if st.state != connFail && st.state != connWarn {
			continue
		}
		switch {
		case st.id == connStepNet && st.state == connFail:
			run.wifiFix = true
		case st.id == connStepInternet && strings.Contains(st.summary, "sign in"):
			run.wifiFix = true
		case st.id == connStepSecure && strings.Contains(st.summary, "clock"):
			run.clockFix = true
		}
	}
	return true
}

// connReconcile corrects what one step got wrong on its own once the later
// steps are in: a router that ignored every probe while the internet answered
// is working (it just drops connection tests), so it is a note, not the fault.
// Pure, so the rule is testable without a network.
func connReconcile(steps *[connStepCount]connStep) {
	router := &steps[connStepRouter]
	if router.state == connFail && (steps[connStepInternet].state == connOK || steps[connStepDNS].state == connOK) {
		router.state = connWarn
		router.summary = strings.TrimSuffix(router.summary, " did not answer") + " ignores tests, but traffic gets through"
		router.fix = ""
	}
}

// connVerdict is the headline, the advice and the tone of a finished check:
// the first step that fails, in order, is where the connection breaks. Pure.
func connVerdict(steps [connStepCount]connStep) (string, string, connStepState) {
	for _, st := range steps {
		if st.state != connFail {
			continue
		}
		headline := ""
		switch st.id {
		case connStepNet:
			headline = "No network"
		case connStepRouter:
			headline = "The router does not answer"
		case connStepDNS:
			headline = "Name lookups fail"
		case connStepInternet:
			headline = "No internet on this network"
			if strings.Contains(st.summary, "sign in") {
				headline = "This Wi-Fi wants a sign-in"
			} else if strings.Contains(st.summary, "blocked") {
				headline = "Web requests are blocked"
			}
		case connStepSecure:
			headline = "Secure sites fail"
			if strings.Contains(st.summary, "clock") {
				headline = "The clock is wrong"
			}
		}
		return headline, st.fix, connFail
	}
	for _, st := range steps {
		if st.state == connWarn && st.fix != "" {
			return "Works, with a warning", st.fix, connWarn
		}
	}
	return "Everything works", "Wi-Fi, the router, name lookups, the internet and secure sites all answered.", connOK
}

// connMS writes a duration the way the rows do: "3 ms", "1.2 s".
func connMS(d time.Duration) string {
	if d < time.Second {
		ms := d.Milliseconds()
		if ms < 1 {
			ms = 1
		}
		return strconv.FormatInt(ms, 10) + " ms"
	}
	return fmt.Sprintf("%.1f s", d.Seconds())
}

// ── Steps ─────────────────────────────────────────────────────────────────

// connProbeNetwork is step one: does this device have an address, on which
// network, and how strong is the signal. It returns the step and the address.
func connProbeNetwork() (connStep, string) {
	st := connNewStep(connStepNet)
	ip := localIPv4()
	status, statusErr := wifiReadStatus()
	ssid := ""
	if statusErr == nil {
		ssid = strings.TrimSpace(status.SSID)
		if ip == "" && strings.TrimSpace(status.IP) != "" && !shotModeOn() {
			ip = strings.TrimSpace(status.IP)
		}
	}
	if ssid != "" && shotModeOn() {
		ssid = shotSSID
	}
	radioOn, radioKnown := wifiEnabled()
	if ip == "" {
		st.state = connFail
		switch {
		case radioKnown && !radioOn:
			st.summary = "Wi-Fi is turned off"
			st.fix = "Turn Wi-Fi on in Quick Settings or on the Wi-Fi page, then run the check again."
		case statusErr == nil && status.Connected && ssid != "":
			st.summary = "Joined “" + ssid + "” but got no address"
			st.fix = "The router did not give this device an address. Join the network again from the Wi-Fi page; if that fails, restart the router."
		default:
			st.summary = "Not connected to a network"
			st.fix = "Choose a network on the Wi-Fi page and join it."
		}
		st.facts = []string{"Address: none", "Wi-Fi radio: " + connRadioWord(radioOn, radioKnown)}
		if ssid != "" {
			st.facts = append(st.facts, "Network: “"+ssid+"”")
		}
		return st, ""
	}
	st.state = connOK
	parts := []string{"Connected"}
	if ssid != "" {
		parts[0] = "Connected to “" + ssid + "”"
	}
	parts = append(parts, ip)
	st.facts = []string{"Address: " + ip}
	if ssid != "" {
		st.facts = append(st.facts, "Network: “"+ssid+"”")
	}
	if m := netToolsSignalDBM().FindStringSubmatch(powerWiFiSummary()); m != nil {
		if dbm, err := strconv.Atoi(m[1]); err == nil {
			word := strings.Trim(wifiSignalVerdict(float64(dbm)), "()")
			parts = append(parts, m[1]+" dBm")
			st.facts = append(st.facts, "Signal: "+m[1]+" dBm ("+word+")")
			if dbm < -75 {
				st.state = connWarn
				parts = append(parts, "weak signal")
				st.fix = "The signal is weak: move closer to the router, or away from thick walls and microwaves."
			}
		}
	}
	st.summary = strings.Join(parts, " · ")
	return st, ip
}

// connRadioWord says the radio's state for the facts.
func connRadioWord(on, known bool) string {
	switch {
	case !known:
		return "unknown"
	case on:
		return "on"
	}
	return "off"
}

// connProbeRouter is step two: the default gateway, asked for a TCP handshake
// on the ports a home router serves (an answer, or a refusal, both prove it is
// there), and the neighbour table as the fallback for a router that drops
// every connection on its LAN side.
func connProbeRouter(ctx context.Context, ip string) connStep {
	st := connNewStep(connStepRouter)
	gw, guessed := defaultGateway(), false
	if gw == "" {
		gw, guessed = powerGatewayGuess(ip), true
	}
	if gw == "" {
		st.state = connWarn
		st.summary = "No router address found"
		st.facts = []string{"This device's routes name no default gateway."}
		return st
	}
	shown := gw
	if shotModeOn() {
		shown = "192.168.1.1"
	}
	st.facts = []string{"Router: " + shown}
	if guessed {
		st.facts = append(st.facts, "The address is a guess from this device's own (the routes cannot be read here).")
	}
	if took, ok := connDialAny(ctx, gw, []string{"53", "80", "443"}, 1500*time.Millisecond); ok {
		st.state = connOK
		st.summary = shown + " answered in " + connMS(took)
		st.facts = append(st.facts, "Answered a connection test in "+connMS(took))
		return st
	}
	if connNeighbourHas(gw) {
		st.state = connOK
		st.summary = shown + " is there (it ignores connection tests)"
		st.facts = append(st.facts, "No answer to connection tests, but it is in the neighbour table: it is on the network.")
		return st
	}
	st.state = connFail
	st.summary = shown + " did not answer"
	st.facts = append(st.facts, "No answer on ports 53, 80 or 443 within 1.5 s, and not in the neighbour table.")
	st.fix = "This device is on the network but cannot reach the router. Move closer to it, or restart the router."
	return st
}

// connDialAny dials host on every port at once and reports how long the first
// answer took. A refused connection is an answer: something at that address
// said no. Every dial ends with ctx or timeout.
func connDialAny(ctx context.Context, host string, ports []string, timeout time.Duration) (time.Duration, bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type answer struct {
		took time.Duration
		ok   bool
	}
	ch := make(chan answer, len(ports))
	start := time.Now()
	var d net.Dialer
	for _, port := range ports {
		go func(port string) {
			conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
			if err == nil {
				conn.Close()
				ch <- answer{took: time.Since(start), ok: true}
				return
			}
			ch <- answer{took: time.Since(start), ok: connRefused(err)}
		}(port)
	}
	for range ports {
		if a := <-ch; a.ok {
			return a.took, true
		}
	}
	return 0, false
}

// connRefused reports a connection the far end refused ("connection refused"
// on Linux, "actively refused it" on Windows).
func connRefused(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "refused")
}

// connNeighbourHas reports whether ip is in the neighbour table with a
// hardware address, which means it answered on the local network.
func connNeighbourHas(ip string) bool {
	list, err := lanNeighbourList()
	if err != nil {
		return false
	}
	for _, n := range list {
		if n.IP == ip && strings.TrimSpace(n.MAC) != "" {
			return true
		}
	}
	return false
}

// connProbeDNS is step three: one lookup through the system's resolver, the
// same path every download takes.
func connProbeDNS(ctx context.Context) connStep {
	st := connNewStep(connStepDNS)
	servers := connDNSServers()
	if len(servers) > 0 {
		st.facts = []string{"Name servers: " + strings.Join(servers, ", ")}
	}
	lctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	start := time.Now()
	addrs, err := net.DefaultResolver.LookupHost(lctx, connDNSName)
	took := time.Since(start)
	if err == nil && len(addrs) > 0 {
		st.state = connOK
		st.summary = "Names resolve in " + connMS(took)
		if len(servers) > 0 {
			st.summary += " · via " + servers[0]
		}
		st.facts = append(st.facts, connDNSName+" is "+addrs[0])
		return st
	}
	st.state = connFail
	st.summary = "Names do not resolve"
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		switch {
		case dnsErr.IsTimeout:
			st.summary = "The name server did not answer"
		case dnsErr.IsNotFound:
			st.summary = "The name server knows no such name"
		}
	}
	if err != nil {
		st.facts = append(st.facts, "Error: "+firstLine(err.Error()))
	}
	st.fix = "Restart the router. If names still fail, set the router's DNS to 1.1.1.1 or 8.8.8.8."
	return st
}

// connDNSServers lists the resolvers in /etc/resolv.conf (Linux and the
// handheld), at most three; nil where there is no such file. Screenshot mode
// shows a stand-in, as it does for every address.
func connDNSServers() []string {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		server := fields[1]
		if shotModeOn() {
			server = "192.168.1.1"
		}
		out = append(out, server)
		if len(out) == 3 {
			break
		}
	}
	return out
}

// connProbeInternet is step four: the connectivity probe (which also sees a
// captive portal), then — if it fails — a handshake with two public resolvers
// by address, which needs no name lookup. dnsOK says whether step three
// passed, so "answers by address" can be read as a DNS fault.
func connProbeInternet(ctx context.Context, dnsOK bool) connStep {
	st := connNewStep(connStepInternet)
	start := time.Now()
	res := probeConnectivityWithin(8 * time.Second)
	took := time.Since(start)
	switch {
	case res.online:
		st.state = connOK
		st.summary = "The internet answers in " + connMS(took)
		st.facts = []string{"A test page answered with nothing in between."}
		return st
	case res.portal != "":
		st.state = connFail
		st.summary = "This Wi-Fi wants you to sign in"
		st.facts = []string{"Sign-in page: " + res.portal}
		st.fix = "Open the Wi-Fi page and press Sign in, sign in on the page that opens, then run the check again."
		return st
	}
	if ctx.Err() != nil {
		st.state = connSkipped
		st.summary = "Stopped"
		return st
	}
	host := connPublicHostA
	ipTook, ipOK := connDialAny(ctx, host, []string{"443", "80"}, 2500*time.Millisecond)
	if !ipOK {
		host = connPublicHostB
		ipTook, ipOK = connDialAny(ctx, host, []string{"443", "53"}, 2500*time.Millisecond)
	}
	st.facts = []string{"The test page did not answer (" + res.reason + ")."}
	switch {
	case ipOK && !dnsOK:
		st.state = connWarn
		st.summary = "Answers by address only (" + host + " in " + connMS(ipTook) + ")"
		st.facts = append(st.facts, host+" answered by address: the internet is there, names are the problem.")
	case ipOK:
		st.state = connFail
		st.summary = "Web requests are blocked, though " + host + " answers"
		st.facts = append(st.facts, host+" answered by address in "+connMS(ipTook)+".")
		st.fix = "Something on this network blocks web requests: a firewall, a proxy or parental controls. Try another network, or ask whoever runs this one."
	default:
		st.state = connFail
		st.summary = "The internet does not answer"
		st.facts = append(st.facts, connPublicHostA+" and "+connPublicHostB+" did not answer by address either.")
		st.fix = "The router has no internet connection. Restart the router and the modem, and check the router's internet light."
	}
	return st
}

// connProbeSecure is step five: one HTTPS request with the same certificate
// store the rest of JukaHub uses (the firmware's bundle where the system has
// none), so a wrong clock or a network that intercepts TLS is named here
// instead of as "offline".
func connProbeSecure(ctx context.Context) connStep {
	st := connNewStep(connStepSecure)
	rctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, connSecureURL, nil)
	if err != nil {
		st.state, st.summary = connWarn, "Could not build the request"
		return st
	}
	start := time.Now()
	resp, err := connSecureClient().Do(req)
	took := time.Since(start)
	if err == nil {
		resp.Body.Close()
		st.state = connOK
		st.summary = "Secure connections work (" + connMS(took) + ")"
		st.facts = []string{"A secure test page answered with a valid certificate."}
		return st
	}
	st.state = connFail
	st.facts = []string{"Error: " + firstLine(err.Error())}
	switch bugrepClassifyError(err) {
	case bugrepErrClock:
		st.summary = "The device clock is wrong (it says " + time.Now().Format("2 Jan 2006") + ")"
		st.fix = "Secure sites check the date. Set the date and time (Settings → Date & time), or turn on network time, then run the check again."
	case bugrepErrTrust:
		st.summary = "Secure sites are not trusted on this network"
		st.fix = "This network intercepts secure connections, or this device is missing its certificate list. Try another network; if it happens everywhere, update JukaHub."
	default:
		st.summary = "Secure connections fail"
		st.fix = "Plain web requests work but secure ones do not: something on this network interferes with them. Try another network."
	}
	return st
}

// connSecureClient is a one-request client with the certificate store the
// rest of JukaHub verifies with (bugrepRootCAs: the firmware's bundle where
// the system has none) and keep-alives off, so nothing outlives the check.
func connSecureClient() *http.Client {
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 6 * time.Second}).DialContext,
		TLSHandshakeTimeout: 6 * time.Second,
		DisableKeepAlives:   true,
	}
	if pool := bugrepRootCAs(); pool != nil {
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Timeout: 8 * time.Second, Transport: tr}
}

// connCheckReport is the whole run as text, for Copy.
func connCheckReport(run connRun) string {
	var b strings.Builder
	b.WriteString("JukaHub connection check")
	if !run.finished.IsZero() {
		b.WriteString(" · " + run.finished.Format("2006-01-02 15:04"))
	}
	b.WriteString("\n")
	if run.verdict != "" {
		b.WriteString(run.verdict + "\n")
	}
	if run.advice != "" {
		b.WriteString(run.advice + "\n")
	}
	for _, st := range run.steps {
		fmt.Fprintf(&b, "\n%s: %s — %s\n", st.title, connStateWord(st.state), st.summary)
		for _, f := range st.facts {
			b.WriteString("  " + f + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// connStateWord is a state as the pills and the report say it.
func connStateWord(s connStepState) string {
	switch s {
	case connRunning:
		return "Checking…"
	case connOK:
		return "OK"
	case connWarn:
		return "Warning"
	case connFail:
		return "Problem"
	case connSkipped:
		return "Not checked"
	}
	return "Waiting"
}
