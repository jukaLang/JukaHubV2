package main

// setsheet.go — a list sheet for a choice with more values than a carousel
// should step through: the Settings hub opens it for "every subtitle
// language", and any screen can open one for its own list.
//
// It is a card over a scrim, Home's glass, one row per value with a second
// line for detail, the stored value marked with a check, and the usual keys:
// Up/Down (or the wheel) to move, A/Enter or a tap to choose, B/Esc or a tap
// outside the card to close without changing anything.

import (
	"github.com/veandco/go-sdl2/sdl"
)

// setSheetState is one open sheet. The owner draws it with setSheetDraw and
// routes its input through setSheetKey, setSheetPad, setSheetMouse and
// setSheetWheel while open is true.
type setSheetState struct {
	open    bool
	title   string
	note    string
	options []setOption
	current string
	cursor  int
	scroll  int32
	choose  func(value string)
	// The last frame's geometry, for the mouse.
	card  sdl.Rect
	view  sdl.Rect
	rects []sdl.Rect
}

// setSheetOpen shows a sheet with the cursor on the stored value.
func setSheetOpen(s *setSheetState, title, note string, options []setOption, current string, choose func(value string)) {
	*s = setSheetState{
		open:    true,
		title:   title,
		note:    note,
		options: options,
		current: current,
		choose:  choose,
		rects:   s.rects[:0],
	}
	for i, o := range options {
		if o.Value == current {
			s.cursor = i
			break
		}
	}
	PlayActivateSound()
}

// setSheetClose dismisses the sheet without choosing.
func setSheetClose(s *setSheetState) {
	if !s.open {
		return
	}
	s.open = false
	s.choose = nil
	PlayBackSound()
}

// setSheetPick chooses the value at i and closes the sheet.
func setSheetPick(s *setSheetState, i int) {
	if !s.open || i < 0 || i >= len(s.options) {
		return
	}
	choose := s.choose
	value := s.options[i].Value
	s.open = false
	s.choose = nil
	PlayActivateSound()
	if choose != nil {
		choose(value)
	}
}

// setSheetMove moves the cursor by delta, clamped to the list.
func setSheetMove(s *setSheetState, delta int) {
	n := len(s.options)
	if n == 0 || delta == 0 {
		return
	}
	next := s.cursor + delta
	if next < 0 {
		next = 0
	}
	if next > n-1 {
		next = n - 1
	}
	if next != s.cursor {
		s.cursor = next
		PlayNavSound()
	}
}

// setSheetPage is how many rows one page jump moves.
func setSheetPage(s *setSheetState) int {
	if len(s.rects) > 1 && s.view.H > 0 && s.rects[0].H > 0 {
		if n := int(s.view.H / (s.rects[0].H + 6)); n > 1 {
			return n - 1
		}
	}
	return 5
}

// setSheetKey handles a key while the sheet is open.
func setSheetKey(s *setSheetState, sym sdl.Keycode) {
	switch sym {
	case sdl.K_UP:
		setSheetMove(s, -1)
	case sdl.K_DOWN:
		setSheetMove(s, 1)
	case sdl.K_PAGEUP, sdl.K_LEFT:
		// Left/Right page like the pad's D-pad does (setSheetPad), so the
		// footer's "D-Pad" means the same on the arrow keys.
		setSheetMove(s, -setSheetPage(s))
	case sdl.K_PAGEDOWN, sdl.K_RIGHT:
		setSheetMove(s, setSheetPage(s))
	case sdl.K_HOME:
		setSheetMove(s, -len(s.options))
	case sdl.K_END:
		setSheetMove(s, len(s.options))
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		setSheetPick(s, s.cursor)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		setSheetClose(s)
	}
}

// setSheetPad handles a controller button while the sheet is open.
func setSheetPad(s *setSheetState, button uint8) {
	switch button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		setSheetMove(s, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		setSheetMove(s, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		setSheetMove(s, -setSheetPage(s))
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		setSheetMove(s, setSheetPage(s))
	case sdl.CONTROLLER_BUTTON_A:
		setSheetPick(s, s.cursor)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		setSheetClose(s)
	}
}

// setSheetMouse handles a click while the sheet is open: a row chooses, a
// click outside the card closes, anything else on the card is ignored.
func setSheetMouse(s *setSheetState, mx, my int32) {
	for i, r := range s.rects {
		if r.W > 0 && r.H > 0 && pointInRect(mx, my, r) {
			s.cursor = i
			setSheetPick(s, i)
			return
		}
	}
	if s.card.W > 0 && !pointInRect(mx, my, s.card) {
		setSheetClose(s)
	}
}

// setSheetWheel moves the cursor three rows per notch.
func setSheetWheel(s *setSheetState, dy int32) {
	switch {
	case dy > 0:
		setSheetMove(s, -3)
	case dy < 0:
		setSheetMove(s, 3)
	}
}

// setSheetDraw paints the sheet over whatever is on screen.
func setSheetDraw(renderer *sdl.Renderer, config *Config, s *setSheetState) {
	if renderer == nil || !s.open {
		return
	}
	label := FontForRole(config, TypeBody)
	small := FontForRole(config, TypeLabel)
	if label == nil || small == nil {
		return
	}
	drawOverlayScrim(renderer)

	w := int32(640)
	if w > screenWidth-48 {
		w = screenWidth - 48
	}
	top := headerBarHeight() + 8
	h := screenHeight - top - bottomBarHeight("Settings") - 16
	if h > 600 {
		h = 600
	}
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: top + (screenHeight-top-bottomBarHeight("Settings")-16-h)/2, W: w, H: h}
	s.card = card
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	header := overlayPageHeader(card, s.title, s.note, "list")
	bodyTop := UIPageHeaderDraw(renderer, config, header) + 12
	view := sdl.Rect{X: card.X + OverlayPadX, Y: bodyTop, W: card.W - 2*OverlayPadX, H: card.Y + card.H - OverlayPadX - bodyTop}
	s.view = view

	lh, sh := int32(label.Height()), int32(small.Height())
	rowH := lh + sh + 16
	if rowH < 52 {
		rowH = 52
	}
	const gap = int32(6)
	// Keep the cursor's row in view.
	cy := int32(s.cursor) * (rowH + gap)
	if cy < s.scroll {
		s.scroll = cy
	}
	if cy+rowH > s.scroll+view.H {
		s.scroll = cy + rowH - view.H
	}
	if s.scroll < 0 {
		s.scroll = 0
	}
	if cap(s.rects) < len(s.options) {
		s.rects = make([]sdl.Rect, len(s.options))
	}
	s.rects = s.rects[:len(s.options)]
	check := sh + 6
	clip := view
	_ = renderer.SetClipRect(&clip)
	for i, o := range s.options {
		r := sdl.Rect{X: view.X, Y: view.Y + int32(i)*(rowH+gap) - s.scroll, W: view.W, H: rowH}
		if r.Y+r.H < view.Y || r.Y > view.Y+view.H {
			s.rects[i] = sdl.Rect{}
			continue
		}
		s.rects[i] = r
		hovered := pointInRect(mouseX, mouseY, r) && i != s.cursor
		UIListRow(renderer, r, glassRowRadius, i == s.cursor, hovered)
		x := r.X + 16
		right := r.X + r.W - 16
		if o.Value == s.current {
			box := sdl.Rect{X: right - check, Y: r.Y + (r.H-check)/2, W: check, H: check}
			DrawHomeIcon(renderer, "check", box, true)
			right = box.X - 10
		}
		avail := right - x
		if o.Note == "" {
			drawText(renderer, label, ellipsize(label, o.Label, avail), x, r.Y+(r.H-lh)/2, ColorTextPrimary(), textAlignLeft)
			continue
		}
		ty := r.Y + (r.H-lh-sh-2)/2
		drawText(renderer, label, ellipsize(label, o.Label, avail), x, ty, ColorTextPrimary(), textAlignLeft)
		drawText(renderer, small, ellipsize(small, o.Note, avail), x, ty+lh+2, ColorTextSecondary(), textAlignLeft)
	}
	_ = renderer.SetClipRect(nil)
	content := int32(len(s.options))*(rowH+gap) - gap
	if content > view.H && view.H > 0 {
		thumb := float64(view.H) / float64(content)
		frac := float64(s.scroll) / float64(content-view.H)
		drawScrollbar(renderer, view.X+view.W+6, view.Y, 3, view.H, thumb, frac)
	}
}
