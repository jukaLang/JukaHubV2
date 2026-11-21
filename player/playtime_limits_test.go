package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPlaytimeLimitMinutesAcceptsOnlyKnownSteps(t *testing.T) {
	c := &Config{Variables: Variables{Custom: map[string]interface{}{
		playtimeDailyLimitKey: "60",
		playtimeBreakLimitKey: "50",
	}}}
	if got := playtimeDailyLimitMinutes(c); got != 60 {
		t.Errorf("daily limit = %d, want 60", got)
	}
	if got := playtimeBreakLimitMinutes(c); got != 0 {
		t.Errorf("break limit = %d, want 0 for an unknown step", got)
	}
	c.Variables.Custom[playtimeBreakLimitKey] = "30"
	if got := playtimeBreakLimitMinutes(c); got != 30 {
		t.Errorf("break limit = %d, want 30", got)
	}
}

func TestPlaytimeDailyLimitNeedsASecondPress(t *testing.T) {
	oldApp, oldPlaytime, oldLimits := appConfig, playtime, playtimeLimits
	defer func() {
		appConfig, playtime, playtimeLimits = oldApp, oldPlaytime, oldLimits
	}()
	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{
		playtimeDailyLimitKey: "60",
	}}}
	playtime = playtimeState{data: newPlaytimeData()}
	playtime.data.GameDays[playtimeTodayKey()] = 60 * 60
	playtimeLimits = playtimeLimitState{}

	if err := playtimeAllowGameLaunch("A game"); err == nil {
		t.Fatal("a game over the daily limit must ask first")
	}
	if err := playtimeAllowGameLaunch("A game"); err != nil {
		t.Fatalf("the second press must confirm the launch: %v", err)
	}
	if err := playtimeAllowGameLaunch("A game"); err == nil {
		t.Fatal("the confirmation must be single-use")
	}
}

func TestPlaytimeLimitLabel(t *testing.T) {
	if got := playtimeDurationLabel(90); got != "1½ hours" {
		t.Errorf("90 minutes reads %q", got)
	}
}

// Play time by game and day (playtime_periods.go) moves with network time the
// way the day totals do (playtimeReanchorDays).
func TestPlaytimePeriodsReanchorMovesWhatTheRunMisdated(t *testing.T) {
	days := map[string]map[string]int64{
		"2026-01-01": {"GBA|A": 600, "GBA|B": 300, "SNES|Old": 50},
		"2026-11-12": {"GBA|A": 30},
	}
	run := map[string]playtimePeriodsRunEntry{"GBA|A": {secs: 600, seq: 1}, "GBA|B": {secs: 300, seq: 2}}
	if !playtimePeriodsReanchor(days, "2026-01-01", run, 300*24*time.Hour, "2026-11-09") {
		t.Fatal("a stale power-on date must move this run's games to today")
	}
	if got := days["2026-11-09"]; got["GBA|A"] != 630 || got["GBA|B"] != 300 {
		t.Fatalf("today holds %v, want A 630 (with the day ahead folded in) and B 300", got)
	}
	if got := days["2026-01-01"]; len(got) != 1 || got["SNES|Old"] != 50 {
		t.Fatalf("what an earlier run recorded must stay: %v", got)
	}
	if _, ok := days["2026-11-12"]; ok {
		t.Fatal("a day after today must be folded into today")
	}

	// Three seconds of correction just after midnight re-date three seconds,
	// the newest credit first.
	days = map[string]map[string]int64{"2026-11-08": {"GBA|A": 1200, "GBA|B": 600}}
	run = map[string]playtimePeriodsRunEntry{"GBA|A": {secs: 1200, seq: 1}, "GBA|B": {secs: 600, seq: 2}}
	playtimePeriodsReanchor(days, "2026-11-08", run, 3*time.Second, "2026-11-09")
	if days["2026-11-09"]["GBA|B"] != 3 || days["2026-11-08"]["GBA|B"] != 597 || days["2026-11-08"]["GBA|A"] != 1200 {
		t.Fatalf("a 3 s correction moved %v", days)
	}
	if playtimePeriodsReanchor(days, "2026-11-09", run, time.Hour, "2026-11-09") {
		t.Fatal("a jump that left the date alone must move nothing")
	}
}

// The store saves through AtomicWrite, prunes past its window, reads back
// after a reload and falls back to the .bak when the file is damaged.
func TestPlaytimePeriodsPersistReloadAndPrune(t *testing.T) {
	oldPath, oldStore, oldPlaytime := playtimePeriodsTestPath, playtimePeriods, playtime
	t.Cleanup(func() { playtimePeriodsTestPath, playtimePeriods, playtime = oldPath, oldStore, oldPlaytime })
	path := filepath.Join(t.TempDir(), playtimePeriodsFileName)
	playtimePeriodsTestPath = path
	playtimePeriods.loaded = false
	now := time.Now()
	today := now.Format(wrappedDayKey)
	old := now.AddDate(0, 0, -(playtimePeriodsKeepDays + 10)).Format(wrappedDayKey)

	playtimePeriodsCredit("Game Boy|Tetris", today, 90)
	playtimePeriodsCredit("Game Boy|Tetris", today, 30)
	playtimePeriods.data.Days[old] = map[string]int64{"NES|Zelda": 500}
	playtimePeriodsPersist(true)
	playtimePeriodsReload()
	if got := playtimePeriodTotals(playtimePeriodToday, now)["Game Boy|Tetris"]; got != 120 {
		t.Fatalf("today's Tetris after a reload = %d, want 120", got)
	}
	if _, ok := playtimePeriods.data.Days[old]; ok {
		t.Fatal("a day past the window must be pruned when saved")
	}

	playtimePeriodsMoveKey("Game Boy|Tetris", "Game Boy Color|Tetris")
	playtimePeriodsPersist(true)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	playtimePeriodsReload()
	if got := playtimePeriodTotals(playtimePeriodToday, now); got["Game Boy|Tetris"] != 120 || got["Game Boy Color|Tetris"] != 0 {
		t.Fatalf("a damaged file must read back its backup (the copy before the move): %v", got)
	}
}
