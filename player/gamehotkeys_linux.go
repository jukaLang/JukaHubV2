//go:build linux

package main

// gamehotkeys_linux.go — the pad watcher's half of the game hotkeys
// (gamehotkeys.go): F1 / L3 held, F2 / R3 pressed, and Menu + Select pressed
// together, read from the built-in pad's evdev node by the watcher that
// already runs beside the game (gameassist_linux.go). Nothing is grabbed and
// nothing is written into the pad: the game sees every press as before.
//
// The watcher calls three hooks: hotkeyKey for each button edge while the
// game has the pad (gaPadSession.feed), hotkeyTick once per read
// (gaPadSession.step) and hotkeyStop when it closes the pad.

import "time"

// The pad buttons the hotkeys read besides the ones vcursor_evdev.go names
// (input-event-codes.h). The Brick's and the Hammer's front F1 / F2 keys come
// as these two (brickkeys.go: SDL sees them as the stick clicks); on a
// handheld with sticks they are the clicks themselves.
const (
	gameHotkeyBtnThumbL = 0x13d // BTN_THUMBL: L3, the Brick's F1
	gameHotkeyBtnThumbR = 0x13e // BTN_THUMBR: R3, the Brick's F2
)

// gameHotkeyPad is one watcher's hotkey state. The watcher's goroutine owns
// it.
type gameHotkeyPad struct {
	// F1 / L3: rwArmed is the key down (alone) while the game had the pad,
	// rwAt since when, rwOn that this watcher has rewind held.
	rwArmed bool
	rwAt    time.Time
	rwOn    bool
	// F2 / R3: the last toggle, for the bounce guard.
	ffAt time.Time
	// Menu + Select: when each went down (zero: up), pair that both came down
	// together with nothing else, pairAt when, spoiled that something else
	// joined (nothing fires until both are up again).
	menuAt, selAt time.Time
	pair          bool
	pairAt        time.Time
	spoiled       bool
}

// hotkeyModifierHeld reports Select, Start or Menu held: a press with one of
// them is a chord (RetroArch's hotkeys, JukaHub's Select chords), not a
// hotkey.
func (s *gaPadSession) hotkeyModifierHeld() bool {
	return s.eng.key(evdevBtnSelect) || s.eng.key(evdevBtnStart) || s.eng.key(evdevBtnMode)
}

// hotkeyOthersHeld reports a button other than Menu and Select held, a
// trigger past half, or the D-pad away from the middle.
func (s *gaPadSession) hotkeyOthersHeld() bool {
	for i, k := range s.eng.keys {
		code := uint16(mbridgeKeyBase + i)
		if k && code != evdevBtnMode && code != evdevBtnSelect {
			return true
		}
	}
	return s.r2 || s.l2 || s.eng.abs[evdevAbsHat0X] != 0 || s.eng.abs[evdevAbsHat0Y] != 0
}

// hotkeyKey follows one button going down or up while the game has the pad
// (feed, after the button's state is stored).
func (s *gaPadSession) hotkeyKey(code uint16, down bool, now time.Time) {
	h := &s.hk
	if code == evdevBtnMode || code == evdevBtnSelect {
		s.hotkeyPair(code, down, now)
		return
	}
	if down && (!h.menuAt.IsZero() || !h.selAt.IsZero()) {
		// A third button with Menu or Select: an emulator's hotkey.
		h.pair, h.spoiled = false, true
	}
	if gameHotkeyKeys(gameHotkeysLive.keys.Load()) == gameHotkeysNone {
		h.rwArmed = false
		return
	}
	switch code {
	case gameHotkeyBtnThumbL:
		h.rwArmed = down && !s.hotkeyModifierHeld()
		h.rwAt = now
	case gameHotkeyBtnThumbR:
		if !down || s.hotkeyModifierHeld() {
			return
		}
		if !h.ffAt.IsZero() && now.Sub(h.ffAt) < gameHotkeyFastGap {
			return
		}
		h.ffAt = now
		gameHotkeysFastToggle(int(gameHotkeysLive.port.Load()))
	}
}

// hotkeyPair follows Menu and Select for the save-and-quit press: the two
// down within gameSaveQuitPairGap with nothing else held, and let go within
// gameSaveQuitMaxHold. It acts when the first of them comes up, so a third
// button joining (an emulator's Menu + Select + … hotkey) cancels it; the
// main thread does the rest (gameHotkeysSync).
func (s *gaPadSession) hotkeyPair(code uint16, down bool, now time.Time) {
	h := &s.hk
	at, other := &h.menuAt, h.selAt
	if code == evdevBtnSelect {
		at, other = &h.selAt, h.menuAt
	}
	if down {
		*at = now
		switch {
		case other.IsZero():
			// The first of the two.
			if s.hotkeyOthersHeld() {
				h.spoiled = true
			}
		case !h.spoiled && now.Sub(other) <= gameSaveQuitPairGap && !s.hotkeyOthersHeld():
			h.pair, h.pairAt = true, now
		default:
			h.pair, h.spoiled = false, true
		}
		return
	}
	*at = time.Time{}
	if h.pair && !h.spoiled && now.Sub(h.pairAt) <= gameSaveQuitMaxHold && !s.hotkeyOthersHeld() &&
		gameHotkeysLive.saveQuit.Load() {
		gameHotkeysLive.saveQuitAt.Store(time.Now().UnixNano())
		logInfo("gameassist", "Menu + Select in %q: save and quit", s.name)
	}
	h.pair = false
	if h.menuAt.IsZero() && h.selAt.IsZero() {
		h.spoiled = false
	}
}

// hotkeyTick is the per-read upkeep: rewind held or let go, and the state the
// events left behind put right where the pad was read whole (resync) or held
// for the overlay.
func (s *gaPadSession) hotkeyTick(now time.Time) {
	h := &s.hk
	if s.held {
		// The overlay has the pad: nothing pressed for it is a hotkey, and a
		// button still down when it is given back must be let go first.
		*h = gameHotkeyPad{rwOn: h.rwOn, ffAt: h.ffAt}
	}
	if !h.menuAt.IsZero() && !s.eng.key(evdevBtnMode) {
		h.menuAt, h.pair = time.Time{}, false
	}
	if !h.selAt.IsZero() && !s.eng.key(evdevBtnSelect) {
		h.selAt, h.pair = time.Time{}, false
	}
	if h.menuAt.IsZero() && h.selAt.IsZero() {
		h.spoiled = false
	} else if s.r2 || s.l2 || s.eng.abs[evdevAbsHat0X] != 0 || s.eng.abs[evdevAbsHat0Y] != 0 {
		// A trigger axis or the D-pad joined Menu or Select (they make no
		// button edge for hotkeyKey): an emulator's hotkey, not the pair.
		h.pair, h.spoiled = false, true
	}
	if h.rwArmed && !s.eng.key(gameHotkeyBtnThumbL) {
		h.rwArmed = false // its release was among dropped events
	}
	want := h.rwArmed && !s.held && gameHotkeysLive.rewind.Load() && !s.hotkeyModifierHeld() &&
		now.Sub(h.rwAt) >= gameHotkeyRewindHold
	if want == h.rwOn {
		return
	}
	h.rwOn = want
	gameHotkeysLive.rewinding.Store(want)
	if want {
		gameHotkeysRewindRun(int(gameHotkeysLive.port.Load()))
	}
}

// hotkeyStop lets go of everything when the watcher closes the pad.
func (s *gaPadSession) hotkeyStop() {
	if s.hk.rwOn {
		gameHotkeysLive.rewinding.Store(false)
	}
	s.hk = gameHotkeyPad{}
}
