package main

import (
	"encoding/json"
	"os"
	"testing"
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

// A settings-only save must preserve favorites/chat state that the caller did
// not supply, and must not persist runtime-only custom keys.
func TestSaveUserConfigPreservesSections(t *testing.T) {
	t.Chdir(t.TempDir())

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
