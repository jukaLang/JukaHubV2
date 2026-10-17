package main

import (
	"os"
	"path/filepath"
	"testing"
)

// existingDirWithin is the guard behind File Explorer navigation: it must
// accept directories below the root and reject anything outside it, including
// paths that only escape after cleaning.
func TestExistingDirWithinRejectsEscape(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "media", "shows")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := existingDirWithin(root, sub)
	if err != nil {
		t.Fatalf("directory inside the root was rejected: %v", err)
	}
	if got != filepath.Clean(sub) {
		t.Fatalf("resolved = %q, want %q", got, filepath.Clean(sub))
	}

	outside := t.TempDir()
	for _, candidate := range []string{
		outside,
		filepath.Join(root, "..", filepath.Base(outside)),
		filepath.Join(sub, "..", "..", "..", filepath.Base(outside)),
	} {
		if _, err := existingDirWithin(root, candidate); err == nil {
			t.Errorf("directory %q outside the root was accepted", candidate)
		}
	}

	// A regular file is not a directory, even inside the root.
	file := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := existingDirWithin(root, file); err == nil {
		t.Error("regular file accepted as a browsable directory")
	}
}

// fe_path lives in the config file, so a tampered value must not make the
// File Explorer list an arbitrary directory.
func TestFECurrentPathFallsBackToRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "media")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()

	cfg := &Config{}
	cfg.Variables.FileExplorerRoot = root
	cfg.Variables.Custom = map[string]interface{}{}

	cfg.Variables.Custom["fe_path"] = sub
	if got := feCurrentPath(cfg); got != filepath.Clean(sub) {
		t.Fatalf("fe_path inside root: got %q, want %q", got, filepath.Clean(sub))
	}

	cfg.Variables.Custom["fe_path"] = outside
	if got := feCurrentPath(cfg); got != root {
		t.Fatalf("fe_path outside root: got %q, want the root %q", got, root)
	}

	cfg.Variables.Custom["fe_path"] = filepath.Join(root, "..", filepath.Base(outside))
	if got := feCurrentPath(cfg); got != root {
		t.Fatalf("fe_path traversal: got %q, want the root %q", got, root)
	}

	cfg.Variables.Custom["fe_path"] = filepath.Join(root, "missing")
	if got := feCurrentPath(cfg); got != root {
		t.Fatalf("missing fe_path: got %q, want the root %q", got, root)
	}
}

// An app's config.json names its launch script; that name must stay inside the
// app folder instead of pointing at a script elsewhere on the card.
func TestScanAppSubdirRejectsEscapingLaunch(t *testing.T) {
	base := t.TempDir()
	appDir := filepath.Join(base, "Evil")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	evil := filepath.Join(base, "evil.sh")
	if err := os.WriteFile(evil, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"label": "Evil", "launch": "../evil.sh"}`
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := scanAppSubdir(appDir); ok {
		t.Fatal("app whose launch script escapes its folder was accepted")
	}

	// A containment-safe relative path still works.
	if err := os.WriteFile(filepath.Join(appDir, "launch.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg = `{"label": "Good", "launch": "launch.sh"}`
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	entry, ok := scanAppSubdir(appDir)
	if !ok || entry.Path != filepath.Join(appDir, "launch.sh") {
		t.Fatalf("valid app rejected or wrong path: ok=%v entry=%+v", ok, entry)
	}
}

// The launcher must not start an arbitrary executable just because a row in the
// Apps list says so.
func TestIsLaunchableAppPathRejectsArbitraryPaths(t *testing.T) {
	if isLaunchableAppPath(filepath.Join(t.TempDir(), "not-an-app.exe")) {
		t.Fatal("launcher accepted a path outside the app folders")
	}
}
