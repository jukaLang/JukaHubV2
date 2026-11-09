package main

import (
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
