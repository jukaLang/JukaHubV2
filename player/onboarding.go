package main

// First-run guided tour. It is a global modal rather than a scene because it
// has to sit above whatever screen the user lands on and cannot be navigated
// away from by the normal element dispatcher.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

type onboardingStep struct {
	title string
	body  string
	hint  string
}

var onboardingSteps = []onboardingStep{
	{
		title: "Welcome to JukaHub",
		body:  "Home is your launchpad. Movies, music, files, games and settings all start from here.",
		hint:  "You can leave this tour at any time.",
	},
	{
		title: "Move with the D-pad",
		body:  "Use the D-pad or arrow keys to move, A or Enter to select, and B or Escape to go back. The on-screen footer always shows the current controls.",
		hint:  "Every screen keeps the same A / B language.",
	},
	{
		title: "The top bar is alive",
		body:  "The clock opens the calendar. The bell opens notifications, downloads and update messages. Both work from any screen.",
		hint:  "Unread notifications show a red badge.",
	},
	{
		title: "Make it yours",
		body:  "Settings > Appearance changes button, label and input colours. Theme Presets has complete palettes, and Smart Widgets adds battery saver and time-of-day themes.",
		hint:  "Changes preview live before you save.",
	},
	{
		title: "There is more inside",
		body:  "Open the Start menu for Games, Music, Notes and Calculator. Tools adds terminal, file servers, power tools and device monitors.",
		hint:  "Have fun — and press F1 for shortcuts.",
	},
}

var onboarding = struct {
	active bool
	step   int
	enter  uiEnter
	next   sdl.Rect
	skip   sdl.Rect
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
}

func onboardingAdvance(config *Config) {
	if onboarding.step >= len(onboardingSteps)-1 {
		onboardingFinish(config, false)
		return
	}
	onboarding.step++
	onboarding.enter.begin(uiClockMS(), 180)
	PlayNavSound()
}

func onboardingBack() {
	if onboarding.step > 0 {
		onboarding.step--
		onboarding.enter.begin(uiClockMS(), 180)
		PlayNavSound()
	}
}

func handleOnboardingKey(e *sdl.KeyboardEvent) bool {
	if !onboarding.active || e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
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
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_START:
		onboardingAdvance(config)
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
	if cardH > screenHeight-100 {
		cardH = screenHeight - 72
	}
	cx := (screenWidth - cardW) / 2
	cy := (screenHeight - cardH) / 2
	if cy < 24 {
		cy = 24
	}

	progress := 1.0
	if !config.Variables.ReducedMotion {
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
	footer := "Left / Right to move • A or Enter to continue • B to skip"
	fw, _, _ := smallFont.SizeUTF8(footer)
	renderText(renderer, config, smallFont, footer, WithAlpha(ColorTextTertiary(), 170),
		cx+(cardW-int32(fw))/2, cy+cardH-18)
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
