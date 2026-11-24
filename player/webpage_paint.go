package main

// webpage_paint.go — paint accuracy helpers for the styled web reader.
//
//   * box-shadow keeps its blur and spread and paints a soft falloff under the
//     element's own background, instead of a hard offset copy of the box that
//     read as a dark border on every card;
//   * a shadow list is read layer by layer, so the first visible outer layer
//     wins: an inset layer or Tailwind's transparent `0 0 #0000` ring
//     placeholders no longer hide the real shadow behind them;
//   * transform: translate()/translateX()/translateY()/translate3d() is read
//     for absolutely and fixed positioned boxes, whose `left: 50%;
//     transform: translate(-50%, -50%)` centring otherwise lands half a box
//     off. Rotation, scaling and skewing stay ignored so text stays upright.

import (
	"math"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// webShadowMaxBlur bounds a shadow's blur radius in device pixels; a
	// 200px glow would otherwise tint half the screen for one card.
	webShadowMaxBlur = 96
	// webShadowMaxSteps bounds the translucent layers one soft shadow costs.
	webShadowMaxSteps = 6
)

// webShadow is one layer of a box-shadow or text-shadow list, in CSS px.
type webShadow struct {
	X, Y     float64
	Blur     float64
	Spread   float64
	Color    sdl.Color
	HasColor bool
	Inset    bool
}

// webShadowLayers splits a shadow list on its top-level commas (the commas of
// an rgba() colour stay inside its parentheses) and reads every layer.
func webShadowLayers(value string, fontPx float64) []webShadow {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "none") {
		return nil
	}
	var out []webShadow
	for _, part := range webSplitTopLevel(value, ',') {
		if s, ok := webShadowLayer(part, fontPx); ok {
			out = append(out, s)
		}
	}
	return out
}

// webShadowLayer reads `[inset] <x> <y> [<blur> [<spread>]] [<color>]` in any
// order of the colour and the inset keyword.
func webShadowLayer(value string, fontPx float64) (webShadow, bool) {
	var s webShadow
	var lengths []float64
	for _, field := range webCSSSpaceFields(strings.TrimSpace(value)) {
		if strings.EqualFold(field, "inset") {
			s.Inset = true
			continue
		}
		if c, valid := webColorInValue(field); valid {
			s.Color, s.HasColor = c, true
			continue
		}
		if webShadowLength(field) {
			lengths = append(lengths, webLength(field, fontPx))
		}
	}
	if len(lengths) < 2 {
		return webShadow{}, false
	}
	s.X, s.Y = lengths[0], lengths[1]
	if len(lengths) > 2 && lengths[2] > 0 {
		s.Blur = lengths[2]
	}
	if len(lengths) > 3 {
		s.Spread = lengths[3]
	}
	return s, true
}

// webShadowVisible reports whether a layer can put any ink outside its box.
// A transparent colour, or a shadow with no offset, blur or spread, paints
// nothing; frameworks emit such placeholders ahead of the real shadow.
func webShadowVisible(s webShadow) bool {
	if s.HasColor && s.Color.A == 0 {
		return false
	}
	return s.X != 0 || s.Y != 0 || s.Blur > 0 || s.Spread > 0
}

// webFirstOuterShadow returns the first visible outer layer of a box-shadow
// list. Inset layers paint inside the box and are not modelled.
func webFirstOuterShadow(value string, fontPx float64) (webShadow, bool) {
	for _, s := range webShadowLayers(value, fontPx) {
		if !s.Inset && webShadowVisible(s) {
			return s, true
		}
	}
	return webShadow{}, false
}

// webFirstTextShadow returns the first visible layer of a text-shadow list.
func webFirstTextShadow(value string, fontPx float64) (webShadow, bool) {
	for _, s := range webShadowLayers(value, fontPx) {
		if webShadowVisible(s) {
			return s, true
		}
	}
	return webShadow{}, false
}

// webTextShadowSoften scales a blurred text shadow's alpha. The reader draws
// one sharp copy of the glyphs; a blurred shadow spreads the same ink over
// blur pixels, so its peak is fainter. Without this a soft `0 2px 4px` heading
// shadow drew as a hard dark double of the text.
func webTextShadowSoften(c sdl.Color, blur float64) sdl.Color {
	if blur <= 0 {
		return c
	}
	f := 2 / (2 + blur)
	if f < 0.25 {
		f = 0.25
	}
	c.A = uint8(math.Round(float64(c.A) * f))
	return c
}

// emitBoxShadow records an element's box-shadow for box, its painted box in
// layout-local coordinates. The Backgrounds entry is what paints: it sits
// right before the element's own background in CSS order, so the shadow is
// under the box instead of tinting it. The matching Rects entry keeps the
// geometry every consumer of layout.Rects has always seen; the rect painters
// skip it, like a BackgroundFill entry.
func (l *webLayouter) emitBoxShadow(style webStyle, box sdl.Rect, radius int32, z int) {
	if l == nil || l.layout == nil || !style.HasBoxShadow || style.Invisible || box.W <= 0 || box.H <= 0 {
		return
	}
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	color := sdl.Color{R: 0, G: 0, B: 0, A: 70}
	if style.HasBoxShadowColor {
		color = style.BoxShadowColor
	}
	if color.A == 0 {
		return
	}
	paper := sdl.Color{R: 255, G: 255, B: 255, A: 255}
	if l.doc != nil {
		paper = l.doc.Paper
	}
	color = webFadeColor(color, paper, style)
	rect := sdl.Rect{
		X: box.X + int32(math.Round(style.BoxShadowX*scale)),
		Y: box.Y + int32(math.Round(style.BoxShadowY*scale)),
		W: box.W, H: box.H,
	}
	blur := int32(math.Round(style.BoxShadowBlur * scale))
	if blur < 0 {
		blur = 0
	}
	if blur > webShadowMaxBlur {
		blur = webShadowMaxBlur
	}
	spread := int32(math.Round(style.BoxShadowSpread * scale))
	if spread > webShadowMaxBlur {
		spread = webShadowMaxBlur
	}
	l.layout.Rects = append(l.layout.Rects, webRect{
		Rect: rect, Color: color, Z: z - 1, Radius: radius,
		Shadow: true, Blur: blur, Spread: spread,
	})
	l.layout.Backgrounds = append(l.layout.Backgrounds, webBackgroundPaint{
		Rect: rect, Color: color, Radius: radius, Z: z,
		Shadow: true, Blur: blur, Spread: spread,
	})
}

// drawWebShadowPaint paints a Backgrounds shadow entry whose layout rect now
// starts at (x, y) on screen. view is the visible page rect: a shadow whose
// blurred extent cannot reach it costs nothing.
func drawWebShadowPaint(r *sdl.Renderer, bg webBackgroundPaint, x, y int32, view sdl.Rect) {
	ext := bg.Blur + max32(bg.Spread, 0)
	if bg.Rect.W <= 0 || bg.Rect.H <= 0 || y+bg.Rect.H+ext <= view.Y || y-ext >= view.Y+view.H {
		return
	}
	drawWebBoxShadow(r, sdl.Rect{X: x, Y: y, W: bg.Rect.W, H: bg.Rect.H}, bg.Radius, bg.Blur, bg.Spread, bg.Color)
}

// drawWebBoxShadow paints one box shadow. rect is the box already moved by the
// shadow offset; spread grows (or shrinks) it first. A hard shadow is one
// fill. A blurred one is a bounded stack of translucent rounded rects spread
// over ±blur around the edge, each faint enough that the whole stack reaches
// the authored alpha: dense under the box, about half strength at its edge
// and gone blur pixels outside it, which is the shape of the Gaussian a
// browser draws. Each layer is one geometry fan (webPaintFill); where SDL
// cannot draw geometry every layer reuses one cached shape scaled to its size,
// so a shadow costs a few blits rather than a texture per layer.
func drawWebBoxShadow(r *sdl.Renderer, rect sdl.Rect, radius, blur, spread int32, color sdl.Color) {
	if r == nil || color.A == 0 {
		return
	}
	rect = sdl.Rect{X: rect.X - spread, Y: rect.Y - spread, W: rect.W + 2*spread, H: rect.H + 2*spread}
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	if radius > 0 {
		radius = max32(radius+spread, 0)
	}
	if half := min32(rect.W, rect.H) / 2; radius > half {
		radius = half
	}
	if blur <= 0 {
		webPaintFill(r, rect.X, rect.Y, rect.W, rect.H, radius, color)
		return
	}
	steps := blur / 3
	if steps < 2 {
		steps = 2
	}
	if steps > webShadowMaxSteps {
		steps = webShadowMaxSteps
	}
	a := 1 - math.Pow(1-float64(color.A)/255, 1/float64(steps))
	layer := color
	layer.A = uint8(math.Max(1, math.Min(255, math.Round(a*255))))
	// A blurred corner is rounder than the box's own, and the outer layers
	// (the shape scaled up) round further still.
	shapeR := radius + blur/2
	if half := min32(rect.W, rect.H) / 2; shapeR > half {
		shapeR = half
	}
	for i := int32(0); i < steps; i++ {
		// From the outermost layer (+blur) to the innermost one (-blur).
		e := int32(math.Round(float64(blur) * (1 - (2*float64(i)+1)/float64(steps))))
		dst := sdl.Rect{X: rect.X - e, Y: rect.Y - e, W: rect.W + 2*e, H: rect.H + 2*e}
		if dst.W <= 0 || dst.H <= 0 {
			continue
		}
		if !webGeomFailed {
			// One fan per layer, its corner growing with the layer as a blur
			// rounds it (webpage_paintfast.go): no texture baked per shadow.
			webPaintFill(r, dst.X, dst.Y, dst.W, dst.H, max32(shapeR+e, 0), layer)
			continue
		}
		fillRoundedRectAt(r, rect, dst, shapeR, layer)
	}
}

// webTranslateValue reads the translation part of a CSS transform list.
// translate(), translateX(), translateY() and translate3d() add up in order;
// rotate, scale, skew and matrix are ignored so the box stays upright and
// readable. Lengths come back in CSS px and percentages (of the element's own
// border box) separately; ok is false when nothing in the list translates.
func webTranslateValue(value string, fontPx float64) (x, y, pctX, pctY float64, ok bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" || v == "none" {
		return 0, 0, 0, 0, false
	}
	for _, fn := range webCSSSpaceFields(v) {
		open := strings.IndexByte(fn, '(')
		if open <= 0 || !strings.HasSuffix(fn, ")") {
			continue
		}
		name := strings.TrimSpace(fn[:open])
		args := webSplitTopLevel(fn[open+1:len(fn)-1], ',')
		arg := func(i int) (float64, float64, bool) {
			if i >= len(args) {
				return 0, 0, false
			}
			return webTranslateLength(args[i], fontPx)
		}
		switch name {
		case "translate", "translate3d":
			if px, pct, valid := arg(0); valid {
				x, pctX, ok = x+px, pctX+pct, true
			}
			if px, pct, valid := arg(1); valid {
				y, pctY, ok = y+px, pctY+pct, true
			}
		case "translatex":
			if px, pct, valid := arg(0); valid {
				x, pctX, ok = x+px, pctX+pct, true
			}
		case "translatey":
			if px, pct, valid := arg(0); valid {
				y, pctY, ok = y+px, pctY+pct, true
			}
		}
	}
	return x, y, pctX, pctY, ok
}

// webTranslateLength reads one translate() argument: a length in CSS px or a
// signed percentage of the element's own box.
func webTranslateLength(value string, fontPx float64) (px, pct float64, ok bool) {
	a := strings.TrimSpace(value)
	if strings.HasSuffix(a, "%") {
		f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(a, "%")), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, 0, false
		}
		return 0, f, true
	}
	if !webShadowLength(a) {
		return 0, 0, false
	}
	px = webLength(a, fontPx)
	if math.IsNaN(px) || math.IsInf(px, 0) {
		return 0, 0, false
	}
	return px, 0, true
}

// webInsetPercent returns the percentage of a top/right/bottom/left value, or
// 0 when it is a length or auto. A positioned box resolves it against its
// containing block in layoutLayer; webLength alone would read `50%` as half
// the font size.
func webInsetPercent(value string) float64 {
	v := strings.TrimSpace(value)
	if !strings.HasSuffix(v, "%") {
		return 0
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(v, "%")), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

// webInsetPx is one inset in layout pixels: its percentage of extent (the
// containing block's width or height) when it was written as one, else its
// CSS px length.
func webInsetPx(px, pct float64, extent int32, scale float64) int32 {
	if pct != 0 {
		return int32(math.Round(float64(extent) * pct / 100))
	}
	return int32(math.Round(px * scale))
}

// webInsetPercents expands the one-to-four values of an `inset` shorthand
// into top/right/bottom/left percentages, the same way webBoxShorthand
// expands its lengths.
func webInsetPercents(value string) [4]float64 {
	var out [4]float64
	fields := webCSSSpaceFields(value)
	switch len(fields) {
	case 0:
		return out
	case 1:
		p := webInsetPercent(fields[0])
		out = [4]float64{p, p, p, p}
	case 2:
		v, h := webInsetPercent(fields[0]), webInsetPercent(fields[1])
		out = [4]float64{v, h, v, h}
	case 3:
		h := webInsetPercent(fields[1])
		out = [4]float64{webInsetPercent(fields[0]), h, webInsetPercent(fields[2]), h}
	default:
		out = [4]float64{webInsetPercent(fields[0]), webInsetPercent(fields[1]), webInsetPercent(fields[2]), webInsetPercent(fields[3])}
	}
	return out
}
