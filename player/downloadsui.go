package main

// downloadsui.go — the Downloads panel.
//
// One screen for everything JukaHub keeps on the card: the card's own storage
// bar at the top, then the queue grouped into downloading / queued / failed /
// saved rows. Rows carry the media they save — an m4a track or an mp4 video —
// and a row is actionable: A plays a saved track or video, or retries a failed
// one, X forgets it, Y pauses the whole queue. The screen is a transfer manager
// rather than a log.
//
// The panel is a pure view over downloads.go: it never starts a transfer
// itself, only queues links and reads snapshots, which keeps the queue legal
// while the panel is closed.

import (
	"fmt"
	"os"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// downloadTab filters the list. Saved lives on its own tab because it grows
// without bound while the queue itself stays short.
type downloadTab int

const (
	downloadTabAll downloadTab = iota
	downloadTabActive
	downloadTabSaved
)

func downloadTabLabel(t downloadTab) string {
	switch t {
	case downloadTabActive:
		return "Active"
	case downloadTabSaved:
		return "Saved"
	}
	return "All"
}

// downloadRow is one line of the panel: either the "Add a link" affordance or
// one queued/saved track.
type downloadRow struct {
	add     bool
	item    downloadItem
	section string // section heading drawn above this row, when non-empty
}

// downloadLinkInput is the field the on-screen keyboard edits for this panel.
// It is only read on submit, so a half-typed link never queues anything.
var downloadLinkInput string

// downloadLinkSubmit validates and queues a pasted link. Every way of getting a
// link into the queue (keyboard, next turn's share sheet) ends up here.
func downloadLinkSubmit(raw string) {
	link := strings.TrimSpace(raw)
	downloadLinkInput = ""
	if link == "" {
		return
	}
	if _, err := downloadEnqueue(link); err != nil {
		ShowToast(err.Error(), ToastKindWarning)
		return
	}
	// The row is already on screen and names itself once yt-dlp resolves the
	// link, so the toast only has to confirm the queue took it.
	ShowToast("Download queued", ToastKindInfo)
}

// downloadHit is one clickable row. The row index travels with the rect: the
// list scrolls, so a hit rect's position in the slice is not its row.
type downloadHit struct {
	index int
	rect  sdl.Rect
}

var dlPanel = struct {
	open   bool
	tab    downloadTab
	cursor int
	scroll int32
	rows   []downloadRow
	hits   []downloadHit
	tabs   []sdl.Rect
	panel  sdl.Rect
}{}

// openDownloadsPanel shows the panel. It is a modal like the rest: whatever
// panel it was opened from closes rather than stacking underneath.
func openDownloadsPanel() {
	qs.open = false
	toolsPanel.open = false
	gameLib.open = false
	dlPanel.open = true
	dlPanel.tab = downloadTabAll
	dlPanel.cursor = 0
	dlPanel.scroll = 0
	dlPanel.rows = nil
	dlPanel.hits = nil
	dlPanel.tabs = nil
	PlayActivateSound()
}

func closeDownloadsPanel() {
	if !dlPanel.open {
		return
	}
	dlPanel.open = false
	dlPanel.rows = nil
	dlPanel.hits = nil
	dlPanel.tabs = nil
	PlayBackSound()
}

// downloadRowsFor builds the visible rows for a tab, with one heading per
// non-empty section. The "Add a link" row always comes first so a fresh install
// has an obvious next step.
func downloadRowsFor(tab downloadTab) []downloadRow {
	items := downloadsSnapshot()
	rows := []downloadRow{{
		add:     true,
		section: "",
	}}
	add := func(section string, it downloadItem, first bool) {
		row := downloadRow{item: it, section: ""}
		if first {
			row.section = section
		}
		rows = append(rows, row)
	}
	groups := []struct {
		name   string
		status downloadStatus
	}{
		{"Downloading", downloadActive},
		{"Queued", downloadQueued},
		{"Failed", downloadFailed},
		{"Saved", downloadDone},
	}
	if tab == downloadTabActive {
		groups = groups[:2]
	}
	if tab == downloadTabSaved {
		groups = groups[3:]
	}
	for _, g := range groups {
		// The heading carries its count ("FAILED · 2"), so scrolling a long
		// list answers "how much is stuck" without remembering the tab label.
		n := 0
		for _, it := range items {
			if it.Status == g.status {
				n++
			}
		}
		name := g.name
		if n > 0 {
			name = fmt.Sprintf("%s · %d", g.name, n)
		}
		first := true
		for _, it := range items {
			if it.Status != g.status {
				continue
			}
			add(name, it, first)
			first = false
		}
	}
	return rows
}

// downloadRowCountFor counts only real tracks, for the empty-state checks.
func downloadRowCount(rows []downloadRow) int {
	n := 0
	for _, r := range rows {
		if !r.add {
			n++
		}
	}
	return n
}

// downloadCursorRows refreshes the rows and keeps the highlight on a real row.
func downloadCursorRows() []downloadRow {
	rows := downloadRowsFor(dlPanel.tab)
	dlPanel.rows = rows
	if dlPanel.cursor >= len(rows) {
		dlPanel.cursor = len(rows) - 1
	}
	if dlPanel.cursor < 0 {
		dlPanel.cursor = 0
	}
	return rows
}

func downloadPanelMove(delta int) {
	rows := downloadCursorRows()
	if len(rows) == 0 {
		return
	}
	next := dlPanel.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= len(rows) {
		next = len(rows) - 1
	}
	if next == dlPanel.cursor {
		return
	}
	dlPanel.cursor = next
	PlayNavSound()
}

// downloadPanelTab switches tabs and starts the list from the top; a cursor
// carried across tabs would point at a different track than the one under the
// user's thumb.
func downloadPanelTab(t downloadTab) {
	if dlPanel.tab == t {
		return
	}
	dlPanel.tab = t
	dlPanel.cursor = 0
	dlPanel.scroll = 0
	dlPanel.rows = downloadCursorRows()
	PlayNavSound()
}

func downloadPanelCycleTab(delta int) {
	tabs := []downloadTab{downloadTabAll, downloadTabActive, downloadTabSaved}
	idx := 0
	for i, t := range tabs {
		if t == dlPanel.tab {
			idx = i
		}
	}
	idx = (idx + delta + len(tabs)) % len(tabs)
	downloadPanelTab(tabs[idx])
}

// downloadPanelActivate runs the focused row.
func downloadPanelActivate(config *Config) {
	rows := downloadCursorRows()
	if dlPanel.cursor < 0 || dlPanel.cursor >= len(rows) {
		return
	}
	row := rows[dlPanel.cursor]
	if row.add {
		vkOpenDownloadLink()
		return
	}
	it, ok := downloadFocused(row.item.ID)
	if !ok {
		return
	}
	switch it.Status {
	case downloadFailed:
		if downloadRetry(it.ID) {
			ShowToast("Retrying "+it.displayTitle(), ToastKindInfo)
		}
	case downloadDone:
		if info, err := os.Stat(it.Path); err != nil || info.Size() == 0 {
			ShowToast("That file is no longer on the card", ToastKindWarning)
			return
		}
		closeDownloadsPanel()
		if it.Kind == downloadKindVideo {
			// Saved videos play in the embedded player, on the scene that owns
			// it, exactly like a stream.
			playStream(config, it.Path)
			return
		}
		openAudioPlayer(config, it.Path)
	case downloadQueued:
		if downloadsPaused() {
			downloadSetPaused(false)
			ShowToast("Resumed the queue", ToastKindInfo)
			return
		}
		ShowToast("Waiting for the current download", ToastKindInfo)
	default:
		ShowToast("Still downloading", ToastKindInfo)
	}
}

// downloadPanelRemove forgets the focused row (and deletes its file).
func downloadPanelRemove() {
	rows := downloadCursorRows()
	if dlPanel.cursor < 0 || dlPanel.cursor >= len(rows) {
		return
	}
	row := rows[dlPanel.cursor]
	if row.add {
		return
	}
	if downloadRemove(row.item.ID) {
		PlayBackSound()
		ShowToast("Removed from downloads", ToastKindInfo)
		downloadCursorRows()
	}
}

// downloadPanelTogglePause is Y: one press stops the transfer and one resumes it.
func downloadPanelTogglePause() {
	paused := downloadsPaused()
	downloadSetPaused(!paused)
	if paused {
		ShowToast("Resuming downloads", ToastKindInfo)
	} else {
		ShowToast("Queue paused", ToastKindInfo)
	}
	PlayNavSound()
}

// downloadPanelStatus summarises the queue for the header.
func downloadPanelStatus() string {
	items := downloadsSnapshot()
	active, queued, failed, done := downloadCounts(items)
	var parts []string
	if active > 0 {
		parts = append(parts, fmt.Sprintf("%d downloading", active))
	}
	if queued > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", queued))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if done > 0 {
		parts = append(parts, fmt.Sprintf("%d saved", done))
	}
	if len(parts) == 0 {
		return "Nothing queued — press A on “Add a link” to save a track"
	}
	status := strings.Join(parts, " · ")
	if downloadsPaused() {
		status = "Paused · " + status
	}
	return status
}

// ── Input ────────────────────────────────────────────────────────────────────

// handleDownloadsKey consumes every key while the panel is open, so nothing
// underneath reacts behind the modal.
func handleDownloadsKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !dlPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		downloadPanelMove(-1)
	case sdl.K_DOWN:
		downloadPanelMove(1)
	case sdl.K_LEFT:
		downloadPanelCycleTab(-1)
	case sdl.K_RIGHT:
		downloadPanelCycleTab(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		downloadPanelActivate(config)
	case sdl.K_x:
		downloadPanelRemove()
	case sdl.K_y:
		downloadPanelTogglePause()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeDownloadsPanel()
	}
	return true
}

func ctrlDownloadsPanel(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || !dlPanel.open {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		downloadPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		downloadPanelMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		downloadPanelCycleTab(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		downloadPanelCycleTab(1)
	case sdl.CONTROLLER_BUTTON_A:
		downloadPanelActivate(config)
	case sdl.CONTROLLER_BUTTON_X:
		downloadPanelRemove()
	case sdl.CONTROLLER_BUTTON_Y:
		downloadPanelTogglePause()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		closeDownloadsPanel()
	default:
		return false
	}
	return true
}

// handleDownloadsMouse selects a row or tab, and closes the panel on a click
// outside it.
func handleDownloadsMouse(mx, my int32, config *Config) bool {
	if !dlPanel.open {
		return false
	}
	for i, rect := range dlPanel.tabs {
		if pointInRect(mx, my, rect) {
			downloadPanelTab(downloadTab(i))
			return true
		}
	}
	for _, hit := range dlPanel.hits {
		if pointInRect(mx, my, hit.rect) {
			dlPanel.cursor = hit.index
			downloadPanelActivate(config)
			return true
		}
	}
	if dlPanel.panel.W > 0 && pointInRect(mx, my, dlPanel.panel) {
		return true
	}
	closeDownloadsPanel()
	return true
}

// ── Rendering ────────────────────────────────────────────────────────────────

func renderDownloadsPanel(renderer *sdl.Renderer, config *Config) {
	if !dlPanel.open || renderer == nil {
		return
	}
	small, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if small == nil {
		return
	}
	if medium == nil {
		medium = small
	}

	panelW := int32(940)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := int32(620)
	if panelH > screenHeight-40 {
		panelH = screenHeight - 40
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	dlPanel.panel = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 185)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	// Rows are drawn with ColorSurfaceRow (one step lighter), so the panel
	// itself uses the darker surface for the list to read as separate rows.
	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorSurface)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	const padX = int32(28)
	textW := panelW - 2*padX
	// A short window keeps the list usable by dropping the header's aside and
	// the card's legend before it lets the rows shrink.
	compact := panelH < 520

	// The title band is the shared page header: the aside that only fits a full
	// window becomes the status read-out opposite the title, so it moves up and
	// down with the same geometry every other page uses.
	header := UIPanelHeader("Downloads", downloadPanelStatus(), "media")
	header.SubtitleColor = ColorTextSecondary()
	if !compact {
		header.Status = "Saved audio and video play offline"
		header.StatusColor = ColorTextTertiary()
	}
	header.X = panelX + padX
	header.Y = panelY + UIPanelHeaderInset
	header.W = textW
	UIPageHeaderDraw(renderer, config, header)

	cardH := downloadStorageCardHeight(small, !compact)
	cardY := panelY + UIPanelHeaderBand(config, header, 14)
	drawDownloadStorageCard(renderer, config, small, sdl.Rect{X: panelX + padX, Y: cardY, W: textW, H: cardH}, !compact)

	tabY := cardY + cardH + 12
	drawDownloadTabs(renderer, config, small, panelX+padX, tabY, textW)

	rows := downloadCursorRows()
	dlPanel.hits = dlPanel.hits[:0]

	listTop := tabY + int32(small.Height()) + 16
	footerH := int32(30)
	listBottom := panelY + panelH - footerH - 8
	// Rows keep a readable height and the list scrolls instead of shrinking:
	// squeezing six rows into the panel used to push the detail line out of its
	// row and under the next section heading.
	rowH := int32(68)
	gap := int32(8)
	sectionH := int32(24)
	if listBottom <= listTop {
		listBottom = listTop + rowH
	}
	if avail := listBottom - listTop; avail < 2*(rowH+gap)+sectionH {
		rowH = 50
		if avail < 2*(rowH+gap)+sectionH {
			rowH = 44
		}
	}

	// The cursor is always visible: the list scrolls by whole rows.
	visibleH := listBottom - listTop
	offsets := make([]int32, len(rows))
	y := int32(0)
	for i, row := range rows {
		if row.section != "" {
			y += sectionH
		}
		offsets[i] = y
		y += rowH + gap
	}
	totalH := y
	maxScroll := totalH - visibleH
	if maxScroll < 0 {
		maxScroll = 0
	}
	want := offsets[dlPanel.cursor] + rowH + gap - visibleH
	if want > dlPanel.scroll {
		dlPanel.scroll = want
	}
	if below := offsets[dlPanel.cursor]; below < dlPanel.scroll {
		dlPanel.scroll = below
	}
	if dlPanel.scroll > maxScroll {
		dlPanel.scroll = maxScroll
	}
	if dlPanel.scroll < 0 {
		dlPanel.scroll = 0
	}

	// Draw the list inside its own clip rectangle: a row that only half fits at
	// the bottom is cut off cleanly instead of painting over the footer, and a
	// scrolled heading cannot leak into the tabs above.
	listRect := sdl.Rect{X: panelX + padX - 14, Y: listTop, W: textW + 28, H: listBottom - listTop}
	renderer.SetClipRect(&listRect)
	for i, row := range rows {
		ry := listTop + offsets[i] - dlPanel.scroll
		if row.section != "" {
			if sy := ry - sectionH; sy >= listTop && sy+sectionH <= listBottom {
				drawDownloadSectionLabel(renderer, config, row.section, panelX+padX, sy, textW)
			}
		}
		rect := sdl.Rect{X: panelX + padX, Y: ry, W: textW, H: rowH}
		if ry+rowH > listTop && ry < listBottom {
			dlPanel.hits = append(dlPanel.hits, downloadHit{index: i, rect: rect})
		}
		if row.add {
			drawDownloadAddRow(renderer, config, small, medium, rect, i == dlPanel.cursor)
			continue
		}
		drawDownloadItemRow(renderer, config, small, medium, rect, row.item, i == dlPanel.cursor)
	}
	renderer.SetClipRect(nil)

	if downloadRowCount(rows) == 0 && dlPanel.tab != downloadTabAll {
		msg := "Nothing saved yet"
		if dlPanel.tab == downloadTabActive {
			msg = "Nothing downloading"
		}
		mw, _, _ := small.SizeUTF8(msg)
		renderText(renderer, config, small, msg, ColorTextTertiary(),
			panelX+(panelW-int32(mw))/2, listTop+rowH+gap+12)
	}

	pauseLabel := "Pause all"
	if downloadsPaused() {
		pauseLabel = "Resume"
	}
	footer := hintLine(FooterHint{"A", "Open"}, FooterHint{"X", "Remove"},
		FooterHint{"Y", pauseLabel}, FooterHint{"L1/R1", "Tabs"}, FooterHint{"B", "Close"})
	fw, _, _ := small.SizeUTF8(footer)
	renderText(renderer, config, small, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-footerH+2)
}

// drawDownloadTabs paints the tab strip and records each tab's hit rect. The
// active tab is named in the accent colour with a short underline, matching the
// top bar's navigation, and carries its count so an empty tab is obvious.
func drawDownloadTabs(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w int32) {
	if font == nil {
		return
	}
	items := downloadsSnapshot()
	active, queued, failed, done := downloadCounts(items)
	tabs := []struct {
		tab   downloadTab
		count int
	}{
		{downloadTabAll, active + queued + failed + done},
		{downloadTabActive, active + queued},
		{downloadTabSaved, done},
	}
	dlPanel.tabs = dlPanel.tabs[:0]
	cx := x
	for _, entry := range tabs {
		label := downloadTabLabel(entry.tab)
		if entry.count > 0 {
			label = fmt.Sprintf("%s %d", label, entry.count)
		}
		tw, th, err := font.SizeUTF8(label)
		if err != nil {
			continue
		}
		dlPanel.tabs = append(dlPanel.tabs, sdl.Rect{
			X: cx - 8, Y: y - 6, W: int32(tw) + 16, H: int32(th) + 14,
		})
		color := ColorTextTertiary()
		if entry.tab == dlPanel.tab {
			color = ColorTextPrimary()
		}
		renderText(renderer, config, font, label, color, cx, y)
		if entry.tab == dlPanel.tab {
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 235)
			renderer.FillRect(&sdl.Rect{X: cx, Y: y + int32(th) + 5, W: int32(tw), H: 3})
		}
		cx += int32(tw) + 30
		if cx > x+w {
			break
		}
	}
}

// downloadStorageCardHeight is the height the SD card needs at this font: the
// title row, the bar with padding, and (when shown) the legend row. Fixed
// offsets used to put the bar through the text and clip the legend at higher
// accessibility scales.
func downloadStorageCardHeight(font *ttf.Font, showLegend bool) int32 {
	if font == nil {
		return 92
	}
	h := 24 + int32(font.Height()) + 10 + 8 + 10
	if showLegend {
		h += int32(font.Height())
	}
	return h
}

// drawDownloadStorageCard paints the SD card summary: capacity on the right,
// a segmented usage bar, and a legend under it. The internal layout is driven
// by the font's own height, so every row keeps its breathing room at any
// text scale instead of a taller font sliding into the next row.
func drawDownloadStorageCard(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, showLegend bool) {
	if font == nil || rect.W <= 0 {
		return
	}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, RadiusMD, ColorPanelRaised)
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, RadiusMD, 1, ColorBorderDefault)

	label := "SD card"
	rowH := int32(font.Height())
	titleY := rect.Y + 12
	// A tiny card glyph: rounded body with a notch, drawn rather than fonted so
	// it renders on every theme.
	iconX := rect.X + 20
	iconY := titleY + rowH/2 - 8
	fillRoundedRect(renderer, iconX, iconY, 22, 16, 3, WithAlpha(ColorTextTertiary(), 160))
	fillRoundedRect(renderer, iconX+14, iconY-3, 6, 4, 1, WithAlpha(ColorTextTertiary(), 160))
	renderText(renderer, config, font, label, ColorTextPrimary(), iconX+32, titleY)

	stats := downloadSpace()
	if summary := downloadSpaceSummary(stats); summary != "" {
		// The title column ends after the icon and its text; past that, the
		// summary ellipsizes so a long reading never slides under the title on
		// a narrow window.
		if avail := rect.X + rect.W - 20 - (iconX + 32 + 24); avail > 0 {
			summary = ellipsize(font, summary, avail)
		}
		if sw, _, err := font.SizeUTF8(summary); err == nil {
			renderText(renderer, config, font, summary, ColorTextSecondary(),
				rect.X+rect.W-20-int32(sw), titleY)
		}
	}

	barX := rect.X + 20
	barY := titleY + rowH + 10
	barW := rect.W - 40
	barH := int32(8)
	fillRoundedRect(renderer, barX, barY, barW, barH, barH/2, WithAlpha(ColorTrack, 255))
	if stats.Available {
		shares := downloadSpaceShares(stats)
		music := int32(shares[0] * float64(barW))
		other := int32(shares[1] * float64(barW))
		if music > 0 {
			fillRoundedRect(renderer, barX, barY, music, barH, barH/2, accentColor)
		}
		if other > 0 {
			renderer.SetDrawColor(WithAlpha(ColorTextTertiary(), 150).R,
				WithAlpha(ColorTextTertiary(), 150).G,
				WithAlpha(ColorTextTertiary(), 150).B, 150)
			renderer.FillRect(&sdl.Rect{X: barX + music, Y: barY, W: other, H: barH})
		}
	}

	if !showLegend {
		return
	}
	legendY := barY + barH + 10
	baseY := legendY + rowH/2 - 8
	value := func(n int64) string {
		if !stats.Available {
			return "—"
		}
		return humanBytes(n)
	}
	entries := []struct {
		label string
		value string
		color sdl.Color
	}{
		// Both saved folders are counted here: "Downloads" is the app's own
		// media, audio and video together.
		{"Downloads", value(stats.Music), accentColor},
		{"Other", value(stats.Other), WithAlpha(ColorTextTertiary(), 150)},
		{"Free", value(stats.Free), WithAlpha(ColorTrack, 255)},
	}
	lx := barX
	for _, e := range entries {
		fillRoundedRect(renderer, lx, baseY, 10, 10, 3, e.color)
		renderText(renderer, config, font, e.label, ColorTextTertiary(), lx+16, legendY)
		lw, _, _ := font.SizeUTF8(e.label)
		valueX := lx + 16 + int32(lw) + 6
		renderText(renderer, config, font, e.value, ColorTextSecondary(), valueX, legendY)
		vw, _, _ := font.SizeUTF8(e.value)
		lx = valueX + int32(vw) + 20
	}
}

// drawDownloadSectionLabel paints a group heading with a hairline, like the
// Tools panel, so the list reads in sections without spending a row on a rule.
func drawDownloadSectionLabel(renderer *sdl.Renderer, config *Config, title string, x, y, w int32) {
	font := getFontSized(config, "small", 15)
	if font == nil {
		return
	}
	label := strings.ToUpper(title)
	tw, th, _ := font.SizeUTF8(label)
	renderText(renderer, config, font, label, ColorTextTertiary(), x, y+2)
	if lx := x + int32(tw) + 12; lx < x+w {
		renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 80)
		renderer.FillRect(&sdl.Rect{X: lx, Y: y + 2 + int32(th)/2, W: x + w - lx, H: 1})
	}
}

// downloadTileColors gives a row's art tile its own two-tone hue, like the
// design: a stable color comes from the title hash, so the same track always
// wears the same tile without any stored state.
func downloadTileColors(it downloadItem) (top, bottom sdl.Color) {
	var h uint32
	for _, r := range it.displayTitle() {
		h = h*31 + uint32(r)
	}
	for _, r := range it.URL {
		h = h*31 + uint32(r)*7
	}
	hue := float64(h%1000) / 1000
	r, g, b := hsvToRGB(hue, 0.55, 0.62)
	top = sdl.Color{R: r, G: g, B: b, A: 255}
	r, g, b = hsvToRGB(hue, 0.58, 0.40)
	bottom = sdl.Color{R: r, G: g, B: b, A: 255}
	return top, bottom
}

// drawDownloadFocusRing paints the outline the design uses for the focused
// row: a bright rounded ring just outside the row plus a faint halo, so the
// selection reads on a handheld screen even in direct light.
func drawDownloadFocusRing(renderer *sdl.Renderer, rect sdl.Rect) {
	if renderer == nil {
		return
	}
	strokeRoundedRect(renderer, rect.X-2, rect.Y-2, rect.W+4, rect.H+4, RadiusMD, 2, WithAlpha(accentColor, 225))
	strokeRoundedRect(renderer, rect.X-4, rect.Y-4, rect.W+8, rect.H+8, RadiusMD, 1, WithAlpha(accentColor, 70))
}

// drawDownloadAddRow is the first row: paste a link to start a download.
func drawDownloadAddRow(renderer *sdl.Renderer, config *Config, small, medium *ttf.Font, rect sdl.Rect, focused bool) {
	UIListRow(renderer, rect, RadiusMD, focused, false)
	lineH := int32(medium.Height())
	twoLines := rect.H >= lineH+int32(small.Height())+8

	glyph := int32(30)
	if rect.H < lineH+8 {
		glyph = rect.H - 12
	}
	if glyph < 16 {
		glyph = 16
	}
	gx := rect.X + 18
	gy := rect.Y + (rect.H-glyph)/2
	fillRoundedRect(renderer, gx, gy, glyph, glyph, 8, WithAlpha(accentColor, 90))
	// A drawn plus keeps the row independent of the font's glyph coverage.
	plus := int32(2)
	arm := glyph - 16
	if arm < 6 {
		arm = 6
	}
	renderer.SetDrawColor(255, 255, 255, 220)
	renderer.FillRect(&sdl.Rect{X: gx + glyph/2 - plus/2, Y: gy + (glyph-arm)/2, W: plus, H: arm})
	renderer.FillRect(&sdl.Rect{X: gx + (glyph-arm)/2, Y: gy + glyph/2 - plus/2, W: arm, H: plus})

	textX := gx + glyph + 16
	if !twoLines {
		renderText(renderer, config, medium, "Add a link", ColorTextPrimary(),
			textX, rect.Y+(rect.H-lineH)/2)
		return
	}
	renderText(renderer, config, medium, "Add a link", ColorTextPrimary(), textX, rect.Y+6)
	renderText(renderer, config, small, "Paste a link to save its audio for offline listening",
		ColorTextTertiary(), textX, rect.Y+6+lineH)
	if focused {
		drawDownloadFocusRing(renderer, rect)
	}
}

// drawDownloadItemRow paints one track: title — source, a status on the right,
// and a progress bar while it runs. A short row drops the second line instead
// of letting it spill under the next heading.
func drawDownloadItemRow(renderer *sdl.Renderer, config *Config, small, medium *ttf.Font, rect sdl.Rect, it downloadItem, focused bool) {
	UIListRow(renderer, rect, RadiusMD, focused, false)
	lineH := int32(medium.Height())
	twoLines := rect.H >= lineH+int32(small.Height())+8

	art := int32(36)
	if rect.H-12 < art {
		art = rect.H - 12
	}
	if art < 16 {
		art = 16
	}
	ax := rect.X + 16
	ay := rect.Y + (rect.H-art)/2
	// Two-tone tile in the row's own hue: the design's way of making the queue
	// scannable by color before any text is read.
	top, bottom := downloadTileColors(it)
	fillRoundedRect(renderer, ax, ay, art, art/2, 8, top)
	fillRoundedRect(renderer, ax, ay+art/2, art, art-art/2, 8, bottom)

	statusText, statusColor := downloadRowStatus(it)
	textX := ax + art + 14
	statusW := int32(0)
	if statusText != "" {
		if w, _, err := small.SizeUTF8(statusText); err == nil {
			statusW = int32(w)
		}
	}
	labelW := rect.X + rect.W - 16 - textX - statusW - 14
	if labelW < 40 {
		labelW = 40
	}

	title := it.displayTitle()
	source := strings.TrimSpace(it.Source)
	if !twoLines && it.Status == downloadFailed && it.Err != "" {
		// A short row has no second line for the reason, so it rides beside
		// the title instead of being lost.
		source = it.Err
	}
	line := title
	titleW, _, _ := medium.SizeUTF8(title)
	if source != "" && int32(titleW)+24 < labelW {
		line = title + " — " + source
	}
	titleY := rect.Y + 6
	if !twoLines {
		titleY = rect.Y + (rect.H-lineH)/2
	}
	renderText(renderer, config, medium, ellipsize(medium, line, labelW), ColorTextPrimary(), textX, titleY)

	if it.Status == downloadActive {
		// Progress under the title, matching the card's proportions: a track
		// with an unknown size still gets an empty track rather than a bar
		// that lies about being almost done.
		barY := rect.Y + 6 + lineH + 8
		barH := int32(5)
		if !twoLines {
			// One line: the bar becomes a thin strip along the row's bottom.
			barY = rect.Y + rect.H - 7
			barH = 3
		}
		if labelW > 0 {
			fillRoundedRect(renderer, textX, barY, labelW, barH, barH/2, WithAlpha(ColorTrack, 255))
			if p := it.percent(); p > 0 {
				fillRoundedRect(renderer, textX, barY, int32(float64(labelW)*p), barH, barH/2, accentColor)
			}
		}
	} else if twoLines {
		if detail := downloadRowDetail(it); detail != "" {
			renderText(renderer, config, small, ellipsize(small, detail, labelW), ColorTextTertiary(),
				textX, rect.Y+6+lineH)
		}
	}

	if statusText != "" {
		renderText(renderer, config, small, statusText, statusColor,
			rect.X+rect.W-16-int32(statusW), rect.Y+(rect.H-smallH(small))/2)
	}
	if focused {
		drawDownloadFocusRing(renderer, rect)
	}
}

// smallH is the font height as an int32 for centering.
func smallH(f *ttf.Font) int32 {
	if f == nil {
		return 0
	}
	return int32(f.Height())
}

// downloadRowStatus is the right-hand column: percent and speed while running,
// "Queued" while waiting, "Saved" once it is on the card, "Failed" otherwise.
func downloadRowStatus(it downloadItem) (string, sdl.Color) {
	switch it.Status {
	case downloadActive:
		speed := ""
		if it.Speed > 0 {
			speed = fmt.Sprintf("%.1f MiB/s", it.Speed/(1024*1024))
		}
		if it.Total > 0 {
			pct := fmt.Sprintf("%d%%", int(it.percent()*100))
			if speed != "" {
				return pct + " · " + speed, ColorTextSecondary()
			}
			return pct, ColorTextSecondary()
		}
		if speed != "" {
			return speed, ColorTextSecondary()
		}
		return "Starting…", ColorTextSecondary()
	case downloadQueued:
		if downloadsPaused() {
			return "Paused", ColorToastWarn
		}
		return "Queued", ColorTextTertiary()
	case downloadDone:
		return "Saved", ColorToastSuccess
	}
	return "Failed", ColorToastError
}

// downloadRowDetail is the second line under a title for rows without progress.
func downloadRowDetail(it downloadItem) string {
	switch it.Status {
	case downloadFailed:
		if it.Err != "" {
			return it.Err
		}
		return "Press A to retry"
	case downloadDone:
		size := humanBytes(it.Done)
		if it.Done <= 0 {
			size = ""
		}
		detail := it.Kind.label() + " · Ready offline"
		if size != "" {
			detail += " · " + size
		}
		return detail + " · A plays it"
	case downloadQueued:
		return "Waiting for its turn"
	}
	return ""
}
