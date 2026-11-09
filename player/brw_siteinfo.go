package main

// brw_siteinfo.go — "Site info" for the page on screen.
//
// The HTTP check, the TLS certificate report and the security-headers audit
// used to be three rows in Tools that asked you to type a URL. They are about
// the page you are reading, so they live in the browser now: the padlock at the
// left of the address bar (or the Info chip, `i` on a keyboard, R3 on a pad —
// F2 on a Brick) opens a sheet over the page with three tabs:
//
//   - Connection: HTTP status, timing, server, type and size (httpCheckSummary)
//   - Certificate: issuer, validity, key, names and trust (certSummary); an
//     http:// page says "Not encrypted" without making a connection
//   - Security headers: which of the six hardening headers the server sends
//     (headerAuditSummary)
//
// All three run on their own goroutines as soon as the sheet opens, so
// switching tabs is instant, and the results are kept per URL for the session.
// Offline, the tabs say they need internet and start by themselves when the
// connection returns.

import (
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	brwInfoConnection = iota
	brwInfoCertificate
	brwInfoHeaders
	brwInfoTabs
)

// brwInfoTabNames are the tab labels.
var brwInfoTabNames = [brwInfoTabs]string{"Connection", "Certificate", "Security headers"}

// brwInfoRunners run the three reports. They are variables so tests can stand
// in for the network.
var brwInfoRunners = [brwInfoTabs]func(string) (string, error){
	httpCheckSummary,
	certSummary,
	headerAuditSummary,
}

// Result states.
const (
	brwInfoIdle = iota
	brwInfoLoading
	brwInfoDone
	brwInfoFailed
)

// Status tones.
const (
	brwToneNeutral = iota
	brwToneGood
	brwToneWarn
	brwToneBad
)

// brwInfoRow is one line of a report: a labelled value, a header that is
// present (Mark 1) or missing (Mark -1), or a note (no label).
type brwInfoRow struct {
	Label string
	Value string
	Mark  int
}

// brwInfoResult is one tab's report for one URL.
type brwInfoResult struct {
	State  int
	Rows   []brwInfoRow
	Status string
	Tone   int
	Err    string
}

// brwInfoEntry is everything known about one URL.
type brwInfoEntry struct {
	results [brwInfoTabs]brwInfoResult
	gen     [brwInfoTabs]int
	used    time.Time
}

// brwInfoCacheLimit bounds the URLs remembered this session.
const brwInfoCacheLimit = 24

var brwInfoCache = struct {
	mu      sync.Mutex
	entries map[string]*brwInfoEntry
}{entries: map[string]*brwInfoEntry{}}

// brwInfoState is the open sheet.
type brwInfoState struct {
	open      bool
	url       string
	host      string
	https     bool
	tab       int
	scroll    [brwInfoTabs]int32
	scrollMax [brwInfoTabs]int32
	card      sdl.Rect
	tabRects  [brwInfoTabs]sdl.Rect
	recheck   sdl.Rect
	close     sdl.Rect
	wifi      sdl.Rect
}

var brwInfo brwInfoState

// brwInfoForget closes the sheet without a sound (a scene change).
func brwInfoForget() {
	brwInfo = brwInfoState{}
}

// brwInfoPageURL is the address of the page on screen: the final URL of the
// document when one is drawn, the address bar otherwise, "" when there is no
// page.
func brwInfoPageURL(config *Config) string {
	if config == nil {
		return ""
	}
	if elem, ok := findBrowserElement(config); ok {
		if doc := browserDocumentFor(elem.Variable); doc != nil && strings.TrimSpace(doc.URL) != "" {
			return strings.TrimSpace(doc.URL)
		}
	}
	address, _ := config.Variables.Custom[textBrowserURLVariable].(string)
	return strings.TrimSpace(address)
}

// brwInfoOpenFor opens the sheet for the page on screen.
func brwInfoOpenFor(config *Config) {
	address := brwInfoPageURL(config)
	u, err := url.Parse(address)
	if address == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		ShowToast("Open a page first to see its site info", ToastKindInfo)
		return
	}
	brwInfo = brwInfoState{open: true, url: u.String(), host: u.Hostname(), https: u.Scheme == "https"}
	brwInfoEnsure(brwInfo.url, brwInfo.https)
	PlayActivateSound()
}

func brwInfoClose() {
	if !brwInfo.open {
		return
	}
	brwInfoForget()
	PlayBackSound()
}

// brwInfoEnsure starts every report the URL does not have yet. Offline nothing
// starts: the sheet says it needs internet and calls this again when the
// connection is back.
func brwInfoEnsure(address string, https bool) {
	if !IsOnline() {
		return
	}
	brwInfoCache.mu.Lock()
	defer brwInfoCache.mu.Unlock()
	e := brwInfoEntryLocked(address)
	for tab := 0; tab < brwInfoTabs; tab++ {
		if e.results[tab].State != brwInfoIdle {
			continue
		}
		if tab == brwInfoCertificate && !https {
			e.results[tab] = brwInfoResult{
				State:  brwInfoDone,
				Status: "Not encrypted",
				Tone:   brwToneWarn,
				Rows: []brwInfoRow{
					{Label: "Scheme", Value: "http:// — no certificate, no encryption"},
					{Value: "Anything this page sends or receives can be read or changed on the way. Prefer the https:// address when the site has one."},
				},
			}
			continue
		}
		e.gen[tab]++
		gen := e.gen[tab]
		e.results[tab] = brwInfoResult{State: brwInfoLoading}
		run := brwInfoRunners[tab]
		go brwInfoRun(e, tab, gen, address, run)
	}
}

// brwInfoEntryLocked returns the entry for a URL, making room in the cache
// when it is full. The caller holds the lock.
func brwInfoEntryLocked(address string) *brwInfoEntry {
	e, ok := brwInfoCache.entries[address]
	if !ok {
		if len(brwInfoCache.entries) >= brwInfoCacheLimit {
			oldest, oldestAt := "", time.Time{}
			for key, other := range brwInfoCache.entries {
				if oldest == "" || other.used.Before(oldestAt) {
					oldest, oldestAt = key, other.used
				}
			}
			delete(brwInfoCache.entries, oldest)
		}
		e = &brwInfoEntry{}
		brwInfoCache.entries[address] = e
	}
	e.used = time.Now()
	return e
}

// brwInfoRun is one report on its goroutine. A re-check that started after it
// wins: a result is only stored while its generation is current.
func brwInfoRun(e *brwInfoEntry, tab, gen int, address string, run func(string) (string, error)) {
	result := brwInfoResult{State: brwInfoFailed, Err: "This check could not run."}
	defer func() {
		if recover() != nil {
			result = brwInfoResult{State: brwInfoFailed, Err: "This check could not run."}
		}
		brwInfoCache.mu.Lock()
		if e.gen[tab] == gen {
			e.results[tab] = result
		}
		brwInfoCache.mu.Unlock()
	}()
	if run == nil {
		return
	}
	report, err := run(address)
	if err != nil {
		result = brwInfoResult{State: brwInfoFailed, Err: brwFriendlyNetError(err)}
		return
	}
	rows := brwInfoParse(report)
	status, tone := brwInfoStatus(tab, rows)
	result = brwInfoResult{State: brwInfoDone, Rows: rows, Status: status, Tone: tone}
}

// brwInfoResultFor reads one tab's result.
func brwInfoResultFor(address string, tab int) brwInfoResult {
	brwInfoCache.mu.Lock()
	defer brwInfoCache.mu.Unlock()
	if e, ok := brwInfoCache.entries[address]; ok && tab >= 0 && tab < brwInfoTabs {
		return e.results[tab]
	}
	return brwInfoResult{}
}

// brwInfoRecheck drops a URL's reports and runs them again.
func brwInfoRecheck() {
	if !brwInfo.open {
		return
	}
	brwInfoCache.mu.Lock()
	if e, ok := brwInfoCache.entries[brwInfo.url]; ok {
		for tab := range e.results {
			e.results[tab] = brwInfoResult{}
		}
	}
	brwInfoCache.mu.Unlock()
	brwInfo.scroll = [brwInfoTabs]int32{}
	brwInfoEnsure(brwInfo.url, brwInfo.https)
	PlayActivateSound()
}

// brwInfoParse turns a report's "Label:  value" lines into rows. The headers
// audit's "  + Name value" / "  - Name missing · why" lines become marked rows,
// and anything else (the "Read-only" footnote, a warning sentence) is a note.
func brwInfoParse(report string) []brwInfoRow {
	var rows []brwInfoRow
	for _, raw := range strings.Split(report, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(line, "  + ") || strings.HasPrefix(line, "  - ") {
			mark := 1
			if line[2] == '-' {
				mark = -1
			}
			name, value, _ := strings.Cut(strings.TrimSpace(line[4:]), " ")
			rows = append(rows, brwInfoRow{Label: name, Value: strings.TrimSpace(value), Mark: mark})
			continue
		}
		if label, value, ok := strings.Cut(trimmed, ":"); ok && brwInfoIsLabel(label) {
			rows = append(rows, brwInfoRow{Label: label, Value: strings.TrimSpace(value)})
			continue
		}
		rows = append(rows, brwInfoRow{Value: trimmed})
	}
	return rows
}

// brwInfoIsLabel reports whether text before a colon is a report label (one
// short capitalised word or two) rather than part of a sentence.
func brwInfoIsLabel(label string) bool {
	if label == "" || len(label) > 14 || strings.EqualFold(label, "read-only") || strings.EqualFold(label, "note") {
		return false
	}
	if label[0] < 'A' || label[0] > 'Z' {
		return false
	}
	for _, r := range label {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == ' ') {
			return false
		}
	}
	return true
}

// brwInfoValue is the value of the first row with a label.
func brwInfoValue(rows []brwInfoRow, label string) string {
	for _, row := range rows {
		if strings.EqualFold(row.Label, label) && row.Mark == 0 {
			return row.Value
		}
	}
	return ""
}

// brwInfoStatus is the one-line verdict at the top of a tab and its tone.
func brwInfoStatus(tab int, rows []brwInfoRow) (string, int) {
	switch tab {
	case brwInfoConnection:
		status := brwInfoValue(rows, "Status")
		fields := strings.Fields(status)
		if len(fields) == 0 {
			return "Answered", brwToneNeutral
		}
		code, _ := strconv.Atoi(fields[0])
		text := status
		if t := brwInfoValue(rows, "Time"); t != "" {
			text += " · " + t
		}
		switch {
		case code >= 500:
			return text, brwToneBad
		case code >= 400:
			return text, brwToneWarn
		case code >= 200:
			return text, brwToneGood
		}
		return text, brwToneNeutral
	case brwInfoCertificate:
		trust := strings.ToLower(brwInfoValue(rows, "Trust"))
		expires := brwInfoValue(rows, "Expires")
		left := ""
		if i := strings.Index(expires, "("); i >= 0 {
			left = strings.TrimSuffix(expires[i+1:], ")")
		}
		switch {
		case strings.Contains(expires, "expired"):
			return "Expired certificate", brwToneBad
		case strings.HasPrefix(trust, "not trusted"):
			return "Not trusted by this device", brwToneBad
		case left != "":
			days := 0
			if f := strings.Fields(left); len(f) > 0 {
				days, _ = strconv.Atoi(f[0])
			}
			if strings.Contains(left, "today") || (days > 0 && days < 15) {
				return "Trusted · " + left, brwToneWarn
			}
			return "Trusted · " + left, brwToneGood
		}
		return "Certificate received", brwToneNeutral
	case brwInfoHeaders:
		headers := brwInfoValue(rows, "Headers")
		present, total := 0, 0
		if fields := strings.Fields(headers); len(fields) >= 3 {
			present, _ = strconv.Atoi(fields[0])
			total, _ = strconv.Atoi(fields[2])
		}
		if total == 0 {
			return "Headers checked", brwToneNeutral
		}
		text := strconv.Itoa(present) + " of " + strconv.Itoa(total) + " security headers"
		switch {
		case present >= total-1:
			return text, brwToneGood
		case present*2 >= total:
			return text, brwToneWarn
		}
		return text, brwToneBad
	}
	return "", brwToneNeutral
}

// brwToneColor is the ink a tone is written in.
func brwToneColor(tone int) sdl.Color {
	switch tone {
	case brwToneGood:
		return SuccessInk()
	case brwToneWarn:
		return WarningInk()
	case brwToneBad:
		return DangerInk()
	}
	return ColorTextSecondary()
}

// ── Input ────────────────────────────────────────────────────────────────

func brwInfoSwitch(delta int) {
	brwInfo.tab = ((brwInfo.tab+delta)%brwInfoTabs + brwInfoTabs) % brwInfoTabs
	PlayNavSound()
}

func brwInfoScroll(delta int32) {
	t := brwInfo.tab
	brwInfo.scroll[t] = max(0, min(brwInfo.scroll[t]+delta, brwInfo.scrollMax[t]))
}

// brwInfoController owns the pad while the sheet is open.
func brwInfoController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil {
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_RIGHTSTICK:
		brwInfoClose()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER, sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		brwInfoSwitch(-1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		brwInfoSwitch(1)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		brwInfoScroll(-40)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		brwInfoScroll(40)
	case sdl.CONTROLLER_BUTTON_Y:
		brwInfoRecheck()
	case sdl.CONTROLLER_BUTTON_A:
		if !IsOnline() {
			brwInfoClose()
			wifiScrOpen(config)
		}
	}
	return true
}

// brwInfoKey owns the keyboard while the sheet is open.
func brwInfoKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !brwInfo.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_i:
		brwInfoClose()
	case sdl.K_LEFT:
		brwInfoSwitch(-1)
	case sdl.K_RIGHT, sdl.K_TAB:
		brwInfoSwitch(1)
	case sdl.K_1, sdl.K_2, sdl.K_3:
		brwInfo.tab = int(e.Keysym.Sym - sdl.K_1)
		PlayNavSound()
	case sdl.K_UP:
		brwInfoScroll(-40)
	case sdl.K_DOWN:
		brwInfoScroll(40)
	case sdl.K_PAGEUP:
		brwInfoScroll(-240)
	case sdl.K_PAGEDOWN:
		brwInfoScroll(240)
	case sdl.K_r, sdl.K_F5:
		brwInfoRecheck()
	case sdl.K_RETURN:
		if !IsOnline() {
			brwInfoClose()
			wifiScrOpen(config)
		}
	}
	return true
}

// brwInfoMouse takes a click while the sheet is open: tabs, Re-check, the
// close button, Wi-Fi when offline; outside the card closes it.
func brwInfoMouse(mx, my int32, config *Config) bool {
	if !brwInfo.open {
		return false
	}
	for i, rect := range brwInfo.tabRects {
		if rect.W > 0 && pointInRect(mx, my, rect) {
			if brwInfo.tab != i {
				brwInfo.tab = i
				PlayNavSound()
			}
			return true
		}
	}
	switch {
	case brwInfo.close.W > 0 && hitTouchTarget(brwInfo.close, mx, my):
		brwInfoClose()
	case brwInfo.recheck.W > 0 && hitTouchTarget(brwInfo.recheck, mx, my):
		brwInfoRecheck()
	case brwInfo.wifi.W > 0 && pointInRect(mx, my, brwInfo.wifi):
		brwInfoClose()
		wifiScrOpen(config)
	case brwInfo.card.W > 0 && !pointInRect(mx, my, brwInfo.card):
		brwInfoClose()
	}
	return true
}

// brwInfoWheel scrolls the open tab.
func brwInfoWheel(dy int32) bool {
	if !brwInfo.open {
		return false
	}
	brwInfoScroll(-dy * 48)
	return true
}

// brwPadlockRect is the site-info button at the left end of the address
// field: the lock (https), the open lock (http) or the info mark.
func brwPadlockRect(config *Config, elem Element) sdl.Rect {
	address, _ := textBrowserToolbarRects(config, elem)
	if address.W < 80 {
		return sdl.Rect{}
	}
	size := address.H - 8
	return sdl.Rect{X: address.X + 4, Y: address.Y + 4, W: size, H: size}
}

// brwPadlockTap opens the sheet when a tap lands on the padlock.
func brwPadlockTap(config *Config, elem Element, mx, my int32) bool {
	rect := brwPadlockRect(config, elem)
	if rect.W <= 0 || !pointInRect(mx, my, textBrowserTouchTarget(rect)) {
		return false
	}
	brwInfoOpenFor(config)
	return true
}

// brwDrawPadlock draws the padlock button: a green-washed lock for a page that
// arrived over https, an amber open lock for http, a neutral info mark while
// nothing is loaded.
func brwDrawPadlock(r *sdl.Renderer, rect sdl.Rect, doc *webDocument) {
	if r == nil || rect.W <= 0 {
		return
	}
	icon, wash := "info", glassChipFill()
	if doc != nil && strings.TrimSpace(doc.URL) != "" {
		if browserHTTPSAddress(doc.URL) {
			icon, wash = "lock", WithAlpha(ColorSuccess, 60)
		} else {
			icon, wash = "unlock", WithAlpha(ColorWarning, 70)
		}
	}
	fillRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, rect.H/2, wash)
	inset := rect.W / 5
	DrawHomeIcon(r, icon, sdl.Rect{X: rect.X + inset, Y: rect.Y + inset, W: rect.W - 2*inset, H: rect.H - 2*inset}, false)
}

// ── Sheet ────────────────────────────────────────────────────────────────

// brwInfoRender draws the open sheet over the browser.
func brwInfoRender(r *sdl.Renderer, config *Config) {
	if r == nil || config == nil || !brwInfo.open {
		return
	}
	// Opened offline: start the moment the connection is back.
	if IsOnline() && brwInfoResultFor(brwInfo.url, brwInfo.tab).State == brwInfoIdle {
		brwInfoEnsure(brwInfo.url, brwInfo.https)
	}
	small, _ := getCachedFont(config, "small")
	body := getFontSized(config, "small", 17)
	label := getFontSized(config, "small", 15)
	if small == nil || body == nil || label == nil {
		return
	}

	cardW := min(screenWidth-64, int32(900))
	cardH := screenHeight - 96
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	brwInfo.card = card
	drawOverlayScrim(r)
	drawOverlayCard(r, card)
	drawOverlayCardAccent(r, card.X, card.Y, card.W)

	glyph := "unlock"
	subtitle := brwInfo.host + " · not encrypted (http)"
	if brwInfo.https {
		glyph = "lock"
		subtitle = brwInfo.host + " · encrypted (https)"
	}
	header := UIPanelHeader("Site info", subtitle, glyph)
	headerH := UIPanelHeaderBand(config, header, 10)
	UIPageHeaderDraw(r, config, overlayHeaderOnCard(header, card))

	// The close button in the corner, for a mouse or a finger.
	closeSize := int32(36)
	brwInfo.close = sdl.Rect{X: card.X + card.W - OverlayPadX - closeSize, Y: card.Y + UIPanelHeaderInset, W: closeSize, H: closeSize}
	drawGlassSurface(r, brwInfo.close, closeSize/2, glassState{})
	DrawHomeIcon(r, "close", sdl.Rect{X: brwInfo.close.X + 9, Y: brwInfo.close.Y + 9, W: closeSize - 18, H: closeSize - 18}, false)

	// Tabs: a segmented row under the header.
	x := card.X + OverlayPadX
	y := card.Y + headerH
	tabH := int32(small.Height()) + 14
	for i, name := range brwInfoTabNames {
		tw, th := measureText(small, name)
		rect := sdl.Rect{X: x, Y: y, W: tw + 32, H: tabH}
		brwInfo.tabRects[i] = rect
		if i == brwInfo.tab {
			fillRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, rect.H/2, glassAccent())
			renderText(r, config, small, name, contrastText(glassAccent()), rect.X+16, rect.Y+(rect.H-th)/2)
		} else {
			drawGlassSurface(r, rect, rect.H/2, glassState{})
			renderText(r, config, small, name, ColorTextSecondary(), rect.X+16, rect.Y+(rect.H-th)/2)
		}
		x += rect.W + 8
	}
	y += tabH + 14

	// Status line: the verdict pill, and Re-check on the right.
	res := brwInfoResultFor(brwInfo.url, brwInfo.tab)
	recheckLabel := "Re-check"
	rw, rh := measureText(label, recheckLabel)
	brwInfo.recheck = sdl.Rect{X: card.X + card.W - OverlayPadX - rw - 30, Y: y, W: rw + 30, H: rh + 14}
	drawGlassSurface(r, brwInfo.recheck, brwInfo.recheck.H/2, glassState{})
	renderText(r, config, label, recheckLabel, ColorTextSecondary(), brwInfo.recheck.X+15, y+7)
	brwInfo.wifi = sdl.Rect{}

	status, tone := "", brwToneNeutral
	switch {
	case !IsOnline() && res.State != brwInfoDone:
		status, tone = "Needs internet", brwToneWarn
	case res.State == brwInfoLoading || res.State == brwInfoIdle:
		status = "Checking " + brwInfo.host + "…"
	case res.State == brwInfoFailed:
		status, tone = "Couldn't check", brwToneBad
	default:
		status, tone = res.Status, res.Tone
	}
	if status != "" {
		sw, sh := measureText(body, status)
		pill := sdl.Rect{X: card.X + OverlayPadX, Y: y, W: min(sw+40, brwInfo.recheck.X-card.X-OverlayPadX-12), H: sh + 12}
		ink := brwToneColor(tone)
		fillRoundedRect(r, pill.X, pill.Y, pill.W, pill.H, pill.H/2, WithAlpha(ink, 36))
		fillCircle(r, pill.X+14, pill.Y+pill.H/2, 4, ink)
		renderText(r, config, body, ellipsize(body, status, pill.W-36), ink, pill.X+26, pill.Y+6)
	}
	y += max(brwInfo.recheck.H, int32(body.Height())+12) + 12

	// Body: the rows, scrolled inside the card.
	hintH := int32(small.Height()) + 18
	area := sdl.Rect{X: card.X + OverlayPadX, Y: y, W: card.W - 2*OverlayPadX, H: card.Y + card.H - hintH - y}
	setDrawColor(r, glassHairline())
	_ = r.FillRect(&sdl.Rect{X: area.X, Y: area.Y - 6, W: area.W, H: 1})
	switch {
	case !IsOnline() && res.State != brwInfoDone:
		brwInfo.wifi = brwInfoNotice(r, config, area, "wifi-off", "Needs internet",
			"Connect to Wi-Fi to check this site. The checks start by themselves once you're online.", "Wi-Fi settings")
	case res.State == brwInfoFailed:
		// "Y / R": the keyboard re-checks with R (brwInfoKey).
		brwInfoNotice(r, config, area, "warning", "Couldn't check this site", res.Err+" "+hintSentence("Press %s to try again.", "Y / R", "Retry"), "")
	case res.State == brwInfoLoading || res.State == brwInfoIdle:
		brwInfoSkeleton(r, area)
	default:
		renderWithClip(r, area.X, area.Y, area.W, area.H, func(rr *sdl.Renderer) {
			brwInfoDrawRows(rr, config, area, res.Rows, label, body)
		})
	}

	hint := hintLine(FooterHint{Button: "L1/R1", Label: "Tab"}, FooterHint{Button: "Up/Down", Label: "Scroll"},
		FooterHint{Button: "Y", Label: "Re-check"}, FooterHint{Button: "B", Label: "Close"})
	renderText(r, config, small, ellipsize(small, hint, card.W-2*OverlayPadX), ColorTextTertiary(),
		card.X+OverlayPadX, card.Y+card.H-hintH+6)
}

// brwInfoNotice is a centred state block with an optional button; it returns
// the button's rect.
func brwInfoNotice(r *sdl.Renderer, config *Config, area sdl.Rect, icon, title, text, button string) sdl.Rect {
	titleFont := getFontSized(config, "small", 22)
	bodyFont := getFontSized(config, "small", 16)
	if titleFont == nil || bodyFont == nil {
		return sdl.Rect{}
	}
	lines := brwInfoLines(bodyFont, text, min(area.W-40, int32(560)))
	badge := int32(52)
	lineH := int32(bodyFont.Height())
	blockH := badge + 14 + int32(titleFont.Height()) + 8 + int32(len(lines))*lineH
	if button != "" {
		blockH += 18 + 44
	}
	y := area.Y + max(0, (area.H-blockH)/2)
	cx := area.X + area.W/2
	DrawHomeIcon(r, icon, drawGlassBadge(r, sdl.Rect{X: cx - badge/2, Y: y, W: badge, H: badge}), false)
	y += badge + 14
	tw, _ := measureText(titleFont, title)
	renderText(r, config, titleFont, title, ColorTextPrimary(), cx-tw/2, y)
	y += int32(titleFont.Height()) + 8
	for _, line := range lines {
		lw, _ := measureText(bodyFont, line)
		renderText(r, config, bodyFont, line, ColorTextSecondary(), cx-lw/2, y)
		y += lineH
	}
	if button == "" {
		return sdl.Rect{}
	}
	bw, bh := measureText(bodyFont, button)
	rect := sdl.Rect{X: cx - (bw+48)/2, Y: y + 18, W: bw + 48, H: 44}
	fillRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, rect.H/2, glassAccent())
	renderText(r, config, bodyFont, button, contrastText(glassAccent()), rect.X+24, rect.Y+(rect.H-bh)/2)
	return rect
}

// brwInfoSkeleton is the loading state: soft bars that breathe.
func brwInfoSkeleton(r *sdl.Renderer, area sdl.Rect) {
	phase := float64(sdl.GetTicks64()%1600) / 1600
	widths := [...]int32{60, 82, 48, 70, 56}
	for i, pct := range widths {
		y := area.Y + 8 + int32(i)*34
		if y+20 > area.Y+area.H {
			break
		}
		UILoadingSkeleton(r, sdl.Rect{X: area.X, Y: y, W: area.W * 18 / 100, H: 18}, phase+float64(i)*0.1)
		UILoadingSkeleton(r, sdl.Rect{X: area.X + area.W*22/100, Y: y, W: area.W * pct / 100 * 70 / 100, H: 18}, phase+float64(i)*0.1+0.05)
	}
}

// brwInfoDrawRows lays the report out: labels in a muted column, values
// wrapped beside them, marked headers with a check or a cross, notes as muted
// paragraphs. It also records how far the tab can scroll.
func brwInfoDrawRows(r *sdl.Renderer, config *Config, area sdl.Rect, rows []brwInfoRow, label, body *ttf.Font) {
	tab := brwInfo.tab
	labelW := int32(0)
	for _, row := range rows {
		if row.Label != "" && row.Mark == 0 {
			if w, _ := measureText(label, row.Label); w > labelW {
				labelW = w
			}
		}
	}
	labelW = min(labelW+18, area.W*30/100)
	lineH := int32(body.Height()) + 2
	y := area.Y + 4 - brwInfo.scroll[tab]
	for _, row := range rows {
		switch {
		case row.Mark != 0:
			icon, ink := "check", SuccessInk()
			if row.Mark < 0 {
				icon, ink = "close", DangerInk()
			}
			iconSize := int32(18)
			DrawHomeIcon(r, icon, sdl.Rect{X: area.X, Y: y + (lineH-iconSize)/2, W: iconSize, H: iconSize}, false)
			nameW, _ := measureText(body, row.Label)
			renderText(r, config, body, row.Label, ink, area.X+iconSize+10, y)
			valueX := area.X + iconSize + 10 + nameW + 14
			lines := brwInfoLines(body, row.Value, area.X+area.W-valueX)
			for _, line := range lines {
				renderText(r, config, body, line, ColorTextSecondary(), valueX, y)
				y += lineH
			}
			if len(lines) == 0 {
				y += lineH
			}
			y += 6
		case row.Label != "":
			renderText(r, config, label, row.Label, ColorTextTertiary(), area.X, y+2)
			lines := brwInfoLines(body, row.Value, area.W-labelW)
			for _, line := range lines {
				renderText(r, config, body, line, ColorTextPrimary(), area.X+labelW, y)
				y += lineH
			}
			if len(lines) == 0 {
				y += lineH
			}
			y += 6
		default:
			y += 6
			for _, line := range brwInfoLines(label, row.Value, area.W) {
				renderText(r, config, label, line, ColorTextTertiary(), area.X, y)
				y += int32(label.Height()) + 2
			}
			y += 6
		}
	}
	content := y + brwInfo.scroll[tab] - area.Y
	brwInfo.scrollMax[tab] = max(0, content-area.H+8)
	if brwInfo.scroll[tab] > brwInfo.scrollMax[tab] {
		brwInfo.scroll[tab] = brwInfo.scrollMax[tab]
	}
}

// brwInfoLineCache keeps wrapped values, so a report is measured once per
// width rather than every frame.
var brwInfoLineCache = struct {
	entries map[brwInfoLineKey][]string
}{entries: map[brwInfoLineKey][]string{}}

type brwInfoLineKey struct {
	font  *ttf.Font
	text  string
	width int32
}

func brwInfoLines(font *ttf.Font, text string, width int32) []string {
	if text == "" || width <= 0 {
		return nil
	}
	key := brwInfoLineKey{font: font, text: text, width: width}
	if lines, ok := brwInfoLineCache.entries[key]; ok {
		return lines
	}
	if len(brwInfoLineCache.entries) > 512 {
		brwInfoLineCache.entries = map[brwInfoLineKey][]string{}
	}
	lines := wrapTextLines(font, text, width, 4)
	brwInfoLineCache.entries[key] = lines
	return lines
}
