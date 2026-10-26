package main

// statusbar_status_test.go covers the two gauges in the top-right system corner:
// the Wi-Fi meter (empty without a link, filled by signal strength) and the
// battery cell (green / yellow / red by charge). They are drawn on the home
// header and on every scene's status bar from the same helpers, so the tests
// check the shared primitives, the measured rects both bars record, and that the
// meter is the button that opens the Wi-Fi picker.

import (
	"testing"
	"time"
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

// The meter has to say which of three things is true: no Wi-Fi at all, a link
// whose strength nobody measured, or a measured strength. Four hollow bars were
// the "no Wi-Fi" state and read as a weak signal instead.
func TestDrawStatusSignalBarsEmptyWithoutLink(t *testing.T) {
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	warning := ColorWarning
	capture := func(level int) *probeFrame {
		renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
		renderer.Clear()
		drawStatusSignalBars(renderer, 40, 40, 21, 14, level, warning)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
			unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
			t.Skipf("ReadPixels: %v", err)
		}
		return f
	}

	greenish := func(c sdl.Color) bool { return c.G > 150 && c.G > c.R && c.G > c.B }
	lit := sdl.Rect{X: 36, Y: 34, W: 30, H: 24}

	empty := capture(statusWiFiNoLink)
	if n := countPixelsMatching(empty, lit, greenish); n != 0 {
		t.Fatalf("no-link meter lit %d green pixels, want an empty meter", n)
	}
	// The hollow bars are still drawn, so the corner shows a meter rather than
	// an empty gap that looks like a missing feature.
	if n := countPixelsMatching(empty, lit, func(c sdl.Color) bool { return c != f.bg }); n == 0 {
		t.Fatal("the no-link meter drew nothing at all")
	}
	// And the slash is what marks it as "none" rather than "weak": it crosses
	// the meter from the bottom-left to the top-right, in the warning ink.
	slash := func(c sdl.Color) bool { return c.R > 150 && c.R > c.B+40 && c.R > c.G+40 }
	if n := countPixelsMatching(empty, lit, slash); n < 6 {
		t.Fatalf("the no-link meter drew %d warning pixels, want a slash across the bars", n)
	}
	if n := countPixelsMatching(capture(4), lit, slash); n != 0 {
		t.Fatalf("a full meter drew %d warning pixels, want none", n)
	}

	// A live link of unknown strength is outlined in the accent and left
	// hollow: connected, with no claim about how strong the signal is.
	unknown := capture(statusWiFiUnknown)
	if n := countPixelsMatching(unknown, lit, greenish); n != 0 {
		t.Errorf("the unknown-strength meter lit %d green pixels", n)
	}
	accentish := func(c sdl.Color) bool {
		return int(c.R)+int(c.G)+int(c.B) > int(f.bg.R)+int(f.bg.G)+int(f.bg.B)+60
	}
	outlineInk := countPixelsMatching(unknown, lit, accentish)
	fullInk := countPixelsMatching(capture(4), lit, accentish)
	if outlineInk < 10 {
		t.Errorf("the unknown-strength meter drew %d outline pixels, want an outlined meter", outlineInk)
	}
	if outlineInk >= fullInk {
		t.Errorf("the outlined meter (%d px) claimed as much ink as a full one (%d px)", outlineInk, fullInk)
	}
	if diff := countPixelsMatching(unknown, lit, func(c sdl.Color) bool { return c != f.bg }); diff == 0 {
		t.Error("the unknown-strength meter drew nothing")
	}

	strong := capture(4)
	if n := countPixelsMatching(strong, lit, greenish); n < 10 {
		t.Fatalf("full meter lit only %d green pixels", n)
	}
}

// The chip's ink comes from the part, not from the bars: a device with no Wi-Fi
// link paints the slash as a warning while it is offline and as a quiet note
// while something else (Ethernet) keeps it online. Neither case may light a bar.
func TestWiFiChipInksTheNoLinkMark(t *testing.T) {
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	noop := func(string, sdl.Color, int32, int32) {}

	draw := func(part string, bars int) *probeFrame {
		pinStatusValues(t, 87, bars)
		renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
		renderer.Clear()
		drawStatusClusterPart(renderer, nil, part, 10, 10, noop)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
			unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
			t.Skipf("ReadPixels: %v", err)
		}
		return f
	}

	iconH := statusIconHeight(nil)
	box := sdl.Rect{X: 8, Y: 8, W: statusWiFiIconWidth(iconH) + 4, H: iconH + 4}
	greenish := func(c sdl.Color) bool { return c.G > 150 && c.G > c.R && c.G > c.B }
	warnish := func(c sdl.Color) bool { return c.R > 150 && c.R > c.B+40 && c.R > c.G+40 }

	offline := draw("! Offline", statusWiFiNoLink)
	if n := countPixelsMatching(offline, box, greenish); n != 0 {
		t.Errorf("the offline chip lit %d bars, want a crossed-out meter", n)
	}
	if n := countPixelsMatching(offline, box, warnish); n < 6 {
		t.Errorf("the offline chip drew %d warning pixels, want the slash", n)
	}

	// Online through something else, with no radio: the same mark, muted.
	onlineNoRadio := draw(statusWiFiMarker, statusWiFiNoLink)
	if n := countPixelsMatching(onlineNoRadio, box, greenish); n != 0 {
		t.Errorf("an online device without a radio lit %d bars", n)
	}
	if n := countPixelsMatching(onlineNoRadio, box, warnish); n != 0 {
		t.Errorf("the muted no-radio mark drew %d warning pixels", n)
	}
	if n := countPixelsMatching(onlineNoRadio, box, func(c sdl.Color) bool { return c != f.bg }); n == 0 {
		t.Error("the no-radio mark drew nothing")
	}

	// A measured link still lights the bars, so the mark is not the default.
	if n := countPixelsMatching(draw(statusWiFiMarker, 4), box, greenish); n < 10 {
		t.Errorf("a full-strength link lit only %d bar pixels", n)
	}
}

// TestParseWPASignalPollReadsReport pins the supplicant format, including the
// shapes that are not a measurement: FAIL, a missing RSSI and the 0 a
// disconnected supplicant prints. Negative values are all legal readings, even
// implausibly weak ones, so they are not filtered by "looks small".
func TestParseWPASignalPollReadsReport(t *testing.T) {
	sample, err := parseWPASignalPoll("Selected interface 'wlan0'\nRSSI=-57\nLINKSPEED=144\nNOISE=-256\nFREQUENCY=2437\n")
	if err != nil {
		t.Fatalf("typical report: %v", err)
	}
	if sample.RSSI != -57 || sample.LinkKbps != 144 || sample.Noise != -256 || sample.FreqMHz != 2437 {
		t.Fatalf("sample = %+v", sample)
	}

	for name, out := range map[string]string{
		"FAIL":      "FAIL\n",
		"no RSSI":   "LINKSPEED=144\n",
		"zero RSSI": "RSSI=0\nLINKSPEED=0\n",
		"nonsense":  "RSSI=strong\n",
		"empty":     "",
	} {
		if _, err := parseWPASignalPoll(out); err == nil {
			t.Errorf("%s: parse succeeded, want an error (no usable RSSI)", name)
		}
	}

	// A very weak but real link still parses; only non-negative values are
	// placeholders.
	if sample, err := parseWPASignalPoll("RSSI=-5\n"); err != nil || sample.RSSI != -5 {
		t.Fatalf("RSSI=-5 = %+v, %v", sample, err)
	}
	// Below -110 dBm is outside what any radio reports; treating it as a
	// reading would light the meter from a garbled line.
	if _, err := parseWPASignalPoll("RSSI=-120\n"); err == nil {
		t.Fatal("RSSI=-120 should not be a reading")
	}
}

// TestWiFiSignalPollAsksTheSupplicant covers the command itself: the interface
// is passed with -i, and a printed FAIL is an error even though wpa_cli exits 0.
func TestWiFiSignalPollAsksTheSupplicant(t *testing.T) {
	calls := stubWiFi(t, func(stdin, name string, args []string) (string, error) {
		if name != "wpa_cli" {
			t.Errorf("ran %q", name)
		}
		if wifiStubCommand(args) != "signal_poll" {
			return "OK\n", nil
		}
		return "RSSI=-48\nLINKSPEED=866\n", nil
	})
	t.Cleanup(resetWiFiSignalPollForTest)

	sample, err := wifiSignalPoll()
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if sample.RSSI != -48 || sample.LinkKbps != 866 {
		t.Fatalf("sample = %+v", sample)
	}
	found := false
	for _, call := range *calls {
		if len(call.args) >= 3 && call.args[0] == "-i" && call.args[1] == "wlan0" && call.args[2] == "signal_poll" {
			found = true
		}
	}
	if !found {
		t.Fatalf("signal_poll was not run against the detected interface: %+v", *calls)
	}

	// A supplicant that answers with FAIL is a failure, not a reading.
	stubWiFi(t, func(stdin, name string, args []string) (string, error) { return "FAIL\n", nil })
	if _, err := wifiSignalPoll(); err == nil {
		t.Fatal("FAIL from signal_poll must be an error")
	}
}

// TestWiFiSignalPollBarsFeedsTheMeter checks what the meter reads once a
// background poll has landed, that an unanswered poll stays "unknown" rather
// than becoming a weak-looking link, and that the caller never waits for the
// command.
func TestWiFiSignalPollBarsFeedsTheMeter(t *testing.T) {
	resetWiFiSignalPollForTest()
	t.Cleanup(resetWiFiSignalPollForTest)
	stubWiFi(t, func(stdin, name string, args []string) (string, error) {
		return "RSSI=-60\n", nil
	})

	// Pretend a poll just started, so this check does not race the background
	// goroutine it would otherwise spawn: the meter must answer immediately with
	// what it has, which is nothing yet.
	wifiPollMu.Lock()
	wifiPollKnocked = time.Now()
	wifiPollMu.Unlock()
	if bars, ok := wifiSignalPollBars(); ok || bars != statusWiFiUnknown {
		t.Fatalf("before any poll = %d, %v; want unknown, false", bars, ok)
	}

	if got := refreshWiFiSignalPoll(); got != 3 {
		t.Fatalf("refresh for -60 dBm = %d bars, want 3", got)
	}
	wifiPollMu.Lock()
	wifiPollBars = 3
	wifiPollMu.Unlock()
	if bars, ok := wifiSignalPollBars(); !ok || bars != 3 {
		t.Fatalf("cached poll = %d, %v; want 3, true", bars, ok)
	}

	// A failed poll clears the reading instead of keeping a stale level.
	stubWiFi(t, func(stdin, name string, args []string) (string, error) { return "FAIL\n", nil })
	if got := refreshWiFiSignalPoll(); got != statusWiFiUnknown {
		t.Fatalf("failed refresh = %d, want unknown", got)
	}
	wifiPollMu.Lock()
	wifiPollBars = refreshWiFiSignalPoll()
	wifiPollKnocked = time.Now()
	wifiPollMu.Unlock()
	if bars, ok := wifiSignalPollBars(); ok || bars != statusWiFiUnknown {
		t.Fatalf("after a failure = %d, %v; want unknown, false", bars, ok)
	}
}

// TestWiFiMeterFallsBackToSignalPoll is the chain in wifiMeterLevelFrom: the
// kernel's quality wins when it exists, the supplicant answers when it does
// not, and the honest sentinels stay for the cases where neither has a number.
func TestWiFiMeterFallsBackToSignalPoll(t *testing.T) {
	noPoll := func() (int, bool) {
		t.Helper()
		t.Fatal("the kernel's quality should have been used; the supplicant was asked instead")
		return statusWiFiUnknown, false
	}
	if got := wifiMeterLevelFrom(52, noPoll, func() bool { return true }); got != 4 {
		t.Fatalf("quality 52 = %d, want 4 bars", got)
	}

	polled := 0
	poll := func() (int, bool) {
		polled++
		return 3, true
	}
	if got := wifiMeterLevelFrom(-1, poll, func() bool { return true }); got != 3 {
		t.Fatalf("polled 3 bars = %d, want 3", got)
	}
	if polled != 1 {
		t.Fatalf("poll called %d times, want 1", polled)
	}

	// A listed interface with quality 0 is not a measurement: some drivers
	// report 0 while associated, which is the whole point of asking the
	// supplicant instead of ending at "no link".
	if got := wifiMeterLevelFrom(0, func() (int, bool) { return 2, true }, func() bool { return true }); got != 2 {
		t.Fatalf("quality 0 with a polled 2 bars = %d, want 2", got)
	}
	// With no other answer, 0 still means what it always meant: nothing on the link.
	if got := wifiMeterLevelFrom(0, func() (int, bool) { return statusWiFiUnknown, false }, func() bool { return false }); got != statusWiFiNoLink {
		t.Fatalf("quality 0 with no poll and no link = %d, want no link", got)
	}

	// The supplicant could not answer: the interface being up is unknown, and a
	// down interface is no link at all.
	if got := wifiMeterLevelFrom(-1, func() (int, bool) { return statusWiFiUnknown, false }, func() bool { return true }); got != statusWiFiUnknown {
		t.Fatalf("unanswered poll, link up = %d, want unknown", got)
	}
	if got := wifiMeterLevelFrom(-1, func() (int, bool) { return statusWiFiUnknown, false }, func() bool { return false }); got != statusWiFiNoLink {
		t.Fatalf("unanswered poll, link down = %d, want no link", got)
	}

	// A polled value outside 1..4 is not a bar count, so it is not reported as
	// a measurement.
	if bars, ok := wifiPollBarState(statusWiFiUnknown); ok || bars != statusWiFiUnknown {
		t.Fatalf("unknown sentinel = %d, %v", bars, ok)
	}
	if bars, ok := wifiPollBarState(statusWiFiNoLink); ok || bars != statusWiFiUnknown {
		t.Fatalf("no-link sentinel = %d, %v", bars, ok)
	}
	if bars, ok := wifiPollBarState(4); !ok || bars != 4 {
		t.Fatalf("4 bars = %d, %v", bars, ok)
	}
}

// TestWiFiMeterBarsForRSSI pins the dBm thresholds the meter uses for the
// supplicant path; a non-measurement stays unknown instead of one bar.
func TestWiFiMeterBarsForRSSI(t *testing.T) {
	cases := map[int]int{
		-45: 4, -55: 4, -60: 3, -67: 3, -70: 2, -78: 2, -85: 1, -100: 1,
		0: statusWiFiUnknown, 5: statusWiFiUnknown, -120: statusWiFiUnknown,
	}
	for rssi, want := range cases {
		if got := wifiMeterBarsForRSSI(rssi); got != want {
			t.Errorf("RSSI %d -> %d, want %d", rssi, got, want)
		}
	}
}

// wifiMeterLevel is the one place that decides what the kernel's answer means,
// so the "no link" and "nobody measured it" states cannot drift apart.
func TestWiFiMeterLevelStates(t *testing.T) {
	cases := []struct {
		name    string
		quality int
		linkUp  bool
		want    int
	}{
		{"strong link", 52, true, 4},
		{"weak link", 22, true, 2},
		{"measured zero", 0, true, statusWiFiNoLink},
		{"no proc, interface up", -1, true, statusWiFiUnknown},
		{"no proc, interface down", -1, false, statusWiFiNoLink},
		{"no radio", -1, false, statusWiFiNoLink},
	}
	for _, tc := range cases {
		if got := wifiMeterLevel(tc.quality, tc.linkUp); got != tc.want {
			t.Errorf("%s: quality %d up=%v -> %d, want %d", tc.name, tc.quality, tc.linkUp, got, tc.want)
		}
	}
	// The two sentinels are distinct, and neither is a bar count.
	if statusWiFiNoLink == statusWiFiUnknown {
		t.Fatal("no-link and unknown share a value")
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
