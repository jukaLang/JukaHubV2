package main

import (
	"fmt"
	"testing"
	"time"
)

// TestCompanionThrottleIsPerAddress covers SEC-28: a paired phone polling on
// its own address used to clear a guesser's failures with every request. The
// budget is now per remote address, and a success clears only its own.
func TestCompanionThrottleIsPerAddress(t *testing.T) {
	s := newCompanionServer("123456", "", func(companionEvent) {})
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	const guesser, paired = "10.0.0.2", "10.0.0.9"

	for i := 0; i < companionFailLimit; i++ {
		s.noteFailure(guesser, now)
	}
	if !s.throttled(guesser, now.Add(time.Second)) {
		t.Fatal("the guessing address is not throttled after spending its budget")
	}
	if s.throttled(paired, now.Add(time.Second)) {
		t.Fatal("the paired address is throttled by another address's failures")
	}

	// The paired phone keeps polling and the guesser keeps trying.
	s.noteSuccess(paired)
	s.noteFailure(guesser, now.Add(2*time.Second))
	if !s.throttled(guesser, now.Add(2*time.Second)) {
		t.Fatal("a success from another address cleared the guesser's budget")
	}

	s.noteSuccess(guesser)
	if s.throttled(guesser, now.Add(3*time.Second)) {
		t.Fatal("a success on the guessing address did not clear its own budget")
	}
}

// TestCompanionThrottleCapsAddresses keeps a network of pinned devices from
// growing the per-address map without bound.
func TestCompanionThrottleCapsAddresses(t *testing.T) {
	s := newCompanionServer("123456", "", func(companionEvent) {})
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	for i := 0; i < companionTrackedAddrs+8; i++ {
		s.noteFailure(fmt.Sprintf("10.0.1.%d", i), now.Add(time.Duration(i)*time.Second))
	}
	s.mu.Lock()
	tracked := len(s.throttles)
	s.mu.Unlock()
	if tracked > companionTrackedAddrs {
		t.Fatalf("tracked %d addresses, cap is %d", tracked, companionTrackedAddrs)
	}
}
