package main

// plugcatalog_test.go — the Plugins page's model. Every plugin folder is
// listed, switched on, off or broken; the user's switch wins over the manifest
// and lives in the user config rather than in the plugin's files; the plugins
// that ship with JukaHub are copied out once and never over a user's folder;
// and an action runs off the event thread, one at a time per plugin.

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// plugUseDir points the loader at a scratch folder and puts the loader's state
// back afterwards.
func plugUseDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prevDir, prevPlugins, prevCatalog, prevSafe := pluginDir, plugins, plugCatalog, safeMode
	pluginDir, safeMode = dir, safeModeState{}
	t.Cleanup(func() {
		pluginDir, plugins, plugCatalog, safeMode = prevDir, prevPlugins, prevCatalog, prevSafe
	})
	return dir
}

// plugWriteTestPlugin writes one plugin folder: its manifest and a script.
func plugWriteTestPlugin(t *testing.T, dir, name, manifest string) {
	t.Helper()
	folder := filepath.Join(dir, name)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "run.sh"), []byte("#!/bin/sh\necho \"ran $1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// plugTestConfig is a config whose record says the bundled plugins were already
// copied out, so a scan of a scratch folder lists only that folder's plugins.
func plugTestConfig() *Config {
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{plugSeededKey: strings.Join(plugBundledNames(), ",")}
	return config
}

func TestPluginCatalogListsEveryFolder(t *testing.T) {
	dir := plugUseDir(t)
	plugWriteTestPlugin(t, dir, "alpha", `{"title":"Alpha","entry":"run.sh","enabled":true,"actions":[{"id":"go","label":"Go"},{"id":" "}]}`)
	plugWriteTestPlugin(t, dir, "beta", `{"title":"Beta","entry":"run.sh","enabled":false}`)
	plugWriteTestPlugin(t, dir, "gamma", `{not json`)
	LoadPlugins(plugTestConfig())

	entries := plugCatalogEntries()
	if len(entries) != 3 {
		t.Fatalf("the page lists %d plugins, want all three folders: %+v", len(entries), entries)
	}
	for i, want := range []string{"alpha", "beta", "gamma"} {
		if entries[i].Name != want {
			t.Fatalf("row %d is %q, want %q (the list is sorted by title)", i, entries[i].Name, want)
		}
	}
	alpha, _ := plugEntryNamed("alpha")
	if !alpha.Enabled || len(alpha.Actions) != 1 || alpha.Actions[0].Label != "Go" {
		t.Errorf("alpha = %+v: want it on, with its one real action", alpha)
	}
	if beta, _ := plugEntryNamed("beta"); beta.Enabled || beta.RunNote == "" {
		t.Errorf("beta = %+v: its manifest says off, so it is listed off and its buttons say why", beta)
	}
	if gamma, _ := plugEntryNamed("gamma"); gamma.Problem == "" || gamma.Title != "Gamma" ||
		!strings.HasPrefix(gamma.RunNote, "Can't run here: ") {
		t.Errorf("a broken manifest is listed as %+v: want its folder name and the reason it cannot run", gamma)
	}
	if _, loaded := GetPlugins()["alpha"]; !loaded {
		t.Error("a plugin that is on was not loaded for its buttons")
	}
	if _, loaded := GetPlugins()["beta"]; loaded {
		t.Error("a plugin that is off was loaded anyway")
	}
	if on, total := plugEnabledCount(); on != 1 || total != 3 {
		t.Errorf("the header would say %d of %d on, want 1 of 3", on, total)
	}
}

func TestPluginSwitchIsTheUsersAndSurvivesARescan(t *testing.T) {
	dir := plugUseDir(t)
	manifest := `{"title":"Beta","entry":"run.sh","enabled":false}`
	plugWriteTestPlugin(t, dir, "beta", manifest)
	config := plugTestConfig()
	LoadPlugins(config)

	plugSetEnabled(config, "beta", true)
	if e, _ := plugEntryNamed("beta"); !e.Enabled {
		t.Fatal("switching beta on did not take")
	}
	if _, loaded := GetPlugins()["beta"]; !loaded {
		t.Fatal("beta was switched on but not loaded, so its buttons would not run")
	}
	state, _ := config.Variables.Custom[plugStateKey].(map[string]interface{})
	if state["beta"] != true {
		t.Fatalf("the choice was stored as %v, want it in the user config", state)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "beta", "plugin.json")); err != nil || string(data) != manifest {
		t.Fatalf("the plugin's own manifest changed: %q (%v)", data, err)
	}
	LoadPlugins(config)
	if e, _ := plugEntryNamed("beta"); !e.Enabled {
		t.Fatal("a rescan forgot the user's switch")
	}
	plugSetEnabled(config, "beta", false)
	if _, loaded := GetPlugins()["beta"]; loaded {
		t.Fatal("switching beta off left it loaded")
	}
}

func TestBundledPluginsAreCopiedOutOnce(t *testing.T) {
	names := plugBundledNames()
	if len(names) < 8 {
		t.Fatalf("only %d plugins ship with JukaHub: %v", len(names), names)
	}
	dir := t.TempDir()
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}

	// A folder of the user's own under a bundled name is never overwritten.
	mine := filepath.Join(dir, names[0])
	if err := os.MkdirAll(mine, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "plugin.json"), []byte(`{"title":"Mine"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	wrote := plugSeedBundled(config, dir)
	if len(wrote) != len(names)-1 {
		t.Fatalf("copied %v, want every bundled plugin but the user's own %q", wrote, names[0])
	}
	for _, name := range names[1:] {
		if _, err := os.Stat(filepath.Join(dir, name, "plugin.json")); err != nil {
			t.Errorf("%s was not copied out: %v", name, err)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(mine, "plugin.json")); string(data) != `{"title":"Mine"}` {
		t.Errorf("the user's own %s was overwritten with %q", names[0], data)
	}
	if again := plugSeedBundled(config, dir); len(again) != 0 {
		t.Errorf("a second scan copied %v again", again)
	}

	// One the user deleted stays deleted.
	if err := os.RemoveAll(filepath.Join(dir, names[1])); err != nil {
		t.Fatal(err)
	}
	if again := plugSeedBundled(config, dir); len(again) != 0 {
		t.Errorf("a deleted bundled plugin came back: %v", again)
	}
	if _, err := os.Stat(filepath.Join(dir, names[1])); !os.IsNotExist(err) {
		t.Errorf("%s is back on disk (%v)", names[1], err)
	}

	// A bare config has nowhere to remember the copy, so it copies nothing.
	if got := plugSeedBundled(&Config{}, t.TempDir()); got != nil {
		t.Errorf("a config without a custom block seeded %v", got)
	}
}

// TestBundledPluginsAreWellFormed reads each shipped manifest the way the
// loader does: a title, a description, a known icon, an .sh entry that is in
// the bundle, and buttons whose action ids the script actually handles.
func TestBundledPluginsAreWellFormed(t *testing.T) {
	for _, name := range plugBundledNames() {
		root := path.Join("plugins", name)
		data, err := fs.ReadFile(plugBundleFS, path.Join(root, "plugin.json"))
		if err != nil {
			t.Errorf("%s: no manifest in the bundle: %v", name, err)
			continue
		}
		var info PluginInfo
		if err := json.Unmarshal(data, &info); err != nil {
			t.Errorf("%s: plugin.json does not parse: %v", name, err)
			continue
		}
		if strings.TrimSpace(info.Title) == "" || strings.TrimSpace(info.Description) == "" {
			t.Errorf("%s: a shipped plugin needs a title and a description", name)
		}
		if !iconKnown(info.Icon) {
			t.Errorf("%s: icon %q is not one of the shared glyphs", name, info.Icon)
		}
		if !info.Enabled {
			t.Errorf("%s: a shipped plugin starts switched on", name)
		}
		if !strings.HasSuffix(info.Entry, ".sh") {
			t.Errorf("%s: entry %q is not a shell script", name, info.Entry)
			continue
		}
		script, err := fs.ReadFile(plugBundleFS, path.Join(root, info.Entry))
		if err != nil {
			t.Errorf("%s: the entry %q is not in the bundle: %v", name, info.Entry, err)
			continue
		}
		if len(info.Actions) == 0 {
			t.Errorf("%s: no buttons for the Plugins page", name)
		}
		seen := map[string]bool{}
		for _, a := range info.Actions {
			if a.ID == "" || a.Label == "" || seen[a.ID] {
				t.Errorf("%s: action %+v is unnamed, unlabelled or repeated", name, a)
			}
			seen[a.ID] = true
			if !strings.Contains(string(script), a.ID+")") {
				t.Errorf("%s: the script has no case for the %q button", name, a.ID)
			}
		}
	}
}

// TestPluginActionRunsOffTheEventThread: an action runs on a goroutine, a
// second press while it runs is refused rather than queued, and what it printed
// lands on the page.
func TestPluginActionRunsOffTheEventThread(t *testing.T) {
	release := make(chan struct{})
	prevRunner := plugRunner
	plugRunner = func(info PluginInfo, action string, vars map[string]interface{}) string {
		<-release
		return "  ran " + action + " for " + info.Name + "\n"
	}
	t.Cleanup(func() { plugRunner = prevRunner })

	info := PluginInfo{Name: "plugtest-runner", Entry: "run.sh"}
	if !plugRunAsync(info, "go", "Runner", nil) {
		t.Fatal("the first press was refused")
	}
	if running, _, _ := plugRunStatus(info.Name); running != "go" {
		t.Fatalf("the page shows %q running, want the action", running)
	}
	if plugRunAsync(info, "go", "Runner", nil) {
		t.Fatal("a second press while the action runs was accepted")
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !plugWaitIdle(ctx) {
		t.Fatal("the action never finished")
	}
	running, output, at := plugRunStatus(info.Name)
	if running != "" || output != "ran go for plugtest-runner" || at.IsZero() {
		t.Fatalf("after the run: running %q, output %q, at %v", running, output, at)
	}
}
