package main

// virtualcursor.go — a mouse pointer on the right analog stick.
//
// The TrimUI Smart Pro has two sticks and no touch screen, so every surface
// that is easiest with a pointer (links on a web page, the store's shelves,
// a seek bar, small chips) was D-pad only there. The left stick is the D-pad's
// twin (controller.go); the right stick is the pointer:
//
//	right stick  moving it brings the pointer up and moves it (speed
//	             follows the tilt)
//	A            clicks; hold A and move to drag
//	L2 / R2      scroll up / down under the pointer (held: repeats)
//
// B and the other buttons keep their usual jobs. The D-pad — and the left
// stick, which steps like it — puts the pointer away and moves the focus as
// always, so A goes back to pressing the focused item; so does half a minute
// without touching the right stick. Settings → Controls → Virtual cursor turns
// it off. Screens that use the right stick themselves keep it: JukaLand and
// browser games aim with it, and the stick calibration and the controller test
// measure it. Chat shares it: up / down scrolls the history, a sideways tilt
// brings the pointer up (so its paperclip, links and chips can be clicked on a
// TrimUI), and once the pointer is put away up / down scrolls again.
//
// The pointer is a real mouse to the rest of the app: it queues SDL mouse
// motion, button and wheel events at its position (in window pixels, the way
// SDL reports a mouse), so every surface that answers the mouse — hover focus,
// clicks, drags, the wheel — answers the pointer with no change of its own.
// It is drawn after the screenshot / companion captures, so it never shows up
// in a screenshot.
//
// Every stick value is read through the stick calibration (stickcal.go): the
// dead zone, resting centre and reach Settings → Controls measures, so a
// drifting stick neither brings the pointer up nor creeps it, and a short one
// still reaches full speed.
//
// Mouse mode (vcursor_mode.go) is the same pointer turned on on purpose: hold
// Select + L1 and it stays up, the D-pad and the left stick move it too, and
// only the screens that own the pad put it away. That is also how the TrimUI
// Brick, which has no sticks, gets a pointer.

import (
	"math"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// vcursorKey is the Custom setting (default on).
	vcursorKey = "virtualCursor"
	// vcursorShowTilt is the calibrated right-stick tilt (0 inside the dead
	// zone, 1 at the reach) that brings the pointer up: a deliberate move,
	// never a stick resting a little off centre.
	vcursorShowTilt = 0.25
	// vcursorActiveTilt is the tilt that counts as using the pointer (it keeps
	// the idle hide away).
	vcursorActiveTilt = 0.1
	// vcursorCreep is the smallest tilt that moves the pointer.
	vcursorCreep = 0.06
	// vcursorMaxSpeed is the pointer's speed at full tilt, logical px/s: the
	// 1280-wide canvas is crossed in about 1.4 s; a light touch is slow enough
	// to land on a link.
	vcursorMaxSpeed = 900.0
	// vcursorScrollDelay / vcursorScrollRepeat pace a held L2 / R2: one notch
	// at once, then a notch every Repeat after Delay.
	vcursorScrollDelay  = 350 * time.Millisecond
	vcursorScrollRepeat = 90 * time.Millisecond
	// vcursorIdleHide puts an untouched pointer away.
	vcursorIdleHide = 30 * time.Second
	// vcursorScale is the size of one bitmap pixel in logical pixels.
	vcursorScale = 2
)

type vcursorState struct {
	active  bool
	placed  bool // x/y hold a position from an earlier showing
	told    bool // the how-to toast was shown this run
	x, y    float64
	pressed bool    // A is held: the left mouse button is down
	trig    [2]bool // L2 / R2 held while the pointer is up: scrolling
	sentX   int32   // last position queued, window pixels
	sentY   int32
	last    time.Time // previous tick
	used    time.Time // last deliberate move, click or scroll
	next    time.Time // when a held trigger scrolls again
	// Mouse mode (vcursor_mode.go).
	mode      bool      // on: the pointer stays up; the D-pad and left stick move it
	modeTold  bool      // its how-to toast was shown this run
	dpad      [4]bool   // up, down, left, right held in mouse mode
	dpadSince time.Time // when the D-pad started moving the pointer
}

// vcursorStaleGap is a tick gap that means the main loop was away (a modal
// input loop, a launched game): the A release and the stick returning to rest
// may have happened there, out of the pointer's sight.
const vcursorStaleGap = 2 * time.Second

// vcursor is main-thread state (event loop and render loop only).
var vcursor vcursorState

// vcursorClock is the time source; tests replace it.
var vcursorClock = time.Now

// vcursorEnabled reports the Settings toggle (on unless turned off).
func vcursorEnabled(config *Config) bool {
	if config == nil {
		return true
	}
	return customBool(config.Variables.Custom, vcursorKey, true)
}

// vcursorSetEnabled stores the toggle; turning it off puts the pointer away.
func vcursorSetEnabled(config *Config, on bool) {
	if !on {
		vcursorHide()
	}
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[vcursorKey] = on
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// vcursorBlocked reports a moment the pointer must not be up: a screen that
// owns the pad outright (vcursorScreenBlocks), or — outside mouse mode — the
// Virtual cursor switch being off or Chat scrolling with the right stick. In
// mouse mode the pointer is the user's own choice, so only the screens that
// own the pad put it away; Chat keeps its right stick (vcursorAxis) while the
// D-pad and the left stick move the pointer.
func vcursorBlocked(config *Config) bool {
	if vcursorScreenBlocks(config) {
		return true
	}
	if vcursorModeLive(config) {
		return false
	}
	return !vcursorEnabled(config) || vcursorChatOwnsStick(config)
}

// vcursorScreenBlocks reports a screen that owns the pad: JukaLand / a browser
// game aim with the sticks, the stick calibrations (JukaHub's and PPSSPP's)
// and the controller test measure them, a ringing alarm is modal. Quick
// Settings and the wallpaper picker are pad-modal, but the mouse path has no
// guard for them: a click would press the screen behind. The screensaver and
// the dock's darkened panel show nothing to point at (screensaverAsleep), and
// mouse mode's pointer, which never times out, was drawn over the starfield.
func vcursorScreenBlocks(config *Config) bool {
	return jlandKeysCaptured(config) || calibrationPanel.open || stickCalOwnsSticks(config) || ctrldiag.open ||
		alarmFiringActive() || qs.open || wpOpen || screensaverAsleep()
}

// vcursorChatOwnsStick reports the Chat screen scrolling its history with the
// right stick: not while its keyboard or an overlay is up (chatSceneStick),
// where the pointer can press the keys, and — outside mouse mode — not while
// the pointer is up (a sideways tilt brought it up: vcursorChatSummon), when
// the stick moves the pointer until it is put away.
func vcursorChatOwnsStick(config *Config) bool {
	return chatSceneActive(config) && !virtualKeyboardActive && !chatOverlayOpen() &&
		(!vcursor.active || vcursorModeLive(config))
}

// vcursorChatSummon brings the pointer up on the Chat screen from a sideways
// right-stick tilt; an up / down tilt stays Chat's scroll. It reports true
// when the event was taken.
func vcursorChatSummon(config *Config) bool {
	if vcursor.active || !vcursorChatOwnsStick(config) || vcursorModeLive(config) ||
		!vcursorEnabled(config) || vcursorScreenBlocks(config) {
		return false
	}
	x, y := stickShaped(config, stickRight)
	if math.Abs(x) < vcursorShowTilt || math.Abs(x) <= math.Abs(y) {
		return false
	}
	chatStickSpeed = 0 // a scroll that was running stops under the pointer
	vcursorShow()
	return true
}

// vcursorMouseState is sdl.GetMouseState with the virtual cursor folded in:
// queued events do not move SDL's own pointer, so a surface that polls the
// mouse (the on-screen keyboard's hover and hold-to-repeat) asks here.
func vcursorMouseState() (x, y int32, state uint32) {
	if vcursor.active {
		x, y = vcursorWindowPos()
		if vcursor.pressed {
			state = sdl.ButtonLMask()
		}
		return x, y, state
	}
	return sdl.GetMouseState()
}

// vcursorResetAxes answers a controller coming or going: the sticks are read
// again, a click the pad that went away was holding is released, held
// triggers, a held D-pad and a half-held mouse-mode chord are let go, and a
// stick measurement is cancelled.
func vcursorResetAxes() {
	stickResetAxes()
	vcursor.trig = [2]bool{}
	vcursorDpadClear()
	vcursorChord = vcursorChordState{}
	if vcursor.pressed {
		vcursor.pressed = false
		vcursorPushButton(false)
	}
	stickCalDeviceChanged()
}

// vcursorRightTilt is how far the right stick is tilted, calibrated (0
// inside its dead zone, 1 at its reach).
func vcursorRightTilt(config *Config) float64 {
	x, y := stickShaped(config, stickRight)
	return math.Hypot(x, y)
}

// vcursorAxis decides what a stick or trigger event does. It reports true
// when the event belongs to the pointer: the right stick (which brings it up),
// the left stick in mouse mode, and L2 / R2 while it is up. false sends the
// event on: the left stick to stick navigation, the triggers to their
// shoulder actions.
func vcursorAxis(e *sdl.ControllerAxisEvent, config *Config) bool {
	switch e.Axis {
	case sdl.CONTROLLER_AXIS_TRIGGERLEFT, sdl.CONTROLLER_AXIS_TRIGGERRIGHT:
		return vcursorTrigger(e, config)
	case sdl.CONTROLLER_AXIS_LEFTX, sdl.CONTROLLER_AXIS_LEFTY:
		// The left stick is stick navigation's, except on the stick
		// calibration screen, which measures it, and in mouse mode, where it
		// moves the pointer (vcursorTick reads it).
		if stickCalOwnsSticks(config) {
			return true
		}
		return vcursorModeLive(config) && !vcursorBlocked(config)
	case sdl.CONTROLLER_AXIS_RIGHTX, sdl.CONTROLLER_AXIS_RIGHTY:
	default:
		return false
	}
	// The stick calibration screen measures the sticks: there they move
	// nothing, not the pointer either.
	if stickCalOwnsSticks(config) {
		vcursorHide()
		return true
	}
	// Chat: a sideways tilt brings the pointer up; up / down scrolls on.
	if vcursorChatSummon(config) {
		return true
	}
	if vcursorBlocked(config) {
		vcursorHide()
		return false
	}
	// In mouse mode Chat still scrolls its history with the right stick; the
	// pointer stays where the D-pad and the left stick put it.
	if vcursorChatOwnsStick(config) {
		return false
	}
	now := vcursorClock()
	tilt := vcursorRightTilt(config)
	if tilt >= vcursorActiveTilt {
		vcursor.used = now
	}
	if !vcursor.active && tilt >= vcursorShowTilt {
		vcursorShow()
	}
	return true
}

// vcursorTrigger turns L2 / R2 into the wheel while the pointer is up: a
// notch at once, then repeating while held (vcursorTick). The shoulders' own
// latch still follows the trigger, so it is in step when the pointer goes.
func vcursorTrigger(e *sdl.ControllerAxisEvent, config *Config) bool {
	if !vcursor.active || vcursorBlocked(config) {
		return false
	}
	side := 0
	if e.Axis == sdl.CONTROLLER_AXIS_TRIGGERRIGHT {
		side = 1
	}
	// Over the on-screen keyboard R2 is voice typing (vkvoice_button.go); over
	// the page above it the triggers still scroll.
	if side == 1 && vkVoiceOwnsR2At(vcursor.x, vcursor.y) {
		vcursor.trig[1] = false
		return false
	}
	// Only a press the pointer saw start scrolls: one that began before the
	// pointer came up stays the shoulder action it already was.
	pressed := triggerPressedEdge(side, e.Value)
	if !padTriggerHeld[side] {
		vcursor.trig[side] = false
	}
	if pressed {
		now := vcursorClock()
		vcursor.trig[side] = true
		vcursorScrollNotch(side)
		vcursor.used, vcursor.next = now, now.Add(vcursorScrollDelay)
	}
	return true
}

// vcursorScrollNotch scrolls one notch under the pointer: up for L2, down for
// R2 (wheel Y is positive away from the user).
func vcursorScrollNotch(side int) {
	if side == 0 {
		vcursorPushWheel(0, 1)
		return
	}
	vcursorPushWheel(0, -1)
}

// vcursorShow puts the pointer up where it was last time (the centre at
// first). The first time in a run it says how it works.
func vcursorShow() {
	now := vcursorClock()
	if !vcursor.placed {
		vcursor.x, vcursor.y = float64(screenWidth)/2, float64(screenHeight)/2
		vcursor.placed = true
	}
	vcursor.active = true
	vcursor.trig = [2]bool{}
	vcursor.last, vcursor.used = now, now
	vcursorPushMotion(true)
	vcursorWake()
	if !vcursor.told {
		vcursor.told = true
		setToast("Pointer: A clicks · L2/R2 scroll · D-pad hides it")
	}
}

// vcursorHide puts the pointer away, releasing a held click first so nothing
// is left mid-drag.
func vcursorHide() {
	if !vcursor.active {
		return
	}
	if vcursor.pressed {
		vcursor.pressed = false
		vcursorPushButton(false)
	}
	vcursor.active = false
	vcursor.trig = [2]bool{}
	vcursorDpadClear()
}

// vcursorButton turns A into the left mouse button while the pointer is up.
// A D-pad press (the left stick's steps are D-pad presses too) puts the
// pointer away and still moves the focus — except in mouse mode, where the
// D-pad moves the pointer. It reports true when the press is consumed.
func vcursorButton(e *sdl.ControllerButtonEvent, config *Config) bool {
	if !vcursor.active {
		return false
	}
	if vcursorBlocked(config) {
		vcursorHide()
		return false
	}
	down := e.Type == sdl.CONTROLLERBUTTONDOWN
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A:
		if down {
			if !vcursor.pressed {
				vcursor.pressed = true
				vcursor.used = vcursorClock()
				vcursorPushButton(true)
			}
			return true
		}
		// Only the release of a press the pointer took; an A that was already
		// down when the pointer came up is released to whoever got its press.
		if vcursor.pressed {
			vcursor.pressed = false
			vcursor.used = vcursorClock()
			vcursorPushButton(false)
			return true
		}
	case sdl.CONTROLLER_BUTTON_DPAD_UP, sdl.CONTROLLER_BUTTON_DPAD_DOWN,
		sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if vcursorModeLive(config) {
			// Mouse mode: the D-pad moves the pointer (vcursorTick). A queued
			// hold-to-repeat press (navrepeat.go) stands for a direction that
			// is already held, so it only has to stay away from the focus.
			if e.Which != padRepeatWhich {
				vcursorDpadEvent(e.Button, down)
			}
			return true
		}
		if down && !vcursor.pressed {
			vcursorHide()
		}
	}
	return false
}

// vcursorCurve maps a calibrated stick value (the dead zone already taken
// out) to a -1..1 speed on a gentle curve, so small tilts give fine control.
func vcursorCurve(v float64) float64 {
	t := math.Pow(math.Min(math.Abs(v), 1), 1.7)
	if v < 0 {
		return -t
	}
	return t
}

// vcursorTick runs once per frame: it times the mouse-mode chord, moves the
// pointer from the held sticks and, in mouse mode, the held D-pad (axis events
// only arrive when a value changes, so a steady tilt has to be integrated
// here, not in the event handler), and repeats a held trigger's scroll.
func vcursorTick(config *Config) {
	now := vcursorClock()
	// Select + L1 held long enough: mouse mode on or off (vcursor_mode.go).
	// A chord button whose release was lost is dropped first, and a switch
	// turned off behind mouse mode's back (a reloaded config) ends it.
	vcursorChord.forget(vcursorChordPadHeld)
	if vcursorChord.tick(now) {
		vcursorModeToggle(config)
	}
	if vcursor.mode && !vcursorModeEnabled(config) {
		vcursorModeApply(config, false, false)
	}
	// Mouse mode brings the pointer back once whatever put it away (a panel,
	// the on-screen keyboard) is gone.
	if !vcursor.active && vcursorModeLive(config) && !vcursorBlocked(config) {
		vcursorShow()
	}
	if !vcursor.active {
		return
	}
	if vcursorBlocked(config) {
		vcursorHide()
		return
	}
	gap := now.Sub(vcursor.last)
	dt := gap.Seconds()
	vcursor.last = now
	if gap > vcursorStaleGap {
		// Whatever happened while the loop was away is unknown: drop the
		// held click (its release went elsewhere), the held triggers and
		// D-pad, and re-read the sticks.
		vcursor.pressed = false
		vcursor.trig = [2]bool{}
		vcursorDpadClear()
		stickResetAxes()
	}
	if dt < 0 {
		dt = 0
	}
	// A stalled frame must not fling the pointer across the screen.
	if dt > 0.05 {
		dt = 0.05
	}
	busy := false
	live := vcursorModeLive(config)

	// Right stick: the direction it points, at a speed on the curve (in mouse
	// mode Chat keeps it for its history). Mouse mode adds the left stick and
	// the held D-pad, all together never faster than full tilt.
	var vx, vy float64
	if !live || !vcursorChatOwnsStick(config) {
		rx, ry := stickShaped(config, stickRight)
		vx, vy = vcursorStickVelocity(rx, ry)
	}
	if live {
		mx, my := vcursorModeVelocity(config, now)
		vx, vy = vcursorClampSpeed(vx+mx, vy+my)
	}
	if vx != 0 || vy != 0 {
		vcursor.x = clampFloat(vcursor.x+vx*dt, 0, float64(screenWidth-1))
		vcursor.y = clampFloat(vcursor.y+vy*dt, 0, float64(screenHeight-1))
		busy = vcursorPushMotion(false)
	}

	// A held trigger keeps scrolling.
	if (vcursor.trig[0] || vcursor.trig[1]) && !now.Before(vcursor.next) {
		for side, held := range vcursor.trig {
			if held {
				vcursorScrollNotch(side)
			}
		}
		vcursor.next = now.Add(vcursorScrollRepeat)
		vcursor.used = now
		busy = true
	}

	if busy {
		vcursorWake()
	}
	// Idle is measured from deliberate use, not from movement: a stick
	// drifting just past its dead zone creeps the pointer but does not keep it
	// up for ever. Mouse mode keeps it up until it is turned off.
	if !live && !vcursor.pressed && now.Sub(vcursor.used) > vcursorIdleHide {
		vcursorHide()
	}
}

// clampFloat keeps v within lo..hi.
func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// vcursorWake keeps the frame rate up and the screensaver away while the
// pointer is being used: the queued mouse events do that too, but only once
// they are polled, a frame later.
func vcursorWake() {
	if frameRateCtrl != nil {
		frameRateCtrl.NotifyInput()
	}
	screensaverNotifyInput()
}

// vcursorWindowPos is the pointer in window pixels, the space SDL mouse events
// use (main.go converts them back with physicalToLogical).
func vcursorWindowPos() (int32, int32) {
	px := int32(math.Round(vcursor.x*windowScaleFactor)) + windowOffsetX
	py := int32(math.Round(vcursor.y*windowScaleFactor)) + windowOffsetY
	return px, py
}

// ── Queued mouse events ────────────────────────────────────────────────────
//
// Padded like every synthetic event (sdlevents.go): SDL_PushEvent copies a
// whole SDL_Event from the pointer it is handed.

type queuedMouseWheelEvent struct {
	sdl.MouseWheelEvent
	_ [syntheticEventPad]byte
}

// vcursorPushMotion queues a move to the pointer and reports whether it did:
// nothing is sent while the window-pixel position stays the same, unless
// force (showing the pointer).
func vcursorPushMotion(force bool) bool {
	px, py := vcursorWindowPos()
	if !force && px == vcursor.sentX && py == vcursor.sentY {
		return false
	}
	var state uint32
	if vcursor.pressed {
		state = sdl.ButtonLMask()
	}
	ev := &queuedMouseMotionEvent{MouseMotionEvent: sdl.MouseMotionEvent{
		Type:      sdl.MOUSEMOTION,
		Timestamp: sdl.GetTicks(),
		State:     state,
		X:         px,
		Y:         py,
		XRel:      px - vcursor.sentX,
		YRel:      py - vcursor.sentY,
	}}
	vcursor.sentX, vcursor.sentY = px, py
	_, _ = syntheticEventPush(ev)
	return true
}

// vcursorPushButton queues a left-button press or release at the pointer.
func vcursorPushButton(down bool) {
	px, py := vcursorWindowPos()
	ev := &queuedMouseButtonEvent{MouseButtonEvent: sdl.MouseButtonEvent{
		Type:      sdl.MOUSEBUTTONUP,
		Timestamp: sdl.GetTicks(),
		Button:    sdl.BUTTON_LEFT,
		State:     sdl.RELEASED,
		Clicks:    1,
		X:         px,
		Y:         py,
	}}
	if down {
		ev.Type = sdl.MOUSEBUTTONDOWN
		ev.State = sdl.PRESSED
	}
	_, _ = syntheticEventPush(ev)
}

// vcursorPushWheel queues wheel notches (x right, y up) under the pointer.
func vcursorPushWheel(x, y int32) {
	_, _ = syntheticEventPush(&queuedMouseWheelEvent{MouseWheelEvent: sdl.MouseWheelEvent{
		Type:      sdl.MOUSEWHEEL,
		Timestamp: sdl.GetTicks(),
		X:         x,
		Y:         y,
	}})
}

// ── Drawing ────────────────────────────────────────────────────────────────

// vcursorBitmap is the arrow: B outline, W fill, '.' clear. Hot spot = top-left.
var vcursorBitmap = [...]string{
	"B...........",
	"BB..........",
	"BWB.........",
	"BWWB........",
	"BWWWB.......",
	"BWWWWB......",
	"BWWWWWB.....",
	"BWWWWWWB....",
	"BWWWWWWWB...",
	"BWWWWWWWWB..",
	"BWWWWWWWWWB.",
	"BWWWWWWBBBBB",
	"BWWWBWWB....",
	"BWWB.BWWB...",
	"BWB..BWWB...",
	"BB....BWWB..",
	"B.....BWWB..",
	".......BB...",
}

// vcursorRun is one horizontal run of one colour in the bitmap.
type vcursorRun struct {
	row, col, n int32
	fill        bool
}

// vcursorRuns is the bitmap as runs, built once.
var vcursorRuns = vcursorBuildRuns(vcursorBitmap[:])

func vcursorBuildRuns(rows []string) []vcursorRun {
	var out []vcursorRun
	for r, line := range rows {
		for c := 0; c < len(line); {
			ch := line[c]
			if ch != 'B' && ch != 'W' {
				c++
				continue
			}
			start := c
			for c < len(line) && line[c] == ch {
				c++
			}
			out = append(out, vcursorRun{row: int32(r), col: int32(start), n: int32(c - start), fill: ch == 'W'})
		}
	}
	return out
}

// vcursorRects are reused between frames.
var vcursorRects struct{ edge, fill []sdl.Rect }

// vcursorRender draws the pointer on top of the finished frame. The fill turns
// blue while A is held so a press is visible.
func vcursorRender(renderer *sdl.Renderer) {
	if !vcursor.active || renderer == nil {
		return
	}
	x0, y0 := int32(math.Round(vcursor.x)), int32(math.Round(vcursor.y))
	edge, fill := vcursorRects.edge[:0], vcursorRects.fill[:0]
	for _, run := range vcursorRuns {
		rc := sdl.Rect{X: x0 + run.col*vcursorScale, Y: y0 + run.row*vcursorScale, W: run.n * vcursorScale, H: vcursorScale}
		if run.fill {
			fill = append(fill, rc)
		} else {
			edge = append(edge, rc)
		}
	}
	vcursorRects.edge, vcursorRects.fill = edge, fill
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE)
	_ = renderer.SetDrawColor(20, 20, 24, 255)
	_ = renderer.FillRects(edge)
	if vcursor.pressed {
		_ = renderer.SetDrawColor(120, 190, 255, 255)
	} else {
		_ = renderer.SetDrawColor(250, 250, 252, 255)
	}
	_ = renderer.FillRects(fill)
}
