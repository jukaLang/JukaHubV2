package main

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// resetTVLibrary isolates the Live TV store for one test and restores the
// previous state afterwards (the store is a package global like the other
// scene libraries).
func resetTVLibrary(t *testing.T) {
	t.Helper()
	tvLib.mu.Lock()
	prevChannels, prevIndex, prevCat, prevQuery := tvLib.channels, tvLib.index, tvLib.category, tvLib.query
	prevFavs, prevRead := tvLib.favs, tvLib.favsRead
	tvLib.channels, tvLib.index, tvLib.category, tvLib.query = nil, nil, tvAllCategory, ""
	tvLib.favs, tvLib.favsRead = nil, false
	tvLib.mu.Unlock()

	prevPath := tvFavoritesFilePath
	prevFocus := focusedFileIndex
	dir := t.TempDir()
	tvFavoritesFilePath = func() string { return filepath.Join(dir, tvFavoritesFileName) }
	t.Cleanup(func() {
		tvLib.mu.Lock()
		tvLib.channels, tvLib.index = prevChannels, prevIndex
		tvLib.category, tvLib.query = prevCat, prevQuery
		tvLib.favs, tvLib.favsRead = prevFavs, prevRead
		tvLib.mu.Unlock()
		tvFavoritesFilePath = prevPath
		focusedFileIndex = prevFocus
		drainCustomForTest()
	})
}

// drainCustomForTest empties the background publish queue so a test never
// blocks on a full channel.
func drainCustomForTest() {
	for {
		select {
		case <-customCh:
		default:
			return
		}
	}
}

const sampleM3U = "\ufeff#EXTM3U\r\n" +
	"#EXTINF:-1 tvg-id=\"bbc1.uk\" tvg-name=\"BBC, One\" group-title=\"News, UK\",BBC One\r\n" +
	"http://example.com/bbc1.m3u8\r\n" +
	"#EXTINF:-1 group-title=\"Sports\",Eurosport\r\n" +
	"#EXTVLCOPT:network-caching=1000\r\n" +
	"http://example.com/eurosport.m3u8\r\n" +
	"#EXTGRP:Kids\r\n" +
	"#EXTINF:-1,Kids TV\r\n" +
	"http://example.com/kids.m3u8\r\n" +
	"#EXTINF:-1,No Group\r\n" +
	"http://example.com/nogroup.m3u8\r\n" +
	"#EXTINF:-1,Broken Entry\r\n" +
	"file:///tmp/local.mp4\r\n" +
	"#EXTINF:-1,Duplicate\r\n" +
	"http://example.com/bbc1.m3u8\r\n"

// TestParseM3UChannelsReadsGroupsAndNames covers the parser upgrades: BOM and
// CRLF handling, group-title, #EXTGRP, quoted commas in attributes, directive
// lines between the metadata and the URL, skipped non-stream URLs, and dedupe.
func TestParseM3UChannelsReadsGroupsAndNames(t *testing.T) {
	chans := parseM3UChannels(sampleM3U)
	if len(chans) != 4 {
		t.Fatalf("parsed %d channels, want 4: %+v", len(chans), chans)
	}
	want := []TVChannel{
		{Name: "BBC One", URL: "http://example.com/bbc1.m3u8", Group: "News, UK"},
		{Name: "Eurosport", URL: "http://example.com/eurosport.m3u8", Group: "Sports"},
		{Name: "Kids TV", URL: "http://example.com/kids.m3u8", Group: "Kids"},
		{Name: "No Group", URL: "http://example.com/nogroup.m3u8", Group: tvOtherGroup},
	}
	for i, w := range want {
		if chans[i] != w {
			t.Errorf("channel %d = %+v, want %+v", i, chans[i], w)
		}
	}
}

// TestParseM3UBareURLList keeps simple one-URL-per-line playlists working.
func TestParseM3UBareURLList(t *testing.T) {
	chans := parseM3UChannels("http://a.example/one.m3u8\n\nhttp://b.example/two.m3u8\n")
	if len(chans) != 2 {
		t.Fatalf("parsed %d channels, want 2", len(chans))
	}
	if chans[0].URL != "http://a.example/one.m3u8" || chans[1].URL != "http://b.example/two.m3u8" {
		t.Fatalf("bare URLs lost: %+v", chans)
	}
	for _, ch := range chans {
		if ch.Group != tvOtherGroup || ch.Name == "" {
			t.Errorf("channel %+v has no fallback name/group", ch)
		}
	}
}

// TestParseM3UInfoLineHandlesQuotedCommas: the display name follows the last
// comma outside quotes, never the comma inside tvg-name.
func TestParseM3UInfoLineHandlesQuotedCommas(t *testing.T) {
	attrs, name := parseM3UInfoLine(`#EXTINF:-1 tvg-id="x" tvg-name="BBC, One" group-title="News, UK",BBC One HD`)
	if name != "BBC One HD" {
		t.Fatalf("name = %q, want %q", name, "BBC One HD")
	}
	if attrs["group-title"] != "News, UK" || attrs["tvg-id"] != "x" {
		t.Fatalf("attrs = %v", attrs)
	}
}

// TestTVCategoryCycleAndSearch: the selector walks All → Favorites → groups and
// the query filters within the active category.
func TestTVCategoryCycleAndSearch(t *testing.T) {
	resetTVLibrary(t)
	tvSetChannels([]TVChannel{
		{Name: "BBC One", URL: "http://x/1", Group: "News"},
		{Name: "BBC Two", URL: "http://x/2", Group: "News"},
		{Name: "Eurosport", URL: "http://x/3", Group: "Sports"},
	})
	if got := tvCategories(); len(got) != 4 || got[0] != tvAllCategory || got[1] != tvFavoritesCategory || got[2] != "News" || got[3] != "Sports" {
		t.Fatalf("categories = %v, want [All Favorites News Sports]", got)
	}
	if got := len(tvFilteredChannels()); got != 3 {
		t.Fatalf("All shows %d channels, want 3", got)
	}

	tvSetCategory("News")
	if got := len(tvFilteredChannels()); got != 2 {
		t.Fatalf("News shows %d channels, want 2", got)
	}
	tvSetQuery("two")
	if got := tvFilteredChannels(); len(got) != 1 || got[0].Name != "BBC Two" {
		t.Fatalf("search within News = %+v, want BBC Two", got)
	}

	// Cycling keeps the query; Sports has no "two" match.
	tvCycleCategory(1) // News -> Sports
	if got := tvCategory(); got != "Sports" {
		t.Fatalf("after Right category = %q, want Sports", got)
	}
	if got := len(tvFilteredChannels()); got != 0 {
		t.Fatalf("Sports with query two shows %d channels, want 0", got)
	}
	tvSetQuery("")
	if got := len(tvFilteredChannels()); got != 1 {
		t.Fatalf("Sports shows %d channels, want 1", got)
	}

	// Left walks back to News, then to Favorites, which is empty.
	tvCycleCategory(-1)
	if got := tvCategory(); got != "News" {
		t.Fatalf("after Left category = %q, want News", got)
	}
	tvCycleCategory(-1)
	if got := tvCategory(); got != tvFavoritesCategory {
		t.Fatalf("after second Left category = %q, want Favorites", got)
	}
	if got := len(tvFilteredChannels()); got != 0 {
		t.Fatalf("empty Favorites shows %d channels", got)
	}
}

// TestTVFavoriteToggleAndPersistence: starring writes tvfavorites.json and the
// Favorites category still lists the channel after the store is reloaded.
func TestTVFavoriteToggleAndPersistence(t *testing.T) {
	resetTVLibrary(t)
	tvSetChannels([]TVChannel{
		{Name: "BBC One", URL: "http://x/1", Group: "News"},
		{Name: "Eurosport", URL: "http://x/3", Group: "Sports"},
	})
	focusedFileIndex = 1

	ch, nowFav, ok := tvToggleFavoriteFocused()
	if !ok || !nowFav || ch.Name != "Eurosport" {
		t.Fatalf("toggle = (%+v, %v, %v), want Eurosport favorited", ch, nowFav, ok)
	}
	if err := saveTVFavorites(); err != nil {
		t.Fatalf("saveTVFavorites: %v", err)
	}
	data, err := os.ReadFile(tvFavoritesFilePath())
	if err != nil || len(data) == 0 {
		t.Fatalf("favorites file not written: %v", err)
	}

	tvSetCategory(tvFavoritesCategory)
	if got := tvFilteredChannels(); len(got) != 1 || got[0].Name != "Eurosport" {
		t.Fatalf("Favorites = %+v, want just Eurosport", got)
	}

	// Simulate the next launch: drop the in-memory stars and reload from disk.
	tvLib.mu.Lock()
	tvLib.favs, tvLib.favsRead = nil, false
	tvLib.mu.Unlock()
	if got := tvFavoriteCount(); got != 1 {
		t.Fatalf("reloaded favorite count = %d, want 1", got)
	}
	focusedFileIndex = 0
	if _, nowFav, ok := tvToggleFavoriteFocused(); !ok || nowFav {
		t.Fatalf("second toggle = (%v, %v), want removed", nowFav, ok)
	}
	if err := saveTVFavorites(); err != nil {
		t.Fatalf("saveTVFavorites after removal: %v", err)
	}
	if got := len(tvFilteredChannels()); got != 0 {
		t.Fatalf("Favorites after removal shows %d channels", got)
	}
}

// TestTVFavoritesSurvivePlaylistReload: a starred channel is kept by URL even
// if the display name changes, and a vanished group falls back to All.
func TestTVFavoritesSurvivePlaylistReload(t *testing.T) {
	resetTVLibrary(t)
	tvSetChannels([]TVChannel{{Name: "Old Name", URL: "http://x/9", Group: "News"}})
	tvSetCategory("News")
	focusedFileIndex = 0
	if _, nowFav, ok := tvToggleFavoriteFocused(); !ok || !nowFav {
		t.Fatal("could not favorite the channel")
	}
	if err := saveTVFavorites(); err != nil {
		t.Fatalf("saveTVFavorites: %v", err)
	}

	// The next playlist has the same URL in another group and no News group.
	tvSetChannels([]TVChannel{{Name: "New Name", URL: "http://x/9", Group: "World"}})
	if got := tvCategory(); got != tvAllCategory {
		t.Fatalf("category %q after its group vanished, want All", got)
	}
	tvSetCategory(tvFavoritesCategory)
	got := tvFilteredChannels()
	if len(got) != 1 || got[0].Name != "New Name" {
		t.Fatalf("Favorites after reload = %+v, want the reloaded channel", got)
	}
}

// TestTVSyncClampsSelection: filtering a long list must not leave the cursor
// past the end.
func TestTVSyncClampsSelection(t *testing.T) {
	resetTVLibrary(t)
	tvSetChannels([]TVChannel{
		{Name: "One", URL: "http://x/1", Group: "News"},
		{Name: "Two", URL: "http://x/2", Group: "News"},
	})
	focusedFileIndex = 7
	tvSync()
	if focusedFileIndex != 1 {
		t.Fatalf("focusedFileIndex = %d after sync, want 1", focusedFileIndex)
	}
	tvSetChannels(nil)
	tvSync()
	if focusedFileIndex != 0 {
		t.Fatalf("focusedFileIndex = %d on an empty list, want 0", focusedFileIndex)
	}
}

// TestTVFilterBarLabel: the bar reports the visible count and, when filtered,
// how many channels exist in total.
func TestTVFilterBarLabel(t *testing.T) {
	resetTVLibrary(t)
	tvSetChannels([]TVChannel{
		{Name: "One", URL: "http://x/1", Group: "News"},
		{Name: "Two", URL: "http://x/2", Group: "Sports"},
	})
	if got := tvBarLabel(); got != "All channels  2" {
		t.Fatalf("label = %q, want %q", got, "All channels  2")
	}
	tvSetCategory("Sports")
	if got := tvBarLabel(); got != "Sports  1/2" {
		t.Fatalf("label = %q, want %q", got, "Sports  1/2")
	}
}

// TestTVSearchKeyboardTarget: the on-screen keyboard edits the channel query
// and the list filters live while typing.
func TestTVSearchKeyboardTarget(t *testing.T) {
	resetTVLibrary(t)
	tvSetChannels([]TVChannel{
		{Name: "BBC One", URL: "http://x/1", Group: "News"},
		{Name: "Eurosport", URL: "http://x/3", Group: "Sports"},
	})
	prevTarget := vkTarget
	vkTarget = vkTargetTV
	t.Cleanup(func() { vkTarget = prevTarget })

	vkInsertText("euro", nil)
	if got := tvQuery(); got != "euro" {
		t.Fatalf("query = %q, want euro", got)
	}
	if got := tvFilteredChannels(); len(got) != 1 || got[0].Name != "Eurosport" {
		t.Fatalf("filtered = %+v, want just Eurosport", got)
	}
	if got := vkCurrentText(); got != "euro" {
		t.Fatalf("keyboard buffer = %q, want euro", got)
	}
	vkBackspace(nil)
	if got := vkCurrentText(); got != "eur" {
		t.Fatalf("after backspace buffer = %q, want eur", got)
	}
}

// nextCustomEntry reads the most recent published dynamic-list payload.
func nextCustomEntry(t *testing.T) []FileEntry {
	t.Helper()
	select {
	case u := <-customCh:
		if u.key != tvEntriesVar {
			t.Fatalf("published %q, want %q", u.key, tvEntriesVar)
		}
		entries, ok := u.val.([]FileEntry)
		if !ok {
			t.Fatalf("published payload %T, want []FileEntry", u.val)
		}
		return entries
	default:
		t.Fatal("nothing was published for the channel list")
		return nil
	}
}

// TestTVSyncPublishesFilteredEntries: the list the scene renders and clicks is
// the filtered library, and playlist URLs are passed through untouched (many
// live streams have no file extension at all).
func TestTVSyncPublishesFilteredEntries(t *testing.T) {
	resetTVLibrary(t)
	tvSetChannels([]TVChannel{
		{Name: "BBC One", URL: "http://host:8080/live/1", Group: "News"},
		{Name: "Two", URL: "http://x/2.m3u8?token=abc", Group: "Sports"},
	})
	drainCustomForTest()

	tvSetCategory("Sports")
	tvSync()
	entries := nextCustomEntry(t)
	if len(entries) != 1 || entries[0].Name != "Two" || entries[0].Path != "http://x/2.m3u8?token=abc" {
		t.Fatalf("Sports entries = %+v, want just Two", entries)
	}

	tvSetCategory(tvAllCategory)
	tvSync()
	entries = nextCustomEntry(t)
	if len(entries) != 2 || entries[0].Path != "http://host:8080/live/1" {
		t.Fatalf("All entries = %+v, want both channels in playlist order", entries)
	}
}

// TestTVLiveListRendersBelowFilterBar draws the shipped Live TV element offscreen
// and checks the pixels: the filter bar occupies the top of the panel, the first
// channel row starts below it, and the two are separate bands.
func TestTVLiveListRendersBelowFilterBar(t *testing.T) {
	resetTVLibrary(t)
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	withScreen(t, probeW, probeH)
	ClearTextCache()
	t.Cleanup(func() {
		ClearTextCache()
		focusedFileIndex = 0
	})

	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	idx := findSceneIndex(config, "LiveTV")
	if idx < 0 {
		t.Fatal("LiveTV scene missing")
	}
	element, ok := sceneDynamicListElement(config.Scenes[idx])
	if !ok {
		t.Fatal("LiveTV has no dynamiclist")
	}
	prevScene := currentSceneIndex
	currentSceneIndex = idx
	prevRects := [3]sdl.Rect{tvPrevRect, tvNextRect, tvSearchRect}
	t.Cleanup(func() {
		currentSceneIndex = prevScene
		tvPrevRect, tvNextRect, tvSearchRect = prevRects[0], prevRects[1], prevRects[2]
	})

	tvSetChannels([]TVChannel{
		{Name: "BBC One", URL: "http://x/1.m3u8", Group: "News"},
		{Name: "Eurosport", URL: "http://x/2.m3u8", Group: "Sports"},
	})
	tvSetCategory(tvAllCategory)
	drainCustomForTest()
	tvSync()
	entries := nextCustomEntry(t)
	config.Variables.Custom[tvEntriesVar] = entries
	focusedFileIndex = 0

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderDynamicList(renderer, config, element)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}

	if tvPrevRect.W <= 0 || tvNextRect.W <= 0 || tvSearchRect.W <= 0 {
		t.Fatalf("filter bar widgets were not placed: prev=%+v next=%+v search=%+v", tvPrevRect, tvNextRect, tvSearchRect)
	}
	barPx := f.at(element.X+30, element.Y+30)
	gapY := element.Y + dynamicListListOffset(element) - 4
	gapPx := f.at(element.X+30, gapY)
	rowPx := f.at(element.X+30, element.Y+dynamicListListOffset(element)+20)
	if barPx == gapPx {
		t.Fatalf("filter bar is invisible: bar and gap are both %v", barPx)
	}
	if rowPx == gapPx {
		t.Fatalf("no channel row below the filter bar (row and gap are both %v)", rowPx)
	}
}

// TestDynamicListRowMapSharesFilterBarGeometry: clicks on the Live TV filter bar
// must not play the channel that would sit there without the bar, and the last
// visible row must still be reachable.
func TestDynamicListRowMapSharesFilterBarGeometry(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	idx := findSceneIndex(config, "LiveTV")
	if idx < 0 {
		t.Fatal("LiveTV scene missing from jukaconfig.json")
	}
	element, ok := sceneDynamicListElement(config.Scenes[idx])
	if !ok || element.Variable != tvEntriesVar {
		t.Fatalf("LiveTV dynamiclist = %+v, %v", element, ok)
	}

	offset := dynamicListListOffset(element)
	if offset != 8+tvFilterBarH+6 {
		t.Fatalf("list offset = %d, want %d below the filter bar", offset, 8+tvFilterBarH+6)
	}
	maxVisible := dynamicListMaxVisible(element)
	top := element.Y + offset

	if row := dynamicListRowAt(element, 0, maxVisible, element.Y+8+tvFilterBarH/2); row != -1 {
		t.Fatalf("click inside the filter bar selected row %d, want none", row)
	}
	if row := dynamicListRowAt(element, 0, maxVisible, top-4); row != -1 {
		t.Fatalf("click above the first row selected row %d, want none", row)
	}
	if row := dynamicListRowAt(element, 0, maxVisible, top+2); row != 0 {
		t.Fatalf("click on the first row selected %d, want 0", row)
	}
	if row := dynamicListRowAt(element, 0, maxVisible, top+dynamicListRowH+2); row != 1 {
		t.Fatalf("click on the second row selected %d, want 1", row)
	}
	last := maxVisible - 1
	if row := dynamicListRowAt(element, 0, maxVisible, top+int32(last)*dynamicListRowH+2); row != last {
		t.Fatalf("click on the last visible row selected %d, want %d", row, last)
	}
	if row := dynamicListRowAt(element, 0, maxVisible, top+int32(maxVisible)*dynamicListRowH+4); row != -1 {
		t.Fatalf("click below the list selected row %d, want none", row)
	}
}

// TestTVFavoritesOutliveThePlaylist keeps starred channels reachable after the
// playlist that contained them is gone: the Favorites category lists them from
// the star store and keeps enough metadata to render and play them.
func TestTVFavoritesOutliveThePlaylist(t *testing.T) {
	resetTVLibrary(t)
	tvSetChannels([]TVChannel{{Name: "BBC One", URL: "http://x/1", Group: "News"}})
	focusedFileIndex = 0
	if _, nowFav, ok := tvToggleFavoriteFocused(); !ok || !nowFav {
		t.Fatal("could not favorite the channel")
	}
	if err := saveTVFavorites(); err != nil {
		t.Fatalf("saveTVFavorites: %v", err)
	}

	tvSetChannels(nil) // every playlist file was removed
	tvSetCategory(tvFavoritesCategory)
	got := tvFilteredChannels()
	if len(got) != 1 || got[0].Name != "BBC One" || got[0].URL != "http://x/1" {
		t.Fatalf("Favorites without a playlist = %+v, want the starred URL", got)
	}
	if group, fav := tvRowMeta("http://x/1"); !fav || group != "News" {
		t.Fatalf("row meta = (%q, %v), want News, true", group, fav)
	}
	tvSetQuery("bbc")
	if got := len(tvFilteredChannels()); got != 1 {
		t.Fatalf("search over starred-only channels shows %d, want 1", got)
	}
	tvSetQuery("zzz")
	if got := len(tvFilteredChannels()); got != 0 {
		t.Fatalf("non-matching search over favorites shows %d, want 0", got)
	}
}

// TestTVLiveKeyAndControllerRouting: Left/Right cycle categories, X/Y are Live
// TV-only shortcuts, and every other key falls through to the list handler.
func TestTVLiveKeyAndControllerRouting(t *testing.T) {
	resetTVLibrary(t)
	tvSetChannels([]TVChannel{
		{Name: "One", URL: "http://x/1", Group: "News"},
		{Name: "Two", URL: "http://x/2", Group: "Sports"},
	})
	prevScene := currentSceneIndex
	currentSceneIndex = 0
	prevRects := [3]sdl.Rect{tvPrevRect, tvNextRect, tvSearchRect}
	config := &Config{Scenes: []SceneConfig{{Name: "LiveTV"}}}
	t.Cleanup(func() {
		currentSceneIndex = prevScene
		tvPrevRect, tvNextRect, tvSearchRect = prevRects[0], prevRects[1], prevRects[2]
	})

	if !tvListKeyAction(sdl.K_RIGHT, config) {
		t.Fatal("Right was not handled on the channel list")
	}
	if got := tvCategory(); got != tvFavoritesCategory {
		t.Fatalf("Right from All = %q, want Favorites", got)
	}
	if !tvListKeyAction(sdl.K_RIGHT, config) {
		t.Fatal("second Right was not handled")
	}
	if got := tvCategory(); got != "News" {
		t.Fatalf("second Right = %q, want News", got)
	}
	if !tvListKeyAction(sdl.K_LEFT, config) || tvCategory() != tvFavoritesCategory {
		t.Fatalf("Left = %q, want back to Favorites", tvCategory())
	}
	if tvListKeyAction(sdl.K_RETURN, config) || tvListKeyAction(sdl.K_UP, config) {
		t.Fatal("list keys were swallowed by the Live TV shortcut handler")
	}

	if !tvListControllerAction(sdl.CONTROLLER_BUTTON_DPAD_RIGHT, config) {
		t.Fatal("D-pad Right was not handled")
	}
	if got := tvCategory(); got != "News" {
		t.Fatalf("D-pad Right = %q, want News", got)
	}
	if tvListControllerAction(sdl.CONTROLLER_BUTTON_A, config) ||
		tvListControllerAction(sdl.CONTROLLER_BUTTON_DPAD_UP, config) {
		t.Fatal("controller list buttons were swallowed by the Live TV shortcut handler")
	}
}

// TestTVFilterBarMouseTargets: the category arrows are touch targets, but only
// while the Live TV list is actually on screen.
func TestTVFilterBarMouseTargets(t *testing.T) {
	resetTVLibrary(t)
	tvSetChannels([]TVChannel{
		{Name: "One", URL: "http://x/1", Group: "News"},
		{Name: "Two", URL: "http://x/2", Group: "Sports"},
	})
	prevScene := currentSceneIndex
	prevRects := [3]sdl.Rect{tvPrevRect, tvNextRect, tvSearchRect}
	config := &Config{Scenes: []SceneConfig{{Name: "LiveTV"}, {Name: "Home"}}}
	currentSceneIndex = 0
	tvPrevRect = sdl.Rect{X: 40, Y: 89, W: 28, H: 28}
	tvNextRect = sdl.Rect{X: 240, Y: 89, W: 28, H: 28}
	tvSearchRect = sdl.Rect{X: 990, Y: 86, W: 240, H: 34}
	t.Cleanup(func() {
		currentSceneIndex = prevScene
		tvPrevRect, tvNextRect, tvSearchRect = prevRects[0], prevRects[1], prevRects[2]
	})

	if !handleTVFilterMouse(tvNextRect.X+4, tvNextRect.Y+4, config) {
		t.Fatal("click on the next-category arrow was ignored")
	}
	if got := tvCategory(); got != tvFavoritesCategory {
		t.Fatalf("next arrow = %q, want Favorites", got)
	}
	if !handleTVFilterMouse(tvPrevRect.X+4, tvPrevRect.Y+4, config) {
		t.Fatal("click on the previous-category arrow was ignored")
	}
	if got := tvCategory(); got != tvAllCategory {
		t.Fatalf("previous arrow = %q, want All", got)
	}
	if handleTVFilterMouse(5, 5, config) {
		t.Fatal("click outside the bar was swallowed")
	}

	// A stale bar from the previous screen must not eat clicks on Home.
	currentSceneIndex = 1
	if handleTVFilterMouse(tvNextRect.X+4, tvNextRect.Y+4, config) {
		t.Fatal("filter bar reacted on a scene that does not draw it")
	}
}

// TestLoadTVChannelsFromDir merges playlist files in name order and dedupes
// across them.
func TestLoadTVChannelsFromDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.m3u"), []byte(sampleM3U), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.m3u8"), []byte("#EXTM3U\n#EXTINF:-1 group-title=\"News\",A One\nhttp://a.example/1.m3u8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(sampleM3U), 0o644); err != nil {
		t.Fatal(err)
	}
	chans := loadTVChannelsFromDir(dir)
	if len(chans) != 5 {
		t.Fatalf("merged %d channels, want 5 (1 + 4, txt ignored): %+v", len(chans), chans)
	}
	if chans[0].Name != "A One" {
		t.Fatalf("first channel = %q, want A One (a.m3u8 sorts first)", chans[0].Name)
	}
}

// TestTVCountsFollowFilterChanges guards the cached count: the filter bar must
// still show a new number after a query, a category switch or a reload.
func TestTVCountsFollowFilterChanges(t *testing.T) {
	resetTVLibrary(t)
	tvSetChannels([]TVChannel{
		{Name: "One", URL: "http://x/1", Group: "News"},
		{Name: "Two", URL: "http://x/2", Group: "Sports"},
		{Name: "Three", URL: "http://x/3", Group: "Sports"},
	})
	if visible, total := tvCounts(); visible != 3 || total != 3 {
		t.Fatalf("counts = %d/%d, want 3/3", visible, total)
	}
	tvSetCategory("Sports")
	if visible, total := tvCounts(); visible != 2 || total != 3 {
		t.Fatalf("after category: counts = %d/%d, want 2/3", visible, total)
	}
	tvSetQuery("three")
	if visible, total := tvCounts(); visible != 1 || total != 3 {
		t.Fatalf("after query: counts = %d/%d, want 1/3", visible, total)
	}
	tvSetQuery("")
	tvSetCategory(tvAllCategory)
	if visible, total := tvCounts(); visible != 3 || total != 3 {
		t.Fatalf("after reset: counts = %d/%d, want 3/3", visible, total)
	}
}
