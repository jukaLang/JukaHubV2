package main

// widescreen.go — Widescreen (16:9): 3D games drawn across the whole wide
// screen instead of a 4:3 picture with bars at the sides.
//
// Several libretro cores have a widescreen hack — they draw what the game's camera
// sees beyond the 4:3 edges — and the Smart Pro's 1280x720 panel is 16:9, so
// the bars can go. Hacks can glitch (objects popping in at the edges, menus
// and 2D stretched), so it is Off unless chosen.
//
// The choice. Settings → Emulation → Adjust for <console> has "Widescreen
// (16:9)", Off or On, for the consoles a hack exists for: Nintendo 64,
// PlayStation, Dreamcast and Mega Drive (Saturn's cores have none). A game
// can choose its own in its options sheet: Default, On or Off. All of it is
// hidden on a 4:3 screen (the Brick): the test is the display's shape, not
// the model, so a Brick Pro on a TV gets it and a Smart Pro does too.
//
// The launch (widescreenPrepare, a launch hook from gamelaunch_hooks.go) sets,
// for that start only and through raScopeApply (racfgscope.go), the core's
// own options in the options file RetroArch reads for the game
// (raTuneCoreOptionFile, ratuning.go) and RetroArch's picture shape:
//
//	Mupen64Plus-Next  mupen64plus-aspect = "16:9 adjusted" (GLideN64 only: a
//	                  game set to another renderer is skipped)
//	ParaLLEl N64      parallel-n64-aspectratiohint = "widescreen"
//	Flycast           flycast_widescreen_hack = "enabled", and the older
//	                  reicast_widescreen_hack for builds before the rename
//	SwanStation       swanstation_GPU_WidescreenHack = "true",
//	                  swanstation_Display_AspectRatio = "16:9"
//	Beetle PSX HW     beetle_psx_hw_widescreen_hack = "enabled",
//	                  beetle_psx_hw_widescreen_hack_aspect_ratio = "16:9"
//	Beetle PSX        the same without "_hw"
//	Genesis Plus GX   genesis_plus_gx_wide_h40_extra_columns = "10" (the
//	  Wide            16:9 width for the usual 320-pixel mode)
//	retroarch.cfg     aspect_ratio_index = "1" (16:9 in RetroArch's aspect
//	                  table, gfx/video_driver.c: 0 is 4:3, 1 is 16:9) and
//	                  video_aspect_ratio_auto = "false", which would
//	                  otherwise put the core's own shape back
//
// PCSX-ReARMed, the PlayStation core TrimUI ships, has no widescreen mode;
// the settings say to pick SwanStation or Beetle PSX in the game's Emulator
// row. Plain Genesis Plus GX and PicoDrive are 4:3 cores. A core not in the
// table changes nothing and the log says so. The picture shape is only set
// when the core's options were: a 4:3 picture stretched to 16:9 is worse than
// bars.
//
// Order with the aspect-ratio settings (raratio*.go, another session's work
// on 2026-11-20): this hook runs after the built-in launch settings and after
// hooks registered from files sorting before this one, so for a game played
// widescreen its 16:9 wins for that launch; raratio can ask widescreenOnFor
// to step aside instead. Everything goes back when RetroArch exits.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	widescreenOn  = "on"
	widescreenOff = "off"

	// raTuning.<console>.widescreen holds "on" or "off"; unset is off.
	widescreenWhat = "widescreen"

	// widescreenAspectIndex is 16:9 in RetroArch's aspect_ratio_lut.
	widescreenAspectIndex = "1"
)

// widescreenConsoles are the consoles a hack exists for, in Settings order.
var widescreenConsoles = []string{"Nintendo 64", "PlayStation", "Dreamcast", "Mega Drive"}

// widescreenGameLadder is the sheet row's steps: Default, On, Off.
var widescreenGameLadder = []string{"", widescreenOn, widescreenOff}

// widescreenCoreFolders are the option folders (library_name) of the cores
// below that ratuning.go's table does not list yet; added in init().
var widescreenCoreFolders = map[string]string{
	"mupen64plus_next":       "Mupen64Plus-Next",
	"mupen64plus_next_gles2": "Mupen64Plus-Next",
	"mupen64plus_next_gles3": "Mupen64Plus-Next",
	"parallel_n64":           "ParaLLEl N64",
	"flycast":                "Flycast",
	"swanstation":            "SwanStation",
	"mednafen_psx_hw":        "Beetle PSX HW",
	"beetle_psx_hw":          "Beetle PSX HW",
	"mednafen_psx":           "Beetle PSX",
	"beetle_psx":             "Beetle PSX",
	"genesis_plus_gx_wide":   "Genesis Plus GX Wide",
}

func normaliseWidescreenGame(v string) string {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case widescreenOn, widescreenOff:
		return v
	}
	return ""
}

// widescreenConsoleFor is the console of a library system when a hack exists
// for it, "" otherwise.
func widescreenConsoleFor(system string) string {
	name := canonicalCompatSystem(system)
	for _, c := range widescreenConsoles {
		if strings.EqualFold(c, name) {
			return c
		}
	}
	return ""
}

// widescreenConsoleOn is one console's setting; off unless turned on.
func widescreenConsoleOn(c *Config, console string) bool {
	return c != nil && customBool(c.Variables.Custom, raTuneConsoleKey(console, widescreenWhat), false)
}

// widescreenWanted is a game's choice: its own, else its console's. It does
// not look at the screen or the core.
func widescreenWanted(c *Config, game libraryGame) bool {
	console := widescreenConsoleFor(game.System)
	if console == "" {
		return false
	}
	switch emuExtrasFor(game.Path).Widescreen {
	case widescreenOn:
		return true
	case widescreenOff:
		return false
	}
	return widescreenConsoleOn(c, console)
}

// widescreenCoreOptions are a core's widescreen options, nil for a core
// without one JukaHub knows. Pure.
func widescreenCoreOptions(core string) []raScopeValue {
	core = normalizeCoreName(core)
	switch {
	case strings.HasPrefix(core, "mupen64plus_next"):
		return []raScopeValue{{Key: "mupen64plus-aspect", Value: "16:9 adjusted"}}
	case core == "parallel_n64":
		return []raScopeValue{{Key: "parallel-n64-aspectratiohint", Value: "widescreen"}}
	case core == "flycast":
		return []raScopeValue{
			{Key: "flycast_widescreen_hack", Value: "enabled"},
			{Key: "reicast_widescreen_hack", Value: "enabled"},
		}
	case core == "swanstation":
		return []raScopeValue{
			{Key: "swanstation_GPU_WidescreenHack", Value: "true"},
			{Key: "swanstation_Display_AspectRatio", Value: "16:9"},
		}
	case core == "mednafen_psx_hw" || core == "beetle_psx_hw":
		return []raScopeValue{
			{Key: "beetle_psx_hw_widescreen_hack", Value: "enabled"},
			{Key: "beetle_psx_hw_widescreen_hack_aspect_ratio", Value: "16:9"},
		}
	case core == "mednafen_psx" || core == "beetle_psx":
		return []raScopeValue{
			{Key: "beetle_psx_widescreen_hack", Value: "enabled"},
			{Key: "beetle_psx_widescreen_hack_aspect_ratio", Value: "16:9"},
		}
	case core == "genesis_plus_gx_wide":
		return []raScopeValue{{Key: "genesis_plus_gx_wide_h40_extra_columns", Value: "10"}}
	}
	return nil
}

// widescreenAspectValues are the retroarch.cfg values of a widescreen launch.
func widescreenAspectValues() []raScopeValue {
	return []raScopeValue{
		{Key: "aspect_ratio_index", Value: widescreenAspectIndex},
		{Key: "video_aspect_ratio_auto", Value: "false"},
	}
}

// widescreenConsoleCores says which cores draw a console wide, for the rows.
func widescreenConsoleCores(console string) string {
	switch console {
	case "Nintendo 64":
		return "Mupen64Plus-Next (GLideN64) or ParaLLEl N64"
	case "PlayStation":
		return "SwanStation or Beetle PSX (not PCSX-ReARMed)"
	case "Dreamcast":
		return "Flycast"
	case "Mega Drive":
		return "Genesis Plus GX Wide"
	}
	return ""
}

// ── The screen ───────────────────────────────────────────────────────────

// widescreenScreenWide reports a screen wide enough for 16:9 to help: 16:10
// or wider. It asks SDL for the display RetroArch will open on (a TV when the
// firmware made it the screen), else the model's panel. Main thread.
func widescreenScreenWide() bool {
	w, h := 0, 0
	if mode, err := sdl.GetCurrentDisplayMode(0); err == nil && mode.W > 0 && mode.H > 0 {
		w, h = int(mode.W), int(mode.H)
	} else if hw := deviceHardwareCurrent(); hw.PanelW > 0 && hw.PanelH > 0 {
		w, h = int(hw.PanelW), int(hw.PanelH)
	} else {
		return true // a desktop without SDL up yet: no reason to hide it
	}
	if h > w {
		w, h = h, w // a panel mounted on its side
	}
	return w*10 >= h*16
}

// widescreenOnFor reports whether a launch of game plays widescreen: chosen,
// on a wide screen, in a RetroArch core with a hack. For the aspect-ratio
// settings, which may step aside for it. Main thread.
func widescreenOnFor(launcher string, game libraryGame) bool {
	if appConfig == nil || !widescreenWanted(appConfig, game) || !widescreenScreenWide() {
		return false
	}
	if !retroArchLauncher(launcher) || nextuiIsPakLauncher(launcher) {
		return false
	}
	return len(widescreenCoreOptions(gameCoreFromLauncher(launcher, game.System))) > 0
}

// ── Launch ───────────────────────────────────────────────────────────────

// widescreenCandidates is a variable so tests could point at a temporary
// config.
var widescreenCandidates = retroArchConfigCandidates

// widescreenPrepare is the launch hook. It never blocks or fails a launch:
// anything it cannot set is logged and the game starts in 4:3. Main thread.
func widescreenPrepare(launcher string, game libraryGame) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("emulation", "preparing widescreen failed: %v", r)
		}
	}()
	if appConfig == nil || !widescreenWanted(appConfig, game) {
		return
	}
	name := filepath.Base(game.Path)
	if !widescreenScreenWide() {
		logInfo("emulation", "widescreen: %s stays 4:3, the screen is not wide", name)
		return
	}
	if !retroArchLauncher(launcher) || nextuiIsPakLauncher(launcher) {
		logInfo("emulation", "widescreen: %s's emulator is not RetroArch; nothing set", name)
		return
	}
	core := gameCoreFromLauncher(launcher, game.System)
	options := widescreenCoreOptions(core)
	if len(options) == 0 {
		logInfo("emulation", "widescreen: %s has no 16:9 option JukaHub knows (%s games: %s)", core, widescreenConsoleFor(game.System), widescreenConsoleCores(widescreenConsoleFor(game.System)))
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, widescreenCandidates()))
	if len(targets) == 0 {
		logInfo("emulation", "widescreen: no retroarch.cfg found for %s", launcher)
		return
	}
	opt := raTuneCoreOptionFile(targets[0], core, game.Path)
	if opt == "" {
		logInfo("emulation", "widescreen: %s has no options file yet (start it once from RetroArch); %s stays 4:3", core, name)
		return
	}
	if strings.HasPrefix(normalizeCoreName(core), "mupen64plus_next") {
		if raw, err := os.ReadFile(opt); err == nil {
			if rdp, ok := raConfigGet(string(raw), "mupen64plus-rdp-plugin"); ok && !strings.EqualFold(strings.TrimSpace(rdp), "gliden64") {
				logInfo("emulation", "widescreen: %s uses the %s renderer; only GLideN64 draws 16:9, so it stays 4:3", name, rdp)
				return
			}
		}
	}
	if _, err := raScopeApply(opt, options); err != nil {
		logWarn("emulation", "widescreen: cannot set %s's options in %s: %v", core, opt, err)
		return
	}
	for _, cfgPath := range targets {
		if _, err := raScopeApply(cfgPath, widescreenAspectValues()); err != nil {
			logWarn("emulation", "widescreen: cannot set the 16:9 picture in %s: %v", cfgPath, err)
		}
	}
	keys := make([]string, 0, len(options))
	for _, v := range options {
		keys = append(keys, v.Key)
	}
	Log().Info("widescreen 16:9 for this launch", "game", name, "core", core, "options", strings.Join(keys, ", "))
}

func init() {
	for core, folder := range widescreenCoreFolders {
		if len(raTuneCoreFolders[core]) == 0 {
			raTuneCoreFolders[core] = []string{folder}
		}
	}
	gameLaunchRegisterPrepare(widescreenPrepare)
}
