package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWriteAppFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanAppsFolder(t *testing.T) {
	dir := t.TempDir()
	mustWriteAppFile(t, filepath.Join(dir, "CoolApp.exe"))
	mustWriteAppFile(t, filepath.Join(dir, "My Link.lnk"))
	mustWriteAppFile(t, filepath.Join(dir, "Uninstall CoolApp.lnk")) // skipped
	mustWriteAppFile(t, filepath.Join(dir, "notes.txt"))             // not a launcher
	mustWriteAppFile(t, filepath.Join(dir, "script.sh"))

	// TrimUI-style subfolder app.
	sub := filepath.Join(dir, "JukaLand")
	mustWriteAppFile(t, filepath.Join(sub, "launch.sh"))
	cfg := `{"label": "JukaLand", "launch": "launch.sh"}`
	if err := os.WriteFile(filepath.Join(sub, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	// A folder without config.json is not an app.
	mustWriteAppFile(t, filepath.Join(dir, "random-dir", "x.txt"))

	entries := scanAppsFolder(dir)
	got := map[string]string{}
	for _, e := range entries {
		got[e.Name] = e.Path
	}
	want := map[string]string{
		"CoolApp":  filepath.Join(dir, "CoolApp.exe"),
		"My Link":  filepath.Join(dir, "My Link.lnk"),
		"script":   filepath.Join(dir, "script.sh"),
		"JukaLand": filepath.Join(sub, "launch.sh"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries %v, want %d", len(entries), got, len(want))
	}
	for name, path := range want {
		if got[name] != path {
			t.Errorf("entry %q: got path %q, want %q", name, got[name], path)
		}
	}
	// Sorted case-insensitively by display name.
	for i := 1; i < len(entries); i++ {
		if strings.ToLower(entries[i-1].Name) > strings.ToLower(entries[i].Name) {
			t.Errorf("entries not sorted: %q before %q", entries[i-1].Name, entries[i].Name)
		}
	}
}

func TestScanSystemAppsDepthCapAndSkip(t *testing.T) {
	root := t.TempDir()
	mustWriteAppFile(t, filepath.Join(root, "Top.lnk"))
	mustWriteAppFile(t, filepath.Join(root, "Vendor", "Nested.lnk"))
	mustWriteAppFile(t, filepath.Join(root, "Vendor", "Deep", "Deeper.lnk"))
	mustWriteAppFile(t, filepath.Join(root, "Vendor", "Deep", "Deeper", "TooDeep", "Hidden.lnk"))
	mustWriteAppFile(t, filepath.Join(root, "Vendor", "Uninstall Nested.lnk"))

	entries := scanSystemApps(root)
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	for _, want := range []string{"Top", "Nested", "Deeper"} {
		if !names[want] {
			t.Errorf("missing entry %q (got %v)", want, names)
		}
	}
	if names["Hidden"] {
		t.Error("depth cap failed: found entry 4 levels deep")
	}
	if names["Uninstall Nested"] {
		t.Error("uninstaller shortcut should be skipped")
	}
}

func TestCollectAppEntriesOrderAndDedupe(t *testing.T) {
	appsDir := t.TempDir()
	mustWriteAppFile(t, filepath.Join(appsDir, "Alpha.exe"))
	mustWriteAppFile(t, filepath.Join(appsDir, "Shared.exe"))

	sys := t.TempDir()
	mustWriteAppFile(t, filepath.Join(sys, "shared.lnk")) // same display name as portable
	mustWriteAppFile(t, filepath.Join(sys, "Beta.lnk"))

	entries := collectAppEntries(appsDir, []string{sys})
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	wantOrder := []string{"Alpha", "Shared", "Beta"}
	for i, want := range wantOrder {
		if i >= len(names) || names[i] != want {
			t.Fatalf("order = %v, want prefix %v", names, wantOrder)
		}
	}
	// Built-in applets always trail the external entries.
	last := names[len(wantOrder):]
	if len(last) != len(builtinApplets) {
		t.Fatalf("built-ins = %v, want %d entries", last, len(builtinApplets))
	}
	for i, a := range builtinApplets {
		if last[i] != a.Name+" (built-in)" {
			t.Errorf("built-in %d = %q, want %q", i, last[i], a.Name+" (built-in)")
		}
	}
}

func TestAppEntryTarget(t *testing.T) {
	scene, external := appEntryTarget(FileEntry{Path: "scene:JukaLand"})
	if scene != "JukaLand" || external != "" {
		t.Errorf("built-in: got (%q, %q)", scene, external)
	}
	scene, external = appEntryTarget(FileEntry{Path: `C:\apps\Foo.exe`})
	if scene != "" || external != `C:\apps\Foo.exe` {
		t.Errorf("external: got (%q, %q)", scene, external)
	}
}

// TestBuiltinAppletScenesExist guards against listing an applet whose scene is
// missing from jukaconfig.json: activating it would silently do nothing.
func TestBuiltinAppletScenesExist(t *testing.T) {
	data, err := os.ReadFile("jukaconfig.json")
	if err != nil {
		t.Fatalf("read jukaconfig.json: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse jukaconfig.json: %v", err)
	}
	// The Browser scene is injected at startup, not stored in shipped configs.
	ensureBrowserScene(&cfg)
	scenes := map[string]bool{}
	for _, s := range cfg.Scenes {
		scenes[s.Name] = true
	}
	for _, a := range builtinApplets {
		if !scenes[a.Scene] {
			t.Errorf("built-in app %q points at missing scene %q", a.Name, a.Scene)
		}
	}
}

// TestAppsSceneConfig checks the Apps scene in jukaconfig.json is wired to the
// app launcher: a dynamiclist on app_entries plus the launcher action buttons.
func TestAppsSceneConfig(t *testing.T) {
	data, err := os.ReadFile("jukaconfig.json")
	if err != nil {
		t.Fatalf("read jukaconfig.json: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse jukaconfig.json: %v", err)
	}
	idx := findSceneIndex(&cfg, "Apps")
	if idx < 0 || cfg.Scenes[idx].Name != "Apps" {
		t.Fatal("Apps scene missing from jukaconfig.json")
	}
	dl, ok := sceneDynamicListElement(cfg.Scenes[idx])
	if !ok {
		t.Fatal("Apps scene has no dynamiclist")
	}
	if dl.Variable != appsEntriesVar {
		t.Errorf("dynamiclist variable = %q, want %q", dl.Variable, appsEntriesVar)
	}
	// The buttons must use the exact trigger constants handled in the trigger
	// switch, so config and code cannot drift apart.
	triggers := map[string]bool{}
	for _, e := range cfg.Scenes[idx].Elements {
		if e.Type == "button" {
			triggers[e.Trigger] = true
		}
	}
	for _, want := range []string{triggerAppRefresh, triggerAppOpenFolder} {
		if !triggers[want] {
			t.Errorf("Apps scene missing button with trigger %q", want)
		}
	}
}
