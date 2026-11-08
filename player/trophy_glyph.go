package main

// trophy_glyph.go — the trophy itself: a cup in the tier's metal, the medallion
// it sits on, the shine that sweeps across it when it is earned, and the level
// ring of the trophy room's header.
//
// This is an illustration rather than an icon: a tier is told apart by its
// metal (a gradient from the metal's shadow to its highlight with a specular
// streak down the bowl), which a single-ink glyph from the shared icon set
// cannot carry. Everything that does not animate is drawn once into the shape
// cache (shapecache.go) and blitted after that — a whole grid of cups costs one
// textured copy each — and the shine, which moves, is a handful of scanlines.

import (
	"math"

	"github.com/veandco/go-sdl2/sdl"
)

// Shape-cache kinds for the trophy shapes. They are far from the cache's own
// small kinds so a new shared kind can never collide with them.
const (
	trophyShapeKindCup     = int32(9301)
	trophyShapeKindCupFlat = int32(9302)
	trophyShapeKindRing    = int32(9303)
)

// trophyMetal is one tier's metal: the shadow, the body, the highlight.
type trophyMetal struct {
	dark  sdl.Color
	base  sdl.Color
	light sdl.Color
}

// trophyMetals are the tier metals, Bronze to Platinum. Platinum is the cool
// blue-white the consoles use, so it never reads as a second silver.
var trophyMetals = [trophyTierCount]trophyMetal{
	{dark: sdl.Color{R: 104, G: 54, B: 20, A: 255}, base: sdl.Color{R: 205, G: 127, B: 50, A: 255}, light: sdl.Color{R: 250, G: 196, B: 138, A: 255}},
	{dark: sdl.Color{R: 96, G: 106, B: 122, A: 255}, base: sdl.Color{R: 190, G: 199, B: 211, A: 255}, light: sdl.Color{R: 250, G: 252, B: 255, A: 255}},
	{dark: sdl.Color{R: 140, G: 90, B: 10, A: 255}, base: sdl.Color{R: 242, G: 189, B: 62, A: 255}, light: sdl.Color{R: 255, G: 238, B: 168, A: 255}},
	{dark: sdl.Color{R: 70, G: 112, B: 168, A: 255}, base: sdl.Color{R: 168, G: 210, B: 246, A: 255}, light: sdl.Color{R: 242, G: 250, B: 255, A: 255}},
}

// trophyMetalOf returns a tier's metal; an out-of-range tier is bronze.
func trophyMetalOf(t trophyTier) trophyMetal {
	if int(t) >= len(trophyMetals) {
		return trophyMetals[0]
	}
	return trophyMetals[t]
}

// trophyTierColor is the tier's body colour, for dots, bars and glows.
func trophyTierColor(t trophyTier) sdl.Color {
	return trophyMetalOf(t).base
}

// trophyTierInk is the tier colour for text. On a light theme the body colours
// (silver above all) wash out against the glass, so the shadow tone is used.
func trophyTierInk(t trophyTier) sdl.Color {
	if glassLightTheme() {
		return trophyMetalOf(t).dark
	}
	m := trophyMetalOf(t)
	if t == trophyBronze {
		// Bronze's body is too dark to read as text on navy glass.
		return glassMix(m.base, m.light, 0.35)
	}
	return m.base
}

// trophyMix blends a toward b by t, keeping a's alpha.
func trophyMix(a, b sdl.Color, t float64) sdl.Color {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	mix := func(x, y uint8) uint8 {
		return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t))
	}
	return sdl.Color{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B), A: a.A}
}

// trophyMetalShade maps an intensity (0 = shadow, 0.5 = body, 1 = highlight)
// onto the metal.
func trophyMetalShade(m trophyMetal, intensity float64) sdl.Color {
	if intensity < 0.5 {
		return trophyMix(m.dark, m.base, intensity/0.5)
	}
	return trophyMix(m.base, m.light, (intensity-0.5)/0.5)
}

// ── Cup geometry ──────────────────────────────────────────────────────────────

// trophySpan is one horizontal run of the cup on a row, in box units (0..1).
type trophySpan struct {
	x0, x1 float64
	bowl   bool // part of the bowl: the specular streak runs down it
}

// trophyCupRow returns the cup's runs on the row at height v (0 = top of the
// box, 1 = bottom), in a fixed array so painting a row allocates nothing. The
// silhouette is a classic cup: a lip, a bowl that curves into a stem, a ring
// handle either side, a collar on the stem and a two-step plinth.
func trophyCupRow(v float64) ([5]trophySpan, int) {
	var spans [5]trophySpan
	n := 0
	add := func(x0, x1 float64, bowl bool) {
		if x1 <= x0 || n >= len(spans) {
			return
		}
		spans[n] = trophySpan{x0: x0, x1: x1, bowl: bowl}
		n++
	}
	switch {
	case v >= 0.10 && v < 0.155:
		// The lip, a little wider than the bowl under it.
		add(0.5-0.335, 0.5+0.335, true)
	case v >= 0.155 && v < 0.60:
		u := (v - 0.155) / 0.445
		hw := 0.315 * (1 - 0.80*math.Pow(u, 2.4))
		add(0.5-hw, 0.5+hw, true)
	case v >= 0.60 && v < 0.74:
		u := (v - 0.60) / 0.14
		hw := 0.045 + 0.022*u*u
		if v >= 0.615 && v < 0.655 {
			hw = 0.078 // the collar
		}
		add(0.5-hw, 0.5+hw, false)
	case v >= 0.74 && v < 0.79:
		add(0.5-0.16, 0.5+0.16, false)
	case v >= 0.79 && v < 0.90:
		add(0.5-0.215, 0.5+0.215, false)
	}
	// The handles: an elliptical ring on each side, centred on the bowl's edge.
	const (
		handleCY = 0.30
		outerRX  = 0.135
		outerRY  = 0.125
		innerRX  = 0.072
		innerRY  = 0.066
	)
	dy := v - handleCY
	if math.Abs(dy) < outerRY {
		ox := outerRX * math.Sqrt(1-(dy*dy)/(outerRY*outerRY))
		ix := 0.0
		if math.Abs(dy) < innerRY {
			ix = innerRX * math.Sqrt(1-(dy*dy)/(innerRY*innerRY))
		}
		for _, cx := range [2]float64{0.5 - 0.315, 0.5 + 0.315} {
			if ix > 0 {
				add(cx-ox, cx-ix, false)
				add(cx+ix, cx+ox, false)
			} else {
				add(cx-ox, cx+ox, false)
			}
		}
	}
	return spans, n
}

// trophyCupIntensity is the metal's lighting at a point: a cylinder lit from the
// upper left. t runs across the run (0 = left edge), v down the box.
func trophyCupIntensity(t, v float64) float64 {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	cyl := 0.28 + 0.72*math.Sin(math.Pi*math.Pow(t, 0.8))
	vert := 1.05 - 0.40*v
	i := cyl * vert
	if i < 0 {
		i = 0
	}
	if i > 1 {
		i = 1
	}
	return i
}

// trophyPaintCup paints the shaded cup into a w×h box at (ox, oy). Each run is
// split into a few bands of constant colour (cheap, and invisible at these
// sizes), and its two end pixels are faded by how much of them the edge covers,
// which is what keeps the silhouette smooth on a 4.96" panel.
func trophyPaintCup(renderer *sdl.Renderer, ox, oy, w, h int32, metal trophyMetal) {
	if renderer == nil || w <= 0 || h <= 0 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	const bands = 6
	fw := float64(w)
	for py := int32(0); py < h; py++ {
		v := (float64(py) + 0.5) / float64(h)
		spans, n := trophyCupRow(v)
		y := oy + py
		for s := 0; s < n; s++ {
			sp := spans[s]
			x0 := float64(ox) + sp.x0*fw
			x1 := float64(ox) + sp.x1*fw
			width := x1 - x0
			if width <= 0 {
				continue
			}
			for b := 0; b < bands; b++ {
				bx0 := x0 + width*float64(b)/bands
				bx1 := x0 + width*float64(b+1)/bands
				mid := (float64(b) + 0.5) / bands
				c := trophyMetalShade(metal, trophyCupIntensity(mid, v))
				if sp.bowl && mid > 0.18 && mid < 0.36 && v > 0.16 && v < 0.52 {
					// The specular streak down the bowl.
					c = trophyMix(c, sdl.Color{R: 255, G: 255, B: 255, A: 255}, 0.35)
				}
				px0 := int32(math.Ceil(bx0))
				px1 := int32(math.Floor(bx1)) - 1
				if px1 >= px0 {
					setDrawColor(renderer, c)
					_ = renderer.DrawLine(px0, y, px1, y)
				}
			}
			// Anti-aliased ends: the partly covered pixel at each edge.
			trophyEdgePixel(renderer, x0, y, trophyMetalShade(metal, trophyCupIntensity(0, v)), true)
			trophyEdgePixel(renderer, x1, y, trophyMetalShade(metal, trophyCupIntensity(1, v)), false)
		}
	}
}

// trophyPaintFlat paints the cup's silhouette in one colour: a locked trophy.
func trophyPaintFlat(renderer *sdl.Renderer, ox, oy, w, h int32, c sdl.Color) {
	if renderer == nil || w <= 0 || h <= 0 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fw := float64(w)
	c.A = 255
	for py := int32(0); py < h; py++ {
		v := (float64(py) + 0.5) / float64(h)
		spans, n := trophyCupRow(v)
		y := oy + py
		for s := 0; s < n; s++ {
			x0 := float64(ox) + spans[s].x0*fw
			x1 := float64(ox) + spans[s].x1*fw
			px0 := int32(math.Ceil(x0))
			px1 := int32(math.Floor(x1)) - 1
			if px1 >= px0 {
				setDrawColor(renderer, c)
				_ = renderer.DrawLine(px0, y, px1, y)
			}
			trophyEdgePixel(renderer, x0, y, c, true)
			trophyEdgePixel(renderer, x1, y, c, false)
		}
	}
}

// trophyEdgePixel draws the pixel an edge at x cuts through, at the alpha of the
// part of it inside the shape. left says which side of x is inside.
func trophyEdgePixel(renderer *sdl.Renderer, x float64, y int32, c sdl.Color, left bool) {
	px := math.Floor(x)
	cover := x - px // the part of pixel px left of the edge
	if left {
		cover = 1 - cover
	}
	if cover <= 0.04 {
		return
	}
	if cover > 1 {
		cover = 1
	}
	_ = renderer.SetDrawColor(c.R, c.G, c.B, uint8(float64(c.A)*cover))
	_ = renderer.DrawPoint(int32(px), y)
}

// trophyDrawCup draws the shaded cup of a tier into dst. The cup is cached at
// base×base and scaled into dst, so the celebration's pop (dst grows and
// shrinks) reuses one texture instead of minting one per frame.
func trophyDrawCup(renderer *sdl.Renderer, dst sdl.Rect, base int32, tier trophyTier) {
	if renderer == nil || dst.W <= 0 || dst.H <= 0 {
		return
	}
	if base <= 0 {
		base = dst.W
	}
	metal := trophyMetalOf(tier)
	drawShapeCachedAt(renderer, trophyShapeKindCup, base, base, dst, int32(tier), 0, metal.base,
		func(r *sdl.Renderer, ox, oy, w, h int32) { trophyPaintCup(r, ox, oy, w, h, metal) })
}

// trophyDrawCupFlat draws the cup's silhouette in c (alpha included): the
// locked trophy, and the tier glyph on a chip.
func trophyDrawCupFlat(renderer *sdl.Renderer, dst sdl.Rect, c sdl.Color) {
	if renderer == nil || dst.W <= 0 || dst.H <= 0 {
		return
	}
	drawShapeCachedAt(renderer, trophyShapeKindCupFlat, dst.W, dst.H, dst, 0, 0, c,
		func(r *sdl.Renderer, ox, oy, w, h int32) { trophyPaintFlat(r, ox, oy, w, h, c) })
}

// trophyDrawShine sweeps a bright diagonal band across the cup in dst. phase
// runs 0..1 over the sweep; outside it nothing is drawn. Only the rows the band
// crosses are touched, clipped to the cup's own runs, so the light stays on the
// metal instead of painting a stripe over the card.
func trophyDrawShine(renderer *sdl.Renderer, dst sdl.Rect, phase float64, strength uint8) {
	if renderer == nil || dst.W <= 0 || dst.H <= 0 || phase <= 0 || phase >= 1 || strength == 0 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	// The band's centre travels from left of the cup to right of it; it leans
	// right as it goes down (x + 0.45·v is constant along it).
	center := -0.35 + 1.7*phase
	const half = 0.085
	fw := float64(dst.W)
	for py := int32(0); py < dst.H; py++ {
		v := (float64(py) + 0.5) / float64(dst.H)
		b0 := center - half - 0.45*(v-0.5)
		b1 := center + half - 0.45*(v-0.5)
		spans, n := trophyCupRow(v)
		for s := 0; s < n; s++ {
			x0 := math.Max(spans[s].x0, b0)
			x1 := math.Min(spans[s].x1, b1)
			if x1 <= x0 {
				continue
			}
			px0 := dst.X + int32(math.Ceil(x0*fw))
			px1 := dst.X + int32(math.Floor(x1*fw)) - 1
			if px1 < px0 {
				continue
			}
			// The core of the band is brighter than its edges.
			coreA := strength
			edgeA := strength / 2
			_ = renderer.SetDrawColor(255, 255, 255, edgeA)
			_ = renderer.DrawLine(px0, dst.Y+py, px1, dst.Y+py)
			c0 := dst.X + int32(math.Ceil(math.Max(x0, center-half*0.45-0.45*(v-0.5))*fw))
			c1 := dst.X + int32(math.Floor(math.Min(x1, center+half*0.45-0.45*(v-0.5))*fw)) - 1
			if c1 >= c0 {
				_ = renderer.SetDrawColor(255, 255, 255, coreA)
				_ = renderer.DrawLine(c0, dst.Y+py, c1, dst.Y+py)
			}
		}
	}
}

// trophyDrawGlint is the four-pointed sparkle that catches the light at the end
// of a shine: two thin crossing strokes with a bright centre.
func trophyDrawGlint(renderer *sdl.Renderer, cx, cy, size int32, alpha uint8) {
	if renderer == nil || size < 4 || alpha == 0 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	arm := size / 2
	_ = renderer.SetDrawColor(255, 255, 255, alpha/2)
	_ = renderer.DrawLine(cx-arm, cy, cx+arm, cy)
	_ = renderer.DrawLine(cx, cy-arm, cx, cy+arm)
	short := arm / 2
	_ = renderer.SetDrawColor(255, 255, 255, alpha)
	_ = renderer.DrawLine(cx-short, cy, cx+short, cy)
	_ = renderer.DrawLine(cx, cy-short, cx, cy+short)
	_ = renderer.DrawLine(cx-1, cy-1, cx+1, cy+1)
	_ = renderer.DrawLine(cx-1, cy+1, cx+1, cy-1)
}

// trophyDrawMedallion is the disc a cup stands on: a rim of the metal's light,
// a body in its shadow and a soft glow of the tier colour in the middle, so a
// silver cup reads on a light theme as well as a dark one. A locked medallion
// is plain glass.
func trophyDrawMedallion(renderer *sdl.Renderer, cx, cy, radius int32, tier trophyTier, earned bool) {
	if renderer == nil || radius < 4 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if !earned {
		fillCircle(renderer, cx, cy, radius, glassA(26))
		fillCircle(renderer, cx, cy, radius-1, glassA(14))
		return
	}
	m := trophyMetalOf(tier)
	rim := m.light
	rim.A = 200
	fillCircle(renderer, cx, cy, radius, rim)
	body := trophyMix(m.dark, sdl.Color{R: 8, G: 12, B: 22, A: 255}, 0.45)
	fillCircle(renderer, cx, cy, radius-2, body)
	glow := radius * 3 / 2
	homeGlowBlob(renderer, sdl.Rect{X: cx - glow, Y: cy - glow, W: glow * 2, H: glow * 2}, m.base, 120)
}

// trophyDrawTrophy is the complete glyph in a square box: the medallion, the
// cup (metal when earned, a dim silhouette when locked) and, on a hidden trophy
// still to be earned, no cup at all — the caller prints its "?" instead.
func trophyDrawTrophy(renderer *sdl.Renderer, box sdl.Rect, tier trophyTier, earned, hidden bool) {
	if renderer == nil || box.W <= 0 || box.H <= 0 {
		return
	}
	size := min32(box.W, box.H)
	cx := box.X + box.W/2
	cy := box.Y + box.H/2
	trophyDrawMedallion(renderer, cx, cy, size/2, tier, earned)
	cup := size * 72 / 100
	dst := sdl.Rect{X: cx - cup/2, Y: cy - cup/2 + size/40, W: cup, H: cup}
	switch {
	case earned:
		trophyDrawCup(renderer, dst, cup, tier)
	case !hidden:
		trophyDrawCupFlat(renderer, dst, glassA(70))
	}
}

// ── Level ring ────────────────────────────────────────────────────────────────

// trophyPaintRing paints an annulus of the given outer radius and width, from
// the top clockwise through frac of a turn, into a (2r+2)² box at (ox, oy). Runs
// of covered pixels on a row become one line; the inner and outer edges are
// faded by distance, so the ring is smooth without supersampling.
func trophyPaintRing(renderer *sdl.Renderer, ox, oy, w, h int32, width int32, frac float64, c sdl.Color) {
	if renderer == nil || w <= 0 || h <= 0 || frac <= 0 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if frac > 1 {
		frac = 1
	}
	c.A = 255
	cxf := float64(w) / 2
	cyf := float64(h) / 2
	outer := math.Min(cxf, cyf) - 1
	inner := outer - float64(width)
	limit := frac * 2 * math.Pi
	for py := int32(0); py < h; py++ {
		dy := float64(py) + 0.5 - cyf
		runStart := int32(-1)
		for px := int32(0); px <= w; px++ {
			cover := 0.0
			if px < w {
				dx := float64(px) + 0.5 - cxf
				d := math.Hypot(dx, dy)
				cover = math.Min(math.Min(outer-d+0.5, d-inner+0.5), 1)
				if cover > 0 && frac < 1 {
					a := math.Atan2(dy, dx) + math.Pi/2
					if a < 0 {
						a += 2 * math.Pi
					}
					if a > limit {
						cover = 0
					}
				}
			}
			if cover >= 0.999 {
				if runStart < 0 {
					runStart = px
				}
				continue
			}
			if runStart >= 0 {
				setDrawColor(renderer, c)
				_ = renderer.DrawLine(ox+runStart, oy+py, ox+px-1, oy+py)
				runStart = -1
			}
			if cover > 0.04 && px < w {
				_ = renderer.SetDrawColor(c.R, c.G, c.B, uint8(255*cover))
				_ = renderer.DrawPoint(ox+px, oy+py)
			}
		}
	}
}

// trophyDrawRing draws a ring (or its first frac of a turn) centred on (cx, cy).
// Each (size, width, fraction in whole percent, colour) is one cached texture.
func trophyDrawRing(renderer *sdl.Renderer, cx, cy, radius, width int32, frac float64, c sdl.Color) {
	if renderer == nil || radius < 4 || width < 1 || frac <= 0 {
		return
	}
	percent := int32(math.Round(frac * 100))
	if percent < 1 {
		percent = 1
	}
	if percent > 100 {
		percent = 100
	}
	box := 2*radius + 2
	f := float64(percent) / 100
	dst := sdl.Rect{X: cx - box/2, Y: cy - box/2, W: box, H: box}
	drawShapeCachedAt(renderer, trophyShapeKindRing, box, box, dst, percent, width, c,
		func(r *sdl.Renderer, ox, oy, w, h int32) { trophyPaintRing(r, ox, oy, w, h, width, f, c) })
}
