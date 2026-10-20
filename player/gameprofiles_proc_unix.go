//go:build linux || darwin || freebsd || netbsd || openbsd

package main

import (
	"os/exec"
	"syscall"
)

// configureGameChild isolates the game from JukaHub's terminal signals. The
// process is still reaped normally after it exits.
func configureGameChild(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}
