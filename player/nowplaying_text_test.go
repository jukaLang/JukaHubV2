package main

import "testing"

// TestNowPlayingTitleCentresOnDrawnWidth is the regression test for the
// full-screen now-playing title. The renderer measured the raw filename with
// big.SizeUTF8(title) and then drew ellipsize(title, screenW-160) at
// (screenW-rawW)/2: for a long filename the ellipsized line started left of
// the canvas (a 2000 px title on a 1280 px screen began at -360) and its left
// half was off-screen. The line must be measured after it is truncated.
func TestNowPlayingTitleCentresOnDrawnWidth(t *testing.T) {
	const screenW int32 = 1280
	// The old placement, kept so the regression is provable: a 2000 px
	// untruncated title started off the left edge.
	if oldX := (screenW - 2000) / 2; oldX >= 0 {
		t.Fatalf("test cannot discriminate: the old placement (%d) is on screen", oldX)
	}
	drawn := nowPlayingLineMax(screenW)
	if drawn <= 0 || drawn > screenW {
		t.Fatalf("line budget %d is not a usable width on a %d px canvas", drawn, screenW)
	}
	if x := nowPlayingLineX(screenW, drawn); x < 0 || x != (screenW-drawn)/2 {
		t.Fatalf("ellipsized title starts at %d, want %d", x, (screenW-drawn)/2)
	}
	// A short line is still centred exactly.
	if x := nowPlayingLineX(screenW, 600); x != 340 {
		t.Fatalf("600 px line starts at %d, want 340", x)
	}
	// A line measured at the full canvas starts at 0, never at a negative x.
	if x := nowPlayingLineX(screenW, screenW); x != 0 {
		t.Fatalf("full-width line starts at %d, want 0", x)
	}
}

// TestNowPlayingLineBudgetNeverNegative pins the clamp: a canvas narrower than
// the 160 px margins must not produce a negative ellipsize budget (ellipsize
// returns the string unchanged for non-positive widths).
func TestNowPlayingLineBudgetNeverNegative(t *testing.T) {
	if got := nowPlayingLineMax(120); got != 0 {
		t.Fatalf("budget on a 120 px canvas = %d, want 0", got)
	}
}
