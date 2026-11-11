package main

// vcursor_mode.go — mouse mode: the virtual cursor as a mode you turn on.
//
// The right-stick pointer (virtualcursor.go) comes and goes by itself, and a
// TrimUI Brick, which has no sticks, never had a pointer at all. GammaOS (an
// Android firmware for the same handhelds) answers both with a mouse mode
// that a held chord turns on and off in every app. JukaHub does the same:
//
//	hold Select + L1     mouse mode on / off (about 0.7 s)
//	D-pad, either stick  move the pointer (a held D-pad speeds up)
//	A                    click; hold A and move to drag
//	L2 / R2              scroll under the pointer
//	B                    Back, as always
//
// GammaOS uses Select + R1. In JukaHub, holding Select and pressing R1 already
// steps through the control profiles (inmap_runtime.go, on by default), and
// that acts the moment R1 goes down, so a held Select + R1 would switch the
// profile first. Mouse mode takes the mirror image, Select + L1, which nothing
// else uses; a control-profile line for select+l1 only runs with Mouse mode
// off, the way a select+r1 line only runs with the profile hotkey off.
//
// While mouse mode is on the pointer stays up (no idle hide) and the D-pad and
// the left stick move it instead of the focus. B and every other button keep
// their jobs, so nothing is ever trapped behind the pointer. The on-screen
// keyboard keeps the D-pad (it reads the pad itself), and the screens that own
// the pad outright — the calibrations, the controller test, a ringing alarm,
// Quick Settings, the wallpaper picker, JukaLand — put the pointer away until
// they close. Chat keeps its right-stick scroll; the D-pad and the left stick
// move the pointer there. F6 is the keyboard's twin of the chord.
//
// The chord is read from the physical buttons before the accessibility remap,
// like the quit chord (exit_chord.go): it is a system shortcut, the same on
// every screen and in every profile. L1's press is taken while Select is held,
// so the shoulder does not also turn a page. Select's own press is held back
// by the control-profile engine until it is released (its profile hotkey), and
// the chord marks it used, so its release does not go Back either.
//
// Settings → Controls → Mouse mode turns the chord off. Mouse in games and
// apps carries the same chord into games and apps (vcursor_bridge_linux.go).

import (
	"math"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// vcursorModeKey is the Custom setting that lets the chord (and F6) turn
	// mouse mode on: on unless turned off.
	vcursorModeKey = "mouseMode"
	// vcursorGamesKey is "Mouse in games and apps": off unless turned on.
	vcursorGamesKey = "mouseInGames"
	// vcursorChordHold is how long Select + L1 have to be held together.
	vcursorChordHold = 700 * time.Millisecond
	// vcursorDpadMinSpeed is a D-pad's speed the moment it is pressed, logical
	// px/s: a tap moves the pointer a pixel or two.
	vcursorDpadMinSpeed = 90.0
	// vcursorDpadRamp is how long a held D-pad takes to reach full speed.
	vcursorDpadRamp = 1200 * time.Millisecond
	// vcursorDpadStale is how long a D-pad press is trusted before the pad is
	// asked whether it is still held (a release a modal loop swallowed).
	vcursorDpadStale = 250 * time.Millisecond
)

// The two buttons of the mouse-mode chord.
const (
	vcursorChordSelect = 0
	vcursorChordL1     = 1
)

// vcursorChordState follows the chord: which of its two buttons are held,
// since when both are, whether this hold already toggled, and whether L1's
// press was taken (its release then is too). The in-app pointer and the games
// bridge each keep one.
type vcursorChordState struct {
	held    [2]bool
	since   time.Time
	fired   bool
	swallow bool
}

// press feeds one press or release of a chord button and reports whether the
// event belongs to the chord: L1 pressed while Select is held, and that L1's
// release. Select itself is never taken.
func (c *vcursorChordState) press(btn int, down bool, now time.Time) bool {
	if btn != vcursorChordSelect && btn != vcursorChordL1 {
		return false
	}
	if !down {
		c.held[btn] = false
		c.since = time.Time{}
		if !c.held[vcursorChordSelect] && !c.held[vcursorChordL1] {
			// Both up: the next hold may toggle again.
			c.fired = false
		}
		if btn == vcursorChordL1 && c.swallow {
			c.swallow = false
			return true
		}
		return false
	}
	if c.held[btn] {
		// A second press without a release (a lost release, a driver
		// repeat) changes nothing.
		return btn == vcursorChordL1 && c.swallow
	}
	c.held[btn] = true
	if c.held[vcursorChordSelect] && c.held[vcursorChordL1] && !c.fired {
		c.since = now
	}
	if btn == vcursorChordL1 && c.held[vcursorChordSelect] {
		c.swallow = true
		return true
	}
	return false
}

// forget drops a button the pad says is no longer down: its release went
// somewhere the chord never saw (a modal loop, the ringing alarm, the
// controller test). Without this a lost Select release would swallow every
// later L1 press, and a lost L1 release would let Select alone fire.
func (c *vcursorChordState) forget(heldNow func(btn int) bool) {
	for b := range c.held {
		if !c.held[b] || heldNow(b) {
			continue
		}
		c.held[b] = false
		c.since = time.Time{}
		if b == vcursorChordL1 {
			c.swallow = false
		}
	}
	if !c.held[vcursorChordSelect] && !c.held[vcursorChordL1] {
		c.fired = false
	}
}

// tick reports whether the chord has just been held long enough. It fires
// once per hold: both buttons have to come up before it can fire again.
func (c *vcursorChordState) tick(now time.Time) bool {
	if c.fired || c.since.IsZero() || !c.held[vcursorChordSelect] || !c.held[vcursorChordL1] {
		return false
	}
	if now.Sub(c.since) < vcursorChordHold {
		return false
	}
	c.fired = true
	return true
}

// vcursorChord is the in-app chord; main thread only.
var vcursorChord vcursorChordState

// vcursorModeSound is the feedback of a toggle. Tests silence it.
var vcursorModeSound = PlayToggleSound

// vcursorChordPadHeld asks the pad whether a chord button is physically down
// right now (Select may arrive as GUIDE on some firmwares). With no pad to ask
// — none open, or a test — the events are trusted. Main thread.
var vcursorChordPadHeld = func(btn int) bool {
	pad := gameController
	if pad == nil {
		return true
	}
	if btn == vcursorChordL1 {
		return pad.Button(sdl.CONTROLLER_BUTTON_LEFTSHOULDER) != 0
	}
	return pad.Button(sdl.CONTROLLER_BUTTON_BACK) != 0 || pad.Button(sdl.CONTROLLER_BUTTON_GUIDE) != 0
}

// ── Settings ──────────────────────────────────────────────────────────────

// vcursorModeEnabled reports Settings → Controls → Mouse mode.
func vcursorModeEnabled(config *Config) bool {
	if config == nil {
		return true
	}
	return customBool(config.Variables.Custom, vcursorModeKey, true)
}

// vcursorGamesEnabled reports Settings → Controls → Mouse in games and apps.
func vcursorGamesEnabled(config *Config) bool {
	if config == nil {
		return false
	}
	return customBool(config.Variables.Custom, vcursorGamesKey, false)
}

// vcursorStoreBool saves one of the two switches.
func vcursorStoreBool(config *Config, key string, on bool) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[key] = on
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// vcursorModeSetEnabled stores the Mouse mode switch; turning it off also
// turns mouse mode off.
func vcursorModeSetEnabled(config *Config, on bool) {
	if !on {
		vcursorModeApply(config, false, false)
		vcursorChord = vcursorChordState{}
	}
	vcursorStoreBool(config, vcursorModeKey, on)
}

// vcursorModeDetail is the Mouse mode row's second line, in the words the
// hints use (the pad's chord, or F6 on a keyboard).
func vcursorModeDetail(*Config) string {
	if hintsUseKeyboard() {
		return "F6 (or Select + L1 on the pad) turns a pointer on"
	}
	if !stickHardware() {
		return "Hold Select + L1: the D-pad moves a pointer"
	}
	return "Hold Select + L1: the D-pad and sticks move a pointer"
}

// vcursorModeSettingsRows are the two mouse rows of Settings → Controls. They
// sit under the virtual cursor (stickcal.go): the same pointer, and the Brick,
// which has no sticks, gets them too.
func vcursorModeSettingsRows() []setRow {
	return []setRow{
		{
			ID: vcursorModeKey, Label: "Mouse mode", Kind: setKindToggle,
			Detail: vcursorModeDetail,
			Get:    vcursorModeEnabled,
			Set: func(c *Config, on bool) {
				vcursorModeSetEnabled(c, on)
				setToast("Mouse mode: " + setOnOff(on))
			},
		},
		{
			// The same chord inside a game: the pad becomes a mouse for the
			// app (vcursor_bridge_linux.go). A Linux handheld only.
			ID: vcursorGamesKey, Label: "Mouse in games and apps", Kind: setKindToggle,
			Detail:  setStatic("Select + L1 in a game; only apps with a pointer show it"),
			Visible: func(*Config) bool { return vcursorBridgeSupported() },
			Get:     vcursorGamesEnabled,
			Set: func(c *Config, on bool) {
				vcursorStoreBool(c, vcursorGamesKey, on)
				setToast("Mouse in games and apps: " + setOnOff(on))
			},
		},
	}
}

// vcursorLegendRow is the controller card's "Every screen" row for the chord
// (controlslegend.go), or false with Mouse mode off.
func vcursorLegendRow(keyboard bool) (FooterHint, bool) {
	if !vcursorModeEnabled(appConfig) {
		return FooterHint{}, false
	}
	if keyboard {
		return FooterHint{Button: "F6", Label: "Mouse mode"}, true
	}
	return FooterHint{Button: "Select + L1", Label: "Mouse mode (hold)"}, true
}

// ── The mode ──────────────────────────────────────────────────────────────

// vcursorModeLive reports mouse mode in force: on, and the on-screen keyboard
// not up (it moves its own highlight with the D-pad, polling the pad, so the
// D-pad cannot be the pointer's there).
func vcursorModeLive(*Config) bool {
	return vcursor.mode && !virtualKeyboardActive
}

// vcursorModeApply turns mouse mode on or off; announce says so in a toast
// and with the toggle sound.
func vcursorModeApply(config *Config, on, announce bool) {
	if on == vcursor.mode {
		return
	}
	// The Select half of the chord must not also go Back on its release.
	vcursorSettleSelect()
	vcursor.mode = on
	vcursorDpadClear()
	if on {
		// The plain pointer's how-to toast would replace mouse mode's.
		vcursor.told = true
		if vcursorModeLive(config) && !vcursorBlocked(config) {
			vcursorShow()
		}
	} else {
		vcursorHide()
	}
	if !announce {
		return
	}
	if on {
		setToast(vcursorModeOnText(!vcursor.modeTold))
		vcursor.modeTold = true
	} else {
		setToast("Mouse mode off")
	}
	if vcursorModeSound != nil {
		vcursorModeSound()
	}
}

// vcursorModeToggle flips mouse mode: the chord's and F6's action.
func vcursorModeToggle(config *Config) {
	vcursorModeApply(config, !vcursor.mode, true)
}

// vcursorModeKeyToggle is F6, the keyboard's twin of the chord (shortcuts.go).
// It reports whether the key was used.
func vcursorModeKeyToggle(config *Config) bool {
	if !vcursorModeEnabled(config) && !vcursor.mode {
		setToast("Mouse mode is off in Settings → Controls")
		return true
	}
	vcursorModeToggle(config)
	return true
}

// vcursorModeOnText is the toast for mouse mode turning on: the whole how-to
// the first time in a run, two words after that.
func vcursorModeOnText(first bool) string {
	if !first {
		return "Mouse mode on"
	}
	if hintsUseKeyboard() {
		return "Mouse mode on · F6 turns it off"
	}
	move := "the D-pad and sticks move it"
	if !stickHardware() {
		move = "the D-pad moves it"
	}
	return "Mouse mode on · " + move + ", " + hintKeyFor("A", "Click") + " clicks · hold Select + L1 to turn it off"
}

// vcursorSettleSelect tells the control-profile engine that a held Select was
// the first half of a chord: its release then runs nothing (no Back). The
// engine holds Select back for its own profile hotkey; this is the same mark
// its hotkey sets (inmap_runtime.go inmapPress).
func vcursorSettleSelect() {
	st := &inmapEngine.btn[inmapBtnSelect]
	if st.held && st.mode == inmapPending {
		st.chordUsed = true
	}
}

// vcursorChordButton feeds the chord from main.go, ahead of the remap. It
// reports whether the press or release was taken (L1 while Select is held).
func vcursorChordButton(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || (e.Type != sdl.CONTROLLERBUTTONDOWN && e.Type != sdl.CONTROLLERBUTTONUP) {
		return false
	}
	var btn int
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_BACK:
		btn = vcursorChordSelect
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		btn = vcursorChordL1
	default:
		return false
	}
	if !vcursorModeEnabled(config) {
		vcursorChord = vcursorChordState{}
		return false
	}
	// A Local play guest's pad belongs to a player, not to the system.
	if e.Which >= 0 && hotseatExtraIndex(e.Which) >= 0 {
		return false
	}
	down := e.Type == sdl.CONTROLLERBUTTONDOWN
	if down {
		vcursorChord.forget(vcursorChordPadHeld)
	}
	taken := vcursorChord.press(btn, down, vcursorClock())
	if taken && down {
		// L1 joined a held Select: from here Select is the chord's, so a
		// profile's hold line on Select must not run while the chord is
		// timed, and its release must not go Back even if L1 is let go early.
		vcursorSettleSelect()
	}
	return taken
}

// ── Moving ────────────────────────────────────────────────────────────────

// vcursorStickVelocity is the pointer velocity, logical px/s, a calibrated
// stick position asks for: its direction at a speed on vcursorCurve, nothing
// under vcursorCreep (a stick settling just outside its dead zone).
func vcursorStickVelocity(x, y float64) (float64, float64) {
	m := math.Hypot(x, y)
	if m < vcursorCreep {
		return 0, 0
	}
	k := vcursorCurve(m) / m * vcursorMaxSpeed
	return x * k, y * k
}

// vcursorDpadSpeed is a held D-pad's speed after held: slow at first for a
// precise nudge, easing up to the sticks' full speed over vcursorDpadRamp.
func vcursorDpadSpeed(held time.Duration) float64 {
	t := held.Seconds() / vcursorDpadRamp.Seconds()
	t = clampFloat(t, 0, 1)
	return vcursorDpadMinSpeed + (vcursorMaxSpeed-vcursorDpadMinSpeed)*t*t
}

// vcursorDpadVelocity is the velocity of a held D-pad direction (dx, dy each
// -1, 0 or 1); a diagonal goes no faster than a straight line.
func vcursorDpadVelocity(dx, dy int, held time.Duration) (float64, float64) {
	if dx == 0 && dy == 0 {
		return 0, 0
	}
	speed := vcursorDpadSpeed(held)
	fx, fy := float64(dx), float64(dy)
	if dx != 0 && dy != 0 {
		fx, fy = fx*math.Sqrt2/2, fy*math.Sqrt2/2
	}
	return fx * speed, fy * speed
}

// vcursorClampSpeed keeps a summed velocity (two sticks and the D-pad at once)
// at the pointer's top speed.
func vcursorClampSpeed(vx, vy float64) (float64, float64) {
	m := math.Hypot(vx, vy)
	if m <= vcursorMaxSpeed || m == 0 {
		return vx, vy
	}
	k := vcursorMaxSpeed / m
	return vx * k, vy * k
}

// vcursorDpadIndex is the slot of a D-pad button in vcursor.dpad, -1 for any
// other button.
func vcursorDpadIndex(button uint8) int {
	switch button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return 0
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return 1
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return 2
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return 3
	}
	return -1
}

// vcursorDpadIdle reports that no D-pad direction is held.
func vcursorDpadIdle() bool {
	return !vcursor.dpad[0] && !vcursor.dpad[1] && !vcursor.dpad[2] && !vcursor.dpad[3]
}

// vcursorDpadEvent records a D-pad press or release in mouse mode. The first
// direction pressed starts the speed ramp; letting go of all of them ends it.
func vcursorDpadEvent(button uint8, down bool) {
	i := vcursorDpadIndex(button)
	if i < 0 {
		return
	}
	wasIdle := vcursorDpadIdle()
	vcursor.dpad[i] = down
	switch {
	case vcursorDpadIdle():
		vcursor.dpadSince = time.Time{}
	case wasIdle:
		vcursor.dpadSince = vcursorClock()
	}
}

// vcursorDpadDir is the held D-pad as a direction.
func vcursorDpadDir() (int, int) {
	dx, dy := 0, 0
	if vcursor.dpad[2] {
		dx--
	}
	if vcursor.dpad[3] {
		dx++
	}
	if vcursor.dpad[0] {
		dy--
	}
	if vcursor.dpad[1] {
		dy++
	}
	return dx, dy
}

// vcursorDpadClear forgets the held D-pad.
func vcursorDpadClear() {
	vcursor.dpad = [4]bool{}
	vcursor.dpadSince = time.Time{}
}

// vcursorModeVelocity is what mouse mode adds to the right stick: the left
// stick and the held D-pad. A D-pad press the pad no longer reports as held
// (its release went to a modal loop) is dropped after vcursorDpadStale; the
// pad is read the way hold-to-repeat reads it, through the accessibility
// remap and the control profile (padRepeatHeldDirection), so a direction a
// profile makes from another button keeps moving the pointer.
func vcursorModeVelocity(config *Config, now time.Time) (float64, float64) {
	lx, ly := stickShaped(config, stickLeft)
	vx, vy := vcursorStickVelocity(lx, ly)
	dx, dy := vcursorDpadDir()
	if dx == 0 && dy == 0 {
		return vx, vy
	}
	if gameController != nil && now.Sub(vcursor.dpadSince) > vcursorDpadStale {
		if px, py := padRepeatHeldDirection(); px == 0 && py == 0 {
			vcursorDpadClear()
			return vx, vy
		}
	}
	bx, by := vcursorDpadVelocity(dx, dy, now.Sub(vcursor.dpadSince))
	return vx + bx, vy + by
}

// ── Games and apps (the settings side) ────────────────────────────────────

// evdevFaceCode is the evdev key behind an SDL face button on the X360-style
// mapping the TrimUI pad uses (main.go: a:b0, b:b1, x:b2, y:b3 — the pad's
// first four keys in code order, BTN_SOUTH, BTN_EAST, BTN_NORTH, BTN_WEST).
func evdevFaceCode(button uint8) uint16 {
	switch button {
	case sdl.CONTROLLER_BUTTON_B:
		return evdevBtnEast
	case sdl.CONTROLLER_BUTTON_X:
		return evdevBtnNorth
	case sdl.CONTROLLER_BUTTON_Y:
		return evdevBtnWest
	}
	return evdevBtnSouth
}

// mbridgeParamsFor reads, on the main thread, what the games bridge needs from
// the settings: which evdev buttons are A, B, X and Y once the handheld's
// face-button swaps are undone (padFaceButton — the printed A is the pad's
// BTN_EAST on a TrimUI), and both sticks' calibration.
func mbridgeParamsFor(config *Config) mbridgeParams {
	return mbridgeParams{
		codeA: evdevFaceCode(padFaceButton(config, sdl.CONTROLLER_BUTTON_A)),
		codeB: evdevFaceCode(padFaceButton(config, sdl.CONTROLLER_BUTTON_B)),
		codeX: evdevFaceCode(padFaceButton(config, sdl.CONTROLLER_BUTTON_X)),
		codeY: evdevFaceCode(padFaceButton(config, sdl.CONTROLLER_BUTTON_Y)),
		calL:  stickCalFor(config, stickLeft),
		calR:  stickCalFor(config, stickRight),
	}
}

// mbridgeDefaultParams is mbridgeParamsFor with every setting at its default
// (the handheld swaps A/B and X/Y, the sticks are uncalibrated), for a launch
// that started off the main thread with no settings read for it. It reads no
// config, so any goroutine may call it.
func mbridgeDefaultParams() mbridgeParams {
	a, b := uint8(sdl.CONTROLLER_BUTTON_A), uint8(sdl.CONTROLLER_BUTTON_B)
	x, y := uint8(sdl.CONTROLLER_BUTTON_X), uint8(sdl.CONTROLLER_BUTTON_Y)
	if P().IsHandheld() {
		a, b, x, y = b, a, y, x
	}
	return mbridgeParams{
		codeA: evdevFaceCode(a),
		codeB: evdevFaceCode(b),
		codeX: evdevFaceCode(x),
		codeY: evdevFaceCode(y),
		calL:  stickCalDefault(),
		calR:  stickCalDefault(),
	}
}
