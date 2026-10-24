package main

// Log export rotation.
//
// The in-app viewer is a 2000-line ring buffer, but exporting it used to
// overwrite logs.txt in place, so a user who exported after a crash lost the
// previous file — which is often the one worth keeping. An export now moves the
// last file to logs.txt.1 (one backup, oldest dropped) and writes the new one
// atomically. The export is also capped, so a pathological line cannot grow the
// file without bound.

import (
	"bytes"
	"os"
)

const (
	// logExportMaxBytes caps one exported log file at 2 MiB. The 2000-line ring
	// usually fits in a few hundred KB; the cap is for pathological lines.
	logExportMaxBytes = 2 << 20
	// logBufferMaxBytes caps the in-memory ring as well as its line count, so a
	// single huge stack trace cannot hold megabytes for the whole session.
	logBufferMaxBytes = 512 << 10
)

// capLogData returns at most maxBytes from the end of data (the newest part),
// starting at a line boundary so an export never begins mid-line. A single line
// longer than maxBytes is kept as its own tail.
func capLogData(data []byte, maxBytes int64) []byte {
	if maxBytes <= 0 || int64(len(data)) <= maxBytes {
		return data
	}
	tail := data[int64(len(data))-maxBytes:]
	if i := bytes.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
		tail = tail[i+1:]
	}
	return tail
}

// writeRotatedLog writes data to path after moving the previous non-empty file
// to path+".1". Only one backup is kept: a second export replaces it, because
// the point is to survive one crash, not to fill the card with log history.
// The write itself goes through AtomicWrite (temp file + rename).
func writeRotatedLog(path string, data []byte, maxBytes int64) error {
	data = capLogData(data, maxBytes)
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		backup := path + ".1"
		if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(path, backup); err != nil {
			return err
		}
	}
	return AtomicWrite(path, data, 0o644)
}
