package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Windows release build has no console (build.bat links -H windowsgui), so
// errors.txt in the log folder is the only record of a warning. It must keep
// the newest session and rotate once, exactly like launch.sh's copy on the
// handheld, instead of growing on the user's disk for ever.
func TestLogFileRotatesAtOneMiB(t *testing.T) {
	dir := t.TempDir()
	line := strings.Repeat("warn ", 200) // 1 KB per record
	const writes = 1100                  // a little over one 1 MiB file
	for i := 0; i < writes; i++ {
		if err := logFileWriteLine(dir, line); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	cur := filepath.Join(dir, logFileName)
	info, err := os.Stat(cur)
	if err != nil {
		t.Fatalf("errors.txt after %d writes: %v", writes, err)
	}
	if info.Size() > logFileMaxBytes {
		t.Errorf("errors.txt is %d bytes, want at most %d", info.Size(), logFileMaxBytes)
	}
	prev, err := os.Stat(cur + ".1")
	if err != nil {
		t.Fatalf("errors.txt.1 missing: %v", err)
	}
	if prev.Size() == 0 || prev.Size() > logFileMaxBytes {
		t.Errorf("errors.txt.1 is %d bytes, want the previous session", prev.Size())
	}
	// The newest line has to be in the live file: that is what a user sends.
	data, err := os.ReadFile(cur)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), line) {
		t.Error("the newest line is not in errors.txt")
	}
}

// A dependency can put a whole response body into one warning record; one line
// must not decide the file's size by itself.
func TestLogFileCapsOneLine(t *testing.T) {
	dir := t.TempDir()
	huge := strings.Repeat("x", logFileMaxLine*3)
	if err := logFileWriteLine(dir, huge); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, logFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > logFileMaxLine+8 {
		t.Errorf("one line wrote %d bytes, want at most %d", info.Size(), logFileMaxLine+8)
	}
}

// A write failure is dropped, not logged: logging it would call the writer
// again from inside itself.
func TestLogFileAppendToleratesMissingFolder(t *testing.T) {
	if err := logFileWriteLine("", "a warning"); err == nil {
		t.Fatal("an empty folder was accepted")
	}
}
