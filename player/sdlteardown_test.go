package main

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// sdlTeardownTestBail records the deadline's verdict instead of ending the
// test binary.
func sdlTeardownTestBail(t *testing.T) (reasons func() []string, fired <-chan struct{}) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	ch := make(chan struct{})
	var once sync.Once
	prev := sdlTeardownBail
	sdlTeardownBail = func(reason string) {
		mu.Lock()
		got = append(got, reason)
		mu.Unlock()
		once.Do(func() { close(ch) })
	}
	t.Cleanup(func() { sdlTeardownBail = prev })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}, ch
}

// A teardown call that returns in time is just a call: no early exit, and the
// watchdog is told the main goroutine has left SDL again.
func TestSDLTeardownStepInTime(t *testing.T) {
	reasons, _ := sdlTeardownTestBail(t)
	ran := false
	sdlTeardownStep("SDL_DestroyWindow", time.Second, func() {
		if !sdlTeardownStepActive.Load() {
			t.Error("the watchdog was not told an SDL teardown call is running")
		}
		ran = true
	})
	if !ran {
		t.Fatal("the teardown call did not run")
	}
	if sdlTeardownStepActive.Load() {
		t.Fatal("the teardown flag stayed up after the call returned")
	}
	time.Sleep(1100 * time.Millisecond)
	if got := reasons(); len(got) != 0 {
		t.Fatalf("a call that returned in time still bailed out: %v", got)
	}
}

// The Smart Pro case: SDL_DestroyWindow sits in the GPU driver. The process
// must leave after the step's own short deadline, naming the call, instead of
// waiting for the six-second watchdog and its goroutine dump.
func TestSDLTeardownStepWedged(t *testing.T) {
	reasons, fired := sdlTeardownTestBail(t)
	start := time.Now()
	sdlTeardownStep("SDL_DestroyWindow", 100*time.Millisecond, func() {
		select {
		case <-fired:
		case <-time.After(5 * time.Second):
			t.Error("the deadline never fired for a wedged teardown call")
		}
	})
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("bailing out took %v", waited)
	}
	got := reasons()
	if len(got) != 1 || !strings.Contains(got[0], "SDL_DestroyWindow") || !strings.Contains(got[0], "did not return") {
		t.Fatalf("bail reasons %q, want one naming SDL_DestroyWindow", got)
	}
}

// What a wedged teardown leads to: a quit leaves with 0; a teardown reached
// while unwinding from a panic before the loop ended leaves as a crash; and
// during a crash the crash path's own deadline ends the process, after it has
// recorded the panic and stopped a running game.
func TestSDLTeardownExitPlan(t *testing.T) {
	cases := []struct {
		crashing, loopEnded bool
		leave               bool
		code                int
	}{
		{false, true, true, 0},
		{false, false, true, crashExitCode},
		{true, true, false, crashExitCode},
		{true, false, false, crashExitCode},
	}
	for _, c := range cases {
		leave, code := sdlTeardownExitPlan(c.crashing, c.loopEnded)
		if leave != c.leave || (leave && code != c.code) {
			t.Errorf("crashing=%v loopEnded=%v: leave %v code %d, want leave %v code %d",
				c.crashing, c.loopEnded, leave, code, c.leave, c.code)
		}
	}
}

// Nil handles (a renderer that was never created) are not teardown steps.
func TestSDLTeardownNilHandles(t *testing.T) {
	reasons, _ := sdlTeardownTestBail(t)
	sdlDestroyRendererBounded(nil)
	sdlDestroyWindowBounded(nil)
	sdlTeardownStep("nothing", time.Millisecond, nil)
	time.Sleep(20 * time.Millisecond)
	if got := reasons(); len(got) != 0 {
		t.Fatalf("nil teardown steps bailed out: %v", got)
	}
}
