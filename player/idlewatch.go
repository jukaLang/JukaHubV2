package main

// idlewatch.go — idle in a game: the screen dims, then the handheld sleeps or
// powers off, and user scripts can follow along (idle, extended and active
// hooks).
//
// JukaHub already looks after idle on its own screens — the idle screen
// (screensaver.go, the clock dock), its low-power state (saver_power.go) and
// Auto sleep (devpower.go) — but all of them stand down while a game owns
// the screen: the main loop is parked behind the game and no SDL input
// reaches it. So a game left running on a table played on at full brightness
// until the battery ran out. This file covers that gap and nothing else.
//
// While a library game runs (the Started launch hook to the Exited one,
// gamelaunch_hooks.go), with Settings → Power & battery → Dim when idle in
// games on, a goroutine reads every input device beside the game
// (idlewatch_linux.go: /dev/input/event* opened read-only and never grabbed,
// like the power key's reader, so the game and the firmware still get every
// event). A button, a key, a mouse move or a stick pushed past an eighth of
// its travel is input; a stick resting with a little jitter is not.
//
//  1. After N minutes without input (5 / 10 / 15 / 30) the backlight goes
//     down to the lowest level JukaHub ever sets (brightnessFloorFor). The
//     level it had is kept, and the first input puts exactly that back —
//     unless something else changed the light meanwhile (the firmware's
//     brightness keys), which then wins.
//  2. After M more minutes (Then: 5 / 10 / 30 / 60) it acts:
//     - Sleep: the in-game menu's own Sleep (devSleepNow → sleepNow): the
//       game is paused in memory, the card synced and the device suspended;
//       a press wakes it into the game. Settings → Power off after sleeping
//       then saves it and powers off if the device stays asleep that long
//       (quickresume.go), and a low battery brings that time nearer
//       (saveledger.go) — so nothing new is needed to keep the game safe.
//     - Power off: Power off's own path (devPowerRequest): RetroArch saves a
//       state and quits first (quickPowerOffSavesFirst, the low-battery
//       guard's retroArchSaveAndQuit), a standalone emulator saves on its way
//       out where it can (sessionsafe.go), and the resume marker brings the
//       game back at the next power-on. With a JukaHub alarm set it sleeps
//       instead: a powered-off device cannot ring.
//     Neither happens on a charger (nothing is at risk; the screen just stays
//     dim), while JukaHub's own music plays (the rule Auto sleep follows), or
//     while an alarm rings.
//
// The game under the in-game menu or the game assistant (gaLink), or a
// ringing alarm, counts as in use: the overlay grabs the pad, so no event
// would reach this reader, and the overlay has its own time limit. A sleep
// from anywhere restarts the idle clock.
//
// Hooks (Linux only). Executable or not, every *.sh in the data folder's
// hooks/idle, hooks/extended and hooks/active runs with /bin/sh, in name
// order, one at a time, at most idleHookTimeout each (then its process
// group is killed), with JUKAHUB_IDLE_STATE (idle, extended, active),
// JUKAHUB_IDLE_WHERE (game, home) and JUKAHUB_IDLE_SOURCE (what caused it)
// in the environment. They never run on the UI thread: a worker goroutine
// takes them in order, and holds them while the startup lock is up
// (idleHooksWorker). The in-game watcher fires them at each stage; on
// Home the idle screen coming up is "idle" and going away "active"
// (idleHooksNoteHome, called from saver_power.go), and every sleep is
// "extended" before it and "active" after it (sleephooks.go). A state is
// only announced when it changes, per place, so a sleep the watcher started
// does not run the extended scripts twice.
//
// Nothing here was run on a device from this desk.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// idleWatchDimKey is the setting: minutes without input before the dim,
	// "0" (the default) for Off.
	idleWatchDimKey = "gameIdleDim"
	// idleWatchAfterKey is the minutes after the dim before the action, "0"
	// (the default) for Off: the screen then stays dim.
	idleWatchAfterKey = "gameIdleAfter"
	// idleWatchActionKey is the action: "sleep" (the default) or "poweroff".
	idleWatchActionKey = "gameIdleAction"

	idleActionSleep    = "sleep"
	idleActionPowerOff = "poweroff"

	// idleWatchSlow and idleWatchFast pace the reads: once a second while the
	// player is active, four times a second while dimmed, so the light comes
	// back as soon as a button is pressed. The kernel queues the events in
	// between; a queue that overflowed says so (SYN_DROPPED), which counts as
	// input too.
	idleWatchSlow = time.Second
	idleWatchFast = 250 * time.Millisecond
	// idleRescanEvery is how often the device list is read again, for a pad
	// or keyboard connected during the game.
	idleRescanEvery = 10 * time.Second

	// The hooks' states and places.
	idleStateIdle     = "idle"
	idleStateExtended = "extended"
	idleStateActive   = "active"
	idleWhereGame     = "game"
	idleWhereHome     = "home"

	// idleHookTimeout bounds one script.
	idleHookTimeout = 10 * time.Second
	// idleHookMaxScripts bounds one folder.
	idleHookMaxScripts = 32
	// idleHookQueue is how many state changes may wait for the worker.
	idleHookQueue = 16
	// idleHookSleepWait is how long a sleep waits for the extended scripts:
	// the before-sleep steps share sleepHookBeforeBudget (the save ledger's
	// share alone is 1.2 s), and the person is waiting for the screen to go
	// dark.
	idleHookSleepWait = 700 * time.Millisecond
	// idleHookActionWait is how long the in-game watcher waits for them
	// before its own sleep or power off: nobody is waiting then.
	idleHookActionWait = 15 * time.Second
	// idleHookLockPoll is how often the worker looks whether the startup
	// lock is down again.
	idleHookLockPoll = 500 * time.Millisecond
)

// idleWatchDimSteps and idleWatchAfterSteps are the rows' stops, in minutes;
// 0 is Off.
var (
	idleWatchDimSteps   = []int{0, 5, 10, 15, 30}
	idleWatchAfterSteps = []int{0, 5, 10, 30, 60}
)

func init() {
	gameLaunchRegisterStarted(idleWatchStarted)
	gameLaunchRegisterExited(idleWatchExited)
	sleepHookRegisterBefore(idleHooksBeforeSleep)
	sleepHookRegisterWake(idleHooksAfterSleep)
	// A light dimmed at exit goes back to the level it had (devpower.go puts
	// a switched-off one back on first).
	Cleanup = append(Cleanup, func() { idleWatchUndim("exit") })
}

// ── Settings ─────────────────────────────────────────────────────────────

// idleWatchSupported reports whether in-game idle can work here: the TrimUI,
// where JukaHub reads the input devices, moves the backlight and sleeps.
func idleWatchSupported() bool { return devPowerSupported() }

// idleWatchMinutes reads a minutes setting; 0 when off or unreadable.
func idleWatchMinutes(c *Config, key string) int {
	if c == nil {
		return 0
	}
	s, ok := customString(c.Variables.Custom, key)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 || n > 24*60 {
		return 0
	}
	return n
}

// idleWatchAction is the chosen action.
func idleWatchAction(c *Config) string {
	if c == nil {
		return idleActionSleep
	}
	v, _ := customString(c.Variables.Custom, idleWatchActionKey)
	if strings.EqualFold(strings.TrimSpace(v), idleActionPowerOff) {
		return idleActionPowerOff
	}
	return idleActionSleep
}

// idleWatchMinutesLabel is a stop's text.
func idleWatchMinutesLabel(n int) string {
	if n <= 0 {
		return "Off"
	}
	return strconv.Itoa(n) + " min"
}

// idleWatchMinutesRow is a minutes choice row over steps.
func idleWatchMinutesRow(key, label, detail string, steps []int, visible func(*Config) bool) setRow {
	return setRow{
		ID: key, Label: label, Kind: setKindChoice, Clamp: true,
		Detail:  setStatic(detail),
		Visible: visible,
		Options: func(*Config) []setOption {
			out := make([]setOption, 0, len(steps))
			for _, n := range steps {
				out = append(out, setOption{Value: strconv.Itoa(n), Label: idleWatchMinutesLabel(n)})
			}
			return out
		},
		Current: func(c *Config) int {
			cur := idleWatchMinutes(c, key)
			for i, n := range steps {
				if n == cur {
					return i
				}
			}
			return -1
		},
		Value: func(c *Config) string { return idleWatchMinutesLabel(idleWatchMinutes(c, key)) },
		Choose: func(c *Config, value string) {
			n, err := strconv.Atoi(value)
			if err != nil {
				return
			}
			setStoreString(c, key, strconv.Itoa(n))
			setToast(label + ": " + idleWatchMinutesLabel(n))
		},
	}
}

// idleWatchDimSettingsRow is Settings → Power & battery → Dim when idle in
// games.
func idleWatchDimSettingsRow() setRow {
	return idleWatchMinutesRow(idleWatchDimKey, "Dim when idle in games",
		"No input for this long while a game runs: the screen dims; any button brings it back",
		idleWatchDimSteps, func(*Config) bool { return idleWatchSupported() })
}

// idleWatchAfterSettingsRow is the second stage, shown once the dim is on.
func idleWatchAfterSettingsRow() setRow {
	return idleWatchMinutesRow(idleWatchAfterKey, "Then sleep or power off after",
		"Still idle this long after the dim; never on a charger or while music plays",
		idleWatchAfterSteps, func(c *Config) bool {
			return idleWatchSupported() && idleWatchMinutes(c, idleWatchDimKey) > 0
		})
}

// idleWatchActionSettingsRow is what the second stage does.
func idleWatchActionSettingsRow() setRow {
	options := []setOption{
		{Value: idleActionSleep, Label: "Sleep", Note: "The game waits in memory; Power off after sleeping saves it later"},
		{Value: idleActionPowerOff, Label: "Save and power off", Note: "Saved like Power off; it resumes at the next power-on"},
	}
	return setRow{
		ID: idleWatchActionKey, Label: "When idle in games", Kind: setKindChoice,
		Detail: func(c *Config) string {
			if idleWatchAction(c) == idleActionPowerOff {
				return "Saves the game and powers off (sleeps instead while an alarm is set)"
			}
			return "Sleeps like the in-game menu's Sleep; a press wakes it into the game"
		},
		Visible: func(c *Config) bool {
			return idleWatchSupported() && idleWatchMinutes(c, idleWatchDimKey) > 0 && idleWatchMinutes(c, idleWatchAfterKey) > 0
		},
		Options: func(*Config) []setOption { return options },
		Current: func(c *Config) int {
			if idleWatchAction(c) == idleActionPowerOff {
				return 1
			}
			return 0
		},
		Value: func(c *Config) string {
			if idleWatchAction(c) == idleActionPowerOff {
				return options[1].Label
			}
			return options[0].Label
		},
		Choose: func(c *Config, value string) {
			setStoreString(c, idleWatchActionKey, value)
			for _, o := range options {
				if o.Value == value {
					setToast("When idle in games: " + o.Label)
				}
			}
		},
	}
}

// ── The session ──────────────────────────────────────────────────────────

// idleStage is where an idle spell has got to.
type idleStage int

const (
	idleStageActive idleStage = iota
	idleStageDimmed
	idleStageExtended
)

// idleWatchStageFor is the whole timing decision, kept pure: dim after dim
// without input, the action after dim+after (after 0: never).
func idleWatchStageFor(idleFor, dim, after time.Duration) idleStage {
	switch {
	case dim <= 0 || idleFor < dim:
		return idleStageActive
	case after > 0 && idleFor >= dim+after:
		return idleStageExtended
	}
	return idleStageDimmed
}

// idleWatchSession is one game's watcher. The settings are copied in when
// the game starts: the goroutine never reads the config.
type idleWatchSession struct {
	game      libraryGame
	startedAt time.Time
	dim       time.Duration
	after     time.Duration
	action    string
	stop      chan struct{}
	once      sync.Once
	// poke: something outside the reader counts as input (a wake).
	poke atomic.Bool
	// poweringOff: the watcher asked for the power off, so the game's exit
	// is not the player coming back.
	poweringOff atomic.Bool
}

// end stops the goroutine; safe to call more than once.
func (s *idleWatchSession) end() { s.once.Do(func() { close(s.stop) }) }

var idleWatch struct {
	mu  sync.Mutex
	cur *idleWatchSession
}

// idleWatchStarted starts the watcher for a library game. Main thread.
func idleWatchStarted(game libraryGame, _ string, _ *exec.Cmd) {
	dim := idleWatchMinutes(appConfig, idleWatchDimKey)
	if dim <= 0 || !idleWatchSupported() {
		return
	}
	s := &idleWatchSession{
		game:      game,
		startedAt: time.Now(),
		dim:       time.Duration(dim) * time.Minute,
		after:     time.Duration(idleWatchMinutes(appConfig, idleWatchAfterKey)) * time.Minute,
		action:    idleWatchAction(appConfig),
		stop:      make(chan struct{}),
	}
	idleWatch.mu.Lock()
	old := idleWatch.cur
	idleWatch.cur = s
	idleWatch.mu.Unlock()
	if old != nil {
		old.end()
	}
	// A new game starts in use.
	idleHooksReset(idleWhereGame)
	go idleWatchRun(s)
}

// idleWatchExited stops the watcher of the game that ended (the reaper
// goroutine), puts the light back and, when the player was away, says
// "active" so a script that switched something off on idle can undo it. An
// exit that ran longer than the current session belongs to an earlier launch
// of the same game.
func idleWatchExited(game libraryGame, _ string, ranFor time.Duration, _ error) {
	idleWatch.mu.Lock()
	s := idleWatch.cur
	if s == nil || s.game.Path != game.Path || ranFor > time.Since(s.startedAt) {
		idleWatch.mu.Unlock()
		return
	}
	idleWatch.cur = nil
	idleWatch.mu.Unlock()
	s.end()
	idleWatchUndim("game ended")
	if !s.poweringOff.Load() && !exitRequested.Load() {
		idleHooksFire(idleStateActive, idleWhereGame, "game ended")
	}
}

// idleWatchGamePaused reports a game held by the in-game menu, the game
// assistant or a ringing alarm: in use, whatever the reader sees (the
// overlay grabs the pad). Any goroutine.
func idleWatchGamePaused() bool {
	return gaLink.live.Load() || gaLink.held.Load() || alarmFiringActive()
}

// idleWatchActBlocker names what keeps the second stage from acting now, or
// "" when nothing does. Any goroutine: every check is atomic or locked.
func idleWatchActBlocker() string {
	switch {
	case exitRequested.Load():
		return "JukaHub is quitting"
	case powerSleepActive():
		return "a sleep is already running"
	case alarmFiringActive():
		return "an alarm is ringing"
	case batteryHoldingCharge():
		return "the battery is charging"
	case quickMusicPlaying():
		return "music is playing"
	}
	return ""
}

// idleWatchAct is the second stage: the extended scripts first (bounded), then
// the sleep or the power off. stillIdle is asked once the scripts are done:
// someone who came back meanwhile keeps the game, and false is returned. The
// watcher's goroutine.
func idleWatchAct(s *idleWatchSession, stillIdle func() bool) bool {
	action := s.action
	if action == idleActionPowerOff && powerSleepNextAlarm.Load() > 0 {
		// The sleep's RTC wakes the device for the alarm; a power off would not.
		logInfo("idle", "an alarm is set: sleeping instead of powering off")
		action = idleActionSleep
	}
	idle := idleWatchMinutesLabel(int((s.dim + s.after) / time.Minute))
	idleHooksFireWait(idleStateExtended, idleWhereGame, "idle "+idle+" in a game: "+action, idleHookActionWait)
	if s.stopped() || exitRequested.Load() {
		return true
	}
	if !stillIdle() {
		logInfo("idle", "%s is in use again: no %s", idleGameName(s.game), action)
		return false
	}
	if why := idleWatchActBlocker(); why != "" {
		logInfo("idle", "no %s after the idle scripts: %s", action, why)
		return false
	}
	if action == idleActionPowerOff {
		logInfo("idle", "%s idle for %s: saving and powering off", idleGameName(s.game), idle)
		s.poweringOff.Store(true)
		devPowerRequest(devPowerPoweroff)
		return true
	}
	logInfo("idle", "%s idle for %s: sleeping", idleGameName(s.game), idle)
	if !devSleepNow("idle in a game") {
		logInfo("idle", "the sleep did not start (another one is running, or this device cannot sleep)")
	}
	return true
}

// stopped reports whether the session was ended.
func (s *idleWatchSession) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

// idleGameName is the game for the log.
func idleGameName(g libraryGame) string {
	if t := strings.TrimSpace(g.DisplayTitle); t != "" {
		return t
	}
	if t := strings.TrimSpace(g.Title); t != "" {
		return t
	}
	return filepath.Base(g.Path)
}

// idleWatchNoteWake is a wake: the light back to the level it had before the
// dim, and the idle clock started again. Any goroutine.
func idleWatchNoteWake() {
	idleWatch.mu.Lock()
	s := idleWatch.cur
	idleWatch.mu.Unlock()
	if s != nil {
		s.poke.Store(true)
	}
	idleWatchUndim("wake")
}

// idleInputWanted reports whether a device from /proc/bus/input/devices is
// read for input: one with keys or buttons, axes or relative motion, but not
// a motion sensor (an accelerometer reports axes all the time). Pure.
func idleInputWanted(d evdevDevice) bool {
	if !evdevHandlerOK(d.Handler) {
		return false
	}
	if !evdevHas(d.EV, evdevEvKey) && !evdevHas(d.EV, evdevEvAbs) && !evdevHas(d.EV, evdevEvRel) {
		return false
	}
	name := strings.ToLower(d.Name)
	for _, w := range []string{"accel", "gyro", "sensor", "imu", "compass", "magnet"} {
		if strings.Contains(name, w) {
			return false
		}
	}
	return true
}

// ── Hooks ────────────────────────────────────────────────────────────────

// idleHookJob is one state change for the worker. done is closed once its
// scripts have run.
type idleHookJob struct {
	state, where, source string
	done                 chan struct{}
}

// idleHooks is the last state announced per place, and the worker's queue.
var idleHooks struct {
	mu   sync.Mutex
	last map[string]string
	once sync.Once
	ch   chan idleHookJob
}

// idleHooksSupported reports whether hook scripts run here: /bin/sh on Linux.
func idleHooksSupported() bool { return runtime.GOOS == "linux" }

// idleHooksDir is the folder of one state's scripts, or "" without a data
// folder.
func idleHooksDir(state string) string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "hooks", state)
}

// idleHooksReset marks a place as in use without announcing it.
func idleHooksReset(where string) {
	idleHooks.mu.Lock()
	if idleHooks.last != nil {
		delete(idleHooks.last, where)
	}
	idleHooks.mu.Unlock()
}

// idleHooksFire announces a state at a place, if it is a change, and queues
// its scripts. It returns a channel closed once they have run, or nil when
// nothing was queued. Never blocks; safe from any goroutine, the UI thread
// included.
func idleHooksFire(state, where, source string) chan struct{} {
	idleHooks.mu.Lock()
	if idleHooks.last == nil {
		idleHooks.last = map[string]string{}
	}
	prev := idleHooks.last[where]
	if prev == "" {
		prev = idleStateActive
	}
	if prev == state {
		idleHooks.mu.Unlock()
		return nil
	}
	idleHooks.last[where] = state
	idleHooks.mu.Unlock()
	logDebug("idle", "%s → %s (%s)", where, state, source)
	if !idleHooksSupported() {
		return nil
	}
	idleHooks.once.Do(func() {
		idleHooks.ch = make(chan idleHookJob, idleHookQueue)
		go idleHooksWorker(idleHooks.ch)
	})
	job := idleHookJob{state: state, where: where, source: source, done: make(chan struct{})}
	select {
	case idleHooks.ch <- job:
		return job.done
	default:
		logWarn("idle", "too many idle changes at once: the %s scripts for %s were skipped", state, where)
		return nil
	}
}

// idleHooksFireWait is idleHooksFire that waits up to wait for the scripts.
func idleHooksFireWait(state, where, source string, wait time.Duration) {
	done := idleHooksFire(state, where, source)
	if done == nil || wait <= 0 {
		return
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		logInfo("idle", "the %s scripts are still running after %s; going on", state, wait)
	}
}

// idleHooksWorker runs the queued state changes' scripts, one at a time.
// Nothing runs over the startup lock (bootlock.go: nothing may act until it
// is unlocked): a change waits for the unlock, and one that a later change
// at its place has replaced meanwhile is skipped.
func idleHooksWorker(ch <-chan idleHookJob) {
	for job := range ch {
		func() {
			defer close(job.done)
			defer func() {
				if r := recover(); r != nil {
					logWarn("idle", "idle scripts failed: %v", r)
				}
			}()
			if startupLocked() {
				for startupLocked() && !exitRequested.Load() {
					time.Sleep(idleHookLockPoll)
				}
				if exitRequested.Load() || !idleHooksCurrent(job.state, job.where) {
					logDebug("idle", "the %s scripts for %s were skipped: the startup lock was up", job.state, job.where)
					return
				}
			}
			idleHookRunScripts(job.state, job.where, job.source)
		}()
	}
}

// idleHooksCurrent reports whether state is still the last one announced at
// where.
func idleHooksCurrent(state, where string) bool {
	idleHooks.mu.Lock()
	defer idleHooks.mu.Unlock()
	last := idleHooks.last[where]
	if last == "" {
		last = idleStateActive
	}
	return last == state
}

// idleHookScriptNames picks the scripts of a folder listing: *.sh, not
// hidden, regular files only (no links), in name order, at most
// idleHookMaxScripts. Pure.
func idleHookScriptNames(entries []os.DirEntry) []string {
	var names []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") || !strings.HasSuffix(strings.ToLower(n), ".sh") || !e.Type().IsRegular() {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > idleHookMaxScripts {
		names = names[:idleHookMaxScripts]
	}
	return names
}

// idleHooksNoteHome is the idle screens' line (saver_power.go saverPowerTick,
// every frame on the event thread): the idle screen or the clock dock coming
// up is "idle" on Home, going away "active". One atomic swap per frame.
func idleHooksNoteHome(idleUp bool) {
	if idleHomeUp.Swap(idleUp) == idleUp {
		return
	}
	if idleUp {
		idleHooksFire(idleStateIdle, idleWhereHome, "idle screen")
		return
	}
	idleHooksFire(idleStateActive, idleWhereHome, "idle screen")
}

// idleHomeUp is the idle screen as idleHooksNoteHome last saw it.
var idleHomeUp atomic.Bool

// idleHooksWhere is where the device is now: in a game or on Home.
func idleHooksWhere() string {
	if gameScreenSuspended() {
		return idleWhereGame
	}
	return idleWhereHome
}

// idleHooksBeforeSleep is the sleep's "extended" (sleephooks.go): the
// scripts get idleHookSleepWait before the game is stopped and the screen
// goes dark.
func idleHooksBeforeSleep(source string) {
	idleHooksFireWait(idleStateExtended, idleHooksWhere(), "sleep: "+source, idleHookSleepWait)
}

// idleHooksAfterSleep is the wake's "active", and the in-game watcher's
// light and clock (sleephooks.go). Not when the wake is the sleep's own power
// off.
func idleHooksAfterSleep(source string, _ time.Duration, _ bool) {
	if quickPowerOffSilent() || exitRequested.Load() {
		return
	}
	idleWatchNoteWake()
	idleHooksFire(idleStateActive, idleHooksWhere(), fmt.Sprintf("wake: %s", source))
}
