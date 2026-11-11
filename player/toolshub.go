package main

// toolshub.go — the Tools hub: one full-screen page for every tool.
//
// It replaced two older screens. The config's "Misc" scene was a page of
// shortcut buttons (Terminal, Cron, Net Speed …) and the Tools overlay was a
// modal of thirty-five tiles (the FTP & IP panel) that the Misc page opened
// with an "FTP & IP" button. Neither showed what needed a network, the overlay
// buried Watch Party and the toolkits under its network rows, and the user
// had two "Tools" screens with a "Classic Tools" tile pointing from one to the
// other. Now:
//
//   - Tools is this Go-owned scene, injected in memory (ensureToolsHubScene),
//     with Watch Party first, then the toolkits, connectivity, the system tools
//     and updates/support, each a section of glass tiles;
//   - every network tool moved to its own Network tools page
//     (nettoolsscreen.go), and the Wi-Fi picker became a page as well
//     (wifiscreen.go);
//   - the IP address sits in the header's top-right corner as a chip that
//     copies it when pressed;
//   - a tile the network cannot serve is dimmed with a "Needs Wi-Fi" or
//     "Needs internet" tag and explains itself when pressed.
//
// The three pages share their scene plumbing here: the injected scenes, the
// scene-entry hook core.go calls, and the click/wheel/key hooks main.go calls.

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	toolsHubSceneName = "Tools"
	netToolsSceneName = "NetworkTools"
	wifiScrSceneName  = "WiFi"
	// toolsHubLegacyScene is the classic Tools page this hub replaced. Configs
	// written before the hub still carry it; ensureToolsHubScene retires it.
	toolsHubLegacyScene = "Misc"
)

// toolsHubSlowValues are the tile values that read the disk (installed tools,
// the security posture). They refresh when the hub opens and every 15 s
// while it stays open, not on every rebuild.
type toolsHubSlowValues struct {
	toolbox      string
	toolboxReady bool
	audit        string
	auditReady   bool
	security     string
	securityWarn bool
	at           time.Time
}

// toolsHub is the hub's state: its tiles, its focus and the slow values.
var toolsHub = struct {
	page    toolsHubPage
	tiles   []toolsHubTile
	builtAt time.Time
	slow    toolsHubSlowValues
}{page: newToolsHubPage()}

// toolsHubWiFiName is the connected network's name, read off the render thread
// when the hub opens so the Wi-Fi tile can say where the device is connected.
var toolsHubWiFiName struct {
	mu   sync.Mutex
	ssid string
	at   time.Time
	busy bool
}

// The two hub actions that run in the background report through a toast; the
// flags stop a second press from starting a second run.
var (
	toolsHubCacheRunning atomic.Bool
	toolsHubDiscRunning  atomic.Bool
)

// ── Scenes ────────────────────────────────────────────────────────────────

// ensureToolsHubScene gives the Tools hub, the Network tools page, the Wi-Fi
// page and the Calculator a scene each, in memory, like ensureAlarmScene: the
// shipped configs carry none of them and the user's config file is never
// rewritten for it. It also retires the classic "Misc" page: buttons that
// pointed at it now open the hub, and the scene itself is dropped, so an
// older config on the card shows the same single Tools screen as a new one.
// Idempotent; called at startup and after every config reload.
func ensureToolsHubScene(config *Config) {
	if config == nil {
		return
	}
	toolsHubRetireMisc(config)
	toolsHubEnsureScenes(config)
	// The three pages are drawn and driven by Go. A button another feature
	// injected into one of them would win the keyboard from the page (the
	// element dispatch runs first), so their element lists stay empty.
	for i := range config.Scenes {
		switch config.Scenes[i].Name {
		case toolsHubSceneName, netToolsSceneName, wifiScrSceneName:
			config.Scenes[i].Elements = nil
			config.Scenes[i].Layout = ""
		}
	}
}

// toolsHubEnsureScenes appends whichever of the pages' scenes are missing.
// Appending never moves an existing scene, so it is safe on the live config;
// the entry points call it in case a config was swapped underneath them.
func toolsHubEnsureScenes(config *Config) {
	if config == nil {
		return
	}
	toolsHubEnsureScene(config, toolsHubSceneName, "T", "Tools")
	toolsHubEnsureScene(config, netToolsSceneName, "", "Network tools")
	toolsHubEnsureScene(config, wifiScrSceneName, "", "Wi-Fi")
	toolsHubEnsureScene(config, "Calculator", "C", "Quick math with history")
}

// toolsHubEnsureScene appends a scene when no scene of that name exists.
func toolsHubEnsureScene(config *Config, name, icon, description string) {
	if sceneIndexExact(config, name) >= 0 {
		return
	}
	config.Scenes = append(config.Scenes, SceneConfig{
		Name:        name,
		Icon:        icon,
		Description: description,
	})
}

// toolsHubRetireMisc points every "change_scene:Misc" (and the old
// "tools_open" overlay trigger) at the hub and removes the Misc scene. It only
// runs on a config that is not on screen yet — at startup, before the first
// scene is chosen, and on a freshly loaded reload, whose current scene is then
// found again by name — so no live scene index points into the shortened list.
func toolsHubRetireMisc(config *Config) {
	for si := range config.Scenes {
		elems := config.Scenes[si].Elements
		for ei := range elems {
			e := &elems[ei]
			if e.Trigger == "change_scene:"+toolsHubLegacyScene || e.Trigger == "tools_open" {
				e.Trigger = "change_scene:" + toolsHubSceneName
				e.TriggerTarget = toolsHubSceneName
			}
			// The loader copies the target into TriggerTarget (and the builder
			// writes SceneChange), so both spellings follow the trigger.
			if e.TriggerTarget == toolsHubLegacyScene &&
				(e.Trigger == "change_scene" || e.Trigger == "change_scene:"+toolsHubSceneName) {
				e.TriggerTarget = toolsHubSceneName
			}
			if e.SceneChange == toolsHubLegacyScene {
				e.SceneChange = toolsHubSceneName
			}
		}
	}
	if idx := sceneIndexExact(config, toolsHubLegacyScene); idx >= 0 {
		config.Scenes = append(config.Scenes[:idx], config.Scenes[idx+1:]...)
	}
}

// toolsHubOpen opens the Tools hub at its top (cross-area contract: Home, the
// Start menu, the "tools_open" trigger and every other entry point call it).
// Coming back from a tool goes through the scene history instead and keeps
// the tile that was used.
func toolsHubOpen(config *Config) {
	if config == nil {
		return
	}
	toolsHubEnsureScenes(config)
	idx := sceneIndexExact(config, toolsHubSceneName)
	if idx < 0 {
		return
	}
	// The hub replaces the panel it was opened from.
	qs.open = false
	toolsHub.page.reset()
	if idx == currentSceneIndex {
		toolsHubEnter(config)
		return
	}
	changeSceneTo(config, idx)
}

// toolsScenesEnter runs when a scene transition lands (core.go calls it for
// every scene). Each page loads what it shows as it opens: the hub's values,
// the Network tools probes that need no input, the Wi-Fi scan.
func toolsScenesEnter(config *Config, name string) {
	switch name {
	case toolsHubSceneName:
		toolsHubEnter(config)
	case netToolsSceneName:
		netToolsEnter(config)
	case wifiScrSceneName:
		wifiScrEnter(config)
	}
}

// toolsHubTourScene is the scene the first-run tour was drawn over when one of
// its rows opened a page (the Wi-Fi page, the Tools hub). Pages are scenes,
// not modal panels, so the tour cannot wait for "the panel to close"; it waits
// for the user to come back to this scene instead.
var toolsHubTourScene string

// toolsHubTourMark remembers the scene the tour is on before a row navigates.
func toolsHubTourMark() {
	toolsHubTourScene = ""
	if appConfig != nil && currentSceneIndex >= 0 && currentSceneIndex < len(appConfig.Scenes) {
		toolsHubTourScene = appConfig.Scenes[currentSceneIndex].Name
	}
}

// toolsHubTourReset forgets the mark (a new tour starts where it is).
func toolsHubTourReset() { toolsHubTourScene = "" }

// toolsHubTourAway reports whether a tour row took the user to another scene
// that they have not come back from yet. The tour hides and lets the keys
// through meanwhile, exactly as it does under a modal panel.
func toolsHubTourAway() bool {
	if toolsHubTourScene == "" || appConfig == nil || currentSceneIndex < 0 || currentSceneIndex >= len(appConfig.Scenes) {
		return false
	}
	return appConfig.Scenes[currentSceneIndex].Name != toolsHubTourScene
}

// toolsScenesCurrent names the current scene when it is one of the three
// tools pages, or "".
func toolsScenesCurrent(config *Config) string {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return ""
	}
	switch name := config.Scenes[currentSceneIndex].Name; name {
	case toolsHubSceneName, netToolsSceneName, wifiScrSceneName:
		return name
	}
	return ""
}

// toolsScenesHandleKey gives the tools pages their keys before the generic
// element dispatch, the way Patch and Alarm get theirs. It steps aside while
// the top bar holds the focus, so the bar's own handler answers.
func toolsScenesHandleKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !toolsHubRingVisible() {
		return false
	}
	switch toolsScenesCurrent(config) {
	case toolsHubSceneName:
		return toolsHubKey(e, config)
	case netToolsSceneName:
		return netToolsKey(e, config)
	case wifiScrSceneName:
		return wifiScrKey(e, config)
	}
	return false
}

// toolsScenesClick routes a click or tap on a tools page.
func toolsScenesClick(mx, my int32, config *Config) bool {
	switch toolsScenesCurrent(config) {
	case toolsHubSceneName:
		return toolsHubClick(mx, my, config)
	case netToolsSceneName:
		return netToolsClick(mx, my, config)
	case wifiScrSceneName:
		return wifiScrClick(mx, my, config)
	}
	return false
}

// toolsScenesWheel scrolls a tools page (or its open sheet).
func toolsScenesWheel(config *Config, y int32) bool {
	switch toolsScenesCurrent(config) {
	case toolsHubSceneName:
		return toolsHub.page.wheel(y)
	case netToolsSceneName:
		return netToolsWheel(y)
	case wifiScrSceneName:
		return wifiScrWheel(y)
	}
	return false
}

// toolsScenesFooterHints is the footer for a tools page, following its open
// sheet the way Patch's footer follows its dialog. hints is the default
// navigate/open/back row.
func toolsScenesFooterHints(name string, hints []FooterHint) []FooterHint {
	switch name {
	case toolsHubSceneName:
		if toolsHub.page.chip {
			return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Copy IP"}, {Button: "B", Label: "Back"}}
		}
		return append(hints, FooterHint{Button: "L1/R1", Label: "Section"})
	case netToolsSceneName:
		return netToolsFooterHints(hints)
	case wifiScrSceneName:
		return wifiScrFooterHints(hints)
	}
	return hints
}

// ── Hub ───────────────────────────────────────────────────────────────────

// toolsHubEnter refreshes everything the hub shows as it opens.
func toolsHubEnter(config *Config) {
	toolsHubRefreshSlow(config)
	toolsHubRebuild(config)
	toolsHubRefreshWiFiName()
	toolsHub.page.follow = true
}

// toolsHubRefreshSlow reads the values that touch the disk.
func toolsHubRefreshSlow(config *Config) {
	s := &toolsHub.slow
	s.toolbox = toolboxSummaryValue()
	s.toolboxReady = toolboxInstalledCount() > 0
	installed := secAuditInstalledCount()
	s.audit = strconv.Itoa(installed) + " of " + strconv.Itoa(len(secAuditCatalog())) + " installed"
	s.auditReady = installed > 0
	s.security, s.securityWarn = securityPostureValue(config)
	s.at = time.Now()
}

// toolsHubRefreshWiFiName reads the connected network's name in the
// background; the Wi-Fi tile shows it on the next rebuild.
func toolsHubRefreshWiFiName() {
	toolsHubWiFiName.mu.Lock()
	if toolsHubWiFiName.busy || (!toolsHubWiFiName.at.IsZero() && time.Since(toolsHubWiFiName.at) < 20*time.Second) {
		toolsHubWiFiName.mu.Unlock()
		return
	}
	toolsHubWiFiName.busy = true
	toolsHubWiFiName.mu.Unlock()
	started := runSceneTask(func(context.Context) {
		ssid := ""
		if st, err := wifiReadStatus(); err == nil && st.Connected {
			ssid = st.SSID
		}
		toolsHubWiFiName.mu.Lock()
		toolsHubWiFiName.ssid = ssid
		toolsHubWiFiName.at = time.Now()
		toolsHubWiFiName.busy = false
		toolsHubWiFiName.mu.Unlock()
	})
	if !started {
		toolsHubWiFiName.mu.Lock()
		toolsHubWiFiName.busy = false
		toolsHubWiFiName.mu.Unlock()
	}
}

// toolsHubWiFiSSID is the last network name read, or "". Screenshot mode
// names a stand-in network instead (screenshotmode.go).
func toolsHubWiFiSSID() string {
	toolsHubWiFiName.mu.Lock()
	defer toolsHubWiFiName.mu.Unlock()
	if toolsHubWiFiName.ssid != "" && shotModeOn() {
		return shotSSID
	}
	return toolsHubWiFiName.ssid
}

// toolsHubRebuild rebuilds the tiles and keeps the focus on the same tool.
func toolsHubRebuild(config *Config) {
	focusID := ""
	if c := toolsHub.page.cursor; c >= 0 && c < len(toolsHub.tiles) {
		focusID = toolsHub.tiles[c].id
	}
	toolsHub.tiles = toolsHubBuildTiles(config, toolsHubReadNet(), toolsHub.slow, toolsHubWiFiSSID())
	toolsHub.builtAt = time.Now()
	if focusID != "" {
		if i := toolsHubIndexOf(toolsHub.tiles, focusID); i >= 0 {
			toolsHub.page.cursor = i
		}
	}
	toolsHub.page.clampCursor(len(toolsHub.tiles))
}

// toolsHubBuildTiles is the hub's tile list, in display order. A tile whose
// screen this config does not have is left out rather than drawn dead.
func toolsHubBuildTiles(config *Config, net toolsHubNet, slow toolsHubSlowValues, ssid string) []toolsHubTile {
	tiles := make([]toolsHubTile, 0, 24)
	has := func(scene string) bool { return sceneIndexExact(config, scene) >= 0 }

	// TOGETHER — Watch Party leads the page.
	party := toolsHubTile{
		id: "party", section: "Together", sectionIcon: "users",
		label: "Watch Party", icon: "party", need: toolsHubNeedLAN, wide: true,
		value: "Watch the same stream together on this Wi-Fi, with a shared queue",
	}
	if partyActive() {
		party.tone = toolsHubToneGood
		party.value = "You are in a room"
		if invite := partyInviteText(); invite != "" {
			party.value = "In a room · invite " + invite
		}
	}
	tiles = append(tiles, party)
	// Local play sits beside it: players joining with their own pads, and
	// pass and play (hotseat_ui.go).
	tiles = append(tiles, hotseatToolsTile())
	// Your other JukaHubs: who is playing what on this Wi-Fi, and saves kept
	// the same across devices (peer.go, nearby.go, ssync.go).
	tiles = append(tiles, nearbyHubTile(), ssyncHubTile())

	// TOOLKITS
	toolbox := toolsHubTile{
		id: "toolbox", section: "Toolkits", sectionIcon: "toolbox",
		label: "Toolbox", icon: "toolbox", value: slow.toolbox,
	}
	if slow.toolboxReady {
		toolbox.tone = toolsHubToneGood
	}
	audit := toolsHubTile{id: "audit", label: "Audit toolkit", icon: "audit", value: slow.audit}
	if slow.auditReady {
		audit.tone = toolsHubToneGood
	}
	security := toolsHubTile{id: "sectrust", label: "Security & trust", icon: "shield", value: slow.security}
	if slow.securityWarn {
		security.tone = toolsHubToneWarn
	}
	tiles = append(tiles, toolbox, audit, security)

	// CONNECTIVITY
	network := toolsHubTile{
		id: "nettools", section: "Connectivity", sectionIcon: "network",
		label: "Network tools", icon: "network",
		value: "File servers, probes and lookups",
	}
	switch ftp, ssh := ftpSrv.Running(), sshSrv.Running(); {
	case ftp && ssh:
		network.value, network.tone = "FTP and SFTP are on", toolsHubToneGood
	case ftp:
		network.value, network.tone = "FTP server is on", toolsHubToneGood
	case ssh:
		network.value, network.tone = "SFTP server is on", toolsHubToneGood
	}
	wifi := toolsHubTile{id: "wifi", label: "Wi-Fi", icon: "wifi", value: "Not connected — choose a network"}
	switch {
	case net.ip != "" && ssid != "":
		wifi.value, wifi.tone = ssid, toolsHubToneGood
	case net.ip != "":
		wifi.value, wifi.tone = "Connected · "+net.ip, toolsHubToneGood
	default:
		wifi.icon = "wifi-off"
	}
	tiles = append(tiles, network, wifi)
	if has("NetSpeed") {
		tiles = append(tiles, toolsHubTile{
			id: "netspeed", label: "Net speed", icon: "speed",
			need: toolsHubNeedInternet, value: "Download, upload and ping",
		})
	}

	// SYSTEM
	system := make([]toolsHubTile, 0, 12)
	if has("Terminal") {
		system = append(system, toolsHubTile{id: "terminal", label: "Terminal", icon: "terminal", value: "Run commands on this device"})
	}
	if has("Cron") {
		system = append(system, toolsHubTile{id: "sched", label: "Scheduled tasks", icon: "schedule", value: "Timed jobs and startup items"})
	}
	if has("Hardware") {
		system = append(system, toolsHubTile{id: "hardware", label: "Hardware monitor", icon: "hardware", value: "CPU, memory and temperature"})
	}
	if has("DiskSpace") {
		system = append(system, toolsHubTile{id: "disk", label: "Disk space", icon: "disk", value: "How full each drive is"})
	}
	if has("Benchmark") {
		system = append(system, toolsHubTile{id: "benchmark", label: "Benchmark", icon: "benchmark", value: "Storage read and write speed"})
	}
	if has("LogExporter") {
		system = append(system, toolsHubTile{id: "logs", label: "Logs", icon: "logs", value: "Export logs for a bug report"})
	}
	cache := toolsHubTile{id: "cache", label: "Clear cache", icon: "broom", value: "Thumbnails and saved lookups"}
	if toolsHubCacheRunning.Load() {
		cache.value, cache.tone = "Clearing…", toolsHubToneBusy
	}
	system = append(system, cache)
	system = append(system, toolsHubTile{id: "powertools", label: "Power tools", icon: "power", value: "Files · Scripts · Sync"})
	disc := toolsHubTile{id: "disc", label: "Disc helpers", icon: "disc", value: "Cue sheets and multi-disc playlists"}
	if toolsHubDiscRunning.Load() {
		disc.value, disc.tone = "Checking your disc games…", toolsHubToneBusy
	}
	system = append(system, disc)
	system = append(system, toolsHubTile{id: "rommgr", label: "ROM manager", icon: "rom", value: "File loose games, BIOS and duplicates"})
	if has("Calculator") {
		system = append(system, toolsHubTile{id: "calculator", label: "Calculator", icon: "calculator", value: "Quick math with history"})
	}
	// The trophy room is injected on demand (trophyOpenRoom), so its tile is
	// there whatever the config carries. Its value is the live count, which is
	// what makes the tile worth opening; trophySummary is cached between frames.
	trophies := toolsHubTile{id: "trophies", label: "Trophies & playtime", icon: "trophy", value: "Your stats and badges"}
	if earned, shown := trophySummary(); shown > 0 {
		trophies.value = strconv.Itoa(earned) + " of " + strconv.Itoa(shown) + " earned"
	}
	system = append(system, trophies)
	system[0].section, system[0].sectionIcon = "System", "cpu"
	tiles = append(tiles, system...)

	// YOUR SETUP — experience packs, the settings as one file, and backups
	// (xpack.go, cfgdoc.go, bkup.go).
	packs := toolsHubTile{
		id: "xpack", section: "Your setup", sectionIcon: "sparkles",
		label: "Experience packs", icon: "sparkles",
		value: "Retro Arcade, Movie Night and more in one switch",
	}
	if name := xpackLabelCache; name != "" {
		packs.value, packs.tone = "Using "+name, toolsHubToneGood
	}
	cfgTile := toolsHubTile{id: "cfgdoc", label: "Config", icon: "code", value: "What you changed, as one file to diff and share"}
	if up := cfgdocUpstreamLabel(config); up != "" {
		cfgTile.value, cfgTile.tone = up+" since the last update", toolsHubToneWarn
	}
	backup := toolsHubTile{id: "bkup", label: "Backup & restore", icon: "backup", value: "Your whole setup in one file for any device"}
	if bkupAutoEnabled(config) {
		backup.value, backup.tone = "Weekly backup is on", toolsHubToneGood
	}
	tiles = append(tiles, packs, cfgTile, backup)

	// UPDATES & SUPPORT
	support := make([]toolsHubTile, 0, 3)
	if has("Patch") {
		updates := toolsHubTile{id: "updates", label: "Updates", icon: "update", value: "Updates, packages and rollback"}
		if updateAvailable() {
			updates.value, updates.tone = "Update "+availableUpdateLabel()+" is ready", toolsHubToneWarn
		}
		support = append(support, updates)
	}
	support = append(support,
		toolsHubTile{id: "bug", label: "Report a bug", icon: "bug", value: "Send a report with diagnostics"},
		toolsHubTile{id: "community", label: "Community & support", icon: "community", value: "What's new, help and feedback"},
	)
	support[0].section, support[0].sectionIcon = "Updates & support", "update"
	tiles = append(tiles, support...)

	toolsHubApplyNeeds(tiles, net)
	return tiles
}

// toolsHubHeaderGlyph is the header's icon, built once.
var toolsHubHeaderGlyph = func(r *sdl.Renderer, box sdl.Rect, _ sdl.Color) {
	DrawHomeIcon(r, "tools", box, false)
}

// toolsHubHeader is the hub's page bar. The subtitle says what the network
// allows, so the dimmed tiles below explain themselves at a glance.
func toolsHubHeader(net toolsHubNet) UIPageHeader {
	sub := "Share, fix and tinker — everything that is not a game or a video"
	subInk := sdl.Color{}
	switch {
	case net.ip == "":
		sub, subInk = "Offline — tools that need Wi-Fi are dimmed until you connect", ColorToastWarn
	case !net.online:
		sub, subInk = "No internet on this Wi-Fi — tools that need it are dimmed", ColorToastWarn
	}
	return UIPageHeader{
		Title:         "Tools",
		Subtitle:      sub,
		SubtitleColor: StatusInk(subInk),
		TitleSize:     menuTitleSize,
		Glyph:         toolsHubHeaderGlyph,
	}
}

// renderToolsHub draws the hub: Home's backdrop, the page, then the shared
// status bar and footer on top.
func renderToolsHub(renderer *sdl.Renderer, config *Config) {
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	if toolsHub.tiles == nil || time.Since(toolsHub.builtAt) > time.Second {
		if time.Since(toolsHub.slow.at) > 15*time.Second {
			toolsHubRefreshSlow(config)
		}
		toolsHubRebuild(config)
	}
	net := toolsHubReadNet()
	header := toolsHubHeader(net)
	toolsHubDrawPage(renderer, config, &toolsHub.page, header, toolsHub.tiles, net.ip, toolsHubRingVisible())
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetClipRect(nil)
}

// toolsHubPress is A / Enter on the hub: the chip copies the address, a tile
// opens its tool.
func toolsHubPress(config *Config) {
	if toolsHub.page.chip {
		toolsHubCopyIP()
		return
	}
	toolsHubActivate(config, toolsHub.page.cursor)
}

// toolsHubActivate opens the tool behind tile idx. A dimmed tile says what it
// needs and stays on the hub: it never navigates anywhere unexpected.
func toolsHubActivate(config *Config, idx int) {
	if idx < 0 || idx >= len(toolsHub.tiles) {
		return
	}
	t := toolsHub.tiles[idx]
	if t.blocked != "" {
		ShowToast(toolsHubBlockedMessage(t.label, t.blocked), ToastKindWarning)
		return
	}
	if t.id != "powertools" {
		// Power tools plays its own sound as it opens.
		PlayActivateSound()
	}
	switch t.id {
	case "party":
		partyOpenScreen(config)
	case "localplay":
		hotseatOpen(config)
	case "nearby":
		nearbyOpenScreen(config)
	case "ssync":
		ssyncOpenScreen(config)
	case "toolbox":
		tbxOpenScreen(config)
	case "audit":
		auditOpenScreen(config)
	case "sectrust":
		sectrustOpenScreen(config)
	case "nettools":
		netToolsOpen(config)
	case "wifi":
		wifiScrOpen(config)
	case "netspeed":
		toolsHubOpenScene(config, "NetSpeed")
	case "terminal":
		toolsHubOpenScene(config, "Terminal")
	case "sched":
		schedOpen(config)
	case "hardware":
		toolsHubOpenScene(config, "Hardware")
	case "disk":
		toolsHubOpenScene(config, "DiskSpace")
	case "benchmark":
		toolsHubOpenScene(config, "Benchmark")
	case "logs":
		toolsHubOpenScene(config, "LogExporter")
	case "cache":
		toolsHubClearCache()
	case "powertools":
		openPowerTools(config)
	case "disc":
		toolsHubRunDiscHelpers()
	case "rommgr":
		rommOpen(config)
	case "calculator":
		toolsHubOpenScene(config, "Calculator")
	case "trophies":
		trophyOpenRoom(config)
	case "xpack":
		xpackOpenGallery(config)
	case "cfgdoc":
		cfgdocOpenInspector(config)
	case "bkup":
		bkupOpenScreen(config)
	case "updates":
		toolsHubOpenScene(config, "Patch")
	case "bug":
		bugrepOpenScreen(config)
	case "community":
		openCommunityPanel(config, communityPageHome)
	}
}

// toolsHubOpenScene switches to a config scene by its exact name. A missing
// scene is reported, never replaced by Home (findSceneIndex's fallback).
func toolsHubOpenScene(config *Config, name string) {
	idx := sceneIndexExact(config, name)
	if idx < 0 {
		ShowToast(sceneDisplayName(name)+" is not part of this build", ToastKindInfo)
		return
	}
	if reason, blocked := offlineSceneBlock(name); blocked {
		ShowToast(reason, ToastKindWarning)
		return
	}
	changeSceneTo(config, idx)
}

// toolsHubClearCache empties the shared cache (artwork lookups, feeds) off the
// render thread, the job the classic page's "Clear Cache" button did inline.
func toolsHubClearCache() {
	if !toolsHubCacheRunning.CompareAndSwap(false, true) {
		ShowToast("Already clearing the cache", ToastKindInfo)
		return
	}
	go func() {
		defer toolsHubCacheRunning.Store(false)
		db, err := openSharedCache("jukahub.cache")
		if err == nil {
			err = cacheClear(db)
		}
		if err != nil {
			ShowToast("Could not clear the cache: "+firstLine(err.Error()), ToastKindError)
			return
		}
		ShowToast("Cache cleared — artwork and lookups load fresh", ToastKindSuccess)
	}()
}

// toolsHubRunDiscHelpers writes the missing cue sheets and playlists in the
// background: walking a card full of disc images is too slow for a frame.
func toolsHubRunDiscHelpers() {
	if !toolsHubDiscRunning.CompareAndSwap(false, true) {
		ShowToast("Disc helpers are already running", ToastKindInfo)
		return
	}
	ShowToast("Checking your disc games…", ToastKindInfo)
	go func() {
		defer toolsHubDiscRunning.Store(false)
		report := generateDiscHelpers(romLibraryRoots())
		kind := ToastKindInfo
		switch {
		case report.Created() == 0 && len(report.Errors) > 0:
			kind = ToastKindError
		case len(report.Errors) > 0:
			kind = ToastKindWarning
		case report.Created() > 0:
			kind = ToastKindSuccess
		}
		ShowToast(report.Summary(), kind)
	}()
}

// ── Hub input ─────────────────────────────────────────────────────────────

// toolsHubKey is the hub's keyboard.
func toolsHubKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	p := &toolsHub.page
	switch e.Keysym.Sym {
	case sdl.K_UP:
		return p.move(toolsHub.tiles, 0, -1)
	case sdl.K_DOWN:
		return p.move(toolsHub.tiles, 0, 1)
	case sdl.K_LEFT:
		return p.move(toolsHub.tiles, -1, 0)
	case sdl.K_RIGHT:
		return p.move(toolsHub.tiles, 1, 0)
	case sdl.K_PAGEUP:
		p.jumpSection(toolsHub.tiles, -1)
	case sdl.K_PAGEDOWN:
		p.jumpSection(toolsHub.tiles, 1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		toolsHubPress(config)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		goBackScene(config)
	default:
		return false
	}
	return true
}

// toolsHubPad is the hub's controller.
func toolsHubPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	p := &toolsHub.page
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return p.move(toolsHub.tiles, 0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return p.move(toolsHub.tiles, 0, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return p.move(toolsHub.tiles, -1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return p.move(toolsHub.tiles, 1, 0)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		p.jumpSection(toolsHub.tiles, -1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		p.jumpSection(toolsHub.tiles, 1)
	case sdl.CONTROLLER_BUTTON_A:
		toolsHubPress(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(config)
	default:
		return false
	}
	return true
}

// toolsHubClick is a click or tap on the hub: the chip copies, a tile takes
// the focus and opens. A miss is left for the bars.
func toolsHubClick(mx, my int32, config *Config) bool {
	p := &toolsHub.page
	if p.chipRect.W > 0 && hitTouchTarget(p.chipRect, mx, my) {
		p.chip = true
		toolsHubCopyIP()
		return true
	}
	if i := p.hitTile(mx, my); i >= 0 {
		p.chip = false
		p.cursor = i
		p.wantX = -1
		toolsHubActivate(config, i)
		return true
	}
	return false
}

// toolsHubScene registers the hub with the scene registry.
type toolsHubScene struct{ sceneBase }

func (toolsHubScene) render(r *sdl.Renderer, c *Config) bool {
	renderToolsHub(r, c)
	return true
}

func (toolsHubScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return toolsHubKey(e, c)
}

func (toolsHubScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return toolsHubPad(e, c)
}

func init() { registerScene(toolsHubScene{sceneBase{name: toolsHubSceneName}}) }
