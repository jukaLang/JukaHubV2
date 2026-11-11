package main

// Tests for the second trophy set: its shape (401 trophies, unique ids and
// titles, every old id kept), the event counters and their drain, the lifetime
// days and runs, the clock and the calendar, availability gates, the burst
// announcement and the room's category list and progress lines.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// trophyFirstSetIDs are the ids of the first 101 trophies. An id is what an
// unlock is saved under, so none of them may ever change or go.
var trophyFirstSetIDs = []string{
	"first_steps", "one_hour", "five_hours", "ten_hours", "twentyfive_hours", "fifty_hours", "hundred_hours",
	"two_hundred_hours", "five_hundred_hours", "thousand_hours", "three_days", "seven_days", "fortnight",
	"thirty_days", "sixty_days", "ninety_days", "streak_three", "streak_week", "streak_fortnight",
	"streak_month", "streak_two_months", "best_day_30m", "best_day_1h", "best_day_2h", "best_day_4h", "week_5h",
	"week_10h", "week_20h", "explorer", "explorer_15", "explorer_25", "explorer_35", "explorer_50", "gamer",
	"games_1h", "games_5h", "games_25h", "games_100h", "audiophile", "music_1h", "music_10h", "music_50h",
	"tube_10m", "tube_1h", "tube_10h", "tube_50h", "shorts_10m", "shorts_2h", "shorts_10h", "browser_10m",
	"browser_1h", "browser_5h", "files_5m", "files_1h", "packages_5m", "patch_10m", "settings_10m",
	"settings_gen_10m", "appearance_10m", "themes_5m", "chat_10m", "chat_2h", "notes_10m", "notes_2h",
	"terminal_5m", "terminal_2h", "calc_5m", "unit_5m", "dictionary_10m", "pdf_10m", "pdf_2h", "livetv_10m",
	"podcasts_30m", "recordings_10m", "weather_5m", "hardware_5m", "benchmark_5m", "diskspace_5m",
	"emulator_10m", "curation_10m", "ports_10m", "saves_10m", "pomodoro_30m", "alarm_5m", "favorites_10m",
	"apps_10m", "misc_10m", "jukaland_10m", "dashboard_10m", "about_2m", "badges_5", "badges_10", "badges_20",
	"badges_30", "badges_40", "badges_50", "badges_60", "badges_70", "badges_80", "badges_90", "platinum",
}

// keepTrophyEvents starts a test with nothing pending and leaves nothing behind.
func keepTrophyEvents(t *testing.T) {
	t.Helper()
	trophyEventsDiscard()
	t.Cleanup(trophyEventsDiscard)
}

// keepTrophyCelebrations saves the celebration queue and the notification
// centre, which an unlock pass writes, and restores them after the test.
func keepTrophyCelebrations(t *testing.T) {
	t.Helper()
	notifTestKeepState(t)
	trophyQueue.mu.Lock()
	items := append([]trophyToast(nil), trophyQueue.items...)
	folded, foldTier := trophyQueue.folded, trophyQueue.foldTier
	trophyQueue.items, trophyQueue.folded, trophyQueue.foldTier = nil, 0, trophyBronze
	trophyQueue.mu.Unlock()
	t.Cleanup(func() {
		trophyQueue.mu.Lock()
		trophyQueue.items, trophyQueue.folded, trophyQueue.foldTier = items, folded, foldTier
		trophyQueue.mu.Unlock()
	})
}

// keepTrophyLifeNoted clears the day the lifetime record last counted in this
// run (trophyNoteDay writes it) and puts it back after the test.
func keepTrophyLifeNoted(t *testing.T) {
	t.Helper()
	prev := trophyLifeNoted
	trophyLifeNoted = ""
	t.Cleanup(func() { trophyLifeNoted = prev })
}

// trophyTestDay is a day key n days from base.
func trophyTestDay(base time.Time, n int) string {
	return base.AddDate(0, 0, n).Format(wrappedDayKey)
}

func TestTrophySetShape(t *testing.T) {
	if len(playtimeAchievements) != playtimeAchievementCount || playtimeAchievementCount != 401 {
		t.Fatalf("the set has %d trophies, declared %d, want 401", len(playtimeAchievements), playtimeAchievementCount)
	}
	ids := map[string]int{}
	titles := map[string]string{}
	units := map[string]bool{"": true, "time": true, "MB": true}
	lastCounted, firstCollection := -1, -1
	for i := range playtimeAchievements {
		a := &playtimeAchievements[i]
		if prev, dup := ids[a.ID]; dup {
			t.Errorf("id %q is used twice (#%d and #%d)", a.ID, prev+1, i+1)
		}
		ids[a.ID] = i
		key := strings.ToLower(a.Title)
		if prev, dup := titles[key]; dup {
			t.Errorf("title %q is used by %s and %s", a.Title, prev, a.ID)
		}
		titles[key] = a.ID
		if a.Title == "" || a.Desc == "" || a.Group == "" {
			t.Errorf("%s has an empty title, description or group", a.ID)
		}
		if a.Metric != trophyMetricPlatinum && a.Goal <= 0 {
			t.Errorf("%s has goal %d", a.ID, a.Goal)
		}
		switch a.Metric {
		case trophyMetricCount, trophyMetricBest, trophyMetricSet:
			if a.Key == "" {
				t.Errorf("%s counts no key", a.ID)
			}
			if !units[a.Unit] && (strings.TrimSpace(a.Unit) != a.Unit || strings.Contains(a.Unit, "  ")) {
				t.Errorf("%s has a malformed unit %q", a.ID, a.Unit)
			}
		}
		if a.Icon == "" {
			t.Errorf("%s has no number", a.ID)
		}
		switch a.Metric {
		case trophyMetricTrophies:
			if firstCollection < 0 {
				firstCollection = i
			}
		case trophyMetricPlatinum:
			if i != len(playtimeAchievements)-1 {
				t.Errorf("the platinum is #%d, not last", i+1)
			}
		default:
			lastCounted = i
		}
	}
	if firstCollection <= lastCounted {
		t.Errorf("a collection trophy (#%d) comes before a trophy it counts (#%d)", firstCollection+1, lastCounted+1)
	}
	for _, id := range trophyFirstSetIDs {
		if _, ok := ids[id]; !ok {
			t.Errorf("the first set's %q is gone: its unlocks would be lost", id)
		}
	}
	if len(trophyFirstSetIDs) != 101 {
		t.Fatalf("the first set lists %d ids", len(trophyFirstSetIDs))
	}
	if n := len(trophySetTwo()); n != 288 {
		t.Errorf("the second set's table has %d rows, want 288 (+12 collection rungs = 300)", n)
	}
}

func TestTrophyGroupsAreContiguousAndOrdered(t *testing.T) {
	seen := map[string]bool{}
	prev := ""
	for i := range playtimeAchievements {
		g := playtimeAchievements[i].Group
		if g != prev {
			if seen[g] {
				t.Errorf("group %q appears in two places (again at #%d)", g, i+1)
			}
			seen[g] = true
			prev = g
		}
	}
	// A group the first set has, extended by the second, stays sorted by goal.
	for _, group := range []string{"Lifetime", "Days", "This week", "Explorer"} {
		last := int64(-1)
		for i := range playtimeAchievements {
			a := &playtimeAchievements[i]
			if a.Group != group {
				continue
			}
			if a.Goal < last {
				t.Errorf("%s: %s (goal %d) comes after a goal of %d", group, a.ID, a.Goal, last)
			}
			last = a.Goal
		}
	}
	// The first set's per-screen table keeps its order, the new rows follow.
	first, firstNew := trophyIndexByID("gamer"), trophyIndexByID("visit_wifi")
	if first < 0 || firstNew < 0 || firstNew < trophyIndexByID("about_2m") || first > firstNew {
		t.Errorf("Screens order: gamer #%d, about_2m #%d, visit_wifi #%d", first+1, trophyIndexByID("about_2m")+1, firstNew+1)
	}
	groups := trophyGroups()
	if len(groups) == 0 || groups[0] != "Lifetime" || groups[len(groups)-1] != "Platinum" {
		t.Fatalf("groups = %v", groups)
	}
}

func TestTrophyDaysTrophiesReadEveryDay(t *testing.T) {
	for _, id := range []string{"three_days", "seven_days", "fortnight", "thirty_days", "sixty_days", "ninety_days"} {
		i := trophyIndexByID(id)
		if i < 0 || playtimeAchievements[i].Metric != trophyMetricLifeDays {
			t.Errorf("%s does not count every day ever", id)
		}
	}
}

func TestTrophyEventsDrain(t *testing.T) {
	keepTrophyEvents(t)
	const count, best, set = trophyKey("test_count"), trophyKey("test_best"), trophyKey("test_set")
	d := newPlaytimeData()
	if trophyDrainEvents(&d) {
		t.Fatal("an empty record changed the snapshot")
	}
	trophyEventN(count, 2)
	trophyEvent(count)
	trophyEventN(count, 0)  // ignored
	trophyEventN(count, -4) // ignored
	trophyEventPeak(best, 5)
	trophyEventPeak(best, 3)
	trophyEventDistinct(set, "Foo")
	trophyEventDistinct(set, "  foo ")
	trophyEventDistinct(set, "bar")
	trophyEventDistinct(set, "   ")
	if !trophyDrainEvents(&d) {
		t.Fatal("the drain reported no change")
	}
	if got := trophyCountOf(d, count); got != 3 {
		t.Errorf("count = %d, want 3", got)
	}
	if got := trophyCountOf(d, best); got != 5 {
		t.Errorf("best = %d, want 5", got)
	}
	if got := trophySetLen(d, set); got != 2 {
		t.Errorf("set holds %d values, want 2 (case and space do not make a value new)", got)
	}
	// A second drain finds nothing of this test's (another test's goroutine
	// may report its own keys meanwhile, so the keys are read, not the bool).
	trophyDrainEvents(&d)
	if trophyCountOf(d, count) != 3 || trophyCountOf(d, best) != 5 || trophySetLen(d, set) != 2 {
		t.Error("a second drain changed this test's keys")
	}
	// A best below the one kept changes nothing.
	trophyEventPeak(best, 4)
	trophyDrainEvents(&d)
	if trophyCountOf(d, best) != 5 {
		t.Errorf("a lower best changed the snapshot (best %d)", trophyCountOf(d, best))
	}
	// Events may come from any goroutine.
	done := make(chan struct{})
	for g := 0; g < 4; g++ {
		go func() {
			for i := 0; i < 100; i++ {
				trophyEvent(count)
			}
			done <- struct{}{}
		}()
	}
	for g := 0; g < 4; g++ {
		<-done
	}
	trophyDrainEvents(&d)
	if got := trophyCountOf(d, count); got != 403 {
		t.Errorf("count after 400 concurrent events = %d, want 403", got)
	}
}

func TestTrophySetAddKeepsSortedAndCapped(t *testing.T) {
	d := newPlaytimeData()
	const key = trophyKey("test_cap")
	for i := uint32(0); i < trophySetCap+50; i++ {
		// Odd multiplier: distinct values in a scrambled order.
		trophySetAdd(&d, key, i*2654435761)
	}
	set := d.Sets[string(key)]
	if len(set) != trophySetCap {
		t.Fatalf("set holds %d values, want the cap %d", len(set), trophySetCap)
	}
	for i := 1; i < len(set); i++ {
		if set[i-1] >= set[i] {
			t.Fatalf("set is not sorted and unique at %d: %d, %d", i, set[i-1], set[i])
		}
	}
	if trophySetAdd(&d, key, set[3]) {
		t.Error("a value already in the set was added again")
	}
	if _, ok := trophyHash(""); ok {
		t.Error("an empty value hashed")
	}
	ha, _ := trophyHash("Super Mario")
	hb, _ := trophyHash(" super mario ")
	if ha != hb {
		t.Error("the hash depends on case or surrounding space")
	}
}

func TestTrophyLifeSeedAndNewDays(t *testing.T) {
	keepTrophyLifeNoted(t)
	now := time.Date(2026, 11, 10, 15, 0, 0, 0, time.Local)
	d := newPlaytimeData()
	for _, n := range []int{0, -1, -2, -5, -10} {
		d.Days[trophyTestDay(now, n)] = 600
	}
	// A day with no time is not a day used, and a day after today is a clock
	// that ran ahead.
	d.Days[trophyTestDay(now, -20)] = 0
	d.Days[trophyTestDay(now, 3)] = 600
	trophyLifeSeed(&d, now)
	if d.Life.Days != 5 || d.Life.BestStreak != 3 || d.Life.Streak != 3 {
		t.Fatalf("seeded life = %+v, want 5 days, best 3, running 3", d.Life)
	}
	if d.Life.First != trophyTestDay(now, -10) || d.Life.Last != trophyTestDay(now, 0) {
		t.Fatalf("seeded first/last = %q/%q", d.Life.First, d.Life.Last)
	}
	// Seeding again changes nothing.
	before := d.Life
	trophyLifeSeed(&d, now)
	if d.Life != before {
		t.Fatalf("a second seed moved the record: %+v → %+v", before, d.Life)
	}
	trophyNoteDay(&d, trophyTestDay(now, 0)) // the same day
	if d.Life != before {
		t.Fatalf("the same day counted again: %+v", d.Life)
	}
	trophyNoteDay(&d, trophyTestDay(now, 1)) // the next day: the run goes on
	if d.Life.Days != 6 || d.Life.Streak != 4 || d.Life.BestStreak != 4 {
		t.Fatalf("after the next day: %+v", d.Life)
	}
	trophyNoteDay(&d, trophyTestDay(now, 3)) // a day skipped: a new run
	if d.Life.Days != 7 || d.Life.Streak != 1 || d.Life.BestStreak != 4 {
		t.Fatalf("after a gap: %+v", d.Life)
	}
	trophyNoteDay(&d, trophyTestDay(now, 2)) // a clock that went back
	if d.Life.Days != 7 || d.Life.Last != trophyTestDay(now, 3) {
		t.Fatalf("an earlier day was counted: %+v", d.Life)
	}
	// The run across a month and a year end.
	e := newPlaytimeData()
	trophyNoteDay(&e, "2026-12-31")
	trophyNoteDay(&e, "2027-01-01")
	if e.Life.Streak != 2 {
		t.Fatalf("31 Dec → 1 Jan broke the run: %+v", e.Life)
	}
}

func TestTrophyLifeIgnoresUnsetClocksAndFollowsJumps(t *testing.T) {
	keepTrophyLifeNoted(t)
	// A handheld that boots at its RTC's default before network time arrives.
	d := newPlaytimeData()
	trophyNoteDay(&d, "2000-01-01")
	if d.Life.Days != 0 || d.Life.First != "" || trophyCountOf(d, tkDayNewYear) != 0 {
		t.Fatalf("an unset clock's date counted: %+v %v", d.Life, d.Counts)
	}
	if !trophyClockSane(time.Now()) || trophyClockSane(time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("the clock floor reads the build's date wrong")
	}
	// A clock on a plausible but wrong date, then network time well over a
	// year on: the day counted under the old date was today.
	trophyNoteDay(&d, "2025-06-01")
	if !trophyLifeReanchor(&d, "2025-06-01", "2026-11-10", 527*24*time.Hour) {
		t.Fatal("a misdated first day was not moved")
	}
	if d.Life.First != "2026-11-10" || d.Life.Last != "2026-11-10" || d.Life.Days != 1 {
		t.Fatalf("after the jump: %+v", d.Life)
	}
	trophyNoteDay(&d, "2026-11-10") // today is not counted a second time
	if d.Life.Days != 1 {
		t.Fatalf("today counted twice: %+v", d.Life)
	}
	if keys := trophyCalendarKeys(time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), d.Life.First); len(keys) != 0 {
		t.Fatalf("the moved first day still made an anniversary: %v", keys)
	}
	// A correction of minutes across midnight moves nothing.
	e := newPlaytimeData()
	trophyNoteDay(&e, "2026-11-10")
	if trophyLifeReanchor(&e, "2026-11-10", "2026-11-11", 2*time.Minute) {
		t.Fatal("a two-minute correction re-dated a day")
	}
	// A clock a couple of hours ahead across midnight, then set right: the
	// day it counted was today, however small the jump.
	h := newPlaytimeData()
	trophyNoteDay(&h, "2026-11-11")
	if !trophyLifeReanchor(&h, "2026-11-11", "2026-11-10", -2*time.Hour) || h.Life.Last != "2026-11-10" {
		t.Fatalf("a day from a clock hours ahead stayed: %+v", h.Life)
	}
	// A clock that ran ahead: the future day becomes today.
	f := newPlaytimeData()
	trophyNoteDay(&f, "2027-03-01")
	if !trophyLifeReanchor(&f, "2027-03-01", "2026-11-10", -111*24*time.Hour) || f.Life.Last != "2026-11-10" {
		t.Fatalf("a day from a clock that ran ahead stayed: %+v", f.Life)
	}
	// A day counted before this run is not the old clock's to move.
	g := newPlaytimeData()
	g.Life = playtimeLife{Days: 4, First: "2026-11-01", Last: "2026-11-09", Streak: 2, BestStreak: 2}
	trophyLifeNoted = ""
	if trophyLifeReanchor(&g, "2026-11-09", "2026-11-11", 48*time.Hour) {
		t.Fatalf("a day from an earlier run was moved: %+v", g.Life)
	}
}

// TestTrophyKeysAreReportedAndRead holds every key of trophy_events.go to its
// two ends: something reports it (a feature's trophyEvent call, the tracker,
// or the calendar) and some trophy reads it. A key with no reporter is a
// trophy nobody can earn.
func TestTrophyKeysAreReportedAndRead(t *testing.T) {
	src, err := os.ReadFile("trophy_events.go")
	if err != nil {
		t.Skipf("no source to read: %v", err)
	}
	decl := regexp.MustCompile(`(?m)^\t(tk[A-Za-z0-9]+)\s+trophyKey\s*=\s*"([^"]+)"`).FindAllStringSubmatch(string(src), -1)
	if len(decl) < 100 {
		t.Fatalf("found only %d keys in trophy_events.go", len(decl))
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	// One pass over the sources: every key that follows a reporting call.
	reporter := regexp.MustCompile(`(?:trophyEvent(?:N|Peak|Distinct)?\(\s*|trophyCount(?:Add|Peak)\((?:d|&playtime\.data), |trophySetAddValue\(d, |append\(keys, )(tk[A-Za-z0-9]+)\b`)
	reported := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "trophy_events.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range reporter.FindAllSubmatch(b, -1) {
			reported[string(m[1])] = true
		}
	}
	read := map[trophyKey]bool{}
	for i := range playtimeAchievements {
		if k := playtimeAchievements[i].Key; k != "" {
			read[k] = true
		}
	}
	for _, m := range decl {
		name, value := m[1], m[2]
		if !reported[name] {
			t.Errorf("%s (%q) is never reported", name, value)
		}
		if !read[trophyKey(value)] {
			t.Errorf("%s (%q) is read by no trophy", name, value)
		}
	}
}

func TestTrophyCalendarKeys(t *testing.T) {
	has := func(keys []trophyKey, want trophyKey) bool {
		for _, k := range keys {
			if k == want {
				return true
			}
		}
		return false
	}
	date := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		at   time.Time
		want trophyKey
	}{
		{date(2026, 11, 13), tkDayFriday13},
		{date(2028, 2, 29), tkDayLeap},
		{date(2026, 9, 13), tkDayProgrammers}, // day 256 of a common year
		{date(2028, 9, 12), tkDayProgrammers}, // and of a leap year
		{date(2026, 10, 31), tkDayHalloween},
		{date(2026, 12, 25), tkDayChristmas},
		{date(2027, 1, 1), tkDayNewYear},
		{date(2026, 12, 31), tkDayNewYearsEve},
		{date(2027, 5, 4), tkDayMayFourth},
		{date(2027, 3, 14), tkDayPi},
	}
	for _, c := range cases {
		if keys := trophyCalendarKeys(c.at, ""); !has(keys, c.want) {
			t.Errorf("%s gave %v, want %s", c.at.Format(wrappedDayKey), keys, c.want)
		}
	}
	if keys := trophyCalendarKeys(date(2026, 11, 11), ""); len(keys) != 0 {
		t.Errorf("an ordinary day gave %v", keys)
	}
	if keys := trophyCalendarKeys(date(2026, 11, 13), "2025-11-14"); has(keys, tkDayAnniversary) {
		t.Error("the anniversary came a day early")
	}
	if keys := trophyCalendarKeys(date(2026, 11, 14), "2025-11-14"); !has(keys, tkDayAnniversary) {
		t.Error("no anniversary a year after the first day")
	}
	// Noting a day sets its flag once and adds its day of the week.
	keepTrophyLifeNoted(t)
	d := newPlaytimeData()
	trophyNoteDay(&d, "2026-10-31")
	if trophyCountOf(d, tkDayHalloween) != 1 || trophySetLen(d, tkWeekdays) != 1 {
		t.Fatalf("after Halloween: counts %v, weekdays %d", d.Counts, trophySetLen(d, tkWeekdays))
	}
}

func TestTrophyClockBands(t *testing.T) {
	d := newPlaytimeData()
	// Tuesday 10 November 2026, 03:30.
	trophyNoteClock(&d, time.Date(2026, 11, 10, 3, 30, 0, 0, time.UTC), 60)
	if trophyCountOf(d, tkClockNight) != 60 || trophyCountOf(d, tkClockThreeAM) != 60 {
		t.Fatalf("3:30 am: %v", d.Counts)
	}
	if trophyCountOf(d, tkClockWeekend) != 0 || trophyCountOf(d, tkClockMonday) != 0 {
		t.Fatalf("a Tuesday counted as a weekend or a Monday: %v", d.Counts)
	}
	// 90 seconds from 04:59:30 on a Saturday: 30 at night, 60 early.
	e := newPlaytimeData()
	trophyNoteClockSpan(&e, time.Date(2026, 11, 14, 4, 59, 30, 0, time.UTC), 90)
	if trophyCountOf(e, tkClockNight) != 30 || trophyCountOf(e, tkClockEarly) != 60 || trophyCountOf(e, tkClockWeekend) != 90 {
		t.Fatalf("span across 5 am: %v", e.Counts)
	}
	// A long session splits at every hour and loses nothing.
	f := newPlaytimeData()
	trophyNoteClockSpan(&f, time.Date(2026, 11, 9, 18, 20, 0, 0, time.UTC), 5*3600)
	if got := trophyCountOf(f, tkClockEvening) + trophyCountOf(f, tkClockLunch) + trophyCountOf(f, tkClockNight) +
		trophyCountOf(f, tkClockEarly); got != 4*3600 {
		// 18:20–23:20: 19:00–23:00 is the evening band, 4 hours.
		t.Fatalf("evening band got %d s of a 5-hour session from 18:20", got)
	}
	if trophyCountOf(f, tkClockMonday) != 5*3600 {
		t.Fatalf("a Monday session counted %d s on Mondays", trophyCountOf(f, tkClockMonday))
	}
}

func TestPlaytimeCrossedMidnight(t *testing.T) {
	last := time.Date(2026, 11, 10, 23, 59, 59, 0, time.UTC)
	if !playtimeCrossedMidnight("2026-11-10", "2026-11-11", last, last.Add(2*time.Second)) {
		t.Error("two seconds across midnight were not a crossing")
	}
	if playtimeCrossedMidnight("2026-11-10", "2026-11-11", last, last.Add(time.Hour)) {
		t.Error("an hour away counted as staying up")
	}
	if playtimeCrossedMidnight("2026-11-10", "2026-11-10", last, last.Add(time.Second)) {
		t.Error("the same day counted")
	}
	if playtimeCrossedMidnight("", "2026-11-11", last, last.Add(time.Second)) {
		t.Error("the first record of a run counted")
	}
	if playtimeCrossedMidnight("2026-11-11", "2026-11-10", last, last.Add(time.Second)) {
		t.Error("a clock that went back counted")
	}
}

func TestTrophySeedFromHistory(t *testing.T) {
	keepTrophyState(t)
	keepTrophyEvents(t)
	now := time.Now()
	old := map[string]interface{}{
		"total":    float64(7200),
		"scenes":   map[string]interface{}{"Main": float64(7200)},
		"unlocked": map[string]interface{}{},
		"days": map[string]interface{}{
			trophyTestDay(now, 0): float64(1000), trophyTestDay(now, -2): float64(1000),
			trophyTestDay(now, -4): float64(1000), trophyTestDay(now, -6): float64(1000),
			trophyTestDay(now, -8): float64(1000),
		},
		"games": map[string]interface{}{"Super Mario World": float64(3000), "Tetris": float64(1200)},
	}
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{playtimeCustomKey: old}
	playtimeInit(config)
	d := playtime.data
	if d.Life.Days != 5 {
		t.Fatalf("an old save's 5 days became %d lifetime days", d.Life.Days)
	}
	if trophyCountOf(d, tkGameSecs) != 4200 || trophySetLen(d, tkGameTitles) != 2 {
		t.Fatalf("game time %d, titles %d; want 4200 and 2", trophyCountOf(d, tkGameSecs), trophySetLen(d, tkGameTitles))
	}
	i := trophyIndexByID("five_days")
	if i < 0 || !playtimeAchievements[i].Check(d) {
		t.Fatal("five days in the window do not earn \"Use JukaHub on 5 days\"")
	}
	// The game time is seeded once: a second start does not add the ledger again.
	config.Variables.Custom[playtimeCustomKey] = d
	playtimeInit(config)
	if got := trophyCountOf(playtime.data, tkGameSecs); got != 4200 {
		t.Fatalf("a second start made the game time %d", got)
	}
}

func TestTrophyCountTrophiesUnlockFromEvents(t *testing.T) {
	keepTrophyState(t)
	keepTrophyEvents(t)
	keepTrophyCelebrations(t)
	playtime = playtimeState{loaded: true, data: newPlaytimeData(), rev: playtime.rev + 1}
	trophyEventN(tkScreenshot, 10)
	if !trophyDrainEvents(&playtime.data) {
		t.Fatal("the events did not reach the snapshot")
	}
	playtimeCheckAchievements()
	for _, id := range []string{"screenshot_1", "screenshot_10"} {
		if _, ok := playtime.data.Unlocked[id]; !ok {
			t.Errorf("%s was not earned by 10 screenshots", id)
		}
	}
	if _, ok := playtime.data.Unlocked["screenshot_100"]; ok {
		t.Error("screenshot_100 was earned by 10 screenshots")
	}
}

func TestTrophyAvailabilityGates(t *testing.T) {
	keepTrophyState(t)
	config := &Config{Scenes: []SceneConfig{{Name: "Main"}, {Name: "Notes"}}}
	ok := trophyComputeAvailability(config, nil)
	avail := func(id string) bool {
		i := trophyIndexByID(id)
		if i < 0 {
			t.Fatalf("no trophy %q", id)
		}
		return ok[i]
	}
	if !avail("note_1") {
		t.Error("a count trophy whose screen is in the config is not on offer")
	}
	if avail("term_1") {
		t.Error("a count trophy whose screen is missing is on offer")
	}
	if !avail("screenshot_1") {
		t.Error("a trophy with no screen or gate is not on offer")
	}
	if avail("portmaster_1") != !IsWindows() {
		t.Errorf("the handheld gate offers PortMaster on Windows=%v", IsWindows())
	}
	if !trophyGateOpen("") || !trophyGateOpen("no such gate") {
		t.Error("an empty or unknown gate is closed")
	}
	if trophyGateOpen("windows") != IsWindows() || trophyGateOpen("handheld") == IsWindows() {
		t.Error("the platform gates disagree with IsWindows")
	}
}

func TestTrophyAnnounceBatch(t *testing.T) {
	keepTrophyCelebrations(t)
	rows := func() int {
		notifHistoryMu.Lock()
		defer notifHistoryMu.Unlock()
		return len(nc.history)
	}
	pick := func(n int) []playtimeAchievement {
		out := make([]playtimeAchievement, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, playtimeAchievements[i])
		}
		return out
	}
	trophyAnnounceBatch(pick(2))
	if got := rows(); got != 2 {
		t.Fatalf("two trophies filed %d rows, want one each", got)
	}
	trophyAnnounceBatch(pick(7))
	if got := rows(); got != 3 {
		t.Fatalf("a burst of seven filed %d more rows, want one", got-2)
	}
	notifHistoryMu.Lock()
	top := nc.history[0].Message
	notifHistoryMu.Unlock()
	if !strings.HasPrefix(top, "7 trophies earned: ") || !strings.HasSuffix(top, " and 4 more") {
		t.Fatalf("burst row = %q", top)
	}
	plat := playtimeAchievements[len(playtimeAchievements)-1]
	trophyAnnounceBatch(append(pick(4), plat))
	if got := rows(); got != 5 {
		t.Fatalf("a burst with the platinum filed %d more rows, want two", got-3)
	}
	if msg := trophyBurstNotice(pick(3)); !strings.HasSuffix(msg, " and "+playtimeAchievements[2].Title) {
		t.Fatalf("three named = %q", msg)
	}
}

func TestTrophyProgressLines(t *testing.T) {
	cases := []struct{ got, want string }{
		{trophyNum(0), "0"},
		{trophyNum(999), "999"},
		{trophyNum(1000), "1,000"},
		{trophyNum(1234567), "1,234,567"},
		{trophyNum(-1234), "-1,234"},
		{trophySizeText(340), "340 MB"},
		{trophySizeText(1024), "1 GB"},
		{trophySizeText(1536), "1.5 GB"},
		{trophySizeText(102400), "100 GB"},
		{trophyAmountText("time", 3600, 7200), "1h of 2h"},
		{trophyAmountText("MB", 340, 1024), "340 MB of 1 GB"},
		{trophyAmountText("tracks", 37, 1000), "37 of 1,000 tracks"},
		{trophyAmountText("", 128, 256), "128 of 256"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
	a := playtimeAchievements[trophyIndexByID("snake_100")]
	if got := trophyProgressText(&a, 40, 100); got != "Best 40 of 100 points" {
		t.Errorf("a best's line = %q", got)
	}
	b := playtimeAchievements[trophyIndexByID("launch_1")]
	if got := trophyProgressText(&b, 0, 1); got != "Not yet" {
		t.Errorf("a first-time trophy's line = %q", got)
	}
}

func TestTrophyGroupOptions(t *testing.T) {
	keepTrophyState(t)
	trophyAvail.known = false
	d := newPlaytimeData()
	d.Unlocked["first_steps"] = "2026-11-10T12:00:00Z"
	opts := trophyGroupOptions(d)
	if len(opts) < 2 || opts[0].Value != "" || opts[0].Label != "All categories" {
		t.Fatalf("options start %+v", opts[:1])
	}
	if opts[0].Note != "1 of 401 earned" {
		t.Errorf("all categories note = %q", opts[0].Note)
	}
	if opts[1].Value != "Lifetime" || !strings.HasPrefix(opts[1].Note, "1 of ") {
		t.Errorf("first category = %+v", opts[1])
	}
}

func TestTrophyBonusIsNotNeededForThePlatinum(t *testing.T) {
	keepTrophyState(t)
	trophyAvail.known = false
	d := newPlaytimeData()
	bonus := 0
	for i := range playtimeAchievements {
		a := &playtimeAchievements[i]
		if a.Bonus {
			bonus++
			continue
		}
		if a.Metric != trophyMetricPlatinum {
			d.Unlocked[a.ID] = "2026-11-10T12:00:00Z"
		}
	}
	if bonus == 0 {
		t.Fatal("no bonus trophies in the set")
	}
	got, need := trophyPlatinumProgress(d)
	if need == 0 || got != need {
		t.Fatalf("with every non-bonus trophy earned the platinum is at %d of %d", got, need)
	}
	if !playtimeAchievements[len(playtimeAchievements)-1].Check(d) {
		t.Fatal("the platinum waits for a bonus trophy")
	}
}

func TestPlaytimeSittingEndsAfterABreak(t *testing.T) {
	keepTrophyState(t)
	playtime = playtimeState{loaded: true, data: newPlaytimeData(), rev: playtime.rev + 1}
	now := time.Now()
	playtime.lastRecordAt = now.Add(-time.Minute)
	playtime.session = 3000
	playtimeNoteSitting(now, 1)
	if playtime.session != 3001 {
		t.Fatalf("a minute's pause ended the sitting: %d", playtime.session)
	}
	playtime.lastRecordAt = now.Add(-(playtimeSessionBreak + time.Second))
	playtimeNoteSitting(now, 1)
	if playtime.session != 1 {
		t.Fatalf("a break did not start a new sitting: %d", playtime.session)
	}
	if trophyCountOf(playtime.data, tkSessionBest) != 3001 {
		t.Fatalf("the best sitting = %d, want 3001", trophyCountOf(playtime.data, tkSessionBest))
	}
}

func TestTrophyGroupsFollowTheReadingOrder(t *testing.T) {
	rank := map[string]int{}
	for i, g := range trophyGroupOrder {
		rank[g] = i
	}
	last := -1
	for _, g := range trophyGroups() {
		r, ok := rank[g]
		if !ok {
			continue // the collection and the platinum come after the listed ones
		}
		if r < last {
			t.Fatalf("group %q is out of order in %v", g, trophyGroups())
		}
		last = r
	}
}

func TestTrophyRungsReachableWithoutBonus(t *testing.T) {
	keepTrophyState(t)
	config := &Config{Scenes: []SceneConfig{{Name: "Main"}, {Name: "Tube"}, {Name: "Settings"}}}
	ok := trophyComputeAvailability(config, nil)
	others := 0
	for i := range playtimeAchievements {
		a := &playtimeAchievements[i]
		if ok[i] && !a.Bonus && a.Metric != trophyMetricPlatinum {
			others++
		}
	}
	for i := range playtimeAchievements {
		a := &playtimeAchievements[i]
		if a.Metric == trophyMetricTrophies && ok[i] && a.Goal > int64(others-1) {
			t.Errorf("%s (goal %d) is on offer with only %d other non-bonus trophies", a.ID, a.Goal, others-1)
		}
	}
}
