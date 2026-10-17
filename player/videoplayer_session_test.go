package main

import (
	"os"
	"os/exec"
	"path/filepath"
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

// Stopping a video used to delete whatever file it had played, including the
// user's own media on the SD card. Only downloads owned by playWithTempFile may
// be removed.
func TestRemoveTempVideoLeavesUserMedia(t *testing.T) {
	dir := t.TempDir()

	userFile := filepath.Join(dir, "holiday.mp4")
	if err := os.WriteFile(userFile, []byte("user media"), 0o644); err != nil {
		t.Fatal(err)
	}
	removeTempVideo(userFile)
	if _, err := os.Stat(userFile); err != nil {
		t.Fatalf("removeTempVideo deleted a user file: %v", err)
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
