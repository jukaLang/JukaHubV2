package main

// Offscreen check for the video-snap preview: the art box shows the clip when
// one is playing for the focused game, and keeps showing the cover when the
// frame belongs to another game, when there is no frame yet, or when the clip
// is a lie about its own size.

import (
	"image/color"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// snapTestFrame publishes a flat grey 320x180 IYUV frame for gamePath, the way
// the decoder's swap does.
func snapTestFrame(t *testing.T, gamePath string) {
	t.Helper()
	const w, h = snapPreviewWidth, snapPreviewHeight
	buf := make([]byte, int(w*h*3/2))
	for i := range buf {
		buf[i] = 128 // Y=U=V=128 is mid-grey in every matrix
	}
	snapState.mu.Lock()
	snapState.game = gamePath
	snapState.snap = "/roms/nes/A.mp4"
	snapState.frame = buf
	snapState.w, snapState.h = w, h
	snapState.seq++
	snapState.playing = true
	snapState.starting = false
	snapState.mu.Unlock()
	t.Cleanup(snapPreviewReset)
}

// renderLibraryArt draws the art box on the probe renderer and returns the
// captured frame.
func renderLibraryArt(t *testing.T, rect sdl.Rect, game libraryGame) *probeFrame {
	t.Helper()
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	t.Cleanup(func() { destroyTestRenderer(renderer) })

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	drawLibraryArt(renderer, &Config{}, rect, game)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}
	return f
}

// TestLibraryArtShowsTheSnapFrame is the visible half of the feature: a frame
// for the focused game replaces the cover inside the same box.
func TestLibraryArtShowsTheSnapFrame(t *testing.T) {
	withScreen(t, probeW, probeH)
	ClearTextCache()
	t.Cleanup(ClearTextCache)

	cover := filepath.Join(t.TempDir(), "A.png")
	writeSolidPNG(t, cover, 128, 128, color.RGBA{R: 235, G: 40, B: 40, A: 255})
	game := libraryGame{Path: "/roms/nes/A.nes", System: "NES", Title: "Adventure", Art: cover, HasArt: true}
	rect := sdl.Rect{X: 40, Y: 40, W: 250, H: 250}

	snapTestFrame(t, game.Path)
	f := renderLibraryArt(t, rect, game)

	// The middle of the box is the frame: grey, and unmistakably not the red
	// cover.
	mid := f.at(rect.X+rect.W/2, rect.Y+rect.H/2)
	if mid.R < 100 || mid.R > 160 || mid.G < 100 || mid.G > 160 || mid.B < 100 || mid.B > 160 {
		t.Fatalf("middle of the art box = %v, want the grey snap frame", mid)
	}
	// The lower half only: the "SNAP" chip in the top-left corner uses the
	// theme accent, which a custom theme may well make red.
	lower := sdl.Rect{X: rect.X, Y: rect.Y + rect.H/2, W: rect.W, H: rect.H / 2}
	if reds := countPixelsMatching(f, lower, func(c sdl.Color) bool {
		return c.R > 190 && c.G < 90 && c.B < 90
	}); reds > 0 {
		t.Fatalf("%d cover pixels are still visible behind the snap", reds)
	}

	// The clip is 16:9 inside a square box, so there are letterbox areas above
	// and below the picture: they must not be the frame colour.
	top := f.at(rect.X+rect.W/2, rect.Y+3)
	if top.G > 150 && top.B > 150 {
		t.Fatalf("the letterbox bar at the top of the box = %v, want a dark bar", top)
	}
}

// TestLibraryArtFallsBackToTheCover covers the three ways the box must stay box
// art: no frame yet, a frame that belongs to a different game, and a frame
// whose size does not match its buffer.
func TestLibraryArtFallsBackToTheCover(t *testing.T) {
	withScreen(t, probeW, probeH)
	ClearTextCache()
	t.Cleanup(ClearTextCache)

	cover := filepath.Join(t.TempDir(), "B.png")
	writeSolidPNG(t, cover, 128, 128, color.RGBA{R: 235, G: 40, B: 40, A: 255})
	game := libraryGame{Path: "/roms/nes/B.nes", System: "NES", Title: "Bouncer", Art: cover, HasArt: true}
	rect := sdl.Rect{X: 40, Y: 40, W: 250, H: 250}
	isCoverRed := func(c sdl.Color) bool { return c.R > 190 && c.G < 90 && c.B < 90 }

	// 1. Another game's frame must never paint here.
	snapTestFrame(t, "/roms/nes/A.nes")
	f := renderLibraryArt(t, rect, game)
	if reds := countPixelsMatching(f, rect, isCoverRed); reds == 0 {
		t.Fatal("a frame belonging to a different game replaced this game's cover")
	}

	// 2. No frame at all.
	snapPreviewReset()
	f = renderLibraryArt(t, rect, game)
	if reds := countPixelsMatching(f, rect, isCoverRed); reds == 0 {
		t.Fatal("the cover disappeared with no snap frame to show")
	}

	// 3. A frame whose buffer does not match its declared size is refused rather
	// than uploaded as garbage.
	snapTestFrame(t, game.Path)
	snapState.mu.Lock()
	snapState.frame = snapState.frame[:16]
	snapState.mu.Unlock()
	f = renderLibraryArt(t, rect, game)
	if reds := countPixelsMatching(f, rect, isCoverRed); reds == 0 {
		t.Fatal("a truncated frame was drawn instead of the cover")
	}
}
