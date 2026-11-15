//go:build linux

package main

// closeall_linux.go — the /proc half of closeall.go: finding the apps no
// JukaHub of this run tracks, signalling them, and dropping the page cache.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// closeAllScanAllowed: only on a handheld, where every process is the
// player's and runs as root. On a Linux desktop "java" or "wine" belongs to
// someone's IDE or another program, so only JukaHub's own children close.
func closeAllScanAllowed() bool {
	return IsTrimuiDevice() || P().IsHandheld()
}

// closeAllScan lists the untracked apps to close. exclude are commands whose
// processes (their group and everything under them) are spared; keepWine
// spares every Wine process (closeAllKeepsWine). Any goroutine.
func closeAllScan(exclude []*exec.Cmd, keepWine bool) []closeAllProc {
	if !closeAllScanAllowed() {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	all := make(map[int]closeAllStat, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		if st, ok := closeAllParseStat(data); ok {
			all[pid] = st
		}
	}
	roots := make(map[int]bool, len(exclude))
	for _, cmd := range exclude {
		if cmd != nil && cmd.Process != nil && cmd.Process.Pid > 0 {
			roots[cmd.Process.Pid] = true
		}
	}
	never := closeAllAncestors(all, os.Getpid())
	never[os.Getppid()] = true
	// JukaHub's own process group (launch.sh, the helpers it starts without a
	// group of their own) and the firmware loops launch.sh, the boot hook or
	// the display guard paused (they are not always above JukaHub) are never
	// touched either.
	own := syscall.Getpgrp()
	for pid, st := range all {
		if own > 1 && st.pgid == own {
			never[pid] = true
		}
	}
	for _, pid := range closeAllPausedPids() {
		never[pid] = true
	}
	return closeAllPick(all, roots, never, closeAllOwnDir(), keepWine, func(pid int) ([]string, string) {
		dir := "/proc/" + strconv.Itoa(pid)
		data, err := os.ReadFile(dir + "/cmdline")
		if err != nil {
			return nil, ""
		}
		exe, _ := os.Readlink(dir + "/exe")
		return closeAllSplitCmdline(data), exe
	})
}

// closeAllSameProc reports that p is still the process the scan saw (same
// start time, not a zombie).
func closeAllSameProc(p closeAllProc) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(p.pid) + "/stat")
	if err != nil {
		return false
	}
	st, ok := closeAllParseStat(data)
	return ok && st.start == p.start && st.state != 'Z' && st.state != 'X'
}

// closeAllSignalProcs asks procs to quit (SIGTERM, and SIGCONT so a stopped
// one acts on it) or, with kill, ends them (SIGKILL).
func closeAllSignalProcs(procs []closeAllProc, kill bool) {
	for _, p := range procs {
		if p.pid <= 1 || p.pid == os.Getpid() || !closeAllSameProc(p) {
			continue
		}
		if kill {
			_ = syscall.Kill(p.pid, syscall.SIGKILL)
			continue
		}
		_ = syscall.Kill(p.pid, syscall.SIGTERM)
		_ = syscall.Kill(p.pid, syscall.SIGCONT)
	}
}

// closeAllProcsAlive reports whether any of procs still runs.
func closeAllProcsAlive(procs []closeAllProc) bool {
	for _, p := range procs {
		if closeAllSameProc(p) {
			return true
		}
	}
	return false
}

// closeAllFlushCaches writes dirty pages out and drops the page cache, so the
// memory the closed apps' files held reads as free. Errors are ignored: it
// needs root, which the handheld has.
func closeAllFlushCaches() {
	if !closeAllScanAllowed() {
		return
	}
	syscall.Sync()
	_ = os.WriteFile("/proc/sys/vm/drop_caches", []byte("3\n"), 0o644)
}

// closeAllAncestors is pid and every process above it in all.
func closeAllAncestors(all map[int]closeAllStat, pid int) map[int]bool {
	out := map[int]bool{pid: true}
	for i := 0; i < 64 && pid > 1; i++ {
		st, ok := all[pid]
		if !ok {
			break
		}
		pid = st.ppid
		out[pid] = true
	}
	return out
}

// closeAllPausedPids are the PIDs in launch.sh's paused record
// (displayGuardPausedRecord: "PID start name" lines).
func closeAllPausedPids() []int {
	data, err := os.ReadFile(displayGuardPausedRecord)
	if err != nil {
		return nil
	}
	var out []int
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil && pid > 0 {
			out = append(out, pid)
		}
	}
	return out
}

// closeAllOwnDir is JukaHub's folder ("" when unknown).
func closeAllOwnDir() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}
