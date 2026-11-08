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

// TestWiFiPanelActionsAreTappable renders the Wi-Fi page and checks that every
// action it offers has a finger-sized touch target: the header's Scan and
// Diagnostics, each row's details button, and the details sheet's pills —
// Reconnect, Disconnect and Forget for the connected network — then presses
// Close through the same click path a finger uses.
func TestWiFiPanelActionsAreTappable(t *testing.T) {
	config := toolsHubTestNav(t)
	renderer := probeRenderer(t)
	currentSceneIndex = sceneIndexExact(config, wifiScrSceneName)
	// A details read counts as running, so opening the connected network's
	// sheet does not start a real one.
	wifiScrDetails.mu.Lock()
	prevBusy := wifiScrDetails.busy
	wifiScrDetails.busy = true
	wifiScrDetails.mu.Unlock()
	t.Cleanup(func() {
		wifiScrDetails.mu.Lock()
		wifiScrDetails.busy = prevBusy
		wifiScrDetails.mu.Unlock()
	})

	frame := func() {
		renderer.SetDrawColor(9, 14, 25, 255)
		renderer.Clear()
		renderWiFiScr(renderer, config)
	}
	for _, size := range []struct{ w, h int32 }{{probeW, probeH}, {800, 480}} {
		withScreen(t, size.w, size.h)
		wifiPanel = wifiPanelState{
			available: true,
			radioOn:   true,
			loadedAt:  time.Now(),
			status:    wifiStatus{State: wifiCompleteState, SSID: "HomeNet", IP: "192.168.1.20", Connected: true},
			networks: []wifiNetwork{
				{SSID: "HomeNet", Signal: -47, Secured: true, Saved: true, Active: true},
				{SSID: "Cafe", Signal: -78},
			},
		}
		wifiScr.sheet = toolsHubSheet{}
		wifiScr.follow = true
		frame()

		ids := map[string]bool{}
		for _, c := range wifiScr.ctrls {
			ids[c.id] = true
			if c.rect.W <= 0 || c.rect.H < 24 || c.rect.X < 0 || c.rect.X+c.rect.W > size.w {
				t.Fatalf("%dx%d: header control %q at %+v is not a usable target", size.w, size.h, c.id, c.rect)
			}
		}
		if !ids["scan"] || !ids["diag"] {
			t.Fatalf("%dx%d: header controls %v, want Scan and Diagnostics", size.w, size.h, ids)
		}
		info := wifiScr.infoRects[0]
		if info.W < 24 || info.H < 24 {
			t.Fatalf("%dx%d: the details button %+v is too small for a finger", size.w, size.h, info)
		}
		if !wifiScrClick(info.X+info.W/2, info.Y+info.H/2, config) || !wifiScr.sheet.open {
			t.Fatalf("%dx%d: tapping the details button did not open the sheet", size.w, size.h)
		}
		frame()
		s := &wifiScr.sheet
		got := map[string]sdl.Rect{}
		for i, r := range s.pillRects {
			got[s.pills[i].id] = r
			if r.W <= 0 || r.H < 24 {
				t.Fatalf("%dx%d: pill %q %+v is too small for a finger", size.w, size.h, s.pills[i].id, r)
			}
			if !pointInRect(r.X, r.Y, s.card) || !pointInRect(r.X+r.W-1, r.Y+r.H-1, s.card) {
				t.Fatalf("%dx%d: pill %q %+v leaves the card %+v", size.w, size.h, s.pills[i].id, r, s.card)
			}
		}
		for _, id := range []string{"reconnect", "disconnect", "forget", "close"} {
			if _, ok := got[id]; !ok {
				t.Fatalf("%dx%d: no touch target for %q (have %d pills)", size.w, size.h, id, len(got))
			}
		}
		closeRect := got["close"]
		if !wifiScrClick(closeRect.X+closeRect.W/2, closeRect.Y+closeRect.H/2, config) {
			t.Fatalf("%dx%d: tap on Close was not handled", size.w, size.h)
		}
		if s.open || transitionPhase != "none" {
			t.Fatalf("%dx%d: tap on Close left the sheet open or left the page", size.w, size.h)
		}
	}

	// Nothing to disconnect or forget on a network that is not the connection
	// and not saved: those pills are not drawn rather than drawn and inert.
	withScreen(t, probeW, probeH)
	wifiPanel = wifiPanelState{
		available: true,
		radioOn:   true,
		loadedAt:  time.Now(),
		status:    wifiStatus{State: "DISCONNECTED"},
		networks:  []wifiNetwork{{SSID: "Cafe", Signal: -70}},
	}
	frame()
	info := wifiScr.infoRects[0]
	wifiScrClick(info.X+info.W/2, info.Y+info.H/2, config)
	frame()
	for _, p := range wifiScr.sheet.pills {
		if p.id == "reconnect" || p.id == "disconnect" || p.id == "forget" {
			t.Fatalf("an unsaved, unconnected network still offers %q", p.id)
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

	prevPanel, prevFresh := wifiPanel, wifiScr.fresh
	prevScene, prevPending, prevPhase := currentSceneIndex, pendingSceneIndex, transitionPhase
	prevHistory := append([]int(nil), sceneHistory...)
	t.Cleanup(func() {
		wifiSettleForTest()
		wifiPanel, wifiScr.fresh = prevPanel, prevFresh
		currentSceneIndex, pendingSceneIndex, transitionPhase = prevScene, prevPending, prevPhase
		sceneHistory = prevHistory
	})
	wifiPanel = wifiPanelState{}
	currentSceneIndex, pendingSceneIndex, transitionPhase = 0, -1, "none"

	// The padded edge is outside the drawn bars but inside the target: that is
	// the whole point of growing it.
	tapX := statusWiFiRect.X + 2
	tapY := statusWiFiRect.Y + 4
	if !handleChromeBarClick(tapX, tapY, config) {
		t.Fatalf("tap at (%d,%d) near the meter's edge was not handled", tapX, tapY)
	}
	if want := sceneIndexExact(config, wifiScrSceneName); want < 0 || pendingSceneIndex != want || transitionPhase != "fade-out" {
		t.Fatalf("the tap started phase %q to scene %d, want the Wi-Fi page (%d)", transitionPhase, pendingSceneIndex, want)
	}
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
	// Start from an empty slot: the producers block rather than drop, so a
	// leftover result would hang this test's own send instead of failing it.
	wifiDrainDiscarded()

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
