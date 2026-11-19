package main

// qs_edit.go — Edit Quick Settings: arranging the panel's buttons and
// switching its layout, the way Edit Home arranges Home (home_edit.go).
//
// L2 in the panel (Q or Home on a keyboard) opens it. While it is open:
//
//   - a strip replaces the title: the three layouts (Tiles, Compact, List),
//     Reset and Done. L1/R1 (Tab and Shift+Tab, PgUp/PgDn) switch the layout
//     from anywhere; Up from the top row reaches the strip, Down leaves it;
//   - A picks the focused button up and the D-pad carries it (Left/Right one
//     place earlier or later, Up/Down to the place of the button above or
//     below); A or B puts it down;
//   - X (R) resizes a tile on the Tiles and Compact grids: Right and Down
//     grow it a cell, Left and Up shrink it, from one cell (two across for a
//     slider) to the grid's width and two rows; A, B or X end resizing;
//   - Y (Delete, Backspace) hides the focused button, or shows a hidden one
//     again. Hidden buttons stay on the grid while editing, faded, so they
//     can be brought back; outside Edit they are not drawn;
//   - B (Esc) or L2 (Q) ends editing. Reset puts the default arrangement back
//     on a second press within three seconds.
//
// With the pointer, a click on a strip chip presses it, a click focuses a
// button and a second click picks it up, and a click on another button puts
// the carried one there. The focused tile wears a hide badge in its top-right
// corner and, on a grid, a size chip that steps through the usual sizes.
//
// The arrangement being edited is a copy (qsEd.work), saved when a button is
// put down, resized, hidden or shown, on a reset, a layout switch and when
// Edit closes — so carrying a button across the grid is not a save per step.

import "github.com/veandco/go-sdl2/sdl"

const (
	// The strip's chips after the layouts.
	qsEditChipReset = len(qsLayoutChoices)
	qsEditChipDone  = qsEditChipReset + 1
	qsEditChipCount = qsEditChipReset + 2
)

// qsSizePresets are the sizes a click on the size chip steps through, by
// area: a cell, a wide cell, three across, a square, four across, the
// Compact grid's whole width, and the tall ones.
var qsSizePresets = [...]homeTileSize{{1, 1}, {2, 1}, {3, 1}, {2, 2}, {4, 1}, {6, 1}, {4, 2}, {6, 2}}

// qsEd is Edit's state. Render/event thread only.
var qsEd struct {
	open bool
	// work is the arrangement being edited; dirty says it has changes not
	// saved yet.
	work  []qsTileEntry
	dirty bool
	// holding: the focused button is picked up. resizing: the D-pad sizes it.
	holding  bool
	resizing bool
	// strip: the strip has the focus, on chip stripIdx.
	strip    bool
	stripIdx int
	// resetAt is when Reset was armed (0 = not armed).
	resetAt uint64
	// As last drawn, for the pointer: the strip's chips, and the focused
	// tile's hide badge and size chip.
	chips     [qsEditChipCount]sdl.Rect
	hideBadge sdl.Rect
	sizeChip  sdl.Rect
}

// qsEditClearRects forgets the pointer targets drawn last.
func qsEditClearRects() {
	qsEd.chips = [qsEditChipCount]sdl.Rect{}
	qsEd.hideBadge = sdl.Rect{}
	qsEd.sizeChip = sdl.Rect{}
}

// qsEditOpen starts editing the open panel.
func qsEditOpen(config *Config) {
	if !qs.open || qsEd.open {
		return
	}
	if config == nil {
		config = appConfig
	}
	qsEd.work = append([]qsTileEntry(nil), qsTilesCurrent(config)...)
	qsEd.dirty = false
	qsEd.holding, qsEd.resizing = false, false
	qsEd.strip = false
	qsEd.stripIdx = qsLayoutIndex(qsLayoutCurrent(config))
	qsEd.resetAt = 0
	qsEditClearRects()
	qsLay.adjusting = false
	qsEd.open = true
	PlayActivateSound()
	qsLayoutSync(config)
	if len(qsLay.slots) == 0 {
		qsEd.strip = true
	}
}

// qsEditClose ends editing, saving what is not saved yet. Safe to call when
// Edit is not open: the panel calls it whenever it opens or closes, because
// many screens close the panel by clearing qs.open directly.
func qsEditClose(config *Config) {
	if !qsEd.open {
		return
	}
	if config == nil {
		config = appConfig
	}
	qsEd.holding, qsEd.resizing = false, false
	if qsEd.dirty {
		qsEditCommit(config)
	}
	qsEd.open = false
	qsEd.strip = false
	qsEd.resetAt = 0
	qsEd.work = nil
	qsEditClearRects()
	qsLayoutSync(config)
}

// qsEditCommit saves the arrangement being edited.
func qsEditCommit(config *Config) {
	qsTilesSet(config, qsEd.work)
	qsEd.dirty = false
}

// qsEditChanged records a change and lays the panel out again at once, so
// the next press already sees the new places.
func qsEditChanged(config *Config) {
	qsEd.dirty = true
	qsLayoutSync(config)
}

// qsEditEntry is the focused button and its index on the list being edited.
func qsEditEntry() (qsSlot, int, bool) {
	f, ok := qsFocused()
	if !ok {
		return qsSlot{}, -1, false
	}
	i := qsTilesIndex(qsEd.work, f.ID)
	return f, i, i >= 0
}

// ── Edits ─────────────────────────────────────────────────────────────────

// qsEditMoveTo puts the focused button where the button on slot k is.
func qsEditMoveTo(config *Config, k int) bool {
	f, i, ok := qsEditEntry()
	if !ok || k < 0 || k >= len(qsLay.slots) || k == qs.cursor {
		return false
	}
	j := qsTilesIndex(qsEd.work, qsLay.slots[k].ID)
	if j < 0 || j == i {
		return false
	}
	qsEd.work = qsTilesMoveEntry(qsEd.work, i, j)
	qsEditChanged(config)
	qsFocusID(f.ID)
	return true
}

// qsEditMoveHeld carries the held button one step: on the list, and sideways
// on a grid, one place earlier or later; up and down a grid, to the place of
// the button above or below.
func qsEditMoveHeld(config *Config, dir FocusDirection) {
	if _, _, ok := qsEditEntry(); !ok {
		qsEd.holding = false
		return
	}
	k := -1
	if qsLay.cols == 0 || dir == FocusLeft || dir == FocusRight {
		switch dir {
		case FocusLeft, FocusUp:
			k = qs.cursor - 1
		case FocusRight, FocusDown:
			k = qs.cursor + 1
		}
	} else {
		k = qsGridStep(qsLay.cells, qs.cursor, dir, qsLay.wantCol)
	}
	if qsEditMoveTo(config, k) {
		PlayNavSound()
	}
}

// qsEditDrop puts the held button down where it is.
func qsEditDrop(config *Config) {
	qsEd.holding = false
	if qsEd.dirty {
		qsEditCommit(config)
	}
	PlayActivateSound()
}

// qsEditResizable is the focused button when it can change size: on a grid.
func qsEditResizable() (qsSlot, int, bool) {
	f, i, ok := qsEditEntry()
	if !ok || qsLay.cols == 0 {
		return f, i, false
	}
	return f, i, true
}

// qsEditResizeToggle is X: it starts resize mode, where the D-pad grows and
// shrinks the tile, and X again ends it. A carried button is put down first.
func qsEditResizeToggle(config *Config) {
	if qsEd.resizing {
		qsEditResizeEnd(config)
		return
	}
	if qsLay.cols == 0 {
		ShowToast("Sizes are for the Tiles and Compact layouts (L1/R1)", ToastKindInfo)
		return
	}
	if _, _, ok := qsEditResizable(); !ok {
		return
	}
	if qsEd.holding {
		qsEd.holding = false
		if qsEd.dirty {
			qsEditCommit(config)
		}
	}
	qsEd.resizing = true
	PlayActivateSound()
}

// qsEditResizeEnd leaves resize mode and saves the size it reached.
func qsEditResizeEnd(config *Config) {
	if !qsEd.resizing {
		return
	}
	qsEd.resizing = false
	if qsEd.dirty {
		qsEditCommit(config)
	}
	PlayActivateSound()
}

// qsEditResizeStep is a D-pad press in resize mode: Right and Down add a
// column or a row, Left and Up take one away, inside the button's limits on
// this grid. The grid packs again at once and the focus stays with the tile.
func qsEditResizeStep(config *Config, dir FocusDirection) {
	f, i, ok := qsEditResizable()
	if !ok {
		qsEd.resizing = false
		return
	}
	spec, ok := qsSpecFor(f.ID)
	if !ok {
		return
	}
	dw, dh := 0, 0
	switch dir {
	case FocusLeft:
		dw = -1
	case FocusRight:
		dw = 1
	case FocusUp:
		dh = -1
	case FocusDown:
		dh = 1
	}
	cur := qsSnapSize(spec, qsEd.work[i].Size, qsLay.cols)
	next := qsSnapSize(spec, homeTileSize{W: cur.W + dw, H: cur.H + dh}, qsLay.cols)
	if next == cur {
		// Already as small (or as large) as the tile goes that way.
		PlayBackSound()
		return
	}
	qsEd.work[i].Size = next
	qsEditChanged(config)
	qsFocusID(f.ID)
	PlayNavSound()
}

// qsSizeCycle is what the size chip steps through for a button on a grid
// cols wide: the presets inside its limits, smallest first.
func qsSizeCycle(spec qsTileSpec, cols int) []homeTileSize {
	lo, hi := qsSizeLimits(spec, cols)
	out := make([]homeTileSize, 0, len(qsSizePresets))
	for _, c := range qsSizePresets {
		if c.W < lo.W || c.H < lo.H || c.W > hi.W || c.H > hi.H {
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		out = append(out, lo)
	}
	return out
}

// qsEditResizeCycle steps the focused tile to the next of the usual sizes
// and saves: a click on its size chip.
func qsEditResizeCycle(config *Config) {
	f, i, ok := qsEditResizable()
	if !ok {
		return
	}
	spec, ok := qsSpecFor(f.ID)
	if !ok {
		return
	}
	cur := qsSnapSize(spec, qsEd.work[i].Size, qsLay.cols)
	cycle := qsSizeCycle(spec, qsLay.cols)
	next := cycle[0]
	found := false
	for k, c := range cycle {
		if c == cur {
			next, found = cycle[(k+1)%len(cycle)], true
			break
		}
	}
	if !found {
		// A size between the presets (one the D-pad made) goes on to the
		// first larger one.
		for _, c := range cycle {
			if c.W*c.H > cur.W*cur.H {
				next = c
				break
			}
		}
	}
	qsEd.resizing, qsEd.holding = false, false
	qsEd.work[i].Size = next
	qsEditChanged(config)
	qsEditCommit(config)
	qsFocusID(f.ID)
	PlayNavSound()
}

// qsEditToggleHidden is Y: hide the focused button, or show it again.
func qsEditToggleHidden(config *Config) {
	f, i, ok := qsEditEntry()
	if !ok {
		return
	}
	qsEd.holding, qsEd.resizing = false, false
	qsEd.work[i].Hidden = !qsEd.work[i].Hidden
	qsEditChanged(config)
	qsEditCommit(config)
	qsFocusID(f.ID)
	if qsEd.work[i].Hidden {
		PlayBackSound()
	} else {
		PlayActivateSound()
	}
}

// qsEditReset puts the default arrangement back: the first press arms it, a
// second within homeEditResetArmMS resets. The layout is left alone.
func qsEditReset(config *Config) {
	now := sdl.GetTicks64()
	if qsEd.resetAt == 0 || now-qsEd.resetAt > homeEditResetArmMS {
		qsEd.resetAt = now
		PlayNavSound()
		return
	}
	qsEd.resetAt = 0
	qsEd.holding, qsEd.resizing = false, false
	qsEd.work = qsDefaultTiles()
	qsEditChanged(config)
	qsEditCommit(config)
	qs.cursor = 0
	qsNoteCol()
	ShowToast("Quick Settings reset", ToastKindInfo)
	PlayActivateSound()
}

// qsEditResetArmed reports whether Reset is waiting for its second press.
func qsEditResetArmed() bool {
	return qsEd.resetAt != 0 && sdl.GetTicks64()-qsEd.resetAt <= homeEditResetArmMS
}

// qsEditChooseLayout switches the panel to layout i of qsLayoutChoices.
func qsEditChooseLayout(config *Config, i int) {
	if i < 0 || i >= len(qsLayoutChoices) {
		return
	}
	if qsEd.dirty {
		qsEditCommit(config)
	}
	qsEd.holding, qsEd.resizing = false, false
	qsEd.resetAt = 0
	qsEd.stripIdx = i
	if name := qsLayoutChoices[i]; name != qsLayoutCurrent(config) {
		qsLayoutSet(config, name)
	}
	qsLayoutSync(config)
	qsNoteCol()
	PlayNavSound()
}

// qsEditCycleLayout steps the layout (L1/R1, Tab).
func qsEditCycleLayout(config *Config, step int) {
	n := len(qsLayoutChoices)
	cur := qsLayoutIndex(qsLayoutCurrent(config))
	qsEditChooseLayout(config, ((cur+step)%n+n)%n)
}

// ── Input ─────────────────────────────────────────────────────────────────

// qsEditNavigate is a D-pad press while editing.
func qsEditNavigate(config *Config, dir FocusDirection) {
	if qsEd.resizing {
		qsEditResizeStep(config, dir)
		return
	}
	if qsEd.strip {
		qsEditStripMove(dir)
		return
	}
	if qsEd.holding {
		qsEditMoveHeld(config, dir)
		return
	}
	n := len(qsLay.slots)
	if n == 0 {
		qsEd.strip = true
		return
	}
	if qsLay.cols == 0 {
		switch dir {
		case FocusUp:
			if qs.cursor == 0 {
				qsEditToStrip()
				return
			}
			qs.cursor--
			PlayNavSound()
		case FocusDown:
			if qs.cursor < n-1 {
				qs.cursor++
				PlayNavSound()
			}
		}
		return
	}
	if qsGridMove(dir) {
		return
	}
	if dir == FocusUp {
		qsEditToStrip()
	}
}

// qsEditToStrip gives the strip the focus, on the layout in force.
func qsEditToStrip() {
	qsEd.strip = true
	qsEd.stripIdx = qsLayoutIndex(qsLay.layout)
	PlayNavSound()
}

// qsEditStripMove walks the strip; Down returns to the buttons.
func qsEditStripMove(dir FocusDirection) {
	switch dir {
	case FocusLeft:
		if qsEd.stripIdx > 0 {
			qsEd.stripIdx--
			PlayNavSound()
		}
	case FocusRight:
		if qsEd.stripIdx < qsEditChipCount-1 {
			qsEd.stripIdx++
			PlayNavSound()
		}
	case FocusDown:
		if len(qsLay.slots) > 0 {
			qsEd.strip = false
			PlayNavSound()
		}
	}
}

// qsEditStripActivate presses strip chip i.
func qsEditStripActivate(config *Config, i int) {
	switch {
	case i >= 0 && i < len(qsLayoutChoices):
		qsEditChooseLayout(config, i)
	case i == qsEditChipReset:
		qsEditReset(config)
	case i == qsEditChipDone:
		PlayBackSound()
		qsEditClose(config)
	}
}

// qsEditPrimary is A: the strip's chip, or picking up and putting down the
// focused button. In resize mode it is done resizing.
func qsEditPrimary(config *Config) {
	switch {
	case qsEd.resizing:
		qsEditResizeEnd(config)
	case qsEd.strip:
		qsEditStripActivate(config, qsEd.stripIdx)
	case qsEd.holding:
		qsEditDrop(config)
	default:
		if _, ok := qsFocused(); ok {
			qsEd.holding = true
			PlayActivateSound()
		}
	}
}

// qsEditBack is B: end resizing, put the held button down, else finish.
func qsEditBack(config *Config) {
	switch {
	case qsEd.resizing:
		qsEditResizeEnd(config)
	case qsEd.holding:
		qsEditDrop(config)
	default:
		PlayBackSound()
		qsEditClose(config)
	}
}

// qsEditShoulder is a shoulder or trigger while editing: L1/R1 switch the
// layout (not mid-resize, which would lose track of the tile) and L2 is
// Done.
func qsEditShoulder(s padShoulder, config *Config) {
	switch s {
	case padL1, padR1:
		if qsEd.resizing {
			return
		}
		step := -1
		if s == padR1 {
			step = 1
		}
		qsEditCycleLayout(config, step)
	case padL2:
		PlayBackSound()
		qsEditClose(config)
	}
}

// qsEditPad is the controller while editing.
func qsEditPad(e *sdl.ControllerButtonEvent, config *Config) {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		qsEditNavigate(config, FocusUp)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		qsEditNavigate(config, FocusDown)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		qsEditNavigate(config, FocusLeft)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		qsEditNavigate(config, FocusRight)
	case sdl.CONTROLLER_BUTTON_A:
		qsEditPrimary(config)
	case sdl.CONTROLLER_BUTTON_X:
		if !qsEd.strip {
			qsEditResizeToggle(config)
		}
	case sdl.CONTROLLER_BUTTON_Y:
		if !qsEd.strip && !qsEd.resizing {
			qsEditToggleHidden(config)
		}
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		qsEditShoulder(padL1, config)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		qsEditShoulder(padR1, config)
	case sdl.CONTROLLER_BUTTON_B:
		qsEditBack(config)
	case sdl.CONTROLLER_BUTTON_RIGHTSTICK:
		// F2 on a Brick puts the whole panel away, saving the edit.
		if brickHasFunctionKeys() {
			qsEditClose(config)
			qsToggleOpen()
			PlayBackSound()
		}
	case sdl.CONTROLLER_BUTTON_BACK:
		// Select starts chords (Select + L1, the virtual mouse; Select + R1,
		// the control profiles): as in Edit Home it does nothing on its own
		// here, so a chord cannot end the edit.
	}
}

// qsEditKey is the keyboard while editing: the arrows, Enter (A), X or R
// (resize), Y, Delete or Backspace (hide/show), Tab / Shift+Tab or PgUp/PgDn
// (layout), Esc or B (back), and Q or Home, L2's twins, which are Done.
func qsEditKey(e *sdl.KeyboardEvent, config *Config) {
	switch e.Keysym.Sym {
	case sdl.K_UP:
		qsEditNavigate(config, FocusUp)
	case sdl.K_DOWN:
		qsEditNavigate(config, FocusDown)
	case sdl.K_LEFT:
		qsEditNavigate(config, FocusLeft)
	case sdl.K_RIGHT:
		qsEditNavigate(config, FocusRight)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		if e.Repeat == 0 {
			qsEditPrimary(config)
		}
	case sdl.K_x, sdl.K_r:
		if e.Repeat == 0 && !qsEd.strip {
			qsEditResizeToggle(config)
		}
	case sdl.K_DELETE, sdl.K_BACKSPACE, sdl.K_y:
		if e.Repeat == 0 && !qsEd.strip && !qsEd.resizing {
			qsEditToggleHidden(config)
		}
	case sdl.K_TAB:
		if e.Repeat == 0 && !qsEd.resizing {
			step := 1
			if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
				step = -1
			}
			qsEditCycleLayout(config, step)
		}
	case sdl.K_PAGEUP, sdl.K_PAGEDOWN:
		if e.Repeat == 0 {
			qsEditShoulder(keyShoulder(e.Keysym.Sym), config)
		}
	case sdl.K_ESCAPE, sdl.K_b:
		if e.Repeat == 0 {
			qsEditBack(config)
		}
	case sdl.K_q, sdl.K_HOME:
		if e.Repeat != 0 {
			break
		}
		if e.Keysym.Sym == sdl.K_q && e.Keysym.Mod&sdl.KMOD_CTRL != 0 {
			// Ctrl+Q puts the whole panel away, saving the edit.
			qsEditClose(config)
			qs.open = false
			PlayBackSound()
			break
		}
		qsEditShoulder(padL2, config)
	}
}

// qsEditClick is a left press inside the panel while editing.
func qsEditClick(mx, my int32, config *Config) {
	for i, r := range qsEd.chips {
		if r.W > 0 && pointInRect(mx, my, r) {
			// A carried button is put down quietly: the chip has a sound of
			// its own.
			qsEd.holding, qsEd.resizing = false, false
			if qsEd.dirty {
				qsEditCommit(config)
			}
			qsEd.strip, qsEd.stripIdx = true, i
			qsEditStripActivate(config, i)
			return
		}
	}
	if qsEd.hideBadge.W > 0 && pointInRect(mx, my, qsEd.hideBadge) {
		qsEditToggleHidden(config)
		return
	}
	if qsEd.sizeChip.W > 0 && pointInRect(mx, my, qsEd.sizeChip) {
		qsEditResizeCycle(config)
		return
	}
	i := qsSlotAt(mx, my)
	if i < 0 {
		return
	}
	if qsEd.holding {
		// A click on another button puts the carried one in its place.
		qsEditMoveTo(config, i)
		qsEditDrop(config)
		return
	}
	if qsEd.resizing {
		qsEditResizeEnd(config)
	}
	wasStrip := qsEd.strip
	qsEd.strip = false
	if i == qs.cursor && !wasStrip {
		// A second click on the focused button picks it up (with the strip
		// focused no button showed the focus, so this click only gives it).
		qsEd.holding = true
		PlayActivateSound()
		return
	}
	qs.cursor = i
	qsNoteCol()
	PlayNavSound()
}
