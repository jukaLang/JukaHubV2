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
	"math"
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
	toolsActionToolbox
	toolsActionSecurity
	toolsActionDiscHelpers
	toolsActionCopyIP
	toolsActionUtilities
	// Network probes. They used to be a Power tools page; they are transfer
	// tools, so they live here under the same FTP & IP heading.
	toolsActionGatewayScan
	toolsActionClipboardScan
	toolsActionPing
	toolsActionTraceroute
	toolsActionDNSLookup
	toolsActionWiFiSignal
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
	// scroll is the pixel offset of the tile window: the grid is taller than
	// the panel, so the cursor moves the window instead of the panel growing
	// off screen.
	scroll int
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
	toolsPanel.scroll = 0
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
			Color:   "$labelColor",
			BgColor: "$buttonColor",
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

	// The network probes used to be a page inside Power tools. They answer the
	// same question as the rows above ("is this thing on the network?"), so they
	// belong under the same heading instead of two menus deep.
	gateway := powerGatewayGuess(ip)
	if gateway == "" {
		gateway = "No LAN address"
	}
	share = append(share, toolsRow{
		label:   "Gateway port scan",
		value:   gateway,
		detail:  "13 common ports, read-only TCP connects",
		hint:    hintLine(FooterHint{"A", "Scan"}),
		action:  toolsActionGatewayScan,
		enabled: gateway != "No LAN address",
		status:  toolsStatusNeutral,
	})
	share = append(share, toolsRow{
		label:   "Clipboard host scan",
		value:   "Copy a host first",
		detail:  "Same 13 ports, limited to the copied IP or host name",
		hint:    hintLine(FooterHint{"A", "Scan"}),
		action:  toolsActionClipboardScan,
		enabled: true,
	})
	share = append(share, toolsRow{
		label:   "Ping",
		value:   "1.1.1.1",
		detail:  "ICMP if the device has ping, then a TCP fallback",
		hint:    hintLine(FooterHint{"A", "Run"}),
		action:  toolsActionPing,
		enabled: true,
	})
	share = append(share, toolsRow{
		label:   "Traceroute",
		value:   "1.1.1.1",
		detail:  "Uses traceroute/tracert when installed",
		hint:    hintLine(FooterHint{"A", "Trace"}),
		action:  toolsActionTraceroute,
		enabled: true,
	})
	share = append(share, toolsRow{
		label:   "DNS lookup",
		value:   "github.com · example.com",
		detail:  "Uses the resolver configured on this device",
		hint:    hintLine(FooterHint{"A", "Run"}),
		action:  toolsActionDNSLookup,
		enabled: true,
	})
	share = append(share, toolsRow{
		label:   "Wi-Fi signal",
		value:   "Local interface",
		detail:  "Link quality and dBm when associated",
		hint:    hintLine(FooterHint{"A", "Read"}),
		action:  toolsActionWiFiSignal,
		enabled: true,
	})

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
		if updateAvailable() {
			patchRow.value = "Update " + availableUpdateLabel()
			patchRow.status = toolsStatusWarn
		}
		system = append(system, patchRow)
	}

	system = append(system, toolsRow{
		// The FTP & IP probes live in this panel now, so the Power tools row
		// only advertises what is still behind it.
		label:   "Power tools",
		value:   "Files · Scripts · Sync",
		detail:  "Hex viewer, snippets, config history, memory scan",
		hint:    hintLine(FooterHint{"A", "Open"}),
		action:  toolsActionPowerTools,
		enabled: true,
	})

	// The toolbox downloads popular command-line tools on demand and runs them
	// with visible output; the security row summarises the posture the rest of
	// this build already maintains (encryption, signatures, downloads, servers).
	toolboxRow := toolsRow{
		label:   "Toolbox",
		value:   toolboxSummaryValue(),
		detail:  "rclone, jq, ripgrep, fd, pandoc and age — install and run",
		hint:    hintLine(FooterHint{"A", "Open"}),
		action:  toolsActionToolbox,
		enabled: true,
	}
	if toolboxInstalledCount() > 0 {
		toolboxRow.status = toolsStatusGood
	}
	system = append(system, toolboxRow)

	securityValue, securityWarn := securityPostureValue(config)
	securityRow := toolsRow{
		label:   "Security & trust",
		value:   securityValue,
		detail:  "Secrets, storage keys, downloads and open servers",
		hint:    hintLine(FooterHint{"A", "Open"}),
		action:  toolsActionSecurity,
		enabled: true,
	}
	if securityWarn {
		securityRow.status = toolsStatusWarn
	}
	system = append(system, securityRow)

	// Disc-based systems need files the ripper often leaves out: a cue sheet
	// for a bare .bin, and a playlist so a multi-disc set switches discs. Both
	// are additive, so the row is safe to press on any card.
	system = append(system, toolsRow{
		label:   "Disc helpers",
		value:   "CUE & M3U",
		detail:  "Add missing cue sheets and multi-disc playlists in Roms/",
		hint:    hintLine(FooterHint{"A", "Create"}),
		action:  toolsActionDiscHelpers,
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
	markToolsSection(share, "FTP & IP")
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

// toolsPanelMove moves the tile cursor. Left/Right walk the focused row,
// Up/Down jump by the grid's column count — the same ragged-grid semantics as
// the Apps launcher — and both clamp so focus can never wrap somewhere
// unexpected.
func toolsPanelMove(config *Config, dx, dy int) {
	rows := toolsRows(config)
	if len(rows) == 0 {
		return
	}
	prev := toolsPanel.cursor
	switch {
	case dx != 0:
		toolsPanel.cursor += dx
	case dy != 0:
		toolsPanel.cursor += dy * toolsPanelMetrics(config, rows).cols
	}
	if toolsPanel.cursor < 0 {
		toolsPanel.cursor = 0
	}
	if toolsPanel.cursor >= len(rows) {
		toolsPanel.cursor = len(rows) - 1
	}
	if toolsPanel.cursor != prev {
		PlayNavSound()
	}
}

// The tile grid reuses the Apps launcher's look with this panel's data. Each
// tile shows an icon, the tool's name and its status dot; the value ("Running",
// "192.168.1.42") and the detail ("ftp://…") that the rows used to print move
// into a footer that always describes the focused tile, so long addresses no
// longer fight with a small row for space.
type toolsGridMetrics struct {
	cols      int
	tileW     int32
	tileH     int32
	gap       int32
	padX      int32
	panelW    int32
	sectionH  int32
	headerH   int32
	footH     int32
	infoLineH int32
	infoH     int32
	info      []toolsInfoLine
}

type toolsGridLine struct {
	heading  string
	headingY int32
	tilesY   int32
	h        int32
	tiles    []int
}

type toolsGrid struct {
	m     toolsGridMetrics
	lines []toolsGridLine
	rects []sdl.Rect
	bodyH int32
}

// toolsPanelMetrics is the measured geometry for the tile grid. The column
// count comes from the panel width, the bands from the loaded fonts: a device
// theme can draw glyphs taller than their point size, so no measure here is a
// fixed constant.
func toolsPanelMetrics(config *Config, rows []toolsRow) toolsGridMetrics {
	_ = rows
	small, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if medium == nil {
		medium = small
	}
	smallH, medH := int32(28), int32(28)
	if small != nil {
		smallH = int32(small.Height())
	}
	if medium != nil {
		medH = int32(medium.Height())
	}

	m := toolsGridMetrics{
		padX: 24,
		gap:  12,
		info: toolsInfoLines(),
	}
	m.infoLineH = smallH + 8
	if len(m.info) > 1 {
		// One live line is what fits under the grid on a 720p panel, and the
		// SFTP password must not be the thing that gets dropped.
		m.info = m.info[:1]
	}
	if len(m.info) > 0 {
		m.infoH = m.infoLineH + 5
	}

	// The header is the shared page bar: its height comes from the component
	// that draws it, so the panel and the bar cannot disagree about the band.
	headerBand := uiPageHeaderMetricsFor(config, toolsHeader(config, 0, 0, 0))
	m.headerH = 12 + headerBand.Height + 12
	// Footer: value line, detail line, and the closing hint.
	m.footH = 6 + medH + 2 + smallH + 6 + smallH + 6

	m.sectionH = 28
	if label := getFontSized(config, "small", 15); label != nil {
		m.sectionH = int32(label.Height()) + 14
		if m.sectionH < 26 {
			m.sectionH = 26
		}
	}

	labelFont := getFontSized(config, "small", 22)
	if labelFont == nil {
		labelFont = small
	}
	labelH := int32(26)
	if labelFont != nil {
		labelH = int32(labelFont.Height() + 1)
	}
	m.tileH = 8 + 52 + 6 + labelH*2 + 8
	if m.tileH < 96 {
		m.tileH = 96
	}

	m.tileW = 188
	m.panelW = 880
	if m.panelW > screenWidth-64 {
		m.panelW = screenWidth - 64
	}
	bodyW := m.panelW - 2*m.padX
	cols := int((bodyW + m.gap) / (m.tileW + m.gap))
	if cols < 1 {
		cols = 1
	}
	m.cols = cols
	m.tileW = (bodyW - int32(cols-1)*m.gap) / int32(cols)
	return m
}

// toolsGridBuild lays the rows out: each section heading opens its own lane
// and the section's tiles wrap to the grid's width, exactly like the Apps
// launcher wraps its shortcuts. g.rects is in body coordinates; the renderer
// subtracts the scroll offset when it draws.
func toolsGridBuild(rows []toolsRow, m toolsGridMetrics) toolsGrid {
	g := toolsGrid{m: m, rects: make([]sdl.Rect, len(rows))}
	type pendingLine struct {
		heading string
		tiles   []int
	}
	var lines []pendingLine
	cur := pendingLine{}
	for i := range rows {
		if rows[i].section != "" {
			if len(cur.tiles) > 0 {
				lines = append(lines, cur)
				cur = pendingLine{}
			}
			cur.heading = rows[i].section
		}
		cur.tiles = append(cur.tiles, i)
		if len(cur.tiles) == m.cols {
			lines = append(lines, cur)
			cur = pendingLine{}
		}
	}
	if len(cur.tiles) > 0 || cur.heading != "" {
		lines = append(lines, cur)
	}

	var y int32
	for _, l := range lines {
		line := toolsGridLine{heading: l.heading, tiles: l.tiles}
		if l.heading != "" {
			line.headingY = y
			y += m.sectionH
		}
		line.tilesY = y
		n := (len(l.tiles) + m.cols - 1) / m.cols
		line.h = int32(n)*m.tileH + int32(n-1)*m.gap
		for k, idx := range l.tiles {
			col := int32(k % m.cols)
			rowOff := int32(k / m.cols)
			g.rects[idx] = sdl.Rect{
				X: m.padX + col*(m.tileW+m.gap),
				Y: y + rowOff*(m.tileH+m.gap),
				W: m.tileW,
				H: m.tileH,
			}
		}
		y += line.h + m.gap
		g.lines = append(g.lines, line)
	}
	g.bodyH = max32(0, y-m.gap)
	return g
}

// toolsGridEnsureVisible returns the scroll offset that keeps the focused tile
// fully in view and records it, so consecutive moves scroll smoothly instead
// of snapping the focused tile to an edge every frame.
func toolsGridEnsureVisible(g toolsGrid, cursor int, viewH int32) int32 {
	if cursor < 0 || cursor >= len(g.rects) || viewH <= 0 {
		return 0
	}
	scroll := max32(0, int32(toolsPanel.scroll))
	r := g.rects[cursor]
	if r.Y-scroll < 0 {
		scroll = r.Y
	}
	if r.Y+r.H-scroll > viewH {
		scroll = r.Y + r.H - viewH
	}
	if maxScroll := g.bodyH - viewH; maxScroll > 0 && scroll > maxScroll {
		scroll = maxScroll
	} else if maxScroll <= 0 {
		scroll = 0
	}
	toolsPanel.scroll = int(scroll)
	return scroll
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
	case toolsActionToolbox:
		openPowerTools(config)
		powerTools.returnToTools = true
		powerSetPage(powerPageToolbox)
		PlayActivateSound()
	case toolsActionSecurity:
		openPowerTools(config)
		powerTools.returnToTools = true
		powerSetPage(powerPageSecurity)
		PlayActivateSound()
	case toolsActionDiscHelpers:
		report := generateDiscHelpers(romLibraryRoots())
		if report.Created() == 0 {
			if len(report.Errors) > 0 {
				ShowToast(report.Summary(), ToastKindError)
				return
			}
			ShowToast(report.Summary(), ToastKindInfo)
			return
		}
		PlaySuccessSound()
		kind := ToastKindSuccess
		if len(report.Errors) > 0 {
			kind = ToastKindWarning
		}
		ShowToast(report.Summary(), kind)
	case toolsActionCopyIP:
		ip := toolsIPAddress()
		if ip == "" {
			ShowToast("No WiFi address yet — connect to WiFi", ToastKindWarning)
			return
		}
		if err := copyTextToClipboard(ip, "Tools"); err != nil {
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
	case toolsActionGatewayScan:
		powerProbeGatewayScan(config)
	case toolsActionClipboardScan:
		powerProbeClipboardScan(config)
	case toolsActionPing:
		powerProbePing(config)
	case toolsActionTraceroute:
		powerProbeTraceroute(config)
	case toolsActionDNSLookup:
		powerProbeDNS(config)
	case toolsActionWiFiSignal:
		powerProbeWiFiSignal(config)
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
		toolsPanelMove(config, 0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		toolsPanelMove(config, 0, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		toolsPanelMove(config, -1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		toolsPanelMove(config, 1, 0)
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
		toolsPanelMove(config, 0, -1)
	case sdl.K_DOWN:
		toolsPanelMove(config, 0, 1)
	case sdl.K_LEFT:
		toolsPanelMove(config, -1, 0)
	case sdl.K_RIGHT:
		toolsPanelMove(config, 1, 0)
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
	m := toolsPanelMetrics(config, rows)
	g := toolsGridBuild(rows, m)
	padX := m.padX
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	// A little wider than before: the values (addresses, "Files · Scripts ·
	// Sync") are the point of the panel and used to be ellipsised.
	panelW := m.panelW
	panelH := m.headerH + g.bodyH + m.footH + m.infoH
	if maxH := screenHeight - 40; panelH > maxH {
		panelH = maxH
	}
	// A single line of tiles always has to fit, so the panel never shrinks to
	// a header and a footer.
	if minH := m.headerH + m.tileH + m.gap + m.footH; panelH < minH && minH <= screenHeight-40 {
		panelH = minH
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	if panelY < 0 {
		panelY = 0
	}
	toolsPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	// Accent hairline at the top of the panel.
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	// The header is the shared page bar, the same one the Tools page and About
	// use: title well, subtitle, and the status read-out every transfer tool
	// below needs.
	UIPageHeaderDraw(renderer, config, toolsHeader(config, panelX+padX, panelY+12, panelW-2*padX))

	bodyTop := panelY + m.headerH
	bodyViewH := panelH - m.headerH - m.footH - m.infoH
	if bodyViewH < m.tileH {
		bodyViewH = m.tileH
	}
	scrollY := toolsGridEnsureVisible(g, toolsPanel.cursor, bodyViewH)

	// The theme accent can be very dark (several presets use a near-black
	// "info" colour), so reuse the install card's contrast check instead of
	// painting the selected tile with an invisible tint.
	focusColor := installHighlight()

	// The grid scrolls, so only the window that fits is drawn and the cursor
	// scrolls it. Rects of off-screen tiles stay zero, which also keeps a tap
	// from landing on a tile that is not on screen.
	toolsRowRects = make([]sdl.Rect, len(rows))
	renderWithClip(renderer, panelX, bodyTop, panelW, bodyViewH, func(r *sdl.Renderer) {
		for _, line := range g.lines {
			top := line.headingY
			if line.heading == "" {
				top = line.tilesY
			}
			bottom := line.tilesY + line.h
			if top-scrollY >= bodyViewH || bottom-scrollY <= 0 {
				continue
			}
			if line.heading != "" {
				hy := bodyTop + line.headingY - scrollY
				// A heading scrolling out under the top edge is normal, but one
				// clipped by the footer left a slice of label floating above the
				// footer band, so a heading that starts in view has to fit
				// whole; only one already crossing the top edge may be cut.
				if hy < bodyTop || hy+sectionH(m) <= bodyTop+bodyViewH {
					drawToolsSectionLabel(r, config, line.heading, panelX+padX, hy, panelW-2*padX)
				}
			}
			for _, idx := range line.tiles {
				// The grid is laid out in body coordinates; the clip window
				// starts at bodyTop on screen, so shift by it (and the scroll)
				// instead of clipping away half the grid.
				cell := g.rects[idx]
				cell.X += panelX
				cell.Y += bodyTop - scrollY
				if cell.Y+cell.H <= bodyTop || cell.Y >= bodyTop+bodyViewH {
					continue
				}
				// A row that straddles an edge is drawn clipped, the same rule
				// the Apps grid follows: hiding it left a blank band at the top
				// of a scrolled panel, and a card cut by the panel edge reads as
				// the edge itself.
				drawToolsTile(r, config, cell, rows[idx], idx == toolsPanel.cursor)
				toolsRowRects[idx] = cell
			}
		}
	})

	// A scrollbar appears only when the grid is taller than the body, so the
	// user can tell there are more tiles below the fold.
	if maxScroll := g.bodyH - bodyViewH; maxScroll > 0 {
		trackX := panelX + panelW - 9
		trackY := bodyTop
		trackH := bodyViewH
		fillRoundedRect(renderer, trackX, trackY, 3, trackH, 2, WithAlpha(ColorBorder, 120))
		thumbH := trackH * bodyViewH / g.bodyH
		if thumbH < 24 {
			thumbH = 24
		}
		thumbY := trackY + (trackH-thumbH)*scrollY/maxScroll
		fillRoundedRect(renderer, trackX, thumbY, 3, thumbH, 2, WithAlpha(accentColor, 200))
	}

	// Live details while a server is open: the SFTP password is shown here (it
	// is random per run and never written to the config or the log), followed
	// by the warning for whichever share is exposed. Pinned above the footer so
	// scrolling the tiles never pushes it away.
	infoY := panelY + panelH - m.footH - m.infoH + 2
	for _, line := range m.info {
		color := ColorTextSecondary()
		if line.warn {
			color = ColorToastWarn
		}
		renderText(renderer, config, font, ellipsize(font, line.text, panelW-2*padX), color, panelX+padX, infoY)
		infoY += m.infoLineH
	}

	// The focused tile's own state lives in the footer: the value ("Running",
	// the LAN address) with the action chip on the right, then the detail line
	// ("ftp://…") underneath — the parts a row used to squeeze next to the
	// name.
	row := rows[toolsPanel.cursor]
	footY := panelY + panelH - m.footH
	focusRect := sdl.Rect{X: panelX + padX, Y: footY, W: panelW - 2*padX, H: medH + 8}
	chipW := drawToolsHintChip(renderer, config, font, row.hint, focusRect, true, focusColor)
	valueW := panelW - 2*padX - chipW - 16
	if valueW > 0 {
		value := ellipsize(medium, row.value, valueW)
		renderText(renderer, config, medium, value, toolsValueColor(row), panelX+padX, valueY(m, footY))
	}
	if row.detail != "" {
		detailW := panelW - 2*padX
		if detailW > 0 {
			renderText(renderer, config, font, ellipsize(font, row.detail, detailW), ColorTextTertiary(),
				panelX+padX, valueY(m, footY)+medH+2)
		}
	}
	closeText := hintLine(FooterHint{"B", "Close"})
	cw, _, _ := font.SizeUTF8(closeText)
	renderText(renderer, config, font, closeText, ColorTextTertiary(),
		panelX+(panelW-int32(cw))/2, panelY+panelH-smallH-6)
}

// toolsValueColor colours the focused tile's value line: warnings are amber,
// running servers green.
func toolsValueColor(row toolsRow) sdl.Color {
	switch {
	case row.status == toolsStatusWarn:
		return ColorToastWarn
	case (row.action == toolsActionFTP || row.action == toolsActionSSH) && row.status == toolsStatusGood:
		return ColorToastSuccess
	case !row.enabled:
		return ColorTextTertiary()
	}
	return ColorTextPrimary()
}

// sectionH and valueY keep the layout arithmetic in one spot: both bands are
// measured (fonts can be taller than their point size), never constant.
func sectionH(m toolsGridMetrics) int32 { return m.sectionH }

func valueY(m toolsGridMetrics, footY int32) int32 { return footY + 6 }

// toolsSectionGlyph picks the glyph that opens a group heading. Each group
// gets a representative tool instead of a generic bullet, drawn with the same
// shapes as the tiles underneath, so a section is recognisable before its
// small-caps label is read. An unknown group falls back to the Tools glyph.
func toolsSectionGlyph(title string) toolsAction {
	switch strings.ToUpper(strings.TrimSpace(title)) {
	case "FTP & IP":
		return toolsActionFTP
	case "WATCH TOGETHER":
		return toolsActionParty
	case "SYSTEM & UPDATES":
		return toolsActionPatch
	}
	return toolsActionPowerTools
}

// drawToolsSectionLabel paints a group heading through the shared section
// component: the section's own glyph makes it recognisable before the small
// uppercase label is read.
func drawToolsSectionLabel(renderer *sdl.Renderer, config *Config, title string, x, y, w int32) {
	glyph := toolsSectionGlyph(title)
	UISectionLabel(renderer, config, title, x, y, w, func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
		drawToolsIcon(r, box, glyph, tint)
	})
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

// toolsHeader builds the panel's page bar: the same glyph as the Home tile that
// opened the panel, the title, and the address every transfer tool below needs.
// The metrics pass calls it with a zero rect, because only the fonts matter to
// the height.
func toolsHeader(config *Config, x, y, w int32) UIPageHeader {
	sub := "FTP & IP transfers, network probes and device utilities"
	if !IsOnline() {
		sub = "Offline — connect WiFi to transfer files"
	}
	status, statusCol := "Offline", ColorToastWarn
	if ip := toolsIPAddress(); ip != "" {
		status, statusCol = "WiFi · "+ip, ColorTextTertiary()
	}
	return UIPageHeader{
		X: x, Y: y, W: w,
		Title:       "Tools",
		Subtitle:    sub,
		Status:      status,
		StatusColor: statusCol,
		// The same title size the Tools page uses: the two headers are one
		// recipe, and a 4px difference would show up the moment both are on
		// screen in a bug report.
		TitleSize: menuTitleSize,
		Glyph: func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
			drawToolsIcon(r, box, toolsActionPowerTools, tint)
		},
	}
}

// drawToolsTile renders one Tools tile through the shared category-tile
// component, so the overlay and the Tools page draw the same card.
func drawToolsTile(r *sdl.Renderer, config *Config, cell sdl.Rect, row toolsRow, focused bool) {
	tile := UICategoryTile{
		Rect:     cell,
		Label:    row.label,
		Surface:  UITileRaised,
		Focused:  focused,
		Disabled: !row.enabled,
	}
	if row.status != toolsStatusNeutral {
		tile.Status = toolsStatusColor(row.status)
	}
	action := row.action
	tile.Glyph = func(renderer *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
		drawToolsIcon(renderer, box, action, tint)
	}
	UICategoryTileDraw(r, config, tile)
}

// drawToolsIcon paints the tile's glyph for one tool, built from the same
// filled shapes and strokes as the home icons so the two sets read with one
// visual weight. The glyph uses the tile's own tint, like the Apps grid's
// initial does.
func drawToolsIcon(r *sdl.Renderer, box sdl.Rect, action toolsAction, color sdl.Color) {
	_ = r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	w, h := box.W, box.H
	cx, cy := box.X+w/2, box.Y+h/2

	switch action {
	case toolsActionFTP:
		// Folder: a filled body with a tab, reading as "share this folder".
		body := sdl.Rect{X: box.X + w/8, Y: box.Y + h/3, W: w - w/4, H: h / 2}
		fillRoundedRect(r, body.X, body.Y, body.W, body.H, 4, color)
		tab := sdl.Rect{X: box.X + w/8, Y: box.Y + h/6, W: w/2 - w/8, H: h / 5}
		fillRoundedRect(r, tab.X, tab.Y, tab.W, tab.H, 2, color)
	case toolsActionSSH, toolsActionUtilities:
		// Terminal: a rounded frame holding a prompt.
		strokeRoundedRect(r, box.X, box.Y, w, h, 6, 2, color)
		ery := cy + h/6
		r.DrawLine(cx-w/4, cy+h/8, cx-w/10, ery)
		r.DrawLine(cx-w/10, ery, cx-w/4, cy+h*3/8)
		fillRoundedRect(r, cx-w/20, ery-1, w*3/20, 3, 1, color)
	case toolsActionCopyIP:
		// LAN: the device linked to two peers, so the glyph says "the address
		// other machines reach".
		fillCircle(r, cx, cy, 5, color)
		fillCircle(r, cx-w/3, box.Y+h/4, 3, color)
		fillCircle(r, cx+w/3, box.Y+h/4, 3, color)
		fillCircle(r, cx-w/3, box.Y+h*3/4, 3, color)
		fillCircle(r, cx+w/3, box.Y+h*3/4, 3, color)
		r.DrawLine(cx-3, cy-3, cx-w/3+3, box.Y+h/4+3)
		r.DrawLine(cx+3, cy-3, cx+w/3-3, box.Y+h/4+3)
		r.DrawLine(cx-3, cy+3, cx-w/3+3, box.Y+h*3/4-3)
		r.DrawLine(cx+3, cy+3, cx+w/3-3, box.Y+h*3/4-3)
	case toolsActionGatewayScan, toolsActionClipboardScan:
		// Port scan: crosshair inside a frame.
		strokeRoundedRect(r, box.X, box.Y, w, h, 5, 2, color)
		strokeCircle(r, cx, cy, w*17/100, color)
		r.DrawLine(cx, box.Y+3, cx, cy-w*17/100-2)
		r.DrawLine(cx, cy+w*17/100+2, cx, box.Y+h-3)
		r.DrawLine(box.X+3, cy, cx-w*17/100-2, cy)
		r.DrawLine(cx+w*17/100+2, cy, box.X+w-3, cy)
	case toolsActionPing:
		// Ping: a dot with echo arcs above it.
		fillCircle(r, cx, box.Y+h*3/4, 4, color)
		for _, rad := range []int32{h / 4, h * 2 / 5} {
			arc(r, cx, box.Y+h*3/4, rad, 215, 110, color)
		}
	case toolsActionTraceroute:
		// Traceroute: hops along a path.
		fillCircle(r, box.X+w/5, box.Y+h*3/4, 4, color)
		fillCircle(r, cx, box.Y+h/2, 3, color)
		fillCircle(r, box.X+w*4/5, box.Y+h/4, 4, color)
		r.DrawLine(box.X+w/5+4, box.Y+h*3/4-3, cx-3, cy)
		r.DrawLine(cx+3, cy-2, box.X+w*4/5-4, box.Y+h/4+3)
	case toolsActionDNSLookup:
		// DNS: the globe also used for the browser.
		DrawHomeIcon(r, "browser", box, false)
	case toolsActionWiFiSignal:
		// Wi-Fi signal: a dot with two widening arcs.
		fillCircle(r, cx, box.Y+h*3/4, 4, color)
		arc(r, cx, box.Y+h*3/4, h/3, 200, 140, color)
		arc(r, cx, box.Y+h*3/4, h*13/20, 200, 140, color)
	case toolsActionCompanion:
		// Phone: a rounded body with a home dot.
		strokeRoundedRect(r, box.X+w/4, box.Y+h/8, w/2, h*3/4, 4, 2, color)
		fillCircle(r, cx, box.Y+h*3/4, 2, color)
	case toolsActionParty:
		// Watch Party: two people side by side.
		fillCircle(r, cx-w/6, box.Y+h/3, h/7, color)
		fillCircle(r, cx+w/6, box.Y+h/3, h/7, color)
		fillRoundedRect(r, cx-w/3, box.Y+h/2, w/3, h/4, 3, color)
		fillRoundedRect(r, cx, box.Y+h/2, w/3, h/4, 3, color)
	case toolsActionPatch:
		// Updates: a rising arrow under a shelf.
		fillTriangleFilled(r,
			pt{x: cx, y: box.Y + h/6},
			pt{x: cx - w/6, y: box.Y + h/2},
			pt{x: cx + w/6, y: box.Y + h/2}, color)
		fillRoundedRect(r, cx-2, box.Y+h/2, 4, h*3/8, 2, color)
		fillRoundedRect(r, box.X+w/8, box.Y+h*5/6, w*3/4, 2, 1, color)
	case toolsActionPowerTools:
		// Reuse the home "tools" glyph: slider tracks with knobs.
		DrawHomeIcon(r, "tools", box, false)
	case toolsActionDiscHelpers:
		// Disc: a ring with a hub.
		strokeCircle(r, cx, cy, w*9/20, color)
		strokeCircle(r, cx, cy, w/8, color)
		fillCircle(r, cx, cy, w/20, color)
	case toolsActionCalculator:
		// Calculator: a rounded body with a key grid.
		strokeRoundedRect(r, box.X+w/5, box.Y+h/8, w*3/5, h*3/4, 4, 2, color)
		cell := h / 10
		for row := int32(0); row < 2; row++ {
			for col := int32(0); col < 2; col++ {
				fillRoundedRect(r, cx-w/7+col*(cell*3), cy-2+row*(cell*3), cell, cell, 1, color)
			}
		}
		fillRoundedRect(r, box.X+w*7/20, box.Y+h/4, w*3/10, h/10, 1, color)
	case toolsActionCommunity:
		// Speech bubble, matching the home "chat" glyph.
		DrawHomeIcon(r, "chat", box, false)
	default:
		// Unknown action draws the safe ring fallback rather than an empty
		// well: the tile must never look broken.
		fillCircle(r, cx, cy, w/3, color)
		fillCircle(r, cx, cy, w/6, ColorSurfaceRaised)
	}
}

// arc strokes an arc from angle a0 spanning a1 degrees, in short segments, so
// wifi-style glyphs stay smooth without adding an arc primitive per size.
func arc(r *sdl.Renderer, cx, cy, radius int32, a0, a1 float64, color sdl.Color) {
	if radius <= 0 {
		return
	}
	steps := 18
	rx := float64(radius)
	px := int32(float64(cx) + rx*math.Cos(a0*math.Pi/180))
	py := int32(float64(cy) + rx*math.Sin(a0*math.Pi/180))
	r.SetDrawColor(color.R, color.G, color.B, color.A)
	for i := 1; i <= steps; i++ {
		a := a0 + a1*float64(i)/float64(steps)
		x := int32(float64(cx) + rx*math.Cos(a*math.Pi/180))
		y := int32(float64(cy) + rx*math.Sin(a*math.Pi/180))
		r.DrawLine(px, py, x, y)
		px, py = x, y
	}
}
