package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const validThemeJSON = `{
  "name": "Test Theme",
  "background": "#101010", "buttonColor": "#202020", "labelColor": "#f0f0f0",
  "inputColor": "#303030", "surface": "#181818", "surfaceAlt": "#222222",
  "surfaceRaised": "#2a2a2a", "textPrimary": "#ffffff", "textSecondary": "#cccccc",
  "textTertiary": "#999999", "borderSubtle": "#333333", "borderDefault": "#444444",
  "borderFocus": "#55aaff", "success": "#33cc66", "warning": "#ffcc00",
  "danger": "#ff3333", "info": "#3399ff", "overlay": "#00000080"
}`

// snapshotThemeState saves the global preset registry and restores it on cleanup,
// so loader tests never leak registered themes into other tests.
func snapshotThemeState(t *testing.T) {
	t.Helper()
	presets := make(map[string]ThemePreset, len(themePresets))
	for k, v := range themePresets {
		presets[k] = v
	}
	order := slices.Clone(themePresetOrder)
	t.Cleanup(func() {
		themePresets = presets
		themePresetOrder = order
	})
}

func writeThemeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadThemeFilesRegistersValidTheme(t *testing.T) {
	snapshotThemeState(t)
	dir := t.TempDir()
	writeThemeFile(t, dir, "Test-Ocean.json", validThemeJSON)

	added, failed := LoadThemeFiles(dir)
	if len(failed) != 0 {
		t.Fatalf("unexpected failures: %v", failed)
	}
	if !slices.Equal(added, []string{"test-ocean"}) {
		t.Fatalf("added = %v, want [test-ocean]", added)
	}
	preset, ok := themePresets["test-ocean"]
	if !ok {
		t.Fatal("test-ocean preset not registered")
	}
	if preset.Name != "Test Theme" || preset.Background != "#101010" {
		t.Fatalf("preset not converted correctly: %+v", preset)
	}
	if !slices.Contains(themePresetOrder, "test-ocean") {
		t.Fatalf("test-ocean missing from themePresetOrder: %v", themePresetOrder)
	}
}

func TestLoadThemeFilesSkipsInvalidFiles(t *testing.T) {
	snapshotThemeState(t)
	dir := t.TempDir()
	writeThemeFile(t, dir, "broken.json", `{not json`)
	writeThemeFile(t, dir, "noname.json", `{"background": "#000000"}`)
	writeThemeFile(t, dir, "badcolour.json", `{"name": "Bad", "background": "red"}`)
	writeThemeFile(t, dir, "notes.txt", validThemeJSON)

	added, failed := LoadThemeFiles(dir)
	if len(added) != 0 {
		t.Fatalf("added = %v, want none", added)
	}
	if len(failed) != 3 {
		t.Fatalf("failed = %d entries, want 3: %v", len(failed), failed)
	}
	if _, ok := themePresets["broken"]; ok {
		t.Fatal("invalid theme was registered")
	}
}

func TestLoadThemeFilesDoesNotOverrideBuiltIn(t *testing.T) {
	snapshotThemeState(t)
	var builtin string
	for id := range themePresets {
		builtin = id
		break
	}
	if builtin == "" {
		t.Fatal("no built-in presets to collide with")
	}
	before := themePresets[builtin]
	dir := t.TempDir()
	writeThemeFile(t, dir, builtin+".json", validThemeJSON)

	added, failed := LoadThemeFiles(dir)
	if len(added) != 0 {
		t.Fatalf("added = %v, want none", added)
	}
	if _, ok := failed[filepath.Join(dir, builtin+".json")]; !ok {
		t.Fatalf("collision not reported: %v", failed)
	}
	if themePresets[builtin] != before {
		t.Fatal("built-in preset was overwritten")
	}
}

func TestLoadThemeFilesMissingDirIsNoOp(t *testing.T) {
	snapshotThemeState(t)
	added, failed := LoadThemeFiles(filepath.Join(t.TempDir(), "absent"))
	if len(added) != 0 || len(failed) != 0 {
		t.Fatalf("expected no-op, got added=%v failed=%v", added, failed)
	}
}
