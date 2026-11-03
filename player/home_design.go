package main

// home_design.go — the home screen's visual language.
//
// One design system, shared by every control on the screen: translucent glass
// tiles over the ambient background, a 48px icon badge in the top-left of a
// tile with the label anchored bottom-left (never centred), a 20px tile radius
// against a 12px icon radius, and exactly one focus treatment — a cyan ring, a
// 3% lift and a soft glow. mockups/home-16x9-and-4x3.html is the reference
// sheet; the numbers here are its tokens in 1280x720 canvas units.

import (
	"math"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	homeSideMargin int32 = 32 // frame margin on both sides
	homeGutter     int32 = 16 // gap between every two tiles
	homeTileRadius int32 = 20
	homeIconRadius int32 = 12
	homeTilePad    int32 = 20
	homeIconSize   int32 = 48
	homeGlyphSize  int32 = 24
)

// homeGlass is white at the given alpha: the cards are frosted panes over the
// background, not opaque slabs, so the ambient glow reads through them.
func homeGlass(alpha uint8) sdl.Color {
	return sdl.Color{R: 255, G: 255, B: 255, A: alpha}
}

// homeCardFill is the resting tile fill (white 5%) and homeCardEdge its 8%
// border, straight from the design sheet's --card / --card-border.
func homeCardFill() sdl.Color      { return homeGlass(13) }
func homeCardFillFocus() sdl.Color { return homeGlass(20) }
func homeCardEdge() sdl.Color      { return homeGlass(20) }

// homeGlowBlob blits the soft radial glow into rect. One textured copy, no
// gradient math: the ambience behind the home screen is two of these.
func homeGlowBlob(renderer *sdl.Renderer, rect sdl.Rect, color sdl.Color, alpha uint8) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	glow := ambientGlowFor(renderer)
	if glow == nil {
		return
	}
	_ = glow.SetColorMod(color.R, color.G, color.B)
	_ = glow.SetAlphaMod(alpha)
	_ = renderer.Copy(glow, nil, &rect)
	_ = glow.SetColorMod(255, 255, 255)
	_ = glow.SetAlphaMod(255)
}

// lerpColorRGBA blends every channel, alpha included: the shared lerpColor keeps
// the source alpha, which silently turns a fade-to-black scrim into a no-op.
func lerpColorRGBA(a, b sdl.Color, t float32) sdl.Color {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	blend := func(x, y uint8) uint8 {
		return uint8(int(x) + int(float32(int(y)-int(x))*t))
	}
	return sdl.Color{R: blend(a.R, b.R), G: blend(a.G, b.G), B: blend(a.B, b.B), A: blend(a.A, b.A)}
}

// homeCornerInset is how far a scanline at `distance` from an edge has to be
// inset to stay inside a corner of the given radius.
func homeCornerInset(radius, distance int32) int32 {
	if radius <= 0 || distance >= radius {
		return 0
	}
	d := float64(radius - distance)
	return radius - int32(math.Sqrt(float64(radius*radius)-d*d))
}

// fillRoundedGradient paints a rounded rect with a colour ramp: vertical when
// horizontal is false, left-to-right when it is true. Each band follows the
// corner radius, so the ramp keeps the card's shape instead of stamping a
// square over the corners.
func fillRoundedGradient(renderer *sdl.Renderer, rect sdl.Rect, radius int32, from, to sdl.Color, horizontal bool) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	const step = 2
	if horizontal {
		for x := int32(0); x < rect.W; x += step {
			w := int32(step)
			if x+w > rect.W {
				w = rect.W - x
			}
			t := float64(x) / float64(rect.W)
			inset := homeCornerInset(radius, min32(x, rect.W-1-x))
			c := lerpColorRGBA(from, to, float32(t))
			setDrawColor(renderer, c)
			_ = renderer.FillRect(&sdl.Rect{X: rect.X + x, Y: rect.Y + inset, W: w, H: rect.H - 2*inset})
		}
		return
	}
	for y := int32(0); y < rect.H; y += step {
		h := int32(step)
		if y+h > rect.H {
			h = rect.H - y
		}
		t := float64(y) / float64(rect.H)
		inset := homeCornerInset(radius, min32(y, rect.H-1-y))
		c := lerpColorRGBA(from, to, float32(t))
		setDrawColor(renderer, c)
		_ = renderer.FillRect(&sdl.Rect{X: rect.X + inset, Y: rect.Y + y, W: rect.W - 2*inset, H: h})
	}
}

// drawHomeFocusGlow paints the soft cyan bloom behind a focused tile. It is
// drawn before the card's own art, so the artwork stays readable and the light
// reads as coming from behind the control.
func drawHomeFocusGlow(renderer *sdl.Renderer, rect sdl.Rect) {
	if renderer == nil || rect.W <= 0 {
		return
	}
	const glowPad = 26
	homeGlowBlob(renderer, sdl.Rect{
		X: rect.X - glowPad, Y: rect.Y - glowPad,
		W: rect.W + glowPad*2, H: rect.H + glowPad*2,
	}, ColorAccent, 70)
}

// drawHomeFocusRing paints the ring itself: 3px of cyan on the card's edge. It
// is drawn inside the rect so the lift never paints outside the control, and it
// shares the settled surface's cached shape.
func drawHomeFocusRing(renderer *sdl.Renderer, base, rect sdl.Rect, radius int32) {
	if renderer == nil || rect.W <= 0 {
		return
	}
	strokeRoundedRectAt(renderer, base, rect, radius, 3, ColorAccent)
}

// drawGlassTile paints a card: the frosted fill, its hairline border, and the
// focus ring on top. base/rect split keeps the fill texture cached while the
// focus lift animates.
func drawGlassTile(renderer *sdl.Renderer, base, rect sdl.Rect, focused, pressed bool) {
	if renderer == nil {
		return
	}
	fill := homeCardFill()
	if focused {
		fill = homeCardFillFocus()
	}
	if pressed {
		fill = sdl.Color{R: 255, G: 255, B: 255, A: 26}
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRectAt(renderer, base, rect, homeTileRadius, fill)
	if focused {
		drawHomeFocusRing(renderer, base, rect, homeTileRadius)
	} else {
		strokeRoundedRectAt(renderer, base, rect, homeTileRadius, 1, homeCardEdge())
	}
}

// drawIconBadge draws the 48px rounded badge in a tile's top-left corner and
// returns the box inside it the glyph is painted into.
func drawIconBadge(renderer *sdl.Renderer, rect sdl.Rect, tinted bool) sdl.Rect {
	fill := homeGlass(20)
	if tinted {
		fill = sdl.Color{R: 6, G: 32, B: 42, A: 140}
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, homeIconRadius, fill)
	if tinted {
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, homeIconRadius, 1, homeGlass(40))
	} else {
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, homeIconRadius, 1, homeGlass(16))
	}
	glyph := min32(homeGlyphSize, rect.W-rect.W/4)
	return sdl.Rect{
		X: rect.X + (rect.W-glyph)/2,
		Y: rect.Y + (rect.H-glyph)/2,
		W: glyph, H: glyph,
	}
}

// homeWavePattern is the amplitude profile of the Media tile's waveform. Fixed
// values keep it from shimmering between frames; it is decoration, not audio.
var homeWavePattern = []float64{
	0.45, 0.62, 0.38, 0.80, 0.95, 0.55, 0.30, 0.70, 1.0, 0.62,
	0.42, 0.88, 0.66, 0.34, 0.74, 0.92, 0.48, 0.58, 0.86, 0.40,
	0.68, 0.52, 0.78, 0.36, 0.60, 0.44,
}

// drawWaveform paints the low-alpha waveform that fills the right end of the
// Media tile.
func drawWaveform(renderer *sdl.Renderer, rect sdl.Rect, color sdl.Color) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 || len(homeWavePattern) == 0 {
		return
	}
	n := int32(len(homeWavePattern))
	gap := max32(2, rect.W/n/3)
	barW := max32(2, rect.W/n-gap)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	for i := int32(0); i < n; i++ {
		amp := homeWavePattern[i]
		bh := int32(float64(rect.H) * amp)
		if bh < 3 {
			bh = 3
		}
		x := rect.X + i*(barW+gap)
		if x+barW > rect.X+rect.W {
			break
		}
		setDrawColor(renderer, color)
		fillRoundedRect(renderer, x, rect.Y+(rect.H-bh)/2, barW, bh, barW/2, color)
	}
}

// drawProgressBar paints the Continue card's progress line: a track, a cyan
// fill and the glowing head the design sheet uses as the playhead.
func drawProgressBar(renderer *sdl.Renderer, track sdl.Rect, progress float32, color sdl.Color) {
	if renderer == nil || track.W <= 0 {
		return
	}
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, track.X, track.Y, track.W, track.H, track.H/2, homeGlass(51))
	fillW := int32(float32(track.W) * progress)
	if fillW <= 0 {
		return
	}
	if fillW < track.H {
		fillW = track.H
	}
	fillRoundedRect(renderer, track.X, track.Y, fillW, track.H, track.H/2, color)
	headR := max32(4, track.H/2+2)
	cx := track.X + fillW
	cy := track.Y + track.H/2
	homeGlowBlob(renderer, sdl.Rect{X: cx - headR*3, Y: cy - headR*3, W: headR * 6, H: headR * 6}, color, 120)
	fillCircle(renderer, cx, cy, headR, sdl.Color{R: 234, G: 252, B: 255, A: 255})
	strokeCircle(renderer, cx, cy, headR+2, sdl.Color{R: ColorAccent.R, G: ColorAccent.G, B: ColorAccent.B, A: 110})
}

// drawPlayBadge paints the round accent play button in the Continue card.
func drawPlayBadge(renderer *sdl.Renderer, rect sdl.Rect) {
	if renderer == nil || rect.W <= 0 {
		return
	}
	cx := rect.X + rect.W/2
	cy := rect.Y + rect.H/2
	r := min32(rect.W, rect.H) / 2
	homeGlowBlob(renderer, sdl.Rect{X: cx - r*2, Y: cy - r*2, W: r * 4, H: r * 4}, ColorAccent, 90)
	fillCircle(renderer, cx, cy, r, ColorAccent)
	tri := int32(r) * 7 / 10
	drawHomeGlyphTinted(renderer, "play", sdl.Rect{
		X: cx - tri/2, Y: cy - tri/2, W: tri, H: tri,
	}, ColorIconDark)
}

// drawContinueThumb paints the Continue card's artwork: a deep navy ramp, the
// two colour blooms the design sheet uses where a thumbnail would be, and a
// scrim that keeps the labels readable on any panel brightness.
func drawContinueThumb(renderer *sdl.Renderer, rect sdl.Rect) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	fillRoundedGradient(renderer, rect, homeTileRadius,
		sdl.Color{R: 11, G: 34, B: 50, A: 255}, sdl.Color{R: 8, G: 14, B: 26, A: 255}, false)
	// The blooms are clipped just inside the card so they cannot bleed past its
	// rounded corners into the next tile.
	inner := sdl.Rect{X: rect.X + 6, Y: rect.Y + 6, W: rect.W - 12, H: rect.H - 12}
	if inner.W > 0 && inner.H > 0 {
		_ = renderer.SetClipRect(&inner)
		homeGlowBlob(renderer, sdl.Rect{
			X: inner.X - inner.W/3, Y: inner.Y - inner.H/2,
			W: inner.W * 3 / 2, H: inner.W * 3 / 2,
		}, ColorAccent, 92)
		homeGlowBlob(renderer, sdl.Rect{
			X: inner.X + inner.W/4, Y: inner.Y - inner.H/3,
			W: inner.W, H: inner.W,
		}, sdl.Color{R: 124, G: 58, B: 237}, 84)
		_ = renderer.SetClipRect(nil)
	}
	// Scrim: transparent where the artwork shows, near-black behind the labels.
	// It starts without corner rounding and follows the card's radius only at
	// the bottom, so it cannot draw a seam across the middle of the card.
	scrimTop := rect.Y + rect.H/4
	fillVerticalScrim(renderer, rect, scrimTop,
		sdl.Color{R: 0, G: 0, B: 0, A: 0}, sdl.Color{R: 0, G: 0, B: 0, A: 200})
}

// fillVerticalScrim ramps from top to bottom inside a rounded card, starting at
// fromY: square where it begins, following the card's radius at the bottom.
func fillVerticalScrim(renderer *sdl.Renderer, card sdl.Rect, fromY int32, top, bottom sdl.Color) {
	if renderer == nil || card.W <= 0 || fromY >= card.Y+card.H {
		return
	}
	if fromY < card.Y {
		fromY = card.Y
	}
	start := fromY - card.Y
	span := card.H - start
	if span <= 0 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	const step = 2
	for y := int32(0); y < span; y += step {
		sh := int32(step)
		if y+sh > span {
			sh = span - y
		}
		t := float32(y) / float32(span)
		inset := homeCornerInset(homeTileRadius, min32(span-1-y, card.H-1-(start+y)))
		setDrawColor(renderer, lerpColorRGBA(top, bottom, t))
		_ = renderer.FillRect(&sdl.Rect{
			X: card.X + inset, Y: card.Y + start + y,
			W: card.W - 2*inset, H: sh,
		})
	}
}
