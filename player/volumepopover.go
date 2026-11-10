package main

// volumepopover.go — the card the header's speaker button opens.
//
// The speaker sits at the left end of the status cluster, immediately left of
// the Wi-Fi meter, on Home and on every scene's status bar (statusbar_status.go
// draws it). A, a click or a tap on it opens this card under it: the level as a
// number and a slider, and a mute button. It is a top-bar overlay like the
// weather card and the calendar: modal while it is up (the pad, the keys and
// the pointer all belong to it), closed by B, Esc or a click outside, and put
// away when the scene changes.
//
//	Pad:      Left/Down quieter, Right/Up louder (held: repeats), A mute, B close
//	Keyboard: arrows, -/+ and PgDn/PgUp, Enter/Space/M mute, Esc close
//	Pointer:  click or drag on the slider, the mute button, wheel anywhere
//
// The level itself, the mixer and the persistence live in volumectl.go.

import (
	"strconv"

	"github.com/veandco/go-sdl2/sdl"
)

// volumePopoverOpen is the card's open flag (render/event thread).
var volumePopoverOpen bool

// volumePopoverUI is the card as last drawn, so the pointer handlers hit
// exactly what is on screen.
var volumePopoverUI struct {
	card     sdl.Rect
	track    sdl.Rect // the slider's drawn bar
	trackHit sdl.Rect // the slider's hit area: the whole control row's height
	mute     sdl.Rect
	dragging bool
}

const (
	volumePanelW    = int32(380)
	volumePanelPad  = int32(18)
	volumeRowH      = int32(44)
	volumeTrackH    = int32(6)
	volumeKnobR     = int32(9)
	volumeRowGap    = int32(14)
	volumeScrimFade = 0.25
)

// volumePopoverLayout is where everything on the card goes.
type volumePopoverLayout struct {
	card, mute, track, trackHit sdl.Rect
	titleY, hintY, noteY        int32
}

// volumePopoverLayoutFor anchors the card under the speaker button (or the
// top-right corner before the button was measured), clamped to the screen,
// and lays its rows out from the measured font heights. Pure.
func volumePopoverLayoutFor(screenW, screenH int32, anchor sdl.Rect, medH, smallH int32, withNote bool) volumePopoverLayout {
	w := volumePanelW
	if w > screenW-32 {
		w = screenW - 32
	}
	h := volumePanelPad + medH + volumeRowGap + volumeRowH + 12 + smallH + 14
	if withNote {
		h += smallH + 4
	}
	x := screenW - 32 - w
	if anchor.W > 0 {
		x = anchor.X + anchor.W/2 - w/2
	}
	if x+w > screenW-16 {
		x = screenW - 16 - w
	}
	if x < 16 {
		x = 16
	}
	y := HomeTopBarH + 8
	if anchor.W > 0 && anchor.Y+anchor.H+8 > y {
		y = anchor.Y + anchor.H + 8
	}
	if y+h > screenH-8 {
		y = max32(8, screenH-8-h)
	}
	card := sdl.Rect{X: x, Y: y, W: w, H: h}
	l := volumePopoverLayout{card: card}
	l.titleY = y + volumePanelPad
	rowY := l.titleY + medH + volumeRowGap
	l.mute = sdl.Rect{X: x + volumePanelPad, Y: rowY, W: volumeRowH, H: volumeRowH}
	trackX := l.mute.X + l.mute.W + volumeRowGap + volumeKnobR
	trackW := x + w - volumePanelPad - volumeKnobR - trackX
	if trackW < 40 {
		trackW = 40
	}
	l.track = sdl.Rect{X: trackX, Y: rowY + (volumeRowH-volumeTrackH)/2, W: trackW, H: volumeTrackH}
	l.trackHit = sdl.Rect{X: trackX - volumeKnobR - 4, Y: rowY, W: trackW + 2*volumeKnobR + 8, H: volumeRowH}
	l.hintY = rowY + volumeRowH + 12
	l.noteY = l.hintY + smallH + 4
	return l
}

// volumePercentAtX maps a pointer x on the slider to a level. Pure.
func volumePercentAtX(track sdl.Rect, x int32) int {
	if track.W <= 0 {
		return 0
	}
	frac := float64(x-track.X) / float64(track.W)
	return volumeClamp(int(uiClamp01(frac)*100 + 0.5))
}

// toggleVolumePopover opens or closes the card. Like the calendar and the
// weather card it is exclusive with the other top-bar overlays, and opening it
// reads the mixer again so the number is the one the hardware keys left.
func toggleVolumePopover() {
	if volumePopoverOpen {
		dismissVolumePopover()
		return
	}
	volumePopoverOpen = true
	volumePopoverUI.dragging = false
	homeCalendarOpen = false
	tb.startOpen = false
	nc.open = false
	dismissWeatherPopover()
	volumeRefreshNow()
}

// dismissVolumePopover closes the card. A no-op when it is closed, so every
// dismissal path (a scene change, an outside click) can call it.
func dismissVolumePopover() {
	volumePopoverOpen = false
	volumePopoverUI.dragging = false
}

// volumePopoverStep moves the level by delta with a tick when it moved.
func volumePopoverStep(config *Config, delta int) {
	before, after := volumeStepBy(config, delta)
	if after != before {
		PlayNavSound()
	}
}

// volumePopoverMute toggles the mute with the activation sound — played after
// the change, so un-muting is heard at the level it returned to.
func volumePopoverMute(config *Config) {
	volumeToggleMute(config)
	PlayActivateSound()
}

// ctrlVolumePopover owns the pad while the card is up: no press may reach the
// scene underneath.
func ctrlVolumePopover(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		volumePopoverStep(config, -volumeStep)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_DPAD_UP:
		volumePopoverStep(config, volumeStep)
	case sdl.CONTROLLER_BUTTON_A:
		volumePopoverMute(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		dismissVolumePopover()
		PlayBackSound()
	}
	return true
}

// volumePopoverKey is the keyboard's half of the same bindings (the twins the
// hint line names: Enter for A, Esc for B). It owns every key while the card
// is up.
func volumePopoverKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !volumePopoverOpen {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_LEFT, sdl.K_DOWN, sdl.K_MINUS, sdl.K_KP_MINUS, sdl.K_PAGEDOWN:
		volumePopoverStep(config, -volumeStep)
	case sdl.K_RIGHT, sdl.K_UP, sdl.K_PLUS, sdl.K_EQUALS, sdl.K_KP_PLUS, sdl.K_PAGEUP:
		volumePopoverStep(config, volumeStep)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE, sdl.K_m, sdl.K_a:
		if e.Repeat == 0 {
			volumePopoverMute(config)
		}
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		dismissVolumePopover()
		PlayBackSound()
	case sdl.K_F10:
		// F10 is Start's keyboard twin (inputhints.go), and Start closes the
		// card on the pad; the card owns every key, so without this case F10
		// was swallowed with no effect.
		if e.Repeat == 0 {
			dismissVolumePopover()
			PlayBackSound()
		}
	}
	return true
}

// volumePopoverMouseDown takes every click while the card is up: the slider
// sets the level where it was pressed (and starts a drag), the mute button
// mutes, the rest of the card is inert, and a click anywhere else — the
// speaker button included — closes the card without pressing what is under it.
func volumePopoverMouseDown(mx, my int32, config *Config) bool {
	if !volumePopoverOpen {
		return false
	}
	ui := &volumePopoverUI
	switch {
	case ui.trackHit.W > 0 && pointInRect(mx, my, ui.trackHit):
		ui.dragging = true
		volumePopoverSetFromX(config, mx)
	case ui.mute.W > 0 && hitTouchTarget(ui.mute, mx, my):
		volumePopoverMute(config)
	case ui.card.W > 0 && pointInRect(mx, my, ui.card):
	default:
		dismissVolumePopover()
	}
	return true
}

// volumePopoverPointerMotion follows a drag on the slider and keeps the
// pointer from hovering (and so focusing) Home's cards behind the card. A
// motion with the button up ends the drag.
func volumePopoverPointerMotion(config *Config, x, y int32, held bool) bool {
	if !volumePopoverOpen {
		return false
	}
	if volumePopoverUI.dragging {
		if held {
			volumePopoverSetFromX(config, x)
		} else {
			volumePopoverUI.dragging = false
		}
	}
	return true
}

// volumePopoverSetFromX applies the level under the pointer, only when it
// lands on another 5% step, so a drag does not queue a write per pixel.
func volumePopoverSetFromX(config *Config, x int32) {
	want := volumeSnap(volumePercentAtX(volumePopoverUI.track, x))
	if cur, known := volumeCurrent(); known && cur == want {
		return
	}
	before := volumeBase()
	if volumeSetLevel(config, want) != before {
		PlayNavSound()
	}
}

// volumeWheel turns the wheel into volume steps: anywhere while the card is
// up, and over the speaker button, which opens the card so the change shows.
func volumeWheel(config *Config, x, y, dy int32) bool {
	if dy == 0 {
		return false
	}
	if !volumePopoverOpen {
		if statusVolumeRect.W <= 0 || !pointInRect(x, y, statusVolumeRect) {
			return false
		}
		if volumeChromeCovered() {
			return false
		}
		toggleVolumePopover()
	}
	if dy > 0 {
		volumePopoverStep(config, volumeStep)
	} else {
		volumePopoverStep(config, -volumeStep)
	}
	return true
}

// volumeChromeCovered reports whether something drawn above the status bar
// owns the input. The speaker's rect is refreshed whenever the bar is drawn,
// and the bar is still drawn under a full-screen video, the image viewer and
// every modal panel; the wheel used to open the card there, where it was
// invisible yet took the pad (or sat under a panel that kept the pad, so B
// could not close it). The click and the D-pad reach the speaker only through
// the bar, which those surfaces already cover.
func volumeChromeCovered() bool {
	return textInputBlockedByOverlay() || globalSearchOpen || clipSearchOpen ||
		alarmFiringActive() || (playerPhase() != "idle" && !videoMiniVisible())
}

// volumePopoverNote is the card's honest footnote: on Windows the slider is
// JukaHub's own level in the Windows mixer (volume_windows.go), not the PC's.
func volumePopoverNote() string {
	// IsWindows first: elsewhere volumeBackend would look for binaries on
	// every frame the card is up.
	if IsWindows() && volumeBackend() == "winmm" {
		return "JukaHub's own level in the Windows volume mixer"
	}
	return ""
}

// renderVolumePopover draws the card. The shared render loop calls it on every
// screen with the other top-bar overlays (renderTopBarOverlays), so it floats
// above the scene and its status bar.
func renderVolumePopover(renderer *sdl.Renderer, config *Config) {
	if !volumePopoverOpen || renderer == nil {
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
	note := volumePopoverNote()
	lay := volumePopoverLayoutFor(screenWidth, screenHeight, statusVolumeRect,
		int32(medium.Height()), int32(font.Height()), note != "")
	ui := &volumePopoverUI
	ui.card, ui.track, ui.trackHit, ui.mute = lay.card, lay.track, lay.trackHit, lay.mute

	drawOverlayScrimFade(renderer, volumeScrimFade)
	drawOverlayCard(renderer, lay.card)
	drawOverlayCardAccent(renderer, lay.card.X, lay.card.Y, lay.card.W)

	pct, known := volumeCurrent()
	if !known {
		pct = volumeBase()
	}
	muted := known && pct == 0
	accent := glassAccent()
	left := lay.card.X + volumePanelPad

	renderText(renderer, config, medium, "Volume", ColorTextPrimary(), left, lay.titleY)
	value, valueInk := strconv.Itoa(pct)+"%", ColorTextSecondary()
	switch {
	case !known:
		value = "…"
	case muted:
		value, valueInk = "Muted", WarningInk()
	}
	vw, _ := measureText(medium, value)
	renderText(renderer, config, medium, value, valueInk, lay.card.X+lay.card.W-volumePanelPad-vw, lay.titleY)

	// The mute button: the speaker for the level, tinted while muted.
	m := lay.mute
	drawGlassSurface(renderer, m, glassButtonRadius(m.H), glassState{Hovered: pointInRect(mouseX, mouseY, m), NoLift: true})
	ink := ColorTextPrimary()
	if muted {
		fillRoundedRect(renderer, m.X, m.Y, m.W, m.H, glassButtonRadius(m.H), WithAlpha(accent, 70))
		ink = accent
	}
	inset := m.W / 4
	iconDraw(renderer, statusVolumeGlyph(pct, known), sdl.Rect{X: m.X + inset, Y: m.Y + inset, W: m.W - 2*inset, H: m.H - 2*inset}, ink)

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

	muteLabel := "Mute"
	if muted {
		muteLabel = "Unmute"
	}
	hint := hintLine(FooterHint{"Left / Right", "Adjust"}, FooterHint{"A", muteLabel}, FooterHint{"B", "Close"})
	textW := lay.card.W - 2*volumePanelPad
	renderText(renderer, config, font, ellipsize(font, hint, textW), ColorTextTertiary(), left, lay.hintY)
	if note != "" {
		renderText(renderer, config, font, ellipsize(font, note, textW), ColorTextTertiary(), left, lay.noteY)
	}
}
