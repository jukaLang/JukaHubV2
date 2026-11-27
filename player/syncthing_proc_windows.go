//go:build windows

package main

// syncthing_proc_windows.go — the managed Syncthing runs with no console
// window and below-normal priority, so a sync never competes with a game.

import (
	"os/exec"
	"syscall"
)

const syncthingBelowNormalPriority = 0x00004000 // BELOW_NORMAL_PRIORITY_CLASS

// syncthingProcAttr hides the console and starts the child at below-normal
// priority (the class is set at creation, so there is nothing to lower later).
func syncthingProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syncthingBelowNormalPriority
}

// syncthingLowerPriority is a no-op: the priority class came with the process.
func syncthingLowerPriority(cmd *exec.Cmd) {}

// syncthingKillTree kills Syncthing's monitor process and the instance it
// runs (taskkill /T, terminal_windows.go).
func syncthingKillTree(cmd *exec.Cmd) { _ = termKillTree(cmd) }
