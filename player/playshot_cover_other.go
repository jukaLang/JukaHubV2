//go:build !windows

package main

// playshot_cover_other.go — reading the game's screen for a gameplay cover
// (playshot_cover.go) on a Linux handheld: the framebuffer, read the way Hold
// Menu: screenshot reads it (menushot.go). A desktop Linux has no framebuffer
// the game draws to, so it has no gameplay covers.

import "image"

// playShotGrab is the picture on the panel now, upright. Any goroutine; the
// main thread published the turn and the canvas (gameShotSync) before the try.
func playShotGrab() (*image.RGBA, error) {
	if !IsTrimuiDevice() {
		return nil, errPlayShotUnsupported
	}
	fr, err := gameShotGrab(nil)
	if err != nil {
		return nil, err
	}
	return gameShotImage(fr, int(gameShotLive.rotate.Load()),
		int(gameShotLive.canvasW.Load()), int(gameShotLive.canvasH.Load()))
}
