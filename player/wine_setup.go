package main

// wine_setup.go — what the Windows Apps setup page says about the runtime.
//
// The page is drawn every frame and its facts read files (the pack's
// wine.json, the Wine marker, WestonPack's header), so they are gathered at
// most once a second here; the install state (installing, failed) is read
// fresh each frame on top. The rows are the three things the chain installs:
// the runtime (which also brings the display and prepares Windows), the
// display alone, and DXVK, which is optional and, on a Vulkan 1.1 handheld,
// an experiment the user has to confirm.

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// windowsSetupBase is the cached part of the setup page.
type windowsSetupBase struct {
	facts  wineRuntimeFacts
	weston westonPackSource
	rows   []windowsSetupRow
	lines  []string
}

var windowsSetupCache struct {
	mu   sync.Mutex
	at   time.Time
	base windowsSetupBase
}

// windowsSetupSnapshot returns the setup page's facts, at most a second old.
func windowsSetupSnapshot() windowsSetupBase {
	windowsSetupCache.mu.Lock()
	defer windowsSetupCache.mu.Unlock()
	if windowsSetupCache.at.IsZero() || time.Since(windowsSetupCache.at) > time.Second {
		windowsSetupCache.base = buildWindowsSetupBase()
		windowsSetupCache.at = time.Now()
	}
	return windowsSetupCache.base
}

// invalidateWindowsSetupSnapshot makes the next frame read the facts again
// (after an install finished).
func invalidateWindowsSetupSnapshot() {
	windowsSetupCache.mu.Lock()
	windowsSetupCache.at = time.Time{}
	windowsSetupCache.mu.Unlock()
}

// windowsDesktopSession reports an X11 or Wayland session JukaHub runs in.
func windowsDesktopSession() bool {
	return strings.TrimSpace(os.Getenv("DISPLAY")) != "" || strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) != ""
}

func buildWindowsSetupBase() windowsSetupBase {
	if IsWindows() {
		return windowsSetupBase{
			facts: wineRuntimeFacts{Flavor: "Windows"},
			rows: []windowsSetupRow{
				{Title: "Windows runs .exe files itself", Detail: "Nothing to install on a PC.", Status: "not needed"},
				{Title: "Display", Detail: "Programs open in their own windows.", Status: "not needed"},
				{Title: "DXVK", Detail: "Windows has Direct3D already.", Status: "not needed"},
			},
			lines: []string{"On a PC, Windows Apps opens .exe files directly: nothing needs installing."},
		}
	}
	facts := currentWineRuntimeFacts()
	weston := findWestonPackSource()
	dxvk, _ := resolveDXVK()
	vulkan := resolveVulkan() != ""
	base := windowsSetupBase{facts: facts, weston: weston}
	base.rows = []windowsSetupRow{
		windowsRuntimeSetupRow(facts),
		windowsDisplaySetupRow(weston),
		windowsDXVKSetupRow(dxvk, vulkan),
	}
	base.lines = windowsSetupFactLines(facts, weston, dxvk, vulkan)
	return base
}

// windowsRuntimeSetupRow is the first row: the runtime, which runs the whole
// chain (runtime, display, prepared Windows).
func windowsRuntimeSetupRow(facts wineRuntimeFacts) windowsSetupRow {
	row := windowsSetupRow{Title: "Install Windows runtime (" + windowsRuntimeLabel() + ")"}
	switch {
	case facts.Flavor != "":
		row.Title = "Windows runtime (" + facts.Flavor + ")"
		row.Status = "installed"
		switch {
		case facts.PackTag != "":
			row.Detail = "Box64 from JukaHub " + facts.PackTag
		case facts.Wine != "":
			row.Detail = facts.Wine
		}
		if !facts.PrefixReady && facts.PrefixProblem == "" {
			row.Detail = strings.TrimPrefix(row.Detail+" · press A to prepare Windows now (a few minutes)", " · ")
		}
	case winePackSupported():
		row.Detail = "Downloads about 130 MB (Box64, Wine 10.0, WestonPack); needs about 700 MB free."
		row.Status, row.Bad = "missing", true
	case wineNativeSupported():
		row.Detail = "Downloads about 62 MB (Wine 10.0 from GitHub); needs about 700 MB free."
		row.Status, row.Bad = "missing", true
	default:
		row.Title = "Windows runtime"
		row.Detail = "JukaHub has no Windows runtime for this device."
		row.Status, row.Bad = "unavailable", true
	}
	return row
}

// windowsDisplaySetupRow is the WestonPack row.
func windowsDisplaySetupRow(weston westonPackSource) windowsSetupRow {
	switch {
	case windowsDesktopSession():
		return windowsSetupRow{Title: "Display (this desktop)", Detail: "Wine opens its windows on this computer's desktop.", Status: "not needed"}
	case weston.Wrapper != "":
		return windowsSetupRow{Title: "Display runtime (WestonPack)", Detail: weston.Wrapper, Status: "installed"}
	case weston.Image != "":
		status := "installed"
		if strings.Contains(weston.Image, "PortMaster") {
			status = "from PortMaster"
		}
		return windowsSetupRow{Title: "Display runtime (WestonPack)", Detail: weston.Image, Status: status}
	case winePackSupported():
		return windowsSetupRow{Title: "Install display runtime (WestonPack)", Detail: "No X11 or Wayland here: WestonPack (about 53 MB, from PortMaster) shows Windows programs.", Status: "missing", Bad: true}
	}
	return windowsSetupRow{Title: "Display", Detail: "Start JukaHub inside an X11 or Wayland desktop to show Windows programs.", Status: "missing", Bad: true}
}

// windowsDXVKSetupRow is the optional DXVK row.
func windowsDXVKSetupRow(dxvk string, vulkan bool) windowsSetupRow {
	major, minor, known := wineVulkanLevel()
	switch {
	case dxvk != "":
		row := windowsSetupRow{Title: "DXVK (Direct3D over Vulkan)", Status: "on"}
		version := dxvkVersionAt(dxvk)
		if version == "" {
			version = "of unknown version"
		}
		if ok, why := dxvkUsableHere(dxvk); ok {
			row.Detail = "DXVK " + version + " runs Direct3D for every launch."
		} else {
			row.Detail = "Off: " + why + "."
			row.Status = "off"
		}
		return row
	case !vulkan:
		return windowsSetupRow{Title: "DXVK (Direct3D over Vulkan)", Detail: "Needs a Vulkan driver, and this device has none; Wine's own Direct3D is used.", Status: "not needed"}
	case dxvkLegacyOnly():
		return windowsSetupRow{
			Title:  "Install DXVK " + windowsDXVKLegacyVersion + " (experiment)",
			Detail: fmt.Sprintf("Vulkan %d.%d here, and DXVK 2 needs 1.3: only this old release can try. Wine's own Direct3D is the default.", major, minor),
			Status: "optional",
		}
	}
	detail := "Faster Direct3D for games, through Vulkan 1.3."
	if known {
		detail = fmt.Sprintf("Faster Direct3D for games; this driver has Vulkan %d.%d.", major, minor)
	}
	return windowsSetupRow{Title: "Install DXVK (Direct3D over Vulkan)", Detail: detail, Status: "optional"}
}

// windowsSetupFactLines are the status lines under the rows.
func windowsSetupFactLines(facts wineRuntimeFacts, weston westonPackSource, dxvk string, vulkan bool) []string {
	runtimeLine := "Runtime: not installed"
	if facts.Flavor != "" {
		runtimeLine = "Runtime: " + facts.Flavor
		if facts.Wine != "" {
			runtimeLine += " · " + facts.Wine
		}
	}
	var folder string
	switch {
	case facts.PrefixProblem != "":
		folder = "Missing: " + facts.PrefixProblem
	case facts.PrefixImage != "":
		folder = "Windows folder: inside " + facts.PrefixImage + " (opened while an app runs)"
	case facts.Prefix != "":
		folder = "Windows folder: " + facts.Prefix
	}
	display := "Display: "
	switch {
	case windowsDesktopSession():
		display += "this desktop"
	case weston.Wrapper != "":
		display += "WestonPack · " + weston.Wrapper
	case weston.Image != "":
		display += "WestonPack · " + weston.Image
	default:
		display += "not available"
	}
	dxvkLine := "DXVK: not installed (Wine's own Direct3D is used)"
	if dxvk != "" {
		dxvkLine = "DXVK: " + dxvk
	}
	vulkanLine := "Vulkan: not found"
	if vulkan {
		vulkanLine = "Vulkan: driver found"
		if major, minor, known := wineVulkanLevel(); known {
			vulkanLine = fmt.Sprintf("Vulkan: %d.%d", major, minor)
		}
	}
	lines := []string{runtimeLine}
	if folder != "" {
		lines = append(lines, folder)
	}
	lines = append(lines, display, dxvkLine, vulkanLine, "",
		"Copy .exe files to data/windows-apps or /mnt/SDCARD/Roms/PC.",
		"The first launch prepares Windows, which takes a few minutes.")
	return lines
}

// windowsDXVKConfirm holds the first press on the experimental DXVK row: a
// second A within ten seconds installs it.
var windowsDXVKConfirm struct {
	mu sync.Mutex
	at time.Time
}

// windowsDXVKConfirmed reports whether this press confirms the experimental
// install, arming the confirmation otherwise.
func windowsDXVKConfirmed() bool {
	windowsDXVKConfirm.mu.Lock()
	defer windowsDXVKConfirm.mu.Unlock()
	if !windowsDXVKConfirm.at.IsZero() && time.Since(windowsDXVKConfirm.at) < 10*time.Second {
		windowsDXVKConfirm.at = time.Time{}
		return true
	}
	windowsDXVKConfirm.at = time.Now()
	return false
}
