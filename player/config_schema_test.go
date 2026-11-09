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

// The interface comes from jukaconfig.json: main() boots through
// LoadXMLConfigWithFallback and draws only what the config declares, so the
// shipped file must load on its own (not through the .bak), validate, and
// carry a Main scene with elements in it.
func TestShippedJukaconfigBootsTheInterface(t *testing.T) {
	// The live file must parse on its own. The boot loader is willing to
	// serve jukaconfig.json.bak when the live file is broken — a recovery
	// path for the card, not a state the shipped file may sit in.
	if _, err := loadConfig("jukaconfig.json"); err != nil {
		t.Fatalf("jukaconfig.json itself is not loadable (the boot would fall back to the backup): %v", err)
	}

	// The same entry point main() uses, so what boots is what is tested.
	cfg, path, err := LoadXMLConfigWithFallback("jukaconfig.json")
	if err != nil {
		t.Fatalf("the shipped config did not load through the boot entry point: %v", err)
	}
	// With no jukaconfig.xml beside it, the boot must be the JSON itself.
	// An active editor export legitimately takes over by mod-time (that is
	// ResolveConfigPath's design), so the pin applies to the checkout git
	// ships — and still catches a loader that starts reporting some other
	// file, like the backup, as the source of the interface.
	if _, statErr := os.Stat(XMLSidecarPath("jukaconfig.json")); statErr != nil {
		if path != "jukaconfig.json" {
			t.Fatalf("the boot loaded %q without a sidecar to explain it, want the shipped JSON", path)
		}
	}

	// The same validation main() runs: scenes named, elements typed. main
	// treats a failure as advisory and keeps going; for the file that ships,
	// it must simply pass.
	if err := NewConfigValidator().Validate(cfg); err != nil {
		t.Fatalf("the shipped config does not validate: %v", err)
	}
	if cfg.Version == "" {
		t.Error("the shipped config has no Version; About and the updater read it from here")
	}

	// It has to be an interface: scenes to switch between, elements in them
	// to draw.
	if len(cfg.Scenes) == 0 {
		t.Fatal("no scenes: the interface would render nothing")
	}
	total := 0
	for i, scene := range cfg.Scenes {
		if strings.TrimSpace(scene.Name) == "" {
			t.Errorf("scene %d has no name; scenes are addressed by name", i)
		}
		total += len(scene.Elements)
	}
	if total == 0 {
		t.Fatal("no elements in any scene: there is no interface to draw")
	}

	// Boot is currentSceneIndex = 0: the first scene is the first screen, so
	// it must be a screen.
	boot := cfg.Scenes[0]
	if strings.TrimSpace(boot.Name) == "" || len(boot.Elements) == 0 {
		t.Fatalf("the boot scene %q has %d elements; currentSceneIndex starts at 0",
			boot.Name, len(boot.Elements))
	}

	// Back-navigation from several scenes addresses "Main" by name
	// (favorites, packages and shorts all go home that way).
	foundMain := false
	for _, scene := range cfg.Scenes {
		if scene.Name == "Main" {
			foundMain = true
			break
		}
	}
	if !foundMain {
		t.Error(`no "Main" scene; back-navigation calls findSceneIndex(config, "Main")`)
	}
}
