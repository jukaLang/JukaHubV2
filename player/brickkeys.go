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
// it opened. Those are the defaults: Settings → Controls → F1 key / F2 key
// pick another action for either (frontkeys.go).
//
// The two keys do that on every screen (owner decision, 2026-11-22): no
// JukaHub screen gives L3 / R3 a job of its own any more (stickclicks.go), so
// the browser and the audio player, which used to keep them, no longer take
// them from F1 / F2. A modal panel that owns every press (the game library)
// takes them as it takes any other button. Other hardware is untouched: on a
// Smart Pro S or a Brick Pro L3 / R3 are stick clicks, and the original
// Smart Pro has none.

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
	if !brickHasFunctionKeys() {
		return false
	}
	// The key's chosen action (frontkeys.go): by default F1 the Controller
	// card, F2 Quick Settings.
	return frontKeyPress(e.Button, config)
}
