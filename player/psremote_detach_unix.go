//go:build linux || darwin || freebsd || netbsd || openbsd

package main

import (
	"os/exec"
	"syscall"
)

// configureChiakiDetached puts the handoff script in its own session so it
// keeps running after JukaHub exits and is not signalled with JukaHub's
// terminal or process group.
func configureChiakiDetached(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
