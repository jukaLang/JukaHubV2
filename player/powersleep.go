package main

// powersleep.go — putting the handheld to sleep from JukaHub (PWR-1).
//
// sleepNow is the one entry point: the power key (powerkey.go) calls it with
// "power-button", the in-game menu with "game-menu". It never blocks the
// caller — the work runs on its own goroutine, one sleep at a time — because
// the steps that matter can take seconds (a sync on a slow card) and the
// in-game menu calls it from the render thread.
//
// The order is the whole design:
//
//  1. Input is muted (powerSleepDropInput), so nothing pressed in a pocket, and
//     not the wake press either, reaches Home or a panel.
//  2. A running game, emulator, port or app is stopped (SIGSTOP to its process
//     group, the alarm's own helper alarmSignalChild), so its audio and its
//     game time freeze — unless something else already paused it: the in-game
//     menu / game assistant overlay and a ringing alarm own their pause, and a
//     group that is already stopped (/proc says "T") is left exactly as it is,
//     so waking never resumes a game under the in-game menu. Music playing in
//     JukaHub is paused the way the alarm pauses it.
//  3. The backlight goes off (devpower.go backlightOff, the display driver's
//     own switch on the TrimUI) and the disks are synced.
//  4. The next JukaHub alarm is put in the RTC's wakealarm, so a sleeping
//     device wakes up in time for it (JukaHub's alarms are its own timers; a
//     suspended process has none).
//  5. suspendDevice (lowbattery.go): "mem", else "freeze". The write returns
//     once the device is awake again.
//  6. Everything comes back in reverse: the RTC alarm is cleared, the
//     backlight returns to the level it had, the children JukaHub stopped (and
//     only those) continue, music resumes, and input stays muted a moment
//     longer so the wake press does not act on the screen.
//
// When the kernel cannot suspend (no /sys/power/state, not root, a driver that
// refuses), or comes straight back (a wakeup source fired at once), the sleep
// becomes a screen-off sleep: backlight off and the game stopped until the
// next power press. That fallback needs the power-key watcher, since nothing
// else can wake it once input is muted; without the watcher the screen comes
// back at once with a toast instead of leaving a dark device that ignores
// every button.
//
// devpower.go's power card Sleep and Auto sleep come through here too
// (devSleepNow calls sleepNow), so there is one suspend and one set of wake
// rules; its per-frame backlight guard leaves the light off while
// powerSleepActive.
//
// Every step is undone by a deferred release, panic or not: a game is never
// left stopped and the screen is never left dark because one step failed.
//
// Wi-Fi and Bluetooth are not touched: the drivers handle a system suspend
// themselves, and wake_resume.go already notices the gap and gets the network
// back (wifi_resume.go nudges a slow radio). The Bluetooth and PS wake code
// (bt*_*.go, psremote_wake.go) has no suspend hooks and needs none.

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// powerSleepWakeMute is how long input stays muted after a wake: long
	// enough for the wake press's own release (and SDL's copy of it) to pass.
	powerSleepWakeMute = 700 * time.Millisecond
	// powerSleepAlarmLead is how early the RTC wakes the device before an
	// alarm, so JukaHub is running again when the minute comes.
	powerSleepAlarmLead = 20 * time.Second
	// powerSleepAlarmHorizon bounds the RTC alarm to the next day; alarms are
	// daily, so nothing further is ever the next one.
	powerSleepAlarmHorizon = 24 * time.Hour
	// powerSleepSpuriousWake is the least a real suspend lasts: a write that
	// returned sooner, with the clocks showing no time asleep, came straight
	// back (a wakeup source fired) and turns into the screen-off sleep.
	powerSleepSpuriousWake = time.Second
)

// powerSleep is the state of the one sleep that can run at a time. Any
// goroutine.
var powerSleep struct {
	// active is set from sleepNow until everything has been given back.
	active atomic.Bool
	// screenOff is set while the fallback screen-off sleep waits for a press.
	screenOff atomic.Bool
	// muteUntil is a UnixNano before which SDL input is dropped.
	muteUntil atomic.Int64
	// wokeAt is when (monotonic, powerMonoNow) the last sleep ended, for the
	// power key's wake-press guard (powerkey.go); 0 before the first.
	wokeAt atomic.Int64
	// wake is signalled by a power press during the screen-off sleep.
	wake chan struct{}
	once sync.Once
}

// powerSleepNextAlarm is the UnixNano of the next JukaHub alarm (0: none),
// kept by powerKeyTick on the main thread, where the alarm list lives, so the
// sleep goroutine never reads that list itself.
var powerSleepNextAlarm atomic.Int64

// sleepNow puts the device to sleep. source names the caller for the log
// ("power-button", "game-menu"). A second call while a sleep is running is
// ignored. Safe from any goroutine; never blocks.
func sleepNow(source string) {
	if !powerSleepSupported() {
		showToast("Sleep is not available on this device", ToastInfo())
		return
	}
	if !powerSleep.active.CompareAndSwap(false, true) {
		return
	}
	powerSleep.once.Do(func() { powerSleep.wake = make(chan struct{}, 1) })
	// Drain a stale wake left by a press that arrived after the last sleep.
	select {
	case <-powerSleep.wake:
	default:
	}
	powerSleep.muteUntil.Store(math.MaxInt64)
	go powerSleepRun(source)
}

// powerSleepActive reports whether a sleep is running (being entered, asleep,
// or being undone).
func powerSleepActive() bool { return powerSleep.active.Load() }

// powerSleepWakeFromPress is the power key during the screen-off sleep. It
// reports whether the press was taken as the wake.
func powerSleepWakeFromPress() bool {
	if !powerSleep.screenOff.Load() {
		return false
	}
	select {
	case powerSleep.wake <- struct{}{}:
	default:
	}
	return true
}

// powerSleepJustWoke reports whether the device woke from a JukaHub sleep
// within d.
func powerSleepJustWoke(d time.Duration) bool {
	at := powerSleep.wokeAt.Load()
	return at != 0 && powerMonoNow()-time.Duration(at) < d
}

// powerSleepDropInput is the main loop's filter, called for every SDL event
// before anything else sees it: while asleep, and for a moment after the
// wake, key, button, stick, touch and mouse presses are dropped. Window,
// quit and drop events always pass. Main thread.
func powerSleepDropInput(event sdl.Event) bool {
	until := powerSleep.muteUntil.Load()
	if until == 0 || time.Now().UnixNano() >= until {
		return false
	}
	switch event.(type) {
	case *sdl.KeyboardEvent, *sdl.TextInputEvent, *sdl.ControllerButtonEvent, *sdl.ControllerAxisEvent,
		*sdl.JoyButtonEvent, *sdl.JoyAxisEvent, *sdl.JoyHatEvent, *sdl.MouseButtonEvent, *sdl.MouseWheelEvent,
		*sdl.TouchFingerEvent:
		return true
	}
	return false
}

// powerSleepHeld is what a sleep paused and must give back.
type powerSleepHeld struct {
	children []childHandle
	music    bool
}

// powerSleepRun is the sleep itself, on its own goroutine.
func powerSleepRun(source string) {
	var held powerSleepHeld
	lightOff := false
	clearRTC := func() {}
	defer func() {
		if r := recover(); r != nil {
			logWarn("sleep", "sleep failed: %v", r)
		}
		// Undo in reverse; each step is safe to run after a partial entry.
		clearRTC()
		if lightOff {
			backlightOn()
		}
		powerSleepRelease(held)
		powerSleep.screenOff.Store(false)
		powerSleep.wokeAt.Store(int64(powerMonoNow()))
		powerSleep.muteUntil.Store(time.Now().Add(powerSleepWakeMute).UnixNano())
		// The wake counts as input, so the idle screen does not start the
		// moment the device is back (devpower.go does the same).
		noteUserInputTime(time.Now())
		powerSleep.active.Store(false)
		// The pollers parked behind the game or the dark screen refresh now
		// (backgroundwork.go); the wake watcher handles the network.
		wakeForegroundWork()
	}()

	logInfo("sleep", "going to sleep (%s)", source)
	held = powerSleepHold()
	// devpower.go's switch: the display driver's backlight on the TrimUI,
	// with a marker that lets the next start undo a light a crash left off.
	backlightOff(nil)
	lightOff = true
	if err := syncDisks(); err != nil {
		logWarn("sleep", "sync before sleep failed: %v", err)
	}
	clearRTC = powerSleepArmAlarm()

	// powerSleepAsleepClock counts only time spent suspended, so the
	// difference is how long the device really slept.
	before, haveClocks := powerSleepAsleepClock()
	err := suspendDevice()
	slept, measured := time.Duration(0), false
	if after, ok := powerSleepAsleepClock(); ok && haveClocks {
		slept, measured = after-before, true
	}
	switch {
	case err != nil:
		logWarn("sleep", "the kernel did not suspend: %v", err)
	case measured && slept < powerSleepSpuriousWake:
		logWarn("sleep", "the device came straight back from suspend")
	default:
		logInfo("sleep", "awake after %s", slept.Round(time.Second))
		powerKeyNoteOwnWake()
		return
	}
	// The kernel could not sleep: a screen-off sleep, if a power press can
	// end it.
	if !powerKeyWatcherLive() {
		showToast("This device could not sleep", ToastWarn())
		return
	}
	powerSleepScreenOffWait()
}

// powerSleepScreenOffWait keeps the screen dark and the game stopped until the
// next power press, an alarm about to ring, or a quit request.
func powerSleepScreenOffWait() {
	logInfo("sleep", "screen-off sleep until the next power press")
	powerSleep.screenOff.Store(true)
	defer powerSleep.screenOff.Store(false)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-powerSleep.wake:
			logInfo("sleep", "woken by the power button")
			return
		case <-ticker.C:
			if exitRequested.Load() {
				return
			}
			if at := powerSleepNextAlarm.Load(); at > 0 && time.Until(time.Unix(0, at)) <= powerSleepAlarmLead {
				logInfo("sleep", "woken for an alarm")
				return
			}
		}
	}
}

// childHandle is one screen-taking child this sleep stopped.
type childHandle struct {
	stop func(stop bool) error
	pid  int
}

// powerSleepHold stops the running children and pauses music. A child that is
// owned by another pause (the in-game menu, the game assistant, a ringing
// alarm) or already stopped is left alone, so the wake gives back only what
// the sleep took.
func powerSleepHold() powerSleepHeld {
	var held powerSleepHeld
	if alarmCanPauseChildren() && !gameAssistOverlayActive() && !alarmFiringActive() {
		for _, cmd := range liveScreenChildren() {
			if cmd == nil || cmd.Process == nil {
				continue
			}
			pid := cmd.Process.Pid
			if powerChildGroupStopped(pid) {
				continue
			}
			c := cmd
			if err := alarmSignalChild(c, true); err != nil {
				logWarn("sleep", "could not pause the running game: %v", err)
				continue
			}
			held.children = append(held.children, childHandle{
				stop: func(stop bool) error { return alarmSignalChild(c, stop) },
				pid:  pid,
			})
		}
	}
	music.mu.Lock()
	if music.playing && !music.paused {
		music.paused = true
		held.music = true
	}
	music.mu.Unlock()
	if held.music {
		ClearVideoAudioQueue()
	}
	return held
}

// powerSleepRelease gives back what powerSleepHold took. Children first: a
// game must never be left stopped.
func powerSleepRelease(held powerSleepHeld) {
	for _, ch := range held.children {
		if err := ch.stop(false); err != nil {
			logWarn("sleep", "could not resume the game (pid %d): %v", ch.pid, err)
		}
	}
	if held.music {
		music.mu.Lock()
		if music.playing && music.paused {
			music.paused = false
		}
		music.mu.Unlock()
	}
}

// powerSleepArmAlarm sets the RTC to wake the device shortly before the next
// JukaHub alarm and returns what clears it again. Nothing is armed without an
// alarm in the next day.
func powerSleepArmAlarm() func() {
	at := powerSleepNextAlarm.Load()
	if at <= 0 {
		return func() {}
	}
	in := time.Until(time.Unix(0, at)) - powerSleepAlarmLead
	if in > powerSleepAlarmHorizon {
		return func() {}
	}
	if in < 5*time.Second {
		in = 5 * time.Second
	}
	return powerRTCWakeIn(in)
}

// powerSleepNextAlarmAt is when the next enabled alarm rings (a snooze
// included), or the zero time. Main thread: it reads the alarm list.
func powerSleepNextAlarmAt(now time.Time) time.Time {
	var best time.Time
	consider := func(t time.Time) {
		if t.After(now) && (best.IsZero() || t.Before(best)) {
			best = t
		}
	}
	for i := range alarmStore.Alarms {
		a := alarmStore.Alarms[i]
		if !a.Enabled {
			continue
		}
		if a.SnoozeUntil != "" {
			if until, err := time.Parse(time.RFC3339, a.SnoozeUntil); err == nil {
				consider(until)
			}
		}
		at := time.Date(now.Year(), now.Month(), now.Day(), a.Hour, a.Minute, 0, 0, now.Location())
		if a.firedToday(now) || !at.After(now) {
			at = at.AddDate(0, 0, 1)
		}
		consider(at)
	}
	return best
}
