package main

// Offscreen render checks for the goodbye exit sequence. These reuse the
// shared dummy-driver probe from installbar_render_test.go: the animation is
// pure SDL primitives, so real pixels can be captured without a window.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// renderGoodbyeFrame draws one goodbye frame at the given timeline offset and
// returns the captured pixels.
func renderGoodbyeFrame(t *testing.T, elapsed float64) *probeFrame {
	t.Helper()
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()

	// Deterministic field: rebuild the state and drive it with a fixed tick
	// instead of wall time so the frame is reproducible.
	initGoodbye()
	goodbye.inited = true
	tick := goodbye.startTick + uint64(elapsed)
	renderGoodbye(renderer, &Config{}, tick)

	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
	return f
}

// countNonBG counts pixels that differ from the flat background, so the test
// proves the animation actually paints the screen.
func (f *probeFrame) countNonBG() int {
	n := 0
	for y := int32(0); y < probeH; y += 4 {
		for x := int32(0); x < probeW; x += 4 {
			if !f.isBG(x, y) {
				n++
			}
		}
	}
	return n
}

// The goodbye sequence must actually draw at every major timeline phase.
func TestGoodbyeRendersAtEveryPhase(t *testing.T) {
	phases := []struct {
		name    string
		elapsed float64
	}{
		{"ring-in", 800},
		{"wordmark", 1700},
		{"shine", 2400},
		{"collapse", 3000},
		{"fade-out", 3350},
	}
	for _, ph := range phases {
		f := renderGoodbyeFrame(t, ph.elapsed)
		if got := f.countNonBG(); got < 200 {
			t.Errorf("%s (t=%.0fms): animation drew almost nothing (%d lit samples)", ph.name, ph.elapsed, got)
		}
	}
}

// The wordmark band must be lit during the letter reveal, and the full
// sequence must terminate (renderGoodbye returns false after the duration).
func TestGoodbyeWordmarkAndCompletion(t *testing.T) {
	f := renderGoodbyeFrame(t, 1700)
	// Sample the wordmark band around 43% screen height.
	lit := 0
	for y := probeH * 40 / 100; y < probeH*47/100; y += 2 {
		for x := probeW * 35 / 100; x < probeW*65/100; x += 2 {
			if !f.isBG(x, y) {
				lit++
			}
		}
	}
	if lit < 50 {
		t.Errorf("wordmark band not rendered at t=1700ms (%d lit samples)", lit)
	}

	// A tick past the total duration must report the sequence as finished.
	initGoodbye()
	goodbye.inited = true
	tick := goodbye.startTick + uint64(goodbyeDuration) + 10
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	if renderGoodbye(renderer, &Config{}, tick) {
		t.Error("renderGoodbye still active past goodbyeDuration")
	}
}

// requestAppExit must arm the exit flag the main loop watches.
func TestRequestAppExitSetsFlag(t *testing.T) {
	prev := exitRequested.Load()
	defer exitRequested.Store(prev)
	exitRequested.Store(false)
	requestAppExit()
	if !exitRequested.Load() {
		t.Error("requestAppExit did not set exitRequested")
	}
}
