//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

const gameCreateNewProcessGroup = 0x00000200

func configureGameChild(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= gameCreateNewProcessGroup
}
