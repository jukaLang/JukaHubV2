package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// homeGridCards are the cards of the dashboard grid whose edges the static
// focus graph spells out card to card. Header chips, the launchers and the
// search pill are measured per frame and pinned by their own tests.
var homeGridCards = []HomeFocusID{
	HomeFocusContinue, HomeFocusMedia, HomeFocusFiles, HomeFocusPackages, HomeFocusApps,
	HomeFocusFavorites, HomeFocusBrowser, HomeFocusChat, HomeFocusTools, HomeFocusEmulator,
	HomeFocusSettings,
}

// homeGeometricNeighbour derives a card's neighbour from where the cards are
// drawn: the nearest card past the edge in that direction among those that
// overlap it on the other axis, preferring the one it overlaps most (within
// 2px is a tie — the integer scaling of the compact canvas — and the leftmost /
// topmost wins). "" means nothing is in that direction.
func homeGeometricNeighbour(layout *HomeLayout, from HomeFocusID, dir FocusDirection) HomeFocusID {
	src := layout.Items[from].Rect
	overlap := func(a0, a1, b0, b1 int32) int32 {
		lo, hi := max32(a0, b0), min32(a1, b1)
		if hi <= lo {
			return 0
		}
		return hi - lo
	}
	best := HomeFocusID("")
	var bestGap, bestOverlap, bestPos int32
	for _, id := range homeGridCards {
		if id == from {
			continue
		}
		r := layout.Items[id].Rect
		var gap, ov, pos int32
		switch dir {
		case FocusUp:
			gap, ov, pos = src.Y-(r.Y+r.H), overlap(src.X, src.X+src.W, r.X, r.X+r.W), r.X
		case FocusDown:
			gap, ov, pos = r.Y-(src.Y+src.H), overlap(src.X, src.X+src.W, r.X, r.X+r.W), r.X
		case FocusLeft:
			gap, ov, pos = src.X-(r.X+r.W), overlap(src.Y, src.Y+src.H, r.Y, r.Y+r.H), r.Y
		case FocusRight:
			gap, ov, pos = r.X-(src.X+src.W), overlap(src.Y, src.Y+src.H, r.Y, r.Y+r.H), r.Y
		}
		if gap < 0 || ov <= 0 {
			continue
		}
		switch {
		case best == "" || gap < bestGap:
			best, bestGap, bestOverlap, bestPos = id, gap, ov, pos
		case gap == bestGap && (ov > bestOverlap+2 || (ov+2 >= bestOverlap && pos < bestPos)):
			best, bestGap, bestOverlap, bestPos = id, gap, ov, pos
		}
	}
	return best
}

// TestHomeGridNeighboursFollowTheDesign pins the dashboard's focus graph to
// where the cards are actually drawn, on both shipped canvases: the Smart Pro's
// 1280x720 and the Brick's 960x720. The graph had drifted from the geometry
// more than once: Left from Packages stepped over the Files card to the
// Continue hero, Down from the full-width Media card skipped Files, Up from
// Files jumped past Media to the header, and Settings still pointed at the
// top-right corner it was drawn in before the six-tile row.
func TestHomeGridNeighboursFollowTheDesign(t *testing.T) {
	dirs := []struct {
		dir  FocusDirection
		name string
		of   func(homeNeighbors) HomeFocusID
	}{
		{FocusUp, "Up", func(n homeNeighbors) HomeFocusID { return n.Up }},
		{FocusDown, "Down", func(n homeNeighbors) HomeFocusID { return n.Down }},
		{FocusLeft, "Left", func(n homeNeighbors) HomeFocusID { return n.Left }},
		{FocusRight, "Right", func(n homeNeighbors) HomeFocusID { return n.Right }},
	}
	for _, canvas := range []sdl.Rect{{W: uiCanvasWideWidth, H: uiCanvasWideHeight}, {W: uiCanvasCompactW, H: uiCanvasCompactH}} {
		layout := NewHomeLayout(canvas.W, canvas.H)
		for _, from := range homeGridCards {
			for _, d := range dirs {
				want := homeGeometricNeighbour(layout, from, d.dir)
				got := d.of(homeFocusGraph[from])
				if want == "" {
					// Nothing on screen in that direction: the graph either stays
					// put or leaves the grid for a control outside it (the header,
					// the search pill, the Exit pill) — never for another card.
					for _, id := range homeGridCards {
						if got == id && got != from {
							t.Errorf("%dx%d: %s.%s = %s, but no card is drawn %s of it",
								canvas.W, canvas.H, from, d.name, got, d.name)
						}
					}
					continue
				}
				if got != want {
					t.Errorf("%dx%d: %s.%s = %s, want %s (the card drawn there)",
						canvas.W, canvas.H, from, d.name, got, want)
				}
			}
		}
	}
}

// TestHomeGridKeyEdges spells out the edges the user reported, so a failure
// names the complaint and not just a geometry rule.
func TestHomeGridKeyEdges(t *testing.T) {
	cases := []struct {
		from HomeFocusID
		dir  string
		want HomeFocusID
	}{
		{HomeFocusPackages, "Left", HomeFocusFiles},
		{HomeFocusMedia, "Down", HomeFocusFiles},
		{HomeFocusFiles, "Up", HomeFocusMedia},
		{HomeFocusPackages, "Up", HomeFocusMedia},
		{HomeFocusApps, "Up", HomeFocusMedia},
		{HomeFocusApps, "Down", HomeFocusSettings},
		{HomeFocusSettings, "Up", HomeFocusApps},
		{HomeFocusSettings, "Left", HomeFocusEmulator},
		{HomeFocusEmulator, "Right", HomeFocusSettings},
		{HomeFocusContinue, "Up", HomeFocusSearch},
		{HomeFocusSearch, "Down", HomeFocusContinue},
	}
	for _, tc := range cases {
		n := homeFocusGraph[tc.from]
		got := map[string]HomeFocusID{"Up": n.Up, "Down": n.Down, "Left": n.Left, "Right": n.Right}[tc.dir]
		if got != tc.want {
			t.Errorf("%s.%s = %s, want %s", tc.from, tc.dir, got, tc.want)
		}
	}
}

// TestHomeSearchPillSitsAboveTheGrid keeps the search pill in the band between
// the greeting and the cards on both canvases: inside the canvas, clear of the
// Continue card, and no wider than the greeting column.
func TestHomeSearchPillSitsAboveTheGrid(t *testing.T) {
	for _, canvas := range []sdl.Rect{{W: uiCanvasWideWidth, H: uiCanvasWideHeight}, {W: uiCanvasCompactW, H: uiCanvasCompactH}} {
		layout := NewHomeLayout(canvas.W, canvas.H)
		pill := layout.Items[HomeFocusSearch].Rect
		cont := layout.Items[HomeFocusContinue].Rect
		if pill.W <= 0 || pill.H <= 0 {
			t.Fatalf("%dx%d: no search pill: %+v", canvas.W, canvas.H, pill)
		}
		if pill.Y+pill.H > cont.Y-scaleY(8, canvas.H) {
			t.Errorf("%dx%d: pill %+v runs into the Continue card %+v", canvas.W, canvas.H, pill, cont)
		}
		if pill.Y < headerBarHeight() {
			t.Errorf("%dx%d: pill %+v climbs into the header bar", canvas.W, canvas.H, pill)
		}
		if pill.X != cont.X || pill.X+pill.W > cont.X+cont.W {
			t.Errorf("%dx%d: pill %+v is not inside the Continue column %+v", canvas.W, canvas.H, pill, cont)
		}
		for _, problem := range layout.BoundsViolations() {
			t.Errorf("%dx%d: %s", canvas.W, canvas.H, problem)
		}
	}
}
