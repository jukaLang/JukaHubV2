package main

// nextui_sleep.go — Power off after sleeping for the sleep JukaHub does not
// start: minarch's own, in a NextUI pak game.
//
// While a pak game holds the screen the power key is minarch's (nextui.go). A
// press puts the game to sleep: minarch saves it first — NextUI's docs: before
// sleep or power-off its emulators write SRAM, RTC data, an autosave state and
// an auto-resume marker (MinUI's Menu_beforeSleep: slot 9, then
// .userdata/shared/.minui/auto_resume.txt; Menu_afterSleep removes the marker
// on the wake) — and NextUI's suspend script suspends the device. NextUI sets
// no deadline on that sleep, so a handheld forgotten in a bag drains to empty:
// the reason for lmerriam's Auto Power Off pak (2026-11). JukaHub is still the
// pak's parent, so Settings → Power off after sleeping covers that sleep too,
// without a hook in NextUI's files:
//
//   - Every pakSleepTick the guard looks at the marker. A new one that names
//     the running game, written together with minarch's slot-9 state since
//     the last wake, is minarch going to sleep with the game saved: the RTC's
//     wake alarm is set for the delay then (a JukaHub alarm that comes first
//     instead). Nothing is armed while the game is played, so a core that
//     cannot save state, or any other suspend, never gets a wake.
//   - Every tick also reads the clocks (powerSleepReadClocks): time that
//     passed on the RTC or BOOTTIME but not on the monotonic clock is time the
//     device was suspended, not JukaHub merely stalled.
//   - A wake whose sleep ended within the delay's slack of the deadline, with
//     the alarm held by the RTC before the sleep and gone after it, no press
//     of the power key and no JukaHub alarm due, is the deadline's. The device
//     then powers off through Power off's own path (devPowerRequest,
//     silently), once minarch's state is checked again; the teardown stops
//     minarch, and the marker its wake removed is put back word for word
//     (nextuiSleepGuardClose), so the next power-on resumes the game: NextUI's
//     own auto-resume, or quickResumeNextUIPlan when JukaHub boots first.
//   - Any other wake ends that sleep: the alarm is cleared. A marker that goes
//     away without a sleep (woken from the dark screen before the suspend)
//     clears it too.
//
// Unlike JukaHub's own sleep this one powers off on a charger too: minarch's
// sleep cannot be resumed quietly, and a game left running on screen all
// night on a charger is worse than a handheld that is off.
//
// A marker the guard does not see before the suspend (written less than a
// tick before it) arms nothing: that sleep simply has no deadline, as before.
// The alarm is cleared when the last pak game ends (nextuiPakExited) and
// before any power off, restart or exit (devPowerAfterShutdown), so a stale
// one never wakes NextUI or the firmware later. Auto Power Off installed as
// well is harmless: the RTC holds one alarm, an earlier one already set by
// someone else is left alone, and whichever deadline comes first powers off.
//
// Nothing here was run on a device from this desk.

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// pakSleepTick is how often the guard looks at the marker and the
	// clocks: minarch writes the marker a moment before the suspend.
	pakSleepTick = 250 * time.Millisecond
	// pakSleepMinGap is the least time suspended between two ticks that counts
	// as a sleep.
	pakSleepMinGap = 10 * time.Second
	// pakSleepStateSkew is how much older than the marker minarch's slot-9
	// state may be: it writes the state, then the marker.
	pakSleepStateSkew = time.Minute
	// pakSleepFATSlack covers FAT's two-second modification times.
	pakSleepFATSlack = 2 * time.Second
	// pakSleepMarkerMax bounds a read of the marker: one path.
	pakSleepMarkerMax = 4096
)

// pakSleep is the guard's shared state.
var pakSleep struct {
	// running is set while the guard's goroutine runs.
	running atomic.Bool
	// mu orders the wake alarm's writes: the guard's, and the disarm from a
	// game's end or the teardown.
	mu sync.Mutex
	// armed: the RTC holds the guard's alarm.
	armed bool
	// closed: JukaHub is shutting down; nothing is armed any more.
	closed bool
	// restore is the marker the deadline's power off puts back if it is gone.
	restore []byte
}

// pakSleepMarker is NextUI's auto-resume marker as read once.
type pakSleepMarker struct {
	text    []byte    // the marker, word for word
	rom     string    // the game it names ("" for none on the card)
	modTime time.Time // its modification time
}

// pakSleepState is the guard's view from one tick to the next. Its goroutine
// only.
type pakSleepState struct {
	// markerPath is NextUI's auto-resume marker (nextuiAutoResumePath),
	// looked up once.
	markerPath string
	prev       powerSleepClocks
	havePrev   bool
	// lastWake is the wall clock when the guard started or last saw a wake: a
	// marker older than it is an earlier sleep's.
	lastWake time.Time
	// seen is the marker as last read, haveSeen whether there was one.
	seen     pakSleepMarker
	haveSeen bool
	// armedFor is the delay the wake alarm was set for (0: none), armMono
	// when that was (monotonic), asleep the time suspended since, held
	// whether the RTC still held the alarm at the last tick. mark is the
	// marker it was set for, state when minarch's slot-9 state was written.
	armedFor time.Duration
	armMono  time.Duration
	asleep   time.Duration
	held     bool
	mark     pakSleepMarker
	state    time.Time
	warned   bool // a failed arm was logged
}

// nextuiSleepGuardStart starts the guard when a pak game takes the screen
// (nextuiPakStarted). One runs at a time, until the last pak game ends.
func nextuiSleepGuardStart() {
	if !sleepPowerOffQuickAvailable() {
		return
	}
	if pakSleep.running.CompareAndSwap(false, true) {
		go nextuiSleepGuardRun()
	}
}

// nextuiSleepGuardRun is the guard's goroutine.
func nextuiSleepGuardRun() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("sleep", "the NextUI sleep guard stopped: %v", r)
			nextuiSleepGuardDisarm()
			pakSleep.running.Store(false)
		}
	}()
	for {
		if nextuiSleepGuardLoop() {
			// The power off is under way: nothing more to watch.
			pakSleep.running.Store(false)
			return
		}
		pakSleep.running.Store(false)
		// A game that started as the loop ended found the guard still running.
		if exitRequested.Load() || len(nextuiPakGames()) == 0 || !pakSleep.running.CompareAndSwap(false, true) {
			return
		}
	}
}

// nextuiSleepGuardLoop runs the guard until no pak game is left or JukaHub
// quits (false), or the deadline's power off is under way (true).
func nextuiSleepGuardLoop() bool {
	st := pakSleepState{markerPath: nextuiAutoResumePath(), lastWake: time.Now()}
	ticker := time.NewTicker(pakSleepTick)
	defer ticker.Stop()
	for {
		if exitRequested.Load() || len(nextuiPakGames()) == 0 {
			nextuiSleepGuardDisarm()
			return false
		}
		if st.armedFor == 0 && quickSleepDeadline() <= 0 {
			// Off: nothing to watch until it is turned on again.
			st.havePrev, st.haveSeen = false, false
			<-ticker.C
			continue
		}
		cur := powerSleepReadClocks()
		if st.havePrev {
			gap, _ := powerSleepSlept(st.prev, cur)
			if st.armedFor > 0 {
				st.asleep += gap
			}
			if gap >= pakSleepMinGap {
				st.lastWake = time.Now()
				if st.armedFor > 0 {
					logInfo("sleep", "NextUI game: the device slept %s", gap.Round(time.Second))
					if nextuiSleepDeadlineReached(&st, cur) && nextuiSleepAtDeadline(&st) {
						return true
					}
					// Any other wake ends this sleep, and its alarm.
					nextuiSleepGuardDisarmFor(&st)
				}
			}
		}
		nextuiSleepWatch(&st, cur)
		st.prev, st.havePrev = cur, true
		<-ticker.C
	}
}

// nextuiSleepWatch follows NextUI's auto-resume marker: a new one is minarch
// going to sleep (nextuiSleepArmFor); one that goes away while armed, with no
// sleep seen, is minarch awake again before the suspend. While armed it notes
// whether the RTC still holds the alarm.
func nextuiSleepWatch(st *pakSleepState, cur powerSleepClocks) {
	path := st.markerPath
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		st.haveSeen = false
		if st.armedFor > 0 {
			logInfo("sleep", "NextUI game: awake again before the suspend")
			nextuiSleepGuardDisarmFor(st)
		}
		return
	}
	if !st.haveSeen || !info.ModTime().Equal(st.seen.modTime) {
		text, rerr := nextuiReadSmall(path, pakSleepMarkerMax)
		if rerr != nil {
			return // read again next tick
		}
		rom, _ := nextuiAutoResumeROM(text)
		st.seen, st.haveSeen = pakSleepMarker{text: text, rom: rom, modTime: info.ModTime()}, true
		nextuiSleepArmFor(st, cur)
	}
	if st.armedFor > 0 {
		pending, known := powerRTCAlarmPending()
		st.held = known && pending
	}
}

// nextuiSleepArmFor arms the wake alarm for the marker just read when it is
// minarch saving the running game for a sleep: it names a game the running pak
// was started with, it was written since the last wake, and minarch's slot-9
// state for that game was written with it. With the setting off, or while
// JukaHub's own sleep holds the RTC, nothing is armed.
func nextuiSleepArmFor(st *pakSleepState, cur powerSleepClocks) {
	m := st.seen
	since := st.lastWake.Add(-pakSleepFATSlack)
	if m.rom == "" || m.modTime.Before(since) || !quickArgsMention(nextuiPakGames(), filepath.Base(m.rom)) {
		return
	}
	deadline := quickSleepDeadline()
	if deadline <= 0 {
		return
	}
	state, ok := nextuiResumeStateTime(m.rom)
	if !ok || state.Before(since) || state.Before(m.modTime.Add(-pakSleepStateSkew)) {
		logInfo("sleep", "NextUI game: %s sleeps without a fresh resume state, so it gets no power-off time", filepath.Base(m.rom))
		return
	}
	in, _ := powerSleepWakeIn(deadline)
	secs := int64(in / time.Second)
	pakSleep.mu.Lock()
	defer pakSleep.mu.Unlock()
	if pakSleep.closed || powerSleepActive() || exitRequested.Load() || len(nextuiPakGames()) == 0 {
		return
	}
	if !pakSleep.armed {
		// JukaHub holds no alarm while it is awake, so one set now is someone
		// else's (Auto Power Off's, the firmware's): an earlier one stays.
		if at, ok := powerRTCAlarmAt(); ok && at > 0 {
			if now, ok := powerRTCNow(); ok && at > now && at <= now+secs {
				logInfo("sleep", "NextUI game: an earlier RTC wake alarm is already set, so it is left as it is")
				return
			}
		}
	}
	if !powerRTCWakeSet(secs) {
		pakSleep.armed = false
		if !st.warned {
			st.warned = true
			logWarn("sleep", "NextUI game: could not set the RTC wake alarm, so a long sleep will not power off")
		}
		return
	}
	pakSleep.armed = true
	st.armedFor, st.armMono, st.asleep, st.held = deadline, cur.mono, 0, true
	st.mark, st.state = m, state
	logInfo("sleep", "NextUI game asleep, %s saved by minarch: the device powers off after %s asleep (Power off after sleeping)",
		filepath.Base(m.rom), sleepPowerOffLabel(deadline))
}

// nextuiSleepGuardDisarmFor ends the sleep the guard armed for: its alarm is
// cleared. The guard's goroutine.
func nextuiSleepGuardDisarmFor(st *pakSleepState) {
	st.armedFor, st.asleep, st.held = 0, 0, false
	nextuiSleepGuardDisarm()
}

// nextuiSleepGuardDisarm clears the guard's wake alarm if the RTC holds it:
// the sleep ended, the last pak game ended, or JukaHub is about to quit,
// power off or restart. Any goroutine.
func nextuiSleepGuardDisarm() {
	pakSleep.mu.Lock()
	defer pakSleep.mu.Unlock()
	if !pakSleep.armed {
		return
	}
	if powerSleepActive() {
		// JukaHub's own sleep owns the RTC now; the next disarm (the wake,
		// the game's end, the teardown) clears whatever is left.
		return
	}
	pakSleep.armed = false
	powerRTCWakeClear()
}

// nextuiSleepGuardClose is the teardown's last word (devPowerAfterShutdown):
// the guard's alarm is cleared and nothing is armed again, and the marker the
// deadline's power off needs is put back if minarch's wake removed it. The
// teardown has stopped minarch by now, so it stays for the next power-on.
func nextuiSleepGuardClose() {
	pakSleep.mu.Lock()
	pakSleep.closed = true
	text := pakSleep.restore
	pakSleep.restore = nil
	pakSleep.mu.Unlock()
	nextuiSleepGuardDisarm()
	if len(text) == 0 {
		return
	}
	path := nextuiAutoResumePath()
	if regularFile(path) {
		return
	}
	if err := AtomicWrite(path, text, 0o644); err != nil {
		logWarn("sleep", "could not put NextUI's auto-resume marker back: %v", err)
		return
	}
	logInfo("sleep", "NextUI's auto-resume marker put back for the next power-on")
}

// nextuiSleepDeadlineReached reports whether the sleep that just ended was
// the deadline's: it ended within the slack of the delay since the alarm was
// set (an RTC wake comes on time; a much later one was a press the RTC slept
// through), the RTC held the alarm before the sleep and no longer does, no
// press of the power key came, and no JukaHub alarm is due, whose ringing
// must not be powered off. A sleep of JukaHub's own has its own deadline
// (powersleep.go). The guard's goroutine; it waits quickDeadlinePressWait for
// the key's reader.
func nextuiSleepDeadlineReached(st *pakSleepState, cur powerSleepClocks) bool {
	if powerSleepActive() || powerSleepJustWoke(pakSleepMinGap) {
		return false
	}
	since := cur.mono - st.armMono + st.asleep
	if since < st.armedFor-quickDeadlineSlack || since > st.armedFor+quickDeadlineLate {
		return false
	}
	if !st.held {
		logInfo("sleep", "NextUI game: asleep long enough, but the RTC did not hold the alarm before the sleep")
		return false
	}
	if at := powerSleepNextAlarm.Load(); at > 0 && time.Until(time.Unix(0, at)) < quickDeadlineAlarmGuard {
		logInfo("sleep", "NextUI game: power-off time reached, but an alarm is due: staying on for it")
		return false
	}
	// The key's reader, and the RTC's own bookkeeping of the fired alarm,
	// get a moment after the wake.
	time.Sleep(quickDeadlinePressWait)
	if at := time.Duration(powerKeyLastDown.Load()); at != 0 && at >= st.prev.mono {
		logInfo("sleep", "NextUI game: power-off time reached, but the power key woke it")
		return false
	}
	if pending, known := powerRTCAlarmPending(); !known || pending {
		logInfo("sleep", "NextUI game: asleep long enough, but the RTC still holds the alarm, so something else woke it")
		return false
	}
	if quickSleepDeadline() <= 0 {
		return false // turned off meanwhile
	}
	return true
}

// nextuiSleepAtDeadline is the deadline wake's work: with the game still the
// one minarch saved and its slot-9 state still there, the device powers off
// and the marker is kept for the teardown to put back; otherwise nothing
// happens. It reports whether the power off is under way.
func nextuiSleepAtDeadline(st *pakSleepState) bool {
	m := st.mark
	at, ok := nextuiResumeStateTime(m.rom)
	why := ""
	switch {
	case !quickArgsMention(nextuiPakGames(), filepath.Base(m.rom)):
		why = "the game it saved is no longer the one running"
	case !ok:
		why = "minarch's resume state for it is gone"
	case at.Before(st.state):
		why = "minarch's resume state for it is older than the sleep's"
	case !devPowerSupported():
		why = "JukaHub cannot power this device off"
	}
	if why != "" {
		logWarn("sleep", "NextUI game asleep past Power off after sleeping, but %s: staying on so nothing is lost", why)
		return false
	}
	pakSleep.mu.Lock()
	pakSleep.restore = append([]byte(nil), m.text...)
	pakSleep.mu.Unlock()
	logInfo("sleep", "NextUI game asleep long enough (Power off after sleeping); minarch saved %s before the sleep: powering off", filepath.Base(m.rom))
	quickPowerOff.silent.Store(true)
	devPowerRequest(devPowerPoweroff)
	return true
}
