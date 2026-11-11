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
//
// A Windows program (a Wine session through WestonPack) gets the same bridge
// differently (wineInputStart): the device is made before Weston starts, so
// Weston finds it, whatever the switch says; the pad is the mouse from the
// start; the device also types the keys of the Windows-app layout (layoutKey,
// vcursor_evdev.go); and the chord or Menu only lets go of the pad — the
// device stays until the session ends, because a compositor that looks for
// devices once would not find a new one.

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

// wineInput is the bridge lent to a Windows-app session (wineInputStart): at
// most one, from before its program starts until the session ends.
var wineInput struct {
	mu      sync.Mutex
	running bool
	stop    chan struct{}
	done    chan struct{}
	once    *sync.Once
}

func init() {
	// Quitting gives the pad back and removes the mouse even with a game
	// still running.
	Cleanup = append(Cleanup, mbridgeShutdown, wineInputShutdown)
}

// vcursorBridgeSupported reports whether this device can lend its pad to a
// game as a mouse: a TrimUI handheld (the firmware's pad, uinput, root).
func vcursorBridgeSupported() bool { return IsTrimuiDevice() }

// vcursorBridgeTick runs on the main thread, from the parked loop, while a
// game owns the screen. It mirrors the switch for the goroutine and starts
// one per game while the switch is on; the goroutine ends with the game.
func vcursorBridgeTick(config *Config) {
	// A Windows-app session has the pad already (wineInputStart).
	want := vcursorGamesEnabled(config) && vcursorBridgeSupported() && !wineInputActive()
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

// wineInputActive reports a Windows-app session holding the pad.
func wineInputActive() bool {
	wineInput.mu.Lock()
	defer wineInput.mu.Unlock()
	return wineInput.running
}

// wineInputStart lends the pad to a Windows-app session until the returned
// stop is called: the device ("JukaHub Mouse", with the keys of the
// Windows-app layout) is made and the pad grabbed now, before the program and
// its compositor start, so they find the device; Select + L1 or Menu lets go
// of the pad and Select + L1 takes it again. quit (may be nil) is called, on a
// goroutine of its own, when Select + Start are held to end the program. p is
// read on the main thread (mbridgeParamsFor) or is mbridgeDefaultParams: the
// goroutine reads no config. An error means the pad stays the program's.
func wineInputStart(p mbridgeParams, quit func()) (func(), error) {
	if !vcursorBridgeSupported() {
		return func() {}, errors.New("this device cannot lend its pad as a mouse")
	}
	p.keys = true
	wineInput.mu.Lock()
	defer wineInput.mu.Unlock()
	if wineInput.running {
		return func() {}, errors.New("the pad is already a mouse for another Windows program")
	}
	s := &mbridgeSession{padFD: -1, mouseFD: -1, buf: make([]byte, mbridgeReadEvents*evdevEventSize), wine: true, wantOn: true, quit: quit}
	s.eng.p = p
	now := time.Now()
	if err := s.openPad(now); err != nil {
		return func() {}, err
	}
	if err := s.mouseOn(now); err != nil {
		s.closeAll()
		return func() {}, err
	}
	stop, done, once := make(chan struct{}), make(chan struct{}), &sync.Once{}
	wineInput.running, wineInput.stop, wineInput.done, wineInput.once = true, stop, done, once
	go mbridgeRunWine(s, stop, done)
	return func() { wineInputStop(stop, done, once) }, nil
}

// wineInputStop ends a Windows-app session's bridge and waits a moment for the
// pad to be given back; the kernel lets go of it with JukaHub anyway.
func wineInputStop(stop chan struct{}, done chan struct{}, once *sync.Once) {
	once.Do(func() { close(stop) })
	select {
	case <-done:
	case <-time.After(2 * mbridgeExitGrace):
		logWarn("vcursor", "the Windows-app mouse did not stop within %s", 2*mbridgeExitGrace)
	}
}

// wineInputShutdown is the Cleanup hook for a session still running when
// JukaHub quits.
func wineInputShutdown() {
	wineInput.mu.Lock()
	running, stop, done, once := wineInput.running, wineInput.stop, wineInput.done, wineInput.once
	wineInput.mu.Unlock()
	if running && once != nil {
		wineInputStop(stop, done, once)
	}
}

// mbridgeRunWine is a Windows-app session's goroutine: the pad stays the
// mouse until the session ends (stop), the user hands it back (the chord or
// Menu, and the chord again takes it), or an alarm rings — JukaHub then needs
// the pad, and the mouse comes back once the alarm is done.
func mbridgeRunWine(s *mbridgeSession, stop <-chan struct{}, done chan<- struct{}) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("vcursor", "Windows-app mouse stopped after a fault: %v", r)
		}
	}()
	defer func() {
		wineInput.mu.Lock()
		wineInput.running = false
		wineInput.mu.Unlock()
		close(done)
	}()
	defer s.closeAll()
	var retryAt time.Time
	for {
		select {
		case <-stop:
			return
		default:
		}
		now := time.Now()
		if alarmFiringActive() {
			// The pad is let go and closed (the device stays): the presses
			// that answer the alarm must not reach the program afterwards as
			// clicks. It is opened afresh once the alarm is done.
			if s.padFD >= 0 {
				s.close()
			}
			time.Sleep(4 * mbridgeWatchEvery)
			continue
		}
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
		if s.wantOn && !s.eng.on {
			// Back after an alarm or a pad that went away and came back. A
			// failure is buzzed once; the chord tries again.
			if err := s.mouseOn(now); err != nil {
				s.wantOn = false
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

// mbridgeSession is one game's bridge: the pad, the mouse and the engine
// between them. The goroutine owns it. wine marks a Windows-app session
// (wineInputStart): its device stays between toggles, wantOn is whether the
// user wants the pad to be the mouse, and quit ends the program.
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
	wine     bool
	wantOn   bool
	quit     func()
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
			if s.wine {
				// A device that cannot be written to is gone for good.
				s.destroyMouse()
				s.wantOn = false
			}
			return nil
		}
	}
	if s.eng.quit {
		// Select + Start held in a Windows program: end it. quit signals
		// and waits, so it runs beside the bridge.
		s.eng.quit = false
		if s.quit != nil {
			playHaptic(hapticBackPulse)
			logInfo("vcursor", "Select + Start held: ending the Windows program")
			go s.quit()
		}
	}
	switch {
	case s.eng.on && (toggle || off):
		s.wantOn = false
		s.mouseOff(now, true)
	case toggle:
		s.wantOn = true
		_ = s.mouseOn(now)
	}
	return nil
}

// mouseOn lends the pad to the game as a mouse: the steps in the file comment.
// A Windows-app session keeps its device between toggles, so only the grab is
// taken again. A failure is buzzed and logged (fail) and returned.
func (s *mbridgeSession) mouseOn(now time.Time) error {
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
		return err
	}
	s.grabbed = true
	if s.mouseFD < 0 {
		fd, err := mbridgeCreateMouse(s.eng.p.keys)
		if err != nil {
			_ = mbridgeIoctl(s.padFD, evdevGrab, 0)
			s.grabbed = false
			s.fail("could not create the mouse", err)
			return err
		}
		s.mouseFD = fd
	}
	s.eng.setOn(true, now)
	playHaptic(hapticSuccessPulse)
	logInfo("vcursor", "games mouse on: %q is the mouse until Select + L1 or Menu", s.padName)
	return nil
}

// mouseOff gives the pad back: the mouse's held buttons (and keys) are let
// go, the mouse is removed — kept, for a Windows-app session — and the grab
// released. pulse buzzes when the mouse was on.
func (s *mbridgeSession) mouseOff(now time.Time, pulse bool) {
	if s.mouseFD >= 0 {
		if ups := s.eng.letGo(nil); len(ups) > 0 {
			ups = append(ups, evdevEvent{Type: evdevEvSyn, Code: evdevSynReport})
			_ = s.writeEvents(s.mouseFD, ups)
		}
		if !s.wine {
			s.destroyMouse()
		}
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

// destroyMouse removes the virtual device.
func (s *mbridgeSession) destroyMouse() {
	if s.mouseFD < 0 {
		return
	}
	_ = mbridgeIoctl(s.mouseFD, evdevUIDevDestroy, 0)
	_ = syscall.Close(s.mouseFD)
	s.mouseFD = -1
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

// closeAll is close plus the device a Windows-app session kept.
func (s *mbridgeSession) closeAll() {
	s.close()
	s.destroyMouse()
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
// or, failing that, the older uinput_user_dev write, then UI_DEV_CREATE. keys
// adds every keyboard key, as PortMaster's gptokeyb does for its device: a
// device with the whole first row is what udev tags as a keyboard, and only
// a tagged keyboard's keys reach a Weston session.
func mbridgeCreateMouse(keys bool) (int, error) {
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
	if keys {
		for code := uintptr(1); code <= evdevKeyLast; code++ {
			if err := mbridgeIoctl(fd, evdevUISetKeyBit, code); err != nil {
				return fail("uinput keys", err)
			}
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
