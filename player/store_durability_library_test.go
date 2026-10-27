package main

// store_durability_library_test.go — the library-area stores outside
// jukauser.json.
//
// Favorites, the per-game launch options, the Continue list, the save-sync
// folder manifest and the curation profiles a user builds are each something
// the card cannot regenerate. All five used to be written with a bare
// os.WriteFile or a temp file with no fsync and no backup, and their loaders
// answered a damaged file with an empty store — so a power cut looked like the
// user had deleted everything.
//
// Each test drives the real save and load functions through their path seam:
// the second save must leave the first version in <path>.bak, no .tmp may
// remain, a truncated live file must come back from that backup, and an absent
// file must still read as empty.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// libraryDurRequireBackupOf fails unless path+".bak" holds want and the atomic
// writer left no temporary file behind.
func libraryDurRequireBackupOf(t *testing.T, path, want string) {
	t.Helper()
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("a temp file was left beside %s", path)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no backup beside %s: %v", path, err)
	}
	if !strings.Contains(string(backup), want) {
		t.Errorf("backup is not the previous version (want %q):\n%s", want, backup)
	}
}

// libraryDurCorrupt truncates the live file the way a power cut mid-write does.
func libraryDurCorrupt(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{"half":`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLibraryDurFavoritesRecoverFromBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "favorites.json")
	favoritesMutex.Lock()
	prev := favoritesStore
	favoritesStore = FavoritesStore{}
	favoritesMutex.Unlock()
	t.Cleanup(func() {
		favoritesMutex.Lock()
		favoritesStore = prev
		favoritesMutex.Unlock()
	})

	favoritesMutex.Lock()
	favoritesStore.Files = []FavoriteItem{{
		Type: "file", Data: FileEntry{Name: "first.txt", Path: "/roms/first.txt"}, Timestamp: time.Now(),
	}}
	favoritesMutex.Unlock()
	saveFavoritesTo(path)

	favoritesMutex.Lock()
	favoritesStore.Files = append(favoritesStore.Files, FavoriteItem{
		Type: "file", Data: FileEntry{Name: "second.txt", Path: "/roms/second.txt"}, Timestamp: time.Now(),
	})
	favoritesMutex.Unlock()
	saveFavoritesTo(path)

	libraryDurRequireBackupOf(t, path, "first.txt")
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(backup), "second.txt") {
		t.Errorf("backup holds the current list, not the previous one:\n%s", backup)
	}

	libraryDurCorrupt(t, path)
	favoritesMutex.Lock()
	favoritesStore = FavoritesStore{}
	favoritesMutex.Unlock()
	loadFavoritesFrom(path)
	favoritesMutex.Lock()
	files := append([]FavoriteItem(nil), favoritesStore.Files...)
	favoritesMutex.Unlock()
	if len(files) != 1 {
		t.Fatalf("favorites came back as %+v, want the one file from the backup", files)
	}
	if fe, ok := files[0].Data.(FileEntry); !ok || fe.Name != "first.txt" {
		t.Errorf("recovered favorite = %+v, want first.txt", files[0].Data)
	}

	// A missing file is a first run, not damage.
	favoritesMutex.Lock()
	favoritesStore = FavoritesStore{}
	favoritesMutex.Unlock()
	loadFavoritesFrom(filepath.Join(t.TempDir(), "absent.json"))
	favoritesMutex.Lock()
	files = append([]FavoriteItem(nil), favoritesStore.Files...)
	favoritesMutex.Unlock()
	if len(files) != 0 {
		t.Errorf("an absent store produced %+v", files)
	}
}

func TestLibraryDurGameOptionsRecoverFromBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "game-options.json")
	first := map[string]gameOptions{"/roms/one.gba": {MaxFreqKHz: 600000}}
	if err := saveGameOptionsFile(path, first); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := saveGameOptionsFile(path, map[string]gameOptions{"/roms/two.gba": {MaxFreqKHz: 800000}}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	libraryDurRequireBackupOf(t, path, "one.gba")

	libraryDurCorrupt(t, path)
	got := loadGameOptionsFile(path)
	opts, ok := got[gameOptionsKey("/roms/one.gba")]
	if !ok {
		t.Fatalf("overrides came back as %+v, want the one from the backup", got)
	}
	if opts.MaxFreqKHz != 600000 {
		t.Errorf("recovered ceiling = %d, want 600000", opts.MaxFreqKHz)
	}
	if len(loadGameOptionsFile(filepath.Join(t.TempDir(), "absent.json"))) != 0 {
		t.Error("an absent options file produced overrides")
	}
}

func TestLibraryDurRecentsRecoverFromBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "game-recents.json")
	if err := saveGameRecentsFile(path, []recentGame{{Path: "/roms/first.gba", Title: "First"}}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := saveGameRecentsFile(path, []recentGame{{Path: "/roms/second.gba", Title: "Second"}}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	libraryDurRequireBackupOf(t, path, "first.gba")

	libraryDurCorrupt(t, path)
	got := loadGameRecentsFile(path)
	if len(got) != 1 || got[0].Path != "/roms/first.gba" {
		t.Fatalf("recents came back as %+v, want the one from the backup", got)
	}
	if len(loadGameRecentsFile(filepath.Join(t.TempDir(), "absent.json"))) != 0 {
		t.Error("an absent recents file produced entries")
	}
}

func TestLibraryDurSaveSyncManifestRecoverFromBackup(t *testing.T) {
	dir := t.TempDir()
	prevDir, prevSD := saveManagerDirOverride, saveManagerSDRootOverride
	saveManagerDirOverride = filepath.Join(dir, "save-manager")
	saveManagerSDRootOverride = filepath.Join(dir, "sdcard")
	t.Cleanup(func() {
		saveManagerDirOverride, saveManagerSDRootOverride = prevDir, prevSD
	})

	firstRoot := filepath.Join(dir, "retroarch", "saves")
	secondRoot := filepath.Join(dir, "retroarch", "states")
	for _, root := range []string{firstRoot, secondRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := prepareSaveSync([]saveRoot{{Name: "RetroArch saves", Path: firstRoot}}); err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	if _, err := prepareSaveSync([]saveRoot{{Name: "RetroArch states", Path: secondRoot}}); err != nil {
		t.Fatalf("second prepare: %v", err)
	}

	path := filepath.Join(saveManagerSyncDir(), "folders.json")
	libraryDurRequireBackupOf(t, path, "RetroArch saves")
	libraryDurCorrupt(t, path)

	manifest, ok := saveSyncStatus()
	if !ok {
		t.Fatal("a damaged manifest read as \"save sync was never set up\"")
	}
	if len(manifest.Folders) != 1 || manifest.Folders[0].Name != "RetroArch saves" {
		t.Fatalf("manifest came back as %+v, want the folder from the backup", manifest.Folders)
	}
}

// A deleted profile must not be able to return from the backup the atomic write
// leaves beside it — the same trap a forgotten cookie session has.
func TestLibraryDurDeletingAProfileTakesTheBackup(t *testing.T) {
	setCurationTestDirs(t)
	profile := curationProfile{Name: "Temporary", Theme: "hub", TextScale: 1}
	if _, err := writeCurationProfile(profile, curationProfilesDir()); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// A second save is what creates the backup.
	profile.Notes = "edited"
	path, err := writeCurationProfile(profile, curationProfilesDir())
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("no backup was written to delete: %v", err)
	}

	// normalizeCurationProfile assigns the ID inside the write, so the delete
	// names the file that was actually written.
	saved := profile
	saved.ID = strings.TrimSuffix(filepath.Base(path), ".json")
	saved.Source = "local"
	if err := deleteCurationProfile(saved); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, p := range []string{path, path + ".bak"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived the delete (err=%v)", p, err)
		}
	}
}

func TestLibraryDurCurationProfileIsWrittenAtomically(t *testing.T) {
	dir := t.TempDir()
	profile := curationProfile{Name: "Desk tune", Theme: "hub", TextScale: 1.05}
	path, err := writeCurationProfile(profile, dir)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	profile.Notes = "second version"
	again, err := writeCurationProfile(profile, dir)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if again != path {
		t.Fatalf("the second write went to %s, want %s", again, path)
	}
	libraryDurRequireBackupOf(t, path, `"name": "Desk tune"`)

	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(backup), "second version") {
		t.Errorf("backup holds the current profile, not the previous one:\n%s", backup)
	}
	live, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(live), "second version") {
		t.Errorf("live profile did not receive the new notes:\n%s", live)
	}
}
