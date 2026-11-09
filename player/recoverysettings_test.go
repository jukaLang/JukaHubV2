package main

// Tests for the settings half of recovery: where the last good settings are
// found, how the card dates them, and the restart contract with launch.sh.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoveryFindSettingsSourcesNewestFirst(t *testing.T) {
	appDir, exportDir := t.TempDir(), t.TempDir()
	touch := func(path string, mod time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	day := time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)

	lastGood := t.TempDir()
	if got := recoveryFindSettingsSources(appDir, exportDir, lastGood); len(got) != 0 {
		t.Fatalf("nothing saved yet, got %+v", got)
	}

	touch(filepath.Join(appDir, "jukauser.json.bak"), day.Add(48*time.Hour))
	got := recoveryFindSettingsSources(appDir, exportDir, "")
	if len(got) != 1 || got[0].Kind != recoverySourceBak || got[0].Path != appDir {
		t.Fatalf("the .bak alone: %+v", got)
	}

	// Two weekly backups and a manual one: only the newest weekly counts, and
	// it is newer than the .bak.
	touch(filepath.Join(exportDir, "JukaHub-backup-brick-20261101-1200-auto"+bkupExt), day)
	newest := filepath.Join(exportDir, "JukaHub-backup-brick-20261108-1200-auto"+bkupExt)
	touch(newest, day.Add(7*24*time.Hour))
	touch(filepath.Join(exportDir, "JukaHub-backup-brick-20261109-1200"+bkupExt), day.Add(8*24*time.Hour))
	got = recoveryFindSettingsSources(appDir, exportDir, lastGood)
	if len(got) != 2 {
		t.Fatalf("want the .bak and the newest weekly backup, got %+v", got)
	}
	if got[0].Kind != recoverySourceWeekly || got[0].Path != newest {
		t.Fatalf("newest first: %+v", got)
	}
	if got[1].Kind != recoverySourceBak {
		t.Fatalf("the .bak comes second: %+v", got)
	}

	// What the last healthy start read leads, even when it is the oldest: the
	// others were written by saves, and a save may be what broke the start.
	touch(filepath.Join(lastGood, "jukaconfig.json"), day)
	got = recoveryFindSettingsSources(appDir, exportDir, lastGood)
	if len(got) != 3 || got[0].Kind != recoverySourceLastGood || got[0].Path != lastGood ||
		got[1].Kind != recoverySourceWeekly || got[2].Kind != recoverySourceBak {
		t.Fatalf("the last good start first, then newest first: %+v", got)
	}
}

func TestSafetyBackupPruneKeepsTheNewOne(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, mod time.Time) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	for i := 0; i < bkupSafetyKeep; i++ {
		write("JukaHub-safety-2026110"+string(rune('1'+i))+"-1000"+bkupExt, good.Add(time.Duration(i)*time.Hour))
	}
	// Written after a cold boot without an RTC: the oldest by its time.
	fresh := write("JukaHub-safety-20200101-0000"+bkupExt, time.Date(2020, 1, 1, 0, 0, 30, 0, time.Local))
	bkupPruneKeep(dir, "JukaHub-safety-", "", bkupSafetyKeep, fresh)
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("the backup just written must survive the prune: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != bkupSafetyKeep {
		t.Fatalf("kept %d, want %d", len(entries), bkupSafetyKeep)
	}
	if _, err := os.Stat(filepath.Join(dir, "JukaHub-safety-20261101-1000"+bkupExt)); !os.IsNotExist(err) {
		t.Fatalf("the oldest dated backup should have gone: %v", err)
	}
}

func TestRecoveryWhenHidesAnUnsetClock(t *testing.T) {
	if got := recoveryWhen(time.Date(2019, 12, 31, 0, 0, 0, 0, time.UTC)); got != "" {
		t.Fatalf("a factory-epoch time must not be shown, got %q", got)
	}
	if got := recoveryWhen(time.Time{}); got != "" {
		t.Fatalf("no time, got %q", got)
	}
	if got := recoveryWhen(time.Date(2026, 11, 9, 14, 3, 0, 0, time.Local)); got != " · 9 Nov 14:03" {
		t.Fatalf("got %q", got)
	}
}

func TestLaunchScriptHonoursTheRestartRequest(t *testing.T) {
	data, err := os.ReadFile("launch.sh")
	if err != nil {
		t.Skipf("launch.sh not next to the package: %v", err)
	}
	script := string(data)
	for _, want := range []string{recoveryRestartToken, recoveryRestartMarker, "CRASHED"} {
		if !strings.Contains(script, want) {
			t.Errorf("launch.sh does not carry %q; the app would never use it", want)
		}
	}
}
