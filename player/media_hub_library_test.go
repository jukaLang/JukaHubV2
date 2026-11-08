package main

// media_hub_library_test.go — the Library tab on the Media screen: its rows
// and See all card, opening a show and coming back with B, the empty state's
// Scan again, the offline landing that keeps the Library, the Keep fully
// watched videos switch in Settings, and how a picture is fitted to a card.

import (
	"fmt"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// mlibTestModel publishes a model built from items, as a finished scan does.
func mlibTestModel(items []mlibItem) {
	m := mlibBuildModel(items, nil)
	mlibState.mu.Lock()
	mlibState.loaded = true
	mlibState.items = append([]mlibItem(nil), items...)
	mlibState.gen++
	m.gen = mlibState.gen
	mlibState.model = m
	mlibState.mu.Unlock()
}

func TestMediaLibraryRowsOpenAShowAndBComesBack(t *testing.T) {
	mlibTestState(t)
	mediaHubTestState(t)
	config := mediaTestTubeConfig(t)
	items := []mlibItem{
		{Path: "/tv/o/s1e1.mkv", Video: true, Show: "The Office", Season: 1, Episode: 1, Title: "Pilot", Added: 3},
		{Path: "/tv/o/s1e2.mkv", Video: true, Show: "The Office", Season: 1, Episode: 2, Added: 2},
		{Path: "/m/a/01.mp3", Title: "One", Artist: "Artist", Album: "Album", Track: 1, Added: 1},
	}
	for i := 0; i < mediaRowCards+1; i++ {
		items = append(items, mlibItem{Path: fmt.Sprintf("/films/f%02d.mkv", i), Video: true, Title: fmt.Sprintf("Film %02d", i)})
	}
	mlibTestModel(items)
	mediaHub.tab = mediaTabLibrary
	mediaHub.zone = mediaZoneContent
	mediaHub.builtAt = 0
	mediaHubEnsureBuilt(config)

	kinds := map[mediaRowKind]int{}
	for i, row := range mediaHub.rows {
		kinds[row.kind] = i
	}
	movies, okMovies := kinds[mediaRowLibMovies]
	shows, okShows := kinds[mediaRowLibShows]
	_, okMusic := kinds[mediaRowLibMusic]
	if !okMovies || !okShows || !okMusic {
		t.Fatalf("rows = %d, want Movies, TV shows and Music", len(mediaHub.rows))
	}
	films := mediaHub.rows[movies].cards
	if len(films) != mediaRowCards+1 || films[mediaRowCards].mlibKind != mlibCardAllMovies {
		t.Fatalf("Movies row has %d cards, want %d and a See all", len(films), mediaRowCards+1)
	}
	if last := films[mediaRowCards]; last.meta != fmt.Sprintf("%d movies", mediaRowCards+1) {
		t.Fatalf("See all says %q", last.meta)
	}
	show := mediaHub.rows[shows].cards[0]
	if show.mlibKind != mlibCardShow || show.meta != "2 episodes" {
		t.Fatalf("show card = %+v", show)
	}

	mediaHub.row = shows
	mediaHub.rows[shows].focus = 0
	mediaHubActivate(config)
	if len(mlibNav.stack) != 1 || mediaTabHasRows(mediaHub.tab) {
		t.Fatal("A on the show card did not open its episodes")
	}
	if len(mediaHub.cards) != 2 || mediaHub.cards[0].title != "Pilot" || mediaHub.cards[1].title != "Episode 2" ||
		mediaHub.cards[0].meta != "Season 1 · Episode 1" {
		t.Fatalf("episodes = %+v", mediaHub.cards)
	}
	if card, ok := mediaHubFocusedCard(); !ok || card.mlibKind != mlibCardVideo {
		t.Fatal("the ring is not on the first episode")
	}

	mediaHubInput(config, mediaInBack)
	if len(mlibNav.stack) != 0 || mediaHub.tab != mediaTabLibrary || !mediaTabHasRows(mediaHub.tab) {
		t.Fatal("B did not close the show back to the rows")
	}
	if mediaHub.row != shows {
		t.Fatalf("back on row %d, want the TV shows row %d", mediaHub.row, shows)
	}
}

// With nothing found and no scan running, the empty state's Scan again is
// reachable with the pad.
func TestMediaLibraryEmptyStateOffersAScan(t *testing.T) {
	mlibTestState(t)
	mediaHubTestState(t)
	config := mediaTestTubeConfig(t)
	mediaHub.tab = mediaTabLibrary
	if got := mediaHubStateAction(config, true); got != -1 {
		t.Fatalf("before the index was read: action %d, want none", got)
	}
	mlibLoad()
	if got := mediaHubStateAction(config, true); got != 2 {
		t.Fatalf("an empty library: action %d, want 2 (Scan again)", got)
	}
	if !mediaHubContentFocusable(config) {
		t.Fatal("Scan again cannot be reached with the pad")
	}
}

// Offline, Media lands on Downloads from a network tab, but the Library is
// all on the card and stays where it is.
func TestMediaOfflineLandsOnDownloadsButKeepsTheLibrary(t *testing.T) {
	mediaTestOnline(t, false)
	dlLibraryTestState(t)
	mlibTestState(t)
	mediaHubTestState(t)
	config := mediaTestTubeConfig(t)

	mediaHub.tab = mediaTabTrending
	mediaHubEnter(config)
	if mediaHub.tab != mediaTabDownloads || !mediaHub.offlineLanded || mediaHub.zone != mediaZoneContent {
		t.Fatalf("offline from Trending: tab %d landed=%v zone %d, want Downloads", mediaHub.tab, mediaHub.offlineLanded, mediaHub.zone)
	}
	mediaHub.tab = mediaTabLibrary
	mediaHub.offlineLanded = false
	mediaHubEnter(config)
	if mediaHub.tab != mediaTabLibrary || mediaHub.offlineLanded {
		t.Fatalf("offline on the Library: moved to tab %d", mediaHub.tab)
	}
	for _, tab := range []mediaTab{mediaTabTrending, mediaTabShorts, mediaTabLiveTV, mediaTabPodcasts} {
		if !mediaTabNeedsNetwork(tab) {
			t.Errorf("tab %s does not say it needs the network", mediaTabLabel(tab))
		}
	}
	for _, tab := range []mediaTab{mediaTabDownloads, mediaTabHistory, mediaTabFavorites, mediaTabLibrary} {
		if mediaTabNeedsNetwork(tab) {
			t.Errorf("tab %s says it needs the network", mediaTabLabel(tab))
		}
	}
}

func TestDlShouldKeepWatchedDecides(t *testing.T) {
	cases := []struct {
		keep     bool
		page     string
		complete bool
		want     bool
	}{
		{true, "https://example.com/v", true, true},
		{false, "https://example.com/v", true, false},
		{true, "  ", true, false},
		{true, "https://example.com/v", false, false},
	}
	for _, c := range cases {
		if got := dlShouldKeepWatched(c.keep, c.page, c.complete); got != c.want {
			t.Errorf("dlShouldKeepWatched(%v, %q, %v) = %v, want %v", c.keep, c.page, c.complete, got, c.want)
		}
	}
}

// The Settings row reads and writes the switch the player obeys.
func TestMediaKeepWatchedSettingsRow(t *testing.T) {
	prevKeep := dlKeepWatchedOn.Load()
	t.Cleanup(func() { dlKeepWatchedOn.Store(prevKeep) })
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	row := setTestRow(t, "media", "keepWatched")
	if row.Kind != setKindToggle {
		t.Fatalf("Keep fully watched videos is row kind %d, want a switch", row.Kind)
	}
	dlKeepWatchedOn.Store(true)
	if !row.Get(config) || !mediaKeepWatchedEnabled(config) {
		t.Fatal("the shipped default is on, but the row shows it off")
	}
	row.Set(config, false)
	if row.Get(config) || dlKeepWatchedEnabled() || config.Variables.Custom[dlKeepWatchedKey] != false {
		t.Fatal("the row did not turn keeping watched videos off")
	}
	mediaSetKeepWatched(config, true)
	if !row.Get(config) || !dlKeepWatchedEnabled() {
		t.Fatal("the setter did not turn it back on")
	}
	config.Variables.Custom[dlKeepWatchedKey] = "off"
	if mediaKeepWatchedEnabled(config) {
		t.Fatal("a hand-edited \"off\" reads as on")
	}
}

func TestMediaCoverCropFillsTheFrame(t *testing.T) {
	cases := []struct {
		name         string
		tw, th, w, h int32
		want         sdl.Rect
	}{
		{"same shape", 1280, 720, 320, 180, sdl.Rect{X: 0, Y: 0, W: 1280, H: 720}},
		{"4:3 thumbnail in 16:9", 480, 360, 320, 180, sdl.Rect{X: 0, Y: 45, W: 480, H: 270}},
		{"16:9 frame in a Short", 1920, 1080, 180, 320, sdl.Rect{X: 656, Y: 0, W: 607, H: 1080}},
		{"poster in 16:9", 600, 900, 290, 163, sdl.Rect{X: 0, Y: 281, W: 600, H: 337}},
		{"unknown frame", 100, 50, 0, 0, sdl.Rect{W: 100, H: 50}},
	}
	for _, c := range cases {
		if got := mediaCoverCrop(c.tw, c.th, c.w, c.h); got != c.want {
			t.Errorf("%s: mediaCoverCrop = %+v, want %+v", c.name, got, c.want)
		}
	}
}
