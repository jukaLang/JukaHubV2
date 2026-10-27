package main

// wifi_actions_test.go covers the touch path of the Wi-Fi picker. The status
// bar meter is the entry point on the handheld, so a tap has to reach it, and
// once the picker is open every action (rescan, reconnect, disconnect, forget,
// close) needs a button: the footer hints are pad-only.

import (
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// TestWiFiReconnectDropsAndRejoins pins the reconnect sequence: the
// association is dropped first (select_network alone is a no-op while the
// supplicant still thinks it is connected) and the saved key is selected
// again.
func TestWiFiReconnectDropsAndRejoins(t *testing.T) {
	var order []string
	stubWiFi(t, func(stdin, name string, args []string) (string, error) {
		if stdin != "" {
			return "OK\n", nil
		}
		cmd := wifiStubCommand(args)
		order = append(order, cmd)
		switch cmd {
		case "status":
			return "wpa_state=COMPLETED\nssid=HomeNet\nip_address=192.168.1.20\n", nil
		case "list_networks":
			return "network id / ssid / bssid / flags\n2\tHomeNet\tany\t[CURRENT]\n", nil
		}
		return "OK\n", nil
	})

	res, err := wifiReconnect()
	if err != nil {
		t.Fatalf("wifiReconnect: %v", err)
	}
	if res.SSID != "HomeNet" {
		t.Fatalf("reconnect reported %q, want HomeNet", res.SSID)
	}

	disconnectAt, selectAt := -1, -1
	for i, cmd := range order {
		switch cmd {
		case "disconnect":
			if disconnectAt < 0 {
				disconnectAt = i
			}
		case "select_network":
			selectAt = i
		}
	}
	if disconnectAt < 0 {
		t.Fatalf("reconnect never dropped the association: %v", order)
	}
	if selectAt < 0 || selectAt < disconnectAt {
		t.Fatalf("reconnect did not select the saved network after disconnecting: %v", order)
	}
}

// TestWiFiReconnectNeedsASavedNetwork: a link that is not in the saved list has
// no key to reconnect with, so the action has to report that instead of
// pretending to work.
func TestWiFiReconnectNeedsASavedNetwork(t *testing.T) {
	stubWiFi(t, func(stdin, name string, args []string) (string, error) {
		switch wifiStubCommand(args) {
		case "status":
			return "wpa_state=COMPLETED\nssid=Guest\nip_address=192.168.1.50\n", nil
		case "list_networks":
			return "network id / ssid / bssid / flags\n", nil
		}
		return "OK\n", nil
	})
	if _, err := wifiReconnect(); err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("reconnecting an unsaved network returned %v, want a password hint", err)
	}
}

// TestWiFiPanelActionsAreTappable renders the picker and checks that every
// action it offers has a touch target inside the panel, then presses Close
// through the same mouse path a finger uses.
func TestWiFiPanelActionsAreTappable(t *testing.T) {
	_ = newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)

	prevPanel, prevActions, prevRows := wifiPanel, wifiActionRects, wifiRowRects
	t.Cleanup(func() {
		wifiPanel, wifiActionRects, wifiRowRects = prevPanel, prevActions, prevRows
		ClearTextCache()
	})

	for _, size := range []struct{ w, h int32 }{{probeW, probeH}, {800, 480}} {
		withScreen(t, size.w, size.h)
		wifiPanel = wifiPanelState{
			open:      true,
			available: true,
			radioOn:   true,
			loadedAt:  time.Now(),
			status:    wifiStatus{State: wifiCompleteState, SSID: "HomeNet", IP: "192.168.1.20", Connected: true},
			networks: []wifiNetwork{
				{SSID: "HomeNet", Signal: -47, Secured: true, Saved: true, Active: true},
				{SSID: "Cafe", Signal: -78},
			},
		}
		renderer.SetDrawColor(9, 14, 25, 255)
		renderer.Clear()
		renderWiFiPanel(renderer, &Config{})

		got := map[wifiActionID]sdl.Rect{}
		for _, hit := range wifiActionRects {
			got[hit.id] = hit.rect
			r := hit.rect
			if r.W <= 0 || r.H <= 0 {
				t.Fatalf("%dx%d: action %v has an empty rect", size.w, size.h, hit.id)
			}
			if !pointInRect(r.X, r.Y, wifiPanelRect) || !pointInRect(r.X+r.W-1, r.Y+r.H-1, wifiPanelRect) {
				t.Fatalf("%dx%d: action %v rect %+v leaves the panel %+v",
					size.w, size.h, hit.id, r, wifiPanelRect)
			}
			if r.H < 24 {
				t.Fatalf("%dx%d: action %v is only %dpx tall, too small for a finger",
					size.w, size.h, hit.id, r.H)
			}
		}
		for _, id := range []wifiActionID{wifiActionRescan, wifiActionReconnect, wifiActionDisconnect, wifiActionClose} {
			if _, ok := got[id]; !ok {
				t.Fatalf("%dx%d: no touch target for action %v (have %d pills)", size.w, size.h, id, len(got))
			}
		}

		closeRect := got[wifiActionClose]
		if !handleWiFiMouse(closeRect.X+closeRect.W/2, closeRect.Y+closeRect.H/2, &Config{}) {
			t.Fatalf("%dx%d: tap on Close was not handled", size.w, size.h)
		}
		if wifiPanel.open {
			t.Fatalf("%dx%d: tap on Close left the panel open", size.w, size.h)
		}
	}

	// Nothing to disconnect or forget on a disconnected picker: those pills are
	// not drawn rather than drawn and inert.
	withScreen(t, probeW, probeH)
	wifiPanel = wifiPanelState{
		open:      true,
		available: true,
		radioOn:   true,
		loadedAt:  time.Now(),
		status:    wifiStatus{State: "DISCONNECTED"},
		networks:  []wifiNetwork{{SSID: "Cafe", Signal: -70}},
	}
	renderer.Clear()
	renderWiFiPanel(renderer, &Config{})
	for _, hit := range wifiActionRects {
		if hit.id == wifiActionReconnect || hit.id == wifiActionDisconnect || hit.id == wifiActionForget {
			t.Fatalf("disconnected picker still offers action %v", hit.id)
		}
	}
}

// TestStatusWiFiTapTargetIsFingerSized renders the real status bar and checks
// that the meter's hit area is bigger than the 34px it draws, spans the bar
// height, never overlaps the clock, and that a tap in the grown margin still
// opens the picker.
func TestStatusWiFiTapTargetIsFingerSized(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	pinStatusValues(t, 60, 3)
	_ = statusBarProbe(t, config)

	if statusWiFiRect.W <= 0 {
		t.Fatal("the Wi-Fi meter was not measured")
	}
	if statusWiFiRect.W < 44 {
		t.Fatalf("meter hit area is %dpx wide, too small for a finger", statusWiFiRect.W)
	}
	if statusWiFiRect.Y > 0 || statusWiFiRect.Y+statusWiFiRect.H < HomeTopBarH {
		t.Fatalf("meter hit area %+v does not span the top bar", statusWiFiRect)
	}
	if statusClockRect.W > 0 && statusWiFiRect.X+statusWiFiRect.W > statusClockRect.X {
		t.Fatalf("meter hit area %+v overlaps the clock %+v", statusWiFiRect, statusClockRect)
	}

	prevPanel, prevRects := wifiPanel, wifiRowRects
	t.Cleanup(func() {
		wifiPanel, wifiRowRects = prevPanel, prevRects
		wifiGeneration++
		wifiDrainDiscarded()
	})
	wifiPanel = wifiPanelState{}

	// The padded edge is outside the drawn bars but inside the target: that is
	// the whole point of growing it.
	tapX := statusWiFiRect.X + 2
	tapY := statusWiFiRect.Y + 4
	if !handleChromeBarClick(tapX, tapY, config) {
		t.Fatalf("tap at (%d,%d) near the meter's edge was not handled", tapX, tapY)
	}
	if !wifiPanel.open {
		t.Fatal("the tap did not open the Wi-Fi picker")
	}
	wifiPanel.open = false
}

// TestWiFiStatusLineNamesTheScanProblem: a scan the supplicant refuses must say
// why in the header. The panel used to put Go's "exit status 255" there with an
// empty list under it, which told a person neither what broke nor what to do.
func TestWiFiStatusLineNamesTheScanProblem(t *testing.T) {
	prev := wifiPanel
	t.Cleanup(func() { wifiPanel = prev })

	wifiPanel = wifiPanelState{
		open:      true,
		available: true,
		radioOn:   true,
		radioOK:   true,
		scanErr:   "Wi-Fi control is unreachable — wpa_supplicant is not answering",
	}
	line, _ := wifiStatusLine()
	if !strings.Contains(line, "unreachable") {
		t.Fatalf("header = %q, want the scan's reason", line)
	}
	if !strings.Contains(line, "Y") {
		t.Fatalf("header = %q, want the retry hint", line)
	}

	// Connected: the connection is the more useful line, and the scan problem
	// must not hide it.
	wifiPanel.status = wifiStatus{State: "COMPLETED", SSID: "HomeNet", IP: "10.0.0.7", Connected: true}
	wifiPanel.networks = []wifiNetwork{{SSID: "HomeNet", Active: true}}
	line, _ = wifiStatusLine()
	if !strings.Contains(line, "Connected to HomeNet") {
		t.Fatalf("header = %q, want the connection", line)
	}
}

// TestWiFiScanProblemSurvivesAStatusRefresh: the reason a scan did not run must
// stay in the header until a scan works. The auto-refresh re-reads the status
// every 15 seconds and does not scan, and it used to be enough to wipe the
// explanation and fall back to "press Y to scan" — which is what the user had
// already pressed.
func TestWiFiScanProblemSurvivesAStatusRefresh(t *testing.T) {
	prev := wifiPanel
	t.Cleanup(func() {
		wifiPanel = prev
		wifiGeneration++
		wifiDrainDiscarded()
		wifiWorkPending = false
	})
	wifiPanel = wifiPanelState{open: true, available: true}
	wifiGeneration++

	snap := wifiSnapshot{Status: wifiStatus{State: "COMPLETED", SSID: "HomeNet", Connected: true}}
	wifiUpdates <- wifiUpdate{
		kind:    wifiUpdateState,
		gen:     wifiGeneration,
		snap:    &snap,
		scanErr: "Wi-Fi control is unreachable — wpa_supplicant is not answering",
		scanned: true,
	}
	wifiDrainUpdates()

	// The plain status refresh that follows carries no scan information.
	wifiUpdates <- wifiUpdate{kind: wifiUpdateState, gen: wifiGeneration, snap: &snap}
	wifiDrainUpdates()
	if !strings.Contains(wifiPanel.scanErr, "unreachable") {
		t.Fatalf("scanErr = %q, want the reason kept across a status refresh", wifiPanel.scanErr)
	}

	// A scan that works clears it.
	wifiUpdates <- wifiUpdate{kind: wifiUpdateState, gen: wifiGeneration, snap: &snap, scanned: true}
	wifiDrainUpdates()
	if wifiPanel.scanErr != "" {
		t.Fatalf("scanErr = %q, want it cleared by a successful scan", wifiPanel.scanErr)
	}
}
