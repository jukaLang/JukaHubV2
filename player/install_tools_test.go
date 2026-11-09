package main

// Tests for the helper-tool install pipeline: nested archive extraction that
// keeps a working binary, downloads that refuse a truncated body, the
// repository allowlist, verification that never executes the file, and install
// runs that cannot cancel each other.

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestExtractToolFilesNestedZip(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "ffmpeg.zip")
	out := t.TempDir()

	// BtbN layout: binaries nested a few folders deep inside the archive.
	zw := zip.NewWriter(mustCreate(t, archive))
	for name, body := range map[string]string{
		"ffmpeg-master-latest-win64-gpl/bin/ffplay.exe": "ffplay-bytes",
		"ffmpeg-master-latest-win64-gpl/bin/ffmpeg.exe": "ffmpeg-bytes",
		"ffmpeg-master-latest-win64-gpl/README.txt":     "docs",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	found, err := extractToolFiles(archive, out, []string{"ffplay.exe", "ffmpeg.exe", "ffprobe.exe"})
	if err != nil {
		t.Fatalf("extractToolFiles: %v", err)
	}
	if len(found) != 2 {
		t.Errorf("found = %v, want ffplay.exe and ffmpeg.exe", found)
	}
	for name, want := range map[string]string{
		"ffplay.exe": "ffplay-bytes",
		"ffmpeg.exe": "ffmpeg-bytes",
	} {
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Errorf("%s not extracted to dest root: %v", name, err)
			continue
		}
		if string(data) != want {
			t.Errorf("%s content = %q, want %q", name, data, want)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "README.txt")); err == nil {
		t.Error("README.txt should not be extracted")
	}
}

func TestExtractToolFilesNestedTarGz(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "ffmpeg.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	files := map[string]string{
		"ffmpeg-master-latest-linuxarm64-gpl/bin/ffplay": "ffplay-bytes",
		"ffmpeg-master-latest-linuxarm64-gpl/bin/ffmpeg": "ffmpeg-bytes",
		"ffmpeg-master-latest-linuxarm64-gpl/LICENSE":    "license",
	}
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	found, err := extractToolFiles(archive, out, []string{"ffplay", "ffmpeg"})
	if err != nil {
		if strings.Contains(err.Error(), "no tar available") || strings.Contains(err.Error(), "xz support missing") {
			t.Skipf("system tar cannot extract this archive: %v", err)
		}
		t.Fatalf("extractToolFiles(tar.gz): %v", err)
	}
	if len(found) != 2 {
		t.Errorf("found = %v, want ffplay and ffmpeg", found)
	}
	for name, want := range map[string]string{"ffplay": "ffplay-bytes", "ffmpeg": "ffmpeg-bytes"} {
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Errorf("%s not extracted: %v", name, err)
			continue
		}
		if string(data) != want {
			t.Errorf("%s content = %q, want %q", name, data, want)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "LICENSE")); err == nil {
		t.Error("LICENSE should not be extracted")
	}
}

func TestDownloadFileRejectsTinyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tiny"))
	}))
	defer srv.Close()
	if err := downloadFile(srv.URL, filepath.Join(t.TempDir(), "out.bin")); err == nil {
		t.Error("downloadFile should reject bodies under 4096 bytes")
	}
}

func TestGetToolPathNeverReturnsDirectory(t *testing.T) {
	// A configured tools_path that exists but does not contain the tool must
	// fall through, not return the directory itself (exec would fail with
	// "is a directory").
	dir := t.TempDir()
	got := getToolPath("jukahub-test-probe", &Config{Variables: Variables{ToolsPath: dir}})
	if info, err := os.Stat(got); err == nil && info.IsDir() {
		t.Fatalf("getToolPath returned the directory %q", got)
	}
}

func TestResolveToolURLRejectsUnknownRepo(t *testing.T) {
	if _, err := resolveToolURL("evil/exfiltrate", "", "x.zip"); err == nil {
		t.Error("resolveToolURL must reject repos outside the allowlist")
	}
}

// resetInstallBar clears the overlay state between tests. It resets fields
// individually: assigning a whole installBarState would swap the mutex and
// unlock an unlocked one.
func resetInstallBar() {
	installBar.mu.Lock()
	installBar.visible = false
	installBar.tool = ""
	installBar.label = ""
	installBar.source = ""
	installBar.phase = ""
	installBar.done = 0
	installBar.total = 0
	installBar.speed = 0
	installBar.message = ""
	installBar.endedAt = time.Time{}
	installBar.animAt = time.Time{}
	installBar.retryable = false
	if installBar.runCancel != nil {
		installBar.runCancel()
		installBar.runCancel = nil
	}
	installBar.runCtx = nil
	installBar.mu.Unlock()
}

// The run token is per-run minted: a second begin retires the previous
// token (and any worse — the retired one is cancelled, so a goroutine still
// winding down from the old run sees the stop) and a fresh cancel can no
// longer clobber the new run's token.
func TestInstallBarRunContextPerRun(t *testing.T) {
	t.Cleanup(resetInstallBar)

	installBarBegin("ffmpeg")
	first := installBarRunContext()
	installBarBegin("ffplay")
	second := installBarRunContext()
	if first == second {
		t.Fatal("two runs share one token — a cancel for one would kill the other")
	}
	if first.Err() == nil {
		t.Fatal("retiring the first run did not stop its still-winding goroutine")
	}
	if second.Err() != nil {
		t.Fatalf("the fresh run started cancelled: %v", second.Err())
	}
}

// TestInstallBarAbortRunLeavesALaterRunAlone: a run whose token a later begin
// retired must not hide or cancel the run that replaced it on its way out. On
// a first boot the core update and the helper-tool download took each other
// down that way.
func TestInstallBarAbortRunLeavesALaterRunAlone(t *testing.T) {
	t.Cleanup(resetInstallBar)

	installBarBegin("ffmpeg")
	first := installBarGeneration()
	installBarBeginLabelled("Emulator cores", "Updating emulator cores")
	second := installBarRunContext()

	installBarAbortRun(first)
	if visible, tool, _, _, _, _, _, _, _ := installBarSnapshot(); !visible || tool != "Emulator cores" {
		t.Fatalf("the retired run hid the card: visible=%v tool=%q", visible, tool)
	}
	if second.Err() != nil {
		t.Fatalf("the retired run cancelled its replacement: %v", second.Err())
	}

	installBarAbortRun(installBarGeneration())
	if visible, _, _, _, _, _, _, _, _ := installBarSnapshot(); visible {
		t.Error("a run could not hide its own card")
	}
}

func TestVerifyHelperToolChecksFileWithoutExecuting(t *testing.T) {
	dir := t.TempDir()
	valid := make([]byte, minHelperToolBytes+64)
	switch runtime.GOOS {
	case "windows":
		copy(valid, []byte("MZ"))
	case "linux":
		copy(valid, []byte{0x7f, 'E', 'L', 'F'})
	}
	good := filepath.Join(dir, "ffplay")
	if err := os.WriteFile(good, valid, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifyHelperTool(good); err != nil {
		t.Errorf("valid binary rejected: %v", err)
	}

	truncated := filepath.Join(dir, "truncated")
	if err := os.WriteFile(truncated, valid[:4096], 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifyHelperTool(truncated); err == nil {
		t.Error("a 4 KB download must be rejected as truncated")
	}

	if runtime.GOOS == "windows" || runtime.GOOS == "linux" {
		junk := filepath.Join(dir, "junk")
		if err := os.WriteFile(junk, make([]byte, minHelperToolBytes+64), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := verifyHelperTool(junk); err == nil {
			t.Error("a file without an executable header must be rejected")
		}
	}
}

func TestExtractToolFilesKeepsExistingBinary(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "ffmpeg.zip")
	out := t.TempDir()

	zw := zip.NewWriter(mustCreate(t, archive))
	for name, body := range map[string]string{
		"ffmpeg-master/bin/ffplay.exe": "new-ffplay",
		"ffmpeg-master/bin/ffmpeg.exe": "new-ffmpeg",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	// A copy that is already installed (possibly the executable of a running
	// player) must be left untouched: rewriting it would fail with ETXTBSY.
	existing := filepath.Join(out, "ffplay.exe")
	if err := os.WriteFile(existing, []byte("running-copy"), 0o755); err != nil {
		t.Fatal(err)
	}

	found, err := extractToolFiles(archive, out, []string{"ffplay.exe", "ffmpeg.exe"})
	if err != nil {
		t.Fatalf("extractToolFiles: %v", err)
	}
	if len(found) != 2 {
		t.Errorf("found = %v, want both binaries", found)
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "running-copy" {
		t.Errorf("existing ffplay.exe was modified: %q (err %v)", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(out, "ffmpeg.exe")); err != nil || string(data) != "new-ffmpeg" {
		t.Errorf("ffmpeg.exe = %q (err %v), want new-ffmpeg", data, err)
	}

	// The staging directory must not be left behind in the tools folder.
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("staging directory %q left behind in the tools folder", e.Name())
		}
	}
}

func mustCreate(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
