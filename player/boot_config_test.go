package main

// boot_config_test.go — the promise that the interface comes from
// jukaconfig.json. JukaHub has no built-in UI: main() enters through
// LoadXMLConfigWithFallback("jukaconfig.json") and log.Fatals if nothing
// loads, then draws only what the config declares — currentSceneIndex starts
// at 0, and every renderer iterates config.Scenes. These checks pin that
// chain against the shipped file without skipping: the JSON is tracked in
// git, so its absence or corruption is a failure, not "not available here".

import (
	"os"
	"strings"
	"testing"
)

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
