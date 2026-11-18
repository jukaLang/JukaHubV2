//go:build windows

package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// feToolProcAttr keeps an unpacking program's console window hidden.
func feToolProcAttr(cmd *exec.Cmd) {
	emuHideConsole(cmd)
}

// fe7ZipCommandLine writes a 7-Zip command's line the way 7-Zip reads it. Go
// writes the Windows command line for the C runtime's rules (a backslash
// before a quote escapes it), but 7-Zip ignores those: it splits
// GetCommandLineW() itself (CommandLineParser.cpp, SplitCommandLine), where a
// blank or tab outside quotes ends a word, every " switches quoting on or off
// and is dropped, and a backslash is an ordinary character. A password with a
// " or a trailing \ written Go's way reached 7-Zip altered, or split into
// words it echoed in "Unsupported switch". Here every word goes in as "word",
// which that splitter reads back exactly for any word without a " — and a
// word with one cannot be written at all, so the answer is false and nothing
// may run (feExtractWithTool refuses such a password first). It keeps the
// HideWindow settings feToolProcAttr made; call it after that.
func fe7ZipCommandLine(cmd *exec.Cmd) bool {
	var b strings.Builder
	for i, word := range cmd.Args {
		if strings.Contains(word, `"`) {
			return false
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('"')
		b.WriteString(word)
		b.WriteByte('"')
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = b.String()
	return true
}

// feRenameNoReplace moves from to to only if nothing has the name to (MoveFileEx
// without MOVEFILE_REPLACE_EXISTING), tried again a few times when the virus
// scanner or the indexer still holds a file that was just written.
func feRenameNoReplace(from, to string) error {
	src, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	dst, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	err = windows.MoveFileEx(src, dst, 0)
	for _, d := range pkgRenameDelays {
		if err == nil || !pkgRenameRetryable(err) {
			break
		}
		time.Sleep(d)
		err = windows.MoveFileEx(src, dst, 0)
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}
