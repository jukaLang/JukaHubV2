//go:build !windows

package main

// files_drives_other.go — the Linux (handheld and desktop) half of the Files
// Locations helpers. There are no drive letters: the filesystem root is one of
// the locations, and volumes mounted for removable media are listed by
// files_locations.go from /media and /run/media.

import (
	"os"
	"strings"
)

// filesDrives has nothing to list outside Windows.
func filesDrives() []filesLocation { return nil }

// filesEntryHidden reports whether a listing should hide an entry while "Show
// hidden files" is off. On Linux that is the dotfile convention, read from the
// name alone so a folder of thousands of entries costs no extra lstat.
func filesEntryHidden(e os.DirEntry) bool {
	return strings.HasPrefix(e.Name(), ".")
}

// filesFreeSpace is the space left on the volume holding path.
func filesFreeSpace(path string) (int64, error) {
	return freeBytes(path)
}

// filesSkipFreeSpace reports whether a location's free space should not be
// asked for. Local mounts answer statfs at once.
func filesSkipFreeSpace(loc filesLocation) bool {
	return false
}
