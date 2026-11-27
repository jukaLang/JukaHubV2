//go:build !windows

package main

import (
	"context"
	"os/exec"
)

// hideConsole is a no-op off Windows: there is no console window to hide.
func hideConsole(cmd *exec.Cmd) {}

// hiddenCommand is exec.Command on non-Windows platforms.
func hiddenCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// hiddenCommandContext is exec.CommandContext on non-Windows platforms.
func hiddenCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// startDetached puts POSIX helpers in their own process group. There is no
// console to hide here, but the group is what lets the exit path stop the
// whole tree (javaStartDetached, secAuditOpen); dropping it when the console
// helpers were consolidated left those children in JukaHub's own group.
func startDetached(cmd *exec.Cmd) {
	processGroupConfigure(cmd)
}
