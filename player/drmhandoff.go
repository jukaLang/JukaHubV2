package main

// drmhandoff.go — handing a DRM/KMS display to a game and taking it back.
//
// On the A133 handhelds (Smart Pro, Brick, Brick Pro) SDL draws through the
// framebuffer, which JukaHub and a game can share: JukaHub simply stops
// presenting while the game runs (gamesuspend.go). The Smart Pro S (A523) can
// run SDL on KMSDRM instead, and a DRM device has one master: while JukaHub's
// window holds it, a game's own SDL cannot set a mode or flip a page, and the
// game starts black. When the game quits, the kernel restores its console
// configuration ("the kernel blanks the screen when the display is
// released"), so JukaHub's next page flip lands on a CRTC it no longer set up.
//
// So, only when SDL's video driver is KMSDRM: before a screen-taking child
// starts, JukaHub drops master on its own DRM descriptors; when the last one
// is gone it takes master back and asks SDL to set the mode again on the next
// frame (toggling fullscreen makes SDL's KMSDRM backend rebuild its surfaces
// with a pending modeset). Every other video driver is untouched.
// JUKAHUB_DRM_HANDOFF=0 turns it off for a test on the device.

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/veandco/go-sdl2/sdl"
)

var drmHandoff struct {
	enabled   atomic.Bool // SDL runs on KMSDRM and the handoff is not switched off
	remodeset atomic.Bool // master is back: the render thread re-sets the mode

	mu       sync.Mutex
	released []int // descriptors whose master was dropped, in order
}

// drmHandoffInit decides once, after SDL's video init, whether the handoff
// applies. Call it on the render thread (SDL's video driver query).
func drmHandoffInit() {
	drv, err := sdl.GetCurrentVideoDriver()
	if err != nil || !strings.EqualFold(strings.TrimSpace(drv), "KMSDRM") {
		return
	}
	if v := strings.TrimSpace(os.Getenv("JUKAHUB_DRM_HANDOFF")); v == "0" || strings.EqualFold(v, "off") {
		logInfo("handover", "DRM display: handoff switched off (JUKAHUB_DRM_HANDOFF=%s)", v)
		return
	}
	drmHandoff.enabled.Store(true)
	logInfo("handover", "DRM display (SDL KMSDRM): games get DRM master while they run")
}

// drmHandoffRelease drops master before a game takes the screen. Safe to call
// more than once per launch: only the first call does anything until master
// is taken back.
func drmHandoffRelease() {
	if !drmHandoff.enabled.Load() {
		return
	}
	drmHandoff.mu.Lock()
	defer drmHandoff.mu.Unlock()
	if drmHandoff.released != nil {
		return
	}
	fds := drmDropMaster()
	drmHandoff.released = fds
	if drmHandoff.released == nil {
		drmHandoff.released = []int{}
	}
	logInfo("handover", "DRM master dropped on %d descriptor(s) for the game", len(fds))
}

// drmHandoffReacquire takes master back once no screen-taking child is left,
// and leaves the modeset to the render thread (drmHandoffRemodeset).
func drmHandoffReacquire() {
	if !drmHandoff.enabled.Load() {
		return
	}
	drmHandoff.mu.Lock()
	defer drmHandoff.mu.Unlock()
	if drmHandoff.released == nil {
		return
	}
	fds := drmHandoff.released
	drmHandoff.released = nil
	if len(fds) == 0 {
		return
	}
	if err := drmSetMaster(fds); err != nil {
		logWarn("handover", "DRM master not taken back: %v", err)
	}
	drmHandoff.remodeset.Store(true)
}

// drmHandoffRemodeset runs on the render thread before the first frame after
// a game: SDL's KMSDRM backend rebuilds its surfaces and sets the mode again
// when the window's fullscreen state changes, so the state is flipped and put
// back.
func drmHandoffRemodeset(window *sdl.Window) {
	if window == nil || !drmHandoff.remodeset.CompareAndSwap(true, false) {
		return
	}
	// Flip to the other state and back to the window's own one.
	flags := window.GetFlags() & (sdl.WINDOW_FULLSCREEN | sdl.WINDOW_FULLSCREEN_DESKTOP)
	var other uint32
	if flags == 0 {
		other = sdl.WINDOW_FULLSCREEN_DESKTOP
	}
	if err := window.SetFullscreen(other); err != nil {
		logWarn("handover", "DRM remodeset (flip): %v", err)
	}
	if err := window.SetFullscreen(flags); err != nil {
		logWarn("handover", "DRM remodeset (restore): %v", err)
		return
	}
	logInfo("handover", "DRM display taken back after the game")
}
