package main

// portmaster_splash.go — PortMaster's start-up splash, and a launch whose
// launcher has ended while something it started still holds the screen.
//
// Testers on a TrimUI Brick (2026-11-20, a photo) were left on PortMaster's
// splash ("PM — Your Ultimate Handheld Linux Port Manager") for good. The
// splash is a LÖVE program that PortMaster's launcher starts in the
// background (utils/pmsplash, process love.<arch>) while its Python menu,
// pugwash, loads; pugwash ends it — it deletes utils/pmsplash/stopsplash —
// once SDL has given it a window (REG-Linux PortMaster-GUI #2, ArkOS #1281).
// Two ways that goes wrong leave the splash up:
//
//   - pugwash dies before that point: a Python error, a device check that
//     found no CPU architecture (ArkOS #1281: KeyError 'primary_arch'), a
//     screen it could not open. The launcher then ends, but the splash keeps
//     running in its process group, and JukaHub keeps the screen parked until
//     the whole group is gone (gamesuspend.go, up to 8 hours) — so the splash
//     stayed until the handheld was switched off;
//   - pugwash runs but the splash does not stop: on the framebuffer models
//     both draw to the same screen and LÖVE redraws every frame, so the menu
//     is hidden under it while it answers the buttons.
//
// So:
//
//   - the moment a launcher (PortMaster's or a port's) ends, the rest of its
//     group is looked at; when all that is left are helpers that only served
//     it — PortMaster's splash, a key mapper (gptokeyb), sdl2imgshow, portmsg,
//     PortMaster's tools/splash, and shells that only wait for those — they
//     are ended (SIGTERM, then SIGKILL) and the screen comes back at once.
//     Anything else left there (a game a script started in the background)
//     keeps the screen as before. After PortMaster a toast says it closed
//     before its menu appeared, with the cause its log.txt names;
//   - while PortMaster runs, a watch (once a second) ends the splash when
//     another program of the launch has had the screen open — a display
//     device it opened itself, not one inherited from JukaHub — for 5 seconds
//     with the splash still up: pugwash's own stop did not work. It ends the
//     whole launch when the splash has been up for 4 minutes with nothing else
//     on the screen (PortMaster's device check gives up after 60 s). Time the
//     launch spent stopped (the in-game menu, an alarm, sleep) is not counted;
//   - "Why didn't it open?", the launch log's end line and the bug report's
//     PortMaster section say what happened.
//
// PortMaster's files are not changed, and the splash still shows while
// pugwash loads.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// pmSplashPoll paces the watch.
	pmSplashPoll = time.Second
	// pmSplashCover is how long another program of the launch may have the
	// screen open with the splash still up before the splash is ended.
	pmSplashCover = 5 * time.Second
	// pmSplashGiveUp is how long the splash may be up with nothing else on
	// the screen before the launch is ended: well past the device check's
	// 60 s, with room for a slow first start.
	pmSplashGiveUp = 4 * time.Minute
	// pmSplashNoShow ends the watch when no splash appeared by then: this
	// PortMaster has none.
	pmSplashNoShow = time.Minute
	// portOrphanGrace is how long a launch's group may take to end by itself
	// after its launcher before what is left is looked at.
	portOrphanGrace = 1500 * time.Millisecond
	// pmSplashStopWait bounds the reaper's wait for the watch to stop.
	pmSplashStopWait = 8 * time.Second
	// pmSplashLabel names the splash in logs and notes.
	pmSplashLabel = "PortMaster's splash"
	// pmFdCloexec is O_CLOEXEC as /proc/PID/fdinfo prints it (octal flags).
	pmFdCloexec = 0o2000000
)

// What JukaHub had to do about a launch's splash.
const (
	pmSplashNone     = iota
	pmSplashCovered  // the splash was still drawing over a program that had the screen: ended
	pmSplashOrphaned // the launcher ended and left the splash (and helpers): ended
	pmSplashGaveUp   // the splash was up for pmSplashGiveUp with nothing else: the launch ended
)

// pmSplashLast is the last time JukaHub had to step in, for "Why didn't it
// open?" and the bug report.
var pmSplashLast struct {
	mu   sync.Mutex
	kind int
	note string
	at   time.Time
}

// pmSplashWatch follows one PortMaster launch while it runs
// (portmaster_splash_linux.go). A nil watch does nothing.
type pmSplashWatch struct {
	done chan struct{} // closed by stop
	over chan struct{} // closed when the watch has ended
	once sync.Once
	mu   sync.Mutex
	kind int
	note string
}

// stop ends the watch and waits for it (at most pmSplashStopWait: reading a
// process's /proc files can block while it is stuck on the card), so nothing
// it does overlaps the reaper's own clean-up; it returns what the watch did.
// Reaper goroutine.
func (w *pmSplashWatch) stop() (int, string) {
	if w == nil {
		return pmSplashNone, ""
	}
	w.once.Do(func() { close(w.done) })
	select {
	case <-w.over:
	case <-time.After(pmSplashStopWait):
		logWarn("portmaster", "the splash watch did not stop within %s; going on without it", pmSplashStopWait)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.kind, w.note
}

// set records what the watch did (and keeps it for the reports).
func (w *pmSplashWatch) set(kind int, note string) {
	w.mu.Lock()
	w.kind, w.note = kind, note
	w.mu.Unlock()
	pmSplashRecord(kind, note)
}

// pmSplashRecord keeps what JukaHub did about a splash and logs it.
func pmSplashRecord(kind int, note string) {
	if kind == pmSplashNone || note == "" {
		return
	}
	pmSplashLast.mu.Lock()
	pmSplashLast.kind, pmSplashLast.note, pmSplashLast.at = kind, note, time.Now()
	pmSplashLast.mu.Unlock()
	logInfo("portmaster", "%s", note)
}

// pmSplashForget drops the last note: PortMaster has since opened and closed
// without JukaHub having to step in.
func pmSplashForget() {
	pmSplashLast.mu.Lock()
	pmSplashLast.kind, pmSplashLast.note, pmSplashLast.at = pmSplashNone, "", time.Time{}
	pmSplashLast.mu.Unlock()
}

// pmSplashLastNote is the last note within the given time, with its kind.
func pmSplashLastNote(within time.Duration) (int, string, time.Time) {
	pmSplashLast.mu.Lock()
	defer pmSplashLast.mu.Unlock()
	if pmSplashLast.note == "" || time.Since(pmSplashLast.at) > within {
		return pmSplashNone, "", time.Time{}
	}
	return pmSplashLast.kind, pmSplashLast.note, pmSplashLast.at
}

// pmProcBase is the last element of a Linux path. Pure.
func pmProcBase(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// pmSplashNamed reports a path or argument that names PortMaster's splash
// folder (utils/pmsplash, or a pmsplash.love). Pure.
func pmSplashNamed(s string) bool {
	return strings.Contains(strings.ToLower(s), "pmsplash")
}

// pmSplashMatch reports PortMaster's LÖVE splash: a LÖVE program (its name,
// or one of a loader's first arguments, starts with "love") whose command
// line, program file or working folder names pmsplash. A LÖVE port may run
// the very same love.<arch>, but on its own folder, so the folder decides.
// Pure.
func pmSplashMatch(comm string, argv []string, exe, cwd string) bool {
	love := strings.HasPrefix(strings.ToLower(comm), "love")
	for i, a := range argv {
		if i > 2 {
			break
		}
		if strings.HasPrefix(strings.ToLower(pmProcBase(a)), "love") {
			love = true
		}
	}
	if !love {
		return false
	}
	if pmSplashNamed(exe) || pmSplashNamed(cwd) {
		return true
	}
	for i, a := range argv {
		if i > 5 {
			break
		}
		if pmSplashNamed(a) {
			return true
		}
	}
	return false
}

// portHelperLabel names a process that only serves a launch and has no
// reason to outlive its launcher: PortMaster's splash, a key mapper, an
// image or message shown while the launch loads. Pure.
func portHelperLabel(comm string, argv []string, exe, cwd string) (string, bool) {
	if pmSplashMatch(comm, argv, exe, cwd) {
		return pmSplashLabel, true
	}
	switch comm {
	case "gptokeyb", "gptokeyb2", "oga_controls", "sdl2imgshow":
		return comm, true
	}
	if strings.HasPrefix(comm, "portmsg") {
		return "portmsg", true
	}
	if comm == "splash" {
		first := ""
		if len(argv) > 0 {
			first = argv[0]
		}
		if strings.HasSuffix(exe, "/tools/splash") || strings.HasSuffix(first, "/tools/splash") {
			return "PortMaster's tools/splash", true
		}
	}
	return "", false
}

// portShellProc reports a shell: by its name, or — for a program started
// through a loader, as JukaHub's bash on the private glibc is (ld.so
// --library-path … --argv0 … bash; /proc shows the loader's own command line
// and comm, also in a subshell it forks) — by the program the loader runs.
// Pure.
func portShellProc(comm string, argv []string) bool {
	names := []string{comm}
	if len(argv) > 0 {
		names = append(names, pmProcBase(argv[0]))
		if strings.HasPrefix(comm, "ld-") || strings.HasPrefix(pmProcBase(argv[0]), "ld-") {
			names = append(names, pmLoaderTarget(argv)...)
		}
	}
	for _, n := range names {
		switch strings.TrimPrefix(n, "-") {
		case "sh", "bash", "ash", "dash", "busybox":
			return true
		}
	}
	return false
}

// pmLoaderTarget is what a dynamic loader's command line runs: the name its
// --argv0 gives and the base name of the program (the first argument that is
// not an option or an option's value). Pure.
func pmLoaderTarget(argv []string) []string {
	var out []string
	for i := 1; i < len(argv) && i < 16; i++ {
		switch a := argv[i]; a {
		case "--argv0":
			if i+1 < len(argv) {
				out = append(out, pmProcBase(argv[i+1]))
			}
			i++
		case "--library-path", "--preload", "--audit", "--inhibit-rpath", "--glibc-hwcaps-prepend", "--glibc-hwcaps-mask":
			i++
		default:
			if strings.HasPrefix(a, "--") {
				continue
			}
			return append(out, pmProcBase(a))
		}
	}
	return out
}

// portOrphan is a process still in a launch's group after its launcher ended.
// outKids counts its live children outside the group.
type portOrphan struct {
	pid, ppid int
	start     uint64
	comm      string
	argv      []string
	exe, cwd  string
	outKids   int
}

// portSameProcs reports whether two picks name the same processes. Pure.
func portSameProcs(a, b []closeAllProc) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[closeAllProc]bool, len(a))
	for _, p := range a {
		seen[closeAllProc{pid: p.pid, start: p.start}] = true
	}
	for _, p := range b {
		if !seen[closeAllProc{pid: p.pid, start: p.start}] {
			return false
		}
	}
	return true
}

// portOrphanPick decides, all or nothing, what to end once a launcher has
// ended: every process left must be a helper (portHelperLabel) whose own
// children here are helpers too, or a shell with children here that all are
// (a `( … ) &` that waits for the splash) — and none may have a child outside
// the group (a game moved to a session of its own). Then all of them are
// returned, with the helpers' labels. One that is neither — a game a script
// started in the background — leaves everything as it is: nil. Pure.
func portOrphanPick(left []portOrphan) ([]closeAllProc, []string) {
	if len(left) == 0 {
		return nil, nil
	}
	byPid := make(map[int]portOrphan, len(left))
	for _, o := range left {
		byPid[o.pid] = o
	}
	kids := map[int][]int{}
	for _, o := range left {
		if _, ok := byPid[o.ppid]; ok && o.ppid != o.pid {
			kids[o.ppid] = append(kids[o.ppid], o.pid)
		}
	}
	memo := map[int]bool{}
	var helper func(pid, depth int) bool
	helper = func(pid, depth int) bool {
		if v, ok := memo[pid]; ok {
			return v
		}
		memo[pid] = false // a loop (impossible in a process tree) is not a helper
		o := byPid[pid]
		_, isHelper := portHelperLabel(o.comm, o.argv, o.exe, o.cwd)
		ok := depth < 8 && o.outKids == 0 && (isHelper || (portShellProc(o.comm, o.argv) && len(kids[pid]) > 0))
		for _, k := range kids[pid] {
			if !ok {
				break
			}
			ok = helper(k, depth+1)
		}
		memo[pid] = ok
		return ok
	}
	for _, o := range left {
		if !helper(o.pid, 0) {
			return nil, nil
		}
	}
	procs := make([]closeAllProc, 0, len(left))
	var labels []string
	for _, o := range left {
		label, isHelper := portHelperLabel(o.comm, o.argv, o.exe, o.cwd)
		if !isHelper {
			label = setOr(o.comm, "shell")
		} else {
			labels = append(labels, label+" ("+strconv.Itoa(o.pid)+")")
		}
		procs = append(procs, closeAllProc{pid: o.pid, start: o.start, label: label})
	}
	if len(labels) == 0 {
		return nil, nil
	}
	return procs, labels
}

// pmDisplayDevice reports a device file a program draws through: the
// framebuffer, DRM/KMS, a GPU driver's own node (PowerVR on the A133, Mali,
// Vivante) or Allwinner's display engine. Pure.
func pmDisplayDevice(p string) bool {
	for _, pre := range []string{"/dev/fb", "/dev/dri/", "/dev/pvr", "/dev/mali", "/dev/galcore", "/dev/disp"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// pmFdInfoCloexec reports a /proc/PID/fdinfo text whose flags have
// O_CLOEXEC. Unreadable flags count as not close-on-exec. Pure.
func pmFdInfoCloexec(info []byte) bool {
	for _, line := range strings.Split(string(info), "\n") {
		if rest, ok := strings.CutPrefix(line, "flags:"); ok {
			v, err := strconv.ParseUint(strings.TrimSpace(rest), 8, 64)
			return err == nil && v&pmFdCloexec != 0
		}
	}
	return false
}

// pmLaunchProcName is a short name for a program of a launch, for the notes:
// pugwash for PortMaster's menu (it runs as python3 ./pugwash), else its
// name. Pure.
func pmLaunchProcName(comm string, argv []string) string {
	for i, a := range argv {
		if i > 3 {
			break
		}
		if b := pmProcBase(a); b == "pugwash" || b == "pugwash.py" {
			return "PortMaster's menu (pugwash)"
		}
	}
	return setOr(comm, "a program")
}

// portOrphanNote is the launch log's and JukaHub log's line for helpers
// ended after their launcher. Pure.
func portOrphanNote(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	return "JukaHub ended what the launcher left on the screen: " + strings.Join(labels, ", ")
}

// pmSplashLeft reports whether helper labels include PortMaster's splash.
// Pure.
func pmSplashLeft(labels []string) bool {
	for _, l := range labels {
		if strings.HasPrefix(l, pmSplashLabel) {
			return true
		}
	}
	return false
}

// pmSplashExitToast is what the end of PortMaster says when JukaHub had to
// end its splash or the launch ("" when it did not): with the cause
// PortMaster's own log.txt names, when it names one. Off the main thread.
func pmSplashExitToast(kind int) string {
	if kind != pmSplashGaveUp && kind != pmSplashOrphaned {
		return ""
	}
	cause := ""
	if own := pmOwnLogPath(); own != "" {
		if info, err := os.Stat(own); err == nil && time.Since(info.ModTime()) <= pmSplashGiveUp+2*time.Minute {
			if issues := pmOwnLogFindings(pmTailLines(own, 40)); len(issues) > 0 {
				cause = ": " + issues[0].Title
			}
		}
	}
	tail := " — " + hintKeyFor("Y", "Options") + " on its row, then Why didn't it open?"
	switch kind {
	case pmSplashGaveUp:
		return fmt.Sprintf("PortMaster's menu had not appeared after %d minutes, so JukaHub closed it", int(pmSplashGiveUp/time.Minute)) + cause + tail
	case pmSplashOrphaned:
		return "PortMaster closed before its menu appeared" + cause + tail
	}
	return "" // a kind without a toast never gets here
}

// pmSplashIssue is "Why didn't it open?"'s line about the last time JukaHub
// had to step in, if that was in the last day.
func pmSplashIssue() []launchIssue {
	kind, note, at := pmSplashLastNote(pmReportRecent)
	if note == "" {
		return nil
	}
	issue := launchIssue{Level: int(gameDiagLikely), Detail: note + " (" + timeAgo(at) + ")."}
	switch kind {
	case pmSplashCovered:
		issue.Level = int(gameDiagMaybe)
		issue.Title = "Its splash stayed over the menu"
		issue.Detail += " PortMaster normally ends its splash itself once its menu has the screen."
	case pmSplashGaveUp:
		issue.Title = "Its menu never appeared"
		issue.Detail += " PortMaster's log.txt below says how far it got."
	default:
		issue.Title = "It closed before its menu appeared"
		issue.Detail += " PortMaster's log.txt below says why its menu stopped."
	}
	return []launchIssue{issue}
}
