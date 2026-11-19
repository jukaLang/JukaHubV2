package main

// game_library_pixel.go — the library's pixel lettering: a 5×7 bitmap face
// that spells a console's name in squares.
//
// A name is laid out once per name and box (consolePixelLayoutFor) and drawn
// as one batch of squares per colour, from slices reused frame to frame, so a
// grid of lettered consoles costs a few FillRects calls a frame and allocates
// nothing. The Covers look uses it for a console button with no cover yet;
// the Pixel names and Pixel cards looks (game_library_pixel_look.go) make it
// the console's art.
//
// The focused name flies together square by square when the player lands on
// it: every lit square starts at its own scattered place across the button
// and a little past it, sets off a moment after its neighbour to the left
// (with a little jitter), and eases into its letter with a small overshoot,
// in consolePixelTotalMS. The scatter is a hash of the name and the square's
// index, so a console's letters fly in the same way every time. Reduce motion
// (and Battery Saver, which implies it) puts them straight in place.
//
// JukaHub's own logo is the glass tile with an Inter "J" (branding/), not
// pixel lettering, so the face is this file's own.

import (
	"math"
	"strings"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
)

// consolePixelGlyphs are the face's letters: seven rows of five bits, the
// high bit on the left. Names are upper-cased and folded to ASCII first
// (consoleLogoText), so only capitals, digits and the marks console names use
// are here; anything else draws as '?'.
var consolePixelGlyphs = map[rune][7]byte{
	'A': {14, 17, 17, 31, 17, 17, 17}, 'B': {30, 17, 17, 30, 17, 17, 30},
	'C': {14, 17, 16, 16, 16, 17, 14}, 'D': {30, 17, 17, 17, 17, 17, 30},
	'E': {31, 16, 16, 30, 16, 16, 31}, 'F': {31, 16, 16, 30, 16, 16, 16},
	'G': {14, 17, 16, 23, 17, 17, 14}, 'H': {17, 17, 17, 31, 17, 17, 17},
	'I': {31, 4, 4, 4, 4, 4, 31}, 'J': {7, 2, 2, 2, 18, 18, 12},
	'K': {17, 18, 20, 24, 20, 18, 17}, 'L': {16, 16, 16, 16, 16, 16, 31},
	'M': {17, 27, 21, 21, 17, 17, 17}, 'N': {17, 25, 21, 19, 17, 17, 17},
	'O': {14, 17, 17, 17, 17, 17, 14}, 'P': {30, 17, 17, 30, 16, 16, 16},
	'Q': {14, 17, 17, 17, 21, 18, 13}, 'R': {30, 17, 17, 30, 20, 18, 17},
	'S': {15, 16, 16, 14, 1, 1, 30}, 'T': {31, 4, 4, 4, 4, 4, 4},
	'U': {17, 17, 17, 17, 17, 17, 14}, 'V': {17, 17, 17, 17, 17, 10, 4},
	'W': {17, 17, 17, 21, 21, 27, 17}, 'X': {17, 17, 10, 4, 10, 17, 17},
	'Y': {17, 17, 10, 4, 4, 4, 4}, 'Z': {31, 1, 2, 4, 8, 16, 31},
	'0': {14, 17, 19, 21, 25, 17, 14}, '1': {4, 12, 4, 4, 4, 4, 14},
	'2': {14, 17, 1, 2, 4, 8, 31}, '3': {30, 1, 1, 14, 1, 1, 30},
	'4': {2, 6, 10, 18, 31, 2, 2}, '5': {31, 16, 16, 30, 1, 1, 30},
	'6': {14, 16, 16, 30, 17, 17, 14}, '7': {31, 1, 2, 4, 8, 8, 8},
	'8': {14, 17, 17, 14, 17, 17, 14}, '9': {14, 17, 17, 15, 1, 1, 14},
	'-': {0, 0, 0, 31, 0, 0, 0}, '+': {0, 4, 4, 31, 4, 4, 0},
	'.': {0, 0, 0, 0, 0, 12, 12}, '/': {1, 1, 2, 4, 8, 16, 16},
	' ': {}, '?': {14, 17, 1, 2, 4, 0, 4},
	'&': {12, 18, 20, 8, 21, 18, 13}, '\'': {12, 4, 8, 0, 0, 0, 0},
	'!': {4, 4, 4, 4, 4, 0, 4}, ':': {0, 12, 12, 0, 12, 12, 0},
	',': {0, 0, 0, 0, 12, 4, 8}, ';': {0, 12, 12, 0, 12, 4, 8},
	'(': {2, 4, 8, 8, 8, 4, 2}, ')': {8, 4, 2, 2, 2, 4, 8},
	'[': {14, 8, 8, 8, 8, 8, 14}, ']': {14, 2, 2, 2, 2, 2, 14},
	'"': {10, 10, 0, 0, 0, 0, 0}, '_': {0, 0, 0, 0, 0, 0, 31},
	'#': {10, 10, 31, 10, 31, 10, 10}, '*': {0, 4, 21, 14, 21, 4, 0},
	'=': {0, 0, 31, 0, 31, 0, 0}, '%': {24, 25, 2, 4, 8, 19, 3},
	'@': {14, 17, 23, 21, 23, 16, 15}, '$': {4, 15, 20, 14, 5, 30, 4},
	'<': {2, 4, 8, 16, 8, 4, 2}, '>': {8, 4, 2, 1, 2, 4, 8},
	'|': {4, 4, 4, 4, 4, 4, 4}, '~': {0, 0, 8, 21, 2, 0, 0},
}

// consolePixelMarks maps the typographic marks a name may carry to the ASCII
// ones the face has; "" drops the mark.
var consolePixelMarks = map[rune]string{
	'‘': "'", '’': "'", '`': "'", '´': "'", '“': "\"", '”': "\"",
	'‐': "-", '–': "-", '—': "-", '−': "-", '·': "-", '•': "-",
	'…': "...", '×': "X", '\u00a0': " ", '™': "", '®': "", '©': "",
}

// consoleLogoText is a name as the face spells it: trimmed, upper case,
// accented letters folded to plain ones (the fold nearby.go compares titles
// with: "Pokémon" is POKEMON, "ß" is SS) and typographic marks to ASCII.
func consoleLogoText(name string) string {
	name = strings.TrimSpace(name)
	plain := true
	for i := 0; i < len(name); i++ {
		// '`' is ASCII but has no letter of its own: consolePixelMarks has it.
		if name[i] >= 0x80 || name[i] == '`' {
			plain = false
			break
		}
	}
	if plain {
		return strings.ToUpper(name)
	}
	var b strings.Builder
	for _, r := range name {
		if s, ok := nearbyFold[unicode.ToLower(r)]; ok {
			b.WriteString(strings.ToUpper(s))
			continue
		}
		if s, ok := consolePixelMarks[r]; ok {
			b.WriteString(s)
			continue
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return strings.TrimSpace(b.String())
}

// consoleLogoFit picks the largest scale, from maxScale down to 1, at which
// the name word-wraps into maxW×maxH, and returns it with its lines joined by
// "\n". When even scale 1 cannot hold a word, it is one line at scale 1 cut
// with a dot.
func consoleLogoFit(name string, maxW, maxH, maxScale int32) (string, int32) {
	name = consoleLogoText(name)
	words := strings.Fields(name)
	if len(words) == 0 || maxW <= 0 {
		return "", 1
	}
	for scale := max32(maxScale, 1); scale >= 1; scale-- {
		// n letters are n*6*scale-scale wide: the last one has no gap after it.
		perLine := int((maxW + scale) / (6 * scale))
		lines := consoleLogoWrap(words, perLine)
		if lines != nil && int32(len(lines))*8*scale-scale <= maxH {
			return strings.Join(lines, "\n"), scale
		}
	}
	letters := []rune(strings.Join(words, " "))
	maxLetters := int((maxW + 1) / 6)
	if maxLetters < 1 {
		return "", 1
	}
	if len(letters) > maxLetters {
		if maxLetters > 1 {
			letters = append(letters[:maxLetters-1], '.')
		} else {
			letters = letters[:1]
		}
	}
	return string(letters), 1
}

// consoleLogoWrap fills lines of at most perLine letters word by word. It
// returns nil when a single word is longer than a line.
func consoleLogoWrap(words []string, perLine int) []string {
	if perLine < 1 {
		return nil
	}
	var lines []string
	cur := ""
	for _, word := range words {
		n := len([]rune(word))
		switch {
		case n > perLine:
			return nil
		case cur == "":
			cur = word
		case len([]rune(cur))+1+n <= perLine:
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
	}
	return append(lines, cur)
}

// The flight's timing: each square flies for consolePixelFlightMS, setting off
// up to consolePixelSweepMS later the further right it rests, plus up to
// consolePixelJitterMS of its own. The last one lands at consolePixelTotalMS.
const (
	consolePixelFlightMS = 300
	consolePixelSweepMS  = 140
	consolePixelJitterMS = 100
	consolePixelTotalMS  = consolePixelFlightMS + consolePixelSweepMS + consolePixelJitterMS
	// consolePixelOvershoot is the ease's back-off: about 3% past the letter
	// before it settles.
	consolePixelOvershoot = 0.9
	// consolePixelLayoutCap bounds the layout cache; a grid shows at most a
	// few dozen names, and a resize lays them all out again.
	consolePixelLayoutCap = 128
)

// consolePixelCell is one lit square of a laid-out name: where it rests and
// where its flight starts, both from the button's top-left corner, and how
// long after the landing it sets off.
type consolePixelCell struct {
	x, y   int32
	sx, sy int32
	delay  uint32
}

// consolePixelLayout is a name laid out in a box: its squares and their side.
type consolePixelLayout struct {
	cells []consolePixelCell
	size  int32
}

// consolePixelKey is what a layout depends on: the name, the box it rests in
// (from the button's corner), the button's size (the scatter spans it) and
// the largest square allowed.
type consolePixelKey struct {
	name     string
	box      sdl.Rect
	w, h     int32
	maxScale int32
}

// consolePixelLayouts caches layouts. Render thread only.
var consolePixelLayouts = map[consolePixelKey]*consolePixelLayout{}

// consolePixelLayoutFor returns the name's layout in box, from the cache when
// it has been laid out before.
func consolePixelLayoutFor(name string, box sdl.Rect, w, h, maxScale int32) *consolePixelLayout {
	key := consolePixelKey{name: name, box: box, w: w, h: h, maxScale: maxScale}
	if l, ok := consolePixelLayouts[key]; ok {
		return l
	}
	if len(consolePixelLayouts) >= consolePixelLayoutCap {
		consolePixelLayouts = map[consolePixelKey]*consolePixelLayout{}
	}
	l := buildConsolePixelLayout(name, box, w, h, maxScale)
	consolePixelLayouts[key] = l
	return l
}

// consolePixelHash mixes a 32-bit value (the "lowbias32" finaliser): the
// scatter and the jitter come from it, so they are the same on every visit.
func consolePixelHash(x uint32) uint32 {
	x ^= x >> 16
	x *= 0x7feb352d
	x ^= x >> 15
	x *= 0x846ca68b
	x ^= x >> 16
	return x
}

// buildConsolePixelLayout centres the name's lines in box at the largest
// square up to maxScale that fits, and gives every square a flight: a start
// anywhere across the w×h button and up to an eighth past its sides and a
// sixth past its top and bottom, and a delay that sweeps left to right.
func buildConsolePixelLayout(name string, box sdl.Rect, w, h, maxScale int32) *consolePixelLayout {
	l := &consolePixelLayout{size: 1}
	if box.W <= 0 || box.H <= 0 {
		return l
	}
	text, scale := consoleLogoFit(name, box.W, box.H, maxScale)
	if text == "" {
		return l
	}
	l.size = scale
	seed := uint32(2166136261)
	for i := 0; i < len(text); i++ {
		seed = (seed ^ uint32(text[i])) * 16777619
	}
	mx, my := w/8, h/6
	spanW, spanH := w+2*mx-scale, h+2*my-scale
	if spanW < 1 {
		spanW = 1
	}
	if spanH < 1 {
		spanH = 1
	}
	lines := strings.Split(text, "\n")
	y := box.Y + (box.H-(int32(len(lines))*8*scale-scale))/2
	n := uint32(0)
	for _, line := range lines {
		runes := []rune(line)
		x := box.X + (box.W-(int32(len(runes))*6*scale-scale))/2
		for _, r := range runes {
			glyph, ok := consolePixelGlyphs[r]
			if !ok {
				glyph = consolePixelGlyphs['?']
			}
			for row, bits := range glyph {
				for col := 0; col < 5; col++ {
					if bits&(1<<(4-col)) == 0 {
						continue
					}
					cx, cy := x+int32(col)*scale, y+int32(row)*scale
					h1 := consolePixelHash(seed ^ (n * 0x9e3779b9))
					h2 := consolePixelHash(h1)
					h3 := consolePixelHash(h2)
					n++
					sweep := int32(0)
					if box.W > 0 {
						sweep = (cx - box.X) * consolePixelSweepMS / box.W
					}
					if sweep < 0 {
						sweep = 0
					}
					l.cells = append(l.cells, consolePixelCell{
						x:     cx,
						y:     cy,
						sx:    -mx + int32(h1%uint32(spanW)),
						sy:    -my + int32(h2%uint32(spanH)),
						delay: uint32(sweep) + h3%consolePixelJitterMS,
					})
				}
			}
			x += 6 * scale
		}
		y += 8 * scale
	}
	return l
}

// consolePixelEase is the flight's progress at t (0..1 of consolePixelFlightMS):
// an ease-out that runs a little past 1 and comes back.
func consolePixelEase(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	u := t - 1
	return 1 + (consolePixelOvershoot+1)*u*u*u + consolePixelOvershoot*u*u
}

// consolePixelBuf holds a frame's squares by colour; the slices are reused so
// drawing a name allocates nothing once they have grown.
var consolePixelBuf struct {
	shadow, rest, fly, wait []sdl.Rect
}

// consolePixelAppend adds the size×size square at (x, y), cut to clip.
func consolePixelAppend(dst []sdl.Rect, x, y, size int32, clip sdl.Rect) []sdl.Rect {
	x2, y2 := x+size, y+size
	if x < clip.X {
		x = clip.X
	}
	if y < clip.Y {
		y = clip.Y
	}
	if x2 > clip.X+clip.W {
		x2 = clip.X + clip.W
	}
	if y2 > clip.Y+clip.H {
		y2 = clip.Y + clip.H
	}
	if x2 <= x || y2 <= y {
		return dst
	}
	return append(dst, sdl.Rect{X: x, Y: y, W: x2 - x, H: y2 - y})
}

// consolePixelFill draws one colour's squares in one call.
func consolePixelFill(renderer *sdl.Renderer, rects []sdl.Rect, c sdl.Color) {
	if len(rects) == 0 || c.A == 0 {
		return
	}
	_ = renderer.SetDrawColor(c.R, c.G, c.B, c.A)
	_ = renderer.FillRects(rects)
}

// consolePixelAskFrames keeps frames coming while letters fly: the menus'
// active cadence, past Low Power's idle cap (the handheld's default), the way
// the focus ring's move asks (homefocusglide.go). Battery Saver never gets
// here: it implies Reduce motion, so nothing flies.
func consolePixelAskFrames() {
	if batterySaverActive {
		requestFrameEvery(1)
		return
	}
	requestFrameEveryPastLowPower(1)
}

// drawConsolePixelCells draws a laid-out name with its button's corner at
// (ox, oy), every square cut to clip. elapsed is the time since the player
// landed on it: while animate is set and it is under consolePixelTotalMS the
// squares that have not set off wait at their start (faint), the ones on
// their way are drawn a step lighter, and the ones home in ink over their
// shadow (none when shadow's alpha is 0).
func drawConsolePixelCells(renderer *sdl.Renderer, l *consolePixelLayout, ox, oy int32, clip sdl.Rect, ink, shadow sdl.Color, elapsed uint64, animate bool) {
	if renderer == nil || l == nil || len(l.cells) == 0 || clip.W <= 0 || clip.H <= 0 {
		return
	}
	buf := &consolePixelBuf
	buf.shadow, buf.rest, buf.fly, buf.wait = buf.shadow[:0], buf.rest[:0], buf.fly[:0], buf.wait[:0]
	size := l.size
	drop := max32(1, size/3)
	moving := animate && elapsed < consolePixelTotalMS
	for _, c := range l.cells {
		if moving && elapsed < uint64(c.delay)+consolePixelFlightMS {
			if elapsed <= uint64(c.delay) {
				buf.wait = consolePixelAppend(buf.wait, ox+c.sx, oy+c.sy, size, clip)
				continue
			}
			p := consolePixelEase(float64(elapsed-uint64(c.delay)) / consolePixelFlightMS)
			fx := float64(c.sx) + float64(c.x-c.sx)*p
			fy := float64(c.sy) + float64(c.y-c.sy)*p
			buf.fly = consolePixelAppend(buf.fly, ox+int32(math.Round(fx)), oy+int32(math.Round(fy)), size, clip)
			continue
		}
		if shadow.A > 0 {
			buf.shadow = consolePixelAppend(buf.shadow, ox+c.x+drop, oy+c.y+drop, size, clip)
		}
		buf.rest = consolePixelAppend(buf.rest, ox+c.x, oy+c.y, size, clip)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	consolePixelFill(renderer, buf.shadow, shadow)
	consolePixelFill(renderer, buf.wait, WithAlpha(ink, uint8(uint32(ink.A)*2/5)))
	consolePixelFill(renderer, buf.fly, WithAlpha(ink, uint8(uint32(ink.A)*3/4)))
	consolePixelFill(renderer, buf.rest, ink)
	if moving {
		consolePixelAskFrames()
	}
}

// drawConsolePixelLogo letters a console's name in the bitmap face inside
// rect (4px in from its edges), each line centred, at up to maxScale (a
// tile's cover slot asks for 4, so a short name reads as a cartridge label).
// Animate flies the squares in from across rect over the first
// consolePixelTotalMS of elapsed; nothing is drawn outside rect.
func drawConsolePixelLogo(renderer *sdl.Renderer, name string, rect sdl.Rect, maxScale int32, color sdl.Color, elapsed uint64, animate bool) {
	if renderer == nil || rect.W <= 8 || rect.H <= 8 {
		return
	}
	l := consolePixelLayoutFor(name, sdl.Rect{X: 4, Y: 4, W: rect.W - 8, H: rect.H - 8}, rect.W, rect.H, maxScale)
	drawConsolePixelCells(renderer, l, rect.X, rect.Y, rect, color, sdl.Color{}, elapsed, animate)
}
