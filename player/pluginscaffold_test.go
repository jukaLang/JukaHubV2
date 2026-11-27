package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestPluginScaffoldCreatesALoadableManifest proves the generated folder is a
// real plugin, not just three files: the manifest decodes as PluginInfo, the
// typed manifest parser finds no problems, and the entry the manifest names is
// there.
func TestPluginScaffoldCreatesALoadableManifest(t *testing.T) {
	root := t.TempDir()
	dir, err := newPluginScaffold(root, "hello-world")
	if err != nil {
		t.Fatalf("newPluginScaffold: %v", err)
	}
	if dir != filepath.Join(root, "hello-world") {
		t.Fatalf("scaffold dir = %q", dir)
	}
	data, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var info PluginInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("scaffolded manifest does not decode: %v", err)
	}
	if info.SchemaVersion != plugSchemaVersion {
		t.Fatalf("schemaVersion = %d, want %d", info.SchemaVersion, plugSchemaVersion)
	}
	if info.Name != "hello-world" || info.Title != "Hello World" || !info.Enabled {
		t.Fatalf("manifest = %+v", info)
	}
	if len(info.Permissions) != 0 {
		t.Fatalf("a new plugin asks for permissions: %v", info.Permissions)
	}
	if info.Entry == "" {
		t.Fatal("manifest names no entry")
	}
	if st, err := os.Stat(filepath.Join(dir, info.Entry)); err != nil || !st.Mode().IsRegular() {
		t.Fatalf("entry %q: %v", info.Entry, err)
	}
	bytes, err := os.ReadFile(filepath.Join(dir, info.Entry))
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes) == 0 {
		t.Fatal("the scaffolded entry is empty")
	}
	if ext := plugxParse("hello-world", dir, data); len(ext.Problems) != 0 {
		t.Fatalf("typed manifest parser reported %v", ext.Problems)
	}
	if _, err := newPluginScaffold(root, "hello-world"); err == nil {
		t.Fatal("scaffolding into an existing folder was allowed")
	}
	if _, err := newPluginScaffold(root, "../escape"); err == nil {
		t.Fatal("an unsafe plugin name was accepted")
	}
}

func TestPluginSchemaVersion(t *testing.T) {
	for _, tc := range []struct {
		version int
		ok      bool
	}{
		{0, true}, // old manifest without the field
		{1, true},
		{2, false},
		{-1, false},
	} {
		if got := plugSchemaProblem(tc.version) == ""; got != tc.ok {
			t.Errorf("plugSchemaProblem(%d) ok = %v, want %v", tc.version, got, tc.ok)
		}
	}
}

// TestHelloPluginExampleLoads keeps the documented sample honest. It is copied
// into plugins/ by hand, so a broken manifest would only be found by a reader.
func TestHelloPluginExampleLoads(t *testing.T) {
	dir := filepath.Join("..", "examples", "hello-plugin")
	data, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var info PluginInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("examples/hello-plugin/plugin.json: %v", err)
	}
	if problem := plugSchemaProblem(info.SchemaVersion); problem != "" {
		t.Fatal(problem)
	}
	if info.SchemaVersion != 1 || !info.Enabled || info.Entry == "" {
		t.Fatalf("example manifest = %+v", info)
	}
	if st, err := os.Stat(filepath.Join(dir, info.Entry)); err != nil || !st.Mode().IsRegular() {
		t.Fatalf("example entry %q: %v", info.Entry, err)
	}
	if ext := plugxParse("hello-plugin", dir, data); len(ext.Problems) != 0 {
		t.Fatalf("example parser problems = %v", ext.Problems)
	}
}

func TestNewPluginCommandScaffolds(t *testing.T) {
	root := t.TempDir()
	plugins := filepath.Join(root, "plugins")
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if handled, code := handleNewPluginCommand([]string{"--new-plugin", "cli-made"}); !handled || code != 0 {
		t.Fatalf("handled=%v code=%d", handled, code)
	}
	if _, err := os.Stat(filepath.Join(plugins, "cli-made", "plugin.json")); err != nil {
		t.Fatalf("the command wrote no manifest: %v", err)
	}
	if handled, code := handleNewPluginCommand([]string{"run"}); handled || code != 0 {
		t.Fatalf("an ordinary argument was intercepted: handled=%v code=%d", handled, code)
	}
	if handled, code := handleNewPluginCommand([]string{"--new-plugin"}); !handled || code != 2 {
		t.Fatalf("--new-plugin without a name: handled=%v code=%d", handled, code)
	}
}
