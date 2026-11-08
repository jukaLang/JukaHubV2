package main

// media_hub_favs_test.go — removing a favourite on the Media screen is visible
// and reversible: X, Delete or the bin take it off at once, Undo (Y, Ctrl+Z,
// the bar) puts it back in its place, and a downloaded video asks whether its
// file goes too instead of deleting it behind the user's back.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// mediaFavTestCard is the Favorites tab's card for a saved video.
func mediaFavTestCard(title, url string) mediaCard {
	return mediaCard{title: title, favorite: true, video: VideoInfo{Title: title, WebpageURL: url}}
}

func TestMediaFavoritesRemoveCanBeUndoneInPlace(t *testing.T) {
	favoritesTestState(t,
		VideoInfo{Title: "A", WebpageURL: "https://example.com/a"},
		VideoInfo{Title: "B", WebpageURL: "https://example.com/b"},
		VideoInfo{Title: "C", WebpageURL: "https://example.com/c"},
	)
	dlLibraryTestState(t)
	mediaHubTestState(t)
	mediaHub.tab = mediaTabFavorites

	if !mediaHubRemoveFavorite(mediaFavTestCard("B", "https://example.com/b")) {
		t.Fatal("B was not removed")
	}
	if got := favoritesTitles(); len(got) != 2 || got[0] != "A" || got[1] != "C" {
		t.Fatalf("after removing B the favourites are %q", got)
	}
	if !mediaFavUndoActive() {
		t.Fatal("the removal was not offered back")
	}
	if mediaHub.confirm.open {
		t.Fatal("a video with no download asked about deleting a file")
	}
	if !mediaHubUndoFavorite() {
		t.Fatal("Undo did nothing")
	}
	if got := favoritesTitles(); len(got) != 3 || got[1] != "B" {
		t.Fatalf("after Undo the favourites are %q, want B back in the middle", got)
	}
	if mediaFavUndoActive() || mediaHubUndoFavorite() {
		t.Fatal("one removal was undone twice")
	}

	if mediaHubRemoveFavorite(mediaFavTestCard("Nope", "https://example.com/nope")) {
		t.Fatal("a video that is not a favourite was removed")
	}
	mediaHubRemoveFavorite(mediaFavTestCard("A", "https://example.com/a"))
	mediaFavUndo.at = time.Now().Add(-mediaFavUndoWindow - time.Second)
	if mediaFavUndoActive() || mediaHubUndoFavorite() {
		t.Fatal("a removal was undone after the Undo bar had gone")
	}
}

// A favourite that is also downloaded asks whether the file goes too: Keep
// (the highlighted answer) leaves it in Downloads, Delete frees the space.
func TestMediaFavoritesRemoveAsksAboutTheDownload(t *testing.T) {
	page := "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	favoritesTestState(t, VideoInfo{Title: "Song", WebpageURL: page})
	root := dlLibraryTestState(t)
	mediaHubTestState(t)
	file := filepath.Join(root, "video", "Song.mp4")
	dlWriteTestFile(t, file, 3)
	dlLibrary.mu.Lock()
	dlLibrary.entries = []dlLibraryEntry{{ID: "s", Path: file, Video: true, Source: page, Size: 3, Title: "Song"}}
	dlLibrary.mu.Unlock()
	mediaHub.tab = mediaTabFavorites
	mediaHub.zone = mediaZoneContent
	card := mediaFavTestCard("Song", page)

	mediaHubRemoveFavorite(card)
	c := mediaHub.confirm
	if !c.open || c.kind != mediaConfirmFavDownload || c.libID != "s" || c.focus != 0 || c.cancel != "Keep file" {
		t.Fatalf("question = %+v, want one about entry s with Keep file highlighted", c)
	}
	mediaHubConfirmInput(mediaInConfirm)
	if mediaHub.confirm.open {
		t.Fatal("Keep did not close the question")
	}
	if _, ok := dlLibraryFindID("s"); !ok {
		t.Fatal("Keep file removed the download")
	}
	if !mediaFavUndoActive() {
		t.Fatal("answering the question used up the Undo")
	}

	if !mediaHubUndoFavorite() {
		t.Fatal("the favourite could not be restored")
	}
	mediaHubRemoveFavorite(card)
	mediaHubConfirmInput(mediaInRight)
	mediaHubConfirmInput(mediaInConfirm)
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

// On the Favorites tab X and Delete remove the focused card; while the
// removal can be taken back Y is Undo, and so is Ctrl+Z.
func TestMediaFavoritesKeysRemoveAndUndo(t *testing.T) {
	favoritesTestState(t,
		VideoInfo{Title: "A", WebpageURL: "https://example.com/a"},
		VideoInfo{Title: "B", WebpageURL: "https://example.com/b"},
	)
	dlLibraryTestState(t)
	mediaHubTestState(t)
	config := mediaTestTubeConfig(t)
	mediaTestPlayerPhase(t, "idle")
	prevPhase := transitionPhase
	transitionPhase = "none"
	t.Cleanup(func() { transitionPhase = prevPhase })
	mediaHub.tab = mediaTabFavorites
	mediaHub.zone = mediaZoneContent
	mediaHub.cards = []mediaCard{mediaFavTestCard("A", "https://example.com/a")}
	mediaHub.index = 0

	pad := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_X}
	if !mediaHubHandleController(pad, config) {
		t.Fatal("X was not handled on Favorites")
	}
	if got := favoritesTitles(); len(got) != 1 || got[0] != "B" {
		t.Fatalf("X left %q, want only B", got)
	}
	pad.Button = sdl.CONTROLLER_BUTTON_Y
	if !mediaHubHandleController(pad, config) || len(favoritesTitles()) != 2 {
		t.Fatal("Y did not take the removal back")
	}
	if mediaHub.sheet.open {
		t.Fatal("Y opened the details sheet instead of undoing")
	}

	del := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_DELETE}}
	if !mediaHubHandleKey(del, config) || len(favoritesTitles()) != 1 {
		t.Fatal("Delete did not remove the focused favourite")
	}
	undo := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_z, Mod: sdl.KMOD_CTRL}}
	if !mediaHubHandleKey(undo, config) || len(favoritesTitles()) != 2 {
		t.Fatal("Ctrl+Z did not take the removal back")
	}

	// Elsewhere X is still the search key.
	mediaHub.tab = mediaTabHistory
	if mediaHubRemoveKey() {
		t.Fatal("X removes on History")
	}
}
