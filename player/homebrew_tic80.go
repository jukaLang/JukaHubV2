package main

// homebrew_tic80.go — TIC-80 carts in Get Homebrew (homebrew_store.go), and
// the two things the store's newer consoles share: a Roms folder the ROM
// manager does not know, and games that only play in a browser.
//
// TIC-80 is PICO-8's open-source cousin: a fantasy console whose games are
// one .tic cartridge, played on the handheld by RetroArch's TIC-80 core. On
// itch.io they are listed by the "Made with TIC-80" engine filter (about a
// thousand games) and the tic-80 / tic80 tags. Most of those pages offer only
// the HTML5 build that itch.io plays in the browser; a page with a .tic
// download (alone or in a .zip) installs like any cartridge. A browser-only
// game says "Plays in the browser only" on its page and offers its itch.io
// page instead of Install: nothing is ever scraped out of a web build.
//
// Roms folder: CrossMix, spruce and Onion all call it TIC (Knulli and
// Batocera "tic80", NextUI "TIC-80 (TIC)" with a community pak); every one of
// those is a library alias (romSystemAliases), so a folder the card already
// has is used, and a card with none gets "TIC". The ROM manager files neither
// TIC-80 nor EasyRPG games (rommSystems has no row for them, so RomM has no
// id to match either): hbFolderConsoleDir finds their folder through the
// library's own aliases instead. EasyRPG's folder is EASYRPG, the name Onion
// and the CrossMix-style firmwares use (Batocera's "easyrpg" is the same
// folder on a FAT card).

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	// hbTIC80System is the Games library's name for TIC-80 (romSystemAliases).
	hbTIC80System = "TIC-80"
	// hbTIC80ID and hbEasyRPGID are their hbConsoles ids.
	hbTIC80ID   = "tic80"
	hbEasyRPGID = "easyrpg"
)

// hbFolderConsoles are the store's consoles the ROM manager has no row for,
// with the Roms folder a card that has none for them gets.
var hbFolderConsoles = map[string]string{
	hbTIC80ID:   "TIC",
	hbEasyRPGID: "EASYRPG",
}

// hbFolderConsoleDir is the Roms folder of a console in hbFolderConsoles:
// one the card already has for its system (any alias, a NextUI tagged
// folder), else its folder made in the first Roms folder there is. ok is
// false for every other console.
func hbFolderConsoleDir(con hbConsole) (dir string, ok bool, err error) {
	folder := hbFolderConsoles[con.id]
	if folder == "" {
		return "", false, nil
	}
	roots := romLibraryRoots()
	for _, root := range roots {
		for _, name := range rommListDirs(root) {
			if s, _, found := romFolderSystem(name, nil); found && s.Name == con.name {
				return filepath.Join(root, name), true, nil
			}
		}
	}
	// A Roms folder that is there first (the card's), then any absolute one
	// (a PC's, made on first use).
	for _, mustExist := range []bool{true, false} {
		for _, root := range roots {
			if !filepath.IsAbs(root) || (mustExist && !dirExists(root)) {
				continue
			}
			dir := filepath.Join(root, folder)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", true, err
			}
			return dir, true, nil
		}
	}
	return "", true, errors.New("there is no Roms folder on the card")
}

// hbWebOnly reports a game whose only build is one itch.io plays in the
// browser: a page that also offers a PC or Android download is not "browser
// only" (it is a PC game here). Soundtracks, manuals and the like do not
// count. The caller asks only when nothing else could be installed. Pure.
func hbWebOnly(ups []hbUpload) bool {
	web := false
	for _, u := range ups {
		switch {
		case u.Type == "html":
			web = true
		case u.Preorder || hbSkipUploadTypes[u.Type]:
		default:
			return false
		}
	}
	return web
}

// hbFileNote is the page's line for the file Install takes.
func hbFileNote(u hbUpload) string {
	note := "File: " + u.Filename
	if u.Size > 0 {
		note += " · " + humanBytes(u.Size)
	}
	return note
}

// hbUploadsNote is the game page's line about its files: the one Install
// takes, or why there is none. webOnly marks a game that only plays in a
// browser: its page offers itch.io instead of Install. Pure.
func hbUploadsNote(ups []hbUpload, consoles uint16) (note string, bad, webOnly bool) {
	if u, ok := hbEasyRPGUpload(ups, consoles); ok {
		return hbFileNote(u) + " · unpacked into its own folder", false, false
	}
	if pick, ok := hbPickUpload(ups, consoles); ok {
		return hbFileNote(pick.Upload), false, false
	}
	note, _ = hbNoCartridgeReason(ups, consoles)
	return note, true, hbWebOnly(ups)
}

// hbNoCartridgeReason says why a game's files hold nothing to install: as the
// page's line and as the end of "Could not install X: …". Pure.
func hbNoCartridgeReason(ups []hbUpload, consoles uint16) (note, reason string) {
	switch {
	case hbWebOnly(ups):
		return "Plays in the browser only — open its page on itch.io to play it there",
			"it plays in the browser only — open its page on itch.io to play it there"
	case hbWantsEasyRPG(consoles):
		if why := hbEasyRPGNoZipReason(ups); why != "" {
			return strings.ToUpper(why[:1]) + why[1:], why
		}
	}
	return "No cartridge for these consoles here — it may be a PC or web game",
		"its page has no cartridge for these consoles — it may be a PC or web game"
}
