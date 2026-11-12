package main

// memstat_linux.go — the process's own memory from /proc/self/status
// (memstat.go).

import (
	"os"
	"strings"
)

// readSelfMemory reads VmRSS and VmSwap. Any goroutine: it is one small file
// read, which the render thread still leaves to the Hardware screen's sampler
// and the cache (selfMemoryCachedText).
func readSelfMemory() selfMemStat {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return selfMemStat{}
	}
	return parseSelfStatus(string(data))
}

// parseSelfStatus picks VmRSS and VmSwap out of /proc/self/status. A kernel
// without swap support has no VmSwap line, which is no swap, not a failure.
func parseSelfStatus(text string) selfMemStat {
	var st selfMemStat
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "VmRSS:"):
			if v := parseKilobytes(strings.TrimPrefix(line, "VmRSS:")); v > 0 {
				st.rss, st.ok = uint64(v), true
			}
		case strings.HasPrefix(line, "VmSwap:"):
			if v := parseKilobytes(strings.TrimPrefix(line, "VmSwap:")); v > 0 {
				st.swap = uint64(v)
			}
		}
	}
	return st
}
