package main

import "strings"

// skipScanEntryName reports the names every library, media and file scan must
// ignore. They are not content:
//
//   - anything starting with "." — hidden folders, .DS_Store, the "._Name"
//     AppleDouble companion a Mac leaves beside every copied file;
//   - "__MACOSX", the folder a zip made on a Mac carries at its root;
//   - the metadata files Windows writes into picture and folder views.
//
// A launcher that lists them shows phantom games and albums that cannot be
// launched, which is what FIX-9 in MODERNIZATION.md was about.
func skipScanEntryName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return true
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch strings.ToLower(name) {
	case "__macosx", "desktop.ini", "thumbs.db":
		return true
	}
	return false
}
