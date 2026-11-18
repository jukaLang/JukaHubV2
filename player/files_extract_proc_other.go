//go:build !linux && !windows

package main

import "os/exec"

// feToolProcAttr has nothing to set here.
func feToolProcAttr(cmd *exec.Cmd) {
	emuHideConsole(cmd)
}

// fe7ZipCommandLine has nothing to do here: 7-Zip gets its words as argv.
func fe7ZipCommandLine(*exec.Cmd) bool {
	return true
}

// feRenameNoReplace moves from to to unless something has the name to.
func feRenameNoReplace(from, to string) error {
	return feRenameByReserving(from, to)
}
