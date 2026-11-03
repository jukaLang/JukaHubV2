//go:build linux || darwin || freebsd || netbsd || openbsd

package main

// screengroup_unix.go — the POSIX half of the screen hand-off. A launcher on
// the handheld is usually a shell script, and stock TrimUI/CrossMix scripts do
// not always `exec` the emulator: some start it and return. Waiting only for
// the script would hand the framebuffer back while RetroArch is still drawing,
// which is the flicker between the UI and the game. Putting the child in its
// own process group lets the reaper ask whether anything it started is still
// alive, without touching the group the app itself belongs to.

import (
	"errors"
	"os/exec"
	"syscall"
)

// screenChildGroupConfigure makes cmd the leader of a new process group. It
// must run before cmd.Start.
func screenChildGroupConfigure(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// screenChildGroupConfigured reports whether the child leads its own group,
// which is what makes the group check meaningful.
func screenChildGroupConfigured(cmd *exec.Cmd) bool {
	return cmd != nil && cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid
}

// screenSignal is the portable set of signals the exit path needs. The group
// helpers map it to the platform's own constants so the callers stay shared.
type screenSignal int

const (
	screenSignalInterrupt screenSignal = iota
	screenSignalKill
)

// screenChildSignal delivers a signal to the child's whole group. A launcher
// that started the emulator in the background must not survive the request: the
// emulator keeps the framebuffer, and the firmware is left on a frozen picture.
func screenChildSignal(cmd *exec.Cmd, kind screenSignal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	sig := syscall.SIGINT
	if kind == screenSignalKill {
		sig = syscall.SIGKILL
	}
	if screenChildGroupConfigured(cmd) {
		if err := syscall.Kill(-cmd.Process.Pid, sig); err == nil {
			return nil
		}
	}
	return cmd.Process.Signal(sig)
}

// screenChildKill stops the child and anything it started, without waiting.
func screenChildKill(cmd *exec.Cmd) error {
	return screenChildSignal(cmd, screenSignalKill)
}

// screenChildGroupAlive reports whether any process in cmd's group still
// exists. Signal 0 only checks: nothing is delivered. EPERM means the group is
// there but belongs to another user, which still counts as alive.
func screenChildGroupAlive(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return false
	}
	err := syscall.Kill(-cmd.Process.Pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}
