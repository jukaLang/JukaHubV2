//go:build !windows

package main

// terminal_other.go — the Terminal's shell on the handheld and other POSIX
// systems: /bin/sh (BusyBox ash on TrimUI), stopped as a whole process group.

import (
	"context"
	"os"
	"os/exec"
)

// termShellCommand runs line through sh -c, so pipes, quotes, wildcards and
// redirects behave as they do in any shell. It returns nil when the system has
// no shell; the caller then runs the first word as a program directly.
//
// Trust boundary (SEC-12): line is what the user typed at the Terminal prompt
// on the device (or picked from their own history). Running it in a shell is
// the Terminal's purpose; no other code path builds a line for it.
func termShellCommand(ctx context.Context, line string) *exec.Cmd {
	sh, err := exec.LookPath("sh")
	if err != nil {
		if _, statErr := os.Stat("/bin/sh"); statErr != nil {
			return nil
		}
		sh = "/bin/sh"
	}
	return exec.CommandContext(ctx, sh, "-c", line)
}

// termKillTree stops the command and everything it started: the job leads its
// own process group (screenChildGroupConfigure), so the whole group goes.
func termKillTree(cmd *exec.Cmd) error {
	return screenChildKill(cmd)
}
