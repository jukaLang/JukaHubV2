package main

// The system corner of the top bar: a Wi-Fi meter and a battery gauge.
//
// The right-hand cluster is still the shared []string from headerStatusParts,
// so layout, trimming (fitStatusParts) and the clock/weather hit rects keep
// working unchanged. What changes here is how two of those parts are painted:
// the network slot becomes signal bars (empty when there is no link) and the
// battery part grows a gauge coloured green / yellow / red by the charge left.
//
// Both the home header and every scene's status bar call the helpers below with
// their own text drawing function, so the icons are pixel-identical on every
// screen and cannot drift apart.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// statusWiFiMarker is the network part headerStatusParts emits while the
	// device is online. The meter replaces the word itself.
	statusWiFiMarker = "WiFi"

	// statusChipTextGap separates a gauge from its label and the bar gap.
	statusChipTextGap = int32(6)

	// statusWiFiRefresh bounds how often /proc/net/wireless is read.
	statusWiFiRefresh = 4 * time.Second
)

// statusTextDrawer draws one run of text with the caller's own renderer (the
// scene status bar and the home header keep separate text caches).
type statusTextDrawer func(text string, color sdl.Color, x, y int32)

// statusWiFiRect is the meter's hit area, measured on every frame by whichever
// header is on screen. A click or tap on it opens the Wi-Fi picker.
var statusWiFiRect sdl.Rect

// statusBatteryPercent supplies the level in the shared status cluster. It is a
// variable so the render tests can pin a level instead of depending on the host
// machine's battery (the real reader returns -1 when there is none).
var statusBatteryPercent = getBatteryPercent

// statusWiFiSignal returns the live 0..4 signal level, 0 when there is no link.
// A variable for the same reason: tests pin the meter instead of the kernel.
var statusWiFiSignal = readWiFiSignalLevel

// statusBatteryColor is the gauge colour: green with charge to spare, yellow
// when the user should start thinking about a cable, red when it is nearly gone.
func statusBatteryColor(pct int) sdl.Color {
	switch {
	case pct >= 50:
		return sdl.Color{R: 92, G: 200, B: 140, A: 255}
	case pct >= 20:
		return sdl.Color{R: 236, G: 191, B: 82, A: 255}
	default:
		return sdl.Color{R: 232, G: 96, B: 88, A: 255}
	}
}

// statusBatteryLabel is the text half of the gauge, matching the part text that
// headerStatusParts puts in the cluster.
func statusBatteryLabel(pct int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return strconv.Itoa(pct) + "%"
}

// statusBatteryPercentOf reads the level out of a status part like "87%".
func statusBatteryPercentOf(part string) (int, bool) {
	s := strings.TrimSpace(part)
	if !strings.HasSuffix(s, "%") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(s, "%"))
	if err != nil || n < 0 || n > 100 {
		return 0, false
	}
	return n, true
}

// isBatteryStatusPart reports whether the part is the battery reading. The
// clock ("12:40"), the forecast ("21C / 12C") and SSIDs cannot match this shape,
// the same way isClockStatusPart and isWeatherStatusPart tell their parts apart.
func isBatteryStatusPart(part string) bool {
	_, ok := statusBatteryPercentOf(part)
	return ok
}

// isWiFiStatusPart reports whether the part is the leading network slot: the
// online marker whose word the meter replaces, or the offline badge, which
// keeps its warning text beside an empty meter.
func isWiFiStatusPart(part string) bool {
	return part == statusWiFiMarker || strings.HasPrefix(part, "! Offline")
}

// statusIconHeight derives the gauge height from the status font, so the icons
// scale with the bar (and with the home header on a larger screen) instead of
// being a fixed pixel size that looks wrong on one of them.
func statusIconHeight(font *ttf.Font) int32 {
	h := int32(14)
	if font != nil {
		h = int32(font.Height()) * 3 / 4
	}
	if h < 9 {
		h = 9
	}
	if h > 22 {
		h = 22
	}
	return h
}

// statusWidth is the text half's width, zero without a font.
func statusWidth(font *ttf.Font, text string) int32 {
	if font == nil || text == "" {
		return 0
	}
	w, _, _ := font.SizeUTF8(text)
	return int32(w)
}

// statusWiFiIconWidth is the meter's width for a given icon height.
func statusWiFiIconWidth(iconH int32) int32 {
	w := iconH * 3 / 2
	if w < 12 {
		w = 12
	}
	return w
}

// statusBatteryIconWidth is the gauge body's width (the terminal nub is extra).
func statusBatteryIconWidth(iconH int32) int32 {
	w := iconH * 3 / 2
	if w < 12 {
		w = 12
	}
	return w
}

// statusClusterPartWidth measures one part as it will be drawn. fitStatusParts
// uses it, so a gauged part is never clipped by the bar's trimming.
func statusClusterPartWidth(font *ttf.Font, part string) int32 {
	iconH := statusIconHeight(font)
	switch {
	case isWiFiStatusPart(part):
		w := statusWiFiIconWidth(iconH)
		if part != statusWiFiMarker {
			w += statusChipTextGap + statusWidth(font, part)
		}
		return w
	case isBatteryStatusPart(part):
		pct, _ := statusBatteryPercentOf(part)
		return statusBatteryIconWidth(iconH) + statusBatteryNubWidth(iconH) + 1 +
			statusChipTextGap + statusWidth(font, statusBatteryLabel(pct))
	}
	return statusWidth(font, part)
}

// statusBatteryNubWidth is the little terminal cap on the right of the gauge.
func statusBatteryNubWidth(iconH int32) int32 {
	w := iconH / 6
	if w < 2 {
		w = 2
	}
	return w
}

// drawStatusSignalBars draws four ascending bars filled to level (0..4), all
// hollow when level is 0 — the "no Wi-Fi" state.
func drawStatusSignalBars(renderer *sdl.Renderer, x, y, w, h int32, level int) {
	if renderer == nil || w <= 0 || h <= 0 {
		return
	}
	if level < 0 {
		level = 0
	}
	if level > 4 {
		level = 4
	}
	gap := w / 10
	if gap < 1 {
		gap = 1
	}
	barW := (w - 3*gap) / 4
	if barW < 2 {
		barW = 2
	}
	col := wifiSignalColor(level)
	empty := sdl.Color{R: 78, G: 86, B: 104, A: 165}
	baseY := y + h
	for i := int32(0); i < 4; i++ {
		bh := h * (i + 1) / 4
		if bh < 2 {
			bh = 2
		}
		c := col
		if level == 0 || int(i) >= level {
			c = empty
		}
		fillRoundedRect(renderer, x+i*(barW+gap), baseY-bh, barW, bh, 1, c)
	}
}

// drawStatusBatteryGauge draws the cell and its charge, returning the width used.
func drawStatusBatteryGauge(renderer *sdl.Renderer, x, y, iconH int32, pct int, col sdl.Color) int32 {
	if renderer == nil || iconH <= 0 {
		return 0
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	w := statusBatteryIconWidth(iconH)
	nub := statusBatteryNubWidth(iconH)
	strokeRoundedRect(renderer, x, y, w, iconH, 2, 1, WithAlpha(col, 200))
	fillRoundedRect(renderer, x+w+1, y+iconH/4, nub, iconH/2, 1, WithAlpha(col, 200))
	inner := w - 4
	if inner > 0 {
		fw := inner * int32(pct) / 100
		if pct > 0 && fw < 2 {
			fw = 2
		}
		if fw > inner {
			fw = inner
		}
		if fw > 0 {
			fillRoundedRect(renderer, x+2, y+2, fw, iconH-4, 1, WithAlpha(col, 235))
		}
	}
	return w + nub + 1
}

// drawStatusWiFiChip paints the network slot: signal bars, plus the offline
// warning text when there is no link. It returns the width it used.
func drawStatusWiFiChip(renderer *sdl.Renderer, font *ttf.Font, part string, x, textTop int32, draw statusTextDrawer) int32 {
	iconH := statusIconHeight(font)
	iconW := statusWiFiIconWidth(iconH)
	textH := iconH
	if font != nil {
		textH = int32(font.Height())
	}
	iconY := textTop + (textH-iconH)/2
	level := 0
	offline := part != statusWiFiMarker
	if !offline {
		level = statusWiFiSignal()
	}
	drawStatusSignalBars(renderer, x, iconY, iconW, iconH, level)
	if offline && draw != nil {
		draw(part, ColorWarning, x+iconW+statusChipTextGap, textTop)
	}
	return statusClusterPartWidth(font, part)
}

// drawStatusBatteryChip paints the battery part: gauge plus percentage, both in
// the level's colour so a dying battery is visible in the corner of the screen.
func drawStatusBatteryChip(renderer *sdl.Renderer, font *ttf.Font, part string, x, textTop int32, draw statusTextDrawer) int32 {
	pct, ok := statusBatteryPercentOf(part)
	if !ok {
		return 0
	}
	iconH := statusIconHeight(font)
	textH := iconH
	if font != nil {
		textH = int32(font.Height())
	}
	col := statusBatteryColor(pct)
	gaugeW := drawStatusBatteryGauge(renderer, x, textTop+(textH-iconH)/2, iconH, pct, col)
	if draw != nil {
		draw(statusBatteryLabel(pct), col, x+gaugeW+statusChipTextGap, textTop)
	}
	return statusClusterPartWidth(font, part)
}

// isStatusIconPart reports whether the part is drawn by one of the gauges
// rather than as plain text.
func isStatusIconPart(part string) bool {
	return isWiFiStatusPart(part) || isBatteryStatusPart(part)
}

// drawStatusClusterPart paints one part of the shared cluster and returns the
// width it used, so the caller can advance exactly as it measured.
func drawStatusClusterPart(renderer *sdl.Renderer, font *ttf.Font, part string, x, textTop int32, draw statusTextDrawer) int32 {
	switch {
	case isWiFiStatusPart(part):
		return drawStatusWiFiChip(renderer, font, part, x, textTop, draw)
	case isBatteryStatusPart(part):
		return drawStatusBatteryChip(renderer, font, part, x, textTop, draw)
	}
	if draw != nil {
		draw(part, ColorTextSecondary(), x, textTop)
	}
	return statusWidth(font, part)
}

// --- Wi-Fi link quality ------------------------------------------------------

var (
	statusWiFiCacheMu sync.Mutex
	statusWiFiCacheAt time.Time
	statusWiFiCache   int
)

// readWiFiSignalLevel reads the Wi-Fi link quality from the kernel and maps it
// onto four bars. A short cache keeps a 60 fps render loop from opening the
// proc file every frame while still reacting within a few seconds to a dropped
// or regained link. TrimUI devices run a plain Linux kernel that exposes
// /proc/net/wireless through wpa_supplicant, the same source the picker's own
// signal values come from.
func readWiFiSignalLevel() int {
	statusWiFiCacheMu.Lock()
	defer statusWiFiCacheMu.Unlock()
	if !statusWiFiCacheAt.IsZero() && time.Since(statusWiFiCacheAt) < statusWiFiRefresh {
		return statusWiFiCache
	}
	level := 0
	if data, err := os.ReadFile("/proc/net/wireless"); err == nil {
		level = wifiBarsForQuality(parseWirelessLinkQuality(string(data)))
	}
	if level == 0 && wifiLinkIsUp() {
		// Some handheld kernels build without wireless extensions, so the proc
		// table is missing even while the device is associated. An interface
		// the kernel reports as running is a live link; the strength is simply
		// unknown, so the meter fills halfway instead of lying with "no link".
		level = 2
	}
	statusWiFiCacheAt = time.Now()
	statusWiFiCache = level
	return level
}

// wifiLinkIsUp reports whether a wireless interface is up, which is how the
// meter stays honest on kernels without /proc/net/wireless. An idle radio is
// dormant or down, never up, so this does not light the meter for a Wi-Fi chip
// that is simply present.
func wifiLinkIsUp() bool {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		base := filepath.Join("/sys/class/net", entry.Name())
		if _, err := os.Stat(filepath.Join(base, "wireless")); err != nil {
			continue
		}
		state, err := os.ReadFile(filepath.Join(base, "operstate"))
		if err == nil && strings.TrimSpace(string(state)) == "up" {
			return true
		}
	}
	return false
}

// parseWirelessLinkQuality returns the strongest link quality in the output of
// /proc/net/wireless. The kernel reports it on a 0..70 scale next to the
// interface name, so 52. is roughly three quarters of the range. -1 means the
// file listed no usable interface (no Wi-Fi hardware, or the driver does not
// expose wireless extensions).
func parseWirelessLinkQuality(out string) int {
	best := -1
	for _, line := range strings.Split(out, "\n") {
		colon := strings.Index(line, ":")
		if colon <= 0 {
			continue
		}
		iface := strings.TrimSpace(line[:colon])
		if iface == "" || strings.ContainsAny(iface, "|-") {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		if len(fields) < 2 {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimRight(fields[1], "."), 64)
		if err != nil {
			continue
		}
		if int(q) > best {
			best = int(q)
		}
	}
	return best
}

// wifiBarsForQuality maps a 0..70 kernel link quality onto four bars.
func wifiBarsForQuality(q int) int {
	switch {
	case q <= 0:
		return 0
	case q >= 50:
		return 4
	case q >= 35:
		return 3
	case q >= 20:
		return 2
	}
	return 1
}
