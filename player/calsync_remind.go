package main

// calsync_remind.go — event reminders, and "Set alarm" for an event.
//
// A timed event shows a notification reminder() minutes before it starts
// (Settings → Email & calendar), an all-day event one at 9:00 on its first
// day — a birthday is worth a word in the morning, and a reminder at
// midnight would only be seen the next day. Each instance reminds once: the
// keys of the ones shown (UID and start) are kept in memory and in
// mailcalDataDir()/reminded.json, so a restart does not repeat them.
//
// The handheld has no battery-backed clock and starts before network time
// sets it, so a reminder only fires on a clock that is trusted: never one
// before this build's date (clockFloor), and where JukaHub sets the clock
// itself, only after a network sync confirmed or set it (ntpsync.go), or the
// user set it by hand. A reminder is never shown for an event long gone: one
// that was missed while the device was off or the clock was wrong stays
// missed, as on a phone.
//
// The check rides on the alarms' once-a-second tick (alarmCheck), which runs
// on every path of the main loop — under a running game too — and looks at
// the index every 15 seconds. Reminders follow the other notifications: the
// toast respects Quiet Mode and the "notifications during videos" switch
// (renderToast), and every one is filed unread in the bell. During a
// RetroArch game the library started, the reminder also goes to RetroArch's
// own on-screen messages, because JukaHub's toast is not on screen then.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// calRemindEvery is how often the index is checked.
	calRemindEvery = 15 * time.Second
	// calRemindLate is how long after its start a timed event still reminds
	// (a check that came just after the moment).
	calRemindLate = 2 * time.Minute
	// calRemindAllDayHour and calRemindAllDayUntil frame the all-day
	// reminder: from 9:00, and not after noon (a device switched on in the
	// evening does not announce the morning's birthday).
	calRemindAllDayHour  = 9
	calRemindAllDayUntil = 12
	// calRemindKeep is how long a shown reminder's key is remembered.
	calRemindKeep = 3 * 24 * time.Hour
)

// calRemind is the reminder state. Main thread only.
var calRemind struct {
	last   time.Time
	loaded bool
	fired  map[string]int64 // key → instance start (Unix), for pruning
	floor  time.Time
	// trusted latches once network time has confirmed or set the clock.
	trusted bool
}

// calRemindClockTrusted reports whether the clock can be believed.
func calRemindClockTrusted(now time.Time) bool {
	if calRemind.floor.IsZero() {
		calRemind.floor = clockFloor()
	}
	if now.Before(calRemind.floor) {
		return false
	}
	if calRemind.trusted {
		return true
	}
	if netTime.enabled.Load() && clockSetSupported() {
		// JukaHub keeps the time here: believe it once network time has
		// confirmed it or set it. clockJumps.count alone is not that: a
		// change of zone (dateTimeSetLocal) counts too, with no shift of the
		// clock itself; a set does shift it. (Setting it by hand switches
		// network time off, which the branch below trusts.)
		if clockConfirmCount.Load() > 0 {
			calRemind.trusted = true
		} else if clockJumps.count.Load() > 0 {
			clockJumps.mu.Lock()
			calRemind.trusted = clockJumps.total.clock != 0
			clockJumps.mu.Unlock()
		}
		return calRemind.trusted
	}
	// A PC, or a handheld whose owner keeps the time by hand.
	return true
}

// calRemindPath is reminded.json.
func calRemindPath() string {
	dir := mailcalDataDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "reminded.json")
}

// calRemindLoad reads the shown keys once.
func calRemindLoad() {
	calRemind.loaded = true
	calRemind.fired = map[string]int64{}
	p := calRemindPath()
	if p == "" {
		return
	}
	f, err := os.Open(p)
	if err != nil {
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return
	}
	var m map[string]int64
	if json.Unmarshal(data, &m) == nil {
		calRemind.fired = m
	}
}

// calRemindSave prunes old keys and writes the rest off the main thread.
func calRemindSave(now time.Time) {
	cut := now.Add(-calRemindKeep).Unix()
	out := make(map[string]int64, len(calRemind.fired))
	for k, at := range calRemind.fired {
		if at >= cut {
			out[k] = at
		}
	}
	calRemind.fired = out
	p := calRemindPath()
	if p == "" {
		return
	}
	data, err := json.Marshal(out)
	if err != nil {
		return
	}
	go func() {
		if err := calSyncWriteFile(p, data); err != nil {
			logWarn("calendar", "could not save the reminders shown: %v", err)
		}
	}()
}

// calRemindTick is called from alarmCheck once a second. Main thread.
func calRemindTick(now time.Time) {
	if !calSync.started.Load() {
		return
	}
	if !calRemind.last.IsZero() {
		if d := now.Sub(calRemind.last); d >= 0 && d < calRemindEvery {
			return
		}
	}
	calRemind.last = now
	s := mailcalSettingsGet()
	if s.NoReminders || !calRemindClockTrusted(now) {
		return
	}
	if !calRemind.loaded {
		calRemindLoad()
	}
	lead := time.Duration(s.reminder()) * time.Minute
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.Local)

	type due struct {
		key string
		msg string
		at  time.Time
	}
	var fire []due
	for _, ev := range calEventsBetween(now.Add(-calRemindLate), now.Add(lead+time.Minute)) {
		var key, msg string
		if ev.AllDay {
			if !calSameDay(ev.Start, today) || now.Hour() < calRemindAllDayHour || now.Hour() >= calRemindAllDayUntil {
				continue
			}
			key = ev.UID + "|" + ev.Start.Format("20060102")
			msg = "Today: " + calEvTitle(ev)
		} else {
			if now.Before(ev.Start.Add(-lead)) || !now.Before(ev.Start.Add(calRemindLate)) {
				continue
			}
			key = ev.UID + "|" + fmt.Sprint(ev.Start.Unix())
			msg = calRemindText(ev, now)
		}
		if ev.UID == "" {
			key = ev.Title + key
		}
		if _, done := calRemind.fired[key]; done {
			continue
		}
		fire = append(fire, due{key: key, msg: msg, at: ev.Start})
	}
	if len(fire) == 0 {
		return
	}
	sort.SliceStable(fire, func(i, j int) bool { return fire[i].at.Before(fire[j].at) })
	for _, f := range fire {
		calRemind.fired[f.key] = f.at.Unix()
		// Every reminder is filed unread in the bell.
		notifRecord(f.msg, notifInfo)
	}
	calRemindSave(now)
	toast := fire[0].msg
	if len(fire) > 1 {
		toast = fmt.Sprintf("%s (and %d more in notifications)", fire[0].msg, len(fire)-1)
	}
	showToastPrimitive(toast, ToastKindInfo)
	if !quietModeOn() {
		PlaySuccessSound()
	}
	// Under a RetroArch game the toast is not on screen; RetroArch's own
	// message line is (held, like the toast, in Quiet Mode).
	if port := raLibraryPort(); port > 0 && !quietModeOn() {
		line := strings.ReplaceAll(toast, "\n", " ")
		go func() { _ = sendRetroArchNetCmd(port, "SHOW_MSG "+line) }()
	}
}

// calSameDay reports whether a and b fall on the same local date.
func calSameDay(a, b time.Time) bool {
	ay, am, ad := a.Local().Date()
	by, bm, bd := b.Local().Date()
	return ay == by && am == bm && ad == bd
}

// calEvTitle is an event's drawable title.
func calEvTitle(ev calEvent) string {
	if t := calSafeText(ev.Title, 80); t != "" {
		return t
	}
	return "Event"
}

// calRemindText is a timed reminder: "In 10 min: Standup · Room 4".
func calRemindText(ev calEvent, now time.Time) string {
	left := ev.Start.Sub(now)
	var when string
	switch {
	case left <= 30*time.Second:
		when = "Now"
	case left < time.Hour:
		when = fmt.Sprintf("In %d min", int((left+time.Minute-time.Second)/time.Minute))
	default:
		when = "At " + clockDigits(appConfig, ev.Start.Local())
	}
	msg := when + ": " + calEvTitle(ev)
	if loc := calSafeText(ev.Location, 40); loc != "" {
		msg += " · " + loc
	}
	return msg
}

// ── Set alarm ─────────────────────────────────────────────────────────────

// alarmSnoozeSpent reports whether a's snooze, if it has one, can no longer
// ring: past its end by more than the catch-up window (alarmDue), or
// unreadable. alarmCheck switches a one-time alarm off only then.
func alarmSnoozeSpent(a alarmEntry, now time.Time) bool {
	if a.SnoozeUntil == "" {
		return true
	}
	until, err := time.Parse(time.RFC3339, a.SnoozeUntil)
	return err != nil || now.Sub(until) >= alarmCatchUp
}

// calEvSetAlarm sets a one-time alarm for an event: reminder() minutes
// before it starts (at the start when that is already past), labelled with
// its title. Main thread: it changes the alarm list.
func calEvSetAlarm(ev calEvent) {
	if ev.AllDay {
		ShowToast("An all-day event has no start time to wake you for", ToastKindInfo)
		return
	}
	now := time.Now()
	lead := time.Duration(mailcalSettingsGet().reminder()) * time.Minute
	at := ev.Start.Add(-lead).Local()
	if !at.After(now) {
		at = ev.Start.Local()
	}
	// Alarms ring on the minute: one in the current minute would be armed
	// for tomorrow's same time.
	if at.Truncate(time.Minute).Sub(now.Truncate(time.Minute)) < time.Minute {
		ShowToast("That event starts too soon for an alarm", ToastKindInfo)
		return
	}
	label := calSafeText(ev.Title, 40)
	if label == "" {
		label = "Event"
	}
	date := alarmDate(at)
	for _, a := range alarmStore.Alarms {
		if a.Once == date && a.Hour == at.Hour() && a.Minute == at.Minute() && a.Label == label {
			ShowToast("That alarm is already set", ToastKindInfo)
			return
		}
	}
	alarmNextID++
	entry := alarmEntry{
		Hour:    at.Hour(),
		Minute:  at.Minute(),
		Label:   label,
		Sound:   alarmDefaultSound,
		Enabled: true,
		ID:      alarmNextID,
		Once:    date,
	}
	alarmArmFrom(&entry, now)
	alarmStore.Alarms = append(alarmStore.Alarms, entry)
	sort.SliceStable(alarmStore.Alarms, func(i, j int) bool {
		if alarmStore.Alarms[i].Hour != alarmStore.Alarms[j].Hour {
			return alarmStore.Alarms[i].Hour < alarmStore.Alarms[j].Hour
		}
		return alarmStore.Alarms[i].Minute < alarmStore.Alarms[j].Minute
	})
	alarmSave()
	trophyEvent(tkAlarmSet)
	day := "today"
	switch {
	case calSameDay(at, now.AddDate(0, 0, 1)):
		day = "tomorrow"
	case !calSameDay(at, now):
		day = formatDateBrief(appConfig, at)
	}
	PlayActivateSound()
	ShowToast(fmt.Sprintf("Alarm set for %s %s: %s", day, clockDigits(appConfig, at), label), ToastKindSuccess)
}
