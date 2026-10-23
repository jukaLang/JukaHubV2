package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsNetworkSource(t *testing.T) {
	cases := map[string]bool{
		"https://example.com/watch?v=1": true,
		"HTTP://example.com/a.mp4":      true,
		"rtsp://cam/stream":             true,
		"/home/user/movie.mkv":          false,
		"":                              false,
	}
	for src, want := range cases {
		if got := isNetworkSource(src); got != want {
			t.Errorf("isNetworkSource(%q) = %v, want %v", src, got, want)
		}
	}
}

func TestPickSubtitleFilePrefersVTT(t *testing.T) {
	dir := t.TempDir()
	if got := pickSubtitleFile(dir); got != "" {
		t.Fatalf("empty dir: got %q, want empty", got)
	}
	srt := filepath.Join(dir, "subs.en.srt")
	if err := os.WriteFile(srt, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pickSubtitleFile(dir); got != srt {
		t.Fatalf("srt only: got %q, want %q", got, srt)
	}
	vtt := filepath.Join(dir, "subs.en.vtt")
	if err := os.WriteFile(vtt, []byte("WEBVTT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pickSubtitleFile(dir); got != vtt {
		t.Fatalf("vtt and srt: got %q, want %q", got, vtt)
	}
}
