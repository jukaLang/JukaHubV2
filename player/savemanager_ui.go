package main

// savemanager_ui.go — the Save Manager overlay: snapshot, browse, restore,
// prepare Syncthing and export a phone resume bundle.
//
// It is a modal like the Ports and Emulators panels, so it works on every
// config without a dedicated scene. The panel is intentionally plain: a list
// of rows with the action on A and the destructive restore confirmed by a
// second A press, because the handheld has no room for another dialog.

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/veandco/go-sdl2/sdl"
)

type saveManagerAction int

const (
	saveActionSnapshot saveManagerAction = iota
	saveActionRestoreLatest
	saveActionBrowse
	saveActionFolders
	saveActionSync
	saveActionResume
	saveActionRefresh
)

type saveManagerRow struct {
	label   string
	value   string
	detail  string
	hint    string
	action  saveManagerAction
	enabled bool
}

type saveManagerMode int

const (
	saveModeMain saveManagerMode = iota
	saveModeSnapshots
)

type saveManagerPanelState struct {
	open      bool
	mode      saveManagerMode
	cursor    int
	scroll    int
	roots     []saveRoot
	snapshots []saveSnapshot
	sync      saveSyncManifest
	syncReady bool
	resume    string
	resumeOK  bool
	confirm   string
	rect      sdl.Rect
}

var savePanel saveManagerPanelState

type saveRowHit struct {
	index int
	rect  sdl.Rect
}

var saveRowHits []saveRowHit

func openSaveManagerPanel() {
	qs.open = false
	toolsPanel.open = false
	emuPanel.open = false
	gpPanel.open = false
	raPanel.open = false
	cpPanel.open = false
	winAppsPanel = windowsAppsPanelState{}
	portsPanel = portsPanelState{}
	castPanel.open = false
	musicSettings.open = false
	a11y.open = false
	smartHomePanel.open = false
	if calibrationPanel.open {
		closeStickCalibrationPanel()
	}
	chiakiPanel = chiakiPanelState{}

	savePanel = saveManagerPanelState{
		open:   true,
		mode:   saveModeMain,
		cursor: 0,
		scroll: 0,
		rect:   sdl.Rect{},
	}
	saveRowHits = nil
	refreshSaveManager()
	PlayActivateSound()
}

func closeSaveManagerPanel() {
	if !savePanel.open {
		return
	}
	savePanel = saveManagerPanelState{}
	saveRowHits = nil
	PlayBackSound()
}

// refreshSaveManager re-reads the live folders and snapshots. The panel is
// opened rarely and the lists are small, so there is no cache to invalidate.
func refreshSaveManager() {
	savePanel.roots = saveManagerRoots()
	savePanel.snapshots = listSaveSnapshots()
	savePanel.sync, savePanel.syncReady = saveSyncStatus()
	savePanel.resume, savePanel.resumeOK = latestResumeBundle()
	savePanel.confirm = ""
	clampSaveManagerCursor()
}

func clampSaveManagerCursor() {
	rows := saveManagerRows()
	if len(rows) == 0 {
		savePanel.cursor = 0
		savePanel.scroll = 0
		return
	}
	if savePanel.cursor < 0 {
		savePanel.cursor = 0
	}
	if savePanel.cursor >= len(rows) {
		savePanel.cursor = len(rows) - 1
	}
}

func saveSnapshotTime(snap saveSnapshot) string {
	return snap.Manifest.Created.Local().Format("2006-01-02 15:04")
}

func saveSnapshotFileCount(snap saveSnapshot) int {
	n := 0
	for _, root := range snap.Manifest.Roots {
		n += len(root.Files)
	}
	return n
}

func saveSnapshotRootCount(snap saveSnapshot) int {
	return len(snap.Manifest.Roots)
}

func saveSnapshotDetail(snap saveSnapshot) string {
	roots := saveSnapshotRootCount(snap)
	files := saveSnapshotFileCount(snap)
	noun := "folders"
	if roots == 1 {
		noun = "folder"
	}
	return fmt.Sprintf("%d file(s) across %d %s · %s", files, roots, noun, snap.Manifest.ID)
}

func saveFolderNames(roots []saveRoot) string {
	if len(roots) == 0 {
		return "None yet"
	}
	names := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, root := range roots {
		if seen[root.Name] {
			continue
		}
		seen[root.Name] = true
		names = append(names, root.Name)
	}
	if len(names) > 3 {
		return fmt.Sprintf("%s +%d more", joinComma(names[:3]), len(names)-3)
	}
	return joinComma(names)
}

func joinComma(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += ", "
		}
		out += part
	}
	return out
}

func saveManagerRows() []saveManagerRow {
	if savePanel.mode == saveModeSnapshots {
		if len(savePanel.snapshots) == 0 {
			return []saveManagerRow{{
				label:   "No snapshots yet",
				detail:  "Press B and choose Snapshot now to create the first one.",
				enabled: false,
			}}
		}
		rows := make([]saveManagerRow, 0, len(savePanel.snapshots))
		for _, snap := range savePanel.snapshots {
			rows = append(rows, saveManagerRow{
				label:   saveSnapshotTime(snap) + " · " + snap.Manifest.Label,
				value:   humanBytes(snap.Manifest.TotalBytes),
				detail:  saveSnapshotDetail(snap),
				hint:    hintLine(FooterHint{"A", "Restore"}),
				enabled: true,
			})
		}
		return rows
	}

	latest := "None"
	if len(savePanel.snapshots) > 0 {
		latest = saveSnapshotTime(savePanel.snapshots[0])
	}
	syncValue := "Not set up"
	if savePanel.syncReady {
		syncValue = fmt.Sprintf("%d folder(s)", len(savePanel.sync.Folders))
	}
	resumeValue := "None"
	if savePanel.resumeOK {
		resumeValue = filepath.Base(savePanel.resume)
	}
	snapshotValue := "0 saved"
	if len(savePanel.snapshots) > 0 {
		snapshotValue = fmt.Sprintf("%d saved", len(savePanel.snapshots))
	}
	return []saveManagerRow{
		{
			label:   "Snapshot now",
			value:   snapshotValue,
			detail:  "Copy every save folder into a versioned snapshot.",
			hint:    hintLine(FooterHint{"A", "Snapshot"}),
			action:  saveActionSnapshot,
			enabled: len(savePanel.roots) > 0,
		},
		{
			label:   "Restore latest",
			value:   latest,
			detail:  "Takes a safety snapshot first. Press A twice to confirm.",
			hint:    hintLine(FooterHint{"A", "Restore"}),
			action:  saveActionRestoreLatest,
			enabled: len(savePanel.snapshots) > 0,
		},
		{
			label:   "Browse snapshots",
			value:   fmt.Sprintf("%d", len(savePanel.snapshots)),
			detail:  "Older copies stay on the card; ten are kept.",
			hint:    hintLine(FooterHint{"A", "Open"}),
			action:  saveActionBrowse,
			enabled: len(savePanel.snapshots) > 0,
		},
		{
			label:   "Save folders",
			value:   fmt.Sprintf("%d", len(savePanel.roots)),
			detail:  saveFolderNames(savePanel.roots),
			hint:    hintLine(FooterHint{"A", "Rescan"}),
			action:  saveActionFolders,
			enabled: true,
		},
		{
			label:   "Syncthing setup",
			value:   syncValue,
			detail:  "Mark each save folder and write the folder IDs to data/save-manager/sync.",
			hint:    hintLine(FooterHint{"A", "Prepare"}),
			action:  saveActionSync,
			enabled: len(savePanel.roots) > 0,
		},
		{
			label:   "Phone resume bundle",
			value:   resumeValue,
			detail:  "Zip the newest snapshot for the Companion or FTP file transfer.",
			hint:    hintLine(FooterHint{"A", "Create"}),
			action:  saveActionResume,
			enabled: len(savePanel.snapshots) > 0,
		},
		{
			label:   "Refresh",
			detail:  "Rescan save folders, snapshots and the sync manifest.",
			hint:    hintLine(FooterHint{"A", "Refresh"}),
			action:  saveActionRefresh,
			enabled: true,
		},
	}
}

func saveManagerMove(delta int) {
	rows := saveManagerRows()
	if len(rows) == 0 {
		return
	}
	savePanel.cursor += delta
	if savePanel.cursor < 0 {
		savePanel.cursor = len(rows) - 1
	}
	if savePanel.cursor >= len(rows) {
		savePanel.cursor = 0
	}
	savePanel.confirm = ""
	PlayNavSound()
}

func saveManagerPage(dir int) {
	rows := saveManagerRows()
	if len(rows) == 0 {
		return
	}
	savePanel.cursor += dir * 5
	if savePanel.cursor < 0 {
		savePanel.cursor = 0
	}
	if savePanel.cursor >= len(rows) {
		savePanel.cursor = len(rows) - 1
	}
	savePanel.confirm = ""
	PlayNavSound()
}

func saveManagerActivate() {
	if savePanel.mode == saveModeSnapshots {
		if savePanel.cursor < 0 || savePanel.cursor >= len(savePanel.snapshots) {
			return
		}
		snap := savePanel.snapshots[savePanel.cursor]
		if savePanel.confirm != snap.Manifest.ID {
			savePanel.confirm = snap.Manifest.ID
			ShowToast("Press A again to restore "+saveSnapshotTime(snap), ToastKindInfo)
			return
		}
		restoreSaveManagerSnapshot(snap)
		return
	}

	rows := saveManagerRows()
	if savePanel.cursor < 0 || savePanel.cursor >= len(rows) {
		return
	}
	row := rows[savePanel.cursor]
	if !row.enabled {
		ShowToast("Nothing to do for this row yet", ToastKindInfo)
		return
	}
	switch row.action {
	case saveActionSnapshot:
		snap, err := createSaveSnapshot(savePanel.roots, "manual")
		if err != nil {
			saveManagerError("Could not snapshot saves", err)
			return
		}
		refreshSaveManager()
		ShowToast("Snapshot "+snap.Manifest.ID+" saved ("+humanBytes(snap.Manifest.TotalBytes)+")", ToastKindSuccess)
	case saveActionRestoreLatest:
		snap, ok := latestSaveSnapshot()
		if !ok {
			ShowToast("No snapshots to restore", ToastKindInfo)
			return
		}
		if savePanel.confirm != snap.Manifest.ID {
			savePanel.confirm = snap.Manifest.ID
			ShowToast("Press A again to restore "+saveSnapshotTime(snap), ToastKindWarning)
			return
		}
		restoreSaveManagerSnapshot(snap)
	case saveActionBrowse:
		savePanel.mode = saveModeSnapshots
		savePanel.cursor = 0
		savePanel.scroll = 0
		savePanel.confirm = ""
		PlayActivateSound()
	case saveActionFolders:
		refreshSaveManager()
		ShowToast(saveManagerSummary(savePanel.roots, savePanel.snapshots), ToastKindInfo)
	case saveActionSync:
		manifest, err := prepareSaveSync(savePanel.roots)
		if err != nil {
			saveManagerError("Could not prepare save sync", err)
			return
		}
		refreshSaveManager()
		ShowToast(fmt.Sprintf("Save sync prepared for %d folder(s) — add them in Syncthing", len(manifest.Folders)), ToastKindSuccess)
	case saveActionResume:
		snap, ok := latestSaveSnapshot()
		if !ok {
			ShowToast("No snapshots to bundle", ToastKindInfo)
			return
		}
		path, err := createResumeBundle(snap)
		if err != nil {
			saveManagerError("Could not create the resume bundle", err)
			return
		}
		refreshSaveManager()
		ShowToast("Resume bundle ready: "+filepath.Base(path), ToastKindSuccess)
	case saveActionRefresh:
		refreshSaveManager()
		ShowToast("Save folders refreshed", ToastKindSuccess)
	}
}

func restoreSaveManagerSnapshot(snap saveSnapshot) {
	pre, err := restoreSaveSnapshot(snap, savePanel.roots)
	if err != nil {
		saveManagerError("Could not restore saves", err)
		return
	}
	refreshSaveManager()
	savePanel.mode = saveModeMain
	savePanel.confirm = ""
	msg := "Restored " + saveSnapshotTime(snap)
	if pre.Manifest.ID != "" {
		msg += " · safety copy " + pre.Manifest.ID
	}
	PlaySuccessSound()
	ShowToast(msg, ToastKindSuccess)
}

func saveManagerError(prefix string, err error) {
	if errors.Is(err, errSaveManagerEmpty) {
		ShowToast("Save folders are empty — start a game once", ToastKindInfo)
		return
	}
	ShowToast(prefix+": "+firstLine(err.Error()), ToastKindError)
}

func saveManagerDelete() {
	if savePanel.mode != saveModeSnapshots {
		return
	}
	if savePanel.cursor < 0 || savePanel.cursor >= len(savePanel.snapshots) {
		return
	}
	snap := savePanel.snapshots[savePanel.cursor]
	if savePanel.confirm != "delete:"+snap.Manifest.ID {
		savePanel.confirm = "delete:" + snap.Manifest.ID
		ShowToast("Press X again to delete "+saveSnapshotTime(snap), ToastKindWarning)
		return
	}
	if err := deleteSaveSnapshot(snap); err != nil {
		saveManagerError("Could not delete snapshot", err)
		return
	}
	refreshSaveManager()
	if savePanel.cursor >= len(saveManagerRows()) {
		savePanel.cursor = len(saveManagerRows()) - 1
	}
	savePanel.confirm = ""
	ShowToast("Snapshot deleted", ToastKindSuccess)
}

func saveManagerBack() {
	if savePanel.mode == saveModeSnapshots {
		savePanel.mode = saveModeMain
		savePanel.cursor = 0
		savePanel.scroll = 0
		savePanel.confirm = ""
		PlayBackSound()
		return
	}
	closeSaveManagerPanel()
}

func ctrlSaveManager(e *sdl.ControllerButtonEvent) bool {
	if e == nil || !savePanel.open {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		saveManagerMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		saveManagerMove(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		saveManagerPage(-1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		saveManagerPage(1)
	case sdl.CONTROLLER_BUTTON_A:
		saveManagerActivate()
	case sdl.CONTROLLER_BUTTON_X:
		saveManagerDelete()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		saveManagerBack()
	}
	return true
}

func handleSaveManagerKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !savePanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		saveManagerMove(-1)
	case sdl.K_DOWN:
		saveManagerMove(1)
	case sdl.K_PAGEUP:
		saveManagerPage(-1)
	case sdl.K_PAGEDOWN:
		saveManagerPage(1)
	case sdl.K_HOME:
		savePanel.cursor = 0
		savePanel.confirm = ""
	case sdl.K_END:
		if rows := saveManagerRows(); len(rows) > 0 {
			savePanel.cursor = len(rows) - 1
			savePanel.confirm = ""
		}
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		saveManagerActivate()
	case sdl.K_x, sdl.K_DELETE:
		saveManagerDelete()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		saveManagerBack()
	}
	return true
}

func handleSaveManagerMouse(mx, my int32) bool {
	if !savePanel.open {
		return false
	}
	for _, hit := range saveRowHits {
		if pointInRect(mx, my, hit.rect) {
			// A second tap on the same row is the confirming press; only a tap
			// on another row drops a pending confirmation. Clearing it on every
			// tap re-armed the restore each time, so it could never complete.
			if hit.index != savePanel.cursor {
				savePanel.cursor = hit.index
				savePanel.confirm = ""
			}
			saveManagerActivate()
			return true
		}
	}
	if savePanel.rect.W > 0 && pointInRect(mx, my, savePanel.rect) {
		return true
	}
	closeSaveManagerPanel()
	return true
}

// renderSaveManagerPanel draws the modal list. Snapshot mode is the same card
// with one row per copy, so the user never loses the header or the way back.
func renderSaveManagerPanel(renderer *sdl.Renderer, config *Config) {
	if !savePanel.open || renderer == nil {
		return
	}
	rows := saveManagerRows()
	if len(rows) == 0 {
		rows = []saveManagerRow{{label: "No saves found", detail: "Start a game once, then refresh.", enabled: false}}
	}
	clampSaveManagerCursor()

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

	// The title band is the shared page header, built before the card is sized
	// so the panel and the bar that fills it measure the same height.
	title := "Save Manager"
	if savePanel.mode == saveModeSnapshots {
		title = "Save Manager · Snapshots"
	}
	sub := saveManagerSummary(savePanel.roots, savePanel.snapshots)
	if savePanel.mode == saveModeSnapshots {
		sub = "Newest first · A restores · X deletes"
	}
	header := UIPanelHeader(title, sub, "files")
	headerH := UIPanelHeaderBand(config, header, 14)
	footerH := 20 + smallH

	panelW := int32(860)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	maxRows := (screenHeight - 40 - headerH - footerH) / rowH
	if maxRows < 3 {
		maxRows = 3
	}
	visible := int32(len(rows))
	if visible > maxRows {
		visible = maxRows
	}
	panelH := headerH + visible*rowH + footerH
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	savePanel.rect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	if savePanel.cursor < savePanel.scroll {
		savePanel.scroll = savePanel.cursor
	}
	if savePanel.cursor >= savePanel.scroll+int(visible) {
		savePanel.scroll = savePanel.cursor - int(visible) + 1
	}
	if savePanel.scroll > len(rows)-int(visible) {
		savePanel.scroll = len(rows) - int(visible)
	}
	if savePanel.scroll < 0 {
		savePanel.scroll = 0
	}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
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
	rowW := panelW - 2*padX
	saveRowHits = make([]saveRowHit, 0, visible)
	for i := int32(0); i < visible; i++ {
		idx := savePanel.scroll + int(i)
		row := rows[idx]
		y := panelY + headerH + i*rowH
		r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
		saveRowHits = append(saveRowHits, saveRowHit{index: idx, rect: r})

		focused := idx == savePanel.cursor
		rowBg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			rowBg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}

		labelColor := ColorTextPrimary()
		valueColor := ColorTextSecondary()
		if !row.enabled {
			labelColor = ColorTextTertiary()
			valueColor = ColorTextTertiary()
		}
		renderText(renderer, config, medium, ellipsize(medium, row.label, r.W-240), labelColor, r.X+16, r.Y+8)
		if row.value != "" {
			vw, _, _ := font.SizeUTF8(row.value)
			renderText(renderer, config, font, ellipsize(font, row.value, 220), valueColor, r.X+r.W-16-int32(vw), r.Y+8+(medH-smallH)/2)
		}
		if row.detail != "" {
			renderText(renderer, config, font, ellipsize(font, row.detail, r.W-32), ColorTextTertiary(), r.X+16, r.Y+8+medH+4)
		}
	}

	hints := []FooterHint{{"A", "Open"}, {"B", "Close"}}
	if savePanel.mode == saveModeSnapshots {
		hints = []FooterHint{{"A", "Restore"}, {"X", "Delete"}, {"B", "Back"}}
	}
	if savePanel.confirm != "" {
		if savePanel.mode == saveModeSnapshots {
			hints = []FooterHint{{"A Again", "Restore"}, {"X Again", "Delete"}, {"B", "Cancel"}}
		} else {
			hints = []FooterHint{{"A Again", "Restore"}, {"B", "Cancel"}}
		}
	}
	if len(rows) > int(visible) {
		hints = append([]FooterHint{{Button: "L / R", Label: fmt.Sprintf("Page %d-%d of %d",
			savePanel.scroll+1, savePanel.scroll+int(visible), len(rows))}}, hints...)
	}
	hint := hintLine(hints...)
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+padX, panelY+panelH-footerH+8)

	if len(rows) > int(visible) {
		thumbFrac := float64(visible) / float64(len(rows))
		scrollFrac := float64(savePanel.scroll) / float64(len(rows)-int(visible))
		drawScrollbar(renderer, panelX+panelW-8, panelY+headerH, 3, panelH-headerH-footerH, thumbFrac, scrollFrac)
	}
}
