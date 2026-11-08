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
	// The Wi-Fi suite stubs wpa_cli output, so it must be answered by the
	// supplicant backend even on a Windows machine that has netsh. The Windows
	// backend's own tests call the winWifi* functions directly.
	wifiPOSIXForced = true
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
	// The on-screen keyboard learns typed words into the data directory too;
	// the suite's typing must not reach the real store.
	if err == nil {
		vkLearnedPath = func() string { return filepath.Join(dir, "vk-words.json") }
	}
	// The Emulators pages start a background buildbot/GitHub check when they
	// open, and Windows installs land in the real Emulators folder: neither
	// belongs in the suite, which swaps those URLs and folders per test.
	emuUpdateChecksEnabled = false
	// The chat's sender loop would connect to Discord and Groq from the suite
	// whenever a test opens the Chat scene; its tests drive chatSyncStep.
	chatSyncEnabled = false
	emuDir, emuErr := os.MkdirTemp("", "jukahub-emulators")
	if emuErr == nil {
		emuWinRootsOverride = []string{emuDir}
	}
	// The Downloads library (dl_library.go) scans and indexes the download
	// folder in the background whenever Media or Favorites opens; the suite
	// must never read or write the real one.
	dlDir, dlErr := os.MkdirTemp("", "jukahub-downloads")
	if dlErr == nil {
		downloadsRootOverride = dlDir
	}
	// A game's page keeps its data (games.json, the libretro lists) and the ROM
	// manager its undo journal in the data directory: the suite uses a
	// temporary one, writes it synchronously and never looks a game up online.
	gmetaDir, gmetaErr := os.MkdirTemp("", "jukahub-gamemeta")
	if gmetaErr == nil {
		gmetaStorePath = func() string { return filepath.Join(gmetaDir, "games.json") }
		gmetaDatDir = func() string { return filepath.Join(gmetaDir, "libretro") }
		rommJournalPath = func() string { return filepath.Join(gmetaDir, "rom-journal.json") }
	}
	gmetaSyncWrites = true
	gmetaCelebrations = false
	gmetaLookupEnabled = false
	gmetaBackgroundLoads = false
	rommScanEnabled = false
	code := m.Run()
	if gmetaErr == nil {
		os.RemoveAll(gmetaDir)
	}
	if dlErr == nil {
		os.RemoveAll(dlDir)
	}
	if emuErr == nil {
		os.RemoveAll(emuDir)
	}
	if err == nil {
		os.RemoveAll(dir)
	}
	if cookieErr == nil {
		os.RemoveAll(cookieDir)
	}
	os.Exit(code)
}
