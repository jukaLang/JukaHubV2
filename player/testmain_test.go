package main

import (
	"os"
	"testing"
)

// TestMain disables the debounced user-config writer for the whole test binary.
// Loading the shipped jukaconfig.json schedules a save; without this, running
// the suite rewrote jukauser.json and jukauser.json.bak in the checkout a
// second after a test finished. Direct saves (saveUserConfig) still work, and
// TestDebouncedUserConfigSaveStaysInScheduledDir turns the writer back on for
// its own check.
func TestMain(m *testing.M) {
	userConfigWritesEnabled = false
	os.Exit(m.Run())
}
