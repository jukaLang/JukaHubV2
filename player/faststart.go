package main

// faststart.go — Faster game starts: the seconds between A and a game's first
// frame.
//
// Juka (2026-11-23): "Improve performance on Trimui devices even further...
// improve emulator launching...". What a library launch (launchLibraryGame)
// spent those seconds on, read from the launch path:
//
//   - JukaHub's own preparation: a dozen or more one-launch RetroArch settings
//     (the save-state switches, the OSD, the log, the picture, the controls,
//     the speed level…), each synced to the card on its own — three fsyncs
//     apiece on a FAT card. They are now written as they come and synced once,
//     just before the game starts: the launch batch (racfgscope.go), opened
//     here for every library launch, row or no row, because nothing is given
//     up for it.
//   - the clock: Automatic CPU speed (autoperf.go) began a game's session at
//     the level the game settled at last time — 1.0 GHz for a GBA game, say —
//     right while RetroArch was still loading the core, the ROM and the auto
//     state. A game's session now begins at the top the heat allows, with its
//     floor near it (the values Faster menus uses, uiboost.go), for
//     fastStartBurstHold, and only then takes its level; those seconds are not
//     learned and not stepped by load (the burst, below).
//   - the card: a light game read its core and ROM 128 KB at a time, while
//     JukaHub's own work after the start (the recents, the game's record, the
//     log, the garbage the preparation left for the collector) competed with
//     it. For fastStartWindow after the start (the window, below) the card the
//     game and its emulator are on reads ahead 1 MB, as the heavy-game boost
//     does for its games; JukaHub's threads step behind the game (nice 10 and
//     the lowest best-effort I/O level — not the idle class, which a game
//     streaming its disc could starve — with SDL's sound threads left alone
//     for music played under the game, faststart_linux.go); and the game's
//     emulator, core and ROM are read ahead while RetroArch is still setting
//     up its screen, unless the highlight's warm-up already read them
//     (gameWarmAtStart, gamewarm.go).
//
// Settings → Games & emulators → "Faster game starts" (gameStartBoost, on by
// default) switches the burst and the window; both rest in the Battery Saver
// power mode and under the low-battery saver, the burst with Automatic CPU
// speed off, on a warm chip and while the kernel cools it. A game the
// heavy-game boost (xboxboost.go) takes gets the read-ahead and the step back
// from that boost, for its whole run, and a CPU-boosted one (cpuboost.go)
// holds the top clock already: the parts they cover stand aside. Only on the
// handheld. JUKAHUB_GAME_START=0 switches the window off for a run;
// JUKAHUB_LAUNCH_BATCH=0 the batch.
//
// The window's values are sysfs and thread priorities: a crash leaves a
// read-ahead of 1 MB until the next boot (what JukaHub's own OS image boots
// with anyway) and takes the priorities with the process, so there is no
// crash marker.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// fastStartKey is the Settings row (on unless set to false).
	fastStartKey = "gameStartBoost"
	// fastStartBurstHold is how long a game's session holds the top clock
	// from its first tick (up to two seconds after the start): RetroArch's
	// own start, the core, the ROM and the auto state.
	fastStartBurstHold = 8 * time.Second
	// fastStartWindow is how long after the start the card reads ahead, the
	// start warm may read and JukaHub stays behind the game.
	fastStartWindow = 10 * time.Second
)

var (
	// fastStartBurstOn is the burst's say, published by the main thread
	// (fastStartPublish) for Automatic CPU speed's ticker.
	fastStartBurstOn atomic.Bool
	// fastStartBurstLogged: the run's first burst went to the normal log.
	// Under autoPerf.mu.
	fastStartBurstLogged bool
	// fastStartOff is JUKAHUB_GAME_START=0: no window for this run.
	fastStartOff = os.Getenv("JUKAHUB_GAME_START") == "0"
)

// fastStart is the window of the launch in progress; one at a time.
var fastStart struct {
	mu      sync.Mutex
	gen     uint64
	open    bool
	started bool
	label   string
	since   time.Time
	writes  []saverSysfsWrite // the read-ahead raised, for the restore
	stepped int               // JukaHub's threads moved (faststart_linux.go)
	timer   *time.Timer
	warm    context.CancelFunc
}

func init() {
	gameLaunchRegisterStarted(fastStartStarted)
}

// ── Settings ─────────────────────────────────────────────────────────────

// fastStartEnabled reads the row; on by default.
func fastStartEnabled(c *Config) bool {
	if c == nil {
		return true
	}
	return customBool(c.Variables.Custom, fastStartKey, true)
}

// fastStartSupported: the handheld's clocks, card and priorities only.
func fastStartSupported() bool { return autoPerfSupported() }

// fastStartResting reports the Battery Saver power mode or the low-battery
// saver, when a start is not worth the extra clock. Main thread.
func fastStartResting(c *Config) bool {
	return currentPowerMode(c) == PowerModeBatterySaver || batterySaverActive
}

// fastStartPublish tells Automatic CPU speed's ticker whether a game's start
// may be boosted. Main thread: autoPerfFrame, twice a second, and the row.
func fastStartPublish(c *Config) {
	fastStartBurstOn.Store(fastStartSupported() && fastStartEnabled(c) && autoPerfEnabled(c) && !fastStartResting(c))
}

// fastStartSettingsRow is Settings → Games & emulators → "Faster game
// starts", under Faster menus, on a TrimUI handheld.
func fastStartSettingsRow() setRow {
	return setRow{
		ID: fastStartKey, Label: "Faster game starts", Kind: setKindToggle,
		Visible: func(*Config) bool { return fastStartSupported() },
		Detail:  fastStartSettingDetail,
		Get:     fastStartEnabled,
		Set: func(c *Config, on bool) {
			setStoreBool(c, fastStartKey, on)
			fastStartPublish(c)
			setToast("Faster game starts: " + setOnOff(on))
		},
	}
}

// fastStartSettingDetail is the row's second line. Cheap: drawn every frame.
func fastStartSettingDetail(c *Config) string {
	switch {
	case !fastStartEnabled(c):
		return "Off: a game loads at the speed it settled at last time"
	case fastStartResting(c):
		return "Resting while Battery Saver is on"
	case !autoPerfEnabled(c):
		return "Card read-ahead only: the CPU part needs Automatic CPU speed"
	}
	return "Full CPU speed and a faster card while a game loads, then its usual speed"
}

// ── The burst (autoperf.go calls these under autoPerf.mu) ────────────────

// fastStartBurstArmLocked starts a game session's burst as the session
// begins (autoPerfBeginLocked): a game Automatic CPU speed clocks itself —
// not a boosted one, which holds the top already, nor one with its own CPU
// setting — while the row allows it and the chip is cool.
func fastStartBurstArmLocked(s *autoPerfSession, now time.Time) {
	s.burst = false
	if s.class != autoPerfClassGame || !fastStartBurstEligibleLocked(s) {
		return
	}
	if !fastStartBurstOn.Load() || autoPerf.denied || heatGuardHoldsLocked() || saverLowPowerEngaged() || voiceBoostActive() {
		return
	}
	if active, known := cpuThermalThrottleState(); known && active {
		return
	}
	// An emulator on the userspace governor sets its own clock (NextUI's
	// minarch): a raised floor would overrule it.
	if cpuBoostGovernor(s.dirs) == "userspace" {
		return
	}
	s.burst, s.burstUntil = true, now.Add(fastStartBurstHold)
	ceil, floor := uiBoostLevelsLocked(s)
	const line = "faster game start: %s at %s for %s, then %s"
	if !fastStartBurstLogged {
		// Once a run in the normal log, for a device check.
		fastStartBurstLogged = true
		logInfo("power", line, s.label, uiBoostRange(floor, ceil), fastStartBurstHold, formatFrequencyKHz(s.levels[s.idx]))
		return
	}
	logDebug("power", line, s.label, uiBoostRange(floor, ceil), fastStartBurstHold, formatFrequencyKHz(s.levels[s.idx]))
}

// fastStartBurstEligibleLocked reports a session Automatic CPU speed clocks
// itself, with a level table and a level in it.
func fastStartBurstEligibleLocked(s *autoPerfSession) bool {
	return s.active && !s.manual && !s.foreign && !s.boost && len(s.levels) > 0 &&
		s.top >= 0 && s.top < len(s.levels) && s.idx >= 0 && s.idx < len(s.levels)
}

// fastStartBurstHoldsLocked reports whether s's writes take the burst's lift
// (autoPerfWriteLocked): a burst in force on a session Automatic CPU speed
// still clocks.
func fastStartBurstHoldsLocked(s *autoPerfSession) bool {
	return s.burst && fastStartBurstEligibleLocked(s)
}

// fastStartBurstTickLocked is one tick of a game's burst (autoPerfAdaptLocked):
// false when none is in force, and the tick goes on as usual; true when the
// burst took the tick — still holding (the lift follows the heat dial, which
// may have moved), or just over, with the clock back at the game's level.
func fastStartBurstTickLocked(s *autoPerfSession, now time.Time) bool {
	if !s.burst {
		return false
	}
	s.last = now // the load's seconds are not learned
	held := fastStartBurstHoldsLocked(s)
	over := !held || !now.Before(s.burstUntil) || !fastStartBurstOn.Load() || heatGuardHoldsLocked() || saverLowPowerEngaged()
	if !over {
		if active, known := cpuThermalThrottleState(); known && active {
			over = true
		}
	}
	if !over {
		autoPerfWriteLocked(s.levels[s.idx])
		return true
	}
	s.burst = false
	if held {
		if autoPerfWriteLocked(s.levels[s.idx]) {
			autoPerfSetLine(autoPerfLineLocked(s))
		}
		logDebug("power", "faster game start: %s back to %s", s.label, formatFrequencyKHz(s.levels[s.idx]))
	}
	return true
}

// ── The window ───────────────────────────────────────────────────────────

// fastStartPrepare opens a library launch's fast start, at the top of its
// preparation (launchLibraryGame): the launch batch, and on the handheld with
// the row on the window's read-ahead. It returns the launch's end, which the
// launch defers: the batch ends (the start synced it already, or the launch
// stopped short and it syncs now), and a window whose game never started
// closes. Main thread.
func fastStartPrepare(game libraryGame, launcher string) func() {
	raScopeBatchBegin()
	gen := fastStartOpen(game, launcher)
	return func() {
		raScopeBatchEnd()
		if gen != 0 {
			fastStartAbandon(gen)
		}
	}
}

// fastStartWanted: the handheld, the row on, nothing resting. Main thread.
func fastStartWanted(c *Config) bool {
	return !fastStartOff && fastStartSupported() && fastStartEnabled(c) && !fastStartResting(c)
}

// fastStartOpen opens the window for game: a window still open from the game
// before (one that quit in its first seconds) closes first, then the card the
// ROM is on, and the one the emulator is on, read ahead 1 MB. It returns the
// window's generation, 0 when none opened.
func fastStartOpen(game libraryGame, launcher string) uint64 {
	if !fastStartWanted(appConfig) {
		return 0
	}
	fastStart.mu.Lock()
	prev := fastStartTakeLocked()
	fastStart.mu.Unlock()
	prev.restore("another game is starting")
	// The heavy-game boost raises the read-ahead itself, for the game's whole
	// run (xboxboost.go): a restore after the window would undo it.
	var writes []saverSysfsWrite
	if xboxBoostKindForGame(game) == xboxBoostNone || !xboxBoostEnabled(appConfig) {
		writes = fastStartApply(fastStartReadAheadPlan(game.Path, launcher))
	}
	fastStart.mu.Lock()
	defer fastStart.mu.Unlock()
	fastStart.gen++
	fastStart.open, fastStart.started = true, false
	fastStart.label, fastStart.since = game.Label(), time.Now()
	fastStart.writes, fastStart.stepped = writes, 0
	return fastStart.gen
}

// fastStartStarted is the launch's start (a gameLaunchRegisterStarted hook,
// main thread): the window runs fastStartWindow from now, JukaHub's threads
// step behind the game unless the heavy-game boost does that for its whole
// run, and the start warm reads what the highlight's warm-up did not.
func fastStartStarted(game libraryGame, launcher string, cmd *exec.Cmd) {
	// Read here, on the main thread: the warm-up's row lives in the config's
	// live map, which a goroutine must not read.
	warm := gameWarmEnabled(appConfig)
	fastStart.mu.Lock()
	if !fastStart.open || fastStart.started {
		fastStart.mu.Unlock()
		return
	}
	fastStart.started = true
	gen := fastStart.gen
	if !xboxBoostLive.Load() {
		fastStart.stepped = fastStartStepBack()
	}
	if warm {
		ctx, cancel := context.WithTimeout(gameWarmStartContext(), fastStartWindow)
		fastStart.warm = cancel
		go gameWarmAtStart(ctx, cancel, game, launcher)
	}
	fastStart.timer = time.AfterFunc(fastStartWindow, func() { fastStartClose(gen, "its first seconds are over") })
	parts := fastStartDescribeLocked()
	fastStart.mu.Unlock()
	if parts == "" {
		logDebug("library", "faster game start for %s: nothing to change", strconv.Quote(game.Label()))
		return
	}
	logInfo("library", "faster game start for %s: %s", strconv.Quote(game.Label()), parts)
}

// fastStartDescribeLocked is the window's account for the log, "" when it
// changes nothing. Under fastStart.mu.
func fastStartDescribeLocked() string {
	var parts []string
	for _, w := range fastStart.writes {
		disk := filepath.Base(filepath.Dir(filepath.Dir(w.Path)))
		parts = append(parts, disk+" reads ahead "+w.Original+" → "+w.Written+" KB")
	}
	if fastStart.stepped > 0 {
		parts = append(parts, "JukaHub's "+pluralizeCount(fastStart.stepped, "thread", "threads")+" behind the game")
	}
	if fastStart.warm != nil {
		parts = append(parts, "start warm-up")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + ", for " + fastStartWindow.String()
}

// fastStartTaken is a closed window's values, for its restore outside the
// lock.
type fastStartTaken struct {
	label   string
	since   time.Time
	writes  []saverSysfsWrite
	stepped int
	warm    context.CancelFunc
}

// fastStartTakeLocked closes the open window and hands back what it holds;
// nil when none is open. Under fastStart.mu.
func fastStartTakeLocked() *fastStartTaken {
	if !fastStart.open {
		return nil
	}
	t := &fastStartTaken{label: fastStart.label, since: fastStart.since, writes: fastStart.writes,
		stepped: fastStart.stepped, warm: fastStart.warm}
	if fastStart.timer != nil {
		fastStart.timer.Stop()
		fastStart.timer = nil
	}
	fastStart.open, fastStart.started = false, false
	fastStart.writes, fastStart.stepped, fastStart.warm = nil, 0, nil
	return t
}

// restore puts back what a closed window changed: the start warm stops, the
// read-ahead goes back where it still holds the window's value, and
// JukaHub's threads come forward.
func (t *fastStartTaken) restore(why string) {
	if t == nil {
		return
	}
	if t.warm != nil {
		t.warm()
	}
	n := saverRestoreWrites(t.writes)
	if t.stepped > 0 {
		fastStartStepForward()
	}
	logDebug("library", "faster game start for %s over after %s (%s): %d of %d read-ahead value(s) put back",
		strconv.Quote(t.label), time.Since(t.since).Round(100*time.Millisecond), why, n, len(t.writes))
}

// fastStartClose closes the window of generation gen (its timer).
func fastStartClose(gen uint64, why string) {
	fastStart.mu.Lock()
	if gen != fastStart.gen {
		fastStart.mu.Unlock()
		return
	}
	t := fastStartTakeLocked()
	fastStart.mu.Unlock()
	t.restore(why)
}

// fastStartAbandon closes the window of generation gen when its game never
// started (the launch's deferred end: a refusal or an error after the window
// opened).
func fastStartAbandon(gen uint64) {
	fastStart.mu.Lock()
	if gen != fastStart.gen || !fastStart.open || fastStart.started {
		fastStart.mu.Unlock()
		return
	}
	t := fastStartTakeLocked()
	fastStart.mu.Unlock()
	t.restore("the game did not start")
}

// fastStartScreenFree closes a started window once JukaHub has the screen
// again (noteGameChildExited, on the reaper goroutine): the game ended within
// its first seconds, and the hub must not stay stepped back. A window whose
// launch is still preparing — another game, picked the moment the hub came
// back — has not started and stays open.
func fastStartScreenFree() {
	fastStart.mu.Lock()
	if !fastStart.open || !fastStart.started {
		fastStart.mu.Unlock()
		return
	}
	t := fastStartTakeLocked()
	fastStart.mu.Unlock()
	t.restore("the game ended")
}
