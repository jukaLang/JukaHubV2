package main

// sdlteardown.go bounds the SDL half of quitting.
//
// The last thing the process does is hand back the renderer, the window and
// SDL itself (deferred in main). On the TrimUI those calls go into the vendor's
// GPU driver, and a Smart Pro log caught SDL_DestroyWindow still running when
// the six-second exit watchdog fired: the user looked at a frozen last frame
// for six seconds, and errors.txt got a full goroutine dump for what was an
// ordinary quit. By then everything that matters is done — settings and
// favourites saved, CPU and speaker restored, the boot record written — and
// the kernel frees the display and the GPU context when the process ends.
//
// So each SDL teardown call gets its own short deadline. When it passes, the
// process runs what the skipped part of main would still have run — the
// Cleanup hooks (the TrimUI volume flush, the restart Recovery asked for) — and
// leaves, with one warning line and the exit code a clean quit returns. The
// watchdog skips its goroutine dump while the main goroutine is known to be
// inside one of these calls (exitguard.go). A crash keeps its own path: its
// deadline (crashBegin) records the panic, stops a running game and exits 2.

import (
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// sdlTeardownGrace is how long one SDL teardown call may take. Healthy
	// ones return in milliseconds.
	sdlTeardownGrace = 1500 * time.Millisecond
	// sdlTeardownHooksGrace bounds the Cleanup hooks a bail-out still runs.
	sdlTeardownHooksGrace = time.Second
)

var (
	// sdlTeardownStepActive is up while an SDL teardown call runs, so the
	// exit watchdog knows where the main goroutine is.
	sdlTeardownStepActive atomic.Bool
	// sdlMainLoopEnded is raised when the main loop has ended normally. A
	// teardown that runs without it is main unwinding from a panic before the
	// loop (the crash handler has not seen it yet), not a quit.
	sdlMainLoopEnded atomic.Bool
	// sdlTeardownBail is what a step's deadline runs. Tests swap it.
	sdlTeardownBail = sdlTeardownLeave
	// cleanupHooksOnce runs the Cleanup hooks exactly once, on whichever path
	// gets there first: main's deferred teardown or a bail-out.
	cleanupHooksOnce sync.Once
)

// sdlTeardownStep runs one SDL teardown call under its own deadline.
func sdlTeardownStep(name string, grace time.Duration, step func()) {
	if step == nil {
		return
	}
	// Captured now: a deadline that fires late must use the action of its
	// own step, not one a test swapped in afterwards.
	bail := sdlTeardownBail
	sdlTeardownStepActive.Store(true)
	defer sdlTeardownStepActive.Store(false)
	timer := time.AfterFunc(grace, func() {
		bail(name + " did not return within " + grace.String())
	})
	defer timer.Stop()
	step()
}

// sdlTeardownExitPlan is what a wedged teardown call leads to: whether to
// leave now, and with which code. During a crash the crash path's own
// deadline ends the process, after it has recorded the panic.
func sdlTeardownExitPlan(crashing, loopEnded bool) (leave bool, code int) {
	switch {
	case crashing:
		return false, crashExitCode
	case !loopEnded:
		// Unwinding before the loop ended is a panic on its way to the
		// top-level handler; launch.sh must not read it as a quit.
		return true, crashExitCode
	default:
		return true, 0
	}
}

// sdlTeardownLeave ends the process after a wedged SDL teardown call. It
// shares the watchdog's once, so a forced exit already under way is not
// raced, and none starts after this one.
func sdlTeardownLeave(reason string) {
	leave, code := sdlTeardownExitPlan(crashInProgress.Load(), sdlMainLoopEnded.Load())
	if !leave {
		logWarn("exit", "%s during a crash; the crash path ends the process", reason)
		return
	}
	exitForcingOnce.Do(func() {
		logWarn("exit", "%s; leaving without it (the system frees the display when the process ends)", reason)
		killScreenChildren()
		runCleanupHooksBounded(sdlTeardownHooksGrace)
		os.Exit(code)
	})
}

// runCleanupHooks runs the Cleanup hooks once per process. main defers it, so
// they run after the window is gone and before SDL itself is shut down.
func runCleanupHooks() {
	cleanupHooksOnce.Do(func() {
		for _, fn := range Cleanup {
			fn()
		}
	})
}

// runCleanupHooksBounded is runCleanupHooks for a forced exit: it waits at
// most grace, because the process is leaving either way.
func runCleanupHooksBounded(grace time.Duration) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		runCleanupHooks()
	}()
	select {
	case <-done:
	case <-time.After(grace):
		logWarn("exit", "the cleanup hooks did not finish within %s", grace)
	}
}

// sdlDestroyRendererBounded is the deferred renderer teardown.
func sdlDestroyRendererBounded(r *sdl.Renderer) {
	if r == nil {
		return
	}
	sdlTeardownStep("SDL_DestroyRenderer", sdlTeardownGrace, func() { _ = r.Destroy() })
}

// sdlDestroyWindowBounded is the deferred window teardown.
func sdlDestroyWindowBounded(w *sdl.Window) {
	if w == nil {
		return
	}
	sdlTeardownStep("SDL_DestroyWindow", sdlTeardownGrace, func() { _ = w.Destroy() })
}

// sdlQuitBounded is the deferred SDL_Quit.
func sdlQuitBounded() {
	sdlTeardownStep("SDL_Quit", sdlTeardownGrace, sdl.Quit)
}
