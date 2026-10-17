package main

// autoupdate_test.go covers the release-stamp parsing and the staged-file
// planning used by the automatic updater. Everything runs on temporary
// directories; no network and no real installation is touched.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReleaseTimeFromTag(t *testing.T) {
	got, ok := releaseTimeFromTag("JukaHub-20261017120000")
	if !ok {
		t.Fatal("expected the workflow tag to parse")
	}
	want := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("releaseTimeFromTag = %s, want %s", got, want)
	}
	for _, bad := range []string{"", "v0.4.0", "JukaHub-latest", "JukaHub-2026"} {
		if _, ok := releaseTimeFromTag(bad); ok {
			t.Errorf("tag %q must not parse as a release time", bad)
		}
	}
}

func TestReleasePublishedTimeAndLabel(t *testing.T) {
	rel := &githubRelease{TagName: "JukaHub-20260101000000", PublishedAt: "2026-10-17T09:30:00Z"}
	if label := releaseDateLabel(rel); label != "2026-10-17" {
		t.Fatalf("releaseDateLabel = %q", label)
	}
	if rel.PublishedAt != "" && releaseStampISO(rel) != "2026-10-17T09:30:00Z" {
		t.Fatalf("releaseStampISO = %q", releaseStampISO(rel))
	}
	// published_at wins over the tag timestamp.
	if ts, ok := releasePublishedTime(rel); !ok || ts.Hour() != 9 {
		t.Fatalf("releasePublishedTime = %s, %v", ts, ok)
	}
	// Without published_at the tag supplies the date.
	rel2 := &githubRelease{TagName: "JukaHub-20261017120000"}
	if label := releaseDateLabel(rel2); label != "2026-10-17" {
		t.Fatalf("tag-only releaseDateLabel = %q", label)
	}
	// Unknown metadata is never a valid release time.
	if _, ok := releasePublishedTime(&githubRelease{TagName: "nonsense"}); ok {
		t.Error("garbage tag must not yield a release time")
	}
}

func TestFormatBuildLabel(t *testing.T) {
	if got := formatBuildLabel("v0.4.0", "2026-10-17"); got != "v0.4.0 · 2026-10-17" {
		t.Fatalf("formatBuildLabel = %q", got)
	}
	if got := formatBuildLabel("v0.4.0", ""); got != "v0.4.0" {
		t.Fatalf("formatBuildLabel without date = %q", got)
	}
}

func TestStagedInstallFilesOrdersReleaseStampLast(t *testing.T) {
	root := t.TempDir()
	exe := "JukaHub"
	if runtime.GOOS == "windows" {
		exe = "JukaHub.exe"
	}
	for _, name := range []string{exe, "Inter-Regular.ttf", releaseInfoFileName} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dest := t.TempDir()
	files, err := stagedInstallFiles(root, dest)
	if err != nil {
		t.Fatalf("stagedInstallFiles: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("got %d files, want 3", len(files))
	}
	if files[len(files)-1].Name != releaseInfoFileName {
		t.Fatalf("release stamp must be copied last, got order %v", files)
	}
	for _, f := range files {
		if filepath.Dir(f.Dest) != dest {
			t.Errorf("destination %q is outside the executable directory", f.Dest)
		}
	}
}

func TestStagedInstallFilesRejectsEmptyTree(t *testing.T) {
	if _, err := stagedInstallFiles(t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("an empty staged tree must be rejected")
	}
}

func TestWriteSwapScriptIncludesEveryFile(t *testing.T) {
	dir := t.TempDir()
	files := []stagedInstallFile{
		{Src: filepath.Join(dir, "a.txt"), Name: "a.txt", Dest: filepath.Join(dir, "out", "a.txt")},
		{Src: filepath.Join(dir, "JukaHub"), Name: "JukaHub", Dest: filepath.Join(dir, "out", "JukaHub")},
	}
	script := filepath.Join(dir, "apply-update")
	if err := writeSwapScript(script, filepath.Join(dir, "out", "JukaHub"), files, filepath.Join(dir, "journal.json")); err != nil {
		t.Fatalf("writeSwapScript: %v", err)
	}
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{"a.txt", "JukaHub", "journal.json"} {
		if !strings.Contains(body, want) {
			t.Errorf("swap script does not mention %q", want)
		}
	}
	if runtime.GOOS != "windows" && !strings.HasPrefix(body, "#!/bin/sh") {
		t.Error("POSIX helper must be a shell script")
	}
}
