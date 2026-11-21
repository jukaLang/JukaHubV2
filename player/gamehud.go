package main

// gamehud.go — Battery in games: the battery level on RetroArch's own message
// line while a game runs.
//
// JukaHub cannot draw over an emulator (osdpreset.go explains why), but
// RetroArch shows any line sent to its network command port as
// "SHOW_MSG <text>", the way the calendar reminders and the save ledger's
// "Progress safe" already reach the player (calsync_remind.go,
// saveledger.go). So with Settings → Power & battery → Battery in games on,
// a RetroArch game the library starts gets, for that launch only
// (racfgscope.go), the command interface on, and a goroutine beside the game
// sends a line like
//
//	Battery 54% · about 1 h 20 min left
//
// at the stops chosen:
//
//   - Low battery only: once each at 20, 15, 10 and 5% while the battery is
//     draining (a threshold the level climbs back over on a charger fires
//     again later). A game started below one says so once.
//   - Every 15 min / Every 5 min: the same line on that interval (the first
//     one 20 seconds into the game), plus the low-battery lines.
//
// "Show the time too" puts the clock first ("14:05 · Battery 54% · …"). The
// estimate is battery_eta.go's phrase, so a guess reads "roughly" and a
// charger reads "full in about …".
//
// The low-battery guard (lowbattery.go) only toasts at 5% and 3%, and a toast
// is not on screen under a game; the warning's own in-game line ("Show over
// games", batterywarn.go) stands down while these lines run for the game
// (gameHudActive), down to the 5% line, so RetroArch shows one battery line,
// not two; below 5% its per-percent line speaks again, and a RetroArch the
// library did not start keeps it. The 5% line here adds "plug in now". The
// guard's own save at 3% is left alone. Quiet Mode holds the interval lines,
// not the low-battery ones.
//
// A stopped RetroArch reads no commands: a line sent while the in-game menu
// or the game assistant holds the game (gaLink), an alarm rings or the
// device sleeps would wait in the socket and pop up later out of date. So
// nothing is sent then; a line that was due goes out once the game runs
// again. RetroArch only shows the line with its on-screen notifications on —
// the OSD preset "quiet" turns them off (osdpreset.go).
//
// One goroutine per game, started by the Started launch hook and ended by
// the Exited one (gamelaunch_hooks.go). It never reads the config: the
// settings are copied into the session when the game starts.

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// gameHudKey is the setting: "off" (the default), "low", "15" or "5".
	gameHudKey = "gameHudBattery"
	// gameHudClockKey puts the time before the level (default off).
	gameHudClockKey = "gameHudClock"

	// gameHudTick is how often the level is looked at. The gauge moves a
	// percent every few minutes at most, so a threshold is never late by
	// more than this.
	gameHudTick = 15 * time.Second
	// gameHudFirstAfter is when the first interval line goes out: past
	// RetroArch's own start-up messages (the core, the auto state loaded).
	gameHudFirstAfter = 20 * time.Second
	// gameHudGap keeps two lines apart: a threshold line moves the next
	// interval line on, so the player never reads the same thing twice.
	gameHudGap = time.Minute
	// gameHudRearm is how far above a threshold the level must climb (on a
	// charger) before that threshold may fire again: the gauge wobbles by a
	// percent around a value.
	gameHudRearm = 3
	// gameHudResumeDelay lets a game that was just given back run a moment
	// before a held line is sent.
	gameHudResumeDelay = 2 * time.Second
	// gameHudPrepareMatch is how close the launch settings and the start of
	// the same game must be.
	gameHudPrepareMatch = time.Minute
	// gameHudPlugAt is the threshold whose line adds "plug in now", the
	// low-battery guard's warning level.
	gameHudPlugAt = lowBatteryWarnPct
)

// gameHudThresholds are the low-battery stops, highest first.
var gameHudThresholds = []int{20, 15, 10, 5}

// gameHudModes are the row's stops, in order.
var gameHudModes = []setOption{
	{Value: "off", Label: "Off"},
	{Value: "low", Label: "Low battery only", Note: "At 20, 15, 10 and 5%"},
	{Value: "15", Label: "Every 15 min", Note: "And at 20, 15, 10 and 5%"},
	{Value: "5", Label: "Every 5 min", Note: "And at 20, 15, 10 and 5%"},
}

func init() {
	gameLaunchRegisterPrepare(gameHudPrepare)
	gameLaunchRegisterStarted(gameHudStarted)
	gameLaunchRegisterExited(gameHudExited)
}

// ── Settings ─────────────────────────────────────────────────────────────

// gameHudMode is the stored stop; anything unknown is "off".
func gameHudMode(c *Config) string {
	if c == nil {
		return "off"
	}
	v, _ := customString(c.Variables.Custom, gameHudKey)
	v = strings.ToLower(strings.TrimSpace(v))
	for _, o := range gameHudModes {
		if o.Value == v {
			return v
		}
	}
	return "off"
}

// gameHudInterval is the chosen interval, 0 for none (Off or Low only).
func gameHudInterval(mode string) time.Duration {
	switch mode {
	case "15":
		return 15 * time.Minute
	case "5":
		return 5 * time.Minute
	}
	return 0
}

// gameHudClockOn reports "Show the time too".
func gameHudClockOn(c *Config) bool {
	if c == nil {
		return false
	}
	if v, ok := customValue(c.Variables.Custom, gameHudClockKey); ok {
		return coerceBool(v)
	}
	return false
}

// gameHudAvailable hides the rows where there is no battery to report.
func gameHudAvailable(*Config) bool { return getBatteryPercent() >= 0 }

// gameHudBatterySettingsRow is Settings → Power & battery → Battery in games.
func gameHudBatterySettingsRow() setRow {
	return setRow{
		ID: gameHudKey, Label: "Battery in games", Kind: setKindChoice,
		Detail:  setStatic("RetroArch games show the level and the time left on their message line"),
		Visible: gameHudAvailable,
		Options: func(*Config) []setOption { return gameHudModes },
		Current: func(c *Config) int {
			cur := gameHudMode(c)
			for i, o := range gameHudModes {
				if o.Value == cur {
					return i
				}
			}
			return 0
		},
		Choose: func(c *Config, value string) {
			setStoreString(c, gameHudKey, value)
			for _, o := range gameHudModes {
				if o.Value == value {
					setToast("Battery in games: " + o.Label)
				}
			}
		},
	}
}

// gameHudClockSettingsRow is the switch under it.
func gameHudClockSettingsRow() setRow {
	return setRow{
		ID: gameHudClockKey, Label: "Show the time too", Kind: setKindToggle,
		Detail:  setStatic("The battery line in games starts with the clock"),
		Visible: func(c *Config) bool { return gameHudAvailable(c) && gameHudMode(c) != "off" },
		Get:     gameHudClockOn,
		Set: func(c *Config, on bool) {
			setStoreBool(c, gameHudClockKey, on)
			setToast("Show the time too: " + setOnOff(on))
		},
	}
}

// ── The launch ───────────────────────────────────────────────────────────

// gameHudPrepare scopes RetroArch's command interface on for a RetroArch
// launch while Battery in games is on, and notes its port for the Started
// hook. The in-game menu's save slots usually did it already
// (savestate_resume.go); a second scope of the same value changes nothing.
func gameHudPrepare(launcher string, game libraryGame) {
	gameHudSetPending("", 0)
	if gameHudMode(appConfig) == "off" || strings.TrimSpace(game.Path) == "" || !retroArchLauncher(launcher) {
		return
	}
	port := 0
	for _, path := range raScopeTargets(raLaunchCandidates(launcher, savestateResumeCandidates())) {
		if _, err := raScopeApply(path, []raScopeValue{{Key: "network_cmd_enable", Value: "true"}}); err != nil {
			logWarn("gamehud", "cannot turn on RetroArch's commands in %s: %v", path, err)
			continue
		}
		if port == 0 {
			if raw, err := os.ReadFile(path); err == nil {
				port = networkCmdPort(string(raw))
			}
		}
	}
	if port > 0 {
		gameHudSetPending(game.Path, port)
	}
}

// gameHudSetPending notes (or with "" clears) the prepared launch.
func gameHudSetPending(path string, port int) {
	gameHud.mu.Lock()
	gameHud.pendPath, gameHud.pendPort, gameHud.pendAt = path, port, time.Now()
	gameHud.mu.Unlock()
}

// gameHudSession is one game's lines.
type gameHudSession struct {
	game      libraryGame
	port      int
	interval  time.Duration
	clock     bool
	startedAt time.Time
	stop      chan struct{}
	once      sync.Once
}

// end stops the session's goroutine; safe to call more than once.
func (s *gameHudSession) end() { s.once.Do(func() { close(s.stop) }) }

// gameHud is the running session and the command port the last launch's
// settings scoped on (pend*), which the Started hook takes.
var gameHud struct {
	mu       sync.Mutex
	cur      *gameHudSession
	pendPath string
	pendPort int
	pendAt   time.Time
}

// gameHudStarted starts the lines for the game the last launch prepared.
// Main thread.
func gameHudStarted(game libraryGame, launcher string, _ *exec.Cmd) {
	gameHud.mu.Lock()
	port, path, at := gameHud.pendPort, gameHud.pendPath, gameHud.pendAt
	gameHud.pendPath, gameHud.pendPort = "", 0
	gameHud.mu.Unlock()
	mode := gameHudMode(appConfig)
	if mode == "off" || port <= 0 || path != game.Path || time.Since(at) > gameHudPrepareMatch || !retroArchLauncher(launcher) {
		return
	}
	s := &gameHudSession{
		game:      game,
		port:      port,
		interval:  gameHudInterval(mode),
		clock:     gameHudClockOn(appConfig),
		startedAt: time.Now(),
		stop:      make(chan struct{}),
	}
	// The time's 12- or 24-hour form is mirrored for goroutines here
	// (clockformat.go dateSeen), so clockDigits(nil, …) below matches the
	// header's.
	clockHour12(appConfig)
	gameHud.mu.Lock()
	old := gameHud.cur
	gameHud.cur = s
	gameHud.mu.Unlock()
	if old != nil {
		old.end()
	}
	go gameHudRun(s)
}

// gameHudExited ends the lines of the game that ended (the reaper
// goroutine). An exit that ran longer than the current session belongs to an
// earlier launch of the same game.
func gameHudExited(game libraryGame, _ string, ranFor time.Duration, _ error) {
	gameHud.mu.Lock()
	s := gameHud.cur
	if s == nil || s.game.Path != game.Path || ranFor > time.Since(s.startedAt) {
		gameHud.mu.Unlock()
		return
	}
	gameHud.cur = nil
	gameHud.mu.Unlock()
	s.end()
}

// gameHudActive reports whether the lines run for the game now: a session
// the Started hook began (a RetroArch game the library started, its command
// port known) that no exit and no later game has ended. batterywarn.go's
// in-game line stands down only then, not merely because the setting is on.
// Any goroutine.
func gameHudActive() bool {
	gameHud.mu.Lock()
	s := gameHud.cur
	gameHud.mu.Unlock()
	if s == nil {
		return false
	}
	select {
	case <-s.stop:
		return false
	default:
		return true
	}
}

// ── The lines ────────────────────────────────────────────────────────────

// gameHudThresholdDue is the low-battery decision, kept pure: thresholds the
// level climbed gameHudRearm above are armed again; while draining, every
// armed threshold at or above pct is spent and the lowest of them is the
// one to announce (0: none). fired holds one bit per gameHudThresholds entry.
func gameHudThresholdDue(pct int, draining bool, fired uint8) (int, uint8) {
	if pct < 0 {
		return 0, fired
	}
	for i, t := range gameHudThresholds {
		if pct >= t+gameHudRearm {
			fired &^= 1 << i
		}
	}
	if !draining {
		return 0, fired
	}
	due := 0
	for i, t := range gameHudThresholds {
		if pct <= t && fired&(1<<i) == 0 {
			fired |= 1 << i
			due = t
		}
	}
	return due, fired
}

// gameHudLine is one line: the clock when asked, the level, the estimate
// ("about 1 h 20 min left", "full in about 40 min") or "charging" when there
// is none yet, and "plug in now" at the guard's warning level.
func gameHudLine(clock string, pct int, e batteryEstimate, holding, plug bool) string {
	parts := make([]string, 0, 4)
	if clock != "" {
		parts = append(parts, clock)
	}
	parts = append(parts, fmt.Sprintf("Battery %d%%", pct))
	if phrase := batteryETAPhraseFor(e); phrase != "" {
		parts = append(parts, phrase)
	} else if holding {
		parts = append(parts, "charging")
	}
	if plug && !holding {
		parts = append(parts, "plug in now")
	}
	return strings.Join(parts, " · ")
}

// gameHudHeld reports whether the game cannot read a command now: paused
// under the in-game menu or the game assistant, by a ringing alarm or by a
// sleep, or JukaHub is quitting. Any goroutine.
func gameHudHeld() bool {
	return gaLink.live.Load() || gaLink.held.Load() || alarmFiringActive() || powerSleepActive() || exitRequested.Load()
}

// gameHudRetroArchUp reports whether RetroArch is still the game: on Linux
// the /proc scan; elsewhere a line to a port nobody reads costs nothing.
func gameHudRetroArchUp() bool {
	if runtime.GOOS != "linux" {
		return true
	}
	return retroArchRunning()
}

// gameHudRun is the session's goroutine.
func gameHudRun(s *gameHudSession) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("gamehud", "battery lines stopped: %v", r)
		}
	}()
	var fired uint8
	nextInterval := time.Time{}
	if s.interval > 0 {
		nextInterval = s.startedAt.Add(gameHudFirstAfter)
	}
	lastSent := time.Time{}
	// A game given back runs until heldUntil before a line; pendingLow is a
	// threshold that came due while the game was held.
	heldUntil := time.Time{}
	wasHeld := false
	pendingLow := 0
	first := time.NewTimer(gameHudFirstAfter)
	defer first.Stop()
	ticker := time.NewTicker(gameHudTick)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-first.C:
		case <-ticker.C:
		}
		now := time.Now()
		if gameHudHeld() {
			wasHeld = true
			// Thresholds still count while held; their line waits.
			due, f := gameHudThresholdDue(getBatteryPercent(), !batteryHoldingCharge(), fired)
			fired = f
			if due > 0 {
				pendingLow = due
			}
			continue
		}
		if wasHeld {
			wasHeld = false
			heldUntil = now.Add(gameHudResumeDelay)
		}
		if now.Before(heldUntil) {
			// Look again right after the delay rather than a tick later.
			first.Reset(time.Until(heldUntil) + 100*time.Millisecond)
			continue
		}
		if now.Sub(s.startedAt) < gameHudFirstAfter {
			// RetroArch's own start-up messages come first.
			continue
		}
		pct := getBatteryPercent()
		if pct < 0 {
			continue
		}
		holding := batteryHoldingCharge()
		due, f := gameHudThresholdDue(pct, !holding, fired)
		fired = f
		if due == 0 && pendingLow > 0 && !holding {
			due = pendingLow
		}
		pendingLow = 0
		intervalDue := !nextInterval.IsZero() && !now.Before(nextInterval)
		if due == 0 && (!intervalDue || quietModeOn()) {
			if intervalDue {
				// Quiet Mode: this one is skipped, not saved for later.
				nextInterval = now.Add(s.interval)
			}
			continue
		}
		if due == 0 && !lastSent.IsZero() && now.Sub(lastSent) < gameHudGap {
			continue
		}
		clock := ""
		if s.clock {
			clock = clockDigits(nil, now)
		}
		line := gameHudLine(clock, pct, batteryETA(), holding, due > 0 && due <= gameHudPlugAt)
		if !gameHudRetroArchUp() {
			// Not RetroArch (yet, or any more): the Exited hook ends the
			// session; a scan that missed it costs one line, not the rest.
			pendingLow = due
			continue
		}
		if err := sendRetroArchNetCmd(s.port, "SHOW_MSG "+line); err != nil {
			logDebug("gamehud", "battery line not sent: %v", err)
			pendingLow = due
			continue
		}
		lastSent = now
		if s.interval > 0 {
			nextInterval = now.Add(s.interval)
		}
	}
}
