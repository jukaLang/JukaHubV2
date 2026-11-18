package main

// The first-run tour keeps colour and Home layout as separate choices. The
// rows preview each arrangement without saving every highlight movement; Next
// or A applies exactly the one the person chose.

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

var onboardingThemeChoices = [...]string{
	"hub", "light", "oled", "ocean", "sunset", "sakura", "nord", "highcontrast",
}

var onbLayout struct {
	cursor int
	hits   []sdl.Rect
}

var onboardingLayoutNotes = [...]string{
	"Sections with their own pages",
	"Classic dashboard cards",
	"Large Continue tile",
	"Browse one tile at a time",
	"Flexible tile board",
}

func onboardingLayoutStepIs() bool {
	return onboarding.step >= 0 && onboarding.step < len(onboardingSteps) && onboardingSteps[onboarding.step].kind == "layout"
}

func onboardingLayoutReset(config *Config) {
	onbLayout.cursor = homeArrangementChoiceIndex(homeArrangement(config))
	onbLayout.hits = onbLayout.hits[:0]
}

func onboardingLayoutMove(delta int) {
	next := onbLayout.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= len(homeArrangementChoiceList) {
		next = len(homeArrangementChoiceList) - 1
	}
	if next != onbLayout.cursor {
		onbLayout.cursor = next
		PlayNavSound()
	}
}

func onboardingLayoutClick(mx, my int32) bool {
	for i, rect := range onbLayout.hits {
		if !pointInRect(mx, my, rect) {
			continue
		}
		onboardingLayoutMove(i - onbLayout.cursor)
		return true
	}
	return false
}

func onboardingLayoutCommit(config *Config) {
	if config == nil || onbLayout.cursor < 0 || onbLayout.cursor >= len(homeArrangementChoiceList) {
		return
	}
	homeSetArrangement(config, homeArrangementChoiceList[onbLayout.cursor])
}

// onboardingRenderLayout puts all five choices on the card. A short, wide
// canvas uses two columns; a narrow canvas keeps enough width for the labels.
func onboardingRenderLayout(renderer *sdl.Renderer, config *Config, cy, cardH, textX, textW, top int32,
	bodyFont, smallFont *ttf.Font, accent sdl.Color) {
	onbLayout.hits = onbLayout.hits[:0]
	if renderer == nil || bodyFont == nil || smallFont == nil {
		return
	}
	bottom := cy + cardH - 108
	available := bottom - top
	if available <= 0 {
		return
	}
	cols := int32(1)
	if textW >= 380 && available/int32(len(homeArrangementChoiceList)) < int32(bodyFont.Height())+22 {
		cols = 2
	}
	rows := (int32(len(homeArrangementChoiceList)) + cols - 1) / cols
	pitch := min32(60, available/rows)
	gap := int32(7)
	rowH := pitch - gap
	if rowH < 24 {
		rowH = min32(24, pitch)
	}
	colW := (textW + 20 - gap*(cols-1)) / cols
	perCol := (int32(len(homeArrangementChoiceList)) + cols - 1) / cols
	for i, name := range homeArrangementChoiceList {
		col, row := int32(i)/perCol, int32(i)%perCol
		rect := sdl.Rect{X: textX - 10 + col*(colW+gap), Y: top + row*pitch, W: colW, H: rowH}
		onbLayout.hits = append(onbLayout.hits, rect)
		selected := i == onbLayout.cursor
		UIListRow(renderer, rect, RadiusMD, selected, false)
		markX, markY := rect.X+rect.W-18, rect.Y+rect.H/2
		if selected {
			fillCircle(renderer, markX, markY, 7, accent)
			fillCircle(renderer, markX, markY, 2, contrastText(accent))
		} else {
			strokeCircle(renderer, markX, markY, 7, WithAlpha(ColorTextTertiary(), 160))
		}
		thumb := sdl.Rect{X: rect.X + 9, Y: rect.Y + 5, W: min32(82, rect.W/3), H: rect.H - 10}
		onboardingLayoutThumbnail(renderer, name, thumb, accent)
		labelX := thumb.X + thumb.W + 12
		labelW := markX - 12 - labelX
		font := smallFont
		if cols == 1 && rowH >= int32(bodyFont.Height())+int32(smallFont.Height())+6 {
			font = bodyFont
		}
		ink := ColorTextSecondary()
		if selected {
			ink = ColorTextPrimary()
		}
		if cols == 1 && font == bodyFont {
			labelY := rect.Y + (rect.H-int32(bodyFont.Height())-int32(smallFont.Height())-2)/2
			renderText(renderer, config, font, ellipsize(font, homeArrangementChoiceLabel(name), labelW), ink, labelX, labelY)
			renderText(renderer, config, smallFont, ellipsize(smallFont, onboardingLayoutNotes[i], labelW), ColorTextTertiary(),
				labelX, labelY+int32(bodyFont.Height())+2)
		} else {
			renderText(renderer, config, font, ellipsize(font, homeArrangementChoiceLabel(name), labelW), ink,
				labelX, rect.Y+(rect.H-int32(font.Height()))/2)
		}
	}
}

func onboardingLayoutThumbnail(renderer *sdl.Renderer, name string, r sdl.Rect, accent sdl.Color) {
	if r.W <= 10 || r.H <= 10 {
		return
	}
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 4, ColorBackground)
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 4, 1, ColorBorder)
	x, y, w, h := r.X+5, r.Y+5, r.W-10, r.H-10
	quiet := ColorTextTertiary()
	tile := func(rx, ry, rw, rh int32, color sdl.Color) {
		if rw > 0 && rh > 0 {
			fillRoundedRect(renderer, rx, ry, rw, rh, 2, color)
		}
	}
	switch normalizeHomeArrangement(name) {
	case homeArrangementTabs:
		tile(x, y, w, 4, quiet)
		tile(x+w/3, y, w/3, 4, accent)
		for i := int32(0); i < 3; i++ {
			tile(x+i*(w+4)/3, y+8, (w-8)/3, h-8, quiet)
		}
	case homeArrangementGrid:
		tile(x, y, w/3, h, accent)
		for i := int32(0); i < 2; i++ {
			for j := int32(0); j < 2; j++ {
				tile(x+w/3+4+i*(w-w/3)/2, y+j*(h+3)/2, (w-w/3-8)/2, (h-3)/2, quiet)
			}
		}
	case homeArrangementCarousel:
		tile(x, y+h/4, w/4, h/2, quiet)
		tile(x+w/4+3, y, w/2-6, h, accent)
		tile(x+3*w/4, y+h/4, w/4, h/2, quiet)
	case homeArrangementWidgets:
		for i := int32(0); i < 3; i++ {
			for j := int32(0); j < 2; j++ {
				color := quiet
				if i == 1 && j == 0 {
					color = accent
				}
				tile(x+i*(w+3)/3, y+j*(h+3)/2, (w-6)/3, (h-3)/2, color)
			}
		}
	default: // Standard: one wide feature card, smaller cards below.
		tile(x, y, w*2/3, h*2/3, accent)
		tile(x+w*2/3+3, y, w/3-3, h*2/3, quiet)
		for i := int32(0); i < 3; i++ {
			tile(x+i*(w+3)/3, y+h*2/3+3, (w-6)/3, h/3-3, quiet)
		}
	}
}
