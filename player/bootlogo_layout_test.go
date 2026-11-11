package main

import (
	"math"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// The Smart Pro's canvas is its panel, so these are the exact pixels the
// firmware's boot logo and the boot animation's first frame share.
func TestBootLogoPlaceSmartPro(t *testing.T) {
	l := bootLogoPlace(1280, 720, 0)
	if want := (sdl.Rect{X: 550, Y: 219, W: 180, H: 180}); l.Sprite != want {
		t.Fatalf("sprite = %+v, want %+v", l.Sprite, want)
	}
	if l.WordPx != 43 || l.WordLineH != 52 || l.WordTop != 421 {
		t.Fatalf("wordmark = %dpx line %d top %d, want 43px line 52 top 421", l.WordPx, l.WordLineH, l.WordTop)
	}
	if want := (sdl.Rect{X: 512, Y: 513, W: 256, H: 3}); l.Track != want {
		t.Fatalf("track = %+v, want %+v", l.Track, want)
	}
	if l.StatusY != 529 {
		t.Fatalf("status line at %d, want 529", l.StatusY)
	}
}

// The Brick draws a 960x720 canvas scaled onto its 1024x768 panel, while its
// boot logo is written at the panel's own size: both placements must meet on
// the panel within a pixel and a half, or the hand-over from the firmware's
// picture to the animation would jump.
func TestBootLogoPlaceBrickCanvasMatchesPanel(t *testing.T) {
	canvas := bootLogoPlace(960, 720, 0)
	panel := bootLogoPlace(1024, 768, 0)
	scale := 768.0 / 720.0
	near := func(name string, got int32, want float64) {
		t.Helper()
		if math.Abs(float64(got)-want) > 1.5 {
			t.Errorf("%s: panel %d, canvas scaled %.1f", name, got, want)
		}
	}
	near("sprite x", panel.Sprite.X, float64(canvas.Sprite.X)*scale)
	near("sprite y", panel.Sprite.Y, float64(canvas.Sprite.Y)*scale)
	near("sprite side", panel.Sprite.W, float64(canvas.Sprite.W)*scale)
	near("wordmark top", panel.WordTop, float64(canvas.WordTop)*scale)
}

func TestBootLogoPlaceMeasuredLineAndDegenerate(t *testing.T) {
	est := bootLogoPlace(1280, 720, 0)
	measured := bootLogoPlace(1280, 720, 60)
	if measured.WordLineH != 60 {
		t.Fatalf("a measured line box must be used as given, got %d", measured.WordLineH)
	}
	// A taller line box pushes the group up by half the difference and the
	// track down by the other half.
	if measured.Sprite.Y >= est.Sprite.Y || measured.Track.Y <= est.Track.Y {
		t.Fatalf("taller line box: sprite %d→%d, track %d→%d", est.Sprite.Y, measured.Sprite.Y, est.Track.Y, measured.Track.Y)
	}
	if got := bootLogoPlace(0, 720, 0); got != (bootLogoLayout{}) {
		t.Fatalf("zero width must lay nothing out, got %+v", got)
	}
	if got := bootLogoPlace(1280, -1, 0); got != (bootLogoLayout{}) {
		t.Fatalf("negative height must lay nothing out, got %+v", got)
	}
	// A screen narrower than the artwork clamps it instead of overflowing.
	tiny := bootLogoPlace(100, 720, 0)
	if tiny.Sprite.W > 100 || tiny.Sprite.X < 0 {
		t.Fatalf("narrow screen: sprite %+v", tiny.Sprite)
	}
}

func TestBootLogoPaneInsideTile(t *testing.T) {
	sprite := sdl.Rect{X: 550, Y: 219, W: 180, H: 180}
	tile, tileR := bootLogoTileRect(sprite)
	pane, paneR := bootLogoPaneRect(sprite)
	if tile.W <= 0 || pane.W <= 0 || pane.W >= tile.W {
		t.Fatalf("tile %+v pane %+v", tile, pane)
	}
	if pane.X <= tile.X || pane.Y <= tile.Y || pane.X+pane.W >= tile.X+tile.W {
		t.Fatalf("pane %+v must sit inside tile %+v", pane, tile)
	}
	// mklogo.py: the pane is 67% of the artwork's side, its corner 23% of that.
	if math.Abs(float64(pane.W)-0.67*180) > 0.01 || math.Abs(paneR-0.23*0.67*180) > 0.01 {
		t.Fatalf("pane side %.2f radius %.2f", pane.W, paneR)
	}
	if math.Abs(tileR-0.25*float64(tile.W)) > 0.01 {
		t.Fatalf("tile radius %.2f for side %.2f", tileR, tile.W)
	}
}
