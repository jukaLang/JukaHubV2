package main

// plugx_test.go — the plugin extension points: a manifest written before them
// loads exactly as it did, every typed entry is validated on its own, ROM
// formats are matched by extension and header, a plugin's theme comes and goes
// with its switch, control profiles are handed over once, and every sample
// plugin that ships declares its extensions cleanly.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plugxTestWrite writes files (slash names, folders made) under dir.
func plugxTestWrite(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// plugxTestIsolate puts back everything a plugin scan installs: the theme
// catalogue, the ROM formats, the theme owners, the registered profiles, the
// plugin packs and the Quick Settings label. Profiles are recorded rather than
// handed to the input profiles, and every script counts as runnable.
func plugxTestIsolate(t *testing.T) {
	t.Helper()
	snapshotThemeState(t)
	plugxRegistry.mu.Lock()
	parsers := plugxRegistry.parsers
	plugxRegistry.mu.Unlock()
	owners, registered := plugxThemeOwners, plugxInputRegistered
	plugxThemeOwners, plugxInputRegistered = map[string]string{}, map[string]bool{}
	register := plugxRegisterProfile
	plugxRegisterProfile = func(string) error { return nil }
	xpackStore.mu.Lock()
	pluginPacks := xpackStore.plugin
	xpackStore.mu.Unlock()
	label, pins := xpackLabelCache, xpackPinCache
	lookPath := plugLookPath
	plugLookPath = func(string) (string, error) { return "sh", nil }
	t.Cleanup(func() {
		plugxRegistry.mu.Lock()
		plugxRegistry.parsers = parsers
		plugxRegistry.mu.Unlock()
		plugxThemeOwners, plugxInputRegistered = owners, registered
		plugxRegisterProfile = register
		xpackStore.mu.Lock()
		xpackStore.plugin = pluginPacks
		xpackStore.mu.Unlock()
		xpackLabelCache, xpackPinCache = label, pins
		plugLookPath = lookPath
	})
}

func TestPlugxOldManifestLoadsUnchanged(t *testing.T) {
	dir := plugUseDir(t)
	plugxTestIsolate(t)
	manifest := `{"title":"Old","entry":"run.sh","enabled":true,"actions":[{"id":"go","label":"Go"}]}`
	plugWriteTestPlugin(t, dir, "old", manifest)
	LoadPlugins(plugTestConfig())

	row, ok := plugEntryNamed("old")
	if !ok || !row.Enabled || row.Problem != "" || row.RunNote != "" {
		t.Fatalf("a manifest without extensions is listed as %+v, want it on and runnable", row)
	}
	if row.Provides != "" || len(row.ExtLines) != 0 {
		t.Errorf("a manifest without extensions claims to add %q %v", row.Provides, row.ExtLines)
	}
	if _, loaded := GetPlugins()["old"]; !loaded {
		t.Error("the plugin was not loaded for its buttons")
	}
	ext := plugxParse("old", filepath.Join(dir, "old"), []byte(manifest))
	if ext.hasAny() || len(ext.Problems) != 0 {
		t.Errorf("an old manifest declared %+v", ext)
	}
	// A manifest that is not JSON adds nothing and breaks nothing.
	if broken := plugxParse("broken", dir, []byte(`{not json`)); broken.hasAny() || len(broken.Problems) != 0 {
		t.Errorf("a broken manifest declared %+v", broken)
	}
}

func TestPlugxParsesEachTypedEntryOnItsOwn(t *testing.T) {
	dir := t.TempDir()
	plugxTestWrite(t, dir, map[string]string{
		"theme.json":        validThemeJSON,
		"broken-theme.json": `{"name": "Broken"}`,
		"pad.jkinput":       "profile: Pad\nmap:\n  a -> button:b\n",
		"pad.json":          "{}",
		"packdir/pack.json": `{"id": "folder-pack", "name": "Folder pack"}`,
		"single.json":       `{"id": "single-pack", "name": "Single pack"}`,
		"archive.jkpack":    "not a zip",
	})
	manifest := `{
	  "title": "Kit", "entry": "run.sh",
	  "romParsers": [
	    {"name": "Pocket", "system": "pocket", "extensions": ["pk", ".PKB"], "magic": "50 4B", "offset": 2},
	    {"name": "No ext", "system": "noext", "extensions": []},
	    {"name": "Bad system", "system": "Bad System!", "extensions": [".x"]},
	    {"name": "Bad magic", "system": "badmagic", "extensions": [".bm"], "magic": "zz"}
	  ],
	  "themes": ["theme.json", "broken-theme.json", "../outside.json", "missing.json"],
	  "inputProfiles": ["pad.jkinput", "pad.json"],
	  "packs": ["packdir", "single.json", "archive.jkpack"]
	}`
	ext := plugxParse("kit", dir, []byte(manifest))

	if len(ext.RomParsers) != 1 {
		t.Fatalf("ROM formats = %+v, want only the valid one", ext.RomParsers)
	}
	p := ext.RomParsers[0]
	if p.Plugin != "kit" || p.System != "pocket" || strings.Join(p.Extensions, ",") != ".pk,.pkb" ||
		string(p.Magic) != "PK" || p.Offset != 2 {
		t.Errorf("parser = %+v", p)
	}
	if len(ext.Themes) != 1 || ext.Themes[0].Stem != "theme" || ext.Themes[0].Preset.Name != "Test Theme" {
		t.Errorf("themes = %+v, want the valid file only", ext.Themes)
	}
	if len(ext.InputProfiles) != 1 || filepath.Base(ext.InputProfiles[0]) != "pad.jkinput" {
		t.Errorf("control profiles = %v, want pad.jkinput only", ext.InputProfiles)
	}
	if len(ext.Packs) != 2 {
		t.Errorf("packs = %v, want the folder and the manifest", ext.Packs)
	}
	// Every refused entry is named, so the Plugins page can say why.
	for _, want := range []string{"No ext", "Bad system", "Bad magic", "broken-theme.json", "../outside.json",
		"missing.json", "pad.json", "archive.jkpack"} {
		found := false
		for _, problem := range ext.Problems {
			found = found || strings.Contains(problem, want)
		}
		if !found {
			t.Errorf("no problem names %q: %v", want, ext.Problems)
		}
	}
	provides, lines := plugxDescribe(ext)
	for _, want := range []string{"1 theme", "1 ROM format", "1 control profile", "2 experience packs"} {
		if !strings.Contains(provides, want) {
			t.Errorf("summary %q lacks %q", provides, want)
		}
	}
	skipped := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "Skipped ") {
			skipped++
		}
	}
	if skipped != len(ext.Problems) || len(lines) != skipped+5 {
		t.Errorf("card lines = %v", lines)
	}
}

func TestPlugxRomParserMatchesExtensionThenHeader(t *testing.T) {
	plugxTestIsolate(t)
	plugxRegistry.mu.Lock()
	plugxRegistry.parsers = []plugxRomParser{
		{Plugin: "a", Name: "Plain", System: "plain", Extensions: []string{".bin"}},
		{Plugin: "b", Name: "Signed", System: "signed", Extensions: []string{".bin"}, Magic: []byte("SIG"), Offset: 4},
		{Plugin: "c", Name: "Other", System: "other", Extensions: []string{".oth"}},
	}
	plugxRegistry.mu.Unlock()
	dir := t.TempDir()
	plugxTestWrite(t, dir, map[string]string{
		"signed.bin": "1234SIGrest",
		"plain.bin":  "1234XYZrest",
		"short.bin":  "12",
		"game.OTH":   "anything",
		"none.zip":   "PK",
	})
	for name, want := range map[string]string{
		"signed.bin": "signed", "plain.bin": "plain", "short.bin": "plain", "game.OTH": "other", "none.zip": "",
	} {
		got := ""
		if p, ok := plugxRomParserFor(filepath.Join(dir, name)); ok {
			got = p.System
		}
		if got != want {
			t.Errorf("%s: matched %q, want %q", name, got, want)
		}
	}
	list := plugxRomParsers()
	list[0].Name = "changed"
	if plugxRomParsers()[0].Name == "changed" {
		t.Error("plugxRomParsers handed out the registry itself")
	}
}

func TestPlugxThemeComesAndGoesWithItsPlugin(t *testing.T) {
	plugxTestIsolate(t)
	palette := capturePalette()
	t.Cleanup(func() { restorePalette(palette) })
	dir := t.TempDir()
	plugxTestWrite(t, dir, map[string]string{"glow.json": validThemeJSON})
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	theme, err := plugxLoadTheme(dir, "glow.json")
	if err != nil {
		t.Fatal(err)
	}

	plugxSyncThemes(config, []plugxExtensions{{Plugin: "glowkit", Dir: dir, Themes: []plugxTheme{theme}}})
	if _, ok := themePresets["glow"]; !ok || plugxThemeOwners["glow"] != "glowkit" {
		t.Fatalf("the plugin's theme was not registered (owners %v)", plugxThemeOwners)
	}

	// A theme whose id is a built-in's is listed under the plugin's name and
	// never replaces the built-in.
	builtin := themePresetOrder[0]
	before := themePresets[builtin]
	clash := plugxTheme{Stem: builtin, File: theme.File, Preset: theme.Preset}
	plugxSyncThemes(config, []plugxExtensions{{Plugin: "glowkit", Dir: dir, Themes: []plugxTheme{theme, clash}}})
	if _, ok := themePresets[plugxSlug("glowkit-"+builtin)]; !ok {
		t.Errorf("a theme clashing with %q was not listed under the plugin's name", builtin)
	}
	if themePresets[builtin] != before {
		t.Errorf("a plugin theme replaced the built-in %q", builtin)
	}

	// Switched off: its themes leave the catalogue and the gallery order.
	plugxSyncThemes(config, nil)
	if _, ok := themePresets["glow"]; ok {
		t.Error("the theme of a plugin that was switched off is still listed")
	}
	for _, id := range themePresetOrder {
		if id == "glow" || id == plugxSlug("glowkit-"+builtin) {
			t.Errorf("the withdrawn theme %q is still in the gallery order", id)
		}
	}
}

func TestPlugxExtensionOnlyPluginIsNotBroken(t *testing.T) {
	dir := plugUseDir(t)
	plugxTestIsolate(t)
	plugxTestWrite(t, filepath.Join(dir, "looks"), map[string]string{
		"plugin.json": `{"title": "Looks", "enabled": true, "themes": ["dusk.json"]}`,
		"dusk.json":   validThemeJSON,
	})
	LoadPlugins(plugTestConfig())

	row, ok := plugEntryNamed("looks")
	if !ok || row.Problem != "" || row.Provides != "1 theme" {
		t.Fatalf("a theme-only plugin is listed as %+v, want no problem and what it adds", row)
	}
	if !strings.HasPrefix(row.RunNote, "Adds 1 theme") {
		t.Errorf("its card says %q, want what it adds", row.RunNote)
	}
	if _, ok := themePresets["dusk"]; !ok {
		t.Error("the plugin's theme is not in the catalogue after the scan")
	}
}

func TestPlugxControlProfilesAreHandedOverOnce(t *testing.T) {
	plugxTestIsolate(t)
	var got []string
	plugxRegisterProfile = func(path string) error {
		got = append(got, filepath.Base(path))
		if strings.Contains(path, "bad") {
			return errors.New("bad.jkinput: line 1: nothing usable\nsecond line")
		}
		return nil
	}
	found := []plugxExtensions{{Plugin: "pads", InputProfiles: []string{"/p/good.jkinput", "/p/bad.jkinput"}}}

	notes := plugxSyncInputProfiles(found)
	if len(got) != 2 || len(notes["pads"]) != 1 || !strings.Contains(notes["pads"][0], "bad.jkinput") {
		t.Fatalf("registered %v, notes %v", got, notes)
	}
	if strings.Contains(notes["pads"][0], "second line") {
		t.Errorf("the note carries more than the first line: %q", notes["pads"][0])
	}
	got = nil
	plugxSyncInputProfiles(found)
	if len(got) != 1 || got[0] != "bad.jkinput" {
		t.Errorf("a rescan registered %v, want only the file that was refused", got)
	}
}

// TestPlugxSamplePluginsAreClean: the plugins that ship with JukaHub show one
// of each extension point, and every entry they declare validates.
func TestPlugxSamplePluginsAreClean(t *testing.T) {
	root := t.TempDir()
	kinds := map[string]bool{}
	for _, name := range plugBundledNames() {
		dir := filepath.Join(root, name)
		if err := plugCopyBundled(name, dir); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
		if err != nil {
			t.Fatal(err)
		}
		ext := plugxParse(name, dir, data)
		if len(ext.Problems) != 0 {
			t.Errorf("%s: %v", name, ext.Problems)
		}
		kinds["romParsers"] = kinds["romParsers"] || len(ext.RomParsers) > 0
		kinds["themes"] = kinds["themes"] || len(ext.Themes) > 0
		kinds["inputProfiles"] = kinds["inputProfiles"] || len(ext.InputProfiles) > 0
		kinds["packs"] = kinds["packs"] || len(ext.Packs) > 0
		for _, th := range ext.Themes {
			if th.Preset.Kind == themeKindConversion {
				t.Errorf("%s: the sample theme %q is a total conversion; the conversion tab is the shipped set", name, th.Stem)
			}
		}
	}
	for _, kind := range []string{"romParsers", "themes", "inputProfiles", "packs"} {
		if !kinds[kind] {
			t.Errorf("no shipped plugin shows how to use %q", kind)
		}
	}
}

func TestPlugxSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Neon Grid":        "neon-grid",
		"  --Odd__Name!! ": "odd-name",
		"ÄÖÜ":              "",
		"a/b\\c":           "a-b-c",
	} {
		if got := plugxSlug(in); got != want {
			t.Errorf("plugxSlug(%q) = %q, want %q", in, got, want)
		}
	}
}
