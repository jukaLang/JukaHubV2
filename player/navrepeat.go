package main

// navrepeat.go — a held D-pad repeats in menus, not only on the on-screen
// keyboard.
//
// The on-screen keyboard has polled the pad and stepped its highlight for a
// while (vkeyboard.go), but every other screen needed one press per row: walking
// a library of two hundred games or a long save list meant two hundred taps, and
// the analog stick was the only input that repeated (through handleStickDpad).
//
// This file watches the held D-pad and, after a delay, re-queues the same press
// through SDL at a fixed cadence. Re-queueing instead of calling a scene handler
// keeps the router in charge: overlays, modals, the video player and the
// on-screen keyboard all keep seeing ordinary presses, in the order a person
// would have made them, and nothing here has to know which screen is open.
//
// The keyboard keeps its own repeat path (vkTickNavRepeat moves the highlight
// directly), so this stays out of its way while the keyboard is up.

import (
	"github.com/veandco/go-sdl2/sdl"
)

const (
	// navRepeatDelayMs is the pause before a held direction starts repeating; the
	// press itself already moved once, so this only waits for the second step.
	navRepeatDelayMs = 320
	// navRepeatIntervalMs is the cadence after that: fast enough to cross a long
	// list in a couple of seconds, slow enough to stop on the intended row.
	navRepeatIntervalMs = 70
)

// navRepeatTracker follows one held direction. A direction change restarts the
// delay, so switching directions is a normal press and never inherits the old
// cadence. A released direction clears the tracker.
type navRepeatTracker struct {
	dx, dy int
	next   uint64
}

// step advances the tracker for one frame and returns the move to apply, if any.
func (t *navRepeatTracker) step(now uint64, dx, dy int) (int, int) {
	if dx == 0 && dy == 0 {
		*t = navRepeatTracker{}
		return 0, 0
	}
	if dx != t.dx || dy != t.dy {
		*t = navRepeatTracker{dx: dx, dy: dy, next: now + uint64(a11yRepeatMs(navRepeatDelayMs))}
		return 0, 0
	}
	if now < t.next {
		return 0, 0
	}
	t.next = now + uint64(a11yRepeatMs(navRepeatIntervalMs))
	return dx, dy
}

var padNavRepeat navRepeatTracker

// padRepeatHeldDirection reads the direction held on the pad right now.
var padRepeatHeldDirection = vkControllerNavDirection

// padRepeatPush re-queues one D-pad press. Swappable so a test can watch what a
// held pad would queue without a controller or an SDL event loop.
var padRepeatPush = func(button uint8) { pushControllerButtonDown(button) }

// padRepeatNow is the clock the tracker reads. Swappable for the same reason.
var padRepeatNow = func() uint64 { return sdl.GetTicks64() }

// pushControllerButtonDown queues a controller press through SDL so the frame
// loop's router handles it like a physical one. It is the same trick
// handleStickDpad uses for the analog stick.
func pushControllerButtonDown(button uint8) {
	if _, err := sdl.PushEvent(&sdl.ControllerButtonEvent{
		Type:   sdl.CONTROLLERBUTTONDOWN,
		Button: button,
	}); err != nil {
		LogScene("input").Warn("could not queue a repeat press", "button", button, "err", err)
	}
}

// padDirectionButton maps a tracked direction to the button a repeat presses.
// A D-pad cannot report a real diagonal, but the tracker is shared with anything
// that can, so the horizontal axis wins and the vertical step follows on the
// next tick.
func padDirectionButton(dx, dy int) (uint8, bool) {
	switch {
	case dx < 0:
		return sdl.CONTROLLER_BUTTON_DPAD_LEFT, true
	case dx > 0:
		return sdl.CONTROLLER_BUTTON_DPAD_RIGHT, true
	case dy < 0:
		return sdl.CONTROLLER_BUTTON_DPAD_UP, true
	case dy > 0:
		return sdl.CONTROLLER_BUTTON_DPAD_DOWN, true
	}
	return 0, false
}

// padTickRepeat advances the held-pad state once per frame and reports whether it
// queued a repeat press, so the caller can count it as input and keep the idle
// frame cadence from stalling the repeat.
func padTickRepeat() bool {
	// The on-screen keyboard moves its own highlight while a direction is held;
	// queueing a press as well would move twice per tick.
	if virtualKeyboardActive {
		padNavRepeat = navRepeatTracker{}
		return false
	}
	dx, dy := padRepeatHeldDirection()
	stepX, stepY := padNavRepeat.step(padRepeatNow(), dx, dy)
	if stepX == 0 && stepY == 0 {
		return false
	}
	button, ok := padDirectionButton(stepX, stepY)
	if !ok {
		return false
	}
	padRepeatPush(button)
	return true
}

// padResetNavRepeat forgets the held direction, so a repeat does not survive a
// scene that swallowed input or an overlay that closed under a held pad.
func padResetNavRepeat() {
	padNavRepeat = navRepeatTracker{}
}
