package main

import "testing"

func TestPlaybackStopOwnsOnlyItsSession(t *testing.T) {
	old := &playbackSession{id: 1, pageURL: "https://example.com/old"}
	current := &playbackSession{id: 2, pageURL: "https://example.com/new"}
	if playbackStopOwns(old, current) {
		t.Fatal("an old session's stop claimed a newer play")
	}
	if !playbackStopOwns(current, current) {
		t.Fatal("the current session's stop did not own the player")
	}
	if !playbackStopOwns(nil, current) {
		t.Fatal("the screen's Stop action did not target the current session")
	}
	if playbackStopOwns(current, nil) {
		t.Fatal("a stopped session owned a player that was already gone")
	}
}

func TestPlaybackPageAccessor(t *testing.T) {
	old := playbackPageURL()
	defer setPlaybackPageURL(old)
	setPlaybackPageURL("https://example.com/page")
	if got := playbackPageURL(); got != "https://example.com/page" {
		t.Fatalf("playback page = %q", got)
	}
	clearPlaybackPageURLIf("https://example.com/other")
	if got := playbackPageURL(); got != "https://example.com/page" {
		t.Fatalf("an unrelated clear removed the page: %q", got)
	}
	clearPlaybackPageURLIf("https://example.com/page")
	if got := playbackPageURL(); got != "" {
		t.Fatalf("the matching page was not cleared: %q", got)
	}
}
