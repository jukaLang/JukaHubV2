package main

// gamelaunch_hooks.go — places in the library's launch path
// (launchLibraryGame) where a feature can take part without editing it.
//
// launchLibraryGame is the one way a game starts from the library, the game
// page, the recents, Files and Get Homebrew, and many features already have a
// line in it. Several more need one — the Game Ready check before anything
// starts, the DOS and ScummVM shelves' launch settings, Game Lab's
// measurements — and one shared file edited by many hands at once is where
// launches break. So the launch path calls four lists, and a feature adds
// itself to one from init():
//
//   - gameLaunchRegisterGate: asked before the launcher is even checked
//     (launcher may be "" when the console has no emulator). A gate that
//     returns held=true has taken the launch over — it shows its own card and
//     starts the game again later (launchLibraryGame again) if the player
//     wants; the launch stops there without an error. A gate must let its own
//     restart through, and must be quick: it runs on the main thread.
//   - gameLaunchRegisterPrepare: one-launch settings, run after the built-in
//     ones (cheats, controls) and before the game's options are read.
//   - gameLaunchRegisterStarted: the game's process has started.
//   - gameLaunchRegisterExited: the game's process group is gone; runs on the
//     reaper goroutine, so a hook posts UI work with tbxPost and keeps its own
//     state guarded.

import (
	"os/exec"
	"sync"
	"time"
)

type (
	gameLaunchGateFunc    func(game libraryGame, launcher string) (held bool, err error)
	gameLaunchPrepareFunc func(launcher string, game libraryGame)
	gameLaunchStartedFunc func(game libraryGame, launcher string, cmd *exec.Cmd)
	gameLaunchExitedFunc  func(game libraryGame, launcher string, ranFor time.Duration, waitErr error)
)

var gameLaunchHooks struct {
	mu       sync.RWMutex
	gates    []gameLaunchGateFunc
	prepares []gameLaunchPrepareFunc
	started  []gameLaunchStartedFunc
	exited   []gameLaunchExitedFunc
}

// gameLaunchHeld is the game whose last launch a gate held without starting
// anything, and gameLaunchStarts counts the games started; main thread only.
// A held launch returns nil like the launch path's own deferrals (a save
// download, a C library install), so a caller that celebrates a start asks
// gameLaunchWasHeld first.
var (
	gameLaunchHeld   string
	gameLaunchStarts int
)

// gameLaunchWasHeld reports whether the last launch of path was held by a
// gate (its card is up; the game has not started).
func gameLaunchWasHeld(path string) bool {
	return path != "" && gameLaunchHeld == path
}

func gameLaunchRegisterGate(fn gameLaunchGateFunc) {
	if fn == nil {
		return
	}
	gameLaunchHooks.mu.Lock()
	gameLaunchHooks.gates = append(gameLaunchHooks.gates, fn)
	gameLaunchHooks.mu.Unlock()
}

func gameLaunchRegisterPrepare(fn gameLaunchPrepareFunc) {
	if fn == nil {
		return
	}
	gameLaunchHooks.mu.Lock()
	gameLaunchHooks.prepares = append(gameLaunchHooks.prepares, fn)
	gameLaunchHooks.mu.Unlock()
}

func gameLaunchRegisterStarted(fn gameLaunchStartedFunc) {
	if fn == nil {
		return
	}
	gameLaunchHooks.mu.Lock()
	gameLaunchHooks.started = append(gameLaunchHooks.started, fn)
	gameLaunchHooks.mu.Unlock()
}

func gameLaunchRegisterExited(fn gameLaunchExitedFunc) {
	if fn == nil {
		return
	}
	gameLaunchHooks.mu.Lock()
	gameLaunchHooks.exited = append(gameLaunchHooks.exited, fn)
	gameLaunchHooks.mu.Unlock()
}

// gameLaunchRunGates asks every gate in turn; the first that holds the launch
// (or fails it) ends the round.
func gameLaunchRunGates(game libraryGame, launcher string) (held bool, err error) {
	gameLaunchHooks.mu.RLock()
	gates := gameLaunchHooks.gates
	gameLaunchHooks.mu.RUnlock()
	gameLaunchHeld = ""
	before := gameLaunchStarts
	for _, gate := range gates {
		if held, err := gate(game, launcher); held || err != nil {
			// A gate that started the game itself (a DOS game sent to its
			// new start program) did not hold it.
			if held && err == nil && gameLaunchStarts == before {
				gameLaunchHeld = game.Path
			}
			return held, err
		}
	}
	return false, nil
}

func gameLaunchRunPrepares(launcher string, game libraryGame) {
	gameLaunchHooks.mu.RLock()
	prepares := gameLaunchHooks.prepares
	gameLaunchHooks.mu.RUnlock()
	for _, fn := range prepares {
		fn(launcher, game)
	}
}

func gameLaunchRunStarted(game libraryGame, launcher string, cmd *exec.Cmd) {
	gameLaunchStarts++
	gameLaunchHooks.mu.RLock()
	started := gameLaunchHooks.started
	gameLaunchHooks.mu.RUnlock()
	for _, fn := range started {
		fn(game, launcher, cmd)
	}
}

func gameLaunchRunExited(game libraryGame, launcher string, ranFor time.Duration, waitErr error) {
	gameLaunchHooks.mu.RLock()
	exited := gameLaunchHooks.exited
	gameLaunchHooks.mu.RUnlock()
	for _, fn := range exited {
		fn(game, launcher, ranFor, waitErr)
	}
}
