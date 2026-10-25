package main

// Tests for the recovery chord. The chord itself is hardware, but its two
// decisions — "was this a hold?" and "which backup restores the executable?" —
// are pure, and the rollback runs against a temporary state dir.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSafebootHeldAll(t *testing.T) {
	if safebootHeldAll(nil) {
		t.Fatal("no samples is not a hold")
	}
	if !safebootHeldAll([]bool{true, true, true}) {
		t.Fatal("a chord held in every sample is a hold")
	}
	if safebootHeldAll([]bool{true, false, true}) {
		t.Fatal("a released button means the chord was tapped, not held")
	}
}

func TestLatestExecutableBackup(t *testing.T) {
	self := filepath.FromSlash("/mnt/SDCARD/Apps/JukaHub/JukaHub")
	manifests := map[string]*BackupManifest{
		"20260101T000000Z": {Version: "0.3.0", Files: []BackupFile{{Path: self, Rel: "JukaHub"}}},
		"20260102T000000Z": {Version: "0.3.1", Files: []BackupFile{{Path: "/mnt/SDCARD/Apps/JukaHub/jukaconfig.json", Rel: "jukaconfig.json"}}},
	}
	load := func(name string) (*BackupManifest, error) {
		if m, ok := manifests[name]; ok {
			return m, nil
		}
		return nil, errors.New("missing")
	}

	// Newest-first order: the newest set has no executable, so the older one
	// that does must win.
	name, file, ok := latestExecutableBackup([]string{"20260102T000000Z", "20260101T000000Z"}, self, load)
	if !ok || name != "20260101T000000Z" || file.Rel != "JukaHub" {
		t.Fatalf("picked %q/%q ok=%v", name, file.Rel, ok)
	}

	// No set contains the executable.
	if _, _, ok := latestExecutableBackup([]string{"20260102T000000Z"}, self, load); ok {
		t.Fatal("a config-only backup must not be treated as an executable backup")
	}

	// A manifest that cannot be read is skipped, not fatal.
	if _, _, ok := latestExecutableBackup([]string{"missing", "20260101T000000Z"}, self, load); !ok {
		t.Fatal("an unreadable set must not hide a usable one")
	}
}

func TestRollbackLastUpdateRestoresExecutable(t *testing.T) {
	withTempPatchState(t)
	dir := t.TempDir()
	self := filepath.Join(dir, "JukaHub")
	if err := os.WriteFile(self, []byte("old-build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CreateBackupSet("test-update", []string{self}); err != nil {
		t.Fatalf("create backup: %v", err)
	}
	if err := os.WriteFile(self, []byte("new-broken-build"), 0o755); err != nil {
		t.Fatal(err)
	}

	name, version, err := rollbackLastUpdateTo(self)
	if err != nil {
		t.Fatalf("rollback failed: %v", err)
	}
	if name == "" {
		t.Fatal("rollback did not name the backup set it used")
	}
	_ = version // the test config has no version, so this may be empty

	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old-build" {
		t.Fatalf("executable was not restored: %q", data)
	}
	// The backup stays available for a second attempt.
	if sets, err := ListBackupSets(); err != nil || len(sets) == 0 {
		t.Fatalf("rollback consumed the backup set: %v %v", sets, err)
	}
}

func TestRollbackWithoutBackupsIsAnError(t *testing.T) {
	withTempPatchState(t)
	self := filepath.Join(t.TempDir(), "JukaHub")
	if err := os.WriteFile(self, []byte("build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rollbackLastUpdateTo(self); err == nil {
		t.Fatal("rollback with no backup set must report an error")
	}
}

func TestSafebootDiagnostic(t *testing.T) {
	prev := safeboot
	t.Cleanup(func() { safeboot = prev })

	safeboot = safebootState{}
	if got := safebootDiagnostic(); got != "" {
		t.Fatalf("unused chord should add no diagnostics line, got %q", got)
	}

	safeboot = safebootState{Triggered: true}
	if got := safebootDiagnostic(); !strings.Contains(got, "Safe boot    : on") {
		t.Fatalf("missing safe boot line: %q", got)
	}

	safeboot = safebootState{Triggered: true, RolledBack: "20260101T000000Z", Version: "0.3.0"}
	if got := safebootDiagnostic(); !strings.Contains(got, "rolled back to 0.3.0") {
		t.Fatalf("rollback version is not reported: %q", got)
	}

	safeboot = safebootState{Triggered: true, Err: errors.New("no backup set contains this executable")}
	if got := safebootDiagnostic(); !strings.Contains(got, "no rollback") {
		t.Fatalf("a failed rollback must be reported, got %q", got)
	}
	safeboot = safebootState{AutoRevert: true, RolledBack: "20260101T000000Z", Version: "0.4.0"}
	if got := safebootDiagnostic(); !strings.Contains(got, "Boot guard") || !strings.Contains(got, "rolled back to 0.4.0") {
		t.Fatalf("an automatic revert must be reported as a boot-guard line, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Boot guard — crash-loop counting and the automatic revert
// ---------------------------------------------------------------------------

// withSafebootReset saves the globals the boot guard writes to.
func withSafebootReset(t *testing.T) {
	t.Helper()
	prevBoot, prevMode := safeboot, safeMode
	t.Cleanup(func() { safeboot, safeMode = prevBoot, prevMode })
	safeboot = safebootState{}
	safeMode = safeModeState{}
}

// runBootGuard performs one boot of the "v2" binary at the given time.
func runBootGuard(t *testing.T, dir, updatedAt string, at time.Time, rollback func(string) (string, string, error)) {
	t.Helper()
	self := filepath.Join(dir, "JukaHub")
	if err := os.WriteFile(self, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	applyBootGuard(dir, self, "v2", updatedAt, at, rollback)
}

func TestBootGuardCountsFastFailedBootsThenRollsBack(t *testing.T) {
	withSafebootReset(t)
	dir := t.TempDir()
	updatedAt := "2026-10-23T12:00:00Z"
	t0 := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	rollbackCalls := 0
	rollback := func(self string) (string, string, error) {
		rollbackCalls++
		return "set1", "1.1", nil
	}

	runBootGuard(t, dir, updatedAt, t0, rollback)                     // boot 1: nothing before it
	runBootGuard(t, dir, updatedAt, t0.Add(10*time.Second), rollback) // boot 2: fast fail
	s, _ := readBootGuardFile(dir)
	if s.Unclean != 1 {
		t.Fatalf("boot2 unclean = %d, want 1", s.Unclean)
	}
	runBootGuard(t, dir, updatedAt, t0.Add(20*time.Second), rollback) // boot 3: fast fail
	s, _ = readBootGuardFile(dir)
	if s.Unclean != 2 {
		t.Fatalf("boot3 unclean = %d, want 2", s.Unclean)
	}
	runBootGuard(t, dir, updatedAt, t0.Add(30*time.Second), rollback) // boot 4: third fast fail
	s, _ = readBootGuardFile(dir)
	if rollbackCalls != 1 {
		t.Fatalf("rollback calls = %d, want 1 at the third fast failure", rollbackCalls)
	}
	if s.Unclean != 0 {
		t.Fatalf("counter must reset after the rollback, got %d", s.Unclean)
	}
	if !safeboot.AutoRevert || safeboot.Version != "1.1" {
		t.Fatalf("safeboot state = %+v, want AutoRevert with version 1.1", safeboot)
	}
	if !safeMode.Active || safeMode.Reason != "crash-loop auto-rollback" {
		t.Fatalf("safe mode = %+v, want crash-loop auto-rollback", safeMode)
	}
	if got := safebootDiagnostic(); got == "" || !strings.Contains(got, "Boot guard") || !strings.Contains(got, "1.1") {
		t.Errorf("diagnostics line = %q, want the Boot guard revert line", got)
	}
}

func TestBootGuardIgnoresLongBoots(t *testing.T) {
	withSafebootReset(t)
	dir := t.TempDir()
	updatedAt := "2026-10-23T12:00:00Z"
	t0 := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	noRollback := func(self string) (string, string, error) {
		t.Error("rollback must not run on this test path")
		return "", "", nil
	}

	// A long, apparently power-cut session must not count as a failure...
	runBootGuard(t, dir, updatedAt, t0, noRollback)
	runBootGuard(t, dir, updatedAt, t0.Add(40*time.Minute), noRollback)
	s, _ := readBootGuardFile(dir)
	if s.Unclean != 0 {
		t.Fatalf("a 40-minute boot was counted as failed: unclean %d", s.Unclean)
	}
	// ...and a long session resets a counted streak instead of growing it.
	runBootGuard(t, dir, updatedAt, t0.Add(40*time.Minute+10*time.Second), noRollback)
	s, _ = readBootGuardFile(dir)
	if s.Unclean != 1 {
		t.Fatalf("quick fail after long run: unclean %d, want 1", s.Unclean)
	}
	runBootGuard(t, dir, updatedAt, t0.Add(40*time.Minute+10*time.Second+30*time.Minute), noRollback)
	s, _ = readBootGuardFile(dir)
	if s.Unclean != 0 {
		t.Fatalf("long run did not reset the streak: unclean %d", s.Unclean)
	}
}

func TestBootGuardResetsOnVersionChange(t *testing.T) {
	withSafebootReset(t)
	dir := t.TempDir()
	updatedAt := "2026-10-23T12:00:00Z"
	t0 := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	noRollback := func(self string) (string, string, error) {
		t.Error("a fresh build must not inherit the old build's crash count")
		return "", "", nil
	}

	// The previous build failed fast twice; the new build then takes over and
	// the marker carries the old stamp with two counted failures.
	runBootGuard(t, dir, "", t0, noRollback)
	runBootGuard(t, dir, "", t0.Add(10*time.Second), noRollback)
	prev, _ := readBootGuardFile(dir)
	prev.Version = "v1"
	prev.Unclean = 2
	writeBootGuardFile(dir, prev)

	runBootGuard(t, dir, updatedAt, t0.Add(30*time.Second), noRollback)
	s, _ := readBootGuardFile(dir)
	if s.Unclean != 0 || s.Version != "v2" {
		t.Fatalf("new build marker = %+v, want unclean 0 / v2", s)
	}
	if safeMode.Active {
		t.Error("a version change must not activate safe mode")
	}
}

func TestBootGuardNeedsARecentUpdate(t *testing.T) {
	withSafebootReset(t)
	dir := t.TempDir()
	t0 := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	rollbackCalls := 0
	rollback := func(self string) (string, string, error) {
		rollbackCalls++
		return "set1", "1.1", nil
	}

	// No recent update: the old install has three fast failures, but the
	// guard only logs — the manual recovery chord is its fallback.
	runBootGuard(t, dir, "", t0, rollback)
	runBootGuard(t, dir, "", t0.Add(10*time.Second), rollback)
	self := filepath.Join(dir, "JukaHub")
	applyBootGuard(dir, self, "v2", "2001-01-01T00:00:00Z", t0.Add(20*time.Second), rollback)
	s, _ := readBootGuardFile(dir)
	if rollbackCalls != 0 {
		t.Fatalf("rollback ran %d time(s) for a stale install", rollbackCalls)
	}
	if s.Unclean != 2 {
		t.Fatalf("stale-install fast failures are still counted (but no rollback), got %d", s.Unclean)
	}
	if safeboot.AutoRevert {
		t.Error("safeboot must stay untouched when the guard declines to roll back")
	}
}

func TestBootGuardClockResetCountsAsFailure(t *testing.T) {
	withSafebootReset(t)
	dir := t.TempDir()
	updatedAt := "2026-10-23T12:00:00Z"
	t0 := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)

	// The handheld has no RTC: boot 1 with a good clock, boot 2 before NTP
	// with the clock back at the factory epoch. Unknown-short uptime is the
	// safer bet for a device that cannot start.
	runBootGuard(t, dir, updatedAt, t0, func(string) (string, string, error) { return "", "", nil })
	runBootGuard(t, dir, updatedAt, t0.Add(-6*365*24*time.Hour), func(string) (string, string, error) { return "", "", nil })
	s, _ := readBootGuardFile(dir)
	if s.Unclean != 1 {
		t.Fatalf("a clock-reset boot was not counted: unclean %d", s.Unclean)
	}
}

func TestBootGuardClearDeletesMarker(t *testing.T) {
	dir := withTempPatchState(t)
	writeBootGuardFile(dir, bootGuardFileState{Version: "v2", Unclean: 1})
	clearBootGuard()
	if _, err := os.Stat(filepath.Join(dir, bootGuardFile)); !os.IsNotExist(err) {
		t.Errorf("marker survived the clean-exit cleanup: %v", err)
	}
}
