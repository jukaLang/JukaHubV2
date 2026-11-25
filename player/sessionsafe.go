package main

// sessionsafe.go — "Never lose a session": the low-battery guard and the
// automatic power offs, extended from RetroArch to the standalone emulators.
//
// RetroArch has always been protected: at 3% the guard asks it to save a state
// and quit through its network commands, syncs and suspends (lowbattery.go),
// and the sleep's power off and Power off itself do the same before the device
// goes down (quickresume.go). Every other emulator JukaHub starts — PPSSPP,
// DraStic, Flycast, Mupen64Plus, a port, a Wine game — was left out: the
// guard did not even count it as a game, so the battery simply ran out under
// it, mid-write, and the sleep's power off slept again forever.
//
// Now every one of those paths runs the same steps for them:
//
//  1. find which emulator the game's process group runs (the recipe table,
//     sessionsafe_recipes.go) and read its own config: does it write a save
//     state as it quits, and does it load it again at the next start;
//  2. ask it to quit politely (SIGTERM or SIGINT to the emulator's own
//     processes, and SIGCONT for one paused under the in-game menu; an
//     unknown program or Wine: to the whole group, as the exit path does):
//     an emulator with its "save state on exit" option writes the state
//     now, and every SDL program flushes the game's own saves on the way
//     out. Its launch script then finishes by itself (sessionSafeScriptGrace)
//     before its group is asked too;
//  3. wait, bounded (sessionSafeSaveWait / sessionSafeDefaultWait, then one
//     more nudge with the other signal and sessionSafeNudgeWait);
//  4. verify the save: a state file with the emulator's extension, non-empty
//     and written since the request, in the folders it keeps them in;
//  5. hand back to the caller, which syncs and then suspends or powers off.
//
// SIGKILL is never sent here: an emulator that ignores both signals is still
// running when the caller suspends (the RAM keeps it) or powers off (the
// teardown ends it), exactly as a RetroArch that ignored QUIT always was.
//
// JukaHub's own PPSSPP build saves on request instead, before step 2:
// SIGUSR1 asks it for a state at the file its launch named
// (JUKAHUB_PPSSPP_SAVE_TO, ppsspp_resume.go), its answer file says when the
// state is written, and only then is it asked to quit. The game's next start
// is given that state (PPSSPP's --state=), so it resumes. Which family of
// game gets which step at the battery, the sleep's power off and Power off is
// instantresume.go's plan; a game nothing can save is held asleep at the
// battery instead of closed there.
//
// The paths:
//   - Battery at 3% (protectLowBattery): a standalone game now counts as a
//     game, and is closed as above before the sync and the suspend. An app
//     (KOReader, a tool from the Apps screen) is not a game and is left
//     alone, as before (sessionSafeIsGame).
//   - Battery at 10% (sessionSafeLowBatteryTick): once per launch, an
//     emulator that cannot save a state by itself gets the warning "save in
//     the game now", with a buzz — the toast is under the game until it ends.
//   - Power off after sleeping (quickSaveHeldGame): an emulator whose save on
//     exit is on is woken just long enough to save and quit, then the device
//     powers off. One that cannot save still sleeps again, as before — unless
//     the battery is down to 5%, when a clean quit and a power off beat dying
//     asleep.
//   - Power off with a game running (quickPowerOffSavesFirst): the same quit,
//     with the emulator's own time to save, before the teardown's short one.
//
// What was running and which state was written is kept in
// sessionsafe-last.json in the data folder. A state the emulator loads by
// itself (PPSSPP's AutoLoadSaveState, Flycast's AutoLoadState, Mednafen's
// autosave) also gets the power-on resume marker, so Resume game at power on
// starts it again through the same launcher; the emulator puts it back where
// it was. A state it does not load by itself is named in the notification
// instead: it is in that emulator's own Load State menu. The record is
// dropped the next time that game starts.
//
// Settings → Power & battery → Protect standalone emulators turns it off.
// Nothing here was run on a device from this desk (backlog SAFE-HW-1).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// sessionSafeKey is the setting (default on).
	sessionSafeKey = "protectStandaloneEmus"
	// sessionSafeWarnPct is where an emulator that cannot save gets its warning.
	sessionSafeWarnPct = 10
	// sessionSafeWarnLead keeps the warning that far above a save level picked
	// in Settings → Power (batterywarn.go), so it still comes before the save.
	sessionSafeWarnLead = 5
	// sessionSafeNudgeWait is the second signal's wait.
	sessionSafeNudgeWait = 3 * time.Second
	// sessionSafeStateSlack lets a state written within a clock tick of the
	// request count.
	sessionSafeStateSlack = 2 * time.Second
	// sessionSafeDirLimit bounds one state folder's listing.
	sessionSafeDirLimit = 4096
	// sessionSafeRescan is how often an unrecognised game is looked at again
	// (its launch script may not have started the emulator yet).
	sessionSafeRescan = 5 * time.Second
	// sessionSafeMarkerName is the record in the data folder.
	sessionSafeMarkerName = "sessionsafe-last.json"
	// sessionSafeRequestWait bounds the wait for JukaHub's PPSSPP build to
	// answer a save request; sessionSafeSettleWait the wait for the state to
	// stop growing after it, looked at every sessionSafeSettleGap.
	sessionSafeRequestWait = 8 * time.Second
	sessionSafeSettleWait  = 3 * time.Second
	sessionSafeSettleGap   = 300 * time.Millisecond
	// sessionSafeDoneSuffix names the answer file beside a requested state
	// ("ok" or "fail", written by the build once PPSSPP has saved).
	sessionSafeDoneSuffix = ".done"
)

// sessionSafeProc is one process of a running game, as the /proc scan saw it.
type sessionSafeProc struct {
	pid   int
	comm  string
	argv  []string
	exe   string
	start uint64
}

// sessionSafeTarget is one of the emulator's own processes, with its start
// time (/proc/PID/stat), so a pid recycled meanwhile is never signalled.
type sessionSafeTarget struct {
	pid   int
	start uint64
}

// sessionSafeRun is the running game: which emulator, and what its config says.
type sessionSafeRun struct {
	recipe *sessionSafeRecipe
	class  sessionSafeClass
	pid    int
	cmds   []*exec.Cmd
	// targets are the emulator's own processes, asked first; none: the
	// whole group is (an unknown program, Wine).
	targets []sessionSafeTarget
	// bases are the emulator's HOME, working folder and program folder.
	bases      []string
	saveOnQuit bool
	autoLoad   bool
	config     string // the config the flags came from ("" when none)
	// saveTo is where JukaHub's own PPSSPP build writes a state on request
	// ("" for any other program), and saver the process that takes it.
	saveTo string
	saver  sessionSafeTarget
}

// label names the emulator for the user.
func (r sessionSafeRun) label() string {
	if r.recipe == nil {
		return "The game"
	}
	return r.recipe.Label
}

// sessionSafeResult is what ending one game did.
type sessionSafeResult struct {
	saved  bool
	state  string
	exited bool
	why    string
}

// sessionSafeLaunch is a library launch, kept by its process.
type sessionSafeLaunch struct {
	game     libraryGame
	launcher string
}

// sessionSafeLaunches maps a running game's process to its library launch
// (gamelaunch_hooks.go). Any goroutine.
var sessionSafeLaunches struct {
	mu    sync.Mutex
	byPid map[int]sessionSafeLaunch
}

// sessionSafeCache is the warning's detection, made on a goroutine. Any
// goroutine, under mu.
var sessionSafeCache struct {
	mu   sync.Mutex
	key  int
	at   time.Time
	busy bool
	have bool
	run  sessionSafeRun
}

// sessionSafeWarnedKey is the launch (its first process) the 10% warning was
// given for. Main thread.
var sessionSafeWarnedKey int

func init() {
	gameLaunchRegisterStarted(sessionSafeStarted)
	gameLaunchRegisterExited(sessionSafeExited)
}

// ── Settings ─────────────────────────────────────────────────────────────

// sessionSafeEnabled reads the switch; on unless turned off.
func sessionSafeEnabled(c *Config) bool {
	if c != nil {
		if v, ok := customValue(c.Variables.Custom, sessionSafeKey); ok {
			return coerceBool(v)
		}
	}
	return true
}

// sessionSafeSettingsRow is Settings → Power & battery → Protect standalone
// emulators.
func sessionSafeSettingsRow() setRow {
	return setRow{
		ID: sessionSafeKey, Label: "Protect standalone emulators", Kind: setKindToggle,
		Detail:  sessionSafeDetail,
		Visible: func(*Config) bool { return runtime.GOOS == "linux" },
		Get:     sessionSafeEnabled,
		Set: func(c *Config, on bool) {
			setStoreBool(c, sessionSafeKey, on)
			setToast("Protect standalone emulators: " + setOnOff(on))
		},
	}
}

// sessionSafeDetail says what the row does, or what it did last.
func sessionSafeDetail(c *Config) string {
	if !sessionSafeEnabled(c) {
		return "Off: at 3% and at a power off, only RetroArch games are saved and closed first"
	}
	if m, ok := sessionSafeLastMarker(); ok {
		return sessionSafeMarkerLine(m)
	}
	return "PPSSPP, DraStic, Flycast and others save where they can and quit cleanly before a suspend or power off"
}

// sessionSafeMarkerLine is the record as one line: "Last: Tekken 6 in PPSSPP — state saved".
func sessionSafeMarkerLine(m sessionSafeMarker) string {
	what := m.Title
	if what == "" {
		what = "a game"
	}
	how := "closed cleanly, no save state"
	switch {
	case m.State != "" && m.AutoLoad:
		how = "state saved, resumes by itself"
	case m.State != "":
		how = "state saved in its Load State menu"
	}
	if m.Emulator == sessionSafeUnknown.ID || m.Label == "" {
		return fmt.Sprintf("Last: %s — %s", what, how)
	}
	return fmt.Sprintf("Last: %s in %s — %s", what, m.Label, how)
}

// ── Launch hooks ─────────────────────────────────────────────────────────

// sessionSafeStarted remembers a library launch by its process, and drops the
// record of an earlier save of the same game: this start is its resume (or
// its fresh start). Main thread; the file work runs on its own.
func sessionSafeStarted(game libraryGame, launcher string, cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	sessionSafeLaunches.mu.Lock()
	if sessionSafeLaunches.byPid == nil {
		sessionSafeLaunches.byPid = make(map[int]sessionSafeLaunch, 2)
	}
	sessionSafeLaunches.byPid[cmd.Process.Pid] = sessionSafeLaunch{game: game, launcher: launcher}
	sessionSafeLaunches.mu.Unlock()
	go sessionSafeNoteStart(game.Path)
}

// sessionSafeNoteStart drops the record of an earlier save of the game at
// path: this start is its resume (or its fresh start). A start JukaHub closes
// for calls it too (hakux_handoff.go). Any goroutine.
func sessionSafeNoteStart(path string) {
	m, ok := sessionSafeLastMarker()
	if !ok || m.Path == "" || m.Path != path {
		return
	}
	switch {
	case m.Resume != "":
		logInfo("power", "%s starts again from the state JukaHub saved for it in %s", filepath.Base(path), m.Label)
	case m.AutoLoad:
		logInfo("power", "%s starts again in %s, which loads the state it saved by itself", filepath.Base(path), m.Label)
	case m.State != "":
		logInfo("power", "%s starts again; the state %s saved is in its Load State menu (%s)", filepath.Base(path), m.Label, filepath.Base(m.State))
	}
	sessionSafeClearLast()
}

// sessionSafeExited forgets a launch whose process group is gone. The
// reaper goroutine. This hook runs after the screen was handed back, so the
// same game started again meanwhile is a live screen child and is kept.
func sessionSafeExited(game libraryGame, _ string, _ time.Duration, _ error) {
	live := make(map[int]bool, 2)
	for _, cmd := range liveScreenChildren() {
		if cmd != nil && cmd.Process != nil {
			live[cmd.Process.Pid] = true
		}
	}
	sessionSafeLaunches.mu.Lock()
	for pid, l := range sessionSafeLaunches.byPid {
		if l.game.Path == game.Path && !live[pid] {
			delete(sessionSafeLaunches.byPid, pid)
		}
	}
	sessionSafeLaunches.mu.Unlock()
}

// sessionSafeLaunchFor is the library launch behind the running children.
func sessionSafeLaunchFor(cmds []*exec.Cmd) (sessionSafeLaunch, bool) {
	sessionSafeLaunches.mu.Lock()
	defer sessionSafeLaunches.mu.Unlock()
	for _, cmd := range cmds {
		if cmd == nil || cmd.Process == nil {
			continue
		}
		if l, ok := sessionSafeLaunches.byPid[cmd.Process.Pid]; ok {
			return l, true
		}
	}
	return sessionSafeLaunch{}, false
}

// sessionSafeStartedAsGame reports whether the screen children were started
// as a game, not as an app: a library launch, a game profile, or a launcher,
// argument or working folder in a game folder (sessionSafeGameDirs). Cheap:
// two registries and the children's arguments. Any goroutine.
func sessionSafeStartedAsGame(cmds []*exec.Cmd) bool {
	if _, ok := sessionSafeLaunchFor(cmds); ok {
		return true
	}
	pids := make(map[int]bool, len(cmds))
	for _, cmd := range cmds {
		if cmd == nil {
			continue
		}
		if cmd.Process != nil {
			pids[cmd.Process.Pid] = true
		}
		if sessionSafeGamePath(cmd.Dir) || sessionSafeGamePath(cmd.Path) {
			return true
		}
		for _, a := range cmd.Args {
			if sessionSafeGamePath(a) {
				return true
			}
		}
	}
	gameProfileRunning.mu.Lock()
	defer gameProfileRunning.mu.Unlock()
	for _, pid := range gameProfileRunning.pids {
		if pids[pid] {
			return true
		}
	}
	return false
}

// sessionSafeIsGame reports whether a detected run is a game: RetroArch, an
// emulator the table names, or anything started as a game. An app (KOReader,
// a tool from the Apps screen) is not: the 3% guard does not close it, which
// would throw away unsaved work in it, and the 10% warning does not name it.
func sessionSafeIsGame(run sessionSafeRun) bool {
	if run.class == sessionSafeRetroArch || (run.recipe != nil && run.recipe != &sessionSafeUnknown) {
		return true
	}
	return sessionSafeStartedAsGame(run.cmds)
}

// ── Detection ────────────────────────────────────────────────────────────

// sessionSafeClassify picks the program that decides what can be done for a
// game's processes: RetroArch anywhere in the group leaves the game to
// lowbattery.go; otherwise the known emulator that can do the most; otherwise
// the unknown recipe (a polite interrupt, no save). Pure.
func sessionSafeClassify(procs []sessionSafeProc) (sessionSafeProc, *sessionSafeRecipe, sessionSafeClass) {
	if len(procs) == 0 {
		return sessionSafeProc{}, nil, sessionSafeNone
	}
	var best sessionSafeProc
	var recipe *sessionSafeRecipe
	rank := 0
	for _, p := range procs {
		argv0 := ""
		if len(p.argv) > 0 {
			argv0 = p.argv[0]
		}
		if retroArchCmdline([]byte(argv0)) || retroArchCmdline([]byte(p.comm)) || retroArchCmdline([]byte(p.exe)) {
			return p, nil, sessionSafeRetroArch
		}
		if sessionSafeIsShell(p) {
			continue
		}
		r, ok := sessionSafeProcRecipe(p)
		if !ok {
			continue
		}
		if k := sessionSafeClassRank(r.Class); k > rank {
			best, recipe, rank = p, r, k
		}
	}
	if recipe == nil {
		return procs[0], &sessionSafeUnknown, sessionSafeQuitOnly
	}
	return best, recipe, recipe.Class
}

// sessionSafeIsShell reports a process whose program (exe, else argv[0]) is
// a shell: a launch script, whatever it is called. Pure.
func sessionSafeIsShell(p sessionSafeProc) bool {
	name := p.exe
	if name == "" && len(p.argv) > 0 {
		name = p.argv[0]
	}
	name = strings.TrimSuffix(strings.TrimSpace(name), " (deleted)")
	return sessionSafeShells[strings.ToLower(closeAllBase(name))]
}

// sessionSafeTargets are recipe's emulator processes among procs, the ones
// the polite signal goes to first. None for a program no row names, or one
// ended as a whole group (Wine): the exit path's interrupt to the group is
// what is known to work for those. Pure.
func sessionSafeTargets(procs []sessionSafeProc, recipe *sessionSafeRecipe) []sessionSafeTarget {
	if recipe == nil || recipe == &sessionSafeUnknown || recipe.WholeGroup {
		return nil
	}
	var out []sessionSafeTarget
	for _, p := range procs {
		if p.pid <= 1 || sessionSafeIsShell(p) {
			continue
		}
		if r, ok := sessionSafeProcRecipe(p); ok && r == recipe {
			out = append(out, sessionSafeTarget{pid: p.pid, start: p.start})
		}
	}
	return out
}

// sessionSafeDetect finds the emulator the children run and reads its config.
// Off the main thread: it reads /proc and the emulator's files.
func sessionSafeDetect(cmds []*exec.Cmd) sessionSafeRun {
	run := sessionSafeRun{cmds: cmds}
	procs := sessionSafeScan(cmds)
	best, recipe, class := sessionSafeClassify(procs)
	run.recipe, run.class, run.pid = recipe, class, best.pid
	if recipe == nil || class == sessionSafeNone || class == sessionSafeRetroArch {
		return run
	}
	run.targets = sessionSafeTargets(procs, recipe)
	if best.pid > 0 {
		run.bases = sessionSafeProcBases(best.pid)
	}
	if recipe.Flags != nil {
		text, path := sessionSafeReadConfig(run.bases, recipe.Configs)
		run.saveOnQuit, run.autoLoad = recipe.Flags(text)
		run.config = path
	}
	// JukaHub's own PPSSPP build saves on request, into a file JukaHub gives
	// the game's next start (instantresume.go): that is a resume too.
	if recipe.ID == "ppsspp" && best.pid > 0 {
		if to := sessionSafeRequestTarget(best.pid); to != "" {
			run.saveTo, run.saver = to, sessionSafeTarget{pid: best.pid, start: best.start}
			run.saveOnQuit, run.autoLoad = true, true
		}
	}
	return run
}

// sessionSafeReadConfig reads the first config found under the bases.
func sessionSafeReadConfig(bases, rels []string) (text, path string) {
	for _, base := range bases {
		for _, rel := range rels {
			p := filepath.Join(base, rel)
			data, err := sessionSafeReadLimited(p, sessionSafeConfigLimit)
			if err == nil {
				return string(data), p
			}
		}
	}
	return "", ""
}

// sessionSafeReadLimited reads at most limit bytes of a regular file.
func sessionSafeReadLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.IsDir() {
		return nil, errors.New("not a file")
	}
	return io.ReadAll(io.LimitReader(f, limit))
}

// sessionSafeChildKey names a launch by its smallest screen-child pid; 0 when
// nothing runs.
func sessionSafeChildKey(cmds []*exec.Cmd) int {
	key := 0
	for _, cmd := range cmds {
		if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
			continue
		}
		if key == 0 || cmd.Process.Pid < key {
			key = cmd.Process.Pid
		}
	}
	return key
}

// sessionSafeDetectCached is sessionSafeDetect for the once-a-second tick: the
// scan runs on a goroutine and ok is false until it has an answer for this
// launch. An unrecognised game is looked at again every sessionSafeRescan.
func sessionSafeDetectCached(cmds []*exec.Cmd, key int) (sessionSafeRun, bool) {
	c := &sessionSafeCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.have && c.key == key && (c.run.recipe != &sessionSafeUnknown || time.Since(c.at) < sessionSafeRescan) {
		return c.run, true
	}
	if !c.busy {
		c.busy = true
		go func() {
			run := sessionSafeDetect(cmds)
			c.mu.Lock()
			c.key, c.run, c.at, c.have, c.busy = key, run, time.Now(), true, false
			c.mu.Unlock()
		}()
	}
	return sessionSafeRun{}, false
}

// sessionSafeGameRunning reports a game of JukaHub's on screen that this file
// can protect, for the low-battery guard's decision: a screen child started
// as a game, or one whose emulator the table names (the cached scan; not
// known yet is "no", the next tick asks again). An app is not a game here.
// Main thread; cheap.
func sessionSafeGameRunning(c *Config) bool {
	if runtime.GOOS != "linux" || !sessionSafeEnabled(c) {
		return false
	}
	cmds := liveScreenChildren()
	if len(cmds) == 0 {
		return false
	}
	if sessionSafeStartedAsGame(cmds) {
		return true
	}
	run, ok := sessionSafeDetectCached(cmds, sessionSafeChildKey(cmds))
	return ok && sessionSafeIsGame(run)
}

// ── The steps ────────────────────────────────────────────────────────────

// sessionSafeEnd asks the game to quit (its emulator saving on the way out
// where it can), waits bounded and checks the save. tag names the caller in
// the log. Off the main thread: it waits.
func sessionSafeEnd(tag string, run sessionSafeRun) sessionSafeResult {
	var res sessionSafeResult
	r := run.recipe
	if r == nil {
		r = &sessionSafeUnknown
	}
	wait := r.Wait
	if wait <= 0 {
		wait = sessionSafeDefaultWait
	}
	label := run.label()
	// A state on request (JukaHub's PPSSPP build) is written before the quit;
	// one on exit is looked for in the emulator's state folders after it.
	trySave := run.class == sessionSafeOnQuit && run.saveOnQuit && run.saveTo == ""
	how := "save on exit " + setOnOff(trySave)
	if run.saveTo != "" {
		how = "save on request"
	}
	logInfo("power", "%s: closing %s (%s, config %q)", tag, label, how, run.config)
	var dirs []sessionSafeStateDir
	if trySave {
		dirs = sessionSafeOpenStateDirs(run)
		defer sessionSafeCloseStateDirs(dirs)
	}
	since := time.Now()
	if run.saveTo != "" && sessionSafeRequestSave(tag, run) {
		res.state, res.saved = run.saveTo, true
	}
	// JukaHub ends the game here, not the game itself (gamelaunch_watch.go).
	gameLaunchNoteStopAll()
	other := sessionSafeSigInt
	if r.Signal == sessionSafeSigInt {
		other = sessionSafeSigTerm
	}
	if len(run.targets) > 0 {
		res.exited = sessionSafeEndTargets(tag, label, run, r.Signal, other, wait)
	} else {
		sessionSafeSignalGroups(run.cmds, r.Signal)
		res.exited = sessionSafeWaitGone(run.cmds, wait)
		if !res.exited {
			logWarn("power", "%s: %s still running after %s; asking once more", tag, label, wait)
			sessionSafeSignalGroups(run.cmds, other)
			res.exited = sessionSafeWaitGone(run.cmds, sessionSafeNudgeWait)
		}
	}
	if trySave {
		res.state = sessionSafeNewestState(dirs, r.StateExt, since)
		res.saved = res.state != ""
	}
	if res.saved {
		logInfo("power", "%s: %s saved %s", tag, label, res.state)
	}
	switch {
	case !res.exited:
		// Saved or not, a game still running is the caller's to hold.
		res.why = label + " did not quit"
	case res.saved:
	case trySave || run.saveTo != "":
		res.why = label + " wrote no new save state"
	case run.class == sessionSafeStream:
		res.why = "a stream: the game goes on on the other machine"
	case run.class == sessionSafeOnQuit:
		res.why = label + "'s " + r.Option + " is off"
	default:
		res.why = label + " cannot save a state by itself"
	}
	if !res.exited {
		logWarn("power", "%s: %s did not quit within %s", tag, label, wait+sessionSafeNudgeWait)
	} else if !res.saved {
		logInfo("power", "%s: %s closed (%s)", tag, label, res.why)
	}
	return res
}

// sessionSafeEndTargets asks the emulator itself to quit, not its launch
// script: the script then runs its own lines after the game (an unmount, a
// governor put back) and ends by itself, where a signal to the whole group
// ends an untrapped script at once — and would miss an emulator that left
// the group. The group is asked only if it is still there after
// sessionSafeScriptGrace; an emulator that ignored the signal gets the other
// one, with its group. Bounded: wait, then sessionSafeNudgeWait plus
// sessionSafeScriptGrace at most. It reports whether everything is gone.
func sessionSafeEndTargets(tag, label string, run sessionSafeRun, sig, other sessionSafeSignal, wait time.Duration) bool {
	for _, cmd := range run.cmds {
		// A game paused under the in-game menu or an alarm acts on the
		// request at once instead of when it is killed.
		_ = alarmSignalChild(cmd, false)
	}
	sessionSafeSignalTargets(run.targets, sig)
	if !sessionSafeWaitTargetsGone(run.targets, wait) {
		logWarn("power", "%s: %s still running after %s; asking once more", tag, label, wait)
		sessionSafeSignalTargets(run.targets, other)
		sessionSafeSignalGroups(run.cmds, other)
		return sessionSafeWaitTargetsGone(run.targets, sessionSafeNudgeWait) && sessionSafeWaitGone(run.cmds, sessionSafeScriptGrace)
	}
	if sessionSafeWaitGone(run.cmds, sessionSafeScriptGrace) {
		return true
	}
	logInfo("power", "%s: %s quit; its launcher is still running, asking it to stop", tag, label)
	sessionSafeSignalGroups(run.cmds, sig)
	return sessionSafeWaitGone(run.cmds, sessionSafeNudgeWait)
}

// sessionSafeWaitTargetsGone polls until none of the emulator's processes
// runs (a zombie waiting for its parent writes nothing more) or the budget
// runs out.
func sessionSafeWaitTargetsGone(targets []sessionSafeTarget, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		alive := false
		for _, t := range targets {
			if sessionSafeTargetAlive(t) {
				alive = true
				break
			}
		}
		if !alive {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// sessionSafeWaitGone polls until every child's group is gone or the budget
// runs out.
func sessionSafeWaitGone(cmds []*exec.Cmd, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		alive := false
		for _, cmd := range cmds {
			if screenChildGroupAlive(cmd) {
				alive = true
				break
			}
		}
		if !alive {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// sessionSafeStateName reports a state file name with one of exts. Pure.
func sessionSafeStateName(name string, exts []string) bool {
	if name == "" || name[0] == '.' {
		return false
	}
	lower := strings.ToLower(name)
	for _, ext := range exts {
		if strings.HasSuffix(lower, strings.ToLower(ext)) {
			return true
		}
	}
	return false
}

// sessionSafeStateDir is one of the emulator's state folders, held open from
// before the quit: a bind mount its launch script takes down on the way out
// (JukaHub's PPSSPP shares the firmware's PPSSPP_STATE that way) must not
// hide the state that was just written through it.
type sessionSafeStateDir struct {
	path string
	root *os.Root
}

// sessionSafeOpenStateDirs opens the state folders that exist, each once.
func sessionSafeOpenStateDirs(run sessionSafeRun) []sessionSafeStateDir {
	if run.recipe == nil {
		return nil
	}
	var out []sessionSafeStateDir
	seen := map[string]bool{}
	for _, base := range run.bases {
		for _, rel := range run.recipe.StateDirs {
			dir := filepath.Clean(filepath.Join(base, rel))
			if seen[dir] {
				continue
			}
			seen[dir] = true
			root, err := os.OpenRoot(dir)
			if err != nil {
				continue
			}
			out = append(out, sessionSafeStateDir{path: dir, root: root})
		}
	}
	return out
}

// sessionSafeCloseStateDirs lets the folders go.
func sessionSafeCloseStateDirs(dirs []sessionSafeStateDir) {
	for _, d := range dirs {
		_ = d.root.Close()
	}
}

// sessionSafeNewestState is the newest non-empty state file written since
// since in the held folders, or "".
func sessionSafeNewestState(dirs []sessionSafeStateDir, exts []string, since time.Time) string {
	best, bestAt := "", time.Time{}
	for _, d := range dirs {
		f, err := d.root.Open(".")
		if err != nil {
			continue
		}
		entries, _ := f.ReadDir(sessionSafeDirLimit)
		_ = f.Close()
		for _, e := range entries {
			if e.IsDir() || !sessionSafeStateName(e.Name(), exts) {
				continue
			}
			info, err := d.root.Lstat(e.Name())
			if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
				continue
			}
			at := info.ModTime()
			if at.Before(since.Add(-sessionSafeStateSlack)) {
				continue
			}
			if best == "" || at.After(bestAt) {
				best, bestAt = filepath.Join(d.path, e.Name()), at
			}
		}
	}
	return best
}

// ── The callers ──────────────────────────────────────────────────────────

// sessionSafeProtectGame is the 3% guard's save (protectLowBattery): RetroArch
// through its network commands as always, a standalone emulator through the
// steps above. A state saved either way comes back at the next power-on
// (instantresume.go). It reports whether a state was saved and, if not, why.
func sessionSafeProtectGame(tag string) (bool, string) {
	cmds := liveScreenChildren()
	if runtime.GOOS != "linux" || len(cmds) == 0 || !sessionSafeEnabled(appConfig) || retroArchRunning() {
		return resumeRetroArchSave(tag, cmds)
	}
	run := sessionSafeDetect(cmds)
	if run.class == sessionSafeNone || run.class == sessionSafeRetroArch {
		return resumeRetroArchSave(tag, cmds)
	}
	if !sessionSafeIsGame(run) {
		// An app, not a game: closing it could throw away unsaved work, and
		// the suspend keeps it in memory as it is.
		logInfo("power", "%s: the program on screen is an app, not a game; it is left open", tag)
		return false, "an app is on screen, not a game"
	}
	l, named := sessionSafeLaunchFor(cmds)
	res := sessionSafeEnd(tag, run)
	if res.exited {
		// Its launch's own end first (the screen handed back), as Power off.
		quickAwaitGameCleanup()
	}
	m := sessionSafeRecord(tag, run, res, l, named)
	if qm, ok := sessionSafeQuickMarker(m); ok && res.exited {
		quickResumeWriteMarker(qm)
	}
	switch {
	case res.saved && m.AutoLoad:
		ShowToast(fmt.Sprintf("Battery critical: %s saved in %s; it resumes there", sessionSafeTitle(m), m.Label), ToastKindWarning)
	case res.saved:
		ShowToast(fmt.Sprintf("Battery critical: %s saved — load the state from %s's menu", sessionSafeTitle(m), m.Label), ToastKindWarning)
	case res.exited:
		// The guard's own toast follows; the bell keeps what happened.
		notifRecord(fmt.Sprintf("Battery critical: %s was closed cleanly before the suspend (%s)", run.label(), res.why), notifWarning)
	}
	return res.saved, res.why
}

// sessionSafePowerOffGame is Power off with a standalone game running
// (quickPowerOffSavesFirst): the emulator's own time to save and quit, and
// the resume marker when it will load that state by itself. Off the main
// thread. A game that does not quit is left to the teardown.
func sessionSafePowerOffGame(cmds []*exec.Cmd) {
	if runtime.GOOS != "linux" || len(cmds) == 0 || !sessionSafeEnabled(appConfig) {
		return
	}
	run := sessionSafeDetect(cmds)
	if run.class == sessionSafeNone || run.class == sessionSafeRetroArch {
		return
	}
	l, named := sessionSafeLaunchFor(cmds)
	res := sessionSafeEnd("power off", run)
	if !res.exited {
		return
	}
	quickAwaitGameCleanup()
	m := sessionSafeRecord("power off", run, res, l, named)
	if qm, ok := sessionSafeQuickMarker(m); ok {
		quickResumeWriteMarker(qm)
	}
}

// sessionSafeDeadlineAction is what the sleep's power off does with a
// standalone game.
type sessionSafeDeadlineAction int

const (
	// sessionSafeDeadlineSleep sleeps again: nothing can be saved.
	sessionSafeDeadlineSleep sessionSafeDeadlineAction = iota
	// sessionSafeDeadlineSave saves on the way out, then powers off.
	sessionSafeDeadlineSave
	// sessionSafeDeadlineQuit closes the game cleanly and powers off: the
	// battery would run out asleep, or a stream has nothing to keep.
	sessionSafeDeadlineQuit
)

// sessionSafeDeadlineActionFor decides it by the plan (instantresume.go): a
// game that saves (on exit, or JukaHub's PPSSPP on request, both with
// saveOnQuit set) saves; a stream, or a game nothing can save once the
// battery is down to 5%, is closed; anything else sleeps on. An app is held
// like a game here. RetroArch is quickSaveHeldGame's. percent is a fresh
// reading (-1: unknown). Pure.
func sessionSafeDeadlineActionFor(percent int, class sessionSafeClass, saveOnQuit bool) sessionSafeDeadlineAction {
	switch class {
	case sessionSafeNone, sessionSafeRetroArch:
		return sessionSafeDeadlineSleep
	}
	switch resumeActionFor(resumeCauseSleepPowerOff, resumeFamilyOf(class, saveOnQuit, false, true, true), percent, false) {
	case resumeSave:
		return sessionSafeDeadlineSave
	case resumeClose:
		return sessionSafeDeadlineQuit
	}
	return sessionSafeDeadlineSleep
}

// sessionSafeSaveHeldGame is the sleep's power off for a game that is not
// RetroArch (quickSaveHeldGame): it reports the power-on marker (nil when
// there is nothing to resume), whether the device may power off, and if not,
// why. A game that does not quit is paused again. The sleep goroutine.
func sessionSafeSaveHeldGame(held *powerSleepHeld, cmds []*exec.Cmd) (*quickResumeMarker, bool, string) {
	const notRA = "the game is not running in RetroArch, the one emulator JukaHub can ask to save"
	if runtime.GOOS != "linux" || !sessionSafeEnabled(appConfig) {
		return nil, false, notRA
	}
	run := sessionSafeDetect(cmds)
	// Read now, not getBatteryPercent's cache: its age is measured on the
	// monotonic clock, which stood still while the device slept.
	percent := batteryCalCorrect(batteryPercentIn(batterySysfsDir()))
	action := sessionSafeDeadlineActionFor(percent, run.class, run.saveOnQuit)
	if action == sessionSafeDeadlineSleep {
		switch {
		case run.class == sessionSafeNone || run.class == sessionSafeRetroArch:
			return nil, false, notRA
		case run.class == sessionSafeOnQuit && run.recipe != nil:
			return nil, false, run.label() + "'s " + run.recipe.Option + " is off, so it cannot save the game"
		}
		return nil, false, run.label() + " cannot save a state by itself"
	}
	if action == sessionSafeDeadlineQuit {
		logInfo("sleep", "power-off time reached with %s running and the battery at %d%%: closing it cleanly", run.label(), percent)
	}
	l, named := sessionSafeLaunchFor(cmds)
	// The game runs again for the moment the quit takes; the screen is off.
	for _, ch := range held.children {
		if err := ch.stop(false); err != nil {
			logWarn("sleep", "could not continue the game to close it (pid %d): %v", ch.pid, err)
		}
	}
	res := sessionSafeEnd("sleep power-off", run)
	if !res.exited {
		for _, ch := range held.children {
			if err := ch.stop(true); err != nil {
				logWarn("sleep", "could not pause the game again (pid %d): %v", ch.pid, err)
			}
		}
		return nil, false, res.why
	}
	held.children = nil
	quickAwaitGameCleanup()
	m := sessionSafeRecord("sleep power-off", run, res, l, named)
	if qm, ok := sessionSafeQuickMarker(m); ok {
		return &qm, true, ""
	}
	// Closed cleanly: what the game had saved itself is on the card, and
	// sleeping on with nothing running would only drain the battery.
	return nil, true, ""
}

// ── The 10% warning ──────────────────────────────────────────────────────

// sessionSafeWarnFor reports whether a running game needs the early warning:
// one that cannot save a state by itself. RetroArch has the guard's own
// warning; a stream loses nothing. Pure.
func sessionSafeWarnFor(class sessionSafeClass, saveOnQuit bool) bool {
	switch class {
	case sessionSafeQuitOnly:
		return true
	case sessionSafeOnQuit:
		return !saveOnQuit
	}
	return false
}

// sessionSafeWarnMessage is the warning's text.
func sessionSafeWarnMessage(percent int, run sessionSafeRun) string {
	who := run.label()
	if run.recipe == nil || run.recipe == &sessionSafeUnknown {
		who = "This game"
	}
	if run.class == sessionSafeOnQuit && run.recipe != nil && run.recipe.Option != "" {
		return fmt.Sprintf("Battery %d%% — %s can't save automatically (%s is off): save in the game now", percent, who, run.recipe.Option)
	}
	return fmt.Sprintf("Battery %d%% — %s can't be saved automatically: save in the game now", percent, who)
}

// sessionSafeLowBatteryTick is the guard's early warning (lowBatteryTick,
// once a second): at sessionSafeWarnPct, once per launch, a game whose
// emulator cannot save a state by itself is named. The toast waits under the
// game, so a buzz says it now; the bell keeps it. Main thread; the detection
// runs on a goroutine.
func sessionSafeLowBatteryTick(config *Config, percent int) {
	if runtime.GOOS != "linux" {
		return
	}
	// The guard's save level is Settings → Power's (batterywarn.go, published
	// by lowBatteryTick just before): 10% and 3% under Auto as always, the
	// warning kept sessionSafeWarnLead above a higher one.
	savePct := batteryWarnNow().savePct()
	warnPct := max(sessionSafeWarnPct, savePct+sessionSafeWarnLead)
	if percent < 0 || percent > warnPct+2 {
		sessionSafeWarnedKey = 0
		return
	}
	if percent > warnPct || percent <= savePct || !sessionSafeEnabled(config) {
		return
	}
	cmds := liveScreenChildren()
	key := sessionSafeChildKey(cmds)
	if key == 0 || key == sessionSafeWarnedKey {
		return
	}
	run, ok := sessionSafeDetectCached(cmds, key)
	if !ok || batteryHoldingCharge() {
		return
	}
	sessionSafeWarnedKey = key
	if !sessionSafeIsGame(run) || !sessionSafeWarnFor(run.class, run.saveOnQuit) {
		return
	}
	msg := sessionSafeWarnMessage(percent, run)
	logInfo("power", "%s", msg)
	ShowToast(msg, ToastKindWarning)
	playHaptic(hapticErrorPulse)
}

// ── The record ───────────────────────────────────────────────────────────

// sessionSafeMarker is what was running when a game was closed for the
// battery or a power off, and the state it saved. Resume is set when that
// state is one JukaHub gives the game's next start itself (JukaHub's PPSSPP
// build's save on request, ppsspp_resume.go).
type sessionSafeMarker struct {
	Path     string `json:"path,omitempty"`
	System   string `json:"system,omitempty"`
	Title    string `json:"title,omitempty"`
	Launcher string `json:"launcher,omitempty"`
	Emulator string `json:"emulator"`
	Label    string `json:"label,omitempty"`
	State    string `json:"state,omitempty"`
	Resume   string `json:"resume,omitempty"`
	AutoLoad bool   `json:"auto_load,omitempty"`
	Reason   string `json:"reason,omitempty"`
	BootID   string `json:"boot_id,omitempty"`
}

// sessionSafeLast caches the record. Any goroutine, under mu.
var sessionSafeLast struct {
	mu     sync.Mutex
	loaded bool
	ok     bool
	m      sessionSafeMarker
}

// sessionSafeMarkerPath is the record's file, or "" without a data folder.
func sessionSafeMarkerPath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, sessionSafeMarkerName)
}

// sessionSafeDecodeMarker reads a record; one without an emulator is damaged.
// Pure.
func sessionSafeDecodeMarker(data []byte) (sessionSafeMarker, bool) {
	var m sessionSafeMarker
	if err := json.Unmarshal(data, &m); err != nil || strings.TrimSpace(m.Emulator) == "" {
		return sessionSafeMarker{}, false
	}
	return m, true
}

// sessionSafeWriteMarkerAt writes a record through AtomicWrite (fsync and
// rename): a power off right after cannot leave half of it.
func sessionSafeWriteMarkerAt(path string, m sessionSafeMarker) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(data, '\n'), 0o644)
}

// sessionSafeReadMarkerAt reads a record; a missing or damaged one is none.
func sessionSafeReadMarkerAt(path string) (sessionSafeMarker, bool) {
	data, err := sessionSafeReadLimited(path, 64<<10)
	if err != nil {
		return sessionSafeMarker{}, false
	}
	return sessionSafeDecodeMarker(data)
}

// sessionSafeLastMarker is the record, read from the card once.
func sessionSafeLastMarker() (sessionSafeMarker, bool) {
	sessionSafeLast.mu.Lock()
	defer sessionSafeLast.mu.Unlock()
	if !sessionSafeLast.loaded {
		sessionSafeLast.loaded = true
		if path := sessionSafeMarkerPath(); path != "" {
			sessionSafeLast.m, sessionSafeLast.ok = sessionSafeReadMarkerAt(path)
		}
	}
	return sessionSafeLast.m, sessionSafeLast.ok
}

// sessionSafeClearLast drops the record.
func sessionSafeClearLast() {
	sessionSafeLast.mu.Lock()
	defer sessionSafeLast.mu.Unlock()
	sessionSafeLast.loaded, sessionSafeLast.ok, sessionSafeLast.m = true, false, sessionSafeMarker{}
	if path := sessionSafeMarkerPath(); path != "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			logWarn("power", "could not remove %s: %v", path, err)
		}
		_ = os.Remove(path + ".bak") // AtomicWrite's copy of the one before
	}
}

// sessionSafeRecord keeps what was closed and what it saved, and files a
// saved state the emulator does not load by itself in the bell.
func sessionSafeRecord(tag string, run sessionSafeRun, res sessionSafeResult, l sessionSafeLaunch, named bool) sessionSafeMarker {
	m := sessionSafeMarker{Label: run.label(), State: res.state, AutoLoad: res.saved && run.autoLoad, Reason: tag, BootID: quickBootID()}
	m.Emulator = sessionSafeUnknown.ID
	if run.recipe != nil {
		m.Emulator = run.recipe.ID
	}
	if named {
		m.Path, m.System, m.Title, m.Launcher = l.game.Path, l.game.System, l.game.Title, l.launcher
	}
	if res.saved && run.saveTo != "" && res.state == run.saveTo {
		// JukaHub gives it to the game's next start; older ones go.
		m.Resume = res.state
		ppssppResumePrune(m.Resume)
	}
	sessionSafeKeepMarker(m)
	if res.saved && !m.AutoLoad && tag != "low battery" {
		notifRecord(fmt.Sprintf("%s saved %s before the power off — load the state from its menu", m.Label, sessionSafeTitle(m)), notifInfo)
	}
	return m
}

// sessionSafeKeepMarker writes the record and keeps it as the cached one.
func sessionSafeKeepMarker(m sessionSafeMarker) {
	if path := sessionSafeMarkerPath(); path != "" {
		if err := sessionSafeWriteMarkerAt(path, m); err != nil {
			logWarn("power", "could not keep the record of %s: %v", m.Label, err)
		}
	}
	sessionSafeLast.mu.Lock()
	sessionSafeLast.loaded, sessionSafeLast.ok, sessionSafeLast.m = true, true, m
	sessionSafeLast.mu.Unlock()
}

// ── Save on request (JukaHub's PPSSPP build) ─────────────────────────────

// sessionSafeRequestSave asks JukaHub's own PPSSPP build for a state at
// run.saveTo (SIGUSR1 to the one process sessionSafeRequestTarget approved)
// and waits, bounded, for its answer file and for the state to stop growing.
// PPSSPP writes the state on its next emulated frame, so a game paused under
// the in-game menu or the sleep is continued for it. It reports whether a
// whole state is there.
func sessionSafeRequestSave(tag string, run sessionSafeRun) bool {
	label := run.label()
	done := run.saveTo + sessionSafeDoneSuffix
	if err := os.Remove(done); err != nil && !errors.Is(err, os.ErrNotExist) {
		logWarn("power", "%s: could not clear %s: %v", tag, done, err)
		return false
	}
	since := time.Now()
	for _, cmd := range run.cmds {
		_ = alarmSignalChild(cmd, false)
	}
	if !sessionSafeSignalSave(run.saver) {
		logWarn("power", "%s: could not ask %s for a save state (pid %d)", tag, label, run.saver.pid)
		return false
	}
	logInfo("power", "%s: asked %s for a save state at %s", tag, label, run.saveTo)
	deadline := time.Now().Add(sessionSafeRequestWait)
	for {
		if data, err := sessionSafeReadLimited(done, 64); err == nil {
			if answer := strings.TrimSpace(string(data)); answer != "" {
				if answer != "ok" {
					logWarn("power", "%s: %s could not write its save state (%q)", tag, label, answer)
					return false
				}
				return sessionSafeStateSettled(run.saveTo, since)
			}
		}
		if !time.Now().Before(deadline) {
			logWarn("power", "%s: %s did not answer the save request within %s", tag, label, sessionSafeRequestWait)
			return false
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// sessionSafeStateSettled reports a state file written since since, not
// empty, and the same size two looks sessionSafeSettleGap apart, within
// sessionSafeSettleWait.
func sessionSafeStateSettled(path string, since time.Time) bool {
	deadline := time.Now().Add(sessionSafeSettleWait)
	last := int64(-1)
	for {
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && info.Size() > 0 && !info.ModTime().Before(since.Add(-sessionSafeStateSlack)) {
			if info.Size() == last {
				return true
			}
			last = info.Size()
		}
		if !time.Now().Before(deadline) {
			logWarn("power", "the save state %s was not whole within %s", path, sessionSafeSettleWait)
			return false
		}
		time.Sleep(sessionSafeSettleGap)
	}
}

// sessionSafeTitle names the record's game for a message.
func sessionSafeTitle(m sessionSafeMarker) string {
	if m.Title != "" {
		return m.Title
	}
	return "the game"
}

// sessionSafeQuickMarker is the power-on resume for a record: only a state
// the emulator loads by itself, of a library game whose launcher is known —
// anything else would start the game from its title screen while claiming a
// resume.
func sessionSafeQuickMarker(m sessionSafeMarker) (quickResumeMarker, bool) {
	if m.State == "" || !m.AutoLoad || strings.TrimSpace(m.Path) == "" || strings.TrimSpace(m.Launcher) == "" {
		return quickResumeMarker{}, false
	}
	return quickResumeMarker{Path: m.Path, System: m.System, Title: m.Title, Launcher: m.Launcher, BootID: m.BootID}, true
}
