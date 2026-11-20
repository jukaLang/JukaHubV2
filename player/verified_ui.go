package main

// verified_ui.go — "Verified on this device" on a game's page, and the check
// card the Tested on this device page (verified_page.go) opens too.
// verified.go keeps the results; this file only shows them and takes the
// player's word.
//
// The page gets three things:
//
//   - a chip in the details column, right after Game Ready's: "Verified on
//     Smart Pro", "3 of 5 checks on Smart Pro" or "Known issue". A game nobody
//     tested shows nothing there — every new card would otherwise carry a
//     grey chip — except while the More sheet is open, where "Not tested on
//     Smart Pro" says what the Tested on this device row is about;
//   - the More sheet's "Tested on this device" row, its second line the verdict;
//   - the check card: the five checks in vfChecks order, each with its mark,
//     where the result came from (seen by JukaHub, marked by you, a shared
//     list), its note, the JukaHub release and the emulator it was seen
//     with; the known problem; and what the player can do about it. A on a
//     check steps it Works → Doesn't work → Not checked; "Describe a problem"
//     opens the keyboard (submit only) for the player's own words; "Clear the
//     problem" hides it; "Forget this game's results" asks first, then drops
//     this device's own record (a shared list's results show again).
//
// The chip is drawn every frame, so it answers from a memo that is rebuilt
// only when the game or vfGeneration changes; the card rebuilds its rows the
// same way. Main thread only.

import (
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// vfuiIssueMaxRunes bounds the player's description of a problem: one card
// line or two (verified.go clips at vfIssueMaxRunes anyway).
const vfuiIssueMaxRunes = 200

func init() {
	// Right after Game Ready's chip (10) and its Ready check row (20).
	gamePageRegisterStatus(gamePageStatus{ID: "verified", Order: 11, Chip: vfuiChip})
	gamePageRegisterExtra(gamePageExtra{ID: "verified", Order: 21, Icon: "devices", Row: vfuiMoreRow, Open: vfuiOpenFromPage})
	gamePageRegisterOverlay(gamePageOverlay{
		ID:     "verified",
		IsOpen: func() bool { return vfCard.open && vfCard.host == vfCardHostGamePage },
		Key:    vfCardKey,
		Pad:    vfCardPad,
		Mouse:  vfCardMouse,
		Render: vfCardRender,
		Close:  func() { vfCardClose(false) },
	})
	// The file is read on a goroutine the first time anything asks; a page
	// opening is a good moment, so the chip is there by the next frames.
	gamePageRegisterOpened(func(libraryGame) { vfEnsureLoaded() })
}

// vfuiApplies reports whether a game has a Tested on this device card: anything in the
// library with a file, except JukaHub's own built-in games.
func vfuiApplies(game libraryGame) bool {
	return strings.TrimSpace(game.Path) != "" && !isBuiltInLibraryGame(game)
}

// ── The chip ─────────────────────────────────────────────────────────────

// vfuiChipMemo is the last game's verdict, so a frame does no lookup.
var vfuiChipMemo struct {
	valid  bool
	path   string
	gen    int64
	loaded bool
	level  int
	text   string
	tone   int
}

// vfuiChip is the page's chip, from the memo.
func vfuiChip(game libraryGame) (string, int, bool) {
	if !vfuiApplies(game) {
		return "", 0, false
	}
	m := &vfuiChipMemo
	gen := vfGeneration.Load()
	if !m.valid || m.path != game.Path || m.gen != gen {
		kind, id, _ := vfGameRef(game)
		rec, ok := vfLookup(kind, id)
		if !ok {
			rec = vfRecord{Kind: kind, ID: id}
		}
		v := vfVerdictFor(rec)
		m.valid, m.path, m.gen = true, game.Path, gen
		m.loaded = vfLoaded()
		m.level, m.text, m.tone = v.Level, v.Text, v.Tone
	}
	if !m.loaded {
		return "", 0, false
	}
	if m.level == vfLevelUntested && !gamePageMoreUI.open {
		return "", 0, false
	}
	return m.text, m.tone, true
}

// ── The More row ─────────────────────────────────────────────────────────

// vfuiMoreRow is "Tested on this device" with the verdict under it. Called when the
// sheet opens, not per frame.
func vfuiMoreRow(game libraryGame) (string, string, bool) {
	if !vfuiApplies(game) {
		return "", "", false
	}
	if !vfLoaded() {
		vfEnsureLoaded()
		return "Tested on this device", "Reading this device's results…", true
	}
	kind, id, _ := vfGameRef(game)
	rec, ok := vfLookup(kind, id)
	if !ok {
		rec = vfRecord{Kind: kind, ID: id}
	}
	return "Tested on this device", vfuiVerdictLine(vfVerdictFor(rec)), true
}

// vfuiVerdictLine is a verdict in one line: what it is, then why.
func vfuiVerdictLine(v vfVerdict) string {
	if v.Level == vfLevelIssue {
		return "Known issue: " + v.Detail
	}
	return v.Text + " · " + v.Detail
}

// vfuiOpenFromPage is the row's A (the sheet played the sound).
func vfuiOpenFromPage(game libraryGame) {
	kind, id, title := vfGameRef(game)
	vfCardOpen(kind, id, title, vfCardHostGamePage)
}

// ── The check card ───────────────────────────────────────────────────────

// Where the card is open: over a game's page or over the Tested on this
// device page. Each host routes its input and draws it.
const (
	vfCardHostNone = iota
	vfCardHostGamePage
	vfCardHostPage
)

// The card's action rows.
const (
	vfCardRowIssue  = "issue"
	vfCardRowClear  = "clear"
	vfCardRowForget = "forget"
	vfCardRowCheck  = "check:"
)

// vfCardRow is one focusable row of the card, worded when the card is built.
type vfCardRow struct {
	id     string
	check  string
	icon   string
	ink    sdl.Color
	tinted bool
	label  string
	state  string
	detail string
	// act is what A does, for the hints.
	act string
}

// vfCard is the open card. Main thread only.
var vfCard struct {
	open bool
	host int
	kind string
	id   string
	// name is the title changes are recorded with ("" keeps the record's);
	// title is what the card shows.
	name  string
	title string
	// gen is the vfGeneration the rows were built at.
	gen      int64
	loaded   bool
	rec      vfRecord
	verdict  vfVerdict
	hasLocal bool
	header   UIPageHeader
	summary  string
	issue    string
	rows     []vfCardRow
	// confirm is the Forget question; confirmFocus 0 is Keep, 1 Forget.
	confirm      bool
	confirmFocus int
	nav          tbxNav
	card         sdl.Rect
	pills        [2]sdl.Rect
	wrapSummary  tbxWrapCache
	wrapIssue    tbxWrapCache
	wrapAsk      tbxWrapCache
}

// vfCardOpen opens the card for kind/id on host.
func vfCardOpen(kind, id, title string, host int) {
	vfEnsureLoaded()
	vfCard.open = true
	vfCard.host = host
	vfCard.kind, vfCard.id = kind, id
	vfCard.name = strings.TrimSpace(title)
	vfCard.title = setOr(vfCard.name, id)
	vfCard.confirm, vfCard.confirmFocus = false, 0
	vfCard.nav.focus = vfCardRowCheck + vfCheckLaunch
	vfCard.nav.scroll = 0
	vfCard.card = sdl.Rect{}
	vfCardRebuild()
}

// vfCardClose shuts the card.
func vfCardClose(sound bool) {
	if !vfCard.open {
		return
	}
	vfCard.open = false
	vfCard.host = vfCardHostNone
	vfCard.confirm = false
	vfCard.rows = vfCard.rows[:0]
	if sound {
		PlayBackSound()
	}
}

// vfuiHasLocal reports whether this device keeps a record of its own for
// kind/id (what Forget would drop). verified.go has no reader for it: the
// store is read here under its lock, without changing anything.
func vfuiHasLocal(kind, id string) bool {
	key := vfMapKey(vfModel(), kind, vfNormalizeID(id))
	vfStore.mu.Lock()
	defer vfStore.mu.Unlock()
	if !vfStore.loaded {
		return false
	}
	_, ok := vfStore.index[key]
	return ok
}

// vfuiSourceLabel says where a result came from.
func vfuiSourceLabel(by string) string {
	switch by {
	case vfByAuto:
		return "Seen by JukaHub"
	case vfByUser:
		return "Marked by you"
	}
	return "From a shared list"
}

// vfuiIssueSource says, inside brackets, who described a known issue.
func vfuiIssueSource(by string) string {
	switch by {
	case vfByAuto:
		return "noted by JukaHub"
	case vfByUser:
		return "described by you"
	}
	return "from a shared list"
}

// vfuiResultLine is a check's second line: where its result came from, the
// note, the release and the emulator it was seen with.
func vfuiResultLine(res vfResult, have bool) string {
	if !have {
		return "Not seen yet"
	}
	parts := make([]string, 0, 4)
	parts = append(parts, vfuiSourceLabel(res.By))
	if note := strings.TrimSpace(res.Note); note != "" {
		parts = append(parts, note)
	}
	if v := strings.TrimSpace(res.Version); v != "" {
		parts = append(parts, vfVersionLabel(v))
	}
	if e := strings.TrimSpace(res.Engine); e != "" {
		parts = append(parts, e)
	}
	return strings.Join(parts, " · ")
}

// vfuiKindNoun names a kind for the Forget row.
func vfuiKindNoun(kind string) string {
	switch kind {
	case vfKindPort:
		return "port"
	case vfKindApp:
		return "app"
	case vfKindEmulator:
		return "emulator"
	}
	return "game"
}

// vfCardRebuild reads the record again and words the rows. On open, after
// every change and when vfGeneration moves.
func vfCardRebuild() {
	c := &vfCard
	c.gen = vfGeneration.Load()
	c.loaded = vfLoaded()
	rec, ok := vfLookup(c.kind, c.id)
	if !ok {
		rec = vfRecord{Kind: c.kind, ID: c.id, Title: c.title}
	}
	c.rec = rec
	c.verdict = vfVerdictFor(rec)
	c.hasLocal = vfuiHasLocal(c.kind, c.id)

	glyph := "info"
	switch c.verdict.Level {
	case vfLevelVerified:
		glyph = "check"
	case vfLevelIssue:
		glyph = "warning"
	}
	sub := c.verdict.Text
	if !c.loaded {
		sub = "Reading this device's results…"
	}
	c.header = UIPanelHeader(c.title, sub, glyph)

	c.summary = c.verdict.Detail
	if c.verdict.Level == vfLevelIssue && rec.Issue != nil {
		// The issue has its own paragraph; the summary says what passed.
		c.summary = vfuiPassedLine(c.verdict)
	}
	if c.verdict.Stale {
		c.summary += " · not seen on this JukaHub release yet"
	}
	c.issue = ""
	if rec.Issue != nil {
		c.issue = "Known issue: " + rec.Issue.Text + " (" + vfuiIssueSource(rec.Issue.By)
		if rec.Issue.Version != "" {
			c.issue += " · " + vfVersionLabel(rec.Issue.Version)
		}
		c.issue += ")"
	}

	// Rows. The focused row stays focused; one that went away hands the
	// focus to the row now in its place.
	prevIdx := -1
	for i := range c.rows {
		if c.rows[i].id == c.nav.focus {
			prevIdx = i
			break
		}
	}
	c.rows = c.rows[:0]
	for _, check := range vfChecks {
		res, have := rec.Checks[check]
		row := vfCardRow{
			id:     vfCardRowCheck + check,
			check:  check,
			label:  vfCheckLabel(check),
			detail: vfuiResultLine(res, have),
			tinted: true,
		}
		switch {
		case have && res.State == vfPass:
			row.icon, row.ink, row.state, row.act = "check", ColorSuccess, "Works", "Mark as not working"
		case have && res.State == vfFail:
			row.icon, row.ink, row.state = "close", ColorWarning, "Doesn't work"
			row.act = "Mark as not checked"
			if res.By == vfByCatalog {
				// A shared list's result cannot be unmarked here, only
				// answered: the next step is the player's own "works".
				row.act = "Mark as working"
			}
		default:
			row.icon, row.ink, row.state, row.act = "minus", ColorTextTertiary(), "Not checked", "Mark as working"
		}
		c.rows = append(c.rows, row)
	}
	issueRow := vfCardRow{
		id:     vfCardRowIssue,
		icon:   "edit",
		label:  "Describe a problem",
		detail: "In your own words; it shows on this page and in shared results",
		act:    "Describe",
	}
	if rec.Issue != nil && rec.Issue.By == vfByUser {
		issueRow.label, issueRow.detail, issueRow.act = "Change the problem you described", rec.Issue.Text, "Change"
	}
	c.rows = append(c.rows, issueRow)
	if rec.Issue != nil {
		c.rows = append(c.rows, vfCardRow{
			id:     vfCardRowClear,
			icon:   "broom",
			label:  "Clear the problem",
			detail: "It doesn't happen any more",
			act:    "Clear",
		})
	}
	if c.hasLocal {
		c.rows = append(c.rows, vfCardRow{
			id:     vfCardRowForget,
			icon:   "trash",
			label:  "Forget this " + vfuiKindNoun(c.kind) + "'s results",
			detail: "What this device saw and what you marked; shared lists stay",
			act:    "Forget",
		})
	}
	if c.confirm && !c.hasLocal {
		c.confirm = false
	}
	found := false
	for i := range c.rows {
		if c.rows[i].id == c.nav.focus {
			found = true
			break
		}
	}
	if !found && len(c.rows) > 0 {
		if prevIdx < 0 {
			prevIdx = 0
		}
		c.nav.focus = c.rows[min(prevIdx, len(c.rows)-1)].id
	}
}

// vfuiPassedLine is the summary under a known issue: how many checks passed.
func vfuiPassedLine(v vfVerdict) string {
	switch {
	case v.Passed == len(vfChecks):
		return "All five checks passed anyway"
	case v.Passed > 0:
		return strconv.Itoa(v.Passed) + " of 5 checks passed"
	}
	return "No check has passed yet"
}

// vfCardFresh rebuilds the rows when the store changed since.
func vfCardFresh() {
	if vfCard.open && vfCard.gen != vfGeneration.Load() {
		vfCardRebuild()
	}
}

// vfCardRowByID is the row with id, or nil.
func vfCardRowByID(id string) *vfCardRow {
	for i := range vfCard.rows {
		if vfCard.rows[i].id == id {
			return &vfCard.rows[i]
		}
	}
	return nil
}

// ── Card actions ─────────────────────────────────────────────────────────

// vfCardReady says whether a change can be made now: the results are read.
func vfCardReady() bool {
	if vfLoaded() {
		return true
	}
	ShowToast("Still reading this device's results — try again in a moment", ToastKindInfo)
	return false
}

// vfCardActivate is A (or a click) on row id.
func vfCardActivate(id string) {
	c := &vfCard
	row := vfCardRowByID(id)
	if row == nil {
		return
	}
	switch {
	case row.check != "":
		if !vfCardReady() {
			return
		}
		vfCardCycle(row.check)
	case id == vfCardRowIssue:
		if !vfCardReady() {
			return
		}
		vfCardDescribe()
	case id == vfCardRowClear:
		if !vfCardReady() {
			return
		}
		if vfSetIssue(c.kind, c.id, c.name, "", vfByUser) {
			PlayToggleSound()
			ShowToast("Problem cleared for "+c.title, ToastKindInfo)
		}
		vfCardRebuild()
	case id == vfCardRowForget:
		c.confirm, c.confirmFocus = true, 0
		PlayActivateSound()
	}
}

// vfCardCycle steps one check: Works → Doesn't work → Not checked. A shared
// list's failed result has no "not checked" here (clearing the device's own
// result shows the list's again), so it goes to Works.
func vfCardCycle(check string) {
	c := &vfCard
	res, have := c.rec.Checks[check]
	switch {
	case !have:
		vfNote(c.kind, c.id, c.name, check, vfPass, vfByUser, "", "")
	case res.State == vfPass:
		vfNote(c.kind, c.id, c.name, check, vfFail, vfByUser, "", "")
	case res.By != vfByCatalog:
		vfClearCheck(c.kind, c.id, check)
	default:
		vfNote(c.kind, c.id, c.name, check, vfPass, vfByUser, "", "")
	}
	PlayToggleSound()
	vfCardRebuild()
}

// vfCardDescribe opens the keyboard for the player's own words. Done saves
// them; B (or Done on an empty line) changes nothing.
func vfCardDescribe() {
	c := &vfCard
	kind, id, name, title := c.kind, c.id, c.name, c.title
	seed := ""
	if c.rec.Issue != nil && c.rec.Issue.By == vfByUser {
		seed = c.rec.Issue.Text
	}
	PlayActivateSound()
	osPromptOpen("Describe the problem", "What goes wrong, in a few words", "Save", seed, vfuiIssueMaxRunes, func(text string) {
		if text == "" {
			return
		}
		if vfSetIssue(kind, id, name, text, vfByUser) {
			ShowToast("Noted for "+title, ToastKindInfo)
		}
		if vfCard.open && vfCard.kind == kind && vfCard.id == id {
			vfCardRebuild()
		}
	})
}

// vfCardForget is Forget on the question.
func vfCardForget() {
	c := &vfCard
	c.confirm = false
	if !vfCardReady() {
		return
	}
	if vfForget(c.kind, c.id) {
		PlayToggleSound()
		ShowToast("Forgot what this device saw for "+c.title, ToastKindInfo)
	}
	vfCardRebuild()
}

// vfCardAction is the card's input.
func vfCardAction(act tbxAction) {
	c := &vfCard
	if !c.open || act == tbxActNone {
		return
	}
	vfCardFresh()
	if c.confirm {
		switch act {
		case tbxActLeft:
			if c.confirmFocus != 0 {
				c.confirmFocus = 0
				PlayNavSound()
			}
		case tbxActRight:
			if c.confirmFocus != 1 {
				c.confirmFocus = 1
				PlayNavSound()
			}
		case tbxActConfirm:
			if c.confirmFocus == 1 {
				vfCardForget()
			} else {
				c.confirm = false
				PlayBackSound()
			}
		case tbxActBack:
			c.confirm = false
			PlayBackSound()
		}
		return
	}
	switch act {
	case tbxActBack:
		vfCardClose(true)
	case tbxActConfirm:
		vfCardActivate(c.nav.focus)
	case tbxActUp, tbxActDown, tbxActPageUp, tbxActPageDown, tbxActHome, tbxActEnd:
		tbxNavAction(&c.nav, act)
	}
}

// vfCardKey and vfCardPad take every press while the card is up.
func vfCardKey(e *sdl.KeyboardEvent) bool {
	vfCardAction(tbxKeyAction(e))
	return true
}

func vfCardPad(e *sdl.ControllerButtonEvent) bool {
	vfCardAction(tbxPadAction(e))
	return true
}

// vfCardMouse takes a click while the card is up: a row or a button presses,
// a click off the card goes back.
func vfCardMouse(mx, my int32) bool {
	c := &vfCard
	if !c.open {
		return false
	}
	vfCardFresh()
	if c.confirm {
		for i, r := range c.pills {
			if r.W > 0 && pointInRect(mx, my, r) {
				c.confirmFocus = i
				vfCardAction(tbxActConfirm)
				return true
			}
		}
		if c.card.W > 0 && !pointInRect(mx, my, c.card) {
			vfCardAction(tbxActBack)
		}
		return true
	}
	if it, ok := c.nav.hit(mx, my); ok {
		c.nav.focus = it.id
		vfCardActivate(it.id)
		return true
	}
	if c.card.W > 0 && !pointInRect(mx, my, c.card) {
		vfCardClose(true)
	}
	return true
}

// vfCardHints are the card's buttons in the device's words.
func vfCardHints() []FooterHint {
	c := &vfCard
	if c.confirm {
		label := "Keep"
		if c.confirmFocus == 1 {
			label = "Forget"
		}
		return []FooterHint{{Button: "Left/Right", Label: "Choose"}, {Button: "A", Label: label}, {Button: "B", Label: "Keep"}}
	}
	hints := make([]FooterHint, 0, 3)
	hints = append(hints, FooterHint{Button: "Up/Down", Label: "Choose"})
	if row := vfCardRowByID(c.nav.focus); row != nil && row.act != "" {
		hints = append(hints, FooterHint{Button: "A", Label: row.act})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

// ── Drawing the card ─────────────────────────────────────────────────────

// vfCardRender draws the card over its host, its hints as its last line
// like the More sheet's (on the Tested page the scrim dims the footer band).
func vfCardRender(renderer *sdl.Renderer, config *Config) {
	c := &vfCard
	if !c.open || renderer == nil || config == nil {
		return
	}
	vfCardFresh()
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	header := c.header
	headerH := UIPanelHeaderBand(config, header, 6)
	cardW := min(int32(780), screenWidth-48)
	textW := cardW - 2*OverlayPadX
	const summaryMax, issueMax = 2, 3
	summary := c.wrapSummary.get(fonts.small, c.summary, textW)
	issue := c.wrapIssue.get(fonts.small, c.issue, textW)
	nSummary, nIssue := min(len(summary), summaryMax), 0
	if c.issue != "" {
		nIssue = min(len(issue), issueMax)
	}
	lineH := fonts.smallH + 4
	topH := int32(nSummary)*lineH + 10
	if nIssue > 0 {
		topH += int32(nIssue)*lineH + 8
	}
	rowH := fonts.bodyH + fonts.tinyH + 22
	footerH := fonts.smallH + 20
	listH := int32(len(c.rows)) * rowH
	if c.confirm {
		listH = 3*fonts.bodyH + 2*fonts.smallH + 90
	}
	cardH := min(headerH+topH+listH+footerH+8, screenHeight-48)
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	c.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	x := card.X + OverlayPadX
	y := card.Y + headerH
	for i := 0; i < nSummary; i++ {
		text := summary[i]
		if i == nSummary-1 && len(summary) > nSummary {
			text = ellipsize(fonts.small, text+" …", textW)
		}
		renderText(renderer, config, fonts.small, text, ColorTextSecondary(), x, y)
		y += lineH
	}
	y += 6
	for i := 0; i < nIssue; i++ {
		text := issue[i]
		if i == nIssue-1 && len(issue) > nIssue {
			text = ellipsize(fonts.small, text+" …", textW)
		}
		renderText(renderer, config, fonts.small, text, ColorWarning, x, y)
		y += lineH
	}
	if nIssue > 0 {
		y += 8
	}
	listTop := y + 4
	listBottom := card.Y + card.H - footerH - 4
	if c.confirm {
		vfCardDrawConfirm(renderer, config, fonts, sdl.Rect{X: x, Y: listTop, W: textW, H: listBottom - listTop})
	} else {
		c.pills = [2]sdl.Rect{}
		vfCardDrawRows(renderer, config, fonts, x, textW, listTop, listBottom, rowH)
	}
	hint := hintLine(vfCardHints()...)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, textW), ColorTextSecondary(),
		x, card.Y+card.H-footerH+4)
}

// vfCardDrawRows lays the rows out on the card's focus list and draws the
// ones in view.
func vfCardDrawRows(renderer *sdl.Renderer, config *Config, fonts tbxFonts, x, w, top, bottom, rowH int32) {
	c := &vfCard
	nav := &c.nav
	viewH := bottom - top
	if viewH < rowH {
		viewH = rowH
	}
	nav.begin(top, viewH)
	for i := range c.rows {
		nav.add(c.rows[i].id, sdl.Rect{X: x, Y: int32(i) * rowH, W: w, H: rowH - 6}, i, false)
	}
	nav.end()
	tbxClip(renderer, sdl.Rect{X: x - 4, Y: top, W: w + 8, H: viewH})
	stateW := int32(0)
	for i := range c.rows {
		if c.rows[i].state != "" {
			sw, _ := measureText(fonts.small, c.rows[i].state)
			stateW = max(stateW, sw)
		}
	}
	iconS := int32(28)
	for i := range c.rows {
		row := &c.rows[i]
		rect := nav.screenRect(nav.items[i])
		if rect.Y+rect.H < top || rect.Y > top+viewH {
			continue
		}
		focused := nav.is(row.id)
		UIListRow(renderer, rect, glassRowRadius, focused, false)
		icon := sdl.Rect{X: rect.X + 14, Y: rect.Y + (rect.H-iconS)/2, W: iconS, H: iconS}
		if row.tinted {
			drawHomeGlyphTinted(renderer, row.icon, icon, row.ink)
		} else {
			DrawHomeIcon(renderer, row.icon, icon, focused)
		}
		textX := icon.X + icon.W + 14
		right := rect.X + rect.W - 16
		if row.state != "" {
			_, sh := measureText(fonts.small, row.state)
			renderText(renderer, config, fonts.small, row.state, row.ink, right-stateW, rect.Y+(rect.H-sh)/2)
			right -= stateW + 14
		}
		textW := right - textX
		ty := rect.Y + (rect.H-fonts.bodyH-fonts.tinyH-2)/2
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, row.label, textW), ColorTextPrimary(), textX, ty)
		renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, row.detail, textW), ColorTextSecondary(), textX, ty+fonts.bodyH+2)
	}
	tbxUnclip(renderer)
	tbxDrawScrollbar(renderer, x+w+8, top, viewH, nav.contentH, nav.scroll)
}

// vfCardDrawConfirm draws the Forget question in place of the rows: the
// question, what goes and what stays, and Keep (focused first) and Forget.
func vfCardDrawConfirm(renderer *sdl.Renderer, config *Config, fonts tbxFonts, body sdl.Rect) {
	c := &vfCard
	y := body.Y + 6
	ask := "Forget what this device saw for " + c.title + "?"
	lines := c.wrapAsk.get(fonts.body, ask, body.W)
	for i, line := range lines {
		if i == 2 {
			break
		}
		if i == 1 && len(lines) > 2 {
			// A long title: the question still ends visibly cut.
			line = ellipsize(fonts.body, line+" …", body.W)
		}
		renderText(renderer, config, fonts.body, line, ColorTextPrimary(), body.X, y)
		y += fonts.bodyH + 4
	}
	y += 4
	for _, line := range [...]string{
		"Its checks and the problem you described are removed from this device.",
		"Results from a shared list stay and show again.",
	} {
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, line, body.W), ColorTextSecondary(), body.X, y)
		y += fonts.smallH + 4
	}
	bh := int32(46)
	by := min(y+16, body.Y+body.H-bh)
	labels := [2]string{"Keep", "Forget"}
	bx := body.X + body.W
	for i := 1; i >= 0; i-- {
		w := tbxButtonWidth(fonts.body, labels[i], 120)
		bx -= w
		c.pills[i] = sdl.Rect{X: bx, Y: by, W: w, H: bh}
		bx -= 12
	}
	for i := range labels {
		tbxDrawButton(renderer, config, fonts.body, c.pills[i], labels[i], false, c.confirmFocus == i, false)
	}
}
