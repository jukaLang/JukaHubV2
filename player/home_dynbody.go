package main

// home_dynbody.go — Home's body in the Carousel and Widgets layouts.
//
// Standard, the dashboard and the Hero grid place a fixed set of cards
// (rebuildItems). The two "dynamic" layouts place whatever the tile list
// (home_tiles.go) holds instead: Widgets packs the tiles on a six-column grid
// that scrolls by whole rows once it outgrows the three rows Home shows,
// Carousel shows them as one row of cards centred on the focus
// (home_carousel.go). Both share this file's bookkeeping:
//
//   - the tiles become HomeLayout items with focus IDs of their own (a built-in
//     card keeps its ID, title and activation; a widget, an app and the add
//     tile get theirs from the ref), and Order lists them before the header
//     and footer controls, which keep their old order;
//   - the cards that are not on the list are switched off in the focus
//     controller, so neither the D-pad nor a tap can reach them;
//   - focus steps by the drawn geometry inside the grid, and leaves it for the
//     header control or footer pill nearest the tile — the hand-tuned graph in
//     homefocus.go describes the dashboard and would send the D-pad to cards
//     that are not there;
//   - every animation (a tile settling after a move, the grid's scroll, the
//     carousel's slide) eases by elapsed time, not per call: refreshHomeLayout
//     runs more than once a frame.

import (
	"math"

	"github.com/veandco/go-sdl2/sdl"
)

// The widget grid in 1280x720 design units: the dashboard's body box, six
// columns with 16px gutters and rows of the dashboard's 152px tiles. The body
// shows exactly three rows.
const (
	homeDynBodyX   int32 = 32
	homeDynBodyW   int32 = 1216
	homeDynBodyTop int32 = 160
	homeDynBodyH   int32 = 488
	homeDynGap     int32 = 16
	homeDynRowH    int32 = 152
	homeDynPitch   int32 = homeDynRowH + homeDynGap
	// homeDynClipPad is how far the drawing may reach past the body: a
	// top-row tile's focus ring and lift stand 10px off it, and the header
	// band above ends at y=149 (headerQRRect keeps that gap free).
	homeDynClipPad     int32 = 11
	homeDynClipPadEdit int32 = 16
	// homeDynEaseRate is the share of the remaining distance an animation
	// covers per 60 Hz frame — uiHover's settle.
	homeDynEaseRate = 0.28
	// homeDynAppsRefreshMS is how often app tiles re-read the Start menu: a
	// plugin switched on or off changes what an app tile can open.
	homeDynAppsRefreshMS = 10000
)

// homeDynColX is the left edge of grid column c in design units (c == cols is
// the right edge plus a gutter).
func homeDynColX(c int) float64 {
	cell := float64(homeDynBodyW-int32(homeGridCols-1)*homeDynGap) / float64(homeGridCols)
	return float64(homeDynBodyX) + float64(c)*(cell+float64(homeDynGap))
}

// homeDynCellRect is a packed cell's rect in design units, in content space
// (row 0 at the body's top, no scroll).
func homeDynCellRect(c homeCell) sdl.Rect {
	x0 := int32(math.Round(homeDynColX(c.Col)))
	x1 := int32(math.Round(homeDynColX(c.Col+c.W))) - homeDynGap
	return sdl.Rect{
		X: x0,
		Y: homeDynBodyTop + int32(c.Row)*homeDynPitch,
		W: x1 - x0,
		H: int32(c.H)*homeDynRowH + int32(c.H-1)*homeDynGap,
	}
}

// homeDynTile is one tile of the dynamic body.
type homeDynTile struct {
	ID   HomeFocusID
	Ref  string
	Kind homeTileKind
	Size homeTileSize
	Cell homeCell
	// Title, Subtitle and Icon are a widget's, an app's or the add tile's
	// labels; a built-in card keeps rebuildItems' item.
	Title    string
	Subtitle string
	Icon     string
	// Dim marks an app tile whose Start menu entry is gone.
	Dim bool
	// x, y are the tile's drawn place in design units (content space), eased
	// toward its cell after the list changes; placed says they are set.
	x, y   float64
	placed bool
}

// homeDynSig is what the dynamic body was built from; any change rebuilds it.
type homeDynSig struct {
	gen     uint64
	editing bool
	empty   bool
	arr     string
	w, h    int32
}

// homeDyn is the dynamic body's state.
type homeDyn struct {
	active bool
	sig    homeDynSig
	tiles  []homeDynTile
	cells  []homeCell
	index  map[HomeFocusID]int
	rows   int
	// baseOrder is Order as the standard layouts have it, kept so leaving a
	// dynamic layout puts it back exactly.
	baseOrder []HomeFocusID
	// first is the first grid row the body shows; scroll eases toward it.
	first  int
	scroll float64
	// carIdx is the carousel's focused card; carPos eases toward it along
	// the shorter way round.
	carIdx int
	carPos float64
	// lastTick is the time the animations last stepped.
	lastTick uint64
	// anim reports that something moved on the last step, so the frame rate
	// stays up until it settles (uiAnimating).
	anim bool
	// apps names the app tiles; appsAt is when it was read and appsSig the
	// app refs it was read for.
	apps    map[string]homeAppInfo
	appsAt  uint64
	appsSig string
}

// dynActive reports whether the dynamic body is in charge of Home's body.
func (h *HomeLayout) dynActive() bool {
	return h != nil && h.dyn.active
}

// dynIsTile reports whether id is one of the dynamic body's tiles.
func (h *HomeLayout) dynIsTile(id HomeFocusID) bool {
	if h == nil || !h.dyn.active {
		return false
	}
	_, ok := h.dyn.index[id]
	return ok
}

// dynTileIndex is id's index in the tile list, or -1.
func (h *HomeLayout) dynTileIndex(id HomeFocusID) int {
	if h == nil || !h.dyn.active {
		return -1
	}
	if i, ok := h.dyn.index[id]; ok {
		return i
	}
	return -1
}

// dynCarousel reports whether the dynamic body is the carousel.
func (h *HomeLayout) dynCarousel() bool {
	return h.dynActive() && h.Arrangement == homeArrangementCarousel
}

// homeLayoutTiles is the list the body shows: the one being edited while
// edit mode is open, the stored one otherwise. The second result changes
// whenever the list does.
func homeLayoutTiles(config *Config) ([]homeTile, uint64) {
	if homeEdit.open {
		return homeEdit.work, homeEdit.gen
	}
	tiles := homeTilesCurrent(config)
	return tiles, homeTilesGen()
}

// dynSync keeps the dynamic body in step with the arrangement and the tile
// list, then steps its animations and places the tiles. refreshHomeLayout
// calls it every frame.
func (h *HomeLayout) dynSync(config *Config) {
	if h == nil {
		return
	}
	if !homeArrangementIsDynamic(h.Arrangement) {
		if h.dyn.active {
			h.dynDeactivate()
		}
		return
	}
	tiles, gen := homeLayoutTiles(config)
	sig := homeDynSig{
		gen: gen, editing: homeEdit.open, empty: len(tiles) == 0,
		arr: h.Arrangement, w: h.Width, h: h.Height,
	}
	now := sdl.GetTicks64()
	if !h.dyn.active || sig != h.dyn.sig {
		h.dynRebuild(config, tiles)
		h.dyn.sig = sig
	} else if now-h.dyn.appsAt >= homeDynAppsRefreshMS {
		h.dynRefreshApps(config, tiles)
	}
	h.dynKeepFocusShown()
	h.dynStep(now)
	h.dynPlace()
}

// dynKeepFocusShown keeps the body showing the focused tile whatever moved
// the focus: a control that went away under it, a focus restored on return
// to Home, a switch from the carousel. The carousel turns to it; the grid
// scrolls only when it is wholly out of view, so a tile the pointer hovers
// (always one on screen) never scrolls it, and a tile carried by the pointer
// is left to the drag.
func (h *HomeLayout) dynKeepFocusShown() {
	i := h.dynTileIndex(h.Focus.Current())
	if i < 0 {
		return
	}
	if h.dynCarousel() {
		h.dyn.carIdx = i
		return
	}
	if homeEdit.press.active && homeEdit.press.drag {
		return
	}
	if !h.dynVisible(i) {
		h.dynEnsureVisible(h.dyn.tiles[i].ID)
	}
}

// dynChromeOrder is Order's header and footer controls in their standard
// order, then the edit pill.
func (h *HomeLayout) dynChromeOrder() []HomeFocusID {
	out := make([]HomeFocusID, 0, len(h.dyn.baseOrder)+1)
	for _, id := range h.dyn.baseOrder {
		if homeIsBodyCard(id) || id == HomeFocusEditPill {
			continue
		}
		out = append(out, id)
		if id == HomeFocusExit {
			out = append(out, HomeFocusEditPill)
		}
	}
	for _, id := range out {
		if id == HomeFocusEditPill {
			return out
		}
	}
	return append(out, HomeFocusEditPill)
}

// dynRebuild lays the tile list out again: items, order, focus IDs, cells.
func (h *HomeLayout) dynRebuild(config *Config, tiles []homeTile) {
	if !h.dyn.active {
		if len(h.dyn.baseOrder) == 0 {
			h.dyn.baseOrder = append([]HomeFocusID(nil), h.Order...)
		}
		h.dyn.active = true
		h.dyn.first, h.dyn.scroll = 0, 0
		h.dyn.lastTick = 0
	}
	// The built-in cards' items as rebuildItems makes them: a card that comes
	// back on the list gets its title, icon and action back.
	h.rebuildItems()

	prev := make(map[HomeFocusID]homeDynTile, len(h.dyn.tiles))
	for _, t := range h.dyn.tiles {
		prev[t.ID] = t
	}

	list := tiles
	withAdd := homeEdit.open || len(tiles) == 0
	n := len(list)
	if withAdd {
		n++
	}
	out := make([]homeDynTile, 0, n)
	for _, t := range list {
		kind := homeTileKindOf(t.Ref)
		if kind == homeTileUnknown || kind == homeTileAdd {
			continue
		}
		out = append(out, homeDynTile{ID: HomeFocusID(t.Ref), Ref: t.Ref, Kind: kind, Size: homeTileSnapSize(t.Ref, t.Size)})
	}
	if withAdd {
		add := homeDynTile{ID: HomeFocusID(homeTileAddRef), Ref: homeTileAddRef, Kind: homeTileAdd, Size: homeTileSize{W: 1, H: 1},
			Title: "Add", Subtitle: "Widgets and apps", Icon: "plus"}
		if len(tiles) == 0 && !homeEdit.open {
			add.Title, add.Subtitle = "Add tiles", "Home is empty"
		}
		out = append(out, add)
	}

	// Labels: widgets from their catalogue entry, apps from the Start menu.
	// The menu is read again only when the app tiles changed (or on the
	// refresh tick): carrying a tile is a rebuild per step, and reading the
	// menu each time was a whole Start menu build per press.
	if sig := homeAppRefsSig(tiles); sig != h.dyn.appsSig || h.dyn.apps == nil {
		h.dyn.apps = homeResolveApps(config, tiles)
		h.dyn.appsSig = sig
		h.dyn.appsAt = sdl.GetTicks64()
	}
	for i := range out {
		t := &out[i]
		switch t.Kind {
		case homeTileWidget:
			if spec, ok := homeWidgetSpecFor(homeTileWidgetName(t.Ref)); ok {
				t.Title, t.Subtitle, t.Icon = spec.Title, spec.Note, spec.Glyph
			} else {
				// A widget this build does not have: kept, shown as such.
				t.Title, t.Subtitle, t.Icon, t.Dim = homeTileWidgetName(t.Ref), "Not available here", "sparkles", true
			}
		case homeTileApp:
			info := h.dyn.apps[t.Ref]
			t.Title, t.Subtitle, t.Icon, t.Dim = info.Title, info.Note, info.Glyph, !info.ok
		}
		if old, ok := prev[t.ID]; ok && old.placed {
			t.x, t.y, t.placed = old.x, old.y, true
		}
	}

	// Cells. The carousel does not use them, but packing is cheap and keeps
	// the grid's geometry ready for the switch back.
	sizes := make([]homeTileSize, len(out))
	for i, t := range out {
		sizes[i] = t.Size
	}
	h.dyn.cells, h.dyn.rows = homePackTiles(sizes, homeGridCols, h.dyn.cells)
	for i := range out {
		out[i].Cell = h.dyn.cells[i]
	}
	h.dyn.tiles = out
	h.dyn.index = make(map[HomeFocusID]int, len(out))
	for i, t := range out {
		h.dyn.index[t.ID] = i
	}

	// Items: the non-card tiles get theirs; leftovers of tiles that left the
	// list go.
	for id := range h.Items {
		if _, inGraph := homeFocusGraph[id]; inGraph {
			continue
		}
		if _, keep := h.dyn.index[id]; !keep {
			delete(h.Items, id)
		}
	}
	for _, t := range out {
		if t.Kind == homeTileCard {
			continue
		}
		h.Items[t.ID] = HomeItem{ID: t.ID, Title: t.Title, Subtitle: t.Subtitle, Icon: t.Icon}
	}

	// Order and focus: the tiles first (a tap on a tile's edge stays the
	// tile's, as the cards' did), then the controls in their usual order. The
	// focus is read first: switching a card off moves it on its own.
	cur := h.Focus.Current()
	ids := make([]HomeFocusID, 0, len(out))
	var dynamicIDs []HomeFocusID
	for _, t := range out {
		ids = append(ids, t.ID)
		if _, inGraph := homeFocusGraph[t.ID]; !inGraph {
			dynamicIDs = append(dynamicIDs, t.ID)
		}
	}
	h.Order = append(ids, h.dynChromeOrder()...)
	h.Focus.SetDynamic(dynamicIDs, ids)
	for _, id := range homeBodyCards {
		_, on := h.dyn.index[id]
		h.Focus.SetEnabled(id, on)
	}

	// Keep the focus on a tile that is still there: the same one, or the one
	// that took its place (the first tile at or after its old cell).
	if old, was := prev[cur]; was {
		if _, still := h.dyn.index[cur]; !still && len(h.dyn.tiles) > 0 {
			idx := len(h.dyn.tiles) - 1
			for i, t := range h.dyn.tiles {
				if t.Cell.Row > old.Cell.Row || (t.Cell.Row == old.Cell.Row && t.Cell.Col >= old.Cell.Col) {
					idx = i
					break
				}
			}
			h.Focus.Set(h.dyn.tiles[idx].ID)
		}
	}
	if n := len(h.dyn.tiles); n > 0 {
		if h.dyn.carIdx >= n {
			h.dyn.carIdx = n - 1
		}
		if h.dyn.carIdx < 0 {
			h.dyn.carIdx = 0
		}
	} else {
		h.dyn.carIdx = 0
	}
	if i := h.dynTileIndex(h.Focus.Current()); i >= 0 {
		h.dyn.carIdx = i
	}
	h.dynClampScroll()
}

// dynRefreshApps re-reads the app tiles' names from the Start menu.
func (h *HomeLayout) dynRefreshApps(config *Config, tiles []homeTile) {
	h.dyn.appsAt = sdl.GetTicks64()
	if h.dyn.appsSig == "" {
		// No app tiles: nothing to read.
		return
	}
	h.dyn.apps = homeResolveApps(config, tiles)
	for i := range h.dyn.tiles {
		t := &h.dyn.tiles[i]
		if t.Kind != homeTileApp {
			continue
		}
		info := h.dyn.apps[t.Ref]
		t.Title, t.Subtitle, t.Icon, t.Dim = info.Title, info.Note, info.Glyph, !info.ok
		item := h.Items[t.ID]
		item.Title, item.Subtitle, item.Icon = t.Title, t.Subtitle, t.Icon
		h.Items[t.ID] = item
	}
}

// dynDeactivate hands the body back to the standard layouts.
func (h *HomeLayout) dynDeactivate() {
	if h == nil || !h.dyn.active {
		return
	}
	if len(h.dyn.baseOrder) > 0 {
		h.Order = append(h.Order[:0:0], h.dyn.baseOrder...)
	}
	// The cards first, so a focus that was on a widget falls back to the
	// first card rather than to the search pill.
	for _, id := range homeBodyCards {
		h.Focus.SetEnabled(id, true)
	}
	h.Focus.SetDynamic(nil, nil)
	for id := range h.Items {
		if _, inGraph := homeFocusGraph[id]; !inGraph {
			delete(h.Items, id)
		}
	}
	h.Focus.SetEnabled(HomeFocusEditPill, false)
	base := h.dyn.baseOrder
	h.dyn = homeDyn{baseOrder: base}
	h.rebuildItems()
}

// homeDynEase moves cur toward target by the share of the distance dt
// milliseconds of the 60 Hz settle cover.
func homeDynEase(cur, target, dtMS float64) float64 {
	if dtMS <= 0 {
		return cur
	}
	k := 1 - math.Pow(1-homeDynEaseRate, dtMS/16.6667)
	return cur + (target-cur)*k
}

// dynStep advances the animations to now. With motion off everything snaps.
func (h *HomeLayout) dynStep(now uint64) {
	d := &h.dyn
	dt := 0.0
	if d.lastTick != 0 && now > d.lastTick {
		dt = float64(now - d.lastTick)
	}
	if dt > 100 {
		dt = 100
	}
	if now != d.lastTick {
		d.lastTick = now
	}
	snap := !h.Motion
	moving := false
	settle := func(cur, target, eps float64) float64 {
		if snap || math.Abs(target-cur) < eps {
			return target
		}
		next := homeDynEase(cur, target, dt)
		if math.Abs(target-next) < eps {
			next = target
		}
		if next != target {
			moving = true
		}
		return next
	}
	// The grid: every tile toward its cell, the body toward its first row.
	for i := range d.tiles {
		t := &d.tiles[i]
		r := homeDynCellRect(t.Cell)
		tx, ty := float64(r.X), float64(r.Y)
		if !t.placed {
			t.x, t.y, t.placed = tx, ty, true
			continue
		}
		t.x = settle(t.x, tx, 0.5)
		t.y = settle(t.y, ty, 0.5)
	}
	d.scroll = settle(d.scroll, float64(d.first), 0.004)
	// The carousel: the shorter way round, then back into [0, n).
	if n := len(d.tiles); n > 0 {
		target := d.carPos + homeCarouselWrap(float64(d.carIdx)-d.carPos, n)
		d.carPos = settle(d.carPos, target, 0.002)
		if d.carPos == target {
			d.carPos = float64(d.carIdx)
		}
	} else {
		d.carPos = 0
	}
	// A second call in the same millisecond moves nothing; it must not
	// report the animation as finished.
	if snap {
		d.anim = false
	} else if dt > 0 {
		d.anim = moving
	}
}

// homeLayoutAnimating reports whether Home's dynamic body is still moving,
// so the frame rate stays at its interactive cadence until it settles.
func homeLayoutAnimating() bool {
	return homeLayoutActive && homeLayout != nil && homeLayout.dyn.active && homeLayout.dyn.anim
}

// dynClampScroll keeps the first shown row inside the grid.
func (h *HomeLayout) dynClampScroll() {
	maxFirst := h.dyn.rows - homeGridRowsShown
	if maxFirst < 0 {
		maxFirst = 0
	}
	if h.dyn.first > maxFirst {
		h.dyn.first = maxFirst
	}
	if h.dyn.first < 0 {
		h.dyn.first = 0
	}
}

// dynEnsureVisible scrolls the grid so id's rows are on screen, and turns the
// carousel to it.
func (h *HomeLayout) dynEnsureVisible(id HomeFocusID) {
	i := h.dynTileIndex(id)
	if i < 0 {
		return
	}
	h.dyn.carIdx = i
	c := h.dyn.tiles[i].Cell
	if c.Row < h.dyn.first {
		h.dyn.first = c.Row
	}
	if c.Row+c.H > h.dyn.first+homeGridRowsShown {
		h.dyn.first = c.Row + c.H - homeGridRowsShown
	}
	h.dynClampScroll()
}

// dynViewport is the grid's body box on the canvas; dynClip the box drawing
// may use (the body plus room for a top or bottom tile's ring).
func (h *HomeLayout) dynViewport() sdl.Rect {
	return ScaleHomeRect(sdl.Rect{X: homeDynBodyX, Y: homeDynBodyTop, W: homeDynBodyW, H: homeDynBodyH}, h.Width, h.Height)
}

func (h *HomeLayout) dynClip() sdl.Rect {
	pad := homeDynClipPad
	if homeEdit.open {
		// A carried tile stands 14px proud with its ring, and the remove
		// badges overhang the corners: more room at the sides and the bottom.
		// Not at the top, where Edit Home's strip ends at y=148.
		pad = homeDynClipPadEdit
	}
	return ScaleHomeRect(sdl.Rect{
		X: homeDynBodyX - pad, Y: homeDynBodyTop - homeDynClipPad,
		W: homeDynBodyW + 2*pad, H: homeDynBodyH + homeDynClipPad + pad,
	}, h.Width, h.Height)
}

// dynTileDesignRect is where tile i is drawn now, in design units on the
// screen (its eased place, less the scroll).
func (h *HomeLayout) dynTileDesignRect(i int) sdl.Rect {
	t := h.dyn.tiles[i]
	r := homeDynCellRect(t.Cell)
	r.X = int32(math.Round(t.x))
	r.Y = int32(math.Round(t.y - h.dyn.scroll*float64(homeDynPitch)))
	return r
}

// dynTargetRect is where tile i settles, on the canvas, at the scroll the
// body is heading for: what focus steps by, so a press during an animation
// goes where the finished layout says.
func (h *HomeLayout) dynTargetRect(i int) sdl.Rect {
	r := homeDynCellRect(h.dyn.tiles[i].Cell)
	r.Y -= int32(h.dyn.first) * homeDynPitch
	return ScaleHomeRect(r, h.Width, h.Height)
}

// dynPlace writes every tile's on-screen rect into its item.
func (h *HomeLayout) dynPlace() {
	if h.dynCarousel() {
		h.carPlace()
		return
	}
	for i, t := range h.dyn.tiles {
		item := h.Items[t.ID]
		item.ID = t.ID
		item.Rect = ScaleHomeRect(h.dynTileDesignRect(i), h.Width, h.Height)
		h.Items[t.ID] = item
	}
}

// dynVisible reports whether tile i's settled place is inside the body.
func (h *HomeLayout) dynVisible(i int) bool {
	if h.dynCarousel() {
		return true
	}
	c := h.dyn.tiles[i].Cell
	return c.Row+c.H > h.dyn.first && c.Row < h.dyn.first+homeGridRowsShown
}

// dynHitRect is the part of a tile a tap or the pointer can reach: its drawn
// rect inside the body (the carousel's cards are hit as drawn).
func (h *HomeLayout) dynHitRect(id HomeFocusID) sdl.Rect {
	item, ok := h.Items[id]
	if !ok || item.Rect.W <= 0 || item.Rect.H <= 0 {
		return sdl.Rect{}
	}
	if h.dynCarousel() {
		return item.Rect
	}
	return glassIntersect(item.Rect, h.dynViewport())
}

// dynTileAt is the tile under a point, or "".
func (h *HomeLayout) dynTileAt(x, y int32) HomeFocusID {
	if !h.dynActive() {
		return ""
	}
	if h.dynCarousel() {
		return h.carTileAt(x, y)
	}
	for _, t := range h.dyn.tiles {
		if r := h.dynHitRect(t.ID); r.W > 0 && pointInRect(x, y, r) {
			return t.ID
		}
	}
	return ""
}

// dynFocusHover is FocusAt for a tile: the pointer focuses a grid tile it is
// over (inside the body), never a carousel card — the row would slide out
// from under the pointer. handled=false lets FocusAt try the next item.
func (h *HomeLayout) dynFocusHover(id HomeFocusID, x, y int32) (handled, focused bool) {
	if !h.dynIsTile(id) {
		return false, false
	}
	if h.dynCarousel() {
		return true, false
	}
	r := h.dynHitRect(id)
	if r.W <= 0 || !pointInRect(x, y, r) || !h.dynVisible(h.dynTileIndex(id)) {
		// A tile still sliding out of a scroll is not taken: the body would
		// scroll straight back to it (dynKeepFocusShown).
		return true, false
	}
	return true, h.Focus.Set(id)
}

// ── Moving the focus ──────────────────────────────────────────────────────

// dynMove is Move for the dynamic layouts.
func (h *HomeLayout) dynMove(direction FocusDirection) HomeFocusID {
	cur := h.Focus.Current()
	if h.dynIsTile(cur) {
		if next, ok := h.dynStepFrom(cur, direction); ok {
			if next != cur && h.Focus.Set(next) {
				h.Focus.Remember()
				h.dynEnsureVisible(next)
			}
			return h.Focus.Current()
		}
		var target HomeFocusID
		switch direction {
		case FocusUp:
			target = h.dynHeaderAbove(cur)
		case FocusDown:
			target = h.dynFooterBelow(cur)
		}
		if target != "" && h.Focus.Set(target) {
			h.Focus.Remember()
		}
		return h.Focus.Current()
	}
	// From the footer pills Up takes the tile drawn above them.
	if direction == FocusUp && (cur == HomeFocusExit || cur == HomeFocusEditPill) {
		if id := h.dynTileAbove(h.Items[cur].Rect); id != "" && h.Focus.Set(id) {
			h.Focus.Remember()
			return id
		}
		return cur
	}
	// The header band's controls sit right over the body: Down enters it at
	// the tile under them.
	if direction == FocusDown {
		switch cur {
		case HomeFocusSearch, HomeFocusPS4, HomeFocusPS5, HomeFocusQR:
			if id := h.dynEntryBelow(h.Items[cur].Rect); id != "" && h.Focus.Set(id) {
				h.Focus.Remember()
				h.dynEnsureVisible(id)
				return id
			}
			return cur
		}
	}
	next := h.Focus.Move(direction)
	footer := cur == HomeFocusExit || cur == HomeFocusEditPill
	if direction == FocusDown && !footer && (next == cur || homeIsBodyCard(next) || h.dynIsTile(next) ||
		next == HomeFocusExit || next == HomeFocusEditPill) {
		// The graph went for a dashboard card (or ran out): enter the body
		// under the control instead.
		if id := h.dynEntryBelow(h.Items[cur].Rect); id != "" && h.Focus.Set(id) {
			h.Focus.Remember()
			h.dynEnsureVisible(id)
			return id
		}
		if next != cur && !h.dynIsTile(next) && next != HomeFocusExit && next != HomeFocusEditPill {
			h.Focus.Set(cur)
		}
	}
	return h.Focus.Current()
}

// dynStepFrom is one D-pad step inside the body: the grid by geometry, the
// carousel round its ring. ok=false means the press leaves the body (Up and
// Down at its edges); at a side edge the focus stays.
func (h *HomeLayout) dynStepFrom(cur HomeFocusID, direction FocusDirection) (HomeFocusID, bool) {
	i := h.dynTileIndex(cur)
	if i < 0 {
		return "", false
	}
	if h.dynCarousel() {
		n := len(h.dyn.tiles)
		switch direction {
		case FocusLeft:
			return h.dyn.tiles[(i-1+n)%n].ID, true
		case FocusRight:
			return h.dyn.tiles[(i+1)%n].ID, true
		}
		return "", false
	}
	next := h.dynGridNeighbor(i, direction, -1)
	if next >= 0 {
		return h.dyn.tiles[next].ID, true
	}
	if direction == FocusLeft || direction == FocusRight {
		return cur, true
	}
	return "", false
}

// dynGridNeighbor is the tile a press from tile i reaches by the settled
// geometry: the nearest one lying wholly past i's edge in that direction,
// preferring one that shares its row (or column) — homeGridStep's rule. skip
// is a tile to leave out (the one being carried), -1 for none. -1 when there
// is none.
func (h *HomeLayout) dynGridNeighbor(i int, direction FocusDirection, skip int) int {
	const tol = 4
	f := h.dynTargetRect(i)
	best, bestScore := -1, int64(math.MaxInt64)
	for j := range h.dyn.tiles {
		if j == i || j == skip {
			continue
		}
		r := h.dynTargetRect(j)
		var gap, offset int32
		switch direction {
		case FocusRight:
			if r.X < f.X+f.W-tol {
				continue
			}
			gap, offset = r.X-(f.X+f.W), spanOffset(f.Y, f.H, r.Y, r.H)
		case FocusLeft:
			if r.X+r.W > f.X+tol {
				continue
			}
			gap, offset = f.X-(r.X+r.W), spanOffset(f.Y, f.H, r.Y, r.H)
		case FocusDown:
			if r.Y < f.Y+f.H-tol {
				continue
			}
			gap, offset = r.Y-(f.Y+f.H), spanOffset(f.X, f.W, r.X, r.W)
		case FocusUp:
			if r.Y+r.H > f.Y+tol {
				continue
			}
			gap, offset = f.Y-(r.Y+r.H), spanOffset(f.X, f.W, r.X, r.W)
		default:
			return -1
		}
		if gap < 0 {
			gap = 0
		}
		score := int64(gap) + 2*int64(offset)
		if score < bestScore {
			best, bestScore = j, score
		}
	}
	return best
}

// homeDynSideScore is how far apart two rects are sideways, for the presses
// that leave or enter the body: how far their spans miss each other first,
// then how far their centres are apart, so of two controls that both sit over
// a wide tile the one over its middle wins — and Down from it comes back.
func homeDynSideScore(a, b sdl.Rect) int64 {
	d := int64((a.X + a.W/2) - (b.X + b.W/2))
	if d < 0 {
		d = -d
	}
	return 4*int64(spanOffset(a.X, a.W, b.X, b.W)) + d/8
}

// dynEntryBelow is the tile Down from a control at r lands on: the
// carousel's focused card, or the grid tile in the top shown row nearest r
// sideways.
func (h *HomeLayout) dynEntryBelow(r sdl.Rect) HomeFocusID {
	if !h.dynActive() || len(h.dyn.tiles) == 0 {
		return ""
	}
	if h.dynCarousel() {
		if h.dyn.carIdx >= 0 && h.dyn.carIdx < len(h.dyn.tiles) {
			return h.dyn.tiles[h.dyn.carIdx].ID
		}
		return h.dyn.tiles[0].ID
	}
	best, bestScore := -1, int64(math.MaxInt64)
	for i, t := range h.dyn.tiles {
		if !h.dynVisible(i) {
			continue
		}
		tr := h.dynTargetRect(i)
		row := t.Cell.Row
		if row < h.dyn.first {
			row = h.dyn.first
		}
		score := int64(row-h.dyn.first)*100000 + homeDynSideScore(r, tr)
		if score < bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		return ""
	}
	return h.dyn.tiles[best].ID
}

// dynTileAbove is the shown tile nearest above a footer control at r.
func (h *HomeLayout) dynTileAbove(r sdl.Rect) HomeFocusID {
	if !h.dynActive() || len(h.dyn.tiles) == 0 {
		return ""
	}
	if h.dynCarousel() {
		return h.dynEntryBelow(r)
	}
	best, bestScore := -1, int64(math.MaxInt64)
	for i := range h.dyn.tiles {
		if !h.dynVisible(i) {
			continue
		}
		tr := glassIntersect(h.dynTargetRect(i), h.dynViewport())
		if tr.W <= 0 || tr.Y+tr.H > r.Y+4 {
			continue
		}
		score := int64(r.Y-(tr.Y+tr.H)) + 2*homeDynSideScore(r, tr)
		if score < bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		return ""
	}
	return h.dyn.tiles[best].ID
}

// dynHeaderAbove is where Up from a top tile goes: the header band's
// control nearest it sideways (the search pill, the Remote Play launchers,
// the pairing code), or failing those the nearest control of the status row.
func (h *HomeLayout) dynHeaderAbove(cur HomeFocusID) HomeFocusID {
	r := h.Items[cur].Rect
	pick := func(ids []HomeFocusID) HomeFocusID {
		best, bestScore := HomeFocusID(""), int64(math.MaxInt64)
		for _, id := range ids {
			if !h.Focus.IsEnabled(id) {
				continue
			}
			c := h.Items[id].Rect
			if c.W <= 0 || c.H <= 0 {
				continue
			}
			score := homeDynSideScore(r, c)
			if score < bestScore {
				best, bestScore = id, score
			}
		}
		return best
	}
	if id := pick([]HomeFocusID{HomeFocusSearch, HomeFocusPS4, HomeFocusPS5, HomeFocusQR}); id != "" {
		return id
	}
	return pick([]HomeFocusID{HomeFocusBrightness, HomeFocusVolume, HomeFocusWiFi, HomeFocusWeather,
		HomeFocusClock, HomeFocusNowPlaying, HomeFocusUpdate, HomeFocusTrophy, HomeFocusBell})
}

// dynFooterBelow is where Down from a bottom tile goes: the power or edit
// pill, from a tile that starts over their end of the footer (the dashboard's
// rule for Favorites and the power button); "" elsewhere.
func (h *HomeLayout) dynFooterBelow(cur HomeFocusID) HomeFocusID {
	f := h.Items[cur].Rect
	if h.dynCarousel() {
		for _, id := range []HomeFocusID{HomeFocusExit, HomeFocusEditPill} {
			if h.Focus.IsEnabled(id) && h.Items[id].Rect.W > 0 {
				return id
			}
		}
		return ""
	}
	best, bestScore := HomeFocusID(""), int64(math.MaxInt64)
	for _, id := range []HomeFocusID{HomeFocusExit, HomeFocusEditPill} {
		if !h.Focus.IsEnabled(id) {
			continue
		}
		p := h.Items[id].Rect
		if p.W <= 0 || f.X > p.X+p.W {
			continue
		}
		score := homeDynSideScore(f, p)
		if score < bestScore {
			best, bestScore = id, score
		}
	}
	return best
}

// dynWheel scrolls the grid a row per notch (up is positive), or turns the
// carousel a card. It reports whether the wheel was used.
func (h *HomeLayout) dynWheel(dy int32) bool {
	if !h.dynActive() || dy == 0 {
		return false
	}
	if h.dynCarousel() {
		n := len(h.dyn.tiles)
		if n == 0 {
			return false
		}
		step := 1
		if dy > 0 {
			step = -1
		}
		i := h.dynTileIndex(h.Focus.Current())
		if i < 0 {
			i = h.dyn.carIdx
		}
		next := (i + step + n) % n
		if h.Focus.Set(h.dyn.tiles[next].ID) {
			h.Focus.Remember()
		}
		h.dynEnsureVisible(h.dyn.tiles[next].ID)
		PlayNavSound()
		return true
	}
	if h.dyn.rows <= homeGridRowsShown {
		return false
	}
	before := h.dyn.first
	if dy > 0 {
		h.dyn.first--
	} else {
		h.dyn.first++
	}
	h.dynClampScroll()
	if h.dyn.first == before {
		return false
	}
	// A focused tile the scroll took off screen hands the focus to the
	// nearest one still shown, in reading order: A and Y must never act on a
	// tile nobody can see.
	if i := h.dynTileIndex(h.Focus.Current()); i >= 0 && !h.dynVisible(i) {
		pick := -1
		for j := range h.dyn.tiles {
			if !h.dynVisible(j) {
				continue
			}
			if pick < 0 || homeAbsInt(j-i) < homeAbsInt(pick-i) {
				pick = j
			}
		}
		if pick >= 0 && h.Focus.Set(h.dyn.tiles[pick].ID) {
			h.Focus.Remember()
		}
	}
	return true
}

// ── Drawing ───────────────────────────────────────────────────────────────

// dynRender draws the dynamic body. Render calls it before the header and
// footer controls; focused is the focused ID and lift its eased hover lift.
func (h *HomeLayout) dynRender(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData, focused HomeFocusID, lift float64) {
	if renderer == nil || !h.dynActive() {
		return
	}
	if homeEdit.open && homeEdit.strip {
		// The strip has the focus: no tile wears the ring.
		focused = ""
	}
	if h.dynCarousel() {
		h.carRender(renderer, fonts, data, focused, lift)
		return
	}
	hadClip := renderer.IsClipEnabled()
	prevClip := renderer.GetClipRect()
	clip := h.dynClip()
	if hadClip {
		clip = glassIntersect(prevClip, clip)
	}
	_ = renderer.SetClipRect(&clip)

	drawTile := func(i int) {
		t := h.dyn.tiles[i]
		item := h.Items[t.ID]
		if item.Rect.W <= 0 || item.Rect.H <= 0 || !homeRectsOverlap(item.Rect, clip) {
			return
		}
		isFocus := t.ID == focused
		cardLift := 0.0
		if isFocus {
			cardLift = lift
		}
		pressed := data.Pressed == t.ID
		pressDepth := 0.0
		if pressed {
			pressDepth = data.PressDepth
		}
		held := homeEdit.open && homeEdit.holding && isFocus
		dragged := homeEditDragging(t.ID)
		if dragged {
			// The carried tile follows the pointer; its slot stays empty.
			item.Rect = homeEditDragRect(item.Rect)
		}
		h.dynPaintTile(renderer, fonts, data, t, item, isFocus, pressed, pressDepth, cardLift, held || dragged)
		_ = renderer.SetClipRect(&clip)
	}
	focusIdx := -1
	for i, t := range h.dyn.tiles {
		if t.ID == focused || homeEditDragging(t.ID) {
			focusIdx = i
			continue
		}
		drawTile(i)
	}
	// The focused (or carried) tile last, so its lift and ring are on top.
	if focusIdx >= 0 {
		drawTile(focusIdx)
	}

	if hadClip {
		_ = renderer.SetClipRect(&prevClip)
	} else {
		_ = renderer.SetClipRect(nil)
	}
	h.dynDrawScrollbar(renderer)
}

// dynPaintTile paints one tile with the painter its kind uses, then the
// edit mode's badges. item.Rect is where it sits (scrolled, eased); the
// painters draw the lift and the press from it.
func (h *HomeLayout) dynPaintTile(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData, t homeDynTile, item HomeItem, focused, pressed bool, pressDepth, lift float64, held bool) {
	if held {
		// A carried tile stands higher than a focused one and casts a shadow.
		lift = 1
		grow := scaleX(6, h.Width)
		shadow := sdl.Rect{X: item.Rect.X - grow + scaleX(4, h.Width), Y: item.Rect.Y - grow + scaleY(10, h.Height),
			W: item.Rect.W + 2*grow, H: item.Rect.H + 2*grow}
		fillRoundedRectAt(renderer, item.Rect, shadow, homeTileRadius, sdl.Color{R: 0, G: 0, B: 0, A: 80})
	}
	// Continue and Media paint themselves from item.Rect and the lift, so a
	// carried one is not grown again here: its badges must sit on its corner.
	ownShape := t.Kind == homeTileCard && (t.ID == HomeFocusContinue || t.ID == HomeFocusMedia)
	draw := scaleHomeCard(item.Rect, lift)
	if held && !ownShape {
		grow := scaleX(4, h.Width)
		draw = sdl.Rect{X: draw.X - grow, Y: draw.Y - grow, W: draw.W + 2*grow, H: draw.H + 2*grow}
	}
	draw.Y += homePressOffset(pressDepth, h.Height)
	switch t.Kind {
	case homeTileCard:
		switch t.ID {
		case HomeFocusContinue:
			h.drawContinueCard(renderer, fonts, item, focused, pressed, pressDepth, data.Continue, lift)
		case HomeFocusMedia:
			h.drawMediaCard(renderer, fonts, item, focused, pressed, pressDepth, lift)
		default:
			h.drawHomeAppTile(renderer, fonts, item, draw, focused, pressed, false)
		}
	case homeTileWidget:
		if t.Dim {
			h.drawHomeAppTile(renderer, fonts, item, draw, focused, pressed, true)
			break
		}
		h.drawHomeWidget(renderer, fonts, homeTileWidgetName(t.Ref), item, draw, focused, pressed)
	case homeTileApp:
		h.drawHomeAppTile(renderer, fonts, item, draw, focused, pressed, t.Dim)
	case homeTileAdd:
		h.drawEditAddTile(renderer, fonts, item, draw, focused, pressed)
	}
	if homeEdit.open && t.Kind != homeTileAdd {
		h.drawEditBadges(renderer, fonts, t, draw, !h.dynCarousel() && focused && !homeEdit.strip)
	}
}

// dynDrawScrollbar draws the grid's scroll position in the right margin when
// it has more rows than the body shows.
func (h *HomeLayout) dynDrawScrollbar(renderer *sdl.Renderer) {
	rows := h.dyn.rows
	if rows <= homeGridRowsShown {
		return
	}
	// In the margin past the clip, so a right-hand tile's ring and remove
	// badge are never painted over.
	track := ScaleHomeRect(sdl.Rect{X: homeDynBodyX + homeDynBodyW + homeDynClipPadEdit + 1, Y: homeDynBodyTop, W: 4, H: homeDynBodyH}, h.Width, h.Height)
	if track.W < 2 {
		track.W = 2
	}
	fillRoundedRect(renderer, track.X, track.Y, track.W, track.H, track.W/2, homeGlass(34))
	thumbH := track.H * int32(homeGridRowsShown) / int32(rows)
	if thumbH < track.W*4 {
		thumbH = track.W * 4
	}
	maxFirst := float64(rows - homeGridRowsShown)
	frac := 0.0
	if maxFirst > 0 {
		frac = h.dyn.scroll / maxFirst
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	y := track.Y + int32(frac*float64(track.H-thumbH))
	fillRoundedRect(renderer, track.X, y, track.W, thumbH, track.W/2, homeGlass(140))
}
