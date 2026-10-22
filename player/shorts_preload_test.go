package main

// The Shorts feed has to keep at least four videos resolved ahead of the one
// playing, the Media screen has to carry a button that really opens it, and
// N/P have to skip through the list without a modifier.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func shortsTestFeed(n int) []VideoInfo {
	videos := make([]VideoInfo, 0, n)
	for i := 0; i < n; i++ {
		videos = append(videos, VideoInfo{
			ID:         string(rune('a' + i)),
			Title:      "short " + string(rune('a'+i)),
			WebpageURL: "https://youtu.be/" + string(rune('a'+i)),
		})
	}
	return videos
}

// The ahead window starts after the current short and covers four videos, and
// it shrinks at the end of the feed instead of asking for videos that do not
// exist.
func TestShortsAheadURLsCoversFourVideos(t *testing.T) {
	videos := shortsTestFeed(7)
	urls := shortsAheadURLs(videos, 1, shortsPreloadAhead)
	if len(urls) != 4 {
		t.Fatalf("window from 1 = %d urls (%v), want 4", len(urls), urls)
	}
	if urls[0] != videos[2].GetURL() || urls[3] != videos[5].GetURL() {
		t.Fatalf("window covers %v, want the videos after the current one", urls)
	}
	if got := shortsAheadURLs(videos, 5, shortsPreloadAhead); len(got) != 1 {
		t.Fatalf("window at the end = %d urls, want the one remaining video", len(got))
	}
	if got := shortsAheadURLs(videos, 6, shortsPreloadAhead); len(got) != 0 {
		t.Fatalf("window after the last short = %v, want none", got)
	}
	// Videos without a URL (a broken feed entry) are not queued.
	broken := []VideoInfo{{ID: "x"}, {ID: "y", WebpageURL: "https://youtu.be/y"}}
	if got := shortsAheadURLs(broken, 0, 4); len(got) != 1 || got[0] != "https://youtu.be/y" {
		t.Fatalf("window of a feed with a broken entry = %v", got)
	}
}

// Queuing waits for the player to actually run, and asks once per short.
func TestEnsureShortsPreloadQueuesFourAheadOnce(t *testing.T) {
	prevAutoplay, prevIdx, prevQueued := shortsAutoplay, currentShortIdx, shortsPreloadIdx
	prevQueue := shortsQueuePrefetch
	t.Cleanup(func() {
		shortsAutoplay, currentShortIdx, shortsPreloadIdx = prevAutoplay, prevIdx, prevQueued
		shortsQueuePrefetch = prevQueue
	})

	var queued [][]string
	shortsQueuePrefetch = func(_ *Config, urls []string) { queued = append(queued, urls) }

	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"shorts_list": shortsTestFeed(6)}
	shortsAutoplay = true
	currentShortIdx = 0
	shortsPreloadIdx = -1

	// While the short is still being resolved nothing is queued: that work
	// would be cancelled the moment the player starts.
	setVideoPhaseForTest(t, "downloading")
	ensureShortsPreload(config)
	if len(queued) != 0 {
		t.Fatalf("queued %v while the short was still preparing", queued)
	}

	setVideoPhaseForTest(t, "playing")
	ensureShortsPreload(config)
	if len(queued) != 1 {
		t.Fatalf("preload windows queued = %d, want 1", len(queued))
	}
	if len(queued[0]) != shortsPreloadAhead {
		t.Fatalf("ahead window = %v, want %d urls", queued[0], shortsPreloadAhead)
	}
	if shortsPreloadIdx != 0 {
		t.Fatalf("queued index = %d, want 0", shortsPreloadIdx)
	}

	// The same short must not queue the same window on every frame.
	ensureShortsPreload(config)
	if len(queued) != 1 {
		t.Fatalf("the same short queued %d windows", len(queued))
	}

	// Moving to the next short queues its own window.
	currentShortIdx = 1
	ensureShortsPreload(config)
	if len(queued) != 2 {
		t.Fatalf("a new short did not queue its window (%d windows)", len(queued))
	}

	// An unarmed feed (the user left Shorts) queues nothing more.
	shortsAutoplay = false
	currentShortIdx = 2
	ensureShortsPreload(config)
	if len(queued) != 2 {
		t.Fatalf("an unarmed feed queued %d windows", len(queued))
	}
}

// The Media screen must offer the feed as a button, not only as a hidden chord
// or the Y button, and the button has to sit inside the button row.
func TestMediaSceneHasAWorkingShortsButton(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	idx := sceneIndexExact(config, "Tube")
	if idx < 0 {
		t.Fatal("config has no Media scene")
	}
	var button *Element
	for i := range config.Scenes[idx].Elements {
		element := &config.Scenes[idx].Elements[i]
		if element.Trigger == triggerShortsOpen {
			button = element
			break
		}
	}
	if button == nil {
		t.Fatalf("Media has no button with trigger %q", triggerShortsOpen)
	}
	if button.Text != "Shorts" {
		t.Errorf("button text = %q, want Shorts", button.Text)
	}
	if button.X < 0 || button.X+getElementWidth(*button, 116) > screenWidth-24 {
		t.Errorf("the Shorts button sits outside the button row (x=%d w=%s)", button.X, button.Width)
	}

	// The trigger must really open the feed: with a build that has the Shorts
	// scene, pressing it arms the feed even before the list arrives.
	prevAutoplay, prevPending := shortsAutoplay, shortsPendingStart
	prevScene := currentSceneIndex
	t.Cleanup(func() {
		shortsAutoplay, shortsPendingStart = prevAutoplay, prevPending
		currentSceneIndex = prevScene
	})
	config.Variables.Custom = map[string]interface{}{}
	shortsAutoplay, shortsPendingStart = false, false
	if !openShortsFeed(config) {
		t.Fatal("the Shorts button opened nothing")
	}
	if !shortsAutoplay || !shortsPendingStart {
		t.Fatalf("after the button: autoplay=%v pending=%v, want both armed", shortsAutoplay, shortsPendingStart)
	}
}

// N and P skip through the launching list without a modifier. A one-video list
// reports the end instead of starting playback, so this stays a pure check.
func TestVideoKeysSkipToTheNextAndPreviousVideo(t *testing.T) {
	resetVideoControlState(t)
	setVideoPhaseForTest(t, "playing")
	prevQueue := playbackQueue
	t.Cleanup(func() { playbackQueue = prevQueue })
	playbackQueue = playbackQueueState{videos: []VideoInfo{{Title: "only"}}, index: 0}

	press := func(sym sdl.Keycode) bool {
		return HandleVideoKeyInput(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}})
	}
	if !press(sdl.K_n) {
		t.Error("N was not consumed by the player")
	}
	if !press(sdl.K_p) {
		t.Error("P was not consumed by the player")
	}
	if playbackQueue.index != 0 {
		t.Fatalf("skipping past the ends moved the queue to %d", playbackQueue.index)
	}

	// With no list behind the video the keys still belong to the player.
	playbackQueue = playbackQueueState{index: -1}
	if !press(sdl.K_n) {
		t.Error("N was not consumed with no list behind the video")
	}
}
