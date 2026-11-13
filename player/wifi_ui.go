package main

// wifi_ui.go — the Wi-Fi state machine behind the Wi-Fi page (wifiscreen.go).
//
// The page is the first-party replacement for the firmware's settings screen:
// scan, join (passphrase typed on the on-screen keyboard), reconnect a saved
// network without retyping its password, disconnect, forget, and switch the
// radio. This file holds what the page shows and the work behind it; the page
// owns the drawing and the input.
//
// Everything that touches wpa_cli (or netsh on Windows) runs on a worker
// goroutine, exactly like the Quick Settings probe: a wedged supplicant must
// not freeze the render thread. Results are handed back through a one-slot
// channel that the page drains every frame, so the state is only ever written
// from the event thread.

import (
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// wifiForgetConfirmWindow is how long the first Forget press stays armed.
	wifiForgetConfirmWindow = 5 * time.Second
	// wifiAutoRefresh re-reads status while the page is open, so a drop or a
	// connection made elsewhere is reflected without a manual rescan. It never
	// triggers a scan: that would cost battery for no extra information.
	wifiAutoRefresh = 15 * time.Second
)

type wifiPanelState struct {
	// open is the retired picker overlay's flag. The picker is the Wi-Fi page
	// now and nothing sets it; it stays false and is kept only because the
	// modal audits (ambient.go, shoulders.go, updateprompt.go) still list it.
	open      bool
	cursor    int
	networks  []wifiNetwork
	saved     []savedNetwork
	status    wifiStatus
	radioOn   bool
	radioOK   bool
	available bool
	// startable: no backend yet, but this image has a radio and a switch, so
	// the page opens in "turn Wi-Fi on" mode instead of refusing. The Brick's
	// image starts its supplicant from the firmware's own Wi-Fi menu, so a
	// device that never had Wi-Fi on there has no control socket to find and
	// the one control that could create it used to be unreachable.
	startable bool
	detail    string
	err       string
	// scanErr is why the last scan did not run, kept apart from err so a
	// refused scan still leaves the connection and the saved list on screen.
	scanErr  string
	loadedAt time.Time

	busy      bool
	busyLabel string

	confirmForget string
	confirmAt     time.Time

	// diagOpen is true while the diagnostics sheet is open on the page. The
	// sheet is reachable while the list itself cannot load — the state it
	// diagnoses.
	diagOpen bool
	diag     wifiDiagReport
}

var wifiPanel wifiPanelState

// wifiPendingSSID is the network whose password the keyboard is typing.
var wifiPendingSSID string

// ── Worker results ─────────────────────────────────────────────────────────

type wifiUpdateKind int

const (
	wifiUpdateState wifiUpdateKind = iota
	wifiUpdateJoin
	wifiUpdateDiag
)

type wifiUpdate struct {
	kind wifiUpdateKind
	gen  uint64
	snap *wifiSnapshot
	err  string
	// scanErr reports a scan that failed while the rest of the refresh worked.
	scanErr string
	// scanned marks an update that carried scan information, so a plain
	// status refresh cannot silently forget the reason a scan did not run.
	scanned bool
	msg     string
	// prompt names the SSID whose stored password turned out to be wrong;
	// the drain opens the passphrase keyboard for it after showing the error.
	prompt string
	// diag carries a collected diagnostics report for the sheet.
	diag *wifiDiagReport
}

var (
	wifiUpdates     = make(chan wifiUpdate, 1)
	wifiWorkPending bool
	// wifiGeneration invalidates results from a previous visit to the page
	// (or from a scan superseded by a newer one). A stale worker must not
	// overwrite the list the user is looking at.
	wifiGeneration uint64
)

// wifiStartWork runs fn off the event thread and marks the page busy. A
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
		// Block rather than drop: a dropped result would leave the page busy
		// forever (the flag is only cleared by the drain), and a newer worker
		// would silently lose its result to a stale one already in the slot.
		// The page drains once per frame, and a new visit drains the slot
		// too, so the wait is bounded.
		wifiUpdates <- u
	}()
}

// wifiDrainUpdates applies finished work. Called from the page's render.
func wifiDrainUpdates() {
	for {
		select {
		case u := <-wifiUpdates:
			if u.gen != wifiGeneration {
				// A worker from an ended or superseded visit: discard it
				// without touching the busy flag owned by the current one.
				continue
			}
			wifiWorkPending = false
			wifiPanel.busy = false
			wifiPanel.busyLabel = ""
			if u.snap != nil {
				wifiApplySnapshot(*u.snap)
			}
			if u.diag != nil {
				wifiPanel.diag = *u.diag
			}
			switch {
			case u.scanned:
				wifiPanel.scanErr = u.scanErr
			case u.msg != "" && u.err == "":
				// A completed action proves the backend answers again, so a
				// remembered scan problem is no longer the truth.
				wifiPanel.scanErr = ""
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
			if u.prompt != "" && !virtualKeyboardActive {
				// The stored password was rejected: retyping it is the only
				// fix, and the error above says why. Runs on the render
				// thread, where opening the keyboard belongs.
				wifiPromptPassphrase(u.prompt)
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
	// A snapshot that could be read is proof the backend answers, so a page
	// opened in "turn Wi-Fi on" mode promotes itself to the real list.
	wifiPanel.available = true
	wifiPanel.startable = false
	wifiPanel.detail = ""
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

// ── Visits ────────────────────────────────────────────────────────────────

// wifiScrBeginVisit starts a fresh visit: anything still in flight belongs to
// the previous one, the interface is looked up again and a scan starts.
func wifiScrBeginVisit() {
	diag := wifiPanel.diag
	wifiPanel = wifiPanelState{diag: diag}
	wifiGeneration++
	wifiWorkPending = false
	// Opening the page is the moment a person would have plugged an adapter
	// in, so the supplicant is asked again instead of trusting a cached answer
	// for the rest of its minute.
	resetWiFiInterfaceCache()

	_, detail := wifiSupport()
	wifiPanel.available = detail == ""
	wifiPanel.detail = detail
	wifiPanel.startable = !wifiPanel.available && wifiEntryReachable()
	if !wifiPanel.available {
		if wifiPanel.startable {
			// The page can still switch the radio on and start the
			// supplicant, so it opens on that instead of turning the user away.
			wifiPanel.radioOn, wifiPanel.radioOK = wifiRadioState()
			return
		}
		wifiPanel.err = detail
		return
	}
	// Discard anything a previous visit left behind before asking for fresh
	// state, so an old list cannot appear as the result of this scan.
	wifiDrainDiscarded()
	wifiRefreshAsync(true)
}

// wifiScrEndVisit ends a visit: in-flight work is invalidated, so its toast
// cannot appear over the screen the user went back to, and a typed but
// unsubmitted password is dropped.
func wifiScrEndVisit() {
	wifiPanel.confirmForget = ""
	wifiPanel.diagOpen = false
	wifiPendingSSID = ""
	wifiPassphraseBuffer = ""
	wifiGeneration++
	wifiDrainDiscarded()
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
		scanErr := ""
		if scan {
			if _, err := wifiScanNetworks(); err != nil {
				scanErr = firstLine(err.Error())
			}
		}
		snap, err := wifiReadSnapshot()
		if err != nil {
			if scanErr != "" {
				// Both halves failed: the scan's reason is the more specific one.
				return wifiUpdate{err: scanErr}
			}
			return wifiUpdate{err: firstLine(err.Error())}
		}
		// A refused scan costs the list, not the page: the connected network
		// and the saved entries are still the truth about this device.
		return wifiUpdate{kind: wifiUpdateState, snap: &snap, scanErr: scanErr, scanned: true}
	})
}

// ── Navigation and actions ─────────────────────────────────────────────────

func wifiMove(delta int) {
	if len(wifiPanel.networks) == 0 {
		return
	}
	before := wifiPanel.cursor
	wifiPanel.cursor += delta
	if wifiPanel.cursor < 0 {
		wifiPanel.cursor = 0
	}
	if wifiPanel.cursor >= len(wifiPanel.networks) {
		wifiPanel.cursor = len(wifiPanel.networks) - 1
	}
	wifiPanel.confirmForget = ""
	if wifiPanel.cursor != before {
		PlayNavSound()
	}
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
		if !wifiPanel.startable {
			ShowToast(wifiPanel.detail, ToastKindWarning)
			return
		}
		if wifiPanel.radioOK && !wifiPanel.radioOn {
			wifiEnableRadioAsync()
			return
		}
		// The radio is already on: the missing piece is the supplicant, so
		// start it and try the route again.
		wifiStartBackendAsync()
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
		// Pressing the network that already shows Connected reconnects it.
		// The common case (it says connected but nothing loads) is a stale
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
// rfkill can each take seconds on a slow boot, and a frozen page would look
// like the A press did nothing.
func wifiEnableRadioAsync() {
	if wifiPanel.busy {
		return
	}
	if iface := wifiInterface(); iface != "" {
		resetSupplicantStart(iface)
	}
	wifiStartWork("Turning Wi-Fi on…", func() wifiUpdate {
		if err := setWifiEnabled(true); err != nil {
			return wifiUpdate{kind: wifiUpdateJoin, err: firstLine(err.Error())}
		}
		// Home's Wi-Fi tile reads the radio again (home_quicktiles.go).
		saverWifiInvalidate()
		// Give the interface a moment to come up before scanning.
		time.Sleep(600 * time.Millisecond)
		return wifiBackendUpdate("Wi-Fi on")
	})
}

// wifiDisableRadioAsync switches the radio off, the page's header switch. The
// supplicant stops answering once the radio is down, so the result is built
// here instead of read back.
func wifiDisableRadioAsync() {
	if wifiPanel.busy {
		return
	}
	wifiStartWork("Turning Wi-Fi off…", func() wifiUpdate {
		if err := setWifiEnabled(false); err != nil {
			return wifiUpdate{kind: wifiUpdateJoin, err: firstLine(err.Error())}
		}
		saverWifiInvalidate()
		snap := wifiSnapshot{RadioOn: false, RadioOK: true}
		return wifiUpdate{kind: wifiUpdateState, snap: &snap, scanned: true, msg: "Wi-Fi off"}
	})
}

// wifiStartBackendAsync retries the supplicant start without touching the radio,
// for a device whose radio is on but whose control socket is missing.
func wifiStartBackendAsync() {
	if wifiPanel.busy {
		return
	}
	if iface := wifiInterface(); iface != "" {
		resetSupplicantStart(iface)
	}
	wifiStartWork("Starting Wi-Fi…", func() wifiUpdate {
		return wifiBackendUpdate("Wi-Fi ready")
	})
}

// wifiBackendUpdate brings the supplicant route up (when the image has one but
// nothing is listening), then reads the snapshot. It runs on the worker
// goroutine; the answer goes back through the usual update slot.
func wifiBackendUpdate(msg string) wifiUpdate {
	if wifiCommandBackend() == "" {
		ensureWifiBackend()
	}
	if !wifiControlAvailable() {
		// No route still: report the reason the page knows, so a radio that
		// refused to come up says why instead of looking like a dead button.
		return wifiUpdate{kind: wifiUpdateState, err: firstLine(wifiUnavailableReason())}
	}
	scanErr := ""
	if _, err := wifiScanNetworks(); err != nil {
		scanErr = firstLine(err.Error())
	}
	snap, err := wifiReadSnapshot()
	if err != nil {
		if scanErr != "" {
			return wifiUpdate{kind: wifiUpdateState, err: scanErr}
		}
		return wifiUpdate{err: firstLine(err.Error())}
	}
	return wifiUpdate{kind: wifiUpdateState, snap: &snap, scanErr: scanErr, scanned: true, msg: msg}
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
		trophyEvent(tkWifiJoin)
		trophyEventDistinct(tkWifiNets, res.SSID)
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
			u := wifiUpdate{kind: wifiUpdateJoin, err: firstLine(err.Error())}
			if wifiPasswordRejected(err) {
				u.prompt = res.SSID
			}
			return u
		}
		trophyEvent(tkWifiJoin)
		trophyEventDistinct(tkWifiNets, res.SSID)
		u := wifiUpdate{kind: wifiUpdateJoin, msg: wifiJoinMessage(res)}
		if snap, snapErr := wifiReadSnapshot(); snapErr == nil {
			u.snap = &snap
		}
		return u
	})
}

// wifiPasswordRejected marks the one join failure the user can fix by
// retyping: a stored password the router no longer accepts. Without this the
// page only toasted the error forever, and selecting the network again retried
// the same rejected key with no way to enter a new one.
func wifiPasswordRejected(err error) bool {
	return err != nil && strings.Contains(err.Error(), "password was rejected")
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
// key. It backs the details sheet's Reconnect.
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
			u := wifiUpdate{kind: wifiUpdateJoin, err: firstLine(err.Error())}
			if wifiPasswordRejected(err) {
				u.prompt = res.SSID
			}
			return u
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
		// "Press again" rather than a button name: the same action is a touch
		// pill too, and the second tap is the confirmation either way.
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

// ── Status ─────────────────────────────────────────────────────────────────

// wifiStatusLine is the page header's one-line truth: backend problem first,
// then work in flight, then the connection.
func wifiStatusLine() (string, sdl.Color) {
	switch {
	case wifiPanel.startable && wifiPanel.radioOK && !wifiPanel.radioOn:
		return "Wi-Fi is off — press A to turn it on", ColorToastWarn
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
	case wifiPanel.scanErr != "":
		// Nothing to list and the scan is why: say so instead of the generic
		// "press Y" hint, which reads like the button was never pressed.
		return wifiPanel.scanErr + " — press Y to retry", ColorToastWarn
	case len(wifiPanel.networks) == 0:
		return "No networks yet — press Y to scan", ColorTextTertiary()
	}
	return "Not connected", ColorTextSecondary()
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

// drawWiFiBars draws four ascending bars filled to the signal's strength; the
// unlit bars are the glass ink, so they read on light and dark themes alike.
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
			c = glassA(52)
		}
		fillRoundedRect(renderer, bx, by, 6, h, 1, c)
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
