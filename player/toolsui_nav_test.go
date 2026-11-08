package main

// The Tools hub's logic: the in-memory scenes that replaced the classic "Misc"
// page and the Tools overlay, the tile list and its sections, the offline rule
// that dims a tile instead of letting a press fail, and the sectioned grid the
// D-pad walks. Nothing here draws; toolsui_render_test.go covers the pixels.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// toolsHubTestNav isolates the scene-navigation globals a Tools page test
// touches and returns a config with Home at index 0 and the injected pages,
// Home on screen and no transition running.
func toolsHubTestNav(t *testing.T) *Config {
	t.Helper()
	// A Wi-Fi worker an earlier test left running must not land in this one.
	wifiSettleForTest()
	wifiWorkPending = false
	prevConfig := appConfig
	prevScene, prevPending, prevPhase := currentSceneIndex, pendingSceneIndex, transitionPhase
	prevHistory := append([]int(nil), sceneHistory...)
	prevQS, prevProcess := qs.open, processPanel.open
	prevHub, prevNet, prevWiFi := toolsHub, netTools, wifiScr
	prevPanel := wifiPanel
	t.Cleanup(func() {
		wifiSettleForTest()
		appConfig = prevConfig
		currentSceneIndex, pendingSceneIndex, transitionPhase = prevScene, prevPending, prevPhase
		sceneHistory = prevHistory
		qs.open, processPanel.open = prevQS, prevProcess
		toolsHub, netTools, wifiScr = prevHub, prevNet, prevWiFi
		wifiPanel = prevPanel
		// Work the test started belongs to its own scope; the next test starts
		// with a live one.
		enterSceneWork("")
	})
	config := &Config{Scenes: []SceneConfig{{Name: "Main", Layout: "home"}}}
	ensureToolsHubScene(config)
	appConfig = config
	currentSceneIndex, pendingSceneIndex, transitionPhase = 0, -1, "none"
	sceneHistory = nil
	enterSceneWork("Main")
	return config
}

// toolsHubPinIP makes toolsIPAddress answer ip for the test ("" is no Wi-Fi).
func toolsHubPinIP(t *testing.T, ip string) {
	t.Helper()
	toolsIPCache.mu.Lock()
	prevIP, prevAt := toolsIPCache.ip, toolsIPCache.at
	toolsIPCache.ip, toolsIPCache.at = ip, time.Now().Add(time.Hour)
	toolsIPCache.mu.Unlock()
	t.Cleanup(func() {
		toolsIPCache.mu.Lock()
		toolsIPCache.ip, toolsIPCache.at = prevIP, prevAt
		toolsIPCache.mu.Unlock()
	})
}

// toolsHubPinOnline forces the connectivity monitor's answer for the test.
func toolsHubPinOnline(t *testing.T, online bool) {
	t.Helper()
	netMon.mu.Lock()
	prevOnline, prevChecked, prevReason := netMon.online, netMon.checked, netMon.reason
	netMon.online, netMon.checked, netMon.reason = online, true, "test"
	netMon.mu.Unlock()
	t.Cleanup(func() {
		netMon.mu.Lock()
		netMon.online, netMon.checked, netMon.reason = prevOnline, prevChecked, prevReason
		netMon.mu.Unlock()
	})
}

// toolsHubAllScenes is a config carrying every screen a hub tile can open.
func toolsHubAllScenes() *Config {
	config := &Config{}
	for _, name := range []string{"Main", "NetSpeed", "Terminal", "Cron", "Hardware", "DiskSpace",
		"Benchmark", "LogExporter", "Calculator", playtimeSceneName, "Patch"} {
		config.Scenes = append(config.Scenes, SceneConfig{Name: name})
	}
	return config
}

// TestEnsureToolsHubSceneRetiresMisc: an older config on the card still opens
// one Tools screen. Buttons that pointed at the classic page (or at the old
// overlay trigger) open the hub, the page itself is dropped, the three pages
// exist once each, and a running twice changes nothing.
func TestEnsureToolsHubSceneRetiresMisc(t *testing.T) {
	config := &Config{Scenes: []SceneConfig{
		{Name: "Main", Layout: "home", Elements: []Element{
			{Type: "button", Text: "Tools", Trigger: "change_scene:Misc"},
			{Type: "button", Text: "More", Trigger: "change_scene", TriggerTarget: "Misc"},
			{Type: "button", Text: "FTP & IP", Trigger: "tools_open"},
			{Type: "button", Text: "Games", Trigger: "change_scene:Games"},
			// The web builder's spelling, as the loader leaves it.
			{Type: "button", Text: "Built", Trigger: "change_scene", TriggerTarget: "Misc", SceneChange: "Misc"},
		}},
		{Name: "Misc", Elements: []Element{{Type: "button", Text: "Terminal", Trigger: "change_scene:Terminal"}}},
		{Name: "Games"},
		// A page another feature injected a button into keeps none: the page
		// draws itself, and a config button would win its keys.
		{Name: toolsHubSceneName, Layout: "home", Elements: []Element{{Type: "button", Text: "Stray", Trigger: "change_scene:Games"}}},
		// A config that already has its own Calculator keeps it as written.
		{Name: "Calculator", Elements: []Element{{Type: "label", Text: "kept"}}},
	}}
	ensureToolsHubScene(config)
	scenes := len(config.Scenes)
	ensureToolsHubScene(config)
	if len(config.Scenes) != scenes {
		t.Fatalf("a second pass changed the scene count from %d to %d", scenes, len(config.Scenes))
	}

	if sceneIndexExact(config, toolsHubLegacyScene) >= 0 {
		t.Fatal("the classic Misc page survived")
	}
	for _, name := range []string{toolsHubSceneName, netToolsSceneName, wifiScrSceneName, "Calculator", "Games"} {
		count := 0
		for _, s := range config.Scenes {
			if s.Name == name {
				count++
			}
		}
		if count != 1 {
			t.Errorf("scene %q appears %d times, want once", name, count)
		}
	}

	home := config.Scenes[0].Elements
	cases := []struct {
		elem          Element
		trigger, dest string
	}{
		{home[0], "change_scene:" + toolsHubSceneName, toolsHubSceneName},
		{home[1], "change_scene", toolsHubSceneName},
		{home[2], "change_scene:" + toolsHubSceneName, toolsHubSceneName},
		{home[3], "change_scene:Games", ""},
		{home[4], "change_scene", toolsHubSceneName},
	}
	for _, c := range cases {
		if c.elem.Trigger != c.trigger || c.elem.TriggerTarget != c.dest {
			t.Errorf("%q now triggers %q → %q, want %q → %q", c.elem.Text, c.elem.Trigger, c.elem.TriggerTarget, c.trigger, c.dest)
		}
	}
	if home[4].SceneChange != toolsHubSceneName {
		t.Errorf("the builder's scene field still names %q", home[4].SceneChange)
	}

	for _, name := range []string{toolsHubSceneName, netToolsSceneName, wifiScrSceneName} {
		page := config.Scenes[sceneIndexExact(config, name)]
		if len(page.Elements) != 0 || page.Layout != "" {
			t.Errorf("page %q carries %d config elements and layout %q, want none", name, len(page.Elements), page.Layout)
		}
	}
	if calc := config.Scenes[sceneIndexExact(config, "Calculator")]; len(calc.Elements) != 1 {
		t.Errorf("the config's own Calculator lost its content: %+v", calc)
	}
	// A nil config is a no-op, not a panic.
	ensureToolsHubScene(nil)
	toolsHubEnsureScenes(nil)
}

// TestShippedConfigsHaveNoMiscPage: the classic page and the overlay trigger
// are gone from both shipped configs. Home's Tools tile carries no trigger, like
// the Browser's: the home card opens the hub itself (homeActivate), and the
// hub is a scene only the app injects.
func TestShippedConfigsHaveNoMiscPage(t *testing.T) {
	for _, path := range []string{"jukaconfig.json", filepath.Join("trimui", "jukaconfig.device.json")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(data)
		for _, gone := range []string{`"name": "Misc"`, "change_scene:Misc", `"tools_open"`} {
			if strings.Contains(text, gone) {
				t.Errorf("%s still contains %s", path, gone)
			}
		}
	}
	// Whatever the tile says, Home resolves the card to the hub.
	config := &Config{Scenes: []SceneConfig{{Name: "Main", Layout: "home", Elements: []Element{
		{Type: "button", Style: "tile", Text: "Tools"},
	}}}}
	prev := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prev })
	currentSceneIndex = 0
	if got := homeSceneTarget(config, HomeFocusTools); got != toolsHubSceneName {
		t.Errorf("Home's Tools card resolves to %q, want the hub", got)
	}
}

// TestToolsHubTileOrderAndSections pins the page the user asked for: Watch
// Party first and wide, then the toolkits, connectivity, the system tools and
// updates & support, every section opened by its first tile. Nothing of the
// classic page or of the overlay's network rows is a hub tile any more.
func TestToolsHubTileOrderAndSections(t *testing.T) {
	tiles := toolsHubBuildTiles(toolsHubAllScenes(), toolsHubNet{ip: "192.168.1.20", online: true}, toolsHubSlowValues{}, "HomeNet")
	want := []string{
		"party", "localplay", "nearby", "ssync",
		"toolbox", "audit", "sectrust",
		"nettools", "wifi", "netspeed",
		"terminal", "sched", "hardware", "disk", "benchmark", "logs", "cache", "powertools", "disc", "rommgr", "calculator", "trophies",
		"xpack", "cfgdoc", "bkup",
		"updates", "bug", "community",
	}
	if len(tiles) != len(want) {
		ids := make([]string, len(tiles))
		for i := range tiles {
			ids[i] = tiles[i].id
		}
		t.Fatalf("tiles = %v, want %v", ids, want)
	}
	sections := map[string]string{
		"party": "Together", "toolbox": "Toolkits", "nettools": "Connectivity",
		"terminal": "System", "xpack": "Your setup", "updates": "Updates & support",
	}
	for i, tile := range tiles {
		if tile.id != want[i] {
			t.Errorf("tile %d is %q, want %q", i, tile.id, want[i])
		}
		if got := sections[tile.id]; tile.section != got {
			t.Errorf("tile %q opens section %q, want %q", tile.id, tile.section, got)
		}
		if tile.section != "" && tile.sectionIcon == "" {
			t.Errorf("section %q has no glyph", tile.section)
		}
		if strings.TrimSpace(tile.label) == "" || tile.icon == "" {
			t.Errorf("tile %q has no label or icon: %+v", tile.id, tile)
		}
		if tile.blocked != "" {
			t.Errorf("tile %q is dimmed (%q) on a working connection", tile.id, tile.blocked)
		}
	}
	if !tiles[0].wide {
		t.Error("Watch Party is not the wide tile at the top of the page")
	}
	for i := 1; i < len(tiles); i++ {
		if tiles[i].wide {
			t.Errorf("tile %q is wide; only Watch Party spans the row", tiles[i].id)
		}
	}
	if w := tiles[toolsHubIndexOf(tiles, "wifi")]; w.value != "HomeNet" || w.tone != toolsHubToneGood {
		t.Errorf("Wi-Fi tile = %+v, want the network it is on", w)
	}
	for _, gone := range []string{"misc", "classic", "utilities", "ftp", "ssh", "copyip", "banner", "httpcheck", "cert", "headers"} {
		if toolsHubIndexOf(tiles, gone) >= 0 {
			t.Errorf("the hub still has a %q tile", gone)
		}
	}
}

// TestToolsHubNeedsDimTiles: a tile the network cannot serve is dimmed with a
// tag that says what it needs; everything that runs on the device stays live.
func TestToolsHubNeedsDimTiles(t *testing.T) {
	config := toolsHubAllScenes()
	cases := []struct {
		name         string
		net          toolsHubNet
		party, speed string
	}{
		{"no Wi-Fi", toolsHubNet{}, toolsHubTagWiFi, toolsHubTagWiFi},
		{"Wi-Fi without internet", toolsHubNet{ip: "192.168.1.20"}, "", toolsHubTagInternet},
		{"online", toolsHubNet{ip: "192.168.1.20", online: true}, "", ""},
	}
	local := []string{"toolbox", "audit", "sectrust", "nettools", "wifi", "terminal", "sched", "hardware",
		"disk", "benchmark", "logs", "cache", "powertools", "disc", "rommgr", "calculator", "trophies", "updates", "bug", "community"}
	for _, c := range cases {
		tiles := toolsHubBuildTiles(config, c.net, toolsHubSlowValues{}, "")
		if got := tiles[toolsHubIndexOf(tiles, "party")].blocked; got != c.party {
			t.Errorf("%s: Watch Party tag = %q, want %q", c.name, got, c.party)
		}
		if got := tiles[toolsHubIndexOf(tiles, "netspeed")].blocked; got != c.speed {
			t.Errorf("%s: Net speed tag = %q, want %q", c.name, got, c.speed)
		}
		for _, id := range local {
			if i := toolsHubIndexOf(tiles, id); i < 0 || tiles[i].blocked != "" {
				t.Errorf("%s: %q is missing or dimmed; it does not need the network", c.name, id)
			}
		}
		if c.net.ip == "" && tiles[toolsHubIndexOf(tiles, "wifi")].icon != "wifi-off" {
			t.Errorf("%s: the Wi-Fi tile does not show the disconnected glyph", c.name)
		}
	}

	if msg := toolsHubBlockedMessage("Watch Party", toolsHubTagWiFi); !strings.Contains(msg, "Connect to Wi-Fi") || !strings.Contains(msg, "Watch Party") {
		t.Errorf("Wi-Fi message = %q", msg)
	}
	if msg := toolsHubBlockedMessage("Net speed", toolsHubTagInternet); !strings.Contains(msg, "needs the internet") {
		t.Errorf("internet message = %q", msg)
	}
	if tag := toolsHubBlockedTag(toolsHubNeedNothing, toolsHubNet{}); tag != "" {
		t.Errorf("a tool that needs nothing was tagged %q offline", tag)
	}
}

// TestToolsHubBlockedTileStaysOnHub: pressing a dimmed tile explains itself
// and never navigates, and a tile whose screen is missing says so instead of
// falling back to Home.
func TestToolsHubBlockedTileStaysOnHub(t *testing.T) {
	config := toolsHubTestNav(t)
	clearToastAfterTest(t)
	currentSceneIndex = sceneIndexExact(config, toolsHubSceneName)
	toolsHub.tiles = toolsHubBuildTiles(config, toolsHubNet{}, toolsHubSlowValues{}, "")

	toolsHubActivate(config, toolsHubIndexOf(toolsHub.tiles, "party"))
	if transitionPhase != "none" || pendingSceneIndex != -1 {
		t.Fatalf("a dimmed tile navigated: phase %q, pending %d", transitionPhase, pendingSceneIndex)
	}
	if currentToast.Kind != ToastKindWarning || !strings.Contains(currentToast.Message, "Connect to Wi-Fi") {
		t.Fatalf("toast = %q (kind %v), want the Wi-Fi explanation", currentToast.Message, currentToast.Kind)
	}

	toolsHubOpenScene(config, "NoSuchScreen")
	if transitionPhase != "none" || pendingSceneIndex != -1 {
		t.Fatalf("a missing screen navigated: phase %q, pending %d", transitionPhase, pendingSceneIndex)
	}
	if !strings.Contains(currentToast.Message, "not part of this build") {
		t.Fatalf("toast = %q, want the missing screen named", currentToast.Message)
	}

	// Out-of-range presses are ignored rather than panicking.
	toolsHubActivate(config, -1)
	toolsHubActivate(config, len(toolsHub.tiles))
}

// TestToolsPagesOpenFromEverywhere: every entry point lands on its page as a
// scene, the panel it was opened from closes, and the overlay's old names
// still lead to the new pages.
func TestToolsPagesOpenFromEverywhere(t *testing.T) {
	config := toolsHubTestNav(t)
	hub := sceneIndexExact(config, toolsHubSceneName)

	qs.open = true
	toolsHubOpen(config)
	if pendingSceneIndex != hub || transitionPhase != "fade-out" {
		t.Fatalf("toolsHubOpen started phase %q to scene %d, want the hub (%d)", transitionPhase, pendingSceneIndex, hub)
	}
	if qs.open {
		t.Error("Quick Settings stayed open over the hub")
	}

	cases := []struct {
		name string
		open func()
		want string
	}{
		{"openToolsPanel", openToolsPanel, toolsHubSceneName},
		{"openWiFiPanel", openWiFiPanel, wifiScrSceneName},
		{"netToolsOpen", func() { netToolsOpen(config) }, netToolsSceneName},
		{"wifiScrOpen", func() { wifiScrOpen(config) }, wifiScrSceneName},
	}
	for _, c := range cases {
		currentSceneIndex, pendingSceneIndex, transitionPhase = 0, -1, "none"
		c.open()
		if want := sceneIndexExact(config, c.want); want < 0 || pendingSceneIndex != want || transitionPhase != "fade-out" {
			t.Errorf("%s started phase %q to scene %d, want %s (%d)", c.name, transitionPhase, pendingSceneIndex, c.want, want)
		}
	}
	if !wifiScr.fresh {
		t.Error("opening the Wi-Fi page did not ask for a fresh visit")
	}

	// Already on the hub, opening it again refreshes in place at the top.
	// (The Wi-Fi name was read just now, so no background read starts.)
	toolsHubWiFiName.mu.Lock()
	toolsHubWiFiName.at = time.Now()
	toolsHubWiFiName.mu.Unlock()
	toolsHub.slow.at = time.Now()
	currentSceneIndex, pendingSceneIndex, transitionPhase = hub, -1, "none"
	toolsHub.page.cursor = 5
	toolsHubOpen(config)
	if transitionPhase != "none" {
		t.Errorf("reopening the hub started a transition (%q)", transitionPhase)
	}
	if toolsHub.page.cursor != 0 || len(toolsHub.tiles) == 0 {
		t.Errorf("reopening the hub left cursor %d with %d tiles, want the top of a built page", toolsHub.page.cursor, len(toolsHub.tiles))
	}

	// A nil config is a no-op, not a panic.
	toolsHubOpen(nil)
	netToolsOpen(nil)
	wifiScrOpen(nil)
}

// TestToolsScenesCurrentNamesOnlyTheToolsPages: the input and click hooks in
// main.go answer only on the three pages.
func TestToolsScenesCurrentNamesOnlyTheToolsPages(t *testing.T) {
	config := toolsHubTestNav(t)
	if got := toolsScenesCurrent(config); got != "" {
		t.Fatalf("Home reported as tools page %q", got)
	}
	for _, name := range []string{toolsHubSceneName, netToolsSceneName, wifiScrSceneName} {
		currentSceneIndex = sceneIndexExact(config, name)
		if got := toolsScenesCurrent(config); got != name {
			t.Errorf("on %q toolsScenesCurrent = %q", name, got)
		}
	}
	currentSceneIndex = 0
	if toolsScenesClick(10, 10, config) || toolsScenesWheel(config, 1) {
		t.Error("a click or the wheel on Home was taken by a tools page")
	}
	if toolsScenesHandleKey(pressKey(sdl.K_DOWN), config) {
		t.Error("a key on Home was taken by a tools page")
	}
	if toolsScenesCurrent(nil) != "" {
		t.Error("a nil config named a tools page")
	}
}

// TestToolsHubTourWaitsForTheReturn: a tour row that opens a page hides the
// tour until the user is back on the screen it was drawn over.
func TestToolsHubTourWaitsForTheReturn(t *testing.T) {
	config := toolsHubTestNav(t)
	prevTour := toolsHubTourScene
	t.Cleanup(func() { toolsHubTourScene = prevTour })

	toolsHubTourReset()
	if toolsHubTourAway() {
		t.Fatal("a reset tour reports the user away")
	}
	toolsHubTourMark()
	if toolsHubTourScene != "Main" || toolsHubTourAway() {
		t.Fatalf("mark on Home = %q, away %v", toolsHubTourScene, toolsHubTourAway())
	}
	currentSceneIndex = sceneIndexExact(config, wifiScrSceneName)
	if !toolsHubTourAway() {
		t.Fatal("on the Wi-Fi page the tour still shows")
	}
	currentSceneIndex = 0
	if toolsHubTourAway() {
		t.Fatal("back on Home the tour stays hidden")
	}
}

// TestToolsHubCopyIPChip: the chip copies the address with a toast, and with
// no address it explains how to get one instead of copying nothing.
func TestToolsHubCopyIPChip(t *testing.T) {
	clearToastAfterTest(t)
	var spy clipboardSpy
	installClipboardSpy(t, &spy)
	resetClipboardHistory(t)

	toolsHubPinIP(t, "192.168.1.20")
	toolsHubCopyIP()
	if spy.set != "192.168.1.20" {
		t.Fatalf("clipboard = %q, want the address", spy.set)
	}
	if currentToast.Message != "IP copied" || currentToast.Kind != ToastKindSuccess {
		t.Fatalf("toast = %q (kind %v), want \"IP copied\"", currentToast.Message, currentToast.Kind)
	}

	toolsHubPinIP(t, "")
	spy.set = ""
	toolsHubCopyIP()
	if spy.set != "" {
		t.Fatalf("copied %q with no address", spy.set)
	}
	if currentToast.Kind != ToastKindWarning || !strings.Contains(currentToast.Message, "Wi-Fi") {
		t.Fatalf("toast = %q (kind %v), want the connect hint", currentToast.Message, currentToast.Kind)
	}
	if toolsHubChipLabel("") != "No Wi-Fi" || toolsHubChipLabel("10.0.0.2") != "10.0.0.2" {
		t.Error("the chip label does not follow the address")
	}
}

// toolsHubTestTiles is a page with a hero, then sections of three, five and
// one tile.
func toolsHubTestTiles() []toolsHubTile {
	return []toolsHubTile{
		{id: "hero", section: "Together", wide: true},
		{id: "a0", section: "A"}, {id: "a1"}, {id: "a2"},
		{id: "b0", section: "B"}, {id: "b1"}, {id: "b2"}, {id: "b3"}, {id: "b4"},
		{id: "c0", section: "C"},
	}
}

// toolsHubTestSpec is a 400px body with three 126px columns.
var toolsHubTestSpec = toolsHubGridSpec{width: 400, cols: 3, tileH: 100, gap: 10, headingH: 30, sectionGap: 6}

func TestToolsHubLayoutGridSections(t *testing.T) {
	g := toolsHubLayoutGrid(toolsHubTestTiles(), toolsHubTestSpec)
	wantRows := [][]int{{0}, {1, 2, 3}, {4, 5, 6}, {7, 8}, {9}}
	if len(g.rows) != len(wantRows) {
		t.Fatalf("rows = %+v, want %v", g.rows, wantRows)
	}
	for r, want := range wantRows {
		if len(g.rows[r].tiles) != len(want) {
			t.Fatalf("row %d = %v, want %v", r, g.rows[r].tiles, want)
		}
		for k := range want {
			if g.rows[r].tiles[k] != want[k] {
				t.Fatalf("row %d = %v, want %v", r, g.rows[r].tiles, want)
			}
		}
	}
	// A section's first row sits under its heading; a wrapped row does not.
	for r, headed := range []bool{true, true, true, false, true} {
		if g.rows[r].headed != headed {
			t.Errorf("row %d headed = %v, want %v", r, g.rows[r].headed, headed)
		}
	}
	if len(g.headings) != 4 {
		t.Fatalf("headings = %d, want one per section", len(g.headings))
	}
	for i, want := range []int32{0, 146, 292, 548} {
		if g.headings[i].y != want {
			t.Errorf("heading %d at y=%d, want %d", i, g.headings[i].y, want)
		}
	}
	if g.height != 678 {
		t.Errorf("grid height = %d, want 678", g.height)
	}
	hero := g.rects[0]
	if hero.X != 0 || hero.W != 400 || hero.Y != 30 {
		t.Errorf("hero = %+v, want the full row under its heading", hero)
	}
	for i := 1; i < len(g.rects); i++ {
		r := g.rects[i]
		if r.W != 126 || r.H != 100 || r.X < 0 || r.X+r.W > 400 {
			t.Errorf("tile %d = %+v, want a 126x100 column inside the body", i, r)
		}
	}
	if g.rects[1].X != g.rects[4].X || g.rects[3].X != g.rects[6].X {
		t.Error("the columns of two sections do not line up")
	}
}

func TestToolsHubGridMoveStaysOnTheGrid(t *testing.T) {
	g := toolsHubLayoutGrid(toolsHubTestTiles(), toolsHubTestSpec)
	center := func(i int) int32 { return g.rects[i].X + g.rects[i].W/2 }
	cases := []struct {
		why         string
		cur, dx, dy int
		wantX       int32
		want        int
		ok          bool
	}{
		{"right inside the row", 1, 1, 0, -1, 2, true},
		{"right at the row's end stays put", 3, 1, 0, -1, 3, false},
		{"left at the row's start stays put", 1, -1, 0, -1, 1, false},
		{"down from the hero's left edge", 0, 0, 1, 0, 1, true},
		{"down keeps the column", 3, 0, 1, center(3), 6, true},
		{"down into a shorter row takes the nearest", 6, 0, 1, center(6), 8, true},
		{"down crosses a heading", 8, 0, 1, center(8), 9, true},
		{"down at the last row stays put", 9, 0, 1, center(9), 9, false},
		{"up at the first row stays put", 0, 0, -1, center(0), 0, false},
		{"up keeps the column", 7, 0, -1, center(7), 4, true},
		{"an off-grid cursor stays put", 42, 1, 0, -1, 42, false},
	}
	for _, c := range cases {
		got, ok := toolsHubGridMove(g, c.cur, c.dx, c.dy, c.wantX)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: move(%d, %d, %d) = %d/%v, want %d/%v", c.why, c.cur, c.dx, c.dy, got, ok, c.want, c.ok)
		}
	}
}

func TestToolsHubSectionStep(t *testing.T) {
	tiles := toolsHubTestTiles()
	cases := []struct {
		cur, dir, want int
	}{
		{0, 1, 1}, {1, 1, 4}, {5, 1, 9}, {9, 1, 9},
		{5, -1, 4}, {4, -1, 1}, {2, -1, 1}, {1, -1, 0}, {0, -1, 0},
		{-3, 1, 1}, {99, -1, 4},
	}
	for _, c := range cases {
		if got := toolsHubSectionStep(tiles, c.cur, c.dir); got != c.want {
			t.Errorf("section step from %d by %d = %d, want %d", c.cur, c.dir, got, c.want)
		}
	}
	if got := toolsHubSectionStep(nil, 3, 1); got != 3 {
		t.Errorf("an empty page moved the cursor to %d", got)
	}
}

func TestToolsHubScrollKeepsTheFocusInView(t *testing.T) {
	g := toolsHubLayoutGrid(toolsHubTestTiles(), toolsHubTestSpec)
	const viewH, pad = 300, 8
	cases := []struct {
		why    string
		idx    int
		scroll int32
		want   int32
	}{
		{"the last tile scrolls to the end", 9, 0, 378},
		{"the first row shows the page top", 0, 200, 0},
		{"a tile in view leaves the offset alone", 7, 378, 378},
		{"a section's first row brings its heading", 1, 378, 146},
	}
	for _, c := range cases {
		if got := toolsHubScrollFor(g, c.idx, toolsHubTestSpec.headingH, pad, viewH, c.scroll); got != c.want {
			t.Errorf("%s: scroll = %d, want %d", c.why, got, c.want)
		}
	}
	for _, c := range []struct{ scroll, content, view, want int32 }{
		{-5, 678, 300, 0}, {1000, 678, 300, 378}, {50, 100, 300, 0}, {120, 678, 300, 120},
	} {
		if got := toolsHubClampScroll(c.scroll, c.content, c.view); got != c.want {
			t.Errorf("clamp(%d, %d, %d) = %d, want %d", c.scroll, c.content, c.view, got, c.want)
		}
	}
}

// TestToolsHubPageMoveReachesTheChip: Up from the first row lands on the copy
// IP chip, Up from the chip is left for the top bar, Left/Right on the chip go
// nowhere, and Down comes back to the tile under it.
func TestToolsHubPageMoveReachesTheChip(t *testing.T) {
	tiles := toolsHubTestTiles()[1:] // no hero: the first row has three tiles
	p := newToolsHubPage()
	p.grid = toolsHubLayoutGrid(tiles, toolsHubTestSpec)
	p.view = sdl.Rect{X: 40, Y: 120, W: 400, H: 300}
	p.chipRect = sdl.Rect{X: 340, Y: 60, W: 100, H: 40} // over the third column

	p.cursor = 1
	if !p.move(tiles, 0, -1) || !p.chip {
		t.Fatal("Up from the first row did not reach the chip")
	}
	if p.move(tiles, 0, -1) || !p.chip {
		t.Fatal("Up from the chip was swallowed; it belongs to the top bar")
	}
	if !p.move(tiles, 1, 0) || !p.chip {
		t.Fatal("Right on the chip left it or fell through")
	}
	if !p.move(tiles, 0, 1) || p.chip || p.cursor != 2 {
		t.Fatalf("Down from the chip landed on %d (chip %v), want the tile under it (2)", p.cursor, p.chip)
	}

	// Without a chip, Up from the first row is the top bar's.
	q := newToolsHubPage()
	q.grid = p.grid
	if q.move(tiles, 0, -1) {
		t.Fatal("Up from the first row of a page with no chip was swallowed")
	}
	// The other edges swallow the press so the focus never jumps away.
	q.cursor = 8
	if !q.move(tiles, 0, 1) || q.cursor != 8 {
		t.Fatalf("Down at the bottom moved to %d or fell through", q.cursor)
	}

	// L1/R1 jump by section and leave the chip.
	p.chip = true
	p.jumpSection(tiles, 1)
	if p.chip || p.cursor != 3 {
		t.Fatalf("R1 from the chip went to %d (chip %v), want the next section (3)", p.cursor, p.chip)
	}
}

func TestToolsHubPageWheelAndHit(t *testing.T) {
	p := newToolsHubPage()
	p.grid = toolsHubLayoutGrid(toolsHubTestTiles(), toolsHubTestSpec)
	if p.wheel(-1) {
		t.Fatal("the wheel scrolled a page that was never laid out")
	}
	p.view = sdl.Rect{X: 0, Y: 100, W: 400, H: 300}
	p.headingH = 30
	if !p.wheel(-1) || p.target != 60 || p.follow {
		t.Fatalf("wheel down: target %d, follow %v, want 60 and the focus left alone", p.target, p.follow)
	}
	for i := 0; i < 20; i++ {
		p.wheel(-1)
	}
	if p.target != 378 {
		t.Fatalf("the wheel scrolled past the end: %d", p.target)
	}
	if p.wheel(0) {
		t.Fatal("a zero wheel event was taken")
	}

	p.tileRects = []sdl.Rect{{X: 0, Y: 0, W: 10, H: 10}, {}, {X: 20, Y: 0, W: 10, H: 10}}
	if got := p.hitTile(25, 5); got != 2 {
		t.Fatalf("hit = %d, want 2", got)
	}
	if got := p.hitTile(15, 5); got != -1 {
		t.Fatalf("a gap hit tile %d", got)
	}
}

// TestToolsHubKeysWalkAndLeave drives the hub with the keyboard: the D-pad
// keys walk the drawn grid, Page Down jumps a section, and Escape goes back.
func TestToolsHubKeysWalkAndLeave(t *testing.T) {
	config := toolsHubTestNav(t)
	currentSceneIndex = sceneIndexExact(config, toolsHubSceneName)
	sceneHistory = []int{0}
	toolsHub.tiles = toolsHubBuildTiles(config, toolsHubNet{ip: "192.168.1.20", online: true}, toolsHubSlowValues{}, "")
	toolsHub.page = newToolsHubPage()
	toolsHub.page.grid = toolsHubLayoutGrid(toolsHub.tiles, toolsHubGridSpec{width: 1216, cols: 4, tileH: 120, gap: 14, headingH: 38, sectionGap: 6})

	focused := func() string { return toolsHub.tiles[toolsHub.page.cursor].id }
	if !toolsHubKey(pressKey(sdl.K_DOWN), config) || focused() != "localplay" {
		t.Fatalf("Down from Watch Party focused %q, want localplay", focused())
	}
	if !toolsHubKey(pressKey(sdl.K_DOWN), config) || focused() != "toolbox" {
		t.Fatalf("Down from Local play focused %q, want toolbox", focused())
	}
	if !toolsHubKey(pressKey(sdl.K_RIGHT), config) || focused() != "audit" {
		t.Fatalf("Right focused %q, want audit", focused())
	}
	if !toolsHubKey(pressKey(sdl.K_PAGEDOWN), config) || focused() != "nettools" {
		t.Fatalf("Page Down focused %q, want the Connectivity section", focused())
	}
	if !toolsHubKey(pressKey(sdl.K_PAGEUP), config) || focused() != "toolbox" {
		t.Fatalf("Page Up focused %q, want the Toolkits section", focused())
	}
	if toolsHubKey(pressKey(sdl.K_F5), config) {
		t.Fatal("an unbound key was taken")
	}
	if !toolsHubKey(pressKey(sdl.K_ESCAPE), config) || pendingSceneIndex != 0 || transitionPhase != "fade-out" {
		t.Fatalf("Escape started phase %q to scene %d, want Home", transitionPhase, pendingSceneIndex)
	}

	// The pad has the same bindings.
	transitionPhase, pendingSceneIndex = "none", -1
	sceneHistory = []int{0}
	toolsHub.page.cursor = 0
	if !toolsHubPad(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_DOWN}, config) || focused() != "localplay" {
		t.Fatalf("D-pad Down focused %q, want localplay", focused())
	}
	if !toolsHubPad(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_DOWN}, config) || focused() != "toolbox" {
		t.Fatalf("second D-pad Down focused %q, want toolbox", focused())
	}
	if !toolsHubPad(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_RIGHTSHOULDER}, config) || focused() != "nettools" {
		t.Fatalf("R1 focused %q, want the Connectivity section", focused())
	}
	if !toolsHubPad(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_B}, config) || pendingSceneIndex != 0 {
		t.Fatalf("B started phase %q to scene %d, want Home", transitionPhase, pendingSceneIndex)
	}
}

// TestToolsScenesFooterHintsFollowTheFocus: the hub's footer names L1/R1 and,
// on the chip, what A does there.
func TestToolsScenesFooterHintsFollowTheFocus(t *testing.T) {
	prev := toolsHub.page
	t.Cleanup(func() { toolsHub.page = prev })
	base := []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Open"}, {Button: "B", Label: "Back"}}

	toolsHub.page.chip = false
	hints := toolsScenesFooterHints(toolsHubSceneName, append([]FooterHint(nil), base...))
	if last := hints[len(hints)-1]; last.Button != "L1/R1" {
		t.Fatalf("hub hints = %+v, want L1/R1 last", hints)
	}
	toolsHub.page.chip = true
	hints = toolsScenesFooterHints(toolsHubSceneName, append([]FooterHint(nil), base...))
	if len(hints) != 3 || hints[1].Label != "Copy IP" {
		t.Fatalf("chip hints = %+v, want A Copy IP", hints)
	}
	if got := toolsScenesFooterHints("Settings", base); len(got) != len(base) {
		t.Fatalf("another scene's hints changed: %+v", got)
	}
}
