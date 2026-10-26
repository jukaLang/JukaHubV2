package main

// wifi_ui.go — the Wi-Fi network picker.
//
// The panel is the first-party replacement for the firmware's settings screen:
// scan, join (passphrase typed on the on-screen keyboard), reconnect a saved
// network without retyping its password, disconnect, and forget. It is modal
// and controller-first, matching the rest of the app's overlays.
//
// Everything that touches wpa_cli runs on a worker goroutine, exactly like the
// Quick Settings probe: a wedged supplicant must not freeze the render thread.
// Results are handed back through a one-slot channel that the render loop
// drains, so the panel's state is only ever written from the event thread.

import (
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// wifiForgetConfirmWindow is how long the first SELECT press stays armed.
	wifiForgetConfirmWindow = 5 * time.Second
	// wifiPanelMaxRows keeps the list readable on a 720p screen.
	wifiPanelMaxRows = 9
	// wifiAutoRefresh re-reads status while the panel is open, so a drop or a
	// connection made elsewhere is reflected without a manual rescan. It never
	// triggers a scan: that would cost battery for no extra information.
	wifiAutoRefresh = 15 * time.Second
)

type wifiPanelState struct {
	open      bool
	cursor    int
	networks  []wifiNetwork
	saved     []savedNetwork
	status    wifiStatus
	radioOn   bool
	radioOK   bool
	available bool
	detail    string
	err       string
	loadedAt  time.Time

	busy      bool
	busyLabel string

	confirmForget string
	confirmAt     time.Time
}

var wifiPanel wifiPanelState

// wifiRowRects maps a tap back to a row; only touched on the event thread.
var wifiRowRects []sdl.Rect

var wifiPanelRect sdl.Rect

// wifiActionID is one tappable row of action pills at the bottom of the panel.
// The picker is reachable by touch from the status bar, so every action the pad
// can perform (rescan, reconnect, disconnect, forget, close) also needs a
// button: the footer hints alone are not usable with a finger.
type wifiActionID int

const (
	wifiActionRescan wifiActionID = iota
	wifiActionReconnect
	wifiActionDisconnect
	wifiActionForget
	wifiActionClose
)

type wifiActionHit struct {
	id   wifiActionID
	rect sdl.Rect
}

// wifiActionRects is rebuilt on every frame, like wifiRowRects.
var wifiActionRects []wifiActionHit

// wifiPendingSSID is the network whose password the keyboard is typing.
var wifiPendingSSID string

// ── Worker results ─────────────────────────────────────────────────────────

type wifiUpdateKind int

const (
	wifiUpdateState wifiUpdateKind = iota
	wifiUpdateJoin
)

type wifiUpdate struct {
	kind wifiUpdateKind
	gen  uint64
	snap *wifiSnapshot
	err  string
	msg  string
}

var (
	wifiUpdates     = make(chan wifiUpdate, 1)
	wifiWorkPending bool
	// wifiGeneration invalidates results from a previous visit to the panel
	// (or from a scan superseded by a newer one). A stale worker must not
	// overwrite the list the user is looking at.
	wifiGeneration uint64
)

// wifiStartWork runs fn off the event thread and marks the panel busy. A
// second request while one is in flight is ignored: scanning twice at once
// just makes both slower.
func wifiStartWork(label string, fn func() wifiUpdate) {
	if wifiWorkPending {
		return
	}
	wifiWorkPending = true
	wifiPanel.busy = true
	wifiPanel.busyLabel = label
	gen := wifiGeneration
	go func() {
		u := fn()
		u.gen = gen
		// Block rather than drop: a dropped result would leave the panel busy
		// forever (the flag is only cleared by the drain), and a newer worker
		// would silently lose its result to a stale one already in the slot.
		// The render loop drains once per frame, and closing the panel drains
		// the slot too, so the wait is bounded.
		wifiUpdates <- u
	}()
}

// wifiDrainUpdates applies finished work. Called from the render loop.
func wifiDrainUpdates() {
	for {
		select {
		case u := <-wifiUpdates:
			if u.gen != wifiGeneration {
				// A worker from a closed or superseded visit: discard it without
				// touching the busy flag owned by the current one.
				continue
			}
			wifiWorkPending = false
			wifiPanel.busy = false
			wifiPanel.busyLabel = ""
			if u.snap != nil {
				wifiApplySnapshot(*u.snap)
			}
			if u.err != "" {
				wifiPanel.err = u.err
				// Back off the auto-refresh: retrying a missing wpa_cli every
				// frame would fork a process per frame.
				wifiPanel.loadedAt = time.Now()
				if u.kind == wifiUpdateJoin {
					ShowToast(u.err, ToastKindError)
				}
			}
			if u.msg != "" {
				kind := ToastKindSuccess
				if strings.Contains(u.msg, "no IP") || strings.Contains(u.msg, "could not be saved") {
					kind = ToastKindWarning
				}
				ShowToast(u.msg, kind)
			}
		default:
			return
		}
	}
}

// wifiApplySnapshot replaces the list while keeping the cursor on the same
// network, so a background refresh never moves the selection under the user.
func wifiApplySnapshot(snap wifiSnapshot) {
	prevSSID := ""
	if wifiPanel.cursor >= 0 && wifiPanel.cursor < len(wifiPanel.networks) {
		prevSSID = wifiPanel.networks[wifiPanel.cursor].SSID
	}
	wifiPanel.status = snap.Status
	wifiPanel.saved = snap.Saved
	wifiPanel.networks = snap.Networks
	wifiPanel.radioOn = snap.RadioOn
	wifiPanel.radioOK = snap.RadioOK
	wifiPanel.err = ""
	wifiPanel.loadedAt = time.Now()
	wifiPanel.cursor = 0
	for i := range snap.Networks {
		if prevSSID != "" && snap.Networks[i].SSID == prevSSID {
			wifiPanel.cursor = i
			return
		}
	}
	if prevSSID == "" {
		for i := range snap.Networks {
			if snap.Networks[i].Active {
				wifiPanel.cursor = i
				break
			}
		}
	}
}

// ── Opening / closing ──────────────────────────────────────────────────────

// openWiFiPanel shows the picker and starts a scan. Other modals are closed so
// two lists never stack on each other.
func openWiFiPanel() {
	qs.open = false
	processPanel.open = false
	toolsPanel.open = false
	wifiRowRects = nil
	wifiActionRects = nil
	wifiPanel = wifiPanelState{open: true}
	// Anything still in flight belongs to the previous visit.
	wifiGeneration++
	wifiWorkPending = false

	_, detail := wifiSupport()
	wifiPanel.available = detail == ""
	wifiPanel.detail = detail
	if !wifiPanel.available {
		wifiPanel.err = detail
		PlayBackSound()
		return
	}
	PlayActivateSound()
	// Discard anything a previous visit left behind before asking for fresh
	// state, so an old list cannot appear as the result of this scan.
	wifiDrainDiscarded()
	wifiRefreshAsync(true)
}

// wifiDrainDiscarded empties the result channel without applying it.
func wifiDrainDiscarded() {
	for {
		select {
		case <-wifiUpdates:
			wifiWorkPending = false
		default:
			return
		}
	}
}

// wifiSupport reports whether the backend exists and, if not, why.
func wifiSupport() (bool, string) {
	if wifiControlAvailable() {
		return true, ""
	}
	return false, wifiUnavailableReason()
}

func closeWiFiPanel() {
	wifiPanel.open = false
	wifiRowRects = nil
	wifiActionRects = nil
	wifiPanel.confirmForget = ""
	wifiPendingSSID = ""
	wifiPassphraseBuffer = ""
	// Invalidate in-flight work: its toast must not appear over the scene the
	// user returned to.
	wifiGeneration++
	wifiDrainDiscarded()
	PlayBackSound()
}

// wifiRefreshAsync reloads the list. scan also asks the supplicant for a new
// scan, which is the slow path.
func wifiRefreshAsync(scan bool) {
	if !wifiPanel.available {
		return
	}
	label := "Refreshing…"
	if scan {
		label = "Scanning for networks…"
	}
	wifiStartWork(label, func() wifiUpdate {
		if scan {
			if _, err := wifiScanNetworks(); err != nil {
				return wifiUpdate{err: firstLine(err.Error())}
			}
		}
		snap, err := wifiReadSnapshot()
		if err != nil {
			return wifiUpdate{err: firstLine(err.Error())}
		}
		return wifiUpdate{kind: wifiUpdateState, snap: &snap}
	})
}

// ── Navigation and actions ─────────────────────────────────────────────────

func wifiMove(delta int) {
	if len(wifiPanel.networks) == 0 {
		return
	}
	wifiPanel.cursor += delta
	if wifiPanel.cursor < 0 {
		wifiPanel.cursor = 0
	}
	if wifiPanel.cursor >= len(wifiPanel.networks) {
		wifiPanel.cursor = len(wifiPanel.networks) - 1
	}
	wifiPanel.confirmForget = ""
	PlayNavSound()
}

func wifiSelected() *wifiNetwork {
	if wifiPanel.cursor < 0 || wifiPanel.cursor >= len(wifiPanel.networks) {
		return nil
	}
	return &wifiPanel.networks[wifiPanel.cursor]
}

// wifiActivate joins the selected network. A saved network reconnects with the
// key the supplicant already holds; a secured new one asks for its password.
func wifiActivate() {
	if wifiPanel.busy {
		ShowToast("Still "+strings.ToLower(strings.TrimSuffix(wifiPanel.busyLabel, "…")), ToastKindInfo)
		return
	}
	if !wifiPanel.available {
		ShowToast(wifiPanel.detail, ToastKindWarning)
		return
	}
	n := wifiSelected()
	if n == nil {
		// Empty list: the useful action is turning the radio on or scanning.
		if wifiPanel.radioOK && !wifiPanel.radioOn {
			wifiEnableRadioAsync()
			return
		}
		wifiRefreshAsync(true)
		return
	}
	if n.Active && wifiPanel.status.Connected {
		// Tapping the row that already shows CONNECTED reconnects it. The
		// common case (it says connected but nothing loads) is a stale
		// association, and a touch user has no other way to ask for a retry.
		wifiReconnectAsync(n.SSID)
		return
	}
	if id, ok := wifiSavedID(n.SSID, wifiPanel.saved); ok {
		wifiSelectSavedAsync(id)
		return
	}
	if n.Secured {
		wifiPromptPassphrase(n.SSID)
		return
	}
	wifiJoinAsync(n.SSID, "", false)
}

func wifiPromptPassphrase(ssid string) {
	wifiPendingSSID = ssid
	wifiPassphraseBuffer = ""
	vkOpenWiFi()
}

// wifiSubmitPassphrase is called by the on-screen keyboard.
func wifiSubmitPassphrase(pass string) {
	ssid := wifiPendingSSID
	wifiPendingSSID = ""
	if ssid == "" {
		return
	}
	wifiJoinAsync(ssid, pass, true)
}

// wifiEnableRadioAsync turns the radio on off the event thread: nmcli and
// rfkill can each take seconds on a slow boot, and a frozen panel would look
// like the A press did nothing.
func wifiEnableRadioAsync() {
	if wifiPanel.busy {
		return
	}
	wifiStartWork("Turning Wi-Fi on…", func() wifiUpdate {
		if err := setWifiEnabled(true); err != nil {
			return wifiUpdate{kind: wifiUpdateJoin, err: firstLine(err.Error())}
		}
		// Give the interface a moment to come up before scanning.
		time.Sleep(600 * time.Millisecond)
		if _, err := wifiScanNetworks(); err != nil {
			return wifiUpdate{kind: wifiUpdateState, err: firstLine(err.Error())}
		}
		snap, err := wifiReadSnapshot()
		if err != nil {
			return wifiUpdate{err: firstLine(err.Error())}
		}
		return wifiUpdate{kind: wifiUpdateState, snap: &snap, msg: "Wi-Fi on"}
	})
}

func wifiJoinAsync(ssid, pass string, secured bool) {
	if wifiPanel.busy {
		return
	}
	wifiStartWork("Connecting to "+ssid+"…", func() wifiUpdate {
		res, err := wifiJoin(ssid, pass, secured)
		if err != nil {
			return wifiUpdate{kind: wifiUpdateJoin, err: firstLine(err.Error())}
		}
		u := wifiUpdate{kind: wifiUpdateJoin, msg: wifiJoinMessage(res)}
		if snap, snapErr := wifiReadSnapshot(); snapErr == nil {
			u.snap = &snap
		}
		return u
	})
}

func wifiSelectSavedAsync(id int) {
	if wifiPanel.busy {
		return
	}
	wifiStartWork("Reconnecting…", func() wifiUpdate {
		res, err := wifiSelectSaved(id)
		if err != nil {
			return wifiUpdate{kind: wifiUpdateJoin, err: firstLine(err.Error())}
		}
		u := wifiUpdate{kind: wifiUpdateJoin, msg: wifiJoinMessage(res)}
		if snap, snapErr := wifiReadSnapshot(); snapErr == nil {
			u.snap = &snap
		}
		return u
	})
}

// wifiJoinMessage is the one-line confirmation after a successful join.
func wifiJoinMessage(res wifiJoinResult) string {
	msg := "Connected to " + res.SSID
	if res.IP != "" {
		msg += " · " + res.IP
	}
	if res.Warn != "" {
		msg += " · " + res.Warn
	}
	return msg
}

// wifiReconnectSelected re-establishes the current connection with the saved
// key. It backs the Reconnect pill and the reconnect on the active row.
func wifiReconnectSelected() {
	if wifiPanel.busy || !wifiPanel.available {
		return
	}
	if !wifiPanel.status.Connected || wifiPanel.status.SSID == "" {
		ShowToast("Not connected to a network", ToastKindInfo)
		return
	}
	wifiReconnectAsync(wifiPanel.status.SSID)
}

func wifiReconnectAsync(ssid string) {
	if wifiPanel.busy {
		return
	}
	wifiStartWork("Reconnecting to "+ssid+"…", func() wifiUpdate {
		res, err := wifiReconnect()
		if err != nil {
			return wifiUpdate{kind: wifiUpdateJoin, err: firstLine(err.Error())}
		}
		u := wifiUpdate{kind: wifiUpdateJoin, msg: wifiJoinMessage(res)}
		if snap, snapErr := wifiReadSnapshot(); snapErr == nil {
			u.snap = &snap
		}
		return u
	})
}

func wifiDisconnectSelected() {
	if wifiPanel.busy || !wifiPanel.available {
		return
	}
	if !wifiPanel.status.Connected {
		ShowToast("Not connected to a network", ToastKindInfo)
		return
	}
	wifiStartWork("Disconnecting…", func() wifiUpdate {
		if err := wifiDisconnect(); err != nil {
			return wifiUpdate{kind: wifiUpdateJoin, err: firstLine(err.Error())}
		}
		u := wifiUpdate{kind: wifiUpdateJoin, msg: "Wi-Fi disconnected"}
		if snap, snapErr := wifiReadSnapshot(); snapErr == nil {
			u.snap = &snap
		}
		return u
	})
}

// wifiForgetSelected removes the saved network. It takes two presses: losing a
// saved password on a handheld usually means retyping it on a controller.
func wifiForgetSelected() {
	if wifiPanel.busy {
		return
	}
	n := wifiSelected()
	if n == nil {
		return
	}
	if !n.Saved {
		ShowToast(n.SSID+" is not saved on this device", ToastKindInfo)
		return
	}
	if wifiPanel.confirmForget != n.SSID {
		wifiPanel.confirmForget = n.SSID
		wifiPanel.confirmAt = time.Now()
		PlayNavSound()
		// "Press again" rather than "Press SELECT": the same action is a touch
		// pill now, and the second tap is the confirmation either way.
		ShowToast("Press again to forget "+n.SSID, ToastKindWarning)
		return
	}
	wifiPanel.confirmForget = ""
	ssid := n.SSID
	wifiStartWork("Forgetting "+ssid+"…", func() wifiUpdate {
		if err := wifiForget(ssid); err != nil {
			return wifiUpdate{kind: wifiUpdateJoin, err: firstLine(err.Error())}
		}
		u := wifiUpdate{kind: wifiUpdateJoin, msg: "Forgot " + ssid}
		if snap, snapErr := wifiReadSnapshot(); snapErr == nil {
			u.snap = &snap
		}
		return u
	})
}

// ── Input ──────────────────────────────────────────────────────────────────

// handleWiFiKey consumes every key while the panel is open. The password
// keyboard is handled earlier in the chain, so it keeps its keys.
func handleWiFiKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !wifiPanel.open || virtualKeyboardActive {
		return false
	}
	if wifiPanel.confirmForget != "" && time.Since(wifiPanel.confirmAt) > wifiForgetConfirmWindow {
		wifiPanel.confirmForget = ""
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		wifiMove(-1)
	case sdl.K_DOWN:
		wifiMove(1)
	case sdl.K_PAGEUP:
		wifiMove(-5)
	case sdl.K_PAGEDOWN:
		wifiMove(5)
	case sdl.K_RETURN, sdl.K_SPACE:
		wifiActivate()
	case sdl.K_y, sdl.K_r:
		wifiRefreshAsync(true)
	case sdl.K_x, sdl.K_d:
		wifiDisconnectSelected()
	case sdl.K_c:
		wifiReconnectSelected()
	case sdl.K_f:
		wifiForgetSelected()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeWiFiPanel()
	}
	return true
}

// ctrlWiFi is called before every other controller overlay.
func ctrlWiFi(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || !wifiPanel.open || virtualKeyboardActive {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		wifiMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		wifiMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		wifiActivate()
	case sdl.CONTROLLER_BUTTON_Y:
		wifiRefreshAsync(true)
	case sdl.CONTROLLER_BUTTON_X:
		wifiDisconnectSelected()
	case sdl.CONTROLLER_BUTTON_BACK:
		wifiForgetSelected()
	case sdl.CONTROLLER_BUTTON_B:
		closeWiFiPanel()
	}
	return true
}

// wifiRunAction is the single entry point for the panel's actions, shared by
// the tappable pills and the keyboard/controller handlers.
func wifiRunAction(id wifiActionID) {
	switch id {
	case wifiActionRescan:
		wifiRefreshAsync(true)
	case wifiActionReconnect:
		wifiReconnectSelected()
	case wifiActionDisconnect:
		wifiDisconnectSelected()
	case wifiActionForget:
		wifiForgetSelected()
	case wifiActionClose:
		closeWiFiPanel()
	}
}

// handleWiFiMouse selects and activates a row, presses an action pill, or
// closes the panel when the click lands outside it.
func handleWiFiMouse(mx, my int32, config *Config) bool {
	if !wifiPanel.open {
		return false
	}
	if wifiPanel.confirmForget != "" && time.Since(wifiPanel.confirmAt) > wifiForgetConfirmWindow {
		wifiPanel.confirmForget = ""
	}
	// Pills first: they sit below the list, but a tap that hits one must never
	// be read as a row click if the two ever get close.
	for _, hit := range wifiActionRects {
		if hit.rect.W > 0 && pointInRect(mx, my, hit.rect) {
			wifiRunAction(hit.id)
			return true
		}
	}
	for i, r := range wifiRowRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			wifiPanel.cursor = i
			wifiActivate()
			return true
		}
	}
	if wifiPanelRect.W > 0 && !pointInRect(mx, my, wifiPanelRect) {
		closeWiFiPanel()
	}
	return true
}

// ── Rendering ──────────────────────────────────────────────────────────────

// wifiStatusLine is the header's one-line truth: backend problem first, then
// work in flight, then the connection.
func wifiStatusLine() (string, sdl.Color) {
	switch {
	case !wifiPanel.available:
		return wifiPanel.detail, ColorToastWarn
	case wifiPanel.busy:
		return wifiPanel.busyLabel, ColorTextSecondary()
	case wifiPanel.radioOK && !wifiPanel.radioOn:
		return "Wi-Fi is off — press A to turn it on", ColorToastWarn
	case wifiPanel.err != "":
		return wifiPanel.err, ColorToastError
	case wifiPanel.status.Connected:
		line := "Connected to " + wifiPanel.status.SSID
		if wifiPanel.status.IP != "" {
			line += " · " + wifiPanel.status.IP
		}
		return line, ColorTextSecondary()
	case len(wifiPanel.networks) == 0:
		return "No networks yet — press Y to scan", ColorTextTertiary()
	}
	return "Not connected", ColorTextSecondary()
}

// wifiRowTag is one right-aligned state label on a network row.
type wifiRowTag struct {
	text  string
	color sdl.Color
}

func wifiSignalColor(bars int) sdl.Color {
	switch {
	case bars >= 4:
		return sdl.Color{R: 90, G: 200, B: 140, A: 235}
	case bars == 3:
		return sdl.Color{R: 140, G: 200, B: 110, A: 235}
	case bars == 2:
		return sdl.Color{R: 220, G: 180, B: 90, A: 235}
	}
	return sdl.Color{R: 220, G: 120, B: 100, A: 235}
}

// drawWiFiBars draws four ascending bars filled to the signal's strength.
func drawWiFiBars(renderer *sdl.Renderer, x, cy int32, bars int) {
	if bars < 1 {
		bars = 1
	}
	if bars > 4 {
		bars = 4
	}
	col := wifiSignalColor(bars)
	for i := 0; i < 4; i++ {
		h := int32(5 + i*4)
		bx := x + int32(i)*7
		by := cy + 9 - h
		c := col
		if i >= bars {
			c = sdl.Color{R: 70, G: 78, B: 96, A: 170}
		}
		fillRoundedRect(renderer, bx, by, 6, h, 1, c)
	}
}

// renderWiFiPanel draws the picker over the current scene.
func renderWiFiPanel(renderer *sdl.Renderer, config *Config) {
	if !wifiPanel.open || renderer == nil {
		return
	}
	wifiDrainUpdates()
	// Rebuilt below; clearing here keeps a pill from staying tappable on a
	// frame where the bar is not drawn (the backend became unavailable).
	wifiActionRects = wifiActionRects[:0]
	if wifiPanel.confirmForget != "" && time.Since(wifiPanel.confirmAt) > wifiForgetConfirmWindow {
		wifiPanel.confirmForget = ""
	}
	if wifiPanel.available && !wifiPanel.busy && !wifiWorkPending &&
		(wifiPanel.loadedAt.IsZero() || time.Since(wifiPanel.loadedAt) > wifiAutoRefresh) {
		wifiRefreshAsync(false)
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

	rowH := smallH + 22
	headerH := 18 + medH + 6 + smallH + 14
	footerH := 20 + smallH
	// Touch users need visible buttons for rescan/reconnect/disconnect/forget;
	// the hint line below stays for the pad. No backend means nothing to do, so
	// the bar is only reserved while the picker can act.
	actionH := int32(0)
	if wifiPanel.available {
		actionH = smallH + 18
	}

	// Fit the list to the screen before laying the panel out: on a 480p
	// handheld the same panel must still show its header, actions and footer.
	maxListH := screenHeight - headerH - footerH - actionH - 48
	visible := len(wifiPanel.networks)
	if visible < 1 {
		visible = 1
	}
	if visible > wifiPanelMaxRows {
		visible = wifiPanelMaxRows
	}
	for visible > 1 && int32(visible)*rowH > maxListH {
		visible--
	}

	panelW := int32(820)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := headerH + int32(visible)*rowH + actionH + footerH
	if panelH > screenHeight-40 {
		panelH = screenHeight - 40
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	wifiPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	renderText(renderer, config, medium, "Wi-Fi", ColorTextPrimary(), panelX+padX, panelY+18)
	line, lineColor := wifiStatusLine()
	renderText(renderer, config, font, ellipsize(font, line, panelW-2*padX),
		lineColor, panelX+padX, panelY+18+medH+6)

	// List window: keep the cursor in view without moving a short list.
	first := 0
	if wifiPanel.cursor >= visible {
		first = wifiPanel.cursor - visible + 1
	}
	listY := panelY + headerH
	wifiRowRects = wifiRowRects[:0]
	if len(wifiPanel.networks) == 0 {
		empty := "Press Y to scan for nearby networks"
		switch {
		case !wifiPanel.available:
			empty = wifiPanel.detail
		case wifiPanel.radioOK && !wifiPanel.radioOn:
			empty = "Turn Wi-Fi on to see nearby networks"
		}
		renderText(renderer, config, font, ellipsize(font, empty, panelW-2*padX),
			ColorTextTertiary(), panelX+padX, listY+10)
	} else {
		focusColor := installHighlight()
		rowW := panelW - 2*padX
		rowY := listY
		for i := 0; i < visible; i++ {
			idx := first + i
			if idx >= len(wifiPanel.networks) {
				break
			}
			n := wifiPanel.networks[idx]
			rect := sdl.Rect{X: panelX + padX, Y: rowY, W: rowW, H: rowH - 4}
			wifiRowRects = append(wifiRowRects, rect)
			focused := idx == wifiPanel.cursor
			if focused {
				fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, WithAlpha(focusColor, 46))
				strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 1, focusColor)
			}
			textColor := ColorTextSecondary()
			if focused {
				textColor = ColorTextPrimary()
			}
			cy := rect.Y + rect.H/2
			// statusWiFiSignal is the meter's own source chain, so the connected
			// row cannot disagree with the corner it was opened from.
			drawWiFiBars(renderer, rect.X+12, cy, wifiRowBars(n, statusWiFiSignal))
			ssidX := rect.X + 52
			tagW := int32(0)
			tags := make([]wifiRowTag, 0, 3)
			if n.Active {
				tags = append(tags, wifiRowTag{"CONNECTED", accentColor})
			}
			if n.Saved {
				tags = append(tags, wifiRowTag{"SAVED", ColorTextTertiary()})
			}
			if n.Secured {
				tags = append(tags, wifiRowTag{"SECURE", ColorTextTertiary()})
			}
			for _, tag := range tags {
				tw, _, _ := font.SizeUTF8(tag.text)
				tagW += int32(tw) + 14
			}
			nameW := rect.X + rect.W - 16 - tagW - ssidX
			if nameW < 40 {
				nameW = 40
			}
			renderText(renderer, config, font, ellipsize(font, n.SSID, nameW),
				textColor, ssidX, rect.Y+(rect.H-smallH)/2)
			tagX := rect.X + rect.W - 14
			for i := len(tags) - 1; i >= 0; i-- {
				tag := tags[i]
				tw, _, _ := font.SizeUTF8(tag.text)
				tagX -= int32(tw)
				renderText(renderer, config, font, tag.text, tag.color,
					tagX, rect.Y+(rect.H-smallH)/2)
				tagX -= 14
			}
			rowY += rowH
		}
	}

	if actionH > 0 {
		wifiPanelActionBar(renderer, config, font, panelX,
			panelY+panelH-footerH-actionH, panelW, actionH, smallH)
	}

	footer := hintLine(FooterHint{"A", "Join"}, FooterHint{"X", "Disconnect"},
		FooterHint{"Y", "Rescan"}, FooterHint{"Select", "Forget"}, FooterHint{"B", "Close"})
	footerColor := ColorTextTertiary()
	switch {
	case wifiPanel.confirmForget != "":
		footer = hintLine(FooterHint{Label: "Select again to forget " + wifiPanel.confirmForget},
			FooterHint{"B", "Cancel"})
		footerColor = ColorToastWarn
	case !wifiPanel.available:
		footer = hintLine(FooterHint{"B", "Close"})
	}
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, footerColor,
		panelX+(panelW-int32(fw))/2, panelY+panelH-footerH+4)
}

// wifiPanelActionBar draws the tappable actions and records their rects. The
// set follows the state: a disconnected radio has nothing to reconnect or
// disconnect, and an unsaved row has nothing to forget, so those pills are not
// drawn at all rather than greyed out and clickable-looking.
func wifiPanelActionBar(renderer *sdl.Renderer, config *Config, font *ttf.Font,
	x, y, w, h, textH int32) {
	wifiActionRects = wifiActionRects[:0]
	if renderer == nil || font == nil || h <= 0 || w <= 0 {
		return
	}
	type pill struct {
		id    wifiActionID
		label string
	}
	pills := []pill{{wifiActionRescan, "Rescan"}}
	if wifiPanel.status.Connected {
		pills = append(pills,
			pill{wifiActionReconnect, "Reconnect"},
			pill{wifiActionDisconnect, "Disconnect"})
	}
	if n := wifiSelected(); n != nil && n.Saved {
		label := "Forget"
		if wifiPanel.confirmForget == n.SSID {
			// The second press is the confirmation, so the pill says so.
			label = "Forget?"
		}
		pills = append(pills, pill{wifiActionForget, label})
	}
	pills = append(pills, pill{wifiActionClose, "Close"})

	const gap = int32(8)
	padX := int32(14)
	// A 4:3 handheld has less room than the 1280 canvas, so tighten the pills
	// rather than letting the row spill out of the panel.
	widths := make([]int32, len(pills))
	total := int32(0)
	for i, p := range pills {
		tw, _, _ := font.SizeUTF8(p.label)
		widths[i] = int32(tw) + 2*padX
		total += widths[i]
	}
	total += gap * int32(len(pills)-1)
	if total > w-16 {
		padX = 8
		total = 0
		for i, p := range pills {
			tw, _, _ := font.SizeUTF8(p.label)
			widths[i] = int32(tw) + 2*padX
			total += widths[i]
		}
		total += gap * int32(len(pills)-1)
	}

	pillH := textH + 10
	if pillH > h-2 {
		pillH = h - 2
	}
	px := x + (w-total)/2
	py := y + (h-pillH)/2
	for i, p := range pills {
		r := sdl.Rect{X: px, Y: py, W: widths[i], H: pillH}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, ColorIconSurface)
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, 1, ColorBorder)
		renderText(renderer, config, font, p.label, ColorTextSecondary(),
			r.X+padX, r.Y+(r.H-textH)/2)
		wifiActionRects = append(wifiActionRects, wifiActionHit{id: p.id, rect: r})
		px += r.W + gap
	}
}

// ensureHardwareWiFiEntry gives the Hardware scene a "Wi-Fi" button without
// touching the config on disk, mirroring ensureHardwareProcessEntry. It sits on
// the action row, to the right of whatever buttons are already there.
func ensureHardwareWiFiEntry(config *Config) {
	if config == nil {
		return
	}
	for i := range config.Scenes {
		scene := &config.Scenes[i]
		if scene.Name != "Hardware" {
			continue
		}
		for _, e := range scene.Elements {
			if e.Trigger == "wifi_open" {
				return
			}
		}
		x := int32(60)
		y := int32(config.Height - 109)
		for _, e := range scene.Elements {
			if e.Type != "button" {
				continue
			}
			if right := e.X + getElementWidth(e, 140) + 20; right > x {
				x = right
			}
			if e.Y > y-20 {
				y = e.Y
			}
		}
		scene.Elements = append(scene.Elements, Element{
			Type:    "button",
			Text:    "Wi-Fi",
			Color:   "$labelColor",
			BgColor: "$buttonColor",
			X:       x,
			Y:       y,
			Width:   "140",
			Height:  "45",
			Trigger: "wifi_open",
		})
		return
	}
}
