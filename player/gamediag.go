package main

// gamediag.go — "why didn't my game start?", answered with file names and
// folders.
//
// A game that cannot start on the handheld usually shows a black screen for
// a second and drops back to JukaHub with no word of why: RetroArch quits when
// a core refuses the content, and its own message is gone with it. The cause
// is almost always one of a handful of things the hub can check itself:
//
//   - a BIOS the core requires is not in the folder this RetroArch reads
//     (gamediag_bios.go: per-core table, folder from retroarch.cfg);
//   - the core the launch script names is not installed, or cannot load on
//     this device (a 32-bit build, a missing library);
//   - the game file is gone, empty, a damaged or empty zip, a .cue/.m3u/.gdi
//     whose tracks are missing, or a file type the system's cores do not read;
//   - an arcade set is incomplete or wants a parent/BIOS set (neogeo.zip) —
//     named from the core's own log lines;
//   - a PortMaster port whose original game data was never copied in.
//
// How it runs. Every RetroArch launch from the library gets five logging
// keys for that launch only (log_verbosity, log_to_file,
// log_to_file_timestamp, log_dir, the two log levels) through the scoped
// retroarch.cfg edit (racfgscope.go), so RetroArch writes retroarch.log into
// JukaHub's data folder; the file is emptied before every launch, so it is one
// game's log and stays small. RetroArch writes no file at all unless
// log_verbosity is on — release builds drop even errors without it.
//
// When the game's process group is gone within gameDiagQuickExit of the
// launch, the diagnosis runs on the reaper goroutine (never the render
// thread), and when it has something to say — a finding, a non-zero exit, or
// a log line that says loading failed — the result is handed to the main
// thread (gameDiagShowPending, called by the main loop each frame) and shown
// in the launch-failure card (launch_errors.go) once JukaHub is on screen
// again. A player who quits a working game after five seconds sees nothing:
// a clean exit with a quiet log is not a failure. When nothing concrete is
// found the card says so and shows the emulator's last relevant log lines.
// A PortMaster port has its own rule (gamediag_port.go): one that ends by
// itself within gameDiagQuickExit always gets the card, and one within
// gameDiagPortQuickExit when its logs name a cause.
//
// The same checks run on demand from the game's options sheet ("Check why
// it won't start"), before the first launch.

import (
	"archive/zip"
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// gameDiagQuickExit is how soon after the launch an exit counts as "did
	// not start". A core refusing its content quits in one or two seconds; a
	// slow card and a CD image's first read can stretch that.
	gameDiagQuickExit = 8 * time.Second
	// gameDiagLogDirName is the folder under JukaHub's data folder RetroArch
	// writes retroarch.log into for JukaHub's launches.
	gameDiagLogDirName = "gamediag"
	// gameDiagLogTail bounds how much of a log is read.
	gameDiagLogTail = 96 << 10
	// gameDiagExcerptMax is how many log lines the card shows.
	gameDiagExcerptMax = 8
	// gameDiagListFileMax bounds a .cue/.m3u/.gdi/launch script read.
	gameDiagListFileMax = 64 << 10
)

// gameDiagReport is what the card shows.
type gameDiagReport struct {
	Title    string
	Summary  string
	Issues   []launchIssue
	LogLines []string
	LogPath  string
}

// gameDiagState hands a finished report to the main thread and remembers
// which game the RetroArch log belongs to.
var gameDiagState struct {
	mu       sync.Mutex
	pending  *gameDiagReport
	lastGame string // the ROM path of the launch the log is from
}

// gameDiagLaunch is one launch being watched.
type gameDiagLaunch struct {
	game     libraryGame
	launcher string
	logPath  string // RetroArch's log for this launch, "" when not set up
	outPath  string // the launch script's stdout/stderr (gamelaunch_firmware.go)
	port     string // a PortMaster script, instead of a library game
	portName string
	started  time.Time
	// ended, stopped and memMB (gamelaunch_watch.go; a port's too): when its
	// process group was gone, whether JukaHub had asked it to stop, and the
	// memory free (MB) when it started.
	ended   time.Time
	stopped bool
	memMB   int
}

// gameDiagLogDir is JukaHub's own folder for emulator logs.
func gameDiagLogDir() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, gameDiagLogDirName)
}

// gameDiagRetroArchLogValues are the keys that make RetroArch write
// <dir>/retroarch.log (names from RetroArch's config_file / verbosity.c: a
// file is written only when log_verbosity is on; with the timestamp off the
// name is always retroarch.log and each start overwrites it; 1 = info).
func gameDiagRetroArchLogValues(dir string) []raScopeValue {
	return []raScopeValue{
		{Key: "log_verbosity", Value: "true"},
		{Key: "log_to_file", Value: "true"},
		{Key: "log_to_file_timestamp", Value: "false"},
		{Key: "log_dir", Value: dir},
		{Key: "frontend_log_level", Value: "1"},
		{Key: "libretro_log_level", Value: "1"},
	}
}

// prepareGameDiag is the launch-time hook (game_library.go, beside the OSD
// preset): for a RetroArch launcher it empties JukaHub's retroarch.log and
// points RetroArch at it for this launch. It never blocks a launch. The
// returned watch goes to the reaper goroutine (exited).
func prepareGameDiag(game libraryGame, launcher string) *gameDiagLaunch {
	d := &gameDiagLaunch{game: game, launcher: launcher, started: time.Now(), memMB: gameLaunchMemFreeMB()}
	if !retroArchLauncher(launcher) {
		return d
	}
	dir := gameDiagLogDir()
	if dir == "" || os.MkdirAll(dir, 0o755) != nil {
		return d
	}
	logPath := filepath.Join(dir, "retroarch.log")
	// One game's log: what an earlier launch wrote must not be read as this
	// one's when RetroArch never gets far enough to write.
	_ = os.WriteFile(logPath, nil, 0o644)
	values := gameDiagRetroArchLogValues(dir)
	configured := false
	for _, path := range raScopeTargets(raLaunchCandidates(launcher, retroArchConfigCandidates())) {
		if _, err := raScopeApply(path, values); err != nil {
			logWarn("gamediag", "cannot turn on RetroArch's log in %s: %v", path, err)
			continue
		}
		configured = true
	}
	if configured {
		d.logPath = logPath
		gameDiagState.mu.Lock()
		gameDiagState.lastGame = game.Path
		gameDiagState.mu.Unlock()
	}
	return d
}

// gameDiagPortStart watches a PortMaster port launch (ports.go). PortMaster's
// own menu is not a game and is not watched.
func gameDiagPortStart(name, script string) *gameDiagLaunch {
	if name == portMasterAppName {
		return nil
	}
	return &gameDiagLaunch{port: script, portName: name, started: time.Now(), memMB: gameLaunchMemFreeMB()}
}

// gameDiagExitCode is the launcher's exit status: 0 for a clean exit, -1
// when it was killed by a signal or the status is unknown.
func gameDiagExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// JukaHub's own quit (in-game menu, exit) interrupts or kills the
		// group; that is the player leaving, not a failed start. A shell that
		// reports its child's signal says 128+N.
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			switch ws.Signal() {
			case syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL, syscall.SIGHUP:
				return 0
			}
		}
		switch code := exitErr.ExitCode(); code {
		case 128 + 1, 128 + 2, 128 + 9, 128 + 15:
			return 0
		default:
			return code
		}
	}
	return -1
}

// exited runs on the reaper goroutine once the game's process group is gone
// (after screenChildReaped). A long session is never diagnosed.
func (d *gameDiagLaunch) exited(waitErr error) {
	if d == nil {
		return
	}
	// The shader speed check reads this launch's log now (shader_speed.go).
	shaderSpeedGameEnded(d.game.Path, d.logPath, d.started)
	ran := time.Since(d.started)
	if !d.ended.IsZero() {
		// When the game's process group was gone, by the reaper's clock: the
		// profile restore and the time-lapse that run before this are not the
		// game's time.
		ran = d.ended.Sub(d.started)
	}
	quick := gameDiagQuickExit
	if d.port != "" {
		quick = gameDiagPortQuickExit
	}
	if ran > quick+screenSettleDelay {
		// A game that got going proves the core build it ran: a build on
		// probation stays (corebuilds_probation.go). Unless RetroArch's log
		// says the core never started it: RetroArch can sit in its own menu
		// after a core it could not open, and a player who leaves that menu
		// late has not proven anything.
		if d.port == "" {
			if core, path := d.coreUsed(); core != "" {
				if outcome, why := d.longRunOutcome(core); outcome != coreRunUnclear {
					coreProbationJudge(core, path, outcome, why)
				}
			}
		}
		return
	}
	code := gameDiagExitCode(waitErr)
	// For a library game JukaHub did not ask to stop (gamelaunch_watch.go), a
	// signal JukaHub did not send is not the player leaving, and a game over
	// within gameDiagBriefRun never got going, whatever its status: a Brick
	// Pro on CrossMix showed "Playing …" and nothing else because its launch
	// scripts ended at once with status 0. A port has its own rule below; a
	// Windows launcher is judged as before, as it may hand its game on and
	// return at once.
	_, sig := gameLaunchExitStatus(waitErr)
	ownGame := d.port == "" && !d.stopped
	killed := ownGame && sig > 0
	brief := ownGame && !IsWindows() && ran <= gameDiagBriefRun+screenSettleDelay
	var report gameDiagReport
	var failed bool
	if d.port != "" {
		report, failed = diagnosePort(d.portName, d.port, d.started)
	} else {
		report, failed = diagnoseLibraryGame(d.game, d.launcher, d.logPath)
		// What the launch script itself said: "ra64.trimui: not found".
		d.addLauncherOutput(&report)
	}
	// Only a finding that stops a game counts on its own: a "may be needed"
	// (Flycast's built-in BIOS, an unusual file type) is true of games that
	// run fine, and a player who quits one within seconds must not get a
	// "didn't start" card for it.
	strong := false
	for _, is := range report.Issues {
		if is.Level < int(gameDiagMaybe) {
			strong = true
			break
		}
	}
	// A port has its own rule (gamediag_port.go). One that ended by itself
	// within gameDiagQuickExit never got going, whatever it printed: the card
	// always opens. Later, up to gameDiagPortQuickExit — and at any time for
	// a cloud-gaming client, which may be left that soon on purpose — only a
	// cause in its own output (failed) opens it. One JukaHub stopped (Select +
	// Start, the in-game menu) is the player leaving.
	portGone := false
	if d.port != "" {
		// PortMaster scripts often end on a command that returns non-zero
		// after a normal quit, so a port's exit code proves nothing.
		code = 0
		early := ran <= gameDiagQuickExit+screenSettleDelay
		if (!early || x1PortName(filepath.Base(d.port))) && (d.stopped || !failed) {
			return
		}
		portGone = !d.stopped
		if !early {
			report.Title = d.portName + " stopped"
		}
	}
	// The first game of a core build JukaHub just activated: a build that
	// could not start it is put back (corebuilds_probation.go), and the card
	// says so first.
	probation := false
	// A Dolphin start that RetroArch's video driver could not draw proves
	// nothing about the core build: the driver is switched below instead of
	// the build being put back (dolphin_video.go).
	if ownGame && !dolphinVideoFailedStart(d.game, d.launcher, d.logPath) {
		if outcome, why := gameDiagCoreOutcome(report, failed, code, sig); outcome != coreRunUnclear {
			core, path := d.coreUsed()
			if note := coreProbationJudge(core, path, outcome, why); note != "" {
				report.Issues = append([]launchIssue{{Level: int(gameDiagSure), Title: "A core build JukaHub just installed could not start it", Detail: note}}, report.Issues...)
				strong = true
				// A rollback that failed (coreProbationFailedNote) leaves the
				// build in place, and the fix below may still replace it.
				probation = !strings.Contains(note, "putting the build before it back failed")
			}
		}
	}
	// A core build this firmware cannot open at all (RetroArch's own core
	// updater installs such nightlies): JukaHub puts one that runs here in its
	// place (corefix.go), and the card says so first.
	if ownGame && !probation {
		if fix, ok := coreFixAfterGame(report.Issues); ok {
			report.Issues = append([]launchIssue{fix}, report.Issues...)
			strong = true
		}
		// The Dolphin core with a video driver that could not draw for it:
		// the other driver next time (dolphin_video.go). Only for a start
		// that failed, not a player who quit within seconds.
		if code != 0 || failed || killed || brief {
			if fix, ok := dolphinAfterGame(d.game, d.launcher, d.logPath); ok {
				report.Issues = append([]launchIssue{fix}, report.Issues...)
				strong = true
			}
		}
	}
	if !strong && code == 0 && !failed && !killed && !brief && !portGone {
		// A clean exit with a quiet log: the player quit.
		return
	}
	secs := int(ran.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	when := "It closed after " + pluralizeCount(secs, "second", "seconds")
	switch {
	case killed:
		when += ": " + gameDiagSignalNote(sig)
	case code > 0:
		when += " (exit code " + strconv.Itoa(code) + ")"
	}
	// The memory free at the start, when it may be the answer: the system
	// killed the game, or there was little to begin with.
	memory := ""
	if d.memMB > 0 && ((killed && sig == 9) || d.memMB < gameDiagLowMemMB) {
		memory = " Free memory when it started: " + strconv.Itoa(d.memMB) + " MB."
	}
	switch {
	case len(report.Issues) > 0 && d.port != "" && !strong:
		report.Summary = when + "." + memory + " These may be why:"
	case len(report.Issues) > 0:
		report.Summary = when + "." + memory + " This is what stops it:"
	case d.port != "":
		report.Summary = when + ". JukaHub found nothing missing that it can check." + memory
		if len(report.LogLines) > 0 {
			report.Summary += " The last lines the port printed are below; PortMaster's Discord (#port-help) can read the full log."
		} else {
			report.Summary += " The port printed nothing before it stopped."
		}
	default:
		report.Summary = when + ". JukaHub found no missing BIOS, core or game file." + memory
		if len(report.LogLines) > 0 {
			report.Summary += " The emulator's own last words are below."
		} else {
			report.Summary += " The emulator wrote nothing useful to its log."
		}
	}
	logInfoDiag(report)
	gameDiagPost(report)
}

// logInfoDiag puts the findings in JukaHub's own log too, for bug reports.
func logInfoDiag(report gameDiagReport) {
	titles := make([]string, 0, len(report.Issues))
	for _, is := range report.Issues {
		titles = append(titles, is.Title)
	}
	Log().Info("game diagnosis", "title", report.Title, "issues", strings.Join(titles, "; "))
}

// gameDiagPost hands a report to the main thread.
func gameDiagPost(report gameDiagReport) {
	gameDiagState.mu.Lock()
	gameDiagState.pending = &report
	gameDiagState.mu.Unlock()
}

// gameDiagShowPending opens the card for a finished diagnosis once JukaHub is
// drawing again and no other launch card is up. The main loop calls it every
// frame (main.go, beside wineShowProfileFailure).
func gameDiagShowPending() {
	if launchFailure.open || gameScreenSuspended() {
		return
	}
	gameDiagState.mu.Lock()
	report := gameDiagState.pending
	gameDiagState.pending = nil
	gameDiagState.mu.Unlock()
	if report == nil {
		return
	}
	showLaunchDiagnosis(report.Title, report.Summary, report.Issues, report.LogLines, report.LogPath)
}

// gameDiagRunManual is the options sheet's "Check why it won't start": the
// same checks for the highlighted game, before or after a launch, on a
// goroutine. The launcher is resolved here on the main thread, as the
// library's own launch does.
func gameDiagRunManual(game libraryGame) {
	if game.System == androidLibrarySystem {
		ShowToast("Android games start through their game profile; nothing to check here", ToastKindInfo)
		return
	}
	if game.System == portsLibrarySystem {
		// A port's own checks: PortMaster, runtimes, game files, its log
		// (ports_manage.go).
		ShowToast("Checking "+game.Label()+"…", ToastKindInfo)
		portWhyRun(game.Label(), game.Path)
		return
	}
	launcher := resolveGameLauncherFor(game)
	ShowToast("Checking "+game.Label()+"…", ToastKindInfo)
	go func() {
		gameDiagState.mu.Lock()
		sameGame := gameDiagState.lastGame == game.Path
		gameDiagState.mu.Unlock()
		logPath := ""
		if sameGame {
			if dir := gameDiagLogDir(); dir != "" {
				logPath = filepath.Join(dir, "retroarch.log")
			}
		}
		report, _ := diagnoseLibraryGame(game, launcher, logPath)
		switch {
		case len(report.Issues) == 0:
			report.Title = game.Label() + " looks ready"
			report.Summary = "Nothing is missing: the game file, the core and the BIOS this core needs were checked."
			if len(report.LogLines) > 0 {
				report.Summary += " The last launch's log is below."
			}
		default:
			report.Title = "Before you play " + game.Label()
			report.Summary = "Found " + pluralizeCount(len(report.Issues), "thing", "things") + " that can stop it from starting:"
		}
		gameDiagPost(report)
	}()
}

// ── Library games ────────────────────────────────────────────────────────

// diagnoseLibraryGame runs every check for one game. failed reports a log
// line that says loading failed.
func diagnoseLibraryGame(game libraryGame, launcher, logPath string) (gameDiagReport, bool) {
	report := gameDiagReport{Title: game.Label() + " didn't start"}
	var issues []launchIssue
	issues = append(issues, gameDiagROMIssues(game.System, game.Path)...)
	// A Godot pack that is none, or of an engine the port lacks (godot.go).
	issues = append(issues, godotDiagIssues(game.System, game.Path, launcher)...)
	if strings.TrimSpace(launcher) == "" {
		issues = append(issues, launchIssue{
			Level:  int(gameDiagSure),
			Title:  "No emulator for " + game.System,
			Detail: "Install or enable an emulator for this system on the Emulators screen.",
		})
	} else if !regularFile(launcher) {
		issues = append(issues, launchIssue{
			Level:  int(gameDiagSure),
			Title:  "The emulator's launcher is gone",
			Files:  []string{filepath.Base(launcher)},
			Where:  filepath.Dir(launcher),
			Detail: "Reinstall the emulator, then rescan the Emulators screen.",
		})
	} else if retroArchLauncher(launcher) {
		core, known, coreIssues := gameDiagCoreIssues(launcher, game.System)
		issues = append(issues, coreIssues...)
		if core != "" {
			issues = append(issues, gameDiagBIOSIssues(game.System, game.Path, launcher, core, known)...)
		}
	}
	failed := false
	if logPath != "" {
		if lines := gameDiagReadTail(logPath, time.Time{}); len(lines) > 0 {
			sysDir, _, _ := gameDiagSystemDir(launcher, game.Path)
			logIssues, excerpt, logFailed := gameDiagLogFindings(lines, game.Path, sysDir)
			issues = gameDiagMergeIssues(issues, logIssues)
			report.LogLines = excerpt
			report.LogPath = logPath
			failed = logFailed
		}
	}
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].Level < issues[j].Level })
	report.Issues = issues
	return report, failed
}

// gameDiagMergeIssues adds the log's findings, skipping a file the static
// checks already named.
func gameDiagMergeIssues(issues, more []launchIssue) []launchIssue {
	named := map[string]bool{}
	for _, is := range issues {
		for _, f := range is.Files {
			named[strings.ToLower(strings.Fields(f + " x")[0])] = true
		}
	}
	for _, is := range more {
		var files []string
		for _, f := range is.Files {
			if !named[strings.ToLower(strings.Fields(f + " x")[0])] {
				files = append(files, f)
			}
		}
		if len(is.Files) > 0 && len(files) == 0 {
			continue
		}
		is.Files = files
		issues = append(issues, is)
	}
	return issues
}

// ── The core ─────────────────────────────────────────────────────────────

// gameDiagCoreIssues names the core the launcher starts and checks it is
// installed and loadable. known is false when the launcher names no core and
// the system's usual core is assumed.
func gameDiagCoreIssues(launcher, system string) (core string, known bool, issues []launchIssue) {
	ext := strings.ToLower(filepath.Ext(launcher))
	if ext == ".so" || ext == ".dll" {
		core = gameCoreFromLauncher(launcher, system)
		if problem := coreLoadProblemFile(launcher, launcher); problem != "" {
			issues = append(issues, gameDiagCoreLoadIssue(core, launcher, problem))
		}
		if is, ok := coreReplacedIssue(core, launcher); ok {
			issues = append(issues, is)
		}
		return core, true, issues
	}
	head, err := gameDiagReadHead(launcher)
	if err != nil {
		return gameCoreFromLauncher(launcher, system), false, nil
	}
	names := coreNamesIn(string(head))
	core = gameCoreFromLauncher(launcher, system)
	if len(names) == 0 || core == "" {
		return core, false, nil
	}
	known = len(names) == 1
	dirs := retroArchCoreDirs()
	if len(dirs) == 0 {
		return core, known, nil
	}
	path := coreActiveBinary(core, dirs)
	if path == "" {
		issues = append(issues, launchIssue{
			Level: int(gameDiagSure),
			Title: "The " + coreConfigDisplayName(core) + " core is not installed",
			Files: []string{coreBuildFileName(core)},
			Where: dirs[0],
			Detail: filepath.Base(launcher) + " starts RetroArch with this core and it is in none of RetroArch's cores folders. " +
				"Copy the core file there, or install it with RetroArch's core downloader.",
		})
		return core, known, issues
	}
	if problem := coreLoadProblemFile(path, path); problem != "" {
		issues = append(issues, gameDiagCoreLoadIssue(core, path, problem))
	}
	// A GPU core JukaHub's update replaced: said as a "maybe", so a report
	// shows it beside whatever else stopped the game.
	if is, ok := coreReplacedIssue(core, path); ok {
		issues = append(issues, is)
	}
	return core, known, issues
}

// gameDiagCoreOutcome is what a quick exit says about the core build the game
// ran, for its probation (corebuilds_probation.go): failed when a finding names
// the core, or — with nothing else (a missing game file or BIOS) to explain it
// — when it crashed, its log says loading failed, or it exited with an error.
// A quick, clean, quiet exit is unclear: a player who changed their mind.
func gameDiagCoreOutcome(report gameDiagReport, failed bool, code, sig int) (coreRunOutcome, string) {
	// RetroArch's own words about the GPU context or libGL name the build
	// whatever else is wrong. Any other finding about the core counts only
	// when nothing else (a missing game file or BIOS, a launch script that
	// failed) explains the exit: the issues are sorted by level, so a missing
	// ROM can come before it.
	other, coreTitle := false, ""
	for _, is := range report.Issues {
		if is.Level >= int(gameDiagMaybe) {
			continue
		}
		switch {
		case strings.Contains(strings.ToLower(is.Title), "needs desktop opengl"):
			return coreRunFailed, is.Title
		case gameDiagCoreIssueTitle(is.Title):
			if coreTitle == "" {
				coreTitle = is.Title
			}
		default:
			other = true
		}
	}
	if other {
		return coreRunUnclear, ""
	}
	if coreTitle != "" {
		return coreRunFailed, coreTitle
	}
	switch {
	case sig == 4 || sig == 6 || sig == 7 || sig == 8 || sig == 11:
		return coreRunFailed, "it crashed (signal " + strconv.Itoa(sig) + ")"
	case failed:
		return coreRunFailed, "RetroArch's log says loading failed"
	case code > 0:
		return coreRunFailed, "it exited with code " + strconv.Itoa(code)
	}
	return coreRunUnclear, ""
}

// gameDiagCoreIssueTitle reports a finding about the core build itself (not
// its absence, not the game file).
func gameDiagCoreIssueTitle(title string) bool {
	lower := strings.ToLower(title)
	for _, mark := range []string{"cannot load on this device", "could not open the core", "needs desktop opengl"} {
		if strings.Contains(lower, mark) {
			return true
		}
	}
	return false
}

// gameDiagLogCoreRe finds the core RetroArch loaded, or could not open, in its
// log: `Loading dynamic libretro core from: "/…/mupen64plus_next_libretro.so"`.
var gameDiagLogCoreRe = regexp.MustCompile(`(?i)(?:loading dynamic libretro core from|failed to open libretro core)[^"/\\\r\n]*"?([^"\r\n]*?_libretro\.(?:so|dll))`)

// coreUsed is the core a library launch ran, and its file when that is known:
// the one RetroArch's log names, else the core file itself, else — only when
// no log was set up for the launch — the one core a RetroArch launch script
// names; "" when it cannot be told (a script naming several cores). A log that
// was set up and names no core means RetroArch never loaded one: a launch
// script that runs a standalone emulator (CrossMix's N64 script can run either)
// or failed before RetroArch started says nothing about the core it names.
func (d *gameDiagLaunch) coreUsed() (core, path string) {
	if d == nil {
		return "", ""
	}
	if d.logPath != "" {
		if head, err := gameDiagReadHead(d.logPath); err == nil {
			if m := gameDiagLogCoreRe.FindSubmatch(head); m != nil {
				file := strings.TrimSpace(string(m[1]))
				if name, _ := parseCoreFile(filepath.Base(strings.ReplaceAll(file, `\`, "/"))); name != "" {
					if !filepath.IsAbs(file) {
						file = ""
					}
					return name, file
				}
			}
		}
	}
	if ext := strings.ToLower(filepath.Ext(d.launcher)); ext == ".so" || ext == ".dll" {
		name, _ := parseCoreFile(filepath.Base(d.launcher))
		return name, d.launcher
	}
	if d.logPath != "" || !retroArchLauncher(d.launcher) {
		return "", ""
	}
	if head, err := gameDiagReadHead(d.launcher); err == nil {
		if names := coreNamesIn(string(head)); len(names) == 1 {
			return names[0], ""
		}
	}
	return "", ""
}

// longRunOutcome is what a game that stayed past the quick-exit window says
// about core, the build it ran: proven, unless RetroArch's log says that core
// was never opened or could not get its GPU context (failed), or that loading
// failed for a reason that may be the game's (unclear). A core the player
// loaded later from RetroArch's menu is not this one.
func (d *gameDiagLaunch) longRunOutcome(core string) (coreRunOutcome, string) {
	if d.logPath == "" {
		return coreRunProven, ""
	}
	lines := gameDiagReadTail(d.logPath, time.Time{})
	if gpu, ok := gameDiagGPULogIssue(lines); ok {
		return coreRunFailed, gpu.Title
	}
	for _, line := range lines {
		if !strings.Contains(strings.ToLower(line), "failed to open libretro core") {
			continue
		}
		if m := gameDiagLogCoreRe.FindStringSubmatch(line); m != nil {
			if name, _ := parseCoreFile(filepath.Base(strings.ReplaceAll(m[1], `\`, "/"))); name != "" && name != normalizeCoreName(core) {
				continue
			}
		}
		return coreRunFailed, "RetroArch could not open the core"
	}
	if _, failed := gameDiagExcerpt(lines); failed {
		return coreRunUnclear, ""
	}
	return coreRunProven, ""
}

// gameDiagGPULogIssue reads RetroArch's log for the two ways a core built for
// desktop OpenGL fails under this device's OpenGL ES RetroArch: the core cannot
// be opened at all (it links libGL.so.1, which the firmware does not have), or
// it opens and asks for a desktop OpenGL context RetroArch cannot make.
func gameDiagGPULogIssue(lines []string) (launchIssue, bool) {
	const fix = " The firmware's own build of this core draws with OpenGL ES: roll the core back on the Emulators page (R1) or in Core builds."
	for _, line := range lines {
		lower := strings.ToLower(line)
		switch {
		case strings.Contains(lower, "compiled against opengles") || strings.Contains(lower, "cannot use hw context") ||
			strings.Contains(lower, "verification of hw context failed") || strings.Contains(lower, "verify hw context failed"):
			return launchIssue{
				Level:  int(gameDiagSure),
				Title:  "This core build needs desktop OpenGL",
				Detail: "RetroArch on this device draws with OpenGL ES only, and the core asked it for a desktop OpenGL context, so the game could not start." + fix,
			}, true
		case strings.Contains(lower, "libgl.so") && (strings.Contains(lower, "cannot open shared object") || strings.Contains(lower, "no such file")):
			return launchIssue{
				Level:  int(gameDiagSure),
				Title:  "This core build needs desktop OpenGL (libGL.so.1)",
				Detail: "This device has OpenGL ES only, so RetroArch could not open the core at all." + fix,
			}, true
		}
	}
	return launchIssue{}, false
}

func gameDiagCoreLoadIssue(core, path, problem string) launchIssue {
	return launchIssue{
		Level:  int(gameDiagLikely),
		Title:  "The " + coreConfigDisplayName(core) + " core cannot load on this device",
		Where:  path,
		Detail: problem + ". Replace it with a build for this device (64-bit ARM on the Smart Pro).",
	}
}

// gameDiagReadHead reads the start of a small text file.
func gameDiagReadHead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, gameDiagListFileMax))
}

// ── The game file ────────────────────────────────────────────────────────

// gameDiagSystemExtTable lists the file types each system's cores read
// (besides .zip and .7z). A system missing here gets no
// file-type check.
var gameDiagSystemExtTable = []struct {
	keys []string
	exts []string
}{
	{[]string{"nes", "fc", "famicom"}, []string{".nes", ".fds", ".unf", ".unif"}},
	{[]string{"snes", "sfc", "super nintendo"}, []string{".smc", ".sfc", ".swc", ".fig", ".bs", ".st"}},
	{[]string{"game boy", "gb"}, []string{".gb", ".gbc", ".sgb", ".dmg"}},
	{[]string{"game boy color", "gbc"}, []string{".gbc", ".gb", ".sgb"}},
	{[]string{"game boy advance", "gba"}, []string{".gba", ".agb", ".bin", ".mb"}},
	{[]string{"mega drive", "md", "megadrive", "genesis"}, []string{".md", ".gen", ".smd", ".bin", ".68k", ".sgd", ".mdx"}},
	{[]string{"master system", "ms", "sms"}, []string{".sms", ".bin", ".sg"}},
	{[]string{"game gear", "gg"}, []string{".gg", ".sms", ".bin"}},
	{[]string{"32x", "sega 32x"}, []string{".32x", ".bin", ".md"}},
	{[]string{"atari 2600", "a2600", "atari2600"}, []string{".a26", ".bin"}},
	{[]string{"atari 7800", "a7800", "atari7800"}, []string{".a78", ".bin"}},
	{[]string{"atari lynx", "lynx"}, []string{".lnx", ".o", ".lyx"}},
	{[]string{"wonderswan", "ws"}, []string{".ws", ".wsc", ".pc2"}},
	{[]string{"wonderswan color", "wsc"}, []string{".wsc", ".ws", ".pc2"}},
	// GameCube and Wii before the Neo Geo Pocket rows: Roms/NGC is a GameCube
	// folder (romsetup.go), and its .ngc is the Pocket's file type.
	{[]string{"gamecube", "gc", "nintendo - gamecube"}, []string{".iso", ".gcm", ".gcz", ".rvz", ".ciso", ".wia", ".tgc", ".dol", ".elf", ".m3u"}},
	{[]string{"wii", "nintendo - wii"}, []string{".iso", ".wbfs", ".rvz", ".wia", ".gcz", ".ciso", ".wad", ".dol", ".elf", ".m3u"}},
	{[]string{"neo geo pocket", "ngp"}, []string{".ngp", ".ngc", ".ngpc", ".npc"}},
	{[]string{"neo geo pocket color", "ngpc", "ngc"}, []string{".ngc", ".ngp", ".ngpc", ".npc"}},
	{[]string{"nintendo 64", "n64"}, []string{".n64", ".v64", ".z64", ".ndd", ".bin", ".u1"}},
	{[]string{"nintendo ds", "nds"}, []string{".nds", ".dsi", ".ids", ".srl"}},
	{[]string{"pc engine", "pce", "tg16"}, []string{".pce", ".sgx", ".cue", ".chd", ".ccd", ".m3u", ".toc", ".iso"}},
	{[]string{"sega cd", "segacd", "mega cd", "megacd"}, []string{".cue", ".chd", ".iso", ".m3u", ".bin", ".img", ".ccd"}},
	{[]string{"playstation", "ps", "ps1", "psx"}, []string{".cue", ".bin", ".img", ".chd", ".pbp", ".m3u", ".iso", ".ccd", ".mdf", ".toc", ".exe", ".cbn", ".ecm"}},
	{[]string{"dreamcast", "dc"}, []string{".gdi", ".cdi", ".chd", ".cue", ".m3u", ".iso", ".elf", ".lst"}},
	{[]string{"saturn", "ss"}, []string{".cue", ".chd", ".ccd", ".m3u", ".iso", ".mds", ".toc"}},
	{[]string{"playstation portable", "psp"}, []string{".iso", ".cso", ".pbp", ".chd", ".elf", ".prx"}},
	{[]string{"neo geo cd", "neogeocd"}, []string{".cue", ".chd"}},
}

// gameDiagSystemExts is the file types for a game's system; known is false
// for a system the table does not list.
func gameDiagSystemExts(system, romPath string) (exts []string, known bool) {
	keys := gameDiagSystemKeys(system, romPath)
	for _, row := range gameDiagSystemExtTable {
		for _, k := range row.keys {
			if keys[k] {
				return row.exts, true
			}
		}
	}
	return nil, false
}

// gameDiagIsArcade reports a system whose zips are ROM sets, not one game file.
func gameDiagIsArcade(system, romPath string) bool {
	for k := range gameDiagSystemKeys(system, romPath) {
		switch k {
		case "arcade", "mame", "fbneo", "fba", "fbalpha", "cps1", "cps2", "cps3", "neo geo", "neogeo",
			"naomi", "atomiswave", "mvs", "cps", "snk - neo geo":
			return true
		}
	}
	return false
}

// gameDiagROMIssues checks the game file itself.
func gameDiagROMIssues(system, romPath string) []launchIssue {
	base, dir := filepath.Base(romPath), filepath.Dir(romPath)
	info, err := os.Stat(romPath)
	switch {
	case err != nil && os.IsNotExist(err):
		return []launchIssue{{
			Level:  int(gameDiagSure),
			Title:  "The game file is gone",
			Files:  []string{base},
			Where:  dir,
			Detail: "It was moved, renamed or deleted after the library was scanned. Put it back, or rescan the library.",
		}}
	case err != nil:
		return []launchIssue{{
			Level:  int(gameDiagSure),
			Title:  "The game file cannot be read",
			Where:  romPath,
			Detail: err.Error() + ". The card may need a check on a computer.",
		}}
	case info.IsDir():
		return nil
	case info.Size() == 0:
		return []launchIssue{{
			Level:  int(gameDiagSure),
			Title:  "The game file is empty (0 bytes)",
			Where:  romPath,
			Detail: "The copy did not finish. Copy the game onto the card again.",
		}}
	}
	f, err := os.Open(romPath)
	if err != nil {
		return []launchIssue{{Level: int(gameDiagSure), Title: "The game file cannot be opened", Where: romPath, Detail: err.Error()}}
	}
	f.Close()
	ext := strings.ToLower(filepath.Ext(romPath))
	exts, known := gameDiagSystemExts(system, romPath)
	arcade := gameDiagIsArcade(system, romPath)
	var out []launchIssue
	switch ext {
	case ".zip":
		out = append(out, gameDiagZipIssues(romPath, exts, known && !arcade)...)
	case ".7z":
		if !gameDiagHasMagic(romPath, 0, []byte("7z\xbc\xaf\x27\x1c")) {
			out = append(out, gameDiagDamaged(romPath, "7-Zip archive"))
		}
	case ".chd":
		if !gameDiagHasMagic(romPath, 0, []byte("MComprHD")) {
			out = append(out, gameDiagDamaged(romPath, "CHD disc image"))
		}
	case ".pbp":
		if !gameDiagHasMagic(romPath, 0, []byte("\x00PBP")) {
			out = append(out, gameDiagDamaged(romPath, "PBP file"))
		}
	case ".cue":
		if missing := gameDiagCueMissing(romPath); len(missing) > 0 {
			out = append(out, gameDiagTracksIssue(romPath, missing))
		}
	case ".gdi":
		if missing := gameDiagGdiMissing(romPath); len(missing) > 0 {
			out = append(out, gameDiagTracksIssue(romPath, missing))
		}
	case ".ccd":
		img := strings.TrimSuffix(base, filepath.Ext(base)) + ".img"
		if _, ok := gameDiagFileIn(dir, img); !ok {
			out = append(out, gameDiagTracksIssue(romPath, []string{img}))
		}
	case ".m3u":
		out = append(out, gameDiagM3UIssues(romPath)...)
	}
	if known && !arcade && ext != "" && ext != ".zip" && ext != ".7z" && !gameDiagHasExt(exts, ext) {
		out = append(out, launchIssue{
			Level:  int(gameDiagMaybe),
			Title:  ext + " files may not load as " + system + " games",
			Where:  romPath,
			Detail: "This system's cores usually read " + strings.Join(exts, " ") + ". The file may belong to another system's folder.",
		})
	}
	return out
}

func gameDiagHasExt(exts []string, ext string) bool {
	for _, e := range exts {
		if e == ext {
			return true
		}
	}
	return false
}

// gameDiagHasMagic reports whether the file has magic at offset.
func gameDiagHasMagic(path string, offset int64, magic []byte) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, len(magic))
	if _, err := f.ReadAt(buf, offset); err != nil {
		return false
	}
	return bytes.Equal(buf, magic)
}

func gameDiagDamaged(path, kind string) launchIssue {
	return launchIssue{
		Level:  int(gameDiagSure),
		Title:  "The game file is damaged",
		Where:  path,
		Detail: "It does not start like a " + kind + " does. The download or copy was cut short: copy it again.",
	}
}

// gameDiagZipIssues checks a zip opens and, for a cartridge system, holds a
// game file of a type the system reads.
func gameDiagZipIssues(path string, exts []string, checkContent bool) []launchIssue {
	r, err := zip.OpenReader(path)
	if err != nil {
		return []launchIssue{gameDiagDamaged(path, "zip archive")}
	}
	defer r.Close()
	var names []string
	ok := false
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		names = append(names, f.Name)
		if gameDiagHasExt(exts, strings.ToLower(filepath.Ext(f.Name))) {
			ok = true
		}
	}
	if len(names) == 0 {
		return []launchIssue{{
			Level:  int(gameDiagSure),
			Title:  "The zip is empty",
			Where:  path,
			Detail: "There is no game inside it. Copy the game again.",
		}}
	}
	if !checkContent || ok {
		return nil
	}
	shown := names
	if len(shown) > 5 {
		shown = append(append([]string(nil), shown[:5]...), "and "+strconv.Itoa(len(names)-5)+" more")
	}
	return []launchIssue{{
		Level:  int(gameDiagLikely),
		Title:  "The zip holds no game this system reads",
		Where:  path,
		Detail: "Inside: " + strings.Join(shown, ", ") + ". Expected a " + strings.Join(exts, " ") + " file. Unzip it on a computer and copy the game file itself, or put it in the right system's folder.",
	}}
}

// gameDiagTracksIssue reports files a disc's index lists but the folder lacks.
func gameDiagTracksIssue(path string, missing []string) launchIssue {
	return launchIssue{
		Level: int(gameDiagSure),
		Title: "Files the " + strings.ToLower(filepath.Ext(path)) + " lists are missing",
		Files: missing,
		Where: filepath.Dir(path),
		Detail: filepath.Base(path) + " only describes the disc; the files it names must sit beside it with exactly these names. " +
			"Renaming a .bin without editing the .cue breaks it.",
	}
}

var (
	gameDiagCueFileRe = regexp.MustCompile(`(?i)^\s*FILE\s+(?:"([^"]+)"|(\S+))`)
	gameDiagGdiLineRe = regexp.MustCompile(`^\s*\d+\s+\d+\s+\d+\s+\d+\s+(?:"([^"]+)"|(\S+))`)
)

// gameDiagListedMissing reads an index file and returns the files it names
// that are not in its folder.
func gameDiagListedMissing(path string, re *regexp.Regexp) []string {
	data, err := gameDiagReadHead(path)
	if err != nil {
		return nil
	}
	dir := filepath.Dir(path)
	var missing []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		m := re.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		name := m[1]
		if name == "" {
			name = m[2]
		}
		name = filepath.FromSlash(strings.ReplaceAll(name, `\`, "/"))
		if _, ok := gameDiagFileIn(filepath.Join(dir, filepath.Dir(name)), filepath.Base(name)); !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

func gameDiagCueMissing(path string) []string { return gameDiagListedMissing(path, gameDiagCueFileRe) }
func gameDiagGdiMissing(path string) []string { return gameDiagListedMissing(path, gameDiagGdiLineRe) }

// gameDiagM3UIssues checks every disc a playlist names, and each disc's own
// track list.
func gameDiagM3UIssues(path string) []launchIssue {
	data, err := gameDiagReadHead(path)
	if err != nil {
		return nil
	}
	dir := filepath.Dir(path)
	var missing []string
	var out []launchIssue
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// "disc.cue|Label" is the M3U label syntax some cores read.
		if i := strings.IndexByte(line, '|'); i >= 0 {
			line = line[:i]
		}
		name := filepath.FromSlash(strings.ReplaceAll(line, `\`, "/"))
		full := name
		if !filepath.IsAbs(full) {
			full = filepath.Join(dir, name)
		}
		found, ok := gameDiagFileIn(filepath.Dir(full), filepath.Base(full))
		if !ok {
			missing = append(missing, name)
			continue
		}
		switch strings.ToLower(filepath.Ext(found)) {
		case ".cue":
			if m := gameDiagCueMissing(found); len(m) > 0 {
				out = append(out, gameDiagTracksIssue(found, m))
			}
		case ".gdi":
			if m := gameDiagGdiMissing(found); len(m) > 0 {
				out = append(out, gameDiagTracksIssue(found, m))
			}
		}
	}
	if len(missing) > 0 {
		out = append([]launchIssue{{
			Level:  int(gameDiagSure),
			Title:  "Discs the playlist lists are missing",
			Files:  missing,
			Where:  dir,
			Detail: filepath.Base(path) + " names these discs; each must be in this folder under exactly this name.",
		}}, out...)
	}
	return out
}

// ── Logs ─────────────────────────────────────────────────────────────────

// gameDiagReadTail reads the end of a log, as lines. A log older than since
// (when set) is from an earlier run and is ignored.
func gameDiagReadTail(path string, since time.Time) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return nil
	}
	if !since.IsZero() && info.ModTime().Before(since.Add(-2*time.Second)) {
		return nil
	}
	start := info.Size() - gameDiagLogTail
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, gameDiagLogTail))
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r", ""), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // a cut first line
	}
	return lines
}

// gameDiagExcerptMarkers pick the log lines worth showing.
var gameDiagExcerptMarkers = []string{
	"error", "fail", "missing", "not found", "required", "could not", "couldn't", "cannot", "can't",
	"unable", "bios", "firmware", "no such file", "segmentation", "abort",
}

// gameDiagExcerptNoise are error lines RetroArch logs on every start of a
// working game, so the card leaves them out: TrimUI's stock retroarch.cfg asks
// for an "sdl" input driver its ra64.trimui does not have, and RetroArch then
// goes on with the first input driver it does have.
var gameDiagExcerptNoise = []string{
	"couldn't find any input driver named",
}

// gameDiagFailMarkers are lines that mean loading failed outright.
var gameDiagFailMarkers = []string{
	"failed to load content", "failed to open libretro core", "failed to load core", "could not load content",
	"unable to load content", "some files are missing", "is required", "failed to load game",
	"bios missing", "missing bios", "segmentation fault", "failed to load rom",
}

var (
	gameDiagFBNeoNamedRe = regexp.MustCompile(`(?i)with name (\S+) and CRC 0x([0-9a-f]+) is required`)
	gameDiagFBNeoIndexRe = regexp.MustCompile(`(?i)ROM at index (\d+) with CRC 0x([0-9a-f]+) is required`)
	gameDiagFBNeoSearch  = regexp.MustCompile(`(?i)index (\d+) with CRC 0x([0-9a-f]+) and name (\S+)`)
	gameDiagMAMEMissRe   = regexp.MustCompile(`(\S+)\s+NOT FOUND(?:\s*\(tried in ([^)]*)\))?`)
	gameDiagFirmwareRe   = regexp.MustCompile(`(?i)[A-Za-z0-9_\-+.]+\.(?:bin|rom|zip|img|pce|bios|dat|zim)\b`)
	gameDiagPathTokenRe  = regexp.MustCompile(`[^\s'"\[\]()<>,;]+\.[A-Za-z0-9]{1,5}\b`)
)

// gameDiagHasAny reports whether lower contains one of the markers.
func gameDiagHasAny(lower string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// gameDiagExcerpt keeps the last relevant lines, trimmed for the card.
func gameDiagExcerpt(lines []string) ([]string, bool) {
	var out []string
	failed := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if gameDiagHasAny(lower, gameDiagFailMarkers) || strings.Contains(line, "NOT FOUND") {
			failed = true
		}
		if !gameDiagHasAny(lower, gameDiagExcerptMarkers) || gameDiagHasAny(lower, gameDiagExcerptNoise) {
			continue
		}
		if r := []rune(line); len(r) > 220 {
			line = string(r[:220]) + "…"
		}
		out = append(out, line)
	}
	if len(out) > gameDiagExcerptMax {
		out = out[len(out)-gameDiagExcerptMax:]
	}
	return out, failed
}

// gameDiagListFiles caps a file list for the card.
func gameDiagListFiles(names []string, max int) []string {
	if len(names) <= max {
		return names
	}
	return append(append([]string(nil), names[:max]...), "and "+strconv.Itoa(len(names)-max)+" more")
}

// gameDiagLogFindings reads RetroArch's log: an arcade core's missing ROM
// files and parent/BIOS sets, firmware a core reports missing, and a core
// RetroArch could not open.
func gameDiagLogFindings(lines []string, romPath, sysDir string) (issues []launchIssue, excerpt []string, failed bool) {
	excerpt, failed = gameDiagExcerpt(lines)
	romDir := filepath.Dir(romPath)
	romStem := strings.ToLower(strings.TrimSuffix(filepath.Base(romPath), filepath.Ext(romPath)))
	searched := map[string]string{} // CRC → file name, from FBNeo's search lines
	var chips, sets, firmware []string
	seen := map[string]bool{}
	addTo := func(list *[]string, name string) {
		key := strings.ToLower(name)
		if name == "" || seen[key] {
			return
		}
		seen[key] = true
		*list = append(*list, name)
	}
	coreProblem := ""
	for _, line := range lines {
		if m := gameDiagFBNeoSearch.FindStringSubmatch(line); m != nil {
			searched[strings.ToLower(m[2])] = m[3]
		}
	}
	for _, line := range lines {
		lower := strings.ToLower(line)
		switch {
		case gameDiagFBNeoNamedRe.MatchString(line):
			addTo(&chips, gameDiagFBNeoNamedRe.FindStringSubmatch(line)[1])
		case gameDiagFBNeoIndexRe.MatchString(line):
			m := gameDiagFBNeoIndexRe.FindStringSubmatch(line)
			idx, _ := strconv.Atoi(m[1])
			name := searched[strings.ToLower(m[2])]
			switch {
			case idx >= 128 && name != "":
				// FBNeo numbers BIOS and parent-set files from 128.
				addTo(&chips, name+" (from a BIOS or parent set)")
			case idx >= 128:
				addTo(&chips, "a BIOS or parent-set file with CRC 0x"+m[2])
			case name != "":
				addTo(&chips, name)
			default:
				addTo(&chips, "a file with CRC 0x"+m[2])
			}
		case strings.Contains(line, "NOT FOUND"):
			m := gameDiagMAMEMissRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			addTo(&chips, strings.Trim(m[1], `"':`))
			for _, set := range strings.Fields(m[2]) {
				if strings.ToLower(set) != romStem {
					addTo(&sets, set+".zip")
				}
			}
		case strings.Contains(lower, "neogeo bios missing") || strings.Contains(lower, "neo geo bios missing"):
			addTo(&sets, "neogeo.zip")
		case strings.Contains(lower, "failed to open libretro core") || strings.Contains(lower, "failed to load core"):
			// Not any "dlopen" line: a graphics or audio back end probing an
			// optional library logs one in a game that runs.
			if coreProblem == "" {
				coreProblem = strings.TrimSpace(line)
			}
		case (strings.Contains(lower, "bios") || strings.Contains(lower, "firmware")) &&
			gameDiagHasAny(lower, []string{"missing", "not found", "required", "fail", "could not", "cannot", "unable", "error"}):
			for _, tok := range gameDiagFirmwareRe.FindAllString(line, -1) {
				base := filepath.Base(strings.ReplaceAll(tok, `\`, "/"))
				if strings.Contains(strings.ToLower(base), "_libretro") || strings.EqualFold(base, filepath.Base(romPath)) {
					continue
				}
				addTo(&firmware, base)
			}
		}
	}
	if len(chips) > 0 {
		issues = append(issues, launchIssue{
			Level: int(gameDiagSure),
			Title: "The arcade ROM set is incomplete",
			Files: gameDiagListFiles(chips, 12),
			Where: romPath,
			Detail: "These files belong inside the game's zip. The usual cause is a set made for another MAME/FBNeo version: " +
				"use a set that matches the core (FBNeo wants a current FBNeo set; MAME 2003-Plus a 0.78-based one).",
		})
	}
	if len(sets) > 0 {
		detail := "The game also loads files from these sets. Copy each zip as it is, beside the game."
		for _, s := range sets {
			if strings.EqualFold(s, "neogeo.zip") {
				detail += " neogeo.zip may also go in the BIOS folder: " + sysDir
				break
			}
		}
		issues = append(issues, launchIssue{
			Level:  int(gameDiagSure),
			Title:  "A parent or BIOS set is missing",
			Files:  sets,
			Where:  romDir,
			Detail: detail,
		})
	}
	if len(firmware) > 0 {
		// Cores with a built-in BIOS log the missing file and boot anyway, so
		// without a line that says loading failed this is only a "maybe".
		level := gameDiagLikely
		if !failed {
			level = gameDiagMaybe
		}
		issues = append(issues, launchIssue{
			Level:  int(level),
			Title:  "The core reports missing firmware",
			Files:  gameDiagListFiles(firmware, 8),
			Where:  sysDir,
			Detail: "Named by the core in RetroArch's log. Copy the files into RetroArch's BIOS folder with exactly these names.",
		})
	}
	if coreProblem != "" {
		issues = append(issues, launchIssue{
			Level:  int(gameDiagSure),
			Title:  "RetroArch could not open the core",
			Detail: coreProblem,
		})
	}
	// A core built for desktop OpenGL, named first: it is the whole answer.
	if gpu, ok := gameDiagGPULogIssue(lines); ok {
		issues = append([]launchIssue{gpu}, issues...)
		failed = true
	}
	return issues, excerpt, failed
}

// ── PortMaster ports ─────────────────────────────────────────────────────

var gameDiagGameDirRe = regexp.MustCompile(`(?m)^\s*(?:export\s+)?GAMEDIR\s*=\s*["']?([^"'\s]+)`)

// gameDiagPortDir is the folder a port's script keeps its files in
// (GAMEDIR="/$directory/ports/<name>"): the part after "ports/" is looked
// up under every ports folder PortMaster uses. rel is that part, for the
// card when the folder is missing.
func gameDiagPortDir(script string) (dir, rel string) {
	head, err := gameDiagReadHead(script)
	if err != nil {
		return "", ""
	}
	m := gameDiagGameDirRe.FindSubmatch(head)
	if m == nil {
		return "", ""
	}
	value := filepath.ToSlash(string(m[1]))
	lower := strings.ToLower(value)
	i := strings.LastIndex(lower, "/ports/")
	switch {
	case i >= 0:
		rel = value[i+len("/ports/"):]
	case strings.HasPrefix(lower, "ports/"):
		rel = value[len("ports/"):]
	case filepath.IsAbs(value) && !strings.Contains(value, "$"):
		if info, err := os.Stat(value); err == nil && info.IsDir() {
			return value, filepath.Base(value)
		}
		return "", filepath.Base(value)
	default:
		return "", ""
	}
	rel = strings.Trim(rel, "/")
	if rel == "" || strings.Contains(rel, "$") || strings.Contains(rel, "..") {
		return "", ""
	}
	roots := append([]string{filepath.Dir(script)}, portRoots(portMasterSDRoot)...)
	for _, root := range roots {
		candidate := filepath.Join(root, filepath.FromSlash(rel))
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, rel
		}
		// FAT ignores case; an ext4 card does not.
		if found, ok := gameDiagFileIn(root, filepath.FromSlash(rel)); ok {
			if info, err := os.Stat(found); err == nil && info.IsDir() {
				return found, rel
			}
		}
	}
	return "", rel
}

// gameDiagDirHasGameFiles reports whether dir holds a file other than notes
// (README, *.txt, *.md, .gitkeep), two levels deep.
func gameDiagDirHasGameFiles(dir string, depth int) bool {
	items, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, it := range items {
		name := strings.ToLower(it.Name())
		if it.IsDir() {
			if depth > 0 && gameDiagDirHasGameFiles(filepath.Join(dir, it.Name()), depth-1) {
				return true
			}
			continue
		}
		if strings.HasPrefix(name, "readme") || strings.HasPrefix(name, ".") ||
			strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, ".md") {
			continue
		}
		return true
	}
	return false
}

// diagnosePort checks a PortMaster port that quit at once: its folder, the
// game data folder many ports need filled with the original game's files,
// and what it printed — JukaHub's log of the launch and the port's log.txt,
// sorted into causes by gameDiagPortLogFindings (gamediag_port.go).
func diagnosePort(name, script string, started time.Time) (gameDiagReport, bool) {
	report := gameDiagReport{Title: name + " didn't start"}
	// JukaHub's log has the launch notes and everything the script printed,
	// also before it sent its output to log.txt: a script that died under
	// BusyBox's sh or sourcing control.txt never wrote log.txt.
	jukaLog := portLogPath(name, script)
	notes, output := gameDiagPortJukaLog(jukaLog, started)
	// The full log the card names: JukaHub's when the script printed there,
	// else log.txt (below), else JukaHub's notes alone (finish).
	if len(output) > 0 {
		report.LogPath = jukaLog
	}
	finish := func(dir string) (gameDiagReport, bool) {
		failed := gameDiagPortAddOutput(&report, notes, output, script, dir)
		if report.LogPath == "" && len(notes) > 0 {
			report.LogPath = jukaLog
		}
		return report, failed
	}
	dir, rel := gameDiagPortDir(script)
	if dir == "" {
		if rel != "" {
			report.Issues = append(report.Issues, launchIssue{
				Level:  int(gameDiagSure),
				Title:  "The port's folder is missing",
				Files:  []string{rel},
				Where:  filepath.Dir(script),
				Detail: filepath.Base(script) + " runs the port from this folder. Reinstall the port from PortMaster.",
			})
		}
		return finish("")
	}
	for _, sub := range []string{"gamedata", "data", "assets", "game"} {
		data, ok := gameDiagFileIn(dir, sub)
		if !ok {
			continue
		}
		if info, err := os.Stat(data); err != nil || !info.IsDir() || gameDiagDirHasGameFiles(data, 2) {
			continue
		}
		detail := "This port runs the original game's data, which PortMaster cannot ship. Copy the files the port's instructions list into this folder."
		if p, ok := gameDiagFileIn(dir, "README.md"); ok {
			detail += " The instructions: " + p
		}
		report.Issues = append(report.Issues, launchIssue{
			Level:  int(gameDiagLikely),
			Title:  "The game's own files are not there yet",
			Where:  data,
			Detail: detail,
		})
		break
	}
	if logPath, ok := gameDiagFileIn(dir, "log.txt"); ok {
		if lines := gameDiagReadTail(logPath, started); len(lines) > 0 {
			output = gameDiagPortMergeOutput(output, lines)
			if report.LogPath == "" {
				report.LogPath = logPath
			}
		}
	}
	return finish(dir)
}

// gameDiagPortAddOutput adds what a port printed to its report: the causes
// its lines show, else the files they name as missing, and the log lines to
// read. failed: the port's own output names a cause — a line says loading
// failed, or a finding rests on one of its lines (the launch notes and the
// folder checks do not count). dir is the port's folder ("" when missing).
func gameDiagPortAddOutput(report *gameDiagReport, notes, output []string, script, dir string) bool {
	excerpt, failed := gameDiagExcerpt(output)
	found, evidence, fromOutput := gameDiagPortLogFindings(notes, output, script)
	failed = failed || fromOutput
	// The log's causes go first, so within a level they come before a data
	// folder that may only look empty.
	report.Issues = append(found, report.Issues...)
	strong := false
	for _, is := range report.Issues {
		if is.Level < int(gameDiagMaybe) {
			strong = true
			break
		}
	}
	if files := gameDiagPortMissingFiles(output); len(files) > 0 && !strong {
		report.Issues = append(report.Issues, launchIssue{
			Level:  int(gameDiagLikely),
			Title:  "The port is missing files",
			Files:  gameDiagListFiles(files, 8),
			Where:  dir,
			Detail: "Named in the port's log. Game data goes in the port's folder (often a gamedata folder inside it); a missing .so library means this build does not suit the device.",
		})
	}
	// The surest first.
	sort.SliceStable(report.Issues, func(i, j int) bool { return report.Issues[i].Level < report.Issues[j].Level })
	report.LogLines = gameDiagPortExcerpt(evidence, excerpt, output)
	return failed
}

// gameDiagPortMissingFiles pulls file names out of "No such file", "cannot
// open" and "not found" lines.
func gameDiagPortMissingFiles(lines []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range lines {
		lower := strings.ToLower(line)
		if !gameDiagHasAny(lower, []string{"no such file", "cannot open", "can't open", "couldn't open", "could not open",
			"unable to open", "failed to open", "not found", "missing"}) {
			continue
		}
		for _, tok := range gameDiagPathTokenRe.FindAllString(line, -1) {
			base := filepath.Base(strings.ReplaceAll(tok, `\`, "/"))
			ext := strings.ToLower(filepath.Ext(base))
			if ext == ".sh" || ext == ".txt" || base == "" || seen[strings.ToLower(base)] {
				continue
			}
			if _, err := strconv.ParseFloat(base, 64); err == nil {
				continue // "1.0" is a version, not a file
			}
			seen[strings.ToLower(base)] = true
			out = append(out, base)
		}
	}
	return out
}
