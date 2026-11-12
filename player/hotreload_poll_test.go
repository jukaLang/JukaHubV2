package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConfigWatcherPollsOnlyWhilePaired covers PERF-6: with no Live Builder
// session paired the watcher does no file I/O, and pairing resumes polling on
// the files as they are then.
func TestConfigWatcherPollsOnlyWhilePaired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jukaconfig.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cw := NewConfigWatcher(path)
	// A change the poller would report if it were running.
	cw.lastMod[path] = time.Now().Add(-2 * time.Hour)

	cw.SetPolling(false)
	cw.checkFile()
	select {
	case <-cw.reloadCh:
		t.Fatal("a paused watcher queued a reload")
	case <-time.After(100 * time.Millisecond):
	}

	cw.SetPolling(true)
	if cw.paused {
		t.Fatal("resuming left the watcher paused")
	}
	// The resume baseline is the file as it is now, so this is the change the
	// running poller must notice.
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	cw.checkFile()
	select {
	case <-cw.reloadCh:
	case <-time.After(3 * time.Second):
		t.Fatal("a resumed watcher did not notice a change")
	}
}
