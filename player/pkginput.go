package main

// pkginput.go — the Packages store's input.
//
// Every device reduces to the same actions: the pad and the keyboard map
// buttons onto pkgAct values, a click hits the targets the last frame
// recorded, a wheel or a finger drag scrolls what is under it. B and Escape
// close the details sheet first and leave the screen otherwise; X / R / F5
// refresh; Y / V switch between the rows, grid and list views.
//
// A press on a card opens it on release, and only if the press did not turn
// into a drag: that is what lets a finger swipe a shelf on a touch screen
// without opening every card it starts on.
//
// Up at the top of any view reaches the "More stores" strip above the content
// (pkgstores.go), and Up from the strip the top bar.

import (
	"github.com/veandco/go-sdl2/sdl"
)

// pkgAct is one store action, whatever device asked for it.
type pkgAct int

const (
	pkgActUp pkgAct = iota
	pkgActDown
	pkgActLeft
	pkgActRight
	pkgActConfirm
	pkgActBack
	pkgActRefresh
	pkgActView
	pkgActPageUp
	pkgActPageDown
	pkgActFirst
	pkgActLast
	pkgActNextChip
	pkgActPrevChip
	pkgActSort
	pkgActPrevItem
	pkgActNextItem
)

// ── Focus helpers ────────────────────────────────────────────────────────

// pkgHasHero reports whether the rows view opens with the hero banner.
func pkgHasHero(m *pkgModel) bool {
	mode, _ := pkgHeroFor(m)
	return mode != pkgHeroNone
}

// pkgRowCount is the rows view's focus rows: the hero, then every shelf.
func pkgRowCount(m *pkgModel) int {
	if m == nil {
		return 0
	}
	n := len(m.Shelves)
	if pkgHasHero(m) {
		n++
	}
	return n
}

// pkgShelfAtRow is the shelf on a focus row (nil for the hero) and its index.
func pkgShelfAtRow(m *pkgModel, row int) (*pkgShelf, int) {
	if m == nil {
		return nil, -1
	}
	if pkgHasHero(m) {
		if row == 0 {
			return nil, -1
		}
		row--
	}
	if row < 0 || row >= len(m.Shelves) {
		return nil, -1
	}
	return &m.Shelves[row], row
}

// pkgNoteRowFocus remembers the focused row by identity, so a rebuilt model
// (an update finishing, the list refreshing) keeps the focus on the same
// shelf even when shelves above it come or go.
func pkgNoteRowFocus() {
	st := &pkgUI
	if shelf, _ := pkgShelfAtRow(st.model, st.rowFocus); shelf != nil {
		st.rowFocusID = shelf.ID
		return
	}
	st.rowFocusID = "hero"
}

// pkgRestoreRowFocus puts the rows focus back on the remembered shelf.
func pkgRestoreRowFocus() {
	st := &pkgUI
	m := st.model
	if m == nil {
		return
	}
	hero := pkgHasHero(m)
	switch {
	case st.rowFocusID == "hero" && hero:
		st.rowFocus = 0
		return
	case st.rowFocusID != "":
		for i := range m.Shelves {
			if m.Shelves[i].ID == st.rowFocusID {
				st.rowFocus = i
				if hero {
					st.rowFocus++
				}
				return
			}
		}
	}
	st.rowFocus = clampIndex(st.rowFocus, pkgRowCount(m))
}

// pkgCurrentFocusID names the focused card or button for the lift animation.
func pkgCurrentFocusID() pkgFocusKey {
	st := &pkgUI
	m := st.model
	if m == nil || st.sheet.open || !pkgBodyFocused() {
		return pkgFocusKey{}
	}
	switch st.view {
	case pkgViewRows:
		shelf, _ := pkgShelfAtRow(m, st.rowFocus)
		if shelf == nil {
			if pkgHasHero(m) && st.rowFocus == 0 {
				return pkgFocusKey{kind: 'h', btn: st.heroBtn}
			}
			return pkgFocusKey{}
		}
		if len(shelf.Items) == 0 {
			return pkgFocusKey{}
		}
		col := clampIndex(st.rowCols[shelf.ID], len(shelf.Items))
		return pkgFocusKey{kind: 'r', shelf: shelf.ID, key: shelf.Items[col].Key}
	case pkgViewGrid:
		if st.chipFocus || len(st.filtered) == 0 {
			return pkgFocusKey{}
		}
		return pkgFocusKey{kind: 'g', key: st.filtered[clampIndex(st.gridIndex, len(st.filtered))].Key}
	}
	if st.chipFocus || len(st.filtered) == 0 {
		return pkgFocusKey{}
	}
	return pkgFocusKey{kind: 'l', key: st.filtered[clampIndex(st.listIndex, len(st.filtered))].Key}
}

// pkgFocusedItem is the package the focus is on, in any view (the hero's
// package when the hero holds the focus).
func pkgFocusedItem() *pkgItem {
	st := &pkgUI
	m := st.model
	if m == nil {
		return nil
	}
	switch st.view {
	case pkgViewRows:
		shelf, _ := pkgShelfAtRow(m, st.rowFocus)
		if shelf == nil {
			_, it := pkgHeroFor(m)
			return it
		}
		if len(shelf.Items) == 0 {
			return nil
		}
		return shelf.Items[clampIndex(st.rowCols[shelf.ID], len(shelf.Items))]
	case pkgViewGrid:
		pkgEnsureFiltered()
		if len(st.filtered) == 0 {
			return nil
		}
		return st.filtered[clampIndex(st.gridIndex, len(st.filtered))]
	}
	pkgEnsureFiltered()
	if len(st.filtered) == 0 {
		return nil
	}
	return st.filtered[clampIndex(st.listIndex, len(st.filtered))]
}

// ── Actions ──────────────────────────────────────────────────────────────

// pkgDo runs one action on the store.
func pkgDo(config *Config, act pkgAct) {
	st := &pkgUI
	pkgEnsureMaps()
	if st.model == nil || st.dirty {
		pkgSync(config)
	}
	if st.sheet.open {
		pkgSheetDo(config, act)
		return
	}
	switch act {
	case pkgActBack:
		if config != nil {
			goBackScene(config)
		}
		return
	case pkgActRefresh:
		pkgRequestRefresh(config)
		return
	case pkgActView:
		pkgSetView(config, (st.view+1)%pkgViewModes)
		return
	case pkgActSort:
		if st.view != pkgViewRows {
			pkgCycleSort(config)
		}
		return
	}
	// The "More stores" strip takes the D-pad and A while it holds the focus;
	// what it leaves (Tab's chips, the jump to the last row) goes to the view.
	if pkgStoresFocused() && pkgStoresDo(config, act) {
		return
	}
	if st.view == pkgViewRows {
		pkgRowsDo(config, act)
		return
	}
	pkgGridDo(config, act)
}

// pkgSetView switches the view and keeps the same package focused.
func pkgSetView(config *Config, v pkgViewMode) {
	st := &pkgUI
	if v < 0 || v >= pkgViewModes || v == st.view {
		return
	}
	key := ""
	if it := pkgFocusedItem(); it != nil {
		key = it.Key
	}
	st.view = v
	st.chipFocus = false
	switch v {
	case pkgViewGrid:
		if key != "" {
			st.gridKey = key
		}
	case pkgViewList:
		if key != "" {
			st.listKey = key
		}
	}
	st.filterValid = false
	st.reveal = true
	PlayToggleSound()
	pkgSavePrefs(config)
}

// pkgCycleSort steps the grid/list sort and keeps the focused package.
func pkgCycleSort(config *Config) {
	st := &pkgUI
	if it := pkgFocusedItem(); it != nil {
		st.gridKey, st.listKey = it.Key, it.Key
	}
	st.sortMode = (st.sortMode + 1) % pkgSortModes
	st.filterValid = false
	PlayToggleSound()
	pkgSavePrefs(config)
}

// pkgSelectChip filters the grid and list by a chip and starts at the top.
func pkgSelectChip(id string) {
	st := &pkgUI
	if id == st.chip {
		return
	}
	st.chip = id
	st.gridIndex, st.listIndex = 0, 0
	st.gridKey, st.listKey = "", ""
	st.gridScroll.jump(0)
	st.listScroll.jump(0)
	st.filterValid = false
	PlayToggleSound()
}

// pkgCycleChip steps the filter chips (Tab on a keyboard).
func pkgCycleChip(dir int) {
	st := &pkgUI
	if st.model == nil || len(st.model.Chips) == 0 {
		return
	}
	cur := 0
	for i, c := range st.model.Chips {
		if c.ID == st.chip {
			cur = i
		}
	}
	n := len(st.model.Chips)
	pkgSelectChip(st.model.Chips[(cur+dir+n)%n].ID)
}

// pkgRowMoved is a move to another row of the rows view.
func pkgRowMoved() {
	pkgNoteRowFocus()
	pkgUI.reveal = true
	PlayNavSound()
}

// pkgHeroStep shows the previous or next featured package.
func pkgHeroStep(dir int) {
	st := &pkgUI
	mode, _ := pkgHeroFor(st.model)
	if mode != pkgHeroFeatured || len(st.model.Featured) < 2 {
		return
	}
	n := len(st.model.Featured)
	st.heroIndex = ((st.heroIndex+dir)%n + n) % n
	st.heroSince = sdl.GetTicks64()
	PlayNavSound()
}

// pkgHeroActivate presses one of the hero's buttons.
func pkgHeroActivate(config *Config, btn int) {
	st := &pkgUI
	m := st.model
	mode, it := pkgHeroFor(m)
	if it == nil {
		return
	}
	if mode == pkgHeroUpdates {
		if btn == 0 {
			pkgUpdateAll(config)
			return
		}
		for i := range m.Shelves {
			if m.Shelves[i].ID == "updates" {
				st.rowFocus = i + 1
				pkgRowMoved()
				return
			}
		}
		return
	}
	if btn == 1 {
		pkgOpenSheet(it)
		return
	}
	pkgPrimaryAction(config, it)
}

// pkgPrimaryAction is the one-press action of a package: get it, update it, or
// open its folder; while it is busy, the sheet that manages it.
func pkgPrimaryAction(config *Config, it *pkgItem) {
	if it != nil && pkgBusyKey(it.Key) {
		// Rolling back or being removed: the sheet says so, and nothing else
		// may start on its folders meanwhile.
		pkgOpenSheet(it)
		return
	}
	status, _ := pkgStatusOf(it, pkgLiveNow())
	switch status {
	case pkgStatusInstalled:
		pkgOpenFolder(config, it)
	case pkgStatusUpdate, pkgStatusGet:
		pkgStartInstall(config, it, false)
	default:
		pkgOpenSheet(it)
	}
}

// pkgRowsDo is an action in the rows view.
func pkgRowsDo(config *Config, act pkgAct) {
	st := &pkgUI
	m := st.model
	rows := pkgRowCount(m)
	if rows == 0 {
		if act == pkgActUp {
			pkgStoresEnterFromContent()
		}
		return
	}
	st.rowFocus = clampIndex(st.rowFocus, rows)
	shelf, _ := pkgShelfAtRow(m, st.rowFocus)
	moveRow := func(to int) {
		to = clampIndex(to, rows)
		if to != st.rowFocus {
			st.rowFocus = to
			pkgRowMoved()
		}
	}
	switch act {
	case pkgActUp:
		if st.rowFocus == 0 {
			// Above the hero (or the first shelf) is the "More stores" strip.
			pkgStoresEnterFromContent()
			return
		}
		moveRow(st.rowFocus - 1)
	case pkgActDown:
		moveRow(st.rowFocus + 1)
	case pkgActPageUp:
		moveRow(st.rowFocus - 2)
	case pkgActPageDown:
		moveRow(st.rowFocus + 2)
	case pkgActFirst:
		moveRow(0)
	case pkgActLast:
		moveRow(rows - 1)
	case pkgActLeft, pkgActRight:
		dir := 1
		if act == pkgActLeft {
			dir = -1
		}
		if shelf == nil {
			next := st.heroBtn + dir
			if next < 0 || next > 1 {
				pkgHeroStep(dir)
				return
			}
			st.heroBtn = next
			PlayNavSound()
			return
		}
		col := clampIndex(st.rowCols[shelf.ID], len(shelf.Items))
		next := col + dir
		if next < 0 || next >= len(shelf.Items) {
			return
		}
		st.rowCols[shelf.ID] = next
		st.reveal = true
		PlayNavSound()
	case pkgActConfirm:
		if shelf == nil {
			pkgHeroActivate(config, st.heroBtn)
			return
		}
		if len(shelf.Items) > 0 {
			pkgOpenSheet(shelf.Items[clampIndex(st.rowCols[shelf.ID], len(shelf.Items))])
		}
	case pkgActNextChip, pkgActPrevChip, pkgActPrevItem, pkgActNextItem:
	}
}

// pkgGridDo is an action in the grid or the list.
func pkgGridDo(config *Config, act pkgAct) {
	st := &pkgUI
	pkgEnsureFiltered()
	m := st.model
	if m == nil {
		return
	}
	switch act {
	case pkgActNextChip:
		pkgCycleChip(1)
		return
	case pkgActPrevChip:
		pkgCycleChip(-1)
		return
	}
	if st.chipFocus {
		chips := len(m.Chips) + 1
		switch act {
		case pkgActLeft:
			if st.chipCursor > 0 {
				st.chipCursor--
				PlayNavSound()
			}
		case pkgActRight:
			if st.chipCursor < chips-1 {
				st.chipCursor++
				PlayNavSound()
			}
		case pkgActDown:
			if len(st.filtered) > 0 {
				st.chipFocus = false
				st.reveal = true
				PlayNavSound()
			}
		case pkgActUp:
			// Above the chips is the "More stores" strip.
			pkgStoresEnterFromContent()
		case pkgActConfirm:
			if st.chipCursor >= len(m.Chips) {
				pkgCycleSort(config)
			} else {
				pkgSelectChip(m.Chips[st.chipCursor].ID)
			}
		}
		return
	}

	n := len(st.filtered)
	cols := 1
	page := 5
	idx := &st.listIndex
	key := &st.listKey
	if st.view == pkgViewGrid {
		cols = st.gridCols
		if cols < 1 {
			cols = 1
		}
		page = cols * 3
		idx = &st.gridIndex
		key = &st.gridKey
	}
	*idx = clampIndex(*idx, n)
	move := func(to int) {
		to = clampIndex(to, n)
		if to != *idx {
			*idx = to
			st.reveal = true
			PlayNavSound()
		}
	}
	switch act {
	case pkgActUp:
		if *idx-cols >= 0 {
			move(*idx - cols)
		} else {
			st.chipFocus = true
			st.chipCursor = 0
			for i, c := range m.Chips {
				if c.ID == st.chip {
					st.chipCursor = i
				}
			}
			PlayNavSound()
		}
	case pkgActDown:
		switch {
		case *idx+cols < n:
			move(*idx + cols)
		case *idx/cols < (n-1)/cols:
			move(n - 1)
		}
	case pkgActLeft:
		if cols > 1 && *idx%cols > 0 {
			move(*idx - 1)
		}
	case pkgActRight:
		if cols > 1 && *idx%cols < cols-1 && *idx+1 < n {
			move(*idx + 1)
		}
	case pkgActPageUp:
		move(*idx - page)
	case pkgActPageDown:
		move(*idx + page)
	case pkgActFirst:
		move(0)
	case pkgActLast:
		move(n - 1)
	case pkgActConfirm:
		if n > 0 {
			pkgOpenSheet(st.filtered[*idx])
		}
	}
	if n > 0 {
		*key = st.filtered[clampIndex(*idx, n)].Key
	}
}

// ── Details sheet ────────────────────────────────────────────────────────

// pkgOpenSheet opens the details sheet on a package.
func pkgOpenSheet(it *pkgItem) {
	if it == nil {
		return
	}
	pkgUI.sheet = pkgSheet{open: true, key: it.Key, openedAt: sdl.GetTicks64()}
	PlayActivateSound()
}

// pkgCloseSheet closes the details sheet.
func pkgCloseSheet(sound bool) {
	pkgUI.sheet = pkgSheet{}
	if sound {
		PlayBackSound()
	}
}

// pkgSheetDo is an action while the details sheet is open: the buttons on the
// left take Up/Down and A, Right moves into the text (Up/Down then scroll it),
// L1/R1 step to the previous or next package without closing.
func pkgSheetDo(config *Config, act pkgAct) {
	st := &pkgUI
	it := st.model.ByKey[st.sheet.key]
	if it == nil {
		pkgCloseSheet(false)
		return
	}
	btns := pkgSheetButtons(it, pkgLiveNow())
	// Three body lines a press: the sheet's text runs at about 20px a line.
	step := float64(64)
	prevBtn := st.sheet.btn
	switch act {
	case pkgActBack:
		pkgCloseSheet(true)
		return
	case pkgActRefresh:
		pkgRequestRefresh(config)
	case pkgActUp, pkgActDown:
		if st.sheet.textFocus {
			if act == pkgActUp {
				st.sheet.scroll.target -= step
			} else {
				st.sheet.scroll.target += step
			}
			return
		}
		dir := 1
		if act == pkgActUp {
			dir = -1
		}
		for i := st.sheet.btn + dir; i >= 0 && i < len(btns); i += dir {
			if !btns[i].Disabled {
				st.sheet.btn = i
				PlayNavSound()
				break
			}
		}
	case pkgActRight:
		if !st.sheet.textFocus && st.sheet.contentH > st.sheet.viewH {
			st.sheet.textFocus = true
			PlayNavSound()
		}
	case pkgActLeft:
		if st.sheet.textFocus {
			st.sheet.textFocus = false
			PlayNavSound()
		}
	case pkgActPageUp:
		st.sheet.scroll.target -= float64(st.sheet.viewH) * 0.8
	case pkgActPageDown:
		st.sheet.scroll.target += float64(st.sheet.viewH) * 0.8
	case pkgActFirst:
		st.sheet.scroll.target = 0
	case pkgActLast:
		st.sheet.scroll.target = float64(st.sheet.contentH)
	case pkgActPrevItem:
		pkgSheetStep(-1)
	case pkgActNextItem:
		pkgSheetStep(1)
	case pkgActConfirm:
		if st.sheet.textFocus {
			st.sheet.textFocus = false
			PlayNavSound()
			return
		}
		st.sheet.btn = pkgSheetClampButton(btns, st.sheet.btn)
		if st.sheet.btn < len(btns) {
			pkgSheetRun(config, it, btns[st.sheet.btn])
		}
		return
	}
	if st.sheet.btn != prevBtn {
		st.sheet.armed = ""
	}
}

// pkgSheetRun presses one sheet button.
func pkgSheetRun(config *Config, it *pkgItem, b pkgSheetBtn) {
	st := &pkgUI
	if b.Disabled {
		PlayErrorSound()
		if !st.online {
			showToast(offlinePlainNotice, ToastWarn())
		}
		return
	}
	now := sdl.GetTicks64()
	switch b.ID {
	case "get", "update":
		pkgStartInstall(config, it, false)
	case "reinstall":
		pkgStartInstall(config, it, true)
	case "open":
		pkgOpenFolder(config, it)
	case "rollback":
		pkgStartRollback(it)
	case "remove":
		if st.sheet.armed == "remove" && now-st.sheet.armedAt < 4000 {
			st.sheet.armed = ""
			PlayActivateSound()
			pkgStartRemove(it)
			return
		}
		st.sheet.armed = "remove"
		st.sheet.armedAt = now
		PlayNavSound()
	case "source":
		pkgOpenSource(config, it)
	case "cancel":
		if installBarCancelActive() {
			PlayBackSound()
			showToast("Install cancelled", ToastInfo())
		}
	case "dequeue":
		if pkgDequeue(it.Pkg.Name) {
			PlayBackSound()
			showToast("Removed from the queue", ToastInfo())
		}
	}
}

// pkgSheetContext is the list the sheet steps through: the focused shelf, the
// featured packages behind the hero, or the grid/list content.
func pkgSheetContext() []*pkgItem {
	st := &pkgUI
	m := st.model
	if m == nil {
		return nil
	}
	if st.view == pkgViewRows {
		shelf, _ := pkgShelfAtRow(m, st.rowFocus)
		if shelf == nil {
			return m.Featured
		}
		return shelf.Items
	}
	pkgEnsureFiltered()
	return st.filtered
}

// pkgSheetStep moves the sheet to the previous or next package, and the focus
// behind it with it, so closing the sheet lands on the package it showed.
func pkgSheetStep(dir int) {
	st := &pkgUI
	items := pkgSheetContext()
	cur := -1
	for i, it := range items {
		if it.Key == st.sheet.key {
			cur = i
			break
		}
	}
	next := cur + dir
	if cur < 0 || next < 0 || next >= len(items) {
		return
	}
	it := items[next]
	switch st.view {
	case pkgViewRows:
		if shelf, _ := pkgShelfAtRow(st.model, st.rowFocus); shelf != nil {
			st.rowCols[shelf.ID] = next
		} else {
			st.heroIndex = next
			st.heroSince = sdl.GetTicks64()
		}
	case pkgViewGrid:
		st.gridIndex, st.gridKey = next, it.Key
	default:
		st.listIndex, st.listKey = next, it.Key
	}
	st.reveal = true
	opened := st.sheet.openedAt
	st.sheet = pkgSheet{open: true, key: it.Key, openedAt: opened}
	PlayNavSound()
}

// ── Footer ───────────────────────────────────────────────────────────────

// pkgFooterHints is the footer for the Packages scene (extras.go asks). The
// three hints the planner keeps when space runs out come first.
func pkgFooterHints() []FooterHint {
	st := &pkgUI
	if st.sheet.open {
		label := "Select"
		if st.sheet.textFocus {
			label = "Buttons"
		} else if st.model != nil {
			if it := st.model.ByKey[st.sheet.key]; it != nil {
				btns := pkgSheetButtons(it, pkgLiveNow())
				if i := pkgSheetClampButton(btns, st.sheet.btn); i < len(btns) {
					label = btns[i].Label
				}
			}
		}
		return []FooterHint{
			{Button: "D-Pad", Label: "Choose"},
			{Button: "A", Label: label},
			{Button: "B", Label: "Close"},
			{Button: "L1/R1 / [ ]", Label: "Previous / next"},
		}
	}
	if pkgStoresFocused() {
		return pkgStoresFooterHints()
	}
	if st.chipFocus && st.view != pkgViewRows {
		hints := []FooterHint{
			{Button: "D-Pad", Label: "Choose"},
			{Button: "A", Label: "Select"},
			{Button: "B", Label: "Back"},
		}
		if pkgHubEnabled() {
			hints = append(hints, FooterHint{Button: "L2/R2", Label: "Stores"})
		}
		return append(hints, FooterHint{Button: "Y", Label: "View"})
	}
	hints := []FooterHint{
		{Button: "D-Pad", Label: "Navigate"},
		{Button: "A", Label: "Details"},
		{Button: "B", Label: "Back"},
	}
	if pkgHubEnabled() {
		hints = append(hints, FooterHint{Button: "L2/R2", Label: "Stores"})
	}
	return append(hints, FooterHint{Button: "X", Label: "Refresh"}, FooterHint{Button: "Y", Label: "View"})
}

// ── Devices ──────────────────────────────────────────────────────────────

// handlePackageInput is the packagelist's keyboard: arrows move, Enter/Space
// open, Escape/Backspace close the sheet or leave, R/F5 refresh, V switches
// the view (1/2/3 pick one), S changes the sort, Tab walks the filter chips
// and [ ] step through packages while the sheet is open.
func handlePackageInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil {
		return
	}
	shift := e.Keysym.Mod&sdl.KMOD_SHIFT != 0
	// Ctrl and Alt chords belong to the app (paste, quick settings, the
	// fullscreen toggle): a letter held with one is not a store shortcut.
	if e.Keysym.Mod&(sdl.KMOD_CTRL|sdl.KMOD_ALT) != 0 {
		return
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		pkgDo(config, pkgActUp)
	case sdl.K_DOWN:
		pkgDo(config, pkgActDown)
	case sdl.K_LEFT:
		pkgDo(config, pkgActLeft)
	case sdl.K_RIGHT:
		pkgDo(config, pkgActRight)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE, sdl.K_a:
		pkgDo(config, pkgActConfirm)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		pkgDo(config, pkgActBack)
	case sdl.K_r, sdl.K_F5, sdl.K_x:
		pkgDo(config, pkgActRefresh)
	case sdl.K_v, sdl.K_y:
		pkgDo(config, pkgActView)
	case sdl.K_s:
		pkgDo(config, pkgActSort)
	case sdl.K_PAGEUP:
		pkgDo(config, pkgActPageUp)
	case sdl.K_PAGEDOWN:
		pkgDo(config, pkgActPageDown)
	case sdl.K_HOME:
		pkgDo(config, pkgActFirst)
	case sdl.K_END:
		pkgDo(config, pkgActLast)
	case sdl.K_TAB:
		if shift {
			pkgDo(config, pkgActPrevChip)
		} else {
			pkgDo(config, pkgActNextChip)
		}
	case sdl.K_LEFTBRACKET:
		pkgDo(config, pkgActPrevItem)
	case sdl.K_RIGHTBRACKET:
		pkgDo(config, pkgActNextItem)
	case sdl.K_1:
		if !pkgUI.sheet.open {
			pkgSetView(config, pkgViewRows)
		}
	case sdl.K_2:
		if !pkgUI.sheet.open {
			pkgSetView(config, pkgViewGrid)
		}
	case sdl.K_3:
		if !pkgUI.sheet.open {
			pkgSetView(config, pkgViewList)
		}
	}
}

// packageListController is the packagelist's pad. L1/R1 page the view, or
// step through packages while the sheet is open.
func packageListController(e *sdl.ControllerButtonEvent, config *Config) {
	if e == nil {
		return
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		pkgDo(config, pkgActUp)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		pkgDo(config, pkgActDown)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		pkgDo(config, pkgActLeft)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		pkgDo(config, pkgActRight)
	case sdl.CONTROLLER_BUTTON_A:
		pkgDo(config, pkgActConfirm)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		pkgDo(config, pkgActBack)
	case sdl.CONTROLLER_BUTTON_X:
		pkgDo(config, pkgActRefresh)
	case sdl.CONTROLLER_BUTTON_Y:
		pkgDo(config, pkgActView)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		if pkgUI.sheet.open {
			pkgDo(config, pkgActPrevItem)
		} else {
			pkgDo(config, pkgActPageUp)
		}
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		if pkgUI.sheet.open {
			pkgDo(config, pkgActNextItem)
		} else {
			pkgDo(config, pkgActPageDown)
		}
	}
}

// pkgShoulder is the triggers' paging (shoulders.go): L2/R2 jump to the first
// and last row on devices without the Smart Pro store loop, or scroll the
// sheet's text to its ends.
func pkgShoulder(s padShoulder) bool {
	switch s {
	case padL1:
		pkgDo(appConfig, pkgActPageUp)
	case padR1:
		pkgDo(appConfig, pkgActPageDown)
	case padL2:
		pkgDo(appConfig, pkgActFirst)
	case padR2:
		pkgDo(appConfig, pkgActLast)
	}
	return true
}

// pkgElementIndex is the packagelist's index in the current scene, or -1.
func pkgElementIndex(config *Config) int {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return -1
	}
	for i, e := range config.Scenes[currentSceneIndex].Elements {
		if e.Type == "packagelist" {
			return i
		}
	}
	return -1
}

// pkgHitAt is the topmost target the last frame drew under a point.
func pkgHitAt(mx, my int32) (pkgHit, bool) {
	hits := pkgUI.hits
	for i := len(hits) - 1; i >= 0; i-- {
		if pointInRect(mx, my, hits[i].Rect) {
			return hits[i], true
		}
	}
	return pkgHit{}, false
}

// pkgAbs32 is |v|.
func pkgAbs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// handlePackageMouseClick is a button-down on the store (main.go calls it for
// every click while the scene is up; a click outside the element is ignored).
func handlePackageMouseClick(mx, my int32, config *Config) {
	st := &pkgUI
	hit, ok := pkgHitAt(mx, my)
	if !ok {
		return
	}
	// The click lands on the store: the pad focus leaves the header with it.
	if idx := pkgElementIndex(config); idx >= 0 {
		selectedButtonIndex = idx
		focusEngine.SetByElementIndex(idx)
	}
	if st.sheet.open {
		pkgSheetClick(config, hit, mx, my)
		return
	}
	// A pill of the "More stores" strip opens at once; a press on the content
	// takes the pad's focus off the strip, so the ring follows the pointer.
	switch hit.Kind {
	case pkgHitStore:
		pkgStoresClick(config, hit.Index)
		return
	case pkgHitCard, pkgHitChip, pkgHitSort, pkgHitHeroPrimary, pkgHitHeroDetails, pkgHitHeroDot, pkgHitShelfArrow:
		pkgStores.focus = false
	case pkgHitBody:
		// Blank content counts too (a gap between cards, the hero's
		// background, the empty state's text); the band of the toolbar and
		// the strip above it does not.
		if pointInRect(mx, my, st.contentClip) {
			pkgStores.focus = false
		}
	}
	m := st.model
	switch hit.Kind {
	case pkgHitView:
		pkgSetView(config, pkgViewMode(hit.Index))
	case pkgHitRefresh:
		pkgRequestRefresh(config)
	case pkgHitChip:
		if m != nil && hit.Index < len(m.Chips) {
			st.chipFocus = false
			pkgSelectChip(m.Chips[hit.Index].ID)
		}
	case pkgHitSort:
		pkgCycleSort(config)
	case pkgHitHeroPrimary, pkgHitHeroDetails:
		btn := 0
		if hit.Kind == pkgHitHeroDetails {
			btn = 1
		}
		st.rowFocus, st.heroBtn = 0, btn
		pkgNoteRowFocus()
		pkgHeroActivate(config, btn)
	case pkgHitHeroDot:
		if m != nil && hit.Index < len(m.Featured) {
			st.heroIndex = hit.Index
			st.heroSince = sdl.GetTicks64()
			PlayNavSound()
		}
	case pkgHitShelfArrow:
		if shelf, _ := pkgShelfAtRow(m, hit.Row); shelf != nil {
			page := float64(st.viewW) * 0.8
			if page < float64(pkgCardW) {
				page = float64(pkgCardW)
			}
			pkgShelfScroll(shelf.ID).target += float64(hit.Index) * page
			PlayNavSound()
		}
	case pkgHitCard:
		// Focus follows the press; opening waits for the release, so a swipe
		// that starts on a card scrolls instead of opening it.
		switch st.view {
		case pkgViewRows:
			if shelf, _ := pkgShelfAtRow(m, hit.Row); shelf != nil {
				st.rowFocus = hit.Row
				st.rowCols[shelf.ID] = hit.Index
				pkgNoteRowFocus()
			}
		case pkgViewGrid:
			st.chipFocus = false
			st.gridIndex, st.gridKey = hit.Index, hit.Key
		default:
			st.chipFocus = false
			st.listIndex, st.listKey = hit.Index, hit.Key
		}
		st.press = pkgPress{active: true, hit: hit, x: mx, y: my}
	case pkgHitBody:
		st.press = pkgPress{active: true, hit: hit, x: mx, y: my}
	}
}

// pkgSheetClick is a button-down while the sheet is open: its buttons and its
// close button act, its text takes the focus (and can be dragged), and a
// click on the dimmed screen around it closes it.
func pkgSheetClick(config *Config, hit pkgHit, mx, my int32) {
	st := &pkgUI
	switch hit.Kind {
	case pkgHitSheetButton:
		it := st.model.ByKey[st.sheet.key]
		if it == nil {
			pkgCloseSheet(false)
			return
		}
		btns := pkgSheetButtons(it, pkgLiveNow())
		if hit.Index < len(btns) {
			if st.sheet.btn != hit.Index {
				st.sheet.armed = ""
			}
			st.sheet.btn = hit.Index
			st.sheet.textFocus = false
			pkgSheetRun(config, it, btns[hit.Index])
		}
	case pkgHitSheetClose:
		pkgCloseSheet(true)
	case pkgHitSheetText:
		st.sheet.textFocus = st.sheet.contentH > st.sheet.viewH
		st.press = pkgPress{active: true, hit: hit, x: mx, y: my}
	case pkgHitSheet:
	default:
		pkgCloseSheet(true)
	}
}

// pkgPointerMotion follows a held press: past a few pixels it becomes a drag
// that scrolls what it started on — a shelf sideways, the page, the grid, the
// list or the sheet's text — with the content under the finger.
func pkgPointerMotion(config *Config, mx, my int32, leftDown bool) {
	_ = config
	st := &pkgUI
	p := &st.press
	if !p.active {
		return
	}
	if !leftDown {
		st.press = pkgPress{}
		return
	}
	dx, dy := mx-p.x, my-p.y
	if !p.dragging {
		if pkgAbs32(dx) < 12 && pkgAbs32(dy) < 12 {
			return
		}
		// The drag starts here: the axis is decided once, and the content
		// follows the finger from this point on rather than jumping by the
		// threshold.
		p.dragging = true
		horizontal := pkgAbs32(dx) > pkgAbs32(dy)
		p.x, p.y = mx, my
		dx, dy = 0, 0
		switch {
		case st.sheet.open:
			p.start = st.sheet.scroll.cur
		case st.view == pkgViewRows && p.hit.Kind == pkgHitCard && horizontal:
			p.axisX = true
			if shelf, _ := pkgShelfAtRow(st.model, p.hit.Row); shelf != nil {
				p.shelfID = shelf.ID
				p.start = pkgShelfScroll(shelf.ID).cur
			}
		case st.view == pkgViewRows:
			p.start = st.pageScroll.cur
		case st.view == pkgViewGrid:
			p.start = st.gridScroll.cur
		default:
			p.start = st.listScroll.cur
		}
	}
	switch {
	case st.sheet.open:
		st.sheet.scroll.jump(p.start - float64(dy))
	case p.axisX:
		if p.shelfID != "" {
			pkgShelfScroll(p.shelfID).jump(p.start - float64(dx))
		}
	case st.view == pkgViewRows:
		st.pageScroll.jump(p.start - float64(dy))
	case st.view == pkgViewGrid:
		st.gridScroll.jump(p.start - float64(dy))
	default:
		st.listScroll.jump(p.start - float64(dy))
	}
}

// pkgPointerUp ends a press: a press that never became a drag and is released
// on the card it started on opens that card.
func pkgPointerUp(config *Config, mx, my int32) {
	_ = config
	st := &pkgUI
	p := st.press
	st.press = pkgPress{}
	if !p.active || p.dragging || p.hit.Kind != pkgHitCard || st.sheet.open {
		return
	}
	if !pointInRect(mx, my, p.hit.Rect) || st.model == nil {
		return
	}
	if it := st.model.ByKey[p.hit.Key]; it != nil {
		st.reveal = true
		pkgOpenSheet(it)
	}
}

// pkgHandleWheel scrolls the store under the pointer and reports whether the
// wheel was over the element. A horizontal wheel scrolls the shelf under the
// pointer; the vertical wheel scrolls the page, the grid, the list or the
// sheet's text.
func pkgHandleWheel(config *Config, elem Element, mx, my, wx, wy int32) bool {
	_ = config
	st := &pkgUI
	body := sdl.Rect{X: elem.X, Y: elem.Y, W: getElementWidth(elem, 1232), H: getElementHeight(elem, 576)}
	if !pointInRect(mx, my, body) {
		return false
	}
	step := float64(72)
	if st.sheet.open {
		st.sheet.scroll.target -= float64(wy) * step
		return true
	}
	if wx != 0 && st.view == pkgViewRows {
		if hit, ok := pkgHitAt(mx, my); ok && (hit.Kind == pkgHitCard || hit.Kind == pkgHitShelfArrow) {
			if shelf, _ := pkgShelfAtRow(st.model, hit.Row); shelf != nil {
				pkgShelfScroll(shelf.ID).target += float64(wx) * step
			}
		}
		return true
	}
	switch st.view {
	case pkgViewRows:
		st.pageScroll.target -= float64(wy) * step
	case pkgViewGrid:
		st.gridScroll.target -= float64(wy) * step
	default:
		st.listScroll.target -= float64(wy) * step
	}
	return true
}
