package main

// home_carousel.go — Home's Carousel layout: the tile list as one row of big
// cards. The focused card stands in the middle at full size; the others get
// smaller and fade toward the edges, and the row turns round — Right from the
// last card is the first, so the row never runs out. The focused card's name
// and line sit under it, with a dot per card below them.
//
// The row slides: carPos (home_dynbody.go) eases toward the focused index
// along the shorter way round, and every card's place is a function of its
// distance from carPos, so a press glides the row instead of snapping it.
// Cards are painted from one base size (the centre card's) and scaled, so a
// card shrinking out of the middle reuses one cached shape the whole way.
// The full Continue and Media cards are drawn only for the settled centre
// card; elsewhere the compact painters stand in (home_widgetdraw.go).

import (
	"math"
	"strconv"

	"github.com/veandco/go-sdl2/sdl"
)

// The carousel in 1280x720 design units.
const (
	homeCarCX    = 640.0
	homeCarCY    = 331.0
	homeCarW     = 520.0
	homeCarH     = 330.0
	homeCarSide  = 0.5   // a neighbour's scale
	homeCarStep1 = 414.0 // centre to the first neighbour's centre
	homeCarStepN = 284.0 // between neighbours further out (a side card and a gutter)
	homeCarReach = 2.6   // how far from the centre a card is still drawn

	homeCarTitleY  int32 = 512
	homeCarNoteY   int32 = 552
	homeCarDotsY   int32 = 606
	homeCarDotsMax       = 15
)

// homeCarouselWrap maps a distance between two positions on a ring of n
// cards onto the shorter way round: [-n/2, n/2).
func homeCarouselWrap(d float64, n int) float64 {
	if n <= 0 {
		return 0
	}
	fn := float64(n)
	d = math.Mod(d, fn)
	if d < -fn/2 {
		d += fn
	}
	if d >= fn/2 {
		d -= fn
	}
	return d
}

// homeCarouselGeom is a card's centre x and size, in design units, at
// offset o from the focus (o is in cards; 0 is the middle).
func homeCarouselGeom(o float64) (cx, w, h float64) {
	a := math.Abs(o)
	s := 1 - (1-homeCarSide)*math.Min(a, 1)
	d := a * homeCarStep1
	if a > 1 {
		d = homeCarStep1 + (a-1)*homeCarStepN
	}
	if o < 0 {
		d = -d
	}
	return homeCarCX + d, homeCarW * s, homeCarH * s
}

// carRect is the card at offset o on the canvas.
func (h *HomeLayout) carRect(o float64) sdl.Rect {
	cx, w, hh := homeCarouselGeom(o)
	sx := float64(h.Width) / float64(HomeBaseWidth)
	sy := float64(h.Height) / float64(HomeBaseHeight)
	return sdl.Rect{
		X: int32(math.Round((cx - w/2) * sx)),
		Y: int32(math.Round((homeCarCY - hh/2) * sy)),
		W: int32(math.Round(w * sx)),
		H: int32(math.Round(hh * sy)),
	}
}

// carBase is the centre card's size centred on r: the rect every card is
// painted from (the shape cache key), so scaling a card never makes a new
// shape.
func (h *HomeLayout) carBase(r sdl.Rect) sdl.Rect {
	bw := int32(math.Round(homeCarW * float64(h.Width) / float64(HomeBaseWidth)))
	bh := int32(math.Round(homeCarH * float64(h.Height) / float64(HomeBaseHeight)))
	return sdl.Rect{X: r.X + r.W/2 - bw/2, Y: r.Y + r.H/2 - bh/2, W: bw, H: bh}
}

// carOffset is tile i's offset from the focus right now.
func (h *HomeLayout) carOffset(i int) float64 {
	return homeCarouselWrap(float64(i)-h.dyn.carPos, len(h.dyn.tiles))
}

// carSettled reports whether the row has stopped on the focused card.
func (h *HomeLayout) carSettled() bool {
	return math.Abs(homeCarouselWrap(float64(h.dyn.carIdx)-h.dyn.carPos, len(h.dyn.tiles))) < 0.02
}

// carPlace writes the cards' drawn rects into their items; a card too far
// out to be drawn gets none, so it cannot be hit.
func (h *HomeLayout) carPlace() {
	for i, t := range h.dyn.tiles {
		item := h.Items[t.ID]
		item.ID = t.ID
		if o := h.carOffset(i); math.Abs(o) <= homeCarReach {
			item.Rect = h.carRect(o)
		} else {
			item.Rect = sdl.Rect{}
		}
		h.Items[t.ID] = item
	}
}

// carTileAt is the card under a point: the one nearest the middle wins where
// two overlap, as it is drawn on top.
func (h *HomeLayout) carTileAt(x, y int32) HomeFocusID {
	best, bestA := HomeFocusID(""), math.MaxFloat64
	for i, t := range h.dyn.tiles {
		r := h.Items[t.ID].Rect
		if r.W <= 0 || !pointInRect(x, y, r) {
			continue
		}
		if a := math.Abs(h.carOffset(i)); a < bestA {
			best, bestA = t.ID, a
		}
	}
	return best
}

// dynClick is a tap on the dynamic body that FocusAt does not cover: a
// carousel card. A side card turns the row to it; the middle one opens. It
// reports whether the tap was used.
func (h *HomeLayout) dynClick(mx, my int32, config *Config) bool {
	if !h.dynCarousel() {
		return false
	}
	id := h.carTileAt(mx, my)
	if id == "" {
		return false
	}
	i := h.dynTileIndex(id)
	if i < 0 || !h.Focus.Set(id) {
		return false
	}
	h.Focus.Remember()
	if i != h.dyn.carIdx || !h.carSettled() {
		// dynEnsureVisible turns the row and keeps the grid's scroll in step,
		// for a switch to Widgets later.
		h.dynEnsureVisible(id)
		PlayNavSound()
		return true
	}
	homeActivate(config)
	return true
}

// carRender draws the row, the focused card's labels and the dots.
func (h *HomeLayout) carRender(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData, focused HomeFocusID, lift float64) {
	n := len(h.dyn.tiles)
	if n == 0 {
		return
	}
	// The drawn cards, far ones first so the middle one ends on top. The
	// buffers are arrays: the row is drawn every frame.
	var order [homeTilesMax + 2]int
	var offs [homeTilesMax + 2]float64
	m := 0
	for i := range h.dyn.tiles {
		o := h.carOffset(i)
		if math.Abs(o) > homeCarReach || m >= len(order) {
			continue
		}
		order[m], offs[m] = i, o
		m++
	}
	for a := 1; a < m; a++ {
		for b := a; b > 0 && math.Abs(offs[b]) > math.Abs(offs[b-1]); b-- {
			order[b], order[b-1] = order[b-1], order[b]
			offs[b], offs[b-1] = offs[b-1], offs[b]
		}
	}
	settled := h.carSettled()
	for k := 0; k < m; k++ {
		i, o := order[k], offs[k]
		t := h.dyn.tiles[i]
		item := h.Items[t.ID]
		if item.Rect.W <= 0 {
			continue
		}
		centre := math.Abs(o) < 0.02
		isFocus := t.ID == focused && centre
		held := homeEdit.open && homeEdit.holding && t.ID == h.Focus.Current() && centre
		cardLift := 0.0
		if isFocus {
			cardLift = lift
		}
		pressed := data.Pressed == t.ID
		pressDepth := 0.0
		if pressed {
			pressDepth = data.PressDepth
		}
		base := h.carBase(item.Rect)
		draw := item.Rect
		if held {
			cardLift = 1
			grow := scaleX(6, h.Width)
			shadow := sdl.Rect{X: draw.X - grow + scaleX(4, h.Width), Y: draw.Y - grow + scaleY(12, h.Height),
				W: draw.W + 2*grow, H: draw.H + 2*grow}
			fillRoundedRectAt(renderer, base, shadow, homeTileRadius, sdl.Color{R: 0, G: 0, B: 0, A: 80})
		}
		draw = scaleHomeCard(draw, cardLift)
		// The full Continue and Media cards paint themselves from base and the
		// lift, so a carried one is not grown again: its badge stays on its
		// corner (dynPaintTile does the same).
		ownShape := t.Kind == homeTileCard && (t.ID == HomeFocusContinue || t.ID == HomeFocusMedia) && centre && settled
		if held && !ownShape {
			grow := scaleX(4, h.Width)
			draw = sdl.Rect{X: draw.X - grow, Y: draw.Y - grow, W: draw.W + 2*grow, H: draw.H + 2*grow}
		}
		draw.Y += homePressOffset(pressDepth, h.Height)
		paint := item
		paint.Rect = base
		full := centre && settled
		switch t.Kind {
		case homeTileCard:
			switch {
			case t.ID == HomeFocusContinue && full:
				h.drawContinueCard(renderer, fonts, paint, isFocus, pressed, pressDepth, data.Continue, cardLift)
			case t.ID == HomeFocusContinue:
				h.drawHomeContinueCompact(renderer, fonts, paint, draw, data.Continue, isFocus, pressed)
			case t.ID == HomeFocusMedia && full:
				h.drawMediaCard(renderer, fonts, paint, isFocus, pressed, pressDepth, cardLift)
			default:
				h.drawHomeAppTile(renderer, fonts, paint, draw, isFocus, pressed, false)
			}
		case homeTileWidget:
			if t.Dim {
				h.drawHomeAppTile(renderer, fonts, paint, draw, isFocus, pressed, true)
				break
			}
			h.drawHomeWidget(renderer, fonts, homeTileWidgetName(t.Ref), paint, draw, isFocus, pressed)
		case homeTileApp:
			h.drawHomeAppTile(renderer, fonts, paint, draw, isFocus, pressed, t.Dim)
		case homeTileAdd:
			h.drawEditAddTile(renderer, fonts, paint, draw, isFocus, pressed)
		}
		if !centre {
			// The row fades toward the edges: a veil of the backdrop's own
			// colour, so a light theme fades to light and a dark one to dark.
			veil := WithAlpha(ColorBackground, uint8(140*math.Min(math.Abs(o), 1)))
			fillRoundedRectAt(renderer, base, draw, homeTileRadius, veil)
		}
		if homeEdit.open && t.Kind != homeTileAdd && centre {
			h.drawEditBadges(renderer, fonts, t, draw, false)
		}
	}
	h.carDrawLabels(renderer, fonts)
	h.carDrawDots(renderer, fonts)
}

// carDrawLabels writes the focused card's name and line under the row.
func (h *HomeLayout) carDrawLabels(renderer *sdl.Renderer, fonts HomeFonts) {
	i := h.dyn.carIdx
	if i < 0 || i >= len(h.dyn.tiles) {
		return
	}
	item := h.Items[h.dyn.tiles[i].ID]
	maxW := scaleX(homeCarW+200, h.Width)
	cx := scaleX(int32(homeCarCX), h.Width)
	if f := fonts.Greeting; f != nil && item.Title != "" {
		drawText(renderer, f, ellipsize(f, item.Title, maxW), cx, scaleY(homeCarTitleY, h.Height), ColorTextPrimary(), textAlignCenter)
	}
	if f := fonts.Helper; f != nil && item.Subtitle != "" {
		drawText(renderer, f, ellipsize(f, item.Subtitle, maxW), cx, scaleY(homeCarNoteY, h.Height), ColorTextMuted(), textAlignCenter)
	}
}

// homeCarCounter caches the "7 / 24" a long row shows instead of dots.
var homeCarCounter struct {
	i, n int
	text string
}

// carDrawDots marks where the focused card is in the row: a dot per card and
// a wider accent pill for the focused one, or "7 / 24" past homeCarDotsMax.
func (h *HomeLayout) carDrawDots(renderer *sdl.Renderer, fonts HomeFonts) {
	n := len(h.dyn.tiles)
	if n < 2 {
		return
	}
	cur := h.dyn.carIdx
	cx := scaleX(int32(homeCarCX), h.Width)
	y := scaleY(homeCarDotsY, h.Height)
	if n > homeCarDotsMax {
		if fonts.Small == nil {
			return
		}
		c := &homeCarCounter
		if c.text == "" || c.i != cur || c.n != n {
			c.i, c.n = cur, n
			c.text = strconv.Itoa(cur+1) + " / " + strconv.Itoa(n)
		}
		drawText(renderer, fonts.Small, c.text, cx, y-int32(fonts.Small.Height())/2, ColorTextMuted(), textAlignCenter)
		return
	}
	d := max32(4, scaleY(8, h.Height))
	gap := max32(4, scaleX(8, h.Width))
	pill := max32(d*2, scaleX(20, h.Width))
	total := int32(n-1)*(d+gap) + pill
	x := cx - total/2
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	for i := 0; i < n; i++ {
		if i == cur {
			fillRoundedRect(renderer, x, y-d/2, pill, d, d/2, ColorAccent)
			x += pill + gap
			continue
		}
		fillRoundedRect(renderer, x, y-d/2, d, d, d/2, homeGlass(70))
		x += d + gap
	}
}
