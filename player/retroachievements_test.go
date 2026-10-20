package main

// RetroAchievements tests: SD-card credential discovery, the API/cache shape,
// and the overlay's render/hit geometry. Network tests use httptest, so no real
// RA account or SD card is required.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// The RA probe uses its own hidden window and keeps both alive for the whole
// test binary. The panel populates the shared text-texture cache, and those
// textures must not outlive their renderer: a later theme test calls
// ClearTextCache, which would otherwise destroy stale textures and crash. A
// separate window also avoids interfering with tests that own renderWin.
var (
	raTestWindow   *sdl.Window
	raTestRenderer *sdl.Renderer
)

func renderRATestProbe(t *testing.T, screen func(*sdl.Renderer)) *probeFrame {
	t.Helper()
	f := newProbeFrame(t)
	if raTestRenderer == nil {
		win, err := sdl.CreateWindow("retroachievements-probe", 0, 0, probeW, probeH, sdl.WINDOW_HIDDEN)
		if err != nil {
			t.Skipf("software window unavailable: %v", err)
		}
		r, err := sdl.CreateRenderer(win, -1, sdl.RENDERER_SOFTWARE)
		if err != nil {
			win.Destroy()
			t.Skipf("software renderer unavailable: %v", err)
		}
		raTestWindow, raTestRenderer = win, r
	}
	raTestRenderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	raTestRenderer.Clear()
	screen(raTestRenderer)
	if err := raTestRenderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	return f
}

func newRATestEnv(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	settings := filepath.Join(dir, "retroachievements.json")
	cache := filepath.Join(dir, "retroachievements-cache.json")

	oldSettings, oldCache := raSettingsPathOverride, raCachePathOverride
	oldCheevos, oldSpruce := raCheevosKeyPaths, raSpruceConfigPaths
	oldCheevosSettings, oldRAConfigs := raCheevosSettingsPaths, raRetroArchConfigPaths
	oldGlobs, oldBase, oldClient := raRetroArchConfigGlobs, raAPIBase, raHTTPClient

	raSettingsPathOverride = settings
	raCachePathOverride = cache
	raCheevosKeyPaths = nil
	raSpruceConfigPaths = nil
	raCheevosSettingsPaths = nil
	raRetroArchConfigPaths = nil
	raRetroArchConfigGlobs = nil
	raAPIBase = raAPIBaseDefault
	raHTTPClient = &http.Client{Timeout: raFetchTimeout}

	raResetStateForTests()
	raPanel = raPanelState{}
	raPanelRect = sdl.Rect{}
	raRowHits = nil
	raCloseRect, raRefreshRect, raSettingsRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}

	t.Cleanup(func() {
		raSettingsPathOverride, raCachePathOverride = oldSettings, oldCache
		raCheevosKeyPaths, raSpruceConfigPaths = oldCheevos, oldSpruce
		raCheevosSettingsPaths, raRetroArchConfigPaths = oldCheevosSettings, oldRAConfigs
		raRetroArchConfigGlobs, raAPIBase, raHTTPClient = oldGlobs, oldBase, oldClient
		raResetStateForTests()
		raPanel = raPanelState{}
		raPanelRect = sdl.Rect{}
		raRowHits = nil
		raCloseRect, raRefreshRect, raSettingsRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	})
	return settings, cache
}

const testRAKey = "0123456789abcdef0123456789abcdef"

func TestRetroAchievementsDetectsSDCardCredentials(t *testing.T) {
	newRATestEnv(t)
	root := t.TempDir()
	cfg := filepath.Join(root, "retroarch-smartpro.cfg")
	if err := os.WriteFile(cfg, []byte("cheevos_username = \"PlayerOne\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "apikey.txt")
	// Windows Notepad commonly saves this file as UTF-8 with a BOM.
	if err := os.WriteFile(keyPath, []byte("\ufeff"+testRAKey+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raRetroArchConfigPaths = []string{cfg}
	raCheevosKeyPaths = []string{keyPath}

	creds := raEffectiveCredentials()
	if creds.Username != "PlayerOne" {
		t.Fatalf("username = %q, want PlayerOne", creds.Username)
	}
	if creds.APIKey != testRAKey {
		t.Fatalf("API key = %q, want the key from apikey.txt", creds.APIKey)
	}
	if creds.UsernameSource != "RetroArch config" {
		t.Fatalf("username source = %q", creds.UsernameSource)
	}
	if creds.KeySource != "Cheevos app" {
		t.Fatalf("key source = %q", creds.KeySource)
	}
}

func TestRetroAchievementsReadsSpruceWebAPIKey(t *testing.T) {
	newRATestEnv(t)
	root := t.TempDir()
	spruce := filepath.Join(root, "spruce-config.json")
	doc := `{
	  "menuOptions": {
	    "RetroAchievements Settings": {"username": {"selected": "SpruceUser"}},
	    "Web API": {"webApiKey": "` + testRAKey + `"}
	  }
	}`
	if err := os.WriteFile(spruce, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	raSpruceConfigPaths = []string{spruce}
	creds := raEffectiveCredentials()
	if creds.Username != "SpruceUser" || creds.APIKey != testRAKey {
		t.Fatalf("creds = %+v, want Spruce username and key", creds)
	}
	if !creds.complete() {
		t.Fatal("Spruce credentials should be complete")
	}
}

func TestRetroAchievementsSettingsAndCachePersist(t *testing.T) {
	_, cachePath := newRATestEnv(t)
	want := raSettings{Username: "PlayerOne", APIKey: testRAKey}
	if err := raSaveSettings(want); err != nil {
		t.Fatal(err)
	}
	// Saving again replaces the existing file atomically (Windows included).
	if err := raSaveSettings(want); err != nil {
		t.Fatalf("settings overwrite failed: %v", err)
	}
	raResetStateForTests()
	if got := raLoadSettings(); got != want {
		t.Fatalf("settings round-trip = %+v, want %+v", got, want)
	}

	cache := raCache{
		Version:   raCacheVersion,
		Username:  "PlayerOne",
		UpdatedAt: time.Now().Unix(),
		Recent: []raAchievement{
			{AchievementID: 7, Title: "First", GameTitle: "Game", Points: 5, Date: "2026-10-18 10:00:00"},
		},
	}
	if err := raSaveCache(cache); err != nil {
		t.Fatal(err)
	}
	raResetStateForTests()
	raEnsureCacheLoaded()
	raState.mu.Lock()
	got := raState.cache
	raState.mu.Unlock()
	if got.Username != "PlayerOne" || len(got.Recent) != 1 || got.Recent[0].Title != "First" {
		t.Fatalf("cache round-trip = %+v", got)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache file was not written: %v", err)
	}
}

func TestRetroAchievementsFetchParsesAndSorts(t *testing.T) {
	newRATestEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("u") != "PlayerOne" || r.URL.Query().Get("y") != testRAKey {
			t.Errorf("request did not carry the account credentials: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/API/API_GetUserProfile.php":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"User": "PlayerOne", "TotalPoints": 1234, "TotalTruePoints": 5678,
				"RichPresenceMsg": "Playing Test Game",
			})
		case "/API/API_GetUserRecentAchievements.php":
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{"Date": "2026-10-17 08:00:00", "AchievementID": 1, "Title": "Older", "Points": 5, "GameTitle": "Game A", "GameID": 10},
				{"Date": "2026-10-19 09:00:00", "AchievementID": 2, "Title": "Newer", "Points": 10, "GameTitle": "Game B", "GameID": 20, "HardcoreMode": 1},
				{"Date": "2026-10-18 09:00:00", "AchievementID": 101000001, "Title": "Unsupported emulator", "Points": 0, "GameTitle": "Game B", "GameID": 20},
			})
		case "/API/API_GetUserCompletionProgress.php":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"Count": 2, "Total": 2,
				"Results": []map[string]interface{}{
					{"GameID": 10, "Title": "Game A", "MaxPossible": 20, "NumAwarded": 5, "MostRecentAwardedDate": "2026-10-17T08:00:00+00:00"},
					{"GameID": 20, "Title": "Game B", "MaxPossible": 10, "NumAwarded": 10, "MostRecentAwardedDate": "2026-10-19T09:00:00+00:00"},
					{"GameID": 30, "Title": "No achievements", "MaxPossible": 0, "NumAwarded": 0},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	raAPIBase = server.URL + "/API"
	raHTTPClient = server.Client()

	cache, err := raFetchAll(context.Background(), raCredentials{Username: "PlayerOne", APIKey: testRAKey}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if cache.Username != "PlayerOne" || cache.Profile.TotalPoints != 1234 {
		t.Fatalf("cache profile = %+v", cache.Profile)
	}
	if len(cache.Recent) != 2 || cache.Recent[0].Title != "Newer" || cache.Recent[1].Title != "Older" {
		t.Fatalf("recent unlocks were not sorted/filtered: %+v", cache.Recent)
	}
	if len(cache.Progress) != 2 || cache.Progress[0].Title != "Game B" {
		t.Fatalf("progress = %+v, want two games sorted by award date", cache.Progress)
	}
	if pct := raAwardPercent(cache.Progress[0].NumAwarded, cache.Progress[0].MaxPossible); pct != 100 {
		t.Fatalf("Game B percentage = %d, want 100", pct)
	}
}

func TestRetroAchievementsPanelRendersOverviewAndSetup(t *testing.T) {
	newRATestEnv(t)
	if err := raSaveSettings(raSettings{Username: "PlayerOne", APIKey: testRAKey}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	raState.mu.Lock()
	raState.loaded = true
	raState.cache = raCache{
		Version:   raCacheVersion,
		Username:  "PlayerOne",
		UpdatedAt: now.Unix(),
		Profile:   raProfile{User: "PlayerOne", TotalPoints: 1234, TotalTruePoints: 5678, RichPresence: "Playing Test Game"},
		Recent: []raAchievement{
			{AchievementID: 1, Title: "First unlock", GameTitle: "Game A", ConsoleName: "SNES", Points: 5, Date: "2026-10-19 09:00:00", HardcoreMode: 1},
		},
		Progress: []raGameProgress{
			{GameID: 10, Title: "Game A", MaxPossible: 20, NumAwarded: 5, MostRecentAwardedDate: "2026-10-19T09:00:00+00:00"},
		},
	}
	raState.mu.Unlock()

	prevW, prevH := screenWidth, screenHeight
	prevAccent := accentColor
	screenWidth, screenHeight = probeW, probeH
	SetAccentColor(sdl.Color{R: 52, G: 152, B: 219, A: 255})
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		setAccentColorRaw(prevAccent)
	})

	raPanel = raPanelState{open: true}
	f := renderRATestProbe(t, func(r *sdl.Renderer) {
		renderRetroAchievementsPanel(r, &Config{})
	})
	if raPanelRect.W <= 0 || raPanelRect.H <= 0 {
		t.Fatalf("panel rect not set: %+v", raPanelRect)
	}
	if len(raRowHits) != 1 || raRowHits[0].kind != raHitRecent {
		t.Fatalf("overview row hits = %+v, want one recent unlock", raRowHits)
	}
	if got := f.countColor(accentColor, raPanelRect.X, raPanelRect.Y, raPanelRect.X+raPanelRect.W, raPanelRect.Y+raPanelRect.H); got < 50 {
		t.Fatalf("overview painted only %d accent pixels", got)
	}

	// Setup mode has two clickable credential rows and never shows the key.
	raPanel = raPanelState{open: true, settings: true}
	raState.mu.Lock()
	raState.cache = raCache{}
	raState.mu.Unlock()
	renderRATestProbe(t, func(r *sdl.Renderer) {
		renderRetroAchievementsPanel(r, &Config{})
	})
	if len(raRowHits) != 2 || raRowHits[0].kind != raHitUsername || raRowHits[1].kind != raHitAPIKey {
		t.Fatalf("setup row hits = %+v, want username and API key", raRowHits)
	}
}

func TestRetroAchievementsEmulatorEntryPinned(t *testing.T) {
	newRATestEnv(t)
	if err := raSaveSettings(raSettings{Username: "PlayerOne", APIKey: testRAKey}); err != nil {
		t.Fatal(err)
	}
	invalidateEmulatorCache()
	t.Cleanup(invalidateEmulatorCache)
	entries := emulatorEntries()
	if len(entries) == 0 {
		t.Fatal("emulator catalog is empty")
	}
	if entries[0].Name != retroAchievementsEntryName || !entries[0].Pinned {
		t.Fatalf("first row = %+v, want pinned RetroAchievements", entries[0])
	}
	if entries[0].Status != emuAvailable {
		t.Fatalf("RetroAchievements status = %v, want available", entries[0].Status)
	}
}

func TestRetroAchievementsStartMenuEntry(t *testing.T) {
	newRATestEnv(t)
	if err := raSaveSettings(raSettings{Username: "PlayerOne", APIKey: testRAKey}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, app := range getStartMenuApps(&Config{}) {
		if app.name == retroAchievementsEntryName && app.action != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("Start menu has no RetroAchievements entry")
	}
}

func TestRetroAchievementsFromEmuReturnsToList(t *testing.T) {
	newRATestEnv(t)
	if err := raSaveSettings(raSettings{Username: "PlayerOne", APIKey: testRAKey}); err != nil {
		t.Fatal(err)
	}
	raState.mu.Lock()
	raState.loaded = true
	raState.cache = raCache{Version: raCacheVersion, Username: "PlayerOne", UpdatedAt: time.Now().Unix()}
	raState.mu.Unlock()
	emuPanel = emuPanelState{open: true, cursor: 3, scroll: 1}
	t.Cleanup(func() { emuPanel = emuPanelState{} })

	openRetroAchievementsPanelFromEmu()
	if !raPanel.open || emuPanel.open {
		t.Fatal("opening RA from Emulators did not replace the list")
	}
	closeRetroAchievementsPanel()
	if !emuPanel.open || emuPanel.cursor != 3 || emuPanel.scroll != 1 {
		t.Fatalf("closing RA did not restore the list: %+v", emuPanel)
	}
}

func TestPlaytimeYOpensRetroAchievements(t *testing.T) {
	newRATestEnv(t)
	if err := raSaveSettings(raSettings{Username: "PlayerOne", APIKey: testRAKey}); err != nil {
		t.Fatal(err)
	}
	raState.mu.Lock()
	raState.loaded = true
	raState.cache = raCache{Version: raCacheVersion, Username: "PlayerOne", UpdatedAt: time.Now().Unix()}
	raState.mu.Unlock()
	raPanel = raPanelState{}
	mod, ok := lookupScene(playtimeSceneName)
	if !ok {
		t.Fatal("Playtime scene is not registered")
	}
	e := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_y}}
	if !mod.handleKey(e, &Config{}) {
		t.Fatal("Playtime did not consume Y")
	}
	if !raPanel.open {
		t.Fatal("Y did not open RetroAchievements")
	}
}

func TestRetroAchievementsRejectsShortAPIKey(t *testing.T) {
	if _, err := raNormalizeAPIKey("too-short"); err == nil {
		t.Fatal("short key was accepted")
	}
	if _, err := raNormalizeAPIKey(strings.Repeat("z", 32)); err != nil {
		t.Fatalf("32-character key rejected: %v", err)
	}
}
