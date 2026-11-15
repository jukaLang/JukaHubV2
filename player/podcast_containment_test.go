package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func podcastContainmentSeed(t *testing.T, shows []podcastShow) {
	t.Helper()
	oldLoaded, oldFeeds, oldShows, oldStatus := podcastStore.loaded, podcastStore.feeds, podcastStore.shows, podcastStore.status
	oldPanel := podPanel
	oldShowHits, oldEpisodeHits := podcastShowRowHits, podcastEpisodeHits
	t.Cleanup(func() {
		podcastStore.mu.Lock()
		podcastStore.loaded, podcastStore.feeds, podcastStore.shows, podcastStore.status = oldLoaded, oldFeeds, oldShows, oldStatus
		podcastStore.mu.Unlock()
		podPanel = oldPanel
		podcastShowRowHits, podcastEpisodeHits = oldShowHits, oldEpisodeHits
	})
	feeds := make([]string, len(shows))
	byURL := make(map[string]podcastShow, len(shows))
	for i, show := range shows {
		feeds[i] = show.FeedURL
		byURL[show.FeedURL] = show
	}
	podcastStore.mu.Lock()
	podcastStore.loaded = true
	podcastStore.feeds = feeds
	podcastStore.shows = byURL
	podcastStore.status = ""
	podcastStore.mu.Unlock()
	podPanel.open = true
	podPanel.show = 0
	podPanel.column = podcastColumnShows
	podPanel.episode = 0
	podPanel.firstShow, podPanel.firstEpisode = 0, 0
}

func podcastContainmentShows() []podcastShow {
	now := time.Now()
	shows := make([]podcastShow, 0, 8)
	for i := 0; i < 8; i++ {
		episodes := make([]podcastEpisode, 0, 16)
		for j := 0; j < 16; j++ {
			episodes = append(episodes, podcastEpisode{
				Title:     fmt.Sprintf("Episode %d: A very long conversation about the state of handheld audio and what comes next", j+1),
				URL:       fmt.Sprintf("https://example.test/%d/%d.mp3", i, j),
				Published: now.Add(-time.Duration(j) * 24 * time.Hour),
				Duration:  3723,
			})
		}
		shows = append(shows, podcastShow{
			Title:    fmt.Sprintf("The Long Show About Everything, Episode %d", i+1),
			Author:   "Somebody With A Rather Long Name",
			FeedURL:  fmt.Sprintf("https://example.test/%d/feed.xml", i),
			Episodes: episodes,
		})
	}
	return shows
}

// TestPodcastColumnsShowTheirScrollIndicators covers both columns' overflow:
// eight shows and sixteen episodes are taller than the 560 px card, but the
// browser drew no scrollbar, so a D-pad user could not see that either list
// continued past the visible rows.
func TestPodcastColumnsShowTheirScrollIndicators(t *testing.T) {
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	oldW, oldH := screenWidth, screenHeight
	t.Cleanup(func() { screenWidth, screenHeight = oldW, oldH })
	cfg := &Config{}

	for _, c := range menuContainmentProfiles(t) {
		t.Run(c.name, func(t *testing.T) {
			screenWidth, screenHeight = c.width, c.height
			podcastContainmentSeed(t, podcastContainmentShows())
			renderer := menuContainmentRenderer(t, c.width, c.height)
			renderPodcastPanel(renderer, cfg)

			l := podcastPanelLayout
			panel := podcastPanelRect
			for name, bar := range map[string]sdl.Rect{"shows": l.showBar, "episodes": l.epBar} {
				if bar.W <= 0 || bar.H <= 0 {
					t.Fatalf("%s scrollbar was not drawn: %+v", name, bar)
				}
				if bar.X < panel.X || bar.X+bar.W > panel.X+panel.W ||
					bar.Y < panel.Y || bar.Y+bar.H > panel.Y+panel.H {
					t.Fatalf("%s scrollbar = %+v, outside the card %+v", name, bar, panel)
				}
			}
			if l.showBar.X < l.showList.X || l.showBar.X+l.showBar.W > l.showList.X+l.showList.W {
				t.Fatalf("show scrollbar = %+v, outside its column %+v", l.showBar, l.showList)
			}
			if l.epBar.X < l.epList.X || l.epBar.X+l.epBar.W > l.epList.X+l.epList.W {
				t.Fatalf("episode scrollbar = %+v, outside its column %+v", l.epBar, l.epList)
			}
			if l.showBar.Y < l.showList.Y || l.showBar.Y+l.showBar.H > l.showList.Y+l.showList.H {
				t.Fatalf("show scrollbar = %+v, outside its list height %+v", l.showBar, l.showList)
			}
			if l.epBar.Y < l.epList.Y || l.epBar.Y+l.epBar.H > l.epList.Y+l.epList.H {
				t.Fatalf("episode scrollbar = %+v, outside its list height %+v", l.epBar, l.epList)
			}
		})
	}
}

// TestPodcastEmptyStatesUseDrawnGlyphsAndFit covers loading and failure states.
// They passed the bare name "media", which UIEmptyState painted as the literal
// word; a failed feed also stayed in the tertiary empty tone.
func TestPodcastEmptyStatesUseDrawnGlyphsAndFit(t *testing.T) {
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	oldW, oldH := screenWidth, screenHeight
	t.Cleanup(func() { screenWidth, screenHeight = oldW, oldH })
	cfg := &Config{}

	for _, c := range menuContainmentProfiles(t) {
		t.Run(c.name, func(t *testing.T) {
			screenWidth, screenHeight = c.width, c.height
			for _, tc := range []struct {
				name     string
				err      string
				wantIcon string
				wantErr  bool
			}{
				{"loading", "", "@media", false},
				{"error", "feed could not be reached", "@warning", true},
			} {
				shows := []podcastShow{{Title: "A Show", FeedURL: "https://example.test/feed.xml", Err: tc.err}}
				podcastContainmentSeed(t, shows)
				renderer := menuContainmentRenderer(t, c.width, c.height)
				renderPodcastPanel(renderer, cfg)

				gotGlyph, title, body, isError := podcastEpisodeEmptyState(shows[0], true)
				if gotGlyph != tc.wantIcon || isError != tc.wantErr {
					t.Fatalf("%s empty state = %q/%v, want %q/%v", tc.name, gotGlyph, isError, tc.wantIcon, tc.wantErr)
				}
				if title == "" || body == "" {
					t.Fatalf("%s empty state has empty text: %q / %q", tc.name, title, body)
				}
				if h := uiStateBlockHeight(cfg, gotGlyph, title, body); h > podcastPanelLayout.epList.H {
					t.Fatalf("%s empty block height %d, taller than its column %d", tc.name, h, podcastPanelLayout.epList.H)
				}
			}
		})
	}
}
