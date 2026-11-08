package main

// favorites_undo_test.go — removing a favourite is visible and reversible: X
// (or Delete) takes the focused row away, the Undo bar puts it back exactly
// where it was, and a video that is also downloaded asks whether its file
// goes too instead of deleting it behind the user's back.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// favoritesTestState gives one test the favourites store, the Videos tab and
// the removal prompts to itself. saveFavorites only updates the cached user
// config in the suite (TestMain turns config writes off); that is put back
// too.
func favoritesTestState(t *testing.T, videos ...VideoInfo) {
	t.Helper()
	favoritesMutex.Lock()
	prevStore := favoritesStore
	store := FavoritesStore{Videos: []FavoriteItem{}, Recent: []FavoriteItem{}, Files: []FavoriteItem{}, IPTV: []FavoriteItem{}}
	for i, v := range videos {
		store.Videos = append(store.Videos, FavoriteItem{Type: "video", Data: v, Timestamp: time.Unix(int64(1000-i), 0)})
	}
	favoritesStore = store
	favoritesMutex.Unlock()
	prevTab, prevFocus := favoritesCurrentTab, favoritesFocusIndex
	prevUndo, prevPrompt := favoritesUndo, favoritesDeletePrompt
	prevCache := userConfigCache
	var prevFavs *FavoritesStore
	if prevCache != nil {
		prevFavs = prevCache.Favorites
	}
	t.Cleanup(func() {
		favoritesMutex.Lock()
		favoritesStore = prevStore
		favoritesMutex.Unlock()
		favoritesCurrentTab, favoritesFocusIndex = prevTab, prevFocus
		favoritesUndo, favoritesDeletePrompt = prevUndo, prevPrompt
		if prevCache != nil {
			prevCache.Favorites = prevFavs
		}
		userConfigCache = prevCache
	})
	favoritesCurrentTab, favoritesFocusIndex = favTabVideos, 0
	favoritesUndo = favoritesUndoState{}
	favoritesDeletePrompt.open = false
}

// favoritesTitles lists the titles the current tab shows.
func favoritesTitles() []string {
	items := getCurrentFavorites()
	out := make([]string, 0, len(items))
	for _, it := range items {
		if v, ok := it.Data.(VideoInfo); ok {
			out = append(out, v.Title)
		}
	}
	return out
}

func TestFavoritesRemoveCanBeUndoneInPlace(t *testing.T) {
	favoritesTestState(t,
		VideoInfo{Title: "A", WebpageURL: "https://example.com/a"},
		VideoInfo{Title: "B", WebpageURL: "https://example.com/b"},
		VideoInfo{Title: "C", WebpageURL: "https://example.com/c"},
	)
	dlLibraryTestState(t)
	favoritesFocusIndex = 1

	favoritesRemoveFocused(nil)
	if got := favoritesTitles(); len(got) != 2 || got[0] != "A" || got[1] != "C" {
		t.Fatalf("after removing B the tab shows %q", got)
	}
	if !favoritesUndoActive() {
		t.Fatal("the removal was not offered back")
	}
	if favoritesDeletePrompt.open {
		t.Fatal("a video with no download asked about deleting a file")
	}
	if !favoritesUndoRemove() {
		t.Fatal("Undo did nothing")
	}
	if got := favoritesTitles(); len(got) != 3 || got[1] != "B" {
		t.Fatalf("after Undo the tab shows %q, want B back in the middle", got)
	}
	if favoritesUndoActive() || favoritesUndoRemove() {
		t.Fatal("one removal was undone twice")
	}
}

func TestFavoritesUndoExpires(t *testing.T) {
	favoritesTestState(t, VideoInfo{Title: "A", WebpageURL: "https://example.com/a"})
	dlLibraryTestState(t)
	favoritesRemoveFocused(nil)
	favoritesUndo.at = time.Now().Add(-favoritesUndoWindow - time.Second)
	if favoritesUndoActive() || favoritesUndoRemove() {
		t.Fatal("a removal was undone after the Undo bar had gone")
	}
	if got := favoritesTitles(); len(got) != 0 {
		t.Fatalf("the tab shows %q, want it empty", got)
	}
}

// A favourite that is also downloaded asks whether the file goes too: Keep
// leaves it in Downloads, Delete frees the space.
func TestFavoritesRemoveAsksBeforeDeletingTheDownload(t *testing.T) {
	page := "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	favoritesTestState(t, VideoInfo{Title: "Song", WebpageURL: page})
	root := dlLibraryTestState(t)
	file := filepath.Join(root, "video", "Song.mp4")
	dlWriteTestFile(t, file, 3)
	dlLibrary.mu.Lock()
	dlLibrary.entries = []dlLibraryEntry{{ID: "s", Path: file, Video: true, Source: page, Size: 3, Title: "Song"}}
	dlLibrary.mu.Unlock()

	favoritesRemoveFocused(nil)
	if !favoritesDeletePrompt.open || favoritesDeletePrompt.libID != "s" {
		t.Fatalf("prompt = open %v id %q, want a question about entry s", favoritesDeletePrompt.open, favoritesDeletePrompt.libID)
	}
	// A then picks the highlighted button, Keep file.
	favoritesPromptInput(false, false, true, false)
	if favoritesDeletePrompt.open {
		t.Fatal("Keep did not close the prompt")
	}
	if _, ok := dlLibraryFindID("s"); !ok {
		t.Fatal("Keep file removed the download")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("Keep file deleted the file: %v", err)
	}

	if !favoritesUndoRemove() {
		t.Fatal("the favourite could not be restored")
	}
	favoritesFocusIndex = 0
	favoritesRemoveFocused(nil)
	if !favoritesDeletePrompt.open {
		t.Fatal("the second removal did not ask about the download")
	}
	favoritesPromptInput(false, true, false, false)
	favoritesPromptInput(false, false, true, false)
	if favoritesDeletePrompt.open {
		t.Fatal("Delete did not close the prompt")
	}
	if _, ok := dlLibraryFindID("s"); ok {
		t.Fatal("Delete file left the download listed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(file); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Delete file left the file on the card")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestFavoritesKeysRemoveAndUndo(t *testing.T) {
	favoritesTestState(t,
		VideoInfo{Title: "A", WebpageURL: "https://example.com/a"},
		VideoInfo{Title: "B", WebpageURL: "https://example.com/b"},
	)
	dlLibraryTestState(t)

	handleFavoritesInput(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_DELETE}}, nil)
	if got := favoritesTitles(); len(got) != 1 || got[0] != "B" {
		t.Fatalf("Delete left %q, want only B", got)
	}
	handleFavoritesInput(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_z, Mod: sdl.KMOD_CTRL}}, nil)
	if got := favoritesTitles(); len(got) != 2 || got[0] != "A" {
		t.Fatalf("Ctrl+Z left %q, want A back first", got)
	}
	pad := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_X}
	if !handleFavoritesController(pad, nil) {
		t.Fatal("X was not handled on Favorites")
	}
	if got := favoritesTitles(); len(got) != 1 {
		t.Fatalf("X left %q, want one row", got)
	}
	pad.Button = sdl.CONTROLLER_BUTTON_Y
	if !handleFavoritesController(pad, nil) || len(favoritesTitles()) != 2 {
		t.Fatal("Y did not take the removal back")
	}
}

// The footer names what X and Y do here, and the prompt's own keys while it
// is up.
func TestFavoritesFooterFollowsTheRemoval(t *testing.T) {
	favoritesTestState(t, VideoInfo{Title: "A", WebpageURL: "https://example.com/a"})
	base := func() []FooterHint {
		return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Open"}, {Button: "B", Label: "Back"}}
	}
	has := func(hints []FooterHint, button, label string) bool {
		for _, h := range hints {
			if h.Button == button && h.Label == label {
				return true
			}
		}
		return false
	}

	hints := favoritesFooterHints(base())
	if !has(hints, "X", "Remove") || !has(hints, "A", "Play") || has(hints, "Y", "Undo") {
		t.Fatalf("list footer = %v, want A Play and X Remove without Undo", hints)
	}
	favoritesUndo = favoritesUndoState{label: "Removed “A”", at: time.Now()}
	if hints = favoritesFooterHints(base()); !has(hints, "Y", "Undo") {
		t.Fatalf("footer during the Undo window = %v, want Y Undo", hints)
	}
	favoritesDeletePrompt.open = true
	if hints = favoritesFooterHints(base()); len(hints) != 3 || !has(hints, "B", "Keep file") {
		t.Fatalf("prompt footer = %v, want its three keys", hints)
	}
}
