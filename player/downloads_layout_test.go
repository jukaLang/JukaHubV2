package main

import "testing"

// TestDownloadListStaysInsidePanel is the regression test for the short-panel
// fallback: when the tabs and storage card leave no room, the viewport used to
// be set to listTop+rowH, past the panel's footer, and the clipped list painted
// over the card frame. A degenerate panel must collapse instead.
func TestDownloadListStaysInsidePanel(t *testing.T) {
	// The old fallback for a panel bottom above the list top: 500+68 = 568,
	// which is outside the panel. Kept so the test discriminates.
	if oldBottom := int32(500) + 68; oldBottom <= 480 {
		t.Fatal("test cannot discriminate: the old fallback already fits the panel")
	}
	top, bottom := downloadListBounds(500, 480)
	if top != bottom || bottom != 480 {
		t.Fatalf("degenerate viewport = [%d,%d), want [480,480)", top, bottom)
	}
	if bottom > 480 {
		t.Fatalf("viewport bottom %d is past the panel edge 480", bottom)
	}
	// Normal space is passed through untouched.
	if top, bottom := downloadListBounds(100, 400); top != 100 || bottom != 400 {
		t.Fatalf("normal viewport = [%d,%d), want [100,400)", top, bottom)
	}
}

// TestDownloadVisibleRangeReachesLastRow certifies the class-2 case for this
// list: at maximum scroll the window contains the last row and nothing past it.
func TestDownloadVisibleRangeReachesLastRow(t *testing.T) {
	const rowH, gap, sectionH, visibleH = int32(68), int32(8), int32(24), int32(76)
	rows := make([]downloadRow, 5)
	// Five rows of 76 px in a 76 px viewport: max scroll is 4*(rowH+gap).
	first, end := downloadVisibleRowRange(rows, 4*(rowH+gap), visibleH, rowH, gap, sectionH, nil)
	if first != 4 || end != 5 {
		t.Fatalf("window at max scroll = [%d,%d), want the last row only [4,5)", first, end)
	}
}
