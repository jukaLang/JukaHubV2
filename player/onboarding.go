package main

// First-run guided tour. It is a global modal rather than a scene because it
// has to sit above whatever screen the user lands on and cannot be navigated
// away from by the normal element dispatcher.

import (
	"fmt"
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
		title: "Welcome to JukaHub",
		body:  "Let’s get your handheld ready. This short setup checks the basics, then shows you where everything lives.",
		hint:  "You can skip the setup or replay it later from Settings.",
	},
	{
		title: "Connect to Wi-Fi",
		body:  "Wi-Fi enables updates, weather, companion controls and artwork downloads. JukaHub will open your system network settings.",
		hint:  "Network status is shown below. Press F or select the button to open Wi-Fi settings.",
		kind:  "wifi",
	},
	{
		title: "Set your time zone",
		body:  "Correct local time keeps the clock, alarms and release dates consistent. This opens your device’s date and time settings.",
		hint:  "JukaHub reads the time zone from the operating system. Restart JukaHub if the clock does not update.",
		kind:  "timezone",
	},
	{
		title: "Choose a theme",
		body:  "Preview a complete palette now. You can change it again any time in Settings > Appearance.",
		hint:  "Press F or select the button to cycle through the built-in themes.",
		kind:  "theme",
	},
	{
		title: "Scan your ROMs",
		body:  "JukaHub checks the usual Roms folders for supported files. It only reads filenames and does not move or edit your games.",
		hint:  "Press F or select the button to scan again after adding games.",
		kind:  "roms",
	},
	{
		title: "Fetch game artwork",
		body:  "When online, JukaHub can look up box art from the public Libretro thumbnails collection and save matches to its data folder.",
		hint:  "This is optional. Unmatched titles are left unchanged.",
		kind:  "artwork",
	},
	{
		title: "Get set up",
		body:  "These links unlock the rest of JukaHub: achievements, video, Remote Play and your phone. Everything here is optional, and the tour comes back when you close a panel.",
		hint:  "Choose a row and press A to open it.",
		kind:  "setup",
	},
	{
		title: "Move around JukaHub",
		body:  "Use the D-pad or arrow keys to move, A or Enter to select, and B or Escape to go back. Home, Start and Tools make the main features easy to find.",
		hint:  "The on-screen footer shows the controls for each screen.",
	},
}

var onboarding = struct {
	active bool
	step   int
	enter  uiEnter
	next   sdl.Rect
	skip   sdl.Rect
	action sdl.Rect
	info   string
	roms   *romScanReport
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
func onboardingStartIfNeeded(config *Config) {
	if onboardingDone(config) {
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
	onboarding.info = ""
	onboarding.roms = nil
	onboarding.setupCursor = 0
	onboarding.setupRows = nil
	onboarding.setupHits = nil
	onboarding.setupAt = 0
}

func onboardingFinish(config *Config, skipped bool) {
	onboarding.active = false
	if config != nil {
		if config.Variables.Custom == nil {
			config.Variables.Custom = map[string]interface{}{}
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
}

func onboardingAdvance(config *Config) {
	if onboarding.step >= len(onboardingSteps)-1 {
		onboardingFinish(config, false)
		return
	}
	onboarding.step++
	onboarding.action = sdl.Rect{}
	onboarding.enter.begin(uiClockMS(), 180)
	// The checklist reads the device when it appears, so a step the user just
	// finished is never shown as still needed.
	if onboardingStepIsSetup() {
		onboardingSetupRefresh(config, true)
	}
	PlayNavSound()
}

// onboardingStepIsSetup reports whether the tour is showing its checklist page.
func onboardingStepIsSetup() bool {
	if onboarding.step < 0 || onboarding.step >= len(onboardingSteps) {
		return false
	}
	return onboardingSteps[onboarding.step].kind == "setup"
}

// ── "Get set up" checklist ──────────────────────────────────────────────

// onboardingCoveredByOverlay reports whether a modal panel is on top of the
// tour. The checklist opens those panels, so the tour stops drawing and stops
// consuming input while one is open instead of covering it.
func onboardingCoveredByOverlay() bool {
	return modalPanelBlocksShoulders()
}

// onboardingSetupRefresh rebuilds the checklist from live device state. The
// probes read the disk, so the render path refreshes at most twice a second;
// every input and every action refreshes immediately.
func onboardingSetupRefresh(config *Config, force bool) []setupRow {
	now := sdl.GetTicks64()
	if !force && onboarding.setupRows != nil && now-onboarding.setupAt < 500 {
		return onboarding.setupRows
	}
	onboarding.setupRows = setupChecklist(liveSetupEnv(config), setupActionsFor())
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
	case "wifi":
		return "Open Wi-Fi settings"
	case "timezone":
		return "Open date & time settings"
	case "theme":
		return "Preview next theme"
	case "roms":
		return "Scan ROM folders"
	case "artwork":
		return "Fetch game artwork"
	default:
		return ""
	}
}

func onboardingStatus(config *Config, kind string) string {
	switch kind {
	case "wifi":
		return "Network: " + NetworkStatus()
	case "timezone":
		return "Current time zone: " + time.Now().Location().String()
	case "theme":
		if name, ok := customString(config.Variables.Custom, "theme_preset"); ok && name != "" {
			return "Current theme: " + name
		}
		return "Current theme: dark"
	case "roms":
		if onboarding.roms == nil {
			return "ROM scan has not run yet."
		}
		return fmt.Sprintf("Found %d ROM(s) across %d system(s).", len(onboarding.roms.Games), len(onboarding.roms.Counts))
	case "artwork":
		return artworkFetchSummary()
	default:
		return ""
	}
}

func openFirstRunSettings(kind string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "windows":
		command = "rundll32"
		target := "ms-settings:network-wifi"
		if kind == "timezone" {
			target = "ms-settings:dateandtime"
		}
		args = []string{"url.dll,FileProtocolHandler", target}
	case "darwin":
		command = "open"
		target := "x-apple.systempreferences:com.apple.preference.network"
		if kind == "timezone" {
			target = "x-apple.systempreferences:com.apple.Date-Time-Settings.extension"
		}
		args = []string{target}
	default:
		command = "gnome-control-center"
		page := "wifi"
		if kind == "timezone" {
			page = "datetime"
		}
		if _, err := exec.LookPath(command); err == nil {
			args = []string{page}
		} else if kind == "wifi" {
			command = "nm-connection-editor"
		} else {
			return fmt.Errorf("open Date & Time from your device settings")
		}
	}
	cmd := exec.Command(command, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func onboardingPerformAction(config *Config) {
	if config == nil || onboarding.step < 0 || onboarding.step >= len(onboardingSteps) {
		return
	}
	kind := onboardingSteps[onboarding.step].kind
	switch kind {
	case "wifi", "timezone":
		if err := openFirstRunSettings(kind); err != nil {
			onboarding.info = err.Error()
			ShowToast(err.Error(), ToastKindInfo)
			return
		}
		onboarding.info = "System settings opened. Return here when you are ready."
	case "theme":
		names := ListThemePresets()
		if len(names) == 0 {
			return
		}
		current, _ := customString(config.Variables.Custom, "theme_preset")
		index := 0
		for i, name := range names {
			if name == current {
				index = (i + 1) % len(names)
				break
			}
		}
		applyThemePreset(config, names[index], false)
		onboarding.info = "Previewing " + names[index] + "."
	case "roms":
		report := scanROMLibrary(romLibraryRoots())
		onboarding.roms = &report
		onboarding.info = fmt.Sprintf("Scan complete: %d ROM(s) across %d system(s).", len(report.Games), len(report.Counts))
	case "artwork":
		if onboarding.roms == nil {
			report := scanROMLibrary(romLibraryRoots())
			onboarding.roms = &report
		}
		if len(onboarding.roms.Games) == 0 {
			onboarding.info = "No supported ROMs found. Scan folders after adding games."
			return
		}
		if startROMArtworkFetch(onboarding.roms.Games) {
			onboarding.info = "Artwork fetch started in the background."
		} else {
			onboarding.info = "An artwork fetch is already running."
		}
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
	switch e.Keysym.Sym {
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		if setup {
			onboardingSetupActivate(appConfig)
			break
		}
		onboardingAdvance(appConfig)
	case sdl.K_f:
		onboardingPerformAction(appConfig)
	case sdl.K_RIGHT:
		onboardingAdvance(appConfig)
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
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A:
		if setup {
			onboardingSetupActivate(config)
			break
		}
		onboardingAdvance(config)
	case sdl.CONTROLLER_BUTTON_START:
		onboardingAdvance(config)
	case sdl.CONTROLLER_BUTTON_X:
		if setup {
			onboardingSetupActivate(config)
			break
		}
		onboardingPerformAction(config)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		onboardingBack()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		onboardingAdvance(config)
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

	titleFont, _ := getCachedFont(config, "large")
	bodyFont, _ := getCachedFont(config, "medium")
	smallFont, _ := getCachedFont(config, "small")
	if titleFont == nil || bodyFont == nil || smallFont == nil {
		return
	}

	// Dim the screen and focus the card.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(2, 5, 12, 190)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	cardW := int32(820)
	if cardW > screenWidth-80 {
		cardW = screenWidth - 80
	}
	if cardW < 420 {
		cardW = screenWidth - 32
	}
	cardH := int32(470)
	if step.kind == "setup" {
		// The checklist needs a row per setting; the plain steps do not.
		cardH = 600
	}
	if cardH > screenHeight-100 {
		cardH = screenHeight - 72
	}
	cx := (screenWidth - cardW) / 2
	cy := (screenHeight - cardH) / 2
	if cy < 24 {
		cy = 24
	}

	progress := 1.0
	if !effectiveReducedMotion(config) {
		progress = onboarding.enter.progress(uiClockMS())
	}
	rise := int32((1 - progress) * 18)
	cy += rise

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
	coinR := int32(38)
	coinX := cx + 52 + coinR
	coinY := cy + 100
	fillCircle(renderer, coinX, coinY, coinR, WithAlpha(accent, 28))
	strokeRoundedRect(renderer, coinX-coinR, coinY-coinR, coinR*2, coinR*2, coinR, 2, WithAlpha(accent, 200))
	num := itoa(onboarding.step + 1)
	nw, nh, _ := titleFont.SizeUTF8(num)
	renderText(renderer, config, titleFont, num, accent,
		coinX-int32(nw)/2, coinY-int32(nh)/2)

	// Text column starts under the coin on narrow cards and beside it on wide
	// ones. The body wraps in pixels so no sentence spills past the card.
	textX := cx + 52
	if cardW >= 680 {
		textX = coinX + coinR + 34
	}
	textW := cx + cardW - 44 - textX
	titleY := cy + 74
	if cardW >= 680 {
		titleY = cy + 78
	}
	title := ellipsize(titleFont, step.title, textW)
	renderText(renderer, config, titleFont, title, ColorTextPrimary(), textX, titleY)

	bodyY := titleY + int32(titleFont.Height()) + 18
	if step.kind == "setup" {
		onboardingRenderSetup(renderer, config, cy, cx, cardW, cardH, textX, textW, bodyY, bodyFont, smallFont, accent)
	} else {
		lines := wrapMeasured([]rune(step.body), textW, func(s string) int32 {
			w, _, _ := bodyFont.SizeUTF8(s)
			return int32(w)
		})
		lineH := int32(bodyFont.Height()) + 6
		for i, line := range lines {
			renderText(renderer, config, bodyFont, line, ColorTextSecondary(), textX, bodyY+int32(i)*lineH)
		}

		hintY := bodyY + int32(len(lines))*lineH + 16
		if hintY < cy+cardH-140 {
			renderText(renderer, config, smallFont, step.hint, ColorTextTertiary(), textX, hintY)
		}
		status := onboardingStatus(config, step.kind)
		if status != "" {
			statusY := hintY + int32(smallFont.Height()) + 8
			if statusY < cy+cardH-162 {
				renderText(renderer, config, smallFont, ellipsize(smallFont, status, textW), ColorTextSecondary(), textX, statusY)
			}
		}
		if onboarding.info != "" {
			infoY := hintY + 2*int32(smallFont.Height()) + 14
			if infoY < cy+cardH-162 {
				renderText(renderer, config, smallFont, ellipsize(smallFont, onboarding.info, textW), accent, textX, infoY)
			}
		}
	}

	// Progress dots.
	dotY := cy + cardH - 92
	for i := range onboardingSteps {
		dx := textX + int32(i)*20
		if i == onboarding.step {
			fillRoundedRect(renderer, dx, dotY, 18, 6, 3, accent)
		} else {
			fillCircle(renderer, dx+4, dotY+3, 3, WithAlpha(ColorTextTertiary(), 140))
		}
	}
	onboarding.action = sdl.Rect{}
	if label := onboardingActionLabel(step.kind); label != "" {
		actionW := int32(300)
		if actionW > cardW-104 {
			actionW = cardW - 104
		}
		actionH := int32(42)
		actionY := cy + cardH - 150
		onboarding.action = UIButton(renderer, config, sdl.Rect{X: textX, Y: actionY, W: actionW, H: actionH}, label, UIButtonStyle{
			Variant:    UIButtonGhost,
			LabelColor: accent,
			Font:       smallFont,
			Radius:     RadiusMD,
		}, UIButtonState{})
	}

	// Skip and Next buttons.
	btnH := int32(50)
	btnW := int32(210)
	nextX := cx + cardW - 28 - btnW
	nextY := cy + cardH - 28 - btnH
	nextLabel := "Next"
	if onboarding.step == len(onboardingSteps)-1 {
		nextLabel = "Start exploring"
	}
	onboarding.next = UIButton(renderer, config, sdl.Rect{X: nextX, Y: nextY, W: btnW, H: btnH}, nextLabel, UIButtonStyle{
		Variant: UIButtonCustom,
		Fill:    accent,
		Font:    bodyFont,
		Radius:  RadiusMD,
	}, UIButtonState{})

	skipW := int32(120)
	skipX := nextX - 14 - skipW
	skipY := nextY + (btnH-36)/2
	skipLabel := "Skip tour"
	onboarding.skip = UIButton(renderer, config, sdl.Rect{X: skipX, Y: skipY, W: skipW, H: 36}, skipLabel, UIButtonStyle{
		Variant:    UIButtonGhost,
		LabelColor: ColorTextTertiary(),
		Font:       smallFont,
		Radius:     RadiusSM,
		NoShadow:   true,
	}, UIButtonState{})

	// Footer hint. Use words, not key glyphs, so it survives any controller
	// mapping.
	footer := hintLine(FooterHint{"Left / Right", "Move"}, FooterHint{"A / Enter", "Next"},
		FooterHint{"F / X", "Action"}, FooterHint{"B", "Skip"})
	footerY := cy + cardH - 18
	if step.kind == "setup" {
		footer = hintLine(FooterHint{"Up / Down", "Choose"}, FooterHint{"A / Enter", "Open"},
			FooterHint{"B", "Done"})
		footerY = cy + cardH - 26
	}
	fw, _, _ := smallFont.SizeUTF8(footer)
	renderText(renderer, config, smallFont, footer, WithAlpha(ColorTextTertiary(), 170),
		cx+(cardW-int32(fw))/2, footerY)
}

// onboardingRenderSetup draws the "Get set up" checklist: one row per optional
// link, each with its live state. The summary sits beside the step counter so
// progress is visible before any row is read.
func onboardingRenderSetup(renderer *sdl.Renderer, config *Config, cy, cx, cardW, cardH, textX, textW, bodyY int32, bodyFont, smallFont *ttf.Font, accent sdl.Color) {
	rows := onboardingSetupRefresh(config, false)
	onboarding.setupHits = onboarding.setupHits[:0]
	if len(rows) == 0 {
		return
	}
	if summary := setupSummary(rows); summary != "" {
		w, _, _ := smallFont.SizeUTF8(summary)
		renderText(renderer, config, smallFont, summary, ColorTextSecondary(),
			cx+cardW-52-int32(w), cy+28)
	}

	const gap = int32(10)
	top := bodyY - 6
	bottom := cy + cardH - 116
	pitch := int32(72)
	if n := int32(len(rows)); n > 0 {
		if fit := (bottom - top) / n; fit < pitch {
			pitch = fit
		}
	}
	rowH := pitch - gap
	if rowH < 26 {
		rowH = 26
	}
	if rowH > 62 {
		rowH = 62
	}

	for i, row := range rows {
		rect := sdl.Rect{X: textX - 10, Y: top + int32(i)*pitch, W: textW + 20, H: rowH}
		onboarding.setupHits = append(onboarding.setupHits, rect)
		UIListRow(renderer, rect, RadiusMD, i == onboarding.setupCursor, false)

		// A filled tick means done; a hollow ring means this row is still open.
		dotX := rect.X + 26
		dotY := rect.Y + rowH/2
		if row.Done {
			fillCircle(renderer, dotX, dotY, 7, WithAlpha(accent, 235))
			setDrawColor(renderer, ColorTopBar)
			renderer.DrawLine(dotX-3, dotY, dotX-1, dotY+3)
			renderer.DrawLine(dotX-1, dotY+3, dotX+4, dotY-3)
		} else {
			strokeCircle(renderer, dotX, dotY, 7, WithAlpha(ColorTextTertiary(), 170))
		}

		labelX := dotX + 18
		labelW := rect.X + rect.W - 14 - labelX
		titleColor := ColorTextPrimary()
		if !row.Done {
			titleColor = ColorTextSecondary()
		}
		if rowH >= 50 {
			renderText(renderer, config, bodyFont, ellipsize(bodyFont, row.Title, labelW), titleColor, labelX, rect.Y+6)
			detailY := rect.Y + 6 + int32(bodyFont.Height())
			renderText(renderer, config, smallFont, ellipsize(smallFont, row.Detail, labelW),
				ColorTextTertiary(), labelX, detailY)
			continue
		}
		// A short card shows one line per row rather than spilling the detail.
		renderText(renderer, config, smallFont, ellipsize(smallFont, row.Title, labelW), titleColor,
			labelX, rect.Y+(rowH-int32(smallFont.Height()))/2)
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
	scene.Elements = append(scene.Elements, Element{
		Type:    "button",
		Text:    onboardingEntryLabel,
		X:       340,
		Y:       520,
		Width:   "280",
		Height:  "48",
		Trigger: "onboarding_replay",
		Color:   "$labelColor",
		BgColor: "$buttonColor",
	})
}

// onboardingSummary is used by the tour's tests and by the diagnostic overlay.
func onboardingSummary() string {
	if len(onboardingSteps) == 0 {
		return "empty tour"
	}
	return strings.TrimSpace(onboardingSteps[0].title)
}
