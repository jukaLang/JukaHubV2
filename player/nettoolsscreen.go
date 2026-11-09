package main

// nettoolsscreen.go — Tools → Network tools: the file servers, the probes and
// the lookups on a page of their own.
//
// They used to be twenty rows of the Tools overlay, and every probe opened the
// Power tools viewer on top of it, so reading a result meant leaving Tools and
// pressing Back twice. Here a result opens in a sheet on this page, and the
// probes that need no input (public IP, LAN neighbours, listening ports, the
// subnet plan, the Wi-Fi signal and the clock check) run as the page opens:
// their tiles fill in with the answer while the user is still looking.
//
// The page keeps every tool's last result in netToolsJobs. Probes run on the
// page's scene task (scenetasks.go), so leaving the page cancels them and a
// late answer is dropped instead of landing on another screen; coming back
// reruns whatever has gone stale.
//
// Removed with the move: the Service banner row (a raw socket read nobody
// needed on a handheld), and the HTTP check, TLS certificate and Security
// headers rows, which belong to a web page and now live in the Browser's site
// information (httpCheckSummary, certSummary and headerAuditSummary are kept
// for it).

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
)

// netToolsMode is what A does on a tile.
type netToolsMode uint8

const (
	// netToolsModeServer starts the server, or shows its details while it runs.
	netToolsModeServer netToolsMode = iota
	// netToolsModePanel opens the tool's own panel (the phone pairing card).
	netToolsModePanel
	// netToolsModeRun runs the probe now and shows it running.
	netToolsModeRun
	// netToolsModeAuto ran as the page opened; A shows its answer.
	netToolsModeAuto
	// netToolsModeInput asks for a target on the keyboard, then runs.
	netToolsModeInput
)

// netToolsSpec is one tile's fixed description.
type netToolsSpec struct {
	id          string
	section     string
	sectionIcon string
	label       string
	icon        string
	need        toolsHubNeed
	mode        netToolsMode
	idle        string
}

// netToolsSpecs is the page, in display order.
var netToolsSpecs = []netToolsSpec{
	{id: "ftp", section: "Servers & sharing", sectionIcon: "server", label: "FTP server", icon: "server", need: toolsHubNeedLAN, mode: netToolsModeServer},
	{id: "ssh", label: "SSH / SFTP", icon: "key", need: toolsHubNeedLAN, mode: netToolsModeServer},
	{id: "companion", label: "Phone companion", icon: "phone", need: toolsHubNeedLAN, mode: netToolsModePanel, idle: "Type, use a remote and send files from a phone"},
	{id: "gateway", section: "Probes", sectionIcon: "radar", label: "Gateway port scan", icon: "scan", need: toolsHubNeedLAN, mode: netToolsModeRun, idle: "13 common ports on your router"},
	{id: "clipscan", label: "Clipboard host scan", icon: "paste", need: toolsHubNeedLAN, mode: netToolsModeRun, idle: "Scan a host or IP you copied"},
	{id: "ping", label: "Ping", icon: "radar", need: toolsHubNeedInternet, mode: netToolsModeRun, idle: "Round trip to 1.1.1.1"},
	{id: "traceroute", label: "Traceroute", icon: "route", need: toolsHubNeedInternet, mode: netToolsModeRun, idle: "Every hop to 1.1.1.1"},
	{id: "dns", label: "DNS lookup", icon: "dns", need: toolsHubNeedInternet, mode: netToolsModeInput, idle: "Addresses, mail and name servers"},
	{id: "portcheck", label: "Port check", icon: "link", need: toolsHubNeedLAN, mode: netToolsModeInput, idle: "Is one port open? Type host:port"},
	{id: "neighbours", label: "LAN neighbours", icon: "devices", need: toolsHubNeedLAN, mode: netToolsModeAuto, idle: "Devices this network has seen"},
	{id: "wifisignal", label: "Wi-Fi signal", icon: "wifi", mode: netToolsModeAuto, idle: "Signal strength and link speed"},
	{id: "publicip", section: "Lookups", sectionIcon: "globe", label: "Public IP", icon: "public-ip", need: toolsHubNeedInternet, mode: netToolsModeAuto, idle: "What the internet sees"},
	{id: "whois", label: "Domain whois", icon: "id-card", need: toolsHubNeedInternet, mode: netToolsModeInput, idle: "Registrar, dates and name servers"},
	{id: "clock", label: "Clock check", icon: "clock-check", need: toolsHubNeedInternet, mode: netToolsModeAuto, idle: "Device time against NTP"},
	{id: "subnet", label: "Subnet plan", icon: "subnet", need: toolsHubNeedLAN, mode: netToolsModeAuto, idle: "Network, range and host count"},
	{id: "listening", label: "Listening ports", icon: "plug", mode: netToolsModeAuto, idle: "Open ports on this device"},
	{id: "codec", label: "Encode / decode", icon: "code", mode: netToolsModeInput, idle: "Base64, hex and URL, both ways"},
}

// netToolsSpecFor finds a tile's description.
func netToolsSpecFor(id string) (netToolsSpec, bool) {
	for _, spec := range netToolsSpecs {
		if spec.id == id {
			return spec, true
		}
	}
	return netToolsSpec{}, false
}

// netToolsFresh is how long an automatic result stands before the page runs
// the probe again on its next visit.
const netToolsFresh = 45 * time.Second

// netToolsPingHost is the address Ping and Traceroute use: Cloudflare's
// resolver answers ICMP and is the same host the connectivity probe dials.
const netToolsPingHost = "1.1.1.1"

// ── Results ───────────────────────────────────────────────────────────────

// netToolsResult is one tool's last run.
type netToolsResult struct {
	running bool
	seq     uint64
	target  string
	text    string
	summary string
	tone    toolsHubTone
	failed  bool
	at      time.Time
	started time.Time
}

// netToolsOutcome is what one probe run produced.
type netToolsOutcome struct {
	text    string
	summary string
	tone    toolsHubTone
	err     error
}

// netToolsJobs holds every tool's last result. Probes finish on their own
// goroutines; the page copies results out under the lock, and version tells
// it when something changed.
var netToolsJobs = struct {
	mu      sync.Mutex
	seq     uint64
	version uint64
	byID    map[string]*netToolsResult
}{byID: map[string]*netToolsResult{}}

// netToolsResultOf copies one tool's last result.
func netToolsResultOf(id string) (netToolsResult, bool) {
	netToolsJobs.mu.Lock()
	defer netToolsJobs.mu.Unlock()
	if res := netToolsJobs.byID[id]; res != nil {
		return *res, true
	}
	return netToolsResult{}, false
}

// netToolsVersion changes whenever a result starts or lands.
func netToolsVersion() uint64 {
	netToolsJobs.mu.Lock()
	defer netToolsJobs.mu.Unlock()
	return netToolsJobs.version
}

// netToolsRun starts fn for tool id on the page's scene task. A newer run of
// the same tool supersedes an older one, and a run that outlives the page is
// dropped: the next visit runs it again.
func netToolsRun(id, target string, fn func(context.Context) netToolsOutcome) bool {
	netToolsJobs.mu.Lock()
	netToolsJobs.seq++
	seq := netToolsJobs.seq
	res := netToolsJobs.byID[id]
	if res == nil {
		res = &netToolsResult{}
		netToolsJobs.byID[id] = res
	}
	res.running = true
	res.seq = seq
	res.target = target
	res.started = time.Now()
	netToolsJobs.version++
	netToolsJobs.mu.Unlock()

	started := runSceneTask(func(ctx context.Context) {
		out := netToolsSafeRun(ctx, fn)
		netToolsJobs.mu.Lock()
		defer netToolsJobs.mu.Unlock()
		cur := netToolsJobs.byID[id]
		if cur == nil || cur.seq != seq {
			return
		}
		cur.running = false
		netToolsJobs.version++
		if ctx.Err() != nil {
			return
		}
		cur.text, cur.summary, cur.tone, cur.failed = out.text, out.summary, out.tone, false
		if out.err != nil {
			msg := netToolsErrorText(id, out.err)
			cur.failed = true
			cur.tone = toolsHubToneWarn
			if strings.TrimSpace(cur.text) == "" {
				cur.text = "Error: " + msg
			} else {
				cur.text += "\n\nError: " + msg
			}
			if cur.summary == "" {
				cur.summary = netToolsShortError(msg)
			}
		}
		cur.at = time.Now()
	})
	if !started {
		netToolsJobs.mu.Lock()
		res.running = false
		netToolsJobs.version++
		netToolsJobs.mu.Unlock()
	}
	return started
}

// netToolsErrCrashed marks a run whose tool panicked: that is never the
// network's fault, whatever the connection is doing.
var netToolsErrCrashed = errors.New("the tool stopped unexpectedly")

// netToolsSafeRun turns a panicking probe into an error result.
func netToolsSafeRun(ctx context.Context, fn func(context.Context) netToolsOutcome) (out netToolsOutcome) {
	defer func() {
		if r := recover(); r != nil {
			out = netToolsOutcome{err: fmt.Errorf("%w: %v", netToolsErrCrashed, r)}
		}
	}()
	return fn(ctx)
}

// netToolsErrorText words a failed run. A tool that needs no network reports
// its own error (a string that is not base64 is not a missing Wi-Fi), and so
// does a crash; the network tools get the shared plain wording for a dropped
// connection or a slow server.
func netToolsErrorText(id string, err error) string {
	if errors.Is(err, netToolsErrCrashed) {
		return firstLine(err.Error())
	}
	if spec, ok := netToolsSpecFor(id); ok && spec.need == toolsHubNeedNothing {
		return firstLine(err.Error())
	}
	return plainNetworkError(err)
}

// netToolsShortError fits an error on a tile's value line.
func netToolsShortError(msg string) string {
	if msg == offlinePlainNotice {
		return "No connection"
	}
	r := []rune(strings.TrimSpace(strings.TrimPrefix(msg, "Error: ")))
	if len(r) > 0 {
		r[0] = unicode.ToUpper(r[0])
	}
	return string(r)
}

// netToolsBusyText is the working line a running tool shows.
func netToolsBusyText(id, target string) string {
	switch id {
	case "gateway", "clipscan":
		return "Scanning " + target + "…"
	case "ping":
		return "Pinging " + netToolsPingHost + "…"
	case "traceroute":
		return "Tracing the route to " + netToolsPingHost + "…"
	case "dns":
		return "Looking up " + target + "…"
	case "portcheck":
		return "Connecting to " + target + "…"
	case "neighbours":
		return "Reading the neighbour table…"
	case "wifisignal":
		return "Reading the Wi-Fi signal…"
	case "publicip":
		return "Asking what the internet sees…"
	case "whois":
		return "Asking the registry about " + target + "…"
	case "clock":
		return "Comparing the clock with pool.ntp.org…"
	case "subnet":
		return "Reading this device's address…"
	case "listening":
		return "Reading the socket table…"
	case "codec":
		return "Decoding…"
	}
	return "Working…"
}

// ── Probes ────────────────────────────────────────────────────────────────

var (
	netToolsOpenPort   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?m)^\s*\d+/tcp\s+open`) })
	netToolsPingTime   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`time[=<]\s?([0-9]+(?:\.[0-9]+)?)\s?ms`) })
	netToolsTCPTry     = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`try \d+: ([0-9]+) ms`) })
	netToolsSignalDBM  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`Signal: (-?[0-9]+) dBm(?: \(([a-z]+)\))?`) })
	netToolsSignalPct  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?i)signal\s*:\s*([0-9]+)\s*%`) })
	netToolsHop        = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?m)^\s*[0-9]+\s`) })
	netToolsDNSRecords = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`([0-9]+) records? in`) })
)

// netToolsProbeStub, when set, replaces every tool's run: the tests use it so
// the page's flow is exercised without the network. Nil in the app.
var netToolsProbeStub func(id, target string) func(context.Context) netToolsOutcome

// netToolsProbe resolves the run behind a tool.
func netToolsProbe(id, target string) func(context.Context) netToolsOutcome {
	if netToolsProbeStub != nil {
		return netToolsProbeStub(id, target)
	}
	return netToolsProbeFor(id, target)
}

// netToolsProbeFor is the run behind a tool, bound to its target.
func netToolsProbeFor(id, target string) func(context.Context) netToolsOutcome {
	switch id {
	case "gateway", "clipscan":
		return func(context.Context) netToolsOutcome { return netToolsScanOutcome(target) }
	case "ping":
		return func(context.Context) netToolsOutcome { return netToolsPingOutcome(powerPingSummary(netToolsPingHost)) }
	case "traceroute":
		return func(context.Context) netToolsOutcome {
			text, err := powerTraceRoute(netToolsPingHost)
			out := netToolsOutcome{text: text, err: err}
			if hops := len(netToolsHop().FindAllString(text, -1)); hops > 0 {
				out.summary = strconv.Itoa(hops) + " hops to " + netToolsPingHost
			}
			return out
		}
	case "dns":
		return func(context.Context) netToolsOutcome {
			q, err := powerDNSParseQuery(target)
			if err != nil {
				return netToolsOutcome{err: err}
			}
			text := powerDNSReport(q)
			out := netToolsOutcome{text: text, summary: q.Host}
			if m := netToolsDNSRecords().FindStringSubmatch(text); m != nil {
				out.summary = q.Host + " · " + m[1] + " records"
				if m[1] == "0" {
					out.tone = toolsHubToneWarn
				}
			}
			return out
		}
	case "portcheck":
		return func(ctx context.Context) netToolsOutcome {
			text, open, err := portCheckRun(ctx, target)
			out := netToolsOutcome{text: text, err: err, summary: target + " is closed"}
			if open {
				out.summary, out.tone = target+" is open", toolsHubToneGood
			}
			return out
		}
	case "neighbours":
		return func(context.Context) netToolsOutcome {
			list, err := lanNeighbourList()
			if err != nil {
				return netToolsOutcome{err: err}
			}
			return netToolsOutcome{text: formatNeighbours(list), summary: netToolsCount(len(list), "device", "devices") + " seen"}
		}
	case "wifisignal":
		return func(context.Context) netToolsOutcome { return netToolsSignalOutcome(powerWiFiSummary()) }
	case "publicip":
		return func(ctx context.Context) netToolsOutcome {
			info, err := publicIPLookup(ctx)
			if err != nil {
				return netToolsOutcome{err: err}
			}
			summary := info.IP
			if info.Country != "" {
				summary += " · " + info.Country
			}
			return netToolsOutcome{text: formatPublicIP(info), summary: summary}
		}
	case "whois":
		return func(ctx context.Context) netToolsOutcome {
			domain, info, err := whoisQuery(ctx, target)
			if err != nil {
				return netToolsOutcome{err: err}
			}
			out := netToolsOutcome{text: whoisText(domain, info, rdapRegistrar(info.Entities)), summary: domain}
			if expires := rdapEventDate(info.Events, "expiration"); expires != "" {
				out.summary = domain + " · expires " + expires
			}
			return out
		}
	case "clock":
		return func(ctx context.Context) netToolsOutcome {
			r, err := clockMeasure(ctx)
			if err != nil {
				return netToolsOutcome{err: err}
			}
			return netToolsClockOutcome(r)
		}
	case "subnet":
		return func(context.Context) netToolsOutcome {
			iface, ipnet, err := subnetInterface()
			if err != nil {
				return netToolsOutcome{err: err}
			}
			text, err := subnetText(iface, ipnet.IP, ipnet.Mask)
			if err != nil {
				return netToolsOutcome{err: err}
			}
			ones, _ := ipnet.Mask.Size()
			summary := ipnet.IP.Mask(ipnet.Mask).String() + "/" + strconv.Itoa(ones)
			if ones <= 30 {
				summary += " · " + strconv.Itoa(1<<(32-ones)-2) + " hosts"
			}
			return netToolsOutcome{text: text, summary: summary}
		}
	case "listening":
		return func(context.Context) netToolsOutcome {
			list, err := listeningPortList()
			if err != nil {
				return netToolsOutcome{err: err}
			}
			return netToolsOutcome{text: formatListeningPorts(list), summary: netToolsCount(len(list), "port", "ports") + " listening"}
		}
	case "codec":
		return func(context.Context) netToolsOutcome {
			text, err := codecSummary(target)
			return netToolsOutcome{text: text, err: err, summary: "Decoded “" + netToolsClip(target, 24) + "”"}
		}
	}
	return nil
}

// netToolsCount is "1 device" / "3 devices".
func netToolsCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// netToolsClip shortens a typed target for a tile.
func netToolsClip(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// netToolsScanOutcome scans the 13 common ports on host.
func netToolsScanOutcome(host string) netToolsOutcome {
	text := powerScanText(host, 400*time.Millisecond)
	open := len(netToolsOpenPort().FindAllString(text, -1))
	out := netToolsOutcome{text: text, summary: host + " · no open ports"}
	if open > 0 {
		out.summary = host + " · " + netToolsCount(open, "open port", "open ports")
		out.tone = toolsHubToneGood
	}
	return out
}

// netToolsPingOutcome summarises a ping report by its average round trip.
func netToolsPingOutcome(text string) netToolsOutcome {
	out := netToolsOutcome{text: text, summary: "No reply from " + netToolsPingHost, tone: toolsHubToneWarn}
	matches := netToolsPingTime().FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		matches = netToolsTCPTry().FindAllStringSubmatch(text, -1)
	}
	total, n := 0.0, 0
	for _, m := range matches {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			total += v
			n++
		}
	}
	if n > 0 {
		out.summary = fmt.Sprintf("%.0f ms average to %s", total/float64(n), netToolsPingHost)
		out.tone = toolsHubToneNeutral
	}
	return out
}

// netToolsSignalOutcome summarises the local signal report.
func netToolsSignalOutcome(text string) netToolsOutcome {
	out := netToolsOutcome{text: text, summary: "No signal report"}
	if m := netToolsSignalDBM().FindStringSubmatch(text); m != nil {
		out.summary = m[1] + " dBm"
		if m[2] != "" {
			out.summary += " · " + m[2]
		}
		if m[2] == "weak" {
			out.tone = toolsHubToneWarn
		}
		return out
	}
	if m := netToolsSignalPct().FindStringSubmatch(text); m != nil {
		out.summary = m[1] + "% signal"
	}
	return out
}

// netToolsClockOutcome turns an SNTP reading into the report and a verdict.
func netToolsClockOutcome(r clockReading) netToolsOutcome {
	out := netToolsOutcome{text: clockText(r.device, r.server, r.offset, r.rtt, r.stratum)}
	off := r.offset
	switch {
	case off > 2*time.Second:
		out.summary = fmt.Sprintf("Behind by %.1f s", off.Seconds())
		out.tone = toolsHubToneWarn
	case off < -2*time.Second:
		out.summary = fmt.Sprintf("Ahead by %.1f s", -off.Seconds())
		out.tone = toolsHubToneWarn
	default:
		out.summary = fmt.Sprintf("In sync (%+d ms)", off.Milliseconds())
		out.tone = toolsHubToneGood
	}
	return out
}

// ── Page ──────────────────────────────────────────────────────────────────

// netToolsSheetKey is what the open sheet was built from; the sheet is rebuilt
// only when it changes (a result lands, a server starts, a second ticks by).
type netToolsSheetKey struct {
	id      string
	seq     uint64
	running bool
	failed  bool
	age     int64
	server  bool
	ip      string
	keys    bool   // the hints name keyboard keys (inputhints.go)
	remap   uint32 // hintRemapGen(): Accessibility's remap the hints follow
	clients uint64 // fsClientsVersion: who is connected changed
	armed   bool   // the whole-system switch waits for its second press
}

// netTools is the page's state.
var netTools = struct {
	page         toolsHubPage
	tiles        []toolsHubTile
	builtAt      time.Time
	builtVersion uint64
	sheet        toolsHubSheet
	sheetID      string
	sheetKey     netToolsSheetKey
	// gateway is the router address the port scan targets, read when the
	// page opens and again only when the LAN address changes.
	gateway   string
	gatewayIP string
	// pendingID/pendingTarget is a run asked for from off the page (the
	// keyboard submitted after the page was left); it starts on arrival.
	pendingID     string
	pendingTarget string
}{page: newToolsHubPage()}

// netToolsOpen opens the Network tools page at its top (cross-area contract).
func netToolsOpen(config *Config) {
	if config == nil {
		return
	}
	toolsHubEnsureScenes(config)
	idx := sceneIndexExact(config, netToolsSceneName)
	if idx < 0 {
		return
	}
	qs.open = false
	netTools.page.reset()
	netTools.sheet.open = false
	if idx == currentSceneIndex {
		netToolsEnter(config)
		return
	}
	changeSceneTo(config, idx)
}

// netToolsEnter loads the page: the servers' published state, the router
// address, and every automatic probe whose answer has gone stale.
func netToolsEnter(config *Config) {
	_ = config
	publishFTPState()
	publishSSHState()
	net := toolsHubReadNet()
	netTools.gateway = netToolsGatewayFor(net.ip)
	netTools.gatewayIP = net.ip
	for _, spec := range netToolsSpecs {
		if spec.mode != netToolsModeAuto || toolsHubBlockedTag(spec.need, net) != "" {
			continue
		}
		if res, ok := netToolsResultOf(spec.id); ok && (res.running || (!res.failed && time.Since(res.at) < netToolsFresh)) {
			continue
		}
		netToolsRun(spec.id, "", netToolsProbe(spec.id, ""))
	}
	if id := netTools.pendingID; id != "" {
		target := netTools.pendingTarget
		netTools.pendingID, netTools.pendingTarget = "", ""
		if fn := netToolsProbe(id, target); fn != nil && netToolsRun(id, target, fn) {
			netToolsShowSheet(id)
		}
	}
	netToolsRebuild(net)
	netTools.page.follow = true
}

// netToolsGatewayFor is the router's address: the kernel's default route, or
// the usual .1 of this subnet when the route cannot be read (Windows).
func netToolsGatewayFor(ip string) string {
	if ip == "" {
		return ""
	}
	if gw := defaultGateway(); gw != "" {
		return gw
	}
	return powerGatewayGuess(ip)
}

// netToolsRebuild rebuilds the tiles from the network, the servers and the
// last results, keeping the focus on the same tool.
func netToolsRebuild(net toolsHubNet) {
	focusID := ""
	if c := netTools.page.cursor; c >= 0 && c < len(netTools.tiles) {
		focusID = netTools.tiles[c].id
	}
	netTools.tiles = netToolsBuildTiles(net, netTools.gateway)
	netTools.builtAt = time.Now()
	netTools.builtVersion = netToolsVersion()
	if focusID != "" {
		if i := toolsHubIndexOf(netTools.tiles, focusID); i >= 0 {
			netTools.page.cursor = i
		}
	}
	netTools.page.clampCursor(len(netTools.tiles))
}

// netToolsBuildTiles is the page's tile list.
func netToolsBuildTiles(net toolsHubNet, gateway string) []toolsHubTile {
	tiles := make([]toolsHubTile, 0, len(netToolsSpecs))
	for _, spec := range netToolsSpecs {
		t := toolsHubTile{
			id:          spec.id,
			section:     spec.section,
			sectionIcon: spec.sectionIcon,
			label:       spec.label,
			icon:        spec.icon,
			need:        spec.need,
			value:       spec.idle,
		}
		switch spec.id {
		case "ftp":
			t.value = "Off — share a folder with another PC"
			if fileServerOnTrimui() {
				t.value = "Off — share games, saves and media with a PC"
			}
			if ftpSrv.Running() {
				t.value, t.tone = "On · "+ftpSrv.Address()+netToolsConnected(ftpSrv.clients.count()), toolsHubToneGood
			}
		case "ssh":
			t.value = "Off — browse files from a PC"
			if sshSrv.Running() {
				t.value, t.tone = "On · "+sshSrv.Address()+netToolsConnected(sshSrv.clients.count()), toolsHubToneGood
			}
		case "companion":
			switch {
			case companion.err != "":
				t.value, t.tone = "Unavailable — "+companion.err, toolsHubToneWarn
			case companion.open:
				t.value, t.tone = "Showing the pairing code", toolsHubToneGood
			}
		case "gateway":
			if gateway != "" {
				t.value = "Router " + gateway + " · 13 common ports"
			}
		case "dns":
			if host := strings.TrimSpace(powerDNSLastHost); host != "" {
				t.value = "Last: " + host
			}
		case "portcheck":
			if netLastPort != "" {
				t.value = "Last: " + netLastPort
			}
		case "whois":
			if netLastDomain != "" {
				t.value = "Last: " + netLastDomain
			}
		}
		if res, ok := netToolsResultOf(spec.id); ok {
			switch {
			case res.running:
				t.tone = toolsHubToneBusy
				if res.summary == "" {
					t.value = "Checking…"
				} else {
					t.value = res.summary
				}
			case res.summary != "":
				t.value, t.tone = res.summary, res.tone
			}
		}
		tiles = append(tiles, t)
	}
	toolsHubApplyNeeds(tiles, net)
	return tiles
}

// netToolsHeaderGlyph is the header's icon, built once.
var netToolsHeaderGlyph = func(r *sdl.Renderer, box sdl.Rect, _ sdl.Color) {
	DrawHomeIcon(r, "network", box, false)
}

// netToolsHeader is the page bar; its subtitle says what the network allows.
func netToolsHeader(net toolsHubNet) UIPageHeader {
	sub := "File servers, probes and lookups for this network"
	subInk := sdl.Color{}
	switch {
	case net.ip == "":
		sub, subInk = "Offline — connect to Wi-Fi to use most of these tools", ColorToastWarn
	case !net.online:
		sub, subInk = "No internet on this Wi-Fi — local tools still work", ColorToastWarn
	}
	return UIPageHeader{
		Title:         "Network tools",
		Subtitle:      sub,
		SubtitleColor: StatusInk(subInk),
		TitleSize:     menuTitleSize,
		Glyph:         netToolsHeaderGlyph,
	}
}

// renderNetTools draws the page, the shared bars and, on top, the sheet.
func renderNetTools(renderer *sdl.Renderer, config *Config) {
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	net := toolsHubReadNet()
	if net.ip != netTools.gatewayIP {
		netTools.gateway = netToolsGatewayFor(net.ip)
		netTools.gatewayIP = net.ip
	}
	if netTools.tiles == nil || netToolsVersion() != netTools.builtVersion || time.Since(netTools.builtAt) > time.Second {
		netToolsRebuild(net)
	}
	toolsHubDrawPage(renderer, config, &netTools.page, netToolsHeader(net), netTools.tiles, net.ip,
		toolsHubRingVisible() && !netTools.sheet.open)
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
	if netTools.sheet.open {
		netToolsSyncSheet()
		toolsHubDrawSheet(renderer, config, &netTools.sheet)
	}
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetClipRect(nil)
}

// ── Sheet ─────────────────────────────────────────────────────────────────

// netToolsShowSheet opens the sheet for a tool.
func netToolsShowSheet(id string) {
	if netTools.sheetID != id || !netTools.sheet.open {
		netTools.sheet.scroll = 0
		netTools.sheet.focus = 0
	}
	netTools.sheetID = id
	netTools.sheetKey = netToolsSheetKey{}
	netTools.sheet.open = true
	netToolsSyncSheet()
}

// netToolsCloseSheet closes the sheet.
func netToolsCloseSheet() {
	if !netTools.sheet.open {
		return
	}
	netTools.sheet.open = false
	PlayBackSound()
}

// netToolsSyncSheet rebuilds the sheet when what it shows has changed.
func netToolsSyncSheet() {
	id := netTools.sheetID
	spec, ok := netToolsSpecFor(id)
	if !ok {
		netTools.sheet.open = false
		return
	}
	key := netToolsSheetKey{id: id, server: spec.mode == netToolsModeServer, ip: toolsIPAddress(), keys: hintsUseKeyboard(), remap: hintRemapGen()}
	res, _ := netToolsResultOf(id)
	switch id {
	case "ftp", "ssh":
		key.running = ftpSrv.Running()
		if id == "ssh" {
			key.running = sshSrv.Running()
			key.armed = netToolsWholeSystemArmed()
		}
		// The client list says "connected 3 min ago": refresh it every half
		// minute, and at once when someone connects or a transfer starts.
		key.clients = fsClientsVersion.Load()
		key.age = time.Now().Unix() / 30
	default:
		key.seq, key.running, key.failed = res.seq, res.running, res.failed
		if !res.at.IsZero() {
			key.age = int64(time.Since(res.at) / time.Second)
		}
	}
	if key == netTools.sheetKey && netTools.sheet.title != "" {
		return
	}
	netTools.sheetKey = key
	s := &netTools.sheet
	s.title, s.icon = spec.label, spec.icon
	if spec.mode == netToolsModeServer {
		netToolsServerSheet(s, id)
	} else {
		netToolsResultSheet(s, spec, res)
	}
	if s.focus >= len(s.pills) {
		s.focus = len(s.pills) - 1
	}
	if s.focus < 0 {
		s.focus = 0
	}
}

// netToolsAge is "just now", "12 s ago", "3 min ago".
func netToolsAge(at time.Time) string {
	d := time.Since(at)
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return strconv.Itoa(int(d/time.Second)) + " s ago"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " min ago"
	}
	return at.Format("15:04")
}

// netToolsResultSheet fills the sheet with a probe's last result.
func netToolsResultSheet(s *toolsHubSheet, spec netToolsSpec, res netToolsResult) {
	target := res.target
	if target == "" {
		switch spec.id {
		case "ping", "traceroute":
			target = netToolsPingHost
		case "gateway":
			target = netTools.gateway
		}
	}
	s.subInk = sdl.Color{}
	switch {
	case res.running:
		s.subtitle = "Running…"
	case res.at.IsZero():
		s.subtitle = "Not run yet"
	default:
		s.subtitle = "Updated " + netToolsAge(res.at)
		if res.failed {
			s.subtitle = "Failed " + netToolsAge(res.at)
			s.subInk = StatusInk(ColorToastWarn)
		}
	}
	if target != "" {
		s.subtitle = target + " · " + s.subtitle
	}
	s.setText(res.text)
	s.busy = ""
	if res.running {
		s.busy = netToolsBusyText(spec.id, target)
	}
	s.empty = "Press A to run " + spec.label
	run := "Run again"
	if res.at.IsZero() && !res.running {
		run = "Run"
	}
	s.pills = s.pills[:0]
	s.pills = append(s.pills, toolsHubPill{id: "run", label: run, accent: true})
	if spec.mode == netToolsModeInput {
		s.pills = append(s.pills, toolsHubPill{id: "edit", label: "New target"})
	}
	if res.text != "" {
		s.pills = append(s.pills, toolsHubPill{id: "copy", label: "Copy"})
	}
	s.pills = append(s.pills, toolsHubPill{id: "close", label: "Close"})
	hints := []FooterHint{{Button: "A", Label: "Select"}}
	if res.text != "" {
		hints = append(hints, FooterHint{Button: "X", Label: "Copy"})
	}
	if spec.mode == netToolsModeInput {
		hints = append(hints, FooterHint{Button: "Y", Label: "New target"})
	}
	hints = append(hints, FooterHint{Button: "Up/Down", Label: "Scroll"}, FooterHint{Button: "B", Label: "Close"})
	s.hint = hintLine(hints...)
}

// netToolsConnected is the tile's " · 2 connected" suffix ("" for nobody).
func netToolsConnected(n int) string {
	if n <= 0 {
		return ""
	}
	return " · " + strconv.Itoa(n) + " connected"
}

// netToolsFTPFolders says what FTP shares before it runs (the real list needs
// the card read, which the start does). The handheld test is the device's, as
// fileServerCardRoot's: P().IsHandheld() holds on any Linux computer.
func netToolsFTPFolders() string {
	if fileServerOnTrimui() {
		return "Roms, BIOS, Saves, media folders and JukaHub downloads"
	}
	return "the shared folder next to JukaHub (" + cachedFTPRoot() + ") and JukaHub downloads"
}

// netToolsSSHDefaultFolder names SFTP's usual root.
func netToolsSSHDefaultFolder() string {
	if fileServerOnTrimui() {
		return "the SD card"
	}
	return "your home folder"
}

// netToolsSSHFolder says what SFTP will share on its next start.
func netToolsSSHFolder() string {
	if custom := sshRootOverride(); custom != "" {
		return custom
	}
	if sshWholeSystemOn() {
		return "/ — the whole file system (advanced)"
	}
	return netToolsSSHDefaultFolder()
}

// netToolsSSHOpensSheet reports whether a stopped SFTP's tile opens its sheet
// instead of starting it: where the sheet holds a choice to make before the
// start (what SFTP shares), the press must be able to reach it.
func netToolsSSHOpensSheet() bool {
	return sshWholeSystemSupported() && sshRootOverride() == ""
}

// netToolsWholeSystemArmedUntil is when the whole-system switch's first press
// stops waiting for the second.
var netToolsWholeSystemArmedUntil time.Time

func netToolsWholeSystemArmed() bool { return time.Now().Before(netToolsWholeSystemArmedUntil) }

// netToolsWholeSystemPress is the SFTP sheet's advanced switch. On takes two
// presses — the first says what it shares — and lasts until JukaHub restarts;
// off is one press. The root is chosen when the server starts, so the switch
// waits for a stopped server.
func netToolsWholeSystemPress() {
	if sshSrv.Running() {
		ShowToast("Stop the SFTP server first, then choose what it shares", ToastKindInfo)
		return
	}
	if sshWholeSystemSession.Load() {
		sshWholeSystemSession.Store(false)
		netToolsWholeSystemArmedUntil = time.Time{}
		ShowToast("SFTP will share "+netToolsSSHDefaultFolder()+" again", ToastKindInfo)
		return
	}
	if netToolsWholeSystemArmed() {
		sshWholeSystemSession.Store(true)
		netToolsWholeSystemArmedUntil = time.Time{}
		logInfo("ssh", "whole file system share switched on for this run")
		ShowToast("SFTP will share the whole file system until JukaHub restarts", ToastKindWarning)
		return
	}
	netToolsWholeSystemArmedUntil = time.Now().Add(8 * time.Second)
	ShowToast("Advanced: this shares every file on the device, system files included — press again to confirm", ToastKindWarning)
}

// netToolsServerSheet fills the sheet with a server's details: where to
// connect, how to sign in, what it shares and who is connected.
func netToolsServerSheet(s *toolsHubSheet, id string) {
	var b strings.Builder
	running := false
	wholeSwitch := ""
	switch id {
	case "ftp":
		running = ftpSrv.Running()
		if running {
			fmt.Fprintf(&b, "Status:   Running\nAddress:  %s\nUser:     %s\nPassword: %s\n",
				ftpSrv.Address(), ftpSrv.User(), ftpSrv.Password())
			fmt.Fprintf(&b, "Folders:  %s\n", strings.Join(ftpSrv.ShareNames(), ", "))
			fsClientLines(&b, ftpSrv.Clients())
		} else {
			b.WriteString("Status:   Stopped\n")
			fmt.Fprintf(&b, "Folders:  %s\n", netToolsFTPFolders())
		}
		b.WriteString("\nSign in with this user name and password; the password is new every time the server starts and is never saved or logged. " +
			"FTP is not encrypted: on a network you do not trust, use SFTP instead.")
	case "ssh":
		running = sshSrv.Running()
		whole := false
		if running {
			fmt.Fprintf(&b, "Status:   Running\nAddress:  %s\nUser:     %s\nPassword: %s\n",
				sshSrv.Address(), sshSrv.User(), sshSrv.Password())
			if fp := sshSrv.Fingerprint(); fp != "" {
				fmt.Fprintf(&b, "Host key: %s\n", fp)
			}
			fmt.Fprintf(&b, "Folder:   %s\n", sshSrv.Root())
			whole = sshSrv.WholeSystem()
		} else {
			b.WriteString("Status:   Stopped\n")
			fmt.Fprintf(&b, "Folder:   %s\n", netToolsSSHFolder())
			// JUKAHUB_SSH_ROOT wins over the switch: warn only when the
			// next start really shares "/".
			whole = sshWholeSystemNext()
		}
		b.WriteString("Access:   SFTP file transfer only, no shell\n")
		if running {
			fsClientLines(&b, sshSrv.Clients())
		}
		if whole {
			b.WriteString("\nAdvanced: the whole file system is shared. Anyone who signs in can change system files — share " +
				netToolsSSHDefaultFolder() + " again when you are done.\n")
		}
		b.WriteString("\nThe password is new every time the server starts and is never saved or logged.")
		// The switch is offered while stopped, and only where "/" means one
		// file system; a config or environment that forces it is not undone
		// here, and with JUKAHUB_SSH_ROOT set it would change nothing.
		if !running && netToolsSSHOpensSheet() && (sshWholeSystemSession.Load() || !whole) {
			wholeSwitch = "Share whole system…"
			if netToolsWholeSystemArmed() {
				wholeSwitch = "Press again to share everything"
			}
			if sshWholeSystemSession.Load() {
				wholeSwitch = "Share " + netToolsSSHDefaultFolder() + " only"
			}
		}
	}
	b.WriteString("\nThe server switches itself off after " + fsIdleLimitText() + " nobody uses it, and when the device joins another network.")
	s.subInk = sdl.Color{}
	if running {
		s.subtitle = "Running on " + toolsIPAddress()
		s.subInk = StatusInk(ColorToastSuccess)
	} else {
		s.subtitle = "Stopped"
	}
	s.setText(b.String())
	s.busy = ""
	s.empty = ""
	s.pills = s.pills[:0]
	hints := make([]FooterHint, 0, 4)
	if running {
		s.pills = append(s.pills,
			toolsHubPill{id: "toggle", label: "Stop server", danger: true},
			toolsHubPill{id: "copyaddr", label: "Copy address"})
		hints = append(hints, FooterHint{Button: "A", Label: "Select"}, FooterHint{Button: "X", Label: "Copy address"})
	} else {
		s.pills = append(s.pills, toolsHubPill{id: "toggle", label: "Start server", accent: true})
		hints = append(hints, FooterHint{Button: "A", Label: "Select"})
	}
	if wholeSwitch != "" {
		s.pills = append(s.pills, toolsHubPill{id: "wholesys", label: wholeSwitch, danger: netToolsWholeSystemArmed()})
	}
	s.pills = append(s.pills, toolsHubPill{id: "close", label: "Close"})
	hints = append(hints, FooterHint{Button: "B", Label: "Close"})
	s.hint = hintLine(hints...)
}

// netToolsSheetAction runs one of the sheet's actions.
func netToolsSheetAction(config *Config, action string) {
	id := netTools.sheetID
	switch action {
	case "run":
		netToolsRerun(config, id)
	case "edit":
		netToolsPrompt(id)
	case "copy":
		res, _ := netToolsResultOf(id)
		netToolsCopy(res.text, "Result copied")
	case "copyaddr":
		addr := ftpSrv.Address()
		if id == "ssh" {
			addr = sshSrv.Address()
		}
		netToolsCopy(addr, "Address copied")
	case "toggle":
		PlayActivateSound()
		if id == "ssh" {
			toggleSSHServer()
			publishSSHState()
		} else {
			toggleFTPServer()
			publishFTPState()
		}
		netTools.sheetKey = netToolsSheetKey{}
		netToolsSyncSheet()
	case "wholesys":
		PlayActivateSound()
		netToolsWholeSystemPress()
		netTools.sheetKey = netToolsSheetKey{}
		netToolsSyncSheet()
	case "close":
		netToolsCloseSheet()
	}
}

// netToolsCopy puts text on the clipboard with a toast.
func netToolsCopy(text, done string) {
	if strings.TrimSpace(text) == "" {
		ShowToast("Nothing to copy yet", ToastKindInfo)
		return
	}
	if err := copyTextToClipboard(text, "Network tools"); err != nil {
		ShowToast("Could not copy: "+firstLine(err.Error()), ToastKindError)
		return
	}
	ShowToast(done, ToastKindSuccess)
}

// ── Actions ───────────────────────────────────────────────────────────────

// netToolsPress is A / Enter on the page.
func netToolsPress(config *Config) {
	if netTools.page.chip {
		toolsHubCopyIP()
		return
	}
	netToolsActivate(config, netTools.page.cursor)
}

// netToolsActivate is A on tile idx: a dimmed tile explains what it needs; the
// rest start, run, ask or show, by their mode.
func netToolsActivate(config *Config, idx int) {
	if idx < 0 || idx >= len(netTools.tiles) {
		return
	}
	t := netTools.tiles[idx]
	if t.blocked != "" {
		ShowToast(toolsHubBlockedMessage(t.label, t.blocked), ToastKindWarning)
		return
	}
	spec, ok := netToolsSpecFor(t.id)
	if !ok {
		return
	}
	switch spec.mode {
	case netToolsModeServer:
		netToolsServerPress(spec.id)
	case netToolsModePanel:
		// The pairing card is a modal of its own above the page.
		openCompanionOverlay()
	case netToolsModeRun:
		PlayActivateSound()
		netToolsRerun(config, spec.id)
	case netToolsModeAuto:
		PlayActivateSound()
		res, _ := netToolsResultOf(spec.id)
		if !res.running && (res.at.IsZero() || res.failed || time.Since(res.at) > netToolsFresh) {
			netToolsRun(spec.id, "", netToolsProbe(spec.id, ""))
		}
		netToolsShowSheet(spec.id)
	case netToolsModeInput:
		PlayActivateSound()
		netToolsPrompt(spec.id)
	}
}

// netToolsServerPress starts a stopped server and shows how to reach it; on a
// running server it shows the details (Stop is on the sheet, so a stray press
// never cuts a transfer off). A stopped SFTP on Linux opens its sheet with
// Start first: what it shares is chosen there before the start, and a press
// that started it at once left that choice reachable only after a stop.
func netToolsServerPress(id string) {
	running := ftpSrv.Running()
	if id == "ssh" {
		running = sshSrv.Running()
	}
	if !running && id == "ssh" && netToolsSSHOpensSheet() {
		PlayActivateSound()
		netToolsShowSheet(id)
		return
	}
	if !running {
		PlayActivateSound()
		if id == "ssh" {
			toggleSSHServer()
			publishSSHState()
			running = sshSrv.Running()
		} else {
			toggleFTPServer()
			publishFTPState()
			running = ftpSrv.Running()
		}
		if !running {
			return
		}
	} else {
		PlayActivateSound()
	}
	netToolsShowSheet(id)
}

// netToolsRerun runs a tool again with what it ran on last, and shows it.
func netToolsRerun(config *Config, id string) {
	spec, ok := netToolsSpecFor(id)
	if !ok {
		return
	}
	if tag := toolsHubBlockedTag(spec.need, toolsHubReadNet()); tag != "" {
		ShowToast(toolsHubBlockedMessage(spec.label, tag), ToastKindWarning)
		return
	}
	res, _ := netToolsResultOf(id)
	target := res.target
	switch id {
	case "gateway":
		target = netTools.gateway
		if target == "" {
			ShowToast("No router address yet — connect to Wi-Fi first", ToastKindWarning)
			return
		}
	case "clipscan":
		text, err := powerClipboardText()
		if err != nil || text == "" {
			ShowToast("Copy a host name or IP address first, then scan it", ToastKindWarning)
			return
		}
		host, err := powerCleanHost(text)
		if err != nil {
			ShowToast(err.Error(), ToastKindWarning)
			return
		}
		target = host
	case "dns", "portcheck", "whois", "codec":
		if strings.TrimSpace(target) == "" {
			netToolsPrompt(id)
			return
		}
	}
	if toolsScenesCurrent(config) != netToolsSceneName {
		netTools.pendingID, netTools.pendingTarget = id, target
		netToolsOpen(config)
		return
	}
	if netToolsRun(id, target, netToolsProbe(id, target)) {
		netToolsShowSheet(id)
	}
}

// netToolsPrompt asks for a tool's target on the on-screen keyboard. The
// field starts with the last target, so checking again is an edit.
func netToolsPrompt(id string) {
	res, _ := netToolsResultOf(id)
	switch id {
	case "dns":
		vkOpenDNSQuery(res.target)
	case "portcheck":
		vkOpenNetPrompt(netPromptPort, res.target)
	case "whois":
		vkOpenNetPrompt(netPromptWhois, res.target)
	case "codec":
		vkOpenNetPrompt(netPromptCodec, res.target)
	}
}

// netToolsSubmit runs a tool on a target the keyboard just submitted. Off the
// page (the keyboard outlived it) the page opens and the run starts there.
func netToolsSubmit(id, target string) {
	config := appConfig
	if config != nil && toolsScenesCurrent(config) != netToolsSceneName {
		netTools.pendingID, netTools.pendingTarget = id, target
		netToolsOpen(config)
		return
	}
	if netToolsRun(id, target, netToolsProbe(id, target)) {
		netToolsShowSheet(id)
	}
}

// netToolsRunDNS is the DNS keyboard's submit (powerDNSLookupSubmit hands it
// over): the lookup runs on the Network tools page, which opens first when the
// keyboard outlived it. It reports false only without a config, where the
// Power tools viewer still answers.
func netToolsRunDNS(q powerDNSQuery) bool {
	if appConfig == nil {
		return false
	}
	netToolsSubmit("dns", q.Host)
	return true
}

// ── Input ─────────────────────────────────────────────────────────────────

// netToolsKey is the page's keyboard; the open sheet takes every key.
func netToolsKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	if netTools.sheet.open {
		return netToolsSheetKeyDown(e, config)
	}
	p := &netTools.page
	switch e.Keysym.Sym {
	case sdl.K_UP:
		return p.move(netTools.tiles, 0, -1)
	case sdl.K_DOWN:
		return p.move(netTools.tiles, 0, 1)
	case sdl.K_LEFT:
		return p.move(netTools.tiles, -1, 0)
	case sdl.K_RIGHT:
		return p.move(netTools.tiles, 1, 0)
	case sdl.K_PAGEUP:
		p.jumpSection(netTools.tiles, -1)
	case sdl.K_PAGEDOWN:
		p.jumpSection(netTools.tiles, 1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		netToolsPress(config)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		goBackScene(config)
	default:
		return false
	}
	return true
}

// netToolsSheetKeyDown drives the open sheet from the keyboard.
func netToolsSheetKeyDown(e *sdl.KeyboardEvent, config *Config) bool {
	s := &netTools.sheet
	switch e.Keysym.Sym {
	case sdl.K_UP:
		if s.scrollBy(-2) {
			PlayNavSound()
		}
	case sdl.K_DOWN:
		if s.scrollBy(2) {
			PlayNavSound()
		}
	case sdl.K_PAGEUP:
		s.scrollBy(-s.fits)
	case sdl.K_PAGEDOWN:
		s.scrollBy(s.fits)
	case sdl.K_LEFT:
		s.movePill(-1)
	case sdl.K_RIGHT:
		s.movePill(1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		netToolsSheetAction(config, s.focusedPill())
	case sdl.K_x, sdl.K_c:
		netToolsSheetCopyKey(config)
	case sdl.K_y, sdl.K_e:
		if spec, ok := netToolsSpecFor(netTools.sheetID); ok && spec.mode == netToolsModeInput {
			netToolsSheetAction(config, "edit")
		}
	case sdl.K_r:
		if spec, ok := netToolsSpecFor(netTools.sheetID); ok && spec.mode != netToolsModeServer {
			netToolsSheetAction(config, "run")
		}
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		netToolsCloseSheet()
	}
	return true
}

// netToolsSheetCopyKey is X: the result, or a server's address.
func netToolsSheetCopyKey(config *Config) {
	spec, ok := netToolsSpecFor(netTools.sheetID)
	if !ok {
		return
	}
	if spec.mode == netToolsModeServer {
		netToolsSheetAction(config, "copyaddr")
		return
	}
	netToolsSheetAction(config, "copy")
}

// netToolsPad is the page's controller; the open sheet takes every button.
func netToolsPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	if netTools.sheet.open {
		s := &netTools.sheet
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			if s.scrollBy(-2) {
				PlayNavSound()
			}
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			if s.scrollBy(2) {
				PlayNavSound()
			}
		case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
			s.scrollBy(-s.fits)
		case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
			s.scrollBy(s.fits)
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			s.movePill(-1)
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			s.movePill(1)
		case sdl.CONTROLLER_BUTTON_A:
			netToolsSheetAction(config, s.focusedPill())
		case sdl.CONTROLLER_BUTTON_X:
			netToolsSheetCopyKey(config)
		case sdl.CONTROLLER_BUTTON_Y:
			if spec, ok := netToolsSpecFor(netTools.sheetID); ok && spec.mode == netToolsModeInput {
				netToolsSheetAction(config, "edit")
			}
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
			netToolsCloseSheet()
		}
		return true
	}
	p := &netTools.page
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return p.move(netTools.tiles, 0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return p.move(netTools.tiles, 0, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return p.move(netTools.tiles, -1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return p.move(netTools.tiles, 1, 0)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		p.jumpSection(netTools.tiles, -1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		p.jumpSection(netTools.tiles, 1)
	case sdl.CONTROLLER_BUTTON_A:
		netToolsPress(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(config)
	default:
		return false
	}
	return true
}

// netToolsClick is a click or tap on the page or its sheet.
func netToolsClick(mx, my int32, config *Config) bool {
	if netTools.sheet.open {
		s := &netTools.sheet
		if id := s.hitPill(mx, my); id != "" {
			for i := range s.pills {
				if s.pills[i].id == id {
					s.focus = i
				}
			}
			netToolsSheetAction(config, id)
			return true
		}
		if !pointInRect(mx, my, s.card) {
			netToolsCloseSheet()
		}
		return true
	}
	p := &netTools.page
	if p.chipRect.W > 0 && hitTouchTarget(p.chipRect, mx, my) {
		p.chip = true
		toolsHubCopyIP()
		return true
	}
	if i := p.hitTile(mx, my); i >= 0 {
		p.chip = false
		p.cursor = i
		p.wantX = -1
		netToolsActivate(config, i)
		return true
	}
	return false
}

// netToolsWheel scrolls the sheet by lines, or the page.
func netToolsWheel(y int32) bool {
	if netTools.sheet.open {
		netTools.sheet.scrollBy(int(-y) * 3)
		return true
	}
	return netTools.page.wheel(y)
}

// netToolsFooterHints follows the sheet, and names what A does on the
// focused tile.
func netToolsFooterHints(hints []FooterHint) []FooterHint {
	if netTools.sheet.open {
		return []FooterHint{
			{Button: "Up/Down", Label: "Scroll"},
			{Button: "A", Label: "Select"},
			{Button: "X", Label: "Copy"},
			{Button: "B", Label: "Close"},
		}
	}
	if netTools.page.chip {
		return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Copy IP"}, {Button: "B", Label: "Back"}}
	}
	verb := "Open"
	if c := netTools.page.cursor; c >= 0 && c < len(netTools.tiles) {
		if spec, ok := netToolsSpecFor(netTools.tiles[c].id); ok {
			switch spec.mode {
			case netToolsModeServer:
				verb = "Start"
				if (spec.id == "ftp" && ftpSrv.Running()) || (spec.id == "ssh" && sshSrv.Running()) {
					verb = "Details"
				}
			case netToolsModeRun:
				verb = "Run"
			case netToolsModeAuto:
				verb = "Details"
			case netToolsModeInput:
				verb = "Type target"
			}
		}
	}
	if len(hints) > 1 {
		hints[1] = FooterHint{Button: "A", Label: verb}
	}
	return append(hints, FooterHint{Button: "L1/R1", Label: "Section"})
}

// netToolsScene registers the page with the scene registry.
type netToolsScene struct{ sceneBase }

func (netToolsScene) render(r *sdl.Renderer, c *Config) bool {
	renderNetTools(r, c)
	return true
}

func (netToolsScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return netToolsKey(e, c)
}

func (netToolsScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return netToolsPad(e, c)
}

func init() { registerScene(netToolsScene{sceneBase{name: netToolsSceneName}}) }
