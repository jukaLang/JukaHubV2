package main

// icondraw.go — the entry points every icon in the shell is drawn through.
//
// Callers name a glyph (a canonical name from icondefs.go or an alias, in any
// case) and give a box and an ink colour; iconDraw centres the largest square
// that fits, resolves the name, and serves it from the atlas (iconatlas.go) at
// that exact pixel size. DrawHomeIcon, drawHomeGlyphTinted and the Tools and
// launcher painters are thin wrappers that pick the ink.

import "github.com/veandco/go-sdl2/sdl"

// iconSquare is the largest square centred in rect.
func iconSquare(rect sdl.Rect) sdl.Rect {
	size := rect.W
	if rect.H < size {
		size = rect.H
	}
	return sdl.Rect{X: rect.X + (rect.W-size)/2, Y: rect.Y + (rect.H-size)/2, W: size, H: size}
}

// iconDraw draws the named glyph centred in rect in the given ink. Unknown
// names draw the placeholder glyph, never nothing.
func iconDraw(renderer *sdl.Renderer, name string, rect sdl.Rect, color sdl.Color) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 || color.A == 0 {
		return
	}
	dst := iconSquare(rect)
	key := iconResolve(name)
	if homeIconAtlas.draw(renderer, key, dst, color) {
		return
	}
	homeIconAtlas.directPaint()
	iconDrawDirect(renderer, key, dst, color)
}

// iconDrawDirect paints the glyph without the atlas texture, as horizontal
// runs of its coverage mask: the same picture, for renderers that cannot hold
// the atlas. Sizes past the atlas cap paint at the cap, centred.
func iconDrawDirect(renderer *sdl.Renderer, name string, rect sdl.Rect, color sdl.Color) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 || color.A == 0 {
		return
	}
	dst := iconSquare(rect)
	size := dst.W
	if size > iconAtlasMaxGlyph {
		size = iconAtlasMaxGlyph
	}
	runs := homeIconAtlas.runsFor(iconAtlasKey{name: iconResolve(name), size: size})
	iconPaintRuns(renderer, runs, dst.X+(dst.W-size)/2, dst.Y+(dst.H-size)/2, color)
}

// iconHomeInk is the ink a home glyph draws in: the theme's primary text
// colour, or the dark ink a focused tile's accent badge needs.
func iconHomeInk(focused bool) sdl.Color {
	if focused {
		return ColorIconDark
	}
	return ColorTextPrimary()
}

// DrawHomeIcon renders one glyph of the shared icon set inside the given box,
// in the theme's text ink (dark ink when focused). Every name in icondefs.go
// works here, as do the legacy aliases; an unknown name draws the placeholder.
func DrawHomeIcon(renderer *sdl.Renderer, name string, rect sdl.Rect, focused bool) {
	iconDraw(renderer, name, rect, iconHomeInk(focused))
}

// drawHomeGlyphTinted paints a glyph in an explicit colour: the accent tiles
// (Media, the Continue play button) carry their icon on a tinted badge, where
// the theme's body-text colour would be wrong.
func drawHomeGlyphTinted(renderer *sdl.Renderer, name string, rect sdl.Rect, color sdl.Color) {
	iconDraw(renderer, name, rect, color)
}
