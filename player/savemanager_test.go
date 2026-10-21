package main

// savemanager_test.go — the save manager's promise is that a snapshot can be
// restored after a card scare, so the tests exercise a real round trip through
// temp folders instead of only checking the manifest shape.

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setSaveManagerTestDirs points the data folder and the SD-card scan at temp
// trees. The real card is never touched.
func setSaveManagerTestDirs(t *testing.T) (dataDir, sdRoot string) {
	t.Helper()
	dataDir = t.TempDir()
	sdRoot = t.TempDir()
	prevDir, prevRoot := saveManagerDirOverride, saveManagerSDRootOverride
	saveManagerDirOverride, saveManagerSDRootOverride = dataDir, sdRoot
	t.Cleanup(func() {
		saveManagerDirOverride, saveManagerSDRootOverride = prevDir, prevRoot
	})
	if err := ensureSaveManagerLayout(); err != nil {
		t.Fatalf("ensureSaveManagerLayout: %v", err)
	}
	return dataDir, sdRoot
}

func writeSaveTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readSaveTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func hasSaveRoot(roots []saveRoot, name, path string) bool {
	want := filepath.Clean(path)
	for _, root := range roots {
		if root.Name == name && filepath.Clean(root.Path) == want {
			return true
		}
	}
	return false
}

func TestResolveRetroArchSaveDir(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "retroarch.cfg")
	cfgDir := filepath.Dir(cfg)

	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"quoted absolute", `"` + filepath.Join(cfgDir, "saves") + `"`, filepath.Join(cfgDir, "saves")},
		{"config-relative prefix", ":/states", filepath.Join(cfgDir, "states")},
		{"config-relative windows", ":\\saves", filepath.Join(cfgDir, "saves")},
		{"relative beside cfg", "saves", filepath.Join(cfgDir, "saves")},
		{"empty", "  ", ""},
		{"default", "default", ""},
		{"disabled", "disabled", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveRetroArchSaveDir(tc.value, cfg)
			if got != tc.want {
				t.Fatalf("resolveRetroArchSaveDir(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestSaveRootsForFindsStandardFolders(t *testing.T) {
	_, sdRoot := setSaveManagerTestDirs(t)
	saves := filepath.Join(sdRoot, "RetroArch", "saves")
	states := filepath.Join(sdRoot, "RetroArch", "states")
	psp := filepath.Join(sdRoot, "PSP", "SAVEDATA")
	for _, dir := range []string{saves, states, psp} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	roots := saveRootsFor(sdRoot, "", "")
	if !hasSaveRoot(roots, "RetroArch saves", saves) {
		t.Fatalf("RetroArch saves missing from %+v", roots)
	}
	if !hasSaveRoot(roots, "RetroArch states", states) {
		t.Fatalf("RetroArch states missing from %+v", roots)
	}
	if !hasSaveRoot(roots, "PPSSPP savedata", psp) {
		t.Fatalf("PPSSPP savedata missing from %+v", roots)
	}
	// Folders that do not exist must never become rows the panel offers.
	if hasSaveRoot(roots, "DraStic backups", filepath.Join(sdRoot, "DraStic", "backup")) {
		t.Fatalf("missing DraStic folder was offered: %+v", roots)
	}
}

func TestSaveRootsForHonoursRetroArchConfig(t *testing.T) {
	_, sdRoot := setSaveManagerTestDirs(t)
	custom := filepath.Join(sdRoot, "my-saves")
	customStates := filepath.Join(sdRoot, "my-states")
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(customStates, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(sdRoot, "RetroArch", "retroarch.cfg")
	cfg := "savefile_directory = \"" + filepath.ToSlash(custom) + "\"\n" +
		"savestate_directory = \"" + filepath.ToSlash(customStates) + "\"\n"
	writeSaveTestFile(t, cfgPath, cfg)

	roots := saveRootsFor(sdRoot, cfg, cfgPath)
	if !hasSaveRoot(roots, "RetroArch saves", custom) {
		t.Fatalf("configured save folder missing from %+v", roots)
	}
	if !hasSaveRoot(roots, "RetroArch states", customStates) {
		t.Fatalf("configured state folder missing from %+v", roots)
	}
}

func TestSaveRootsForSkipsItsOwnSnapshots(t *testing.T) {
	dataDir, sdRoot := setSaveManagerTestDirs(t)
	// A portable build puts the data folder beside the executable, which can
	// be the SD root. A root that contains the snapshots would copy itself.
	enclosing := filepath.Dir(dataDir)
	roots := saveRootsFor(sdRoot, "savefile_directory = \""+filepath.ToSlash(enclosing)+"\"", "")
	for _, root := range roots {
		if filepath.Clean(root.Path) == filepath.Clean(enclosing) {
			t.Fatalf("save manager folder was offered as a save root: %+v", roots)
		}
	}
}

func TestSaveRootSlug(t *testing.T) {
	cases := map[string]string{
		"RetroArch saves":  "retroarch-saves",
		"PPSSPP savedata":  "ppsspp-savedata",
		"  weird // name ": "weird-name",
		"":                 "root",
		"!!!":              "root",
	}
	for in, want := range cases {
		if got := saveRootSlug(in); got != want {
			t.Fatalf("saveRootSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsSaveFile(t *testing.T) {
	for _, name := range []string{".stfolder", ".stignore", ".DS_Store", "thumbs.db", "save.tmp", "save.part"} {
		if isSaveFile(name) {
			t.Fatalf("%s should be skipped", name)
		}
	}
	for _, name := range []string{"mario.srm", "state.000", "battery.sav"} {
		if !isSaveFile(name) {
			t.Fatalf("%s should be captured", name)
		}
	}
}

func TestSaveSnapshotRoundTrip(t *testing.T) {
	setSaveManagerTestDirs(t)
	root := t.TempDir()
	writeSaveTestFile(t, filepath.Join(root, "Mario.srm"), "original save")
	writeSaveTestFile(t, filepath.Join(root, "sub", "state.001"), "state data")
	writeSaveTestFile(t, filepath.Join(root, ".stfolder"), "")
	roots := []saveRoot{{Name: "RetroArch saves", Path: root}}

	snap, err := createSaveSnapshot(roots, "test")
	if err != nil {
		t.Fatalf("createSaveSnapshot: %v", err)
	}
	if snap.Manifest.ID == "" || snap.Manifest.Label != "test" {
		t.Fatalf("unexpected manifest: %+v", snap.Manifest)
	}
	if len(snap.Manifest.Roots) != 1 {
		t.Fatalf("want 1 root, got %+v", snap.Manifest.Roots)
	}
	snapRoot := snap.Manifest.Roots[0]
	if snapRoot.Slug != "retroarch-saves" {
		t.Fatalf("unexpected slug %q", snapRoot.Slug)
	}
	if len(snapRoot.Files) != 2 {
		t.Fatalf("want 2 captured files, got %+v", snapRoot.Files)
	}
	if _, err := os.Stat(filepath.Join(root, "Mario.srm")); err != nil {
		t.Fatalf("snapshot must not move the live save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snap.Dir, "files", "retroarch-saves", "sub", "state.001")); err != nil {
		t.Fatalf("nested file missing from snapshot: %v", err)
	}

	// Wreck the live save, then restore and expect the snapshot back.
	writeSaveTestFile(t, filepath.Join(root, "Mario.srm"), "corrupted")
	if err := os.Remove(filepath.Join(root, "sub", "state.001")); err != nil {
		t.Fatal(err)
	}
	pre, err := restoreSaveSnapshot(snap, roots)
	if err != nil {
		t.Fatalf("restoreSaveSnapshot: %v", err)
	}
	if got := readSaveTestFile(t, filepath.Join(root, "Mario.srm")); got != "original save" {
		t.Fatalf("restored save = %q", got)
	}
	if got := readSaveTestFile(t, filepath.Join(root, "sub", "state.001")); got != "state data" {
		t.Fatalf("restored state = %q", got)
	}
	if pre.Manifest.ID == "" || pre.Manifest.Label != "before restore" {
		t.Fatalf("restore must leave a safety snapshot, got %+v", pre.Manifest)
	}
	if snaps := listSaveSnapshots(); len(snaps) != 2 {
		t.Fatalf("want 2 snapshots after restore, got %d", len(snaps))
	}
}

func TestRestoreMatchesRootsByPath(t *testing.T) {
	setSaveManagerTestDirs(t)
	first := t.TempDir()
	second := t.TempDir()
	writeSaveTestFile(t, filepath.Join(first, "a.sav"), "first")
	writeSaveTestFile(t, filepath.Join(second, "b.sav"), "second")
	// Two folders share the label; only the paths tell them apart.
	roots := []saveRoot{
		{Name: "RetroArch saves", Path: first},
		{Name: "RetroArch saves", Path: second},
	}
	snap, err := createSaveSnapshot(roots, "test")
	if err != nil {
		t.Fatalf("createSaveSnapshot: %v", err)
	}
	writeSaveTestFile(t, filepath.Join(first, "a.sav"), "changed")
	writeSaveTestFile(t, filepath.Join(second, "b.sav"), "changed")
	if _, err := restoreSaveSnapshot(snap, roots); err != nil {
		t.Fatalf("restoreSaveSnapshot: %v", err)
	}
	if got := readSaveTestFile(t, filepath.Join(first, "a.sav")); got != "first" {
		t.Fatalf("first folder = %q, want first", got)
	}
	if got := readSaveTestFile(t, filepath.Join(second, "b.sav")); got != "second" {
		t.Fatalf("second folder = %q, want second", got)
	}
}

func TestRestoreRefusesAmbiguousFolderName(t *testing.T) {
	setSaveManagerTestDirs(t)
	original := t.TempDir()
	writeSaveTestFile(t, filepath.Join(original, "a.sav"), "save")
	snap, err := createSaveSnapshot([]saveRoot{{Name: "RetroArch saves", Path: original}}, "test")
	if err != nil {
		t.Fatalf("createSaveSnapshot: %v", err)
	}

	// Simulate a card that was recreated with two folders of the same name at
	// new paths: guessing which one receives the restore would be destructive.
	one := t.TempDir()
	two := t.TempDir()
	_, err = restoreSaveSnapshot(snap, []saveRoot{
		{Name: "RetroArch saves", Path: one},
		{Name: "RetroArch saves", Path: two},
	})
	if err == nil || !strings.Contains(err.Error(), "more than one folder") {
		t.Fatalf("want an ambiguity error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(one, "a.sav")); !os.IsNotExist(statErr) {
		t.Fatalf("ambiguous restore wrote into %s", one)
	}
}

func TestCreateSaveSnapshotEmpty(t *testing.T) {
	setSaveManagerTestDirs(t)
	empty := t.TempDir()
	_, err := createSaveSnapshot([]saveRoot{{Name: "RetroArch saves", Path: empty}}, "test")
	if !errors.Is(err, errSaveManagerEmpty) {
		t.Fatalf("want errSaveManagerEmpty, got %v", err)
	}
}

func TestPruneSaveSnapshots(t *testing.T) {
	setSaveManagerTestDirs(t)
	root := t.TempDir()
	writeSaveTestFile(t, filepath.Join(root, "save.srm"), "save")
	roots := []saveRoot{{Name: "RetroArch saves", Path: root}}
	for i := 0; i < saveSnapshotKeep+2; i++ {
		if _, err := createSaveSnapshot(roots, "test"); err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
	}
	if snaps := listSaveSnapshots(); len(snaps) != saveSnapshotKeep {
		t.Fatalf("want %d snapshots kept, got %d", saveSnapshotKeep, len(snaps))
	}
}

func TestPrepareSaveSyncWritesManifestAndMarkers(t *testing.T) {
	dataDir, _ := setSaveManagerTestDirs(t)
	first := t.TempDir()
	second := t.TempDir()
	writeSaveTestFile(t, filepath.Join(first, "a.sav"), "a")
	writeSaveTestFile(t, filepath.Join(second, "b.sav"), "b")
	roots := []saveRoot{
		{Name: "RetroArch saves", Path: first},
		{Name: "RetroArch saves", Path: second},
	}

	manifest, err := prepareSaveSync(roots)
	if err != nil {
		t.Fatalf("prepareSaveSync: %v", err)
	}
	if len(manifest.Folders) != 2 {
		t.Fatalf("want 2 folders, got %+v", manifest.Folders)
	}
	if manifest.Folders[0].FolderID == manifest.Folders[1].FolderID {
		t.Fatalf("folder IDs must be unique: %+v", manifest.Folders)
	}
	for _, root := range roots {
		for _, name := range []string{".stfolder", ".stignore"} {
			if _, err := os.Stat(filepath.Join(root.Path, name)); err != nil {
				t.Fatalf("%s marker missing from %s: %v", name, root.Path, err)
			}
		}
	}
	for _, name := range []string{"folders.json", "README.txt"} {
		if _, err := os.Stat(filepath.Join(dataDir, "sync", name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	var onDisk saveSyncManifest
	raw := readSaveTestFile(t, filepath.Join(dataDir, "sync", "folders.json"))
	if err := json.Unmarshal([]byte(raw), &onDisk); err != nil {
		t.Fatalf("folders.json is not valid JSON: %v", err)
	}
	if len(onDisk.Folders) != 2 {
		t.Fatalf("folders.json has %d folders", len(onDisk.Folders))
	}
	if ready, ok := saveSyncStatus(); !ok || len(ready.Folders) != 2 {
		t.Fatalf("saveSyncStatus did not read the manifest back")
	}
}

func TestPrepareSaveSyncWithoutRoots(t *testing.T) {
	setSaveManagerTestDirs(t)
	if _, err := prepareSaveSync(nil); err == nil {
		t.Fatal("want an error when no save folders exist")
	}
}

func TestCreateResumeBundle(t *testing.T) {
	dataDir, _ := setSaveManagerTestDirs(t)
	root := t.TempDir()
	writeSaveTestFile(t, filepath.Join(root, "Mario.srm"), "save body")
	writeSaveTestFile(t, filepath.Join(root, "sub", "state.001"), "state body")
	snap, err := createSaveSnapshot([]saveRoot{{Name: "RetroArch saves", Path: root}}, "test")
	if err != nil {
		t.Fatalf("createSaveSnapshot: %v", err)
	}

	bundle, err := createResumeBundle(snap)
	if err != nil {
		t.Fatalf("createResumeBundle: %v", err)
	}
	if filepath.Dir(bundle) != filepath.Join(dataDir, "resume") {
		t.Fatalf("bundle landed in %s", filepath.Dir(bundle))
	}
	reader, err := zip.OpenReader(bundle)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	defer reader.Close()
	want := map[string]string{
		"manifest.json":                       "",
		"RESUME.txt":                          "",
		"files/retroarch-saves/Mario.srm":     "save body",
		"files/retroarch-saves/sub/state.001": "state body",
	}
	got := map[string]bool{}
	for _, file := range reader.File {
		got[file.Name] = true
		if body, ok := want[file.Name]; ok && body != "" {
			rc, err := file.Open()
			if err != nil {
				t.Fatalf("open %s: %v", file.Name, err)
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatalf("read %s: %v", file.Name, err)
			}
			if string(data) != body {
				t.Fatalf("%s = %q, want %q", file.Name, data, body)
			}
		}
	}
	for name := range want {
		if !got[name] {
			t.Fatalf("%s missing from bundle (have %v)", name, got)
		}
	}
	if latest, ok := latestResumeBundle(); !ok || latest != bundle {
		t.Fatalf("latestResumeBundle = %q, %v", latest, ok)
	}
}
