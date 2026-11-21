package main

// ratuning.go — how RetroArch plays a game started from the library: the
// sound, the delay between a press and the picture, smooth motion, an
// overlay, rumble strength, arcade rotation and PAL timing.
//
// Players on these handhelds ask for: high-quality audio resampling with a quality/performance choice per
// emulator, about one frame of latency, no tearing or sync stutter, overlays,
// variable rumble, FBNeo screen rotation and a PAL mode. RetroArch can do all
// of them already; they are buried in its menus, and some are core options
// rather than RetroArch settings. Settings → Emulation (ratuning_settings.go)
// puts them in one place, and prepareRATuning writes them for one launch.
//
// Like the picture filter (videofilter.go) and the OSD preset (osdpreset.go),
// nothing is written for good: every key goes through raScopeApply
// (racfgscope.go), journalled and put back when RetroArch exits, and a value
// the user changes in RetroArch's own menu during the game wins. RetroArch
// started from the firmware's menu keeps its own settings. Standalone
// emulators are left alone.
//
// What each setting writes (all RetroArch 1.9+ keys; older builds ignore a
// key they do not know):
//
//	Audio quality   audio_resampler sinc + audio_resampler_quality 4 (Best),
//	                sinc + 2 (Balanced), cc (Fast). RetroArch's quality levels
//	                are 1 lowest … 5 highest, 3 normal, and only the sinc
//	                resampler reads them. Auto picks by console: Best for the
//	                8/16-bit consoles and the handhelds, Balanced for the
//	                PlayStation, N64, DS and the CD/arcade systems, Fast for the
//	                PSP, Dreamcast and Saturn, which need every cycle the A133
//	                has.
//	Latency         Low: video_max_swapchain_images 2 (one frame less queued
//	                than the default 3), video_frame_delay_auto on, and
//	                audio_latency no higher than 64 ms. Lowest: audio 48 ms and,
//	                on the light consoles only, run-ahead of one frame
//	                (run_ahead_enabled/frames, without the second instance,
//	                which would double the memory). Auto is Low on the light
//	                consoles and RetroArch's own elsewhere: an A133 that drops
//	                frames on a PSP game gains nothing from a shorter queue.
//	                The two numbers are ceilings: a lower value the user set is
//	                kept.
//	Smooth motion   video_vsync on, vrr_runloop_enable off (no "sync to exact
//	                content framerate" on a fixed 60 Hz panel), audio_sync on
//	                and audio_rate_control on: the picture paces the game and
//	                dynamic rate control absorbs the small clock mismatch, which
//	                is what stops both tearing and the periodic stutter.
//	                video_threaded is left as it is: threaded video lowers
//	                frame drops on slow cores but adds a frame of latency and
//	                behaves differently per GLES driver, so the firmware's
//	                choice stays — except on the Smart Pro S, and when
//	                "Threaded video" says otherwise (ra_threaded.go).
//	Overlay         input_overlay_enable, input_overlay (a .cfg found under
//	                RetroArch's overlay folders), input_overlay_opacity 1 (an
//	                overlay on a handheld is a border, not a see-through touch
//	                pad) and input_overlay_hide_in_menu. "None" switches an
//	                overlay the user had off for the launch. The player's own
//	                overlays in Overlays/<Console> on the card are offered too,
//	                and a console at Default plays its folder's (useroverlays.go).
//	Rumble          input_rumble_gain, a percentage.
//	Arcade rotation video_allow_rotate on, so FBNeo and MAME can turn a vertical
//	                game upright; Sideways (TATE) also sets the core's vertical
//	                mode (fbneo-vertical-mode, mame2003-plus_tate_mode) so the
//	                game fills the screen with the handheld held on its side.
//	PAL mode        the region core option of the cores that have one
//	                (Genesis Plus GX, PicoDrive, FCEUmm, Nestopia, Snes9x,
//	                PCSX-ReARMed), forcing 50 Hz European timing.
//	Crop overscan   video_crop_overscan true (On) or false (Off): RetroArch
//	                asks the core to drop the border a TV hid, or to keep the
//	                whole picture. Unset leaves RetroArch's own value (true by
//	                default, config.def.h). It is a hint that only some cores
//	                follow; the cores' own overscan options are left alone:
//	                FCEUmm and Nestopia replaced their on/off switches with
//	                per-edge pixel counts, and their defaults already crop.
//
// The look (shader) is chosen on the same page, per console too, but written
// by videofilter.go (shader_looks.go decides which one).
//
// Core options live in another file than retroarch.cfg. RetroArch reads, in
// order: a game's own options (config/<Core>/<game>.opt), the content
// folder's (config/<Core>/<folder>.opt), then — with "global core options"
// off, the default since 1.9 — the core's config/<Core>/<Core>.opt, or
// retroarch-core-options.cfg with it on. That file is found from the same
// retroarch.cfg the launch edits and edited through the same scoped writer. A
// core that has never run has no .opt yet; RetroArch would seed it from the
// global file on exit, copying JukaHub's value into a file nobody restores, so
// that one case is skipped (and logged) rather than written.
//
// Audio quality, latency, the overlay, the look and overscan cropping can
// differ per console (stored as raTuning.<console>.audio / .latency /
// .overlay / .look / .crop); the rest is one choice for every console.

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// The Custom-map keys of the all-consoles choices.
const (
	raTuneAudioKey   = "raTuningAudio"
	raTuneLatencyKey = "raTuningLatency"
	raTuneOverlayKey = "raTuningOverlay"
	raTuneSmoothKey  = "raTuningSmooth"
	raTuneRumbleKey  = "raTuningRumble"
	raTuneRotateKey  = "raTuningRotate"
	raTunePALKey     = "raTuningPAL"
	raTuneCropKey    = "raTuningCrop"

	// raTuneConsolePrefix starts a per-console key:
	// raTuning.<console slug>.audio | .latency | .overlay | .look | .crop.
	raTuneConsolePrefix = "raTuning."

	// The overlay scan is bounded like the shader scan: RetroArch's overlay
	// pack is a few hundred files, and a card full of something else must not
	// stall the settings page's background probe. SkyWalker541's Brick pack
	// (shader_looks_download.go) sits a few folders down, under its own tree.
	raTuneOverlayDepth    = 5
	raTuneOverlayScanMax  = 6000
	raTuneOverlayListMax  = 400
	raTuneOverlayOpacity  = "1.000000"
	raTuneLowAudioMs      = "64"
	raTuneLowestAudioMs   = "48"
	raTuneLowSwapchainMax = "2"
)

// The stored values of the choices.
const (
	raTuneOwn = "own" // leave RetroArch's own value alone

	raTuneAuto     = "auto"
	raTuneBest     = "best"
	raTuneBalanced = "balanced"
	raTuneFast     = "fast"

	raTuneStandard = "standard"
	raTuneLow      = "low"
	raTuneLowest   = "lowest"

	raTuneOverlayNone = "none"

	raTuneUpright = "upright"
	raTuneTate    = "tate"
)

// raTuneTier is how hard a console works the A133: it picks Auto's audio
// quality and latency.
type raTuneTier int

const (
	raTuneLight  raTuneTier = iota // 8/16-bit consoles and the handhelds
	raTuneMedium                   // PlayStation, N64, DS, CD and arcade systems
	raTuneHeavy                    // PSP, Dreamcast, Saturn, Jaguar
)

// raTuneSystem is one console the Emulation page can tune on its own.
type raTuneSystem struct {
	Name string // the library's console name (romSystemAliases)
	Tier raTuneTier
	// RunAhead marks the consoles cheap enough to emulate twice per frame on
	// the A133, so Lowest may add a frame of run-ahead. The SNES and the GBA
	// are light but not that light.
	RunAhead bool
}

// raTuneSystems is every console the per-console picker lists, in the order
// a player looks for them. A console not listed is Medium.
var raTuneSystems = []raTuneSystem{
	{"NES", raTuneLight, true},
	{"SNES", raTuneLight, false},
	{"Game Boy", raTuneLight, true},
	{"Game Boy Color", raTuneLight, true},
	{"Game Boy Advance", raTuneLight, false},
	{"Mega Drive", raTuneLight, true},
	{"Master System", raTuneLight, true},
	{"Game Gear", raTuneLight, true},
	{"PC Engine", raTuneLight, true},
	{"Atari 2600", raTuneLight, true},
	{"Atari 5200", raTuneLight, true},
	{"Atari 7800", raTuneLight, true},
	{"Atari Lynx", raTuneLight, true},
	{"WonderSwan", raTuneLight, true},
	{"WonderSwan Color", raTuneLight, true},
	{"Neo Geo Pocket", raTuneLight, true},
	{"Neo Geo Pocket Color", raTuneLight, true},
	{"PlayStation", raTuneMedium, false},
	{"Nintendo 64", raTuneMedium, false},
	{"Nintendo DS", raTuneMedium, false},
	{"Sega CD", raTuneMedium, false},
	{"32X", raTuneMedium, false},
	{"Arcade", raTuneMedium, false},
	{"Neo Geo", raTuneMedium, false},
	{"Neo Geo CD", raTuneMedium, false},
	// fake-08 in RetroArch: listed for its own overlay (Overlays/PICO-8,
	// useroverlays.go) and buttons; Medium is what an unlisted console gets.
	{"PICO-8", raTuneMedium, false},
	{"Atari Jaguar", raTuneHeavy, false},
	{"Saturn", raTuneHeavy, false},
	{"Dreamcast", raTuneHeavy, false},
	{"PlayStation Portable", raTuneHeavy, false},
}

// raTuneCoreFolders are the folder names RetroArch gives a core's options
// (its library_name) for the cores whose options JukaHub sets, by core file
// stem. Nestopia has used both names.
var raTuneCoreFolders = map[string][]string{
	"fbneo":           {"FinalBurn Neo"},
	"mame2003_plus":   {"MAME 2003-Plus"},
	"genesis_plus_gx": {"Genesis Plus GX"},
	"picodrive":       {"PicoDrive"},
	"fceumm":          {"FCEUmm"},
	"nestopia":        {"Nestopia", "Nestopia UE"},
	"snes9x":          {"Snes9x"},
	"pcsx_rearmed":    {"PCSX-ReARMed"},
	// Set by the DOS and ScummVM shelves and the control layouts (DS touch).
	"dosbox_pure": {"DOSBox-pure"},
	"scummvm":     {"scummvm"},
	"melonds":     {"melonDS"},
	"melondsds":   {"melonDS DS"},
	"desmume":     {"DeSmuME"},
	"desmume2015": {"DeSmuME 2015"},
	"tic80":       {"TIC-80"},
	"easyrpg":     {"EasyRPG Player"},
	// Set by the PS2 renderer fallback (ps2_video.go).
	"armsx2": {"ARMSX2"},
}

// raTunePALOptions is each core's region option set to PAL. The values are the
// cores' own spellings (libretro docs; FCEUmm and PCSX-ReARMed capitalise).
var raTunePALOptions = map[string]raScopeValue{
	"genesis_plus_gx": {Key: "genesis_plus_gx_region_detect", Value: "pal"},
	"picodrive":       {Key: "picodrive_region", Value: "Europe"},
	"fceumm":          {Key: "fceumm_region", Value: "PAL"},
	"nestopia":        {Key: "nestopia_favored_system", Value: "pal"},
	"snes9x":          {Key: "snes9x_region", Value: "pal"},
	"pcsx_rearmed":    {Key: "pcsx_rearmed_region", Value: "PAL"},
}

// raTuneTateOptions is each arcade core's vertical (TATE) mode option; both
// take "enabled" and "disabled".
var raTuneTateOptions = map[string]string{
	"fbneo":         "fbneo-vertical-mode",
	"mame2003_plus": "mame2003-plus_tate_mode",
}

// raTuneCandidates is a variable so tests could point at a temporary config.
var raTuneCandidates = retroArchConfigCandidates

// ── Consoles ─────────────────────────────────────────────────────────────

// raTuneSystemFor finds the console a library system name is (any alias the
// compatibility table knows: "GBA", "genesis", "psx"…). ok is false for a
// console the page does not list.
func raTuneSystemFor(system string) (raTuneSystem, bool) {
	name := canonicalCompatSystem(system)
	for _, s := range raTuneSystems {
		if strings.EqualFold(s.Name, name) || strings.EqualFold(s.Name, strings.TrimSpace(system)) {
			return s, true
		}
	}
	return raTuneSystem{Name: name, Tier: raTuneMedium}, false
}

// raTuneSlug is a console's part of a per-console key: "Game Boy Advance" →
// "game-boy-advance".
func raTuneSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if b.Len() > 0 && !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// raTuneConsoleKey is the Custom key of one console's own choice ("audio",
// "latency" or "overlay").
func raTuneConsoleKey(system, what string) string {
	return raTuneConsolePrefix + raTuneSlug(system) + "." + what
}

// ── Reading the choices ──────────────────────────────────────────────────

// raTuneRaw is a stored choice as written ("" when unset).
func raTuneRaw(c *Config, key string) string {
	if c == nil {
		return ""
	}
	if v, ok := customValue(c.Variables.Custom, key); ok && v != nil {
		return coerceString(v)
	}
	return ""
}

func normaliseRATuneAudio(v string) string {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case raTuneAuto, raTuneBest, raTuneBalanced, raTuneFast, raTuneOwn:
		return v
	}
	return ""
}

func normaliseRATuneLatency(v string) string {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case raTuneAuto, raTuneStandard, raTuneLow, raTuneLowest:
		return v
	}
	return ""
}

// normaliseRATuneOverlay keeps a path as it is; the two words are
// case-insensitive.
func normaliseRATuneOverlay(v string) string {
	v = strings.TrimSpace(v)
	switch strings.ToLower(v) {
	case raTuneOwn:
		return raTuneOwn
	case raTuneOverlayNone:
		return raTuneOverlayNone
	}
	return v
}

// raTuneAudioSetting is the all-consoles audio choice; unset is Auto.
func raTuneAudioSetting(c *Config) string {
	if v := normaliseRATuneAudio(raTuneRaw(c, raTuneAudioKey)); v != "" {
		return v
	}
	return raTuneAuto
}

// raTuneLatencySetting is the all-consoles latency choice; unset is Auto.
func raTuneLatencySetting(c *Config) string {
	if v := normaliseRATuneLatency(raTuneRaw(c, raTuneLatencyKey)); v != "" {
		return v
	}
	return raTuneAuto
}

// raTuneOverlaySetting is the all-consoles overlay: a path, "none", or "own"
// (unset).
func raTuneOverlaySetting(c *Config) string {
	if v := normaliseRATuneOverlay(raTuneRaw(c, raTuneOverlayKey)); v != "" {
		return v
	}
	return raTuneOwn
}

// raTuneConsoleAudio, raTuneConsoleLatency and raTuneConsoleOverlay are one
// console's own choice, "" when it follows the all-consoles one. Auto is not
// a per-console choice (it already is one).
func raTuneConsoleAudio(c *Config, system string) string {
	if v := normaliseRATuneAudio(raTuneRaw(c, raTuneConsoleKey(system, "audio"))); v != raTuneAuto {
		return v
	}
	return ""
}

func raTuneConsoleLatency(c *Config, system string) string {
	if v := normaliseRATuneLatency(raTuneRaw(c, raTuneConsoleKey(system, "latency"))); v != raTuneAuto {
		return v
	}
	return ""
}

func raTuneConsoleOverlay(c *Config, system string) string {
	return normaliseRATuneOverlay(raTuneRaw(c, raTuneConsoleKey(system, "overlay")))
}

// raTuneAutoAudio is Auto's answer for a tier.
func raTuneAutoAudio(tier raTuneTier) string {
	switch tier {
	case raTuneLight:
		return raTuneBest
	case raTuneHeavy:
		return raTuneFast
	}
	return raTuneBalanced
}

// raTuneAutoLatency is Auto's answer for a tier.
func raTuneAutoLatency(tier raTuneTier) string {
	if tier == raTuneLight {
		return raTuneLow
	}
	return raTuneStandard
}

// raTuneAudioFor is the audio mode a launch of a system's game uses: the
// console's own choice, else the all-consoles one, with Auto resolved.
func raTuneAudioFor(c *Config, system string) string {
	sys, _ := raTuneSystemFor(system)
	mode := raTuneConsoleAudio(c, sys.Name)
	if mode == "" {
		mode = raTuneAudioSetting(c)
	}
	if mode == raTuneAuto {
		mode = raTuneAutoAudio(sys.Tier)
	}
	return mode
}

// raTuneLatencyFor is raTuneAudioFor for latency.
func raTuneLatencyFor(c *Config, system string) string {
	sys, _ := raTuneSystemFor(system)
	mode := raTuneConsoleLatency(c, sys.Name)
	if mode == "" {
		mode = raTuneLatencySetting(c)
	}
	if mode == raTuneAuto {
		mode = raTuneAutoLatency(sys.Tier)
	}
	return mode
}

// raTuneOverlayFor is the overlay a launch uses: a path, "none" or "own". A
// console left at Default plays the overlay in its Overlays/<Console> folder
// on the card when there is one (useroverlays.go; reads that folder).
func raTuneOverlayFor(c *Config, system string) string {
	sys, _ := raTuneSystemFor(system)
	if v := raTuneConsoleOverlay(c, sys.Name); v != "" {
		return v
	}
	if v := userOverlayAuto(sys.Name); v != "" {
		return v
	}
	return raTuneOverlaySetting(c)
}

// raTuneSmoothOn is the Smooth motion switch; on unless turned off.
func raTuneSmoothOn(c *Config) bool {
	return c == nil || customBool(c.Variables.Custom, raTuneSmoothKey, true)
}

// raTuneRumbleSetting is "own" or a percentage 0…100 as text.
func raTuneRumbleSetting(c *Config) string {
	v := strings.ToLower(raTuneRaw(c, raTuneRumbleKey))
	if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 100 {
		return strconv.Itoa(n)
	}
	return raTuneOwn
}

// raTuneRotateSetting is "upright" (the default), "tate" or "own".
func raTuneRotateSetting(c *Config) string {
	switch v := strings.ToLower(raTuneRaw(c, raTuneRotateKey)); v {
	case raTuneTate, raTuneOwn:
		return v
	}
	return raTuneUpright
}

// raTunePALOn is the PAL mode switch; off unless turned on.
func raTunePALOn(c *Config) bool {
	return c != nil && customBool(c.Variables.Custom, raTunePALKey, false)
}

// raTuneCropWord reads a stored crop choice: "on", "off", or "" for neither.
func raTuneCropWord(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true":
		return "on"
	case "off", "false":
		return "off"
	}
	return ""
}

// raTuneCropSetting is the all-consoles Crop overscan choice: "on", "off",
// or "own" (unset: RetroArch's own setting, which crops by default).
func raTuneCropSetting(c *Config) string {
	if v := raTuneCropWord(raTuneRaw(c, raTuneCropKey)); v != "" {
		return v
	}
	return raTuneOwn
}

// raTuneConsoleCrop is one console's own crop choice: "on", "off", or ""
// when it follows All consoles.
func raTuneConsoleCrop(c *Config, system string) string {
	return raTuneCropWord(raTuneRaw(c, raTuneConsoleKey(system, "crop")))
}

// raTuneCropFor is a launch's crop choice: "on", "off" or "own".
func raTuneCropFor(c *Config, system string) string {
	sys, _ := raTuneSystemFor(system)
	if v := raTuneConsoleCrop(c, sys.Name); v != "" {
		return v
	}
	return raTuneCropSetting(c)
}

// ── The plan for one launch ──────────────────────────────────────────────

// raTunePlan is what one launch writes: retroarch.cfg values, numbers that
// are only ever lowered (Ceil), and the core's options.
type raTunePlan struct {
	Config []raScopeValue
	Ceil   []raScopeValue
	Core   []raScopeValue
	// What says, for the log, which settings the values came from.
	What []string
}

// raTuneArcade reports whether a launch is an arcade one: rotation applies.
func raTuneArcade(system, core string) bool {
	if _, ok := raTuneTateOptions[core]; ok {
		return true
	}
	switch canonicalCompatSystem(system) {
	case "Arcade", "Neo Geo":
		return true
	}
	return false
}

// raTunePlanFor builds the launch's values from the settings. Pure but for
// the overlay: the file check, the console's Overlays folder, and the .cfg a
// picture from there gets (useroverlays.go).
func raTunePlanFor(c *Config, system, core string) raTunePlan {
	var p raTunePlan
	sys, _ := raTuneSystemFor(system)

	switch mode := raTuneAudioFor(c, system); mode {
	case raTuneBest, raTuneBalanced:
		quality := "4"
		if mode == raTuneBalanced {
			quality = "2"
		}
		p.Config = append(p.Config,
			raScopeValue{Key: "audio_resampler", Value: "sinc"},
			raScopeValue{Key: "audio_resampler_quality", Value: quality})
		p.What = append(p.What, "audio "+mode)
	case raTuneFast:
		p.Config = append(p.Config, raScopeValue{Key: "audio_resampler", Value: "cc"})
		p.What = append(p.What, "audio fast")
	}

	switch mode := raTuneLatencyFor(c, system); mode {
	case raTuneLow, raTuneLowest:
		audio := raTuneLowAudioMs
		if mode == raTuneLowest {
			audio = raTuneLowestAudioMs
		}
		p.Config = append(p.Config, raScopeValue{Key: "video_frame_delay_auto", Value: "true"})
		p.Ceil = append(p.Ceil,
			raScopeValue{Key: "video_max_swapchain_images", Value: raTuneLowSwapchainMax},
			raScopeValue{Key: "audio_latency", Value: audio})
		if mode == raTuneLowest && sys.RunAhead {
			p.Config = append(p.Config,
				raScopeValue{Key: "run_ahead_enabled", Value: "true"},
				raScopeValue{Key: "run_ahead_frames", Value: "1"},
				raScopeValue{Key: "run_ahead_secondary_instance", Value: "false"})
			mode += " + run-ahead"
		}
		p.What = append(p.What, "latency "+mode)
	}

	if raTuneSmoothOn(c) {
		p.Config = append(p.Config,
			raScopeValue{Key: "video_vsync", Value: "true"},
			raScopeValue{Key: "vrr_runloop_enable", Value: "false"},
			raScopeValue{Key: "audio_sync", Value: "true"},
			raScopeValue{Key: "audio_rate_control", Value: "true"})
		p.What = append(p.What, "smooth motion")
	}

	// Threaded video: off on the Smart Pro S unless chosen (ra_threaded.go).
	if v, what, ok := raThreadedValue(c); ok {
		p.Config = append(p.Config, v)
		p.What = append(p.What, what)
	}

	switch overlay := raTuneOverlayFor(c, system); overlay {
	case raTuneOwn:
	case raTuneOverlayNone:
		p.Config = append(p.Config, raScopeValue{Key: "input_overlay_enable", Value: "false"})
		p.What = append(p.What, "no overlay")
	default:
		if !regularFile(overlay) {
			logWarn("emulation", "overlay %s is not on the card any more; launching with RetroArch's own overlay setting", overlay)
			break
		}
		// A picture from an Overlays folder gets a generated .cfg (useroverlays.go).
		cfg := userOverlayRetroArchCfg(overlay)
		if cfg == "" {
			break
		}
		p.Config = append(p.Config,
			raScopeValue{Key: "input_overlay_enable", Value: "true"},
			raScopeValue{Key: "input_overlay", Value: cfg},
			raScopeValue{Key: "input_overlay_opacity", Value: raTuneOverlayOpacity},
			raScopeValue{Key: "input_overlay_hide_in_menu", Value: "true"})
		p.What = append(p.What, "overlay "+filepath.Base(overlay))
	}

	if gain := raTuneRumbleSetting(c); gain != raTuneOwn {
		p.Config = append(p.Config, raScopeValue{Key: "input_rumble_gain", Value: gain})
		p.What = append(p.What, "rumble "+gain+"%")
	}

	if raTuneArcade(system, core) {
		switch rot := raTuneRotateSetting(c); rot {
		case raTuneUpright, raTuneTate:
			p.Config = append(p.Config, raScopeValue{Key: "video_allow_rotate", Value: "true"})
			// Upright as the untouched default only lets the core rotate; a
			// vertical mode the user set in RetroArch stays. Picked on the
			// row, Upright also switches that mode off.
			explicit := raTuneRaw(c, raTuneRotateKey) != ""
			if key, ok := raTuneTateOptions[core]; ok && (rot == raTuneTate || explicit) {
				value := "disabled"
				if rot == raTuneTate {
					value = "enabled"
				}
				p.Core = append(p.Core, raScopeValue{Key: key, Value: value})
			}
			p.What = append(p.What, "rotation "+rot)
		}
	}

	if raTunePALOn(c) {
		if opt, ok := raTunePALOptions[core]; ok {
			p.Core = append(p.Core, opt)
			p.What = append(p.What, "PAL")
		}
	}

	switch raTuneCropFor(c, system) {
	case "on":
		p.Config = append(p.Config, raScopeValue{Key: "video_crop_overscan", Value: "true"})
		p.What = append(p.What, "crop overscan")
	case "off":
		p.Config = append(p.Config, raScopeValue{Key: "video_crop_overscan", Value: "false"})
		p.What = append(p.What, "no overscan crop")
	}
	return p
}

// raTuneCeilValues keeps the ceiling values a config needs: a key it does not
// have, or holds above the ceiling (or not as a positive number). A lower
// value the user chose stays. Pure.
func raTuneCeilValues(cfg string, ceil []raScopeValue) []raScopeValue {
	var out []raScopeValue
	for _, v := range ceil {
		if cur, had := raConfigGet(cfg, v.Key); had {
			n, err := strconv.ParseFloat(strings.TrimSpace(cur), 64)
			limit, limitErr := strconv.ParseFloat(v.Value, 64)
			if err == nil && limitErr == nil && n > 0 && n <= limit {
				continue
			}
		}
		out = append(out, v)
	}
	return out
}

// ── Core options ─────────────────────────────────────────────────────────

// raTuneMenuConfigDir is RetroArch's menu config folder for a retroarch.cfg:
// rgui_config_directory, else "config" beside it.
func raTuneMenuConfigDir(cfg, cfgPath string) string {
	if v, ok := raConfigGet(cfg, "rgui_config_directory"); ok {
		if dir := videoFilterExpandDir(v, cfgPath); dir != "" {
			return dir
		}
	}
	return filepath.Join(filepath.Dir(cfgPath), "config")
}

// raTuneCoreOptionFile is the options file RetroArch will read and write for
// core when it plays romPath with the config at cfgPath, or "" when there is
// none JukaHub can safely edit (see the file comment). Order: the game's own
// .opt, the content folder's, then the core's .opt or the global file as
// global_core_options says. A config without that key predates per-core
// options (RetroArch before 1.9), so the global file is the one.
func raTuneCoreOptionFile(cfgPath, core, romPath string) string {
	folders := raTuneCoreFolders[core]
	if len(folders) == 0 {
		return ""
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return ""
	}
	cfg := string(raw)
	confDir := raTuneMenuConfigDir(cfg, cfgPath)
	stem := strings.TrimSuffix(filepath.Base(romPath), filepath.Ext(romPath))
	parent := filepath.Base(filepath.Dir(romPath))
	for _, folder := range folders {
		dir := filepath.Join(confDir, folder)
		if !dirExists(dir) {
			continue
		}
		for _, name := range []string{stem, parent} {
			if name == "" || name == "." || name == string(filepath.Separator) {
				continue
			}
			if p := filepath.Join(dir, name+".opt"); regularFile(p) {
				return p
			}
		}
	}
	globalFile := func() string {
		if v, ok := raConfigGet(cfg, "core_options_path"); ok {
			if p := videoFilterExpandDir(v, cfgPath); p != "" && regularFile(p) {
				return p
			}
		}
		if p := filepath.Join(filepath.Dir(cfgPath), "retroarch-core-options.cfg"); regularFile(p) {
			return p
		}
		return ""
	}
	global, hasGlobal := raConfigGet(cfg, "global_core_options")
	if hasGlobal && raValueEqual(global, "true") {
		return globalFile()
	}
	for _, folder := range folders {
		if p := filepath.Join(confDir, folder, folder+".opt"); regularFile(p) {
			return p
		}
	}
	if !hasGlobal {
		return globalFile()
	}
	return ""
}

// ── Launch ───────────────────────────────────────────────────────────────

// prepareRATuning is the launch-time entry point (game_library.go, beside
// prepareVideoFilter). It never blocks or fails a launch: a read-only card, a
// missing config or a core with no options file yet is logged and the game
// starts with RetroArch's own settings. Standalone emulators are skipped.
func prepareRATuning(launcher string, game libraryGame) {
	if appConfig == nil || !retroArchLauncher(launcher) {
		return
	}
	core := gameCoreFromLauncher(launcher, game.System)
	plan := raTunePlanFor(appConfig, game.System, core)
	if len(plan.Config) == 0 && len(plan.Ceil) == 0 && len(plan.Core) == 0 {
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		return
	}
	applied := false
	for _, path := range targets {
		raw, err := os.ReadFile(path)
		if err != nil {
			logWarn("emulation", "cannot read %s: %v", path, err)
			continue
		}
		values := append(append([]raScopeValue(nil), plan.Config...), raTuneCeilValues(string(raw), plan.Ceil)...)
		if len(values) == 0 {
			continue
		}
		changed, err := raScopeApply(path, values)
		if err != nil {
			logWarn("emulation", "cannot apply the emulation settings in %s: %v", path, err)
			continue
		}
		applied = applied || changed
	}
	if len(plan.Core) > 0 {
		if opt := raTuneCoreOptionFile(targets[0], core, game.Path); opt != "" {
			changed, err := raScopeApply(opt, plan.Core)
			if err != nil {
				logWarn("emulation", "cannot set %s's options in %s: %v", core, opt, err)
			}
			applied = applied || changed
		} else {
			logInfo("emulation", "%s has no options file yet (start it once from RetroArch); rotation/PAL skipped for this launch", core)
		}
	}
	if applied {
		Log().Info("emulation settings applied for this launch", "system", game.System, "core", core, "settings", strings.Join(plan.What, ", "))
	}
}

// ── Overlays on the card ─────────────────────────────────────────────────

// raTuneOverlay is one overlay file the page offers.
type raTuneOverlay struct {
	Path  string
	Label string // its place under the overlay folder, without .cfg
}

// raTuneOverlaySkipDirs are overlay folders made for touch screens: on-screen
// pads and keyboards a handheld with buttons has no use for.
var raTuneOverlaySkipDirs = map[string]bool{
	"gamepads":  true,
	"keyboards": true,
	"ipad":      true,
	"ctr":       true,
}

// raTuneScanOverlays lists the overlay .cfg files of the RetroArch install a
// config belongs to: overlay_directory and the usual folders, a few levels
// deep, bounded.
func raTuneScanOverlays(cfgPath string) []raTuneOverlay {
	if cfgPath == "" {
		return nil
	}
	raw, _ := os.ReadFile(cfgPath)
	install := raInstallDir(cfgPath)
	var roots []string
	if v, ok := raConfigGet(string(raw), "overlay_directory"); ok {
		roots = append(roots, videoFilterExpandDir(v, cfgPath))
	}
	roots = append(roots,
		filepath.Join(install, "overlays"),
		filepath.Join(install, ".retroarch", "overlays"),
		filepath.Join(filepath.Dir(cfgPath), "overlays"))

	var out []raTuneOverlay
	seenRoot := map[string]bool{}
	seenFile := map[string]bool{}
	count := 0
	var walk func(root, dir string, level int)
	walk = func(root, dir string, level int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			count++
			if count > raTuneOverlayScanMax || len(out) >= raTuneOverlayListMax {
				return
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			full := filepath.Join(dir, name)
			if e.IsDir() {
				if level < raTuneOverlayDepth && !raTuneOverlaySkipDirs[strings.ToLower(name)] {
					walk(root, full, level+1)
				}
				continue
			}
			if !strings.EqualFold(filepath.Ext(name), ".cfg") {
				continue
			}
			key := strings.ToLower(full)
			if seenFile[key] {
				continue
			}
			seenFile[key] = true
			rel, err := filepath.Rel(root, full)
			if err != nil {
				rel = name
			}
			label := strings.TrimSuffix(filepath.ToSlash(rel), filepath.Ext(rel))
			out = append(out, raTuneOverlay{Path: full, Label: strings.ReplaceAll(label, "/", " / ")})
		}
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		key := strings.ToLower(filepath.Clean(root))
		if seenRoot[key] {
			continue
		}
		seenRoot[key] = true
		walk(root, root, 0)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	logDebug("emulation", "%s: %d overlays", install, len(out))
	return out
}

// raTuneOverlayProbe is the default install's overlay list for the Settings
// page, filled in the background so opening the page never stalls a frame.
var raTuneOverlayProbe struct {
	mu      sync.Mutex
	list    []raTuneOverlay
	done    bool
	running bool
	// gen moves on when the list is dropped (raTuneOverlaysForget); stale
	// keeps the old list on show until the new scan is in.
	gen   int
	stale bool
}

// raTuneOverlays is the list for the menus; ok is false while the scan runs
// (the first call starts it).
func raTuneOverlays() ([]raTuneOverlay, bool) {
	raTuneOverlayProbe.mu.Lock()
	defer raTuneOverlayProbe.mu.Unlock()
	if raTuneOverlayProbe.done {
		return raTuneOverlayProbe.list, true
	}
	if !raTuneOverlayProbe.running {
		raTuneOverlayProbe.running = true
		gen := raTuneOverlayProbe.gen
		go func() {
			var list []raTuneOverlay
			if targets := raScopeTargets(raTuneCandidates()); len(targets) > 0 {
				list = raTuneScanOverlays(targets[0])
			}
			raTuneOverlayProbe.mu.Lock()
			if gen == raTuneOverlayProbe.gen {
				raTuneOverlayProbe.list = list
				raTuneOverlayProbe.done = true
				raTuneOverlayProbe.stale = false
			}
			raTuneOverlayProbe.running = false
			raTuneOverlayProbe.mu.Unlock()
		}()
	}
	if raTuneOverlayProbe.stale {
		return raTuneOverlayProbe.list, true // the old list until the rescan is in
	}
	return nil, false
}

// raTuneOverlaysForget makes the menus scan the overlay folders again
// (overlays were downloaded). Safe from any goroutine.
func raTuneOverlaysForget() {
	raTuneOverlayProbe.mu.Lock()
	raTuneOverlayProbe.gen++
	raTuneOverlayProbe.stale = raTuneOverlayProbe.done || raTuneOverlayProbe.stale
	raTuneOverlayProbe.done = false
	raTuneOverlayProbe.mu.Unlock()
}
