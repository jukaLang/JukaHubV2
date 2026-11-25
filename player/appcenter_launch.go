package main

// appcenter_launch.go — starting a TrimUI app (with a file, for "Open with")
// and coming back to JukaHub cleanly when it exits.
//
// The start is the Apps screen's hand-off (apps.go launchInstalledApp): the
// app leads its own process group with the firmware's environment
// (screenChildGroupConfigure), JukaHub lets go of the sound device and, on a
// KMSDRM display, of DRM master (screenChildStart), and parks its main loop
// while anything in that group is alive (screenChildReaped). Around it, the
// App Center remembers what an app may leave changed and puts it back:
//
//   - the CPU governor of every core and, with Auto performance off, its
//     frequency ceiling (a reader or a recorder may pin them). The voice
//     boost and Auto's lowered hub ceiling give the CPU back first, as for
//     every launch; with Auto on, the ceiling is Auto's, which follows the
//     app as its own session;
//   - TrimUI's input daemon, trimui_inputd: an app that stops it to read the
//     sticks itself leaves them dead for JukaHub, so a daemon that ran before
//     and is gone after is started again, in its own process group;
//   - the stock menu: an app whose launch script starts MainUI as it exits
//     leaves it in the app's own process group, where it would keep JukaHub's
//     screen parked for hours; an input daemon it restarted with "&" does the
//     same. For up to three seconds after the app's script has ended, a MainUI
//     in the group is stopped, and so is an input daemon once nothing else of
//     the app is left (it is started again on its own), and JukaHub takes the
//     screen back. (A MainUI started any other way is the display guard's,
//     displayguard_linux.go, which looks for a minute after every child.)
//
// Brightness and volume are left alone: the firmware keeps them, and the
// apps in the catalogue do not change them.
//
// Every exit is one log line, "appcenter: <app> exited (code, seconds)", plus
// what was put back. A quick failure, or anything put back, is said in a
// toast too.
//
// A Background app (the Screen Recorder) starts a helper and returns at once:
// it is started without the hand-off, so JukaHub stays on screen.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// acQuickExit is how soon an exit with an error counts as "did not start".
const acQuickExit = 3 * time.Second

// acInputDaemon is the stock input daemon and where the firmware keeps it.
const (
	acInputDaemonName = "trimui_inputd"
	acInputDaemonPath = "/usr/trimui/bin/trimui_inputd"
)

// acSnapshot is what an app may change, as it was before the app started.
type acSnapshot struct {
	governors []cpuGovernorSave
	ceilings  []cpuFrequencySave
	inputd    bool
}

// acSnapshotTake reads the state (a few sysfs files and one /proc walk). The
// caller has given the CPU back first (voiceBoostEnd, autoPerfLaunchBegin), so
// what it records is the hub's own setting. With Auto performance on, the
// ceilings are Auto's to move: it follows the app as its own session and puts
// its ceiling back itself (autoperf.go), so they are not recorded — restoring
// them after the app would undo Auto's next write and make it look foreign.
// The same holds with a CPU frequency set by hand (cpufreq_manual.go), which
// Auto's sessions hold whether Automatic CPU speed is on or off.
func acSnapshotTake() acSnapshot {
	var s acSnapshot
	paths := cpuGovernorPaths(cpufreqRoot)
	s.governors = readGovernorSaves(paths)
	if autoPerfSupported() && (autoPerfOn.Load() || cpuFreqSetKHz.Load() > 0) {
		paths = nil
	}
	for _, p := range paths {
		maxPath := filepath.Join(filepath.Dir(p), "scaling_max_freq")
		if raw, err := os.ReadFile(maxPath); err == nil {
			if v := strings.TrimSpace(string(raw)); v != "" {
				s.ceilings = append(s.ceilings, cpuFrequencySave{path: maxPath, value: v})
			}
		}
	}
	s.inputd = procNameRunning([]string{acInputDaemonName}) != ""
	return s
}

// acSnapshotRestore puts back what differs and names what it put back.
func acSnapshotRestore(s acSnapshot) []string {
	var restored []string
	changed := false
	for _, g := range s.governors {
		if raw, err := os.ReadFile(g.path); err == nil && strings.TrimSpace(string(raw)) != g.value {
			changed = true
			break
		}
	}
	if changed {
		if err := restoreGovernors(s.governors); err != nil {
			logWarn("appcenter", "putting the CPU governor back: %v", err)
		} else {
			restored = append(restored, "CPU governor")
		}
	}
	ceilings := 0
	for _, c := range s.ceilings {
		raw, err := os.ReadFile(c.path)
		if err != nil || strings.TrimSpace(string(raw)) == c.value {
			continue
		}
		if err := os.WriteFile(c.path, []byte(c.value+"\n"), 0o644); err != nil {
			logWarn("appcenter", "putting %s back: %v", c.path, err)
			continue
		}
		ceilings++
	}
	if ceilings > 0 {
		restored = append(restored, "CPU speed")
	}
	if s.inputd && procNameRunning([]string{acInputDaemonName}) == "" {
		if info, err := os.Stat(acInputDaemonPath); err == nil && info.Mode().IsRegular() {
			cmd := exec.Command(acInputDaemonPath)
			cmd.Dir = filepath.Dir(acInputDaemonPath)
			// Its own process group: never the app's, whose leftovers are
			// stopped, and never JukaHub's screen hand-off.
			processGroupConfigure(cmd)
			// The firmware's library path and PATH, not JukaHub's.
			cmd.Env = screenChildEnv(os.Environ())
			if err := cmd.Start(); err != nil {
				logWarn("appcenter", "starting %s again: %v", acInputDaemonName, err)
			} else {
				restored = append(restored, "stick input")
				go func() { _ = cmd.Wait() }()
			}
		}
	}
	return restored
}

// acStrayWait bounds how long, after the app's script ended, the reaper keeps
// looking for a MainUI or input daemon the app left in its process group, and
// acStrayPoll how often it looks.
const (
	acStrayWait = 3 * time.Second
	acStrayPoll = 200 * time.Millisecond
)

// acStopStraysInGroup stops what an exiting app leaves in its process group
// that would keep JukaHub's screen parked (screenChildReaped waits for the
// whole group, up to hours): a MainUI its script started, and a
// trimui_inputd it started again with "&". The input daemon is only stopped
// when nothing but these two is left of the app — a helper of the app that
// still runs may need it — and acSnapshotRestore starts it again in its own
// group. screenChildGroupNames lists the group as "name(pid) …"; the names
// are the kernel's 15-character ones.
func acStopStraysInGroup(cmd *exec.Cmd) (menu, inputd bool) {
	type proc struct {
		name string
		pid  int
	}
	var procs []proc
	others := false
	for _, part := range strings.Fields(screenChildGroupNames(cmd)) {
		open := strings.LastIndexByte(part, '(')
		if open <= 0 || !strings.HasSuffix(part, ")") {
			if part != "…" {
				others = true
			}
			continue
		}
		pid, err := strconv.Atoi(part[open+1 : len(part)-1])
		if err != nil || pid <= 1 || pid == os.Getpid() {
			continue
		}
		name := part[:open]
		if name != "MainUI" && name != acInputDaemonName {
			others = true
			continue
		}
		procs = append(procs, proc{name, pid})
	}
	for _, p := range procs {
		if p.name == acInputDaemonName && others {
			continue
		}
		pr, err := os.FindProcess(p.pid)
		if err != nil {
			continue
		}
		// MainUI ignores SIGTERM (displayguard_linux.go): both are killed.
		if err := pr.Kill(); err != nil {
			continue
		}
		if p.name == "MainUI" {
			menu = true
			logInfo("appcenter", "stopped MainUI pid %d that the app's script started", p.pid)
		} else {
			inputd = true
			logInfo("appcenter", "stopped %s pid %d that the app left in its process group; it is started again on its own", acInputDaemonName, p.pid)
		}
	}
	return menu, inputd
}

// acReapStrays is the reaper's look after the app's script ended: while the
// group lives, for up to acStrayWait, it stops the strays
// acStopStraysInGroup names. Anything else of the app still running is then
// waited for by screenChildReaped, as for any launch.
func acReapStrays(cmd *exec.Cmd) (menu bool) {
	deadline := time.Now().Add(acStrayWait)
	for {
		m, _ := acStopStraysInGroup(cmd)
		menu = menu || m
		if !screenChildGroupAlive(cmd) || !time.Now().Before(deadline) {
			return menu
		}
		time.Sleep(acStrayPoll)
	}
}

// acWatchExecMenu stops the app's own process when its script ends with
// "exec MainUI": the script's process then is the menu, and its Wait would
// only return when the menu quits. It looks once a second until stop closes.
func acWatchExecMenu(cmd *exec.Cmd, stop <-chan struct{}) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	comm := "/proc/" + strconv.Itoa(cmd.Process.Pid) + "/comm"
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		raw, err := os.ReadFile(comm)
		if err != nil {
			return
		}
		if strings.TrimSpace(string(raw)) == "MainUI" {
			logInfo("appcenter", "the app's script became MainUI (pid %d); stopping it to take the screen back", cmd.Process.Pid)
			// JukaHub ends it, not a crash: Verified records no failure.
			gameLaunchNoteStop(cmd)
			_ = cmd.Process.Kill()
			return
		}
	}
}

// acLaunch starts an installed app, with file as its first argument when
// file is not "". It reports whether the app started. Main thread.
func acLaunch(in acInstalled, file string) bool {
	if !acHandheld() {
		ShowToast(acBrowseOnlyText(), ToastKindInfo)
		return false
	}
	name := in.Label
	clean, err := cleanUserPath(in.Launch)
	if err != nil || !isLaunchableAppPath(clean) || !appLaunchPathRE.MatchString(clean) {
		logWarn("appcenter", "security: refusing to launch %q: outside the app folders", in.Launch)
		ShowToast("Refusing to launch an app outside the app folders", ToastKindError)
		return false
	}
	if !isPathWithin(clean, in.Dir) {
		logWarn("appcenter", "security: %q is not inside %s", clean, in.Dir)
		ShowToast("Refusing to launch "+name+": its launch script is outside its folder", ToastKindError)
		return false
	}
	info, err := os.Stat(clean)
	if err != nil || !info.Mode().IsRegular() {
		ShowToast("Could not launch "+name+" — its launch script is gone", ToastKindError)
		return false
	}
	var args []string
	if file != "" {
		f, ferr := cleanUserPath(file)
		if ferr == nil {
			// Absolute: the app runs from its own folder, and a name that
			// starts with "-" can never read as an option.
			f, ferr = filepath.Abs(f)
		}
		if ferr != nil || strings.ContainsAny(f, "\x00\r\n") {
			ShowToast("That file's name cannot be handed to an app", ToastKindError)
			return false
		}
		if st, serr := os.Stat(f); serr != nil || !st.Mode().IsRegular() {
			ShowToast("The file is not there any more", ToastKindError)
			return false
		}
		args = append(args, f)
	}
	var app *acApp
	if in.AppID != "" {
		app = acCatalogueByID(in.AppID)
	}
	var cmd *exec.Cmd
	if strings.EqualFold(filepath.Ext(clean), ".sh") {
		// sh reads the script; the path and the file are operands, never code.
		cmd = exec.Command("sh", append([]string{"--", clean}, args...)...)
	} else {
		cmd = exec.Command(clean, args...)
	}
	cmd.Dir = filepath.Dir(clean)
	started := time.Now()

	if app != nil && app.Background {
		// No hand-off: the app starts its helper and returns, and the helper
		// records JukaHub. It still gets the firmware's environment.
		processGroupConfigure(cmd)
		cmd.Env = screenChildEnv(os.Environ())
		if err := cmd.Start(); err != nil {
			logWarn("appcenter", "launch failed for %s: %v", clean, err)
			ShowToast("Could not start "+name, ToastKindError)
			return false
		}
		logInfo("appcenter", "started %s in the background (%s)", name, clean)
		trophyEvent(tkAppLaunch)
		go func() {
			err := cmd.Wait()
			logInfo("appcenter", "%s exited (%s, %.0f s); its helper keeps running", name, acExitText(cmd, err), time.Since(started).Seconds())
		}()
		return true
	}

	// As every other launch does (powerprofile.go beginGamePowerProfileWith):
	// the voice assistant's boost and Auto performance's lowered hub ceiling
	// give the CPU back before the snapshot reads it. Auto then follows the
	// app under its folder's name — one key per app, never per file handed
	// to it (its own fallback names a child by its last argument).
	voiceBoostEnd()
	autoPerfLaunchBegin(in.Dir)
	autoPerfYield()
	snap := acSnapshotTake()
	screenChildGroupConfigure(cmd)
	if err := screenChildStart(cmd); err != nil {
		logWarn("appcenter", "launch failed for %s: %v", clean, err)
		ShowToast("Could not start "+name, ToastKindError)
		return false
	}
	logInfo("appcenter", "started %s (%s) with %d argument(s)", name, clean, len(args))
	trophyEvent(tkAppLaunch)
	noteGameChildStarted(cmd)
	stopWatch := make(chan struct{})
	go acWatchExecMenu(cmd, stopWatch)
	go func() {
		err := cmd.Wait()
		took := time.Since(started)
		close(stopWatch)
		menu := acReapStrays(cmd)
		screenChildReaped(cmd)
		restored := acSnapshotRestore(snap)
		if menu {
			restored = append(restored, "the screen (MainUI stopped)")
		}
		code := acExitText(cmd, err)
		logInfo("appcenter", "%s exited (%s, %.0f s)", name, code, took.Seconds())
		if len(restored) > 0 {
			logInfo("appcenter", "%s: put back %s", name, strings.Join(restored, ", "))
		}
		failed := err != nil && took < acQuickExit
		if !failed && len(restored) == 0 {
			return
		}
		tbxPost(func() {
			switch {
			case failed && len(restored) > 0:
				ShowToast(name+" closed at once ("+code+") · put back: "+strings.Join(restored, ", "), ToastKindWarning)
			case failed:
				ShowToast(name+" closed at once ("+code+")", ToastKindWarning)
			default:
				ShowToast("Back from "+name+" · put back: "+strings.Join(restored, ", "), ToastKindInfo)
			}
		})
	}()
	return true
}

// acExitText is an exit's code in words for the log and the toast.
func acExitText(cmd *exec.Cmd, err error) string {
	if cmd == nil || cmd.ProcessState == nil {
		if err != nil {
			return firstLine(err.Error())
		}
		return "code 0"
	}
	if code := cmd.ProcessState.ExitCode(); code >= 0 {
		return "code " + strconv.Itoa(code)
	}
	return cmd.ProcessState.String()
}
