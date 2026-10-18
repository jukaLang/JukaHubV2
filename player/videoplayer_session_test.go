package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// restorePlaybackGlobals snapshots the global player state so a test cannot
// leak sessions or phases into the rest of the suite.
func restorePlaybackGlobals(t *testing.T) {
	t.Helper()
	prevSession := playbackCurrentSession()
	prevPhase := playerPhase()
	t.Cleanup(func() {
		playbackMu.Lock()
		playbackCurrent = prevSession
		playbackMu.Unlock()
		embeddedPlayer.mu.Lock()
		embeddedPlayer.phase = prevPhase
		embeddedPlayer.mu.Unlock()
	})
}

// The bug users hit: a video closed about a second after it started. A decoder
// that had just been superseded (a second tap on a result, a seek, a restart)
// reached the end of its loop and its finish() torn down the playback that had
// replaced it. A stale session must never touch the current one.
func TestStaleSessionDoesNotStopNewerPlayback(t *testing.T) {
	restorePlaybackGlobals(t)

	first := beginPlaybackSession(filepath.Join(t.TempDir(), "first.mp4"), "", false)
	second := beginPlaybackSession(filepath.Join(t.TempDir(), "second.mp4"), "", false)

	embeddedPlayer.mu.Lock()
	embeddedPlayer.phase = "playing"
	embeddedPlayer.mu.Unlock()

	first.finish()

	if got := playerPhase(); got != "playing" {
		t.Fatalf("stale session stopped the current playback: phase = %q", got)
	}
	if !second.isCurrent() {
		t.Fatal("second session lost ownership of playback")
	}
	second.stop()
}

// stop() must cancel the session context, so a decoder that is still starting
// cannot register a process for a session that is already gone.
func TestStoppedSessionRejectsNewCommands(t *testing.T) {
	restorePlaybackGlobals(t)

	sess := beginPlaybackSession(filepath.Join(t.TempDir(), "clip.mp4"), "", false)
	sess.stop()

	if sess.registerVideoCmd(&exec.Cmd{}) {
		t.Fatal("stopped session accepted a video command")
	}
	if sess.registerAudioCmd(&exec.Cmd{}) {
		t.Fatal("stopped session accepted an audio command")
	}
}

// Downloads must be staged in the directory cleanup checks, otherwise files
// pile up on the SD card (and on the handheld /tmp used to be RAM-backed, which
// got the app killed while downloading).
func TestTempVideoPathUsesVideoTempDir(t *testing.T) {
	dir := t.TempDir()
	prevTempDir := videoTempDir
	videoTempDir = func() string { return dir }
	t.Cleanup(func() { videoTempDir = prevTempDir })

	path := tempVideoPath()
	if filepath.Dir(path) != dir {
		t.Fatalf("tempVideoPath() = %q, want it inside %q", path, dir)
	}
	if base := filepath.Base(path); !strings.HasPrefix(base, "jukahub-video-") || !strings.HasSuffix(base, ".mp4") {
		t.Fatalf("tempVideoPath() base = %q, want a downloadable mp4 name", base)
	}

	// The file it names must be the file cleanup removes.
	if err := os.WriteFile(path, []byte("download"), 0o644); err != nil {
		t.Fatal(err)
	}
	removeTempVideo(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("removeTempVideo did not remove %q: %v", path, err)
	}
}

// Stopping a video used to delete whatever file it had played, including the
// user's own media on the SD card. Only downloads owned by playWithTempFile may
// be removed.
func TestRemoveTempVideoLeavesUserMedia(t *testing.T) {
	dir := t.TempDir()
	prevTempDir := videoTempDir
	videoTempDir = func() string { return dir }
	t.Cleanup(func() { videoTempDir = prevTempDir })

	userFile := filepath.Join(dir, "holiday.mp4")
	if err := os.WriteFile(userFile, []byte("user media"), 0o644); err != nil {
		t.Fatal(err)
	}
	removeTempVideo(userFile)
	if _, err := os.Stat(userFile); err != nil {
		t.Fatalf("removeTempVideo deleted a user file: %v", err)
	}

	// A same-named file outside the download directory is not ours either.
	outside := t.TempDir()
	foreign := filepath.Join(outside, "jukahub-video-456.mp4")
	if err := os.WriteFile(foreign, []byte("not ours"), 0o644); err != nil {
		t.Fatal(err)
	}
	removeTempVideo(foreign)
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("removeTempVideo deleted a file outside the temp dir: %v", err)
	}

	tempFile := filepath.Join(dir, "jukahub-video-123.mp4")
	if err := os.WriteFile(tempFile, []byte("download"), 0o644); err != nil {
		t.Fatal(err)
	}
	removeTempVideo(tempFile)
	if _, err := os.Stat(tempFile); !os.IsNotExist(err) {
		t.Fatalf("removeTempVideo left its own download behind (err = %v)", err)
	}
}
