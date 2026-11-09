package main

// setwidgets.go — the controls the Settings hub is built from, kept separate so
// any screen can use them: a pill sized from its text, an on/off switch, and a
// carousel ("‹ value ›") that cycles a closed set of values.
//
// Each one measures before it draws. The old Settings page placed fixed boxes
// and then drew whatever text arrived into them, which is how the theme
// gallery's "Current" badge came out smaller than its own word and how the
// Video snaps and Cookies rows ran out of their buttons on the 960-wide canvas.
// Here the box is derived from the measured text (pill) or the text is fitted to
// the box (carousel), so neither can happen.
//
// Everything draws with Home's glass tokens (ui_glass.go) and the theme inks,
// and allocates nothing per frame: measurements go through the shared text
// caches.

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ── Pill ──────────────────────────────────────────────────────────────────

// setPillSize is the size of a pill holding text: the measured label, a 3px
// band above and below it, and half the pill's height (at least 8px) on either
// side, so the rounded ends never cut into a letter.
func setPillSize(font *ttf.Font, text string) (int32, int32) {
	if font == nil || text == "" {
		return 0, 0
	}
	tw, th := measureText(font, text)
	if tw <= 0 || th <= 0 {
		return 0, 0
	}
	h := th + 6
	padX := h / 2
	if padX < 8 {
		padX = 8
	}
	return tw + 2*padX, h
}

// setPillDraw paints text in a pill whose size comes from the text itself and
// returns the pill's rect. x, y is the top-left corner; a caller that wants the
// pill right-aligned measures it with setPillSize first.
func setPillDraw(renderer *sdl.Renderer, font *ttf.Font, text string, x, y int32, fill, ink sdl.Color) sdl.Rect {
	w, h := setPillSize(font, text)
	if renderer == nil || w <= 0 || h <= 0 {
		return sdl.Rect{}
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, x, y, w, h, h/2, fill)
	tw, th := measureText(font, text)
	drawText(renderer, font, text, x+(w-tw)/2, y+(h-th)/2, ink, textAlignLeft)
	return sdl.Rect{X: x, Y: y, W: w, H: h}
}

// ── Switch ────────────────────────────────────────────────────────────────

// setSwitchSize is the switch for a row of height rowH: a track a little over
// twice as wide as it is tall, never taller than 30px.
func setSwitchSize(rowH int32) (int32, int32) {
	h := rowH / 2
	if h > 30 {
		h = 30
	}
	if h < 18 {
		h = 18
	}
	return h*2 + h/4, h
}

// setSwitchDraw paints an on/off switch in rect: the accent track with the knob
// on the right when on, a quiet glass track with the knob on the left when off.
// The knob's side carries the state on its own, so the switch still reads for a
// colour-blind user or on a monochrome theme.
func setSwitchDraw(renderer *sdl.Renderer, rect sdl.Rect, on, disabled bool) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	radius := rect.H / 2
	track := glassA(34)
	edge := glassA(70)
	knob := WithAlpha(ColorTextSecondary(), 235)
	if on {
		track = glassAccent()
		edge = track
		knob = contrastText(track)
	}
	if disabled {
		track = WithAlpha(track, 90)
		edge = WithAlpha(edge, 90)
		knob = WithAlpha(knob, 140)
	}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, track)
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, edge)
	inset := int32(3)
	d := rect.H - 2*inset
	if d < 4 {
		return
	}
	kx := rect.X + inset
	if on {
		kx = rect.X + rect.W - inset - d
	}
	fillCircle(renderer, kx+d/2, rect.Y+rect.H/2, d/2, knob)
}

// ── Chevron ───────────────────────────────────────────────────────────────

// setDrawChevron strokes a chevron about 2px thick centred on (cx, cy): dir < 0
// points left, dir > 0 right. half is half the glyph's height.
func setDrawChevron(renderer *sdl.Renderer, cx, cy, half int32, dir int, col sdl.Color) {
	if renderer == nil || half <= 0 {
		return
	}
	setDrawColor(renderer, col)
	reach := half * 3 / 5
	for off := int32(-1); off <= 1; off++ {
		if dir < 0 {
			_ = renderer.DrawLine(cx+reach/2+off, cy-half, cx-reach/2+off, cy)
			_ = renderer.DrawLine(cx-reach/2+off, cy, cx+reach/2+off, cy+half)
		} else {
			_ = renderer.DrawLine(cx-reach/2+off, cy-half, cx+reach/2+off, cy)
			_ = renderer.DrawLine(cx+reach/2+off, cy, cx-reach/2+off, cy+half)
		}
	}
}

// ── Carousel ──────────────────────────────────────────────────────────────

// setCarouselParts is a carousel's geometry: the two arrow buttons and the
// value between them. Hit-testing uses the same rects the draw used.
type setCarouselParts struct {
	Prev  sdl.Rect
	Value sdl.Rect
	Next  sdl.Rect
}

// setCarouselArrow is the arrow button's size for a carousel of height h.
func setCarouselArrow(h int32) int32 {
	a := h - 8
	if a > 34 {
		a = 34
	}
	if a < 20 {
		a = 20
	}
	return a
}

// setCarouselWidthFor is setCarouselWidth over option labels plus the value
// on display (which may be none of them, "Custom · #12ab34"), with room for a
// colour chip when the options carry one. It allocates nothing, so a screen can
// size its carousels every frame.
func setCarouselWidthFor(font *ttf.Font, options []setOption, shown string, h, min, max int32) int32 {
	widest, _ := measureText(font, shown)
	chip := false
	for _, o := range options {
		if w, _ := measureText(font, o.Label); w > widest {
			widest = w
		}
		if o.Swatch != "" {
			chip = true
		}
	}
	if chip {
		widest += setCarouselChip + 8
	}
	return setCarouselClamp(widest, h, min, max)
}

// setCarouselClamp turns the widest text into the control's width.
func setCarouselClamp(widest, h, min, max int32) int32 {
	w := widest + 2*setCarouselArrow(h) + 28
	if w < min {
		w = min
	}
	if max > 0 && w > max {
		w = max
	}
	return w
}

// setCarouselChip is the diameter of the colour chip a swatch value shows.
const setCarouselChip = int32(14)

// setCarouselLayout splits rect into the arrow buttons and the value well.
func setCarouselLayout(rect sdl.Rect) setCarouselParts {
	a := setCarouselArrow(rect.H)
	ay := rect.Y + (rect.H-a)/2
	return setCarouselParts{
		Prev:  sdl.Rect{X: rect.X, Y: ay, W: a, H: a},
		Value: sdl.Rect{X: rect.X + a + 6, Y: rect.Y, W: rect.W - 2*a - 12, H: rect.H},
		Next:  sdl.Rect{X: rect.X + rect.W - a, Y: ay, W: a, H: a},
	}
}

// setCarouselDraw paints the carousel: two round glass arrow buttons and the
// value centred between them, fitted to the space with an ellipsis as a last
// resort. focused lights the arrows in the accent so the row says Left/Right
// will change it; an arrow that has nowhere to go is drawn dimmed. hoverPart is
// 0 for none, -1 for the previous arrow and +1 for the next one. A swatch with
// a non-zero alpha is drawn as a colour chip before the value, ringed dark and
// light so any colour reads on any theme.
func setCarouselDraw(renderer *sdl.Renderer, font *ttf.Font, rect sdl.Rect, value string, swatch sdl.Color, focused, canPrev, canNext bool, hoverPart int) setCarouselParts {
	parts := setCarouselLayout(rect)
	if renderer == nil || font == nil || rect.W <= 0 || rect.H <= 0 {
		return parts
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	arrow := func(r sdl.Rect, dir int, enabled, hovered bool) {
		fill := glassA(16)
		ink := ColorTextSecondary()
		if focused || hovered {
			fill = glassA(28)
			ink = ColorTextPrimary()
		}
		if focused && enabled {
			ink = glassAccent()
		}
		if !enabled {
			fill = glassA(8)
			ink = WithAlpha(ColorTextTertiary(), 120)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, fill)
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, 1, glassA(40))
		setDrawChevron(renderer, r.X+r.W/2, r.Y+r.H/2, r.H/5, dir, ink)
	}
	arrow(parts.Prev, -1, canPrev, hoverPart < 0)
	arrow(parts.Next, 1, canNext, hoverPart > 0)

	chip := int32(0)
	if swatch.A > 0 {
		chip = setCarouselChip + 8
	}
	text := ellipsize(font, value, parts.Value.W-chip)
	tw, th := measureText(font, text)
	x := parts.Value.X + (parts.Value.W-tw-chip)/2
	if chip > 0 {
		d := setCarouselChip
		cy := parts.Value.Y + parts.Value.H/2
		fillCircle(renderer, x+d/2, cy, d/2+2, sdl.Color{R: 0, G: 0, B: 0, A: 120})
		fillCircle(renderer, x+d/2, cy, d/2+1, sdl.Color{R: 255, G: 255, B: 255, A: 150})
		swatch.A = 255
		fillCircle(renderer, x+d/2, cy, d/2, swatch)
		x += chip
	}
	drawText(renderer, font, text, x, parts.Value.Y+(parts.Value.H-th)/2, ColorTextPrimary(), textAlignLeft)
	return parts
}

// setCarouselHit reports which part of a carousel a point lands on: -1 the
// previous arrow, +1 the next one, 0 the value, and false for a miss. The
// arrows use the shared touch-target padding so a fingertip that lands just
// beside one still steps the value.
func setCarouselHit(parts setCarouselParts, x, y int32) (int, bool) {
	if parts.Prev.W > 0 && hitTouchTarget(parts.Prev, x, y) && x < parts.Value.X {
		return -1, true
	}
	if parts.Next.W > 0 && hitTouchTarget(parts.Next, x, y) && x >= parts.Value.X+parts.Value.W {
		return 1, true
	}
	if parts.Value.W > 0 && pointInRect(x, y, parts.Value) {
		return 0, true
	}
	return 0, false
}

// setCycleIndex steps i by dir through n values, wrapping at both ends.
func setCycleIndex(i, dir, n int) int {
	if n <= 0 {
		return 0
	}
	i = (i + dir) % n
	if i < 0 {
		i += n
	}
	return i
}
