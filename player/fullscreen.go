package main

// fullscreen.go — the one path for fullscreen changes.
//
// Two modes were in use: exclusive `SetFullscreen(1)`, which changes the
// display mode and is slow to come back from, and desktop fullscreen
// (`WINDOW_FULLSCREEN_DESKTOP`), which lets SDL letterbox the logical canvas
// over the current mode. Settings triggers, the save button and F11 each picked
// one, so the same toggle behaved differently depending on where it was
// pressed — and a Windows start ignored the saved choice entirely. Everything
// now goes through applyFullscreen.

import "github.com/veandco/go-sdl2/sdl"

// applyFullscreen switches the window between desktop fullscreen and windowed,
// keeping config.Variables.Fullscreen in step with what is on screen so the next
// start (and the settings row) reads the same value.
func applyFullscreen(config *Config, on bool) {
	if config != nil {
		config.Variables.Fullscreen = on
	}
	if mainWindow == nil {
		return
	}
	if on {
		mainWindow.SetFullscreen(sdl.WINDOW_FULLSCREEN_DESKTOP)
		return
	}
	mainWindow.SetFullscreen(0)
}
