package main

// n64light.go — Settings → Emulation → N64 graphics: a lighter picture for
// Nintendo 64 games where the GPU is small (the Rice renderer on the Brick).
//
// The choices: Auto (the default), Full and Light. Auto is Light on the Brick
// family — Brick, Brick Pro and Hammer: the A133P's PowerVR GE8300 driving a
// 1024x768 panel — and wherever the N64's Speed is Fastest (Settings →
// Emulation, emuspeed.go), Full everywhere else. Full writes nothing. Light sets,
// for one launch, in the options file RetroArch reads for the game
// (raTuneCoreOptionFile, ratuning.go) and through raScopeApply
// (racfgscope.go), so it all goes back when RetroArch exits:
//
//	ParaLLEl N64      parallel-n64-gfxplugin = "rice" (light enough for the
//	                  Brick) and parallel-n64-gfxplugin-accuracy
//	                  = "low"
//	Mupen64Plus-Next  mupen64plus-43screensize = "320x240" (half the default
//	                  640x480; the value the compatibility boost writes,
//	                  compat.go), mupen64plus-EnableFBEmulation = "False" and
//	                  mupen64plus-EnableCopyColorToRDRAM = "Off" (GLideN64's
//	                  frame-buffer work, the costliest part on that GPU; some
//	                  effects — a pause-screen backdrop, a camera — go with it)
//
// A choice someone made wins: a key is written only while the file holds the
// core's own default for it (or already the Light value). A game's own .opt
// from RetroArch's "Save Game Options", the compatibility boost's per-game
// file or a renderer picked in RetroArch's menu all keep their values, and a
// key the file does not have (a core build without it) is not added.
// Mupen64Plus-Next set to another renderer than GLideN64 (Angrylion, ParaLLEl
// RDP) is left alone: those options are GLideN64's.
//
// Widescreen (widescreen.go) wins: a launch it plays 16:9 keeps Full, so the
// 16:9 hack is never drawn by a renderer that ignores it. Standalone emulators
// (Mupen64Plus standalone) and NextUI paks are not touched; a core that has
// never run has no options file yet and is skipped (logged), as everywhere
// else in the Emulation settings.

import (
	"os"
	"strings"
)

const (
	// n64LightKey stores the choice: "auto" (unset), "full" or "light".
	n64LightKey = "raTuningN64Graphics"

	n64LightAuto  = "auto"
	n64LightFull  = "full"
	n64LightLight = "light"
)

// n64LightOpt is one core option Light sets: the value, and the core's own
// defaults (more than one where builds differ) — any other value was chosen.
type n64LightOpt struct {
	Key      string
	Light    string
	Defaults []string
}

// n64LightCoreFolders are the option folders of the two N64 cores, in case
// widescreen.go (which adds them too) ever stops listing them.
var n64LightCoreFolders = map[string]string{
	"mupen64plus_next":       "Mupen64Plus-Next",
	"mupen64plus_next_gles2": "Mupen64Plus-Next",
	"mupen64plus_next_gles3": "Mupen64Plus-Next",
	"parallel_n64":           "ParaLLEl N64",
}

func normaliseN64Light(v string) string {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case n64LightAuto, n64LightFull, n64LightLight:
		return v
	}
	return ""
}

// n64LightSetting is the stored choice; unset is Auto.
func n64LightSetting(c *Config) string {
	if v := normaliseN64Light(raTuneRaw(c, n64LightKey)); v != "" {
		return v
	}
	return n64LightAuto
}

// n64LightAutoLight reports whether Auto means Light on this device: the
// Brick family.
func n64LightAutoLight() bool {
	return deviceFamily(DetectDevice()) == DeviceTrimuiBrick
}

// n64LightOn reports whether launches use Light: Auto is Light on the Brick
// family, and wherever the N64's Speed is Fastest (emuspeed.go).
func n64LightOn(c *Config) bool {
	switch n64LightSetting(c) {
	case n64LightLight:
		return true
	case n64LightFull:
		return false
	}
	return n64LightAutoLight() || n64LightSpeedFastest(c)
}

// n64LightSpeedFastest reports the N64's Speed at Fastest (emuspeed.go).
func n64LightSpeedFastest(c *Config) bool {
	return emuSpeedLevelFor(c, "Nintendo 64") == emuSpeedFastest
}

// n64LightTable is a core's Light options, nil for a core that is not an N64
// one JukaHub knows. Pure.
func n64LightTable(core string) []n64LightOpt {
	core = normalizeCoreName(core)
	switch {
	case core == "parallel_n64":
		return []n64LightOpt{
			{"parallel-n64-gfxplugin", "rice", []string{"auto"}},
			// "veryhigh" on Linux, "medium" on the mobile builds.
			{"parallel-n64-gfxplugin-accuracy", "low", []string{"veryhigh", "medium"}},
		}
	case strings.HasPrefix(core, "mupen64plus_next"):
		return []n64LightOpt{
			{"mupen64plus-43screensize", "320x240", []string{"640x480"}},
			{"mupen64plus-EnableFBEmulation", "False", []string{"True"}},
			{"mupen64plus-EnableCopyColorToRDRAM", "Off", []string{"Async", "TripleBuffer"}},
		}
	}
	return nil
}

// n64LightValues are the Light values an options file takes: keys it has at
// the core's default (or at the Light value already). kept names the keys
// left at someone's own value, for the log. Pure.
func n64LightValues(opt string, table []n64LightOpt) (values []raScopeValue, kept []string) {
	for _, o := range table {
		cur, had := raConfigGet(opt, o.Key)
		if !had {
			continue
		}
		own := !raValueEqual(cur, o.Light)
		for _, d := range o.Defaults {
			if strings.EqualFold(strings.TrimSpace(cur), d) {
				own = false
			}
		}
		if own {
			kept = append(kept, o.Key+" = "+strings.TrimSpace(cur))
			continue
		}
		values = append(values, raScopeValue{Key: o.Key, Value: o.Light})
	}
	return values, kept
}

// ── Launch ───────────────────────────────────────────────────────────────

// n64LightPrepare is the launch hook. It never blocks or fails a launch: the
// game starts with the core's own settings whatever cannot be set. Main
// thread (widescreenOnFor asks SDL for the screen).
func n64LightPrepare(launcher string, game libraryGame) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("emulation", "preparing light N64 graphics failed: %v", r)
		}
	}()
	if appConfig == nil || !n64LightOn(appConfig) || !retroArchLauncher(launcher) || nextuiIsPakLauncher(launcher) {
		return
	}
	core := gameCoreFromLauncher(launcher, game.System)
	table := n64LightTable(core)
	if len(table) == 0 {
		return
	}
	if widescreenOnFor(launcher, game) {
		logInfo("emulation", "N64 graphics: %s plays widescreen; full graphics for this launch", game.Title)
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		return
	}
	opt := raTuneCoreOptionFile(targets[0], core, game.Path)
	if opt == "" {
		logInfo("emulation", "N64 graphics: %s has no options file yet (start it once from RetroArch); full graphics for this launch", core)
		return
	}
	raw, err := os.ReadFile(opt)
	if err != nil {
		logWarn("emulation", "N64 graphics: cannot read %s: %v", opt, err)
		return
	}
	text := string(raw)
	if strings.HasPrefix(normalizeCoreName(core), "mupen64plus_next") {
		if rdp, ok := raConfigGet(text, "mupen64plus-rdp-plugin"); ok && !strings.EqualFold(strings.TrimSpace(rdp), "gliden64") {
			logInfo("emulation", "N64 graphics: %s uses the %s renderer; its own settings stay", core, rdp)
			return
		}
	}
	values, kept := n64LightValues(text, table)
	if len(kept) > 0 {
		logInfo("emulation", "N64 graphics: kept your own %s", strings.Join(kept, ", "))
	}
	if len(values) == 0 {
		return
	}
	if _, err := raScopeApply(opt, values); err != nil {
		logWarn("emulation", "N64 graphics: cannot set %s's options in %s: %v", core, opt, err)
		return
	}
	keys := make([]string, 0, len(values))
	for _, v := range values {
		keys = append(keys, v.Key+" = "+v.Value)
	}
	Log().Info("light N64 graphics for this launch", "game", game.Title, "core", core, "options", strings.Join(keys, ", "))
}

// ── Settings → Emulation ─────────────────────────────────────────────────

func n64LightLabel(v string) string {
	switch v {
	case n64LightFull:
		return "Full"
	case n64LightLight:
		return "Light"
	}
	return "Auto"
}

func n64LightOptions(c *Config) []setOption {
	auto := "Auto · Full"
	if n64LightAutoLight() || n64LightSpeedFastest(c) {
		auto = "Auto · Light"
	}
	return []setOption{
		{Value: n64LightAuto, Label: auto, Note: "Light on the Brick and Brick Pro, and wherever N64 Speed is Fastest; Full on the Smart Pro"},
		{Value: n64LightFull, Label: "Full", Note: "The emulator's own graphics settings"},
		{Value: n64LightLight, Label: "Light", Note: "Faster: the Rice renderer, or half resolution without frame-buffer effects; a few effects go missing"},
	}
}

// n64LightSettingsRows is the Emulation page's row (ratuning_settings.go):
// one choice for every N64 game, shown with All consoles or Nintendo 64 picked
// in "Adjust for".
func n64LightSettingsRows() []setRow {
	return []setRow{{
		ID: "n64Graphics", Label: "N64 graphics", Kind: setKindChoice,
		Detail: setStatic("ParaLLEl N64 and Mupen64Plus-Next · a setting you saved in RetroArch for a game stays"),
		Visible: func(*Config) bool {
			return raTuneScope == "" || strings.EqualFold(raTuneScope, "Nintendo 64")
		},
		Options: n64LightOptions,
		Current: func(c *Config) int { return raTuneOptionIndex(n64LightOptions(c), n64LightSetting(c)) },
		Choose: func(c *Config, value string) {
			value = normaliseN64Light(value)
			if value == "" {
				value = n64LightAuto
			}
			setStoreString(c, n64LightKey, value)
			setToast("N64 graphics: " + n64LightLabel(value))
		},
	}}
}

func init() {
	for core, folder := range n64LightCoreFolders {
		if len(raTuneCoreFolders[core]) == 0 {
			raTuneCoreFolders[core] = []string{folder}
		}
	}
	gameLaunchRegisterPrepare(n64LightPrepare)
}
