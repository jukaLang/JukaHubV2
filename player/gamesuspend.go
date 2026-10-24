package main

// gamesuspend.go owns the "who draws to the screen" handshake between JukaHub
// and the full-screen processes it launches: games, emulators, ports, Wine
// apps and external app shortcuts.
//
// The bug it fixes is the launch blink on handheld firmwares. JukaHub and the
// child share one framebuffer, so while both keep presenting, the panel
// alternated between the JukaHub UI and the game. launch_handover_test.go
// documents the identical fight for the same framebuffer after a self-update.
// The rule here is simpler: while any screen-taking child is alive, the main
// loop must not present a single frame.
//
// The counter is atomic on purpose. Every launch site records a start once its
// child has actually started, and the child's Wait goroutine records the exit,
// so both writes happen off the render thread. No lock is involved, which
// means the normal path cannot deadlock: the render thread only ever takes a
// load, and a goroutine that exits late can delay the counter falling to zero,
// never block the loop.
//
// The same start/exit pair also keeps the live commands in a small registry.
// That registry is what lets quitting stop the game: if JukaHub exits while a
// child still owns the framebuffer, the firmware is left showing a frozen
// picture that ignores input, and every later launch inherits it. The registry
// is separate from the counter so the render-thread check stays a single
// atomic load.

import (
	"log"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// runningGameChildren counts screen-taking children that are alive right now.
var runningGameChildren atomic.Int32

// screenChildren holds the commands whose start was recorded, so an exit can
// stop them instead of leaving one alive over the framebuffer.
var screenChildren struct {
	mu   sync.Mutex
	cmds map[*exec.Cmd]struct{}
}

// noteGameChildStarted records that a screen-taking child has started. Call
// the moment cmd.Start succeeded, so the main loop parks before the child's
// first frame.
func noteGameChildStarted(cmd *exec.Cmd) {
	runningGameChildren.Add(1)
	if cmd == nil {
		return
	}
	screenChildren.mu.Lock()
	if screenChildren.cmds == nil {
		screenChildren.cmds = make(map[*exec.Cmd]struct{}, 1)
	}
	screenChildren.cmds[cmd] = struct{}{}
	screenChildren.mu.Unlock()
}

// noteGameChildExited records that a screen-taking child is gone. Call from
// the goroutine that reaps the child. An unpaired or duplicate exit is
// tolerated: the count clamps at zero instead of turning the detection off
// forever for later launches.
func noteGameChildExited(cmd *exec.Cmd) {
	// Decrement-and-clamp as one compare-and-swap. The earlier Add(-1) followed
	// by Store(0) let a launch that started in between have its increment
	// overwritten, so the loop un-parked under a live game (a blink); worse, a
	// lost increment could later go negative and hide the next launch.
	for {
		cur := runningGameChildren.Load()
		next := cur - 1
		if next < 0 {
			next = 0
		}
		if runningGameChildren.CompareAndSwap(cur, next) {
			break
		}
	}
	if cmd == nil {
		return
	}
	screenChildren.mu.Lock()
	delete(screenChildren.cmds, cmd)
	screenChildren.mu.Unlock()
}

// gameScreenSuspended reports whether a screen-taking child is live and the
// main loop must therefore stop presenting frames.
func gameScreenSuspended() bool {
	return runningGameChildren.Load() > 0
}

// liveScreenChildren snapshots the children that have not been reaped yet.
func liveScreenChildren() []*exec.Cmd {
	screenChildren.mu.Lock()
	defer screenChildren.mu.Unlock()
	if len(screenChildren.cmds) == 0 {
		return nil
	}
	cmds := make([]*exec.Cmd, 0, len(screenChildren.cmds))
	for cmd := range screenChildren.cmds {
		cmds = append(cmds, cmd)
	}
	return cmds
}

// killScreenChildren stops every live child immediately. It never waits, so it
// is safe on the exit deadline where the whole point is to be gone.
func killScreenChildren() {
	for _, cmd := range liveScreenChildren() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
}

// stopScreenChildren asks every live child to quit, waits up to grace for the
// reapers to see them exit, then kills whatever is left. The wait is bounded
// and never joins a child's Wait goroutine, so a child that ignores the signal
// delays the exit by at most grace and cannot deadlock it.
func stopScreenChildren(grace time.Duration) int {
	cmds := liveScreenChildren()
	if len(cmds) == 0 {
		return 0
	}
	log.Printf("[exit] stopping %d running child process(es) before handing the screen back", len(cmds))
	for _, cmd := range cmds {
		if cmd.Process != nil {
			// os.Interrupt is the portable equivalent: not every target has a
			// SIGTERM, and emulators handle INT the same way (save and quit).
			_ = cmd.Process.Signal(os.Interrupt)
		}
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if len(liveScreenChildren()) == 0 {
			return len(cmds)
		}
		time.Sleep(25 * time.Millisecond)
	}
	for _, cmd := range liveScreenChildren() {
		if cmd.Process != nil {
			log.Printf("[exit] child %d ignored the quit request; killing it", cmd.Process.Pid)
			_ = cmd.Process.Kill()
		}
	}
	return len(cmds)
}
