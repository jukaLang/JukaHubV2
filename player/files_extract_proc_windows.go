//go:build windows

package main

import (
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/windows"
)

// feToolProcAttr keeps an unpacking program's console window hidden.
func feToolProcAttr(cmd *exec.Cmd) {
	emuHideConsole(cmd)
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
