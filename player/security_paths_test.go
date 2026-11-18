package main

// security_paths_test.go covers the path boundaries used by media, packs,
// sync bundles and the tools installer: an untrusted relative name must stay
// inside its root, and an archive entry that climbs out must be refused before
// anything is written.

import (
	"archive/zip"
	"context"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
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

func TestOpenRegularFileWithinKeepsTheOpenedFileInsideRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := openRegularFileWithin(root, "inside.txt")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(data) != "inside" {
		t.Fatalf("opened %q: %v", data, err)
	}
	for _, path := range []string{"../outside.txt", outside} {
		if f, err := openRegularFileWithin(root, path); err == nil {
			f.Close()
			t.Errorf("opened an outside path %q", path)
		}
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outside, link); err == nil {
		if f, err := openRegularFileWithin(root, link); err == nil {
			f.Close()
			t.Fatal("followed a symlink outside the root")
		}
	}
}

func TestLocalPlayableFileRequiresAnAbsoluteMediaFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(path, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secret, []byte("not media"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !localPlayableFile(path) {
		t.Fatal("regular local media file was not recognized")
	}
	for _, source := range []string{"clip.mp4", "https://example.test/clip.mp4", secret} {
		if localPlayableFile(source) {
			t.Errorf("non-local or non-media source %q was treated as a local file", source)
		}
	}
}

func TestAmbientPhotoFilesSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	photo := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(photo, []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.jpg")
	if err := os.WriteFile(outside, []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.jpg")); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}
	files := ambientPhotoFiles(dir)
	if len(files) != 1 || files[0] != photo {
		t.Fatalf("listed photos = %v, want only %q", files, photo)
	}
}

func TestConfigCanvasWidthRejectsOverflow(t *testing.T) {
	config := &Config{Width: -1}
	config.Variables.ScreenWidth = 1024
	if got := configCanvasWidth(config); got != 1024 {
		t.Fatalf("fallback width = %d, want 1024", got)
	}
	if strconv.IntSize == 64 {
		tooWide := int64(math.MaxInt32) + 1
		config.Width = int(tooWide)
		if got := configCanvasWidth(config); got != 1024 {
			t.Fatalf("overflowed width = %d, want 1024", got)
		}
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

// Juka's file tools reach only the storage roots (aitools.go), as written and
// after links are followed, and every tool result is scrubbed of credentials
// before it goes to the model.
func TestAIToolsPathsStayInRootsAndResultsLoseSecrets(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "card")
	outside := filepath.Join(dir, "outside")
	for _, d := range []string{filepath.Join(root, "Roms"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	roots := []string{root}
	if p, err := aiResolvePath("Roms/game.gba", roots); err != nil || p != filepath.Join(root, "Roms", "game.gba") {
		t.Fatalf("relative path inside the card: %q, %v", p, err)
	}
	for _, bad := range []string{"../outside/x", filepath.Join(outside, "x"), "Roms/../../outside"} {
		if p, err := aiResolvePath(bad, roots); err == nil {
			t.Fatalf("%q escaped the card as %q", bad, p)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err == nil {
		if p, err := aiResolvePath("link/secret.txt", roots); err == nil {
			t.Fatalf("a link out of the card was followed to %q", p)
		}
	}

	stored := "my-romm-token-123456"
	in := "groq gsk_abcdefghijklmnopqrstuvwx; token=abc123456789; psk=\"hunter22\"; romm " + stored
	out := aiRedactWith(in, []string{stored})
	for _, secret := range []string{"gsk_abcdefghijklmnopqrstuvwx", "abc123456789", "hunter22", stored} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q survived redaction: %q", secret, out)
		}
	}
}
