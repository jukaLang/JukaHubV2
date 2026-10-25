package main

// atomic_config_test.go pins the durability properties of AtomicWrite and the
// jukauser.json recovery path. The device is normally stopped by pulling the
// card or holding the power button, so "the write was interrupted" is the
// expected failure mode, not an exotic one.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The backup is a copy, so a failed replacement must leave the live config
// untouched. The old implementation renamed the live file to .bak first, which
// meant a failed write left no config at its path at all.
func TestAtomicWriteLeavesTheLiveFileWhenTheReplaceFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := AtomicWrite(path, []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// A directory where the temp file must go makes the write fail after the
	// backup step, which is the exact window that used to lose the file.
	if err := os.Mkdir(path+".tmp", 0o755); err != nil {
		t.Fatalf("create blocking directory: %v", err)
	}
	if err := AtomicWrite(path, []byte(`{"version":2}`), 0o644); err == nil {
		t.Fatal("a blocked temp write must report an error")
	}
	live, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("live config disappeared after a failed write: %v", err)
	}
	if string(live) != `{"version":1}` {
		t.Errorf("live config = %q, want the previous content", live)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(backup) != `{"version":1}` {
		t.Errorf("backup = %q, want a copy of the live file", backup)
	}
}

func TestAtomicWriteBackupTracksThePreviousVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	for _, body := range []string{"one", "two", "three"} {
		if err := AtomicWrite(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %q: %v", body, err)
		}
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backup) != "two" {
		t.Errorf("backup = %q, want the version before the newest one", backup)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file left behind")
	}
}

// TestLoadUserConfigRecoversFromBackup is the user-visible half: a damaged
// jukauser.json is replaced by the previous good copy, the primary path is
// rewritten, and a notice is queued exactly once.
func TestLoadUserConfigRecoversFromBackup(t *testing.T) {
	t.Chdir(t.TempDir())
	prevCache, prevWrites := userConfigCache, userConfigWritesEnabled
	userConfigCache, userConfigWritesEnabled = nil, true
	t.Cleanup(func() {
		userConfigCache, userConfigWritesEnabled = prevCache, prevWrites
		userConfigRecoveryNotice = ""
	})

	if err := os.WriteFile("jukauser.json", []byte(`{"variables":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("jukauser.json.bak", []byte(`{"variables":{"weatherUnit":"F"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	uc := loadUserConfig()
	if uc == nil || uc.Variables.WeatherUnit != "F" {
		t.Fatalf("recovered config lost the backup settings: %+v", uc)
	}
	notice := takeUserConfigRecoveryNotice()
	if !strings.Contains(notice, "restored from the last backup") {
		t.Errorf("recovery notice = %q, want a clear explanation", notice)
	}
	if second := takeUserConfigRecoveryNotice(); second != "" {
		t.Errorf("the recovery notice repeated: %q", second)
	}

	restored, err := os.ReadFile("jukauser.json")
	if err != nil {
		t.Fatalf("primary config was not rewritten: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(restored, &decoded); err != nil {
		t.Errorf("rewritten primary config does not parse: %v\n%s", err, restored)
	}
}

func TestLoadUserConfigStartsFreshWithNoFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	prevCache, prevWrites := userConfigCache, userConfigWritesEnabled
	userConfigCache, userConfigWritesEnabled = nil, false
	t.Cleanup(func() {
		userConfigCache, userConfigWritesEnabled = prevCache, prevWrites
		userConfigRecoveryNotice = ""
	})

	uc := loadUserConfig()
	if uc == nil {
		t.Fatal("no config produced a nil result")
	}
	if uc.Variables.Custom == nil {
		t.Error("fresh config has no Custom map; callers write into it")
	}
	if notice := takeUserConfigRecoveryNotice(); notice != "" {
		t.Errorf("a first run produced a recovery notice: %q", notice)
	}
}
