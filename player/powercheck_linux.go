package main

// powercheck_linux.go — the CPU counters behind the battery life check
// (powercheck.go): JukaHub's own time from /proc/self/stat and the whole
// system's from /proc/stat.

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// powerCheckTicksPerSecond is the kernel's USER_HZ, which /proc counts in:
// 100 on every Linux JukaHub runs on.
const powerCheckTicksPerSecond = 100

// powerCheckCPUNow reads the counters; ok is false when /proc is unreadable.
func powerCheckCPUNow() (powerCPUStat, bool) {
	st := powerCPUStat{at: time.Now()}
	self, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return st, false
	}
	own, ok := powerCheckOwnTicks(string(self))
	if !ok {
		return st, false
	}
	all, err := os.ReadFile("/proc/stat")
	if err != nil {
		return st, false
	}
	busy, total, ok := powerCheckSystemTicks(string(all))
	if !ok {
		return st, false
	}
	st.own, st.busy, st.total = own, busy, total
	return st, true
}

// powerCheckOwnTicks is utime + stime from a /proc/<pid>/stat line. The
// command name in parentheses may hold spaces, so the fields are counted
// from its closing parenthesis. Pure.
func powerCheckOwnTicks(stat string) (uint64, bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, false
	}
	// After ") ": state is field 3; utime and stime are fields 14 and 15.
	f := strings.Fields(stat[i+1:])
	if len(f) < 13 {
		return 0, false
	}
	u, err1 := strconv.ParseUint(f[11], 10, 64)
	s, err2 := strconv.ParseUint(f[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return u + s, true
}

// powerCheckSystemTicks is the busy and total ticks of /proc/stat's "cpu"
// line: busy leaves out idle and iowait. Pure.
func powerCheckSystemTicks(stat string) (busy, total uint64, ok bool) {
	line, _, _ := strings.Cut(stat, "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0, false
	}
	var idle uint64
	for i, v := range f[1:] {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		// guest and guest_nice (the 9th and 10th) are already in user.
		if i >= 8 {
			break
		}
		total += n
		if i == 3 || i == 4 {
			idle += n
		}
	}
	return total - idle, total, total > 0
}
