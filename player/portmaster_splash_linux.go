//go:build linux

package main

// portmaster_splash_linux.go — the /proc side of portmaster_splash.go: what is
// left of a launch once its launcher ended, and the watch over PortMaster's
// splash while PortMaster runs.

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// pmProcArgv is pid's command line (nil when it cannot be read).
func pmProcArgv(pid int) []string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil
	}
	return closeAllSplitCmdline(data)
}

// pmProcLinks are pid's program file and working folder ("" when unknown).
func pmProcLinks(pid int) (exe, cwd string) {
	p := "/proc/" + strconv.Itoa(pid)
	exe, _ = os.Readlink(p + "/exe")
	cwd, _ = os.Readlink(p + "/cwd")
	return exe, cwd
}

// pmProcStats is every user process's stat, by PID, without JukaHub itself,
// kernel threads and zombies.
func pmProcStats() map[int]closeAllStat {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	self := os.Getpid()
	out := make(map[int]closeAllStat, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 || pid == self {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		st, ok := closeAllParseStat(data)
		if !ok || st.kernel() || st.state == 'Z' || st.state == 'X' {
			continue
		}
		out[pid] = st
	}
	return out
}

// portGroupLeft is what still runs in process group pgid, each with the
// number of its children outside the group.
func portGroupLeft(pgid int) []portOrphan {
	all := pmProcStats()
	outKids := map[int]int{}
	for _, st := range all {
		if st.pgid != pgid {
			outKids[st.ppid]++
		}
	}
	var left []portOrphan
	for pid, st := range all {
		if st.pgid != pgid {
			continue
		}
		exe, cwd := pmProcLinks(pid)
		left = append(left, portOrphan{pid: pid, ppid: st.ppid, start: st.start, comm: st.comm,
			argv: pmProcArgv(pid), exe: exe, cwd: cwd, outKids: outKids[pid]})
	}
	return left
}

// portEndOrphanHelpers runs on the reaper goroutine the moment a port's or
// PortMaster's launcher has exited, before the screen hand-back waits for its
// process group: when all that is left there are helpers (portOrphanPick),
// the same ones in two looks 300 ms apart (a subshell may be about to start
// a game), they are ended, so the splash or a key mapper does not keep the
// screen parked. It returns their labels.
func portEndOrphanHelpers(cmd *exec.Cmd) []string {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 1 || !IsTrimuiDevice() || !screenChildGroupConfigured(cmd) {
		return nil
	}
	for deadline := time.Now().Add(portOrphanGrace); screenChildGroupAlive(cmd) && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
	}
	var prev []closeAllProc
	for look := 0; look < 4; look++ {
		if look > 0 {
			time.Sleep(300 * time.Millisecond)
		}
		if !screenChildGroupAlive(cmd) {
			return nil
		}
		procs, labels := portOrphanPick(portGroupLeft(cmd.Process.Pid))
		if len(procs) == 0 {
			return nil // something that is not a helper is still running
		}
		if look > 0 && portSameProcs(prev, procs) {
			logInfo("portmaster", "launcher %d ended; ending what it left in its group: %s", cmd.Process.Pid, strings.Join(labels, ", "))
			closeAllEndProcs(procs)
			return labels
		}
		prev = procs
	}
	return nil // still changing: left as it is
}

// pmDisplayInherited is JukaHub's own display descriptors a child inherits
// (no close-on-exec), as "fd path" keys: a program of a launch that holds one
// of these did not open the screen itself.
func pmDisplayInherited() map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return out
	}
	for _, e := range entries {
		link, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err != nil || !pmDisplayDevice(link) {
			continue
		}
		info, err := os.ReadFile("/proc/self/fdinfo/" + e.Name())
		if err != nil || !pmFdInfoCloexec(info) {
			out[e.Name()+" "+link] = true
		}
	}
	return out
}

// pmHoldsDisplay reports whether pid has a display device open that it did
// not inherit from JukaHub.
func pmHoldsDisplay(pid int, inherited map[string]bool) bool {
	dir := "/proc/" + strconv.Itoa(pid) + "/fd"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for i, e := range entries {
		if i >= 512 {
			break
		}
		link, err := os.Readlink(dir + "/" + e.Name())
		if err == nil && pmDisplayDevice(link) && !inherited[e.Name()+" "+link] {
			return true
		}
	}
	return false
}

// pmLaunchScan is one look at a running PortMaster launch.
type pmLaunchScan struct {
	splash []closeAllProc // PortMaster's splash, in the launch's group or not
	holder closeAllProc   // the lowest-PID program of the launch with the screen open itself (not the splash, not a shell)
	paused bool           // the launch is stopped (in-game menu, alarm, sleep)
}

// pmScanLaunch looks at the processes of group pgid, and for PortMaster's
// splash anywhere (a launcher may start it in a session of its own).
func pmScanLaunch(pgid int, inherited map[string]bool) pmLaunchScan {
	var s pmLaunchScan
	for pid, st := range pmProcStats() {
		inGroup := st.pgid == pgid
		if !inGroup && !strings.HasPrefix(strings.ToLower(st.comm), "love") {
			continue
		}
		argv := pmProcArgv(pid)
		exe, cwd := pmProcLinks(pid)
		stopped := st.state == 'T' || st.state == 't'
		if pmSplashMatch(st.comm, argv, exe, cwd) {
			s.splash = append(s.splash, closeAllProc{pid: pid, start: st.start, label: pmSplashLabel})
			s.paused = s.paused || (inGroup && stopped)
			continue
		}
		if !inGroup {
			continue
		}
		s.paused = s.paused || stopped
		if (s.holder.pid == 0 || pid < s.holder.pid) && !portShellProc(st.comm, argv) && pmHoldsDisplay(pid, inherited) {
			s.holder = closeAllProc{pid: pid, start: st.start, label: pmLaunchProcName(st.comm, argv)}
		}
	}
	return s
}

// pmSplashWatchStart starts the watch over PortMaster's splash for a
// PortMaster launch that has just started (portmaster_splash.go).
func pmSplashWatchStart(cmd *exec.Cmd) *pmSplashWatch {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 1 || !IsTrimuiDevice() || !screenChildGroupConfigured(cmd) {
		return nil
	}
	w := &pmSplashWatch{done: make(chan struct{}), over: make(chan struct{})}
	inherited := pmDisplayInherited()
	go w.run(cmd, inherited)
	return w
}

// run is the watch: once a second until stop, or until the splash has gone,
// was ended, or never showed.
func (w *pmSplashWatch) run(cmd *exec.Cmd, inherited map[string]bool) {
	defer close(w.over)
	defer func() {
		if r := recover(); r != nil {
			logWarn("portmaster", "watching PortMaster's splash failed: %v", r)
		}
	}()
	pgid := cmd.Process.Pid
	t := time.NewTicker(pmSplashPoll)
	defer t.Stop()
	var up, waited, holderFor time.Duration // counted in ticks, never while stopped
	var holder closeAllProc
	seen := false
	for {
		select {
		case <-w.done:
			return
		case <-t.C:
		}
		s := pmScanLaunch(pgid, inherited)
		if s.paused {
			continue
		}
		if len(s.splash) == 0 {
			waited += pmSplashPoll
			if seen || waited >= pmSplashNoShow {
				return // PortMaster ended its splash, or this one has none
			}
			continue
		}
		seen = true
		// While some program of the launch has the screen open, the splash is
		// covering it; only a splash with nothing else counts towards the
		// give-up.
		if s.holder.pid != 0 {
			if holder.pid == 0 {
				holder = s.holder
			}
			holderFor += pmSplashPoll
			if holderFor >= pmSplashCover {
				w.set(pmSplashCovered, fmt.Sprintf("%s was still drawing over %s, which had had the screen for %ds; JukaHub ended the splash",
					pmSplashLabel, holder.label, int(pmSplashCover/time.Second)))
				closeAllEndProcs(s.splash)
				return
			}
			continue
		}
		holder, holderFor = closeAllProc{}, 0
		up += pmSplashPoll
		if up >= pmSplashGiveUp {
			w.set(pmSplashGaveUp, fmt.Sprintf("%s was up for %d minutes and nothing else of PortMaster had the screen; JukaHub ended the launch",
				pmSplashLabel, int(pmSplashGiveUp/time.Minute)))
			_ = screenChildSignal(cmd, screenSignalInterrupt)
			closeAllEndProcs(s.splash)
			// Not cut short by stop(): the group's kill below must follow, or
			// a menu that ignored the interrupt keeps the screen parked.
			for deadline := time.Now().Add(3 * time.Second); screenChildGroupAlive(cmd) && time.Now().Before(deadline); {
				time.Sleep(100 * time.Millisecond)
			}
			if screenChildGroupAlive(cmd) {
				_ = screenChildKill(cmd)
			}
			return
		}
	}
}
