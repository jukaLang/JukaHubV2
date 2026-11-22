package main

// uiboost.go — Faster menus: the CPU speeds up the moment a button is pressed.
//
// Juka (2026-11-22): "Optimize JukaHub to run even faster and have better
// performance on Trimui devices... feel free to manage cpu freq". The games
// already get the clock (powerprofile.go, cpuboost.go). The hub's own screens
// ran on what Automatic CPU speed (autoperf.go) chose from a two-second
// sample: Home and the menus start at 1.2 GHz and step down to 816 MHz when
// quiet, under the firmware's ondemand governor, which idles the clock between
// frames and on these Allwinner kernels looks at the load only now and then. A
// press then drew its frames at that low clock — a page opening, a list
// scrolling, the keyboard coming up — until the ticker's next sample.
//
// The boost is the dial's other end for a moment. On a press (and while a held
// button repeats), when a game hands the screen back, and at the start,
// Automatic CPU speed's hub session holds
//
//   - the ceiling at the top of its table, under the heat guard's dial
//     (heatguard.go), and
//   - the floor at uiBoostFloorPct of that top, so the governor cannot idle
//     the clock between the frames the press asked for,
//
// for uiBoostPressHold after the last press (uiBoostReturnHold after a game,
// uiBoostStartHold at the start), then hands both back: the floor to the value
// it found, the ceiling to the level the load chose. Both are the session's
// own writes (autoPerfWriteLocked), so the compare-and-restore, the crash
// marker and every hand-over Automatic CPU speed already does cover them, and
// a launch ends the boost with the session.
//
// It stands aside with Settings → Faster menus off; Automatic CPU speed off or
// refused; the Battery Saver power mode or the low-battery saver; a warm chip
// or battery (the heat guard steps nothing up then) or the kernel's own
// thermal limit; the idle screens, whose low-power state needs the floor down
// (saver_power.go ends the boost before it lowers the clock); the voice
// assistant's own boost; a game. With the Performance power mode it holds for
// as long as the hub is in front.
//
// The frame controller reads the same state (adaptive.go): while the user is
// pressing, Low Power — "a slower frame rate when nothing moves" — does not
// slow the frames, and with the clock up and frames cheap the menus draw at
// 60 fps.

import (
	"strings"
	"sync/atomic"
	"time"
)

const (
	// uiBoostSetting is the Settings row (on unless set to false).
	uiBoostSetting = "menuBoost"

	// How long the clock stays up: after the last press, after a game hands
	// the screen back (textures, covers and the page come back at once), and
	// at the start (the first frames and the start-up work queued behind them).
	uiBoostPressHold  = 1500 * time.Millisecond
	uiBoostReturnHold = 4 * time.Second
	uiBoostStartHold  = 6 * time.Second

	// uiBoostFloorPct is the floor while boosted, in percent of the table's
	// top: the lowest level at or above it (1.6 GHz of the Smart Pro's 2 GHz,
	// 1.4 of a 1.8 GHz Brick), never over the heat dial.
	uiBoostFloorPct = 75

	// uiBoostRetry is how soon a request that could not be served yet is
	// looked at again: a press that woke the idle screen (the screen in front
	// is named twice a second), a launch settling, the voice assistant's boost.
	uiBoostRetry = 200 * time.Millisecond

	// uiBoostIdleKey is the idle screens' context (autoPerfHubContext): never
	// boosted, the saver lowers the clock there.
	uiBoostIdleKey = "hub:idle"
)

// uiBoostKind is what asked for the boost; it sets the hold.
type uiBoostKind int

const (
	uiBoostPress  uiBoostKind = iota // a button, key, touch or deflected stick
	uiBoostReturn                    // a game handed the screen back
	uiBoostStart                     // JukaHub's first frames
)

var (
	// uiBoostOn is what the row, Automatic CPU speed and the power mode allow
	// (uiBoostPublish); uiBoostPinned is the Performance power mode.
	uiBoostOn     atomic.Bool
	uiBoostPinned atomic.Bool
	// uiBoostUntil is when the newest request's hold ends (Unix nanoseconds).
	uiBoostUntil atomic.Int64
	// uiBoostLive is the boost in force: the frame controller's 60 fps step
	// reads it, and a request while it is up needs no wake.
	uiBoostLive atomic.Bool
	// uiBoostKick wakes Automatic CPU speed's ticker for a request.
	uiBoostKick = make(chan struct{}, 1)
)

// uiBoost is the boost's state, under autoPerf.mu.
var uiBoost struct {
	on     bool
	since  time.Time
	logged bool // the run's first boost went to the normal log
}

// ── Settings ─────────────────────────────────────────────────────────────

// uiBoostEnabled reads the row; on by default: where the clock cannot be
// changed it does nothing.
func uiBoostEnabled(c *Config) bool {
	if c == nil {
		return true
	}
	return customBool(c.Variables.Custom, uiBoostSetting, true)
}

// uiBoostPublish tells the ticker and the frame controller what is allowed
// now, and wakes the ticker when that changed. Main thread: autoPerfFrame
// twice a second, and the row.
func uiBoostPublish(c *Config) {
	mode := currentPowerMode(c)
	on := autoPerfSupported() && uiBoostEnabled(c) && autoPerfEnabled(c) &&
		mode != PowerModeBatterySaver && !batterySaverActive
	pinned := on && mode == PowerModePerformance
	wasOn := uiBoostOn.Swap(on)
	wasPinned := uiBoostPinned.Swap(pinned)
	if !on {
		uiBoostUntil.Store(0)
	}
	if wasOn != on || wasPinned != pinned {
		uiBoostWake()
	}
}

// uiBoostWake wakes the ticker; a wake already waiting is enough.
func uiBoostWake() {
	select {
	case uiBoostKick <- struct{}{}:
	default:
	}
}

// ── Requests ─────────────────────────────────────────────────────────────

// uiBoostNote asks for the boost: kind's hold from now, or longer if an
// earlier request still holds it. Main thread (the frame controller's
// NotifyInput, the main loop) and cheap: a clock read, two atomics and, when
// the boost is not up yet, a wake for the ticker.
func uiBoostNote(kind uiBoostKind) {
	if !uiBoostOn.Load() {
		return
	}
	hold := uiBoostPressHold
	switch kind {
	case uiBoostReturn:
		hold = uiBoostReturnHold
	case uiBoostStart:
		hold = uiBoostStartHold
	}
	until := time.Now().Add(hold).UnixNano()
	for {
		cur := uiBoostUntil.Load()
		if until <= cur || uiBoostUntil.CompareAndSwap(cur, until) {
			break
		}
	}
	if !uiBoostLive.Load() {
		uiBoostWake()
	}
}

// uiBoostEndNow ends the boost at once, whatever its hold. saver_power.go
// calls it before an idle screen lowers the clock: under a raised floor the
// kernel refuses the idle screen's ceiling. Main thread.
func uiBoostEndNow() {
	if !uiBoostLive.Load() {
		return
	}
	uiBoostUntil.Store(0)
	autoPerf.mu.Lock()
	defer autoPerf.mu.Unlock()
	uiBoostEndLocked()
}

// ── The ticker's side (autoperf.go autoPerfLoop) ─────────────────────────

// uiBoostReconcile puts the boost where the requests and the state of things
// say, and reports when to look again (0: nothing to wait for; the ticker's
// two seconds cover the rest). Automatic CPU speed's ticker goroutine.
func uiBoostReconcile(now time.Time) time.Duration {
	autoPerf.mu.Lock()
	defer autoPerf.mu.Unlock()
	// Read under the lock: a press that lands while the tick holds it is seen.
	until := uiBoostUntil.Load()
	pinned := uiBoostPinned.Load()
	want := uiBoostOn.Load() && (pinned || now.UnixNano() < until)
	if autoPerf.stopped {
		uiBoost.on = false
		uiBoostLive.Store(false)
		return 0
	}
	if want && !heatGuard.inited {
		// The start's request comes before the first tick: the heat guard
		// reads the chip first, so a warm one is never boosted.
		heatGuardTickLocked(now)
	}
	s := &autoPerf.sess
	if want && autoPerfHubInFrontLocked() &&
		(!s.active || (strings.HasPrefix(s.key, "hub:") && s.key != autoPerfHubKeyLocked())) {
		// The hub's screen in front changed since the last tick (a game
		// handed it back, the idle screen went): follow it now, not up to two
		// seconds later, with no load sample.
		autoPerfFollowLocked(now, 0, 0, false)
	}
	ok := uiBoostEligibleLocked(s)
	switch {
	case want && ok && !uiBoost.on:
		if active, known := cpuThermalThrottleState(); known && active {
			break // the kernel is cooling the chip: no clock goes up
		}
		// Live before the write, so uiBoostEndNow never skips a boost whose
		// floor is going up.
		uiBoost.on, uiBoost.since = true, now
		uiBoostLive.Store(true)
		if !autoPerfWriteLocked(s.levels[s.idx]) {
			// Another tool moved the clock: the session is its now.
			uiBoostFinishLocked()
			return 0
		}
		ceil, floor := uiBoostLevelsLocked(s)
		autoPerfSetLine(autoPerfLineLocked(s))
		if !uiBoost.logged {
			// Once a run in the normal log, for a device check.
			uiBoost.logged = true
			logInfo("power", "faster menus: first boost, %s held at %s (%s)", s.label, uiBoostRange(floor, ceil), uiBoostWhy(pinned, until, now))
		} else {
			logDebug("power", "faster menus: %s held at %s (%s)", s.label, uiBoostRange(floor, ceil), uiBoostWhy(pinned, until, now))
		}
	case uiBoost.on && (!want || !ok):
		if !uiBoostEndLocked() {
			return uiBoostRetry
		}
		if uiBoostOn.Load() && time.Now().UnixNano() < uiBoostUntil.Load() {
			// A press landed while this one ended: look again at once.
			uiBoostWake()
		}
	}
	switch {
	case pinned:
		return 0 // no hold to run out; the ticker looks every two seconds
	case uiBoost.on:
		return max(time.Duration(until-now.UnixNano()), 10*time.Millisecond)
	case want:
		return min(uiBoostRetry, time.Duration(until-now.UnixNano()))
	}
	return 0
}

// uiBoostWhy names a boost's request for the debug line.
func uiBoostWhy(pinned bool, until int64, now time.Time) string {
	if pinned {
		return "Performance mode"
	}
	return "for " + time.Duration(until-now.UnixNano()).Round(10*time.Millisecond).String()
}

// uiBoostEligibleLocked reports whether the session in front may be boosted:
// an in-hub screen Automatic CPU speed clocks itself, not the idle screens,
// with nothing else holding the clock and the chip not warm. Under
// autoPerf.mu; it takes autoPerfHub.mu and voiceBoost.mu, as the tick does.
func uiBoostEligibleLocked(s *autoPerfSession) bool {
	if autoPerf.denied || !s.active || s.manual || s.foreign || s.boost || len(s.levels) == 0 ||
		s.idx < 0 || s.idx >= len(s.levels) || s.top < 0 || s.top >= len(s.levels) ||
		!strings.HasPrefix(s.key, "hub:") || s.key == uiBoostIdleKey {
		return false
	}
	if heatGuardHoldsLocked() || saverLowPowerEngaged() || voiceBoostActive() {
		return false
	}
	return autoPerfHubKeyLocked() != uiBoostIdleKey
}

// uiBoostLevelsLocked is the ceiling and the floor the boost holds for s, as
// clocks of the fastest policy: the top of its table the heat dial allows, and
// the lowest level at uiBoostFloorPct of the table's top or above, never over
// that ceiling. The caller checked s (uiBoostEligibleLocked).
func uiBoostLevelsLocked(s *autoPerfSession) (ceil, floor int64) {
	hi := autoPerfHeatIdxLocked(s)
	fi := min(autoPerfIndexAtLeast(s.levels, s.levels[s.top]*uiBoostFloorPct/100), hi)
	return s.levels[hi], s.levels[fi]
}

// uiBoostEndLocked takes the boost off: the floor back to the value found and
// the ceiling to the level the load chose. It reports false while it has to
// wait: the voice assistant's boost holds the clock (its governor makes the
// floor moot, and lowering the ceiling under it would slow the answer). Under
// autoPerf.mu.
func uiBoostEndLocked() bool {
	if !uiBoost.on {
		return true
	}
	if voiceBoostActive() {
		return false
	}
	uiBoost.on = false
	uiBoostLive.Store(false)
	s := &autoPerf.sess
	if saverLowPowerEngaged() || !s.active || s.manual || s.foreign || s.boost ||
		s.idx < 0 || s.idx >= len(s.levels) {
		// Not Automatic CPU speed's to write now: the session's own end puts
		// back whatever is still the value it wrote.
		return true
	}
	autoPerfWriteLocked(s.levels[s.idx])
	autoPerfSetLine(autoPerfLineLocked(s))
	logDebug("power", "faster menus: %s back to %s after %s", s.label, formatFrequencyKHz(s.levels[s.idx]),
		time.Since(uiBoost.since).Round(10*time.Millisecond))
	return true
}

// uiBoostFinishLocked forgets the boost with the session that held it: the
// session's end has put its floor and ceiling back. autoPerfFinishLocked,
// under autoPerf.mu.
func uiBoostFinishLocked() {
	uiBoost.on = false
	uiBoostLive.Store(false)
}

// uiBoostRange is "1.6–2.0 GHz", or one clock when both are the same.
func uiBoostRange(floor, ceil int64) string {
	if floor <= 0 || floor >= ceil {
		return formatFrequencyKHz(ceil)
	}
	return strings.TrimSuffix(formatFrequencyKHz(floor), " GHz") + "–" + formatFrequencyKHz(ceil)
}

// ── Settings → Games & emulators → Faster menus ──────────────────────────

// uiBoostSettingsRow is the row under Automatic CPU speed, on a TrimUI
// handheld.
func uiBoostSettingsRow() setRow {
	return setRow{
		ID: uiBoostSetting, Label: "Faster menus", Kind: setKindToggle,
		Visible: func(*Config) bool { return autoPerfSupported() },
		Detail:  uiBoostSettingDetail,
		Get:     uiBoostEnabled,
		Set: func(c *Config, on bool) {
			setStoreBool(c, uiBoostSetting, on)
			uiBoostPublish(c)
			setToast("Faster menus: " + setOnOff(on))
		},
	}
}

// uiBoostSettingDetail is the row's second line. Cheap: drawn every frame.
func uiBoostSettingDetail(c *Config) string {
	autoPerfShown.mu.Lock()
	reason := autoPerfShown.reason
	autoPerfShown.mu.Unlock()
	mode := currentPowerMode(c)
	switch {
	case reason != "":
		return "Not available: " + reason
	case !uiBoostEnabled(c):
		return "Off: menus run at the Automatic CPU speed level"
	case !autoPerfEnabled(c):
		return "Needs Automatic CPU speed"
	case mode == PowerModeBatterySaver || batterySaverActive:
		return "Resting while Battery Saver is on"
	case mode == PowerModePerformance:
		return "Performance mode: menus keep the fast clock"
	}
	// The clocks themselves are on the Automatic CPU speed row's line.
	return "A press speeds the CPU up for a moment and menus draw up to 60 fps"
}
