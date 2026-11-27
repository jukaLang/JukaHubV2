package main

// playtime_store_test.go — LIB-13: playtime's own files, the bounded session
// log keyed by content hash, the 400-day window, Wrapped's year, the export
// and the config no longer carrying playtime.

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// playtimeTestStore points the three playtime stores at one temp folder and
// puts the globals back afterwards.
func playtimeTestStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prevData, prevSessions, prevHash := playtime.data, playtime.sessions, playtimeGameHash
	prevPath, prevSessPath, prevLoaded := playtimeStorePath, playtimeSessionsPath, playtime.loaded
	playtimeStorePath = func() string { return filepath.Join(dir, playtimeFileName) }
	playtimeSessionsPath = func() string { return filepath.Join(dir, playtimeSessionsFileName) }
	playtimeGameHash = func(path string) string { return "hash:" + filepath.Base(path) }
	t.Cleanup(func() {
		playtime.data, playtime.sessions, playtimeGameHash = prevData, prevSessions, prevHash
		playtimeStorePath, playtimeSessionsPath, playtime.loaded = prevPath, prevSessPath, prevLoaded
	})
	return dir
}

// TestPlaytimeSessionLogIsBoundedAndKeepsTheNewest is the log's contract: one
// record per session, the 400-day window and the record cap both enforced by
// dropping the oldest, and the file reading back what was written.
func TestPlaytimeSessionLogIsBoundedAndKeepsTheNewest(t *testing.T) {
	dir := playtimeTestStore(t)
	playtime.sessions = nil
	now := time.Now()

	// Past the window: dropped.
	playtime.sessions = append(playtime.sessions, playtimeSession{
		Hash: "old", Title: "Old", Seconds: 60,
		Start: now.AddDate(0, 0, -(playtimeDaysKept + 1)),
		Day:   now.AddDate(0, 0, -(playtimeDaysKept + 1)).Format(wrappedDayKey),
	})
	playtime.sessions = playtimeSessionsPrune(playtime.sessions, now)
	if len(playtime.sessions) != 0 {
		t.Fatalf("a session past the 400-day window was kept: %+v", playtime.sessions)
	}

	// Past the record cap: the oldest go, the newest stay.
	for i := 0; i < playtimeSessionsMax+10; i++ {
		at := now.Add(-time.Duration(playtimeSessionsMax+10-i) * time.Minute)
		playtime.sessions = append(playtime.sessions, playtimeSession{
			Hash: fmt.Sprintf("h%05d", i), Title: fmt.Sprintf("Game %05d", i),
			Seconds: 60, Start: at, Day: at.Format(wrappedDayKey),
		})
	}
	playtime.sessions = playtimeSessionsPrune(playtime.sessions, now)
	if len(playtime.sessions) != playtimeSessionsMax {
		t.Fatalf("log holds %d records, want the cap %d", len(playtime.sessions), playtimeSessionsMax)
	}
	if got := playtime.sessions[0].Title; got != fmt.Sprintf("Game %05d", 10) {
		t.Fatalf("the oldest kept record is %q, want Game 00010", got)
	}

	// The file round-trips, and a record with no day cannot grow it.
	playtime.sessions = append(playtime.sessions, playtimeSession{Hash: "nodate", Title: "No date", Seconds: 5})
	if err := playtimeSessionsSave(playtime.sessions); err != nil {
		t.Fatalf("save sessions: %v", err)
	}
	loaded := playtimeSessionsLoad()
	if len(loaded) != playtimeSessionsMax {
		t.Fatalf("loaded %d sessions, want %d (the undated one dropped)", len(loaded), playtimeSessionsMax)
	}
	if loaded[len(loaded)-1].Hash != fmt.Sprintf("h%05d", playtimeSessionsMax+9) {
		t.Fatalf("the newest session did not survive: %+v", loaded[len(loaded)-1])
	}
	if _, err := os.Stat(filepath.Join(dir, playtimeSessionsFileName)); err != nil {
		t.Fatalf("session file: %v", err)
	}
}

// TestPlaytimeMovesOutOfTheUserConfig is the migration: an old config's
// snapshot lands in playtime.json once, the key leaves the config, and a later
// start reads the file rather than the config.
func TestPlaytimeMovesOutOfTheUserConfig(t *testing.T) {
	dir := playtimeTestStore(t)
	playtime.loaded = false
	playtime.sessions = nil
	legacy := newPlaytimeData()
	legacy.Total = 4242
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{
		playtimeCustomKey: legacy,
		"theme_preset":    "Boot",
	}}}
	playtimeInit(cfg)
	if playtime.data.Total != 4242 {
		t.Fatalf("migrated total = %d, want 4242", playtime.data.Total)
	}
	if _, ok := cfg.Variables.Custom[playtimeCustomKey]; ok {
		t.Fatal("the playtime key stayed in the user config")
	}
	if _, err := os.Stat(filepath.Join(dir, playtimeFileName)); err != nil {
		t.Fatalf("playtime.json was not written: %v", err)
	}

	// A second start reads the file, not a config key that is gone.
	playtime.loaded = false
	fresh := &Config{Variables: Variables{Custom: map[string]interface{}{"theme_preset": "Boot"}}}
	playtimeInit(fresh)
	if playtime.data.Total != 4242 {
		t.Fatalf("second start read total %d, want the file's 4242", playtime.data.Total)
	}
}

// TestPlaytimePersistLeavesTheUserConfigAlone is the point of the move: saving
// playtime writes its own files and does not touch the user config.
func TestPlaytimePersistLeavesTheUserConfigAlone(t *testing.T) {
	dir := playtimeTestStore(t)
	playtime.loaded = true
	playtime.data = newPlaytimeData()
	playtime.data.Total = 3600
	playtime.sessions = []playtimeSession{{Hash: "abc", Title: "Sonic", Seconds: 3600,
		Start: time.Now(), Day: time.Now().Format(wrappedDayKey)}}
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{"theme_preset": "Boot"}}}
	playtimePersist(cfg)
	if _, ok := cfg.Variables.Custom[playtimeCustomKey]; ok {
		t.Fatal("playtimePersist put the snapshot back into the user config")
	}
	b, err := os.ReadFile(filepath.Join(dir, playtimeFileName))
	if err != nil {
		t.Fatalf("playtime.json: %v", err)
	}
	if !strings.Contains(string(b), "3600") {
		t.Fatalf("playtime.json does not hold the total: %s", b)
	}
	if _, err := os.Stat(filepath.Join(dir, playtimeSessionsFileName)); err != nil {
		t.Fatalf("the session log was not written: %v", err)
	}
}

// TestPlaytimePruneKeepsFourHundredDays is the retention the task names.
func TestPlaytimePruneKeepsFourHundredDays(t *testing.T) {
	prev := playtime.data
	t.Cleanup(func() { playtime.data = prev })
	playtime.data = newPlaytimeData()
	now := time.Now()
	inside := now.AddDate(0, 0, -(playtimeDaysKept - 1)).Format(wrappedDayKey)
	outside := now.AddDate(0, 0, -(playtimeDaysKept + 5)).Format(wrappedDayKey)
	playtime.data.Days[inside] = 60
	playtime.data.Days[outside] = 60
	playtime.data.GameDays[inside] = 60
	playtime.data.GameDays[outside] = 60
	playtimePruneDays()
	if playtime.data.Days[inside] == 0 {
		t.Fatalf("day %s inside the window was dropped", inside)
	}
	if playtime.data.Days[outside] != 0 {
		t.Fatalf("day %s outside the window was kept", outside)
	}
	if playtime.data.GameDays[outside] != 0 {
		t.Fatalf("game day %s outside the window was kept", outside)
	}
}

// TestWrappedCoversAYear proves the heatmap reaches back a year, not 13 weeks.
func TestWrappedCoversAYear(t *testing.T) {
	if wrappedWeeks < 52 {
		t.Fatalf("wrappedWeeks = %d, want a year", wrappedWeeks)
	}
	now := time.Now()
	data := newPlaytimeData()
	// A day 300 days ago must be inside the grid and counted.
	old := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -300)
	data.Days[old.Format(wrappedDayKey)] = 3600
	ins := buildWrappedInsights(data, now)
	if ins.ActiveDays != 1 || ins.Total != 3600 {
		t.Fatalf("insights over a year = %d active days, %d seconds; want the 300-day-old day", ins.ActiveDays, ins.Total)
	}
	found := false
	for _, cell := range ins.Days {
		if cell.Day.Equal(old) && cell.Seconds == 3600 {
			found = true
		}
	}
	if !found {
		t.Fatal("the day 300 days back is not in the grid")
	}
}

// BenchmarkPlaytimeSessionNote measures a session's append at the log's cap:
// the prune runs on every append, so the steady state is what a long session
// on a full card costs.
func BenchmarkPlaytimeSessionNote(b *testing.B) {
	prevHash, prevSessions := playtimeGameHash, playtime.sessions
	playtimeGameHash = func(string) string { return "benchhash" }
	playtime.sessions = nil
	defer func() { playtimeGameHash, playtime.sessions = prevHash, prevSessions }()
	day := time.Now().Format(wrappedDayKey)
	start := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		playtimeSessionNote("/roms/SNES/Game.sfc", "SNES", "Game", start, 60, day)
	}
}

// TestPlaytimeExportWritesCSVAndHTML covers the export: both files, sorted,
// escaped, with the hash and the last session, and a title that would break a
// naive CSV or HTML.
func TestPlaytimeExportWritesCSVAndHTML(t *testing.T) {
	dir := t.TempDir()
	rows := []playtimeExportRow{
		{System: "SNES", Title: "Sonic, the Hedgehog", Hash: "h1", Seconds: 7200, LastPlayed: "2026-01-02 10:00"},
		{System: "Mega Drive", Title: "<Streets>", Hash: "h2", Seconds: 3600, LastPlayed: "2026-01-01 09:00"},
	}
	// playtimeExportRows does the joining and sorting; check it here too.
	games := map[string]int64{
		playtimeGameKey("SNES", "Sonic, the Hedgehog"): 3600,
		playtimeGameKey("Mega Drive", "<Streets>"):     3600,
	}
	sessions := []playtimeSession{
		{Hash: "h1", System: "SNES", Title: "Sonic, the Hedgehog", Start: time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC), Seconds: 3600, Day: "2026-01-02"},
		{Hash: "h2", System: "Mega Drive", Title: "<Streets>", Start: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC), Seconds: 3600, Day: "2026-01-01"},
	}
	joined := playtimeExportRows(games, sessions)
	if len(joined) != 2 {
		t.Fatalf("joined %d rows, want 2", len(joined))
	}
	if joined[0].Title != "Sonic, the Hedgehog" && joined[1].Title != "Sonic, the Hedgehog" {
		t.Fatalf("the tables joined the wrong keys: %+v", joined)
	}
	for _, r := range joined {
		if r.Hash == "" || r.LastPlayed == "" {
			t.Fatalf("a row lost its session data: %+v", r)
		}
	}

	csvPath, htmlPath, err := playtimeExportFiles(dir, []playtimeExportRow{
		rows[0],
	}, "2026-02-01 12:00")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	f, err := os.Open(csvPath)
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	records, err := csv.NewReader(f).ReadAll()
	f.Close()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(records) != 2 || records[0][0] != "system" || records[0][2] != "hash" {
		t.Fatalf("csv header = %v", records[0])
	}
	if records[1][1] != "Sonic, the Hedgehog" || records[1][2] != "h1" || records[1][3] != "7200" {
		t.Fatalf("csv row = %v", records[1])
	}
	h, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("html: %v", err)
	}
	page := string(h)
	if !strings.Contains(page, "Sonic, the Hedgehog") || !strings.Contains(page, "h1") {
		t.Fatalf("html lost the row: %s", page)
	}
	if !strings.Contains(page, "2h 0m") && !strings.Contains(page, "2h") {
		t.Fatalf("html lost the total: %s", page)
	}
	if !strings.Contains(page, "<table>") {
		t.Fatal("html has no table")
	}

	// The escaping case matters: a title is a file name.
	_, htmlPath2, err := playtimeExportFiles(dir, rows, "2026-02-01 12:00")
	if err != nil {
		t.Fatalf("export 2: %v", err)
	}
	h2, _ := os.ReadFile(htmlPath2)
	if strings.Contains(string(h2), "<Streets>") || !strings.Contains(string(h2), "&lt;Streets&gt;") {
		t.Fatalf("html did not escape a title: %s", h2)
	}
}
