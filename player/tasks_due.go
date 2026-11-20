package main

// tasks_due.go — when a task is due, and how that reads in the Tasks app.
//
// A due value is local wall time written as text: "2006-01-02" for a day, or
// "2006-01-02T15:04" for a day and a time. It is text rather than an instant
// because a task set for 9:00 is a 9:00 task wherever the handheld travels and
// whatever zone it is set to, and a day without a time must not become "23:00
// the day before" after a change of zone.
//
// Days are compared by number (tasksDayIndex) and moved with AddDate on noon,
// so no step ever lands on the hour a daylight-saving change skips. Everything
// here is pure apart from the label, which reads the date and clock settings.

import (
	"fmt"
	"strings"
	"time"
)

const (
	tasksDayLayout  = "2006-01-02"
	tasksTimeLayout = "2006-01-02T15:04"
	// tasksRemindLate is how long after its time a timed task still reminds:
	// the check runs every 15 seconds, and a frame that blocked or a short
	// sleep must not step over the minute. Longer than that is missed.
	tasksRemindLate = 5 * time.Minute
	// tasksDefaultHour is the time a date gets when a time is switched on for
	// a day other than today.
	tasksDefaultHour = 9
)

// The repeat values a task can carry. "" is a task that happens once.
const (
	tasksRepeatNone   = ""
	tasksRepeatDaily  = "daily"
	tasksRepeatWeekly = "weekly"
)

// tasksDue is a parsed due value: the day at noon, local time, and, when
// timed is set, min minutes after that day's midnight.
type tasksDue struct {
	day   time.Time
	timed bool
	min   int
}

// tasksParseDue reads a stored due value. It reports false for "" and for
// anything that is not one of the two layouts.
func tasksParseDue(s string) (tasksDue, bool) {
	s = strings.TrimSpace(s)
	switch len(s) {
	case len(tasksDayLayout):
		t, err := time.ParseInLocation(tasksDayLayout, s, time.Local)
		if err != nil {
			return tasksDue{}, false
		}
		return tasksDue{day: tasksNoon(t)}, true
	case len(tasksTimeLayout):
		t, err := time.ParseInLocation(tasksTimeLayout, s, time.Local)
		if err != nil {
			return tasksDue{}, false
		}
		return tasksDue{day: tasksNoon(t), timed: true, min: t.Hour()*60 + t.Minute()}, true
	}
	return tasksDue{}, false
}

// tasksNoon is t's date at noon, local time.
func tasksNoon(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 12, 0, 0, 0, time.Local)
}

// tasksDayIndex numbers local dates, so two can be compared and subtracted
// without time-of-day or daylight-saving arithmetic.
func tasksDayIndex(t time.Time) int {
	y, m, d := t.Date()
	return int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// set reports whether d holds a day at all.
func (d tasksDue) set() bool { return !d.day.IsZero() }

// String is the stored form of d, "" when it holds no day. It is written from
// the fields rather than through time.Date, which would move a time inside a
// daylight-saving gap to the next hour.
func (d tasksDue) String() string {
	if !d.set() {
		return ""
	}
	y, m, dd := d.day.Date()
	if !d.timed {
		return fmt.Sprintf("%04d-%02d-%02d", y, int(m), dd)
	}
	return fmt.Sprintf("%04d-%02d-%02dT%02d:%02d", y, int(m), dd, d.min/60, d.min%60)
}

// at is the moment a timed due falls on, and the start of the day for a date.
func (d tasksDue) at() time.Time {
	y, m, dd := d.day.Date()
	if !d.timed {
		return time.Date(y, m, dd, 0, 0, 0, 0, time.Local)
	}
	return time.Date(y, m, dd, d.min/60, d.min%60, 0, 0, time.Local)
}

// remindWindow is when d's reminder may show. A timed task reminds at its time
// and for tasksRemindLate after it; a day reminds from 9:00 to noon on that
// day, the rule calendar reminders use for an all-day event: a word in the
// morning, and nothing in the evening for a morning the device slept through.
func (d tasksDue) remindWindow() (from, until time.Time) {
	if !d.timed {
		y, m, dd := d.day.Date()
		return time.Date(y, m, dd, calRemindAllDayHour, 0, 0, 0, time.Local),
			time.Date(y, m, dd, calRemindAllDayUntil, 0, 0, 0, time.Local)
	}
	from = d.at()
	return from, from.Add(tasksRemindLate)
}

// overdue reports whether d is behind now: a time once its minute is over, a
// day once the day is.
func (d tasksDue) overdue(now time.Time) bool {
	if !d.set() {
		return false
	}
	if d.timed {
		return !now.Before(d.at().Add(time.Minute))
	}
	return tasksDayIndex(d.day) < tasksDayIndex(now)
}

// byDay reports whether d falls on day or before it.
func (d tasksDue) byDay(day time.Time) bool {
	return d.set() && tasksDayIndex(d.day) <= tasksDayIndex(day)
}

// onDay reports whether d falls on day.
func (d tasksDue) onDay(day time.Time) bool {
	return d.set() && tasksDayIndex(d.day) == tasksDayIndex(day)
}

// sortKey orders due values: by day, and within a day the ones without a time
// first (they are "some time today"), then by time.
func (d tasksDue) sortKey() int64 {
	k := int64(tasksDayIndex(d.day)) * 1441
	if d.timed {
		k += int64(d.min) + 1
	}
	return k
}

// addDays moves d by n days, keeping its time.
func (d tasksDue) addDays(n int) tasksDue {
	if !d.set() {
		return d
	}
	d.day = d.day.AddDate(0, 0, n)
	return d
}

// addMinutes moves a timed due's time by n minutes (a multiple of five),
// wrapping inside its day: the picker has a row of its own for the day. A time
// off the five-minute grid (one typed on the phone) snaps to the grid in the
// step's direction first, with the snap counted as the step's first five, so
// every step lands on a round time.
func (d tasksDue) addMinutes(n int) tasksDue {
	if !d.timed || n == 0 {
		return d
	}
	m := d.min
	if m%5 != 0 {
		if n > 0 {
			m += 5 - m%5
			n -= 5
		} else {
			m -= m % 5
			n += 5
		}
	}
	d.min = ((m+n)%1440 + 1440) % 1440
	return d
}

// withTime switches d's time on or off. A time switched on for today is the
// next whole hour (the last five minutes of the day at the latest), for
// another day tasksDefaultHour.
func (d tasksDue) withTime(on bool, now time.Time) tasksDue {
	if !d.set() || d.timed == on {
		return d
	}
	d.timed = on
	if !on {
		d.min = 0
		return d
	}
	d.min = tasksDefaultHour * 60
	if d.onDay(now) {
		d.min = min((now.Hour()+1)*60, 23*60+55)
	}
	return d
}

// tasksDueOn is a day without a time.
func tasksDueOn(t time.Time) tasksDue { return tasksDue{day: tasksNoon(t)} }

// tasksRepeatStep is a repeat's step in days, 0 for none.
func tasksRepeatStep(r string) int {
	switch r {
	case tasksRepeatDaily:
		return 1
	case tasksRepeatWeekly:
		return 7
	}
	return 0
}

// tasksRepeatLabel is a repeat as the item sheet names it.
func tasksRepeatLabel(r string) string {
	switch r {
	case tasksRepeatDaily:
		return "Every day"
	case tasksRepeatWeekly:
		return "Every week"
	}
	return "Never"
}

// tasksNextDue is where a repeating task goes when it is ticked off: one step
// on at least, and on until it lies ahead — tomorrow or later for a day, a
// moment after now for a time — so a daily task ticked three days late comes
// back tomorrow, not the day after the day it was due.
func tasksNextDue(d tasksDue, repeat string, now time.Time) tasksDue {
	step := tasksRepeatStep(repeat)
	if step == 0 || !d.set() {
		return d
	}
	today := tasksDayIndex(now)
	n := 1
	if behind := today - tasksDayIndex(d.day); behind > step {
		// Jump to within a step of today instead of walking day by day
		// through a task that has waited for months.
		n = behind / step
	}
	next := d.addDays(n * step)
	for i := 0; i < 4; i++ {
		ahead := tasksDayIndex(next.day) > today
		if next.timed {
			ahead = next.at().After(now)
		}
		if ahead {
			break
		}
		n++
		next = d.addDays(n * step)
	}
	return next
}

// tasksDueLabel is how a due reads in a row: "Today", "Tomorrow 9:30",
// "Yesterday", "Mon 17 Nov 15:00", with the year only when it is not this
// year's. With a config it is main thread only (it reads the date and clock
// settings); with nil, any goroutine gets the settings the main thread last
// saw (the phone companion's labels).
func tasksDueLabel(config *Config, d tasksDue, now time.Time) string {
	if !d.set() {
		return ""
	}
	var day string
	switch tasksDayIndex(d.day) - tasksDayIndex(now) {
	case 0:
		day = "Today"
	case 1:
		day = "Tomorrow"
	case -1:
		day = "Yesterday"
	default:
		if d.day.Year() == now.Year() {
			day = formatDateBrief(config, d.day)
		} else {
			day = formatDate(config, d.day)
		}
	}
	if d.timed {
		day += " " + clockDigitsHM(config, d.min/60, d.min%60)
	}
	return day
}
