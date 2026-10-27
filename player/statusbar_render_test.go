package main

// Offscreen checks for the reworked chrome: the top bar owns the system status
// (clock, battery, notification bell, Apps pill), and the bottom strip is the
// controller hint bar rather than an OS taskbar with a second clock.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// TestStatusBarOwnsSystemCorner renders the real status bar over a synthetic
// scene and checks where the interactive pieces ended up.
func TestStatusBarOwnsSystemCorner(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	idx := findSceneIndex(config, "Settings")
	if idx < 0 {
		t.Fatal("Settings scene missing")
	}

	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	withScreen(t, probeW, probeH)
	ClearTextCache()

	prevScene := currentSceneIndex
	prevHome := homeLayoutActive
	prevBack, prevClock := sceneTitleBackRect, statusClockRect
	prevStart, prevBell := tb.startRect, nc.bellRect
	prevAccent := accentColor
	currentSceneIndex = idx
	homeLayoutActive = false
	sceneTitleBackRect, statusClockRect = sdl.Rect{}, sdl.Rect{}
	tb.startRect, nc.bellRect = sdl.Rect{}, sdl.Rect{}
	accentColor = ColorAccent
	t.Cleanup(func() {
		currentSceneIndex = prevScene
		homeLayoutActive = prevHome
		sceneTitleBackRect, statusClockRect = prevBack, prevClock
		tb.startRect, nc.bellRect = prevStart, prevBell
		accentColor = prevAccent
		ClearTextCache()
	})

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderStatusBar(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}

	// The Apps pill opens the app menu and lives in the left half of the bar.
	if tb.startRect.W <= 0 {
		t.Fatal("the Apps pill was not measured")
	}
	if tb.startRect.Y+tb.startRect.H > HomeTopBarH {
		t.Fatalf("Apps pill %+v is outside the top bar", tb.startRect)
	}
	if tb.startRect.X > screenWidth/2 {
		t.Fatalf("Apps pill X=%d is not on the left", tb.startRect.X)
	}

	// The notification bell owns the far-right corner.
	if nc.bellRect.W <= 0 {
		t.Fatal("the notification bell was not measured")
	}
	if nc.bellRect.X < screenWidth/2 {
		t.Fatalf("bell X=%d is not on the right", nc.bellRect.X)
	}
	if nc.bellRect.X+nc.bellRect.W > screenWidth-StatusBarMargin+1 {
		t.Fatalf("bell %+v overflows the right margin", nc.bellRect)
	}
	if nc.bellRect.Y+nc.bellRect.H > HomeTopBarH {
		t.Fatalf("bell %+v is outside the top bar", nc.bellRect)
	}
	bellPx := f.at(nc.bellRect.X+nc.bellRect.W/2, nc.bellRect.Y+nc.bellRect.H/2)
	if bellPx == ColorTopBar {
		t.Fatalf("bell area was not drawn (still the bar fill %v)", bellPx)
	}

	// The clock is still a focusable top-right target, left of the bell.
	if statusClockRect.W <= 0 {
		t.Fatal("the status clock was not measured")
	}
	if statusClockRect.X < screenWidth/2 {
		t.Fatalf("clock X=%d is not on the right", statusClockRect.X)
	}
	if statusClockRect.X+statusClockRect.W > nc.bellRect.X {
		t.Fatalf("clock %+v overlaps the bell %+v", statusClockRect, nc.bellRect)
	}
	if statusClockRect.Y+statusClockRect.H > HomeTopBarH {
		t.Fatalf("clock %+v is outside the top bar", statusClockRect)
	}
}

// TestBottomBarIsFooterButtons: the bottom strip reserves the footer height,
// which is what keeps scene action buttons clear of it, and the footer draws
// hint badges rather than a system tray.
func TestBottomBarIsFooterButtons(t *testing.T) {
	if got := bottomBarHeight("Settings"); got != HomeFooterH {
		t.Fatalf("bottom bar height = %d, want footer height %d", got, HomeFooterH)
	}
	withScreen(t, probeW, probeH)

	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	idx := findSceneIndex(config, "Settings")
	if idx < 0 {
		t.Fatal("Settings scene missing")
	}
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)

	prevScene := currentSceneIndex
	currentSceneIndex = idx
	t.Cleanup(func() { currentSceneIndex = prevScene })

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderFooter(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
	top := screenHeight - HomeFooterH
	if px := f.at(4, top+8); px == f.bg {
		t.Fatalf("footer band was not drawn at (%d,%d)", 4, top+8)
	}
	if px := f.at(4, top-6); px != f.bg {
		t.Fatalf("footer bled above its band: %v at y=%d", px, top-6)
	}
	// The far-right of the band is empty hint space now, not a clock.
	right := f.at(screenWidth-6, top+8)
	if right == f.bg {
		t.Fatal("footer band missing at the right edge")
	}
}

// TestThemeGalleryRendersWithoutOverflow draws the shipped gallery element and
// verifies cards stay inside the panel while the catalogue scrolls.
func TestThemeGalleryRendersWithoutOverflow(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	idx := findSceneIndex(config, "ThemePresets")
	if idx < 0 {
		t.Fatal("ThemePresets scene missing")
	}
	var element Element
	for _, e := range config.Scenes[idx].Elements {
		if e.Type == "themegallery" {
			element = e
		}
	}
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	withScreen(t, probeW, probeH)
	ClearTextCache()

	prevIndex, prevScroll := themeGalleryIndex, themeGalleryScroll
	themeGalleryIndex, themeGalleryScroll = 0, 0
	t.Cleanup(func() {
		themeGalleryIndex, themeGalleryScroll = prevIndex, prevScroll
		ClearTextCache()
	})

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderThemeGallery(renderer, config, element)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}

	cols, cardW, visibleRows := themeGalleryMetrics(element)
	if cols < 3 || visibleRows < 2 {
		t.Fatalf("grid is %dx%d, want at least 3 columns and 2 visible rows", cols, visibleRows)
	}
	// The first card is drawn inside the element.
	if f.at(element.X+12, element.Y+12) == f.bg {
		t.Fatal("first theme card was not drawn")
	}
	// Below the visible rows the panel background shows through, not a card.
	below := element.Y + int32(visibleRows)*(themeGalleryH+themeGalleryGap) + 4
	if below < element.Y+getElementHeight(element, 560) {
		if f.at(element.X+12, below) != f.bg {
			t.Fatalf("a card was drawn outside the visible rows at y=%d", below)
		}
	}
	// Cards never cross the element's right edge.
	lastColX := element.X + int32(cols-1)*(cardW+themeGalleryGap)
	if lastColX+cardW > element.X+getElementWidth(element, 1160)+1 {
		t.Fatalf("columns overflow the element: last card ends at %d", lastColX+cardW)
	}
}

// A crowded top bar must drop the network name before the battery: the level is
// the whole point of the status cluster.
func TestFitStatusPartsKeepsClockAndBattery(t *testing.T) {
	width := func(s string) int32 { return int32(len(s)) * 8 }
	gap := int32(8)
	parts := []string{"MyHomeNetwork", "21C / 12C", "12:40", "87%"}

	all := fitStatusParts(append([]string(nil), parts...), width, gap, 1000)
	if len(all) != 4 {
		t.Fatalf("a roomy bar trimmed the cluster to %v", all)
	}

	// Just wide enough for forecast + clock + battery.
	fitted := fitStatusParts(append([]string(nil), parts...), width, gap, 160)
	if len(fitted) != 3 || fitted[0] != "21C / 12C" || fitted[2] != "87%" {
		t.Fatalf("crowded bar kept %v, want the network name dropped and 87%% kept", fitted)
	}

	// The offline badge is not preferred over the clock and the level.
	offline := fitStatusParts([]string{"! Offline", "21C / 12C", "12:40", "87%"}, width, gap, 72)
	if len(offline) != 2 || offline[0] != "12:40" || offline[1] != "87%" {
		t.Fatalf("offline cluster kept %v, want clock and battery", offline)
	}

	// Even a bar too narrow for anything keeps one part rather than none.
	if got := fitStatusParts([]string{"87%"}, width, gap, 1); len(got) != 1 {
		t.Fatalf("the only part was dropped: %v", got)
	}
}
