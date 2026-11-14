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

func consoleLogoFit(name string, maxW int32) (string, int32) {
	name = consoleLogoText(name)
	letters := []rune(name)
	scale := int32(2)
	if int32(len(letters))*6*scale > maxW {
		words := strings.Fields(name)
		for split := 1; split < len(words); split++ {
			first, second := strings.Join(words[:split], " "), strings.Join(words[split:], " ")
			if int32(len([]rune(first)))*12 <= maxW && int32(len([]rune(second)))*12 <= maxW {
				return first + "\n" + second, scale
			}
		}
		scale = 1
	}
	if maxW < 6*scale {
		return "", scale
	}
	maxLetters := int(maxW / (6 * scale))
	if len(letters) > maxLetters {
		if maxLetters > 1 {
			letters = append(letters[:maxLetters-1], '.')
		} else {
			letters = letters[:1]
		}
	}
	return string(letters), scale
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

func drawConsolePixelLogo(renderer *sdl.Renderer, name string, rect sdl.Rect, maxW int32, color sdl.Color, elapsed uint64, animate bool) {
	if renderer == nil || maxW <= 0 {
		return
	}
	name, scale := consoleLogoFit(name, maxW)
	if name == "" {
		return
	}
	height := 7 * scale
	if strings.Contains(name, "\n") {
		height = 15 * scale
	}
	y := rect.Y + (rect.H-height-3)/2
	x0 := rect.X + 12
	x := x0
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = renderer.SetDrawColor(color.R, color.G, color.B, color.A)
	pixel := 0
	for _, r := range name {
		if r == '\n' {
			x = x0
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
				if px < rect.X || py < rect.Y || px+scale > rect.X+rect.W || py+scale > rect.Y+rect.H-3 {
					continue
				}
				_ = renderer.FillRect(&sdl.Rect{X: px, Y: py, W: scale, H: scale})
			}
		}
		x += 6 * scale
	}
}
