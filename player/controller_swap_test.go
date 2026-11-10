package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestAbButtonsSwappedReadsSetting(t *testing.T) {
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	// Handhelds default to the swap (their firmware reports A/B inverted);
	// desktops default to the SDL mapping.
	if got, want := abButtonsSwapped(config), P().IsHandheld(); got != want {
		t.Fatalf("default swap = %v, want %v for this platform", got, want)
	}
	config.Variables.Custom["swapABButtons"] = "true"
	if !abButtonsSwapped(config) {
		t.Fatal("swapABButtons=true must enable the swap")
	}
	config.Variables.Custom["swapABButtons"] = false
	if abButtonsSwapped(config) {
		t.Fatal("swapABButtons=false must disable the swap")
	}
	if abButtonsSwapped(nil) {
		t.Fatal("nil config must not swap")
	}
}

func TestMigrateHandheldButtonMapping(t *testing.T) {
	user := &UserConfig{Variables: UserVariables{Custom: map[string]interface{}{"swapABButtons": false}}}
	if !migrateHandheldButtonMapping(user, true) {
		t.Fatal("handheld migration must change the stale default")
	}
	if user.Variables.Custom["swapABButtons"] != true {
		t.Fatal("handheld must default to swapped A/B")
	}
	if user.Variables.Custom[abSwapMigrationKey] != true {
		t.Fatal("migration marker must be stored")
	}
	if migrateHandheldButtonMapping(user, true) {
		t.Fatal("migration must only run once")
	}
	// A user who turns the swap back off keeps that choice.
	user.Variables.Custom["swapABButtons"] = false
	if migrateHandheldButtonMapping(user, true) {
		t.Fatal("migration must not override a later choice")
	}
	if got := abButtonsSwapped(&Config{Variables: Variables{Custom: user.Variables.Custom}}); got {
		t.Fatal("explicit swapABButtons=false must be respected")
	}

	desktop := &UserConfig{Variables: UserVariables{Custom: map[string]interface{}{"swapABButtons": false}}}
	if migrateHandheldButtonMapping(desktop, false) {
		t.Fatal("desktop configs must not be migrated")
	}
	if desktop.Variables.Custom["swapABButtons"] != false {
		t.Fatal("desktop setting must be untouched")
	}
}

// Some handheld firmwares expose the B face button as GUIDE rather than B.
// It must still reach the back handlers, which only know B and BACK.
func TestNormalizeHandheldBackButton(t *testing.T) {
	if got := normalizeHandheldBackButton(sdl.CONTROLLER_BUTTON_GUIDE, true); got != sdl.CONTROLLER_BUTTON_BACK {
		t.Errorf("handheld GUIDE = %d, want BACK", got)
	}
	if got := normalizeHandheldBackButton(sdl.CONTROLLER_BUTTON_GUIDE, false); got != sdl.CONTROLLER_BUTTON_GUIDE {
		t.Errorf("desktop GUIDE must stay untouched, got %d", got)
	}
	if got := normalizeHandheldBackButton(sdl.CONTROLLER_BUTTON_A, true); got != sdl.CONTROLLER_BUTTON_A {
		t.Errorf("A must not be remapped, got %d", got)
	}
	if got := normalizeHandheldBackButton(sdl.CONTROLLER_BUTTON_B, true); got != sdl.CONTROLLER_BUTTON_B {
		t.Errorf("B must not be remapped, got %d", got)
	}
}

// The back fallback must find Home even when the config does not name it
// "Main" (older exported configs keep a different first-scene name).
func TestHomeSceneIndex(t *testing.T) {
	home := &Config{Scenes: []SceneConfig{
		{Name: "Intro"},
		{Name: "Dashboard", Layout: "home"},
		{Name: "Settings"},
	}}
	if got := homeSceneIndex(home); got != 1 {
		t.Fatalf("homeSceneIndex with a layout scene = %d, want 1", got)
	}

	named := &Config{Scenes: []SceneConfig{{Name: "Intro"}, {Name: "Main"}}}
	if got := homeSceneIndex(named); got != 1 {
		t.Fatalf("homeSceneIndex with Main = %d, want 1", got)
	}

	unnamed := &Config{Scenes: []SceneConfig{{Name: "Intro"}, {Name: "Settings"}}}
	if got := homeSceneIndex(unnamed); got != 0 {
		t.Fatalf("homeSceneIndex fallback = %d, want 0", got)
	}
	if got := homeSceneIndex(nil); got != -1 {
		t.Fatalf("homeSceneIndex(nil) = %d, want -1", got)
	}
}

func TestApplyButtonSwapFlipsABOnly(t *testing.T) {
	// The X/Y half is its own setting (TestApplyButtonSwapFlipsXYOnHandheld). It
	// is pinned off here: left unset it follows the platform, and a Linux run
	// can be the TrimUI platform, where it is on.
	on := &Config{Variables: Variables{Custom: map[string]interface{}{
		"swapABButtons": true, "swapXYButtons": false,
	}}}

	a := &sdl.ControllerButtonEvent{Button: sdl.CONTROLLER_BUTTON_A}
	applyButtonSwap(on, a)
	if a.Button != sdl.CONTROLLER_BUTTON_B {
		t.Fatalf("A should flip to B, got %v", a.Button)
	}

	b := &sdl.ControllerButtonEvent{Button: sdl.CONTROLLER_BUTTON_B}
	applyButtonSwap(on, b)
	if b.Button != sdl.CONTROLLER_BUTTON_A {
		t.Fatalf("B should flip to A, got %v", b.Button)
	}

	y := &sdl.ControllerButtonEvent{Button: sdl.CONTROLLER_BUTTON_Y}
	applyButtonSwap(on, y)
	if y.Button != sdl.CONTROLLER_BUTTON_Y {
		t.Fatalf("Y must be left alone, got %v", y.Button)
	}

	// With no explicit setting the default follows the platform: handhelds
	// report the face buttons inverted and swap, desktops keep SDL's mapping.
	// Pin the platform so the test means the same thing on every OS (it used to
	// pass only on a Windows desktop and fail on the Linux/TrimUI platform).
	prev := platform
	defer func() { platform = prev }()

	unset := &Config{Variables: Variables{Custom: map[string]interface{}{}}}

	platform = &windowsPlatform{}
	x := &sdl.ControllerButtonEvent{Button: sdl.CONTROLLER_BUTTON_A}
	applyButtonSwap(unset, x)
	if x.Button != sdl.CONTROLLER_BUTTON_A {
		t.Fatalf("desktop with no setting must not flip, got %v", x.Button)
	}

	platform = &trimuiLinuxPlatform{}
	h := &sdl.ControllerButtonEvent{Button: sdl.CONTROLLER_BUTTON_A}
	applyButtonSwap(unset, h)
	if h.Button != sdl.CONTROLLER_BUTTON_B {
		t.Fatalf("handheld with no setting must flip A to B, got %v", h.Button)
	}

	// An explicit "off" wins over the handheld default.
	explicitOff := &Config{Variables: Variables{Custom: map[string]interface{}{"swapABButtons": false}}}
	e := &sdl.ControllerButtonEvent{Button: sdl.CONTROLLER_BUTTON_A}
	applyButtonSwap(explicitOff, e)
	if e.Button != sdl.CONTROLLER_BUTTON_A {
		t.Fatalf("explicit off must not flip even on a handheld, got %v", e.Button)
	}
}

func TestXYButtonsSwappedReadsSetting(t *testing.T) {
	prev := platform
	defer func() { platform = prev }()

	// With nothing stored the default follows the platform, like A/B: the
	// TrimUI firmware mirrors X and Y, a desktop pad does not.
	unset := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	platform = &trimuiLinuxPlatform{}
	if !xyButtonsSwapped(unset) {
		t.Fatal("handheld with no setting must swap X and Y")
	}
	platform = &windowsPlatform{}
	if xyButtonsSwapped(unset) {
		t.Fatal("desktop with no setting must keep SDL's X and Y")
	}

	on := &Config{Variables: Variables{Custom: map[string]interface{}{"swapXYButtons": "true"}}}
	if !xyButtonsSwapped(on) {
		t.Fatal("swapXYButtons=true must enable the swap on a desktop")
	}
	platform = &trimuiLinuxPlatform{}
	off := &Config{Variables: Variables{Custom: map[string]interface{}{"swapXYButtons": false}}}
	if xyButtonsSwapped(off) {
		t.Fatal("swapXYButtons=false must win over the handheld default")
	}
	if xyButtonsSwapped(nil) {
		t.Fatal("nil config must not swap")
	}
}

// The TrimUI pad reports positions the Xbox way under Nintendo labels, so on a
// handheld with nothing stored both pairs flip at the event entry point. The
// X/Y half is its own switch: turning it off leaves the A/B swap alone, and a
// desktop only flips X/Y when asked to.
func TestApplyButtonSwapFlipsXYOnHandheld(t *testing.T) {
	prev := platform
	defer func() { platform = prev }()
	press := func(config *Config, button uint8) uint8 {
		e := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: button}
		applyButtonSwap(config, e)
		return e.Button
	}
	unset := &Config{Variables: Variables{Custom: map[string]interface{}{}}}

	platform = &trimuiLinuxPlatform{}
	// Raw X is the left button, printed Y; raw Y is the top one, printed X.
	if got := press(unset, sdl.CONTROLLER_BUTTON_X); got != sdl.CONTROLLER_BUTTON_Y {
		t.Fatalf("handheld raw X (the button printed Y) = %d, want Y", got)
	}
	if got := press(unset, sdl.CONTROLLER_BUTTON_Y); got != sdl.CONTROLLER_BUTTON_X {
		t.Fatalf("handheld raw Y (the button printed X) = %d, want X", got)
	}
	xyOff := &Config{Variables: Variables{Custom: map[string]interface{}{"swapXYButtons": false}}}
	if got := press(xyOff, sdl.CONTROLLER_BUTTON_X); got != sdl.CONTROLLER_BUTTON_X {
		t.Fatalf("swapXYButtons=false must leave X alone, got %d", got)
	}
	if got := press(xyOff, sdl.CONTROLLER_BUTTON_A); got != sdl.CONTROLLER_BUTTON_B {
		t.Fatalf("turning X/Y off must not touch the A/B swap, got %d", got)
	}

	platform = &windowsPlatform{}
	if got := press(unset, sdl.CONTROLLER_BUTTON_X); got != sdl.CONTROLLER_BUTTON_X {
		t.Fatalf("desktop with no setting must not flip X, got %d", got)
	}
	xyOn := &Config{Variables: Variables{Custom: map[string]interface{}{"swapXYButtons": true}}}
	if got := press(xyOn, sdl.CONTROLLER_BUTTON_Y); got != sdl.CONTROLLER_BUTTON_X {
		t.Fatalf("swapXYButtons=true must flip Y on a desktop too, got %d", got)
	}
	if got := press(xyOn, sdl.CONTROLLER_BUTTON_A); got != sdl.CONTROLLER_BUTTON_A {
		t.Fatalf("the X/Y switch must not flip A on a desktop, got %d", got)
	}
}

// padFaceButton is its own inverse. That is what lets the phone remote and the
// control profiles pre-flip a press with the same call the main loop uses to
// flip it back, and lets a pad poll turn a logical button into the raw one.
func TestPadFaceButtonIsAnInvolution(t *testing.T) {
	both := &Config{Variables: Variables{Custom: map[string]interface{}{
		"swapABButtons": true, "swapXYButtons": true,
	}}}
	for b := uint8(0); b < uint8(sdl.CONTROLLER_BUTTON_MAX); b++ {
		if got := padFaceButton(both, padFaceButton(both, b)); got != b {
			t.Errorf("padFaceButton twice on %d = %d, want %d", b, got, b)
		}
	}
	for _, b := range []uint8{sdl.CONTROLLER_BUTTON_DPAD_UP, sdl.CONTROLLER_BUTTON_START,
		sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_LEFTSHOULDER} {
		if got := padFaceButton(both, b); got != b {
			t.Errorf("non-face button %d mapped to %d", b, got)
		}
	}
	if got := padFaceButton(nil, sdl.CONTROLLER_BUTTON_X); got != sdl.CONTROLLER_BUTTON_X {
		t.Errorf("nil config must not swap, got %d", got)
	}
}
