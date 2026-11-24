package main

// webpage_color.go — CSS colour values the way a browser reads them.
//
// The reader used to know rgb(), hex and forty-odd colour names, a third of
// them approximations: "red" was a brick red, "blue" a mid blue, "orange" a
// burnt orange, and "darkgray" was darker than "gray". A sheet that wrote any
// other colour lost the declaration, so the text kept whatever colour it
// inherited and a box lost its fill. This file reads every colour form pages
// use:
//
//   - the 148 named colours with their exact sRGB values, and the system
//     colours (Canvas, CanvasText, LinkText, ButtonFace, Mark, …) as a
//     light-mode Chrome resolves them;
//   - hsl()/hsla() and hwb(), in the legacy comma and the modern space syntax,
//     with hues in deg, grad, rad or turn;
//   - lab(), lch(), oklab() and oklch() — Tailwind CSS 4 writes its whole
//     palette in oklch() — and color() in srgb, srgb-linear, display-p3,
//     a98-rgb, prophoto-rgb, rec2020 and the xyz spaces;
//   - color-mix(), which Tailwind 4 emits for every opacity modifier
//     (bg-black/50), mixed with premultiplied alpha in the space it names;
//   - light-dark(), answered for the light scheme the reader paints
//     (webMediaDiscrete), and the relative syntax (rgb(from var(--brand) r g b
//     / 50%)) where each channel is a channel keyword or a number.
//
// A colour outside sRGB is clipped per channel, as a display without a wide
// gamut shows it. The file also holds the colour arithmetic the reader's
// contrast and backdrop decisions use (webColorOver).

import (
	"math"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// webRGBA is a colour as a browser holds it before painting: gamma-encoded
// sRGB channels that may lie outside 0..1 (a wide-gamut colour), and alpha in
// 0..1.
type webRGBA struct {
	r, g, b, a float64
}

// toSDL clips the colour into the 8-bit sRGB the screen shows.
func (c webRGBA) toSDL() sdl.Color {
	ch := func(v float64) uint8 {
		if math.IsNaN(v) {
			return 0
		}
		return uint8(math.Round(math.Max(0, math.Min(1, v)) * 255))
	}
	return sdl.Color{R: ch(c.r), G: ch(c.g), B: ch(c.b), A: ch(c.a)}
}

// webColorOver composites fg over an opaque bg with fg's alpha: the colour a
// translucent fill or a translucent text colour shows on its backdrop.
func webColorOver(fg, bg sdl.Color) sdl.Color {
	if fg.A == 255 {
		return fg
	}
	t := float64(fg.A) / 255
	mix := func(a, b uint8) uint8 {
		return uint8(math.Round(float64(a)*t + float64(b)*(1-t)))
	}
	return sdl.Color{R: mix(fg.R, bg.R), G: mix(fg.G, bg.G), B: mix(fg.B, bg.B), A: 255}
}

// webCurrentColor is CSS currentColor for an element's border or outline when
// the sheet gives it no colour: the element's text colour as painted (black
// on a page that sets none), not the reader's old light-grey hairline.
func webCurrentColor(style webStyle, doc *webDocument) sdl.Color {
	if doc == nil {
		return sdl.Color{A: 255}
	}
	// The border painters apply the element's opacity themselves.
	style.HasOpacity = false
	return webResolveColor(style, doc)
}

// webSetBackground records a background colour; one with alpha 0 removes the
// surface instead.
func webSetBackground(st *webStyle, c sdl.Color) {
	if c.A == 0 {
		st.Background, st.HasBg = sdl.Color{}, false
		return
	}
	st.Background, st.HasBg = c, true
}

// webBackgroundShorthandClears reports a background shorthand that names no
// colour but does set the property: none, an image or a gradient. A shorthand
// resets every longhand it leaves out, so the background colour becomes
// transparent.
func webBackgroundShorthandClears(v string) bool {
	lower := strings.ToLower(strings.TrimSpace(v))
	switch lower {
	case "none", "initial", "unset", "0":
		return true
	}
	return strings.Contains(lower, "url(") || strings.Contains(lower, "gradient(") || strings.Contains(lower, "image-set(")
}

// webCSSColor reads one CSS colour value, already lower-cased and trimmed.
// transparent is a colour here (alpha 0); currentcolor has no value without an
// element and reports false, as does anything that is not a colour.
func webCSSColor(v string, depth int) (webRGBA, bool) {
	if v == "" || depth > 8 {
		return webRGBA{}, false
	}
	if v[0] == '#' {
		return webHexColor(v[1:])
	}
	open := strings.IndexByte(v, '(')
	if open < 0 {
		return webNamedColor(v)
	}
	if !strings.HasSuffix(v, ")") {
		return webRGBA{}, false
	}
	name := strings.TrimSpace(v[:open])
	inner := strings.TrimSpace(v[open+1 : len(v)-1])
	if strings.HasPrefix(inner, "from ") {
		return webRelativeColor(name, strings.TrimSpace(inner[len("from "):]), depth)
	}
	switch name {
	case "rgb", "rgba":
		return webRGBFunc(inner)
	case "hsl", "hsla":
		return webHSLFunc(inner, false)
	case "hwb":
		return webHSLFunc(inner, true)
	case "lab", "lch", "oklab", "oklch":
		return webLabFunc(name, inner)
	case "color":
		return webColorFunc(inner)
	case "color-mix":
		return webColorMix(inner, depth)
	case "light-dark":
		parts := webSplitTopLevel(inner, ',')
		if len(parts) != 2 {
			return webRGBA{}, false
		}
		return webCSSColor(parts[0], depth+1)
	}
	return webRGBA{}, false
}

// webHexColor reads #rgb, #rgba, #rrggbb and #rrggbbaa (without the #).
func webHexColor(hex string) (webRGBA, bool) {
	n := len(hex)
	if n != 3 && n != 4 && n != 6 && n != 8 {
		return webRGBA{}, false
	}
	var d [8]uint32
	for i := 0; i < n; i++ {
		c := hex[i]
		switch {
		case c >= '0' && c <= '9':
			d[i] = uint32(c - '0')
		case c >= 'a' && c <= 'f':
			d[i] = uint32(c-'a') + 10
		default:
			return webRGBA{}, false
		}
	}
	ch := [4]uint32{0, 0, 0, 255}
	if n <= 4 {
		// #rgb(a): each digit stands for itself twice (#f80 is #ff8800).
		for i := 0; i < n; i++ {
			ch[i] = d[i]<<4 | d[i]
		}
	} else {
		for i := 0; i < n/2; i++ {
			ch[i] = d[2*i]<<4 | d[2*i+1]
		}
	}
	return webRGBA{float64(ch[0]) / 255, float64(ch[1]) / 255, float64(ch[2]) / 255, float64(ch[3]) / 255}, true
}

// webNamedColor looks a colour keyword up: a named colour, a system colour, or
// transparent.
func webNamedColor(name string) (webRGBA, bool) {
	if name == "transparent" {
		return webRGBA{}, true
	}
	hex, ok := webCSSNamedColors[name]
	if !ok {
		hex, ok = webCSSSystemColors[name]
	}
	if !ok {
		return webRGBA{}, false
	}
	r := float64(hex>>16&0xff) / 255
	g := float64(hex>>8&0xff) / 255
	b := float64(hex&0xff) / 255
	return webRGBA{r, g, b, 1}, true
}

// webColorNumber reads one channel of a colour function: a number, a
// percentage (pct) or an angle, returned in degrees. none is a missing
// channel, which reads as zero.
func webColorNumber(s string) (v float64, pct, ok bool) {
	s = strings.TrimSpace(s)
	if s == "none" {
		return 0, false, true
	}
	scale := 1.0
	switch {
	case strings.HasSuffix(s, "%"):
		pct, s = true, s[:len(s)-1]
	case strings.HasSuffix(s, "deg"):
		s = s[:len(s)-3]
	case strings.HasSuffix(s, "grad"):
		s, scale = s[:len(s)-4], 0.9
	case strings.HasSuffix(s, "rad"):
		s, scale = s[:len(s)-3], 180/math.Pi
	case strings.HasSuffix(s, "turn"):
		s, scale = s[:len(s)-4], 360
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, false, false
	}
	return n * scale, pct, true
}

// webColorAlpha reads an alpha value: a number or a percentage, clamped to
// 0..1.
func webColorAlpha(s string) (float64, bool) {
	v, pct, ok := webColorNumber(s)
	if !ok {
		return 0, false
	}
	if pct {
		v /= 100
	}
	return math.Max(0, math.Min(1, v)), true
}

// webColorArgs splits a colour function's arguments into its channels and its
// alpha: `a b c / d`, or the legacy `a, b, c[, d]`.
func webColorArgs(inner string) (channels []string, alpha string, hasAlpha bool) {
	body := inner
	if i := strings.LastIndexByte(inner, '/'); i >= 0 {
		body, alpha, hasAlpha = inner[:i], strings.TrimSpace(inner[i+1:]), true
	}
	channels = strings.FieldsFunc(body, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f'
	})
	if !hasAlpha && len(channels) == 4 {
		alpha, hasAlpha = channels[3], true
		channels = channels[:3]
	}
	return channels, alpha, hasAlpha
}

// webWithAlpha gives c the alpha a colour function stated, if any.
func webWithAlpha(c webRGBA, alpha string, has bool) (webRGBA, bool) {
	if !has {
		return c, true
	}
	a, ok := webColorAlpha(alpha)
	if !ok {
		return webRGBA{}, false
	}
	c.a = a
	return c, true
}

// webRGBFunc reads rgb()/rgba(): numbers 0..255 or percentages.
func webRGBFunc(inner string) (webRGBA, bool) {
	ch, alpha, hasAlpha := webColorArgs(inner)
	if len(ch) != 3 {
		return webRGBA{}, false
	}
	var out [3]float64
	for i, s := range ch {
		v, pct, ok := webColorNumber(s)
		if !ok {
			return webRGBA{}, false
		}
		if pct {
			v = v * 255 / 100
		}
		out[i] = v / 255
	}
	return webWithAlpha(webRGBA{out[0], out[1], out[2], 1}, alpha, hasAlpha)
}

// webHSLFunc reads hsl()/hsla(), or hwb() when hwb is set: a hue and two
// percentages (a bare number is a percentage in the modern syntax).
func webHSLFunc(inner string, hwb bool) (webRGBA, bool) {
	ch, alpha, hasAlpha := webColorArgs(inner)
	if len(ch) != 3 {
		return webRGBA{}, false
	}
	h, _, ok1 := webColorNumber(ch[0])
	x, _, ok2 := webColorNumber(ch[1])
	y, _, ok3 := webColorNumber(ch[2])
	if !ok1 || !ok2 || !ok3 {
		return webRGBA{}, false
	}
	var r, g, b float64
	if hwb {
		r, g, b = webHWBToRGB(h, math.Max(0, x)/100, math.Max(0, y)/100)
	} else {
		r, g, b = webHSLToRGB(h, math.Max(0, x)/100, y/100)
	}
	return webWithAlpha(webRGBA{r, g, b, 1}, alpha, hasAlpha)
}

// webLabFunc reads lab(), lch(), oklab() and oklch(). Percentages scale each
// channel by its CSS reference: L is 0..100 (lab, lch) or 0..1 (oklab, oklch),
// 100% of a or b is 125 (lab) or 0.4 (oklab), and 100% chroma is 150 or 0.4.
func webLabFunc(name, inner string) (webRGBA, bool) {
	ch, alpha, hasAlpha := webColorArgs(inner)
	if len(ch) != 3 {
		return webRGBA{}, false
	}
	var v [3]float64
	var pct [3]bool
	for i, s := range ch {
		n, p, ok := webColorNumber(s)
		if !ok {
			return webRGBA{}, false
		}
		v[i], pct[i] = n, p
	}
	var r, g, b float64
	switch name {
	case "lab":
		l := math.Max(0, math.Min(100, webPctRef(v[0], pct[0], 100)))
		r, g, b = webLabToSRGB(l, webPctRef(v[1], pct[1], 125), webPctRef(v[2], pct[2], 125))
	case "lch":
		l := math.Max(0, math.Min(100, webPctRef(v[0], pct[0], 100)))
		r, g, b = webLabToSRGB(webPolarToRect(l, webPctRef(v[1], pct[1], 150), v[2]))
	case "oklab":
		l := math.Max(0, math.Min(1, webPctRef(v[0], pct[0], 1)))
		r, g, b = webOKLabToSRGB(l, webPctRef(v[1], pct[1], 0.4), webPctRef(v[2], pct[2], 0.4))
	default:
		l := math.Max(0, math.Min(1, webPctRef(v[0], pct[0], 1)))
		r, g, b = webOKLabToSRGB(webPolarToRect(l, webPctRef(v[1], pct[1], 0.4), v[2]))
	}
	return webWithAlpha(webRGBA{r, g, b, 1}, alpha, hasAlpha)
}

// webPctRef resolves a percentage against its channel's reference value.
func webPctRef(v float64, pct bool, ref float64) float64 {
	if pct {
		return v * ref / 100
	}
	return v
}

// webPolarToRect turns lightness, chroma and a hue in degrees into the
// rectangular axes of the same space (lch to lab, oklch to oklab).
func webPolarToRect(l, c, h float64) (float64, float64, float64) {
	c = math.Max(0, c)
	rad := h * math.Pi / 180
	return l, c * math.Cos(rad), c * math.Sin(rad)
}

// webColorFunc reads color(<space> c1 c2 c3 [/ alpha]).
func webColorFunc(inner string) (webRGBA, bool) {
	space, rest, ok := strings.Cut(strings.TrimSpace(inner), " ")
	if !ok {
		return webRGBA{}, false
	}
	ch, alpha, hasAlpha := webColorArgs(rest)
	if len(ch) != 3 {
		return webRGBA{}, false
	}
	var v [3]float64
	for i, s := range ch {
		n, pct, ok := webColorNumber(s)
		if !ok {
			return webRGBA{}, false
		}
		if pct {
			n /= 100
		}
		v[i] = n
	}
	r, g, b, ok := webSpaceToSRGB(strings.TrimSpace(space), v)
	if !ok {
		return webRGBA{}, false
	}
	return webWithAlpha(webRGBA{r, g, b, 1}, alpha, hasAlpha)
}

// webSpaceToSRGB converts the channels of a predefined colour space into
// gamma-encoded sRGB.
func webSpaceToSRGB(space string, v [3]float64) (r, g, b float64, ok bool) {
	switch space {
	case "srgb":
		return v[0], v[1], v[2], true
	case "srgb-linear":
		r, g, b = webLinearToSRGB(v[0], v[1], v[2])
		return r, g, b, true
	case "display-p3":
		x, y, z := webMul3(webLinearP3ToXYZ, webGammaDecode(v[0]), webGammaDecode(v[1]), webGammaDecode(v[2]))
		r, g, b = webLinearToSRGB(webMul3(webXYZToLinearSRGB, x, y, z))
		return r, g, b, true
	case "a98-rgb":
		lin := func(c float64) float64 { return math.Copysign(math.Pow(math.Abs(c), 563.0/256.0), c) }
		x, y, z := webMul3(webLinearA98ToXYZ, lin(v[0]), lin(v[1]), lin(v[2]))
		r, g, b = webLinearToSRGB(webMul3(webXYZToLinearSRGB, x, y, z))
		return r, g, b, true
	case "prophoto-rgb":
		lin := func(c float64) float64 {
			if math.Abs(c) <= 16.0/512.0 {
				return c / 16
			}
			return math.Copysign(math.Pow(math.Abs(c), 1.8), c)
		}
		x, y, z := webMul3(webLinearProPhotoToXYZD50, lin(v[0]), lin(v[1]), lin(v[2]))
		x, y, z = webMul3(webD50ToD65, x, y, z)
		r, g, b = webLinearToSRGB(webMul3(webXYZToLinearSRGB, x, y, z))
		return r, g, b, true
	case "rec2020":
		lin := func(c float64) float64 {
			const alpha, beta = 1.09929682680944, 0.018053968510807
			if math.Abs(c) < beta*4.5 {
				return c / 4.5
			}
			return math.Copysign(math.Pow((math.Abs(c)+alpha-1)/alpha, 1/0.45), c)
		}
		x, y, z := webMul3(webLinearRec2020ToXYZ, lin(v[0]), lin(v[1]), lin(v[2]))
		r, g, b = webLinearToSRGB(webMul3(webXYZToLinearSRGB, x, y, z))
		return r, g, b, true
	case "xyz", "xyz-d65":
		r, g, b = webLinearToSRGB(webMul3(webXYZToLinearSRGB, v[0], v[1], v[2]))
		return r, g, b, true
	case "xyz-d50":
		x, y, z := webMul3(webD50ToD65, v[0], v[1], v[2])
		r, g, b = webLinearToSRGB(webMul3(webXYZToLinearSRGB, x, y, z))
		return r, g, b, true
	}
	return 0, 0, 0, false
}

// webColorMix reads color-mix(in <space> [<hue> hue], <color> [<p>], <color>
// [<p>]). The percentages are normalised as CSS does (both missing is 50/50,
// one missing is the rest of 100%, a sum under 100% scales the alpha), and the
// channels are mixed premultiplied by alpha, so mixing with transparent fades
// a colour instead of greying it. Hues take the shorter arc unless the method
// says otherwise, and a colour with no hue (a grey) takes the other's.
func webColorMix(inner string, depth int) (webRGBA, bool) {
	parts := webSplitTopLevel(inner, ',')
	if len(parts) != 3 {
		return webRGBA{}, false
	}
	method := strings.Fields(parts[0])
	if len(method) < 2 || method[0] != "in" {
		return webRGBA{}, false
	}
	space, hueMode := method[1], "shorter"
	if len(method) >= 4 && method[3] == "hue" {
		hueMode = method[2]
	}
	c1, p1, has1, ok1 := webColorMixStop(parts[1], depth)
	c2, p2, has2, ok2 := webColorMixStop(parts[2], depth)
	if !ok1 || !ok2 {
		return webRGBA{}, false
	}
	switch {
	case !has1 && !has2:
		p1, p2 = 0.5, 0.5
	case !has1:
		p1 = 1 - p2
	case !has2:
		p2 = 1 - p1
	}
	total := p1 + p2
	if p1 < 0 || p2 < 0 || total <= 0 {
		return webRGBA{}, false
	}
	scale := math.Min(total, 1)
	p1, p2 = p1/total, p2/total
	v1, hue, none1 := webToMixSpace(space, c1)
	v2, _, none2 := webToMixSpace(space, c2)
	none1 = none1 || c1.a == 0
	none2 = none2 || c2.a == 0
	a := c1.a*p1 + c2.a*p2
	var out [3]float64
	if a > 0 {
		for i := range out {
			if i != hue {
				out[i] = (v1[i]*c1.a*p1 + v2[i]*c2.a*p2) / a
			}
		}
	}
	if hue >= 0 {
		switch {
		case none1 && none2:
			out[hue] = 0
		case none1:
			out[hue] = v2[hue]
		case none2:
			out[hue] = v1[hue]
		default:
			out[hue] = webMixHue(v1[hue], v2[hue], p2, hueMode)
		}
	}
	r, g, b := webFromMixSpace(space, out)
	return webRGBA{r, g, b, a * scale}, true
}

// webColorMixStop reads one color-mix() operand: a colour and an optional
// percentage on either side of it.
func webColorMixStop(part string, depth int) (c webRGBA, p float64, hasP, ok bool) {
	var color []string
	for _, field := range webCSSSpaceFields(part) {
		if strings.HasSuffix(field, "%") && !hasP {
			if n, pct, valid := webColorNumber(field); valid && pct {
				p, hasP = n/100, true
				continue
			}
		}
		color = append(color, field)
	}
	if len(color) == 0 {
		return webRGBA{}, 0, false, false
	}
	c, ok = webCSSColor(strings.Join(color, " "), depth+1)
	return c, p, hasP, ok
}

// webMixHue interpolates two hues (degrees) by t along the arc the
// interpolation method names.
func webMixHue(h1, h2, t float64, mode string) float64 {
	h1, h2 = webHueNorm(h1), webHueNorm(h2)
	d := h2 - h1
	switch mode {
	case "longer":
		if d > 0 && d < 180 {
			d -= 360
		} else if d > -180 && d <= 0 {
			d += 360
		}
	case "increasing":
		if d < 0 {
			d += 360
		}
	case "decreasing":
		if d > 0 {
			d -= 360
		}
	default:
		if d > 180 {
			d -= 360
		} else if d < -180 {
			d += 360
		}
	}
	return webHueNorm(h1 + d*t)
}

// webHueNorm brings a hue into 0..360.
func webHueNorm(h float64) float64 {
	h = math.Mod(h, 360)
	if h < 0 {
		h += 360
	}
	return h
}

// webToMixSpace expresses an sRGB colour in a color-mix() space. hue is the
// index of the hue channel in a polar space (-1 otherwise), and none reports a
// colour whose hue is powerless there (a grey). An unknown space mixes in
// oklab, the CSS default.
func webToMixSpace(space string, c webRGBA) (v [3]float64, hue int, none bool) {
	switch space {
	case "srgb":
		return [3]float64{c.r, c.g, c.b}, -1, false
	case "srgb-linear":
		return [3]float64{webGammaDecode(c.r), webGammaDecode(c.g), webGammaDecode(c.b)}, -1, false
	case "xyz", "xyz-d65", "xyz-d50":
		x, y, z := webMul3(webLinearSRGBToXYZ, webGammaDecode(c.r), webGammaDecode(c.g), webGammaDecode(c.b))
		if space == "xyz-d50" {
			x, y, z = webMul3(webD65ToD50, x, y, z)
		}
		return [3]float64{x, y, z}, -1, false
	case "lab":
		l, a, b := webSRGBToLab(c.r, c.g, c.b)
		return [3]float64{l, a, b}, -1, false
	case "lch":
		l, a, b := webSRGBToLab(c.r, c.g, c.b)
		chroma := math.Hypot(a, b)
		return [3]float64{l, chroma, webHueNorm(math.Atan2(b, a) * 180 / math.Pi)}, 2, chroma < 0.02
	case "oklch":
		l, a, b := webSRGBToOKLab(c.r, c.g, c.b)
		chroma := math.Hypot(a, b)
		return [3]float64{l, chroma, webHueNorm(math.Atan2(b, a) * 180 / math.Pi)}, 2, chroma < 0.0002
	case "hsl":
		h, s, l, ok := webRGBToHSL(c.r, c.g, c.b)
		return [3]float64{h, s, l}, 0, !ok
	case "hwb":
		h, _, _, ok := webRGBToHSL(c.r, c.g, c.b)
		w := math.Min(c.r, math.Min(c.g, c.b))
		bl := 1 - math.Max(c.r, math.Max(c.g, c.b))
		return [3]float64{h, w, bl}, 0, !ok
	}
	l, a, b := webSRGBToOKLab(c.r, c.g, c.b)
	return [3]float64{l, a, b}, -1, false
}

// webFromMixSpace is webToMixSpace backwards.
func webFromMixSpace(space string, v [3]float64) (r, g, b float64) {
	switch space {
	case "srgb":
		return v[0], v[1], v[2]
	case "srgb-linear":
		return webLinearToSRGB(v[0], v[1], v[2])
	case "xyz", "xyz-d65":
		return webLinearToSRGB(webMul3(webXYZToLinearSRGB, v[0], v[1], v[2]))
	case "xyz-d50":
		x, y, z := webMul3(webD50ToD65, v[0], v[1], v[2])
		return webLinearToSRGB(webMul3(webXYZToLinearSRGB, x, y, z))
	case "lab":
		return webLabToSRGB(v[0], v[1], v[2])
	case "lch":
		return webLabToSRGB(webPolarToRect(v[0], v[1], v[2]))
	case "oklch":
		return webOKLabToSRGB(webPolarToRect(v[0], v[1], v[2]))
	case "hsl":
		return webHSLToRGB(v[0], v[1], v[2])
	case "hwb":
		return webHWBToRGB(v[0], v[1], v[2])
	}
	return webOKLabToSRGB(v[0], v[1], v[2])
}

// webRelativeColor reads the relative syntax, `<fn>(from <origin> c1 c2 c3
// [/ alpha])`, for rgb, hsl, hwb, lab, lch, oklab and oklch: each channel is
// one of the origin's channel keywords, none, or a number. Anything else (a
// calc() over channels) keeps the origin's own channel, the closest reading
// the reader can give.
func webRelativeColor(name, rest string, depth int) (webRGBA, bool) {
	fields := webCSSSpaceFields(rest)
	if len(fields) == 0 {
		return webRGBA{}, false
	}
	origin, ok := webCSSColor(fields[0], depth+1)
	if !ok {
		return webRGBA{}, false
	}
	// The channels are whole tokens (a calc() keeps its spaces); whatever
	// follows a slash is the alpha.
	var ch []string
	alpha, hasAlpha := "", false
	for _, field := range fields[1:] {
		for k, piece := range webSplitTopLevel(field, '/') {
			if k > 0 {
				hasAlpha = true
			}
			switch {
			case piece == "":
			case hasAlpha:
				alpha = piece
			default:
				ch = append(ch, piece)
			}
		}
	}
	if len(ch) != 3 {
		return webRGBA{}, false
	}
	name = strings.TrimSuffix(name, "a")
	var keys [3]string
	var vals [3]float64
	switch name {
	case "rgb":
		keys = [3]string{"r", "g", "b"}
		vals = [3]float64{origin.r * 255, origin.g * 255, origin.b * 255}
	case "hsl":
		h, s, l, _ := webRGBToHSL(origin.r, origin.g, origin.b)
		keys = [3]string{"h", "s", "l"}
		vals = [3]float64{h, s * 100, l * 100}
	case "hwb":
		h, _, _, _ := webRGBToHSL(origin.r, origin.g, origin.b)
		keys = [3]string{"h", "w", "b"}
		vals = [3]float64{h, math.Min(origin.r, math.Min(origin.g, origin.b)) * 100, (1 - math.Max(origin.r, math.Max(origin.g, origin.b))) * 100}
	case "lab", "oklab":
		v, _, _ := webToMixSpace(name, origin)
		keys = [3]string{"l", "a", "b"}
		vals = v
	case "lch", "oklch":
		v, _, _ := webToMixSpace(name, origin)
		keys = [3]string{"l", "c", "h"}
		vals = v
	default:
		return webRGBA{}, false
	}
	parts := make([]string, 3)
	for i, s := range ch {
		parts[i] = strconv.FormatFloat(vals[i], 'f', -1, 64)
		if k := webKeyIndex(keys, s); k >= 0 {
			parts[i] = strconv.FormatFloat(vals[k], 'f', -1, 64)
		} else if _, _, valid := webColorNumber(s); valid {
			parts[i] = s
		}
	}
	a := origin.a
	if hasAlpha && alpha != "alpha" {
		if v, valid := webColorAlpha(alpha); valid {
			a = v
		}
	}
	text := name + "(" + strings.Join(parts, " ") + " / " + strconv.FormatFloat(a, 'f', -1, 64) + ")"
	return webCSSColor(text, depth+1)
}

// webKeyIndex is the index of s among a colour function's channel keywords,
// or -1.
func webKeyIndex(keys [3]string, s string) int {
	for i, k := range keys {
		if k == s {
			return i
		}
	}
	return -1
}

// --- Backdrops ---------------------------------------------------------------

// webStyleImageBackdrop reports an element whose own background is a picture
// that fills its box: a photo behind a hero, a tiled texture. A small
// non-repeating image (an icon in a corner, an arrow beside a link) is not
// the backdrop its text is read on.
func webStyleImageBackdrop(st *webStyle) bool {
	if st.BackgroundLayerCount == 0 {
		return st.BackgroundImage != "" && webBackgroundLayerCovers(st.BackgroundSize, st.BackgroundRepeat)
	}
	for i := 0; i < st.BackgroundLayerCount && i < webMaxBackgroundLayers; i++ {
		if layer := &st.BackgroundLayers[i]; layer.Image != "" && webBackgroundLayerCovers(layer.Size, layer.Repeat) {
			return true
		}
	}
	return false
}

// webBackgroundLayerCovers reports a background layer sized to cover or
// contain its box, or one that tiles it.
func webBackgroundLayerCovers(size, repeat string) bool {
	size = strings.ToLower(size)
	if strings.Contains(size, "cover") || strings.Contains(size, "contain") || strings.Contains(size, "100%") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(repeat)) {
	case "", "repeat", "repeat repeat", "round", "space":
		return true
	}
	return false
}

// anchorSawImage reports whether the containing block of a positioned box
// about to open (its nearest positioned ancestor) already holds a picture:
// the box is an overlay on the photo — a caption, a badge, a play button.
func (p *webParser) anchorSawImage() bool {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if webStyleIsAnchor(&p.stack[i].style) {
			return p.stack[i].sawImage
		}
	}
	return false
}

// markImageInAnchors notes a picture in every positioned element it is
// inside (anchorSawImage).
func (p *webParser) markImageInAnchors() {
	for i := range p.stack {
		if webStyleIsAnchor(&p.stack[i].style) {
			p.stack[i].sawImage = true
		}
	}
}

// --- Colour spaces -----------------------------------------------------------

// The D50 white point of lab() and lch(), as CSS Color 4 defines it.
const (
	webD50X = 0.3457 / 0.3585
	webD50Z = (1 - 0.3457 - 0.3585) / 0.3585
)

var webD50ToD65 = [3][3]float64{
	{0.955473421488075, -0.02309845494876471, 0.06325924320057072},
	{-0.0283697093338637, 1.0099953980813041, 0.021041441191917323},
	{0.012314014864481998, -0.020507649298898964, 1.330365926242124},
}

var webD65ToD50 = [3][3]float64{
	{1.0479298208405488, 0.022946793341019088, -0.05019222954313557},
	{0.029627815688159344, 0.990434484573249, -0.01707382502938514},
	{-0.009243058152591178, 0.015055144896577895, 0.7518742899580008},
}

var webXYZToLinearSRGB = [3][3]float64{
	{3.2409699419045226, -1.537383177570094, -0.4986107602930034},
	{-0.9692436362808796, 1.8759675015077202, 0.04155505740717559},
	{0.05563007969699366, -0.20397695888897652, 1.0569715142428786},
}

var webLinearSRGBToXYZ = [3][3]float64{
	{0.41239079926595934, 0.357584339383878, 0.1804807884018343},
	{0.21263900587151027, 0.715168678767756, 0.07219231536073371},
	{0.01933081871559182, 0.11919477979462598, 0.9505321522496607},
}

var webLinearP3ToXYZ = [3][3]float64{
	{0.4865709486482162, 0.26566769316909306, 0.1982172852343625},
	{0.2289745640697488, 0.6917385218365064, 0.079286914093745},
	{0, 0.04511338185890264, 1.043944368900976},
}

var webLinearA98ToXYZ = [3][3]float64{
	{0.5766690429101305, 0.1855582379065463, 0.1882286462349947},
	{0.29734497525053605, 0.6273635662554661, 0.07529145849399788},
	{0.02703136138641234, 0.07068885253582723, 0.9913375368376388},
}

var webLinearProPhotoToXYZD50 = [3][3]float64{
	{0.7977604896723027, 0.13518583717574031, 0.0313493495815248},
	{0.2880711282292934, 0.7118432178101014, 0.00008565396060525902},
	{0, 0, 0.8251046025104601},
}

var webLinearRec2020ToXYZ = [3][3]float64{
	{0.6369580483012914, 0.14461690358620832, 0.1688809751641721},
	{0.2627002120112671, 0.6779980715188708, 0.05930171646986196},
	{0, 0.028072693049087428, 1.060985057710791},
}

// webMul3 multiplies a 3×3 matrix by a column vector.
func webMul3(m [3][3]float64, x, y, z float64) (float64, float64, float64) {
	return m[0][0]*x + m[0][1]*y + m[0][2]*z,
		m[1][0]*x + m[1][1]*y + m[1][2]*z,
		m[2][0]*x + m[2][1]*y + m[2][2]*z
}

// webGammaEncode applies the sRGB transfer curve to a linear channel.
func webGammaEncode(v float64) float64 {
	sign := 1.0
	if v < 0 {
		sign, v = -1, -v
	}
	if v <= 0.0031308 {
		return sign * 12.92 * v
	}
	return sign * (1.055*math.Pow(v, 1/2.4) - 0.055)
}

// webGammaDecode undoes webGammaEncode.
func webGammaDecode(v float64) float64 {
	sign := 1.0
	if v < 0 {
		sign, v = -1, -v
	}
	if v <= 0.04045 {
		return sign * v / 12.92
	}
	return sign * math.Pow((v+0.055)/1.055, 2.4)
}

// webLinearToSRGB gamma-encodes three linear channels.
func webLinearToSRGB(r, g, b float64) (float64, float64, float64) {
	return webGammaEncode(r), webGammaEncode(g), webGammaEncode(b)
}

// webLabToSRGB converts CIE Lab (D50) to sRGB.
func webLabToSRGB(l, a, b float64) (float64, float64, float64) {
	const kappa = 24389.0 / 27.0
	const epsilon = 216.0 / 24389.0
	fy := (l + 16) / 116
	fx := a/500 + fy
	fz := fy - b/200
	xr := fx * fx * fx
	if xr <= epsilon {
		xr = (116*fx - 16) / kappa
	}
	yr := fy * fy * fy
	if l <= kappa*epsilon {
		yr = l / kappa
	}
	zr := fz * fz * fz
	if zr <= epsilon {
		zr = (116*fz - 16) / kappa
	}
	x, y, z := webMul3(webD50ToD65, xr*webD50X, yr, zr*webD50Z)
	return webLinearToSRGB(webMul3(webXYZToLinearSRGB, x, y, z))
}

// webSRGBToLab converts sRGB to CIE Lab (D50).
func webSRGBToLab(r, g, b float64) (float64, float64, float64) {
	x, y, z := webMul3(webLinearSRGBToXYZ, webGammaDecode(r), webGammaDecode(g), webGammaDecode(b))
	x, y, z = webMul3(webD65ToD50, x, y, z)
	f := func(t float64) float64 {
		const kappa = 24389.0 / 27.0
		const epsilon = 216.0 / 24389.0
		if t > epsilon {
			return math.Cbrt(t)
		}
		return (kappa*t + 16) / 116
	}
	fx, fy, fz := f(x/webD50X), f(y), f(z/webD50Z)
	return 116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)
}

// webOKLabToSRGB converts OKLab to sRGB.
func webOKLabToSRGB(l, a, b float64) (float64, float64, float64) {
	lc := l + 0.3963377774*a + 0.2158037573*b
	mc := l - 0.1055613458*a - 0.0638541728*b
	sc := l - 0.0894841775*a - 1.2914855480*b
	lc, mc, sc = lc*lc*lc, mc*mc*mc, sc*sc*sc
	return webLinearToSRGB(
		4.0767416621*lc-3.3077115913*mc+0.2309699292*sc,
		-1.2684380046*lc+2.6097574011*mc-0.3413193965*sc,
		-0.0041960863*lc-0.7034186147*mc+1.7076147010*sc,
	)
}

// webSRGBToOKLab converts sRGB to OKLab.
func webSRGBToOKLab(r, g, b float64) (float64, float64, float64) {
	r, g, b = webGammaDecode(r), webGammaDecode(g), webGammaDecode(b)
	lc := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	mc := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	sc := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	return 0.2104542553*lc + 0.7936177850*mc - 0.0040720468*sc,
		1.9779984951*lc - 2.4285922050*mc + 0.4505937099*sc,
		0.0259040371*lc + 0.7827717662*mc - 0.8086757660*sc
}

// webHSLToRGB converts a hue in degrees and saturation and lightness in 0..1
// to sRGB.
func webHSLToRGB(h, s, l float64) (float64, float64, float64) {
	h = webHueNorm(h)
	f := func(n float64) float64 {
		k := math.Mod(n+h/30, 12)
		a := s * math.Min(l, 1-l)
		return l - a*math.Max(-1, math.Min(math.Min(k-3, 9-k), 1))
	}
	return f(0), f(8), f(4)
}

// webHWBToRGB converts a hue in degrees and whiteness and blackness in 0..1 to
// sRGB.
func webHWBToRGB(h, w, bl float64) (float64, float64, float64) {
	if w+bl >= 1 {
		gray := w / (w + bl)
		return gray, gray, gray
	}
	r, g, b := webHSLToRGB(h, 1, 0.5)
	k := 1 - w - bl
	return r*k + w, g*k + w, b*k + w
}

// webRGBToHSL converts sRGB to a hue in degrees and saturation and lightness
// in 0..1; ok is false for a grey, whose hue is powerless.
func webRGBToHSL(r, g, b float64) (h, s, l float64, ok bool) {
	hi := math.Max(r, math.Max(g, b))
	lo := math.Min(r, math.Min(g, b))
	l = (hi + lo) / 2
	d := hi - lo
	if d <= 1e-9 {
		return 0, 0, l, false
	}
	if l > 0 && l < 1 {
		s = (hi - l) / math.Min(l, 1-l)
	}
	switch hi {
	case r:
		h = math.Mod((g-b)/d+6, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	return h * 60, s, l, true
}

// --- Keywords -----------------------------------------------------------------

// webCSSNamedColors is the CSS named-colour table, exact sRGB.
var webCSSNamedColors = map[string]uint32{
	"aliceblue":            0xf0f8ff,
	"antiquewhite":         0xfaebd7,
	"aqua":                 0x00ffff,
	"aquamarine":           0x7fffd4,
	"azure":                0xf0ffff,
	"beige":                0xf5f5dc,
	"bisque":               0xffe4c4,
	"black":                0x000000,
	"blanchedalmond":       0xffebcd,
	"blue":                 0x0000ff,
	"blueviolet":           0x8a2be2,
	"brown":                0xa52a2a,
	"burlywood":            0xdeb887,
	"cadetblue":            0x5f9ea0,
	"chartreuse":           0x7fff00,
	"chocolate":            0xd2691e,
	"coral":                0xff7f50,
	"cornflowerblue":       0x6495ed,
	"cornsilk":             0xfff8dc,
	"crimson":              0xdc143c,
	"cyan":                 0x00ffff,
	"darkblue":             0x00008b,
	"darkcyan":             0x008b8b,
	"darkgoldenrod":        0xb8860b,
	"darkgray":             0xa9a9a9,
	"darkgreen":            0x006400,
	"darkgrey":             0xa9a9a9,
	"darkkhaki":            0xbdb76b,
	"darkmagenta":          0x8b008b,
	"darkolivegreen":       0x556b2f,
	"darkorange":           0xff8c00,
	"darkorchid":           0x9932cc,
	"darkred":              0x8b0000,
	"darksalmon":           0xe9967a,
	"darkseagreen":         0x8fbc8f,
	"darkslateblue":        0x483d8b,
	"darkslategray":        0x2f4f4f,
	"darkslategrey":        0x2f4f4f,
	"darkturquoise":        0x00ced1,
	"darkviolet":           0x9400d3,
	"deeppink":             0xff1493,
	"deepskyblue":          0x00bfff,
	"dimgray":              0x696969,
	"dimgrey":              0x696969,
	"dodgerblue":           0x1e90ff,
	"firebrick":            0xb22222,
	"floralwhite":          0xfffaf0,
	"forestgreen":          0x228b22,
	"fuchsia":              0xff00ff,
	"gainsboro":            0xdcdcdc,
	"ghostwhite":           0xf8f8ff,
	"gold":                 0xffd700,
	"goldenrod":            0xdaa520,
	"gray":                 0x808080,
	"green":                0x008000,
	"greenyellow":          0xadff2f,
	"grey":                 0x808080,
	"honeydew":             0xf0fff0,
	"hotpink":              0xff69b4,
	"indianred":            0xcd5c5c,
	"indigo":               0x4b0082,
	"ivory":                0xfffff0,
	"khaki":                0xf0e68c,
	"lavender":             0xe6e6fa,
	"lavenderblush":        0xfff0f5,
	"lawngreen":            0x7cfc00,
	"lemonchiffon":         0xfffacd,
	"lightblue":            0xadd8e6,
	"lightcoral":           0xf08080,
	"lightcyan":            0xe0ffff,
	"lightgoldenrodyellow": 0xfafad2,
	"lightgray":            0xd3d3d3,
	"lightgreen":           0x90ee90,
	"lightgrey":            0xd3d3d3,
	"lightpink":            0xffb6c1,
	"lightsalmon":          0xffa07a,
	"lightseagreen":        0x20b2aa,
	"lightskyblue":         0x87cefa,
	"lightslategray":       0x778899,
	"lightslategrey":       0x778899,
	"lightsteelblue":       0xb0c4de,
	"lightyellow":          0xffffe0,
	"lime":                 0x00ff00,
	"limegreen":            0x32cd32,
	"linen":                0xfaf0e6,
	"magenta":              0xff00ff,
	"maroon":               0x800000,
	"mediumaquamarine":     0x66cdaa,
	"mediumblue":           0x0000cd,
	"mediumorchid":         0xba55d3,
	"mediumpurple":         0x9370db,
	"mediumseagreen":       0x3cb371,
	"mediumslateblue":      0x7b68ee,
	"mediumspringgreen":    0x00fa9a,
	"mediumturquoise":      0x48d1cc,
	"mediumvioletred":      0xc71585,
	"midnightblue":         0x191970,
	"mintcream":            0xf5fffa,
	"mistyrose":            0xffe4e1,
	"moccasin":             0xffe4b5,
	"navajowhite":          0xffdead,
	"navy":                 0x000080,
	"oldlace":              0xfdf5e6,
	"olive":                0x808000,
	"olivedrab":            0x6b8e23,
	"orange":               0xffa500,
	"orangered":            0xff4500,
	"orchid":               0xda70d6,
	"palegoldenrod":        0xeee8aa,
	"palegreen":            0x98fb98,
	"paleturquoise":        0xafeeee,
	"palevioletred":        0xdb7093,
	"papayawhip":           0xffefd5,
	"peachpuff":            0xffdab9,
	"peru":                 0xcd853f,
	"pink":                 0xffc0cb,
	"plum":                 0xdda0dd,
	"powderblue":           0xb0e0e6,
	"purple":               0x800080,
	"rebeccapurple":        0x663399,
	"red":                  0xff0000,
	"rosybrown":            0xbc8f8f,
	"royalblue":            0x4169e1,
	"saddlebrown":          0x8b4513,
	"salmon":               0xfa8072,
	"sandybrown":           0xf4a460,
	"seagreen":             0x2e8b57,
	"seashell":             0xfff5ee,
	"sienna":               0xa0522d,
	"silver":               0xc0c0c0,
	"skyblue":              0x87ceeb,
	"slateblue":            0x6a5acd,
	"slategray":            0x708090,
	"slategrey":            0x708090,
	"snow":                 0xfffafa,
	"springgreen":          0x00ff7f,
	"steelblue":            0x4682b4,
	"tan":                  0xd2b48c,
	"teal":                 0x008080,
	"thistle":              0xd8bfd8,
	"tomato":               0xff6347,
	"turquoise":            0x40e0d0,
	"violet":               0xee82ee,
	"wheat":                0xf5deb3,
	"white":                0xffffff,
	"whitesmoke":           0xf5f5f5,
	"yellow":               0xffff00,
	"yellowgreen":          0x9acd32,
}

// webCSSSystemColors are the CSS system colours as a light-mode Chrome
// resolves them, with the deprecated names mapped as CSS Color 4 maps them.
var webCSSSystemColors = map[string]uint32{
	"canvas":              0xffffff,
	"canvastext":          0x000000,
	"linktext":            0x0000ee,
	"visitedtext":         0x551a8b,
	"activetext":          0xff0000,
	"buttonface":          0xefefef,
	"buttontext":          0x000000,
	"buttonborder":        0x767676,
	"field":               0xffffff,
	"fieldtext":           0x000000,
	"highlight":           0x0078d7,
	"highlighttext":       0xffffff,
	"selecteditem":        0x0078d7,
	"selecteditemtext":    0xffffff,
	"mark":                0xffff00,
	"marktext":            0x000000,
	"graytext":            0x6d6d6d,
	"accentcolor":         0x0075ff,
	"accentcolortext":     0xffffff,
	"-webkit-link":        0x0000ee,
	"-webkit-activelink":  0xff0000,
	"activeborder":        0x767676,
	"activecaption":       0xffffff,
	"appworkspace":        0xffffff,
	"background":          0xffffff,
	"buttonhighlight":     0xefefef,
	"buttonshadow":        0xefefef,
	"captiontext":         0x000000,
	"inactiveborder":      0x767676,
	"inactivecaption":     0xffffff,
	"inactivecaptiontext": 0x6d6d6d,
	"infobackground":      0xffffff,
	"infotext":            0x000000,
	"menu":                0xffffff,
	"menutext":            0x000000,
	"scrollbar":           0xffffff,
	"threeddarkshadow":    0x767676,
	"threedface":          0xefefef,
	"threedhighlight":     0x767676,
	"threedlightshadow":   0x767676,
	"threedshadow":        0x767676,
	"window":              0xffffff,
	"windowframe":         0x767676,
	"windowtext":          0x000000,
}
