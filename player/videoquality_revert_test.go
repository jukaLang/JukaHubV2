package main

// videoquality_revert_test.go — the Quality row has to describe the stream that
// is actually on screen.
//
// A switch writes its new ceiling before the re-resolve starts, because the
// yt-dlp format selector reads it at that moment. That is fine while the switch
// plays, but B cancels the resolve — deliberately, so the video that was already
// playing carries on — and the row used to keep the cancelled ceiling. These
// tests pin the repair: the ceiling the picture was started with is committed by
// startEmbeddedPlayback, and a switch that never reaches the player puts it back.

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// qualityTestState keeps the session globals these tests touch, so one case
// cannot leak a pin or a page URL into the next.
func qualityTestState(t *testing.T) {
	t.Helper()
	prevURL := currentPlaybackURL
	prevSeq := videoPrepare.seq
	setVideoQualityOverride("", 0)
	t.Cleanup(func() {
		currentPlaybackURL = prevURL
		setVideoQualityOverride("", 0)
		videoPrepareMu.Lock()
		if videoPrepare.cancel != nil {
			videoPrepare.cancel()
		}
		videoPrepare = videoPrepareState{seq: prevSeq}
		videoPrepareMu.Unlock()
	})
}

// TestCancelledQualitySwitchThroughTheRealPath drives the switch itself, not
// just the decision helper: the press commits the new ceiling, the resolve is
// cancelled by B before it reaches the player, and the row has to end up on the
// ceiling the picture was started with.
func TestCancelledQualitySwitchThroughTheRealPath(t *testing.T) {
	qualityTestState(t)
	prevResolve := resolveQualityStream
	resolveQualityStream = func(seq uint64, ctx context.Context, config *Config, url string, pos float64, label string) bool {
		// What B leaves behind: the resolve stops before the player is touched,
		// so the video already on screen keeps playing.
		cancelVideoPrepare()
		return false
	}
	t.Cleanup(func() { resolveQualityStream = prevResolve })

	const page = "https://youtu.be/qualitycancel0"
	config := &Config{}
	currentPlaybackURL = page
	commitVideoQualityPlayback(page, 360)
	setVideoQualityOverride(page, 1080)

	// 360 is what the switch replaced, which is also what the picture started
	// at; a stream this build did not start would fall back to it.
	restartVideoAtQuality(config, page, 42, 360)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if videoQualityOverrideFor(page) == 360 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := videoQualityOverrideFor(page); got != 360 {
		t.Fatalf("after a cancelled switch the row still carries %d, want the playing 360", got)
	}
	if got := videoQualityLabel(config); got != "360p (this video)" {
		t.Fatalf("Quality row reads %q, want the ceiling that is playing", got)
	}
}

// TestCancelledQualitySwitchRestoresThePlayingCeiling is the reported case: the
// row showed 1080p after B cancelled the switch, while the stream on screen was
// still the 360p one the video started at.
func TestCancelledQualitySwitchRestoresThePlayingCeiling(t *testing.T) {
	qualityTestState(t)
	const page = "https://youtu.be/qualitycancel1"
	config := &Config{}
	config.Variables.PlaybackResolution = ""

	// The video is playing: it started at 360p.
	currentPlaybackURL = page
	commitVideoQualityPlayback(page, 360)
	if got := effectivePlaybackMaxHeight(config, page); got != 720 {
		t.Fatalf("the baseline resolved at %d, want the global 720", got)
	}

	// The user cycles to 1080p, which pins it and starts a re-resolve.
	setVideoQualityOverride(page, 1080)
	if got := videoQualityLabel(config); got != "1080p (this video)" {
		t.Fatalf("the press did not move the row: %q", got)
	}
	seq, _ := beginVideoPrepare(page)
	// B cancels the resolve; cancellation keeps the sequence, because nothing
	// newer has taken the row over.
	if !cancelVideoPrepare() {
		t.Fatal("nothing was in flight to cancel")
	}
	if !revertVideoQualitySwitch(seq, page, 360) {
		t.Fatal("a cancelled switch did not restore the ceiling")
	}

	if got := effectivePlaybackMaxHeight(config, page); got != 360 {
		t.Fatalf("the cancelled switch left the ceiling at %d, want the playing 360", got)
	}
	if got := videoQualityLabel(config); got != "360p (this video)" {
		t.Fatalf("Quality row reads %q after a cancelled switch, want the playing ceiling", got)
	}
}

// TestRevertedSwitchRestoresTheStartedCeilingNotThePressedOne covers the ladder:
// a stream started at Auto (no pin), the user stepped through two ceilings and
// cancelled the second. The row must go back to Auto, not to the first step.
func TestRevertedSwitchRestoresTheStartedCeilingNotThePressedOne(t *testing.T) {
	qualityTestState(t)
	const page = "https://youtu.be/qualitycancel2"
	config := &Config{}

	currentPlaybackURL = page
	commitVideoQualityPlayback(page, 0)

	// First step: 480p, resolve cancelled. The row is back to Auto.
	setVideoQualityOverride(page, 480)
	seq1, _ := beginVideoPrepare(page)
	cancelVideoPrepare()
	if !revertVideoQualitySwitch(seq1, page, 0) {
		t.Fatal("the first cancelled switch did not restore the ceiling")
	}
	if got := effectivePlaybackMaxHeight(config, page); got != 720 {
		t.Fatalf("after the first cancel the ceiling is %d, want Auto's 720", got)
	}

	// Second step starts from Auto, so its fallback is Auto as well, and the
	// committed ceiling is still what has to win.
	setVideoQualityOverride(page, 360)
	seq2, _ := beginVideoPrepare(page)
	cancelVideoPrepare()
	if !revertVideoQualitySwitch(seq2, page, 0) {
		t.Fatal("the second cancelled switch did not restore the ceiling")
	}
	if got := videoQualityOverrideFor(page); got != 0 {
		t.Fatalf("the row kept a pin of %d after cancelling back to the playing stream", got)
	}
}

// TestQualityRevertLeavesANewerLoadAlone: if Play on another card has already
// started, the cancelled switch's late finish must not rewrite the row that load
// now owns.
func TestQualityRevertLeavesANewerLoadAlone(t *testing.T) {
	qualityTestState(t)
	const page = "https://youtu.be/qualitycancel3"
	const other = "https://youtu.be/qualitycancel4"

	currentPlaybackURL = page
	commitVideoQualityPlayback(page, 360)
	setVideoQualityOverride(page, 1080)
	oldSeq, _ := beginVideoPrepare(page)

	// A newer load begins while the switch is still resolving.
	newSeq, _ := beginVideoPrepare(other)
	if newSeq == oldSeq {
		t.Fatal("beginVideoPrepare reused a sequence number")
	}
	if revertVideoQualitySwitch(oldSeq, page, 360) {
		t.Fatal("a superseded switch restored its ceiling over a newer load")
	}
	if got := videoQualityOverrideFor(page); got != 1080 {
		t.Fatalf("the superseded switch changed the row to %d", got)
	}
}

// TestQualityRevertIgnoresAnotherSource: once the player has moved on, an old
// switch finishing must not pin the ceiling it was carrying onto the new video.
func TestQualityRevertIgnoresAnotherSource(t *testing.T) {
	qualityTestState(t)
	const page = "https://youtu.be/qualitycancel5"
	const other = "https://youtu.be/qualitycancel6"

	currentPlaybackURL = page
	commitVideoQualityPlayback(page, 360)
	setVideoQualityOverride(page, 1080)
	seq, _ := beginVideoPrepare(page)

	// Playback moves to another video, which re-syncs the override source.
	currentPlaybackURL = other
	syncVideoQualitySource(other)

	if revertVideoQualitySwitch(seq, page, 360) {
		t.Fatal("a switch restored a ceiling for a video that is no longer playing")
	}
	if got := videoQualityOverrideFor(other); got != 0 {
		t.Fatalf("the new video inherited a pin of %d", got)
	}
}

// TestQualityRevertFallsBackToTheReplacedCeiling covers a stream this build did
// not start (a Watch Party stream, or playback adopted from another device):
// nothing is known about its ceiling, so the row keeps the value the switch
// replaced rather than guessing Auto.
func TestQualityRevertFallsBackToTheReplacedCeiling(t *testing.T) {
	qualityTestState(t)
	// A source nothing in this process could have committed: the commit only
	// happens when a stream on that source starts, and this one never does.
	page := fmt.Sprintf("https://youtu.be/uncommitted-%d", time.Now().UnixNano())

	currentPlaybackURL = page
	if _, ok := playingVideoQuality(page); ok {
		t.Fatalf("a fresh source already has a committed ceiling, so the fallback cannot be checked")
	}
	setVideoQualityOverride(page, 720)
	seq, _ := beginVideoPrepare(page)
	cancelVideoPrepare()
	if !revertVideoQualitySwitch(seq, page, 480) {
		t.Fatal("an unknown playing ceiling did not fall back to the replaced one")
	}
	if got := videoQualityOverrideFor(page); got != 480 {
		t.Fatalf("fallback restored %d, want the replaced 480", got)
	}
}
