package main

// home_edit.go — Edit Home: arranging the Carousel and Widgets layouts, and
// switching between Home's layouts.
//
// L2 on Home in any layout (Q on a keyboard), X on a Carousel or Widgets
// tile, the footer's Edit pill, a right-click on a tile, Start → Device →
// Edit Home or Settings → Display & theme → Edit Home open it. Standard and
// the Hero grid keep their cards in place, so editing from them switches Home
// to Widgets — and back again if editing ends with nothing changed, so a
// stray L2 costs nothing. While it is open:
//
//   - a strip replaces the greeting and the search pill: the four layouts
//     (Standard, Hero grid, Carousel, Widgets), Reset and Done. L1/R1 (Tab and
//     Shift+Tab) switch the layout from anywhere; Up from the top tiles reaches
//     the strip;
//   - A picks the focused tile up and the D-pad carries it (Left/Right one
//     place earlier or later, Up/Down to the place of the tile above or
//     below); A or B puts it down;
//   - X resizes a widget-grid tile: the D-pad grows it (Right, Down) and
//     shrinks it (Left, Up) a square cell at a time, from one cell to the
//     grid's width and three rows (home_tiles.go homeTileLimits); A, B or X
//     ends resizing. With the pointer, the handle on the focused tile's
//     corner drags its size and a click on its size chip steps through the
//     usual sizes;
//   - Y (Delete, Backspace) removes a tile; the "+" tile at the end opens the
//     add sheet (home_addsheet.go), which inserts after the tile that had the
//     focus;
//   - Up from the strip, or its Top bar chip, opens the Top bar row: the
//     controls on the right of the top bar, to put in any order and to hide
//     or show (home_edit_header.go);
//   - B (Esc) or L2 (Q) ends editing. So does opening the Start menu, the
//     notification list or search, and leaving Home.
//
// The list being edited is a copy (homeEdit.work); every finished edit — a
// tile put down, resized, removed or added, a reset, a layout switch — saves
// it through homeTilesSet, so nothing is lost if the app quits mid-edit, but
// carrying a tile across the grid or growing it a cell at a time is not a
// save per step. On Standard and the Hero grid the strip is all there is:
// those layouts keep their cards where they are, and the body under the
// strip says so.
//
// Every control has a pointer twin: a click focuses a tile, the badges on it
// remove or resize it, dragging carries it (the grid makes room as it goes),
// dragging its corner handle sizes it, and the strip's chips are buttons.

import (
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// The strip's chips after the four layouts: Top bar (the row that
	// arranges the top bar, home_edit_header.go), Reset and Done.
	homeEditStripBar   = 4
	homeEditStripReset = 5
	homeEditStripDone  = 6
	homeEditStripCount = 7

	// homeEditResetArmMS is how long Reset waits for its second press.
	homeEditResetArmMS = 3000
	// homeEditDragSlop is how far (design px) a press may move before it
	// becomes a drag.
	homeEditDragSlop = 12
	// homeEditPendingMS bounds how long an edit request made on another
	// screen waits for Home to come up.
	homeEditPendingMS = 5000
	// homeEditDragScrollMS paces the grid's scroll under a tile carried to
	// the body's edge.
	homeEditDragScrollMS = 350
)

// homeEdit is edit mode's state. Render/event thread only.
var homeEdit struct {
	open      bool
	pending   bool
	pendingAt uint64

	// work is the list being edited and gen its generation (the body
	// rebuilds when it moves); dirty says it has changes not yet saved.
	work  []homeTile
	gen   uint64
	dirty bool

	// holding: the focused tile is picked up.
	holding bool
	// resizing: the D-pad grows and shrinks the focused tile (X).
	resizing bool
	// restore is the layout editing switched Home away from (Standard or the
	// Hero grid). It comes back when editing ends with nothing changed; any
	// saved change, or a layout picked on the strip, clears it.
	restore string
	// strip: the strip has the focus, on chip stripIdx.
	strip    bool
	stripIdx int
	// resetAt is when Reset was armed (0 = not armed).
	resetAt uint64
	// chips are the strip's buttons as last drawn, for the pointer.
	chips [homeEditStripCount]sdl.Rect

	sheet homeAddSheetState

	// press is a pointer press on a tile that may turn into a drag. lastSwap
	// is the tile a drag last traded places with: it is not traded with again
	// until the pointer has left it, or a tile wider than the carried one
	// would swap back and forth under a still pointer. A press on the corner
	// handle (resize) sizes the tile instead: its size follows the pointer
	// from the tile's top-left corner where the press found it (anchorX,
	// anchorY), so a tile the packing moves to another row keeps its size;
	// resized says the press changed the size at least once.
	press struct {
		active, drag     bool
		resize, resized  bool
		id               HomeFocusID
		x, y             int32
		offX, offY       int32
		anchorX, anchorY int32
		lastSwap         string
	}
	mouseX, mouseY int32
	// scrollAt is when a drag last scrolled the grid at its edge.
	scrollAt uint64
}

// homeOwnsPointer reports whether Home itself is in front: no menu, popover,
// sheet, keyboard or panel is open over it. The wheel and a right click act on
// the tiles only then; otherwise they belong to what is on top.
func homeOwnsPointer() bool {
	if !homeLayoutActive || homeLayout == nil {
		return false
	}
	if tb.startOpen || nc.open || globalSearchOpen || weatherPopoverOpen || shortcutsOpen || clipSearchOpen ||
		virtualKeyboardActive || wpOpen || hoffCardVisible() {
		return false
	}
	return !triggerOwnedByOverlay() && !modalPanelBlocksShoulders()
}

// homeEditActive reports whether Home is being edited.
func homeEditActive() bool { return homeEdit.open }

// homeEditHomeScene is the index of the config's home-layout scene, or -1.
func homeEditHomeScene(config *Config) int {
	if config == nil {
		return -1
	}
	for i, sc := range config.Scenes {
		if sc.Layout == "home" {
			return i
		}
	}
	return -1
}

// homeEditOpen opens edit mode from anywhere: on another screen it goes to
// Home first and opens on arrival; on Standard or the Hero grid it switches
// Home to Widgets, the layout that edits everything.
func homeEditOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil || homeEdit.open {
		return
	}
	if !homeLayoutActive || homeLayout == nil {
		idx := homeEditHomeScene(config)
		if idx < 0 {
			ShowToast("Home is not part of this setup", ToastKindInfo)
			return
		}
		homeEdit.pending, homeEdit.pendingAt = true, sdl.GetTicks64()
		if idx != currentSceneIndex {
			changeSceneTo(config, idx)
		}
		return
	}
	homeEditBegin(config)
}

// homeEditPendingCheck opens a requested edit mode once Home is on screen.
// refreshHomeLayout calls it every frame Home is shown.
func homeEditPendingCheck(config *Config) {
	if !homeEdit.pending {
		return
	}
	if sdl.GetTicks64()-homeEdit.pendingAt > homeEditPendingMS {
		homeEdit.pending = false
		return
	}
	if homeLayoutActive && homeLayout != nil {
		homeEditBegin(config)
	}
}

// homeEditBegin opens edit mode on the Home on screen.
func homeEditBegin(config *Config) {
	homeEdit.pending = false
	if homeLayout == nil || config == nil {
		return
	}
	// Standard and the Hero grid have nothing to arrange: Home shows Widgets
	// while editing, without saving it (homeArrangement answers Widgets while
	// restore is set). The first saved change keeps it (homeEditCommit);
	// otherwise the stored layout was never touched, so even quitting
	// mid-edit leaves it as it was.
	homeEdit.restore = ""
	if from := homeArrangement(config); !homeArrangementIsDynamic(from) {
		homeEdit.restore = from
		homeLayout.SetArrangement(homeArrangementWidgets)
		ShowToast("Home layout: Widgets — change something to keep it", ToastKindInfo)
	}
	homeCalendarOpen = false
	dismissWeatherPopover()
	dismissVolumePopover()
	dismissBrightnessPopover()
	if homePowerConfirm.open {
		homePowerClose()
	}
	homeEdit.work = append([]homeTile(nil), homeTilesCurrent(config)...)
	homeEdit.gen++
	homeEdit.dirty = false
	homeEdit.holding = false
	homeEdit.resizing = false
	homeEdit.strip = false
	homeEdit.resetAt = 0
	homeEdit.press.active, homeEdit.press.drag, homeEdit.press.resize = false, false, false
	homeEdit.sheet = homeAddSheetState{}
	homeEdit.mouseX, homeEdit.mouseY = mouseX, mouseY
	homeEditBar.open, homeEditBar.holding, homeEditBar.dirty = false, false, false
	homeEdit.open = true
	homeLayout.dynSync(config)
	if !homeLayout.dynIsTile(homeLayout.Focus.Current()) {
		homeEditFocusIndex(0)
	}
}

// homeEditClose ends edit mode, saving what is not saved yet.
func homeEditClose(config *Config) {
	if !homeEdit.open {
		return
	}
	if config == nil {
		config = appConfig
	}
	// The Top bar row saves what it has not saved yet.
	homeEditBarClose(config, false)
	homeEdit.holding = false
	homeEdit.resizing = false
	homeEdit.press.active, homeEdit.press.drag, homeEdit.press.resize = false, false, false
	homeEdit.sheet.open = false
	if homeEdit.dirty {
		homeEditCommit(config)
	}
	homeEdit.open = false
	homeEdit.strip = false
	homeEdit.resetAt = 0
	homeEdit.work = nil
	// Nothing was changed on the Widgets Home editing borrowed: the stored
	// layout, which editing never wrote, is shown again.
	if homeEdit.restore != "" {
		homeEdit.restore = ""
		back := homeArrangement(config)
		if homeLayout != nil {
			homeLayout.SetArrangement(back)
		}
		ShowToast("Home layout: "+homeArrangementChoiceLabel(back), ToastKindInfo)
	}
	if homeLayout == nil {
		return
	}
	onAdd := homeLayout.Focus.Current() == HomeFocusID(homeTileAddRef)
	homeLayout.dynSync(config)
	if onAdd {
		if n := len(homeLayout.dyn.tiles); n > 0 {
			homeEditFocusIndex(n - 1)
		}
	}
}

// homeEditCommit saves the list being edited.
func homeEditCommit(config *Config) {
	homeTilesSet(config, homeEdit.work)
	homeEdit.dirty = false
	// A real change keeps the Widgets Home it was made on: the layout editing
	// only borrowed is saved now.
	if homeEdit.restore != "" {
		homeEdit.restore = ""
		homeSetArrangement(config, homeArrangementWidgets)
	}
	trophyEvent(tkHomeEdit) // every caller commits a real change (after homeEditChanged or when dirty)
}

// homeEditEndForExit ends Edit Home on the way out of the app (shutdown.go):
// what is not saved yet is saved — which keeps a borrowed Widgets layout when
// something changed — and nothing is put back on a screen that is going away.
func homeEditEndForExit(config *Config) {
	if !homeEdit.open {
		return
	}
	if config == nil {
		config = appConfig
	}
	if homeEdit.dirty {
		homeEditCommit(config)
	}
	homeEdit.restore = ""
	homeEditClose(config)
}

// homeEditChanged records a change to the list being edited and lays the
// body out again at once, so the next press already sees the new places.
func homeEditChanged(config *Config) {
	homeEdit.gen++
	homeEdit.dirty = true
	if homeLayout != nil {
		homeLayout.dynSync(config)
	}
}

// homeEditFocusIndex focuses the body's tile i and brings it into view.
func homeEditFocusIndex(i int) {
	h := homeLayout
	if h == nil || !h.dynActive() || len(h.dyn.tiles) == 0 {
		return
	}
	if i < 0 {
		i = 0
	}
	if i >= len(h.dyn.tiles) {
		i = len(h.dyn.tiles) - 1
	}
	id := h.dyn.tiles[i].ID
	if h.Focus.Set(id) {
		h.Focus.Remember()
	}
	h.dynEnsureVisible(id)
}

// homeEditFocusRef focuses the tile with this ref.
func homeEditFocusRef(ref string) {
	if h := homeLayout; h != nil {
		if i := h.dynTileIndex(HomeFocusID(ref)); i >= 0 {
			homeEditFocusIndex(i)
		}
	}
}

// homeEditCurrent is the focused tile's ref and its index in the list being
// edited (-1 for the add tile or no tile).
func homeEditCurrent() (string, int) {
	if homeLayout == nil {
		return "", -1
	}
	ref := string(homeLayout.Focus.Current())
	return ref, homeTilesIndex(homeEdit.work, ref)
}

// homeEditLastTile is the last tile on the list, where the add tile inserts.
func homeEditLastTile() HomeFocusID {
	if n := len(homeEdit.work); n > 0 {
		return HomeFocusID(homeEdit.work[n-1].Ref)
	}
	return ""
}

// ── Edits ─────────────────────────────────────────────────────────────────

// homeEditMoveHeld carries the held tile one step.
func homeEditMoveHeld(config *Config, dir FocusDirection) {
	h := homeLayout
	ref, i := homeEditCurrent()
	if h == nil || i < 0 {
		homeEdit.holding = false
		return
	}
	n := len(homeEdit.work)
	j := i
	switch dir {
	case FocusLeft:
		j = i - 1
	case FocusRight:
		j = i + 1
	case FocusUp, FocusDown:
		if h.dynCarousel() {
			return
		}
		k := h.dynGridNeighbor(h.dynTileIndex(HomeFocusID(ref)), dir, -1)
		switch {
		case k >= 0 && h.dyn.tiles[k].Kind == homeTileAdd:
			j = n - 1
		case k >= 0:
			j = homeTilesIndex(homeEdit.work, h.dyn.tiles[k].Ref)
		case dir == FocusDown:
			j = n - 1
		}
	}
	if j < 0 {
		j = 0
	}
	if j > n-1 {
		j = n - 1
	}
	if j == i {
		return
	}
	homeEdit.work = homeTilesMove(homeEdit.work, i, j)
	homeEditChanged(config)
	if h.dynCarousel() {
		// The carried card stays in the middle; its neighbours change places.
		h.dyn.carPos = float64(h.dynTileIndex(HomeFocusID(ref)))
	}
	homeEditFocusRef(ref)
	PlayNavSound()
}

// homeTilesMove moves the entry at i to index j, in place.
func homeTilesMove(tiles []homeTile, i, j int) []homeTile {
	if i < 0 || i >= len(tiles) || j < 0 || j >= len(tiles) || i == j {
		return tiles
	}
	t := tiles[i]
	if i < j {
		copy(tiles[i:j], tiles[i+1:j+1])
	} else {
		copy(tiles[j+1:i+1], tiles[j:i])
	}
	tiles[j] = t
	return tiles
}

// homeEditResizable is the focused tile's ref and list index when it is a
// widget-grid tile that can change size; ok=false otherwise.
func homeEditResizable() (ref string, i int, ok bool) {
	h := homeLayout
	ref, i = homeEditCurrent()
	if h == nil || i < 0 || !h.dynActive() || h.dynCarousel() || !homeTileCanResize(ref) {
		return ref, i, false
	}
	return ref, i, true
}

// homeEditResizeToggle is X on a tile: it starts resize mode, where the D-pad
// grows and shrinks the tile, and X again ends it. A carried tile is put down
// first.
func homeEditResizeToggle(config *Config) {
	if homeEdit.resizing {
		homeEditResizeEnd(config)
		return
	}
	if _, _, ok := homeEditResizable(); !ok {
		return
	}
	if homeEdit.holding {
		homeEdit.holding = false
		if homeEdit.dirty {
			homeEditCommit(config)
		}
	}
	homeEdit.resizing = true
	PlayActivateSound()
}

// homeEditResizeEnd leaves resize mode and saves the size it reached.
func homeEditResizeEnd(config *Config) {
	if !homeEdit.resizing {
		return
	}
	homeEdit.resizing = false
	if homeEdit.dirty {
		homeEditCommit(config)
	}
	PlayActivateSound()
}

// homeEditResizeStep is a D-pad press in resize mode: Right and Down add a
// column or a row, Left and Up take one away, inside the tile's limits. The
// grid packs again at once (the other tiles make room or close up), and the
// focus stays with the tile wherever the packing puts it.
func homeEditResizeStep(config *Config, dir FocusDirection) {
	ref, i, ok := homeEditResizable()
	if !ok {
		homeEdit.resizing = false
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
	cur := homeTileSnapSize(ref, homeEdit.work[i].Size)
	next := homeTileResized(ref, cur, dw, dh)
	if next == cur {
		// Already as small (or as large) as the tile goes that way.
		PlayBackSound()
		return
	}
	homeEdit.work[i].Size = next
	homeEditChanged(config)
	homeEditFocusRef(ref)
	PlayNavSound()
}

// homeEditResizeCycle steps the focused tile to the next of the usual sizes
// and saves: a click on its size chip (home_tiles.go homeTileNextSize).
func homeEditResizeCycle(config *Config) {
	ref, i, ok := homeEditResizable()
	if !ok {
		return
	}
	homeEdit.work[i].Size = homeTileNextSize(ref, homeEdit.work[i].Size)
	homeEditChanged(config)
	homeEditCommit(config)
	homeEditFocusRef(ref)
	PlayNavSound()
}

// homeEditResizeDragTo sizes the tile whose corner handle the pointer holds:
// the pointer (less where on the handle it was grabbed) is the tile's
// bottom-right corner, measured in whole cells from the corner the tile had
// when the press began.
func homeEditResizeDragTo(config *Config, mx, my int32) {
	h := homeLayout
	if h == nil || !h.dynActive() || h.dynCarousel() {
		return
	}
	i := homeTilesIndex(homeEdit.work, string(homeEdit.press.id))
	if i < 0 {
		return
	}
	ref := homeEdit.work[i].Ref
	colPitch := float64(scaleX(int32(math.Round(homeDynColX(1)-homeDynColX(0))), h.Width))
	rowPitch := float64(scaleY(homeDynPitch, h.Height))
	gapX := float64(scaleX(homeDynGap, h.Width))
	gapY := float64(scaleY(homeDynGap, h.Height))
	if colPitch <= 0 || rowPitch <= 0 {
		return
	}
	w := float64(mx+homeEdit.press.offX-homeEdit.press.anchorX) + gapX
	ht := float64(my+homeEdit.press.offY-homeEdit.press.anchorY) + gapY
	next := homeTileSnapSize(ref, homeTileSize{W: int(math.Round(w / colPitch)), H: int(math.Round(ht / rowPitch))})
	if next == homeTileSnapSize(ref, homeEdit.work[i].Size) {
		return
	}
	homeEdit.work[i].Size = next
	homeEdit.press.resized = true
	homeEditChanged(config)
	if h.Focus.Set(homeEdit.press.id) {
		h.Focus.Remember()
	}
	PlayNavSound()
}

// homeEditRemove takes the focused tile off Home; the focus goes to the tile
// that takes its place.
func homeEditRemove(config *Config) {
	_, i := homeEditCurrent()
	if homeLayout == nil || i < 0 {
		return
	}
	homeEdit.holding = false
	homeEdit.resizing = false
	homeEdit.work = append(homeEdit.work[:i:i], homeEdit.work[i+1:]...)
	homeEditChanged(config)
	homeEditCommit(config)
	homeEditFocusIndex(i)
	PlayBackSound()
}

// homeEditInsert puts ref on Home after the tile after (at the end when
// after is not a tile), saves, and focuses it.
func homeEditInsert(config *Config, ref string, after HomeFocusID) {
	if ref == "" || homeTilesPresent(homeEdit.work)[strings.ToLower(ref)] {
		return
	}
	if len(homeEdit.work) >= homeTilesMax {
		ShowToast("Home is full: remove a tile first", ToastKindInfo)
		return
	}
	idx := len(homeEdit.work)
	if a := homeTilesIndex(homeEdit.work, string(after)); a >= 0 {
		idx = a + 1
	}
	t := homeTile{Ref: ref, Size: homeTileDefaultSize(ref)}
	work := make([]homeTile, 0, len(homeEdit.work)+1)
	work = append(work, homeEdit.work[:idx]...)
	work = append(work, t)
	work = append(work, homeEdit.work[idx:]...)
	homeEdit.work = work
	homeEdit.strip = false
	homeEditChanged(config)
	homeEditCommit(config)
	homeEditFocusRef(ref)
}

// homeEditReset puts the default tiles back. The first press arms it; a
// second within homeEditResetArmMS resets.
func homeEditReset(config *Config) {
	now := sdl.GetTicks64()
	if homeEdit.resetAt == 0 || now-homeEdit.resetAt > homeEditResetArmMS {
		homeEdit.resetAt = now
		PlayNavSound()
		return
	}
	homeEdit.resetAt = 0
	homeEdit.holding = false
	homeEdit.resizing = false
	homeEdit.work = homeDefaultTiles()
	homeEditChanged(config)
	homeEditCommit(config)
	homeEditFocusIndex(0)
	ShowToast("Home tiles reset", ToastKindInfo)
	PlayActivateSound()
}

// homeEditResetArmed reports whether Reset is waiting for its second press.
func homeEditResetArmed() bool {
	return homeEdit.resetAt != 0 && sdl.GetTicks64()-homeEdit.resetAt <= homeEditResetArmMS
}

// homeEditChooseLayout switches Home to layout i of homeArrangementChoiceList.
func homeEditChooseLayout(config *Config, i int) {
	if i < 0 || i >= len(homeArrangementChoiceList) || homeLayout == nil {
		return
	}
	if homeEdit.dirty {
		homeEditCommit(config)
	}
	homeEdit.holding = false
	homeEdit.resizing = false
	homeEdit.resetAt = 0
	// A layout picked here is the user's choice: editing must not put the old
	// one back when it ends.
	homeEdit.restore = ""
	name := homeArrangementChoiceList[i]
	changed := normalizeHomeArrangement(name) != homeArrangement(config)
	homeSetArrangement(config, name)
	if changed { // A on the layout already in use is not an edit
		trophyEvent(tkHomeEdit)
	}
	homeEdit.stripIdx = i
	if !homeArrangementIsDynamic(name) {
		homeEdit.strip = true
	}
	homeLayout.dynSync(config)
	if homeLayout.dynActive() && !homeLayout.dynIsTile(homeLayout.Focus.Current()) {
		homeEditFocusIndex(0)
	}
	PlayNavSound()
}

// homeEditCycleLayout steps the layout (L1/R1, Tab).
func homeEditCycleLayout(config *Config, step int) {
	n := len(homeArrangementChoiceList)
	cur := homeArrangementChoiceIndex(homeArrangement(config))
	if cur < 0 {
		cur = 0
	}
	homeEditChooseLayout(config, ((cur+step)%n+n)%n)
}

// ── Input ─────────────────────────────────────────────────────────────────

// homeEditNavigate is a D-pad press.
func homeEditNavigate(config *Config, dir FocusDirection) {
	h := homeLayout
	if h == nil {
		return
	}
	if homeEditBar.open {
		homeEditBarNavigate(config, dir)
		return
	}
	if homeEdit.resizing {
		homeEditResizeStep(config, dir)
		return
	}
	if !h.dynActive() {
		homeEdit.strip = true
	}
	if homeEdit.strip {
		homeEditStripMove(dir)
		return
	}
	if homeEdit.holding {
		homeEditMoveHeld(config, dir)
		return
	}
	cur := h.Focus.Current()
	if !h.dynIsTile(cur) {
		homeEditFocusIndex(0)
		return
	}
	if next, ok := h.dynStepFrom(cur, dir); ok {
		if next != cur && h.Focus.Set(next) {
			h.Focus.Remember()
			h.dynEnsureVisible(next)
			PlayNavSound()
		}
		return
	}
	if dir == FocusUp {
		homeEdit.strip = true
		homeEdit.stripIdx = homeArrangementChoiceIndex(h.Arrangement)
		if homeEdit.stripIdx < 0 {
			homeEdit.stripIdx = 0
		}
		PlayNavSound()
	}
}

// homeEditStripMove walks the strip; Down returns to the tiles and Up goes
// on to the Top bar row.
func homeEditStripMove(dir FocusDirection) {
	dynamic := homeLayout != nil && homeLayout.dynActive()
	step := 0
	switch dir {
	case FocusUp:
		homeEditBarOpen(appConfig)
		return
	case FocusLeft:
		step = -1
	case FocusRight:
		step = 1
	case FocusDown:
		if dynamic {
			homeEdit.strip = false
			if !homeLayout.dynIsTile(homeLayout.Focus.Current()) {
				homeEditFocusIndex(0)
			}
			PlayNavSound()
		}
		return
	default:
		return
	}
	i := homeEdit.stripIdx + step
	if i == homeEditStripReset && !dynamic {
		// Reset only exists for the tile layouts.
		i += step
	}
	if i < 0 || i >= homeEditStripCount {
		return
	}
	homeEdit.stripIdx = i
	PlayNavSound()
}

// homeEditStripActivate presses strip chip i.
func homeEditStripActivate(config *Config, i int) {
	switch {
	case i >= 0 && i < len(homeArrangementChoiceList):
		homeEditChooseLayout(config, i)
	case i == homeEditStripBar:
		homeEditBarOpen(config)
	case i == homeEditStripReset:
		if homeLayout != nil && homeLayout.dynActive() {
			homeEditReset(config)
		}
	case i == homeEditStripDone:
		PlayBackSound()
		homeEditClose(config)
	}
}

// homeEditPrimary is A: the strip's chip, the add sheet, or picking up and
// putting down the focused tile. In resize mode it is done resizing.
func homeEditPrimary(config *Config) {
	if homeEdit.resizing {
		homeEditResizeEnd(config)
		return
	}
	if homeEdit.strip {
		homeEditStripActivate(config, homeEdit.stripIdx)
		return
	}
	h := homeLayout
	if h == nil {
		return
	}
	cur := h.Focus.Current()
	if cur == HomeFocusID(homeTileAddRef) {
		PlayActivateSound()
		homeAddSheetOpen(config, homeEditLastTile())
		return
	}
	if !h.dynIsTile(cur) {
		return
	}
	if homeEdit.holding {
		homeEditDrop(config)
		return
	}
	homeEdit.holding = true
	PlayActivateSound()
}

// homeEditDrop puts the held tile down where it is.
func homeEditDrop(config *Config) {
	homeEdit.holding = false
	if homeEdit.dirty {
		homeEditCommit(config)
	}
	PlayActivateSound()
}

// homeEditBack is B: end resizing, put the held tile down, else finish
// editing.
func homeEditBack(config *Config) {
	if homeEdit.resizing {
		homeEditResizeEnd(config)
		return
	}
	if homeEdit.holding {
		homeEditDrop(config)
		return
	}
	PlayBackSound()
	homeEditClose(config)
}

// homeEditPad is the controller while editing. Start and the buttons edit
// mode does not use fall through: Start opens the menu, which ends editing.
func homeEditPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	if homeEdit.sheet.open {
		return homeAddSheetPad(e, config)
	}
	if homeEditBar.open {
		return homeEditBarPad(e, config)
	}
	if homeEdit.resizing {
		// Resize mode: the D-pad sizes the tile, A, B and X end it. Y and
		// the shoulders wait (a layout switch or a removal mid-resize would
		// lose track of the tile); Start falls through to the menu, which
		// ends editing and saves.
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			homeEditResizeStep(config, FocusUp)
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			homeEditResizeStep(config, FocusDown)
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			homeEditResizeStep(config, FocusLeft)
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			homeEditResizeStep(config, FocusRight)
		case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_X:
			homeEditResizeEnd(config)
		case sdl.CONTROLLER_BUTTON_Y, sdl.CONTROLLER_BUTTON_LEFTSHOULDER, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER,
			sdl.CONTROLLER_BUTTON_BACK:
		default:
			return false
		}
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		homeEditNavigate(config, FocusUp)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		homeEditNavigate(config, FocusDown)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		homeEditNavigate(config, FocusLeft)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		homeEditNavigate(config, FocusRight)
	case sdl.CONTROLLER_BUTTON_A:
		homeEditPrimary(config)
	case sdl.CONTROLLER_BUTTON_X:
		if !homeEdit.strip {
			homeEditResizeToggle(config)
		}
	case sdl.CONTROLLER_BUTTON_Y:
		if !homeEdit.strip {
			homeEditRemove(config)
		}
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		homeEditCycleLayout(config, -1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		homeEditCycleLayout(config, 1)
	case sdl.CONTROLLER_BUTTON_B:
		homeEditBack(config)
	case sdl.CONTROLLER_BUTTON_BACK:
		// Select starts chords (Select + L1, the virtual mouse; Select + R1,
		// the control profiles): it must not end editing on its own press,
		// nor fall through to the scene's Back.
	default:
		return false
	}
	return true
}

// homeEditKey is the keyboard while editing: the arrows, Enter (A), X or R
// (resize; the arrows then size the tile), Delete, Backspace or Y (remove),
// Tab / Shift+Tab (layout), Esc (B) and Q, L2's twin on Home, which is Done.
// E, the other section key, is kept from paging away mid-edit.
func homeEditKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return true
	}
	if homeEdit.sheet.open {
		return homeAddSheetKey(e, config)
	}
	if homeEditBar.open {
		return homeEditBarKey(e, config)
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		homeEditNavigate(config, FocusUp)
	case sdl.K_DOWN:
		homeEditNavigate(config, FocusDown)
	case sdl.K_LEFT:
		homeEditNavigate(config, FocusLeft)
	case sdl.K_RIGHT:
		homeEditNavigate(config, FocusRight)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		if e.Repeat == 0 {
			homeEditPrimary(config)
		}
	case sdl.K_x, sdl.K_r:
		if e.Repeat == 0 && !homeEdit.strip {
			homeEditResizeToggle(config)
		}
	case sdl.K_DELETE, sdl.K_BACKSPACE, sdl.K_y:
		if e.Repeat == 0 && !homeEdit.strip && !homeEdit.resizing {
			homeEditRemove(config)
		}
	case sdl.K_TAB:
		if e.Repeat == 0 && !homeEdit.resizing {
			step := 1
			if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
				step = -1
			}
			homeEditCycleLayout(config, step)
		}
	case sdl.K_ESCAPE, sdl.K_b:
		if e.Repeat == 0 {
			homeEditBack(config)
		}
	case sdl.K_q:
		if e.Repeat == 0 {
			homeEditTrigger(config)
		}
	case sdl.K_e:
	default:
		return false
	}
	return true
}

// homeEditTrigger is L2 on Home (shoulders.go) and Q, its keyboard twin: it
// opens Edit Home, and while editing it is Done, as B is — a carried tile is
// put down and a resize kept. The add sheet ignores it (B closes the sheet).
// It opens only while Home itself is in front: under a menu, a popover or the
// keyboard the trigger belongs to what is on top.
func homeEditTrigger(config *Config) {
	if homeEdit.open {
		if homeEdit.sheet.open {
			return
		}
		PlayBackSound()
		homeEditClose(config)
		return
	}
	if !homeOwnsPointer() {
		return
	}
	PlayActivateSound()
	homeEditOpen(config)
}

// homeEditBadgeRects are a tile's edit badges for a tile drawn at draw: the
// remove badge, and on the focused grid tile (withSize) the size chip and the
// resize handle. The remove badge sits on the tile's top-right corner, most
// of it in the gutter, so it covers neither a tile's content (the Continue
// card's play button, a clock's figures) nor its neighbour; the handle
// mirrors it on the bottom-right corner. The size chip is a tab on the middle
// of the bottom edge, and only the focused tile of the grid wears one: one
// tile's chip over the gutter reads as its label, a chip on every tile
// covered their names.
func (h *HomeLayout) homeEditBadgeRects(fonts HomeFonts, t homeDynTile, draw sdl.Rect, withSize bool) (remove, size, handle sdl.Rect) {
	d := max32(18, scaleY(26, h.Height))
	over := min32(d/2, max32(4, scaleX(10, h.Width)))
	remove = sdl.Rect{X: draw.X + draw.W - d + over, Y: draw.Y - over, W: d, H: d}
	if withSize && homeTileCanResize(t.Ref) && fonts.Small != nil {
		lw, _ := measureText(fonts.Small, homeTileSizeLabel(t.Size))
		hh := max32(18, scaleY(24, h.Height))
		w := lw + scaleX(16, h.Width)
		size = sdl.Rect{X: draw.X + (draw.W-w)/2, Y: draw.Y + draw.H - hh/2 - scaleY(2, h.Height), W: w, H: hh}
		handle = sdl.Rect{X: draw.X + draw.W - d + over, Y: draw.Y + draw.H - d + over, W: d, H: d}
	}
	return remove, size, handle
}

// homeEditBadgeClick runs a tap on the focused grid tile's badges — remove,
// the size chip (the next usual size) or the corner handle (a press that
// sizes the tile as it moves). They hang over the tile's edges, where the
// tile itself is not hit, so they are tried before the tile under the
// pointer. It reports whether the tap was theirs.
func homeEditBadgeClick(config *Config, mx, my int32) bool {
	h := homeLayout
	if h == nil || !h.dynActive() || h.dynCarousel() || homeEdit.strip {
		return false
	}
	id := h.Focus.Current()
	i := h.dynTileIndex(id)
	if i < 0 || h.dyn.tiles[i].Kind == homeTileAdd || !h.dynVisible(i) {
		return false
	}
	draw := h.Items[id].Rect
	if draw.W <= 0 || draw.H <= 0 {
		return false
	}
	remove, size, handle := h.homeEditBadgeRects(loadHomeFonts(), h.dyn.tiles[i], draw, true)
	// A tile the pad is carrying is put down where it is first, quietly: the
	// badge's own sound follows.
	putDown := func() {
		if homeEdit.holding {
			homeEdit.holding = false
			if homeEdit.dirty {
				homeEditCommit(config)
			}
		}
	}
	switch {
	// The handle is hit as drawn, not grown to a touch target: grown, it
	// reached over the gutter onto the remove badge of the tile below.
	case handle.W > 0 && pointInRect(mx, my, handle):
		putDown()
		r := h.dynTargetRect(i)
		homeEdit.press.active, homeEdit.press.drag, homeEdit.press.resize = true, false, true
		homeEdit.press.resized = false
		homeEdit.press.id = id
		homeEdit.press.x, homeEdit.press.y = mx, my
		homeEdit.press.anchorX, homeEdit.press.anchorY = r.X, r.Y
		homeEdit.press.offX, homeEdit.press.offY = r.X+r.W-mx, r.Y+r.H-my
		homeEdit.press.lastSwap = ""
		PlayActivateSound()
		return true
	case size.W > 0 && pointInRect(mx, my, growTouchTarget(size)):
		putDown()
		homeEditResizeCycle(config)
		return true
	case pointInRect(mx, my, growTouchTarget(remove)):
		homeEditRemove(config)
		return true
	}
	return false
}

// homeEditClick is a tap while editing. Every tap is edit mode's.
func homeEditClick(mx, my int32, config *Config) bool {
	if !homeEdit.open {
		return false
	}
	homeEdit.mouseX, homeEdit.mouseY = mx, my
	if homeEdit.sheet.open {
		homeAddSheetClick(mx, my, config)
		return true
	}
	if homeEditBarClick(config, mx, my) {
		return true
	}
	for i, r := range homeEdit.chips {
		if r.W > 0 && pointInRect(mx, my, growTouchTarget(r)) {
			// A resize in progress ends (and is saved) first: the D-pad must
			// not go on sizing a tile behind a focused strip.
			if homeEdit.resizing {
				homeEditResizeEnd(config)
			}
			homeEdit.strip = true
			homeEdit.stripIdx = i
			homeEditStripActivate(config, i)
			return true
		}
	}
	h := homeLayout
	if h == nil || !h.dynActive() {
		return true
	}
	if homeEditBadgeClick(config, mx, my) {
		return true
	}
	id := h.dynTileAt(mx, my)
	if id == "" {
		return true
	}
	i := h.dynTileIndex(id)
	if i < 0 {
		return true
	}
	// A tile the pad is carrying is put down before the tap does anything
	// else: the held state must not pass to the tile that was tapped. A resize
	// in progress ends (and is saved) the same way.
	if homeEdit.holding || homeEdit.resizing {
		if homeEdit.holding {
			homeEditDrop(config)
		} else {
			homeEditResizeEnd(config)
		}
		i = h.dynTileIndex(id)
		if i < 0 {
			return true
		}
	}
	t := h.dyn.tiles[i]
	draw := h.Items[id].Rect
	centre := !h.dynCarousel() || (i == h.dyn.carIdx && h.carSettled())
	if t.Kind != homeTileAdd && centre {
		remove, _, _ := h.homeEditBadgeRects(loadHomeFonts(), t, draw, false)
		if pointInRect(mx, my, growTouchTarget(remove)) {
			if h.Focus.Set(id) {
				homeEditRemove(config)
			}
			return true
		}
	}
	if !h.Focus.Set(id) {
		return true
	}
	h.Focus.Remember()
	homeEdit.strip = false
	if h.dynCarousel() && !centre {
		h.dynEnsureVisible(id)
		PlayNavSound()
		return true
	}
	if t.Kind == homeTileAdd {
		PlayActivateSound()
		homeAddSheetOpen(config, homeEditLastTile())
		return true
	}
	// Arm a drag: on the grid the tile follows the pointer; on the carousel
	// the middle card moves a place for every half card the pointer travels.
	homeEdit.press.active, homeEdit.press.drag = true, false
	homeEdit.press.id = id
	homeEdit.press.x, homeEdit.press.y = mx, my
	homeEdit.press.offX, homeEdit.press.offY = mx-draw.X, my-draw.Y
	homeEdit.press.lastSwap = ""
	return true
}

// homeEditDragging reports whether the pointer is carrying grid tile id (the
// carousel's carried card stays in the middle).
func homeEditDragging(id HomeFocusID) bool {
	return homeEdit.open && homeEdit.press.active && homeEdit.press.drag && homeEdit.press.id == id &&
		homeLayout != nil && !homeLayout.dynCarousel()
}

// homeEditDragRect is where the carried tile is drawn: under the pointer,
// held where it was grabbed.
func homeEditDragRect(r sdl.Rect) sdl.Rect {
	return sdl.Rect{X: homeEdit.mouseX - homeEdit.press.offX, Y: homeEdit.mouseY - homeEdit.press.offY, W: r.W, H: r.H}
}

// homeEditPointerMotion follows the pointer while editing: a press past the
// slop starts a drag, a drag reorders the tiles under it, and hovering focuses
// what it is over. It reports whether edit mode took the motion (always,
// while editing), so Home's own hover does not run.
func homeEditPointerMotion(config *Config, mx, my int32, held bool) bool {
	if !homeEdit.open {
		return false
	}
	moved := mx != homeEdit.mouseX || my != homeEdit.mouseY
	homeEdit.mouseX, homeEdit.mouseY = mx, my
	if homeEdit.sheet.open {
		if moved {
			homeAddSheetHover(mx, my)
		}
		return true
	}
	if homeEditBarPointerMotion(config, mx, my, held) {
		return true
	}
	h := homeLayout
	if h == nil {
		return true
	}
	if homeEdit.press.active {
		if !held {
			// The release happened somewhere the button-up did not reach.
			homeEditPointerUp(config, mx, my)
			return true
		}
		if homeEdit.press.resize {
			// The corner handle: the tile's size follows the pointer.
			homeEditResizeDragTo(config, mx, my)
			return true
		}
		if !homeEdit.press.drag {
			slop := max32(4, scaleX(homeEditDragSlop, h.Width))
			if homeAbsInt(int(mx-homeEdit.press.x)) > int(slop) || homeAbsInt(int(my-homeEdit.press.y)) > int(slop) {
				homeEdit.press.drag = true
				if h.dynCarousel() {
					// The carousel lifts the card it carries, as A does.
					homeEdit.holding = true
				}
			}
		}
		if homeEdit.press.drag {
			if h.dynCarousel() {
				homeEditCarouselDrag(config, mx)
			} else {
				homeEditDragTo(config, mx, my)
				homeEditDragScroll(config, my)
			}
		}
		return true
	}
	// A carried tile and one being resized keep the focus: hovering another
	// tile must not hand the D-pad to it.
	if !moved || homeEdit.holding || homeEdit.resizing {
		return true
	}
	for i, r := range homeEdit.chips {
		if r.W > 0 && pointInRect(mx, my, r) {
			homeEdit.strip = true
			homeEdit.stripIdx = i
			return true
		}
	}
	if h.dynActive() && !h.dynCarousel() {
		if id := h.dynTileAt(mx, my); id != "" && h.dynVisible(h.dynTileIndex(id)) && h.Focus.Set(id) {
			homeEdit.strip = false
		}
	}
	return true
}

// homeEditDragTo moves the carried tile to the place of the tile whose
// settled cell is under the pointer. Settled cells, not the animated ones: a
// tile sliding out of the way must not be "under the pointer" again. The tile
// it last traded places with is left alone until the pointer leaves it: a
// tile wider than the carried one is still under the pointer after the trade,
// and trading back would flip the two on every motion event.
func homeEditDragTo(config *Config, mx, my int32) {
	h := homeLayout
	if h == nil || !h.dynActive() || h.dynCarousel() {
		return
	}
	from := homeTilesIndex(homeEdit.work, string(homeEdit.press.id))
	if from < 0 {
		return
	}
	target, targetRef := -1, ""
	for i, t := range h.dyn.tiles {
		if t.ID == homeEdit.press.id {
			continue
		}
		r := glassIntersect(h.dynTargetRect(i), h.dynViewport())
		if r.W <= 0 || !pointInRect(mx, my, r) {
			continue
		}
		targetRef = t.Ref
		if t.Kind == homeTileAdd {
			target = len(homeEdit.work) - 1
		} else {
			target = homeTilesIndex(homeEdit.work, t.Ref)
		}
		break
	}
	if target < 0 {
		homeEdit.press.lastSwap = ""
		return
	}
	if targetRef == homeEdit.press.lastSwap || target == from {
		return
	}
	homeEdit.work = homeTilesMove(homeEdit.work, from, target)
	homeEdit.press.lastSwap = targetRef
	homeEditChanged(config)
	if h.Focus.Set(homeEdit.press.id) {
		h.Focus.Remember()
	}
}

// homeEditDragScroll scrolls the grid a row while a carried tile is held
// near the body's top or bottom edge, so a tile can travel to rows that are
// not on screen. It is paced: one row per homeEditDragScrollMS.
func homeEditDragScroll(config *Config, my int32) {
	h := homeLayout
	if h == nil || !h.dynActive() || h.dynCarousel() || h.dyn.rows <= homeGridRowsShown {
		return
	}
	now := sdl.GetTicks64()
	if now-homeEdit.scrollAt < homeEditDragScrollMS {
		return
	}
	vp := h.dynViewport()
	edge := scaleY(28, h.Height)
	before := h.dyn.first
	switch {
	case my < vp.Y+edge:
		h.dyn.first--
	case my > vp.Y+vp.H-edge:
		h.dyn.first++
	default:
		return
	}
	h.dynClampScroll()
	if h.dyn.first != before {
		homeEdit.scrollAt = now
		homeEdit.press.lastSwap = ""
		homeEditDragTo(config, homeEdit.mouseX, my)
	}
}

// homeEditCarouselDrag moves the carried carousel card one place for every
// half card the pointer travels sideways; the row slides under it.
func homeEditCarouselDrag(config *Config, mx int32) {
	h := homeLayout
	if h == nil || !h.dynCarousel() {
		return
	}
	step := max32(8, scaleX(int32(homeCarStep1/2), h.Width))
	for mx-homeEdit.press.x >= step || homeEdit.press.x-mx >= step {
		dir := FocusRight
		if mx < homeEdit.press.x {
			dir = FocusLeft
			homeEdit.press.x -= step
		} else {
			homeEdit.press.x += step
		}
		before := homeEdit.gen
		homeEditMoveHeld(config, dir)
		if homeEdit.gen == before {
			// At an end of the row: nothing more to move.
			homeEdit.press.x = mx
			return
		}
	}
}

// homeEditPointerUp ends a press: a drag puts the tile down and saves, and so
// does letting go of the corner handle.
func homeEditPointerUp(config *Config, mx, my int32) {
	if !homeEdit.open {
		return
	}
	if homeEditBarPointerUp(config) || !homeEdit.press.active {
		return
	}
	dragged, sized := homeEdit.press.drag, homeEdit.press.resize
	homeEdit.press.active, homeEdit.press.drag, homeEdit.press.resize = false, false, false
	homeEdit.press.lastSwap = ""
	homeEdit.mouseX, homeEdit.mouseY = mx, my
	if sized {
		if homeEdit.dirty {
			homeEditCommit(config)
		}
		h := homeLayout
		if h == nil {
			return
		}
		h.dynEnsureVisible(homeEdit.press.id)
		// A click on the handle that did not drag toggles resize mode, so
		// the arrow keys can size the tile too.
		slop := int(max32(4, scaleX(homeEditDragSlop, h.Width)))
		if !homeEdit.press.resized && homeAbsInt(int(mx-homeEdit.press.x)) <= slop &&
			homeAbsInt(int(my-homeEdit.press.y)) <= slop && h.Focus.Current() == homeEdit.press.id {
			homeEditResizeToggle(config)
		}
		return
	}
	if dragged {
		homeEdit.holding = false
		if homeEdit.dirty {
			homeEditCommit(config)
		}
		if homeLayout != nil {
			homeLayout.dynEnsureVisible(homeEdit.press.id)
		}
		PlayActivateSound()
	}
}

// homeWheel is the wheel on Home: the add sheet's list, the widget grid's
// rows or the carousel's cards. Only while Home itself is in front
// (homeOwnsPointer): over a menu, a popover or a panel the wheel is theirs.
func homeWheel(config *Config, dy int32) bool {
	if !homeOwnsPointer() {
		return false
	}
	if homeEdit.open && homeEdit.sheet.open {
		homeAddSheetWheel(dy)
		return true
	}
	if homeEdit.open && (homeEdit.holding || homeEdit.resizing || homeEdit.press.active) {
		return true
	}
	return homeLayout.dynWheel(dy)
}

// homeRightClick opens edit mode on the tile under the pointer of a Widgets
// or Carousel Home.
func homeRightClick(config *Config, mx, my int32) bool {
	h := homeLayout
	if !homeLayoutActive || h == nil || !h.dynActive() {
		return false
	}
	// Not through a menu, a popover or a panel open over Home.
	if !homeOwnsPointer() {
		return false
	}
	if homeEdit.open {
		return true
	}
	id := h.dynTileAt(mx, my)
	if id == "" {
		return false
	}
	if h.Focus.Set(id) {
		h.Focus.Remember()
	}
	PlayActivateSound()
	homeEditOpen(config)
	return true
}

// homeActivateDynamic runs A on a control only the dynamic layouts have: the
// edit pill, the add tile, a widget or an app tile. It reports false for
// everything else, which homeActivate handles as before.
func homeActivateDynamic(config *Config, id HomeFocusID) bool {
	switch id {
	case HomeFocusEditPill:
		homeEditOpen(config)
		return true
	case HomeFocusID(homeTileAddRef):
		homeEditOpen(config)
		if homeEdit.open {
			homeAddSheetOpen(config, homeEditLastTile())
		}
		return true
	}
	ref := string(id)
	switch homeTileKindOf(ref) {
	case homeTileWidget:
		if _, known := homeWidgetSpecFor(homeTileWidgetName(ref)); !known {
			ShowToast("This widget is not available in this version", ToastKindInfo)
			return true
		}
		homeWidgetActivate(config, homeTileWidgetName(ref))
		return true
	case homeTileApp:
		homeActivateApp(config, ref)
		return true
	}
	return false
}

// renderHomeEditOverlays ends edit mode when another overlay takes the
// screen, and draws the add sheet. renderHomeModern calls it last.
func renderHomeEditOverlays(renderer *sdl.Renderer, config *Config) {
	if !homeEdit.open {
		return
	}
	if tb.startOpen || nc.open || globalSearchOpen || homePowerConfirm.open {
		homeEditClose(config)
		return
	}
	// A tile held still at the body's edge keeps the grid scrolling.
	if homeEdit.press.active && homeEdit.press.drag {
		homeEditDragScroll(config, homeEdit.mouseY)
	}
	renderHomeAddSheet(renderer, config)
}

// ── Footer ────────────────────────────────────────────────────────────────

// The footer's hint rows for the dynamic layouts and edit mode; same length
// as homeFooterHintsStart, empty slots are skipped.
var (
	homeFooterHintsDynamic = [len(homeFooterHintsStart)]footerHint{
		{button: "D-Pad", label: "Navigate"},
		{button: "A", label: "Open"},
		{button: "Y", label: "Search"},
		{button: "L2", label: "Edit"},
		{button: "Start", label: "Menu"},
		{button: "Select+Start", label: "Quit"},
		{}, // Menu/F4 Talk to Juka, when available
	}
	homeFooterHintsEdit = [len(homeFooterHintsStart)]footerHint{
		{button: "D-Pad", label: "Navigate"},
		{button: "A", label: "Move"},
		{button: "X", label: "Resize"},
		{button: "Y / Del", label: "Remove"},
		{button: "L1/R1 / Tab", label: "Layout"},
		{button: "B", label: "Done"},
	}
	homeFooterHintsHold = [len(homeFooterHintsStart)]footerHint{
		{button: "D-Pad", label: "Move"},
		{button: "A", label: "Drop"},
		{button: "X", label: "Resize"},
		{button: "B", label: "Drop"},
	}
	// Resize mode: the D-pad grows and shrinks the tile a cell at a time.
	homeFooterHintsResize = [len(homeFooterHintsStart)]footerHint{
		{button: "D-Pad", label: "Resize"},
		{button: "A / B", label: "Done"},
	}
	homeFooterHintsStrip = [len(homeFooterHintsStart)]footerHint{
		{button: "D-Pad", label: "Choose"},
		{button: "A", label: "Select"},
		{button: "L1/R1 / Tab", label: "Layout"},
		{button: "B", label: "Done"},
	}
	// The Top bar row (home_edit_header.go), and a control carried along it.
	homeFooterHintsBar = [len(homeFooterHintsStart)]footerHint{
		{button: "D-Pad", label: "Choose"},
		{button: "A", label: "Move"},
		{button: "Y / Del", label: "Hide/Show"},
		{button: "X", label: "Reset"},
		{button: "B", label: "Back"},
	}
	homeFooterHintsBarHold = [len(homeFooterHintsStart)]footerHint{
		{button: "D-Pad", label: "Move"},
		{button: "A", label: "Drop"},
		{button: "B", label: "Drop"},
	}
	homeFooterHintsSheet = [len(homeFooterHintsStart)]footerHint{
		{button: "D-Pad", label: "Navigate"},
		{button: "A", label: "Add"},
		{button: "L1/R1", label: "Sections"},
		{button: "B", label: "Close"},
	}
)

// homeFooterHintSet is the footer's hint row for the state Home is in.
func (h *HomeLayout) homeFooterHintSet(showBack bool) [len(homeFooterHintsStart)]footerHint {
	carousel := h.dynCarousel()
	switch {
	case homeEdit.open && homeEdit.sheet.open:
		set := homeFooterHintsSheet
		if len(homeEdit.sheet.entries) == 0 {
			// Nothing left to add: A does nothing, so it is not offered.
			set[1], set[2] = footerHint{}, footerHint{}
		}
		return set
	case homeEdit.open && homeEditBar.open && homeEditBar.holding:
		return homeFooterHintsBarHold
	case homeEdit.open && homeEditBar.open:
		set := homeFooterHintsBar
		if s, ok := homeEditBarSlot(); !ok || !headerSlotHideable(s) {
			set[2] = footerHint{}
		}
		if homeEditBar.idx >= int(hsCount) {
			set[1] = footerHint{button: "A", label: "Select"}
		}
		return set
	case homeEdit.open && (homeEdit.strip || !h.dynActive()):
		return homeFooterHintsStrip
	case homeEdit.open && homeEdit.resizing:
		return homeFooterHintsResize
	case homeEdit.open && homeEdit.holding:
		set := homeFooterHintsHold
		if _, _, ok := homeEditResizable(); !ok || carousel {
			set[2] = footerHint{}
		}
		return set
	case homeEdit.open:
		set := homeFooterHintsEdit
		if _, _, ok := homeEditResizable(); !ok || carousel {
			set[2] = footerHint{}
		}
		if h.Focus.Current() == HomeFocusID(homeTileAddRef) {
			set[1].label = "Add"
			set[2], set[3] = footerHint{}, footerHint{}
		}
		return set
	case h.dynActive():
		set := homeFooterHintsDynamic
		if showBack {
			set[5] = footerHint{button: "B", label: "Back"}
		}
		return homeFooterL2(set)
	case showBack:
		return homeFooterL2(homeFooterHintsBack)
	}
	return homeFooterL2(homeFooterHintsStart)
}

// homeFooterL2 drops "L2 Edit" from a row while a video plays: the pad's
// shoulders are the player's then (shoulders.go handleVideoShoulder), and L2
// is its previous video. Q, the keyboard's Edit, still works beside the mini
// player, so a keyboard row keeps it.
func homeFooterL2(set [len(homeFooterHintsStart)]footerHint) [len(homeFooterHintsStart)]footerHint {
	if hintsUseKeyboard() || snapshotVideoState().phase == "idle" {
		return set
	}
	for i := range set {
		if set[i].button == "L2" {
			set[i] = footerHint{}
		}
	}
	return set
}

// ── The edit pill ─────────────────────────────────────────────────────────

// sizeEditPill places the footer's Edit pill right of the power button. It
// is a control only on a dynamic Home that is not being edited (the strip's
// Done ends editing). Render calls it after sizePowerPill.
func (h *HomeLayout) sizeEditPill(fonts HomeFonts) {
	item := h.Items[HomeFocusEditPill]
	item.ID = HomeFocusEditPill
	item.Title, item.Subtitle, item.Icon = "Edit", "Arrange Home", "edit"
	if !h.dynActive() || homeEdit.open || fonts.Small == nil {
		item.Rect = sdl.Rect{}
		h.Items[HomeFocusEditPill] = item
		h.Focus.SetEnabled(HomeFocusEditPill, false)
		return
	}
	slot := ScaleHomeRect(sdl.Rect{X: 48, Y: 674, W: 116, H: 36}, h.Width, h.Height)
	x := slot.X
	if p := h.Items[HomeFocusExit].Rect; p.W > 0 && h.Focus.IsEnabled(HomeFocusExit) {
		x = p.X + p.W + scaleX(12, h.Width)
		slot.Y, slot.H = p.Y, p.H
	}
	glyph := min32(scaleY(18, h.Height), slot.H-scaleY(12, h.Height))
	lw, _ := measureText(fonts.Small, item.Title)
	w := scaleX(14, h.Width) + glyph + scaleX(8, h.Width) + lw + scaleX(16, h.Width)
	item.Rect = sdl.Rect{X: x, Y: slot.Y, W: w, H: slot.H}
	h.Items[HomeFocusEditPill] = item
	h.Focus.SetEnabled(HomeFocusEditPill, true)
}

// drawEditPill paints the Edit pill: Home's glass pill with the pencil and
// its label, the search pill's focus treatment.
func (h *HomeLayout) drawEditPill(renderer *sdl.Renderer, fonts HomeFonts, item HomeItem, focused, pressed bool, pressDepth, lift float64) {
	if renderer == nil || item.Rect.W <= 0 || item.Rect.H <= 0 || fonts.Small == nil {
		return
	}
	base := item.Rect
	draw := scaleHomeCard(base, lift)
	draw.Y += homePressOffset(pressDepth, h.Height)
	radius := base.H / 2
	if focused {
		drawHomeFocusGlow(renderer, draw)
	}
	fill, edge := homeGlass(16), homeGlass(36)
	switch {
	case pressed:
		fill = homeGlass(32)
	case focused:
		fill = homeGlass(26)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRectAt(renderer, base, draw, radius, fill)
	if focused {
		strokeRoundedRectAt(renderer, base, draw, radius, 3, ColorAccent)
	} else {
		strokeRoundedRectAt(renderer, base, draw, radius, 1, edge)
	}
	glyph := min32(scaleY(18, h.Height), draw.H-scaleY(12, h.Height))
	if glyph < 10 {
		glyph = 10
	}
	icon := sdl.Rect{X: draw.X + scaleX(14, h.Width), Y: draw.Y + (draw.H-glyph)/2, W: glyph, H: glyph}
	ink := ColorTextMuted()
	if focused {
		ink = ColorAccent
	}
	drawHomeGlyphTinted(renderer, item.Icon, icon, ink)
	labelInk := ColorTextMuted()
	if focused {
		labelInk = ColorTextPrimary()
	}
	textX := icon.X + icon.W + scaleX(8, h.Width)
	maxW := draw.X + draw.W - scaleX(12, h.Width) - textX
	if maxW > 0 {
		drawText(renderer, fonts.Small, ellipsize(fonts.Small, item.Title, maxW), textX,
			draw.Y+(draw.H-int32(fonts.Small.Height()))/2, labelInk, textAlignLeft)
	}
}

// ── The strip and the decorations ─────────────────────────────────────────

// homeEditCaption is the line beside "Edit Home": what this layout lets you
// change.
func homeEditCaption(arrangement string) string {
	switch normalizeHomeArrangement(arrangement) {
	case homeArrangementWidgets:
		return "Move, resize, add and remove tiles"
	case homeArrangementCarousel:
		return "Reorder, add and remove cards"
	}
	return "This layout keeps its cards in place: pick Carousel or Widgets to change them"
}

// drawEditStrip draws edit mode's strip where the greeting and the search
// pill are: the title, the four layouts, Top bar, Reset and Done — or, while
// it has the focus, the Top bar row (home_edit_header.go).
func (h *HomeLayout) drawEditStrip(renderer *sdl.Renderer, fonts HomeFonts) {
	for i := range homeEdit.chips {
		homeEdit.chips[i] = sdl.Rect{}
	}
	if renderer == nil {
		return
	}
	x0 := scaleX(homeSearchPillX, h.Width)
	right := h.Width - scaleX(homeSideMargin, h.Width)
	if f := fonts.Greeting; f != nil {
		const title = "Edit Home"
		titleY := scaleY(homeGreetingY, h.Height)
		drawText(renderer, f, title, x0, titleY, ColorTextPrimary(), textAlignLeft)
		if note := fonts.Helper; note != nil {
			tw, _ := measureText(f, title)
			cx := x0 + tw + scaleX(18, h.Width)
			if maxW := right - cx; maxW > scaleX(80, h.Width) {
				cy := titleY + int32(f.Height()) - int32(note.Height()) - scaleY(4, h.Height)
				caption := homeEditCaption(h.Arrangement)
				if homeEditBar.open {
					caption = homeEditBarCaption()
				}
				drawText(renderer, note, ellipsize(note, caption, maxW), cx, cy, ColorTextMuted(), textAlignLeft)
			}
		}
	}
	font := fonts.Helper
	if font == nil {
		font = fonts.Small
	}
	if font == nil {
		return
	}
	y := scaleY(homeSearchPillY, h.Height)
	hh := scaleY(homeSearchPillH, h.Height)
	if homeEditBar.open {
		// The Top bar row has the strip's place (home_edit_header.go).
		h.drawEditBarRow(renderer, font, x0, y, hh, right)
		return
	}
	pad := scaleX(18, h.Width)
	active := homeArrangementChoiceIndex(h.Arrangement)
	x := x0
	for i, name := range homeArrangementChoiceList {
		label := homeArrangementChoiceLabel(name)
		lw, _ := measureText(font, label)
		r := sdl.Rect{X: x, Y: y, W: lw + 2*pad, H: hh}
		h.drawEditChip(renderer, font, r, label, "", i == active, homeEdit.strip && homeEdit.stripIdx == i)
		homeEdit.chips[i] = r
		x += r.W + scaleX(10, h.Width)
	}
	{
		// Top bar: the row that arranges the controls on the right of the
		// top bar, a little apart from the layouts.
		const label = "Top bar"
		glyph := min32(scaleY(18, h.Height), hh-scaleY(14, h.Height))
		lw, _ := measureText(font, label)
		x += scaleX(8, h.Width)
		r := sdl.Rect{X: x, Y: y, W: lw + 2*pad + glyph + scaleX(8, h.Width), H: hh}
		h.drawEditChip(renderer, font, r, label, "edit", false, homeEdit.strip && homeEdit.stripIdx == homeEditStripBar)
		homeEdit.chips[homeEditStripBar] = r
		x += r.W + scaleX(10, h.Width)
	}
	glyph := min32(scaleY(18, h.Height), hh-scaleY(14, h.Height))
	dw, _ := measureText(font, "Done")
	done := sdl.Rect{W: dw + 2*pad + glyph + scaleX(8, h.Width), H: hh, Y: y}
	done.X = right - done.W
	if done.X > x {
		h.drawEditChip(renderer, font, done, "Done", "check", true, homeEdit.strip && homeEdit.stripIdx == homeEditStripDone)
		homeEdit.chips[homeEditStripDone] = done
	}
	if h.dynActive() {
		label := "Reset"
		if homeEditResetArmed() {
			label = "Press again to reset"
		}
		rw, _ := measureText(font, label)
		reset := sdl.Rect{W: rw + 2*pad, H: hh, Y: y}
		reset.X = done.X - scaleX(10, h.Width) - reset.W
		if reset.X > x {
			h.drawEditChip(renderer, font, reset, label, "", false, homeEdit.strip && homeEdit.stripIdx == homeEditStripReset)
			homeEdit.chips[homeEditStripReset] = reset
		}
	}
}

// drawEditChip is one of the strip's pills: glass, accent-tinted when it is
// the layout in force (or Done), ringed when the strip's focus is on it.
func (h *HomeLayout) drawEditChip(renderer *sdl.Renderer, font *ttf.Font, r sdl.Rect, label, icon string, active, focused bool) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	radius := r.H / 2
	if focused {
		drawGlassFocusGlow(renderer, r)
	}
	fill, edge := homeGlass(16), homeGlass(36)
	if active {
		fill, edge = WithAlpha(ColorAccent, 56), WithAlpha(ColorAccent, 150)
	}
	if focused && !active {
		fill = homeGlass(28)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, fill)
	if focused {
		out := homeFocusRingOutset()
		strokeRoundedRect(renderer, r.X-out, r.Y-out, r.W+2*out, r.H+2*out, radius+out, homeFocusRingWidth, ColorAccent)
	} else {
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 1, edge)
	}
	lw, lh := measureText(font, label)
	glyph, gap := int32(0), int32(0)
	if icon != "" {
		glyph = min32(scaleY(18, h.Height), r.H-scaleY(14, h.Height))
		gap = scaleX(8, h.Width)
	}
	x := r.X + (r.W-(glyph+gap+lw))/2
	if glyph > 0 {
		drawHomeGlyphTinted(renderer, icon, sdl.Rect{X: x, Y: r.Y + (r.H-glyph)/2, W: glyph, H: glyph}, ColorTextPrimary())
	}
	drawText(renderer, font, label, x+glyph+gap, r.Y+(r.H-lh)/2, ColorTextPrimary(), textAlignLeft)
}

// drawEditVeil covers a Standard or Hero grid body while editing: those
// layouts keep their cards in place, and the strip above is what there is to
// change.
func (h *HomeLayout) drawEditVeil(renderer *sdl.Renderer, fonts HomeFonts) {
	if renderer == nil {
		return
	}
	body := ScaleHomeRect(sdl.Rect{X: homeDynBodyX - 8, Y: homeDynBodyTop - 8, W: homeDynBodyW + 16, H: homeDynBodyH + 16}, h.Width, h.Height)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, body.X, body.Y, body.W, body.H, scaleY(homeTileRadius, h.Height), WithAlpha(ColorBackground, 170))
	title, note := fonts.Card, fonts.Helper
	if title == nil || note == nil {
		return
	}
	cx := body.X + body.W/2
	cy := body.Y + body.H/2
	const head = "Pick Carousel or Widgets above"
	const line = "to move, resize, add and remove what Home shows"
	maxW := body.W - scaleX(48, h.Width)
	drawText(renderer, title, ellipsize(title, head, maxW), cx, cy-int32(title.Height()), ColorTextPrimary(), textAlignCenter)
	drawText(renderer, note, ellipsize(note, line, maxW), cx, cy+scaleY(4, h.Height), ColorTextMuted(), textAlignCenter)
}

// drawEditBadges draws a tile's edit-mode badges: the remove disc and, on the
// focused grid tile, the size chip and the resize handle. In resize mode the
// chip and the handle take the accent, so the tile the D-pad sizes is plain.
func (h *HomeLayout) drawEditBadges(renderer *sdl.Renderer, fonts HomeFonts, t homeDynTile, draw sdl.Rect, withSize bool) {
	if renderer == nil {
		return
	}
	remove, size, handle := h.homeEditBadgeRects(fonts, t, draw, withSize)
	resizing := withSize && homeEdit.resizing
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	cx, cy, r := remove.X+remove.W/2, remove.Y+remove.H/2, remove.W/2
	fillCircle(renderer, cx, cy, r+1, sdl.Color{R: 0, G: 0, B: 0, A: 70})
	fillCircle(renderer, cx, cy, r, WithAlpha(ColorDanger, 235))
	g := remove.W * 11 / 20
	drawHomeGlyphTinted(renderer, "minus", sdl.Rect{X: cx - g/2, Y: cy - g/2, W: g, H: g}, contrastText(ColorDanger))
	if handle.W > 0 {
		// The handle: a glass disc with the expand arrows on the bottom-right
		// corner, on an opaque sheet like the chip.
		hx, hy, hr := handle.X+handle.W/2, handle.Y+handle.H/2, handle.W/2
		fillCircle(renderer, hx, hy, hr+1, sdl.Color{R: 0, G: 0, B: 0, A: 70})
		fillCircle(renderer, hx, hy, hr, glassSheetFill())
		ink := glassChipInk()
		if resizing {
			fillCircle(renderer, hx, hy, hr, WithAlpha(ColorAccent, 235))
			ink = contrastText(ColorAccent)
		} else {
			fillCircle(renderer, hx, hy, hr, glassChipFill())
		}
		hg := handle.W * 11 / 20
		drawHomeGlyphTinted(renderer, "fullscreen", sdl.Rect{X: hx - hg/2, Y: hy - hg/2, W: hg, H: hg}, ink)
	}
	if size.W > 0 && fonts.Small != nil {
		// The tab hangs over the tile's edge and its ring: an opaque sheet
		// under the glass chip keeps its words clear of the lines behind it.
		fillRoundedRect(renderer, size.X, size.Y, size.W, size.H, size.H/2, glassSheetFill())
		ink := glassChipInk()
		if resizing {
			fillRoundedRect(renderer, size.X, size.Y, size.W, size.H, size.H/2, WithAlpha(ColorAccent, 235))
			ink = contrastText(ColorAccent)
		} else {
			fillRoundedRect(renderer, size.X, size.Y, size.W, size.H, size.H/2, glassChipFill())
			strokeRoundedRect(renderer, size.X, size.Y, size.W, size.H, size.H/2, 1, glassChipEdge())
		}
		drawText(renderer, fonts.Small, homeTileSizeLabel(t.Size), size.X+size.W/2,
			size.Y+(size.H-int32(fonts.Small.Height()))/2, ink, textAlignCenter)
	}
}

// drawEditAddTile paints the add tile: a quiet outlined pane with the plus
// and its label.
func (h *HomeLayout) drawEditAddTile(renderer *sdl.Renderer, fonts HomeFonts, item HomeItem, draw sdl.Rect, focused, pressed bool) {
	if renderer == nil || draw.W <= 0 || draw.H <= 0 {
		return
	}
	if focused {
		drawHomeFocusGlow(renderer, draw)
	}
	fill := homeGlass(6)
	switch {
	case pressed:
		fill = homeGlass(26)
	case focused:
		fill = homeGlass(18)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRectAt(renderer, item.Rect, draw, homeTileRadius, fill)
	strokeRoundedRectAt(renderer, item.Rect, draw, homeTileRadius, 2, homeGlass(54))
	if focused {
		drawHomeFocusRing(renderer, item.Rect, draw, homeTileRadius)
	}
	title := fonts.Tile
	if title == nil {
		title = fonts.Card
	}
	glyph := min32(draw.W, draw.H) / 4
	if glyph < 20 {
		glyph = 20
	}
	if glyph > scaleY(56, h.Height) {
		glyph = scaleY(56, h.Height)
	}
	textH := int32(0)
	if title != nil {
		textH = int32(title.Height())
	}
	top := draw.Y + (draw.H-glyph-scaleY(8, h.Height)-textH)/2
	ink := ColorTextMuted()
	if focused {
		ink = ColorAccent
	}
	drawHomeGlyphTinted(renderer, "plus", sdl.Rect{X: draw.X + (draw.W-glyph)/2, Y: top, W: glyph, H: glyph}, ink)
	if title != nil {
		maxW := draw.W - 2*scaleX(homeTilePad, h.Width)
		drawText(renderer, title, ellipsize(title, item.Title, maxW), draw.X+draw.W/2, top+glyph+scaleY(8, h.Height),
			ColorTextPrimary(), textAlignCenter)
	}
	if note := fonts.Meta; note != nil && item.Subtitle != "" && draw.H >= scaleY(140, h.Height) {
		maxW := draw.W - 2*scaleX(homeTilePad, h.Width)
		y := top + glyph + scaleY(8, h.Height) + textH + scaleY(2, h.Height)
		if y+int32(note.Height()) <= draw.Y+draw.H-scaleY(8, h.Height) {
			drawText(renderer, note, ellipsize(note, item.Subtitle, maxW), draw.X+draw.W/2, y, ColorTextMuted(), textAlignCenter)
		}
	}
}
