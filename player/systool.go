package main

// systool.go — what the Terminal, Scheduled tasks and the system tool screens
// (Speed test, Benchmark, Hardware, Storage, Logs) share: the one place the
// app's click, wheel, footer and scene-entry dispatch hands them their input,
// the in-memory clean-up of the buttons those screens no longer have, and the
// glass pieces their pages are built from.
//
// Each of these screens is a registered scene module that draws its whole page
// and owns its pad and keys. They load on entry, keep the render thread free
// (their work runs as scene tasks, cancelled when the screen is left) and leave
// on B / Escape like every other screen.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// sysToolModuleScenes are the screens that draw their whole page themselves.
// Nothing in their config is drawn, so nothing in it may take focus either.
var sysToolModuleScenes = map[string]bool{
	"Terminal":    true,
	"Cron":        true,
	"NetSpeed":    true,
	"Benchmark":   true,
	"Hardware":    true,
	"DiskSpace":   true,
	"LogExporter": true,
}

// sysToolMigrateScenes drops, in memory, the controls these screens used to
// carry in the config: the Terminal's command field and Run button, the Load /
// Startup buttons and the second "Startup Items" panel of Scheduled tasks, and
// the Run / Refresh / Export / Clear / Processes / Wi-Fi buttons of the tool
// screens. Each screen draws its own controls now, and a button that is not
// drawn but can still take focus is exactly what used to swallow the arrows
// and Enter on the Terminal. A user's own config from an older release is
// cleaned the same way; the file on disk is never rewritten. Panels that are
// not controls (the textlogs, the disk chart) stay, so the web builder still
// previews the screens. Idempotent.
func sysToolMigrateScenes(config *Config) {
	if config == nil {
		return
	}
	for i := range config.Scenes {
		scene := &config.Scenes[i]
		if !sysToolModuleScenes[scene.Name] {
			continue
		}
		kept := scene.Elements[:0]
		for _, el := range scene.Elements {
			if sysToolLegacyElement(scene.Name, el) {
				continue
			}
			kept = append(kept, el)
		}
		scene.Elements = kept
	}
}

// sysToolLegacyElement reports whether el is one of the controls a module
// screen replaced.
func sysToolLegacyElement(scene string, el Element) bool {
	if isFocusableElement(el) {
		return true
	}
	if scene != "Cron" {
		return false
	}
	switch el.Type {
	case "label":
		return strings.EqualFold(strings.TrimSpace(el.Text), "Startup Items")
	case "textlog":
		return el.Variable == cronStartupVar
	}
	return false
}

// sysToolHandleClick gives a left click to the screen that owns it. The top
// bar's band and the menus it opens keep their own handling: a click there
// returns false so the shared handlers run.
func sysToolHandleClick(mx, my int32, config *Config) bool {
	name := sysToolCurrentScene(config)
	if !sysToolModuleScenes[name] {
		return false
	}
	// A sheet a screen opened over itself is modal: it takes every click,
	// the bar's too.
	switch {
	case name == "Terminal" && termHist.open:
		return handleTermMouseClick(mx, my, config)
	case name == schedSceneName && schedUI.sheet:
		return schedHandleClick(mx, my, config)
	case name == "LogExporter" && sysToolLogs.sheet:
		return sysToolLogsClick(mx, my)
	}
	if tb.startOpen || nc.open || my < headerBarHeight() {
		return false
	}
	if activityStripClick(mx, my) {
		return true
	}
	switch name {
	case "Terminal":
		return handleTermMouseClick(mx, my, config)
	case schedSceneName:
		return schedHandleClick(mx, my, config)
	case "NetSpeed":
		return sysToolNetClick(mx, my, config)
	case "Benchmark":
		return sysToolBenchClick(mx, my, config)
	case "Hardware":
		return sysToolHWClick(mx, my, config)
	case "DiskSpace":
		return sysToolDiskClick(mx, my)
	case "LogExporter":
		return sysToolLogsClick(mx, my)
	}
	return false
}

// sysToolHandleWheel scrolls the screen that owns the wheel.
func sysToolHandleWheel(dy int32, config *Config) bool {
	switch sysToolCurrentScene(config) {
	case "Terminal":
		return handleTermWheel(dy)
	case schedSceneName:
		return schedHandleWheel(dy)
	case "LogExporter":
		return sysToolLogsWheel(dy)
	}
	return false
}

// sysToolFooterHints is the footer row for these screens, and reports whether
// the scene is one of them. footerHints (extras.go) asks here first, so the
// footer and the controller legend both show what the screen really does.
func sysToolFooterHints(sceneName string) ([]FooterHint, bool) {
	switch sceneName {
	case "Terminal":
		return termFooterHints(), true
	case schedSceneName:
		return schedFooterHints(), true
	case "NetSpeed":
		return sysToolNetFooterHints(), true
	case "Benchmark":
		return sysToolBenchFooterHints(), true
	case "Hardware":
		return sysToolHWFooterHints(), true
	case "DiskSpace":
		return sysToolDiskFooterHints(), true
	case "LogExporter":
		return sysToolLogsFooterHints(), true
	}
	return nil, false
}

// sysToolSceneEnter starts a screen's own work as it opens: the Scheduled
// tasks lists load, the tools measure. completeSceneTransition (core.go) calls
// it once per visit, after the scene's task scope has started, so everything
// started here stops when the screen is left.
func sysToolSceneEnter(config *Config, name string) {
	switch name {
	case schedSceneName:
		schedEnter(config)
	case "NetSpeed":
		sysToolNetEnter()
	case "Benchmark":
		// The run guards itself: the textlog hook (benchmarkAutoStartOnOpen)
		// starting it too does not start a second pass.
		sysToolBenchEnter(config)
	case "Hardware":
		sysToolHWEnter()
	case "DiskSpace":
		sysToolDiskEnter()
	case "LogExporter":
		sysToolLogsEnter()
	}
}

// sysToolCurrentScene names the scene on screen, or "".
func sysToolCurrentScene(config *Config) string {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return ""
	}
	return config.Scenes[currentSceneIndex].Name
}

// sysToolPointIn reports whether the logical mouse is over r (hover states).
func sysToolPointIn(r sdl.Rect) bool {
	return r.W > 0 && r.H > 0 && pointInRect(mouseX, mouseY, r)
}
