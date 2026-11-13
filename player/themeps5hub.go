package main

// themeps5hub.go — PS5 Style's Home: the welcome hub.
//
// The Carousel arrangement's "hub" style (themeconsole.go) lays Home out the
// way the console's home screen is laid out:
//
//   - "Games  Media" heads the page where the greeting was, Games lit; a tap
//     on Media opens Media.
//   - The tile list is one row of small rounded icon tiles under the header.
//     The focused tile stands larger at a fixed anchor with its name beside
//     it, under the small tiles. The row slides under the anchor and ends at
//     both sides instead of turning round.
//   - Under the row, two pills (trophy progress, the game library) and a
//     board of flat graphite cards in four columns: the battery, the Continue
//     card with its artwork, Chat, Trophies, the last game played, Packages,
//     Storage, Now playing and today's playtime.
//
// The pills and cards are focus stops of their own ("hub:" IDs, added beside
// the tiles by dynRebuild through hubRebuild): Down from the row enters them,
// the D-pad walks them by their drawn geometry, Up from the pills returns to
// the row's focused tile and Down from the bottom cards reaches the footer.
// A takes each to what it shows (homeHubActivate). The board is fixed: Edit
// Home arranges the row, and the board is hidden while it is open.
//
// Colour, shape and arrangement only, as with every console look: no logos,
// fonts or artwork of the console's own.

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// The row in 1280x720 design units: the focused tile at the anchor, the
// small tiles hanging from the same top line beside it.
const (
	homeHubAnchorX = 96.0
	homeHubRowTop  = 166.0
	homeHubBig     = 112.0
	homeHubSmall   = 80.0
	homeHubGap     = 10.0
	// homeHubReachL is how far left of the focus a tile is still drawn: the
	// one before it peeks in at the screen's edge.
	homeHubReachL = 1.5
)

// The board in design units: the pill band, then four columns of cards one or
// two rows tall, from under the focused tile to the right margin. Three card
// rows end at 638, clear of the footer.
const (
	homeHubBoardX  int32 = 96
	homeHubBoardW  int32 = 1152
	homeHubPillY   int32 = 296
	homeHubPillH   int32 = 36
	homeHubCardTop int32 = 346
	homeHubCardH   int32 = 88
	homeHubRowGap  int32 = 14
	homeHubColGap  int32 = 16
	homeHubCols          = 4
	homeHubPad     int32 = 13
)

// homeHubIDPrefix marks the board's focus IDs.
const homeHubIDPrefix = "hub:"

// The board's focus stops.
const (
	homeHubProgress HomeFocusID = homeHubIDPrefix + "progress"
	homeHubExplore  HomeFocusID = homeHubIDPrefix + "explore"
	homeHubBattery  HomeFocusID = homeHubIDPrefix + "battery"
	homeHubContinue HomeFocusID = homeHubIDPrefix + "continue"
	homeHubChat     HomeFocusID = homeHubIDPrefix + "chat"
	homeHubTrophies HomeFocusID = homeHubIDPrefix + "trophies"
	homeHubRecent   HomeFocusID = homeHubIDPrefix + "recent"
	homeHubStore    HomeFocusID = homeHubIDPrefix + "store"
	homeHubStorage  HomeFocusID = homeHubIDPrefix + "storage"
	homeHubMusic    HomeFocusID = homeHubIDPrefix + "music"
	homeHubPlaytime HomeFocusID = homeHubIDPrefix + "playtime"
)

// Shape-cache kinds of the hub's gradient faces (shapeCache.go).
const (
	homeHubShapeTile = int32(9401)
	homeHubShapeArt  = int32(9402)
)

// homeHubSpec is one pill or card of the board.
type homeHubSpec struct {
	id HomeFocusID
	// col and row are a card's cell (row -1 is the pill band); rows is how
	// many card rows it spans.
	col, row, rows int
	// pillX and pillW place a pill along its band.
	pillX, pillW int32
	title, sub   string
	icon         string
}

// homeHubSpecs is the board, pills first, then the cards column by column.
// The order is also the tie-break when two stops are equally near.
var homeHubSpecs = [...]homeHubSpec{
	{id: homeHubProgress, row: -1, pillX: 96, pillW: 270, title: "Trophy progress", sub: "Trophies earned", icon: "trophy"},
	{id: homeHubExplore, row: -1, pillX: 380, pillW: 330, title: "Game library", sub: "Explore your games", icon: "plus"},
	{id: homeHubBattery, col: 0, row: 0, rows: 1, title: "Battery", sub: "Charge level", icon: "battery"},
	{id: homeHubContinue, col: 0, row: 1, rows: 2, title: "Continue", sub: "Resume your latest activity", icon: "continue"},
	{id: homeHubChat, col: 1, row: 0, rows: 1, title: "Chat", sub: "Messages and servers", icon: "chat"},
	{id: homeHubTrophies, col: 1, row: 1, rows: 1, title: "Trophies", sub: "Earned by tier", icon: "trophy"},
	{id: homeHubRecent, col: 1, row: 2, rows: 1, title: "Recently played", sub: "Your last game", icon: "history"},
	{id: homeHubStore, col: 2, row: 0, rows: 2, title: "Packages", sub: "Apps, games and emulators", icon: "packages"},
	{id: homeHubStorage, col: 2, row: 2, rows: 1, title: "Storage", sub: "Free space", icon: "disk"},
	{id: homeHubMusic, col: 3, row: 0, rows: 2, title: "Now playing", sub: "Music and videos", icon: "music"},
	{id: homeHubPlaytime, col: 3, row: 2, rows: 1, title: "Playtime", sub: "Played today", icon: "clock"},
}

// homeHubDesignRect is a pill's or card's rect in design units.
func homeHubDesignRect(s homeHubSpec) sdl.Rect {
	if s.row < 0 {
		return sdl.Rect{X: s.pillX, Y: homeHubPillY, W: s.pillW, H: homeHubPillH}
	}
	colW := (homeHubBoardW - int32(homeHubCols-1)*homeHubColGap) / int32(homeHubCols)
	rows := int32(max(s.rows, 1))
	return sdl.Rect{
		X: homeHubBoardX + int32(s.col)*(colW+homeHubColGap),
		Y: homeHubCardTop + int32(s.row)*(homeHubCardH+homeHubRowGap),
		W: colW,
		H: rows*homeHubCardH + (rows-1)*homeHubRowGap,
	}
}

// homeHub is the row's slide and the board's bookkeeping.
var homeHub struct {
	pos    float64
	tick   uint64
	placed bool
	anim   bool
	// items says the board's stops are among Home's items (hubRebuild).
	items bool
	// tabMedia is where the Media tab was drawn this frame: a tap opens it.
	tabMedia sdl.Rect
}

// hubActive reports whether Home is drawing the welcome hub.
func (h *HomeLayout) hubActive() bool {
	return h != nil && homeConsoleLook.carousel == homeCarouselHub && h.dynCarousel()
}

// homeHubID reports whether id names one of the board's stops, whether or
// not the board is up.
func homeHubID(id HomeFocusID) bool {
	return strings.HasPrefix(string(id), homeHubIDPrefix)
}

// hubIsItem reports whether id is one of the board's stops right now.
func (h *HomeLayout) hubIsItem(id HomeFocusID) bool {
	if h == nil || !homeHub.items || !homeHubID(id) {
		return false
	}
	_, ok := h.Items[id]
	return ok
}

// hubRebuild gives the board's pills and cards their items while the hub is
// the Carousel's style, and returns their IDs for Order and the focus
// controller — nil on any other layout and while Home is being edited (the
// row is what Edit Home arranges). dynRebuild calls it after dropping the
// last build's items.
func (h *HomeLayout) hubRebuild() []HomeFocusID {
	homeHub.items = false
	if h == nil || homeConsoleLook.carousel != homeCarouselHub || h.Arrangement != homeArrangementCarousel || homeEdit.open {
		return nil
	}
	// On a canvas taller than it is wide for the design (4:3) the row's
	// square tiles come out shorter than the design's; the board moves up by
	// as much, so it keeps its gap under the row.
	_, sy, k := h.hubRowScale()
	up := int32(math.Round(homeHubBig * math.Max(sy-k, 0)))
	ids := make([]HomeFocusID, 0, len(homeHubSpecs))
	for _, s := range homeHubSpecs {
		rect := ScaleHomeRect(homeHubDesignRect(s), h.Width, h.Height)
		rect.Y -= up
		h.Items[s.id] = HomeItem{ID: s.id, Title: s.title, Subtitle: s.sub, Icon: s.icon, Rect: rect}
		ids = append(ids, s.id)
	}
	homeHub.items = true
	return ids
}

// ── The row ──────────────────────────────────────────────────────────────

// homeHubRowGeom is a row tile's rect in design units at offset o from the
// focus: the focused tile big at the anchor, the ones after it small to its
// right, the ones before it small to its left; a tile changing places grows
// or shrinks on the way.
func homeHubRowGeom(o float64) (x, y, w, hh float64) {
	size := homeHubBig - (homeHubBig-homeHubSmall)*math.Min(math.Abs(o), 1)
	switch {
	case o >= 1:
		x = homeHubAnchorX + homeHubBig + homeHubGap + (o-1)*(homeHubSmall+homeHubGap)
	case o >= 0:
		x = homeHubAnchorX + o*(homeHubBig+homeHubGap)
	default:
		x = homeHubAnchorX + o*(homeHubSmall+homeHubGap)
	}
	return x, homeHubRowTop, size, size
}

// hubRowScale is the canvas's scale from design units across and down, and
// the one the row's tiles are sized by: the smaller of the two, so a tile
// stays square on the 4:3 canvas instead of standing a third taller than it
// is wide.
func (h *HomeLayout) hubRowScale() (sx, sy, k float64) {
	sx = float64(h.Width) / float64(HomeBaseWidth)
	sy = float64(h.Height) / float64(HomeBaseHeight)
	return sx, sy, math.Min(sx, sy)
}

// hubRowRect is a row tile's rect on the canvas at offset o; false when it is
// off the screen. Places scale across with the canvas, sizes by hubRowScale.
func (h *HomeLayout) hubRowRect(o float64) (sdl.Rect, bool) {
	if o < -homeHubReachL {
		return sdl.Rect{}, false
	}
	x, y, w, _ := homeHubRowGeom(o)
	if x >= float64(HomeBaseWidth) {
		return sdl.Rect{}, false
	}
	sx, sy, k := h.hubRowScale()
	side := int32(math.Round(w * k))
	return sdl.Rect{X: int32(math.Round(x * sx)), Y: int32(math.Round(y * sy)), W: side, H: side}, true
}

// hubStep eases the row toward the focused tile; carPlace calls it every
// refresh, and a second call in the same millisecond moves nothing.
func (h *HomeLayout) hubStep() {
	s := &homeHub
	target := float64(h.dyn.carIdx)
	now := sdl.GetTicks64()
	dt := 0.0
	if s.tick != 0 && now > s.tick {
		dt = math.Min(float64(now-s.tick), 100)
	}
	s.tick = now
	if !s.placed || !h.Motion || math.Abs(target-s.pos) < 0.002 {
		s.pos, s.placed = target, true
	} else {
		s.pos = homeDynEase(s.pos, target, dt)
	}
	s.anim = s.pos != target
}

// hubStepFrom is one D-pad step from row tile i: along the row without
// turning round, Down onto the board. ok=false hands Up (and Down with no
// board) to dynMove.
func (h *HomeLayout) hubStepFrom(cur HomeFocusID, i int, direction FocusDirection) (HomeFocusID, bool) {
	n := len(h.dyn.tiles)
	switch direction {
	case FocusLeft:
		if i > 0 {
			return h.dyn.tiles[i-1].ID, true
		}
		return cur, true
	case FocusRight:
		if i < n-1 {
			return h.dyn.tiles[i+1].ID, true
		}
		return cur, true
	case FocusDown:
		if id := h.hubNeighborFrom(h.Items[cur].Rect, cur, FocusDown); id != "" {
			return id, true
		}
	}
	return "", false
}

// homeHubTint is a row tile's face: a coloured ramp for the places the row
// opens — the console's game art stands there — and graphite for the tools.
// ok=false is the plain face. The index keeps two ramps that share a top
// colour apart in the shape cache.
func homeHubTint(t homeDynTile) (top, bottom sdl.Color, index int32, ok bool) {
	ramp := func(i int32, r1, g1, b1, r2, g2, b2 uint8) (sdl.Color, sdl.Color, int32, bool) {
		return sdl.Color{R: r1, G: g1, B: b1, A: 255}, sdl.Color{R: r2, G: g2, B: b2, A: 255}, i, true
	}
	switch t.ID {
	case HomeFocusContinue:
		top, bottom = lerpColor(ColorAccent, sdl.Color{R: 0, G: 0, B: 0, A: 255}, 0.25), lerpColor(ColorAccent, sdl.Color{R: 0, G: 0, B: 0, A: 255}, 0.75)
		top.A, bottom.A = 255, 255
		return top, bottom, 1, true
	case HomeFocusMedia:
		return ramp(2, 128, 82, 236, 44, 22, 110)
	case HomeFocusEmulator:
		return ramp(3, 232, 64, 72, 112, 18, 36)
	case HomeFocusPackages:
		return ramp(4, 32, 122, 236, 10, 46, 120)
	case HomeFocusFavorites:
		return ramp(5, 236, 176, 40, 128, 74, 8)
	case HomeFocusBrowser:
		return ramp(6, 28, 170, 178, 8, 74, 88)
	case HomeFocusChat:
		return ramp(7, 92, 104, 244, 36, 42, 128)
	}
	return sdl.Color{}, sdl.Color{}, 0, false
}

// hubRenderRow draws the row — the focused tile last, on top — and the
// focused tile's name beside it once the row has stopped.
func (h *HomeLayout) hubRenderRow(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData, focused HomeFocusID, lift float64) {
	cur := h.dyn.carIdx
	for pass := 0; pass < 2; pass++ {
		for i, t := range h.dyn.tiles {
			if (i == cur) != (pass == 1) {
				continue
			}
			item := h.Items[t.ID]
			if item.Rect.W <= 0 || item.Rect.H <= 0 {
				continue
			}
			h.hubPaintRowTile(renderer, fonts, data, i, t, item, focused, lift)
		}
	}
	if cur < 0 || cur >= len(h.dyn.tiles) || !h.carSettled() {
		// The name belongs under the small tiles beside the anchor, where a
		// tile shrinking out of it is still passing while the row slides.
		return
	}
	item := h.Items[h.dyn.tiles[cur].ID]
	f := fonts.Tile
	if f == nil {
		f = fonts.Card
	}
	if f == nil || item.Title == "" || item.Rect.W <= 0 {
		return
	}
	x := item.Rect.X + item.Rect.W + scaleX(18, h.Width)
	_, _, k := h.hubRowScale()
	bandTop := item.Rect.Y + int32(math.Round(homeHubSmall*k))
	bandBottom := item.Rect.Y + item.Rect.H
	// Centred in the band under the small tiles, never up against them (the
	// band is shorter than the type on the 4:3 canvas).
	y := bandTop + max32(scaleY(2, h.Height), (bandBottom-bandTop-int32(f.Height()))/2)
	drawText(renderer, f, ellipsize(f, item.Title, scaleX(460, h.Width)), x, y, ColorTextPrimary(), textAlignLeft)
}

// hubPaintRowTile paints row tile i: its face (a coloured ramp or graphite),
// its glyph, the press and the ring, and Edit Home's badges on the carried or
// focused one. Every tile is painted from the focused tile's size (carBase),
// so a tile growing into the anchor reuses one cached face.
func (h *HomeLayout) hubPaintRowTile(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData, i int, t homeDynTile, item HomeItem, focused HomeFocusID, lift float64) {
	isCur := i == h.dyn.carIdx
	isFocus := isCur && t.ID == focused
	held := homeEdit.open && homeEdit.holding && isCur && t.ID == h.Focus.Current()
	pressed := data.Pressed == t.ID
	depth := 0.0
	if pressed {
		depth = data.PressDepth
	}
	// The focused tile's square, centred on this one: the face every tile is
	// painted from (carBase is not square on the 4:3 canvas).
	_, _, k := h.hubRowScale()
	side := int32(math.Round(homeHubBig * k))
	base := sdl.Rect{X: item.Rect.X + item.Rect.W/2 - side/2, Y: item.Rect.Y + item.Rect.H/2 - side/2, W: side, H: side}
	cardLift := 0.0
	if isFocus {
		cardLift = lift
	}
	if held {
		cardLift = 1
	}
	draw := scaleHomeCard(item.Rect, cardLift)
	if held {
		grow := scaleX(4, h.Width)
		shadow := sdl.Rect{X: draw.X - grow + scaleX(4, h.Width), Y: draw.Y - grow + scaleY(10, h.Height),
			W: draw.W + 2*grow, H: draw.H + 2*grow}
		fillRoundedRectAt(renderer, base, shadow, homeTileRadius, sdl.Color{R: 0, G: 0, B: 0, A: 90})
		draw = sdl.Rect{X: draw.X - grow, Y: draw.Y - grow, W: draw.W + 2*grow, H: draw.H + 2*grow}
	}
	draw.Y += homePressOffset(depth, h.Height)
	r := homeTileRadius
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	ink := ColorTextPrimary()
	top, bottom, index, tinted := homeHubTint(t)
	if tinted && !t.Dim {
		drawShapeCachedAt(renderer, homeHubShapeTile, base.W, base.H, draw, r, index, top,
			func(rd *sdl.Renderer, ox, oy, w, hh int32) {
				fillRoundedGradient(rd, sdl.Rect{X: ox, Y: oy, W: w, H: hh}, r, top, bottom, false)
			})
		ink = sdl.Color{R: 255, G: 255, B: 255, A: 255}
	} else {
		face := ColorSurfaceRaised
		face.A = 255
		if isFocus {
			face = lerpColor(face, ColorTextPrimary(), 0.08)
			face.A = 255
		}
		fillRoundedRectAt(renderer, base, draw, r, face)
		strokeRoundedRectAt(renderer, base, draw, r, 1, homeGlass(16))
	}
	if t.Dim {
		ink = ColorTextTertiary()
	}
	icon := item.Icon
	if icon == "" {
		icon = t.Icon
	}
	if icon == "" {
		icon = "apps"
	}
	gs := draw.W * 44 / 100
	drawHomeGlyphTinted(renderer, icon, sdl.Rect{X: draw.X + (draw.W-gs)/2, Y: draw.Y + (draw.H-gs)/2, W: gs, H: gs}, ink)
	if pressed {
		fillRoundedRectAt(renderer, base, draw, r, homeGlass(30))
	}
	if isFocus || held {
		drawHomeFocusRing(renderer, base, draw, r)
	}
	// A tile that opens Chat carries its unread count on its corner.
	if !t.Dim && homeItemOpensChat(item) {
		if c := homeHubChatNow(); c.n > 0 {
			h.hubCountBadge(renderer, fonts, draw, c.count)
		}
	}
	if homeEdit.open && t.Kind != homeTileAdd && isCur {
		h.drawEditBadges(renderer, fonts, t, draw, false)
	}
}

// hubCountBadge hangs a red count off a row tile's top-right corner.
func (h *HomeLayout) hubCountBadge(renderer *sdl.Renderer, fonts HomeFonts, tile sdl.Rect, label string) {
	f := fonts.Meta
	if f == nil {
		f = fonts.Small
	}
	if label == "" || f == nil {
		return
	}
	tw, _ := measureText(f, label)
	d := int32(f.Height()) + scaleY(4, h.Height)
	w := max32(d, tw+d/2)
	b := sdl.Rect{X: tile.X + tile.W - w + w/3, Y: tile.Y - d/3, W: w, H: d}
	fillRoundedRect(renderer, b.X, b.Y, b.W, b.H, d/2, ColorDanger)
	drawText(renderer, f, label, b.X+b.W/2, b.Y+(b.H-int32(f.Height()))/2, sdl.Color{R: 255, G: 255, B: 255, A: 255}, textAlignCenter)
}

// ── The board ────────────────────────────────────────────────────────────

// hubRender draws the welcome hub: the row and, outside Edit Home, the
// pills and the cards. carRender hands the whole body to it.
func (h *HomeLayout) hubRender(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData, focused HomeFocusID, lift float64) {
	h.hubRenderRow(renderer, fonts, data, focused, lift)
	if !homeHub.items {
		return
	}
	f, ok := hwpResolveFonts(fonts)
	if !ok {
		return
	}
	now := time.Now()
	for _, s := range homeHubSpecs {
		item, have := h.Items[s.id]
		if !have || item.Rect.W <= 0 || item.Rect.H <= 0 {
			continue
		}
		isFocus := s.id == focused
		cardLift := 0.0
		if isFocus {
			cardLift = lift
		}
		pressed := data.Pressed == s.id
		depth := 0.0
		if pressed {
			depth = data.PressDepth
		}
		draw := scaleHomeCard(item.Rect, cardLift)
		draw.Y += homePressOffset(depth, h.Height)
		if s.id == homeHubContinue {
			// The Continue card's own compact painter: the artwork (requested at
			// this card's settled size), its chip, title and progress line. Its
			// focus is the board's plain ring, without the accent glow.
			h.drawHomeContinueCompact(renderer, fonts, item, draw, data.Continue, false, pressed)
			if isFocus {
				drawHomeFocusRing(renderer, item.Rect, draw, homeTileRadius)
			}
			continue
		}
		if s.row < 0 {
			h.hubPaintPill(renderer, f, s, item.Rect, draw, isFocus, pressed)
			continue
		}
		h.hubPaintCard(renderer, f, s, item.Rect, draw, isFocus, pressed, now)
	}
}

// hubFrame paints a card's flat face, its press and its ring.
func (h *HomeLayout) hubFrame(renderer *sdl.Renderer, base, draw sdl.Rect, radius int32, focused, pressed bool) {
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	face := ColorSurface
	face.A = 255
	if pressed {
		face = ColorSurfaceRaised
		face.A = 255
	}
	fillRoundedRectAt(renderer, base, draw, radius, face)
	if focused {
		drawHomeFocusRing(renderer, base, draw, radius)
	}
}

// hubInner is a card's content box.
func (h *HomeLayout) hubInner(draw sdl.Rect) sdl.Rect {
	px, py := scaleX(homeHubPad+3, h.Width), scaleY(homeHubPad, h.Height)
	return sdl.Rect{X: draw.X + px, Y: draw.Y + py, W: draw.W - 2*px, H: draw.H - 2*py}
}

// hubHeader writes a card's glyph and name along its top, and meta (when
// set) right-aligned on the same line; it returns the line's bottom.
func (h *HomeLayout) hubHeader(renderer *sdl.Renderer, f hwpFonts, in sdl.Rect, icon, title, meta string, ink, metaInk sdl.Color) int32 {
	lineH := int32(f.tile.Height())
	gs := lineH - scaleY(4, h.Height)
	drawHomeGlyphTinted(renderer, icon, sdl.Rect{X: in.X, Y: in.Y + (lineH-gs)/2, W: gs, H: gs}, ink)
	tx := in.X + gs + scaleX(10, h.Width)
	room := in.X + in.W - tx
	if meta != "" {
		mw, _ := measureText(f.tile, meta)
		drawText(renderer, f.tile, meta, in.X+in.W, in.Y, metaInk, textAlignRight)
		room -= mw + scaleX(12, h.Width)
	}
	if room > 0 {
		drawText(renderer, f.tile, ellipsize(f.tile, title, room), tx, in.Y, ink, textAlignLeft)
	}
	return in.Y + lineH
}

// hubPaintCard paints one card of the board by what it shows.
func (h *HomeLayout) hubPaintCard(renderer *sdl.Renderer, f hwpFonts, s homeHubSpec, base, draw sdl.Rect, focused, pressed bool, now time.Time) {
	switch s.id {
	case homeHubStore:
		h.hubPaintArt(renderer, f, s, base, draw, focused, pressed, 1)
		return
	case homeHubMusic:
		h.hubPaintArt(renderer, f, s, base, draw, focused, pressed, 2)
		return
	}
	h.hubFrame(renderer, base, draw, homeTileRadius, focused, pressed)
	// The content keeps the settled size, centred in the lifted card, so no
	// ring or label changes size while the focus bounce plays.
	in := h.hubInner(h.hwpSettled(base, draw))
	if in.W <= 0 || in.H <= 0 {
		return
	}
	primary, muted := ColorTextPrimary(), ColorTextMuted()
	switch s.id {
	case homeHubBattery:
		h.hubDrawBattery(renderer, f, in)
	case homeHubChat:
		c := homeHubChatNow()
		meta := ""
		if c.n > 0 {
			meta = c.count
		}
		h.hubHeader(renderer, f, in, s.icon, s.title, meta, primary, primary)
		if c.n > 0 {
			// The console's online dot, before the count.
			mw, _ := measureText(f.tile, c.count)
			dot := scaleY(10, h.Height)
			lineH := int32(f.tile.Height())
			fillCircle(renderer, in.X+in.W-mw-scaleX(8, h.Width)-dot/2, in.Y+lineH/2, dot/2, ColorSuccess)
		}
		h.hubBottomLine(renderer, f.helper, in, c.line, muted)
	case homeHubTrophies:
		t := homeHubTrophiesNow()
		// "Total: 48", or the bare count where the narrow canvas would squeeze
		// the card's name.
		meta := t.total
		titleW, _ := measureText(f.tile, s.title)
		if metaW, _ := measureText(f.tile, meta); titleW+metaW+int32(f.tile.Height())+scaleX(26, h.Width) > in.W {
			meta = t.count
		}
		h.hubHeader(renderer, f, in, s.icon, s.title, meta, primary, primary)
		h.hubDrawTiers(renderer, f, in, t)
	case homeHubRecent:
		h.hubHeader(renderer, f, in, s.icon, s.title, "", primary, primary)
		h.hubDrawRecent(renderer, f, in, now)
	case homeHubStorage:
		h.hubHeader(renderer, f, in, s.icon, s.title, "", primary, primary)
		h.hubDrawStorage(renderer, f, in, now)
	case homeHubPlaytime:
		h.hubHeader(renderer, f, in, s.icon, s.title, "", primary, primary)
		p := homeHubPlaytimeNow(now)
		y := in.Y + in.H - int32(f.tile.Height())
		vw, _ := measureText(f.tile, p.value)
		drawText(renderer, f.tile, ellipsize(f.tile, p.value, in.W), in.X, y, primary, textAlignLeft)
		if p.note != "" && vw+scaleX(10, h.Width) < in.W {
			ny := y + int32(f.tile.Height()) - int32(f.helper.Height())
			drawText(renderer, f.helper, ellipsize(f.helper, p.note, in.W-vw-scaleX(10, h.Width)), in.X+vw+scaleX(10, h.Width), ny-scaleY(1, h.Height), muted, textAlignLeft)
		}
	}
}

// hubBottomLine writes one muted line along a card's bottom.
func (h *HomeLayout) hubBottomLine(renderer *sdl.Renderer, font *ttf.Font, in sdl.Rect, text string, ink sdl.Color) {
	if font == nil || text == "" {
		return
	}
	drawText(renderer, font, ellipsize(font, text, in.W), in.X, in.Y+in.H-int32(font.Height()), ink, textAlignLeft)
}

// hubPaintPill paints one pill of the band: trophy progress, or the way into
// the game library in the theme's warning yellow.
func (h *HomeLayout) hubPaintPill(renderer *sdl.Renderer, f hwpFonts, s homeHubSpec, base, draw sdl.Rect, focused, pressed bool) {
	radius := base.H / 2
	h.hubFrame(renderer, base, draw, radius, focused, pressed)
	lay := h.hwpSettled(base, draw)
	padX := scaleX(14, h.Width)
	x := lay.X + padX
	right := lay.X + lay.W - padX
	cy := lay.Y + lay.H/2
	lineH := int32(f.tile.Height())
	switch s.id {
	case homeHubProgress:
		t := homeHubTrophiesNow()
		rr := base.H*3/10 + 1
		width := max32(2, scaleY(3, h.Height))
		trophyDrawRing(renderer, x+rr, cy, rr, width, 1, homeGlass(46))
		if t.frac > 0 {
			trophyDrawRing(renderer, x+rr, cy, rr, width, t.frac, ColorSuccess)
		}
		x += 2*rr + scaleX(10, h.Width)
		ink := TextInk(ColorSuccess)
		drawText(renderer, f.tile, t.pct, x, cy-lineH/2, ink, textAlignLeft)
		pw, _ := measureText(f.tile, t.pct)
		x += pw + scaleX(12, h.Width)
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		setDrawColor(renderer, homeGlass(60))
		_ = renderer.FillRect(&sdl.Rect{X: x, Y: cy - lineH/2, W: max32(1, scaleX(1, h.Width)), H: lineH})
		x += scaleX(12, h.Width)
		if room := right - x; room > 0 {
			hh := int32(f.helper.Height())
			drawText(renderer, f.helper, homeHubFit(f.helper, room, s.sub, "Trophies"), x, cy-hh/2, ColorTextPrimary(), textAlignLeft)
		}
	case homeHubExplore:
		ink := TextInk(ColorWarning)
		gs := lineH - scaleY(2, h.Height)
		drawHomeGlyphTinted(renderer, "plus", sdl.Rect{X: x, Y: cy - gs/2, W: gs, H: gs}, ColorWarning)
		x += gs + scaleX(10, h.Width)
		if room := right - x; room > 0 {
			hh := int32(f.helper.Height())
			drawText(renderer, f.helper, homeHubFit(f.helper, room, "Explore your game library", s.title), x, cy-hh/2, ink, textAlignLeft)
		}
	}
}

// hubPaintArt paints a tall card with a coloured face — Packages in the
// accent's blue, Now playing in violet — the way the console's store and news
// cards carry artwork: a large faint glyph, the header in white, a line of
// title and a muted line under it.
func (h *HomeLayout) hubPaintArt(renderer *sdl.Renderer, f hwpFonts, s homeHubSpec, base, draw sdl.Rect, focused, pressed bool, index int32) {
	black := sdl.Color{R: 0, G: 0, B: 0, A: 255}
	var top, bottom sdl.Color
	if index == 1 {
		top, bottom = lerpColor(ColorAccent, black, 0.3), lerpColor(ColorAccent, black, 0.82)
	} else {
		top, bottom = sdl.Color{R: 118, G: 70, B: 222, A: 255}, sdl.Color{R: 30, G: 14, B: 72, A: 255}
	}
	top.A, bottom.A = 255, 255
	r := homeTileRadius
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	drawShapeCachedAt(renderer, homeHubShapeArt, base.W, base.H, draw, r, index, top,
		func(rd *sdl.Renderer, ox, oy, w, hh int32) {
			fillRoundedGradient(rd, sdl.Rect{X: ox, Y: oy, W: w, H: hh}, r, top, bottom, false)
		})
	white := sdl.Color{R: 255, G: 255, B: 255, A: 255}
	soft := sdl.Color{R: 255, G: 255, B: 255, A: 205}
	// The watermark: the card's glyph, large and faint, in its far corner.
	wm := draw.H * 58 / 100
	drawHomeGlyphTinted(renderer, s.icon, sdl.Rect{X: draw.X + draw.W - wm - scaleX(14, h.Width), Y: draw.Y + draw.H/2 - wm/2 - scaleY(6, h.Height), W: wm, H: wm},
		sdl.Color{R: 255, G: 255, B: 255, A: 34})
	if pressed {
		fillRoundedRectAt(renderer, base, draw, r, homeGlass(30))
	}
	if focused {
		drawHomeFocusRing(renderer, base, draw, r)
	}
	in := h.hubInner(h.hwpSettled(base, draw))
	if in.W <= 0 || in.H <= 0 {
		return
	}
	title, line := "Find something new", s.sub
	if s.id == homeHubMusic {
		title, line = homeHubMusicNow()
	}
	h.hubHeader(renderer, f, in, s.icon, s.title, "", white, white)
	lineH := int32(f.helper.Height())
	y := in.Y + in.H - lineH
	drawText(renderer, f.helper, ellipsize(f.helper, line, in.W), in.X, y, soft, textAlignLeft)
	titleH := int32(f.card.Height())
	y -= titleH + scaleY(4, h.Height)
	// Two lines when the title needs them and the card has the room.
	if l1, l2 := hwpWrap2(f.card, title, in.W); l2 != "" && y-titleH > in.Y+int32(f.tile.Height())+scaleY(8, h.Height) {
		drawText(renderer, f.card, l1, in.X, y-titleH, white, textAlignLeft)
		drawText(renderer, f.card, ellipsize(f.card, l2, in.W), in.X, y, white, textAlignLeft)
		return
	}
	drawText(renderer, f.card, ellipsize(f.card, title, in.W), in.X, y, white, textAlignLeft)
}

// ── Card contents ────────────────────────────────────────────────────────

// homeHubBatt is the battery card's strings, made per reading.
var homeHubBatt struct {
	made     bool
	pct      int
	charging bool
	pad      bool
	line     string
	short    string
	name     string
	nameAt   time.Time
}

// homeHubDeviceName names what the battery card reads: the handheld by its
// model, a PC by that word, or — with no battery to show — the controller.
func homeHubDeviceName(battery bool) string {
	if d := DetectDevice(); d.IsTrimui() {
		name := deviceDisplayName(d)
		if cut, _, ok := strings.Cut(name, " ("); ok {
			name = cut
		}
		return name
	}
	if !battery && gameController != nil {
		if name := strings.TrimSpace(gameController.Name()); name != "" {
			return name
		}
	}
	return "This PC"
}

// hubDrawBattery paints the battery card: a ring gauge in the status bar's
// level colours round a controller glyph, the device's name and the level.
func (h *HomeLayout) hubDrawBattery(renderer *sdl.Renderer, f hwpFonts, in sdl.Rect) {
	c := &homeHubBatt
	pct := getBatteryPercent()
	if pct > 100 {
		pct = 100
	}
	charging := pct >= 0 && getBatteryCharging()
	pad := gameController != nil
	if !c.made || c.pct != pct || c.charging != charging || c.pad != pad || time.Since(c.nameAt) > 5*time.Second {
		c.made, c.pct, c.charging, c.pad, c.nameAt = true, pct, charging, pad, time.Now()
		c.name = homeHubDeviceName(pct >= 0)
		switch {
		case pct >= 0 && charging:
			c.line, c.short = strconv.Itoa(pct)+"% · Charging", strconv.Itoa(pct)+"%"
		case pct >= 0:
			c.line, c.short = strconv.Itoa(pct)+"% · On battery", strconv.Itoa(pct)+"%"
		case pad:
			c.line, c.short = "Connected · No battery to read", "Connected"
		default:
			c.line, c.short = "No battery · Plugged in", "Plugged in"
		}
	}
	rr := min32(in.H/2, scaleX(34, h.Width))
	width := max32(3, scaleY(5, h.Height))
	cx, cy := in.X+rr, in.Y+in.H/2
	trophyDrawRing(renderer, cx, cy, rr, width, 1, homeGlass(40))
	if pct >= 0 {
		trophyDrawRing(renderer, cx, cy, rr, width, float64(max(pct, 1))/100, hwpLevelInk(pct))
	} else if pad {
		trophyDrawRing(renderer, cx, cy, rr, width, 1, ColorSuccess)
	}
	glyph := "gamepad"
	if pct >= 0 && !DetectDevice().IsTrimui() {
		glyph = "battery"
	}
	gs := rr
	drawHomeGlyphTinted(renderer, glyph, sdl.Rect{X: cx - gs/2, Y: cy - gs/2, W: gs, H: gs}, ColorTextPrimary())
	if charging {
		bs := gs / 2
		drawHomeGlyphTinted(renderer, "bolt", sdl.Rect{X: cx - bs/2, Y: cy + gs/2 - scaleY(2, h.Height), W: bs, H: bs}, ColorSuccess)
	}
	x := cx + rr + scaleX(16, h.Width)
	room := in.X + in.W - x
	if room <= 0 {
		return
	}
	// The name in the title face, or the smaller one where the narrow
	// canvas would cut "TrimUI Smart Pro" short.
	nameFont := f.tile
	if nw, _ := measureText(f.tile, c.name); nw > room {
		nameFont = f.helper
	}
	nameH, helpH := int32(nameFont.Height()), int32(f.helper.Height())
	top := cy - (nameH+helpH+scaleY(2, h.Height))/2
	drawText(renderer, nameFont, ellipsize(nameFont, c.name, room), x, top, ColorTextPrimary(), textAlignLeft)
	line := homeHubFit(f.helper, room, c.line, c.short)
	drawText(renderer, f.helper, line, x, top+nameH+scaleY(2, h.Height), ColorTextMuted(), textAlignLeft)
}

// homeHubFit is the first of texts that fits maxW in font, or the last one
// ellipsized to it.
func homeHubFit(font *ttf.Font, maxW int32, texts ...string) string {
	for _, t := range texts {
		if w, _ := measureText(font, t); w <= maxW {
			return t
		}
	}
	if len(texts) == 0 {
		return ""
	}
	return ellipsize(font, texts[len(texts)-1], maxW)
}

// homeHubChatText is the chat card's strings, made per count.
type homeHubChatText struct {
	made  bool
	n     int
	count string
	line  string
}

var homeHubChatCache homeHubChatText

// homeHubChatNow is the chat card's count and line.
func homeHubChatNow() *homeHubChatText {
	c := &homeHubChatCache
	n := chatUnreadTotal()
	if c.made && c.n == n {
		return c
	}
	c.made, c.n = true, n
	c.count = chatBadgeLabel(n)
	c.line = "Messages and servers"
	if n > 0 {
		c.line = chatUnreadSubtitle(n)
	}
	return c
}

// homeHubTrophyText is the trophies' counts and strings, made per change of
// the snapshot or of what this setup offers.
type homeHubTrophyText struct {
	made  bool
	rev   uint64
	gen   uint64
	known bool
	// counted is what the strings were made from: the earned count by tier,
	// then earned and shown overall.
	counted [trophyTierCount + 2]int
	tiers   [trophyTierCount]string
	total   string
	count   string
	pct     string
	frac    float64
}

var homeHubTrophyCache homeHubTrophyText

// homeHubTrophiesNow counts the earned trophies by tier the way the trophy
// room does (trophyShown): what the setup cannot award counts for nothing.
func homeHubTrophiesNow() *homeHubTrophyText {
	c := &homeHubTrophyCache
	if c.made && c.rev == playtime.rev && c.gen == trophyAvail.gen && c.known == trophyAvail.known {
		return c
	}
	first := !c.made
	c.made, c.rev, c.gen, c.known = true, playtime.rev, trophyAvail.gen, trophyAvail.known
	d := playtime.data
	var counted [trophyTierCount + 2]int
	earned, shown := 0, 0
	for i := range playtimeAchievements {
		if !trophyShown(i, d) {
			continue
		}
		shown++
		if _, ok := d.Unlocked[playtimeAchievements[i].ID]; ok {
			earned++
			if t := int(playtimeAchievements[i].Tier); t >= 0 && t < trophyTierCount {
				counted[t]++
			}
		}
	}
	counted[trophyTierCount], counted[trophyTierCount+1] = earned, shown
	// The snapshot moves with every second played; the strings only when a
	// trophy did.
	if !first && counted == c.counted {
		return c
	}
	c.counted = counted
	for t := 0; t < trophyTierCount; t++ {
		c.tiers[t] = strconv.Itoa(counted[t])
	}
	c.count = strconv.Itoa(earned)
	c.total = "Total: " + c.count
	c.frac, c.pct = 0, "0%"
	if shown > 0 {
		c.frac = float64(earned) / float64(shown)
		c.pct = strconv.Itoa(earned*100/shown) + "%"
	}
	return c
}

// hubDrawTiers sets the four cups along the trophies card's bottom, rarest
// first as the console does: platinum, gold, silver, bronze, each with its
// count.
func (h *HomeLayout) hubDrawTiers(renderer *sdl.Renderer, f hwpFonts, in sdl.Rect, t *homeHubTrophyText) {
	order := [trophyTierCount]trophyTier{trophyPlatinum, trophyGold, trophySilver, trophyBronze}
	// The four groups spread across the card with equal room between them,
	// in the title face — or the caption face where three-figure counts on
	// the narrow canvas would run into the next cup.
	minGap := scaleX(8, h.Width)
	var widths [trophyTierCount]int32
	font := f.tile
	for pass := 0; pass < 2; pass++ {
		lineH := int32(font.Height())
		inner := scaleX(6, h.Width)
		total := int32(0)
		for k, tier := range order {
			tw, _ := measureText(font, t.tiers[tier])
			widths[k] = lineH + inner + tw
			total += widths[k]
		}
		if in.W-total >= 3*minGap || font == f.meta {
			break
		}
		font = f.meta
	}
	lineH := int32(font.Height())
	inner := scaleX(6, h.Width)
	total := int32(0)
	for _, w := range widths {
		total += w
	}
	gap := max32(minGap, (in.W-total)/(trophyTierCount-1))
	x, y := in.X, in.Y+in.H-lineH
	for k, tier := range order {
		trophyDrawCup(renderer, sdl.Rect{X: x, Y: y, W: lineH, H: lineH}, lineH, tier)
		drawText(renderer, font, t.tiers[tier], x+lineH+inner, y, ColorTextPrimary(), textAlignLeft)
		x += widths[k] + gap
	}
}

// homeHubRecentText is the last game played, read from the recents list at
// most every two seconds.
var homeHubRecentText struct {
	at    time.Time
	ok    bool
	game  recentGame
	title string
	ago   string
}

// homeHubRecentNow refreshes the last game played.
func homeHubRecentNow(now time.Time) {
	c := &homeHubRecentText
	if !c.at.IsZero() && now.Sub(c.at) < 2*time.Second && now.After(c.at) {
		return
	}
	c.at = now
	games := recentGames()
	c.ok = len(games) > 0
	if !c.ok {
		c.title, c.ago = "", ""
		return
	}
	c.game = games[0]
	c.title = strings.TrimSpace(c.game.Title)
	if c.title == "" {
		c.title = "A game"
	}
	// TrimUI boots before its clock is set: a time from before the clock was
	// right, or one ahead of it, says nothing true.
	c.ago = ""
	if t := c.game.PlayedAt; !t.IsZero() {
		if d := now.Sub(t); d >= 0 && d < 5*365*24*time.Hour {
			c.ago = timeAgo(t)
		}
	}
}

// hubDrawRecent writes the last game played and when, along the card's
// bottom.
func (h *HomeLayout) hubDrawRecent(renderer *sdl.Renderer, f hwpFonts, in sdl.Rect, now time.Time) {
	homeHubRecentNow(now)
	c := &homeHubRecentText
	y := in.Y + in.H - int32(f.tile.Height())
	if !c.ok {
		h.hubBottomLine(renderer, f.helper, in, "No games played yet", ColorTextMuted())
		return
	}
	agoW := int32(0)
	gap := scaleX(10, h.Width)
	if c.ago != "" {
		agoW, _ = measureText(f.helper, c.ago)
	}
	room := in.W
	if agoW > 0 && agoW+gap < in.W/2 {
		room = in.W - agoW - gap
	} else {
		agoW = 0
	}
	title := ellipsize(f.tile, c.title, room)
	drawText(renderer, f.tile, title, in.X, y, ColorTextPrimary(), textAlignLeft)
	if agoW > 0 {
		tw, _ := measureText(f.tile, title)
		ay := y + int32(f.tile.Height()) - int32(f.helper.Height()) - scaleY(1, h.Height)
		drawText(renderer, f.helper, c.ago, in.X+tw+gap, ay, ColorTextMuted(), textAlignLeft)
	}
}

// homeHubStorageText is the storage card's value, made per reading.
var homeHubStorageText struct {
	src   *hwpStorageReading
	value string
}

// hubDrawStorage paints the storage card: "Free space" and the free figure
// on one line, the used share as a bar under them.
func (h *HomeLayout) hubDrawStorage(renderer *sdl.Renderer, f hwpFonts, in sdl.Rect, now time.Time) {
	r := hwpStorageNow(now)
	switch {
	case r == nil:
		h.hubBottomLine(renderer, f.helper, in, "Reading storage…", ColorTextMuted())
		return
	case !r.ok:
		h.hubBottomLine(renderer, f.helper, in, "Storage not readable", ColorTextMuted())
		return
	}
	c := &homeHubStorageText
	if c.src != r {
		c.src = r
		c.value = strings.TrimSpace(r.value + " " + r.unit)
	}
	barH := max32(4, scaleY(6, h.Height))
	bar := sdl.Rect{X: in.X, Y: in.Y + in.H - barH, W: in.W, H: barH}
	hwpBar(renderer, bar, r.used, hwpStorageInk(r.used))
	y := bar.Y - scaleY(6, h.Height) - int32(f.tile.Height())
	drawText(renderer, f.tile, c.value, in.X+in.W, y, ColorTextPrimary(), textAlignRight)
	vw, _ := measureText(f.tile, c.value)
	dot := scaleY(8, h.Height)
	ly := y + int32(f.tile.Height()) - int32(f.helper.Height()) - scaleY(1, h.Height)
	fillCircle(renderer, in.X+dot/2, ly+int32(f.helper.Height())/2, dot/2, homeGlass(90))
	if room := in.W - vw - dot - scaleX(20, h.Width); room > 0 {
		drawText(renderer, f.helper, ellipsize(f.helper, "Free space", room), in.X+dot+scaleX(8, h.Width), ly, ColorTextMuted(), textAlignLeft)
	}
}

// homeHubPlayText is the playtime card's strings, made per reading.
type homeHubPlayText struct {
	made  bool
	nums  [2]string
	units [2]string
	n     int
	plain string
	value string
	note  string
}

var homeHubPlay homeHubPlayText

// homeHubPlaytimeNow is today's playtime as the card writes it: "1 h 12 m"
// and "played today", or "Not yet today".
func homeHubPlaytimeNow(now time.Time) *homeHubPlayText {
	s := hwpPlaytimeStat(now)
	c := &homeHubPlay
	if c.made && c.n == s.n && c.nums == s.nums && c.units == s.units && c.plain == s.plain {
		return c
	}
	c.made, c.n, c.nums, c.units, c.plain = true, s.n, s.nums, s.units, s.plain
	c.note = ""
	switch {
	case s.n <= 0:
		c.value = s.plain
	case s.n == 1:
		c.value = strings.TrimSpace(s.nums[0] + " " + s.units[0])
		c.note = "played today"
	default:
		c.value = strings.TrimSpace(s.nums[0] + " " + s.units[0] + " " + s.nums[1] + " " + s.units[1])
		c.note = "played today"
	}
	return c
}

// homeHubMusicNow is the Now playing card's title and line: what plays, or
// the way to the music player.
func homeHubMusicNow() (string, string) {
	title, ok := currentMediaTitle()
	if !ok || strings.TrimSpace(title) == "" {
		return "Nothing playing", "Open Music"
	}
	if mediaPaused() {
		return title, "Paused"
	}
	return title, "Playing now"
}

// ── Focus ────────────────────────────────────────────────────────────────

// hubNeighborFrom is the board stop a press from rect f reaches by the
// drawn geometry: the nearest one lying wholly past f's edge in that
// direction, preferring one that shares its row (or column) — the rule the
// widget grid steps by (dynGridNeighbor). skip is the stop the press starts
// on. "" when there is none.
func (h *HomeLayout) hubNeighborFrom(f sdl.Rect, skip HomeFocusID, direction FocusDirection) HomeFocusID {
	if !homeHub.items || f.W <= 0 || f.H <= 0 {
		return ""
	}
	const tol = 4
	best, bestScore := HomeFocusID(""), int64(math.MaxInt64)
	for _, s := range homeHubSpecs {
		if s.id == skip || !h.Focus.IsEnabled(s.id) {
			continue
		}
		r := h.Items[s.id].Rect
		if r.W <= 0 || r.H <= 0 {
			continue
		}
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
			return ""
		}
		if offset > 0 && (direction == FocusLeft || direction == FocusRight) {
			// Sideways stays on the line: every sideways neighbour on the
			// board shares a row with the card, and Right from the library
			// pill must not slant down to the far column.
			continue
		}
		if gap < 0 {
			gap = 0
		}
		score := int64(gap) + 2*int64(offset)
		if score < bestScore {
			best, bestScore = s.id, score
		}
	}
	return best
}

// hubMove is Move while the focus is on the board: the stops by geometry,
// Up past the pills to the row's focused tile, Down past the bottom cards to
// the footer pill nearest sideways; at the board's sides the focus stays.
func (h *HomeLayout) hubMove(direction FocusDirection) HomeFocusID {
	cur := h.Focus.Current()
	from := h.Items[cur].Rect
	next := h.hubNeighborFrom(from, cur, direction)
	if next == "" {
		switch direction {
		case FocusUp:
			next = h.dynEntryBelow(from)
		case FocusDown:
			best, bestScore := HomeFocusID(""), int64(math.MaxInt64)
			for _, id := range []HomeFocusID{HomeFocusExit, HomeFocusEditPill} {
				p := h.Items[id].Rect
				if !h.Focus.IsEnabled(id) || p.W <= 0 || p.H <= 0 {
					continue
				}
				if score := homeDynSideScore(from, p); score < bestScore {
					best, bestScore = id, score
				}
			}
			next = best
		}
	}
	if next != "" && next != cur && h.Focus.Set(next) {
		h.Focus.Remember()
		if h.dynIsTile(next) {
			h.dynEnsureVisible(next)
		}
	}
	return h.Focus.Current()
}

// hubItemAbove is the board stop drawn nearest above a footer control at r:
// where Up from the power or Edit pill lands. "" with no board.
func (h *HomeLayout) hubItemAbove(r sdl.Rect) HomeFocusID {
	if !homeHub.items || r.W <= 0 {
		return ""
	}
	best, bestScore := HomeFocusID(""), int64(math.MaxInt64)
	for _, s := range homeHubSpecs {
		if !h.Focus.IsEnabled(s.id) {
			continue
		}
		c := h.Items[s.id].Rect
		if c.W <= 0 || c.H <= 0 || c.Y+c.H > r.Y+4 {
			continue
		}
		score := int64(r.Y-(c.Y+c.H)) + 2*homeDynSideScore(r, c)
		if score < bestScore {
			best, bestScore = s.id, score
		}
	}
	return best
}

// ── Activation and the header ────────────────────────────────────────────

// homeHubActivate is A (or a click) on a board stop. The cards that stand for
// a Home card hand back that card's ID for homeActivate to open the usual
// way (done=false); the rest open their screen here (done=true). An ID that
// is not the board's returns "", false.
func homeHubActivate(config *Config, id HomeFocusID) (HomeFocusID, bool) {
	if !strings.HasPrefix(string(id), homeHubIDPrefix) {
		return "", false
	}
	switch id {
	case homeHubContinue:
		return HomeFocusContinue, false
	case homeHubChat:
		return HomeFocusChat, false
	case homeHubStore:
		return HomeFocusPackages, false
	case homeHubExplore:
		return HomeFocusEmulator, false
	case homeHubBattery:
		homeWidgetActivate(config, "battery")
	case homeHubStorage:
		homeWidgetActivate(config, "storage")
	case homeHubPlaytime:
		homeWidgetActivate(config, "playtime")
	case homeHubMusic:
		homeWidgetActivate(config, "nowplaying")
	case homeHubTrophies, homeHubProgress:
		headerTrophyActivate(config)
	case homeHubRecent:
		homeHubRecentNow(time.Now())
		if homeHubRecentText.ok {
			playRecentGame(homeHubRecentText.game)
		} else {
			openGameLibraryPanel()
		}
	}
	return "", true
}

// hubDrawTabs heads the hub with the console's two tabs where the greeting
// stands: Games, lit, and Media, which a tap opens. It reports whether it
// drew them; drawGreeting draws the greeting otherwise.
func (h *HomeLayout) hubDrawTabs(renderer *sdl.Renderer, fonts HomeFonts) bool {
	homeHub.tabMedia = sdl.Rect{}
	if !h.hubActive() || fonts.Greeting == nil {
		return false
	}
	f := fonts.Greeting
	x := scaleX(homeSearchPillX, h.Width)
	y := scaleY(homeGreetingY, h.Height)
	drawText(renderer, f, "Games", x, y, ColorTextPrimary(), textAlignLeft)
	gw, gh := measureText(f, "Games")
	mx := x + gw + scaleX(32, h.Width)
	drawText(renderer, f, "Media", mx, y, ColorTextTertiary(), textAlignLeft)
	mw, _ := measureText(f, "Media")
	// Grown for a fingertip, but not down into the search pill under it.
	padX, padY := scaleX(8, h.Width), scaleY(4, h.Height)
	homeHub.tabMedia = sdl.Rect{X: mx - padX, Y: y - padY, W: mw + 2*padX, H: gh + 2*padY}
	return true
}

// homeHubTabClick opens Media from a tap on its tab. It reports whether the
// tap was the tab's.
func homeHubTabClick(mx, my int32, config *Config) bool {
	r := homeHub.tabMedia
	if r.W <= 0 || !pointInRect(mx, my, r) || homeLayout == nil || !homeLayout.hubActive() || config == nil {
		return false
	}
	target := homeSceneTarget(config, HomeFocusMedia)
	if idx := sceneIndexExact(config, target); idx >= 0 {
		PlayActivateSound()
		changeSceneTo(config, idx)
	}
	return true
}

// drawThemeSketchHub is the theme card's sketch of the welcome hub: the icon
// row with its larger focused tile and name, the pill band, and the board's
// four columns of cards.
func drawThemeSketchHub(renderer *sdl.Renderer, x, y, w, h int32, ink, ring sdl.Color) {
	soft := sdl.Color{R: ink.R, G: ink.G, B: ink.B, A: 90}
	dim := sdl.Color{R: ink.R, G: ink.G, B: ink.B, A: 55}
	right := x + w
	box := func(bx, by, bw, bh int32, colour sdl.Color) {
		if bx+bw > right {
			bw = right - bx
		}
		if bw > 0 && bh > 0 {
			fillRoundedRect(renderer, bx, by, bw, bh, 2, colour)
		}
	}
	// The gallery's sketch box is about 50 px tall: the row takes a quarter,
	// the gaps between bands a pixel or two, the board the rest.
	gap := max32(w/48, 2)
	vgap := max32(h/25, 1)
	big := h * 26 / 100
	small := big * 7 / 10
	x0 := x + w/12
	box(x0-gap-small, y, small, small, dim)
	box(x0, y, big, big, soft)
	strokeRoundedRect(renderer, x0-2, y-2, big+4, big+4, 3, 1, ring)
	for sx := x0 + big + gap; sx < right; sx += small + gap {
		box(sx, y, small, small, soft)
	}
	bar := max32(h/22, 1)
	box(x0+big+gap*2, y+small+(big-small)/2-bar/2, w/6, bar, ink)
	py := y + big + vgap*2
	pillH := max32(h/12, 3)
	box(x0, py, w/5, pillH, soft)
	box(x0+w/5+gap, py, w/4, pillH, sdl.Color{R: 245, G: 197, B: 24, A: 160})
	cy := py + pillH + vgap*2
	bottom := y + h
	colW := (right - x0 - 3*gap) / 4
	cardH := (bottom - cy - 2*vgap) / 3
	if colW <= 0 || cardH <= 0 {
		return
	}
	cell := func(col, row, rows int32) {
		box(x0+col*(colW+gap), cy+row*(cardH+vgap), colW, rows*cardH+(rows-1)*vgap, dim)
	}
	cell(0, 0, 1)
	cell(0, 1, 2)
	cell(1, 0, 1)
	cell(1, 1, 1)
	cell(1, 2, 1)
	cell(2, 0, 2)
	cell(2, 2, 1)
	cell(3, 0, 2)
	cell(3, 2, 1)
}
