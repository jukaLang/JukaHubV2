package main

// gamewarm.go — "Warm up games before launch" (instant start).
//
// A game that stays highlighted in the library for a moment (gameWarmDwell),
// or the game on Home's Continue card, is likely the next one started. Its
// launch reads the same few files every time: RetroArch, the core and that
// core's .info, and the ROM. On a cold SD card those reads are much of the
// wait between A and the first frame, so they are asked into the page cache
// in the background while the player is still looking:
//
//   - the emulator is resolveGameLauncherFor's, the one the launch starts when
//     no picker asks; a core started directly is the core itself, and a
//     firmware script's core is read the way the launch log reads it
//     (gameCoreFromLauncher and coreActiveBinary, as coreLaunchNote does);
//     RetroArch is the one that owns the core's folder (coreRetroArchBinary);
//   - the ROM only up to gameWarmMaxROM, everything together up to
//     gameWarmMaxBytes, and only while MemAvailable is at least twice that;
//   - never while a game runs, never at 15% battery or less off the charger,
//     one warm at a time, and a file warmed in the last five minutes (by the
//     monotonic clock, not the wall clock a TrimUI sets late) is not asked
//     for again;
//   - a new highlight, the page left, or the setting switched off cancels
//     the warm in flight; a launch stops it too, and its log line says
//     whether the warm had finished (a gameLaunchRegisterStarted hook).
//
// The reads are Linux's (gamewarm_linux.go): posix_fadvise WILLNEED on a
// thread of its own at nice 19 in the idle I/O class, with a fallback that
// reads and discards 1 MiB at a time. Elsewhere this does nothing
// (gamewarm_other.go). Android, HTML5, ports, built-in games and games on a
// network drive are left alone.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	gameWarmKey        = "gameWarmUp" // Custom key, default on
	gameWarmDwell      = 350 * time.Millisecond
	gameWarmRetry      = 100 * time.Millisecond
	gameWarmMaxBytes   = int64(160 << 20)
	gameWarmMaxROM     = int64(64 << 20)
	gameWarmRepeat     = 5 * time.Minute
	gameWarmLowBattery = 15
)

var gameWarm struct {
	// noted is the game path last noted; main thread only, so a frame that
	// notes the same highlight costs one string compare.
	noted string

	mu      sync.Mutex
	gen     uint64
	timer   *time.Timer
	cancel  context.CancelFunc
	running string // the ROM of the warm in flight, "" for none
	warmed  map[string]time.Time
	last    gameWarmResult
}

// gameWarmResult is how the last warm ended, for the launch log.
type gameWarmResult struct {
	rom   string
	state string // "finished", "stopped" or "skipped"
	why   string
	files int
	bytes int64
}

func init() {
	gameLaunchRegisterStarted(gameWarmLaunchStarted)
}

// gameWarmEnabled is the setting; on unless switched off.
func gameWarmEnabled(c *Config) bool {
	if c == nil {
		return true
	}
	return customBool(c.Variables.Custom, gameWarmKey, true)
}

// gameWarmSettingsRow is "Warm up games before launch" in Settings → Games &
// emulators.
func gameWarmSettingsRow() setRow {
	return setRow{
		ID: gameWarmKey, Label: "Warm up games before launch", Kind: setKindToggle,
		Detail: func(c *Config) string {
			if gameWarmEnabled(c) {
				return "Reads a highlighted game's emulator and ROM ahead, so it starts sooner"
			}
			return "Off: a game is read from the card when it starts"
		},
		Visible: func(*Config) bool { return gameWarmSupported },
		Get:     gameWarmEnabled,
		Set: func(c *Config, on bool) {
			setStoreBool(c, gameWarmKey, on)
			setToast("Warm up games before launch: " + setOnOff(on))
		},
	}
}

// gameWarmNoteLibrary notes the library's highlighted game, or none on the
// consoles grid. Main thread, every frame the library draws.
func gameWarmNoteLibrary() {
	var game libraryGame
	if gameLib.open && gameLib.focus == 1 {
		game, _ = selectedLibraryGame()
	}
	gameWarmNote(game)
}

// gameWarmNoteRecent notes the game on Home's Continue card. The library,
// when it is open, has the say. Main thread.
func gameWarmNoteRecent(g recentGame) {
	if gameLib.open {
		return
	}
	gameWarmNote(libraryGame{Path: g.Path, System: g.System, Title: g.Title})
}

// gameWarmNote arms a warm for game once it has stayed noted for
// gameWarmDwell; another game (or none) cancels what was armed or running.
func gameWarmNote(game libraryGame) {
	path := game.Path
	if path != "" && (!gameWarmSupported || !gameWarmEnabled(appConfig) || !gameWarmCandidate(game)) {
		path = ""
	}
	if path == gameWarm.noted {
		return
	}
	gameWarm.noted = path
	gameWarm.mu.Lock()
	defer gameWarm.mu.Unlock()
	gameWarm.gen++
	gameWarmStopLocked()
	if path == "" {
		return
	}
	gen := gameWarm.gen
	gameWarm.timer = time.AfterFunc(gameWarmDwell, func() { gameWarmBegin(gen, game) })
}

// gameWarmStopLocked drops the armed timer and cancels the warm in flight.
func gameWarmStopLocked() {
	if gameWarm.timer != nil {
		gameWarm.timer.Stop()
		gameWarm.timer = nil
	}
	if gameWarm.cancel != nil {
		gameWarm.cancel()
		gameWarm.cancel = nil
	}
}

// gameWarmCandidate reports whether a game's start reads ROM files this can
// warm: not a built-in game, an Android or HTML5 game, a port or a game on a
// network drive.
func gameWarmCandidate(game libraryGame) bool {
	if strings.TrimSpace(game.Path) == "" {
		return false
	}
	switch game.System {
	case "", androidLibrarySystem, html5LibrarySystem, portsLibrarySystem:
		return false
	}
	if isBuiltInLibraryGame(game) {
		return false
	}
	_, onDrive := nasLibraryRef(game.Path)
	return !onDrive
}

// gameWarmBegin starts the armed warm (the dwell timer's goroutine) unless
// the highlight has moved on. The warm before it, cancelled, may still be
// finishing its step: one at a time, so this one waits for it.
func gameWarmBegin(gen uint64, game libraryGame) {
	gameWarm.mu.Lock()
	defer gameWarm.mu.Unlock()
	if gen != gameWarm.gen {
		return
	}
	if gameWarm.running != "" {
		gameWarm.timer = time.AfterFunc(gameWarmRetry, func() { gameWarmBegin(gen, game) })
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	gameWarm.timer, gameWarm.cancel, gameWarm.running = nil, cancel, game.Path
	go gameWarmRun(ctx, cancel, game)
}

// gameWarmShouldStop reports whether the warm in flight must stop now: it
// was cancelled, or a game took the screen.
func gameWarmShouldStop(ctx context.Context) bool {
	return ctx.Err() != nil || runningGameChildren.Load() > 0
}

// gameWarmRun warms game's launch files on its own goroutine.
func gameWarmRun(ctx context.Context, cancel context.CancelFunc, game libraryGame) {
	res := gameWarmResult{rom: game.Path, state: "skipped"}
	defer func() {
		cancel()
		gameWarm.mu.Lock()
		gameWarm.running = ""
		gameWarm.last = res
		gameWarm.mu.Unlock()
		if res.state != "skipped" || res.why != "" {
			logDebug("gamewarm", "%s: %s", filepath.Base(game.Path), gameWarmDescribe(res))
		}
	}()
	if runningGameChildren.Load() > 0 {
		res.why = "a game is running"
		return
	}
	if pct := getBatteryPercent(); pct >= 0 && pct <= gameWarmLowBattery && !getBatteryCharging() {
		res.why = "battery at " + strconv.Itoa(pct) + "%"
		return
	}
	plan := gameWarmPlan(game)
	files, sizes, total, recent := gameWarmPick(plan, time.Now())
	if gameWarmShouldStop(ctx) {
		res.state = "stopped"
		return
	}
	if len(files) == 0 {
		// Nothing left to read: warmed a moment ago, or a ROM too large (or
		// gone) and no emulator files to read for it.
		res.why = "nothing to read"
		if recent > 0 {
			res.state, res.why = "finished", "already warm"
		}
		return
	}
	if avail := memAvailNowMB(); avail == 0 || avail<<20 < uint64(2*total) {
		res.why = "too little free memory"
		return
	}
	gameWarmLowPriority()
	for i, path := range files {
		n, err := gameWarmFile(ctx, path, sizes[i])
		res.bytes += n
		if gameWarmShouldStop(ctx) {
			res.state = "stopped"
			return
		}
		if err != nil {
			logDebug("gamewarm", "%s: %v", path, err)
			continue
		}
		res.files++
		gameWarm.mu.Lock()
		if gameWarm.warmed == nil {
			gameWarm.warmed = map[string]time.Time{}
		}
		gameWarm.warmed[path] = time.Now()
		gameWarm.mu.Unlock()
	}
	res.state = "finished"
}

// gameWarmPlan lists the files game's launch reads, most needed first:
// RetroArch, the core, its .info, the ROM. A launcher that is neither a core
// nor a RetroArch script (a standalone emulator, a pak) adds only the ROM.
// It resolves the emulator the way the launch does, so it runs off the main
// thread.
func gameWarmPlan(game libraryGame) []string {
	launcher := resolveGameLauncherFor(game)
	core := ""
	switch {
	case launcher == "":
	case strings.EqualFold(filepath.Ext(launcher), ".so"):
		core = launcher
	case retroArchLauncher(launcher):
		if name := gameCoreFromLauncher(launcher, game.System); name != "" {
			if dirs := retroArchCoreDirs(); len(dirs) > 0 {
				core = coreActiveBinary(name, dirs)
			}
		}
	}
	var files []string
	if core != "" {
		if ra := coreRetroArchBinary(filepath.Dir(core)); ra != "" {
			files = append(files, ra)
		}
		files = append(files, core)
		if info := gameWarmCoreInfo(core); info != "" {
			files = append(files, info)
		}
	}
	return append(files, game.Path)
}

// gameWarmCoreInfo is the core's .info file: beside it, or in RetroArch's
// info folder next to the cores folder. "" when neither has one.
func gameWarmCoreInfo(core string) string {
	name := strings.TrimSuffix(filepath.Base(core), filepath.Ext(core)) + ".info"
	dir := filepath.Dir(core)
	for _, p := range []string{filepath.Join(dir, name), filepath.Join(filepath.Dir(dir), "info", name)} {
		if regularFile(p) {
			return p
		}
	}
	return ""
}

// gameWarmPick keeps the plan's files that are worth asking for at now: real
// files, not warmed in the last gameWarmRepeat, the ROM (the plan's last) no
// larger than gameWarmMaxROM, and together within gameWarmMaxBytes; recent
// counts the files left out as warmed already. Expired marks are dropped on
// the way.
func gameWarmPick(plan []string, now time.Time) (files []string, sizes []int64, total int64, recent int) {
	gameWarm.mu.Lock()
	for path, at := range gameWarm.warmed {
		if now.Sub(at) >= gameWarmRepeat {
			delete(gameWarm.warmed, path)
		}
	}
	gameWarm.mu.Unlock()
	for i, path := range plan {
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			continue
		}
		size := info.Size()
		if i == len(plan)-1 && size > gameWarmMaxROM {
			continue
		}
		if total+size > gameWarmMaxBytes {
			continue
		}
		gameWarm.mu.Lock()
		at, seen := gameWarm.warmed[path]
		gameWarm.mu.Unlock()
		if seen && now.Sub(at) < gameWarmRepeat {
			recent++
			continue
		}
		files = append(files, path)
		sizes = append(sizes, size)
		total += size
	}
	return files, sizes, total, recent
}

// gameWarmDescribe is a result in words for the log.
func gameWarmDescribe(r gameWarmResult) string {
	text := r.state
	if r.why != "" {
		text += " (" + r.why + ")"
	}
	if r.files > 0 || r.bytes > 0 {
		text += ", " + pluralizeCount(r.files, "file", "files") + ", " + strconv.FormatInt(r.bytes>>20, 10) + " MB"
	}
	return text
}

// gameWarmLaunchStarted runs when a library launch has started its game
// (gamelaunch_hooks.go): the game reads for itself now, so a warm still in
// flight stops, and the log says how far the warm for this game had got.
// The highlight stays noted, so coming back from the game warms nothing again.
func gameWarmLaunchStarted(game libraryGame, launcher string, cmd *exec.Cmd) {
	if !gameWarmSupported || !gameWarmCandidate(game) {
		return
	}
	gameWarm.mu.Lock()
	last, running := gameWarm.last, gameWarm.running
	gameWarm.gen++
	gameWarmStopLocked()
	gameWarm.mu.Unlock()
	state := "none for this game"
	switch {
	case !gameWarmEnabled(appConfig):
		state = "off"
	case running == game.Path:
		state = "still running at the start, stopped"
	case last.rom == game.Path:
		state = gameWarmDescribe(last)
	}
	logInfo("library", "launch warm-up for %s: %s", strconv.Quote(game.Label()), state)
}
