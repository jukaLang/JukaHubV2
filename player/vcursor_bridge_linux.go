//go:build linux

package main

// vcursor_bridge_linux.go — Mouse in games and apps, on a Linux handheld.
//
// While a game, an emulator or a port owns the screen (gameScreenSuspended),
// a goroutine reads the built-in pad's evdev node beside the game. Holding
// Select + L1 for 0.7 s — the chord of JukaHub's own mouse mode — turns the
// pad into a mouse for that program:
//
//  1. every button the game saw go down is released and every stick is put
//     back at rest, by writing those events into the pad, so nothing stays
//     stuck in the game while it cannot see the pad;
//  2. the pad is grabbed (EVIOCGRAB): from then on only JukaHub reads it;
//  3. a mouse is created through /dev/uinput ("JukaHub Mouse": left, right
//     and middle buttons, relative X / Y and a wheel), and the sticks and the
//     D-pad move it at 125 Hz on the in-app pointer's curve. A is the left
//     button (hold it to drag), B the right one, X the middle one, L2 / R2
//     the wheel.
//
// The chord again, or Menu, gives the pad back: the mouse is removed and the
// grab let go. So does the game ending, JukaHub quitting (a Cleanup hook
// waits a moment for it), any error, and the pad going away. If JukaHub dies
// outright the kernel does the same, because both belong to its open files.
//
// Only a program that draws its own pointer shows one (ScummVM, DOSBox, PC
// ports, a WPE browser); others ignore the mouse, and a program whose SDL
// finds devices only when it starts (no udev hotplug) may not see a mouse
// that appears later. Settings → Controls → Mouse in games and apps (off by
// default) turns this on; it is the game-side half of mouse mode
// (vcursor_mode.go), and the pure half — structures, ioctl numbers, the device
// list and the engine — is vcursor_evdev.go.
//
// Nothing JukaHub draws is on screen during a game, so the feedback is the
// vibration motor: a firm pulse when the mouse comes on, a short one when it
// goes, a long one when it could not.

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	// mbridgeWatchEvery paces the reads while the bridge only watches for
	// the chord; mbridgeMoveEvery while the pad is the mouse (125 Hz).
	mbridgeWatchEvery = 25 * time.Millisecond
	mbridgeMoveEvery  = 8 * time.Millisecond
	// mbridgeRetryEvery is how soon a pad that could not be opened, or went
	// away, is looked for again.
	mbridgeRetryEvery = 2 * time.Second
	// mbridgeExitGrace bounds how long quitting waits for the pad to be given
	// back.
	mbridgeExitGrace = 300 * time.Millisecond
	// mbridgeReadEvents is how many events one read takes at most.
	mbridgeReadEvents = 64
)

// mbridgeUinputPaths are where the uinput node lives on Linux systems.
var mbridgeUinputPaths = []string{"/dev/uinput", "/dev/input/uinput"}

// mbridge is the bridge's lifetime: one goroutine per game, started from the
// parked main loop (vcursorBridgeTick) and ended by the game ending, the
// switch turning off, or quitting (mbridgeShutdown).
var mbridge struct {
	mu      sync.Mutex
	running bool
	stop    chan struct{}
	done    chan struct{}
	// want mirrors the switch for the goroutine, which must not read the
	// config.
	want atomic.Bool
}

func init() {
	// Quitting gives the pad back and removes the mouse even with a game
	// still running.
	Cleanup = append(Cleanup, mbridgeShutdown)
}

// vcursorBridgeSupported reports whether this device can lend its pad to a
// game as a mouse: a TrimUI handheld (the firmware's pad, uinput, root).
func vcursorBridgeSupported() bool { return IsTrimuiDevice() }

// vcursorBridgeTick runs on the main thread, from the parked loop, while a
// game owns the screen. It mirrors the switch for the goroutine and starts
// one per game while the switch is on; the goroutine ends with the game.
func vcursorBridgeTick(config *Config) {
	want := vcursorGamesEnabled(config) && vcursorBridgeSupported()
	mbridge.want.Store(want)
	if !want {
		return
	}
	mbridge.mu.Lock()
	if mbridge.running {
		mbridge.mu.Unlock()
		return
	}
	stop, done := make(chan struct{}), make(chan struct{})
	mbridge.running, mbridge.stop, mbridge.done = true, stop, done
	mbridge.mu.Unlock()
	// The goroutine buzzes the motor; find it here, on the main thread, so a
	// first pulse never probes an SDL pad from the goroutine.
	hapticsAvailable()
	go mbridgeRun(mbridgeParamsFor(config), stop, done)
}

// mbridgeShutdown is the Cleanup hook: it asks a running bridge to give the
// pad back and waits at most mbridgeExitGrace. Quitting is never held up
// longer; the kernel lets go of both when the process ends anyway.
func mbridgeShutdown() {
	mbridge.want.Store(false)
	mbridge.mu.Lock()
	if !mbridge.running {
		mbridge.mu.Unlock()
		return
	}
	stop, done := mbridge.stop, mbridge.done
	select {
	case <-stop:
	default:
		close(stop)
	}
	mbridge.mu.Unlock()
	select {
	case <-done:
	case <-time.After(mbridgeExitGrace):
		logWarn("vcursor", "the games mouse did not stop within %s; the system lets go of the pad when JukaHub ends", mbridgeExitGrace)
	}
}

// mbridgeSession is one game's bridge: the pad, the mouse and the engine
// between them. The goroutine owns it.
type mbridgeSession struct {
	eng      mbridgeEngine
	padFD    int
	padName  string
	canWrite bool
	grabbed  bool
	dropping bool
	mouseFD  int
	buf      []byte
	wbuf     []byte
	events   []evdevEvent
	out      []evdevEvent
	lastNote string
}

// mbridgeRun is the goroutine: it watches the pad while the game runs and
// lends it as a mouse on the chord. Whatever happens, the pad is given back
// before it returns.
func mbridgeRun(p mbridgeParams, stop <-chan struct{}, done chan<- struct{}) {
	s := &mbridgeSession{padFD: -1, mouseFD: -1, buf: make([]byte, mbridgeReadEvents*evdevEventSize)}
	s.eng.p = p
	// Run last to first: the pad is given back, then the bridge is marked
	// stopped (so quitting stops waiting), and a fault anywhere is logged
	// instead of taking JukaHub down with it.
	defer func() {
		if r := recover(); r != nil {
			logWarn("vcursor", "games mouse stopped after a fault: %v", r)
		}
	}()
	defer func() {
		mbridge.mu.Lock()
		mbridge.running = false
		mbridge.mu.Unlock()
		close(done)
	}()
	defer s.close()
	var retryAt time.Time
	for {
		select {
		case <-stop:
			return
		default:
		}
		// The game ended, the switch went off, or an alarm rings: JukaHub is
		// on screen again (the alarm takes it over the paused game) and needs
		// the pad. The parked loop starts a new bridge when the game is back.
		if !gameScreenSuspended() || !mbridge.want.Load() || alarmFiringActive() {
			return
		}
		now := time.Now()
		if s.padFD < 0 {
			if now.Before(retryAt) {
				time.Sleep(4 * mbridgeWatchEvery)
				continue
			}
			if err := s.openPad(now); err != nil {
				s.note("no pad to watch: " + err.Error())
				retryAt = now.Add(mbridgeRetryEvery)
				continue
			}
		}
		if err := s.step(now); err != nil {
			s.note("lost the pad: " + err.Error())
			s.close()
			retryAt = now.Add(mbridgeRetryEvery)
			continue
		}
		if s.eng.on {
			time.Sleep(mbridgeMoveEvery)
		} else {
			time.Sleep(mbridgeWatchEvery)
		}
	}
}

// openPad finds the built-in pad in /proc/bus/input/devices and opens it —
// read-write when it may, so the releases can be written into it — then
// reads its axes' ranges and the buttons held.
func (s *mbridgeSession) openPad(now time.Time) error {
	data, err := os.ReadFile(evdevDevicesPath)
	if err != nil {
		return err
	}
	dev, ok := evdevPickPad(evdevParseDevices(string(data)))
	if !ok {
		return errors.New("no gamepad in " + evdevDevicesPath)
	}
	path := "/dev/input/" + dev.Handler
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	s.canWrite = err == nil
	if err != nil {
		if fd, err = syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0); err != nil {
			return err
		}
	}
	s.padFD, s.padName, s.dropping = fd, dev.Name, false
	s.readAxes()
	s.syncKeys()
	s.eng.last = now
	logInfo("vcursor", "games mouse: watching %q (%s) for Select + L1", dev.Name, path)
	return nil
}

// readAxes loads each axis's range and value now into the engine.
func (s *mbridgeSession) readAxes() {
	info := make([]byte, evdevAbsInfoSize)
	for _, axis := range [...]uint16{evdevAbsX, evdevAbsY, evdevAbsZ, evdevAbsRX, evdevAbsRY, evdevAbsRZ, evdevAbsHat0X, evdevAbsHat0Y} {
		if mbridgeIoctlBuf(s.padFD, evdevGetAbs(axis), info) != nil {
			continue
		}
		if v, r, ok := evdevParseAbsInfo(info); ok {
			s.eng.setRange(axis, r, v)
		}
	}
}

// syncKeys loads the buttons held now into the engine.
func (s *mbridgeSession) syncKeys() {
	bitmap := make([]byte, evdevKeyBitmapSize)
	if mbridgeIoctlBuf(s.padFD, evdevGetKey(evdevKeyBitmapSize), bitmap) == nil {
		s.eng.setKeys(bitmap)
	}
}

// step reads what the pad sent since the last step, feeds the engine, writes
// the mouse events it made, and turns the mouse on or off when asked.
func (s *mbridgeSession) step(now time.Time) error {
	out := s.out[:0]
	off := false
	for {
		n, err := syscall.Read(s.padFD, s.buf)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if errors.Is(err, syscall.EAGAIN) {
			break
		}
		if err != nil {
			return err
		}
		if n <= 0 {
			return errors.New("the pad stopped reporting")
		}
		s.events = evdevDecode(s.buf[:n], s.events[:0])
		for _, ev := range s.events {
			// The kernel dropped events (our reads fell behind): what follows
			// up to the next report is incomplete, so it is skipped and the
			// pad's state read whole instead — buttons, axes, and the mouse
			// buttons whose release was lost let go.
			if ev.Type == evdevEvSyn && ev.Code == evdevSynDropped {
				s.dropping = true
				continue
			}
			if s.dropping {
				if ev.Type == evdevEvSyn && ev.Code == evdevSynReport {
					s.dropping = false
					s.syncKeys()
					s.readAxes()
					out = s.eng.reconcileButtons(out)
				}
				continue
			}
			var menu bool
			out, menu = s.eng.feed(ev, now, out)
			off = off || menu
		}
		if n < len(s.buf) {
			break
		}
	}
	out, toggle := s.eng.tick(now, out)
	s.out = out
	if s.eng.on && len(out) > 0 {
		out = append(out, evdevEvent{Type: evdevEvSyn, Code: evdevSynReport})
		s.out = out
		if err := s.writeEvents(s.mouseFD, out); err != nil {
			s.note("the mouse stopped: " + err.Error())
			s.mouseOff(now, false)
			return nil
		}
	}
	switch {
	case s.eng.on && (toggle || off):
		s.mouseOff(now, true)
	case toggle:
		s.mouseOn(now)
	}
	return nil
}

// mouseOn lends the pad to the game as a mouse: the steps in the file comment.
func (s *mbridgeSession) mouseOn(now time.Time) {
	if s.canWrite {
		if rel := s.eng.releaseEvents(nil); len(rel) > 0 {
			rel = append(rel, evdevEvent{Type: evdevEvSyn, Code: evdevSynReport})
			if err := s.writeEvents(s.padFD, rel); err != nil {
				logDebug("vcursor", "games mouse: could not let go of the game's buttons: %v", err)
			}
		}
	}
	if err := mbridgeIoctl(s.padFD, evdevGrab, 1); err != nil {
		s.fail("could not take the pad from the game", err)
		return
	}
	s.grabbed = true
	fd, err := mbridgeCreateMouse()
	if err != nil {
		_ = mbridgeIoctl(s.padFD, evdevGrab, 0)
		s.grabbed = false
		s.fail("could not create the mouse", err)
		return
	}
	s.mouseFD = fd
	s.eng.setOn(true, now)
	playHaptic(hapticSuccessPulse)
	logInfo("vcursor", "games mouse on: %q is the mouse until Select + L1 or Menu", s.padName)
}

// mouseOff gives the pad back: the mouse's held buttons are let go, the mouse
// is removed and the grab released. pulse buzzes when the mouse was on.
func (s *mbridgeSession) mouseOff(now time.Time, pulse bool) {
	if s.mouseFD >= 0 {
		var ups []evdevEvent
		for i, held := range s.eng.btn {
			if held {
				ups = append(ups, evdevEvent{Type: evdevEvKey, Code: evdevMouseButtons[i], Value: 0})
			}
		}
		if len(ups) > 0 {
			ups = append(ups, evdevEvent{Type: evdevEvSyn, Code: evdevSynReport})
			_ = s.writeEvents(s.mouseFD, ups)
		}
		_ = mbridgeIoctl(s.mouseFD, evdevUIDevDestroy, 0)
		_ = syscall.Close(s.mouseFD)
		s.mouseFD = -1
	}
	if s.grabbed && s.padFD >= 0 {
		_ = mbridgeIoctl(s.padFD, evdevGrab, 0)
	}
	s.grabbed = false
	wasOn := s.eng.on
	s.eng.setOn(false, now)
	if wasOn && pulse {
		playHaptic(hapticBackPulse)
		logInfo("vcursor", "games mouse off: the pad is the game's again")
	}
}

// close gives everything back and closes the pad; the engine forgets the pad
// but keeps the settings, for a pad opened again.
func (s *mbridgeSession) close() {
	s.mouseOff(time.Now(), false)
	if s.padFD >= 0 {
		_ = syscall.Close(s.padFD)
		s.padFD = -1
	}
	s.eng = mbridgeEngine{p: s.eng.p}
}

// writeEvents writes events in one write(), the way the kernel reads them.
func (s *mbridgeSession) writeEvents(fd int, evs []evdevEvent) error {
	if fd < 0 {
		return syscall.EBADF
	}
	s.wbuf = s.wbuf[:0]
	for _, ev := range evs {
		s.wbuf = evdevEncode(s.wbuf, ev)
	}
	for {
		_, err := syscall.Write(fd, s.wbuf)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// note logs a problem once, not on every retry.
func (s *mbridgeSession) note(msg string) {
	if msg != s.lastNote {
		s.lastNote = msg
		logWarn("vcursor", "games mouse: %s", msg)
	}
}

// fail reports a mouse that could not come on: logged, and a long buzz.
func (s *mbridgeSession) fail(what string, err error) {
	s.note(what + ": " + err.Error())
	playHaptic(hapticErrorPulse)
}

// mbridgeCreateMouse makes the virtual mouse: EV_KEY with the three buttons
// and EV_REL with X, Y and the wheel, then UI_DEV_SETUP (Linux 4.5 and later)
// or, failing that, the older uinput_user_dev write, then UI_DEV_CREATE.
func mbridgeCreateMouse() (int, error) {
	fd, err := -1, error(os.ErrNotExist)
	for _, path := range mbridgeUinputPaths {
		if fd, err = syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0); err == nil {
			break
		}
	}
	if err != nil {
		return -1, err
	}
	fail := func(step string, err error) (int, error) {
		_ = syscall.Close(fd)
		return -1, errors.New(step + ": " + err.Error())
	}
	steps := []struct {
		req uint32
		arg uintptr
	}{
		{evdevUISetEvBit, evdevEvKey},
		{evdevUISetKeyBit, evdevBtnLeft},
		{evdevUISetKeyBit, evdevBtnRight},
		{evdevUISetKeyBit, evdevBtnMiddle},
		{evdevUISetEvBit, evdevEvRel},
		{evdevUISetRelBit, evdevRelX},
		{evdevUISetRelBit, evdevRelY},
		{evdevUISetRelBit, evdevRelWheel},
	}
	for _, st := range steps {
		if err := mbridgeIoctl(fd, st.req, st.arg); err != nil {
			return fail("uinput bits", err)
		}
	}
	if err := mbridgeIoctlBuf(fd, evdevUIDevSetup, evdevSetupBytes(mbridgeMouseName, evdevBusVirtual, 0, 0, 1)); err != nil {
		// A kernel before 4.5 has no UI_DEV_SETUP: it reads the description
		// from a write instead.
		if _, werr := syscall.Write(fd, evdevUserDevBytes(mbridgeMouseName, evdevBusVirtual, 0, 0, 1)); werr != nil {
			return fail("uinput setup", werr)
		}
	}
	if err := mbridgeIoctl(fd, evdevUIDevCreate, 0); err != nil {
		return fail("uinput create", err)
	}
	return fd, nil
}

// mbridgeIoctl sends a request whose argument travels by value (EVIOCGRAB,
// UI_SET_*BIT, UI_DEV_CREATE / DESTROY).
func mbridgeIoctl(fd int, req uint32, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(req), arg); e != 0 {
		return e
	}
	return nil
}

// mbridgeIoctlBuf sends a request whose argument is a buffer the kernel reads
// or fills (EVIOCGABS, EVIOCGKEY, UI_DEV_SETUP).
func mbridgeIoctlBuf(fd int, req uint32, buf []byte) error {
	if len(buf) == 0 {
		return syscall.EINVAL
	}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
		return e
	}
	return nil
}
