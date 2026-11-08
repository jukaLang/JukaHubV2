package main

// The picker used to draw the connected row from the scan's RSSI, which on an
// image without /proc/net/wireless can be a driver's 0 — one bar that looks
// weak. The connected row now reads through `statusWiFiSignal`, the exact chain
// the corner meter uses, so the two cannot disagree. These tests pin the rule
// and the pixels: the connected row follows the meter, other rows stay on the
// scan.

import (
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func TestWiFiRowBarsFollowsTheMeterForTheConnectedRow(t *testing.T) {
	home := wifiNetwork{SSID: "HomeNet", Signal: 0, Active: true}
	cafe := wifiNetwork{SSID: "Cafe", Signal: -70} // two bars by the scan

	cases := []struct {
		name  string
		net   wifiNetwork
		meter func() int
		want  int
	}{
		{"connected takes the meter", home, func() int { return 4 }, 4},
		{"a /proc-less meter still wins", home, func() int { return 2 }, 2},
		{"meter unknown falls back to the scan", home, func() int { return statusWiFiUnknown }, 1},
		{"meter no-link falls back to the scan", home, func() int { return statusWiFiNoLink }, 1},
		{"no meter leaves the scan", home, nil, 1},
		{"other rows ignore the meter", cafe, func() int { return 4 }, 2},
		{"other rows with no meter", cafe, nil, 2},
	}
	for _, c := range cases {
		if got := wifiRowBars(c.net, c.meter); got != c.want {
			t.Errorf("%s: wifiRowBars = %d, want %d", c.name, got, c.want)
		}
	}

	// The meter must not even be asked for a row that is not the link.
	asked := false
	wifiRowBars(cafe, func() int { asked = true; return 4 })
	if asked {
		t.Error("the meter was read for a row that is not connected")
	}
}

// TestWiFiPickerDrawsTheMeterLevelOnTheConnectedRow is the visible half: with a
// scan level of 0 (which draws one bar) and a meter reading of four, the
// connected row's fourth bar is filled, while a non-connected row keeps its own
// two-bar reading.
func TestWiFiPickerDrawsTheMeterLevelOnTheConnectedRow(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := toolsHubTestNav(t)
	r := probeRenderer(t)
	currentSceneIndex = sceneIndexExact(config, wifiScrSceneName)
	prevSignal := statusWiFiSignal
	t.Cleanup(func() { statusWiFiSignal = prevSignal })

	wifiPanel = wifiPanelState{
		available: true,
		loadedAt:  time.Now(),
		status:    wifiStatus{State: wifiCompleteState, SSID: "HomeNet", Connected: true},
		networks: []wifiNetwork{
			{SSID: "HomeNet", Signal: 0, Secured: true, Saved: true, Active: true},
			{SSID: "Cafe", Signal: -70},
		},
	}

	// The fourth bar is the one a scan level of 0 leaves empty, so it is the
	// pixel that proves which source drew the row.
	draw := func(level int) []sdl.Color {
		statusWiFiSignal = func() int { return level }
		wifiScr.follow = true
		r.SetDrawColor(9, 14, 25, 255)
		r.Clear()
		renderWiFiScr(r, config)
		if len(wifiScr.rowRects) != 2 || wifiScr.rowRects[1].W == 0 {
			t.Fatalf("drew %d row targets, want both rows", len(wifiScr.rowRects))
		}
		buf := make([]byte, probeW*probeH*4)
		if err := r.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
			t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
		}
		frame := &probeFrame{buf: buf, bg: sdl.Color{R: 9, G: 14, B: 25, A: 255}}
		out := make([]sdl.Color, len(wifiScr.rowRects))
		for i, rect := range wifiScr.rowRects {
			// Bars start 20px into the row and step 7px; the fourth begins at
			// 41px and spans 6, so its middle is 44px in. The row's middle is
			// inside every bar.
			out[i] = frame.at(rect.X+44, rect.Y+rect.H/2)
		}
		return out
	}

	four := draw(4)
	one := draw(1)

	// Same row, same focus treatment, one draw apart: the only difference is
	// the meter, and the lit bar is green rather than the grey outline.
	if four[0] == one[0] {
		t.Errorf("the meter reading made no difference to the connected row: %v", four[0])
	}
	if int(four[0].G) <= int(one[0].G)+60 {
		t.Errorf("connected row did not light its fourth bar at meter 4: %v vs %v", four[0], one[0])
	}
	// The other row ignores the meter entirely.
	if four[1] != one[1] {
		t.Errorf("a non-connected row followed the meter: %v at 4, %v at 1", four[1], one[1])
	}
}
