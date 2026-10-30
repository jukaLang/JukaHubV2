package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// resetVideoControlState puts the player's control state back to "nothing
// focused" and restores it after the test.
func resetVideoControlState(t *testing.T) {
	t.Helper()
	prevFocus, prevSheet, prevItem, prevRect := videoControlFocus, videoSettingsOpen, videoSettingsItem, videoControlRects
	t.Cleanup(func() {
		videoControlFocus, videoSettingsOpen, videoSettingsItem, videoControlRects = prevFocus, prevSheet, prevItem, prevRect
	})
	videoControlFocus = vcNone
	videoSettingsOpen = false
	videoSettingsItem = 0
}

// The first D-pad press must land next to play/pause: a ring that starts on
// Stop would end the video on the first A press.
func TestVideoControlRingStartsAtPlayPause(t *testing.T) {
	resetVideoControlState(t)

	moveVideoControlFocus(1)
	if videoControlFocus != vcSeekFwd {
		t.Fatalf("right from no ring focused %v, want seek-forward", videoControlFocus)
	}
	videoControlFocus = vcNone
	moveVideoControlFocus(-1)
	if videoControlFocus != vcSeekBack {
		t.Fatalf("left from no ring focused %v, want seek-back", videoControlFocus)
	}
}

// The ring ends on the corner actions: Stop is the leftmost button, and the
// chevron that steps the player down a size is the rightmost after Close.
func TestVideoControlRingWrapsAtBothEnds(t *testing.T) {
	resetVideoControlState(t)

	videoControlFocus = vcStop
	moveVideoControlFocus(-1)
	if videoControlFocus != vcBack {
		t.Fatalf("left from the first control focused %v, want the last", videoControlFocus)
	}
	videoControlFocus = vcBack
	moveVideoControlFocus(1)
	if videoControlFocus != vcStop {
		t.Fatalf("right from the last control focused %v, want the first", videoControlFocus)
	}
}

// The ring has to be drawn exactly over the button it selects, so the rect comes
// from the same layout the click hit test uses.
func TestVideoControlRingRectMatchesLayout(t *testing.T) {
	resetVideoControlState(t)
	computeVideoControlLayout(0, 0, 1280, 720, videoStateSnapshot{phase: "playing"})

	videoControlFocus = vcPlayPause
	rect, ok := videoControlFocusedRect()
	if !ok {
		t.Fatal("the focused control has no rect")
	}
	if rect != videoControlRects.playPause {
		t.Fatalf("ring rect %+v, want the play/pause rect %+v", rect, videoControlRects.playPause)
	}

	videoControlFocus = vcNone
	if _, ok := videoControlFocusedRect(); ok {
		t.Error("an empty ring reported a rect")
	}
}

func TestClampSeekFraction(t *testing.T) {
	cases := []struct {
		name     string
		progress float64
		duration float64
		seconds  float64
		want     float64
	}{
		{"forward inside the clip", 0.5, 600, 10, 0.5 + 10.0/600},
		{"back inside the clip", 0.5, 600, -10, 0.5 - 10.0/600},
		{"back past the start clamps to zero", 0.01, 600, -10, 0},
		{"forward past the end clamps to one", 0.99, 600, 10, 1},
		{"unknown duration stays at the start", 0.5, 0, 10, 0},
	}
	for _, tc := range cases {
		if got := clampSeekFraction(tc.progress, tc.duration, tc.seconds); got != tc.want {
			t.Errorf("%s: clampSeekFraction(%v, %v, %v) = %v, want %v",
				tc.name, tc.progress, tc.duration, tc.seconds, got, tc.want)
		}
	}
}

func TestVideoSettingsItemWraps(t *testing.T) {
	resetVideoControlState(t)

	moveVideoSettingsItem(-1)
	if videoSettingsItem != videoSettingsSheetRows-1 {
		t.Fatalf("up from the first row selected %d, want %d", videoSettingsItem, videoSettingsSheetRows-1)
	}
	moveVideoSettingsItem(1)
	if videoSettingsItem != 0 {
		t.Fatalf("down from the last row selected %d, want 0", videoSettingsItem)
	}
}

// The ring has to be visible, not just tracked: a D-pad user needs to see which
// control A will press. It is drawn 4px outside the button, over the dark bar.
func TestVideoControlRingIsDrawn(t *testing.T) {
	prevFocus := videoControlFocus
	t.Cleanup(func() { videoControlFocus = prevFocus })

	countOutside := func(f *probeFrame, r sdl.Rect) int {
		n := 0
		for y := r.Y - 6; y < r.Y+r.H+6; y++ {
			for x := r.X - 6; x < r.X+r.W+6; x++ {
				if x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H {
					continue
				}
				p := f.at(x, y)
				if overlayAbs(int32(p.R)-int32(ColorInfo.R)) <= 60 &&
					overlayAbs(int32(p.G)-int32(ColorInfo.G)) <= 60 &&
					overlayAbs(int32(p.B)-int32(ColorInfo.B)) <= 60 {
					n++
				}
			}
		}
		return n
	}

	videoControlFocus = vcNone
	baseline := videoOverlayProbe(t, true, true)
	ringRect := videoControlRects.playPause
	quiet := countOutside(baseline, ringRect)

	videoControlFocus = vcPlayPause
	focused := videoOverlayProbe(t, true, true)
	drawn := countOutside(focused, ringRect)

	if drawn == 0 {
		t.Fatalf("no ring pixels around the play/pause button %v", ringRect)
	}
	if drawn <= quiet {
		t.Fatalf("ring not distinguishable: %d accent pixels around the button, %d without a ring", drawn, quiet)
	}
}

// setVideoPhaseForTest swaps the player into a phase and restores it after.
func setVideoPhaseForTest(t *testing.T, phase string) {
	t.Helper()
	embeddedPlayer.mu.Lock()
	prev := embeddedPlayer.phase
	embeddedPlayer.phase = phase
	embeddedPlayer.mu.Unlock()
	t.Cleanup(func() {
		embeddedPlayer.mu.Lock()
		embeddedPlayer.phase = prev
		embeddedPlayer.mu.Unlock()
	})
}

// While a video is on screen the D-pad belongs to the player. Unconsumed
// presses used to move the results grid behind the video as well.
func TestVideoKeysAreConsumedWhilePlaying(t *testing.T) {
	resetVideoControlState(t)
	setVideoPhaseForTest(t, "playing")

	key := func(sym sdl.Keycode) bool {
		return HandleVideoKeyInput(&sdl.KeyboardEvent{
			Type:   sdl.KEYDOWN,
			Keysym: sdl.Keysym{Sym: sym},
		})
	}
	if !key(sdl.K_RIGHT) {
		t.Error("right was not consumed while a video is playing")
	}
	if videoControlFocus != vcSeekFwd {
		t.Fatalf("right focused %v, want seek-forward", videoControlFocus)
	}
	if !key(sdl.K_LEFT) {
		t.Error("left was not consumed while a video is playing")
	}
	if videoControlFocus != vcPlayPause {
		t.Fatalf("left focused %v, want play/pause", videoControlFocus)
	}
	if key(sdl.K_z) {
		t.Error("an unrelated key was consumed by the player")
	}
	if key(sdl.K_ESCAPE) != true {
		t.Error("escape was not consumed by the player")
	}
}

func TestVideoKeysPassThroughWhenIdle(t *testing.T) {
	resetVideoControlState(t)
	setVideoPhaseForTest(t, "idle")
	if HandleVideoKeyInput(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_RIGHT}}) {
		t.Error("the player consumed a key with no video on screen")
	}
	if HandleVideoControllerInput(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A}) {
		t.Error("the player consumed a controller press with no video on screen")
	}
}

func TestVideoControllerRingAndSettingsSheet(t *testing.T) {
	resetVideoControlState(t)
	setVideoPhaseForTest(t, "playing")

	press := func(b uint8) bool {
		return HandleVideoControllerInput(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: b})
	}
	if !press(sdl.CONTROLLER_BUTTON_DPAD_LEFT) {
		t.Fatal("d-pad left was not consumed while playing")
	}
	if videoControlFocus != vcSeekBack {
		t.Fatalf("d-pad left focused %v, want seek-back", videoControlFocus)
	}

	// Opening the sheet and moving through it must not stop playback, and B
	// closes the sheet instead of ending the video.
	videoSettingsOpen = true
	videoSettingsItem = 0
	if !press(sdl.CONTROLLER_BUTTON_DPAD_DOWN) {
		t.Fatal("d-pad down was not consumed by the settings sheet")
	}
	if videoSettingsItem != 1 {
		t.Fatalf("settings item = %d, want 1", videoSettingsItem)
	}
	if !press(sdl.CONTROLLER_BUTTON_B) {
		t.Fatal("B was not consumed by the settings sheet")
	}
	if videoSettingsOpen {
		t.Error("B did not close the settings sheet")
	}
	if playerPhase() != "playing" {
		t.Errorf("closing the sheet stopped playback (phase %q)", playerPhase())
	}
}

// A finished video used to leave the phase at "playing", so the frozen last
// frame kept the player over the scene. Ending must release it.
func TestPlaybackEndReleasesThePlayer(t *testing.T) {
	prevPhase := playerPhase()
	embeddedPlayer.mu.Lock()
	embeddedPlayer.phase = "playing"
	embeddedPlayer.mu.Unlock()
	t.Cleanup(func() {
		StopEmbeddedPlayback()
		embeddedPlayer.mu.Lock()
		embeddedPlayer.phase = prevPhase
		embeddedPlayer.mu.Unlock()
	})
	if prevPhase == "idle" {
		embeddedPlayer.mu.Lock()
		embeddedPlayer.phase = "playing"
		embeddedPlayer.mu.Unlock()
	}

	playbackEnded.Store(true)
	videoSyncPlaybackEnd()

	if phase := playerPhase(); phase != "idle" {
		t.Fatalf("phase after end of stream = %q, want idle", phase)
	}
	if videoControlFocus != vcNone {
		t.Error("the control ring survived the end of playback")
	}
	if playbackEnded.Load() {
		t.Error("the end-of-stream flag was not consumed")
	}
}

// A cancelled decoder (stop, seek, next video) must not raise the end flag.
func TestStoppedPlaybackDoesNotRaiseEndFlag(t *testing.T) {
	playbackEnded.Store(false)
	t.Cleanup(func() { playbackEnded.Store(false) })

	// Nothing ran to EOF in this test, so a sync must be a no-op.
	videoSyncPlaybackEnd()
	if playbackEnded.Load() {
		t.Error("sync raised the end flag on its own")
	}
}
