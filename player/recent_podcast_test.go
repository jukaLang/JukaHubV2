package main

import (
	"encoding/json"
	"testing"
	"time"
)

// A podcast in Continue has to survive the favourites file: the store marshals
// each item's payload by type, and a type it does not know comes back as a map,
// which would make the row unplayable after a restart.
func TestFavoritePodcastItemRoundTrips(t *testing.T) {
	ep := podcastEpisode{
		Title:     "Episode 12",
		URL:       "https://example.com/ep12.mp3",
		Duration:  1800,
		Published: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(FavoriteItem{Type: "podcast", Data: ep, Timestamp: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	var back FavoriteItem
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got, ok := back.Data.(podcastEpisode)
	if !ok {
		t.Fatalf("payload decoded as %T, want podcastEpisode", back.Data)
	}
	if got.URL != ep.URL || got.Title != ep.Title || got.Duration != ep.Duration {
		t.Fatalf("payload = %+v", got)
	}
	if label := back.Label(); label != "Episode 12" {
		t.Fatalf("label = %q", label)
	}
}

func TestAddRecentPodcastDedupesAndOrders(t *testing.T) {
	favoritesMutex.Lock()
	old := append([]FavoriteItem(nil), favoritesStore.Recent...)
	favoritesStore.Recent = nil
	favoritesMutex.Unlock()
	t.Cleanup(func() {
		favoritesMutex.Lock()
		favoritesStore.Recent = old
		favoritesMutex.Unlock()
	})

	addRecentPodcast(podcastEpisode{Title: "First", URL: "https://x/1.mp3"})
	addRecentPodcast(podcastEpisode{Title: "Second", URL: "https://x/2.mp3"})
	addRecentPodcast(podcastEpisode{Title: "First again", URL: "https://x/1.mp3"})

	var podcasts []FavoriteItem
	for _, it := range getRecentItems() {
		if it.Type == "podcast" {
			podcasts = append(podcasts, it)
		}
	}
	if len(podcasts) != 2 {
		t.Fatalf("%d podcast rows, want 2: %+v", len(podcasts), podcasts)
	}
	if podcasts[0].Label() != "First again" {
		t.Fatalf("newest row = %q, want the re-played episode on top", podcasts[0].Label())
	}
	if podcasts[1].Label() != "Second" {
		t.Fatalf("second row = %q", podcasts[1].Label())
	}

	// An entry without a link cannot be resumed, so it must not enter the list.
	addRecentPodcast(podcastEpisode{Title: "No link"})
	for _, it := range getRecentItems() {
		if it.Type == "podcast" && it.Label() == "No link" {
			t.Fatal("a podcast without a URL was recorded")
		}
	}
}
