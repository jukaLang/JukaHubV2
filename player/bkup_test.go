package main

// bkup_test.go — zero-config backup: a backup carries every chosen file with
// its checksum and nothing it should not, restores byte for byte onto an empty
// device after a safety backup, refuses an archive that was changed, damaged or
// that names an unknown place, translates folders between Windows and the
// handheld, merges the user config section by section and keeps the newest
// weekly backups.

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// bkupTestPlaces points every place a backup reads and writes at a scratch
// folder and returns it: app/ (the folder JukaHub runs in, with plugins/ and
// themes/), data/, packs/, saves/RetroArch, patch/ and out/ (exports).
func bkupTestPlaces(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"app", "data", "packs", "saves/RetroArch", "patch", "out"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prevApp, prevData, prevExport, prevSearch := bkupAppDirOverride, bkupDataDirOverride, bkupExportDirOverride, bkupSearchDirsOverride
	prevSaves, prevPatch, prevPacks := bkupSaveRoots, bkupPatchDir, xpackDirOverride
	prevPlugins, prevThemes := pluginDir, themeDir
	bkupAppDirOverride = filepath.Join(root, "app")
	bkupDataDirOverride = filepath.Join(root, "data")
	bkupExportDirOverride = filepath.Join(root, "out")
	bkupSearchDirsOverride = []string{filepath.Join(root, "out")}
	bkupSaveRoots = func() []saveRoot {
		return []saveRoot{{Name: "RetroArch", Path: filepath.Join(root, "saves", "RetroArch")}}
	}
	bkupPatchDir = func() (string, error) { return filepath.Join(root, "patch"), nil }
	xpackDirOverride = filepath.Join(root, "packs")
	pluginDir, themeDir = "plugins", "themes"
	t.Cleanup(func() {
		bkupAppDirOverride, bkupDataDirOverride, bkupExportDirOverride, bkupSearchDirsOverride = prevApp, prevData, prevExport, prevSearch
		bkupSaveRoots, bkupPatchDir, xpackDirOverride = prevSaves, prevPatch, prevPacks
		pluginDir, themeDir = prevPlugins, prevThemes
	})
	return root
}

// bkupTestSetup is a device's files, by slash path under the scratch root.
var bkupTestSetup = map[string]string{
	"app/jukauser.json":                 `{"variables": {"weatherUnit": "F"}}`,
	"app/jukatimezone":                  "Europe/Berlin\n",
	"app/jukaconfig.json":               `{"scenes": []}`,
	"app/notes.json":                    `{"notes": []}`,
	"app/themes/neon.json":              `{"name": "Neon"}`,
	"app/plugins/demo/plugin.json":      `{"title": "Demo"}`,
	"app/plugins/demo/run.sh":           "#!/bin/sh\necho hi\n",
	"app/plugins/demo/helper.bin":       "\x7fELF compiled for one system",
	"data/vk-words.json":                `{"words": []}`,
	"data/games.json":                   `{"games": {}}`,
	"data/weather-cache.json":           `{"cached": true}`,
	"data/browser-cookies.json":         `{"session": "secret"}`,
	"data/input-profiles/pad.jkinput":   "profile: Pad\n",
	"packs/mine/pack.json":              `{"id": "mine", "name": "Mine"}`,
	"saves/RetroArch/game.srm":          "SAVE",
	"saves/RetroArch/game.srm.tmp":      "partial",
	"patch/db/db.json":                  `{"installed": []}`,
	"app/plugins/demo/.hidden/skip.txt": "hidden",
}

// bkupTestTargets are the places the setup's files are stored under, and the
// files a backup must leave out.
var (
	bkupTestTargets = []string{
		"app:jukauser.json", "app:jukatimezone", "app:jukaconfig.json", "app:notes.json", "themes:neon.json",
		"plugins:demo/plugin.json", "plugins:demo/run.sh", "data:vk-words.json", "data:games.json",
		"input:pad.jkinput", "packs:mine/pack.json", "saves/retroarch:game.srm", "patch:db/db.json",
		"doc:" + cfgdocFileName,
	}
	bkupTestLeftOut = []string{
		"data:weather-cache.json", "data:browser-cookies.json", "plugins:demo/helper.bin",
		"saves/retroarch:game.srm.tmp", "plugins:demo/.hidden/skip.txt",
	}
)

// bkupTestAllCats is every category, saves included.
func bkupTestAllCats() map[string]bool {
	cats := map[string]bool{}
	for _, cat := range bkupCategoryOrder {
		cats[cat] = true
	}
	return cats
}

// bkupTestMake writes the setup and backs it up, returning the archive.
func bkupTestMake(t *testing.T, root string) (string, bkupManifest) {
	t.Helper()
	plugxTestWrite(t, root, bkupTestSetup)
	name := bkupFileName("manual", time.Date(2026, 11, 8, 9, 30, 0, 0, time.UTC))
	path, m, err := bkupCreate(context.Background(), filepath.Join(root, "out"), name, bkupTestAllCats(),
		bkupOptions{PluginFiles: true, Saves: true}, "manual", "v1.2.3", []byte(`{"schemaVersion": 1}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	return path, m
}

func TestBkupCarriesTheSetupAndNothingElse(t *testing.T) {
	root := bkupTestPlaces(t)
	path, m := bkupTestMake(t, root)

	if !strings.HasPrefix(filepath.Base(path), "JukaHub-backup-") || !strings.HasSuffix(path, bkupExt) {
		t.Errorf("backup named %s", filepath.Base(path))
	}
	got := map[string]bkupFile{}
	for _, f := range m.Files {
		got[f.Target] = f
	}
	for _, target := range bkupTestTargets {
		if _, ok := got[target]; !ok {
			t.Errorf("the backup lacks %s", target)
		}
	}
	for _, target := range bkupTestLeftOut {
		if _, ok := got[target]; ok {
			t.Errorf("the backup carries %s", target)
		}
	}
	if !got["app:jukaconfig.json"].Platform || got["app:jukauser.json"].Platform {
		t.Error("the design file must be marked as made for its platform, the user config must not")
	}
	if m.Format != bkupFormat || m.AppVersion != "v1.2.3" || m.Platform != P().Name() || m.Roots["data"] == "" {
		t.Errorf("manifest = %+v", m)
	}
	verified, err := bkupVerify(path)
	if err != nil {
		t.Fatalf("a fresh backup does not verify: %v", err)
	}
	if len(verified.Files) != len(m.Files) {
		t.Errorf("verified %d files, the backup lists %d", len(verified.Files), len(m.Files))
	}

	// Without saves and plugin scripts only the manifests come along.
	_, small, err := bkupCreate(context.Background(), filepath.Join(root, "out"), "small"+bkupExt, bkupTestAllCats(),
		bkupOptions{}, "", "v1.2.3", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range small.Files {
		if strings.HasPrefix(f.Target, "saves/") || f.Target == "plugins:demo/run.sh" || strings.HasPrefix(f.Target, "doc:") {
			t.Errorf("a backup without saves, scripts or a document carries %s", f.Target)
		}
	}
}

func TestBkupRestoreRebuildsAnEmptyDevice(t *testing.T) {
	root := bkupTestPlaces(t)
	path, m := bkupTestMake(t, root)
	for _, dir := range []string{"app", "data", "packs", "saves/RetroArch", "patch"} {
		full := filepath.Join(root, filepath.FromSlash(dir))
		if err := os.RemoveAll(full); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cats := bkupTestAllCats()
	plan := bkupPlanRestore(path, m, cats)
	res := bkupRestoreFiles(context.Background(), plan, cats, "v1.2.3", nil)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if !res.HasUser || string(res.UserData) != bkupTestSetup["app/jukauser.json"] {
		t.Errorf("the user config is handed to the main thread as %q", res.UserData)
	}
	if _, err := os.Stat(filepath.Join(root, "app", "jukauser.json")); !os.IsNotExist(err) {
		t.Error("the user config was written by the background half; the main thread merges it")
	}
	for rel, content := range bkupTestSetup {
		if rel == "app/jukauser.json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		left := false
		for _, out := range bkupTestLeftOut {
			left = left || strings.HasSuffix(rel, strings.SplitN(out, ":", 2)[1])
		}
		switch {
		case left && err == nil:
			t.Errorf("%s came back although the backup leaves it out", rel)
		case !left && (err != nil || string(data) != content):
			t.Errorf("%s restored as %q (%v), want %q", rel, data, err, content)
		}
	}
	if res.SafetyPath == "" || !strings.HasPrefix(filepath.Base(res.SafetyPath), "JukaHub-safety-") {
		t.Errorf("no safety backup was written first (%q)", res.SafetyPath)
	} else if _, err := bkupVerify(res.SafetyPath); err != nil {
		t.Errorf("the safety backup does not verify: %v", err)
	}
	if res.Written != len(m.Files)-2 {
		t.Errorf("wrote %d files, want every file but the user config and the document", res.Written)
	}
}

// bkupTestRewrite copies a backup, letting edit change, rename or drop entries.
func bkupTestRewrite(t *testing.T, src, dst string, edit func(name string, data []byte) (string, []byte, bool)) {
	t.Helper()
	zr, err := zip.OpenReader(src)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		name, data, keep := edit(f.Name, data)
		if !keep {
			continue
		}
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBkupRefusesAChangedBackup(t *testing.T) {
	root := bkupTestPlaces(t)
	path, _ := bkupTestMake(t, root)
	notesEntry := bkupEntryName("app:notes.json")
	editManifest := func(change func(m *bkupManifest)) func(string, []byte) (string, []byte, bool) {
		return func(name string, data []byte) (string, []byte, bool) {
			if name != bkupManifestName {
				return name, data, true
			}
			var m bkupManifest
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			change(&m)
			out, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			return name, out, true
		}
	}
	cases := map[string]func(string, []byte) (string, []byte, bool){
		"a file edited by hand": func(name string, data []byte) (string, []byte, bool) {
			if name == notesEntry {
				return name, []byte(`{"notes": ["sneaky"]}`), true
			}
			return name, data, true
		},
		"a file missing": func(name string, data []byte) (string, []byte, bool) {
			return name, data, name != notesEntry
		},
		"a file the manifest does not list": func(name string, data []byte) (string, []byte, bool) {
			if name == notesEntry {
				return "files/app/extra.sh", data, true
			}
			return name, data, true
		},
		"an unknown place": editManifest(func(m *bkupManifest) {
			for i := range m.Files {
				if m.Files[i].Path == notesEntry {
					m.Files[i].Target = "evil:notes.json"
				}
			}
		}),
		"a path that climbs out": editManifest(func(m *bkupManifest) {
			for i := range m.Files {
				if m.Files[i].Path == notesEntry {
					m.Files[i].Target = "app:../notes.json"
				}
			}
		}),
		"another app's archive": editManifest(func(m *bkupManifest) { m.Format = "something-else" }),
	}
	for label, edit := range cases {
		tampered := filepath.Join(t.TempDir(), "tampered"+bkupExt)
		bkupTestRewrite(t, path, tampered, edit)
		if _, err := bkupVerify(tampered); err == nil {
			t.Errorf("%s: the backup was accepted", label)
		}
	}
	if _, err := bkupVerify(filepath.Join(root, "out", "missing"+bkupExt)); err == nil {
		t.Error("a missing file verified")
	}
}

func TestBkupTimeZoneTravelsAndRestoresThroughOwner(t *testing.T) {
	root := bkupTestPlaces(t)
	path, m := bkupTestMake(t, root)
	found := false
	for _, f := range m.Files {
		if f.Target == bkupZoneTarget {
			found = true
		}
	}
	if !found {
		t.Fatalf("the backup does not carry %s", bkupZoneTarget)
	}

	if err := os.Remove(filepath.Join(root, "app", "jukatimezone")); err != nil {
		t.Fatal(err)
	}
	cats := bkupTestAllCats()
	res := bkupRestoreFiles(context.Background(), bkupPlanRestore(path, m, cats), cats, "v1.2.3", nil)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Zone != "Europe/Berlin" {
		t.Fatalf("restored zone = %q, want Europe/Berlin", res.Zone)
	}
	data, err := os.ReadFile(filepath.Join(root, "app", "jukatimezone"))
	if err != nil || strings.TrimSpace(string(data)) != "Europe/Berlin" {
		t.Fatalf("restored zone file = %q (%v)", data, err)
	}

	prevOwner, prevApply := bkupZoneOwner, bkupZoneApply
	t.Cleanup(func() { bkupZoneOwner, bkupZoneApply = prevOwner, prevApply })
	var applied string
	bkupZoneOwner = func() tzOwner { return tzOwnerJukaHub }
	bkupZoneApply = func(name string) error { applied = name; return nil }
	bkupApplyRestoredZone(res.Zone)
	if applied != "Europe/Berlin" {
		t.Fatalf("the handheld applied %q, want Europe/Berlin", applied)
	}

	applied = ""
	bkupZoneOwner = func() tzOwner { return tzOwnerWindows }
	bkupApplyRestoredZone(res.Zone)
	if applied != "" {
		t.Fatalf("a desktop applied the restored zone as %q", applied)
	}
}

func TestBkupTargetsNameKnownPlacesOnly(t *testing.T) {
	for _, target := range []string{"app:jukauser.json", "data:sub/x.json", "saves/retroarch:game.srm", "doc:jukahub-config.json"} {
		if _, _, err := bkupSplitTarget(target); err != nil {
			t.Errorf("%s was refused: %v", target, err)
		}
	}
	for _, target := range []string{"jukauser.json", "evil:x", "app:../x", "app:/etc/passwd", `app:a\b`,
		"saves/../x:y", "saves/a.b:c", "app:", "saves/:x"} {
		if _, _, err := bkupSplitTarget(target); err == nil {
			t.Errorf("%s was accepted", target)
		}
	}
}

func TestBkupTranslatesFoldersBetweenDevices(t *testing.T) {
	from := map[string]string{"data": `C:\Users\me\AppData\Roaming\JukaHub`, "app": `C:\JukaHub`, "home": `C:\Users\me`}
	to := map[string]string{"data": "/mnt/SDCARD/Apps/JukaHub/data", "app": "/mnt/SDCARD/Apps/JukaHub", "home": "/root", "card": "/mnt/SDCARD"}
	cases := []struct {
		in, want string
		ok       bool
	}{
		{`C:\JukaHub\themes\mine.json`, filepath.Join("/mnt/SDCARD/Apps/JukaHub", "themes", "mine.json"), true},
		{`c:\users\me\Music`, filepath.Join("/root", "Music"), true},
		{`C:\Users\me\AppData\Roaming\JukaHub`, "/mnt/SDCARD/Apps/JukaHub/data", true},
		{`D:\Elsewhere\file.txt`, "", false},
		{"relative/file.txt", "relative/file.txt", true},
	}
	for _, c := range cases {
		got, ok := bkupTranslatePath(c.in, from, to, true)
		if got != c.want || ok != c.ok {
			t.Errorf("bkupTranslatePath(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
	// On the same kind of system a folder outside the known ones keeps its meaning.
	if got, ok := bkupTranslatePath(`D:\Elsewhere\file.txt`, from, to, false); !ok || got != `D:\Elsewhere\file.txt` {
		t.Errorf("same-platform path became %q, %v", got, ok)
	}
}

func TestBkupMergeKeepsWhatBelongsToThisDevice(t *testing.T) {
	current := &UserConfig{}
	current.Variables.Fullscreen = true
	current.Variables.Volume, current.Variables.VolumeSet = 40, true
	current.Variables.Custom = map[string]interface{}{"swapABButtons": true, playtimeCustomKey: "mine", "weatherUnit": "C"}
	backup := &UserConfig{}
	backup.Variables.Volume, backup.Variables.VolumeSet = 90, true
	backup.Variables.FileExplorerRoot = `C:\Users\me\Games`
	backup.Variables.Custom = map[string]interface{}{
		"swapABButtons": false, playtimeCustomKey: "theirs", "weatherUnit": "F",
		"lastFolder": `C:\Users\me\Music`, "elsewhere": `E:\x`,
	}
	m := bkupManifest{Platform: "another-platform", Roots: map[string]string{"home": `C:\Users\me`}}
	here := map[string]string{"home": "/root"}

	merged := bkupMergeUser(current, backup, map[string]bool{bkupCatSettings: true}, m, here)
	if merged.Variables.Custom["weatherUnit"] != "F" {
		t.Errorf("weatherUnit = %v, want the backup's setting", merged.Variables.Custom["weatherUnit"])
	}
	if !merged.Variables.Fullscreen || merged.Variables.Custom["swapABButtons"] != true {
		t.Error("a fact about this device was taken from the other platform's backup")
	}
	if merged.Variables.Volume != 40 {
		t.Errorf("volume = %d, want this device's level", merged.Variables.Volume)
	}
	if merged.Variables.Custom[playtimeCustomKey] != "mine" {
		t.Error("the library came back although only the settings were chosen")
	}
	if merged.Variables.FileExplorerRoot != filepath.Join("/root", "Games") {
		t.Errorf("file explorer root = %q", merged.Variables.FileExplorerRoot)
	}
	if merged.Variables.Custom["lastFolder"] != filepath.Join("/root", "Music") {
		t.Errorf("lastFolder = %v", merged.Variables.Custom["lastFolder"])
	}
	if _, ok := merged.Variables.Custom["elsewhere"]; ok {
		t.Error("a folder that does not exist on this kind of device was kept")
	}
	if current.Variables.Custom["weatherUnit"] != "C" {
		t.Error("merging changed the config in use")
	}

	library := bkupMergeUser(current, backup, map[string]bool{bkupCatLibrary: true}, m, here)
	if library.Variables.Custom[playtimeCustomKey] != "theirs" || library.Variables.Custom["weatherUnit"] != "C" {
		t.Errorf("a library-only restore merged %v", library.Variables.Custom)
	}
}

func TestBkupNamesAndPrunesBackups(t *testing.T) {
	now := time.Date(2026, 11, 8, 9, 5, 0, 0, time.UTC)
	for kind, check := range map[string]func(string) bool{
		"manual": func(n string) bool {
			return strings.HasPrefix(n, "JukaHub-backup-") && !strings.HasSuffix(n, "-auto"+bkupExt)
		},
		"auto": func(n string) bool {
			return strings.HasPrefix(n, "JukaHub-backup-") && strings.HasSuffix(n, "-auto"+bkupExt)
		},
		"safety": func(n string) bool { return strings.HasPrefix(n, "JukaHub-safety-") },
	} {
		name := bkupFileName(kind, now)
		if !check(name) || !strings.Contains(name, "20261108-0905") {
			t.Errorf("%s backup named %s", kind, name)
		}
	}

	dir := t.TempDir()
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		path := filepath.Join(dir, fmt.Sprintf("JukaHub-backup-test-2026110%d-1000-auto%s", i+1, bkupExt))
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		mod := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	manual := filepath.Join(dir, "JukaHub-backup-test-20261101-0900"+bkupExt)
	if err := os.WriteFile(manual, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bkupPrune(dir, "JukaHub-backup-", "-auto", 3)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if len(left) != 4 {
		t.Fatalf("left %v, want the three newest weekly backups and the manual one", left)
	}
	for _, gone := range []string{"20261101-1000-auto", "20261102-1000-auto"} {
		for _, name := range left {
			if strings.Contains(name, gone) {
				t.Errorf("the old weekly backup %s was kept", name)
			}
		}
	}
	if _, err := os.Stat(manual); err != nil {
		t.Error("pruning the weekly backups removed a manual one")
	}
}

func TestBkupWeeklySwitchAndSchedule(t *testing.T) {
	root := bkupTestPlaces(t)
	if bkupAutoEnabled(&Config{}) || bkupAutoEnabled(nil) {
		t.Error("the weekly backup is on by default")
	}
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{bkupAutoKey: "true"}
	if !bkupAutoEnabled(config) {
		t.Error("the weekly switch is not read")
	}
	now := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	if !bkupAutoDue(now) {
		t.Error("a first weekly backup is not due")
	}
	plugxTestWrite(t, filepath.Join(root, "data", "backups"), map[string]string{
		"auto.json": `{"last": "` + now.Add(-48*time.Hour).Format(time.RFC3339) + `"}`,
	})
	if bkupAutoDue(now) {
		t.Error("a backup two days old is already due again")
	}
	if !bkupAutoDue(now.Add(6 * 24 * time.Hour)) {
		t.Error("a backup eight days old is not due")
	}
}
