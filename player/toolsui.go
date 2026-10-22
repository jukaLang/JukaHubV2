package main

// toolsui.go — the Tools panel: LAN IP address, the on-device FTP server and
// the SSH/SFTP server.
//
// It is an overlay (like Quick Settings) so it works on every config: the home
// "Tools" card opens it when the config has no "Misc" scene, and config authors
// can open it from any button with the "tools_open" trigger. The classic Misc
// scene is still reachable from the "System utilities" row when it exists.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

type toolsAction int

const (
	toolsActionFTP toolsAction = iota
	toolsActionSSH
	toolsActionCompanion
	toolsActionParty
	toolsActionPatch
	toolsActionCalculator
	toolsActionCommunity
	toolsActionPowerTools
	toolsActionCopyIP
	toolsActionUtilities
)

// toolsStatus colours the small dot on a row: a server that is running is
// green, something the user has to fix (offline, no address) is amber.
type toolsStatus int

const (
	toolsStatusNeutral toolsStatus = iota
	toolsStatusGood
	toolsStatusWarn
)

type toolsRow struct {
	label   string
	value   string
	detail  string
	hint    string
	action  toolsAction
	enabled bool
	// section is drawn as a small heading above this row; only the first row
	// of a group carries one.
	section string
	status  toolsStatus
}

type toolsPanelState struct {
	open   bool
	cursor int
}

var toolsPanel toolsPanelState

// toolsRowRects is filled by renderToolsPanel so a click can be mapped back to
// a row. Only touched on the render/event thread.
var toolsRowRects []sdl.Rect

var toolsPanelRect sdl.Rect

// toolsIPCache avoids calling net.Interfaces() every frame while the panel is
// open on a device with a slow /proc.
var toolsIPCache struct {
	mu sync.Mutex
	ip string
	at time.Time
}

func toolsIPAddress() string {
	toolsIPCache.mu.Lock()
	defer toolsIPCache.mu.Unlock()
	if time.Since(toolsIPCache.at) < 2*time.Second {
		return toolsIPCache.ip
	}
	toolsIPCache.ip = localIPv4()
	toolsIPCache.at = time.Now()
	return toolsIPCache.ip
}

// openToolsPanel shows the panel and refreshes the published network values.
func openToolsPanel() {
	// Close Quick Settings so two modals never stack on top of each other.
	qs.open = false
	// The companion QR card is also a modal; opening Tools replaces it.
	if companion.open {
		closeCompanionOverlay()
	}
	toolsPanel.open = true
	toolsPanel.cursor = 0
	toolsPanelRect = sdl.Rect{}
	toolsRowRects = nil
	toolsIPCache.mu.Lock()
	toolsIPCache.at = time.Time{}
	toolsIPCache.mu.Unlock()
	publishFTPState()
	publishSSHState()
	PlayActivateSound()
}

func closeToolsPanel() {
	toolsPanel.open = false
	PlayBackSound()
}

// sceneIndexExact returns the scene index or -1. findSceneIndex deliberately
// falls back to 0, which would silently navigate back to Home.
func sceneIndexExact(config *Config, name string) int {
	if config == nil {
		return -1
	}
	for i, scene := range config.Scenes {
		if scene.Name == name {
			return i
		}
	}
	return -1
}

// ensureToolsMenuEntry adds an "FTP & IP" button to the classic Misc scene so
// the Tools screen shows the new panel on configs that still use that scene.
// The edit is in-memory and idempotent: the user's config file is never
// rewritten, and a scene that already has a tools_open trigger is left alone.
func ensureToolsMenuEntry(config *Config) {
	if config == nil {
		return
	}
	for i := range config.Scenes {
		scene := &config.Scenes[i]
		if scene.Name != "Misc" {
			continue
		}
		for _, e := range scene.Elements {
			if e.Trigger == "tools_open" {
				return
			}
		}
		var maxY int32
		for _, e := range scene.Elements {
			h := safeInt(string(e.Height), 55)
			if e.Y+h > maxY {
				maxY = e.Y + h
			}
		}
		y := maxY + 10
		if y+55 > int32(config.Height)-40 {
			return
		}
		scene.Elements = append(scene.Elements, Element{
			Type:    "button",
			Text:    "FTP & IP",
			Color:   "#F8FAFC",
			BgColor: "#334155",
			X:       24,
			Y:       y,
			Width:   "296",
			Height:  "55",
			Trigger: "tools_open",
		})
		return
	}
}

// The shared folder is resolved once: the panel redraws every frame and stats
// for a nonexistent /mnt/SDCARD in a render loop are pure waste.
var ftpRootCache struct {
	sync.Once
	root string
}

func cachedFTPRoot() string {
	ftpRootCache.Do(func() { ftpRootCache.root = ftpRootDir() })
	return ftpRootCache.root
}

// toolsRows builds the current rows, most-used first: the LAN address people
// copy to type into a PC, then the file servers and the phone companion, then
// the shared-session and maintenance tools. The rows are grouped into sections
// so a long list stays scannable.
func toolsRows(config *Config) []toolsRow {
	ip := toolsIPAddress()

	share := make([]toolsRow, 0, 4)
	ipRow := toolsRow{
		label:   "IP address",
		value:   "No WiFi address",
		detail:  "Connect to WiFi to transfer files",
		hint:    hintLine(FooterHint{"A", "Copy"}),
		action:  toolsActionCopyIP,
		enabled: true,
		status:  toolsStatusWarn,
	}
	if ip != "" {
		ipRow.value = ip
		ipRow.detail = "Other devices use this address"
		ipRow.status = toolsStatusGood
	}
	if !IsOnline() {
		ipRow.detail = "Offline — connect WiFi first"
		ipRow.status = toolsStatusWarn
	}
	share = append(share, ipRow)

	ftp := toolsRow{
		label:   "FTP server",
		value:   "Stopped",
		detail:  "Share " + cachedFTPRoot() + " with a PC on this WiFi",
		hint:    hintLine(FooterHint{"A", "Start"}),
		action:  toolsActionFTP,
		enabled: true,
	}
	if ftpSrv.Running() {
		addr := ip
		if addr == "" {
			addr = "this device"
		}
		ftp.value = "Running"
		ftp.detail = fmt.Sprintf("ftp://%s:%d", addr, ftpSrv.Port())
		ftp.hint = hintLine(FooterHint{"A", "Stop"})
		ftp.status = toolsStatusGood
	}
	share = append(share, ftp)

	// The phone companion is the answer to typing on a D-pad, so it belongs
	// next to the file servers rather than buried in the classic Tools scene.
	companionRow := toolsRow{
		label:   "Phone companion",
		value:   "Scan to connect",
		detail:  "Type, use a remote and send files from your phone",
		hint:    hintLine(FooterHint{"A", "Show QR Code"}),
		action:  toolsActionCompanion,
		enabled: true,
	}
	if companion.err != "" {
		companionRow.value = "Offline"
		companionRow.hint = hintLine(FooterHint{"A", "Details"})
		companionRow.status = toolsStatusWarn
	} else if companion.open {
		companionRow.value = companionStatusLine()
		companionRow.hint = hintLine(FooterHint{"A", "Hide"})
		companionRow.status = toolsStatusGood
	}
	share = append(share, companionRow)

	sshRow := toolsRow{
		label:   "SSH / SFTP",
		value:   "Stopped",
		detail:  "Browse the file system from a PC with any SFTP client",
		hint:    hintLine(FooterHint{"A", "Start"}),
		action:  toolsActionSSH,
		enabled: true,
	}
	if sshSrv.Running() {
		addr := ip
		if addr == "" {
			addr = "this device"
		}
		sshRow.value = "Running"
		sshRow.detail = fmt.Sprintf("sftp://%s@%s:%d", sshSrv.User(), addr, sshSrv.Port())
		sshRow.hint = hintLine(FooterHint{"A", "Stop"})
		sshRow.status = toolsStatusGood
	}
	share = append(share, sshRow)

	together := make([]toolsRow, 0, 1)
	partyRow := toolsRow{
		label:   "Watch Party",
		value:   "LAN",
		detail:  "Watch the same stream together, with a shared queue",
		hint:    hintLine(FooterHint{"A", "Open"}),
		action:  toolsActionParty,
		enabled: true,
	}
	if partyActive() {
		partyRow.value = "In a room"
		partyRow.detail = partyInviteText()
		partyRow.status = toolsStatusGood
	}
	together = append(together, partyRow)

	system := make([]toolsRow, 0, 5)
	if sceneIndexExact(config, "Patch") >= 0 {
		patchRow := toolsRow{
			label:   "Updates",
			value:   "Check for updates",
			detail:  "Updates, packages and rollback",
			hint:    hintLine(FooterHint{"A", "Open"}),
			action:  toolsActionPatch,
			enabled: true,
		}
		if availableUpdateTag() != "" {
			patchRow.value = "Update " + availableUpdateLabel()
			patchRow.status = toolsStatusWarn
		}
		system = append(system, patchRow)
	}

	system = append(system, toolsRow{
		label:   "Power tools",
		value:   "Network · Files · Scripts",
		detail:  "Port scan, hex viewer, snippets, config history",
		hint:    hintLine(FooterHint{"A", "Open"}),
		action:  toolsActionPowerTools,
		enabled: true,
	})

	if sceneIndexExact(config, "Calculator") >= 0 {
		system = append(system, toolsRow{
			label:   "Calculator",
			value:   "Open",
			detail:  "Quick math with history",
			hint:    hintLine(FooterHint{"A", "Open"}),
			action:  toolsActionCalculator,
			enabled: true,
		})
	}

	system = append(system, toolsRow{
		label:   "Community & support",
		value:   "Changelog · Bugs",
		detail:  "What's new, report a bug, anonymous diagnostics",
		hint:    hintLine(FooterHint{"A", "Open"}),
		action:  toolsActionCommunity,
		enabled: true,
	})

	if sceneIndexExact(config, "Misc") >= 0 {
		system = append(system, toolsRow{
			label:   "Classic Tools",
			value:   "Terminal · Logs",
			detail:  "Terminal, cron, logs, patch and more",
			hint:    hintLine(FooterHint{"A", "Open"}),
			action:  toolsActionUtilities,
			enabled: true,
		})
	}

	// One heading per group; rows are added conditionally, so the label goes on
	// whichever row ended up first.
	markToolsSection(share, "SHARE & TRANSFER")
	markToolsSection(together, "WATCH TOGETHER")
	markToolsSection(system, "SYSTEM & UPDATES")

	rows := make([]toolsRow, 0, len(share)+len(together)+len(system))
	rows = append(rows, share...)
	rows = append(rows, together...)
	rows = append(rows, system...)
	return rows
}

// markToolsSection labels the first row of a group so the renderer can draw one
// heading per group.
func markToolsSection(rows []toolsRow, title string) {
	if len(rows) > 0 {
		rows[0].section = title
	}
}

func toolsPanelMove(delta int) {
	rows := toolsRows(appConfig)
	if len(rows) == 0 {
		return
	}
	toolsPanel.cursor += delta
	if toolsPanel.cursor < 0 {
		toolsPanel.cursor = len(rows) - 1
	}
	if toolsPanel.cursor >= len(rows) {
		toolsPanel.cursor = 0
	}
	PlayNavSound()
}

func toolsPanelActivate(config *Config) {
	rows := toolsRows(config)
	if toolsPanel.cursor < 0 || toolsPanel.cursor >= len(rows) {
		return
	}
	row := rows[toolsPanel.cursor]
	switch row.action {
	case toolsActionFTP:
		toggleFTPServer()
	case toolsActionSSH:
		toggleSSHServer()
	case toolsActionParty:
		// The Watch Party panel replaces Tools; two full-screen overlays
		// stacked would be unreadable.
		toolsPanel.open = false
		partyOpenPanel()
	case toolsActionCompanion:
		// The QR card replaces the panel: both are full-screen overlays and
		// two of them stacked would be unreadable.
		toolsPanel.open = false
		if !IsOnline() || toolsIPAddress() == "" {
			ShowToast("Connect WiFi first — the phone needs an address", ToastKindWarning)
			return
		}
		openCompanionOverlay()
	case toolsActionPatch:
		idx := sceneIndexExact(config, "Patch")
		if idx < 0 {
			ShowToast("This build has no Patch scene", ToastKindInfo)
			return
		}
		toolsPanel.open = false
		changeSceneTo(config, idx)
	case toolsActionCalculator:
		idx := sceneIndexExact(config, "Calculator")
		if idx < 0 {
			ShowToast("This build has no Calculator scene", ToastKindInfo)
			return
		}
		toolsPanel.open = false
		changeSceneTo(config, idx)
	case toolsActionCommunity:
		toolsPanel.open = false
		openCommunityPanel(config, communityPageHome)
		PlayActivateSound()
	case toolsActionPowerTools:
		openPowerTools(config)
	case toolsActionCopyIP:
		ip := toolsIPAddress()
		if ip == "" {
			ShowToast("No WiFi address yet — connect to WiFi", ToastKindWarning)
			return
		}
		if err := sdl.SetClipboardText(ip); err != nil {
			ShowToast("Could not copy the address", ToastKindError)
			return
		}
		PlaySuccessSound()
		ShowToast("Copied "+ip, ToastKindSuccess)
	case toolsActionUtilities:
		idx := sceneIndexExact(config, "Misc")
		if idx < 0 {
			ShowToast("This build has no classic Tools scene", ToastKindInfo)
			return
		}
		toolsPanel.open = false
		changeSceneTo(config, idx)
	}
}

// toggleFTPServer starts or stops the server and reports the result. It is also
// used by the "ftp_toggle" config trigger.
func toggleFTPServer() {
	if ftpSrv.Running() {
		ftpSrv.Stop()
		ShowToast("FTP server stopped", ToastKindInfo)
		return
	}
	if !IsOnline() || toolsIPAddress() == "" {
		ShowToast("Connect WiFi first — FTP needs a network address", ToastKindWarning)
		return
	}
	if err := ftpSrv.Start(); err != nil {
		ShowToast("FTP failed: "+firstLine(err.Error()), ToastKindError)
		return
	}
	PlaySuccessSound()
	ShowToast("FTP on "+ftpSrv.Address(), ToastKindSuccess)
}

// ctrlToolsPanel is called before every other controller overlay.
func ctrlToolsPanel(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		toolsPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		toolsPanelMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		toolsPanelActivate(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeToolsPanel()
	}
	return true
}

// handleToolsKey processes keyboard input while the panel is open. It consumes
// every key so the scene underneath never reacts to typing behind the modal.
func handleToolsKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !toolsPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		toolsPanelMove(-1)
	case sdl.K_DOWN:
		toolsPanelMove(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		toolsPanelActivate(config)
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeToolsPanel()
	}
	return true
}

// handleToolsMouse activates a clicked row, or closes the panel when the click
// lands outside it. Returns true when the click was consumed.
func handleToolsMouse(mx, my int32, config *Config) bool {
	if !toolsPanel.open {
		return false
	}
	for i, r := range toolsRowRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			toolsPanel.cursor = i
			toolsPanelActivate(config)
			return true
		}
	}
	if toolsPanelRect.W > 0 && !pointInRect(mx, my, toolsPanelRect) {
		closeToolsPanel()
	}
	return true
}

// renderToolsPanel draws the modal over the current scene.
// toolsInfoLine is one line of live server detail under the rows.
type toolsInfoLine struct {
	text string
	warn bool
}

// toolsInfoLines returns at most one line of live server detail: the SFTP
// credentials when it runs, plus the exposure warning in the same line. One
// line is what fits under the rows on a 720p handheld once the list is grouped
// into sections, and the password must not be the thing that gets dropped.
func toolsInfoLines() []toolsInfoLine {
	if sshSrv.Running() {
		line := fmt.Sprintf("%s · password %s", sshSrv.User(), sshSrv.Password())
		if ftpSrv.Running() {
			line += " — FTP is open to this WiFi"
		} else {
			line += " — SFTP only, no shell"
		}
		return []toolsInfoLine{{text: line, warn: true}}
	}
	if ftpSrv.Running() {
		return []toolsInfoLine{{text: "Anyone on this WiFi can read and write the shared folder.", warn: true}}
	}
	return nil
}

// toolsPanelLayout is the measured geometry for the current font, rows and
// server state. Handheld fonts can be taller than their point size, so a fixed
// row height used to push the last row and the footer off a 720p screen. When
// the full three-line row does not fit, the detail line is folded into the value
// and the row padding shrinks; section headings are paid for from the same
// budget.
type toolsPanelLayout struct {
	smallH, medH, sectionH, headerH, footerH, rowH, rowPad, infoLineH, infoH int32
	showDetail, dense                                                        bool
	info                                                                     []toolsInfoLine
	sections                                                                 int
}

// contentH is the height the panel needs for rows, headings and the footer.
func (l toolsPanelLayout) contentH(rows int) int32 {
	return l.headerH + int32(rows)*l.rowH + int32(l.sections)*l.sectionH + l.infoH + l.footerH
}

func toolsPanelMetrics(config *Config, rows []toolsRow) toolsPanelLayout {
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return toolsPanelLayout{}
	}
	if medium == nil {
		medium = font
	}
	const gap = int32(2)
	layout := toolsPanelLayout{
		smallH:     int32(font.Height()),
		medH:       int32(medium.Height()),
		showDetail: true,
		info:       toolsInfoLines(),
	}
	for _, row := range rows {
		if row.section != "" {
			layout.sections++
		}
	}
	// Section headings are a fixed, small size so they read as labels instead
	// of another row, whatever the theme's body fonts are set to.
	layout.sectionH = 24
	if label := getFontSized(config, "small", 15); label != nil {
		layout.sectionH = int32(label.Height()) + 10
		if layout.sectionH < 22 {
			layout.sectionH = 22
		}
	}
	layout.headerH = 18 + layout.medH + 4 + layout.smallH + 12
	layout.footerH = 18 + layout.smallH
	layout.infoLineH = layout.smallH + 8
	if len(layout.info) > 0 {
		layout.infoH = int32(len(layout.info))*layout.infoLineH + 5
	}
	layout.rowPad = 10
	layout.rowH = 2*layout.rowPad + layout.smallH + gap + layout.medH + gap + layout.smallH

	available := screenHeight - 40
	fits := func() bool { return layout.contentH(len(rows)) <= available }
	if !fits() {
		layout.showDetail = false
		layout.rowPad = 8
		layout.rowH = 2*layout.rowPad + layout.smallH + gap + layout.medH
	}
	if !fits() {
		// Dense mode: label and value on one line. Patch and Calculator add
		// two rows, which does not fit the three-line row on a 720p screen.
		layout.dense = true
		layout.rowPad = 5
		layout.rowH = max32(layout.smallH, layout.medH) + 2*layout.rowPad
	}
	if !fits() && len(layout.info) > 1 {
		layout.info = layout.info[:1]
		layout.infoH = layout.infoLineH + 5
	}
	if !fits() {
		layout.info = nil
		layout.infoH = 0
	}
	return layout
}

func renderToolsPanel(renderer *sdl.Renderer, config *Config) {
	if !toolsPanel.open || renderer == nil {
		return
	}
	rows := toolsRows(config)
	if toolsPanel.cursor >= len(rows) {
		toolsPanel.cursor = len(rows) - 1
	}
	if toolsPanel.cursor < 0 {
		toolsPanel.cursor = 0
	}

	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}

	// Sizes come from the fonts, not from constants: a config that does not
	// override "small"/"medium" gets 24px for both, and fixed offsets used to
	// stack the label, value and detail on top of each other.
	layout := toolsPanelMetrics(config, rows)
	smallH := layout.smallH
	medH := layout.medH
	const (
		padX = int32(24)
		// label + gap + value + gap + detail
		rowTextH = int32(2)
	)
	rowH := layout.rowH
	headerH := layout.headerH
	footerH := layout.footerH
	sectionH := layout.sectionH
	// A little wider than before: the values (addresses, "Network · Files ·
	// Scripts") are the point of the panel and used to be ellipsised.
	panelW := int32(880)
	if panelW > screenWidth-64 {
		panelW = screenWidth - 64
	}
	info := layout.info
	infoLineH := layout.infoLineH
	panelH := layout.contentH(len(rows))
	if panelH > screenHeight-40 {
		panelH = screenHeight - 40
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	toolsPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	// Accent hairline at the top of the panel.
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	renderText(renderer, config, medium, "Tools", ColorTextPrimary(), panelX+padX, panelY+16)
	sub := "Files, network and device utilities"
	if !IsOnline() {
		sub = "Offline — connect WiFi to transfer files"
	}
	renderText(renderer, config, font, sub, ColorTextTertiary(), panelX+padX, panelY+16+medH+4)

	// The address every transfer tool below needs is readable from the header
	// without hunting for it in the list.
	statusText, statusCol := "Offline", ColorToastWarn
	if ip := toolsIPAddress(); ip != "" {
		statusText, statusCol = "WiFi · "+ip, ColorTextTertiary()
	}
	if sw, _, err := font.SizeUTF8(statusText); err == nil {
		renderText(renderer, config, font, statusText, statusCol,
			panelX+panelW-padX-int32(sw), panelY+16+(medH-smallH)/2)
	}

	// The theme accent can be very dark (several presets use a near-black
	// "info" colour), so reuse the install card's contrast check instead of
	// painting the selected row with an invisible tint.
	focusColor := installHighlight()

	rowW := panelW - 2*padX
	toolsRowRects = make([]sdl.Rect, len(rows))
	y := panelY + headerH
	for i, row := range rows {
		if row.section != "" {
			drawToolsSectionLabel(renderer, config, row.section, panelX+padX, y, rowW)
			y += sectionH
		}
		rowGap := int32(8)
		if layout.dense {
			// Dense rows keep a 36px card at the shipped font size; the gap is
			// trimmed instead of the text padding.
			rowGap = 4
		}
		r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - rowGap}
		toolsRowRects[i] = r
		y += rowH

		focused := i == toolsPanel.cursor
		rowBg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			rowBg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
			// Accent spine: the cursor position reads at a glance instead of
			// relying on a 1px outline alone.
			fillRoundedRect(renderer, r.X+1, r.Y+8, 4, r.H-16, 2, focusColor)
		}
		// A status dot per row: running / available / needs attention. It is
		// cheaper to scan than the value text and gives the list a rhythm.
		fillCircle(renderer, r.X+20, r.Y+r.H/2, 4, toolsStatusColor(row.status))

		valueColor := ColorTextPrimary()
		switch {
		case row.status == toolsStatusWarn:
			valueColor = ColorToastWarn
		case (row.action == toolsActionFTP || row.action == toolsActionSSH) && row.status == toolsStatusGood:
			valueColor = ColorToastSuccess
		}
		value := row.value
		if !layout.showDetail && value == "Running" && row.detail != "" {
			// Fold the address into the value line when there is no room for a
			// separate detail line: it is the part the user needs to type.
			value = row.detail
		}

		textX := r.X + 38
		if layout.dense {
			// One line per row: label in a fixed column, value after it, action
			// chip on the right. The fixed column keeps every value aligned, so
			// the list reads as a table instead of ragged text.
			chipW := drawToolsHintChip(renderer, config, font, row.hint, r, focused, focusColor)
			labelCol := (r.W - 38 - 24) * 34 / 100
			label := ellipsize(font, row.label, labelCol)
			renderText(renderer, config, font, label, ColorTextSecondary(), textX, r.Y+(r.H-smallH)/2)
			valueX := textX + labelCol
			valueW := r.X + r.W - 12 - chipW - 12 - valueX
			if valueW > 0 {
				renderText(renderer, config, medium, ellipsize(medium, value, valueW), valueColor, valueX, r.Y+(r.H-medH)/2)
			}
			continue
		}

		labelY := r.Y + layout.rowPad
		valueY := labelY + smallH + rowTextH
		detailY := valueY + medH + rowTextH
		renderText(renderer, config, font, row.label, ColorTextSecondary(), textX, labelY)
		renderText(renderer, config, medium, value, valueColor, textX, valueY)

		if layout.showDetail && row.detail != "" {
			maxW := r.W - (textX - r.X) - 150
			renderText(renderer, config, font, ellipsize(font, row.detail, maxW), ColorTextTertiary(), textX, detailY)
		}
		drawToolsHintChip(renderer, config, font, row.hint, r, focused, focusColor)
	}

	// Live details while a server is open: the SFTP password is shown here (it
	// is random per run and never written to the config or the log), followed
	// by the warning for whichever share is exposed.
	infoY := panelY + headerH + int32(len(rows))*rowH + int32(layout.sections)*sectionH + 2
	for _, line := range info {
		color := ColorTextSecondary()
		if line.warn {
			color = ColorToastWarn
		}
		renderText(renderer, config, font, ellipsize(font, line.text, rowW), color, panelX+padX, infoY)
		infoY += infoLineH
	}

	footer := hintLine(FooterHint{"A", "Select"}, FooterHint{"B", "Close"})
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-footerH+8)
}

// drawToolsSectionLabel paints a group heading: small uppercase text with a
// hairline running to the panel edge, so the list reads in sections without
// spending a filled row on a divider.
func drawToolsSectionLabel(renderer *sdl.Renderer, config *Config, title string, x, y, w int32) {
	font := getFontSized(config, "small", 15)
	if font == nil {
		return
	}
	label := strings.ToUpper(title)
	tw, th, _ := font.SizeUTF8(label)
	renderText(renderer, config, font, label, ColorTextTertiary(), x+38, y+4)
	if lx := x + 38 + int32(tw) + 12; lx < x+w {
		renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 80)
		renderer.FillRect(&sdl.Rect{X: lx, Y: y + 4 + int32(th)/2, W: x + w - lx, H: 1})
	}
}

// drawToolsHintChip paints the row's action ("A Start", "A Copy") as a pill on
// the right edge and returns its width, so the value never runs under it.
func drawToolsHintChip(renderer *sdl.Renderer, config *Config, font *ttf.Font, hint string, row sdl.Rect, focused bool, focus sdl.Color) int32 {
	if font == nil || hint == "" {
		return 0
	}
	tw, th, _ := font.SizeUTF8(hint)
	padX := int32(9)
	h := int32(th) + 6
	w := int32(tw) + 2*padX
	x := row.X + row.W - w - 10
	y := row.Y + (row.H-h)/2
	fill := WithAlpha(ColorSurface, 235)
	border := WithAlpha(ColorBorder, 140)
	label := ColorTextSecondary()
	if focused {
		fill = WithAlpha(accentColor, 90)
		border = WithAlpha(focus, 220)
		label = ColorTextPrimary()
	}
	fillRoundedRect(renderer, x, y, w, h, h/2, fill)
	strokeRoundedRect(renderer, x, y, w, h, h/2, 1, border)
	renderText(renderer, config, font, hint, label, x+padX, y+(h-int32(th))/2)
	return w
}

// toolsStatusColor maps a row's state to the dot beside it.
func toolsStatusColor(status toolsStatus) sdl.Color {
	switch status {
	case toolsStatusGood:
		return ColorToastSuccess
	case toolsStatusWarn:
		return ColorToastWarn
	}
	return WithAlpha(ColorTextTertiary(), 120)
}
