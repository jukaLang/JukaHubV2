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
