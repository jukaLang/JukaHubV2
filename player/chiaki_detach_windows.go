//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// chiakiCreateNewProcessGroup keeps the helper out of JukaHub's console group.
// The portable Chiaki-ng package is Linux-only, so this path is not reachable
// in practice; it exists so the package still builds on Windows.
const chiakiCreateNewProcessGroup = 0x00000200

func configureChiakiDetached(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= chiakiCreateNewProcessGroup
}
