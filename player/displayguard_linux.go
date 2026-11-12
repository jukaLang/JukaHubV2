//go:build linux

package main

// displayguard_linux.go — nothing else draws while JukaHub has the screen.
//
// Two programs presenting to one framebuffer is the TrimUI's flicker
// (gamesuspend.go): the panel shows each one's frames in turn. launch.sh
// stops the stock menu before JukaHub starts and the boot hook hands over
// through the firmware's own queue, but a tester on CrossMix still saw
// JukaHub flicker when it started with the device, and nothing on the device
// said which program was drawing beside it. So for the first minute after
// JukaHub starts, and for a minute after each game, this looks once a second
// (off the main goroutine) at what else is alive:
//
//   - a MainUI that is running while JukaHub owns the screen and no game
//     runs. It is logged once, with what started it ("display: MainUI pid 812
//     (parent runtrimui.sh pid 640, running) is drawing beside JukaHub"), and
//     on the handheld under launch.sh it is stopped by the same rules
//     launch.sh uses: its parent is paused by pid first when it is a shell
//     script — written down in /tmp/jukahub-paused, which launch.sh resumes
//     as it hands the screen back (here at exit under an older launch.sh) —
//     never init, never a daemon, never the loop that started launch.sh, which
//     is waiting for it; then SIGTERM, and SIGKILL a second later (MainUI
//     ignores SIGTERM). A MainUI that keeps coming back is left alone after
//     three stops: fighting a restarter only makes more flicker;
//   - any other process holding the display (the framebuffer, or the GPU's
//     nodes: displayGuardNodes), logged once per process and never touched —
//     a firmware daemon may keep the framebuffer open for screenshots, and a
//     CrossMix boot script's splash is gone a moment later. Another JukaHub
//     drawing is said as such.
//
// JukaHub's own children are not judged here (a game, the screen share's
// capture): the exit report below lists those still alive as the app quits,
// and stops the ones that hold the display or a Bluetooth session.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// displayGuardWindow is how long after a start or a game the guard looks.
	displayGuardWindow = time.Minute
	// displayGuardEvery is the shortest gap between two looks.
	displayGuardEvery = time.Second
	// displayGuardMaxStops bounds the MainUIs stopped in one session.
	displayGuardMaxStops = 3
	// displayGuardPausedRecord is launch.sh's paused record (its header).
	displayGuardPausedRecord = "/tmp/jukahub-paused"
)

// displayGuardNodes are the device nodes a process that draws holds open: the
// framebuffer, and the PowerVR GE8300's nodes that every GLES client opens
// (the DRM render and card nodes, its sync device); /dev/mali is there for a
// Mali board. A trailing slash matches a whole directory. The one list the
// guard and the exit report read.
var displayGuardNodes = []string{"/dev/fb0", "/dev/dri/", "/dev/pvr_sync", "/dev/mali"}

// displayGuardRadioNames are the helpers JukaHub starts that hold a Bluetooth
// session open; one left running after JukaHub would keep the radio scanning.
var displayGuardRadioNames = []string{"bluetoothctl", "btmgmt", "hcitool"}

// displayGuardProc is one process as /proc/<pid>/stat shows it.
type displayGuardProc struct {
	pid, ppid int
	state     string
	start     string
	comm      string
}

// displayGuardPaused is a launcher loop the guard paused itself.
type displayGuardPaused struct {
	pid   int
	start string
}

var displayGuard struct {
	mu       sync.Mutex
	started  bool
	until    time.Time
	last     time.Time
	wasGame  bool
	scanning bool
	stops    int
	reported map[string]bool
	paused   []displayGuardPaused
}

// displayGuardTick runs from widgetsTick (once a second, also while a game
// runs): it opens the window at the first tick and again when a game ends,
// and starts one look when the window is open and no look is running.
func displayGuardTick() {
	if !IsTrimuiDevice() {
		return
	}
	now := time.Now()
	game := gameScreenSuspended()
	displayGuard.mu.Lock()
	if !displayGuard.started {
		displayGuard.started = true
		displayGuard.until = now.Add(displayGuardWindow)
		displayGuard.reported = make(map[string]bool)
		Cleanup = append(Cleanup, displayGuardExit)
	}
	if displayGuard.wasGame && !game {
		displayGuard.until = now.Add(displayGuardWindow)
	}
	displayGuard.wasGame = game
	if game || displayGuard.scanning || now.After(displayGuard.until) || now.Sub(displayGuard.last) < displayGuardEvery {
		displayGuard.mu.Unlock()
		return
	}
	displayGuard.scanning = true
	displayGuard.last = now
	displayGuard.mu.Unlock()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("display", "the display check failed: %v", r)
			}
			displayGuard.mu.Lock()
			displayGuard.scanning = false
			displayGuard.mu.Unlock()
		}()
		displayGuardLook()
	}()
}

// displayGuardLook is one look at what else is alive.
func displayGuardLook() {
	self := os.Getpid()
	procs := displayGuardProcs()
	byPid := make(map[int]displayGuardProc, len(procs))
	for _, p := range procs {
		byPid[p.pid] = p
	}
	ancestors := displayGuardAncestors(self, byPid)
	ours := displayGuardDescendants(self, procs)
	for _, p := range procs {
		if p.pid == self || p.state == "Z" || ours[p.pid] || ancestors[p.pid] {
			continue
		}
		mainUI := p.comm == "MainUI"
		node := displayGuardHeldNode(p.pid)
		if !mainUI && node == "" {
			continue
		}
		key := strconv.Itoa(p.pid) + " " + p.start
		displayGuard.mu.Lock()
		seen := displayGuard.reported[key]
		displayGuard.reported[key] = true
		displayGuard.mu.Unlock()
		who := displayGuardDescribe(p, byPid)
		switch {
		case mainUI:
			if !seen {
				logWarn("display", "%s is drawing beside JukaHub", who)
			}
			displayGuardStopMainUI(p, byPid, ancestors)
		case seen:
		case p.comm == "JukaHub":
			logWarn("display", "another JukaHub, %s, holds %s beside this one", who, node)
		default:
			logWarn("display", "%s holds %s while JukaHub has the screen", who, node)
		}
	}
}

// displayGuardStopMainUI stops a MainUI that draws beside JukaHub, by the
// rules in the header. Only under launch.sh on the handheld: a JukaHub started
// by hand (over SSH) is a developer's, and the log line is enough there.
func displayGuardStopMainUI(p displayGuardProc, byPid map[int]displayGuardProc, ancestors map[int]bool) {
	if !launchedByDeviceLauncher() {
		return
	}
	displayGuard.mu.Lock()
	if displayGuard.stops >= displayGuardMaxStops {
		displayGuard.mu.Unlock()
		return
	}
	displayGuard.stops++
	last := displayGuard.stops == displayGuardMaxStops
	displayGuard.mu.Unlock()
	if parent, ok := byPid[p.ppid]; ok && p.ppid > 1 && !ancestors[p.ppid] && displayGuardPausable(parent) {
		if err := syscall.Kill(parent.pid, syscall.SIGSTOP); err == nil {
			displayGuardRecordPause(parent)
			logWarn("display", "paused %s, which started that MainUI; it runs again when JukaHub hands the screen back", displayGuardDescribe(parent, nil))
		}
	}
	_ = syscall.Kill(p.pid, syscall.SIGTERM)
	time.Sleep(time.Second)
	if cur, ok := displayGuardReadProc(p.pid); ok && cur.start == p.start && cur.state != "Z" {
		_ = syscall.Kill(p.pid, syscall.SIGKILL)
	}
	logWarn("display", "stopped MainUI pid %d", p.pid)
	if last {
		logWarn("display", "MainUI was stopped %d times this session; the next one is only logged", displayGuardMaxStops)
	}
}

// displayGuardRecordPause writes a paused loop down for launch.sh and for the
// exit hook.
func displayGuardRecordPause(p displayGuardProc) {
	displayGuard.mu.Lock()
	displayGuard.paused = append(displayGuard.paused, displayGuardPaused{pid: p.pid, start: p.start})
	displayGuard.mu.Unlock()
	f, err := os.OpenFile(displayGuardPausedRecord, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		logWarn("display", "could not write the paused record: %v", err)
		return
	}
	_, _ = fmt.Fprintf(f, "%d %s %s\n", p.pid, p.start, displayGuardField(p.comm))
	_ = f.Close()
}

// displayGuardExit is the Cleanup hook: the exit report, and the guard's own
// pauses let go when no launch.sh will do it.
func displayGuardExit() {
	self := os.Getpid()
	procs := displayGuardProcs()
	ours := displayGuardDescendants(self, procs)
	exe, _ := os.Executable()
	var alive, stopped []string
	for _, p := range procs {
		if !ours[p.pid] || p.state == "Z" {
			continue
		}
		// Started from JukaHub's own binary: the VPN tunnel, which follows the
		// launcher's session, or a restart Recovery asked for.
		if exe != "" {
			if target, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(p.pid), "exe")); err == nil && target == exe {
				continue
			}
		}
		name := fmt.Sprintf("%s (pid %d)", p.comm, p.pid)
		alive = append(alive, name)
		if displayGuardHeldNode(p.pid) != "" || displayGuardIsRadio(p.comm) {
			if syscall.Kill(p.pid, syscall.SIGTERM) == nil {
				stopped = append(stopped, name)
			}
		}
	}
	if len(alive) > 0 {
		logWarn("exit", "still running as JukaHub quits: %s", strings.Join(alive, ", "))
	}
	if len(stopped) > 0 {
		logWarn("exit", "stopped, as they held the display or the radio: %s", strings.Join(stopped, ", "))
	}
	if strings.TrimSpace(os.Getenv("JUKAHUB_PAUSED_RECORD")) == "1" {
		return
	}
	displayGuard.mu.Lock()
	paused := displayGuard.paused
	displayGuard.paused = nil
	displayGuard.mu.Unlock()
	for _, pp := range paused {
		if cur, ok := displayGuardReadProc(pp.pid); ok && cur.start == pp.start {
			_ = syscall.Kill(pp.pid, syscall.SIGCONT)
		}
	}
}

// displayGuardProcs reads every process's stat line.
func displayGuardProcs() []displayGuardProc {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	out := make([]displayGuardProc, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if p, ok := displayGuardReadProc(pid); ok {
			out = append(out, p)
		}
	}
	return out
}

// displayGuardReadProc reads one process's state, parent, start time and name.
// The name sits in brackets and may hold spaces or brackets itself, so the
// fields are read after its last ") ".
func displayGuardReadProc(pid int) (displayGuardProc, bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return displayGuardProc{}, false
	}
	return parseDisplayGuardStat(pid, string(raw))
}

// parseDisplayGuardStat parses a /proc/<pid>/stat line.
func parseDisplayGuardStat(pid int, line string) (displayGuardProc, bool) {
	open := strings.IndexByte(line, '(')
	end := strings.LastIndex(line, ") ")
	if open < 0 || end < open {
		return displayGuardProc{}, false
	}
	fields := strings.Fields(line[end+2:])
	// state, ppid, ... starttime is the 22nd field of the line, the 20th here.
	if len(fields) < 20 {
		return displayGuardProc{}, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return displayGuardProc{}, false
	}
	return displayGuardProc{pid: pid, ppid: ppid, state: fields[0], start: fields[19], comm: line[open+1 : end]}, true
}

// displayGuardHeldNode is the first display node pid holds open, or "".
func displayGuardHeldNode(pid int) string {
	dir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, node := range displayGuardNodes {
			if target == node || (strings.HasSuffix(node, "/") && strings.HasPrefix(target, node)) ||
				(node == "/dev/mali" && strings.HasPrefix(target, node)) {
				return target
			}
		}
	}
	return ""
}

// displayGuardAncestors is pid's chain of parents: launch.sh, and the loop
// that started it, which is waiting for it.
func displayGuardAncestors(pid int, byPid map[int]displayGuardProc) map[int]bool {
	out := make(map[int]bool)
	for i := 0; i < 32; i++ {
		p, ok := byPid[pid]
		if !ok || p.ppid <= 1 || out[p.ppid] {
			break
		}
		out[p.ppid] = true
		pid = p.ppid
	}
	return out
}

// displayGuardDescendants is every process below pid.
func displayGuardDescendants(pid int, procs []displayGuardProc) map[int]bool {
	children := make(map[int][]int, len(procs))
	for _, p := range procs {
		children[p.ppid] = append(children[p.ppid], p.pid)
	}
	out := make(map[int]bool)
	queue := []int{pid}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range children[cur] {
			if !out[c] && c != pid {
				out[c] = true
				queue = append(queue, c)
			}
		}
	}
	return out
}

// displayGuardPausable reports whether p is a shell script or the firmware's
// launcher binary, the only kind of parent ever paused (launch.sh's rule).
func displayGuardPausable(p displayGuardProc) bool {
	switch p.comm {
	case "sh", "ash", "bash", "dash", "busybox", "trimui_launcher":
		return true
	}
	if strings.HasSuffix(p.comm, ".sh") {
		return true
	}
	target, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(p.pid), "exe"))
	if err != nil {
		return false
	}
	switch filepath.Base(target) {
	case "sh", "ash", "bash", "dash", "busybox":
		return true
	}
	return false
}

// displayGuardIsRadio reports whether comm is a Bluetooth session helper.
func displayGuardIsRadio(comm string) bool {
	for _, name := range displayGuardRadioNames {
		if comm == name {
			return true
		}
	}
	return false
}

// displayGuardDescribe is p for the log, with up to two parents: "MainUI pid
// 812 (parent runtrimui.sh pid 640, running)". A shell shows the script it
// runs, since a busybox may call every script "sh".
func displayGuardDescribe(p displayGuardProc, byPid map[int]displayGuardProc) string {
	s := displayGuardName(p) + " pid " + strconv.Itoa(p.pid)
	if byPid == nil {
		return s
	}
	var chain []string
	cur := p
	for i := 0; i < 2; i++ {
		parent, ok := byPid[cur.ppid]
		if !ok || cur.ppid <= 1 {
			break
		}
		chain = append(chain, fmt.Sprintf("parent %s pid %d, %s", displayGuardName(parent), parent.pid, displayGuardStateWord(parent.state)))
		cur = parent
	}
	if len(chain) == 0 {
		return s + " (parent init)"
	}
	return s + " (" + strings.Join(chain, "; ") + ")"
}

// displayGuardName is a process's name, or the script a shell runs.
func displayGuardName(p displayGuardProc) string {
	switch p.comm {
	case "sh", "ash", "bash", "dash", "busybox":
	default:
		return displayGuardField(p.comm)
	}
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(p.pid), "cmdline"))
	if err != nil {
		return p.comm
	}
	args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	for _, a := range args[1:] {
		if a != "" && !strings.HasPrefix(a, "-") {
			return displayGuardField(filepath.Base(a))
		}
	}
	return p.comm
}

// displayGuardField keeps a name to one printable word for a log line or the
// paused record.
func displayGuardField(s string) string {
	s = strings.Map(func(r rune) rune {
		if r <= ' ' || r == 0x7f {
			return '_'
		}
		return r
	}, s)
	if len(s) > 48 {
		s = s[:48]
	}
	if s == "" {
		return "?"
	}
	return s
}

// displayGuardStateWord says a /proc state letter in words.
func displayGuardStateWord(state string) string {
	switch state {
	case "R":
		return "running"
	case "S", "D", "I":
		return "waiting"
	case "T", "t":
		return "paused"
	case "Z":
		return "a zombie"
	}
	return state
}
