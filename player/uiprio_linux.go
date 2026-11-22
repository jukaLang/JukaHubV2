//go:build linux

package main

// uiprio_linux.go — the screen's thread first.
//
// Every frame JukaHub draws — input, layout, text, the draw calls, Present —
// runs on one thread: the main one, which go-sdl2 locks the main goroutine to.
// Everything else JukaHub does runs beside it, on the Go runtime's other
// threads and in the processes it starts: a cover scan, an archive being
// unpacked, a download, Syncthing, ffmpeg making a thumbnail. On a four-core
// Cortex-A53 those can take every core for a while, and each frame then waited
// its turn — the stutter while something installs.
//
// The main thread runs at nice uiThreadNice: about three times the share of a
// thread at 0 while every core is busy, and no difference while one is free.
// It is set with SCHED_RESET_ON_FORK, so every thread and process created from
// it starts at nice 0 again: a game, an emulator or a helper JukaHub starts
// from the main loop runs exactly as it did before. Heavy-game boost
// (xboxboost_hub_linux.go) still steps the thread back behind a boosted game,
// and puts this value back after it.
//
// TrimUI handhelds only (JukaHub runs as root there; a negative nice needs
// it). JUKAHUB_UI_PRIORITY=0 leaves the thread alone.

import (
	"os"

	"golang.org/x/sys/unix"
)

// uiThreadNice is the main thread's nice value.
const uiThreadNice = -5

// uiThreadPriorityInit raises the calling thread. main.go calls it once, on
// the main goroutine, after the logger.
func uiThreadPriorityInit() {
	if os.Getenv("JUKAHUB_UI_PRIORITY") == "0" || !IsTrimuiDevice() {
		return
	}
	tid := unix.Gettid()
	attr := unix.SchedAttr{
		Policy: unix.SCHED_NORMAL,
		Flags:  unix.SCHED_FLAG_RESET_ON_FORK,
		Nice:   uiThreadNice,
	}
	if err := unix.SchedSetAttr(tid, &attr, 0); err != nil {
		logInfo("perf", "screen thread priority unchanged: %v", err)
		return
	}
	logInfo("perf", "screen thread %d at nice %d (what it starts runs at 0)", tid, uiThreadNice)
}
