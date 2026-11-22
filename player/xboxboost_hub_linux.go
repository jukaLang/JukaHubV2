//go:build linux

package main

// xboxboost_hub_linux.go — two more things Heavy-game boost (xboxboost.go)
// does on Linux, from 2026-11-21 ("improve performance even further"):
//
//   - JukaHub steps back: every thread of JukaHub's own process goes to nice
//     xboxBoostHubNice and the idle I/O class while a boosted game plays, so a
//     cover scan, a sync or a download that wakes in the background takes a
//     core and the card only when the game does not want them. Only threads
//     at a lower nice (or a busier I/O class) are moved, each one's value is
//     remembered, and the end of the game puts back exactly those still at
//     the boost's value — a worker that lowered itself on purpose (the library
//     index's CRC pass sits at nice 19, idle I/O) is never raised. Threads
//     Go starts during the game inherit the stepped-back values and are reset
//     to normal with the rest. JukaHub's own priority dies with it, so a crash
//     leaves nothing to put back.
//   - the fast cluster: on a chip with two CPU clusters of different top
//     clocks (the Smart Pro S's A523: four Cortex-A55 at 2.0 GHz, four at
//     1.4 GHz), the game's first process is held to the faster cluster's
//     CPUs at its start, before it starts the emulator, which inherits that.
//     A scheduler that put the emulator's main thread on the slow cluster
//     loses a third of its speed there. One cluster (the A133P), or a fast
//     cluster under four CPUs, changes nothing.

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	// xboxBoostHubNice is JukaHub's threads' priority while a boosted game
	// plays; xboxBoostIOIdle the idle I/O class (IOPRIO_CLASS_IDLE << 13).
	xboxBoostHubNice = 10
	xboxBoostIOIdle  = 3 << 13
	// xboxBoostPinMin is the fewest CPUs a fast cluster needs to hold a game.
	xboxBoostPinMin = 4
)

// xboxBoostHubThread is one JukaHub thread the boost moved, with what it had.
type xboxBoostHubThread struct {
	tid    int
	nice   int
	ioprio int
	niced  bool
	ioIdle bool
}

var xboxBoostHub struct {
	mu      sync.Mutex
	threads []xboxBoostHubThread
}

// xboxBoostThreadNice reads a thread's nice from /proc/self/task/<tid>/stat
// (field 19); ok is false when it cannot be read.
func xboxBoostThreadNice(tid int) (int, bool) {
	raw, err := os.ReadFile(filepath.Join("/proc/self/task", strconv.Itoa(tid), "stat"))
	if err != nil {
		return 0, false
	}
	s := string(raw)
	// The command name is in parentheses and may hold spaces: fields count
	// from after its closing one (field 3 is the first there).
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 17 {
		return 0, false
	}
	n, err := strconv.Atoi(fields[16])
	return n, err == nil
}

// xboxBoostStepBack moves JukaHub's threads behind the game and reports how
// many it moved.
func xboxBoostStepBack() int {
	entries, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return 0
	}
	xboxBoostHub.mu.Lock()
	defer xboxBoostHub.mu.Unlock()
	if len(xboxBoostHub.threads) > 0 {
		return 0 // stepped back already
	}
	for _, e := range entries {
		tid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		t := xboxBoostHubThread{tid: tid}
		if nice, ok := xboxBoostThreadNice(tid); ok && nice < xboxBoostHubNice {
			if unix.Setpriority(unix.PRIO_PROCESS, tid, xboxBoostHubNice) == nil {
				t.nice, t.niced = nice, true
			}
		}
		// ioprio_get(IOPRIO_WHO_PROCESS, tid): the class sits in the top bits.
		if prio, _, errno := unix.Syscall(unix.SYS_IOPRIO_GET, 1, uintptr(tid), 0); errno == 0 && int(prio)>>13 != 3 {
			if _, _, errno := unix.Syscall(unix.SYS_IOPRIO_SET, 1, uintptr(tid), uintptr(xboxBoostIOIdle)); errno == 0 {
				t.ioprio, t.ioIdle = int(prio), true
			}
		}
		if t.niced || t.ioIdle {
			xboxBoostHub.threads = append(xboxBoostHub.threads, t)
		}
	}
	return len(xboxBoostHub.threads)
}

// xboxBoostStepForward puts JukaHub's threads back: the ones it moved to
// their own values where they still hold the boost's, and threads started
// during the game (which inherited them) to normal.
func xboxBoostStepForward() {
	xboxBoostHub.mu.Lock()
	moved := xboxBoostHub.threads
	xboxBoostHub.threads = nil
	xboxBoostHub.mu.Unlock()
	if len(moved) == 0 {
		return
	}
	known := make(map[int]xboxBoostHubThread, len(moved))
	for _, t := range moved {
		known[t.tid] = t
	}
	entries, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return
	}
	for _, e := range entries {
		tid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		t, was := known[tid]
		if nice, ok := xboxBoostThreadNice(tid); ok && nice == xboxBoostHubNice {
			switch {
			case was && t.niced:
				_ = unix.Setpriority(unix.PRIO_PROCESS, tid, t.nice)
			case !was:
				_ = unix.Setpriority(unix.PRIO_PROCESS, tid, 0)
			}
		}
		prio, _, errno := unix.Syscall(unix.SYS_IOPRIO_GET, 1, uintptr(tid), 0)
		if errno != 0 || int(prio) != xboxBoostIOIdle {
			continue
		}
		switch {
		case was && t.ioIdle:
			_, _, _ = unix.Syscall(unix.SYS_IOPRIO_SET, 1, uintptr(tid), uintptr(t.ioprio))
		case !was:
			_, _, _ = unix.Syscall(unix.SYS_IOPRIO_SET, 1, uintptr(tid), 0)
		}
	}
}

// xboxBoostFastCPUs lists the CPUs of the fastest cluster when the chip has
// clusters of different top clocks and that one has xboxBoostPinMin CPUs or
// more; nil otherwise.
func xboxBoostFastCPUs(root string) []int {
	dirs := cpufreqPolicyDirs(root)
	if len(dirs) < 2 {
		return nil
	}
	type cluster struct {
		top  int64
		cpus []int
	}
	var clusters []cluster
	for _, d := range dirs {
		var cpus []int
		for _, f := range strings.Fields(strings.ReplaceAll(saverReadTrimmed(filepath.Join(d, "related_cpus")), ",", " ")) {
			if n, err := strconv.Atoi(f); err == nil && n >= 0 && n < 1024 {
				cpus = append(cpus, n)
			}
		}
		top := readKHzFile(filepath.Join(d, "cpuinfo_max_freq"))
		if len(cpus) > 0 && top > 0 {
			clusters = append(clusters, cluster{top: top, cpus: cpus})
		}
	}
	if len(clusters) < 2 {
		return nil
	}
	sort.SliceStable(clusters, func(i, j int) bool { return clusters[i].top > clusters[j].top })
	if clusters[0].top == clusters[len(clusters)-1].top || len(clusters[0].cpus) < xboxBoostPinMin {
		return nil
	}
	return clusters[0].cpus
}

// xboxBoostPin holds pid to the fast cluster; it returns the CPUs, or "" when
// nothing was pinned.
func xboxBoostPin(pid int) string {
	cpus := xboxBoostFastCPUs(cpufreqRoot)
	if len(cpus) == 0 || pid <= 0 {
		return ""
	}
	var set unix.CPUSet
	set.Zero()
	names := make([]string, 0, len(cpus))
	for _, c := range cpus {
		set.Set(c)
		names = append(names, strconv.Itoa(c))
	}
	if err := unix.SchedSetaffinity(pid, &set); err != nil {
		logInfo("xbox", "boost: could not hold the game to CPUs %s (%v)", strings.Join(names, ","), err)
		return ""
	}
	return strings.Join(names, ",")
}
