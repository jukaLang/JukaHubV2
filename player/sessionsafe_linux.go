//go:build linux

package main

// sessionsafe_linux.go — the Linux half of "Never lose a session"
// (sessionsafe.go): which processes a running game is made of, where its
// emulator keeps its files, and the polite signals that ask it to quit.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// sessionSafeScanLimit bounds the /proc walk; a handheld runs a few hundred
// processes at most.
const sessionSafeScanLimit = 1024

// sessionSafeScan lists the live processes of the children's process groups,
// and their descendants that left the group (a launch script's setsid), with
// their names, argv and program path.
func sessionSafeScan(cmds []*exec.Cmd) []sessionSafeProc {
	roots := make(map[int]bool, len(cmds))
	for _, cmd := range cmds {
		if cmd != nil && cmd.Process != nil && cmd.Process.Pid > 0 {
			roots[cmd.Process.Pid] = true
		}
	}
	if len(roots) == 0 {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	all := make(map[int]closeAllStat, 128)
	seen := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		seen++
		if seen > sessionSafeScanLimit {
			break
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		if st, ok := closeAllParseStat(data); ok {
			all[pid] = st
		}
	}
	var out []sessionSafeProc
	for pid, st := range all {
		if st.state == 'Z' || st.state == 'X' || (!roots[st.pgid] && !closeAllUnder(all, pid, roots)) {
			continue
		}
		dir := "/proc/" + strconv.Itoa(pid)
		p := sessionSafeProc{pid: pid, comm: st.comm, start: st.start}
		if data, err := sessionSafeReadLimited(dir+"/cmdline", 8<<10); err == nil {
			p.argv = closeAllSplitCmdline(data)
		}
		p.exe, _ = os.Readlink(dir + "/exe")
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pid < out[j].pid })
	return out
}

// sessionSafeProcBases are where a process keeps its files: its HOME, its
// working folder and its program's folder, each once.
func sessionSafeProcBases(pid int) []string {
	dir := "/proc/" + strconv.Itoa(pid)
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(strings.TrimSuffix(p, " (deleted)"))
		if p == "" || !filepath.IsAbs(p) {
			return
		}
		p = filepath.Clean(p)
		for _, have := range out {
			if have == p {
				return
			}
		}
		out = append(out, p)
	}
	if data, err := sessionSafeReadLimited(dir+"/environ", 64<<10); err == nil {
		for _, kv := range bytes.Split(data, []byte{0}) {
			if bytes.HasPrefix(kv, []byte("HOME=")) {
				add(string(kv[len("HOME="):]))
			}
		}
	}
	if cwd, err := os.Readlink(dir + "/cwd"); err == nil {
		add(cwd)
	}
	if exe, err := os.Readlink(dir + "/exe"); err == nil {
		add(filepath.Dir(strings.TrimSuffix(exe, " (deleted)")))
	}
	return out
}

// sessionSafeSignalGroups sends the polite signal to each child's whole group
// and a continue after it, so a game paused under the in-game menu or an
// alarm acts on it at once instead of when it is killed.
func sessionSafeSignalGroups(cmds []*exec.Cmd, sig sessionSafeSignal) {
	s := syscall.SIGTERM
	if sig == sessionSafeSigInt {
		s = syscall.SIGINT
	}
	for _, cmd := range cmds {
		if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
			continue
		}
		// A game JukaHub stops did not fail to start (gamelaunch_watch.go).
		gameLaunchNoteStop(cmd)
		sent := false
		if screenChildGroupConfigured(cmd) {
			sent = syscall.Kill(-cmd.Process.Pid, s) == nil
		}
		if !sent {
			_ = cmd.Process.Signal(s)
		}
		_ = alarmSignalChild(cmd, false)
	}
}

// sessionSafeSignalTargets sends the polite signal to the emulator's own
// processes, each followed by a continue (one outside the group is not
// reached by the group's). A process that is gone, or whose pid now belongs
// to another program (a different start time), is skipped; JukaHub itself
// is never one.
func sessionSafeSignalTargets(targets []sessionSafeTarget, sig sessionSafeSignal) {
	s := syscall.SIGTERM
	if sig == sessionSafeSigInt {
		s = syscall.SIGINT
	}
	self := os.Getpid()
	for _, t := range targets {
		if t.pid == self || !sessionSafeTargetAlive(t) {
			continue
		}
		_ = syscall.Kill(t.pid, s)
		_ = syscall.Kill(t.pid, syscall.SIGCONT)
	}
}

// sessionSafeTargetAlive reports whether t still runs: the same pid with the
// same start time, and not a zombie (which writes nothing more).
func sessionSafeTargetAlive(t sessionSafeTarget) bool {
	if t.pid <= 1 {
		return false
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(t.pid) + "/stat")
	if err != nil {
		return false
	}
	st, ok := closeAllParseStat(data)
	return ok && st.start == t.start && st.state != 'Z' && st.state != 'X'
}
