package main

// upch_ui.go — the update channel in the Patch scene, and its Settings rows.
//
// The channel leads the Patch list: an "Update channel" row with the state in
// one line (up to date, downloading with a bar along the row, ready offline,
// installed from the cache), "Install now — no internet needed" while a
// verified release waits in the cache, "Version history" and "Install from
// file…". The last two open glass sheets over the list; their confirmations go
// through the Patch modal (hold A for anything that replaces JukaHub), so
// every install, rollback and delete is confirmed the same way. Controller,
// keyboard, mouse and touch reach every action.

import (
	"context"
	"runtime"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// The sheets the channel opens over the Patch list.
const (
	upchSheetNone = iota
	upchSheetHistory
	upchSheetFiles
)

// upchUI is the sheets' state. It belongs to the render thread; background
// work reports through upchUIPost.
var upchUI struct {
	sheet int
	focus int
	top   int
	// rows are the visible rows' click targets; rows[k] is item rowBase+k.
	rows      []sdl.Rect
	rowBase   int
	card      sdl.Rect
	notesBtn  sdl.Rect
	deleteBtn sdl.Rect
	rescanBtn sdl.Rect
	// The rows' text, built when the data changes rather than every frame.
	icons    []string
	titles   []string
	subs     []string
	badges   []string
	headLine string
	// Version history.
	entries    []uhistEntry
	entriesVer uint64
	// Install from file: gen retires the results of a scan or a check the user
	// walked away from.
	gen      uint64
	scanning bool
	files    []upchFileCandidate
	checking string
	verdict  *upchFileVerdict
	// The wrapped sideload instructions, kept per width.
	helpLines []string
	helpWidth int32
}

// upchUIEvents carries background results to the render thread.
var upchUIEvents = make(chan func(), 8)

// Footer hints of the sheets; allocated once, not every frame.
var (
	upchHintsHistory      = []FooterHint{{Button: "A", Label: "Roll back"}, {Button: "X", Label: "Delete"}, {Button: "Y", Label: "Notes"}, {Button: "B", Label: "Close"}}
	upchHintsHistoryEmpty = []FooterHint{{Button: "B", Label: "Close"}}
	upchHintsFiles        = []FooterHint{{Button: "A", Label: "Check & install"}, {Button: "Y", Label: "Look again"}, {Button: "B", Label: "Close"}}
	upchHintsFilesEmpty   = []FooterHint{{Button: "Y", Label: "Look again"}, {Button: "B", Label: "Close"}}
)

// upchUIPost hands a result to the render thread.
func upchUIPost(fn func()) {
	select {
	case upchUIEvents <- fn:
	default:
	}
}

// upchUIDrain applies the results that arrived since the last frame.
func upchUIDrain() {
	for {
		select {
		case fn := <-upchUIEvents:
			fn()
		default:
			return
		}
	}
}

// upchOnPatchScene reports whether Patch is the scene on screen (main thread).
func upchOnPatchScene() bool {
	return appConfig != nil && currentSceneIndex >= 0 && currentSceneIndex < len(appConfig.Scenes) &&
		appConfig.Scenes[currentSceneIndex].Name == "Patch"
}

// upchSceneFrame runs every frame from PatchSceneFrame, on every scene: it
// applies background results, and closes a sheet the user left Patch without
// closing, so it is not still up on the way back.
func upchSceneFrame() {
	upchUIDrain()
	if upchSheetOpen() && !upchOnPatchScene() {
		upchCloseSheet()
	}
}

// ── Patch rows ───────────────────────────────────────────────────────────────

// upchPatchActions are the channel's rows, which lead the Patch list.
func upchPatchActions() []patchAction {
	v := upchStatusView()
	out := make([]patchAction, 0, 4)
	out = append(out, patchAction{ID: "channel", Label: "Update channel", Sub: v.Line})
	if v.Ready {
		out = append(out, patchAction{
			ID:    "offline-install",
			Label: "Install now — no internet needed",
			Sub:   "JukaHub " + v.ReadyLabel + " · " + v.Verified + " · restarts JukaHub",
		})
	}
	out = append(out, patchAction{ID: "versions", Label: "Version history", Sub: uhistSummaryLine()})
	out = append(out, patchAction{ID: "fromfile", Label: "Install from file…", Sub: "From the SD card, a USB stick or Downloads — no internet needed"})
	return out
}

// upchActivateAction runs a channel row (patchActivate has played the press).
func upchActivateAction(config *Config, action string) {
	switch action {
	case "channel":
		v := upchStatusView()
		switch {
		case v.Running:
			ShowToast("Downloading — it will install without internet", ToastKindInfo)
		case v.Ready:
			upchConfirmCachedInstall()
		case updateAvailable():
			if !IsOnline() {
				ShowToast(offlinePlainNotice, ToastKindWarning)
				return
			}
			upchDownloadNow()
		default:
			RunPatchOp("Checking for updates", func(ctx context.Context) error {
				err := OpCheckUpdates(ctx, config)
				upchNudge()
				return err
			})
		}
	case "offline-install":
		upchConfirmCachedInstall()
	case "versions":
		upchOpenHistory(config)
	case "fromfile":
		upchOpenFiles()
	}
}

// upchConfirmCachedInstall asks before the cached release replaces JukaHub.
func upchConfirmCachedInstall() {
	v := upchStatusView()
	if !v.Ready {
		ShowToast("No update is waiting in the cache", ToastKindInfo)
		return
	}
	body := "The update is already on this device (" + v.Verified + "), so no internet is needed. " +
		"It is checked again before anything is replaced.\n\n" +
		"JukaHub restarts. The version you are on now is kept in Version history."
	if v.Verified == upchVerifiedPhrase(upchVerifiedNone) {
		body += "\n\nThis release published no checksum, so the downloaded copy could not be verified."
	}
	patchModal = &patchModalState{
		ID:       "upch-install-cached",
		Title:    "Install JukaHub " + v.ReadyLabel + " now?",
		Body:     body,
		Confirm:  "Install & Restart",
		HighRisk: true,
	}
}

// upchDrawChannelProgress draws the download's bar along the bottom of the
// "Update channel" row (the first row of the Patch list) while it runs.
func upchDrawChannelProgress(renderer *sdl.Renderer, row sdl.Rect, clipY, clipH int32) {
	if renderer == nil || row.W <= 0 || row.H <= 0 {
		return
	}
	v := upchStatusView()
	if !v.Running {
		return
	}
	bar := sdl.Rect{X: row.X + 16, Y: row.Y + row.H - 8, W: row.W - 32, H: 4}
	renderWithClip(renderer, row.X, clipY, row.W, clipH, func(r *sdl.Renderer) {
		if v.Progress >= 0 {
			UIProgressBar(r, bar, v.Progress, glassHairline(), glassAccent())
			return
		}
		// No size known: a short segment sweeps along the track.
		UIProgressBar(r, bar, 0, glassHairline(), glassAccent())
		span := bar.W / 4
		phase := float64(sdl.GetTicks64()%1600) / 1600
		x := bar.X + int32(phase*float64(bar.W-span))
		fillRoundedRect(r, x, bar.Y, span, bar.H, bar.H/2, glassAccent())
	})
}

// ── Settings ─────────────────────────────────────────────────────────────────

// upchCheckNowFromSettings runs the release check from Settings; an update
// found is then downloaded ahead when the moment allows.
func upchCheckNowFromSettings(c *Config) {
	if !IsOnline() {
		ShowToast(offlinePlainNotice, ToastKindWarning)
		return
	}
	ShowToast("Checking for updates…", ToastKindInfo)
	RunPatchOp("Checking for updates", func(ctx context.Context) error {
		err := OpCheckUpdates(ctx, c)
		upchNudge()
		return err
	})
}

// upchLastCheckLine is the line under "Check for updates now".
func upchLastCheckLine() string {
	patchStateMu.Lock()
	manual, auto := patchState.LastCheckAt, patchState.AutoUpdateCheckedAt
	patchStateMu.Unlock()
	var latest time.Time
	for _, s := range []string{manual, auto} {
		if t, ok := parseReleaseTimestamp(s); ok && t.After(latest) {
			latest = t
		}
	}
	if latest.IsZero() {
		return "Asks for the newest JukaHub release"
	}
	return "Last checked " + latest.Local().Format("Jan 2, 15:04")
}

// ── Sheets ───────────────────────────────────────────────────────────────────

// upchSheetOpen reports whether a channel sheet is up over the Patch list.
func upchSheetOpen() bool { return upchUI.sheet != upchSheetNone }

// upchOpenHistory opens the version history.
func upchOpenHistory(config *Config) {
	upchUI.sheet = upchSheetHistory
	upchUI.focus, upchUI.top = 0, 0
	upchUI.headLine = "Installed now · JukaHub " + versionDisplayString(config)
	upchSetHistory()
}

// upchSetHistory refreshes the history rows from the cache.
func upchSetHistory() {
	upchUI.entries, upchUI.entriesVer = uhistEntriesSnapshot()
	n := len(upchUI.entries)
	upchUI.icons = upchUI.icons[:0]
	upchUI.titles = upchUI.titles[:0]
	upchUI.subs = upchUI.subs[:0]
	upchUI.badges = upchUI.badges[:0]
	for _, e := range upchUI.entries {
		label := uhistLabel(e.Tag, e.Date, e.Version)
		title := "JukaHub " + label
		if e.Version != "" && e.Version != label {
			title += " · " + e.Version
		}
		sub := humanBytes(e.Bytes)
		if t, ok := parseReleaseTimestamp(e.SavedAt); ok {
			sub = "Replaced " + t.Local().Format("Jan 2, 15:04") + " · " + sub
		}
		if note := upchFirstNote(e.Notes); note != "" {
			sub += " · " + note
		}
		badge := ""
		if e.Reason == uhistReasonRollback {
			badge = "Kept by a rollback"
		}
		upchUI.icons = append(upchUI.icons, "backup")
		upchUI.titles = append(upchUI.titles, title)
		upchUI.subs = append(upchUI.subs, sub)
		upchUI.badges = append(upchUI.badges, badge)
	}
	if upchUI.focus >= n {
		upchUI.focus = n - 1
	}
	if upchUI.focus < 0 {
		upchUI.focus = 0
	}
}

// upchFirstNote is the first change a release's notes list, for a row.
func upchFirstNote(notes string) string {
	for _, line := range plainReleaseNotes(notes) {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "• "))
		if line != "" && !strings.HasPrefix(line, "@") && !strings.HasPrefix(line, "http") {
			return line
		}
	}
	return ""
}

// upchOpenFiles opens Install from file and looks for update files.
func upchOpenFiles() {
	upchUI.sheet = upchSheetFiles
	upchUI.focus, upchUI.top = 0, 0
	upchUI.verdict = nil
	upchUI.checking = ""
	upchUI.headLine = ""
	upchStartScan()
}

// upchStartScan looks for update files off the render thread.
func upchStartScan() {
	upchUI.gen++
	gen := upchUI.gen
	// A check still running reports into nothing now; nothing waits on it.
	upchUI.checking = ""
	upchUI.verdict = nil
	upchUI.scanning = true
	upchSetFiles(nil)
	go func() {
		files := upchScanSideload()
		upchUIPost(func() {
			if upchUI.gen != gen || upchUI.sheet != upchSheetFiles {
				return
			}
			upchUI.scanning = false
			upchUI.focus, upchUI.top = 0, 0
			upchSetFiles(files)
		})
	}()
}

// upchSetFiles sets the found files and their rows.
func upchSetFiles(files []upchFileCandidate) {
	upchUI.files = files
	upchUI.icons = upchUI.icons[:0]
	upchUI.titles = upchUI.titles[:0]
	upchUI.subs = upchUI.subs[:0]
	upchUI.badges = upchUI.badges[:0]
	for _, f := range files {
		icon, badge := "download", "Full release"
		if f.Kind == upchKindDelta {
			icon, badge = "update", "Delta"
		}
		sub := f.Folder + " · " + formatInstallBytes(f.Size) + " · " + f.ModTime.Local().Format("Jan 2, 15:04")
		upchUI.icons = append(upchUI.icons, icon)
		upchUI.titles = append(upchUI.titles, f.Name)
		upchUI.subs = append(upchUI.subs, sub)
		upchUI.badges = append(upchUI.badges, badge)
	}
}

// upchCloseSheet closes the sheet. A scan or check still running reports into
// nothing.
func upchCloseSheet() {
	upchUI.sheet = upchSheetNone
	upchUI.gen++
	upchUI.checking = ""
	upchUI.verdict = nil
	upchUI.rows = upchUI.rows[:0]
}

// upchSheetRowCount is how many rows the open sheet lists.
func upchSheetRowCount() int {
	switch upchUI.sheet {
	case upchSheetHistory:
		return len(upchUI.entries)
	case upchSheetFiles:
		return len(upchUI.files)
	}
	return 0
}

func upchSheetMove(delta int) {
	n := upchSheetRowCount()
	if n == 0 {
		return
	}
	next := upchUI.focus + delta
	if next < 0 {
		next = 0
	}
	if next >= n {
		next = n - 1
	}
	if next != upchUI.focus {
		upchUI.focus = next
		PlayNavSound()
	}
}

// upchSheetActivate is A: roll back to the focused version, or check and
// install the focused file.
func upchSheetActivate() {
	switch upchUI.sheet {
	case upchSheetHistory:
		if upchUI.focus < 0 || upchUI.focus >= len(upchUI.entries) {
			return
		}
		e := upchUI.entries[upchUI.focus]
		PlayActivateSound()
		label := uhistLabel(e.Tag, e.Date, e.Version)
		patchModal = &patchModalState{
			ID:    "uhist-rollback:" + e.ID,
			Title: "Roll back to JukaHub " + label + "?",
			Body: "JukaHub restarts on the version from " + label + ". No internet is needed, and the version " +
				"you are on now is kept in this list, so you can come back to it.\n\n" +
				"Your settings and files are not touched; files the newer version added stay in place.",
			Confirm:  "Roll back & Restart",
			HighRisk: true,
		}
	case upchSheetFiles:
		if upchUI.checking != "" || upchUI.focus < 0 || upchUI.focus >= len(upchUI.files) {
			return
		}
		PlayActivateSound()
		upchCheckFile(upchUI.files[upchUI.focus])
	}
}

// upchSheetSecondary is X: delete the focused saved version.
func upchSheetSecondary() {
	if upchUI.sheet != upchSheetHistory || upchUI.focus < 0 || upchUI.focus >= len(upchUI.entries) {
		return
	}
	e := upchUI.entries[upchUI.focus]
	PlayActivateSound()
	patchModal = &patchModalState{
		ID:      "uhist-delete:" + e.ID,
		Title:   "Delete the saved JukaHub " + uhistLabel(e.Tag, e.Date, e.Version) + "?",
		Body:    "Frees " + humanBytes(e.Bytes) + " on the card. You cannot roll back to this version afterwards.",
		Confirm: "Delete",
	}
}

// upchSheetTertiary is Y: the focused version's release notes, or look for
// update files again.
func upchSheetTertiary() {
	switch upchUI.sheet {
	case upchSheetHistory:
		if upchUI.focus < 0 || upchUI.focus >= len(upchUI.entries) {
			return
		}
		e := upchUI.entries[upchUI.focus]
		PlayActivateSound()
		body := strings.Join(plainReleaseNotes(e.Notes), "\n")
		if strings.TrimSpace(body) == "" {
			body = "No release notes were saved with this version."
		}
		patchModal = &patchModalState{
			ID:      "uhist-notes",
			Title:   "What's in JukaHub " + uhistLabel(e.Tag, e.Date, e.Version),
			Body:    body,
			Confirm: "Close",
		}
	case upchSheetFiles:
		PlayActivateSound()
		upchStartScan()
	}
}

// upchCheckFile verifies a picked file off the render thread, then asks.
func upchCheckFile(c upchFileCandidate) {
	upchUI.checking = c.Name
	upchUI.verdict = nil
	gen := upchUI.gen
	go func() {
		v := upchVerifyFile(c.Path)
		upchUIPost(func() {
			if upchUI.gen != gen || upchUI.sheet != upchSheetFiles {
				return
			}
			upchUI.checking = ""
			if !upchOnPatchScene() {
				return
			}
			upchUI.verdict = &v
			upchConfirmFile(v)
		})
	}()
}

// upchConfirmFile asks before a checked file replaces JukaHub, saying how far
// it could be verified; a file that failed its check only gets the reason.
func upchConfirmFile(v upchFileVerdict) {
	if v.Err != "" {
		patchModal = &patchModalState{
			ID:      "upch-file-error",
			Title:   "This file cannot be installed",
			Body:    v.Err,
			Confirm: "OK",
		}
		return
	}
	var b strings.Builder
	b.WriteString(v.Detail)
	b.WriteString(" · ")
	b.WriteString(formatInstallBytes(v.Size))
	if v.Kind == upchKindDelta {
		b.WriteString(" · a delta for the version you have")
	}
	b.WriteString(".")
	if v.Expired {
		b.WriteString(" The signature is valid; its time window has passed, which only matters for downloads.")
	}
	if v.Verified == upchVerifiedNone {
		b.WriteString("\n\nAnyone could have made this file. Install it only if you know where it came from.")
	}
	if v.Older {
		b.WriteString("\n\nIt is not newer than the version you have: installing it goes back.")
	}
	b.WriteString("\n\nNo internet is needed. JukaHub restarts; the version you are on now is kept in Version history.")
	patchModal = &patchModalState{
		ID:       "upch-file-install",
		Title:    "Install JukaHub " + upchTagLabel(v.Tag, v.Date) + " from this file?",
		Body:     b.String(),
		Confirm:  "Install & Restart",
		HighRisk: true,
	}
}

// upchModalConfirmed runs a confirmation the channel raised through the Patch
// modal. It reports whether id was one of the channel's.
func upchModalConfirmed(id string) bool {
	switch {
	case strings.HasPrefix(id, "uhist-rollback:"):
		entryID := strings.TrimPrefix(id, "uhist-rollback:")
		upchCloseSheet()
		RunPatchOp("Rolling back JukaHub", func(ctx context.Context) error {
			return uhistRollbackOp(ctx, appConfig, entryID)
		})
	case strings.HasPrefix(id, "uhist-delete:"):
		entryID := strings.TrimPrefix(id, "uhist-delete:")
		RunPatchOp("Deleting the saved version", func(ctx context.Context) error {
			return uhistDelete(entryID)
		})
	case id == "upch-file-install":
		if upchUI.verdict == nil {
			return true
		}
		v := *upchUI.verdict
		upchCloseSheet()
		RunPatchOp("Installing JukaHub from a file", func(ctx context.Context) error {
			return upchInstallFileOp(ctx, appConfig, v)
		})
	case id == "upch-install-cached":
		RunPatchOp("Installing JukaHub from the update cache", func(ctx context.Context) error {
			return upchInstallCachedOp(ctx, appConfig)
		})
	case id == "uhist-notes", id == "upch-file-error":
		// Read and closed; nothing to run.
	default:
		return false
	}
	return true
}

// upchSheetFooterHints are the footer's hints while a sheet is up.
func upchSheetFooterHints() ([]FooterHint, bool) {
	switch upchUI.sheet {
	case upchSheetHistory:
		if len(upchUI.entries) == 0 {
			return upchHintsHistoryEmpty, true
		}
		return upchHintsHistory, true
	case upchSheetFiles:
		if len(upchUI.files) == 0 {
			return upchHintsFilesEmpty, true
		}
		return upchHintsFiles, true
	}
	return nil, false
}

// ── Sheet input ──────────────────────────────────────────────────────────────

// upchSheetKey handles a key while a sheet is up; the sheet takes every key.
func upchSheetKey(e *sdl.KeyboardEvent) bool {
	switch e.Keysym.Sym {
	case sdl.K_UP:
		upchSheetMove(-1)
	case sdl.K_DOWN:
		upchSheetMove(1)
	case sdl.K_PAGEUP:
		upchSheetMove(-5)
	case sdl.K_PAGEDOWN:
		upchSheetMove(5)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		upchSheetActivate()
	case sdl.K_DELETE, sdl.K_x:
		upchSheetSecondary()
	case sdl.K_y, sdl.K_n, sdl.K_F5:
		upchSheetTertiary()
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		upchCloseSheet()
		PlayBackSound()
	}
	return true
}

// upchSheetController handles a pad button while a sheet is up.
func upchSheetController(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		upchSheetMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		upchSheetMove(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		upchSheetMove(-5)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		upchSheetMove(5)
	case sdl.CONTROLLER_BUTTON_A:
		upchSheetActivate()
	case sdl.CONTROLLER_BUTTON_X:
		upchSheetSecondary()
	case sdl.CONTROLLER_BUTTON_Y:
		upchSheetTertiary()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		upchCloseSheet()
		PlayBackSound()
	}
	return true
}

// upchSheetMouse handles a click while a sheet is up. A click outside the card
// closes it; the card swallows every click that misses its rows and buttons.
func upchSheetMouse(mx, my int32) bool {
	if upchUI.card.W > 0 && !pointInRect(mx, my, upchUI.card) {
		upchCloseSheet()
		PlayBackSound()
		return true
	}
	switch {
	case upchUI.notesBtn.W > 0 && pointInRect(mx, my, upchUI.notesBtn):
		upchSheetTertiary()
		return true
	case upchUI.deleteBtn.W > 0 && pointInRect(mx, my, upchUI.deleteBtn):
		upchSheetSecondary()
		return true
	case upchUI.rescanBtn.W > 0 && pointInRect(mx, my, upchUI.rescanBtn):
		// Drawn disabled while a scan runs, so a click then does nothing.
		if !upchUI.scanning {
			upchSheetTertiary()
		}
		return true
	}
	for k, r := range upchUI.rows {
		if r.W > 0 && pointInRect(mx, my, r) {
			upchUI.focus = upchUI.rowBase + k
			upchSheetActivate()
			return true
		}
	}
	return true
}

// ── Sheet drawing ────────────────────────────────────────────────────────────

const (
	upchRowH   = int32(64)
	upchRowGap = int32(8)
)

// upchSheetRect is the sheet's card: below the header, above the footer, at
// most 900px wide.
func upchSheetRect() sdl.Rect {
	margin := scaleX(homeSideMargin, screenWidth)
	w := screenWidth - 2*margin
	if w > 900 {
		w = 900
	}
	top := headerBarHeight() + 6
	bottom := screenHeight - HomeFooterH - 16
	return sdl.Rect{X: (screenWidth - w) / 2, Y: top, W: w, H: bottom - top}
}

// upchRenderSheet draws the open sheet over the Patch list and records its
// click targets. Patch calls it every frame, before its own modal.
func upchRenderSheet(renderer *sdl.Renderer, config *Config) {
	upchUIDrain()
	upchUI.rows = upchUI.rows[:0]
	upchUI.notesBtn, upchUI.deleteBtn, upchUI.rescanBtn = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	upchUI.card = sdl.Rect{}
	if upchUI.sheet == upchSheetNone || renderer == nil {
		return
	}
	if upchUI.sheet == upchSheetHistory && uhistVersion() != upchUI.entriesVer {
		upchSetHistory()
	}
	titleFont := getFontSized(config, "small", 18)
	subFont := getFontSized(config, "small", 14)
	btnFont := getFontSized(config, "small", 15)
	if titleFont == nil || subFont == nil || btnFont == nil {
		return
	}

	drawOverlayScrim(renderer)
	card := upchSheetRect()
	upchUI.card = card
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	title, subtitle, glyph := "Version history", "JukaHub builds kept on this device · roll back in one press", "backup"
	if upchUI.sheet == upchSheetFiles {
		title, subtitle, glyph = "Install from file", "Update from the SD card, a USB stick or Downloads — no internet needed", "folder"
	}
	y := UIPageHeaderDraw(renderer, config, overlayPageHeader(card, title, subtitle, glyph)) + 10
	x := card.X + OverlayPadX
	w := card.W - 2*OverlayPadX
	bottom := card.Y + card.H - OverlayPadX

	if upchUI.headLine != "" {
		drawText(renderer, subFont, ellipsize(subFont, upchUI.headLine, w), x, y, ColorTextTertiary(), textAlignLeft)
		y += int32(subFont.Height()) + 10
	}
	if upchUI.sheet == upchSheetFiles {
		// "Look again" sits under the list, for a mouse or a finger.
		btn := sdl.Rect{X: x + w - 160, Y: bottom - 40, W: 160, H: 40}
		upchUI.rescanBtn = UIButton(renderer, nil, btn, "Look again", UIButtonStyle{Variant: UIButtonSecondary, Font: btnFont, Pill: true},
			UIButtonState{Hovered: pointInRect(mouseX, mouseY, btn), Disabled: upchUI.scanning})
		status := ""
		switch {
		case upchUI.checking != "":
			status = "Checking " + upchUI.checking + " — signature, checksum and contents…"
		case upchUI.scanning:
			status = "Looking for update files…"
		case len(upchUI.files) > 0:
			status = "A release.json or SHA256SUMS next to a file lets JukaHub verify it."
		}
		if status != "" && btn.X-12 > x {
			_, th := measureText(subFont, status)
			drawText(renderer, subFont, ellipsize(subFont, status, btn.X-12-x), x, btn.Y+(btn.H-th)/2, ColorTextTertiary(), textAlignLeft)
		}
		bottom = btn.Y - 12
	}
	area := sdl.Rect{X: x, Y: y, W: w, H: bottom - y}
	if area.H <= 0 {
		return
	}
	if upchSheetRowCount() == 0 {
		upchDrawSheetEmpty(renderer, config, subFont, area)
		return
	}
	upchDrawSheetRows(renderer, titleFont, subFont, btnFont, area)
}

// upchDrawSheetEmpty draws what an empty sheet says instead of rows.
func upchDrawSheetEmpty(renderer *sdl.Renderer, config *Config, font *ttf.Font, area sdl.Rect) {
	switch {
	case upchUI.sheet == upchSheetHistory && uhistKeep.Load() == 0:
		UIEmptyState(renderer, config, area, "@backup", "Keeping previous versions is off",
			"Turn it on in Settings → About → Keep previous versions")
	case upchUI.sheet == upchSheetHistory:
		UIEmptyState(renderer, config, area, "@backup", "No earlier versions yet",
			"The next update keeps the version it replaces")
	case upchUI.scanning:
		UIEmptyState(renderer, config, area, "@search", "Looking for update files…",
			"SD card, USB sticks, Downloads and JukaHub's updates folder")
	default:
		upchDrawSideloadHelp(renderer, font, area)
	}
}

// upchSideloadHelp is how to update a device that never goes online.
func upchSideloadHelp() string {
	archive := "JukaHub-Trimui.zip"
	if runtime.GOOS == "windows" {
		archive = "JukaHub-Windows.zip"
	}
	return "No update files found. On any computer, download " + archive + " from the JukaHub releases page — " +
		"with its release.json when the release has one, so the signature can be checked here. Copy them to the " +
		"SD card (or an updates folder on it), a USB stick or Downloads, then press Y to look again. A .jkdelta " +
		"made for the version you have works too, and is much smaller."
}

// upchDrawSideloadHelp draws the instructions, wrapped once per width.
func upchDrawSideloadHelp(renderer *sdl.Renderer, font *ttf.Font, area sdl.Rect) {
	maxW := area.W - 2*OverlayPadX
	if maxW <= 40 {
		return
	}
	lineH := int32(font.Height()) + 4
	maxLines := int(area.H / lineH)
	if maxLines < 1 {
		return
	}
	if upchUI.helpLines == nil || upchUI.helpWidth != maxW {
		upchUI.helpLines = wrapTextLines(font, upchSideloadHelp(), maxW, 12)
		upchUI.helpWidth = maxW
	}
	lines := upchUI.helpLines
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	y := area.Y + (area.H-int32(len(lines))*lineH)/2
	DrawHomeIcon(renderer, "folder", sdl.Rect{X: area.X + (area.W-36)/2, Y: y - 52, W: 36, H: 36}, false)
	for _, line := range lines {
		lw, _ := measureText(font, line)
		drawText(renderer, font, line, area.X+(area.W-lw)/2, y, ColorTextSecondary(), textAlignLeft)
		y += lineH
	}
}

// upchDrawSheetRows draws the visible rows, keeping the focused one in view.
func upchDrawSheetRows(renderer *sdl.Renderer, titleFont, subFont, btnFont *ttf.Font, area sdl.Rect) {
	n := upchSheetRowCount()
	visible := int((area.H + upchRowGap) / (upchRowH + upchRowGap))
	if visible < 1 {
		visible = 1
	}
	if upchUI.focus < upchUI.top {
		upchUI.top = upchUI.focus
	}
	if upchUI.focus >= upchUI.top+visible {
		upchUI.top = upchUI.focus - visible + 1
	}
	if upchUI.top > n-visible {
		upchUI.top = n - visible
	}
	if upchUI.top < 0 {
		upchUI.top = 0
	}
	upchUI.rowBase = upchUI.top
	y := area.Y
	for i := upchUI.top; i < n && i < upchUI.top+visible && i < len(upchUI.titles); i++ {
		r := sdl.Rect{X: area.X, Y: y, W: area.W, H: upchRowH}
		focused := i == upchUI.focus
		UIListRow(renderer, r, glassRowRadius, focused, !focused && pointInRect(mouseX, mouseY, r))
		icon := sdl.Rect{X: r.X + 16, Y: r.Y + (r.H-28)/2, W: 28, H: 28}
		DrawHomeIcon(renderer, upchUI.icons[i], icon, focused)
		textX := icon.X + icon.W + 14
		right := r.X + r.W - 16
		if focused && upchUI.sheet == upchSheetHistory {
			bw, bh := int32(96), int32(34)
			del := sdl.Rect{X: right - bw, Y: r.Y + (r.H-bh)/2, W: bw, H: bh}
			notes := sdl.Rect{X: del.X - 8 - bw, Y: del.Y, W: bw, H: bh}
			upchUI.deleteBtn = UIButton(renderer, nil, del, "Delete", UIButtonStyle{Variant: UIButtonSecondary, Font: btnFont, Pill: true},
				UIButtonState{Hovered: pointInRect(mouseX, mouseY, del)})
			upchUI.notesBtn = UIButton(renderer, nil, notes, "Notes", UIButtonStyle{Variant: UIButtonSecondary, Font: btnFont, Pill: true},
				UIButtonState{Hovered: pointInRect(mouseX, mouseY, notes)})
			right = notes.X - 12
		} else if badge := upchUI.badges[i]; badge != "" {
			bw, bh := measureText(subFont, badge)
			drawText(renderer, subFont, badge, right-bw, r.Y+(r.H-bh)/2, ColorTextTertiary(), textAlignLeft)
			right -= bw + 14
		}
		if textW := right - textX; textW > 40 {
			drawText(renderer, titleFont, ellipsize(titleFont, upchUI.titles[i], textW), textX, r.Y+10, ColorTextPrimary(), textAlignLeft)
			drawText(renderer, subFont, ellipsize(subFont, upchUI.subs[i], textW), textX, r.Y+37, ColorTextSecondary(), textAlignLeft)
		}
		upchUI.rows = append(upchUI.rows, r)
		y += upchRowH + upchRowGap
	}
}
