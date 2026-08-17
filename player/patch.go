package main

// patch.go is the MISC -> Patch scene: a controller-first list of update
// classes, local actions, backups and diagnostics. It uses the shared design
// system (colors, rounded rects, fonts, toasts, footer) and never draws its
// own header/footer — the shared shell owns those.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// FooterHint is one controller-hint group shared by the scene footer
// (extras.go) and the Patch scene's contextual hints.
type FooterHint struct {
	Button string
	Label  string
}

// --- Patch scene state ---

var (
	patchSnap         *PatchSnapshot
	patchFocusIndex   int
	patchScrollY      int32
	patchLastRefresh  uint64
	patchModal        *patchModalState
	patchHoldStart    uint64
	patchHoldActive   bool
	patchHoldDuration = uint64(1000) // hold-to-confirm for high-risk actions
	patchInitDone     bool
)

// patchModalState is an active confirmation modal or package action menu.
type patchModalState struct {
	ID       string
	Title    string
	Body     string
	Confirm  string
	HighRisk bool
	// Menu: when set, the modal shows a vertical action menu instead of the
	// standard confirm/cancel buttons (package install/update/remove).
	Menu        []string
	MenuFocus   int
	PackageID   string
	PackageName string
}

// patchAction describes one top-level action row.
type patchAction struct {
	ID    string
	Label string
	Sub   string
}

func patchActions() []patchAction {
	actions := []patchAction{
		{ID: "check", Label: "Check for updates", Sub: "JukaHub releases · signed Patch repository"},
		{ID: "refresh", Label: "Refresh repository", Sub: "Download + verify the signed package index"},
		{ID: "editrepo", Label: "Edit packages.json", Sub: "Add your own packages (fonts, themes, tools, configs)"},
		{ID: "history", Label: "History & Rollback", Sub: "Transaction log with rollback to the last backup"},
		{ID: "repair", Label: "Verify & repair", Sub: "Validate config, tools, assets; clear stale downloads"},
		{ID: "diagnostics", Label: "Export diagnostics", Sub: "Redacted report for support"},
	}
	if patchState.LastUpdateVer != "" {
		actions = append(actions, patchAction{ID: "apply", Label: "Apply staged update", Sub: patchState.LastUpdateVer + " is staged and verified"})
	}
	if patchSnap != nil && len(patchSnap.Backups) > 0 {
		actions = append(actions, patchAction{ID: "restore", Label: "Restore latest backup", Sub: "Reverts the most recent backup set"})
	}
	return actions
}

// patchFocusables returns all focusable items in visual order.
func patchFocusables() []string {
	var out []string
	for _, a := range patchActions() {
		out = append(out, "action:"+a.ID)
	}
	for _, r := range patchSnap.Rows {
		out = append(out, "row:"+string(r.Component))
	}
	// Package entries (apt-style install/update/remove targets).
	if patchSnap != nil {
		for _, p := range patchSnap.Packages {
			out = append(out, "pkg:"+p.ID)
		}
	}
	for _, b := range patchSnap.Backups {
		out = append(out, "backup:"+b)
	}
	return out
}

// patchClampFocus keeps patchFocusIndex in range.
func patchClampFocus() {
	n := len(patchFocusables())
	if n == 0 {
		patchFocusIndex = 0
		return
	}
	if patchFocusIndex < 0 {
		patchFocusIndex = 0
	}
	if patchFocusIndex >= n {
		patchFocusIndex = n - 1
	}
}

// patchCurrentFocus returns the focused item id ("action:x", "row:x", "backup:x").
func patchCurrentFocus() string {
	list := patchFocusables()
	patchClampFocus()
	if patchFocusIndex < 0 || patchFocusIndex >= len(list) {
		return ""
	}
	return list[patchFocusIndex]
}

// patchMove moves focus by delta, skipping unavailable rows.
func patchMove(delta int) {
	list := patchFocusables()
	if len(list) == 0 {
		return
	}
	// Skip disabled/unavailable rows so focus never lands on a dead item.
	next := patchFocusIndex
	for i := 0; i < len(list); i++ {
		next += delta
		if next < 0 {
			next = len(list) - 1
		}
		if next >= len(list) {
			next = 0
		}
		if patchFocusEnabled(list[next]) {
			break
		}
	}
	if next != patchFocusIndex {
		patchFocusIndex = next
		PlayNavSound()
	}
}

// patchFocusEnabled reports whether an item can be focused/activated.
func patchFocusEnabled(id string) bool {
	if strings.HasPrefix(id, "action:") {
		return true
	}
	if strings.HasPrefix(id, "backup:") {
		return true
	}
	if strings.HasPrefix(id, "row:") {
		comp := PatchComponent(strings.TrimPrefix(id, "row:"))
		for _, r := range patchSnap.Rows {
			if r.Component == comp {
				return r.Enabled
			}
		}
	}
	if strings.HasPrefix(id, "pkg:") {
		return true
	}
	return true
}

// --- Entry points called from main.go ---

// renderPatchScene renders the Patch scene body (background, header, list,
// modal). The shared status bar and footer are drawn by renderScene.
func renderPatchScene(renderer *sdl.Renderer, config *Config) {
	if !patchInitDone {
		patchSnap = BuildPatchSnapshot(config)
		patchInitDone = true
	}
	if sdl.GetTicks64()-patchLastRefresh > 2000 {
		patchSnap = BuildPatchSnapshot(config)
		patchLastRefresh = sdl.GetTicks64()
	}
	patchClampFocus()

	ensureBackgroundTexture(renderer, config)
	if bgTexture != nil {
		renderer.Copy(bgTexture, nil, &sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	} else {
		renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
		renderer.Clear()
	}
	renderAmbientBackground(renderer)

	contentY := int32(72)
	contentH := screenHeight - HomeFooterH - 16 - contentY
	contentTop := contentY
	contentBottom := contentY + contentH

	// Section header: Patch + device + busy/status line.
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil || medium == nil {
		return
	}

	titleStr := "Patch"
	subStr := "Update and repair JukaHub"
	if patchSnap != nil {
		subStr += "  ·  " + deviceDisplayName(patchSnap.Device)
	}
	renderText(renderer, config, medium, titleStr, ColorTextPrimary(), 24, contentTop+2)
	renderText(renderer, config, font, subStr, ColorTextSecondary(), 24+int32(120), contentTop+10)

	statusLine := "Local checks only · press A on a row for details"
	if label := CurrentPatchBusyLabel(); label != "" {
		statusLine = label
	} else if patchState.LastCheckAt != "" {
		statusLine = "Last check: " + patchState.LastCheckAt[:16]
	}
	if patchState.LastCheckError != "" {
		statusLine = patchState.LastCheckError
	}
	renderText(renderer, config, font, statusLine, ColorTextTertiary(), 24, contentTop+34)

	listTop := contentTop + 60
	listH := contentBottom - listTop

	// Build rows for the list (actions, update classes, packages, backups).
	type uiRow struct {
		id     string
		title  string
		sub    string
		right  string // status/right-aligned text
		risk   string
		status PatchRowStatus
	}
	var rows []uiRow
	// Action rows.
	for _, a := range patchActions() {
		rows = append(rows, uiRow{id: "action:" + a.ID, title: a.Label, sub: a.Sub, right: "A  Open"})
	}
	// Update class rows.
	if patchSnap != nil {
		for _, r := range patchSnap.Rows {
			avail := r.Available
			if avail == "" {
				avail = "—"
			}
			rows = append(rows, uiRow{
				id:     "row:" + string(r.Component),
				title:  ComponentDisplayName(r.Component),
				sub:    fmt.Sprintf("Installed %s · %s", r.Installed, r.Source),
				right:  string(r.Status),
				risk:   r.Risk,
				status: r.Status,
			})
		}
	}
	// Package rows (install/update/remove).
	if patchSnap != nil {
		for _, p := range patchSnap.Packages {
			status := StatusAvailable
			right := "A  Install"
			installed := IsPackageInstalled(&p)
			current := installed && IsPackageCurrent(&p)
			switch {
			case installed && current:
				status = StatusCurrent
				right = "Installed · A  Menu"
			case installed:
				status = StatusAvailable
				right = "Update · A  Menu"
			}
			rows = append(rows, uiRow{
				id:     "pkg:" + p.ID,
				title:  p.Name,
				sub:    fmt.Sprintf("v%s · %s", p.Version, p.Description),
				right:  right,
				status: status,
			})
		}
	}
	// Backup rows.
	for _, b := range patchSnap.Backups {
		rows = append(rows, uiRow{id: "backup:" + b, title: "Backup " + b, sub: "Restore this backup set", right: "A  Restore"})
	}
	if len(rows) == 0 {
		renderText(renderer, config, font, "Nothing to show.", ColorTextSecondary(), 24, listTop+20)
		return
	}

	rowH := int32(64)
	rowGap := int32(10)
	visible := int(listH / (rowH + rowGap))
	if visible < 1 {
		visible = 1
	}
	focusID := patchCurrentFocus()
	focusRow := 0
	for i, r := range rows {
		if r.id == focusID {
			focusRow = i
			break
		}
	}
	// Keep focused row visible.
	if focusRow < int(patchScrollY/(rowH+rowGap)) {
		patchScrollY = int32(focusRow) * (rowH + rowGap)
	}
	if focusRow >= int(patchScrollY/(rowH+rowGap))+visible {
		patchScrollY = int32(focusRow-visible+1) * (rowH + rowGap)
	}
	if patchScrollY < 0 {
		patchScrollY = 0
	}

	// Render rows inside the clipped viewport.
	renderWithClip(renderer, 24, listTop, screenWidth-48, listH, func(r *sdl.Renderer) {
		for i, row := range rows {
			y := listTop - patchScrollY + int32(i)*(rowH+rowGap)
			if y+rowH < listTop || y > listTop+listH {
				continue
			}
			focused := row.id == focusID
			bg := ColorCard
			border := ColorBorder
			thick := int32(1)
			if focused {
				bg = ColorCardFocus
				border = ColorAccent
				thick = 3
			}
			fillRoundedRect(r, 24, y, screenWidth-48, rowH, RadiusMD, bg)
			strokeRoundedRect(r, 24, y, screenWidth-48, rowH, RadiusMD, thick, border)

			// Risk chip (system updates stand out).
			chipX := int32(28)
			chipY := y + (rowH-22)/2
			riskColor := ColorTextSecondary()
			if row.risk == "System Update" {
				riskColor = ColorWarning
			}
			chipW := int32(0)
			if row.risk != "" {
				ch, _, _ := font.SizeUTF8(row.risk)
				chipW = int32(ch) + 16
				fillRoundedRect(r, chipX, chipY, chipW, 22, 11, ColorIconSurface)
				renderText(r, config, font, row.risk, riskColor, chipX+8, chipY+1)
			}
			textX := chipX + chipW + 12
			if chipW == 0 {
				textX = chipX
			}
			titleColor := ColorTextPrimary()
			if !patchFocusEnabled(row.id) {
				titleColor = ColorTextTertiary()
			}
			renderText(r, config, medium, row.title, titleColor, textX, y+10)
			renderText(r, config, font, row.sub, ColorTextSecondary(), textX, y+38)
			_ = row
		}
	})

	// Right-side status text for focused row (drawn after rows so it stays crisp).
	for i, row := range rows {
		if row.id != focusID {
			continue
		}
		y := listTop - patchScrollY + int32(i)*(rowH+rowGap)
		if y+rowH < listTop || y > listTop+listH {
			continue
		}
		statusColor := ColorTextSecondary()
		switch row.status {
		case StatusAvailable, StatusStaged:
			statusColor = ColorSuccess
		case StatusFailed:
			statusColor = ColorDanger
		case StatusUnavailable:
			statusColor = ColorTextTertiary()
		}
		sw, _, _ := font.SizeUTF8(row.right)
		renderText(renderer, config, font, row.right, statusColor, screenWidth-24-int32(sw), y+22)
	}

	// Scrollbar when the list overflows.
	totalH := int32(len(rows))*(rowH+rowGap) - rowGap
	if totalH > listH {
		barH := int32(float64(listH) * float64(listH) / float64(totalH))
		barY := listTop + int32(float64(listH-barH)*float64(patchScrollY)/float64(totalH-listH))
		fillRoundedRect(renderer, screenWidth-12, barY, 4, barH, 2, WithAlpha(ColorAccent, 140))
	}

	// Modal overlay (drawn last, on top of everything in the scene).
	renderPatchModal(renderer, config, font, medium)
}

// renderPatchModal draws the active confirmation modal.
func renderPatchModal(renderer *sdl.Renderer, config *Config, font, medium *ttf.Font) {
	m := patchModal
	if m == nil || font == nil {
		return
	}
	w := int32(720)
	h := int32(300)
	x := (screenWidth - w) / 2
	y := (screenHeight - h) / 2

	// Scrim.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(6, 8, 14, 190)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, x, y, w, h, RadiusLG, ColorCard)
	strokeRoundedRect(renderer, x, y, w, h, RadiusLG, 1, ColorBorder)
	renderer.SetDrawColor(ColorAccent.R, ColorAccent.G, ColorAccent.B, 255)
	renderer.FillRect(&sdl.Rect{X: x, Y: y, W: w, H: 3})

	renderText(renderer, config, medium, m.Title, ColorTextPrimary(), x+24, y+20)

	// Body: wrap to the modal width.
	bodyMax := w - 48
	lines := wrapMeasured([]rune(m.Body), bodyMax, func(s string) int32 {
		sw, _, _ := font.SizeUTF8(s)
		return int32(sw)
	})
	if len(lines) == 0 {
		lines = []string{""}
	}
	lineH := int32(font.Height()) + 4
	textY := y + 66
	renderWithClip(renderer, x+24, textY, bodyMax, h-160, func(r *sdl.Renderer) {
		for _, l := range lines {
			renderText(r, config, font, l, ColorTextSecondary(), x+24, textY)
			textY += lineH
		}
	})

	// Package action menu: vertical action rows instead of confirm/cancel.
	if len(m.Menu) > 0 {
		menuY := y + h - 64 - int32(len(m.Menu))*44
		for i, item := range m.Menu {
			itemY := menuY + int32(i)*44
			focused := i == m.MenuFocus
			bg := ColorCardFocus
			if focused {
				bg = ColorCardFocus
				strokeRoundedRect(renderer, x+24, itemY, w-48, 38, RadiusMD, 2, ColorAccent)
			} else {
				strokeRoundedRect(renderer, x+24, itemY, w-48, 38, RadiusMD, 1, ColorBorder)
			}
			fillRoundedRect(renderer, x+24, itemY, w-48, 38, RadiusMD, bg)
			itemColor := ColorTextPrimary()
			if item == "Remove" {
				itemColor = ColorDanger
			}
			iw, _, _ := font.SizeUTF8(item)
			renderText(renderer, config, font, item, itemColor, x+24+(w-48-int32(iw))/2, itemY+9)
		}
		return
	}

	// Confirm / cancel buttons.
	btnY := y + h - 64
	btnH := int32(40)
	confirmW := int32(220)
	cancelW := int32(140)
	confirmX := x + w - confirmW - 24
	cancelX := confirmX - cancelW - 16

	// Confirm button: hold-to-confirm for high-risk actions.
	confirmLabel := m.Confirm
	if m.HighRisk {
		progress := float64(0)
		if patchHoldActive {
			elapsed := sdl.GetTicks64() - patchHoldStart
			if elapsed >= patchHoldDuration {
				progress = 1
			} else {
				progress = float64(elapsed) / float64(patchHoldDuration)
			}
		}
		fillRoundedRect(renderer, confirmX, btnY, confirmW, btnH, RadiusMD, ColorAccent)
		if progress > 0 && progress < 1 {
			// Progress fill inside the button.
			fillRoundedRect(renderer, confirmX, btnY, int32(float64(confirmW)*progress), btnH, RadiusMD, WithAlpha(ColorAccent, 180))
			renderer.SetDrawColor(6, 8, 14, 255)
		}
		if patchHoldActive && progress >= 1 {
			confirmLabel = "Release A to confirm"
		} else if patchHoldActive {
			confirmLabel = "Hold A…"
		} else {
			confirmLabel = "Hold A to confirm"
		}
		renderText(renderer, config, font, confirmLabel, ColorTextInverse(), confirmX+18, btnY+10)
	} else {
		fillRoundedRect(renderer, confirmX, btnY, confirmW, btnH, RadiusMD, ColorCardFocus)
		strokeRoundedRect(renderer, confirmX, btnY, confirmW, btnH, RadiusMD, 1, ColorAccent)
		cw, _, _ := font.SizeUTF8(confirmLabel)
		renderText(renderer, config, font, confirmLabel, ColorTextPrimary(), confirmX+(confirmW-int32(cw))/2, btnY+10)
	}
	fillRoundedRect(renderer, cancelX, btnY, cancelW, btnH, RadiusMD, ColorIconSurface)
	cw2, _, _ := font.SizeUTF8("Cancel")
	renderText(renderer, config, font, "Cancel", ColorTextSecondary(), cancelX+(cancelW-int32(cw2))/2, btnY+10)
}

// --- Input ---

// handlePatchKey processes keyboard input for the Patch scene. Returns true
// when consumed.
func handlePatchKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	if patchModal != nil {
		if len(patchModal.Menu) > 0 {
			switch e.Keysym.Sym {
			case sdl.K_UP:
				if patchModal.MenuFocus > 0 {
					patchModal.MenuFocus--
				}
			case sdl.K_DOWN:
				if patchModal.MenuFocus < len(patchModal.Menu)-1 {
					patchModal.MenuFocus++
				}
			case sdl.K_RETURN, sdl.K_SPACE:
				patchConfirmModal()
			case sdl.K_ESCAPE, sdl.K_b:
				patchModal = nil
				patchHoldActive = false
				PlayBackSound()
			}
			return true
		}
		switch e.Keysym.Sym {
		case sdl.K_ESCAPE, sdl.K_b:
			patchModal = nil
			patchHoldActive = false
			PlayBackSound()
			return true
		case sdl.K_RETURN, sdl.K_SPACE:
			if patchModal.HighRisk {
				return true // must hold A
			}
			patchConfirmModal()
			return true
		}
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		patchMove(-1)
	case sdl.K_DOWN:
		patchMove(1)
	case sdl.K_PAGEUP:
		patchMove(-6)
	case sdl.K_PAGEDOWN:
		patchMove(6)
	case sdl.K_RETURN, sdl.K_SPACE:
		patchActivate(config)
	case sdl.K_ESCAPE:
		goBackScene(config)
		return true
	default:
		return false
	}
	return true
}

// handlePatchController processes controller input for the Patch scene.
func handlePatchController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	if patchModal != nil {
		if len(patchModal.Menu) > 0 {
			switch e.Button {
			case sdl.CONTROLLER_BUTTON_DPAD_UP:
				if patchModal.MenuFocus > 0 {
					patchModal.MenuFocus--
				}
			case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
				if patchModal.MenuFocus < len(patchModal.Menu)-1 {
					patchModal.MenuFocus++
				}
			case sdl.CONTROLLER_BUTTON_A:
				patchConfirmModal()
			case sdl.CONTROLLER_BUTTON_B:
				patchModal = nil
				patchHoldActive = false
				PlayBackSound()
			}
			return true
		}
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_B:
			patchModal = nil
			patchHoldActive = false
			PlayBackSound()
			return true
		case sdl.CONTROLLER_BUTTON_A:
			if patchModal.HighRisk {
				patchHoldActive = true
				patchHoldStart = sdl.GetTicks64()
			} else {
				patchConfirmModal()
			}
			return true
		}
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		patchMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		patchMove(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		patchMove(-1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		patchMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		patchActivate(config)
	case sdl.CONTROLLER_BUTTON_B:
		goBackScene(config)
		return true
	default:
		return false
	}
	return true
}

// handlePatchHoldUpdate must be called every frame while a high-risk modal is
// active; it completes the confirmation once the hold duration elapses.
func handlePatchHoldUpdate() {
	if patchModal == nil || !patchModal.HighRisk || !patchHoldActive {
		return
	}
	if sdl.GetTicks64()-patchHoldStart >= patchHoldDuration {
		patchHoldActive = false
		patchConfirmModal()
	}
}

// patchConfirmModal executes the confirmed modal action.
func patchConfirmModal() {
	m := patchModal
	if m == nil {
		return
	}
	patchModal = nil
	patchHoldActive = false
	// Package action menu: A on a menu item runs the chosen action.
	if len(m.Menu) > 0 {
		action := ""
		if m.MenuFocus >= 0 && m.MenuFocus < len(m.Menu) {
			action = m.Menu[m.MenuFocus]
		}
		switch action {
		case "Rollback last transaction":
			patchModal = &patchModalState{
				ID:       "confirm-rollback",
				Title:    "Roll back the last transaction?",
				Body:     "Restores the files from the transaction's backup set. Any user edits made after the transaction are preserved unless they conflict.",
				Confirm:  "Roll back",
				HighRisk: true,
			}
		case "Install", "Update":
			patchModal = &patchModalState{
				ID:        "confirm-install",
				Title:     "Install " + m.PackageName + "?",
				Body:      "Installs the package's files with automatic backup and rollback.",
				Confirm:   "Install",
				HighRisk:  false,
				PackageID: m.PackageID,
			}
			patchConfirmModal()
		case "Remove":
			patchModal = &patchModalState{
				ID:        "confirm-remove",
				Title:     "Remove " + m.PackageName + "?",
				Body:      "Removes the package's managed files. A backup is created first so the removal can be rolled back.",
				Confirm:   "Remove",
				HighRisk:  true,
				PackageID: m.PackageID,
			}
		case "Cancel":
			// nothing
		}
		return
	}
	switch m.ID {
	case "confirm-swap":
		RunPatchOp("Applying staged update", func(ctx context.Context) error {
			return OpApplyStagedSwap(ctx, appConfig)
		})
	case "confirm-restore":
		name := m.Body // body holds the backup set name
		RunPatchOp("Restoring backup", func(ctx context.Context) error {
			return OpRestoreBackup(ctx, appConfig, name)
		})
	case "repair-report":
		// informational; nothing to run
	case "confirm-install":
		RunPatchOp("Installing package", func(ctx context.Context) error {
			return OpInstallPackage(ctx, appConfig, m.PackageID)
		})
	case "confirm-remove":
		RunPatchOp("Removing package", func(ctx context.Context) error {
			return OpRemovePackage(ctx, appConfig, m.PackageID)
		})
	case "confirm-rollback":
		RunPatchOp("Rolling back transaction", func(ctx context.Context) error { return OpRollbackTx(ctx, appConfig) })
	}
}

// openPatchHistory shows the transaction history and offers a rollback action
// when a committed transaction with a backup exists.
func openPatchHistory(config *Config) {
	txs := dbTransactions()
	if len(txs) == 0 {
		ShowToast("No transactions yet", ToastKindInfo)
		return
	}
	var sb strings.Builder
	for _, tx := range txs[:minInt(len(txs), 8)] {
		sb.WriteString(fmt.Sprintf("%s  %-9s %s  [%s]\n", shortTime(tx.At), tx.Op, strings.Join(tx.Packages, ", "), tx.Result))
	}
	menu := []string{"Close"}
	rollbackAvailable := false
	for _, tx := range txs {
		if tx.Result == "committed" && tx.BackupSet != "" {
			rollbackAvailable = true
			break
		}
	}
	if rollbackAvailable {
		menu = append([]string{"Rollback last transaction"}, menu...)
	}
	patchModal = &patchModalState{
		ID:    "history",
		Title: "History & Rollback",
		Body:  strings.TrimRight(sb.String(), "\n"),
		Menu:  menu, MenuFocus: 0,
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// shortTime renders an RFC3339 timestamp compactly.
func shortTime(s string) string {
	if len(s) >= 16 {
		return s[:16]
	}
	return s
}

// patchActivate runs the focused item.
func patchActivate(config *Config) {
	id := patchCurrentFocus()
	if id == "" {
		return
	}
	PlayActivateSound()
	switch {
	case strings.HasPrefix(id, "action:"):
		action := strings.TrimPrefix(id, "action:")
		switch action {
		case "check":
			RunPatchOp("Checking for updates", func(ctx context.Context) error { return OpCheckUpdates(ctx, config) })
		case "refresh":
			RunPatchOp("Refreshing repository", func(ctx context.Context) error { return OpRefreshSignedRepo(ctx, config) })
		case "history":
			openPatchHistory(config)
		case "editrepo":
			openPatchRepoEditor(config)
		case "repair":
			RunPatchOp("Repairing JukaHub", func(ctx context.Context) error { return OpRepair(ctx, config) })
		case "diagnostics":
			RunPatchOp("Exporting diagnostics", func(ctx context.Context) error { return OpExportDiagnostics(ctx, config) })
		case "apply":
			patchModal = &patchModalState{
				ID:       "confirm-swap",
				Title:    "Update JukaHub to " + patchState.LastUpdateVer + "?",
				Body:     "Applying replaces the running executable and restarts JukaHub.\n\nBackup and rollback are automatic.",
				Confirm:  "Apply & Restart",
				HighRisk: true,
			}
		case "restore":
			if len(patchSnap.Backups) > 0 {
				name := patchSnap.Backups[0]
				patchModal = &patchModalState{
					ID:       "confirm-restore",
					Title:    "Restore backup " + name + "?",
					Body:     name,
					Confirm:  "Restore",
					HighRisk: true,
				}
			}
		}
	case strings.HasPrefix(id, "row:"):
		comp := PatchComponent(strings.TrimPrefix(id, "row:"))
		patchRowActivate(config, comp)
	case strings.HasPrefix(id, "pkg:"):
		pkgID := strings.TrimPrefix(id, "pkg:")
		p, _, err := findPackageByID(pkgID)
		if err != nil {
			ShowToast(err.Error(), ToastKindError)
			return
		}
		menu := []string{"Cancel"}
		if !IsPackageInstalled(p) {
			menu = append([]string{"Install"}, menu...)
		} else {
			if !IsPackageCurrent(p) {
				menu = append([]string{"Update"}, menu...)
			} else {
				menu = append([]string{"Reinstall"}, menu...)
			}
			menu = append(menu, "Remove")
		}
		patchModal = &patchModalState{
			ID:          "pkg-menu",
			Title:       p.Name + "  v" + p.Version,
			Body:        p.Description,
			Menu:        menu,
			MenuFocus:   0,
			PackageID:   p.ID,
			PackageName: p.Name,
		}
	case strings.HasPrefix(id, "backup:"):
		name := strings.TrimPrefix(id, "backup:")
		patchModal = &patchModalState{
			ID:       "confirm-restore",
			Title:    "Restore backup " + name + "?",
			Body:     name,
			Confirm:  "Restore",
			HighRisk: true,
		}
	}
}

// patchRowActivate handles A on an update-class row.
func patchRowActivate(config *Config, comp PatchComponent) {
	row := patchRowFor(comp)
	if row == nil || !row.Enabled {
		return
	}
	switch comp {
	case ComponentApp:
		if patchState.LastUpdateVer != "" {
			patchModal = &patchModalState{
				ID:       "confirm-swap",
				Title:    "Update JukaHub to " + patchState.LastUpdateVer + "?",
				Body:     "Applying replaces the running executable and restarts JukaHub.",
				Confirm:  "Apply & Restart",
				HighRisk: true,
			}
			return
		}
		RunPatchOp("Checking for updates", func(ctx context.Context) error { return OpCheckUpdates(ctx, config) })
	case ComponentAssets:
		RunPatchOp("Verifying config & assets", func(ctx context.Context) error { return OpVerifyAssets(ctx, config) })
	case ComponentTools:
		RunPatchOp("Verifying helper tools", func(ctx context.Context) error { return OpVerifyTools(ctx, config) })
	case ComponentPackages:
		openPatchRepoEditor(config)
	}
}

func patchRowFor(comp PatchComponent) *PatchRow {
	if patchSnap == nil {
		return nil
	}
	for i := range patchSnap.Rows {
		if patchSnap.Rows[i].Component == comp {
			return &patchSnap.Rows[i]
		}
	}
	return nil
}

// openPatchRepoEditor opens the patch state directory in the File Explorer so
// the user can open/edit packages.json (on the device, or on a desktop build
// with any text editor). Returning to Patch reloads the repo.
func openPatchRepoEditor(config *Config) {
	path := mustRepoPath()
	if _, err := os.Stat(path); err != nil {
		ShowToast("No packages.json yet — created on first Patch use", ToastKindInfo)
		return
	}
	dir := filepath.Dir(path)
	config.Variables.Custom["fe_path"] = dir
	config.Variables.Custom["fe_entries"] = nil
	if idx := findSceneIndex(config, "FileExplorer"); idx >= 0 {
		changeSceneTo(config, idx)
	} else {
		ShowToast("Open "+path+" in your editor", ToastKindInfo)
	}
}

// --- Event handling (called from the main loop each frame) ---

// patchDrainEvents drains the worker event channel and applies updates.
func patchDrainEvents() {
	DrainPatchEvents(func(ev PatchEvent) {
		switch ev.Kind {
		case PatchEventState:
			if ev.Row != "" {
				for i := range patchSnap.Rows {
					if patchSnap.Rows[i].Component == ev.Row && ev.Status != "" {
						patchSnap.Rows[i].Status = ev.Status
					}
				}
			}
		case PatchEventToast:
			kind := ToastKindInfo
			if strings.Contains(ev.Text, "failed") || strings.Contains(ev.Text, "error") {
				kind = ToastKindError
			}
			ShowToast(ev.Text, kind)
		case PatchEventModal:
			if ev.Modal != nil {
				patchModal = &patchModalState{
					ID:       ev.Modal.ID,
					Title:    ev.Modal.Title,
					Body:     ev.Modal.Body,
					Confirm:  ev.Modal.Confirm,
					HighRisk: ev.Modal.HighRisk,
				}
			}
		case PatchEventSnap:
			if ev.Snap != nil {
				patchSnap = ev.Snap
			}
		}
	})
	handlePatchHoldUpdate()
}

// PatchSceneFrame is a convenience called from the main loop every frame when
// the current scene is Patch: drains events and refreshes the busy label.
func PatchSceneFrame() {
	if currentSceneIndex >= 0 && currentSceneIndex < len(appConfig.Scenes) && appConfig.Scenes[currentSceneIndex].Name == "Patch" {
		patchDrainEvents()
	}
}

// patchFooterHints returns the footer hint override for Patch (nil, false
// keeps the generic hints).
func patchFooterHints() ([]FooterHint, bool) {
	if patchModal != nil {
		if patchModal.HighRisk {
			return []FooterHint{
				{Button: "Hold A", Label: "Confirm"},
				{Button: "B", Label: "Cancel"},
			}, true
		}
		return []FooterHint{
			{Button: "A", Label: "OK"},
			{Button: "B", Label: "Cancel"},
		}, true
	}
	return nil, false
}

// formatPatchTime is a tiny helper for display.
func formatPatchTime(t time.Time) string {
	return t.Format("2006-01-02 15:04")
}
