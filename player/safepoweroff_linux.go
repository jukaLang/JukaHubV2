//go:build linux

package main

// safepoweroff_linux.go — the steps of Safe power off (safepoweroff.go): the
// card's mounts from /proc/mounts, the programs holding files there from
// /proc/*/fd, SIGTERM, sync and the read-only remount, all inside one
// deadline.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// safePowerOffRun is the platform half of safePowerOffPrepare.
func safePowerOffRun(verb string, deadline time.Time) func() {
	began := time.Now()
	// 1. The card's mounts and the programs with files open there. A read of
	// /proc can block (a process stuck on the card holds its own locks), so
	// the scan runs on its own goroutine; nobody is signalled when it overran.
	scanBy := began.Add(safePowerOffScanBudget)
	if scanBy.After(deadline) {
		scanBy = deadline
	}
	var mounts []string
	var mountFlags map[string]uintptr
	var holders []safePowerOffHolder
	if !safePowerOffBounded(scanBy.Add(100*time.Millisecond), func() {
		mounts, mountFlags = safePowerOffCardMounts()
		if len(mounts) > 0 {
			holders = safePowerOffHolders(mounts, scanBy)
		}
	}) {
		logWarn("power", "safe power off: the scan of the card's users did not finish in time; the sync follows as usual")
		return func() {}
	}
	if len(mounts) == 0 {
		logInfo("power", "safe power off: no separate writable card mount found; the sync follows as usual")
		return func() {}
	}
	if len(holders) > 0 {
		var names []string
		for _, h := range holders {
			if err := syscall.Kill(h.pid, syscall.SIGTERM); err == nil {
				names = append(names, h.name+" ("+strconv.Itoa(h.pid)+")")
			}
		}
		logInfo("power", "safe power off: asked %d program(s) using the card to close: %s", len(names), strings.Join(names, ", "))
		// 2. Up to safePowerOffWait for them, leaving time for the sync.
		waitUntil := time.Now().Add(safePowerOffWait)
		if reserve := deadline.Add(-700 * time.Millisecond); waitUntil.After(reserve) {
			waitUntil = reserve
		}
		left := safePowerOffWaitGone(holders, waitUntil)
		if left > 0 {
			logInfo("power", "safe power off: %d program(s) still running; going on", left)
		}
	}
	// 3. sync, bounded.
	if !safePowerOffBounded(deadline.Add(-200*time.Millisecond), func() { syscall.Sync() }) {
		logWarn("power", "safe power off: the sync did not finish in time; going on")
		return func() {}
	}
	// 4. Read-only, deepest mount first. JukaHub's own errors.txt (stdout and
	// stderr) is let go of first, or the kernel refuses for it alone.
	// The mount's own options (noatime, nosuid…) go with both remounts: a
	// remount replaces them with the flags passed.
	saved := safePowerOffReleaseStdio(mounts)
	var mu sync.Mutex
	var readOnly []string
	undone := false // set by the undo; a remount still running then is reverted
	finished := safePowerOffBounded(deadline, func() {
		for _, mp := range mounts {
			mu.Lock()
			stop := undone
			mu.Unlock()
			if stop {
				return
			}
			if err := syscall.Mount("", mp, "", syscall.MS_REMOUNT|syscall.MS_RDONLY|mountFlags[mp], ""); err != nil {
				logInfo("power", "safe power off: %s stays writable (%v)", mp, err)
				continue
			}
			mu.Lock()
			late := undone
			if !late {
				readOnly = append(readOnly, mp)
			}
			mu.Unlock()
			if late {
				_ = syscall.Mount("", mp, "", syscall.MS_REMOUNT|mountFlags[mp], "")
				return
			}
		}
	})
	mu.Lock()
	done := append([]string(nil), readOnly...)
	mu.Unlock()
	if !finished {
		logWarn("power", "safe power off: the read-only switch did not finish in time; going on")
	}
	logInfo("power", "safe power off before %s: %d of %d card mount(s) read-only in %s", verb, len(done), len(mounts), time.Since(began).Round(10*time.Millisecond))
	return func() {
		mu.Lock()
		undone = true
		undoList := append([]string(nil), readOnly...)
		mu.Unlock()
		for _, mp := range undoList {
			if err := syscall.Mount("", mp, "", syscall.MS_REMOUNT|mountFlags[mp], ""); err != nil {
				logWarn("power", "safe power off: could not make %s writable again: %v", mp, err)
			}
		}
		safePowerOffRestoreStdio(saved)
		if len(undoList) > 0 {
			logWarn("power", "%s failed: the card is writable again", verb)
		}
	}
}

// safePowerOffCardMounts lists the writable mount points at or below the
// card's roots, deepest first, never "/", with each one's per-mount flags.
func safePowerOffCardMounts() ([]string, map[string]uintptr) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil, nil
	}
	roots := []string{"/mnt/SDCARD"}
	if r := defaultTrimuiSDRoot(); r != "" {
		roots = append(roots, filepath.Clean(r))
	}
	return safePowerOffParseMounts(string(data), roots), safePowerOffMountFlags(string(data))
}

// safePowerOffMountOpts are the /proc/mounts options a remount must pass
// again to keep them.
var safePowerOffMountOpts = map[string]uintptr{
	"nosuid":     syscall.MS_NOSUID,
	"nodev":      syscall.MS_NODEV,
	"noexec":     syscall.MS_NOEXEC,
	"noatime":    syscall.MS_NOATIME,
	"nodiratime": syscall.MS_NODIRATIME,
	"relatime":   syscall.MS_RELATIME,
	"sync":       syscall.MS_SYNCHRONOUS,
	"dirsync":    syscall.MS_DIRSYNC,
}

// safePowerOffMountFlags maps each mount point of a /proc/mounts text to the
// flags of its options in safePowerOffMountOpts (the last line of a point
// wins: it is the mount on top). Pure.
func safePowerOffMountFlags(text string) map[string]uintptr {
	out := map[string]uintptr{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		var flags uintptr
		for _, opt := range strings.Split(fields[3], ",") {
			flags |= safePowerOffMountOpts[opt]
		}
		out[safePowerOffUnescape(fields[1])] = flags
	}
	return out
}

// safePowerOffParseMounts picks the mount points of a /proc/mounts text at or
// below one of roots, without "/", duplicates or ones mounted read-only (an
// image the undo must not make writable), deepest first. Pure.
func safePowerOffParseMounts(text string, roots []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		mp := safePowerOffUnescape(fields[1])
		if mp == "/" || seen[mp] {
			continue
		}
		if len(fields) >= 4 && (fields[3] == "ro" || strings.HasPrefix(fields[3], "ro,")) {
			continue
		}
		for _, root := range roots {
			if root == "" || root == "/" {
				continue
			}
			if mp == root || strings.HasPrefix(mp, root+"/") {
				seen[mp] = true
				out = append(out, mp)
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// safePowerOffUnescape undoes /proc/mounts' octal escapes (\040 for a space).
func safePowerOffUnescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// safePowerOffHolder is one process with a file open on the card.
type safePowerOffHolder struct {
	pid  int
	name string
}

// safePowerOffHolders scans /proc for processes with an open file below one
// of mounts, until scanBy or the caps. Never PID 1, a kernel thread, JukaHub
// or its parent chain.
func safePowerOffHolders(mounts []string, scanBy time.Time) []safePowerOffHolder {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	self := os.Getpid()
	skip := map[int]bool{1: true, self: true}
	for p, n := os.Getppid(), 0; p > 1 && n < 32; n++ {
		skip[p] = true
		p = safePowerOffParent(p)
	}
	// The firmware loops launch.sh or the display guard paused are left as
	// Close all leaves them (closeall_linux.go), and so is JukaHub's own
	// process group (launch.sh's helpers).
	for _, pid := range closeAllPausedPids() {
		skip[pid] = true
	}
	own := syscall.Getpgrp()
	var out []safePowerOffHolder
	pids, links := 0, 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 || skip[pid] {
			continue
		}
		pids++
		if pids > safePowerOffMaxPids || links > safePowerOffMaxFds || time.Now().After(scanBy) {
			break
		}
		base := "/proc/" + e.Name()
		// A kernel thread (and a zombie) has an empty command line.
		if cmdline, err := os.ReadFile(base + "/cmdline"); err != nil || len(cmdline) == 0 {
			continue
		}
		fds, err := os.ReadDir(base + "/fd")
		if err != nil {
			continue
		}
		holds := false
		for _, fd := range fds {
			links++
			if links > safePowerOffMaxFds {
				break
			}
			target, err := os.Readlink(base + "/fd/" + fd.Name())
			if err != nil || !strings.HasPrefix(target, "/") {
				continue
			}
			if safePowerOffUnder(target, mounts) {
				holds = true
				break
			}
		}
		if !holds {
			continue
		}
		stat, err := os.ReadFile(base + "/stat")
		if err != nil {
			continue
		}
		if state, _, pgrp := safePowerOffParseStat(string(stat)); state == 'Z' || (own > 1 && pgrp == own) {
			continue
		}
		name, _ := os.ReadFile(base + "/comm")
		out = append(out, safePowerOffHolder{pid: pid, name: strings.TrimSpace(string(name))})
	}
	return out
}

// safePowerOffUnder reports whether path is at or below one of mounts. Pure.
func safePowerOffUnder(path string, mounts []string) bool {
	for _, mp := range mounts {
		if path == mp || strings.HasPrefix(path, mp+"/") {
			return true
		}
	}
	return false
}

// safePowerOffParent is a process's parent from /proc/<pid>/stat, or 0.
func safePowerOffParent(pid int) int {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	_, ppid, _ := safePowerOffParseStat(string(data))
	return ppid
}

// safePowerOffParseStat reads the state, the parent and the process group
// from a /proc/<pid>/stat line: "pid (comm) S ppid pgrp …", where comm may
// hold spaces and parentheses. Pure.
func safePowerOffParseStat(stat string) (state byte, ppid, pgrp int) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 || i+2 >= len(stat) {
		return 0, 0, 0
	}
	fields := strings.Fields(stat[i+1:])
	if len(fields) < 3 || len(fields[0]) == 0 {
		return 0, 0, 0
	}
	ppid, _ = strconv.Atoi(fields[1])
	pgrp, _ = strconv.Atoi(fields[2])
	return fields[0][0], ppid, pgrp
}

// safePowerOffWaitGone waits until every holder has ended (or is a zombie)
// or until; it returns how many are left.
func safePowerOffWaitGone(holders []safePowerOffHolder, until time.Time) int {
	for {
		left := 0
		for _, h := range holders {
			data, err := os.ReadFile("/proc/" + strconv.Itoa(h.pid) + "/stat")
			if err != nil {
				continue
			}
			if state, _, _ := safePowerOffParseStat(string(data)); state == 'Z' || state == 'X' {
				continue
			}
			left++
		}
		if left == 0 || !time.Now().Before(until) {
			return left
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// safePowerOffBounded runs fn on a goroutine and waits for it until the
// deadline; false when it was still running then (it goes on alone).
func safePowerOffBounded(until time.Time, fn func()) bool {
	wait := time.Until(until)
	if wait <= 0 {
		return false
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = recover() }()
		fn()
	}()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// safePowerOffReleaseStdio points stdout and stderr at /dev/null when they
// write to a file on the card, and returns those files' paths by fd.
func safePowerOffReleaseStdio(mounts []string) map[int]string {
	saved := map[int]string{}
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return saved
	}
	defer null.Close()
	for _, fd := range []int{1, 2} {
		target, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
		if err != nil || !safePowerOffUnder(target, mounts) {
			continue
		}
		if err := syscall.Dup3(int(null.Fd()), fd, 0); err == nil {
			saved[fd] = target
		}
	}
	return saved
}

// safePowerOffRestoreStdio points them back at their files, appending.
func safePowerOffRestoreStdio(saved map[int]string) {
	for fd, path := range saved {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			continue
		}
		_ = syscall.Dup3(int(f.Fd()), fd, 0)
		_ = f.Close()
	}
}
