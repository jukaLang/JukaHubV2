package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A pending debounced save must not follow the process into a different working
// directory. This used to dirty a test-runner's checkout two seconds after the
// change was made.
func TestDebouncedUserConfigSaveStaysInScheduledDir(t *testing.T) {
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Restore the working directory before the testing package removes the temp
	// directories; Windows cannot delete a directory that is still the cwd.
	defer func() { _ = os.Chdir(prevWD) }()
	prevDelay := userConfigSaveDelay
	prevCache := userConfigCache
	userConfigSaveDelay = 20 * time.Millisecond
	userConfigCache = nil
	t.Cleanup(func() {
		userConfigSaveMutex.Lock()
		if userConfigSaveTimer != nil {
			userConfigSaveTimer.Stop()
			userConfigSaveTimer = nil
		}
		pendingUserConfig = nil
		pendingUserConfigDir = ""
		userConfigSaveMutex.Unlock()
		userConfigSaveDelay = prevDelay
		userConfigCache = prevCache
		_ = os.Chdir(prevWD)
	})

	dirA := t.TempDir()
	dirB := t.TempDir()
	if err := os.Chdir(dirA); err != nil {
		t.Fatal(err)
	}

	saveUserConfigDebounced(&UserConfig{
		Variables: UserVariables{
			ReducedMotion: true,
			Custom:        map[string]interface{}{"debounced": "yes"},
		},
	})

	// Simulate the directory going away before the timer fires.
	if err := os.Chdir(dirB); err != nil {
		t.Fatal(err)
	}

	pathA := filepath.Join(dirA, "jukauser.json")
	deadline := time.Now().Add(2 * time.Second)
	var data []byte
	var readErr error
	// Poll for a readable, non-empty file instead of stat-then-read: on Windows
	// a scanner or antivirus can briefly hold a freshly renamed file, and an
	// immediate read then fails with a sharing violation even though the save
	// landed correctly. The assertion is unchanged, only the wait is robust.
	for time.Now().Before(deadline) {
		data, readErr = os.ReadFile(pathA)
		if readErr == nil && len(data) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if readErr != nil {
		t.Fatalf("debounced save did not land in the scheduled directory: %v", readErr)
	}
	if len(data) == 0 {
		t.Fatal("debounced save wrote an empty file")
	}
	if _, err := os.Stat(filepath.Join(dirB, "jukauser.json")); !os.IsNotExist(err) {
		t.Errorf("debounced save leaked into the current directory (err=%v)", err)
	}
}
