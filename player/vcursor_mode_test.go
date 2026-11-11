package main

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// TestVcursorChordHold: Select + L1 fire once after the hold time, not
// before; L1's press and release are taken while Select is held; the chord
// fires again only after both buttons came up.
func TestVcursorChordHold(t *testing.T) {
	var c vcursorChordState
	t0 := time.Unix(100, 0)
	if c.press(vcursorChordSelect, true, t0) {
		t.Fatal("Select's press was taken")
	}
	if !c.press(vcursorChordL1, true, t0.Add(50*time.Millisecond)) {
		t.Fatal("L1 pressed with Select held was not taken")
	}
	if c.tick(t0.Add(50*time.Millisecond + vcursorChordHold - time.Millisecond)) {
		t.Fatal("the chord fired before the hold time")
	}
	if !c.tick(t0.Add(50*time.Millisecond + vcursorChordHold)) {
		t.Fatal("the chord did not fire after the hold time")
	}
	if c.tick(t0.Add(5 * time.Second)) {
		t.Fatal("one hold fired twice")
	}
	// Letting go of L1 and pressing it again while Select is still held is
	// the same hold: no second toggle.
	if !c.press(vcursorChordL1, false, t0.Add(6*time.Second)) {
		t.Error("the release of a taken L1 was not taken")
	}
	c.press(vcursorChordL1, true, t0.Add(6100*time.Millisecond))
	if c.tick(t0.Add(8 * time.Second)) {
		t.Error("the chord fired again without both buttons coming up")
	}
	// Both up, then a new hold: it fires again.
	c.press(vcursorChordL1, false, t0.Add(9*time.Second))
	if c.press(vcursorChordSelect, false, t0.Add(9*time.Second)) {
		t.Error("Select's release was taken")
	}
	c.press(vcursorChordSelect, true, t0.Add(10*time.Second))
	c.press(vcursorChordL1, true, t0.Add(10*time.Second))
	if !c.tick(t0.Add(10*time.Second + vcursorChordHold)) {
		t.Error("a second hold after letting go did not fire")
	}
}

// TestVcursorChordReleasedEarly: letting go before the hold time cancels it;
// L1 first and Select second is still the chord, but that L1 press was
// already the shoulder's and is not taken.
func TestVcursorChordReleasedEarly(t *testing.T) {
	var c vcursorChordState
	t0 := time.Unix(200, 0)
	c.press(vcursorChordSelect, true, t0)
	c.press(vcursorChordL1, true, t0)
	c.press(vcursorChordL1, false, t0.Add(300*time.Millisecond))
	if c.tick(t0.Add(2 * time.Second)) {
		t.Fatal("a chord let go early fired")
	}
	c.press(vcursorChordSelect, false, t0.Add(2*time.Second))

	var r vcursorChordState
	if r.press(vcursorChordL1, true, t0) {
		t.Error("L1 pressed alone was taken")
	}
	r.press(vcursorChordSelect, true, t0.Add(100*time.Millisecond))
	if !r.tick(t0.Add(100*time.Millisecond + vcursorChordHold)) {
		t.Error("L1 then Select did not fire")
	}
	if r.press(vcursorChordL1, false, t0.Add(2*time.Second)) {
		t.Error("the release of an L1 that was not taken was taken")
	}
	// Buttons that are not the chord's are never taken.
	if r.press(7, true, t0) {
		t.Error("another button was taken")
	}
}

// TestVcursorChordForgetsLostRelease: a chord button whose release never
// arrived is dropped once the pad says it is up, so L1 is the shoulder's again
// and a lone button cannot fire the chord.
func TestVcursorChordForgetsLostRelease(t *testing.T) {
	var c vcursorChordState
	t0 := time.Unix(300, 0)
	c.press(vcursorChordSelect, true, t0)
	c.press(vcursorChordL1, true, t0)
	selectUp := func(btn int) bool { return btn == vcursorChordL1 }
	c.forget(selectUp)
	if c.held[vcursorChordSelect] || !c.held[vcursorChordL1] {
		t.Fatalf("held after forget = %v", c.held)
	}
	if c.tick(t0.Add(2 * time.Second)) {
		t.Fatal("L1 alone fired the chord")
	}
	c.forget(func(int) bool { return false })
	if c.held[vcursorChordL1] || c.swallow || c.fired {
		t.Fatalf("state after both forgotten: %+v", c)
	}
	if c.press(vcursorChordL1, true, t0.Add(3*time.Second)) {
		t.Error("L1 was swallowed by a Select whose release was lost")
	}
}

// vcursorChordEvent sends one chord button through main.go's hook.
func vcursorChordEvent(config *Config, button uint8, down bool) bool {
	typ := uint32(sdl.CONTROLLERBUTTONUP)
	if down {
		typ = sdl.CONTROLLERBUTTONDOWN
	}
	return vcursorChordButton(&sdl.ControllerButtonEvent{Type: typ, Button: button}, config)
}

// vcursorHoldChord holds Select + L1 for the hold time and lets go.
func vcursorHoldChord(t *testing.T, config *Config, now *time.Time) {
	t.Helper()
	vcursorChordEvent(config, sdl.CONTROLLER_BUTTON_BACK, true)
	if !vcursorChordEvent(config, sdl.CONTROLLER_BUTTON_LEFTSHOULDER, true) {
		t.Fatal("L1 with Select held reached the screen")
	}
	*now = now.Add(vcursorChordHold + 10*time.Millisecond)
	vcursorTick(config)
	vcursorChordEvent(config, sdl.CONTROLLER_BUTTON_LEFTSHOULDER, false)
	vcursorChordEvent(config, sdl.CONTROLLER_BUTTON_BACK, false)
}

// vcursorDpad presses or releases a D-pad direction through the pointer.
func vcursorDpad(config *Config, button uint8, down bool) bool {
	typ := uint32(sdl.CONTROLLERBUTTONUP)
	if down {
		typ = sdl.CONTROLLERBUTTONDOWN
	}
	return vcursorButton(&sdl.ControllerButtonEvent{Type: typ, Button: button}, config)
}

// TestVcursorMouseMode: the chord turns mouse mode on — the pointer comes up
// and stays, the D-pad and the left stick move it, B is still Back — and the
// chord again turns it off, giving the D-pad back to the focus.
func TestVcursorMouseMode(t *testing.T) {
	now, _ := vcursorTestState(t, time.Unix(9000, 0))
	config := &Config{}

	vcursorHoldChord(t, config, now)
	if !vcursor.mode || !vcursor.active {
		t.Fatalf("mouse mode on: mode %v, pointer up %v", vcursor.mode, vcursor.active)
	}
	currentToastMu.Lock()
	toast := currentToast.Message
	currentToastMu.Unlock()
	if !strings.Contains(toast, "Mouse mode on") ||
		(!strings.Contains(toast, "Select + L1") && !strings.Contains(toast, "F6")) {
		t.Errorf("first toast %q does not say how to turn it off", toast)
	}

	// The D-pad moves the pointer and never reaches the focus.
	startX := vcursor.x
	if !vcursorDpad(config, sdl.CONTROLLER_BUTTON_DPAD_RIGHT, true) {
		t.Fatal("a D-pad press in mouse mode reached the screen")
	}
	for i := 0; i < 10; i++ {
		*now = now.Add(20 * time.Millisecond)
		vcursorTick(config)
	}
	if vcursor.x <= startX || !vcursor.active {
		t.Fatalf("the held D-pad did not move the pointer: %v → %v", startX, vcursor.x)
	}
	if !vcursorDpad(config, sdl.CONTROLLER_BUTTON_DPAD_RIGHT, false) {
		t.Error("the D-pad release in mouse mode reached the screen")
	}
	stopX := vcursor.x
	*now = now.Add(20 * time.Millisecond)
	vcursorTick(config)
	if vcursor.x != stopX {
		t.Errorf("the pointer kept moving after the D-pad was let go: %v → %v", stopX, vcursor.x)
	}
	// A queued hold-to-repeat press is taken but starts nothing.
	repeat := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_DOWN, Which: padRepeatWhich}
	if !vcursorButton(repeat, config) || !vcursorDpadIdle() {
		t.Error("a repeat press reached the screen or held the D-pad")
	}

	// B is still Back.
	if vcursorDpad(config, sdl.CONTROLLER_BUTTON_B, true) {
		t.Error("B was taken in mouse mode")
	}

	// The left stick belongs to the pointer in mouse mode.
	stickAxes[0] = 0.9
	if !vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_LEFTX, Value: 29490}, config) {
		t.Error("the left stick still stepped the focus in mouse mode")
	}
	leftX := vcursor.x
	*now = now.Add(20 * time.Millisecond)
	vcursorTick(config)
	if vcursor.x <= leftX {
		t.Errorf("the left stick did not move the pointer: %v → %v", leftX, vcursor.x)
	}
	stickAxes[0] = 0

	// No idle hide in mouse mode.
	*now = now.Add(vcursorIdleHide + 10*time.Second)
	vcursorTick(config)
	*now = now.Add(20 * time.Millisecond)
	vcursorTick(config)
	if !vcursor.active {
		t.Fatal("the pointer hid itself in mouse mode")
	}

	// The chord again: off, and the D-pad is the focus's again.
	vcursorHoldChord(t, config, now)
	if vcursor.mode || vcursor.active {
		t.Fatalf("mouse mode off: mode %v, pointer up %v", vcursor.mode, vcursor.active)
	}
	if vcursorDpad(config, sdl.CONTROLLER_BUTTON_DPAD_RIGHT, true) {
		t.Error("the D-pad was still taken after mouse mode went off")
	}
	if vcursorAxis(&sdl.ControllerAxisEvent{Axis: sdl.CONTROLLER_AXIS_LEFTX, Value: 29490}, config) {
		t.Error("the left stick was still the pointer's after mouse mode went off")
	}
}

// TestVcursorMouseModeSwitchOff: with Settings → Controls → Mouse mode off
// the chord does nothing and L1 is the shoulder's; turning the switch off
// also ends mouse mode.
func TestVcursorMouseModeSwitchOff(t *testing.T) {
	now, _ := vcursorTestState(t, time.Unix(9100, 0))
	off := &Config{}
	off.Variables.Custom = map[string]interface{}{vcursorModeKey: false}
	vcursorChordEvent(off, sdl.CONTROLLER_BUTTON_BACK, true)
	if vcursorChordEvent(off, sdl.CONTROLLER_BUTTON_LEFTSHOULDER, true) {
		t.Error("L1 was taken with Mouse mode off")
	}
	*now = now.Add(2 * time.Second)
	vcursorTick(off)
	if vcursor.mode {
		t.Fatal("the chord turned mouse mode on with the switch off")
	}

	on := &Config{}
	vcursorModeApply(on, true, false)
	if !vcursor.mode {
		t.Fatal("mouse mode did not come on")
	}
	vcursorModeSetEnabled(on, false)
	if vcursor.mode || vcursor.active || vcursorModeEnabled(on) {
		t.Errorf("switching Mouse mode off: mode %v, pointer %v, switch %v", vcursor.mode, vcursor.active, vcursorModeEnabled(on))
	}
}

// TestVcursorMouseModeKey: F6 is the chord's keyboard twin.
func TestVcursorMouseModeKey(t *testing.T) {
	vcursorTestState(t, time.Unix(9200, 0))
	config := &Config{}
	if !vcursorModeKeyToggle(config) || !vcursor.mode {
		t.Fatal("F6 did not turn mouse mode on")
	}
	if !vcursorModeKeyToggle(config) || vcursor.mode {
		t.Fatal("F6 did not turn mouse mode off")
	}
	off := &Config{}
	off.Variables.Custom = map[string]interface{}{vcursorModeKey: false}
	if !vcursorModeKeyToggle(off) || vcursor.mode {
		t.Error("F6 turned mouse mode on with the switch off")
	}
}

// TestVcursorMouseModePausedByPanels: a pad-modal panel puts the pointer away
// and gives the D-pad to the panel; when it closes the pointer comes back.
// The on-screen keyboard keeps the D-pad too.
func TestVcursorMouseModePausedByPanels(t *testing.T) {
	now, _ := vcursorTestState(t, time.Unix(9300, 0))
	oldQS, oldVK := qs.open, virtualKeyboardActive
	t.Cleanup(func() { qs.open, virtualKeyboardActive = oldQS, oldVK })
	config := &Config{}
	vcursorModeApply(config, true, false)

	qs.open = true
	*now = now.Add(20 * time.Millisecond)
	vcursorTick(config)
	if vcursor.active {
		t.Fatal("the pointer stayed up over Quick Settings")
	}
	if vcursorDpad(config, sdl.CONTROLLER_BUTTON_DPAD_DOWN, true) {
		t.Error("the D-pad did not reach Quick Settings")
	}
	qs.open = false
	*now = now.Add(20 * time.Millisecond)
	vcursorTick(config)
	if !vcursor.active || !vcursor.mode {
		t.Fatal("the pointer did not come back when Quick Settings closed")
	}

	virtualKeyboardActive = true
	if vcursorDpad(config, sdl.CONTROLLER_BUTTON_DPAD_DOWN, true) {
		t.Error("the D-pad did not reach the on-screen keyboard")
	}
	virtualKeyboardActive = false
	*now = now.Add(20 * time.Millisecond)
	vcursorTick(config)
	if !vcursor.active {
		t.Error("the pointer did not come back after the keyboard")
	}
}

// TestVcursorMouseModeChat: in mouse mode Chat keeps its right-stick scroll,
// and the pointer stays up for the D-pad.
func TestVcursorMouseModeChat(t *testing.T) {
	vcursorTestState(t, time.Unix(9400, 0))
	oldScene := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = oldScene })
	config := &Config{Scenes: []SceneConfig{{Name: chatSceneName}}}
	currentSceneIndex = 0
	vcursorModeApply(config, true, false)
	if !vcursor.active {
		t.Fatal("mouse mode did not put the pointer up in Chat")
	}
	if vcursorRight(config, 0.9, 0) {
		t.Error("the right stick was taken from Chat in mouse mode")
	}
	if !vcursor.active {
		t.Error("the right stick put the pointer away in mouse mode")
	}
	if !vcursorDpad(config, sdl.CONTROLLER_BUTTON_DPAD_UP, true) {
		t.Error("the D-pad did not move the pointer in Chat")
	}
}

// TestVcursorSettleSelect: turning mouse mode on marks a Select the profile
// engine is holding back as used, so its release does not go Back.
func TestVcursorSettleSelect(t *testing.T) {
	vcursorTestState(t, time.Unix(9500, 0))
	old := inmapEngine.btn
	t.Cleanup(func() { inmapEngine.btn = old })
	inmapEngine.btn[inmapBtnSelect] = inmapBtnState{held: true, mode: inmapPending}
	vcursorModeApply(&Config{}, true, false)
	if !inmapEngine.btn[inmapBtnSelect].chordUsed {
		t.Error("a held-back Select was not marked as the chord's")
	}
	// L1 joining a held Select marks it at once, before the hold completes,
	// so a profile's hold line on Select cannot run meanwhile.
	inmapEngine.btn[inmapBtnSelect] = inmapBtnState{held: true, mode: inmapPending}
	config := &Config{}
	vcursorChordEvent(config, sdl.CONTROLLER_BUTTON_BACK, true)
	vcursorChordEvent(config, sdl.CONTROLLER_BUTTON_LEFTSHOULDER, true)
	if !inmapEngine.btn[inmapBtnSelect].chordUsed {
		t.Error("L1 joining Select did not mark Select as the chord's")
	}
}

// TestVcursorSpeeds: the D-pad starts slow and ramps to the sticks' top
// speed; a diagonal is no faster; sums are capped; a creeping stick is still.
func TestVcursorSpeeds(t *testing.T) {
	if got := vcursorDpadSpeed(0); got != vcursorDpadMinSpeed {
		t.Errorf("D-pad speed at the press = %v, want %v", got, vcursorDpadMinSpeed)
	}
	if got := vcursorDpadSpeed(vcursorDpadRamp); math.Abs(got-vcursorMaxSpeed) > 1e-9 {
		t.Errorf("D-pad speed after the ramp = %v, want %v", got, vcursorMaxSpeed)
	}
	if got := vcursorDpadSpeed(10 * vcursorDpadRamp); math.Abs(got-vcursorMaxSpeed) > 1e-9 {
		t.Errorf("D-pad speed long after = %v, want %v", got, vcursorMaxSpeed)
	}
	prev := 0.0
	for ms := 0; ms <= 1500; ms += 50 {
		s := vcursorDpadSpeed(time.Duration(ms) * time.Millisecond)
		if s < prev {
			t.Fatalf("D-pad speed fell at %d ms: %v < %v", ms, s, prev)
		}
		prev = s
	}
	vx, vy := vcursorDpadVelocity(1, -1, 0)
	if m := math.Hypot(vx, vy); math.Abs(m-vcursorDpadMinSpeed) > 1e-9 || vx <= 0 || vy >= 0 {
		t.Errorf("diagonal D-pad = (%v, %v), speed %v", vx, vy, m)
	}
	if vx, vy := vcursorDpadVelocity(0, 0, time.Second); vx != 0 || vy != 0 {
		t.Error("an idle D-pad moved")
	}
	if vx, vy := vcursorStickVelocity(vcursorCreep/2, 0); vx != 0 || vy != 0 {
		t.Error("a creeping stick moved the pointer")
	}
	if vx, _ := vcursorStickVelocity(1, 0); math.Abs(vx-vcursorMaxSpeed) > 1e-9 {
		t.Errorf("full tilt = %v, want %v", vx, vcursorMaxSpeed)
	}
	if vx, _ := vcursorStickVelocity(0.5, 0); vx <= 0 || vx >= vcursorMaxSpeed/2 {
		t.Errorf("half tilt = %v, want a gentler speed than linear", vx)
	}
	cx, cy := vcursorClampSpeed(vcursorMaxSpeed, vcursorMaxSpeed)
	if m := math.Hypot(cx, cy); math.Abs(m-vcursorMaxSpeed) > 1e-9 {
		t.Errorf("summed speed not capped: %v", m)
	}
}

// TestVcursorModeSettingsRows: the two mouse rows follow the virtual cursor's
// in Settings → Controls.
func TestVcursorModeSettingsRows(t *testing.T) {
	rows := stickCalSettingsRows()
	if len(rows) < 3 || rows[0].ID != vcursorKey || rows[1].ID != vcursorModeKey || rows[2].ID != vcursorGamesKey {
		var ids []string
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		t.Fatalf("Controls rows %v: want the virtual cursor, then the two mouse rows", ids)
	}
	config := &Config{}
	if !rows[1].Get(config) || rows[2].Get(config) {
		t.Error("defaults: Mouse mode should be on and Mouse in games and apps off")
	}
	if rows[2].Visible(config) != vcursorBridgeSupported() {
		t.Error("the games row shows where the bridge cannot run")
	}
}

// TestVcursorLegendRow: the controller card lists the chord (F6 on a
// keyboard) while Mouse mode is on.
func TestVcursorLegendRow(t *testing.T) {
	old := appConfig
	t.Cleanup(func() { appConfig = old })
	appConfig = &Config{}
	if row, ok := vcursorLegendRow(false); !ok || row.Button != "Select + L1" {
		t.Errorf("pad row = %+v, %v", row, ok)
	}
	if row, ok := vcursorLegendRow(true); !ok || row.Button != "F6" {
		t.Errorf("keyboard row = %+v, %v", row, ok)
	}
	appConfig.Variables.Custom = map[string]interface{}{vcursorModeKey: false}
	if _, ok := vcursorLegendRow(false); ok {
		t.Error("the chord is listed with Mouse mode off")
	}
}

// TestEvdevFaceCodes: the bridge's A, B and X follow the firmware swap, the
// way the rest of JukaHub reads the pad.
func TestEvdevFaceCodes(t *testing.T) {
	plain := &Config{}
	plain.Variables.Custom = map[string]interface{}{"swapABButtons": false, "swapXYButtons": false}
	p := mbridgeParamsFor(plain)
	if p.codeA != evdevBtnSouth || p.codeB != evdevBtnEast || p.codeX != evdevBtnNorth {
		t.Errorf("no swap: A %#x B %#x X %#x", p.codeA, p.codeB, p.codeX)
	}
	swapped := &Config{}
	swapped.Variables.Custom = map[string]interface{}{"swapABButtons": true, "swapXYButtons": true}
	p = mbridgeParamsFor(swapped)
	if p.codeA != evdevBtnEast || p.codeB != evdevBtnSouth || p.codeX != evdevBtnWest {
		t.Errorf("swapped: A %#x B %#x X %#x", p.codeA, p.codeB, p.codeX)
	}
}
