package main

// videoplayer_close_test.go — the two corner actions on a playing video.
//
// Back used to be the only corner button, drawn as an X: pressing the X made the
// video windowed instead of closing it, and once it was windowed the controls
// were drawn but not clickable, so the only way to end playback was Stop at the
// far end of the bar. Close and back are now two buttons, both in the D-pad ring,
// and the windowed player accepts taps inside its own window.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// layoutVideoWindow lays out a windowed player over the Tube scene's video
// element, the way a rendered frame would.
func layoutVideoWindow(t *testing.T) {
	t.Helper()
	// Setup gives a playing video on the Media scene with Home behind it, which
	// is what back needs to float the window onto.
	setupVideoMiniTest(t, miniTestTube)
	resetVideoUIControls(t)
	resetVideoControlState(t)
	videoMiniClear()
	videoFullscreen = false
	computeVideoControlLayout(tubeVideoX, tubeVideoY, tubeVideoW, tubeVideoH, playingSnapshot())
}

// The X in the corner closes the video: playback ends and nothing is left behind.
func TestVideoCloseButtonEndsPlaybackInTheWindow(t *testing.T) {
	layoutVideoWindow(t)

	closeRect := videoControlRects.close
	if closeRect.W <= 0 || closeRect.H <= 0 {
		t.Fatalf("close button was not laid out: %+v", closeRect)
	}
	if !HandleVideoControlClick(closeRect.X+closeRect.W/2, closeRect.Y+closeRect.H/2) {
		t.Fatal("tap on the close button was not consumed")
	}
	if got := playerPhase(); got != "idle" {
		t.Fatalf("close left the player in phase %q, want idle", got)
	}
	if videoMiniActive() {
		t.Error("close left the floating window armed")
	}
	if videoFullscreen {
		t.Error("close left full screen on")
	}
}

// The chevron beside it keeps the old behaviour: step down a size, still
// playing. The two buttons must not be the same target.
func TestVideoBackButtonStillStepsDownInsteadOfClosing(t *testing.T) {
	layoutVideoWindow(t)

	back := videoControlRects.back
	close := videoControlRects.close
	if back == close {
		t.Fatal("close and back share one target")
	}
	if !HandleVideoControlClick(back.X+back.W/2, back.Y+back.H/2) {
		t.Fatal("tap on the back button was not consumed")
	}
	if got := playerPhase(); got != "playing" {
		t.Fatalf("back ended the session: phase=%q", got)
	}
	if !videoMiniActive() {
		t.Fatal("back did not step the video down to the floating window")
	}
}

// Both corner buttons belong to the windowed player: they sit inside the video
// element, and a tap outside it is still the scene's.
func TestVideoWindowedClicksStayInsideTheVideoElement(t *testing.T) {
	layoutVideoWindow(t)

	element := sdl.Rect{X: tubeVideoX, Y: tubeVideoY, W: tubeVideoW, H: tubeVideoH}
	for name, rect := range map[string]sdl.Rect{
		"close": videoControlRects.close,
		"back":  videoControlRects.back,
	} {
		if rect.X < element.X || rect.Y < element.Y ||
			rect.X+rect.W > element.X+element.W || rect.Y+rect.H > element.Y+element.H {
			t.Errorf("%s button %+v escapes the video element %+v", name, rect, element)
		}
	}
	if HandleVideoControlClick(5, 5) {
		t.Error("a click outside the windowed player was consumed")
	}
	if !HandleVideoControlClick(element.X+element.W/2, element.Y+element.H/2) {
		t.Error("a click on the picture was not consumed")
	}
	if got := playerPhase(); got != "playing" {
		t.Fatalf("a click on the picture changed the phase to %q", got)
	}
}

// A pad user must reach both choices without a touchscreen: the ring walks onto
// them and A runs them.
func TestVideoRingReachesCloseAndBack(t *testing.T) {
	layoutVideoWindow(t)

	seen := map[videoControlID]bool{}
	items := videoControlsInOrder()
	for i := 0; i < len(items)*2; i++ {
		moveVideoControlFocus(1)
		seen[videoControlFocus] = true
	}
	for _, want := range []videoControlID{vcClose, vcBack, vcStop} {
		if !seen[want] {
			t.Errorf("the D-pad ring never reached control %v", want)
		}
	}

	// A on back floats the video; A on close ends it.
	videoControlFocus = vcBack
	if !HandleVideoControllerInput(&sdl.ControllerButtonEvent{
		Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A,
	}) {
		t.Fatal("A on the back control was not consumed")
	}
	if got := playerPhase(); got != "playing" || !videoMiniActive() {
		t.Fatalf("A on back ended the session: phase=%q mini=%v", got, videoMiniActive())
	}

	videoMiniClear()
	videoControlFocus = vcClose
	if !HandleVideoControllerInput(&sdl.ControllerButtonEvent{
		Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A,
	}) {
		t.Fatal("A on the close control was not consumed")
	}
	if got := playerPhase(); got != "idle" {
		t.Fatalf("A on close left phase %q, want idle", got)
	}
}
