//go:build !linux

package main

// uiThreadPriorityInit has nothing to raise off Linux (uiprio_linux.go).
func uiThreadPriorityInit() {}
