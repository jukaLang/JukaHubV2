package main

// componentreport_test.go pins the report behind Patch → Component versions and
// the diagnostics export. The point of the card is that a bug report names exact
// builds, so the tests check that a known file's digest is the real SHA-256,
// that a missing tool says so instead of being dropped, and that the on-screen
// form stays short while the copied form carries the whole hash.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestComponentVersionsListsDigests(t *testing.T) {
	withTempPatchState(t)
	// Snapshot the value, not the pointer: dbRecordInstall mutates the pointed-to
	// database in place, and a pointer restore would leave this test's package
	// visible to the next test that loads the (empty) temp directory.
	prevDB := *patchDB
	t.Cleanup(func() { patchDB = &prevDB })

	tools := t.TempDir()
	body := []byte("#!/bin/sh\necho fake yt-dlp\n")
	name := "yt-dlp"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(tools, name), body, 0o755); err != nil {
		t.Fatalf("write fake tool: %v", err)
	}
	cfg := &Config{Version: "9.9.9"}
	cfg.Variables.Custom = map[string]interface{}{}
	cfg.Variables.ToolsPath = tools

	pkgDigest := strings.Repeat("ab", 32)
	if err := dbRecordInstall(DBInstalledPkg{
		Name:    "demo",
		Title:   "Demo Package",
		Version: "1.2.3",
		Files:   []DBInstalledFile{{Path: "/demo.txt", SHA256: pkgDigest}},
	}); err != nil {
		t.Fatalf("record package: %v", err)
	}

	rows := componentVersions(cfg)
	byName := map[string]componentReportRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}

	app, ok := byName["JukaHub"]
	if !ok {
		t.Fatal("report has no JukaHub row")
	}
	if app.Version != "v9.9.9" {
		t.Errorf("app version = %q, want the config version", app.Version)
	}
	if len(app.Digest) != 64 {
		t.Errorf("app digest = %q, want a full SHA-256", app.Digest)
	}

	tool, ok := byName["yt-dlp"]
	if !ok {
		t.Fatal("report has no yt-dlp row")
	}
	sum := sha256.Sum256(body)
	if tool.Digest != hex.EncodeToString(sum[:]) {
		t.Errorf("yt-dlp digest = %q, want the file's SHA-256", tool.Digest)
	}

	pkg, ok := byName["Demo Package"]
	if !ok {
		t.Fatal("report dropped the installed package")
	}
	if pkg.Version != "1.2.3" || pkg.Digest != pkgDigest {
		t.Errorf("package row = %+v, want version 1.2.3 and its file digest", pkg)
	}

	if _, ok := byName["Launcher"]; !ok {
		t.Error("report has no launcher row")
	}
	if _, ok := byName["Config"]; !ok {
		t.Error("report has no config row")
	}
}

func TestComponentVersionsNamesMissingTools(t *testing.T) {
	withTempPatchState(t)
	// Snapshot the value, not the pointer: dbRecordInstall mutates the pointed-to
	// database in place, and a pointer restore would leave this test's package
	// visible to the next test that loads the (empty) temp directory.
	prevDB := *patchDB
	t.Cleanup(func() { patchDB = &prevDB })

	cfg := &Config{Version: "1.0.0"}
	cfg.Variables.Custom = map[string]interface{}{}
	cfg.Variables.ToolsPath = t.TempDir()

	for _, r := range componentVersions(cfg) {
		if r.Name != "ffmpeg" {
			continue
		}
		if r.Digest != "" {
			t.Fatalf("missing ffmpeg reported a digest %q", r.Digest)
		}
		if r.Note != "not installed" {
			t.Fatalf("missing ffmpeg note = %q, want \"not installed\"", r.Note)
		}
		return
	}
	t.Fatal("report dropped the missing ffmpeg row")
}

func TestComponentVersionsTextShortOnScreenFullOnCopy(t *testing.T) {
	withTempPatchState(t)
	// Snapshot the value, not the pointer: dbRecordInstall mutates the pointed-to
	// database in place, and a pointer restore would leave this test's package
	// visible to the next test that loads the (empty) temp directory.
	prevDB := *patchDB
	t.Cleanup(func() { patchDB = &prevDB })

	cfg := &Config{Version: "1.0.0"}
	cfg.Variables.Custom = map[string]interface{}{}

	compact := componentVersionsText(cfg, false)
	full := componentVersionsText(cfg, true)

	if !strings.Contains(compact, "Press A to copy") {
		t.Errorf("compact report does not say how to get the full hashes:\n%s", compact)
	}
	const wanted = 12
	for _, line := range strings.Split(compact, "\n") {
		if !strings.HasPrefix(line, "JukaHub v") {
			continue
		}
		parts := strings.SplitN(line, "sha256:", 2)
		if len(parts) != 2 || len(parts[1]) != wanted {
			t.Errorf("compact app digest is not the short form: %q", line)
		}
	}
	for _, line := range strings.Split(full, "\n") {
		if !strings.HasPrefix(line, "JukaHub v") {
			continue
		}
		// "JukaHub v1.0.0  sha256:<64>" — the full form must keep the entire
		// digest so a maintainer can compare it to a release.
		parts := strings.SplitN(line, "sha256:", 2)
		if len(parts) != 2 {
			t.Fatalf("full app row has no digest: %q", line)
		}
		digest := strings.Fields(parts[1])
		if len(digest) == 0 || len(digest[0]) != 64 {
			t.Errorf("full report truncated the app digest: %q", line)
		}
	}
}
