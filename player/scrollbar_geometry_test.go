package main

import (
	"math"
	"testing"
)

// TestScrollbarThumbStaysInsideTrack is the regression test for the thumb
// geometry: the draw code floored the thumb at 24 px and then placed it with
// maxOff = h - thumbH, so a track shorter than 24 px got a thumb that hung
// above or below the track (negative maxOff made the offset run backwards).
// Every caller with a short list body hits that.
func TestScrollbarThumbStaysInsideTrack(t *testing.T) {
	const trackY int32 = 100
	cases := []struct {
		name         string
		h            int32
		frac, scroll float64
	}{
		{"short track at the top", 8, 0.5, 0},
		{"short track at the end", 8, 0.5, 1},
		{"short track mid-scroll", 18, 0.1, 0.5},
		{"just under the floor", 23, 0.9, 1},
		{"exactly the floor", 24, 0.5, 0.5},
		{"one-pixel track", 1, 0.5, 0.5},
		{"unmeasurable thumb", 10, math.NaN(), 0.5},
		{"unmeasurable scroll", 40, 0.25, math.NaN()},
	}
	for _, tc := range cases {
		r := scrollbarThumbRect(trackY, tc.h, tc.frac, tc.scroll)
		if r.H <= 0 || r.H > tc.h {
			t.Fatalf("%s: thumb height %d does not fit a %d px track", tc.name, r.H, tc.h)
		}
		if r.Y < trackY || r.Y+r.H > trackY+tc.h {
			t.Fatalf("%s: thumb %+v leaves the track [%d,%d)", tc.name, r, trackY, trackY+tc.h)
		}
	}
	// A full-size track keeps the old reading: a quarter-size thumb at the end
	// sits flush with the bottom.
	r := scrollbarThumbRect(trackY, 200, 0.25, 1)
	if r.H != 50 || r.Y != trackY+150 {
		t.Fatalf("full track: %+v, want H 50 at Y %d", r, trackY+150)
	}
}
