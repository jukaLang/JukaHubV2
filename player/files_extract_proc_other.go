//go:build !linux && !windows

package main

import "os/exec"

// feToolProcAttr has nothing to set here.
func feToolProcAttr(cmd *exec.Cmd) {
	emuHideConsole(cmd)
}

// feRenameNoReplace moves from to to unless something has the name to.
func feRenameNoReplace(from, to string) error {
	return feRenameByReserving(from, to)
}
