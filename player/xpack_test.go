package main

// xpack_test.go — experience packs: the built-ins are valid, validation drops
// exactly the parts a pack may not use, applying is undone completely, a pack's
// screens follow the pack in force without shifting the scene list, Quick
// Settings steps through the packs, and a pack survives an export and import
// while an archive that tries to escape its folder is refused.

import (
	"archive/zip"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// xpackTestIsolate gives a test its own packs folder and puts back everything
// applying a pack touches: the pack store, the screens packs added, the theme
// catalogue and palette, the interface sounds, the Quick Settings label and the
// control-profile hooks (which do nothing here).
func xpackTestIsolate(t *testing.T) {
	t.Helper()
	snapshotThemeState(t)
	palette := capturePalette()
	muted, level, levelSet := bootSfxMuted.Load(), bootSfxLevel.Load(), bootSfxLevelSet.Load()
	prevDir := xpackDirOverride
	xpackDirOverride = t.TempDir()
	xpackStore.mu.Lock()
	user, plugin, loaded := xpackStore.user, xpackStore.plugin, xpackStore.loaded
	problems, scanned := xpackStore.problems, xpackStore.scanned
	xpackStore.user, xpackStore.plugin, xpackStore.loaded, xpackStore.problems = nil, nil, nil, nil
	// Marked scanned, so nothing here starts a background scan of its own.
	xpackStore.scanned = true
	xpackStore.gen++
	xpackStore.mu.Unlock()
	injected := xpackInjectedScenes
	xpackInjectedScenes = map[string]string{}
	label, pins := xpackLabelCache, xpackPinCache
	activate, current := xpackActivateInputProfile, xpackCurrentInputProfile
	xpackActivateInputProfile = func(*Config, string) error { return nil }
	xpackCurrentInputProfile = func(*Config) string { return "" }
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, scanning, _ := xpackScanState(); !scanning {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		xpackStore.mu.Lock()
		xpackStore.user, xpackStore.plugin, xpackStore.loaded = user, plugin, loaded
		xpackStore.problems, xpackStore.scanned = problems, scanned
		xpackStore.gen++
		xpackStore.mu.Unlock()
		xpackInjectedScenes = injected
		xpackLabelCache, xpackPinCache = label, pins
		xpackActivateInputProfile, xpackCurrentInputProfile = activate, current
		xpackDirOverride = prevDir
		bootSfxMuted.Store(muted)
		bootSfxLevel.Store(level)
		bootSfxLevelSet.Store(levelSet)
		restorePalette(palette)
	})
}

// xpackTestSnapshot is everything a pack can change, for comparing a setup
// before a pack and after its undo.
type xpackTestSnapshot struct {
	Keys                 map[string]interface{}
	Button, Label, Input RGB
	Wallpaper            string
}

func xpackTestState(config *Config) xpackTestSnapshot {
	s := xpackTestSnapshot{
		Keys:      map[string]interface{}{},
		Button:    config.Variables.ButtonColor,
		Label:     config.Variables.LabelColor,
		Input:     config.Variables.InputColor,
		Wallpaper: config.Variables.BackgroundImage,
	}
	for _, k := range xpackStateKeys() {
		if v, ok := config.Variables.Custom[k]; ok {
			s.Keys[k] = v
		}
	}
	return s
}

// xpackTestZip writes a zip archive of files at path.
func xpackTestZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestXpackBuiltinPacksAreValid(t *testing.T) {
	want := []string{"default", "retro-arcade", "movie-night", "focus-reading", "night-owl"}
	got := xpackBuiltinEntries()
	if len(got) != len(want) {
		t.Fatalf("%d built-in packs, want %d", len(got), len(want))
	}
	for i, e := range got {
		m := e.Manifest
		if m.ID != want[i] {
			t.Errorf("pack %d is %q, want %q", i, m.ID, want[i])
		}
		if len(e.Problems) != 0 {
			t.Errorf("%s: %v", m.ID, e.Problems)
		}
		if m.Name == "" || m.Description == "" {
			t.Errorf("%s has no name or description", m.ID)
		}
		if m.Theme != xpackThemeDefault {
			if _, ok := themePresets[m.Theme]; !ok {
				t.Errorf("%s names theme %q, which is not a preset", m.ID, m.Theme)
			}
		}
		if m.Sounds == nil || m.Screensaver == "" || m.HomeLayout == "" {
			t.Errorf("%s leaves part of the setup as it was: %+v", m.ID, m)
		}
	}
}

func TestXpackValidateDropsWhatCannotApply(t *testing.T) {
	dir := t.TempDir()
	plugxTestWrite(t, dir, map[string]string{
		"wall.png":  "png",
		"own.json":  validThemeJSON,
		"notes.txt": "hello",
	})
	raw := `{
	  "id": "My Pack!", "name": "Mine", "theme": "nord", "themeFile": "own.json",
	  "homeLayout": "Sideways", "wallpaper": "wall.png",
	  "sounds": {"enabled": false, "level": 500},
	  "pinned": ["Games", "Games", " Tube "],
	  "screensaver": "fireworks",
	  "favorites": [
	    {"type": "iptv", "name": "News", "path": "https://example.com/live.m3u8"},
	    {"type": "iptv", "name": "Bad", "path": "file:///etc/passwd"},
	    {"type": "file", "name": "Notes", "path": "notes.txt"},
	    {"type": "file", "name": "Escape", "path": "../../secret"},
	    {"type": "app", "name": "Nope", "path": "x"}
	  ],
	  "scenes": [
	    {"name": "Corner", "elements": [{"type": "label", "text": "Hi $groq_api_key"},
	      {"type": "button", "text": "Go", "trigger": "change_scene:Games"}]},
	    {"name": "Evil", "elements": [{"type": "button", "text": "Run", "trigger": "terminal_run"}]},
	    {"name": "Sneaky", "elements": [{"type": "input", "variable": "groq_api_key"}]}
	  ]
	}`
	m, err := xpackParseManifest([]byte(raw), "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "my-pack" {
		t.Errorf("id = %q, want the slug my-pack", m.ID)
	}
	e := xpackEntry{Manifest: m, Source: xpackSourceUser, Dir: dir}
	xpackValidate(&e)

	if e.ThemePreset == nil || e.ThemePreset.Name != "Test Theme" {
		t.Error("the pack's own theme file was not loaded")
	}
	if e.Manifest.HomeLayout != "" || e.Manifest.Screensaver != "" {
		t.Errorf("an unknown layout or idle screen was kept: %q %q", e.Manifest.HomeLayout, e.Manifest.Screensaver)
	}
	if filepath.Base(e.WallpaperPath) != "wall.png" || !filepath.IsAbs(e.WallpaperPath) {
		t.Errorf("wallpaper = %q, want the pack's file as an absolute path", e.WallpaperPath)
	}
	if s := e.Manifest.Sounds; s == nil || s.Level != 100 || s.Enabled == nil || *s.Enabled {
		t.Errorf("sounds = %+v, want off at a clamped 100%%", s)
	}
	if got := strings.Join(e.Manifest.Pinned, ","); got != "Games,Tube" {
		t.Errorf("pinned = %q, want the names trimmed and de-duplicated", got)
	}
	if len(e.Manifest.Favorites) != 2 {
		t.Errorf("favourites = %+v, want the stream and the pack's own file", e.Manifest.Favorites)
	}
	if len(e.Scenes) != 1 || e.Scenes[0].Name != "Corner" {
		t.Fatalf("screens = %+v, want only the safe one", e.Scenes)
	}
	if text := e.Scenes[0].Elements[0].Text; strings.Contains(text, "groq_api_key") {
		t.Errorf("a secret variable survived in %q", text)
	}
	if target := e.Scenes[0].Elements[1].TriggerTarget; target != "Games" {
		t.Errorf("change_scene target = %q, want Games", target)
	}
	if e.Scenes[0].Icon == "" {
		t.Error("a pack screen without a monogram would be unreachable from the Start menu")
	}
	if len(e.Problems) != 7 {
		t.Errorf("problems = %v, want each of the seven dropped parts named", e.Problems)
	}
	assets := strings.Join(e.Assets, ",")
	for _, want := range []string{"own.json", "wall.png", "notes.txt"} {
		if !strings.Contains(assets, want) {
			t.Errorf("assets %v lack %s", e.Assets, want)
		}
	}
	if _, err := xpackParseManifest([]byte(`{"schema": 99, "id": "future"}`), ""); err == nil {
		t.Error("a pack made for a newer schema was accepted")
	}
}

func TestXpackApplyThenUndoPutsEverythingBack(t *testing.T) {
	xpackTestIsolate(t)
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{
		"homeArrangement": homeArrangementDashboard,
		bootSfxEnabledKey: "true",
		bootSfxVolumeKey:  "80",
	}
	config.Variables.BackgroundImage = "my-wallpaper.jpg"
	config.Variables.ButtonColor = RGB{R: 1, G: 2, B: 3}
	config.Variables.LabelColor = RGB{R: 4, G: 5, B: 6}
	config.Variables.InputColor = RGB{R: 7, G: 8, B: 9}
	before := xpackTestState(config)

	var switched []string
	xpackActivateInputProfile = func(_ *Config, name string) error {
		switched = append(switched, name)
		return nil
	}
	xpackCurrentInputProfile = func(*Config) string { return "Left-handed" }

	retro, ok := xpackFind("retro-arcade")
	if !ok {
		t.Fatal("no Retro Arcade pack")
	}
	retro.Manifest.InputProfile = "Gaming"
	note, err := xpackApply(config, retro)
	if err != nil || note != "" {
		t.Fatalf("apply: note %q, err %v", note, err)
	}
	if got := themePresetName(config); got != "synthwave" {
		t.Errorf("theme = %q, want synthwave", got)
	}
	if got := homeArrangement(config); got != homeArrangementGrid {
		t.Errorf("Home arrangement = %q, want the grid", got)
	}
	if xpackActiveID(config) != "retro-arcade" || xpackQSLabel() != "Retro Arcade" {
		t.Errorf("pack in force = %q, label %q", xpackActiveID(config), xpackQSLabel())
	}
	if config.Variables.Custom[xpackStartSceneKey] != "Games" {
		t.Errorf("start screen = %v, want Games", config.Variables.Custom[xpackStartSceneKey])
	}
	if config.Variables.BackgroundImage != xpackDefaultWallpaper {
		t.Errorf("wallpaper = %q, want Home's own backdrop", config.Variables.BackgroundImage)
	}
	if got := strings.Join(xpackPinnedScenes(), ","); got != "Games,MusicPlayer,Playtime" {
		t.Errorf("pinned screens = %q", got)
	}
	if len(switched) != 1 || switched[0] != "Gaming" {
		t.Errorf("control profiles switched to %v, want Gaming", switched)
	}

	name, err := xpackUndo(config)
	if err != nil || name != "Retro Arcade" {
		t.Fatalf("undo: %q %v", name, err)
	}
	if after := xpackTestState(config); !reflect.DeepEqual(after, before) {
		t.Errorf("undo left\n%+v\nwant\n%+v", after, before)
	}
	if len(switched) != 2 || switched[1] != "Left-handed" {
		t.Errorf("control profiles switched to %v, want the previous one back", switched)
	}
	if xpackActiveID(config) != "" || xpackQSLabel() != "None" || len(xpackPinnedScenes()) != 0 {
		t.Errorf("after undo the pack in force is %q (%q)", xpackActiveID(config), xpackQSLabel())
	}
	if _, again := xpackUndoLabel(config); again {
		t.Error("undo can be pressed twice")
	}
	if _, err := xpackUndo(config); err == nil {
		t.Error("a second undo did something")
	}
}

func TestXpackScreensFollowThePackInForce(t *testing.T) {
	xpackTestIsolate(t)
	corner := xpackEntry{
		Manifest: xpackManifest{Schema: xpackSchema, ID: "cornerpack", Name: "Corner pack", Scenes: []json.RawMessage{
			json.RawMessage(`{"name": "Corner", "elements": [{"type": "button", "text": "Games", "trigger": "change_scene:Games"}]}`),
		}},
		Source: xpackSourceUser,
		Dir:    t.TempDir(),
	}
	xpackValidate(&corner)
	clash := xpackEntry{
		Manifest: xpackManifest{Schema: xpackSchema, ID: "clash", Name: "Clash", Scenes: []json.RawMessage{
			json.RawMessage(`{"name": "Games", "elements": [{"type": "label", "text": "mine now"}]}`),
		}},
		Source: xpackSourceUser,
		Dir:    t.TempDir(),
	}
	xpackValidate(&clash)
	if len(corner.Scenes) != 1 || len(clash.Scenes) != 1 {
		t.Fatalf("screens did not validate: %v %v", corner.Problems, clash.Problems)
	}
	xpackStore.mu.Lock()
	xpackStore.user = []xpackEntry{corner, clash}
	xpackStore.mu.Unlock()
	config := &Config{Scenes: []SceneConfig{{Name: "Main", Icon: "H"}, {Name: "Games", Icon: "G"}}}
	config.Variables.Custom = map[string]interface{}{}

	if _, err := xpackApply(config, corner); err != nil {
		t.Fatal(err)
	}
	idx := sceneIndexExact(config, "Corner")
	if idx < 0 || config.Scenes[idx].Icon == "" {
		t.Fatalf("the pack's screen is not in the Start menu: %+v", config.Scenes)
	}

	// Another pack takes over: the screen stays where it is (the scene list is
	// index-addressed) but leaves the Start menu.
	night, ok := xpackFind("night-owl")
	if !ok {
		t.Fatal("no Night Owl pack")
	}
	if _, err := xpackApply(config, night); err != nil {
		t.Fatal(err)
	}
	if sceneIndexExact(config, "Corner") != idx || config.Scenes[idx].Icon != "" {
		t.Errorf("the screen of a pack no longer in force is %+v", config.Scenes[idx])
	}

	// Undo brings the first pack back, and its screen with it.
	if _, err := xpackUndo(config); err != nil {
		t.Fatal(err)
	}
	if config.Scenes[idx].Icon == "" {
		t.Error("undo did not bring the pack's screen back to the Start menu")
	}

	// A pack screen never takes over a screen the config has of its own.
	if _, err := xpackApply(config, clash); err != nil {
		t.Fatal(err)
	}
	games := config.Scenes[sceneIndexExact(config, "Games")]
	if len(games.Elements) != 0 || games.Icon != "G" {
		t.Errorf("a pack replaced the config's own Games screen: %+v", games)
	}
	if len(config.Scenes) != 3 {
		t.Errorf("%d scenes, want the config's two and the one pack screen", len(config.Scenes))
	}
}

func TestXpackQuickSettingsStepsThroughThePacks(t *testing.T) {
	xpackTestIsolate(t)
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	entries := xpackEntries()
	steps := []struct {
		dir  int
		want string
	}{
		{1, entries[0].Manifest.ID},
		{1, entries[1].Manifest.ID},
		{-1, entries[0].Manifest.ID},
		{-1, entries[len(entries)-1].Manifest.ID},
	}
	for i, s := range steps {
		xpackQSStep(config, s.dir)
		if got := xpackActiveID(config); got != s.want {
			t.Fatalf("step %d (%+d): pack %q, want %q", i, s.dir, got, s.want)
		}
	}
	if xpackQSLabel() != entries[len(entries)-1].Manifest.Name {
		t.Errorf("Quick Settings shows %q", xpackQSLabel())
	}
}

func TestXpackExportImportRoundTrip(t *testing.T) {
	xpackTestIsolate(t)
	src := t.TempDir()
	plugxTestWrite(t, src, map[string]string{
		"pack.json":    `{"id": "trip", "name": "Round trip", "wallpaper": "art/wall.png", "themeFile": "look.json"}`,
		"art/wall.png": "PNG",
		"look.json":    validThemeJSON,
		"unused.txt":   "left behind",
	})
	e, err := xpackLoadEntry(src, xpackSourceUser)
	if err != nil || len(e.Problems) != 0 {
		t.Fatalf("load: %v %v", err, e.Problems)
	}
	archive, err := xpackExport(e, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(archive) != "trip"+xpackArchiveExt {
		t.Errorf("exported as %s", filepath.Base(archive))
	}
	m, err := xpackArchiveManifest(archive)
	if err != nil || m.Name != "Round trip" {
		t.Fatalf("the archive's manifest: %+v %v", m, err)
	}

	got, err := xpackImport(archive)
	if err != nil {
		t.Fatal(err)
	}
	if got.Manifest.Name != "Round trip" || got.ThemePreset == nil || got.WallpaperPath == "" {
		t.Errorf("imported %+v", got)
	}
	if want := filepath.Join(xpackDir(), "trip"); got.Dir != want {
		t.Errorf("imported into %s, want %s", got.Dir, want)
	}
	if _, err := os.Stat(filepath.Join(got.Dir, "unused.txt")); !os.IsNotExist(err) {
		t.Error("a file the pack does not use was exported with it")
	}

	// A pack that reuses a built-in's id, and one whose names escape the pack
	// folder, are refused without writing anything.
	bad := t.TempDir()
	builtin := filepath.Join(bad, "builtin"+xpackArchiveExt)
	xpackTestZip(t, builtin, map[string]string{"pack.json": `{"id": "retro-arcade", "name": "Fake"}`})
	escape := filepath.Join(bad, "escape"+xpackArchiveExt)
	xpackTestZip(t, escape, map[string]string{"pack.json": `{"id": "escape", "name": "Escape"}`, "../evil.txt": "x"})
	for _, path := range []string{builtin, escape, filepath.Join(bad, "not-a-pack.zip")} {
		if _, err := xpackImport(path); err == nil {
			t.Errorf("%s was imported", filepath.Base(path))
		}
	}
	if _, err := os.Stat(filepath.Join(xpackDir(), "escape")); !os.IsNotExist(err) {
		t.Error("a refused archive left a pack folder behind")
	}
	if _, err := os.Stat(filepath.Join(xpackDir(), "..", "evil.txt")); !os.IsNotExist(err) {
		t.Error("an archive wrote outside the packs folder")
	}
}

func TestXpackZipEntryNamesStayInsideThePack(t *testing.T) {
	cases := []struct {
		name, prefix, want string
		bad                bool
	}{
		{"pack.json", "", "pack.json", false},
		{"art/wall.png", "", "art/wall.png", false},
		{"mypack/art/wall.png", "mypack/", "art/wall.png", false},
		{"other/file.txt", "mypack/", "", false},
		{"art/", "", "", false},
		{"../evil", "", "", true},
		{"/etc/passwd", "", "", true},
		{`a\..\b`, "", "", true},
		{"C:/x", "", "", true},
		{"a/../../b", "", "", true},
	}
	for _, c := range cases {
		got, err := xpackZipEntryName(c.name, c.prefix)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("xpackZipEntryName(%q, %q) = %q, %v; want %q, refused %v", c.name, c.prefix, got, err, c.want, c.bad)
		}
	}
}

// TestXpackSceneTriggersAreDispatched: every trigger a pack screen may use is
// one the app actually handles, so an allowed button never does nothing.
func TestXpackSceneTriggersAreDispatched(t *testing.T) {
	handled := triggerActionsFromSource(t)
	for trigger := range xpackSceneTriggers {
		if !handled[trigger] {
			t.Errorf("pack screens may use %q, which handleTrigger does not dispatch", trigger)
		}
	}
}

// TestXpackSamplePluginPackIsClean: the pack the sample plugin ships names a
// shipped theme and validates without dropping anything.
func TestXpackSamplePluginPackIsClean(t *testing.T) {
	data, err := fs.ReadFile(plugBundleFS, "plugins/classicpack/pocket-classic.json")
	if err != nil {
		t.Fatal(err)
	}
	m, err := xpackParseManifest(data, "")
	if err != nil {
		t.Fatal(err)
	}
	e := xpackEntry{Manifest: m, Source: xpackSourcePlugin + "classicpack", Dir: t.TempDir()}
	xpackValidate(&e)
	if len(e.Problems) != 0 || len(e.Scenes) != 1 {
		t.Errorf("the sample pack lost %v (screens %d)", e.Problems, len(e.Scenes))
	}
	if _, ok := themePresets[m.Theme]; !ok {
		t.Errorf("the sample pack names theme %q, which is not shipped", m.Theme)
	}
	for _, b := range xpackBuiltinEntries() {
		if b.Manifest.ID == m.ID {
			t.Errorf("the sample pack reuses the built-in id %q", m.ID)
		}
	}
}
