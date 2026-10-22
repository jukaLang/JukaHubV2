package main

// exit_chord.go adds the handheld's system quit chord: Select + Start held
// together.
//
// On the TrimUI every other launcher quits an app with that pair, and the
// stock firmware maps the physical Select button to SDL's CONTROLLER_BUTTON_BACK
// and Start to CONTROLLER_BUTTON_START. JukaHub checked those buttons only as
// separate actions (leave the scene, open the app menu), so there was no way
// to leave the app from the pad at all — the Exit button on Home was the only
// route, and it needs the mouse on a smaller screen.
//
// The chord is edge-based: it fires when the second of the two buttons goes
// down while the first is still held, and the two presses have to land within
// exitChordWindow. A button that is merely stuck (a missed release event, a
// worn switch) can therefore never quit the app long after the fact.

import (
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// exitChordWindow is how long the first half of the chord stays armed.
	exitChordWindow = 700 * time.Millisecond
)

type exitChordState struct {
	mu    sync.Mutex
	back  bool
	start bool
	// armedAt is when the first of the pair went down; both halves have to
	// land inside exitChordWindow of it.
	armedAt time.Time
	// fired blocks the chord until both buttons have been released: a driver
	// that repeats a held button must not ask the app to quit again.
	fired bool
}

var exitChord exitChordState

// exitChordClock is time.Now in production. Tests replace it so the window can
// be exercised without sleeping.
var exitChordClock = time.Now

// resetExitChord forgets any held buttons (used by tests and after a chord).
// It clears the fields instead of the struct: the mutex lives inside it.
func resetExitChord() {
	exitChord.mu.Lock()
	exitChord.back = false
	exitChord.start = false
	exitChord.armedAt = time.Time{}
	exitChord.fired = false
	exitChord.mu.Unlock()
}

// exitChordPressed feeds one controller button event into the latch and
// reports whether it completed the Select + Start chord.
func exitChordPressed(e *sdl.ControllerButtonEvent) bool {
	if e == nil {
		return false
	}
	button := e.Button
	// Not every firmware names the Select key the same way: some report it as
	// GUIDE, which the rest of the app already treats as Back on a handheld.
	if button == sdl.CONTROLLER_BUTTON_GUIDE {
		button = sdl.CONTROLLER_BUTTON_BACK
	}
	switch button {
	case sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
	default:
		return false
	}
	return exitChord.update(button, e.Type == sdl.CONTROLLERBUTTONDOWN, exitChordClock())
}

func (s *exitChordState) update(button uint8, down bool, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	isBack := button == sdl.CONTROLLER_BUTTON_BACK
	if down {
		// A hold that repeats the same button is not a new press, and a chord
		// that already fired waits for the buttons to come up.
		if s.fired || (isBack && s.back) || (!isBack && s.start) {
			return false
		}
		if isBack {
			s.back = true
		} else {
			s.start = true
		}
		if !s.back || !s.start {
			// First half of the pair: start the window.
			s.armedAt = now
			return false
		}
		armed := !s.armedAt.IsZero()
		within := armed && now.Sub(s.armedAt) <= exitChordWindow
		if within {
			s.fired = true
		}
		s.armedAt = time.Time{}
		return within
	}
	if isBack {
		s.back = false
	} else {
		s.start = false
	}
	if !s.back && !s.start {
		s.armedAt = time.Time{}
		s.fired = false
	}
	return false
}
