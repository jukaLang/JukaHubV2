package main

// dolphin_speed.go — Settings → Games & emulators → "GameCube speed": how much
// of Dolphin's accuracy JukaHub trades for frames on the handheld.
//
// Juka, 2026-11-21: "are there any optimizations we can do for Trimui Smart Pro
// to get Gamecube sort of running at least a few fps?". The A133P (four
// Cortex-A53 at 1.8 GHz, a PowerVR GE8300, 1 GB) is far below what Dolphin is
// written for, and the machine is already the game's while it plays
// (xboxboost.go: every core at its top clock, the GPU at full speed, JukaHub's
// memory handed back, the game's process ahead of everything else). What is
// left is Dolphin's own settings and RetroArch's around it. Two levels, written
// for one launch at a time through raScopeApply (racfgscope.go: journalled,
// put back when RetroArch exits) into the options file RetroArch reads for the
// game (raTuneCoreOptionFile) and into its config:
//
//	Fast     nothing a game's picture depends on. Dolphin's CPU on a thread
//	         of its own beside its GPU's ("dual core": with four slow cores the
//	         one setting that matters most, so it is set on every launch; its
//	         aarch64 freeze was fixed on 2026-10-23, and an older core is
//	         replaced before it starts, dolphin_setup.go), shaders compiled in
//	         the background with the object skipped for the frame or two that
//	         takes, native resolution (640x528), duplicate frames not presented
//	         again, EFB copies written back to RAM only when they are read; in
//	         RetroArch no run-ahead, preemptive frames or rewind, each of which
//	         would run Dolphin twice or save its whole state every frame.
//	Fastest  Fast, and three trades: the CPU's reads of the GPU's frame
//	         buffer skipped ("Skip EFB Access from CPU": a read-back on a
//	         mobile GPU waits for every draw before it; some effects that read
//	         the screen go — Sunshine's goop, a lens flare, a targeting
//	         reticle), the GameCube's CPU emulated at half its clock (a game
//	         with cycles to spare plays as before with half the work per frame;
//	         a busy scene slows down inside the game instead of the whole game
//	         crawling), and RetroArch's vsync off (no wait for the panel
//	         between frames; the picture may tear).
//
// "Dolphin's own" writes nothing but the first start's shader settings
// (dolphin_video.go). Automatic is Fastest on the A133P models (Smart Pro,
// Brick, Brick Pro) and Fast on the Smart Pro S (A523), which plays the lighter
// games at full speed already. The audio resampler is the Emulation page's,
// whose Automatic picks the fast one for GameCube, Wii and PS2 (ratuning.go).
//
// A choice someone made wins, as with N64 graphics (n64light.go): an option is
// written while the file holds nothing for it, a value Dolphin ships with, or
// the value written here — except dual core, background shaders, duplicate
// frames and deferred EFB copies, which Fast sets whatever the file holds
// (Dolphin's own leaves them to the player). A game's own options file
// (RetroArch's "Save Game Options") is not touched at all. An older core build
// spelled some values differently (the CPU clock as "100%", the resolution as
// "x1 (640 x 528)"); the spelling of the value in the file is followed. A
// value a build does not offer is dropped by RetroArch, which then uses the
// core's default.

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	// dolphinSpeedSetting stores the choice.
	dolphinSpeedSetting = "gamecubeSpeed"
	dolphinSpeedAuto    = "auto"
	dolphinSpeedFastest = "fastest"
	dolphinSpeedFast    = "fast"
	dolphinSpeedOwn     = "own"
)

// dolphinSpeedModes is the Settings row's order.
var dolphinSpeedModes = []string{dolphinSpeedAuto, dolphinSpeedFastest, dolphinSpeedFast, dolphinSpeedOwn}

// dolphinSpeedFrom reads the setting: Automatic when unset or unknown.
func dolphinSpeedFrom(c *Config) string {
	if c != nil {
		if v, ok := customString(c.Variables.Custom, dolphinSpeedSetting); ok {
			v = strings.ToLower(strings.TrimSpace(v))
			if slices.Contains(dolphinSpeedModes, v) {
				return v
			}
		}
	}
	return dolphinSpeedAuto
}

// dolphinSpeedAutoLevel is what Automatic means here: Fastest on the A133P,
// Fast on any other chip.
func dolphinSpeedAutoLevel() string {
	if dolphinSoC() == socA133P {
		return dolphinSpeedFastest
	}
	return dolphinSpeedFast
}

// dolphinSpeedLevel is the level launches use: fastest, fast or own.
func dolphinSpeedLevel(c *Config) string {
	if mode := dolphinSpeedFrom(c); mode != dolphinSpeedAuto {
		return mode
	}
	return dolphinSpeedAutoLevel()
}

func dolphinSpeedLabel(level string) string {
	switch level {
	case dolphinSpeedFastest:
		return "Fastest"
	case dolphinSpeedFast:
		return "Fast"
	case dolphinSpeedOwn:
		return "Dolphin's own"
	}
	return "Automatic"
}

// ── What a level writes ──────────────────────────────────────────────────

// dolphinSpeedOpt is one core option a level sets.
type dolphinSpeedOpt struct {
	Key   string
	Value string // the spelling of the builds since the 2025 rebase
	// Old is the value in an older build's spelling ("" when it is the same),
	// used when the file holds a value that is not a number.
	Old string
	// Defaults are the values nobody chose (Dolphin's, in either spelling);
	// any other value in the file is someone's and stays. Always sets the
	// value whatever the file holds.
	Defaults []string
	Always   bool
}

// dolphinSpeedFastOpts are Fast's options (see the file comment).
var dolphinSpeedFastOpts = []dolphinSpeedOpt{
	{Key: "dolphin_main_cpu_thread", Value: "enabled", Always: true},
	// Ubershaders (1, 2) cost these GPUs more than the stutter they save
	// (dolphin_video.go), so every other mode counts as nobody's choice too.
	{Key: "dolphin_shader_compilation_mode", Value: "3", Old: "a-sync Skip Rendering", Always: true},
	{Key: "dolphin_efb_scale", Value: "1", Old: "x1 (640 x 528)", Defaults: []string{"0"}},
	{Key: "dolphin_skip_dupe_frames", Value: "enabled", Always: true},
	{Key: "dolphin_defer_efb_copies", Value: "enabled", Always: true},
}

// dolphinSpeedFastestOpts are what Fastest adds.
var dolphinSpeedFastestOpts = []dolphinSpeedOpt{
	{Key: "dolphin_efb_access_enable", Value: "disabled", Defaults: []string{"enabled"}},
	{Key: "dolphin_cpu_clock_rate", Value: "0.50", Old: "50%", Defaults: []string{"1.00", "1.0", "1", "100%"}},
}

// dolphinSpeedTable is a level's core options. Pure.
func dolphinSpeedTable(level string) []dolphinSpeedOpt {
	switch level {
	case dolphinSpeedFastest:
		return append(slices.Clone(dolphinSpeedFastOpts), dolphinSpeedFastestOpts...)
	case dolphinSpeedFast:
		return slices.Clone(dolphinSpeedFastOpts)
	}
	return nil
}

// dolphinSpeedConfigValues are a level's RetroArch settings. Pure.
func dolphinSpeedConfigValues(level string) []raScopeValue {
	if level != dolphinSpeedFast && level != dolphinSpeedFastest {
		return nil
	}
	values := []raScopeValue{
		{Key: "run_ahead_enabled", Value: "false"},
		{Key: "preemptive_frames_enable", Value: "false"},
		{Key: "rewind_enable", Value: "false"},
	}
	if level == dolphinSpeedFastest {
		values = append(values, raScopeValue{Key: "video_vsync", Value: "false"})
	}
	return values
}

// dolphinSpeedNumeric reports a value written as a number ("3", "1.00").
func dolphinSpeedNumeric(v string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return err == nil
}

// dolphinSpeedValues are the values an options file takes from table: every
// option it has no value for, holds at one of Dolphin's defaults or at the
// value already, and every Always option. kept names the options left at
// someone's own value, for the log. Pure.
func dolphinSpeedValues(opt string, table []dolphinSpeedOpt) (values []raScopeValue, kept []string) {
	for _, o := range table {
		cur, had := raConfigGet(opt, o.Key)
		value := o.Value
		if had && o.Old != "" && strings.TrimSpace(cur) != "" && !dolphinSpeedNumeric(cur) {
			value = o.Old
		}
		if had && !o.Always && !raValueEqual(cur, value) && !dolphinSpeedIsDefault(cur, o) {
			kept = append(kept, o.Key+" = "+strings.TrimSpace(cur))
			continue
		}
		values = append(values, raScopeValue{Key: o.Key, Value: value})
	}
	return values, kept
}

// dolphinSpeedIsDefault reports a value that is one of o's defaults, or empty.
func dolphinSpeedIsDefault(v string, o dolphinSpeedOpt) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return true
	}
	for _, d := range o.Defaults {
		if strings.EqualFold(v, d) {
			return true
		}
	}
	return false
}

// dolphinSpeedGameFile reports an options file of one game or one folder's
// games (config/dolphin-emu/<name>.opt other than the core's own): a choice
// made for those games, which a level leaves alone.
func dolphinSpeedGameFile(opt string) bool {
	return strings.EqualFold(filepath.Base(filepath.Dir(opt)), dolphinCoreConfigName) &&
		!strings.EqualFold(filepath.Base(opt), dolphinCoreConfigName+".opt")
}

// ── Launch ───────────────────────────────────────────────────────────────

// dolphinSpeedPrepare is the launch hook (gamelaunch_hooks.go): a start in
// Dolphin on the handheld gets its level's settings for this launch. It never
// blocks or fails a launch: what cannot be set is logged and the game starts
// with Dolphin's own. Main thread.
func dolphinSpeedPrepare(launcher string, game libraryGame) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("emulation", "GameCube speed: preparing the launch failed: %v", r)
		}
	}()
	if appConfig == nil || dolphinSoC() == "" || !retroArchLauncher(launcher) || nextuiIsPakLauncher(launcher) {
		return
	}
	if !dolphinLauncherPlays(launcher, game.System) {
		return
	}
	level := dolphinSpeedLevel(appConfig)
	if level == dolphinSpeedOwn {
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		return
	}
	var set []string
	// RetroArch's own settings, in each config of its install and, for a key
	// it sets itself, in Dolphin's override, which RetroArch reads last.
	cfgValues := dolphinSpeedConfigValues(level)
	for _, path := range targets {
		if _, err := raScopeApply(path, cfgValues); err != nil {
			logWarn("emulation", "GameCube speed: cannot set RetroArch's settings in %s: %v", path, err)
		}
	}
	if _, override := dolphinOverridePaths(launcher); override != "" {
		if raw, err := os.ReadFile(override); err == nil {
			var held []raScopeValue
			for _, v := range cfgValues {
				if _, has := raConfigGet(string(raw), v.Key); has {
					held = append(held, v)
				}
			}
			if len(held) > 0 {
				if _, err := raScopeApply(override, held); err != nil {
					logWarn("emulation", "GameCube speed: cannot set RetroArch's settings in %s: %v", override, err)
				}
			}
		}
	}
	for _, v := range cfgValues {
		set = append(set, v.Key+" = "+v.Value)
	}
	// Dolphin's options.
	core := gameCoreFromLauncher(launcher, game.System)
	opt := raTuneCoreOptionFile(targets[0], core, game.Path)
	switch {
	case opt == "":
		logInfo("emulation", "GameCube speed: %s has no options file yet; Dolphin's own options this launch", core)
	case dolphinSpeedGameFile(opt):
		logInfo("emulation", "GameCube speed: %s has options of its own (%s); they stay", game.Title, filepath.Base(opt))
	default:
		raw, err := os.ReadFile(opt)
		if err != nil {
			logWarn("emulation", "GameCube speed: cannot read %s: %v", opt, err)
			break
		}
		values, kept := dolphinSpeedValues(string(raw), dolphinSpeedTable(level))
		if len(kept) > 0 {
			logInfo("emulation", "GameCube speed: kept your own %s", strings.Join(kept, ", "))
		}
		if len(values) == 0 {
			break
		}
		if _, err := raScopeApply(opt, values); err != nil {
			logWarn("emulation", "GameCube speed: cannot set Dolphin's options in %s: %v", opt, err)
			break
		}
		for _, v := range values {
			set = append(set, v.Key+" = "+v.Value)
		}
	}
	Log().Info("GameCube speed for this launch", "game", game.Title, "level", dolphinSpeedLabel(level), "settings", strings.Join(set, ", "))
}

// ── Settings → Games & emulators ─────────────────────────────────────────

// dolphinSpeedSettingsRow is "GameCube speed", under "GameCube & Wii", on the
// handheld while GameCube and Wii play there (Automatic or On).
func dolphinSpeedSettingsRow() setRow {
	return setRow{
		ID: dolphinSpeedSetting, Label: "GameCube speed", Kind: setKindChoice,
		Visible: func(c *Config) bool {
			switch dolphinModeFrom(c) {
			case dolphinModeOn:
				return dolphinSoC() != ""
			case dolphinModeAuto:
				return dolphinSoC() != ""
			}
			return false
		},
		Detail: dolphinSpeedDetail,
		Options: func(*Config) []setOption {
			auto := "Automatic · " + dolphinSpeedLabel(dolphinSpeedAutoLevel())
			notes := map[string]string{
				dolphinSpeedAuto:    "Fastest on the Smart Pro, Brick and Brick Pro; Fast on the Smart Pro S",
				dolphinSpeedFastest: "Fast, plus: Dolphin skips the CPU's reads of the picture (some effects vanish), the GameCube's CPU runs at half its clock (busy scenes slow down in the game) and vsync is off (the picture may tear)",
				dolphinSpeedFast:    "Dual core, shaders in the background, native resolution, no duplicate frames, no run-ahead or rewind: every game still looks as it should",
				dolphinSpeedOwn:     "Only what you set in RetroArch's Core Options",
			}
			out := make([]setOption, 0, len(dolphinSpeedModes))
			for _, m := range dolphinSpeedModes {
				label := dolphinSpeedLabel(m)
				if m == dolphinSpeedAuto {
					label = auto
				}
				out = append(out, setOption{Value: m, Label: label, Note: notes[m]})
			}
			return out
		},
		Current: func(c *Config) int { return slices.Index(dolphinSpeedModes, dolphinSpeedFrom(c)) },
		Choose: func(c *Config, value string) {
			if !slices.Contains(dolphinSpeedModes, value) {
				value = dolphinSpeedAuto
			}
			setStoreString(c, dolphinSpeedSetting, value)
			setToast("GameCube speed: " + dolphinSpeedLabel(dolphinSpeedLevel(c)) + " · from the next game")
		},
	}
}

// dolphinSpeedDetail is the row's second line. Cheap: drawn every frame.
func dolphinSpeedDetail(c *Config) string {
	switch dolphinSpeedLevel(c) {
	case dolphinSpeedFastest:
		return "Fastest · speed before effects; options you saved for a game in RetroArch stay"
	case dolphinSpeedFast:
		return "Fast · speed settings that change no game's picture"
	}
	return "Dolphin's own settings · JukaHub changes none"
}

func init() {
	if len(raTuneCoreFolders[dolphinCoreName]) == 0 {
		raTuneCoreFolders[dolphinCoreName] = []string{dolphinCoreConfigName}
	}
	gameLaunchRegisterPrepare(dolphinSpeedPrepare)
}
