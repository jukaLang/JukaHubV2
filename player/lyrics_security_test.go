package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLyricsUsesOnlyARegularSidecar(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "song.mp3")
	lyrics := filepath.Join(dir, "song.lrc")
	if err := os.WriteFile(lyrics, []byte("[00:01.00]hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines := loadLyricsFor(audio)
	if len(lines) != 1 || lines[0].Text != "hello" {
		t.Fatalf("loaded lyrics = %#v", lines)
	}
	if err := os.Remove(lyrics); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.lrc")
	if err := os.WriteFile(outside, []byte("[00:01.00]private\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, lyrics); err == nil {
		if got := loadLyricsFor(audio); len(got) != 0 {
			t.Fatalf("followed a sidecar link outside its folder: %#v", got)
		}
	}
}
