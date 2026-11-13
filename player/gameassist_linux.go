//go:build linux

package main

// gameassist_linux.go — the game assistant's pad watcher, on a Linux handheld.
//
// While a game JukaHub launched owns the screen and Settings → Voice & AI →
// Game assistant is on, a goroutine reads the built-in pad's evdev node beside
// the game, the way the games mouse does (vcursor_bridge_linux.go, whose
// device discovery, decoding and ioctls it shares). Holding Select + R2 for
// 0.7 s calls Juka:
//
//  1. every button the game saw go down is released and every stick put back
//     at rest, by writing those events into the pad (the same events reach
//     SDL in JukaHub, so neither is left with Select or R2 held);
//  2. the pad is grabbed (EVIOCGRAB): from then on only this goroutine reads
//     it, so nothing pressed while Juka listens reaches the paused game;
//  3. the parked main loop is told (gaLink.request); it pauses the game and
//     draws the assistant over it (gameassist.go), and the presses that matter
//     there — A, B, R2, Menu and the chord again — go to it through a small
//     queue, because SDL in JukaHub sees nothing of a grabbed pad either.
//
// When the main loop is done (B, Menu, the chord, an alarm, the cap) it asks
// for the pad back. The grab is let go once nothing is held — so the game is
// not handed the end of a press it never saw begin, or R2 sliding back up
// through "pressed" — or after a second whatever is still held. The goroutine
// ends with the game, the switch, an alarm or quitting (gaPadStop); if
// JukaHub dies the kernel lets go of the grab with its open files.
//
// Select + R2 because the others are taken: Select + L1 is the games mouse,
// Select + Start quits, and Menu, alone or with a button, is RetroArch's own.
// While the pad is the games mouse the bridge holds it, so the chord is not
// seen then.
//
// The same watcher serves the in-game menu (Settings → Controls → In-game
// menu, on until turned off): Select + L2 held for half a second takes the
// pad the same way and asks for the menu instead — resume, ask Juka, quit to
// JukaHub — over any game or emulator (RetroArch, PPSSPP, a port), since it
// never asks the game for anything. While the pad is held for either, the
// D-pad's up and down go to the main loop too, and either chord pressed again
// (no hold) is passed on: the overlay decides what it means there.
//
// It also reads Menu held alone, without taking the pad, for Hold Menu:
// screenshot (menushot.go): the game keeps Menu (its menu still opens), and
// the screen read at the press is saved when the hold lands.
//
// Before the overlay is up the feedback is the motor: a firm pulse when the
// pad is taken, a long one when it could not be.
//
// A game paused by the assistant would stay stopped for good if JukaHub were
// killed outright (the out-of-memory killer, a held power key): the paused
// processes are written down in the temp directory before the stop
// (gaPausedNote), and the next start asks one it finds still there to quit
// (gaRecoverPaused).

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// gaEvdevBtnTR2 is BTN_TR2: R2 on a pad whose triggers are buttons. The
	// Smart Pro's are axes (ABS_RZ); both are read.
	gaEvdevBtnTR2 = 0x139
	// gaEvdevBtnTL2 is BTN_TL2: L2 on a pad whose triggers are buttons (the
	// Smart Pro's is ABS_Z).
	gaEvdevBtnTL2 = 0x138
	// gaEvdevBtnDpadUp / gaEvdevBtnDpadDown are a D-pad sent as buttons; the
	// TrimUI pads send a hat (ABS_HAT0Y), and both are read.
	gaEvdevBtnDpadUp   = 0x220
	gaEvdevBtnDpadDown = 0x221
	// gaChordHold is how long Select + R2 are held: the games mouse's hold, so
	// the two chords feel the same.
	gaChordHold = vcursorChordHold
	// gaMenuHold is how long Select + L2 are held for the in-game menu:
	// shorter, since leaving a game should be quick, and still a hold, so a
	// game's own Select + L2 tap does not pause it.
	gaMenuHold = 500 * time.Millisecond
	// gaChordCloseHold is the chord's hold while the overlay is up: pressing
	// Select + R2 again plays on at once (Juka, 2026-11-11), with just enough
	// of a hold that two buttons landing a few milliseconds apart count.
	gaChordCloseHold = 40 * time.Millisecond
	// gaHeldEvery paces the reads while the pad is held, so the overlay's
	// buttons answer at once; watching for the chord uses the bridge's pace.
	gaHeldEvery = 16 * time.Millisecond
	// gaGiveBackWait bounds the wait for an idle pad before the grab goes.
	gaGiveBackWait = time.Second
	// gaOrphanWait is how long the pad stays held with no overlay up: the main
	// loop never took the request (an alarm began meanwhile).
	gaOrphanWait = 2 * time.Second
	// gaRecoverGrace is how long a stopped game found at start gets to quit
	// before it is killed.
	gaRecoverGrace = 3 * time.Second
)

// gaPad is the watcher's lifetime: one goroutine per game, started from the
// parked main loop (gaPadSync) and ended by the game ending, the switch going
// off, an alarm, or quitting.
var gaPad struct {
	mu      sync.Mutex
	running bool
	stop    chan struct{}
	done    chan struct{}
	// want mirrors the switches for the goroutine, which must not read the
	// config: want is either, wantAsk the assistant's chord (Select + R2),
	// wantMenu the in-game menu's (Select + L2).
	want     atomic.Bool
	wantAsk  atomic.Bool
	wantMenu atomic.Bool
	// wantShot: Menu held alone saves the game's screen (menushot.go).
	wantShot atomic.Bool
}

// gameAssistSupported reports whether the assistant can come up over a game
// here: a TrimUI handheld (its pad's evdev node, root, the framebuffer).
func gameAssistSupported() bool { return IsTrimuiDevice() }

// gaPadSync runs on the main thread, from the parked loop: it mirrors which
// chords are wanted — and the Menu hold for a screenshot — and starts one
// watcher per game while any is.
func gaPadSync(config *Config, ask, menu, shot bool) {
	want := ask || menu || shot
	if shot {
		gameShotSync(config)
	}
	gaPad.wantAsk.Store(ask)
	gaPad.wantMenu.Store(menu)
	gaPad.wantShot.Store(shot)
	gaPad.want.Store(want)
	if !want {
		return
	}
	gaPad.mu.Lock()
	if gaPad.running {
		gaPad.mu.Unlock()
		return
	}
	stop, done := make(chan struct{}), make(chan struct{})
	gaPad.running, gaPad.stop, gaPad.done = true, stop, done
	gaPad.mu.Unlock()
	// The goroutine buzzes the motor: it is found here, on the main thread,
	// so a first pulse never probes an SDL pad from the goroutine.
	hapticsAvailable()
	go gaPadRun(mbridgeParamsFor(config), stop, done)
}

// gaPadStop asks a running watcher to give the pad back and waits at most
// mbridgeExitGrace (the Cleanup hook).
func gaPadStop() {
	gaPad.want.Store(false)
	gaPad.mu.Lock()
	if !gaPad.running {
		gaPad.mu.Unlock()
		return
	}
	stop, done := gaPad.stop, gaPad.done
	select {
	case <-stop:
	default:
		close(stop)
	}
	gaPad.mu.Unlock()
	select {
	case <-done:
	case <-time.After(mbridgeExitGrace):
		logWarn("gameassist", "the pad watcher did not stop within %s; the system lets go of the pad when JukaHub ends", mbridgeExitGrace)
	}
}

// gaPadSession is one game's watcher. The goroutine owns it. eng keeps the
// pad's buttons and axes (its mouse half is never turned on) so the releases
// written before the grab cover whatever the game saw held.
type gaPadSession struct {
	eng      mbridgeEngine
	fd       int
	name     string
	canWrite bool
	dropping bool
	buf      []byte
	wbuf     []byte
	events   []evdevEvent
	lastNote string

	// The chords: Select + R2 (the assistant) and Select + L2 (the menu).
	ask  gaChord
	menu gaChord
	// R2: the trigger past half (with hysteresis), and R2 down by the trigger
	// or a button. r2Latched: R2 was held when the pad was taken, so its next
	// press counts only after a release. L2 the same, without the latch (L2
	// alone means nothing over the overlay).
	r2Axis    bool
	r2        bool
	r2Latched bool
	l2Axis    bool
	l2        bool

	// The pad held for the overlay: since when, until when presses are the
	// firmware re-sending buttons still held (the bridge's quiet window), and
	// since when the main loop wants it back.
	held       bool
	heldAt     time.Time
	quiet      time.Time
	giveBackAt time.Time
	// Menu acts when it comes up, unless a volume key joined it.
	menuDown   bool
	menuVolume int64

	// Menu held alone while the game plays: a screenshot (menushot.go).
	// shotAt is when Menu went down (zero: not down, or the hold was spoiled
	// by another button or a volume key), shotArmed that the hold lasted (the
	// release saves it), shotVolume the volume-key count then, shotFrame the
	// screen read at the press — before the game's own menu, which most
	// emulators open on Menu, was drawn — and shotBuf the buffer it is read
	// into, reused from press to press.
	shotAt     time.Time
	shotArmed  bool
	shotVolume int64
	shotFrame  gameShotFrame
	shotBuf    []byte
}

// gaPadRun is the goroutine: it watches for the chord while the game runs and
// holds the pad for the overlay. Whatever happens, the pad is given back
// before it returns.
func gaPadRun(p mbridgeParams, stop <-chan struct{}, done chan<- struct{}) {
	s := &gaPadSession{fd: -1, buf: make([]byte, mbridgeReadEvents*evdevEventSize)}
	s.eng.p = p
	// A "Hey Juka" from an earlier game is not this one's.
	gaLink.wake.Store(false)
	// Run last to first: the pad is given back, the watcher is marked stopped,
	// and a fault anywhere is logged instead of taking JukaHub down with it.
	defer func() {
		if r := recover(); r != nil {
			logWarn("gameassist", "the pad watcher stopped after a fault: %v", r)
		}
	}()
	defer func() {
		gaPad.mu.Lock()
		gaPad.running = false
		gaPad.mu.Unlock()
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
		now := time.Now()
		// Nothing to watch for: the game ended, the switch went off, or an
		// alarm rings and needs the pad (the parked loop starts a new watcher
		// when the game has the screen again). A held pad stays until the main
		// loop asks for it back.
		if !s.held && (!gameScreenSuspended() || !gaPad.want.Load() || alarmFiringActive()) {
			return
		}
		if s.fd < 0 {
			if now.Before(retryAt) {
				time.Sleep(4 * mbridgeWatchEvery)
				continue
			}
			if err := s.open(now); err != nil {
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
		if s.held {
			time.Sleep(gaHeldEvery)
		} else {
			time.Sleep(mbridgeWatchEvery)
		}
	}
}

// open finds the built-in pad and opens it — read-write when it may, so the
// releases can be written into it — then reads what is held.
func (s *gaPadSession) open(now time.Time) error {
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
	s.fd, s.name, s.dropping = fd, dev.Name, false
	s.resync(now)
	// A chord already held when the watch starts does not fire from it.
	s.resetChords()
	logInfo("gameassist", "watching %q (%s) for Select + R2 and Select + L2", dev.Name, path)
	return nil
}

// resetChords starts both chords over: one still held must be let go before
// it counts again.
func (s *gaPadSession) resetChords() {
	sel := s.eng.key(evdevBtnSelect)
	s.ask.reset(sel && s.r2)
	s.menu.reset(sel && s.l2)
}

// resync reads the pad whole — the buttons held, each axis's range and value
// — at open and after the kernel dropped events.
func (s *gaPadSession) resync(now time.Time) {
	bitmap := make([]byte, evdevKeyBitmapSize)
	if mbridgeIoctlBuf(s.fd, evdevGetKey(evdevKeyBitmapSize), bitmap) == nil {
		s.eng.setKeys(bitmap)
	}
	if !s.eng.key(evdevBtnMode) {
		// Menu's release was among the dropped events.
		s.shotCancel()
	}
	info := make([]byte, evdevAbsInfoSize)
	for _, axis := range [...]uint16{evdevAbsX, evdevAbsY, evdevAbsZ, evdevAbsRX, evdevAbsRY, evdevAbsRZ, evdevAbsHat0X, evdevAbsHat0Y} {
		if mbridgeIoctlBuf(s.fd, evdevGetAbs(axis), info) != nil {
			continue
		}
		if v, r, ok := evdevParseAbsInfo(info); ok {
			s.eng.setRange(axis, r, v)
		}
	}
	s.r2Axis = s.eng.ranges[evdevAbsRZ].trigger(s.eng.abs[evdevAbsRZ]) >= mbridgeTrigDown
	s.r2 = s.r2Axis || s.eng.key(gaEvdevBtnTR2)
	if !s.r2 {
		s.r2Latched = false
	}
	s.l2Axis = s.eng.ranges[evdevAbsZ].trigger(s.eng.abs[evdevAbsZ]) >= mbridgeTrigDown
	s.l2 = s.l2Axis || s.eng.key(gaEvdevBtnTL2)
	s.chord(now)
}

// step reads what the pad sent since the last step, follows the buttons, and
// takes the pad on the chord or runs the held pad's upkeep.
func (s *gaPadSession) step(now time.Time) error {
	for {
		n, err := syscall.Read(s.fd, s.buf)
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
			// The kernel dropped events: what follows up to the next report is
			// incomplete, so the pad is read whole instead.
			if ev.Type == evdevEvSyn && ev.Code == evdevSynDropped {
				s.dropping = true
				continue
			}
			if s.dropping {
				if ev.Type == evdevEvSyn && ev.Code == evdevSynReport {
					s.dropping = false
					s.resync(now)
				}
				continue
			}
			s.feed(ev, now)
		}
		if n < len(s.buf) {
			break
		}
	}
	switch {
	case s.held:
		s.heldTick(now)
	case gaLink.wake.Swap(false):
		// "Hey Juka" (wakeword.go): the assistant as if the chord were held.
		if gaPad.wantAsk.Load() {
			s.take(now, gaModeAsk, true)
		}
	case gaPad.wantMenu.Load() && s.menu.due(now, gaMenuHold):
		s.take(now, gaModeMenu, false)
	case gaPad.wantAsk.Load() && s.ask.due(now, gaChordHold):
		s.take(now, gaModeAsk, false)
	case !s.shotAt.IsZero() && !s.shotArmed && now.Sub(s.shotAt) >= menuShotHold:
		s.shotArm()
	}
	return nil
}

// feed follows one event: the button or axis it moves, the chord, and while
// the pad is held the presses the overlay acts on.
func (s *gaPadSession) feed(ev evdevEvent, now time.Time) {
	switch ev.Type {
	case evdevEvKey:
		if ev.Code == gaEvdevBtnDpadUp || ev.Code == gaEvdevBtnDpadDown {
			// A D-pad sent as buttons: outside the pad's button range, read
			// only to move the menu's highlight while the pad is held.
			if s.held && ev.Value == 1 && !now.Before(s.quiet) {
				if ev.Code == gaEvdevBtnDpadUp {
					gaPush(gaBtnUp)
				} else {
					gaPush(gaBtnDown)
				}
			}
			if ev.Value == 1 {
				// Menu + the D-pad is a hotkey, no screenshot.
				s.shotCancel()
			}
			return
		}
		i := int(ev.Code) - mbridgeKeyBase
		if i < 0 || i >= len(s.eng.keys) || ev.Value == 2 {
			// Not one of the pad's buttons, or a key repeat.
			return
		}
		down := ev.Value != 0
		was := s.eng.keys[i]
		s.eng.keys[i] = down
		switch ev.Code {
		case gaEvdevBtnTR2:
			s.setR2(now)
		case gaEvdevBtnTL2:
			s.setL2(now)
		case evdevBtnSelect:
			s.chord(now)
		}
		if !s.held && down != was {
			s.shotKey(ev.Code, down)
		}
		if !s.held || down == was {
			return
		}
		if down {
			s.press(ev.Code, now)
			return
		}
		if ev.Code == evdevBtnMode && s.menuDown {
			// Menu acts when it comes up, the way the TrimUI's own launchers
			// read it: a volume key pressed meanwhile made it the firmware's
			// Menu + volume brightness chord.
			s.menuDown = false
			if orbVolumeKeys.Load() == s.menuVolume {
				gaPush(gaBtnMenu)
			}
		}
	case evdevEvAbs:
		if int(ev.Code) >= len(s.eng.abs) {
			return
		}
		prev := s.eng.abs[ev.Code]
		s.eng.abs[ev.Code] = ev.Value
		switch ev.Code {
		case evdevAbsRZ:
			s.setR2(now)
		case evdevAbsZ:
			s.setL2(now)
		case evdevAbsHat0X:
			if ev.Value != 0 {
				s.shotCancel() // Menu + the D-pad: a hotkey
			}
		case evdevAbsHat0Y:
			if ev.Value != 0 {
				s.shotCancel()
			}
			// The D-pad leaving the middle, up or down, while the pad is held:
			// the menu's highlight moves once per press.
			if d := evdevHatDir(ev.Value); s.held && d != 0 && d != evdevHatDir(prev) && !now.Before(s.quiet) {
				if d < 0 {
					gaPush(gaBtnUp)
				} else {
					gaPush(gaBtnDown)
				}
			}
		}
	}
}

// press is a button going down while the pad is held.
func (s *gaPadSession) press(code uint16, now time.Time) {
	if now.Before(s.quiet) {
		return
	}
	switch code {
	case s.eng.p.codeA:
		gaPush(gaBtnA)
	case s.eng.p.codeB:
		gaPush(gaBtnB)
	case evdevBtnMode:
		s.menuDown, s.menuVolume = true, orbVolumeKeys.Load()
	}
}

// setR2 follows R2 — the trigger past half, with the bridge's hysteresis, or
// its button — and, while the pad is held, its press: R2 alone sends what was
// said (with Select it is the chord, timed by heldTick).
func (s *gaPadSession) setR2(now time.Time) {
	v := s.eng.ranges[evdevAbsRZ].trigger(s.eng.abs[evdevAbsRZ])
	switch {
	case !s.r2Axis && v >= mbridgeTrigDown:
		s.r2Axis = true
	case s.r2Axis && v < mbridgeTrigUp:
		s.r2Axis = false
	}
	down := s.r2Axis || s.eng.key(gaEvdevBtnTR2)
	was := s.r2
	s.r2 = down
	if down && !was {
		s.shotCancel() // Menu + R2: a hotkey
	}
	if !down {
		s.r2Latched = false
	}
	if down == was {
		return
	}
	s.chord(now)
	if s.held && down && !s.r2Latched && !s.eng.key(evdevBtnSelect) && !now.Before(s.quiet) {
		gaPush(gaBtnR2)
	}
}

// setL2 follows L2 — the trigger past half, with the bridge's hysteresis, or
// its button — for the Select + L2 chord.
func (s *gaPadSession) setL2(now time.Time) {
	v := s.eng.ranges[evdevAbsZ].trigger(s.eng.abs[evdevAbsZ])
	switch {
	case !s.l2Axis && v >= mbridgeTrigDown:
		s.l2Axis = true
	case s.l2Axis && v < mbridgeTrigUp:
		s.l2Axis = false
	}
	down := s.l2Axis || s.eng.key(gaEvdevBtnTL2)
	if down == s.l2 {
		return
	}
	s.l2 = down
	if down {
		s.shotCancel() // Menu + L2: a hotkey
	}
	s.chord(now)
}

// shotKey follows the pad's buttons for Hold Menu: screenshot while the game
// has the pad. Menu going down alone starts the hold and reads the screen at
// once; any other button going down ends it; Menu coming up after the hold
// armed it (shotArm) saves the picture.
func (s *gaPadSession) shotKey(code uint16, down bool) {
	if code != evdevBtnMode {
		if down {
			s.shotCancel()
		}
		return
	}
	if !down {
		if s.shotArmed {
			s.shotSave()
		}
		s.shotCancel()
		return
	}
	s.shotCancel()
	if !gaPad.wantShot.Load() || s.shotOtherHeld() {
		// Switched off, or Menu joining a held button (a hotkey).
		return
	}
	s.shotAt, s.shotVolume = time.Now(), orbVolumeKeys.Load()
	fr, err := gameShotGrab(s.shotBuf)
	if err != nil {
		// Read again when it is saved (shotSave); the game's menu may be in
		// that picture, but it is better than none.
		s.note("screenshot: could not read the screen: " + err.Error())
		s.shotFrame = gameShotFrame{}
		return
	}
	s.shotBuf, s.shotFrame = fr.raw, fr
}

// shotCancel ends a hold: nothing is saved. The buffer is kept for the next.
func (s *gaPadSession) shotCancel() {
	s.shotAt, s.shotArmed = time.Time{}, false
}

// shotOtherHeld reports a button other than Menu held, a trigger past half,
// or the D-pad away from the middle.
func (s *gaPadSession) shotOtherHeld() bool {
	for i, k := range s.eng.keys {
		if k && i != int(evdevBtnMode)-mbridgeKeyBase {
			return true
		}
	}
	return s.r2 || s.l2 || s.eng.abs[evdevAbsHat0X] != 0 || s.eng.abs[evdevAbsHat0Y] != 0
}

// shotStillAlone reports Menu still down alone, with no volume key pressed
// since it went down (the firmware's Menu + volume brightness).
func (s *gaPadSession) shotStillAlone() bool {
	return gaPad.wantShot.Load() && s.eng.key(evdevBtnMode) && !s.shotOtherHeld() &&
		orbVolumeKeys.Load() == s.shotVolume
}

// shotArm is the hold reaching menuShotHold: a tick of the motor says Menu
// may be let go. The picture is saved on the release, so a volume key
// pressed late in a long hold (brightness) still cancels it.
func (s *gaPadSession) shotArm() {
	if !s.shotStillAlone() {
		s.shotCancel()
		return
	}
	s.shotArmed = true
	playHaptic(hapticTogglePulse)
}

// shotSave is Menu let go after an armed hold: the picture read at the press
// goes to a goroutine to be written (gameShotSave buzzes when it is).
func (s *gaPadSession) shotSave() {
	if !gaPad.wantShot.Load() || s.shotOtherHeld() || orbVolumeKeys.Load() != s.shotVolume {
		return
	}
	fr := s.shotFrame
	// The goroutine owns the picture's buffer now; the next press reads into
	// a new one.
	s.shotFrame, s.shotBuf = gameShotFrame{}, nil
	if fr.raw == nil {
		var err error
		if fr, err = gameShotGrab(nil); err != nil {
			s.note("screenshot: could not read the screen: " + err.Error())
			playHaptic(hapticErrorPulse)
			return
		}
	}
	logInfo("gameassist", "Menu held in %q: saving the game's screen", s.name)
	go gameShotSave(fr, time.Now())
}

// gaChord is one chord's hold: since when Select and its trigger have both
// been down, and whether this hold already fired (both must come up before
// the next one counts).
type gaChord struct {
	since time.Time
	fired bool
}

// follow takes the chord's two buttons as they are now: both down starts the
// clock (once per hold), either coming up stops it, and both up let the next
// hold fire.
func (c *gaChord) follow(sel, trig bool, now time.Time) {
	switch {
	case !sel && !trig:
		c.fired, c.since = false, time.Time{}
	case !sel || !trig:
		c.since = time.Time{}
	case c.since.IsZero() && !c.fired:
		c.since = now
	}
}

// due reports a hold that has just lasted hold. It fires once.
func (c *gaChord) due(now time.Time, hold time.Duration) bool {
	if c.fired || c.since.IsZero() || now.Sub(c.since) < hold {
		return false
	}
	c.fired, c.since = true, time.Time{}
	return true
}

// reset stops the clock; held: the chord is down now, so it must be let go
// before it counts again.
func (c *gaChord) reset(held bool) {
	c.fired, c.since = held, time.Time{}
}

// chord follows both holds: Select + R2 and Select + L2.
func (s *gaPadSession) chord(now time.Time) {
	sel := s.eng.key(evdevBtnSelect)
	s.ask.follow(sel, s.r2, now)
	s.menu.follow(sel, s.l2, now)
}

// gaChordKeys is the trigger of a mode's chord: its button and its axis.
func gaChordKeys(mode gaMode) (btn, axis uint16) {
	if mode == gaModeMenu {
		return gaEvdevBtnTL2, evdevAbsZ
	}
	return gaEvdevBtnTR2, evdevAbsRZ
}

// chordHeldNow asks the pad itself whether Select and the mode's trigger are
// down: the events may be stale (a release that went to the games mouse while
// it held the pad).
func (s *gaPadSession) chordHeldNow(mode gaMode) bool {
	bitmap := make([]byte, evdevKeyBitmapSize)
	if mbridgeIoctlBuf(s.fd, evdevGetKey(evdevKeyBitmapSize), bitmap) != nil {
		// The pad cannot be asked: the events are all there is.
		return true
	}
	if !evdevKeyBit(bitmap, evdevBtnSelect) {
		return false
	}
	btn, axis := gaChordKeys(mode)
	if evdevKeyBit(bitmap, btn) {
		return true
	}
	info := make([]byte, evdevAbsInfoSize)
	if mbridgeIoctlBuf(s.fd, evdevGetAbs(axis), info) != nil {
		return false
	}
	v, r, ok := evdevParseAbsInfo(info)
	return ok && r.trigger(v) >= mbridgeTrigUp
}

// take holds the pad for the overlay — the assistant or the menu, by mode:
// the steps in the file comment. byVoice: "Hey Juka" asked, no chord is held.
func (s *gaPadSession) take(now time.Time, mode gaMode, byVoice bool) {
	if !byVoice && !s.chordHeldNow(mode) {
		// The events said both were down and the pad says not: a release went
		// missing. It is read whole, and a real hold is waited for.
		s.resync(now)
		s.resetChords()
		return
	}
	injected := false
	if s.canWrite {
		rel := s.eng.releaseEvents(nil)
		injected = true
		if len(rel) > 0 {
			rel = append(rel, evdevEvent{Type: evdevEvSyn, Code: evdevSynReport})
			if err := s.write(rel); err != nil {
				injected = false
				logDebug("gameassist", "could not let go of the game's buttons: %v", err)
			}
		}
	}
	if err := mbridgeIoctl(s.fd, evdevGrab, 1); err != nil {
		s.note("could not take the pad from the game: " + err.Error())
		playHaptic(hapticErrorPulse)
		if !injected {
			s.resync(now)
		}
		return
	}
	// The releases just written came back to this reader too; they are no
	// presses, and nothing read before the grab matters now.
	s.drain()
	s.held, s.heldAt, s.quiet, s.giveBackAt = true, now, now.Add(mbridgeQuiet), time.Time{}
	s.menuDown = false
	s.shotCancel()
	// R2 still held from the Select + R2 hold must come up before it sends;
	// after Select + L2 it was never down, so its first press counts.
	s.r2Latched = s.r2
	if injected {
		s.r2Axis, s.r2 = false, false
		s.l2Axis, s.l2 = false, false
	} else {
		s.resync(now)
	}
	// The chord that opened it is held still: neither counts again until
	// let go. A voice opening held no chord, so the next one counts at once
	// (with fired set, only an all-released event would clear it).
	s.ask.reset(!byVoice)
	s.menu.reset(!byVoice)
	gaLink.release.Store(false)
	gaLink.lost.Store(false)
	gaLink.mode.Store(int32(mode))
	gaLink.held.Store(true)
	gaLink.request.Store(true)
	playHaptic(hapticSuccessPulse)
	switch {
	case mode == gaModeMenu:
		logInfo("gameassist", "Select + L2 held: %q is JukaHub's while the in-game menu is up", s.name)
	case byVoice:
		logInfo("gameassist", "Hey Juka: %q is JukaHub's while Juka listens", s.name)
	default:
		logInfo("gameassist", "Select + R2 held: %q is JukaHub's while Juka listens", s.name)
	}
}

// gaWakeAsk asks the pad watcher to take the pad for the assistant, as the
// chord does, because "Hey Juka" was heard (wakeword.go). false: no watcher
// is running for the assistant, or it holds the pad already. Main thread.
func gaWakeAsk() bool {
	gaPad.mu.Lock()
	running := gaPad.running
	gaPad.mu.Unlock()
	if !running || !gaPad.wantAsk.Load() || gaLink.held.Load() || gaLink.live.Load() {
		return false
	}
	gaLink.wake.Store(true)
	return true
}

// heldTick is the held pad's upkeep: either chord again, the give-back the
// main loop asked for, and the pad given back when no overlay came up for it.
func (s *gaPadSession) heldTick(now time.Time) {
	// The firmware re-sending buttons still held (the quiet window) is no
	// press of a chord.
	if !now.Before(s.quiet) {
		if s.ask.due(now, gaChordCloseHold) {
			gaPush(gaBtnChord)
		}
		if s.menu.due(now, gaChordCloseHold) {
			gaPush(gaBtnMenuChord)
		}
	}
	switch {
	case gaLink.release.Load():
		if s.giveBackAt.IsZero() {
			s.giveBackAt = now
		}
		if s.idle() || now.Sub(s.giveBackAt) >= gaGiveBackWait {
			s.giveBack()
		}
	case !gaLink.live.Load() && now.Sub(s.heldAt) >= gaOrphanWait:
		s.note("no overlay came up: giving the pad back")
		s.giveBack()
	case now.Sub(s.heldAt) >= gaOverlayCap+30*time.Second:
		s.note("the pad was held too long: giving it back")
		s.giveBack()
	}
}

// idle reports a pad with nothing held: no button, both triggers up.
func (s *gaPadSession) idle() bool {
	for _, k := range s.eng.keys {
		if k {
			return false
		}
	}
	return !s.r2 && !s.l2 && s.eng.ranges[evdevAbsZ].trigger(s.eng.abs[evdevAbsZ]) < mbridgeTrigUp &&
		s.eng.abs[evdevAbsHat0X] == 0 && s.eng.abs[evdevAbsHat0Y] == 0
}

// giveBack lets go of the grab: the pad is the game's again.
func (s *gaPadSession) giveBack() {
	if s.fd >= 0 {
		_ = mbridgeIoctl(s.fd, evdevGrab, 0)
	}
	s.held, s.giveBackAt, s.menuDown = false, time.Time{}, false
	// A chord still held must be let go before it counts again.
	s.resetChords()
	gaLink.held.Store(false)
	gaLink.release.Store(false)
	gaLink.request.Store(false)
	playHaptic(hapticBackPulse)
	logInfo("gameassist", "the pad is the game's again")
}

// drain throws away what is waiting to be read.
func (s *gaPadSession) drain() {
	for i := 0; i < 64; i++ {
		n, err := syscall.Read(s.fd, s.buf)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil || n < len(s.buf) {
			return
		}
	}
}

// close gives everything back and closes the pad (closing the file lets go of
// a grab). A pad held for the overlay is reported lost, so the main loop does
// not wait on it.
func (s *gaPadSession) close() {
	if s.held {
		s.held = false
		gaLink.held.Store(false)
		gaLink.request.Store(false)
		gaLink.release.Store(false)
		gaLink.lost.Store(true)
	}
	if s.fd >= 0 {
		_ = syscall.Close(s.fd)
		s.fd = -1
	}
	s.eng = mbridgeEngine{p: s.eng.p}
	s.ask, s.menu = gaChord{}, gaChord{}
	s.r2Axis, s.r2, s.r2Latched = false, false, false
	s.l2Axis, s.l2 = false, false
	s.menuDown = false
	s.shotCancel()
	s.shotFrame, s.shotBuf = gameShotFrame{}, nil
}

// write writes events in one write(), the way the kernel reads them.
func (s *gaPadSession) write(evs []evdevEvent) error {
	if s.fd < 0 {
		return syscall.EBADF
	}
	s.wbuf = s.wbuf[:0]
	for _, ev := range evs {
		s.wbuf = evdevEncode(s.wbuf, ev)
	}
	for {
		_, err := syscall.Write(s.fd, s.wbuf)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// note logs a problem once, not on every retry.
func (s *gaPadSession) note(msg string) {
	if msg != s.lastNote {
		s.lastNote = msg
		logWarn("gameassist", "pad watcher: %s", msg)
	}
}

// ── A game left paused ───────────────────────────────────────────────────

// gaPausedProc is one paused process: its pid and the kernel's start time for
// it, so a pid reused since is never signalled.
type gaPausedProc struct {
	Pid   int    `json:"pid"`
	Start string `json:"start"`
}

// gaProcInfo is what /proc/<pid>/stat says about a process: its group and its
// start time (clock ticks since boot).
type gaProcInfo struct {
	pgrp  int
	start string
}

// gaPausedPath is where the paused processes are written down. The temp
// directory is RAM on the handheld, so a reboot forgets them with them.
func gaPausedPath() string {
	return filepath.Join(os.TempDir(), "jukahub-gameassist-paused.json")
}

// gaPausedNote writes down the processes about to be paused: each child and,
// for one that leads its own group, every process in the group — a stock
// launch.sh that started RetroArch and returned is gone already while
// RetroArch, in its group, is what is paused.
func gaPausedNote(cmds []*exec.Cmd) {
	pids := map[int]bool{}
	for _, cmd := range cmds {
		if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 1 {
			continue
		}
		pids[cmd.Process.Pid] = true
		if screenChildGroupConfigured(cmd) {
			for _, pid := range gaGroupMembers(cmd.Process.Pid) {
				pids[pid] = true
			}
		}
	}
	var procs []gaPausedProc
	for pid := range pids {
		if info, ok := gaProcStat(pid); ok {
			procs = append(procs, gaPausedProc{Pid: pid, Start: info.start})
		}
	}
	if len(procs) == 0 {
		return
	}
	data, err := json.Marshal(procs)
	if err != nil {
		return
	}
	if err := os.WriteFile(gaPausedPath(), data, 0o600); err != nil {
		logDebug("gameassist", "could not write down the paused game: %v", err)
	}
}

// gaGroupMembers lists the processes in group pgid.
func gaGroupMembers(pgid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		if info, ok := gaProcStat(pid); ok && info.pgrp == pgid {
			out = append(out, pid)
		}
	}
	return out
}

// gaPausedClear forgets them: the game runs again.
func gaPausedClear() {
	if err := os.Remove(gaPausedPath()); err != nil && !os.IsNotExist(err) {
		logDebug("gameassist", "could not remove %s: %v", gaPausedPath(), err)
	}
}

// gaRecoverPaused lets go of a game a JukaHub killed outright left paused:
// each process still there (same pid, same start time) is asked to quit — an
// interrupt, then a continue so it can act on it — and killed after
// gaRecoverGrace. An orphan game that ran on would fight this JukaHub for the
// framebuffer, and a stopped one holds the memory and the speaker. Runs once,
// at the first frame; the signals and the wait run on a goroutine.
func gaRecoverPaused() {
	data, err := os.ReadFile(gaPausedPath())
	if err != nil {
		return
	}
	gaPausedClear()
	var procs []gaPausedProc
	if json.Unmarshal(data, &procs) != nil || len(procs) == 0 {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("gameassist", "letting go of a paused game failed: %v", r)
			}
		}()
		var left []gaPausedProc
		for _, p := range procs {
			if p.Pid <= 1 || !gaProcSame(p) {
				continue
			}
			logWarn("gameassist", "a game the last JukaHub paused (pid %d) is still there: asking it to quit", p.Pid)
			_ = syscall.Kill(p.Pid, syscall.SIGINT)
			_ = syscall.Kill(p.Pid, syscall.SIGCONT)
			left = append(left, p)
		}
		if len(left) == 0 {
			return
		}
		time.Sleep(gaRecoverGrace)
		for _, p := range left {
			if gaProcSame(p) {
				_ = syscall.Kill(p.Pid, syscall.SIGKILL)
			}
		}
	}()
}

// gaProcSame reports whether p's process is still the one written down.
func gaProcSame(p gaPausedProc) bool {
	info, ok := gaProcStat(p.Pid)
	return ok && info.start != "" && info.start == p.Start
}

// gaProcStat reads /proc/<pid>/stat.
func gaProcStat(pid int) (gaProcInfo, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return gaProcInfo{}, false
	}
	return gaParseProcStat(string(data))
}

// gaParseProcStat reads a /proc/<pid>/stat line. The command name in brackets
// may itself hold spaces and brackets, so the fields are counted from its last
// ")", which ends field 2: the process group is field 5, the start time field
// 22. Pure.
func gaParseProcStat(s string) (gaProcInfo, bool) {
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return gaProcInfo{}, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return gaProcInfo{}, false
	}
	pgrp, err := strconv.Atoi(f[2])
	if err != nil {
		return gaProcInfo{}, false
	}
	return gaProcInfo{pgrp: pgrp, start: f[19]}, true
}

// gmInterruptGame asks a game to quit from the in-game menu (gamemenu.go): an
// interrupt to every process in its group that is not a shell, so a launch
// script lives on to do what it does after the emulator exits (restore the
// CPU, sync the SD card); with only shells in the group, or no group, the
// whole group gets it, as on the exit path. A stopped process keeps the
// interrupt until it is continued.
func gmInterruptGame(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if screenChildGroupConfigured(cmd) {
		sent := 0
		for _, pid := range gaGroupMembers(cmd.Process.Pid) {
			if gmIsShell(pid) {
				continue
			}
			if syscall.Kill(pid, syscall.SIGINT) == nil {
				sent++
			}
		}
		if sent > 0 {
			return
		}
	}
	_ = screenChildSignal(cmd, screenSignalInterrupt)
}

// gmIsShell reports whether pid runs a shell (a launch script).
func gmIsShell(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return false
	}
	switch strings.TrimSpace(string(data)) {
	case "sh", "bash", "ash", "dash", "hush", "busybox":
		return true
	}
	return false
}
