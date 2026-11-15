package main

// Small bitmap lettering gives each console a recognisable card title. The
// focused title assembles one square at a time when the player lands on it.

import (
	"strings"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
)

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
}

func consoleLogoText(name string) string {
	return strings.ToUpper(strings.TrimSpace(name))
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

func consolePixelEase(elapsed uint64, pixel int) float64 {
	if elapsed >= 470 {
		return 1
	}
	delay := uint64((pixel * 37) % 130)
	if elapsed <= delay {
		return 0
	}
	p := float64(elapsed-delay) / 340
	if p > 1 {
		p = 1
	}
	return p * p * (3 - 2*p)
}

// drawConsolePixelLogo letters a console's name in the bitmap face, each line
// centred in rect, at up to maxScale (a tile's cover slot asks for 4, so a
// short name reads as a cartridge label). Animate assembles the letters one
// square at a time over the first 470 ms of elapsed.
func drawConsolePixelLogo(renderer *sdl.Renderer, name string, rect sdl.Rect, maxScale int32, color sdl.Color, elapsed uint64, animate bool) {
	maxW, maxH := rect.W-8, rect.H-8
	if renderer == nil || maxW <= 0 || maxH <= 0 {
		return
	}
	name, scale := consoleLogoFit(name, maxW, maxH, maxScale)
	if name == "" {
		return
	}
	lines := strings.Split(name, "\n")
	height := int32(len(lines))*8*scale - scale
	lineX := func(line string) int32 {
		w := int32(len([]rune(line)))*6*scale - scale
		return rect.X + (rect.W-w)/2
	}
	y := rect.Y + (rect.H-height)/2
	x := lineX(lines[0])
	line := 0
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = renderer.SetDrawColor(color.R, color.G, color.B, color.A)
	pixel := 0
	for _, r := range name {
		if r == '\n' {
			line++
			x = lineX(lines[line])
			y += 8 * scale
			continue
		}
		glyph, ok := consolePixelGlyphs[unicode.ToUpper(r)]
		if !ok {
			glyph = consolePixelGlyphs['?']
		}
		for row, bits := range glyph {
			for col := 0; col < 5; col++ {
				if bits&(1<<(4-col)) == 0 {
					continue
				}
				px, py := x+int32(col)*scale, y+int32(row)*scale
				if animate {
					p := consolePixelEase(elapsed, pixel)
					px += int32(float64((pixel*17)%43-21) * (1 - p))
					py += int32(float64((pixel*29)%23-11) * (1 - p))
				}
				pixel++
				if px < rect.X || py < rect.Y || px+scale > rect.X+rect.W || py+scale > rect.Y+rect.H {
					continue
				}
				_ = renderer.FillRect(&sdl.Rect{X: px, Y: py, W: scale, H: scale})
			}
		}
		x += 6 * scale
	}
}
