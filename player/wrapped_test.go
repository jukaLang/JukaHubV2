package main

import (
	"testing"
	"time"
)

// wrappedTestData builds a snapshot with play only on the given day offsets
// (negative = days before today).
func wrappedTestData(now time.Time, offsets map[int]int64) playtimeData {
	data := newPlaytimeData()
	for offset, seconds := range offsets {
		day := now.AddDate(0, 0, offset).Format(wrappedDayKey)
		data.Days[day] = seconds
		data.Total += seconds
	}
	return data
}

// The grid must be whole calendar weeks so every column is a real week, and
// today must land in the last column, marked as the present rather than the
// future. Checked on a Monday, a Sunday and a mid-week day because the
// alignment arithmetic differs on each.
func TestWrappedInsightsGridIsWholeWeeksEndingToday(t *testing.T) {
	base := time.Date(2026, 10, 26, 15, 0, 0, 0, time.Local) // Monday
	for _, offset := range []int{0, 2, 6, 13} {
		now := base.AddDate(0, 0, offset)
		ins := buildWrappedInsights(newPlaytimeData(), now)
		if len(ins.Days) != wrappedDays {
			t.Fatalf("%s: %d cells, want %d", now.Weekday(), len(ins.Days), wrappedDays)
		}
		if got := ins.Days[0].Day.Weekday(); got != time.Monday {
			t.Fatalf("%s: first cell is %s, want Monday", now.Weekday(), got)
		}
		last := ins.Days[len(ins.Days)-1]
		if got := last.Day.Weekday(); got != time.Sunday {
			t.Fatalf("%s: last cell is %s, want Sunday", now.Weekday(), got)
		}
		// Today sits in the last column, on its own weekday row.
		todayRow := (int(now.Weekday()) + 6) % 7
		cell := ins.Days[len(ins.Days)-7+todayRow]
		if !sameDay(cell.Day, now) {
			t.Fatalf("%s: last column row %d is %s, want today", now.Weekday(), todayRow, cell.Day)
		}
		if cell.Future {
			t.Fatalf("%s: today is marked future", now.Weekday())
		}
		// Nothing before today may be future, and the tail after today must be.
		for i, day := range ins.Days {
			if day.Day.After(now) != day.Future {
				t.Fatalf("%s: cell %d (%s) future=%v", now.Weekday(), i, day.Day, day.Future)
			}
		}
	}
}

func sameDay(a, b time.Time) bool {
	return a.Format(wrappedDayKey) == b.Format(wrappedDayKey)
}

// An empty today must not erase a run: the day is still in progress. Adding
// today's minutes extends the run by one.
func TestWrappedInsightsStreakToleratesEmptyToday(t *testing.T) {
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.Local)
	data := wrappedTestData(now, map[int]int64{-1: 600, -2: 600, -3: 600})
	if got := buildWrappedInsights(data, now).CurrentStreak; got != 3 {
		t.Fatalf("streak with an empty today = %d, want 3", got)
	}
	data.Days[now.Format(wrappedDayKey)] = 60
	if got := buildWrappedInsights(data, now).CurrentStreak; got != 4 {
		t.Fatalf("streak with today played = %d, want 4", got)
	}
}

// A gap ends the current streak, and the longest run in the window is reported
// separately from it.
func TestWrappedInsightsLongestStreakIgnoresGap(t *testing.T) {
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.Local)
	data := wrappedTestData(now, map[int]int64{
		-1: 60, -2: 60, // two-day run, current
		-10: 60, -11: 60, -12: 60, -13: 60, // four-day run, older
	})
	ins := buildWrappedInsights(data, now)
	if ins.CurrentStreak != 2 {
		t.Fatalf("current streak = %d, want 2", ins.CurrentStreak)
	}
	if ins.LongestStreak != 4 {
		t.Fatalf("longest streak = %d, want 4", ins.LongestStreak)
	}
}

// Totals, the best day and the average only count days in the window, and a
// tie goes to the most recent day.
func TestWrappedInsightsCountsWindowOnly(t *testing.T) {
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.Local)
	data := newPlaytimeData()
	data.Days[now.Format(wrappedDayKey)] = 1800
	data.Days[now.AddDate(0, 0, -1).Format(wrappedDayKey)] = 3600
	data.Days[now.AddDate(0, 0, -2).Format(wrappedDayKey)] = 3600
	// Outside the 13-week window: must not leak into any reading.
	data.Days[now.AddDate(0, 0, -400).Format(wrappedDayKey)] = 99999

	ins := buildWrappedInsights(data, now)
	if ins.Total != 9000 {
		t.Fatalf("window total = %d, want 9000", ins.Total)
	}
	if ins.ActiveDays != 3 {
		t.Fatalf("active days = %d, want 3", ins.ActiveDays)
	}
	if ins.BestSeconds != 3600 {
		t.Fatalf("best day = %d, want 3600", ins.BestSeconds)
	}
	if !sameDay(ins.BestDay, now.AddDate(0, 0, -1)) {
		t.Fatalf("best day = %s, want the most recent tie (yesterday)", ins.BestDay.Format(wrappedDayKey))
	}
	if ins.DailyAverage != 3000 {
		t.Fatalf("daily average = %d, want 3000 (total over active days)", ins.DailyAverage)
	}
	if ins.MaxDay != 3600 {
		t.Fatalf("max day = %d, want 3600", ins.MaxDay)
	}
}

// No play at all is a normal state, not a special case the renderer has to
// guard: zeroes out of every field, and the best-day label says nothing yet.
func TestWrappedInsightsEmptySnapshot(t *testing.T) {
	ins := buildWrappedInsights(newPlaytimeData(), time.Now())
	if ins.Total != 0 || ins.ActiveDays != 0 || ins.CurrentStreak != 0 ||
		ins.LongestStreak != 0 || ins.DailyAverage != 0 || ins.MaxDay != 0 {
		t.Fatalf("empty snapshot produced %+v", ins)
	}
	if got := wrappedBestDayLabel(ins); got != "—" {
		t.Fatalf("empty best-day label = %q", got)
	}
	if got := wrappedDaysLabel(1); got != "1 day" {
		t.Fatalf("singular label = %q", got)
	}
}

// A corrupted snapshot with far more consecutive days than the window must
// still terminate, and must not report a streak longer than the guard allows.
func TestWrappedStreakBoundedOnCorruptSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.Local)
	data := newPlaytimeData()
	for i := 0; i < 600; i++ {
		data.Days[now.AddDate(0, 0, -i).Format(wrappedDayKey)] = 60
	}
	got := buildWrappedInsights(data, now).CurrentStreak
	if got <= 0 || got > wrappedDays+367 {
		t.Fatalf("streak on a corrupt snapshot = %d, want it bounded", got)
	}
}

// The ramp has to read as quantity: an empty day is the track, and each level
// is at least as bright as the one below it.
func TestWrappedHeatColorRamp(t *testing.T) {
	track := wrappedHeatColor(0, 3600)
	border := WithAlpha(ColorBorder, 60)
	if track != border {
		t.Fatalf("empty day = %+v, want the track colour %+v", track, border)
	}
	max := int64(3600)
	prev := uint8(0)
	for _, seconds := range []int64{1, 900, 1800, 2700} {
		got := wrappedHeatColor(seconds, max)
		if got.A <= prev {
			t.Fatalf("%ds alpha = %d, want more than %d", seconds, got.A, prev)
		}
		prev = got.A
	}
}
