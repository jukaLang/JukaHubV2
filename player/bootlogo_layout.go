package main

// bootlogo_layout.go — where the boot screen's logo, name and progress sit.
//
// A power-on shows three pictures of the same screen one after another: the
// boot logo the firmware draws before Linux starts (bootlogo.go writes it at
// the panel's resolution), the first frame of the boot animation (bootos.go,
// on the canvas) and, for anyone flashing by hand, the reference images
// branding/mkbootlogo.py renders. They only read as one continuous boot if the
// logo and the wordmark land on the same pixels in all three, so one function
// places them, in fractions of the screen height, for a screen of any size.
//
// Change a constant here and its twin in branding/mkbootlogo.py together.

import "github.com/veandco/go-sdl2/sdl"

const (
	// bootLogoSpriteFrac is the side of the logo artwork's square (the 300 px
	// JukaPlayer.png canvas, its transparent margin included), of the height.
	bootLogoSpriteFrac = 0.25
	// bootLogoWordFrac is the wordmark's font size, of the height.
	bootLogoWordFrac = 0.06
	// bootLogoGapFrac separates the artwork's square from the wordmark's line
	// box, of the height.
	bootLogoGapFrac = 0.03
	// bootLogoLiftFrac raises the group above the true centre to the optical
	// one, of the height.
	bootLogoLiftFrac = 0.02
	// bootLogoTrackGapFrac separates the wordmark's line box from the progress
	// track, of the height.
	bootLogoTrackGapFrac = 0.055
	// bootLogoTrackWFrac is the progress track's width, of the width.
	bootLogoTrackWFrac = 0.2
	// bootLogoStatusGapFrac separates the track from the status line under it.
	bootLogoStatusGapFrac = 0.018
	// bootLogoInterLineEm is Inter's line height (ascender + descender over the
	// em), used when no font is at hand to measure the wordmark's line box.
	bootLogoInterLineEm = 1.2109375

	// The artwork's own construction (branding/mklogo.py), as fractions of the
	// artwork's square: the tile's margin and corner, and the glass pane inside
	// it whose rim the boot animation lights.
	bootLogoTileMargin = 0.055 // TILE_MARGIN
	bootLogoTileCorner = 0.25  // TILE_CORNER, of the tile's side
	bootLogoPaneInset  = 0.165 // PANE_INSET
	bootLogoPaneCorner = 0.23  // PANE_CORNER, of the pane's side

	// bootLogoWordText is the wordmark.
	bootLogoWordText = "JukaHub"
)

// bootLogoInk is the wordmark's colour on the black boot screen: white with a
// breath of the logo's ice, so it does not glare on a dark panel.
var bootLogoInk = sdl.Color{R: 238, G: 243, B: 255, A: 255}

// bootLogoLayout is the boot screen's geometry on one screen size.
type bootLogoLayout struct {
	// Sprite is the artwork's square, margin included: the logo texture or
	// image is drawn to exactly this rect.
	Sprite sdl.Rect
	// WordPx is the wordmark's font size in pixels.
	WordPx int
	// WordTop is the top of the wordmark's line box (the top of the surface
	// SDL_ttf renders), and WordLineH that box's height.
	WordTop   int32
	WordLineH int32
	// Track is the progress track; StatusY is the top of the status line
	// under it.
	Track   sdl.Rect
	StatusY int32
}

// bootLogoWordPx is the wordmark's font size on a screen h pixels tall.
func bootLogoWordPx(h int32) int {
	px := int(float64(h)*bootLogoWordFrac + 0.5)
	if px < 12 {
		px = 12
	}
	return px
}

// bootLogoRound rounds a non-negative fraction of a size to whole pixels.
func bootLogoRound(v float64) int32 {
	if v < 0 {
		return -int32(-v + 0.5)
	}
	return int32(v + 0.5)
}

// bootLogoPlace lays the boot screen out on a w×h screen. lineH is the height
// of the wordmark's line box as the font in use reports it (SDL_ttf's surface
// height for the wordmark); zero or less estimates it from Inter's metrics.
// The artwork, the wordmark and the gap between them are centred as a group,
// lifted a little above the middle, and the progress track sits under them.
func bootLogoPlace(w, h, lineH int32) bootLogoLayout {
	if w <= 0 || h <= 0 {
		return bootLogoLayout{}
	}
	fh := float64(h)
	side := bootLogoRound(fh * bootLogoSpriteFrac)
	if side > w {
		side = w
	}
	wordPx := bootLogoWordPx(h)
	if lineH <= 0 {
		lineH = bootLogoRound(float64(wordPx) * bootLogoInterLineEm)
	}
	gap := bootLogoRound(fh * bootLogoGapFrac)
	group := side + gap + lineH
	top := bootLogoRound(float64(h-group)/2 - fh*bootLogoLiftFrac)
	if top < 0 {
		top = 0
	}
	l := bootLogoLayout{
		Sprite:    sdl.Rect{X: (w - side) / 2, Y: top, W: side, H: side},
		WordPx:    wordPx,
		WordTop:   top + side + gap,
		WordLineH: lineH,
	}
	trackW := bootLogoRound(float64(w) * bootLogoTrackWFrac)
	trackH := bootLogoRound(fh * 0.0045)
	if trackH < 3 {
		trackH = 3
	}
	l.Track = sdl.Rect{
		X: (w - trackW) / 2,
		Y: l.WordTop + lineH + bootLogoRound(fh*bootLogoTrackGapFrac),
		W: trackW,
		H: trackH,
	}
	l.StatusY = l.Track.Y + l.Track.H + bootLogoRound(fh*bootLogoStatusGapFrac)
	return l
}

// bootLogoTileRect is the visible tile inside the artwork's square, and its
// corner radius.
func bootLogoTileRect(sprite sdl.Rect) (sdl.FRect, float64) {
	s := float64(sprite.W)
	m := s * bootLogoTileMargin
	side := s - 2*m
	return sdl.FRect{X: float32(float64(sprite.X) + m), Y: float32(float64(sprite.Y) + m), W: float32(side), H: float32(side)},
		side * bootLogoTileCorner
}

// bootLogoPaneRect is the glass pane inside the tile — the rounded square whose
// cyan-to-violet rim the boot animation runs its light around — and its corner
// radius.
func bootLogoPaneRect(sprite sdl.Rect) (sdl.FRect, float64) {
	s := float64(sprite.W)
	inset := s * bootLogoPaneInset
	side := s - 2*inset
	return sdl.FRect{X: float32(float64(sprite.X) + inset), Y: float32(float64(sprite.Y) + inset), W: float32(side), H: float32(side)},
		side * bootLogoPaneCorner
}
