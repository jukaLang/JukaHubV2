package main

// thememinimal.go — Minimal Style's Home: no tiles, no art, no cards, just
// words. Home's tiles become a left-aligned list of names (carouselStyle
// "text"); Up and Down walk it, the focused name is bright with a thin accent
// bar before it and its one-line description after it, the others are muted.
// The list scrolls only when the focus would leave the visible rows.
//
// It hooks the carousel the way the PS5 hub does (one line each):
// home_carousel.go carOffset/carSettled/carPlace/carRender, themeconsole.go
// carStyleRect/homeCarBaseSize/homeConsoleAnimating/applyThemeConsoleLook/
// drawThemeSketchFor/normalizeCarouselStyle, home_dynbody.go dynStepFrom.

import (
	"math"

	"github.com/veandco/go-sdl2/sdl"
)

// homeCarouselText is Minimal Style's text list.
const homeCarouselText = "text"

// The list in 1280x720 design units.
const (
	homeTextX     = 112.0
	homeTextTop   = 168.0
	homeTextRow   = 56.0
	homeTextW     = 1056.0
	homeTextShown = 8
)

// homeText is the list's scroll: first is the first row shown, pos eases
// toward it.
var homeText struct {
	first  int
	pos    float64
	tick   uint64
	placed bool
	anim   bool
}

// textStep keeps the focused row among the shown ones and eases the list
// there (carPlace, every refresh).
func (h *HomeLayout) textStep() {
	s := &homeText
	n, i := len(h.dyn.tiles), h.dyn.carIdx
	if i < s.first {
		s.first = i
	}
	if i > s.first+homeTextShown-1 {
		s.first = i - homeTextShown + 1
	}
	s.first = max(0, min(s.first, n-homeTextShown))
	target := float64(s.first)
	now := sdl.GetTicks64()
	dt := 0.0
	if s.tick != 0 && now > s.tick {
		dt = math.Min(float64(now-s.tick), 100)
	}
	s.tick = now
	if !s.placed || !h.Motion || math.Abs(target-s.pos) < 0.002 {
		s.pos, s.placed = target, true
	} else {
		s.pos = homeDynEase(s.pos, target, dt)
	}
	s.anim = s.pos != target
}

// textRowRect is row i's hit rect on the canvas; false when it is scrolled
// out of the list's window.
func (h *HomeLayout) textRowRect(i int) (sdl.Rect, bool) {
	o := float64(i) - homeText.pos
	if o < -0.5 || o > float64(homeTextShown)-0.5 {
		return sdl.Rect{}, false
	}
	return h.carDesignRect(homeTextX, homeTextTop+o*homeTextRow, homeTextW, homeTextRow), true
}

// textStepFrom is one D-pad step in the list: Up and Down walk it, Up from
// the first row leaves for the top bar, Down from the last stays.
func (h *HomeLayout) textStepFrom(cur HomeFocusID, i int, direction FocusDirection) (HomeFocusID, bool) {
	n := len(h.dyn.tiles)
	switch direction {
	case FocusUp:
		if i > 0 {
			return h.dyn.tiles[i-1].ID, true
		}
		return "", false
	case FocusDown:
		if i < n-1 {
			return h.dyn.tiles[i+1].ID, true
		}
		return "", false
	}
	return cur, true
}

// textRender draws the list: names only, the focused one bright.
func (h *HomeLayout) textRender(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData, focused HomeFocusID) {
	f := fonts.Card
	if f == nil {
		f = fonts.Tile
	}
	if f == nil {
		return
	}
	sub := fonts.Helper
	accent := ColorAccent
	if homeConsoleLook.hasRing {
		accent = homeConsoleLook.ring
	}
	muted := ColorTextMuted()
	muted.A = 150
	maxW := scaleX(475, h.Width) // ≈ 0.45 × homeTextW (475.2); Go can't truncate a non-integer constant to int32
	for _, t := range h.dyn.tiles {
		item := h.Items[t.ID]
		r := item.Rect
		if r.W <= 0 || item.Title == "" {
			continue
		}
		mid := r.Y + r.H/2
		y := mid - int32(f.Height())/2
		if data.Pressed == t.ID {
			y += homePressOffset(data.PressDepth, h.Height)
		}
		if t.ID != focused {
			drawText(renderer, f, ellipsize(f, item.Title, maxW), r.X, y, muted, textAlignLeft)
			continue
		}
		bar := sdl.Rect{X: r.X - scaleX(22, h.Width), Y: mid - int32(f.Height())*3/8,
			W: max32(scaleX(4, h.Width), 2), H: int32(f.Height()) * 3 / 4}
		renderer.SetDrawColor(accent.R, accent.G, accent.B, 255)
		renderer.FillRect(&bar)
		title := ellipsize(f, item.Title, maxW)
		drawText(renderer, f, title, r.X, y, ColorTextPrimary(), textAlignLeft)
		if sub != nil && item.Subtitle != "" {
			tw, _, err := f.SizeUTF8(title)
			if err == nil {
				x := r.X + int32(tw) + scaleX(28, h.Width)
				room := r.X + r.W - x
				if room > scaleX(80, h.Width) {
					drawText(renderer, sub, ellipsize(sub, item.Subtitle, room), x,
						mid-int32(sub.Height())/2, ColorTextMuted(), textAlignLeft)
				}
			}
		}
	}
}

// drawThemeSketchText is Minimal Style's theme-card sketch: a few lines of
// text, the second one marked.
func drawThemeSketchText(renderer *sdl.Renderer, x, y, w, h int32, ink, ring sdl.Color) {
	soft := sdl.Color{R: ink.R, G: ink.G, B: ink.B, A: 90}
	bar := max32(h/16, 2)
	gap := bar * 2
	x0 := x + w/8
	widths := [...]int32{w * 2 / 5, w / 2, w / 3, w * 3 / 8, w / 4}
	for k, lw := range widths {
		ly := y + h/6 + int32(k)*(bar+gap)
		if ly+bar > y+h {
			break
		}
		colour := soft
		if k == 1 {
			colour = ink
			fillRoundedRect(renderer, x0-gap, ly, max32(bar/2, 1), bar, 0, ring)
		}
		fillRoundedRect(renderer, x0, ly, lw, bar, 1, colour)
	}
}
