package main

import "testing"

// These tests guard the scene registry itself: names, coverage and the
// duplicate policy. They need no renderer, so they run anywhere.

func TestRegisteredScenesReportTheirOwnName(t *testing.T) {
	if len(sceneRegistry) == 0 {
		t.Fatal("no scenes registered")
	}
	for name, m := range sceneRegistry {
		if m == nil {
			t.Fatalf("%q registered a nil module", name)
		}
		if got := m.sceneName(); got != name {
			t.Fatalf("registry key %q holds a module that calls itself %q", name, got)
		}
	}
}

// Every scene with custom input or rendering must be reachable through the
// registry; if one is missing, its keys silently fall through to element
// navigation.
func TestCustomScenesAreRegistered(t *testing.T) {
	wanted := []string{
		"About", "Alarm", "Calculator", "Dashboard", "Dictionary", "Games",
		"JukaLand", "MusicPlayer", "Notes", "PDFReader", "Patch", "Pomodoro",
		"Terminal",
	}
	for _, name := range wanted {
		if _, ok := lookupScene(name); !ok {
			t.Errorf("scene %q is not registered", name)
		}
	}
}

// The config-driven screens must stay unregistered, or the generic renderer and
// element navigation would never run for them.
func TestConfigDrivenScenesStayGeneric(t *testing.T) {
	for _, name := range []string{
		"Main", "Tube", "Shorts", "Apps", "Favorites", "Chat", "Misc",
		"Emulator", "Settings", "SettingsGeneral", "SettingsAppearance",
		"ThemePresets", "LogExporter", "Exit", "FileExplorer", "Hardware",
	} {
		if _, ok := lookupScene(name); ok {
			t.Errorf("scene %q is registered but should use the generic path", name)
		}
	}
}

// stubScene is a module with no behaviour, used to exercise the registry.
type stubScene struct{ sceneBase }

func TestRegisterSceneKeepsFirstOnDuplicate(t *testing.T) {
	saved := sceneRegistry
	sceneRegistry = map[string]sceneModule{}
	defer func() { sceneRegistry = saved }()

	first := stubScene{sceneBase{name: "Dup"}}
	second := stubScene{sceneBase{name: "Dup"}}
	registerScene(first)
	registerScene(second)

	got, ok := lookupScene("Dup")
	if !ok {
		t.Fatal("duplicate registration dropped the scene entirely")
	}
	if got != first {
		t.Fatal("duplicate registration replaced the first module")
	}
	if len(sceneRegistry) != 1 {
		t.Fatalf("registry grew to %d entries on a duplicate", len(sceneRegistry))
	}
}

func TestRegisterSceneIgnoresEmptyName(t *testing.T) {
	saved := sceneRegistry
	sceneRegistry = map[string]sceneModule{}
	defer func() { sceneRegistry = saved }()

	registerScene(stubScene{sceneBase{name: ""}})
	registerScene(nil)
	if len(sceneRegistry) != 0 {
		t.Fatalf("registry accepted %d nameless modules", len(sceneRegistry))
	}
}

func TestRegisteredSceneNamesAreSorted(t *testing.T) {
	names := registeredSceneNames()
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Fatalf("registeredSceneNames is not sorted: %v", names)
		}
	}
	if len(names) != len(sceneRegistry) {
		t.Fatalf("registeredSceneNames returned %d of %d entries", len(names), len(sceneRegistry))
	}
}
