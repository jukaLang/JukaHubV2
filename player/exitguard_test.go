package main

// exitguard_test.go covers the part of the handheld's "the app locked when I
// pressed power" report that can be pinned in a unit test: an outside stop
// request has to become the ordinary exit request, and the shutdown has to be
// bounded even when nothing cooperates. The signal plumbing itself (one line of
// signal.Notify) is exercised by every real run; what used to be missing is
// everything downstream of it.

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestExitSignalRequestsQuitAndArmsTheDeadline is the contract that keeps a
// power-key stop from killing the process outright: the request lands in the
// same flag the Exit button sets, and a deadline is armed so the app cannot
// stay up (and on screen) if the shutdown wedges.
func TestExitSignalRequestsQuitAndArmsTheDeadline(t *testing.T) {
	prevRequested := exitRequested.Load()
	prevBySignal := exitRequestedBySignal.Load()
	prevGrace := exitShutdownGrace
	prevAction := forceExitAction
	t.Cleanup(func() {
		exitRequested.Store(prevRequested)
		exitRequestedBySignal.Store(prevBySignal)
		exitShutdownGrace = prevGrace
		forceExitAction = prevAction
	})

	exitRequested.Store(false)
	exitRequestedBySignal.Store(false)
	exitShutdownGrace = 60 * time.Millisecond

	// The watchdog must not really exit the test binary: record what it would
	// have done and when.
	var mu sync.Mutex
	var reasons []string
	fired := make(chan struct{}, 4)
	forceExitAction = func(reason string) {
		mu.Lock()
		reasons = append(reasons, reason)
		mu.Unlock()
		fired <- struct{}{}
	}

	handleExitSignal(os.Interrupt)

	if !exitRequested.Load() {
		t.Fatal("a stop signal must arm the exit request the main loop watches")
	}
	if !exitRequestedBySignal.Load() {
		t.Fatal("the exit must be marked as signal-initiated so the farewell is skipped")
	}

	// The deadline is armed once: the post-loop call with a much longer grace
	// (the real shutdown path does exactly that) must not extend it.
	armExitDeadline(30 * time.Second)

	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("the forced-exit deadline never fired; a wedged shutdown would hang forever")
	}
	mu.Lock()
	got := reasons[0]
	mu.Unlock()
	if !strings.Contains(got, "shutdown exceeded") {
		t.Fatalf("deadline reason = %q, want a shutdown-exceeded message", got)
	}
	// A second, later deadline would mean the grace period can be pushed out
	// by the code being guarded against.
	select {
	case <-fired:
		t.Fatal("the forced-exit deadline fired more than once")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestSecondExitSignalForcesImmediately: if the first request is not being
// honoured, the user pressing again (or the firmware sending a second signal)
// must not wait out the deadline.
func TestSecondExitSignalForcesImmediately(t *testing.T) {
	prevBySignal := exitRequestedBySignal.Load()
	prevAction := forceExitAction
	t.Cleanup(func() {
		exitRequestedBySignal.Store(prevBySignal)
		forceExitAction = prevAction
	})

	exitRequestedBySignal.Store(false)
	var mu sync.Mutex
	forced := 0
	forceExitAction = func(string) {
		mu.Lock()
		forced++
		mu.Unlock()
	}

	handleExitSignal(os.Interrupt)
	handleExitSignal(os.Interrupt)

	mu.Lock()
	defer mu.Unlock()
	if forced != 1 {
		t.Fatalf("second signal forced %d exits, want exactly 1", forced)
	}
}
