package main

// stopwatch.go — the Stopwatch app's two clocks: a stopwatch with laps and a
// countdown timer. stopwatch_ui.go draws them; this file keeps them running.
//
// Both live in package state, not in the scene, so they keep going while the
// user is anywhere else in JukaHub or in a game. They are measured with Go's
// monotonic clock: a time.Time taken with time.Now keeps a monotonic reading,
// and time.Since on it ignores the wall clock. The handheld starts before
// network time and then jumps its clock by years; a stopwatch built on wall
// time would jump with it. (The monotonic clock does not advance while the
// handheld sleeps, so time asleep is not counted — the same as a paused game.)
//
// Every change (start, pause, lap, reset, a new countdown length, the tab) is
// saved to stopwatch.json in JukaHub's data folder, and once more when the app
// exits. A clock still running at that moment is saved as paused with the time
// it had: its monotonic start cannot survive a restart, and resuming from a
// wall-clock difference would bring the clock jumps back. A restart therefore
// shows it paused where it was, one press from carrying on.
//
// The countdown's end and its messages over a game are driven from
// stopwatchTick, which alarmCheck calls once a second on every path of the main
// loop — under a running game too — so the timer ends on time whatever is on
// screen. The end chimes once (the alarm's "chime" motif twice, not the ring),
// raises a "Time's up" toast and files a notification; during a RetroArch game
// the library started, RetroArch's own message line says so too (JukaHub cannot
// draw over a running emulator), and it also says when 5 minutes, 1 minute and
// 10 seconds are left. Quiet Mode holds the toast, the chime and the game
// messages, as it does for calendar reminders (calsync_remind.go). The
// stopwatch sends nothing periodic, which would only cover the game; it
// answers presses made from the in-game menu (Select + L2) instead.

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	swTabStopwatch = 0
	swTabCountdown = 1
	// swMaxLaps bounds the lap list (and the saved file).
	swMaxLaps = 200
	// swMaxCountdown is the longest countdown the fields can set.
	swMaxCountdown = 99*time.Hour + 59*time.Minute + 59*time.Second
	// swMaxElapsed bounds a saved stopwatch reading (a damaged file).
	swMaxElapsed = 10000 * time.Hour
	// swDefaultCountdown is the length a fresh countdown starts at.
	swDefaultCountdown = 5 * time.Minute
	// swChimeGain is the end chime's level: clearly heard, below a ring.
	swChimeGain = 0.6
)

// swPresets are the countdown's one-press lengths (Y while it is set, or the
// chips under the ring).
var swPresets = [...]time.Duration{
	1 * time.Minute, 3 * time.Minute, 5 * time.Minute, 10 * time.Minute,
	15 * time.Minute, 25 * time.Minute, 60 * time.Minute,
}

// swMilestones are the times left at which a running countdown tells a game.
var swMilestones = [...]time.Duration{5 * time.Minute, time.Minute, 10 * time.Second}

// sw is both clocks. Main thread only.
var sw struct {
	loaded bool
	tab    int

	// The stopwatch: banked is what it had counted before the current run,
	// since when that run began (with its monotonic reading).
	running bool
	since   time.Time
	banked  time.Duration
	// laps is the total at each lap press, oldest first.
	laps      []time.Duration
	lapScroll int

	// The countdown: cdLeft is what was left when the current run began, or
	// when it was paused.
	cdSet     time.Duration
	cdRunning bool
	cdSince   time.Time
	cdLeft    time.Duration
	// cdStarted: started since the last reset and not yet run out (running,
	// or paused part-way).
	cdStarted bool
	// cdDone: it ran out; "Time's up" shows until it is reset or restarted.
	cdDone bool
	// cdPassed has bit i set once swMilestones[i] was passed in this run.
	cdPassed uint8
	// field is the part of the length Up/Down change: 0 hours, 1 minutes,
	// 2 seconds.
	field int
}

// ── Reading the clocks ────────────────────────────────────────────────────

// swElapsed is the stopwatch's reading now.
func swElapsed() time.Duration {
	if !sw.running {
		return sw.banked
	}
	return sw.banked + time.Since(sw.since)
}

// swCountdownLeft is what the countdown has left now.
func swCountdownLeft() time.Duration {
	if !sw.cdRunning {
		return sw.cdLeft
	}
	left := sw.cdLeft - time.Since(sw.cdSince)
	if left < 0 {
		return 0
	}
	return left
}

// swClock is a stopwatch reading: "2:34.5", or "1:02:03.4" past an hour.
func swClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	tenths := int64(d / (100 * time.Millisecond))
	h, m, s, t := tenths/36000, tenths/600%60, tenths/10%60, tenths%10
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d.%d", h, m, s, t)
	}
	return fmt.Sprintf("%d:%02d.%d", m, s, t)
}

// swClockShort is a reading without the tenths: "2:34", "1:02:03".
func swClockShort(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	secs := int64(d / time.Second)
	h, m, s := secs/3600, secs/60%60, secs%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// swLeft is a countdown's time left, rounded up to the second the way a
// kitchen timer reads (it shows 0:01 until it is really over).
func swLeft(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return swClockShort(d + time.Second - time.Nanosecond)
}

// swLapTime is lap i's own time (the totals are kept).
func swLapTime(i int) time.Duration {
	if i <= 0 {
		return sw.laps[0]
	}
	return sw.laps[i] - sw.laps[i-1]
}

// swLapExtremes are the fastest and slowest laps' indexes, or -1 with fewer
// than two laps.
func swLapExtremes() (fast, slow int) {
	if len(sw.laps) < 2 {
		return -1, -1
	}
	fast, slow = 0, 0
	for i := 1; i < len(sw.laps); i++ {
		d := swLapTime(i)
		if d < swLapTime(fast) {
			fast = i
		}
		if d > swLapTime(slow) {
			slow = i
		}
	}
	return fast, slow
}

// ── Changing them ─────────────────────────────────────────────────────────

// swToggle starts or pauses the stopwatch.
func swToggle() {
	if sw.running {
		sw.banked = swElapsed()
		sw.running = false
	} else {
		sw.since = time.Now()
		sw.running = true
	}
	PlayToggleSound()
	swSave()
}

// swLap records a lap while the stopwatch runs.
func swLap() {
	if !sw.running {
		return
	}
	if len(sw.laps) >= swMaxLaps {
		ShowToast(fmt.Sprintf("The stopwatch keeps %d laps — reset it for a new list", swMaxLaps), ToastKindInfo)
		return
	}
	sw.laps = append(sw.laps, swElapsed())
	sw.lapScroll = 0
	PlayNavSound()
	swSave()
}

// swReset clears a paused stopwatch and its laps.
func swReset() {
	if sw.running || (sw.banked == 0 && len(sw.laps) == 0) {
		return
	}
	sw.banked, sw.laps, sw.lapScroll = 0, nil, 0
	PlayBackSound()
	swSave()
}

// swCdEditable reports a countdown that is being set (not started, or run
// out), whose length the fields and presets change.
func swCdEditable() bool {
	return !sw.cdStarted
}

// swCdToggle starts, pauses or resumes the countdown.
func swCdToggle() {
	switch {
	case sw.cdRunning:
		sw.cdLeft = swCountdownLeft()
		sw.cdRunning = false
	case sw.cdStarted:
		sw.cdSince = time.Now()
		sw.cdRunning = true
	default:
		if sw.cdSet <= 0 {
			ShowToast("Set a time first", ToastKindInfo)
			return
		}
		sw.cdLeft, sw.cdSince = sw.cdSet, time.Now()
		sw.cdRunning, sw.cdStarted, sw.cdDone = true, true, false
		sw.cdPassed = swMilestonesPassed(sw.cdSet)
	}
	PlayToggleSound()
	swSave()
}

// swCdReset puts the countdown back to its length, stopped.
func swCdReset() {
	if !sw.cdStarted && !sw.cdDone {
		return
	}
	sw.cdRunning, sw.cdStarted, sw.cdDone = false, false, false
	sw.cdLeft = 0
	PlayBackSound()
	swSave()
}

// swCdParts splits the length into the fields.
func swCdParts() [3]int {
	secs := int(sw.cdSet / time.Second)
	return [3]int{secs / 3600, secs / 60 % 60, secs % 60}
}

// swCdAdjust steps the focused field by delta, wrapping within it (59 → 0
// leaves the hours alone, as on a microwave).
func swCdAdjust(delta int) {
	if !swCdEditable() || delta == 0 {
		return
	}
	p := swCdParts()
	limit := [3]int{100, 60, 60}[sw.field]
	p[sw.field] = ((p[sw.field]+delta)%limit + limit) % limit
	sw.cdSet = time.Duration(p[0])*time.Hour + time.Duration(p[1])*time.Minute + time.Duration(p[2])*time.Second
	sw.cdDone = false
	PlayNavSound()
	swSave()
}

// swCdField moves the focus between hours, minutes and seconds.
func swCdField(dir int) {
	if !swCdEditable() {
		return
	}
	next := sw.field + dir
	if next < 0 || next > 2 {
		return
	}
	sw.field = next
	// Choosing a field puts the fields back in the ring after "Time's up".
	sw.cdDone = false
	PlayNavSound()
	swSave()
}

// swCdPreset sets the next preset longer than the current length (after the
// longest, the shortest again).
func swCdPreset() {
	if !swCdEditable() {
		return
	}
	next := swPresets[0]
	for _, p := range swPresets {
		if p > sw.cdSet {
			next = p
			break
		}
	}
	swCdSetLength(next)
}

// swCdSetLength sets the countdown's length (a preset chip, or Y).
func swCdSetLength(d time.Duration) {
	if !swCdEditable() {
		return
	}
	sw.cdSet = min(max(d, 0), swMaxCountdown)
	sw.cdDone = false
	PlayNavSound()
	swSave()
}

// swSetTab switches the scene's tab and remembers it.
func swSetTab(tab int) {
	if tab != swTabCountdown {
		tab = swTabStopwatch
	}
	if tab == sw.tab {
		return
	}
	sw.tab = tab
	PlayNavSound()
	swSave()
}

// ── The countdown's end and its messages ──────────────────────────────────

// swMilestonesPassed are the milestones at or above left: a countdown started
// (or set) below one never announces it.
func swMilestonesPassed(left time.Duration) uint8 {
	var bits uint8
	for i, m := range swMilestones {
		if left <= m {
			bits |= 1 << i
		}
	}
	return bits
}

// swCheck ends a countdown that ran out and sends the milestones it passed.
// Main thread: stopwatchTick calls it once a second, the scene every frame.
func swCheck() {
	if !sw.cdRunning {
		return
	}
	left := swCountdownLeft()
	if left <= 0 {
		swFinish()
		return
	}
	for i, m := range swMilestones {
		bit := uint8(1) << i
		if sw.cdPassed&bit != 0 || left > m {
			continue
		}
		sw.cdPassed |= bit
		// Only a crossing seen as it happened: a tick held up for a while
		// (a sleep) must not announce "5 minutes left" with one to go.
		if m-left <= 3*time.Second {
			swGameMessage("Timer: " + swMilestoneWords(m) + " left")
		}
	}
}

// swMilestoneWords names a milestone: "5 minutes", "1 minute", "10 seconds".
func swMilestoneWords(m time.Duration) string {
	switch {
	case m >= 2*time.Minute:
		return fmt.Sprintf("%d minutes", int(m/time.Minute))
	case m >= time.Minute:
		return "1 minute"
	}
	return fmt.Sprintf("%d seconds", int(m/time.Second))
}

// swFinish is the countdown's end, once: the chime, the toast, the
// notification and RetroArch's message line during a game.
func swFinish() {
	sw.cdRunning, sw.cdStarted, sw.cdDone = false, false, true
	sw.cdLeft = 0
	swSave()
	length := swClockShort(sw.cdSet)
	// Filed unread: a timer that ran out behind a game was not seen.
	notifRecord("Timer: time's up ("+length+")", notifInfo)
	showToastPrimitive("Time's up", ToastKindInfo)
	if !quietModeOn() {
		swChime()
	}
	swGameMessage("Time's up — " + length + " timer")
}

// swGameMessage puts line on RetroArch's own message line during a RetroArch
// game the library started (held, like the toast, in Quiet Mode). Main thread:
// raLibraryPort reads the game tracker.
func swGameMessage(line string) {
	if quietModeOn() {
		return
	}
	if port := raLibraryPort(); port > 0 {
		line = strings.ReplaceAll(line, "\n", " ")
		go func() { _ = sendRetroArchNetCmd(port, "SHOW_MSG "+line) }()
	}
}

// swChime plays the alarm's default motif twice, once, on the UI's audio
// device: a short sound that says the time is up, not a ring that has to be
// answered. A ringing alarm owns the speaker, so it plays nothing then.
func swChime() {
	if alarmFiringActive() {
		return
	}
	go func() {
		audioInitOnce.Do(initAudioDevice)
		if !audioAvailable {
			return
		}
		pcm := alarmSoundPCM(audioSampleRate, alarmSoundByID(alarmDefaultSound))
		if len(pcm) == 0 {
			return
		}
		buf := make([]byte, 0, len(pcm)*4)
		for rep := 0; rep < 2; rep++ {
			for _, s := range pcm {
				v := int16(float64(s) * swChimeGain)
				buf = append(buf, byte(v), byte(uint16(v)>>8))
			}
		}
		audioMutex.Lock()
		err := outQueue(audioDev, buf)
		audioMutex.Unlock()
		if err != nil {
			logWarn("stopwatch", "the end chime could not play: %v", err)
		}
	}()
}

// stopwatchTick is called from alarmCheck once a second, on every path of the
// main loop (a game running included). It loads the saved clocks the first
// time, so the in-game menu and Quick Settings know them without the scene
// having been opened, and ends the countdown on time. Main thread.
func stopwatchTick(now time.Time) {
	_ = now // the clocks are monotonic; the wall time is not used
	swEnsureLoaded()
	swCheck()
}

// ── Saving ────────────────────────────────────────────────────────────────

// swSaved is stopwatch.json. Durations are milliseconds; nothing in it is a
// wall-clock time.
type swSaved struct {
	Tab       int     `json:"tab"`
	ElapsedMS int64   `json:"elapsedMs"`
	LapsMS    []int64 `json:"lapsMs,omitempty"`
	SetMS     int64   `json:"countdownSetMs"`
	LeftMS    int64   `json:"countdownLeftMs,omitempty"`
	Started   bool    `json:"countdownStarted,omitempty"`
	Done      bool    `json:"countdownDone,omitempty"`
	Field     int     `json:"field"`
}

// swSaveMu serialises the writes (AtomicWrite uses one temporary name per
// file); swSaveGen lets a queued write that a newer one follows skip itself.
var (
	swSaveMu  sync.Mutex
	swSaveGen atomic.Int64
)

// swPath is stopwatch.json.
func swPath() string { return osAppsDataPath("stopwatch.json") }

// swEnsureLoaded reads stopwatch.json once. Everything read is clamped: the
// file is the user's to edit (or damage).
func swEnsureLoaded() {
	if sw.loaded {
		return
	}
	sw.loaded = true
	sw.cdSet, sw.field = swDefaultCountdown, 1
	var s swSaved
	if _, err := readJSONWithBackup(swPath(), &s); err != nil {
		return
	}
	ms := func(v int64, limit time.Duration) time.Duration {
		// Clamped before the multiplication, which a huge value would wrap.
		return time.Duration(min(max(v, 0), limit.Milliseconds())) * time.Millisecond
	}
	if s.Tab == swTabCountdown {
		sw.tab = swTabCountdown
	}
	sw.banked = ms(s.ElapsedMS, swMaxElapsed)
	for _, l := range s.LapsMS {
		if len(sw.laps) >= swMaxLaps {
			break
		}
		d := ms(l, sw.banked)
		if n := len(sw.laps); n > 0 && d < sw.laps[n-1] {
			continue
		}
		sw.laps = append(sw.laps, d)
	}
	sw.cdSet = ms(s.SetMS, swMaxCountdown)
	if left := ms(s.LeftMS, sw.cdSet); s.Started && left > 0 {
		sw.cdStarted, sw.cdLeft = true, left
		sw.cdPassed = swMilestonesPassed(left)
	}
	sw.cdDone = s.Done && !sw.cdStarted
	if s.Field >= 0 && s.Field <= 2 {
		sw.field = s.Field
	}
}

// swSnapshot is the state as it is saved. A running clock is written with the
// time it has now and no "running": see the file's comment.
func swSnapshot() swSaved {
	s := swSaved{
		Tab:       sw.tab,
		ElapsedMS: swElapsed().Milliseconds(),
		SetMS:     sw.cdSet.Milliseconds(),
		Started:   sw.cdStarted,
		Done:      sw.cdDone,
		Field:     sw.field,
	}
	if sw.cdStarted {
		s.LeftMS = swCountdownLeft().Milliseconds()
	}
	for _, l := range sw.laps {
		s.LapsMS = append(s.LapsMS, l.Milliseconds())
	}
	return s
}

// swSave writes the state off the main thread.
func swSave() {
	if !sw.loaded {
		return
	}
	data, err := json.Marshal(swSnapshot())
	if err != nil {
		return
	}
	gen := swSaveGen.Add(1)
	go func() {
		swSaveMu.Lock()
		defer swSaveMu.Unlock()
		if swSaveGen.Load() != gen {
			return // a newer state follows
		}
		swWrite(data)
	}()
}

// swWrite writes one state. Callers hold swSaveMu.
func swWrite(data []byte) {
	if err := AtomicWrite(swPath(), data, 0o644); err != nil {
		logWarn("stopwatch", "could not save the stopwatch: %v", err)
	}
}

// stopwatchFlush saves the clocks as the app exits, on the calling goroutine.
// A clock still running is saved paused, with the time it has now.
func stopwatchFlush() {
	if !sw.loaded {
		return
	}
	data, err := json.Marshal(swSnapshot())
	if err != nil {
		return
	}
	swSaveGen.Add(1) // any queued write is older than this one
	swSaveMu.Lock()
	defer swSaveMu.Unlock()
	swWrite(data)
}

// ── What other screens show ───────────────────────────────────────────────

// stopwatchHubValue is the Tools hub tile's line: the running clock, else a
// paused one, else what the app is. It reads memory only (the tile is drawn
// every frame); true when a clock has time on it.
func stopwatchHubValue() (string, bool) {
	switch {
	case sw.cdRunning:
		return "Timer · " + swLeft(swCountdownLeft()) + " left", true
	case sw.running:
		return "Running · " + swClockShort(swElapsed()), true
	case sw.cdStarted:
		return "Timer paused · " + swLeft(sw.cdLeft) + " left", true
	case sw.banked > 0:
		return "Paused · " + swClockShort(sw.banked), true
	}
	return "Laps and a countdown timer", false
}

// qsLinkStopwatch is the Quick Settings button that opens the app
// (quicksettings.go qsOpenLink), numbered past that file's own link actions so
// a new one there cannot collide.
const qsLinkStopwatch qsLinkAction = 100

// stopwatchQSLine is the Quick Settings button's value: the time on the
// running clock (true: the button is lit), a paused one's, or "".
func stopwatchQSLine() (string, bool) {
	switch {
	case sw.cdRunning:
		return swLeft(swCountdownLeft()) + " left", true
	case sw.running:
		return swClockShort(swElapsed()), true
	case sw.cdStarted:
		return swLeft(sw.cdLeft) + " left", false
	case sw.banked > 0:
		return swClockShort(sw.banked), false
	}
	return "", false
}

// ── The in-game menu's row (gamemenu.go) ──────────────────────────────────

// gmStopwatch is the in-game menu's Stopwatch row, numbered past gamemenu.go's
// own rows so a new row there cannot collide.
const gmStopwatch gmItem = 100

// swGameMode is the clock the in-game row works: the one that has been
// started, or with both, the one whose tab was used last. A countdown that ran
// out keeps the row (A restarts it) until it is reset in the app: a row that
// vanished while the menu was open would move the next row under the
// highlight, and the A meant for the timer would press that one.
func swGameMode() (int, bool) {
	cd := sw.cdStarted || sw.cdDone
	st := sw.running || sw.banked > 0
	switch {
	case cd && st:
		return sw.tab, true
	case cd:
		return swTabCountdown, true
	case st:
		return swTabStopwatch, true
	}
	return 0, false
}

// stopwatchGameMenuShown reports whether the in-game menu lists the row: a
// clock has been started (running, or paused with time on it), or a countdown
// ran out.
func stopwatchGameMenuShown() bool {
	swEnsureLoaded()
	_, ok := swGameMode()
	return ok
}

// stopwatchGameMenuLabel is the row's words, live: "Stopwatch 12:34.5" or
// "Timer 4:59 left", and what the buttons do.
func stopwatchGameMenuLabel() (string, string) {
	mode, _ := swGameMode()
	a := orbKeyName("A")
	if mode == swTabCountdown {
		if sw.cdDone {
			return "Timer: time's up", swClockShort(sw.cdSet) + " timer · " + a + " restart"
		}
		label := "Timer " + swLeft(swCountdownLeft()) + " left"
		if sw.cdRunning {
			return label, a + " pause"
		}
		return label, "Paused · " + a + " resume"
	}
	label := "Stopwatch " + swClock(swElapsed())
	if sw.running {
		return label, a + " pause · Right lap"
	}
	return label, "Paused · " + a + " resume"
}

// stopwatchGameMenuPress acts on the row: A starts or pauses its clock, Right
// takes a lap. Each press is confirmed on RetroArch's message line, which shows
// once the game is resumed.
func stopwatchGameMenuPress(b gaButton) {
	mode, ok := swGameMode()
	if !ok {
		return
	}
	switch {
	case b == gaBtnA && mode == swTabCountdown:
		swCdToggle()
		state := "paused"
		if sw.cdRunning {
			state = "running"
		}
		swGameMessage("Timer " + state + " · " + swLeft(swCountdownLeft()) + " left")
	case b == gaBtnA:
		swToggle()
		state := "paused"
		if sw.running {
			state = "running"
		}
		swGameMessage("Stopwatch " + state + " · " + swClock(swElapsed()))
	case b == gaBtnRight && mode == swTabStopwatch && sw.running:
		swLap()
		if n := len(sw.laps); n > 0 {
			swGameMessage(fmt.Sprintf("Lap %d · %s (total %s)", n, swClock(swLapTime(n-1)), swClock(sw.laps[n-1])))
		}
	}
}
