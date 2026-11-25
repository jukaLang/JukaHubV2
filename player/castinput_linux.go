//go:build linux

package main

// castinput_linux.go — a Screen Share viewer's presses on a TrimUI handheld:
// written into the built-in pad's evdev node (evdevPickPad, "TRIMUI
// Player1"), the way the VNC server's keys reach a game
// (vncserver_input_linux.go). An event written to an evdev node is injected
// into that device, so every program reading the pad sees it.
//
// The buttons go by position, like the handheld's own: the viewer's right
// face button is the pad's BTN_EAST (the printed A), the bottom one BTN_SOUTH
// (B), the top one 0x134 (X), the left one 0x133 (Y). The D-pad is the pad's
// hat, L2/R2 its trigger axes and the sticks its X/Y and RX/RY axes, scaled
// into the ranges the pad reports — each written only when it changes, and
// a button this side never pressed is never released by it.
//
// The firmware's own reader (trimui_inputd) writes the pad too. If it
// re-sends what the physical buttons and sticks say on every poll, it would
// undo a held press within a few milliseconds. So what comes back from the
// pad is watched: an event that contradicts what the viewer holds and is newer
// than this side's last write for it (the kernel stamps both) is the firmware
// writing over it, and the viewer's value is written again at once. Three of
// those within a second mark a firmware that fights, and from then on its
// reader is stopped (SIGSTOP) while the viewer holds anything and continued
// 200 ms after the viewer lets go — the handheld's own buttons pause for that
// time, which is the price of the TV's controller being heard. It is continued
// on every way out (castinput.go's stops, the device closing, JukaHub closing),
// and the ids it was stopped under are written to /tmp so a JukaHub that died
// in between continues it at its next start (castInputRecover).

import (
	"encoding/binary"
	"errors"
	"math/bits"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// castPadFightWindow / castPadFightLimit: this many contradictions in this
	// long mark a firmware that rewrites the pad.
	castPadFightWindow = time.Second
	castPadFightLimit  = 3
	// castPadResumeAfter is how long after the viewer let go the firmware's
	// reader runs again.
	castPadResumeAfter = 200 * time.Millisecond
	// castInputdMarker lists the reader's ids while it is stopped.
	castInputdMarker = "/tmp/jukahub-share-inputd-stopped"
)

// castInputdNames are the firmware pad readers that write the built-in pad.
var castInputdNames = []string{"trimui_inputd", "tsp_inputd"}

// castEvdevPad is the built-in pad, opened for one viewer's turn.
type castEvdevPad struct {
	fd     int
	name   string
	keys   []uint64
	hat    bool
	trig   bool
	ranges map[uint16]evdevAbsRange
	// down are the keys this side pressed, abs the axis values it set, and
	// wrote when it last wrote each code (the wall clock the kernel's event
	// stamps use).
	down  map[uint16]bool
	abs   map[uint16]int32
	wrote map[uint32]time.Time
	wbuf  []byte
	rbuf  []byte
	// fights: the firmware was seen writing over held input; stopped are its
	// reader's ids while paused; calmAt when the viewer last let go.
	fightAt []time.Time
	fights  bool
	stopped []int
	calmAt  time.Time
	dead    bool
}

// castPadDeviceOpen opens the built-in pad for writing.
func castPadDeviceOpen() (castPadDevice, error) {
	data, err := os.ReadFile(evdevDevicesPath)
	if err != nil {
		return nil, err
	}
	dev, ok := evdevPickPad(evdevParseDevices(string(data)))
	if !ok {
		return nil, errors.New("no gamepad in " + evdevDevicesPath)
	}
	path := "/dev/input/" + dev.Handler
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	p := &castEvdevPad{fd: fd, name: dev.Name, keys: dev.Key,
		ranges: map[uint16]evdevAbsRange{}, down: map[uint16]bool{}, abs: map[uint16]int32{},
		wrote: map[uint32]time.Time{}, rbuf: make([]byte, 64*evdevEventSize)}
	p.hat = evdevHas(dev.Abs, evdevAbsHat0X) && evdevHas(dev.Abs, evdevAbsHat0Y)
	for _, axis := range [...]uint16{evdevAbsX, evdevAbsY, evdevAbsZ, evdevAbsRX, evdevAbsRY, evdevAbsRZ} {
		if !evdevHas(dev.Abs, int(axis)) {
			continue
		}
		if r := vncPadAxis(fd, axis); r.valid() {
			p.ranges[axis] = r
		}
	}
	_, z := p.ranges[evdevAbsZ]
	_, rz := p.ranges[evdevAbsRZ]
	p.trig = z && rz
	logInfo("cast", "a viewer's presses go to %q (%s)", dev.Name, path)
	return p, nil
}

// castEvdevFace are the pad's codes for the remote's A, B, X and Y: by
// position (right, bottom, top, left), as the TrimUI pad reports its buttons.
var castEvdevFace = [4]uint16{evdevBtnEast, evdevBtnSouth, evdevBtnWest, evdevBtnNorth}

// keyCode is the evdev key for a remote button, 0 for one that is an axis
// on this pad.
func (p *castEvdevPad) keyCode(b int) uint16 {
	switch b {
	case castBtnA, castBtnB, castBtnX, castBtnY:
		return castEvdevFace[b-castBtnA]
	case castBtnL1:
		return evdevBtnTL
	case castBtnR1:
		return evdevBtnTR
	case castBtnSelect:
		return evdevBtnSelect
	case castBtnStart:
		return evdevBtnStart
	case castBtnMenu:
		return evdevBtnMode
	case castBtnL3:
		return 0x13d // BTN_THUMBL
	case castBtnR3:
		return 0x13e // BTN_THUMBR
	case castBtnL2:
		if !p.trig {
			return 0x138 // BTN_TL2
		}
	case castBtnR2:
		if !p.trig {
			return 0x139 // BTN_TR2
		}
	case castBtnUp, castBtnDown, castBtnLeft, castBtnRight:
		if !p.hat {
			return 0x220 + uint16(b-castBtnUp) // BTN_DPAD_UP, DOWN, LEFT, RIGHT
		}
	}
	return 0
}

// axisValues is what st puts on each of the pad's axes, and each axis's rest.
func (p *castEvdevPad) axisValues(st castPadState) (want, rest map[uint16]int32) {
	want, rest = map[uint16]int32{}, map[uint16]int32{}
	if p.hat {
		hx, hy := int32(0), int32(0)
		if st.has(castBtnLeft) {
			hx--
		}
		if st.has(castBtnRight) {
			hx++
		}
		if st.has(castBtnUp) {
			hy--
		}
		if st.has(castBtnDown) {
			hy++
		}
		want[evdevAbsHat0X], want[evdevAbsHat0Y] = hx, hy
		rest[evdevAbsHat0X], rest[evdevAbsHat0Y] = 0, 0
	}
	stick := func(axis uint16, v float64) {
		r, ok := p.ranges[axis]
		if !ok {
			return
		}
		mid := (float64(r.min) + float64(r.max)) / 2
		half := (float64(r.max) - float64(r.min)) / 2
		rest[axis] = int32(mid)
		want[axis] = int32(mid + max(-1, min(1, v))*half)
	}
	stick(evdevAbsX, st.lx)
	stick(evdevAbsY, st.ly)
	stick(evdevAbsRX, st.rx)
	stick(evdevAbsRY, st.ry)
	if p.trig {
		trig := func(axis uint16, down bool, t float64) {
			r := p.ranges[axis]
			if down {
				t = 1
			}
			rest[axis] = r.min
			want[axis] = r.min + int32(max(0, min(1, t))*float64(r.max-r.min))
		}
		trig(evdevAbsZ, st.has(castBtnL2), st.lt)
		trig(evdevAbsRZ, st.has(castBtnR2), st.rt)
	}
	return want, rest
}

// apply writes what changed since the last state.
func (p *castEvdevPad) apply(st castPadState, now time.Time) {
	if p.dead {
		return
	}
	var evs []evdevEvent
	for b := 0; b < castBtnCount; b++ {
		code := p.keyCode(b)
		if code == 0 || !evdevHas(p.keys, int(code)) {
			continue
		}
		switch down := st.has(b); {
		case down && !p.down[code]:
			p.down[code] = true
			evs = append(evs, evdevEvent{Type: evdevEvKey, Code: code, Value: 1})
		case !down && p.down[code]:
			delete(p.down, code)
			evs = append(evs, evdevEvent{Type: evdevEvKey, Code: code, Value: 0})
		}
	}
	want, rest := p.axisValues(st)
	for axis, v := range want {
		cur, set := p.abs[axis]
		if (set && cur == v) || (!set && v == rest[axis]) {
			continue
		}
		if v == rest[axis] {
			// Back at rest: the axis is the firmware's again.
			delete(p.abs, axis)
		} else {
			p.abs[axis] = v
		}
		evs = append(evs, evdevEvent{Type: evdevEvAbs, Code: axis, Value: v})
	}
	p.write(evs)
}

// write sends events and a report in one write() and notes when each code
// was written: after the write returns, so the events it produced — stamped
// by the kernel while it ran — are older than the note and never taken for
// the firmware's.
func (p *castEvdevPad) write(evs []evdevEvent) {
	if len(evs) == 0 || p.dead {
		return
	}
	buf := p.wbuf[:0]
	for _, ev := range evs {
		buf = evdevEncode(buf, ev)
	}
	buf = evdevEncode(buf, evdevEvent{Type: evdevEvSyn, Code: evdevSynReport})
	p.wbuf = buf
	for {
		_, err := syscall.Write(p.fd, buf)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			logWarn("cast", "a viewer's presses stopped reaching %q: %v", p.name, err)
			p.dead = true
			return
		}
		break
	}
	at := time.Now()
	for _, ev := range evs {
		p.wrote[castEvdevKey(ev)] = at
	}
}

// slack is how far an axis may read from what this side set before it counts
// as written over: the kernel smooths a value by the axis's fuzz, and a stick
// that is overruled lands far away.
func (p *castEvdevPad) slack(axis uint16) int32 {
	if r, ok := p.ranges[axis]; ok {
		return (r.max - r.min) / 16
	}
	return 0
}

// castEvdevKey names an event's type and code together.
func castEvdevKey(ev evdevEvent) uint32 { return uint32(ev.Type)<<16 | uint32(ev.Code) }

// castEvdevStamp reads an event's time stamp (struct timeval: two C longs).
func castEvdevStamp(rec []byte) time.Time {
	w := bits.UintSize / 8
	if len(rec) < 2*w {
		return time.Time{}
	}
	var sec, usec int64
	if w == 8 {
		sec, usec = int64(binary.LittleEndian.Uint64(rec)), int64(binary.LittleEndian.Uint64(rec[8:]))
	} else {
		sec, usec = int64(int32(binary.LittleEndian.Uint32(rec))), int64(int32(binary.LittleEndian.Uint32(rec[4:])))
	}
	return time.Unix(sec, usec*1000)
}

// poll reads what the pad reported since, writes held input again over a
// firmware that wrote over it, and pauses or continues that firmware's
// reader. It reports whether the fast tick is needed.
func (p *castEvdevPad) poll(st castPadState, now time.Time) bool {
	if p.dead {
		p.resumeReader()
		return false
	}
	fought := false
	for {
		n, err := syscall.Read(p.fd, p.rbuf)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil || n <= 0 {
			break
		}
		for off := 0; off+evdevEventSize <= n; off += evdevEventSize {
			rec := p.rbuf[off : off+evdevEventSize]
			evs := evdevDecode(rec, nil)
			if len(evs) != 1 {
				continue
			}
			ev := evs[0]
			at := castEvdevStamp(rec)
			if !at.After(p.wrote[castEvdevKey(ev)]) {
				continue // this side's own write, or older than it
			}
			switch ev.Type {
			case evdevEvKey:
				fought = fought || (ev.Value == 0 && p.down[ev.Code])
			case evdevEvAbs:
				if v, set := p.abs[ev.Code]; set && (ev.Value-v > p.slack(ev.Code) || v-ev.Value > p.slack(ev.Code)) {
					fought = true
				}
			}
		}
		if n < len(p.rbuf) {
			break
		}
	}
	if fought {
		p.reassert(now)
		keep := p.fightAt[:0]
		for _, t := range p.fightAt {
			if now.Sub(t) < castPadFightWindow {
				keep = append(keep, t)
			}
		}
		p.fightAt = append(keep, now)
		if !p.fights && len(p.fightAt) >= castPadFightLimit {
			p.fights = true
			logInfo("cast", "the firmware rewrites %q over a viewer's held buttons: its reader pauses while the viewer holds one", p.name)
		}
	}
	if p.fights {
		if !st.neutral() {
			p.calmAt = time.Time{}
			if p.stopped == nil {
				p.stopReader()
			}
		} else if p.stopped != nil {
			if p.calmAt.IsZero() {
				p.calmAt = now
			} else if now.Sub(p.calmAt) >= castPadResumeAfter {
				p.resumeReader()
			}
		}
	}
	return p.stopped != nil || len(p.down) > 0 || len(p.abs) > 0
}

// reassert writes everything held again.
func (p *castEvdevPad) reassert(now time.Time) {
	evs := make([]evdevEvent, 0, len(p.down)+len(p.abs))
	for code := range p.down {
		evs = append(evs, evdevEvent{Type: evdevEvKey, Code: code, Value: 1})
	}
	for axis, v := range p.abs {
		evs = append(evs, evdevEvent{Type: evdevEvAbs, Code: axis, Value: v})
	}
	p.write(evs)
}

func (p *castEvdevPad) failed() bool { return p.dead }

// close lets go of everything this side holds, continues the firmware's
// reader and closes the pad.
func (p *castEvdevPad) close() {
	if !p.dead {
		p.apply(castPadState{}, time.Now())
	}
	p.resumeReader()
	_ = syscall.Close(p.fd)
	p.dead = true
}

// ── The firmware's reader ─────────────────────────────────────────────────

var castInputdMu sync.Mutex

// stopReader pauses the firmware's pad readers and notes their ids.
func (p *castEvdevPad) stopReader() {
	pids := castInputdPIDs()
	if len(pids) == 0 {
		p.stopped = []int{}
		return
	}
	castInputdMu.Lock()
	defer castInputdMu.Unlock()
	var ids []string
	for _, pid := range pids {
		ids = append(ids, strconv.Itoa(pid))
	}
	// Written first: a JukaHub that dies after the stop still knows whom
	// to continue.
	_ = os.WriteFile(castInputdMarker, []byte(strings.Join(ids, " ")+"\n"), 0o644)
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGSTOP)
	}
	p.stopped = pids
}

// resumeReader continues what stopReader paused.
func (p *castEvdevPad) resumeReader() {
	if p.stopped == nil {
		return
	}
	castInputdMu.Lock()
	defer castInputdMu.Unlock()
	for _, pid := range p.stopped {
		_ = syscall.Kill(pid, syscall.SIGCONT)
	}
	_ = os.Remove(castInputdMarker)
	p.stopped = nil
	p.calmAt = time.Time{}
}

// castInputdPIDs are the running firmware pad readers.
func castInputdPIDs() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 || pid == os.Getpid() {
			continue
		}
		if castInputdIs(pid) {
			out = append(out, pid)
		}
	}
	return out
}

// castInputdIs reports whether pid is a firmware pad reader.
func castInputdIs(pid int) bool {
	comm, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
	if err != nil {
		return false
	}
	name := strings.TrimSpace(string(comm))
	for _, n := range castInputdNames {
		if name == n {
			return true
		}
	}
	return false
}

// castInputRecover continues a pad reader a JukaHub that died left stopped.
func castInputRecover() {
	castInputdMu.Lock()
	defer castInputdMu.Unlock()
	raw, err := os.ReadFile(castInputdMarker)
	if err != nil {
		return
	}
	for _, f := range strings.Fields(string(raw)) {
		if pid, err := strconv.Atoi(f); err == nil && pid > 1 && castInputdIs(pid) {
			_ = syscall.Kill(pid, syscall.SIGCONT)
			logInfo("cast", "continued the pad reader %d a Screen Share viewer's turn left paused", pid)
		}
	}
	_ = os.Remove(castInputdMarker)
}
