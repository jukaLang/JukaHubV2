package main

// gamepad_hotplug.go — controllers that arrive or leave after boot.
//
// SDL only delivers button events for a controller that has been opened, and
// the boot step opened whatever was attached at launch and nothing afterwards.
// A pad switched on after JukaHub started (a desktop pad on Windows, a
// Bluetooth controller paired later, a USB pad on the dock) therefore did
// nothing at all, and unplugging the open pad left a dead handle behind. SDL
// reports every controller present at init as "added" once as well; that event
// finds the pad already open and changes nothing.

import (
	"log"

	"github.com/veandco/go-sdl2/sdl"
)

// handleControllerDevice opens a newly attached controller when none is
// usable, and lets go of the open one when it is unplugged — falling back to
// any other controller that is still attached. Main thread only.
func handleControllerDevice(e *sdl.ControllerDeviceEvent) {
	if e == nil {
		return
	}
	switch e.Type {
	case sdl.CONTROLLERDEVICEADDED:
		if gameController != nil && gameController.Attached() {
			return
		}
		// For an "added" event Which is the device index.
		c := sdl.GameControllerOpen(int(e.Which))
		if c == nil {
			return
		}
		replaceGameController(c)
		log.Printf("[GAMEPAD] Opened controller: %s", c.Name())
	case sdl.CONTROLLERDEVICEREMOVED:
		// For a "removed" event Which is the instance id.
		if gameController == nil || gameController.Joystick() == nil ||
			gameController.Joystick().InstanceID() != e.Which {
			return
		}
		log.Printf("[GAMEPAD] Controller removed: %s", gameController.Name())
		replaceGameController(nil)
		for i := 0; i < sdl.NumJoysticks(); i++ {
			if !sdl.IsGameController(i) {
				continue
			}
			if c := sdl.GameControllerOpen(i); c != nil {
				replaceGameController(c)
				log.Printf("[GAMEPAD] Switched to controller: %s", c.Name())
				break
			}
		}
	}
}

// replaceGameController swaps the open controller. The rumble backend is told
// first: a pulse through a closed SDL controller is a use-after-free in C.
func replaceGameController(next *sdl.GameController) {
	prev := gameController
	if prev == next {
		return
	}
	if prev != nil {
		hapticsForgetPad(prev)
		prev.Close()
	}
	gameController = next
	if next != nil {
		hapticsPadAttached()
	}
}
