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

func TestBackupMergeUsesLiveSettingsAndPlaytime(t *testing.T) {
	keepUserConfigLatest(t)
	prevCache, prevPlaytime := userConfigCache, playtime
	t.Cleanup(func() { userConfigCache, playtime = prevCache, prevPlaytime })
	userConfigCache = &UserConfig{
		Variables: UserVariables{Volume: 42, VolumeSet: true, Custom: map[string]interface{}{
			"theme_preset": "Boot", playtimeCustomKey: map[string]interface{}{"total": float64(10)},
		}},
		Favorites: &FavoritesStore{Videos: []FavoriteItem{{Type: "video"}}},
	}
	playtime = playtimeState{loaded: true, data: newPlaytimeData()}
	playtime.data.Total = 77
	live := &Config{}
	live.Variables.Custom = map[string]interface{}{
		"theme_preset": "Live", "recently_played": []interface{}{"Game"},
	}
	current := bkupCurrentUserConfig(live)
	if current.Variables.Custom["theme_preset"] != "Live" || current.Variables.Volume != 42 || current.Favorites == nil {
		t.Fatalf("current settings came from the boot cache: %+v", current)
	}
	// Playtime is its own file now: a backup's user config must not carry it,
	// or a restore would put a stale snapshot back where nothing reads it.
	if _, ok := current.Variables.Custom[playtimeCustomKey]; ok {
		t.Fatal("the backup still carries playtime inside the user config")
	}
	backup := &UserConfig{Variables: UserVariables{Custom: map[string]interface{}{
		"theme_preset": "Restored", playtimeCustomKey: map[string]interface{}{"total": float64(1)},
	}}}
	merged := bkupMergeUser(current, backup, map[string]bool{bkupCatSettings: true},
		bkupManifest{Platform: P().Name()}, nil)
	if merged.Variables.Custom["theme_preset"] != "Restored" {
		t.Fatalf("restored theme = %v", merged.Variables.Custom["theme_preset"])
	}
	if _, ok := merged.Variables.Custom[playtimeCustomKey]; ok {
		t.Fatalf("a settings restore put a playtime snapshot back into the config: %v", merged.Variables.Custom[playtimeCustomKey])
	}
	if _, ok := merged.Variables.Custom["recently_played"]; !ok || merged.Favorites == nil {
		t.Fatal("the current library was lost during a settings-only restore")
	}
}

func TestBackupRestoreDoesNotReviveRemovedCustomSettings(t *testing.T) {
	live := &Config{}
	live.Variables.Custom = map[string]interface{}{
		"old_setting": "should go", "search_results": "runtime only",
	}
	restored := &UserConfig{Variables: UserVariables{Custom: map[string]interface{}{
		"theme_preset": "Restored",
	}}}
	bkupMergeUserIntoLive(live, restored)
	if _, exists := live.Variables.Custom["old_setting"]; exists {
		t.Fatal("the next save would revive a setting removed by the restore")
	}
	if live.Variables.Custom["search_results"] != "runtime only" ||
		live.Variables.Custom["theme_preset"] != "Restored" {
		t.Fatalf("restored or runtime values were lost: %+v", live.Variables.Custom)
	}
}

func TestChosenFiveMinuteScreensaverSurvivesReload(t *testing.T) {
	chosen := &Config{}
	mergeUserConfig(chosen, &UserConfig{Variables: UserVariables{
		ScreensaverTimeout: 300,
		Custom:             map[string]interface{}{"screensaverTimeoutMigrated": true},
	}})
	if chosen.Variables.ScreensaverTimeout != 300 {
		t.Fatalf("explicit five-minute choice became %d seconds", chosen.Variables.ScreensaverTimeout)
	}
	legacy := &Config{}
	mergeUserConfig(legacy, &UserConfig{Variables: UserVariables{ScreensaverTimeout: 300}})
	if legacy.Variables.ScreensaverTimeout != 25 || legacy.Variables.Custom["screensaverTimeoutMigrated"] != true {
		t.Fatalf("legacy default was not migrated once: %+v", legacy.Variables)
	}
}

func TestFailedUserConfigReloadKeepsLatestSave(t *testing.T) {
	t.Chdir(t.TempDir())
	keepUserConfigLatest(t)
	prevCache := userConfigCache
	t.Cleanup(func() { userConfigCache = prevCache })
	userConfigCache = &UserConfig{
		Variables: UserVariables{Custom: map[string]interface{}{"theme_preset": "Boot"}},
		Favorites: &FavoritesStore{Videos: []FavoriteItem{{Type: "video"}}},
	}
	latest := UserConfig{Variables: UserVariables{ReducedMotion: true, Custom: map[string]interface{}{
		"theme_preset": "Current", "nested": map[string]interface{}{"count": float64(2)},
	}}}
	data, err := json.Marshal(latest)
	if err != nil {
		t.Fatal(err)
	}
	userConfigNoteLatest(data)
	got := reloadUserConfigFromDisk() // jukauser.json does not exist here.
	if !got.Variables.ReducedMotion || got.Variables.Custom["theme_preset"] != "Current" || got.Favorites == nil {
		t.Fatalf("failed reload used stale settings or dropped sections: %+v", got)
	}
	got.Variables.Custom["nested"].(map[string]interface{})["count"] = float64(9)
	if current := userConfigLatestVars().Custom["nested"].(map[string]interface{})["count"]; current != float64(2) {
		t.Fatalf("reload changed the stored latest settings: %v", current)
	}
}

func TestRecoveryResetKeepsLiveDeviceChoices(t *testing.T) {
	shipped := &Config{}
	shipped.Variables.Custom = map[string]interface{}{"theme_preset": "Shipped"}
	live := &Config{}
	live.Variables.Fullscreen = true
	live.Variables.Custom = map[string]interface{}{
		"theme_preset": "Live", "swapABButtons": true,
	}
	data, err := recoveryResetUser(shipped, live)
	if err != nil {
		t.Fatal(err)
	}
	var reset UserConfig
	if err := json.Unmarshal(data, &reset); err != nil {
		t.Fatal(err)
	}
	if !reset.Variables.Fullscreen || reset.Variables.Custom["swapABButtons"] != true ||
		reset.Variables.Custom["theme_preset"] != "Shipped" {
		t.Fatalf("reset kept the wrong settings: %+v", reset.Variables)
	}
}

func TestConfigHistoryRestoreSurvivesExitSaves(t *testing.T) {
	design, err := os.ReadFile("jukaconfig.json")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if err := os.WriteFile("jukaconfig.json", design, 0o644); err != nil {
		t.Fatal(err)
	}
	keepUserConfigLatest(t)
	prevCache, prevPlaytime, prevConfig := userConfigCache, playtime, appConfig
	prevWrites := userConfigWritesEnabled
	userConfigWritesEnabled = true
	t.Cleanup(func() {
		userConfigCache, playtime, appConfig = prevCache, prevPlaytime, prevConfig
		userConfigWritesEnabled = prevWrites
	})
	userConfigCache = &UserConfig{Variables: UserVariables{Custom: map[string]interface{}{"theme_preset": "Boot"}}}
	live, _, err := LoadXMLConfigWithFallback("jukaconfig.json")
	if err != nil {
		t.Fatal(err)
	}
	live.Variables.Custom["theme_preset"] = "Live"
	playtime = playtimeState{loaded: true, data: newPlaytimeData(), lastTick: time.Now()}
	playtime.data.Total = 99
	snapshot := UserConfig{Variables: UserVariables{ReducedMotion: true, Custom: map[string]interface{}{
		"theme_preset": "Snapshot", playtimeCustomKey: map[string]interface{}{"total": float64(7)},
	}}}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("snapshot.json", data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := powerRestoreUserSnapshot(live, "snapshot.json"); err != nil {
		t.Fatal(err)
	}
	if !live.Variables.ReducedMotion || live.Variables.Custom["theme_preset"] != "Snapshot" {
		t.Fatalf("running settings did not receive the snapshot: %+v", live.Variables)
	}
	if playtime.data.Total != 99 {
		// Playtime is its own file (LIB-13), so a settings snapshot neither
		// brings playtime in nor drops the session that is running: the 99 is
		// the live one, and the snapshot's legacy 7 is only to be deleted.
		t.Fatalf("a settings restore changed the running playtime to %d", playtime.data.Total)
	}
	// The two writes at exit used to put the old session back over a restore.
	playtimeFlush(live)
	saveUserConfig(userConfigCache)
	saved, err := os.ReadFile("jukauser.json")
	if err != nil {
		t.Fatal(err)
	}
	var got UserConfig
	if err := json.Unmarshal(saved, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Variables.ReducedMotion || got.Variables.Custom["theme_preset"] != "Snapshot" {
		t.Fatalf("exit save overwrote the restored settings: %+v", got.Variables)
	}
	if _, ok := got.Variables.Custom[playtimeCustomKey]; ok {
		// The snapshot is playtime.json now; the exit save must not put it back
		// into the user config (LIB-13).
		t.Fatalf("the exit save wrote playtime into the user config: %v", got.Variables.Custom[playtimeCustomKey])
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
		"theme_preset": "Midnight",
	}
	saveUserConfig(userConfigFromVariables(live))
	userConfigCache.Favorites = &FavoritesStore{Videos: []FavoriteItem{{Type: "video"}, {Type: "video"}}}
	saveUserConfig(userConfigCache)
	got := read()
	if got.Variables.Custom["theme_preset"] != "Midnight" {
		t.Fatalf("a section save put back theme %v", got.Variables.Custom["theme_preset"])
	}
	if _, ok := got.Variables.Custom[playtimeCustomKey]; ok {
		// Playtime has its own files (LIB-13): nothing in a save path writes
		// it into the user config any more.
		t.Fatalf("a save put playtime into the config: %v", got.Variables.Custom[playtimeCustomKey])
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
