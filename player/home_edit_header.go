package main

// home_edit_header.go — Edit Home's "Top bar" row: arranging the right of the
// top bar (header_arrange.go).
//
// Up from Edit Home's strip, or its "Top bar" chip, swaps the strip's chips
// for one chip per control of the top bar — brightness, volume, Bluetooth,
// Wi-Fi, weather, clock, battery, now playing, the update offer, Trophies,
// Report a bug and Notifications — in their order, followed by Reset and
// Done. The control the focus is on wears a ring in the live bar above, and
// every change shows there at once, on Home and on every other screen:
//
//   - Left/Right walk the chips; A picks the focused control up and
//     Left/Right carry it one place at a time; A or B puts it down;
//   - Y (Delete, Backspace) hides the control or shows it again (a hidden
//     one is drawn faded and struck through; the bell cannot be hidden);
//   - X (R) resets the bar to the shipped order with everything shown, on a
//     second press within three seconds, as the Reset chip does;
//   - B (Esc) or Down goes back to the strip; L2 (Q) ends editing.
//
// With the pointer, a click focuses a chip, dragging it along the row
// carries it, and the badge on the focused chip's corner hides or shows it.
//
// The order being edited is a copy (homeEditBar.work), which both headers
// draw while the row is open (headerArrangementCurrent); it is saved when a
// control is put down, hidden or shown, on a reset, and when the row closes,
// so carrying a control across the bar is not a save per step.

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// The row's chips after the slots.
	homeEditBarChipReset = int(hsCount)
	homeEditBarChipDone  = int(hsCount) + 1
	homeEditBarChipCount = int(hsCount) + 2
)

// homeEditBar is the Top bar row's state. Render/event thread only.
var homeEditBar struct {
	open bool
	// idx is the focused chip: a place on the order (0..hsCount-1), Reset or
	// Done.
	idx     int
	holding bool
	work    headerArrangement
	dirty   bool
	resetAt uint64
	// chips are the row's chips as last drawn, and badge the hide/show badge
	// on the focused one, for the pointer.
	chips [homeEditBarChipCount]sdl.Rect
	badge sdl.Rect
	// press is a pointer press on a slot chip that may turn into a drag.
	press struct {
		active, drag bool
		x            int32
	}
}

// homeEditBarActive reports whether the Top bar row has the focus.
func homeEditBarActive() bool { return homeEdit.open && homeEditBar.open }

// homeEditBarOpen gives the focus to the Top bar row, on its first control.
func homeEditBarOpen(config *Config) {
	if homeEditBar.open {
		return
	}
	if config == nil {
		config = appConfig
	}
	work := headerArrangementCurrent(config)
	homeEditBar.work = work
	homeEditBar.open = true
	homeEditBar.idx = 0
	homeEditBar.holding = false
	homeEditBar.dirty = false
	homeEditBar.resetAt = 0
	homeEditBar.press.active, homeEditBar.press.drag = false, false
	homeEdit.strip = false
	PlayNavSound()
}

// homeEditBarSave stores the order being edited when it changed.
func homeEditBarSave(config *Config) {
	if !homeEditBar.dirty {
		return
	}
	if config == nil {
		config = appConfig
	}
	homeEditBar.dirty = false
	headerArrangementSet(config, homeEditBar.work)
}

// homeEditBarClose saves and hands the focus back to the strip (toStrip) or
// to nothing (edit mode is ending).
func homeEditBarClose(config *Config, toStrip bool) {
	if !homeEditBar.open {
		return
	}
	homeEditBar.holding = false
	homeEditBar.press.active, homeEditBar.press.drag = false, false
	homeEditBarSave(config)
	homeEditBar.open = false
	homeEditBar.resetAt = 0
	homeEditBar.chips = [homeEditBarChipCount]sdl.Rect{}
	homeEditBar.badge = sdl.Rect{}
	if toStrip {
		homeEdit.strip = true
		homeEdit.stripIdx = homeEditStripBar
	}
}

// homeEditBarSlot is the slot on the focused chip; ok is false on Reset and
// Done.
func homeEditBarSlot() (headerSlot, bool) {
	if homeEditBar.idx < 0 || homeEditBar.idx >= int(hsCount) {
		return 0, false
	}
	return homeEditBar.work.order[homeEditBar.idx], true
}

// homeEditBarResetArmed reports whether Reset waits for its second press.
func homeEditBarResetArmed() bool {
	return homeEditBar.resetAt != 0 && sdl.GetTicks64()-homeEditBar.resetAt < homeEditResetArmMS
}

// homeEditBarReset is Reset: the first press arms it, the second puts the
// shipped order back with everything shown.
func homeEditBarReset(config *Config) {
	if !homeEditBarResetArmed() {
		homeEditBar.resetAt = sdl.GetTicks64()
		PlayNavSound()
		ShowToast("Press Reset again to put the top bar back as it ships", ToastKindInfo)
		return
	}
	homeEditBar.resetAt = 0
	homeEditBar.holding = false
	homeEditBar.work = headerArrangeDefault()
	homeEditBar.dirty = true
	homeEditBarSave(config)
	PlayActivateSound()
	ShowToast("Top bar reset", ToastKindSuccess)
}

// homeEditBarToggleHidden hides the focused control or shows it again.
func homeEditBarToggleHidden(config *Config) {
	s, ok := homeEditBarSlot()
	if !ok {
		return
	}
	if !headerSlotHideable(s) {
		PlayErrorSound()
		ShowToast(headerSlotInfo[s].label+" stays in the top bar — move it instead", ToastKindInfo)
		return
	}
	homeEditBar.holding = false
	homeEditBar.work.hidden[s] = !homeEditBar.work.hidden[s]
	homeEditBar.dirty = true
	homeEditBarSave(config)
	PlayActivateSound()
}

// homeEditBarMoveHeld carries the held control one place left (step -1) or
// right (+1).
func homeEditBarMoveHeld(step int) {
	i := homeEditBar.idx
	j := i + step
	if i < 0 || i >= int(hsCount) || j < 0 || j >= int(hsCount) {
		return
	}
	o := &homeEditBar.work.order
	o[i], o[j] = o[j], o[i]
	homeEditBar.idx = j
	homeEditBar.dirty = true
	PlayNavSound()
}

// homeEditBarMoveTo carries the control at place from to place to, the
// others closing up behind it (the pointer's drag).
func homeEditBarMoveTo(from, to int) {
	if from == to || from < 0 || to < 0 || from >= int(hsCount) || to >= int(hsCount) {
		return
	}
	o := &homeEditBar.work.order
	s := o[from]
	if from < to {
		copy(o[from:to], o[from+1:to+1])
	} else {
		copy(o[to+1:from+1], o[to:from])
	}
	o[to] = s
	homeEditBar.idx = to
	homeEditBar.dirty = true
}

// homeEditBarNavigate is a D-pad press on the row.
func homeEditBarNavigate(config *Config, dir FocusDirection) {
	switch dir {
	case FocusLeft, FocusRight:
		step := 1
		if dir == FocusLeft {
			step = -1
		}
		if homeEditBar.holding {
			homeEditBarMoveHeld(step)
			return
		}
		if j := homeEditBar.idx + step; j >= 0 && j < homeEditBarChipCount {
			homeEditBar.idx = j
			PlayNavSound()
		}
	case FocusDown:
		PlayNavSound()
		homeEditBarClose(config, true)
	}
}

// homeEditBarPrimary is A: pick the control up or put it down, or press
// Reset or Done.
func homeEditBarPrimary(config *Config) {
	switch homeEditBar.idx {
	case homeEditBarChipReset:
		homeEditBarReset(config)
		return
	case homeEditBarChipDone:
		PlayBackSound()
		homeEditClose(config)
		return
	}
	if homeEditBar.holding {
		homeEditBar.holding = false
		homeEditBarSave(config)
		PlayActivateSound()
		return
	}
	homeEditBar.holding = true
	PlayActivateSound()
}

// homeEditBarBack is B: put the control down, else back to the strip.
func homeEditBarBack(config *Config) {
	if homeEditBar.holding {
		homeEditBar.holding = false
		homeEditBarSave(config)
		PlayActivateSound()
		return
	}
	PlayBackSound()
	homeEditBarClose(config, true)
}

// homeEditBarPad is the controller on the row. Start falls through (the menu
// ends editing, which saves); L2 is homeEditTrigger's (shoulders.go).
func homeEditBarPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		homeEditBarNavigate(config, FocusDown)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		homeEditBarNavigate(config, FocusLeft)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		homeEditBarNavigate(config, FocusRight)
	case sdl.CONTROLLER_BUTTON_A:
		homeEditBarPrimary(config)
	case sdl.CONTROLLER_BUTTON_Y:
		homeEditBarToggleHidden(config)
	case sdl.CONTROLLER_BUTTON_X:
		homeEditBarReset(config)
	case sdl.CONTROLLER_BUTTON_B:
		homeEditBarBack(config)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER, sdl.CONTROLLER_BUTTON_BACK:
		// The layout is switched from the strip; Select starts chords.
	default:
		return false
	}
	return true
}

// homeEditBarKey is the keyboard on the row: the arrows, Enter (A), Delete,
// Backspace or Y (hide/show), X or R (reset), Esc or B (back) and Q (Done).
func homeEditBarKey(e *sdl.KeyboardEvent, config *Config) bool {
	switch e.Keysym.Sym {
	case sdl.K_UP:
	case sdl.K_DOWN:
		homeEditBarNavigate(config, FocusDown)
	case sdl.K_LEFT:
		homeEditBarNavigate(config, FocusLeft)
	case sdl.K_RIGHT:
		homeEditBarNavigate(config, FocusRight)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		if e.Repeat == 0 {
			homeEditBarPrimary(config)
		}
	case sdl.K_DELETE, sdl.K_BACKSPACE, sdl.K_y:
		if e.Repeat == 0 {
			homeEditBarToggleHidden(config)
		}
	case sdl.K_x, sdl.K_r:
		if e.Repeat == 0 {
			homeEditBarReset(config)
		}
	case sdl.K_ESCAPE, sdl.K_b:
		if e.Repeat == 0 {
			homeEditBarBack(config)
		}
	case sdl.K_q:
		if e.Repeat == 0 {
			homeEditTrigger(config)
		}
	case sdl.K_TAB, sdl.K_e:
	default:
		return false
	}
	return true
}

// ── Pointer ───────────────────────────────────────────────────────────────

// homeEditBarClick is a tap while editing. It reports whether the tap was
// the row's: a chip, the badge, or (with the row open) anywhere else, which
// hands the focus back to the strip and lets the tap go on to what is there.
func homeEditBarClick(config *Config, mx, my int32) bool {
	if !homeEditBar.open {
		return false
	}
	// The badge's own disc, not a grown target: grown, it would reach into
	// the next chip.
	if b := homeEditBar.badge; b.W > 0 && pointInRect(mx, my, b) {
		homeEditBarToggleHidden(config)
		return true
	}
	for i, r := range homeEditBar.chips {
		if r.W <= 0 || !pointInRect(mx, my, growTouchTarget(r)) {
			continue
		}
		if homeEditBar.holding && i != homeEditBar.idx {
			homeEditBar.holding = false
			homeEditBarSave(config)
		}
		homeEditBar.idx = i
		switch i {
		case homeEditBarChipReset, homeEditBarChipDone:
			homeEditBarPrimary(config)
		default:
			PlayNavSound()
			homeEditBar.press.active, homeEditBar.press.drag = true, false
			homeEditBar.press.x = mx
		}
		return true
	}
	homeEditBarClose(config, false)
	return false
}

// homeEditBarPointerMotion follows a press on a slot chip: past the slop it
// carries the control to the place of the chip under the pointer. Hovering a
// chip focuses it. It reports whether the row took the motion.
func homeEditBarPointerMotion(config *Config, mx, my int32, held bool) bool {
	if !homeEditBar.open {
		return false
	}
	if homeEditBar.press.active {
		if !held {
			homeEditBarPointerUp(config)
			return true
		}
		if !homeEditBar.press.drag {
			slop := int32(12)
			if homeLayout != nil {
				slop = max32(4, scaleX(homeEditDragSlop, homeLayout.Width))
			}
			if homeAbsInt(int(mx-homeEditBar.press.x)) > int(slop) {
				homeEditBar.press.drag = true
				homeEditBar.holding = true
			}
		}
		if homeEditBar.press.drag {
			best, bestD := -1, int32(0)
			for i := 0; i < int(hsCount); i++ {
				r := homeEditBar.chips[i]
				if r.W <= 0 {
					continue
				}
				d := mx - (r.X + r.W/2)
				if d < 0 {
					d = -d
				}
				if best < 0 || d < bestD {
					best, bestD = i, d
				}
			}
			if best >= 0 && best != homeEditBar.idx {
				homeEditBarMoveTo(homeEditBar.idx, best)
				PlayNavSound()
			}
		}
		return true
	}
	if homeEditBar.holding {
		return true
	}
	for i, r := range homeEditBar.chips {
		if r.W > 0 && pointInRect(mx, my, r) {
			homeEditBar.idx = i
			return true
		}
	}
	return false
}

// homeEditBarPointerUp ends a press on a slot chip: a drag puts the control
// down and saves.
func homeEditBarPointerUp(config *Config) bool {
	if !homeEditBar.open || !homeEditBar.press.active {
		return false
	}
	dragged := homeEditBar.press.drag
	homeEditBar.press.active, homeEditBar.press.drag = false, false
	if dragged {
		homeEditBar.holding = false
		homeEditBarSave(config)
		PlayActivateSound()
	}
	return true
}

// ── Drawing ───────────────────────────────────────────────────────────────

// homeEditBarCaption is the line beside "Edit Home" while the row is open:
// the focused control and its state.
func homeEditBarCaption() string {
	switch homeEditBar.idx {
	case homeEditBarChipReset:
		if homeEditBarResetArmed() {
			return "Top bar — press Reset again to put it back as it ships"
		}
		return "Top bar — put it back as it ships"
	case homeEditBarChipDone:
		return "Top bar — finish editing"
	}
	s, ok := homeEditBarSlot()
	if !ok {
		return "Top bar"
	}
	line := "Top bar — " + headerSlotInfo[s].label
	switch {
	case homeEditBar.holding:
		line += " (moving)"
	case homeEditBar.work.hidden[s]:
		line += " (hidden)"
	case !headerSlotHideable(s):
		line += " (always shown)"
	}
	return line
}

// drawEditBarRow draws the row where the strip's chips are: one square chip
// per control in the order being edited, then Reset and Done at the right.
func (h *HomeLayout) drawEditBarRow(renderer *sdl.Renderer, font *ttf.Font, x0, y, hh, right int32) {
	homeEditBar.chips = [homeEditBarChipCount]sdl.Rect{}
	homeEditBar.badge = sdl.Rect{}
	if renderer == nil || font == nil {
		return
	}
	pad := scaleX(18, h.Width)
	gap := scaleX(8, h.Width)
	glyphDone := min32(scaleY(18, h.Height), hh-scaleY(14, h.Height))
	dw, _ := measureText(font, "Done")
	done := sdl.Rect{W: dw + 2*pad + glyphDone + scaleX(8, h.Width), H: hh, Y: y}
	done.X = right - done.W
	resetLabel := "Reset"
	if homeEditBarResetArmed() {
		resetLabel = "Press again"
	}
	rw, _ := measureText(font, resetLabel)
	reset := sdl.Rect{W: rw + 2*pad, H: hh, Y: y}
	reset.X = done.X - scaleX(10, h.Width) - reset.W
	// The slot chips share what is left of the row, square at most.
	room := reset.X - scaleX(16, h.Width) - x0
	w := hh + scaleX(10, h.Width)
	if fit := (room - gap*int32(hsCount-1)) / int32(hsCount); fit < w {
		w = fit
	}
	if w < scaleX(24, h.Width) {
		w = scaleX(24, h.Width)
	}
	glyph := min32(scaleY(20, h.Height), hh-scaleY(14, h.Height))
	lift := scaleY(4, h.Height)
	x := x0
	for i := 0; i < int(hsCount); i++ {
		s := homeEditBar.work.order[i]
		r := sdl.Rect{X: x, Y: y, W: w, H: hh}
		focused := homeEditBar.idx == i
		held := focused && homeEditBar.holding
		draw := r
		if held {
			draw.Y -= lift
		}
		hidden := homeEditBar.work.hidden[s]
		h.drawEditChip(renderer, font, draw, "", "", held, focused)
		ink := ColorTextPrimary()
		if hidden {
			ink = WithAlpha(ColorTextMuted(), 120)
		}
		box := sdl.Rect{X: draw.X + (draw.W-glyph)/2, Y: draw.Y + (draw.H-glyph)/2, W: glyph, H: glyph}
		drawHomeGlyphTinted(renderer, headerSlotInfo[s].icon, box, ink)
		if hidden {
			// Struck through: off the bar, still here to be shown again.
			_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
			_ = renderer.SetDrawColor(ColorAccent.R, ColorAccent.G, ColorAccent.B, 200)
			for t := int32(-1); t <= 1; t++ {
				_ = renderer.DrawLine(box.X-2, box.Y+box.H+2+t, box.X+box.W+2, box.Y-2+t)
			}
		}
		homeEditBar.chips[i] = r
		if focused && !held && headerSlotHideable(s) {
			// The pointer's hide/show badge on the chip's top-right corner.
			bs := max32(scaleY(18, h.Height), hh/2)
			b := sdl.Rect{X: draw.X + draw.W - bs*2/3, Y: draw.Y - bs/3, W: bs, H: bs}
			homeEditBar.badge = b
			fillRoundedRect(renderer, b.X, b.Y, b.W, b.H, b.H/2, ColorAccent)
			icon := "minus"
			if hidden {
				icon = "plus"
			}
			in := b.W / 5
			drawHomeGlyphTinted(renderer, icon, sdl.Rect{X: b.X + in, Y: b.Y + in, W: b.W - 2*in, H: b.H - 2*in}, ColorIconDark)
		}
		x += w + gap
	}
	// The slot chips were sized to leave room for Reset and Done, so both are
	// always drawn: the D-pad can never land on a chip that is not there.
	h.drawEditChip(renderer, font, reset, resetLabel, "", false, homeEditBar.idx == homeEditBarChipReset)
	homeEditBar.chips[homeEditBarChipReset] = reset
	h.drawEditChip(renderer, font, done, "Done", "check", true, homeEditBar.idx == homeEditBarChipDone)
	homeEditBar.chips[homeEditBarChipDone] = done
}

// drawEditBarLiveRing rings the focused control where the bar above draws it
// (headerSlotRects), so the chip and the control read as one thing. A hidden
// control, or one the bar has no room for, has nothing to ring.
func drawEditBarLiveRing(renderer *sdl.Renderer) {
	if renderer == nil || !homeEditBarActive() {
		return
	}
	s, ok := homeEditBarSlot()
	if !ok {
		return
	}
	r := headerSlotRects[s]
	if r.W <= 0 || r.H <= 0 {
		return
	}
	out := homeFocusRingOutset()
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if homeEditBar.holding {
		fillRoundedRect(renderer, r.X-out, r.Y-out, r.W+2*out, r.H+2*out, 10+out, WithAlpha(ColorAccent, 48))
	}
	strokeRoundedRect(renderer, r.X-out, r.Y-out, r.W+2*out, r.H+2*out, 10+out, homeFocusRingWidth, ColorAccent)
}
