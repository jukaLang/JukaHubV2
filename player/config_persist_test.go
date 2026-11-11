package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// saveConfig writes only design data: runtime-only custom keys are dropped,
// user settings stay in jukauser.json, and the file carries the current schema
// version so a later load migrates nothing.
func TestSaveConfigPersistsDesignDataOnly(t *testing.T) {
	t.Chdir(t.TempDir())

	cfg := &Config{
		AppName: "JukaHub",
		Version: "0.4.0",
		Width:   1280,
		Height:  720,
	}
	cfg.Variables.Custom = map[string]interface{}{
		"theme_preset":   "Midnight",
		"search_results": "transient",
		"fe_entries":     "transient",
	}
	cfg.Variables.ButtonColor = RGB{R: 1, G: 2, B: 3}
	cfg.Variables.Fullscreen = true
	cfg.Variables.FileExplorerRoot = "/tmp"

	saveConfig(cfg)

	data, err := os.ReadFile("jukaconfig.json")
	if err != nil {
		t.Fatalf("saveConfig did not write jukaconfig.json: %v", err)
	}
	var raw struct {
		SchemaVersion int `json:"schemaVersion"`
		Variables     struct {
			Fullscreen       bool           `json:"fullscreen"`
			FileExplorerRoot string         `json:"fileExplorerRoot"`
			Custom           map[string]any `json:"Custom"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("saved config is not valid JSON: %v", err)
	}
	if raw.SchemaVersion != ConfigSchemaVersion {
		t.Errorf("schemaVersion = %d, want %d", raw.SchemaVersion, ConfigSchemaVersion)
	}
	if got := raw.Variables.Custom["theme_preset"]; got != "Midnight" {
		t.Errorf("theme_preset = %v, want Midnight", got)
	}
	for _, volatile := range []string{"search_results", "fe_entries"} {
		if _, ok := raw.Variables.Custom[volatile]; ok {
			t.Errorf("volatile key %q was persisted", volatile)
		}
	}
	// User-only settings must never leak into the design file.
	if raw.Variables.Fullscreen {
		t.Error("fullscreen is a user setting and must not be persisted in jukaconfig.json")
	}
	if raw.Variables.FileExplorerRoot != "" {
		t.Errorf("fileExplorerRoot = %q, want empty", raw.Variables.FileExplorerRoot)
	}

	reloaded, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("reload saved config: %v", err)
	}
	if reloaded.Variables.Custom["theme_preset"] != "Midnight" {
		t.Error("design custom key lost across save/load")
	}
}

// keepUserConfigLatest starts a test with no full save on record
// (userConfigLatest) and puts the run's back when it ends.
func keepUserConfigLatest(t *testing.T) {
	t.Helper()
	userConfigLatest.mu.Lock()
	prev := userConfigLatest.vars
	userConfigLatest.vars = nil
	userConfigLatest.mu.Unlock()
	t.Cleanup(func() {
		userConfigLatest.mu.Lock()
		userConfigLatest.vars = prev
		userConfigLatest.mu.Unlock()
	})
}

// A settings-only save must preserve favorites/chat state that the caller did
// not supply, and must not persist runtime-only custom keys.
func TestSaveUserConfigPreservesSections(t *testing.T) {
	t.Chdir(t.TempDir())
	keepUserConfigLatest(t)

	prevCache, prevTimer, prevPending := userConfigCache, userConfigSaveTimer, pendingUserConfig
	prevWrites := userConfigWritesEnabled
	// The test binary disables user-config writes; this test asserts one.
	userConfigWritesEnabled = true
	t.Cleanup(func() {
		userConfigCache, userConfigSaveTimer, pendingUserConfig = prevCache, prevTimer, prevPending
		userConfigWritesEnabled = prevWrites
	})
	userConfigSaveTimer = nil
	pendingUserConfig = nil

	userConfigCache = &UserConfig{
		Favorites: &FavoritesStore{
			Videos: []FavoriteItem{{Type: "video"}},
		},
		Chat: &ChatSection{Messages: []ChatMessage{{Text: "hello"}}},
	}

	settingsOnly := &UserConfig{
		Variables: UserVariables{
			ReducedMotion: true,
			Custom: map[string]interface{}{
				"keep_me":    "yes",
				"fe_entries": "runtime only",
			},
		},
	}
	saveUserConfig(settingsOnly)

	data, err := os.ReadFile("jukauser.json")
	if err != nil {
		t.Fatalf("saveUserConfig did not write jukauser.json: %v", err)
	}
	var raw struct {
		Variables struct {
			ReducedMotion bool           `json:"reducedMotion"`
			Custom        map[string]any `json:"Custom"`
		} `json:"variables"`
		Favorites *FavoritesStore `json:"favorites"`
		Chat      *ChatSection    `json:"chat"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("saved user config is not valid JSON: %v", err)
	}
	if !raw.Variables.ReducedMotion {
		t.Error("reducedMotion was not saved")
	}
	if raw.Variables.Custom["keep_me"] != "yes" {
		t.Errorf("keep_me = %v, want yes", raw.Variables.Custom["keep_me"])
	}
	if _, ok := raw.Variables.Custom["fe_entries"]; ok {
		t.Error("runtime-only custom key was persisted")
	}
	if raw.Favorites == nil || len(raw.Favorites.Videos) != 1 {
		t.Error("favorites from the cache were dropped by a settings-only save")
	}
	if raw.Chat == nil || len(raw.Chat.Messages) != 1 {
		t.Error("chat history from the cache was dropped by a settings-only save")
	}

	// Reload from disk to prove the file round-trips through the loader.
	userConfigCache = nil
	loaded := loadUserConfig()
	if loaded == nil {
		t.Fatal("loadUserConfig returned nil")
	}
	if loaded.Variables.Custom["keep_me"] != "yes" {
		t.Errorf("reloaded keep_me = %v, want yes", loaded.Variables.Custom["keep_me"])
	}
	if loaded.Favorites == nil || len(loaded.Favorites.Videos) != 1 {
		t.Error("favorites lost across save/load")
	}
}

// A section save (favourites, chat) hands over the cached user config, whose
// settings are the ones read at boot. It writes the newest full save's
// settings instead: it used to put the boot copy back over every setting and
// trophy saved since, at every exit too, so each start re-awarded the same
// trophies. A full save waiting in the debounced slot keeps the slot.
func TestSectionSaveKeepsTheNewestSettings(t *testing.T) {
	t.Chdir(t.TempDir())
	keepUserConfigLatest(t)
	prevCache, prevWrites := userConfigCache, userConfigWritesEnabled
	userConfigWritesEnabled = true
	cancelPendingUserConfigSave(t)
	t.Cleanup(func() {
		cancelPendingUserConfigSave(t)
		userConfigCache, userConfigWritesEnabled = prevCache, prevWrites
	})
	read := func() UserConfig {
		t.Helper()
		// Polled like the debounced test's read: on Windows a scanner can hold
		// a freshly renamed file for a moment.
		var data []byte
		var err error
		for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if data, err = os.ReadFile("jukauser.json"); (err == nil && len(data) > 0) || time.Now().After(deadline) {
				break
			}
		}
		if err != nil {
			t.Fatalf("read jukauser.json: %v", err)
		}
		var uc UserConfig
		if err := json.Unmarshal(data, &uc); err != nil {
			t.Fatalf("parse jukauser.json: %v", err)
		}
		return uc
	}
	// The cache as boot read it.
	userConfigCache = &UserConfig{Variables: UserVariables{Volume: 40, VolumeSet: true, Custom: map[string]interface{}{
		"theme_preset": "Boot", playtimeCustomKey: map[string]interface{}{"total": float64(10)},
	}}}
	// Before any full save the cache is the newest there is.
	userConfigCache.Favorites = &FavoritesStore{Videos: []FavoriteItem{{Type: "video"}}}
	saveUserConfig(userConfigCache)
	if got := read(); got.Variables.Custom["theme_preset"] != "Boot" {
		t.Fatalf("the first section save wrote theme %v", got.Variables.Custom["theme_preset"])
	}
	// A full save from the live config, then a favourites save.
	live := &Config{}
	live.Variables.Custom = map[string]interface{}{
		"theme_preset": "Midnight", playtimeCustomKey: map[string]interface{}{"total": float64(99)},
	}
	saveUserConfig(userConfigFromVariables(live))
	userConfigCache.Favorites = &FavoritesStore{Videos: []FavoriteItem{{Type: "video"}, {Type: "video"}}}
	saveUserConfig(userConfigCache)
	got := read()
	if got.Variables.Custom["theme_preset"] != "Midnight" {
		t.Fatalf("a section save put back theme %v", got.Variables.Custom["theme_preset"])
	}
	if pt, _ := got.Variables.Custom[playtimeCustomKey].(map[string]interface{}); pt == nil || pt["total"] != float64(99) {
		t.Fatalf("a section save put back playtime %v", got.Variables.Custom[playtimeCustomKey])
	}
	if got.Favorites == nil || len(got.Favorites.Videos) != 2 {
		t.Fatal("the section save lost its own section")
	}
	if got.Variables.Volume != 40 || !got.Variables.VolumeSet {
		t.Fatalf("levels = %d/%v, want the cache's 40", got.Variables.Volume, got.Variables.VolumeSet)
	}
	if got.Variables.NetPlayEnabled == nil || *got.Variables.NetPlayEnabled {
		t.Fatal("the netplay switch (off) was not saved")
	}
	// A section save queued after a full save does not take its slot.
	live.Variables.Custom["theme_preset"] = "Dawn"
	saveUserConfigDebounced(userConfigFromVariables(live))
	saveUserConfigDebounced(userConfigCache)
	flushPendingUserConfig()
	if got := read(); got.Variables.Custom["theme_preset"] != "Dawn" {
		t.Fatalf("a queued section save replaced the waiting full save (theme %v)", got.Variables.Custom["theme_preset"])
	}
}

// A pending debounced save must not follow the process into a different working
// directory. This used to dirty a test-runner's checkout two seconds after the
// change was made.
func TestDebouncedUserConfigSaveStaysInScheduledDir(t *testing.T) {
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Restore the working directory before the testing package removes the temp
	// directories; Windows cannot delete a directory that is still the cwd.
	defer func() { _ = os.Chdir(prevWD) }()
	keepUserConfigLatest(t)
	prevDelay := userConfigSaveDelay
	prevCache := userConfigCache
	prevWrites := userConfigWritesEnabled
	userConfigSaveDelay = 20 * time.Millisecond
	userConfigCache = nil
	// This test is about the writer actually landing a file; the rest of the
	// binary runs with the debounced writer disabled so tests never touch the
	// checkout.
	userConfigWritesEnabled = true
	t.Cleanup(func() {
		userConfigSaveMutex.Lock()
		if userConfigSaveTimer != nil {
			userConfigSaveTimer.Stop()
			userConfigSaveTimer = nil
		}
		pendingUserConfig = nil
		pendingUserConfigDir = ""
		userConfigSaveMutex.Unlock()
		userConfigSaveDelay = prevDelay
		userConfigCache = prevCache
		userConfigWritesEnabled = prevWrites
		_ = os.Chdir(prevWD)
	})

	dirA := t.TempDir()
	dirB := t.TempDir()
	if err := os.Chdir(dirA); err != nil {
		t.Fatal(err)
	}

	saveUserConfigDebounced(&UserConfig{
		Variables: UserVariables{
			ReducedMotion: true,
			Custom:        map[string]interface{}{"debounced": "yes"},
		},
	})

	// Simulate the directory going away before the timer fires.
	if err := os.Chdir(dirB); err != nil {
		t.Fatal(err)
	}

	pathA := filepath.Join(dirA, "jukauser.json")
	deadline := time.Now().Add(2 * time.Second)
	var data []byte
	var readErr error
	// Poll for a readable, non-empty file instead of stat-then-read: on Windows
	// a scanner or antivirus can briefly hold a freshly renamed file, and an
	// immediate read then fails with a sharing violation even though the save
	// landed correctly. The assertion is unchanged, only the wait is robust.
	for time.Now().Before(deadline) {
		data, readErr = os.ReadFile(pathA)
		if readErr == nil && len(data) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if readErr != nil {
		t.Fatalf("debounced save did not land in the scheduled directory: %v", readErr)
	}
	if len(data) == 0 {
		t.Fatal("debounced save wrote an empty file")
	}
	if _, err := os.Stat(filepath.Join(dirB, "jukauser.json")); !os.IsNotExist(err) {
		t.Errorf("debounced save leaked into the current directory (err=%v)", err)
	}
}

// Loading a config never schedules a jukauser.json write. Every load used to:
// at boot that write carried jukaconfig.json's values (with the Quick Settings
// levels zeroed) over the user's settings, so a crash or a power cut inside
// the debounce reset them.
func TestConfigLoadSchedulesNoUserSave(t *testing.T) {
	prevWrites := userConfigWritesEnabled
	userConfigWritesEnabled = true
	cancelPendingUserConfigSave(t)
	t.Cleanup(func() {
		cancelPendingUserConfigSave(t)
		userConfigWritesEnabled = prevWrites
	})

	if _, err := loadConfig("jukaconfig.json"); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	userConfigSaveMutex.Lock()
	pending := pendingUserConfig
	userConfigSaveMutex.Unlock()
	if pending != nil {
		t.Fatal("loading the config scheduled a jukauser.json save")
	}
}
