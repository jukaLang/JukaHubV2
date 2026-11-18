package main

// quickresume.go — pick up and play (PWR-2): a sleeping handheld that powers
// itself off safely, the game that comes back at power-on, and a power key
// that only darkens the screen while music plays.
//
// Deep sleep, then power off. A suspended TrimUI still drains its battery
// ("mem" keeps the RAM powered; "freeze", the only state some kernels offer,
// keeps far more of the SoC awake), so a handheld left in a bag for days dies
// in its sleep, and a game held in RAM dies with it. sleepNow (powersleep.go)
// therefore arms the RTC for the earlier of the next alarm and Settings →
// Power off after sleeping (6 h by default on a deep "mem" kernel, 1 h on a
// light-sleep one: "freeze", or "mem" that is s2idle). When the device wakes and that deadline is what woke it
// — the time-asleep clock covers it, the RTC's alarm is no longer pending, no
// alarm is due and no power press arrived — quickSleepAtDeadline runs with the
// screen still off and input still muted:
//
//   - on a charger it simply sleeps again;
//   - with nothing running it powers off;
//   - with a RetroArch game the sleep stopped, the game continues for the
//     moment it takes to SAVE_STATE and QUIT through RetroArch's network
//     commands (retroArchSaveAndQuit, the low-battery guard's own steps), and
//     only once RetroArch has exited — the one proof the state was written —
//     is the resume marker written and the device powered off through Power
//     off's own path (devPowerRequest: the clean teardown, sync, busybox
//     poweroff), without the shutdown chime;
//   - anything that cannot be saved that way (a standalone emulator, a port,
//     RetroArch with network commands off, a game under the in-game menu, an
//     emulator that did not exit) is never powered off: the game is stopped
//     again and the device goes back to sleep. Progress is never traded for
//     battery.
//
// The delay runs from 15 minutes to 48 hours (the range NextUI's Auto Power
// Off pak offers) and Quick Settings has it one press away: A turns it off
// or back on at the last delay. Time asleep is read on two clocks, BOOTTIME
// and the RTC that holds the alarm (powerSleepSlept), since a kernel that
// does not add the suspend to BOOTTIME would hide the sleep. A sleep JukaHub
// did not start — minarch's own deep sleep in a NextUI pak game — gets the
// same deadline through nextui_sleep.go.
//
// Resume at power-on. The marker (resume-at-power-on.json in the data
// directory) names the game, its console, its launcher and the boot it was
// written in. The next start in a different boot reads it and deletes it
// before the first frame (a game that takes JukaHub down with it can never
// start a loop; only a travel-locked power-on, turned straight back off,
// leaves it for the next one), skips
// the boot animation, and as soon as the first frame is up and the work queued
// behind it has run (bounded by quickResumeStartupWait, so the network and
// Syncthing are not left waiting behind the game) starts the game through the
// Continue row's own launch (playRecentGameWith), where RetroArch's auto-load
// (savestate_resume.go) puts it back where it was. The startup lock is
// honoured first, and B held at power-on skips the resume. On a NextUI card,
// with no marker of JukaHub's, the game minarch saved when it powered off
// (NextUI's auto_resume.txt, slot 9) is resumed the same way through its pak —
// when JukaHub, not NextUI, is the first UI of the power-on.
//
// Power key while music plays. With JukaHub's music playing and no game
// running, a press darkens the screen instead of sleeping (Settings → Music
// and the power button): the clock dock's dark mode (ambient.go), so the
// backlight is off, nothing is drawn and the CPU drops to the dock's music
// profile (saver_power.go). The buttons follow Music → Screen-off controls,
// except that "Any key" counts as "Controls" here: the power key is a
// deliberate lock, and a stray press in a pocket must not light the screen.
// The power key wakes it (powerkey.go asks quickMusicDarkPress first). When
// the music stops, or stays paused, for quickDarkQuietGrace the device goes to
// the normal sleep. Auto sleep never interrupts music: idleScreenHoldOff
// already counts a loaded track, playing or paused.
//
// Nothing here was run on a device from this desk.

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
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// sleepPowerOffKey is the Custom setting: how long asleep before the
	// power off, in whole hours ("6", the only form older builds wrote) or in
	// minutes with an "m" ("15m"); "0" is Off and missing is quickSleepDefault.
	sleepPowerOffKey = "sleepPowerOff"
	// sleepPowerOffLastKey remembers the last delay that was not Off, for the
	// Quick Settings toggle to turn back on. Bookkeeping (cfgdocRuntimeKeys).
	sleepPowerOffLastKey = "sleepPowerOffLast"
	// sleepPowerOffMax bounds a hand-edited delay: a week asleep is no
	// deadline any more, and the arithmetic stays far from overflowing.
	sleepPowerOffMax = 7 * 24 * time.Hour
	// resumeAtPowerOnKey switches the power-on resume (default on).
	resumeAtPowerOnKey = "resumeAtPowerOn"
	// powerKeyMusicKey is what the power key does while music plays:
	// "screen-off" (the default) or "sleep".
	powerKeyMusicKey = "powerKeyMusic"

	// quickDeadlineSlack is how much short of the deadline a wake may come
	// and still be the deadline's: RTCs count whole seconds and the clocks
	// that measure the sleep drift a little.
	quickDeadlineSlack = time.Minute
	// quickDeadlineLate is how far past the deadline a wake may come and
	// still be the RTC's: the alarm wakes the device within seconds of its
	// time, so a wake long after it came from something else (a press on an
	// RTC whose alarm cannot wake the device, the key's reader missing it).
	quickDeadlineLate = 3 * time.Minute
	// quickDeadlineAlarmGuard keeps the device on for an alarm due this soon
	// after the deadline wake: powering off would silence it.
	quickDeadlineAlarmGuard = 15 * time.Minute
	// quickDeadlinePressWait is how long after the wake the power key's reader
	// gets to report a press, so a press that happened to coincide with the
	// deadline still wakes the device instead of powering it off.
	quickDeadlinePressWait = 1500 * time.Millisecond
	// quickChildReapWait bounds the wait for the game's launch to finish its
	// own cleanup (its RetroArch config put back) after the emulator exited.
	quickChildReapWait = 6 * time.Second
	// quickPowerOffExitWait is how long the sleep waits for the power off to
	// take the process down before it gives the device back.
	quickPowerOffExitWait = 90 * time.Second
	// quickDarkQuietGrace is how long the dark screen waits once the music
	// stopped or paused before the device sleeps.
	quickDarkQuietGrace = 2 * time.Minute
	// quickDarkSeek is the D-pad's step while the screen is dark, as in the
	// player.
	quickDarkSeek = 10.0
	// quickResumeMarkerName is the marker's file in the data directory.
	quickResumeMarkerName = "resume-at-power-on.json"
	// quickResumeBootFrames is how many main-loop ticks pass before the resume
	// launches: the first frame's bookkeeping (startup.go, the boot guard's
	// health clock) runs before a game takes the screen.
	quickResumeBootFrames = 2
	// quickResumeStartupWait bounds the wait for the tasks queued for after
	// the first frame (the network monitor, Syncthing, the companion…): they
	// only run while JukaHub has the screen, so a game started over them
	// would leave them waiting until it quits.
	quickResumeStartupWait = 2500 * time.Millisecond
)

// sleepPowerOffSteps are the row's stops; 0 is Off. The short ones are for a
// handheld put down "for a minute" whose RAM should not stay powered for
// long, 48 h for one left asleep over a weekend: the range NextUI's Auto
// Power Off pak offers (2026-11), around JukaHub's own 6 h default.
var sleepPowerOffSteps = []time.Duration{
	0, 15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour,
	6 * time.Hour, 8 * time.Hour, 12 * time.Hour, 24 * time.Hour, 48 * time.Hour,
}

// quickCfg mirrors the settings for the sleep goroutine and the power key's
// reader, written once a second on the main thread (quickResumeTick).
var quickCfg struct {
	noted        atomic.Bool
	sleepMinutes atomic.Int32
	musicSleep   atomic.Bool
}

// quickDark is the music screen-off. active and the two requests are read
// from any goroutine; the rest is main-thread state.
var quickDark struct {
	active   atomic.Bool
	enterReq atomic.Bool
	wakeReq  atomic.Bool

	ownDock         bool      // the dock was opened for the dark screen
	quietSince      time.Time // when the music stopped or paused (zero: playing)
	leaveAfterSleep bool      // the sleep it went to started from a dock it owns
}

// quickBoot is the power-on resume waiting for its frame. Main thread.
var quickBoot struct {
	pending bool
	ticks   int
	since   time.Time // the first tick
	marker  quickResumeMarker
	// nextui: the game is NextUI's auto-resume (minarch's slot 9).
	nextui bool
	// drop are the marker files read at startup, removed by
	// quickResumeBootCommit once the start goes on.
	drop []string
}

// quickPowerOff is a power off this file asked for.
var quickPowerOff struct {
	// silent: the sleep's own power off, with no chime in a pocket.
	silent atomic.Bool
	// busy: Power off is saving a running game first; saved lets its second
	// request through.
	busy  atomic.Bool
	saved atomic.Bool
}

// quickTickAt throttles the settings mirror. Main thread.
var quickTickAt time.Time

// ── Settings ─────────────────────────────────────────────────────────────

func init() {
	// The toggle's memory is not a setting anyone chose on its own.
	cfgdocRuntimeKeys[sleepPowerOffLastKey] = true
}

// quickSleepDefault is Power off after sleeping's default: 6 h where the
// kernel has real suspend ("mem", deep): a handheld put down in the morning
// is still instant in the evening, and one forgotten overnight is off with
// its game saved before the drain matters. 1 h on a light sleep ("freeze",
// or "mem" that is s2idle), which keeps enough awake to empty a battery in a
// day.
func quickSleepDefault() time.Duration {
	if quickLightSleep() {
		return time.Hour
	}
	return 6 * time.Hour
}

// quickLightSleep reports whether this kernel's suspend only freezes
// ("freeze", or "mem" whose mem_sleep mode is s2idle).
func quickLightSleep() bool {
	s := quickSuspendState()
	return s == "freeze" || s == "s2idle"
}

// sleepPowerOffParse reads a stored delay: whole hours ("6") or minutes with
// an "m" ("15m"); "0" is Off. ok is false for anything else, which then
// counts as missing. A delay past sleepPowerOffMax is held there.
func sleepPowerOffParse(s string) (time.Duration, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	unit := time.Hour
	if strings.HasSuffix(s, "m") {
		s, unit = strings.TrimSpace(strings.TrimSuffix(s, "m")), time.Minute
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, false
	}
	if time.Duration(n) > sleepPowerOffMax/unit {
		return sleepPowerOffMax, true
	}
	return time.Duration(n) * unit, true
}

// sleepPowerOffFormat is a delay's stored form: whole hours as every build
// wrote them (an older build reads those), anything shorter in minutes.
func sleepPowerOffFormat(d time.Duration) string {
	switch {
	case d <= 0:
		return "0"
	case d%time.Hour == 0:
		return strconv.Itoa(int(d / time.Hour))
	}
	return strconv.Itoa(int(d/time.Minute)) + "m"
}

// sleepPowerOffDelay is the chosen delay; 0 is Off.
func sleepPowerOffDelay(c *Config) time.Duration {
	if c != nil {
		if s, ok := customString(c.Variables.Custom, sleepPowerOffKey); ok {
			if d, ok := sleepPowerOffParse(s); ok {
				return d
			}
		}
	}
	return quickSleepDefault()
}

// sleepPowerOffLast is the delay the quick toggle turns back on: the last
// one chosen that was not Off, else the default.
func sleepPowerOffLast(c *Config) time.Duration {
	if c != nil {
		if s, ok := customString(c.Variables.Custom, sleepPowerOffLastKey); ok {
			if d, ok := sleepPowerOffParse(s); ok && d > 0 {
				return d
			}
		}
	}
	return quickSleepDefault()
}

// sleepPowerOffSet stores a delay chosen in Settings or Quick Settings, and
// remembers one that is not Off for the toggle. Main thread.
func sleepPowerOffSet(c *Config, d time.Duration) {
	if c == nil {
		return
	}
	if d > 0 {
		// Bookkeeping goes straight into Custom; setStoreString below saves
		// it with the setting.
		if c.Variables.Custom == nil {
			c.Variables.Custom = map[string]interface{}{}
		}
		c.Variables.Custom[sleepPowerOffLastKey] = sleepPowerOffFormat(d)
	}
	setStoreString(c, sleepPowerOffKey, sleepPowerOffFormat(d))
	quickResumeNoteConfig(c)
}

// sleepPowerOffToggle is Quick Settings' A on the row: Off, or back on at the
// last delay. It reports the delay now in force. Main thread.
func sleepPowerOffToggle(c *Config) time.Duration {
	d := time.Duration(0)
	if sleepPowerOffDelay(c) <= 0 {
		d = sleepPowerOffLast(c)
	}
	sleepPowerOffSet(c, d)
	return d
}

// sleepPowerOffStep is the stop after cur (dir > 0) or before it; a value
// between stops (a hand-edited one) moves to the nearest stop that way. The
// ends hold: Left never wraps round to 48 h, nor Right to Off.
func sleepPowerOffStep(cur time.Duration, dir int) time.Duration {
	if dir > 0 {
		for _, d := range sleepPowerOffSteps {
			if d > cur {
				return d
			}
		}
		return cur
	}
	for i := len(sleepPowerOffSteps) - 1; i >= 0; i-- {
		if sleepPowerOffSteps[i] < cur {
			return sleepPowerOffSteps[i]
		}
	}
	return 0
}

// sleepPowerOffStops are the Settings row's stops: sleepPowerOffSteps, with
// the stored delay in its place when it is none of them (an older build's
// 3 h, a hand-edited value), so the row shows it and steps on from it.
func sleepPowerOffStops(c *Config) []time.Duration {
	cur := sleepPowerOffDelay(c)
	out := make([]time.Duration, 0, len(sleepPowerOffSteps)+1)
	added := false
	for _, d := range sleepPowerOffSteps {
		if d == cur {
			added = true
		} else if !added && d > cur {
			out = append(out, cur)
			added = true
		}
		out = append(out, d)
	}
	if !added {
		out = append(out, cur)
	}
	return out
}

// sleepPowerOffQuickAvailable reports whether Quick Settings offers the row:
// a handheld JukaHub powers off, with an RTC that can wake it.
func sleepPowerOffQuickAvailable() bool {
	return devPowerSupported() && quickRTCWakeSupported()
}

// sleepPowerOffLabel is a delay's text: "Off", "15 min", "6 h".
func sleepPowerOffLabel(d time.Duration) string {
	switch {
	case d <= 0:
		return "Off"
	case d%time.Hour == 0:
		return strconv.Itoa(int(d/time.Hour)) + " h"
	}
	return strconv.Itoa(int(d/time.Minute)) + " min"
}

// sleepPowerOffDetail says what the row does on this kernel, and what it
// cannot do.
func sleepPowerOffDetail(c *Config) string {
	switch {
	case !quickRTCWakeSupported():
		return "This device has no RTC wake alarm, so it cannot wake itself to power off"
	case sleepPowerOffDelay(c) <= 0:
		return "Stays asleep until the battery runs out"
	case quickLightSleep():
		return "Saves the game, then powers off. This kernel only has light sleep (s2idle), which drains faster"
	case nextuiActive():
		// NextUI games sleep in minarch, which saved them first (nextui_sleep.go).
		return "Saves the game, then powers off; NextUI games once minarch's own auto-save is found. Never a game it cannot save"
	}
	return "Saves the game, then powers off; never on a charger, never a game it cannot save"
}

// sleepPowerOffSettingsRow is Settings → Power → Power off after sleeping.
func sleepPowerOffSettingsRow() setRow {
	return setRow{
		ID: sleepPowerOffKey, Label: "Power off after sleeping", Kind: setKindChoice,
		// Off and 48 h are ends, as in Quick Settings: no wrapping round.
		Clamp:   true,
		Detail:  sleepPowerOffDetail,
		Visible: func(*Config) bool { return devPowerSupported() },
		Options: func(c *Config) []setOption {
			stops := sleepPowerOffStops(c)
			out := make([]setOption, 0, len(stops))
			for _, d := range stops {
				out = append(out, setOption{Value: sleepPowerOffFormat(d), Label: sleepPowerOffLabel(d)})
			}
			return out
		},
		Current: func(c *Config) int {
			cur := sleepPowerOffDelay(c)
			for i, d := range sleepPowerOffStops(c) {
				if d == cur {
					return i
				}
			}
			return -1
		},
		Value: func(c *Config) string { return sleepPowerOffLabel(sleepPowerOffDelay(c)) },
		Choose: func(c *Config, value string) {
			d, ok := sleepPowerOffParse(value)
			if !ok {
				return
			}
			sleepPowerOffSet(c, d)
			setToast("Power off after sleeping: " + sleepPowerOffLabel(d))
		},
	}
}

// resumeAtPowerOnEnabled reads the switch; on unless turned off.
func resumeAtPowerOnEnabled(c *Config) bool {
	if c != nil {
		if v, ok := customValue(c.Variables.Custom, resumeAtPowerOnKey); ok {
			return coerceBool(v)
		}
	}
	return true
}

// resumeAtPowerOnSettingsRow is Settings → Power → Resume game at power on.
func resumeAtPowerOnSettingsRow() setRow {
	return setRow{
		ID: resumeAtPowerOnKey, Label: "Resume game at power on", Kind: setKindToggle,
		Detail:  setStatic("A game saved by the power off starts where it was; hold B at power-on to skip"),
		Visible: func(*Config) bool { return devPowerSupported() },
		Get:     resumeAtPowerOnEnabled,
		Set: func(c *Config, on bool) {
			setStoreBool(c, resumeAtPowerOnKey, on)
			setToast("Resume game at power on: " + setOnOff(on))
		},
	}
}

// powerKeyMusicSleeps reports whether the power key sleeps even while music
// plays; the default is the screen-off.
func powerKeyMusicSleeps(c *Config) bool {
	if c == nil {
		return false
	}
	v, _ := customString(c.Variables.Custom, powerKeyMusicKey)
	return strings.EqualFold(strings.TrimSpace(v), "sleep")
}

// powerKeyMusicSettingsRow is Settings → Power → Power button while music
// plays, shown where the power key is read.
func powerKeyMusicSettingsRow() setRow {
	options := []setOption{
		{Value: "screen-off", Label: "Screen off", Note: "The music keeps playing; the power button wakes the screen"},
		{Value: "sleep", Label: "Sleep", Note: "The device sleeps and the music pauses"},
	}
	return setRow{
		ID: powerKeyMusicKey, Label: "Power button while music plays", Kind: setKindChoice,
		Detail: func(c *Config) string {
			if powerKeyMusicSleeps(c) {
				return "A press sleeps and pauses the music"
			}
			return "Screen off, music on; buttons follow the music player's screen-off controls"
		},
		Visible: func(*Config) bool { return powerKeyWatcherLive() },
		Options: func(*Config) []setOption { return options },
		Current: func(c *Config) int {
			if powerKeyMusicSleeps(c) {
				return 1
			}
			return 0
		},
		Value: func(c *Config) string {
			if powerKeyMusicSleeps(c) {
				return options[1].Label
			}
			return options[0].Label
		},
		Choose: func(c *Config, value string) {
			setStoreString(c, powerKeyMusicKey, value)
			quickResumeNoteConfig(c)
			for _, o := range options {
				if o.Value == value {
					setToast("Power button while music plays: " + o.Label)
				}
			}
		},
	}
}

// quickResumeNoteConfig mirrors the settings for the other goroutines. Main
// thread.
func quickResumeNoteConfig(c *Config) {
	quickCfg.sleepMinutes.Store(int32(sleepPowerOffDelay(c) / time.Minute))
	quickCfg.musicSleep.Store(powerKeyMusicSleeps(c))
	quickCfg.noted.Store(true)
}

// quickSleepDeadline is how long a sleep lasts before the power off, or 0
// for none. Any goroutine.
func quickSleepDeadline() time.Duration {
	d := quickSleepDefault()
	if quickCfg.noted.Load() {
		d = time.Duration(quickCfg.sleepMinutes.Load()) * time.Minute
	}
	if d <= 0 || !devPowerSupported() {
		return 0
	}
	return d
}

// ── The main-thread tick ────────────────────────────────────────────────

// quickResumeTick is this file's share of the main loop, called from
// powerKeyTick every frame, parked or not, and from the dark dock's loop.
func quickResumeTick(config *Config, parked bool) {
	now := time.Now()
	if now.Sub(quickTickAt) >= time.Second {
		quickTickAt = now
		quickResumeNoteConfig(config)
	}
	quickBootTick(parked)
	quickDarkTick(config, now, parked)
}

// ── Deep sleep → power off ───────────────────────────────────────────────

// quickDeadlineOutcome is what quickSleepAtDeadline did.
type quickDeadlineOutcome int

const (
	// quickDeadlineWake gives the device back as after any wake.
	quickDeadlineWake quickDeadlineOutcome = iota
	// quickDeadlineSleepAgain sleeps once more, the deadline re-armed.
	quickDeadlineSleepAgain
	// quickDeadlinePoweringOff: the power off is under way.
	quickDeadlinePoweringOff
)

// quickSleepDeadlineWake reports whether a wake after slept, from a suspend
// that started at the monotonic time from, was the power-off deadline's: the
// sleep covered it, the RTC held an alarm before the suspend (rtcArmed) and
// no longer does, no alarm is due soon and no power press came in. Without
// that RTC proof a press the key's reader did not see (no watcher, an RTC
// that could not be armed) would power the device off. It waits
// quickDeadlinePressWait for the key's reader. The sleep goroutine.
func quickSleepDeadlineWake(slept, deadline, from time.Duration, rtcArmed bool) bool {
	if deadline <= 0 || !rtcArmed || slept < deadline-quickDeadlineSlack || slept > deadline+quickDeadlineLate {
		return false
	}
	if at := powerSleepNextAlarm.Load(); at > 0 && time.Until(time.Unix(0, at)) < quickDeadlineAlarmGuard {
		logInfo("sleep", "power-off time reached, but an alarm is due: staying on for it")
		return false
	}
	if pending, known := powerRTCAlarmPending(); !known || pending {
		// The RTC still holds the alarm (or cannot say): something else
		// woke the device.
		return false
	}
	time.Sleep(quickDeadlinePressWait)
	if at := time.Duration(powerSleep.pressAt.Load()); at != 0 && at >= from {
		return false
	}
	return true
}

// quickSleepAtDeadline is the deadline wake's work: sleep again, power off,
// or (quitting) give the device back. held is the sleep's: a game saved and
// gone is taken out of it. The sleep goroutine; the screen stays off.
func quickSleepAtDeadline(held *powerSleepHeld) quickDeadlineOutcome {
	if exitRequested.Load() {
		return quickDeadlineWake
	}
	// Read now, not getBatteryCharging's cache: its age is measured on the
	// monotonic clock, which stood still while the device slept.
	if batteryChargingIn(powerSupplyRoot) {
		logInfo("sleep", "power-off time reached on a charger: sleeping again")
		return quickDeadlineSleepAgain
	}
	var marker *quickResumeMarker
	if gameScreenSuspended() {
		m, ok, why := quickSaveHeldGame(held)
		if !ok {
			logWarn("sleep", "power-off time reached, but %s: sleeping again so nothing is lost", why)
			return quickDeadlineSleepAgain
		}
		marker = m
	}
	if marker != nil {
		quickResumeWriteMarker(*marker)
	}
	logInfo("sleep", "asleep long enough (Power off after sleeping): powering off")
	quickPowerOff.silent.Store(true)
	devPowerRequest(devPowerPoweroff)
	return quickDeadlinePoweringOff
}

// quickSleepAwaitPowerOff holds the sleep while the power off takes the
// process down. If it never does, the device is given back.
func quickSleepAwaitPowerOff() {
	time.Sleep(quickPowerOffExitWait)
	logWarn("sleep", "the power off did not happen within %s; waking up", quickPowerOffExitWait)
	quickPowerOff.silent.Store(false)
}

// quickPowerOffSilent reports whether the shutdown under way is the sleep's
// own power off, which plays no chime (bootchime.go).
func quickPowerOffSilent() bool { return quickPowerOff.silent.Load() }

// quickSaveHeldGame saves the game the sleep stopped and quits it. It reports
// the marker for the next power-on (nil when the game cannot be named),
// whether the state was saved, and if not, why. A game that could not be
// saved is stopped again. The sleep goroutine.
func quickSaveHeldGame(held *powerSleepHeld) (*quickResumeMarker, bool, string) {
	cmds := liveScreenChildren()
	if len(cmds) == 0 {
		return nil, false, "a program JukaHub did not start owns the screen"
	}
	heldPids := make(map[int]bool, len(held.children))
	for _, ch := range held.children {
		heldPids[ch.pid] = true
	}
	for _, c := range cmds {
		if c == nil || c.Process == nil || !heldPids[c.Process.Pid] {
			return nil, false, "the game is paused by something else (the in-game menu or an alarm)"
		}
	}
	if !retroArchRunning() {
		return nil, false, "the game is not running in RetroArch, the one emulator JukaHub can ask to save"
	}
	if ok, why := retroArchNetCmdReady(); !ok {
		return nil, false, why
	}
	marker, named := quickRunningGameMarker(cmds)
	// The game runs again for the moment the save takes; the screen is off.
	for _, ch := range held.children {
		if err := ch.stop(false); err != nil {
			logWarn("sleep", "could not continue the game to save it (pid %d): %v", ch.pid, err)
		}
	}
	if saved, why := retroArchSaveAndQuit("sleep power-off"); !saved {
		for _, ch := range held.children {
			if err := ch.stop(true); err != nil {
				logWarn("sleep", "could not pause the game again (pid %d): %v", ch.pid, err)
			}
		}
		return nil, false, why
	}
	held.children = nil
	quickAwaitGameCleanup()
	if !named {
		logInfo("sleep", "game saved; it cannot be resumed at power-on (not a library launch, or save-state resume is off)")
		return nil, true, ""
	}
	return &marker, true, ""
}

// quickAwaitGameCleanup waits, bounded, for the launch's own end: the screen
// handed back and the game's RetroArch config restored (game_library.go).
func quickAwaitGameCleanup() {
	deadline := time.Now().Add(quickChildReapWait)
	for gameScreenSuspended() && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
}

// quickRunningGameMarker names the running game for the resume: the newest
// library launch, when its ROM is what the running child was given, it is a
// RetroArch launch and save-state resume is on (without auto-load the
// relaunch would start from the title screen). Off the main thread.
func quickRunningGameMarker(cmds []*exec.Cmd) (quickResumeMarker, bool) {
	if !savestateResumeEnabled() {
		return quickResumeMarker{}, false
	}
	recent := recentGames()
	if len(recent) == 0 {
		return quickResumeMarker{}, false
	}
	g := recent[0]
	switch g.System {
	case androidLibrarySystem, html5LibrarySystem, portsLibrarySystem:
		return quickResumeMarker{}, false
	}
	if strings.TrimSpace(g.Path) == "" || !quickArgsMention(cmds, filepath.Base(g.Path)) {
		return quickResumeMarker{}, false
	}
	// The relaunch must be RetroArch too: another emulator (a NextUI pak
	// picked since, a standalone one) has no auto-load and would start the
	// game from its title screen while claiming a resume.
	launcher := resolveGameLauncherFor(libraryGame{Path: g.Path, System: g.System, Title: g.Title})
	if launcher == "" || !retroArchLauncher(launcher) {
		return quickResumeMarker{}, false
	}
	return quickResumeMarker{
		Path: g.Path, System: g.System, Title: g.Title, Launcher: launcher, BootID: quickBootID(),
	}, true
}

// quickArgsMention reports whether any child was started with name in an
// argument: firmwareROMArg rewrites the ROM's folder, never its file name.
func quickArgsMention(cmds []*exec.Cmd, name string) bool {
	if name == "" || name == "." {
		return false
	}
	for _, c := range cmds {
		if c == nil {
			continue
		}
		for _, a := range c.Args {
			if strings.Contains(a, name) {
				return true
			}
		}
	}
	return false
}

// quickPowerOffSavesFirst is devPowerRequest's Power off with a game still
// running: RetroArch is asked to save and quit first (and the resume marker
// is written when it did), then the request is made again. It reports
// whether it took the request over. A game it cannot save is ended by the
// teardown as before. Any goroutine: the work runs on its own.
func quickPowerOffSavesFirst() bool {
	if quickPowerOff.saved.Swap(false) {
		return false
	}
	cmds := liveScreenChildren()
	if len(cmds) == 0 {
		return false
	}
	if !quickPowerOff.busy.CompareAndSwap(false, true) {
		return true
	}
	go func() {
		defer quickPowerOff.busy.Store(false)
		if retroArchRunning() {
			if ok, why := retroArchNetCmdReady(); !ok {
				logWarn("power", "power off with a game running: cannot save it (%s)", why)
			} else {
				marker, named := quickRunningGameMarker(cmds)
				if saved, why := retroArchSaveAndQuit("power off"); !saved {
					logWarn("power", "power off with a game running: the save failed (%s)", why)
				} else {
					quickAwaitGameCleanup()
					if named {
						quickResumeWriteMarker(marker)
					}
				}
			}
		}
		quickPowerOff.saved.Store(true)
		devPowerRequest(devPowerPoweroff)
	}()
	return true
}

// ── The resume marker ────────────────────────────────────────────────────

// quickResumeMarker is the game the next power-on starts.
type quickResumeMarker struct {
	Path     string `json:"path"`
	System   string `json:"system"`
	Title    string `json:"title,omitempty"`
	Launcher string `json:"launcher,omitempty"`
	// BootID is the kernel's boot id when it was written: the same boot
	// (JukaHub restarted, the power off failed) is not a power-on.
	BootID string `json:"boot_id,omitempty"`
}

// quickResumeMarkerPath is the marker's file, or "" without a data
// directory.
func quickResumeMarkerPath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, quickResumeMarkerName)
}

// quickResumeWriteMarker writes the marker through AtomicWrite (fsync and
// rename), so the power off right after cannot leave half of it.
func quickResumeWriteMarker(m quickResumeMarker) {
	path := quickResumeMarkerPath()
	if path == "" {
		return
	}
	data, err := json.Marshal(m)
	if err == nil {
		err = AtomicWrite(path, append(data, '\n'), 0o644)
	}
	if err != nil {
		logWarn("power", "could not write the resume marker: %v", err)
		return
	}
	logInfo("power", "%s resumes at the next power-on", filepath.Base(m.Path))
}

// quickResumeReadMarker reads the marker without removing it: present
// reports a file to remove (quickResumeBootCommit removes it, used or not, a
// damaged one included), ok a game to resume. A missing one is not an error.
func quickResumeReadMarker() (m quickResumeMarker, present, ok bool) {
	path := quickResumeMarkerPath()
	if path == "" {
		return m, false, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logWarn("power", "could not read the resume marker: %v", err)
		}
		return m, false, false
	}
	quickBoot.drop = append(quickBoot.drop, path)
	if err := json.Unmarshal(data, &m); err != nil || strings.TrimSpace(m.Path) == "" {
		logWarn("power", "ignoring a damaged resume marker")
		return quickResumeMarker{}, true, false
	}
	return m, true, true
}

// quickResumeBootCommit removes the markers quickResumeBootPlan read, once
// the start is known to go on: main.go calls it right after the travel lock's
// power-on check, which turns a locked device straight back off and must
// leave the resume for the power-on after it. From here, whatever happens,
// each marker is used once. One that cannot be removed (a read-only card) is
// not used at all: it would start the game at every power-on. Main thread,
// once.
func quickResumeBootCommit() {
	for _, path := range quickBoot.drop {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			if quickBoot.pending {
				logWarn("power", "could not remove %s, so nothing is resumed: %v", path, err)
			}
			quickBoot.pending = false
		}
	}
	quickBoot.drop = nil
}

// quickPowerOnStart reports whether this start is the device's power-on,
// whatever the startup animation setting says: bootos.go's own decision
// without "Always quick" and "Full every time", so a relaunch (an update, a
// game handing the screen back) is never one.
func quickPowerOnStart(config *Config) bool {
	in := bootOSLiveInputs(config, time.Now())
	in.Setting, in.Bench = "", false
	on, _ := bootOSDecide(in)
	return on
}

// ── Resume at power-on ───────────────────────────────────────────────────

// quickResumeBootPlan is main.go's step right after the boot plan: with a
// marker to resume, the boot animation is skipped (the allotment becomes 0)
// and the game starts on the first frames. Without one, NextUI's own
// auto-resume may name a game (quickResumeNextUIPlan). The markers are only
// read here; quickResumeBootCommit removes them. Main thread, once.
func quickResumeBootPlan(config *Config, allotment time.Duration) time.Duration {
	m, present, ok := quickResumeReadMarker()
	if !present {
		return quickResumeNextUIPlan(config, allotment)
	}
	if !ok {
		return allotment
	}
	switch {
	case m.BootID != "" && m.BootID == quickBootID():
		logInfo("power", "resume marker from this same boot: not a power-on, nothing resumed")
	case !resumeAtPowerOnEnabled(config):
		logInfo("power", "resume at power-on is off: %s not resumed", filepath.Base(m.Path))
	case !regularFile(m.Path):
		logWarn("power", "the game to resume is gone: %s", m.Path)
	default:
		quickBoot.pending, quickBoot.ticks, quickBoot.marker = true, 0, m
		logInfo("startup", "resuming %s at power-on: boot animation skipped", filepath.Base(m.Path))
		return 0
	}
	return allotment
}

// quickResumeNextUIPlan is the power-on resume of a NextUI card's game: the
// one minarch saved (slot 9) and named in NextUI's auto_resume.txt when it
// powered the device off. Only when JukaHub, not NextUI, is the first UI of
// this power-on — started by NextUI, NextUI has already resumed it itself —
// and the state is really there. The game then starts through its pak with
// minarch's resume armed (nextuiRequestResume), by the same tick, B and
// startup-lock rules as JukaHub's own marker; NextUI's marker is consumed as
// NextUI would (quickResumeBootCommit), so NextUI does not resume it again
// later. Main thread, once.
func quickResumeNextUIPlan(config *Config, allotment time.Duration) time.Duration {
	if !nextuiActive() || nextuiStartedByNextUI() || !resumeAtPowerOnEnabled(config) {
		return allotment
	}
	path, marker, ok := nextuiPeekAutoResume()
	if !ok || !quickPowerOnStart(config) {
		return allotment
	}
	pak := nextuiPakFor(path)
	switch {
	case !nextuiResumeStatePresent(path):
		// Left for NextUI: without the state there is nothing to resume here.
		logInfo("power", "NextUI's auto-resume names %s, but minarch's resume state is missing: not resumed", filepath.Base(path))
		return allotment
	case pak == "":
		logInfo("power", "NextUI's auto-resume names %s, but no pak plays it: not resumed", filepath.Base(path))
		return allotment
	}
	quickBoot.drop = append(quickBoot.drop, marker)
	quickBoot.pending, quickBoot.ticks, quickBoot.nextui = true, 0, true
	quickBoot.marker = quickResumeMarker{Path: path, System: nextuiROMSystem(path), Launcher: pak}
	logInfo("startup", "resuming %s at power-on (NextUI's auto-resume): boot animation skipped", filepath.Base(path))
	return 0
}

// quickBootTick launches the pending resume once the first frame is up and
// the startup lock is open. Main thread.
func quickBootTick(parked bool) {
	if !quickBoot.pending {
		return
	}
	if exitRequested.Load() || parked {
		// Quitting, or something else took the screen first.
		quickBoot.pending = false
		return
	}
	quickBoot.ticks++
	if quickBoot.since.IsZero() {
		quickBoot.since = time.Now()
	}
	if quickBoot.ticks < quickResumeBootFrames || startupLocked() || emuPickUI.open {
		return
	}
	if quickStartupTasksLeft() && time.Since(quickBoot.since) < quickResumeStartupWait {
		return
	}
	quickBoot.pending = false
	m := quickBoot.marker
	if gameController != nil && gameController.Button(sdl.CONTROLLER_BUTTON_B) != 0 {
		logInfo("power", "B held at power-on: %s not resumed", filepath.Base(m.Path))
		ShowToast("Resume skipped", ToastKindInfo)
		return
	}
	title := m.Title
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(m.Path), filepath.Ext(m.Path))
	}
	g := recentGame{Path: m.Path, System: m.System, Title: title}
	logInfo("power", "resuming %s", title)
	if quickBoot.nextui {
		// minarch's slot 9 through the game's pak, never another emulator:
		// only minarch has that state.
		if !regularFile(m.Launcher) {
			logWarn("power", "the pak for %s is gone: not resumed", title)
			return
		}
		nextuiRequestResume(m.Path)
		playRecentGameWith(g, libraryGame{Path: g.Path, System: g.System, Title: g.Title}, m.Launcher)
		return
	}
	if m.Launcher != "" && regularFile(m.Launcher) {
		playRecentGameWith(g, libraryGame{Path: g.Path, System: g.System, Title: g.Title}, m.Launcher)
		return
	}
	// The launcher moved: the Continue row's own choice (it may ask).
	playRecentGame(g)
}

// quickStartupTasksLeft reports whether tasks queued for after the first
// frame are still waiting (startup.go).
func quickStartupTasksLeft() bool {
	deferredStartup.mu.Lock()
	defer deferredStartup.mu.Unlock()
	return len(deferredStartup.tasks) > 0
}

// ── Power key while music plays ─────────────────────────────────────────

// quickMusicPlaying reports JukaHub's music playing, not paused.
func quickMusicPlaying() bool {
	music.mu.Lock()
	defer music.mu.Unlock()
	return music.playing && !music.paused
}

// quickMusicPowerKey is sleepNow's first look at a power-key sleep: while
// music plays (and no game runs) the press darkens the screen instead, and a
// press while it is dark wakes it. It reports whether it took the press.
// Any goroutine.
func quickMusicPowerKey() bool {
	if quickDark.active.Load() {
		quickDark.wakeReq.Store(true)
		return true
	}
	if quickDark.enterReq.Swap(false) {
		return true // a second press before the first took effect: neither
	}
	if quickCfg.musicSleep.Load() || gameScreenSuspended() || !quickMusicPlaying() {
		return false
	}
	quickDark.enterReq.Store(true)
	return true
}

// quickMusicDarkPress is the power key's press while the music screen is
// dark (powerkey.go): the wake, at once, without the firmware wait. Any
// goroutine.
func quickMusicDarkPress() bool {
	if !quickDark.active.Load() {
		return false
	}
	quickDark.wakeReq.Store(true)
	return true
}

// quickDarkTick runs the dark screen: entering and waking on request,
// noticing another wake, and the sleep once the music has been quiet for
// quickDarkQuietGrace. Main thread.
func quickDarkTick(config *Config, now time.Time, parked bool) {
	if quickDark.leaveAfterSleep && !powerSleepActive() {
		quickDark.leaveAfterSleep = false
		if ambientIsActive() {
			ambientLeave()
		}
	}
	if quickDark.enterReq.Swap(false) && !parked {
		quickDarkEnter(config)
	}
	if !quickDark.active.Load() {
		quickDark.wakeReq.Store(false)
		return
	}
	if quickDark.wakeReq.Swap(false) {
		quickDarkWake("the power button")
		return
	}
	if parked {
		// A game took the screen while it was dark (a phone's launch): it
		// must not run unlit, nor be slept by the quiet-music timer.
		quickDarkWake("a game started")
		return
	}
	if !ambientIsScreenOff() {
		// Woken another way (an alarm, the dock's own wake): it is the
		// dock's now.
		quickDark.active.Store(false)
		quickDark.ownDock, quickDark.quietSince = false, time.Time{}
		return
	}
	if quickMusicPlaying() {
		quickDark.quietSince = time.Time{}
		return
	}
	if quickDark.quietSince.IsZero() {
		quickDark.quietSince = now
		return
	}
	if now.Sub(quickDark.quietSince) < quickDarkQuietGrace {
		return
	}
	logInfo("power", "music stopped with the screen off: sleeping")
	quickDark.active.Store(false)
	quickDark.leaveAfterSleep = quickDark.ownDock
	quickDark.ownDock, quickDark.quietSince = false, time.Time{}
	sleepNow("music stopped")
}

// quickDarkBatteryAt throttles quickDarkBatteryTick. Main thread.
var quickDarkBatteryAt time.Time

// quickDarkBatteryTick is the low-battery guard while the dock is dark:
// main.go's dark branch skips widgetsTick, which runs it otherwise, and hours
// of music with the screen off must still warn and suspend before the
// battery dies. Once a second, as widgetsTick; the reading also feeds the
// battery trend behind the guard's "holding charge" test. Main thread.
func quickDarkBatteryTick(config *Config, now time.Time) {
	if !quickDarkBatteryAt.IsZero() && now.Sub(quickDarkBatteryAt) < widgetTickInterval {
		return
	}
	quickDarkBatteryAt = now
	batteryTrendNote(getBatteryPercent(), getBatteryCharging())
	lowBatteryTick(config)
}

// quickDarkLowBatteryHold is the low-battery guard's suspend with the music
// screen dark (lowbattery.go protectLowBattery): the music pauses first, as a
// sleep pauses it, so the device does not wake into a dark screen that spends
// the last percent on music. The dark screen itself stays as it was: the
// power key still lights it, and its quiet-music timer sleeps the device
// quickDarkQuietGrace after the wake. Any goroutine.
func quickDarkLowBatteryHold() {
	if !quickDark.active.Load() {
		return
	}
	music.mu.Lock()
	held := music.playing && !music.paused
	if held {
		music.paused = true
	}
	music.mu.Unlock()
	if held {
		ClearVideoAudioQueue()
		logInfo("power", "battery critical with the screen dark: music paused before the suspend")
	}
}

// quickDarkEnter darkens the screen with the music playing on: the dock's
// dark mode, opened for it when the dock was not up. Main thread.
func quickDarkEnter(config *Config) {
	if !quickMusicPlaying() || gameScreenSuspended() || powerSleepActive() {
		return
	}
	if ambientIsScreenOff() {
		// The dock darkened itself (its own screen-off, the sleep timer):
		// the press lights it, as the power button does once this is dark.
		ambientNotifyInput()
		return
	}
	own := !ambientIsActive()
	if own {
		ambientEnter(config)
	}
	ambientDarken()
	if !ambientIsScreenOff() {
		if own {
			ambientLeave()
		}
		return
	}
	quickDark.ownDock, quickDark.quietSince = own, time.Time{}
	quickDark.active.Store(true)
	// The power button used to sleep here: "Lock after sleep" goes up the
	// same way, before the screen comes back (bootlock.go).
	bootLockNoteSleep()
	logInfo("power", "power button with music playing: screen off, the music plays on")
}

// quickDarkWake lights the screen again: back to the screen the press came
// from when the dock was opened for the dark, else the dock. Main thread.
func quickDarkWake(why string) {
	quickDark.active.Store(false)
	own := quickDark.ownDock
	quickDark.ownDock, quickDark.quietSince = false, time.Time{}
	if own {
		ambientLeave()
	} else {
		ambientNotifyInput()
	}
	noteUserInputTime(time.Now())
	logInfo("power", "screen on again (%s)", why)
}

// quickDarkDropInput is the main loop's filter while the music screen is
// dark (powerSleepDropInput asks it): transport buttons act as Music →
// Screen-off controls says, Select wakes, and nothing reaches the screen
// underneath. Volume is left to the firmware and the volume-key watcher.
// Main thread.
func quickDarkDropInput(event sdl.Event) bool {
	if !quickDark.active.Load() || !ambientIsScreenOff() {
		return false
	}
	mode := musicScreenModeFromConfig(appConfig)
	if mode == musicScreenAny {
		mode = musicScreenControls
	}
	switch e := event.(type) {
	case *sdl.ControllerButtonEvent:
		if e.Type == sdl.CONTROLLERBUTTONDOWN {
			quickDarkAct(musicScreenOffButton(e.Button), mode, e.Button == uint8(sdl.CONTROLLER_BUTTON_DPAD_RIGHT))
		}
		return true
	case *sdl.KeyboardEvent:
		if e.Type == sdl.KEYDOWN && e.Repeat == 0 {
			quickDarkAct(musicScreenOffKey(e.Keysym.Sym), mode, e.Keysym.Sym == sdl.K_RIGHT)
		}
		return true
	case *sdl.TextInputEvent, *sdl.ControllerAxisEvent, *sdl.JoyButtonEvent, *sdl.JoyAxisEvent, *sdl.JoyHatEvent,
		*sdl.MouseButtonEvent, *sdl.MouseWheelEvent, *sdl.MouseMotionEvent, *sdl.TouchFingerEvent:
		return true
	}
	return false
}

// quickDarkAct runs one press on the dark screen. forward is a seek's
// direction. Main thread.
func quickDarkAct(action musicRemoteAction, mode musicScreenMode, forward bool) {
	switch {
	case action == musicActionWake:
		quickDarkWake("Select")
	case !musicScreenActionAllowed(mode, action):
	case action == musicActionPlayPause:
		musicTogglePause()
	case action == musicActionNext:
		musicNext()
	case action == musicActionPrev:
		musicPrev()
	case action == musicActionSeek:
		if forward {
			musicSeekBy(quickDarkSeek)
		} else {
			musicSeekBy(-quickDarkSeek)
		}
	}
}

// ── Platform facts, read once ────────────────────────────────────────────

var quickFacts struct {
	once    sync.Once
	state   string
	rtcWake bool
}

// quickFactsLoad reads the suspend state and the RTC wake alarm once.
func quickFactsLoad() {
	quickFacts.once.Do(func() {
		quickFacts.state = quickReadSuspendState()
		quickFacts.rtcWake = quickReadRTCWake()
	})
}

// quickMemSleepMode is the bracketed mode in a /sys/power/mem_sleep line
// ("s2idle [deep]" is "deep"), or "".
func quickMemSleepMode(line string) string {
	for _, f := range strings.Fields(line) {
		if strings.HasPrefix(f, "[") && strings.HasSuffix(f, "]") {
			return strings.Trim(f, "[]")
		}
	}
	return ""
}

// quickSuspendState is the sleep suspendDevice gets ("mem", "s2idle",
// "freeze"), or "" when the kernel has none.
func quickSuspendState() string {
	quickFactsLoad()
	return quickFacts.state
}

// quickRTCWakeSupported reports whether the RTC can wake the device.
func quickRTCWakeSupported() bool {
	quickFactsLoad()
	return quickFacts.rtcWake
}
