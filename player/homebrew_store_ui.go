package main

// homebrew_store_ui.go — drawing and input for Get Homebrew
// (homebrew_store.go).
//
// The page is the tool screens' frame, in Home's glass, like the Ports Store
// (ports_store_ui.go): the shared header with a read-out, then the console
// chips (All and the twelve; L1/R1 step through them), the search, sort and
// key chips, a one-line summary and the grid of cards. A on a card opens the
// game's page over the screen; L1/R1 there step to the next game of the
// list. X installs (or updates) straight from a card; Y opens the options.

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	hbChipH    = int32(40)
	hbCardGap  = int32(18)
	hbCardMinW = int32(190)
	// hbOptsToken names the options sheet, so its buttons can relabel it.
	hbOptsToken = "homebrew-options"
)

// hbAct is what a page button does.
type hbAct int

const (
	hbActClose hbAct = iota
	hbActInstall
	hbActUpdate
	hbActCancel
	hbActPlay
	hbActDelete
	hbActBuy
	hbActPage
	hbActKey
)

type hbButton struct {
	label   string
	act     hbAct
	primary bool
}

// hbDetail is the open game page.
type hbDetail struct {
	open      bool
	key       string   // the item's game page
	list      []string // the grid it was opened from, for L1/R1
	facts     []string
	body      string
	fileNote  string
	fileBad   bool
	webOnly   bool // its only build plays in a browser: no Install
	infoGen   uint64
	focus     int
	scroll    int32
	maxScroll int32
	lineH     int32
	buttons   []hbButton
	rects     []sdl.Rect
	card      sdl.Rect
	wrap      tbxWrapCache
}

// hbDetailGen numbers the page's background lookups.
var hbDetailGen uint64

// ── Drawing ─────────────────────────────────────────────────────────────

// hbHeader is the page bar.
func hbHeader() UIPageHeader {
	status, ink := "", ColorTextTertiary()
	switch {
	case hb.loading && len(hb.items) == 0:
		status = "Reading itch.io… " + strconv.Itoa(hb.loadDone) + " of " + strconv.Itoa(hb.loadTotal) + " lists"
	case hb.loadErr != "" && len(hb.items) == 0:
		status, ink = "Offline", ToastWarnInk()
	case len(hb.items) > 0:
		status = hb.headerStatus
		if hb.loading {
			status = "Reading more lists… " + strconv.Itoa(hb.loadDone) + " of " + strconv.Itoa(hb.loadTotal)
		}
		if hb.updates > 0 {
			ink = ToastSuccessInk()
		}
	}
	if n, now := hbQueueCounts(); n > 0 {
		status, ink = "Installing "+setOr(now, "…"), ToastSuccessInk()
		if n > 1 {
			status += " · " + strconv.Itoa(n-1) + " more queued"
		}
	}
	return tbxHeader(homebrewEntryName, "New games for your consoles, from itch.io", homebrewGlyph, status, ink)
}

// hbRender draws the screen.
func hbRender(renderer *sdl.Renderer, config *Config) {
	if hb.visit.begin() {
		hbEnter()
	}
	hb.lastFrame = time.Now()
	if hb.dirty {
		hbRebuild()
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, hbHeader())
	body := frame.bodyRect()
	nav := &hb.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	y, row := hbDrawChips(renderer, config, fonts, frame, top)
	hbDrawGrid(renderer, config, fonts, frame, body, top, y, row)
	tbxUnclip(renderer)
	prev := nav.focus
	nav.end()
	if nav.focus != prev {
		nav.reveal()
	}
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	if hb.detail.open {
		hbDrawDetail(renderer, config, fonts)
	}
	peerEndFrame(renderer, config, hbHints())
}

// hbKeyChipLabel is the key chip's words.
func hbKeyChipLabel() string {
	if hbKeyGet() == "" {
		return "Add itch.io key"
	}
	if name := hbKeyAccount(); name != "" {
		return "itch.io: " + name
	}
	return "itch.io key"
}

// hbDrawChips draws the console chips (wrapping), then search, sort, key and
// Update all; it returns the body y below them and the next focus row.
func hbDrawChips(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top int32) (int32, int) {
	nav := &hb.nav
	x, y, row := frame.x, int32(0), 0
	right := frame.x + frame.w
	chip := func(id, label string, on bool) {
		w := tbxButtonWidth(fonts.small, label, 0)
		if w > frame.w {
			w = frame.w
		}
		if x+w > right && x > frame.x {
			x, y, row = frame.x, y+hbChipH+8, row+1
		}
		rect := sdl.Rect{X: x, Y: y, W: w, H: hbChipH}
		scr := rect
		scr.Y += top
		tbxDrawButton(renderer, config, fonts.small, scr, label, on, nav.focused(id), false)
		nav.add(id, rect, row, false)
		x += w + 8
	}
	for i, id := range hb.conIDs {
		label := "All"
		if i > 0 {
			label = hbConsoles[i-1].label
		}
		chip(id, label, hb.console == i-1)
	}
	x, y, row = frame.x, y+hbChipH+12, row+1
	chip("search", hb.searchLabel, hb.query != "")
	chip("sort", "Sort: "+hbSortNames[hb.sort], false)
	chip("key", hbKeyChipLabel(), false)
	if hb.updates > 0 {
		chip("updall", "Update all · "+strconv.Itoa(hb.updates), true)
	}
	y += hbChipH
	nav.extend(y)
	y += 12
	if hb.gridNote != "" {
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, hb.gridNote, frame.w), ColorTextTertiary(), frame.x, top+y)
	}
	y += fonts.smallH
	nav.extend(y)
	return y + 12, row + 1
}

// hbCardMetrics is the card size: columns, width and height (the cover at
// itch.io's 315×250, the title and one line under it).
func hbCardMetrics(fonts tbxFonts, frame tbxFrame) (int, int32, int32) {
	cols := UIGridColumns(frame.w, hbCardMinW, hbCardGap)
	cw := UIGridCellWidth(frame.w, cols, hbCardGap)
	ch := 8 + (cw-16)*250/315 + 8 + fonts.titleH + 4 + fonts.smallH + 12
	return cols, cw, ch
}

// hbDrawGrid draws the cards, or the loading, offline or empty state.
func hbDrawGrid(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, body sdl.Rect, top, y int32, row int) {
	nav := &hb.nav
	empty := func(icon, title, text string) {
		tbxDrawEmpty(renderer, config, sdl.Rect{X: frame.x, Y: top + y, W: frame.w, H: 200}, icon, title, text)
		nav.extend(y + 200)
	}
	switch {
	case len(hb.items) == 0 && hb.loading:
		empty(homebrewGlyph, "Reading itch.io…", "Game Boy, NES, Mega Drive, PICO-8 and more — the lists are saved for when you are offline.")
		return
	case len(hb.items) == 0 && hb.loadErr == "":
		empty(homebrewGlyph, "No games yet", "itch.io's lists came back empty — Options (Y) → Reload lists tries again.")
		return
	case len(hb.items) == 0:
		empty("wifi", "itch.io could not be reached", hb.loadErr+" — connect to Wi-Fi and try again.")
		y += 210
		w := tbxButtonWidth(fonts.body, "Try again", 160)
		r := sdl.Rect{X: frame.x + (frame.w-w)/2, Y: y, W: w, H: 46}
		scr := r
		scr.Y += top
		tbxDrawButton(renderer, config, fonts.body, scr, "Try again", true, nav.focused("retry"), hb.loading)
		nav.add("retry", r, row, hb.loading)
		nav.extend(y + 46)
		return
	case len(hb.view) == 0:
		msg := "Nothing matches — clear the search or pick All."
		switch {
		case hb.sort == hbSortYours && hbKeyGet() == "":
			msg = "Add your itch.io key to see the games you bought."
		case hb.sort == hbSortYours:
			msg = "Games you install or buy on itch.io show here."
		case hb.loading:
			msg = "Reading itch.io's lists…"
		}
		empty(homebrewGlyph, "No games here", msg)
		return
	}
	cols, cw, ch := hbCardMetrics(fonts, frame)
	for k, it := range hb.view {
		c := k % cols
		if c == 0 && k > 0 {
			y += ch + hbCardGap
			row++
		}
		rect := sdl.Rect{X: frame.x + int32(c)*(cw+hbCardGap), Y: y, W: cw, H: ch}
		nav.add(it.cardID, rect, row, false)
		scr := rect
		scr.Y += top
		if pstoreVisible(scr, body) {
			hbDrawCard(renderer, config, fonts, scr, it, nav.focused(it.cardID))
		}
	}
	nav.extend(y + ch)
}

// hbCardStatus is a card's pill and its tone.
func hbCardStatus(it *hbItem) (string, tbxTone) {
	if st, _, ok := hbJobLookup(it.key); ok {
		switch st {
		case hbJobQueued:
			return "Queued", tbxToneAccent
		case hbJobWorking:
			return "Installing", tbxToneAccent
		case hbJobFailed:
			if _, inst := hbInstalled(it); !inst {
				return "Failed", tbxToneBad
			}
		}
	}
	if r, ok := hbInstalled(it); ok {
		if r.Update {
			return "Update", tbxToneAccent
		}
		return "Installed", tbxToneGood
	}
	switch {
	case it.owned:
		return "Bought", tbxToneGood
	case it.hbFree():
		return "Free", tbxToneNeutral
	}
	return setOr(it.price, "Paid"), tbxToneWarn
}

// hbDrawCover draws a game's picture in rect, or the console's glyph.
func hbDrawCover(renderer *sdl.Renderer, it *hbItem, rect sdl.Rect, focused bool) {
	if it != nil {
		if tex := hbCoverTexture(renderer, it.image); tex != nil && drawCoverFill(renderer, tex, rect) {
			return
		}
	}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, glassA(26))
	g := rect.H / 3
	if g > 72 {
		g = 72
	}
	glyph := homebrewGlyph
	if it != nil {
		for i, c := range hbConsoles {
			if it.consoles&(1<<uint(i)) != 0 {
				glyph = c.glyph
				break
			}
		}
	}
	DrawHomeIcon(renderer, glyph, sdl.Rect{X: rect.X + (rect.W-g)/2, Y: rect.Y + (rect.H-g)/2, W: g, H: g}, focused)
}

// hbDrawCard draws one card at screen rect r.
func hbDrawCard(renderer *sdl.Renderer, config *Config, fonts tbxFonts, r sdl.Rect, it *hbItem, focused bool) {
	drawGlassTile(renderer, r, r, focused, false)
	cover := sdl.Rect{X: r.X + 8, Y: r.Y + 8, W: r.W - 16, H: (r.W - 16) * 250 / 315}
	hbDrawCover(renderer, it, cover, focused)
	if label, tone := hbCardStatus(it); label != "" {
		pstoreDrawPillOn(renderer, config, fonts, cover.X+cover.W-6, cover.Y+6, label, tone)
	}
	ty := cover.Y + cover.H + 8
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, it.title, r.W-20), ColorTextPrimary(), r.X+10, ty)
	ty += fonts.titleH + 4
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, it.author, r.W-20), ColorTextMuted(), r.X+10, ty)
}

// ── The game page ───────────────────────────────────────────────────────

// hbItemByKey is the item for a game page, nil when it is gone.
func hbItemByKey(key string) *hbItem {
	if hb.byKey == nil {
		return nil
	}
	return hb.byKey[key]
}

// hbOpenDetail opens a game's page; list is the grid it was picked from.
func hbOpenDetail(it *hbItem, list []*hbItem) {
	if it == nil {
		return
	}
	d := hbDetail{open: true, key: it.key}
	for _, x := range list {
		d.list = append(d.list, x.key)
	}
	hb.detail = d
	hbDetailFill()
	btns := hbDetailButtons()
	hb.detail.focus = len(btns) - 1
	hbDetailLookup(it)
	PlayActivateSound()
}

// hbDetailFill writes the page's facts and text from the item.
func hbDetailFill() {
	d := &hb.detail
	it := hbItemByKey(d.key)
	if it == nil {
		return
	}
	d.facts = d.facts[:0]
	if it.author != "" {
		d.facts = append(d.facts, "By "+it.author)
	}
	if cons := hbItemConsoles(it); cons != "" {
		d.facts = append(d.facts, cons)
	}
	if t := hbItemDate(it); !t.IsZero() {
		line := "Added " + t.Local().Format("Jan 2, 2006")
		if !it.updated.IsZero() && it.updated.Sub(t) > 24*time.Hour {
			line += " · updated " + it.updated.Local().Format("Jan 2, 2006")
		}
		d.facts = append(d.facts, line)
	}
	if r, ok := hbInstalled(it); ok {
		d.facts = append(d.facts, "Installed: "+hbInstalledName(r))
		if r.Note != "" {
			d.facts = append(d.facts, r.Note)
		}
	}
	var b strings.Builder
	b.WriteString(setOr(it.desc, "itch.io's list has no description for this game — its page has more."))
	b.WriteString("\n\n## On itch.io\n" + strings.TrimPrefix(it.key, "https://"))
	if hbKeyGet() == "" {
		b.WriteString("\n\n## Downloading\nAdd your itch.io key to install it (Add itch.io key on the store page).")
	}
	d.body = b.String()
	d.wrap = tbxWrapCache{}
}

// hbDetailLookup reads the game's data.json and, with a key, its files, on
// a goroutine: the author, the price and whether there is a cartridge.
func hbDetailLookup(it *hbItem) {
	if it == nil || !IsOnline() {
		return
	}
	hbDetailGen++
	gen := hbDetailGen
	hb.detail.infoGen = gen
	link, consoles, keyID, owned := it.key, it.consoles, it.keyID, it.owned
	key := hbKeyGet()
	if r, ok := hbInstalled(it); ok {
		if c := hbConsoleIndex(r.Console); c >= 0 {
			consoles |= 1 << uint(c)
		}
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("homebrew", "reading a game page failed: %v", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		data, derr := hbGameDataFor(ctx, link)
		note, bad, web := "", false, false
		if derr == nil && key != "" && (owned || keyID > 0 || hbPriceFree(data.Price)) {
			ups, err := hbFetchUploads(ctx, key, data.ID, keyID)
			if err != nil {
				note, bad = "Its files could not be read: "+firstLine(err.Error()), true
			} else {
				// The cartridge, an RPG Maker game's zip, or why there is
				// none (homebrew_tic80.go).
				note, bad, web = hbUploadsNote(ups, consoles)
			}
		}
		tbxPost(func() {
			if derr == nil {
				if x := hbItemByKey(link); x != nil {
					hbApplyGameData(x, data)
				}
			}
			if !hb.detail.open || hb.detail.infoGen != gen {
				return
			}
			hb.detail.fileNote, hb.detail.fileBad, hb.detail.webOnly = note, bad, web
			hbDetailFill()
		})
	}()
}

// hbDetailButtons rebuilds the buttons for the game's current state.
func hbDetailButtons() []hbButton {
	d := &hb.detail
	b := append(d.buttons[:0], hbButton{label: "Close", act: hbActClose})
	it := hbItemByKey(d.key)
	if it == nil {
		d.buttons = b
		return b
	}
	st, _, hasJob := hbJobLookup(it.key)
	rec, installed := hbInstalled(it)
	paid := !it.hbFree() && !it.owned
	switch {
	case hasJob && st == hbJobQueued:
		b = append(b, hbButton{label: "Cancel", act: hbActCancel, primary: true})
	case hasJob && st == hbJobWorking:
	case installed:
		b = append(b, hbButton{label: "Delete", act: hbActDelete}, hbButton{label: "Page on itch.io", act: hbActPage})
		if rec.Update {
			b = append(b, hbButton{label: "Update", act: hbActUpdate})
		}
		b = append(b, hbButton{label: "Play", act: hbActPlay, primary: true})
	case paid:
		b = append(b, hbButton{label: "Buy on itch.io", act: hbActBuy, primary: true})
	case hbKeyGet() == "":
		b = append(b, hbButton{label: "Page on itch.io", act: hbActPage},
			hbButton{label: "Add itch.io key", act: hbActKey, primary: true})
	case d.webOnly:
		// A browser-only game (most TIC-80 pages): itch.io plays it.
		b = append(b, hbButton{label: "Page on itch.io", act: hbActPage, primary: true})
	default:
		label := "Install"
		if hasJob && st == hbJobFailed {
			label = "Try again"
		}
		b = append(b, hbButton{label: "Page on itch.io", act: hbActPage}, hbButton{label: label, act: hbActInstall, primary: true})
	}
	d.buttons = b
	if d.focus >= len(b) {
		d.focus = len(b) - 1
	}
	return b
}

// hbDetailStatus is the line under the price while a job runs or failed.
func hbDetailStatus(it *hbItem) (string, sdl.Color) {
	st, note, ok := hbJobLookup(it.key)
	if !ok {
		return "", sdl.Color{}
	}
	switch st {
	case hbJobQueued:
		return "In the install queue", ToastSuccessInk()
	case hbJobWorking:
		if f := pstoreBarFraction(it.title); f >= 0 {
			return "Downloading · " + strconv.Itoa(int(f*100)) + "%", ToastSuccessInk()
		}
		return "Installing…", ToastSuccessInk()
	case hbJobFailed:
		if _, inst := hbInstalled(it); !inst {
			return "Last try failed: " + note, ToastErrorInk()
		}
	}
	return "", sdl.Color{}
}

// hbPriceLine is the page's price line and whether it reads as good news.
func hbPriceLine(it *hbItem) (string, bool) {
	if r, ok := hbInstalled(it); ok {
		if r.Update {
			return "Update available", true
		}
		return "Installed", true
	}
	switch {
	case it.owned:
		return "Bought — yours to install", true
	case it.hbFree():
		return "Free", true
	}
	p := setOr(it.price, "Paid")
	return p + " — buy it on itch.io to install it here", false
}

// hbDetailClampScroll keeps the text scroll in range.
func hbDetailClampScroll() {
	d := &hb.detail
	if d.scroll > d.maxScroll {
		d.scroll = d.maxScroll
	}
	if d.scroll < 0 {
		d.scroll = 0
	}
}

// hbDrawDetail draws the game page over the screen (under the bars).
func hbDrawDetail(renderer *sdl.Renderer, config *Config, fonts tbxFonts) {
	d := &hb.detail
	it := hbItemByKey(d.key)
	if it == nil {
		d.open = false
		return
	}
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
	coverW := card.W * 2 / 5
	if coverW > 440 {
		coverW = 440
	}
	coverH := coverW * 250 / 315
	if limit := (btnY - card.Y - pad - 24) * 3 / 5; coverH > limit && limit > 60 {
		coverH = limit
		coverW = coverH * 315 / 250
	}
	cover := sdl.Rect{X: card.X + pad, Y: card.Y + pad, W: coverW, H: coverH}
	hbDrawCover(renderer, it, cover, false)
	fy := cover.Y + cover.H + 14
	for _, f := range d.facts {
		if fy+fonts.smallH > btnY-10 {
			break
		}
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, f, coverW), ColorTextMuted(), cover.X, fy)
		fy += fonts.smallH + 6
	}

	tx := cover.X + cover.W + 24
	tw := card.X + card.W - pad - tx
	if tw < 120 {
		tw = 120
	}
	titleFont := FontForRole(config, TypeHeading)
	if titleFont == nil {
		titleFont = fonts.title
	}
	ty := card.Y + pad
	renderText(renderer, config, titleFont, ellipsize(titleFont, it.title, tw), ColorTextPrimary(), tx, ty)
	ty += int32(titleFont.Height()) + 6
	price, good := hbPriceLine(it)
	ink := ToastWarnInk()
	if good {
		ink = ToastSuccessInk()
	}
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, price, tw), ink, tx, ty)
	ty += fonts.smallH + 4
	if d.fileNote != "" {
		fink := ColorTextMuted()
		if d.fileBad {
			fink = ToastWarnInk()
		}
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, d.fileNote, tw), fink, tx, ty)
		ty += fonts.smallH + 4
	}
	if line, lineInk := hbDetailStatus(it); line != "" {
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, line, tw), lineInk, tx, ty)
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
	hbDetailClampScroll()
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

	btns := hbDetailButtons()
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

// hbDetailStep opens the next (dir 1) or previous game of the page's list.
func hbDetailStep(dir int) {
	d := &hb.detail
	if !d.open || len(d.list) < 2 {
		return
	}
	pos := -1
	for k, key := range d.list {
		if key == d.key {
			pos = k
			break
		}
	}
	if pos < 0 {
		return
	}
	list := d.list
	next := hbItemByKey(list[(pos+dir+len(list))%len(list)])
	if next == nil {
		return
	}
	hb.detail = hbDetail{open: true, key: next.key, list: list}
	hbDetailFill()
	btns := hbDetailButtons()
	hb.detail.focus = len(btns) - 1
	hbDetailLookup(next)
	PlayNavSound()
}

// hbDetailAction is one input while the page is open.
func hbDetailAction(config *Config, act tbxAction) bool {
	d := &hb.detail
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
		hbDetailStep(-1)
	case tbxActPageDown:
		hbDetailStep(1)
	case tbxActConfirm:
		hbDetailPress(config, d.focus)
	case tbxActBack:
		d.open = false
		PlayBackSound()
	}
	hbDetailClampScroll()
	return true
}

// hbDetailClick is a click while the page is open.
func hbDetailClick(mx, my int32) {
	d := &hb.detail
	for i, r := range d.rects {
		if r.W > 0 && pointInRect(mx, my, r) {
			d.focus = i
			hbDetailPress(appConfig, i)
			return
		}
	}
	if d.card.W > 0 && !pointInRect(mx, my, d.card) {
		d.open = false
		PlayBackSound()
	}
}

// hbDetailPress runs page button i.
func hbDetailPress(config *Config, i int) {
	d := &hb.detail
	if i < 0 || i >= len(d.buttons) {
		return
	}
	it := hbItemByKey(d.key)
	if it == nil {
		d.open = false
		return
	}
	switch d.buttons[i].act {
	case hbActClose:
		d.open = false
		PlayBackSound()
	case hbActInstall:
		hbQueueItem(config, it, false)
	case hbActUpdate:
		hbQueueItem(config, it, true)
	case hbActCancel:
		if hbCancel(it.key) {
			PlayBackSound()
			ShowToast(it.title+" is out of the queue", ToastKindInfo)
		}
	case hbActPlay:
		hbPlay(config, it)
	case hbActDelete:
		hbConfirmDelete(config, it)
	case hbActBuy:
		hbOpenPageSheet(config, it, true)
	case hbActPage:
		hbOpenPageSheet(config, it, false)
	case hbActKey:
		PlayActivateSound()
		hbOpenKeySheet(config, "Installing "+it.title+" needs your itch.io key.")
	}
}

// hbQueueItem queues an install or update and says so.
func hbQueueItem(config *Config, it *hbItem, update bool) {
	if hbKeyGet() == "" {
		PlayActivateSound()
		hbOpenKeySheet(config, "Installing "+it.title+" needs your itch.io key.")
		return
	}
	if !it.hbFree() && !it.owned {
		if _, installed := hbInstalled(it); !installed {
			hbOpenPageSheet(config, it, true)
			return
		}
	}
	if err := hbEnqueue(it, update); err != nil {
		PlayErrorSound()
		ShowToast(firstLine(err.Error()), ToastKindWarning)
		return
	}
	PlayActivateSound()
	n, _ := hbQueueCounts()
	msg := it.title + " is downloading — the status strip shows how far"
	if n > 1 {
		msg = it.title + " is queued (" + strconv.Itoa(n) + " in the queue)"
	}
	ShowToast(msg, ToastKindInfo)
}

// hbPlay starts an installed game with the console's emulator.
func hbPlay(config *Config, it *hbItem) {
	r, ok := hbInstalled(it)
	c := hbConsoleIndex(r.Console)
	if !ok || c < 0 || !regularFile(r.Path) {
		PlayErrorSound()
		ShowToast(it.title+" is not on the card any more", ToastKindWarning)
		return
	}
	game := libraryGame{Path: r.Path, System: hbConsoles[c].name, Title: gameDisplayTitle(r.Path)}
	chooseGameLauncher(game, func(launcher string) {
		if strings.TrimSpace(launcher) == "" {
			ShowToast("No emulator for "+game.System+" yet — add one in Emulators", ToastKindWarning)
			return
		}
		if err := launchLibraryGame(game, launcher); err != nil {
			gameLaunchErrorToast(it.title, err)
			return
		}
		if gameLaunchWasHeld(game.Path) {
			return // a card asks first (gamelaunch_hooks.go)
		}
		recordPlayed(config, r.Path)
		PlaySuccessSound()
		ShowToast("Playing "+it.title, ToastKindSuccess)
	})
}

// hbConfirmDelete asks before a game's file goes.
func hbConfirmDelete(config *Config, it *hbItem) {
	r, ok := hbInstalled(it)
	if !ok {
		return
	}
	PlayErrorSound()
	link, title := it.key, it.title
	tbxOpenSheet(config, tbxSheetSpec{
		title: "Delete " + title + "?", subtitle: hbInstalledName(r), icon: "trash",
		body: "The game file and the cover Get Homebrew saved for it go. Its saves and save states stay, " +
			"so installing it again later picks up where you left off.",
		buttons: []tbxSheetButton{{label: "Keep it", primary: true}, {label: "Delete", run: func() {
			hb.detail.open = false
			hbDelete(link, title)
		}}},
	})
}

// hbOpenPageSheet shows a code to the game's itch.io page: to buy it (buy),
// or just to see it. On a PC the browser opens too.
func hbOpenPageSheet(config *Config, it *hbItem, buy bool) {
	PlayActivateSound()
	link := it.key
	title, body := "On itch.io", "Scan the code to open "+it.title+"'s page on your phone."
	if buy {
		title = "Buy on itch.io"
		body = it.title + " costs " + setOr(it.price, "money") + " on itch.io. Scan the code to buy it there with your account — " +
			"then come back and press I bought it: it installs from your library like a free game.\n\n" +
			"JukaHub never downloads a paid game you have not bought."
	}
	buttons := []tbxSheetButton{{label: "Close"}}
	if runtime.GOOS == "windows" {
		buttons = append(buttons, tbxSheetButton{label: "Open in the browser", run: func() {
			if err := openExternalURL(link); err != nil {
				ShowToast("Could not open the browser — "+firstLine(err.Error()), ToastKindError)
			}
		}})
	}
	if buy {
		buttons = append(buttons, tbxSheetButton{label: "I bought it", primary: true, run: func() {
			if hbKeyGet() == "" {
				hbOpenKeySheet(config, "Add your itch.io key so JukaHub can see what you bought.")
				return
			}
			hb.ownedAt = time.Time{}
			hbLoadOwned()
			ShowToast("Asking itch.io for your games…", ToastKindInfo)
		}})
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title: title, subtitle: it.title, icon: "qr", body: body,
		qr: tbxQRModules(link), qrLabel: strings.TrimPrefix(link, "https://"),
		buttons: buttons, focus: len(buttons) - 1,
	})
}

// ── Input ───────────────────────────────────────────────────────────────

// hbAction handles one action on the screen (the sheet already had its
// chance in tbxDispatch).
func hbAction(config *Config, act tbxAction) bool {
	if hb.detail.open {
		return hbDetailAction(config, act)
	}
	nav := &hb.nav
	switch act {
	case tbxActConfirm:
		if it, ok := nav.current(); ok && !it.disabled {
			hbActivate(config, it.id)
		}
		return true
	case tbxActX:
		hbQuick(config, nav.focus)
		return true
	case tbxActY:
		PlayActivateSound()
		hbOpenOptions(config)
		return true
	case tbxActPageUp:
		hbStepConsole(-1)
		return true
	case tbxActPageDown:
		hbStepConsole(1)
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

// hbItemForCard resolves a card id ("g:<game page>").
func hbItemForCard(id string) *hbItem {
	if !strings.HasPrefix(id, "g:") {
		return nil
	}
	return hbItemByKey(id[2:])
}

// hbActivate is A (or a click) on the focused control.
func hbActivate(config *Config, id string) {
	switch {
	case strings.HasPrefix(id, "con:"):
		if n, err := strconv.Atoi(id[4:]); err == nil {
			hbSetConsole(n)
		}
	case id == "search":
		hbOpenSearch()
	case id == "sort":
		hbSetSort(hb.sort + 1)
		PlayToggleSound()
	case id == "key":
		PlayActivateSound()
		hbOpenKeySheet(config, "")
	case id == "updall":
		hbUpdateAll(config)
	case id == "retry":
		PlayActivateSound()
		hb.loadedAt[hb.sort] = time.Time{}
		hbLoadSort(hb.sort)
	default:
		if it := hbItemForCard(id); it != nil {
			hbOpenDetail(it, hb.view)
		}
	}
}

// hbQuick is X: install or update from a card, or clear the search.
func hbQuick(config *Config, id string) {
	if id == "search" {
		if hb.query != "" {
			homebrewSetQuery("")
			PlayBackSound()
		}
		return
	}
	it := hbItemForCard(id)
	if it == nil {
		return
	}
	if r, ok := hbInstalled(it); ok {
		if r.Update {
			hbQueueItem(config, it, true)
			return
		}
		ShowToast(it.title+hintSentence(" is installed — %s for Play and more", "A", "Details"), ToastKindInfo)
		return
	}
	hbQueueItem(config, it, false)
}

// hbItemForRec is the list's item for an install, or one made from the
// record when no list has the game.
func hbItemForRec(r hbInstallRec) *hbItem {
	if it := hbItemByKey(r.Link); it != nil {
		return it
	}
	it := &hbItem{key: r.Link, title: r.Title, gameID: r.GameID, keyID: r.KeyID, owned: r.KeyID > 0, cardID: "g:" + r.Link}
	if c := hbConsoleIndex(r.Console); c >= 0 {
		it.consoles = 1 << uint(c)
	}
	return it
}

// hbUpdateAll queues every install itch.io has a newer file for.
func hbUpdateAll(config *Config) {
	n := 0
	for _, r := range hb.installs {
		if !r.Update {
			continue
		}
		if hbEnqueue(hbItemForRec(r), true) == nil {
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

// hbOpenOptions is Y: sort, console, search, the key, updates and reloading.
func hbOpenOptions(config *Config) {
	body, buttons := hbOptionsSpec(config)
	tbxOpenSheet(config, tbxSheetSpec{
		token: hbOptsToken, title: "Get Homebrew options", subtitle: "What the list shows",
		icon: homebrewGlyph, body: body, buttons: buttons, focus: len(buttons) - 1,
	})
}

// hbOptionsRefresh relabels an open options or key sheet in place.
func hbOptionsRefresh() {
	if !tbxSheet.open {
		return
	}
	switch tbxSheet.spec.token {
	case hbOptsToken:
		body, buttons := hbOptionsSpec(appConfig)
		tbxSheet.spec.body, tbxSheet.spec.buttons = body, buttons
	case hbKeySheetToken:
		body, buttons := hbKeySheetSpec("")
		tbxSheet.spec.body, tbxSheet.spec.buttons = body, buttons
	}
	if n := len(tbxSheet.spec.buttons); tbxSheet.focus >= n {
		tbxSheet.focus = n - 1
	}
	tbxSheet.focus = tbxSheetSafeFocus(tbxSheet.spec.buttons, tbxSheet.focus)
}

// hbOptionsSpec is the options sheet's text and buttons.
func hbOptionsSpec(config *Config) (string, []tbxSheetButton) {
	var b strings.Builder
	b.WriteString("## Sort\n" + hbSortNames[hb.sort] + " — Popular, Newest and Top rated are itch.io's own orders; " +
		"Free shows only free games; Yours shows what you bought or installed.\n\n")
	con := "All consoles"
	if hb.console >= 0 {
		con = hbConsoles[hb.console].name
	}
	b.WriteString("## Console\n" + con + "\n\n")
	b.WriteString("## Where games go\nEach console's Roms folder, under the game's name, with its cover — the Games library shows it on that console's shelf.\n\n")
	b.WriteString("## Updates\n")
	switch {
	case hbKeyGet() == "":
		b.WriteString("Add your itch.io key to install games and check them for updates.")
	case hb.checking:
		b.WriteString("Checking itch.io…")
	case hb.updates > 0:
		b.WriteString(pluralizeCount(hb.updates, "game has", "games have") + " a newer file on itch.io.")
	default:
		b.WriteString("Checked when the store opens and once a day. An update keeps the file name, so saves still match.")
	}
	buttons := []tbxSheetButton{
		{label: "Sort", keepOpen: true, run: func() {
			hbSetSort(hb.sort + 1)
			hbOptionsRefresh()
		}},
		{label: "Console", keepOpen: true, run: func() {
			n := len(hbConsoles) + 1
			hbSetConsole((hb.console+2)%n - 1)
			hbOptionsRefresh()
		}},
		{label: "Search", run: hbOpenSearch},
		{label: "itch.io key", run: func() { hbOpenKeySheet(config, "") }},
	}
	if hbKeyGet() != "" && len(hb.installs) > 0 {
		buttons = append(buttons, tbxSheetButton{label: "Check for updates", keepOpen: true, run: func() {
			hbCheckUpdatesSoon(0)
			hbOptionsRefresh()
		}})
	}
	buttons = append(buttons, tbxSheetButton{label: "Reload lists", run: func() {
		if hb.loading {
			ShowToast("Still reading the lists — try again when they are in", ToastKindInfo)
			return
		}
		for i := range hb.loadedAt {
			hb.loadedAt[i] = time.Time{}
		}
		hbFeedForce = true
		hbLoadSort(hb.sort)
		ShowToast("Reading itch.io's lists again…", ToastKindInfo)
	}}, tbxSheetButton{label: "Done", primary: true})
	return b.String(), buttons
}

// hbHints is the footer.
func hbHints() []FooterHint {
	if d := &hb.detail; d.open {
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
		if len(d.list) > 1 {
			hints = append(hints, FooterHint{Button: "L1/R1", Label: "Next game"})
		}
		return append(hints, FooterHint{Button: "B", Label: "Close"})
	}
	id := hb.nav.focus
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	a, x := "", ""
	switch {
	case strings.HasPrefix(id, "con:"):
		a = "Show"
	case id == "search":
		a = "Search"
		if hb.query != "" {
			x = "Clear"
		}
	case id == "sort":
		a = "Change sort"
	case id == "key":
		a = "itch.io key"
	case id == "updall":
		a = "Update all"
	case id == "retry":
		a = "Try again"
	default:
		if it := hbItemForCard(id); it != nil {
			a = "Details"
			if r, ok := hbInstalled(it); ok {
				if r.Update {
					x = "Update"
				}
			} else if it.hbFree() || it.owned {
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
	hints = append(hints, FooterHint{Button: "Y", Label: "Options"}, FooterHint{Button: "L1/R1", Label: "Console"})
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}
