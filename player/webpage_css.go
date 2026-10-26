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
	"math"
	"strconv"
	"strings"

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

// webGradient is a linear CSS gradient resolved to stops the reader can paint.
//
// The reader draws rows rather than a raster, so a diagonal gradient is drawn
// on its dominant axis, and anything that is not a linear gradient — radial,
// conic, url() — is not a ramp at all and keeps the averaged fill.
type webGradient struct {
	Stops    []webGradientStop
	Vertical bool
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
		return lerpColor(prev.Color, next.Color, float32((t-prev.Pos)/span))
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
	out := webGradient{Vertical: g.Vertical, Stops: make([]webGradientStop, len(g.Stops))}
	for i, stop := range g.Stops {
		out.Stops[i] = webGradientStop{Pos: stop.Pos, Color: webFadeColor(stop.Color, behind, style)}
	}
	return out
}

// webParseGradient resolves linear-gradient() into a ramp the reader can
// paint. It reports false for anything that is not a linear gradient with at
// least two colour stops, so the caller can fall back to webGradientColor.
//
// repeating-linear-gradient() contains this name and is drawn as one pass:
// a row grid has no way to tile a ramp vertically and honestly.
func webParseGradient(value string) (webGradient, bool) {
	lower := strings.ToLower(value)
	idx := strings.Index(lower, "linear-gradient(")
	if idx < 0 {
		return webGradient{}, false
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

	type rawStop struct {
		color  sdl.Color
		pos    float64
		hasPos bool
	}
	raw := make([]rawStop, 0, len(parts)-start)
	for _, part := range parts[start:] {
		color, ok := webColorInValue(part)
		if !ok {
			continue
		}
		pos, hasPos := webGradientStopPercent(part)
		raw = append(raw, rawStop{color: color, pos: pos, hasPos: hasPos})
	}
	if len(raw) < 2 {
		return webGradient{}, false
	}

	// CSS leaves the ends implicit and spreads an unpositioned middle stop
	// evenly between its neighbours: `red, green, blue` is thirds.
	if !raw[0].hasPos {
		raw[0].pos, raw[0].hasPos = 0, true
	}
	if last := len(raw) - 1; !raw[last].hasPos {
		raw[last].pos, raw[last].hasPos = 1, true
	}
	for i := 0; i < len(raw); i++ {
		if raw[i].hasPos {
			continue
		}
		next := i + 1
		for next < len(raw) && !raw[next].hasPos {
			next++
		}
		right := 1.0
		if next < len(raw) {
			right = raw[next].pos
		}
		left := raw[i-1].pos
		span := float64(next - i + 1)
		for k := i; k < next; k++ {
			raw[k].pos = left + (right-left)*float64(k-i+1)/span
			raw[k].hasPos = true
		}
		i = next - 1
	}

	stops := make([]webGradientStop, 0, len(raw))
	prev := 0.0
	for _, stop := range raw {
		pos := stop.pos
		if pos < 0 {
			pos = 0
		}
		if pos > 1 {
			pos = 1
		}
		if pos < prev {
			pos = prev
		}
		prev = pos
		stops = append(stops, webGradientStop{Pos: pos, Color: stop.color})
	}
	if reversed {
		// Mirroring the ramp is one reverse plus 1-p on every stop; the
		// positions stay ascending because both operations flip order.
		for i, j := 0, len(stops)-1; i < j; i, j = i+1, j-1 {
			stops[i], stops[j] = stops[j], stops[i]
		}
		for i := range stops {
			stops[i].Pos = 1 - stops[i].Pos
		}
	}
	return webGradient{Stops: stops, Vertical: vertical}, true
}

// webGradientDirection reads the first argument of linear-gradient(). ok is
// false when that argument is a colour, which means the direction was left out.
func webGradientDirection(arg string) (vertical, reversed, ok bool) {
	d := strings.ToLower(strings.TrimSpace(arg))
	if d == "" {
		return true, false, false
	}
	if _, isColor := webColorInValue(d); isColor {
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

// webGradientStopPercent reads the trailing percentage of a colour stop. The
// colour function is stripped first, because rgb(100%, 0%, 0%) carries its own
// percents and they are not positions.
func webGradientStopPercent(part string) (float64, bool) {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(part); i++ {
		switch part[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteByte(part[i])
			}
		}
	}
	scan := b.String()
	i := strings.IndexByte(scan, '%')
	if i <= 0 {
		return 0, false
	}
	j := i
	for j > 0 {
		c := scan[j-1]
		if (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '+' {
			j--
			continue
		}
		break
	}
	if j == i {
		return 0, false
	}
	value, err := strconv.ParseFloat(scan[j:i], 64)
	if err != nil || math.IsNaN(value) {
		return 0, false
	}
	return value / 100, true
}

// webGradientColor turns a gradient into the average of its colour stops: the
// fallback for gradients that are not a linear ramp (radial, conic) and the
// colour text contrast is judged against. A ramp itself is painted by
// drawWebGradient from webParseGradient's stops.
func webGradientColor(value string) (sdl.Color, bool) {
	lower := strings.ToLower(value)
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
			if c, ok := webColorInValue(part); ok {
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
// tried rather than only the first.
func webListStyleValue(value string) int {
	for _, token := range strings.Fields(strings.ToLower(value)) {
		switch strings.Trim(token, ",") {
		case "none":
			return webListStyleNone
		case "disc":
			return webListStyleDisc
		case "circle":
			return webListStyleCircle
		case "square":
			return webListStyleSquare
		case "decimal", "decimal-leading-zero":
			return webListStyleDecimal
		case "lower-alpha", "lower-latin":
			return webListStyleLowerAlpha
		case "upper-alpha", "upper-latin":
			return webListStyleUpperAlpha
		case "lower-roman":
			return webListStyleLowerRoman
		case "upper-roman":
			return webListStyleUpperRoman
		}
	}
	return webListStyleDefault
}

// webListMarker renders one list item's marker. Ordered lists use the formatter
// the style asks for (decimal when the author said nothing), unordered ones the
// matching glyph; a marker list-style:none asked for is never drawn.
func webListMarker(style int, ordered bool, n int) string {
	if style == webListStyleNone {
		return ""
	}
	if ordered {
		if n < 1 {
			n = 1
		}
		switch style {
		case webListStyleLowerAlpha:
			return webAlphaMarker(n, false) + "."
		case webListStyleUpperAlpha:
			return webAlphaMarker(n, true) + "."
		case webListStyleLowerRoman:
			return strings.ToLower(webRomanMarker(n)) + "."
		case webListStyleUpperRoman:
			return webRomanMarker(n) + "."
		default:
			return strconv.Itoa(n) + "."
		}
	}
	switch style {
	case webListStyleCircle:
		return "◦"
	case webListStyleSquare:
		return "▪"
	default:
		return "•"
	}
}

// webAlphaMarker numbers like a spreadsheet: a..z, then aa, ab, ...  A page
// with more than 26 items is rare, but wrapping to "{}" would look broken.
func webAlphaMarker(n int, upper bool) string {
	if n < 1 {
		n = 1
	}
	letter := byte('a' + (n-1)%26)
	prefix := (n - 1) / 26
	if upper {
		letter = byte('A' + (n-1)%26)
	}
	if prefix == 0 {
		return string(letter)
	}
	return webAlphaMarker(prefix, upper) + string(letter)
}

// webRomanMarker formats 1..3999; anything larger falls back to decimal, which
// is what browsers do rather than drawing a nonsense numeral.
func webRomanMarker(n int) string {
	if n <= 0 || n > 3999 {
		return strconv.Itoa(n)
	}
	type numeral struct {
		value  int
		glyphs string
	}
	table := []numeral{
		{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"},
		{100, "C"}, {90, "XC"}, {50, "L"}, {40, "XL"},
		{10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"},
	}
	var b strings.Builder
	for _, entry := range table {
		for n >= entry.value {
			b.WriteString(entry.glyphs)
			n -= entry.value
		}
	}
	return b.String()
}
