package main

// update_stage_test.go covers the "it actually unzips and installs" half of the
// one-press updater: a release-shaped ZIP is extracted into the staging folder,
// the executable is found, the release stamp is written, and the staged files
// planned by the swap helper are the ones the archive shipped.

import (
	"archive/zip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeReleaseZip builds the same shape the release workflow publishes: the
// JukaHub executable, its runtime DLLs, a font, a background and the stamp file.
func writeReleaseZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func releaseExecutableName() string {
	if runtime.GOOS == "windows" {
		return "JukaHub.exe"
	}
	return "JukaHub"
}

func TestStageDownloadedReleaseUnzipsAndStamps(t *testing.T) {
	exe := releaseExecutableName()
	archive := filepath.Join(t.TempDir(), "JukaHub-Test.zip")
	writeReleaseZip(t, archive, map[string]string{
		// The release ships a stale stamp; staging must rewrite it.
		exe:                 "new-binary",
		"SDL2.dll":          "new-dll",
		"Inter-Regular.ttf": "font",
		"required/yt-dlp":   "archive-tool",
		releaseInfoFileName: `{"tag":"JukaHub-20200101000000"}`,
	})

	staging := filepath.Join(t.TempDir(), "staging", "app-test")
	rel := &githubRelease{TagName: "JukaHub-20261019120000", PublishedAt: "2026-10-19T12:00:00Z"}
	if err := stageDownloadedRelease(context.Background(), rel, archive, staging); err != nil {
		t.Fatalf("stageDownloadedRelease: %v", err)
	}

	bin := findStagedBinary(staging)
	if bin == "" {
		t.Fatal("staged tree has no JukaHub executable")
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(bin); err != nil || info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("staged executable is not executable: %v, %v", info, err)
		}
	}

	stagedInfo := readReleaseInfoFromDir(filepath.Dir(bin))
	if stagedInfo.Tag != rel.TagName {
		t.Fatalf("staged release stamp = %q, want %q", stagedInfo.Tag, rel.TagName)
	}
	if _, err := os.Stat(releaseInfoPath(filepath.Dir(bin)) + ".bak"); !os.IsNotExist(err) {
		t.Error("the stamp's .bak copy must not ship inside the staging tree")
	}

	// The swap helper plans exactly the archive's files (tools under required/
	// are preserved), and the stamp is copied last.
	install := t.TempDir()
	files, err := stagedInstallFiles(filepath.Dir(bin), install)
	if err != nil {
		t.Fatalf("stagedInstallFiles: %v", err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.Name] = true
		if strings.HasPrefix(f.Name, preservedInstallDir+"/") {
			t.Errorf("planned to overwrite preserved tool %q", f.Name)
		}
	}
	for _, want := range []string{exe, "SDL2.dll", "Inter-Regular.ttf", releaseInfoFileName} {
		if !seen[filepath.FromSlash(want)] {
			t.Errorf("staged files are missing %q: %v", want, seen)
		}
	}
	if last := files[len(files)-1]; last.Name != releaseInfoFileName {
		t.Fatalf("release stamp must be copied last, got %q", last.Name)
	}

	// Re-staging the same release must reuse the complete tree instead of
	// extracting again.
	if err := stageDownloadedRelease(context.Background(), rel, archive, staging); err != nil {
		t.Fatalf("re-staging: %v", err)
	}
}

// TestStagingWithoutReadyMarkerIsExtractedAgain pins the reuse check: a tree
// holding the binary and the archive's own release-info.json — what an
// extraction cut short by a cancel or a power cut leaves — is not a staged
// release. It is unpacked again instead of being installed half-written.
func TestStagingWithoutReadyMarkerIsExtractedAgain(t *testing.T) {
	exe := releaseExecutableName()
	archive := filepath.Join(t.TempDir(), "JukaHub-Test.zip")
	writeReleaseZip(t, archive, map[string]string{
		exe:                 "new-binary",
		"SDL2.dll":          "new-dll",
		releaseInfoFileName: `{"tag":"JukaHub-20200101000000"}`,
	})
	staging := filepath.Join(t.TempDir(), "staging", "app-test")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		exe:                 "trunc",
		releaseInfoFileName: `{"tag":"JukaHub-20200101000000"}`,
	} {
		if err := os.WriteFile(filepath.Join(staging, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	rel := &githubRelease{TagName: "JukaHub-20261019120000", PublishedAt: "2026-10-19T12:00:00Z"}
	if err := stageDownloadedRelease(context.Background(), rel, archive, staging); err != nil {
		t.Fatalf("stageDownloadedRelease: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(staging, exe)); err != nil || string(got) != "new-binary" {
		t.Fatalf("staged binary = %q, %v; want the archive's full build", got, err)
	}
	if _, err := os.Stat(filepath.Join(staging, "SDL2.dll")); err != nil {
		t.Errorf("the re-extracted tree is missing SDL2.dll: %v", err)
	}
	if _, err := os.Stat(stagedReadyMarker(staging)); err != nil {
		t.Errorf("a complete staging tree must carry the ready marker: %v", err)
	}
}

// TestStagedUpdateUnzipsAndReplacesTheInstallation is the whole one-press path
// short of the restart: a release ZIP is staged, the swap helper is generated
// from the staged files, and running it (with the relaunch line removed) leaves
// the installation carrying the new build.
func TestStagedUpdateUnzipsAndReplacesTheInstallation(t *testing.T) {
	exe := releaseExecutableName()
	archive := filepath.Join(t.TempDir(), "JukaHub-Test.zip")
	writeReleaseZip(t, archive, map[string]string{
		exe:                 "new-binary",
		"SDL2.dll":          "new-dll",
		"Inter-Regular.ttf": "new-font",
		releaseInfoFileName: `{"tag":"JukaHub-20200101000000"}`,
	})

	staging := filepath.Join(t.TempDir(), "staging", "app-test")
	rel := &githubRelease{TagName: "JukaHub-20261019120000", PublishedAt: "2026-10-19T12:00:00Z"}
	if err := stageDownloadedRelease(context.Background(), rel, archive, staging); err != nil {
		t.Fatalf("stage: %v", err)
	}
	bin := findStagedBinary(staging)
	if bin == "" {
		t.Fatal("no staged executable")
	}

	// A throwaway installation with the previous release in place.
	install := t.TempDir()
	for name, body := range map[string]string{
		exe:                 "old-binary",
		"SDL2.dll":          "old-dll",
		"Inter-Regular.ttf": "old-font",
		releaseInfoFileName: `{"tag":"JukaHub-20200101000000"}`,
	} {
		if err := os.WriteFile(filepath.Join(install, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	files, err := stagedInstallFiles(filepath.Dir(bin), install)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	journalPath := filepath.Join(t.TempDir(), "journal.json")
	if err := os.WriteFile(journalPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "apply-update")
	if runtime.GOOS == "windows" {
		script += ".bat"
	}
	if err := writeSwapScript(script, filepath.Join(install, exe), files, journalPath, 0); err != nil {
		t.Fatalf("writeSwapScript: %v", err)
	}

	// Remove the relaunch step only: the test must not start a real app.
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	sep := "\n"
	if runtime.GOOS == "windows" {
		sep = "\r\n"
	}
	var kept []string
	for _, line := range strings.Split(string(data), sep) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "start ") || strings.Contains(trimmed, "nohup") ||
			trimmed == "sleep 2" || strings.HasPrefix(trimmed, "ping -n 3 ") {
			continue
		}
		kept = append(kept, line)
	}
	if err := os.WriteFile(script, []byte(strings.Join(kept, sep)), 0o700); err != nil {
		t.Fatal(err)
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", script)
	} else {
		cmd = exec.Command(script)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("swap helper failed: %v\n%s", err, out)
	}

	for name, want := range map[string]string{
		exe:                 "new-binary",
		"SDL2.dll":          "new-dll",
		"Inter-Regular.ttf": "new-font",
	} {
		got, err := os.ReadFile(filepath.Join(install, name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	info := readReleaseInfoFromDir(install)
	if info.Tag != rel.TagName {
		t.Errorf("installed stamp = %q, want %q", info.Tag, rel.TagName)
	}
	if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
		t.Error("journal must be removed after a successful swap")
	}
}
