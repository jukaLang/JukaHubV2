//go:build linux

package main

// lightgun_linux.go — the light gun's mouse on a Linux handheld.
//
// lightgunPrepare makes the mouse before the game starts (mbridgeCreateMouse,
// the games mouse's uinput device), because an SDL that looks for input
// devices only when it starts would never see one made later. A goroutine
// then waits, quiet, for the game's process (lightgunSessionLive); a launch
// that fails after the prepare never sends that, and the goroutine removes
// the mouse by itself after lightgunStartWait.
//
// Once live it reads the built-in pad's evdev node beside the game — opened
// read-only and never grabbed, so the game keeps every button — and writes
// the mouse's events at 125 Hz (lightgun_aim.go). It writes nothing while
// JukaHub has the screen over the game (a ringing alarm, the in-game menu, the
// assistant: those would see the mouse too), and anchors the pointer again
// when the game is back. An anchor also runs 3 seconds after the start (the
// crosshair starts in the middle) and, for Point, again at 12 seconds —
// RetroArch's SDL may open the mouse late, and an anchor before that is lost
// — and whenever the spare R shoulder or R3 is pressed (lightgun_aim.go).
//
// The game ending (lightgunSessionStop from the exit hook), a new launch, and
// JukaHub quitting (a Cleanup hook) remove the mouse; JukaHub dying outright
// removes it too, because the device belongs to its open file.

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// lightgunStartWait is how long a made gun waits for its game.
	lightgunStartWait = 30 * time.Second
	// lightgunEvery paces the writes while the game plays (125 Hz); a held
	// gun (JukaHub on screen) is looked at every lightgunIdleEvery.
	lightgunEvery     = 8 * time.Millisecond
	lightgunIdleEvery = 50 * time.Millisecond
	// lightgunStopGrace bounds how long a stop waits for the goroutine.
	lightgunStopGrace = 300 * time.Millisecond
)

// lightgunAnchorsAfterStart are when, after the game started, the pointer is
// anchored by itself.
var lightgunAnchorsAfterStart = []time.Duration{3 * time.Second, 12 * time.Second}

// lgSession is the one gun at a time.
var lgSession struct {
	mu      sync.Mutex
	running bool
	path    string
	stop    chan struct{}
	done    chan struct{}
	live    atomic.Bool
	active  atomic.Bool
}

// lightgunSessionSupported reports whether this device makes the gun's mouse:
// a TrimUI handheld (the firmware's pad, uinput, root), as the games mouse.
func lightgunSessionSupported() bool { return vcursorBridgeSupported() }

// lightgunActive reports a gun made for a launch, live or waiting: Mouse in
// games and apps stays off the pad meanwhile.
func lightgunActive() bool { return lgSession.active.Load() }

// lightgunSessionStart makes the mouse and starts the goroutine. Main thread.
func lightgunSessionStart(p lightgunSessionParams) error {
	if !lightgunSessionSupported() {
		return errors.New("the light gun's mouse is made on a TrimUI handheld only")
	}
	lightgunSessionStop("", "a new gun")
	fd, err := mbridgeCreateMouse(false)
	if err != nil {
		return err
	}
	s := &lightgunSess{p: p, padFD: -1, mouseFD: fd, buf: make([]byte, mbridgeReadEvents*evdevEventSize)}
	s.eng.p = p.Aim
	stop, done := make(chan struct{}), make(chan struct{})
	lgSession.mu.Lock()
	lgSession.running, lgSession.path, lgSession.stop, lgSession.done = true, p.Path, stop, done
	lgSession.live.Store(false)
	lgSession.active.Store(true)
	lgSession.mu.Unlock()
	logInfo("lightgun", "light gun mouse made for %s (%s aim, %dx%d)", p.Path, p.Aim.Set.Aim, p.Aim.W, p.Aim.H)
	go lightgunRun(s, stop, done)
	return nil
}

// lightgunSessionLive lets the gun for path move: its game has started.
func lightgunSessionLive(path string) {
	lgSession.mu.Lock()
	defer lgSession.mu.Unlock()
	if lgSession.running && lgSession.path == path {
		lgSession.live.Store(true)
	}
}

// lightgunSessionStop removes the gun for path ("" any) and waits a moment for
// it. Any thread.
func lightgunSessionStop(path, why string) {
	lgSession.mu.Lock()
	if !lgSession.running || (path != "" && lgSession.path != path) {
		lgSession.mu.Unlock()
		return
	}
	stop, done := lgSession.stop, lgSession.done
	select {
	case <-stop:
	default:
		close(stop)
	}
	lgSession.mu.Unlock()
	select {
	case <-done:
		logInfo("lightgun", "light gun removed: %s", why)
	case <-time.After(lightgunStopGrace):
		logWarn("lightgun", "the light gun did not stop within %s; the system removes it when JukaHub ends", lightgunStopGrace)
	}
}

// lightgunSess is one launch's gun: the pad, the mouse and the engine. The
// goroutine owns it.
type lightgunSess struct {
	p        lightgunSessionParams
	eng      lightgunEngine
	padFD    int
	mouseFD  int
	dropping bool
	buf      []byte
	wbuf     []byte
	events   []evdevEvent
	lastNote string
}

// lightgunHeldByJukaHub reports JukaHub on screen over the game: the gun
// writes nothing then.
func lightgunHeldByJukaHub() bool {
	return !gameScreenSuspended() || alarmFiringActive() || gaLink.held.Load() || gaLink.live.Load()
}

// lightgunRun is the goroutine. Whatever happens, the mouse is removed before
// it returns.
func lightgunRun(s *lightgunSess, stop <-chan struct{}, done chan<- struct{}) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("lightgun", "light gun stopped after a fault: %v", r)
		}
	}()
	defer func() {
		lgSession.mu.Lock()
		// A stop that gave up waiting (lightgunStopGrace) may have let the
		// next launch's gun start meanwhile: its state is not this one's to
		// clear, or that gun could no longer be stopped and would let Mouse
		// in games take the pad.
		if lgSession.done == done {
			lgSession.running = false
			lgSession.live.Store(false)
			lgSession.active.Store(false)
		}
		lgSession.mu.Unlock()
		close(done)
	}()
	defer s.close()
	made := time.Now()
	var liveAt, retryAt, anchorAt time.Time
	var anchors []time.Time
	paused, told := true, false
	for {
		select {
		case <-stop:
			return
		default:
		}
		now := time.Now()
		if !lgSession.live.Load() {
			if now.Sub(made) > lightgunStartWait {
				s.note("the game never started; the light gun is removed")
				return
			}
			time.Sleep(lightgunIdleEvery)
			continue
		}
		if liveAt.IsZero() {
			liveAt = now
			for i, d := range lightgunAnchorsAfterStart {
				// Move needs no known position: one anchor puts the
				// crosshair in the middle, and a later one would only yank
				// it there mid-game.
				if i > 0 && (s.p.Aim.Set.Aim != "point" || !s.p.Aim.Sticks) {
					break
				}
				anchors = append(anchors, now.Add(d))
			}
		}
		if s.padFD < 0 {
			if now.Before(retryAt) {
				time.Sleep(lightgunIdleEvery)
				continue
			}
			if err := s.openPad(now); err != nil {
				s.note("no pad to aim with: " + err.Error())
				retryAt = now.Add(mbridgeRetryEvery)
				continue
			}
		}
		if err := s.readPad(now); err != nil {
			s.note("lost the pad: " + err.Error())
			s.closePad()
			retryAt = now.Add(mbridgeRetryEvery)
			continue
		}
		if lightgunHeldByJukaHub() {
			if !paused {
				paused = true
				_ = s.write(s.eng.releaseAll(nil))
			}
			s.eng.recenter = false
			time.Sleep(lightgunIdleEvery)
			continue
		}
		if paused {
			paused = false
			s.eng.last = now
			if now.Sub(liveAt) > time.Second {
				// Back from JukaHub's screen: where the pointer is may have
				// changed under it.
				anchorAt = now.Add(500 * time.Millisecond)
			}
		}
		if s.eng.recenter {
			s.eng.recenter = false
			anchorAt = now
		}
		for len(anchors) > 0 && !now.Before(anchors[0]) {
			anchors = anchors[1:]
			anchorAt = now
		}
		if !anchorAt.IsZero() && !now.Before(anchorAt) {
			anchorAt = time.Time{}
			// The push in a report of its own: an SDL that adds a report's
			// moves up before it clamps would lose the corner otherwise.
			if err := s.write(s.eng.anchorEvents(nil)); err != nil {
				s.note("the mouse stopped: " + err.Error())
				return
			}
			s.eng.anchored()
			if !told {
				told = true
				playHaptic(hapticSuccessPulse)
			}
			time.Sleep(lightgunEvery)
			continue
		}
		out := s.eng.tick(now, nil)
		if len(out) > 0 {
			if err := s.write(out); err != nil {
				s.note("the mouse stopped: " + err.Error())
				return
			}
		}
		if s.eng.shot {
			s.eng.shot = false
			if s.p.Recoil {
				playHaptic(lightgunRecoilPulse)
			}
		}
		time.Sleep(lightgunEvery)
	}
}

// openPad finds the built-in pad and opens it read-only: the gun only
// watches it.
func (s *lightgunSess) openPad(now time.Time) error {
	data, err := os.ReadFile(evdevDevicesPath)
	if err != nil {
		return err
	}
	dev, ok := evdevPickPad(evdevParseDevices(string(data)))
	if !ok {
		return errors.New("no gamepad in " + evdevDevicesPath)
	}
	fd, err := syscall.Open("/dev/input/"+dev.Handler, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	s.padFD, s.dropping = fd, false
	s.sync()
	s.eng.last, s.eng.dpadAt = now, now
	logInfo("lightgun", "aiming with %q (/dev/input/%s)", dev.Name, dev.Handler)
	return nil
}

// sync reads the pad whole: its axes and the buttons held.
func (s *lightgunSess) sync() {
	info := make([]byte, evdevAbsInfoSize)
	for _, axis := range [...]uint16{evdevAbsX, evdevAbsY, evdevAbsZ, evdevAbsRX, evdevAbsRY, evdevAbsRZ, evdevAbsHat0X, evdevAbsHat0Y} {
		if mbridgeIoctlBuf(s.padFD, evdevGetAbs(axis), info) != nil {
			continue
		}
		if v, r, ok := evdevParseAbsInfo(info); ok {
			s.eng.setRange(axis, r, v)
		}
	}
	bitmap := make([]byte, evdevKeyBitmapSize)
	if mbridgeIoctlBuf(s.padFD, evdevGetKey(evdevKeyBitmapSize), bitmap) == nil {
		s.eng.setKeys(bitmap)
	}
}

// readPad feeds the engine everything the pad sent since the last read.
func (s *lightgunSess) readPad(now time.Time) error {
	for {
		n, err := syscall.Read(s.padFD, s.buf)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if errors.Is(err, syscall.EAGAIN) {
			return nil
		}
		if err != nil {
			return err
		}
		if n <= 0 {
			return errors.New("the pad stopped reporting")
		}
		s.events = evdevDecode(s.buf[:n], s.events[:0])
		for _, ev := range s.events {
			// The kernel dropped events: skip to the next report and read the
			// pad whole.
			if ev.Type == evdevEvSyn && ev.Code == evdevSynDropped {
				s.dropping = true
				continue
			}
			if s.dropping {
				if ev.Type == evdevEvSyn && ev.Code == evdevSynReport {
					s.dropping = false
					s.sync()
				}
				continue
			}
			s.eng.feed(ev, now)
		}
		if n < len(s.buf) {
			return nil
		}
	}
}

// write sends events and a report in one write().
func (s *lightgunSess) write(evs []evdevEvent) error {
	if len(evs) == 0 {
		return nil
	}
	if s.mouseFD < 0 {
		return syscall.EBADF
	}
	s.wbuf = s.wbuf[:0]
	for _, ev := range evs {
		s.wbuf = evdevEncode(s.wbuf, ev)
	}
	s.wbuf = evdevEncode(s.wbuf, evdevEvent{Type: evdevEvSyn, Code: evdevSynReport})
	for {
		_, err := syscall.Write(s.mouseFD, s.wbuf)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func (s *lightgunSess) closePad() {
	if s.padFD >= 0 {
		_ = syscall.Close(s.padFD)
		s.padFD = -1
	}
}

// close lets go of the mouse's buttons, removes it and closes the pad.
func (s *lightgunSess) close() {
	if s.mouseFD >= 0 {
		_ = s.write(s.eng.releaseAll(nil))
		_ = mbridgeIoctl(s.mouseFD, evdevUIDevDestroy, 0)
		_ = syscall.Close(s.mouseFD)
		s.mouseFD = -1
	}
	s.closePad()
}

// note logs a problem once, not on every retry.
func (s *lightgunSess) note(msg string) {
	if msg != s.lastNote {
		s.lastNote = msg
		logWarn("lightgun", "%s", msg)
	}
}
