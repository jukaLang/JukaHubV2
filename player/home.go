package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// This file routes the home (Main) scene through the dedicated HomeLayout
// renderer and the explicit home focus graph (homefocus.go), while keeping the
// generic config-driven scene renderer for every other scene.

var (
	homeLayout      *HomeLayout
	homePressedID   HomeFocusID
	homePressedAt   uint64
	homeTileByIndex = map[int]HomeFocusID{} // Main scene element index -> home item
	homeTileMapSig  string                  // tile signature homeTileByIndex was built from

	homeFontsOnce  sync.Once
	homeFontsCache HomeFonts

	// homeActivateFromPad is set while homeActivate runs for a controller
	// press: the search pill opens the on-screen keyboard for a pad, never for
	// a desktop click or Enter, where a physical keyboard is already there.
	homeActivateFromPad bool
)

// homeOnHandheld reports whether JukaHub runs on a handheld with no physical
// keyboard, where every text field needs the on-screen keyboard. It is the
// same device test the cache budgets use (cachebudget.go).
func homeOnHandheld() bool {
	return IsTrimuiDevice() || P().IsHandheld()
}

// homePressDuration is the visual "pressed" feedback window before the action
// takes effect (spec: 70-100 ms). The scene transition begins right after, so
// the darkened card is visible as feedback starts.
const homePressDuration uint64 = 90

// refreshHomeLayout syncs the home layout state with the active scene. It is
// called every frame from renderScene and lazily builds the layout the first
// time the home scene is shown.
func refreshHomeLayout(config *Config) {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		homeLayoutActive = false
		return
	}
	scene := config.Scenes[currentSceneIndex]
	homeLayoutActive = scene.Layout == "home"
	if !homeLayoutActive {
		// The power card belongs to Home: a scene change (a section shoulder,
		// a shortcut) leaves it behind instead of carrying it to the next visit.
		if homePowerConfirm.open {
			homePowerClose()
		}
		// So does Edit Home, which saves what it has not saved yet.
		if homeEditActive() {
			homeEditClose(config)
		}
		return
	}
	if homeLayout == nil {
		homeLayout = NewHomeLayout(screenWidth, screenHeight)
	} else if homeLayout.Width != screenWidth || homeLayout.Height != screenHeight {
		homeLayout.Resize(screenWidth, screenHeight)
	}
	// The arrangement a total-conversion preset installed, applied to the live
	// layout as soon as it differs. Custom leaves the authored geometry in
	// charge, which is what ApplyConfig below then applies.
	homeLayout.SetArrangement(homeArrangement(config))

	// Keep the cards following jukaconfig.json: position, size, title, subtitle
	// and icon of every menu card are the Main scene's tiles (ApplyConfig),
	// so a config edit (or hot reload) reaches the screen on the next frame.
	homeLayout.ApplyConfig(scene.Elements)

	// Map config tile elements to home items so activation reuses the triggers
	// declared in jukaconfig.json ("Preserve the existing activation trigger").
	// The map is rebuilt whenever the scene's tile set changes: a hot reload that
	// adds, removes or renames tiles must re-map or the legacy press/click path
	// would resolve tiles against the previous config's geometry.
	if sig := homeTileSignature(scene.Elements); len(homeTileByIndex) == 0 || sig != homeTileMapSig {
		homeTileByIndex = buildHomeTileMap(scene.Elements, homeLayout)
		homeTileMapSig = sig
	}

	// The Carousel and Widgets bodies follow the tile list and step their
	// animations (home_dynbody.go); an Edit Home asked for on another screen
	// opens now that Home is up (home_edit.go).
	homeLayout.dynSync(config)
	homeEditPendingCheck(config)

	// Clear the pressed feedback once the short visual window has elapsed.
	if homePressedID != "" && sdl.GetTicks64()-homePressedAt > homePressDuration {
		homePressedID = ""
	}
}

// homeTileRect is a compatibility shim for the legacy tile renderer so the
// render + mouse code stays consistent even though the home scene now renders
// through HomeLayout.
func homeTileRect(elemIndex int) (sdl.Rect, bool) {
	if homeLayout == nil {
		return sdl.Rect{}, false
	}
	id, ok := homeTileByIndex[elemIndex]
	if !ok {
		return sdl.Rect{}, false
	}
	item, ok := homeLayout.Items[id]
	if !ok {
		return sdl.Rect{}, false
	}
	return item.Rect, true
}

// homeTileSignature fingerprints the home scene's tile set: the index and text
// of every tile button. A hot reload that adds, removes, moves or renames
// tiles produces a different signature, so the index -> card mapping is
// rebuilt instead of silently pointing at the previous config's cards.
func homeTileSignature(elements []Element) string {
	var sb strings.Builder
	for i, el := range elements {
		if el.Type != "button" || !strings.EqualFold(el.Style, "tile") {
			continue
		}
		fmt.Fprintf(&sb, "%d:%s;", i, strings.TrimSpace(el.Text))
	}
	return sb.String()
}

// buildHomeTileMap maps Main-scene tile element indices to the home items they
// belong to, using the same leniency as the triggers always have matched with:
// an exact (case insensitive) title, or a name the card has had
// (homeTitleAlias: "Emulator" for Games, "Search / Tools" for Tools).
func buildHomeTileMap(elements []Element, h *HomeLayout) map[int]HomeFocusID {
	out := map[int]HomeFocusID{}
	for i, el := range elements {
		if el.Type != "button" || !strings.EqualFold(el.Style, "tile") {
			continue
		}
		title := strings.TrimSpace(el.Text)
		alias := homeTitleAlias(title)
		for _, id := range h.Order {
			item, ok := h.Items[id]
			if !ok {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(item.Title), title) || alias == id {
				out[i] = id
				break
			}
		}
	}
	return out
}

// homeRecentRect returns the Continue card rect for the legacy recent panel.
func homeRecentRect() (sdl.Rect, bool) {
	if homeLayout == nil {
		return sdl.Rect{}, false
	}
	item, ok := homeLayout.Items[HomeFocusContinue]
	if !ok {
		return sdl.Rect{}, false
	}
	return item.Rect, true
}

// homeSceneTarget resolves a home item to a destination scene name. It prefers
// the configured tile triggers on the home scene; otherwise it falls back to a
// fixed mapping against the real scene names in jukaconfig.json.
func homeSceneTarget(config *Config, id HomeFocusID) string {
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		for _, el := range config.Scenes[currentSceneIndex].Elements {
			if el.Type != "button" || el.Style != "tile" || el.Trigger == "" {
				continue
			}
			title := strings.TrimSpace(el.Text)
			matches := strings.EqualFold(title, string(id)) ||
				(id == HomeFocusTools && strings.Contains(strings.ToLower(title), "tools"))
			if id == HomeFocusContinue {
				matches = strings.EqualFold(title, "media")
			}
			if matches && strings.HasPrefix(el.Trigger, "change_scene:") {
				return strings.TrimPrefix(el.Trigger, "change_scene:")
			}
		}
	}
	switch id {
	case HomeFocusContinue, HomeFocusMedia:
		return "Tube"
	case HomeFocusFiles:
		return "FileExplorer"
	case HomeFocusPackages:
		return "Packages"
	case HomeFocusApps:
		return "Apps"
	case HomeFocusFavorites:
		return "Favorites"
	case HomeFocusBrowser:
		// The browser scene is injected into memory at startup, so it has no
		// tile of its own in the shipped config.
		return browserSceneName
	case HomeFocusChat:
		return "Chat"
	case HomeFocusTools:
		return toolsHubSceneName
	case HomeFocusEmulator:
		return "Emulator"
	case HomeFocusSettings:
		return "Settings"
	case HomeFocusExit, HomeFocusNowPlaying:
		// Exit and the now-playing chip are handled before any scene lookup.
		return ""
	}
	return ""
}

// homeCalendarOpen tracks the header clock's toggle_calendar state: while
// set, the mini calendar popover shows under the clock in the top-right.
var homeCalendarOpen bool

// homeCalendarRect is the mini calendar popover's anchor: a panel directly
// under the header clock, right-aligned with the header status cluster. Both
// the renderer and the click hit-testing use this one rect.
func homeCalendarRect() sdl.Rect {
	// The popover has its own, roomier card (calendarpopover.go); the Alarm
	// screen's corner widget keeps the compact one.
	w, h := calendarCardSize()
	return sdl.Rect{
		X: screenWidth - scaleX(32, screenWidth) - w,
		Y: scaleY(64, screenHeight),
		W: w,
		H: h,
	}
}

// calendarAlarmButtonRect is the "Set alarm" button below the mini calendar,
// so the clock popover is the single entry point for both the date and alarms.
func calendarAlarmButtonRect() sdl.Rect {
	r := homeCalendarRect()
	return sdl.Rect{
		X: r.X,
		Y: r.Y + r.H + scaleY(8, screenHeight),
		W: r.W,
		H: scaleY(38, screenHeight),
	}
}

// toggleCalendarPopover opens or closes the clock popover. The clock behaves
// the same on Home and every other scene, so there is one toggle for both.
// It is exclusive with the other top-bar overlays: two floating panels at once
// would overlap and fight over the same clicks.
func toggleCalendarPopover() {
	homeCalendarOpen = !homeCalendarOpen
	if homeCalendarOpen {
		tb.startOpen = false
		nc.open = false
		dismissWeatherPopover()
		dismissVolumePopover()
		dismissBrightnessPopover()
		// Every open starts on the current month: a view left on March must not
		// look like the clock's date the next time it is opened.
		resetCalendarView()
	}
}

// renderCalendarPopover draws the mini calendar and the Set alarm action below
// it. It is called by the home renderer and by the shared render loop for all
// other scenes, so the clock looks and behaves identically everywhere.
func renderCalendarPopover(renderer *sdl.Renderer, config *Config) {
	r := homeCalendarRect()
	renderCalendarCard(renderer, config, r.X, r.Y)

	b := calendarAlarmButtonRect()
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	UIButton(renderer, config, b, "Set alarm", UIButtonStyle{
		Variant:    UIButtonCustom,
		Fill:       WithAlpha(ColorInfo, 55),
		Border:     WithAlpha(ColorInfo, 150),
		LabelColor: ColorTextPrimary(),
		Font:       font,
		Radius:     10,
		NoShadow:   true,
	}, UIButtonState{})
}

// calendarPopoverClick consumes a mouse click while the clock popover is open.
// The Set alarm row opens the alarm editor; any other click dismisses the
// popover. It reports whether the popover was open and handled the click.
func calendarPopoverClick(mx, my int32, config *Config) bool {
	if !homeCalendarOpen {
		return false
	}
	// The header's arrows change month and the title returns to today. These
	// are inside the card, so they must be tested before the "click inside the
	// card is inert" rule below.
	if calendarPopoverNavigation(mx, my) {
		return true
	}
	// Clicks on the calendar card itself stay inside the popover (viewing the
	// month must not dismiss it); only the action row changes screens.
	if pointInRect(mx, my, homeCalendarRect()) {
		return true
	}
	if pointInRect(mx, my, calendarAlarmButtonRect()) {
		homeCalendarOpen = false
		PlayActivateSound()
		openAlarmScene(config)
		return true
	}
	homeCalendarOpen = false
	return true
}

// homeActivate triggers the action for the currently focused home item.
// Continue resumes the most recent item when one exists, otherwise it opens
// Media. Every other item performs a scene change using its configured target.
// The Clock has a dedicated trigger: toggle_calendar.
func homeActivate(config *Config) {
	if homeLayout == nil {
		return
	}
	id := homeLayout.Focus.Current()
	// The notification bell opens the same dropdown as the touch target and
	// the chrome control on other scenes. notifToggle plays its own
	// activation sound, so return before the generic one.
	if id == HomeFocusBell {
		logDebug("home", "activate card %s", id)
		homePressedID = id
		homePressedAt = sdl.GetTicks64()
		notifToggle()
		return
	}
	// The pairing code goes full screen: the size and brightness a phone
	// camera reads from arm's length (qrzoom.go). The card plays its own
	// activation sound, so this returns before the generic one too.
	if id == HomeFocusQR {
		logDebug("home", "activate card %s", id)
		homePressedID = id
		homePressedAt = sdl.GetTicks64()
		homeShowPairingQR()
		return
	}
	PlayActivateSound()
	logDebug("home", "activate card %s", id)
	homePressedID = id
	homePressedAt = sdl.GetTicks64()

	// The tile layouts' own controls: a widget, an app tile, the add tile and
	// the Edit pill (home_edit.go). Built-in cards go on as below.
	if homeActivateDynamic(config, id) {
		return
	}

	// The PS5 launcher is the one header tile that is also a launcher: A plays
	// the PS5 build, or downloads it first. It gets its own sound, so return
	// before the generic card handling below.
	if id == HomeFocusPS5 {
		ps5TileActivate()
		return
	}
	if id == HomeFocusPS4 {
		ps4TileActivate()
		return
	}
	// The update chip is the other one: A downloads and installs the release
	// directly, without opening Patch or any other menu.
	if id == HomeFocusUpdate {
		startHeaderUpdate()
		return
	}
	// The search pill opens the search sheet. The on-screen keyboard comes up
	// with it when there is no physical keyboard to type on: a pad press, or
	// any press on the handheld.
	if id == HomeFocusSearch {
		homeSearchOpen(config, homeActivateFromPad || homeOnHandheld())
		return
	}
	// The now-playing chip names a running session; A opens that player, the
	// same as a tap on the chip or on the chrome control of any other scene.
	if id == HomeFocusNowPlaying {
		openNowPlaying(config)
		return
	}
	// The trophy room's cup beside the bell opens Trophies, the same as a tap
	// on it and the chrome control of every other scene (header_trophy.go).
	if id == HomeFocusTrophy {
		headerTrophyActivate(config)
		return
	}
	if id == HomeFocusBug {
		headerBugActivate(config)
		return
	}
	if id == HomeFocusContinue {
		if homeContinueResume(config) {
			return
		}
		// Nothing to resume: the first-run card's own step, the one its pill
		// names (homeContinueEmptyState) — the game library on a handheld,
		// Media on the desktop.
		if homeContinueStartsGames() {
			openGameLibraryPanel()
			return
		}
		id = HomeFocusMedia
	}
	// The power button asks first (home_power.go): its card's Exit stops any
	// embedded playback and quits through the main loop's clean shutdown,
	// which draws the glass teardown.
	if id == HomeFocusExit {
		homePowerOpen()
		return
	}
	// Header chips never map to a scene: their dedicated triggers run before
	// the scene lookup. The meter opens the Wi-Fi screen (toolshub.go).
	if id == HomeFocusWiFi {
		wifiScrOpen(config)
		return
	}
	// The Bluetooth rune beside it opens the Bluetooth page, where paired
	// devices connect and disconnect (statusbar_bluetooth.go).
	if id == HomeFocusBluetooth {
		btHeaderActivate(config)
		return
	}
	// The speaker left of the rune opens the volume popover
	// (volumepopover.go), and the sun left of the speaker the brightness card
	// (brightnesspopover.go).
	if id == HomeFocusVolume {
		toggleVolumePopover()
		return
	}
	if id == HomeFocusBrightness {
		toggleBrightnessPopover()
		return
	}
	if id == HomeFocusWeather {
		toggleWeatherPopover()
		return
	}
	if id == HomeFocusClock {
		toggleCalendarPopover()
		return
	}
	// The Games card opens the game library — consoles, their games and box
	// art — on every platform; it is a modal panel, so it needs no scene and
	// works on the desktop config too. The emulator manager it used to open is
	// setup, not play: it stays one step away in Start → Games → Emulators and
	// in search.
	if id == HomeFocusEmulator {
		openGameLibraryPanel()
		return
	}
	// The Browser card opens the in-app browser on its start page; the scene is
	// injected on demand so a config that predates it still works.
	if id == HomeFocusBrowser {
		openBrowserHome(config)
		return
	}
	// The Tools card opens the Tools hub, which replaced the classic Misc
	// scene and the tools overlay (toolshub.go owns where that lands).
	if id == HomeFocusTools {
		toolsHubOpen(config)
		return
	}
	target := homeSceneTarget(config, id)
	if target == "" {
		return
	}
	// findSceneIndex falls back to 0 when the scene is missing, so check the
	// exact name here: a card whose screen this config lacks must not quietly
	// return to Home.
	idx := sceneIndexExact(config, target)
	logDebug("home", "%s -> scene %q (index %d)", id, target, idx)
	if idx >= 0 {
		changeSceneTo(config, idx)
	}
}

// handleHomeKey processes keyboard input on the home scene. It returns true
// when the key was consumed so the generic element-based handler is skipped.
func handleHomeKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || homeLayout == nil {
		return false
	}
	// The power button's confirmation is modal over Home.
	if homePowerConfirm.open {
		return homePowerKey(e)
	}
	// So is Edit Home (home_edit.go).
	if homeEditActive() {
		return homeEditKey(e, config)
	}
	// Easter egg: track J-U-K-A key sequence for firework animation.
	if !fw.active {
		keyMap := map[sdl.Keycode]string{
			sdl.K_j: "j", sdl.K_u: "u", sdl.K_k: "k", sdl.K_a: "a",
		}
		if letter, ok := keyMap[e.Keysym.Sym]; ok {
			onHomeKeyPress(letter)
		}
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		if homeLayout.Focus.Current() != homeLayout.Move(FocusUp) {
			PlayNavSound()
		}
	case sdl.K_DOWN:
		if homeLayout.Focus.Current() != homeLayout.Move(FocusDown) {
			PlayNavSound()
		}
	case sdl.K_LEFT:
		if homeLayout.Focus.Current() != homeLayout.Move(FocusLeft) {
			PlayNavSound()
		}
	case sdl.K_RIGHT:
		if homeLayout.Focus.Current() != homeLayout.Move(FocusRight) {
			PlayNavSound()
		}
	case sdl.K_RETURN, sdl.K_SPACE:
		// A held Enter on the pairing code must not open the full-screen
		// card again with every repeat (each open also drives the backlight).
		if e.Repeat != 0 && homeLayout.Focus.Current() == HomeFocusQR {
			break
		}
		homeActivate(config)
	case sdl.K_SLASH:
		// "/" is the keyboard's search key on Home (F2 opens search anywhere).
		// Its text event follows the key press and must not type a "/".
		PlayActivateSound()
		homeSearchOpen(config, false)
		homeSearch.skipText = "/"
	case sdl.K_x:
		// X edits a Carousel or Widgets Home, as the pad's X does; elsewhere
		// the key keeps its old meaning further down the dispatch.
		if !homeLayout.dynActive() || e.Repeat != 0 || homeLayout.Focus.Current() == HomeFocusNowPlaying {
			return false
		}
		PlayActivateSound()
		homeEditOpen(config)
	case sdl.K_q:
		// Q is L2's twin on Home (the section keys Q/E mirror L2/R2): it
		// opens Edit Home in any layout (home_edit.go). A video playing full
		// size keeps it: Q ends playback there.
		if snapshotVideoState().phase != "idle" && !videoMiniVisible() {
			return false
		}
		if e.Repeat == 0 {
			homeEditTrigger(config)
		}
	case sdl.K_w:
		// W is R2's twin on Home: the next liquid background level
		// (liquidbg.go liquidCycle). Ctrl/Alt chords are not it.
		if e.Keysym.Mod&(sdl.KMOD_CTRL|sdl.KMOD_ALT) != 0 ||
			(snapshotVideoState().phase != "idle" && !videoMiniVisible()) {
			return false
		}
		if e.Repeat == 0 {
			PlayActivateSound()
			liquidCycle(config)
		}
	case sdl.K_ESCAPE:
		// Escape dismisses the calendar popover first.
		if homeCalendarOpen {
			homeCalendarOpen = false
			break
		}
		// Home is the root scene, but a video left playing from another scene
		// must still be closable instead of swallowing the key.
		if snapshotVideoState().phase != "idle" {
			StopEmbeddedPlayback()
		}
	default:
		return false
	}
	return true
}

// handleHomeController processes controller input on the home scene. Shoulder
// buttons intentionally fall through so scene paging keeps working.
func handleHomeController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || homeLayout == nil {
		return false
	}
	if homePowerConfirm.open {
		return homePowerPad(e)
	}
	if homeEditActive() {
		return homeEditPad(e, config)
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if homeLayout.Focus.Current() != homeLayout.Move(FocusUp) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if homeLayout.Focus.Current() != homeLayout.Move(FocusDown) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		if homeLayout.Focus.Current() != homeLayout.Move(FocusLeft) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if homeLayout.Focus.Current() != homeLayout.Move(FocusRight) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_A:
		homeActivateFromPad = true
		homeActivate(config)
		homeActivateFromPad = false
	case sdl.CONTROLLER_BUTTON_Y:
		// Y opens search from anywhere on Home, like a phone launcher's search
		// key; the pill and the footer show the same badge.
		PlayActivateSound()
		homeSearchOpen(config, true)
	case sdl.CONTROLLER_BUTTON_X:
		// On a Carousel or Widgets Home, X edits it (the footer says so);
		// the now-playing chip keeps closing the mini player.
		if homeLayout.dynActive() && homeLayout.Focus.Current() != HomeFocusNowPlaying {
			PlayActivateSound()
			homeEditOpen(config)
			break
		}
		if homeLayout.Focus.Current() != HomeFocusNowPlaying || !closeFocusedMiniVideo() {
			return false
		}
	case sdl.CONTROLLER_BUTTON_B:
		// B dismisses the calendar popover first.
		if homeCalendarOpen {
			homeCalendarOpen = false
			break
		}
		// Home is the root scene; a playing video is the only thing to close.
		if snapshotVideoState().phase != "idle" {
			StopEmbeddedPlayback()
		}
	default:
		return false
	}
	return true
}

// handleHomeMouseClick selects and activates a home card under the cursor.
func handleHomeMouseClick(mx, my int32, config *Config) bool {
	if homeLayout == nil {
		return false
	}
	// The power button's confirmation owns every tap while it is up.
	if homePowerConfirm.open {
		homePowerClick(mx, my)
		return true
	}
	// So does Edit Home (home_edit.go).
	if homeEditActive() {
		return homeEditClick(mx, my, config)
	}
	// With the calendar popover open, a click inside it is consumed, a click
	// on the clock toggles it closed, and any other click dismisses it without
	// activating whatever lies underneath - standard dropdown behaviour.
	if calendarPopoverClick(mx, my, config) {
		return true
	}
	if weatherPopoverClick(mx, my, config) {
		return true
	}
	// The header QR is a direct pairing affordance: a tap shows it full screen,
	// the same as A on it (focus follows, so the D-pad continues from it). The
	// pairing card with the PIN and the address stays in Settings, Network
	// tools and search.
	if companionHeaderRect.W > 0 && pointInRect(mx, my, companionHeaderRect) {
		homeLayout.Focus.Set(HomeFocusQR)
		homeLayout.Focus.Remember()
		homeShowPairingQR()
		return true
	}
	// The PS5 launcher beside it starts streaming in one press, installing the
	// beta first when it is missing.
	if ps4TileRect.W > 0 && pointInRect(mx, my, ps4TileRect) {
		// A click is a deliberate choice: focus follows it so the D-pad
		// continues from the tile the user just touched.
		homeLayout.Focus.Set(HomeFocusPS4)
		homeLayout.Focus.Remember()
		ps4TileActivate()
		return true
	}
	if ps5TileRect.W > 0 && pointInRect(mx, my, ps5TileRect) {
		// A click is a deliberate choice: focus follows it so the D-pad
		// continues from the tile the user just touched.
		homeLayout.Focus.Set(HomeFocusPS5)
		homeLayout.Focus.Remember()
		ps5TileActivate()
		return true
	}
	// A carousel card: a side card turns the row to it, the middle one opens
	// (home_carousel.go). The pointer's hover never moves the row.
	if homeLayout.dynClick(mx, my, config) {
		return true
	}
	if homeLayout.FocusAt(mx, my) {
		// A clicked card is a deliberate choice, so it becomes the card
		// restored when the user returns to Home.
		homeLayout.Focus.Remember()
		homeActivate(config)
		return true
	}
	return false
}

// homeViewData assembles the per-frame data the HomeLayout renderer needs:
// greeting, header status (network / weather / time / battery), the Continue
// state resolved from the recently-played store, and pressed feedback.
// homeGreeting builds the greeting line, appending the configured username so
// the home screen feels personal without touching the header status cluster.
// A screenshot leaves the name out (screenshotmode.go).
func homeGreeting(name string) string {
	g := defaultGreeting(time.Now())
	if n := strings.TrimSpace(name); n != "" && !shotModeOn() {
		return g + ", " + n
	}
	return g
}

func homeViewData(config *Config) HomeViewData {
	// Home is the root scene, so B has nothing to go back to there: the footer
	// names Start instead, the menu that reaches every other screen.
	// Subtitle stays empty: the line under the greeting is the search pill,
	// which picks the longest placeholder that fits its width.
	data := HomeViewData{
		Greeting:     homeGreeting(""),
		BuildDate:    installedBuildDate(),
		StatusParts:  headerStatusParts(config),
		Pressed:      homePressedID,
		ShowBackHint: false,
	}
	// Use the status bar's own bell metrics here, not a second home-only size,
	// so the control is pixel-identical on Home and every other scene.
	data.BellFont, _ = getCachedFont(config, "small")
	data.BellW, data.BellH = notifBellSize(config)
	// Ease the click feedback over the (short) press window. uiPressProgress
	// snaps to full depth when motion is disabled, so reduced motion keeps the
	// press readable without animating it.
	if homePressedID != "" {
		age := float64(sdl.GetTicks64() - homePressedAt)
		data.PressDepth = uiPressProgress(config, age, float64(homePressDuration))
	}
	// The name is Settings → Your name, shared with the chat (userName). The
	// old lookup read Custom["TSPUsername"], a key Settings never writes (it
	// stores "tspUsername"), so a set name never reached the greeting.
	data.Greeting = homeGreeting(userName(config))

	// The Continue card shows exactly what A on it resumes (home_continue.go):
	// the music session when a track is loaded, otherwise the newest item on
	// the timeline. The snapshot is rebuilt a few times a second, not on every
	// frame: copying and sorting the timeline and formatting its labels per
	// frame was allocation the handheld paid for nothing.
	data.Continue = homeContinueSnapshot()
	return data
}

// recentSourceLabel describes where a recent item came from for the Continue
// card's supporting text.
func recentSourceLabel(it FavoriteItem) string {
	label := ""
	switch it.Type {
	case "video":
		label = "Video"
	case "iptv":
		label = "Channel"
	case "file":
		label = "File"
	case "game":
		// The console is the useful context for a game, not the word "Game".
		if g, ok := it.Data.(recentGame); ok && g.System != "" {
			label = g.System
		} else {
			label = "Game"
		}
	default:
		label = "Item"
	}
	if !it.Timestamp.IsZero() {
		label += " · " + timeAgo(it.Timestamp)
	}
	return label
}

// homeRecentProgress returns the 0..1 playback fraction for the Continue
// card, derived from the persisted watch position on the most recent video.
func homeRecentProgress(it FavoriteItem) float32 {
	if it.Type != "video" {
		return 0
	}
	v, ok := it.Data.(VideoInfo)
	if !ok || v.Duration <= 0 || v.Position <= 0 {
		return 0
	}
	p := float32(v.Position / v.Duration)
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	return p
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// renderHomeModern draws the home scene through the dedicated layout renderer.
// It owns the background so the layout stays focused on content.
func renderHomeModern(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || config == nil {
		return
	}
	refreshHomeLayout(config)
	if homeLayout == nil {
		return
	}

	// The layout owns the background: its backdrop fills the screen with the
	// theme colour, opaquely, before the ambient blobs go on top. Copying a
	// background image or the gradient here first painted a 1280x720 texture and
	// a blended full-screen fill that the very next call covered, every frame —
	// on the Smart Pro that was 7.4 MB of bandwidth per frame for pixels nobody
	// ever saw. The texture is still refreshed here so a changed BackgroundImage
	// setting does not leave the previous image resident.
	ensureBackgroundTexture(renderer, config)

	// Easter egg fireworks: play explosive particle animation if active.
	if fw.active {
		renderFireworks(renderer, config, 0.016)
	}

	// Hover/focus lift honours reduced motion; the layout also owns the win
	// animations for the apps menu and notification dropdown.
	// Reduced motion and focusMotion none both drop the eased lift; the
	// colour/press feedback stays either way (motion.go).
	homeLayout.Motion = uiMotionEnabled(config) && motionFocusEnabled(config)
	homeLayout.Glide = (config == nil || !effectiveReducedMotion(config)) && motionFocusEnabled(config)
	// The layout's backdrop draws the wallpaper of the config being rendered,
	// the same one ensureBackgroundTexture above was refreshed for.
	homeBackdropConfig = config
	_ = homeLayout.Render(renderer, loadHomeFonts(), homeViewData(config))

	// The header clock toggles the mini calendar (trigger: toggle_calendar).
	// It draws after the layout so the popover floats above the cards instead
	// of being painted over by the layout backdrop.
	if homeCalendarOpen {
		renderCalendarPopover(renderer, config)
	}
	// A paired device's activity to continue here, or a pairing code to
	// compare: a modal glass card over the layout (hoffcard.go).
	hoffRenderCard(renderer, config)
	// The power button's "Exit JukaHub?" card, over everything Home draws.
	renderHomePowerConfirm(renderer, config)
	// Edit Home's add sheet (home_addsheet.go); edit mode also ends here when
	// another overlay has taken the screen.
	renderHomeEditOverlays(renderer, config)
}

// loadHomeFonts opens the bundled Inter font at the home-screen sizes. Inter
// is the only face Home draws with (home_fonts.go); there is no second font to
// fall back to. Fonts are cached so the render loop never allocates per frame.
func loadHomeFonts() HomeFonts {
	homeFontsOnce.Do(func() {
		// The compact 4:3 canvas keeps the 720 height but loses a quarter of the
		// width, so the home type scale shrinks with it; otherwise the narrow
		// feature row truncates "Favorites" to "Favo…". The legibility floor
		// (uiLegibleFontSize) stops the 14px captions there at 12px instead of
		// the 11px the bare scale gave a 3.2" panel.
		homeFontsCache = HomeFonts{
			Brand:    openHomeFont(uiLegibleFontSize(26)),
			Greeting: openHomeFont(uiLegibleFontSize(32)),
			Subtitle: openHomeFont(uiLegibleFontSize(19)),
			Card:     openHomeFont(uiLegibleFontSize(23)),
			Tile:     openHomeFont(uiLegibleFontSize(20)),
			Meta:     openHomeFont(uiLegibleFontSize(14)),
			Helper:   openHomeFont(uiLegibleFontSize(17)),
			Small:    openHomeFont(uiLegibleFontSize(16)),
		}
	})
	return homeFontsCache
}

// openHomeFont opens the bundled Inter at one home-screen size. It goes through
// the shared font cache, so the shutdown path closes the face, the cmap is
// registered for character substitution (fonts.go), and a size Home shares
// with a scene role is opened once instead of twice.
func openHomeFont(size int) *ttf.Font {
	return loadCachedFont(homeUIFontForRole(homeUIFontFile), size)
}

// resetHomeFonts drops Home's size caches after a text-size or bold-text
// change. The shared face cache is cleared by a11yRefreshFonts; this releases
// the cached role set so the next frame opens the new face at the same sizes.
func resetHomeFonts() {
	homeFontsOnce = sync.Once{}
	homeFontsCache = HomeFonts{}
	homeWidgetFontsOnce = sync.Once{}
	homeWidgetFontsCache = homeWidgetFontSet{}
}
