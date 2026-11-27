//go:build !windows

package main

// emuproc_other.go — the console-hiding hook is a Windows concern; elsewhere a
// helper process has no window to hide.
