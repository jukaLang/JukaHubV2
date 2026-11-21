package main

// ps2_video.go — which renderer the ARMSX2 core draws with on the handheld
// (ps2.go).
//
// ARMSX2 draws through the GPU context RetroArch shares with it: Vulkan by
// default, OpenGL (OpenGL ES on these GPUs) or its software renderer when the
// "GS Renderer" core option (armsx2_renderer) says so; the option takes effect
// at the next start. The Smart Pro's, Brick's and Hammer's PowerVR GE8300 has
// no Vulkan driver in the firmware, so the core's first start there gets
// OpenGL (ps2BoostOptions). Which renderer a TrimUI RetroArch can really give
// it is a property of that build and the firmware's GPU libraries, which
// nothing on the card states. So a start that stops at once, whose RetroArch
// log shows the renderer or its GPU context failing (ps2VideoFailureLine),
// moves armsx2_renderer to the next one for the next start — Vulkan only where
// the firmware has a Vulkan driver, then OpenGL, then Software (slow, but it
// draws anywhere RetroArch can show a picture) — in the options file RetroArch
// reads for the core, and the card says so first (gamediag.go). Each renderer
// is tried once per core build; when all failed, the card says this RetroArch
// cannot draw for ARMSX2.

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ps2RendererChain is the order the renderers are tried in on this device.
func ps2RendererChain() []string {
	if dolphinVulkanPresent() {
		return []string{ps2RendererVulkan, ps2RendererGL, ps2RendererSoftware}
	}
	return []string{ps2RendererGL, ps2RendererSoftware}
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
	}
	return r
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

// ps2AfterGame is the reaper's hook for a start in the ARMSX2 core that did
// not get going (gamediag.go calls it only for one that failed): when
// RetroArch's log for it shows the renderer failing to draw, the next start
// uses the next renderer, and the issue says so. ok is false for every other
// start. Reaper goroutine.
func ps2AfterGame(game libraryGame, launcher, logPath string) (issue launchIssue, ok bool) {
	if ps2SoC() == "" || strings.TrimSpace(logPath) == "" || !ps2LauncherPlays(launcher, game.System) {
		return launchIssue{}, false
	}
	line := ps2VideoFailureIn(logPath)
	if line == "" {
		return launchIssue{}, false
	}
	quoted := "its log: \"" + line + "\""
	optPath := ps2OptionsPath(launcher, game.Path)
	if optPath == "" {
		return launchIssue{
			Level:  int(gameDiagLikely),
			Title:  "RetroArch could not draw for ARMSX2",
			Detail: "RetroArch stopped before the game drew (" + quoted + "). Set ARMSX2's renderer to OpenGL or Software in RetroArch (Quick Menu → Core Options → GS Renderer), then start the game again.",
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
	core := launcher
	if !ps2IsCore(core) {
		core = ps2FindCore()
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
	for _, r := range ps2RendererChain() {
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

// ps2VideoFailedStart reports whether a start in the ARMSX2 core stopped with
// the renderer failing to draw. The core build is not what failed then, so
// gamediag.go keeps such a start out of a new build's probation and lets
// ps2AfterGame switch the renderer instead. Reaper goroutine.
func ps2VideoFailedStart(game libraryGame, launcher, logPath string) bool {
	return ps2SoC() != "" && strings.TrimSpace(logPath) != "" &&
		ps2LauncherPlays(launcher, game.System) && ps2VideoFailureIn(logPath) != ""
}

// ps2VideoFailureIn is the last line of the end of RetroArch's log at path
// that shows the renderer failing (the one nearest the stop), trimmed for the
// card; "" when there is none.
func ps2VideoFailureIn(path string) string {
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
		if !ps2VideoFailureLine(line) {
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
// dolphinVideoFailureLine reads them, and PCSX2's "Failed to create render
// device" and the like from the core.
func ps2VideoFailureLine(line string) bool {
	if dolphinVideoFailureLine(line) {
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
