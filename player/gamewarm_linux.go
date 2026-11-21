package main

// gamewarm_linux.go — the reads behind "Warm up games before launch"
// (gamewarm.go).

import (
	"context"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const gameWarmSupported = true

// gameWarmStep is one WILLNEED request. The kernel cuts a larger one to the
// device's read-ahead window (128 KiB by default) and drops the rest, so the
// file is asked for in window-sized steps (startup_linux.go does the same);
// it also makes a cancel take effect within one step.
const gameWarmStep = int64(128 << 10)

var errGameWarmStopped = errors.New("warm-up stopped")

// gameWarmLowPriority puts the warm's goroutine on a thread of its own at
// nice 19 in the idle I/O class (the library index's CRC pass does the same),
// so a launch, the menus and the game itself always read first. The thread
// ends with the goroutine.
func gameWarmLowPriority() { libIndexLowPriority() }

// gameWarmFile asks the kernel to read path's first size bytes into the page
// cache, and reports how many bytes it asked for. WILLNEED only queues
// read-ahead; when the file system refuses it, the rest is read and thrown
// away instead.
func gameWarmFile(ctx context.Context, path string, size int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fd := int(f.Fd())
	done := int64(0)
	for off := int64(0); off < size; off += gameWarmStep {
		if gameWarmShouldStop(ctx) {
			return done, errGameWarmStopped
		}
		n := min(gameWarmStep, size-off)
		if err := unix.Fadvise(fd, off, n, unix.FADV_WILLNEED); err != nil {
			return gameWarmRead(ctx, f, off, size, done)
		}
		done += n
	}
	return done, nil
}

// gameWarmRead is the fallback: f read from off to size, 1 MiB at a time,
// into a buffer that is thrown away.
func gameWarmRead(ctx context.Context, f *os.File, off, size, done int64) (int64, error) {
	buf := make([]byte, 1<<20)
	for off < size {
		if gameWarmShouldStop(ctx) {
			return done, errGameWarmStopped
		}
		n, err := f.ReadAt(buf, off)
		off += int64(n)
		done += int64(n)
		if errors.Is(err, io.EOF) {
			return done, nil
		}
		if err != nil {
			return done, err
		}
	}
	return done, nil
}
