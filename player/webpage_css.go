package main

// webpage_css.go — the CSS properties the reader approximates rather than
// implements.
//
// The styled reader draws with its own row grid, not a box model, so some
// properties cannot be honoured exactly. Each helper here turns one of them
// into something the grid can do, and says so in a comment: a gradient becomes
// the average of its stops, opacity is blended into the colours that get
// painted, a line-height becomes extra row slots. The result is "looks like the
// site", never "pixel-identical to Chrome", and the approximations are chosen
// so a page never looks broken — an average of #1e3a8a and #3b82f6 is a blue
// header, not a grey one.

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

// webGradientColor turns a gradient into the average of its colour stops.
// The reader has no per-pixel painting, but the average keeps the header,
// button or hero card the colour the author intended instead of uncoloured.
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
