package main

// brightnesspopover.go — the card the header's sun button opens.
//
// The sun sits at the left end of the status cluster, immediately left of the
// volume speaker, on Home and on every scene's status bar (statusbar_status.go
// draws it). A, a click or a tap on it opens this card under it — the same
// card as the speaker's (volumepopover.go), so the two read as one family:
// the level as a number and a slider, and a Dim button where the volume card
// has Mute. It is a top-bar overlay like the others: modal while it is up
// (the pad, the keys and the pointer all belong to it), closed by B, Esc or a
// click outside, and put away when the scene changes.
//
//	Pad:      Left/Down dimmer, Right/Up brighter (held: repeats), A dim, B close
//	Keyboard: arrows, -/+ and PgDn/PgUp, Enter/Space/D dim, Esc close
//	Pointer:  click or drag on the slider, the Dim button, wheel anywhere
//
// The level itself, the hardware and the persistence live in brightnessctl.go.

import (
	"strconv"

	"github.com/veandco/go-sdl2/sdl"
)

// brightnessPopoverOpen is the card's open flag (render/event thread).
var brightnessPopoverOpen bool

// brightnessPopoverUI is the card as last drawn, so the pointer handlers hit
// exactly what is on screen.
var brightnessPopoverUI struct {
	card     sdl.Rect
	track    sdl.Rect // the slider's drawn bar
	trackHit sdl.Rect // the slider's hit area: the whole control row's height
	dim      sdl.Rect
	dragging bool
}

// brightnessPercentAtX maps a pointer x on the slider to a level. Pure.
func brightnessPercentAtX(track sdl.Rect, x int32) int {
	if track.W <= 0 {
		return 0
	}
	frac := float64(x-track.X) / float64(track.W)
	return brightnessClamp(int(uiClamp01(frac)*100 + 0.5))
}

// toggleBrightnessPopover opens or closes the card. It is exclusive with the
// other top-bar overlays, and opening it reads the level again so the number
// is the one the firmware's keys (or Windows) left. It does not open under
// Quick Settings, which is drawn over it and would leave it holding the pad
// unseen (a tap reaches the header through the panel).
func toggleBrightnessPopover() {
	if brightnessPopoverOpen {
		dismissBrightnessPopover()
		return
	}
	if qs.open {
		return
	}
	brightnessPopoverOpen = true
	brightnessPopoverUI.dragging = false
	homeCalendarOpen = false
	tb.startOpen = false
	nc.open = false
	dismissWeatherPopover()
	dismissVolumePopover()
	brightnessRefreshNow()
}

// dismissBrightnessPopover closes the card. A no-op when it is closed, so
// every dismissal path (a scene change, an outside click) can call it.
func dismissBrightnessPopover() {
	brightnessPopoverOpen = false
	brightnessPopoverUI.dragging = false
}

// brightnessPopoverStep moves the level by one step of the backend's grid in
// dir's direction, with a tick when it moved.
func brightnessPopoverStep(config *Config, dir int) {
	before, after := brightnessStepBy(config, dir*brightnessGrid())
	if after != before {
		PlayNavSound()
	}
}

// brightnessPopoverDim toggles Dim with the activation sound.
func brightnessPopoverDim(config *Config) {
	brightnessToggleDim(config)
	PlayActivateSound()
}

// ctrlBrightnessPopover owns the pad while the card is up: no press may reach
// the scene underneath.
func ctrlBrightnessPopover(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		brightnessPopoverStep(config, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_DPAD_UP:
		brightnessPopoverStep(config, 1)
	case sdl.CONTROLLER_BUTTON_A:
		brightnessPopoverDim(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		dismissBrightnessPopover()
		PlayBackSound()
	}
	return true
}

// brightnessPopoverKey is the keyboard's half of the same bindings (the twins
// the hint line names: Enter for A, Esc for B). It owns every key while the
// card is up.
func brightnessPopoverKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !brightnessPopoverOpen {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_LEFT, sdl.K_DOWN, sdl.K_MINUS, sdl.K_KP_MINUS, sdl.K_PAGEDOWN:
		brightnessPopoverStep(config, -1)
	case sdl.K_RIGHT, sdl.K_UP, sdl.K_PLUS, sdl.K_EQUALS, sdl.K_KP_PLUS, sdl.K_PAGEUP:
		brightnessPopoverStep(config, 1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE, sdl.K_d, sdl.K_a:
		if e.Repeat == 0 {
			brightnessPopoverDim(config)
		}
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		dismissBrightnessPopover()
		PlayBackSound()
	case sdl.K_F10:
		// F10 is Start's keyboard twin (inputhints.go), and Start closes the
		// card on the pad.
		if e.Repeat == 0 {
			dismissBrightnessPopover()
			PlayBackSound()
		}
	}
	return true
}

// brightnessPopoverMouseDown takes every click while the card is up: the
// slider sets the level where it was pressed (and starts a drag), the Dim
// button dims, the rest of the card is inert, and a click anywhere else — the
// sun included — closes the card without pressing what is under it.
func brightnessPopoverMouseDown(mx, my int32, config *Config) bool {
	if !brightnessPopoverOpen {
		return false
	}
	ui := &brightnessPopoverUI
	switch {
	case ui.trackHit.W > 0 && pointInRect(mx, my, ui.trackHit):
		ui.dragging = true
		brightnessPopoverSetFromX(config, mx)
	case ui.dim.W > 0 && hitTouchTarget(ui.dim, mx, my):
		brightnessPopoverDim(config)
	case ui.card.W > 0 && pointInRect(mx, my, ui.card):
	default:
		dismissBrightnessPopover()
	}
	return true
}

// brightnessPopoverPointerMotion follows a drag on the slider and keeps the
// pointer from hovering (and so focusing) Home's cards behind the card. A
// motion with the button up ends the drag.
func brightnessPopoverPointerMotion(config *Config, x, y int32, held bool) bool {
	if !brightnessPopoverOpen {
		return false
	}
	if brightnessPopoverUI.dragging {
		if held {
			brightnessPopoverSetFromX(config, x)
		} else {
			brightnessPopoverUI.dragging = false
		}
	}
	return true
}

// brightnessPopoverSetFromX applies the level under the pointer, only when it
// lands on another step of the grid, so a drag does not queue a write (a
// PowerShell run, a DDC/CI exchange) per pixel.
func brightnessPopoverSetFromX(config *Config, x int32) {
	want := brightnessNormalize(brightnessKnownBackend(), brightnessPercentAtX(brightnessPopoverUI.track, x))
	if cur, known := brightnessCurrent(); known && cur == want {
		return
	}
	before := brightnessBase()
	if brightnessSetLevel(config, want) != before {
		PlayNavSound()
	}
}

// brightnessWheel turns the wheel into brightness steps: anywhere while the
// card is up, and over the sun, which opens the card so the change shows.
func brightnessWheel(config *Config, x, y, dy int32) bool {
	if dy == 0 {
		return false
	}
	if !brightnessPopoverOpen {
		if statusBrightnessRect.W <= 0 || !pointInRect(x, y, statusBrightnessRect) {
			return false
		}
		// The bar is still drawn under full-screen surfaces that own the
		// input (volumeChromeCovered); the wheel must not open an invisible
		// card there.
		if volumeChromeCovered() {
			return false
		}
		toggleBrightnessPopover()
	}
	if dy > 0 {
		brightnessPopoverStep(config, 1)
	} else {
		brightnessPopoverStep(config, -1)
	}
	return true
}

// brightnessPopoverNote is the card's honest footnote where the slider is not
// the screen's own backlight, or not obviously so.
func brightnessPopoverNote() string {
	switch brightnessKnownBackend() {
	case brightnessBackendSoft:
		return "No brightness control found: this dims JukaHub's picture only"
	case brightnessBackendDDC:
		return "The monitor's own brightness, over DDC/CI"
	}
	return ""
}

// renderBrightnessPopover draws the card. The shared render loop calls it on
// every screen with the other top-bar overlays (renderTopBarOverlays), so it
// floats above the scene and its status bar.
func renderBrightnessPopover(renderer *sdl.Renderer, config *Config) {
	if !brightnessPopoverOpen || renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	note := brightnessPopoverNote()
	// The volume card's geometry, hung from the sun instead of the speaker:
	// its mute button's square is the Dim button here.
	lay := volumePopoverLayoutFor(screenWidth, screenHeight, statusBrightnessRect,
		int32(medium.Height()), int32(font.Height()), note != "")
	ui := &brightnessPopoverUI
	ui.card, ui.track, ui.trackHit, ui.dim = lay.card, lay.track, lay.trackHit, lay.mute

	drawOverlayScrimFade(renderer, volumeScrimFade)
	drawOverlayCard(renderer, lay.card)
	drawOverlayCardAccent(renderer, lay.card.X, lay.card.Y, lay.card.W)

	pct, known := brightnessCurrent()
	if !known {
		pct = brightnessBase()
	}
	dimmed := brightnessDimmed(pct, known)
	accent := glassAccent()
	left := lay.card.X + volumePanelPad

	renderText(renderer, config, medium, "Brightness", ColorTextPrimary(), left, lay.titleY)
	value := strconv.Itoa(pct) + "%"
	if !known {
		value = "…"
	}
	vw, _ := measureText(medium, value)
	renderText(renderer, config, medium, value, ColorTextSecondary(), lay.card.X+lay.card.W-volumePanelPad-vw, lay.titleY)

	// The Dim button: the sun for the level, tinted while dimmed.
	d := lay.mute
	drawGlassSurface(renderer, d, glassButtonRadius(d.H), glassState{Hovered: pointInRect(mouseX, mouseY, d), NoLift: true})
	ink := ColorTextPrimary()
	if dimmed {
		fillRoundedRect(renderer, d.X, d.Y, d.W, d.H, glassButtonRadius(d.H), WithAlpha(accent, 70))
		ink = accent
	}
	inset := d.W / 4
	iconDraw(renderer, brightnessGlyph(pct, known), sdl.Rect{X: d.X + inset, Y: d.Y + inset, W: d.W - 2*inset, H: d.H - 2*inset}, ink)

	// The slider: track, the level in the accent, and the knob wearing the
	// ring — the pad's Left/Right drive it, so it is what holds the focus.
	t := lay.track
	fillRoundedRect(renderer, t.X, t.Y, t.W, t.H, t.H/2, glassA(46))
	fw := t.W * int32(pct) / 100
	if fw > 0 {
		fillRoundedRect(renderer, t.X, t.Y, max32(fw, t.H), t.H, t.H/2, accent)
	}
	cx, cy := t.X+fw, t.Y+t.H/2
	r := volumeKnobR
	fillRoundedRect(renderer, cx-r, cy-r, 2*r, 2*r, r, sdl.Color{R: 245, G: 248, B: 252, A: 255})
	strokeRoundedRect(renderer, cx-r-3, cy-r-3, 2*r+6, 2*r+6, r+3, 2, accent)

	dimLabel := "Dim"
	if dimmed {
		dimLabel = "Brighten"
	}
	hint := hintLine(FooterHint{"Left/Right", "Adjust"}, FooterHint{"A", dimLabel}, FooterHint{"B", "Close"})
	textW := lay.card.W - 2*volumePanelPad
	renderText(renderer, config, font, ellipsize(font, hint, textW), ColorTextTertiary(), left, lay.hintY)
	if note != "" {
		renderText(renderer, config, font, ellipsize(font, note, textW), ColorTextTertiary(), left, lay.noteY)
	}
}
