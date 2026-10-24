package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
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

// startMenuEntry is a single item in a group: a scene to open or an action.
type startMenuEntry struct {
	name   string
	icon   string
	scene  string
	action func(*Config)
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

// startMenuSceneEntries lists the config's own scenes in the order people
// expect them: the main screens first, then everything else. Hidden, utility
// and maintenance scenes stay out; About is offered by the Device group so it
// is not listed twice.
func startMenuSceneEntries(config *Config) []startMenuEntry {
	if config == nil {
		return nil
	}
	preferOrder := []string{
		"Main", "Tube", "LiveTV", "FileExplorer", "Chat", "Calculator",
		"Pomodoro", "Alarm", "Dashboard", "Cron", "Settings",
		"JukaLand", "CanvasSandbox", "Favorites", "Terminal",
		"Dictionary", "PDFReader", "MusicPlayer", "Notes",
		"Games", "Browser", "TextBrowser", "Apps", "Packages", "Shorts",
	}
	seen := make(map[string]bool)
	var entries []startMenuEntry
	add := func(sc SceneConfig) {
		if sc.Icon == "" || sc.Name == "Exit" || sc.Name == "Patch" || sc.Name == "About" {
			return
		}
		entries = append(entries, startMenuEntry{
			name:  sceneDisplayName(sc.Name),
			icon:  sc.Icon,
			scene: sc.Name,
		})
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
	games := []startMenuEntry{
		{name: "Emulators", icon: "E", action: func(*Config) { openEmulatorPanel() }},
		{name: gamesLibraryEntryName, icon: "L", action: func(*Config) { openGameLibraryPanel() }},
		{name: gameProfilesEntryName, icon: "G", action: func(*Config) { openGameProfilePanel(false) }},
		{name: "PS Remote Play", icon: "C", action: func(*Config) { openChiakiPanel() }},
		{name: "RetroAchievements", icon: "R", action: func(*Config) { openRetroAchievementsPanel() }},
		{name: "Save Manager", icon: "S", action: func(*Config) { openSaveManagerPanel() }},
	}
	if !IsWindows() {
		games = append(games,
			startMenuEntry{name: windowsAppsEntryName, icon: "W", action: func(*Config) { openWindowsAppsPanel(false) }},
			startMenuEntry{name: portMasterEntryName, icon: "P", action: func(*Config) { openPortsPanel(false) }},
		)
	}
	// RetroAchievements is only meaningful where a card can hold credentials.
	if !retroAchievementsSupported() {
		games = withoutStartMenuEntry(games, "RetroAchievements")
	}
	if p := compatEntryName; p != "" {
		games = append(games, startMenuEntry{name: p, icon: "T", action: func(*Config) { openCompatPanel(true) }})
	}

	tools := []startMenuEntry{
		{name: "Downloads", icon: "D", action: func(*Config) { openDownloadsPanel() }},
		{name: "Curation Hub", icon: "H", action: func(c *Config) { openCurationPanel(c) }},
		{name: "Audio", icon: "A", action: func(c *Config) { openMusicSettingsPanel(c) }},
		{name: "Smart Home", icon: "M", action: func(c *Config) { openSmartHomePanel(c) }},
		{name: "Remote Cast", icon: "V", action: func(c *Config) { openCastPanel(c) }},
		{name: "Accessibility", icon: "Y", action: func(c *Config) { openAccessibilityPanel(c) }},
	}
	if IsTrimuiDevice() || hasPPSSPPInstall(ppssppSearchRoots()) {
		tools = append(tools, startMenuEntry{name: "Stick Calibration", icon: "K", action: func(c *Config) { openStickCalibrationPanel(c) }})
	}

	device := []startMenuEntry{
		{name: "Quick Settings", icon: "Q", action: func(*Config) { qsToggleOpen() }},
		{name: "Wi-Fi Networks", icon: "N", action: func(*Config) { openWiFiPanel() }},
		{name: "Screenshot", icon: "S", action: func(*Config) { requestScreenshot() }},
		{name: "Sleep Screen", icon: "Z", action: func(*Config) { activateScreensaver() }},
		{name: "Ambient", icon: "D", action: func(c *Config) { ambientEnter(c) }},
		{name: "Wallpapers", icon: "P", action: func(*Config) { wpOpen = true; initWallpaperPicker() }},
		{name: "About", icon: "i", scene: "About"},
		{name: "Quit JukaHub", icon: "X", action: func(*Config) { requestAppExit() }},
	}
	// A button that cannot do anything on this firmware would only confuse;
	// Quick Settings still reports the backend when it is missing.
	if !IsWindows() && !wifiControlAvailable() {
		device = withoutStartMenuEntry(device, "Wi-Fi Networks")
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

	return []startMenuGroup{
		{name: "Scenes", icon: "S", items: kept},
		{name: "Games", icon: "G", items: games},
		{name: "Tools", icon: "T", items: tools},
		{name: "Device", icon: "D", items: device},
	}
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
	rows := int((menuH - startMenuHeadH - startMenuFootH) / startMenuItemH)
	if rows < 1 {
		rows = 1
	}
	return rows
}

// startMenuGroupRows returns how many groups fit in the rail. The four groups
// always do; the clamp keeps a future fifth group from drawing past the card.
func startMenuGroupRows() int {
	_, menuH := startMenuSize()
	rows := int((menuH - startMenuHeadH - startMenuFootH) / startMenuGroupH)
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
	if !tb.startOpen {
		return
	}

	font, _ := getCachedFont(config, "medium")
	smallFont, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

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

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, uint8(140*uiScrimProgress(progress, uiMenuEnterMS)))
	renderer.FillRect(&sdl.Rect{X: 0, Y: HomeTopBarH, W: screenWidth, H: screenHeight - HomeTopBarH})

	compositeUILayerIn(renderer, fade, slide, sdl.Rect{X: menuX, Y: menuY, W: menuW, H: menuH}, func() {
		UICard(renderer, sdl.Rect{X: menuX, Y: menuY, W: menuW, H: menuH}, UICardStyle{
			Fill:        ColorSurfaceAlt,
			Border:      ColorBorder,
			Radius:      12,
			BorderWidth: 1,
		})
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 90)
		renderer.FillRect(&sdl.Rect{X: menuX, Y: menuY, W: 3, H: menuH})

		// ── Header ──────────────────────────────────────────────────────
		renderText(renderer, config, font, "Start", ColorTextPrimary(), menuX+20, menuY+12)
		if smallFont != nil {
			renderText(renderer, config, smallFont, "Switch scenes or run an action",
				ColorTextTertiary(), menuX+20, menuY+12+int32(font.Height())+2)
			count := fmt.Sprintf("%d in this group", len(items))
			cw, _, _ := smallFont.SizeUTF8(count)
			renderText(renderer, config, smallFont, count, ColorTextTertiary(),
				menuX+menuW-int32(cw)-16, menuY+16)
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
			ry := menuY + startMenuHeadH + int32(i)*startMenuGroupH
			rect := sdl.Rect{X: railX, Y: ry, W: railW, H: startMenuGroupH - 4}
			startMenuGroupHits = append(startMenuGroupHits, startMenuHit{index: i, rect: rect})

			selected := i == tb.group
			hovered := pointInRect(mouseX, mouseY, rect)
			if selected || hovered {
				fill := WithAlpha(ColorSurfaceRaised, 180)
				if selected {
					fill = WithAlpha(accentColor, 70)
				}
				fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, fill)
			}
			if selected && tb.focus == 0 {
				strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, 2, accentColor)
			}
			renderText(renderer, config, font, g.icon, ColorTextSecondary(), rect.X+12, rect.Y+8)
			renderText(renderer, config, font, ellipsize(font, g.name, rect.W-52),
				ColorTextPrimary(), rect.X+44, rect.Y+8)
		}

		// A hairline makes the two panes read as panes rather than one grid.
		dividerX := railX + railW + 5
		renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 120)
		renderer.FillRect(&sdl.Rect{X: dividerX, Y: menuY + startMenuHeadH - 6, W: 1, H: menuH - startMenuHeadH - startMenuFootH + 4})

		// ── Entries ─────────────────────────────────────────────────────
		listX := dividerX + 13
		listW := menuX + menuW - listX - 18
		startMenuItemHits = startMenuItemHits[:0]
		if len(items) == 0 {
			if smallFont != nil {
				renderText(renderer, config, smallFont, "Nothing in this group yet",
					ColorTextTertiary(), listX, menuY+startMenuHeadH+12)
			}
		}
		rows := startMenuRows()
		for row := 0; row < rows; row++ {
			i := tb.scroll + row
			if i >= len(items) {
				break
			}
			entry := items[i]
			iy := menuY + startMenuHeadH + int32(row)*startMenuItemH
			rect := sdl.Rect{X: listX, Y: iy, W: listW, H: startMenuItemH - 4}
			startMenuItemHits = append(startMenuItemHits, startMenuHit{index: i, rect: rect})

			focused := tb.focus == 1 && i == tb.cursor
			hovered := pointInRect(mouseX, mouseY, rect)
			if focused || hovered {
				fill := WithAlpha(ColorSurfaceRaised, 180)
				if focused {
					fill = WithAlpha(accentColor, 70)
				}
				fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, fill)
			}
			iconCol := ColorTextSecondary()
			nameCol := ColorTextPrimary()
			if focused {
				iconCol = ColorTextPrimary()
			}
			renderText(renderer, config, font, entry.icon, iconCol, rect.X+12, rect.Y+6)
			renderText(renderer, config, font, ellipsize(font, entry.name, rect.W-56), nameCol, rect.X+46, rect.Y+6)
		}

		// ── Footer ─────────────────────────────────────────────────────
		if smallFont != nil {
			ver := versionDisplayString(config)
			renderText(renderer, config, smallFont, ver, ColorTextTertiary(),
				menuX+20, menuY+menuH-24)
			if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
				sceneName := sceneDisplayName(config.Scenes[currentSceneIndex].Name)
				sw, _, _ := smallFont.SizeUTF8(sceneName)
				renderText(renderer, config, smallFont, sceneName, ColorTextTertiary(),
					menuX+menuW-int32(sw)-20, menuY+menuH-24)
			}
		}

		// Scrollbar when the list is longer than the pane.
		if len(items) > rows {
			thumbFrac := float64(rows) / float64(len(items))
			scrollFrac := 0.0
			if len(items) > rows {
				scrollFrac = float64(tb.scroll) / float64(len(items)-rows)
			}
			drawScrollbar(renderer, menuX+menuW-6, menuY+startMenuHeadH, 3,
				menuH-startMenuHeadH-startMenuFootH, thumbFrac, scrollFrac)
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
		tb.group, tb.cursor, tb.scroll, tb.focus = 0, 0, 0, 1
		tb.enter.begin(uiClockMS(), uiMenuEnterMS)
		// The top-bar overlays are exclusive: a dropdown behind the menu
		// would swallow the D-pad as soon as the menu closed.
		nc.open = false
		homeCalendarOpen = false
		dismissWeatherPopover()
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
	tb.startOpen = false
	// A menu choice replaces whatever overlay was open. Without this, opening
	// the menu from Save Manager and picking another app would stack two
	// modals and leave the old one swallowing input behind the new one.
	savePanel = saveManagerPanelState{}
	saveRowHits = nil
	curationPanel = curationPanelState{}
	curationRowHits = nil
	if app.action != nil {
		app.action(config)
	} else if app.scene != "" {
		if idx := findSceneIndex(config, app.scene); idx >= 0 {
			changeSceneTo(config, idx)
		}
	}
	PlayActivateSound()
}

// sceneClaimsStart lists the scenes whose own controller handler already uses
// Start (pause menu, replay). Start must not hijack those.
func sceneClaimsStart(name string) bool {
	switch name {
	case "Games", "Ambient":
		return true
	}
	return false
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
