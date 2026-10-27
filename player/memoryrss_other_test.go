//go:build !linux && !windows

package main

// Platforms without a reader report unknown memory; the texture accounting in
// perf.go still measures exactly, and the harness skips the RSS comparison.

func currentProcessRSSBytes() uint64 { return 0 }
