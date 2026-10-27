package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// TestGrowTouchTargetEnlargesSmallRects: a control smaller than the minimum on
// either axis grows to it, keeping its centre.
func TestGrowTouchTargetEnlargesSmallRects(t *testing.T) {
	// The Exit pill: 116 wide, 36 tall. Width already qualifies; height grows.
	r := sdl.Rect{X: 8, Y: 678, W: 116, H: 36}
	g := growTouchTarget(r)
	if g.H < minTouchTarget {
		t.Fatalf("height = %d, want at least %d", g.H, minTouchTarget)
	}
	if g.W != 116 {
		t.Fatalf("width grew to %d; it was already big enough and should be unchanged", g.W)
	}
	// Centre is preserved vertically: 678 + 18 == grownY + grownH/2.
	origCenter := r.Y + r.H/2
	newCenter := g.Y + g.H/2
	if origCenter != newCenter {
		t.Fatalf("vertical centre moved: %d -> %d", origCenter, newCenter)
	}
}

// TestGrowTouchTargetLeavesLargeRects: an already-big rect is returned as-is.
func TestGrowTouchTargetLeavesLargeRects(t *testing.T) {
	r := sdl.Rect{X: 100, Y: 100, W: 200, H: 80}
	if got := growTouchTarget(r); got != r {
		t.Fatalf("large rect changed: %+v -> %+v", r, got)
	}
}

// TestGrowTouchTargetLeavesDegenerate: a zero rect (a hidden control) must not
// grow into a phantom target that swallows taps.
func TestGrowTouchTargetLeavesDegenerate(t *testing.T) {
	r := sdl.Rect{}
	if got := growTouchTarget(r); got != r {
		t.Fatalf("degenerate rect changed: %+v -> %+v", r, got)
	}
}

// TestHitTouchTargetAbsorbsNearMiss: a tap just outside the drawn pill but
// inside the grown area registers; one far away does not.
func TestHitTouchTargetAbsorbsNearMiss(t *testing.T) {
	// 116x36 pill at y=678: drawn bottom is 714. A tap at y=716 is 2px below
	// the drawn rect but inside the grown target.
	r := sdl.Rect{X: 8, Y: 678, W: 116, H: 36}
	if !hitTouchTarget(r, 60, 716) {
		t.Fatal("a 2px near-miss below the pill should still hit the grown target")
	}
	if hitTouchTarget(r, 60, 760) {
		t.Fatal("a tap far below the pill should not hit")
	}
}

// TestHomeExitChipHasFingerSizedTarget pins the Phase 8 audit item at its real
// subject: the built-in Exit pill is drawn 36px tall, and a tap a couple of
// pixels below it must still focus it.
func TestHomeExitChipHasFingerSizedTarget(t *testing.T) {
	h := NewHomeLayout(1280, 720)
	if h == nil {
		t.Fatal("nil layout")
	}
	exit := h.Items[HomeFocusExit].Rect
	if exit.H >= minTouchTarget {
		// The premise is that the drawn pill is smaller than a finger target,
		// so the grown tap area is what makes the near-miss land. If the pill
		// grew to 44px the test has lost its subject; re-point it at a compact
		// control rather than reporting green without checking anything.
		t.Fatalf("the Exit pill is drawn %dpx tall, at or above the %dpx touch target; the near-miss probe needs a smaller control", exit.H, minTouchTarget)
	}
	grown := growTouchTarget(exit)
	// A tap 2px below the drawn bottom edge lands focus on Exit.
	belowBottom := exit.Y + exit.H + 2
	if belowBottom >= grown.Y+grown.H {
		t.Fatalf("grown target bottom %d does not cover the near-miss y=%d", grown.Y+grown.H, belowBottom)
	}
	// FocusAt uses the grown rect, so it should set Exit.
	h.Focus.Set(HomeFocusContinue)
	if !h.FocusAt(exit.X+20, belowBottom) {
		t.Fatal("FocusAt did not focus the Exit pill from a near-miss tap")
	}
	if got := h.Focus.Current(); got != HomeFocusExit {
		t.Fatalf("focused %q from an Exit near-miss, want %q", got, HomeFocusExit)
	}
}
