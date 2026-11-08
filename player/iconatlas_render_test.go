package main

// Offscreen checks for the icon atlas behind DrawHomeIcon and iconDraw. These
// run with the dummy video driver and the software renderer like the other
// probe suites, so they verify real pixels and the atlas' copy/bake
// bookkeeping without a GPU.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// iconProbe creates a test renderer over the shared hidden probe window with a
// flat background on it.
func iconProbe(t *testing.T) (*sdl.Renderer, *probeFrame) {
	t.Helper()
	_ = newProbeFrame(t) // SDL video init + the hidden window
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	f := &probeFrame{buf: make([]byte, probeW*probeH*4), bg: sdl.Color{R: 9, G: 14, B: 25, A: 255}}
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	return renderer, f
}

// iconCapture reads the renderer's backbuffer into a fresh frame buffer.
func iconCapture(t *testing.T, renderer *sdl.Renderer, f *probeFrame) *probeFrame {
	t.Helper()
	captured := &probeFrame{buf: make([]byte, probeW*probeH*4), bg: f.bg}
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&captured.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
	return captured
}

// clearIconFrame wipes the backbuffer between captures while keeping one
// renderer alive, so the atlas keeps its baked texture.
func clearIconFrame(_ *testing.T, renderer *sdl.Renderer, f *probeFrame) {
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
}

// drawIcons paints each icon at a distinct 32px box so no draw overlaps
// another, and returns the boxes for pixel checks.
func drawIcons(renderer *sdl.Renderer, names []string, origins []sdl.Point, focused bool) []sdl.Rect {
	boxes := make([]sdl.Rect, 0, len(names))
	for i, name := range names {
		box := sdl.Rect{X: origins[i].X, Y: origins[i].Y, W: 32, H: 32}
		DrawHomeIcon(renderer, name, box, focused)
		boxes = append(boxes, box)
	}
	return boxes
}

// disableIconAtlasForTest marks the renderer unsupported so every draw takes
// the fallback painter. Restored by the caller.
func disableIconAtlasForTest(renderer *sdl.Renderer) func() {
	homeIconAtlas.mu.Lock()
	homeIconAtlas.unsupported[renderer] = true
	homeIconAtlas.mu.Unlock()
	return func() {
		homeIconAtlas.mu.Lock()
		delete(homeIconAtlas.unsupported, renderer)
		homeIconAtlas.mu.Unlock()
	}
}

// iconMask is the glyph's own coverage mask at a size, for comparing what the
// renderer drew against what the rasterizer produced.
func iconMask(name string, size int) []uint8 {
	var cv vecCanvas
	var path vecPath
	buf := make([]uint8, size*size)
	iconRasterize(name, size, buf, &cv, &path)
	return buf
}

// iconBlendDiff is the largest per-channel difference between the frame inside
// box and ink blended over bg by the glyph's mask.
func iconBlendDiff(f *probeFrame, box sdl.Rect, mask []uint8, ink sdl.Color) int {
	worst := 0
	for y := int32(0); y < box.H; y++ {
		for x := int32(0); x < box.W; x++ {
			a := int(mask[int(y*box.W+x)])
			got := f.at(box.X+x, box.Y+y)
			for _, ch := range [][2]int{
				{int(got.R), (int(ink.R)*a + int(f.bg.R)*(255-a)) / 255},
				{int(got.G), (int(ink.G)*a + int(f.bg.G)*(255-a)) / 255},
				{int(got.B), (int(ink.B)*a + int(f.bg.B)*(255-a)) / 255},
			} {
				d := ch[0] - ch[1]
				if d < 0 {
					d = -d
				}
				if d > worst {
					worst = d
				}
			}
		}
	}
	return worst
}

// TestIconAtlasBakesOnceServesEveryLaterDraw verifies the core behaviour: the
// first frame bakes each glyph exactly once and every later draw — same scene
// or moved boxes, which is what a later frame looks like — is an atlas copy,
// with no shape-cache traffic at all.
func TestIconAtlasBakesOnceServesEveryLaterDraw(t *testing.T) {
	renderer, f := iconProbe(t)
	defer destroyTestRenderer(renderer)

	names := []string{"media", "apps", "tools", "clock", "gamepad", "unknown-glyph"}
	origins := iconTestOrigins(len(names))
	drawIcons(renderer, names, origins, false)

	copies, bakes, direct := IconAtlasStats()
	if bakes != int64(len(names)) {
		t.Fatalf("first frame: bakes = %d, want %d (every glyph exactly once)", bakes, int64(len(names)))
	}
	if direct != 0 {
		t.Fatalf("first frame: fallback paints = %d, want 0 (every glyph came from the atlas)", direct)
	}

	// A later frame of the same scene: same glyphs, moved boxes, and the
	// focused ink, which is a colour mod rather than a second bake.
	before, _, _ := ShapeCacheStats()
	clearIconFrame(t, renderer, f)
	moved := make([]sdl.Point, len(origins))
	for i, o := range origins {
		moved[i] = sdl.Point{X: o.X + 4, Y: o.Y + 4}
	}
	drawIcons(renderer, names, moved, true)
	after, _, _ := ShapeCacheStats()

	copies2, bakes2, _ := IconAtlasStats()
	if bakes2 != bakes {
		t.Fatalf("second frame: bakes = %d, want %d (no rebakes, not even for the focused ink)", bakes2, bakes)
	}
	if copies2 < copies+int64(len(names)) {
		t.Fatalf("second frame: copies = %d, want >= %d (every glyph a copy)", copies2, copies+int64(len(names)))
	}
	if grew := after - before; grew != 0 {
		t.Fatalf("second frame: shape-cache copies grew by %d, want 0 — icons are one atlas copy each", grew)
	}
}

// TestIconAtlasIsRendererLocal checks the atlas is renderer-local: a second
// renderer gets its own bake pass (its texture belongs to it) once the first is
// destroyed, and the per-renderer counters restart with it.
func TestIconAtlasIsRendererLocal(t *testing.T) {
	rendererA, _ := iconProbe(t)
	drawIcons(rendererA, []string{"apps", "clock"}, []sdl.Point{{X: 200, Y: 200}, {X: 240, Y: 200}}, false)
	_, bakesA, _ := IconAtlasStats()
	// Destroyed before asserting: a renderer left on the shared probe window
	// would make every later probe in the package fail to create its own.
	destroyTestRenderer(rendererA)
	if bakesA != 2 {
		t.Fatalf("renderer A bakes = %d, want 2", bakesA)
	}

	rendererB, _ := iconProbe(t)
	defer destroyTestRenderer(rendererB)
	drawIcons(rendererB, []string{"apps", "clock"}, []sdl.Point{{X: 200, Y: 200}, {X: 240, Y: 200}}, false)
	// Renderer A's texture died with it; renderer B bakes its own two slots.
	if _, bakes, direct := IconAtlasStats(); bakes != 2 || direct != 0 {
		t.Fatalf("renderer B: bakes = %d, direct = %d, want 2 and 0", bakes, direct)
	}
}

// TestIconAtlasPixelsAreTheVectorGlyph pins the pixel contract: an atlas copy
// is the rasterizer's mask, blended in the requested ink, at the requested
// size — for both the theme ink and the focused dark ink — and the fallback
// painter draws the same picture to within its coverage quantization.
func TestIconAtlasPixelsAreTheVectorGlyph(t *testing.T) {
	renderer, f := iconProbe(t)
	defer destroyTestRenderer(renderer)

	for _, tc := range []struct {
		name    string
		size    int32
		focused bool
	}{
		{"media", 32, false},
		{"settings", 24, false},
		{"emu-n64", 46, false},
		{"folder", 18, true},
	} {
		box := sdl.Rect{X: 300, Y: 300, W: tc.size, H: tc.size}
		ink := iconHomeInk(tc.focused)
		mask := iconMask(tc.name, int(tc.size))

		clearIconFrame(t, renderer, f)
		DrawHomeIcon(renderer, tc.name, box, tc.focused)
		if d := iconBlendDiff(iconCapture(t, renderer, f), box, mask, ink); d > 2 {
			t.Errorf("%s at %dpx via the atlas: off the vector mask by %d levels", tc.name, tc.size, d)
		}

		restore := disableIconAtlasForTest(renderer)
		clearIconFrame(t, renderer, f)
		DrawHomeIcon(renderer, tc.name, box, tc.focused)
		restore()
		if d := iconBlendDiff(iconCapture(t, renderer, f), box, mask, ink); d > 8 {
			t.Errorf("%s at %dpx via the fallback: off the vector mask by %d levels", tc.name, tc.size, d)
		}
	}
}

// TestIconAtlasBakesEachSizeNatively verifies a glyph drawn at a new size is
// baked at that size (never a scaled copy of another) and that the size then
// stays cached.
func TestIconAtlasBakesEachSizeNatively(t *testing.T) {
	renderer, _ := iconProbe(t)
	defer destroyTestRenderer(renderer)

	draw := func(size int32) {
		DrawHomeIcon(renderer, "apps", sdl.Rect{X: 100, Y: 100, W: size, H: size}, false)
	}
	draw(46)
	draw(46)
	draw(32)
	copies, bakes, direct := IconAtlasStats()
	if bakes != 2 || copies != 1 || direct != 0 {
		t.Fatalf("46, 46, 32: bakes = %d, copies = %d, direct = %d, want 2, 1, 0", bakes, copies, direct)
	}
	// A non-square box draws the largest centred square, from the same bake.
	DrawHomeIcon(renderer, "apps", sdl.Rect{X: 100, Y: 100, W: 60, H: 32}, false)
	if _, bakes2, _ := IconAtlasStats(); bakes2 != bakes {
		t.Fatalf("a 60x32 box rebaked (%d → %d); it should reuse the 32px glyph", bakes, bakes2)
	}
}

// TestIconAtlasUnknownNamesGetTheFallbackGlyph pins the safe-fallback
// guarantee: unknown icon names draw the placeholder glyph from the atlas —
// one shared bake — so a config that invents an icon key still draws
// something and still costs one copy.
func TestIconAtlasUnknownNamesGetTheFallbackGlyph(t *testing.T) {
	renderer, f := iconProbe(t)
	defer destroyTestRenderer(renderer)

	names := []string{"definitely-not-an-icon", "YET ANOTHER"}
	boxes := drawIcons(renderer, names, iconTestOrigins(2), false)
	frame := iconCapture(t, renderer, f)

	mask := iconMask(iconFallbackName, 32)
	for _, box := range boxes {
		if d := iconBlendDiff(frame, box, mask, ColorTextPrimary()); d > 2 {
			t.Fatalf("unknown icon at %+v is not the placeholder glyph (off by %d levels)", box, d)
		}
	}
	if _, bakes, direct := IconAtlasStats(); bakes != 1 || direct != 0 {
		t.Fatalf("bakes = %d, direct = %d after two unknown names, want one shared bake and no fallback paints", bakes, direct)
	}
}

// TestIconAtlasClearRebakes pins the lifecycle hook: after ClearIconAtlas
// (theme change, memory pressure) the next frame re-bakes fresh slots and
// copies them; the counters restart from the clear.
func TestIconAtlasClearRebakes(t *testing.T) {
	renderer, _ := iconProbe(t)
	defer destroyTestRenderer(renderer)

	drawIcons(renderer, []string{"apps"}, []sdl.Point{{X: 600, Y: 500}}, false)
	ClearIconAtlas()
	if _, bakes, direct := IconAtlasStats(); bakes != 0 || direct != 0 {
		t.Fatalf("after ClearIconAtlas: bakes = %d, direct = %d, want both 0", bakes, direct)
	}
	drawIcons(renderer, []string{"apps"}, []sdl.Point{{X: 600, Y: 500}}, false)
	if _, bakes, direct := IconAtlasStats(); bakes != 1 || direct != 0 {
		t.Fatalf("after clear+redraw: bakes = %d, direct = %d, want 1 and 0", bakes, direct)
	}
}

// TestIconAtlasFillsAndGrows bakes far more glyph sizes than the first texture
// holds: the atlas must grow (or restart) instead of failing, and every draw
// must still come from it.
func TestIconAtlasFillsAndGrows(t *testing.T) {
	renderer, _ := iconProbe(t)
	defer destroyTestRenderer(renderer)

	names := iconLibGet().order
	drawn := 0
	for size := int32(40); size <= 72; size += 4 {
		for i, name := range names {
			if i >= 60 {
				break
			}
			DrawHomeIcon(renderer, name, sdl.Rect{X: 10, Y: 10, W: size, H: size}, false)
			drawn++
		}
	}
	if _, _, direct := IconAtlasStats(); direct != 0 {
		t.Fatalf("%d of %d draws fell back to the run painter; a full atlas must grow or restart", direct, drawn)
	}
	homeIconAtlas.mu.Lock()
	side := homeIconAtlas.side
	homeIconAtlas.mu.Unlock()
	if side != iconAtlasMaxSide {
		t.Fatalf("atlas side = %d after %d large glyphs, want it grown to %d", side, drawn, iconAtlasMaxSide)
	}
}

// iconTestOrigins lays out i boxes at a 48px pitch in the top-left of the
// frame so each 32px box sits alone on a plain background.
func iconTestOrigins(n int) []sdl.Point {
	origins := make([]sdl.Point, 0, n)
	for i := 0; i < n; i++ {
		col := int32(i % 8)
		row := int32(i / 8)
		origins = append(origins, sdl.Point{X: 64 + col*48, Y: 96 + row*48})
	}
	return origins
}
