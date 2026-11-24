//go:build linux

package main

// faststart_linux.go — the Linux half of Faster game starts (faststart.go):
// the card's read-ahead, and JukaHub stepping behind a game while it loads.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	// fastStartHubNice is JukaHub's threads' priority while a game loads, and
	// fastStartHubIO their I/O priority: best-effort at its lowest level
	// (IOPRIO_CLASS_BE << 13 | 7), so the game's reads go first and JukaHub's
	// still go — the idle class waits for a quiet card, which a game
	// streaming its disc may never leave.
	fastStartHubNice = 10
	fastStartHubIO   = 2<<13 | 7
)

// fastStartThread is one JukaHub thread the window moved, with what it had.
type fastStartThread struct {
	tid    int
	nice   int
	ioprio int
	niced  bool
	io     bool
}

var fastStartHub struct {
	mu      sync.Mutex
	threads []fastStartThread
}

// fastStartReadAheadPlan is the read-ahead raise for the disks paths are on,
// each disk once: the heavy-game boost's plan (1 MB where lower,
// xboxboost_linux.go).
func fastStartReadAheadPlan(paths ...string) []saverSysfsWrite {
	var plan []saverSysfsWrite
	for _, p := range paths {
		for _, w := range xboxBoostReadAheadPlan(p) {
			dup := false
			for _, have := range plan {
				if have.Path == w.Path {
					dup = true
					break
				}
			}
			if !dup {
				plan = append(plan, w)
			}
		}
	}
	return plan
}

// fastStartApply makes the planned writes and keeps the ones that took, with
// what the kernel reads back (what the restore compares with).
func fastStartApply(plan []saverSysfsWrite) []saverSysfsWrite {
	var done []saverSysfsWrite
	for _, w := range plan {
		if err := os.WriteFile(w.Path, []byte(w.Written+"\n"), 0o644); err != nil {
			logDebug("library", "faster game start: %s left as it is (%v)", w.Path, err)
			continue
		}
		if got := saverReadTrimmed(w.Path); got != "" {
			w.Written = got
		}
		done = append(done, w)
	}
	return done
}

// fastStartSoundThread reports one of SDL's sound threads ("SDLAudioP2"):
// music JukaHub keeps playing under a game keeps its place on the CPU.
func fastStartSoundThread(tid int) bool {
	raw, err := os.ReadFile(filepath.Join("/proc/self/task", strconv.Itoa(tid), "comm"))
	return err == nil && strings.HasPrefix(string(raw), "SDLAudio")
}

// fastStartIOAhead reports an I/O priority ahead of fastStartHubIO: the
// "none" class (which follows the nice) or a best-effort level under 7. The
// real-time and idle classes are someone's choice and stay.
func fastStartIOAhead(prio int) bool {
	class := prio >> 13
	level := prio & 0x1fff
	return class == 0 || (class == 2 && level < 7)
}

// fastStartIOBack is the value that puts an I/O priority back. The "none"
// class takes no level (ioprio_set refuses one), though ioprio_get reports a
// thread that never did I/O as "none, level 4".
func fastStartIOBack(prio int) int {
	if prio>>13 == 0 {
		return 0
	}
	return prio
}

// fastStartStepBack moves JukaHub's threads behind the game — each one at a
// lower nice to fastStartHubNice, each one whose I/O priority is ahead of it
// to fastStartHubIO — remembering what each had, and reports how many it
// moved. SDL's sound threads stay as they are.
func fastStartStepBack() int {
	entries, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return 0
	}
	fastStartHub.mu.Lock()
	defer fastStartHub.mu.Unlock()
	if len(fastStartHub.threads) > 0 {
		return len(fastStartHub.threads) // stepped back already
	}
	for _, e := range entries {
		tid, err := strconv.Atoi(e.Name())
		if err != nil || fastStartSoundThread(tid) {
			continue
		}
		t := fastStartThread{tid: tid}
		if nice, ok := xboxBoostThreadNice(tid); ok && nice < fastStartHubNice {
			if unix.Setpriority(unix.PRIO_PROCESS, tid, fastStartHubNice) == nil {
				t.nice, t.niced = nice, true
			}
		}
		// ioprio_get / ioprio_set(IOPRIO_WHO_PROCESS, tid, …).
		if prio, _, errno := unix.Syscall(unix.SYS_IOPRIO_GET, 1, uintptr(tid), 0); errno == 0 && fastStartIOAhead(int(prio)) {
			if _, _, errno := unix.Syscall(unix.SYS_IOPRIO_SET, 1, uintptr(tid), uintptr(fastStartHubIO)); errno == 0 {
				t.ioprio, t.io = int(prio), true
			}
		}
		if t.niced || t.io {
			fastStartHub.threads = append(fastStartHub.threads, t)
		}
	}
	return len(fastStartHub.threads)
}

// fastStartStepForward puts JukaHub's threads back: the ones it moved to their
// own values where they still hold the window's, and threads started during
// the window (which inherited them) to normal.
func fastStartStepForward() {
	fastStartHub.mu.Lock()
	moved := fastStartHub.threads
	fastStartHub.threads = nil
	fastStartHub.mu.Unlock()
	if len(moved) == 0 {
		return
	}
	known := make(map[int]fastStartThread, len(moved))
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
		if nice, ok := xboxBoostThreadNice(tid); ok && nice == fastStartHubNice {
			switch {
			case was && t.niced:
				_ = unix.Setpriority(unix.PRIO_PROCESS, tid, t.nice)
			case !was:
				_ = unix.Setpriority(unix.PRIO_PROCESS, tid, 0)
			}
		}
		prio, _, errno := unix.Syscall(unix.SYS_IOPRIO_GET, 1, uintptr(tid), 0)
		if errno != 0 || int(prio) != fastStartHubIO {
			continue
		}
		switch {
		case was && t.io:
			_, _, _ = unix.Syscall(unix.SYS_IOPRIO_SET, 1, uintptr(tid), uintptr(fastStartIOBack(t.ioprio)))
		case !was:
			_, _, _ = unix.Syscall(unix.SYS_IOPRIO_SET, 1, uintptr(tid), 0)
		}
	}
}
