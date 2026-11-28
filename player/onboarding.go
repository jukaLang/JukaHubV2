package main

// First-run guided tour. It is a global modal rather than a scene because it
// has to sit above whatever screen the user lands on and cannot be navigated
// away from by the normal element dispatcher.

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

type onboardingStep struct {
	title string
	body  string
	hint  string
	kind  string
}

var onboardingSteps = []onboardingStep{
	{
		// The first question, before any English: the list and its title are
		// drawn by onboarding_lang.go, and the language switches as the
		// highlight moves, so every later page already reads in it.
		title: "Choose your language",
		kind:  "language",
	},
	{
		// The quick start (onboarding_start.go): play, add games, Wi-Fi or a
		// look, each one press from here; the rest of the tour is its last
		// row.
		title: "Welcome to JukaHub",
		body:  "What would you like to do first? Everything else can wait.",
		kind:  "start",
	},
	{
		// The user's name (onboarding_name.go): Home's greeting, the chat and
		// the boot and shutdown greetings use it.
		title: "What should we call you?",
		body:  "JukaHub greets you by name on Home, at startup and at shutdown.",
		hint:  "Optional. Settings > General > Your name changes it later.",
		kind:  "name",
	},
	{
		title: "Connect to Wi-Fi",
		body:  "Wi-Fi enables updates, weather, companion controls and artwork downloads. The button opens the Wi-Fi settings.",
		hint:  "Network status is shown below. Press F or select the button to open Wi-Fi settings.",
		kind:  "wifi",
	},
	{
		title: "Set your time zone",
		body:  "Correct local time keeps the clock, alarms and release dates consistent. The button opens the time zone settings.",
		hint:  "Internet time keeps the clock right; choose the zone you live in.",
		kind:  "timezone",
	},
	{
		// On a TrimUI the first start has already switched this on
		// (autostart_firstrun.go); the step says so and offers the way back.
		// Elsewhere it is off, and the button turns it on.
		title: "Start with your device",
		body:  "JukaHub can open by itself every time the device starts.",
		hint:  "Settings > General > Start JukaHub at login changes it later.",
		kind:  "autostart",
	},
	{
		title: "Choose a colour",
		body:  "Choose a colour for JukaHub. The next step lets you choose the Home layout.",
		hint:  "Press F or select the button to preview colours.",
		kind:  "theme",
	},
	{
		title: "Choose your Home layout",
		body:  "Pick the Home layout you prefer. You can change it later in Settings.",
		kind:  "layout",
	},
	{
		title: "Scan your ROMs",
		body:  "JukaHub checks the usual Roms folders for supported files. It only reads filenames and does not move or edit your games.",
		hint:  "Press F or select the button to scan again after adding games.",
		kind:  "roms",
	},
	{
		// The background passes are boxart_auto.go's; the button still fetches
		// at once.
		title: "Fetch game artwork",
		body:  "When online, JukaHub finds box art in the background. Only game titles go to the public Libretro thumbnails collection.",
		hint:  "The button fetches now. Settings > Games & emulators turns it off.",
		kind:  "artwork",
	},
	{
		title: "Get set up",
		body:  "These links unlock the rest of JukaHub: achievements, video, Remote Play and your phone. Everything here is optional, and the tour comes back when you close a panel.",
		hint:  hintSentence("Choose a row and press %s to open it.", "A", "Open"),
		kind:  "setup",
	},
	{
		title: "Move around JukaHub",
		body:  "Use the D-pad or arrow keys to move, A or Enter to select, and B or Escape to go back. Home, Start and Tools make the main features easy to find.",
		hint:  "The on-screen footer shows the controls for each screen.",
	},
	{
		// The last step names the two Tools pages nobody guesses: the Toolbox
		// installs standalone helpers, and Security & trust answers "how is this
		// device doing?" without a PC. Both are reachable only from Tools, so
		// the tour is the one place they cost nothing to point at.
		title: "Toolbox and trust",
		body:  "Two pages under Tools do what usually needs a computer. Toolbox installs standalone helpers — rclone, jq, ripgrep, fd, pandoc, age — from verified releases and runs them. Security & trust lists what this build protects.",
		hint:  "Neither page changes anything until you install or start something.",
		kind:  "tools",
	},
}

var onboarding = struct {
	active bool
	step   int
	enter  uiEnter
	next   sdl.Rect
	skip   sdl.Rect
	action sdl.Rect
	// card and footer are what the last frame settled on: the card's rect and
	// the controls-hint rect, recorded so the layout test can pin the relation
	// a Brick screenshot caught broken (the hint used to be drawn across the
	// card's bottom border). bodyLines is the wrapped step body for the same
	// reason: the wrap must land on word boundaries.
	card      sdl.Rect
	footer    sdl.Rect
	bodyLines []string
	info      string
	roms      *romScanReport
	// The "Get set up" step's live checklist: the rows, the row the D-pad is
	// on, their hit rects and when they were last rebuilt from the device.
	setupCursor int
	setupRows   []setupRow
	setupHits   []sdl.Rect
	setupAt     uint64
}{
	next: sdl.Rect{},
	skip: sdl.Rect{},
}

func onboardingDone(config *Config) bool {
	if config == nil {
		return true
	}
	v, ok := config.Variables.Custom["onboarding_done"]
	return ok && coerceBool(v)
}

// onboardingStartIfNeeded opens the tour on the first launch after an install
// (or after a config reset). A completed tour is remembered in jukauser.json.
// A screenshot session skips it without marking it done (screenshotmode.go).
func onboardingStartIfNeeded(config *Config) {
	if onboardingDone(config) || shotModeOn() {
		return
	}
	onboardingStart()
}

func onboardingStart() {
	onboarding.active = true
	onboarding.step = 0
	onboarding.enter.begin(uiClockMS(), uiPanelEnterMS)
	onboarding.next = sdl.Rect{}
	onboarding.skip = sdl.Rect{}
	onboarding.action = sdl.Rect{}
	onboarding.card = sdl.Rect{}
	onboarding.footer = sdl.Rect{}
	onboarding.bodyLines = nil
	onboarding.info = ""
	onboarding.roms = nil
	onboarding.setupCursor = 0
	onboarding.setupRows = nil
	onboarding.setupHits = nil
	onboarding.setupAt = 0
	onboardingLangReset(appConfig)
	onboardingNameReset()
	onboardingLayoutReset(appConfig)
	toolsHubTourReset()
}

func onboardingFinish(config *Config, skipped bool) {
	// Skipping from the language page keeps the language on screen.
	langPage := onboardingLangStepIs()
	onboarding.active = false
	if config != nil {
		if config.Variables.Custom == nil {
			config.Variables.Custom = map[string]interface{}{}
		}
		if langPage {
			onboardingLangStore(config)
		}
		config.Variables.Custom["onboarding_done"] = true
		config.Variables.Custom["onboarding_skipped"] = skipped
		saveUserConfigDebounced(userConfigFromVariables(config))
	}
	// The completed tour's payoff: the controller legend appears once, drawn
	// from the same footerHints registry the footers show, so the button rows
	// the tour described in words are now on screen with real bindings. A
	// skipped tour does not interject, and a "Get set up" panel still covering
	// the screen keeps the spotlight.
	if !skipped && !onboardingCoveredByOverlay() {
		openControlsLegend()
	}
	if !skipped {
		trophyEvent(tkTourDone)
	}
}

func onboardingAdvance(config *Config) {
	if onboarding.step >= len(onboardingSteps)-1 {
		onboardingFinish(config, false)
		return
	}
	if onboardingLangStepIs() {
		onboardingLangCommit(config)
	}
	if onboardingLayoutStepIs() {
		onboardingLayoutCommit(config)
	}
	onboarding.step++
	onboarding.action = sdl.Rect{}
	onboarding.enter.begin(uiClockMS(), 180)
	// The checklist reads the device when it appears, so a step the user just
	// finished is never shown as still needed.
	if onboardingStepIsSetup() {
		onboardingSetupRefresh(config, true)
	}
	if onboardingLayoutStepIs() {
		onboardingLayoutReset(config)
	}
	PlayNavSound()
}

// onboardingStepIsSetup reports whether the tour is showing its checklist page.
func onboardingStepIsSetup() bool {
	if onboarding.step < 0 || onboarding.step >= len(onboardingSteps) {
		return false
	}
	// The quick start's choices are a checklist too (onboarding_start.go).
	kind := onboardingSteps[onboarding.step].kind
	return kind == "setup" || kind == "start"
}

// ── "Get set up" checklist ──────────────────────────────────────────────

// onboardingCoveredByOverlay reports whether a modal panel is on top of the
// tour. The checklist opens those panels, so the tour stops drawing and stops
// consuming input while one is open instead of covering it. The Wi-Fi and Tools
// rows open pages rather than panels; the tour steps aside the same way until
// the user is back on the screen it was drawn over (toolsHubTourAway).
func onboardingCoveredByOverlay() bool {
	return modalPanelBlocksShoulders() || toolsHubTourAway() || tzPickerVisible()
}

// onboardingSetupRefresh rebuilds the checklist from live device state. The
// probes read the disk, so the render path refreshes at most twice a second;
// every input and every action refreshes immediately.
func onboardingSetupRefresh(config *Config, force bool) []setupRow {
	now := sdl.GetTicks64()
	if !force && onboarding.setupRows != nil && now-onboarding.setupAt < 500 {
		return onboarding.setupRows
	}
	if onboardingSteps[onboarding.step].kind == "start" {
		onboarding.setupRows = onboardingStartRows(config)
	} else {
		onboarding.setupRows = setupChecklist(liveSetupEnv(config), setupActionsFor())
	}
	onboarding.setupAt = now
	if onboarding.setupCursor >= len(onboarding.setupRows) {
		onboarding.setupCursor = len(onboarding.setupRows) - 1
	}
	if onboarding.setupCursor < 0 {
		onboarding.setupCursor = 0
	}
	return onboarding.setupRows
}

// onboardingSetupMove walks the checklist rows.
func onboardingSetupMove(delta int) {
	if len(onboarding.setupRows) == 0 {
		return
	}
	next := onboarding.setupCursor + delta
	if next < 0 {
		next = 0
	}
	if next >= len(onboarding.setupRows) {
		next = len(onboarding.setupRows) - 1
	}
	if next == onboarding.setupCursor {
		return
	}
	onboarding.setupCursor = next
	PlayNavSound()
}

// onboardingSetupActivate opens the panel for the focused row. The tour stays
// active but is hidden behind that panel until it closes.
func onboardingSetupActivate(config *Config) {
	rows := onboardingSetupRefresh(config, true)
	if onboarding.setupCursor < 0 || onboarding.setupCursor >= len(rows) {
		return
	}
	if action := rows[onboarding.setupCursor].Action; action != nil {
		// A row that opens a page (Wi-Fi, Tools) moves to another scene; the
		// tour remembers where it was drawn and waits there.
		toolsHubTourMark()
		action()
	}
	// The panels change the state the row reports (an RA key was saved, a
	// console paired), so re-read it before the tour is visible again.
	onboardingSetupRefresh(config, true)
}

// onboardingSetupClick activates the row under a pointer.
func onboardingSetupClick(mx, my int32, config *Config) bool {
	for i, rect := range onboarding.setupHits {
		if !pointInRect(mx, my, rect) {
			continue
		}
		onboarding.setupCursor = i
		onboardingSetupActivate(config)
		return true
	}
	return false
}

func onboardingActionLabel(kind string) string {
	switch kind {
	case "name":
		return onboardingNameActionLabel(appConfig)
	case "wifi":
		return "Open Wi-Fi settings"
	case "timezone":
		if dateTimeZoneOwner() == tzOwnerJukaHub {
			return "Choose time zone"
		}
		return "Open date & time settings"
	case "autostart":
		if !autostartSupported() {
			return ""
		}
		if autoStartEnabled(appConfig) {
			return "Turn it off"
		}
		return "Turn it on"
	case "theme":
		return "Preview next colour"
	case "roms":
		return "Scan ROM folders"
	case "artwork":
		return "Fetch game artwork"
	case "tools":
		return "Open Tools"
	default:
		return ""
	}
}

func onboardingStatus(config *Config, kind string) string {
	switch kind {
	case "name":
		return onboardingNameStatus(config)
	case "wifi":
		return "Network: " + NetworkStatus()
	case "timezone":
		return "Current time zone: " + dateTimeZoneLabel(time.Now())
	case "autostart":
		return onboardingAutoStartStatus(config)
	case "theme":
		if name, ok := customString(config.Variables.Custom, "theme_preset"); ok && name != "" {
			return "Current theme: " + GetThemePreset(name).Name
		}
		return "Current theme: Hub"
	case "roms":
		if onboarding.roms == nil {
			return "ROM scan has not run yet."
		}
		return "Found " + pluralizeCount(len(onboarding.roms.Games), "ROM", "ROMs") + " across " + pluralizeCount(len(onboarding.roms.Counts), "system", "systems") + "."
	case "artwork":
		return artworkFetchSummary()
	case "tools":
		// Both pages are read-only, so the step reports their live state instead
		// of describing it: the install count comes off the card and the posture
		// summary is the same read the Tools row shows.
		if config == nil {
			return "Toolbox: " + toolboxSummaryValue()
		}
		posture, _ := securityPostureValue(config)
		return "Toolbox: " + toolboxSummaryValue() + " · Security: " + posture
	default:
		return ""
	}
}

// openSystemSettingsPage opens the operating system's own Wi-Fi ("wifi"), date
// and time ("timezone") or Bluetooth ("bluetooth") page: a Settings deep link
// on Windows and macOS, the desktop's settings app on a Linux desktop. It fails
// where there is no such page — the handheld and any Linux without those tools,
// where JukaHub's own screens answer instead (openFirstRunSettings, the
// Bluetooth page).
func openSystemSettingsPage(kind string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "windows":
		command = "rundll32"
		target := "ms-settings:network-wifi"
		switch kind {
		case "timezone":
			target = "ms-settings:dateandtime"
		case "bluetooth":
			target = "ms-settings:bluetooth"
		}
		args = []string{"url.dll,FileProtocolHandler", target}
	case "darwin":
		command = "open"
		target := "x-apple.systempreferences:com.apple.preference.network"
		switch kind {
		case "timezone":
			target = "x-apple.systempreferences:com.apple.Date-Time-Settings.extension"
		case "bluetooth":
			// System Settings' pane (macOS 13 and later); an older macOS opens
			// System Preferences at its start instead.
			target = "x-apple.systempreferences:com.apple.BluetoothSettings"
		}
		args = []string{target}
	default:
		command, args = linuxDesktopSettingsCommand(kind)
		if command == "" {
			return fmt.Errorf("no system settings app here")
		}
	}
	cmd := hiddenCommand(command, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// linuxDesktopSettingsCommand is the desktop tool for a system page on a real
// Linux desktop: GNOME's settings for any of them, NetworkManager's editor for
// Wi-Fi and Blueman for Bluetooth when GNOME's is missing. Nothing on the
// handheld (which has none of them) or without a graphical session.
func linuxDesktopSettingsCommand(kind string) (string, []string) {
	if IsTrimuiDevice() || (os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "") {
		return "", nil
	}
	if app := linuxDesktopSettingsApp(); app != "" {
		page := "wifi"
		switch kind {
		case "timezone":
			page = "datetime"
		case "bluetooth":
			page = "bluetooth"
		}
		return app, []string{page}
	}
	fallback := ""
	switch kind {
	case "wifi":
		fallback = "nm-connection-editor"
	case "bluetooth":
		fallback = "blueman-manager"
	}
	if fallback != "" {
		if p, err := exec.LookPath(fallback); err == nil && p != "" {
			return fallback, nil
		}
	}
	return "", nil
}

// openFirstRunSettings answers the tour's Wi-Fi and time-zone buttons and
// returns the line the card shows. The system's page opens where there is one;
// elsewhere — the handheld, any Linux without desktop tools — JukaHub's own
// Wi-Fi page and time-zone picker do, and the tour waits until the user is back
// (toolsHubTourMark, onboardingCoveredByOverlay).
func openFirstRunSettings(config *Config, kind string) (string, error) {
	if err := openSystemSettingsPage(kind); err == nil {
		return "System settings opened. Return here when you are ready.", nil
	} else if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return "", err
	}
	toolsHubTourMark()
	switch kind {
	case "wifi":
		wifiScrOpen(config)
		return "Wi-Fi is open. The tour continues when you come back.", nil
	case "timezone":
		dateTimeOpenSettings(config, true)
		return "Pick your zone; the tour continues when you come back.", nil
	}
	return "", fmt.Errorf("nothing to open for %s", kind)
}

func onboardingPerformAction(config *Config) {
	if config == nil || onboarding.step < 0 || onboarding.step >= len(onboardingSteps) {
		return
	}
	kind := onboardingSteps[onboarding.step].kind
	switch kind {
	case "name":
		// The keyboard is a modal: the tour steps aside until it closes.
		onboardingNameOpen()
	case "wifi", "timezone":
		info, err := openFirstRunSettings(config, kind)
		if err != nil {
			onboarding.info = err.Error()
			ShowToast(err.Error(), ToastKindInfo)
			return
		}
		onboarding.info = info
	case "autostart":
		onboarding.info = onboardingAutoStartToggle(config)
	case "theme":
		names := onboardingThemeChoices[:]
		if len(names) == 0 {
			return
		}
		current, _ := customString(config.Variables.Custom, "theme_preset")
		if current == "" {
			current = "hub"
		}
		index := 0
		for i, name := range names {
			if name == current {
				index = (i + 1) % len(names)
				break
			}
		}
		applyThemePreset(config, names[index], false)
		onboarding.info = "Previewing " + GetThemePreset(names[index]).Name + "."
	case "roms":
		report := scanROMLibrary(romLibraryRoots())
		onboarding.roms = &report
		onboarding.info = "Scan complete: " + pluralizeCount(len(report.Games), "ROM", "ROMs") + " across " + pluralizeCount(len(report.Counts), "system", "systems") + "."
	case "artwork":
		if onboarding.roms == nil {
			report := scanROMLibrary(romLibraryRoots())
			onboarding.roms = &report
		}
		if len(onboarding.roms.Games) == 0 {
			onboarding.info = "No supported ROMs found. Scan folders after adding games."
			return
		}
		// The library's own path (LIB-19): only the games with no cover yet,
		// every console in turns, and the not-found cache, so a second press
		// does not ask the server about the same titles again.
		missing := romsMissingArtwork(onboarding.roms.Games, romArtworkDir())
		if len(missing) == 0 {
			onboarding.info = "Every game already has box art."
			return
		}
		if startLibraryArtworkFetch(artworkFetchOrder(missing, "")) {
			onboarding.info = "Fetching " + pluralizeCount(len(missing), "missing cover", "missing covers") + " in the background."
		} else {
			onboarding.info = "An artwork fetch is already running."
		}
	case "tools":
		// Tools is a page: the tour steps aside while the user is on it
		// (onboardingCoveredByOverlay) and comes back to this step on the
		// screen it was drawn over — the same handoff the "Get set up"
		// checklist uses.
		toolsHubTourMark()
		toolsHubOpen(config)
		onboarding.info = "Tools is open: Toolbox and Security & trust are under TOOLKITS."
	}
	PlayActivateSound()
}

func onboardingBack() {
	if onboarding.step > 0 {
		onboarding.step--
		onboarding.action = sdl.Rect{}
		onboarding.enter.begin(uiClockMS(), 180)
		if onboardingStepIsSetup() {
			onboardingSetupRefresh(appConfig, true)
		}
		if onboardingLangStepIs() {
			onboardingLangSync()
		}
		if onboardingLayoutStepIs() {
			onboardingLayoutReset(appConfig)
		}
		PlayNavSound()
	}
}

func handleOnboardingKey(e *sdl.KeyboardEvent) bool {
	if !onboarding.active || e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	if onboardingCoveredByOverlay() {
		return false
	}
	setup := onboardingStepIsSetup()
	if setup {
		switch e.Keysym.Sym {
		case sdl.K_UP:
			onboardingSetupMove(-1)
			return true
		case sdl.K_DOWN:
			onboardingSetupMove(1)
			return true
		}
	}
	if onboardingLangStepIs() {
		switch e.Keysym.Sym {
		case sdl.K_UP:
			onboardingLangMove(-1)
			return true
		case sdl.K_DOWN:
			onboardingLangMove(1)
			return true
		case sdl.K_f:
			return true
		}
	}
	if onboardingLayoutStepIs() {
		switch e.Keysym.Sym {
		case sdl.K_UP:
			onboardingLayoutMove(-1)
			return true
		case sdl.K_DOWN:
			onboardingLayoutMove(1)
			return true
		}
	}
	switch e.Keysym.Sym {
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		if setup {
			onboardingSetupActivate(appConfig)
			break
		}
		if onboardingNameAOpens(appConfig) {
			onboardingPerformAction(appConfig)
			break
		}
		onboardingAdvance(appConfig)
	case sdl.K_f:
		onboardingPerformAction(appConfig)
	case sdl.K_RIGHT:
		if !onboardingLayoutStepIs() {
			onboardingAdvance(appConfig)
		}
	case sdl.K_LEFT:
		onboardingBack()
	case sdl.K_ESCAPE, sdl.K_b:
		onboardingFinish(appConfig, true)
		PlayBackSound()
	default:
		return true
	}
	return true
}

func handleOnboardingController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if !onboarding.active || e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	if onboardingCoveredByOverlay() {
		return false
	}
	setup := onboardingStepIsSetup()
	if setup {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			onboardingSetupMove(-1)
			return true
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			onboardingSetupMove(1)
			return true
		}
	}
	if onboardingLangStepIs() {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			onboardingLangMove(-1)
			return true
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			onboardingLangMove(1)
			return true
		case sdl.CONTROLLER_BUTTON_X:
			return true
		}
	}
	if onboardingLayoutStepIs() {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			onboardingLayoutMove(-1)
			return true
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			onboardingLayoutMove(1)
			return true
		}
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A:
		if setup {
			onboardingSetupActivate(config)
			break
		}
		if onboardingNameAOpens(config) {
			onboardingPerformAction(config)
			break
		}
		onboardingAdvance(config)
	case sdl.CONTROLLER_BUTTON_START:
		if !onboardingLayoutStepIs() {
			onboardingAdvance(config)
		}
	case sdl.CONTROLLER_BUTTON_X:
		if setup {
			onboardingSetupActivate(config)
			break
		}
		onboardingPerformAction(config)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		onboardingBack()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if !onboardingLayoutStepIs() {
			onboardingAdvance(config)
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		onboardingFinish(config, true)
		PlayBackSound()
	default:
		return true
	}
	return true
}

func handleOnboardingMouse(mx, my int32, config *Config) bool {
	if !onboarding.active {
		return false
	}
	if onboardingCoveredByOverlay() {
		return false
	}
	if onboardingStepIsSetup() && onboardingSetupClick(mx, my, config) {
		return true
	}
	if onboardingLangStepIs() && onboardingLangClick(mx, my) {
		return true
	}
	if onboardingLayoutStepIs() && onboardingLayoutClick(mx, my) {
		return true
	}
	if pointInRect(mx, my, onboarding.action) {
		onboardingPerformAction(config)
		return true
	}
	if pointInRect(mx, my, onboarding.next) {
		onboardingAdvance(config)
		return true
	}
	if pointInRect(mx, my, onboarding.skip) {
		onboardingFinish(config, true)
		PlayBackSound()
		return true
	}
	return true
}

// The tour card's geometry in canvas pixels. The Y distances from the bottom
// are measured up from the card's bottom edge: Skip and Next end 28px above
// it, the progress dots sit at onbDotsY and a step's action button (when it
// has one) at onbActionY, above the dots.
const (
	onbCardW       = int32(820)
	onbCardPlainH  = int32(470)
	onbCardListH   = int32(600)
	onbCoinR       = int32(38)
	onbCoinY       = int32(100)
	onbTitleY      = int32(78)
	onbDotsY       = int32(92)
	onbActionY     = int32(148)
	onbActionH     = int32(42)
	onbButtonH     = int32(50)
	onbContentGap  = int32(14)
	onbRowGap      = int32(8)
	onbRowMaxPitch = int32(72)
)

// onbTextGaps are the gaps above a text step's hint, status and info blocks.
var onbTextGaps = [...]int32{14, 8, 6}

// onbTextCuts are the line budgets onboardingTextBlocks tries in turn, as
// body, hint, status and info: everything, then the hint's second line, the
// info's, the status's, the body's fourth and third lines, the hint, the
// body's second line, and on the shortest canvas the status.
var onbTextCuts = [...][4]int{
	{4, 2, 2, 2}, {4, 1, 2, 2}, {4, 1, 2, 1}, {4, 1, 1, 1}, {3, 1, 1, 1}, {2, 1, 1, 1}, {2, 0, 1, 1},
	{1, 0, 1, 1}, {1, 0, 0, 1},
}

// onbTextBlocks are a text step's blocks, wrapped to the text column, in the
// order they are drawn: the body, the hint, the live status line and the
// result of the last action.
type onbTextBlocks struct {
	body, hint, status, info []string
}

// onbBlockH is the height of n lines drawn lineH apart; the last line's
// leading is not part of the block.
func onbBlockH(n int, lineH, leading int32) int32 {
	if n <= 0 {
		return 0
	}
	return int32(n)*lineH - leading
}

// height is how tall the blocks stand when draw stacks them.
func (b onbTextBlocks) height(bodyFont, smallFont *ttf.Font) int32 {
	h := onbBlockH(len(b.body), int32(bodyFont.Height())+6, 6)
	for i, block := range [...][]string{b.hint, b.status, b.info} {
		if len(block) == 0 {
			continue
		}
		if h > 0 {
			h += onbTextGaps[i]
		}
		h += onbBlockH(len(block), int32(smallFont.Height())+4, 4)
	}
	return h
}

// draw stacks the blocks from y down: the body in the body face, then the
// hint, status and info lines in the small face, each in its own ink.
func (b onbTextBlocks) draw(renderer *sdl.Renderer, config *Config, x, y int32, bodyFont, smallFont *ttf.Font, accent sdl.Color) {
	top := y
	for _, line := range b.body {
		renderText(renderer, config, bodyFont, line, ColorTextSecondary(), x, y)
		y += int32(bodyFont.Height()) + 6
	}
	if len(b.body) > 0 {
		y -= 6
	}
	inks := [...]sdl.Color{ColorTextTertiary(), ColorTextSecondary(), accent}
	for i, block := range [...][]string{b.hint, b.status, b.info} {
		if len(block) == 0 {
			continue
		}
		if y > top {
			y += onbTextGaps[i]
		}
		for _, line := range block {
			renderText(renderer, config, smallFont, line, inks[i], x, y)
			y += int32(smallFont.Height()) + 4
		}
		y -= 4
	}
}

// onboardingTextBlocks wraps a text step's blocks to width, giving up lines
// in onbTextCuts' order until they fit in room; the last cut is kept when
// none does.
func onboardingTextBlocks(step onboardingStep, status, info string, bodyFont, smallFont *ttf.Font, width, room int32) onbTextBlocks {
	var b onbTextBlocks
	for _, cut := range onbTextCuts {
		b = onbTextBlocks{
			body:   onboardingWrap(bodyFont, step.body, width, cut[0]),
			hint:   onboardingWrap(smallFont, step.hint, width, cut[1]),
			status: onboardingWrap(smallFont, status, width, cut[2]),
			info:   onboardingWrap(smallFont, info, width, cut[3]),
		}
		if b.height(bodyFont, smallFont) <= room {
			break
		}
	}
	return b
}

// onbWrapKey and onbWrapMemo remember the tour's wrapped text: the card draws
// the same few sentences every frame, and a wrap measures word by word.
type onbWrapKey struct {
	font     *ttf.Font
	text     string
	width    int32
	maxLines int
}

var onbWrapMemo = map[onbWrapKey][]string{}

// onboardingWrap is wrapTextLines remembered by font, translated text, width
// and line budget. Blank text is no lines.
func onboardingWrap(font *ttf.Font, text string, width int32, maxLines int) []string {
	if font == nil || maxLines <= 0 || strings.TrimSpace(text) == "" {
		return nil
	}
	text = uiT(text)
	key := onbWrapKey{font: font, text: text, width: width, maxLines: maxLines}
	if lines, ok := onbWrapMemo[key]; ok {
		return lines
	}
	if len(onbWrapMemo) >= 96 {
		onbWrapMemo = map[onbWrapKey][]string{}
	}
	lines := wrapTextLinesRaw(font, text, width, maxLines)
	onbWrapMemo[key] = lines
	return lines
}

// onbRowFit lays out a column of tour rows: the distance from one row's top
// to the next, a row's height, and what a row shows: its title over a
// detail line (lines 2), the title alone in the body face (1), or the title
// in the small face (0, the last resort on a short card at a large text
// size).
type onbRowFit struct {
	pitch, rowH int32
	lines       int
}

// onboardingRowFit fits n rows between top and bottom by measuring the type.
// The rows used to switch to two lines at a fixed 50px; with the config's
// 24px fonts that put a 30px title over a 30px detail in a 53px row, and the
// detail ran across the border into the next row.
func onboardingRowFit(n int, top, bottom int32, bodyFont, smallFont *ttf.Font) onbRowFit {
	if n <= 0 || bodyFont == nil || smallFont == nil {
		return onbRowFit{}
	}
	bodyH, smallH := int32(bodyFont.Height()), int32(smallFont.Height())
	pitch := min((bottom-top+onbRowGap)/int32(n), onbRowMaxPitch)
	fit := onbRowFit{pitch: pitch, rowH: pitch - onbRowGap}
	switch {
	case fit.rowH >= bodyH+smallH+12:
		fit.lines = 2
	case fit.rowH >= bodyH+10:
		fit.lines = 1
	default:
		// Never shorter than its text: rows that touch read better than a
		// label drawn over a border.
		fit.rowH = max(fit.rowH, smallH+6)
		fit.pitch = max(fit.pitch, fit.rowH+2)
	}
	return fit
}

// onbRowsNeed is the height n rows take at their roomy two-line size, which
// a list step's card grows to when the screen has the room.
func onbRowsNeed(n int, bodyFont, smallFont *ttf.Font) int32 {
	if n <= 0 {
		return 0
	}
	row := int32(bodyFont.Height()) + int32(smallFont.Height()) + 16
	return int32(n)*(row+onbRowGap) - onbRowGap
}

// onbRowText draws a row's title, and its detail when the fit has room for
// it, vertically centred in rect from x.
func onbRowText(renderer *sdl.Renderer, config *Config, rect sdl.Rect, fit onbRowFit, x, width int32, title, detail string, ink sdl.Color, bodyFont, smallFont *ttf.Font) {
	bodyH, smallH := int32(bodyFont.Height()), int32(smallFont.Height())
	switch {
	case fit.lines == 2 && detail != "":
		y := rect.Y + (rect.H-bodyH-2-smallH)/2
		renderText(renderer, config, bodyFont, ellipsize(bodyFont, title, width), ink, x, y)
		renderText(renderer, config, smallFont, ellipsize(smallFont, detail, width), ColorTextTertiary(), x, y+bodyH+2)
	case fit.lines >= 1:
		renderText(renderer, config, bodyFont, ellipsize(bodyFont, title, width), ink, x, rect.Y+(rect.H-bodyH)/2)
	default:
		renderText(renderer, config, smallFont, ellipsize(smallFont, title, width), ink, x, rect.Y+(rect.H-smallH)/2)
	}
}

func renderOnboarding(renderer *sdl.Renderer, config *Config) {
	if !onboarding.active || renderer == nil || config == nil {
		return
	}
	// A panel the checklist opened is on screen: it belongs on top, and it owns
	// the input until it closes.
	if onboardingCoveredByOverlay() {
		return
	}
	if onboarding.step < 0 {
		onboarding.step = 0
	}
	if onboarding.step >= len(onboardingSteps) {
		onboarding.step = len(onboardingSteps) - 1
	}
	step := onboardingSteps[onboarding.step]

	// The design type scale (UX-1), as every panel draws. The tour asked for
	// the config's "large", "medium" and "small" fonts, which the shipped
	// config leaves at one 24px default: each hint was as big as the title and
	// ran off the card, a button cut its label, and a row drew its second line
	// across its own border.
	titleFont := FontForRole(config, TypeTitle)
	bodyFont := FontForRole(config, TypeBody)
	smallFont := FontForRole(config, TypeLabel)
	if titleFont == nil || bodyFont == nil || smallFont == nil {
		return
	}
	titleH := int32(titleFont.Height())
	bodyLineH := int32(bodyFont.Height()) + 6

	// Dim the screen and focus the card.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(2, 5, 12, 190)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	cardW := onbCardW
	if cardW > screenWidth-80 {
		cardW = screenWidth - 80
	}
	if cardW < 420 {
		cardW = screenWidth - 32
	}

	// The text column starts beside the coin on a wide card and under it on a
	// narrow one. Everything in it is measured before the card's height is
	// chosen, so the card grows to its text rather than the text running past
	// the card.
	wide := cardW >= 680
	textOff, titleTop := int32(52), onbTitleY
	if wide {
		textOff = 52 + 2*onbCoinR + 34
	} else {
		titleTop = onbCoinY + onbCoinR + 14
	}
	textW := cardW - 44 - textOff
	var titleLines []string
	if step.kind == "language" {
		// Its own table, in the highlighted language, drawn raw.
		titleLines = []string{ellipsize(titleFont, onbLangTitle(), textW)}
	} else {
		titleLines = onboardingWrap(titleFont, step.title, textW, 2)
	}
	bodyTop := titleTop + max(int32(len(titleLines)), 1)*titleH + 12

	// The band under the text: the action button when the step has one, the
	// dots, then Skip and Next. The controls hint goes below the card, so the
	// card never takes that room.
	actionLabel := onboardingActionLabel(step.kind)
	band := onbDotsY + onbContentGap
	if actionLabel != "" {
		band = onbActionY + onbContentGap
	}
	maxH := screenHeight - 2*(int32(smallFont.Height())+16)
	if maxH < 240 {
		maxH = screenHeight - 16
	}

	cardH := onbCardPlainH
	var blocks onbTextBlocks
	var lead []string // a list step's sentence above its rows
	rowsTop := bodyTop
	switch step.kind {
	case "language":
		cardH = onbCardListH
	case "start", "setup", "layout":
		n := len(homeArrangementChoiceList)
		if step.kind != "layout" {
			n = len(onboardingSetupRefresh(config, false))
		}
		if step.kind != "setup" {
			lead = onboardingWrap(bodyFont, step.body, textW, 2)
			if len(lead) > 0 {
				rowsTop += onbBlockH(len(lead), bodyLineH, 6) + onbContentGap
			}
		}
		cardH = max(onbCardListH, rowsTop+onbRowsNeed(n, bodyFont, smallFont)+band)
	default:
		// Every line while the card can grow to hold it; past that, the
		// blocks give up lines (onbTextCuts) rather than spill.
		status := onboardingStatus(config, step.kind)
		blocks = onboardingTextBlocks(step, status, onboarding.info, bodyFont, smallFont, textW, maxH-band-bodyTop)
		cardH = max(cardH, bodyTop+blocks.height(bodyFont, smallFont)+band)
	}
	cardH = min(cardH, maxH)

	cx := (screenWidth - cardW) / 2
	cy := (screenHeight - cardH) / 2
	if cy < 8 {
		cy = 8
	}

	progress := 1.0
	if !effectiveReducedMotion(config) {
		progress = onboarding.enter.progress(uiClockMS())
	}
	rise := int32((1 - progress) * 18)
	cy += rise

	// What the frame is built on.
	onboarding.card = sdl.Rect{X: cx, Y: cy, W: cardW, H: cardH}

	// Soft shadow and card.
	fillRoundedRect(renderer, cx+2, cy+6, cardW, cardH, 22, sdl.Color{R: 0, G: 0, B: 0, A: 90})
	fillRoundedRect(renderer, cx, cy, cardW, cardH, 22, ColorSurfacePanel)
	strokeRoundedRect(renderer, cx, cy, cardW, cardH, 22, 1, WithAlpha(ColorBorder, 220))

	// Accent rail and step counter.
	accent := getAccentColor(config)
	fillRoundedRect(renderer, cx+22, cy+26, 6, cardH-52, 3, accent)
	stepLabel := "STEP " + itoa(onboarding.step+1) + " OF " + itoa(len(onboardingSteps))
	renderText(renderer, config, smallFont, stepLabel, WithAlpha(accent, 230), cx+52, cy+28)

	// Large sequence coin.
	coinX := cx + 52 + onbCoinR
	coinY := cy + onbCoinY
	fillCircle(renderer, coinX, coinY, onbCoinR, WithAlpha(accent, 28))
	strokeRoundedRect(renderer, coinX-onbCoinR, coinY-onbCoinR, onbCoinR*2, onbCoinR*2, onbCoinR, 2, WithAlpha(accent, 200))
	num := itoa(onboarding.step + 1)
	nw, nh, _ := titleFont.SizeUTF8(num)
	renderText(renderer, config, titleFont, num, accent,
		coinX-int32(nw)/2, coinY-int32(nh)/2)

	textX := cx + textOff
	for i, line := range titleLines {
		if step.kind == "language" {
			renderTextRaw(renderer, config, titleFont, line, ColorTextPrimary(), textX, cy+titleTop)
			continue
		}
		renderText(renderer, config, titleFont, line, ColorTextPrimary(), textX, cy+titleTop+int32(i)*titleH)
	}

	// The rows end where the dots' band begins.
	rowsBottom := cy + cardH - onbDotsY - onbContentGap
	onboarding.bodyLines = lead
	for i, line := range lead {
		renderText(renderer, config, bodyFont, line, ColorTextSecondary(), textX, cy+bodyTop+int32(i)*bodyLineH)
	}
	switch step.kind {
	case "start", "setup":
		onboardingRenderSetup(renderer, config, cy, cx, cardW, textX, textW, cy+rowsTop, rowsBottom, bodyFont, smallFont, accent)
	case "language":
		onboardingRenderLanguage(renderer, config, cy, cardH, textX, textW, cy+bodyTop, bodyFont, smallFont, accent)
	case "layout":
		onboardingRenderLayout(renderer, config, textX, textW, cy+rowsTop, rowsBottom, bodyFont, smallFont, accent)
	default:
		onboarding.bodyLines = blocks.body
		blocks.draw(renderer, config, textX, cy+bodyTop, bodyFont, smallFont, accent)
	}

	// Progress dots.
	dotY := cy + cardH - onbDotsY
	for i := range onboardingSteps {
		dx := textX + int32(i)*20
		if i == onboarding.step {
			fillRoundedRect(renderer, dx, dotY, 18, 6, 3, accent)
		} else {
			fillCircle(renderer, dx+4, dotY+3, 3, WithAlpha(ColorTextTertiary(), 140))
		}
	}
	onboarding.action = sdl.Rect{}
	if actionLabel != "" {
		// As wide as its label needs: a fixed 300px cut "Open date & time
		// settings" to "Open date & time settin…".
		lw, _ := measureText(smallFont, actionLabel)
		actionW := min(max(int32(300), lw+48), textW)
		onboarding.action = UIButton(renderer, config, sdl.Rect{X: textX, Y: cy + cardH - onbActionY, W: actionW, H: onbActionH}, actionLabel, UIButtonStyle{
			Variant:    UIButtonGhost,
			LabelColor: accent,
			Font:       smallFont,
			Radius:     RadiusMD,
		}, UIButtonState{})
	}

	// Skip and Next buttons, each at least as wide as its label.
	nextLabel := "Next"
	if onboarding.step == len(onboardingSteps)-1 {
		nextLabel = "Start exploring"
	} else if step.kind == "layout" {
		nextLabel = "Use this layout"
	}
	nextTextW, _ := measureText(bodyFont, nextLabel)
	btnW := max(int32(210), nextTextW+56)
	nextX := cx + cardW - 28 - btnW
	nextY := cy + cardH - 28 - onbButtonH
	onboarding.next = UIButton(renderer, config, sdl.Rect{X: nextX, Y: nextY, W: btnW, H: onbButtonH}, nextLabel, UIButtonStyle{
		Variant: UIButtonCustom,
		Fill:    accent,
		Font:    bodyFont,
		Radius:  RadiusMD,
	}, UIButtonState{})

	skipLabel := "Skip tour"
	skipTextW, _ := measureText(smallFont, skipLabel)
	skipW := max(int32(120), skipTextW+32)
	skipX := nextX - 14 - skipW
	skipY := nextY + (onbButtonH-36)/2
	onboarding.skip = UIButton(renderer, config, sdl.Rect{X: skipX, Y: skipY, W: skipW, H: 36}, skipLabel, UIButtonStyle{
		Variant:    UIButtonGhost,
		LabelColor: ColorTextTertiary(),
		Font:       smallFont,
		Radius:     RadiusSM,
		NoShadow:   true,
	}, UIButtonState{})

	// Footer hint. Use words, not key glyphs, so it survives any controller
	// mapping. It sits just below the card, in the dimmed band the card's
	// height leaves free (maxH above): centred, with nothing to collide with.
	footer := hintLine(FooterHint{"Left/Right", "Move"}, FooterHint{"A / Enter", "Next"},
		FooterHint{"F / X", "Action"}, FooterHint{"B", "Skip"})
	if step.kind == "setup" {
		footer = hintLine(FooterHint{"Up/Down", "Choose"}, FooterHint{"A / Enter", "Open"},
			FooterHint{"B", "Done"})
	}
	if step.kind == "start" {
		footer = hintLine(FooterHint{"Up/Down", "Choose"}, FooterHint{"A / Enter", "Go"},
			FooterHint{"B", "Skip"})
	}
	if step.kind == "language" {
		footer = hintLine(FooterHint{"Up/Down", "Choose"}, FooterHint{"A / Enter", "Next"},
			FooterHint{"B", "Skip"})
	}
	if step.kind == "name" {
		footer = hintLine(FooterHint{"Left/Right", "Move"}, FooterHint{"A / Enter", onboardingNameAHint(config)},
			FooterHint{"F / X", "Type name"}, FooterHint{"B", "Skip"})
	}
	if step.kind == "layout" {
		footer = hintLine(FooterHint{"Up/Down", "Choose"}, FooterHint{"A / Enter", "Use this layout"},
			FooterHint{"B", "Skip"})
	}
	fw, fh, _ := smallFont.SizeUTF8(footer)
	footerY := cy + cardH + 10
	if limit := screenHeight - 6 - int32(fh); footerY > limit {
		footerY = limit
	}
	onboarding.footer = sdl.Rect{X: cx + (cardW-int32(fw))/2, Y: footerY, W: int32(fw), H: int32(fh)}
	renderText(renderer, config, smallFont, footer, WithAlpha(ColorTextTertiary(), 170),
		onboarding.footer.X, footerY)
}

// onboardingRenderSetup draws the quick start's choices and the "Get set up"
// checklist: one row per entry between top and bottom, each with its live
// state. The checklist's summary sits beside the step counter so progress is
// visible before any row is read.
func onboardingRenderSetup(renderer *sdl.Renderer, config *Config, cy, cx, cardW, textX, textW, top, bottom int32, bodyFont, smallFont *ttf.Font, accent sdl.Color) {
	rows := onboardingSetupRefresh(config, false)
	onboarding.setupHits = onboarding.setupHits[:0]
	if len(rows) == 0 {
		return
	}
	if summary := setupSummary(rows); summary != "" && onboardingSteps[onboarding.step].kind == "setup" {
		w, _, _ := smallFont.SizeUTF8(summary)
		renderText(renderer, config, smallFont, summary, ColorTextSecondary(),
			cx+cardW-52-int32(w), cy+28)
	}

	fit := onboardingRowFit(len(rows), top, bottom, bodyFont, smallFont)
	for i, row := range rows {
		rect := sdl.Rect{X: textX - 10, Y: top + int32(i)*fit.pitch, W: textW + 20, H: fit.rowH}
		onboarding.setupHits = append(onboarding.setupHits, rect)
		UIListRow(renderer, rect, RadiusMD, i == onboarding.setupCursor, false)

		// A filled tick means done; a hollow ring means this row is still open.
		dotX := rect.X + 26
		dotY := rect.Y + rect.H/2
		if row.Done {
			fillCircle(renderer, dotX, dotY, 7, WithAlpha(accent, 235))
			setDrawColor(renderer, ColorTopBar)
			renderer.DrawLine(dotX-3, dotY, dotX-1, dotY+3)
			renderer.DrawLine(dotX-1, dotY+3, dotX+4, dotY-3)
		} else {
			strokeCircle(renderer, dotX, dotY, 7, WithAlpha(ColorTextTertiary(), 170))
		}

		labelX := dotX + 18
		titleColor := ColorTextPrimary()
		if !row.Done {
			titleColor = ColorTextSecondary()
		}
		onbRowText(renderer, config, rect, fit, labelX, rect.X+rect.W-14-labelX, row.Title, row.Detail, titleColor, bodyFont, smallFont)
	}
}

// itoa is a tiny local helper: the tour only formats small positive integers,
// and importing strconv would add noise for two calls.
func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// onboardingEntryLabel keeps the Settings row readable even if the user's
// config already has a button with the same position.
const onboardingEntryLabel = "Replay tour"

// ensureOnboardingEntry adds a replay button to Settings → General. The tour
// itself is skippable, but a replay keeps it discoverable after first run.
func ensureOnboardingEntry(config *Config) {
	scene := settingsGeneralScene(config)
	if scene == nil {
		return
	}
	for _, el := range scene.Elements {
		if el.Trigger == "onboarding_replay" {
			return
		}
	}
	// 340,520 is the screensaver row — this button used to land exactly on top
	// of it, so which of the two answered a tap depended on element order. It
	// now sits in the page's top-right slot, on the row the Fullscreen and
	// Weather toggles already share; the band above Save belongs to the cookie
	// rows.
	scene.Elements = append(scene.Elements, Element{
		Type:    "button",
		Text:    onboardingEntryLabel,
		X:       1040,
		Y:       96,
		Width:   "216",
		Height:  "48",
		Trigger: "onboarding_replay",
		Color:   "$labelColor",
		BgColor: "$buttonColor",
	})
}
