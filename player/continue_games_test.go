package main

// continue_games_test.go pins the Continue card's game wiring. The card used to
// read only the media favourites store, so "Nothing played recently" stayed on
// screen after any number of games — including games launched from the library,
// which were already being recorded.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withRecentFeeds replaces both recents sources for one test.
func withRecentFeeds(t *testing.T, games []recentGame, media []FavoriteItem) {
	t.Helper()
	resetGameRecentsState(t)
	gameRecentsFile = func() string { return filepath.Join(t.TempDir(), "game-recents.json") }
	gameRecentsMu.Lock()
	gameRecents = append([]recentGame(nil), games...)
	gameRecentsLoaded = true
	gameRecentsMu.Unlock()

	favoritesMutex.Lock()
	prev := favoritesStore.Recent
	favoritesStore.Recent = media
	favoritesMutex.Unlock()
	t.Cleanup(func() {
		favoritesMutex.Lock()
		favoritesStore.Recent = prev
		favoritesMutex.Unlock()
	})
}

func TestContinueMergesGameAndMediaRecents(t *testing.T) {
	base := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	withRecentFeeds(t,
		[]recentGame{
			{Path: "/roms/GBA/Zelda.gba", System: "Game Boy Advance", Title: "Zelda", PlayedAt: base},
			{Path: "/roms/NES/Contra.nes", System: "NES", Title: "Contra", PlayedAt: base.Add(-2 * time.Hour)},
		},
		[]FavoriteItem{{
			Type:      "video",
			Data:      VideoInfo{Title: "A Video", URL: "https://example.com/v"},
			Timestamp: base.Add(-time.Hour),
		}},
	)

	items := getRecentItems()
	if len(items) != 3 {
		t.Fatalf("got %d items, want both games and the video", len(items))
	}
	newest := items[0]
	if newest.Label() != "Zelda" || newest.Type != "game" {
		t.Errorf("newest item = %q (%s), want the most recent game", newest.Label(), newest.Type)
	}
	if items[1].Type != "video" {
		t.Errorf("second item = %s, want the video played an hour ago", items[1].Type)
	}
	oldest := items[2]
	if oldest.Label() != "Contra" {
		t.Errorf("third item = %q, want the older game", oldest.Label())
	}

	// The Continue tile's supporting line names the console, not "Game".
	if got := recentSourceLabel(items[0]); !strings.HasPrefix(got, "Game Boy Advance · ") {
		t.Errorf("subtitle = %q, want the console and when it was played", got)
	}
	if p := homeRecentProgress(items[0]); p != 0 {
		t.Errorf("game progress = %v, want 0 (no playback position)", p)
	}
}

func TestContinueGameItemLabelAndIcon(t *testing.T) {
	it := gameRecentItem(recentGame{Path: "/roms/GBA/Zelda.gba", System: "GBA", Title: "Zelda"})
	if got := it.Label(); got != "Zelda" {
		t.Errorf("label = %q, want the game title", got)
	}
	if got := recentItemIcon(it); got != "game" {
		t.Errorf("icon = %q, want the game glyph", got)
	}
	// A recorded entry without a title still shows something readable.
	untitled := gameRecentItem(recentGame{Path: "/roms/GBA/Zelda.gba"})
	if got := untitled.Label(); got != "Zelda.gba" {
		t.Errorf("untitled game label = %q, want the file name", got)
	}
}

// TestPlayRecentGameReportsAMissingROM covers the failure path without starting a
// process: Continue must say what is wrong instead of doing nothing.
func TestPlayRecentGameReportsAMissingROM(t *testing.T) {
	clearToastAfterTest(t)
	currentToast = ToastState{}
	playRecentGame(recentGame{
		Path:   filepath.Join(t.TempDir(), "gone.gba"),
		System: "Game Boy Advance",
		Title:  "Gone",
	})
	if currentToast.Kind != ToastKindError {
		t.Fatalf("toast kind = %v, want an error", currentToast.Kind)
	}
	if !strings.Contains(currentToast.Message, "ROM is missing") {
		t.Errorf("toast = %q, want it to name the missing ROM", currentToast.Message)
	}
}

// TestPlayRecentGameWithoutAnEmulator covers the other failure the card can hit:
// the ROM is there but the system has no launcher configured or detected.
func TestPlayRecentGameWithoutAnEmulator(t *testing.T) {
	clearToastAfterTest(t)
	currentToast = ToastState{}
	rom := filepath.Join(t.TempDir(), "game.gba")
	if err := os.WriteFile(rom, []byte("rom"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A system name no emulator entry or launcher override can match.
	playRecentGame(recentGame{Path: rom, System: "Totally Unknown System", Title: "Mystery"})
	if currentToast.Kind != ToastKindWarning {
		t.Fatalf("toast kind = %v, want a warning", currentToast.Kind)
	}
	if !strings.Contains(currentToast.Message, "No emulator found") {
		t.Errorf("toast = %q, want it to explain the missing emulator", currentToast.Message)
	}
}
