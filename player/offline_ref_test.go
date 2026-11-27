package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflineReferenceScanAndRead(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"zeta.txt":   "  First line\n\nSecond line\n",
		"alpha.html": "<html><head><title>Ignored</title></head><body><h1>Hello</h1><p>A &amp; B</p></body></html>",
		"ignore.bin": "not a document",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := offlineRefDir
	offlineRefDir = func() string { return dir }
	defer func() { offlineRefDir = old; offlineRefUI.open, offlineRefUI.lines = false, nil }()

	items := offlineRefScan()
	if len(items) != 2 || items[0].Name != "alpha.html" || items[1].Name != "zeta.txt" {
		t.Fatalf("documents = %+v", items)
	}
	lines := strings.Join(offlineRefLines(), "\n")
	if !strings.Contains(lines, "alpha.html") || strings.Contains(lines, "ignore.bin") {
		t.Fatalf("list = %s", lines)
	}

	offlineRefUI.cursor = 1
	offlineRefOpen()
	text := strings.Join(offlineRefUI.lines, "\n")
	if !strings.Contains(text, "First line") || !strings.Contains(text, "Second line") {
		t.Fatalf("txt = %s", text)
	}
	offlineRefUI.cursor = 0
	offlineRefOpen()
	text = strings.Join(offlineRefUI.lines, "\n")
	for _, want := range []string{"Hello", "A & B"} {
		if !strings.Contains(text, want) {
			t.Fatalf("html has no %q: %s", want, text)
		}
	}
	if strings.Contains(text, "<h1>") || strings.Contains(text, "Ignored") {
		t.Fatalf("html markup or head text leaked: %s", text)
	}
	offlineRefClose()
	if offlineRefUI.open || len(offlineRefUI.lines) != 0 {
		t.Fatalf("close did not return to the list: %+v", offlineRefUI)
	}
}

func TestOfflineReferenceEmptyText(t *testing.T) {
	lines := offlineRefText("page.html", []byte("<html><body></body></html>"))
	if len(lines) != 1 || lines[0] != "This document has no readable text." {
		t.Fatalf("empty page = %v", lines)
	}
}
