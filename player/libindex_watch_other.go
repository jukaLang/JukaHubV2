//go:build !linux

package main

// libindex_watch_other.go — the library index off Linux (LIB-1): no inotify
// here, so the folders' times are checked every minute instead (libindex.go),
// and the CRC pass keeps the usual thread priority.

// libIndexWatch reports that no folder is watched.
func libIndexWatch(dirs []string) bool {
	return false
}

// libIndexLowPriority does nothing off Linux: the pass's own pauses pace it.
func libIndexLowPriority() {}
