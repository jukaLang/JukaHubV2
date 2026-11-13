package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestBGMSelectedTracks(t *testing.T) {
	root := t.TempDir()
	album := filepath.Join(root, "Album")
	if err := os.Mkdir(album, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"song.mp3", filepath.Join("Album", "another.flac"), "notes.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	tracks, why := bgmSelectedTracks(root)
	if why != "" || !slices.Equal(tracks, []string{filepath.Join(album, "another.flac"), filepath.Join(root, "song.mp3")}) {
		t.Fatalf("folder tracks = %q, %q", tracks, why)
	}
	file := filepath.Join(root, "song.mp3")
	tracks, why = bgmSelectedTracks(file)
	if why != "" || !slices.Equal(tracks, []string{file}) {
		t.Fatalf("file tracks = %q, %q", tracks, why)
	}
	if tracks, why = bgmSelectedTracks(filepath.Join(root, "notes.txt")); len(tracks) != 0 || why == "" {
		t.Fatalf("non-audio file = %q, %q", tracks, why)
	}
	if tracks, why = bgmSelectedTracks(filepath.Join(root, "missing")); len(tracks) != 0 || why == "" {
		t.Fatalf("missing path = %q, %q", tracks, why)
	}
}

func TestBGMSelectionChangeClearsOldQueue(t *testing.T) {
	previousChoice, _ := bgmChoiceState.Load().(bgmChoice)
	previousGeneration := bgmGen.Load()
	previousOn, previousLevel := bgmOn.Load(), bgmLevel.Load()
	bgm.mu.Lock()
	previousTracks, previousIndex, previousPos, previousFails := bgm.lib, bgm.libIdx, bgm.libPos, bgm.libFails
	bgm.mu.Unlock()
	t.Cleanup(func() {
		bgmChoiceState.Store(previousChoice)
		bgmGen.Store(previousGeneration)
		bgmOn.Store(previousOn)
		bgmLevel.Store(previousLevel)
		bgm.mu.Lock()
		bgm.lib, bgm.libIdx, bgm.libPos, bgm.libFails = previousTracks, previousIndex, previousPos, previousFails
		bgm.mu.Unlock()
	})
	c := &Config{}
	c.Variables.Custom = map[string]interface{}{
		bgmSourceKey: bgmSrcSelected, bgmSelectedKey: filepath.Join(t.TempDir(), "first.mp3"),
	}
	bgmBind(c)
	bgm.mu.Lock()
	bgm.lib, bgm.libIdx, bgm.libPos, bgm.libFails = []string{"old.mp3"}, 0, 12, 2
	bgm.mu.Unlock()
	before := bgmGen.Load()
	c.Variables.Custom[bgmSelectedKey] = filepath.Join(t.TempDir(), "second.mp3")
	bgmBind(c)
	bgm.mu.Lock()
	defer bgm.mu.Unlock()
	if bgmGen.Load() == before || len(bgm.lib) != 0 || bgm.libIdx != 0 || bgm.libPos != 0 || bgm.libFails != 0 {
		t.Fatal("changing the selected path did not reset the previous playlist")
	}
}
