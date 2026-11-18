//go:build linux

package main

// ports_cleanup_linux.go — what a port leaves behind once its process group
// is gone. A port script starts its key mapper (gptokeyb / gptokeyb2, or the
// older oga_controls) in the background and kills it on the way out; a
// WestonPack port starts westonwrap, Weston and Xwayland, some of them in a
// session of their own. A port that crashed, or that the player ended with
// Select + Start, never reaches those last lines: the mapper keeps turning
// the pad into keys for nobody (and into keys for JukaHub), and a compositor
// left running keeps the display. The reaper calls this once no screen child
// is alive; only these helper names are touched, never JukaHub's own group,
// its ancestors or a firmware loop launch.sh paused.

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// portLeftoverNames are the helpers a port starts (comm, the kernel's
// 15-character name).
var portLeftoverNames = map[string]string{
	"gptokeyb":     "gptokeyb",
	"gptokeyb2":    "gptokeyb2",
	"oga_controls": "oga_controls",
	"weston":       "Weston",
	"Xwayland":     "Xwayland",
}

// portLeftoverLabel names pid's program when it is a port helper, from its
// comm and command line (westonwrap.sh runs under a shell). Pure.
func portLeftoverLabel(comm string, argv []string) (string, bool) {
	if label, ok := portLeftoverNames[comm]; ok {
		return label, true
	}
	for i, a := range argv {
		if i > 2 {
			break
		}
		if strings.Contains(strings.ToLower(a), "westonwrap") {
			return "westonwrap", true
		}
	}
	return "", false
}

// portCleanupLeftovers ends the helpers a port left (SIGTERM, then SIGKILL
// after a second). Reaper goroutine; returns the labels it ended.
func portCleanupLeftovers() []string {
	if !IsTrimuiDevice() || len(liveScreenChildren()) > 0 {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	self := os.Getpid()
	own := syscall.Getpgrp()
	all := make(map[int]closeAllStat, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
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
	never := closeAllAncestors(all, self)
	for _, pid := range closeAllPausedPids() {
		never[pid] = true
	}
	// A Wine program still running (JukaHub's Windows apps run under Box64
	// inside their own Weston) keeps its compositor.
	wine := false
	for _, st := range all {
		if st.comm == "box64" || strings.HasPrefix(st.comm, "wine") {
			wine = true
			break
		}
	}
	var procs []closeAllProc
	var labels []string
	for pid, st := range all {
		if never[pid] || pid == self || st.kernel() || (own > 1 && st.pgid == own) || st.state == 'Z' {
			continue
		}
		data, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		label, ok := portLeftoverLabel(st.comm, closeAllSplitCmdline(data))
		if !ok || (wine && label != "gptokeyb" && label != "gptokeyb2" && label != "oga_controls") {
			continue
		}
		procs = append(procs, closeAllProc{pid: pid, start: st.start, label: label})
		labels = append(labels, label+" ("+strconv.Itoa(pid)+")")
	}
	if len(procs) == 0 {
		return nil
	}
	logInfo("portmaster", "the port left %s running; ending them", strings.Join(labels, ", "))
	closeAllSignalProcs(procs, false)
	deadline := time.Now().Add(time.Second)
	for closeAllProcsAlive(procs) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if closeAllProcsAlive(procs) {
		closeAllSignalProcs(procs, true)
	}
	return labels
}
