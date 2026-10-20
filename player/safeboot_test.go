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
}
