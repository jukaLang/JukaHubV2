package main

// store_scenes.go — the two Go-owned screens added on 2026-11-17 and the one
// contract they share.
//
//   - Ports Store (ports_store*.go): PortMaster's whole catalogue as a store —
//     shelves, genres, "Ready to Run" ports that need no game files, their
//     screenshots and download counts, a details sheet, and one-press install
//     with a queue and "Update all".
//   - Cloud Gaming (cloudgaming*.go): Xbox Cloud Gaming (Game Pass) and
//     GeForce NOW. On the handheld both stream through GreenOvercast, a native
//     ARM64 client installed as a PortMaster port; on a PC through the
//     services' own apps or the browser.
//
// Both scenes are injected in memory like the APK Store and Minecraft
// (main.go and hotreload.go call storeScenesEnsure; the user's config is never
// rewritten), and both route clicks and the mouse wheel through the two
// functions below, so main.go carries one line per hook for the pair.
//
// The shared contract is portsInstallZip (ports_store_install.go): every
// PortMaster zip JukaHub downloads — a port from the store, GreenOvercast for
// Cloud Gaming — is installed through it, so there is one queue, one place
// that knows how to drive PortMaster's installer, and one fallback.

import (
	"os"
	"path/filepath"
)

const (
	portsStoreSceneName = "PortsStore"
	portsStoreEntryName = "Ports Store"
	portsStoreGlyph     = "store"

	cloudGamingSceneName = "CloudGaming"
	cloudGamingEntryName = "Cloud Gaming"
	cloudGamingGlyph     = "cloud"
)

// storeScenesEnsure gives both screens their scenes in memory. Startup and
// every hot reload.
func storeScenesEnsure(config *Config) {
	portsStoreEnsureScene(config)
	cloudGamingEnsureScene(config)
	// Get Homebrew (homebrew_store*.go) shares the hooks.
	homebrewEnsureScene(config)
}

// storeScenesClick routes a click or tap on either screen; a miss is left for
// the bars.
func storeScenesClick(mx, my int32, config *Config) bool {
	return portsStoreSceneClick(mx, my, config) || cloudGamingSceneClick(mx, my, config) ||
		homebrewSceneClick(mx, my, config)
}

// storeScenesWheel scrolls whichever of the two is on screen.
func storeScenesWheel(config *Config, y int32) bool {
	return portsStoreSceneWheel(config, y) || cloudGamingSceneWheel(config, y) ||
		homebrewSceneWheel(config, y)
}

// ── The shared port installer ────────────────────────────────────────────

// portsInstallRequest asks portsInstallZip to install one PortMaster zip that
// is already downloaded and checked (its MD5 against the catalogue, or its
// shape for a zip that is not in the catalogue).
type portsInstallRequest struct {
	// Name is what toasts and the status strip call the port.
	Name string
	// Zip is the absolute path of the zip. The installer owns it from here:
	// it is moved into PortMaster's autoinstall folder or deleted once
	// installed. Download it into portsStagingDir() so the move is a rename
	// on the same card.
	Zip string
	// OpenPortMaster lets the fallback start PortMaster's own app to finish
	// the install. When false (or while a game holds the screen) the zip is
	// left queued and PortMaster installs it the next time it opens; a caller
	// that wants to decide itself passes false and acts in Done.
	OpenPortMaster bool
	// Done, when set, is called once on the main loop with the outcome.
	Done func(portsInstallResult)
}

// portsInstallZip (ports_store_install.go) is called on the MAIN LOOP only:
// it snapshots the installer setting and the firmware environment from the
// live config there, so the worker never reads the config's maps. It only
// queues the job and returns at once.

// portsInstallResult is how an install ended.
type portsInstallResult struct {
	// Installed: the port's launcher is on the card now.
	Installed bool
	// Queued: the zip waits in PortMaster's autoinstall folder (PortMaster was
	// opened for it when OpenPortMaster allowed).
	Queued bool
	// Err is why neither happened.
	Err error
}

// portsStagingDir is where downloads wait before portsInstallZip takes them:
// a folder in JukaHub's data folder, created on demand. On the handheld it
// is on the SD card, like PortMaster's autoinstall folder.
func portsStagingDir() string {
	dir := filepath.Join("data", "ports-store", "incoming")
	if base, err := P().DataDir(); err == nil && base != "" {
		dir = filepath.Join(base, "ports-store", "incoming")
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}
