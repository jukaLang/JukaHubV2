package main

// Tests for the Brick's front F1 / F2 keys (brickkeys.go) and the controller
// card rows that describe them. The hardware check is swapped for a stand-in,
// so the Brick behaviour is exercised on any host.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// withBrickKeys makes brickHasFunctionKeys answer for device during one test.
func withBrickKeys(t *testing.T, device DeviceModel) {
	t.Helper()
	prev := brickKeysDevice
	brickKeysDevice = func() DeviceModel { return device }
	t.Cleanup(func() { brickKeysDevice = prev })
}

// withBrickScene puts a one-scene config on screen and restores the globals
// the key handlers read.
func withBrickScene(t *testing.T, scene SceneConfig) *Config {
	t.Helper()
	isolateLegend(t)
	prevScene, prevQS, prevVK := currentSceneIndex, qs, virtualKeyboardActive
	t.Cleanup(func() {
		currentSceneIndex, qs, virtualKeyboardActive = prevScene, prevQS, prevVK
	})
	// A real panel layout: opening Quick Settings indexes its slider rows.
	qsInit()
	virtualKeyboardActive = false
	config := &Config{Scenes: []SceneConfig{scene}}
	appConfig = config
	currentSceneIndex = 0
	return config
}

func padDown(button uint8) *sdl.ControllerButtonEvent {
	return &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: button}
}

// TestBrickF1OpensControllerCard: F1 (reported as L3) opens the controller
// card for the screen on display, and the same key closes it again.
func TestBrickF1OpensControllerCard(t *testing.T) {
	withBrickKeys(t, DeviceTrimuiBrick)
	config := withBrickScene(t, SceneConfig{Name: "Main"})

	if !handleBrickFunctionKey(padDown(sdl.CONTROLLER_BUTTON_LEFTSTICK), config) {
		t.Fatal("F1 on a Brick was not handled")
	}
	if !controlsLegend.open || !controlsLegend.brick {
		t.Fatalf("F1 should open the controller card in its Brick form (open %v, brick %v)", controlsLegend.open, controlsLegend.brick)
	}
	ctrlControlsLegend(padDown(sdl.CONTROLLER_BUTTON_LEFTSTICK))
	if controlsLegend.open {
		t.Fatal("F1 again should close the card")
	}
}

// TestBrickF2TogglesQuickSettings: F2 (reported as R3) opens Quick Settings,
// and inside the panel the same key closes it.
func TestBrickF2TogglesQuickSettings(t *testing.T) {
	withBrickKeys(t, DeviceTrimuiBrick)
	withBrickScene(t, SceneConfig{Name: "Main"})
	// Without a config the panel skips the radio probe, which forks system
	// tools; the toggle itself is what is under test.
	appConfig = nil

	if !handleBrickFunctionKey(padDown(sdl.CONTROLLER_BUTTON_RIGHTSTICK), nil) {
		t.Fatal("F2 on a Brick was not handled")
	}
	if !qs.open {
		t.Fatal("F2 should open Quick Settings")
	}
	ctrlQuickSettings(padDown(sdl.CONTROLLER_BUTTON_RIGHTSTICK), nil)
	if qs.open {
		t.Fatal("F2 inside Quick Settings should close it")
	}
}

// TestBrickKeysLeaveOtherHardwareAndClaimingScreens: on a Smart Pro the stick
// clicks are real stick clicks, and on a Brick the audio player and the web
// browser keep the L3 / R3 meanings they already had.
func TestBrickKeysLeaveOtherHardwareAndClaimingScreens(t *testing.T) {
	withBrickKeys(t, DeviceTrimuiSmartPro)
	config := withBrickScene(t, SceneConfig{Name: "Main"})
	if handleBrickFunctionKey(padDown(sdl.CONTROLLER_BUTTON_LEFTSTICK), config) || controlsLegend.open {
		t.Fatal("L3 on a Smart Pro must not open the controller card")
	}

	withBrickKeys(t, DeviceTrimuiBrick)
	music := withBrickScene(t, SceneConfig{Name: "MusicPlayer"})
	if handleBrickFunctionKey(padDown(sdl.CONTROLLER_BUTTON_LEFTSTICK), music) {
		t.Fatal("the audio player owns L3 (lyrics); F1 must reach it")
	}
	browser := withBrickScene(t, SceneConfig{Name: "Browser", Elements: []Element{{Type: "textbrowser", Source: textBrowserSourceWeb}}})
	if handleBrickFunctionKey(padDown(sdl.CONTROLLER_BUTTON_LEFTSTICK), browser) {
		t.Fatal("the web browser owns L3 (links / scroll); F1 must reach it")
	}

	home := withBrickScene(t, SceneConfig{Name: "Main"})
	virtualKeyboardActive = true
	if handleBrickFunctionKey(padDown(sdl.CONTROLLER_BUTTON_RIGHTSTICK), home) || qs.open {
		t.Fatal("the on-screen keyboard keeps the pad while it is typing")
	}
}

// TestBrickControllerCardRows: on a Brick the card names the front keys and
// stops pointing at the keyboard's F1; elsewhere it is unchanged.
func TestBrickControllerCardRows(t *testing.T) {
	isolateLegend(t)
	controlsLegend.brick = true
	rows := systemLegendRows()
	for button, want := range map[string]string{"F1": "This help", "F2": "Quick Settings", "Menu": "Back", "Start": "Start menu"} {
		if got, ok := hintLabel(rows, button); !ok || got != want {
			t.Errorf("Brick row %q = %q (found %v), want %q", button, got, ok, want)
		}
	}

	controlsLegend.brick = false
	if got, ok := hintLabel(systemLegendRows(), "F1"); !ok || got != "Keyboard shortcuts" {
		t.Errorf("non-Brick F1 row = %q (found %v), want \"Keyboard shortcuts\"", got, ok)
	}
}
