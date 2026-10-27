package main

// The Media gallery's tap path used to disagree with what it drew. The list
// layout draws 100px rows at a 114px pitch, but the click path always used
// the grid's cell math and stopped at the first screenful (cols*rows), so a
// tap landed a row short of the card under the finger and every row after
// the sixth was untappable. These tests pin the shared geometry and the pill
// bookkeeping that goes with it.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// TestSearchResultsListRowClickHitsTheDrawnRow walks the whole loaded list: a
// tap in the middle of row i plays i, at every scroll offset, including the
// rows past the old first-screen cap.
func TestSearchResultsListRowClickHitsTheDrawnRow(t *testing.T) {
	withScreen(t, 1280, 720)
	prev := scrollY
	scrollY = 0
	t.Cleanup(func() { scrollY = prev })

	element := Element{Type: "searchresults", Variable: "search_results", X: 24, Y: 136, Columns: 1, Rows: 6}
	videos := make([]VideoInfo, 30)
	pitch := searchResultRowH + searchResultRowGap

	// Unscrolled: row i's centre is at Y + i*pitch + rowH/2, for every row
	// the viewport can show.
	for i := range videos {
		rowY := element.Y + int32(i)*pitch + searchResultRowH/2
		if rowY > element.Y+getElementHeight(element, 456) {
			break
		}
		if got := searchResultIndexAt(element, videos, element.X+100, rowY); got != i {
			t.Fatalf("tap in row %d (y=%d) hit %d, want %d", i, rowY, got, i)
		}
	}

	// Scrolled by two rows: the hit follows the smooth-scroll offset.
	scrollY = 2 * pitch
	if got := searchResultIndexAt(element, videos, element.X+100, element.Y+searchResultRowH/2); got != 2 {
		t.Fatalf("scrolled tap hit %d, want row 2", got)
	}

	// The old cap was cols*rows = 6; row 19 was beyond it and untappable.
	scrollY = 19 * pitch
	if got := searchResultIndexAt(element, videos, element.X+100, element.Y+searchResultRowH/2); got != 19 {
		t.Fatalf("row 19 tapped = %d, want 19 (unreachable before the fix)", got)
	}

	// A tap on no row at all plays nothing.
	scrollY = 0
	if got := searchResultIndexAt(element, videos, element.X+100, element.Y-4); got != -1 {
		t.Fatalf("tap above the list hit %d, want none", got)
	}

	// A row scrolled under the top edge still has geometry there — the clip
	// hides it, so the control band above the list must not play it.
	scrollY = 100
	if got := searchResultIndexAt(element, videos, element.X+100, element.Y-36); got != -1 {
		t.Fatalf("tap above a scrolled list hit %d, want none", got)
	}
	// And nothing outside the results box at all: left of it, under it.
	if got := searchResultIndexAt(element, videos, element.X-1, element.Y+searchResultRowH/2); got != -1 {
		t.Fatalf("tap left of the list hit %d, want none", got)
	}
	if got := searchResultIndexAt(element, videos, element.X+100, element.Y+getElementHeight(element, 456)+1); got != -1 {
		t.Fatalf("tap below the list hit %d, want none", got)
	}
}

// TestSearchResultsGridClickReachesScrolledRows: the grid draws every row of
// the loaded list into the clipped viewport, so a scrolled row must be
// tappable too — the old first-screen cap stopped at cols*rows.
func TestSearchResultsGridClickReachesScrolledRows(t *testing.T) {
	withScreen(t, 1280, 720)
	prev := scrollY
	scrollY = 0
	t.Cleanup(func() { scrollY = prev })

	element := Element{Type: "searchresults", Variable: "search_results", X: 24, Y: 136, Columns: 2, Rows: 6}
	videos := make([]VideoInfo, 30)

	// Unscrolled first screen: the two cards of row 0.
	cols, rows := gridColsRows(element)
	elemWidth := getElementWidth(element, 1232)
	elemHeight := getElementHeight(element, 456)
	cellWidth := (elemWidth - 30) / cols
	cellHeight := (elemHeight - 30) / rows
	if got := searchResultIndexAt(element, videos, element.X+4, element.Y+4); got != 0 {
		t.Fatalf("top-left card tapped = %d, want 0", got)
	}
	if got := searchResultIndexAt(element, videos, element.X+cellWidth+14, element.Y+4); got != 1 {
		t.Fatalf("top-right card tapped = %d, want 1", got)
	}

	// Card 20 is col 0 of row 10. Scroll so that row sits at the viewport top.
	scrollY = 10 * (cellHeight + 10)
	if got := searchResultIndexAt(element, videos, element.X+4, element.Y+4); got != 20 {
		t.Fatalf("scrolled grid tap hit %d, want card 20", got)
	}
	if got := searchResultIndexAt(element, videos, element.X+cellWidth+14, element.Y+4); got != 21 {
		t.Fatalf("scrolled grid tap hit %d, want card 21", got)
	}
}

// TestSearchResultsFramesClearPillHits: only the list layout draws Audio /
// Video / Chat pills, so a grid frame, an error card or an empty state must
// not answer a tap with a pill a previous frame recorded.
func TestSearchResultsFramesClearPillHits(t *testing.T) {
	newProbeFrame(t)
	withScreen(t, probeW, probeH)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	t.Cleanup(func() { destroyTestRenderer(renderer) })

	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	config.Variables.Custom["search_results"] = []VideoInfo{{Title: "one"}, {Title: "two"}}

	listElem := Element{Type: "searchresults", Variable: "search_results", X: 24, Y: 136, Columns: 1, Rows: 6}
	prev := scrollY
	scrollY = 0
	t.Cleanup(func() { scrollY = prev })

	renderSearchResults(renderer, config, listElem)
	if len(resultActionHits) == 0 {
		t.Fatal("the list frame recorded no pill hits to begin with")
	}

	// The same results drawn as a grid paint no pills, so no pill may answer
	// a tap any more.
	gridElem := listElem
	gridElem.Columns = 2
	renderSearchResults(renderer, config, gridElem)
	if len(resultActionHits) != 0 {
		t.Fatalf("the grid frame left %d stale pill hits behind", len(resultActionHits))
	}

	// An empty state clears them too.
	resultActionHits = append(resultActionHits,
		resultActionHit{action: resultActionAudio, index: 0, rect: sdl.Rect{X: 40, Y: 140, W: 70, H: 30}})
	config.Variables.Custom["search_results"] = []VideoInfo{}
	renderSearchResults(renderer, config, listElem)
	if len(resultActionHits) != 0 {
		t.Fatalf("the empty frame left %d stale pill hits behind", len(resultActionHits))
	}
}
