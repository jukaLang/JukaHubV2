//go:build windows

package main

// emuproc_windows.go — keeps the emulator installer's helper processes (7-Zip)
// from flashing a console window over JukaHub.

import (
	"os/exec"
	"syscall"
)

// emuCreateNoWindow is CREATE_NO_WINDOW: the child gets no console at all.
const emuCreateNoWindow = 0x08000000

// emuHideConsole runs cmd without a console window.
func emuHideConsole(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= emuCreateNoWindow
}
