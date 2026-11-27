package main

// logfile.go — the copy of the log a Windows desktop needs.
//
// A release build is a GUI-subsystem executable (build.bat links it with
// -H windowsgui): it has no console, so the handler's stderr copy of warnings
// and errors goes nowhere and a user whose app misbehaved had nothing to send.
// launch.sh does this job on the handheld by appending the app's stderr to
// errors.txt next to the binary. Here every record at warning level and above
// is appended to errors.txt in the log folder (%LOCALAPPDATA%\JukaHub\logs),
// rotated once at 1 MiB exactly like the launcher's copy, so the newest session
// is always in errors.txt and a crash log from weeks ago cannot push it out.
//
// Only the Windows desktop uses it: the handheld's launcher owns errors.txt
// there, and a Linux desktop run from a terminal already writes stderr.
//
// Nothing here may log: a failure to write the log file is dropped. A logWarn
// on that path would call this function again.

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"sync/atomic"
)

const (
	// logFileName is the file launch.sh also writes on the handheld.
	logFileName = "errors.txt"
	// logFileMaxBytes is the rotation point the launcher uses (1 MiB); the
	// previous file is kept as errors.txt.1, one generation only.
	logFileMaxBytes = 1 << 20
	// logFileMaxLine bounds one line. A dependency echoing a whole response
	// body into a WARN record must not decide the file's size on its own.
	logFileMaxLine = 8 << 10
	// logFileMissLimit stops trying after this many failed writes in a row.
	// The target folder is on the user's own disk; when it cannot be written
	// at all, retrying every warning costs a failed syscall each time.
	logFileMissLimit = 3
)

// logFileFailures counts failed writes in a row; a success clears it.
var logFileFailures atomic.Int32

// logFileEnabled reports whether this build keeps the warning log itself. A
// test binary never does: its warnings are not the user's app's, and writing
// them into the real %LOCALAPPDATA%\JukaHub\logs would leave a test run in the
// file a bug report attaches.
func logFileEnabled() bool {
	if flag.Lookup("test.v") != nil {
		return false
	}
	return IsWindows() && !IsTrimuiDevice()
}

// logFileAppend copies one already-rendered log line into errors.txt.
func logFileAppend(line string) {
	if line == "" || !logFileEnabled() {
		return
	}
	if logFileFailures.Load() >= logFileMissLimit {
		return
	}
	dir, err := crashLogDir()
	if err != nil {
		logFileFailures.Add(1)
		return
	}
	if err := logFileWriteLine(dir, line); err != nil {
		logFileFailures.Add(1)
		return
	}
	logFileFailures.Store(0)
}

// logFileWriteLine appends one line to dir/errors.txt, rotating the file first
// when the line would take it past logFileMaxBytes. It is a function of its
// arguments so the rotation is testable without the Windows check above.
func logFileWriteLine(dir, line string) error {
	if dir == "" {
		return errors.New("no log folder")
	}
	if len(line) > logFileMaxLine {
		line = line[:logFileMaxLine] + "…"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, logFileName)
	if info, err := os.Stat(path); err == nil && info.Size()+int64(len(line))+1 > logFileMaxBytes {
		// Keep exactly one previous generation, as launch.sh does. A file
		// another program holds open (a log viewer on Windows) cannot be
		// renamed; removing it is the next best thing and never grows the
		// file without bound.
		_ = os.Remove(path + ".1")
		if err := os.Rename(path, path+".1"); err != nil {
			_ = os.Remove(path)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(line + "\n")
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// logFilePath is errors.txt in the log folder, or "" when the folder cannot be
// resolved. The UI names this file when it points a user at the logs.
func logFilePath() string {
	dir, err := crashLogDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, logFileName)
}
