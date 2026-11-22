package main

// raffspeed.go — Settings → Emulation → Fast-forward speed: how fast a
// RetroArch game runs while fast-forward is on.
//
// RetroArch caps fast-forward with fastforward_ratio: 0 runs as fast as the
// chip can, 2.0 at twice the game's speed, and so on. Uncapped, a Game Boy
// game on the A133 races at ten times its speed or more — too fast to read
// the text a player fast-forwards through — and the cap is three menus deep in
// RetroArch. The row offers RetroArch's own (nothing written; the default),
// Unlimited, 1.5×, 2×, 3×, 4×, 5×, 6×, 8× and 10×, for All consoles or the
// console picked in the page's "Adjust for" (ratuning_settings.go), a
// console's ladder starting at "Default · …" like the page's other scoped
// rows.
//
// Stored as raTuningFFSpeed (all consoles) and raTuning.<console>.ffspeed, the
// custom-map keys of the rest of the Emulation page; the value is the ratio
// ("0" is Unlimited, "2.5" a speed set in RetroArch, see below) or "own". At a
// library launch (raFFSpeedPrepare, a gamelaunch_hooks.go prepare hook) the
// ratio goes into retroarch.cfg through raScopeApply (racfgscope.go) and comes
// back out when RetroArch exits. It is written the way RetroArch saves a float
// ("2.000000"): RetroArch rewrites the file on exit, and the restore only puts
// back a key that still holds exactly what JukaHub wrote.
//
// A speed changed in RetroArch's own menu is kept. A Reddit thread about a
// Brick (Nov 2026) had it the other way round on another firmware: the player
// turned the limit down in RetroArch, and after a sleep or a power-off the
// launcher's config put it back to Unlimited. Here, when the restore finds
// fastforward_ratio no longer holding what this launch wrote (RetroArch saved
// the player's choice on exit; raScopeKeptHook), that value becomes the
// setting the launch used — the console's own when it had one, All consoles
// otherwise — so the next launch writes the player's speed instead of
// overwriting it, and a toast says so. A power cut mid-game stops RetroArch
// before it saves anything, so a speed changed then is not seen.
//
// Fast-forward itself is still started the usual ways (the in-game menu's Fast
// forward, whose line names the speed, or a hotkey from rahotkeys*.go and
// gamehotkeys.go). Standalone emulators and NextUI paks (minarch has its own
// limit) are left alone.

import (
	"math"
	"strconv"
	"strings"
	"sync"
)

const (
	// raFFSpeedKey is the all-consoles choice; a console's own is
	// raTuning.<console slug>.ffspeed (raTuneConsoleKey).
	raFFSpeedKey  = "raTuningFFSpeed"
	raFFSpeedWhat = "ffspeed"

	// raFFSpeedCfgKey is RetroArch's own key.
	raFFSpeedCfgKey = "fastforward_ratio"

	// raFFSpeedMax is the highest ratio kept; RetroArch's menu stops at 10,
	// a hand-edited file may say more. A ratio of 1 or less (other than 0,
	// Unlimited) is no fast-forward at all and is not kept either.
	raFFSpeedMax = 30
)

// raFFSpeedStop is one speed the row offers: the stored ratio, its words.
type raFFSpeedStop struct {
	Value string
	Label string
	Note  string
}

// raFFSpeedStops are the speeds after RetroArch's own, in menu order.
var raFFSpeedStops = []raFFSpeedStop{
	{"0", "Unlimited", "As fast as the handheld can run the game"},
	{"1.5", "1.5×", "A little faster: cut-scenes and walking"},
	{"2", "2×", "Twice the game's speed"},
	{"3", "3×", "Three times the game's speed"},
	{"4", "4×", "Four times the game's speed"},
	{"5", "5×", "Five times the game's speed"},
	{"6", "6×", "Six times the game's speed"},
	{"8", "8×", "Eight times the game's speed"},
	{"10", "10×", "Ten times: grinding and long waits"},
}

// normaliseRAFFSpeed keeps a stored value this build knows ("" drops it):
// "own", or a ratio — 0, or above 1 up to raFFSpeedMax — in its short form,
// one decimal at most ("2.000000" → "2", "2.54" → "2.5"). Pure.
func normaliseRAFFSpeed(v string) string {
	v = strings.ToLower(strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"`)))
	if v == raTuneOwn {
		return v
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(n) {
		return ""
	}
	n = math.Round(n*10) / 10
	if n != 0 && (n <= 1 || n > raFFSpeedMax) {
		return ""
	}
	return strconv.FormatFloat(n, 'f', -1, 64)
}

// raFFSpeedLabel names a stored value. Pure.
func raFFSpeedLabel(v string) string {
	for _, s := range raFFSpeedStops {
		if v == s.Value {
			return s.Label
		}
	}
	if n := normaliseRAFFSpeed(v); n != "" && n != raTuneOwn {
		return n + "×"
	}
	return "RetroArch's own"
}

// raFFSpeedIsStop reports whether v is one of the row's stops. Pure.
func raFFSpeedIsStop(v string) bool {
	for _, s := range raFFSpeedStops {
		if v == s.Value {
			return true
		}
	}
	return false
}

// raFFSpeedSetting is the all-consoles choice; unset is RetroArch's own.
func raFFSpeedSetting(c *Config) string {
	if v := normaliseRAFFSpeed(raTuneRaw(c, raFFSpeedKey)); v != "" {
		return v
	}
	return raTuneOwn
}

// raFFSpeedConsole is one console's own choice, "" when it follows All
// consoles.
func raFFSpeedConsole(c *Config, console string) string {
	return normaliseRAFFSpeed(raTuneRaw(c, raTuneConsoleKey(console, raFFSpeedWhat)))
}

// raFFSpeedFor is the choice a launch of a system's game uses.
func raFFSpeedFor(c *Config, system string) string {
	sys, _ := raTuneSystemFor(system)
	if v := raFFSpeedConsole(c, sys.Name); v != "" {
		return v
	}
	return raFFSpeedSetting(c)
}

// raFFSpeedKeyFor is the setting a launch of a system's game reads: the
// console's own key when it has a value, All consoles' otherwise.
func raFFSpeedKeyFor(c *Config, system string) (key, console string) {
	sys, _ := raTuneSystemFor(system)
	if raFFSpeedConsole(c, sys.Name) != "" {
		return raTuneConsoleKey(sys.Name, raFFSpeedWhat), sys.Name
	}
	return raFFSpeedKey, ""
}

// raFFSpeedValue is a stored choice as retroarch.cfg holds it ("2.000000"),
// "" for RetroArch's own. Pure.
func raFFSpeedValue(v string) string {
	v = normaliseRAFFSpeed(v)
	if v == "" || v == raTuneOwn {
		return ""
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return ""
	}
	return strconv.FormatFloat(n, 'f', 6, 64)
}

// ── Launch ───────────────────────────────────────────────────────────────

// raFFSpeedLaunch is what the last library launch wrote, so a speed the
// player changed in RetroArch's menu can be kept in the setting it came
// from. Prepare (main thread) sets it; the restore hook (raScope's goroutine)
// takes it.
var raFFSpeedLaunch struct {
	mu      sync.Mutex
	wrote   string // the value written ("4.000000"); "" when nothing was
	key     string // the custom-map key the choice came from
	console string // that key's console, "" for All consoles
}

// raFFSpeedPrepare is the launch hook. It never blocks or fails a launch.
func raFFSpeedPrepare(launcher string, game libraryGame) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("emulation", "preparing the fast-forward speed failed: %v", r)
		}
	}()
	raFFSpeedLaunch.mu.Lock()
	raFFSpeedLaunch.wrote, raFFSpeedLaunch.key, raFFSpeedLaunch.console = "", "", ""
	raFFSpeedLaunch.mu.Unlock()
	if appConfig == nil || !retroArchLauncher(launcher) || nextuiIsPakLauncher(launcher) {
		return
	}
	choice := raFFSpeedFor(appConfig, game.System)
	value := raFFSpeedValue(choice)
	if value == "" {
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		logInfo("emulation", "no retroarch.cfg found for %s; fast-forward keeps RetroArch's own speed", launcher)
		return
	}
	key, console := raFFSpeedKeyFor(appConfig, game.System)
	raFFSpeedLaunch.mu.Lock()
	raFFSpeedLaunch.wrote, raFFSpeedLaunch.key, raFFSpeedLaunch.console = value, key, console
	raFFSpeedLaunch.mu.Unlock()
	for _, cfgPath := range targets {
		if _, err := raScopeApply(cfgPath, []raScopeValue{{Key: raFFSpeedCfgKey, Value: value}}); err != nil {
			logWarn("emulation", "cannot set the fast-forward speed in %s: %v", cfgPath, err)
			continue
		}
	}
	Log().Info("fast-forward speed for this launch", "system", game.System, "speed", raFFSpeedLabel(choice))
}

// raFFSpeedKept is raScopeKeptHook: fastforward_ratio no longer holds what
// the last launch wrote, so the player set another speed in RetroArch. Runs
// under raScope.mu; it only takes the launch record and posts the change.
func raFFSpeedKept(e raScopeEdit, now string) {
	if !strings.EqualFold(e.Key, raFFSpeedCfgKey) {
		return
	}
	value := normaliseRAFFSpeed(now)
	if value == "" || value == raTuneOwn {
		return // 0.5, garbage: not a speed this row can hold
	}
	raFFSpeedLaunch.mu.Lock()
	key, console, wrote := raFFSpeedLaunch.key, raFFSpeedLaunch.console, raFFSpeedLaunch.wrote
	if wrote == "" || !raValueEqual(e.Wrote, wrote) {
		raFFSpeedLaunch.mu.Unlock()
		return // not this session's launch (a journal a power cut left)
	}
	// Once per launch: the same RetroArch may own two configs.
	raFFSpeedLaunch.wrote, raFFSpeedLaunch.key, raFFSpeedLaunch.console = "", "", ""
	raFFSpeedLaunch.mu.Unlock()
	tbxPost(func() { raFFSpeedAdopt(key, console, value) })
}

// raFFSpeedAdopt stores a speed chosen in RetroArch's menu. Main thread.
func raFFSpeedAdopt(key, console, value string) {
	if appConfig == nil || key == "" {
		return
	}
	if normaliseRAFFSpeed(raTuneRaw(appConfig, key)) == value {
		return
	}
	setStoreString(appConfig, key, value)
	saveUserConfigDebounced(userConfigFromVariables(appConfig))
	where := "all consoles"
	if console != "" {
		where = console + " games"
	}
	logInfo("emulation", "fast-forward speed %s set in RetroArch kept for %s", raFFSpeedLabel(value), where)
	ShowToast("Fast-forward speed "+raFFSpeedLabel(value)+" from RetroArch kept for "+where, ToastKindInfo)
}

// raFFSpeedGameNote is the in-game menu's Fast forward line for the running
// game's console: the speed it runs at.
func raFFSpeedGameNote(system string) string {
	if appConfig == nil || strings.TrimSpace(system) == "" {
		return ""
	}
	switch v := raFFSpeedFor(appConfig, system); v {
	case raTuneOwn:
		return ""
	case "0":
		return "Run the game as fast as it goes until you switch it off"
	default:
		return "Run the game at " + raFFSpeedLabel(v) + " until you switch it off"
	}
}

// ── Settings → Emulation ─────────────────────────────────────────────────

// raFFSpeedOptions are the row's stops at the page's scope, plus the
// scope's own value when it is a speed kept from RetroArch's menu.
func raFFSpeedOptions(c *Config) []setOption {
	out := make([]setOption, 0, len(raFFSpeedStops)+3)
	if raTuneScope != "" {
		out = append(out, setOption{Value: "", Label: "Default · " + raFFSpeedLabel(raFFSpeedSetting(c)), Note: raTuneDefaultNote})
	}
	out = append(out, setOption{Value: raTuneOwn, Label: "RetroArch's own", Note: "Leave RetroArch's fast-forward limit alone (none unless set there)"})
	if v := raFFSpeedStored(c); v != "" && v != raTuneOwn && !raFFSpeedIsStop(v) {
		out = append(out, setOption{Value: v, Label: raFFSpeedLabel(v), Note: "Set in RetroArch's menu during a game"})
	}
	for _, s := range raFFSpeedStops {
		out = append(out, setOption{Value: s.Value, Label: s.Label, Note: s.Note})
	}
	return out
}

// raFFSpeedStored is the scope's own value as the row shows it.
func raFFSpeedStored(c *Config) string {
	if raTuneScope == "" {
		return raFFSpeedSetting(c)
	}
	return raFFSpeedConsole(c, raTuneScope)
}

// raFFSpeedSettingsRows is the Emulation page's row (ratuning_settings.go).
func raFFSpeedSettingsRows() []setRow {
	return []setRow{{
		ID: "raFFSpeed", Label: "Fast-forward speed", Kind: setKindChoice,
		Detail: func(*Config) string {
			if raTuneScope != "" {
				return raTuneScope + " games only"
			}
			return "How fast a game runs while fast-forward is on; a speed changed in RetroArch's menu is kept"
		},
		Options: raFFSpeedOptions,
		Current: func(c *Config) int { return raTuneOptionIndex(raFFSpeedOptions(c), raFFSpeedStored(c)) },
		Choose: func(c *Config, value string) {
			value = normaliseRAFFSpeed(value)
			if raTuneScope == "" && value == "" {
				value = raTuneOwn
			}
			setStoreString(c, raTuneScopedKey(raFFSpeedKey, raFFSpeedWhat), value)
			label := raFFSpeedLabel(value)
			if value == "" {
				label = "Default"
			}
			raTuneScopedToast("Fast-forward speed", label)
		},
	}}
}

func init() {
	gameLaunchRegisterPrepare(raFFSpeedPrepare)
	raScopeKeptHook = raFFSpeedKept
}
