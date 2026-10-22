package main

import (
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func exitChordEvent(button uint8, down bool) *sdl.ControllerButtonEvent {
	kind := uint32(sdl.CONTROLLERBUTTONUP)
	if down {
		kind = sdl.CONTROLLERBUTTONDOWN
	}
	return &sdl.ControllerButtonEvent{Type: kind, Button: button}
}

// withExitChordClock pins the chord's clock so tests can move it by hand.
func withExitChordClock(t *testing.T, start time.Time) *time.Time {
	t.Helper()
	now := start
	prev := exitChordClock
	exitChordClock = func() time.Time { return now }
	resetExitChord()
	t.Cleanup(func() {
		exitChordClock = prev
		resetExitChord()
	})
	return &now
}

// Select + Start is the handheld's quit chord: it fires on the second press,
// in either order.
func TestExitChordFiresOnSelectPlusStart(t *testing.T) {
	now := withExitChordClock(t, time.Unix(1000, 0))

	if exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_BACK, true)) {
		t.Fatal("Select alone quit the app")
	}
	*now = now.Add(40 * time.Millisecond)
	if !exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_START, true)) {
		t.Fatal("Select + Start did not quit")
	}

	resetExitChord()
	// The same chord in the other order.
	if exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_START, true)) {
		t.Fatal("Start alone quit the app")
	}
	if !exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_BACK, true)) {
		t.Fatal("Start + Select did not quit")
	}
}

// A button that was pressed long ago (a missed release, a stuck switch) must
// not turn a later single press into a quit.
func TestExitChordNeedsBothPressesWithinTheWindow(t *testing.T) {
	now := withExitChordClock(t, time.Unix(2000, 0))

	if exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_BACK, true)) {
		t.Fatal("Select alone quit the app")
	}
	*now = now.Add(5 * time.Second)
	if exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_START, true)) {
		t.Fatal("a Start press long after Select still quit the app")
	}

	// Releasing and pressing the pair again works.
	exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_BACK, false))
	exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_START, false))
	*now = now.Add(time.Second)
	if exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_BACK, true)) {
		t.Fatal("Select alone quit the app")
	}
	*now = now.Add(40 * time.Millisecond)
	if !exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_START, true)) {
		t.Fatal("a fresh Select + Start did not quit")
	}
}

// Holding both produces exactly one quit, and releasing one half re-arms it.
func TestExitChordDoesNotRepeatWhileHeld(t *testing.T) {
	now := withExitChordClock(t, time.Unix(3000, 0))
	exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_BACK, true))
	if !exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_START, true)) {
		t.Fatal("Select + Start did not quit")
	}
	// Repeats of either button while both are still down are not new chords.
	*now = now.Add(20 * time.Millisecond)
	if exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_START, true)) {
		t.Fatal("a repeat of Start quit again")
	}
	if exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_BACK, true)) {
		t.Fatal("a repeat of Select quit again")
	}

	// Releasing both buttons re-arms the chord for a later use.
	exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_BACK, false))
	exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_START, false))
	*now = now.Add(time.Second)
	exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_BACK, true))
	if !exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_START, true)) {
		t.Fatal("the chord did not work again after a release")
	}
}

// Firmware that reports Select as GUIDE still gets the chord.
func TestExitChordAcceptsGuideAsSelect(t *testing.T) {
	withExitChordClock(t, time.Unix(5000, 0))
	if exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_GUIDE, true)) {
		t.Fatal("GUIDE alone quit the app")
	}
	if !exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_START, true)) {
		t.Fatal("GUIDE + Start did not quit")
	}
}

// Other buttons never arm the chord, and a key-up event is not a press.
func TestExitChordIgnoresOtherButtons(t *testing.T) {
	now := withExitChordClock(t, time.Unix(4000, 0))
	for _, b := range []uint8{
		sdl.CONTROLLER_BUTTON_A,
		sdl.CONTROLLER_BUTTON_X,
		sdl.CONTROLLER_BUTTON_Y,
		sdl.CONTROLLER_BUTTON_LEFTSHOULDER,
		sdl.CONTROLLER_BUTTON_DPAD_UP,
	} {
		if exitChordPressed(exitChordEvent(b, true)) {
			t.Fatalf("button %d completed the quit chord", b)
		}
	}
	if exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_START, false)) {
		t.Fatal("a button release quit the app")
	}
	*now = now.Add(time.Millisecond)
	if exitChordPressed(exitChordEvent(sdl.CONTROLLER_BUTTON_BACK, true)) {
		t.Fatal("Select alone quit the app")
	}
	if exitChordPressed(nil) {
		t.Fatal("a nil event quit the app")
	}
}
