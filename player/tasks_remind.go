package main

// tasks_remind.go — what the Tasks app does while it is not on screen: save,
// remind, keep "Ring as alarm" alarms in step, and say what the phone added.
//
// All of it rides on the alarms' once-a-second tick (alarmCheck), which runs
// on every path of the main loop — under a running game too — so a reminder
// is not held back by whatever screen is open.
//
// Reminders follow the calendar's (calsync_remind.go): they fire only on a
// clock that can be believed (calRemindClockTrusted: never before this build's
// date, and on a handheld that sets its own time only after network time
// confirmed it or the owner set it), each due once — the item remembers the
// due it reminded for, so a restart does not repeat it — and one missed while
// the device was off stays missed; the list still shows it as overdue. A
// timed task reminds at its time, a task with only a date at 9:00 that day.
// The toast respects Quiet Mode, every reminder is filed unread in the bell,
// and during a RetroArch game the library started it also goes to RetroArch's
// own on-screen messages, because JukaHub's toast is not on screen then.
//
// "Ring as alarm" puts a one-time alarm (alarmEntry.Once, the same kind the
// calendar's "Set alarm" makes) in the alarm list at the task's time, with
// the task's text as its label. Changing the task's text or time moves it,
// finishing or deleting the task removes it. The alarm list belongs to the
// main thread, so a change made from the phone is queued by the store and
// carried out here, within a second.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// tasksRemindEvery is how often the tasks are checked for a reminder.
const tasksRemindEvery = 15 * time.Second

// tasksRemind is the reminder check's state. Main thread only.
var tasksRemind struct {
	last time.Time
}

// tasksRemindTick is called from alarmCheck once a second. Main thread.
func tasksRemindTick(now time.Time) {
	tasksAlarmApply(now)
	tasksPhoneNews()
	if tasksSaveDue(now) {
		tasksSaveNow(false)
	}
	if !tasksRemind.last.IsZero() {
		if d := now.Sub(tasksRemind.last); d >= 0 && d < tasksRemindEvery {
			return
		}
	}
	tasksRemind.last = now
	if !calRemindClockTrusted(now) {
		return
	}
	msgs := tasksCollectReminders(now)
	if len(msgs) == 0 {
		return
	}
	for _, m := range msgs {
		// Every reminder is filed unread in the bell.
		notifRecord(m, notifInfo)
	}
	toast := msgs[0]
	if len(msgs) > 1 {
		toast = fmt.Sprintf("%s (and %d more in notifications)", msgs[0], len(msgs)-1)
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

// tasksCollectReminders marks every reminder that is owed at now as shown and
// returns their messages, earliest due first. A reminder whose window closed
// unseen is marked too, so it can never come up later.
func tasksCollectReminders(now time.Time) []string {
	type due struct {
		key int64
		msg string
	}
	var fire []due
	tasksMu.Lock()
	tasksEnsureLoadedLocked()
	marked := false
	for i := range tasksState.doc.Items {
		it := &tasksState.doc.Items[i]
		if !it.Remind || it.Done || it.Due == "" || it.Reminded == it.Due {
			continue
		}
		d, ok := tasksParseDue(it.Due)
		if !ok {
			continue
		}
		from, until := d.remindWindow()
		if now.Before(from) {
			continue
		}
		it.Reminded = it.Due
		marked = true
		if !now.Before(until) {
			continue
		}
		msg := "Due today: " + it.Text
		if d.timed {
			msg = "Due now: " + it.Text
		}
		fire = append(fire, due{key: d.sortKey(), msg: truncateRunes(msg, 120)})
	}
	if marked {
		// Bookkeeping only: the file is due a save, but nothing a screen
		// shows has changed.
		t := time.Now()
		if !tasksState.dirty {
			tasksState.dirtySince = t
		}
		tasksState.dirty, tasksState.dirtyAt = true, t
	}
	tasksMu.Unlock()
	sort.SliceStable(fire, func(i, j int) bool { return fire[i].key < fire[j].key })
	out := make([]string, len(fire))
	for i, f := range fire {
		out[i] = f.msg
	}
	return out
}

// tasksPhoneNews tells the device about tasks the phone added: a toast, and
// the phone code (if the Tasks app showed one) closes, since the phone is on
// the page now. Main thread.
func tasksPhoneNews() {
	tasksMu.Lock()
	text, n := tasksState.phoneAdded, tasksState.phoneAddedN
	tasksState.phoneAdded, tasksState.phoneAddedN = "", 0
	tasksMu.Unlock()
	if n == 0 {
		return
	}
	phoneCardArrived(phoneCardTasks)
	msg := "From your phone: " + truncateRunes(text, 60)
	if n > 1 {
		msg = fmt.Sprintf("%d tasks added from your phone", n)
	}
	showToastPrimitive(msg, ToastKindSuccess)
}

// ── Ring as alarm ─────────────────────────────────────────────────────────

// tasksAlarmWant is what one queued task needs from the alarm list.
type tasksAlarmWant struct {
	task    int
	alarmID int
	on      bool
	at      time.Time
	label   string
}

// tasksAlarmApply brings the alarm list in line with the tasks queued for it:
// an alarm set or moved for a task that should ring, removed for one that
// should not (finished, no time, switched off) or is gone. Main thread: the
// alarm list is the main loop's.
func tasksAlarmApply(now time.Time) {
	tasksMu.Lock()
	// The tasks are read on the first tick, so their links are checked
	// before anyone can set a new alarm (tasksAlarmLinksCheckLocked).
	tasksEnsureLoadedLocked()
	tasksAlarmLinksCheckLocked()
	if len(tasksState.alarmSync) == 0 && len(tasksState.alarmDrop) == 0 {
		tasksMu.Unlock()
		return
	}
	queued, drops := tasksState.alarmSync, tasksState.alarmDrop
	tasksState.alarmSync, tasksState.alarmDrop = nil, nil
	wants := make([]tasksAlarmWant, 0, len(queued))
	seen := make(map[int]bool, len(queued))
	for _, id := range queued {
		i := tasksItemIndexLocked(id)
		if seen[id] || i < 0 {
			continue
		}
		seen[id] = true
		it := tasksState.doc.Items[i]
		w := tasksAlarmWant{task: id, alarmID: it.AlarmID, label: truncateRunes(it.Text, alarmLabelMax)}
		if d, ok := tasksParseDue(it.Due); ok && it.Alarm && !it.Done && d.timed && d.at().After(now) {
			w.on, w.at = true, d.at()
		}
		wants = append(wants, w)
	}
	tasksMu.Unlock()

	changed := false
	for _, aid := range drops {
		changed = tasksAlarmRemove(aid) || changed
	}
	links := make(map[int]int, len(wants))
	for _, w := range wants {
		switch {
		case w.on:
			links[w.task] = tasksAlarmPut(w.alarmID, w.at, w.label, now)
			changed = true
		case w.alarmID != 0:
			changed = tasksAlarmRemove(w.alarmID) || changed
			links[w.task] = 0
		}
	}

	// Record the links. A task deleted while the alarm was being set leaves
	// that alarm behind; it goes too.
	var orphans []int
	if len(links) > 0 {
		tasksMu.Lock()
		for id, aid := range links {
			i := tasksItemIndexLocked(id)
			if i < 0 {
				if aid != 0 {
					orphans = append(orphans, aid)
				}
				continue
			}
			if tasksState.doc.Items[i].AlarmID != aid {
				tasksState.doc.Items[i].AlarmID = aid
				tasksChangedLocked()
			}
		}
		tasksMu.Unlock()
	}
	for _, aid := range orphans {
		changed = tasksAlarmRemove(aid) || changed
	}
	if changed {
		sort.SliceStable(alarmStore.Alarms, func(i, j int) bool {
			if alarmStore.Alarms[i].Hour != alarmStore.Alarms[j].Hour {
				return alarmStore.Alarms[i].Hour < alarmStore.Alarms[j].Hour
			}
			return alarmStore.Alarms[i].Minute < alarmStore.Alarms[j].Minute
		})
		alarmSave()
	}
}

// tasksAlarmLinksCheckLocked forgets, once after each read of tasks.json,
// every "Ring as alarm" link whose alarm is no longer in the alarm list
// (deleted in Alarms). Alarm ids are unique only within a run: after a
// restart the id of a deleted alarm can go to a new alarm of the user's, which
// the task would then move or delete as its own. A forgotten link makes a new
// alarm on the task's next change, as a deleted one did before. Caller holds
// tasksMu; main thread (it reads the alarm list).
func tasksAlarmLinksCheckLocked() {
	if !tasksState.loaded || tasksState.linksChecked {
		return
	}
	tasksState.linksChecked = true
	have := make(map[int]bool, len(alarmStore.Alarms))
	for _, a := range alarmStore.Alarms {
		have[a.ID] = true
	}
	changed := false
	for i := range tasksState.doc.Items {
		if it := &tasksState.doc.Items[i]; it.AlarmID != 0 && !have[it.AlarmID] {
			it.AlarmID = 0
			changed = true
		}
	}
	// So do the removals queued (from the phone) before this tick.
	drops := tasksState.alarmDrop[:0]
	for _, aid := range tasksState.alarmDrop {
		if have[aid] {
			drops = append(drops, aid)
		}
	}
	tasksState.alarmDrop = drops
	if changed {
		tasksChangedLocked()
	}
}

// tasksAlarmPut sets the one-time alarm with id aid to at and label, or
// makes a new one when aid is 0 or no longer in the list (deleted in Alarms),
// and returns its id. Main thread.
func tasksAlarmPut(aid int, at time.Time, label string, now time.Time) int {
	date := alarmDate(at)
	if aid != 0 {
		for i := range alarmStore.Alarms {
			a := &alarmStore.Alarms[i]
			if a.ID != aid {
				continue
			}
			a.Hour, a.Minute, a.Label, a.Once, a.Enabled = at.Hour(), at.Minute(), label, date, true
			alarmArmFrom(a, now)
			return aid
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
	return entry.ID
}

// tasksAlarmRemove takes alarm aid out of the list and reports whether it was
// there. The alarm screen's cursor stays on a row that exists. Main thread.
func tasksAlarmRemove(aid int) bool {
	if aid == 0 {
		return false
	}
	for i := range alarmStore.Alarms {
		if alarmStore.Alarms[i].ID != aid {
			continue
		}
		alarmStore.Alarms = append(alarmStore.Alarms[:i], alarmStore.Alarms[i+1:]...)
		if alarmUI.cursor >= len(alarmStore.Alarms) && alarmUI.cursor > 0 {
			alarmUI.cursor = len(alarmStore.Alarms) - 1
		}
		return true
	}
	return false
}
