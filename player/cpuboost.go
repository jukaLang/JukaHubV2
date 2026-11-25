package main

// cpuboost.go — CPU boost in games: an emulator holds the top clock while the
// chip is cool, and the heat guard brings it down when it is not.
//
// Juka (2026-11-21): "boost performance when running emulator by adjusting CPU
// frequency safely...make sure to not overheat device by checking temperature
// (All Trimui devices)". The launch profile (powerprofile.go) already raised
// the governor and the ceiling for every launch, and Automatic CPU speed
// (autoperf.go) then followed the game's load. Three things kept a heavy
// emulator short of that clock:
//
//   - the firmware's own launch scripts run after JukaHub's profile: TrimUI's
//     cpufreq.sh files put an emulator back on ondemand, some under a lower
//     ceiling, and park cores for the lighter systems — and Automatic CPU
//     speed then left the game alone as "set by another tool";
//   - a dynamic governor drops the clock between frames, and an emulator
//     waiting on its GPU reads as "quiet" to Automatic CPU speed, which then
//     stepped it down (why Xbox boost already held its games at the top);
//   - "Performance" in a game's options only chose the governor, and did
//     nothing with Full speed while playing off.
//
// A boosted launch is an Automatic CPU speed session of its own kind
// (autoPerfSession.boost). It begins cpuBoostSettle after the start, when the
// launch script's CPU lines have run (their values are what goes back); every
// parked core comes online, and the clock is held at the top of what the
// launch may use: the ceiling there and, under a dynamic governor, the floor
// there too, so no governor idles it between frames. Only the heat guard
// (heatguard.go) moves it, down one operating point at a time while the chip
// or the battery is hot and back up once they have cooled. Everything goes
// back when the game ends, only where it is still the value written; a crash
// leaves the auto-performance marker, which the next start replays.
//
// Which launches: Settings → Games & emulators → "CPU boost in games"
// (cpuBoostGames):
//
//   - Auto (the default): the 3D consoles these chips only just play (N64,
//     PSP, Dreamcast with NAOMI and Atomiswave, DS, Saturn, Jaguar, GameCube,
//     Wii, PS2: cpuBoostConsoles) and ports; the PlayStation and the 8/16-bit
//     consoles keep following Automatic CPU speed;
//   - Always: every game, port and emulator JukaHub starts with a file;
//   - Off: games follow Automatic CPU speed and their launch scripts.
//
// A game's own options win: "Performance" there is a boost whatever this row
// says; Battery saver, Balanced or a CPU limit keep the game out. A CPU
// frequency set by hand for games (cpufreq_manual.go) wins over the row and
// the heavy-game boost's clock, not over a game's own options. A game the
// heavy-game boost takes (xboxboost.go: Xbox, GameCube, Wii, PS2, and the
// A133P's heavy consoles) is boosted here too unless the row is Off; that
// boost keeps its GPU, memory and priority work. An emulator on the userspace
// governor sets its own clock (NextUI's minarch) and gets the heat guard only.
// With Full speed while playing off, the top is the firmware's own ceiling:
// the boost holds the clock there, never above.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// cpuBoostKey is the Settings row: cpuBoostAuto (the default),
	// cpuBoostAlways or cpuBoostOff.
	cpuBoostKey    = "cpuBoostGames"
	cpuBoostAuto   = "auto"
	cpuBoostAlways = "always"
	cpuBoostOff    = "off"

	// cpuBoostSettle is how long after the start a boosted session begins:
	// the launch script's own CPU lines (cpufreq.sh) have run by then.
	cpuBoostSettle = 3 * time.Second
	// cpuBoostNoteFor is how long a library launch's console stays matched
	// to its file.
	cpuBoostNoteFor = time.Minute
)

// cpuBoost is the library launch being prepared (its file, console and
// launcher folder: autoPerfLaunchBegin sees only the file) and the Settings
// row's line once a boosted session has ended.
var cpuBoost struct {
	mu     sync.Mutex
	path   string
	system string
	folder string
	at     time.Time
	status string
}

// cpuBoostModeNow is the row's stop, published by the main thread
// (autoPerfFrame) for the launch paths that do not run on it.
var cpuBoostModeNow atomic.Value // string

// cpuBoostConsoles are the consoles Auto boosts, by canonicalCompatSystem's
// names (an unknown name stays as given): the 3D consoles these chips only
// just play. The PlayStation and the 8/16-bit consoles run at full speed well
// under the top clock, so Automatic CPU speed keeps following them, and the
// battery with them. cpuBoostFolders are folder names (lower case) neither
// knows.
var (
	cpuBoostConsoles = map[string]bool{
		"Nintendo 64": true, "PlayStation Portable": true, "Dreamcast": true, "Nintendo DS": true,
		"Saturn": true, "Atari Jaguar": true, "GameCube": true, "Wii": true, "PlayStation 2": true,
	}
	cpuBoostFolders = map[string]bool{
		"ss": true, "ds": true, "naomi": true, "naomi2": true, "atomiswave": true, "awave": true,
		"jaguar": true, "atarijaguar": true, "ports": true, "port": true,
	}
)

func init() {
	gameLaunchRegisterPrepare(cpuBoostPrepare)
}

// cpuBoostMode reads the row; Auto unless set.
func cpuBoostMode(c *Config) string {
	if c == nil {
		return cpuBoostAuto
	}
	raw, _ := customString(c.Variables.Custom, cpuBoostKey)
	switch m := strings.ToLower(strings.TrimSpace(raw)); m {
	case cpuBoostAlways, cpuBoostOff:
		return m
	}
	return cpuBoostAuto
}

// cpuBoostModeLoad is the stop the main thread last published.
func cpuBoostModeLoad() string {
	if v, ok := cpuBoostModeNow.Load().(string); ok && v != "" {
		return v
	}
	return cpuBoostAuto
}

// ── Which launches ──────────────────────────────────────────────────────────

// cpuBoostPrepare notes a library launch's console for cpuBoostWanted.
// Main thread, before the launch's power profile.
func cpuBoostPrepare(launcher string, game libraryGame) {
	folder := ""
	if strings.TrimSpace(launcher) != "" {
		folder = filepath.Base(filepath.Dir(launcher))
	}
	cpuBoost.mu.Lock()
	cpuBoost.path, cpuBoost.system, cpuBoost.folder, cpuBoost.at = strings.TrimSpace(game.Path), game.System, folder, time.Now()
	cpuBoost.mu.Unlock()
}

// cpuBoostWanted reports whether target's launch is boosted: the game's own
// options first, then the row. A launch the heavy-game boost holds
// (xboxboost.go) already runs its launch profile at full speed; unless the row
// is Off its session is a boosted one too, so a launch script cannot undo that
// and the heat guard brings it down when the chip is hot. Called by
// autoPerfLaunchBegin; any goroutine.
func cpuBoostWanted(target string, opts gameOptions) bool {
	target = strings.TrimSpace(target)
	if target == "" || !autoPerfSupported() || opts.MaxFreqKHz > 0 {
		return false
	}
	switch normaliseGameCPUProfile(opts.CPUProfile) {
	case "performance":
		return true
	case "":
	default:
		return false // Battery saver or Balanced: the game's own choice
	}
	switch mode := cpuBoostModeLoad(); {
	case mode == cpuBoostOff:
		return false
	case xboxBoostHolds(target):
		return true
	case mode == cpuBoostAlways:
		// Every game: a file JukaHub starts. An App Center app is launched
		// by its folder (appcenter_launch.go) and is not a game.
		fi, err := os.Stat(target)
		return err == nil && !fi.IsDir()
	}
	return cpuBoostHeavyTarget(target)
}

// cpuBoostHeavyTarget reports whether Auto boosts target: the console the
// library noted for it, else the folders above it.
func cpuBoostHeavyTarget(target string) bool {
	cpuBoost.mu.Lock()
	path, system, folder, at := cpuBoost.path, cpuBoost.system, cpuBoost.folder, cpuBoost.at
	cpuBoost.mu.Unlock()
	if path != "" && path == target && time.Since(at) < cpuBoostNoteFor &&
		(cpuBoostHeavySystem(system) || cpuBoostHeavySystem(folder)) {
		return true
	}
	return cpuBoostHeavyPath(target)
}

// cpuBoostHeavySystem reports whether Auto boosts a console, by the library's
// name for it ("N64", NextUI's "Nintendo 64 (N64)") or a folder's (Emus/PSP).
// Pure.
func cpuBoostHeavySystem(system string) bool {
	s := strings.TrimSpace(system)
	if s == "" {
		return false
	}
	names := []string{s}
	if i := strings.LastIndex(s, "("); i > 0 && strings.HasSuffix(s, ")") {
		names = append(names, strings.TrimSpace(s[i+1:len(s)-1]), strings.TrimSpace(s[:i]))
	}
	for _, n := range names {
		if cpuBoostConsoles[canonicalCompatSystem(n)] || cpuBoostFolders[strings.ToLower(n)] {
			return true
		}
	}
	return false
}

// cpuBoostHeavyPath is the folder test, three levels up at most: Roms/PSP/…,
// Emus/N64/launch.sh, Roms/PORTS/…. PortMaster's own menu is not a game.
// Pure.
func cpuBoostHeavyPath(target string) bool {
	if strings.EqualFold(strings.TrimSuffix(filepath.Base(target), filepath.Ext(target)), "portmaster") {
		return false
	}
	dir := filepath.Dir(target)
	for i := 0; i < 3; i++ {
		name := filepath.Base(dir)
		if name == "" || name == "." || name == string(filepath.Separator) {
			return false
		}
		if cpuBoostHeavySystem(name) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
	return false
}

// ── The boosted session (autoperf.go calls these under autoPerf.mu) ─────────

// cpuBoostGovernor is the governor of the first policy in dirs, "" unknown.
func cpuBoostGovernor(dirs []string) string {
	if len(dirs) == 0 {
		return ""
	}
	return saverReadTrimmed(filepath.Join(dirs[0], "scaling_governor"))
}

// cpuBoostCoresOnlineLocked brings every parked core online for the session,
// recording each so it is parked again after. It returns how many came up.
func cpuBoostCoresOnlineLocked(s *autoPerfSession) int {
	paths, _ := filepath.Glob(filepath.Join(cpufreqRoot, "cpu[0-9]*", "online"))
	woken := 0
	for _, p := range paths {
		if !xboxBoostWritable(p) || saverReadTrimmed(p) != "0" {
			continue
		}
		wrote, err := autoPerfWriteRecordedLocked(&s.writes, p, "0", "1")
		if err != nil {
			logInfo("power", "CPU boost: %s stays parked (%v)", filepath.Base(filepath.Dir(p)), err)
			continue
		}
		if wrote {
			woken++
		}
	}
	return woken
}

// cpuBoostSessionEnd notes a finished boosted session for the Settings row:
// what it held, and how warm it got. Under autoPerf.mu; takes cpuBoost.mu.
func cpuBoostSessionEnd(label string, topKHz int64, cores int, heat string) {
	if label == "" {
		return
	}
	parts := []string{"Last boosted: " + label}
	if topKHz > 0 {
		parts = append(parts, formatFrequencyKHz(topKHz))
	}
	switch {
	case cores == 1:
		parts = append(parts, "1 parked core woken")
	case cores > 1:
		parts = append(parts, fmt.Sprintf("%d parked cores woken", cores))
	}
	if heat != "" {
		parts = append(parts, heat)
	}
	cpuBoost.mu.Lock()
	cpuBoost.status = strings.Join(parts, " · ")
	cpuBoost.mu.Unlock()
}

// ── Settings → Games & emulators → CPU boost in games ──────────────────────

func cpuBoostOptions(*Config) []setOption {
	return []setOption{
		{Value: cpuBoostAuto, Label: "Auto", Note: "N64, PSP, Dreamcast, DS, Saturn, Jaguar, GameCube, Wii, PS2 and ports"},
		{Value: cpuBoostAlways, Label: "Always", Note: "Every game and port"},
		{Value: cpuBoostOff, Label: "Off", Note: "Games follow Automatic CPU speed and their launch scripts"},
	}
}

// cpuBoostSettingsRow is Settings → Games & emulators → "CPU boost in games",
// under Automatic CPU speed, on a TrimUI handheld.
func cpuBoostSettingsRow() setRow {
	return setRow{
		ID: cpuBoostKey, Label: "CPU boost in games", Kind: setKindChoice,
		Visible: func(*Config) bool { return autoPerfSupported() },
		Detail:  cpuBoostSettingDetail,
		Options: cpuBoostOptions,
		Current: func(c *Config) int { return ledOptionIndex(cpuBoostOptions(c), cpuBoostMode(c)) },
		Choose: func(c *Config, value string) {
			if c == nil {
				return
			}
			opts := cpuBoostOptions(c)
			i := ledOptionIndex(opts, value)
			if i < 0 {
				return
			}
			setStoreString(c, cpuBoostKey, opts[i].Value)
			cpuBoostModeNow.Store(opts[i].Value)
			setToast("CPU boost in games: " + opts[i].Label + " · from the next game")
		},
	}
}

// cpuBoostSettingDetail is the row's second line. Cheap: drawn every frame.
func cpuBoostSettingDetail(c *Config) string {
	autoPerfShown.mu.Lock()
	reason := autoPerfShown.reason
	autoPerfShown.mu.Unlock()
	if reason != "" {
		return "Not available: " + reason
	}
	if cpuFreqHolds(c, true) {
		// Settings → CPU frequency holds the games (cpufreq_manual.go).
		return "Resting: games run at the CPU frequency set by hand"
	}
	mode := cpuBoostMode(c)
	if mode == cpuBoostOff {
		return "Games follow Automatic CPU speed and their launch scripts"
	}
	cpuBoost.mu.Lock()
	st := cpuBoost.status
	cpuBoost.mu.Unlock()
	switch {
	case st != "":
		return st
	case mode == cpuBoostAlways:
		return "Every game holds the top CPU clock while the chip is cool"
	}
	return "Heavy consoles and ports hold the top CPU clock while the chip is cool"
}
