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
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

type toolsAction int

const (
	toolsActionFTP toolsAction = iota
	toolsActionSSH
	toolsActionCompanion
	toolsActionParty
	toolsActionPatch
	toolsActionCalculator
	toolsActionCopyIP
	toolsActionUtilities
	toolsActionClose
)

type toolsRow struct {
	label   string
	value   string
	detail  string
	hint    string
	action  toolsAction
	enabled bool
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

// toolsRows builds the current rows. The utilities row only exists when the
// config actually has the classic Misc scene.
func toolsRows(config *Config) []toolsRow {
	rows := make([]toolsRow, 0, 4)

	ftp := toolsRow{
		label:   "FTP server",
		value:   "Stopped",
		detail:  "Share " + cachedFTPRoot() + " with a PC on this WiFi",
		hint:    "A: start",
		action:  toolsActionFTP,
		enabled: true,
	}
	if ftpSrv.Running() {
		ip := toolsIPAddress()
		if ip == "" {
			ip = "this device"
		}
		ftp.value = "Running"
		ftp.detail = fmt.Sprintf("ftp://%s:%d", ip, ftpSrv.Port())
		ftp.hint = "A: stop"
	}
	rows = append(rows, ftp)

	sshRow := toolsRow{
		label:   "SSH / SFTP",
		value:   "Stopped",
		detail:  "Browse the file system from a PC with any SFTP client",
		hint:    "A: start",
		action:  toolsActionSSH,
		enabled: true,
	}
	if sshSrv.Running() {
		ip := toolsIPAddress()
		if ip == "" {
			ip = "this device"
		}
		sshRow.value = "Running"
		sshRow.detail = fmt.Sprintf("sftp://%s@%s:%d", sshSrv.User(), ip, sshSrv.Port())
		sshRow.hint = "A: stop"
	}
	rows = append(rows, sshRow)

	// The phone companion is the answer to typing on a D-pad, so it belongs
	// next to the file servers rather than buried in the classic Tools scene.
	companionRow := toolsRow{
		label:   "Phone companion",
		value:   "Scan to connect",
		detail:  "Type, use a remote and send files from your phone",
		hint:    "A: show QR code",
		action:  toolsActionCompanion,
		enabled: true,
	}
	if companion.open {
		companionRow.value = companionStatusLine()
		companionRow.hint = "A: hide"
	}
	rows = append(rows, companionRow)

	partyRow := toolsRow{
		label:   "Watch Party",
		value:   "LAN",
		detail:  "Watch the same stream together, with a shared queue",
		hint:    "A: open",
		action:  toolsActionParty,
		enabled: true,
	}
	if partyActive() {
		partyRow.value = "In a room"
		partyRow.detail = partyInviteText()
	}
	rows = append(rows, partyRow)

	if sceneIndexExact(config, "Patch") >= 0 {
		patchRow := toolsRow{
			label:   "Patch",
			value:   "Check for updates",
			detail:  "Updates, packages and rollback",
			hint:    "A: open",
			action:  toolsActionPatch,
			enabled: true,
		}
		if availableUpdateTag() != "" {
			patchRow.value = "Update " + availableUpdateLabel()
		}
		rows = append(rows, patchRow)
	}
	if sceneIndexExact(config, "Calculator") >= 0 {
		rows = append(rows, toolsRow{
			label:   "Calculator",
			value:   "Open",
			detail:  "Quick math with history",
			hint:    "A: open",
			action:  toolsActionCalculator,
			enabled: true,
		})
	}

	ip := toolsIPAddress()
	ipRow := toolsRow{
		label:   "IP address",
		value:   "No WiFi address",
		detail:  "Connect to WiFi to transfer files",
		hint:    "A: copy",
		action:  toolsActionCopyIP,
		enabled: true,
	}
	if ip != "" {
		ipRow.value = ip
		ipRow.detail = "Other devices use this address"
	}
	if !IsOnline() {
		ipRow.detail = "Offline — connect WiFi first"
	}
	rows = append(rows, ipRow)

	if sceneIndexExact(config, "Misc") >= 0 {
		rows = append(rows, toolsRow{
			label:   "System utilities",
			value:   "Classic Tools",
			detail:  "Terminal, cron, logs, patch and more",
			hint:    "A: open",
			action:  toolsActionUtilities,
			enabled: true,
		})
	}

	rows = append(rows, toolsRow{
		label:   "Close",
		value:   "Back to the app",
		hint:    "B",
		action:  toolsActionClose,
		enabled: true,
	})
	return rows
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
	case toolsActionClose:
		closeToolsPanel()
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

// toolsInfoLines returns at most two lines: the SFTP credentials while the
// server runs, then the warning for the exposed share. Two lines is what fits
// on a 720p handheld without pushing the footer off the panel.
func toolsInfoLines() []toolsInfoLine {
	lines := make([]toolsInfoLine, 0, 2)
	if sshSrv.Running() {
		lines = append(lines, toolsInfoLine{text: fmt.Sprintf("%s  ·  password %s", sshSrv.User(), sshSrv.Password())})
	}
	switch {
	case ftpSrv.Running() && sshSrv.Running():
		lines = append(lines, toolsInfoLine{text: "FTP is open to this WiFi; SFTP allows no shell or tunnels.", warn: true})
	case ftpSrv.Running():
		lines = append(lines, toolsInfoLine{text: "Anyone on this WiFi can read and write the shared folder.", warn: true})
	case sshSrv.Running():
		lines = append(lines, toolsInfoLine{text: "SFTP only: no shell, port forwarding or tunnels.", warn: true})
	}
	return lines
}

// toolsPanelLayout is the measured geometry for the current font, row count and
// server state. Handheld fonts can be taller than their point size, so a fixed
// row height used to push the last row and the footer off a 720p screen once
// the SSH row was added. When the full three-line row does not fit, the detail
// line is folded into the value and the row padding shrinks.
type toolsPanelLayout struct {
	smallH, medH, headerH, footerH, rowH, rowPad, infoLineH, infoH int32
	showDetail, dense                                              bool
	info                                                           []toolsInfoLine
}

func toolsPanelMetrics(config *Config, rowCount int) toolsPanelLayout {
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
	layout.headerH = 18 + layout.medH + 4 + layout.smallH + 14
	layout.footerH = 20 + layout.smallH
	layout.infoLineH = layout.smallH + 8
	if len(layout.info) > 0 {
		layout.infoH = int32(len(layout.info))*layout.infoLineH + 5
	}
	layout.rowPad = 10
	layout.rowH = 2*layout.rowPad + layout.smallH + gap + layout.medH + gap + layout.smallH

	available := screenHeight - 40 - layout.headerH - layout.footerH
	total := func() int32 {
		return int32(rowCount)*layout.rowH + layout.infoH
	}
	if total() > available {
		layout.showDetail = false
		layout.rowPad = 8
		layout.rowH = 2*layout.rowPad + layout.smallH + gap + layout.medH
	}
	if total() > available {
		// Dense mode: label and value on one line. Patch and Calculator add
		// two rows, which does not fit the three-line row on a 720p screen.
		layout.dense = true
		layout.rowPad = 9
		layout.rowH = max32(layout.smallH, layout.medH) + 2*layout.rowPad
	}
	if total() > available && len(layout.info) > 1 {
		layout.info = layout.info[:1]
		layout.infoH = layout.infoLineH + 5
	}
	if total() > available {
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
	layout := toolsPanelMetrics(config, len(rows))
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
	panelW := int32(760)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	info := layout.info
	infoLineH := layout.infoLineH
	infoH := layout.infoH
	panelH := headerH + int32(len(rows))*rowH + infoH + footerH
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

	renderText(renderer, config, medium, "Tools", ColorTextPrimary(), panelX+padX, panelY+18)
	sub := "Network address and file transfer"
	if !IsOnline() {
		sub = "Offline — connect WiFi to use these"
	}
	renderText(renderer, config, font, sub, ColorTextTertiary(), panelX+padX, panelY+18+medH+4)

	// The theme accent can be very dark (several presets use a near-black
	// "info" colour), so reuse the install card's contrast check instead of
	// painting the selected row with an invisible tint.
	focusColor := installHighlight()

	rowW := panelW - 2*padX
	toolsRowRects = make([]sdl.Rect, len(rows))
	for i, row := range rows {
		y := panelY + headerH + int32(i)*rowH
		r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
		toolsRowRects[i] = r

		focused := i == toolsPanel.cursor
		rowBg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			rowBg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}

		valueColor := ColorTextPrimary()
		if (row.action == toolsActionFTP && ftpSrv.Running()) || (row.action == toolsActionSSH && sshSrv.Running()) {
			valueColor = ColorToastSuccess
		}
		if row.action == toolsActionPatch && availableUpdateTag() != "" {
			valueColor = ColorToastWarn
		}
		if row.action == toolsActionCopyIP && row.value == "No WiFi address" {
			valueColor = ColorToastWarn
		}
		value := row.value
		if !layout.showDetail && value == "Running" && row.detail != "" {
			// Fold the address into the value line when there is no room for a
			// separate detail line: it is the part the user needs to type.
			value = row.detail
		}

		if layout.dense {
			// One line per row: label on the left, value after it, hint on the
			// right. Dense mode exists so Patch, Calculator and both servers fit
			// on a 720p screen without shrinking the text.
			hintColor := ColorTextSecondary()
			if focused {
				hintColor = focusColor
			}
			hintW := int32(0)
			if row.hint != "" {
				hw, _, _ := font.SizeUTF8(row.hint)
				hintW = int32(hw)
				renderText(renderer, config, font, row.hint, hintColor, r.X+r.W-hintW-16, r.Y+(r.H-smallH)/2)
			}
			label := ellipsize(font, row.label, r.W/3)
			lw, _, _ := font.SizeUTF8(label)
			renderText(renderer, config, font, label, ColorTextSecondary(), r.X+16, r.Y+(r.H-smallH)/2)
			valueX := r.X + 16 + int32(lw) + 14
			valueW := r.X + r.W - 16 - hintW - 16 - valueX
			if valueW > 0 {
				renderText(renderer, config, medium, ellipsize(medium, value, valueW), valueColor, valueX, r.Y+(r.H-medH)/2)
			}
			continue
		}

		labelY := r.Y + layout.rowPad
		valueY := labelY + smallH + rowTextH
		detailY := valueY + medH + rowTextH
		renderText(renderer, config, font, row.label, ColorTextSecondary(), r.X+16, labelY)
		renderText(renderer, config, medium, value, valueColor, r.X+16, valueY)

		detail := row.detail
		if layout.showDetail && detail != "" {
			maxW := r.W - 32 - 150
			renderText(renderer, config, font, ellipsize(font, detail, maxW), ColorTextTertiary(), r.X+16, detailY)
		}
		if row.hint != "" {
			hintColor := ColorTextSecondary()
			if focused {
				hintColor = focusColor
			}
			hw, _, _ := font.SizeUTF8(row.hint)
			renderText(renderer, config, font, row.hint, hintColor, r.X+r.W-int32(hw)-16, valueY)
		}
	}

	// Live details while a server is open: the SFTP password is shown here (it
	// is random per run and never written to the config or the log), followed
	// by the warning for whichever share is exposed.
	infoY := panelY + headerH + int32(len(rows))*rowH + 2
	for _, line := range info {
		color := ColorTextSecondary()
		if line.warn {
			color = ColorToastWarn
		}
		renderText(renderer, config, font, ellipsize(font, line.text, rowW), color, panelX+padX, infoY)
		infoY += infoLineH
	}

	footer := "A select    B close"
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-footerH+8)
}
