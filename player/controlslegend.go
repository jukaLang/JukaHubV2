package main

// controlslegend.go — the controller legend.
//
// The "controller legend on first launch" idea, shipped (inventoried in
// MODERNIZATION.md §5.5): a first-run card built from the footerHints registry
// instead of a second hardcoded list, and the same card reachable any time as
// the "?" row in Quick Settings (qsLinkControls in quicksettings.go).
//
// Content: the bindings of the scene it opened over — footerHints already
// mirrors what each scene's input handlers really do, so the legend and the
// footer can never disagree — plus the three pad bindings that hold on every
// screen, and the two Tools pages a new user would otherwise only find in the
// docs. It opens once right after the first-run tour is completed
// (onboardingFinish), and B closes it.

import (
	"github.com/veandco/go-sdl2/sdl"
)

// legendCardSection groups the rows under a heading. The row vocabulary is
// FooterHint, so section rows and footer rows stay interchangeable.
type legendCardSection struct {
	Title string
	Rows  []FooterHint
}

type controlsLegendState struct {
	open bool
	// scene is the snapshot taken when the legend was opened, so the rows
	// answer "what do the buttons do here?" for the screen the user saw.
	scene SceneConfig
	// brick is set when the card opened on a Brick, whose front F1 / F2 keys
	// get rows of their own (brickkeys.go). Read once at open, not per frame.
	brick bool
}

var controlsLegend controlsLegendState

// systemLegendRows lists the bindings that hold on every screen, each tied to
// its owner: Start opens the menu on ordinary scenes (startmenu.go — the
// arcade pause menu and Ambient keep it, and the sceneClaimsStart gate in
// controller.go enforces that), Select + Start is the quit chord
// (exit_chord.go), and F1 opens the full keyboard reference (shortcuts.go).
// The keyboard overlay keeps its own much longer list; this row only points
// at it, so there is still exactly one full list in the app.
func systemLegendRows() []FooterHint {
	rows := []FooterHint{
		{Button: "Start", Label: "Start menu"},
		{Button: "Select + Start", Label: "Quit JukaHub"},
	}
	if controlsLegend.brick {
		// On a Brick "F1" is a key on the device, not the keyboard's: it opens
		// this card, F2 opens Quick Settings, and Menu goes back like B.
		return append(rows,
			FooterHint{Button: "F1", Label: "This help"},
			FooterHint{Button: "F2", Label: "Quick Settings"},
			FooterHint{Button: "Menu", Label: "Back"},
		)
	}
	return append(rows, FooterHint{Button: "F1", Label: "Keyboard shortcuts"})
}

// controlsLegendSections builds the card's content. It never hardcodes scene
// bindings: the "On this screen" section is footerHints output for the
// captured scene, the same function the audited footers render.
func controlsLegendSections(config *Config) []legendCardSection {
	return []legendCardSection{
		{Title: sceneDisplayName(controlsLegend.scene.Name), Rows: footerHints(config, controlsLegend.scene)},
		{Title: "Every screen", Rows: systemLegendRows()},
		{Title: "Find it in Tools", Rows: toolsLegendRows()},
	}
}

// toolsLegendRows names the two Tools pages a new user cannot guess: what the
// Toolbox is for, and what the Security & trust page answers. They are rows on
// this card rather than a binding because neither has a key — both open from
// the Tools panel — and the legend is where someone reads when the footer did
// not answer the question. The section title carries the "where", so the rows
// stay short enough to survive the card's 420px width.
func toolsLegendRows() []FooterHint {
	return []FooterHint{
		{Button: "Toolbox", Label: "Install and run CLI tools"},
		{Button: "Security & trust", Label: "What this build protects"},
	}
}

// openControlsLegend captures the active scene and puts the legend up. It is
// a no-op before the config exists or a scene is on screen.
func openControlsLegend() {
	if appConfig == nil || currentSceneIndex < 0 || currentSceneIndex >= len(appConfig.Scenes) {
		return
	}
	controlsLegend.scene = appConfig.Scenes[currentSceneIndex]
	controlsLegend.brick = brickHasFunctionKeys()
	controlsLegend.open = true
}

// closeControlsLegend hides the card; it is idempotent so a key repeat on B
// cannot stack back sounds on an already-closed card.
func closeControlsLegend() {
	if !controlsLegend.open {
		return
	}
	controlsLegend.open = false
	PlayBackSound()
}

// handleControlsLegendKey consumes every key while the legend is up. F1 keeps
// the discoverability of the keyboard overlay: it dismisses the legend and
// opens the full shortcuts reference (the F1 toggle later in main.go's
// keyboard chain sits behind this handler, so the legend owns the key).
func handleControlsLegendKey(e *sdl.KeyboardEvent) bool {
	if !controlsLegend.open {
		return false
	}
	if e != nil && e.Type == sdl.KEYDOWN {
		switch e.Keysym.Sym {
		case sdl.K_ESCAPE, sdl.K_b:
			closeControlsLegend()
		case sdl.K_F1:
			controlsLegend.open = false
			shortcutsOpen = true
		}
	}
	return true // consume all input while open
}

// ctrlControlsLegend is the controller twin: B/Back/Start close it, the rest
// of the pad is consumed so nothing underneath reacts while the card is up.
func ctrlControlsLegend(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		closeControlsLegend()
	case sdl.CONTROLLER_BUTTON_LEFTSTICK:
		// F1 on a Brick opened the card, and the same key puts it away.
		if controlsLegend.brick {
			closeControlsLegend()
		}
	}
	return true
}

// legendRowPitch fits every binding row inside a card of the given height.
// Headings keep their natural height, so only the rows compress, and the floor
// stops a very short screen from collapsing the text onto itself — the card is
// clipped rather than unreadable. Pure so the fit can be tested off-screen.
func legendRowPitch(cardH, headerH, footerH, sectionH int32, sections, rows int, natural, fontH int32) int32 {
	if rows <= 0 {
		return natural
	}
	fit := (cardH - headerH - footerH - int32(sections)*sectionH) / int32(rows)
	if fit >= natural {
		return natural
	}
	floor := fontH - 2
	if floor < 14 {
		floor = 14
	}
	if fit < floor {
		return floor
	}
	return fit
}

// renderControlsLegend draws the card in the Quick Settings modal style
// (scrim, UICard, centered title, one row per binding) so the two help
// surfaces read as siblings.
func renderControlsLegend(renderer *sdl.Renderer, config *Config) {
	if !controlsLegend.open {
		return
	}
	font, _ := getCachedFont(config, "small")
	medFont, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	sections := controlsLegendSections(config)

	const (
		cardPadX = int32(20)
		headerH  = int32(64)
		footerH  = int32(36)
	)

	fontH := int32(font.Height())
	rowH := fontH + 12
	sectionH := rowH + 8
	bindingCount := 0
	for _, s := range sections {
		bindingCount += len(s.Rows)
	}

	cardW := qsPanelWide + 20
	if maxW := screenWidth - 24; cardW > maxW {
		cardW = maxW
	}
	cardH := headerH + int32(len(sections))*sectionH + int32(bindingCount)*rowH + footerH
	if maxH := screenHeight - 40; cardH > maxH {
		cardH = maxH
	}
	// A card clamped to a short screen still has to fit every row: shrink the
	// row pitch before a row can spill over the footer hint.
	rowH = legendRowPitch(cardH, headerH, footerH, sectionH, len(sections), bindingCount, rowH, fontH)
	cardX := (screenWidth - cardW) / 2
	cardY := (screenHeight - cardH) / 2

	// Full-screen dim, like the other modals.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	UICard(renderer, sdl.Rect{X: cardX, Y: cardY, W: cardW, H: cardH}, UICardStyle{
		Fill:   sdl.Color{R: 14, G: 18, B: 28, A: 248},
		Border: ColorBorder,
		Radius: 20,
	})

	if medFont != nil {
		title := "Controller"
		tw, _, _ := medFont.SizeUTF8(title)
		renderText(renderer, config, medFont, title,
			sdl.Color{R: 220, G: 230, B: 245, A: 240},
			cardX+(cardW-int32(tw))/2, cardY+14)
	}
	// The subtitle names what the first section describes; the scene snapshot
	// is the truth at open time, not whatever scene is focused while reading.
	sub := sceneDisplayName(controlsLegend.scene.Name)
	if sub == "" {
		sub = controlsLegend.scene.Name
	}
	if sub != "" {
		subY := cardY + 16 + 24 + 4
		if medFont != nil {
			subY = cardY + 14 + int32(medFont.Height()) + 4
		}
		if sw, _, _ := font.SizeUTF8(sub); sw > 0 {
			renderText(renderer, config, font, sub,
				sdl.Color{R: 140, G: 155, B: 180, A: 200},
				cardX+(cardW-int32(sw))/2, subY)
		}
	}

	// The button column is the widest binding string, so "Select + Start"
	// never collides with its label.
	buttonCol := cardPadX
	buttonColMax := buttonCol
	for _, s := range sections {
		for _, h := range s.Rows {
			if w, _, err := font.SizeUTF8(h.Button); err == nil && buttonCol+int32(w) > buttonColMax {
				buttonColMax = buttonCol + int32(w)
			}
		}
	}

	x := cardX + cardPadX
	textW := cardW - 2*cardPadX
	labelX := x + (buttonColMax - buttonCol) + 16
	labelW := textW - (labelX - x)
	y := cardY + headerH
	for _, s := range sections {
		renderText(renderer, config, font, s.Title, getAccentColor(config), x, y)
		y += sectionH
		for _, h := range s.Rows {
			renderText(renderer, config, font, h.Button, accentColor, x, y)
			renderText(renderer, config, font, ellipsize(font, h.Label, labelW),
				sdl.Color{R: 200, G: 210, B: 230, A: 220}, labelX, y)
			y += rowH
		}
	}

	hint := hintLine(FooterHint{"B", "Close"}, FooterHint{"F1", "Keyboard shortcuts"})
	if controlsLegend.brick {
		hint = hintLine(FooterHint{"B / F1", "Close"})
	}
	hw, _, _ := font.SizeUTF8(hint)
	renderText(renderer, config, font, hint,
		sdl.Color{R: 90, G: 100, B: 125, A: 190},
		cardX+(cardW-int32(hw))/2, cardY+cardH-fontH-12)
}
