package main

// windowsapps_ui.go — the "Windows Apps" overlay.
//
// The panel shows every discovered .exe, says why one cannot launch, and on X
// offers the runtime install (Wine, WestonPack) plus the detected Wine/Box64/
// display status.

import (
	"fmt"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

type windowsAppsPanelState struct {
	open      bool
	cursor    int
	scroll    int
	help      bool
	returnEmu bool
}

var winAppsPanel windowsAppsPanelState

type windowsAppRowHit struct {
	index int
	rect  sdl.Rect
}

var windowsAppRowHits []windowsAppRowHit
var windowsAppPanelRect sdl.Rect

// windowsSetupButtonRect is the tappable "Setup" pill in the panel header, so
// touch-only handheld users can reach the runtime page without a keyboard.
var windowsSetupButtonRect sdl.Rect

func openWindowsAppsPanel(returnEmu bool) {
	qs.open = false
	toolsPanel.open = false
	emuPanel.open = false
	gpPanel.open = false
	raPanel.open = false
	portsPanel = portsPanelState{}
	winAppsPanel = windowsAppsPanelState{open: true, returnEmu: returnEmu}
	windowsAppPanelRect = sdl.Rect{}
	windowsAppRowHits = nil
	windowsSetupButtonRect = sdl.Rect{}
	windowsSetupRowHit = [windowsSetupRowCount]sdl.Rect{}
	setupCursor = windowsSetupRowWine
	invalidateWindowsAppsCache()
	PlayActivateSound()
}

func closeWindowsAppsPanel() {
	if !winAppsPanel.open {
		return
	}
	returnEmu := winAppsPanel.returnEmu
	winAppsPanel = windowsAppsPanelState{}
	windowsAppPanelRect = sdl.Rect{}
	windowsAppRowHits = nil
	windowsSetupButtonRect = sdl.Rect{}
	if returnEmu {
		emuPanel.open = true
		emuPanelRect = sdl.Rect{}
		emuRowHits = nil
	}
	PlayBackSound()
}

func windowsAppsPanelMove(delta int) {
	entries := windowsAppEntries()
	if len(entries) == 0 {
		return
	}
	winAppsPanel.cursor += delta
	if winAppsPanel.cursor < 0 {
		winAppsPanel.cursor = len(entries) - 1
	}
	if winAppsPanel.cursor >= len(entries) {
		winAppsPanel.cursor = 0
	}
	PlayNavSound()
}

func setWindowsSetupCursor(row int) {
	if row < 0 {
		row = windowsSetupRowCount - 1
	}
	if row >= windowsSetupRowCount {
		row = 0
	}
	if row != setupCursor {
		PlayNavSound()
	}
	setupCursor = row
}

func setupMove(delta int) {
	setWindowsSetupCursor(setupCursor + delta)
}

// setupActivate runs the install action for the focused setup row.
func setupActivate() {
	switch setupCursor {
	case windowsSetupRowDisplay:
		windowsAppsPanelInstallDisplay()
	default:
		windowsAppsPanelInstall()
	}
}

// windowsAppsPanelInstall starts the on-device runtime install. The setup
// page keeps rendering while it runs, with the current step on its top line.
func windowsAppsPanelInstall() {
	if windowsRuntimeInstalling() {
		ShowToast("The runtime installer is already running", ToastKindInfo)
		return
	}
	if err := startWindowsRuntimeInstall(); err != nil {
		ShowToast("Could not start the installer: "+err.Error(), ToastKindError)
		return
	}
	PlaySuccessSound()
	setWindowsSetupCursor(windowsSetupRowWine)
}

// windowsAppsPanelInstallDisplay downloads WestonPack, the display bridge Wine
// needs on a handheld with no X11 or Wayland session.
func windowsAppsPanelInstallDisplay() {
	if !beginWindowsRuntimeInstall("weston", "contacting PortMaster for WestonPack") {
		ShowToast("The runtime installer is already running", ToastKindInfo)
		return
	}
	go func() {
		err := installWestonRuntime()
		endWindowsRuntimeInstall()
		invalidateWindowsAppsCache()
		if err != nil {
			setWindowsRuntimeStatus("WestonPack not installed", err.Error(), false)
			ShowToast(err.Error(), ToastKindWarning)
			return
		}
		setWindowsRuntimeStatus("WestonPack installed", "", false)
		ShowToast("WestonPack installed", ToastKindSuccess)
	}()
	PlayActivateSound()
}

func windowsAppsPanelRescan() {
	invalidateWindowsAppsCache()
	entries := windowsAppEntries()
	if winAppsPanel.cursor >= len(entries) {
		winAppsPanel.cursor = len(entries) - 1
	}
	if winAppsPanel.cursor < 0 {
		winAppsPanel.cursor = 0
	}
	runtime := currentWindowsRuntimeStatus()
	detail := fmt.Sprintf("Found %d app(s)", len(entries))
	if !runtime.ready() {
		detail += " · runtime setup needed"
	}
	ShowToast(detail, ToastKindInfo)
	PlayNavSound()
}

func windowsAppsPanelActivate() {
	entries := windowsAppEntries()
	if len(entries) == 0 {
		ShowToast("No .exe files found — press X for setup", ToastKindInfo)
		return
	}
	if winAppsPanel.cursor < 0 || winAppsPanel.cursor >= len(entries) {
		return
	}
	entry := entries[winAppsPanel.cursor]
	if err := launchWindowsApp(entry); err != nil {
		showLaunchFailureDialog(entry.Title, err, "")
		return
	}
	winAppsPanel.open = false
	windowsAppPanelRect = sdl.Rect{}
	windowsAppRowHits = nil
	PlaySuccessSound()
	ShowToast("Launching "+entry.Title, ToastKindSuccess)
}

func windowsAppsPanelToggleHelp() {
	winAppsPanel.help = !winAppsPanel.help
	if winAppsPanel.help {
		PlayActivateSound()
		return
	}
	PlayBackSound()
}

func ctrlWindowsApps(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if winAppsPanel.help {
			setupMove(-1)
		} else {
			windowsAppsPanelMove(-1)
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if winAppsPanel.help {
			setupMove(1)
		} else {
			windowsAppsPanelMove(1)
		}
	case sdl.CONTROLLER_BUTTON_A:
		if winAppsPanel.help {
			setupActivate()
		} else {
			windowsAppsPanelActivate()
		}
	case sdl.CONTROLLER_BUTTON_X:
		windowsAppsPanelToggleHelp()
	case sdl.CONTROLLER_BUTTON_Y:
		if !winAppsPanel.help {
			windowsAppsPanelRescan()
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		if winAppsPanel.help {
			windowsAppsPanelToggleHelp()
		} else {
			closeWindowsAppsPanel()
		}
	}
	return true
}

func handleWindowsAppsKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !winAppsPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		if winAppsPanel.help {
			setupMove(-1)
		} else {
			windowsAppsPanelMove(-1)
		}
	case sdl.K_DOWN:
		if winAppsPanel.help {
			setupMove(1)
		} else {
			windowsAppsPanelMove(1)
		}
	case sdl.K_RETURN, sdl.K_SPACE:
		if winAppsPanel.help {
			setupActivate()
		} else {
			windowsAppsPanelActivate()
		}
	case sdl.K_x, sdl.K_h:
		windowsAppsPanelToggleHelp()
	case sdl.K_y, sdl.K_r:
		if !winAppsPanel.help {
			windowsAppsPanelRescan()
		}
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		if winAppsPanel.help {
			windowsAppsPanelToggleHelp()
		} else {
			closeWindowsAppsPanel()
		}
	}
	return true
}

func handleWindowsAppsMouse(mx, my int32, config *Config) bool {
	if !winAppsPanel.open {
		return false
	}
	if !winAppsPanel.help && windowsSetupButtonRect.W > 0 && pointInRect(mx, my, windowsSetupButtonRect) {
		windowsAppsPanelToggleHelp()
		return true
	}
	if winAppsPanel.help {
		for i, r := range windowsSetupRowHit {
			if r.W > 0 && pointInRect(mx, my, r) {
				setupCursor = i
				setupActivate()
				return true
			}
		}
		if windowsAppPanelRect.W > 0 && pointInRect(mx, my, windowsAppPanelRect) {
			return true
		}
		windowsAppsPanelToggleHelp()
		return true
	}
	for _, hit := range windowsAppRowHits {
		if pointInRect(mx, my, hit.rect) {
			winAppsPanel.cursor = hit.index
			windowsAppsPanelActivate()
			return true
		}
	}
	if windowsAppPanelRect.W > 0 && pointInRect(mx, my, windowsAppPanelRect) {
		return true
	}
	closeWindowsAppsPanel()
	return true
}

// drawWindowsSetupButton paints the header "Setup" pill and records its hit
// rect. It is omitted once the setup page itself is showing.
func drawWindowsSetupButton(renderer *sdl.Renderer, config *Config, font *ttf.Font, panelX, panelY, panelW int32) {
	if renderer == nil || font == nil {
		windowsSetupButtonRect = sdl.Rect{}
		return
	}
	label := "Setup"
	w, _, _ := font.SizeUTF8(label)
	pad := int32(12)
	r := sdl.Rect{
		X: panelX + panelW - 24 - int32(w) - pad*2,
		Y: panelY + 14,
		W: int32(w) + pad*2,
		H: int32(font.Height()) + 10,
	}
	windowsSetupButtonRect = r
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, WithAlpha(ColorSurfaceRow, 200))
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, 1, ColorBorderDefault)
	renderText(renderer, config, font, label, ColorTextSecondary(), r.X+pad, r.Y+5)
}

func windowsAppsSummary(entries []windowsAppEntry, runtime windowsRuntimeStatus) string {
	parts := []string{fmt.Sprintf("%d app(s)", len(entries))}
	if len(entries) == 1 {
		parts[0] = "1 app"
	}
	if runtime.ready() {
		parts = append(parts, "Wine ready")
		if runtime.Display != "" {
			parts = append(parts, runtime.displayLabel())
		}
		if runtime.DXVK != "" {
			if runtime.Vulkan {
				parts = append(parts, "DXVK")
			} else {
				parts = append(parts, "DXVK needs Vulkan")
			}
		}
	} else {
		parts = append(parts, "runtime setup needed")
	}
	return strings.Join(parts, " · ")
}

// Windows Apps setup actions. They are the only focusable rows on the setup
// page; everything below them is status text.
const (
	windowsSetupRowWine    = 0
	windowsSetupRowDisplay = 1
	windowsSetupRowCount   = 2
)

var (
	setupCursor        int
	windowsSetupRowHit [windowsSetupRowCount]sdl.Rect
)

type windowsSetupRow struct {
	Title  string
	Detail string
	Status string
	Bad    bool
}

// windowsAppsSetupRows builds the two install actions plus the detected
// runtime status. The actions come first so the panel answers "what do I press"
// before it explains the environment.
func windowsAppsSetupRows(runtime windowsRuntimeStatus) []windowsSetupRow {
	wineStatus, wineBad := "missing", true
	if runtime.Wine != "" {
		wineStatus, wineBad = "installed", false
	}
	displayStatus, displayBad := "missing", true
	if runtime.Weston != "" {
		displayStatus, displayBad = "installed", false
	}
	if target := windowsRuntimeInstallTarget(); target != "" {
		if target == "wine" {
			wineStatus, wineBad = "installing", false
		} else {
			displayStatus, displayBad = "installing", false
		}
	} else if windowsWineInstallFailed() {
		wineStatus = "failed"
	} else if strings.HasPrefix(windowsRuntimeInstallLine(), "Installer: Wine installed") {
		// Extracted but not yet reported by the resolver (for example no
		// display server); keep the action available for a re-install.
		wineStatus = "reinstall"
	}
	if displayStatus == "missing" && windowsWestonInstallFailed() {
		displayStatus = "failed"
	}

	rows := []windowsSetupRow{
		{
			Title:  "Install Wine (Hangover ARM64)",
			Detail: "Download and unpack Hangover into data/windows-runtime (~2.5 GB).",
			Status: wineStatus, Bad: wineBad,
		},
		{
			Title:  "Install display runtime (WestonPack)",
			Detail: "A stock TrimUI has no X11 or Wayland; Wine needs this bridge.",
			Status: displayStatus, Bad: displayBad,
		},
	}
	return rows
}

func windowsAppsSetupLines(runtime windowsRuntimeStatus) []string {
	wine := runtime.Wine
	if wine == "" {
		wine = "not found"
	}
	box := runtime.Box64
	if box == "" {
		box = "not needed"
	}
	display := runtime.displayLabel()
	if display == "" {
		display = "not available"
	}
	weston := runtime.Weston
	if weston == "" {
		weston = "not found"
	}
	dxvk := runtime.DXVK
	if dxvk == "" {
		dxvk = "not found (Wine's built-in D3D is used)"
	}
	vulkan := "not found"
	if runtime.Vulkan {
		vulkan = "ready"
	}
	lines := []string{
		"Wine: " + wine,
		"Box64: " + box,
		"Display: " + display,
		"WestonPack: " + weston,
		"DXVK: " + dxvk,
		"Vulkan: " + vulkan,
		"",
		"Copy .exe files to data/windows-apps or /mnt/SDCARD/Roms/PC.",
		"For Direct3D, put DXVK in data/windows-runtime/dxvk (or set",
		"JUKAHUB_DXVK). It is enabled only when a Vulkan loader is present.",
		"Basic Win32 windows work without DXVK; games still need Vulkan 1.3.",
	}
	if line := windowsRuntimeInstallLine(); line != "" {
		lines = append(lines, "", line)
	}
	if runtime.Problem != "" {
		lines = append([]string{"Missing: " + runtime.Problem, ""}, lines...)
	}
	return lines
}

// renderWindowsAppsPanel draws the launcher or its setup page. Row hits are
// rebuilt every frame so mouse and touch cannot drift from the visible list.
func renderWindowsAppsPanel(renderer *sdl.Renderer, config *Config) {
	if !winAppsPanel.open || renderer == nil {
		return
	}
	entries := windowsAppEntries()
	runtime := currentWindowsRuntimeStatus()
	if winAppsPanel.cursor >= len(entries) {
		winAppsPanel.cursor = len(entries) - 1
	}
	if winAppsPanel.cursor < 0 {
		winAppsPanel.cursor = 0
	}

	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	const padX = int32(24)
	rowH := 8 + medH + 4 + smallH + 8
	headerH := 18 + medH + 4 + smallH + 14
	footerH := 20 + smallH

	panelW := int32(900)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}

	var bodyH int32
	var visible int32
	setupLines := windowsAppsSetupLines(runtime)
	if winAppsPanel.help {
		bodyH = rowH*2 + 12 + int32(len(setupLines))*(smallH+4) + 12
	} else if len(entries) == 0 {
		bodyH = 18 + medH + 4 + smallH + 4 + smallH + 4 + smallH + 18
	} else {
		maxRows := (screenHeight - 40 - headerH - footerH) / rowH
		if maxRows < 3 {
			maxRows = 3
		}
		visible = int32(len(entries))
		if visible > maxRows {
			visible = maxRows
		}
		bodyH = visible * rowH
	}
	panelH := headerH + bodyH + footerH
	if panelH > screenHeight-24 {
		panelH = screenHeight - 24
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	windowsAppPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	if !winAppsPanel.help && len(entries) > 0 {
		if winAppsPanel.cursor < winAppsPanel.scroll {
			winAppsPanel.scroll = winAppsPanel.cursor
		}
		if winAppsPanel.cursor >= winAppsPanel.scroll+int(visible) {
			winAppsPanel.scroll = winAppsPanel.cursor - int(visible) + 1
		}
		if winAppsPanel.scroll > len(entries)-int(visible) {
			winAppsPanel.scroll = len(entries) - int(visible)
		}
		if winAppsPanel.scroll < 0 {
			winAppsPanel.scroll = 0
		}
	} else {
		winAppsPanel.scroll = 0
	}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	title := "Windows Apps"
	if winAppsPanel.help {
		title = "Windows Apps · Setup"
	}
	renderText(renderer, config, medium, title, ColorTextPrimary(), panelX+padX, panelY+18)
	summary := windowsAppsSummary(entries, runtime)
	summaryColor := ColorTextTertiary()
	if !runtime.ready() {
		summaryColor = ColorWarning
	}
	summaryMax := panelW - 2*padX
	if !winAppsPanel.help {
		// Leave room for the header Setup pill.
		summaryMax -= 90
	}
	renderText(renderer, config, font, ellipsize(font, summary, summaryMax),
		summaryColor, panelX+padX, panelY+18+medH+4)

	if winAppsPanel.help {
		windowsAppRowHits = nil
		windowsSetupButtonRect = sdl.Rect{}
		focusColor := installHighlight()
		rowW := panelW - 2*padX
		if setupCursor < 0 || setupCursor >= windowsSetupRowCount {
			setupCursor = windowsSetupRowWine
		}
		rows := windowsAppsSetupRows(runtime)
		y := panelY + headerH
		for i, row := range rows {
			r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
			windowsSetupRowHit[i] = r
			focused := i == setupCursor
			rowBg := WithAlpha(ColorSurfaceRow, 190)
			if focused {
				rowBg = WithAlpha(focusColor, 46)
			}
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
			if focused {
				strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
			}
			renderText(renderer, config, medium, row.Title, ColorTextPrimary(), r.X+16, r.Y+8)
			statusColor := ColorToastSuccess
			if row.Bad {
				statusColor = ColorToastError
			} else if row.Status == "installing" {
				statusColor = ColorWarning
			}
			sw, _, _ := font.SizeUTF8(row.Status)
			renderText(renderer, config, font, row.Status, statusColor, r.X+r.W-16-int32(sw), r.Y+8+(medH-smallH)/2)
			renderText(renderer, config, font, ellipsize(font, row.Detail, r.W-32),
				ColorTextTertiary(), r.X+16, r.Y+8+medH+4)
			y += rowH
		}
		y += 12
		for _, line := range setupLines {
			if y+smallH > panelY+panelH-footerH {
				break
			}
			color := ColorTextSecondary()
			switch {
			case strings.HasPrefix(line, "Missing:"):
				color = ColorToastError
			case strings.HasPrefix(line, "Installer:") && strings.Contains(line, "failed"):
				color = ColorToastError
			case strings.HasPrefix(line, "Installer:"):
				color = ColorWarning
			case strings.HasPrefix(line, "Wine:") || strings.HasPrefix(line, "Box64:") ||
				strings.HasPrefix(line, "Display:") || strings.HasPrefix(line, "WestonPack:") ||
				strings.HasPrefix(line, "DXVK:") || strings.HasPrefix(line, "Vulkan:"):
				color = ColorTextTertiary()
			}
			if line != "" {
				renderText(renderer, config, font, ellipsize(font, line, panelW-2*padX),
					color, panelX+padX, y)
			}
			y += smallH + 4
		}
	} else if len(entries) == 0 {
		windowsAppRowHits = nil
		drawWindowsSetupButton(renderer, config, font, panelX, panelY, panelW)
		y := panelY + headerH
		renderText(renderer, config, medium, "No Windows apps yet", ColorTextPrimary(), panelX+padX, y)
		y += medH + 4
		renderText(renderer, config, font, "Copy Windows apps into:", ColorTextSecondary(), panelX+padX, y)
		y += smallH + 4
		for i, root := range windowsAppRootsCached() {
			if i >= 2 {
				break
			}
			renderText(renderer, config, font, ellipsize(font, root, panelW-2*padX),
				accentColor, panelX+padX, y)
			y += smallH + 4
		}
		renderText(renderer, config, font, "then press Y to rescan, or X for runtime setup.",
			ColorTextTertiary(), panelX+padX, y)
	} else {
		drawWindowsSetupButton(renderer, config, font, panelX, panelY, panelW)
		focusColor := installHighlight()
		rowW := panelW - 2*padX
		windowsAppRowHits = make([]windowsAppRowHit, 0, visible)
		for i := int32(0); i < visible; i++ {
			idx := winAppsPanel.scroll + int(i)
			entry := entries[idx]
			y := panelY + headerH + i*rowH
			r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
			windowsAppRowHits = append(windowsAppRowHits, windowsAppRowHit{index: idx, rect: r})

			focused := idx == winAppsPanel.cursor
			rowBg := WithAlpha(ColorSurfaceRow, 190)
			if focused {
				rowBg = WithAlpha(focusColor, 46)
			}
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
			if focused {
				strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
			}

			renderText(renderer, config, medium, entry.Title, ColorTextPrimary(), r.X+16, r.Y+8)
			status := windowsAppStatus(entry, runtime)
			statusColor := ColorToastSuccess
			if status == "Unavailable" {
				statusColor = ColorToastError
			} else if status == "Running" {
				statusColor = accentColor
			}
			sw, _, _ := font.SizeUTF8(status)
			renderText(renderer, config, font, status, statusColor, r.X+r.W-16-int32(sw), r.Y+8+(medH-smallH)/2)

			detail := windowsAppDetail(entry)
			detailColor := ColorTextTertiary()
			if !runtime.ready() {
				detailColor = ColorToastError
			}
			renderText(renderer, config, font, ellipsize(font, detail, r.W-32),
				detailColor, r.X+16, r.Y+8+medH+4)
		}
	}

	backLabel := "Back"
	if winAppsPanel.returnEmu {
		backLabel = "Emulators"
	}
	var hint string
	switch {
	case winAppsPanel.help:
		hint = hintLine(FooterHint{"A", "Install Runtime"}, FooterHint{"B", "Back"})
	case len(entries) == 0:
		hint = hintLine(FooterHint{"X", "Setup"}, FooterHint{"Y", "Rescan"}, FooterHint{"B", backLabel})
	default:
		hint = hintLine(FooterHint{"A", "Launch"}, FooterHint{"X", "Setup"},
			FooterHint{"Y", "Rescan"}, FooterHint{"B", backLabel})
	}
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+padX, panelY+panelH-footerH+8)
}
