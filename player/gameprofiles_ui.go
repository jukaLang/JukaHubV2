package main

// gameprofiles_ui.go — the Game Profiles overlay.
//
// The panel is the launcher half of gameprofiles.go: it lists every valid
// profile, shows exactly why one is unavailable, launches with A, exports a
// shareable file with X, and rescans import/local with Y. It is reachable from
// Start → Game Profiles and from the pinned row at the top of Emulators.

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

type gameProfilePanelState struct {
	open      bool
	cursor    int
	scroll    int
	returnEmu bool
}

var gpPanel gameProfilePanelState

// gameProfileRowHit maps a click back to a loaded profile index. Only touched
// on the render/event thread.
type gameProfileRowHit struct {
	index int
	rect  sdl.Rect
}

var gameProfileRowHits []gameProfileRowHit
var gameProfilePanelRect sdl.Rect

// openGameProfilePanel shows the overlay and rebuilds the catalog. When
// returnEmu is true, closing returns to the Emulators list.
func openGameProfilePanel(returnEmu bool) {
	qs.open = false
	toolsPanel.open = false
	emuPanel.open = false
	raPanel.open = false
	gpPanel = gameProfilePanelState{open: true, returnEmu: returnEmu}
	gameProfilePanelRect = sdl.Rect{}
	gameProfileRowHits = nil
	invalidateGameProfileCache()
	entries := gameProfileEntries()
	if n := len(entries); n == 0 {
		log.Printf("[gameprofiles] panel opened with no profiles (folder %s)", gameProfilesImportDir())
	}
	PlayActivateSound()
}

func closeGameProfilePanel() {
	if !gpPanel.open {
		return
	}
	returnEmu := gpPanel.returnEmu
	gpPanel = gameProfilePanelState{}
	gameProfilePanelRect = sdl.Rect{}
	gameProfileRowHits = nil
	if returnEmu {
		// The emulator cursor and cache were left intact while profiles were on
		// top, so B lands exactly where the user left the list.
		emuPanel.open = true
		emuPanelRect = sdl.Rect{}
		emuRowHits = nil
	}
	PlayBackSound()
}

func gameProfilePanelMove(delta int) {
	entries := gameProfileEntries()
	if len(entries) == 0 {
		return
	}
	gpPanel.cursor += delta
	if gpPanel.cursor < 0 {
		gpPanel.cursor = len(entries) - 1
	}
	if gpPanel.cursor >= len(entries) {
		gpPanel.cursor = 0
	}
	PlayNavSound()
}

func gameProfilePanelActivate() {
	entries := gameProfileEntries()
	if len(entries) == 0 {
		ShowToast("No game profiles yet — drop JSON into "+gameProfilesImportDir(), ToastKindInfo)
		return
	}
	if gpPanel.cursor < 0 || gpPanel.cursor >= len(entries) {
		return
	}
	entry := entries[gpPanel.cursor]
	if err := launchGameProfile(entry); err != nil {
		log.Printf("[gameprofiles] launch %s failed: %v", entry.Profile.ID, err)
		ShowToast("Could not launch "+entry.Profile.Title+": "+err.Error(), ToastKindError)
		return
	}
	gpPanel.open = false
	gameProfilePanelRect = sdl.Rect{}
	gameProfileRowHits = nil
	PlaySuccessSound()
	ShowToast("Launching "+entry.Profile.Title, ToastKindSuccess)
}

func gameProfilePanelExport() {
	entries := gameProfileEntries()
	if len(entries) == 0 || gpPanel.cursor < 0 || gpPanel.cursor >= len(entries) {
		ShowToast("No profile selected", ToastKindWarning)
		return
	}
	entry := entries[gpPanel.cursor]
	path, err := exportGameProfile(entry)
	if err != nil {
		ShowToast("Export failed: "+err.Error(), ToastKindError)
		return
	}
	PlaySuccessSound()
	ShowToast("Exported "+entry.Profile.Title+" to "+path, ToastKindSuccess)
}

func gameProfilePanelRescan() {
	invalidateGameProfileCache()
	entries := gameProfileEntries()
	if gpPanel.cursor >= len(entries) {
		gpPanel.cursor = len(entries) - 1
	}
	if gpPanel.cursor < 0 {
		gpPanel.cursor = 0
	}
	ready := 0
	for _, entry := range entries {
		if entry.ready() {
			ready++
		}
	}
	ShowToast(fmt.Sprintf("Rescanned %d profile(s) · %d ready", len(entries), ready), ToastKindInfo)
	PlayNavSound()
}

// ctrlGameProfiles is called from the controller overlay priority chain.
func ctrlGameProfiles(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		gameProfilePanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		gameProfilePanelMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		gameProfilePanelActivate()
	case sdl.CONTROLLER_BUTTON_X:
		gameProfilePanelExport()
	case sdl.CONTROLLER_BUTTON_Y:
		gameProfilePanelRescan()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeGameProfilePanel()
	}
	return true
}

// handleGameProfileKey processes keyboard input while the panel is open and
// consumes every key so the scene underneath never reacts behind it.
func handleGameProfileKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !gpPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		gameProfilePanelMove(-1)
	case sdl.K_DOWN:
		gameProfilePanelMove(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		gameProfilePanelActivate()
	case sdl.K_x:
		gameProfilePanelExport()
	case sdl.K_y, sdl.K_r:
		gameProfilePanelRescan()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeGameProfilePanel()
	}
	return true
}

// handleGameProfileMouse activates a clicked row or closes the panel on a
// click outside it. Returns true when the click was consumed.
func handleGameProfileMouse(mx, my int32, config *Config) bool {
	if !gpPanel.open {
		return false
	}
	for _, hit := range gameProfileRowHits {
		if pointInRect(mx, my, hit.rect) {
			gpPanel.cursor = hit.index
			gameProfilePanelActivate()
			return true
		}
	}
	if gameProfilePanelRect.W > 0 && pointInRect(mx, my, gameProfilePanelRect) {
		return true
	}
	closeGameProfilePanel()
	return true
}

func gameProfileStatus(entry gameProfileEntry) (string, sdl.Color) {
	if pid := gameProfileRunningPID(entry.Profile.ID); pid > 0 {
		return "Running", accentColor
	}
	if !entry.ready() {
		return "Unavailable", ColorToastError
	}
	return "Ready", ColorToastSuccess
}

func gameProfileSummary(entries []gameProfileEntry, issues int) string {
	ready := 0
	for _, entry := range entries {
		if entry.ready() {
			ready++
		}
	}
	parts := []string{fmt.Sprintf("%d profile(s)", len(entries)), fmt.Sprintf("%d ready", ready)}
	if unavailable := len(entries) - ready; unavailable > 0 {
		parts = append(parts, fmt.Sprintf("%d unavailable", unavailable))
	}
	if issues > 0 {
		parts = append(parts, fmt.Sprintf("%d file issue(s)", issues))
	}
	return strings.Join(parts, " · ")
}

// gameProfileDetailLine is the second row line: runtime, executable, source
// and the tested firmware, or the exact reason the profile cannot launch.
func gameProfileDetailLine(entry gameProfileEntry) string {
	if entry.Issue != "" {
		return entry.Issue
	}
	parts := []string{gameProfileRuntimeLabel(entry.Profile.Runtime), filepath.ToSlash(entry.Profile.Executable)}
	if entry.Profile.TestedFirmware != "" {
		parts = append(parts, "tested on "+entry.Profile.TestedFirmware)
	}
	switch entry.Source {
	case "local":
		parts = append(parts, "local override")
	case "import":
		parts = append(parts, "imported")
	}
	if entry.Profile.Notes != "" {
		parts = append(parts, entry.Profile.Notes)
	}
	return strings.Join(parts, " · ")
}

// renderGameProfilesPanel draws the modal launcher list. Rows that do not fit
// are scrolled with the cursor.
func renderGameProfilesPanel(renderer *sdl.Renderer, config *Config) {
	if !gpPanel.open || renderer == nil {
		return
	}
	entries := gameProfileEntries()
	issues := len(gameProfileIssues())
	if gpPanel.cursor >= len(entries) {
		gpPanel.cursor = len(entries) - 1
	}
	if gpPanel.cursor < 0 {
		gpPanel.cursor = 0
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

	panelW := int32(880)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	var visible int32
	var bodyH int32
	if len(entries) == 0 {
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
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	gameProfilePanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	if len(entries) > 0 {
		// Keep the cursor inside the visible window.
		if gpPanel.cursor < gpPanel.scroll {
			gpPanel.scroll = gpPanel.cursor
		}
		if gpPanel.cursor >= gpPanel.scroll+int(visible) {
			gpPanel.scroll = gpPanel.cursor - int(visible) + 1
		}
		if gpPanel.scroll > len(entries)-int(visible) {
			gpPanel.scroll = len(entries) - int(visible)
		}
		if gpPanel.scroll < 0 {
			gpPanel.scroll = 0
		}
	} else {
		gpPanel.scroll = 0
	}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	renderText(renderer, config, medium, "Game Profiles", ColorTextPrimary(), panelX+padX, panelY+18)
	renderText(renderer, config, font, gameProfileSummary(entries, issues), ColorTextTertiary(), panelX+padX, panelY+18+medH+4)

	if len(entries) == 0 {
		gameProfileRowHits = nil
		y := panelY + headerH
		renderText(renderer, config, medium, "No profiles yet", ColorTextPrimary(), panelX+padX, y)
		y += medH + 4
		renderText(renderer, config, font, "Drop a profile JSON into:", ColorTextSecondary(), panelX+padX, y)
		y += smallH + 4
		renderText(renderer, config, font, ellipsize(font, gameProfilesImportDir(), panelW-2*padX),
			accentColor, panelX+padX, y)
		y += smallH + 4
		renderText(renderer, config, font, "then press Y to rescan.", ColorTextTertiary(), panelX+padX, y)
	} else {
		focusColor := installHighlight()
		rowW := panelW - 2*padX
		gameProfileRowHits = make([]gameProfileRowHit, 0, visible)
		for i := int32(0); i < visible; i++ {
			idx := gpPanel.scroll + int(i)
			entry := entries[idx]
			y := panelY + headerH + i*rowH
			r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
			gameProfileRowHits = append(gameProfileRowHits, gameProfileRowHit{index: idx, rect: r})

			focused := idx == gpPanel.cursor
			rowBg := WithAlpha(ColorSurfaceRow, 190)
			if focused {
				rowBg = WithAlpha(focusColor, 46)
			}
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
			if focused {
				strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
			}

			renderText(renderer, config, medium, entry.Profile.Title, ColorTextPrimary(), r.X+16, r.Y+8)
			label, labelColor := gameProfileStatus(entry)
			lw, _, _ := font.SizeUTF8(label)
			renderText(renderer, config, font, label, labelColor, r.X+r.W-16-int32(lw), r.Y+8+(medH-smallH)/2)

			detail := gameProfileDetailLine(entry)
			detailColor := ColorTextTertiary()
			if !entry.ready() {
				detailColor = ColorToastError
			}
			renderText(renderer, config, font, ellipsize(font, detail, r.W-32),
				detailColor, r.X+16, r.Y+8+medH+4)
		}
	}

	hint := "A: launch   X: export   Y: rescan   B: back"
	if len(entries) == 0 {
		hint = "Y: rescan   B: back"
	}
	if gpPanel.returnEmu {
		hint = strings.Replace(hint, "B: back", "B: emulators", 1)
	}
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+padX, panelY+panelH-footerH+8)
}
