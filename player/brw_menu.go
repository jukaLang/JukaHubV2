package main

// brw_menu.go — the Browser's Select menu.
//
// One sheet holds everything the Browser can do, so the page can have the whole
// screen (brw_fullscreen.go): Back, Forward, Reload and Home across the top in a
// browser toolbar's order, and under them the address bar, web search, the
// page's links, find, zoom, link mode, saving and the saved pages, site info,
// the ad filter, sign-ins, the layout switch, Settings, the issue tracker and
// the way out. It follows the WPE browser on the same device, whose Select menu
// starts with Back, Forward, Home and the address bar.
//
// Select opens and closes it on the pad; M or the Menu key on a keyboard, a
// right click with a mouse. B, Esc and a click beside the sheet close it too.
// The rows keep their places whatever the page — a row that cannot act is
// dimmed, not removed — so Select then A is Back whenever there is a page to go
// back to (with none the menu opens on the address row), and the hands learn
// the rest. Zoom and Find change in place with Left/Right; every other row acts and
// closes the menu. It opens over the classic layout as well.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// brwMenuID names one entry of the menu. The order is the menu's order, and it
// never changes with the page.
type brwMenuID int

const (
	brwMenuBack brwMenuID = iota
	brwMenuForward
	brwMenuReload
	brwMenuHome
	brwMenuAddress
	brwMenuSearch
	brwMenuLinks
	brwMenuFind
	brwMenuZoom
	brwMenuMode
	brwMenuSave
	brwMenuSaved
	brwMenuWPE
	brwMenuInfo
	brwMenuFilter
	brwMenuSignIns
	brwMenuDropSignIns
	brwMenuLayout
	brwMenuSettings
	brwMenuIssues
	brwMenuClose
	// brwMenuCount is the number of entries, not an entry.
	brwMenuCount
)

// brwMenuNavCount is how many entries share the top row: Back, Forward,
// Reload and Home.
const brwMenuNavCount = 4

// brwMenuPage is how far L1/R1 (PgUp/PgDn) move the cursor down the list.
const brwMenuPage = 5

// brwMenuItem is one entry as the menu draws it this frame.
type brwMenuItem struct {
	ID    brwMenuID
	Label string
	Icon  string
	// Value is the read-out at the row's right (the engine, a count, a state),
	// written in Tone (brwToneNeutral … brwToneBad, brw_siteinfo.go).
	Value string
	Tone  int
	// Key is the row's own shortcut on the page, the way a hint names it
	// ("Y / /"); KeyOnly marks one that only a keyboard has.
	Key     string
	KeyOnly bool
	// Disabled rows keep their place but cannot act: A says why.
	Disabled bool
	// Adjust rows change in place with Left/Right instead of closing the menu.
	Adjust bool
}

// brwMenuState is the open menu: the cursor, the top row's column the cursor
// last left (Up from the list goes back to it), the list's scroll in pixels,
// and the last frame's geometry for the mouse — the sheet, the list's
// viewport, one rect per entry and the − / + targets of the rows that change
// in place. A zero rect was not drawn.
type brwMenuState struct {
	open   bool
	cursor int
	navCol int
	scroll int32
	card   sdl.Rect
	view   sdl.Rect
	rects  []sdl.Rect
	minus  []sdl.Rect
	plus   []sdl.Rect
}

var brwMenu brwMenuState

// brwMenuItems is the menu as it stands this frame: every entry in its fixed
// place, carrying the page's live state.
func brwMenuItems(config *Config) []brwMenuItem {
	items := make([]brwMenuItem, brwMenuCount)
	elem, haveElem := findBrowserElement(config)
	var doc *webDocument
	if haveElem {
		doc = browserDocumentFor(elem.Variable)
	}
	page := ""
	if doc != nil {
		page = strings.TrimSpace(doc.URL)
	}

	items[brwMenuBack] = brwMenuItem{Label: "Back", Icon: "back", Disabled: !browserHistoryAvailable(-1)}
	items[brwMenuForward] = brwMenuItem{Label: "Forward", Icon: "forward", Disabled: !browserHistoryAvailable(1)}
	items[brwMenuReload] = brwMenuItem{Label: "Reload", Icon: "refresh"}
	items[brwMenuHome] = brwMenuItem{Label: "Home", Icon: "home"}

	items[brwMenuAddress] = brwMenuItem{Label: "Go to address", Icon: "edit"}
	items[brwMenuSearch] = brwMenuItem{Label: "Search the web", Icon: "globe", Value: brwEngineFor(config).Name, Key: "Y / /"}
	links := 0
	if haveElem {
		links = len(browserLinksFor(elem.Variable))
	}
	items[brwMenuLinks] = brwMenuItem{Label: "Links on this page", Icon: "link", Key: "X", Disabled: links == 0}
	if links > 0 {
		items[brwMenuLinks].Value = strconv.Itoa(links)
	}
	items[brwMenuFind] = brwMenuItem{Label: "Find on this page", Icon: "search", Key: "F", KeyOnly: true,
		Value: browserFindChipLabel(), Adjust: browserFind.active, Disabled: page == ""}
	items[brwMenuZoom] = brwMenuItem{Label: "Zoom", Icon: "text", Value: browserZoomLabel(), Key: "L2/R2 / - / +", Adjust: true}
	mode := brwMenuItem{Label: "Link mode", Icon: "gamepad", Value: "Off", Key: "Tab", KeyOnly: true}
	if browserNav == browserModeLinks {
		mode.Value, mode.Tone = "On", brwToneGood
	}
	items[brwMenuMode] = mode

	// Saving goes by the address bar, the way the Save chip did: after a
	// redirect it holds the URL the page landed on.
	save := brwMenuItem{Label: "Save this page", Icon: "star", Disabled: page == ""}
	if page != "" && config != nil && browserBookmarkHas(textBrowserAddress(config)) {
		save.Icon, save.Value, save.Tone = "favorites", "Saved", brwToneGood
	}
	items[brwMenuSave] = save
	marks := browserBookmarksCount()
	items[brwMenuSaved] = brwMenuItem{Label: "Saved pages", Icon: "list", Disabled: marks == 0}
	if marks > 0 {
		items[brwMenuSaved].Value = strconv.Itoa(marks)
	}
	// The page in a full browser: WPE Browser on the handheld (or getting
	// it), the desktop's own browser on a PC (brw_wpe.go).
	var wpe brwMenuItem
	wpe.Label, wpe.Icon, wpe.Value, wpe.Tone, wpe.Disabled = brwWpeMenuEntry(page != "")
	items[brwMenuWPE] = wpe

	info := brwMenuItem{Label: "Site info", Icon: "info", Key: "I", KeyOnly: true, Disabled: page == ""}
	if page != "" {
		if browserHTTPSAddress(page) {
			info.Icon, info.Value, info.Tone = "lock", "Secure", brwToneGood
		} else {
			info.Icon, info.Value, info.Tone = "unlock", "Not secure", brwToneWarn
		}
	}
	items[brwMenuInfo] = info
	filter := brwMenuItem{Label: "Ad & tracker filter", Icon: "shield", Value: "On", Disabled: page == ""}
	if haveElem {
		view := browserAdBlockViewFor(elem.Variable)
		switch {
		case !browserBlockAdsEnabled():
			// Settings → Browser → Block ads and trackers (brw_adblock.go).
			filter.Value, filter.Tone = "Off in Settings", brwToneWarn
		case view.Off:
			filter.Value, filter.Tone = "Off for this site", brwToneWarn
		case view.Blocked > 0:
			filter.Value, filter.Tone = fmt.Sprintf("%d blocked", view.Blocked), brwToneGood
		}
	}
	items[brwMenuFilter] = filter

	sites := len(browserCookieSites())
	signIns := brwMenuItem{Label: "Signed-in sites", Icon: "key", Value: "None", Disabled: sites == 0}
	switch {
	case sites == 1:
		signIns.Value = "1 site"
	case sites > 1:
		signIns.Value = strconv.Itoa(sites) + " sites"
	}
	items[brwMenuSignIns] = signIns
	stored := browserCookiesCount()
	forget := brwMenuItem{Label: "Forget sign-ins", Icon: "trash", Disabled: stored == 0}
	if stored > 0 {
		forget.Value = browserCookieCountLabel(stored)
		// The first press arms it (browserForgetCookiesAction); the row says
		// what the second one does while the window is open.
		if armed := browserForgetConfirm.armedAt; !armed.IsZero() && time.Since(armed) <= browserForgetConfirmWindow {
			forget.Value, forget.Tone = "Press "+hintKeyFor("A", "Forget")+" again", brwToneBad
		}
	}
	items[brwMenuDropSignIns] = forget

	items[brwMenuLayout] = brwMenuItem{Label: "Full-screen pages", Icon: "fullscreen", Value: setOnOff(brwFullScreenOn),
		Disabled: !brwSceneIsOurs(config)}
	items[brwMenuSettings] = brwMenuItem{Label: "Browser settings", Icon: "settings"}
	items[brwMenuIssues] = brwMenuItem{Label: "JukaHub issues", Icon: "bug", Value: "GitHub"}
	items[brwMenuClose] = brwMenuItem{Label: "Close browser", Icon: "exit", Key: "B / Esc"}
	for i := range items {
		items[i].ID = brwMenuID(i)
	}
	return items
}

// brwMenuDisabledNote is what A on a dimmed row says.
func brwMenuDisabledNote(id brwMenuID) string {
	switch id {
	case brwMenuBack:
		return "No previous page"
	case brwMenuForward:
		return "No next page"
	case brwMenuLinks:
		return "No links on this page"
	case brwMenuSaved:
		return "No saved pages yet"
	case brwMenuWPE:
		return brwWpeMenuDisabledNote()
	case brwMenuSignIns, brwMenuDropSignIns:
		return "No sign-ins are saved"
	case brwMenuLayout:
		return "This setup draws its own Browser screen"
	}
	return "Open a page first"
}

// ── Opening and closing ──────────────────────────────────────────────────

// brwMenuCanOpen reports whether Select may open the menu now: the Browser is
// the screen on display, and neither JukaLand on its offline page, a video, a
// keyboard, a scene fade, one of the Browser's own pickers nor anything drawn
// above the scene has the input.
func brwMenuCanOpen(config *Config) bool {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) ||
		config.Scenes[currentSceneIndex].Name != browserSceneName {
		return false
	}
	if _, ok := findBrowserElement(config); !ok {
		return false
	}
	if transitionPhase != "none" || virtualKeyboardActive || activeSceneIndex != -1 || brwGame.active {
		return false
	}
	if videoPrepareActive() || (snapshotVideoState().phase != "idle" && !videoMiniVisible()) {
		return false
	}
	if browserLinkPanel.open || browserCookiePanel.open || browserBookmarkPanel.open || brwInfo.open {
		return false
	}
	return !brwMenuCovered() && !modalPanelBlocksShoulders() && !triggerOwnedByOverlay()
}

// brwMenuCovered reports an overlay that can come up over the open menu — the
// Start menu, Quick Settings, a help card, the keyboard — and takes the input
// while it is up.
func brwMenuCovered() bool {
	return virtualKeyboardActive || tb.startOpen || nc.open || qs.open || globalSearchOpen ||
		shortcutsOpen || controlsLegend.open || clipSearchOpen || imageViewerPath != "" ||
		homeCalendarOpen || weatherPopoverOpen || volumePopoverOpen || wpOpen ||
		onboarding.active || qrZoomActive() || companion.open || updatePrompt.open ||
		launchFailure.open || recoveryCard.open || alarmFiringActive()
}

// brwMenuOpen opens the menu and reports whether it did. The cursor starts on
// Back, or on the address row when there is nothing to go back to.
func brwMenuOpen(config *Config) bool {
	if brwMenu.open || !brwMenuCanOpen(config) {
		return false
	}
	brwMenu = brwMenuState{open: true, rects: brwMenu.rects[:0], minus: brwMenu.minus[:0], plus: brwMenu.plus[:0]}
	if !browserHistoryAvailable(-1) {
		brwMenu.cursor = int(brwMenuAddress)
	}
	// A pointer the right stick left up would take the menu's A as a click
	// wherever it rests (virtualcursor.go). Mouse mode keeps it up on purpose,
	// and the rows answer its clicks.
	if !vcursorModeLive(config) {
		vcursorHide()
	}
	PlayActivateSound()
	return true
}

// brwMenuYield closes the open menu when an overlay has come up over it — a
// control-profile line opened the Start menu or Quick Settings, a cut-short
// toast opened the notification centre — and reports whether it did. Those
// draw under the sheet, so a menu that only stood aside would stay drawn over
// a panel the pad was driving.
func brwMenuYield() bool {
	if !brwMenu.open || !brwMenuCovered() {
		return false
	}
	brwMenuShut(false)
	return true
}

// brwMenuClosedBy is the key whose press last closed the menu, while it is held:
// its auto-repeats belong to that press, and on the page behind (or the screen
// a row opened) they would act again — Esc and B would leave the Browser.
var brwMenuClosedBy sdl.Scancode

// brwMenuShut closes the menu; sound is the back chirp of a menu put away by
// hand, which a row that acts does not want.
func brwMenuShut(sound bool) {
	if !brwMenu.open {
		return
	}
	brwMenu.open = false
	if sound {
		PlayBackSound()
	}
}

// brwMenuForget closes the menu without a sound and forgets the page's status
// stack with it: leaving the Browser takes both, and the next visit names its
// page again.
func brwMenuForget() {
	brwMenu = brwMenuState{}
	brwFS = brwFSState{}
}

// ── Moving ───────────────────────────────────────────────────────────────

// brwMenuMove moves the cursor one step. The top row is a row of four that
// Left/Right walk; under it the list is a column. Up and Down wrap round the
// whole menu, so the last row (Close browser) is one Up from the top.
func brwMenuMove(dx, dy int) {
	last := int(brwMenuCount) - 1
	c := brwMenu.cursor
	switch {
	case dy > 0 && c < brwMenuNavCount:
		brwMenu.navCol = c
		c = brwMenuNavCount
	case dy < 0 && c < brwMenuNavCount:
		brwMenu.navCol = c
		c = last
	case dy < 0 && c == brwMenuNavCount:
		c = brwMenu.navCol
		if c < 0 || c >= brwMenuNavCount {
			c = 0
		}
	case dy > 0 && c >= last:
		c = 0
	case dy != 0:
		c += dy
	case dx != 0 && c < brwMenuNavCount:
		c += dx
		if c < 0 {
			c = 0
		}
		if c >= brwMenuNavCount {
			c = brwMenuNavCount - 1
		}
	}
	brwMenuSetCursor(c)
}

// brwMenuJump moves down (delta > 0) or up the list by delta rows, stopping at
// its ends. From the top row a jump down lands in the list.
func brwMenuJump(delta int) {
	last := int(brwMenuCount) - 1
	c := brwMenu.cursor
	if c < brwMenuNavCount {
		if delta < 0 {
			return
		}
		brwMenu.navCol = c
		c = brwMenuNavCount - 1
	}
	c += delta
	if c < brwMenuNavCount {
		c = brwMenuNavCount
	}
	if c > last {
		c = last
	}
	brwMenuSetCursor(c)
}

// brwMenuSetCursor puts the cursor on entry c, with the move's tick when it
// moved.
func brwMenuSetCursor(c int) {
	if c < 0 || c >= int(brwMenuCount) || c == brwMenu.cursor {
		return
	}
	brwMenu.cursor = c
	PlayNavSound()
}

// brwMenuAdjust is Left/Right on a row that changes in place: Zoom steps the
// text size, Find steps through the matches. It reports false for every other
// row, whose Left/Right do nothing.
func brwMenuAdjust(config *Config, dir int) bool {
	id := brwMenuID(brwMenu.cursor)
	if id != brwMenuZoom && (id != brwMenuFind || !browserFind.active) {
		return false
	}
	elem, ok := findBrowserElement(config)
	if !ok {
		return false
	}
	if id == brwMenuZoom {
		browserZoomStep(config, elem, dir)
	} else {
		browserFindStep(config, elem, dir)
	}
	return true
}

// ── Acting ───────────────────────────────────────────────────────────────

// brwMenuActivate runs entry i. A dimmed entry says why it cannot act and the
// menu stays; Zoom's A puts the text back to the size the site drew it and
// Forget's first press arms it, both in place; everything else closes the menu
// first, so a keyboard or a picker it opens is the only thing on screen.
func brwMenuActivate(renderer *sdl.Renderer, config *Config, i int) {
	if i < 0 || i >= int(brwMenuCount) {
		return
	}
	it := brwMenuItems(config)[i]
	if it.Disabled {
		PlayNavSound()
		ShowToast(brwMenuDisabledNote(it.ID), ToastKindInfo)
		return
	}
	elem, ok := findBrowserElement(config)
	if !ok {
		brwMenuShut(false)
		return
	}
	switch it.ID {
	case brwMenuZoom:
		if browserZoomIndex == browserZoomDefault {
			PlayNavSound()
			ShowToast("Zoom "+browserZoomLabel(), ToastKindInfo)
			return
		}
		browserZoomIndex = browserZoomDefault
		browserZoomApplyChange(config, elem)
		return
	case brwMenuDropSignIns:
		if browserForgetCookiesAction(config) {
			brwMenuShut(false)
		}
		return
	}
	brwMenuShut(false)
	switch it.ID {
	case brwMenuBack:
		PlayActivateSound()
		browserBack(config)
	case brwMenuForward:
		PlayActivateSound()
		browserForward(config)
	case brwMenuReload:
		PlayActivateSound()
		browserReload(config)
	case brwMenuHome:
		PlayActivateSound()
		browserHome(config)
	case brwMenuAddress:
		PlayActivateSound()
		handleTextBrowserAddressInput(renderer, config, elem)
	case brwMenuSearch:
		PlayActivateSound()
		openBrowserSearch(renderer, config, elem)
	case brwMenuLinks:
		openBrowserLinkPanel(config, elem)
	case brwMenuFind:
		PlayActivateSound()
		openBrowserFind(renderer, config, elem)
	case brwMenuMode:
		browserToggleNavMode()
	case brwMenuSave:
		browserBookmarkSaveAction(config)
	case brwMenuSaved:
		openBrowserBookmarkPanel(config)
	case brwMenuWPE:
		brwWpeMenuActivate(config)
	case brwMenuInfo:
		brwInfoOpenFor(config)
	case brwMenuFilter:
		browserToggleAdFilter(config)
	case brwMenuSignIns:
		openBrowserCookiePanel(config)
	case brwMenuLayout:
		PlayActivateSound()
		on := !brwFullScreenOn
		brwSetFullScreen(config, on)
		ShowToast("Full-screen pages: "+setOnOff(on), ToastKindInfo)
	case brwMenuSettings:
		PlayActivateSound()
		brwOpenSettings(config)
	case brwMenuIssues:
		PlayActivateSound()
		browserOpenURL(config, browserIssuesURL)
	case brwMenuClose:
		goBackScene(config)
	}
}

// ── Input ────────────────────────────────────────────────────────────────

// brwMenuController takes a pad press for the menu. Closed, Select opens it
// from anywhere on the Browser screen (the page, or a chip of the classic
// row); open, it owns the pad. The main loop calls it after the overlay
// router, where the renderer the keyboards need is at hand.
func brwMenuController(renderer *sdl.Renderer, e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	if !brwMenu.open {
		return e.Button == sdl.CONTROLLER_BUTTON_BACK && brwMenuOpen(config)
	}
	if brwMenuYield() {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		brwMenuMove(0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		brwMenuMove(0, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		if !brwMenuAdjust(config, -1) {
			brwMenuMove(-1, 0)
		}
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if !brwMenuAdjust(config, 1) {
			brwMenuMove(1, 0)
		}
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		brwMenuJump(-brwMenuPage)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		brwMenuJump(brwMenuPage)
	case sdl.CONTROLLER_BUTTON_A:
		brwMenuActivate(renderer, config, brwMenu.cursor)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		brwMenuShut(true)
	}
	return true
}

// brwMenuKey takes a key for the menu: closed, M or the Menu key opens it
// (Select's keyboard twin); open, it owns the keyboard — the arrows, Tab,
// PgUp/PgDn and Home/End move, Enter/Space choose, Esc/Backspace/M close. A and
// B count too, for the firmware that sends the face buttons as keys.
func brwMenuKey(renderer *sdl.Renderer, e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	sym := e.Keysym.Sym
	menuKey := sym == sdl.K_m || sym == sdl.K_APPLICATION || sym == sdl.K_MENU
	if !brwMenu.open {
		if brwMenuClosedBy != 0 {
			if e.Repeat != 0 && e.Keysym.Scancode == brwMenuClosedBy {
				return true
			}
			brwMenuClosedBy = 0
		}
		if !menuKey || e.Repeat != 0 || e.Keysym.Mod&uint16(sdl.KMOD_CTRL|sdl.KMOD_ALT|sdl.KMOD_GUI) != 0 {
			return false
		}
		return brwMenuOpen(config)
	}
	if brwMenuYield() {
		return false
	}
	// The press that closes the menu, by choosing a row or by putting it away,
	// keeps its repeats from the page behind (brwMenuClosedBy).
	defer func() {
		if !brwMenu.open && e.Repeat == 0 {
			brwMenuClosedBy = e.Keysym.Scancode
		}
	}()
	switch {
	case sym == sdl.K_UP:
		brwMenuMove(0, -1)
	case sym == sdl.K_DOWN:
		brwMenuMove(0, 1)
	case sym == sdl.K_TAB:
		if e.Keysym.Mod&uint16(sdl.KMOD_SHIFT) != 0 {
			brwMenuMove(0, -1)
		} else {
			brwMenuMove(0, 1)
		}
	case sym == sdl.K_LEFT:
		if !brwMenuAdjust(config, -1) {
			brwMenuMove(-1, 0)
		}
	case sym == sdl.K_RIGHT:
		if !brwMenuAdjust(config, 1) {
			brwMenuMove(1, 0)
		}
	case sym == sdl.K_PAGEUP:
		brwMenuJump(-brwMenuPage)
	case sym == sdl.K_PAGEDOWN:
		brwMenuJump(brwMenuPage)
	case sym == sdl.K_HOME:
		brwMenuSetCursor(0)
	case sym == sdl.K_END:
		brwMenuSetCursor(int(brwMenuCount) - 1)
	case sym == sdl.K_RETURN || sym == sdl.K_KP_ENTER || sym == sdl.K_SPACE || sym == sdl.K_a:
		// A held key chooses once: its repeats would act on the next screen.
		if e.Repeat == 0 {
			brwMenuKeyOpensField(e)
			brwMenuActivate(renderer, config, brwMenu.cursor)
		}
	case menuKey || sym == sdl.K_ESCAPE || sym == sdl.K_BACKSPACE || sym == sdl.K_b:
		if e.Repeat == 0 {
			brwMenuShut(true)
		}
	}
	return true
}

// brwMenuKeyOpensField readies the key that chooses a row for the keyboard the
// row may open (Go to address, Search, Find). Space and A type their own
// character, which the keyboard would put into the field it opens — over the
// selected address, so the URL became " " — and the character is dropped
// (browserDropKeyText), whatever the row. Enter types nothing, but its repeats
// would submit the field the moment it opened, so for those rows they are
// watched the way browserDropKeyText watches the others'.
func brwMenuKeyOpensField(e *sdl.KeyboardEvent) {
	if e.Keysym.Sym != sdl.K_RETURN && e.Keysym.Sym != sdl.K_KP_ENTER {
		browserDropKeyText(e)
		return
	}
	switch brwMenuID(brwMenu.cursor) {
	case brwMenuAddress, brwMenuSearch, brwMenuFind:
		if e.Keysym.Scancode != 0 {
			browserTextOpener = e.Keysym.Scancode
		}
	}
}

// brwMenuMouse takes a click while the menu is open: a − or + steps its row, a
// row acts, a click beside the sheet closes it, anything else on the sheet is
// ignored.
func brwMenuMouse(renderer *sdl.Renderer, mx, my int32, config *Config) {
	if !brwMenu.open || brwMenuYield() {
		return
	}
	for i, r := range brwMenu.minus {
		if r.W > 0 && pointInRect(mx, my, r) {
			brwMenuSetCursor(i)
			brwMenuAdjust(config, -1)
			return
		}
	}
	for i, r := range brwMenu.plus {
		if r.W > 0 && pointInRect(mx, my, r) {
			brwMenuSetCursor(i)
			brwMenuAdjust(config, 1)
			return
		}
	}
	for i, r := range brwMenu.rects {
		if r.W > 0 && r.H > 0 && pointInRect(mx, my, r) {
			brwMenuSetCursor(i)
			brwMenuActivate(renderer, config, i)
			return
		}
	}
	if brwMenu.card.W > 0 && !pointInRect(mx, my, brwMenu.card) {
		brwMenuShut(true)
	}
}

// brwMenuWheel walks the list three rows a notch while the menu is open.
func brwMenuWheel(dy int32) bool {
	if !brwMenu.open || brwMenuYield() {
		return false
	}
	switch {
	case dy > 0:
		brwMenuJump(-3)
	case dy < 0:
		brwMenuJump(3)
	}
	return true
}

// brwMenuRightClick is the mouse's Select: a right click opens the menu over
// the Browser and closes it again. It reports whether it did either.
func brwMenuRightClick(config *Config) bool {
	if brwMenu.open {
		if brwMenuYield() {
			return false
		}
		brwMenuShut(true)
		return true
	}
	return brwMenuOpen(config)
}

// ── Drawing ──────────────────────────────────────────────────────────────

// brwMenuCardRect is the menu's sheet: the right-hand side of the screen, full
// height and about a third of its width, so the page stays in view beside it.
func brwMenuCardRect() sdl.Rect {
	sw, sh := screenWidth, screenHeight
	if sw <= 0 || sh <= 0 {
		sw, sh = 1280, 720
	}
	w := sw * 36 / 100
	if w < 380 {
		w = 380
	}
	if w > 470 {
		w = 470
	}
	if w > sw-24 {
		w = sw - 24
	}
	return sdl.Rect{X: sw - w - 12, Y: 12, W: w, H: sh - 24}
}

// brwMenuHeading is the sheet's header: the page's title (or its host) over
// its address, with a padlock for the connection it came over.
func brwMenuHeading(config *Config) (title, subtitle, glyph string) {
	title, glyph = "Browser", "browser"
	if config == nil {
		return title, "", glyph
	}
	address := textBrowserAddress(config)
	if elem, ok := findBrowserElement(config); ok {
		if doc := browserDocumentFor(elem.Variable); doc != nil && strings.TrimSpace(doc.URL) != "" {
			address = strings.TrimSpace(doc.URL)
			title = strings.TrimSpace(doc.Title)
			if title == "" {
				title = brwHostOf(address)
			}
			glyph = "unlock"
			if browserHTTPSAddress(address) {
				glyph = "lock"
			}
		}
	}
	return title, address, glyph
}

// brwMenuStatus is the clock and the battery, which the full-screen page no
// longer shows in a top bar.
func brwMenuStatus(config *Config) string {
	status := clockDigits(config, time.Now())
	if bat := statusBatteryPercent(); bat >= 0 {
		status += " · " + strconv.Itoa(bat) + "%"
	}
	return status
}

// brwMenuRender draws the open menu over the Browser.
func brwMenuRender(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || config == nil || !brwMenu.open || brwMenuYield() {
		return
	}
	label := FontForRole(config, TypeBody)
	small := FontForRole(config, TypeCaption)
	if label == nil || small == nil {
		return
	}
	items := brwMenuItems(config)
	n := len(items)
	if brwMenu.cursor < 0 || brwMenu.cursor >= n {
		brwMenu.cursor = 0
	}
	brwMenu.rects = brwMenuRects(brwMenu.rects, n)
	brwMenu.minus = brwMenuRects(brwMenu.minus, n)
	brwMenu.plus = brwMenuRects(brwMenu.plus, n)

	drawOverlayScrim(renderer)
	card := brwMenuCardRect()
	brwMenu.card = card
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	title, subtitle, glyph := brwMenuHeading(config)
	top := UIPageHeaderDraw(renderer, config, overlayPageHeader(card, title, subtitle, glyph)) + 12
	inner := sdl.Rect{X: card.X + OverlayPadX, Y: top, W: card.W - 2*OverlayPadX}

	// The top row: four buttons, an icon over its word.
	sh := int32(small.Height())
	navH := 22 + 4 + sh + 18
	const navGap = int32(8)
	bw := (inner.W - navGap*(brwMenuNavCount-1)) / brwMenuNavCount
	for i := 0; i < brwMenuNavCount && i < n; i++ {
		it := items[i]
		r := sdl.Rect{X: inner.X + int32(i)*(bw+navGap), Y: top, W: bw, H: navH}
		brwMenu.rects[i] = r
		focused := brwMenu.cursor == i
		// A dimmed button still shows the ring while the cursor is on it: the
		// glass draws no ring on a disabled control, and a cursor that vanishes
		// on Back with no history reads as a menu that lost its place.
		drawGlassSurface(renderer, r, glassControlRadius, glassState{
			Focused: focused, Hovered: !focused && pointInRect(mouseX, mouseY, r), Disabled: it.Disabled && !focused, NoLift: true,
		})
		ink := ColorTextPrimary()
		if it.Disabled {
			ink = WithAlpha(ColorTextTertiary(), 160)
		}
		drawHomeGlyphTinted(renderer, it.Icon, sdl.Rect{X: r.X + (r.W-22)/2, Y: r.Y + 9, W: 22, H: 22}, ink)
		word := ellipsize(small, it.Label, r.W-8)
		ww, _ := measureText(small, word)
		drawGlassText(renderer, config, small, word, ink, r.X+(r.W-ww)/2, r.Y+9+22+4)
	}

	// The list, scrolled so the cursor is in view.
	footH := sh + 16
	view := sdl.Rect{X: inner.X, Y: top + navH + 12, W: inner.W}
	view.H = card.Y + card.H - footH - 6 - view.Y
	brwMenu.view = view
	rowH := int32(label.Height()) + 18
	if rowH < 42 {
		rowH = 42
	}
	const rowGap = int32(4)
	rows := n - brwMenuNavCount
	content := int32(rows)*(rowH+rowGap) - rowGap
	maxScroll := content - view.H
	if maxScroll < 0 {
		maxScroll = 0
	}
	if brwMenu.cursor < brwMenuNavCount {
		brwMenu.scroll = 0
	} else {
		cy := int32(brwMenu.cursor-brwMenuNavCount) * (rowH + rowGap)
		if cy < brwMenu.scroll {
			brwMenu.scroll = cy
		}
		if cy+rowH > brwMenu.scroll+view.H {
			brwMenu.scroll = cy + rowH - view.H
		}
	}
	if brwMenu.scroll > maxScroll {
		brwMenu.scroll = maxScroll
	}
	if brwMenu.scroll < 0 {
		brwMenu.scroll = 0
	}
	if view.H > 0 {
		clip := view
		_ = renderer.SetClipRect(&clip)
		for i := brwMenuNavCount; i < n; i++ {
			r := sdl.Rect{X: view.X, Y: view.Y + int32(i-brwMenuNavCount)*(rowH+rowGap) - brwMenu.scroll, W: view.W, H: rowH}
			if r.Y+r.H <= view.Y || r.Y >= view.Y+view.H {
				continue
			}
			// Only the part of a row inside the viewport takes a click.
			brwMenu.rects[i] = glassIntersect(r, view)
			hovered := i != brwMenu.cursor && pointInRect(mouseX, mouseY, brwMenu.rects[i])
			brwMenuDrawRow(renderer, config, label, small, items[i], r, view, i, i == brwMenu.cursor, hovered)
		}
		_ = renderer.SetClipRect(nil)
		if content > view.H && maxScroll > 0 {
			drawScrollbar(renderer, view.X+view.W+6, view.Y, 3, view.H,
				float64(view.H)/float64(content), float64(brwMenu.scroll)/float64(maxScroll))
		}
	}

	// The footer: what the keys do here, and the clock and battery the bar
	// that is not drawn used to carry.
	hints := []FooterHint{{Button: "D-Pad", Label: "Move"}, {Button: "A", Label: "Choose"}, {Button: "B", Label: "Close"}}
	if cur := items[brwMenu.cursor]; cur.Adjust {
		reset := "Reset"
		if cur.ID == brwMenuFind {
			reset = "New search"
		}
		hints = []FooterHint{{Button: "Left/Right", Label: "Change"}, {Button: "A", Label: reset}, {Button: "B", Label: "Close"}}
	}
	footY := card.Y + card.H - footH + (footH-sh)/2 - 4
	status := brwMenuStatus(config)
	statusW, _ := measureText(small, status)
	drawGlassText(renderer, config, small, status, ColorTextTertiary(), inner.X+inner.W-statusW, footY)
	line := ellipsize(small, hintLine(hints...), inner.W-statusW-16)
	drawGlassText(renderer, config, small, line, ColorTextSecondary(), inner.X, footY)
}

// brwMenuRects is rects reused for n entries, every one zeroed: an entry the
// frame does not draw must not keep last frame's target.
func brwMenuRects(rects []sdl.Rect, n int) []sdl.Rect {
	if cap(rects) < n {
		rects = make([]sdl.Rect, n)
	}
	rects = rects[:n]
	for i := range rects {
		rects[i] = sdl.Rect{}
	}
	return rects
}

// brwMenuDrawRow draws one list row: its icon and label, and at the right its
// value — between − and + targets on a row that changes in place — and the
// page's own shortcut for it.
func brwMenuDrawRow(renderer *sdl.Renderer, config *Config, label, small *ttf.Font, it brwMenuItem, r, view sdl.Rect, i int, focused, hovered bool) {
	UIListRow(renderer, r, glassRowRadius, focused, hovered)
	ink := ColorTextPrimary()
	if it.Disabled {
		ink = WithAlpha(ColorTextTertiary(), 170)
	}
	lh, sh := int32(label.Height()), int32(small.Height())
	icon := sdl.Rect{X: r.X + 14, Y: r.Y + (r.H-20)/2, W: 20, H: 20}
	drawHomeGlyphTinted(renderer, it.Icon, icon, ink)
	x := icon.X + icon.W + 12
	right := r.X + r.W - 14

	if key := brwMenuBadge(it); key != "" {
		kw, _ := measureText(small, key)
		bh := sh + 6
		badge := sdl.Rect{X: right - kw - 14, Y: r.Y + (r.H-bh)/2, W: kw + 14, H: bh}
		fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, bh/2, glassChipFill())
		strokeRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, bh/2, 1, glassChipEdge())
		drawGlassText(renderer, config, small, key, glassChipInk(), badge.X+7, badge.Y+3)
		right = badge.X - 10
	}

	valueInk := brwToneColor(it.Tone)
	if it.Disabled {
		valueInk = WithAlpha(ColorTextTertiary(), 170)
	}
	if it.Adjust {
		// − value +, the two ends tappable. Find walks its matches with the
		// arrows a pager uses.
		less, more := "−", "+"
		if it.ID == brwMenuFind {
			less, more = "‹", "›"
		}
		side := sh + 8
		plus := sdl.Rect{X: right - side, Y: r.Y + (r.H-side)/2, W: side, H: side}
		value := ellipsize(small, it.Value, (right-x)/2)
		vw, _ := measureText(small, value)
		minus := sdl.Rect{X: plus.X - 8 - vw - 8 - side, Y: plus.Y, W: side, H: side}
		for _, b := range []struct {
			rect sdl.Rect
			text string
		}{{minus, less}, {plus, more}} {
			fillRoundedRect(renderer, b.rect.X, b.rect.Y, b.rect.W, b.rect.H, side/2, glassChipFill())
			strokeRoundedRect(renderer, b.rect.X, b.rect.Y, b.rect.W, b.rect.H, side/2, 1, glassChipEdge())
			tw, _ := measureText(small, b.text)
			drawGlassText(renderer, config, small, b.text, glassChipInk(), b.rect.X+(side-tw)/2, b.rect.Y+(side-sh)/2)
		}
		drawGlassText(renderer, config, small, value, valueInk, minus.X+side+8, r.Y+(r.H-sh)/2)
		brwMenu.minus[i] = glassIntersect(minus, view)
		brwMenu.plus[i] = glassIntersect(plus, view)
		right = minus.X - 10
	} else if it.Value != "" {
		value := ellipsize(small, it.Value, (right-x)*45/100)
		vw, _ := measureText(small, value)
		drawGlassText(renderer, config, small, value, valueInk, right-vw, r.Y+(r.H-sh)/2)
		right -= vw + 12
	}

	if avail := right - x; avail > 0 {
		drawGlassText(renderer, config, label, ellipsize(label, it.Label, avail), ink, x, r.Y+(r.H-lh)/2)
	}
}

// brwMenuBadge is a row's shortcut as this frame's hints name it, or "" when
// the row has none (or has one only a keyboard has, and the hints name pad
// buttons).
func brwMenuBadge(it brwMenuItem) string {
	if it.Key == "" || (it.KeyOnly && !hintsUseKeyboard()) {
		return ""
	}
	return hintKeyFor(it.Key, it.Label)
}
