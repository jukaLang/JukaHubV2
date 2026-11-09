package main

// inmap_runtime_test.go — the engine that runs a control profile, driven with
// a fake clock: plain remaps, holds, double taps, chords, screen scopes, the
// pause rules and the recovery from a lost release.

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
