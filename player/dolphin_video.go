package main

// dolphin_video.go — how the Dolphin core draws on the handheld (dolphin.go).
//
// First start: the core gets the handheld's settings, once, through the seed
// every core started directly gets (emuCoreChoiceSeedOptions writes the core's
// .opt only while it has none, so whatever a player sets later stays). Dolphin
// compiles a shader the first time each effect appears; on the Mali-G57 MC1
// and the PowerVR GE8300 the default (wait for it) is a stall at every new
// effect, and ubershaders cost these GPUs more than they save. So shaders
// compile in the background and an object is skipped for the frame or two its
// shader takes. Builds since the 2025 rebase take numbers for these options,
// in the order of Dolphin's own ShaderCompilationMode (0 synchronous, 1
// synchronous ubershaders, 2 asynchronous ubershaders, 3 asynchronous with
// skipped drawing); a value a build does not offer is dropped by RetroArch,
// which then uses the core's default.
//
// The video driver: the core asks RetroArch for the kind of GPU context its
// driver has (OpenGL, OpenGL ES 3 or Vulkan). Which of them a TrimUI
// RetroArch can give it is a property of that build and its firmware's GPU
// libraries, which nothing on the card states. A start that stops at once,
// whose RetroArch log shows the video driver failing to draw for the core
// (dolphinVideoFailureLine), gets Dolphin's own RetroArch override
// (config/dolphin-emu/dolphin-emu.cfg) switched to the other driver for the
// next start — Vulkan only where the firmware has a Vulkan loader and driver
// — and the card says so first (gamediag.go). Each driver is tried once per
// core build; when both failed, the override JukaHub wrote goes and the card
// says this RetroArch cannot draw for Dolphin. An override the player wrote
// themselves is only ever changed by a failure of the driver it names.

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	// dolphinCoreConfigName is the core's library name: RetroArch's folder
	// for its options and override (config/dolphin-emu/dolphin-emu.opt,
	// .cfg).
	dolphinCoreConfigName = "dolphin-emu"
	// RetroArch's video driver names.
	dolphinVideoGL     = "gl"
	dolphinVideoVulkan = "vulkan"
	// dolphinLogTail is how much of the end of RetroArch's log is read.
	dolphinLogTail = int64(512 << 10)
)

// dolphinBoostOptions adds the handheld's Dolphin settings to options
// (addSystemBoostOptions, compat.go).
func dolphinBoostOptions(options map[string]string) {
	options["dolphin_shader_compilation_mode"] = "3"
	options["dolphin_wait_for_shaders"] = "disabled"
}

// dolphinVideoLabel names a driver for the card.
func dolphinVideoLabel(driver string) string {
	switch driver {
	case dolphinVideoVulkan:
		return "Vulkan"
	case dolphinVideoGL:
		return "OpenGL ES"
	}
	return "\"" + driver + "\""
}

// dolphinVideoFamily folds RetroArch's video_driver into the two Dolphin can
// draw with, gl (RetroArch's default when the key is absent) and vulkan, or
// the driver itself for one Dolphin cannot use (sdl2, drm, fbdev…).
func dolphinVideoFamily(driver string) string {
	switch d := strings.ToLower(strings.TrimSpace(driver)); d {
	case dolphinVideoVulkan:
		return dolphinVideoVulkan
	case "", dolphinVideoGL, "glcore", "gl1", "gles", "gles2", "gles3":
		return dolphinVideoGL
	default:
		return d
	}
}

// dolphinAfterGame is the reaper's hook for a start in the Dolphin core that
// did not get going (gamediag.go calls it only for one that failed): when
// RetroArch's log for it shows the video driver failing to draw, the next
// start uses the other driver, and the issue says so. ok is false for every
// other start. Reaper goroutine.
func dolphinAfterGame(game libraryGame, launcher, logPath string) (issue launchIssue, ok bool) {
	if dolphinSoC() == "" || strings.TrimSpace(logPath) == "" || !dolphinLauncherPlays(launcher, game.System) {
		return launchIssue{}, false
	}
	line := dolphinVideoFailureIn(logPath)
	if line == "" {
		return launchIssue{}, false
	}
	core := launcher
	if !dolphinIsCore(core) {
		core = dolphinFindCore()
	}
	coreID := dolphinCoreIdentity(core)
	cfgPath, override := dolphinOverridePaths(launcher)
	if cfgPath == "" {
		return launchIssue{}, false
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return launchIssue{}, false
	}
	cfg := string(raw)
	ovr := ""
	if data, err := os.ReadFile(override); err == nil {
		ovr = string(data)
	} else if !os.IsNotExist(err) {
		// There but unreadable (a card error): never written over blind.
		return launchIssue{}, false
	}
	driver, inOverride := raConfigGet(ovr, "video_driver")
	if !inOverride {
		driver, _ = raConfigGet(cfg, "video_driver")
	}
	current := dolphinVideoFamily(driver)
	quoted := "its log: \"" + line + "\""
	if v, set := raConfigGet(cfg, "auto_overrides_enable"); set && raValueEqual(v, "false") {
		return launchIssue{
			Level:  int(gameDiagLikely),
			Title:  "RetroArch's " + dolphinVideoLabel(current) + " could not draw for Dolphin",
			Detail: "RetroArch stopped before the game drew (" + quoted + "). Its overrides are off here (auto_overrides_enable in " + cfgPath + "), so JukaHub cannot give Dolphin a driver of its own: turn RetroArch → Settings → Configuration → Load Override Files Automatically back on, or change RetroArch's video driver (Settings → Drivers → Video) for every core.",
		}, true
	}
	rec := dolphinRecordGet()
	var failed []string
	if rec.VideoCore == coreID {
		failed = rec.VideoFailed // the same build: what it failed with still counts
	}
	if !slices.Contains(failed, current) {
		failed = append(failed, current)
	}
	// The other driver Dolphin draws with: OpenGL ES from Vulkan or from a
	// driver it cannot use at all, Vulkan from OpenGL ES (or after OpenGL ES
	// failed too).
	next := dolphinVideoGL
	if current == dolphinVideoGL || (current != dolphinVideoVulkan && slices.Contains(failed, dolphinVideoGL)) {
		next = dolphinVideoVulkan
	}
	// Only a line JukaHub wrote, still saying what it wrote, is JukaHub's to
	// take back.
	ours := rec.Video != "" && filepath.Clean(rec.VideoPath) == filepath.Clean(override) &&
		inOverride && strings.EqualFold(strings.TrimSpace(driver), rec.Video)
	if slices.Contains(failed, next) || (next == dolphinVideoVulkan && !dolphinVulkanPresent()) {
		// Nothing left to try: RetroArch's own choice comes back.
		if ours {
			if err := dolphinOverrideWrite(override, ovr, ""); err != nil {
				logWarn("emulators", "GameCube: could not take JukaHub's video driver out of %s: %v", override, err)
			}
		}
		dolphinRecordUpdate(func(r *dolphinRecord) {
			r.VideoFailed, r.VideoCore = failed, coreID
			if ours {
				r.Video, r.VideoPath = "", ""
			}
		})
		labels := make([]string, 0, len(failed))
		for _, d := range failed {
			labels = append(labels, dolphinVideoLabel(d))
		}
		tried := strings.Join(labels, " and ")
		logWarn("emulators", "GameCube: RetroArch could not draw for Dolphin with %s (%s)", tried, line)
		return launchIssue{
			Level:  int(gameDiagLikely),
			Title:  "This RetroArch could not draw for Dolphin",
			Detail: "RetroArch stopped before the game drew with " + tried + " (" + quoted + "). Dolphin needs OpenGL ES 3 or Vulkan from RetroArch for this GPU: the firmware's own RetroArch, or a newer build of it, may have them.",
		}, true
	}
	if err := dolphinOverrideWrite(override, ovr, next); err != nil {
		logWarn("emulators", "GameCube: could not set Dolphin's video driver in %s: %v", override, err)
		return launchIssue{}, false
	}
	dolphinRecordUpdate(func(r *dolphinRecord) {
		r.VideoFailed, r.VideoCore = failed, coreID
		r.Video, r.VideoPath = next, filepath.Clean(override)
	})
	logInfo("emulators", "GameCube: %s could not draw for Dolphin (%s); its override %s now says %s", current, line, override, next)
	return launchIssue{
		Level:  int(gameDiagSure),
		Title:  "Dolphin will draw with " + dolphinVideoLabel(next) + " next time",
		Detail: "RetroArch's " + dolphinVideoLabel(current) + " could not draw for Dolphin (" + quoted + "). JukaHub set Dolphin's own RetroArch override (" + override + ") to " + dolphinVideoLabel(next) + ": start the game again.",
	}, true
}

// dolphinOverridePaths is the RetroArch config the launch reads and Dolphin's
// override beside it; "" when no config is readable.
func dolphinOverridePaths(launcher string) (cfgPath, override string) {
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		return "", ""
	}
	cfgPath = targets[0]
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return "", ""
	}
	dir := filepath.Join(raTuneMenuConfigDir(string(raw), cfgPath), dolphinCoreConfigName)
	return cfgPath, filepath.Join(dir, dolphinCoreConfigName+".cfg")
}

// dolphinOverrideWrite sets video_driver in the override (current is its text,
// "" when there is none) to driver, or takes the line out for "". An override
// left with nothing in it is removed.
func dolphinOverrideWrite(path, current, driver string) error {
	var next string
	if driver == "" {
		next, _ = raConfigUnset(current, "video_driver")
		if strings.TrimSpace(next) == "" {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}
	} else {
		next, _ = raConfigSet(current, "video_driver", driver)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return AtomicWrite(path, []byte(next), 0o644)
}

// dolphinVulkanPresent reports whether the firmware has a Vulkan loader and a
// driver manifest for it.
func dolphinVulkanPresent() bool {
	icd := false
	for _, dir := range []string{"/usr/share/vulkan/icd.d", "/etc/vulkan/icd.d", "/usr/local/share/vulkan/icd.d"} {
		if m, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(m) > 0 {
			icd = true
			break
		}
	}
	if !icd {
		return false
	}
	for _, dir := range []string{"/usr/lib", "/usr/lib64", "/lib", "/lib64", "/usr/lib/aarch64-linux-gnu", "/lib/aarch64-linux-gnu", "/usr/trimui/lib"} {
		if regularFile(filepath.Join(dir, "libvulkan.so.1")) {
			return true
		}
	}
	return false
}

// dolphinVideoFailedStart reports whether a start in the Dolphin core stopped
// with RetroArch's video driver failing to draw for it. The core build is not
// what failed then, so gamediag.go keeps such a start out of a new build's
// probation (which would put the old, freezing build back) and lets
// dolphinAfterGame switch the driver instead. Reaper goroutine.
func dolphinVideoFailedStart(game libraryGame, launcher, logPath string) bool {
	return dolphinSoC() != "" && strings.TrimSpace(logPath) != "" &&
		dolphinLauncherPlays(launcher, game.System) && dolphinVideoFailureIn(logPath) != ""
}

// dolphinVideoFailureIn is the last line of the end of RetroArch's log at
// path that shows the video driver failing to draw (the one nearest the
// stop), trimmed for the card; "" when there is none.
func dolphinVideoFailureIn(path string) string {
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
	return dolphinVideoFailureLines(strings.Split(string(data), "\n"))
}

// dolphinVideoSelectedContextLine marks the point after Dolphin has accepted a
// hardware API. Its earlier OpenGL, ES 3.2 and ES 3.1 probes may all have
// failed on a RetroArch that successfully gave it ES 3.0 afterward.
func dolphinVideoSelectedContextLine(line string) bool {
	lower := strings.ToLower(line)
	return strings.Contains(lower, "video - sethwrender - using ") &&
		(strings.Contains(lower, "opengl") || strings.Contains(lower, "vulkan"))
}

// dolphinVideoFailureLines finds the last graphics failure after the core's
// final successful context choice, if any. A rejected candidate before that
// choice is not a failure of the context Dolphin actually used.
func dolphinVideoFailureLines(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if dolphinVideoFailureLine(line) {
			if r := []rune(line); len(r) > 160 {
				line = string(r[:160]) + "…"
			}
			return line
		}
		if dolphinVideoSelectedContextLine(line) {
			return ""
		}
	}
	return ""
}

// dolphinVideoFailureLine reports whether a RetroArch log line says the video
// driver or its GPU context failed. RetroArch refusing the desktop OpenGL
// context Dolphin asks for first is not one: the core then asks for OpenGL ES
// 3, which is the answer that matters.
func dolphinVideoFailureLine(line string) bool {
	l := strings.ToLower(line)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(l, s) {
				return true
			}
		}
		return false
	}
	if !has("fail", "error", "cannot", "can't", "couldn't", "could not", "unable", "not supported", "no suitable") {
		return false
	}
	// A library the core links that is missing is a load failure, whichever
	// driver runs (corefix.go and the diagnosis say so).
	if has("shared object", "dlopen") {
		return false
	}
	if has("compiled against") {
		return has("opengles3", "gles3", "opengles 3", "vulkan")
	}
	if has("swap interval", "swapinterval", "vsync", "shader preset", "video filter") {
		return false // noise a working driver logs too
	}
	return has("vulkan", "hw context", "hw_render", "hw render", "opengl", "gles2", "gles3", "[gl]", "[egl]",
		"eglcreate", "eglinit", "eglmake", "video driver", "video backend", "graphics backend")
}
