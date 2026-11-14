package main

// security_paths_test.go covers the path boundaries used by media, packs,
// sync bundles and the tools installer: an untrusted relative name must stay
// inside its root, and an archive entry that climbs out must be refused before
// anything is written.

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAuditTestZip(t *testing.T, path string, names ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("payload\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveWithinRootRejectsTraversalAndAbsoluteOutsiders(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "media"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "media", "track.mp3"), []byte("track"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "outside.mp3")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		ok   bool
	}{
		{"relative inside", "media/track.mp3", true},
		{"dotted relative inside", "media/../media/track.mp3", true},
		{"absolute inside", filepath.Join(root, "media", "track.mp3"), true},
		{"parent traversal", "../outside.mp3", false},
		{"nested parent traversal", "media/../../outside.mp3", false},
		{"absolute outside", outsideFile, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveWithinRoot(root, tc.path)
			if tc.ok && (err != nil || !isPathWithin(got, root)) {
				t.Fatalf("resolveWithinRoot(%q) = %q, %v; want inside %q", tc.path, got, err, root)
			}
			if !tc.ok && err == nil {
				t.Fatalf("resolveWithinRoot(%q) = %q; want a rejection", tc.path, got)
			}
		})
	}
}

func TestConfinedRegularFileKeepsLinksInsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, err := confinedRegularFile(root, "inside.txt"); err != nil || !isPathWithin(got, root) {
		t.Fatalf("confinedRegularFile(inside) = %q, %v", got, err)
	}
	if _, err := confinedRegularFile(root, "../outside.txt"); err == nil {
		t.Fatal("confinedRegularFile accepted parent traversal")
	}
	if _, err := confinedRegularFile(root, outsideFile); err == nil {
		t.Fatal("confinedRegularFile accepted an absolute outsider")
	}

	link := filepath.Join(root, "escape.txt")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}
	if _, err := confinedRegularFile(root, "escape.txt"); err == nil {
		t.Fatal("confinedRegularFile followed a link out of the root")
	}
}

func TestMusicParseM3U8ConfinesLocalEntries(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	inside := filepath.Join(root, "track.mp3")
	if err := os.WriteFile(inside, []byte("track"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "outside.mp3")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	data := strings.Join([]string{
		"#EXTM3U",
		"track.mp3",
		"../outside.mp3",
		outsideFile,
		"https://example.invalid/stream.mp3",
		"",
	}, "\n")

	entries, missing := musicParseM3U8([]byte(data), root)
	if len(entries) != 2 {
		t.Fatalf("musicParseM3U8 returned %d entries, want the in-root file and the URL", len(entries))
	}
	if missing != 2 {
		t.Fatalf("musicParseM3U8 missing = %d, want 2 refused local entries", missing)
	}
	if !isPathWithin(entries[0].Path, root) {
		t.Fatalf("first entry = %q, want the in-root file %q", entries[0].Path, inside)
	}
	if entries[1].Path != "https://example.invalid/stream.mp3" {
		t.Fatalf("URL entry = %q", entries[1].Path)
	}
}

func TestRemoveWithinRootRejectsEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	inside := filepath.Join(root, "gone.txt")
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(outsideFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeWithinRoot(root, inside); err != nil {
		t.Fatalf("removeWithinRoot(inside): %v", err)
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatalf("inside file still exists: %v", err)
	}
	if err := removeWithinRoot(root, outsideFile); err == nil {
		t.Fatal("removeWithinRoot accepted an absolute outsider")
	}
	if _, err := os.Stat(outsideFile); err != nil {
		t.Fatalf("outside file was removed: %v", err)
	}
}

func TestSSyncValidBaseKeyRejectsTraversal(t *testing.T) {
	cases := []struct {
		key string
		ok  bool
	}{
		{"gba/save.srm", true},
		{"gba/Season 1/save.srm", true},
		{"gba/../escape.srm", false},
		{"gba/../../escape.srm", false},
		{"../gba/save.srm", false},
		{"gba/C:/escape.srm", false},
		{"gba/escape.srm/..", false},
		{"/gba/save.srm", false},
	}
	for _, tc := range cases {
		if got := ssyncValidBaseKey(tc.key); got != tc.ok {
			t.Errorf("ssyncValidBaseKey(%q) = %v; want %v", tc.key, got, tc.ok)
		}
	}
}

func TestAuditExtractTreeConfinesArchiveEntries(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "stage")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}

	good := filepath.Join(root, "good.zip")
	writeAuditTestZip(t, good, "tool/run.py")
	if err := auditExtractTree(context.Background(), good, dest); err != nil {
		t.Fatalf("auditExtractTree(normal archive): %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "run.py")); err != nil || string(got) != "payload\n" {
		t.Fatalf("normal archive output = %q, %v", got, err)
	}

	bad := filepath.Join(root, "bad.zip")
	writeAuditTestZip(t, bad, "../escape.txt")
	escape := filepath.Join(root, "escape.txt")
	if err := auditExtractTree(context.Background(), bad, dest); err == nil {
		t.Fatal("auditExtractTree accepted an archive entry with ..")
	}
	if _, err := os.Stat(escape); !os.IsNotExist(err) {
		t.Fatalf("archive traversal wrote %q: %v", escape, err)
	}
}
