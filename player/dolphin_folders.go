package main

// dolphin_folders.go — the card's GameCube and Wii folders, and their tiles on
// the Emulators page.
//
// Juka, 2026-11-21: "I can't find GameCube or Wii folders that using dolphin
// emulator and subfolder for both Wii and GameCube inside ROMS file." Nothing
// made them. TrimUI's firmware has no GameCube or Wii folder, the guides said
// to put games in Roms/GC and Roms/WII, and the Emulators page showed a
// console's folder only when the card already had it and no core covered it:
// once the Dolphin core was in, its "dolphin" row was all that was left. So,
// while Dolphin plays GameCube and Wii here (dolphinFetchSupported):
//
//   - The card gets Roms/GC and Roms/WII. After the first frame of every start
//     and after a Dolphin setup a folder is made once per card: one the player
//     deleted later stays deleted (dolphin.json's folders). A press on its
//     tile, and turning GameCube & Wii on, make it whenever it is missing. A
//     folder the card already has for the console, in any spelling the
//     library reads (GAMECUBE, NGC, Wii, a NextUI tag), is used instead.
//     Nothing is made on a NextUI card (its menu lists only its own tagged
//     folders) or on a card with no Roms folder at all.
//   - The Emulators page always lists a GameCube and a Wii tile, as it does the
//     Xbox: Dolphin, where the games go and how many are there. A opens the
//     console's games in the library, or sets Dolphin up while it is not in;
//     X sets it up or looks for a newer build (dolphinTile, dolphinStartInstall).
//     The folder's own row ("GC — no emulator found") is not listed beside it.
//     An Emus launcher folder of the console's own (a standalone Dolphin
//     someone put on the card) keeps its tile, and games start in it as before.
//
// Games still start in the Dolphin core directly (dolphin.go): no Emus folder
// is written, so the stock TrimUI menu does not list the two consoles.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// dolphinConsole is one of Dolphin's consoles on the card.
type dolphinConsole struct {
	System  string // the library's name for it
	Folder  string // the Roms folder made when the card has none for it
	Formats string // the files a hint asks for (romSystemExtensions has all)
}

// dolphinConsoles are GameCube and Wii, in the order the toasts name them.
// The folder names are the ones the ROM manager files them under
// (romm_detect.go).
var dolphinConsoles = []dolphinConsole{
	{System: "GameCube", Folder: "GC", Formats: ".iso or .rvz"},
	{System: "Wii", Folder: "WII", Formats: ".wbfs, .iso or .rvz"},
}

// dolphinCountMax bounds how many entries a tile's game count looks at.
const dolphinCountMax = 4000

// dolphinRomsFolders maps each Dolphin console to its folder in romsRoot: the
// first one, by name, that the library files under it (romFolderSystem). A
// console with none is absent.
func dolphinRomsFolders(romsRoot string) map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(romsRoot)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() || skipScanEntryName(e.Name()) {
			continue
		}
		sys, _, ok := romFolderSystem(e.Name(), nil)
		if !ok || !dolphinIsSystem(sys.Name) {
			continue
		}
		if _, have := out[sys.Name]; !have {
			out[sys.Name] = filepath.Join(romsRoot, e.Name())
		}
	}
	return out
}

// dolphinFolderShown is how a toast or a tile names a console's folder:
// "Roms/<its name>", the folder dolphinEnsureRomsFolders makes when the card
// has none.
func dolphinFolderShown(folders map[string]string, c dolphinConsole) string {
	if dir := folders[c.System]; dir != "" {
		return "Roms/" + filepath.Base(dir)
	}
	return "Roms/" + c.Folder
}

// dolphinFoldersLabel names both folders for a toast: "Roms/GC and Roms/WII".
func dolphinFoldersLabel() string {
	folders := dolphinRomsFolders(deviceRomsRoot)
	names := make([]string, 0, len(dolphinConsoles))
	for _, c := range dolphinConsoles {
		names = append(names, dolphinFolderShown(folders, c))
	}
	return strings.Join(names, " and ")
}

// dolphinEnsureRomsFolders makes the card's GameCube and Wii folders where it
// has none, and returns the ones it made. A console named in asked (a press on
// its tile, GameCube & Wii turned on) gets its folder whenever it is missing;
// the others only the first time: a folder that was there or made once, and
// is gone now, was deleted by the player and is left alone (start-up, a
// finished Dolphin setup). Any thread: a directory listing and at most two
// mkdirs.
func dolphinEnsureRomsFolders(asked ...string) []string {
	if !dolphinFetchSupported() || nextuiActive() {
		return nil
	}
	root := deviceRomsRoot
	if !dirExists(root) {
		return nil
	}
	folders := dolphinRomsFolders(root)
	rec := dolphinRecordGet()
	handled := slices.Clone(rec.Folders)
	var made []string
	for _, c := range dolphinConsoles {
		seen := slices.Contains(handled, c.System)
		if folders[c.System] == "" && (!seen || slices.Contains(asked, c.System)) {
			dir := filepath.Join(root, c.Folder)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				logWarn("library", "GameCube: cannot make %s: %v", dir, err)
				continue
			}
			made = append(made, dir)
		} else if folders[c.System] == "" {
			continue // deleted since: only an ask brings it back
		}
		if !seen {
			handled = append(handled, c.System)
		}
	}
	if len(handled) != len(rec.Folders) {
		dolphinRecordUpdate(func(r *dolphinRecord) { r.Folders = handled })
	}
	if len(made) > 0 {
		logInfo("library", "GameCube: made %s for Dolphin's GameCube and Wii games", strings.Join(made, " and "))
	}
	return made
}

func init() {
	// After the first frame, off the main thread: bootEnqueueChecks synced the
	// setting first.
	deferStartupTask("gamecube-folders", func() { go dolphinEnsureRomsFolders() })
}

// ── The Emulators page ───────────────────────────────────────────────────

// dolphinConsoleTilesShown reports whether the catalog lists the GameCube and
// Wii tiles: on the 64-bit handheld while GameCube & Wii is not Off.
func dolphinConsoleTilesShown() bool {
	return dolphinFetchSupported()
}

// dolphinConsoleFolderName reports whether a Roms folder called name holds
// GameCube or Wii games.
func dolphinConsoleFolderName(name string) bool {
	sys, _, ok := romFolderSystem(name, nil)
	return ok && dolphinIsSystem(sys.Name)
}

// dolphinConsoleTile reports whether a tile is the GameCube or the Wii tile
// dolphinConsoleEntries lists.
func dolphinConsoleTile(e emuEntry) bool {
	if IsWindows() || e.Pinned || e.Folder != "" || e.CorePath != "" || strings.TrimSpace(e.Launch) != "" ||
		normalizeCoreName(e.Core) != dolphinCoreName {
		return false
	}
	return slices.ContainsFunc(dolphinConsoles, func(c dolphinConsole) bool { return e.Name == c.System })
}

// dolphinEmusFolderFor reports whether the tiles so far (byName, keyed by
// lower-case name) hold an Emus launcher folder for system that can start its
// games: that folder's tile speaks for the console. A folder with no launcher
// (a theme's icons only) does not.
func dolphinEmusFolderFor(byName map[string]emuEntry, system string) bool {
	for _, e := range byName {
		if e.Folder == "" || e.Status != emuAvailable || strings.TrimSpace(e.Launch) == "" {
			continue
		}
		if sys, _, ok := romFolderSystem(filepath.Base(e.Folder), nil); ok && sys.Name == system {
			return true
		}
	}
	return false
}

// dolphinConsoleEntries are the GameCube and Wii tiles for the catalog being
// built (see the file comment); none while dolphinConsoleTilesShown is false.
func dolphinConsoleEntries(romsRoot string, byName map[string]emuEntry) []emuEntry {
	if !dolphinConsoleTilesShown() {
		return nil
	}
	core := dolphinFindCore()
	folders := dolphinRomsFolders(romsRoot)
	out := make([]emuEntry, 0, len(dolphinConsoles))
	for _, c := range dolphinConsoles {
		if dolphinEmusFolderFor(byName, c.System) {
			continue
		}
		where := dolphinFolderShown(folders, c)
		e := emuEntry{
			Name:  c.System,
			Core:  dolphinCoreName,
			Glyph: emuGlyphFor(c.System, dolphinCoreName),
		}
		if core == "" {
			e.Status = emuMissing
			e.Detail = "Dolphin · A sets it up (once, a few dozen MB) · games go in " + where
			out = append(out, e)
			continue
		}
		e.Status = emuAvailable
		if n := dolphinCountGames(c.System, folders[c.System]); n > 0 {
			e.Detail = "Dolphin · " + pluralizeCount(n, "game", "games") + " in " + where
		} else {
			e.Detail = "Dolphin · no games yet: put " + c.Formats + " files in " + where
		}
		out = append(out, e)
	}
	return out
}

// dolphinCountGames counts system's games in dir as the library lists them:
// the files its scan takes (romListsFile), a multi-disc set's .m3u as one game.
// At most dolphinCountMax entries are looked at.
func dolphinCountGames(system, dir string) int {
	if dir == "" {
		return 0
	}
	var games []scannedROM
	looked := 0
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if looked++; looked > dolphinCountMax {
			return filepath.SkipAll
		}
		if skipScanEntryName(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if romListsFile(system, dir, p, strings.ToLower(filepath.Ext(d.Name()))) {
			games = append(games, scannedROM{Path: filepath.Clean(p), System: system})
		}
		return nil
	})
	_, counts := dropSheetParts(games)
	return counts[system]
}

// dolphinConsoleActivate is A on the GameCube or Wii tile: the console's
// folder made when it is missing, then its games in the library, or Dolphin's
// setup while the core is not in. Main thread.
func dolphinConsoleActivate(e emuEntry) {
	dolphinEnsureRomsFolders(e.Name)
	if e.Status != emuAvailable {
		dolphinStartInstall()
		return
	}
	i := slices.IndexFunc(dolphinConsoles, func(c dolphinConsole) bool { return c.System == e.Name })
	if i < 0 {
		return
	}
	c := dolphinConsoles[i]
	where := dolphinFolderShown(dolphinRomsFolders(deviceRomsRoot), c)
	openGameLibraryOnSystem(c.System, "No "+c.System+" games yet — put "+c.Formats+" files in "+where+", then press Y to rescan")
}
