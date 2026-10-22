//go:build !windows

package main

import "syscall"

// processAlive reports whether pid still exists. Signal 0 performs the
// permission and existence checks without delivering anything; EPERM means the
// process is alive but owned by someone else.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
