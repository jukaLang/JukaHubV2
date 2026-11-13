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
	"github.com/veandco/go-sdl2/sdl"
)

// extraGameControllers holds the pads opened besides gameController, by
// instance id, so their buttons reach the UI too. Main thread only.
var extraGameControllers map[sdl.JoystickID]*sdl.GameController

// handleControllerDevice opens a newly attached controller when none is
// usable, and lets go of the open one when it is unplugged — falling back to
// any other controller that is still attached. Main thread only.
//
// Local play hears about every change once the main pad is settled
// (hotseat.go): it opens a newly attached pad while its lobby is up, lets go
// of one that left, and announces when two or more are connected.
func handleControllerDevice(e *sdl.ControllerDeviceEvent) {
	if e == nil {
		return
	}
	defer hotseatDeviceChanged(e)
	switch e.Type {
	case sdl.CONTROLLERDEVICEADDED:
		trophyEventDistinct(tkPads, sdl.GameControllerNameForIndex(int(e.Which)))
		if gameController != nil && gameController.Attached() {
			// A second pad (a Bluetooth controller next to the handheld's own
			// buttons) is kept open too: SDL sends no events for a controller
			// nobody opened, so it did nothing at all. Handhelds only: a
			// desktop often sees one pad twice (a DS4 and the Xbox pad a
			// remapper makes of it), and every press would count twice.
			if !IsTrimuiDevice() {
				return
			}
			id := sdl.JoystickGetDeviceInstanceID(int(e.Which))
			if id < 0 || (gameController.Joystick() != nil && gameController.Joystick().InstanceID() == id) {
				return
			}
			if _, open := extraGameControllers[id]; open {
				return
			}
			if c := sdl.GameControllerOpen(int(e.Which)); c != nil {
				if extraGameControllers == nil {
					extraGameControllers = make(map[sdl.JoystickID]*sdl.GameController, 2)
				}
				extraGameControllers[id] = c
				logDebug("gamepad", "Opened another controller: %s", c.Name())
			}
			return
		}
		// For an "added" event Which is the device index.
		c := sdl.GameControllerOpen(int(e.Which))
		if c == nil {
			return
		}
		replaceGameController(c)
		logDebug("gamepad", "Opened controller: %s", c.Name())
	case sdl.CONTROLLERDEVICEREMOVED:
		// For a "removed" event Which is the instance id.
		if c, open := extraGameControllers[e.Which]; open {
			delete(extraGameControllers, e.Which)
			c.Close()
		}
		if gameController == nil || gameController.Joystick() == nil ||
			gameController.Joystick().InstanceID() != e.Which {
			return
		}
		logDebug("gamepad", "Controller removed: %s", gameController.Name())
		replaceGameController(nil)
		for i := 0; i < sdl.NumJoysticks(); i++ {
			if !sdl.IsGameController(i) {
				continue
			}
			if c := sdl.GameControllerOpen(i); c != nil {
				replaceGameController(c)
				logDebug("gamepad", "Switched to controller: %s", c.Name())
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
