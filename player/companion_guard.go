package main

// companion_guard.go — the Files card asks before it renames or deletes a
// folder or file JukaHub depends on.
//
// A console's folder in Roms, PortMaster's folder, JukaHub's own app folder,
// the BIOS, Saves and Overlays folders and JukaHub's own data files are what
// the library, the emulators and the app itself are found by: renamed from a
// phone by a mistap, the games of a whole console "disappear". The server
// enforces it: a rename or delete of such an item without "confirm": true in
// the request is answered 409 with
//
//	{"needsConfirm": true, "reason": "JukaHub keeps your Game Boy Advance games here", "ask": "Rename it anyway?"}
//
// and the page shows the reason in a confirm dialog, then sends the request
// again with "confirm": true. Nothing here makes an item writable: the Files
// card's read-only rules (companionFilesWritable) still run first.
//
// Paths are compared as the card lays them out ("Roms/GBA", "Apps/JukaHub"):
// a card share's name is its first part, and the computer's single share
// ("Shared") stands for the card's top. Names compare as the card's FAT and
// exFAT tell them apart (any case, trailing dots and spaces dropped), and a
// FAT short name ("PORTMA~1") is caught by asking whether it opens the same
// folder as the real name.

import (
	"encoding/json"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// companionGuardRule is one important place, card-relative. "*" in segs is
// any one folder (not a file, not a hidden name). inside makes everything
// under the place count as well; files then get companionGuardInsideReason.
type companionGuardRule struct {
	segs   []string
	reason string
	inside bool
}

// companionGuardRules are the places the Files card asks about. Roms/* gets
// its reason from the console the folder is named after.
var companionGuardRules = []companionGuardRule{
	{segs: []string{"Roms"}, reason: "JukaHub keeps all your games here"},
	{segs: []string{"Roms", "*"}},
	{segs: []string{"BIOS"}, reason: "Emulators read their BIOS files from this folder"},
	{segs: []string{"Saves"}, reason: "JukaHub keeps your game saves here"},
	{segs: []string{"Overlays"}, reason: "JukaHub keeps your game overlays here"},
	{segs: []string{"PortMaster"}, reason: "PortMaster and its ports live here"},
	{segs: []string{"Apps", "PortMaster"}, reason: "PortMaster and its ports live here"},
	{segs: []string{"Roms", "Ports", "PortMaster"}, reason: "PortMaster and its ports live here"},
	{segs: []string{"Apps", "JukaHub"}, reason: "JukaHub itself is installed here", inside: true},
	{segs: []string{ftpDownloadsShareName, "library.json"}, reason: "JukaHub keeps the list of your downloads in this file"},
}

// companionGuardInsideReason words an item inside JukaHub's own folder: its
// settings and data files by name, anything else as part of the app.
func companionGuardInsideReason(rest []string) string {
	last := ""
	if len(rest) > 0 {
		last = strings.ToLower(companionGuardFold(rest[len(rest)-1]))
	}
	switch {
	case last == "jukauser.json" || strings.HasPrefix(last, "jukauser.json."):
		return "JukaHub keeps your settings, play time and favourites in this file"
	case last == "jukaconfig.json":
		return "This is JukaHub's main configuration file"
	case last == libIndexFileName:
		return "JukaHub keeps its game library index in this file"
	case len(rest) > 0 && strings.EqualFold(companionGuardFold(rest[0]), "data"):
		return "JukaHub keeps its library, play history and downloads here"
	}
	return "This is part of JukaHub itself"
}

// companionGuardFold drops the trailing dots and spaces FAT and exFAT ignore.
func companionGuardFold(name string) string {
	return strings.TrimRight(name, ". ")
}

// companionGuardSegs is p as card-relative names: the share's name first,
// except for the computer's single share, which stands for the card's top.
func companionGuardSegs(p ftpPath) []string {
	if p.share == nil {
		return nil
	}
	var segs []string
	if !strings.EqualFold(p.share.name, ftpDesktopShareName) {
		segs = append(segs, p.share.name)
	}
	if p.rel != "." && p.rel != "" {
		segs = append(segs, strings.Split(filepath.ToSlash(p.rel), "/")...)
	}
	return segs
}

// companionGuardReasonFor is the reason the card-relative path segs needs a
// confirm, "" when it does not. isDir says whether it is a folder; same, when
// not nil, answers whether the last name opens the same item as want (a FAT
// short name).
func companionGuardReasonFor(segs []string, isDir bool, same func(want string) bool) string {
	if len(segs) == 0 {
		return ""
	}
	for _, rule := range companionGuardRules {
		n := len(rule.segs)
		switch {
		case len(segs) == n && companionGuardMatch(segs, rule.segs, isDir, same):
			if rule.reason == "" {
				return companionGuardConsoleReason(segs[n-1])
			}
			return rule.reason
		case rule.inside && len(segs) > n && companionGuardMatch(segs[:n], rule.segs, true, nil):
			return companionGuardInsideReason(segs[n:])
		case len(segs) < n && isDir && !strings.Contains(strings.Join(rule.segs, "/"), "*") &&
			companionGuardMatch(segs, rule.segs[:len(segs)], true, same):
			// A folder above an important one: renaming it moves that too.
			return "This folder holds " + strings.Join(rule.segs[len(segs):], "/") + ", which JukaHub needs"
		}
	}
	return ""
}

// companionGuardMatch compares names part by part; "*" takes any folder
// whose name does not start with a dot. The last literal part may also match
// through same (a FAT short name).
func companionGuardMatch(segs, want []string, isDir bool, same func(want string) bool) bool {
	if len(segs) != len(want) {
		return false
	}
	for i, w := range want {
		got := companionGuardFold(segs[i])
		last := i == len(want)-1
		if w == "*" {
			if !last || !isDir || got == "" || strings.HasPrefix(got, ".") {
				return false
			}
			continue
		}
		if strings.EqualFold(got, w) {
			continue
		}
		if last && same != nil && same(w) {
			continue
		}
		return false
	}
	return true
}

// companionGuardConsoleReason words a folder in Roms.
func companionGuardConsoleReason(folder string) string {
	if console := companionDropDirConsoleNamed(companionGuardFold(folder)); console != "" {
		return "JukaHub keeps your " + console + " games here"
	}
	return "JukaHub looks for games in this folder"
}

// companionGuardReason is the reason p needs a confirm, "" when it does not.
// info is p's Lstat.
func companionGuardReason(p ftpPath, info os.FileInfo) string {
	segs := companionGuardSegs(p)
	if len(segs) == 0 || info == nil {
		return ""
	}
	same := func(want string) bool {
		if p.share == nil || p.share.root == nil || p.rel == "." {
			return false
		}
		other, err := p.share.root.Lstat(filepath.Join(filepath.Dir(p.rel), want))
		return err == nil && os.SameFile(info, other)
	}
	return companionGuardReasonFor(segs, info.IsDir(), same)
}

// companionFilesConfirmed reports whether the rename or delete of p may go
// ahead; when it may not, it has answered 409 with the reason for the page's
// confirm dialog. action is "rename" or "delete".
func companionFilesConfirmed(w http.ResponseWriter, p ftpPath, info os.FileInfo, confirmed bool, action string) bool {
	reason := companionGuardReason(p, info)
	if reason == "" {
		return true
	}
	if confirmed {
		logInfo("companion", "files: %s of %s confirmed on the phone (%s)", action, p.display(), reason)
		return true
	}
	ask := "Rename it anyway?"
	if action == "delete" {
		ask = "Delete it anyway?"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"needsConfirm": true,
		"reason":       reason,
		"ask":          ask,
		"name":         path.Base(p.virtual),
	})
	return false
}
