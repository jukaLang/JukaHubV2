//go:build linux

package main

// vncserver_input_linux.go — a VNC viewer's keys in a game, on the handheld:
// written into the built-in pad's evdev node, the way the games mouse writes
// its releases (vcursor_bridge_linux.go). An event written to an evdev node
// is injected into that device, so every program reading the pad —
// RetroArch, a port, the game assistant's Menu watch — sees the press as if
// the button went down. No device is created: the firmware's own pad
// ("TRIMUI Player1", evdevPickPad) is reused, so a game that looks for
// controllers only when it starts still has the one it found. The D-pad is
// the pad's hat and L2/R2 its trigger axes, as the firmware reports them.

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"time"
)

// vncPadRetry is how long a pad that could not be opened waits before the
// next try.
const vncPadRetry = 15 * time.Second

// vncPad is the opened pad and what the viewers hold on it.
var vncPad = struct {
	mu     sync.Mutex
	fd     int
	held   [vncPadCount]bool
	l2, r2 evdevAbsRange
	codes  [4]uint16 // A, B, X, Y under the face-button swap
	failAt time.Time
	wbuf   []byte
}{fd: -1, codes: [4]uint16{evdevBtnSouth, evdevBtnEast, evdevBtnNorth, evdevBtnWest}}

// vncPadSupported reports whether viewer keys can reach a game here.
func vncPadSupported() bool { return IsTrimuiDevice() }

// vncPadSetCodes takes the A/B/X/Y codes the settings give (main thread,
// at the server's start).
func vncPadSetCodes(p mbridgeParams) {
	vncPad.mu.Lock()
	vncPad.codes = [4]uint16{p.codeA, p.codeB, p.codeX, p.codeY}
	vncPad.mu.Unlock()
}

// vncPadPress presses or releases one pad button for a viewer. It reports
// whether the event reached the pad.
func vncPadPress(action int, down bool) bool {
	if action <= vncPadNone || action >= vncPadCount || !vncPadSupported() {
		return false
	}
	vncPad.mu.Lock()
	defer vncPad.mu.Unlock()
	if vncPad.fd < 0 && !vncPadOpenLocked() {
		return false
	}
	if vncPad.held[action] == down {
		// A viewer's key repeat: the pad has no repeat of its own.
		return true
	}
	vncPad.held[action] = down
	return vncPadWriteLocked(vncPadEventLocked(action, down))
}

// vncPadEventLocked is the event for action's new state: a key, a hat axis
// from both of its directions, or a trigger at either end of its range (a
// pad without trigger axes gets BTN_TL2 / BTN_TR2).
func vncPadEventLocked(action int, down bool) evdevEvent {
	hat := func(neg, pos int) int32 {
		v := int32(0)
		if vncPad.held[neg] {
			v--
		}
		if vncPad.held[pos] {
			v++
		}
		return v
	}
	trigger := func(r evdevAbsRange, axis, key uint16) evdevEvent {
		if !r.valid() {
			return evdevEvent{Type: evdevEvKey, Code: key, Value: evdevBool(down)}
		}
		v := r.min
		if down {
			v = r.max
		}
		return evdevEvent{Type: evdevEvAbs, Code: axis, Value: v}
	}
	switch action {
	case vncPadUp, vncPadDown:
		return evdevEvent{Type: evdevEvAbs, Code: evdevAbsHat0Y, Value: hat(vncPadUp, vncPadDown)}
	case vncPadLeft, vncPadRight:
		return evdevEvent{Type: evdevEvAbs, Code: evdevAbsHat0X, Value: hat(vncPadLeft, vncPadRight)}
	case vncPadL2:
		return trigger(vncPad.l2, evdevAbsZ, 0x138)
	case vncPadR2:
		return trigger(vncPad.r2, evdevAbsRZ, 0x139)
	}
	code := uint16(0)
	switch action {
	case vncPadA:
		code = vncPad.codes[0]
	case vncPadB:
		code = vncPad.codes[1]
	case vncPadX:
		code = vncPad.codes[2]
	case vncPadY:
		code = vncPad.codes[3]
	case vncPadL1:
		code = evdevBtnTL
	case vncPadR1:
		code = evdevBtnTR
	case vncPadSelect:
		code = evdevBtnSelect
	case vncPadStart:
		code = evdevBtnStart
	case vncPadMenu:
		code = evdevBtnMode
	}
	return evdevEvent{Type: evdevEvKey, Code: code, Value: evdevBool(down)}
}

// vncPadWriteLocked writes events and a report in one write(). A pad that
// stopped taking them is closed, to be found again on the next key.
func vncPadWriteLocked(evs ...evdevEvent) bool {
	buf := vncPad.wbuf[:0]
	for _, ev := range evs {
		buf = evdevEncode(buf, ev)
	}
	buf = evdevEncode(buf, evdevEvent{Type: evdevEvSyn, Code: evdevSynReport})
	vncPad.wbuf = buf
	for {
		_, err := syscall.Write(vncPad.fd, buf)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			logWarn("vnc", "viewer keys stopped reaching the game: %v", err)
			_ = syscall.Close(vncPad.fd)
			vncPad.fd = -1
			return false
		}
		return true
	}
}

// vncPadOpenLocked finds and opens the pad for writing, and reads its
// trigger ranges.
func vncPadOpenLocked() bool {
	now := time.Now()
	if now.Before(vncPad.failAt) {
		return false
	}
	err := errors.New("no gamepad in " + evdevDevicesPath)
	if data, rerr := os.ReadFile(evdevDevicesPath); rerr != nil {
		err = rerr
	} else if dev, ok := evdevPickPad(evdevParseDevices(string(data))); ok {
		path := "/dev/input/" + dev.Handler
		fd, oerr := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if oerr == nil {
			vncPad.fd = fd
			vncPad.l2, vncPad.r2 = vncPadAxis(fd, evdevAbsZ), vncPadAxis(fd, evdevAbsRZ)
			logInfo("vnc", "viewer keys go to %q (%s) while a game runs", dev.Name, path)
			return true
		}
		err = oerr
	}
	logWarn("vnc", "viewer keys cannot reach the game: %v", err)
	vncPad.failAt = now.Add(vncPadRetry)
	return false
}

// vncPadAxis is one axis's range (zero when the pad has no such axis).
func vncPadAxis(fd int, axis uint16) evdevAbsRange {
	info := make([]byte, evdevAbsInfoSize)
	if mbridgeIoctlBuf(fd, evdevGetAbs(axis), info) != nil {
		return evdevAbsRange{}
	}
	if _, r, ok := evdevParseAbsInfo(info); ok {
		return r
	}
	return evdevAbsRange{}
}

// vncPadReleaseAll lets go of every button the viewers hold on the pad (a
// viewer left, the game ended).
func vncPadReleaseAll() {
	vncPad.mu.Lock()
	defer vncPad.mu.Unlock()
	vncPadReleaseLocked()
}

func vncPadReleaseLocked() {
	var evs []evdevEvent
	for a := vncPadNone + 1; a < vncPadCount; a++ {
		if vncPad.held[a] {
			vncPad.held[a] = false
			evs = append(evs, vncPadEventLocked(a, false))
		}
	}
	if len(evs) > 0 && vncPad.fd >= 0 {
		vncPadWriteLocked(evs...)
	}
}

// vncPadClose releases everything and closes the pad: JukaHub is on screen
// again, or the server stopped.
func vncPadClose() {
	vncPad.mu.Lock()
	defer vncPad.mu.Unlock()
	if vncPad.fd < 0 {
		vncPad.held = [vncPadCount]bool{}
		return
	}
	vncPadReleaseLocked()
	if vncPad.fd >= 0 {
		_ = syscall.Close(vncPad.fd)
		vncPad.fd = -1
	}
	vncPad.failAt = time.Time{}
}
