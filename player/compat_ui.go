package main

// compat_ui.go — the Smart Pro Compatibility panel.
//
// The panel answers three questions in one place:
//   1. which scanned ROMs have a community report, and what does it say;
//   2. what the safe boost plan is for this game, and is it already applied;
//   3. which libretro core builds are installed, active or waiting in the pool.
//
// It is opened from the pinned row at the top of Emulators, mirrors the
// existing modal-panel conventions (D-pad/A/B, mouse rows, scroll window), and
// keeps the ROM walk off the render thread.

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

type compatPanelState struct {
	open      bool
	tab       int // 0 = games, 1 = core builds
	cursor    int
	scroll    int
	returnEmu bool
	cores     coreInventory
}

const (
	compatTabGames = iota
	compatTabCores
)

var cpPanel compatPanelState

type compatRowHit struct {
	index int
	rect  sdl.Rect
}

type compatTabHit struct {
	tab  int
	rect sdl.Rect
}

var compatRowHits []compatRowHit
var compatTabHits []compatTabHit
var compatPanelRect sdl.Rect

func openCompatPanel(returnEmu bool) {
	qs.open = false
	toolsPanel.open = false
	emuPanel.open = false
	gpPanel.open = false
	raPanel.open = false
	cpPanel = compatPanelState{open: true, returnEmu: returnEmu}
	compatPanelRect = sdl.Rect{}
	compatRowHits = nil
	compatTabHits = nil
	invalidateCompatCache()
	cpPanel.cores = scanCoreInventory()
	startCompatibilityScan()
	PlayActivateSound()
}

func closeCompatPanel() {
	if !cpPanel.open {
		return
	}
	returnEmu := cpPanel.returnEmu
	cpPanel = compatPanelState{}
	compatPanelRect = sdl.Rect{}
	compatRowHits = nil
	compatTabHits = nil
	if returnEmu {
		emuPanel.open = true
		emuPanelRect = sdl.Rect{}
		emuRowHits = nil
	}
	PlayBackSound()
}

func compatGames() []gameAssessment {
	games, _, _, _, _ := compatibilitySnapshot()
	return games
}

func compatCoreRows(inv coreInventory) []coreBuild {
	rows := make([]coreBuild, 0, len(inv.Builds)+len(inv.Pool))
	rows = append(rows, inv.Builds...)
	rows = append(rows, inv.Pool...)
	return rows
}

func compatRowCount() int {
	if cpPanel.tab == compatTabCores {
		return len(compatCoreRows(cpPanel.cores))
	}
	return len(compatGames())
}

func compatPanelMove(delta int) {
	count := compatRowCount()
	if count == 0 {
		return
	}
	cpPanel.cursor += delta
	if cpPanel.cursor < 0 {
		cpPanel.cursor = count - 1
	}
	if cpPanel.cursor >= count {
		cpPanel.cursor = 0
	}
	PlayNavSound()
}

func compatPanelSwitchTab(delta int) {
	cpPanel.tab += delta
	if cpPanel.tab < 0 {
		cpPanel.tab = 1
	}
	if cpPanel.tab > 1 {
		cpPanel.tab = 0
	}
	cpPanel.cursor = 0
	cpPanel.scroll = 0
	PlayNavSound()
}

func compatPanelActivate() {
	if cpPanel.tab == compatTabCores {
		rows := compatCoreRows(cpPanel.cores)
		if len(rows) == 0 || cpPanel.cursor < 0 || cpPanel.cursor >= len(rows) {
			ShowToast("No core builds found. See "+compatCorePoolDir(), ToastKindInfo)
			return
		}
		build := rows[cpPanel.cursor]
		if build.Active {
			ShowToast(build.label()+" is already active", ToastKindInfo)
			return
		}
		target, backup, err := activateCoreBuild(build)
		if err != nil {
			log.Printf("[compat] activate core failed: %v", err)
			ShowToast("Core activation failed: "+err.Error(), ToastKindError)
			return
		}
		cpPanel.cores = scanCoreInventory()
		cpPanel.cursor = 0
		startCompatibilityScan()
		message := "Activated " + build.label()
		if backup != "" {
			message += " · backup " + filepath.Base(backup)
		}
		log.Printf("[compat] core active at %s", target)
		PlaySuccessSound()
		ShowToast(message, ToastKindSuccess)
		return
	}

	games := compatGames()
	if len(games) == 0 || cpPanel.cursor < 0 || cpPanel.cursor >= len(games) {
		ShowToast("No scanned ROMs to boost", ToastKindInfo)
		return
	}
	assessment := games[cpPanel.cursor]
	result, err := applyBoost(assessment)
	if err != nil {
		log.Printf("[compat] boost failed for %s: %v", assessment.Title, err)
		ShowToast("Boost failed: "+err.Error(), ToastKindError)
		return
	}
	setCompatibilityBoostState(assessment.Key, true)
	PlaySuccessSound()
	message := "Boost saved for " + assessment.Title
	if len(result.Targets) > 0 {
		message += " · override " + filepath.Base(result.Targets[0])
	}
	if len(result.Warnings) > 0 {
		message += " · " + result.Warnings[0]
	}
	ShowToast(message, ToastKindSuccess)
}

func compatPanelRevert() {
	if cpPanel.tab == compatTabCores {
		ShowToast("Core builds are not changed by revert", ToastKindInfo)
		return
	}
	games := compatGames()
	if len(games) == 0 || cpPanel.cursor < 0 || cpPanel.cursor >= len(games) {
		return
	}
	assessment := games[cpPanel.cursor]
	if !assessment.Boosted && !boostRecipeExists(assessment.Key) {
		ShowToast("No boost recipe applied to "+assessment.Title, ToastKindInfo)
		return
	}
	removed, err := revertBoost(assessment)
	if err != nil {
		ShowToast("Revert failed: "+err.Error(), ToastKindError)
		return
	}
	setCompatibilityBoostState(assessment.Key, false)
	PlayBackSound()
	ShowToast(fmt.Sprintf("Reverted %s · %d file(s) removed", assessment.Title, len(removed)), ToastKindSuccess)
}

func compatPanelRescan() {
	invalidateCompatCache()
	cpPanel.cores = scanCoreInventory()
	cpPanel.cursor = 0
	cpPanel.scroll = 0
	startCompatibilityScan()
	PlayNavSound()
	ShowToast("Rescanning ROM folders and core builds…", ToastKindInfo)
}

func ctrlCompatPanel(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		compatPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		compatPanelMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		compatPanelSwitchTab(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		compatPanelSwitchTab(1)
	case sdl.CONTROLLER_BUTTON_A:
		compatPanelActivate()
	case sdl.CONTROLLER_BUTTON_X:
		compatPanelRevert()
	case sdl.CONTROLLER_BUTTON_Y:
		compatPanelRescan()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		// L1 checks the libretro aarch64 index and fills the core pool; it never
		// replaces the active core, so it is safe to press without reading up.
		startEmulatorUpdates()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeCompatPanel()
	}
	return true
}

func handleCompatKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !cpPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		compatPanelMove(-1)
	case sdl.K_DOWN:
		compatPanelMove(1)
	case sdl.K_LEFT:
		compatPanelSwitchTab(-1)
	case sdl.K_RIGHT, sdl.K_TAB:
		compatPanelSwitchTab(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		compatPanelActivate()
	case sdl.K_x:
		compatPanelRevert()
	case sdl.K_y, sdl.K_r:
		compatPanelRescan()
	case sdl.K_u:
		startEmulatorUpdates()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeCompatPanel()
	}
	return true
}

func handleCompatMouse(mx, my int32) bool {
	if !cpPanel.open {
		return false
	}
	for _, tab := range compatTabHits {
		if pointInRect(mx, my, tab.rect) {
			if cpPanel.tab != tab.tab {
				cpPanel.tab = tab.tab
				cpPanel.cursor = 0
				cpPanel.scroll = 0
				PlayNavSound()
			}
			return true
		}
	}
	for _, hit := range compatRowHits {
		if pointInRect(mx, my, hit.rect) {
			cpPanel.cursor = hit.index
			compatPanelActivate()
			return true
		}
	}
	if compatPanelRect.W > 0 && pointInRect(mx, my, compatPanelRect) {
		return true
	}
	closeCompatPanel()
	return true
}

func compatStatusColor(assessment gameAssessment) sdl.Color {
	if assessment.Verified {
		return ColorToastSuccess
	}
	switch assessment.Status {
	case compatStatusPlayable:
		return ColorToastSuccess
	case compatStatusAudio, compatStatusSlow:
		return sdl.Color{R: 255, G: 190, B: 80, A: 255}
	case compatStatusUnplayable:
		return ColorToastError
	default:
		if assessment.Tier == compatTierSolid {
			return ColorToastSuccess
		}
		if assessment.Tier == compatTierMixed {
			return sdl.Color{R: 255, G: 190, B: 80, A: 255}
		}
		return ColorTextTertiary()
	}
}

func compatBadge(assessment gameAssessment) (string, sdl.Color) {
	if assessment.Verified {
		return "VERIFIED ON SMART PRO", ColorToastSuccess
	}
	switch assessment.Tier {
	case compatTierSolid:
		return "SMART PRO TIER", ColorToastSuccess
	case compatTierMixed:
		return "GAME-DEPENDENT", sdl.Color{R: 255, G: 190, B: 80, A: 255}
	default:
		return "UNREPORTED", ColorTextTertiary()
	}
}

func compatCoreStatusLabel(build coreBuild) (string, sdl.Color) {
	if build.Active {
		return "ACTIVE", ColorToastSuccess
	}
	if build.Pool {
		return "POOL", sdl.Color{R: 120, G: 190, B: 255, A: 255}
	}
	return "BUILD", ColorTextTertiary()
}

func compatCoreDetailLine(build coreBuild) string {
	parts := []string{}
	if build.Version != "" {
		parts = append(parts, "version "+build.Version)
	}
	if build.Size > 0 {
		parts = append(parts, fmt.Sprintf("%.1f MB", float64(build.Size)/(1<<20)))
	}
	if build.Active {
		parts = append(parts, "in RetroArch cores")
	} else if build.Pool {
		parts = append(parts, "waiting in pool")
	}
	return strings.Join(parts, " · ")
}

func compatCoreReportCount(core string) int {
	entries := compatEntries()
	count := 0
	for _, entry := range entries {
		if normalizeCoreName(entry.Core) == normalizeCoreName(core) {
			count++
		}
	}
	return count
}

func compatPanelTitle() string {
	if cpPanel.tab == compatTabCores {
		return "Core Builds"
	}
	return "Smart Pro Compatibility"
}

func compatPanelSubtitle(games []gameAssessment, scanning bool) string {
	if cpPanel.tab == compatTabCores {
		parts := []string{fmt.Sprintf("%d active", len(cpPanel.cores.Builds)), fmt.Sprintf("%d in pool", len(cpPanel.cores.Pool))}
		if cpPanel.cores.Problem != "" {
			parts = append(parts, cpPanel.cores.Problem)
		}
		return strings.Join(parts, " · ")
	}
	if scanning {
		return "Scanning ROM folders…"
	}
	return compatibilitySummary(games)
}

// compatTextLines wraps notes without pulling in the chat/package helpers'
// unrelated types. It is intentionally local so the panel can cap the number
// of lines that fit the detail pane.
func compatTextLines(font *ttf.Font, text string, maxW int32) []string {
	if font == nil || maxW <= 0 {
		return []string{text}
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		cur := ""
		for _, word := range words {
			test := word
			if cur != "" {
				test = cur + " " + word
			}
			width, _, _ := font.SizeUTF8(test)
			if cur != "" && int32(width) > maxW {
				out = append(out, cur)
				cur = word
			} else {
				cur = test
			}
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	return out
}

func renderCompatibilityPanel(renderer *sdl.Renderer, config *Config) {
	if !cpPanel.open || renderer == nil {
		return
	}
	games, roots, issues, scanning, built := compatibilitySnapshot()
	if cpPanel.tab == compatTabGames && cpPanel.cursor >= len(games) {
		cpPanel.cursor = len(games) - 1
	}
	if cpPanel.cursor < 0 {
		cpPanel.cursor = 0
	}
	if cpPanel.tab == compatTabCores {
		if rows := compatCoreRows(cpPanel.cores); cpPanel.cursor >= len(rows) {
			cpPanel.cursor = len(rows) - 1
		}
		if cpPanel.cursor < 0 {
			cpPanel.cursor = 0
		}
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

	panelW := int32(1040)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := int32(620)
	if panelH > screenHeight-40 {
		panelH = screenHeight - 40
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	compatPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	const padX = int32(24)

	// The title band is the shared page header, measured before the body is
	// placed so the list starts under the bar rather than under an assumption
	// about it. The tab strip sits on the subtitle's line, so it takes its y
	// from the same measurement.
	header := UIPanelHeader(compatPanelTitle(), compatPanelSubtitle(games, scanning), "tools")
	headerH := UIPanelHeaderBand(config, header, 10)
	footerH := 18 + smallH
	bodyY := panelY + headerH
	bodyH := panelH - headerH - footerH
	listW := panelW * 58 / 100
	detailX := panelX + listW + 18
	detailW := panelW - listW - 18 - padX

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 185)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	header.X = panelX + padX
	header.Y = panelY + UIPanelHeaderInset
	header.W = panelW - 2*padX
	UIPageHeaderDraw(renderer, config, header)

	// Tab strip: two text buttons, with the active one underlined.
	compatTabHits = nil
	tabY := header.Y + uiPageHeaderMetricsFor(config, header).SubtitleY
	tabX := panelX + padX + 420
	for _, item := range []struct {
		tab   int
		label string
	}{{compatTabGames, "Games"}, {compatTabCores, "Core builds"}} {
		labelColor := ColorTextTertiary()
		if cpPanel.tab == item.tab {
			labelColor = ColorTextPrimary()
		}
		w, _, _ := font.SizeUTF8(item.label)
		renderText(renderer, config, font, item.label, labelColor, tabX, tabY)
		hit := sdl.Rect{X: tabX - 6, Y: tabY - 4, W: int32(w) + 12, H: smallH + 8}
		compatTabHits = append(compatTabHits, compatTabHit{tab: item.tab, rect: hit})
		if cpPanel.tab == item.tab {
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 200)
			renderer.FillRect(&sdl.Rect{X: tabX, Y: tabY + smallH + 2, W: int32(w), H: 2})
		}
		tabX += int32(w) + 28
	}

	rowH := int32(64)
	if cpPanel.tab == compatTabCores {
		rowH = 58
	}
	maxRows := bodyH / rowH
	if maxRows < 2 {
		maxRows = 2
	}
	visible := int32(compatRowCount())
	if visible > maxRows {
		visible = maxRows
	}
	if cpPanel.cursor < cpPanel.scroll {
		cpPanel.scroll = cpPanel.cursor
	}
	if cpPanel.cursor >= cpPanel.scroll+int(visible) {
		cpPanel.scroll = cpPanel.cursor - int(visible) + 1
	}
	if cpPanel.scroll > compatRowCount()-int(visible) {
		cpPanel.scroll = compatRowCount() - int(visible)
	}
	if cpPanel.scroll < 0 {
		cpPanel.scroll = 0
	}

	focusColor := installHighlight()
	compatRowHits = nil

	if cpPanel.tab == compatTabGames {
		renderCompatGamesList(renderer, config, games, roots, issues, scanning, built, panelX, bodyY, listW, rowH, visible, padX, focusColor)
		renderCompatGameDetail(renderer, config, games, detailX, bodyY, detailW, bodyH, font, medium)
	} else {
		rows := compatCoreRows(cpPanel.cores)
		renderCompatCoreList(renderer, config, rows, panelX, bodyY, listW, rowH, visible, padX, focusColor, font, medium)
		renderCompatCoreDetail(renderer, config, rows, detailX, bodyY, detailW, bodyH, font, medium)
	}

	hint := hintLine(FooterHint{"D-Pad", "Navigate"}, FooterHint{"←/→", "Tab"},
		FooterHint{"A", "Apply Boost"}, FooterHint{"X", "Revert"}, FooterHint{"Y", "Rescan"}, FooterHint{"B", "Back"})
	if cpPanel.tab == compatTabCores {
		hint = hintLine(FooterHint{"D-Pad", "Navigate"}, FooterHint{"←/→", "Tab"},
			FooterHint{"A", "Activate Build"}, FooterHint{"U/L1", "Update Cores"},
			FooterHint{"Y", "Rescan"}, FooterHint{"B", "Back"})
	}
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+padX, panelY+panelH-footerH+6)
}

func renderCompatGamesList(renderer *sdl.Renderer, config *Config, games []gameAssessment, roots []string, issues []string, scanning, built bool, panelX, bodyY, listW, rowH, visible, padX int32, focusColor sdl.Color) {
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	rowW := listW - 2*padX
	if len(games) == 0 {
		y := bodyY + 16
		title := "No scanned ROMs yet"
		if scanning {
			title = "Scanning ROM folders…"
		}
		renderText(renderer, config, medium, title, ColorTextPrimary(), panelX+padX, y)
		y += int32(medium.Height()) + 8
		for _, line := range compatTextLines(font, "Searched: "+strings.Join(roots, ", "), rowW) {
			renderText(renderer, config, font, line, ColorTextTertiary(), panelX+padX, y)
			y += int32(font.Height())
		}
		if built && !scanning {
			y += 6
			renderText(renderer, config, font, "Add games under Roms/<system>/ and press Y.", ColorTextSecondary(), panelX+padX, y)
		}
		if len(issues) > 0 {
			y += 6
			renderText(renderer, config, font, "Catalog: "+issues[0], sdl.Color{R: 255, G: 190, B: 80, A: 255}, panelX+padX, y)
		}
		return
	}

	for i := int32(0); i < visible; i++ {
		idx := cpPanel.scroll + int(i)
		game := games[idx]
		y := bodyY + i*rowH
		r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
		compatRowHits = append(compatRowHits, compatRowHit{index: idx, rect: r})
		focused := idx == cpPanel.cursor
		rowBg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			rowBg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}

		renderText(renderer, config, medium, ellipsize(medium, game.Title, r.W-170), ColorTextPrimary(), r.X+14, r.Y+8)
		label, labelColor := compatBadge(game)
		if game.Boosted {
			label = "BOOSTED · " + label
		}
		lw, _, _ := font.SizeUTF8(label)
		renderText(renderer, config, font, label, labelColor, r.X+r.W-14-int32(lw), r.Y+8+int32(medium.Height()-font.Height())/2)
		status, statusColor := game.statusLabel(), compatStatusColor(game)
		renderText(renderer, config, font, status, statusColor, r.X+14, r.Y+8+int32(medium.Height())+3)
		detail := game.detailLine()
		dw, _, _ := font.SizeUTF8(status)
		renderText(renderer, config, font, ellipsize(font, detail, r.W-28-int32(dw)-12), ColorTextTertiary(), r.X+14+int32(dw)+12, r.Y+8+int32(medium.Height())+3)
	}
}

func renderCompatGameDetail(renderer *sdl.Renderer, config *Config, games []gameAssessment, x, y, w, h int32, font, medium *ttf.Font) {
	renderer.SetDrawColor(ColorBorderDefault.R, ColorBorderDefault.G, ColorBorderDefault.B, 110)
	renderer.FillRect(&sdl.Rect{X: x - 9, Y: y + 6, W: 1, H: h - 18})

	if len(games) == 0 || cpPanel.cursor < 0 || cpPanel.cursor >= len(games) {
		return
	}
	game := games[cpPanel.cursor]
	smallH := int32(font.Height())
	medH := int32(medium.Height())
	lineY := y + 8

	renderText(renderer, config, medium, ellipsize(medium, game.Title, w), ColorTextPrimary(), x, lineY)
	lineY += medH + 7
	badge, badgeColor := compatBadge(game)
	renderText(renderer, config, font, badge, badgeColor, x, lineY)
	lineY += smallH + 5
	status := game.statusLabel()
	renderText(renderer, config, font, status, compatStatusColor(game), x, lineY)
	statusW, _, _ := font.SizeUTF8(status)
	if game.Matched {
		source := " · community report"
		if game.Entry.Source != "" {
			source += " (" + game.Entry.Source + ")"
		}
		renderText(renderer, config, font, source, ColorTextTertiary(), x+int32(statusW)+4, lineY)
	} else {
		renderText(renderer, config, font, " · no report yet", ColorTextTertiary(), x+int32(statusW)+4, lineY)
	}
	lineY += smallH + 8

	renderText(renderer, config, font, "Hardware tier: "+game.Tier.label(), ColorTextSecondary(), x, lineY)
	lineY += smallH + 4
	core := game.Core
	if core == "" {
		core = "no core suggestion"
	}
	coreColor := ColorTextTertiary()
	if game.CoreState == "active" {
		coreColor = ColorToastSuccess
	} else if game.CoreState == "outdated" || game.CoreState == "pool" {
		coreColor = sdl.Color{R: 255, G: 190, B: 80, A: 255}
	} else if game.CoreState == "missing" {
		coreColor = ColorToastError
	}
	coreLabel := "Core: " + core
	renderText(renderer, config, font, coreLabel, ColorTextSecondary(), x, lineY)
	coreLabelW, _, _ := font.SizeUTF8(coreLabel)
	renderText(renderer, config, font, game.CoreDetail, coreColor, x+int32(coreLabelW)+8, lineY)
	lineY += smallH + 8

	renderText(renderer, config, font, "Boost: "+game.Plan.summary(), ColorTextSecondary(), x, lineY)
	lineY += smallH + 3
	noteLines := compatTextLines(font, game.Plan.Reason, w)
	for i, line := range noteLines {
		if i >= 2 {
			break
		}
		renderText(renderer, config, font, line, ColorTextTertiary(), x, lineY)
		lineY += smallH
	}
	lineY += 5

	if game.Matched && game.Entry.Notes != "" {
		renderText(renderer, config, font, "Report notes", ColorTextSecondary(), x, lineY)
		lineY += smallH + 2
		for i, line := range compatTextLines(font, game.Entry.Notes, w) {
			if i >= 7 || lineY+smallH > y+h-44 {
				break
			}
			renderText(renderer, config, font, line, ColorTextTertiary(), x, lineY)
			lineY += smallH
		}
	}

	if game.Boosted {
		renderText(renderer, config, font, "Boost applied · X reverts this game", ColorToastSuccess, x, y+h-smallH-24)
	} else if game.Plan.Recommended {
		renderText(renderer, config, font, "A applies the boost plan", accentColor, x, y+h-smallH-24)
	} else {
		renderText(renderer, config, font, "A saves the baseline recipe", ColorTextTertiary(), x, y+h-smallH-24)
	}
}

func renderCompatCoreList(renderer *sdl.Renderer, config *Config, rows []coreBuild, panelX, bodyY, listW, rowH, visible, padX int32, focusColor sdl.Color, font, medium *ttf.Font) {
	rowW := listW - 2*padX
	if len(rows) == 0 {
		y := bodyY + 16
		renderText(renderer, config, medium, "No core builds yet", ColorTextPrimary(), panelX+padX, y)
		y += int32(medium.Height()) + 8
		for _, line := range compatTextLines(font, "Active cores: "+cpPanel.cores.Problem, rowW) {
			renderText(renderer, config, font, line, ColorTextTertiary(), panelX+padX, y)
			y += int32(font.Height())
		}
		y += 6
		for _, line := range compatTextLines(font, "Put extra builds in "+compatCorePoolDir()+"/<core>/<version>/", rowW) {
			renderText(renderer, config, font, line, ColorTextSecondary(), panelX+padX, y)
			y += int32(font.Height())
		}
		return
	}

	for i := int32(0); i < visible; i++ {
		idx := cpPanel.scroll + int(i)
		build := rows[idx]
		y := bodyY + i*rowH
		r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
		compatRowHits = append(compatRowHits, compatRowHit{index: idx, rect: r})
		focused := idx == cpPanel.cursor
		rowBg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			rowBg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}
		renderText(renderer, config, medium, ellipsize(medium, build.Core, r.W-170), ColorTextPrimary(), r.X+14, r.Y+7)
		label, labelColor := compatCoreStatusLabel(build)
		lw, _, _ := font.SizeUTF8(label)
		renderText(renderer, config, font, label, labelColor, r.X+r.W-14-int32(lw), r.Y+7+int32(medium.Height()-font.Height())/2)
		renderText(renderer, config, font, ellipsize(font, compatCoreDetailLine(build), r.W-28), ColorTextTertiary(), r.X+14, r.Y+7+int32(medium.Height())+3)
	}
}

func renderCompatCoreDetail(renderer *sdl.Renderer, config *Config, rows []coreBuild, x, y, w, h int32, font, medium *ttf.Font) {
	renderer.SetDrawColor(ColorBorderDefault.R, ColorBorderDefault.G, ColorBorderDefault.B, 110)
	renderer.FillRect(&sdl.Rect{X: x - 9, Y: y + 6, W: 1, H: h - 18})
	if len(rows) == 0 || cpPanel.cursor < 0 || cpPanel.cursor >= len(rows) {
		return
	}
	build := rows[cpPanel.cursor]
	smallH := int32(font.Height())
	medH := int32(medium.Height())
	lineY := y + 8

	renderText(renderer, config, medium, ellipsize(medium, build.Core, w), ColorTextPrimary(), x, lineY)
	lineY += medH + 7
	label, labelColor := compatCoreStatusLabel(build)
	renderText(renderer, config, font, label, labelColor, x, lineY)
	lineY += smallH + 5
	renderText(renderer, config, font, "Version: "+valueOr(build.Version, "unversioned"), ColorTextSecondary(), x, lineY)
	lineY += smallH + 4
	renderText(renderer, config, font, fmt.Sprintf("Size: %.1f MB", float64(build.Size)/(1<<20)), ColorTextSecondary(), x, lineY)
	lineY += smallH + 8

	renderText(renderer, config, font, "Path", ColorTextSecondary(), x, lineY)
	lineY += smallH + 2
	for _, line := range compatTextLines(font, build.Path, w) {
		if lineY+smallH > y+h-70 {
			break
		}
		renderText(renderer, config, font, line, ColorTextTertiary(), x, lineY)
		lineY += smallH
	}
	lineY += 8

	reports := compatCoreReportCount(build.Core)
	renderText(renderer, config, font, fmt.Sprintf("Suggested by %d catalog report(s)", reports), ColorTextSecondary(), x, lineY)
	lineY += smallH + 8

	action := "This is the active build."
	if build.Pool && !build.Active {
		action = "A activates this build. The current active binary is backed up next to it."
	}
	for _, line := range compatTextLines(font, action, w) {
		if lineY+smallH > y+h-24 {
			break
		}
		renderText(renderer, config, font, line, accentColor, x, lineY)
		lineY += smallH + 2
	}
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
