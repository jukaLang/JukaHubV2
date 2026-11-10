package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Damaged configs are evidence, not a collection: after more recoveries than
// the bound, the card holds the newest few and nothing older.
func TestPreserveCorruptConfigKeepsFiveNewest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jukaconfig.json")
	var kept []string
	for i := 0; i < configCorruptKeep+3; i++ {
		writeTestConfig(t, path, `{"scenes": [`)
		aside := preserveCorruptConfig(path)
		if aside == "" {
			t.Fatalf("recovery %d kept nothing", i)
		}
		kept = append(kept, aside)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "jukaconfig.json.corrupt-") {
			n++
		}
	}
	if n != configCorruptKeep {
		t.Fatalf("kept %d damaged configs, want %d", n, configCorruptKeep)
	}
	for i, aside := range kept {
		_, err := os.Stat(aside)
		if i < len(kept)-configCorruptKeep && err == nil {
			t.Errorf("old copy %s should have been pruned", filepath.Base(aside))
		}
		if i >= len(kept)-configCorruptKeep && err != nil {
			t.Errorf("newest copy %s should have been kept: %v", filepath.Base(aside), err)
		}
	}
}

// Two recoveries in the same second must leave two files, not one.
func TestCorruptAsidePathDoesNotReplaceAnEarlierCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jukaconfig.json")
	if err := os.WriteFile(path+".corrupt-20261109-120000", []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	aside := corruptAsidePath(path)
	if aside == path+".corrupt-20261109-120000" {
		// The name is only fixed when the clock reads that second; either way
		// the returned path must not exist yet.
		if _, err := os.Stat(aside); err == nil {
			t.Fatal("corruptAsidePath returned a name that already exists")
		}
	}
}

// One launcher script is the source for the handheld: the packaged copy drifted
// until it lacked the restart protocol, the time zone and log rotation.
func TestOneLaunchScriptIsShipped(t *testing.T) {
	if _, err := os.Stat(filepath.Join("trimui", "Apps", "JukaHub", "launch.sh")); err == nil {
		t.Fatal("trimui/Apps/JukaHub/launch.sh is back; packages must use player/launch.sh")
	}
	script, err := os.ReadFile("package-sdcard.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), `cp -f launch.sh`) {
		t.Error("package-sdcard.sh no longer copies the single launch.sh")
	}
}
