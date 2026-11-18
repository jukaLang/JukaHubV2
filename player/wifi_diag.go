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
	"strconv"
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

// wifiDiagLast keeps the newest collected report for the diagnostics bundle.
// The sheet's copy lives on the event thread; the bundle is built from a worker,
// so this guarded snapshot is the one piece both may touch. A report is written
// here by the collection itself, which means the bundle carries Wi-Fi facts even
// when the sheet is opened after the export, or not at all.
var wifiDiagLast struct {
	mu     sync.Mutex
	report wifiDiagReport
}

// rememberWiFiDiagReport stores a collection for the support bundle.
func rememberWiFiDiagReport(report wifiDiagReport) {
	wifiDiagLast.mu.Lock()
	wifiDiagLast.report = report
	wifiDiagLast.mu.Unlock()
}

// lastWiFiDiagReport returns the stored collection, zero when none has run.
func lastWiFiDiagReport() wifiDiagReport {
	wifiDiagLast.mu.Lock()
	defer wifiDiagLast.mu.Unlock()
	return wifiDiagLast.report
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
		var marked []string
		for _, name := range names {
			if isWirelessNetdev(wifiSysfsNetRoot, name) {
				marked = append(marked, name)
			}
		}
		if len(marked) > 0 {
			return "sysfs marker in " + filepath.Join(wifiSysfsNetRoot, stationIfacesFirst(marked)[0])
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

// wifiChipFacts is what the kernel says about the radio behind an interface.
type wifiChipFacts struct {
	Iface   string
	Chip    string // "AIC8800", "XR829", … or "" when nothing names it
	Module  string // kernel module of the interface's driver ("aic8800_fdrv")
	Driver  string // bus driver bound to the device ("aicwf_sdio")
	Up      bool   // the interface is administratively up (IFF_UP)
	Guess   bool   // Chip comes from the model's table, not from the kernel
	Bands   string // what the chip can use, for people
	FiveGHz bool   // the chip can use 5 GHz channels
}

// wifiChipName names the chip a driver or module belongs to, "" when unknown.
// Pure.
func wifiChipName(names ...string) string {
	for _, n := range names {
		n = strings.ToLower(n)
		switch {
		case n == "":
		case strings.HasPrefix(n, "aic8800"), strings.HasPrefix(n, "aicwf"), strings.HasPrefix(n, "aicbsp"):
			return "AIC8800"
		case strings.HasPrefix(n, "xradio"), strings.HasPrefix(n, "xr829"):
			return "XR829"
		case strings.HasPrefix(n, "xr819"):
			return "XR819"
		case strings.HasPrefix(n, "rtl"), strings.HasPrefix(n, "8189"), strings.HasPrefix(n, "8723"), strings.HasPrefix(n, "8821"), strings.HasPrefix(n, "88x2"):
			return "Realtek"
		case strings.HasPrefix(n, "brcmfmac"), strings.HasPrefix(n, "bcmdhd"):
			return "Broadcom"
		}
	}
	return ""
}

// wifiChipBands is what a chip can use, for people, and whether that includes
// 5 GHz. The Brick Pro's store page lists 802.11 b/g/n only; the Smart Pro S's
// AIC8800D80 is Wi-Fi 6 on both bands.
func wifiChipBands(chip string) (string, bool) {
	switch chip {
	case "AIC8800":
		return "Wi-Fi 6 · 2.4 and 5 GHz", true
	case "XR829", "XR819":
		return "802.11 b/g/n · 2.4 GHz", false
	}
	return "", false
}

// wifiReadChip reads the radio's chip and driver: the driver bound to the
// interface's device and its module first, then which combo driver the kernel
// has loaded, then the model's table (marked a guess). Files only, no program
// started; it still walks sysfs, so it runs on a worker.
func wifiReadChip(iface string) wifiChipFacts {
	f := wifiChipFacts{Iface: iface}
	if iface != "" {
		dev := filepath.Join(wifiSysfsNetRoot, iface, "device")
		if p, err := filepath.EvalSymlinks(filepath.Join(dev, "driver")); err == nil {
			f.Driver = filepath.Base(p)
		}
		if p, err := filepath.EvalSymlinks(filepath.Join(dev, "driver", "module")); err == nil {
			f.Module = filepath.Base(p)
		}
		if b, err := os.ReadFile(filepath.Join(wifiSysfsNetRoot, iface, "flags")); err == nil {
			hex := strings.TrimPrefix(strings.TrimSpace(string(b)), "0x")
			if v, err := strconv.ParseUint(hex, 16, 32); err == nil {
				f.Up = v&1 != 0
			}
		}
	}
	f.Chip = wifiChipName(f.Module, f.Driver)
	if f.Chip == "" {
		aic, xradio, loaded := radioChipModules()
		switch {
		case aic && !xradio:
			f.Chip = "AIC8800"
		case xradio && !aic:
			f.Chip = "XR829"
		}
		if f.Chip != "" && f.Module == "" {
			for _, m := range loaded {
				if wifiChipName(m) == f.Chip && !strings.Contains(m, "btlpm") && !strings.HasSuffix(m, "_bsp") {
					f.Module = m
					break
				}
			}
		}
	}
	if f.Chip == "" {
		if u, ok := btChipUART(btBoardModel()); ok {
			f.Chip, f.Guess = btChipLabel(u.chip), true
		}
	}
	f.Bands, f.FiveGHz = wifiChipBands(f.Chip)
	return f
}

// summary is the chip in one line: "AIC8800 (aic8800_fdrv) · wlan0 up · Wi-Fi 6
// · 2.4 and 5 GHz".
func (f wifiChipFacts) summary() string {
	parts := make([]string, 0, 3)
	chip := f.Chip
	if chip == "" {
		chip = "unknown chip"
	}
	if f.Guess {
		chip += " (expected for this model; no driver named it)"
	}
	switch {
	case f.Module != "":
		chip += " (" + f.Module + ")"
	case f.Driver != "":
		chip += " (" + f.Driver + ")"
	}
	parts = append(parts, chip)
	if f.Iface != "" {
		state := "down"
		if f.Up {
			state = "up"
		}
		parts = append(parts, f.Iface+" "+state)
	} else {
		parts = append(parts, "no interface")
	}
	if f.Bands != "" {
		parts = append(parts, f.Bands)
	}
	return strings.Join(parts, " · ")
}

func init() { registerDriverStatus(driverRowWiFi, wifiDriverStatus) }

// wifiDriverStatus is the Wi-Fi row of the Drivers & hardware page
// (devicedrivers.go): "AIC8800 (aic8800_fdrv)" on wlan0, "up · Wi-Fi 6 · 2.4
// and 5 GHz". File reads only: the interface comes from sysfs, /proc and the
// supplicant's socket directory, never from wpa_cli (the page's functions
// must not start a process that can hang).
func wifiDriverStatus() driverStatus {
	if winWifiActive() {
		return driverStatus{State: driverUnknown, Driver: "Windows Wi-Fi", Detail: "the system drives the radio"}
	}
	iface := ""
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_WIFI_IFACE")); validIfaceName(override) {
		iface = override
	} else if names := wifiIfacesFromFiles(); len(names) > 0 {
		iface = names[0]
	}
	f := wifiReadChip(iface)
	st := driverStatus{State: driverOK, Node: iface}
	switch {
	case f.Module != "" && f.Chip != "" && !f.Guess:
		st.Driver = f.Chip + " (" + f.Module + ")"
	case f.Chip != "":
		st.Driver = f.Chip
		if f.Guess {
			st.Driver += " (expected)"
		}
	case f.Driver != "":
		st.Driver = f.Driver
	}
	detail := make([]string, 0, 2)
	if iface == "" {
		if f.Chip == "" {
			return driverStatus{State: driverNotUsed}
		}
		st.State = driverMissing
		detail = append(detail, "no wireless interface — Wi-Fi may be switched off")
	} else if f.Up {
		detail = append(detail, "up")
	} else {
		detail = append(detail, "down")
	}
	if f.Bands != "" {
		detail = append(detail, f.Bands)
	}
	st.Detail = strings.Join(detail, " · ")
	return st
}

// wifiIfacesFromFiles is wirelessInterfaceNames without its one forking
// source (asking wpa_cli): sysfs markers, /proc/net/wireless, the supplicant's
// socket directory, then the naming convention.
func wifiIfacesFromFiles() []string {
	names, _ := sysfsNetdevs(wifiSysfsNetRoot)
	var marked, conventional []string
	for _, name := range names {
		if isWirelessNetdev(wifiSysfsNetRoot, name) {
			marked = append(marked, name)
		} else if looksLikeWirelessName(name) {
			conventional = append(conventional, name)
		}
	}
	if len(marked) > 0 {
		return stationIfacesFirst(marked)
	}
	if data, err := os.ReadFile(wifiProcWirelessPath); err == nil {
		if listed := parseWirelessProcIfaces(string(data)); len(listed) > 0 {
			return stationIfacesFirst(listed)
		}
	}
	if viaSupplicant := supplicantInterfaceNames(supplicantControlDirs()); len(viaSupplicant) > 0 {
		return viaSupplicant
	}
	return stationIfacesFirst(conventional)
}

// parseSupplicantCountry reads the country= line out of a wpa_supplicant config
// ("country=US", optionally quoted), "" when it has none. Pure.
func parseSupplicantCountry(text string) string {
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(strings.TrimRight(line, "\r")), "=")
		if !ok || strings.TrimSpace(key) != "country" {
			continue
		}
		cc := strings.ToUpper(strings.Trim(strings.TrimSpace(value), `"`))
		if len(cc) == 2 && cc[0] >= 'A' && cc[0] <= 'Z' && cc[1] >= 'A' && cc[1] <= 'Z' {
			return cc
		}
	}
	return ""
}

// wifiSupplicantCountry is the regulatory country the image's supplicant config
// sets, "" when it sets none.
func wifiSupplicantCountry() string {
	cfg := wifiSupplicantConfigPath()
	if cfg == "" {
		return ""
	}
	data, err := os.ReadFile(cfg)
	if err != nil {
		return ""
	}
	return parseSupplicantCountry(string(data))
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
	if !winWifiActive() {
		chip := wifiReadChip(iface)
		add("Chip", chip.summary(), wifiDiagInfo)
		if chip.FiveGHz {
			if cc := wifiSupplicantCountry(); cc != "" {
				add("Country", cc+" (sets the 5 GHz channels)", wifiDiagOK)
			} else {
				add("Country", "not set in the supplicant config — the driver's default decides the 5 GHz channels", wifiDiagInfo)
			}
		}
	}

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

	// What the keeper (wifi_keeper.go) did last: the evidence for "it does not
	// reconnect by itself".
	if text, at := wifiKeeperLastNote(); text != "" {
		add("Auto-connect", fmt.Sprintf("%s · %s ago", text, shortAge(time.Since(at))), wifiDiagInfo)
	} else if wifiKeeperStarted.Load() {
		add("Auto-connect", "watching — nothing needed yet", wifiDiagInfo)
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

// ── The report as text ─────────────────────────────────────────────────────

// wifiDiagReportText renders the sheet as plain text: the same labels and
// values, one aligned pair per line, under a header that dates the collection.
// Copy puts this on the clipboard and the bundle carries the same words, so what
// a user pastes into a support thread and what is attached to it cannot drift.
func wifiDiagReportText(report wifiDiagReport) string {
	var sb strings.Builder
	sb.WriteString("Wi-Fi diagnostics\n")
	sb.WriteString("-----------------\n")
	if report.At.IsZero() {
		sb.WriteString("Collected    : not yet — the sheet is still reading the radio\n")
		return sb.String()
	}
	// The label column follows the longest row, with the 13-column field
	// DiagnosticsText uses as the floor, so the finished block reads as a table.
	width := 13
	for _, e := range report.Entries {
		if len(e.Label) > width {
			width = len(e.Label)
		}
	}
	field := func(label string) string {
		if n := width - len(label); n > 0 {
			return label + strings.Repeat(" ", n)
		}
		return label
	}
	sb.WriteString(field("Collected") + ": " + report.At.Local().Format("Jan 2 2006 15:04:05") + "\n")
	if len(report.Entries) == 0 {
		sb.WriteString("No facts were read.\n")
		return sb.String()
	}
	for _, e := range report.Entries {
		sb.WriteString(field(e.Label) + ": " + e.Value + "\n")
	}
	return sb.String()
}

// wifiDiagBundleFacts is the sheet's report as it joins the diagnostics bundle.
// Redaction runs here because this file leaves the device: control directories
// and the supplicant's config path can carry an account name. A session that
// never collected a report says so instead of reading like a device with no
// Wi-Fi facts.
func wifiDiagBundleFacts() string {
	report := lastWiFiDiagReport()
	if report.At.IsZero() {
		return "Wi-Fi diagnostics\n-----------------\nNot collected this session: open Wi-Fi → Diagnostics to read the radio.\n"
	}
	return redactDiagnosticText(wifiDiagReportText(report))
}

// ── Opening, closing, refreshing ───────────────────────────────────────────

// openWiFiDiagnostics marks the sheet open and starts collecting. It works
// whatever the page's own state is: a page that could not reach a backend is
// the case the sheet exists for.
func openWiFiDiagnostics() {
	if wifiPanel.diagOpen {
		return
	}
	wifiPanel.diagOpen = true
	// A report older than the auto-refresh window is replaced by the worker
	// below; a fresh one is reused so opening the sheet twice is instant.
	if wifiPanel.diag.At.IsZero() || time.Since(wifiPanel.diag.At) > wifiAutoRefresh {
		wifiDiagRefreshAsync()
	}
	PlayActivateSound()
}

// closeWiFiDiagnostics returns to the network list. It never leaves the page:
// the sheet sits on it, and B means "back one level" everywhere else.
func closeWiFiDiagnostics() {
	if !wifiPanel.diagOpen {
		return
	}
	wifiPanel.diagOpen = false
	PlayBackSound()
}

// wifiDiagRefreshAsync re-reads the facts on a worker: probing the supplicant
// forks, and the render thread must not wait for it.
func wifiDiagRefreshAsync() {
	wifiStartWork("Reading Wi-Fi state…", func() wifiUpdate {
		report := collectWiFiDiagnostics()
		rememberWiFiDiagReport(report)
		return wifiUpdate{kind: wifiUpdateDiag, diag: &report}
	})
}

// wifiDiagCopyReport is the sheet's Copy action: the report on the clipboard for
// a support chat, and the same facts in a diagnostics bundle on the card. The
// two destinations are attempted independently, because a platform that
// refuses the clipboard must not cost the file and a full card must not cost the
// paste. The report is the one on screen, so copying what the user just read is
// the guarantee; the bundle adds the log and the cookie facts around it.
func wifiDiagCopyReport() {
	if len(wifiPanel.diag.Entries) == 0 {
		ShowToast("The report is still being collected — try again in a moment", ToastKindInfo)
		return
	}
	copyErr := copyTextToClipboard(wifiDiagReportText(wifiPanel.diag), "Wi-Fi diagnostics")
	_, bundleErr := ExportDiagnosticsBundle(appConfig)
	switch {
	case copyErr == nil && bundleErr == nil:
		ShowToast("Wi-Fi report copied · bundle saved", ToastKindSuccess)
	case copyErr == nil:
		ShowToast("Wi-Fi report copied · bundle failed: "+bundleErr.Error(), ToastKindWarning)
	case bundleErr == nil:
		ShowToast("Bundle saved · could not copy: "+copyErr.Error(), ToastKindWarning)
	default:
		ShowToast("Could not copy: "+copyErr.Error(), ToastKindWarning)
	}
}

// ── Rendering ──────────────────────────────────────────────────────────────
//
// The Wi-Fi page draws the sheet (wifiscreen.go); these turn the report into
// its rows.

// wifiDiagRow is one drawn line: the label, the value and the color. A value
// that wraps leaves the label blank on its continuation lines.
type wifiDiagRow struct {
	label string
	value string
	color sdl.Color
}

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
