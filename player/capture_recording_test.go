package main

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// captureTestTool finds a helper the app itself would use; the test binary can
// run without a PATH entry, so the bundled copy is tried too.
func captureTestTool(t *testing.T, name string) string {
	t.Helper()
	if p := getToolPath(name, nil); toolPathRunnable(p) {
		return p
	}
	candidates := []string{filepath.Join("required", name), filepath.Join("required", name+".exe")}
	if p, err := exec.LookPath(name); err == nil {
		candidates = append(candidates, p)
	}
	for _, p := range candidates {
		if toolPathRunnable(p) {
			return p
		}
	}
	t.Skipf("%s is not installed; the media test needs it", name)
	return ""
}

func captureTestRun(t *testing.T, bin string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := hiddenCommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(bin), strings.Join(args, " "), err, out)
	}
}

func captureTestCodec(t *testing.T, probe, path string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := hiddenCommandContext(ctx, probe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", filepath.Base(path), err)
	}
	return strings.TrimSpace(string(out))
}

// TestCaptureTrimStreamCopyKeepsTheCodec is the trim contract's media half: a
// source H.264 clip must come back H.264 (a stream copy), not MPEG-4 part 2 as
// the old always-re-encode path produced, and the original must be untouched.
func TestCaptureTrimStreamCopyKeepsTheCodec(t *testing.T) {
	ffmpeg := captureTestTool(t, "ffmpeg")
	probe := captureTestTool(t, "ffprobe")
	dir := t.TempDir()
	src := filepath.Join(dir, "Capture_2026-11-27_12-00-00.mp4")
	captureTestRun(t, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=15:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4",
		"-c:v", "libx264", "-g", "1", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", "-y", src)
	before, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	beforeSum := sha256.Sum256(before)

	out, err := captureTrimRecording(context.Background(), ffmpeg, src, 1, 3, []string{dir})
	if err != nil {
		t.Fatalf("captureTrimRecording: %v", err)
	}
	if got := captureTestCodec(t, probe, out); got != "h264" {
		t.Fatalf("trimmed codec = %q, want h264 from a stream copy", got)
	}
	after, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("the original was removed: %v", err)
	}
	if sha256.Sum256(after) != beforeSum {
		t.Fatal("the original recording changed")
	}
	if !strings.Contains(filepath.Base(out), "(trimmed)") {
		t.Fatalf("trimmed name = %q, want a (trimmed) copy", filepath.Base(out))
	}
}

func TestCaptureDeleteNeedsTwoPresses(t *testing.T) {
	var c captureDeleteConfirm
	now := time.Unix(1_800_000_000, 0)
	if c.armed("a.mp4", now) {
		t.Fatal("the first press deleted")
	}
	if !c.armed("a.mp4", now.Add(2*time.Second)) {
		t.Fatal("the second press did not delete")
	}
	if c.armed("b.mp4", now) {
		t.Fatal("a different clip's press was taken as confirmation")
	}
	if c.armed("a.mp4", now.Add(captureDeleteWindow+time.Second)) {
		t.Fatal("an old press still confirmed a delete")
	}
	if !c.armed("a.mp4", now.Add(captureDeleteWindow+2*time.Second)) {
		t.Fatal("the re-armed press did not delete")
	}
}

func TestCaptureDeleteRecordingStaysInsideTheCaptureFolders(t *testing.T) {
	dir := t.TempDir()
	clip := filepath.Join(dir, "Capture_2026-11-27_12-00-00.mp4")
	if err := os.WriteFile(clip, []byte("a finished recording"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := captureDeleteRecording(clip, []string{dir}); err != nil {
		t.Fatalf("delete in its folder: %v", err)
	}
	if _, err := os.Stat(clip); !os.IsNotExist(err) {
		t.Fatal("the allowed recording still exists")
	}

	outside := filepath.Join(t.TempDir(), "elsewhere.mp4")
	if err := os.WriteFile(outside, []byte("not a capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := captureDeleteRecording(outside, []string{dir}); err == nil {
		t.Fatal("a recording outside the capture folders was deleted")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("the outside file was touched: %v", err)
	}
}

func TestCaptureRecordingMetaShowsDurationAndSize(t *testing.T) {
	clip := captureRecording{size: 4 << 20, duration: 83.4}
	got := captureRecordingMeta(clip)
	want := "1:23 · " + formatSize(clip.size)
	if got != want {
		t.Fatalf("captureRecordingMeta = %q, want %q", got, want)
	}
	if got := captureRecordingMeta(captureRecording{size: 2048}); got != formatSize(2048) {
		t.Fatalf("unknown duration meta = %q", got)
	}
}
