package main

// launchscripts.go — your own shell scripts around every game and app start,
// and the screenshot request file for scripts and paks.
//
// Folders. Beside the idle hooks (idlewatch.go: hooks/idle, …), in the
// data folder (Apps/JukaHub/data on a TrimUI):
//
//	hooks/pre-launch/*.sh            before every game and app
//	hooks/pre-launch/<console>/*.sh  before that console's games only
//	hooks/post-exit/*.sh             after every game and app
//	hooks/post-exit/<console>/*.sh   after that console's games only
//
// <console> is matched without regard to case against the console's name as
// JukaHub shows it ("Game Boy Advance"), the same name in lower case with
// dashes ("game-boy-advance") and the ROM's own folder on the card ("GBA").
// Apps use the folder "apps". Every *.sh (not hidden, a regular file, at most
// idleHookMaxScripts per folder) runs with /bin/sh in name order — the shared
// folder first, then the console's — one at a time, each at most
// launchScriptTimeout; one that overruns is stopped with everything it
// started (its process group). Their output goes to launch-scripts.log in the
// app's log folder (crashLogDir), which "Download logs" and Developer → Copy
// logs to the card pick up.
//
// The environment is the firmware's (like a game's) plus:
//
//	JUKAHUB_STAGE      pre-launch or post-exit
//	JUKAHUB_KIND       game or app
//	JUKAHUB_ROM        the game file (empty for an app)
//	JUKAHUB_APP        the app's file (empty for a game)
//	JUKAHUB_TITLE      the name the library shows
//	JUKAHUB_CONSOLE    the console's name ("Game Boy Advance"; empty for an app)
//	JUKAHUB_CONSOLE_KEY its folder form ("game-boy-advance"; "apps" for an app)
//	JUKAHUB_EMULATOR   the launcher JukaHub starts (launch.sh, a core, a program)
//	JUKAHUB_CORE       the libretro core's name when the launcher is a core
//	JUKAHUB_EXIT_CODE  post-exit only: the game's or app's exit code (-1: killed)
//	JUKAHUB_RAN_FOR    post-exit only: whole seconds it ran
//	JUKAHUB_SCREENSHOT_REQUEST / JUKAHUB_SCREENSHOT_LAST  the screenshot files
//
// A pre-launch script that exits with launchScriptCancelCode (3) cancels the
// start: the scripts after it do not run, the game does not start and a toast
// names the script. Any other non-zero code is only logged. Post-exit codes are
// only logged.
//
// The pre-launch scripts never run on the UI thread: the launch gate
// (gamelaunch_hooks.go) holds the start, a goroutine runs them, and the main
// loop starts the game again afterwards with a one-time pass — the pattern
// saveSyncDeferLaunch uses. A gate registered after this one (a profile's
// question, waiting for Wi-Fi for RetroAchievements) that holds the start
// makes the scripts run again on the next try. Post-exit scripts run on a
// worker, never on the reaper goroutine. Both stand down when Settings →
// Developer → Launch scripts is off, and cost two folder reads when the
// folders are empty.
//
// Screenshot request (Linux): while a game or app JukaHub started runs, a file
// appearing at /tmp/jukahub/screenshot.request (checked every 500 ms, only then)
// takes a screenshot of the game's screen the way Hold Menu does in games
// (menushot.go gameShotGrab), writes the PNG's path to
// /tmp/jukahub/screenshot.last and deletes the request; a failed one deletes
// screenshot.last instead. Every child JukaHub starts gets both paths in its
// environment (screenChildGroupConfigure).

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// launchScriptsKey is the Custom setting behind Settings → Developer →
	// Launch scripts; unset is On.
	launchScriptsKey = "launchScripts"

	launchStagePre  = "pre-launch"
	launchStagePost = "post-exit"

	// launchScriptsAppsFolder is the per-console folder apps use.
	launchScriptsAppsFolder = "apps"

	// launchScriptCancelCode is the exit code that cancels a start.
	launchScriptCancelCode = 3
	// launchScriptTimeout bounds one script.
	launchScriptTimeout = 5 * time.Second
	// launchScriptsSlowToast is how long the pre-launch scripts may take
	// before a toast says why the game has not started yet.
	launchScriptsSlowToast = 700 * time.Millisecond
	// launchScriptsLogMax is when launch-scripts.log becomes .log.1.
	launchScriptsLogMax = 256 << 10
	// launchScriptsLogName is the log file in the app's log folder.
	launchScriptsLogName = "launch-scripts.log"
)

// launchScriptsOn mirrors the setting for the reaper goroutine.
var launchScriptsOn atomic.Bool

// launchScriptsState is the held start: main thread only.
var launchScriptsState struct {
	pending bool
	resume  string // the game or app path allowed through once
}

// launchScriptsRunMu runs one stage's scripts at a time, so a post-exit
// script never overlaps the next start's pre-launch one.
var launchScriptsRunMu sync.Mutex

// launchScriptsLogMu serialises the log file.
var launchScriptsLogMu sync.Mutex

func init() {
	launchScriptsOn.Store(true)
	gameLaunchRegisterGate(launchScriptsGate)
	gameLaunchRegisterExited(launchScriptsGameExited)
	deferStartupTask("launch-scripts", func() { launchScriptsOn.Store(launchScriptsEnabled(appConfig)) })
}

// launchScriptsSupported reports whether scripts run here: /bin/sh on Linux.
func launchScriptsSupported() bool { return runtime.GOOS == "linux" }

// launchScriptsEnabled reports the setting; unset is On.
func launchScriptsEnabled(c *Config) bool {
	if c != nil {
		if v, ok := customValue(c.Variables.Custom, launchScriptsKey); ok {
			return coerceBool(v)
		}
	}
	return true
}

// launchScriptsSettingsRow is Settings → Developer → Launch scripts.
func launchScriptsSettingsRow() setRow {
	return setRow{
		ID: launchScriptsKey, Label: "Launch scripts", Kind: setKindToggle,
		Detail: func(c *Config) string {
			if !launchScriptsEnabled(c) {
				return "Off: scripts in data/hooks/pre-launch and post-exit are not run"
			}
			return "Runs your .sh files in data/hooks/pre-launch and post-exit around games and apps"
		},
		Visible: func(*Config) bool { return launchScriptsSupported() },
		Get:     launchScriptsEnabled,
		Set: func(c *Config, on bool) {
			setStoreBool(c, launchScriptsKey, on)
			launchScriptsOn.Store(on)
			setToast("Launch scripts: " + setOnOff(on))
		},
	}
}

// launchScriptsDir is one stage's folder, or "" without a data folder.
func launchScriptsDir(stage string) string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "hooks", stage)
}

// launchScriptsList is a stage's scripts for these console keys: the shared
// folder's in name order, then each matching console folder's. Disk reads
// only; empty when the folder is missing or empty.
func launchScriptsList(stage string, keys []string) []string {
	dir := launchScriptsDir(stage)
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return nil
	}
	var out []string
	for _, name := range idleHookScriptNames(entries) {
		out = append(out, filepath.Join(dir, name))
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || !launchScriptsKeyMatch(e.Name(), keys) {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		subEntries, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, name := range idleHookScriptNames(subEntries) {
			out = append(out, filepath.Join(sub, name))
		}
	}
	return out
}

// launchScriptsKeyMatch reports whether a folder name is one of keys,
// without regard to case. Pure.
func launchScriptsKeyMatch(folder string, keys []string) bool {
	for _, k := range keys {
		if k != "" && strings.EqualFold(folder, k) {
			return true
		}
	}
	return false
}

// launchScriptsJob is one start's facts for the environment.
type launchScriptsJob struct {
	kind       string // "game" or "app"
	rom        string
	app        string
	title      string
	console    string
	consoleKey string
	emulator   string
	core       string
	keys       []string // the per-console folder names that match
}

// launchScriptsGameJob describes a library game's start.
func launchScriptsGameJob(game libraryGame, launcher string) launchScriptsJob {
	console := gameControlsConsole(game.System)
	title := strings.TrimSpace(game.DisplayTitle)
	if title == "" {
		title = game.Title
	}
	j := launchScriptsJob{
		kind: "game", rom: game.Path, title: title, console: console,
		consoleKey: raTuneSlug(console), emulator: launcher,
	}
	switch ext := strings.ToLower(filepath.Ext(launcher)); ext {
	case ".so", ".dll":
		j.core = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(launcher), ext), "_libretro")
	}
	j.keys = []string{console, strings.TrimSpace(game.System), j.consoleKey}
	if game.Path != "" {
		j.keys = append(j.keys, filepath.Base(filepath.Dir(game.Path)))
	}
	return j
}

// launchScriptsAppJob describes an app's start.
func launchScriptsAppJob(path string) launchScriptsJob {
	title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if strings.EqualFold(title, "launch") {
		// Apps/<Name>/launch.sh: the folder is the app's name.
		title = filepath.Base(filepath.Dir(path))
	}
	return launchScriptsJob{
		kind: "app", app: path, title: title, consoleKey: launchScriptsAppsFolder,
		emulator: path, keys: []string{launchScriptsAppsFolder},
	}
}

// env is the job's environment for one stage, on top of base.
func (j launchScriptsJob) env(base []string, stage string, exitCode int, ranFor time.Duration) []string {
	out := append([]string(nil), base...)
	out = append(out,
		"JUKAHUB_STAGE="+stage,
		"JUKAHUB_KIND="+j.kind,
		"JUKAHUB_ROM="+j.rom,
		"JUKAHUB_APP="+j.app,
		"JUKAHUB_TITLE="+j.title,
		"JUKAHUB_CONSOLE="+j.console,
		"JUKAHUB_CONSOLE_KEY="+j.consoleKey,
		"JUKAHUB_EMULATOR="+j.emulator,
		"JUKAHUB_CORE="+j.core,
	)
	if stage == launchStagePost {
		out = append(out,
			"JUKAHUB_EXIT_CODE="+strconv.Itoa(exitCode),
			"JUKAHUB_RAN_FOR="+strconv.Itoa(int(ranFor/time.Second)),
		)
	}
	return shotRequestChildEnv(out)
}

// launchScriptsResult is what a stage's run came to.
type launchScriptsResult struct {
	cancelled bool
	by        string // the script that cancelled
}

// launchScriptsExitCode turns a Wait error into an exit code: 0 for none, the
// code for an exit, -1 for a signal or a failure to wait.
func launchScriptsExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// ── Games ────────────────────────────────────────────────────────────────

// launchScriptsGate holds a game's start while its pre-launch scripts run
// (gameLaunchRegisterGate). Main thread.
func launchScriptsGate(game libraryGame, launcher string) (bool, error) {
	if !launchScriptsSupported() || !launchScriptsEnabled(appConfig) || strings.TrimSpace(launcher) == "" {
		// No emulator: the launch path says so itself, without scripts.
		return false, nil
	}
	if launchScriptsState.resume != "" && launchScriptsState.resume == game.Path {
		launchScriptsState.resume = ""
		return false, nil
	}
	job := launchScriptsGameJob(game, launcher)
	scripts := launchScriptsList(launchStagePre, job.keys)
	if len(scripts) == 0 {
		return false, nil
	}
	if launchScriptsState.pending {
		return false, errors.New("the launch scripts for the last start are still running — one moment")
	}
	launchScriptsHold(job, scripts, func() {
		launchScriptsState.resume = game.Path
		err := launchLibraryGame(game, launcher)
		launchScriptsState.resume = ""
		if err != nil {
			ShowToast("Cannot launch "+job.title+": "+err.Error(), ToastKindError)
		}
	})
	return true, nil
}

// launchScriptsGameExited runs the post-exit scripts on a worker
// (gameLaunchRegisterExited; the reaper goroutine).
func launchScriptsGameExited(game libraryGame, launcher string, ranFor time.Duration, waitErr error) {
	if !launchScriptsSupported() || !launchScriptsOn.Load() {
		return
	}
	launchScriptsAfter(launchScriptsGameJob(game, launcher), launchScriptsExitCode(waitErr), ranFor)
}

// ── Apps ─────────────────────────────────────────────────────────────────

// launchScriptsAppGate holds an app's start while its pre-launch scripts run;
// true when it did (launchInstalledApp starts it again afterwards). Main
// thread.
func launchScriptsAppGate(path string) bool {
	if !launchScriptsSupported() || !launchScriptsEnabled(appConfig) {
		return false
	}
	if launchScriptsState.resume != "" && launchScriptsState.resume == path {
		launchScriptsState.resume = ""
		return false
	}
	job := launchScriptsAppJob(path)
	scripts := launchScriptsList(launchStagePre, job.keys)
	if len(scripts) == 0 {
		return false
	}
	if launchScriptsState.pending {
		ShowToast("The launch scripts for the last start are still running — one moment", ToastKindInfo)
		return true
	}
	launchScriptsHold(job, scripts, func() {
		launchScriptsState.resume = path
		launchInstalledApp(path)
		launchScriptsState.resume = ""
	})
	return true
}

// launchScriptsAppExited runs an app's post-exit scripts on a worker (the
// app's reaper goroutine, apps.go).
func launchScriptsAppExited(path string, startedAt time.Time, waitErr error) {
	if !launchScriptsSupported() || !launchScriptsOn.Load() {
		return
	}
	launchScriptsAfter(launchScriptsAppJob(path), launchScriptsExitCode(waitErr), time.Since(startedAt))
}

// ── Running them ─────────────────────────────────────────────────────────

// launchScriptsHold runs a held start's pre-launch scripts on a goroutine and
// calls start on the main thread afterwards, unless a script cancelled it or
// something else took the screen meanwhile. Main thread.
func launchScriptsHold(job launchScriptsJob, scripts []string, start func()) {
	launchScriptsState.pending = true
	done := make(chan struct{})
	go func() {
		slow := time.NewTimer(launchScriptsSlowToast)
		defer slow.Stop()
		select {
		case <-done:
		case <-slow.C:
			tbxPost(func() {
				if launchScriptsState.pending {
					ShowToast("Running your launch scripts…", ToastKindInfo)
				}
			})
		}
	}()
	go func() {
		var res launchScriptsResult
		func() {
			defer func() {
				if r := recover(); r != nil {
					logWarn("scripts", "pre-launch scripts failed: %v", r)
				}
			}()
			res = launchScriptsRunStage(job, launchStagePre, scripts, 0, 0)
		}()
		close(done)
		tbxPost(func() {
			launchScriptsState.pending = false
			if res.cancelled {
				ShowToast(job.title+" was not started: the launch script "+res.by+" said no", ToastKindWarning)
				return
			}
			if gameScreenSuspended() || exitRequested.Load() {
				return // something else took the screen meanwhile, or JukaHub is quitting
			}
			start()
		})
	}()
}

// launchScriptsAfter lists and runs a finished start's post-exit scripts on a
// goroutine of their own. Any goroutine.
func launchScriptsAfter(job launchScriptsJob, exitCode int, ranFor time.Duration) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("scripts", "post-exit scripts failed: %v", r)
			}
		}()
		scripts := launchScriptsList(launchStagePost, job.keys)
		if len(scripts) == 0 {
			return
		}
		launchScriptsRunStage(job, launchStagePost, scripts, exitCode, ranFor)
	}()
}

// launchScriptsRunStage runs scripts in order, one stage at a time across the
// app. A pre-launch script exiting launchScriptCancelCode ends the run.
func launchScriptsRunStage(job launchScriptsJob, stage string, scripts []string, exitCode int, ranFor time.Duration) launchScriptsResult {
	launchScriptsRunMu.Lock()
	defer launchScriptsRunMu.Unlock()
	env := job.env(screenChildEnvFor(os.Environ(), childEnvAppDir()), stage, exitCode, ranFor)
	for _, path := range scripts {
		code, took, out, err := launchScriptRunOne(path, env)
		name := launchScriptsShortName(path)
		launchScriptsLog(stage, job, name, code, took, out, err)
		if stage == launchStagePre && code == launchScriptCancelCode {
			logInfo("scripts", "%s cancelled the start of %s", name, job.title)
			return launchScriptsResult{cancelled: true, by: name}
		}
	}
	return launchScriptsResult{}
}

// launchScriptsShortName is a script's name below the stage folder
// ("GBA/fan.sh"). Pure.
func launchScriptsShortName(path string) string {
	dir := filepath.Base(filepath.Dir(path))
	if dir == launchStagePre || dir == launchStagePost {
		return filepath.Base(path)
	}
	return dir + "/" + filepath.Base(path)
}

// launchScriptsLog notes one script's run in JukaHub's log and appends its
// output to launch-scripts.log.
func launchScriptsLog(stage string, job launchScriptsJob, name string, code int, took time.Duration, out []byte, err error) {
	took = took.Round(10 * time.Millisecond)
	status := "exit " + strconv.Itoa(code)
	switch {
	case errors.Is(err, errLaunchScriptTimeout):
		status = "stopped after " + launchScriptTimeout.String()
		logWarn("scripts", "%s %s took longer than %s and was stopped", stage, name, launchScriptTimeout)
	case err != nil && code < 0:
		status = "failed: " + err.Error()
		logWarn("scripts", "%s %s could not run: %v", stage, name, err)
	case code != 0 && !(stage == launchStagePre && code == launchScriptCancelCode):
		logWarn("scripts", "%s %s exited %d after %s", stage, name, code, took)
	default:
		logInfo("scripts", "%s %s ran in %s (exit %d)", stage, name, took, code)
	}
	dir, derr := crashLogDir()
	if derr != nil || dir == "" {
		return
	}
	path := filepath.Join(dir, launchScriptsLogName)
	launchScriptsLogMu.Lock()
	defer launchScriptsLogMu.Unlock()
	if st, serr := os.Stat(path); serr == nil && st.Size() > launchScriptsLogMax {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	f, ferr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if ferr != nil {
		return
	}
	defer f.Close()
	what := job.title
	if what == "" {
		what = job.kind
	}
	fmt.Fprintf(f, "[%s] %s %s for %s %q: %s in %s\n", time.Now().Format("2006-01-02 15:04:05"), stage, name, job.kind, what, status, took)
	if text := strings.TrimRight(string(out), "\r\n"); text != "" {
		for _, line := range strings.Split(text, "\n") {
			fmt.Fprintf(f, "    %s\n", redactLogSecrets(line))
		}
	}
}

// errLaunchScriptTimeout marks a script that overran launchScriptTimeout.
var errLaunchScriptTimeout = errors.New("timed out")

// launchScriptOutputMax bounds what one script's output is kept of.
const launchScriptOutputMax = 8 << 10

// launchScriptOutput keeps the start of a script's output. exec serialises
// the writes when stdout and stderr are the same writer.
type launchScriptOutput struct {
	b []byte
}

func (o *launchScriptOutput) Write(p []byte) (int, error) {
	if room := launchScriptOutputMax - len(o.b); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		o.b = append(o.b, p[:room]...)
	}
	return len(p), nil
}
