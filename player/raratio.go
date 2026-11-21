package main

// raratio.go — the picture's shape (aspect ratio) and hard GPU sync of
// RetroArch games started from the library, per console and per game
// written into retroarch.cfg for one launch.
//
// The choices: Core provided, 4:3, 16:9, 3:2, 10:9, 8:7, Square pixels and
// Full screen (stretched), plus RetroArch's own (the default: nothing is
// written). Each console has its own (Settings → Emulation, under "Adjust
// for", raratio_ui.go), and a game can override its console in the game
// options sheet. Stored in data/game-ratio.json (rahotkeys_store.go).
//
// RetroArch picks the shape by aspect_ratio_index, a place in its enum
// aspect_ratio (gfx/video_driver.h), and the enum has moved: fixed ratios
// were inserted over the years (21:9 in 1.7.4), so Config, Square pixel, Core
// provided and Custom moved from 19…22 to 20…23, and Full (24) came later
// still, at the end. RetroArch 1.9 and newer (1.22 included) use 0 4:3,
// 1 16:9, 7 3:2, 15 8:7, 20 Config (video_aspect_ratio, a float), 21 Square
// pixel, 22 Core provided, 23 Custom — the table Batocera's
// configgen writes today (libretroConfig.py ratioIndexes). The build is not
// asked (no cheap version query before a game runs); retroarch.cfg says it,
// because RetroArch saves every setting it knows, so a key that only 1.9 and
// newer write (global_core_options, per-core options) marks the modern table. Without one, only 4:3 (0) and 16:9 (1) are written
// — the two places that never moved — and the rows offer only those.
//
// The routes, chosen to need as little of the table as possible:
//
//	Core provided   aspect_ratio_index 22, video_aspect_ratio_auto true
//	4:3, 16:9       aspect_ratio_index 0, 1
//	3:2, 8:7        aspect_ratio_index 7, 15
//	Square pixels   aspect_ratio_index 21
//	10:9            aspect_ratio_index 20 (Config), video_aspect_ratio 1.111111
//	                — no build has a 10:9 stop
//	Full screen     aspect_ratio_index 20 (Config), video_aspect_ratio the
//	                screen's own width / height — Full (24) only exists in
//	                newer builds, and an index past the end of an older
//	                build's table is not something to write blind
//
// The picture filter's Pixel perfect (videofilter.go) keeps
// video_scale_integer: RetroArch then scales by whole pixels at the chosen
// shape, so Full screen may not fill. Shader looks and overlays keep their
// keys. A widescreen hack (widescreen*.go) that sets 16:9 for its launch runs
// after this hook and wins, as it should.
//
// Hard GPU sync (video_hard_sync, video_hard_sync_frames 0) trades a little
// CPU for less input lag on the gl video driver; per console, RetroArch's own
// by default. V-sync itself is Smooth motion's (ratuning.go), for every
// console.
//
// Everything goes through raScopeApply (racfgscope.go): put back when
// RetroArch exits. Standalone emulators and NextUI paks are left alone.

import (
	"os"
	"strconv"
	"strings"
)

const (
	raRatioRecordName = "game-ratio.json"
	raRatioField      = "ratio"
	raHardSyncField   = "hardSync"

	raRatioOwn    = "own"
	raRatioCore   = "core"
	raRatioSquare = "square"
	raRatioFull   = "full"

	// The modern table's places (see the header).
	raRatioIdxConfig = "20"
	raRatioIdxSquare = "21"
	raRatioIdxCore   = "22"
)

// raRatioChoice is one shape: its stored value, words, and the index of a
// fixed stop ("" for the routes that need more than an index).
type raRatioChoice struct {
	ID    string
	Label string
	Note  string
	Index string
	// Safe marks a stop whose index never moved: written whatever the
	// RetroArch build.
	Safe bool
}

// raRatioChoices are the shapes in menu order.
var raRatioChoices = []raRatioChoice{
	{raRatioCore, "Core provided", "The shape the emulator asks for; right for most games", raRatioIdxCore, false},
	{"4:3", "4:3", "A TV's shape: NES, SNES, Mega Drive, PlayStation", "0", true},
	{"16:9", "16:9", "Widescreen, for widescreen hacks; other games stretch", "1", true},
	{"3:2", "3:2", "The Game Boy Advance's screen", "7", false},
	{"10:9", "10:9", "The Game Boy and Game Gear's screen", "", false},
	{"8:7", "8:7", "NES and SNES as the console drew them, without a TV's stretch", "15", false},
	{raRatioSquare, "Square pixels", "Every pixel square, at the game's own resolution", raRatioIdxSquare, false},
	{raRatioFull, "Full screen", "Stretched to fill the whole screen", "", false},
}

func raRatioChoiceByID(id string) (raRatioChoice, bool) {
	for _, c := range raRatioChoices {
		if c.ID == id {
			return c, true
		}
	}
	return raRatioChoice{}, false
}

// raRatioLabel names a stored shape.
func raRatioLabel(v string) string {
	if c, ok := raRatioChoiceByID(v); ok {
		return c.Label
	}
	return "RetroArch's own"
}

// raHardSyncLabel names a stored hard sync choice.
func raHardSyncLabel(v string) string {
	switch v {
	case "on":
		return "On"
	case "off":
		return "Off"
	}
	return "RetroArch's own"
}

// normaliseRARatio keeps a stored value this build knows ("" drops it).
func normaliseRARatio(field, value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch field {
	case raRatioField:
		if value == raRatioOwn {
			return value
		}
		if _, ok := raRatioChoiceByID(value); ok {
			return value
		}
	case raHardSyncField:
		switch value {
		case "on", "off", raRatioOwn:
			return value
		}
	}
	return ""
}

// raRatioStore is data/game-ratio.json.
var raRatioStore = &raLevelStore{name: raRatioRecordName, tag: "ratio", norm: normaliseRARatio}

// raRatioModern reports whether a retroarch.cfg was saved by RetroArch 1.9
// or newer, whose aspect_ratio_index table JukaHub knows. Pure. Every build
// since saves global_core_options (1.21 and 1.22 too). The 1.21 turbo keys
// are no sign: Turbo (racontrols_turbo.go) writes them into a never-saved
// config earlier in the same launch.
func raRatioModern(cfg string) bool {
	_, ok := raConfigGet(cfg, "global_core_options")
	return ok
}

// raRatioOldNote is what an older (or never saved) RetroArch config gets.
const raRatioOldNote = "this RetroArch is older than 1.9 or has not saved its settings yet: only 4:3 and 16:9 can be set"

// raRatioValues are the keys a shape writes, or why it cannot be written.
// panelW and panelH are the screen's size, for Full screen. Pure.
func raRatioValues(choice string, modern bool, panelW, panelH int) ([]raScopeValue, string) {
	c, ok := raRatioChoiceByID(choice)
	if !ok {
		return nil, ""
	}
	if !c.Safe && !modern {
		return nil, raRatioOldNote
	}
	switch c.ID {
	case raRatioCore:
		return []raScopeValue{
			{Key: "aspect_ratio_index", Value: raRatioIdxCore},
			{Key: "video_aspect_ratio_auto", Value: "true"},
		}, ""
	case "10:9":
		return []raScopeValue{
			{Key: "aspect_ratio_index", Value: raRatioIdxConfig},
			{Key: "video_aspect_ratio", Value: "1.111111"},
		}, ""
	case raRatioFull:
		if panelW <= 0 || panelH <= 0 {
			return nil, "the screen's size is unknown"
		}
		return []raScopeValue{
			{Key: "aspect_ratio_index", Value: raRatioIdxConfig},
			{Key: "video_aspect_ratio", Value: strconv.FormatFloat(float64(panelW)/float64(panelH), 'f', 6, 64)},
		}, ""
	}
	return []raScopeValue{{Key: "aspect_ratio_index", Value: c.Index}}, ""
}

// raHardSyncValues are the keys a hard sync choice writes. Pure.
func raHardSyncValues(v string) []raScopeValue {
	switch v {
	case "on":
		return []raScopeValue{{Key: "video_hard_sync", Value: "true"}, {Key: "video_hard_sync_frames", Value: "0"}}
	case "off":
		return []raScopeValue{{Key: "video_hard_sync", Value: "false"}}
	}
	return nil
}

// raRatioCandidates is a variable so tests could point at a temporary
// config.
var raRatioCandidates = retroArchConfigCandidates

// raRatioPrepare is the launch hook: the game's shape and its console's hard
// sync, for this launch only. It never blocks or fails a launch. Main thread
// (the screen size is asked of SDL).
func raRatioPrepare(launcher string, game libraryGame) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("ratio", "preparing the game's aspect ratio failed: %v", r)
		}
	}()
	if !retroArchLauncher(launcher) || nextuiIsPakLauncher(launcher) {
		return
	}
	console := gameControlsConsole(game.System)
	ratio, _ := raRatioStore.resolve(console, game.Path, raRatioField)
	hard, _ := raRatioStore.resolve(console, "", raHardSyncField)
	syncValues := raHardSyncValues(hard)
	if (ratio == "" || ratio == raRatioOwn) && len(syncValues) == 0 {
		raRatioStore.setNote(game.Path, "")
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, raRatioCandidates()))
	if len(targets) == 0 {
		logInfo("ratio", "no retroarch.cfg found for %s; launching with RetroArch's own picture shape", launcher)
		raRatioStore.setNote(game.Path, "Not applied last time: JukaHub found no RetroArch settings file")
		return
	}
	w, h := 0, 0
	if ratio == raRatioFull {
		w, h = lightgunPanelSize()
	}
	note := ""
	for _, cfgPath := range targets {
		raw, err := os.ReadFile(cfgPath)
		if err != nil {
			continue
		}
		values, why := raRatioValues(ratio, raRatioModern(string(raw)), w, h)
		if why != "" {
			logInfo("ratio", "%s: aspect ratio %s not set: %s", cfgPath, ratio, why)
			note = "Not applied last time: " + why
		}
		values = append(values, syncValues...)
		if len(values) == 0 {
			continue
		}
		if _, err := raScopeApply(cfgPath, values); err != nil {
			logWarn("ratio", "cannot set the aspect ratio in %s: %v", cfgPath, err)
			note = "Not applied last time: the RetroArch settings file could not be written"
			continue
		}
		Log().Info("aspect ratio applied for this launch", "config", cfgPath, "system", game.System, "ratio", ratio, "hardSync", hard)
	}
	raRatioStore.setNote(game.Path, note)
}

func init() {
	gameLaunchRegisterPrepare(raRatioPrepare)
}
