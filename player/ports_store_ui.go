package main

// ports_store_ui.go — drawing and input for the Ports Store (ports_store.go).
//
// The page is the tool screens' frame: the shared header with a read-out,
// the tab strip as the first row of the focus list, then the tab — shelves of
// cards (Discover), chips and a grid (All, Ready to Run, Bring your game) or
// rows (Installed). A on a card opens the details over the page; L1/R1 (Tab
// and Shift+Tab) switch tabs, or step through the list while the details are
// open; X installs from a card; Y opens the options sheet.

import (
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	pstoreTabH     = int32(46)
	pstoreChipH    = int32(40)
	pstoreCardGap  = int32(18)
	pstoreCardMinW = int32(200)
	pstoreRowGap   = int32(10)
	// pstoreOptsToken names the options sheet, so its buttons can relabel it.
	pstoreOptsToken = "pstore-options"
	// pstoreUninstallConfirm is how long the second press has to arrive.
	pstoreUninstallConfirm = 3 * time.Second
)

// pstoreSortChips are the sort chip's labels.
var pstoreSortChips = [pstoreSortCount]string{"Sort: Most downloaded", "Sort: Newest", "Sort: Recently updated", "Sort: A–Z"}

// pstoreAct is what a details button does.
type pstoreAct int

const (
	pstoreActClose pstoreAct = iota
	pstoreActInstall
	pstoreActCancel
	pstoreActPlay
	pstoreActUpdate
	pstoreActUninstall
	pstoreActWhy
	pstoreActOpenPM
	pstoreActWeb
)

type pstoreButton struct {
	label   string
	act     pstoreAct
	primary bool
}

// pstoreDetail is the open details view.
type pstoreDetail struct {
	open      bool
	entry     mcCatalogEntry
	idx       int    // catalogue index, -1 for an installed port the catalogue lacks
	launch    string // the installed launcher, "" when not installed
	name      string // the launcher's name
	compat    portCompat
	list      []int // the catalogue list it was opened from, for L1/R1
	facts     []string
	kind      string
	kindGood  bool
	verdict   string
	body      string
	focus     int
	scroll    int32
	maxScroll int32
	lineH     int32
	buttons   []pstoreButton
	rects     []sdl.Rect
	card      sdl.Rect
	wrap      tbxWrapCache
	confirmAt time.Time
}

// ── Drawing ─────────────────────────────────────────────────────────────

// pstoreHeader is the page bar.
func pstoreHeader() UIPageHeader {
	status, ink := "", ColorTextTertiary()
	switch {
	case pstore.loading && len(pstore.entries) == 0:
		status = "Reading the catalogue…"
	case pstore.loadErr != "" && len(pstore.entries) == 0:
		status, ink = "Offline", ToastWarnInk()
	case len(pstore.entries) > 0:
		status = pstore.headerStatus
		if pstore.updates > 0 {
			ink = ToastSuccessInk()
		}
	}
	if n, now := pstoreQueueCounts(); n > 0 {
		status, ink = "Installing "+setOr(now, "…"), ToastSuccessInk()
		if n > 1 {
			status += " · " + strconv.Itoa(n-1) + " more queued"
		}
	}
	return tbxHeader(portsStoreEntryName, "PortMaster's catalogue — free games, and engines for games you own",
		portsStoreGlyph, status, ink)
}

// pstoreVisible reports whether a screen rect shows in the body viewport.
func pstoreVisible(r, body sdl.Rect) bool {
	return r.Y+r.H >= body.Y && r.Y <= body.Y+body.H
}

// pstoreRender draws the screen.
func pstoreRender(renderer *sdl.Renderer, config *Config) {
	if pstore.visit.begin() {
		pstoreEnter()
	}
	// The card is read again after the screen was away (PortMaster or a port
	// ran: frames stop while a child has the screen), after an install or an
	// uninstall settled, and every minute.
	now := time.Now()
	gap := now.Sub(pstore.lastFrame)
	pstore.lastFrame = now
	if portsLibraryEnabled() && !pstore.scanning && !gameScreenSuspended() &&
		(gap > 2*time.Second || (!pstore.rescanAt.IsZero() && now.After(pstore.rescanAt)) || now.Sub(pstore.scanAt) > time.Minute) {
		pstore.rescanAt = time.Time{}
		pstoreScanInstalled()
	}
	if pstore.dirty {
		pstoreRebuild()
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, pstoreHeader())
	body := frame.bodyRect()
	nav := &pstore.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	y := pstoreDrawTabs(renderer, config, fonts, frame, top)
	switch {
	case pstore.tab == pstoreTabInstalled:
		pstoreDrawInstalled(renderer, config, fonts, frame, body, top, y)
	case len(pstore.entries) == 0:
		pstoreDrawNoCatalogue(renderer, config, fonts, frame, top, y)
	case pstore.tab == pstoreTabDiscover:
		pstoreDrawDiscover(renderer, config, fonts, frame, body, top, y)
	default:
		pstoreDrawGrid(renderer, config, fonts, frame, body, top, y)
	}
	tbxUnclip(renderer)
	// A focused card that went away (an uninstall, a rebuilt shelf) hands
	// the focus to the first control: scroll there too.
	prev := nav.focus
	nav.end()
	if nav.focus != prev {
		nav.reveal()
	}
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	if pstore.detail.open {
		pstoreDrawDetail(renderer, config, fonts)
	}
	peerEndFrame(renderer, config, pstoreHints())
}

// pstoreDrawTabs draws the tab strip (row 0) and returns the body y below.
func pstoreDrawTabs(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top int32) int32 {
	nav := &pstore.nav
	x := frame.x
	for i := pstoreTab(0); i < pstoreTabCount; i++ {
		label := pstoreTabNames[i]
		if i == pstoreTabInstalled && pstore.installedLabel != "" {
			label = pstore.installedLabel
		}
		w := tbxButtonWidth(fonts.body, label, 120)
		if x+w > frame.x+frame.w {
			w = frame.x + frame.w - x
		}
		if w <= 40 {
			break
		}
		id := pstoreTabIDs[i]
		rect := sdl.Rect{X: x, Y: 0, W: w, H: pstoreTabH}
		screen := rect
		screen.Y += top
		tbxDrawButton(renderer, config, fonts.body, screen, label, i == pstore.tab, nav.focused(id), false)
		nav.add(id, rect, 0, false)
		x += w + 10
	}
	nav.extend(pstoreTabH)
	return pstoreTabH + 16
}

// pstoreDrawNote draws one quiet line and returns the y below it.
func pstoreDrawNote(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top, y int32, text string) int32 {
	if text != "" {
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, text, frame.w), ColorTextTertiary(), frame.x, top+y)
	}
	pstore.nav.extend(y + fonts.smallH)
	return y + fonts.smallH + 12
}

// pstoreDrawNoCatalogue is the catalogue's loading or error state.
func pstoreDrawNoCatalogue(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top, y int32) {
	rect := sdl.Rect{X: frame.x, Y: top + y, W: frame.w, H: 200}
	if pstore.loading || pstore.loadErr == "" {
		tbxDrawEmpty(renderer, config, rect, portsStoreGlyph, "Reading PortMaster's catalogue…",
			"It lists every port PortMaster offers — a few megabytes, saved for when you are offline.")
		pstore.nav.extend(y + 200)
		return
	}
	tbxDrawEmpty(renderer, config, rect, "wifi", "The catalogue could not be read",
		pstore.loadErr+" — connect to Wi-Fi and try again.")
	y += 210
	w := tbxButtonWidth(fonts.body, "Try again", 160)
	r := sdl.Rect{X: frame.x + (frame.w-w)/2, Y: y, W: w, H: pstoreTabH}
	scr := r
	scr.Y += top
	tbxDrawButton(renderer, config, fonts.body, scr, "Try again", true, pstore.nav.focused("retry"), pstore.loading)
	pstore.nav.add("retry", r, 1, pstore.loading)
}

// pstoreCardMetrics is the card size for this frame: columns, width and
// height (the 4:3 screenshot, the title and one line under it).
func pstoreCardMetrics(fonts tbxFonts, frame tbxFrame) (int, int32, int32) {
	cols := UIGridColumns(frame.w, pstoreCardMinW, pstoreCardGap)
	cw := UIGridCellWidth(frame.w, cols, pstoreCardGap)
	ch := 8 + (cw-16)*3/4 + 8 + fonts.titleH + 4 + fonts.smallH + 12
	return cols, cw, ch
}

// pstoreDrawChips draws the grid's chip strip (row 1): search, sort and the
// genres that fit, and returns the body y below it.
func pstoreDrawChips(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top, y int32) int32 {
	nav := &pstore.nav
	x := frame.x
	right := frame.x + frame.w
	chip := func(id, label string, on bool) bool {
		w := tbxButtonWidth(fonts.small, label, 0)
		if x+w > right {
			return false
		}
		rect := sdl.Rect{X: x, Y: y, W: w, H: pstoreChipH}
		scr := rect
		scr.Y += top
		tbxDrawButton(renderer, config, fonts.small, scr, label, on, nav.focused(id), false)
		nav.add(id, rect, 1, false)
		x += w + 8
		return true
	}
	chip("search", pstore.searchLabel, pstore.query != "")
	chip("sort", pstoreSortChips[pstore.sort], false)
	if chip("genre:", "All genres", pstore.genre == "") {
		for k, g := range pstore.genres {
			if k >= len(pstore.genreIDs) || !chip(pstore.genreIDs[k], pstore.genreLabels[k], g == pstore.genre) {
				break
			}
		}
	}
	nav.extend(y + pstoreChipH)
	return y + pstoreChipH + 12
}

// pstoreDrawGrid draws the All, Ready to Run and Bring your game tabs.
func pstoreDrawGrid(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, body sdl.Rect, top, y int32) {
	nav := &pstore.nav
	y = pstoreDrawChips(renderer, config, fonts, frame, top, y)
	y = pstoreDrawNote(renderer, config, fonts, frame, top, y, pstore.gridNote)
	if len(pstore.view) == 0 {
		rect := sdl.Rect{X: frame.x, Y: top + y, W: frame.w, H: 200}
		msg := "Nothing matches — clear the search or pick All genres."
		if pstore.mayRun {
			msg += hintSentence(" Options (%s) → May run here: Off shows ports this handheld may not run.", "Y", "Options")
		}
		tbxDrawEmpty(renderer, config, rect, portsStoreGlyph, "No ports here", msg)
		nav.extend(y + 200)
		return
	}
	cols, cw, ch := pstoreCardMetrics(fonts, frame)
	row := 2
	for k, i := range pstore.view {
		c := k % cols
		if c == 0 && k > 0 {
			y += ch + pstoreCardGap
			row++
		}
		rect := sdl.Rect{X: frame.x + int32(c)*(cw+pstoreCardGap), Y: y, W: cw, H: ch}
		id := pstore.cardIDs[i]
		nav.add(id, rect, row, false)
		scr := rect
		scr.Y += top
		if pstoreVisible(scr, body) {
			pstoreDrawCard(renderer, config, fonts, scr, i, nav.focused(id))
		}
	}
	nav.extend(y + ch)
}

// pstoreDrawDiscover draws the shelves.
func pstoreDrawDiscover(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, body sdl.Rect, top, y int32) {
	nav := &pstore.nav
	if len(pstore.shelves) == 0 {
		rect := sdl.Rect{X: frame.x, Y: top + y, W: frame.w, H: 200}
		tbxDrawEmpty(renderer, config, rect, portsStoreGlyph, "Nothing to show",
			hintSentence("Options (%s) → May run here: Off lists every port, including ones this handheld may not run.", "Y", "Options"))
		nav.extend(y + 200)
		return
	}
	cols, cw, ch := pstoreCardMetrics(fonts, frame)
	row := 1
	for k := range pstore.shelves {
		sh := &pstore.shelves[k]
		if k > 0 {
			y += 8
		}
		if pstoreVisible(sdl.Rect{Y: top + y, H: tbxSectionHeight}, body) {
			tbxDrawSection(renderer, config, sh.title, sh.icon, frame.x, top+y, frame.w)
		}
		y += tbxSectionHeight
		show, all := len(sh.items), false
		// On a one-column screen the shelf keeps its card and drops "See
		// all": a row of only "See all" would hide every port.
		if show > cols && cols > 1 {
			show, all = cols-1, true
		} else if show > cols {
			show = cols
		}
		for j := 0; j < show; j++ {
			i := sh.items[j]
			rect := sdl.Rect{X: frame.x + int32(j)*(cw+pstoreCardGap), Y: y, W: cw, H: ch}
			nav.add(sh.ids[j], rect, row, false)
			scr := rect
			scr.Y += top
			if pstoreVisible(scr, body) {
				pstoreDrawCard(renderer, config, fonts, scr, i, nav.focused(sh.ids[j]))
			}
		}
		if all {
			rect := sdl.Rect{X: frame.x + int32(show)*(cw+pstoreCardGap), Y: y, W: cw, H: ch}
			nav.add(sh.allID, rect, row, false)
			scr := rect
			scr.Y += top
			if pstoreVisible(scr, body) {
				pstoreDrawSeeAll(renderer, config, fonts, scr, sh.more, nav.focused(sh.allID))
			}
		}
		row++
		y += ch + pstoreCardGap
	}
	nav.extend(y)
}

// pstoreCardStatus is a card's pill, its tone and a download fraction (-1
// for none).
func pstoreCardStatus(i int) (string, tbxTone, float64) {
	e := &pstore.entries[i]
	installed := i < len(pstore.launchOf) && pstore.launchOf[i] != ""
	if v, ok := pstoreJobLookup(e.Zip); ok {
		switch v.state {
		case pstoreJobQueued:
			return "Queued", tbxToneAccent, -1
		case pstoreJobWaiting:
			return "Waiting", tbxToneAccent, -1
		case pstoreJobDownloading:
			return "Downloading", tbxToneAccent, pstoreBarFraction(pstoreTitle(*e))
		case pstoreJobInstalling:
			return "Installing", tbxToneAccent, -1
		}
		if !installed {
			switch v.state {
			case pstoreJobPortMaster:
				return "In PortMaster", tbxToneWarn, -1
			case pstoreJobFailed:
				return "Failed", tbxToneBad, -1
			case pstoreJobInstalled:
				return "Installed", tbxToneGood, -1
			}
		}
	}
	if installed {
		if i < len(pstore.updateOf) && pstore.updateOf[i] {
			return "Update", tbxToneAccent, -1
		}
		return "Installed", tbxToneGood, -1
	}
	return "", tbxToneNeutral, -1
}

// pstoreBarFraction is how far the status strip's download for name is, or
// -1 when the strip shows something else.
func pstoreBarFraction(name string) float64 {
	visible, tool, _, _, done, total, _, _, _ := installBarSnapshot()
	if !visible || tool != name || total <= 0 {
		return -1
	}
	return float64(done) / float64(total)
}

// pstoreDrawShot draws a port's screenshot in rect, or its glyph card.
func pstoreDrawShot(renderer *sdl.Renderer, e mcCatalogEntry, rect sdl.Rect, focused bool) {
	if e.Zip != "" {
		if tex := pstoreShotTexture(renderer, e); tex != nil && drawCoverFill(renderer, tex, rect) {
			return
		}
	}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, glassA(26))
	g := rect.H / 3
	if g > 72 {
		g = 72
	}
	DrawHomeIcon(renderer, "gamepad", sdl.Rect{X: rect.X + (rect.W-g)/2, Y: rect.Y + (rect.H-g)/2, W: g, H: g}, focused)
}

// pstoreDrawPillOn draws a status pill over a picture, on a dark backing so
// it reads whatever the screenshot is.
func pstoreDrawPillOn(renderer *sdl.Renderer, config *Config, fonts tbxFonts, right, y int32, label string, tone tbxTone) {
	w, h := tbxPillSize(fonts.tiny, label)
	fillRoundedRect(renderer, right-w, y, w, h, h/2, sdl.Color{A: 170})
	tbxDrawPill(renderer, config, fonts.tiny, right, y+h/2, label, tone)
}

// pstoreDrawCard draws one catalogue card at screen rect r.
func pstoreDrawCard(renderer *sdl.Renderer, config *Config, fonts tbxFonts, r sdl.Rect, i int, focused bool) {
	e := &pstore.entries[i]
	drawGlassTile(renderer, r, r, focused, false)
	shot := sdl.Rect{X: r.X + 8, Y: r.Y + 8, W: r.W - 16, H: (r.W - 16) * 3 / 4}
	pstoreDrawShot(renderer, *e, shot, focused)
	if label, tone, frac := pstoreCardStatus(i); label != "" {
		pstoreDrawPillOn(renderer, config, fonts, shot.X+shot.W-6, shot.Y+6, label, tone)
		if frac >= 0 {
			UIProgressBar(renderer, sdl.Rect{X: shot.X + 8, Y: shot.Y + shot.H - 14, W: shot.W - 16, H: 6}, frac,
				sdl.Color{A: 140}, glassAccent())
		}
	}
	ty := shot.Y + shot.H + 8
	titleColor := ColorTextPrimary()
	if i < len(pstore.mayRunOK) && !pstore.mayRunOK[i] {
		titleColor = ColorTextTertiary()
	}
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, pstoreTitle(*e), r.W-20), titleColor, r.X+10, ty)
	ty += fonts.titleH + 4
	dot := ColorToastWarn
	if e.RTR {
		dot = ColorToastSuccess
	}
	fillCircle(renderer, r.X+14, ty+fonts.smallH/2, 4, dot)
	meta := ""
	if i < len(pstore.meta) {
		meta = pstore.meta[i]
	}
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, meta, r.W-36), ColorTextMuted(), r.X+24, ty)
}

// pstoreDrawSeeAll draws a shelf's "See all" card.
func pstoreDrawSeeAll(renderer *sdl.Renderer, config *Config, fonts tbxFonts, r sdl.Rect, more string, focused bool) {
	drawGlassTile(renderer, r, r, focused, false)
	g := int32(48)
	cy := r.Y + r.H/2 - (g+fonts.titleH+fonts.smallH+16)/2
	DrawHomeIcon(renderer, portsStoreGlyph, sdl.Rect{X: r.X + (r.W-g)/2, Y: cy, W: g, H: g}, focused)
	cy += g + 10
	tw, _ := measureText(fonts.title, "See all")
	renderText(renderer, config, fonts.title, "See all", ColorTextPrimary(), r.X+(r.W-tw)/2, cy)
	cy += fonts.titleH + 6
	mw, _ := measureText(fonts.small, more)
	renderText(renderer, config, fonts.small, more, ColorTextMuted(), r.X+(r.W-mw)/2, cy)
}

// pstoreDrawInstalled draws the Installed tab.
func pstoreDrawInstalled(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, body sdl.Rect, top, y int32) {
	nav := &pstore.nav
	empty := func(icon, title, text string) {
		tbxDrawEmpty(renderer, config, sdl.Rect{X: frame.x, Y: top + y, W: frame.w, H: 200}, icon, title, text)
		nav.extend(y + 200)
	}
	switch {
	case !portsLibraryEnabled():
		empty(portsStoreGlyph, "Ports install on the handheld", "Browse the catalogue here; open the Ports Store on the handheld to install and play.")
		return
	case !pstore.scanned:
		empty(portsStoreGlyph, "Reading the card…", "")
		return
	case len(pstore.installed) == 0:
		empty(portsStoreGlyph, "No ports yet", "Pick one in Discover — Ready to Run ports need nothing else.")
		return
	}
	rowH := tbxRowHeight(fonts)
	if rowH < 84 {
		rowH = 84
	}
	row := 1
	if pstore.updates > 0 {
		rect := sdl.Rect{X: frame.x, Y: y, W: frame.w, H: rowH}
		scr := rect
		scr.Y += top
		tbxDrawRow(renderer, config, fonts, scr, tbxRowSpec{icon: "download", title: "Update all",
			subtitle: pstore.updatesNote, pill: "Update", tone: tbxToneAccent}, nav.focused("updall"))
		nav.add("updall", rect, row, false)
		row++
		y += rowH + pstoreRowGap
	}
	for k := range pstore.installed {
		p := &pstore.installed[k]
		rect := sdl.Rect{X: frame.x, Y: y, W: frame.w, H: rowH}
		nav.add(p.id, rect, row, false)
		scr := rect
		scr.Y += top
		if pstoreVisible(scr, body) {
			pstoreDrawInstalledRow(renderer, config, fonts, scr, p, nav.focused(p.id))
		}
		row++
		y += rowH + pstoreRowGap
	}
	nav.extend(y)
}

// pstoreDrawInstalledRow draws one installed port.
func pstoreDrawInstalledRow(renderer *sdl.Renderer, config *Config, fonts tbxFonts, r sdl.Rect, p *pstoreInstalledPort, focused bool) {
	UIListRow(renderer, r, glassRowRadius, focused, false)
	th := r.H - 16
	thumb := sdl.Rect{X: r.X + 10, Y: r.Y + 8, W: th * 4 / 3, H: th}
	var e mcCatalogEntry
	if p.entry >= 0 && p.entry < len(pstore.entries) {
		e = pstore.entries[p.entry]
	}
	pstoreDrawShot(renderer, e, thumb, focused)
	x := thumb.X + thumb.W + 14
	right := r.X + r.W - 16
	pill, tone := "Play", tbxToneGood
	switch {
	case portCompatBlocked(p.compat.State):
		pill, tone = portCompatBadge(p.compat.State), tbxToneBad
	case p.update:
		pill, tone = "Update", tbxToneAccent
	}
	if e.Zip != "" {
		if v, ok := pstoreJobLookup(e.Zip); ok && pstoreJobActive(v.state) {
			pill, tone = "Updating…", tbxToneAccent
		}
	}
	pr := tbxDrawPill(renderer, config, fonts.tiny, right, r.Y+r.H/2, pill, tone)
	right = pr.X - 12
	tw := right - x
	if tw < 40 {
		tw = 40
	}
	sub := "Installed through PortMaster"
	switch {
	case p.update:
		sub = "A newer build is in PortMaster's catalogue"
	case p.entry < 0 && len(pstore.entries) > 0:
		sub = "Not in PortMaster's catalogue (installed by hand or renamed)"
	case len(p.meta.Porters) > 0:
		sub = "Ported by " + p.meta.Porters[0]
	}
	block := fonts.titleH + 4 + fonts.smallH
	ty := r.Y + (r.H-block)/2
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, pstoreInstalledTitle(*p), tw), ColorTextPrimary(), x, ty)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, sub, tw), ColorTextMuted(), x, ty+fonts.titleH+4)
}

// ── Details ─────────────────────────────────────────────────────────────

// pstoreOpenDetailEntry opens catalogue entry i; list is where it was picked.
func pstoreOpenDetailEntry(i int, list []int) {
	if i < 0 || i >= len(pstore.entries) {
		return
	}
	launch := ""
	if i < len(pstore.launchOf) {
		launch = pstore.launchOf[i]
	}
	compat := portCompat{}
	for k := range pstore.installed {
		if launch != "" && pstore.installed[k].launch == launch {
			compat = pstore.installed[k].compat
		}
	}
	pstoreOpenDetail(pstore.entries[i], i, launch, compat, list)
}

// pstoreOpenDetailInstalled opens installed port k (Installed tab).
func pstoreOpenDetailInstalled(k int) {
	if k < 0 || k >= len(pstore.installed) {
		return
	}
	p := pstore.installed[k]
	if p.entry >= 0 && p.entry < len(pstore.entries) {
		pstoreOpenDetail(pstore.entries[p.entry], p.entry, p.launch, p.compat, nil)
		return
	}
	e := mcCatalogEntry{
		Zip: p.meta.Zip, Title: pstoreInstalledTitle(p), Description: p.meta.Desc, Instructions: p.meta.Inst,
		Porters: p.meta.Porters, Runtime: p.meta.Runtime,
	}
	pstoreOpenDetail(e, -1, p.launch, p.compat, nil)
}

// pstoreOpenDetail fills and opens the details view.
func pstoreOpenDetail(e mcCatalogEntry, idx int, launch string, compat portCompat, list []int) {
	d := pstoreDetail{open: true, entry: e, idx: idx, launch: launch, compat: compat, list: append([]int(nil), list...)}
	if launch != "" {
		d.name = strings.TrimSuffix(filepath.Base(launch), filepath.Ext(launch))
	}
	if len(e.Porters) > 0 {
		d.facts = append(d.facts, "Ported by "+strings.Join(e.Porters, ", "))
	}
	if len(e.Genres) > 0 {
		labels := make([]string, 0, len(e.Genres))
		for _, g := range e.Genres {
			labels = append(labels, pstoreGenreLabel(g))
		}
		d.facts = append(d.facts, strings.Join(labels, " · "))
	}
	if e.Size > 0 {
		d.facts = append(d.facts, "Download "+humanBytes(e.Size))
	}
	if n := pstoreDownloads(idx); n > 0 {
		d.facts = append(d.facts, pstoreCount(n)+" downloads")
	}
	switch {
	case e.DateAdded != "" && e.DateUpdated != "" && e.DateUpdated != e.DateAdded:
		d.facts = append(d.facts, "Added "+e.DateAdded+" · updated "+e.DateUpdated)
	case e.DateAdded != "":
		d.facts = append(d.facts, "Added "+e.DateAdded)
	}
	if launch != "" {
		d.facts = append(d.facts, "Installed: "+filepath.Base(launch))
	}
	switch {
	case idx < 0:
		d.kind = "Installed — not in PortMaster's catalogue"
	case e.RTR:
		d.kind, d.kindGood = "Ready to Run — no game files needed", true
	default:
		d.kind = "Needs your own game files"
	}
	switch {
	case portCompatBlocked(compat.State):
		d.verdict = "This build cannot run here: " + setOr(compat.Note, portCompatBadge(compat.State))
	case idx >= 0 && runtime.GOOS == "linux":
		if why := pstoreWhyNot(e); why != "" {
			d.verdict = "Its own metadata says it cannot run here: " + why
		}
	}
	var b strings.Builder
	b.WriteString(setOr(e.Description, "PortMaster's catalogue has no description for this port."))
	b.WriteString("\n\n## What you need\n")
	switch {
	case e.Instructions != "":
		b.WriteString(e.Instructions)
	case e.RTR:
		b.WriteString("Nothing else: it is Ready to Run.")
	default:
		b.WriteString("Its game files, from a copy you own. Once it is installed, the Why didn't it start? button here shows the port's README, which says where they go.")
	}
	if len(e.Runtime) > 0 {
		b.WriteString("\n\n## Runtimes PortMaster adds\n")
		for _, r := range e.Runtime {
			b.WriteString("• " + r + "\n")
		}
	}
	if runtime.GOOS != "linux" {
		b.WriteString("\n\n## On a PC\nPorts install and run on the handheld: open the Ports Store there to install it.")
	}
	d.body = strings.TrimRight(b.String(), "\n")
	pstore.detail = d
	btns := pstoreDetailButtons()
	pstore.detail.focus = len(btns) - 1
	PlayActivateSound()
}

// pstoreDetailButtons rebuilds the buttons for the port's current state.
func pstoreDetailButtons() []pstoreButton {
	d := &pstore.detail
	// An install or an uninstall that settled while the details are open
	// changes the launcher the rescan found: follow it, so Install turns into
	// Play (and back) without closing the view.
	if d.idx >= 0 && d.idx < len(pstore.launchOf) && d.idx < len(pstore.entries) &&
		pstore.entries[d.idx].Zip == d.entry.Zip && pstore.launchOf[d.idx] != d.launch {
		d.launch = pstore.launchOf[d.idx]
		d.name, d.compat = "", portCompat{}
		if d.launch != "" {
			d.name = strings.TrimSuffix(filepath.Base(d.launch), filepath.Ext(d.launch))
			for k := range pstore.installed {
				if pstore.installed[k].launch == d.launch {
					d.compat = pstore.installed[k].compat
				}
			}
		}
	}
	b := append(d.buttons[:0], pstoreButton{label: "Close", act: pstoreActClose})
	if runtime.GOOS != "linux" {
		if d.entry.Zip != "" {
			b = append(b, pstoreButton{label: "Open on portmaster.games", act: pstoreActWeb, primary: true})
		}
		d.buttons = b
		return b
	}
	var job pstoreJobView
	hasJob := false
	if d.entry.Zip != "" {
		job, hasJob = pstoreJobLookup(d.entry.Zip)
	}
	switch {
	case hasJob && job.state == pstoreJobQueued && job.store:
		b = append(b, pstoreButton{label: "Cancel", act: pstoreActCancel, primary: true})
	case hasJob && pstoreJobActive(job.state):
	case d.launch != "":
		b = append(b, pstoreButton{label: "Uninstall", act: pstoreActUninstall},
			pstoreButton{label: "Why didn't it start?", act: pstoreActWhy})
		if d.idx >= 0 {
			label := "Reinstall"
			if d.idx < len(pstore.updateOf) && pstore.updateOf[d.idx] {
				label = "Update"
			}
			b = append(b, pstoreButton{label: label, act: pstoreActUpdate})
		}
		b = append(b, pstoreButton{label: "Play", act: pstoreActPlay, primary: true})
	case hasJob && job.state == pstoreJobPortMaster:
		b = append(b, pstoreButton{label: "Open PortMaster", act: pstoreActOpenPM, primary: true})
	case d.idx >= 0:
		label := "Install"
		switch {
		case hasJob && job.state == pstoreJobFailed:
			label = "Try again"
		case d.idx < len(pstore.mayRunOK) && !pstore.mayRunOK[d.idx]:
			label = "Install anyway"
		}
		b = append(b, pstoreButton{label: label, act: pstoreActInstall, primary: true})
	}
	d.buttons = b
	if d.focus >= len(b) {
		d.focus = len(b) - 1
	}
	return b
}

// pstoreDetailStatus is the line under the kind while a job runs or failed.
func pstoreDetailStatus() (string, sdl.Color) {
	d := &pstore.detail
	if d.entry.Zip == "" {
		return "", sdl.Color{}
	}
	v, ok := pstoreJobLookup(d.entry.Zip)
	if !ok {
		return "", sdl.Color{}
	}
	switch v.state {
	case pstoreJobQueued:
		return "In the install queue", ToastSuccessInk()
	case pstoreJobWaiting:
		return setOr(v.note, "Waiting"), ToastSuccessInk()
	case pstoreJobDownloading:
		if f := pstoreBarFraction(pstoreTitle(d.entry)); f >= 0 {
			return "Downloading · " + strconv.Itoa(int(f*100)) + "%", ToastSuccessInk()
		}
		return "Downloading…", ToastSuccessInk()
	case pstoreJobInstalling:
		return "PortMaster is installing it…", ToastSuccessInk()
	case pstoreJobPortMaster:
		if d.launch == "" {
			return "Downloaded — PortMaster finishes the install when it opens", ToastWarnInk()
		}
	case pstoreJobFailed:
		if d.launch == "" {
			return "Last try failed: " + v.note, ToastErrorInk()
		}
	}
	return "", sdl.Color{}
}

// pstoreDetailClampScroll keeps the text scroll in range.
func pstoreDetailClampScroll() {
	d := &pstore.detail
	if d.scroll > d.maxScroll {
		d.scroll = d.maxScroll
	}
	if d.scroll < 0 {
		d.scroll = 0
	}
}

// pstoreDrawDetail draws the details over the page (under the bars).
func pstoreDrawDetail(renderer *sdl.Renderer, config *Config, fonts tbxFonts) {
	d := &pstore.detail
	top := headerBarHeight() + 8
	bottom := screenHeight - bottomBarHeight("") - 8
	cardW := screenWidth - 48
	if cardW > 1120 {
		cardW = 1120
	}
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: top, W: cardW, H: bottom - top}
	if card.H < 240 {
		card.H = 240
	}
	d.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)

	const pad = int32(22)
	btnH := int32(46)
	btnY := card.Y + card.H - btnH - 18
	shotW := card.W * 2 / 5
	if shotW > 440 {
		shotW = 440
	}
	shotH := shotW * 3 / 4
	if limit := (btnY - card.Y - pad - 24) * 3 / 5; shotH > limit && limit > 60 {
		shotH = limit
		shotW = shotH * 4 / 3
	}
	shot := sdl.Rect{X: card.X + pad, Y: card.Y + pad, W: shotW, H: shotH}
	pstoreDrawShot(renderer, d.entry, shot, false)

	// The facts under the picture.
	fy := shot.Y + shot.H + 14
	for _, f := range d.facts {
		if fy+fonts.smallH > btnY-10 {
			break
		}
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, f, shotW), ColorTextMuted(), shot.X, fy)
		fy += fonts.smallH + 6
	}

	// The text column.
	tx := shot.X + shot.W + 24
	tw := card.X + card.W - pad - tx
	if tw < 120 {
		tw = 120
	}
	titleFont := FontForRole(config, TypeHeading)
	if titleFont == nil {
		titleFont = fonts.title
	}
	ty := card.Y + pad
	renderText(renderer, config, titleFont, ellipsize(titleFont, pstoreTitle(d.entry), tw), ColorTextPrimary(), tx, ty)
	ty += int32(titleFont.Height()) + 6
	kindInk := ToastWarnInk()
	if d.kindGood {
		kindInk = ToastSuccessInk()
	}
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, d.kind, tw), kindInk, tx, ty)
	ty += fonts.smallH + 4
	if d.verdict != "" {
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, d.verdict, tw), ToastWarnInk(), tx, ty)
		ty += fonts.smallH + 4
	}
	if line, ink := pstoreDetailStatus(); line != "" {
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, line, tw), ink, tx, ty)
		ty += fonts.smallH + 4
	}
	ty += 8
	viewTop, viewBot := ty, btnY-14
	viewH := viewBot - viewTop
	if viewH < fonts.bodyH {
		viewH = fonts.bodyH
	}
	lines := d.wrap.get(fonts.body, d.body, tw-20)
	lineH := fonts.bodyH + 5
	d.lineH = lineH
	bodyH := int32(len(lines)) * lineH
	d.maxScroll = bodyH - viewH
	if d.maxScroll < 0 {
		d.maxScroll = 0
	}
	pstoreDetailClampScroll()
	tbxClip(renderer, sdl.Rect{X: tx, Y: viewTop, W: tw + 4, H: viewH})
	y := viewTop - d.scroll
	for _, line := range lines {
		if y+lineH >= viewTop && y <= viewTop+viewH {
			tbxDrawSheetLine(renderer, config, fonts, line, tx, y, tw)
		}
		y += lineH
	}
	tbxUnclip(renderer)
	tbxDrawScrollbar(renderer, card.X+card.W-10, viewTop, viewH, bodyH, d.scroll)

	// The buttons, right-aligned along the bottom, primary last.
	btns := pstoreDetailButtons()
	if cap(d.rects) < len(btns) {
		d.rects = make([]sdl.Rect, len(btns))
	}
	d.rects = d.rects[:len(btns)]
	x := card.X + card.W - pad
	for i := len(btns) - 1; i >= 0; i-- {
		w := tbxButtonWidth(fonts.body, btns[i].label, 110)
		if x-w < card.X+pad {
			w = x - card.X - pad
		}
		x -= w
		d.rects[i] = sdl.Rect{X: x, Y: btnY, W: w, H: btnH}
		x -= 10
	}
	for i, b := range btns {
		tbxDrawButton(renderer, config, fonts.body, d.rects[i], b.label, b.primary, i == d.focus, false)
	}
}

// pstoreDetailStep opens the next (dir 1) or previous port of the list the
// details were opened from.
func pstoreDetailStep(dir int) {
	d := &pstore.detail
	if !d.open || len(d.list) < 2 || d.idx < 0 {
		return
	}
	pos := -1
	for k, i := range d.list {
		if i == d.idx {
			pos = k
			break
		}
	}
	if pos < 0 {
		return
	}
	next := (pos + dir + len(d.list)) % len(d.list)
	pstoreOpenDetailEntry(d.list[next], d.list)
}

// pstoreDetailAction is one input while the details are open.
func pstoreDetailAction(act tbxAction) bool {
	d := &pstore.detail
	step := d.lineH * 3
	if step <= 0 {
		step = 60
	}
	switch act {
	case tbxActLeft:
		if d.focus > 0 {
			d.focus--
			PlayNavSound()
		}
	case tbxActRight:
		if d.focus < len(d.buttons)-1 {
			d.focus++
			PlayNavSound()
		}
	case tbxActUp:
		d.scroll -= step
	case tbxActDown:
		d.scroll += step
	case tbxActHome:
		d.scroll = 0
	case tbxActEnd:
		d.scroll = d.maxScroll
	case tbxActPageUp:
		pstoreDetailStep(-1)
	case tbxActPageDown:
		pstoreDetailStep(1)
	case tbxActConfirm:
		pstoreDetailPress(d.focus)
	case tbxActBack:
		pstore.detail.open = false
		PlayBackSound()
	}
	pstoreDetailClampScroll()
	return true
}

// pstoreDetailClick is a click while the details are open.
func pstoreDetailClick(mx, my int32) {
	d := &pstore.detail
	for i, r := range d.rects {
		if r.W > 0 && pointInRect(mx, my, r) {
			d.focus = i
			pstoreDetailPress(i)
			return
		}
	}
	if d.card.W > 0 && !pointInRect(mx, my, d.card) {
		d.open = false
		PlayBackSound()
	}
}

// pstoreDetailPress runs details button i.
func pstoreDetailPress(i int) {
	d := &pstore.detail
	if i < 0 || i >= len(d.buttons) {
		return
	}
	switch d.buttons[i].act {
	case pstoreActClose:
		d.open = false
		PlayBackSound()
	case pstoreActInstall, pstoreActUpdate:
		pstoreQueueEntry(d.entry)
	case pstoreActCancel:
		if pstoreCancel(d.entry.Zip) {
			PlayBackSound()
			ShowToast(pstoreTitle(d.entry)+" is out of the queue", ToastKindInfo)
		}
	case pstoreActPlay:
		if portCompatBlocked(d.compat.State) && !portCompatIgnored(appConfig) {
			PlayErrorSound()
			ShowToast(d.name+" cannot run here: "+setOr(d.compat.Note, portCompatBadge(d.compat.State)), ToastKindWarning)
			return
		}
		launchPortScript(d.name, d.launch)
	case pstoreActUninstall:
		if d.confirmAt.IsZero() || time.Since(d.confirmAt) > pstoreUninstallConfirm {
			d.confirmAt = time.Now()
			PlayErrorSound()
			ShowToast("Press Uninstall again to delete "+pstoreTitle(d.entry)+" — its saves go too", ToastKindWarning)
			return
		}
		portUninstallStart(d.name, d.launch)
		d.open = false
		pstore.rescanAt = time.Now().Add(3 * time.Second)
	case pstoreActWhy:
		PlayActivateSound()
		portWhyRun(d.name, d.launch)
	case pstoreActOpenPM:
		if launcher := portMasterLauncher(portMasterSDRoot); launcher != "" {
			d.open = false
			launchPortScript(portMasterAppName, launcher)
		}
	case pstoreActWeb:
		base := strings.TrimSuffix(d.entry.Zip, filepath.Ext(d.entry.Zip))
		if err := openExternalURL("https://portmaster.games/detail.html?name=" + url.QueryEscape(base)); err != nil {
			PlayErrorSound()
			ShowToast("Could not open the browser — "+firstLine(err.Error()), ToastKindError)
			return
		}
		PlayActivateSound()
	}
}

// pstoreQueueEntry queues a port and says so.
func pstoreQueueEntry(e mcCatalogEntry) {
	if err := pstoreEnqueue(e); err != nil {
		PlayErrorSound()
		ShowToast(firstLine(err.Error()), ToastKindWarning)
		return
	}
	PlayActivateSound()
	n, _ := pstoreQueueCounts()
	msg := pstoreTitle(e) + " is downloading — the status strip shows how far"
	if n > 1 {
		msg = pstoreTitle(e) + " is queued (" + strconv.Itoa(n) + " in the queue)"
	}
	ShowToast(msg, ToastKindInfo)
}

// ── Input ───────────────────────────────────────────────────────────────

// pstoreAction handles one action on the screen (the sheet already had its
// chance in tbxDispatch).
func pstoreAction(config *Config, act tbxAction) bool {
	if pstore.detail.open {
		return pstoreDetailAction(act)
	}
	nav := &pstore.nav
	switch act {
	case tbxActConfirm:
		if it, ok := nav.current(); ok && !it.disabled {
			pstoreActivate(config, it.id)
		}
		return true
	case tbxActX:
		pstoreQuick(nav.focus)
		return true
	case tbxActY:
		PlayActivateSound()
		pstoreOpenOptions(config)
		return true
	case tbxActPageUp:
		pstoreSwitchTab(pstore.tab - 1)
		return true
	case tbxActPageDown:
		pstoreSwitchTab(pstore.tab + 1)
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

// pstoreEntryForID resolves a card id ("g:<zip>" or "c:<shelf>:<zip>") to
// its catalogue index and the list it sits in.
func pstoreEntryForID(id string) (int, []int, bool) {
	switch {
	case strings.HasPrefix(id, "g:"):
		if i, ok := pstore.byZip[id[2:]]; ok {
			return i, pstore.view, true
		}
	case strings.HasPrefix(id, "c:"):
		rest := id[2:]
		k, zipName, ok := strings.Cut(rest, ":")
		if !ok {
			return 0, nil, false
		}
		n, err := strconv.Atoi(k)
		if err != nil || n < 0 || n >= len(pstore.shelves) {
			return 0, nil, false
		}
		if i, ok := pstore.byZip[zipName]; ok {
			return i, pstore.shelves[n].items, true
		}
	}
	return 0, nil, false
}

// pstoreInstalledIndex resolves an installed row id.
func pstoreInstalledIndex(id string) int {
	for k := range pstore.installed {
		if pstore.installed[k].id == id {
			return k
		}
	}
	return -1
}

// pstoreActivate is A (or a click) on the focused control.
func pstoreActivate(config *Config, id string) {
	switch {
	case strings.HasPrefix(id, "tab:"):
		if n, err := strconv.Atoi(id[4:]); err == nil {
			pstoreSwitchTab(pstoreTab(n))
		}
	case id == "search":
		pstoreOpenSearch()
	case id == "sort":
		pstore.sort = (pstore.sort + 1) % pstoreSortCount
		pstore.dirty = true
		PlayToggleSound()
	case strings.HasPrefix(id, "genre:"):
		g := id[len("genre:"):]
		if g == pstore.genre {
			g = ""
		}
		pstore.genre = g
		pstore.dirty = true
		PlayToggleSound()
	case id == "retry":
		PlayActivateSound()
		pstoreLoad()
	case id == "updall":
		pstoreUpdateAll()
	case strings.HasPrefix(id, "all:"):
		n, err := strconv.Atoi(id[4:])
		if err != nil || n < 0 || n >= len(pstore.shelves) {
			return
		}
		sh := pstore.shelves[n]
		pstore.tab, pstore.sort, pstore.genre = sh.tab, sh.sort, sh.genre
		pstoreSetQuery("")
		pstore.nav.scroll = 0
		pstore.nav.focus = pstoreTabIDs[sh.tab]
		pstore.dirty = true
		PlayActivateSound()
	case strings.HasPrefix(id, "i:"):
		pstoreOpenDetailInstalled(pstoreInstalledIndex(id))
	default:
		if i, list, ok := pstoreEntryForID(id); ok {
			pstoreOpenDetailEntry(i, list)
		}
	}
}

// pstoreQuick is X: install from a card, update an installed row, or clear
// the search.
func pstoreQuick(id string) {
	switch {
	case id == "search":
		if pstore.query != "" {
			pstoreSetQuery("")
			PlayBackSound()
		}
	case id == "updall":
		pstoreUpdateAll()
	case strings.HasPrefix(id, "i:"):
		k := pstoreInstalledIndex(id)
		if k >= 0 && pstore.installed[k].update && pstore.installed[k].entry >= 0 &&
			pstore.installed[k].entry < len(pstore.entries) {
			pstoreQueueEntry(pstore.entries[pstore.installed[k].entry])
		}
	default:
		i, _, ok := pstoreEntryForID(id)
		if !ok {
			return
		}
		e := pstore.entries[i]
		switch {
		case i < len(pstore.launchOf) && pstore.launchOf[i] != "" && !(i < len(pstore.updateOf) && pstore.updateOf[i]):
			ShowToast(pstoreTitle(e)+hintSentence(" is installed — %s for Play and more", "A", "Details"), ToastKindInfo)
		default:
			pstoreQueueEntry(e)
		}
	}
}

// pstoreUpdateAll queues every installed port the catalogue has a newer
// build of.
func pstoreUpdateAll() {
	n := 0
	for _, p := range pstore.installed {
		if !p.update || p.entry < 0 || p.entry >= len(pstore.entries) {
			continue
		}
		if pstoreEnqueue(pstore.entries[p.entry]) == nil {
			n++
		}
	}
	if n == 0 {
		ShowToast("Nothing to update", ToastKindInfo)
		return
	}
	PlayActivateSound()
	ShowToast(pluralizeCount(n, "update is", "updates are")+" in the queue — the status strip shows each one", ToastKindInfo)
}

// pstoreOpenOptions is Y: sort, genre, May run here and search.
func pstoreOpenOptions(config *Config) {
	body, buttons := pstoreOptionsSpec(config)
	tbxOpenSheet(config, tbxSheetSpec{
		token: pstoreOptsToken, title: "Ports Store options", subtitle: "What the grid shows",
		icon: portsStoreGlyph, body: body, buttons: buttons, focus: len(buttons) - 1,
	})
}

// pstoreOptionsRefresh relabels the open options sheet in place, keeping its
// focus.
func pstoreOptionsRefresh(config *Config) {
	if !tbxSheet.open || tbxSheet.spec.token != pstoreOptsToken {
		return
	}
	body, buttons := pstoreOptionsSpec(config)
	tbxSheet.spec.body, tbxSheet.spec.buttons = body, buttons
}

// pstoreOptionsSpec is the options sheet's text and buttons.
func pstoreOptionsSpec(config *Config) (string, []tbxSheetButton) {
	var b strings.Builder
	b.WriteString("## Sort\n" + pstoreSortNames[pstore.sort] + "\n\n")
	b.WriteString("## Genre\n" + pstoreGenreLabel(pstore.genre) + "\n\n")
	if pstore.mayRun {
		b.WriteString("## May run here: on\nHides ports whose own manifest says they cannot run on this handheld " +
			"(wrong CPU, a newer glibc, two sticks on the Brick…). Ports that say nothing stay listed.\n\n")
	} else {
		b.WriteString("## May run here: off\nEvery port is listed; the ones whose manifest rules this handheld out are dimmed.\n\n")
	}
	b.WriteString("## Installing\n")
	if pstoreInstallerMode() == "portmaster" {
		b.WriteString("Through the PortMaster app (setting portsStoreInstaller: portmaster): JukaHub downloads and checks the port, then opens PortMaster to finish.")
	} else {
		b.WriteString("JukaHub downloads and checks the port, then runs PortMaster's own installer without its menu; " +
			"when that cannot start, PortMaster opens to finish. Set portsStoreInstaller to portmaster to always use the app.")
	}
	mayRun := "May run here: On"
	if !pstore.mayRun {
		mayRun = "May run here: Off"
	}
	buttons := []tbxSheetButton{
		{label: "Sort", keepOpen: true, run: func() {
			pstore.sort = (pstore.sort + 1) % pstoreSortCount
			pstore.dirty = true
			pstoreOptionsRefresh(config)
		}},
		{label: "Genre", keepOpen: true, run: func() {
			pstore.genre = pstoreNextGenre(pstore.genre)
			pstore.dirty = true
			pstoreOptionsRefresh(config)
		}},
		{label: mayRun, keepOpen: true, run: func() {
			pstore.mayRun = !pstore.mayRun
			pstore.dirty = true
			pstoreOptionsRefresh(config)
		}},
		{label: "Search", run: pstoreOpenSearch},
		{label: "Done", primary: true},
	}
	return b.String(), buttons
}

// pstoreNextGenre is the genre after g in the chips' order ("" = all).
func pstoreNextGenre(g string) string {
	if g == "" {
		if len(pstore.genres) > 0 {
			return pstore.genres[0]
		}
		return ""
	}
	for k, x := range pstore.genres {
		if x == g {
			if k+1 < len(pstore.genres) {
				return pstore.genres[k+1]
			}
			return ""
		}
	}
	return ""
}

// pstoreHints is the footer.
func pstoreHints() []FooterHint {
	if d := &pstore.detail; d.open {
		hints := make([]FooterHint, 0, 6)
		if d.maxScroll > 0 {
			hints = append(hints, FooterHint{Button: "Up/Down", Label: "Scroll"})
		}
		if len(d.buttons) > 1 {
			hints = append(hints, FooterHint{Button: "Left/Right", Label: "Choose"})
		}
		if d.focus >= 0 && d.focus < len(d.buttons) {
			hints = append(hints, FooterHint{Button: "A", Label: d.buttons[d.focus].label})
		}
		if len(d.list) > 1 && d.idx >= 0 {
			hints = append(hints, FooterHint{Button: "L1/R1", Label: "Page through ports"})
		}
		return append(hints, FooterHint{Button: "B", Label: "Close"})
	}
	id := pstore.nav.focus
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	a, x := "", ""
	switch {
	case strings.HasPrefix(id, "tab:"):
		a = "Open"
	case id == "search":
		a = "Search"
		if pstore.query != "" {
			x = "Clear"
		}
	case id == "sort":
		a = "Change sort"
	case strings.HasPrefix(id, "genre:"):
		a = "Filter"
	case id == "retry":
		a = "Try again"
	case id == "updall":
		a, x = "Update all", "Update all"
	case strings.HasPrefix(id, "all:"):
		a = "See all"
	case strings.HasPrefix(id, "i:"):
		a = "Details"
		if k := pstoreInstalledIndex(id); k >= 0 && pstore.installed[k].update {
			x = "Update"
		}
	default:
		if i, _, ok := pstoreEntryForID(id); ok {
			a = "Details"
			installed := i < len(pstore.launchOf) && pstore.launchOf[i] != ""
			switch {
			case installed && i < len(pstore.updateOf) && pstore.updateOf[i]:
				x = "Update"
			case !installed && runtime.GOOS == "linux":
				x = "Install"
			}
		}
	}
	if a != "" {
		hints = append(hints, FooterHint{Button: "A", Label: a})
	}
	if x != "" && x != a {
		hints = append(hints, FooterHint{Button: "X", Label: x})
	}
	hints = append(hints, FooterHint{Button: "Y", Label: "Options"}, FooterHint{Button: "L1/R1", Label: "Switch tabs"})
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}
