package main

// virtualcursor.go — a mouse pointer driven by the analog sticks.
//
// The TrimUI Smart Pro has two sticks and no touch screen, so every surface
// that is easiest with a pointer (links on a web page, the store's shelves,
// a seek bar, small chips) was D-pad only there. Moving BOTH sticks at the
// same time (tilting them — they do not have to be clicked in) brings up a
// pointer:
//
//	left stick   moves it (speed follows the tilt)
//	right stick  scrolls whatever is under it (a mouse wheel)
//	A            clicks; hold A and move to drag
//
// Everything else on the pad keeps its normal job (B is still back). Both
// sticks again put the pointer away; so does any D-pad press, and half a
// minute without touching it. Settings → Controls → Virtual cursor turns the
// gesture off.
//
// The gesture is both sticks leaving their centres together — within
// vcursorGesturePair of each other — and going well out. Two sticks already
// out (moving the pointer while scrolling), or a stick resting off centre,
// never make it. The left stick's first D-pad step waits vcursorNavHold for
// the right stick to join, so starting the gesture does not also move the
// focus.
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
// drifting stick does not creep the pointer and a short one still reaches
// full speed.

import (
	"math"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// vcursorKey is the Custom setting (default on).
	vcursorKey = "virtualCursor"
	// The gesture, on calibrated stick tilts (0 inside the dead zone, 1 at the
	// reach): a stick is away from its centre past Away and back under Home
	// (apart, so a stick near the edge of its dead zone does not flicker), and
	// both must leave within Pair of each other and reach Out.
	vcursorGestureOut  = 0.5
	vcursorGestureAway = 0.1
	vcursorGestureHome = 0.05
	vcursorGesturePair = 300 * time.Millisecond
	// vcursorLatchMax is how long a fired gesture keeps its sticks at most:
	// held out longer, they move the pointer.
	vcursorLatchMax = time.Second
	// vcursorNavHold is how long the left stick's first D-pad step waits for
	// the right stick to join (and make it the gesture instead).
	vcursorNavHold = 70 * time.Millisecond
	// vcursorCreep is the smallest tilt that moves the pointer.
	vcursorCreep = 0.05
	// vcursorScrollStart is the tilt past which the right stick scrolls.
	vcursorScrollStart = 0.3
	// vcursorMaxSpeed is the pointer's speed at full tilt, logical px/s: the
	// 1280-wide canvas is crossed in about 1.4 s; a light touch is slow enough
	// to land on a link.
	vcursorMaxSpeed = 900.0
	// vcursorScrollRate is the wheel notches per second at full tilt.
	vcursorScrollRate = 14.0
	// vcursorIdleHide puts an untouched pointer away.
	vcursorIdleHide = 30 * time.Second
	// vcursorScale is the size of one bitmap pixel in logical pixels.
	vcursorScale = 2
)

type vcursorState struct {
	active  bool
	placed  bool // x/y hold a position from an earlier showing
	x, y    float64
	latched bool       // the gesture fired; its sticks are still out
	pressed bool       // A is held: the left mouse button is down
	scroll  [2]float64 // fractional wheel notches still owed (X, Y)
	sentX   int32      // last position queued, window pixels
	sentY   int32
	last    time.Time // previous tick
	used    time.Time // last movement, scroll or click
	shown   time.Time // when the gesture last put it up

	// The gesture: whether each stick is away from its centre and when it
	// left, the departure the gesture last fired for and when it fired.
	away     [2]bool
	left     [2]time.Time
	firedFor time.Time
	firedAt  time.Time
	// navOwed is the left stick's first D-pad step, held back for
	// vcursorNavHold; 0 when none.
	navOwed uint8
}

// vcursorShowGrace is how long after the gesture a D-pad press is taken for a
// stick step the gesture itself queued (handleStickDpad), not a real press.
const vcursorShowGrace = 300 * time.Millisecond

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
		vcursorHide(false)
		vcursorForget()
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

// vcursorBlocked reports a screen that owns the sticks itself, or a moment the
// pointer must not be up: JukaLand / a browser game read both sticks, the
// stick calibrations (JukaHub's and PPSSPP's) and the controller test measure
// them, a ringing alarm is modal.
func vcursorBlocked(config *Config) bool {
	// Quick Settings and the wallpaper picker are pad-modal, but the mouse path
	// has no guard for them: a click would press the screen behind.
	return !vcursorEnabled(config) || jlandKeysCaptured(config) || calibrationPanel.open ||
		stickCalOwnsSticks(config) || ctrldiag.open || alarmFiringActive() || qs.open || wpOpen
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

// vcursorForget drops a gesture under way and a held-back step.
func vcursorForget() {
	vcursor.latched = false
	vcursor.navOwed = 0
}

// vcursorResetAxes answers a controller coming or going: the sticks are read
// again, a gesture under way is dropped, a click the pad that went away was
// holding is released, and a stick measurement is cancelled.
func vcursorResetAxes() {
	stickResetAxes()
	vcursorForget()
	if vcursor.pressed {
		vcursor.pressed = false
		vcursorPushButton(false)
	}
	stickCalDeviceChanged()
}

// vcursorStickTilts are how far each stick is tilted, calibrated (0 inside
// its dead zone, 1 at its reach).
func vcursorStickTilts(config *Config) (left, right float64) {
	lx, ly := stickShaped(config, stickLeft)
	rx, ry := stickShaped(config, stickRight)
	return math.Hypot(lx, ly), math.Hypot(rx, ry)
}

// vcursorTrack follows each stick leaving its centre and coming back.
func vcursorTrack(l, r float64, now time.Time) {
	for i, t := range [2]float64{l, r} {
		switch {
		case !vcursor.away[i] && t >= vcursorGestureAway:
			vcursor.away[i], vcursor.left[i] = true, now
		case vcursor.away[i] && t < vcursorGestureHome:
			vcursor.away[i] = false
		}
	}
}

// vcursorPair is the gesture both sticks are making: the later of their two
// departures, and false when they are not both away or did not leave
// together.
func vcursorPair() (time.Time, bool) {
	if !vcursor.away[0] || !vcursor.away[1] {
		return time.Time{}, false
	}
	a, b := vcursor.left[0], vcursor.left[1]
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	if d > vcursorGesturePair {
		return time.Time{}, false
	}
	if a.After(b) {
		return a, true
	}
	return b, true
}

// vcursorForming reports both sticks away together for a gesture that has
// not fired yet: its sticks move nothing meanwhile.
func vcursorForming() bool {
	id, ok := vcursorPair()
	return ok && id.After(vcursor.firedFor)
}

// vcursorAxis decides what a stick event does. It reports true when the event
// belongs to the pointer (the gesture, a held-back first step, or any stick
// while the pointer is up); false sends it on to the normal stick navigation.
func vcursorAxis(e *sdl.ControllerAxisEvent, config *Config) bool {
	if int(e.Axis) >= len(stickAxes) {
		return false // the triggers stay shoulder buttons
	}
	// The stick calibration screen measures the sticks: there they move
	// nothing, not the focus and not the pointer.
	if stickCalOwnsSticks(config) {
		vcursorHide(false)
		vcursorForget()
		return true
	}
	if vcursorBlocked(config) {
		vcursorHide(false)
		vcursorForget()
		return false
	}
	now := vcursorClock()
	l, r := vcursorStickTilts(config)
	vcursorTrack(l, r, now)
	if vcursor.latched && ((!vcursor.away[0] && !vcursor.away[1]) || now.Sub(vcursor.firedAt) >= vcursorLatchMax) {
		vcursor.latched = false
	}
	if vcursorForming() && l >= vcursorGestureOut && r >= vcursorGestureOut {
		vcursor.firedFor, _ = vcursorPair()
		vcursor.firedAt, vcursor.latched, vcursor.navOwed = now, true, 0
		if vcursor.active {
			vcursorHide(true)
		} else {
			vcursorShow()
		}
		return true
	}
	// While the gesture is held its sticks belong to it: after a hide, the
	// left stick must not go on to move the focus.
	if vcursor.active || vcursor.latched {
		return true
	}
	if vcursorForming() {
		vcursor.navOwed = 0
		return true
	}
	// The left stick's first step waits a moment for the right stick: two
	// thumbs never leave in the same instant, and the step the left stick
	// would take first must not move the focus. A step it earns meanwhile is
	// owed, and taken by vcursorFlushNav if the right stick does not join.
	if (e.Axis == sdl.CONTROLLER_AXIS_LEFTX || e.Axis == sdl.CONTROLLER_AXIS_LEFTY) &&
		vcursor.away[0] && !vcursor.away[1] && now.Sub(vcursor.left[0]) < vcursorNavHold {
		if btn, ok := stickNavButton(config, e.Axis, e.Value); ok && vcursor.navOwed == 0 {
			vcursor.navOwed = btn
		}
		return true
	}
	return false
}

// vcursorFlushNav takes the left stick's held-back step once the right stick
// has had its moment to join, or drops it when the right stick did.
func vcursorFlushNav(config *Config, now time.Time) {
	if vcursor.navOwed == 0 {
		return
	}
	if vcursorForming() || vcursorBlocked(config) {
		vcursor.navOwed = 0
		return
	}
	if now.Sub(vcursor.left[0]) < vcursorNavHold {
		return
	}
	btn := vcursor.navOwed
	vcursor.navOwed = 0
	handleStickDpad(btn)
}

// vcursorShow puts the pointer up where it was last time (the centre at first).
func vcursorShow() {
	now := vcursorClock()
	if !vcursor.placed {
		vcursor.x, vcursor.y = float64(screenWidth)/2, float64(screenHeight)/2
		vcursor.placed = true
	}
	vcursor.active = true
	vcursor.scroll = [2]float64{}
	vcursor.last, vcursor.used, vcursor.shown = now, now, now
	// Chat scrolls with the right stick by itself; the pointer takes the
	// stick's events from here, so Chat would never see it come back.
	chatStickSpeed = 0
	vcursorPushMotion(true)
	vcursorWake()
	setToast("Virtual cursor: A clicks · move both sticks to hide it")
}

// vcursorHide puts the pointer away, releasing a held click first so nothing
// is left mid-drag.
func vcursorHide(toast bool) {
	if !vcursor.active {
		return
	}
	if vcursor.pressed {
		vcursor.pressed = false
		vcursorPushButton(false)
	}
	vcursor.active = false
	vcursor.scroll = [2]float64{}
	if toast {
		setToast("Virtual cursor off")
	}
}

// vcursorButton turns A into the left mouse button while the pointer is up.
// A D-pad press puts the pointer away and still moves the focus. It reports
// true when the press is consumed.
func vcursorButton(e *sdl.ControllerButtonEvent, config *Config) bool {
	if !vcursor.active {
		return false
	}
	if vcursorBlocked(config) {
		vcursorHide(false)
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
		if !down || vcursor.pressed {
			break
		}
		// A stick step queued while the gesture was forming arrives just
		// after the pointer came up: swallow it rather than hide.
		if vcursorClock().Sub(vcursor.shown) < vcursorShowGrace {
			return true
		}
		vcursorHide(false)
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

// vcursorTick runs once per frame: it takes a held-back stick step that is
// due, and moves and scrolls the pointer from the held stick values (axis
// events only arrive when a value changes, so a steady tilt has to be
// integrated here, not in the event handler).
func vcursorTick(config *Config) {
	now := vcursorClock()
	vcursorFlushNav(config, now)
	if !vcursor.active {
		return
	}
	if vcursorBlocked(config) {
		vcursorHide(false)
		return
	}
	gap := now.Sub(vcursor.last)
	dt := gap.Seconds()
	vcursor.last = now
	if gap > vcursorStaleGap {
		// Whatever happened while the loop was away is unknown: drop the
		// held click (its release went elsewhere) and re-read the sticks.
		vcursor.pressed = false
		stickResetAxes()
		vcursor.scroll = [2]float64{}
		vcursorForget()
	}
	if dt < 0 {
		dt = 0
	}
	// The gesture's own tilt is not a move: until both sticks are back home
	// (or held out past vcursorLatchMax) the pointer stays where it appeared;
	// nor does a hide gesture on its way move or scroll anything.
	if vcursor.latched && now.Sub(vcursor.firedAt) >= vcursorLatchMax {
		vcursor.latched = false
	}
	if vcursor.latched || vcursorForming() {
		vcursor.scroll = [2]float64{}
		return
	}
	// A stalled frame must not fling the pointer across the screen.
	if dt > 0.05 {
		dt = 0.05
	}
	busy := false

	// Left stick: the direction it points, at a speed on the curve. A tilt
	// under vcursorCreep (a stick settling just outside its dead zone) moves
	// nothing, so the pointer can still go idle.
	var dx, dy float64
	if lx, ly := stickShaped(config, stickLeft); lx != 0 || ly != 0 {
		if m := math.Hypot(lx, ly); m >= vcursorCreep {
			k := vcursorCurve(m) / m
			dx, dy = lx*k, ly*k
		}
	}
	if dx != 0 || dy != 0 {
		vcursor.x = clampFloat(vcursor.x+dx*vcursorMaxSpeed*dt, 0, float64(screenWidth-1))
		vcursor.y = clampFloat(vcursor.y+dy*vcursorMaxSpeed*dt, 0, float64(screenHeight-1))
		busy = vcursorPushMotion(false)
	}

	// Right stick, past vcursorScrollStart: up scrolls up (wheel Y is positive
	// away from the user), right scrolls right.
	rx, ry := stickShaped(config, stickRight)
	var wheel [2]int32
	for i, v := range [2]float64{rx, -ry} {
		if math.Abs(v) < vcursorScrollStart {
			vcursor.scroll[i] = 0
			continue
		}
		// The first notch comes at once, the rest at the tilt's rate.
		if vcursor.scroll[i] == 0 {
			vcursor.scroll[i] = math.Copysign(1, v)
		} else {
			vcursor.scroll[i] += vcursorCurve(v) * vcursorScrollRate * dt
		}
		n := int32(vcursor.scroll[i]) // truncates toward zero
		if n != 0 {
			wheel[i] = n
			vcursor.scroll[i] -= float64(n)
			// Keep the accumulator non-zero so the next tick does not take
			// it for a fresh tilt and send another immediate notch.
			if vcursor.scroll[i] == 0 {
				vcursor.scroll[i] = math.Copysign(1e-9, v)
			}
		}
	}
	if wheel[0] != 0 || wheel[1] != 0 {
		vcursorPushWheel(wheel[0], wheel[1])
		busy = true
	}

	if busy {
		vcursor.used = now
		vcursorWake()
	} else if !vcursor.pressed && now.Sub(vcursor.used) > vcursorIdleHide {
		vcursorHide(false)
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
