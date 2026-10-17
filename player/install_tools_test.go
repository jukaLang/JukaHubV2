package main

// Tests for the helper-tool install pipeline: ffmpeg asset selection, nested
// archive extraction, download progress reporting, and PATH discovery.

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

func TestFFmpegAssetNames(t *testing.T) {
	cases := []struct {
		goos, goarch string
		wantFirst    string
	}{
		{"windows", "amd64", "ffmpeg-master-latest-win64-gpl.zip"},
		{"windows", "arm64", "ffmpeg-master-latest-winarm64-gpl.zip"},
		{"linux", "amd64", "ffmpeg-master-latest-linux64-gpl.tar.xz"},
		{"linux", "arm64", "ffmpeg-master-latest-linuxarm64-gpl.tar.xz"},
	}
	for _, tc := range cases {
		got, err := ffmpegAssetNames(tc.goos, tc.goarch)
		if err != nil {
			t.Errorf("ffmpegAssetNames(%s, %s) error: %v", tc.goos, tc.goarch, err)
			continue
		}
		if len(got) == 0 || got[0] != tc.wantFirst {
			t.Errorf("ffmpegAssetNames(%s, %s) = %v, want first %q", tc.goos, tc.goarch, got, tc.wantFirst)
		}
	}
	if _, err := ffmpegAssetNames("plan9", "mips"); err == nil {
		t.Error("ffmpegAssetNames(plan9, mips) should fail")
	}
}

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

func TestFFmpegArchiveExt(t *testing.T) {
	cases := map[string]string{
		"https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip":         ".zip",
		"https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linuxarm64-gpl.tar.xz": ".tar.xz",
		"https://example.invalid/ffmpeg.tar.gz": ".tar.gz",
		"https://example.invalid/unknown-asset": ".zip",
	}
	for url, want := range cases {
		if got := ffmpegArchiveExt(url); got != want {
			t.Errorf("ffmpegArchiveExt(%q) = %q, want %q", url, got, want)
		}
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

func TestDownloadFileWithProgress(t *testing.T) {
	payload := make([]byte, 256*1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	var calls int
	var lastDone, lastTotal int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "262144")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	err := downloadFileWithProgress(srv.URL, dest, func(done, total int64) {
		calls++
		lastDone, lastTotal = done, total
	})
	if err != nil {
		t.Fatalf("downloadFileWithProgress: %v", err)
	}
	if calls < 2 {
		t.Errorf("progress called %d times, want >= 2", calls)
	}
	if lastDone != int64(len(payload)) || lastTotal != int64(len(payload)) {
		t.Errorf("final progress = (%d, %d), want (%d, %d)", lastDone, lastTotal, len(payload), len(payload))
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(payload) {
		t.Errorf("downloaded %d bytes, want %d", len(got), len(payload))
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

func TestGetToolPathFindsPATHInstall(t *testing.T) {
	// Probe name that can never exist in the tools directory, so the PATH
	// fallback is what resolves it.
	tool := "jukahub-test-probe"
	dir := t.TempDir()
	exe := tool
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	full := filepath.Join(dir, exe)
	if err := os.WriteFile(full, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got := getToolPath(tool, &Config{})
	if abs, err := filepath.Abs(got); err != nil || abs != full {
		t.Errorf("getToolPath(%s) = %q, want %q", tool, got, full)
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

func TestGetToolPathAcceptsBinaryToolsPath(t *testing.T) {
	dir := t.TempDir()
	name := "jukahub-test-probe"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := getToolPath("jukahub-test-probe", &Config{Variables: Variables{ToolsPath: full}})
	if absGot, err := filepath.Abs(got); err != nil || absGot != full {
		t.Fatalf("getToolPath = %q, want %q", got, full)
	}
}

func TestVersionProbeArgs(t *testing.T) {
	cases := map[string]string{
		"ffmpeg":              "-version",
		"ffplay.exe":          "-version",
		"ffprobe":             "-version",
		"yt-dlp":              "--version",
		"yt-dlp.exe":          "--version",
		"C:/tools/ffplay.exe": "-version",
	}
	for in, want := range cases {
		got := versionProbeArgs(in)
		if len(got) != 1 || got[0] != want {
			t.Errorf("versionProbeArgs(%q) = %v, want [%s]", in, got, want)
		}
	}
}

func TestResolveToolURLRejectsUnknownRepo(t *testing.T) {
	if _, err := resolveToolURL("evil/exfiltrate", "", "x.zip"); err == nil {
		t.Error("resolveToolURL must reject repos outside the allowlist")
	}
}

func TestFormatInstallBytes(t *testing.T) {
	cases := map[int64]string{
		512:          "512 B",
		1536:         "1.5 KB",
		78 * 1 << 20: "78.0 MB",
	}
	for in, want := range cases {
		if got := formatInstallBytes(in); got != want {
			t.Errorf("formatInstallBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

// resetInstallBar clears the overlay state between tests. It resets fields
// individually: assigning a whole installBarState would swap the mutex and
// unlock an unlocked one.
func resetInstallBar() {
	installBar.mu.Lock()
	installBar.visible = false
	installBar.tool = ""
	installBar.phase = ""
	installBar.done = 0
	installBar.total = 0
	installBar.speed = 0
	installBar.message = ""
	installBar.endedAt = time.Time{}
	installBar.animAt = time.Time{}
	installBar.retryable = false
	installBar.mu.Unlock()
}

func TestInstallBarLifecycle(t *testing.T) {
	t.Cleanup(resetInstallBar)

	installBarBegin("ffmpeg")
	visible, tool, phase, detail, _, _, _, _, ending := installBarSnapshot()
	if !visible || tool != "ffmpeg" || phase != installPhaseResolving || ending {
		t.Fatalf("after begin: visible=%v tool=%q phase=%q ending=%v", visible, tool, phase, ending)
	}
	if detail != "" {
		t.Errorf("resolving detail = %q, want empty (progress only once downloading)", detail)
	}

	installBarPhase(installPhaseDownload)
	installBarProgress(3*1<<20, 10*1<<20)
	visible, _, phase, detail, done, total, _, _, _ := installBarSnapshot()
	if !visible || phase != installPhaseDownload {
		t.Fatalf("after progress: visible=%v phase=%q", visible, phase)
	}
	if done != 3*1<<20 || total != 10*1<<20 {
		t.Errorf("progress = (%d, %d), want (%d, %d)", done, total, 3*1<<20, 10*1<<20)
	}
	if detail != "3.0 MB of 10.0 MB" {
		t.Errorf("detail = %q, want \"3.0 MB of 10.0 MB\"", detail)
	}

	installBarDone()
	visible, _, phase, detail, _, _, _, _, ending = installBarSnapshot()
	if !visible || phase != installPhaseDone || !ending {
		t.Fatalf("after done: visible=%v phase=%q ending=%v", visible, phase, ending)
	}
	if detail != "" {
		t.Errorf("done detail = %q, want empty so the card shows \"Ready to use\"", detail)
	}

	// The card hides itself a couple of seconds after settling.
	installBar.mu.Lock()
	installBar.endedAt = time.Now().Add(-10 * time.Second)
	installBar.mu.Unlock()
	if visible, _, _, _, _, _, _, _, _ := installBarSnapshot(); visible {
		t.Error("card should auto-hide after its hold period")
	}
}

func TestInstallBarFailureCarriesReason(t *testing.T) {
	t.Cleanup(resetInstallBar)
	installBarBegin("ffplay")
	installBarFail("download timed out")
	visible, _, phase, detail, _, _, _, _, _ := installBarSnapshot()
	if !visible || phase != installPhaseError || detail != "download timed out" {
		t.Fatalf("failure state = visible:%v phase:%q detail:%q", visible, phase, detail)
	}
	if _, ok := installBarRetryPending(); ok {
		t.Error("a plain failure must not advertise an A: retry action")
	}
}

func TestInstallBarFailureOffersRetry(t *testing.T) {
	t.Cleanup(resetInstallBar)

	installBarBegin("ffmpeg")
	installBarPhase(installPhaseDownload)
	installBarFailRetry("ffmpeg", "No internet — connect WiFi, then press A")

	tool, ok := installBarRetryPending()
	if !ok || tool != "ffmpeg" {
		t.Fatalf("retry pending = (%q, %v), want (ffmpeg, true)", tool, ok)
	}
	visible, _, phase, detail, _, _, _, _, _ := installBarSnapshot()
	if !visible || phase != installPhaseError || !strings.Contains(detail, "WiFi") {
		t.Fatalf("retry state = visible:%v phase:%q detail:%q", visible, phase, detail)
	}

	// Starting the retry clears the card's failed state.
	installBarBegin("ffmpeg")
	if _, ok := installBarRetryPending(); ok {
		t.Error("retry must stop being offered once a new install starts")
	}
}

func TestInstallCardDetailTruncatesLongErrors(t *testing.T) {
	if got := installCardDetail("download timed out"); got != "download timed out" {
		t.Errorf("short detail = %q, want it unchanged", got)
	}
	got := installCardDetail(strings.Repeat("x", 200))
	if runes := []rune(got); len(runes) != 64 || runes[len(runes)-1] != '…' {
		t.Errorf("long detail = %q (%d runes), want 64 runes ending in an ellipsis", got, len([]rune(got)))
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
