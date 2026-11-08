//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

const (
	// auditCreateNoWindow keeps a command-line helper (winget, a version
	// probe, a toolbox run) from flashing a console over the app.
	auditCreateNoWindow = 0x08000000
	// auditDetachedProcess and auditNewProcessGroup start a GUI tool on its
	// own: no console, outside JukaHub's console group, still running after
	// JukaHub exits.
	auditDetachedProcess = 0x00000008
	auditNewProcessGroup = 0x00000200
)

// auditConfigureHidden runs cmd without a console window.
func auditConfigureHidden(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= auditCreateNoWindow
}

// auditConfigureDetached starts cmd as an independent desktop program.
func auditConfigureDetached(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= auditDetachedProcess | auditNewProcessGroup
}
