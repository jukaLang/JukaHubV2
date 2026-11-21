package main

// hometabs_minimal.go — the Tabs Home's Minimal view (a text-only main
// menu): the page's items as big names, one per line, no art, no
// tiles; the selected name in the accent colour, the others muted. It is one
// of the views every tab and folder can be drawn in (homeTabViews: A on the
// strip, Left/Right on the L2 sheet).
//
// The lines are rects like every other view's (homeTabsLayout), so the D-pad
// steps by them (one column: Up/Down walk it and leave it at its ends), the
// page scrolls to keep the selected line in view, the pointer finds them and
// the list fades at an edge with more past it.

import (
	"math"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// homeTabViewMinimal is the Minimal view's name (homeTabViews).
const homeTabViewMinimal = "minimal"

// homeTabsMinimalLayout lays n names out one per line down the body: each
// line as wide as the body, tall enough for the large face. k is the canvas
// scale. Pure.
func homeTabsMinimalLayout(n int, bw int32, k float64, out []sdl.Rect) ([]sdl.Rect, bool, int32) {
	rowH := max32(36, int32(math.Round(64*k)))
	step := rowH + max32(2, int32(math.Round(6*k)))
	for i := 0; i < n; i++ {
		out = append(out, sdl.Rect{X: 0, Y: int32(i) * step, W: bw, H: rowH})
	}
	return out, false, int32(n)*step - (step - rowH)
}

// homeTabsMinimalFont is the largest of Home's faces that fits a line h
// pixels tall.
func homeTabsMinimalFont(fonts HomeFonts, h int32) *ttf.Font {
	for _, f := range [...]*ttf.Font{fonts.Greeting, fonts.Card, fonts.Tile, fonts.Small} {
		if f != nil && int32(f.Height()) <= h {
			return f
		}
	}
	if fonts.Small != nil {
		return fonts.Small
	}
	return fonts.Tile
}

// tabsDrawMinimal draws the Minimal view: each line's name, the selected one
// in the accent (the primary ink while the focus is off the page, so the
// accent always means "A opens this"), a dimmed item in the tertiary ink.
func (h *HomeLayout) tabsDrawMinimal(renderer *sdl.Renderer, fonts HomeFonts, lvl *homeTabsLevel, g *homeTabsGeom, body sdl.Rect,
	dx, scroll int32, clip sdl.Rect, focused, pressed bool, alpha uint8, live bool) {
	if len(g.rects) == 0 {
		return
	}
	font := homeTabsMinimalFont(fonts, g.rects[0].H)
	if font == nil {
		return
	}
	fh := int32(font.Height())
	pad := scaleX(12, h.Width)
	for i, it := range lvl.items {
		if i >= len(g.rects) {
			break
		}
		r := g.rects[i]
		r.X += body.X + dx
		r.Y += body.Y - scroll
		if !homeRectsOverlap(r, clip) {
			continue
		}
		ink := ColorTextMuted()
		switch {
		case i == lvl.focus && focused:
			ink = ColorAccent
		case i == lvl.focus:
			ink = ColorTextPrimary()
		case it.Dim:
			ink = ColorTextTertiary()
		}
		x := r.X + pad
		if i == lvl.focus && focused && pressed {
			x += scaleX(3, h.Width)
		}
		if room := r.W - 2*pad; room > 0 {
			drawText(renderer, font, ellipsize(font, it.Title, room), x, r.Y+(r.H-fh)/2, homeTabsInk(ink, alpha), textAlignLeft)
		}
		if live {
			homeTabs.hits = append(homeTabs.hits, homeTabsHit{i: i, r: glassIntersect(r, clip)})
		}
	}
}
