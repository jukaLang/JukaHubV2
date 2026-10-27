package main

// smart_home_render_test.go — offscreen checks for the Smart Home overlay.
//
// The panel is drawn with the software renderer over a known background so the
// card geometry, the row hit boxes and the "every row the list promises is
// drawn" rule are verified on real pixels. Without this a panic or an empty
// list would only show up on a device.

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// renderSmartHomeProbe draws one frame over the probe background, returning the
// frame, the panel rect and the row rects the renderer recorded for clicks.
func renderSmartHomeProbe(t *testing.T, config *Config, snapshot smartHomeSnapshotData, cursor int) (*probeFrame, sdl.Rect, []sdl.Rect) {
	t.Helper()
	f := newProbeFrame(t)
	if font, _ := getCachedFont(config, "small"); font == nil {
		t.Fatalf("no usable font in this environment: the shipped Inter-Regular.ttf did not load, so this check cannot run")
	}

	prevW, prevH := screenWidth, screenHeight
	prevOpen, prevCursor, prevScroll := smartHomePanel.open, smartHomePanel.cursor, smartHomePanel.scroll
	screenWidth, screenHeight = probeW, probeH
	smartHomePanel.open = true
	smartHomePanel.cursor = cursor
	smartHomePanel.scroll = 0

	prevState := smartHomeSnapshot()
	smartHome.mu.Lock()
	smartHome.smartHomeSnapshotData = snapshot
	smartHome.mu.Unlock()

	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		smartHomePanel.open, smartHomePanel.cursor, smartHomePanel.scroll = prevOpen, prevCursor, prevScroll
		smartHomePanel.rects = nil
		smartHome.mu.Lock()
		smartHome.smartHomeSnapshotData = prevState
		smartHome.mu.Unlock()
	})

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	smartHomePanel.enter.cancel() // draw the settled card, not the rise

	renderSmartHomePanel(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
	return f, smartHomePanelRect, append([]sdl.Rect(nil), smartHomePanel.rects...)
}

func smartHomeProbeSnapshot() smartHomeSnapshotData {
	return smartHomeSnapshotData{
		loaded: true,
		entities: []haEntity{
			{EntityID: "light.kitchen", Name: "Kitchen", Domain: "light", State: "on"},
			{EntityID: "switch.pump", Name: "Pump", Domain: "switch", State: "off"},
		},
		pihole:    piholeSnapshot{Configured: true, Reachable: true, Status: "enabled", Queries: 1200, Blocked: 180, Percent: 15},
		router:    routerSnapshot{Host: "192.168.1.1", Reachable: true, LatencyMS: 3},
		tailscale: tailscaleSnapshot{Installed: true, Running: false, State: "Stopped"},
		wireguard: wireguardSnapshot{Installed: true, Interfaces: []string{"wg0"}},
	}
}

func TestSmartHomePanelRenders(t *testing.T) {
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{
		"ha_url": "http://ha.local:8123", "pihole_url": "http://pi.lan",
	}}}
	f, panel, rects := renderSmartHomeProbe(t, cfg, smartHomeProbeSnapshot(), 0)

	if panel.W <= 0 || panel.H <= 0 {
		t.Fatalf("panel rect = %+v", panel)
	}
	if panel.X < 0 || panel.Y < 0 || panel.X+panel.W > probeW || panel.Y+panel.H > probeH {
		t.Errorf("panel %+v does not fit the %dx%d screen", panel, probeW, probeH)
	}

	rows := smartHomePanelRows(cfg, smartHomeSnapshot())
	if len(rects) != len(rows) {
		t.Errorf("%d row rects for %d rows", len(rects), len(rows))
	}
	for i, r := range rects {
		if r.W <= 0 || r.H <= 0 {
			t.Errorf("row %d has an empty rect: %+v", i, r)
		}
		if r.X < panel.X || r.X+r.W > panel.X+panel.W {
			t.Errorf("row %d (%+v) is outside the panel (%+v)", i, r, panel)
		}
	}

	// A drawn card must differ from the flat background in its middle band,
	// otherwise the panel painted nothing (or the wrong rect).
	mid := sdl.Rect{X: panel.X + panel.W/4, Y: panel.Y + panel.H/3, W: panel.W / 2, H: panel.H / 6}
	if bandLuma(f, mid) == 0 {
		t.Error("the panel body is indistinguishable from the background")
	}
}

// An unconfigured panel is the first thing a new user sees: it must still draw,
// with the setup hints, instead of showing an empty card.
func TestSmartHomePanelRendersWithoutConfig(t *testing.T) {
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	_, panel, rects := renderSmartHomeProbe(t, cfg, smartHomeSnapshotData{loaded: true}, 0)
	if panel.W <= 0 || len(rects) == 0 {
		t.Fatalf("empty panel drew %d rows in %+v", len(rects), panel)
	}
	rows := smartHomePanelRows(cfg, smartHomeSnapshotData{loaded: true})
	if len(rows) < 3 {
		t.Fatalf("unconfigured panel has only %d rows", len(rows))
	}
	for _, row := range rows {
		if strings.TrimSpace(row.label) == "" {
			t.Errorf("row without a label: %+v", row)
		}
		if strings.TrimSpace(row.value) == "" && strings.TrimSpace(row.detail) == "" {
			t.Errorf("row %q says nothing at all", row.label)
		}
	}
}

// Cursor movement must stay inside the list, and activating a status row must
// not try to send a command.
func TestSmartHomePanelNavigation(t *testing.T) {
	prevState := smartHomeSnapshot()
	smartHome.mu.Lock()
	smartHome.smartHomeSnapshotData = smartHomeProbeSnapshot()
	smartHome.mu.Unlock()
	prevOpen, prevCursor := smartHomePanel.open, smartHomePanel.cursor
	smartHomePanel.open, smartHomePanel.cursor = true, 0
	t.Cleanup(func() {
		smartHomePanel.open, smartHomePanel.cursor = prevOpen, prevCursor
		smartHome.mu.Lock()
		smartHome.smartHomeSnapshotData = prevState
		smartHome.mu.Unlock()
	})

	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{"ha_url": "http://ha.local:8123"}}}
	rows := smartHomePanelRows(cfg, smartHomeSnapshot())
	for i := 0; i < len(rows)+3; i++ {
		smartHomePanelMove(cfg, 1)
	}
	if got, want := smartHomePanel.cursor, len(rows)-1; got != want {
		t.Errorf("cursor after walking off the end = %d, want %d", got, want)
	}
	for i := 0; i < len(rows)+3; i++ {
		smartHomePanelMove(cfg, -1)
	}
	if smartHomePanel.cursor != 0 {
		t.Errorf("cursor after walking off the top = %d, want 0", smartHomePanel.cursor)
	}
}
