package main

// dirtyframe.go — PERF-3, dirty-frame presentation.
//
// The loop used to draw and present a whole frame on its cadence whether
// anything had changed or not: a full-screen clear, the scene, every overlay
// and a Present 15–30 times a second on a screen that had not moved since the
// last one. On the Smart Pro that is the UI thread and the GPU working through
// an idle menu.
//
// Now something has to ask for a frame. The decision is one function,
// frameShouldPresent, with three ways to say yes:
//
//   - dirty: an input event, a scene switch, a toast raised or expired — a
//     change that happened once and is on screen the next iteration;
//   - animated: something is moving by itself (a transition, the focus ring,
//     a video, a moving backdrop, a progress bar, a toast's slide and fade, a
//     dock or screensaver, the first frames of a start);
//   - the clock's minute, and a 1 Hz safety redraw, so a change nobody
//     announced (a background job finishing, the Wi-Fi signal bar) still
//     reaches the panel within a second.
//
// The skipped iterations still pump input, run the loop's ticks and sleep on
// the same adaptive cadence, so a press is handled exactly when it was before;
// what they no longer do is draw and present. The backlog's Verify is the test
// of that decision (dirtyframe_test.go): idle asks for at most a frame a
// second, and input, a scene change, a toast, a progress tick or a clock
// minute ask for one at once.

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// dirtyFrameSafetyRedraw bounds how stale the screen can be: whatever
	// changed without telling this file is on the panel within a second.
	dirtyFrameSafetyRedraw = time.Second
)

// frameDirtyFlag is raised by anything that changed what the screen should
// show. It is an atomic flag rather than a counter: one bit is consumed per
// frame, so a hundred changes between two frames still cost one redraw.
var frameDirtyFlag atomic.Bool

// frameMarkDirty asks for a frame on the next iteration. Safe from any
// goroutine: input arrives from SDL's thread, the companion server and the
// voice command path.
func frameMarkDirty() { frameDirtyFlag.Store(true) }

// framePresented is what the last presented frame leaves behind for the
// decision: when it was, which clock minute it showed, whether a toast was on
// it (so the toast's disappearance is a change like any other), and how many
// frames have been skipped since. Render thread only, except the counters
// read by tests.
type framePresented struct {
	lastPresent  time.Time
	clockMinute  int
	toastShowing bool
	skipped      uint64
	drawn        uint64
}

var framesPresented framePresented

// frameShouldPresent is the whole decision, free of globals so the backlog's
// "a test of the redraw decision" is a table test. lastClockMinute is the
// minute of the last presented frame; dirty is a change since then; animated
// is something moving that needs every frame.
func frameShouldPresent(lastPresent, now time.Time, dirty, animated bool, clockMinute, lastClockMinute int) bool {
	if lastPresent.IsZero() {
		// Nothing has been presented yet: the first frame always draws.
		return true
	}
	if now.Before(lastPresent) {
		// The wall clock stepped back (an NTP correction, a user changing the
		// time). The second-long safety window would otherwise last as long
		// as the step; draw now and start again from here.
		return true
	}
	if dirty || animated {
		return true
	}
	if clockMinute != lastClockMinute {
		// The header clock changed minute: the panel shows a different time.
		return true
	}
	return now.Sub(lastPresent) >= dirtyFrameSafetyRedraw
}

// toastOnScreen reports whether a toast is up right now. The decision treats
// a toast appearing and a toast going as changes, so the slide-in and the fade
// are drawn and the panel does not keep a toast that has expired.
func toastOnScreen(now uint64) bool {
	currentToastMu.Lock()
	t := currentToast
	currentToastMu.Unlock()
	if strings.TrimSpace(t.Message) == "" || t.Duration == 0 {
		return false
	}
	return now >= t.StartedAt && now-t.StartedAt <= t.Duration
}

// installBarProgressing reports whether the install bar is up: it shows a
// percentage, a speed and a spinner, so it needs every frame while it is.
func installBarProgressing() bool {
	visible, _, _, _, _, _, _, _, _ := installBarSnapshot()
	return visible
}

// dirtyFrameAnimated is the loop's "something moves by itself" answer, plus
// the states whose drawing is not an animation but must not be left stale:
// a start's first frames and the work its deferred start is doing.
func dirtyFrameAnimated(base, starting bool) bool {
	// The "Still frames" setting (stillframes.go, JUKAHUB_STILL_FRAMES=0) is
	// the user's word on all of this: off means every iteration draws the way
	// it always did, including what the power check measures.
	if !stillFramesEnabled() {
		return true
	}
	if base || starting {
		return true
	}
	if playerPhase() == "playing" || frameRateCtrl != nil && frameRateCtrl.BackdropRequested() {
		return true
	}
	if ambientIsActive() || ss.active || alarmFiringActive() || gameAssistOverlayActive() {
		return true
	}
	if toastOnScreen(sdl.GetTicks64()) || saverToastShowing() {
		return true
	}
	if installBarProgressing() || activityBusyActive(time.Now()) {
		return true
	}
	// The volume and brightness key overlays, and the Exit card the power key
	// asks for by a hold this loop's tick just completed (powerkey.go,
	// volumehud.go): each is raised by time passing, not by the press that
	// started it.
	if volumeHUDVisible() || powerKeyCardRequested.Load() {
		return true
	}
	// A frame request raised outside a draw — an input handler, a spinner
	// starting on another goroutine — is still waiting for its frame
	// (requestFrameEvery, adaptive.go).
	if frameWant.frame == frameCount && frameWant.ms > 0 {
		return true
	}
	// A capture reads the frame the renderer holds, so the frame it asked for
	// has to be drawn first: a phone watching the screen, a share viewer, or
	// the Screenshot action (screenshot.go, castcap.go).
	if screenshotRequested || companionScreenPending() || castUIFrameDue(time.Now()) {
		return true
	}
	return false
}

// dirtyFrameDue applies the decision to the loop's state: it consumes the
// dirty flag, notices a toast coming or going, compares the clock's minute and
// records what it decided.
func dirtyFrameDue(now time.Time, baseAnimating, starting bool) bool {
	animated := dirtyFrameAnimated(baseAnimating, starting)
	dirty := frameDirtyFlag.Swap(false)
	if showing := toastOnScreen(sdl.GetTicks64()); showing != framesPresented.toastShowing {
		// A toast appeared or went since the last frame.
		dirty = true
		framesPresented.toastShowing = showing
	}
	minute := now.Hour()*60 + now.Minute()
	if frameShouldPresent(framesPresented.lastPresent, now, dirty, animated, minute, framesPresented.clockMinute) {
		framesPresented.lastPresent = now
		framesPresented.clockMinute = minute
		framesPresented.drawn++
		return true
	}
	framesPresented.skipped++
	return false
}
