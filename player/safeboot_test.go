package main

// Tests for the recovery chord. The chord itself is hardware, but its two
// decisions — "was this a hold?" and "which backup restores the executable?" —
// are pure, and the rollback runs against a temporary state dir. The boot
// guard's tests are in bootguard_test.go.

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

func TestRollbackPickVersion(t *testing.T) {
	running := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	// Newest saved first, by a device clock that was not set: v3 is the build
	// a rollback has just left, saved last.
	entries := []uhistEntry{
		{ID: "v3", Tag: "JukaHub-20261105000000", SavedAt: "2020-01-01T00:05:00Z"},
		{ID: "v1", Tag: "JukaHub-20261001000000", SavedAt: "2020-01-01T00:04:00Z"},
		{ID: "v2", Tag: "build-a", Date: "2026-10-20T00:00:00Z", SavedAt: "2020-01-01T00:03:00Z"},
	}
	if e, ok := rollbackPickVersion(entries, running, true, "", ""); !ok || e.ID != "v2" {
		t.Fatalf("want the newest release before the running one, got %q ok=%v", e.ID, ok)
	}
	// Without the running build's release time, the newest saved one that is
	// not the release this device was rolled back from.
	if e, ok := rollbackPickVersion(entries, time.Time{}, false, "JukaHub-20261105000000", ""); !ok || e.ID != "v1" {
		t.Fatalf("the release rolled back from must be skipped, got %q ok=%v", e.ID, ok)
	}
	// A newer release is never a previous version.
	if e, ok := rollbackPickVersion(entries[:1], running, true, "", ""); ok {
		t.Fatalf("picked %q, released after the running build", e.ID)
	}
	// An undated saved version is offered when no dated one qualifies.
	undated := []uhistEntry{entries[0], {ID: "x", SavedAt: "2026-10-01T00:00:00Z"}}
	if e, ok := rollbackPickVersion(undated, running, true, "", ""); !ok || e.ID != "x" {
		t.Fatalf("got %q ok=%v", e.ID, ok)
	}
	// The running build's own tag is never offered.
	same := []uhistEntry{{ID: "same", Tag: "JukaHub-20261001000000"}}
	if _, ok := rollbackPickVersion(same, running, true, "", "jukahub-20261001000000"); ok {
		t.Fatal("the running build is not a previous version")
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
