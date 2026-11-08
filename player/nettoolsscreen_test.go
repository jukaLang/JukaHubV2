package main

// The Network tools page: what each tile is and what the network allows it,
// how a run lands in the result registry and in the sheet, the probes that run
// as the page opens, and the keys. Every probe here is stubbed through
// netToolsProbeStub or handed in directly: nothing reaches the network.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// netToolsTestJobs gives the test an empty result registry and puts the app's
// back afterwards, together with the probe stub.
func netToolsTestJobs(t *testing.T) {
	t.Helper()
	netToolsJobs.mu.Lock()
	prev := netToolsJobs.byID
	netToolsJobs.byID = map[string]*netToolsResult{}
	netToolsJobs.mu.Unlock()
	prevStub := netToolsProbeStub
	t.Cleanup(func() {
		// The test's runs finish before the registry they write to is swapped.
		waitForSceneWork()
		netToolsJobs.mu.Lock()
		netToolsJobs.byID = prev
		netToolsJobs.mu.Unlock()
		netToolsProbeStub = prevStub
	})
}

// netToolsCanned answers every tool with a fixed outcome and records which
// tools were asked to run.
type netToolsCanned struct {
	mu    sync.Mutex
	asked []string
	out   map[string]netToolsOutcome
}

func (c *netToolsCanned) install() {
	netToolsProbeStub = func(id, target string) func(context.Context) netToolsOutcome {
		c.mu.Lock()
		c.asked = append(c.asked, id)
		out, ok := c.out[id]
		c.mu.Unlock()
		if !ok {
			out = netToolsOutcome{text: "Result: " + id + " " + target, summary: id + " done"}
		}
		return func(context.Context) netToolsOutcome { return out }
	}
}

func (c *netToolsCanned) askedFor() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.asked...)
}

// netToolsSetResult puts a finished result in the registry.
func netToolsSetResult(id string, res netToolsResult) {
	netToolsJobs.mu.Lock()
	netToolsJobs.seq++
	res.seq = netToolsJobs.seq
	netToolsJobs.byID[id] = &res
	netToolsJobs.version++
	netToolsJobs.mu.Unlock()
}

func TestNetToolsSpecsArePageOrdered(t *testing.T) {
	sections := map[string]string{"ftp": "Servers & sharing", "gateway": "Probes", "publicip": "Lookups"}
	seen := map[string]bool{}
	for _, spec := range netToolsSpecs {
		if seen[spec.id] {
			t.Errorf("tile %q is listed twice", spec.id)
		}
		seen[spec.id] = true
		if spec.section != sections[spec.id] {
			t.Errorf("tile %q opens section %q, want %q", spec.id, spec.section, sections[spec.id])
		}
		if spec.section != "" && spec.sectionIcon == "" {
			t.Errorf("section %q has no glyph", spec.section)
		}
		if strings.TrimSpace(spec.label) == "" || spec.icon == "" {
			t.Errorf("tile %q has no label or icon", spec.id)
		}
		switch spec.mode {
		case netToolsModeRun, netToolsModeAuto, netToolsModeInput:
			if netToolsProbeFor(spec.id, "example.com") == nil {
				t.Errorf("tile %q runs nothing", spec.id)
			}
			if spec.idle == "" {
				t.Errorf("tile %q has no line to show before its first run", spec.id)
			}
		}
	}
	if netToolsSpecs[0].id != "ftp" {
		t.Errorf("the page opens on %q, want the FTP server", netToolsSpecs[0].id)
	}
	if netToolsProbeFor("nope", "") != nil {
		t.Error("an unknown tool has a probe")
	}
	// The probes that need no typing run as the page opens.
	for _, id := range []string{"neighbours", "wifisignal", "publicip", "clock", "subnet", "listening"} {
		if spec, ok := netToolsSpecFor(id); !ok || spec.mode != netToolsModeAuto {
			t.Errorf("%q does not run on arrival", id)
		}
	}
}

// TestNetToolsTilesFollowTheNetwork: the LAN tools want an address, the
// internet tools want a route, and the device's own tools always run.
func TestNetToolsTilesFollowTheNetwork(t *testing.T) {
	netToolsTestJobs(t)
	ip := "192.168.1.20"
	cases := []struct {
		name string
		net  toolsHubNet
		want map[string]string
	}{
		{"no Wi-Fi", toolsHubNet{}, map[string]string{
			"ftp": toolsHubTagWiFi, "companion": toolsHubTagWiFi, "gateway": toolsHubTagWiFi,
			"ping": toolsHubTagWiFi, "publicip": toolsHubTagWiFi, "neighbours": toolsHubTagWiFi,
			"wifisignal": "", "listening": "", "codec": "",
		}},
		{"Wi-Fi without internet", toolsHubNet{ip: ip}, map[string]string{
			"ftp": "", "ssh": "", "gateway": "", "portcheck": "", "neighbours": "", "subnet": "",
			"ping": toolsHubTagInternet, "traceroute": toolsHubTagInternet, "dns": toolsHubTagInternet,
			"publicip": toolsHubTagInternet, "whois": toolsHubTagInternet, "clock": toolsHubTagInternet,
		}},
		{"online", toolsHubNet{ip: ip, online: true}, map[string]string{
			"ftp": "", "ping": "", "dns": "", "publicip": "", "clock": "", "codec": "",
		}},
	}
	for _, c := range cases {
		tiles := netToolsBuildTiles(c.net, "192.168.1.1")
		if len(tiles) != len(netToolsSpecs) {
			t.Fatalf("%s: %d tiles for %d tools", c.name, len(tiles), len(netToolsSpecs))
		}
		for id, want := range c.want {
			i := toolsHubIndexOf(tiles, id)
			if i < 0 {
				t.Fatalf("%s: no %q tile", c.name, id)
			}
			if tiles[i].blocked != want {
				t.Errorf("%s: %q tag = %q, want %q", c.name, id, tiles[i].blocked, want)
			}
		}
	}

	tiles := netToolsBuildTiles(toolsHubNet{ip: ip, online: true}, "192.168.1.1")
	if v := tiles[toolsHubIndexOf(tiles, "gateway")].value; !strings.Contains(v, "192.168.1.1") {
		t.Errorf("gateway tile = %q, want the router's address", v)
	}

	// A result replaces the idle line; a running tool says so.
	netToolsSetResult("publicip", netToolsResult{summary: "203.0.113.9 · AU", at: time.Now()})
	netToolsSetResult("clock", netToolsResult{running: true, started: time.Now()})
	tiles = netToolsBuildTiles(toolsHubNet{ip: ip, online: true}, "")
	if v := tiles[toolsHubIndexOf(tiles, "publicip")].value; v != "203.0.113.9 · AU" {
		t.Errorf("public IP tile = %q, want the last answer", v)
	}
	if clock := tiles[toolsHubIndexOf(tiles, "clock")]; clock.tone != toolsHubToneBusy || clock.value != "Checking…" {
		t.Errorf("a running clock check = %+v, want it shown as working", clock)
	}
}

// TestNetToolsServerTilesShowWhatIsRunning: a running server says so on its
// tile and on the hub's Network tools tile.
func TestNetToolsServerTilesShowWhatIsRunning(t *testing.T) {
	netToolsTestJobs(t)
	t.Setenv("JUKAHUB_SSH_KEY_DIR", t.TempDir())
	if err := ftpSrv.startAt(t.TempDir(), "127.0.0.1:0"); err != nil {
		t.Fatalf("start FTP: %v", err)
	}
	defer ftpSrv.Stop()

	tiles := netToolsBuildTiles(toolsHubNet{ip: "192.168.1.20", online: true}, "")
	ftp := tiles[toolsHubIndexOf(tiles, "ftp")]
	if !strings.HasPrefix(ftp.value, "On") || ftp.tone != toolsHubToneGood {
		t.Errorf("FTP tile = %+v, want it shown running", ftp)
	}
	if ssh := tiles[toolsHubIndexOf(tiles, "ssh")]; !strings.HasPrefix(ssh.value, "Off") {
		t.Errorf("SSH tile = %+v, want it shown stopped", ssh)
	}
	hub := toolsHubBuildTiles(&Config{}, toolsHubNet{ip: "192.168.1.20", online: true}, toolsHubSlowValues{}, "")
	if nt := hub[toolsHubIndexOf(hub, "nettools")]; nt.value != "FTP server is on" || nt.tone != toolsHubToneGood {
		t.Errorf("hub Network tools tile = %+v, want the running server named", nt)
	}
}

// TestNetToolsRunLandsInTheRegistry: a run's answer, its failure and a crash
// all end up as a result the tile and the sheet can show.
func TestNetToolsRunLandsInTheRegistry(t *testing.T) {
	toolsHubTestNav(t)
	netToolsTestJobs(t)
	toolsHubPinOnline(t, true)

	if !netToolsRun("publicip", "", func(context.Context) netToolsOutcome {
		return netToolsOutcome{text: "Public IP: 203.0.113.9", summary: "203.0.113.9 · AU"}
	}) {
		t.Fatal("the run did not start")
	}
	waitForSceneWork()
	res, ok := netToolsResultOf("publicip")
	if !ok || res.running || res.failed || res.summary != "203.0.113.9 · AU" || res.at.IsZero() {
		t.Fatalf("result = %+v, want the answer", res)
	}

	netToolsRun("whois", "example.com", func(context.Context) netToolsOutcome {
		return netToolsOutcome{err: errors.New("rdap: 404 not found")}
	})
	waitForSceneWork()
	res, _ = netToolsResultOf("whois")
	if !res.failed || res.tone != toolsHubToneWarn || !strings.HasPrefix(res.text, "Error: ") || res.summary == "" {
		t.Fatalf("failed result = %+v, want the error shown", res)
	}
	if res.target != "example.com" {
		t.Fatalf("target = %q, want what was typed", res.target)
	}

	netToolsRun("subnet", "", func(context.Context) netToolsOutcome { panic("boom") })
	waitForSceneWork()
	res, _ = netToolsResultOf("subnet")
	if !res.failed || !strings.Contains(res.text, "stopped unexpectedly") {
		t.Fatalf("crashed result = %+v, want the crash reported, not a network error", res)
	}

	// Offline, a tool that needs no network reports its own error, not
	// "connect to Wi-Fi".
	toolsHubPinOnline(t, false)
	netToolsRun("codec", "%%%", func(context.Context) netToolsOutcome {
		return netToolsOutcome{err: errors.New("nothing here decodes")}
	})
	waitForSceneWork()
	res, _ = netToolsResultOf("codec")
	if !strings.Contains(res.text, "nothing here decodes") || strings.Contains(res.text, "internet") {
		t.Fatalf("codec failure = %q, want its own reason", res.text)
	}
}

// TestNetToolsNewerRunWins: a run that a newer run of the same tool replaced
// cannot overwrite the newer answer when it finishes late.
func TestNetToolsNewerRunWins(t *testing.T) {
	toolsHubTestNav(t)
	netToolsTestJobs(t)

	release := make(chan struct{})
	netToolsRun("ping", "", func(context.Context) netToolsOutcome {
		<-release
		return netToolsOutcome{text: "old", summary: "old"}
	})
	netToolsRun("ping", "", func(context.Context) netToolsOutcome {
		return netToolsOutcome{text: "new", summary: "new"}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if res, _ := netToolsResultOf("ping"); res.text == "new" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the newer run never landed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	waitForSceneWork()
	if res, _ := netToolsResultOf("ping"); res.text != "new" || res.running {
		t.Fatalf("result = %+v, want the newer answer kept", res)
	}
}

// TestNetToolsLeavingDropsTheAnswer: a run still going when the page is left
// is cancelled, and its late answer does not land.
func TestNetToolsLeavingDropsTheAnswer(t *testing.T) {
	toolsHubTestNav(t)
	netToolsTestJobs(t)

	started := make(chan struct{})
	netToolsRun("traceroute", "", func(ctx context.Context) netToolsOutcome {
		close(started)
		<-ctx.Done()
		return netToolsOutcome{text: "too late", summary: "too late"}
	})
	<-started
	enterSceneWork("Main") // the page was left
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, _ := netToolsResultOf("traceroute")
		if !res.running {
			if res.text != "" || !res.at.IsZero() {
				t.Fatalf("a cancelled run landed: %+v", res)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the cancelled run never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestNetToolsEnterRunsWhatTheNetworkAllows: arriving on the page runs the
// automatic probes the network can serve, once; a fresh answer is not asked
// for again, and a run asked for from elsewhere starts with its sheet open.
func TestNetToolsEnterRunsWhatTheNetworkAllows(t *testing.T) {
	config := toolsHubTestNav(t)
	netToolsTestJobs(t)
	t.Setenv("JUKAHUB_GATEWAY", "192.168.1.1")
	canned := &netToolsCanned{}
	canned.install()

	toolsHubPinIP(t, "")
	toolsHubPinOnline(t, false)
	netToolsEnter(config)
	waitForSceneWork()
	if got := strings.Join(canned.askedFor(), ","); got != "wifisignal,listening" {
		t.Fatalf("offline arrival ran %q, want only the device's own probes", got)
	}

	toolsHubPinIP(t, "192.168.1.20")
	toolsHubPinOnline(t, true)
	canned.asked = nil
	netToolsEnter(config)
	waitForSceneWork()
	if got := strings.Join(canned.askedFor(), ","); got != "neighbours,publicip,clock,subnet" {
		t.Fatalf("online arrival ran %q, want the probes that were not fresh", got)
	}
	if netTools.gateway != "192.168.1.1" {
		t.Fatalf("gateway = %q, want the default route", netTools.gateway)
	}
	canned.asked = nil
	netToolsEnter(config)
	waitForSceneWork()
	if got := canned.askedFor(); len(got) != 0 {
		t.Fatalf("a second arrival ran %v again", got)
	}

	// The keyboard submitted a target after the page was left: it opens the
	// page, and the run starts there with its sheet.
	currentSceneIndex = 0
	netToolsSubmit("whois", "example.com")
	nt := sceneIndexExact(config, netToolsSceneName)
	if pendingSceneIndex != nt || netTools.pendingID != "whois" {
		t.Fatalf("submit off the page: pending scene %d, run %q", pendingSceneIndex, netTools.pendingID)
	}
	// Arrive the way completeSceneTransition does: a new task scope, then
	// the page's entry.
	currentSceneIndex, pendingSceneIndex, transitionPhase = nt, -1, "none"
	enterSceneWork(netToolsSceneName)
	netToolsEnter(config)
	waitForSceneWork()
	if !netTools.sheet.open || netTools.sheetID != "whois" {
		t.Fatalf("the pending run did not open its sheet (open %v, %q)", netTools.sheet.open, netTools.sheetID)
	}
	if res, _ := netToolsResultOf("whois"); res.target != "example.com" || !strings.Contains(res.text, "example.com") {
		t.Fatalf("whois result = %+v, want the submitted target", res)
	}
}

// TestNetToolsSheetFollowsTheResult: the sheet names what ran and when, offers
// the actions the tool has, and rebuilds when a new answer lands.
func TestNetToolsSheetFollowsTheResult(t *testing.T) {
	toolsHubTestNav(t)
	netToolsTestJobs(t)
	toolsHubPinIP(t, "192.168.1.20")

	pills := func() string {
		ids := make([]string, len(netTools.sheet.pills))
		for i, p := range netTools.sheet.pills {
			ids[i] = p.id
		}
		return strings.Join(ids, ",")
	}

	netToolsShowSheet("publicip")
	s := &netTools.sheet
	if !s.open || s.title != "Public IP" || !strings.Contains(s.subtitle, "Not run yet") {
		t.Fatalf("an unrun tool's sheet = %q / %q", s.title, s.subtitle)
	}
	if got := pills(); got != "run,close" {
		t.Fatalf("unrun pills = %s, want run,close", got)
	}
	if s.pills[0].label != "Run" || !strings.Contains(s.empty, "Public IP") {
		t.Fatalf("unrun sheet = %+v, want Run and the empty hint", s.pills[0])
	}

	netToolsSetResult("publicip", netToolsResult{text: "Public IP: 203.0.113.9\nCountry:   AU", summary: "203.0.113.9 · AU", at: time.Now()})
	netToolsSyncSheet()
	if got := pills(); got != "run,copy,close" {
		t.Fatalf("pills with a result = %s, want run,copy,close", got)
	}
	if s.pills[0].label != "Run again" || !strings.Contains(s.subtitle, "Updated") || s.text == "" {
		t.Fatalf("sheet with a result = %q, %q", s.pills[0].label, s.subtitle)
	}

	netToolsSetResult("publicip", netToolsResult{text: "Error: no route", failed: true, at: time.Now()})
	netToolsSyncSheet()
	if !strings.Contains(s.subtitle, "Failed") || s.subInk == (sdl.Color{}) {
		t.Fatalf("failed sheet subtitle = %q (ink %+v), want the failure called out", s.subtitle, s.subInk)
	}

	// A typed tool offers a new target; a server offers its switch.
	netToolsShowSheet("dns")
	if got := pills(); got != "run,edit,close" {
		t.Fatalf("DNS pills = %s, want run,edit,close", got)
	}
	netToolsShowSheet("ftp")
	if got := pills(); !strings.HasPrefix(got, "toggle") || !strings.HasSuffix(got, "close") {
		t.Fatalf("FTP pills = %s, want the switch first and close last", got)
	}
	if !strings.Contains(s.text, "Status:") || !strings.Contains(s.text, "Folder:") {
		t.Fatalf("FTP sheet text = %q, want where and how to connect", s.text)
	}

	netToolsCloseSheet()
	if s.open {
		t.Fatal("the sheet stayed open")
	}
}

// TestNetToolsBlockedTileExplainsItself: a dimmed tile says what it needs and
// does not run.
func TestNetToolsBlockedTileExplainsItself(t *testing.T) {
	config := toolsHubTestNav(t)
	netToolsTestJobs(t)
	clearToastAfterTest(t)
	canned := &netToolsCanned{}
	canned.install()
	currentSceneIndex = sceneIndexExact(config, netToolsSceneName)

	netTools.tiles = netToolsBuildTiles(toolsHubNet{}, "")
	netToolsActivate(config, toolsHubIndexOf(netTools.tiles, "ping"))
	if len(canned.askedFor()) != 0 {
		t.Fatalf("a dimmed tile ran %v", canned.askedFor())
	}
	if currentToast.Kind != ToastKindWarning || !strings.Contains(currentToast.Message, "Ping") {
		t.Fatalf("toast = %q, want why Ping cannot run", currentToast.Message)
	}

	// A tool that needs nothing runs offline and opens its sheet.
	netToolsActivate(config, toolsHubIndexOf(netTools.tiles, "listening"))
	waitForSceneWork()
	if !netTools.sheet.open || netTools.sheetID != "listening" {
		t.Fatalf("Listening ports did not open its sheet (%v, %q)", netTools.sheet.open, netTools.sheetID)
	}
}

// TestNetToolsKeysDriveThePageAndTheSheet: the page walks its grid; with the
// sheet open every key belongs to the sheet, and B closes it before leaving.
func TestNetToolsKeysDriveThePageAndTheSheet(t *testing.T) {
	config := toolsHubTestNav(t)
	netToolsTestJobs(t)
	toolsHubPinIP(t, "192.168.1.20")
	canned := &netToolsCanned{}
	canned.install()
	currentSceneIndex = sceneIndexExact(config, netToolsSceneName)
	sceneHistory = []int{0}

	netTools.gateway, netTools.gatewayIP = "192.168.1.1", "192.168.1.20"
	netTools.tiles = netToolsBuildTiles(toolsHubNet{ip: "192.168.1.20", online: true}, netTools.gateway)
	netTools.page = newToolsHubPage()
	netTools.page.grid = toolsHubLayoutGrid(netTools.tiles, toolsHubGridSpec{width: 1216, cols: 5, tileH: 120, gap: 14, headingH: 38, sectionGap: 6})
	netTools.sheet.open = false

	if !netToolsKey(pressKey(sdl.K_RIGHT), config) || netTools.tiles[netTools.page.cursor].id != "ssh" {
		t.Fatalf("Right focused %q, want SSH", netTools.tiles[netTools.page.cursor].id)
	}
	if !netToolsKey(pressKey(sdl.K_PAGEDOWN), config) || netTools.tiles[netTools.page.cursor].id != "gateway" {
		t.Fatalf("Page Down focused %q, want the Probes section", netTools.tiles[netTools.page.cursor].id)
	}
	// A on the gateway scan runs it against the router and shows it.
	if !netToolsKey(pressKey(sdl.K_RETURN), config) {
		t.Fatal("Enter was not taken")
	}
	waitForSceneWork()
	if !netTools.sheet.open || netTools.sheetID != "gateway" {
		t.Fatalf("the scan did not open its sheet (%v, %q)", netTools.sheet.open, netTools.sheetID)
	}
	if res, _ := netToolsResultOf("gateway"); res.target != "192.168.1.1" {
		t.Fatalf("scan target = %q, want the router", res.target)
	}

	// The sheet owns the keys: Right walks its pills, Escape closes it and
	// leaves the page where it was.
	before := netTools.sheet.focus
	netToolsKey(pressKey(sdl.K_RIGHT), config)
	if netTools.sheet.focus != before+1 || netTools.tiles[netTools.page.cursor].id != "gateway" {
		t.Fatalf("Right in the sheet moved pill %d→%d and the page to %q", before, netTools.sheet.focus, netTools.tiles[netTools.page.cursor].id)
	}
	if !netToolsKey(pressKey(sdl.K_ESCAPE), config) || netTools.sheet.open || transitionPhase != "none" {
		t.Fatalf("Escape in the sheet: open %v, phase %q, want only the sheet closed", netTools.sheet.open, transitionPhase)
	}
	// B on the page goes back.
	if !netToolsPad(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_B}, config) ||
		pendingSceneIndex != 0 || transitionPhase != "fade-out" {
		t.Fatalf("B on the page started phase %q to scene %d, want Home", transitionPhase, pendingSceneIndex)
	}
}

// TestNetToolsFooterNamesWhatAWillDo: the footer's A verb follows the focused
// tile's mode, and the sheet and the chip have their own rows.
func TestNetToolsFooterNamesWhatAWillDo(t *testing.T) {
	toolsHubTestNav(t)
	netToolsTestJobs(t)
	netTools.tiles = netToolsBuildTiles(toolsHubNet{ip: "192.168.1.20", online: true}, "")
	netTools.sheet.open = false
	netTools.page.chip = false
	base := func() []FooterHint {
		return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Open"}, {Button: "B", Label: "Back"}}
	}
	cases := map[string]string{"ping": "Run", "publicip": "Details", "dns": "Type target", "companion": "Open"}
	if !ftpSrv.Running() {
		cases["ftp"] = "Start"
	}
	for id, verb := range cases {
		netTools.page.cursor = toolsHubIndexOf(netTools.tiles, id)
		hints := netToolsFooterHints(base())
		if hints[1].Label != verb || hints[len(hints)-1].Button != "L1/R1" {
			t.Errorf("on %q the footer is %+v, want A %s and L1/R1", id, hints, verb)
		}
	}
	netTools.page.chip = true
	if hints := netToolsFooterHints(base()); hints[1].Label != "Copy IP" {
		t.Errorf("chip footer = %+v", hints)
	}
	netTools.page.chip = false
	netTools.sheet.open = true
	if hints := netToolsFooterHints(base()); len(hints) != 4 || hints[3].Label != "Close" {
		t.Errorf("sheet footer = %+v", hints)
	}
}

func TestNetToolsOutcomeSummaries(t *testing.T) {
	pings := []struct {
		name, text, want string
		tone             toolsHubTone
	}{
		{"linux ping", "64 bytes from 1.1.1.1: icmp_seq=1 ttl=57 time=12.0 ms\n64 bytes from 1.1.1.1: icmp_seq=2 ttl=57 time=14.0 ms\n",
			"13 ms average to 1.1.1.1", toolsHubToneNeutral},
		{"windows ping", "Reply from 1.1.1.1: bytes=32 time=20ms TTL=57\nReply from 1.1.1.1: bytes=32 time<1ms TTL=57\nReply from 1.1.1.1: bytes=32 time=12ms TTL=57\n",
			"11 ms average to 1.1.1.1", toolsHubToneNeutral},
		{"tcp fallback", "ICMP ping is unavailable here\n\ntry 1: 30 ms\ntry 2: timeout\ntry 3: 50 ms\n",
			"40 ms average to 1.1.1.1", toolsHubToneNeutral},
		{"no reply", "Request timed out.\n", "No reply from 1.1.1.1", toolsHubToneWarn},
	}
	for _, c := range pings {
		out := netToolsPingOutcome(c.text)
		if out.summary != c.want || out.tone != c.tone {
			t.Errorf("%s: summary %q tone %v, want %q %v", c.name, out.summary, out.tone, c.want, c.tone)
		}
	}

	signals := []struct{ text, want string }{
		{"Source: /proc/net/wireless\nLink quality: 60/70\nSignal: -52 dBm (good)", "-52 dBm · good"},
		{"Source: wpa_cli signal_poll\nSignal: -81 dBm (weak)", "-81 dBm · weak"},
		{"Name : Wi-Fi\nSignal : 87%", "87% signal"},
		{"No signal report: nothing answered.", "No signal report"},
	}
	for _, c := range signals {
		if got := netToolsSignalOutcome(c.text).summary; got != c.want {
			t.Errorf("signal %q summary = %q, want %q", c.text, got, c.want)
		}
	}
	if netToolsSignalOutcome("Signal: -85 dBm (weak)").tone != toolsHubToneWarn {
		t.Error("a weak signal is not called out")
	}

	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	clocks := []struct {
		offset time.Duration
		want   string
		tone   toolsHubTone
	}{
		{3 * time.Second, "Behind by 3.0 s", toolsHubToneWarn},
		{-2500 * time.Millisecond, "Ahead by 2.5 s", toolsHubToneWarn},
		{120 * time.Millisecond, "In sync (+120 ms)", toolsHubToneGood},
	}
	for _, c := range clocks {
		out := netToolsClockOutcome(clockReading{device: now, server: now.Add(c.offset), offset: c.offset, rtt: 30 * time.Millisecond, stratum: 2})
		if out.summary != c.want || out.tone != c.tone || out.text == "" {
			t.Errorf("offset %v: summary %q tone %v, want %q %v", c.offset, out.summary, out.tone, c.want, c.tone)
		}
	}
}

func TestNetToolsWordingHelpers(t *testing.T) {
	if got := netToolsShortError(offlinePlainNotice); got != "No connection" {
		t.Errorf("offline short error = %q", got)
	}
	if got := netToolsShortError("Error: connection refused"); got != "Connection refused" {
		t.Errorf("short error = %q", got)
	}
	if got := netToolsCount(1, "device", "devices"); got != "1 device" {
		t.Errorf("count 1 = %q", got)
	}
	if got := netToolsCount(3, "device", "devices"); got != "3 devices" {
		t.Errorf("count 3 = %q", got)
	}
	if got := netToolsClip("  a  b\tc ", 24); got != "a b c" {
		t.Errorf("clip folded spaces to %q", got)
	}
	long := strings.Repeat("é", 40)
	if got := netToolsClip(long, 24); len([]rune(got)) != 24 || !strings.HasSuffix(got, "…") {
		t.Errorf("clip of a long target = %q", got)
	}
	if got := netToolsBusyText("dns", "example.com"); !strings.Contains(got, "example.com") {
		t.Errorf("DNS busy line = %q, want the name", got)
	}
	if got := netToolsBusyText("nope", ""); got != "Working…" {
		t.Errorf("unknown busy line = %q", got)
	}
	for _, c := range []struct {
		ago  time.Duration
		want string
	}{{time.Second, "just now"}, {20 * time.Second, "20 s ago"}, {3 * time.Minute, "3 min ago"}} {
		if got := netToolsAge(time.Now().Add(-c.ago)); got != c.want {
			t.Errorf("age of %v = %q, want %q", c.ago, got, c.want)
		}
	}
}
