package main

// Render and navigation budgets for the handheld's real 1280x720 target.
//
// The other benchmark files measure the pieces (variable substitution, cache
// lookups, filter passes). These measure the frame: clearing the screen and
// drawing the whole home layout, and the cost of one D-pad step. Change a
// layout or a text path, run
//
//	go test -run '^$' -bench 'BenchmarkHome' -benchmem
//
// and compare against the numbers recorded in MODERNIZATION.md (Phase 6).
//
// The benchmarks run headless with SDL's dummy video driver and the software
// renderer, so the absolute numbers are a desktop proxy: they are useful for
// spotting regressions, not as a measurement of the device.

import (
	"os"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	benchScreenW = int32(1280)
	benchScreenH = int32(720)
)

// benchmarkHomeScreen initialises an offscreen 1280x720 target and points the
// home globals at it, the way the handheld sees them.
func benchmarkHomeScreen(b *testing.B) (*sdl.Renderer, *Config) {
	b.Helper()
	os.Setenv("SDL_VIDEODRIVER", "dummy")
	os.Setenv("SDL_AUDIODRIVER", "dummy")
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		b.Skipf("SDL video unavailable: %v", err)
	}
	if err := ttf.Init(); err != nil {
		b.Skipf("SDL_ttf unavailable: %v", err)
	}
	win, err := sdl.CreateWindow("render-bench", 0, 0, benchScreenW, benchScreenH, sdl.WINDOW_HIDDEN)
	if err != nil {
		b.Skipf("hidden window unavailable: %v", err)
	}
	renderer, err := sdl.CreateRenderer(win, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		win.Destroy()
		b.Skipf("software renderer unavailable: %v", err)
	}

	prevIndex, prevLayout, prevActive := currentSceneIndex, homeLayout, homeLayoutActive
	prevW, prevH := screenWidth, screenHeight
	b.Cleanup(func() {
		destroyTestRenderer(renderer)
		win.Destroy()
		currentSceneIndex, homeLayout, homeLayoutActive = prevIndex, prevLayout, prevActive
		screenWidth, screenHeight = prevW, prevH
	})

	screenWidth, screenHeight = benchScreenW, benchScreenH
	currentSceneIndex = 0
	homeLayout = NewHomeLayout(benchScreenW, benchScreenH)
	homeLayoutActive = true
	return renderer, &Config{Scenes: []SceneConfig{{Name: "Main", Layout: "home"}}}
}

// BenchmarkHomeFrame720p is the steady-state cost of one home frame: clear the
// screen, then draw the header, cards and footer with warm text caches. This is
// the work an idle frame repeats at the capped cadence.
func BenchmarkHomeFrame720p(b *testing.B) {
	renderer, config := benchmarkHomeScreen(b)
	// Draw once so the background texture, home label cache and text textures
	// are allocated: the frame worth measuring is the cached one.
	renderHomeModern(renderer, config)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = renderer.SetDrawColor(9, 14, 25, 255)
		_ = renderer.Clear()
		renderHomeModern(renderer, config)
	}
}

// BenchmarkHomeFrame720pColdText measures the same frame without any cached
// textures, which is what a theme switch or the first frame after a cache trim
// pays: every label is rasterised again.
func BenchmarkHomeFrame720pColdText(b *testing.B) {
	renderer, config := benchmarkHomeScreen(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ClearTextCache()
		clearHomeTextCache()
		_ = renderer.SetDrawColor(9, 14, 25, 255)
		_ = renderer.Clear()
		renderHomeModern(renderer, config)
	}
}

// BenchmarkHomeNavigation720p is one D-pad step on the home grid: the focus
// move, its animation start and the pressed-state bookkeeping. A held direction
// repeats this every ~120 ms, so it must stay far below a frame.
func BenchmarkHomeNavigation720p(b *testing.B) {
	_, config := benchmarkHomeScreen(b)
	if !homeLayout.Focus.Set(HomeFocusMedia) {
		b.Skip("the 720p layout has no Media card")
	}
	right := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_RIGHT}}
	left := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_LEFT}}

	// A step that is swallowed would time an empty call, so prove the first one
	// really moves focus before the timer starts.
	start := homeLayout.Focus.Current()
	if !handleHomeKey(right, config) || homeLayout.Focus.Current() == start {
		b.Skipf("the 720p layout does not move focus from %q", start)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e := right
		if i%2 == 1 {
			e = left
		}
		if !handleHomeKey(e, config) {
			b.Fatalf("home key handler ignored %v", e.Keysym.Sym)
		}
	}
}
