package main

// clockjump.go — "the clock jumped": network time set the clock
// (ntpsync.go), or the user chose another time zone (datetime.go). Either way
// the local date and hour moved under code that keeps wall-clock state.
//
// The notification is a counter plus the size of the move: clockJumpNotify may
// be called from any goroutine, and each observer on the main thread compares
// the count with the one it last saw (clockJumpSeen) on its own tick and takes
// the shift since then with it, so nothing runs on the network goroutine and no
// observer can be missed or called twice. The alarms (alarmCheck, once a
// second) and the playtime tracker (playtimeTick, every frame) observe it.
//
// The size matters: an hourly correction of three seconds must not re-date an
// evening's playtime, and a snooze is moved by exactly what the clock moved.

import (
	"sync"
	"sync/atomic"
	"time"
)

// clockShift is how far a jump moved things. clock is the shift of the instant
// itself (network time set the clock); wall is the shift of the local wall
// clock, which is the clock's shift plus any change of the zone's UTC offset.
// A zone change moves the wall clock but no instant.
type clockShift struct {
	clock, wall time.Duration
}

// clockJumps is the running total. count is read every frame without the
// lock; the lock keeps count and total consistent for the observer that saw
// count change.
var clockJumps struct {
	count atomic.Uint64
	mu    sync.Mutex
	total clockShift // guarded by mu
}

// clockConfirmCount counts syncs that found the clock already right
// (ntpsync.go): whatever was dated before one of them was dated right.
var clockConfirmCount atomic.Uint64

// clockJumpView is one observer's view of the total (main thread).
type clockJumpView struct {
	count uint64
	total clockShift
}

// clockJumpNotify records that the clock moved by clock and the local wall
// clock by wall (clock plus the change of UTC offset).
func clockJumpNotify(clock, wall time.Duration) {
	clockJumps.mu.Lock()
	clockJumps.total.clock += clock
	clockJumps.total.wall += wall
	clockJumps.count.Add(1)
	clockJumps.mu.Unlock()
}

// clockConfirmNotify records that network time found the clock right.
func clockConfirmNotify() { clockConfirmCount.Add(1) }

// clockJumpSeen reports whether the clock jumped since v was last brought up
// to date and by how much in all, and brings v up to date.
func clockJumpSeen(v *clockJumpView) (clockShift, bool) {
	if clockJumps.count.Load() == v.count {
		return clockShift{}, false
	}
	clockJumps.mu.Lock()
	n, total := clockJumps.count.Load(), clockJumps.total
	clockJumps.mu.Unlock()
	d := clockShift{clock: total.clock - v.total.clock, wall: total.wall - v.total.wall}
	v.count, v.total = n, total
	return d, true
}

// ── Scheduled tasks ───────────────────────────────────────────────────────

// schedJumpAwareRun decides which single scheduled run a tick at now handles.
// handled is the run the caller last recorded as handled, and next returns the
// first run strictly after a time (schedCronNext). It returns the run to
// handle, the time to record as handled, and whether one is due.
//
// A forward jump can pass several runs at once: only the latest run at or
// before now is returned, and the record moves with it, so the following tick
// sees the next run still ahead instead of replaying the missed ones in a
// burst. A backward jump leaves handled ahead of now, and nothing is due until
// the clock passes the run after the one already handled, so an interval that
// already ran is not run a second time. A normal tick that crosses one run
// returns that run exactly once.
func schedJumpAwareRun(handled, now time.Time, next func(time.Time) (time.Time, bool)) (run, advance time.Time, due bool) {
	if next == nil {
		return time.Time{}, handled, false
	}
	first, ok := next(handled)
	if !ok || now.Before(first) {
		return time.Time{}, handled, false
	}
	// Walk to the last run at or before now: on a jump this is the loop over
	// the missed intervals, on a normal tick it stops at the first.
	latest := first
	for more, ok := next(latest); ok && !more.After(now); more, ok = next(latest) {
		latest = more
	}
	return latest, latest, true
}

// schedHandledRuns is the run each scheduled item last handled, keyed by its
// source and command. The scheduler screen is its only consumer today: it
// recomputes next runs on every refresh, and this record is what makes the
// jump-aware decision apply on the shipped path. A task runner, when one is
// added, reads and writes the same record.
var schedHandledRuns struct {
	sync.Mutex
	runs map[string]time.Time
}

// schedHandledRun returns the run key last handled. An item seen for the first
// time starts at now, so opening the screen does not count a run that predates
// it as missed.
func schedHandledRun(key string, now time.Time) time.Time {
	schedHandledRuns.Lock()
	defer schedHandledRuns.Unlock()
	if schedHandledRuns.runs == nil {
		schedHandledRuns.runs = map[string]time.Time{}
	}
	if t, ok := schedHandledRuns.runs[key]; ok {
		return t
	}
	schedHandledRuns.runs[key] = now
	return now
}

// schedRememberHandledRun records at as handled, never moving the record
// backwards.
func schedRememberHandledRun(key string, at time.Time) {
	schedHandledRuns.Lock()
	defer schedHandledRuns.Unlock()
	if schedHandledRuns.runs == nil {
		schedHandledRuns.runs = map[string]time.Time{}
	}
	if cur, ok := schedHandledRuns.runs[key]; !ok || at.After(cur) {
		schedHandledRuns.runs[key] = at
	}
}

// ── Alarms ────────────────────────────────────────────────────────────────

// alarmJumpView is the alarms' view of the jumps (main thread).
var alarmJumpView clockJumpView

// alarmClockJumpCheck re-anchors the alarms after a jump (alarmCheck).
func alarmClockJumpCheck(now time.Time) {
	shift, jumped := clockJumpSeen(&alarmJumpView)
	if !jumped {
		return
	}
	if alarmReanchor(alarmStore.Alarms, now, shift.clock) {
		alarmSave()
		logInfo("clock", "alarms re-anchored after a clock change")
	}
}

// alarmReanchor repairs what a wrong clock left in the alarms and reports
// whether anything changed; shift is how far the clock itself just moved (zero
// for a change of zone, which moves no instant). A clock that ran ahead marked
// alarms done for a day that has not come yet (FiredOn/RangOn after today),
// which would have silenced that day's real ring; they are re-armed from now
// the way a new alarm is. A snooze is never dropped: one the old clock had
// pending (or just due) was set by that clock and moves with it, so it rings
// as many minutes after the press as it should — a forward jump would
// otherwise push it out of the catch-up window and lose it — and none is left
// further off than a snooze is long. A snooze that had lapsed before the jump
// (the device slept through it) stays lapsed. The alarm times themselves need
// nothing: alarmDue compares them with the clock every second.
func alarmReanchor(alarms []alarmEntry, now time.Time, shift time.Duration) bool {
	today := alarmDate(now)
	snoozeLen := time.Duration(alarmSnoozeMinutes) * time.Minute
	latest := now.Add(snoozeLen)
	// The old clock's reading at this moment, and the range a snooze it set
	// can lie in: up to a snooze ahead of it, or just due.
	before := now.Add(-shift)
	setFrom, setTo := before.Add(-alarmCatchUp), before.Add(snoozeLen)
	changed := false
	for i := range alarms {
		a := &alarms[i]
		if a.FiredOn > today {
			snooze := a.SnoozeUntil
			alarmArmFrom(a, now)
			// alarmArmFrom clears the snooze, as a new alarm should; a
			// re-anchor keeps it and repairs it below.
			a.SnoozeUntil = snooze
			changed = true
		}
		if a.RangOn > today {
			a.RangOn = ""
			changed = true
		}
		if a.SnoozeUntil == "" {
			continue
		}
		until, err := time.Parse(time.RFC3339, a.SnoozeUntil)
		if err != nil {
			continue
		}
		fixed := until
		if shift != 0 && !until.Before(setFrom) && !until.After(setTo) {
			fixed = until.Add(shift)
		}
		if fixed.After(latest) {
			fixed = latest
		}
		if !fixed.Equal(until) {
			a.SnoozeUntil = fixed.Format(time.RFC3339)
			changed = true
		}
	}
	return changed
}

// ── Playtime ──────────────────────────────────────────────────────────────

// playtimeRun is what this run of JukaHub recorded since the clock was last
// known right (a sync found it in time) or last re-anchored, by the day key it
// went under, so a jump can move what a wrong clock dated. Usually one key,
// two across midnight.
var playtimeRun struct {
	days    map[string]int64
	view    clockJumpView
	confirm uint64
}

// playtimeRunNote counts seconds recorded under day (playtimeRecord).
func playtimeRunNote(day string, secs int64) {
	if playtimeRun.days == nil || (len(playtimeRun.days) >= 4 && playtimeRun.days[day] == 0) {
		// Days on end with neither a sync nor a jump: the old keys no longer
		// matter.
		playtimeRun.days = make(map[string]int64, 2)
	}
	playtimeRun.days[day] += secs
}

// playtimeClockJumpCheck re-anchors the playtime days after a jump
// (playtimeTick).
func playtimeClockJumpCheck() {
	if n := clockConfirmCount.Load(); n != playtimeRun.confirm {
		// Network time found the clock right: everything recorded so far
		// went under the right date.
		playtimeRun.confirm = n
		playtimeRun.days = nil
	}
	shift, jumped := clockJumpSeen(&playtimeRun.view)
	if !jumped {
		return
	}
	if playtime.data.Days != nil {
		now := time.Now()
		today := now.Format("2006-01-02") // playtimeTodayKey
		// The date the old clock (and zone) showed at this moment.
		before := now.Add(-shift.wall).Format("2006-01-02")
		if playtimeReanchorDays(playtime.data.Days, before, playtimeRun.days[before], shift.wall, today) {
			playtime.rev++
			logInfo("clock", "playtime days re-anchored to %s after a clock change", today)
		}
		// The trophies' lifetime record of days follows (playtime.go).
		if trophyLifeReanchor(&playtime.data, before, today, shift.wall) {
			playtime.rev++
		}
	}
	// What was recorded so far sits where it belongs now; a later jump moves
	// only what is recorded after this one.
	playtimeRun.days = nil
}

// playtimeReanchorDays moves time recorded under a wrong date to today and
// reports whether anything moved. Days after today can only come from a clock
// that ran ahead: they are folded into today. When the jump changed the date
// (before, the date the old clock showed, is not today), the seconds this run
// recorded under before since the clock was last known right (runSecs)
// happened today — typically a handheld that booted with a stale clock and
// recorded its first minutes under that stale date. Never more moves than the
// jump could have misdated (|shift|: three seconds of correction just after
// midnight re-date three seconds, not the evening) or than the day holds. A
// jump that left the date alone (an idle night, then a small correction or a
// zone change in the morning) moves nothing.
func playtimeReanchorDays(days map[string]int64, before string, runSecs int64, shift time.Duration, today string) bool {
	changed := false
	for day, secs := range days {
		if day > today {
			delete(days, day)
			days[today] += secs
			changed = true
		}
	}
	if before == "" || before == today || runSecs <= 0 {
		return changed
	}
	if shift < 0 {
		shift = -shift
	}
	move := runSecs
	if limit := int64(shift / time.Second); move > limit {
		move = limit
	}
	if have := days[before]; move > have {
		move = have
	}
	if move <= 0 {
		return changed
	}
	days[before] -= move
	if days[before] <= 0 {
		delete(days, before)
	}
	days[today] += move
	return true
}
