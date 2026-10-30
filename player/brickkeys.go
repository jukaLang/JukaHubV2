package main

// brickkeys.go — the TrimUI Brick's front F1 / F2 keys.
//
// The Brick (and the Hammer, the same TG3040 board) has two small keys under
// the screen, F1 and F2. Its firmware reports them as the stick clicks, L3 and
// R3: the Brick has no sticks. Outside three screens nothing in JukaHub
// listened for those, so on a Brick the two keys did nothing at all, and the
// controller card even said "F1 Keyboard shortcuts", which only a keyboard can
// reach. They now mirror their keyboard namesakes as far as a handheld can:
// F1 opens the Controller card for the screen on display (what every button
// does here), F2 opens Quick Settings. Pressing the same key again closes what
// it opened.
//
// Screens that already give L3 / R3 a meaning keep it: the web browser (L3
// switches between scrolling and walking links) and the audio player (lyrics,
// sleep timer). The game library's R3 is handled by its own modal before this
// runs. Other hardware is untouched: on a Smart Pro L3 / R3 are real stick
// clicks.

import (
	"github.com/veandco/go-sdl2/sdl"
)

// brickKeysDevice identifies the hardware for the F1 / F2 bindings. It is a
// variable so tests can stand in for a Brick on any host.
var brickKeysDevice = DetectDevice

// brickHasFunctionKeys reports whether this is a Brick-family handheld with the
// front F1 / F2 keys.
func brickHasFunctionKeys() bool {
	switch brickKeysDevice() {
	case DeviceTrimuiBrick, DeviceTrimuiHammer:
		return true
	}
	return false
}

// sceneClaimsStickClicks reports whether the screen on display already uses
// L3 / R3, in which case F1 / F2 must reach it unchanged.
func sceneClaimsStickClicks(config *Config) bool {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	scene := config.Scenes[currentSceneIndex]
	if scene.Name == "MusicPlayer" {
		return true
	}
	for _, el := range scene.Elements {
		if el.Type == "textbrowser" && textBrowserElementSource(el) == textBrowserSourceWeb {
			return true
		}
	}
	return false
}

// handleBrickFunctionKey runs F1 / F2 on a Brick and reports whether the press
// was used. Every other press, and every press on other hardware, is left for
// the scene.
func handleBrickFunctionKey(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || virtualKeyboardActive {
		return false
	}
	if e.Button != sdl.CONTROLLER_BUTTON_LEFTSTICK && e.Button != sdl.CONTROLLER_BUTTON_RIGHTSTICK {
		return false
	}
	if !brickHasFunctionKeys() || sceneClaimsStickClicks(config) {
		return false
	}
	if e.Button == sdl.CONTROLLER_BUTTON_LEFTSTICK {
		openControlsLegend()
		if !controlsLegend.open {
			return false
		}
		PlayActivateSound()
		return true
	}
	qsToggleOpen()
	PlayActivateSound()
	return true
}
