package main

// The system corner of the top bar: a brightness button, a volume button, a
// Wi-Fi meter and a battery gauge.
//
// The right-hand cluster is still the shared []string from headerStatusParts,
// so layout, trimming (fitStatusParts) and the clock/weather hit rects keep
// working unchanged. What changes here is how four of those parts are painted:
// the leading brightness part is a sun (the button that opens the brightness
// card, brightnesspopover.go), the volume part beside it a speaker glyph (the
// button that opens the volume popover, volumepopover.go), the network slot
// becomes signal bars (empty when there is no link) and the battery part grows
// a gauge coloured green / yellow / red by the charge left, with a bolt over it
// while a charger is plugged in.
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

	// statusVolumePart is the volume button's part: a marker like the media
	// chip's "np|", never drawn as text. headerStatusParts puts it first, so
	// the speaker sits immediately left of the Wi-Fi meter.
	statusVolumePart = "vol|"

	// statusBrightnessPart is the brightness button's part, a marker like the
	// volume's. headerStatusParts puts it before the volume part, so the sun
	// sits immediately left of the speaker and the speaker stays immediately
	// left of the Wi-Fi meter.
	statusBrightnessPart = "bri|"

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

// statusVolumeRect is the speaker button's hit area, measured the same way. A
// click, a tap or A on it opens the volume popover, and the popover hangs
// from it.
var statusVolumeRect sdl.Rect

// statusBrightnessRect is the sun's hit area, measured the same way. A click,
// a tap or A on it opens the brightness card, which hangs from it.
var statusBrightnessRect sdl.Rect

// statusBrightnessLevel supplies the sun's level (brightnessctl.go keeps it
// fresh off the render thread); known=false before the first reading.
var statusBrightnessLevel = brightnessCurrent

// statusBatteryPercent supplies the level in the shared status cluster. It is a
// variable so the render tests can pin a level instead of depending on the host
// machine's battery (the real reader returns -1 when there is none).
var statusBatteryPercent = getBatteryPercent

// statusBatteryCharging says whether the gauge wears the charging bolt. A
// variable for the same reason as the level: a render test pins it instead of
// depending on whether the build machine is on its charger.
var statusBatteryCharging = getBatteryCharging

// statusVolumeLevel supplies the speaker glyph's level (volumectl.go keeps it
// fresh off the render thread); known=false before the first reading.
var statusVolumeLevel = volumeCurrent

// statusWiFiSignal returns the live meter state: 1..4 measured bars,
// statusWiFiUnknown (a link whose strength is not reported) or statusWiFiNoLink
// (no wireless link at all). A variable for the same reason as the battery: the
// tests pin the meter instead of depending on the host's kernel.
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

// isVolumeStatusPart reports whether the part is the volume button. The marker
// cannot collide with a reading: no clock, forecast, battery level, SSID or
// media title is spelt "vol|".
func isVolumeStatusPart(part string) bool {
	return part == statusVolumePart
}

// isBrightnessStatusPart reports whether the part is the brightness button,
// whose marker cannot collide with a reading either.
func isBrightnessStatusPart(part string) bool {
	return part == statusBrightnessPart
}

// statusFingerRect turns a drawn status chip into a tap target a thumb can hit
// without aiming. The meter itself is only about 18px of bars inside a 34px
// chip, which is a miss on a 5-inch panel; the target grows to the full height
// of the bar and to a comfortable width, then stops short of the neighbouring
// controls so a tap on the clock or the forecast still belongs to them.
//
// Neighbours on either side bound it: the cluster now starts with the
// brightness and volume buttons, so the meter is no longer the leftmost chip
// and its target may not grow over the speaker beside it. A chip with nothing
// on its left (the leading button) still grows into the empty space there.
func statusFingerRect(chip sdl.Rect, barH int32, neighbours ...sdl.Rect) sdl.Rect {
	if chip.W <= 0 || barH <= 0 {
		return chip
	}
	const (
		padX       = int32(10)
		minTargetW = int32(44)
	)
	r := sdl.Rect{X: chip.X - padX, Y: 0, W: chip.W + 2*padX, H: barH}
	// floor is the leftmost x the target may take: 0, or 2px clear of a
	// neighbour drawn on the chip's left.
	floor := int32(0)
	// A neighbour on the right is only a few pixels away (the cluster packs
	// its chips tightly), so the right edge stops before it.
	for _, n := range neighbours {
		if n.W <= 0 || n.H <= 0 {
			continue
		}
		if n.X >= chip.X+chip.W/2 {
			if r.X+r.W > n.X-2 {
				if limit := n.X - 2; limit > r.X {
					r.W = limit - r.X
				}
			}
			continue
		}
		if n.X+n.W <= chip.X+chip.W/2 {
			if limit := n.X + n.W + 2; limit > floor {
				floor = limit
			}
		}
	}
	if floor > chip.X {
		floor = chip.X
	}
	if r.X < floor {
		r.W -= floor - r.X
		r.X = floor
	}
	// A short target grows into the space on its left, as far as the floor:
	// the volume button leads the cluster, so that space is empty for it.
	if r.W < minTargetW {
		extra := minTargetW - r.W
		if room := r.X - floor; extra > room {
			extra = room
		}
		r.X -= extra
		r.W += extra
	}
	if r.X < 0 {
		r.W += r.X
		r.X = 0
	}
	if r.W < chip.W {
		r.W = chip.W
	}
	return r
}

// statusGapNeighbours splits the gap between two chips drawn side by side at
// its middle, as the stand-in neighbour each one's statusFingerRect stops at:
// the left chip's target ends at the middle and the right chip's starts there,
// so a tap in the gap belongs to exactly one of them.
func statusGapNeighbours(left, right sdl.Rect) (forLeft, forRight sdl.Rect) {
	mid := (left.X + left.W + right.X) / 2
	// statusFingerRect stops 2px short of a right neighbour and starts 2px past
	// a left one; these 1px stand-ins put both edges exactly on mid.
	forLeft = sdl.Rect{X: mid + 2, Y: right.Y, W: 1, H: 1}
	forRight = sdl.Rect{X: mid - 3, Y: left.Y, W: 1, H: 1}
	return forLeft, forRight
}

// growStatusButtonRects grows the brightness button's, the volume button's
// and the Wi-Fi meter's hit areas once they are measured (both headers call it
// after placing the cluster). They sit side by side in that order, and each
// gap between two of them is split at its middle; the rightmost one still
// stops short of the forecast and the clock on its right (rightOfWiFi), and
// the leftmost grows into the empty space on its left. Any of them may be
// missing: the bar drops parts when it is crowded.
func growStatusButtonRects(barH int32, rightOfWiFi ...sdl.Rect) {
	var live [3]*sdl.Rect
	n := 0
	for _, r := range []*sdl.Rect{&statusBrightnessRect, &statusVolumeRect, &statusWiFiRect} {
		if r.W > 0 {
			live[n] = r
			n++
		}
	}
	var drawn [3]sdl.Rect
	for i := 0; i < n; i++ {
		drawn[i] = *live[i]
	}
	for i := 0; i < n; i++ {
		var nb [6]sdl.Rect
		k := 0
		if i > 0 {
			_, forRight := statusGapNeighbours(drawn[i-1], drawn[i])
			nb[k] = forRight
			k++
		}
		if i+1 < n {
			forLeft, _ := statusGapNeighbours(drawn[i], drawn[i+1])
			nb[k] = forLeft
			k++
		} else {
			for _, r := range rightOfWiFi {
				if k < len(nb) {
					nb[k] = r
					k++
				}
			}
		}
		*live[i] = statusFingerRect(drawn[i], barH, nb[:k]...)
	}
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

// statusWidth is the text half's width, zero without a font. It is measured
// through the shared cache: the bar recomputes the clock, battery and media
// labels on every frame to place its chips.
func statusWidth(font *ttf.Font, text string) int32 {
	if font == nil || text == "" {
		return 0
	}
	w, _ := measureText(font, text)
	return w
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

// statusVolumeIconWidth is the speaker's box for a given icon height. The glyph
// is drawn on the shared 24-grid with its own padding, so its box is a third
// taller than the gauges' to read at the same weight as the signal bars.
func statusVolumeIconWidth(iconH int32) int32 {
	w := iconH * 4 / 3
	if w < 12 {
		w = 12
	}
	if w > 28 {
		w = 28
	}
	return w
}

// statusClusterPartWidth measures one part as it will be drawn. fitStatusParts
// uses it, so a gauged part is never clipped by the bar's trimming.
func statusClusterPartWidth(font *ttf.Font, part string) int32 {
	iconH := statusIconHeight(font)
	switch {
	case isVolumeStatusPart(part), isBrightnessStatusPart(part):
		// The sun shares the speaker's box, so the two buttons read as a pair.
		return statusVolumeIconWidth(iconH)
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
	case isChipStatusPart(part):
		// The now-playing title and the idle output name share the same glyph
		// and the same space, so the cluster does not move when playback starts
		// or stops.
		return statusMediaIconWidth(iconH) + statusChipTextGap +
			statusWidth(font, statusMediaLabel(font, part))
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

// The two states a signal meter can be in without a strength to show. They are
// sentinels rather than "0 bars", because four hollow bars read as a weak
// signal: "no Wi-Fi at all" and "connected, strength not reported" are
// different facts and the meter has to say which one it is.
const (
	statusWiFiNoLink  = 0  // no wireless link
	statusWiFiUnknown = -1 // link up, the kernel reports no strength
)

// drawStatusSignalBars draws four ascending bars for a meter state: 1..4 solid
// bars for a measured strength, hollow bars outlined in the accent for a live
// link whose strength is unknown, and hollow bars with a slash through them when
// there is no link at all — the slash is what stops the empty meter reading as a
// weak signal. noLinkInk colours that slash.
func drawStatusSignalBars(renderer *sdl.Renderer, x, y, w, h int32, level int, noLinkInk sdl.Color) {
	if renderer == nil || w <= 0 || h <= 0 {
		return
	}
	if level > 4 {
		level = 4
	}
	if level < statusWiFiUnknown {
		level = statusWiFiUnknown
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
	unknown := level == statusWiFiUnknown
	if unknown {
		col = WithAlpha(accentColor, 210)
	}
	for i := int32(0); i < 4; i++ {
		bh := h * (i + 1) / 4
		if bh < 2 {
			bh = 2
		}
		bx := x + i*(barW+gap)
		by := baseY - bh
		c := col
		if level == statusWiFiNoLink || level == statusWiFiUnknown || int(i) >= level {
			c = empty
		}
		fillRoundedRect(renderer, bx, by, barW, bh, 1, c)
		if unknown && int(i) < 4 {
			// Outline instead of fill: the glyph says "connected" without
			// claiming a strength nobody measured.
			strokeRoundedRect(renderer, bx, by, barW, bh, 1, 1, WithAlpha(accentColor, 200))
		}
	}
	if level == statusWiFiNoLink {
		// A diagonal slash over the hollow bars. Two parallel 1px lines keep it
		// visible on a 480p panel without swallowing the bars.
		ink := noLinkInk
		if ink.A == 0 {
			ink = sdl.Color{R: 78, G: 86, B: 104, A: 220}
		}
		renderer.SetDrawColor(ink.R, ink.G, ink.B, ink.A)
		renderer.DrawLine(x+1, baseY-2, x+w-2, y+2)
		renderer.DrawLine(x+2, baseY-1, x+w-1, y+3)
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

// drawStatusWiFiChip paints the network slot: the meter for the radio's real
// state, plus the offline warning text when the internet is unreachable. It
// returns the width it used.
func drawStatusWiFiChip(renderer *sdl.Renderer, font *ttf.Font, part string, x, textTop int32, draw statusTextDrawer) int32 {
	iconH := statusIconHeight(font)
	iconW := statusWiFiIconWidth(iconH)
	textH := iconH
	if font != nil {
		textH = int32(font.Height())
	}
	iconY := textTop + (textH-iconH)/2
	// The meter's state is the Wi-Fi link's, not the internet's: a device on
	// Ethernet is online with no wireless link, and the corner must not claim
	// bars for a radio that is not associated. Warning ink says "no Wi-Fi and
	// nothing else is keeping you online"; the muted ink says "no Wi-Fi, but
	// you are still connected".
	offline := part != statusWiFiMarker
	// The meter reports the radio, and the text beside it reports the
	// internet: an associated network with no route shows real bars next to
	// "! Offline", which is the honest pair of facts. Only the mark's ink
	// follows the part, so "no Wi-Fi" is a warning when nothing else is online
	// and a quiet note when something is.
	level := statusWiFiSignal()
	ink := sdl.Color{R: 120, G: 132, B: 154, A: 220}
	if offline {
		ink = WarningInk()
	}
	drawStatusSignalBars(renderer, x, iconY, iconW, iconH, level, ink)
	if offline && draw != nil {
		draw(part, WarningInk(), x+iconW+statusChipTextGap, textTop)
	}
	return statusClusterPartWidth(font, part)
}

// drawStatusBatteryChip paints the battery part: gauge plus percentage, both in
// the level's colour so a dying battery is visible in the corner of the screen.
// On a charger both turn green and a bolt sits over the cell, the way a phone
// says "plugged in" — the gauge alone looked the same on and off the cable.
// The bolt is drawn inside the cell, so the part's width does not change.
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
	charging := statusBatteryCharging()
	col := statusBatteryColor(pct)
	if charging {
		col = statusBatteryChargingColor
	}
	cellY := textTop + (textH-iconH)/2
	gaugeW := drawStatusBatteryGauge(renderer, x, cellY, iconH, pct, col)
	if charging {
		drawStatusChargingBolt(renderer, sdl.Rect{X: x, Y: cellY, W: statusBatteryIconWidth(iconH), H: iconH})
	}
	if draw != nil {
		draw(statusBatteryLabel(pct), col, x+gaugeW+statusChipTextGap, textTop)
	}
	return statusClusterPartWidth(font, part)
}

// statusBatteryChargingColor is the gauge's ink on a charger: the same green as
// a healthy charge, so "charging" never reads as a warning, even at 5%.
var statusBatteryChargingColor = sdl.Color{R: 92, G: 200, B: 140, A: 255}

// drawStatusChargingBolt paints the charging bolt centred on the battery cell:
// a dark silhouette a little larger than the bolt first, so the white bolt
// keeps an edge over the green fill and over an empty cell alike. Both come
// from the icon atlas ("bolt" in icondefs.go), one cached blit each.
func drawStatusChargingBolt(renderer *sdl.Renderer, cell sdl.Rect) {
	if renderer == nil || cell.W <= 0 || cell.H <= 0 {
		return
	}
	side := cell.H + 2
	cx, cy := cell.X+cell.W/2, cell.Y+cell.H/2
	halo := side + 4
	iconDraw(renderer, "bolt", sdl.Rect{X: cx - halo/2, Y: cy - halo/2, W: halo, H: halo},
		sdl.Color{R: 8, G: 12, B: 20, A: 210})
	iconDraw(renderer, "bolt", sdl.Rect{X: cx - side/2, Y: cy - side/2, W: side, H: side},
		sdl.Color{R: 255, G: 255, B: 255, A: 250})
}

// drawStatusVolumeChip paints the volume button: the speaker glyph for the
// current level (struck through at zero, one wave below half, two above), in
// the accent while its popover is open so the button says what is showing.
func drawStatusVolumeChip(renderer *sdl.Renderer, font *ttf.Font, x, textTop int32) int32 {
	iconH := statusIconHeight(font)
	side := statusVolumeIconWidth(iconH)
	textH := iconH
	if font != nil {
		textH = int32(font.Height())
	}
	pct, known := statusVolumeLevel()
	col := ColorTextSecondary()
	if volumePopoverOpen {
		col = glassAccent()
	}
	iconDraw(renderer, statusVolumeGlyph(pct, known), sdl.Rect{X: x, Y: textTop + (textH-side)/2, W: side, H: side}, col)
	return side
}

// statusVolumeGlyph names the speaker glyph for a level. An unread level shows
// the plain speaker: claiming "muted" before the mixer answered would be a lie.
func statusVolumeGlyph(pct int, known bool) string {
	switch {
	case !known:
		return "volume"
	case pct <= 0:
		return "volume-mute"
	case pct < 50:
		return "volume-low"
	}
	return "volume"
}

// drawStatusBrightnessChip paints the brightness button: the sun for the
// current level (the small sun below half), in the accent while its card is
// open, in the speaker's box.
func drawStatusBrightnessChip(renderer *sdl.Renderer, font *ttf.Font, x, textTop int32) int32 {
	iconH := statusIconHeight(font)
	side := statusVolumeIconWidth(iconH)
	textH := iconH
	if font != nil {
		textH = int32(font.Height())
	}
	pct, known := statusBrightnessLevel()
	col := ColorTextSecondary()
	if brightnessPopoverOpen {
		col = glassAccent()
	}
	iconDraw(renderer, brightnessGlyph(pct, known), sdl.Rect{X: x, Y: textTop + (textH-side)/2, W: side, H: side}, col)
	return side
}

// drawStatusClusterPart paints one part of the shared cluster and returns the
// width it used, so the caller can advance exactly as it measured.
func drawStatusClusterPart(renderer *sdl.Renderer, font *ttf.Font, part string, x, textTop int32, draw statusTextDrawer) int32 {
	switch {
	case isBrightnessStatusPart(part):
		return drawStatusBrightnessChip(renderer, font, x, textTop)
	case isVolumeStatusPart(part):
		return drawStatusVolumeChip(renderer, font, x, textTop)
	case isWiFiStatusPart(part):
		return drawStatusWiFiChip(renderer, font, part, x, textTop, draw)
	case isBatteryStatusPart(part):
		return drawStatusBatteryChip(renderer, font, part, x, textTop, draw)
	case isChipStatusPart(part):
		return drawStatusMediaChip(renderer, font, part, x, textTop, draw)
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

// readWiFiSignalLevel reads the Wi-Fi signal strength and maps it onto the
// meter's states: 1..4 measured bars, statusWiFiUnknown when nothing reports a
// strength, or statusWiFiNoLink when there is no wireless link. A short cache
// keeps a 60 fps render loop from opening the proc file every frame while still
// reacting within a few seconds to a dropped or regained link.
//
// Two sources, in order: /proc/net/wireless (the kernel's link quality, present
// on the stock TrimUI kernels) and `wpa_cli signal_poll` (the supplicant's own
// RSSI, present even on images built without wireless extensions). The second
// one runs off-thread — see wifiSignalPollBars — because the render loop must
// never wait on a command.
func readWiFiSignalLevel() int {
	statusWiFiCacheMu.Lock()
	defer statusWiFiCacheMu.Unlock()
	if !statusWiFiCacheAt.IsZero() && time.Since(statusWiFiCacheAt) < statusWiFiRefresh {
		return statusWiFiCache
	}
	quality := -1
	if data, err := os.ReadFile("/proc/net/wireless"); err == nil {
		quality = parseWirelessLinkQuality(string(data))
	}
	level := wifiMeterLevelFrom(quality, wifiSignalPollBars, wifiLinkIsUp)
	statusWiFiCacheAt = time.Now()
	statusWiFiCache = level
	return level
}

// wifiMeterLevelFrom is the meter's source chain in one testable place: a
// readable kernel quality wins, then the supplicant's RSSI, then the honest
// "up but unmeasured" and "no link" states. An idle radio is dormant or down,
// never up, so the last step does not light the meter for a chip that is merely
// present — and a link nobody can measure says exactly that instead of a made-up
// "two bars".
func wifiMeterLevelFrom(quality int, poll func() (int, bool), linkUp func() bool) int {
	// A positive quality is a measurement. Zero is not: the kernel prints 0 for
	// an idle interface and for a driver that has no wireless extensions, and
	// the second case is exactly the image this fallback exists for — so 0 falls
	// through to the supplicant rather than ending as "no link".
	if quality > 0 {
		return wifiBarsForQuality(quality)
	}
	if bars, ok := poll(); ok {
		return bars
	}
	return wifiMeterLevel(quality, linkUp())
}

// ── supplicant signal cache ─────────────────────────────────────────────────

var (
	wifiPollMu   sync.Mutex
	wifiPollBars = statusWiFiUnknown
	// wifiPollKnocked is when the last poll was *started*, so a supplicant that
	// never answers is retried on the same cadence instead of every frame.
	wifiPollKnocked time.Time
	wifiPollBusy    bool
)

// wifiSignalPollBars returns the last `signal_poll` reading and refreshes it in
// the background. It never runs the command on the caller's thread: the meter is
// drawn on the render loop, and a supplicant that stops answering would freeze
// the UI for the command timeout. The first frames after a /proc-less boot read
// "unknown" and become real bars one refresh later.
func wifiSignalPollBars() (int, bool) {
	wifiPollMu.Lock()
	due := wifiPollKnocked.IsZero() || time.Since(wifiPollKnocked) >= statusWiFiRefresh
	if due && !wifiPollBusy && wifiControlAvailable() {
		wifiPollBusy = true
		wifiPollKnocked = time.Now()
		go func() {
			bars := refreshWiFiSignalPoll()
			wifiPollMu.Lock()
			wifiPollBusy = false
			wifiPollBars = bars
			wifiPollMu.Unlock()
		}()
	}
	bars := wifiPollBars
	wifiPollMu.Unlock()
	return wifiPollBarState(bars)
}

// refreshWiFiSignalPoll runs one poll and turns it into meter bars. It is the
// body of the background refresh, split out so a test can run it synchronously.
func refreshWiFiSignalPoll() int {
	sample, err := wifiSignalPoll()
	if err != nil {
		return statusWiFiUnknown
	}
	return wifiMeterBarsForRSSI(sample.RSSI)
}

// wifiPollBarState reports whether a cached value is a real measurement. The
// meter's sentinels are 0 (no link) and -1 (unknown), so anything outside 1..4
// is not a bar count.
func wifiPollBarState(bars int) (int, bool) {
	if bars < 1 || bars > 4 {
		return statusWiFiUnknown, false
	}
	return bars, true
}

// wifiMeterBarsForRSSI maps a supplicant RSSI onto the meter. wifiSignalBars
// deliberately shows one bar for a missing reading in the picker's list; the
// status meter has a dedicated "unmeasured" state, so a non-measurement stays
// unmeasured instead of becoming a weak-looking link.
func wifiMeterBarsForRSSI(rssi int) int {
	if rssi >= 0 || rssi < -110 {
		return statusWiFiUnknown
	}
	return wifiSignalBars(rssi)
}

// wifiLinkIsUp reports whether a wireless interface is up, which is how the
// meter stays honest on kernels without /proc/net/wireless. An idle radio is
// dormant or down, never up, so this does not light the meter for a Wi-Fi chip
// that is simply present.
//
// The interface list comes from wirelessInterfaceNames, not a local scan of
// /sys/class/net: a local scan has to test entries that are symlinks (see
// isWirelessNetdev) and it only ever looked for the "wireless" marker, so a
// cfg80211 image — where the marker is "phy80211" — read as "no link" even with
// the interface up.
func wifiLinkIsUp() bool {
	if winWifiActive() {
		return winWifiLinkUp()
	}
	for _, name := range wirelessInterfaceNames() {
		state, err := os.ReadFile(filepath.Join(wifiSysfsNetRoot, name, "operstate"))
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

// wifiMeterLevel turns what the kernel reported into the meter's state. A
// readable quality is a measurement (0..4 bars); without one, a live interface
// is statusWiFiUnknown and an idle or absent radio is statusWiFiNoLink.
func wifiMeterLevel(quality int, linkUp bool) int {
	if quality >= 0 {
		return wifiBarsForQuality(quality)
	}
	if linkUp {
		return statusWiFiUnknown
	}
	return statusWiFiNoLink
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
