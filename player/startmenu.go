package main

import (
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// Start menu
//
// Start (or the Apps pill in the top bar) opens this menu. It is a small
// two-pane screen: scenes on one side, and the tools and device actions that
// do not belong to a scene on the other. The rail on the left holds the groups,
// the list on the right holds the entries, and the footer names the scene the
// menu was opened over.
//
// The old menu was one long flat list of every scene and tool, which made
// "go to a scene" a scroll-hunt. Grouping keeps the common action (switch
// screen) two presses away and stops a device action from being buried under
// twenty scenes.
// ──────────────────────────────────────────────────────────────────────

const (
	startMenuW      = int32(620)
	startMenuGroupW = int32(158)
	startMenuItemH  = int32(40)
	startMenuGroupH = int32(42)
	startMenuHeadH  = int32(64)
	startMenuFootH  = int32(38)
)

type startMenuState struct {
	startRect sdl.Rect // Apps pill hit area, measured by renderStatusBar
	startOpen bool     // menu visible
	group     int      // highlighted group in the rail
	focus     int      // 0 = group rail, 1 = entries
	cursor    int      // highlighted entry inside the group
	scroll    int      // first visible entry
	enter     uiEnter  // drop-down + fade entrance
}

var tb startMenuState

// startMenuHeadNow is the header height for the fonts currently loaded. It is
// refreshed at the top of every render: at 145% text the fixed 64 px band let
// the subtitle run under the first row's focus ring.
var startMenuHeadNow = startMenuHeadH

// startMenuHeadHeight measures the title and subtitle plus the gap before the
// first row, never below the original 64 px band.
func startMenuHeadHeight(config *Config) int32 {
	height := startMenuHeadH
	font, _ := getCachedFont(config, "medium")
	if font == nil {
		return height
	}
	caption := getFontSized(config, "small", footerHintSize)
	measured := int32(10) + int32(font.Height()) + 4
	if caption != nil {
		measured += int32(caption.Height()) + 8
	}
	if measured > height {
		return measured
	}
	return height
}

var startMenuLayout struct {
	title, subtitle, firstRow, footer sdl.Rect
}

// startMenuEntry is a single item in a group: a scene to open or an action.
type startMenuEntry struct {
	name   string
	icon   string
	scene  string
	action func(*Config)
	// glyph names the row's vector icon directly (plugin rows); "" looks
	// it up by scene or name.
	glyph string
	// badge is a short reading drawn at the row's right edge in the muted
	// ink: "12/101" beside Trophies. It is only drawn — the name stays the
	// row's identity for the audits, the duplicate check and the shortcuts.
	badge string
}

// startMenuGroup is one rail entry and the items it lists.
type startMenuGroup struct {
	name  string
	icon  string
	items []startMenuEntry
}

// startMenuHit maps a visible row or group back to its index for clicks.
type startMenuHit struct {
	index int
	rect  sdl.Rect
}

var (
	startMenuGroupHits []startMenuHit
	startMenuItemHits  []startMenuHit
)

// phoneCompanionEntryName is the Device row that opens the pairing card.
const phoneCompanionEntryName = "Phone Companion"

// The Games rows beside PS Remote Play for Xbox Remote Play (X1, xremote.go;
// also the header pill) and the Steam library (steam_ui.go; also an Apps
// row). Their rows are what an "app:do:" ref names, so a favourite, a
// collection, the Tabs Home's Games tab and Home search all open them
// through homeActivateApp.
const (
	xboxRemotePlayEntryName = "Xbox Remote Play"
	steamEntryName          = "Steam"
)

// startMenuQRRect is the Phone Companion's pairing code under the rail, as
// drawn this frame: a tap shows it full screen, and Screen Share paints over
// it (castShareRedactions) because its link carries the PIN. Empty while the
// menu is closed, no code exists yet or the rail has no room for a readable
// one. Render/event thread only.
var startMenuQRRect sdl.Rect

// startMenuQRMinModule is the smallest module, in pixels, the code is drawn
// with: below it a phone camera across the room cannot read it.
const startMenuQRMinModule = int32(3)

// startMenuDrawQR draws the pairing code at the foot of the rail, between the
// last group and the footer, with a caption under it. The code fills the
// rail's width when the height allows, in whole-pixel modules with a
// two-module quiet zone, like the Home header's (renderCompanionHeaderQR).
func startMenuDrawQR(renderer *sdl.Renderer, config *Config, caption *ttf.Font, railX, railW, top, bottom int32) {
	startMenuQRRect = sdl.Rect{}
	if renderer == nil || len(companion.qr) == 0 {
		return
	}
	core := companionHeaderQRCore()
	n := int32(len(core))
	if n == 0 {
		return
	}
	captionH := int32(0)
	if caption != nil {
		captionH = int32(caption.Height()) + 6
	}
	side := min32(railW-8, bottom-top-captionH)
	module := side / (n + 4)
	if module < startMenuQRMinModule {
		return
	}
	side = module * (n + 4)
	tile := sdl.Rect{X: railX + (railW-side)/2, Y: top, W: side, H: side}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, tile.X, tile.Y, tile.W, tile.H, max32(6, side/12), sdl.Color{R: 255, G: 255, B: 255, A: 255})
	drawCompanionQR(renderer, insetRect(tile, module*2), core)
	startMenuQRRect = tile
	if caption != nil {
		label := ellipsize(caption, "Scan: Phone Companion", railW)
		lw, _ := measureText(caption, label)
		renderText(renderer, config, caption, label, ColorTextMuted(), railX+(railW-lw)/2, tile.Y+tile.H+6)
	}
}

// startMenuSceneEntries lists the config's own scenes in the order people
// expect them: the main screens first, then everything else. Hidden, utility
// and maintenance scenes stay out; About is offered by the Device group so it
// is not listed twice.
//
// Trophies comes right after Home. The trophy room is injected at startup
// (ensurePlaytimeScene), so it used to fall through to the unordered tail and
// sat last in a long Screens list, where testers never found it. It is the
// screen people open for fun, so it is now one press below Home in the group
// the menu opens on (after any screens an experience pack pins), with the
// earned count beside it.
func startMenuSceneEntries(config *Config) []startMenuEntry {
	if config == nil {
		return nil
	}
	preferOrder := []string{
		"Main", playtimeSceneName, "Tube", "LiveTV", "FileExplorer", "Chat", toolsHubSceneName, "Calculator",
		"Pomodoro", "Alarm", "Dashboard", "Cron", "Settings",
		"JukaLand", "CanvasSandbox", "Favorites", "Terminal",
		"Dictionary", "PDFReader", "MusicPlayer", synthSceneName, "Notes",
		"Games", "Browser", "TextBrowser", "Apps", "Packages", "Shorts",
	}
	// The experience pack in force lists the screens it pins first (xpack.go).
	if pins := xpackPinnedScenes(); len(pins) > 0 {
		order := append([]string(nil), pins...)
		for _, name := range preferOrder {
			pinned := false
			for _, pin := range pins {
				pinned = pinned || pin == name
			}
			if !pinned {
				order = append(order, name)
			}
		}
		preferOrder = order
	}
	seen := make(map[string]bool)
	var entries []startMenuEntry
	add := func(sc SceneConfig) {
		if sc.Icon == "" || sc.Name == "Exit" || sc.Name == "Patch" || sc.Name == "About" {
			return
		}
		entry := startMenuEntry{
			name:  sceneDisplayName(sc.Name),
			icon:  sc.Icon,
			scene: sc.Name,
		}
		if sc.Name == playtimeSceneName {
			// Cached between frames (trophySummary), so the menu can ask on
			// every frame it draws.
			entry.badge = trophySummaryBadge()
		}
		entries = append(entries, entry)
		seen[sc.Name] = true
	}
	for _, name := range preferOrder {
		for _, sc := range config.Scenes {
			if sc.Name == name {
				add(sc)
				break
			}
		}
	}
	for _, sc := range config.Scenes {
		if seen[sc.Name] {
			continue
		}
		// Sub-settings screens belong to Settings, not to the Start menu.
		if strings.HasPrefix(sc.Name, "Settings") || strings.HasPrefix(sc.Name, "Theme") {
			continue
		}
		add(sc)
	}
	return entries
}

// startMenuGroups builds the whole menu. Actions reuse the same handlers the
// old flat menu used, so opening a panel from here is unchanged; only where it
// is listed moved.
func startMenuGroups(config *Config) []startMenuGroup {
	var games []startMenuEntry
	// Put the games that ship with JukaHub together at the top of Games.
	// The badge is drawn separately so the scene names stay stable for search
	// and shortcuts. Custom configs may omit any of these scenes. The Synth
	// (synth.go) sits under JukaLand; its Tools hub tile stays.
	for _, scene := range []string{"Games", "JukaLand", synthSceneName} {
		if config != nil && sceneIndexExact(config, scene) >= 0 {
			games = append(games, startMenuEntry{
				name: sceneDisplayName(scene), icon: "G", scene: scene, badge: "Built-in",
			})
		}
	}
	// The recent games, each where it was left (gameswitcher.go).
	if gameSwitcherEnabled(config) {
		games = append(games, startMenuEntry{name: gameSwitcherEntryName, icon: "W", glyph: "games", action: func(c *Config) { gameSwitcherOpen(c) }})
	}
	games = append(games, []startMenuEntry{
		{name: "Emulators", icon: "E", action: func(*Config) { openEmulatorPanel() }},
		{name: gamesLibraryEntryName, icon: "L", action: func(*Config) { openGameLibraryPanel() }},
		{name: gameProfilesEntryName, icon: "G", action: func(*Config) { openGameProfilePanel(false) }},
		{name: "PS Remote Play", icon: "C", action: func(*Config) { openChiakiPanel() }},
		{name: xboxRemotePlayEntryName, icon: "X", glyph: "xbox", action: func(*Config) { x1TileActivate() }},
		// A gaming PC through Moonlight (Vibepollo, Apollo, Sunshine; moonlight_ui.go).
		{name: moonlightEntryName, icon: "P", glyph: "devices", action: func(*Config) { openMoonlightPanel() }},
		// Xbox Cloud Gaming (Game Pass) and GeForce NOW (cloudgaming*.go).
		{name: cloudGamingEntryName, icon: "C", glyph: cloudGamingGlyph, action: func(c *Config) { cloudGamingOpen(c) }},
		{name: steamEntryName, icon: "S", glyph: "gamepad", action: func(*Config) { openSteamPanel() }},
		{name: "RetroAchievements", icon: "R", action: func(*Config) { openRetroAchievementsPanel() }},
		{name: "Save Manager", icon: "S", action: func(*Config) { openSaveManagerPanel() }},
	}...)
	// Windows Apps: Wine on the handheld, the programs themselves on Windows.
	games = append(games, startMenuEntry{name: windowsAppsEntryName, icon: "W", action: func(*Config) { openWindowsAppsPanel(false) }})
	if !IsWindows() {
		games = append(games, startMenuEntry{name: portMasterEntryName, icon: "P", action: func(*Config) { openPortsPanel(false) }})
		// Splore with the user's own PICO-8, or what to copy for it (pico8native.go).
		games = append(games, startMenuEntry{name: pico8SploreEntryName, icon: "8", glyph: "rom", action: func(*Config) { pico8SploreOpen() }})
	}
	// PortMaster's catalogue as a store (ports_store*.go). A PC browses it;
	// the ports themselves install on the handheld.
	games = append(games, startMenuEntry{name: portsStoreEntryName, icon: "P", glyph: portsStoreGlyph, action: func(c *Config) { portsStoreOpen(c) }})
	// PCs can inspect and download APKs even though AndroidRT runs on the handheld.
	games = append(games, startMenuEntry{name: apkStoreEntryName, icon: "A", action: func(c *Config) { apkStoreOpen(c) }})
	// Every way to play Minecraft here: ports, Bedrock, a PC's launchers.
	games = append(games, startMenuEntry{name: minecraftEntryName, icon: "M", glyph: minecraftGlyph, action: func(c *Config) { minecraftOpen(c) }})
	// RetroAchievements is only meaningful where a card can hold credentials.
	if !retroAchievementsSupported() {
		games = withoutStartMenuEntry(games, "RetroAchievements")
	}
	if !x1PillSupported() {
		games = withoutStartMenuEntry(games, xboxRemotePlayEntryName)
	}
	if p := compatEntryName; p != "" {
		games = append(games, startMenuEntry{name: p, icon: "T", action: func(*Config) { openCompatPanel(true) }})
	}

	tools := []startMenuEntry{
		{name: "Network tools", icon: "N", action: func(c *Config) { netToolsOpen(c) }},
		{name: "Downloads", icon: "D", action: func(*Config) { openDownloadsPanel() }},
		// Mail, with its unread count, and the page that sets up mail and
		// calendars (mail_*.go, mailcal_ui.go).
		{name: mailcalAppEntryName, icon: "M", glyph: "mail", badge: mailcalMenuBadge(), action: func(*Config) { mailOpenApp() }},
		{name: mailcalSettingsEntryName, icon: "E", glyph: "calendar", action: func(c *Config) { mailcalOpenSettings(c) }},
		{name: "Curation Hub", icon: "H", action: func(c *Config) { openCurationPanel(c) }},
		{name: "Audio", icon: "A", action: func(c *Config) { openMusicSettingsPanel(c) }},
		{name: "Smart Home", icon: "M", action: func(c *Config) { openSmartHomePanel(c) }},
		{name: "Screen Share", icon: "V", action: func(c *Config) { openCastPanel(c) }},
		// Say what to open (voicecmd.go); the phone's keyboard microphone works
		// too, so the row stays on a device without a microphone.
		{name: "Voice command", icon: "O", action: func(c *Config) { voiceOpen(c) }},
		{name: "Accessibility", icon: "Y", action: func(c *Config) { a11yOpenSettings(c) }},
	}
	if IsTrimuiDevice() || hasPPSSPPInstall(ppssppSearchRoots()) {
		tools = append(tools, startMenuEntry{name: "Stick Calibration", icon: "K", action: func(c *Config) { openStickCalibrationPanel(c) }})
	}
	// Ready-made commands without typing: the Terminal with its Command Deck
	// open (cmddeck.go).
	if config != nil && sceneIndexExact(config, "Terminal") >= 0 {
		tools = append(tools, startMenuEntry{name: "Commands", icon: "C", action: func(c *Config) { cmdDeckOpenFromAnywhere(c, "") }})
	}

	device := []startMenuEntry{
		{name: "Quick Settings", icon: "Q", action: func(*Config) { qsToggleOpen() }},
		{name: "Liquid background", icon: "~", badge: setOnOff(liquidLevelOf(config) != liquidLevelOff), action: liquidToggle},
		{name: "Wi-Fi Networks", icon: "W", action: func(c *Config) { wifiScrOpen(c) }},
		{name: "Screenshot", icon: "S", action: func(*Config) { requestScreenshot() }},
		{name: "Sleep Screen", icon: "Z", action: func(*Config) { activateScreensaver() }},
		{name: "Ambient", icon: "D", action: func(c *Config) { ambientEnter(c) }},
		{name: "Wallpapers", icon: "P", action: func(*Config) { wpOpen = true; initWallpaperPicker() }},
		{name: "Edit Home", icon: "H", action: func(c *Config) { homeEditOpen(c) }},
		// The pairing card: the phone types, pastes keys and sends files
		// (companionui.go). Its code is also under the rail.
		{name: phoneCompanionEntryName, icon: "C", action: func(*Config) { openCompanionOverlay() }},
		// Report a bug (bugrep.go): its Submit sends with no typing at all.
		{name: bugrepEntryName, icon: "B", action: func(c *Config) { bugrepOpenScreen(c) }},
		{name: "About", icon: "i", scene: "About"},
		{name: "Quit JukaHub", icon: "X", action: func(*Config) { requestAppExit() }},
	}
	// A button that cannot do anything on this firmware would only confuse;
	// Quick Settings still reports the backend when it is missing. "Cannot do
	// anything" means no radio to switch either: while the radio is simply off,
	// the picker is the control that turns it on, so the row stays.
	if !IsWindows() && !wifiEntryReachable() {
		device = withoutStartMenuEntry(device, "Wi-Fi Networks")
	}
	// Bluetooth pairing, where bluetoothctl exists (bluetooth_devices_ui.go).
	device = btStartMenuInsert(device)
	// Handoff: send what is on screen to a paired device, which offers it on
	// its Home (hoff.go). Listed first, and only while devices are paired.
	if targets := hoffSendTargets(); len(targets) > 0 {
		sends := make([]startMenuEntry, 0, len(targets)+len(device))
		for _, p := range targets {
			id := p.ID
			sends = append(sends, startMenuEntry{name: "Send to " + p.Name, icon: ">", action: func(c *Config) { hoffSendTo(c, id) }})
		}
		device = append(sends, device...)
	}

	// A config may carry its own scene for something the menu already offers as
	// an action (a hand-made "Emulators" screen, say). The action row is the one
	// that works on every platform, so the scene row steps aside instead of the
	// menu listing the same label twice.
	taken := make(map[string]bool)
	for _, group := range [][]startMenuEntry{games, tools, device} {
		for _, entry := range group {
			taken[entry.name] = true
		}
	}
	scenes := startMenuSceneEntries(config)
	kept := scenes[:0]
	for _, entry := range scenes {
		if taken[entry.name] {
			continue
		}
		kept = append(kept, entry)
	}

	// "Screens", not "Scenes": a scene is the config's word for a screen, and
	// the menu is read by people, not by the config. Plugins lists the
	// buttons of the plugins that are switched on (plughooks.go).
	return []startMenuGroup{
		{name: "Screens", icon: "S", items: kept},
		{name: "Games", icon: "G", items: games},
		{name: "Tools", icon: "T", items: tools},
		{name: "Plugins", icon: "P", items: plugStartMenuEntries()},
		{name: "Device", icon: "D", items: device},
	}
}

// startMenuSceneGlyphs and startMenuActionGlyphs name the vector icon a Start
// menu row draws (icondefs.go). The rows used to print their config "icon" as
// text — "*", "~", ">>", "[D]", ">_", "pkg" — or a single letter, which on a
// handheld read as stray punctuation beside every label. A row with no entry
// here, or a name the icon set does not know, keeps its text mark.
var (
	startMenuSceneGlyphs = map[string]string{
		"Main": "home", "Tube": "media", "IPStream": "stream", "FileExplorer": "files",
		"MusicPlayer": "music", "LiveTV": "tv", "Podcasts": "podcast", "Recordings": "video",
		"Tickers": "chart", "Apps": "apps", "Terminal": "terminal", "Cron": "schedule",
		"NetSpeed": "speed", "Benchmark": "benchmark", "DiskSpace": "disk", "Weather": "weather",
		"Hardware": "hardware", "LogExporter": "logs", "Favorites": "favorites",
		"UnitConverter": "calculator", "Packages": "packages", "Chat": "chat", "Shorts": "video",
		"CanvasSandbox": "palette", "JukaLand": "jukaland", "TextBrowser": "browser",
		"Settings": "settings", "About": "info", "Tools": "tools", "Calculator": "calculator",
		"Pomodoro": "timer", "Alarm": "alarm", "Dashboard": "grid", "PDFReader": "pdf",
		"Notes": "edit", "Games": "games", "Browser": "browser", "Playtime": "trophy",
		"RomManager": "rom", "NetworkTools": "network", "WiFi": "wifi", "Synth": "synth",
	}
	startMenuActionGlyphs = map[string]string{
		"Emulators": "emulator", "Games Library": "games", "Game Profiles": "controller",
		"PS Remote Play": "tv", "RetroAchievements": "trophy", "Save Manager": "saves",
		"Windows Apps": "apps", "PortMaster Ports": "packages", "APK Store": "phone", "Smart Pro Compatibility": "check",
		"Network tools": "network", "Downloads": "download", "Curation Hub": "sparkles", "Commands": "terminal",
		"Audio": "music", "Synth": "synth", "Smart Home": "home", "Screen Share": "tv", "Accessibility": "user",
		"Stick Calibration": "controller", "Quick Settings": "preferences", "Liquid background": "sparkles", "Voice command": "mic",
		"Wi-Fi Networks": "wifi", "Screenshot": "camera", "Sleep Screen": "moon",
		"Ambient": "clock", "Wallpapers": "image", "Edit Home": "edit", "Quit JukaHub": "power",
		bugrepEntryName: "bug", phoneCompanionEntryName: "qr",
		// The rail's groups.
		"Screens": "grid", "Games": "games", "Tools": "tools", "Plugins": "plugin", "Device": "devices",
	}
)

// startMenuGlyph is the icon for a row, or "" to keep its text mark.
func startMenuGlyph(entry startMenuEntry) string {
	if entry.glyph != "" && iconKnown(entry.glyph) {
		return entry.glyph
	}
	name := ""
	if entry.scene != "" {
		name = startMenuSceneGlyphs[entry.scene]
	}
	if name == "" {
		name = startMenuActionGlyphs[entry.name]
	}
	if name == "" && strings.HasPrefix(entry.name, "Send to ") {
		name = "share"
	}
	if name == "" || !iconKnown(name) {
		return ""
	}
	return name
}

// drawStartMenuMark draws a row's icon in its 32px leading column: the vector
// glyph when the row has one, its text mark otherwise.
func drawStartMenuMark(renderer *sdl.Renderer, config *Config, font *ttf.Font, glyph, mark string, x, rowY, rowH int32, ink sdl.Color) {
	if glyph != "" {
		size := int32(22)
		drawHomeGlyphTinted(renderer, glyph, sdl.Rect{X: x, Y: rowY + (rowH-size)/2, W: size, H: size}, ink)
		return
	}
	if font != nil {
		renderText(renderer, config, font, mark, ink, x, rowY+(rowH-int32(font.Height()))/2)
	}
}

// startMenuPower is the menu's power key: X asks to quit JukaHub through the
// Exit screen's confirmation ("Quit JukaHub?" — Back / Quit). Quitting has two
// fast paths now that Home has no Exit pill — this, and the Select + Start
// chord — and the one in a menu asks first, because X sits next to A.
func startMenuPower(config *Config) {
	startMenuDismiss()
	startMenuCloseOverlays()
	if config != nil {
		if idx := sceneIndexExact(config, "Exit"); idx >= 0 {
			PlayActivateSound()
			changeSceneTo(config, idx)
			return
		}
	}
	// A config without the Exit screen still quits, the way the Device
	// group's "Quit JukaHub" row does.
	requestAppExit()
}

// startMenuCloseOverlays drops the modal panels a menu choice replaces.
// Without it, opening the menu from Save Manager and picking another screen (or
// Quit) would stack two modals and leave the old one swallowing input behind
// the new one.
func startMenuCloseOverlays() {
	savePanel = saveManagerPanelState{}
	saveRowHits = nil
	curationPanel = curationPanelState{}
	curationRowHits = nil
}

// withoutStartMenuEntry returns the list without one action, so platform gates
// do not leave a dead row behind.
func withoutStartMenuEntry(entries []startMenuEntry, name string) []startMenuEntry {
	out := entries[:0]
	for _, e := range entries {
		if e.name == name {
			continue
		}
		out = append(out, e)
	}
	return out
}

// getStartMenuApps flattens the groups. The menu itself is grouped, but the
// audits and shortcuts still want one list of everything the menu can reach.
func getStartMenuApps(config *Config) []startMenuEntry {
	var out []startMenuEntry
	seen := make(map[string]bool)
	for _, group := range startMenuGroups(config) {
		for _, entry := range group.items {
			if seen[entry.name] {
				continue
			}
			seen[entry.name] = true
			out = append(out, entry)
		}
	}
	return out
}

// startMenuApps is kept for backward compatibility with shortcuts.go.
var startMenuApps []struct{ name, scene string }

// bottomBarHeight is the height of the bottom strip (the controller-hint
// buttons) for the given scene, so content panels can reserve the margin.
func bottomBarHeight(sceneName string) int32 {
	if screenHeight < 600 {
		return HomeFooterHSmall
	}
	return HomeFooterH
}

// startMenuSize returns the menu card's width and height.
func startMenuSize() (int32, int32) {
	w := startMenuW
	if w > screenWidth-24 {
		w = screenWidth - 24
	}
	menuH := screenHeight - HomeTopBarH - HomeFooterH - 16
	if menuH > 560 {
		menuH = 560
	}
	if menuH < 240 {
		menuH = 240
	}
	return w, menuH
}

// startMenuOrigin anchors the menu under the Apps pill; without a measured
// pill (first frame) it falls back to the top-left corner of the content area.
func startMenuOrigin() (int32, int32) {
	menuW, menuH := startMenuSize()
	x := tb.startRect.X
	if x < 0 {
		x = 0
	}
	if x+menuW > screenWidth-12 {
		x = screenWidth - 12 - menuW
	}
	if x < 12 {
		x = 12
	}
	y := tb.startRect.Y + tb.startRect.H + 8
	if tb.startRect.W <= 0 {
		x, y = 12, HomeTopBarH+8
	}
	if y+menuH > screenHeight-HomeFooterH {
		y = screenHeight - HomeFooterH - menuH
	}
	if y < HomeTopBarH+8 {
		y = HomeTopBarH + 8
	}
	return x, y
}

// startMenuRows returns how many entries fit in the list pane.
func startMenuRows() int {
	_, menuH := startMenuSize()
	rows := int((menuH - startMenuHeadNow - startMenuFootH) / startMenuItemH)
	if rows < 1 {
		rows = 1
	}
	return rows
}

// startMenuGroupRows returns how many groups fit in the rail. The four groups
// always do; the clamp keeps a future fifth group from drawing past the card.
func startMenuGroupRows() int {
	_, menuH := startMenuSize()
	rows := int((menuH - startMenuHeadNow - startMenuFootH) / startMenuGroupH)
	if rows < 1 {
		rows = 1
	}
	return rows
}

// startMenuCurrentGroup returns the highlighted group, clamped to the list.
func startMenuCurrentGroup(config *Config) (startMenuGroup, []startMenuGroup) {
	groups := startMenuGroups(config)
	if len(groups) == 0 {
		return startMenuGroup{}, groups
	}
	if tb.group < 0 {
		tb.group = 0
	}
	if tb.group >= len(groups) {
		tb.group = len(groups) - 1
	}
	return groups[tb.group], groups
}

// startMenuClampScroll keeps the cursor inside the visible window.
func startMenuClampScroll(count int) {
	rows := startMenuRows()
	if tb.cursor < 0 {
		tb.cursor = 0
	}
	if tb.cursor >= count && count > 0 {
		tb.cursor = count - 1
	}
	if tb.cursor < tb.scroll {
		tb.scroll = tb.cursor
	}
	if tb.cursor >= tb.scroll+rows {
		tb.scroll = tb.cursor - rows + 1
	}
	if max := count - rows; tb.scroll > max {
		tb.scroll = max
	}
	if tb.scroll < 0 {
		tb.scroll = 0
	}
}

// renderStartMenu draws the Start menu. The focused group (rail) and the
// focused entry share the highlight, so keys and clicks agree.
func renderStartMenu(renderer *sdl.Renderer, config *Config) {
	startMenuQRRect = sdl.Rect{}
	if !tb.startOpen {
		return
	}

	font, _ := getCachedFont(config, "medium")
	smallFont, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	// The header's subtitle and the footer use the hint size, not the 24px
	// "small" role: at 24px the subtitle ran from the header into the first
	// row (it starts 43px down a 64px band) and the footer line hung 5px past
	// the card's bottom edge.
	captionFont := getFontSized(config, "small", footerHintSize)
	if captionFont == nil {
		captionFont = smallFont
	}

	startMenuHeadNow = startMenuHeadHeight(config)
	menuW, menuH := startMenuSize()
	menuX, menuY := startMenuOrigin()
	group, groups := startMenuCurrentGroup(config)
	if len(groups) == 0 {
		return
	}
	items := group.items
	startMenuClampScroll(len(items))

	// Entrance keyframes: the scrim fades under the menu while the card itself
	// drops from under the Apps pill and fades as one layer. Reduced motion (or
	// a backend without render targets) draws the settled state.
	progress := 1.0
	if uiMotionEnabled(config) {
		progress = tb.enter.progress(uiClockMS())
	} else {
		tb.enter.cancel()
	}
	fade := uiFadeIn.at(progress)
	slide := int32(math.Round(uiMenuDrop.at(progress)))

	// The shared overlay dim (the backdrop's navy), under the header only: the
	// menu hangs from the header's Menu pill, which stays readable above it.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, glassScrimColor(uiScrimProgress(progress, uiMenuEnterMS)))
	renderer.FillRect(&sdl.Rect{X: 0, Y: HomeTopBarH, W: screenWidth, H: screenHeight - HomeTopBarH})

	compositeUILayerIn(renderer, fade, slide, sdl.Rect{X: menuX, Y: menuY, W: menuW, H: menuH}, func() {
		// The shared overlay sheet: Home's glass, made opaque.
		drawOverlayCard(renderer, sdl.Rect{X: menuX, Y: menuY, W: menuW, H: menuH})

		// ── Header ──────────────────────────────────────────────────────
		titleY := menuY + 10
		renderText(renderer, config, font, "Start", ColorTextPrimary(), menuX+20, titleY)
		tw, th, _ := font.SizeUTF8("Start")
		startMenuLayout.title = sdl.Rect{X: menuX + 20, Y: titleY, W: int32(tw), H: int32(th)}
		if captionFont != nil {
			subtitle := "Go to a screen, or run an action"
			subtitleY := menuY + 10 + int32(font.Height()) + 4
			renderText(renderer, config, captionFont, subtitle, ColorTextMuted(), menuX+20, subtitleY)
			sw, sh, _ := captionFont.SizeUTF8(subtitle)
			startMenuLayout.subtitle = sdl.Rect{X: menuX + 20, Y: subtitleY, W: int32(sw), H: int32(sh)}
		}

		// ── Group rail ──────────────────────────────────────────────────
		railX := menuX + 10
		railW := startMenuGroupW
		// A narrow window keeps both panes usable instead of letting the rail
		// eat the list; the handheld canvas is wide enough to never hit this.
		if room := menuW - 20; railW > room*2/5 {
			railW = room * 2 / 5
		}
		if railW < 84 {
			railW = 84
		}
		startMenuGroupHits = startMenuGroupHits[:0]
		groupRows := startMenuGroupRows()
		for i := 0; i < len(groups) && i < groupRows; i++ {
			g := groups[i]
			ry := menuY + startMenuHeadNow + int32(i)*startMenuGroupH
			rect := sdl.Rect{X: railX, Y: ry, W: railW, H: startMenuGroupH - 4}
			startMenuGroupHits = append(startMenuGroupHits, startMenuHit{index: i, rect: rect})

			selected := i == tb.group
			hovered := pointInRect(mouseX, mouseY, rect)
			switch {
			case selected && tb.focus == 0:
				// The cursor: a selected list row (glass, accent wash, ring).
				UIListRow(renderer, rect, 10, true, false)
			case selected:
				// The open group while the cursor is in the list: lit, unringed.
				fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, WithAlpha(glassAccent(), 40))
				strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 1, WithAlpha(glassAccent(), 110))
			case hovered:
				UIListRow(renderer, rect, 10, false, true)
			}
			drawStartMenuMark(renderer, config, font, startMenuGlyph(startMenuEntry{name: g.name}), g.icon,
				rect.X+12, rect.Y, rect.H, ColorTextSecondary())
			renderText(renderer, config, font, ellipsize(font, g.name, rect.W-52),
				ColorTextPrimary(), rect.X+44, rect.Y+(rect.H-int32(font.Height()))/2)
		}
		// The phone's pairing code fills the rail's free space under the
		// groups, so a phone can pair from the menu itself.
		railRows := min(len(groups), groupRows)
		startMenuDrawQR(renderer, config, captionFont, railX, railW,
			menuY+startMenuHeadNow+int32(railRows)*startMenuGroupH+10, menuY+menuH-startMenuFootH-4)

		// A hairline makes the two panes read as panes rather than one grid.
		dividerX := railX + railW + 5
		setDrawColor(renderer, glassHairline())
		renderer.FillRect(&sdl.Rect{X: dividerX, Y: menuY + startMenuHeadNow - 6, W: 1, H: menuH - startMenuHeadNow - startMenuFootH + 4})

		// ── Entries ─────────────────────────────────────────────────────
		listX := dividerX + 13
		listW := menuX + menuW - listX - 18
		startMenuItemHits = startMenuItemHits[:0]
		if len(items) == 0 {
			if smallFont != nil {
				renderText(renderer, config, smallFont, "Nothing in this group yet",
					ColorTextTertiary(), listX, menuY+startMenuHeadNow+12)
			}
		}
		rows := startMenuRows()
		for row := 0; row < rows; row++ {
			i := tb.scroll + row
			if i >= len(items) {
				break
			}
			entry := items[i]
			iy := menuY + startMenuHeadNow + int32(row)*startMenuItemH
			rect := sdl.Rect{X: listX, Y: iy, W: listW, H: startMenuItemH - 4}
			startMenuItemHits = append(startMenuItemHits, startMenuHit{index: i, rect: rect})
			if row == 0 {
				startMenuLayout.firstRow = rect
			}

			focused := tb.focus == 1 && i == tb.cursor
			hovered := pointInRect(mouseX, mouseY, rect)
			if focused || hovered {
				UIListRow(renderer, rect, 10, focused, hovered)
			}
			iconCol := ColorTextSecondary()
			nameCol := ColorTextPrimary()
			if focused {
				iconCol = ColorTextPrimary()
			}
			// The name's room: the 46px icon column on the left, a 10px margin
			// on the right — less the badge and its gap when the row has one.
			textW := rect.W - 56
			if entry.badge != "" && captionFont != nil {
				// Only when the name keeps a usable width beside it: on a menu
				// too narrow for both the name wins and the badge is left out,
				// because ellipsize given no width returns the whole label,
				// which would run under the badge.
				if badgeW, badgeH := measureText(captionFont, entry.badge); textW-badgeW-14 >= 48 {
					badgeInk := ColorTextTertiary()
					if focused {
						badgeInk = ColorTextSecondary()
					}
					renderText(renderer, config, captionFont, entry.badge, badgeInk,
						rect.X+rect.W-14-badgeW, rect.Y+(rect.H-badgeH)/2)
					textW -= badgeW + 14
				}
			}
			label := entry.name
			if _, blocked := offlineSceneBlock(entry.scene); blocked {
				// Dimmed and marked, so the menu says what will not work before
				// the press says it again.
				iconCol, nameCol = ColorTextTertiary(), ColorTextTertiary()
				label = ellipsize(font, entry.name+"  · "+offlineTileTag, textW)
			}
			drawStartMenuMark(renderer, config, font, startMenuGlyph(entry), entry.icon,
				rect.X+12, rect.Y, rect.H, iconCol)
			renderText(renderer, config, font, ellipsize(font, label, textW), nameCol,
				rect.X+46, rect.Y+(rect.H-int32(font.Height()))/2)
		}

		// ── Footer ─────────────────────────────────────────────────────
		// The power key stays on the right: X asks to quit.
		if captionFont != nil {
			footTop := menuY + menuH - startMenuFootH
			startMenuLayout.footer = sdl.Rect{X: menuX, Y: footTop, W: menuW, H: startMenuFootH}
			textY := footTop + (startMenuFootH-int32(captionFont.Height()))/2
			const powerKey, powerLabel = "X", "Quit JukaHub"
			badgeH := footerBadgeHeight(captionFont)
			badgeW := footerBadgeWidth(captionFont, hintKeyFor(powerKey, powerLabel), 6, badgeH)
			labelW, _ := measureText(captionFont, powerLabel)
			badgeX := menuX + menuW - 20 - labelW - 7 - badgeW
			badge := sdl.Rect{X: badgeX, Y: footTop + (startMenuFootH-badgeH)/2, W: badgeW, H: badgeH}
			drawGlassHintBadge(renderer, config, captionFont, badge, powerKey, powerLabel, badge.X+badgeW+7, textY)
		}

		// Scrollbar when the list is longer than the pane.
		if len(items) > rows {
			thumbFrac := float64(rows) / float64(len(items))
			scrollFrac := 0.0
			if len(items) > rows {
				scrollFrac = float64(tb.scroll) / float64(len(items)-rows)
			}
			drawScrollbar(renderer, menuX+menuW-6, menuY+startMenuHeadNow, 3,
				menuH-startMenuHeadNow-startMenuFootH, thumbFrac, scrollFrac)
		}
	})
}

// startMenuIsPointIn tests if a click is inside the Start menu card.
func startMenuIsPointIn(x, y int32) bool {
	if !tb.startOpen {
		return false
	}
	menuW, menuH := startMenuSize()
	menuX, menuY := startMenuOrigin()
	return x >= menuX && x <= menuX+menuW && y >= menuY && y <= menuY+menuH
}

// startMenuHandleClick selects a group or activates an entry.
func startMenuHandleClick(x, y int32, config *Config) {
	if !tb.startOpen {
		return
	}
	// The pairing code under the rail: shown full screen, brighter, with the
	// PIN and address for a phone that cannot scan.
	if startMenuQRRect.W > 0 && pointInRect(x, y, startMenuQRRect) {
		startMenuDismiss()
		homeShowPairingQR()
		return
	}
	for _, hit := range startMenuGroupHits {
		if pointInRect(x, y, hit.rect) {
			tb.group = hit.index
			tb.focus = 1
			tb.cursor = 0
			tb.scroll = 0
			PlayNavSound()
			return
		}
	}
	for _, hit := range startMenuItemHits {
		if pointInRect(x, y, hit.rect) {
			tb.focus = 1
			tb.cursor = hit.index
			startMenuActivate(config)
			return
		}
	}
}

// startMenuToggle toggles the Start menu open/closed.
func startMenuToggle() {
	tb.startOpen = !tb.startOpen
	if tb.startOpen {
		trophyEvent(tkStartMenu)
		tb.group, tb.cursor, tb.scroll, tb.focus = 0, 0, 0, 1
		tb.enter.begin(uiClockMS(), uiMenuEnterMS)
		// The top-bar overlays are exclusive: a dropdown behind the menu
		// would swallow the D-pad as soon as the menu closed.
		nc.open = false
		homeCalendarOpen = false
		dismissWeatherPopover()
		dismissVolumePopover()
		dismissBrightnessPopover()
	}
	PlayActivateSound()
}

// startMenuDismiss closes the Start menu.
func startMenuDismiss() {
	tb.startOpen = false
}

// startMenuNavigate moves the focused entry inside the current group.
func startMenuNavigate(d int, config *Config) {
	if !tb.startOpen {
		return
	}
	group, _ := startMenuCurrentGroup(config)
	if len(group.items) == 0 {
		return
	}
	next := clampIndex(tb.cursor+d, len(group.items))
	if next != tb.cursor {
		tb.cursor = next
		PlayNavSound()
	}
	startMenuClampScroll(len(group.items))
}

// startMenuMoveGroup moves the rail selection and resets the entry cursor.
func startMenuMoveGroup(d int, config *Config) {
	if !tb.startOpen {
		return
	}
	groups := startMenuGroups(config)
	if len(groups) == 0 {
		return
	}
	next := tb.group + d
	if next < 0 {
		next = 0
	}
	if next >= len(groups) {
		next = len(groups) - 1
	}
	if next != tb.group {
		tb.group = next
		tb.cursor, tb.scroll = 0, 0
		PlayNavSound()
	}
}

// startMenuFocusRail / startMenuFocusItems switch panes for Left/Right.
func startMenuFocusRail() {
	if tb.focus != 0 {
		tb.focus = 0
		PlayNavSound()
	}
}

func startMenuFocusItems(config *Config) {
	group, _ := startMenuCurrentGroup(config)
	if len(group.items) == 0 {
		return
	}
	if tb.focus != 1 {
		tb.focus = 1
		tb.cursor, tb.scroll = 0, 0
		PlayNavSound()
	}
}

// startMenuActivate opens the focused entry, or steps into the list when the
// rail holds the focus.
func startMenuActivate(config *Config) {
	if tb.focus == 0 {
		startMenuFocusItems(config)
		return
	}
	group, _ := startMenuCurrentGroup(config)
	if tb.cursor < 0 || tb.cursor >= len(group.items) {
		startMenuDismiss()
		return
	}
	app := group.items[tb.cursor]
	recentRef := homeConsoleMenuRef(app)
	tb.startOpen = false
	startMenuCloseOverlays()
	if app.action != nil {
		if recentRef != "" {
			homeConsoleRecordApp(recentRef)
		}
		app.action(config)
	} else if app.scene == toolsHubSceneName {
		// The hub opens at its top through its own entry point, like the Home
		// card does.
		if recentRef != "" {
			homeConsoleRecordApp(recentRef)
		}
		toolsHubOpen(config)
	} else if app.scene != "" {
		// The exact name: findSceneIndex falls back to Home for a scene the
		// config lost, which read as the menu doing nothing.
		if idx := sceneIndexExact(config, app.scene); idx >= 0 {
			// The row is dimmed already; pressing it names the missing link
			// instead of opening a screen that would fail on its first fetch.
			if reason, blocked := offlineSceneBlock(app.scene); blocked {
				showToast(reason, ToastWarn())
				return
			}
			if recentRef != "" {
				homeConsoleRecordApp(recentRef)
			}
			changeSceneTo(config, idx)
		}
	}
	PlayActivateSound()
}

// sceneClaimsStart lists the scenes whose own controller handler already uses
// Start (pause menu, replay, JukaLand's craft menu, the Terminal's Run). Start
// must not hijack those: JukaLand's footer offers "Start Craft", but the menu
// took the press.
func sceneClaimsStart(name string) bool {
	switch name {
	case "Games", "Ambient", "JukaLand", "Terminal", synthSceneName:
		// The Synth's Start plays and stops the pattern (synth.go).
		return true
	}
	return false
}

// startMenuKeyOpen is F10, the keyboard's Start: it opens the Start menu
// wherever the pad's Start would (handleControllerEvent), so the "F10 Menu"
// the keyboard hints show is always true. The arcade, the dock and JukaLand
// keep their screen; the Terminal's Start is its Run button, which a keyboard
// presses as Enter, so F10 still opens the menu there. While the menu is open,
// handleChromeOverlayKey owns F10 and closes it.
func startMenuKeyOpen(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || e.Repeat != 0 || e.Keysym.Sym != sdl.K_F10 {
		return false
	}
	if tb.startOpen || virtualKeyboardActive || modalPanelBlocksShoulders() {
		return false
	}
	if config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		// The Terminal's and the Synth's own Start is a key of their own
		// (Enter, Space), so F10 still opens the menu there.
		if name := config.Scenes[currentSceneIndex].Name; name != "Terminal" && name != synthSceneName && sceneClaimsStart(name) {
			return false
		}
	}
	startMenuToggle()
	return true
}

// ctrlStartMenu hands the pad to the open Start menu: D-pad walks the current
// group, Left/Right hop to the rail and back, shoulders page a long group and
// A/B behave like Enter/Escape. Everything is consumed so the scene underneath
// never sees a press through the menu.
func ctrlStartMenu(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if tb.focus == 0 {
			startMenuMoveGroup(-1, config)
		} else {
			startMenuNavigate(-1, config)
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if tb.focus == 0 {
			startMenuMoveGroup(1, config)
		} else {
			startMenuNavigate(1, config)
		}
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		startMenuFocusRail()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		startMenuFocusItems(config)
	case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_Y:
		startMenuActivate(config)
	case sdl.CONTROLLER_BUTTON_X:
		// The menu's power key (its footer names it): the Exit screen asks
		// before anything quits.
		startMenuPower(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		startMenuDismiss()
		PlayBackSound()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		startMenuShoulder(padL1, config)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		startMenuShoulder(padR1, config)
	}
	return true
}
