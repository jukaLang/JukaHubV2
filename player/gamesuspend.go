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

import (
	"sync/atomic"
)

// runningGameChildren counts screen-taking children that are alive right now.
var runningGameChildren atomic.Int32

// noteGameChildStarted records that a screen-taking child has started. Call
// the moment cmd.Start succeeded, so the main loop parks before the child's
// first frame.
func noteGameChildStarted() {
	runningGameChildren.Add(1)
}

// noteGameChildExited records that a screen-taking child is gone. Call from
// the goroutine that reaps the child. An unpaired or duplicate exit is
// tolerated: the count clamps at zero instead of turning the detection off
// forever for later launches.
func noteGameChildExited() {
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
			return
		}
	}
}

// gameScreenSuspended reports whether a screen-taking child is live and the
// main loop must therefore stop presenting frames.
func gameScreenSuspended() bool {
	return runningGameChildren.Load() > 0
}
