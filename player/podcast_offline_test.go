package main

// Headless tests for MED-8. Every case uses a synthetic feed, an in-memory
// downloads runner or a temporary file; no network, sound card, ffprobe or
// yt-dlp is involved.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPodcastOPMLExportAndImport(t *testing.T) {
	feeds := []string{
		"https://example.com/a.xml",
		"https://example.com/b.xml",
	}
	data, err := podcastOPMLExport(feeds, map[string]string{
		feeds[0]: "Show A",
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	xmlText := string(data)
	for _, want := range []string{
		`<opml version="2.0">`,
		`text="Show A"`,
		`xmlUrl="https://example.com/a.xml"`,
		`xmlUrl="https://example.com/b.xml"`,
		`text="example.com"`,
	} {
		if !strings.Contains(xmlText, want) {
			t.Errorf("OPML missing %q:\n%s", want, xmlText)
		}
	}

	// Nested folders and the older url spelling are both accepted, and the
	// duplicate feed is collapsed.
	nested := `<?xml version="1.0"?>
<opml version="2.0"><head/><body>
  <outline text="News">
    <outline text="A" type="rss" xmlUrl="https://example.com/a.xml"/>
    <outline text="B" type="rss" xmlUrl="https://example.com/b.xml"/>
  </outline>
  <outline text="C" type="rss" xmlUrl="https://example.com/a.xml"/>
</body></opml>`
	got, err := podcastOPMLImport([]byte(nested))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(got) != 2 || got[0] != feeds[0] || got[1] != feeds[1] {
		t.Fatalf("import = %v, want %v", got, feeds)
	}

	if _, err := podcastOPMLImport([]byte(`<opml><body><outline text="nothing"/></body></opml>`)); err == nil {
		t.Fatal("empty OPML import succeeded, want an explanation")
	}
	if _, err := podcastOPMLImport([]byte(`not xml`)); err == nil {
		t.Fatal("malformed OPML import succeeded")
	}
}

func TestPodcastOPMLFileRoundTrip(t *testing.T) {
	old := podcastOPMLPath
	path := filepath.Join(t.TempDir(), "feeds.opml")
	podcastOPMLPath = func() string { return path }
	t.Cleanup(func() { podcastOPMLPath = old })

	data, err := podcastOPMLExport([]string{"https://example.com/feed.xml"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(podcastOPMLPath(), data, 0o644); err != nil {
		t.Fatal(err)
	}
	read, err := os.ReadFile(podcastOPMLPath())
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := podcastOPMLImport(read)
	if err != nil {
		t.Fatalf("parse written file: %v", err)
	}
	if len(feeds) != 1 || feeds[0] != "https://example.com/feed.xml" {
		t.Fatalf("round-trip feeds = %v", feeds)
	}
}

func TestPodcastOPMLFileImportMergesSubscriptions(t *testing.T) {
	dir := t.TempDir()
	oldPath := podcastSubsPath
	podcastSubsPath = func() string { return filepath.Join(dir, "podcasts.json") }
	podcastStore.mu.Lock()
	oldLoaded, oldFeeds, oldShows := podcastStore.loaded, podcastStore.feeds, podcastStore.shows
	podcastStore.loaded, podcastStore.feeds = false, nil
	podcastStore.shows = map[string]podcastShow{}
	podcastStore.mu.Unlock()
	t.Cleanup(func() {
		podcastStore.mu.Lock()
		podcastStore.loaded, podcastStore.feeds, podcastStore.shows = oldLoaded, oldFeeds, oldShows
		podcastStore.mu.Unlock()
		podcastSubsPath = oldPath
	})
	if err := os.WriteFile(podcastSubsPath(), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}

	opmlFile := filepath.Join(dir, "import.opml")
	opml := `<?xml version="1.0"?><opml version="2.0"><body>
	  <outline text="Folder">
	    <outline text="A" type="rss" xmlUrl="https://example.com/a.xml"/>
	    <outline text="B" type="rss" xmlUrl="https://example.com/b.xml"/>
	  </outline>
	  <outline text="A again" type="rss" xmlUrl="https://example.com/a.xml"/>
	</body></opml>`
	if err := os.WriteFile(opmlFile, []byte(opml), 0o644); err != nil {
		t.Fatal(err)
	}
	added, err := podcastOPMLImportFile(opmlFile)
	if err != nil {
		t.Fatalf("import file: %v", err)
	}
	if added != 2 {
		t.Fatalf("added %d feeds, want 2", added)
	}
	feeds := podcastFeeds()
	if len(feeds) != 2 || feeds[0] != "https://example.com/a.xml" || feeds[1] != "https://example.com/b.xml" {
		t.Fatalf("subscriptions = %v", feeds)
	}
	var saved []string
	data, err := os.ReadFile(podcastSubsPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 {
		t.Fatalf("saved subscriptions = %v", saved)
	}
	// Importing the same file again is a no-op and does not duplicate rows.
	if added, err := podcastOPMLImportFile(opmlFile); err != nil || added != 0 {
		t.Fatalf("second import = %d, %v; want 0, nil", added, err)
	}
}

func TestPodcastFeedReadsChaptersURL(t *testing.T) {
	raw := `<rss version="2.0"><channel><title>Show</title>
	  <item>
	    <title>Episode 1</title>
	    <enclosure url="https://example.com/e1.mp3" type="audio/mpeg"/>
	    <podcast:chapters url="https://example.com/e1-chapters.json" type="application/json+chapters"/>
	  </item></channel></rss>`
	show := podcastParseFeed("https://example.com/feed.xml", raw)
	if len(show.Episodes) != 1 {
		t.Fatalf("parsed %d episodes, want 1", len(show.Episodes))
	}
	ep := show.Episodes[0]
	if ep.ChaptersURL != "https://example.com/e1-chapters.json" {
		t.Fatalf("chapters URL = %q", ep.ChaptersURL)
	}
	if !strings.Contains(podcastEpisodeMeta(ep), "chapters") {
		t.Fatalf("episode meta %q does not mention chapters", podcastEpisodeMeta(ep))
	}
}

func TestPodcastChaptersJSONSortedAndBounded(t *testing.T) {
	raw := `{"chapters":[
	  {"startTime":60,"endTime":120,"title":"  Second\n chapter "},
	  {"startTime":0,"title":"Intro"},
	  {"startTime":60.2,"title":"duplicate"},
	  {"startTime":-4,"title":"bad"},
	  {"startTime":180,"title":""}
	]}`
	chapters := podcastParseChaptersJSON([]byte(raw), 240)
	if len(chapters) != 3 {
		t.Fatalf("got %d chapters, want 3: %#v", len(chapters), chapters)
	}
	if chapters[0].Title != "Intro" || chapters[0].Start != 0 || chapters[0].End != 60 {
		t.Errorf("chapter 0 = %#v", chapters[0])
	}
	if chapters[1].Title != "Second chapter" || chapters[1].Start != 60 || chapters[1].End != 120 {
		t.Errorf("chapter 1 = %#v", chapters[1])
	}
	if chapters[2].Start != 180 || chapters[2].End != 240 || chapters[2].Title != "Chapter 3" {
		t.Errorf("chapter 2 = %#v", chapters[2])
	}
}

func TestPodcastExternalChaptersReachPlayerCache(t *testing.T) {
	oldFetch := podcastChapterFetch
	podcastChapterFetch = func(context.Context, string) string {
		return `{"chapters":[{"startTime":0,"title":"Opening"},{"startTime":30,"title":"Main"}]}`
	}
	t.Cleanup(func() { podcastChapterFetch = oldFetch })

	key := filepath.Join(t.TempDir(), "episode-key")
	ep := podcastEpisode{
		Title:       "Episode",
		URL:         "https://example.com/ep.mp3",
		ChaptersURL: "https://example.com/chapters.json",
		Duration:    90,
	}
	podcastLoadEpisodeChapters(ep, key)
	chapters := audioChaptersFor(key)
	if len(chapters) != 2 || chapters[0].Title != "Opening" || chapters[1].Title != "Main" {
		t.Fatalf("loaded chapters = %#v", chapters)
	}

	// The player reads the same cache for a podcast episode even though the
	// show's queue has several tracks. This is the UI-facing path, not just
	// the JSON parser.
	music.mu.Lock()
	oldTracks, oldCurrent := music.tracks, music.current
	music.tracks = []musicTrack{
		{Path: "https://example.com/other.mp3", Name: "Other"},
		{Path: key, Name: "Episode", Podcast: true, PodcastURL: ep.URL},
	}
	music.current = 1
	music.mu.Unlock()
	t.Cleanup(func() {
		music.mu.Lock()
		music.tracks, music.current = oldTracks, oldCurrent
		music.mu.Unlock()
	})
	got := musicChapters()
	if len(got) != 2 || got[1].Title != "Main" {
		t.Fatalf("musicChapters for podcast = %#v", got)
	}
}

func TestPodcastDownloadedEpisodePlaysOfflineAndCleansUp(t *testing.T) {
	downloadsTestReset(t)
	var saved string
	downloadRunner = func(_ context.Context, job downloadJob, _ func(downloadProgress)) (downloadResult, error) {
		if err := os.MkdirAll(job.Dir, 0o755); err != nil {
			return downloadResult{}, err
		}
		saved = filepath.Join(job.Dir, "episode.m4a")
		if err := os.WriteFile(saved, []byte("synthetic audio"), 0o644); err != nil {
			return downloadResult{}, err
		}
		return downloadResult{Path: saved, Title: "Episode 1"}, nil
	}
	ep := podcastEpisode{
		Title: "Episode 1", URL: "https://example.com/episode-1.mp3", Duration: 60,
	}
	if _, err := podcastDownloadEpisode(ep); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitForDownload(t, "podcast download", func() bool {
		for _, it := range downloadsSnapshot() {
			if it.Status == downloadDone && dlLibraryKey(it.URL) == dlLibraryKey(ep.URL) {
				return true
			}
		}
		return false
	})
	if got := podcastOfflinePath(ep.URL); got != saved {
		t.Fatalf("offline path = %q, want %q", got, saved)
	}
	if _, err := podcastDownloadEpisode(ep); err == nil {
		t.Fatal("enqueueing an already saved episode succeeded")
	}

	tracks := podcastEpisodeTracks(podcastShow{Episodes: []podcastEpisode{ep}})
	if len(tracks) != 1 || tracks[0].Path != saved || !tracks[0].Podcast || tracks[0].PodcastURL != ep.URL {
		t.Fatalf("tracks = %#v, want the saved file and podcast mark", tracks)
	}

	// Exercise the end-of-playback hook, not just the cleanup helper: a
	// finished podcast must remove its saved copy and queue row.
	music.mu.Lock()
	oldTracks, oldCurrent := music.tracks, music.current
	oldGen, oldPlaying := music.gen, music.playing
	oldPosition, oldDuration := music.position, music.duration
	oldPCM, oldRepeat := music.pcmBytes, music.repeatMode
	music.tracks = tracks
	music.current = 0
	music.gen++
	gen := music.gen
	music.playing = true
	music.position = 60
	music.duration = 60
	music.pcmBytes = 1
	music.repeatMode = musicRepeatOff
	music.mu.Unlock()
	t.Cleanup(func() {
		music.mu.Lock()
		music.tracks, music.current = oldTracks, oldCurrent
		music.gen, music.playing = oldGen, oldPlaying
		music.position, music.duration = oldPosition, oldDuration
		music.pcmBytes, music.repeatMode = oldPCM, oldRepeat
		music.mu.Unlock()
	})
	musicDecodeEnded(nil, saved, gen, false)
	if _, err := os.Stat(saved); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("saved file still exists after cleanup: %v", err)
	}
	for _, it := range downloadsSnapshot() {
		if dlLibraryKey(it.URL) == dlLibraryKey(ep.URL) {
			t.Fatalf("queue row still exists after cleanup: %#v", it)
		}
	}
	if got := podcastOfflinePath(ep.URL); got != "" {
		t.Fatalf("offline path after cleanup = %q", got)
	}
}

func TestPodcastCleanupLeavesUnrelatedFile(t *testing.T) {
	downloadsTestReset(t)
	outside := filepath.Join(t.TempDir(), "user-recording.m4a")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	podcastEpisodeCleanup("https://example.com/not-downloaded.mp3", outside)
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("cleanup removed a file outside the download folder: %v", err)
	}
}

func TestPodcastChapterFetchFailureIsQuiet(t *testing.T) {
	oldFetch := podcastChapterFetch
	podcastChapterFetch = func(context.Context, string) string { return "not json" }
	t.Cleanup(func() { podcastChapterFetch = oldFetch })

	key := filepath.Join(t.TempDir(), "missing-chapters")
	ep := podcastEpisode{URL: "https://example.com/e.mp3", ChaptersURL: "https://example.com/e.json", Duration: 10}
	podcastLoadEpisodeChapters(ep, key)
	if got := audioChaptersFor(key); len(got) != 0 {
		t.Fatalf("malformed chapters produced %#v", got)
	}
}

// Keep the test's deadline honest: the injected runner is synchronous, so this
// only catches a regression that makes the queue wait on a real transfer.
func TestPodcastDownloadRunnerIsHeadless(t *testing.T) {
	start := time.Now()
	downloadsTestReset(t)
	downloadRunner = func(context.Context, downloadJob, func(downloadProgress)) (downloadResult, error) {
		return downloadResult{}, errors.New("no transfer in this test")
	}
	_, _ = podcastDownloadEpisode(podcastEpisode{URL: "https://example.com/x.mp3"})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("headless queue took %v", elapsed)
	}
}
