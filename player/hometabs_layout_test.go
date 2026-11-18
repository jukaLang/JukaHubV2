package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestHomeTabsGridUsesSpaceWithoutBreakingNavigation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		width      int32
		height     int32
		bodyWidth  int32
		bodyHeight int32
	}{
		{"Smart Pro", 1280, 720, 1200, 568},
		{"Brick", 1024, 768, 960, 600},
		{"compact", 640, 480, 600, 376},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := homeTabsScale(tc.width, tc.height)
			cards, horiz, extent := homeTabsLayout(homeTabViewGrid, 4, false, tc.bodyWidth, tc.bodyHeight, k, nil)
			if horiz || len(cards) != 4 || extent > tc.bodyHeight {
				t.Fatalf("four-card grid: cards=%d horizontal=%v extent=%d", len(cards), horiz, extent)
			}
			if cards[0].X <= 0 || cards[0].Y <= 0 || cards[0].W <= int32(150*k) {
				t.Fatalf("sparse grid should have larger centered cards: %+v", cards[0])
			}
			if right := tc.bodyWidth - (cards[3].X + cards[3].W); cards[0].X < right-1 || cards[0].X > right+1 {
				t.Fatalf("row is not centered: first=%+v last=%+v", cards[0], cards[3])
			}
			checkHomeTabsRects(t, cards, tc.bodyWidth, tc.bodyHeight)
			if got := homeTabsNeighbor(cards, 0, FocusRight); got != 1 {
				t.Fatalf("right from first card = %d", got)
			}
			if got := homeTabsNeighbor(cards, 3, FocusRight); got != -1 {
				t.Fatalf("right from last card = %d", got)
			}
		})
	}
}

func TestHomeTabsGridStillScrollsWhenFull(t *testing.T) {
	const bw, bh = int32(1200), int32(568)
	cards, horiz, extent := homeTabsLayout(homeTabViewGrid, 15, false, bw, bh, 1, nil)
	if horiz || extent <= bh || cards[0].Y != 0 {
		t.Fatalf("full grid should scroll from the top: horizontal=%v extent=%d first=%+v", horiz, extent, cards[0])
	}
	if got := homeTabsNeighbor(cards, 0, FocusDown); got != 5 {
		t.Fatalf("down from first card = %d, want next row", got)
	}
	lvl := &homeTabsLevel{focus: len(cards) - 1}
	g := &homeTabsGeom{rects: cards, body: sdl.Rect{W: bw, H: bh}, extent: extent}
	scroll := homeTabsScrollTarget(lvl, homeTabViewGrid, g, 14)
	last := cards[len(cards)-1]
	if scroll <= 0 || float64(last.Y+last.H) > scroll+float64(bh) {
		t.Fatalf("last card not brought into view: scroll=%v last=%+v", scroll, last)
	}
	compact, _, _ := homeTabsLayout(homeTabViewCompact, 4, false, bw, bh, 1, nil)
	if compact[0].X != 0 || compact[0].Y != 0 || compact[0].W >= cards[0].W {
		t.Fatalf("compact view lost its dense top-aligned layout: %+v", compact[0])
	}
}

func TestHomeTabsGridBalancesShortSecondRows(t *testing.T) {
	const bw, bh = int32(1200), int32(568)
	for _, tc := range []struct {
		n, columns int
	}{
		{6, 3},
		{7, 4},
		{9, 5},
	} {
		cards, _, extent := homeTabsLayout(homeTabViewGrid, tc.n, false, bw, bh, 1, nil)
		if extent > bh || cards[tc.columns].Y <= cards[0].Y {
			t.Fatalf("%d cards should fit in two rows: extent=%d first=%+v next=%+v", tc.n, extent, cards[0], cards[tc.columns])
		}
		if cards[tc.columns-1].Y != cards[0].Y {
			t.Fatalf("%d cards did not make %d columns", tc.n, tc.columns)
		}
		lastRowLeft := cards[tc.columns].X
		lastRowRight := bw - (cards[len(cards)-1].X + cards[len(cards)-1].W)
		if lastRowLeft < lastRowRight-1 || lastRowLeft > lastRowRight+1 {
			t.Fatalf("%d cards leave an unbalanced last row: %d px left, %d px right", tc.n, lastRowLeft, lastRowRight)
		}
		checkHomeTabsRects(t, cards, bw, bh)
		if got := homeTabsNeighbor(cards, 0, FocusDown); got < tc.columns {
			t.Fatalf("down from first of %d cards should reach second row, got %d", tc.n, got)
		}
	}
}

func checkHomeTabsRects(t *testing.T, cards []sdl.Rect, bw, bh int32) {
	t.Helper()
	for i, r := range cards {
		if r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > bw || r.Y+r.H > bh {
			t.Fatalf("card %d leaves the viewport: %+v in %dx%d", i, r, bw, bh)
		}
		for j := 0; j < i; j++ {
			o := cards[j]
			if r.X < o.X+o.W && o.X < r.X+r.W && r.Y < o.Y+o.H && o.Y < r.Y+r.H {
				t.Fatalf("cards %d and %d overlap: %+v %+v", j, i, o, r)
			}
		}
	}
}
