package main

// saver_status.go — what the idle screens say at a glance: the time, the
// Wi-Fi link and the battery.
//
// Both idle screens — the starfield saver and the dock — are "cover screens":
// the device is sitting on a desk or a charger and the user glances at it. The
// questions are always the same three (what time is it, am I online, does it
// need charging), so both screens draw the same status row from here: glass
// chips in the top-right corner, the same on every idle screen.
//
// The Wi-Fi chip used to be a bare signal meter that read /proc on the render
// thread and could not say *which* network, or tell "not connected" apart from
// "the radio is off". It now shows the network name and bars when connected,
// "Not connected" with a dimmed glyph when the radio is on but not associated,
// and "Wi-Fi off" when the radio is off. The answer comes from a worker that
// asks the supplicant every few seconds while an idle screen is up — never on
// the render thread and never while nobody is looking.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ── Inks ─────────────────────────────────────────────────────────────────

// The idle screens are night surfaces: whatever the theme, their backdrop is
// the dark base, a scrimmed photo or the dock's navy, so text drawn straight on
// that backdrop uses these light inks (a light theme's dark ink would vanish
// there). Text inside a chip, card or button uses saverGlassStyle instead,
// whose fill follows the theme so the theme's own inks and icons stay legible.
var (
	saverInkClock = sdl.Color{R: 246, G: 248, B: 252, A: 255}
	saverInkDate  = sdl.Color{R: 198, G: 210, B: 230, A: 255}
	saverInkSoft  = sdl.Color{R: 152, G: 166, B: 192, A: 255}
)

// saverGlass is the glass of a chip, card or button on an idle screen.
type saverGlass struct {
	fill, edge, raised sdl.Color
	ink, muted         sdl.Color
}

// saverGlassStyle returns the idle-screen glass for the active theme. Text and
// icons inside it use the theme's inks — DrawHomeIcon paints in
// ColorTextPrimary — so a dark theme gets smoked glass (light ink on dark) and
// a light theme gets frosted glass (dark ink on light). Either reads on a dark
// photo, which a theme-blind chip could not promise.
func saverGlassStyle() saverGlass {
	if glassLightTheme() {
		return saverGlass{
			fill:   sdl.Color{R: 246, G: 248, B: 252, A: 222},
			edge:   sdl.Color{R: 255, G: 255, B: 255, A: 245},
			raised: sdl.Color{R: 255, G: 255, B: 255, A: 245},
			ink:    ColorTextPrimary(),
			muted:  ColorTextSecondary(),
		}
	}
	return saverGlass{
		fill:   sdl.Color{R: 12, G: 16, B: 27, A: 150},
		edge:   sdl.Color{R: 255, G: 255, B: 255, A: 40},
		raised: sdl.Color{R: 40, G: 48, B: 66, A: 200},
		ink:    ColorTextPrimary(),
		muted:  ColorTextSecondary(),
	}
}

// ── Sizes ────────────────────────────────────────────────────────────────

// saverScaled scales a 720p design value to the current screen height, never
// below min, so the idle screens keep their proportions on the 4:3 panels.
func saverScaled(v, min int32) int32 {
	s := v
	if screenHeight > 0 {
		s = v * screenHeight / 720
	}
	if s < min {
		s = min
	}
	return s
}

// saverChipFont is the type every idle-screen chip and button uses.
func saverChipFont(config *Config) *ttf.Font {
	return getFontSized(config, "small", int(saverScaled(18, 13)))
}

// ── Wi-Fi state ──────────────────────────────────────────────────────────

// saverWifiKind is what the Wi-Fi chip says.
type saverWifiKind int

const (
	saverWifiUnknown      saverWifiKind = iota // no sample yet: no chip
	saverWifiConnected                         // SSID and bars
	saverWifiConnecting                        // associating / handshaking
	saverWifiDisconnected                      // radio on, no network
	saverWifiOff                               // radio switched off
	saverWifiWired                             // no Wi-Fi hardware, but online
	saverWifiOffline                           // no Wi-Fi hardware, offline
)

// saverWifiState is one finished Wi-Fi reading.
type saverWifiState struct {
	kind  saverWifiKind
	ssid  string
	level int // drawStatusSignalBars level while connected
}

// saverWifiProbe is everything one reading asks the system, kept as a value so
// the decision is testable without a radio.
type saverWifiProbe struct {
	hardware   bool       // the device has a Wi-Fi radio at all
	radioKnown bool       // rfkill / nmcli answered
	radioOn    bool       // ... and said the radio is on
	statusOK   bool       // the supplicant (or Windows) answered
	status     wifiStatus // ... with this association
	level      int        // the status meter's reading
	online     bool       // the connectivity probe's answer
}

// saverWifiDecide turns a probe into what the chip says.
func saverWifiDecide(p saverWifiProbe) saverWifiState {
	if !p.hardware {
		if p.online {
			return saverWifiState{kind: saverWifiWired}
		}
		return saverWifiState{kind: saverWifiOffline}
	}
	if p.radioKnown && !p.radioOn {
		return saverWifiState{kind: saverWifiOff}
	}
	connectedLevel := p.level
	if connectedLevel == statusWiFiNoLink {
		// Associated but unmeasured: outlined bars, never "no signal".
		connectedLevel = statusWiFiUnknown
	}
	if p.statusOK {
		if p.status.Connected {
			return saverWifiState{kind: saverWifiConnected, ssid: strings.TrimSpace(p.status.SSID), level: connectedLevel}
		}
		switch strings.ToUpper(strings.TrimSpace(p.status.State)) {
		case "INTERFACE_DISABLED":
			return saverWifiState{kind: saverWifiOff}
		case "AUTHENTICATING", "ASSOCIATING", "ASSOCIATED", "4WAY_HANDSHAKE", "GROUP_HANDSHAKE":
			return saverWifiState{kind: saverWifiConnecting}
		}
		return saverWifiState{kind: saverWifiDisconnected}
	}
	// Nothing to ask (no supplicant running): the link itself is the evidence.
	// On the handheld the firmware stops the supplicant when Wi-Fi is switched
	// off, so a radio with no supplicant and no link is a radio that is off.
	if p.level != statusWiFiNoLink {
		return saverWifiState{kind: saverWifiConnected, level: connectedLevel}
	}
	return saverWifiState{kind: saverWifiOff}
}

// saverWifiCollect asks the system for one reading. It runs on a worker: the
// supplicant query is a process or a socket round trip, and rfkill is another.
var saverWifiCollect = func() saverWifiProbe {
	p := saverWifiProbe{online: IsOnline()}
	if wifiControlAvailable() {
		p.hardware = true
		if st, err := wifiReadStatus(); err == nil {
			p.status, p.statusOK = st, true
		}
	} else {
		p.hardware = wifiHardwarePresent()
	}
	if !p.hardware {
		return p
	}
	p.radioOn, p.radioKnown = wifiRadioState()
	p.level = readWiFiSignalLevel()
	return p
}

const (
	// saverWifiRefresh is how often the chip is re-read while an idle screen
	// is up: quick enough that a dropped network shows within seconds.
	saverWifiRefresh = 8 * time.Second
	// saverWifiRefreshLowPower is the cadence once the idle screen has settled
	// into its low-power state, where every wake of the CPU costs battery.
	saverWifiRefreshLowPower = 30 * time.Second
)

// saverWifi is the last reading and the worker's bookkeeping. The reading is
// kept across idle screens, so a new one shows the last known state at once
// and corrects it a moment later.
var saverWifi struct {
	mu      sync.Mutex
	state   saverWifiState
	asked   time.Time
	running bool
}

// saverWifiNow returns the last reading and starts a fresh one when it is due.
// It never blocks: the render thread only ever reads the cached answer.
func saverWifiNow(now time.Time) saverWifiState {
	interval := saverWifiRefresh
	if saverLowPowerEngaged() {
		interval = saverWifiRefreshLowPower
	}
	saverWifi.mu.Lock()
	st := saverWifi.state
	due := !saverWifi.running && (saverWifi.asked.IsZero() || now.Sub(saverWifi.asked) >= interval)
	if due {
		saverWifi.running = true
		saverWifi.asked = now
	}
	saverWifi.mu.Unlock()
	if due {
		go saverWifiRefreshNow()
	}
	return st
}

// saverWifiRefreshNow takes one reading and publishes it.
func saverWifiRefreshNow() {
	defer func() {
		if r := recover(); r != nil {
			saverWifi.mu.Lock()
			saverWifi.running = false
			saverWifi.mu.Unlock()
		}
	}()
	st := saverWifiDecide(saverWifiCollect())
	saverWifi.mu.Lock()
	saverWifi.state = st
	saverWifi.running = false
	saverWifi.mu.Unlock()
}

// saverWifiInvalidate makes the next idle frame ask again. Each idle screen
// calls it as it appears, so the chip is corrected within a frame or two of
// the screen coming up instead of a full refresh interval later.
func saverWifiInvalidate() {
	saverWifi.mu.Lock()
	saverWifi.asked = time.Time{}
	saverWifi.mu.Unlock()
}

// saverWifiLabel is the chip's text and whether it is the quiet (muted) kind.
func saverWifiLabel(st saverWifiState) (string, bool) {
	switch st.kind {
	case saverWifiConnected:
		// A screenshot names a stand-in network, never the user's
		// (screenshotmode.go).
		if shotModeOn() {
			return shotSSID, false
		}
		if st.ssid != "" {
			return st.ssid, false
		}
		return "Connected", false
	case saverWifiConnecting:
		return "Connecting…", true
	case saverWifiDisconnected:
		return "Not connected", true
	case saverWifiOff:
		return "Wi-Fi off", true
	case saverWifiWired:
		return "Online", false
	case saverWifiOffline:
		return "Offline", true
	}
	return "", true
}

// saverWifiIcon is the chip's glyph and whether it is drawn dimmed.
func saverWifiIcon(st saverWifiState) (string, bool) {
	switch st.kind {
	case saverWifiConnected:
		return "wifi", false
	case saverWifiConnecting:
		return "wifi", true
	case saverWifiWired:
		return "globe", false
	}
	return "wifi-off", true
}

// ── Battery label ────────────────────────────────────────────────────────

// saverBatteryText remembers the last label, so a frame does not format the
// same string again.
var saverBatteryText struct {
	pct      int
	charging bool
	text     string
	valid    bool
}

// saverBatteryLabel is the battery chip's text: the level, and the charge state
// when plugged in. A device with no battery reading gets no chip at all rather
// than "Battery unknown", which reads as a fault on a screen meant to be calm.
func saverBatteryLabel(pct int, charging bool) string {
	if saverBatteryText.valid && saverBatteryText.pct == pct && saverBatteryText.charging == charging {
		return saverBatteryText.text
	}
	text := ""
	switch {
	case pct >= 0 && charging:
		text = fmt.Sprintf("%d%% · Charging", pct)
	case pct >= 0:
		text = fmt.Sprintf("%d%%", pct)
	case charging:
		text = "Charging"
	}
	saverBatteryText.pct, saverBatteryText.charging = pct, charging
	saverBatteryText.text, saverBatteryText.valid = text, true
	return text
}

// ── Chips ────────────────────────────────────────────────────────────────

// saverChipMetrics are the chip dimensions at the current screen size.
type saverChipMetrics struct {
	h, pad, icon, gap int32
}

func saverChipSize() saverChipMetrics {
	return saverChipMetrics{
		h:    saverScaled(38, 28),
		pad:  saverScaled(13, 9),
		icon: saverScaled(18, 13),
		gap:  saverScaled(8, 6),
	}
}

// saverDrawChipBody paints the glass pill a chip sits in.
func saverDrawChipBody(renderer *sdl.Renderer, rect sdl.Rect, g saverGlass) {
	radius := rect.H / 2
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, g.fill)
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, g.edge)
}

// saverDrawIcon draws a shared glyph, optionally dimmed. The glyphs carry the
// theme's ink and have no alpha of their own, so dimming lays the chip's own
// glass over the glyph: the glyph recedes into the chip it sits in.
func saverDrawIcon(renderer *sdl.Renderer, name string, rect sdl.Rect, dim bool, g saverGlass) {
	if name == "" || rect.W <= 0 || rect.H <= 0 {
		return
	}
	DrawHomeIcon(renderer, name, rect, false)
	if dim {
		veil := g.fill
		veil.A = 150
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		setDrawColor(renderer, veil)
		_ = renderer.FillRect(&rect)
	}
}

// saverDrawWifiChip draws the Wi-Fi chip with its right edge at right and
// returns its left edge (right itself when there is nothing to show yet).
func saverDrawWifiChip(renderer *sdl.Renderer, font *ttf.Font, st saverWifiState, right, y int32) int32 {
	label, muted := saverWifiLabel(st)
	if renderer == nil || font == nil || label == "" {
		return right
	}
	m := saverChipSize()
	g := saverGlassStyle()
	barsW, barsH := int32(0), int32(0)
	if st.kind == saverWifiConnected {
		barsH = m.icon * 3 / 4
		barsW = barsH * 3 / 2
	}
	// The network name is the one thing here of unbounded length.
	maxLabel := saverScaled(230, 120)
	label = ellipsize(font, label, maxLabel)
	lw, lh := measureText(font, label)
	w := m.pad + m.icon + m.gap + lw + m.pad
	if barsW > 0 {
		w += m.gap + barsW
	}
	rect := sdl.Rect{X: right - w, Y: y, W: w, H: m.h}
	saverDrawChipBody(renderer, rect, g)
	icon, dim := saverWifiIcon(st)
	x := rect.X + m.pad
	saverDrawIcon(renderer, icon, sdl.Rect{X: x, Y: y + (m.h-m.icon)/2, W: m.icon, H: m.icon}, dim, g)
	x += m.icon + m.gap
	ink := g.ink
	if muted {
		ink = g.muted
	}
	drawText(renderer, font, label, x, y+(m.h-lh)/2, ink, textAlignLeft)
	if barsW > 0 {
		x += lw + m.gap
		drawStatusSignalBars(renderer, x, y+(m.h-barsH)/2, barsW, barsH, st.level, g.muted)
	}
	return rect.X
}

// saverDrawBatteryChip draws the battery chip with its right edge at right and
// returns its left edge (right itself when the device reports no battery).
func saverDrawBatteryChip(renderer *sdl.Renderer, font *ttf.Font, status ambientStatus, right, y int32) int32 {
	label := saverBatteryLabel(status.battery, status.charging)
	if renderer == nil || font == nil || label == "" {
		return right
	}
	m := saverChipSize()
	g := saverGlassStyle()
	gaugeH := m.icon * 3 / 4
	gaugeW := statusBatteryIconWidth(gaugeH) + statusBatteryNubWidth(gaugeH) + 1
	iconW := gaugeW
	if status.battery < 0 {
		iconW = m.icon
	}
	lw, lh := measureText(font, label)
	w := m.pad + iconW + m.gap + lw + m.pad
	rect := sdl.Rect{X: right - w, Y: y, W: w, H: m.h}
	saverDrawChipBody(renderer, rect, g)
	x := rect.X + m.pad
	if status.battery >= 0 {
		drawStatusBatteryGauge(renderer, x, y+(m.h-gaugeH)/2, gaugeH, status.battery, statusBatteryColor(status.battery))
	} else {
		saverDrawIcon(renderer, "battery", sdl.Rect{X: x, Y: y + (m.h-m.icon)/2, W: m.icon, H: m.icon}, false, g)
	}
	x += iconW + m.gap
	drawText(renderer, font, label, x, y+(m.h-lh)/2, g.ink, textAlignLeft)
	return rect.X
}

// saverDrawLabelChip draws a plain chip — a glyph and a label — with its right
// edge at right, and returns its left edge.
func saverDrawLabelChip(renderer *sdl.Renderer, font *ttf.Font, icon, label string, right, y int32) int32 {
	if renderer == nil || font == nil || label == "" {
		return right
	}
	m := saverChipSize()
	g := saverGlassStyle()
	lw, lh := measureText(font, label)
	w := m.pad + m.icon + m.gap + lw + m.pad
	rect := sdl.Rect{X: right - w, Y: y, W: w, H: m.h}
	saverDrawChipBody(renderer, rect, g)
	saverDrawIcon(renderer, icon, sdl.Rect{X: rect.X + m.pad, Y: y + (m.h-m.icon)/2, W: m.icon, H: m.icon}, false, g)
	drawText(renderer, font, label, rect.X+m.pad+m.icon+m.gap, y+(m.h-lh)/2, g.ink, textAlignLeft)
	return rect.X
}

// saverDrawStatusRow draws the idle screens' status row — battery in the
// corner, Wi-Fi to its left — right-aligned at right, and returns the row's
// left edge so a caller can place something beside it.
func saverDrawStatusRow(renderer *sdl.Renderer, config *Config, right, y int32, now time.Time) int32 {
	font := saverChipFont(config)
	if renderer == nil || font == nil {
		return right
	}
	gap := saverScaled(10, 6)
	left := saverDrawBatteryChip(renderer, font, ambientReadStatus(now), right, y)
	if left < right {
		left -= gap
	}
	return saverDrawWifiChip(renderer, font, saverWifiNow(now), left, y)
}

// ── Clock ────────────────────────────────────────────────────────────────

// saverDrawClock draws the time with the date under it at (x, y), and returns
// the y just below the date.
func saverDrawClock(renderer *sdl.Renderer, config *Config, x, y int32, size int, now time.Time) int32 {
	if renderer == nil {
		return y
	}
	bottom := y
	if clockFont := getFontSized(config, "big", size); clockFont != nil {
		_, h := renderText(renderer, config, clockFont, clockDigits(config, now), saverInkClock, x, y)
		bottom = y + h
	}
	if dateFont := getFontSized(config, "small", int(saverScaled(24, 15))); dateFont != nil {
		_, h := renderText(renderer, config, dateFont, formatDayLong(config, now), saverInkDate, x+saverScaled(4, 2), bottom)
		bottom += h
	}
	return bottom
}

// saverDrawAlarmChip draws the next alarm as a chip whose left edge is x, when
// one is armed, and returns the y below it (y itself when there is none).
func saverDrawAlarmChip(renderer *sdl.Renderer, config *Config, x, y int32) int32 {
	next := alarmNextUpcoming()
	if next == nil {
		return y
	}
	font := saverChipFont(config)
	if font == nil {
		return y
	}
	hour, minute := next.Hour, next.Minute
	if shotModeOn() {
		// A screenshot shows a stand-in alarm, not the user's (screenshotmode.go).
		hour, minute = 10, 45
	}
	label := "Alarm " + clockDigitsHM(config, hour, minute)
	m := saverChipSize()
	lw, _ := measureText(font, label)
	w := m.pad + m.icon + m.gap + lw + m.pad
	saverDrawLabelChip(renderer, font, "alarm", label, x+w, y)
	return y + m.h
}
