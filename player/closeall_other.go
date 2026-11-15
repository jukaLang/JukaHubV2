//go:build !linux

package main

// closeall_other.go — no /proc here: "Close all apps & games" closes
// JukaHub's own children only (closeall.go), and there is no page cache to
// drop.

import "os/exec"

func closeAllScan(exclude []*exec.Cmd, keepWine bool) []closeAllProc { return nil }

func closeAllSignalProcs(procs []closeAllProc, kill bool) {}

func closeAllProcsAlive(procs []closeAllProc) bool { return false }

func closeAllFlushCaches() {}
