package main

// backgroundwork_test.go — the gate the background pollers wait through.
//
// The point of the gate is that a poller does no work while a game owns the
// screen or the window is minimised, and refreshes immediately when that ends.
// These tests drive the state and the waiter directly: no network, no SDL, no
// ticker of their own.

import (
	"testing"
	"time"
)

// drainScreenChildren leaves the process-wide counter at zero even when a test
// fails halfway, so one case cannot leak "a game is running" into the next.
func drainScreenChildren(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		for i := 0; i < 4; i++ {
			if !gameScreenSuspended() {
				break
			}
			noteGameChildExited(nil)
		}
		windowMinimisedFlag.Store(false)
	})
}

func TestBackgroundPausedWhileAGameOwnsTheScreen(t *testing.T) {
	drainScreenChildren(t)

	if backgroundPaused() {
		t.Fatal("nothing is running, so the app is on screen")
	}
	noteGameChildStarted(nil)
	if !backgroundPaused() {
		t.Fatal("a launched game owns the framebuffer, so polling must park")
	}
	noteGameChildExited(nil)
	if backgroundPaused() {
		t.Fatal("the game exited, so polling must continue")
	}
}

func TestBackgroundPausedWhileTheWindowIsMinimised(t *testing.T) {
	drainScreenChildren(t)

	setWindowMinimised(true)
	if !backgroundPaused() {
		t.Fatal("a minimised window has nothing to poll for")
	}
	setWindowMinimised(false)
	if backgroundPaused() {
		t.Fatal("a restored window is on screen again")
	}
}

// The wake is a broadcast with a one-slot buffer per subscriber: a slow poller
// cannot hold up the others, and a second wake before the first is read is not
// an error.
func TestForegroundWakeReachesEverySubscriber(t *testing.T) {
	drainScreenChildren(t)

	a := subscribeForegroundWake()
	b := subscribeForegroundWake()
	wakeForegroundWork()
	wakeForegroundWork() // must not block on the full buffers

	for name, ch := range map[string]<-chan struct{}{"first": a, "second": b} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("%s subscriber never heard the wake", name)
		}
	}
}

// Returning from a game is a foreground transition like restoring the window,
// so the same wake goes out.
func TestGameExitWakesThePollers(t *testing.T) {
	drainScreenChildren(t)

	wake := subscribeForegroundWake()
	noteGameChildStarted(nil)
	noteGameChildExited(nil)

	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("a game exit did not wake the pollers")
	}
}

// waitForPoll reports false for a tick that arrives while a game is running and
// true for the wake that follows it, which is how the weather ticker refreshes
// at once instead of waiting out its ten-minute interval.
func TestWaitForPollSkipsWhilePausedAndRunsOnReturn(t *testing.T) {
	drainScreenChildren(t)

	tick := make(chan time.Time, 1)
	wake := make(chan struct{}, 1)

	noteGameChildStarted(nil)
	tick <- time.Now()
	if waitForPoll(tick, wake) {
		t.Fatal("a tick during a game session must be skipped")
	}

	noteGameChildExited(nil)
	wake <- struct{}{}
	if !waitForPoll(tick, wake) {
		t.Fatal("the wake after the game must let the poller run")
	}

	tick <- time.Now()
	if !waitForPoll(tick, wake) {
		t.Fatal("an ordinary tick on screen must let the poller run")
	}
}
