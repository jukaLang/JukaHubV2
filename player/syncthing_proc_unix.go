//go:build !windows

package main

// syncthing_proc_unix.go — the managed Syncthing runs in its own process group
// at the lowest CPU priority, so a sync never competes with the UI or a game.

import (
	"os/exec"
	"syscall"
)

// syncthingProcAttr puts the child in its own process group: a game's group
// kill never reaches it, and the priority below applies to all its threads.
func syncthingProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// syncthingLowerPriority sets nice 19 on the child's process group. Linux
// keeps a nice value per thread, and PRIO_PGRP reaches every thread that
// exists; threads started later inherit it. Best effort.
func syncthingLowerPriority(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := syscall.Setpriority(syscall.PRIO_PGRP, cmd.Process.Pid, 19); err != nil {
		logInfo("syncthing", "nice 19: %v", err)
	}
}

// syncthingKillTree kills the child's process group: Syncthing's monitor
// process and the instance it runs.
func syncthingKillTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
