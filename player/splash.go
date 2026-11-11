package main

// splash.go — the startup screen's entry point, and what the boot shares with
// the shutdown teardown.
//
// The boot itself lives in bootos.go (what a power-on and an app launch look
// like, and how the queued startup work is paid for between frames) and
// bootarrive.go (how the first screen comes in). The liquid glass both ends of
// a session draw with is bootliquid.go; the teardown's glass card is
// stageui.go. What stays here is the frame cadence the teardown uses too, the
// input rules a modal startup loop follows, and the cleanup main runs at exit.

import (
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// splashCheckGrace is how far past the boot budget a launch's boot may
	// hold so the checks it reports (a write probe on the card, a tool
	// inventory) finish on screen instead of freezing mid-step. Network work
	// is never waited for.
	splashCheckGrace = 420 * time.Millisecond
	// splashInputGuardMS ignores presses this early: the press that launched
	// JukaHub from the stock launcher (or a held chord) is not a request to
	// skip the boot it caused.
	splashInputGuardMS = 220.0
)

// splashFrameBudget is one frame: the Smart Pro's 30 fps cadence, 60 on a
// desktop. The time a frame does not spend drawing pays for queued boot work.
func splashFrameBudget() time.Duration {
	if IsTrimuiDevice() {
		return 33 * time.Millisecond
	}
	return 16 * time.Millisecond
}

// closeSplashFonts is called after the teardown's last frame. The wordmark
// face comes from the shared font cache, which main closes with the rest;
// what is left to release here is the liquid kit, if a frame still holds it.
func closeSplashFonts() {
	if bootLiquid.renderer != nil && bootLiquid.ready {
		bootLiquidRelease()
	}
}

// queuedControllerDeviceEvent and queuedWindowEvent are events put back on the
// queue (padded like the events in sdlevents.go).
type queuedControllerDeviceEvent struct {
	sdl.ControllerDeviceEvent
	_ [syntheticEventPad]byte
}

type queuedWindowEvent struct {
	sdl.WindowEvent
	_ [syntheticEventPad]byte
}

// splashHeld keeps the events a boot holding the queue must hand on (see
// splashPollInput) until the boot ends: the pads' first four, the window's
// last four, since the latest size is the one that counts. They go back on the
// queue once, by splashRequeueHeld — pushed back after every poll, each one
// would run SDL's event watchers (the renderer's own resize handling among
// them) once per frame for the whole boot.
var splashHeld struct {
	devices [4]sdl.ControllerDeviceEvent
	windows [4]sdl.WindowEvent
	pads    int
	wins    int
}

// splashRequeueHeld puts the held events back on the queue for the main loop
// and forgets them. The boot calls it as it hands the screen back.
func splashRequeueHeld() {
	h := &splashHeld
	for i := 0; i < h.pads; i++ {
		_, _ = syntheticEventPush(&queuedControllerDeviceEvent{ControllerDeviceEvent: h.devices[i]})
	}
	for i := 0; i < h.wins; i++ {
		_, _ = syntheticEventPush(&queuedWindowEvent{WindowEvent: h.windows[i]})
	}
	h.pads, h.wins = 0, 0
}

// splashPollInput drains the event queue. Only a fresh press after the input
// guard skips (a release, a key repeat or the launcher's own press does not);
// a quit request is reported so the caller can hand it to the main loop. Two
// kinds of event are the main loop's to handle and must outlive a boot that
// holds the queue: a pad that connects or lets go (gamepad_hotplug.go — a
// Bluetooth pad often pairs during a power-on), and the window's own events
// (a resize during the boot's preview, which the pointer mapping follows).
// Those are copied into splashHeld, which the boot hands on when it ends.
func splashPollInput(elapsedMS float64) (quit, skip bool) {
	h := &splashHeld
	for ev := sdl.PollEvent(); ev != nil; ev = sdl.PollEvent() {
		switch e := ev.(type) {
		case *sdl.ControllerDeviceEvent:
			// Copied now: the pointer is into a buffer the next poll reuses.
			if h.pads < len(h.devices) {
				h.devices[h.pads] = *e
				h.pads++
			}
		case *sdl.WindowEvent:
			if h.wins == len(h.windows) {
				copy(h.windows[:], h.windows[1:])
				h.wins--
			}
			h.windows[h.wins] = *e
			h.wins++
		case *sdl.QuitEvent:
			quit = true
		case *sdl.KeyboardEvent:
			if e.Type == sdl.KEYDOWN && e.Repeat == 0 {
				skip = true
			}
		case *sdl.MouseButtonEvent:
			if e.Type == sdl.MOUSEBUTTONDOWN {
				skip = true
			}
		case *sdl.ControllerButtonEvent:
			if e.Type == sdl.CONTROLLERBUTTONDOWN {
				skip = true
			}
		case *sdl.TouchFingerEvent:
			if e.Type == sdl.FINGERDOWN {
				skip = true
			}
		}
	}
	if elapsedMS < splashInputGuardMS {
		skip = false
	}
	return quit, skip
}

// ShowSplash plays the boot for this start: the full boot when it is a
// power-on (bootOSPlanStartup decided that before the boot queue was built),
// the quick hello otherwise, running the queued startup work between frames
// either way. A zero allotment (a warm start, no budget left) opens the UI
// immediately.
func ShowSplash(renderer *sdl.Renderer, config *Config, allotted time.Duration) {
	if renderer == nil || allotted <= 0 {
		return
	}
	bootOSPlay(renderer, config, allotted, false)
}
