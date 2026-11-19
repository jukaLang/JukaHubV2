//go:build linux

package main

// shader_speed_linux.go — the frames a game really put on the panel, counted
// for the shader speed check (shader_speed.go).
//
// RetroArch on the handheld draws into the framebuffer through the GPU
// driver, which shows each finished frame by panning the panel to the buffer
// it was drawn in: the pan offset FBIOGET_VSCREENINFO reports changes once per
// presented frame. Polling that offset every few milliseconds and counting the
// changes is the played frame rate, measured from outside the emulator, with
// the shader and everything else it costs included. A driver that never pans
// (a single buffer, DRM page flips) shows no change at all; the check then
// falls back to RetroArch's own log.
//
// One descriptor, one buffer and one syscall per poll: nothing is allocated
// while it counts. It polls fast only while the check is in a stretch of play
// it can use, and stops on its own after shaderFlipMaxRun whatever happens.

import (
	"encoding/binary"
	"os"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	shaderFlipDevice = "/dev/fb0"
	// shaderFlipPoll is the fast poll: four polls to a 60 Hz frame, so no
	// flip is missed at any rate a handheld panel runs.
	shaderFlipPoll = 4 * time.Millisecond
	// shaderFlipIdlePoll is the poll while the counts are not used (the game
	// paused, the menu up): just enough to keep the last offset current.
	shaderFlipIdlePoll = 100 * time.Millisecond
	// shaderFlipMaxRun ends a counter its owner forgot.
	shaderFlipMaxRun = 15 * time.Minute
)

// shaderFlipCounter counts pan-offset changes of the framebuffer on its own
// goroutine. flips is read by the check's goroutine at any time.
type shaderFlipCounter struct {
	f     *os.File
	buf   []byte
	flips atomic.Uint64
	fast  atomic.Bool
	stop  atomic.Bool
	// dead is set when the poll has ended, asked to or not (an ioctl the
	// driver refused, shaderFlipMaxRun): its count no longer moves.
	dead atomic.Bool
}

// shaderSpeedFramesPossible reports whether this machine has a framebuffer a
// flip count could be read from at all.
func shaderSpeedFramesPossible() bool {
	info, err := os.Stat(shaderFlipDevice)
	return err == nil && info.Mode()&os.ModeDevice != 0
}

// shaderSpeedFlipsOpen opens the framebuffer and starts counting (slowly until
// setFast). The caller ends it with close.
func shaderSpeedFlipsOpen() (*shaderFlipCounter, error) {
	f, err := os.OpenFile(shaderFlipDevice, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	c := &shaderFlipCounter{f: f, buf: make([]byte, castFBVarInfoSize)}
	if err := castIoctl(f, castFBIOGetVScreenInfo, c.buf); err != nil {
		_ = f.Close()
		return nil, err
	}
	go c.run()
	return c, nil
}

// run is the poll loop. The descriptor is taken once: File.Fd may change the
// file's blocking mode with a syscall of its own on every call.
func (c *shaderFlipCounter) run() {
	defer c.dead.Store(true)
	defer c.f.Close()
	fd := c.f.Fd()
	lastX := binary.LittleEndian.Uint32(c.buf[16:])
	lastY := binary.LittleEndian.Uint32(c.buf[20:])
	started := time.Now()
	for !c.stop.Load() {
		if c.fast.Load() {
			time.Sleep(shaderFlipPoll)
		} else {
			time.Sleep(shaderFlipIdlePoll)
		}
		if time.Since(started) > shaderFlipMaxRun {
			return
		}
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, castFBIOGetVScreenInfo, uintptr(unsafe.Pointer(&c.buf[0]))); errno != 0 {
			return
		}
		x := binary.LittleEndian.Uint32(c.buf[16:]) // xoffset
		y := binary.LittleEndian.Uint32(c.buf[20:]) // yoffset
		if x != lastX || y != lastY {
			lastX, lastY = x, y
			c.flips.Add(1)
		}
	}
}

// count is the number of flips seen so far.
func (c *shaderFlipCounter) count() uint64 {
	if c == nil {
		return 0
	}
	return c.flips.Load()
}

// alive reports whether the poll still runs, so count still moves.
func (c *shaderFlipCounter) alive() bool {
	return c != nil && !c.dead.Load()
}

// setFast switches between the counting poll and the idle one.
func (c *shaderFlipCounter) setFast(on bool) {
	if c != nil {
		c.fast.Store(on)
	}
}

// close ends the counter; its goroutine closes the descriptor within one poll.
func (c *shaderFlipCounter) close() {
	if c != nil {
		c.stop.Store(true)
	}
}
