package main

// A held D-pad has to move one row, wait, then keep moving, and it must stop the
// moment it is released. These tests drive the tracker and the press queue with
// fake seams: no controller, no event loop, no waiting on a clock.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// withPadRepeatSeams replaces the pad seams for one test and restores them.
func withPadRepeatSeams(t *testing.T, held func() (int, int), now func() uint64) *[]uint8 {
	t.Helper()
	prevHeld, prevNow, prevPush := padRepeatHeldDirection, padRepeatNow, padRepeatPush
	var pushed []uint8
	padRepeatHeldDirection = held
	padRepeatNow = now
	padRepeatPush = func(button uint8) { pushed = append(pushed, button) }
	padNavRepeat = navRepeatTracker{}
	t.Cleanup(func() {
		padRepeatHeldDirection, padRepeatNow, padRepeatPush = prevHeld, prevNow, prevPush
		padNavRepeat = navRepeatTracker{}
	})
	return &pushed
}

func TestPadRepeatTrackerCadence(t *testing.T) {
	var tracker navRepeatTracker
	const start = uint64(1000)

	// The press that started the hold already moved; the tracker only owns the
	// second step onward.
	if dx, dy := tracker.step(start, 1, 0); dx != 0 || dy != 0 {
		t.Fatalf("a fresh hold stepped immediately: %d,%d", dx, dy)
	}
	if dx, _ := tracker.step(start+uint64(navRepeatDelayMs)-1, 1, 0); dx != 0 {
		t.Error("the hold repeated before the delay was up")
	}
	if dx, _ := tracker.step(start+uint64(navRepeatDelayMs), 1, 0); dx != 1 {
		t.Error("the hold did not repeat at the delay")
	}
	if dx, _ := tracker.step(start+uint64(navRepeatDelayMs)+1, 1, 0); dx != 0 {
		t.Error("the hold repeated twice in one interval")
	}
	at := start + uint64(navRepeatDelayMs) + uint64(navRepeatIntervalMs)
	if dx, _ := tracker.step(at, 1, 0); dx != 1 {
		t.Error("the hold stopped repeating after one step")
	}

	// A direction change restarts the delay instead of inheriting the cadence.
	if dx, dy := tracker.step(at+10, 0, 1); dx != 0 || dy != 0 {
		t.Error("a direction change repeated immediately")
	}
	if _, dy := tracker.step(at+10+uint64(navRepeatDelayMs), 0, 1); dy != 1 {
		t.Error("the new direction never repeated")
	}

	// Releasing clears the tracker: the next hold starts with a full delay.
	tracker.step(at+500, 0, 0)
	if dx, _ := tracker.step(at+520, 1, 0); dx != 0 {
		t.Error("a released direction kept its cadence")
	}
}

func TestPadTickRepeatQueuesPresses(t *testing.T) {
	now := uint64(5000)
	heldX, heldY := 1, 0
	pushed := withPadRepeatSeams(t,
		func() (int, int) { return heldX, heldY },
		func() uint64 { return now })

	if padTickRepeat() {
		t.Fatal("the first frame of a hold queued a press")
	}
	now += uint64(navRepeatDelayMs) - 1
	if padTickRepeat() {
		t.Fatal("the hold repeated before the delay was up")
	}
	now += 1
	if !padTickRepeat() {
		t.Fatal("the hold never repeated")
	}
	if len(*pushed) != 1 || (*pushed)[0] != sdl.CONTROLLER_BUTTON_DPAD_RIGHT {
		t.Fatalf("queued %v, want one D-pad right press", *pushed)
	}
	now += uint64(navRepeatIntervalMs)
	if !padTickRepeat() {
		t.Fatal("the hold stopped repeating after one press")
	}
	if len(*pushed) != 2 {
		t.Fatalf("queued %d presses, want 2", len(*pushed))
	}

	// Releasing stops the repeats, and holding again restarts with the delay.
	heldX, heldY = 0, 0
	now += 10
	if padTickRepeat() {
		t.Fatal("a released pad kept repeating")
	}
	heldX = -1
	now += 10
	if padTickRepeat() {
		t.Fatal("the next hold repeated before its own delay")
	}
	now += uint64(navRepeatDelayMs)
	if !padTickRepeat() {
		t.Fatal("the next hold never repeated")
	}
	if got := (*pushed)[len(*pushed)-1]; got != sdl.CONTROLLER_BUTTON_DPAD_LEFT {
		t.Fatalf("queued %d, want the left button", got)
	}
}

func TestPadTickRepeatPrefersHorizontal(t *testing.T) {
	now := uint64(9000)
	pushed := withPadRepeatSeams(t,
		func() (int, int) { return 1, 1 },
		func() uint64 { return now })
	padTickRepeat()
	now += uint64(navRepeatDelayMs)
	if !padTickRepeat() {
		t.Fatal("a diagonal hold never repeated")
	}
	if len(*pushed) != 1 || (*pushed)[0] != sdl.CONTROLLER_BUTTON_DPAD_RIGHT {
		t.Fatalf("queued %v, want a horizontal press first", *pushed)
	}
}

func TestPadTickRepeatStandsAsideForKeyboard(t *testing.T) {
	now := uint64(12000)
	pushed := withPadRepeatSeams(t,
		func() (int, int) { return 0, 1 },
		func() uint64 { return now })

	prevActive := virtualKeyboardActive
	virtualKeyboardActive = true
	t.Cleanup(func() { virtualKeyboardActive = prevActive })

	padTickRepeat()
	now += uint64(navRepeatDelayMs) * 3
	if padTickRepeat() {
		t.Fatal("the pad repeated while the on-screen keyboard owns it")
	}
	if len(*pushed) != 0 {
		t.Fatalf("queued %d presses with the keyboard up", len(*pushed))
	}
	if padNavRepeat != (navRepeatTracker{}) {
		t.Error("the tracker was not reset for the keyboard")
	}
}

func TestPadTickRepeatHonoursSlowRepeat(t *testing.T) {
	now := uint64(15000)
	pushed := withPadRepeatSeams(t,
		func() (int, int) { return 0, -1 },
		func() uint64 { return now })

	prevSlow := a11y.slowRepeat
	a11y.slowRepeat = true
	t.Cleanup(func() { a11y.slowRepeat = prevSlow })

	padTickRepeat()
	now += uint64(navRepeatDelayMs) + 1
	if padTickRepeat() {
		t.Fatal("slow repeat repeated at the normal delay")
	}
	now += uint64(navRepeatDelayMs)
	if !padTickRepeat() {
		t.Fatal("slow repeat never repeated at the doubled delay")
	}
	if len(*pushed) != 1 || (*pushed)[0] != sdl.CONTROLLER_BUTTON_DPAD_UP {
		t.Fatalf("queued %v, want one D-pad up press", *pushed)
	}
}
