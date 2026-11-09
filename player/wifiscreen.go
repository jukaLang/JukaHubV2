package main

// wifiscreen.go — the Wi-Fi page: nearby networks, connect, forget, details,
// diagnostics and the radio switch, as a full-screen page.
//
// It replaced the Wi-Fi picker overlay, a card over whatever screen opened it.
// The page scans as it opens, lists the networks with their signal, a lock on
// the secured ones and a badge on the connected and saved ones, and keeps the
// list fresh while it stays open. A on a network joins it (the password is
// typed on the on-screen keyboard), A on the connected one — or X on any —
// opens its details: address, gateway, DNS, signal in dBm and security, with
// Disconnect and Forget. The radio switch sits in the header where the image
// can switch it; the diagnostics sheet explains a radio that will not answer.
//
// The state machine is wifi_ui.go's (wifiPanel, the worker and its update
// slot); this file is the page around it. It works the same on the TrimUI's
// wpa_supplicant and on Windows' netsh backend.

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// wifiScrCtrl is one control in the header: the radio switch, Scan or
// Diagnostics.
type wifiScrCtrl struct {
	id    string
	label string
	on    bool
	rect  sdl.Rect
}

// wifiScrSheetKey is what the open sheet was built from.
type wifiScrSheetKey struct {
	mode      int
	ssid      string
	active    bool
	saved     bool
	armed     bool
	busy      bool
	detailsAt time.Time
	diagAt    time.Time
	keys      hintStamp // the hints' vocabulary and remap (inputhints.go)
}

// The sheet modes.
const (
	wifiScrSheetNone = iota
	wifiScrSheetDetails
	wifiScrSheetDiag
)

// wifiScr is the page's state; the networks and the connection live in
// wifiPanel.
var wifiScr = struct {
	// zone is 0 while the D-pad walks the list, 1 while it walks the header
	// controls.
	zone   int
	ctrl   int
	ctrls  []wifiScrCtrl
	scroll float64
	target int32
	lastAt uint64
	follow bool
	// fresh asks the next arrival on the page to start a new visit.
	fresh     bool
	view      sdl.Rect
	contentH  int32
	rowY      []int32
	rowRects  []sdl.Rect
	infoRects []sdl.Rect
	hover     int
	// meta is each row's second line, rebuilt when the list changes.
	meta    []string
	metaKey wifiScrMetaKey
	// emptyRect is the empty state's button; emptyAction what it does.
	emptyRect   sdl.Rect
	emptyAction string
	sheet       toolsHubSheet
	sheetMode   int
	sheetSSID   string
	sheetKey    wifiScrSheetKey
}{follow: true, hover: -1}

// wifiScrMetaKey says when the rows' second lines are stale.
type wifiScrMetaKey struct {
	at   time.Time
	n    int
	ip   string
	busy string
}

// wifiScrDetailsSample is what the details sheet adds for the connected
// network, read off the render thread when the sheet opens.
type wifiScrDetailsSample struct {
	ssid    string
	signal  wifiSignalSample
	sigErr  bool
	gateway string
	dns     []string
	at      time.Time
}

var wifiScrDetails struct {
	mu     sync.Mutex
	sample wifiScrDetailsSample
	busy   bool
}

// ── Opening ───────────────────────────────────────────────────────────────

// wifiScrOpen opens the Wi-Fi page (cross-area contract: the status bar, Home,
// Quick Settings, the Start menu, the Tools hub and every other place that
// opened the old picker call it). Already on the page, it scans again.
func wifiScrOpen(config *Config) {
	if config == nil {
		return
	}
	toolsHubEnsureScenes(config)
	idx := sceneIndexExact(config, wifiScrSceneName)
	if idx < 0 {
		return
	}
	// The page replaces the panel it was opened from.
	qs.open = false
	processPanel.open = false
	wifiScr.zone = 0
	wifiScr.sheet.open = false
	wifiScr.sheetMode = wifiScrSheetNone
	wifiPanel.diagOpen = false
	wifiScr.follow = true
	if idx == currentSceneIndex {
		if wifiPanel.available && !wifiWorkPending {
			wifiRefreshAsync(true)
		}
		return
	}
	wifiScr.fresh = true
	changeSceneTo(config, idx)
}

// wifiScrEnter runs when the page is reached. An open from elsewhere starts a
// fresh visit and a scan; coming back through the history only refreshes.
func wifiScrEnter(config *Config) {
	_ = config
	if wifiScr.fresh || (wifiPanel.loadedAt.IsZero() && !wifiWorkPending) {
		wifiScr.fresh = false
		wifiScrBeginVisit()
		return
	}
	if wifiPanel.available && !wifiWorkPending {
		wifiRefreshAsync(time.Since(wifiPanel.loadedAt) > wifiAutoRefresh)
	}
}

// wifiScrLeave is B on the page: in-flight work belongs to this visit, and its
// toast must not land on the screen the user went back to.
func wifiScrLeave(config *Config) {
	wifiScrEndVisit()
	goBackScene(config)
}

// ── Layout ────────────────────────────────────────────────────────────────

// wifiScrFonts are the page's type sizes, resolved once a frame.
type wifiScrFonts struct {
	ssid  *ttf.Font
	meta  *ttf.Font
	badge *ttf.Font
	ctrl  *ttf.Font
}

func wifiScrLoadFonts(config *Config) wifiScrFonts {
	small, _ := getCachedFont(config, "small")
	pick := func(f *ttf.Font) *ttf.Font {
		if f == nil {
			return small
		}
		return f
	}
	return wifiScrFonts{
		ssid:  pick(getFontSized(config, "small", 19)),
		meta:  pick(getFontSized(config, "small", 14)),
		badge: pick(getFontSized(config, "small", 13)),
		ctrl:  pick(getFontSized(config, "small", 16)),
	}
}

// wifiScrBuildCtrls is the header's controls, left to right.
func wifiScrBuildCtrls() []wifiScrCtrl {
	ctrls := wifiScr.ctrls[:0]
	scan := "Scan"
	if wifiPanel.busy && strings.HasPrefix(wifiPanel.busyLabel, "Scanning") {
		scan = "Scanning…"
	}
	// A portal is the one thing on this page that needs doing first: the
	// network is joined but every request lands on a login page.
	if PortalSignInURL() != "" {
		ctrls = append(ctrls, wifiScrCtrl{id: "portal", label: "Sign in"})
	}
	if wifiPanel.available {
		ctrls = append(ctrls, wifiScrCtrl{id: "scan", label: scan})
	}
	ctrls = append(ctrls, wifiScrCtrl{id: "diag", label: "Diagnostics"})
	if wifiPanel.radioOK {
		ctrls = append(ctrls, wifiScrCtrl{id: "radio", label: "Wi-Fi", on: wifiPanel.radioOn})
	}
	wifiScr.ctrls = ctrls
	if wifiScr.ctrl >= len(ctrls) {
		wifiScr.ctrl = len(ctrls) - 1
	}
	if wifiScr.ctrl < 0 {
		wifiScr.ctrl = 0
	}
	return ctrls
}

// wifiScrCtrlWidth measures one header control.
func wifiScrCtrlWidth(font *ttf.Font, c wifiScrCtrl) int32 {
	tw, _ := measureText(font, c.label)
	w := tw + 36
	if c.id == "radio" {
		w += 46
	}
	return w
}

// wifiScrSignalWord names a bar count.
func wifiScrSignalWord(bars int) string {
	switch {
	case bars >= 4:
		return "Excellent signal"
	case bars == 3:
		return "Good signal"
	case bars == 2:
		return "Fair signal"
	}
	return "Weak signal"
}

// wifiScrRowMeta is a network row's second line.
func wifiScrRowMeta(n wifiNetwork, status wifiStatus, busyLabel string) string {
	switch {
	case busyLabel != "" && n.SSID != "" && strings.Contains(busyLabel, n.SSID):
		return strings.TrimSuffix(busyLabel, "…") + "…"
	case n.Active && status.Connected:
		if PortalSignInURL() != "" {
			return "Connected · Sign in required"
		}
		if status.IP != "" {
			return "Connected · " + status.IP
		}
		return "Connected"
	}
	word := wifiScrSignalWord(wifiSignalBars(n.Signal))
	switch {
	case n.Saved:
		return "Saved · " + word
	case n.Secured:
		return "Secured · " + word
	}
	return "Open network · " + word
}

// wifiScrSyncMeta rebuilds the rows' second lines when the list changed.
func wifiScrSyncMeta() {
	busy := ""
	if wifiPanel.busy {
		busy = wifiPanel.busyLabel
	}
	key := wifiScrMetaKey{at: wifiPanel.loadedAt, n: len(wifiPanel.networks), ip: wifiPanel.status.IP, busy: busy}
	if key == wifiScr.metaKey && len(wifiScr.meta) == len(wifiPanel.networks) {
		return
	}
	wifiScr.metaKey = key
	wifiScr.meta = wifiScr.meta[:0]
	for _, n := range wifiPanel.networks {
		wifiScr.meta = append(wifiScr.meta, wifiScrRowMeta(n, wifiPanel.status, busy))
	}
}

// ── Rendering ─────────────────────────────────────────────────────────────

// wifiScrHeaderGlyph is the header's icon, built once.
var wifiScrHeaderGlyph = func(r *sdl.Renderer, box sdl.Rect, _ sdl.Color) {
	DrawHomeIcon(r, "wifi", box, false)
}

// renderWiFiScr draws the page: Home's backdrop, the header with its
// controls, the network list (or the state that explains an empty one), the
// shared bars, and on top the open sheet.
func renderWiFiScr(renderer *sdl.Renderer, config *Config) {
	wifiDrainUpdates()
	if wifiPanel.confirmForget != "" && time.Since(wifiPanel.confirmAt) > wifiForgetConfirmWindow {
		wifiPanel.confirmForget = ""
	}
	if wifiPanel.diagOpen {
		if !wifiWorkPending && !wifiPanel.busy &&
			(wifiPanel.diag.At.IsZero() || time.Since(wifiPanel.diag.At) > wifiAutoRefresh) {
			wifiDiagRefreshAsync()
		}
	} else if wifiPanel.available && !wifiPanel.busy && !wifiWorkPending &&
		(wifiPanel.loadedAt.IsZero() || time.Since(wifiPanel.loadedAt) > wifiAutoRefresh) {
		wifiRefreshAsync(false)
	}

	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	fonts := wifiScrLoadFonts(config)
	ring := toolsHubRingVisible() && !wifiScr.sheet.open

	margin := scaleX(homeSideMargin, screenWidth)
	if margin < 16 {
		margin = 16
	}
	contentX, contentW := margin, screenWidth-2*margin
	headerY := headerBarHeight() + 12

	line, lineColor := wifiStatusLine()
	header := UIPageHeader{
		Title:         "Wi-Fi",
		Subtitle:      line,
		SubtitleColor: StatusInk(lineColor),
		TitleSize:     menuTitleSize,
		Glyph:         wifiScrHeaderGlyph,
	}
	m := uiPageHeaderMetricsFor(config, header)

	// Header controls, right-aligned on the title's line.
	ctrls := wifiScrBuildCtrls()
	ctrlH := int32(40)
	well := m.WellBox
	if well <= 0 {
		well = m.TitleH
	}
	ctrlY := headerY + (well-ctrlH)/2
	right := contentX + contentW
	for i := len(ctrls) - 1; i >= 0; i-- {
		w := wifiScrCtrlWidth(fonts.ctrl, ctrls[i])
		right -= w
		ctrls[i].rect = sdl.Rect{X: right, Y: ctrlY, W: w, H: ctrlH}
		right -= 10
	}
	header.X, header.Y, header.W = contentX, headerY, right-contentX-10
	UIPageHeaderDraw(renderer, config, header)
	for i, c := range ctrls {
		wifiScrDrawCtrl(renderer, fonts, c, ring && wifiScr.zone == 1 && i == wifiScr.ctrl, pointInRect(mouseX, mouseY, c.rect))
	}

	bodyTop := headerY + m.Height + 16
	bodyBot := screenHeight - bottomBarHeight("") - 16
	wifiScr.view = sdl.Rect{X: contentX, Y: bodyTop, W: contentW, H: bodyBot - bodyTop}
	wifiScr.emptyRect = sdl.Rect{}
	wifiScr.emptyAction = ""
	n := len(wifiPanel.networks)
	if cap(wifiScr.rowRects) < n {
		wifiScr.rowRects = make([]sdl.Rect, n)
		wifiScr.infoRects = make([]sdl.Rect, n)
		wifiScr.rowY = make([]int32, n)
	}
	wifiScr.rowRects = wifiScr.rowRects[:n]
	wifiScr.infoRects = wifiScr.infoRects[:n]
	wifiScr.rowY = wifiScr.rowY[:n]

	if n == 0 {
		wifiScrDrawEmpty(renderer, config, fonts, wifiScr.view, ring && wifiScr.zone == 0)
	} else {
		wifiScrDrawList(renderer, config, fonts, ring)
	}

	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
	if wifiScr.sheet.open {
		wifiScrSyncSheet()
		toolsHubDrawSheet(renderer, config, &wifiScr.sheet)
	}
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetClipRect(nil)
}

// wifiScrDrawCtrl paints one header control as a glass pill; the radio switch
// carries a track and knob that show its state.
func wifiScrDrawCtrl(renderer *sdl.Renderer, fonts wifiScrFonts, c wifiScrCtrl, focused, hovered bool) {
	r := c.rect
	if focused {
		drawGlassFocusGlow(renderer, r)
	}
	drawGlassSurface(renderer, r, r.H/2, glassState{Focused: focused, Hovered: hovered && !focused})
	tw, th := measureText(fonts.ctrl, c.label)
	x := r.X + 18
	if c.id == "scan" && wifiPanel.busy {
		renderSpinner(renderer, x+6, r.Y+r.H/2, 7, glassAccent())
		x += 18
	}
	drawText(renderer, fonts.ctrl, c.label, x, r.Y+(r.H-th)/2, ColorTextPrimary(), textAlignLeft)
	if c.id == "radio" {
		trackW, trackH := int32(36), int32(20)
		tx := x + tw + 10
		ty := r.Y + (r.H-trackH)/2
		track := glassA(46)
		knobX := tx + trackH/2
		if c.on {
			track = glassAccent()
			knobX = tx + trackW - trackH/2
		}
		fillRoundedRect(renderer, tx, ty, trackW, trackH, trackH/2, track)
		fillCircle(renderer, knobX, ty+trackH/2, trackH/2-3, sdl.Color{R: 255, G: 255, B: 255, A: 255})
	}
}

// wifiScrEmptyState picks what an empty list says and what A does there.
func wifiScrEmptyState() (glyph, title, body, button, action string) {
	switch {
	case !wifiPanel.available && !wifiPanel.startable:
		return "@wifi-off", "Wi-Fi is not available", wifiPanel.detail, "Diagnostics", "diag"
	case (wifiPanel.startable || wifiPanel.radioOK) && !wifiPanel.radioOn:
		return "@wifi-off", "Wi-Fi is off", "Turn it on to see the networks around you", "Turn on Wi-Fi", "radio"
	case wifiPanel.startable:
		return "@wifi-off", "Wi-Fi is not running", "Start it to see the networks around you", "Start Wi-Fi", "radio"
	case wifiPanel.busy:
		return "", "", "", "", ""
	case wifiPanel.scanErr != "":
		return "@wifi", "Could not scan", wifiPanel.scanErr, "Scan again", "scan"
	case wifiPanel.err != "":
		return "@wifi", "Wi-Fi did not answer", wifiPanel.err, "Try again", "scan"
	}
	return "@wifi", "No networks found", "Move closer to the router, then scan again", "Scan again", "scan"
}

// wifiScrDrawEmpty explains an empty list and offers the one useful action.
func wifiScrDrawEmpty(renderer *sdl.Renderer, config *Config, fonts wifiScrFonts, view sdl.Rect, focused bool) {
	glyph, title, body, button, action := wifiScrEmptyState()
	if title == "" {
		// A scan is running: say so where the list will appear.
		label := wifiPanel.busyLabel
		if label == "" {
			label = "Scanning for networks…"
		}
		tw, th := measureText(fonts.ssid, label)
		cx := view.X + view.W/2
		cy := view.Y + view.H/3
		renderSpinner(renderer, cx-tw/2-18, cy+th/2, 9, glassAccent())
		drawText(renderer, fonts.ssid, label, cx-tw/2, cy, ColorTextSecondary(), textAlignLeft)
		return
	}
	block := sdl.Rect{X: view.X, Y: view.Y, W: view.W, H: view.H * 2 / 3}
	UIEmptyState(renderer, config, block, glyph, title, ellipsize(fonts.meta, body, view.W-40))
	if button == "" {
		return
	}
	bw, _ := measureText(fonts.ctrl, button)
	rect := sdl.Rect{X: view.X + (view.W-bw-48)/2, Y: block.Y + block.H + 4, W: bw + 48, H: 44}
	drawGlassButton(renderer, nil, rect, button, fonts.ctrl, WithAlpha(glassAccent(), 60), glassState{
		Focused: focused,
		Hovered: pointInRect(mouseX, mouseY, rect) && !focused,
	})
	wifiScr.emptyRect = rect
	wifiScr.emptyAction = action
}

// wifiScrDrawList draws the networks: the connected one under its own
// heading, the rest under "Networks", scrolled to keep the cursor in view.
func wifiScrDrawList(renderer *sdl.Renderer, config *Config, fonts wifiScrFonts, ring bool) {
	view := wifiScr.view
	wifiScrSyncMeta()
	rowH := int32(66)
	if screenHeight < 600 {
		rowH = 56
	}
	gap := int32(8)
	headingH := toolsHubFontH(getFontSized(config, "small", 15), 18) + 20

	// Lay the rows out in body coordinates.
	var y int32
	headings := [2]struct {
		title string
		y     int32
	}{}
	nHead := 0
	for i, nw := range wifiPanel.networks {
		if i == 0 && nw.Active {
			headings[nHead].title, headings[nHead].y = "Connected", y
			nHead++
			y += headingH
		}
		if (i == 0 && !nw.Active) || (i == 1 && wifiPanel.networks[0].Active) {
			if nHead > 0 {
				y += 6
			}
			headings[nHead].title, headings[nHead].y = "Networks", y
			nHead++
			y += headingH
		}
		wifiScr.rowY[i] = y
		y += rowH + gap
	}
	wifiScr.contentH = y - gap

	cursor := wifiPanel.cursor
	if wifiScr.follow && cursor >= 0 && cursor < len(wifiScr.rowY) {
		top := wifiScr.rowY[cursor] - 8
		if cursor == 0 || (cursor == 1 && wifiPanel.networks[0].Active) {
			top = wifiScr.rowY[cursor] - headingH
		}
		if cursor == 0 {
			top = 0
		}
		bottom := wifiScr.rowY[cursor] + rowH + 8
		if top < wifiScr.target {
			wifiScr.target = top
		}
		if bottom > wifiScr.target+view.H {
			wifiScr.target = bottom - view.H
		}
		wifiScr.follow = false
	}
	if wifiScr.zone == 1 {
		wifiScr.target = 0
	}
	wifiScr.target = toolsHubClampScroll(wifiScr.target, wifiScr.contentH, view.H)
	wifiScrEase(config)
	scroll := int32(wifiScr.scroll + 0.5)

	wifiScr.hover = -1
	clipTop, clipBot := view.Y-10, view.Y+view.H+6
	renderWithClip(renderer, 0, clipTop, screenWidth, clipBot-clipTop, func(r *sdl.Renderer) {
		for h := 0; h < nHead; h++ {
			hy := view.Y + headings[h].y - scroll
			if hy+headingH < clipTop || hy > clipBot {
				continue
			}
			UISectionLabel(r, config, headings[h].title, view.X, hy, view.W, nil)
		}
		for i, nw := range wifiPanel.networks {
			rect := sdl.Rect{X: view.X, Y: view.Y + wifiScr.rowY[i] - scroll, W: view.W, H: rowH}
			wifiScr.rowRects[i] = sdl.Rect{}
			wifiScr.infoRects[i] = sdl.Rect{}
			if rect.Y+rect.H < clipTop || rect.Y > clipBot {
				continue
			}
			visible := glassIntersect(rect, view)
			wifiScr.rowRects[i] = visible
			hovered := visible.W > 0 && pointInRect(mouseX, mouseY, visible)
			if hovered {
				wifiScr.hover = i
			}
			meta := ""
			if i < len(wifiScr.meta) {
				meta = wifiScr.meta[i]
			}
			info := wifiScrDrawRow(r, fonts, rect, nw, meta, ring && wifiScr.zone == 0 && i == cursor, hovered)
			wifiScr.infoRects[i] = glassIntersect(info, view)
		}
	})

	if maxScroll := wifiScr.contentH - view.H; maxScroll > 0 {
		trackX := view.X + view.W + (screenWidth-view.X-view.W)/2 - 2
		drawScrollbar(renderer, trackX, view.Y, 4, view.H,
			float64(view.H)/float64(wifiScr.contentH), wifiScr.scroll/float64(maxScroll))
	}
}

// wifiScrEase moves the list toward its target offset.
func wifiScrEase(config *Config) {
	now := sdl.GetTicks64()
	target := float64(wifiScr.target)
	if !uiMotionEnabled(config) || wifiScr.lastAt == 0 {
		wifiScr.scroll = target
		wifiScr.lastAt = now
		return
	}
	dt := float64(now - wifiScr.lastAt)
	wifiScr.lastAt = now
	if dt > 100 {
		dt = 100
	}
	k := dt / (dt + 70)
	wifiScr.scroll += (target - wifiScr.scroll) * k
	if target-wifiScr.scroll < 0.5 && wifiScr.scroll-target < 0.5 {
		wifiScr.scroll = target
	}
}

// wifiScrDrawRow paints one network: signal bars, the name with a lock on a
// secured network, the state line under it, the Connected / Saved badge and
// the details button. It returns the details button's rect.
func wifiScrDrawRow(renderer *sdl.Renderer, fonts wifiScrFonts, rect sdl.Rect, n wifiNetwork, meta string, focused, hovered bool) sdl.Rect {
	UIListRow(renderer, rect, glassRowRadius, focused, hovered)
	cy := rect.Y + rect.H/2
	drawWiFiBars(renderer, rect.X+20, cy-2, wifiRowBars(n, statusWiFiSignal))

	// Details button on the right edge: a glass circle with the info glyph.
	btn := rect.H - 26
	if btn < 26 {
		btn = 26
	}
	info := sdl.Rect{X: rect.X + rect.W - 16 - btn, Y: rect.Y + (rect.H-btn)/2, W: btn, H: btn}
	fillRoundedRect(renderer, info.X, info.Y, info.W, info.H, info.H/2, glassA(18))
	strokeRoundedRect(renderer, info.X, info.Y, info.W, info.H, info.H/2, 1, glassA(40))
	DrawHomeIcon(renderer, "info", insetRect(info, info.W/5), false)

	right := info.X - 12
	switch {
	case n.Active:
		right = toolsHubDrawTag(renderer, fonts.badge, "Connected", right, cy-toolsHubFontH(fonts.badge, 16)/2-3,
			glassAccent(), contrastText(glassAccent())).X - 8
	case n.Saved:
		right = toolsHubDrawTag(renderer, fonts.badge, "Saved", right, cy-toolsHubFontH(fonts.badge, 16)/2-3,
			glassA(26), ColorTextSecondary()).X - 8
	}

	textX := rect.X + 66
	ssidH := toolsHubFontH(fonts.ssid, 23)
	metaH := toolsHubFontH(fonts.meta, 17)
	top := rect.Y + (rect.H-ssidH-metaH-2)/2
	name := n.SSID
	if strings.TrimSpace(name) == "" {
		name = "Hidden network"
	}
	lock := int32(0)
	if n.Secured {
		lock = 18
	}
	nameW := right - textX - lock - 6
	if nameW < 40 {
		nameW = 40
	}
	name = ellipsize(fonts.ssid, name, nameW)
	drawText(renderer, fonts.ssid, name, textX, top, ColorTextPrimary(), textAlignLeft)
	if n.Secured {
		nw, _ := measureText(fonts.ssid, name)
		drawHomeGlyphTinted(renderer, "lock", sdl.Rect{X: textX + nw + 6, Y: top + (ssidH-16)/2, W: 16, H: 16}, ColorTextTertiary())
	}
	metaInk := ColorTextMuted()
	if n.Active {
		metaInk = ToastSuccessInk()
	}
	drawText(renderer, fonts.meta, ellipsize(fonts.meta, meta, right-textX), textX, top+ssidH+2, metaInk, textAlignLeft)
	return info
}

// ── Sheets ────────────────────────────────────────────────────────────────

// wifiScrOpenDetails opens the details sheet for the network under the
// cursor; for the connected one it also reads the link's live facts.
func wifiScrOpenDetails() {
	n := wifiSelected()
	if n == nil {
		return
	}
	wifiScr.sheetMode = wifiScrSheetDetails
	wifiScr.sheetSSID = n.SSID
	wifiScr.sheetKey = wifiScrSheetKey{}
	wifiScr.sheet.open = true
	wifiScr.sheet.scroll = 0
	wifiScr.sheet.focus = 0
	wifiScr.sheet.layout = nil
	if n.Active {
		wifiScrDetailsAsync(n.SSID)
	}
	PlayActivateSound()
	wifiScrSyncSheet()
}

// wifiScrOpenDiag opens the diagnostics sheet.
func wifiScrOpenDiag() {
	openWiFiDiagnostics()
	wifiScr.sheetMode = wifiScrSheetDiag
	wifiScr.sheetKey = wifiScrSheetKey{}
	wifiScr.sheet.open = true
	wifiScr.sheet.scroll = 0
	wifiScr.sheet.focus = 0
	wifiScrSyncSheet()
}

// wifiScrCloseSheet closes whichever sheet is open.
func wifiScrCloseSheet() {
	if !wifiScr.sheet.open {
		return
	}
	if wifiScr.sheetMode == wifiScrSheetDiag {
		closeWiFiDiagnostics()
	} else {
		PlayBackSound()
	}
	wifiScr.sheet.open = false
	wifiScr.sheetMode = wifiScrSheetNone
	wifiPanel.confirmForget = ""
}

// wifiScrDetailsAsync reads the connected link's signal, gateway and DNS off
// the render thread.
func wifiScrDetailsAsync(ssid string) {
	wifiScrDetails.mu.Lock()
	if wifiScrDetails.busy {
		wifiScrDetails.mu.Unlock()
		return
	}
	wifiScrDetails.busy = true
	wifiScrDetails.mu.Unlock()
	started := runSceneTask(func(context.Context) {
		sample := wifiScrDetailsSample{ssid: ssid}
		sig, err := wifiSignalPoll()
		sample.signal, sample.sigErr = sig, err != nil
		sample.gateway = defaultGateway()
		sample.dns = wifiScrDNSServers()
		sample.at = time.Now()
		wifiScrDetails.mu.Lock()
		wifiScrDetails.sample = sample
		wifiScrDetails.busy = false
		wifiScrDetails.mu.Unlock()
	})
	if !started {
		wifiScrDetails.mu.Lock()
		wifiScrDetails.busy = false
		wifiScrDetails.mu.Unlock()
	}
}

// wifiScrDetailsNow copies the last details sample.
func wifiScrDetailsNow() wifiScrDetailsSample {
	wifiScrDetails.mu.Lock()
	defer wifiScrDetails.mu.Unlock()
	return wifiScrDetails.sample
}

// wifiScrParseResolv lists the nameservers in a resolv.conf.
func wifiScrParseResolv(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" {
			out = append(out, f[1])
		}
	}
	return out
}

// wifiScrDNSServers is the resolver list on Linux; Windows keeps it per
// adapter and the sheet says so instead.
func wifiScrDNSServers() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	return wifiScrParseResolv(string(data))
}

// wifiScrBand names a channel frequency's band.
func wifiScrBand(mhz int) string {
	switch {
	case mhz <= 0:
		return ""
	case mhz < 3000:
		return "2.4 GHz"
	case mhz < 5925:
		return "5 GHz"
	}
	return "6 GHz"
}

// wifiScrDetailsText is the details sheet's report for one network.
func wifiScrDetailsText(n wifiNetwork, status wifiStatus, d wifiScrDetailsSample) string {
	var b strings.Builder
	active := n.Active && status.Connected
	switch {
	case active:
		b.WriteString("Status:     Connected\n")
	case n.Saved:
		b.WriteString("Status:     Saved — in range\n")
	default:
		b.WriteString("Status:     In range\n")
	}
	dbm := n.Signal
	if active && d.ssid == n.SSID && !d.sigErr && d.signal.RSSI < 0 {
		dbm = d.signal.RSSI
	}
	bars := wifiSignalBars(dbm)
	if active {
		bars = wifiRowBars(n, statusWiFiSignal)
	}
	if dbm < 0 {
		b.WriteString("Signal:     " + strconv.Itoa(dbm) + " dBm · " + strings.ToLower(wifiScrSignalWord(bars)) + "\n")
	} else {
		b.WriteString("Signal:     " + strings.ToLower(wifiScrSignalWord(bars)) + "\n")
	}
	if n.Secured {
		b.WriteString("Security:   Password protected (WPA/WPA2/WPA3)\n")
	} else {
		b.WriteString("Security:   Open — traffic on this network is not encrypted\n")
	}
	if n.Saved {
		b.WriteString("Saved:      Yes — joins again without the password\n")
	} else {
		b.WriteString("Saved:      No\n")
	}
	if active {
		ip := status.IP
		if ip == "" {
			ip = toolsIPAddress()
		}
		if ip == "" {
			ip = "Waiting for an address"
		}
		b.WriteString("IP address: " + ip + "\n")
		gateway := d.gateway
		if gateway == "" && d.ssid == n.SSID && ip != "" {
			if guess := powerGatewayGuess(ip); guess != "" {
				gateway = guess + " (usual router address)"
			}
		}
		switch {
		case d.ssid != n.SSID:
			b.WriteString("Gateway:    Reading…\n")
		case gateway != "":
			b.WriteString("Gateway:    " + gateway + "\n")
		}
		switch {
		case d.ssid != n.SSID:
			b.WriteString("DNS:        Reading…\n")
		case len(d.dns) > 0:
			b.WriteString("DNS:        " + strings.Join(d.dns, ", ") + "\n")
		case runtime.GOOS == "windows":
			b.WriteString("DNS:        Set by Windows for this adapter\n")
		default:
			b.WriteString("DNS:        Not reported\n")
		}
		if d.ssid == n.SSID && !d.sigErr {
			if band := wifiScrBand(d.signal.FreqMHz); band != "" {
				b.WriteString("Frequency:  " + strconv.Itoa(d.signal.FreqMHz) + " MHz (" + band + ")\n")
			}
			if d.signal.LinkKbps > 0 {
				b.WriteString("Link speed: " + strconv.Itoa(d.signal.LinkKbps/1000) + " Mbit/s\n")
			}
		}
	}
	if n.BSSID != "" {
		b.WriteString("BSSID:      " + n.BSSID + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// wifiScrSheetNetwork finds the sheet's network in the current list.
func wifiScrSheetNetwork() (wifiNetwork, bool) {
	for i := range wifiPanel.networks {
		if wifiPanel.networks[i].SSID == wifiScr.sheetSSID {
			return wifiPanel.networks[i], true
		}
	}
	return wifiNetwork{}, false
}

// wifiScrSyncSheet rebuilds the open sheet when what it shows changed.
func wifiScrSyncSheet() {
	s := &wifiScr.sheet
	switch wifiScr.sheetMode {
	case wifiScrSheetDiag:
		key := wifiScrSheetKey{mode: wifiScrSheetDiag, busy: wifiPanel.busy, diagAt: wifiPanel.diag.At, keys: hintStampNow()}
		if key == wifiScr.sheetKey && s.title != "" {
			return
		}
		wifiScr.sheetKey = key
		s.title, s.icon = "Wi-Fi diagnostics", "wifi"
		s.subtitle = wifiDiagSubtitle()
		s.subInk = StatusInk(wifiDiagSubtitleColor())
		entries := wifiPanel.diag.Entries
		s.text = ""
		s.layout = func(font *ttf.Font, width int32) []toolsHubTextLine {
			return wifiScrDiagLines(font, entries, width)
		}
		s.lines = nil
		s.busy = ""
		if wifiPanel.busy {
			s.busy = "Reading Wi-Fi state…"
		}
		s.empty = "Reading Wi-Fi state…"
		if wifiPanel.err != "" && len(entries) == 0 {
			s.empty = wifiPanel.err
		}
		s.pills = append(s.pills[:0],
			toolsHubPill{id: "refresh", label: "Refresh", accent: true},
			toolsHubPill{id: "copy", label: "Copy report"},
			toolsHubPill{id: "close", label: "Close"})
		s.hint = hintLine(FooterHint{"A", "Select"}, FooterHint{"Y", "Copy"}, FooterHint{"Up/Down", "Scroll"}, FooterHint{"B", "Close"})
	case wifiScrSheetDetails:
		n, ok := wifiScrSheetNetwork()
		if !ok {
			n = wifiNetwork{SSID: wifiScr.sheetSSID}
		}
		d := wifiScrDetailsNow()
		key := wifiScrSheetKey{
			mode:      wifiScrSheetDetails,
			ssid:      n.SSID,
			active:    n.Active && wifiPanel.status.Connected,
			saved:     n.Saved,
			armed:     wifiPanel.confirmForget == n.SSID,
			busy:      wifiPanel.busy,
			detailsAt: d.at,
			keys:      hintStampNow(),
		}
		if key == wifiScr.sheetKey && s.title != "" {
			return
		}
		wifiScr.sheetKey = key
		name := n.SSID
		if strings.TrimSpace(name) == "" {
			name = "Hidden network"
		}
		s.title, s.icon = name, "wifi"
		s.subInk = sdl.Color{}
		switch {
		case key.active:
			s.subtitle = "Connected"
			s.subInk = StatusInk(ColorToastSuccess)
		case !ok:
			s.subtitle = "Out of range"
		case n.Saved:
			s.subtitle = "Saved network"
		default:
			s.subtitle = "Not connected"
		}
		s.layout = nil
		s.setText(wifiScrDetailsText(n, wifiPanel.status, d))
		s.busy = ""
		if wifiPanel.busy {
			s.busy = wifiPanel.busyLabel
		}
		s.empty = ""
		forget := "Forget"
		if key.armed {
			forget = "Forget? Press again"
		}
		s.pills = s.pills[:0]
		switch {
		case key.active:
			s.pills = append(s.pills,
				toolsHubPill{id: "reconnect", label: "Reconnect", accent: true},
				toolsHubPill{id: "disconnect", label: "Disconnect"},
				toolsHubPill{id: "forget", label: forget, danger: true})
		case n.Saved && ok:
			s.pills = append(s.pills,
				toolsHubPill{id: "connect", label: "Connect", accent: true},
				toolsHubPill{id: "forget", label: forget, danger: true})
		case n.Saved:
			s.pills = append(s.pills, toolsHubPill{id: "forget", label: forget, danger: true})
		case ok:
			s.pills = append(s.pills, toolsHubPill{id: "connect", label: "Connect", accent: true})
		}
		s.pills = append(s.pills, toolsHubPill{id: "close", label: "Close"})
		s.hint = hintLine(FooterHint{"Left/Right", "Choose"}, FooterHint{"A", "Select"}, FooterHint{"B", "Close"})
	}
	if s.focus >= len(s.pills) {
		s.focus = len(s.pills) - 1
	}
	if s.focus < 0 {
		s.focus = 0
	}
}

// wifiScrDiagLines lays the diagnostics out with each value in its state's
// colour, the way the old sheet drew them.
func wifiScrDiagLines(font *ttf.Font, entries []wifiDiagEntry, width int32) []toolsHubTextLine {
	labelW := int32(0)
	for _, e := range entries {
		if w := toolsHubTextWidth(font, e.Label); w > labelW {
			labelW = w
		}
	}
	if limit := width * 2 / 5; labelW > limit {
		labelW = limit
	}
	valueX := labelW + 18
	rows := wifiDiagRenderRows(font, entries, width-valueX)
	lines := make([]toolsHubTextLine, 0, len(rows))
	for _, row := range rows {
		line := toolsHubTextLine{runs: make([]toolsHubTextRun, 0, 2)}
		if row.label != "" {
			line.runs = append(line.runs, toolsHubTextRun{text: ellipsize(font, row.label, labelW), color: ColorTextTertiary()})
		}
		line.runs = append(line.runs, toolsHubTextRun{text: row.value, x: valueX, color: StatusInk(row.color)})
		lines = append(lines, line)
	}
	return lines
}

// wifiScrSheetAction runs one of the open sheet's actions.
func wifiScrSheetAction(action string) {
	switch action {
	case "close":
		wifiScrCloseSheet()
	case "refresh":
		wifiDiagRefreshAsync()
	case "copy":
		wifiDiagCopyReport()
	case "connect":
		wifiScrCloseSheet()
		wifiActivate()
	case "reconnect":
		wifiScrCloseSheet()
		wifiReconnectSelected()
	case "disconnect":
		wifiScrCloseSheet()
		wifiDisconnectSelected()
	case "forget":
		wifiForgetSelected()
		if wifiPanel.confirmForget == "" {
			// The second press started the forget: the sheet's network is
			// going away, so the list is what is left to look at.
			wifiScr.sheet.open = false
			wifiScr.sheetMode = wifiScrSheetNone
		}
		wifiScr.sheetKey = wifiScrSheetKey{}
	}
}

// ── Actions ───────────────────────────────────────────────────────────────

// wifiScrActivateRow is A on the list: the connected network opens its
// details, any other joins (asking for a password when it needs one), and an
// empty list runs the action its state offers.
func wifiScrActivateRow() {
	n := wifiSelected()
	if n == nil {
		wifiScrEmptyActivate()
		return
	}
	if n.Active && wifiPanel.status.Connected && !wifiPanel.busy {
		wifiScrOpenDetails()
		return
	}
	PlayActivateSound()
	wifiActivate()
}

// wifiScrEmptyActivate runs the empty state's action.
func wifiScrEmptyActivate() {
	_, _, _, _, action := wifiScrEmptyState()
	switch action {
	case "diag":
		wifiScrOpenDiag()
	case "radio", "scan":
		PlayActivateSound()
		wifiActivate()
	}
}

// wifiScrCtrlActivate presses a header control.
func wifiScrCtrlActivate(id string) {
	switch id {
	case "portal":
		PlayActivateSound()
		openPortalSignIn(appConfig)
	case "scan":
		PlayActivateSound()
		wifiRefreshAsync(true)
	case "diag":
		wifiScrOpenDiag()
	case "radio":
		PlayActivateSound()
		if wifiPanel.radioOn {
			wifiDisableRadioAsync()
		} else {
			wifiEnableRadioAsync()
		}
	}
}

// wifiScrBack is B: a forget waiting for its second press is cancelled first,
// then the page is left.
func wifiScrBack(config *Config) {
	if wifiPanel.confirmForget != "" {
		wifiPanel.confirmForget = ""
		PlayBackSound()
		return
	}
	wifiScrLeave(config)
}

// wifiScrMove walks the list, stepping up into the header from the top row.
func wifiScrMove(dy int) bool {
	if wifiScr.zone == 1 {
		if dy > 0 {
			wifiScr.zone = 0
			wifiScr.follow = true
			PlayNavSound()
			return true
		}
		// Up from the header belongs to the top bar.
		return false
	}
	if dy < 0 && (wifiPanel.cursor <= 0 || len(wifiPanel.networks) == 0) {
		if len(wifiScr.ctrls) == 0 {
			return false
		}
		wifiScr.zone = 1
		wifiScr.ctrl = len(wifiScr.ctrls) - 1
		PlayNavSound()
		return true
	}
	if len(wifiPanel.networks) == 0 {
		return true
	}
	before := wifiPanel.cursor
	wifiMove(dy)
	if wifiPanel.cursor != before {
		wifiScr.follow = true
	}
	return true
}

// wifiScrPage moves the cursor by a screenful.
func wifiScrPage(dir int) {
	if wifiScr.zone != 0 || len(wifiPanel.networks) == 0 {
		return
	}
	wifiMove(dir * 5)
	wifiScr.follow = true
}

// ── Input ─────────────────────────────────────────────────────────────────

// wifiScrSheetKeyDown drives the open sheet from the keyboard.
func wifiScrSheetKeyDown(e *sdl.KeyboardEvent) bool {
	s := &wifiScr.sheet
	switch e.Keysym.Sym {
	case sdl.K_UP:
		if s.scrollBy(-2) {
			PlayNavSound()
		}
	case sdl.K_DOWN:
		if s.scrollBy(2) {
			PlayNavSound()
		}
	case sdl.K_PAGEUP:
		s.scrollBy(-s.fits)
	case sdl.K_PAGEDOWN:
		s.scrollBy(s.fits)
	case sdl.K_LEFT:
		s.movePill(-1)
	case sdl.K_RIGHT:
		s.movePill(1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		wifiScrSheetAction(s.focusedPill())
	case sdl.K_c, sdl.K_y:
		if wifiScr.sheetMode == wifiScrSheetDiag {
			wifiDiagCopyReport()
		}
	case sdl.K_r:
		if wifiScr.sheetMode == wifiScrSheetDiag {
			wifiDiagRefreshAsync()
		}
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b, sdl.K_i:
		wifiScrCloseSheet()
	}
	return true
}

// wifiScrKey is the page's keyboard. The password keyboard runs before the
// scene in the input chain, so it keeps its keys.
func wifiScrKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || virtualKeyboardActive {
		return false
	}
	if wifiPanel.confirmForget != "" && time.Since(wifiPanel.confirmAt) > wifiForgetConfirmWindow {
		wifiPanel.confirmForget = ""
	}
	if wifiScr.sheet.open {
		return wifiScrSheetKeyDown(e)
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		return wifiScrMove(-1)
	case sdl.K_DOWN:
		return wifiScrMove(1)
	case sdl.K_LEFT, sdl.K_RIGHT:
		if wifiScr.zone == 1 {
			step := 1
			if e.Keysym.Sym == sdl.K_LEFT {
				step = -1
			}
			if next := wifiScr.ctrl + step; next >= 0 && next < len(wifiScr.ctrls) {
				wifiScr.ctrl = next
				PlayNavSound()
			}
		}
	case sdl.K_PAGEUP:
		wifiScrPage(-1)
	case sdl.K_PAGEDOWN:
		wifiScrPage(1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		if wifiScr.zone == 1 && wifiScr.ctrl < len(wifiScr.ctrls) {
			wifiScrCtrlActivate(wifiScr.ctrls[wifiScr.ctrl].id)
		} else {
			wifiScrActivateRow()
		}
	case sdl.K_x:
		wifiScrOpenDetails()
	case sdl.K_y, sdl.K_r:
		if wifiPanel.available {
			PlayActivateSound()
			wifiRefreshAsync(true)
		}
	case sdl.K_d:
		wifiDisconnectSelected()
	case sdl.K_c:
		wifiReconnectSelected()
	case sdl.K_f:
		wifiForgetSelected()
	case sdl.K_i:
		wifiScrOpenDiag()
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		wifiScrBack(config)
	default:
		return false
	}
	return true
}

// wifiScrPad is the page's controller.
func wifiScrPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || virtualKeyboardActive {
		return false
	}
	if wifiPanel.confirmForget != "" && time.Since(wifiPanel.confirmAt) > wifiForgetConfirmWindow {
		wifiPanel.confirmForget = ""
	}
	if wifiScr.sheet.open {
		s := &wifiScr.sheet
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			if s.scrollBy(-2) {
				PlayNavSound()
			}
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			if s.scrollBy(2) {
				PlayNavSound()
			}
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			s.movePill(-1)
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			s.movePill(1)
		case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
			s.scrollBy(-s.fits)
		case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
			s.scrollBy(s.fits)
		case sdl.CONTROLLER_BUTTON_A:
			wifiScrSheetAction(s.focusedPill())
		case sdl.CONTROLLER_BUTTON_Y:
			if wifiScr.sheetMode == wifiScrSheetDiag {
				wifiDiagCopyReport()
			}
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
			wifiScrCloseSheet()
		}
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return wifiScrMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return wifiScrMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if wifiScr.zone == 1 {
			step := 1
			if e.Button == sdl.CONTROLLER_BUTTON_DPAD_LEFT {
				step = -1
			}
			if next := wifiScr.ctrl + step; next >= 0 && next < len(wifiScr.ctrls) {
				wifiScr.ctrl = next
				PlayNavSound()
			}
		}
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		wifiScrPage(-1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		wifiScrPage(1)
	case sdl.CONTROLLER_BUTTON_A:
		if wifiScr.zone == 1 && wifiScr.ctrl < len(wifiScr.ctrls) {
			wifiScrCtrlActivate(wifiScr.ctrls[wifiScr.ctrl].id)
		} else {
			wifiScrActivateRow()
		}
	case sdl.CONTROLLER_BUTTON_X:
		wifiScrOpenDetails()
	case sdl.CONTROLLER_BUTTON_Y:
		if wifiPanel.available {
			PlayActivateSound()
			wifiRefreshAsync(true)
		}
	case sdl.CONTROLLER_BUTTON_BACK:
		// Select opens the diagnostics: the sheet that explains a radio which
		// will not list anything.
		wifiScrOpenDiag()
	case sdl.CONTROLLER_BUTTON_B:
		wifiScrBack(config)
	default:
		return false
	}
	return true
}

// wifiScrClick is a click or tap on the page or its sheet.
func wifiScrClick(mx, my int32, config *Config) bool {
	_ = config
	if wifiScr.sheet.open {
		s := &wifiScr.sheet
		if id := s.hitPill(mx, my); id != "" {
			for i := range s.pills {
				if s.pills[i].id == id {
					s.focus = i
				}
			}
			wifiScrSheetAction(id)
			return true
		}
		if !pointInRect(mx, my, s.card) {
			wifiScrCloseSheet()
		}
		return true
	}
	for i, c := range wifiScr.ctrls {
		if c.rect.W > 0 && hitTouchTarget(c.rect, mx, my) {
			wifiScr.zone, wifiScr.ctrl = 1, i
			wifiScrCtrlActivate(c.id)
			return true
		}
	}
	if wifiScr.emptyRect.W > 0 && hitTouchTarget(wifiScr.emptyRect, mx, my) {
		wifiScr.zone = 0
		wifiScrEmptyActivate()
		return true
	}
	for i := range wifiScr.rowRects {
		if i >= len(wifiPanel.networks) {
			break
		}
		if r := wifiScr.infoRects[i]; r.W > 0 && hitTouchTarget(r, mx, my) {
			wifiScr.zone = 0
			wifiPanel.cursor = i
			wifiScrOpenDetails()
			return true
		}
		if r := wifiScr.rowRects[i]; r.W > 0 && pointInRect(mx, my, r) {
			wifiScr.zone = 0
			wifiPanel.cursor = i
			wifiPanel.confirmForget = ""
			wifiScrActivateRow()
			return true
		}
	}
	return false
}

// wifiScrWheel scrolls the sheet or the list.
func wifiScrWheel(y int32) bool {
	if y == 0 {
		return false
	}
	if wifiScr.sheet.open {
		wifiScr.sheet.scrollBy(int(-y) * 3)
		return true
	}
	wifiScr.target = toolsHubClampScroll(wifiScr.target-y*60, wifiScr.contentH, wifiScr.view.H)
	return true
}

// wifiScrFooterHints names what the pad does on the page and its sheets.
func wifiScrFooterHints(hints []FooterHint) []FooterHint {
	if wifiScr.sheet.open {
		if wifiScr.sheetMode == wifiScrSheetDiag {
			return []FooterHint{{Button: "Up/Down", Label: "Scroll"}, {Button: "A", Label: "Select"}, {Button: "Y", Label: "Copy"}, {Button: "B", Label: "Close"}}
		}
		return []FooterHint{{Button: "Left/Right", Label: "Choose"}, {Button: "A", Label: "Select"}, {Button: "B", Label: "Close"}}
	}
	if wifiScr.zone == 1 {
		return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Select"}, {Button: "B", Label: "Back"}}
	}
	verb := "Connect"
	if n := wifiSelected(); n == nil {
		verb = "Select"
	} else if n.Active && wifiPanel.status.Connected {
		verb = "Details"
	}
	if wifiPanel.confirmForget != "" {
		return []FooterHint{{Button: "F", Label: "Forget " + wifiPanel.confirmForget}, {Button: "B", Label: "Cancel"}}
	}
	_ = hints
	return []FooterHint{
		{Button: "D-Pad", Label: "Navigate"},
		{Button: "A", Label: verb},
		{Button: "B", Label: "Back"},
		{Button: "X", Label: "Details"},
		{Button: "Y", Label: "Scan"},
		// "/ I": the keyboard opens diagnostics with I (inputhints.go shows
		// the half the user can press).
		{Button: "Select / I", Label: "Diagnostics"},
	}
}

// wifiScrScene registers the page with the scene registry.
type wifiScrScene struct{ sceneBase }

func (wifiScrScene) render(r *sdl.Renderer, c *Config) bool {
	renderWiFiScr(r, c)
	return true
}

func (wifiScrScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return wifiScrKey(e, c)
}

func (wifiScrScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return wifiScrPad(e, c)
}

func init() { registerScene(wifiScrScene{sceneBase{name: wifiScrSceneName}}) }
