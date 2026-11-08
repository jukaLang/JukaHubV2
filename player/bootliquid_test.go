package main

// bootliquid_test.go — the liquid glass's pre-rendered layers, checked as
// pixels: no SDL needed for any of these.

import (
	"math"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func bootAlphaAt(pix []byte, w, x, y int) int { return int(pix[(y*w+x)*4+3]) }

// A blob is soft light: transparent along the whole border (a scaled copy must
// never show a square edge) and bright in the middle.
func TestBootLiquidBlobIsSoftWithATransparentBorder(t *testing.T) {
	const size = bootBlobTexSize
	for shape := 0; shape < 2; shape++ {
		pix := bootLiquidBlobPixels(size, shape)
		if len(pix) != size*size*4 {
			t.Fatalf("shape %d: %d bytes", shape, len(pix))
		}
		for i := 0; i < size; i++ {
			for _, p := range [4][2]int{{i, 0}, {i, size - 1}, {0, i}, {size - 1, i}} {
				if a := bootAlphaAt(pix, size, p[0], p[1]); a > 8 {
					t.Fatalf("shape %d: border pixel %v has alpha %d", shape, p, a)
				}
			}
		}
		if a := bootAlphaAt(pix, size, size/2, size/2); a < 200 {
			t.Fatalf("shape %d: centre alpha %d, want a bright core", shape, a)
		}
	}
}

// The rounded-rectangle distance field is negative inside, positive outside,
// and follows the corner radius.
func TestBootRoundRectSDF(t *testing.T) {
	for _, tc := range []struct {
		px, py, want float64
	}{
		{0, 0, -50},     // centre of a 200x100 rect: 50 from the long edges
		{100, 0, 0},     // on the right edge
		{110, 0, 10},    // outside
		{100, 50, 8.28}, // the corner point is outside the rounded corner
	} {
		got := bootRoundRectSDF(tc.px, tc.py, 100, 50, 20)
		if math.Abs(got-tc.want) > 0.05 {
			t.Errorf("sdf(%v,%v) = %.2f, want %.2f", tc.px, tc.py, got, tc.want)
		}
	}
}

// The sheet's corners are cut round in both layers, its body is mostly opaque
// frost (so rows stay readable over moving light), and its edge layer is lit
// at the top lip and nearly clear in the middle.
func TestBootLiquidSheetLayers(t *testing.T) {
	const w, h = 240, 160
	navy := sdl.Color{R: 12, G: 18, B: 34, A: 255}
	white := sdl.Color{R: 255, G: 255, B: 255, A: 255}
	cyan := sdl.Color{R: 85, G: 216, B: 255, A: 255}
	body, edge := bootLiquidSheetPixels(w, h, 24, navy, white, cyan, false)
	if len(body) != w*h*4 || len(edge) != w*h*4 {
		t.Fatal("sheet layers have the wrong size")
	}
	if bootAlphaAt(body, w, 0, 0) != 0 || bootAlphaAt(edge, w, 0, 0) != 0 {
		t.Fatal("the corner pixel is not cut away")
	}
	if a := bootAlphaAt(body, w, w/2, h/2); a < 180 || a > 230 {
		t.Fatalf("body alpha %d in the middle, want frosted (mostly opaque)", a)
	}
	lip := bootAlphaAt(edge, w, w/2, 1)
	middle := bootAlphaAt(edge, w, w/2, h/2)
	if lip < 60 || middle > 20 || lip <= middle {
		t.Fatalf("edge layer: lip %d, middle %d — want a lit top edge over clear glass", lip, middle)
	}

	// A light theme keeps the same shape with a lighter frost.
	lightBody, _ := bootLiquidSheetPixels(w, h, 24, white, navy, cyan, true)
	if lightBody[(h/2*w+w/2)*4] < body[(h/2*w+w/2)*4] {
		t.Fatal("the light theme's frost is darker than the dark theme's")
	}
}

// The wordmark's two layers line up: the sharp glyphs sit pad pixels in, and
// the out-of-focus copy spreads light past them.
func TestBootLiquidWordPixels(t *testing.T) {
	const w, h, pad = 20, 10, 6
	alpha := make([]uint8, w*h)
	for y := 3; y < 7; y++ {
		for x := 8; x < 12; x++ {
			alpha[y*w+x] = 255
		}
	}
	sharp, blurred, ow, oh := bootLiquidWordPixels(alpha, w, h, pad)
	if ow != w+2*pad || oh != h+2*pad {
		t.Fatalf("size %dx%d, want %dx%d", ow, oh, w+2*pad, h+2*pad)
	}
	if bootAlphaAt(sharp, ow, 10+pad, 5+pad) != 255 {
		t.Fatal("the sharp layer lost the glyph")
	}
	outside := [2]int{8 + pad - 2, 5 + pad}
	if bootAlphaAt(sharp, ow, outside[0], outside[1]) != 0 {
		t.Fatal("the sharp layer bled outside the glyph")
	}
	if bootAlphaAt(blurred, ow, outside[0], outside[1]) == 0 {
		t.Fatal("the blurred layer did not spread past the glyph")
	}
	if _, _, ow, _ := bootLiquidWordPixels(alpha, 0, h, pad); ow != 0 {
		t.Fatal("an empty glyph box produced a texture")
	}
}

// The box blur keeps a flat field flat and spreads a spike evenly.
func TestBootBoxBlur(t *testing.T) {
	const w, h = 9, 5
	flat := make([]float64, w*h)
	for i := range flat {
		flat[i] = 0.5
	}
	out := make([]float64, w*h)
	bootBoxBlur(flat, out, w, h, 2, true)
	for i, v := range out {
		if math.Abs(v-0.5) > 1e-9 {
			t.Fatalf("flat field changed at %d: %v", i, v)
		}
	}
	spike := make([]float64, w*h)
	spike[2*w+4] = 1
	bootBoxBlur(spike, out, w, h, 1, true)
	for x := 3; x <= 5; x++ {
		if math.Abs(out[2*w+x]-1.0/3) > 1e-9 {
			t.Fatalf("spike spread %v at x=%d, want 1/3", out[2*w+x], x)
		}
	}
	if out[2*w+2] != 0 || out[1*w+4] != 0 {
		t.Fatal("a horizontal blur leaked outside its row window")
	}
}

// The caustic net, the sheen and the vignette are what they claim to be.
func TestBootLiquidPatterns(t *testing.T) {
	caustic := bootLiquidCausticPixels(64)
	lit := 0
	for i := 3; i < len(caustic); i += 4 {
		if caustic[i] > 128 {
			lit++
		}
	}
	if lit == 0 || lit > 64*64/2 {
		t.Fatalf("caustic net has %d bright pixels: want thin lines, not a fill or nothing", lit)
	}

	const sw, sh = bootSheenTexW, bootSheenTexH
	sheen := bootLiquidSheenPixels(sw, sh)
	if bootAlphaAt(sheen, sw, sw/2, sh/2) <= bootAlphaAt(sheen, sw, 2, sh/2) {
		t.Fatal("the sheen band is not brightest at its centre line")
	}
	if bootAlphaAt(sheen, sw, sw/2, 0) >= bootAlphaAt(sheen, sw, sw/2, sh/2) {
		t.Fatal("the sheen does not fade toward its ends")
	}

	const vw, vh = bootVignetteTexW, bootVignetteTexH
	vignette := bootLiquidVignettePixels(vw, vh)
	if bootAlphaAt(vignette, vw, vw/2, vh/2) != 0 {
		t.Fatal("the vignette darkens the centre")
	}
	if bootAlphaAt(vignette, vw, 0, 0) < 150 {
		t.Fatal("the vignette leaves the corners light")
	}
}

// The inner clip never reaches past a rounded corner.
func TestBootLiquidInnerClip(t *testing.T) {
	sheet := sdl.Rect{X: 100, Y: 50, W: 400, H: 300}
	clip := bootLiquidInnerClip(sheet, 28)
	// The clip's corner must lie inside the rounded rectangle.
	cx := float64(clip.X) - float64(sheet.X) - float64(sheet.W)/2
	cy := float64(clip.Y) - float64(sheet.Y) - float64(sheet.H)/2
	if d := bootRoundRectSDF(cx, cy, float64(sheet.W)/2, float64(sheet.H)/2, 28); d > 0 {
		t.Fatalf("the clip corner is %.2f px outside the rounded sheet", d)
	}
	if clip.W <= sheet.W-40 || clip.H <= sheet.H-40 {
		t.Fatalf("clip %+v gives up too much of the sheet", clip)
	}
}

// Reduced motion keeps the blobs where they rest; full motion moves them.
func TestBootBlobPlaceRespectsReducedMotion(t *testing.T) {
	x0, y0, d0, a0 := bootBlobPlace(0, 0, false, 1280, 720)
	x1, y1, d1, a1 := bootBlobPlace(0, 7.5, false, 1280, 720)
	if x0 != x1 || y0 != y1 || d0 != d1 || a0 != a1 {
		t.Fatal("a blob moved with motion off")
	}
	mx, my, _, _ := bootBlobPlace(0, 7.5, true, 1280, 720)
	if mx == x0 && my == y0 {
		t.Fatal("a blob did not move with motion on")
	}
	if px, py := bootLiquidParallax(3, false); px != 0 || py != 0 {
		t.Fatal("parallax with motion off")
	}
}
