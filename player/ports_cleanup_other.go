//go:build !linux

package main

// ports_cleanup_other.go — ports run on the Linux handheld only.

func portCleanupLeftovers() []string { return nil }
