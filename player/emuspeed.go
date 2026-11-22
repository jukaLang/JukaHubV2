package main

// emuspeed.go — Settings → Emulation → "Speed": how much accuracy the
// emulators trade for frames on the handheld, per console.
//
// Juka, 2026-11-21: "improve performance of all emulators...especially on
// Trimui Smart Pro and Trimui Brick". The machine was already the game's:
// every game runs at the top CPU clock (powerprofile.go), the heavy consoles'
// GPU at full speed (gpuboost.go), and the heaviest get the whole handheld
// (xboxboost.go). GameCube and Wii had their own trade (dolphin_speed.go) and
// N64 its lighter picture on the Brick (n64light.go). Every other emulator ran
// with its own defaults, which are made for a PC: no frame is ever skipped, so
// a scene the A133P cannot keep up with slows the whole game down and its
// sound crackles; the threaded renderers some cores have sit unused beside
// four cores; and a PSP or Dreamcast game pays for effects nobody sees on a
// small screen. Three levels, written for one launch at a time through
// raScopeApply (racfgscope.go: journalled, put back when RetroArch exits) into
// the options file RetroArch reads for the core (raTuneCoreOptionFile) and
// into its config:
//
//	Fast     nothing a game's picture depends on while the handheld keeps up:
//	         frames are skipped only when it falls behind (the cores' own
//	         audio-buffer frameskip, "auto" in PCSX-ReARMed, FBNeo, MAME 2003,
//	         PicoDrive, Genesis Plus GX, Snes9x, gpSP, mGBA, Handy and Beetle
//	         PCE Fast; PPSSPP's and Flycast's automatic skips; YabaSanshiro's),
//	         the threaded renderers (Mupen64Plus-Next's, melonDS's) and the
//	         faster paths that cost nothing (DeSmuME's JIT, Virtual Jaguar's
//	         fast blitter, Opera's DSP thread, N64 frame duping); and, on every
//	         console above the 8/16-bit ones, RetroArch's run-ahead, preemptive
//	         frames and rewind off, each of which runs the core twice in a
//	         frame or saves its whole state every frame.
//	Fastest  Fast, and trades that can change what a game looks like or how
//	         it times: PSP textures cached lazily, curves at low detail and
//	         GPU read-backs skipped; Dreamcast's sound DSP off, alpha sorted
//	         per strip, no mipmaps or anisotropic filtering and more frames
//	         skippable; the PlayStation's GPU on a thread of its own that the
//	         CPU does not wait for; one DS frame in two skipped; the PS2's
//	         emulated CPU at 75% with mild cycle skipping (ARMSX2); and N64's
//	         light picture (n64light.go).
//	Emulators' own  nothing.
//
// Automatic (the default) goes by console and chip: the 8/16-bit consoles and
// the handhelds keep their emulators' own settings (they play at full speed,
// and their latency settings, ratuning.go, matter more there); PlayStation,
// N64, DS, the arcade boards and the CD consoles get Fast; PSP, Dreamcast,
// Saturn, Jaguar and PS2 get Fastest on the A133P (Smart Pro, Brick, Brick
// Pro, Hammer) and Fast on the Smart Pro S. GameCube and Wii are GameCube
// speed's (dolphin_speed.go): their row on this page shows and changes that
// setting.
//
// A choice someone made wins, as with N64 graphics: an option is written only
// where the options file holds one of the core's own defaults for it, a key
// the file does not have (a core build without it) is not added, and a game's
// or a content folder's own options file (RetroArch's "Save Game Options") is
// not touched at all. A core that has never run has no options file yet;
// RetroArch writes one when it exits, so its second start gets the level.
// Standalone emulators keep their own settings, except JukaHub's own PPSSPP,
// whose ppsspp.ini gets the PSP's level the same way (emuspeed_ppsspp.go).
//
// The heavy consoles' cores also log warnings only, once a core has played a
// game in this run of JukaHub (gameDiagQuietCoreLog): RetroArch writes every
// line a core logs to the card at once, and a core logging at info level while
// it plays costs a write per line. The first start of each core keeps the full
// log, so a game that will not start is still diagnosed (gamediag.go).

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// emuSpeedKey stores the all-consoles choice; a console's own is
	// raTuning.<console>.speed (raTuneConsoleKey).
	emuSpeedKey     = "raTuningSpeed"
	emuSpeedAuto    = "auto"
	emuSpeedFastest = "fastest"
	emuSpeedFast    = "fast"
	emuSpeedOwn     = "own"

	// emuSpeedQuietLogLevel is RetroArch's libretro_log_level for a proven
	// heavy core: 2 = warnings and errors.
	emuSpeedQuietLogLevel = "2"

	// emuSpeedAudioFloorMs is the least audio buffer (ms) a Fast or Fastest
	// launch of a bigger console keeps: the cores' automatic frameskip
	// watches that buffer, and one of a few milliseconds reads as "falling
	// behind" at every hiccup, skipping frames a full-speed game never needed
	// to skip.
	emuSpeedAudioFloorMs = 64
)

// emuSpeedOpt is one core option a level sets.
type emuSpeedOpt struct {
	Key   string
	Value string
	// Numeric is the value for a build whose file holds a number for the
	// option ("" when the spelling is the same).
	Numeric string
	// Defaults are the core's own values (in every spelling its builds used);
	// any other value in the file is someone's choice and stays.
	Defaults []string
	// Fastest marks an option only Fastest writes. A Fastest option for a key
	// Fast writes too replaces Fast's value.
	Fastest bool
}

// emuSpeedCoreTables are the options per core, by core file stem. Every key
// is the core's own (its libretro_core_options.h); a key a build lacks is
// skipped, and a value a build does not offer is dropped by RetroArch, which
// then uses the core's default.
var emuSpeedCoreTables = map[string][]emuSpeedOpt{
	// PlayStation.
	"pcsx_rearmed": {
		{Key: "pcsx_rearmed_frameskip_type", Value: "auto", Defaults: []string{"disabled"}},
		{Key: "pcsx_rearmed_gpu_thread_rendering", Value: "async", Defaults: []string{"disabled"}, Fastest: true},
	},
	// PSP. Automatic frameskip acts only with a frameskip of one or more.
	"ppsspp": {
		{Key: "ppsspp_frameskip", Value: "1", Defaults: []string{"disabled", "0", "Off"}},
		{Key: "ppsspp_auto_frameskip", Value: "enabled", Defaults: []string{"disabled"}},
		{Key: "ppsspp_lazy_texture_caching", Value: "enabled", Defaults: []string{"disabled"}, Fastest: true},
		{Key: "ppsspp_spline_quality", Value: "Low", Defaults: []string{"High"}, Fastest: true},
		{Key: "ppsspp_skip_gpu_readbacks", Value: "enabled", Defaults: []string{"disabled"}, Fastest: true},
	},
	// Dreamcast, NAOMI and Atomiswave (init adds the reicast_ names of the
	// builds before Flycast's 2021 rename).
	"flycast": {
		{Key: "flycast_auto_skip_frame", Value: "some", Defaults: []string{"disabled"}},
		{Key: "flycast_auto_skip_frame", Value: "more", Defaults: []string{"disabled"}, Fastest: true},
		{Key: "flycast_enable_dsp", Value: "disabled", Defaults: []string{"enabled"}, Fastest: true},
		{Key: "flycast_alpha_sorting", Value: "per-strip (fast, least accurate)", Defaults: []string{"per-triangle (normal)"}, Fastest: true},
		{Key: "flycast_mipmapping", Value: "disabled", Defaults: []string{"enabled"}, Fastest: true},
		{Key: "flycast_anisotropic_filtering", Value: "off", Defaults: []string{"4"}, Fastest: true},
	},
	// Nintendo 64: GLideN64 on a thread of its own, and the last frame shown
	// again instead of a late one. Fastest's lighter picture is n64light.go's.
	"mupen64plus_next": {
		{Key: "mupen64plus-ThreadedRenderer", Value: "True", Defaults: []string{"False"}},
		{Key: "mupen64plus-FrameDuping", Value: "True", Defaults: []string{"False"}},
	},
	// Saturn.
	"yabasanshiro": {
		{Key: "yabasanshiro_frameskip", Value: "enabled", Defaults: []string{"disabled"}},
	},
	"yabause": {
		{Key: "yabause_frameskip", Value: "enabled", Defaults: []string{"disabled"}},
	},
	// Nintendo DS.
	"melonds": {
		{Key: "melonds_threaded_renderer", Value: "enabled", Defaults: []string{"disabled"}},
	},
	"desmume": {
		{Key: "desmume_cpu_mode", Value: "jit", Defaults: []string{"interpreter"}},
		{Key: "desmume_frameskip", Value: "1", Defaults: []string{"0"}, Fastest: true},
	},
	"desmume2015": {
		{Key: "desmume_cpu_mode", Value: "jit", Defaults: []string{"interpreter"}},
		{Key: "desmume_frameskip", Value: "1", Defaults: []string{"0"}, Fastest: true},
	},
	// Arcade and Neo Geo.
	"fbneo": {
		{Key: "fbneo-frameskip-type", Value: "Auto", Defaults: []string{"disabled"}},
	},
	"mame2003_plus": {
		{Key: "mame2003-plus_frameskip", Value: "auto", Defaults: []string{"disabled", "0"}},
	},
	"mame2003": {
		{Key: "mame2003_frameskip", Value: "auto", Defaults: []string{"disabled", "0"}},
	},
	// Mega Drive, Sega CD and 32X.
	"picodrive": {
		{Key: "picodrive_frameskip", Value: "auto", Defaults: []string{"disabled"}},
	},
	"genesis_plus_gx": {
		{Key: "genesis_plus_gx_frameskip", Value: "auto", Defaults: []string{"disabled"}},
	},
	// PC Engine and its CD.
	"mednafen_pce_fast": {
		{Key: "pce_fast_frameskip", Value: "auto", Defaults: []string{"disabled"}},
	},
	// SNES (Super FX and SA-1 games are the ones that fall behind).
	"snes9x": {
		{Key: "snes9x_frameskip", Value: "auto", Defaults: []string{"disabled"}},
	},
	"snes9x2010": {
		{Key: "snes9x2010_frameskip", Value: "auto", Defaults: []string{"disabled"}},
	},
	"snes9x2005": {
		{Key: "snes9x2005_frameskip", Value: "auto", Defaults: []string{"disabled"}},
	},
	"snes9x2002": {
		{Key: "snes9x2002_frameskip", Value: "auto", Defaults: []string{"disabled"}},
	},
	// Game Boy Advance.
	"gpsp": {
		{Key: "gpsp_frameskip", Value: "auto", Defaults: []string{"disabled"}},
	},
	"mgba": {
		{Key: "mgba_frameskip", Value: "auto", Defaults: []string{"disabled"}},
	},
	// Atari Lynx.
	"handy": {
		{Key: "handy_frameskip", Value: "auto", Defaults: []string{"disabled"}},
	},
	// Atari Jaguar.
	"virtualjaguar": {
		{Key: "virtualjaguar_usefastblitter", Value: "enabled", Defaults: []string{"disabled"}},
	},
	// 3DO.
	"opera": {
		{Key: "opera_dsp_threaded", Value: "enabled", Defaults: []string{"disabled"}},
	},
	// PlayStation 2 (ARMSX2): the emulated EE at 75% (a game with cycles to
	// spare plays as before; a busy scene slows inside the game instead of
	// the whole game crawling) and mild cycle skipping. Multi-threaded VU1
	// and native resolution are the core's defaults already.
	ps2CoreName: {
		{Key: "armsx2_ee_cycle_rate", Value: "75%", Numeric: "-1", Defaults: []string{"100%", "100% (default)", "0"}, Fastest: true},
		{Key: "armsx2_ee_cycle_skip", Value: "mild", Defaults: []string{"disabled"}, Fastest: true},
	},
}

// emuSpeedCoreFamilies are core stems that share another's table: builds
// named apart by renderer.
var emuSpeedCoreFamilies = map[string]string{
	"mupen64plus_next_gles2": "mupen64plus_next",
	"mupen64plus_next_gles3": "mupen64plus_next",
	"flycast_gles2":          "flycast",
}

// emuSpeedCoreConfigFolders are the option folders (library names) of the
// cores above that no other file lists in raTuneCoreFolders.
var emuSpeedCoreConfigFolders = map[string]string{
	"ppsspp":            "PPSSPP",
	"flycast":           "Flycast",
	"flycast_gles2":     "Flycast",
	"yabasanshiro":      "YabaSanshiro",
	"yabause":           "Yabause",
	"mame2003":          "MAME 2003 (0.78)",
	"mednafen_pce_fast": "Beetle PCE Fast",
	"snes9x2010":        "Snes9x 2010",
	"snes9x2005":        "Snes9x 2005",
	"snes9x2002":        "Snes9x 2002",
	"gpsp":              "gpSP",
	"mgba":              "mGBA",
	"handy":             "Handy",
	"virtualjaguar":     "Virtual Jaguar",
	"opera":             "Opera",
}

func init() {
	// The Flycast builds before 2021 named every option reicast_….
	var reicast []emuSpeedOpt
	for _, o := range emuSpeedCoreTables["flycast"] {
		o.Key = "reicast_" + strings.TrimPrefix(o.Key, "flycast_")
		reicast = append(reicast, o)
	}
	emuSpeedCoreTables["flycast"] = append(emuSpeedCoreTables["flycast"], reicast...)
	for core, folder := range emuSpeedCoreConfigFolders {
		if len(raTuneCoreFolders[core]) == 0 {
			raTuneCoreFolders[core] = []string{folder}
		}
	}
	gameLaunchRegisterPrepare(emuSpeedPrepare)
	gameLaunchRegisterExited(emuSpeedGameExited)
}

// ── Which level ──────────────────────────────────────────────────────────

// emuSpeedSupported: the TrimUI handhelds. A PC's emulators have the power.
func emuSpeedSupported() bool {
	return runtime.GOOS == "linux" && !IsWindows() && IsTrimuiDevice()
}

// emuSpeedSoC is the handheld's chip, "" off the handheld.
func emuSpeedSoC() string {
	if !emuSpeedSupported() {
		return ""
	}
	return deviceHardware(DetectDevice()).SoC
}

func normaliseEmuSpeed(v string) string {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case emuSpeedAuto, emuSpeedFastest, emuSpeedFast, emuSpeedOwn:
		return v
	}
	return ""
}

// emuSpeedSetting is the all-consoles choice; unset is Automatic.
func emuSpeedSetting(c *Config) string {
	if v := normaliseEmuSpeed(raTuneRaw(c, emuSpeedKey)); v != "" {
		return v
	}
	return emuSpeedAuto
}

// emuSpeedConsole is one console's own choice, "" when it follows All
// consoles (Automatic is not a console's own choice: it already is one).
// GameCube and Wii have none here: theirs is GameCube speed.
func emuSpeedConsole(c *Config, system string) string {
	if dolphinIsSystem(system) {
		return ""
	}
	if v := normaliseEmuSpeed(raTuneRaw(c, raTuneConsoleKey(system, "speed"))); v != emuSpeedAuto {
		return v
	}
	return ""
}

// emuSpeedAutoLevel is Automatic's answer for a console tier on a chip.
func emuSpeedAutoLevel(tier raTuneTier, soc string) string {
	switch tier {
	case raTuneHeavy:
		if soc == socA133P {
			return emuSpeedFastest
		}
		return emuSpeedFast
	case raTuneMedium:
		return emuSpeedFast
	}
	return emuSpeedOwn
}

// emuSpeedLevelFor is the level a launch of system's game uses: fastest, fast
// or own. Off the handheld it is always own.
func emuSpeedLevelFor(c *Config, system string) string {
	soc := emuSpeedSoC()
	if soc == "" {
		return emuSpeedOwn
	}
	sys, _ := raTuneSystemFor(system)
	if dolphinIsSystem(sys.Name) {
		return dolphinSpeedLevel(c)
	}
	mode := emuSpeedConsole(c, sys.Name)
	if mode == "" {
		mode = emuSpeedSetting(c)
	}
	if mode == emuSpeedAuto {
		mode = emuSpeedAutoLevel(sys.Tier, soc)
	}
	return mode
}

func emuSpeedLabel(level string) string {
	switch level {
	case emuSpeedFastest:
		return "Fastest"
	case emuSpeedFast:
		return "Fast"
	case emuSpeedOwn:
		return "Emulators' own"
	}
	return "Auto"
}

// ── What a level writes ──────────────────────────────────────────────────

// emuSpeedTableFor is a core's options at a level. Pure.
func emuSpeedTableFor(core, level string) []emuSpeedOpt {
	if level != emuSpeedFast && level != emuSpeedFastest {
		return nil
	}
	core = normalizeCoreName(core)
	if family, ok := emuSpeedCoreFamilies[core]; ok {
		core = family
	}
	var out []emuSpeedOpt
	at := map[string]int{}
	for _, o := range emuSpeedCoreTables[core] {
		if o.Fastest && level != emuSpeedFastest {
			continue
		}
		if i, ok := at[o.Key]; ok {
			out[i] = o
			continue
		}
		at[o.Key] = len(out)
		out = append(out, o)
	}
	return out
}

// emuSpeedValues are the values an options file takes from table: options it
// has a line for that hold one of the core's defaults. kept names the options
// left at someone's own value, for the log. Pure.
func emuSpeedValues(opt string, table []emuSpeedOpt) (values []raScopeValue, kept []string) {
	for _, o := range table {
		cur, had := raConfigGet(opt, o.Key)
		if !had {
			continue
		}
		cur = strings.TrimSpace(cur)
		value := o.Value
		if o.Numeric != "" && dolphinSpeedNumeric(cur) {
			value = o.Numeric
		}
		if raValueEqual(cur, value) {
			continue
		}
		if !emuSpeedIsDefault(cur, o.Defaults) {
			kept = append(kept, o.Key+" = "+cur)
			continue
		}
		values = append(values, raScopeValue{Key: o.Key, Value: value})
	}
	return values, kept
}

// emuSpeedIsDefault reports a value that is empty or one of defaults.
func emuSpeedIsDefault(v string, defaults []string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return true
	}
	for _, d := range defaults {
		if strings.EqualFold(v, d) {
			return true
		}
	}
	return false
}

// emuSpeedConfigValues are a level's RetroArch settings for system's games:
// nothing for the 8/16-bit consoles and handhelds, whose run-ahead (Latency:
// Lowest) and rewind (the in-game menu's, retro_transport.go) are JukaHub's
// own features; run-ahead, preemptive frames and rewind off for the rest.
//
// On the A133P, with Threaded video on Auto, they also get RetroArch's video
// on a thread of its own: a core that draws in software (PCSX-ReARMed, FBNeo,
// MAME, PicoDrive, DeSmuME) hands the PowerVR driver's work to another of the
// four cores instead of waiting for it, which is where those cores lose
// frames on this chip; a core with its own GPU renderer is not affected
// (RetroArch threads only software-drawn video).
func emuSpeedConfigValues(c *Config, system, level string) []raScopeValue {
	if level != emuSpeedFast && level != emuSpeedFastest {
		return nil
	}
	sys, _ := raTuneSystemFor(system)
	if sys.Tier == raTuneLight {
		return nil
	}
	values := []raScopeValue{
		{Key: "run_ahead_enabled", Value: "false"},
		{Key: "preemptive_frames_enable", Value: "false"},
	}
	if !retroTransportLightSystem(system) {
		values = append(values, raScopeValue{Key: "rewind_enable", Value: "false"})
	}
	if emuSpeedSoC() == socA133P && raThreadedSetting(c) == raThreadedAuto {
		values = append(values, raScopeValue{Key: "video_threaded", Value: "true"})
	}
	return values
}

// emuSpeedAudioFloor is the audio_latency a config takes so the automatic
// frameskip has a buffer to watch: emuSpeedAudioFloorMs where the file holds
// a smaller number, nothing where it holds more or none (RetroArch's own 64)
// or where the console's Latency is Low or Lowest (their ceilings are a
// choice for latency). Pure but for the settings read.
func emuSpeedAudioFloor(c *Config, system, cfg string) []raScopeValue {
	switch raTuneLatencyFor(c, system) {
	case raTuneLow, raTuneLowest:
		return nil
	}
	cur, had := raConfigGet(cfg, "audio_latency")
	if !had {
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(cur))
	if err != nil || n <= 0 || n >= emuSpeedAudioFloorMs {
		return nil
	}
	return []raScopeValue{{Key: "audio_latency", Value: strconv.Itoa(emuSpeedAudioFloorMs)}}
}

// emuSpeedGameFile reports an options file of one game or one content folder
// (config/<core>/<name>.opt other than the core's own): someone's choice for
// those games, which a level leaves alone.
func emuSpeedGameFile(opt, core string) bool {
	dir, base := filepath.Base(filepath.Dir(opt)), filepath.Base(opt)
	for _, folder := range raTuneCoreFolders[normalizeCoreName(core)] {
		if strings.EqualFold(dir, folder) {
			return !strings.EqualFold(base, folder+".opt")
		}
	}
	return false
}

// ── Launch ───────────────────────────────────────────────────────────────

// emuSpeedPrepare is the launch hook (gamelaunch_hooks.go): a RetroArch start
// on the handheld gets its console's level for this launch, and a start in
// JukaHub's own PPSSPP its ppsspp.ini values (emuspeed_ppsspp.go). It never
// blocks or fails a launch: what cannot be set is logged and the game starts
// with the emulator's own settings. Main thread.
func emuSpeedPrepare(launcher string, game libraryGame) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("emulation", "emulator speed: preparing the launch failed: %v", r)
		}
	}()
	if appConfig == nil || !emuSpeedSupported() {
		return
	}
	if ppssppIsManagedLauncher(launcher) {
		emuSpeedPPSSPPPrepare(game)
		return
	}
	if !retroArchLauncher(launcher) || nextuiIsPakLauncher(launcher) || dolphinLauncherPlays(launcher, game.System) {
		return // GameCube and Wii: dolphin_speed.go
	}
	level := emuSpeedLevelFor(appConfig, game.System)
	if level == emuSpeedOwn {
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		return
	}
	core := gameCoreFromLauncher(launcher, game.System)
	var set []string
	if values := emuSpeedConfigValues(appConfig, game.System, level); len(values) > 0 {
		applied := false
		floored := false
		for _, path := range targets {
			these := values
			if raw, err := os.ReadFile(path); err == nil {
				if floor := emuSpeedAudioFloor(appConfig, game.System, string(raw)); len(floor) > 0 {
					these = append(append([]raScopeValue(nil), values...), floor...)
					floored = true
				}
			}
			if _, err := raScopeApply(path, these); err != nil {
				logWarn("emulation", "emulator speed: cannot set RetroArch's settings in %s: %v", path, err)
				continue
			}
			applied = true
		}
		if applied {
			for _, v := range values {
				set = append(set, v.Key+" = "+v.Value)
			}
			if floored {
				set = append(set, "audio_latency ≥ "+strconv.Itoa(emuSpeedAudioFloorMs))
			}
		}
	}
	if table := emuSpeedTableFor(core, level); len(table) > 0 {
		opt := raTuneCoreOptionFile(targets[0], core, game.Path)
		switch {
		case opt == "":
			logInfo("emulation", "emulator speed: %s has no options file yet (RetroArch writes one when it exits); its own options this launch", core)
		case emuSpeedGameFile(opt, core):
			logInfo("emulation", "emulator speed: %s has options of its own (%s); they stay", game.Title, filepath.Base(opt))
		default:
			raw, err := os.ReadFile(opt)
			if err != nil {
				logWarn("emulation", "emulator speed: cannot read %s: %v", opt, err)
				break
			}
			values, kept := emuSpeedValues(string(raw), table)
			if len(kept) > 0 {
				logInfo("emulation", "emulator speed: kept your own %s", strings.Join(kept, ", "))
			}
			if len(values) == 0 {
				break
			}
			if _, err := raScopeApply(opt, values); err != nil {
				logWarn("emulation", "emulator speed: cannot set %s's options in %s: %v", core, opt, err)
				break
			}
			for _, v := range values {
				set = append(set, v.Key+" = "+v.Value)
			}
		}
	}
	if len(set) > 0 {
		Log().Info("emulator speed for this launch", "game", game.Title, "core", core, "level", emuSpeedLabel(level), "settings", strings.Join(set, ", "))
	}
}

// ── The heavy consoles' core log ─────────────────────────────────────────

// emuSpeedProven holds the cores that played a game past the quick-exit
// window in this run of JukaHub (normalised stems).
var emuSpeedProven struct {
	mu    sync.Mutex
	cores map[string]bool
}

// emuSpeedGameExited is the Exited hook (the reaper goroutine): a RetroArch
// game that ran past gameDiagQuickExit proves its core starts here.
func emuSpeedGameExited(game libraryGame, launcher string, ranFor time.Duration, _ error) {
	if ranFor < gameDiagQuickExit || !retroArchLauncher(launcher) {
		return
	}
	core := normalizeCoreName(gameCoreFromLauncher(launcher, game.System))
	if core == "" {
		return
	}
	emuSpeedProven.mu.Lock()
	if emuSpeedProven.cores == nil {
		emuSpeedProven.cores = map[string]bool{}
	}
	emuSpeedProven.cores[core] = true
	emuSpeedProven.mu.Unlock()
}

// emuSpeedQuietCoreLog reports whether a launch's core may log warnings only:
// a heavy console's game at Fast or Fastest whose core already played here
// this run (its log is no longer needed to say why a game would not start).
// Main thread.
func emuSpeedQuietCoreLog(game libraryGame, launcher string) bool {
	if appConfig == nil || !emuSpeedSupported() || !retroArchLauncher(launcher) {
		return false
	}
	if sys, _ := raTuneSystemFor(game.System); sys.Tier != raTuneHeavy {
		return false
	}
	if emuSpeedLevelFor(appConfig, game.System) == emuSpeedOwn {
		return false
	}
	core := normalizeCoreName(gameCoreFromLauncher(launcher, game.System))
	emuSpeedProven.mu.Lock()
	defer emuSpeedProven.mu.Unlock()
	return core != "" && emuSpeedProven.cores[core]
}

// gameDiagQuietCoreLog is values with the cores' log level at warnings. Pure.
func gameDiagQuietCoreLog(values []raScopeValue) []raScopeValue {
	out := append([]raScopeValue(nil), values...)
	for i := range out {
		if out[i].Key == "libretro_log_level" {
			out[i].Value = emuSpeedQuietLogLevel
		}
	}
	return out
}

// ── Settings → Emulation ─────────────────────────────────────────────────

// emuSpeedScopeIsDolphin reports the page scoped to GameCube or Wii, whose
// Speed row is GameCube speed's.
func emuSpeedScopeIsDolphin() bool {
	return raTuneScope != "" && dolphinIsSystem(raTuneScope)
}

// emuSpeedAutoNote says what Automatic does on this chip.
func emuSpeedAutoNote() string {
	if emuSpeedSoC() == socA133P {
		return "Fastest on PSP, Dreamcast, Saturn and PS2; Fast on PlayStation, N64, DS, arcade and CD consoles; 8/16-bit consoles as they are"
	}
	return "Fast on PlayStation, N64, DS, PSP, Dreamcast, Saturn, PS2, arcade and CD consoles; 8/16-bit consoles as they are"
}

// emuSpeedLevelOptions are the three levels' stops.
func emuSpeedLevelOptions() []setOption {
	return []setOption{
		{Value: emuSpeedFastest, Label: "Fastest", Note: "Fast, plus lighter effects and detail: PSP, Dreamcast, PlayStation, DS, N64 and PS2 trade some looks or accuracy for frames"},
		{Value: emuSpeedFast, Label: "Fast", Note: "Frames skipped only when the handheld falls behind, threaded renderers on, no run-ahead or rewind on the bigger consoles"},
		{Value: emuSpeedOwn, Label: "Emulators' own", Note: "Only what you set in RetroArch's Core Options"},
	}
}

// emuSpeedConsoleOptions are one console's stops: Default (what All consoles
// gives it on this chip), then the levels.
func emuSpeedConsoleOptions(c *Config, system string) []setOption {
	sys, _ := raTuneSystemFor(system)
	eff := emuSpeedSetting(c)
	if eff == emuSpeedAuto {
		eff = emuSpeedAutoLevel(sys.Tier, emuSpeedSoC())
	}
	return append([]setOption{{Value: "", Label: "Default · " + emuSpeedLabel(eff), Note: raTuneDefaultNote}}, emuSpeedLevelOptions()...)
}

func emuSpeedOptions(c *Config) []setOption {
	switch {
	case emuSpeedScopeIsDolphin():
		return dolphinSpeedSettingsRow().Options(c)
	case raTuneScope != "":
		return emuSpeedConsoleOptions(c, raTuneScope)
	}
	return append([]setOption{{Value: emuSpeedAuto, Label: "Auto", Note: emuSpeedAutoNote()}}, emuSpeedLevelOptions()...)
}

// emuSpeedStored is the scope's stored choice ("" for a console that follows
// All consoles).
func emuSpeedStored(c *Config) string {
	switch {
	case emuSpeedScopeIsDolphin():
		return dolphinSpeedFrom(c)
	case raTuneScope == "":
		return emuSpeedSetting(c)
	}
	return emuSpeedConsole(c, raTuneScope)
}

// emuSpeedDetail is the row's second line. Cheap: drawn every frame.
func emuSpeedDetail(c *Config) string {
	switch {
	case emuSpeedScopeIsDolphin():
		return dolphinSpeedDetail(c) + " · the same setting as GameCube speed"
	case raTuneScope == "":
		return "What the emulators trade for frames · Auto goes by console and chip"
	}
	switch emuSpeedLevelFor(c, raTuneScope) {
	case emuSpeedFastest:
		return raTuneScope + " games only · Fastest: speed before looks"
	case emuSpeedFast:
		return raTuneScope + " games only · Fast: frames skipped only when it falls behind"
	}
	return raTuneScope + " games only · the emulators' own settings"
}

// emuSpeedSettingsRow is the Emulation page's Speed row (ratuning_settings.go),
// scoped like Audio quality.
func emuSpeedSettingsRow() setRow {
	return setRow{
		ID: "raTuneSpeed", Label: "Speed", Kind: setKindChoice,
		Visible: func(*Config) bool {
			return emuSpeedSupported() && (!emuSpeedScopeIsDolphin() || dolphinSoC() != "")
		},
		Detail:  emuSpeedDetail,
		Options: emuSpeedOptions,
		Current: func(c *Config) int { return raTuneOptionIndex(emuSpeedOptions(c), emuSpeedStored(c)) },
		Choose: func(c *Config, value string) {
			if emuSpeedScopeIsDolphin() {
				dolphinSpeedSettingsRow().Choose(c, value)
				return
			}
			value = normaliseEmuSpeed(value)
			if raTuneScope != "" && value == emuSpeedAuto {
				value = ""
			}
			if raTuneScope == "" && value == "" {
				value = emuSpeedAuto
			}
			setStoreString(c, raTuneScopedKey(emuSpeedKey, "speed"), value)
			label := emuSpeedLabel(value)
			if value == "" {
				label = "Default"
			}
			raTuneScopedToast("Speed", label+" · from the next game")
		},
	}
}

// ps2SpeedSettingsRow is Settings → Games & emulators → "PS2 speed", under
// "PS2 emulator": the PlayStation 2's own Speed (the Emulation page's row with
// PlayStation 2 picked, the same key), beside the switches it belongs with,
// as GameCube speed sits under GameCube & Wii.
func ps2SpeedSettingsRow() setRow {
	options := func(c *Config) []setOption { return emuSpeedConsoleOptions(c, ps2SystemName) }
	return setRow{
		ID: "ps2Speed", Label: "PS2 speed", Kind: setKindChoice,
		Visible: func(c *Config) bool { return ps2SoC() != "" && ps2Enabled(c) },
		Detail: func(c *Config) string {
			switch emuSpeedLevelFor(c, ps2SystemName) {
			case emuSpeedFastest:
				return "Fastest · the emulated CPU at 75% with mild cycle skipping: busy scenes slow down inside the game"
			case emuSpeedFast:
				return "Fast · no run-ahead or rewind; the PS2's own timing"
			}
			return "ARMSX2's own settings · JukaHub changes none"
		},
		Options: options,
		Current: func(c *Config) int { return raTuneOptionIndex(options(c), emuSpeedConsole(c, ps2SystemName)) },
		Choose: func(c *Config, value string) {
			value = normaliseEmuSpeed(value)
			if value == emuSpeedAuto {
				value = ""
			}
			setStoreString(c, raTuneConsoleKey(ps2SystemName, "speed"), value)
			setToast("PS2 speed: " + emuSpeedLabel(emuSpeedLevelFor(c, ps2SystemName)) + " · from the next game")
		},
	}
}
