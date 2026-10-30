package main

// backgroundwork.go — the "JukaHub is not on screen" gate for background polls.
//
// A handful of pollers run for the life of the process: the weather ticker, the
// connectivity probe, the audio-output probe and the LED animation. That is
// right while the UI is up, but on a handheld they used to keep working while a
// launched game owned the framebuffer, and on a desktop while the window was
// minimised: a bluetoothctl child, a TCP dial, a geolocated forecast request or
// thirty LED writes a second, all for a screen nobody can see. The disk-pie
// refresh already skips itself when its scene is not up; the rest had no such
// check.
//
// Each of them now waits through waitForPoll and skips its work while
// backgroundPaused reports that something else owns the screen. The tick is
// kept, so coming back costs at most one interval, and the slow ones (weather is
// on a ten-minute timer) are refreshed immediately instead: the transition back
// to the foreground sends a wake to every subscriber.

import (
	"sync"
	"sync/atomic"
	"time"
)

// windowMinimisedFlag is set from the event thread when SDL reports the window
// minimised or hidden, and cleared when it comes back. It is atomic because the
// pollers read it from their own goroutines and SDL may only be called on the
// event thread.
var windowMinimisedFlag atomic.Bool

// windowMinimised reports whether the desktop window is minimised or hidden.
func windowMinimised() bool { return windowMinimisedFlag.Load() }

// setWindowMinimised records the window's visibility and sends a wake when it
// comes back, so the pollers refresh now rather than at their next interval.
func setWindowMinimised(minimised bool) {
	wasPaused := backgroundPaused()
	windowMinimisedFlag.Store(minimised)
	if wasPaused && !backgroundPaused() {
		wakeForegroundWork()
	}
}

// backgroundPaused reports whether JukaHub is behind something else: a game,
// emulator, port or external app owns the framebuffer, or the desktop window is
// minimised. Pollers skip their work while this is true.
func backgroundPaused() bool {
	return gameScreenSuspended() || windowMinimised()
}

// foregroundWake is the subscriber list for "JukaHub is on screen again". Each
// subscriber gets its own buffered channel, so a slow poller cannot delay the
// others and a wake that arrives twice before the poller looks costs nothing.
var foregroundWake struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

// subscribeForegroundWake returns a channel that receives one value each time
// JukaHub returns to the foreground. Callers subscribe once, when their poller
// starts, and only read the channel.
func subscribeForegroundWake() <-chan struct{} {
	ch := make(chan struct{}, 1)
	foregroundWake.mu.Lock()
	if foregroundWake.subs == nil {
		foregroundWake.subs = make(map[chan struct{}]struct{}, 4)
	}
	foregroundWake.subs[ch] = struct{}{}
	foregroundWake.mu.Unlock()
	return ch
}

// wakeForegroundWork tells every poller that the app is on screen again. Sends
// are non-blocking: a subscriber that has not drained its channel yet does not
// need a second notification.
func wakeForegroundWork() {
	foregroundWake.mu.Lock()
	defer foregroundWake.mu.Unlock()
	for ch := range foregroundWake.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// waitForPoll blocks until the ticker fires or the app returns to the
// foreground, then reports whether the caller should do its work. A wake is also
// a reason to report the truth after the transition: returning true refreshes
// the reading immediately, which is what keeps a ten-minute weather timer from
// showing a forecast from before the game started.
//
// It reports false while the app is still behind something else, so a poller can
// simply loop: wait, ask, skip, wait again.
func waitForPoll(tick <-chan time.Time, wake <-chan struct{}) bool {
	select {
	case <-tick:
	case <-wake:
	}
	return !backgroundPaused()
}
