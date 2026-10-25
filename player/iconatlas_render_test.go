package main

// Offscreen checks for the icon atlas behind DrawHomeIcon. These run with the
// dummy video driver and the software renderer like the other probe suites, so
// they verify real pixels and the atlas' blit/bake bookkeeping without a GPU.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

var _ = unsafe.Pointer(nil)

// iconProbe creates a test renderer over the shared hidden probe window with a
// flat background on it.
func iconProbe(t *testing.T) (*sdl.Renderer, *probeFrame) {
	t.Helper()
	_ = newProbeFrame(t) // SDL video init + the hidden window
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
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
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	return captured
}

// clearIconFrame wipes the backbuffer between captures while keeping one
// renderer alive, so the atlas keeps its baked strip.
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

// disableIconAtlasForTest marks the renderer unsupported so every DrawHomeIcon
// takes the direct-primitive path. Restored by the caller.
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

// TestIconAtlasBakesOnceServesEveryLaterDraw verifies the core behaviour: the
// first frame bakes each glyph exactly once and every later draw — same scene
// or moved boxes, which is what a later frame looks like — is an atlas blit,
// and the shape cache's per-primitive Copy stream stops.
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
		t.Fatalf("first frame: direct paints = %d, want 0 (every glyph came from the atlas)", direct)
	}

	// A later frame of the same scene: same glyphs, moved boxes.
	before, _, _ := ShapeCacheStats()
	clearIconFrame(t, renderer, f)
	moved := make([]sdl.Point, len(origins))
	for i, o := range origins {
		moved[i] = sdl.Point{X: o.X + 4, Y: o.Y + 4}
	}
	drawIcons(renderer, names, moved, false)
	after, _, _ := ShapeCacheStats()

	copies2, bakes2, _ := IconAtlasStats()
	if bakes2 != bakes {
		t.Fatalf("second frame: bakes = %d, want %d (no rebakes)", bakes2, bakes)
	}
	if copies2 < copies+int64(len(names)) {
		t.Fatalf("second frame: copies = %d, want >= %d (every glyph a blit)", copies2, copies+int64(len(names)))
	}
	if grew := after - before; grew != 0 {
		t.Fatalf("second frame: shape-cache copies grew by %d, want 0 — the atlas must replace the per-primitive blit stream, not sit on top of it", grew)
	}
}

// TestIconAtlasIsRendererLocal checks the atlas is renderer-local: a second
// renderer gets its own bake pass (its strip belongs to it) once the first is
// destroyed, and the per-renderer counters restart with it.
func TestIconAtlasIsRendererLocal(t *testing.T) {
	rendererA, fA := iconProbe(t)
	drawIcons(rendererA, []string{"apps", "clock"}, []sdl.Point{{X: 200, Y: 200}, {X: 240, Y: 200}}, false)
	if _, bakes, _ := IconAtlasStats(); bakes != 2 {
		t.Fatalf("renderer A bakes = %d, want 2", bakes)
	}
	destroyTestRenderer(rendererA)
	_ = fA

	rendererB, _ := iconProbe(t)
	defer destroyTestRenderer(rendererB)
	drawIcons(rendererB, []string{"apps", "clock"}, []sdl.Point{{X: 200, Y: 200}, {X: 240, Y: 200}}, false)
	// Renderer A's strips died with it; renderer B bakes its own two slots.
	if _, bakes, direct := IconAtlasStats(); bakes != 2 || direct != 0 {
		t.Fatalf("renderer B: bakes = %d, direct = %d, want 2 and 0", bakes, direct)
	}
}

// TestIconAtlasInteriorPixelsMatchDirectPaint pins the atlas' pixel contract:
// where the direct painter produced an opaque interior, the atlas Copy must
// produce identical pixels, because the baked strip stores the primitives
// composited once over transparency like every other shape-cache bake.
func TestIconAtlasInteriorPixelsMatchDirectPaint(t *testing.T) {
	renderer, f := iconProbe(t)
	defer destroyTestRenderer(renderer)

	box := sdl.Rect{X: 300, Y: 300, W: 32, H: 32}
	// Capture A: through the atlas.
	DrawHomeIcon(renderer, "media", box, false)
	viaAtlas := iconCapture(t, renderer, f)

	// Capture B: the direct painter, with the strip Copy disabled.
	restore := disableIconAtlasForTest(renderer)
	clearIconFrame(t, renderer, f)
	DrawHomeIcon(renderer, "media", box, false)
	restore()
	viaDirect := iconCapture(t, renderer, f)

	// Every sample point sits in an opaque interior of the "media" glyph:
	// the play triangle body ((16,16) relative is inside it) and the
	// 3px frame strokes on its top and left edges.
	samples := [][2]int32{{10, 16}, {16, 16}, {18, 8}, {1, 5}, {5, 1}, {16, 1}}
	for _, d := range samples {
		px, py := box.X+d[0], box.Y+d[1]
		a, b := viaAtlas.at(px, py), viaDirect.at(px, py)
		if a.R != b.R || a.G != b.G || a.B != b.B || a.A != b.A {
			t.Fatalf("glyph interior at rel(%d,%d): atlas %v vs direct %v", d[0], d[1], a, b)
		}
		if a.A != 255 {
			t.Fatalf("glyph interior at rel(%d,%d): alpha %d, want fully opaque", d[0], d[1], a.A)
		}
	}
}

// TestIconAtlasNonCanonicalSizesPaintDirect verifies box sizes other than the
// canonical 32 still render — painted directly, not scaled — so callers like
// the launcher's 46px empty-state keep native glyph rendering.
func TestIconAtlasNonCanonicalSizesPaintDirect(t *testing.T) {
	renderer, f := iconProbe(t)
	defer destroyTestRenderer(renderer)

	copies0, bakes0, direct0 := IconAtlasStats()
	box := sdl.Rect{X: 100, Y: 100, W: 46, H: 46}
	DrawHomeIcon(renderer, "apps", box, false)
	copies, bakes, direct := IconAtlasStats()
	if bakes != bakes0 || copies != copies0 {
		t.Fatalf("46px draw touched the atlas (bakes %d→%d, copies %d→%d); it must paint at its own size", bakes0, bakes, copies0, copies)
	}
	if direct != direct0+1 {
		t.Fatalf("direct paints: %d→%d, want exactly one more for the off-size draw", direct0, direct)
	}

	frame := iconCapture(t, renderer, f)
	// The top-left apps square's interior. At 46px: cell = max(6, 46/3) = 15,
	// gap = max(5, 46/8) = 5 → total 35, startX = 100+(46-35)/2 = 105, so the
	// square spans 105..120 at 105..120 vertically; (110,110) is inside.
	got := frame.at(110, 110)
	if got.A == 0 || (got.R == f.bg.R && got.G == f.bg.G && got.B == f.bg.B) {
		t.Fatalf("46px glyph drew nothing at (110,110): %v", got)
	}
}

// TestIconAtlasUnknownNamesGetAFallbackSlot pins the safe-fallback guarantee:
// unknown icon names bake the ring glyph into the atlas exactly like known
// ones, so a config that invents an icon key still draws something and still
// costs one blit.
func TestIconAtlasUnknownNamesGetAFallbackSlot(t *testing.T) {
	renderer, f := iconProbe(t)
	defer destroyTestRenderer(renderer)

	names := []string{"definitely-not-an-icon", "YET ANOTHER"}
	drawIcons(renderer, names, iconTestOrigins(2), false)
	frame := iconCapture(t, renderer, f)

	// The fallback ring is the disc w/3 (int32: 10) minus its inner circle
	// w/6 (5), centred in the box: rel (10,12) sits √(6²+4²)≈7.2px from the
	// centre, inside the 5..10 band.
	for _, origin := range iconTestOrigins(2) {
		got := frame.at(origin.X+10, origin.Y+12)
		if got.A == 0 || (got.R == f.bg.R && got.G == f.bg.G && got.B == f.bg.B) {
			t.Fatalf("unknown icon drew nothing at (%d,%d)", origin.X+10, origin.Y+12)
		}
	}
	if _, _, direct := IconAtlasStats(); direct != 0 {
		t.Fatalf("direct paints = %d after unknown names, want 0; both must use atlas slots", direct)
	}
}

// TestIconAtlasClearRebakesAChangePins the lifecycle hook: after ClearIconAtlas
// (theme change — the baked palette is stale), the next frame re-bakes fresh
// slots and blits them; the counters restart from the clear.
func TestIconAtlasClearRebakes(t *testing.T) {
	renderer, _ := iconProbe(t)
	defer destroyTestRenderer(renderer)

	drawIcons(renderer, []string{"apps"}, []sdl.Point{{X: 600, Y: 500}}, false)
	ClearIconAtlas()
	// After the clear the counters are zero again; one draw bakes one slot
	// and blits it, never painting direct.
	if _, bakes, direct := IconAtlasStats(); bakes != 0 || direct != 0 {
		t.Fatalf("after ClearIconAtlas: bakes = %d, direct = %d, want both 0", bakes, direct)
	}
	drawIcons(renderer, []string{"apps"}, []sdl.Point{{X: 600, Y: 500}}, false)
	if _, bakes, direct := IconAtlasStats(); bakes != 1 || direct != 0 {
		t.Fatalf("after clear+redraw: bakes = %d, direct = %d, want 1 and 0", bakes, direct)
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

var _ = unsafe.Pointer(nil)
