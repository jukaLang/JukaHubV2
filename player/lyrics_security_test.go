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

// Lyrics tags inside a file are outside data: a timed tag is preferred and
// parsed as LRC, plain text is spread over the track, metadata lines dropped.
func TestEmbeddedLyricsFromProbe(t *testing.T) {
	probe := `{"streams":[{"tags":{"LYRICS":"[00:02.00]sung\n[00:04.00]later"}}],
		"format":{"duration":"100.0","tags":{"lyrics-eng":"plain one\nplain two","title":"x"}}}`
	text, dur := lyricsFromProbe([]byte(probe))
	if dur != 100 {
		t.Fatalf("duration = %v", dur)
	}
	if lines := lyricsFromText(text, dur); len(lines) != 2 || lines[0].At != 2 || lines[0].Text != "sung" {
		t.Fatalf("timed lyrics = %#v", lines)
	}
	plain := lyricsFromText("[ar:Someone]\nfirst\n\n second  \n", 100)
	if len(plain) != 2 || plain[0].Text != "first" || plain[1].Text != "second" || plain[0].At < 3.9 || plain[0].At > 4.1 || plain[1].At <= plain[0].At {
		t.Fatalf("plain lyrics = %#v", plain)
	}
	if text, _ := lyricsFromProbe([]byte(`{"format":{"tags":{"comment":"no"}}}`)); text != "" {
		t.Fatalf("lyrics from a comment tag: %q", text)
	}
}
