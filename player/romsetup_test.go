package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanROMLibraryCountsSupportedKnownSystems(t *testing.T) {
	root := t.TempDir()
	for path, contents := range map[string]string{
		"FC/Mario.nes":       "nes rom",
		"GBA/Zelda.gba":      "gba rom",
		"PS/game.iso":        "ps rom",
		"Unknown/ignore.bin": "ignore",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	report := scanROMLibrary([]string{root})
	if len(report.Games) != 3 || len(report.Counts) != 3 {
		t.Fatalf("scan found %d game(s) across %d system(s), want 3 across 3", len(report.Games), len(report.Counts))
	}
	if report.Counts["NES"] != 1 || report.Counts["Game Boy Advance"] != 1 || report.Counts["PlayStation"] != 1 {
		t.Fatalf("unexpected system counts: %#v", report.Counts)
	}
}

// A CD game is one row: its .cue (or the .m3u of a multi-disc set), not the
// sheet plus one row per track and disc.
func TestScanROMLibraryListsMultiFileGamesOnce(t *testing.T) {
	root := t.TempDir()
	for path, contents := range map[string]string{
		"PS/Ridge Racer.cue": "FILE \"Ridge Racer (Track 1).bin\" BINARY\r\n  TRACK 01 MODE2/2352\r\n" +
			"FILE \"ridge racer (track 2).bin\" BINARY\r\n  TRACK 02 AUDIO\r\n",
		"PS/Ridge Racer (Track 1).bin": "data",
		"PS/Ridge Racer (Track 2).bin": "audio",
		"PS/FF7.m3u":                   "#EXTM3U\nFF7 (Disc 1).cue\nFF7 (Disc 2).cue\n",
		"PS/FF7 (Disc 1).cue":          "FILE \"FF7 (Disc 1).bin\" BINARY\n",
		"PS/FF7 (Disc 1).bin":          "d1",
		"PS/FF7 (Disc 2).cue":          "FILE \"FF7 (Disc 2).bin\" BINARY\n",
		"PS/FF7 (Disc 2).bin":          "d2",
		"PS/Loose.bin":                 "a bin no sheet names",
		"DC/Crazy Taxi.gdi":            "3\n1 0 4 2352 track01.bin 0\n2 756 0 2352 \"track 02.raw\" 0\n",
		"DC/track01.bin":               "t1",
		"DC/track 02.raw":              "t2",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	report := scanROMLibrary([]string{root})
	var names []string
	for _, g := range report.Games {
		names = append(names, filepath.Base(g.Path))
	}
	got := strings.Join(names, "|")
	want := "Crazy Taxi.gdi|FF7.m3u|Loose.bin|Ridge Racer.cue"
	if got != want {
		t.Fatalf("scan listed %q, want %q", got, want)
	}
	if report.Counts["PlayStation"] != 3 || report.Counts["Dreamcast"] != 1 {
		t.Fatalf("counts = %#v, want 3 PlayStation and 1 Dreamcast", report.Counts)
	}
}

func TestFetchROMArtworkImageRequestsEncodedTitleAndValidatesPNG(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nimage")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if !strings.Contains(r.URL.Path, "Named_Boxarts/Mario%20%28USA%29.png") && !strings.Contains(r.URL.EscapedPath(), "Named_Boxarts/Mario%20%28USA%29.png") {
			t.Errorf("artwork path = %q (escaped %q)", r.URL.Path, r.URL.EscapedPath())
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer server.Close()

	got, err := fetchROMArtworkImage(context.Background(), server.Client(), server.URL, "Nintendo - NES", "Mario (USA)")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(png) {
		t.Fatalf("downloaded bytes = %q, want %q", got, png)
	}
}

func TestFetchROMArtworkImageRejectsNonPNG(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not an image"))
	}))
	defer server.Close()
	if _, err := fetchROMArtworkImage(context.Background(), server.Client(), server.URL, "NES", "Title"); err == nil {
		t.Fatal("non-PNG artwork was accepted")
	}
}
