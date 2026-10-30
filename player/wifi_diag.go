package main

// wifi_diag.go — the Wi-Fi diagnostics sheet.
//
// The picker can only say "no networks" when it cannot name a radio, and on a
// handheld there is no shell to ask why. This view answers the questions a
// support thread asks: which interface was found and by which probe, where the
// supplicant's control directory is and whether its socket is there, how a
// command reaches the supplicant, whether JukaHub started one and what happened,
// the radio's state, the station's state, and the last error with the source it
// came from and how long ago it was.
//
// It opens even when the picker cannot list anything — that is the case it
// exists for. The panel still opens in "turn Wi-Fi on" mode on hardware whose
// supplicant has never run, so the sheet is reachable from the same list.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// wifiDiagState colors one row: ok is the expected answer, warn is a state the
// picker can work around, err is the reason it cannot, info is context.
type wifiDiagState int

const (
	wifiDiagInfo wifiDiagState = iota
	wifiDiagOK
	wifiDiagWarn
	wifiDiagErr
)

// wifiDiagEntry is one labelled fact.
type wifiDiagEntry struct {
	Label string
	Value string
	State wifiDiagState
}

// wifiDiagReport is one collection. At is when the probes ran, which is what the
// sheet's age and auto-refresh read.
type wifiDiagReport struct {
	Entries []wifiDiagEntry
	At      time.Time
}

// ── The last error, remembered ─────────────────────────────────────────────

// wifiLastErr is the last Wi-Fi failure this process saw, kept apart from the
// panel so a sheet opened after the error was dismissed can still show it.
var wifiLastErr struct {
	mu     sync.Mutex
	text   string
	source string
	at     time.Time
}

// recordWiFiError remembers a failure with the phase it happened in ("command",
// "supplicant start", "radio"). The message is what the picker would show, so
// the sheet and the header cannot disagree.
func recordWiFiError(source string, err error) {
	if err == nil {
		return
	}
	text := firstLine(err.Error())
	if text == "" {
		return
	}
	wifiLastErr.mu.Lock()
	wifiLastErr.text = text
	wifiLastErr.source = source
	wifiLastErr.at = time.Now()
	wifiLastErr.mu.Unlock()
}

// wifiLastError returns the remembered failure, its phase and when it happened.
func wifiLastError() (string, string, time.Time) {
	wifiLastErr.mu.Lock()
	defer wifiLastErr.mu.Unlock()
	return wifiLastErr.text, wifiLastErr.source, wifiLastErr.at
}

// wifiSupplicantStartLog records what happened the one time JukaHub tried to
// start the supplicant. The attempt is one-shot per interface per process, so
// "not attempted" and "failed" are different answers and the sheet shows which.
var wifiSupplicantStartLog struct {
	mu    sync.Mutex
	iface string
	note  string
}

// recordSupplicantStart remembers the start attempt's outcome for the sheet.
func recordSupplicantStart(iface, note string) {
	wifiSupplicantStartLog.mu.Lock()
	wifiSupplicantStartLog.iface = iface
	wifiSupplicantStartLog.note = note
	wifiSupplicantStartLog.mu.Unlock()
}

// supplicantStartNote is the remembered start outcome, or "" when JukaHub has
// not tried to start a supplicant this session.
func supplicantStartNote() (string, string) {
	wifiSupplicantStartLog.mu.Lock()
	defer wifiSupplicantStartLog.mu.Unlock()
	return wifiSupplicantStartLog.iface, wifiSupplicantStartLog.note
}

// ── Probes ─────────────────────────────────────────────────────────────────

// wifiInterfaceSource names how the interface was discovered, in the order
// wifiInterface and wirelessInterfaceNames try. It is what turns "no interface"
// into "sysfs has no marker, /proc is empty and no control socket exists".
func wifiInterfaceSource() string {
	if winWifiActive() {
		return "Windows wlan API"
	}
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_WIFI_IFACE")); validIfaceName(override) {
		return "JUKAHUB_WIFI_IFACE"
	}
	if names, err := sysfsNetdevs(wifiSysfsNetRoot); err == nil {
		for _, name := range names {
			if isWirelessNetdev(wifiSysfsNetRoot, name) {
				return "sysfs marker in " + filepath.Join(wifiSysfsNetRoot, name)
			}
		}
	}
	if data, err := os.ReadFile(wifiProcWirelessPath); err == nil {
		if listed := parseWirelessProcIfaces(string(data)); len(listed) > 0 {
			return "kernel list in " + wifiProcWirelessPath
		}
	}
	if names := supplicantInterfaceNames(supplicantControlDirs()); len(names) > 0 {
		return "supplicant control directory"
	}
	if supplicantDrivenInterfaceCached() != "" {
		return "wpa_supplicant (asked wpa_cli)"
	}
	if names, _ := sysfsNetdevs(wifiSysfsNetRoot); len(names) > 0 {
		for _, name := range names {
			if looksLikeWirelessName(name) {
				return "interface name (" + name + ")"
			}
		}
	}
	return "nothing found: no sysfs marker, no kernel list, no control socket"
}

// wifiStationSummary is the supplicant's answer in one line.
func wifiStationSummary(st wifiStatus) string {
	if !st.Connected {
		if st.State == "" {
			return "not connected"
		}
		return "not connected (" + st.State + ")"
	}
	line := st.State
	if st.SSID != "" {
		line += " · " + st.SSID
	}
	if st.IP != "" {
		line += " · " + st.IP
	}
	return line
}

// collectWiFiDiagnostics reads every fact the sheet shows. It runs on a worker
// (some probes fork) and never fails: a source that cannot be read becomes a
// row that says so.
func collectWiFiDiagnostics() wifiDiagReport {
	report := wifiDiagReport{At: time.Now()}
	add := func(label, value string, state wifiDiagState) {
		if strings.TrimSpace(value) == "" {
			value = "—"
		}
		report.Entries = append(report.Entries, wifiDiagEntry{Label: label, Value: value, State: state})
	}

	iface := wifiInterface()
	if iface == "" {
		add("Interface", "none found", wifiDiagErr)
	} else {
		add("Interface", iface, wifiDiagOK)
	}
	add("Found by", wifiInterfaceSource(), wifiDiagInfo)

	backend := wifiCommandBackend()
	switch backend {
	case "":
		add("Backend", "none — no wpa_cli and no supplicant socket", wifiDiagErr)
	default:
		add("Backend", backend, wifiDiagOK)
	}

	if dir := wifiCtrlDirCached(iface); dir != "" {
		add("Control dir", dir, wifiDiagOK)
		if iface != "" {
			path := wpaCtrlSocketPath(dir, iface)
			if wpaCtrlSocketExists(dir, iface) {
				add("Control socket", path+" (present)", wifiDiagOK)
			} else {
				add("Control socket", path+" (missing)", wifiDiagErr)
			}
		}
	} else {
		add("Control dir", "not found in any candidate", wifiDiagWarn)
	}
	add("Searched", strings.Join(supplicantControlDirs(), "  "), wifiDiagInfo)

	if cfg := wifiSupplicantConfigPath(); cfg != "" {
		add("Supplicant config", cfg, wifiDiagOK)
	} else {
		add("Supplicant config", "none found", wifiDiagWarn)
	}
	if startedFor, note := supplicantStartNote(); note != "" {
		state := wifiDiagInfo
		if strings.Contains(note, "failed") {
			state = wifiDiagErr
		}
		label := "Supplicant start"
		if startedFor != "" {
			label += " (" + startedFor + ")"
		}
		add(label, note, state)
	} else {
		add("Supplicant start", "not attempted by JukaHub", wifiDiagInfo)
	}

	if on, ok := wifiRadioState(); ok {
		if on {
			add("Radio", "on ("+wifiRadioBackend()+")", wifiDiagOK)
		} else {
			add("Radio", "off ("+wifiRadioBackend()+")", wifiDiagWarn)
		}
	} else {
		add("Radio", "unknown — no rfkill or nmcli on this image", wifiDiagInfo)
	}

	if backend != "" {
		if st, err := wifiReadStatus(); err != nil {
			add("Station", firstLine(err.Error()), wifiDiagErr)
		} else {
			state := wifiDiagWarn
			if st.Connected {
				state = wifiDiagOK
			}
			add("Station", wifiStationSummary(st), state)
		}
	} else {
		add("Station", "not read — no backend", wifiDiagInfo)
	}

	if text, source, at := wifiLastError(); text != "" {
		label := "Last error"
		if source != "" {
			label += " (" + source + ")"
		}
		add(label, fmt.Sprintf("%s · %s ago", text, shortAge(time.Since(at))), wifiDiagErr)
	} else {
		add("Last error", "none this session", wifiDiagInfo)
	}
	return report
}

// shortAge formats how long ago something happened in the sheet's own words.
func shortAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// ── Opening, closing, refreshing ───────────────────────────────────────────

// openWiFiDiagnostics shows the sheet and starts collecting. It works whatever
// the picker's own state is: a panel that could not reach a backend is the case
// the sheet exists for.
func openWiFiDiagnostics() {
	if wifiPanel.diagOpen {
		return
	}
	wifiPanel.diagOpen = true
	wifiPanel.diagScroll = 0
	// A report older than the auto-refresh window is replaced by the worker
	// below; a fresh one is reused so opening the sheet twice is instant.
	if wifiPanel.diag.At.IsZero() || time.Since(wifiPanel.diag.At) > wifiAutoRefresh {
		wifiDiagRefreshAsync()
	}
	PlayActivateSound()
}

// closeWiFiDiagnostics returns to the network list. It never closes the panel:
// the sheet sits inside it, and B means "back one level" everywhere else.
func closeWiFiDiagnostics() {
	if !wifiPanel.diagOpen {
		return
	}
	wifiPanel.diagOpen = false
	wifiPanel.diagScroll = 0
	PlayBackSound()
}

// wifiDiagRefreshAsync re-reads the facts on a worker: probing the supplicant
// forks, and the render thread must not wait for it.
func wifiDiagRefreshAsync() {
	wifiStartWork("Reading Wi-Fi state…", func() wifiUpdate {
		report := collectWiFiDiagnostics()
		return wifiUpdate{kind: wifiUpdateDiag, diag: &report}
	})
}

// wifiDiagScroll moves the sheet by rows, clamped to what was last rendered.
func wifiDiagScroll(delta int) {
	wifiPanel.diagScroll += delta
	maxScroll := wifiDiagRowsTotal - wifiDiagRowsShown
	if maxScroll < 0 {
		maxScroll = 0
	}
	if wifiPanel.diagScroll > maxScroll {
		wifiPanel.diagScroll = maxScroll
	}
	if wifiPanel.diagScroll < 0 {
		wifiPanel.diagScroll = 0
	}
	PlayNavSound()
}

// ── Rendering ──────────────────────────────────────────────────────────────

// wifiDiagRow is one drawn line: the label, the value and the color. A value
// that wraps leaves the label blank on its continuation lines.
type wifiDiagRow struct {
	label string
	value string
	color sdl.Color
}

var (
	// wifiDiagRowsTotal and wifiDiagRowsShown are what the last frame drew, so
	// scrolling clamps to the list actually on screen.
	wifiDiagRowsTotal int
	wifiDiagRowsShown int
)

// wifiDiagColor maps a row's state onto the palette.
func wifiDiagColor(s wifiDiagState) sdl.Color {
	switch s {
	case wifiDiagOK:
		return ColorToastSuccess
	case wifiDiagWarn:
		return ColorToastWarn
	case wifiDiagErr:
		return ColorToastError
	}
	return ColorTextSecondary()
}

// wifiDiagSubtitle is the sheet's header line: the two facts that decide whether
// anything else can work.
func wifiDiagSubtitle() string {
	iface := wifiInterface()
	if iface == "" {
		iface = "no interface"
	}
	backend := wifiCommandBackend()
	if backend == "" {
		backend = "no backend"
	}
	return iface + " · " + backend
}

// wifiDiagSubtitleColor warns when either deciding fact is missing.
func wifiDiagSubtitleColor() sdl.Color {
	if wifiInterface() == "" || wifiCommandBackend() == "" {
		return ColorToastWarn
	}
	return ColorTextSecondary()
}

// renderWiFiDiagnostics draws the sheet over the scene. The list is drawn by
// renderWiFiPanel; this replaces it while the sheet is open.
func renderWiFiDiagnostics(renderer *sdl.Renderer, config *Config) {
	if renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	smallH := int32(font.Height())
	const padX = int32(24)

	panelW := int32(820)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := screenHeight - 40
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	wifiPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	// The sheet has no rows; a stale row rect would be a tap target on the list
	// that is not drawn.
	wifiRowRects = wifiRowRects[:0]

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	header := UIPanelHeader("Wi-Fi diagnostics", wifiDiagSubtitle(), "settings")
	header.SubtitleColor = wifiDiagSubtitleColor()
	header.X = panelX + padX
	header.Y = panelY + UIPanelHeaderInset
	header.W = panelW - 2*padX
	headerH := UIPanelHeaderBand(config, header, 14)
	UIPageHeaderDraw(renderer, config, header)

	footerH := 20 + smallH
	actionH := smallH + 18
	rowH := smallH + 10

	labelW := int32(168)
	if labelW > panelW/3 {
		labelW = panelW / 3
	}
	valueX := panelX + padX + labelW
	valueW := panelX + panelW - padX - valueX

	listTop := panelY + headerH
	listBottom := panelY + panelH - footerH - actionH
	fits := int((listBottom - listTop) / rowH)
	if fits < 1 {
		fits = 1
	}

	rows := wifiDiagRenderRows(font, wifiPanel.diag.Entries, valueW)
	maxScroll := len(rows) - fits
	if maxScroll < 0 {
		maxScroll = 0
	}
	if wifiPanel.diagScroll > maxScroll {
		wifiPanel.diagScroll = maxScroll
	}
	wifiDiagRowsTotal, wifiDiagRowsShown = len(rows), fits

	if len(rows) == 0 {
		empty := "Reading Wi-Fi state…"
		if wifiPanel.err != "" {
			empty = wifiPanel.err
		}
		renderText(renderer, config, font, ellipsize(font, empty, panelW-2*padX),
			ColorTextTertiary(), panelX+padX, listTop+8)
	} else {
		y := listTop
		for i := wifiPanel.diagScroll; i < len(rows) && i < wifiPanel.diagScroll+fits; i++ {
			row := rows[i]
			if row.label != "" {
				renderText(renderer, config, font, row.label, ColorTextTertiary(), panelX+padX, y)
			}
			renderText(renderer, config, font, row.value, row.color, valueX, y)
			y += rowH
		}
	}

	wifiPanelActionBar(renderer, config, font, panelX,
		panelY+panelH-footerH-actionH, panelW, actionH, smallH)

	footer := hintLine(FooterHint{"A", "Refresh"}, FooterHint{"B", "Back"})
	if len(rows) > fits {
		footer = hintLine(FooterHint{"Up/Down", "Scroll"}, FooterHint{"A", "Refresh"}, FooterHint{"B", "Back"})
	}
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-footerH+4)
}

// wifiDiagRenderRows turns the report into drawable lines, wrapping each value
// to two lines so a long path or error stays readable.
func wifiDiagRenderRows(font *ttf.Font, entries []wifiDiagEntry, maxW int32) []wifiDiagRow {
	rows := make([]wifiDiagRow, 0, len(entries))
	for _, e := range entries {
		color := wifiDiagColor(e.State)
		lines := wifiDiagWrap(font, e.Value, maxW, 2)
		for i, line := range lines {
			label := ""
			if i == 0 {
				label = e.Label
			}
			rows = append(rows, wifiDiagRow{
				label: label,
				value: ellipsize(font, line, maxW),
				color: color,
			})
		}
	}
	return rows
}

// wifiDiagWrap splits text into at most maxLines lines that fit maxW, preferring
// spaces and path separators as break points. A word that cannot fit on its own
// is cut, and the renderer's ellipsize marks the cut.
func wifiDiagWrap(font *ttf.Font, text string, maxW int32, maxLines int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{"—"}
	}
	if maxLines < 1 {
		maxLines = 1
	}
	if font == nil || maxW <= 0 {
		return []string{text}
	}
	if fits, _, _ := font.SizeUTF8(text); fits <= int(maxW) {
		return []string{text}
	}
	var lines []string
	remaining := []rune(text)
	for len(lines) < maxLines && len(remaining) > 0 {
		last := len(lines) == maxLines-1
		if last {
			lines = append(lines, string(remaining))
			break
		}
		// Longest prefix that fits.
		lo, hi := 0, len(remaining)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if w, _, _ := font.SizeUTF8(string(remaining[:mid])); w <= int(maxW) {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		if lo == 0 {
			lo = 1
		}
		// Prefer breaking after a space, slash or comma inside the prefix.
		cut := lo
		for i := lo - 1; i > 0; i-- {
			if remaining[i-1] == ' ' || remaining[i-1] == '/' || remaining[i-1] == ',' {
				cut = i
				break
			}
		}
		lines = append(lines, strings.TrimRight(string(remaining[:cut]), " /,"))
		remaining = []rune(strings.TrimLeft(string(remaining[cut:]), " ,"))
	}
	return lines
}
