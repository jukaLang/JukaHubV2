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

// startDetached is a no-op off Windows: a desktop program runs on its own
// already, without a console.
func startDetached(cmd *exec.Cmd) {}
