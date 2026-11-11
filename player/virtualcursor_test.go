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
	oldTrig := padTriggerHeld
	oldChord, oldSound := vcursorChord, vcursorModeSound
	t.Cleanup(func() {
		syntheticEventPush, vcursorClock, vcursor, stickAxes = oldPush, oldClock, oldState, oldAxes
		windowScaleFactor, windowOffsetX, windowOffsetY = oldScale, oldOX, oldOY
		padTriggerHeld = oldTrig
		vcursorChord, vcursorModeSound = oldChord, oldSound
	})
	vcursorChord, vcursorModeSound = vcursorChordState{}, func() {}
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
	padTriggerHeld = [2]bool{}
	clearToastAfterTest(t)
	return &clock, &events
}

// vcursorRight moves the right stick to x, y and sends its event.
func vcursorRight(config *Config, x, y float64) bool {
	stickAxes[2], stickAxes[3] = x, y
	return vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_RIGHTX, Value: int16(x * 32767)}, config)
}

// TestVcursorRightStick: the right stick brings the pointer up and moves it;
// the left stick stays the D-pad, and its steps put the pointer away.
func TestVcursorRightStick(t *testing.T) {
	now, queued := vcursorTestState(t, time.Unix(1000, 0))
	config := &Config{}

	// A small tilt (inside the dead zone, or just past it) does not bring it up.
	if !vcursorRight(config, 0.2, 0) || vcursor.active {
		t.Fatal("a light right-stick tilt brought the pointer up")
	}
	// A deliberate move does, at the centre the first time.
	if !vcursorRight(config, 0.9, 0) || !vcursor.active {
		t.Fatal("moving the right stick did not bring the pointer up")
	}
	if vcursor.x != float64(screenWidth)/2 {
		t.Errorf("first showing at x=%v, want the centre", vcursor.x)
	}
	startX := vcursor.x
	*now = now.Add(20 * time.Millisecond)
	vcursorTick(config)
	if vcursor.x <= startX {
		t.Errorf("the right stick did not move the pointer: %v → %v", startX, vcursor.x)
	}
	// Inside the dead zone it stays put.
	vcursorRight(config, stickCalDefaultDZ*0.9, 0)
	stillX := vcursor.x
	*now = now.Add(20 * time.Millisecond)
	vcursorTick(config)
	if vcursor.x != stillX {
		t.Errorf("a tilt inside the dead zone moved the pointer: %v → %v", stillX, vcursor.x)
	}

	// The left stick is not the pointer's: it goes on to stick navigation.
	stickAxes[0] = 0.9
	if vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_LEFTX, Value: 29490}, config) {
		t.Error("the left stick was taken by the pointer")
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
	// A D-pad press (or a left-stick step) hides it and still moves the focus.
	if vcursorButton(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_DOWN}, config) {
		t.Error("D-pad press was swallowed")
	}
	if vcursor.active {
		t.Error("D-pad press did not hide the pointer")
	}
	// The right stick brings it back where it was.
	at := vcursor.x
	vcursorRight(config, 0, 0)
	vcursorRight(config, 0, 0.9)
	if !vcursor.active || vcursor.x != at {
		t.Errorf("pointer back: active %v at x=%v, want %v", vcursor.active, vcursor.x, at)
	}
}

// TestVcursorTriggerScroll: while the pointer is up, L2 / R2 scroll under it,
// once at once and repeating while held; with it hidden they are the
// shoulders' again.
func TestVcursorTriggerScroll(t *testing.T) {
	now, queued := vcursorTestState(t, time.Unix(2000, 0))
	config := &Config{}
	trigger := func(axis uint8, v int16) bool {
		return vcursorAxis(&sdl.ControllerAxisEvent{Axis: axis, Value: v}, config)
	}

	if trigger(sdl.CONTROLLER_AXIS_TRIGGERRIGHT, 32000) {
		t.Fatal("R2 was taken with the pointer hidden")
	}
	trigger(sdl.CONTROLLER_AXIS_TRIGGERRIGHT, 0) // let go (the shoulders' latch)

	vcursorRight(config, 0.9, 0)
	vcursorRight(config, 0, 0)
	*queued = nil
	if !trigger(sdl.CONTROLLER_AXIS_TRIGGERRIGHT, 32000) {
		t.Fatal("R2 not taken with the pointer up")
	}
	wheels := func() (n int, last int32) {
		for _, e := range *queued {
			if w, ok := e.(*queuedMouseWheelEvent); ok {
				n++
				last = w.Y
			}
		}
		return n, last
	}
	if n, y := wheels(); n != 1 || y != -1 {
		t.Fatalf("R2 press: %d wheel events, last Y %d; want one notch down", n, y)
	}
	*now = now.Add(200 * time.Millisecond)
	vcursorTick(config)
	if n, _ := wheels(); n != 1 {
		t.Errorf("R2 repeated before the delay: %d notches", n)
	}
	*now = now.Add(200 * time.Millisecond)
	vcursorTick(config)
	if n, _ := wheels(); n != 2 {
		t.Errorf("held R2 did not repeat: %d notches", n)
	}
	trigger(sdl.CONTROLLER_AXIS_TRIGGERRIGHT, 0)
	*now = now.Add(time.Second)
	vcursorTick(config)
	if n, _ := wheels(); n != 2 || vcursor.trig[1] || padTriggerHeld[1] {
		t.Errorf("released R2 kept scrolling: %d notches, held %v latch %v", n, vcursor.trig[1], padTriggerHeld[1])
	}
	// L2 scrolls up.
	*queued = nil
	trigger(sdl.CONTROLLER_AXIS_TRIGGERLEFT, 32000)
	if n, y := wheels(); n != 1 || y != 1 {
		t.Errorf("L2 press: %d wheel events, last Y %d; want one notch up", n, y)
	}
}

func TestVcursorDisabledAndIdle(t *testing.T) {
	now, _ := vcursorTestState(t, time.Unix(3000, 0))

	off := &Config{}
	off.Variables.Custom = map[string]interface{}{vcursorKey: false}
	if vcursorRight(off, 0.9, 0) || vcursor.active {
		t.Error("the right stick brought the pointer up with the setting off")
	}

	on := &Config{}
	vcursorRight(on, 0.9, 0)
	if !vcursor.active {
		t.Fatal("the right stick did not bring the pointer up")
	}
	vcursorRight(on, 0, 0)
	*now = now.Add(vcursorIdleHide + time.Second)
	vcursorTick(on)
	if vcursor.active {
		t.Error("an untouched pointer did not hide itself")
	}
}

// TestVcursorDriftStaysHidden: a stick calibrated to rest off centre does
// not bring the pointer up, and a stick creeping just past its dead zone does
// not keep it up.
func TestVcursorDriftStaysHidden(t *testing.T) {
	now, _ := vcursorTestState(t, time.Unix(4000, 0))
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{
		stickCalKeys[stickRight]: "0.100,0.000,0.300,1.000",
	}
	if vcursorRight(config, 0, 0.3); vcursor.active {
		t.Fatal("a drifting stick at rest brought the pointer up")
	}
	// Brought up on purpose, then left creeping at a tilt of about 0.08.
	vcursorRight(config, 0.9, 0.3)
	if !vcursor.active {
		t.Fatal("a deliberate move did not bring the pointer up")
	}
	vcursorRight(config, 0.172, 0.3)
	for i := 0; i < 40; i++ {
		*now = now.Add(time.Second)
		vcursorTick(config)
	}
	if vcursor.active {
		t.Error("a creeping stick kept the pointer up for ever")
	}
}

// TestVcursorChatKeepsRightStick: Chat scrolls its history with the right
// stick, so the pointer leaves it alone there.
func TestVcursorChatKeepsRightStick(t *testing.T) {
	vcursorTestState(t, time.Unix(5000, 0))
	oldScene := currentSceneIndex
	defer func() { currentSceneIndex = oldScene }()
	config := &Config{Scenes: []SceneConfig{{Name: chatSceneName}}}
	currentSceneIndex = 0
	if vcursorRight(config, 0.9, 0) || vcursor.active {
		t.Error("the pointer took the right stick on the Chat screen")
	}
}

// TestVcursorStickCalScreen: on the stick calibration screen neither stick
// moves anything — not the pointer, not the focus.
func TestVcursorStickCalScreen(t *testing.T) {
	vcursorTestState(t, time.Unix(6000, 0))
	oldScene := currentSceneIndex
	defer func() { currentSceneIndex = oldScene }()
	config := &Config{Scenes: []SceneConfig{{Name: stickCalScene}}}
	currentSceneIndex = 0
	if !vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_LEFTX, Value: 30000}, config) {
		t.Error("the left stick navigated on the calibration screen")
	}
	if !vcursorRight(config, 0.9, 0) || vcursor.active {
		t.Error("the right stick brought the pointer up on the calibration screen")
	}
}

// TestVcursorTriggerHeldBeforePointer: a trigger already held for its
// shoulder action when the pointer comes up does not start scrolling.
func TestVcursorTriggerHeldBeforePointer(t *testing.T) {
	now, queued := vcursorTestState(t, time.Unix(7000, 0))
	config := &Config{}
	// R2 pressed with the pointer hidden: the shoulders' latch holds it.
	triggerPressedEdge(1, 32000)
	vcursorRight(config, 0.9, 0)
	*queued = nil
	vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_TRIGGERRIGHT, Value: 31000}, config)
	*now = now.Add(time.Second)
	vcursorTick(config)
	for _, e := range *queued {
		if _, ok := e.(*queuedMouseWheelEvent); ok {
			t.Fatal("a trigger held before the pointer came up scrolled")
		}
	}
	// Its release and a fresh press do scroll.
	vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_TRIGGERRIGHT, Value: 0}, config)
	vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_TRIGGERRIGHT, Value: 32000}, config)
	n := 0
	for _, e := range *queued {
		if _, ok := e.(*queuedMouseWheelEvent); ok {
			n++
		}
	}
	if n != 1 {
		t.Errorf("a fresh R2 press sent %d notches, want 1", n)
	}
}
