package main

// raffspeed.go — Settings → Emulation → Fast-forward speed: how fast a
// RetroArch game runs while fast-forward is on.
//
// RetroArch caps fast-forward with fastforward_ratio: 0 runs as fast as the
// chip can, 2.0 at twice the game's speed, and so on. Uncapped, a Game Boy
// game on the A133 races at ten times its speed or more — too fast to read
// the text a player fast-forwards through — and the cap is three menus deep in
// RetroArch. The row offers RetroArch's own (nothing written; the default),
// Unlimited, 2×, 3×, 4×, 6× and 8×, for All consoles or the console picked in
// the page's "Adjust for" (ratuning_settings.go), a console's ladder starting
// at "Default · …" like the page's other scoped rows.
//
// Stored as raTuningFFSpeed (all consoles) and raTuning.<console>.ffspeed, the
// custom-map keys of the rest of the Emulation page; the value is the ratio
// ("0" is Unlimited) or "own". At a library launch (raFFSpeedPrepare, a
// gamelaunch_hooks.go prepare hook) the ratio goes into retroarch.cfg through
// raScopeApply (racfgscope.go) and comes back out when RetroArch exits. It is
// written the way RetroArch saves a float ("2.000000"): RetroArch rewrites the
// file on exit, and the restore only puts back a key that still holds exactly
// what JukaHub wrote. Fast-forward itself is still started the usual ways (the
// in-game menu's Fast forward, a hotkey from rahotkeys*.go). Standalone
// emulators and NextUI paks (minarch has its own limit) are left alone.

import (
	"strconv"
	"strings"
)

const (
	// raFFSpeedKey is the all-consoles choice; a console's own is
	// raTuning.<console slug>.ffspeed (raTuneConsoleKey).
	raFFSpeedKey  = "raTuningFFSpeed"
	raFFSpeedWhat = "ffspeed"

	// raFFSpeedCfgKey is RetroArch's own key.
	raFFSpeedCfgKey = "fastforward_ratio"
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
	{"2", "2×", "Twice the game's speed"},
	{"3", "3×", "Three times the game's speed"},
	{"4", "4×", "Four times the game's speed"},
	{"6", "6×", "Six times the game's speed"},
	{"8", "8×", "Eight times the game's speed"},
}

// normaliseRAFFSpeed keeps a stored value this build knows ("" drops it).
func normaliseRAFFSpeed(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == raTuneOwn {
		return v
	}
	for _, s := range raFFSpeedStops {
		if v == s.Value {
			return v
		}
	}
	return ""
}

// raFFSpeedLabel names a stored value.
func raFFSpeedLabel(v string) string {
	for _, s := range raFFSpeedStops {
		if v == s.Value {
			return s.Label
		}
	}
	return "RetroArch's own"
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

// raFFSpeedValue is a stored choice as retroarch.cfg holds it ("2.000000"),
// "" for RetroArch's own. Pure.
func raFFSpeedValue(v string) string {
	if normaliseRAFFSpeed(v) == "" || v == raTuneOwn {
		return ""
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return ""
	}
	return strconv.FormatFloat(float64(n), 'f', 6, 64)
}

// ── Launch ───────────────────────────────────────────────────────────────

// raFFSpeedPrepare is the launch hook. It never blocks or fails a launch.
func raFFSpeedPrepare(launcher string, game libraryGame) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("emulation", "preparing the fast-forward speed failed: %v", r)
		}
	}()
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
	for _, cfgPath := range targets {
		if _, err := raScopeApply(cfgPath, []raScopeValue{{Key: raFFSpeedCfgKey, Value: value}}); err != nil {
			logWarn("emulation", "cannot set the fast-forward speed in %s: %v", cfgPath, err)
			continue
		}
	}
	Log().Info("fast-forward speed for this launch", "system", game.System, "speed", raFFSpeedLabel(choice))
}

// ── Settings → Emulation ─────────────────────────────────────────────────

// raFFSpeedOptions are the row's stops at the page's scope.
func raFFSpeedOptions(c *Config) []setOption {
	out := make([]setOption, 0, len(raFFSpeedStops)+2)
	if raTuneScope != "" {
		out = append(out, setOption{Value: "", Label: "Default · " + raFFSpeedLabel(raFFSpeedSetting(c)), Note: raTuneDefaultNote})
	}
	out = append(out, setOption{Value: raTuneOwn, Label: "RetroArch's own", Note: "Leave RetroArch's fast-forward limit alone (none unless set there)"})
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
			return "How fast a game runs while fast-forward is on (the in-game menu or a hotkey)"
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
}
