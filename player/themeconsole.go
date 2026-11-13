package main

// themeconsole.go — the console looks: what lets a total conversion read as a
// PS5, a Switch or an Xbox dashboard at a glance instead of a recoloured
// JukaHub. Only colour, shape, arrangement and the way focus is shown — no
// logos, fonts or artwork:
//
//   - PS5 Style: the welcome hub (themeps5hub.go). A row of small icon tiles
//     along the top, the focused one larger with its name beside it, then a
//     row of pills and a board of flat graphite cards (battery, chat,
//     trophies, storage…) on a near-black page.
//   - PS4 Style: near-black navy under a deep-blue glow. The Carousel becomes
//     a shelf: small rounded tiles along the top-left, the focused one larger
//     with its name beside it, a white focus ring.
//   - Switch 2 Style and Switch 2 Style Dark: the console's home screen
//     (themeswitch.go) on flat light (or charcoal) grey. The Carousel becomes
//     a strip of square tiles, recent games first, that the focus walks along
//     (the row scrolls only at its edges), the name above the focused tile, a
//     bright cyan outline that pulses after each move, and a line of round
//     buttons under it. The plain strip (carouselStyle "strip") is the same
//     row without the console's chrome.
//   - Xbox Style: charcoal with green highlights on the Widgets grid of
//     rounded-rectangle tiles, a white focus ring over a green glow.
//
// A preset asks for these through optional ThemePreset fields (CarouselStyle,
// TileRadius, FocusRing, FocusRing2). Their zero values are the shipped look,
// so every other theme draws exactly as before. ApplyThemeColors installs them
// with the palette (applyThemeConsoleLook), which is also how a restart, a hot
// reload and clearThemePreset reach them.

import (
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// Carousel styles (ThemePreset.CarouselStyle). Empty is the centred carousel;
// hub is PS5 Style's welcome hub (themeps5hub.go).
const (
	homeCarouselCentred = ""
	homeCarouselShelf   = "shelf"
	homeCarouselStrip   = "strip"
	homeCarouselHub     = "hub"
)

const (
	// homeTileRadiusMax caps a preset's tile radius: rounder than this and a
	// small tile turns into a pill.
	homeTileRadiusMax int32 = 32
	// homeRingPulseMS is how long the ring pulses after a move: two whole
	// periods, so it ends where it began, on the ring colour. A moment, not
	// forever, so a resting Home does not hold the frame rate up.
	homeRingPulseMS     = 2400
	homeRingPulsePeriod = 1200.0
)

// The shelf (PS4) in 1280x720 design units: the focused tile at the anchor,
// its name in the gap after it, the rest small and top-aligned beside it.
const (
	homeShelfX      = 88.0
	homeShelfTop    = 176.0
	homeShelfBig    = 236.0
	homeShelfSmall  = 148.0
	homeShelfGap    = 20.0
	homeShelfTitleW = 320.0
	homeShelfReachL = -1.2
	homeShelfReachR = 4.7
)

// The strip (Switch) in design units: equal square tiles from the left edge,
// four whole ones on screen and the fifth peeking in.
const (
	homeStripX            = 64.0
	homeStripTop          = 214.0
	homeStripSize         = 252.0
	homeStripGap          = 20.0
	homeStripShown        = 4
	homeStripTitleY int32 = 168
)

// themeConsoleSpec is one conversion's console look.
type themeConsoleSpec struct {
	carousel    string
	radius      int32
	ring, ring2 string
}

// themeConsoleSpecs are the console looks of the conversions that carry one,
// keyed by conversion id (themeConversionSpecs); themes.go's init copies them
// onto the conversion. The palettes underneath stay plain colours, so picking
// "PS5 Night" in the Colour tab never reshapes a card.
var themeConsoleSpecs = map[string]themeConsoleSpec{
	"convert-ps5":         {carousel: homeCarouselHub, radius: 10, ring: "#ffffff"},
	"convert-ps4":         {carousel: homeCarouselShelf, radius: 14, ring: "#ffffff"},
	"convert-switch":      {carousel: homeCarouselSwitchHome, radius: 6, ring: "#00a3d9", ring2: "#3c64ff"},
	"convert-switch-dark": {carousel: homeCarouselSwitchHome, radius: 6, ring: "#00c3e3", ring2: "#8ef0ff"},
	"convert-xbox":        {radius: 8, ring: "#ffffff"},
	"convert-xboxone":     {radius: 2, ring: "#ffffff"},
	"convert-minimal":     {carousel: homeCarouselText, radius: 2, ring: "#ffffff"},
}

// applyTo writes the look into a preset.
func (s themeConsoleSpec) applyTo(p *ThemePreset) {
	p.CarouselStyle = s.carousel
	p.TileRadius = s.radius
	p.FocusRing = s.ring
	p.FocusRing2 = s.ring2
}

// normalizeCarouselStyle clamps a stored or file value to a known style; the
// console names are accepted as aliases.
func normalizeCarouselStyle(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case homeCarouselShelf, "ps4":
		return homeCarouselShelf
	case homeCarouselStrip, "switch":
		return homeCarouselStrip
	case homeCarouselHub, "ps5", "welcome":
		return homeCarouselHub
	case homeCarouselText, "minimal", "list":
		return homeCarouselText
	}
	return homeCarouselCentred
}

// homeConsoleLook is the active preset's console look, parsed once per theme
// change: the ring is drawn every frame.
var homeConsoleLook struct {
	carousel    string
	ring, ring2 sdl.Color
	hasRing     bool
	pulse       bool
	switchHome  bool // carouselStyle "switch": the Switch Home (themeswitch.go)
	// pulseFor is the control the pulse runs for, pulseAt when it began.
	pulseFor HomeFocusID
	pulseAt  uint64
}

// homeStrip is the strip's scroll: first is the first whole tile shown, pos
// eases toward it.
var homeStrip struct {
	first  int
	pos    float64
	tick   uint64
	placed bool
	anim   bool
}

// applyThemeConsoleLook installs a preset's console look (ApplyThemeColors).
func applyThemeConsoleLook(p ThemePreset) {
	l := &homeConsoleLook
	l.carousel = normalizeCarouselStyle(p.CarouselStyle)
	l.switchHome = homeIsSwitchHomeStyle(p.CarouselStyle)
	homeTileRadius = homeTileRadiusDefault
	if p.TileRadius > 0 {
		homeTileRadius = min32(max32(p.TileRadius, 2), homeTileRadiusMax)
	}
	l.ring, l.hasRing = parseHexColor(p.FocusRing)
	ring2, ok := parseHexColor(p.FocusRing2)
	l.ring2, l.pulse = ring2, ok && l.hasRing
	l.pulseFor, l.pulseAt = "", 0
	homeStrip.first, homeStrip.placed, homeStrip.anim = 0, false, false
	homeHub.placed, homeHub.anim = false, false
	homeText.first, homeText.placed, homeText.anim = 0, false, false
}

// homeFocusRingColor is the ring around a focused Home tile: the accent, or
// the preset's own ring, swinging toward FocusRing2 and back for a moment
// after each move (the Switch's live outline). Focus bounce Off keeps it still.
func homeFocusRingColor() sdl.Color {
	l := &homeConsoleLook
	if !l.hasRing {
		return ColorAccent
	}
	g := &homeFocusGlide
	if !l.pulse || !g.open || !g.enabled {
		return l.ring
	}
	now := g.frameMS
	if g.focusID != l.pulseFor {
		// Only a direction step pulses: focus restored on the way back to
		// Home, the mouse, or a tile that went away is not a move.
		l.pulseFor, l.pulseAt = g.focusID, 0
		if g.input {
			l.pulseAt = now
		}
	}
	if now < l.pulseAt || now-l.pulseAt >= homeRingPulseMS {
		return l.ring
	}
	k := 0.5 - 0.5*math.Cos(2*math.Pi*float64(now-l.pulseAt)/homeRingPulsePeriod)
	// Six steps, not a new colour every frame: the rounded-stroke cache keys
	// on RGB, and a fresh colour per frame would render a new texture each
	// frame and push the other cards out of the handheld's cache budget.
	k = math.Round(k*6) / 6
	return lerpColor(l.ring, l.ring2, float32(k))
}

// homeConsoleAnimating reports a pulsing ring or a sliding strip, so Home
// keeps its interactive frame rate until they settle (homeLayoutAnimating).
func homeConsoleAnimating() bool {
	l := &homeConsoleLook
	if l.pulse && l.pulseFor != "" && uiClockMS() < l.pulseAt+homeRingPulseMS+20 {
		return true
	}
	if l.carousel == homeCarouselHub {
		return homeHub.anim && homeLayout.dynCarousel()
	}
	if l.carousel == homeCarouselText {
		return homeText.anim && homeLayout.dynCarousel()
	}
	return l.carousel == homeCarouselStrip && homeStrip.anim && homeLayout.dynCarousel()
}

// homeShelfOffset maps a distance from the focus onto (-1, n-1]: on the
// shelf every other tile stands to the right of the focus, and only the one
// leaving (or arriving) slides out to the left.
func homeShelfOffset(d float64, n int) float64 {
	if n <= 0 {
		return 0
	}
	fn := float64(n)
	o := math.Mod(d, fn)
	if o < 0 {
		o += fn
	}
	if o > fn-1 {
		o -= fn
	}
	return o
}

// homeShelfGeom is a shelf tile's rect in design units at offset o: the
// focused tile big at the anchor, the name's room after it, then the small
// tiles, all hanging from one top line.
func homeShelfGeom(o float64) (x, y, w, h float64) {
	size := homeShelfBig - (homeShelfBig-homeShelfSmall)*math.Min(math.Abs(o), 1)
	switch {
	case o >= 1:
		x = homeShelfX + homeShelfBig + homeShelfGap + homeShelfTitleW + (o-1)*(homeShelfSmall+homeShelfGap)
	case o >= 0:
		x = homeShelfX + o*(homeShelfBig+homeShelfGap+homeShelfTitleW)
	default:
		x = homeShelfX + o*(homeShelfSmall+homeShelfGap)
	}
	return x, homeShelfTop, size, size
}

// carDesignRect scales a design-unit rect onto the canvas.
func (h *HomeLayout) carDesignRect(x, y, w, hh float64) sdl.Rect {
	sx := float64(h.Width) / float64(HomeBaseWidth)
	sy := float64(h.Height) / float64(HomeBaseHeight)
	return sdl.Rect{
		X: int32(math.Round(x * sx)),
		Y: int32(math.Round(y * sy)),
		W: int32(math.Round(w * sx)),
		H: int32(math.Round(hh * sy)),
	}
}

// carStyleRect is tile i's rect on the canvas at offset o from the focus in
// the active carousel style; false means the tile is not drawn.
func (h *HomeLayout) carStyleRect(i int, o float64) (sdl.Rect, bool) {
	switch homeConsoleLook.carousel {
	case homeCarouselShelf:
		if o < homeShelfReachL || o > homeShelfReachR {
			return sdl.Rect{}, false
		}
		return h.carDesignRect(homeShelfGeom(o)), true
	case homeCarouselStrip:
		if homeConsoleLook.switchHome {
			return h.switchRowRect(i)
		}
		x := homeStripX + (float64(i)-homeStrip.pos)*(homeStripSize+homeStripGap)
		if x+homeStripSize < -homeStripGap || x > float64(HomeBaseWidth) {
			return sdl.Rect{}, false
		}
		return h.carDesignRect(x, homeStripTop, homeStripSize, homeStripSize), true
	case homeCarouselHub:
		return h.hubRowRect(o)
	case homeCarouselText:
		return h.textRowRect(i)
	}
	if math.Abs(o) > homeCarReach {
		return sdl.Rect{}, false
	}
	return h.carRect(o), true
}

// homeCarBaseSize is the size, in design units, every card of the active
// style is painted from (carBase): its focused card's.
func homeCarBaseSize() (float64, float64) {
	switch homeConsoleLook.carousel {
	case homeCarouselShelf:
		return homeShelfBig, homeShelfBig
	case homeCarouselStrip:
		return homeStripSize, homeStripSize
	case homeCarouselHub:
		return homeHubBig, homeHubBig
	case homeCarouselText:
		return homeTextW, homeTextRow
	}
	return homeCarW, homeCarH
}

// carIsCentre reports whether tile i (at offset o) is the focused card's
// place: the middle of the centred carousel and the shelf's anchor, or on the
// strip simply the focused tile, wherever along the row it is.
func (h *HomeLayout) carIsCentre(i int, o float64) bool {
	if homeConsoleLook.carousel == homeCarouselStrip {
		return i == h.dyn.carIdx
	}
	return math.Abs(o) < 0.02
}

// carVeil is how much of the backdrop colour covers a card at offset o. The
// centred carousel fades its row toward the edges; the shelf barely dims its
// waiting tiles but fades the one leaving on the left; the strip's tiles are
// all equal.
func carVeil(o float64) uint8 {
	a := math.Min(math.Abs(o), 1)
	switch homeConsoleLook.carousel {
	case homeCarouselShelf:
		if o < 0 {
			return uint8(230 * a)
		}
		return uint8(40 * a)
	case homeCarouselStrip:
		return 0
	}
	return uint8(140 * a)
}

// stripStep keeps the focused tile among the strip's whole tiles, scrolling
// only when the focus would leave them, and eases the row there. carPlace
// calls it every refresh; the elapsed time makes a second call in the same
// millisecond a no-op.
func (h *HomeLayout) stripStep() {
	s := &homeStrip
	n, i := len(h.dyn.tiles), h.dyn.carIdx
	if i < s.first {
		s.first = i
	}
	if i > s.first+homeStripShown-1 {
		s.first = i - homeStripShown + 1
	}
	s.first = max(0, min(s.first, n-homeStripShown))
	target := float64(s.first)
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

// carDrawConsoleLabels writes the focused tile's name the console way: beside
// the big shelf tile (name over its line), or above the strip's focused tile
// in the ring's colour. It reports whether it drew them.
func (h *HomeLayout) carDrawConsoleLabels(renderer *sdl.Renderer, fonts HomeFonts) bool {
	style := homeConsoleLook.carousel
	if style == homeCarouselCentred {
		return false
	}
	if homeConsoleLook.switchHome && style == homeCarouselStrip {
		return h.switchDrawTitle(renderer, fonts)
	}
	i := h.dyn.carIdx
	if i < 0 || i >= len(h.dyn.tiles) {
		return true
	}
	item := h.Items[h.dyn.tiles[i].ID]
	r := item.Rect
	if r.W <= 0 || item.Title == "" {
		return true
	}
	if style == homeCarouselShelf {
		// Only once the row has stopped: the name belongs to the gap the
		// focused tile leaves, which is closing while the row slides.
		if !h.carSettled() {
			return true
		}
		x := r.X + r.W + scaleX(28, h.Width)
		maxW := scaleX(int32(homeShelfTitleW)-48, h.Width)
		mid := r.Y + r.H/2
		if f := fonts.Greeting; f != nil {
			drawText(renderer, f, ellipsize(f, item.Title, maxW), x, mid-int32(f.Height())-scaleY(2, h.Height), ColorTextPrimary(), textAlignLeft)
		}
		if f := fonts.Helper; f != nil && item.Subtitle != "" {
			drawText(renderer, f, ellipsize(f, item.Subtitle, maxW), x, mid+scaleY(6, h.Height), ColorTextMuted(), textAlignLeft)
		}
		return true
	}
	f := fonts.Tile
	if f == nil {
		f = fonts.Helper
	}
	if f == nil {
		return true
	}
	text := ellipsize(f, item.Title, scaleX(560, h.Width))
	tw, _, err := f.SizeUTF8(text)
	if err != nil {
		return true
	}
	// Centred over the tile, kept on screen for the first and last ones.
	margin := scaleX(homeSideMargin, h.Width)
	x := r.X + r.W/2 - int32(tw)/2
	x = max(margin, min(x, h.Width-margin-int32(tw)))
	ink := ColorAccent
	if homeConsoleLook.hasRing {
		ink = homeConsoleLook.ring
	}
	drawText(renderer, f, text, x, scaleY(homeStripTitleY, h.Height), TextInk(ink), textAlignLeft)
	return true
}

// drawThemeSketchFor draws a theme card's arrangement sketch: the console
// rows in their own shape, everything else as drawThemeArrangementSketch has
// it.
func drawThemeSketchFor(renderer *sdl.Renderer, p ThemePreset, x, y, w, h int32, ink, accent sdl.Color) {
	style := homeCarouselCentred
	if normalizeHomeArrangement(p.Arrangement) == homeArrangementCarousel {
		style = normalizeCarouselStyle(p.CarouselStyle)
	}
	if drawSwitchHomeSketch(renderer, p, style, x, y, w, h, ink, accent) {
		return
	}
	if style == homeCarouselCentred || w <= 8 || h <= 8 {
		drawThemeArrangementSketch(renderer, p.Arrangement, x, y, w, h, ink, accent)
		return
	}
	ring := accent
	if c, ok := parseHexColor(p.FocusRing); ok {
		ring = c
	}
	if style == homeCarouselHub {
		drawThemeSketchHub(renderer, x, y, w, h, ink, ring)
		return
	}
	if style == homeCarouselText {
		drawThemeSketchText(renderer, x, y, w, h, ink, ring)
		return
	}
	soft := sdl.Color{R: ink.R, G: ink.G, B: ink.B, A: 90}
	right := x + w
	tile := func(tx, ty, tw, th int32, colour sdl.Color) {
		if tx+tw > right {
			tw = right - tx
		}
		if tw > 0 && th > 0 {
			fillRoundedRect(renderer, tx, ty, tw, th, 3, colour)
		}
	}
	gap := max(w/40, 2)
	bar := max(h/14, 2)
	if style == homeCarouselShelf {
		big := h * 55 / 100
		small := big * 6 / 10
		x0, y0 := x+w/12, y+h/8
		tile(x0, y0, big, big, soft)
		strokeRoundedRect(renderer, x0-2, y0-2, big+4, big+4, 4, 1, ring)
		title := x0 + big + gap*2
		tile(title, y0+big/2-bar-1, w/5, bar, ink)
		tile(title, y0+big/2+1, w/8, bar, soft)
		for sx := title + w/5 + gap*2; sx < right; sx += small + gap {
			tile(sx, y0, small, small, soft)
		}
		return
	}
	side := h * 6 / 10
	y0 := y + (h-side)/2 + bar
	for k := int32(0); x+gap+k*(side+gap) < right; k++ {
		sx := x + gap + k*(side+gap)
		tile(sx, y0, side, side, soft)
		if k == 1 {
			strokeRoundedRect(renderer, sx-2, y0-2, side+4, side+4, 3, 2, ring)
			tile(sx+side/6, y0-bar*2-2, side*2/3, bar, ring)
		}
	}
}
