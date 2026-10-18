package main

// Frame-cost check for the cached keyboard layer. The software renderer makes
// the CPU cost of every draw call visible, which is exactly what the cache
// removes: the live path rasterises every cap each frame, the cached path
// copies one texture and draws a single cap.

import (
	"testing"
	"time"
)

func TestKeyboardCachedFrameIsCheaperThanFullRedraw(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, probeW, probeH)
	DestroyVKStaticCache()
	t.Cleanup(DestroyVKStaticCache)

	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	vkTarget = vkTargetChat
	chatInputActive = true
	vkLayer = vkLayerLetters
	keyboardUpper = false
	vkAnimStart = 0
	virtualKeyboardActive = true
	vkLayoutSig = ""
	vkEnsureLayout()

	renderer := newKeyboardTestRenderer(t)
	draw := func() {
		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.Clear()
		renderKeyboard(renderer, cfg)
	}
	// Move the highlight like the d-pad does, so every frame is a distinct
	// frame rather than the same pixels over and over.
	frameCost := func(n int) time.Duration {
		start := time.Now()
		for i := 0; i < n; i++ {
			vkMoveFocus(1, 0)
			draw()
		}
		return time.Since(start) / time.Duration(n)
	}

	// Warm up so both paths start with the same text cache contents.
	draw()
	vkStaticBuilds = 0
	draw()
	if vkStaticBuilds != 0 {
		t.Fatalf("second frame rebuilt the cache %d time(s)", vkStaticBuilds)
	}
	cached := frameCost(20)

	// Force the pre-cache behaviour: repaint every cap each frame.
	DestroyVKStaticCache()
	vkStaticBroken = true
	if vkStaticTex != nil {
		t.Fatal("cache texture survived DestroyVKStaticCache")
	}
	live := frameCost(20)

	t.Logf("keyboard frame at %dx%d: cached=%v live=%v (%.1fx faster)",
		probeW, probeH, cached.Round(time.Microsecond), live.Round(time.Microsecond),
		float64(live)/float64(cached))
	if cached >= live {
		t.Fatalf("cached frame (%v) is not faster than the live redraw (%v)", cached, live)
	}
}
