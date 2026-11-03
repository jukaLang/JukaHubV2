package main

import (
	"os"
	"path/filepath"
	"testing"
)

// podcastTestFeed is a small but realistic feed: a channel block with an
// iTunes author, three episodes out of order, durations in every spelling a
// host actually emits, one enclosure with a tracking query string and one item
// with no title at all.
const podcastTestFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
  <channel>
    <title>The Test Show</title>
    <itunes:author>Test Studio</itunes:author>
    <item>
      <title>Older episode</title>
      <pubDate>Mon, 02 Jan 2023 10:00:00 +0000</pubDate>
      <itunes:duration>1:02:03</itunes:duration>
      <enclosure url="https://cdn.example.com/old.mp3" type="audio/mpeg" length="1" />
    </item>
    <item>
      <title>Newest episode</title>
      <pubDate>Fri, 06 Oct 2023 08:30:00 GMT</pubDate>
      <itunes:duration>45:10</itunes:duration>
      <enclosure url="https://cdn.example.com/new.mp3?track=1" type="audio/mpeg" length="1" />
    </item>
    <item>
      <pubDate>2023-06-01</pubDate>
      <itunes:duration>3725</itunes:duration>
      <enclosure url="https://cdn.example.com/2023/06/some_episode_file.mp3" type="audio/mpeg" length="1" />
    </item>
  </channel>
</rss>`

func TestPodcastParseFeedOrdersAndReadsMetadata(t *testing.T) {
	show := podcastParseFeed("https://example.com/feed.xml", podcastTestFeed)
	if show.Err != "" {
		t.Fatalf("unexpected error: %s", show.Err)
	}
	if show.Title != "The Test Show" || show.Author != "Test Studio" {
		t.Fatalf("show = %q by %q", show.Title, show.Author)
	}
	if len(show.Episodes) != 3 {
		t.Fatalf("episodes = %d, want 3", len(show.Episodes))
	}
	// Newest first, the undated item last.
	if show.Episodes[0].Title != "Newest episode" {
		t.Errorf("first episode = %q, want the newest", show.Episodes[0].Title)
	}
	if show.Episodes[1].Title != "some episode file" {
		t.Errorf("second episode = %q, want the June episode", show.Episodes[1].Title)
	}
	if show.Episodes[2].Title != "Older episode" {
		t.Errorf("third episode = %q, want the oldest", show.Episodes[2].Title)
	}
	if show.Episodes[0].Duration < 2709 || show.Episodes[0].Duration > 2711 {
		t.Errorf("H:MM:SS duration = %v, want 2710s", show.Episodes[0].Duration)
	}
	// A seconds value and a plain-seconds value both land in seconds.
	if show.Episodes[1].Duration != 3725 {
		t.Errorf("seconds duration = %v, want 3725s", show.Episodes[1].Duration)
	}
	if show.Episodes[2].Duration != 3723 {
		t.Errorf("third duration = %v, want 3723s", show.Episodes[2].Duration)
	}
}

func TestPodcastParseFeedRejectsEmptyFeed(t *testing.T) {
	show := podcastParseFeed("https://example.com/empty.xml", `<rss><channel><title>Empty</title></channel></rss>`)
	if show.Err == "" {
		t.Fatal("a feed with no episodes must report why the panel is empty")
	}
	if len(show.Episodes) != 0 {
		t.Fatalf("episodes = %d, want 0", len(show.Episodes))
	}
}

func TestPodcastParseDurationSpellings(t *testing.T) {
	cases := map[string]float64{
		"":         0,
		"3723":     3723,
		"45:10":    2710,
		"1:02:03":  3723,
		"junk":     0,
		"-5":       0,
		"99:99:99": 0, // over a day: a parse accident, not an episode
	}
	for raw, want := range cases {
		if got := podcastParseDuration(raw); got != want {
			t.Errorf("podcastParseDuration(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestPodcastSubscriptionsPersistAndSeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "podcasts.json")
	prevPath := podcastSubsPath
	podcastSubsPath = func() string { return path }
	t.Cleanup(func() {
		podcastSubsPath = prevPath
		resetPodcastStoreForTest()
	})
	resetPodcastStoreForTest()

	// A fresh install starts with the curated shows rather than an empty panel.
	feeds := podcastFeeds()
	if len(feeds) != len(podcastDefaultFeeds) {
		t.Fatalf("seeded feeds = %d, want %d", len(feeds), len(podcastDefaultFeeds))
	}
	if !addPodcastFeed("https://example.com/mine.xml") {
		t.Fatal("adding a new feed should change the list")
	}
	if addPodcastFeed("https://example.com/mine.xml") {
		t.Fatal("adding the same feed twice must be refused")
	}
	if addPodcastFeed("not a url") {
		t.Fatal("a non-URL must not be subscribed")
	}
	if err := savePodcastSubscriptions(); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("subscriptions file: %v", err)
	}

	resetPodcastStoreForTest()
	reloaded := podcastFeeds()
	if len(reloaded) != len(feeds)+1 {
		t.Fatalf("reloaded feeds = %d, want %d", len(reloaded), len(feeds)+1)
	}
	if !removePodcastFeed("https://example.com/mine.xml") {
		t.Fatal("removing a subscribed feed should report success")
	}
	if removePodcastFeed("https://example.com/mine.xml") {
		t.Fatal("removing it twice must report nothing changed")
	}
}

// seedPodcastPanel installs one show with three episodes and opens the panel
// without touching the network.
func seedPodcastPanel(t *testing.T) podcastShow {
	t.Helper()
	resetPodcastStoreForTest()
	podcastStore.mu.Lock()
	podcastStore.loaded = true
	podcastStore.feeds = []string{"https://example.com/feed.xml"}
	podcastStore.shows = map[string]podcastShow{
		"https://example.com/feed.xml": {
			Title:   "The Test Show",
			FeedURL: "https://example.com/feed.xml",
			Episodes: []podcastEpisode{
				{Title: "One", URL: "https://cdn.example.com/1.mp3", Duration: 600},
				{Title: "Two", URL: "https://cdn.example.com/2.mp3", Duration: 900},
				{Title: "Three", URL: "https://cdn.example.com/3.mp3", Duration: 1200},
			},
		},
	}
	show := podcastStore.shows["https://example.com/feed.xml"]
	podcastStore.mu.Unlock()

	podPanel = podcastPanelState{open: true, column: podcastColumnShows}
	t.Cleanup(func() {
		podPanel = podcastPanelState{}
		resetPodcastStoreForTest()
	})
	return show
}

func TestPodcastPanelSelectsShowThenPlaysEpisode(t *testing.T) {
	seedPodcastPanel(t)
	var playedShow podcastShow
	playedIndex := -1
	prev := podPanelPlayFrom
	podPanelPlayFrom = func(_ *Config, show podcastShow, index int) bool {
		playedShow, playedIndex = show, index
		return true
	}
	t.Cleanup(func() { podPanelPlayFrom = prev })

	// Entering a show moves the focus to its episodes.
	podcastPanelActivate(&Config{})
	if podPanel.column != podcastColumnEpisodes {
		t.Fatalf("column = %d, want the episode column", podPanel.column)
	}
	// Down picks the second episode, and A plays that one.
	podcastPanelMove(1)
	podcastPanelActivate(&Config{})
	if playedIndex != 1 || playedShow.Title != "The Test Show" {
		t.Fatalf("played show=%q index=%d, want The Test Show index 1", playedShow.Title, playedIndex)
	}
	if podPanel.open {
		t.Fatal("the panel closes when an episode starts playing")
	}
}

func TestPodcastPanelSwitchesColumnsAndClamps(t *testing.T) {
	seedPodcastPanel(t)
	if podcastPanelSwitchColumn(-1) {
		t.Fatal("Left on the show column has nowhere to go")
	}
	if !podcastPanelSwitchColumn(1) {
		t.Fatal("Right should enter the episode column")
	}
	if podcastPanelMove(-1) {
		t.Fatal("Up at the top of the episode list must not move")
	}
	for i := 0; i < 10; i++ {
		podcastPanelMove(1)
	}
	if podPanel.episode != 2 {
		t.Fatalf("episode cursor = %d, want the last row (2)", podPanel.episode)
	}
	if !podcastPanelSwitchColumn(-1) || podPanel.column != podcastColumnShows {
		t.Fatal("Left should return to the show column")
	}
	// The add-feed row sits after the shows and must be reachable.
	podcastPanelMove(1)
	if podPanel.show != 1 {
		t.Fatalf("show cursor = %d, want the add-feed row (1)", podPanel.show)
	}
}

func TestPodcastShowQueueUsesEpisodeMetadata(t *testing.T) {
	show := podcastShow{Title: "S", Episodes: []podcastEpisode{
		{Title: "One", URL: "https://cdn.example.com/1.mp3", Duration: 600},
		{Title: "", URL: "https://cdn.example.com/2.mp3", Duration: 900},
	}}
	tracks := podcastEpisodeTracks(show)
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(tracks))
	}
	if tracks[0].Name != "One" || tracks[0].Duration != 600 {
		t.Errorf("track 0 = %+v", tracks[0])
	}
	if tracks[1].Name == "" {
		t.Error("an untitled episode still needs a label in the player")
	}
}

func TestAudioURLDetection(t *testing.T) {
	cases := map[string]bool{
		"https://cdn.example.com/ep.mp3":          true,
		"https://cdn.example.com/ep.mp3?track=1":  true,
		"https://cdn.example.com/ep.m4a#t=30":     true,
		"https://cdn.example.com/ep.mp4":          false,
		"/media/music/song.flac":                  false,
		"https://www.youtube.com/watch?v=abcdefg": false,
	}
	for raw, want := range cases {
		if got := isAudioURL(raw); got != want {
			t.Errorf("isAudioURL(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestPodcastEpisodeLookupFindsEnclosure(t *testing.T) {
	seedPodcastPanel(t)
	if _, _, ok := podcastEpisodeLookup("https://cdn.example.com/2.mp3?utm_source=app"); ok {
		t.Fatal("a URL with a different query is a different enclosure")
	}
	show, index, ok := podcastEpisodeLookup("https://cdn.example.com/2.mp3")
	if !ok || show.Title != "The Test Show" || index != 1 {
		t.Fatalf("lookup = %q #%d ok=%v", show.Title, index, ok)
	}
}

func resetPodcastStoreForTest() {
	podcastStore.mu.Lock()
	podcastStore.loaded = false
	podcastStore.feeds = nil
	podcastStore.shows = nil
	podcastStore.mu.Unlock()
}
