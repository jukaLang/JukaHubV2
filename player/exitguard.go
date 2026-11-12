package main

// exitguard.go makes quitting bounded, on every path that ends the app.
//
// On the TrimUI the app rarely chooses when to stop: the user closes the
// window, the Exit button asks for it, a self-update restarts it, or the
// firmware's power handling stops the process from outside. Those last paths
// used to end in one of two bad states, both reported as "the app locked":
//
//   - the stop request arrived as a signal, Go's default action killed the
//     process before any cleanup ran, and a running game kept the framebuffer
//     (plus the stopped stock launcher) behind it: a frozen picture in which
//     nothing responds, especially on the Smart Pro where a game is the
//     common case;
//   - a cleanup step waited on something that never finished (an encoder, a
//     server, a child that ignored the request), so the process neither quit
//     nor drew again.
//
// The pieces here: catch the stop signals and turn them into the ordinary
// exit request the main loop already understands; keep a hard deadline so a
// wedged cleanup cannot hold the device; and on that deadline, stop the
// screen-taking children and dump the goroutines (which lock was waiting is
// in errors.txt) instead of leaving a frozen frame.
//
// Only the main goroutine touches SDL, so nothing here draws: the watchdog may
// fire at any moment, including while a renderer call is in flight.

import (
	"fmt"
	"os"
	"os/signal"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// exitChildGrace is how long a game gets to save and quit before it is
	// killed.
	exitChildGrace = 2 * time.Second
)

var (
	// exitShutdownGrace is how long the whole shutdown (main loop, child
	// stop, favourites flush) may take after a stop request. A user who
	// pressed power is waiting for the launcher to come back; the device must
	// not sit on the last frame longer than this. A variable so a test can
	// shorten it instead of waiting six seconds.
	exitShutdownGrace = 6 * time.Second
	// forceExitAction is what the watchdog runs. Swapped by tests only.
	forceExitAction = forceExit
	// exitRequestedBySignal is raised when the process was asked to stop from
	// outside (signal), so the exit path can skip the teardown card: the caller
	// is waiting for the process, not watching it.
	exitRequestedBySignal atomic.Bool
	// exitRequested is set from the render thread (Exit button, config trigger)
	// as well as the signal handler below, and is read by the main loop, so it
	// is atomic. It used to live in goodbye.go; the request outlived the
	// farewell animation that file was named after.
	exitRequested atomic.Bool
	// exitHandled guards the exit request so the loop leaves once instead of
	// re-entering the teardown on every frame it is still running.
	exitHandled bool
	// exitDeadlineOnce arms the watchdog once per process.
	exitDeadlineOnce sync.Once
	// exitForcingOnce makes sure a force-exit runs its work exactly once.
	exitForcingOnce sync.Once
)

// requestAppExit asks the main loop to leave and run the teardown. External
// callers (a stop signal, the firmware's power handling) land here too, which is
// what keeps every exit on the same cleanup path.
//
// The first request also marks the boot record: a start asked to stop is not
// a failed start, even when the stop is forced before the loop is reached
// (bootGuardNoteStop). On its own goroutine: this may be the render thread or
// the signal handler, and the record is a synced write.
func requestAppExit() {
	if !exitRequested.Swap(true) {
		go bootGuardNoteStop()
	}
}

// installExitSignalHandler turns the stop signals into an ordinary exit
// request. Without it Go's default action terminates the process immediately:
// no LED reset, no favourites flush, no teardown, and any running game child
// stays alive over the display.
//
// A second signal means the first one is not being honoured quickly enough, so
// the process stops waiting and leaves now.
func installExitSignalHandler() {
	// os.Interrupt first: it is the one signal every target delivers.
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	go func() {
		for sig := range signals {
			handleExitSignal(sig)
		}
	}()
}

// handleExitSignal is the body of the signal handler, split out so the exit
// request and the deadline can be exercised without delivering a real signal.
func handleExitSignal(sig os.Signal) {
	if exitRequestedBySignal.Swap(true) {
		forceExitAction("second signal: " + sig.String())
		return
	}
	logInfo("exit", "%v received: asking the main loop to quit", sig)
	requestAppExit()
	// The deadline covers both halves of the exit: reaching the end of the
	// loop, and the cleanup that runs after it.
	armExitDeadline(exitShutdownGrace)
}

// armExitDeadline starts the watchdog that force-exits if the shutdown has not
// finished within grace. It is armed at most once, so a late signal cannot
// extend or shorten the budget already given to the exit.
func armExitDeadline(grace time.Duration) {
	exitDeadlineOnce.Do(func() {
		// Capture the action now, not at fire time: the watchdog already owns
		// the decision, and a test that swaps the action must not have a real
		// exit fire into the suite later.
		action := forceExitAction
		time.AfterFunc(grace, func() {
			action(fmt.Sprintf("shutdown exceeded %s", grace))
		})
	})
}

// forceExit stops the process even if cleanup is wedged. Everything it does is
// bounded and allocation-light: kill the children that own the screen, drop
// the LEDs, write down what the goroutines were waiting on, give the machine
// back its CPU governor and speaker level, see a stop request into the boot
// record, and leave. The launcher script sees the exit and brings the stock UI
// back.
func forceExit(reason string) {
	exitForcingOnce.Do(func() {
		logError("exit", "forcing quit: %s", reason)
		// After a stop request this is still that stop: the boot record must say
		// so before the process ends, or the next start counts a failed one
		// (bootguard.go). Started first so it overlaps the work below, and
		// waited for within the restores' grace. Without a request nothing is
		// noted: a forced exit nobody asked for stays a failed start.
		noted := bootGuardNoteStopAsync()
		notedBy := time.Now().Add(exitLastChanceGrace)
		// Inside SDL's own teardown the wait is the GPU driver's, and the
		// stacks say nothing more than this line (sdlteardown.go). Everything
		// before it has run; the Cleanup hooks after it (the TrimUI volume
		// flush, a Recovery restart) still have to.
		inSDLTeardown := sdlTeardownStepActive.Load()
		if inSDLTeardown {
			logWarn("exit", "the main goroutine is still in SDL's teardown (the GPU driver); no goroutine dump")
		} else {
			dumpGoroutines()
		}
		killScreenChildren()
		// The Cleanup hooks hold the restores that no other exit step makes —
		// the brightness the QR card borrowed, the TrimUI volume and
		// brightness written back to the firmware's file, the microphone
		// switches, the idle screen's CPU cap, the display guard's report and
		// pauses — so a forced exit runs them too, wherever the main goroutine
		// is stuck, under the same bound: a hook that is itself the stuck part
		// costs that bound, not the exit (they run once; one already running
		// is waited for).
		runCleanupHooksBounded(sdlTeardownHooksGrace)
		// A Restart or Power off the power card asked for still happens when
		// the teardown overran (devpower.go; exactly once, a no-op otherwise).
		devPowerAfterShutdown()
		_ = SetLEDSync(false)
		exitLastChanceRestore(exitLastChanceGrace)
		select {
		case <-noted:
		default:
			wait := time.NewTimer(time.Until(notedBy))
			select {
			case <-noted:
			case <-wait.C:
				logWarn("exit", "the stop did not reach the boot record within %s", exitLastChanceGrace)
			}
			wait.Stop()
		}
		// 0 for a quit that only took too long; 2 once a crash is in progress
		// (crashreport.go), so launch.sh still tells a crash from a quit.
		os.Exit(processExitCode())
	})
}

// exitLastChanceGrace bounds the restores a forced exit still attempts.
const exitLastChanceGrace = 600 * time.Millisecond

// exitLastChanceRestore puts back the CPU governor and ceiling the machine
// booted with and the mixer level an alarm raised, waiting at most grace: a
// forced exit is already late, and a device left on the performance governor
// (or at 100% volume) after JukaHub is gone is the kind of damage that outlives
// the app. Both are fast sysfs/mixer writes; the bound is for a wedged one.
func exitLastChanceRestore(grace time.Duration) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := restoreBootCPUSettings(); err != nil {
			logWarn("exit", "CPU restore on the forced exit: %v", err)
		}
		alarmVolumeRestoreWait(grace)
	}()
	select {
	case <-done:
	case <-time.After(grace):
		logWarn("exit", "restores did not finish within %s", grace)
	}
}

// dumpGoroutines writes every goroutine stack to the log. When a shutdown
// hangs on the device, this is the only evidence of which lock or channel was
// waiting, and errors.txt is the one file a user can send.
func dumpGoroutines() {
	profile := pprof.Lookup("goroutine")
	if profile == nil {
		return
	}
	logError("exit", "goroutine dump follows: the blocked shutdown is in here")
	if err := profile.WriteTo(os.Stderr, 2); err != nil {
		logWarn("exit", "goroutine dump failed: %v", err)
	}
}
