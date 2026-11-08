package main

// media_hub_test.go — the Media screen without a network: the tab strip, the
// details sheet's actions, recent searches, the offline search, empty states
// the pad must be able to reach, click targets cut to the visible list, the
// confirmation that guards a deletion, the rebuild keeping the focused row,
// and every footer row on the compact canvas.

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// mediaHubTestState gives one test the Media screen's state to itself: the
// screen, the Library's open lists, the last Favorites removal and a fresh
// focus engine (no header button holding the focus).
func mediaHubTestState(t *testing.T) {
	t.Helper()
	saved, savedClip := mediaHub, mediaHitClip
	savedNav, savedUndo, savedEngine := mlibNav, mediaFavUndo, focusEngine
	t.Cleanup(func() {
		mediaHub = saved
		mediaHitClip = savedClip
		mlibNav, mediaFavUndo, focusEngine = savedNav, savedUndo, savedEngine
	})
	mlibNav.stack = nil
	mlibNav.rowsScroll = 0
	mediaFavUndo.at = time.Time{}
	mediaFavUndo.item = FavoriteItem{}
	focusEngine = NewFocusEngine()
	mediaHub.chromeHeld = false
	mediaHub.tab = mediaTabForYou
	mediaHub.zone = mediaZoneSearch
	mediaHub.tabFocus = 0
	mediaHub.rows = nil
	mediaHub.row = 0
	mediaHub.cards = nil
	mediaHub.index = 0
	mediaHub.scrollY = 0
	mediaHub.chipFocus = -1
	mediaHub.chipN = 0
	mediaHub.hits = nil
	mediaHub.sheet = mediaSheet{}
	mediaHub.confirm = mediaConfirm{}
	mediaHub.hasSearch = false
	mediaHub.searchPending = false
	mediaHub.localSearch = false
	mediaHub.localResults = nil
	mediaHub.recent = nil
	mediaHub.footer = nil
	mediaHub.footerKey = -1
	mediaHitClip = sdl.Rect{}
}

// mediaTestOnline pins the connectivity monitor for one test.
func mediaTestOnline(t *testing.T, online bool) {
	t.Helper()
	netMon.mu.Lock()
	prevOnline, prevChecked := netMon.online, netMon.checked
	netMon.online, netMon.checked = online, true
	netMon.mu.Unlock()
	t.Cleanup(func() {
		netMon.mu.Lock()
		netMon.online, netMon.checked = prevOnline, prevChecked
		netMon.mu.Unlock()
	})
}

// mediaTestTubeConfig is Home plus the Media scene, with Media on screen.
func mediaTestTubeConfig(t *testing.T) *Config {
	t.Helper()
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	config.Scenes = []SceneConfig{
		{Name: "Main", Layout: "home"},
		{Name: mediaHubSceneName, Elements: []Element{
			{Type: "input", Variable: "search_query"},
			{Type: "searchresults", Variable: mediaResultsVar},
			{Type: "video"},
		}},
	}
	prevScene, prevKeyboard, prevActive := currentSceneIndex, virtualKeyboardActive, activeSceneIndex
	currentSceneIndex, virtualKeyboardActive, activeSceneIndex = 1, false, -1
	t.Cleanup(func() {
		currentSceneIndex, virtualKeyboardActive, activeSceneIndex = prevScene, prevKeyboard, prevActive
	})
	return config
}

// mediaTestPlayerPhase sets the player's phase for one test.
func mediaTestPlayerPhase(t *testing.T, phase string) {
	t.Helper()
	embeddedPlayer.mu.Lock()
	prev := embeddedPlayer.phase
	embeddedPlayer.phase = phase
	embeddedPlayer.mu.Unlock()
	t.Cleanup(func() {
		embeddedPlayer.mu.Lock()
		embeddedPlayer.phase = prev
		embeddedPlayer.mu.Unlock()
	})
}

func TestMediaTabsStripAddsResultsAfterASearch(t *testing.T) {
	mediaHubTestState(t)
	tabs := mediaHubTabs()
	if len(tabs) != len(mediaTabOrder) || tabs[0] != mediaTabForYou {
		t.Fatalf("strip before a search = %v", tabs)
	}
	for _, tab := range tabs {
		if tab == mediaTabSearch {
			t.Fatal("the Results chip is shown before any search ran")
		}
	}
	mediaHub.hasSearch = true
	tabs = mediaHubTabs()
	if len(tabs) != len(mediaTabOrder)+1 || tabs[0] != mediaTabSearch {
		t.Fatalf("strip after a search = %v, want Results first", tabs)
	}
	if got := mediaHubTabIndex(mediaTabDownloads); got != 4 {
		t.Fatalf("Downloads chip at %d, want 4 with Results in front", got)
	}
	for _, tab := range tabs {
		if mediaTabLabel(tab) == "" || mediaTabIcon(tab) == "" {
			t.Errorf("tab %d has no label or icon", tab)
		}
	}
	// The Library sits after Favorites, before the two shortcuts, and needs
	// no network.
	lib := mediaHubTabIndex(mediaTabLibrary)
	if lib < 0 || lib != mediaHubTabIndex(mediaTabFavorites)+1 || lib != mediaHubTabIndex(mediaTabLiveTV)-1 {
		t.Fatalf("Library chip at %d, want between Favorites and Live TV", lib)
	}
	if mediaTabLabel(mediaTabLibrary) != "Library" || mediaTabNeedsNetwork(mediaTabLibrary) || mediaTabShortcut(mediaTabLibrary) {
		t.Fatal("the Library chip is not an offline list named Library")
	}
}

// The sheet offers what the card can do: transfers can only be retried or
// cancelled, a file from the library can be removed, a file opened from Files
// cannot, and the network actions disappear offline.
func TestMediaSheetActionsFollowTheCard(t *testing.T) {
	web := mediaCard{title: "Clip", video: VideoInfo{Title: "Clip", WebpageURL: "https://www.youtube.com/watch?v=abcdefghijk", Duration: 100}}
	resumed := web
	resumed.progress = 0.5
	downloaded := web
	downloaded.downloaded = true
	library := mediaCard{title: "Saved", path: "/card/video/Saved.mp4", libID: "id1", downloaded: true}
	foreign := mediaCard{title: "Holiday", path: "/card/Videos/holiday.mp4", downloaded: true}
	failed := mediaCard{title: "Broken", queued: true, queueStatus: downloadFailed}
	running := mediaCard{title: "Busy", queued: true, queueStatus: downloadActive}
	film := mediaCard{title: "Heat", path: "/card/Movies/Heat.mkv", mlibKind: mlibCardVideo,
		video: VideoInfo{Title: "Heat", Duration: 600, URL: "/card/Movies/Heat.mkv"}}
	halfFilm := film
	halfFilm.progress = 0.4
	song := mediaCard{title: "One", path: "/card/Music/A/01 - One.mp3", mlibKind: mlibCardTrack}
	show := mediaCard{title: "The Office", mlibKind: mlibCardShow, mlibKey: "the office"}
	album := mediaCard{title: "Album", mlibKind: mlibCardAlbum, mlibKey: "artist\x00album"}
	seeAll := mediaCard{title: "See all", mlibKind: mlibCardAllMovies}

	cases := []struct {
		name     string
		card     mediaCard
		online   bool
		party    bool
		favorite bool
		want     []mediaSheetAction
	}{
		{"web video online", web, true, false, false,
			[]mediaSheetAction{mediaActPlay, mediaActDownload, mediaActFavorite, mediaActUpNext, mediaActCopy}},
		{"web video offline", web, false, false, false,
			[]mediaSheetAction{mediaActPlay, mediaActFavorite, mediaActCopy}},
		{"resumable with a party", resumed, true, true, true,
			[]mediaSheetAction{mediaActPlay, mediaActPlayStart, mediaActDownload, mediaActFavorite, mediaActUpNext, mediaActParty, mediaActCopy}},
		{"downloaded web video", downloaded, true, false, false,
			[]mediaSheetAction{mediaActPlay, mediaActRemoveDownload, mediaActFavorite, mediaActUpNext, mediaActCopy}},
		{"library file", library, false, false, false,
			[]mediaSheetAction{mediaActPlay, mediaActRemoveDownload, mediaActCopy}},
		{"file opened from Files", foreign, true, false, false,
			[]mediaSheetAction{mediaActPlay, mediaActCopy}},
		{"failed transfer", failed, true, false, false,
			[]mediaSheetAction{mediaActRetry, mediaActCancelDownload}},
		{"running transfer", running, true, false, false,
			[]mediaSheetAction{mediaActCancelDownload}},
		{"library film", film, false, false, false,
			[]mediaSheetAction{mediaActPlay, mediaActCopy}},
		{"library film in progress", halfFilm, true, true, false,
			[]mediaSheetAction{mediaActPlay, mediaActPlayStart, mediaActCopy}},
		{"library song", song, false, false, false,
			[]mediaSheetAction{mediaActPlay, mediaActCopy}},
		{"library show", show, false, false, false,
			[]mediaSheetAction{mediaActOpen, mediaActPlay}},
		{"library album", album, true, false, false,
			[]mediaSheetAction{mediaActOpen, mediaActPlay}},
		{"library see all", seeAll, true, false, false,
			[]mediaSheetAction{mediaActOpen}},
	}
	for _, c := range cases {
		acts, labels := mediaSheetActionsFor(c.card, c.online, c.party, c.favorite, false)
		if len(acts) != len(labels) {
			t.Fatalf("%s: %d actions but %d labels", c.name, len(acts), len(labels))
		}
		same := len(acts) == len(c.want)
		for i := 0; same && i < len(acts); i++ {
			same = acts[i] == c.want[i]
		}
		if !same {
			t.Errorf("%s: actions %v (%q), want %v", c.name, acts, labels, c.want)
		}
		for i, label := range labels {
			if label == "" {
				t.Errorf("%s: action %d has no label", c.name, i)
			}
		}
	}
}

func TestMediaRecentSearchesStayShortAndUnique(t *testing.T) {
	got := mediaPushRecentSearch([]string{"cats", "Dogs", "lofi"}, "  dogs ")
	if len(got) != 3 || got[0] != "dogs" || got[1] != "cats" || got[2] != "lofi" {
		t.Fatalf("push = %q, want dogs first without the older Dogs", got)
	}
	var list []string
	for i := 0; i < 20; i++ {
		list = mediaPushRecentSearch(list, fmt.Sprintf("q%d", i))
	}
	if len(list) != mediaRecentSearchMax || list[0] != "q19" {
		t.Fatalf("after 20 searches: %q, want the newest %d", list, mediaRecentSearchMax)
	}
	if parsed := mediaParseRecentSearches([]interface{}{"a", 3, " ", "b"}); len(parsed) != 2 || parsed[0] != "a" || parsed[1] != "b" {
		t.Fatalf("parsed from JSON = %q, want [a b]", parsed)
	}
	if parsed := mediaParseRecentSearches([]string{"x"}); len(parsed) != 1 || parsed[0] != "x" {
		t.Fatalf("parsed from the session = %q", parsed)
	}
	if parsed := mediaParseRecentSearches(nil); len(parsed) != 0 {
		t.Fatalf("parsed nothing into %q", parsed)
	}
}

func TestMediaProgressFractionHidesFinishedVideos(t *testing.T) {
	for _, c := range []struct {
		pos, dur, want float64
	}{
		{0, 100, 0},
		{50, 100, 0.5},
		{96, 100, 0},
		{10, 0, 0},
	} {
		if got := mediaProgressFraction(c.pos, c.dur); got != c.want {
			t.Errorf("mediaProgressFraction(%v, %v) = %v, want %v", c.pos, c.dur, got, c.want)
		}
	}
}

func TestMediaOfflineSearchLooksThroughDownloads(t *testing.T) {
	entries := []dlLibraryEntry{
		{ID: "1", Title: "Lofi Beats", Channel: "Chill"},
		{ID: "2", Title: "News", Channel: "LOFI radio"},
		{ID: "3", Title: "Cats"},
	}
	got := mediaFilterLibrary(entries, "  lofi ")
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "2" {
		t.Fatalf("offline search = %+v, want the title and the channel match in order", got)
	}
	if got := mediaFilterLibrary(entries, "dogs"); len(got) != 0 {
		t.Fatalf("offline search for dogs = %+v", got)
	}
}

// A card scrolled under the search bar or the tab strip must not take their
// clicks: hits are cut to the visible list.
func TestMediaAddHitClipsToTheVisibleList(t *testing.T) {
	mediaHubTestState(t)
	mediaHitClip = sdl.Rect{X: 0, Y: 100, W: 400, H: 200}
	mediaAddHit(mediaHit{kind: mediaHitCard, b: 1, rect: sdl.Rect{X: 10, Y: 80, W: 100, H: 60}})
	mediaAddHit(mediaHit{kind: mediaHitCard, b: 2, rect: sdl.Rect{X: 10, Y: 10, W: 100, H: 60}})
	if len(mediaHub.hits) != 1 {
		t.Fatalf("recorded %d hits, want only the card that is partly visible", len(mediaHub.hits))
	}
	if r := mediaHub.hits[0].rect; r.Y != 100 || r.H != 40 || r.X != 10 || r.W != 100 {
		t.Fatalf("visible part = %+v, want {10 100 100 40}", r)
	}
	mediaHitClip = sdl.Rect{}
	mediaAddHit(mediaHit{kind: mediaHitTab, rect: sdl.Rect{X: 10, Y: 10, W: 100, H: 60}})
	if len(mediaHub.hits) != 2 || mediaHub.hits[1].rect.Y != 10 {
		t.Fatalf("an unclipped hit was changed: %+v", mediaHub.hits)
	}
}

// The ring never sits on a For you row with nothing in it.
func TestMediaSnapRowSkipsEmptyRows(t *testing.T) {
	mediaHubTestState(t)
	mediaHub.rows = []mediaRow{
		{kind: mediaRowTrending},
		{kind: mediaRowDownloads, cards: []mediaCard{{title: "a"}}},
	}
	mediaHub.row = 0
	mediaHubSnapRow()
	if mediaHub.row != 1 {
		t.Fatalf("ring on row %d, want the first row with cards", mediaHub.row)
	}
	mediaHub.rows[0].cards = []mediaCard{{title: "b"}}
	mediaHub.row = 0
	mediaHubSnapRow()
	if mediaHub.row != 0 {
		t.Fatalf("a row with cards lost the ring to row %d", mediaHub.row)
	}
}

// Every empty state that draws a button can be reached with the pad, and A
// does what the button says.
func TestMediaEmptyStatesAreReachableByPad(t *testing.T) {
	mediaHubTestState(t)
	mediaSearchMu.Lock()
	prevCancel := mediaSearchCancel
	mediaSearchCancel = map[string]context.CancelFunc{}
	mediaSearchMu.Unlock()
	t.Cleanup(func() {
		mediaSearchMu.Lock()
		mediaSearchCancel = prevCancel
		mediaSearchMu.Unlock()
	})
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}

	mediaTestOnline(t, false)
	mediaHub.tab = mediaTabTrending
	if !mediaHubContentFocusable(config) {
		t.Fatal("offline, the Open Downloads button cannot be reached")
	}
	if got := mediaHubStateAction(config, false); got != 0 {
		t.Fatalf("offline Trending: action %d, want 0 (Open Downloads)", got)
	}
	if got := mediaHubStateAction(config, true); got != 1 {
		t.Fatalf("Trending that did not load: action %d, want 1 (Try again)", got)
	}
	mediaSearchMu.Lock()
	mediaSearchCancel[mediaTrendingVar] = func() {}
	mediaSearchMu.Unlock()
	if got := mediaHubStateAction(config, true); got != -1 {
		t.Fatalf("Trending still loading: action %d, want none", got)
	}
	mediaHub.cards = []mediaCard{{title: "x"}}
	if got := mediaHubStateAction(config, true); got != -1 {
		t.Fatalf("a tab with cards: action %d, want none", got)
	}

	mediaHub.cards = nil
	mediaHub.tab = mediaTabHistory
	if got := mediaHubStateAction(config, true); got != -1 || mediaHubContentFocusable(config) {
		t.Fatalf("empty History: action %d, want none and nothing to focus", got)
	}

	mediaHub.tab = mediaTabSearch
	mediaHub.hasSearch = true
	config.Variables.Custom["search_error"] = "boom"
	if got := mediaHubStateAction(config, false); got != -1 {
		t.Fatalf("a failed search: action %d, want a message without a button", got)
	}
	delete(config.Variables.Custom, "search_error")
	if got := mediaHubStateAction(config, false); got != 0 {
		t.Fatalf("an offline search with no downloads to show: action %d, want 0", got)
	}

	mediaHub.tab = mediaTabForYou
	mediaHub.rows = nil
	if got := mediaHubStateAction(config, false); got != 0 {
		t.Fatalf("offline For you with nothing on the card: action %d, want 0", got)
	}
	mediaHub.rows = []mediaRow{{kind: mediaRowDownloads, cards: []mediaCard{{title: "a"}}}}
	if got := mediaHubStateAction(config, false); got != -1 {
		t.Fatalf("offline For you with downloads: action %d, want none", got)
	}
}

// While the confirmation is up it owns every click: outside it cancels, on
// its text nothing happens, and nothing drawn underneath ever runs.
func TestMediaConfirmationOwnsEveryClick(t *testing.T) {
	renderer := newKeyboardTestRenderer(t)
	withScreen(t, 1280, 720)
	t.Cleanup(ClearTextCache)
	config := mediaTestTubeConfig(t)
	mediaHubTestState(t)
	mediaTestPlayerPhase(t, "idle")

	mediaHub.tab = mediaTabDownloads
	mediaHub.zone = mediaZoneContent
	mediaHub.cards = []mediaCard{{title: "A", queued: true}, {title: "B", queued: true}, {title: "C", queued: true}}
	mediaHub.index = 2
	// The list under the dialog: one row spanning most of the screen.
	mediaAddHit(mediaHit{kind: mediaHitCard, a: -1, b: 0, rect: sdl.Rect{X: 0, Y: 100, W: 1280, H: 560}})
	mediaHub.confirm = mediaConfirm{open: true, kind: mediaConfirmRemove, libID: "none", title: "Remove this download?",
		body: "A — frees 1 MB on the card.", ok: "Remove", focus: 1, back: mediaZoneContent}
	mediaHubDrawConfirm(renderer, config)

	var card sdl.Rect
	for _, h := range mediaHub.hits {
		if h.kind == mediaHitStateAction && h.a == -1 {
			card = h.rect
		}
	}
	if card.W == 0 || card.H == 0 {
		t.Fatal("the confirmation recorded no card")
	}
	if !mediaHubClick(card.X+8, card.Y+8, config) || !mediaHub.confirm.open {
		t.Fatal("a click on the confirmation's text closed it")
	}
	if !mediaHubClick(20, 650, config) {
		t.Fatal("a click outside the confirmation was not consumed")
	}
	if mediaHub.confirm.open {
		t.Fatal("a click outside the confirmation did not cancel it")
	}
	if mediaHub.index != 2 {
		t.Fatalf("the click reached the list under the dialog: focus moved to row %d", mediaHub.index)
	}
}

// A played file from the library can be removed from History and Continue;
// any other file on the card is Files' to manage and is never offered.
func TestMediaLocalCardsOnlyRemoveLibraryFiles(t *testing.T) {
	mediaHubTestState(t)
	root := dlLibraryTestState(t)
	kept := filepath.Join(root, "video", "kept.mp4")
	dlLibrary.mu.Lock()
	dlLibrary.entries = []dlLibraryEntry{{ID: "k", Path: kept, Video: true, Size: 5 << 20, Title: "Kept"}}
	dlLibrary.mu.Unlock()

	ctx := mediaNewCardContext()
	card := mediaCardFromVideo(VideoInfo{Title: "Kept", URL: kept}, ctx)
	mediaMarkLocal(&card, ctx)
	if card.libID != "k" || card.path != kept || !card.downloaded {
		t.Fatalf("library file card = %+v, want it linked to its entry", card)
	}
	foreign := mediaCardFromVideo(VideoInfo{Title: "Holiday", URL: filepath.Join(t.TempDir(), "holiday.mp4")}, ctx)
	mediaMarkLocal(&foreign, ctx)
	if foreign.libID != "" {
		t.Fatalf("a file outside the library was linked to entry %q", foreign.libID)
	}

	mediaHubAskRemove(foreign)
	if mediaHub.confirm.open {
		t.Fatal("a file opened from Files was offered for deletion")
	}
	mediaHubAskRemove(card)
	if !mediaHub.confirm.open || mediaHub.confirm.libID != "k" || mediaHub.confirm.freed != 5<<20 {
		t.Fatalf("confirmation = %+v, want one for entry k freeing 5 MB", mediaHub.confirm)
	}
}

// A row appearing above the focused one (Continue watching after a play) must
// not move the ring to another row.
func TestMediaRebuildKeepsTheFocusedRowKind(t *testing.T) {
	mediaHubTestState(t)
	dlLibraryTestState(t)
	mediaTestOnline(t, true)
	favoritesMutex.Lock()
	prevStore := favoritesStore
	favoritesStore = FavoritesStore{}
	favoritesMutex.Unlock()
	t.Cleanup(func() {
		favoritesMutex.Lock()
		favoritesStore = prevStore
		favoritesMutex.Unlock()
	})
	config := &Config{Variables: Variables{Custom: map[string]interface{}{
		mediaTrendingVar: []VideoInfo{
			{Title: "One", WebpageURL: "https://example.com/1"},
			{Title: "Two", WebpageURL: "https://example.com/2"},
		},
	}}}

	mediaHubRebuild(config)
	if len(mediaHub.rows) != 1 || mediaHub.rows[0].kind != mediaRowTrending {
		t.Fatalf("rows = %d, want Trending alone", len(mediaHub.rows))
	}
	mediaHub.rows[0].focus = 1

	favoritesMutex.Lock()
	favoritesStore.Recent = []FavoriteItem{{Type: "video", Timestamp: time.Now(),
		Data: VideoInfo{Title: "Half", WebpageURL: "https://example.com/h", Duration: 100, Position: 40}}}
	favoritesMutex.Unlock()
	mediaHubRebuild(config)
	if len(mediaHub.rows) != 2 || mediaHub.rows[0].kind != mediaRowContinue {
		t.Fatalf("rows = %d, want Continue watching above Trending", len(mediaHub.rows))
	}
	if mediaHub.row != 1 || mediaHub.rows[1].focus != 1 {
		t.Fatalf("ring on row %d card %d, want Trending's second card", mediaHub.row, mediaHub.rows[1].focus)
	}
}

// Every footer row the Media screen shows fits the Brick's compact canvas
// without dropping a hint; the content row is the longest.
func TestMediaFooterRowsFitTheCompactCanvas(t *testing.T) {
	newKeyboardTestRenderer(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	withScreen(t, uiCanvasCompactW, uiCanvasCompactH)
	t.Cleanup(ClearTextCache)
	mediaHubTestState(t)
	mediaTestPlayerPhase(t, "idle")
	videoMiniClear()
	tube := sceneIndexExact(config, mediaHubSceneName)
	if tube < 0 {
		t.Fatal("the shipped config has no Media scene")
	}
	band := sceneFooterBand()
	avail := band.W - 2*scaleX(16, screenWidth)

	states := []struct {
		name  string
		setup func()
		want  int
	}{
		{"search bar", func() { mediaHub.zone = mediaZoneSearch }, 5},
		{"content", func() { mediaHub.zone = mediaZoneContent }, 7},
		{"downloads", func() { mediaHub.tab, mediaHub.zone = mediaTabDownloads, mediaZoneContent }, 6},
		{"details sheet", func() { mediaHub.sheet.open = true }, 3},
		{"confirmation", func() { mediaHub.confirm.open = true }, 3},
		{"favorites", func() { mediaHub.tab, mediaHub.zone = mediaTabFavorites, mediaZoneContent }, 6},
		{"favorites with undo", func() {
			mediaHub.tab, mediaHub.zone = mediaTabFavorites, mediaZoneContent
			mediaFavUndo.at = time.Now()
		}, 6},
		{"library", func() { mediaHub.tab, mediaHub.zone = mediaTabLibrary, mediaZoneContent }, 6},
		{"player", func() {
			embeddedPlayer.mu.Lock()
			embeddedPlayer.phase = "playing"
			embeddedPlayer.mu.Unlock()
		}, 7},
	}
	for _, s := range states {
		mediaHub.tab, mediaHub.zone = mediaTabForYou, mediaZoneSearch
		mediaHub.sheet, mediaHub.confirm = mediaSheet{}, mediaConfirm{}
		embeddedPlayer.mu.Lock()
		embeddedPlayer.phase = "idle"
		embeddedPlayer.mu.Unlock()
		s.setup()
		hints := footerHints(config, config.Scenes[tube])
		if len(hints) != s.want {
			t.Errorf("%s: %d hints, want %d: %v", s.name, len(hints), s.want, hints)
		}
		plan := planFooterHints(config, hints, avail)
		if plan.font == nil {
			t.Fatalf("%s: no font for the hint row", s.name)
		}
		if plan.width > avail || len(plan.hints) < len(hints) {
			t.Errorf("%s: row is %dpx for %dpx and kept %d of %d hints", s.name, plan.width, avail, len(plan.hints), len(hints))
		}
	}
}
