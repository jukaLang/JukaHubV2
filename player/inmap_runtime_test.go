package main

// inmap_runtime_test.go — the engine that runs a control profile, driven with
// a fake clock: plain remaps, holds, double taps, chords, the Select + R1
// switch, screen scopes, the pause rules and the recovery from a lost release.

import (
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// inmapTestRig is one engine under test: its clock, what it queued, and the
// buttons a fake pad reports as held.
type inmapTestRig struct {
	t      *testing.T
	config *Config
	now    uint64
	out    []string
	down   map[inmapButton]bool
}

// newInmapTestRig runs profile text (or the Default profile when text is "")
// with a fake clock and recorders, on a two-screen config showing Main.
func newInmapTestRig(t *testing.T, text string) *inmapTestRig {
	t.Helper()
	inmapTestRegistry(t)
	clearToastAfterTest(t)
	r := &inmapTestRig{t: t, now: 10000, down: map[inmapButton]bool{}}
	r.config = &Config{Scenes: []SceneConfig{{Name: "Main"}, {Name: "Tube"}}}
	prevClock, prevEmit, prevKey := inmapClockHook, inmapEmitHook, inmapKeyHook
	prevPhys, prevPad := inmapPhysicalHook, inmapPadHook
	prevScene, prevVK, prevApp := currentSceneIndex, virtualKeyboardActive, appConfig
	t.Cleanup(func() {
		inmapClockHook, inmapEmitHook, inmapKeyHook = prevClock, prevEmit, prevKey
		inmapPhysicalHook, inmapPadHook = prevPhys, prevPad
		currentSceneIndex, virtualKeyboardActive, appConfig = prevScene, prevVK, prevApp
		inmapResetEngine()
	})
	inmapResetEngine()
	currentSceneIndex, virtualKeyboardActive, appConfig = 0, false, nil
	inmapEngine.lastScene = "Main"
	inmapClockHook = func() uint64 { return r.now }
	inmapEmitHook = func(b inmapButton, down bool) {
		sign := "-"
		if down {
			sign = "+"
		}
		r.out = append(r.out, sign+inmapButtons[b].name)
	}
	inmapKeyHook = func(key sdl.Keycode, down bool) {
		sign := "-"
		if down {
			sign = "+"
		}
		r.out = append(r.out, sign+"key")
	}
	inmapPhysicalHook = func(b inmapButton, _ sdl.JoystickID) bool { return r.down[b] }
	inmapPadHook = func() bool { return true }
	if text != "" {
		p := inmapParse(text)
		if p.errorCount() != 0 {
			t.Fatalf("test profile has problems:\n%s", inmapProblemsText(p))
		}
		inmapReg.mu.Lock()
		inmapReg.user = []*inmapProfile{p}
		inmapReg.base = p.name
		inmapReg.gen++
		inmapReg.mu.Unlock()
	}
	return r
}

// press sends a pad press through the engine's entry and reports whether the
// engine took it.
func (r *inmapTestRig) press(b inmapButton) bool {
	r.t.Helper()
	r.down[b] = true
	if b == inmapBtnL2 || b == inmapBtnR2 {
		return inmapTriggerPress(b, r.config)
	}
	return inmapHandleButtonDown(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: inmapButtons[b].code}, r.config)
}

// release lets a button go (a trigger is noticed by the next tick).
func (r *inmapTestRig) release(b inmapButton) bool {
	r.t.Helper()
	r.down[b] = false
	if b == inmapBtnL2 || b == inmapBtnR2 {
		return false
	}
	return inmapButtonUp(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONUP, Button: inmapButtons[b].code}, r.config)
}

// wait advances the clock and runs one frame tick.
func (r *inmapTestRig) wait(ms uint64) {
	r.now += ms
	inmapTick(r.config)
}

// expect checks what the engine queued since the last check.
func (r *inmapTestRig) expect(want ...string) {
	r.t.Helper()
	if strings.Join(r.out, " ") != strings.Join(want, " ") {
		r.t.Fatalf("queued %v, want %v", r.out, want)
	}
	r.out = nil
}

func TestInmapPlainRemapHoldsWhileHeld(t *testing.T) {
	r := newInmapTestRig(t, "profile: P\nx -> button:y\nl1 -> key:PageUp\nr3 -> none\n")
	if !r.press(inmapBtnX) {
		t.Fatal("a remapped press went through unchanged")
	}
	r.expect("+y")
	r.wait(1000)
	r.expect()
	if !r.release(inmapBtnX) {
		t.Fatal("the release of a remapped press was not taken")
	}
	r.expect("-y")

	r.press(inmapBtnL1)
	r.release(inmapBtnL1)
	r.expect("+key", "-key")

	if !r.press(inmapBtnR3) || !r.release(inmapBtnR3) {
		t.Fatal("a disabled button reached the screen")
	}
	r.expect()

	if r.press(inmapBtnA) {
		t.Fatal("an unmapped button was taken")
	}
	r.release(inmapBtnA)
	r.expect()
}

func TestInmapActionThatDoesNotApplyFallsThrough(t *testing.T) {
	r := newInmapTestRig(t, "profile: P\ndpad_left -> seek:-10s\n")
	// Nothing plays in a test, so the seek has nothing to act on and the
	// D-pad keeps moving the focus.
	if r.press(inmapBtnLeft) {
		t.Fatal("a seek with nothing playing swallowed the press")
	}
	r.release(inmapBtnLeft)
	r.expect()
}

func TestInmapHold(t *testing.T) {
	r := newInmapTestRig(t, "profile: P\na hold 500ms -> button:y\n")
	if !r.press(inmapBtnA) {
		t.Fatal("a press with a hold line was not held back")
	}
	r.wait(499)
	r.expect()
	r.wait(1)
	r.expect("+y", "-y")
	r.wait(1000)
	r.release(inmapBtnA)
	r.expect()

	// Let go early: A itself, as a tap.
	r.press(inmapBtnA)
	r.wait(200)
	r.release(inmapBtnA)
	r.expect("+a", "-a")
	r.wait(1000)
	r.expect()
}

func TestInmapDoubleTap(t *testing.T) {
	r := newInmapTestRig(t, "profile: P\ny double -> button:x\ny -> button:b\n")
	r.press(inmapBtnY)
	r.release(inmapBtnY)
	r.wait(100)
	r.expect()
	if !r.press(inmapBtnY) {
		t.Fatal("the second tap was not taken")
	}
	r.expect("+x", "-x")
	r.release(inmapBtnY)
	r.wait(1000)
	r.expect()

	// One tap: once the window closes, the plain line runs as a tap.
	r.press(inmapBtnY)
	r.release(inmapBtnY)
	r.wait(inmapDoubleWindowMs)
	r.expect()
	r.wait(1)
	r.expect("+b", "-b")

	// Two taps too far apart are two single taps.
	r.press(inmapBtnY)
	r.release(inmapBtnY)
	r.now += inmapDoubleWindowMs + 50
	r.press(inmapBtnY)
	r.expect("+b", "-b")
	r.release(inmapBtnY)
	r.wait(inmapDoubleWindowMs + 1)
	r.expect("+b", "-b")
}

func TestInmapChord(t *testing.T) {
	r := newInmapTestRig(t, "profile: P\nl1+a -> button:start\n")
	if !r.press(inmapBtnL1) {
		t.Fatal("a chord's first button was not held back")
	}
	r.expect()
	if !r.press(inmapBtnA) {
		t.Fatal("the chord was not taken")
	}
	r.expect("+start", "-start")
	r.release(inmapBtnA)
	r.release(inmapBtnL1)
	r.wait(1000)
	r.expect()

	// L1 on its own is still L1, on its way up.
	r.press(inmapBtnL1)
	r.wait(100)
	r.expect()
	r.release(inmapBtnL1)
	r.expect("+l1", "-l1")

	// Held a long time without a chord: neither a chord nor a tap.
	r.press(inmapBtnL1)
	r.wait(inmapModifierTapMs + 100)
	r.release(inmapBtnL1)
	r.expect()

	// A on its own goes through untouched.
	if r.press(inmapBtnA) {
		t.Fatal("A alone was taken")
	}
	r.release(inmapBtnA)
}

func TestInmapSwitchHotkey(t *testing.T) {
	r := newInmapTestRig(t, "")
	before := inmapEffective()
	if !r.press(inmapBtnSelect) {
		t.Fatal("Select was not held back for the switch hotkey")
	}
	if !r.press(inmapBtnR1) {
		t.Fatal("Select + R1 did not switch")
	}
	r.release(inmapBtnR1)
	r.release(inmapBtnSelect)
	r.wait(1000)
	r.expect()
	if after := inmapEffective(); after == before || after.name != "Gaming" {
		t.Fatalf("the switch went from %q to %q, want Gaming", before.name, after.name)
	}
	if !strings.Contains(currentToast.Message, "Controls: Gaming") {
		t.Fatalf("toast = %q", currentToast.Message)
	}

	// Select alone is Back on its way up (on Default, which gives Select no
	// line of its own: Gaming's double tap would make it wait for a second).
	inmapChoose(nil, inmapFindProfile(inmapDefaultName), false)
	r.press(inmapBtnSelect)
	r.release(inmapBtnSelect)
	r.expect("+select", "-select")

	// R1 alone is R1 at once.
	if r.press(inmapBtnR1) {
		t.Fatal("R1 alone was held back")
	}
	r.release(inmapBtnR1)

	// With the hotkey off, or no pad attached, Select goes straight through.
	inmapReg.mu.Lock()
	inmapReg.hotkeyOn = false
	inmapReg.mu.Unlock()
	if r.press(inmapBtnSelect) {
		t.Fatal("Select was held back with the hotkey off")
	}
	r.release(inmapBtnSelect)
	inmapReg.mu.Lock()
	inmapReg.hotkeyOn = true
	inmapReg.mu.Unlock()
	inmapPadHook = func() bool { return false }
	if r.press(inmapBtnSelect) {
		t.Fatal("Select was held back with no pad attached")
	}
	r.release(inmapBtnSelect)
}

func TestInmapSceneScopeAndOverlays(t *testing.T) {
	r := newInmapTestRig(t, "profile: Media\nscenes: Tube\nmap:\nx -> button:y\n")
	if r.press(inmapBtnX) {
		t.Fatal("a Tube-only profile remapped X on Main")
	}
	r.release(inmapBtnX)
	currentSceneIndex = 1
	if !r.press(inmapBtnX) {
		t.Fatal("the Tube profile did not remap X on Tube")
	}
	r.release(inmapBtnX)
	r.expect("+y", "-y")

	// A panel over the screen owns the pad: screen lines stand aside.
	prevQS := qs.open
	qs.open = true
	t.Cleanup(func() { qs.open = prevQS })
	if r.press(inmapBtnX) {
		t.Fatal("a screen-scoped line ran under Quick Settings")
	}
	r.release(inmapBtnX)
	r.expect()
}

func TestInmapPausesAndPassesItsOwnPresses(t *testing.T) {
	r := newInmapTestRig(t, "profile: P\nx -> button:y\n")
	virtualKeyboardActive = true
	if r.press(inmapBtnX) {
		t.Fatal("a profile ran while the on-screen keyboard was up")
	}
	r.release(inmapBtnX)
	virtualKeyboardActive = false

	synth := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_X, Which: inmapSynthWhich}
	if inmapHandleButtonDown(synth, r.config) {
		t.Fatal("the engine took its own queued press")
	}
	repeat := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_X, Which: padRepeatWhich}
	if inmapHandleButtonDown(repeat, r.config) {
		t.Fatal("the engine took a hold-to-repeat press")
	}
	r.expect()

	r.config.Scenes = append(r.config.Scenes, SceneConfig{Name: inmapProfilesScene})
	currentSceneIndex = len(r.config.Scenes) - 1
	if r.press(inmapBtnX) {
		t.Fatal("a profile ran on the Control profiles screen")
	}
	r.release(inmapBtnX)
	r.expect()
}

func TestInmapLostReleaseIsRecovered(t *testing.T) {
	r := newInmapTestRig(t, "profile: P\nx -> button:y\n")
	r.press(inmapBtnX)
	r.expect("+y")
	// The release never reaches the engine (a game took the screen), but the
	// pad says X is up: the remapped Y is let go too.
	r.down[inmapBtnX] = false
	r.wait(inmapPhysicalGraceMs + 1)
	r.expect("-y")
	if inmapEngine.btn[inmapBtnX].held {
		t.Fatal("X is still marked down")
	}
}

func TestInmapTriggersHoldAndRelease(t *testing.T) {
	r := newInmapTestRig(t, "profile: P\nr2 hold 500ms -> button:y\nl2 -> button:x\n")
	if !r.press(inmapBtnR2) {
		t.Fatal("R2 with a hold line was not held back")
	}
	r.wait(500)
	r.expect("+y", "-y")
	r.release(inmapBtnR2)
	r.wait(10)
	r.expect()
	if inmapEngine.btn[inmapBtnR2].held {
		t.Fatal("R2 is still marked down after the latch let go")
	}

	r.press(inmapBtnL2)
	r.expect("+x")
	r.release(inmapBtnL2)
	r.wait(10)
	r.expect("-x")
}

func TestInmapProfileTargetAndAutoSwitch(t *testing.T) {
	r := newInmapTestRig(t, "profile: P\nl3 -> profile:Left-handed\n")
	r.press(inmapBtnL3)
	r.release(inmapBtnL3)
	if got := inmapEffective(); got.name != "Left-handed" {
		t.Fatalf("profile:Left-handed chose %q", got.name)
	}

	// With "Switch by screen" on, a profile naming a screen takes over there
	// and hands back when the screen is left.
	inmapReg.mu.Lock()
	inmapReg.autoOn = true
	inmapReg.mu.Unlock()
	currentSceneIndex = 1
	r.wait(1)
	if got := inmapEffective(); got.name != "Media browsing" {
		t.Fatalf("on Tube the effective profile is %q, want Media browsing", got.name)
	}
	currentSceneIndex = 0
	r.wait(1)
	if got := inmapEffective(); got.name != "Left-handed" {
		t.Fatalf("back on Main the effective profile is %q, want Left-handed", got.name)
	}
}

func TestInmapHeldDirectionFollowsTheProfile(t *testing.T) {
	r := newInmapTestRig(t, "profile: Lefty\na -> button:dpad_down\n")
	r.press(inmapBtnA)
	if dx, dy := inmapHeldNavDirection(); dx != 0 || dy != 1 {
		t.Fatalf("holding A (now Down) repeats as %d,%d, want 0,1", dx, dy)
	}
	r.release(inmapBtnA)
	if dx, dy := inmapHeldNavDirection(); dx != 0 || dy != 0 {
		t.Fatalf("after the release the held direction is %d,%d", dx, dy)
	}
}

func TestInmapCompileKeepsTheFirstLine(t *testing.T) {
	p := inmapParse("profile: P\nscene: Tube / a -> button:x\na -> button:y\nb hold -> home\nl1+r1 -> home\n")
	inmapValidate(p, inmapKnown{})
	onMain := inmapCompile(p, "Main")
	if onMain.plain[inmapBtnA].scene != -1 || onMain.plain[inmapBtnA].global != 1 {
		t.Fatalf("on Main A = %+v, want only the everywhere line", onMain.plain[inmapBtnA])
	}
	onTube := inmapCompile(p, "Tube")
	if onTube.plain[inmapBtnA].pick(false) != 0 || onTube.plain[inmapBtnA].pick(true) != 1 {
		t.Fatalf("on Tube A = %+v: the screen's line must win, unless a panel is up", onTube.plain[inmapBtnA])
	}
	if onTube.hold[inmapBtnB].global != 2 || len(onTube.chords) != 1 || !onTube.modGlobal[inmapBtnL1] {
		t.Fatalf("hold %+v, chords %+v, L1 modifier %v", onTube.hold[inmapBtnB], onTube.chords, onTube.modGlobal[inmapBtnL1])
	}
}
