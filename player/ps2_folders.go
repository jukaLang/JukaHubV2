package main

// ps2_folders.go — the card's PlayStation 2 folder, and the PlayStation 2
// tile on the Emulators page.
//
// Juka, 2026-11-26: "Add PS2 support for Trimui Smart devices … make sure it
// works and launches". PS2 games played in ARMSX2 since 2026-11-20 (ps2.go),
// but only from a Roms/PS2 folder nothing made: TrimUI's firmware has none,
// so a fresh card showed no PlayStation 2 anywhere — no folder to copy games
// into, no tile to set the emulator up from. The same gap GameCube and Wii
// had (dolphin_folders.go), closed the same way, while PS2 plays here
// (ps2FetchSupported: the 64-bit TrimUI handheld with Settings → PlayStation 2
// on — every model, the Smart Pro, Smart Pro S, Brick, Hammer and Brick Pro):
//
//   - The card gets Roms/PS2 once, after the first frame of a start, after an
//     ARMSX2 setup, and whenever PlayStation 2 is turned on or its tile is
//     pressed. One the player deleted after it was made stays deleted until
//     one of those two asks (armsx2.json's folder). A folder the card already
//     has for the console in any spelling the library reads (PS2,
//     PlayStation 2, a NextUI tag) is used instead; nothing is made on a
//     NextUI card or on one with no Roms folder.
//   - The Emulators page always lists a PlayStation 2 tile, as it does the
//     Xbox and GameCube: ARMSX2, where the games and the BIOS go, how many
//     games there are. A opens them in the library, or sets ARMSX2 up while
//     it is not in; X sets it up or looks for a newer build (ps2Tile). The
//     folder's own row ("PS2 — no emulator found") is not listed beside it,
//     and an Emus launcher folder for the console (a standalone emulator
//     someone put on the card) keeps its own tile instead.
//
// No Emus/PS2 is written: games start in the ARMSX2 core directly (ps2.go),
// with the setup, the BIOS copy and JukaHub's glibc that path brings.

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// ps2FolderName is the Roms folder made when the card has none for the
	// console: the name the ROM manager files PS2 games under (romm_detect.go).
	ps2FolderName = "PS2"
	// ps2Formats is what a hint asks for (romSystemExtensions has all).
	ps2Formats = ".iso, .chd or .cso"
)

// ps2RomsFolder is the card's folder for PS2 games in romsRoot: the first
// one, by name, that the library files under the console (romFolderSystem);
// "" when there is none.
func ps2RomsFolder(romsRoot string) string {
	entries, err := os.ReadDir(romsRoot)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() || skipScanEntryName(e.Name()) {
			continue
		}
		if ps2ConsoleFolderName(e.Name()) {
			return filepath.Join(romsRoot, e.Name())
		}
	}
	return ""
}

// ps2FolderShown is how a toast or a tile names the games' folder: dir's
// own name, or the one ps2EnsureRomsFolder makes.
func ps2FolderShown(dir string) string {
	if dir != "" {
		return "Roms/" + filepath.Base(dir)
	}
	return "Roms/" + ps2FolderName
}

// ps2BIOSFolderShown is the folder a hint sends the BIOS to: PS2 in the
// card's own BIOS folder, which ps2FindBIOS looks in, spelt as the card
// spells it.
func ps2BIOSFolderShown() string {
	card := filepath.Dir(deviceRomsRoot)
	for _, name := range []string{"Bios", "BIOS", "bios"} {
		if dirExists(filepath.Join(card, name)) {
			return name + "/PS2"
		}
	}
	return "Bios/PS2"
}

// ps2EnsureRomsFolder makes the card's PS2 folder where it has none and
// returns the folder there is ("" when none is, or none may be made). asked
// (PlayStation 2 turned on, a press on its tile) makes it whenever it is
// missing; otherwise only the first time: a folder that was there or made
// once, and is gone now, was deleted by the player and stays gone. Any
// thread: a directory listing and at most one mkdir.
func ps2EnsureRomsFolder(asked bool) string {
	if !ps2FetchSupported() || nextuiActive() {
		return ""
	}
	root := deviceRomsRoot
	if !dirExists(root) {
		return ""
	}
	seen := ps2RecordGet().Folder
	if dir := ps2RomsFolder(root); dir != "" {
		if !seen {
			ps2RecordUpdate(func(r *ps2Record) { r.Folder = true })
		}
		return dir
	}
	if seen && !asked {
		return "" // deleted since: only an ask brings it back
	}
	dir := filepath.Join(root, ps2FolderName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logWarn("library", "PS2: cannot make %s: %v", dir, err)
		return ""
	}
	if !seen {
		ps2RecordUpdate(func(r *ps2Record) { r.Folder = true })
	}
	logInfo("library", "PS2: made %s for PlayStation 2 games", dir)
	return dir
}

func init() {
	// After the first frame, off the main thread: bootEnqueueChecks synced
	// the setting first. RetroArch's build is read then too, so a game's
	// start does not read the whole binary (ps2_video.go).
	deferStartupTask("ps2-folder", func() {
		go func() {
			ps2EnsureRomsFolder(false)
			ps2RAVulkanWarm()
		}()
	})
}

// ── The Emulators page ───────────────────────────────────────────────────

// ps2ConsoleTilesShown reports whether the catalog lists the PlayStation 2
// tile: on the 64-bit handheld while PlayStation 2 is on.
func ps2ConsoleTilesShown() bool {
	return ps2FetchSupported()
}

// ps2ConsoleFolderName reports whether a Roms folder called name holds PS2
// games.
func ps2ConsoleFolderName(name string) bool {
	sys, _, ok := romFolderSystem(name, nil)
	return ok && ps2IsSystem(sys.Name)
}

// ps2ConsoleTile reports whether a tile is the one ps2ConsoleEntries lists.
func ps2ConsoleTile(e emuEntry) bool {
	if IsWindows() || e.Pinned || e.Folder != "" || e.CorePath != "" || strings.TrimSpace(e.Launch) != "" {
		return false
	}
	return e.Name == ps2SystemName && normalizeCoreName(e.Core) == ps2CoreName
}

// ps2ConsoleEntries is the PlayStation 2 tile for the catalog being built
// (see the file comment); none while ps2ConsoleTilesShown is false or an Emus
// launcher folder for the console speaks for it.
func ps2ConsoleEntries(romsRoot string, byName map[string]emuEntry) []emuEntry {
	if !ps2ConsoleTilesShown() || dolphinEmusFolderFor(byName, ps2SystemName) {
		return nil
	}
	dir := ps2RomsFolder(romsRoot)
	where := ps2FolderShown(dir)
	// Core names ARMSX2 for ps2Tile (X, the update badge); no Launch and no
	// CorePath, so the game pickers never take the tile for a launcher.
	e := emuEntry{
		Name:  ps2SystemName,
		Core:  ps2CoreName,
		Glyph: emuGlyphFor(ps2SystemName, ps2CoreName),
	}
	core := ps2FindCore()
	if core == "" {
		e.Status = emuMissing
		e.Detail = "ARMSX2 · A sets it up (once) · games go in " + where + ", your BIOS in " + ps2BIOSFolderShown()
		return []emuEntry{e}
	}
	e.Status = emuAvailable
	need := ps2Inspect(core, "")
	n := dolphinCountGames(ps2SystemName, dir)
	switch {
	case need.Why != "":
		e.Detail = "ARMSX2 · " + need.Why
	case need.BIOS == "" && need.BIOSFound == "":
		e.Detail = "ARMSX2 · needs your PS2 BIOS: copy it to " + ps2BIOSFolderShown()
	case n > 0:
		e.Detail = "ARMSX2 · " + pluralizeCount(n, "game", "games") + " in " + where
	default:
		e.Detail = "ARMSX2 · no games yet: put " + ps2Formats + " files in " + where
	}
	return []emuEntry{e}
}

// ps2ConsoleActivate is A on the PlayStation 2 tile: the folder made when it
// is missing, then the console's games in the library, or ARMSX2's setup
// while the core is not in. Main thread.
func ps2ConsoleActivate(e emuEntry) {
	dir := ps2EnsureRomsFolder(true)
	if e.Status != emuAvailable {
		ps2StartInstall()
		return
	}
	empty := "No PlayStation 2 games yet — put " + ps2Formats + " files in " + ps2FolderShown(dir) + ", then press Y to rescan"
	if need := ps2Inspect("", ""); need.Why == "" && need.BIOS == "" && need.BIOSFound == "" {
		empty += " · your PS2 BIOS goes in " + ps2BIOSFolderShown()
	}
	openGameLibraryOnSystem(ps2SystemName, empty)
}
