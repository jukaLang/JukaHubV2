package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// homeSearchTestScreens is the catalog as screen rows with inert actions, so
// the ranking can be tested without a config, the Start menu or a scene change.
func homeSearchTestScreens() []homeSearchScreen {
	var out []homeSearchScreen
	for _, e := range homeSearchCatalog() {
		out = append(out, homeSearchScreen{title: e.title, where: e.where, icon: e.icon, keys: e.keys, scene: e.scene, action: func(*Config) {}})
	}
	return out
}

// homeSearchFirstIn returns the first result of a group.
func homeSearchFirstIn(results []gsResult, group string) (gsResult, bool) {
	for _, r := range results {
		if r.Category == group {
			return r, true
		}
	}
	return gsResult{}, false
}

// TestHomeSearchFindsScreensByTheirWords pins the synonyms the request named:
// "wifi" is the Wi-Fi screen, "ftp" the Network tools, "theme" the theme
// presets — and spelling or punctuation does not matter.
func TestHomeSearchFindsScreensByTheirWords(t *testing.T) {
	src := homeSearchSources{screens: homeSearchTestScreens(), online: true}
	cases := []struct {
		query, want string
	}{
		{"wifi", "Wi-Fi"},
		{"Wi-Fi", "Wi-Fi"},
		{"wi fi", "Wi-Fi"},
		{"ftp", "Network tools"},
		{"ssh", "Network tools"},
		{"theme", "Theme presets"},
		{"dark mode", "Appearance"},
		{"keyboard", "Keyboard settings"},
		{"cron", "Scheduled tasks"},
		{"downloads", "Downloads"},
		{"text size", "Accessibility"},
		{"set", "Settings"},
	}
	for _, tc := range cases {
		results := homeSearchResults(tc.query, src)
		first, ok := homeSearchFirstIn(results, homeSearchGroupScreens)
		if !ok {
			t.Errorf("%q found no screen, want %q", tc.query, tc.want)
			continue
		}
		if first.Title != tc.want {
			t.Errorf("%q: first screen = %q, want %q", tc.query, first.Title, tc.want)
		}
	}
}

// TestHomeSearchMatchLadder keeps the ranking's order: an exact title beats a
// prefix, which beats a word match, a synonym and a substring; nothing scores
// for an unrelated query.
func TestHomeSearchMatchLadder(t *testing.T) {
	q := newHomeSearchQuery("files")
	exact := homeSearchMatch(q, "Files", "")
	prefix := homeSearchMatch(newHomeSearchQuery("fil"), "Files", "")
	word := homeSearchMatch(newHomeSearchQuery("man"), "File manager", "")
	synonym := homeSearchMatch(newHomeSearchQuery("explorer"), "Files", "explorer folders")
	substr := homeSearchMatch(newHomeSearchQuery("anager"), "File manager", "")
	none := homeSearchMatch(newHomeSearchQuery("zzz"), "Files", "explorer")
	if !(exact > prefix && prefix > word && word > synonym && synonym > substr && substr > 0) {
		t.Fatalf("ladder out of order: exact %d prefix %d word %d synonym %d substring %d", exact, prefix, word, synonym, substr)
	}
	if none != 0 {
		t.Fatalf("an unrelated query scored %d", none)
	}
	if got := homeSearchMatch(newHomeSearchQuery(""), "Files", ""); got != 0 {
		t.Fatalf("an empty query scored %d", got)
	}
}

// TestHomeSearchGroupsComeInOrder checks the sheet's group order and caps:
// screens, apps & games, files, media history, then the web row last.
func TestHomeSearchGroupsComeInOrder(t *testing.T) {
	src := homeSearchSources{
		screens: homeSearchTestScreens(),
		apps:    []FileEntry{{Name: "Music Maker", Path: "/apps/musicmaker.sh"}, {Name: "JukaLand (built-in)", Path: builtinScenePrefix + "JukaLand"}},
		games:   []recentGame{{Path: "/roms/gba/music quest.gba", System: "GBA", Title: "Music Quest"}},
		files: []homeSearchFile{
			{Name: "music.mp3", Path: "/sd/Music/music.mp3", Parent: "Music", fold: "music mp3", compact: "musicmp3"},
		},
		recent: []FavoriteItem{{Type: "audio", Data: FileEntry{Name: "Music for Airports", Path: "/sd/Music/airports.flac"}, Timestamp: time.Now()}},
		online: true,
	}
	results := homeSearchResults("music", src)
	order := []string{homeSearchGroupScreens, homeSearchGroupApps, homeSearchGroupFiles, homeSearchGroupMedia, homeSearchGroupWeb}
	seen := map[string]int{}
	last := -1
	for _, r := range results {
		idx := -1
		for i, g := range order {
			if g == r.Category {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatalf("unexpected group %q", r.Category)
		}
		if idx < last {
			t.Fatalf("group %q came after a later group", r.Category)
		}
		last = idx
		seen[r.Category]++
	}
	for _, g := range order {
		if seen[g] == 0 {
			t.Errorf("no %q rows for a query every source matches", g)
		}
	}
	if seen[homeSearchGroupScreens] > homeSearchMaxScreens || seen[homeSearchGroupApps] > homeSearchMaxApps {
		t.Errorf("groups exceed their caps: %v", seen)
	}
	if web := results[len(results)-1]; web.Category != homeSearchGroupWeb || !strings.Contains(web.Title, "music") {
		t.Errorf("last row = %+v, want the web search for the query", web)
	}
}

// TestHomeSearchJukaBadgeMarksBuiltinApps: JukaHub's own apps carry the badge,
// installed programs do not.
func TestHomeSearchJukaBadgeMarksBuiltinApps(t *testing.T) {
	src := homeSearchSources{
		apps: []FileEntry{
			{Name: "JukaLand (built-in)", Path: builtinScenePrefix + "JukaLand"},
			{Name: "Jukebox", Path: "/apps/jukebox.sh"},
		},
		online: true,
	}
	results := homeSearchResults("juk", src)
	badges := map[string]string{}
	for _, r := range results {
		if r.Category == homeSearchGroupApps {
			badges[r.Title] = r.Badge
		}
	}
	if badges["JukaLand"] != "Juka" {
		t.Errorf("JukaLand badge = %q, want Juka (results %+v)", badges["JukaLand"], results)
	}
	if b, ok := badges["Jukebox"]; !ok || b != "" {
		t.Errorf("an installed app got badge %q (present %v)", b, ok)
	}
}

// TestHomeSearchWebRowNeedsInternet: the web row is always last; offline it is
// dimmed with the "Needs internet" badge and a press explains instead of
// opening the Browser.
func TestHomeSearchWebRowNeedsInternet(t *testing.T) {
	for _, online := range []bool{true, false} {
		results := homeSearchResults("cats", homeSearchSources{online: online})
		if len(results) == 0 {
			t.Fatalf("online=%v: no web row", online)
		}
		web := results[len(results)-1]
		if web.Category != homeSearchGroupWeb || web.Icon != "globe" {
			t.Fatalf("online=%v: last row %+v is not the web search", online, web)
		}
		if online && (web.Dimmed || web.Blocked != "") {
			t.Errorf("online: web row is dimmed/blocked: %+v", web)
		}
		if !online && (!web.Dimmed || web.Badge != homeSearchNeedsInternet || web.Blocked == "") {
			t.Errorf("offline: web row %+v is not dimmed with the Needs internet badge", web)
		}
	}
	// An address gets an "Open" row before the search row.
	results := homeSearchResults("github.com", homeSearchSources{online: true})
	if len(results) < 2 || !strings.HasPrefix(results[len(results)-2].Title, "Open github.com") {
		t.Errorf("address query rows = %+v, want Open github.com before the search", results)
	}
}

func TestHomeSearchWebURLAndAddresses(t *testing.T) {
	if got, want := homeSearchWebURL(" cats & dogs "), browserSearchURL+"cats+%26+dogs"; got != want {
		t.Errorf("homeSearchWebURL = %q, want %q", got, want)
	}
	cases := map[string]bool{
		"github.com":          true,
		"https://example.org": true,
		"news.ycombinator":    true,
		"cats":                false,
		"cats and dogs":       false,
		"v1.2":                false,
		"":                    false,
		"trailing.":           false,
	}
	for in, want := range cases {
		if got := homeSearchLooksLikeURL(in); got != want {
			t.Errorf("homeSearchLooksLikeURL(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestHomeSearchStartPage: before anything is typed the sheet lists recent
// searches (which refill the field), what was playing and the quick screens.
func TestHomeSearchStartPage(t *testing.T) {
	src := homeSearchSources{
		screens: homeSearchTestScreens(),
		history: []string{"wifi", "zelda"},
		recent:  []FavoriteItem{{Type: "video", Data: VideoInfo{Title: "Big Buck Bunny", URL: "/sd/Videos/bbb.mp4"}, Timestamp: time.Now()}},
		online:  true,
	}
	results := homeSearchResults("", src)
	if len(results) < 4 {
		t.Fatalf("start page has %d rows: %+v", len(results), results)
	}
	if r := results[0]; r.Category != homeSearchGroupRecent || !r.Keep || r.Fill != "wifi" {
		t.Errorf("first row = %+v, want the recent search 'wifi' that refills the field", r)
	}
	if _, ok := homeSearchFirstIn(results, homeSearchGroupMedia); !ok {
		t.Error("the start page does not offer what was playing")
	}
	if r, ok := homeSearchFirstIn(results, homeSearchGroupScreens); !ok || r.Action == nil {
		t.Error("the start page has no quick screens")
	}
	for _, r := range results {
		if r.Category == homeSearchGroupWeb {
			t.Error("the start page offers a web search for an empty query")
		}
	}
}

// TestHomeSearchFileScanIsBounded: the file index skips hidden and system
// folders, honours the depth limit and stops at the count limit.
func TestHomeSearchFileScanIsBounded(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("Music/song.mp3")
	mk("Music/.hidden.mp3")
	mk(".secret/notes.txt")
	mk("cache/blob.bin")
	mk("a/b/c/d/e/f/deep.txt")
	files := homeSearchScanFiles([]string{root}, 100, 3, time.Second)
	names := map[string]bool{}
	for _, f := range files {
		names[f.Name] = true
		if f.fold == "" || f.compact == "" {
			t.Errorf("%s has no folded name", f.Path)
		}
	}
	if !names["song.mp3"] || !names["Music"] {
		t.Errorf("visible entries missing: %v", names)
	}
	for _, hidden := range []string{".hidden.mp3", "notes.txt", "blob.bin", "deep.txt"} {
		if names[hidden] {
			t.Errorf("%s was indexed: %v", hidden, names)
		}
	}
	if got := homeSearchScanFiles([]string{root}, 2, 3, time.Second); len(got) > 2 {
		t.Errorf("count limit 2 returned %d entries", len(got))
	}
}

// homeSearchTestSession opens the sheet's state without the background scans
// or the Start menu, and restores everything after the test.
func homeSearchTestSession(t *testing.T) {
	t.Helper()
	prevOpen, prevQuery, prevCursor, prevResults := globalSearchOpen, gsQuery, gsCursor, gsResults
	prevSession := homeSearch
	prevVK, prevTarget := virtualKeyboardActive, vkTarget
	t.Cleanup(func() {
		globalSearchOpen, gsQuery, gsCursor, gsResults = prevOpen, prevQuery, prevCursor, prevResults
		homeSearch = prevSession
		virtualKeyboardActive, vkTarget = prevVK, prevTarget
	})
	globalSearchOpen = true
	gsQuery, gsCursor, gsResults = "", 0, nil
	homeSearch = homeSearchSession{screens: homeSearchTestScreens()}
	virtualKeyboardActive = false
}

// TestHomeSearchTypingFiltersAndKeysNavigate drives the sheet the way a
// keyboard does: text narrows the results, arrows move, Escape closes — and a
// typed "b" is a letter (the old overlay closed on it).
func TestHomeSearchTypingFiltersAndKeysNavigate(t *testing.T) {
	homeSearchTestSession(t)

	handleGlobalSearchTextInput("wif")
	handleGlobalSearchTextInput("i")
	if gsQuery != "wifi" {
		t.Fatalf("query = %q, want wifi", gsQuery)
	}
	if len(gsResults) == 0 || gsResults[0].Title != "Wi-Fi" {
		t.Fatalf("first result for wifi = %+v", gsResults)
	}
	handleGlobalSearchTextInput("b")
	if !globalSearchOpen || gsQuery != "wifib" {
		t.Fatalf("typing b closed the sheet or was dropped: open=%v query=%q", globalSearchOpen, gsQuery)
	}
	key := func(sym sdl.Keycode) {
		handleGSInput(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}}, &Config{})
	}
	key(sdl.K_BACKSPACE)
	if gsQuery != "wifi" {
		t.Fatalf("Backspace left %q", gsQuery)
	}
	gsCursor = 0
	key(sdl.K_DOWN)
	if gsCursor != 1 && len(gsResults) > 1 {
		t.Fatalf("Down moved the cursor to %d", gsCursor)
	}
	key(sdl.K_UP)
	key(sdl.K_UP)
	if gsCursor != -1 {
		t.Fatalf("Up past the first row = %d, want -1 (the field)", gsCursor)
	}
	key(sdl.K_ESCAPE)
	if globalSearchOpen {
		t.Fatal("Escape did not close the sheet")
	}
}

// TestHomeSearchPadDefersToItsKeyboard: while the on-screen keyboard types
// into the query, the sheet declines pad presses so the main loop gives them
// to the keyboard; with the keyboard closed, B closes the sheet.
func TestHomeSearchPadDefersToItsKeyboard(t *testing.T) {
	homeSearchTestSession(t)
	press := func(b uint8) bool {
		return homeSearchController(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: b}, &Config{})
	}
	virtualKeyboardActive, vkTarget = true, homeSearchVKTarget
	if press(sdl.CONTROLLER_BUTTON_DPAD_DOWN) {
		t.Fatal("the sheet took a press while its keyboard was up")
	}
	if got := vkCurrentText(); got != gsQuery {
		t.Fatalf("keyboard reads %q, want the query %q", got, gsQuery)
	}
	vkSetText("ftp", &Config{})
	if gsQuery != "ftp" || len(gsResults) == 0 || gsResults[0].Title != "Network tools" {
		t.Fatalf("keyboard text did not search: query %q results %+v", gsQuery, gsResults)
	}
	virtualKeyboardActive = false
	if !press(sdl.CONTROLLER_BUTTON_B) || globalSearchOpen {
		t.Fatal("B did not close the sheet")
	}
}

// TestHomeSearchGroupJumps: L1/R1 (and Page keys) hop between group starts.
func TestHomeSearchGroupJumps(t *testing.T) {
	homeSearchTestSession(t)
	gsResults = []gsResult{
		{Title: "a", Category: homeSearchGroupScreens},
		{Title: "b", Category: homeSearchGroupScreens},
		{Title: "c", Category: homeSearchGroupApps},
		{Title: "d", Category: homeSearchGroupApps},
		{Title: "e", Category: homeSearchGroupWeb},
	}
	gsCursor = 0
	homeSearchJumpGroup(1)
	if gsCursor != 2 {
		t.Fatalf("R1 from the first group = %d, want 2", gsCursor)
	}
	homeSearchJumpGroup(1)
	if gsCursor != 4 {
		t.Fatalf("R1 again = %d, want 4", gsCursor)
	}
	gsCursor = 3
	homeSearchJumpGroup(-1)
	if gsCursor != 2 {
		t.Fatalf("L1 inside a group = %d, want its start 2", gsCursor)
	}
	homeSearchJumpGroup(-1)
	if gsCursor != 0 {
		t.Fatalf("L1 from a group start = %d, want the previous group's start 0", gsCursor)
	}
}

// TestHomeSearchBlockedRowExplains: a press on a dimmed row keeps the sheet
// open instead of running the action.
func TestHomeSearchBlockedRowExplains(t *testing.T) {
	homeSearchTestSession(t)
	clearToastAfterTest(t)
	ran := false
	gsResults = []gsResult{{Title: "Live TV", Blocked: "Offline — Live TV needs WiFi", Action: func(*Config) { ran = true }}}
	homeSearchActivate(&Config{}, 0)
	if ran || !globalSearchOpen {
		t.Fatalf("a blocked row ran its action (%v) or closed the sheet (open %v)", ran, globalSearchOpen)
	}
	gsResults = []gsResult{{Title: "Wi-Fi", Action: func(*Config) { ran = true }}}
	gsQuery = "wifi"
	homeSearchActivate(&Config{}, 0)
	if !ran || globalSearchOpen {
		t.Fatalf("a normal row did not run (%v) or left the sheet open (%v)", ran, globalSearchOpen)
	}
	if len(homeSearch.history) == 0 || homeSearch.history[0] != "wifi" {
		t.Errorf("the query was not remembered: %v", homeSearch.history)
	}
}
