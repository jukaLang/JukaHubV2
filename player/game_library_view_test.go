package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// resetLibraryViewForTest points the view store at a temporary file and clears
// its loaded state, then restores the globals for the next test.
func resetLibraryViewForTest(t *testing.T) string {
	t.Helper()
	prevPath := libraryViewPath
	prevLoaded := libraryViewStore.loaded
	prevData := libraryViewStore.data
	path := filepath.Join(t.TempDir(), "game-library.json")
	libraryViewPath = func() string { return path }
	libraryViewStore.mu.Lock()
	libraryViewStore.loaded = true
	libraryViewStore.data = libraryViewEmpty()
	libraryViewStore.mu.Unlock()
	t.Cleanup(func() {
		libraryViewPath = prevPath
		libraryViewStore.mu.Lock()
		libraryViewStore.loaded = prevLoaded
		libraryViewStore.data = prevData
		libraryViewStore.mu.Unlock()
	})
	return path
}

func reloadLibraryView(t *testing.T) {
	t.Helper()
	libraryViewStore.mu.Lock()
	libraryViewStore.loaded = false
	libraryViewStore.mu.Unlock()
	_ = libraryViewGame("")
}

func libraryLabels(games []libraryGame) []string {
	out := make([]string, len(games))
	for i, game := range games {
		out[i] = game.Label()
	}
	return out
}

func wantLabels(t *testing.T, got []libraryGame, want ...string) {
	t.Helper()
	labels := libraryLabels(got)
	if len(labels) != len(want) {
		t.Fatalf("order = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("order = %v, want %v", labels, want)
		}
	}
}

func TestLibrarySortModesPutMissingValuesLast(t *testing.T) {
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	games := []libraryGame{
		{Path: "/b", Title: "Beta", LastPlayed: base},
		{Path: "/a", Title: "Alpha", LastPlayed: base.Add(2 * time.Hour), Playtime: 3600, Rating: 4, Year: 1999},
		{Path: "/c", Title: "Charlie", LastPlayed: base.Add(time.Hour), Playtime: 7200, Rating: 5, Year: 2001},
		{Path: "/d", Title: "Delta"},
	}
	cases := []struct {
		mode string
		want []string
	}{
		{librarySortTitle, []string{"Alpha", "Beta", "Charlie", "Delta"}},
		{librarySortLast, []string{"Alpha", "Charlie", "Beta", "Delta"}},
		{librarySortTime, []string{"Charlie", "Alpha", "Beta", "Delta"}},
		{librarySortRate, []string{"Charlie", "Alpha", "Beta", "Delta"}},
		{librarySortYear, []string{"Charlie", "Alpha", "Beta", "Delta"}},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			got := append([]libraryGame(nil), games...)
			sortLibraryGames(got, tc.mode)
			wantLabels(t, got, tc.want...)
		})
	}

	// The title sort uses the display title, which is the whole point of the
	// rename: the file-derived title is still the metadata key underneath.
	got := []libraryGame{{Path: "/z", Title: "Zulu"}, {Path: "/a", Title: "Alpha", DisplayTitle: "Aardvark"}}
	sortLibraryGames(got, librarySortTitle)
	wantLabels(t, got, "Aardvark", "Zulu")
}

func TestLibraryHideAndShowHiddenPersist(t *testing.T) {
	resetLibraryViewForTest(t)
	key := "content-key"
	setLibraryGameHidden(key, true)
	if !libraryViewGame(key).Hidden {
		t.Fatal("the game was not stored as hidden")
	}

	// A restart rereads the same document.
	reloadLibraryView(t)
	if !libraryViewGame(key).Hidden {
		t.Fatal("hidden state did not survive a reload")
	}

	setLibraryShowHidden(true)
	if !libraryShowHidden() {
		t.Fatal("Show hidden was not stored")
	}
	cycleLibrarySort(1)
	mode := librarySortMode()
	if mode == librarySortTitle {
		t.Fatal("the sort mode did not move off Title")
	}
	reloadLibraryView(t)
	if !libraryShowHidden() {
		t.Fatal("Show hidden did not survive a reload")
	}
	if got := librarySortMode(); got != mode {
		t.Fatalf("sort after reload = %q, want %q", got, mode)
	}

	hidden := []librarySystem{{Name: "GBA", Games: []libraryGame{
		{Title: "One", Hidden: true},
		{Title: "Two"},
	}}}
	if got := filterHiddenLibrarySystems(hidden, false); len(got) != 1 || len(got[0].Games) != 1 || got[0].Games[0].Title != "Two" {
		t.Fatalf("hidden filter = %+v", got)
	}
	if got := filterHiddenLibrarySystems(hidden, true); len(got) != 1 || len(got[0].Games) != 2 {
		t.Fatalf("Show hidden = %+v", got)
	}
}

func TestLibraryDisplayTitleSurvivesSaveReloadAndFileRename(t *testing.T) {
	resetLibraryViewForTest(t)
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "Kart.gba")
	if err := os.WriteFile(oldPath, []byte("same cartridge bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	key := libraryContentKey(oldPath)
	setLibraryGameDisplayTitle(key, "  My   Kart  ")
	if got := libraryViewGame(key).DisplayTitle; got != "My Kart" {
		t.Fatalf("display title = %q", got)
	}

	reloadLibraryView(t)
	if got := libraryViewGame(key).DisplayTitle; got != "My Kart" {
		t.Fatalf("display title after reload = %q", got)
	}

	// Renaming the file must not change the content key, so the name and the
	// hidden flag stay with the cartridge.
	setLibraryGameHidden(key, true)
	newPath := filepath.Join(dir, "Kart (USA).gba")
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	if got := libraryContentKey(newPath); got != key {
		t.Fatalf("content key changed after rename: %q -> %q", key, got)
	}
	game := libraryGame{Path: newPath, System: "Game Boy Advance", Title: "Kart"}
	libraryDecorateGame(&game)
	if got := game.Label(); got != "My Kart" {
		t.Errorf("display title after rename = %q, want My Kart", got)
	}
	if !game.Hidden {
		t.Error("hidden flag did not follow the renamed file")
	}
}
