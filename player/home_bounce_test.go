package main

import (
	"math"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestHomeFocusBounceSettlesAtSmallLift(t *testing.T) {
	base := sdl.Rect{X: 20, Y: 30, W: 100, H: 80}
	if got := scaleHomeCard(base, uiFocusBounce.at(0)); got != base {
		t.Fatalf("focus starts at base rect: got %+v", got)
	}
	peak := scaleHomeCard(base, uiFocusBounce.at(0.72))
	settled := scaleHomeCard(base, uiFocusBounce.at(1))
	if peak.W != base.W+8 || settled.W != base.W+6 {
		t.Fatalf("focus lift should overshoot by one pixel per edge: peak %+v, settled %+v", peak, settled)
	}
}

func TestHomeScrollBounceIsBoundedAndSettles(t *testing.T) {
	for _, tc := range []struct{ from, target float64 }{
		{0, 1}, {4, 0}, {0, 12},
	} {
		if got := homeDynScrollAt(tc.from, tc.target, 0); got != tc.from {
			t.Fatalf("scroll should start at %v, got %v", tc.from, got)
		}
		peak := homeDynScrollAt(tc.from, tc.target, 0.78)
		over := math.Abs(peak-tc.target) * float64(homeDynPitch)
		if over <= 0 || over > 3.001 {
			t.Fatalf("scroll overshoot should stay within 3px: %vpx", over)
		}
		if got := homeDynScrollAt(tc.from, tc.target, 1); got != tc.target {
			t.Fatalf("scroll should settle at %v, got %v", tc.target, got)
		}
	}
}

func TestHomeScrollBounceSnapsWhenMotionDisabled(t *testing.T) {
	h := &HomeLayout{Motion: false, dyn: homeDyn{first: 2}}
	h.dynStep(100)
	if h.dyn.scroll != 2 || h.dyn.scrollEnter.active || h.dyn.anim {
		t.Fatalf("reduced motion should snap to row 2: %+v", h.dyn)
	}
}

func TestOverlayBounceFollowsArrivalDirection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		motion     uiKeyframe
		start, end float64
	}{
		{"Start menu drops", uiMenuDrop, -1, 1},
		{"dropdown drops", uiDropDown, -1, 1},
		{"sheet rises", uiPanelRise, 1, -1},
	} {
		if got := tc.motion.at(0); got*tc.start <= 0 {
			t.Errorf("%s should begin outside its resting point: %v", tc.name, got)
		}
		if got := tc.motion.at(0.6); got*tc.end <= 0 || math.Abs(got) > 8 {
			t.Errorf("%s should bounce by at most 8px in its arrival direction: %v", tc.name, got)
		}
		if got := tc.motion.at(1); got != 0 {
			t.Errorf("%s should settle at its original position: %v", tc.name, got)
		}
	}
}
