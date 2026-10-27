package main

// appgrid_window_test.go — the row virtualization boundary and its on-screen
// consequences. The range tests need no renderer; the pixel tests render the
// real grid offscreen (dummy driver + software renderer, the established
// appGridProbeFrame harness) and look at which rows actually got painted.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestAppGridVisibleRangeZeroEntries(t *testing.T) {
	start, end := appGridVisibleRange(0, 5, 3, 0)
	if start != 0 || end != 0 {
		t.Fatalf("zero total window = [%d, %d), want empty", start, end)
	}
}

func TestAppGridVisibleRangeShortListEverything(t *testing.T) {
	// The whole list fits inside one window: every entry must be visited.
	start, end := appGridVisibleRange(6, 5, 2, 0)
	if start != 0 || end != 6 {
		t.Fatalf("short-list window = [%d, %d), want [0, 6)", start, end)
	}
}

func TestAppGridVisibleRangeScrolledWindow(t *testing.T) {
	// 100 entries at 5 columns (20 rows), 3 visible rows, scrolled to row 10:
	// the window starts at row 10's first index and covers three full rows
	// plus one slack row for the partial-bottom-row case.
	start, end := appGridVisibleRange(100, 5, 3, 10)
	if start != 50 || end != 70 {
		t.Fatalf("scrolled window = [%d, %d), want [50, 70)", start, end)
	}
}

func TestAppGridVisibleRangeEndsAtTotalWithSlack(t *testing.T) {
	// The slack row overlaps the list tail: start at row 3's first index,
	// and the window clamps at total instead of running past it.
	start, end := appGridVisibleRange(21, 5, 3, 3)
	if start != 15 || end != 21 {
		t.Fatalf("tail window = [%d, %d), want [15, 21)", start, end)
	}
}

func TestAppGridVisibleRangeDegenerateValues(t *testing.T) {
	// Defensive mins: zero rows/columns or a negative scroll must still give
	// a sane, non-inverted window (one row of one column, plus slack).
	start, end := appGridVisibleRange(10, 0, 0, -5)
	if start != 0 || end-start != 2 {
		t.Fatalf("degenerate layout window = [%d, %d), want [0, 2)", start, end)
	}
}

// virtualGridElement mirrors the probe harness's fixed launcher element.
func virtualGridElement() Element {
	return Element{Type: "dynamiclist", Variable: appsEntriesVar,
		X: 24, Y: 100, Width: "1232", Height: "488"}
}

// regionVariation is the largest per-channel spread inside rect: a uniformly
// filled (unpainted) region reads flat, a painted card (fill, border, icon
// well, text) does not.
func regionVariation(f *probeFrame, rect sdl.Rect) int {
	var minR, minG, minB = 255, 255, 255
	var maxR, maxG, maxB int
	first := true
	for y := rect.Y; y < rect.Y+rect.H; y++ {
		for x := rect.X; x < rect.X+rect.W; x++ {
			c := f.at(x, y)
			r, g, b := int(c.R), int(c.G), int(c.B)
			if first {
				minR, minG, minB, maxR, maxG, maxB = r, g, b, r, g, b
				first = false
				continue
			}
			if r < minR {
				minR = r
			} else if r > maxR {
				maxR = r
			}
			if g < minG {
				minG = g
			} else if g > maxG {
				maxG = g
			}
			if b < minB {
				minB = b
			} else if b > maxB {
				maxB = b
			}
		}
	}
	spread := func(mn, mx int) int {
		d := mx - mn
		if d < 0 {
			return 0
		}
		return d
	}
	m := spread(minR, maxR)
	if g := spread(minG, maxG); g > m {
		m = g
	}
	if b := spread(minB, maxB); b > m {
		m = b
	}
	return m
}

func TestAppGridPaintsOnlyTheVisibleRowWindow(t *testing.T) {
	// A launcher long enough that totalRows exceeds the visible window by
	// several rows, focused deep on the last row. The count is derived from
	// the layout rather than fixed at 300: a fixed number can fit the probe
	// screen after a tile-size change, and then the test would skip itself
	// instead of checking that off-window rows are not painted.
	elem := virtualGridElement()
	elemH := getElementHeight(elem, 500)
	measure := newAppGridLayout(elem, appGridElementWidth(elem))
	rows := appGridRows(elem, elemH, measure)
	if measure.cols < 1 || rows < 1 {
		t.Fatalf("probe layout is degenerate: %d columns, %d rows", measure.cols, rows)
	}
	count := (rows + 3) * measure.cols
	entries := make([]FileEntry, 0, count)
	for i := 0; i < count; i++ {
		entries = append(entries, FileEntry{Name: "VirtualApp" + itoaGrid(i), Path: "/apps/app.exe"})
	}
	focus := count - 1
	f, layout := appGridProbeFrame(t, entries, focus)
	first := appGridFirstRow(focus, rows, len(entries), layout.cols)
	if first == 0 {
		t.Fatalf("a %d-entry launcher (%d rows) did not scroll the %d-row window", count, (count+layout.cols-1)/layout.cols, rows)
	}

	// The row above the window must read flat — panel fill only: an
	// off-screen row is not appended, not even partially.
	offRow := appGridIconRect(appGridCellRect(layout, (first-1)*layout.cols, first))
	if v := regionVariation(f, offRow); v > 2 {
		t.Errorf("virtualized row's icon box varies by %d; an off-window row must not be painted", v)
	}

	// The window's top row is painted: icon well, border and text.
	onCell := appGridIconRect(appGridCellRect(layout, first*layout.cols, first))
	if v := regionVariation(f, onCell); v < 12 {
		t.Errorf("window row's icon box varies by only %d; the visible row must be painted", v)
	}
}

func TestAppGridClipsThePartialRowAtThePanelEdge(t *testing.T) {
	// Two rows past the window guarantees a row straddles the bottom edge,
	// however tall the tiles are. A fixed 40 entries can fit the panel and
	// then the clip this test exists for would never be exercised.
	elem := virtualGridElement()
	elemH := getElementHeight(elem, 500)
	measure := newAppGridLayout(elem, appGridElementWidth(elem))
	rows := appGridRows(elem, elemH, measure)
	if measure.cols < 1 || rows < 1 {
		t.Fatalf("probe layout is degenerate: %d columns, %d rows", measure.cols, rows)
	}
	count := (rows + 2) * measure.cols
	entries := make([]FileEntry, 0, count)
	for i := 0; i < count; i++ {
		entries = append(entries, FileEntry{Name: "EdgeApp" + itoaGrid(i), Path: "/apps/app.exe"})
	}
	f, layout := appGridProbeFrame(t, entries, 0)
	totalRows := (len(entries) + layout.cols - 1) / layout.cols
	if totalRows <= rows {
		t.Fatalf("a %d-entry grid is only %d rows for a %d-row panel", len(entries), totalRows, rows)
	}
	// The row straddling the bottom edge, opened from first=0.
	straddle := appGridCellRect(layout, rows*layout.cols, 0)
	panelBottom := elem.Y + elemH - 4 // the loop's own bottom bound
	var visible int32
	if straddle.Y < panelBottom {
		visible = straddle.H
		if panelBottom-straddle.Y < visible {
			visible = panelBottom - straddle.Y
		}
	}
	if visible <= 0 || visible >= straddle.H {
		// The fixture now guarantees a row exists past the panel, so the only
		// reason there is no *partially* visible one is that the cut lands
		// exactly on a row boundary. That means the layout changed and this
		// test needs re-pointing, not that it should quietly not run.
		t.Fatalf("the panel cut leaves no partial row (%d of %d px visible); the clip is unverified", visible, straddle.H)
	}
	// Sample the tile's visible band only, up to the cut — pixels past the
	// panel bottom are page background and would fake the spread.
	sampleH := straddle.H - 2
	if room := panelBottom - straddle.Y; room < sampleH {
		sampleH = room
	}
	sample := sdl.Rect{X: straddle.X + straddle.W/2 - 20, Y: straddle.Y, W: 40, H: sampleH}
	if v := regionVariation(f, sample); v < 8 {
		t.Errorf("the straddling row's visible %dpx band is flat (variation %d): a partially visible row must be drawn and clipped, not skipped", visible, v)
	}
}

// itoaGrid is a tiny deterministic suffix so two entries never share a
// display name (tile tints are keyed by name).
func itoaGrid(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoaGrid(i/10) + string(rune('0'+i%10))
}
