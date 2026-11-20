//go:build !linux

package main

// sessionsafe_other.go — "Never lose a session" off Linux: the battery guard
// and the automatic power offs only act on a handheld, so there is no game
// group to scan, and a desktop game is only ever interrupted the way the exit
// path interrupts it.

import "os/exec"

// sessionSafeScan finds nothing: no /proc.
func sessionSafeScan(cmds []*exec.Cmd) []sessionSafeProc { return nil }

// sessionSafeProcBases knows no folders without /proc.
func sessionSafeProcBases(pid int) []string { return nil }

// sessionSafeSignalTargets has no process list to act on: the scan finds
// none, so there are never targets here.
func sessionSafeSignalTargets(targets []sessionSafeTarget, sig sessionSafeSignal) {}

// sessionSafeTargetAlive knows no process without /proc.
func sessionSafeTargetAlive(t sessionSafeTarget) bool { return false }

// sessionSafeSignalGroups interrupts each child as the exit path does.
func sessionSafeSignalGroups(cmds []*exec.Cmd, sig sessionSafeSignal) {
	for _, cmd := range cmds {
		_ = screenChildSignal(cmd, screenSignalInterrupt)
	}
}
