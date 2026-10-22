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
	on := &Config{Variables: Variables{Custom: map[string]interface{}{"swapABButtons": true}}}

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
