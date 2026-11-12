package main

import (
	"strings"
	"testing"
	"time"
)

// hourlyNext is the schedule used below: one run at the top of every hour.
func hourlyNext(t time.Time) (time.Time, bool) {
	return t.Truncate(time.Hour).Add(time.Hour), true
}

// TestSchedJumpAwareRun covers SYS-13 with explicit times (no real clock): a
// forward jump across several intervals yields one run, a backward jump yields
// none for an interval already handled, and a small step still yields one.
func TestSchedJumpAwareRun(t *testing.T) {
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time {
		return base.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}
	tests := []struct {
		name     string
		handled  time.Time
		now      time.Time
		wantDue  bool
		wantRun  time.Time
		wantKeep time.Time // the recorded handled time
	}{
		{
			name: "normal tick between runs is not due", handled: at(1, 0), now: at(1, 30),
			wantKeep: at(1, 0),
		},
		{
			name: "normal tick crossing one run is due once", handled: at(0, 0), now: at(1, 0).Add(30 * time.Second),
			wantDue: true, wantRun: at(1, 0), wantKeep: at(1, 0),
		},
		{
			name: "forward jump over six runs collapses to one", handled: at(0, 0), now: at(6, 30),
			wantDue: true, wantRun: at(6, 0), wantKeep: at(6, 0),
		},
		{
			name: "backward jump leaves an already handled run alone", handled: at(10, 0), now: at(9, 30),
			wantKeep: at(10, 0),
		},
		{
			name: "backward jump inside the handled interval does not repeat it", handled: at(10, 0), now: at(10, 5),
			wantKeep: at(10, 0),
		},
		{
			name: "after a backward jump the next run is still due", handled: at(10, 0), now: at(11, 0).Add(10 * time.Second),
			wantDue: true, wantRun: at(11, 0), wantKeep: at(11, 0),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run, advance, due := schedJumpAwareRun(tc.handled, tc.now, hourlyNext)
			if due != tc.wantDue {
				t.Fatalf("due = %v, want %v", due, tc.wantDue)
			}
			if !advance.Equal(tc.wantKeep) {
				t.Fatalf("handled = %v, want %v", advance, tc.wantKeep)
			}
			if due && !run.Equal(tc.wantRun) {
				t.Fatalf("run = %v, want %v", run, tc.wantRun)
			}
			if !due && !run.IsZero() {
				t.Fatalf("run = %v on a tick with nothing due", run)
			}
		})
	}
}

// TestSchedForwardJumpFiresExactlyOnce ticks a minute at a time across a jump
// that passed seven runs: the first tick handles one run, and no later tick
// fires the missed ones again.
func TestSchedForwardJumpFiresExactlyOnce(t *testing.T) {
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	handled := base
	now := base.Add(7*time.Hour + 30*time.Minute)
	due := 0
	for i := 0; i < 25; i++ { // 07:30 to 07:54, still short of the 08:00 run
		run, advance, isDue := schedJumpAwareRun(handled, now, hourlyNext)
		if isDue {
			due++
			if !run.Equal(advance) {
				t.Fatalf("run %v and recorded time %v differ", run, advance)
			}
			handled = advance
		}
		now = now.Add(time.Minute)
	}
	if due != 1 {
		t.Fatalf("a jump over seven intervals fired %d runs, want 1", due)
	}
}

// TestSchedParseCrontabKeepsHandledRunOnBackwardJump is the production-path
// check: the scheduler screen must not show an already handled run as the next
// one after the clock moves back. Without the jump-aware decision the second
// parse shows 10:00, which the first parse had already handled.
func TestSchedParseCrontabKeepsHandledRunOnBackwardJump(t *testing.T) {
	source := "sys13-" + t.Name()
	const jobs = "0 * * * * /usr/bin/true"
	handledAt := time.Date(2026, 1, 2, 10, 30, 0, 0, time.UTC)
	first := schedParseCrontab(jobs, source, "test", "root", false, handledAt)
	if len(first) != 1 {
		t.Fatalf("parsed %d items, want 1", len(first))
	}
	if !strings.Contains(first[0].Next, "11:00") {
		t.Fatalf("first parse next = %q, want 11:00", first[0].Next)
	}

	back := handledAt.Add(-time.Hour) // the clock now reads 09:30
	second := schedParseCrontab(jobs, source, "test", "root", false, back)
	want := time.Date(2026, 1, 2, 11, 0, 0, 0, time.UTC)
	if !second[0].nextAt.Equal(want) {
		t.Fatalf("after a backward jump next = %v, want %v (the handled run stays handled)", second[0].nextAt, want)
	}
}
