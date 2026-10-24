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
	"log"
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
	// outside (signal), so the exit path can skip the farewell animation: the
	// caller is waiting for the process, not watching it.
	exitRequestedBySignal atomic.Bool
	// exitDeadlineOnce arms the watchdog once per process.
	exitDeadlineOnce sync.Once
	// exitForcingOnce makes sure a force-exit runs its work exactly once.
	exitForcingOnce sync.Once
)

// installExitSignalHandler turns the stop signals into an ordinary exit
// request. Without it Go's default action terminates the process immediately:
// no LED reset, no favourites flush, no farewell, and any running game child
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
	log.Printf("[exit] %v received: asking the main loop to quit", sig)
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
// the LEDs, write down what the goroutines were waiting on, and leave. The
// launcher script sees the exit and brings the stock UI back.
func forceExit(reason string) {
	exitForcingOnce.Do(func() {
		log.Printf("[exit] forcing quit: %s", reason)
		dumpGoroutines()
		killScreenChildren()
		_ = SetLEDSync(false)
		os.Exit(0)
	})
}

// dumpGoroutines writes every goroutine stack to the log. When a shutdown
// hangs on the device, this is the only evidence of which lock or channel was
// waiting, and errors.txt is the one file a user can send.
func dumpGoroutines() {
	profile := pprof.Lookup("goroutine")
	if profile == nil {
		return
	}
	log.Print("[exit] goroutine dump follows: the blocked shutdown is in here")
	if err := profile.WriteTo(os.Stderr, 2); err != nil {
		log.Printf("[exit] goroutine dump failed: %v", err)
	}
}
