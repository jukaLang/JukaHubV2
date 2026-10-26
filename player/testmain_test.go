package main

import (
	"os"
	"path/filepath"
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
	// Keep the browser's per-site filter exceptions away from the developer's
	// real data directory: page loads in the suite consult that list.
	dir, err := os.MkdirTemp("", "jukahub-browser-block")
	if err == nil {
		browserAdBlockSitesPath = func() string { return filepath.Join(dir, "browser-block.json") }
		resetBrowserAdBlockSitesForTest()
	}
	// Same reason for the cookie jar: it is written to the data directory now,
	// and a page load in the suite must not touch the real card.
	cookieDir, cookieErr := os.MkdirTemp("", "jukahub-browser-cookies")
	if cookieErr == nil {
		browserCookiesPath = func() string { return filepath.Join(cookieDir, "browser-cookies.json") }
		browserResetCookies()
	}
	code := m.Run()
	if err == nil {
		os.RemoveAll(dir)
	}
	if cookieErr == nil {
		os.RemoveAll(cookieDir)
	}
	os.Exit(code)
}
