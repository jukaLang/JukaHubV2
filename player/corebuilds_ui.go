package main

// corebuilds_ui.go — the kept core builds panel.
//
// Every core update keeps the binary it replaced as `<name>.jukahub-<stamp>.bak`
// beside the active core, and prunes them to maxCoreBackups per core. That cap
// is a safety net, not a review: the files sit across every RetroArch cores
// folder the card has, and until now the only way to see them was a file
// manager. This panel is that review — one row per kept build with the date it
// was replaced and its size — and the pruning, by hand, one build at a time.
//
// It is deliberately small: a list, a footer, and two actions. Restoring is the
// same call the Emulators page's R1 makes; deleting goes through
// deleteCoreBackup, so the guard is the rollback's guard.

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// coreBuildsConfirmWindow is how long the first X press stays armed.
	// Deleting a kept build is the one action here that cannot be undone, so it
	// takes two presses — the same rule the Wi-Fi picker applies to forgetting
	// a network.
	coreBuildsConfirmWindow = 6 * time.Second
	// coreBuildsRowH is one row: a file name with its size, then the date.
	coreBuildsRowH = int32(66)
)

type coreBuildsPanelState struct {
	open   bool
	cursor int
	scroll int
	rows   []coreBackup
	total  int64
	// confirm is the path armed by the first X press; confirmAt is when that
	// press happened, so the arm expires on its own.
	confirm   string
	confirmAt time.Time
	// returnEmu re-opens the Emulators page on close, so the panel behaves like
	// a child of the page that launched it.
	returnEmu bool
}

var kbPanel coreBuildsPanelState

type coreBuildsRowHit struct {
	index int
	rect  sdl.Rect
}

// coreBuildsRowHits and coreBuildsPanelRect are filled by
// renderCoreBuildsPanel: the visible row rects and the panel rect. Only touched
// on the render/event thread.
var coreBuildsRowHits []coreBuildsRowHit
var coreBuildsPanelRect sdl.Rect

// ── Opening and data ───────────────────────────────────────────────────────

// openCoreBuildsPanel shows the list. returnEmu puts the Emulators page back
// when the panel closes, the way the compatibility panel does.
func openCoreBuildsPanel(returnEmu bool) {
	// Never stack modals: this panel replaces the page that opened it.
	qs.open = false
	toolsPanel.open = false
	emuPanel.open = false
	cpPanel.open = false
	kbPanel = coreBuildsPanelState{open: true, returnEmu: returnEmu}
	coreBuildsPanelRect = sdl.Rect{}
	coreBuildsRowHits = nil
	coreBuildsRefresh()
	PlayActivateSound()
}

func closeCoreBuildsPanel() {
	if !kbPanel.open {
		return
	}
	returnEmu := kbPanel.returnEmu
	kbPanel = coreBuildsPanelState{}
	coreBuildsPanelRect = sdl.Rect{}
	coreBuildsRowHits = nil
	if returnEmu {
		emuPanel.open = true
		emuPanelRect = sdl.Rect{}
		emuRowHits = nil
	}
	PlayBackSound()
}

// coreBuildsRefresh re-reads the card. It runs on open, after every action that
// changed a file, and on Y: the panel's whole value is that it lists what is on
// the card now, not what was there when it opened.
func coreBuildsRefresh() {
	kbPanel.rows = scanCoreBackups()
	kbPanel.total = coreBackupsTotal(kbPanel.rows)
	kbPanel.confirm = ""
	if kbPanel.cursor >= len(kbPanel.rows) {
		kbPanel.cursor = len(kbPanel.rows) - 1
	}
	if kbPanel.cursor < 0 {
		kbPanel.cursor = 0
	}
	if kbPanel.scroll > kbPanel.cursor {
		kbPanel.scroll = kbPanel.cursor
	}
}

func coreBuildsSelected() (coreBackup, bool) {
	if kbPanel.cursor < 0 || kbPanel.cursor >= len(kbPanel.rows) {
		return coreBackup{}, false
	}
	return kbPanel.rows[kbPanel.cursor], true
}

func coreBuildsMove(delta int) {
	if len(kbPanel.rows) == 0 {
		return
	}
	next := kbPanel.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= len(kbPanel.rows) {
		next = len(kbPanel.rows) - 1
	}
	if next == kbPanel.cursor {
		return
	}
	kbPanel.cursor = next
	// Moving off a row disarms it: a confirm that survived a cursor move would
	// delete a build the user is no longer looking at.
	kbPanel.confirm = ""
	PlayNavSound()
}

// emuKeptBuildsHint is the Emulators footer's entry point. It reads the same
// per-frame cache the R1 hint uses, so the page never walks the cores folders
// while drawing.
func emuKeptBuildsHint() string {
	emuCorePresence.mu.Lock()
	defer emuCorePresence.mu.Unlock()
	if !emuCorePresence.built {
		emuCorePresenceBuild()
	}
	if len(emuCorePresence.backups) == 0 {
		return ""
	}
	// "List" is short on purpose. The Emulators line already carries six groups
	// on the Brick with 84px to spare, and "R2 List" is the only wording this
	// panel fits in: "Kept builds" and even "Builds" push the line past the
	// Brick's body, where the B hint at the end would be clipped.
	return "List"
}

// ── Actions ────────────────────────────────────────────────────────────────

// coreBuildsActivate restores the highlighted build over the core it came from.
// The build it replaces is kept, so this is itself reversible.
func coreBuildsActivate() {
	backup, ok := coreBuildsSelected()
	if !ok {
		ShowToast("No kept builds to restore", ToastKindInfo)
		return
	}
	build, err := rollbackCoreBuild(backup)
	if err != nil {
		ShowToast("Restore failed: "+err.Error(), ToastKindError)
		return
	}
	// The tile list, the compatibility panel, the footer's cache and this list
	// all read the cores folder; refresh them together so no screen shows the
	// picture from before the copy.
	invalidateEmulatorCache()
	invalidateCompatCache()
	invalidateEmuCorePresence()
	coreBuildsRefresh()
	PlaySuccessSound()
	ShowToast(coreConfigDisplayName(build.Core)+" restored · "+
		backup.Stamp.Format("2006-01-02 15:04")+" build · the replaced build is kept",
		ToastKindSuccess)
}

// coreBuildsDelete arms the highlighted build, and removes it on the second
// press inside the window. Deleting is the one action here that cannot be
// undone, which is why it is the one that takes two presses.
func coreBuildsDelete() {
	backup, ok := coreBuildsSelected()
	if !ok {
		ShowToast("No kept builds to delete", ToastKindInfo)
		return
	}
	now := time.Now()
	armed := kbPanel.confirm == backup.Path && now.Sub(kbPanel.confirmAt) <= coreBuildsConfirmWindow
	if !armed {
		kbPanel.confirm = backup.Path
		kbPanel.confirmAt = now
		ShowToast("Press X again to delete "+filepath.Base(backup.Path), ToastKindWarning)
		return
	}
	if err := deleteCoreBackup(backup); err != nil {
		ShowToast("Delete failed: "+err.Error(), ToastKindError)
		return
	}
	invalidateEmulatorCache()
	invalidateCompatCache()
	invalidateEmuCorePresence()
	coreBuildsRefresh()
	PlayBackSound()
	ShowToast("Deleted "+filepath.Base(backup.Path)+" · "+formatInstallBytes(backup.Size)+" freed", ToastKindSuccess)
}

func coreBuildsRescan() {
	coreBuildsRefresh()
	PlayNavSound()
	ShowToast(fmt.Sprintf("%d kept build(s) · %s", len(kbPanel.rows), formatInstallBytes(kbPanel.total)), ToastKindInfo)
}

// ── Input ──────────────────────────────────────────────────────────────────

func ctrlCoreBuildsPanel(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		coreBuildsMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		coreBuildsMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		coreBuildsActivate()
	case sdl.CONTROLLER_BUTTON_X:
		coreBuildsDelete()
	case sdl.CONTROLLER_BUTTON_Y:
		coreBuildsRescan()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeCoreBuildsPanel()
	}
	return true
}

// handleCoreBuildsKey consumes every key while the panel is open, so the scene
// underneath never reacts behind the modal.
func handleCoreBuildsKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !kbPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		coreBuildsMove(-1)
	case sdl.K_DOWN:
		coreBuildsMove(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		coreBuildsActivate()
	case sdl.K_x, sdl.K_DELETE:
		coreBuildsDelete()
	case sdl.K_y, sdl.K_r:
		coreBuildsRescan()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeCoreBuildsPanel()
	}
	return true
}

// handleCoreBuildsMouse activates a clicked row or closes the panel on a click
// outside it, matching the other modal lists.
func handleCoreBuildsMouse(mx, my int32) bool {
	if !kbPanel.open {
		return false
	}
	for _, hit := range coreBuildsRowHits {
		if pointInRect(mx, my, hit.rect) {
			kbPanel.cursor = hit.index
			kbPanel.confirm = ""
			coreBuildsActivate()
			return true
		}
	}
	if coreBuildsPanelRect.W > 0 && pointInRect(mx, my, coreBuildsPanelRect) {
		// Clicks inside the panel but off a row only consume.
		return true
	}
	closeCoreBuildsPanel()
	return true
}

// ── Text ───────────────────────────────────────────────────────────────────

// coreBuildAge turns a stamp into the phrase a person reviewing builds thinks
// in, so the list reads as "yesterday" rather than as an arithmetic problem.
func coreBuildAge(stamp, now time.Time) string {
	age := now.Sub(stamp)
	switch {
	case age < 0:
		return "stamped in the future"
	case age < 12*time.Hour:
		return "today"
	case age < 48*time.Hour:
		return "yesterday"
	case age < 14*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(age.Hours()/24))
	case age < 60*24*time.Hour:
		return fmt.Sprintf("%d weeks ago", int(age.Hours()/24/7))
	case age < 730*24*time.Hour:
		return fmt.Sprintf("%d months ago", int(age.Hours()/24/30))
	default:
		return fmt.Sprintf("%d years ago", int(age.Hours()/24/365))
	}
}

// coreBuildsSubtitle is the line under the title: how many builds, how much
// card, and the rule that keeps the pile from growing.
func coreBuildsSubtitle(rows []coreBackup, total int64) string {
	if len(rows) == 0 {
		return "No kept builds · an update keeps the build it replaced"
	}
	return fmt.Sprintf("%d kept · %s · up to %d per core",
		len(rows), formatInstallBytes(total), maxCoreBackups)
}

// coreBuildsFileLabel is a row's title: the core file being kept, without the
// backup stamp, because the date has its own line.
func coreBuildsFileLabel(b coreBackup) string {
	name := filepath.Base(b.Target)
	if name == "" || name == "." {
		name = filepath.Base(b.Path)
	}
	return name
}

// coreBuildsMeta is a row's second line: when the build was replaced, how long
// ago that was, and the folder it will be restored into.
func coreBuildsMeta(b coreBackup, now time.Time) string {
	parts := []string{b.Stamp.Format("2006-01-02 15:04"), coreBuildAge(b.Stamp, now)}
	if dir := filepath.Dir(b.Path); dir != "" && dir != "." {
		parts = append(parts, dir)
	}
	return strings.Join(parts, " · ")
}

// coresRootLabel is the cores folder a user would look at in a file manager:
// the first one the card has, so an empty list still says where it looked.
func coresRootLabel() string {
	dirs := retroArchCoreDirs()
	if len(dirs) == 0 {
		return "not found"
	}
	return dirs[0]
}

// ── Rendering ──────────────────────────────────────────────────────────────

func renderCoreBuildsPanel(renderer *sdl.Renderer, config *Config) {
	if !kbPanel.open || renderer == nil {
		return
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

	panelW := int32(880)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := int32(540)
	if panelH > screenHeight-40 {
		panelH = screenHeight - 40
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	coreBuildsPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	const padX = int32(24)
	header := UIPanelHeader("Kept core builds", coreBuildsSubtitle(kbPanel.rows, kbPanel.total), "packages")
	headerH := UIPanelHeaderBand(config, header, 10)
	footerH := 18 + smallH
	bodyY := panelY + headerH
	bodyH := panelH - headerH - footerH

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

	focusColor := installHighlight()
	coreBuildsRowHits = coreBuildsRowHits[:0]
	rowW := panelW - 2*padX

	if len(kbPanel.rows) == 0 {
		lineY := bodyY + 10
		renderText(renderer, config, medium, "No kept builds on this card", ColorTextPrimary(), panelX+padX, lineY)
		lineY += int32(medium.Height()) + 10
		for _, line := range compatTextLines(font,
			"An update keeps the build it replaced beside the core it came from, so a nightly that behaves worse can be undone without a card reader. This card has none right now; the Emulators page keeps up to five per core once it updates one.",
			rowW) {
			renderText(renderer, config, font, line, ColorTextTertiary(), panelX+padX, lineY)
			lineY += smallH
		}
		lineY += 8
		for _, line := range compatTextLines(font, "Cores folder: "+coresRootLabel(), rowW) {
			renderText(renderer, config, font, line, ColorTextSecondary(), panelX+padX, lineY)
			lineY += smallH
		}
	} else {
		maxRows := bodyH / coreBuildsRowH
		if maxRows < 1 {
			maxRows = 1
		}
		visible := int32(len(kbPanel.rows))
		if visible > maxRows {
			visible = maxRows
		}
		// Keep the cursor inside the window, then clamp the window itself so
		// the last page is full instead of half empty.
		if kbPanel.cursor < kbPanel.scroll {
			kbPanel.scroll = kbPanel.cursor
		}
		if kbPanel.cursor >= kbPanel.scroll+int(visible) {
			kbPanel.scroll = kbPanel.cursor - int(visible) + 1
		}
		if kbPanel.scroll > len(kbPanel.rows)-int(visible) {
			kbPanel.scroll = len(kbPanel.rows) - int(visible)
		}
		if kbPanel.scroll < 0 {
			kbPanel.scroll = 0
		}

		now := time.Now()
		for i := int32(0); i < visible; i++ {
			idx := kbPanel.scroll + int(i)
			if idx >= len(kbPanel.rows) {
				break
			}
			backup := kbPanel.rows[idx]
			r := sdl.Rect{X: panelX + padX, Y: bodyY + i*coreBuildsRowH, W: rowW, H: coreBuildsRowH - 8}
			coreBuildsRowHits = append(coreBuildsRowHits, coreBuildsRowHit{index: idx, rect: r})
			focused := idx == kbPanel.cursor
			armed := kbPanel.confirm != "" && kbPanel.confirm == backup.Path
			rowBg := WithAlpha(ColorSurfaceRow, 190)
			if focused {
				rowBg = WithAlpha(focusColor, 46)
			}
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
			switch {
			case armed:
				strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 2, ColorToastError)
			case focused:
				strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
			}

			// The size sits at the right edge, measured, so the name is
			// ellipsized into exactly the room that is left.
			size := formatInstallBytes(backup.Size)
			sizeW, _, _ := font.SizeUTF8(size)
			renderText(renderer, config, medium,
				ellipsize(medium, coreBuildsFileLabel(backup), r.W-int32(sizeW)-42),
				ColorTextPrimary(), r.X+14, r.Y+7)
			renderText(renderer, config, font, size, ColorTextSecondary(),
				r.X+r.W-14-int32(sizeW), r.Y+7+int32(medium.Height()-font.Height())/2)

			meta := coreBuildsMeta(backup, now)
			metaColor := ColorTextTertiary()
			if armed {
				meta = "Press X again to delete this build"
				metaColor = ColorToastError
			}
			renderText(renderer, config, font, ellipsize(font, meta, r.W-28), metaColor,
				r.X+14, r.Y+7+int32(medium.Height())+3)
		}
		if int(visible) < len(kbPanel.rows) {
			top := bodyY
			drawScrollbar(renderer, panelX+panelW-14, top, 3, bodyH-14,
				float64(visible)/float64(len(kbPanel.rows)),
				float64(kbPanel.scroll)/float64(len(kbPanel.rows)-int(visible)))
		}
	}

	var hints []FooterHint
	if kbPanel.confirm != "" {
		hints = []FooterHint{{Button: "X", Label: "Delete build"}, {Button: "B", Label: "Cancel"}}
	} else {
		hints = []FooterHint{
			{Button: "D-Pad", Label: "Select"},
			{Button: "A", Label: "Restore"},
			{Button: "X", Label: "Delete"},
			{Button: "Y", Label: "Rescan"},
			{Button: "B", Label: "Back"},
		}
	}
	// Measured against the panel, not hoped: the Brick's canvas is narrower
	// than the Smart Pro's, and a hint under the border is unreadable.
	renderText(renderer, config, font,
		ellipsize(font, hintLine(hints...), rowW),
		ColorTextSecondary(), panelX+padX, panelY+panelH-footerH+6)
}
