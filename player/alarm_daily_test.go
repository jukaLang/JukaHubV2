package main

import (
	"testing"
	"time"
)

// A daily alarm rings once per day, every day. The old "fired" flag was only
// cleared by a launch at exactly midnight, so most alarms rang once and then
// never again.
func TestAlarmRingsAgainTheNextDay(t *testing.T) {
	loc := time.Local
	a := alarmEntry{Hour: 7, Minute: 30, Enabled: true}
	day1 := time.Date(2026, 11, 4, 7, 30, 10, 0, loc)
	if !alarmDue(a, day1) {
		t.Fatal("the alarm is not due at its own minute")
	}
	a.FiredOn = alarmDate(day1)
	if alarmDue(a, day1.Add(20*time.Second)) {
		t.Fatal("the alarm rang twice on the same day")
	}
	day2 := day1.Add(24 * time.Hour)
	if !alarmDue(a, day2) {
		t.Fatal("the alarm did not ring again the next day")
	}
	// A frame that stalled over the exact minute still gets the ring.
	if !alarmDue(alarmEntry{Hour: 7, Minute: 30, Enabled: true}, day1.Add(3*time.Minute)) {
		t.Fatal("a few minutes late, the alarm was skipped for the day")
	}
	if alarmDue(alarmEntry{Hour: 7, Minute: 30, Enabled: true}, day1.Add(alarmCatchUp)) {
		t.Fatal("an alarm rang long after its time")
	}
	if alarmDue(alarmEntry{Hour: 7, Minute: 30}, day1) {
		t.Fatal("a disabled alarm rang")
	}
}

// An alarm set for a time already past today starts tomorrow instead of
// ringing at once through the catch-up window.
func TestAlarmArmedAfterItsTimeStartsTomorrow(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 11, 4, 7, 32, 0, 0, loc)
	past := alarmEntry{Hour: 7, Minute: 30, Enabled: true}
	alarmArmFrom(&past, now)
	if alarmDue(past, now) {
		t.Fatal("an alarm set two minutes after its time rang at once")
	}
	if !alarmDue(past, time.Date(2026, 11, 5, 7, 30, 0, 0, loc)) {
		t.Fatal("it did not ring the next morning")
	}
	future := alarmEntry{Hour: 8, Minute: 0, Enabled: true}
	alarmArmFrom(&future, now)
	if !alarmDue(future, time.Date(2026, 11, 4, 8, 0, 0, 0, loc)) {
		t.Fatal("an alarm set for later today did not ring")
	}
}
