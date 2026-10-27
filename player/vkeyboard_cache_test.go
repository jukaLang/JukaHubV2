package main

// The on-screen keyboard caches its static key layer. These tests make sure the
// cache is reused while moving focus and that only the focused cap is redrawn
// on top of it.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func newKeyboardTestRenderer(t *testing.T) *sdl.Renderer {
	t.Helper()
	_ = newProbeFrame(t) // ensures SDL video + TTF and the hidden test window
	r, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	// Text and keyboard textures belong to this renderer: drop them while the
	// renderer is still alive so later tests do not draw with stale pointers.
	t.Cleanup(func() {
		DestroyVKStaticCache()
		destroyTestRenderer(r)
	})
	return r
}

func TestKeyboardStaticLayerIsCachedAcrossFocusMoves(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, probeW, probeH)
	DestroyVKStaticCache()
	t.Cleanup(DestroyVKStaticCache)

	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	vkTarget = vkTargetScene
	virtualKeyboardActive = true
	vkAnimStart = 0
	vkEnsureLayout()

	renderer := newKeyboardTestRenderer(t)
	draw := func() {
		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.Clear()
		renderKeyboard(renderer, cfg)
	}

	vkStaticBuilds = 0
	draw()
	if vkStaticTex == nil {
		t.Fatal("static key layer was not cached")
	}
	first := vkStaticTex
	if vkStaticBuilds != 1 {
		t.Fatalf("built %d times on first frame, want 1", vkStaticBuilds)
	}

	// Moving the highlight must not rebuild the cached layer.
	vkMoveFocus(1, 0)
	draw()
	if vkStaticTex != first {
		t.Fatal("focus move rebuilt the static key layer")
	}
	if vkStaticBuilds != 1 {
		t.Fatalf("focus move caused %d builds, want 1", vkStaticBuilds)
	}

	// A new layer (symbols) has different caps, so it must rebuild once.
	vkLayer = vkLayerSymbols
	draw()
	if vkStaticBuilds != 2 {
		t.Fatalf("layer switch caused %d builds, want 2", vkStaticBuilds)
	}
}

// The panel body, header and footer are part of the cached layer, so a frame
// copied from the cache must still show them (and move them with the slide-in
// animation).
func TestKeyboardCachedLayerKeepsPanelChrome(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, probeW, probeH)
	DestroyVKStaticCache()
	t.Cleanup(DestroyVKStaticCache)

	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	vkTarget = vkTargetScene
	virtualKeyboardActive = true
	vkAnimStart = 0
	vkEnsureLayout()

	renderer := newKeyboardTestRenderer(t)
	frame := newProbeFrame(t)
	draw := func() {
		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.Clear()
		renderKeyboard(renderer, cfg)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
			unsafe.Pointer(&frame.buf[0]), int(probeW)*4); err != nil {
			t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
		}
	}
	// Ink differs from the panel surface, whether the theme is light or dark.
	inkPixels := func(x0, y0, x1, y1 int32) int {
		n := 0
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				if !closeColor(frame.at(x, y), ColorSurfacePanel, 24) {
					n++
				}
			}
		}
		return n
	}

	draw()
	if got := frame.at(vkPanelRect.X+8, vkPanelRect.Y+8); !closeColor(got, ColorSurfacePanel, 16) {
		t.Fatalf("cached panel pixel %v is not the panel surface %v", got, ColorSurfacePanel)
	}
	if n := inkPixels(vkPanelRect.X+12, vkPanelRect.Y+8, vkCloseRect.X-12, vkFieldRect.Y-6); n < 10 {
		t.Fatalf("header text missing from the cached layer (ink pixels=%d)", n)
	}
	if n := inkPixels(vkPanelRect.X+12, vkPanelRect.Y+vkPanelRect.H-32, vkPanelRect.X+vkPanelRect.W-12, vkPanelRect.Y+vkPanelRect.H-6); n < 10 {
		t.Fatalf("footer hints missing from the cached layer (ink pixels=%d)", n)
	}

	// During the slide-in the whole cached layer moves, not just the keys.
	// Zero is the "no animation" sentinel in vkAnimOffset, and a test that
	// starts within the first millisecond of SDL's clock reads exactly 0 —
	// which used to make this check skip instead of testing the slide.
	vkAnimStart = sdl.GetTicks64()
	if vkAnimStart == 0 {
		vkAnimStart = 1
	}
	off := vkAnimOffset()
	if off <= 0 {
		t.Fatalf("vkAnimOffset is %d right after starting the open animation; the slide has no frame to draw", off)
	}
	draw()
	if got := frame.at(vkPanelRect.X+8, vkPanelRect.Y+8+off); !closeColor(got, ColorSurfacePanel, 16) {
		t.Fatalf("moved panel pixel %v is not the panel surface %v", got, ColorSurfacePanel)
	}
}

// The cached layer must look like a live frame. Compare it pixel-for-pixel
// with the fallback path; only the soft alpha edges (shadow, rounded corners,
// antialiased glyphs) may differ, because translucent pixels are composited
// into the cache texture and then onto the scene.
func TestKeyboardCachedLayerMatchesLiveRender(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, probeW, probeH)
	DestroyVKStaticCache()
	t.Cleanup(DestroyVKStaticCache)

	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	vkTarget = vkTargetScene
	virtualKeyboardActive = true
	vkAnimStart = 0
	vkEnsureLayout()

	renderer := newKeyboardTestRenderer(t)
	frame := newProbeFrame(t)
	live := make([]byte, probeW*probeH*4)
	draw := func(dst []byte) {
		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.Clear()
		renderKeyboard(renderer, cfg)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
			unsafe.Pointer(&dst[0]), int(probeW)*4); err != nil {
			t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
		}
	}

	draw(frame.buf) // cached path
	DestroyVKStaticCache()
	vkStaticBroken = true
	draw(live) // live redraw path

	pixels := len(frame.buf) / 4
	var sum int64
	big, maxDiff := 0, 0
	for i := 0; i < len(frame.buf); i += 4 {
		d := 0
		for k := 0; k < 4; k++ {
			v := int(frame.buf[i+k]) - int(live[i+k])
			if v < 0 {
				v = -v
			}
			if v > d {
				d = v
			}
			sum += int64(v)
		}
		if d > 12 {
			big++
		}
		if d > maxDiff {
			maxDiff = d
		}
	}
	mean := float64(sum) / float64(len(frame.buf))
	if mean > 1.0 {
		t.Fatalf("cached frame differs from live rendering: mean channel diff %.3f", mean)
	}
	if big > pixels/500 {
		t.Fatalf("%d of %d pixels differ noticeably from the live render", big, pixels)
	}
	if maxDiff > 80 {
		t.Fatalf("cached frame deviates by %d on a channel", maxDiff)
	}
}

func TestKeyboardFocusDrawsOverCachedLayer(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, probeW, probeH)
	DestroyVKStaticCache()
	t.Cleanup(DestroyVKStaticCache)

	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	vkTarget = vkTargetScene
	virtualKeyboardActive = true
	vkAnimStart = 0
	vkLayer = vkLayerLetters
	keyboardUpper = false
	vkEnsureLayout()

	renderer := newKeyboardTestRenderer(t)
	frame := newProbeFrame(t)
	highlight := vkFocusColor()

	renderAt := func(row, col int) {
		keyboardPosY, keyboardPosX = row, col
		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.Clear()
		renderKeyboard(renderer, cfg)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
			unsafe.Pointer(&frame.buf[0]), int(probeW)*4); err != nil {
			t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
		}
	}

	qRow, qCol := rowIndex(keyboard, "q")
	wRow, wCol := rowIndex(keyboard, "w")
	if qRow < 0 || wRow < 0 {
		t.Fatal("q/w keys missing from the letters layer")
	}
	qRect, wRect := keyRectFor(qRow, qCol), keyRectFor(wRow, wCol)
	if qRect == nil || wRect == nil {
		t.Fatal("key rects missing")
	}
	// Sample inside the cap but away from the centred glyph, the sheen and the
	// rounded corners (which are composited and therefore slightly darker).
	sample := func(r *sdl.Rect) sdl.Color { return frame.at(r.X+12, r.Y+r.H/2) }

	renderAt(qRow, qCol)
	if got := sample(qRect); !closeColor(got, highlight, 4) {
		t.Fatalf("focused key corner = %v, want highlight %v", got, highlight)
	}
	if got := sample(wRect); !closeColor(got, ColorIconSurface, 4) {
		t.Fatalf("unfocused key corner = %v, want surface %v", got, ColorIconSurface)
	}

	// Move the cursor: the old key must return to its surface colour and the
	// new key must be highlighted, all from the cached layer + focused key.
	renderAt(wRow, wCol)
	if got := sample(qRect); !closeColor(got, ColorIconSurface, 4) {
		t.Fatalf("old focused key still highlighted: %v", got)
	}
	if got := sample(wRect); !closeColor(got, highlight, 4) {
		t.Fatalf("new focused key = %v, want highlight %v", got, highlight)
	}
}
