package main

// quickmenu.go — the Quick menu on the TrimUI button.
//
// It is a full-screen menu of a few big tiles — Recents, Collections
// (only when there are any), Games, Tools — over a row of small round buttons:
// Wi-Fi, Sleep, Reboot, Power off, Settings. The D-pad moves, A opens or acts,
// B (or the same button again) closes. Themes give every entry a full-screen
// picture, and with the theme's "hide UI" only the picture shows.
//
// JukaHub had a screen for each entry already; the Quick menu is one place
// that reaches them, never a second copy:
//
//   - Recents is the Game switcher (gameswitcher.go); the tile is only there
//     while the switcher is on.
//   - Collections is the Tabs Home's Collections tab (hometabs_collections.go):
//     Home comes up on that tab. Only while the stored Home is Tabs, the tab is
//     shown, and at least one collection of the user's own exists.
//   - Games is the games library panel, Tools the Tools hub (toolshub.go),
//     Settings the Settings screen.
//   - Wi-Fi is Home's Wi-Fi quick tile (home_quicktiles.go hqWifiActivate): the
//     same switch, the same "Turning on…" state, the Wi-Fi page where nothing
//     can switch the radio.
//   - Sleep is the power card's Sleep (devSleepNow). Restart and Power off are
//     the power card's, behind a question while Settings → Power & battery →
//     "Ask before power off" is on (power_options.go; Restart follows the same
//     switch here because one press on a small button must not reboot by
//     accident). A computer has none of the three: an "Exit" button there
//     opens the power card (Cancel / Exit).
//
// Screens it opens sit on top of it: B from the Tools hub, Settings, the game
// switcher or the library comes back to the Quick menu.
// Collections is the exception — it is Home itself.
//
// Ways in: Start → Device → Quick menu (so also a Home tile, Home search and
// collections, which list the Start menu's rows), the control-profile action
// "quick_menu" (inmap_actions.go), the Brick's F1 / F2 keys and the Smart Pro
// S's Home key (frontkeys*.go), Settings → Power & battery → Start in → Quick
// menu (startup_target.go), and Settings → Controls → "Menu button on Home":
// Quick menu, which makes a tap of Menu on a bare Home open it (and close it
// again) while holding Menu still talks to Juka (voiceorb.go).
//
// Pictures: quickmenu_bg.go. Drawing: quickmenu_ui.go.

import (
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// quickMenuSceneName is the in-memory scene; it has no Icon, so the Start
	// menu's Screens list leaves it out and the Device row is the way there.
	quickMenuSceneName = "Quick menu"
	quickMenuEntryName = "Quick menu"

	// quickMenuHomeButtonKey is Settings → Controls → Menu button on Home.
	quickMenuHomeButtonKey = "quickMenuHomeButton"
	quickMenuHomeVoice     = "voice"
	quickMenuHomeQuick     = "quickmenu"
	// quickMenuBgOnlyKey is Settings → Controls → Quick menu: Backgrounds only.
	quickMenuBgOnlyKey = "quickMenuBgOnly"

	// quickMenuCollectionsWait bounds the wait for Home to come up before its
	// Collections tab is chosen.
	quickMenuCollectionsWait = 3 * time.Second
)

// quickMenuItemID names an entry.
type quickMenuItemID int

const (
	qmRecents quickMenuItemID = iota
	qmCollections
	qmGames
	qmTools
	qmWifi
	qmSleep
	qmRestart
	qmPowerOff
	qmExit
	qmSettings
)

// quickMenuItem is one tile or button. theme is the theme picture's name:
// .media/quick_<theme>.png (quickmenu_bg.go).
type quickMenuItem struct {
	id    quickMenuItemID
	label string
	glyph string
	theme string
}

// quickMenu is the screen's state. Render/event thread only.
var quickMenu struct {
	visit  tbxVisit
	top    []quickMenuItem
	bottom []quickMenuItem
	// row is 0 for the tiles, 1 for the buttons; col the entry in that row.
	row, col int
	// focusID is the entry focused last, kept across visits so B from a
	// screen the menu opened comes back to the entry that opened it.
	focusID  quickMenuItemID
	focusSet bool
}

// quickMenuCollectionsTab is a Collections press waiting for Home. Main thread.
var quickMenuCollectionsTab struct {
	pending  bool
	deadline time.Time
}

// ── Settings ──────────────────────────────────────────────────────────────

// quickMenuHomeButtonOn reports whether a Menu tap on Home opens the Quick
// menu (instead of the voice orb, the default).
func quickMenuHomeButtonOn(c *Config) bool {
	if c == nil {
		return false
	}
	v, _ := customString(c.Variables.Custom, quickMenuHomeButtonKey)
	return strings.TrimSpace(v) == quickMenuHomeQuick
}

// quickMenuBgOnly reports "Backgrounds only": a themed entry shows its picture
// and a small label, no tiles. Off by default.
func quickMenuBgOnly(c *Config) bool {
	if c == nil {
		return false
	}
	return customBool(c.Variables.Custom, quickMenuBgOnlyKey, false)
}

// quickMenuHomeButtonOptions are the row's stops.
func quickMenuHomeButtonOptions() []setOption {
	return []setOption{
		{Value: quickMenuHomeVoice, Label: "Voice orb", Note: "A tap of Menu talks to Juka"},
		{Value: quickMenuHomeQuick, Label: "Quick menu", Note: "Recents, Games, Tools, Wi-Fi and power"},
	}
}

// quickMenuSettingsRows are Settings → Controls' two rows, under "Menu
// button: voice" (stickcal.go).
func quickMenuSettingsRows() []setRow {
	return []setRow{
		{
			ID: quickMenuHomeButtonKey, Label: "Menu button on Home", Kind: setKindChoice,
			Detail: func(c *Config) string {
				if !quickMenuHomeButtonOn(c) {
					return "A tap of Menu on Home opens the voice orb"
				}
				if voiceOrbMenuEnabled(c) {
					return "A tap of Menu on Home opens the Quick menu; holding it still talks to Juka"
				}
				return "A tap of Menu on Home opens the Quick menu, and closes it again"
			},
			Visible: func(*Config) bool { return P().IsHandheld() },
			Options: func(*Config) []setOption { return quickMenuHomeButtonOptions() },
			Current: func(c *Config) int {
				if quickMenuHomeButtonOn(c) {
					return 1
				}
				return 0
			},
			Choose: func(c *Config, value string) {
				setStoreString(c, quickMenuHomeButtonKey, value)
				label := "Voice orb"
				if value == quickMenuHomeQuick {
					label = "Quick menu"
				}
				setToast("Menu button on Home: " + label)
			},
		},
		{
			ID: quickMenuBgOnlyKey, Label: "Quick menu: Backgrounds only", Kind: setKindToggle,
			Detail: setStatic("With a theme's pictures (.media/quick_Games.png …) show only the picture and a small label"),
			Get:    quickMenuBgOnly,
			Set: func(c *Config, on bool) {
				setStoreBool(c, quickMenuBgOnlyKey, on)
				setToast("Quick menu backgrounds only: " + setOnOff(on))
			},
		},
	}
}

// quickMenuLegendRow is the Controller card's row (controlslegend.go) while a
// Menu tap on Home opens the Quick menu.
func quickMenuLegendRow() (FooterHint, bool) {
	if !P().IsHandheld() || !quickMenuHomeButtonOn(appConfig) {
		return FooterHint{}, false
	}
	return FooterHint{Button: "Menu", Label: "Quick menu (tap, on Home)"}, true
}

// ── Ways in ───────────────────────────────────────────────────────────────

// quickMenuEnsureScene gives the screen its scene in memory, like the game
// switcher's (gswEnsureScene); the user's config is never rewritten for it.
func quickMenuEnsureScene(config *Config) {
	if config == nil {
		return
	}
	if sceneIndexExact(config, quickMenuSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: quickMenuSceneName})
	}
	for i := range config.Scenes {
		if config.Scenes[i].Name == quickMenuSceneName {
			config.Scenes[i].Elements = nil
			config.Scenes[i].Layout = ""
		}
	}
}

// quickMenuOnScreen reports whether the Quick menu is the scene on show.
func quickMenuOnScreen(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == quickMenuSceneName
}

// quickMenuOpen opens the Quick menu from anywhere: the panels that float over
// the scenes (Quick Settings, the Start menu, search, the library) close, or
// they would cover it. Other sessions' openers (start-up target, key pickers)
// call this. Main thread.
func quickMenuOpen() {
	config := appConfig
	if config == nil {
		return
	}
	quickMenuEnsureScene(config)
	idx := sceneIndexExact(config, quickMenuSceneName)
	if idx < 0 {
		return
	}
	if qs.open {
		qsToggleOpen() // saves an edit left open (qs_edit.go)
	}
	startMenuDismiss()
	if globalSearchOpen {
		homeSearchClose()
	}
	closeGameLibraryPanel()
	if idx == currentSceneIndex {
		return
	}
	changeSceneTo(config, idx)
}

// quickMenuToggle opens the Quick menu, or closes it when it is on screen
// (the same key again). Main thread.
func quickMenuToggle() {
	if quickMenuOnScreen(appConfig) {
		if tbxSheetVisible(appConfig) {
			tbxCloseSheet()
		}
		goBackScene(appConfig)
		return
	}
	quickMenuOpen()
}

// inmapActQuickMenu is the control-profile action "quick_menu"
// (inmap_actions.go).
func inmapActQuickMenu(*Config, float64) bool {
	PlayActivateSound()
	quickMenuToggle()
	return true
}

// quickMenuOnBareHome reports Home on screen with nothing over it: no Start
// menu, Quick Settings, search, library, notifications, keyboard, Edit Home,
// power card or orb, and no scene change running.
func quickMenuOnBareHome(c *Config) bool {
	if c == nil || !homeLayoutActive || homeLayout == nil || transitionPhase != "none" ||
		currentSceneIndex != homeSceneIndex(c) {
		return false
	}
	return !tb.startOpen && !qs.open && !globalSearchOpen && !gameLib.open && !nc.open &&
		!virtualKeyboardActive && !homeEdit.open && !homePowerConfirm.open && !voiceOrbActive()
}

// quickMenuBare reports the Quick menu on screen with nothing over it.
func quickMenuBare(c *Config) bool {
	return quickMenuOnScreen(c) && transitionPhase == "none" && !tbxSheetVisible(c) &&
		!tb.startOpen && !qs.open && !gameLib.open && !virtualKeyboardActive && !voiceOrbActive()
}

// quickMenuOwnsGuide reports whether a Menu press is read for the Quick menu
// (voiceorb.go voiceOrbController / voiceOrbMenuDown): a handheld with "Menu
// button on Home" set to Quick menu, on a bare Home or the bare Quick menu.
// With the voice switch off this is what keeps Menu from going Back there.
func quickMenuOwnsGuide(c *Config, which sdl.JoystickID) bool {
	if !P().IsHandheld() || !quickMenuHomeButtonOn(c) || brwGameActive() || !voiceOrbPadHasMenu(which) {
		return false
	}
	return quickMenuOnBareHome(c) || quickMenuBare(c)
}

// quickMenuFromMenuTap is a Menu tap's release (voiceorb.go voiceOrbMenuUp):
// on a bare Home with the setting on it opens the Quick menu, on the bare
// Quick menu it closes it. false leaves the tap to what it did before (the
// orb, or Back).
func quickMenuFromMenuTap(c *Config) bool {
	if !P().IsHandheld() || !quickMenuHomeButtonOn(c) {
		return false
	}
	switch {
	case quickMenuBare(c):
		quickMenuToggle()
		return true
	case quickMenuOnBareHome(c) && voiceOrbBlocked(c) == "":
		PlayActivateSound()
		quickMenuOpen()
		return true
	}
	return false
}

// ── Entries ───────────────────────────────────────────────────────────────

// quickMenuHasCollections reports whether the Collections tile has somewhere
// to go: the stored Home is Tabs, its Collections tab is shown, and the user
// has made at least one collection.
func quickMenuHasCollections(c *Config) bool {
	if c == nil || !homeTabsOnTabs(c) || homeTabIndexIn(homeTabsVisible(c), homeTabCollections) < 0 {
		return false
	}
	return len(homeTabsColList()) > 0
}

// quickMenuBuild lays out this visit's entries. Render thread, once a visit.
func quickMenuBuild(c *Config) {
	top := make([]quickMenuItem, 0, 4)
	if gameSwitcherEnabled(c) {
		top = append(top, quickMenuItem{id: qmRecents, label: "Recents", glyph: homeTabsGlyph("recent", "continue", "clock"), theme: "Recents"})
	}
	if quickMenuHasCollections(c) {
		top = append(top, quickMenuItem{id: qmCollections, label: "Collections", glyph: homeTabsGlyph("folder"), theme: "Collections"})
	}
	top = append(top,
		quickMenuItem{id: qmGames, label: "Games", glyph: homeTabsGlyph("games", "gamepad"), theme: "Games"},
		quickMenuItem{id: qmTools, label: "Tools", glyph: homeTabsGlyph("tools"), theme: "Tools"},
	)
	bottom := make([]quickMenuItem, 0, 5)
	if hqAvailable(hqWifiName) {
		bottom = append(bottom, quickMenuItem{id: qmWifi, label: "Wi-Fi", glyph: "wifi", theme: "Wifi"})
	}
	if devPowerSupported() {
		bottom = append(bottom,
			quickMenuItem{id: qmSleep, label: "Sleep", glyph: homeTabsGlyph("moon"), theme: "Sleep"},
			quickMenuItem{id: qmRestart, label: "Restart", glyph: homeTabsGlyph("refresh"), theme: "Reboot"},
			quickMenuItem{id: qmPowerOff, label: "Power off", glyph: homeTabsGlyph("power"), theme: "Poweroff"},
		)
	} else {
		bottom = append(bottom, quickMenuItem{id: qmExit, label: "Exit", glyph: homeTabsGlyph("power"), theme: "Poweroff"})
	}
	bottom = append(bottom, quickMenuItem{id: qmSettings, label: "Settings", glyph: homeTabsGlyph("settings"), theme: "Settings"})
	quickMenu.top, quickMenu.bottom = top, bottom
	quickMenu.row, quickMenu.col = 0, 0
	if quickMenu.focusSet {
		for r, list := range [2][]quickMenuItem{top, bottom} {
			for i, it := range list {
				if it.id == quickMenu.focusID {
					quickMenu.row, quickMenu.col = r, i
				}
			}
		}
	}
}

// quickMenuRowItems is one row's entries.
func quickMenuRowItems(row int) []quickMenuItem {
	if row == 1 {
		return quickMenu.bottom
	}
	return quickMenu.top
}

// quickMenuFocused is the entry under the focus.
func quickMenuFocused() (quickMenuItem, bool) {
	items := quickMenuRowItems(quickMenu.row)
	if quickMenu.col < 0 || quickMenu.col >= len(items) {
		return quickMenuItem{}, false
	}
	return items[quickMenu.col], true
}

// quickMenuEnter starts a visit. Render thread.
func quickMenuEnter(c *Config) {
	quickMenuBuild(c)
	quickMenuBgStart()
}

// ── Input ─────────────────────────────────────────────────────────────────

// quickMenuAction is one press on the screen (tbxDispatch: the open question
// first; an unused B leaves). Every D-pad press is the menu's own, so Up never
// walks onto the top bar.
func quickMenuAction(config *Config, act tbxAction) bool {
	switch act {
	case tbxActLeft:
		quickMenuMove(quickMenu.col - 1)
	case tbxActRight:
		quickMenuMove(quickMenu.col + 1)
	case tbxActPageUp, tbxActHome:
		quickMenuMove(0)
	case tbxActPageDown, tbxActEnd:
		quickMenuMove(len(quickMenuRowItems(quickMenu.row)) - 1)
	case tbxActUp:
		quickMenuChangeRow(0)
	case tbxActDown:
		quickMenuChangeRow(1)
	case tbxActConfirm:
		if it, ok := quickMenuFocused(); ok {
			quickMenuRun(config, it)
		}
	case tbxActX, tbxActY:
		// Nothing here; the press is the screen's.
	default:
		return false
	}
	return true
}

// quickMenuMove moves within the row, stopping at the ends.
func quickMenuMove(col int) {
	n := len(quickMenuRowItems(quickMenu.row))
	if n == 0 {
		return
	}
	col = max(0, min(col, n-1))
	if col == quickMenu.col {
		return
	}
	quickMenu.col = col
	quickMenuNoteFocus()
	PlayNavSound()
}

// quickMenuChangeRow moves to the other row, onto the entry under the one
// focused (the rows have different lengths, so by position, not by index).
func quickMenuChangeRow(row int) {
	if row == quickMenu.row {
		return
	}
	from, to := quickMenuRowItems(quickMenu.row), quickMenuRowItems(row)
	if len(to) == 0 || len(from) == 0 {
		return
	}
	centre := (float64(quickMenu.col) + 0.5) / float64(len(from))
	col := int(centre * float64(len(to)))
	quickMenu.row, quickMenu.col = row, max(0, min(col, len(to)-1))
	quickMenuNoteFocus()
	PlayNavSound()
}

// quickMenuNoteFocus remembers the focused entry for the next visit.
func quickMenuNoteFocus() {
	if it, ok := quickMenuFocused(); ok {
		quickMenu.focusID, quickMenu.focusSet = it.id, true
	}
}

// quickMenuRun does what an entry does. Main thread.
func quickMenuRun(config *Config, it quickMenuItem) {
	if config == nil {
		config = appConfig
	}
	quickMenuNoteFocus()
	switch it.id {
	case qmRecents:
		PlayActivateSound()
		gameSwitcherOpen(config) // says so when the switcher is off
	case qmCollections:
		PlayActivateSound()
		quickMenuOpenCollections(config)
	case qmGames:
		PlayActivateSound()
		openGameLibraryPanel()
	case qmTools:
		PlayActivateSound()
		toolsHubOpen(config)
	case qmSettings:
		PlayActivateSound()
		setOpenScene("Settings")(config)
	case qmWifi:
		// The Home tile's switch: its own sound and toasts (home_quicktiles.go).
		hqWifiActivate(config)
	case qmSleep:
		PlayActivateSound()
		devSleepNow("quick menu")
	case qmRestart:
		quickMenuPower(config, true)
	case qmPowerOff:
		quickMenuPower(config, false)
	case qmExit:
		// The power card (Cancel / Exit), Home first: as Quick Settings' Power.
		PlayActivateSound()
		powerKeyCardRequested.Store(true)
	}
}

// quickMenuPower restarts or powers off through the power card's own path
// (home_power.go homePowerPress), asking first while "Ask before power off"
// is on. Main thread.
func quickMenuPower(config *Config, restart bool) {
	run := func(*Config) {
		PlayActivateSound()
		if snapshotVideoState().phase != "idle" {
			StopEmbeddedPlayback()
		}
		if restart {
			logInfo("power", "restart from the quick menu")
			devPowerRequest(devPowerReboot)
			return
		}
		logInfo("power", "power off from the quick menu")
		devPowerRequest(devPowerPoweroff)
	}
	if !powerOffConfirmWanted(config) {
		run(config)
		return
	}
	PlayActivateSound()
	title, action, body := "Power off?", "Power off", "The handheld switches off. A game still running is saved first."
	if restart {
		title, action, body = "Restart?", "Restart", "The handheld restarts and JukaHub comes back."
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    title,
		subtitle: "Ask before power off is on · Settings → Power & battery",
		icon:     "power",
		body:     body,
		buttons: []tbxSheetButton{
			{label: "Cancel"},
			{label: action, primary: true, run: func() { run(config) }},
		},
		// Cancel first, as the power card never focuses Restart or Power off
		// (confirmfocus.go): a second A by habit must not switch the handheld off.
		focus: 0,
	})
}

// quickMenuOpenCollections shows Home on its Collections tab: Home comes up
// (not on the scene history: B on Home has nowhere to go), then the tab is
// chosen once the Tabs Home is drawn. Main thread.
func quickMenuOpenCollections(config *Config) {
	home := homeSceneIndex(config)
	if config == nil || home < 0 {
		return
	}
	quickMenuCollectionsTab.pending = true
	quickMenuCollectionsTab.deadline = time.Now().Add(quickMenuCollectionsWait)
	if home != currentSceneIndex {
		switchScene(config, home, false)
	}
	tbxPost(quickMenuCollectionsTick)
}

// quickMenuCollectionsTick waits for the Tabs Home, one look per frame, then
// puts it on the Collections tab with the focus on the page. Main thread,
// from the UI queue.
func quickMenuCollectionsTick() {
	if !quickMenuCollectionsTab.pending {
		return
	}
	if time.Now().After(quickMenuCollectionsTab.deadline) || gameScreenSuspended() || appConfig == nil {
		quickMenuCollectionsTab.pending = false
		return
	}
	if transitionPhase != "none" || !homeTabsOn() {
		requestFrameEvery(50)
		tbxPost(quickMenuCollectionsTick)
		return
	}
	quickMenuCollectionsTab.pending = false
	homeTabsGo(appConfig, homeTabCollections, 0)
	if lvl := homeTabsLevelNow(); lvl != nil && len(lvl.items) > 0 && homeLayout != nil {
		homeLayout.Focus.Set(homeTabsPageID)
		homeTabs.autoStrip = false
		homeLayout.Focus.Remember()
	}
}

// ── Scene ─────────────────────────────────────────────────────────────────

type quickMenuScene struct{ sceneBase }

func (quickMenuScene) render(r *sdl.Renderer, c *Config) bool {
	quickMenuRender(r, c)
	return true
}

func (quickMenuScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), quickMenuAction)
}

func (quickMenuScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), quickMenuAction)
}

func init() { registerScene(quickMenuScene{sceneBase{name: quickMenuSceneName}}) }
