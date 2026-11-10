package main

import (
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func TestVcursorCurve(t *testing.T) {
	if vcursorCurve(0) != 0 {
		t.Error("a centred stick moved the pointer")
	}
	if got := vcursorCurve(1); got != 1 {
		t.Errorf("full tilt = %v, want 1", got)
	}
	if got := vcursorCurve(-1); got != -1 {
		t.Errorf("full tilt left = %v, want -1", got)
	}
	small, big := vcursorCurve(0.4), vcursorCurve(0.8)
	if !(small > 0 && small < big && big < 1) {
		t.Errorf("curve not monotonic: 0.4→%v 0.8→%v", small, big)
	}
	if small > 0.4 {
		t.Errorf("small tilt is not fine control: %v", small)
	}
}

func TestVcursorRunsCoverBitmap(t *testing.T) {
	want := 0
	for _, row := range vcursorBitmap {
		for _, ch := range row {
			if ch == 'B' || ch == 'W' {
				want++
			}
		}
	}
	got := 0
	for _, r := range vcursorRuns {
		if r.n <= 0 {
			t.Fatalf("empty run %+v", r)
		}
		got += int(r.n)
	}
	if got != want {
		t.Errorf("runs cover %d pixels, bitmap has %d", got, want)
	}
}

// vcursorTestState saves and restores everything the pointer touches.
func vcursorTestState(t *testing.T, start time.Time) (now *time.Time, queued *[]sdl.Event) {
	t.Helper()
	oldPush, oldClock, oldState, oldAxes := syntheticEventPush, vcursorClock, vcursor, stickAxes
	oldScale, oldOX, oldOY := windowScaleFactor, windowOffsetX, windowOffsetY
	t.Cleanup(func() {
		syntheticEventPush, vcursorClock, vcursor, stickAxes = oldPush, oldClock, oldState, oldAxes
		windowScaleFactor, windowOffsetX, windowOffsetY = oldScale, oldOX, oldOY
	})
	clock := start
	var events []sdl.Event
	syntheticEventPush = func(e sdl.Event) (bool, error) {
		events = append(events, e)
		return false, nil
	}
	vcursorClock = func() time.Time { return clock }
	windowScaleFactor, windowOffsetX, windowOffsetY = 1, 0, 0
	vcursor = vcursorState{}
	stickAxes = [4]float64{}
	clearToastAfterTest(t)
	return &clock, &events
}

// TestVcursorClickAndDrag drives the pointer through show, move, click and
// hide and checks the mouse events it queues.
func TestVcursorClickAndDrag(t *testing.T) {
	now, queued := vcursorTestState(t, time.Unix(1000, 0))
	config := &Config{}

	// Both sticks moved out → shown at the centre.
	stickAxes = [4]float64{0.9, 0, 0.9, 0}
	if !vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_RIGHTX, Value: 30000}, config) || !vcursor.active {
		t.Fatal("gesture did not show the pointer")
	}
	if vcursor.x != float64(screenWidth)/2 {
		t.Errorf("first showing at x=%v, want the centre", vcursor.x)
	}
	// Still latched: the gesture's own tilt neither moves nor scrolls.
	*queued = nil
	*now = now.Add(16 * time.Millisecond)
	vcursorTick(config)
	if len(*queued) != 0 || vcursor.x != float64(screenWidth)/2 {
		t.Errorf("held gesture moved or scrolled: %d events, x=%v", len(*queued), vcursor.x)
	}
	// Sticks back to rest, then the left stick right for a frame.
	stickAxes = [4]float64{}
	vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_LEFTX}, config)
	if vcursor.latched {
		t.Fatal("latch not released at rest")
	}
	stickAxes[0] = 1
	startX := vcursor.x
	*now = now.Add(20 * time.Millisecond)
	vcursorTick(config)
	if vcursor.x <= startX {
		t.Errorf("left stick right did not move the pointer: %v → %v", startX, vcursor.x)
	}
	// A tilt inside the dead zone moves nothing.
	stickAxes[0] = stickCalDefaultDZ * 0.9
	stillX := vcursor.x
	*now = now.Add(20 * time.Millisecond)
	vcursorTick(config)
	if vcursor.x != stillX {
		t.Errorf("a tilt inside the dead zone moved the pointer: %v → %v", stillX, vcursor.x)
	}
	stickAxes[0] = 0

	// A down / up → left button down / up at the pointer.
	*queued = nil
	if !vcursorButton(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A}, config) {
		t.Fatal("A press not taken by the pointer")
	}
	if !vcursorButton(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONUP, Button: sdl.CONTROLLER_BUTTON_A}, config) {
		t.Fatal("A release not taken by the pointer")
	}
	if len(*queued) != 2 {
		t.Fatalf("queued %d events, want 2", len(*queued))
	}
	down, ok1 := (*queued)[0].(*queuedMouseButtonEvent)
	up, ok2 := (*queued)[1].(*queuedMouseButtonEvent)
	if !ok1 || !ok2 || down.Type != sdl.MOUSEBUTTONDOWN || up.Type != sdl.MOUSEBUTTONUP ||
		down.State != sdl.PRESSED || up.State != sdl.RELEASED || down.Button != sdl.BUTTON_LEFT {
		t.Fatalf("click events wrong: %#v / %#v", (*queued)[0], (*queued)[1])
	}
	if down.X != int32(vcursor.x+0.5) && down.X != int32(vcursor.x) {
		t.Errorf("click at x=%d, pointer at %v", down.X, vcursor.x)
	}

	// B is not the pointer's.
	if vcursorButton(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_B}, config) {
		t.Error("B was swallowed by the pointer")
	}
	// The right stick scrolls: one notch at once.
	*queued = nil
	stickAxes[3] = -1 // up
	*now = now.Add(16 * time.Millisecond)
	vcursorTick(config)
	stickAxes[3] = 0
	if len(*queued) != 1 {
		t.Fatalf("scroll queued %d events, want 1", len(*queued))
	}
	if w, ok := (*queued)[0].(*queuedMouseWheelEvent); !ok || w.Y != 1 || w.Type != sdl.MOUSEWHEEL {
		t.Errorf("stick up = %#v, want one wheel notch up", (*queued)[0])
	}

	// A D-pad press right after the gesture is the gesture's own stick step:
	// swallowed, the pointer stays.
	if !vcursorButton(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_RIGHT}, config) || !vcursor.active {
		t.Error("a stick step queued by the gesture hid the pointer")
	}
	// Later, a D-pad press hides it and still goes on to move the focus.
	*now = now.Add(time.Second)
	if vcursorButton(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_DOWN}, config) {
		t.Error("D-pad press was swallowed")
	}
	if vcursor.active {
		t.Error("D-pad press did not hide the pointer")
	}
}

func TestVcursorDisabledAndIdle(t *testing.T) {
	now, _ := vcursorTestState(t, time.Unix(2000, 0))

	off := &Config{}
	off.Variables.Custom = map[string]interface{}{vcursorKey: false}
	stickAxes = [4]float64{0.9, 0, 0.9, 0}
	if vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_LEFTX, Value: 30000}, off) || vcursor.active {
		t.Error("gesture worked with the setting off")
	}

	on := &Config{}
	vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_LEFTX, Value: 30000}, on)
	if !vcursor.active {
		t.Fatal("gesture did not show the pointer")
	}
	// The triggers are never the pointer's.
	if vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_TRIGGERLEFT, Value: 30000}, on) {
		t.Error("a trigger was taken by the pointer")
	}
	stickAxes = [4]float64{}
	vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_LEFTX}, on) // back at rest
	*now = now.Add(vcursorIdleHide + time.Second)
	vcursorTick(on)
	if vcursor.active {
		t.Error("an untouched pointer did not hide itself")
	}
}

// TestVcursorCalibratedDrift: a stick calibrated to rest off centre neither
// fires the gesture nor counts as forming it while it rests there.
func TestVcursorCalibratedDrift(t *testing.T) {
	vcursorTestState(t, time.Unix(3000, 0))
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{
		stickCalKeys[stickLeft]:  "0.100,0.300,0.000,1.000",
		stickCalKeys[stickRight]: "0.100,0.000,0.300,1.000",
	}
	// Both sticks resting at their calibrated, off-centre spots.
	stickAxes = [4]float64{0.3, 0, 0, 0.3}
	if vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_LEFTX, Value: 9830}, config) || vcursor.active {
		t.Error("two drifting sticks at rest fired the gesture")
	}
	l, r := vcursorStickTilts(config)
	if l != 0 || r != 0 || vcursorForming() {
		t.Errorf("resting tilts = %v, %v; want 0, 0", l, r)
	}
}

// TestVcursorGesturePairing: the gesture is both sticks leaving their
// centres together; a stick that has been out for a while does not pair.
func TestVcursorGesturePairing(t *testing.T) {
	now, _ := vcursorTestState(t, time.Unix(4000, 0))
	config := &Config{}
	axis := func(a uint8) bool { return vcursorAxis(&sdl.ControllerAxisEvent{Axis: a}, config) }

	// Left out first, right half a second later: moving the pointer and then
	// scrolling is not the gesture.
	stickAxes = [4]float64{0.9, 0, 0, 0}
	axis(sdl.CONTROLLER_AXIS_LEFTX)
	*now = now.Add(500 * time.Millisecond)
	stickAxes = [4]float64{0.9, 0, 0.9, 0}
	axis(sdl.CONTROLLER_AXIS_RIGHTX)
	if vcursor.active || vcursorForming() {
		t.Fatal("sticks that left half a second apart made the gesture")
	}
	// Both back home, then out together (100 ms apart): the gesture.
	stickAxes = [4]float64{}
	axis(sdl.CONTROLLER_AXIS_LEFTX)
	*now = now.Add(time.Second)
	stickAxes = [4]float64{0.9, 0, 0, 0}
	axis(sdl.CONTROLLER_AXIS_LEFTX)
	*now = now.Add(100 * time.Millisecond)
	stickAxes = [4]float64{0.9, 0, 0.9, 0}
	if !axis(sdl.CONTROLLER_AXIS_RIGHTX) || !vcursor.active {
		t.Fatal("both sticks moved together did not show the pointer")
	}
	// Held out: one toggle only, even once the latch runs out.
	*now = now.Add(2 * vcursorLatchMax)
	axis(sdl.CONTROLLER_AXIS_RIGHTX)
	if !vcursor.active {
		t.Fatal("holding the gesture toggled the pointer again")
	}
	if vcursor.latched {
		t.Error("the latch outlived vcursorLatchMax")
	}
}

// TestVcursorNavHold: the left stick's first step waits for the right stick;
// taken late when it does not come, dropped when it does.
func TestVcursorNavHold(t *testing.T) {
	now, queued := vcursorTestState(t, time.Unix(6000, 0))
	oldBtn, oldTime := lastStickBtn, lastStickTime
	defer func() { lastStickBtn, lastStickTime = oldBtn, oldTime }()
	lastStickBtn, lastStickTime = 0, 0
	config := &Config{}

	stickAxes = [4]float64{0.9, 0, 0, 0}
	if !vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_LEFTX, Value: 29490}, config) {
		t.Fatal("the left stick's first step was not held back")
	}
	*now = now.Add(30 * time.Millisecond)
	vcursorTick(config)
	if len(*queued) != 0 {
		t.Fatalf("the held step was taken early: %d events", len(*queued))
	}
	*now = now.Add(50 * time.Millisecond)
	vcursorTick(config)
	if len(*queued) != 1 {
		t.Fatalf("the held step was not taken: %d events", len(*queued))
	}
	if b, ok := (*queued)[0].(*queuedControllerButtonEvent); !ok || b.Button != sdl.CONTROLLER_BUTTON_DPAD_RIGHT {
		t.Errorf("held step = %#v, want D-pad right", (*queued)[0])
	}

	// Home, then left and right 40 ms apart: the step is dropped.
	stickAxes = [4]float64{}
	vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_LEFTX}, config)
	*queued = nil
	*now = now.Add(time.Second)
	stickAxes = [4]float64{0.4, 0, 0, 0}
	vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_LEFTX, Value: 13107}, config)
	*now = now.Add(40 * time.Millisecond)
	stickAxes = [4]float64{0.4, 0, 0.3, 0}
	if !vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_RIGHTX, Value: 9830}, config) {
		t.Error("two sticks on their way out together were not held as the gesture")
	}
	*now = now.Add(100 * time.Millisecond)
	vcursorTick(config)
	for _, e := range *queued {
		if _, ok := e.(*queuedControllerButtonEvent); ok {
			t.Fatal("the left stick stepped the focus while the gesture formed")
		}
	}
}
