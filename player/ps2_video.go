package main

// ps2_video.go — which renderer the ARMSX2 core draws with on the handheld
// (ps2.go).
//
// ARMSX2 draws through the GPU context RetroArch shares with it: Vulkan by
// default, OpenGL or its software renderer when the "GS Renderer" core option
// (armsx2_renderer) says so; the option takes effect at the next start. With
// OpenGL the core asks for desktop OpenGL 3.3 (OpenGL ES 3.2 only on Android
// and builds made for it), and the software renderer presents its picture
// through the same shared context. A frontend that grants none of them leaves
// the core on its "Null GS": RetroArch keeps running, paused, on a black
// screen, and nothing ends by itself (ARMSX2 issues #693, #705).
//
// What these handhelds give it:
//
//   - The firmware's Vulkan driver: TrimUI added one to the Smart Pro's
//     firmware 1.0.4 (PowerVR blobs 1.19; the Brick and Brick Pro share the
//     platform) and the Smart Pro S ships Mali's.
//   - TrimUI's own RetroArch, built with Vulkan since the SD package of
//     2024-04-01 (RetroArch 1.18, later 1.20; 1.22 on the Smart Pro S),
//     drawing straight to the panel through its "khr_display" Vulkan context
//     (RetroArch issue #18206 shows it on a Smart Pro). For a core that asks
//     for Vulkan, RetroArch switches its video driver to Vulkan by itself —
//     when driver switching is allowed. Its OpenGL driver is OpenGL ES only:
//     it refuses desktop OpenGL.
//
// So every start says the renderer (ps2LaunchPrepare): Vulkan where the
// firmware has a Vulkan driver and the RetroArch that starts the game was
// built with it — read from its binary once per build (ps2RAVulkan) — else
// OpenGL; RetroArch is told for that start that it may switch drivers and
// not to thread video. A start that stops at once with the renderer or its
// GPU context failing in RetroArch's log (ps2VideoFailureLine), that crashes
// in its first half minute (a GPU driver that falls over takes RetroArch's
// last log lines with it), or that ends at any time after the core fell back
// to its Null GS, moves the core to the next renderer for the next start —
// after Vulkan the software renderer (still presented through the Vulkan
// context, which is there even when the GPU renderer could not start on this
// driver), then OpenGL — and the card says so first (gamediag.go). Each
// renderer is tried once per core build; when all failed, the card says this
// RetroArch cannot draw for ARMSX2. Settings → Games & emulators → "PS2
// renderer" picks one by hand instead.
//
// The renderer goes into the options file RetroArch reads for the core (the
// game's own .opt when it has one), where the file holds nothing for it, the
// core's default, or the value JukaHub wrote last: one the player set in
// RetroArch's menu stays while the setting is Automatic.

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ps2RendererSetting is Settings → Games & emulators → "PS2 renderer":
	// "auto", or a renderer in the core's own spelling.
	ps2RendererSetting = "ps2Renderer"
	ps2RendererAuto    = "auto"
	// ps2RendererChosenWhy is ps2RendererWanted's why for that choice: what
	// it picks is written whatever the options file holds.
	ps2RendererChosenWhy = "Settings → PS2 renderer"
)

// ps2RendererChoices is that row's order: Automatic, then the core's values.
var ps2RendererChoices = []string{ps2RendererAuto, ps2RendererVulkan, ps2RendererGL, ps2RendererSoftware}

// ps2RendererFrom reads the setting: ps2RendererAuto when unset or unknown.
func ps2RendererFrom(c *Config) string {
	if c != nil {
		if v, ok := customString(c.Variables.Custom, ps2RendererSetting); ok {
			if r := ps2RendererName(v); slices.Contains(ps2RendererChoices[1:], r) {
				return r
			}
		}
	}
	return ps2RendererAuto
}

// ps2RendererName is an option value in the core's own spelling.
func ps2RendererName(v string) string {
	v = strings.TrimSpace(v)
	for _, r := range []string{ps2RendererVulkan, ps2RendererGL, ps2RendererSoftware} {
		if strings.EqualFold(v, r) {
			return r
		}
	}
	return v
}

// ps2RendererLabel names a renderer for the card.
func ps2RendererLabel(r string) string {
	switch r {
	case ps2RendererGL:
		return "OpenGL ES"
	case ps2RendererSoftware:
		return "the software renderer"
	case ps2RendererAuto:
		return "Automatic"
	}
	return r
}

// ps2RendererShort names a renderer for Settings.
func ps2RendererShort(r string) string {
	if r == ps2RendererSoftware {
		return "Software"
	}
	return ps2RendererLabel(r)
}

// ── What this handheld can draw with ─────────────────────────────────────

// ps2VulkanDriver reports whether the firmware has a Vulkan driver: a loader
// or a driver manifest where a TrimUI firmware or a desktop system keeps
// them. Read once: the firmware does not change while JukaHub runs.
var ps2VulkanDriver = sync.OnceValue(func() bool {
	if dolphinVulkanPresent() {
		return true
	}
	for _, dir := range []string{"/usr/lib", "/usr/lib64", "/lib", "/lib64", "/usr/lib/aarch64-linux-gnu", "/lib/aarch64-linux-gnu", "/usr/trimui/lib"} {
		for _, name := range []string{"libvulkan.so.1", "libvulkan.so"} {
			if regularFile(filepath.Join(dir, name)) {
				return true
			}
		}
	}
	for _, dir := range []string{"/usr/share/vulkan/icd.d", "/etc/vulkan/icd.d", "/usr/local/share/vulkan/icd.d", "/usr/trimui/share/vulkan/icd.d"} {
		if m, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(m) > 0 {
			return true
		}
	}
	return false
})

// ps2RAVulkanMarkers are what a RetroArch binary holds about Vulkan: the
// error a build without it logs for a core that asks for a Vulkan context,
// the loader a build with it opens, and the name of the Vulkan context that
// draws straight to the panel (these handhelds have no X11 or Wayland).
var ps2RAVulkanMarkers = [][]byte{
	[]byte("not compiled against Vulkan"),
	[]byte("libvulkan.so"),
	[]byte("khr_display"),
}

// ps2RAVulkanRead reads the RetroArch binary at bin: "yes" when it was built
// with Vulkan and its display context, "no" when it says it was built
// without (or has Vulkan with no context that can draw here), "" when it
// cannot be read or says neither. It reads the whole file (a few dozen MB at
// most): off the main thread but for a start that finds no answer recorded.
func ps2RAVulkanRead(bin string) string {
	found, err := ps2FileHas(bin, ps2RAVulkanMarkers, 256<<20)
	if err != nil {
		return ""
	}
	switch {
	case found[0]:
		return "no"
	case found[1] && found[2]:
		return "yes"
	case found[1]:
		return "no"
	}
	return ""
}

// ps2FileHas reports which of needles the first limit bytes of the file at
// p hold, read in 1 MiB pieces (the end of one piece is kept for a needle
// that spans two).
func ps2FileHas(p string, needles [][]byte, limit int64) ([]bool, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	longest := 1
	for _, n := range needles {
		longest = max(longest, len(n))
	}
	found := make([]bool, len(needles))
	buf := make([]byte, 1<<20)
	keep := 0
	var read int64
	for read < limit {
		n, err := f.Read(buf[keep:])
		if n > 0 {
			read += int64(n)
			window := buf[:keep+n]
			for i, needle := range needles {
				if !found[i] && bytes.Contains(window, needle) {
					found[i] = true
				}
			}
			keep = min(longest-1, len(window))
			copy(buf, window[len(window)-keep:])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return found, err
		}
	}
	return found, nil
}

// ps2RAVulkanWords is an answer of ps2RAVulkanRead in words, for the log.
func ps2RAVulkanWords(answer string) string {
	switch answer {
	case "yes":
		return "built with Vulkan and its display context"
	case "no":
		return "built without Vulkan (or without a Vulkan context that draws here)"
	}
	return "its file does not say"
}

// ps2RAVulkan is ps2RAVulkanRead's answer for the RetroArch at bin, read
// once per build of it (path, size and time) and kept in armsx2.json.
func ps2RAVulkan(bin string) string {
	stamp := dolphinCoreIdentity(bin)
	if stamp == "" {
		return ""
	}
	if rec := ps2RecordGet(); rec.RAVulkanStamp == stamp {
		return rec.RAVulkan
	}
	answer := ps2RAVulkanRead(bin)
	ps2RecordUpdate(func(r *ps2Record) { r.RAVulkan, r.RAVulkanStamp = answer, stamp })
	logInfo("emulators", "PS2: RetroArch %s is %s", bin, ps2RAVulkanWords(answer))
	return answer
}

// ps2RAVulkanWarm reads it ahead for the RetroArch beside the ARMSX2 core:
// at start-up and after a setup, off the main thread.
func ps2RAVulkanWarm() {
	if ps2SoC() == "" {
		return
	}
	if core := ps2FindCore(); core != "" {
		ps2RAVulkan(coreRetroArchBinary(filepath.Dir(core)))
	}
}

// ps2RetroArchFor is the RetroArch a start in launcher runs: the one beside a
// core started directly, the one beside JukaHub's core for a script; "" when
// there is none.
func ps2RetroArchFor(launcher string) string {
	core := launcher
	if !ps2IsCore(core) {
		core = ps2FindCore()
	}
	if core == "" {
		return ""
	}
	return coreRetroArchBinary(filepath.Dir(core))
}

// ps2VulkanUsable reports whether ARMSX2 can draw with Vulkan through the
// RetroArch at bin: the firmware has a Vulkan driver and that RetroArch was
// built with Vulkan — or its file does not say, as TrimUI's own builds since
// 2024 were.
func ps2VulkanUsable(bin string) bool {
	return ps2VulkanDriver() && ps2RAVulkan(bin) != "no"
}

// ps2RendererChain is the order the renderers are tried in through the
// RetroArch at bin (see the file comment).
func ps2RendererChain(bin string) []string {
	if ps2VulkanUsable(bin) {
		return []string{ps2RendererVulkan, ps2RendererSoftware, ps2RendererGL}
	}
	return []string{ps2RendererGL, ps2RendererSoftware}
}

// ps2ChainWhy says why the chain starts where it does, for the log.
func ps2ChainWhy(bin string) string {
	switch {
	case !ps2VulkanDriver():
		return "the firmware has no Vulkan driver"
	case ps2RAVulkan(bin) == "no":
		return "this RetroArch was built without Vulkan"
	}
	return "the firmware has a Vulkan driver and this RetroArch was built with Vulkan"
}

// ps2RendererWanted is the renderer a start in core through the RetroArch at
// bin gets, and why (for the log): the one mode names, else the first of the
// chain this build of the core has not failed with (all failed: the first
// again, for a newer build or RetroArch to try).
func ps2RendererWanted(mode, bin, core string) (string, string) {
	if mode != ps2RendererAuto {
		return mode, ps2RendererChosenWhy
	}
	chain := ps2RendererChain(bin)
	rec := ps2RecordGet()
	var failed []string
	if id := dolphinCoreIdentity(core); id != "" && rec.RendererCore == id {
		failed = rec.RendererFailed
	}
	why := "automatic: " + ps2ChainWhy(bin)
	for _, r := range chain {
		if !slices.Contains(failed, r) {
			if len(failed) > 0 {
				why += "; " + strings.Join(failed, " and ") + " could not draw with this build"
			}
			return r, why
		}
	}
	return chain[0], why + "; every renderer failed with this build before"
}

// ── Every start ──────────────────────────────────────────────────────────

// ps2LaunchConfigValues are RetroArch's settings for a PS2 start: a core's
// request for another GPU context than RetroArch's video driver is granted
// (RetroArch then switches to the driver for it: Vulkan for ARMSX2), and
// video is not threaded — RetroArch threads only a picture a core draws in
// memory, and its Vulkan driver would not start threaded on the Smart Pro
// (RetroArch issue #18206).
var ps2LaunchConfigValues = []raScopeValue{
	{Key: "driver_switch_enable", Value: "true"},
	{Key: "video_threaded", Value: "false"},
}

// ps2LaunchPrepare is every PS2 start's hook (gameLaunchRegisterPrepare):
// ps2LaunchConfigValues for that start in each config of the RetroArch and,
// for a key it sets itself, in ARMSX2's override, which RetroArch reads last
// (journalled and put back when RetroArch exits, racfgscope.go); then the
// renderer. It never blocks or fails a launch. Main thread.
func ps2LaunchPrepare(launcher string, game libraryGame) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("emulators", "PS2: preparing the launch failed: %v", r)
		}
	}()
	if appConfig == nil || ps2SoC() == "" || !retroArchLauncher(launcher) || nextuiIsPakLauncher(launcher) {
		return
	}
	if !ps2LauncherPlays(launcher, game.System) {
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	for _, path := range targets {
		if _, err := raScopeApply(path, ps2LaunchConfigValues); err != nil {
			logWarn("emulators", "PS2: cannot set RetroArch's video settings in %s: %v", path, err)
		}
	}
	if override := ps2OverridePath(launcher); override != "" {
		if raw, err := os.ReadFile(override); err == nil {
			var held []raScopeValue
			for _, v := range ps2LaunchConfigValues {
				if _, has := raConfigGet(string(raw), v.Key); has {
					held = append(held, v)
				}
			}
			if len(held) > 0 {
				if _, err := raScopeApply(override, held); err != nil {
					logWarn("emulators", "PS2: cannot set RetroArch's video settings in %s: %v", override, err)
				}
			}
		}
	}
	core := launcher
	if !ps2IsCore(core) {
		core = ps2FindCore()
	}
	want, why := ps2RendererWanted(ps2RendererFrom(appConfig), ps2RetroArchFor(launcher), core)
	ps2RendererWrite(launcher, game.Path, want, why)
}

// ps2OverridePath is ARMSX2's RetroArch override (config/ARMSX2/ARMSX2.cfg)
// beside the config the launch reads; "" when no config is readable.
func ps2OverridePath(launcher string) string {
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		return ""
	}
	raw, err := os.ReadFile(targets[0])
	if err != nil {
		return ""
	}
	return filepath.Join(raTuneMenuConfigDir(string(raw), targets[0]), ps2CoreConfigName, ps2CoreConfigName+".cfg")
}

// ps2RendererWrite puts want into the options file RetroArch reads for the
// core (ps2OptionsPath) where it holds nothing for it, the core's default
// (Vulkan), or the value JukaHub wrote last; while the setting is Automatic a
// renderer the player chose in RetroArch's menu stays, and one chosen in
// Settings is written whatever the file holds. Main thread.
func ps2RendererWrite(launcher, romPath, want, why string) {
	optPath := ps2OptionsPath(launcher, romPath)
	if optPath == "" {
		logInfo("emulators", "PS2: no RetroArch options file to set the renderer in; ARMSX2's own (Vulkan) this launch")
		return
	}
	text := ""
	if data, err := os.ReadFile(optPath); err == nil {
		text = string(data)
	} else if !os.IsNotExist(err) {
		// There but unreadable (a card error): never written over blind.
		logWarn("emulators", "PS2: cannot read %s: %v", optPath, err)
		return
	}
	value, set := raConfigGet(text, ps2RendererKey)
	current := ps2RendererName(value)
	if set && current == want {
		logInfo("emulators", "PS2: ARMSX2 draws with %s (%s)", want, why)
		return
	}
	rec := ps2RecordGet()
	ours := filepath.Clean(rec.RendererPath) == filepath.Clean(optPath) && strings.EqualFold(current, rec.Renderer)
	if why != ps2RendererChosenWhy && set && current != "" && current != ps2RendererVulkan && !ours {
		logInfo("emulators", "PS2: ARMSX2 draws with %s, as set in RetroArch (%s); JukaHub's pick would be %s (%s)", current, optPath, want, why)
		return
	}
	updated, _ := raConfigSet(text, ps2RendererKey, want)
	if err := os.MkdirAll(filepath.Dir(optPath), 0o755); err != nil {
		logWarn("emulators", "PS2: could not set ARMSX2's renderer in %s: %v", optPath, err)
		return
	}
	if err := AtomicWrite(optPath, []byte(updated), 0o644); err != nil {
		logWarn("emulators", "PS2: could not set ARMSX2's renderer in %s: %v", optPath, err)
		return
	}
	ps2RecordUpdate(func(r *ps2Record) { r.Renderer, r.RendererPath = want, filepath.Clean(optPath) })
	logInfo("emulators", "PS2: ARMSX2 draws with %s this launch (%s); %s now says so", want, why, optPath)
}

// ps2OptionsPath is the options file RetroArch reads for the ARMSX2 core when
// launcher plays romPath: the game's own, the folder's or the core's .opt, or
// the shared file when global_core_options is on (raTuneCoreOptionFile); the
// core's .opt to create when none is there yet. "" when no RetroArch config is
// readable.
func ps2OptionsPath(launcher, romPath string) string {
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		return ""
	}
	cfgPath := targets[0]
	if p := raTuneCoreOptionFile(cfgPath, ps2CoreName, romPath); p != "" {
		return p
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return ""
	}
	cfg := string(raw)
	if global, ok := raConfigGet(cfg, "global_core_options"); ok && !raValueEqual(global, "true") {
		return filepath.Join(raTuneMenuConfigDir(cfg, cfgPath), ps2CoreConfigName, ps2CoreConfigName+".opt")
	}
	if v, ok := raConfigGet(cfg, "core_options_path"); ok {
		if p := videoFilterExpandDir(v, cfgPath); p != "" {
			return p
		}
	}
	return filepath.Join(filepath.Dir(cfgPath), "retroarch-core-options.cfg")
}

// ── After a start ────────────────────────────────────────────────────────

// ps2CrashWindow is how far into a PS2 start a crash counts as its
// renderer's (ps2StartTrouble): ARMSX2 sets its GPU renderer up in the
// start's first seconds, and starting the core on an A53 takes some of them.
const ps2CrashWindow = 30 * time.Second

// ps2StartTrouble is gamediag's look at a start in the ARMSX2 core that
// ended after ran: crash is the signal it crashed on within ps2CrashWindow
// (SIGABRT, SIGBUS or SIGSEGV; 0 for anything else and for a start JukaHub
// stopped), quiet is ps2QuietFailure. Reaper goroutine.
func ps2StartTrouble(game libraryGame, launcher, logPath string, waitErr error, ran time.Duration, stopped bool) (crash int, quiet bool) {
	if ps2SoC() == "" || !ps2LauncherPlays(launcher, game.System) {
		return 0, false
	}
	if _, sig := gameLaunchExitStatus(waitErr); !stopped && ran <= ps2CrashWindow {
		switch sig {
		case 6, 7, 11: // SIGABRT, SIGBUS, SIGSEGV
			crash = sig
		}
	}
	return crash, ps2QuietFailure(game, launcher, logPath)
}

// ps2AfterGame is the reaper's hook for a start in the ARMSX2 core that did
// not get going, that crashed in its first half minute (crashSig, the
// signal; 0 for none), or that ended after the core never drew
// (gamediag.go): a core that looked for its resources folder on disk gets it
// at the next start (ps2_setup.go's ResNeeded); a renderer that could not
// draw — or that the start crashed with, when its log names nothing else —
// is replaced by the next one, or, chosen in Settings, named on the card. ok
// is false for every other start. Reaper goroutine.
func ps2AfterGame(game libraryGame, launcher, logPath string, crashSig int) (issue launchIssue, ok bool) {
	if ps2SoC() == "" || (strings.TrimSpace(logPath) == "" && crashSig == 0) || !ps2LauncherPlays(launcher, game.System) {
		return launchIssue{}, false
	}
	core := launcher
	if !ps2IsCore(core) {
		core = ps2FindCore()
	}
	if line := ps2LogLastLine(logPath, ps2ResourcesFailureLine); line != "" {
		if issue, ok := ps2ResourcesAfterGame(core, line); ok {
			return issue, true
		}
	}
	line := ps2VideoFailureIn(logPath)
	if line == "" && crashSig > 0 {
		// A GPU driver that falls over while the renderer starts leaves no
		// line of its own: RetroArch's last lines die with it.
		line = "RetroArch crashed (signal " + strconv.Itoa(crashSig) + ", " + gameDiagSignalName(crashSig) + ") while ARMSX2 started"
	}
	if line == "" {
		return launchIssue{}, false
	}
	quoted := "its log: \"" + line + "\""
	if mode := ps2RendererModeNow(); mode != ps2RendererAuto {
		logWarn("emulators", "PS2: ARMSX2 could not draw with %s, chosen in Settings (%s)", mode, line)
		return launchIssue{
			Level:  int(gameDiagLikely),
			Title:  "ARMSX2 could not draw with " + ps2RendererLabel(mode),
			Detail: "RetroArch stopped before the game drew, or never showed a picture (" + quoted + "). " + ps2RendererLabel(mode) + " is chosen in Settings → Games & emulators → PS2 renderer: Automatic tries the others in turn.",
		}, true
	}
	optPath := ps2OptionsPath(launcher, game.Path)
	if optPath == "" {
		return launchIssue{
			Level:  int(gameDiagLikely),
			Title:  "RetroArch could not draw for ARMSX2",
			Detail: "RetroArch stopped before the game drew (" + quoted + "). Set ARMSX2's renderer to Software or OpenGL in RetroArch (Quick Menu → Core Options → GS Renderer), then start the game again.",
		}, true
	}
	text := ""
	if data, err := os.ReadFile(optPath); err == nil {
		text = string(data)
	} else if !os.IsNotExist(err) {
		// There but unreadable (a card error): never written over blind.
		return launchIssue{}, false
	}
	current := ps2RendererVulkan // the core's default
	if v, set := raConfigGet(text, ps2RendererKey); set && strings.TrimSpace(v) != "" {
		current = ps2RendererName(v)
	}
	coreID := dolphinCoreIdentity(core)
	rec := ps2RecordGet()
	var failed []string
	if rec.RendererCore == coreID {
		failed = rec.RendererFailed // the same build: what it failed with still counts
	}
	if !slices.Contains(failed, current) {
		failed = append(failed, current)
	}
	next := ""
	for _, r := range ps2RendererChain(ps2RetroArchFor(launcher)) {
		if !slices.Contains(failed, r) {
			next = r
			break
		}
	}
	if next == "" {
		ps2RecordUpdate(func(r *ps2Record) { r.RendererFailed, r.RendererCore = failed, coreID })
		labels := make([]string, 0, len(failed))
		for _, r := range failed {
			labels = append(labels, ps2RendererLabel(r))
		}
		tried := strings.Join(labels, " and ")
		logWarn("emulators", "PS2: RetroArch could not draw for ARMSX2 with %s (%s)", tried, line)
		return launchIssue{
			Level:  int(gameDiagLikely),
			Title:  "This RetroArch could not draw for ARMSX2",
			Detail: "RetroArch stopped before the game drew with " + tried + " (" + quoted + "). A newer ARMSX2 (Settings → Games & emulators → PS2 emulator) or a newer RetroArch for this device may draw.",
		}, true
	}
	updated, _ := raConfigSet(text, ps2RendererKey, next)
	if err := os.MkdirAll(filepath.Dir(optPath), 0o755); err != nil {
		logWarn("emulators", "PS2: could not set ARMSX2's renderer in %s: %v", optPath, err)
		return launchIssue{}, false
	}
	if err := AtomicWrite(optPath, []byte(updated), 0o644); err != nil {
		logWarn("emulators", "PS2: could not set ARMSX2's renderer in %s: %v", optPath, err)
		return launchIssue{}, false
	}
	ps2RecordUpdate(func(r *ps2Record) {
		r.RendererFailed, r.RendererCore = failed, coreID
		r.Renderer, r.RendererPath = next, filepath.Clean(optPath)
	})
	logInfo("emulators", "PS2: ARMSX2 could not draw with %s (%s); %s now says %s", current, line, optPath, next)
	return launchIssue{
		Level:  int(gameDiagSure),
		Title:  "ARMSX2 will draw with " + ps2RendererLabel(next) + " next time",
		Detail: "ARMSX2 could not draw with " + ps2RendererLabel(current) + " here (" + quoted + "). JukaHub set its GS Renderer to " + next + " (" + optPath + "): start the game again.",
	}, true
}

// ps2ResourcesAfterGame records that JukaHub's core, a build that carries its
// resources, looked for the folder on disk all the same (line, from its log):
// the next start fetches it from the same release (ps2Inspect). ok is false
// when that folder was wanted already: the diagnosis says what is missing.
func ps2ResourcesAfterGame(core, line string) (launchIssue, bool) {
	rec := ps2RecordGet()
	if core == "" || !ps2CoreIsOurs(core, rec) || ps2RecordNeedsResources(rec) {
		return launchIssue{}, false
	}
	ps2RecordUpdate(func(r *ps2Record) { r.ResNeeded = r.Release })
	logWarn("emulators", "PS2: ARMSX2 %s looked for its resources folder on disk (%s); the next start fetches it", rec.Release, line)
	return launchIssue{
		Level:  int(gameDiagSure),
		Title:  "ARMSX2 needs its resources folder",
		Detail: "This ARMSX2 build still looked for its resources on the card (its log: \"" + line + "\"). The next start fetches the folder from the same release (once, online): start the game again.",
	}, true
}

// ps2QuietFailure reports whether a start in the ARMSX2 core ended — at any
// time, by the player's hand too — after the core never drew (its Null GS:
// RetroArch stays on a black screen until the player leaves) or looked for
// its resources folder on disk. The card opens for such a start as for one
// that stopped at once, and ps2AfterGame changes what failed (gamediag.go).
// Reaper goroutine.
func ps2QuietFailure(game libraryGame, launcher, logPath string) bool {
	if ps2SoC() == "" || strings.TrimSpace(logPath) == "" || !ps2LauncherPlays(launcher, game.System) {
		return false
	}
	return ps2LogLastLine(logPath, ps2NullGSLine) != "" || ps2LogLastLine(logPath, ps2ResourcesFailureLine) != ""
}

// ps2VideoFailedStart reports whether a start in the ARMSX2 core stopped with
// the renderer failing to draw, or crashed in its first half minute
// (crashSig, ps2StartTrouble). The core build is not what failed then, as far
// as the card can tell, so gamediag.go keeps such a start out of a new
// build's probation and lets ps2AfterGame switch the renderer instead.
// Reaper goroutine.
func ps2VideoFailedStart(game libraryGame, launcher, logPath string, crashSig int) bool {
	return ps2SoC() != "" && ps2LauncherPlays(launcher, game.System) &&
		(crashSig > 0 || (strings.TrimSpace(logPath) != "" && ps2VideoFailureIn(logPath) != ""))
}

// ps2VideoFailureIn is the last line of the end of RetroArch's log at path
// that shows the renderer failing (the one nearest the stop), trimmed for the
// card; "" when there is none.
func ps2VideoFailureIn(path string) string {
	return ps2LogLastLine(path, ps2VideoFailureLine)
}

// ps2LogLastLine is the last line of the end of RetroArch's log at path that
// match accepts, trimmed for the card; "" when there is none.
func ps2LogLastLine(path string, match func(string) bool) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > dolphinLogTail {
		if _, err := f.Seek(-dolphinLogTail, io.SeekEnd); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, dolphinLogTail))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !match(line) {
			continue
		}
		if r := []rune(line); len(r) > 160 {
			line = string(r[:160]) + "…"
		}
		return line
	}
	return ""
}

// ps2VideoFailureLine reports whether a RetroArch log line says the renderer,
// its GPU context or the video driver failed: RetroArch's own lines as
// dolphinVideoFailureLine reads them, PCSX2's "Failed to create render
// device" and the like from the core, and the core's fall back to its Null
// GS when it got no context at all.
func ps2VideoFailureLine(line string) bool {
	if dolphinVideoFailureLine(line) || ps2NullGSLine(line) {
		return true
	}
	l := strings.ToLower(line)
	if !strings.Contains(l, "fail") && !strings.Contains(l, "unable") && !strings.Contains(l, "could not") {
		return false
	}
	if strings.Contains(l, "shared object") || strings.Contains(l, "dlopen") || strings.Contains(l, "bios") {
		return false
	}
	for _, s := range []string{"render device", "gs device", "initialize gs", "init gs", "gs renderer", "renderer", "gs backend"} {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// ps2NullGSLine reports whether a log line is the core falling back to its
// Null GS: "Frontend refused Vulkan HW context; falling back to Null GS."
func ps2NullGSLine(line string) bool {
	l := strings.ToLower(line)
	return strings.Contains(l, "null gs") && (strings.Contains(l, "fall") || strings.Contains(l, "refus"))
}

// ps2ResourcesFailureLine reports whether a log line is the core failing to
// find a file of its resources folder: a line the core logged (RetroArch
// prefixes them "[libretro") or one naming the game index, with a failure.
func ps2ResourcesFailureLine(line string) bool {
	l := strings.ToLower(line)
	if !strings.Contains(l, "gameindex") && !(strings.Contains(l, "[libretro") && strings.Contains(l, "resource")) {
		return false
	}
	for _, s := range []string{"fail", "missing", "not found", "cannot", "could not", "unable", "no such file"} {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// ── Settings → Games & emulators ─────────────────────────────────────────

// ps2RendererSettingsRow is "PS2 renderer", under "PS2 speed", on the
// handheld while PlayStation 2 is on.
func ps2RendererSettingsRow() setRow {
	return setRow{
		ID: ps2RendererSetting, Label: "PS2 renderer", Kind: setKindChoice,
		Visible: func(c *Config) bool { return ps2SoC() != "" && ps2Enabled(c) },
		Detail:  ps2RendererDetail,
		Options: func(*Config) []setOption {
			return []setOption{
				{Value: ps2RendererAuto, Label: "Automatic", Note: "Vulkan where this RetroArch and the firmware have it, else OpenGL ES; a renderer that cannot draw is replaced by the next"},
				{Value: ps2RendererVulkan, Label: "Vulkan", Note: "ARMSX2's own: the GPU draws the PS2's picture"},
				{Value: ps2RendererGL, Label: "OpenGL ES", Note: "The GPU through OpenGL, for a RetroArch without Vulkan"},
				{Value: ps2RendererSoftware, Label: "Software", Note: "The CPU draws: the slowest, but the GPU only shows the picture"},
			}
		},
		Current: func(c *Config) int { return slices.Index(ps2RendererChoices, ps2RendererFrom(c)) },
		Choose: func(c *Config, value string) {
			r := ps2RendererName(value)
			if !slices.Contains(ps2RendererChoices[1:], r) {
				r = ps2RendererAuto
			}
			setStoreString(c, ps2RendererSetting, r)
			ps2SyncMode(c)
			setToast("PS2 renderer: " + ps2RendererShort(r) + " · from the next game")
		},
	}
}

// ps2RendererDetail is that row's second line. Cheap: drawn every frame.
func ps2RendererDetail(c *Config) string {
	if r := ps2RendererFrom(c); r != ps2RendererAuto {
		return ps2RendererShort(r) + " · chosen here; Automatic tries each in turn"
	}
	if r := ps2RecordGet().Renderer; r != "" {
		return "Automatic · " + ps2RendererShort(r) + " at the last start"
	}
	return "Automatic · Vulkan where RetroArch and the firmware have it, else OpenGL ES"
}

func init() {
	gameLaunchRegisterPrepare(ps2LaunchPrepare)
}
