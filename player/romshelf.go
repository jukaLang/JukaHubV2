package main

// romshelf.go — consoles whose games are folders rather than single files.
//
// The scan (scanROMLibrary) lists a file as a game by its extension: a .gba in
// Roms/GBA is one game. A DOS game is a folder of a hundred files with one
// program that starts it; a ScummVM adventure is a folder of data files the
// engine recognises; an RPG Maker 2000 game is a folder around RPG_RT.ldb.
// Listing every .exe or data file would fill the shelf with installers and
// resource files, and titling the game after DOOM.EXE instead of its folder
// would miss its cover.
//
// So a feature that knows such a console registers two small rules here, from
// init():
//
//   - romShelfRegister(system, rule): which files of that console's folder are
//     games (rule answers for every file the scan meets under the console's
//     folder, with that folder as consoleDir). The extension tables are not
//     consulted for a console with a rule.
//   - romShelfRegisterStem(rule): the title a listed file goes by — its game
//     folder's name for a folder game. romGameStem asks every stem rule first;
//     a rule must answer from the path alone (no file system access), since
//     titles and cover lookups call romGameStem in loops.
//
// The rules run on the scan's goroutine, so a rule that needs to look inside a
// folder keeps what it learned in its own guarded cache.

import "sync"

// romShelfRule reports whether path (with lower-case extension ext), found
// under consoleDir, is listed as a game.
type romShelfRule func(consoleDir, path, ext string) bool

// romShelfStemRule is the title stem of path, ok false when the rule does not
// know the file.
type romShelfStemRule func(path string) (stem string, ok bool)

var romShelf struct {
	mu    sync.RWMutex
	rules map[string]romShelfRule
	stems []romShelfStemRule
}

// romShelfRegister sets the listing rule of a library system ("DOS").
func romShelfRegister(system string, rule romShelfRule) {
	if system == "" || rule == nil {
		return
	}
	romShelf.mu.Lock()
	defer romShelf.mu.Unlock()
	if romShelf.rules == nil {
		romShelf.rules = map[string]romShelfRule{}
	}
	romShelf.rules[system] = rule
}

// romShelfRegisterStem adds a title rule.
func romShelfRegisterStem(rule romShelfStemRule) {
	if rule == nil {
		return
	}
	romShelf.mu.Lock()
	defer romShelf.mu.Unlock()
	romShelf.stems = append(romShelf.stems, rule)
}

// romShelfListsFile asks system's rule about path; handled is false when the
// system has none (the extension tables decide).
func romShelfListsFile(system, consoleDir, path, ext string) (list, handled bool) {
	romShelf.mu.RLock()
	rule := romShelf.rules[system]
	romShelf.mu.RUnlock()
	if rule == nil {
		return false, false
	}
	return rule(consoleDir, path, ext), true
}

// romShelfHas reports whether system has a listing rule. NextUI's tagged
// folders list every file (nextuiListsFile); a folder-game console's rule
// still decides there, or every file of every game would be a game.
func romShelfHas(system string) bool {
	romShelf.mu.RLock()
	defer romShelf.mu.RUnlock()
	return romShelf.rules[system] != nil
}

// romShelfStem asks the title rules about path.
func romShelfStem(path string) (string, bool) {
	romShelf.mu.RLock()
	stems := romShelf.stems
	romShelf.mu.RUnlock()
	for _, rule := range stems {
		if stem, ok := rule(path); ok && stem != "" {
			return stem, true
		}
	}
	return "", false
}
