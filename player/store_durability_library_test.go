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

// An options file from before CPU profiles and video filters reads exactly as
// it did, the new fields round-trip, and a hand-edited value nobody knows
// falls back to the default rather than keeping a game on a broken profile.
func TestLibraryDurGameOptionsProfileFields(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.json")
	legacy := `{"schemaVersion":1,"games":{"/roms/a.gba":{"max_freq_khz":1400000,"args":"--fast"},` +
		`"/roms/b.gba":{"cpu_profile":"turbo","video_filter":"sepia"}}}`
	if err := os.WriteFile(old, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	got := loadGameOptionsFile(old)
	a := got[gameOptionsKey("/roms/a.gba")]
	if a.MaxFreqKHz != 1400000 || a.Args != "--fast" || a.CPUProfile != "" || a.VideoFilter != "" {
		t.Errorf("legacy entry read as %+v", a)
	}
	if b, ok := got[gameOptionsKey("/roms/b.gba")]; ok {
		t.Errorf("unknown values read as %+v, want the defaults (no entry)", b)
	}

	path := filepath.Join(dir, "new.json")
	want := gameOptions{CPUProfile: "battery", VideoFilter: "crt", MaxFreqKHz: 1600000}
	if err := saveGameOptionsFile(path, map[string]gameOptions{"/roms/c.gba": want}); err != nil {
		t.Fatal(err)
	}
	if c := loadGameOptionsFile(path)[gameOptionsKey("/roms/c.gba")]; c != want {
		t.Errorf("round trip = %+v, want %+v", c, want)
	}
	// The per-game limit wins over Battery saver's own ceiling.
	if cfg := gameLaunchPowerConfig(powerProfileConfig{Enabled: true}, want); !cfg.Balanced || cfg.MaxFreqKHz != 1600000 {
		t.Errorf("battery + limit = %+v", cfg)
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

// The Tabs Home's collections.json (hometabs_collections.go): a save round-
// trips and is cleaned on load (names tidied, duplicates and nested
// collections dropped), the second save leaves the first in .bak, a damaged
// file comes back from it with the damage kept as .corrupt, and a file with
// no usable copy at all reads as empty instead of failing.
func TestLibraryDurCollectionsRecoverFromBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), homeTabsColFileName)
	if got := homeTabsColLoadFile(path); len(got.Collections) != 0 || len(got.Favourites) != 0 {
		t.Fatalf("absent file is not empty: %+v", got)
	}
	first := homeTabsColFile{
		Collections: []homeTabsCollection{{
			ID:   "a1",
			Name: "  Couch   co-op ",
			Items: []homeTabsColItem{
				{Ref: "game:/roms/SNES/a.sfc", Title: "A"},
				{Ref: "game:/roms/SNES/a.sfc"},
				{Ref: "col:b2"},
				{Ref: "app:do:Steam", Title: "Steam"},
			},
		}},
		Favourites: []homeTabsColItem{{Ref: "sys:SNES"}, {Ref: "sys:SNES"}},
	}
	if err := homeTabsColSaveFile(path, first); err != nil {
		t.Fatalf("first save: %v", err)
	}
	got := homeTabsColLoadFile(path)
	if got.Version != homeTabsColVersion || len(got.Collections) != 1 || len(got.Favourites) != 1 {
		t.Fatalf("loaded %+v", got)
	}
	if col := got.Collections[0]; col.Name != "Couch co-op" || len(col.Items) != 2 || col.Items[1].Ref != "app:do:Steam" {
		t.Fatalf("collection not cleaned: %+v", col)
	}
	if err := homeTabsColSaveFile(path, got); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got.Collections[0].Name = "Renamed"
	if err := homeTabsColSaveFile(path, got); err != nil {
		t.Fatalf("third save: %v", err)
	}
	libraryDurRequireBackupOf(t, path, `"name": "Couch co-op"`)

	libraryDurCorrupt(t, path)
	back := homeTabsColLoadFile(path)
	if len(back.Collections) != 1 || back.Collections[0].Name != "Couch co-op" {
		t.Fatalf("damaged file did not come back from the backup: %+v", back)
	}
	if kept, err := os.ReadFile(path + ".corrupt"); err != nil || !strings.Contains(string(kept), `{"half":`) {
		t.Errorf("the damaged copy was not kept: %q, %v", kept, err)
	}
	if again := homeTabsColLoadFile(path); len(again.Collections) != 1 {
		t.Errorf("the live file was not restored from the backup: %+v", again)
	}

	for _, p := range []string{path, path + ".bak"} {
		if err := os.WriteFile(p, []byte("not json"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if lost := homeTabsColLoadFile(path); len(lost.Collections) != 0 || len(lost.Favourites) != 0 {
		t.Errorf("unreadable store did not read as empty: %+v", lost)
	}
}

// TestLibraryDurAutoCollectionsStore covers the automatic collections' store
// (autocoll.go) and the outside details it files games by: a missing, damaged
// or other-version game-seen.json reads as an empty store (so the next
// library is the baseline again and nothing floods Recently added), a good
// one reads back whole, and the libretro player counts and genres parse.
func TestLibraryDurAutoCollectionsStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), autoCollFileName)
	for name, body := range map[string]string{
		"absent":        "",
		"damaged":       `{"version": 1, "seen": {`,
		"other version": `{"version": 9, "based": true, "seen": {"k": 3}}`,
	} {
		_ = os.Remove(path)
		if body != "" {
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if f := autoCollReadFile(path); f.Based || len(f.Seen) != 0 || f.Seen == nil || f.Played == nil {
			t.Errorf("%s store did not read as empty: %+v", name, f)
		}
	}
	good := `{"version": 1, "based": true, "scans": 2, "seen": {"k": 2}, "played": {"/r/a.gba": 7}}`
	if err := os.WriteFile(path, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if f := autoCollReadFile(path); !f.Based || f.Scans != 2 || f.Seen["k"] != 2 || f.Played["/r/a.gba"] != 7 {
		t.Errorf("good store read back as %+v", f)
	}
	for in, want := range map[string]int{"": 0, "1": 1, "2": 2, "1-4": 4, "4 players": 4} {
		if got := autoCollPlayers(in); got != want {
			t.Errorf("players %q = %d, want %d", in, got, want)
		}
	}
	for in, want := range map[string]string{"Action / Platformer": "Action", "  Racing ": "Racing", "Shooter, Rail": "Shooter"} {
		if got := autoCollGenreLabel(in); got != want {
			t.Errorf("genre %q = %q, want %q", in, got, want)
		}
	}
	if got := autoCollSlug("Role-Playing (RPG)"); got != "role-playing-rpg" {
		t.Errorf("slug = %q", got)
	}
	if autoCollSlug("Shoot'em Up") != autoCollSlug("SHOOTEM up") || autoCollSlug("ロールプレイング") == "" {
		t.Error("genre spellings did not fold into one folder id")
	}
}
