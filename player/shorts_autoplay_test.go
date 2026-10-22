package main

import "testing"

func TestNextShortIndexWalksTheFeed(t *testing.T) {
	if next, ok := nextShortIndex(0, 3); !ok || next != 1 {
		t.Fatalf("next from 0 = (%d, %v), want (1, true)", next, ok)
	}
	if next, ok := nextShortIndex(2, 3); ok {
		t.Fatalf("next from the last short = (%d, %v), want no next", next, ok)
	}
	if _, ok := nextShortIndex(-1, 0); ok {
		t.Fatal("an empty feed must not yield a next short")
	}
}

// The Media chord must not pretend it opened a feed when the build has no
// Shorts scene, and it must not start playback on its own.
func TestOpenShortsFeedWithoutSceneIsRefused(t *testing.T) {
	config := &Config{}
	if openShortsFeed(config) {
		t.Fatal("openShortsFeed succeeded without a Shorts scene")
	}
	if shortsAutoplay || shortsPendingStart {
		t.Fatal("a refused feed armed autoplay")
	}
	if playerPhase() != "idle" {
		t.Fatalf("player phase = %q, want idle", playerPhase())
	}
}

// A feed that is still downloading waits for its list instead of playing an
// empty index or poking the player.
func TestShortsAutoplayWaitsForTheFeed(t *testing.T) {
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	config.Scenes = []SceneConfig{
		{Name: "Main"},
		{Name: "Shorts", Elements: []Element{{Type: "shortslist"}}},
	}
	prevScene := currentSceneIndex
	currentSceneIndex = 1 // the Shorts screen, waiting for its list
	t.Cleanup(func() { currentSceneIndex = prevScene })
	shortsAutoplay = true
	shortsPendingStart = true
	shortsAutoAdvance = false
	currentShortIdx = -1

	drainShortsAutoplay(config)
	if !shortsPendingStart {
		t.Fatal("pending start was cleared without a feed")
	}
	if shortsAutoAdvance {
		t.Fatal("drain advanced playback with an empty feed")
	}

	// Backing out of the Shorts screen drops the deferred start instead of
	// opening a video behind the next screen.
	currentSceneIndex = 0
	drainShortsAutoplay(config)
	if shortsPendingStart {
		t.Fatal("pending start survived leaving the Shorts screen")
	}
	if shortsAutoplay {
		t.Fatal("the feed stayed armed after leaving the Shorts screen")
	}
	shortsAutoplay = false
	shortsPendingStart = false
}

// A short that reaches its natural end asks the main loop to start the next
// one; a deliberate stop must not.
func TestShortEndRequestsTheNextOne(t *testing.T) {
	shortsAutoplay = true
	shortsAutoAdvance = false
	playbackMu.Lock()
	playbackCurrent = nil
	playbackMu.Unlock()
	prevPhase := playerPhase()
	embeddedPlayer.mu.Lock()
	embeddedPlayer.phase = "playing"
	embeddedPlayer.mu.Unlock()
	playbackEnded.Store(true)

	videoSyncPlaybackEnd()
	if !shortsAutoAdvance {
		t.Fatal("the end of a short did not request the next one")
	}
	if playerPhase() != "idle" {
		t.Fatalf("player phase = %q, want idle after the end", playerPhase())
	}

	shortsAutoAdvance = false
	shortsAutoplay = false
	// A stop (playbackEnded stays false) does not advance the feed.
	embeddedPlayer.mu.Lock()
	embeddedPlayer.phase = "playing"
	embeddedPlayer.mu.Unlock()
	shortsAutoplay = true
	videoSyncPlaybackEnd()
	if shortsAutoAdvance {
		t.Fatal("a manual stop advanced the Shorts feed")
	}
	shortsAutoplay = false
	embeddedPlayer.mu.Lock()
	embeddedPlayer.phase = prevPhase
	embeddedPlayer.mu.Unlock()
}

// Reaching the end of the list ends autoplay instead of replaying the first
// short or leaving the player armed.
func TestShortsAutoplayStopsAtTheEndOfTheFeed(t *testing.T) {
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{
		"shorts_list": []VideoInfo{{Title: "only one"}},
	}
	shortsAutoplay = true
	shortsAutoAdvance = true
	shortsPendingStart = false
	currentShortIdx = 0

	drainShortsAutoplay(config)
	if shortsAutoplay {
		t.Fatal("the feed stayed armed after the last short")
	}
	if shortsAutoAdvance {
		t.Fatal("the advance request was not consumed")
	}
	if playerPhase() != "idle" {
		t.Fatalf("player phase = %q, want idle", playerPhase())
	}
}
