package main

import (
	"path/filepath"
	"testing"
)

// musicPlaylistTestStore points the store at a temp file and clears the cache
// and the player queue around the test.
func musicPlaylistTestStore(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	oldPath := musicPlaylistFilePath
	musicPlaylistFilePath = func() string { return filepath.Join(dir, musicPlaylistFileName) }
	reset := func() {
		musicPlaylistState.mu.Lock()
		musicPlaylistState.items = nil
		musicPlaylistState.loaded = false
		musicPlaylistState.mu.Unlock()
		musicPlaylistSelectionIndex = 0
	}
	reset()
	t.Cleanup(func() {
		musicPlaylistFilePath = oldPath
		reset()
	})
}

func musicPlaylistTestQueue(t *testing.T, tracks ...musicTrack) {
	t.Helper()
	music.mu.Lock()
	old := append([]musicTrack(nil), music.tracks...)
	music.tracks = tracks
	music.mu.Unlock()
	t.Cleanup(func() {
		music.mu.Lock()
		music.tracks = old
		music.mu.Unlock()
	})
}

func TestMusicPlaylistSavesAndReloads(t *testing.T) {
	musicPlaylistTestStore(t)
	musicPlaylistTestQueue(t,
		musicTrack{Path: "/music/one.mp3", Name: "One"},
		musicTrack{Path: "/music/two.flac", Name: "Two", Duration: 120},
	)
	pl, err := musicSaveQueueAsPlaylist("Road Trip")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if pl.Name != "Road Trip" || len(pl.Tracks) != 2 {
		t.Fatalf("saved %+v", pl)
	}

	// The file has to be on disk: drop the cache and read it back.
	musicPlaylistState.mu.Lock()
	musicPlaylistState.items = nil
	musicPlaylistState.loaded = false
	musicPlaylistState.mu.Unlock()
	names := musicPlaylistNames()
	if len(names) != 1 || names[0] != "Road Trip" {
		t.Fatalf("reloaded names = %q", names)
	}
	back, ok := musicPlaylistNamed("road trip")
	if !ok || len(back.Tracks) != 2 {
		t.Fatalf("lookup by a different case = %+v, %v", back, ok)
	}
	if back.Tracks[1].Path != "/music/two.flac" || back.Tracks[1].Duration != 120 {
		t.Fatalf("tracks lost their timing: %+v", back.Tracks)
	}
}

func TestMusicPlaylistSaveReplacesTheSameName(t *testing.T) {
	musicPlaylistTestStore(t)
	musicPlaylistTestQueue(t, musicTrack{Path: "/music/one.mp3", Name: "One"})
	if _, err := musicSaveQueueAsPlaylist("Relaxing"); err != nil {
		t.Fatal(err)
	}

	music.mu.Lock()
	music.tracks = []musicTrack{{Path: "/music/two.mp3", Name: "Two"}}
	music.mu.Unlock()
	pl, err := musicSaveQueueAsPlaylist("relaxing")
	if err != nil {
		t.Fatal(err)
	}
	if pl.Name != "relaxing" || len(pl.Tracks) != 1 || pl.Tracks[0].Path != "/music/two.mp3" {
		t.Fatalf("replacement = %+v", pl)
	}
	if names := musicPlaylistNames(); len(names) != 1 {
		t.Fatalf("saving an existing name made a second list: %q", names)
	}
}

func TestMusicPlaylistRejectsEmptyInput(t *testing.T) {
	musicPlaylistTestStore(t)
	musicPlaylistTestQueue(t)
	if _, err := musicSaveQueueAsPlaylist("   "); err != errMusicPlaylistNoName {
		t.Fatalf("empty name error = %v", err)
	}
	if _, err := musicSaveQueueAsPlaylist("Road Trip"); err != errMusicPlaylistEmpty {
		t.Fatalf("empty queue error = %v", err)
	}
	if names := musicPlaylistNames(); len(names) != 0 {
		t.Fatalf("a rejected save created %q", names)
	}
}

func TestMusicPlaylistSelectionWrapsAndDeletes(t *testing.T) {
	musicPlaylistTestStore(t)
	musicPlaylistTestQueue(t, musicTrack{Path: "/music/a.mp3", Name: "A"})
	for _, name := range []string{"One", "Two"} {
		if _, err := musicSaveQueueAsPlaylist(name); err != nil {
			t.Fatal(err)
		}
	}

	// Saving leaves the cursor on the last saved list; start at the top.
	musicPlaylistSelectionIndex = 0
	if pl, ok := musicCyclePlaylistSelection(-1); !ok || pl.Name != "Two" {
		t.Fatalf("left from the first row = %+v, %v", pl, ok)
	}
	if pl, ok := musicCyclePlaylistSelection(1); !ok || pl.Name != "One" {
		t.Fatalf("right from the last row = %+v, %v", pl, ok)
	}
	if pl, ok := musicSelectedPlaylist(); !ok || pl.Name != "One" {
		t.Fatalf("selection = %+v, %v", pl, ok)
	}

	if !musicDeletePlaylist("one") {
		t.Fatal("delete by a different case should work")
	}
	if names := musicPlaylistNames(); len(names) != 1 || names[0] != "Two" {
		t.Fatalf("after delete: %q", names)
	}
	if musicDeletePlaylist("One") {
		t.Fatal("deleting a list twice must report false")
	}

	// An empty store must not leave a stale name on the row.
	musicPlaylistSelectionIndex = 9
	if _, ok := musicSelectedPlaylist(); !ok {
		// still one list left, so it must clamp, not fail
		t.Fatal("selection did not clamp onto the remaining list")
	}
}
