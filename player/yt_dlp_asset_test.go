package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestYtDlpAssetName(t *testing.T) {
	cases := []struct {
		goos, goarch, want string
	}{
		{"windows", "amd64", "yt-dlp.exe"},
		{"windows", "arm64", "yt-dlp.exe"},
		{"windows", "386", "yt-dlp_x86.exe"},
		// TrimUI Smart Pro ships no Python; it needs the standalone aarch64 build.
		{"linux", "arm64", "yt-dlp_linux_aarch64"},
		{"linux", "arm", "yt-dlp_linux_armv7l"},
		{"linux", "amd64", "yt-dlp_linux"},
		{"darwin", "arm64", "yt-dlp_macos"},
	}
	for _, tc := range cases {
		got, err := ytDlpAssetName(tc.goos, tc.goarch)
		if err != nil || got != tc.want {
			t.Errorf("ytDlpAssetName(%s/%s) = %q, %v; want %q", tc.goos, tc.goarch, got, err, tc.want)
		}
	}
	if _, err := ytDlpAssetName("plan9", "mips"); err == nil {
		t.Error("unsupported platform must return an error")
	}
}

// The handheld failure this guards against: yt-dlp's PyInstaller bootloader
// could not unpack its own files, the raw bootloader text was rendered into the
// results area, and the toast blamed the network.
func TestYtDlpFailureReasonForUnpackFailure(t *testing.T) {
	stderr := "[PYI-6130:ERROR] Failed to extract Cryptodome/Cipher/_ARC4.abi3.so: decompression resulted in return code -1!\n"
	got := ytDlpFailureReason(stderr, errors.New("exit status 1"), "")
	if strings.Contains(got, "Cryptodome") || strings.Contains(got, "PYI-") {
		t.Fatalf("raw bootloader output leaked into the message: %q", got)
	}
	if !strings.Contains(got, "could not unpack itself") {
		t.Fatalf("unpack failure should say what went wrong, got %q", got)
	}
}

// A real yt-dlp error keeps its own first line instead of being replaced by the
// generic network hint.
func TestYtDlpFailureReasonKeepsTheRealError(t *testing.T) {
	stderr := "ERROR: [youtube] dQw4w9WgXcQ: Video unavailable\nsome later line"
	if got := ytDlpFailureReason(stderr, errors.New("exit status 1"), ""); got != "ERROR: [youtube] dQw4w9WgXcQ: Video unavailable" {
		t.Fatalf("reason = %q", got)
	}
	if got := ytDlpFailureReason("", errors.New("exit status 1"), ""); got != "exit status 1" {
		t.Fatalf("empty stderr should fall back to the error, got %q", got)
	}
}

// The helper must get exactly one TMPDIR, and it must be a directory that can
// actually be written to, so PyInstaller has somewhere to unpack.
func TestHelperToolEnvPointsTmpdirAtWritableDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows one-file builds unpack into the system TEMP folder")
	}
	tool := filepath.Join(t.TempDir(), "required", "yt-dlp")
	if err := os.MkdirAll(filepath.Dir(tool), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tool, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	var tmpdirs []string
	for _, kv := range helperToolEnv(tool) {
		if strings.HasPrefix(kv, "TMPDIR=") {
			tmpdirs = append(tmpdirs, strings.TrimPrefix(kv, "TMPDIR="))
		}
	}
	if len(tmpdirs) != 1 {
		t.Fatalf("TMPDIR entries = %v, want exactly one", tmpdirs)
	}
	if want := filepath.Join(filepath.Dir(tool), ".tmp"); tmpdirs[0] != want {
		t.Fatalf("TMPDIR = %q, want %q", tmpdirs[0], want)
	}
	if err := os.WriteFile(filepath.Join(tmpdirs[0], "probe"), []byte("ok"), 0o600); err != nil {
		t.Fatalf("chosen TMPDIR is not writable: %v", err)
	}
}

// A candidate that cannot be created must not win over one that can: a
// read-only or full card is exactly how the unpack fails on the device.
func TestPickHelperTempDirSkipsUnusableCandidates(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	usable := filepath.Join(root, "usable")

	if got := pickHelperTempDir([]string{blocked, usable}); got != usable {
		t.Fatalf("picked %q, want %q", got, usable)
	}
	if info, err := os.Stat(usable); err != nil || !info.IsDir() {
		t.Fatalf("chosen directory was not created: %v", err)
	}
	if got := pickHelperTempDir([]string{blocked}); got != "" {
		t.Fatalf("picked %q when no candidate is usable", got)
	}
}

// Stale extraction directories fill the card; a fresh one may belong to a run
// that is still unpacking, so only old ones may be removed. A PID-named
// directory is judged by whether that process is still alive, because a long
// download keeps its unpacked files for the whole run.
func TestPurgeStaleHelperDirs(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "_MEI-oldrun")
	fresh := filepath.Join(dir, "_MEI-recentrun")
	other := filepath.Join(dir, "keep")
	live := filepath.Join(dir, fmt.Sprintf("_MEI%d", os.Getpid()))
	for _, d := range []string{stale, fresh, other, live} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, d := range []string{stale, live} {
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
	}

	purgeStaleHelperDirs(dir)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale unpack directory survived")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh unpack directory was removed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("unrelated directory was removed")
	}
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(live); err != nil {
			t.Error("unpack directory of a running helper was removed")
		}
	}
}

func TestHelperUnpackPID(t *testing.T) {
	if pid, ok := helperUnpackPID("_MEI4242"); !ok || pid != 4242 {
		t.Fatalf("helperUnpackPID = %d, %v", pid, ok)
	}
	for _, name := range []string{"_MEI", "_MEIabc", "MEI123", "_MEI0", "_MEI-5"} {
		if pid, ok := helperUnpackPID(name); ok {
			t.Errorf("helperUnpackPID(%q) = %d, want not ok", name, pid)
		}
	}
}

// A search command is config-supplied, so it must not be able to hand yt-dlp
// an option that runs another program.
func TestRejectYtDlpExecFlags(t *testing.T) {
	allowed := []string{
		"--flat-playlist",
		"--dump-single-json",
		"--default-search", "ytsearch",
		"--playlist-end", "20",
		"ytsearch20:funny cats",
		"--cookies", "/tmp/cookies.txt",
		"--extractor-args", "youtube:api_key=abc=",
	}
	if err := rejectYtDlpExecFlags(allowed); err != nil {
		t.Fatalf("benign search command rejected: %v", err)
	}

	for _, args := range [][]string{
		{"--exec", "rm -rf /"},
		{"--exec-before-download=sh -c id"},
		{"--downloader", "curl"},
		{"--external-downloader=wget"},
		{"--postprocessor-args", "-i"},
		{"--ppa=ffmpeg:-i"},
		{"--update"},
	} {
		if err := rejectYtDlpExecFlags(args); err == nil {
			t.Errorf("execution-capable args %v were accepted", args)
		}
	}
}
