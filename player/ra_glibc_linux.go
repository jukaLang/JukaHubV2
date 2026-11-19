//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

// raGlibcNamespace starts cmd in a mount namespace of its own (ra_glibc.go).
// Go also marks the new namespace's mounts private, so nothing mounted in it
// reaches JukaHub's or the firmware's. Unsharing needs root, which the TrimUI
// handhelds run JukaHub as; without it cmd.Start fails.
func raGlibcNamespace(cmd *exec.Cmd) bool {
	if cmd == nil {
		return false
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Unshareflags |= syscall.CLONE_NEWNS
	return true
}
