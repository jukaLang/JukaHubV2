package main

// gamehotkeys.go — one-button hotkeys in a RetroArch game the library started.
//
//   - Hold F1 (the Brick's front key) and the game plays backwards while it is
//     held; let go and it carries on from there. Only where the launch turned
//     RetroArch's rewind on (retro_transport.go: the lighter systems).
//   - Press F2 and the game runs fast; press it again for normal speed.
//   - Press Menu and Select together (within gameSaveQuitPairGap, a short
//     press) and the game is saved and quit: JukaHub's Home comes back with
//     "Saved — it continues where you left off", and the next start of the game
//     picks up there.
//
// The Brick (and the Hammer) report F1 and F2 as the stick clicks, L3 and R3
// (brickkeys.go). A handheld with sticks (Smart Pro, Smart Pro S, Brick Pro)
// gets the same two jobs on L3 / R3 instead. Settings → Controls → Game
// hotkeys: Auto (the keys this handheld has), the pair itself, or Off. DS
// games keep the keys to themselves (the DS cores tap the screen and close the
// lid with L3 / R3), and on a handheld with sticks so do PlayStation and DOS
// games, which press the sticks in. The Brick Pro also has F1 / F2 keys of its
// own, but what they send is not known yet (HOTKEY-HW-1), so it uses L3 / R3.
//
// Nothing takes the pad: the game still sees every press (gameassist_linux.go
// reads it beside the game). A press with Select, Start or Menu held is
// someone else's chord (RetroArch's hotkeys, JukaHub's Select chords) and does
// nothing here.
//
// Everything reaches RetroArch through the command port the launch opened for
// the in-game menu (gamemenu_states.go), so the hotkeys need Settings →
// Controls → In-game menu on, and never act on PPSSPP, a port or a NextUI pak:
// RetroArch must be the process running.
//
// Save and quit: with Continue's auto state on for the launch
// (savestate_resume.go), RetroArch writes the game's auto state as it quits
// and loads it at the next start, so only QUIT is sent — a SAVE_STATE would
// overwrite the player's own state in the current slot (slot 1 at launch).
// Without the auto state, SAVE_STATE goes first and the toast says to load the
// state. No power-on resume marker is written (quickresume.go): this is a
// deliberate quit, so the next power-on shows Home, and starting the game
// again (or Home's Continue) carries on from the auto state.

import (
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// gameHotkeysKey is the Custom setting behind Settings → Controls → Game
	// hotkeys: gameHotkeysAuto (the default), gameHotkeysValueFn,
	// gameHotkeysValueStick or gameHotkeysValueOff.
	gameHotkeysKey        = "gameHotkeys"
	gameHotkeysAuto       = "auto"
	gameHotkeysValueFn    = "f1f2"
	gameHotkeysValueStick = "l3r3"
	gameHotkeysValueOff   = "off"
	// gameSaveQuitKey is Settings → Controls → "Menu + Select: save and quit"
	// (on unless it is false).
	gameSaveQuitKey = "gameSaveQuitChord"

	// gameHotkeyRewindHold is how long F1 / L3 is held before the game plays
	// backwards: a stick pushed hard clicks for an instant, a hold is meant.
	gameHotkeyRewindHold = 120 * time.Millisecond
	// gameHotkeyRewindEvery paces REWIND while the key is held: the in-game
	// menu's Rewind row's pace (retroTransportCommand), more than one per
	// frame, since each command is one frame of rewind.
	gameHotkeyRewindEvery = 6 * time.Millisecond
	// gameHotkeyRewindCap ends a rewind held this long (RetroArch's buffer is
	// long spent by then; a key stuck down stops on its own).
	gameHotkeyRewindCap = 30 * time.Second
	// gameHotkeyFastGap is the least time between two fast-forward toggles: a
	// bouncing contact is one press.
	gameHotkeyFastGap = 250 * time.Millisecond
	// gameSaveQuitPairGap is how close together Menu and Select must go down.
	gameSaveQuitPairGap = 150 * time.Millisecond
	// gameSaveQuitMaxHold: a pair held longer than this is not the short press
	// (an emulator hotkey with a third button, a hand resting on them).
	gameSaveQuitMaxHold = time.Second
	// gameSaveQuitConfirmWait is how long the first QUIT gets before a second
	// is sent: a RetroArch set to confirm quitting wants two.
	gameSaveQuitConfirmWait = 1500 * time.Millisecond
)

// gameHotkeyKeys is which pair of keys carries rewind and fast forward.
type gameHotkeyKeys uint32

const (
	gameHotkeysNone  gameHotkeyKeys = iota
	gameHotkeysFn                   // the Brick's front F1 / F2 (sent as L3 / R3)
	gameHotkeysStick                // the stick clicks L3 / R3
)

// gameHotkeysLive is what the main thread (gameHotkeysSync) and the pad
// watcher (gamehotkeys_linux.go) share. Any goroutine.
var gameHotkeysLive struct {
	// keys: the pair active in the game on screen (a gameHotkeyKeys);
	// rewind: its rewind is on; saveQuit: Menu + Select saves and quits;
	// port: the game's RetroArch command port.
	keys     atomic.Uint32
	rewind   atomic.Bool
	saveQuit atomic.Bool
	port     atomic.Int32
	// rewinding: the watcher holds rewind down; the REWIND loop runs while it
	// does. rewindBusy / fastBusy: a loop or a toggle is under way.
	rewinding  atomic.Bool
	rewindBusy atomic.Bool
	fastBusy   atomic.Bool
	// saveQuitAt: when the watcher saw Menu + Select (UnixNano, 0 = not); the
	// main thread acts on it if it is fresh.
	saveQuitAt atomic.Int64
}

// gameHotkeysLaunch is the library launch the hotkeys belong to: its ROM,
// system and launcher, from the launch hook (gameHotkeysPrepare).
var gameHotkeysLaunch struct {
	mu                    sync.Mutex
	rom, system, launcher string
}

func init() {
	gameLaunchRegisterPrepare(gameHotkeysPrepare)
}

// gameHotkeysPrepare is the launch hook: which game and system are starting.
// Main thread.
func gameHotkeysPrepare(launcher string, game libraryGame) {
	gameHotkeysLaunch.mu.Lock()
	gameHotkeysLaunch.rom, gameHotkeysLaunch.system = game.Path, game.System
	gameHotkeysLaunch.launcher = launcher
	gameHotkeysLaunch.mu.Unlock()
}

// gameHotkeysLauncherOf is the launcher of the launch of rom ("" when
// unknown). Any goroutine.
func gameHotkeysLauncherOf(rom string) string {
	gameHotkeysLaunch.mu.Lock()
	defer gameHotkeysLaunch.mu.Unlock()
	if rom == "" || gameHotkeysLaunch.rom != rom {
		return ""
	}
	return gameHotkeysLaunch.launcher
}

// gameHotkeysSystemOf is the system of the launch of rom ("" when unknown).
func gameHotkeysSystemOf(rom string) string {
	gameHotkeysLaunch.mu.Lock()
	defer gameHotkeysLaunch.mu.Unlock()
	if rom == "" || gameHotkeysLaunch.rom != rom {
		return ""
	}
	return gameHotkeysLaunch.system
}

// ── Which keys ───────────────────────────────────────────────────────────

// gameHotkeysKeysOf is the pair a handheld has: the Brick and the Hammer
// their front F1 / F2, a handheld with sticks L3 / R3, anything else none.
// Pure.
func gameHotkeysKeysOf(d DeviceModel) gameHotkeyKeys {
	switch d {
	case DeviceTrimuiBrick, DeviceTrimuiHammer:
		return gameHotkeysFn
	}
	if deviceHardware(d).HasSticks {
		return gameHotkeysStick
	}
	return gameHotkeysNone
}

// gameHotkeysResolve is the pair a stored value means on d: Auto the
// handheld's own, a pinned pair only where the handheld has it (a setting
// carried over from another handheld), Off none. Pure.
func gameHotkeysResolve(value string, d DeviceModel) gameHotkeyKeys {
	have := gameHotkeysKeysOf(d)
	switch value {
	case gameHotkeysValueOff:
		return gameHotkeysNone
	case gameHotkeysValueFn:
		if have == gameHotkeysFn {
			return have
		}
		return gameHotkeysNone
	case gameHotkeysValueStick:
		if have == gameHotkeysStick {
			return have
		}
		return gameHotkeysNone
	}
	return have
}

// gameHotkeysValueOf is the stored value that pins pair k.
func gameHotkeysValueOf(k gameHotkeyKeys) string {
	switch k {
	case gameHotkeysFn:
		return gameHotkeysValueFn
	case gameHotkeysStick:
		return gameHotkeysValueStick
	}
	return gameHotkeysValueOff
}

// gameHotkeysNames are the pair's two keys: rewind's, then fast forward's.
func gameHotkeysNames(k gameHotkeyKeys) (rewind, fast string) {
	if k == gameHotkeysFn {
		return "F1", "F2"
	}
	return "L3", "R3"
}

// gameHotkeysKeyName names a physical button through the hints layer. The
// watcher reads the built-in pad's own buttons, like the quit chord, so the
// pad's words are used whatever the hints show elsewhere, and an
// Accessibility remap does not rename them. Any goroutine.
func gameHotkeysKeyName(button string) string {
	return hintKeyIn(button, "", hintVocabPad, nil)
}

// gameHotkeysPairLabel is "F1 · F2" or "L3 · R3".
func gameHotkeysPairLabel(k gameHotkeyKeys) string {
	rw, ff := gameHotkeysNames(k)
	return gameHotkeysKeyName(rw) + " · " + gameHotkeysKeyName(ff)
}

// gameHotkeysSetting is the stored value, gameHotkeysAuto when unset or
// unknown.
func gameHotkeysSetting(c *Config) string {
	if c == nil {
		return gameHotkeysAuto
	}
	v := strings.ToLower(strings.TrimSpace(customStringValue(c.Variables.Custom, gameHotkeysKey)))
	switch v {
	case gameHotkeysValueFn, gameHotkeysValueStick, gameHotkeysValueOff:
		return v
	}
	return gameHotkeysAuto
}

// gameHotkeysClickConsole reports a console whose games use L3 / R3 as game
// buttons, so pair k leaves them to the game there: the DS cores tap the
// screen and close the lid with them (ctrllayout_builtin.go) — on the Brick
// too, whose F1 / F2 reach the game as L3 / R3 — and PlayStation and DOS
// games press the sticks in on a handheld that has sticks. Pure.
func gameHotkeysClickConsole(system string, k gameHotkeyKeys) bool {
	switch strings.ToLower(strings.TrimSpace(system)) {
	case "nintendo ds", "nintendo dsi", "nds", "ds":
		return true
	case "playstation", "ps1", "psx", "ps", "dos", "ms-dos":
		return k == gameHotkeysStick
	}
	return false
}

// gameHotkeysForGame is the pair active in the launch of rom. A light-gun
// game keeps the pair too: the gun, which reads the pad beside the game just
// like this, re-anchors on R3 (lightgun_aim.go).
func gameHotkeysForGame(c *Config, rom string) gameHotkeyKeys {
	k := gameHotkeysResolve(gameHotkeysSetting(c), DetectDevice())
	if k != gameHotkeysNone && (gameHotkeysClickConsole(gameHotkeysSystemOf(rom), k) || lightgunActive()) {
		return gameHotkeysNone
	}
	return k
}

// gameSaveQuitEnabled reports Settings → Controls → "Menu + Select: save and
// quit".
func gameSaveQuitEnabled(c *Config) bool {
	if c == nil {
		return true
	}
	return customBool(c.Variables.Custom, gameSaveQuitKey, true)
}

// gameSaveQuitAllowed is the switch, and the game can be saved: RetroArch in
// RetroAchievements hardcore writes no state, not even its auto state, so
// Menu + Select would quit and lose the game's progress. It is the game's
// then.
func gameSaveQuitAllowed(c *Config) bool {
	return gameSaveQuitEnabled(c) && !raachHardcoreRunning()
}

// ── The game on screen ───────────────────────────────────────────────────

// gameHotkeysTarget is the RetroArch launch the library made with the command
// port open, when it is the game on screen: its ROM, port and whether rewind
// is on. gmStatesAvailable's test without the overlay (that one is worked out
// once per opening); whether RetroArch itself runs is asked when a key acts.
// Main thread (gaTrack).
func gameHotkeysTarget() (rom string, port int, rewind, ok bool) {
	retroTransport.Lock()
	rom, port, rewind = retroTransport.rom, retroTransport.port, retroTransport.rewind
	retroTransport.Unlock()
	if rom == "" || port <= 0 || port > 65535 {
		return "", 0, false, false
	}
	gmStates.mu.Lock()
	srom, sport, at := gmStates.rom, gmStates.port, gmStates.at
	gmStates.mu.Unlock()
	if srom != rom || sport != port || at.IsZero() || gaTrack.launchAt.IsZero() {
		return "", 0, false, false
	}
	if d := gaTrack.launchAt.Sub(at); d < -gmStateLaunchMatch || d > gmStateLaunchMatch {
		return "", 0, false, false
	}
	// Hardcore refuses rewind, also when it was only known once the game ran.
	return rom, port, rewind && !raachHardcoreRunning(), true
}

// gameHotkeysSync runs on the main thread from the parked loop (gaPadSync):
// it mirrors what the game on screen allows for the watcher, acts on a Menu +
// Select the watcher saw, and reports whether the watcher is wanted for them.
// can: a game JukaHub started owns the screen of an awake handheld and
// nothing else holds the pad (the force-quit hold's own condition).
func gameHotkeysSync(config *Config, can bool) bool {
	keys, rewind, saveQuit, port := gameHotkeysNone, false, false, 0
	if can {
		if rom, p, rw, ok := gameHotkeysTarget(); ok {
			keys = gameHotkeysForGame(config, rom)
			rewind = rw && keys != gameHotkeysNone
			saveQuit = gameSaveQuitAllowed(config)
			port = p
		}
	}
	gameHotkeysLive.port.Store(int32(port))
	gameHotkeysLive.keys.Store(uint32(keys))
	gameHotkeysLive.rewind.Store(rewind)
	gameHotkeysLive.saveQuit.Store(saveQuit)
	if !rewind {
		gameHotkeysLive.rewinding.Store(false)
	}
	if at := gameHotkeysLive.saveQuitAt.Swap(0); at != 0 && saveQuit && time.Since(time.Unix(0, at)) < time.Second {
		gameSaveQuitStart(config, port)
	}
	return keys != gameHotkeysNone || saveQuit
}

// ── Rewind and fast forward ──────────────────────────────────────────────

// gameHotkeysRewindRun starts the REWIND loop for a key just held: one
// command per gameHotkeyRewindEvery for as long as the watcher keeps
// gameHotkeysLive.rewinding set, over one socket. A command RetroArch did not
// take (it quit: the port refuses) ends it. Any goroutine.
func gameHotkeysRewindRun(port int) {
	if port <= 0 || port > 65535 || !gameHotkeysLive.rewindBusy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer gameHotkeysLive.rewindBusy.Store(false)
		defer func() {
			if r := recover(); r != nil {
				logWarn("hotkeys", "rewind stopped after a fault: %v", r)
			}
		}()
		if !retroArchRunning() {
			// A NextUI pak or another emulator behind a RetroArch launcher.
			return
		}
		conn, err := net.DialTimeout("udp", "127.0.0.1:"+strconv.Itoa(port), lowBatteryNetCmdTimeout)
		if err != nil {
			logWarn("hotkeys", "rewind: cannot reach RetroArch: %v", err)
			return
		}
		defer conn.Close()
		msg := []byte("REWIND\n")
		end := time.Now().Add(gameHotkeyRewindCap)
		for gameHotkeysLive.rewinding.Load() && time.Now().Before(end) {
			_ = conn.SetWriteDeadline(time.Now().Add(lowBatteryNetCmdTimeout))
			if _, err := conn.Write(msg); err != nil {
				logWarn("hotkeys", "rewind command failed: %v", err)
				return
			}
			time.Sleep(gameHotkeyRewindEvery)
		}
	}()
}

// gameHotkeysFastToggle switches fast forward for the game on port, keeping
// the in-game menu's row (retroTransport.fast) in step, and buzzes when
// RetroArch took it. Any goroutine; the work runs on its own.
func gameHotkeysFastToggle(port int) {
	if port <= 0 || port > 65535 || !gameHotkeysLive.fastBusy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer gameHotkeysLive.fastBusy.Store(false)
		defer func() {
			if r := recover(); r != nil {
				logWarn("hotkeys", "fast forward failed: %v", r)
			}
		}()
		if !retroArchRunning() {
			return
		}
		retroTransport.Lock()
		if retroTransport.port != port || retroTransport.rom == "" {
			retroTransport.Unlock()
			return
		}
		rom := retroTransport.rom
		retroTransport.fast = !retroTransport.fast
		on := retroTransport.fast
		retroTransport.Unlock()
		if err := sendRetroArchNetCmd(port, "FAST_FORWARD"); err != nil {
			logWarn("hotkeys", "fast forward command failed: %v", err)
			retroTransport.Lock()
			if retroTransport.rom == rom {
				retroTransport.fast = !retroTransport.fast
			}
			retroTransport.Unlock()
			playHaptic(hapticErrorPulse)
			return
		}
		playHaptic(hapticTogglePulse)
		logInfo("hotkeys", "fast forward %s", strings.ToLower(setOnOff(on)))
	}()
}

// ── Save and quit ────────────────────────────────────────────────────────

// gameSaveQuitStart acts on Menu + Select (main thread, gameHotkeysSync):
// not over the in-game menu, a quit already under way, a sleeping handheld or
// a ringing alarm. gmQuitting keeps every chord and overlay away until the
// game is gone.
func gameSaveQuitStart(config *Config, port int) {
	if port <= 0 || gaOverlay.active || gmQuitting.Load() || powerSleepActive() || alarmFiringActive() {
		return
	}
	cmds := liveScreenChildren()
	if len(cmds) == 0 {
		return
	}
	gmQuitting.Store(true)
	logInfo("hotkeys", "Menu + Select: saving the game and quitting to JukaHub")
	resume := true
	if config != nil {
		if v, ok := customValue(config.Variables.Custom, savestateResumeConfigKey); ok && !coerceBool(v) {
			resume = false
		}
	}
	go gameSaveQuitRun(port, cmds, resume)
}

// gameSaveQuitAutoState reports whether RetroArch writes the game's auto
// state as it quits and loads it at the next start: the config this launch's
// RetroArch reads — the first of the files the launch scoped Continue's
// switches into (savestate_resume.go), as the command port was read from
// (prepareGameMenuStates) — says so. Not retroArchConfig(): a core started
// directly runs the RetroArch that owns its cores folder, whose config may
// not be the firmware's. Off the main thread: it reads the card.
func gameSaveQuitAutoState(launcher string) bool {
	for _, path := range raScopeTargets(raLaunchCandidates(launcher, savestateResumeCandidates())) {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		v, found := retroArchConfigValue(string(raw), savestateAutoSaveKey)
		return found && (strings.EqualFold(v, "true") || v == "1")
	}
	return false
}

// gameSaveQuitRun saves and quits the game through RetroArch's command port
// and waits for it to go; JukaHub's Home then says so. A game that is not
// RetroArch after all is left alone. resume: Continue (savestateResume) is
// on. Its own goroutine.
func gameSaveQuitRun(port int, cmds []*exec.Cmd, resume bool) {
	defer gmQuitting.Store(false)
	defer func() {
		if r := recover(); r != nil {
			logWarn("hotkeys", "save and quit failed: %v", r)
		}
	}()
	if !retroArchRunning() {
		logInfo("hotkeys", "Menu + Select: the game is not running in RetroArch; nothing done")
		return
	}
	// Read while the game runs: the launch's scoped values are put back when
	// it exits (racfgscope.go).
	retroTransport.Lock()
	rom := retroTransport.rom
	retroTransport.Unlock()
	auto := resume && gameSaveQuitAutoState(gameHotkeysLauncherOf(rom))
	playHaptic(hapticSuccessPulse)
	if !auto {
		if err := sendRetroArchNetCmd(port, "SAVE_STATE"); err != nil {
			logWarn("hotkeys", "Menu + Select: RetroArch did not take the save: %v", err)
			playHaptic(hapticErrorPulse)
			return
		}
		time.Sleep(lowBatterySaveDelay)
	}
	// JukaHub ends the game here, not the game itself (gamelaunch_watch.go).
	gameLaunchNoteStopAll()
	_ = sendRetroArchNetCmd(port, "QUIT")
	exited := waitForRetroArchExit(gameSaveQuitConfirmWait)
	if !exited {
		_ = sendRetroArchNetCmd(port, "QUIT")
		exited = waitForRetroArchExit(lowBatteryQuitWait)
	}
	if !exited {
		logWarn("hotkeys", "Menu + Select: RetroArch did not quit")
		playHaptic(hapticErrorPulse)
		return
	}
	quickAwaitGameCleanup()
	if auto {
		ShowToast("Saved — it continues where you left off", ToastKindSuccess)
		return
	}
	ShowToast("Saved to a save state — Load state brings it back", ToastKindSuccess)
}

// ── Words ────────────────────────────────────────────────────────────────

// gameHotkeysMenuHint is the in-game menu's line naming the hotkeys of the
// paused game ("" when it has none). Main thread (gmDraw).
func gameHotkeysMenuHint(config *Config) string {
	if !gmStatesAvailable() {
		return ""
	}
	rom, _, rewind, ok := gameHotkeysTarget()
	if !ok {
		return ""
	}
	parts := make([]string, 0, 3)
	if k := gameHotkeysForGame(config, rom); k != gameHotkeysNone {
		rw, ff := gameHotkeysNames(k)
		if rewind {
			parts = append(parts, gameHotkeysKeyName("Hold "+rw)+" rewind")
		}
		parts = append(parts, gameHotkeysKeyName(ff)+" fast forward")
	}
	if gameSaveQuitAllowed(config) {
		parts = append(parts, gameHotkeysKeyName("Menu + Select")+" save & quit")
	}
	return strings.Join(parts, " · ")
}

// ── Settings ─────────────────────────────────────────────────────────────

// gameHotkeysOptions are the Game hotkeys row's stops: Auto, the pair this
// handheld has, Off.
func gameHotkeysOptions(*Config) []setOption {
	pair := gameHotkeysKeysOf(DetectDevice())
	label := gameHotkeysPairLabel(pair)
	return []setOption{
		{Value: gameHotkeysAuto, Label: "Auto", Note: label + " on this handheld"},
		{Value: gameHotkeysValueOf(pair), Label: label, Note: "Rewind and fast forward on " + label},
		{Value: gameHotkeysValueOff, Label: "Off", Note: "The game keeps " + label + " to itself"},
	}
}

// gameHotkeysDetail is the Game hotkeys row's line.
func gameHotkeysDetail(c *Config) string {
	pair := gameHotkeysKeysOf(DetectDevice())
	rw, ff := gameHotkeysNames(pair)
	text := "In a RetroArch game: " + gameHotkeysKeyName("Hold "+rw) + " to rewind, " +
		gameHotkeysKeyName(ff) + " for fast forward"
	if pair == gameHotkeysStick {
		text += " · DS, PlayStation and DOS games keep " + gameHotkeysPairLabel(pair)
	} else {
		text += " · DS games keep " + gameHotkeysPairLabel(pair)
	}
	if !gameMenuEnabled(c) {
		return "Needs In-game menu on · " + text
	}
	return text
}

// gameHotkeysValueLabel is the row's value when the stored one is not a stop
// (a pair this handheld lacks, kept from another handheld).
func gameHotkeysValueLabel(c *Config) string {
	switch v := gameHotkeysSetting(c); v {
	case gameHotkeysValueFn:
		return gameHotkeysPairLabel(gameHotkeysFn) + " (not on this handheld)"
	case gameHotkeysValueStick:
		return gameHotkeysPairLabel(gameHotkeysStick) + " (not on this handheld)"
	case gameHotkeysValueOff:
		return "Off"
	}
	return "Auto"
}

// gameHotkeysSettingsRows are Settings → Controls → Game hotkeys and "Menu +
// Select: save and quit", shown where the pad watcher runs beside a game.
func gameHotkeysSettingsRows() []setRow {
	return []setRow{
		{
			ID: gameHotkeysKey, Label: "Game hotkeys", Kind: setKindChoice,
			Detail: gameHotkeysDetail,
			Visible: func(*Config) bool {
				return gameAssistSupported() && gameHotkeysKeysOf(DetectDevice()) != gameHotkeysNone
			},
			Options: gameHotkeysOptions,
			Current: func(c *Config) int {
				v := gameHotkeysSetting(c)
				for i, o := range gameHotkeysOptions(c) {
					if o.Value == v {
						return i
					}
				}
				return -1
			},
			Value: gameHotkeysValueLabel,
			Choose: func(c *Config, value string) {
				if c == nil {
					return
				}
				if c.Variables.Custom == nil {
					c.Variables.Custom = map[string]interface{}{}
				}
				c.Variables.Custom[gameHotkeysKey] = value
				saveUserConfigDebounced(userConfigFromVariables(c))
				label := gameHotkeysValueLabel(c)
				for _, o := range gameHotkeysOptions(c) {
					if o.Value == value {
						label = o.Label
					}
				}
				setToast("Game hotkeys: " + label)
			},
		},
		{
			ID: gameSaveQuitKey, Label: "Menu + Select: save and quit", Kind: setKindToggle,
			Detail: func(c *Config) string {
				text := "Press " + gameHotkeysKeyName("Menu + Select") + " together in a RetroArch game: it continues there next time"
				if !gameMenuEnabled(c) {
					return "Needs In-game menu on · " + text
				}
				return text
			},
			Visible: func(*Config) bool { return gameAssistSupported() },
			Get:     gameSaveQuitEnabled,
			Set: func(c *Config, on bool) {
				if c == nil {
					return
				}
				if c.Variables.Custom == nil {
					c.Variables.Custom = map[string]interface{}{}
				}
				c.Variables.Custom[gameSaveQuitKey] = on
				saveUserConfigDebounced(userConfigFromVariables(c))
				setToast("Menu + Select: save and quit " + setOnOff(on))
			},
		},
	}
}

// gameHotkeysInsertRows puts the two rows into the Controls page's rows
// (setmodel.go): right after "Select + R1: screenshot", the other in-game
// chord, else before RetroArch's own hotkey rows, else at the end.
func gameHotkeysInsertRows(rows []setRow) []setRow {
	add := gameHotkeysSettingsRows()
	at := -1
	for i, r := range rows {
		if r.ID == gameShotChordKey {
			at = i + 1
			break
		}
	}
	if at < 0 {
		for i, r := range rows {
			if r.ID == "raHotkeysScope" {
				at = i
				break
			}
		}
	}
	if at < 0 {
		return append(rows, add...)
	}
	out := make([]setRow, 0, len(rows)+len(add))
	out = append(out, rows[:at]...)
	out = append(out, add...)
	return append(out, rows[at:]...)
}
