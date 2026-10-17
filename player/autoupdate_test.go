package main

// autoupdate_test.go covers the release-stamp parsing and the staged-file
// planning used by the automatic updater. Everything runs on temporary
// directories; no network and no real installation is touched.

import (
	"os"
	"os/exec"
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

func TestInstalledBuildDateAndNewerRelease(t *testing.T) {
	old := buildDate
	buildDate = "2026-10-17"
	defer func() { buildDate = old }()

	if got := installedBuildDate(); got != "2026-10-17" {
		t.Fatalf("installedBuildDate = %q, want the build stamp", got)
	}
	if releaseIsNewerThanInstalled(&githubRelease{TagName: "JukaHub-20261017090000"}) {
		t.Error("a same-day release before the build must not be newer")
	}
	if !releaseIsNewerThanInstalled(&githubRelease{TagName: "JukaHub-20261018120000"}) {
		t.Error("a later release must be newer")
	}
	if releaseIsNewerThanInstalled(&githubRelease{TagName: "not-a-release"}) {
		t.Error("a release without a timestamp must never trigger an update")
	}
}

// A compile stamp with a time is compared at full precision, so a release
// published later on the same day is still offered.
func TestBuildTimeStampedUpdateDecision(t *testing.T) {
	oldTime, oldDate := buildTime, buildDate
	buildTime, buildDate = "2026-10-17T09:00:00Z", "2026-10-17"
	defer func() { buildTime, buildDate = oldTime, oldDate }()

	after := &githubRelease{TagName: "JukaHub-20261017120000", PublishedAt: "2026-10-17T12:00:00Z"}
	if !releaseIsNewerThanInstalled(after) {
		t.Fatal("a release published after the compile time must be newer")
	}
	if got := installedBuildTimeLabel(); got != "2026-10-17 09:00 UTC" {
		t.Fatalf("installedBuildTimeLabel = %q, want the compile time", got)
	}

	buildTime = "2026-10-17T23:00:00Z"
	if releaseIsNewerThanInstalled(after) {
		t.Fatal("a release published before the compile time must not be newer")
	}
}

// The automatic check only advertises a release: it must not download, stage
// or replace anything before the user confirms in the Patch scene.
func TestAvailableUpdateIsAdvertisedWithoutStaging(t *testing.T) {
	patchStateMu.Lock()
	prev := *patchState
	patchStateMu.Unlock()
	t.Cleanup(func() {
		patchStateMu.Lock()
		*patchState = prev
		patchStateMu.Unlock()
	})

	rel := &githubRelease{TagName: "JukaHub-20261018120000", PublishedAt: "2026-10-18T12:00:00Z"}
	markUpdateAvailable(rel)

	if got := availableUpdateTag(); got != rel.TagName {
		t.Fatalf("availableUpdateTag = %q, want %q", got, rel.TagName)
	}
	if got := availableUpdateLabel(); got != "2026-10-18" {
		t.Fatalf("availableUpdateLabel = %q", got)
	}
	patchStateMu.Lock()
	staged := patchState.LastUpdateVer
	patchStateMu.Unlock()
	if staged != "" {
		t.Fatalf("an available release was staged without confirmation (%q)", staged)
	}

	found := false
	for _, a := range patchActions() {
		if a.ID == "install" {
			found = true
		}
	}
	if !found {
		t.Fatal("Patch does not offer an install action for the advertised release")
	}

	clearAvailableUpdate()
	if availableUpdateTag() != "" {
		t.Fatal("clearAvailableUpdate left the tag behind")
	}
	for _, a := range patchActions() {
		if a.ID == "install" {
			t.Fatal("install action still offered after the release was cleared")
		}
	}
}

// The build scripts must stamp the compile time, otherwise a local or release
// build falls back to a date-only stamp and hides same-day releases.
func TestBuildScriptsStampCompileTime(t *testing.T) {
	for _, name := range []string{"build.bat", "release.sh"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := string(data)
		if !strings.Contains(body, "main.buildTime") {
			t.Errorf("%s does not stamp main.buildTime", name)
		}
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

// TestSwapScriptReplacesFiles executes the generated Windows helper against a
// throwaway installation. The relaunch line is removed so the test never
// starts a real JukaHub process.
func TestSwapScriptReplacesFiles(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the generated helper is a .bat on Windows")
	}
	dir := t.TempDir()
	staged := filepath.Join(dir, "staged")
	install := filepath.Join(dir, "install")
	for _, d := range []string{staged, install} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(staged, "JukaHub.exe", "new-binary")
	write(staged, "SDL2.dll", "new-dll")
	write(staged, releaseInfoFileName, `{"tag":"JukaHub-20261017120000"}`)
	write(install, "JukaHub.exe", "old-binary")
	write(install, "SDL2.dll", "old-dll")

	journal := filepath.Join(dir, "journal.json")
	write(dir, "journal.json", "{}")

	files, err := stagedInstallFiles(staged, install)
	if err != nil {
		t.Fatalf("stagedInstallFiles: %v", err)
	}
	script := filepath.Join(dir, "apply-update.bat")
	if err := writeSwapScript(script, filepath.Join(install, "JukaHub.exe"), files, journal); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	// Drop the relaunch line only.
	var kept []string
	for _, line := range strings.Split(string(data), "\r\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "start ") {
			continue
		}
		kept = append(kept, line)
	}
	if err := os.WriteFile(script, []byte(strings.Join(kept, "\r\n")), 0o700); err != nil {
		t.Fatal(err)
	}

	if out, err := exec.Command("cmd", "/c", script).CombinedOutput(); err != nil {
		t.Fatalf("swap helper failed: %v\n%s", err, out)
	}
	for name, want := range map[string]string{
		"JukaHub.exe":       "new-binary",
		"SDL2.dll":          "new-dll",
		releaseInfoFileName: `{"tag":"JukaHub-20261017120000"}`,
	} {
		data, err := os.ReadFile(filepath.Join(install, name))
		if err != nil || string(data) != want {
			t.Errorf("%s = %q, %v; want %q", name, data, err, want)
		}
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Error("journal must be removed after a successful swap")
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
