//go:build windows

package main

import (
	"context"
	"os/exec"
	"syscall"
)

// hiddenCommand is exec.Command with the console hidden on Windows.
func hiddenCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	hideConsole(cmd)
	return cmd
}

// hiddenCommandContext is exec.CommandContext with the console hidden on Windows.
func hiddenCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	hideConsole(cmd)
	return cmd
}

// hideConsoleFlag is CREATE_NO_WINDOW: the child gets no console at all, so
// a command-line helper (yt-dlp, winget, schtasks, 7-Zip, ...) never flashes
// a window over the app. One constant and one helper replace the per-area
// copies that used to carry the same value under different names.
const hideConsoleFlag = 0x08000000

// hideConsole runs cmd without a console window. It appends to any
// SysProcAttr already set (a below-normal priority class, a detached process
// group) instead of replacing it.
//
// CREATE_NO_WINDOW and nothing else. It gives the child a console with no
// window, which its own children inherit, so the whole tree stays invisible.
// DETACHED_PROCESS must not be added: Windows ignores CREATE_NO_WINDOW when it
// is set, the child then has no console at all, and every console program it
// starts allocates a fresh one with a visible window — the console windows
// that flashed over the app every few seconds.
func hideConsole(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= hideConsoleFlag
}

// startDetached starts cmd as an independent desktop program: no console, its
// own process group, still running after JukaHub exits. It replaces the old
// auditConfigureDetached helper.
func startDetached(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= 0x00000008 | 0x00000200 // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	cmd.SysProcAttr.HideWindow = true
}
