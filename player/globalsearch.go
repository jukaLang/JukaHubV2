package main

// globalsearch.go — the search sheet behind Home's search pill.
//
// The pill on Home (A, a tap or Y), "/" on Home and F2 anywhere open one sheet:
// a search field on top and the results under it, grouped as Screens &
// settings, Apps & games, Files, Media history and a final "Search the web"
// row (homesearch_index.go decides what matches; homesearch_render.go draws).
// Selecting a result goes straight there: a settings page, an app, a file in
// the right viewer, the video at its saved position, or the Browser.
//
// The sheet replaces the old F2 overlay, which searched only scene names, read
// the working directory for "files", drew text glyphs such as "[N]" for icons
// and let a typed "b" close it. It keeps that overlay's switches
// (globalSearchOpen, gsCursor, gsResults) because the overlay chains in main.go,
// controller.go, shoulders.go and ambient.go already route around it.
//
// Typing: a physical keyboard types straight into the field (handleTextInput
// routes to the sheet first). With a pad, or on the handheld, the on-screen
// keyboard opens under the sheet aimed at the query (homeSearchVKTarget); the
// results filter on every key, Y / Done closes the keyboard and puts the focus
// on the first result, and X reopens it to edit the query.

import (
	"strings"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

var (
	// globalSearchOpen is the sheet's visibility. The overlay chains test it,
	// so it keeps the old overlay's name.
	globalSearchOpen bool
	// gsQuery is the text in the field.
	gsQuery string
	// gsCursor is the highlighted row in gsResults (-1 = the field itself).
	gsCursor int
	// gsResults are the selectable rows in display order; the group headers
	// are drawn between them wherever Category changes.
	gsResults []gsResult
)

// gsResult is one row of the sheet.
type gsResult struct {
	Title    string
	Subtitle string
	Icon     string // shared glyph name (DrawHomeIcon)
	Category string // the group header the row sits under
	Action   func(*Config)
	// Dimmed rows are listed but cannot work right now (offline); Blocked is
	// what a press on one says instead of running Action. Badge is the short
	// tag drawn at the row's right end ("Juka", "Needs internet").
	Dimmed  bool
	Blocked string
	Badge   string
	// Keep rows stay on the sheet when pressed: a recent search refills the
	// field with Fill instead of navigating away.
	Keep bool
	Fill string
}

// homeSearchVKTarget aims the on-screen keyboard at the search field. It is a
// target of its own rather than one of vkeyboard.go's iota values, so the
// keyboard's switch statements carry one case per hook (see vkCurrentText,
// vkSetText, vkTitle, vkPlaceholder, vkHints, vkSubmit).
const homeSearchVKTarget vkTargetKind = 64

// homeSearchMaxQuery bounds the field; nothing worth finding needs more.
const homeSearchMaxQuery = 96

// homeSearchSession is the open sheet's state.
type homeSearchSession struct {
	config  *Config
	screens []homeSearchScreen
	history []string // recent queries this session, newest first
	gen     uint64   // index generation the results were built from
	scroll  int      // first visible display row
	// skipText is the text event of the key that opened the sheet ("/" on
	// Home): SDL sends it right after the key press, and it must not become the
	// first character of the query.
	skipText string
	// Geometry recorded by the last draw, shared with the mouse handler so a
	// tap resolves the row that was actually painted.
	sheet sdl.Rect
	field sdl.Rect
	clear sdl.Rect
	hits  []homeSearchHit
}

// homeSearchHit is one drawn result row and the result it shows.
type homeSearchHit struct {
	index int
	rect  sdl.Rect
}

var homeSearch homeSearchSession

// homeSearchOpen shows the sheet (or, when it is already open, just brings up
// the keyboard). withKeyboard opens the on-screen keyboard aimed at the field:
// a pad press or the handheld, where there is nothing else to type on.
func homeSearchOpen(config *Config, withKeyboard bool) {
	if config == nil {
		config = appConfig
	}
	if !globalSearchOpen {
		homeSearchBegin(config)
	}
	if withKeyboard && !homeSearchKeyboardUp() {
		homeSearchOpenKeyboard()
	}
}

// homeSearchBegin resets the sheet for a new search. Other floating panels
// close first: two overlays fighting over the same keys is how a press ends up
// in the wrong one.
func homeSearchBegin(config *Config) {
	globalSearchOpen = true
	shortcutsOpen = false
	clipSearchOpen = false
	tb.startOpen = false
	homeCalendarOpen = false
	if nc.open {
		notifDismiss()
	}
	gsQuery = ""
	gsCursor = 0
	homeSearch.config = config
	homeSearch.scroll = 0
	homeSearch.hits = homeSearch.hits[:0]
	homeSearch.screens = homeSearchScreens(config)
	homeSearchRefreshIndex(config)
	homeSearchRun()
}

// homeSearchClose hides the sheet and drops its keyboard. The session history
// stays (homeSearchActivate records a query that led somewhere), so the next
// search can repeat it.
func homeSearchClose() {
	if homeSearchKeyboardUp() {
		vkCloseKeyboard()
	}
	globalSearchOpen = false
	gsQuery = ""
	gsResults = nil
	gsCursor = 0
	homeSearch.scroll = 0
	homeSearch.hits = homeSearch.hits[:0]
	homeSearch.config = nil
}

// homeSearchToggle is F2: open the sheet anywhere, or close it.
func homeSearchToggle(config *Config) {
	if globalSearchOpen {
		homeSearchClose()
		PlayBackSound()
		return
	}
	PlayActivateSound()
	homeSearchOpen(config, homeOnHandheld())
}

// homeSearchKeyboardUp reports whether the on-screen keyboard is typing into
// the search field.
func homeSearchKeyboardUp() bool {
	return virtualKeyboardActive && vkTarget == homeSearchVKTarget
}

// homeSearchOpenKeyboard opens the on-screen keyboard on the query, the same
// way the other live-filter fields open it (vkOpenTVSearch).
func homeSearchOpenKeyboard() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = homeSearchVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// homeSearchKeyboardDone is the keyboard's Done (Y / Enter): the keyboard has
// closed, and the focus moves to the first result so A opens it.
func homeSearchKeyboardDone() {
	if !globalSearchOpen {
		return
	}
	gsCursor = 0
	if len(gsResults) == 0 {
		gsCursor = -1
	}
	homeSearch.scroll = 0
}

// homeSearchText is the field's text for the keyboard (vkCurrentText).
func homeSearchText() string {
	return gsQuery
}

// homeSearchSetQuery replaces the query and re-runs the search. Both keyboards
// end here, so the results follow every key.
func homeSearchSetQuery(text string) {
	if utf8.RuneCountInString(text) > homeSearchMaxQuery {
		runes := []rune(text)
		text = string(runes[:homeSearchMaxQuery])
	}
	if text == gsQuery {
		return
	}
	gsQuery = text
	gsCursor = 0
	homeSearch.scroll = 0
	homeSearchRun()
}

// homeSearchRun rebuilds the result rows from the current query.
func homeSearchRun() {
	config := homeSearch.config
	if config == nil {
		config = appConfig
	}
	indexedApps, indexedGames, indexedFiles := homeSearchIndexSnapshot()
	src := homeSearchSources{
		screens: homeSearch.screens,
		files:   indexedFiles,
		recent:  getRecentItems(),
		history: homeSearch.history,
		online:  IsOnline(),
	}
	// The launcher's own list wins when the Apps screen has been opened (it
	// knows which rows failed to start); otherwise the background scan plus
	// the built-in applets stand in for it.
	if config != nil {
		if entries, ok := config.Variables.Custom[appsEntriesVar].([]FileEntry); ok && len(entries) > 0 {
			src.apps = entries
		}
	}
	if len(src.apps) == 0 {
		src.apps = append(append([]FileEntry(nil), indexedApps...), builtinAppEntries(config)...)
	}
	src.games = append(recentGames(), indexedGames...)
	gsResults = homeSearchResults(gsQuery, src)
	homeSearch.gen = homeSearchIdx.gen.Load()
	if gsCursor >= len(gsResults) {
		gsCursor = len(gsResults) - 1
	}
}

// homeSearchRefreshIfIndexed re-runs the query when a background scan has
// published since the rows were built, keeping the highlight on the same row.
func homeSearchRefreshIfIndexed() {
	if homeSearchIdx.gen.Load() == homeSearch.gen {
		return
	}
	title := ""
	if gsCursor >= 0 && gsCursor < len(gsResults) {
		title = gsResults[gsCursor].Title
	}
	homeSearchRun()
	for i, r := range gsResults {
		if title != "" && r.Title == title {
			gsCursor = i
			return
		}
	}
}

// homeSearchRemember puts a query that led somewhere at the top of the
// session's history.
func homeSearchRemember(query string) {
	q := strings.TrimSpace(query)
	if q == "" {
		return
	}
	next := make([]string, 0, homeSearchMaxRecent)
	next = append(next, q)
	for _, h := range homeSearch.history {
		if strings.EqualFold(h, q) {
			continue
		}
		if len(next) >= homeSearchMaxRecent {
			break
		}
		next = append(next, h)
	}
	homeSearch.history = next
}

// homeSearchActivate runs the result at index i: a recent search refills the
// field, a dimmed row says why it cannot open, anything else closes the sheet
// and goes there.
func homeSearchActivate(config *Config, i int) {
	if i < 0 || i >= len(gsResults) {
		return
	}
	r := gsResults[i]
	if r.Keep {
		PlayNavSound()
		homeSearchSetQuery(r.Fill)
		return
	}
	if r.Blocked != "" {
		PlayBackSound()
		showToast(r.Blocked, ToastWarn())
		return
	}
	homeSearchRemember(gsQuery)
	homeSearchClose()
	PlayActivateSound()
	if r.Action != nil {
		if config == nil {
			config = appConfig
		}
		r.Action(config)
	}
}

// homeSearchMove steps the highlight; Up from the first row returns to the
// field (-1), where A opens the keyboard.
func homeSearchMove(delta int) {
	if len(gsResults) == 0 {
		gsCursor = -1
		return
	}
	next := gsCursor + delta
	if next < -1 {
		next = -1
	}
	if next >= len(gsResults) {
		next = len(gsResults) - 1
	}
	if next != gsCursor {
		gsCursor = next
		PlayNavSound()
	}
}

// homeSearchJumpGroup moves the highlight to the first row of the next (or
// previous) group, so L1/R1 and Page keys hop over a long group.
func homeSearchJumpGroup(dir int) {
	if len(gsResults) == 0 {
		return
	}
	i := gsCursor
	if i < 0 {
		i = 0
	}
	cat := gsResults[i].Category
	if dir > 0 {
		for j := i + 1; j < len(gsResults); j++ {
			if gsResults[j].Category != cat {
				gsCursor = j
				PlayNavSound()
				return
			}
		}
		return
	}
	// Back to the start of this group, or to the previous group's start when
	// the highlight is already on this group's first row.
	start := i
	for start > 0 && gsResults[start-1].Category == cat {
		start--
	}
	if start == i && start > 0 {
		start--
		prev := gsResults[start].Category
		for start > 0 && gsResults[start-1].Category == prev {
			start--
		}
	}
	if start != gsCursor {
		gsCursor = start
		PlayNavSound()
	}
}

// ── Input ────────────────────────────────────────────────────────────────

// handleGlobalSearchTextInput types physical-keyboard text into the field.
func handleGlobalSearchTextInput(text string) {
	if !globalSearchOpen || text == "" {
		return
	}
	if skip := homeSearch.skipText; skip != "" {
		homeSearch.skipText = ""
		if text == skip && gsQuery == "" {
			return
		}
	}
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, text)
	if clean == "" {
		return
	}
	homeSearchSetQuery(gsQuery + clean)
}

// handleGSInput handles a physical key while the sheet is open. A real
// keyboard needs no on-screen one, so the first key closes it.
func handleGSInput(e *sdl.KeyboardEvent, config *Config) {
	if !globalSearchOpen || e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	homeSearch.skipText = ""
	if homeSearchKeyboardUp() {
		vkCloseKeyboard()
		if e.Keysym.Sym == sdl.K_ESCAPE {
			return
		}
	}
	ctrl := e.Keysym.Mod&sdl.KMOD_CTRL != 0
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_F2:
		// F2 opened the sheet, so it closes it too: while the sheet is open
		// every key reaches this handler before main.go's F2 toggle.
		homeSearchClose()
		PlayBackSound()
	case sdl.K_F4:
		// Say it instead, as Y does on the pad. The global F4 is read after
		// this sheet, so it is answered here; Alt+F4 stays the window's quit.
		if e.Repeat == 0 && e.Keysym.Mod&(sdl.KMOD_ALT|sdl.KMOD_CTRL) == 0 {
			voiceOpen(config)
		}
	case sdl.K_UP:
		homeSearchMove(-1)
	case sdl.K_DOWN:
		homeSearchMove(1)
	case sdl.K_PAGEDOWN, sdl.K_TAB:
		homeSearchJumpGroup(1)
	case sdl.K_PAGEUP:
		homeSearchJumpGroup(-1)
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		i := gsCursor
		if i < 0 && len(gsResults) > 0 {
			// Enter in the field opens the best match, like a launcher.
			i = 0
		}
		homeSearchActivate(config, i)
	case sdl.K_BACKSPACE:
		if ctrl {
			homeSearchSetQuery("")
		} else if gsQuery != "" {
			homeSearchSetQuery(trimLastRune(gsQuery))
		}
	case sdl.K_DELETE:
		homeSearchSetQuery("")
	}
}

// homeSearchController handles a pad button while the sheet is open. It
// returns false while the on-screen keyboard is up, so the main loop hands the
// button to the keyboard (which types into the query through
// homeSearchVKTarget).
func homeSearchController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil {
		return false
	}
	if homeSearchKeyboardUp() {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		homeSearchMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		homeSearchMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		homeSearchJumpGroup(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		homeSearchJumpGroup(1)
	case sdl.CONTROLLER_BUTTON_A:
		if gsCursor < 0 {
			PlayActivateSound()
			homeSearchOpenKeyboard()
			return true
		}
		homeSearchActivate(config, gsCursor)
	case sdl.CONTROLLER_BUTTON_X:
		// X edits the query: the keyboard comes back with the text in it.
		PlayActivateSound()
		homeSearchOpenKeyboard()
	case sdl.CONTROLLER_BUTTON_Y:
		// Y says it instead: the sheet closes and Voice command opens
		// (voicecmd.go). Y used to be a second X.
		voiceOpen(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		homeSearchClose()
		PlayBackSound()
	}
	return true
}

// homeSearchMouseDown handles a click or tap while the sheet is open. A press
// on the on-screen keyboard is left to it; a result opens; the field brings
// the keyboard up on a handheld; anywhere outside the sheet closes it. Every
// other press is swallowed so nothing behind the sheet reacts.
func homeSearchMouseDown(mx, my int32, config *Config) bool {
	if !globalSearchOpen {
		return false
	}
	if homeSearchKeyboardUp() && !vkOutsidePanel(mx, my) {
		return false
	}
	if homeSearch.clear.W > 0 && pointInRect(mx, my, homeSearch.clear) {
		PlayNavSound()
		homeSearchSetQuery("")
		return true
	}
	if homeSearch.field.W > 0 && pointInRect(mx, my, homeSearch.field) {
		gsCursor = -1
		if homeOnHandheld() && !homeSearchKeyboardUp() {
			PlayActivateSound()
			homeSearchOpenKeyboard()
		}
		return true
	}
	for _, hit := range homeSearch.hits {
		if pointInRect(mx, my, hit.rect) {
			if homeSearchKeyboardUp() {
				vkCloseKeyboard()
			}
			gsCursor = hit.index
			homeSearchActivate(config, hit.index)
			return true
		}
	}
	if homeSearch.sheet.W > 0 && !pointInRect(mx, my, homeSearch.sheet) {
		homeSearchClose()
		PlayBackSound()
		return true
	}
	if homeSearchKeyboardUp() {
		// A tap on the sheet's empty space dismisses the keyboard, the way a
		// tap on the dimmed content behind it does everywhere else.
		vkCloseKeyboard()
	}
	return true
}

// homeSearchWheel scrolls the result list by whole rows.
func homeSearchWheel(dy int32) bool {
	if !globalSearchOpen || dy == 0 {
		return false
	}
	if dy > 0 {
		homeSearchMove(-1)
	} else {
		homeSearchMove(1)
	}
	return true
}
