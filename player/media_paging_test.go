package main

import (
	"strings"
	"testing"
)

// The paging command has to ask yt-dlp for a larger playlist and then slice the
// requested window out of it; anything else returns page one again.
func TestBuildMediaPageCommandMovesThePlaylistWindow(t *testing.T) {
	base := `yt-dlp --flat-playlist --dump-single-json --default-search ytsearch --no-playlist ` +
		`--no-check-certificate --geo-bypass --skip-download --quiet --ignore-errors "ytsearch20:lofi beats"`

	if got := buildMediaPageCommand(base, 20, 0); got != base {
		t.Fatalf("page 0 changed the command:\n%s", got)
	}

	next := buildMediaPageCommand(base, 20, 1)
	for _, want := range []string{`"ytsearch40:lofi beats"`, "--playlist-start 21", "--playlist-end 40"} {
		if !strings.Contains(next, want) {
			t.Errorf("next page command missing %q:\n%s", want, next)
		}
	}

	// Trending already carries a 12-result window; page 2 must move both flags
	// and grow the search count to cover the slice.
	trending := `yt-dlp --flat-playlist --dump-single-json --default-search ytsearch ` +
		`--playlist-start 1 --playlist-end 12 "ytsearch12:trending"`
	third := buildMediaPageCommand(trending, 12, 2)
	for _, want := range []string{`"ytsearch36:trending"`, "--playlist-start 25", "--playlist-end 36"} {
		if !strings.Contains(third, want) {
			t.Errorf("page 2 command missing %q:\n%s", want, third)
		}
	}
	if strings.Contains(third, "--playlist-start 1 ") {
		t.Errorf("page 2 kept the first-page window:\n%s", third)
	}
}

func TestMediaPagingStateRemembersAndResets(t *testing.T) {
	clearMediaPaging()
	t.Cleanup(clearMediaPaging)

	rememberMediaPaging("search_results", `yt-dlp "ytsearch20:cats"`)
	state, ok := mediaPageState("search_results")
	if !ok || state.pageSize != 20 || state.page != 0 || state.loading {
		t.Fatalf("state = %+v, %v", state, ok)
	}

	markMediaPageRequested("search_results", 1)
	if !mediaPageLoading("search_results") {
		t.Fatal("a requested page is not marked loading")
	}
	markMediaPageSettled("search_results", true)
	state, _ = mediaPageState("search_results")
	if state.loading || !state.done || state.page != 1 {
		t.Fatalf("settled state = %+v", state)
	}

	// A command without a ytsearch spec has nothing to page.
	rememberMediaPaging("search_results", `yt-dlp "https://youtu.be/abc"`)
	if _, ok := mediaPageState("search_results"); ok {
		t.Fatal("a pasted link kept a page window")
	}
}

func TestMediaPageTargetStepsByPage(t *testing.T) {
	cases := []struct {
		focused, loaded, size, delta int
		wantIndex                    int
		wantMore                     bool
	}{
		{0, 20, 20, 1, 20, true},
		{20, 40, 20, 1, 40, true},
		{25, 40, 20, -1, 20, false},
		{5, 20, 20, -1, 0, false},
		{0, 0, 20, 1, 0, true},
	}
	for _, c := range cases {
		index, more := mediaPageTarget(c.focused, c.loaded, c.size, c.delta)
		if index != c.wantIndex || more != c.wantMore {
			t.Errorf("target(focus=%d loaded=%d size=%d delta=%d) = (%d,%v), want (%d,%v)",
				c.focused, c.loaded, c.size, c.delta, index, more, c.wantIndex, c.wantMore)
		}
	}
}

func TestMergeMediaResultsDropsDuplicates(t *testing.T) {
	existing := []VideoInfo{{ID: "a", Title: "A"}, {ID: "b", Title: "B"}}
	incoming := []VideoInfo{{ID: "b", Title: "B"}, {ID: "c", Title: "C"}, {Title: "A"}}
	merged, added := mergeMediaResults(existing, incoming)
	if added != 1 {
		t.Fatalf("added = %d, want 1 (%+v)", added, merged)
	}
	if len(merged) != 3 || merged[2].ID != "c" {
		t.Fatalf("merged = %+v", merged)
	}
}

// The Next chip only shows once the user has reached the end of what is
// loaded, and its hitbox stays inside the results viewport.
func TestMediaNextPageChipShowsAtTheEnd(t *testing.T) {
	clearMediaPaging()
	t.Cleanup(clearMediaPaging)
	rememberMediaPaging("search_results", `yt-dlp "ytsearch20:cats"`)

	config := &Config{}
	videos := make([]VideoInfo, 20)
	element := Element{X: 24, Y: 136, Columns: 2, Rows: 6, Variable: "search_results"}
	config.Variables.Custom = map[string]interface{}{"search_results": videos}

	prevFocus := focusedResultIndex
	t.Cleanup(func() { focusedResultIndex = prevFocus })

	focusedResultIndex = 5
	if mediaNextPageVisible(config, element, videos) {
		t.Fatal("the chip showed in the middle of the grid")
	}
	focusedResultIndex = len(videos) - 1
	if !mediaNextPageVisible(config, element, videos) {
		t.Fatal("the chip is missing on the last card")
	}

	rect := mediaNextPageRect(element, 1232, 456)
	if rect.X < element.X || rect.X+rect.W > element.X+1232 {
		t.Fatalf("chip escapes the viewport horizontally: %+v", rect)
	}
	if rect.Y < element.Y || rect.Y+rect.H > element.Y+456 {
		t.Fatalf("chip escapes the viewport vertically: %+v", rect)
	}
	if !mediaNextPageHit(config, element, videos, rect.X+4, rect.Y+4) {
		t.Fatal("a click inside the chip was not recognised")
	}
	if mediaNextPageHit(config, element, videos, rect.X+rect.W+10, rect.Y+4) {
		t.Fatal("a click outside the chip was treated as a hit")
	}
}

// Pressing R2/L2 pages through the Media grid; a backward jump inside the
// loaded pages never starts a network fetch.
func TestSearchResultsShoulderPagesTheGrid(t *testing.T) {
	clearMediaPaging()
	t.Cleanup(clearMediaPaging)
	rememberMediaPaging("search_results", `yt-dlp "ytsearch20:cats"`)

	config := &Config{}
	config.Scenes = []SceneConfig{{Name: "Tube", Elements: []Element{
		{Type: "input", Variable: "search_query"},
		{Type: "searchresults", Variable: "search_results", Columns: 2, Rows: 6},
	}}}
	videos := make([]VideoInfo, 20)
	config.Variables.Custom = map[string]interface{}{"search_results": videos}

	prevScene, prevFocus := currentSceneIndex, focusedResultIndex
	t.Cleanup(func() {
		currentSceneIndex, focusedResultIndex = prevScene, prevFocus
	})
	currentSceneIndex = 0
	focusedResultIndex = 12

	element := config.Scenes[0].Elements[1]
	if !searchResultsShoulder(padL2, config, element, videos) {
		t.Fatal("L2 was not handled on the grid")
	}
	if focusedResultIndex != 0 {
		t.Fatalf("L2 from page 1 focus = %d, want 0", focusedResultIndex)
	}

	if !searchResultsShoulder(padR1, config, element, videos) {
		t.Fatal("R1 was not handled on the grid")
	}
	if got := focusedResultIndex; got != 12 {
		t.Fatalf("R1 page step = %d, want one page (12)", got)
	}
}

// Paging needs a list already on screen: asking for page two of nothing would
// leave the grid with the wrong batch.
func TestLoadMoreNeedsResultsOnScreen(t *testing.T) {
	clearMediaPaging()
	t.Cleanup(clearMediaPaging)
	rememberMediaPaging("search_results", `yt-dlp "ytsearch20:cats"`)

	config := &Config{}
	config.Scenes = []SceneConfig{{Name: "Tube", Elements: []Element{
		{Type: "searchresults", Variable: "search_results"},
	}}}
	config.Variables.Custom = map[string]interface{}{}
	prevScene := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prevScene })
	currentSceneIndex = 0

	if loadMoreMediaResults(config) {
		t.Fatal("a page fetch started with an empty grid")
	}
}

func clearMediaPaging() {
	mediaPaging.mu.Lock()
	mediaPaging.byVar = map[string]*mediaResultsPage{}
	mediaPaging.mu.Unlock()
}
