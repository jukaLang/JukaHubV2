package main

// statusbar_status_test.go covers the two gauges in the top-right system corner:
// the Wi-Fi meter (empty without a link, filled by signal strength) and the
// battery cell (green / yellow / red by charge). They are drawn on the home
// header and on every scene's status bar from the same helpers, so the tests
// check the shared primitives, the measured rects both bars record, and that the
// meter is the button that opens the Wi-Fi picker.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// pinStatusValues overrides the live battery level and Wi-Fi bars for one test.
func pinStatusValues(t *testing.T, pct, bars int) {
	t.Helper()
	prevPct, prevBars := statusBatteryPercent, statusWiFiSignal
	statusBatteryPercent = func() int { return pct }
	statusWiFiSignal = func() int { return bars }
	t.Cleanup(func() {
		statusBatteryPercent = prevPct
		statusWiFiSignal = prevBars
	})
}

func TestStatusBatteryColorIsGreenYellowRed(t *testing.T) {
	green := statusBatteryColor(100)
	yellow := statusBatteryColor(20)
	red := statusBatteryColor(19)

	if green != statusBatteryColor(50) {
		t.Fatalf("50%% is not green: %+v", statusBatteryColor(50))
	}
	if green.G <= green.R || green.G <= green.B {
		t.Fatalf("full charge is not green: %+v", green)
	}
	if statusBatteryColor(49) != yellow {
		t.Fatalf("49%% is not yellow: %+v", statusBatteryColor(49))
	}
	if yellow.R <= yellow.B || yellow.G <= yellow.B {
		t.Fatalf("mid charge is not yellow: %+v", yellow)
	}
	if statusBatteryColor(0) != red {
		t.Fatalf("empty is not red: %+v", statusBatteryColor(0))
	}
	if red.R <= red.G || red.R <= red.B {
		t.Fatalf("low charge is not red: %+v", red)
	}
	if len(map[sdl.Color]bool{green: true, yellow: true, red: true}) != 3 {
		t.Fatal("the three levels share a colour")
	}
}

func TestStatusBatteryPercentOfReadsTheClusterPart(t *testing.T) {
	for _, part := range []string{"0%", "87%", "100%"} {
		if _, ok := statusBatteryPercentOf(part); !ok {
			t.Fatalf("%q was not recognised as the battery part", part)
		}
	}
	if pct, ok := statusBatteryPercentOf(" 42% "); !ok || pct != 42 {
		t.Fatalf("spaced part = %d, %v; want 42, true", pct, ok)
	}
	for _, part := range []string{"12:40", "21C / 12C", "WiFi", "! Offline", "101%", "-5%"} {
		if _, ok := statusBatteryPercentOf(part); ok {
			t.Fatalf("%q was mistaken for the battery part", part)
		}
	}
}

func TestParseWirelessLinkQualityReadsKernelTable(t *testing.T) {
	const table = `Inter-| sta-|   Quality        |   Discarded packets               | Missed | WE
 face | tus | link level noise |  nwid  crypt   frag  retry   misc | beacon | 22
 wlan0: 0000   52.  -58.  -256        0      0      0      0      0        0
`
	if got := parseWirelessLinkQuality(table); got != 52 {
		t.Fatalf("quality = %d, want 52", got)
	}
	// The strongest interface wins when a second radio is present.
	two := table + " wlan1: 0000   31.  -71.  -256        0      0      0      0      0        0\n"
	if got := parseWirelessLinkQuality(two); got != 52 {
		t.Fatalf("two radios: quality = %d, want the stronger 52", got)
	}
	if got := parseWirelessLinkQuality(""); got != -1 {
		t.Fatalf("empty table = %d, want -1", got)
	}
	if got := parseWirelessLinkQuality("garbage\nwlan0: nope\n"); got != -1 {
		t.Fatalf("garbage table = %d, want -1", got)
	}
}

func TestWiFiBarsForQuality(t *testing.T) {
	cases := map[int]int{-1: 0, 0: 0, 5: 1, 19: 1, 20: 2, 34: 2, 35: 3, 49: 3, 50: 4, 70: 4}
	for q, want := range cases {
		if got := wifiBarsForQuality(q); got != want {
			t.Errorf("quality %d -> %d bars, want %d", q, got, want)
		}
	}
}

func TestStatusClusterPartWidthMeasuresGauges(t *testing.T) {
	// With no font the text halves measure zero, so the widths are the icon
	// geometry alone: four ascending bars, and a cell plus its terminal nub.
	iconH := statusIconHeight(nil)
	wifi := statusClusterPartWidth(nil, statusWiFiMarker)
	if want := statusWiFiIconWidth(iconH); wifi != want {
		t.Fatalf("online meter width = %d, want %d", wifi, want)
	}
	offline := statusClusterPartWidth(nil, "! Offline")
	if offline <= wifi {
		t.Fatalf("offline meter width = %d, want more than the bare meter %d", offline, wifi)
	}
	battery := statusClusterPartWidth(nil, "87%")
	if want := statusBatteryIconWidth(iconH) + statusBatteryNubWidth(iconH) + 1 + statusChipTextGap; battery != want {
		t.Fatalf("battery width = %d, want %d", battery, want)
	}
	if battery <= statusWiFiIconWidth(iconH) {
		t.Fatalf("battery width %d is smaller than the meter", battery)
	}
	if got := statusClusterPartWidth(nil, "12:40"); got != 0 {
		t.Fatalf("plain text width without a font = %d, want 0", got)
	}
}

// The drawing advance must equal the measured width, or the clock and the bell
// drift every frame.
func TestDrawStatusClusterPartMatchesMeasure(t *testing.T) {
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	pinStatusValues(t, 87, 3)

	noop := func(string, sdl.Color, int32, int32) {}
	for _, part := range []string{statusWiFiMarker, "! Offline", "87%", "12:40"} {
		want := statusClusterPartWidth(nil, part)
		if got := drawStatusClusterPart(renderer, nil, part, 10, 10, noop); got != want {
			t.Errorf("%q drew %dpx but measured %dpx", part, got, want)
		}
	}
}

func TestDrawStatusSignalBarsEmptyWithoutLink(t *testing.T) {
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	capture := func(level int) *probeFrame {
		renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
		renderer.Clear()
		drawStatusSignalBars(renderer, 40, 40, 21, 14, level)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
			unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
			t.Skipf("ReadPixels: %v", err)
		}
		return f
	}

	greenish := func(c sdl.Color) bool { return c.G > 150 && c.G > c.R && c.G > c.B }
	lit := sdl.Rect{X: 36, Y: 34, W: 30, H: 24}

	empty := capture(0)
	if n := countPixelsMatching(empty, lit, greenish); n != 0 {
		t.Fatalf("no-link meter lit %d green pixels, want an empty meter", n)
	}
	// The hollow bars are still drawn, so the corner shows a meter rather than
	// an empty gap that looks like a missing feature.
	if n := countPixelsMatching(empty, lit, func(c sdl.Color) bool { return c != f.bg }); n == 0 {
		t.Fatal("the no-link meter drew nothing at all")
	}

	strong := capture(4)
	if n := countPixelsMatching(strong, lit, greenish); n < 10 {
		t.Fatalf("full meter lit only %d green pixels", n)
	}
}

// statusBarProbe renders the real status bar over the probe canvas and returns
// the pixels.
func statusBarProbe(t *testing.T, config *Config) *probeFrame {
	t.Helper()
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	withScreen(t, probeW, probeH)
	ClearTextCache()

	prevScene, prevHome := currentSceneIndex, homeLayoutActive
	prevAccent, prevWiFi := accentColor, statusWiFiRect
	currentSceneIndex = findSceneIndex(config, "Settings")
	homeLayoutActive = false
	accentColor = ColorAccent
	t.Cleanup(func() {
		currentSceneIndex, homeLayoutActive = prevScene, prevHome
		accentColor, statusWiFiRect = prevAccent, prevWiFi
		ClearTextCache()
	})

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderStatusBar(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
		unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels: %v", err)
	}
	return f
}

// statusBatteryBand is the strip between the status clock and the bell, where
// the battery gauge and its percentage are drawn.
func statusBatteryBand() sdl.Rect {
	left := statusClockRect.X + statusClockRect.W
	right := screenWidth - StatusBarMargin
	if nc.bellRect.W > 0 {
		right = nc.bellRect.X
	}
	if left <= 0 || right <= left {
		return sdl.Rect{}
	}
	return sdl.Rect{X: left, Y: 0, W: right - left, H: HomeTopBarH}
}

func TestStatusBarDrawsBatteryGaugeAndWiFiMeter(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	pinStatusValues(t, 12, 3)
	f := statusBarProbe(t, config)

	if statusWiFiRect.W <= 0 {
		t.Fatal("the Wi-Fi meter was not measured")
	}
	if statusWiFiRect.X < screenWidth/2 {
		t.Fatalf("meter X=%d is not in the right-hand cluster", statusWiFiRect.X)
	}
	if statusWiFiRect.Y+statusWiFiRect.H > HomeTopBarH {
		t.Fatalf("meter %+v is outside the top bar", statusWiFiRect)
	}
	if statusClockRect.W > 0 && statusWiFiRect.X > statusClockRect.X {
		t.Fatalf("meter %+v should lead the clock %+v", statusWiFiRect, statusClockRect)
	}

	// Three bars are filled, in the meter's own green.
	greenish := func(c sdl.Color) bool { return c.G > 150 && c.G > c.R && c.G > c.B }
	if n := countPixelsMatching(f, statusWiFiRect, greenish); n < 6 {
		t.Fatalf("signal bars lit only %d pixels at level 3", n)
	}

	// The battery is at 12%, so its cell and percentage are red in the band
	// between the clock and the notification bell (the bell has its own dot).
	reddish := func(c sdl.Color) bool { return c.R > 170 && c.R > c.G+60 && c.R > c.B+60 }
	corner := statusBatteryBand()
	if corner.W <= 0 {
		t.Fatalf("no band between the clock and the bell: clock %+v bell %+v", statusClockRect, nc.bellRect)
	}
	if n := countPixelsMatching(f, corner, reddish); n < 8 {
		t.Fatalf("low battery drew only %d red pixels in %+v", n, corner)
	}
}

// A healthy battery is green, not red: the colour has to follow the level rather
// than being a fixed accent.
func TestStatusBarBatteryTurnsGreenWhenFull(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	pinStatusValues(t, 92, 4)
	f := statusBarProbe(t, config)

	corner := statusBatteryBand()
	if corner.W <= 0 {
		t.Fatalf("no band between the clock and the bell: clock %+v bell %+v", statusClockRect, nc.bellRect)
	}
	green := func(c sdl.Color) bool { return c.G > 150 && c.G > c.R+30 && c.G > c.B+30 }
	red := func(c sdl.Color) bool { return c.R > 170 && c.R > c.G+60 && c.R > c.B+60 }
	if n := countPixelsMatching(f, corner, green); n < 8 {
		t.Fatalf("full battery drew only %d green pixels", n)
	}
	if n := countPixelsMatching(f, corner, red); n > 0 {
		t.Fatalf("full battery still drew %d red pixels", n)
	}
}

// The home header must show the same two gauges, otherwise the corner changes
// when the user leaves Home.
func TestHomeHeaderDrawsSystemGauges(t *testing.T) {
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	pinStatusValues(t, 12, 3)
	withScreen(t, probeW, probeH)
	ClearTextCache()
	prevWiFi := statusWiFiRect
	t.Cleanup(func() { statusWiFiRect = prevWiFi; ClearTextCache() })

	layout := NewHomeLayout(probeW, probeH)
	data := HomeViewData{
		Greeting:    "Good afternoon",
		Subtitle:    "What do you want to do?",
		Version:     "v0.4.0",
		StatusParts: []string{statusWiFiMarker, "12C / 24C", "12:40", "12%"},
	}
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	if err := layout.Render(renderer, loadHomeFonts(), data); err != nil {
		t.Fatalf("render: %v", err)
	}
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
		unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels: %v", err)
	}

	if statusWiFiRect.W <= 0 {
		t.Fatal("the home header did not measure the Wi-Fi meter")
	}
	if statusWiFiRect.X+statusWiFiRect.W > probeW {
		t.Fatalf("meter %+v overflows the header", statusWiFiRect)
	}
	greenish := func(c sdl.Color) bool { return c.G > 150 && c.G > c.R && c.G > c.B }
	if n := countPixelsMatching(f, statusWiFiRect, greenish); n < 6 {
		t.Fatalf("home signal bars lit only %d pixels", n)
	}
	reddish := func(c sdl.Color) bool { return c.R > 170 && c.R > c.G+60 && c.R > c.B+60 }
	if n := countPixelsMatching(f, statusWiFiRect, reddish); n != 0 {
		t.Fatalf("the 12%% battery painted %d red pixels over the meter", n)
	}
	if n := countPixelsMatching(f, sdl.Rect{X: probeW / 2, Y: 0, W: probeW / 2, H: HomeTopBarH}, reddish); n < 8 {
		t.Fatalf("low battery drew only %d red pixels in the home header", n)
	}
}

// headerStatusParts always leads with the network slot, so the meter has a part
// to hang on whether or not the device is online.
func TestHeaderStatusPartsAlwaysHasNetworkSlot(t *testing.T) {
	pinStatusValues(t, 64, 2)
	parts := headerStatusParts(&Config{})
	if len(parts) < 3 {
		t.Fatalf("cluster = %v, want network, clock and battery", parts)
	}
	if !isWiFiStatusPart(parts[0]) {
		t.Fatalf("first part %q is not the network slot", parts[0])
	}
	last := parts[len(parts)-1]
	if !isBatteryStatusPart(last) {
		t.Fatalf("last part %q is not the battery reading", last)
	}
	if pct, _ := statusBatteryPercentOf(last); pct != 64 {
		t.Fatalf("battery part = %q, want the pinned 64%%", last)
	}
}

func TestWiFiMeterClickOpensPicker(t *testing.T) {
	// The picker scans on open and waits until scan_results changes, so the
	// stub answers with an empty table first and one network after "scan".
	scanned := false
	stubWiFi(t, func(stdin, name string, args []string) (string, error) {
		switch wifiStubCommand(args) {
		case "scan":
			scanned = true
			return "OK\n", nil
		case "scan_results":
			if scanned {
				return "bssid / frequency / signal level / flags / ssid\nAA:BB:CC:DD:EE:FF\t2412\t-58\t[WPA2-PSK-CCMP][ESS]\tHomeNet\n", nil
			}
			return "bssid / frequency / signal level / flags / ssid\n", nil
		case "status":
			return "wpa_state=COMPLETED\nssid=HomeNet\nip_address=192.168.1.20\n", nil
		}
		return "OK\n", nil
	})
	prevPanel, prevRects := wifiPanel, wifiRowRects
	prevRect := statusWiFiRect
	t.Cleanup(func() {
		wifiPanel, wifiRowRects = prevPanel, prevRects
		statusWiFiRect = prevRect
	})

	wifiPanel = wifiPanelState{}
	statusWiFiRect = sdl.Rect{X: 900, Y: 8, W: 40, H: 30}

	// A click outside the meter stays unhandled.
	if handleChromeBarClick(500, 500, &Config{}) {
		t.Fatal("a click far from the status cluster was consumed")
	}
	if !handleChromeBarClick(statusWiFiRect.X+4, statusWiFiRect.Y+4, &Config{}) {
		t.Fatal("a click on the Wi-Fi meter was not handled")
	}
	if !wifiPanel.open {
		t.Fatal("the Wi-Fi picker did not open from the meter")
	}
	waitForWiFiIdle(t)
	closeWiFiPanel()
}
