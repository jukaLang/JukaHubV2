package main

import (
	"strings"
	"testing"
)

// The initial query is limited to five results; each next request grows
// ytsearchN and slices out the following window.
func TestBuildMediaPageCommandMovesThePlaylistWindow(t *testing.T) {
	base := `yt-dlp --flat-playlist --dump-single-json --default-search ytsearch --no-playlist ` +
		`--no-check-certificate --geo-bypass --skip-download --quiet --ignore-errors --playlist-start 1 --playlist-end 5 "ytsearch5:lofi beats"`

	if got := buildMediaPageCommand(base, mediaSearchPageSize, 0); got != base {
		t.Fatalf("page 0 changed the command:\n%s", got)
	}

	next := buildMediaPageCommand(base, mediaSearchPageSize, 1)
	for _, want := range []string{`"ytsearch10:lofi beats"`, "--playlist-start 6", "--playlist-end 10"} {
		if !strings.Contains(next, want) {
			t.Errorf("next page command missing %q:\n%s", want, next)
		}
	}

	// Trending also uses the shared five-result page size. Page 2 must move
	// both flags and grow the search count to cover that requested slice.
	trending := `yt-dlp --flat-playlist --dump-single-json --default-search ytsearch ` +
		`--playlist-start 1 --playlist-end 5 "ytsearch5:trending"`
	third := buildMediaPageCommand(trending, mediaSearchPageSize, 2)
	for _, want := range []string{`"ytsearch15:trending"`, "--playlist-start 11", "--playlist-end 15"} {
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

	rememberMediaPaging("search_results", `yt-dlp "ytsearch5:cats"`)
	state, ok := mediaPageState("search_results")
	if !ok || state.pageSize != mediaSearchPageSize || state.page != 0 || state.loading {
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

// The chip remains available as focus and pointer position change.
func TestMediaNextPageChipStaysVisible(t *testing.T) {
	clearMediaPaging()
	t.Cleanup(clearMediaPaging)
	rememberMediaPaging("search_results", `yt-dlp "ytsearch5:cats"`)

	config := &Config{}
	videos := make([]VideoInfo, 5)
	element := Element{X: 24, Y: 136, Columns: 2, Rows: 6, Variable: "search_results"}
	config.Variables.Custom = map[string]interface{}{"search_results": videos}

	prevFocus := focusedResultIndex
	t.Cleanup(func() { focusedResultIndex = prevFocus })

	focusedResultIndex = 2
	if !mediaNextPageVisible(config, element, videos) {
		t.Fatal("the chip disappeared in the middle of the grid")
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

// The chip overlays a row and can also overlap configured input elements.
// Hover and click must leave the input alone and dispatch exactly one fetch.
func TestMediaNextPageClickFocusesResultsAndFetches(t *testing.T) {
	clearMediaPaging()
	t.Cleanup(clearMediaPaging)
	rememberMediaPaging("search_results", `yt-dlp --playlist-start 1 --playlist-end 5 "ytsearch5:cats"`)
	videos := []VideoInfo{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}, {ID: "e"}}
	element := Element{Type: "searchresults", X: 24, Y: 136, Width: "1232", Height: "520", Columns: 1, Variable: "search_results"}
	config := &Config{Scenes: []SceneConfig{{Name: "Tube", Elements: []Element{
		{Type: "input", X: 1100, Y: 600, Width: "150", Height: "50", Variable: "search_query"}, element,
	}}}}
	config.Variables.Custom = map[string]interface{}{"search_results": videos}
	oldScene, oldFocus, oldSelected, oldAction, oldEngine := currentSceneIndex, focusedResultIndex, selectedButtonIndex, focusedResultAction, focusEngine
	oldExecutor, oldKeyboard := mediaExecutePage, virtualKeyboardActive
	t.Cleanup(func() {
		currentSceneIndex, focusedResultIndex, selectedButtonIndex, focusedResultAction, focusEngine = oldScene, oldFocus, oldSelected, oldAction, oldEngine
		mediaExecutePage, virtualKeyboardActive = oldExecutor, oldKeyboard
	})
	currentSceneIndex, focusedResultIndex, selectedButtonIndex, focusedResultAction = 0, 0, 0, resultActionAudio
	focusEngine = NewFocusEngine()
	focusEngine.SetGraph("Tube", focusEngine.BuildGraph(config.Scenes[0]))
	focusEngine.SetByElementIndex(0)
	virtualKeyboardActive = false
	calls := 0
	mediaExecutePage = func(_ *Config, command, variable string, _ map[string]interface{}, appendMode bool) {
		calls++
		if !appendMode || variable != "search_results" || !strings.Contains(command, "--playlist-start 6") || !strings.Contains(command, "ytsearch10:cats") {
			t.Errorf("unexpected page request: %q, %q, append=%v", command, variable, appendMode)
		}
	}
	rect := mediaNextPageRect(element, 1232, 520)
	x, y := rect.X+rect.W/2, rect.Y+rect.H/2
	if focusMediaResultAt(config, x, y) || focusedResultIndex != 0 {
		t.Fatal("hovering Next changed the result under the chip")
	}
	if !handleMediaNextPageClick(config, x, y) {
		t.Fatal("Next click was not consumed")
	}
	focusEngine.SyncSelected()
	if selectedButtonIndex != 1 || focusedResultAction != resultActionNone || virtualKeyboardActive {
		t.Fatalf("focus=%d action=%d keyboard=%v", selectedButtonIndex, focusedResultAction, virtualKeyboardActive)
	}
	if calls != 1 || !mediaPageLoading("search_results") {
		t.Fatalf("requests=%d loading=%v", calls, mediaPageLoading("search_results"))
	}
	if !handleMediaNextPageClick(config, x, y) || calls != 1 {
		t.Fatal("a loading chip must consume the click without starting another fetch")
	}
	// Start again with focus on the input, as happens after a search. The
	// shoulder must send focus to the list as well as request the next page.
	clearMediaPaging()
	rememberMediaPaging("search_results", `yt-dlp --playlist-start 1 --playlist-end 5 "ytsearch5:cats"`)
	focusEngine.SetByElementIndex(0)
	selectedButtonIndex = 0
	if !searchResultsShoulder(padR2, config, element, videos) || calls != 2 {
		t.Fatal("R2 did not request the next page")
	}
	focusEngine.SyncSelected()
	if selectedButtonIndex != 1 || virtualKeyboardActive {
		t.Fatalf("R2 left focus=%d keyboard=%v", selectedButtonIndex, virtualKeyboardActive)
	}
}

// Pressing R2/L2 pages through the Media grid; a backward jump inside the
// loaded pages never starts a network fetch.
func TestSearchResultsShoulderPagesTheGrid(t *testing.T) {
	clearMediaPaging()
	t.Cleanup(clearMediaPaging)
	rememberMediaPaging("search_results", `yt-dlp "ytsearch5:cats"`)

	config := &Config{}
	config.Scenes = []SceneConfig{{Name: "Tube", Elements: []Element{
		{Type: "input", Variable: "search_query"},
		{Type: "searchresults", Variable: "search_results", Columns: 2, Rows: 6},
	}}}
	videos := make([]VideoInfo, 20)
	config.Variables.Custom = map[string]interface{}{"search_results": videos}

	prevScene, prevFocus, prevSelected, prevAction, prevEngine := currentSceneIndex, focusedResultIndex, selectedButtonIndex, focusedResultAction, focusEngine
	t.Cleanup(func() {
		currentSceneIndex, focusedResultIndex, selectedButtonIndex, focusedResultAction, focusEngine = prevScene, prevFocus, prevSelected, prevAction, prevEngine
	})
	focusEngine = NewFocusEngine()
	currentSceneIndex = 0
	focusedResultIndex = 5

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
		t.Fatalf("R1 page step = %d, want one screenful (12)", got)
	}
}

// Paging needs a list already on screen: asking for page two of nothing would
// leave the grid with the wrong batch.
func TestLoadMoreNeedsResultsOnScreen(t *testing.T) {
	clearMediaPaging()
	t.Cleanup(clearMediaPaging)
	rememberMediaPaging("search_results", `yt-dlp "ytsearch5:cats"`)

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
