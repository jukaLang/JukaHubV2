package main

// webpage_css.go — the CSS properties the reader approximates rather than
// implements.
//
// The styled reader draws with its own row grid, not a box model, so some
// properties cannot be honoured exactly. Each helper here turns one of them
// into something the grid can do, and says so in a comment: a linear gradient
// becomes a real multi-stop ramp along one axis (see webGradient), opacity is
// blended into the colours that get painted, a line-height becomes extra row
// slots. The result is "looks like the site", never "pixel-identical to
// Chrome", and the approximations are chosen so a page never looks broken — a
// two-stop blue header is a blue ramp, and a radial gradient falls back to the
// average of its stops so it is still blue rather than grey.

import (
	htmlstd "html"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

// List markers, in the order the parser and the bullet formatter share them.
const (
	webListStyleDefault = iota
	webListStyleDecimal
	webListStyleLowerAlpha
	webListStyleUpperAlpha
	webListStyleLowerRoman
	webListStyleUpperRoman
	webListStyleDisc
	webListStyleCircle
	webListStyleSquare
	webListStyleNone
	webListStyleDecimalZero
	webListStyleLowerGreek
	webListStyleDisclosureOpen
	webListStyleDisclosureClosed
	// webListStyleString is a CSS string marker (`list-style-type: "→ "`),
	// held in webStyle.ListString.
	webListStyleString
)

// webLineHeightPx resolves a line-height value to CSS px. A unitless number and
// a percentage are relative to the element's own font size, which is what makes
// `line-height: 1.5` behave the same everywhere; the keywords that mean "leave
// it to the reader" return ok=false.
func webLineHeightPx(value string, fontPx float64) (float64, bool) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || value == "normal" || value == "initial" || value == "inherit" {
		return 0, false
	}
	if fontPx <= 0 {
		fontPx = 16
	}
	if strings.HasSuffix(value, "%") {
		if pct, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(value, "%")), 64); err == nil {
			px := fontPx * pct / 100
			return webClampLineHeight(px)
		}
		return 0, false
	}
	// A bare number is a multiplier; a length carries a unit.
	if px, err := strconv.ParseFloat(value, 64); err == nil && !math.IsNaN(px) {
		return webClampLineHeight(px * fontPx)
	}
	if px := webLength(value, fontPx); px > 0 {
		return webClampLineHeight(px)
	}
	return 0, false
}

// webClampLineHeight keeps one author line inside what the row grid can draw:
// below one em the text would collide, and above four the grid spends the whole
// screen on a paragraph.
func webClampLineHeight(px float64) (float64, bool) {
	if px < 8 {
		return 0, false
	}
	if px > 96 {
		px = 96
	}
	return px, true
}

// webSpacingPx resolves letter-spacing/word-spacing. Negative values pull
// glyphs together, so they are kept, but bounded to one em so a hostile page
// cannot make a word's measured width negative. `normal` is zero.
func webSpacingPx(value string, fontPx float64) float64 {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "", "normal", "initial", "inherit", "unset", "revert":
		return 0
	}
	if fontPx <= 0 {
		fontPx = 16
	}
	px := webLength(value, fontPx)
	if px < -fontPx {
		px = -fontPx
	}
	if px > 64 {
		px = 64
	}
	return px
}

// webShadowLength recognises a shadow offset without resolving it. `0px` has
// to count as a length even though its value is zero: `box-shadow: 0 4px`
// uses a zero horizontal offset, and treating the token as absent dropped the
// whole shadow.
func webShadowLength(value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "0" {
		return true
	}
	for _, function := range []string{"calc(", "min(", "max(", "clamp("} {
		if strings.HasPrefix(v, function) {
			return true
		}
	}
	for _, unit := range []string{"px", "em", "rem", "ex", "ch", "vw", "vh", "vmin", "vmax", "%", "pt", "pc", "cm", "mm", "in"} {
		if strings.HasSuffix(v, unit) {
			return true
		}
	}
	return false
}

// webShadowValue reads the first shadow of a text-shadow/box-shadow value.
// The reader paints offsets and colour; blur and spread are not modelled, so
// they are ignored rather than turning the shadow off.
// Test seam (ARCH-8): only tests call it.
func webShadowValue(value string, fontPx float64) (x, y float64, color *sdl.Color, ok bool) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "none") {
		return 0, 0, nil, false
	}
	fields := webCSSSpaceFields(value)
	var lengths []float64
	for _, field := range fields {
		if c, valid := webColorInValue(field); valid {
			copy := c
			color = &copy
			continue
		}
		if webShadowLength(field) {
			lengths = append(lengths, webLength(field, fontPx))
		}
	}
	if len(lengths) < 2 {
		return 0, 0, color, false
	}
	return lengths[0], lengths[1], color, true
}

// webOpacityValue reads `opacity` (0..1, percentages accepted).
func webOpacityValue(value string) (float64, bool) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || value == "initial" || value == "inherit" {
		return 0, false
	}
	if strings.HasSuffix(value, "%") {
		if pct, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(value, "%")), 64); err == nil {
			return webClampOpacity(pct / 100), true
		}
		return 0, false
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(f) {
		return 0, false
	}
	return webClampOpacity(f), true
}

func webClampOpacity(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// webFadeColor blends a colour toward what is behind it, which is how the
// reader renders `opacity` without a compositing layer.
func webFadeColor(c, behind sdl.Color, style webStyle) sdl.Color {
	if !style.HasOpacity || style.Opacity >= 0.999 {
		return c
	}
	t := webClampOpacity(style.Opacity)
	blend := func(fg, bg uint8) uint8 {
		return uint8(math.Round(float64(fg)*t + float64(bg)*(1-t)))
	}
	return sdl.Color{R: blend(c.R, behind.R), G: blend(c.G, behind.G), B: blend(c.B, behind.B), A: c.A}
}

// webFirstLength reads the first length in a shorthand value: the reader draws
// one corner radius per box, so `border-radius: 8px 8px 0 0` uses 8px and
// `border-radius: 50%` (a circle) becomes a half-width radius at draw time.
func webFirstLength(value string, rel float64) float64 {
	value = strings.TrimSpace(value)
	if i := strings.IndexAny(value, "/ "); i > 0 {
		value = strings.TrimSpace(value[:i])
	}
	if strings.HasSuffix(value, "%") {
		// A percentage radius is relative to the box, which the parser does
		// not know yet; 999px is clamped to half the box at draw time and is
		// the honest reading of "as round as it can be" (a pill or circle).
		return 999
	}
	return webLength(value, rel)
}

// ── Gradients ────────────────────────────────────────────────────────────

// webGradientStop is one colour and where it sits along the ramp: 0 at the
// start, 1 at the end.
type webGradientStop struct {
	Pos   float64
	Color sdl.Color
}

// webGradient is a linear, radial or conic CSS gradient resolved to stops the
// reader can paint (webpage_gradient.go). A url() is not a ramp and keeps the
// averaged fill.
type webGradient struct {
	Stops []webGradientStop
	// Raw keeps the stops as written when one is placed in CSS px: where that
	// falls on the ramp depends on the box, so the painter resolves them
	// again for the box it draws (webGradientResolve); Stops assumes a box.
	Raw []webGradientRawStop
	// Repeating is repeating-linear-/radial-gradient(): the stops repeat past
	// the last one, as a stripe or ring pattern. Reversed is set when the stops
	// were mirrored for a ramp running up or left (the axis strip's own
	// direction), which the painter repeats after resolving Raw.
	Repeating bool
	Reversed  bool
	// Conic marks conic-gradient(): the stops run clockwise round (CX, CY)
	// from the angle From (CSS degrees, 0 is up), their positions fractions of
	// a turn.
	Conic bool
	From  float64
	// Vertical is the axis a linear ramp runs on — the one the band fallback
	// draws when the renderer has no geometry.
	Vertical bool
	// Angled marks a linear ramp off the four axes: Angle in CSS degrees (0
	// points up, clockwise), or a corner (CornerX, CornerY ±1: `to bottom
	// right`), whose angle follows the box's shape.
	Angled           bool
	Angle            float64
	CornerX, CornerY int8
	// Radial marks radial-gradient(): an ellipse, or a Circle, centred at
	// (CX, CY) — fractions of the box, or CSS px from its left and top edge
	// with CXPx/CYPx — whose ending shape is sized by Size (webRadial*); an
	// explicit one has radii RX, RY in CSS px, or fractions of the box with
	// RPct. Stop positions are fractions of that shape's radius.
	Radial     bool
	Circle     bool
	Size       uint8
	CX, CY     float64
	CXPx, CYPx bool
	RX, RY     float64
	RPct       bool
}

// webGradientRawStop is one colour stop as written: its colour and up to two
// positions (`red 0 25%` is two stops of one colour, a hard edge), each a
// fraction of the gradient line or turn, or CSS px when Px says so.
type webGradientRawStop struct {
	Color sdl.Color
	N     int
	Pos   [2]float64
	Px    [2]bool
}

// webGradientAt samples the ramp at t. Positions are non-decreasing and
// clamped to 0..1, so both tails are flat: that is what `red, blue 60%` looks
// like in a browser, where the first stop owns everything before it.
func (g webGradient) At(t float64) sdl.Color {
	if len(g.Stops) == 0 {
		return sdl.Color{}
	}
	if t <= g.Stops[0].Pos {
		return g.Stops[0].Color
	}
	last := len(g.Stops) - 1
	if t >= g.Stops[last].Pos {
		return g.Stops[last].Color
	}
	for i := 1; i <= last; i++ {
		next := g.Stops[i]
		if t > next.Pos {
			continue
		}
		prev := g.Stops[i-1]
		span := next.Pos - prev.Pos
		if span <= 0 {
			return next.Color
		}
		f := float32((t - prev.Pos) / span)
		color := lerpColor(prev.Color, next.Color, f)
		// lerpColor deliberately keeps the start alpha for the app's UI ramps,
		// but a CSS gradient often changes only alpha: a hero overlay written
		// as rgba(10,30,60,.8) -> rgba(10,30,60,.4) must fade the photograph
		// through, not paint both stops at the first stop's opacity.
		color.A = uint8(int(prev.Color.A) + int(float32(int(next.Color.A)-int(prev.Color.A))*f))
		return color
	}
	return g.Stops[last].Color
}

// webGradientAverage is the single fill a gradient still falls back to: text
// contrast decisions, and gradients the reader cannot draw as a ramp.
func webGradientAverage(g webGradient) sdl.Color {
	if len(g.Stops) == 0 {
		return sdl.Color{}
	}
	var r, gr, b, a float64
	for _, stop := range g.Stops {
		r += float64(stop.Color.R)
		gr += float64(stop.Color.G)
		b += float64(stop.Color.B)
		a += float64(stop.Color.A)
	}
	n := float64(len(g.Stops))
	return sdl.Color{
		R: uint8(math.Round(r / n)),
		G: uint8(math.Round(gr / n)),
		B: uint8(math.Round(b / n)),
		A: uint8(math.Round(a / n)),
	}
}

// webFadeGradient applies the element's opacity to every stop, so a faded card
// fades its whole ramp instead of only its average colour.
func webFadeGradient(g webGradient, behind sdl.Color, style webStyle) webGradient {
	if !style.HasOpacity || style.Opacity >= 0.999 {
		return g
	}
	out := g
	out.Stops = make([]webGradientStop, len(g.Stops))
	for i, stop := range g.Stops {
		out.Stops[i] = webGradientStop{Pos: stop.Pos, Color: webFadeColor(stop.Color, behind, style)}
	}
	return out
}

// webParseGradient resolves linear-gradient() into a ramp the reader can
// paint, and radial-gradient() too (webParseRadialGradient). It reports false
// for anything that is neither with at least two colour stops, so the caller
// can fall back to webGradientColor.
//
// repeating-linear-gradient() contains this name and is drawn as one pass:
// a row grid has no way to tile a ramp vertically and honestly.
func webParseGradient(value string) (webGradient, bool) {
	lower := asciiLower(value)
	idx := strings.Index(lower, "linear-gradient(")
	if idx < 0 {
		if g, ok := webParseRadialGradient(value); ok {
			return g, true
		}
		return webParseConicGradient(value)
	}
	open := idx + len("linear-gradient")
	end := webCSSParenEnd(value, open)
	if end <= open {
		return webGradient{}, false
	}
	parts := webSplitTopLevel(value[open+1:end], ',')
	if len(parts) < 2 {
		return webGradient{}, false
	}

	vertical, reversed, hasDirection := webGradientDirection(parts[0])
	if !hasDirection {
		// No direction means "to bottom", the CSS default.
		vertical, reversed = true, false
	}
	start := 0
	if hasDirection {
		start = 1
	}
	g := webGradient{Vertical: vertical, Repeating: webGradientRepeating(lower, idx)}
	if !webGradientParseStops(&g, parts[start:], false) {
		return webGradient{}, false
	}
	if reversed {
		g.Stops = webGradientMirror(g.Stops)
		g.Reversed = true
	}
	if hasDirection {
		// Off the four axes the painter draws it at its real angle
		// (webpage_gradient.go); the axis above is the band fallback. Stops
		// mirrored for an upward or leftward axis turn the direction round.
		if angle, cx, cy, axis := webGradientExact(parts[0]); !axis {
			if reversed {
				angle, cx, cy = math.Mod(angle+180, 360), -cx, -cy
			}
			g.Angled, g.Angle, g.CornerX, g.CornerY = true, angle, cx, cy
		}
	}
	return g, true
}

// webGradientRepeating reports whether the gradient function whose name ends
// at lower[idx:] is the repeating-… form.
func webGradientRepeating(lower string, idx int) bool {
	return idx >= len("repeating-") && lower[idx-len("repeating-"):idx] == "repeating-"
}

// webGradientMirror reverses a ramp: one reverse plus 1-p on every stop; the
// positions stay ascending because both operations flip order.
func webGradientMirror(stops []webGradientStop) []webGradientStop {
	for i, j := 0, len(stops)-1; i < j; i, j = i+1, j-1 {
		stops[i], stops[j] = stops[j], stops[i]
	}
	for i := range stops {
		stops[i].Pos = 1 - stops[i].Pos
	}
	return stops
}

// webGradientAssumedPx is the gradient length a stop placed in px is read
// against before the painter knows the box (webGradientResolve).
const webGradientAssumedPx = 400

// webGradientParseStops reads parts as g's colour stops: Stops for a line of
// webGradientAssumedPx CSS px, and Raw too when a stop is placed in px.
// angular reads a conic gradient's angles. false with fewer than two
// colours.
func webGradientParseStops(g *webGradient, parts []string, angular bool) bool {
	raw := make([]webGradientRawStop, 0, len(parts))
	hasPx := false
	for _, part := range parts {
		color, ok := webColorInValueAny(part)
		if !ok {
			continue
		}
		rs := webGradientRawStop{Color: color}
		rs.Pos, rs.Px, rs.N = webGradientStopPositions(part, angular)
		hasPx = hasPx || rs.Px[0] || rs.Px[1]
		raw = append(raw, rs)
	}
	if len(raw) < 2 {
		return false
	}
	g.Stops = webGradientNormalize(raw, webGradientAssumedPx, !g.Repeating)
	if hasPx {
		g.Raw = raw
	}
	return true
}

// webGradientNormalize turns written stops into a ramp, as CSS fixes them up:
// a two-position stop is two stops of its colour; px are fractions of a line
// pxLen CSS px long; the ends default to 0 and 1; an unpositioned middle stop
// is spread evenly between its neighbours (`red, green, blue` is thirds);
// positions never run backwards; and, unless clamp is false (a repeating
// pattern), they stay within 0..1.
func webGradientNormalize(raw []webGradientRawStop, pxLen float64, clamp bool) []webGradientStop {
	type flat struct {
		color  sdl.Color
		pos    float64
		hasPos bool
	}
	if pxLen <= 0 {
		pxLen = webGradientAssumedPx
	}
	list := make([]flat, 0, len(raw)+2)
	for _, r := range raw {
		if r.N == 0 {
			list = append(list, flat{color: r.Color})
			continue
		}
		for k := 0; k < r.N && k < 2; k++ {
			pos := r.Pos[k]
			if r.Px[k] {
				pos /= pxLen
			}
			list = append(list, flat{color: r.Color, pos: pos, hasPos: true})
		}
	}
	if !list[0].hasPos {
		list[0].pos, list[0].hasPos = 0, true
	}
	if last := len(list) - 1; !list[last].hasPos {
		list[last].pos, list[last].hasPos = 1, true
	}
	for i := 0; i < len(list); i++ {
		if list[i].hasPos {
			continue
		}
		next := i + 1
		for next < len(list) && !list[next].hasPos {
			next++
		}
		right := 1.0
		if next < len(list) {
			right = list[next].pos
		}
		left := list[i-1].pos
		span := float64(next - i + 1)
		for k := i; k < next; k++ {
			list[k].pos = left + (right-left)*float64(k-i+1)/span
			list[k].hasPos = true
		}
		i = next - 1
	}
	stops := make([]webGradientStop, 0, len(list))
	prev := math.Inf(-1)
	for _, stop := range list {
		pos := stop.pos
		if clamp {
			pos = math.Max(0, math.Min(1, pos))
		}
		if pos < prev {
			pos = prev
		}
		prev = pos
		stops = append(stops, webGradientStop{Pos: pos, Color: stop.color})
	}
	return webPremultiplyTransparentStops(stops)
}

// webGradientStopPositions reads the positions written after a stop's
// colour: up to two percentages, unitless zeros, lengths (CSS px) or, for a
// conic gradient (angular), angles — as fractions of the line or turn, or px.
func webGradientStopPositions(part string, angular bool) (pos [2]float64, px [2]bool, n int) {
	for _, tok := range webCSSSpaceFields(part) {
		if n == 2 {
			break
		}
		t := strings.ToLower(strings.TrimSpace(tok))
		if v, ok := webPercentValue(t); ok {
			pos[n] = v / 100
			n++
			continue
		}
		if t == "0" || t == "0px" {
			pos[n] = 0
			n++
			continue
		}
		if angular {
			if deg, ok := webGradientAngleRaw(t); ok {
				pos[n] = deg / 360
				n++
			}
			continue
		}
		if strings.HasSuffix(t, "px") || strings.HasSuffix(t, "em") {
			if l := webLength(t, 16); l != 0 && !math.IsNaN(l) && !math.IsInf(l, 0) {
				pos[n], px[n] = l, true
				n++
			}
		}
	}
	return pos, px, n
}

// webGradientAngleRaw reads an angle in degrees as written, not reduced to
// one turn: a conic stop at 360deg is the end of the turn, not its start.
func webGradientAngleRaw(t string) (float64, bool) {
	for _, unit := range [...]struct {
		suffix string
		factor float64
	}{{"deg", 1}, {"grad", 0.9}, {"rad", 180 / math.Pi}, {"turn", 360}} {
		if !strings.HasSuffix(t, unit.suffix) {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSuffix(t, unit.suffix), 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return n * unit.factor, true
	}
	return 0, false
}

// webPremultiplyTransparentStops gives every fully transparent stop the colour
// of its neighbours. CSS interpolates a gradient in premultiplied alpha, so
// `transparent, #000` fades black in and `#fff, transparent` fades white out;
// the reader blends colour and alpha separately, and transparent (black at
// alpha 0) used to darken a white fade into grey. A transparent stop between
// two different colours becomes two stops at its place, one per side.
func webPremultiplyTransparentStops(stops []webGradientStop) []webGradientStop {
	hasClear := false
	for _, s := range stops {
		if s.Color.A == 0 {
			hasClear = true
			break
		}
	}
	if !hasClear {
		return stops
	}
	out := make([]webGradientStop, 0, len(stops)+2)
	for i, s := range stops {
		if s.Color.A != 0 {
			out = append(out, s)
			continue
		}
		prev, next := -1, -1
		for j := i - 1; j >= 0; j-- {
			if stops[j].Color.A != 0 {
				prev = j
				break
			}
		}
		for j := i + 1; j < len(stops); j++ {
			if stops[j].Color.A != 0 {
				next = j
				break
			}
		}
		faded := func(c sdl.Color) webGradientStop {
			return webGradientStop{Pos: s.Pos, Color: sdl.Color{R: c.R, G: c.G, B: c.B}}
		}
		switch {
		case prev >= 0 && next >= 0:
			a, b := stops[prev].Color, stops[next].Color
			out = append(out, faded(a))
			if a.R != b.R || a.G != b.G || a.B != b.B {
				out = append(out, faded(b))
			}
		case prev >= 0:
			out = append(out, faded(stops[prev].Color))
		case next >= 0:
			out = append(out, faded(stops[next].Color))
		default:
			out = append(out, s)
		}
	}
	return out
}

// webGradientDirection reads the first argument of linear-gradient(). ok is
// false when that argument is a colour, which means the direction was left out.
func webGradientDirection(arg string) (vertical, reversed, ok bool) {
	d := strings.ToLower(strings.TrimSpace(arg))
	if d == "" {
		return true, false, false
	}
	if _, isColor := webColorInValueAny(d); isColor {
		return true, false, false
	}
	if angle, isAngle := webGradientAngle(d); isAngle {
		// CSS angles start at "to top" and run clockwise, the opposite of the
		// maths convention: 0deg points up, 90deg right, 180deg down, 270deg
		// left. The reader picks the axis the line points most along.
		switch {
		case angle >= 45 && angle < 135:
			return false, false, true // pointing right
		case angle >= 135 && angle < 225:
			return true, false, true // pointing down
		case angle >= 225 && angle < 315:
			return false, true, true // pointing left
		default:
			return true, true, true // pointing up
		}
	}
	// A corner direction keeps the vertical axis: the reader has rows, and a
	// ramp on the axis that moves the most is the honest reduction.
	switch {
	case strings.Contains(d, "top"):
		return true, true, true
	case strings.Contains(d, "bottom"):
		return true, false, true
	case strings.Contains(d, "right"):
		return false, false, true
	case strings.Contains(d, "left"):
		return false, true, true
	}
	return true, false, false
}

// webGradientAngle parses an angle keyword into degrees in 0..360. Only the
// units a page actually uses are supported; a bare number is not an angle.
func webGradientAngle(value string) (float64, bool) {
	v := strings.TrimSpace(value)
	if i := strings.IndexAny(v, " \t"); i > 0 {
		v = v[:i]
	}
	units := []struct {
		suffix string
		factor float64
	}{{"deg", 1}, {"turn", 360}, {"grad", 0.9}}
	for _, unit := range units {
		if !strings.HasSuffix(v, unit.suffix) {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSuffix(v, unit.suffix), 64)
		if err != nil || math.IsNaN(n) {
			return 0, false
		}
		a := math.Mod(n*unit.factor, 360)
		if a < 0 {
			a += 360
		}
		return a, true
	}
	return 0, false
}

// webGradientColor turns a gradient into the average of its colour stops: the
// fallback for gradients that are not a linear ramp (radial, conic) and the
// colour text contrast is judged against. A ramp itself is painted by
// drawWebGradient from webParseGradient's stops.
func webGradientColor(value string) (sdl.Color, bool) {
	lower := asciiLower(value)
	if !strings.Contains(lower, "gradient(") {
		return sdl.Color{}, false
	}
	var sumR, sumG, sumB, sumA, count float64
	for pos := 0; pos < len(value); {
		idx := strings.Index(lower[pos:], "gradient(")
		if idx < 0 {
			break
		}
		open := pos + idx + len("gradient")
		end := webCSSParenEnd(value, open)
		if end <= open {
			break
		}
		for _, part := range webSplitTopLevel(value[open+1:end], ',') {
			if c, ok := webColorInValueAny(part); ok {
				sumR += float64(c.R)
				sumG += float64(c.G)
				sumB += float64(c.B)
				sumA += float64(c.A)
				count++
			}
		}
		pos = end + 1
	}
	if count == 0 {
		return sdl.Color{}, false
	}
	return sdl.Color{
		R: uint8(math.Round(sumR / count)),
		G: uint8(math.Round(sumG / count)),
		B: uint8(math.Round(sumB / count)),
		A: uint8(math.Round(sumA / count)),
	}, true
}

// webSplitTopLevel splits a value on sep outside parentheses and quotes, so
// the commas inside rgb(1, 2, 3) and var(--x, #fff) do not split a stop.
func webSplitTopLevel(value string, sep byte) []string {
	var out []string
	depth := 0
	var quote byte
	start := 0
	for i := 0; i < len(value); i++ {
		c := value[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case sep:
			if depth == 0 {
				out = append(out, strings.TrimSpace(value[start:i]))
				start = i + 1
			}
		}
	}
	out = append(out, strings.TrimSpace(value[start:]))
	return out
}

// webListStyleValue reads list-style-type / list-style. A shorthand carries the
// marker among `inside`, `outside` and `url(...)` tokens, so every token is
// tried rather than only the first. A string (`"→ "`, CSS Lists 3) is a marker
// of its own, returned as text.
func webListStyleValue(value string) (int, string) {
	if q := strings.IndexAny(value, `"'`); q >= 0 {
		sp := webSelParser{src: value, pos: q}
		if text, ok := sp.str(); ok {
			return webListStyleString, text
		}
	}
	for _, token := range strings.Fields(strings.ToLower(value)) {
		switch strings.Trim(token, ",") {
		case "none":
			return webListStyleNone, ""
		case "disc":
			return webListStyleDisc, ""
		case "circle":
			return webListStyleCircle, ""
		case "square":
			return webListStyleSquare, ""
		case "decimal":
			return webListStyleDecimal, ""
		case "decimal-leading-zero":
			return webListStyleDecimalZero, ""
		case "lower-alpha", "lower-latin":
			return webListStyleLowerAlpha, ""
		case "upper-alpha", "upper-latin":
			return webListStyleUpperAlpha, ""
		case "lower-roman":
			return webListStyleLowerRoman, ""
		case "upper-roman":
			return webListStyleUpperRoman, ""
		case "lower-greek":
			return webListStyleLowerGreek, ""
		case "disclosure-open":
			return webListStyleDisclosureOpen, ""
		case "disclosure-closed":
			return webListStyleDisclosureClosed, ""
		case "armenian", "upper-armenian", "lower-armenian", "georgian", "hebrew", "cjk-decimal",
			"arabic-indic", "persian", "devanagari", "bengali", "thai", "khmer", "lao", "myanmar",
			"tamil", "telugu", "gujarati", "gurmukhi", "kannada", "malayalam", "oriya", "tibetan",
			"cjk-ideographic", "cjk-earthly-branch", "cjk-heavenly-stem", "hiragana", "katakana",
			"hiragana-iroha", "katakana-iroha", "japanese-informal", "japanese-formal", "korean-hangul-formal",
			"simp-chinese-informal", "simp-chinese-formal", "trad-chinese-informal", "trad-chinese-formal":
			// Numbering systems the reader's faces may not draw: numbered,
			// as a browser falls back to decimal for a style it lacks.
			return webListStyleDecimal, ""
		}
	}
	return webListStyleDefault, ""
}

// webListMarker renders one list item's marker from its list-style-type, not
// from its list's tag: `ul { list-style-type: decimal }` numbers and `ol {
// list-style: square }` does not. The reader default is decimal in an <ol>
// and a disc otherwise; a string marker is drawn as written, and a marker
// list-style:none asked for is never drawn. n is the item's list-item count.
func webListMarker(style int, custom string, ordered bool, n int) string {
	if style == webListStyleDefault {
		style = webListStyleDisc
		if ordered {
			style = webListStyleDecimal
		}
	}
	switch style {
	case webListStyleNone:
		return ""
	case webListStyleString:
		return strings.TrimRight(custom, " ")
	case webListStyleDisc:
		return "•"
	case webListStyleCircle:
		return "◦"
	case webListStyleSquare:
		return "▪"
	case webListStyleDisclosureOpen:
		return "▾"
	case webListStyleDisclosureClosed:
		return "▸"
	case webListStyleDecimalZero:
		return webCounterText(n, "decimal-leading-zero") + "."
	case webListStyleLowerAlpha:
		return webCounterText(n, "lower-alpha") + "."
	case webListStyleUpperAlpha:
		return webCounterText(n, "upper-alpha") + "."
	case webListStyleLowerRoman:
		return webCounterText(n, "lower-roman") + "."
	case webListStyleUpperRoman:
		return webCounterText(n, "upper-roman") + "."
	case webListStyleLowerGreek:
		return webCounterText(n, "lower-greek") + "."
	}
	return strconv.Itoa(n) + "."
}

// webDisplayKeywords turns CSS Display 3's multi-keyword display values into
// the one-keyword forms the reader reads (`block flex` is flex, `inline
// flow-root` inline-block, `inline grid` inline-grid), and reports a list item
// (`list-item`, `block list-item`, `block flow list-item`), which is a block
// that draws a marker. A single keyword comes back as it is.
func webDisplayKeywords(v string) (string, bool) {
	fields := strings.Fields(v)
	if len(fields) == 0 {
		return v, false
	}
	outer, inner, item := "", "", false
	for _, f := range fields {
		switch f {
		case "block", "inline", "run-in":
			outer = f
		case "flow", "flow-root", "flex", "grid", "table", "ruby":
			inner = f
		case "list-item":
			item = true
		default:
			return v, false
		}
	}
	inline := outer == "inline"
	switch {
	case item:
		if inline {
			return "inline", false
		}
		return "block", true
	case inner == "flex":
		if inline {
			return "inline-flex", false
		}
		return "flex", false
	case inner == "grid":
		if inline {
			return "inline-grid", false
		}
		return "grid", false
	case inner == "table":
		return "table", false
	case inner == "ruby":
		return "inline", false
	case inner == "flow-root":
		if inline {
			return "inline-block", false
		}
		return "flow-root", false
	case inline:
		return "inline", false
	}
	return "block", false
}

// webReversedListCounts is the number of items of each <ol> in the markup, in
// the order the parser meets the lists, for `<ol reversed>`, which counts down
// from its item count. It walks tags the way webBuildSelectorTree does, so raw
// text never counts; an item counts toward the list it sits straight in.
func webReversedListCounts(source string) []int {
	var counts []int
	var stack []int // the open lists: an <ol>'s index in counts, -1 for ul/menu
	for i := 0; i < len(source); {
		if source[i] != '<' {
			i++
			continue
		}
		if strings.HasPrefix(source[i:], "<!--") {
			end := strings.Index(source[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		if strings.HasPrefix(source[i:], "<!") || strings.HasPrefix(source[i:], "<?") {
			end := strings.IndexByte(source[i:], '>')
			if end < 0 {
				break
			}
			i += end + 1
			continue
		}
		end := findTextBrowserTagEnd(source, i+1)
		if end < 0 {
			break
		}
		name, closing, _, _ := parseWebTag(source[i+1 : end])
		i = end + 1
		switch {
		case name == "":
		case closing && (name == "ol" || name == "ul" || name == "menu"):
			if n := len(stack); n > 0 {
				stack = stack[:n-1]
			}
		case closing:
		case name == "title" || name == "style" || webRawTextElement(name):
			_, i, _ = webRawElement(source, i, name)
		case name == "ol":
			stack = append(stack, len(counts))
			counts = append(counts, 0)
		case name == "ul" || name == "menu":
			stack = append(stack, -1)
		case name == "li":
			if n := len(stack); n > 0 && stack[n-1] >= 0 {
				counts[stack[n-1]]++
			}
		}
	}
	return counts
}

// webFirstLetter is a pending ::first-letter: the pseudo-element's style and
// the stack depth of the element it belongs to.
type webFirstLetter struct {
	style webStyle
	depth int
}

// webFirstLetterHost reports whether an element can have a ::first-letter:
// a block container the parser pushes, not an inline, void or form element.
func webFirstLetterHost(name string, st *webStyle) bool {
	if st.DisplayInline || webVoidElement(name) {
		return false
	}
	switch name {
	case "button", "textarea", "select", "option", "svg", "img", "input", "br", "hr":
		return false
	}
	return name == "li" || webBlockElement(name) || st.DisplayBlock || st.DisplayInlineBlock || st.Display >= webDisplayTable
}

// webFirstLetterEnd is the end, in raw (still entity-encoded) text, of what
// ::first-letter styles: leading white space, punctuation before the letter
// (an opening quote), and the first letter or digit. 0 when the text has no
// letter yet, so the pseudo-element waits for the next text.
func webFirstLetterEnd(raw string) int {
	next := func(at int) (rune, int) {
		if raw[at] == '&' {
			if semi := strings.IndexByte(raw[at:], ';'); semi > 1 && semi <= 10 {
				dec := htmlstd.UnescapeString(raw[at : at+semi+1])
				if r, n := utf8.DecodeRuneInString(dec); n > 0 && n == len(dec) {
					return r, semi + 1
				}
			}
		}
		return utf8.DecodeRuneInString(raw[at:])
	}
	i := 0
	for i < len(raw) {
		r, n := next(i)
		if !unicode.IsSpace(r) {
			break
		}
		i += n
	}
	for i < len(raw) {
		r, n := next(i)
		switch {
		case unicode.IsPunct(r):
			i += n
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			return i + n
		default:
			return 0
		}
	}
	return 0
}

// webKeyframesName is the animation name of an @keyframes prelude.
func webKeyframesName(prelude string) string {
	fields := strings.Fields(prelude)
	if len(fields) < 2 {
		return ""
	}
	return strings.Trim(fields[1], `"'`)
}

// webKeyframesEnd is the declarations of an @keyframes body's end frame
// (`to` or `100%`, the last one when several name it), nil when it has none.
func webKeyframesEnd(body string) map[string]string {
	var end map[string]string
	for i := 0; i < len(body); {
		open := strings.IndexByte(body[i:], '{')
		if open < 0 {
			break
		}
		open += i
		close := webCSSBlockEnd(body, open)
		if close < 0 {
			break
		}
		for _, sel := range strings.Split(body[i:open], ",") {
			switch strings.ToLower(strings.TrimSpace(sel)) {
			case "to", "100%":
				decls, _ := parseWebDeclsSplit(body[open+1 : close])
				end = decls
			}
		}
		i = close + 1
	}
	return end
}

// webAnimationEndProps are the properties an animation's end state may set
// on the reader's static page: what shows or hides an element and where it
// sits, the things fade-ins, slide-ins and preloaders animate.
var webAnimationEndProps = map[string]bool{
	"opacity": true, "visibility": true, "transform": true, "translate": true,
	"color": true, "background-color": true, "top": true, "right": true, "bottom": true,
	"left": true, "inset": true, "max-height": true, "height": true,
}

// applyAnimationEnd puts the end state of the element's animation into its
// declarations when the animation keeps it (animation-fill-mode forwards or
// both) and ends (not infinite). The reader draws a page once, as it stands
// after its animations ran: content that fades in from `opacity: 0` was
// invisible, and a loading screen that fades out covered the page.
func (p *webParser) applyAnimationEnd(merged map[string]string) {
	if len(p.keyframes) == 0 {
		return
	}
	name, fill, infinite := "", "", false
	read := func(list string) {
		first, _, _ := strings.Cut(list, ",")
		for _, token := range strings.Fields(first) {
			lower := strings.ToLower(token)
			switch lower {
			case "forwards", "both", "backwards", "none":
				fill = lower
				continue
			case "infinite":
				infinite = true
				continue
			}
			if _, ok := p.keyframes[strings.Trim(token, `"'`)]; ok && name == "" {
				name = strings.Trim(token, `"'`)
			}
		}
	}
	if v, ok := merged["animation"]; ok {
		read(v)
	}
	if v, ok := merged["animation-name"]; ok {
		first, _, _ := strings.Cut(v, ",")
		if n := strings.Trim(strings.TrimSpace(first), `"'`); n != "" {
			name = ""
			if _, known := p.keyframes[n]; known {
				name = n
			}
		}
	}
	if v, ok := merged["animation-fill-mode"]; ok {
		first, _, _ := strings.Cut(v, ",")
		fill = strings.ToLower(strings.TrimSpace(first))
	}
	if v, ok := merged["animation-iteration-count"]; ok && strings.Contains(strings.ToLower(v), "infinite") {
		infinite = true
	}
	if name == "" || infinite || (fill != "forwards" && fill != "both") {
		return
	}
	for prop, value := range p.keyframes[name] {
		if webAnimationEndProps[prop] {
			merged[prop] = value
		}
	}
}
