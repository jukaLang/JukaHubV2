package main

// smart_home_ui.go — the Smart Home overlay.
//
// The panel is a list, not a dashboard: Home Assistant entities first (each one
// a switch), then the self-hosted services in the order they are useful on a
// handheld — Pi-hole, router, Tailscale, WireGuard — and finally Refresh and
// Close. Rows are rebuilt from smartHomeSnapshot() every frame, so the panel
// never keeps its own copy of the state; the only mutable UI state is the
// cursor and the scroll offset.
//
// Nothing here touches the network. Toggles call into smart_home.go, which
// returns as soon as the request has been answered, and a refresh runs on its
// own goroutine. That is what keeps the list responsive on a device whose Wi-Fi
// drops every time the screen turns off.

import (
	"fmt"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

type smartHomeRowKind int

const (
	smartHomeRowEntity smartHomeRowKind = iota
	smartHomeRowPihole
	smartHomeRowRouter
	smartHomeRowTailscale
	smartHomeRowWireGuard
	smartHomeRowRefresh
	smartHomeRowClose
)

type smartHomeRow struct {
	label    string
	value    string
	detail   string
	kind     smartHomeRowKind
	entityID string
	enabled  bool
	on       bool
}

var smartHomePanel struct {
	open   bool
	cursor int
	scroll int
	enter  uiEnter
	rects  []sdl.Rect
}

// smartHomePanelRect is the card itself, used to tell a click inside the panel
// (on a gap between rows) from a click outside it (which closes).
var smartHomePanelRect sdl.Rect

// smartHomeRowValueColour is the colour of a row's value: green for "on", muted
// for "off", and the warning colour when the row is really an error line.
func smartHomeRowValueColour(row smartHomeRow) sdl.Color {
	switch {
	case !row.enabled:
		return ColorTextTertiary()
	case row.kind == smartHomeRowEntity || row.kind == smartHomeRowTailscale:
		if row.on {
			return ColorToastSuccess
		}
		return ColorTextSecondary()
	}
	return ColorTextPrimary()
}

// smartHomePanelRows builds the list for one frame. It is a pure function of the
// config and the snapshot, which is what makes the panel testable and keeps the
// scroll position meaningful: the same snapshot always yields the same rows in
// the same order.
func smartHomePanelRows(config *Config, snap smartHomeSnapshotData) []smartHomeRow {
	settings := loadSmartHomeSettings(config)
	rows := make([]smartHomeRow, 0, len(snap.entities)+6)

	for _, e := range snap.entities {
		on := haEntityOn(e.State)
		value := "off"
		if on {
			value = "on"
		}
		rows = append(rows, smartHomeRow{
			label:    e.Name,
			value:    value,
			detail:   e.EntityID,
			kind:     smartHomeRowEntity,
			entityID: e.EntityID,
			enabled:  true,
			on:       on,
		})
	}
	if snap.haErr != "" && len(snap.entities) == 0 {
		rows = append(rows, smartHomeRow{
			label:  "Home Assistant",
			value:  snap.haErr,
			detail: "ha_url and ha_token in jukauser.json",
			kind:   smartHomeRowEntity,
		})
	}

	// Pi-hole.
	pi := smartHomeRow{kind: smartHomeRowPihole, label: "Pi-hole", value: "set pihole_url"}
	switch {
	case !settings.hasPiHole():
		pi.detail = "Add pihole_url (and pihole_token) to jukauser.json"
	case snap.pihole.Reachable:
		pi.enabled = true
		pi.value = piholeLine(snap.pihole)
		pi.detail = "Queries blocked today on this network"
	default:
		pi.detail = "Pi-hole is configured but did not answer"
		if snap.pihole.Err != "" {
			pi.value = snap.pihole.Err
		}
	}
	rows = append(rows, pi)

	// Router.
	router := smartHomeRow{kind: smartHomeRowRouter, label: "Router", value: "no gateway"}
	if snap.router.Host != "" {
		router.enabled = snap.router.Reachable
		router.value = routerLine(snap.router)
		router.detail = "TCP handshake to the gateway's web port"
	} else {
		router.detail = "Set router_host when the default gateway is not the router"
	}
	rows = append(rows, router)

	// Tailscale: a real toggle when the CLI is installed, otherwise an honest
	// "not installed" line rather than a switch that cannot do anything.
	ts := smartHomeRow{kind: smartHomeRowTailscale, label: "Tailscale"}
	switch {
	case snap.tailscale.Installed:
		ts.enabled = true
		ts.on = snap.tailscale.Running
		state := snap.tailscale.State
		if state == "" {
			state = "unknown"
		}
		if snap.tailscale.Running {
			ts.value = "connected"
			if len(snap.tailscale.IPs) > 0 {
				ts.detail = state + " · " + strings.Join(snap.tailscale.IPs, ", ")
			} else {
				ts.detail = state
			}
		} else {
			ts.value = strings.ToLower(state)
			ts.detail = "A toggles tailscale up / down"
		}
	case !settings.TSEnabled:
		ts.value = "not installed"
		ts.detail = "tailscale_toggle in jukauser.json shows this row on a device without the CLI"
	default:
		ts.value = "not installed"
		ts.detail = "Install Tailscale to control the tunnel from here"
	}
	rows = append(rows, ts)

	// WireGuard is status only: bringing an interface up needs root, and a
	// switch that always fails is worse than a line that explains the rule.
	wg := smartHomeRow{
		kind:    smartHomeRowWireGuard,
		label:   "WireGuard",
		value:   wireguardLine(snap.wireguard),
		detail:  "Status only — wg-quick needs root",
		enabled: len(snap.wireguard.Interfaces) > 0,
		on:      len(snap.wireguard.Interfaces) > 0,
	}
	if snap.wireguard.Err != "" {
		wg.detail = snap.wireguard.Err
	} else if !snap.wireguard.Installed {
		wg.detail = "Install wg (wireguard-tools) to see tunnels"
	}
	rows = append(rows, wg)

	refresh := smartHomeRow{kind: smartHomeRowRefresh, label: "Refresh", value: "R", enabled: true}
	if settings.hasMQTT() {
		refresh.detail = "Toggles are mirrored to " + settings.MQTTPFX + "/<entity_id>"
	} else {
		refresh.detail = "Add mqtt_host to mirror toggles to a broker"
	}
	rows = append(rows, refresh)
	rows = append(rows, smartHomeRow{kind: smartHomeRowClose, label: "Close", value: "B", enabled: true})

	return rows
}

// openSmartHomePanel shows the panel, replacing any other modal, and kicks off
// one refresh so the list is populated before the first frame settles.
func openSmartHomePanel(config *Config) {
	qs.open = false
	toolsPanel.open = false
	smartHomePanel.open = true
	smartHomePanel.cursor = 0
	smartHomePanel.scroll = 0
	smartHomePanel.rects = nil
	smartHomePanelRect = sdl.Rect{}
	smartHomePanel.enter.begin(uiClockMS(), 180)
	refreshSmartHome(config)
}

// closeSmartHomePanel hides the panel. PlayBackSound is only played for a real
// close, so a click outside an already-closed panel stays silent.
func closeSmartHomePanel() {
	if !smartHomePanel.open {
		return
	}
	smartHomePanel.open = false
	smartHomePanel.enter.cancel()
	PlayBackSound()
}

// smartHomePanelRowHeight is the height of one row, from the medium font.
func smartHomePanelRowHeight(mediumH int32) int32 { return 10 + mediumH + 10 }

// smartHomePanelMaxRows is how many rows fit in the list area with these fonts.
func smartHomePanelMaxRows(mediumH, smallH int32) int {
	rowH := smartHomePanelRowHeight(mediumH)
	headerH := 18 + mediumH + 4 + smallH + 14
	detailH := 10 + smallH + 10
	footerH := 20 + smallH
	avail := screenHeight - 40 - headerH - detailH - footerH
	n := int(avail / rowH)
	if n < 3 {
		n = 3
	}
	return n
}

// smartHomePanelMove moves the cursor and keeps it inside the list.
func smartHomePanelMove(config *Config, delta int) {
	rows := smartHomePanelRows(config, smartHomeSnapshot())
	if len(rows) == 0 {
		smartHomePanel.cursor = 0
		return
	}
	next := smartHomePanel.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= len(rows) {
		next = len(rows) - 1
	}
	if next != smartHomePanel.cursor {
		smartHomePanel.cursor = next
		PlayNavSound()
	}
}

// smartHomePanelActivate runs the focused row.
func smartHomePanelActivate(config *Config) {
	rows := smartHomePanelRows(config, smartHomeSnapshot())
	if len(rows) == 0 {
		return
	}
	if smartHomePanel.cursor < 0 {
		smartHomePanel.cursor = 0
	}
	if smartHomePanel.cursor >= len(rows) {
		smartHomePanel.cursor = len(rows) - 1
	}
	row := rows[smartHomePanel.cursor]
	if !row.enabled && row.kind != smartHomeRowClose {
		// Dimmed rows say why they cannot act instead of doing nothing.
		if row.detail != "" {
			showToast(row.detail, ToastWarn())
		}
		return
	}

	switch row.kind {
	case smartHomeRowEntity:
		if row.entityID == "" {
			return
		}
		if err := toggleSmartHomeEntity(config, row.entityID); err != nil {
			showToast("Smart home: "+err.Error(), ToastWarn())
			return
		}
		PlayActivateSound()
	case smartHomeRowTailscale:
		if err := toggleTailscale(config); err != nil {
			showToast("Tailscale: "+firstLine(err.Error()), ToastWarn())
			return
		}
		PlayActivateSound()
	case smartHomeRowRefresh:
		refreshSmartHome(config)
		PlayNavSound()
	case smartHomeRowClose:
		closeSmartHomePanel()
	case smartHomeRowPihole, smartHomeRowRouter, smartHomeRowWireGuard:
		// Status rows: A re-reads them, which is the most useful thing it can
		// do on a row that has no other action.
		refreshSmartHome(config)
		PlayNavSound()
	}
}

// handleSmartHomeKey consumes keyboard input while the panel is open.
func handleSmartHomeKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !smartHomePanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		smartHomePanelMove(config, -1)
	case sdl.K_DOWN:
		smartHomePanelMove(config, 1)
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		smartHomePanelActivate(config)
	case sdl.K_r:
		refreshSmartHome(config)
		PlayNavSound()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeSmartHomePanel()
	}
	return true
}

// ctrlSmartHomePanel is called before the panel's scene sees a button, so the
// D-pad drives the list even when the scene underneath has its own bindings.
func ctrlSmartHomePanel(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || !smartHomePanel.open {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		smartHomePanelMove(config, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		smartHomePanelMove(config, 1)
	case sdl.CONTROLLER_BUTTON_A:
		smartHomePanelActivate(config)
	case sdl.CONTROLLER_BUTTON_X:
		refreshSmartHome(config)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeSmartHomePanel()
	}
	return true
}

// handleSmartHomeMouse activates a clicked row, or closes the panel when the
// click lands outside it. Returns true when the click was consumed.
func handleSmartHomeMouse(mx, my int32, config *Config) bool {
	if !smartHomePanel.open {
		return false
	}
	for i, r := range smartHomePanel.rects {
		if r.W > 0 && pointInRect(mx, my, r) {
			smartHomePanel.cursor = i
			smartHomePanelActivate(config)
			return true
		}
	}
	if smartHomePanelRect.W > 0 && !pointInRect(mx, my, smartHomePanelRect) {
		closeSmartHomePanel()
	}
	return true
}

// smartHomePanelSubtitle is the one line under the title: what is configured,
// or what to add when nothing is.
func smartHomePanelSubtitle(config *Config, snap smartHomeSnapshotData) string {
	settings := loadSmartHomeSettings(config)
	parts := make([]string, 0, 4)
	if settings.hasHA() {
		parts = append(parts, fmt.Sprintf("%d devices", len(snap.entities)))
	}
	if settings.hasPiHole() {
		parts = append(parts, "Pi-hole")
	}
	if snap.tailscale.Installed {
		parts = append(parts, "Tailscale")
	}
	if snap.wireguard.Installed {
		parts = append(parts, "WireGuard")
	}
	if len(parts) == 0 {
		return "Nothing configured — add ha_url, pihole_url or mqtt_host to jukauser.json"
	}
	line := strings.Join(parts, " · ")
	if snap.loading {
		line += " · refreshing"
	} else if !snap.updatedAt.IsZero() {
		line += " · " + snap.updatedAt.Format("15:04")
	}
	return line
}

// renderSmartHomePanel draws the modal over the current scene.
func renderSmartHomePanel(renderer *sdl.Renderer, config *Config) {
	if !smartHomePanel.open || renderer == nil {
		return
	}
	snap := smartHomeSnapshot()
	rows := smartHomePanelRows(config, snap)
	if len(rows) == 0 {
		return
	}
	if smartHomePanel.cursor >= len(rows) {
		smartHomePanel.cursor = len(rows) - 1
	}
	if smartHomePanel.cursor < 0 {
		smartHomePanel.cursor = 0
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
	rowH := smartHomePanelRowHeight(medH)
	headerH := 18 + medH + 4 + smallH + 14
	detailH := 10 + smallH + 10
	footerH := 20 + smallH

	panelW := int32(820)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	maxRows := smartHomePanelMaxRows(medH, smallH)
	visible := len(rows)
	if visible > maxRows {
		visible = maxRows
	}
	panelH := headerH + int32(visible)*rowH + detailH + footerH
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	smartHomePanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	// Keep the cursor on screen: the scroll follows it in both directions.
	if smartHomePanel.cursor < smartHomePanel.scroll {
		smartHomePanel.scroll = smartHomePanel.cursor
	}
	if smartHomePanel.cursor >= smartHomePanel.scroll+visible {
		smartHomePanel.scroll = smartHomePanel.cursor - visible + 1
	}
	if smartHomePanel.scroll > len(rows)-visible {
		smartHomePanel.scroll = len(rows) - visible
	}
	if smartHomePanel.scroll < 0 {
		smartHomePanel.scroll = 0
	}

	enter := smartHomePanel.enter.progress(uiClockMS())

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, uint8(180*enter))
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	renderText(renderer, config, medium, "Smart Home", ColorTextPrimary(), panelX+padX, panelY+18)
	renderText(renderer, config, font, ellipsize(font, smartHomePanelSubtitle(config, snap), panelW-2*padX),
		ColorTextTertiary(), panelX+padX, panelY+18+medH+4)

	focusColor := installHighlight()
	rowW := panelW - 2*padX
	smartHomePanel.rects = smartHomePanel.rects[:0]
	if cap(smartHomePanel.rects) < len(rows) {
		smartHomePanel.rects = make([]sdl.Rect, 0, len(rows))
	}

	for i := 0; i < visible; i++ {
		idx := smartHomePanel.scroll + i
		row := rows[idx]
		y := panelY + headerH + int32(i)*rowH
		r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 4}
		smartHomePanel.rects = append(smartHomePanel.rects, r)

		focused := idx == smartHomePanel.cursor
		rowBg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			rowBg = WithAlpha(focusColor, 46)
		}
		if !row.enabled {
			rowBg = WithAlpha(ColorSurfaceRow, 120)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}

		labelCol := ColorTextPrimary()
		if !row.enabled {
			labelCol = ColorTextTertiary()
		}
		// The value column is measured first so a long entity name is ellipsized
		// against the space that is actually left, not against the whole row.
		value := row.value
		valueW := int32(0)
		if value != "" {
			vw, _, _ := font.SizeUTF8(value)
			valueW = int32(vw)
			if valueW > r.W/2-16 {
				value = ellipsize(font, value, r.W/2-16)
				vw2, _, _ := font.SizeUTF8(value)
				valueW = int32(vw2)
			}
			renderText(renderer, config, font, value, smartHomeRowValueColour(row),
				r.X+r.W-valueW-16, r.Y+10+(medH-smallH)/2)
		}

		labelW := r.W - 32 - valueW - 16
		if labelW < 80 {
			labelW = 80
		}
		label := ellipsize(medium, row.label, labelW)
		var labelColour sdl.Color
		if row.kind == smartHomeRowEntity && row.enabled {
			labelColour = ColorTextPrimary()
		} else {
			labelColour = labelCol
		}
		renderText(renderer, config, medium, label, labelColour, r.X+16, r.Y+10)

		// A switch is drawn for the two rows that really toggle something; the
		// other rows keep the value column only.
		if row.kind == smartHomeRowEntity || row.kind == smartHomeRowTailscale {
			if row.enabled {
				swW := int32(40)
				swH := int32(22)
				swX := r.X + r.W - swW - 14 - valueW - 12
				swY := r.Y + (r.H-swH)/2
				fill := WithAlpha(ColorSurfaceRow, 220)
				if row.on {
					fill = WithAlpha(ColorToastSuccess, 220)
				}
				fillRoundedRect(renderer, swX, swY, swW, swH, swH/2, fill)
				knobR := int32(7)
				knobX := swX + knobR + 4
				if row.on {
					knobX = swX + swW - knobR - 4
				}
				fillCircle(renderer, knobX, swY+swH/2, knobR, sdl.Color{R: 250, G: 252, B: 255, A: 240})
			}
		}
	}

	// One explanation for the focused row, under the list, so a row can carry a
	// reason without every row paying for a second text line.
	detail := rows[smartHomePanel.cursor].detail
	if detail == "" {
		detail = "A toggles the focused row."
	}
	detailY := panelY + headerH + int32(visible)*rowH + 10
	renderText(renderer, config, font, ellipsize(font, detail, rowW), ColorTextTertiary(), panelX+padX, detailY)

	hints := []FooterHint{{"A", "Toggle"}, {"X / R", "Refresh"}, {"B", "Close"}}
	if len(rows) > visible {
		hints = append([]FooterHint{{Label: fmt.Sprintf("%d/%d", smartHomePanel.cursor+1, len(rows))}}, hints...)
	}
	footer := hintLine(hints...)
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextSecondary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-footerH+8)
}
