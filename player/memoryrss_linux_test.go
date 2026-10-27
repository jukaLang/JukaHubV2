//go:build linux

package main

// Test-only process memory reading. The texture caches account for their own
// bytes exactly, but SDL surfaces and textures are native allocations the Go
// heap says nothing about: a surface created and never freed is invisible to
// runtime.MemStats. The resident set is where it shows up.
//
// Test files only: the app has no reason to read its own RSS, and the mobile
// builds must not grow a /proc dependency.

import (
	"os"
	"strconv"
	"strings"
)

// currentProcessRSSBytes returns this process's resident set size, or 0 when
// /proc is unavailable (some sandboxes hide it).
func currentProcessRSSBytes() uint64 {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb << 10
	}
	return 0
}
