package main

// hometabs_edit.go — arranging the Tabs Home (hometabs.go): L2 on Home (Q on
// a keyboard), Start → Device → Edit Home and Settings → Display & theme →
// Tabs open a sheet over Home listing the tabs:
//
//   - Up/Down choose a row; A picks a tab up and the D-pad carries it to
//     another place on the strip, A or B puts it down;
//   - Left/Right step the tab's view (Grid, Rows, List, Carousel, Compact);
//   - Y (Delete) hides or shows the tab — one always stays;
//   - the rows after the tabs switch "Remember last tab", the look's three
//     (Tabs position, Dim unselected art, List edge fade — homelook.go) and
//     the Home layout;
//   - B, L2 (Esc, Q) close it.
//
// Every change is stored at once (homeTabsShown, homeTabViews); the sheet
// holds no draft.

import (
	"math"

	"github.com/veandco/go-sdl2/sdl"
)

// homeTabsEdit is the sheet's state. Render/event thread only.
var homeTabsEdit struct {
	open    bool
	idx     int
	holding bool
	order   []string
	hidden  map[string]bool
	enter   uiEnter
	card    sdl.Rect
	rows    []sdl.Rect
}

// homeTabsEditRows is how many rows the sheet has: the tabs, then Remember
// last tab, the look's rows and Home layout (homeTabsEditRemember…Layout).
func homeTabsEditRows() int { return len(homeTabsEdit.order) + homeTabsEditExtraRows }

// homeTabsEditToggle opens the sheet, or closes it when it is open (L2 is
// both). Edit Home's way in (homeEditBegin) lands here on the Tabs Home.
func homeTabsEditToggle(c *Config) {
	e := &homeTabsEdit
	if e.open {
		PlayBackSound()
		homeTabsEditClose(c)
		return
	}
	if !homeTabsOn() {
		return
	}
	e.order, e.hidden = parseHomeTabsShown(homeTabsCustom(c, homeTabsShownKey))
	e.idx = max(homeTabIndexIn(e.order, homeTabs.cur), 0)
	e.holding = false
	e.rows = e.rows[:0]
	e.open = true
	e.enter.begin(uiClockMS(), uiPanelEnterMS)
	homeCalendarOpen = false
	dismissWeatherPopover()
	dismissVolumePopover()
	dismissBrightnessPopover()
}

// homeTabsEditClose closes the sheet; a tab being carried is put down where
// it is (it was stored with each step).
func homeTabsEditClose(c *Config) {
	e := &homeTabsEdit
	if !e.open {
		return
	}
	e.open, e.holding = false, false
	e.rows = e.rows[:0]
}

// homeTabsEditSave stores the order and the hidden tabs.
func homeTabsEditSave(c *Config) {
	homeTabsWrite(c, homeTabsShownKey, formatHomeTabsShown(homeTabsEdit.order, homeTabsEdit.hidden))
}

// homeTabsEditMove is Up/Down: the next row, or the carried tab one place
// along.
func homeTabsEditMove(c *Config, step int) {
	e := &homeTabsEdit
	if e.holding {
		j := e.idx + step
		if e.idx < len(e.order) && j >= 0 && j < len(e.order) {
			e.order[e.idx], e.order[j] = e.order[j], e.order[e.idx]
			e.idx = j
			homeTabsEditSave(c)
			PlayNavSound()
		}
		return
	}
	if j := e.idx + step; j >= 0 && j < homeTabsEditRows() {
		e.idx = j
		PlayNavSound()
	}
}

// homeTabsEditChange is Left/Right (step -1/1) on the focused row: a tab's
// view, the Remember switch, or the Home layout.
func homeTabsEditChange(c *Config, step int) {
	e := &homeTabsEdit
	if c == nil || e.holding {
		return
	}
	switch {
	case e.idx < len(e.order):
		id := e.order[e.idx]
		homeTabsSetView(c, id, homeTabsNextView(homeTabsViewFor(c, id), step))
		if p := homeTabsPageOf(id); p != nil && len(p.levels) > 0 {
			p.levels[0].placed = false
		}
		PlayNavSound()
	case e.idx == len(e.order)+homeTabsEditRemember:
		on := !homeTabsRemember(c)
		homeTabsWrite(c, homeTabsRememberKey, boolString(on))
		if on {
			homeTabsNoteLast(c, homeTabs.cur)
		}
		PlayNavSound()
	case e.idx < len(e.order)+homeTabsEditLayout:
		homeLookEditChange(c, e.idx-len(e.order))
		PlayNavSound()
	default:
		n := len(homeArrangementChoiceList)
		i := homeArrangementChoiceIndex(homeArrangement(c))
		next := homeArrangementChoiceList[((i+step)%n+n)%n]
		homeTabsEditClose(c)
		homeSetArrangement(c, next)
		saveUserConfigDebounced(userConfigFromVariables(c))
		ShowToast("Home layout: "+homeArrangementChoiceLabel(next), ToastKindInfo)
		PlayNavSound()
	}
}

// homeTabsEditPrimary is A: pick a tab up or put it down; on the other rows
// the same as Right.
func homeTabsEditPrimary(c *Config) {
	e := &homeTabsEdit
	if e.idx < len(e.order) {
		e.holding = !e.holding
		if !e.holding {
			homeTabsEditSave(c)
		}
		PlayActivateSound()
		return
	}
	homeTabsEditChange(c, 1)
}

// homeTabsEditHide is Y (Delete): hide or show the focused tab. The last tab
// shown stays.
func homeTabsEditHide(c *Config) {
	e := &homeTabsEdit
	if e.holding || e.idx >= len(e.order) {
		return
	}
	id := e.order[e.idx]
	if !e.hidden[id] {
		shown := 0
		for _, t := range e.order {
			if !e.hidden[t] {
				shown++
			}
		}
		if shown <= 1 {
			ShowToast("One tab always stays on the strip", ToastKindInfo)
			return
		}
	}
	if e.hidden == nil {
		e.hidden = map[string]bool{}
	}
	e.hidden[id] = !e.hidden[id]
	homeTabsEditSave(c)
	PlayNavSound()
}

// homeTabsEditBack is B (Esc): put a carried tab down, or close.
func homeTabsEditBack(c *Config) {
	if homeTabsEdit.holding {
		homeTabsEdit.holding = false
		homeTabsEditSave(c)
		PlayBackSound()
		return
	}
	PlayBackSound()
	homeTabsEditClose(c)
}

// homeTabsEditPad is a pad press while the sheet is open. Start is left to
// the Start menu, which closes the sheet.
func homeTabsEditPad(ev *sdl.ControllerButtonEvent, c *Config) bool {
	switch ev.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		homeTabsEditMove(c, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		homeTabsEditMove(c, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		homeTabsEditChange(c, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		homeTabsEditChange(c, 1)
	case sdl.CONTROLLER_BUTTON_A:
		homeTabsEditPrimary(c)
	case sdl.CONTROLLER_BUTTON_Y:
		homeTabsEditHide(c)
	case sdl.CONTROLLER_BUTTON_B:
		homeTabsEditBack(c)
	case sdl.CONTROLLER_BUTTON_START:
		return false
	}
	return true
}

// homeTabsEditKey is a key while the sheet is open: the arrows, Enter, Y or
// Delete, Esc or Q.
func homeTabsEditKey(ev *sdl.KeyboardEvent, c *Config) bool {
	switch ev.Keysym.Sym {
	case sdl.K_UP:
		homeTabsEditMove(c, -1)
	case sdl.K_DOWN:
		homeTabsEditMove(c, 1)
	case sdl.K_LEFT:
		homeTabsEditChange(c, -1)
	case sdl.K_RIGHT:
		homeTabsEditChange(c, 1)
	case sdl.K_RETURN, sdl.K_SPACE:
		if ev.Repeat == 0 {
			homeTabsEditPrimary(c)
		}
	case sdl.K_y, sdl.K_DELETE, sdl.K_BACKSPACE:
		if ev.Repeat == 0 {
			homeTabsEditHide(c)
		}
	case sdl.K_ESCAPE:
		homeTabsEditBack(c)
	case sdl.K_q:
		if ev.Repeat == 0 {
			PlayBackSound()
			homeTabsEditClose(c)
		}
	}
	return true
}

// homeTabsEditClick is a click while the sheet is open: a row focuses and
// does what A does; outside the card closes it.
func homeTabsEditClick(c *Config, mx, my int32) {
	e := &homeTabsEdit
	if !pointInRect(mx, my, e.card) {
		PlayBackSound()
		homeTabsEditClose(c)
		return
	}
	for i, r := range e.rows {
		if !pointInRect(mx, my, r) {
			continue
		}
		e.holding = false
		e.idx = i
		if i < len(e.order) {
			// The right half is the view chip: a click steps the view; the
			// left half shows or hides the tab.
			if mx >= r.X+r.W/2 {
				homeTabsEditChange(c, 1)
			} else {
				homeTabsEditHide(c)
			}
			return
		}
		homeTabsEditChange(c, 1)
		return
	}
}

// homeTabsEditHints is the sheet's hint row for its focused row.
func homeTabsEditHints() []footerHint {
	e := &homeTabsEdit
	switch {
	case e.holding:
		return []footerHint{{button: "D-Pad", label: "Move"}, {button: "A", label: "Drop"}, {button: "B", label: "Drop"}}
	case e.idx < len(e.order):
		// Y on the pad, Delete on a keyboard (homeHintKey would name "/" for
		// a keyboard's Y, Home's search key, which the sheet does not use).
		hide := "Y"
		if hintsUseKeyboard() {
			hide = "Del"
		}
		return []footerHint{{button: "A", label: "Move"}, {button: "Left/Right", label: "View"},
			{button: hide, label: "Hide/Show"}, {button: "B", label: "Done"}}
	}
	return []footerHint{{button: "Left/Right", label: "Change"}, {button: "B", label: "Done"}}
}

// homeTabsEditFooter is Home's footer under the sheet: the same hints.
func homeTabsEditFooter() [len(homeFooterHintsStart)]footerHint {
	var set [len(homeFooterHintsStart)]footerHint
	for i, hint := range homeTabsEditHints() {
		if i < len(set) {
			set[i] = hint
		}
	}
	return set
}

// renderHomeTabsOverlays draws the sheet over Home, and closes it when
// another overlay has taken the screen. renderHomeModern calls it last.
func renderHomeTabsOverlays(renderer *sdl.Renderer, config *Config) {
	e := &homeTabsEdit
	if !e.open {
		return
	}
	if !homeTabsOn() || tb.startOpen || nc.open || globalSearchOpen || homePowerConfirm.open {
		homeTabsEditClose(config)
		return
	}
	if renderer == nil {
		return
	}
	fonts := loadHomeFonts()
	w, h := screenWidth, screenHeight
	t := 1.0
	if uiMotionEnabled(config) {
		t = e.enter.progress(uiClockMS())
	} else {
		e.enter.cancel()
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	scrim := glassScrimColor(uiEaseOutCubic(t))
	scrim.A = uint8(float64(scrim.A) * 0.75)
	setDrawColor(renderer, scrim)
	_ = renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: w, H: h})

	head, foot := scaleY(92, h), scaleY(60, h)
	rows := int32(homeTabsEditRows())
	avail := h - 2*scaleY(20, h) - head - foot
	rowH := min32(scaleY(50, h), avail/max32(rows, 1))
	cw := min32(scaleX(640, w), w-2*scaleX(24, w))
	ch := head + rows*rowH + foot
	rise := int32(math.Round(uiPanelRise.at(t)))
	card := sdl.Rect{X: (w - cw) / 2, Y: (h-ch)/2 + rise, W: cw, H: ch}
	e.card = card
	fillRoundedRect(renderer, card.X+3, card.Y+8, card.W, card.H, OverlayCardRadius, sdl.Color{R: 0, G: 0, B: 0, A: 90})
	drawGlassSheet(renderer, card, glassSheetFill())

	pad := scaleX(24, w)
	if f := fonts.Card; f != nil {
		drawText(renderer, f, "Arrange tabs", card.X+pad, card.Y+scaleY(20, h), ColorTextPrimary(), textAlignLeft)
		if note := fonts.Helper; note != nil {
			drawText(renderer, note, ellipsize(note, "Order the tabs, hide the ones you do not use and pick each one's view", card.W-2*pad),
				card.X+pad, card.Y+scaleY(20, h)+int32(f.Height())+scaleY(2, h), ColorTextMuted(), textAlignLeft)
		}
	}
	e.rows = e.rows[:0]
	y := card.Y + head
	for i := 0; i < int(rows); i++ {
		r := sdl.Rect{X: card.X + scaleX(12, w), Y: y + scaleY(2, h), W: card.W - 2*scaleX(12, w), H: rowH - scaleY(4, h)}
		e.rows = append(e.rows, r)
		homeTabsEditDrawRow(renderer, fonts, config, i, r)
		y += rowH
	}
	homeTabsEditDrawHints(renderer, fonts, card, foot)
}

// homeTabsEditDrawRow draws row i: a tab with its view, or one of the two
// switches.
func homeTabsEditDrawRow(renderer *sdl.Renderer, fonts HomeFonts, c *Config, i int, r sdl.Rect) {
	e := &homeTabsEdit
	w, h := screenWidth, screenHeight
	focused := i == e.idx
	radius := scaleY(14, h)
	switch {
	case focused && e.holding:
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, WithAlpha(ColorAccent, 70))
		strokeRoundedRectAt(renderer, r, homeFocusGlide.apply(r), radius, 2, ColorAccent)
	case focused:
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, homeGlass(24))
		strokeRoundedRectAt(renderer, r, homeFocusGlide.apply(r), radius, 2, ColorAccent)
	}
	f, small := fonts.Tile, fonts.Small
	if f == nil || small == nil {
		return
	}
	label, value, note := "", "", ""
	muted := false
	switch {
	case i < len(e.order):
		id := e.order[i]
		label = homeTabLabel(id)
		value = homeTabViewLabel(homeTabsViewFor(c, id))
		if e.hidden[id] {
			note, muted = "Hidden", true
		}
	case i == len(e.order)+homeTabsEditRemember:
		label, value = "Remember last tab", setOnOff(homeTabsRemember(c))
	case i < len(e.order)+homeTabsEditLayout:
		label, value = homeLookEditRow(c, i-len(e.order))
	default:
		label, value = "Home layout", homeArrangementChoiceLabel(homeArrangement(c))
	}
	right := r.X + r.W - scaleX(12, w)
	// The value in a chip with the arrows that change it.
	vw, _ := measureText(small, value)
	fh := int32(small.Height())
	chip := sdl.Rect{W: vw + scaleX(44, w), H: fh + scaleY(8, h)}
	chip.X, chip.Y = right-chip.W, r.Y+(r.H-chip.H)/2
	fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, chip.H/2, glassChipFill())
	drawText(renderer, small, value, chip.X+chip.W/2, chip.Y+(chip.H-fh)/2, glassChipInk(), textAlignCenter)
	if focused && !e.holding {
		ty := chip.Y + (chip.H-fh)/2
		drawText(renderer, small, "‹", chip.X+scaleX(12, w), ty, ColorAccent, textAlignCenter)
		drawText(renderer, small, "›", chip.X+chip.W-scaleX(12, w), ty, ColorAccent, textAlignCenter)
	}
	tx := r.X + scaleX(16, w)
	ink := ColorTextPrimary()
	if muted {
		ink = ColorTextTertiary()
	}
	room := chip.X - scaleX(16, w) - tx
	if room <= 0 {
		return
	}
	th := int32(f.Height())
	drawText(renderer, f, ellipsize(f, label, room), tx, r.Y+(r.H-th)/2, ink, textAlignLeft)
	if note != "" {
		lw, _ := measureText(f, label)
		if nx := tx + lw + scaleX(12, w); nx < chip.X-scaleX(16, w) {
			drawText(renderer, small, note, nx, r.Y+(r.H-fh)/2, ColorTextTertiary(), textAlignLeft)
		}
	}
}

// homeTabsEditDrawHints draws the sheet's own hint row (the footer is under
// the scrim).
func homeTabsEditDrawHints(renderer *sdl.Renderer, fonts HomeFonts, card sdl.Rect, foot int32) {
	f := fonts.Small
	if f == nil {
		return
	}
	w, h := screenWidth, screenHeight
	badgeD := scaleY(22, h)
	gap := scaleX(7, w)
	itemGap := scaleX(18, w)
	fh := int32(f.Height())
	cy := card.Y + card.H - foot/2
	hints := homeTabsEditHints()
	total := int32(0)
	badgeWs := make([]int32, len(hints))
	groupWs := make([]int32, len(hints))
	for i, g := range hints {
		bw, _ := measureText(f, homeHintKey(g.button, g.label))
		lw, _ := measureText(f, g.label)
		badgeWs[i] = max32(badgeD, bw+scaleX(12, w))
		groupWs[i] = badgeWs[i] + gap + lw
		total += groupWs[i]
	}
	total += itemGap * int32(len(hints)-1)
	x := card.X + card.W - scaleX(24, w) - total
	for i, g := range hints {
		badge := sdl.Rect{X: x, Y: cy - badgeD/2, W: badgeWs[i], H: badgeD}
		fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, homeGlass(16))
		strokeRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, 1, homeGlass(45))
		drawText(renderer, f, homeHintKey(g.button, g.label), badge.X+badge.W/2, badge.Y+(badge.H-fh)/2, homeGlass(210), textAlignCenter)
		drawText(renderer, f, g.label, badge.X+badge.W+gap, cy-fh/2, ColorTextMuted(), textAlignLeft)
		x += groupWs[i] + itemGap
	}
}

// ── Settings ─────────────────────────────────────────────────────────────

// homeTabsOnTabs reports whether the stored Home layout is Tabs: the rows
// below only mean something then.
func homeTabsOnTabs(c *Config) bool {
	return homeArrangement(c) == homeArrangementTabs
}

// homeTabsSettingsRow is Display & theme → Tabs: the arranging sheet, on
// Home.
func homeTabsSettingsRow() setRow {
	return setRow{
		ID: "homeTabs", Label: "Tabs", Kind: setKindAction,
		Detail: func(*Config) string {
			if hintsUseKeyboard() {
				return "Order, hide and pick each tab's view — Q on Home"
			}
			return "Order, hide and pick each tab's view — L2 on Home"
		},
		Visible: homeTabsOnTabs,
		Open:    homeEditOpen,
	}
}

// homeTabsRememberRow is Display & theme → Remember last tab.
func homeTabsRememberRow() setRow {
	return setRow{
		ID: "homeTabsRemember", Label: "Remember last tab", Kind: setKindToggle,
		Detail:  setStatic("Home opens on the tab you left it on; off, it opens on Main"),
		Visible: homeTabsOnTabs,
		Get:     homeTabsRemember,
		Set: func(c *Config, on bool) {
			homeTabsWrite(c, homeTabsRememberKey, boolString(on))
			if on {
				homeTabsNoteLast(c, homeTabs.cur)
			}
		},
	}
}
