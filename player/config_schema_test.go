package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateConfig(t *testing.T) {
	v0 := &Config{}
	from, err := MigrateConfig(v0)
	if err != nil {
		t.Fatalf("v0 migration: %v", err)
	}
	if from != 0 || v0.SchemaVersion != ConfigSchemaVersion {
		t.Errorf("v0 migrated from %d to %d, want 0 -> %d", from, v0.SchemaVersion, ConfigSchemaVersion)
	}

	current := &Config{SchemaVersion: ConfigSchemaVersion}
	from, err = MigrateConfig(current)
	if err != nil || from != ConfigSchemaVersion {
		t.Errorf("current schema: from=%d err=%v", from, err)
	}

	future := &Config{SchemaVersion: ConfigSchemaVersion + 5}
	if _, err := MigrateConfig(future); err == nil {
		t.Error("a config from a newer build must be refused")
	}
	if future.SchemaVersion != ConfigSchemaVersion+5 {
		t.Errorf("a refused config was modified: %d", future.SchemaVersion)
	}
	if _, err := MigrateConfig(nil); err == nil {
		t.Error("nil config should fail")
	}
}

// A pre-schema config loads, is stamped, and survives a save/load round trip.
func TestConfigSchemaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jukaconfig.json")
	legacy := `{"AppName":"JukaHub","variables":{},"scenes":[]}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load legacy config: %v", err)
	}
	if cfg.SchemaVersion != ConfigSchemaVersion {
		t.Fatalf("loaded schema = %d, want %d", cfg.SchemaVersion, ConfigSchemaVersion)
	}

	saved := filepath.Join(dir, "saved.json")
	if err := SaveConfig(saved, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	data, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if got, _ := raw["schemaVersion"].(float64); int(got) != ConfigSchemaVersion {
		t.Errorf("saved schemaVersion = %v, want %d", raw["schemaVersion"], ConfigSchemaVersion)
	}
}

// A config file from a newer build is rejected with a clear error rather than
// loaded with unknown semantics.
func TestConfigRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jukaconfig.json")
	body := `{"schemaVersion":99,"variables":{},"scenes":[]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfig(path)
	if err == nil {
		t.Fatal("a newer schema must not load")
	}
	if !strings.Contains(err.Error(), "newer than this build") {
		t.Errorf("error = %v, want a newer-schema message", err)
	}
}

// AtomicWrite must keep the previous file as .bak and leave no temp file.
func TestAtomicWriteKeepsBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := AtomicWrite(path, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(path, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	live, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(live) != "two" || string(backup) != "one" {
		t.Errorf("live=%q backup=%q, want two/one", live, backup)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file left behind after an atomic write")
	}
}
