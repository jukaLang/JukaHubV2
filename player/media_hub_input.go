package main

// media_hub_input.go — the Media screen's controls. Pad, keyboard, mouse,
// wheel and touch all end in the same handful of semantic actions, so every
// action is reachable from each of them (docs/STYLE.md).
//
// Focus lives in three zones — the search bar, the tab strip and the content —
// plus the details sheet and the confirmation, which own every press while
// they are up. B / Escape leaves the screen from anywhere (after closing an
// open sheet, or a list opened in the Library), except when it closes a
// floating video first.

import (
	"github.com/veandco/go-sdl2/sdl"
)

// mediaInput is one semantic press.
type mediaInput int

const (
	mediaInUp mediaInput = iota
	mediaInDown
	mediaInLeft
	mediaInRight
	mediaInConfirm
	mediaInBack
	mediaInSearch
	mediaInDetails
	mediaInPrevTab
	mediaInNextTab
	mediaInMore
	mediaInPageUp
	mediaInRemove
	mediaInUndo
)

// mediaHubRemoveKey reports whether X removes the focused card: on the
// Downloads and Favorites lists. Everywhere else X opens the search.
func mediaHubRemoveKey() bool {
	return (mediaHub.tab == mediaTabDownloads || mediaHub.tab == mediaTabFavorites) &&
		mediaHub.zone == mediaZoneContent && !mediaHub.sheet.open
}

// mediaHubAcceptsInput reports whether the screen's own controls take input:
// Media is on screen, nothing above it owns the input (the keyboard, a focused
// header button, a typing field), and the player is not on screen — the player
// has its own controls and takes the press first.
func mediaHubAcceptsInput(config *Config) bool {
	if !mediaHubIsCurrent(config) || virtualKeyboardActive || activeSceneIndex != -1 {
		return false
	}
	if transitionPhase != "none" {
		return false
	}
	if mediaHubPlayerOnScreen() {
		return false
	}
	if chromeTier(focusEngine.ElementIndex()) != 0 {
		mediaHub.chromeHeld = true
		return false
	}
	if mediaHub.chromeHeld {
		// The header handed the focus back (Down from its buttons): resume on
		// the search bar, the control right under it.
		mediaHub.chromeHeld = false
		mediaHub.zone = mediaZoneSearch
	}
	return true
}

// mediaHubHandleKey maps a key onto the screen. It is called from the main
// loop's keyboard path right after the player's own keys (main.go), and from
// the scene module. Keys it does not use fall through, so the global chords
// (F1, F2, F12, Ctrl+Q …) keep working here.
func mediaHubHandleKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !mediaHubAcceptsInput(config) {
		return false
	}
	// X and / open the search keyboard. Capture this before the switch so
	// vkDropOpenerText can drop their own character (vkopenertext.go).
	wasOpen := virtualKeyboardActive
	defer vkDropOpenerText(e, wasOpen)
	mod := e.Keysym.Mod
	ctrl := mod&sdl.KMOD_CTRL != 0
	shift := mod&sdl.KMOD_SHIFT != 0
	if ctrl && e.Keysym.Sym == sdl.K_f {
		return mediaHubInput(config, mediaInSearch)
	}
	if ctrl && e.Keysym.Sym == sdl.K_z {
		return mediaHubInput(config, mediaInUndo)
	}
	if ctrl || mod&sdl.KMOD_ALT != 0 {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		return mediaHubInput(config, mediaInUp)
	case sdl.K_DOWN:
		return mediaHubInput(config, mediaInDown)
	case sdl.K_LEFT:
		return mediaHubInput(config, mediaInLeft)
	case sdl.K_RIGHT:
		return mediaHubInput(config, mediaInRight)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		return mediaHubInput(config, mediaInConfirm)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		return mediaHubInput(config, mediaInBack)
	case sdl.K_x:
		if mediaHubRemoveKey() {
			return mediaHubInput(config, mediaInRemove)
		}
		return mediaHubInput(config, mediaInSearch)
	case sdl.K_u:
		return mediaHubInput(config, mediaInUndo)
	case sdl.K_SLASH:
		return mediaHubInput(config, mediaInSearch)
	case sdl.K_y, sdl.K_i, sdl.K_APPLICATION:
		return mediaHubInput(config, mediaInDetails)
	case sdl.K_DELETE:
		return mediaHubInput(config, mediaInRemove)
	case sdl.K_TAB:
		if shift {
			return mediaHubInput(config, mediaInPrevTab)
		}
		return mediaHubInput(config, mediaInNextTab)
	case sdl.K_LEFTBRACKET:
		return mediaHubInput(config, mediaInPrevTab)
	case sdl.K_RIGHTBRACKET:
		return mediaHubInput(config, mediaInNextTab)
	case sdl.K_PAGEDOWN:
		return mediaHubInput(config, mediaInMore)
	case sdl.K_PAGEUP:
		return mediaHubInput(config, mediaInPageUp)
	}
	return false
}

// mediaHubHandleController maps a pad button. The scene registry calls it
// before the player's handler (screenPadPress), so it declines every press
// while the player is on screen.
func mediaHubHandleController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || !mediaHubAcceptsInput(config) {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return mediaHubInput(config, mediaInUp)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return mediaHubInput(config, mediaInDown)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return mediaHubInput(config, mediaInLeft)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return mediaHubInput(config, mediaInRight)
	case sdl.CONTROLLER_BUTTON_A:
		return mediaHubInput(config, mediaInConfirm)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_GUIDE:
		return mediaHubInput(config, mediaInBack)
	case sdl.CONTROLLER_BUTTON_X:
		if mediaHubRemoveKey() {
			return mediaHubInput(config, mediaInRemove)
		}
		return mediaHubInput(config, mediaInSearch)
	case sdl.CONTROLLER_BUTTON_Y:
		return mediaHubInput(config, mediaInDetails)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		return mediaHubInput(config, mediaInPrevTab)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		return mediaHubInput(config, mediaInNextTab)
	}
	return false
}

// mediaHubShoulder takes the triggers (they arrive as axis motion through
// shoulders.go): R2 loads the next page, L2 jumps back up a screen.
func mediaHubShoulder(s padShoulder, config *Config) bool {
	if !mediaHubAcceptsInput(config) {
		return false
	}
	switch s {
	case padL1:
		return mediaHubInput(config, mediaInPrevTab)
	case padR1:
		return mediaHubInput(config, mediaInNextTab)
	case padL2:
		return mediaHubInput(config, mediaInPageUp)
	case padR2:
		return mediaHubInput(config, mediaInMore)
	}
	return false
}

// mediaHubInput runs one semantic press. It always reports true on the Media
// screen: a press that does nothing still belongs to this screen and must not
// fall through to the hidden config elements behind it.
func mediaHubInput(config *Config, in mediaInput) bool {
	if mediaHub.confirm.open {
		mediaHubConfirmInput(in)
		return true
	}
	if mediaHub.sheet.open {
		mediaHubSheetInput(config, in)
		return true
	}
	switch in {
	case mediaInUp:
		mediaHubNavigate(config, 0, -1)
	case mediaInDown:
		mediaHubNavigate(config, 0, 1)
	case mediaInLeft:
		mediaHubNavigate(config, -1, 0)
	case mediaInRight:
		mediaHubNavigate(config, 1, 0)
	case mediaInConfirm:
		mediaHubActivate(config)
	case mediaInBack:
		// An open Library list closes first, one level at a time.
		if mediaHub.tab == mediaTabLibrary && mlibDrillBack(config) {
			break
		}
		goBackScene(config)
	case mediaInSearch:
		mediaHubOpenSearch(config)
	case mediaInDetails:
		// While a removal can be taken back, Y is Undo (the footer says so).
		if mediaHub.tab == mediaTabFavorites && mediaFavUndoActive() {
			mediaHubUndoFavorite()
			break
		}
		if card, ok := mediaHubFocusedCard(); ok {
			mediaHubOpenSheet(card)
		}
	case mediaInUndo:
		mediaHubUndoFavorite()
	case mediaInPrevTab:
		mediaHubStepTab(config, -1)
	case mediaInNextTab:
		mediaHubStepTab(config, 1)
	case mediaInMore:
		mediaHubPage(config, 1)
	case mediaInPageUp:
		mediaHubPage(config, -1)
	case mediaInRemove:
		card, ok := mediaHubFocusedCard()
		if !ok {
			break
		}
		if mediaHub.tab == mediaTabFavorites {
			mediaHubRemoveFavorite(card)
			break
		}
		if card.libID != "" || card.queued || card.downloaded {
			mediaHubAskRemove(card)
		}
	}
	return true
}

// mediaHubNavigate moves the focus one step.
func mediaHubNavigate(config *Config, dx, dy int) {
	switch mediaHub.zone {
	case mediaZoneSearch:
		if dy < 0 {
			if focusChromeBar() {
				mediaHub.chromeHeld = true
			}
			return
		}
		if dy > 0 {
			mediaHub.zone = mediaZoneTabs
			mediaHub.tabFocus = mediaHubTabIndex(mediaHub.tab)
			PlayNavSound()
		}
	case mediaZoneTabs:
		if dy < 0 {
			mediaHub.zone = mediaZoneSearch
			PlayNavSound()
			return
		}
		if dy > 0 {
			if mediaHubContentFocusable(config) {
				mediaHubEnterContent()
				PlayNavSound()
			}
			return
		}
		tabs := mediaHubTabs()
		next := mediaHub.tabFocus + dx
		if next < 0 || next >= len(tabs) {
			return
		}
		mediaHub.tabFocus = next
		if !mediaTabShortcut(tabs[next]) {
			mediaHubSelectTab(config, tabs[next], false)
		}
		PlayNavSound()
	case mediaZoneContent:
		switch {
		case mediaTabHasRows(mediaHub.tab):
			mediaHubNavigateRows(config, dx, dy)
		case mediaHub.tab == mediaTabDownloads:
			mediaHubNavigateDownloads(dx, dy)
		default:
			mediaHubNavigateGrid(config, dx, dy)
		}
	}
}

// mediaHubContentFocusable reports whether the content has something to land
// on: a card, a chip, or the button of an empty state ("Open Downloads",
// "Try again"), which the pad has to be able to reach too.
func mediaHubContentFocusable(config *Config) bool {
	switch {
	case mediaTabHasRows(mediaHub.tab):
		for i := range mediaHub.rows {
			if mediaRowLen(mediaHub.rows[i]) > 0 {
				return true
			}
		}
	case mediaHub.tab == mediaTabDownloads:
		return len(mediaHub.cards) > 0 || mediaHub.chipN > 0
	default:
		if len(mediaHub.cards) > 0 {
			return true
		}
	}
	return mediaHubStateAction(config, IsOnline()) >= 0
}

// mediaHubEnterContent moves the focus into the content, onto a For you row
// that has something to focus.
func mediaHubEnterContent() {
	mediaHub.zone = mediaZoneContent
	mediaHubSnapRow()
}

// mediaHubStateAction is the button the content's empty state shows: 0 opens
// Downloads (offline), 1 loads the list again, 2 scans the Library again, -1
// when there is none (3, the Library list's Back chip, is pointer only). It
// follows mediaHubDrawRows, mediaHubDrawGridEmpty and mediaHubDrawLibrary, so
// A does what the focused button says.
func mediaHubStateAction(config *Config, online bool) int {
	switch mediaHub.tab {
	case mediaTabForYou:
		if !online && !mediaHubRowsHaveContent(online) {
			return 0
		}
	case mediaTabLibrary:
		if len(mlibNav.stack) > 0 {
			return -1
		}
		if m := mlibCurrentModel(); m != nil && len(m.items) > 0 {
			return -1
		}
		if scanning, _, loaded := mlibStatus(); !scanning && loaded {
			return 2
		}
	case mediaTabSearch:
		if len(mediaHub.cards) > 0 || mediaHub.localSearch || mediaHub.searchPending {
			return -1
		}
		if config != nil {
			if errVal, ok := config.Variables.Custom["search_error"]; ok && errVal != nil {
				return -1
			}
		}
		if !online {
			return 0
		}
	case mediaTabTrending, mediaTabShorts:
		if len(mediaHub.cards) > 0 {
			return -1
		}
		if !online {
			return 0
		}
		if mediaSearchIdle(mediaHubTabVar(mediaHub.tab)) && (mediaHub.tab != mediaTabShorts || mediaSearchIdle(mediaShortsVar)) {
			return 1
		}
	}
	return -1
}

// mediaHubRunStateAction runs an empty state's button.
func mediaHubRunStateAction(config *Config, action int) {
	switch action {
	case 0:
		mediaHubSelectTab(config, mediaTabDownloads, true)
		mediaHub.chipFocus = -1
		mediaHub.zone = mediaZoneContent
	case 1:
		PlayActivateSound()
		if mediaHub.tab == mediaTabShorts {
			mediaHubLoadShorts(config)
			return
		}
		mediaHubLoadTrending(config, true)
	case 2:
		PlayActivateSound()
		mlibStartScan(config, true)
	case 3:
		mlibDrillBack(config)
	}
}

// mediaRowLen is how many focus stops a row has.
func mediaRowLen(row mediaRow) int {
	if row.kind == mediaRowRecentSearch {
		return len(row.chips)
	}
	return len(row.cards)
}

// mediaHubNavigateRows walks For you: Left/Right inside a row, Up/Down between
// rows keeping the column the ring was in.
func mediaHubNavigateRows(config *Config, dx, dy int) {
	if len(mediaHub.rows) == 0 {
		if dy < 0 {
			mediaHub.zone = mediaZoneTabs
		}
		return
	}
	row := &mediaHub.rows[mediaHub.row]
	if dx != 0 {
		next := row.focus + dx
		if next < 0 || next >= mediaRowLen(*row) {
			return
		}
		row.focus = next
		PlayNavSound()
		mediaHubWarmFocused(config)
		return
	}
	target := mediaHub.row + dy
	for target >= 0 && target < len(mediaHub.rows) && mediaRowLen(mediaHub.rows[target]) == 0 {
		target += dy
	}
	if target < 0 {
		mediaHub.zone = mediaZoneTabs
		mediaHub.tabFocus = mediaHubTabIndex(mediaHub.tab)
		PlayNavSound()
		return
	}
	if target >= len(mediaHub.rows) {
		return
	}
	// The column on screen, not the index: rows scroll independently.
	col := row.focus - mediaRowFirstVisible(*row)
	mediaHub.row = target
	next := &mediaHub.rows[target]
	focus := mediaRowFirstVisible(*next) + col
	if n := mediaRowLen(*next); focus >= n {
		focus = n - 1
	}
	if focus < 0 {
		focus = 0
	}
	next.focus = focus
	PlayNavSound()
	mediaHubWarmFocused(config)
}

// mediaRowFirstVisible is the first card a row's scroll shows.
func mediaRowFirstVisible(row mediaRow) int {
	pitch := mediaRowPitch(row)
	if pitch <= 0 {
		return 0
	}
	first := int((row.scroll + float64(pitch)/2) / float64(pitch))
	if first < 0 {
		first = 0
	}
	return first
}

// mediaHubNavigateGrid walks a grid of cards.
func mediaHubNavigateGrid(config *Config, dx, dy int) {
	n := len(mediaHub.cards)
	if n == 0 {
		if dy < 0 {
			mediaHub.zone = mediaZoneTabs
		}
		return
	}
	cols := mediaHubGridColumns(mediaHub.tab == mediaTabShorts)
	idx := mediaHub.index
	switch {
	case dx < 0:
		if idx%cols == 0 {
			return
		}
		idx--
	case dx > 0:
		if idx%cols == cols-1 || idx+1 >= n {
			return
		}
		idx++
	case dy < 0:
		if idx < cols {
			mediaHub.zone = mediaZoneTabs
			mediaHub.tabFocus = mediaHubTabIndex(mediaHub.tab)
			PlayNavSound()
			return
		}
		idx -= cols
	case dy > 0:
		if idx+cols >= n {
			// The last row: the next page comes in by itself, and the ring
			// lands on the last card of a short final row.
			mediaHubLoadMore(config, true)
			if last := n - 1; idx/cols < last/cols {
				idx = last
			} else {
				return
			}
		} else {
			idx += cols
		}
	}
	if idx == mediaHub.index {
		return
	}
	mediaHub.index = idx
	PlayNavSound()
	mediaHubWarmFocused(config)
	// Close to the end of what is loaded: fetch the next page in advance.
	if n-idx <= cols {
		mediaHubLoadMore(config, true)
	}
}

// mediaHubNavigateDownloads walks the Downloads list and its header chips.
func mediaHubNavigateDownloads(dx, dy int) {
	if mediaHub.chipFocus >= 0 {
		switch {
		case dx != 0:
			next := mediaHub.chipFocus + dx
			if next >= 0 && next < mediaHub.chipN {
				mediaHub.chipFocus = next
				PlayNavSound()
			}
		case dy < 0:
			mediaHub.chipFocus = -1
			mediaHub.zone = mediaZoneTabs
			mediaHub.tabFocus = mediaHubTabIndex(mediaHub.tab)
			PlayNavSound()
		case dy > 0:
			if len(mediaHub.cards) > 0 {
				mediaHub.chipFocus = -1
				mediaHub.index = 0
				PlayNavSound()
			}
		}
		return
	}
	if dx != 0 {
		return
	}
	next := mediaHub.index + dy
	if next < 0 {
		if mediaHub.chipN > 0 {
			mediaHub.chipFocus = 0
		} else {
			mediaHub.zone = mediaZoneTabs
			mediaHub.tabFocus = mediaHubTabIndex(mediaHub.tab)
		}
		PlayNavSound()
		return
	}
	if next >= len(mediaHub.cards) {
		return
	}
	mediaHub.index = next
	PlayNavSound()
}

// mediaHubPage moves a screen at a time: forward fetches the next page of a
// paged list when the ring is already on its last row.
func mediaHubPage(config *Config, delta int) {
	if mediaHub.zone != mediaZoneContent {
		mediaHubEnterContent()
	}
	if mediaTabHasRows(mediaHub.tab) {
		mediaHubNavigateRows(config, 0, delta)
		return
	}
	switch mediaHub.tab {
	case mediaTabDownloads:
		step := 4 * delta
		next := mediaHub.index + step
		if next < 0 {
			next = 0
		}
		if next >= len(mediaHub.cards) {
			next = len(mediaHub.cards) - 1
		}
		if next >= 0 && next != mediaHub.index {
			mediaHub.index = next
			PlayNavSound()
		}
		return
	}
	n := len(mediaHub.cards)
	if n == 0 {
		return
	}
	cols := mediaHubGridColumns(mediaHub.tab == mediaTabShorts)
	step := cols * 2 * delta
	next := mediaHub.index + step
	if next >= n {
		if mediaHubLoadMore(config, false) {
			next = n - 1
		} else {
			next = n - 1
		}
	}
	if next < 0 {
		next = 0
	}
	if next != mediaHub.index {
		mediaHub.index = next
		PlayNavSound()
		mediaHubWarmFocused(config)
	}
}

// mediaHubActivate is A / Enter / a click on the focused control.
func mediaHubActivate(config *Config) {
	switch mediaHub.zone {
	case mediaZoneSearch:
		mediaHubOpenSearch(config)
	case mediaZoneTabs:
		tabs := mediaHubTabs()
		if mediaHub.tabFocus < 0 || mediaHub.tabFocus >= len(tabs) {
			return
		}
		t := tabs[mediaHub.tabFocus]
		if mediaTabShortcut(t) {
			mediaHubOpenShortcut(config, t)
			return
		}
		mediaHubSelectTab(config, t, true)
		// A on a list that did not load asks for it again: the pad's way to
		// the empty state's "Try again".
		if mediaHubStateAction(config, IsOnline()) == 1 {
			mediaHubRunStateAction(config, 1)
			return
		}
		if mediaHubContentFocusable(config) {
			mediaHubEnterContent()
		}
	case mediaZoneContent:
		if mediaHub.tab == mediaTabDownloads && mediaHub.chipFocus >= 0 {
			mediaHubChipAction(config, mediaHub.chipFocus)
			return
		}
		if mediaTabHasRows(mediaHub.tab) && mediaHub.row >= 0 && mediaHub.row < len(mediaHub.rows) {
			row := mediaHub.rows[mediaHub.row]
			if row.kind == mediaRowRecentSearch {
				if row.focus >= 0 && row.focus < len(row.chips) {
					PlayActivateSound()
					mediaHubRunSearch(config, row.chips[row.focus])
				}
				return
			}
		}
		card, ok := mediaHubFocusedCard()
		if !ok {
			// No card under the ring: the empty state's button is.
			if action := mediaHubStateAction(config, IsOnline()); action >= 0 {
				mediaHubRunStateAction(config, action)
			}
			return
		}
		if mlibCardOpens(card.mlibKind) {
			mlibOpenGroup(config, card)
			return
		}
		list, idx := mediaHubFocusedList()
		mediaHubPlay(config, card, list, idx, false)
	}
}

// mediaHubChipAction runs one of the Downloads header chips.
func mediaHubChipAction(config *Config, chip int) {
	switch chip {
	case 0:
		on := !dlKeepWatchedEnabled()
		dlSetKeepWatched(config, on)
		PlayActivateSound()
		if on {
			ShowToast("Videos that finish downloading while you watch are kept here", ToastKindSuccess)
		} else {
			ShowToast("Watched videos will not be kept", ToastKindInfo)
		}
	case 1:
		limit := dlCycleKeptLimit(config)
		PlayActivateSound()
		ShowToast("Limit for kept videos: "+dlKeptLimitLabel(limit), ToastKindInfo)
	case 2:
		mediaHubAskRemoveOldest()
	}
	mediaHub.builtAt = 0
}

// mediaHubConfirmInput drives the confirmation: Left/Right choose, A runs the
// highlighted button, B cancels.
func mediaHubConfirmInput(in mediaInput) {
	switch in {
	case mediaInLeft, mediaInUp:
		if mediaHub.confirm.focus != 0 {
			mediaHub.confirm.focus = 0
			PlayNavSound()
		}
	case mediaInRight, mediaInDown:
		if mediaHub.confirm.focus != 1 {
			mediaHub.confirm.focus = 1
			PlayNavSound()
		}
	case mediaInConfirm:
		if mediaHub.confirm.focus == 1 {
			mediaHubConfirmAccept()
			return
		}
		mediaHubConfirmCancel()
	case mediaInBack:
		mediaHubConfirmCancel()
	}
}

// mediaHubSheetInput drives the details sheet.
func mediaHubSheetInput(config *Config, in mediaInput) {
	n := len(mediaHub.sheet.actions)
	switch in {
	case mediaInUp:
		if mediaHub.sheet.focus > 0 {
			mediaHub.sheet.focus--
			PlayNavSound()
		}
	case mediaInDown:
		if mediaHub.sheet.focus < n-1 {
			mediaHub.sheet.focus++
			PlayNavSound()
		}
	case mediaInConfirm:
		if mediaHub.sheet.focus >= 0 && mediaHub.sheet.focus < n {
			mediaHubSheetRun(config, mediaHub.sheet.actions[mediaHub.sheet.focus])
		}
	case mediaInBack, mediaInDetails:
		mediaHub.sheet = mediaSheet{}
		PlayBackSound()
	case mediaInRemove:
		if mediaHub.sheet.card.libID != "" || mediaHub.sheet.card.downloaded || mediaHub.sheet.card.queued {
			mediaHubAskRemove(mediaHub.sheet.card)
		}
	}
}

// ── Pointer and touch ────────────────────────────────────────────────────────

// mediaHubClick takes a left click (or a tap) on the screen. Header clicks were
// handled before it; every click on the body is the screen's, so none reaches
// the hidden config elements.
func mediaHubClick(mx, my int32, config *Config) bool {
	if !mediaHubIsCurrent(config) || virtualKeyboardActive {
		return false
	}
	if mediaHubPlayerOnScreen() {
		// The player took the clicks inside its window already; the backdrop
		// around it does nothing.
		return my > headerBarHeight()
	}
	for i := len(mediaHub.hits) - 1; i >= 0; i-- {
		hit := mediaHub.hits[i]
		if !pointInRect(mx, my, hit.rect) {
			continue
		}
		mediaHubClickHit(config, hit)
		return true
	}
	if mediaHub.confirm.open {
		// A click outside the confirmation cancels it.
		mediaHubConfirmCancel()
		return true
	}
	if mediaHub.sheet.open {
		mediaHub.sheet = mediaSheet{}
		PlayBackSound()
		return true
	}
	return my > headerBarHeight()
}

// mediaHubClickHit runs the control under a click.
func mediaHubClickHit(config *Config, hit mediaHit) {
	switch hit.kind {
	case mediaHitConfirm:
		if hit.a == 1 {
			mediaHubConfirmAccept()
		} else {
			mediaHubConfirmCancel()
		}
	case mediaHitSheetAction:
		if hit.a >= 0 && hit.a < len(mediaHub.sheet.actions) {
			mediaHub.sheet.focus = hit.a
			mediaHubSheetRun(config, mediaHub.sheet.actions[hit.a])
		}
	case mediaHitSheetScrim:
		mediaHub.sheet = mediaSheet{}
		PlayBackSound()
	case mediaHitSearch:
		mediaHub.zone = mediaZoneSearch
		mediaHubOpenSearch(config)
	case mediaHitTab:
		tabs := mediaHubTabs()
		if hit.a < 0 || hit.a >= len(tabs) {
			return
		}
		mediaHub.zone = mediaZoneTabs
		mediaHub.tabFocus = hit.a
		if mediaTabShortcut(tabs[hit.a]) {
			mediaHubOpenShortcut(config, tabs[hit.a])
			return
		}
		mediaHubSelectTab(config, tabs[hit.a], true)
	case mediaHitCard:
		if !mediaHubFocusHit(hit) {
			return
		}
		card, ok := mediaHubFocusedCard()
		if !ok {
			return
		}
		if hit.b < 0 {
			mediaHubOpenSheet(card)
			return
		}
		if mlibCardOpens(card.mlibKind) {
			mlibOpenGroup(config, card)
			return
		}
		list, idx := mediaHubFocusedList()
		mediaHubPlay(config, card, list, idx, false)
	case mediaHitRemove:
		// The bin on a focused Favorites card.
		if !mediaHubFocusHit(mediaHit{kind: mediaHitCard, a: hit.a, b: hit.b}) {
			return
		}
		if card, ok := mediaHubFocusedCard(); ok {
			mediaHubRemoveFavorite(card)
		}
	case mediaHitUndo:
		mediaHubUndoFavorite()
	case mediaHitRecent:
		if hit.a >= 0 && hit.a < len(mediaHub.rows) {
			row := mediaHub.rows[hit.a]
			if hit.b >= 0 && hit.b < len(row.chips) {
				PlayActivateSound()
				mediaHubRunSearch(config, row.chips[hit.b])
			}
		}
	case mediaHitChip:
		mediaHub.zone = mediaZoneContent
		mediaHub.chipFocus = hit.a
		mediaHubChipAction(config, hit.a)
	case mediaHitStateAction:
		// a < 0 is a card that only swallows clicks (the sheet, the
		// confirmation).
		if hit.a >= 0 {
			mediaHubRunStateAction(config, hit.a)
		}
	}
}

// mediaHubFocusHit puts the ring on the card a hit was drawn for. A card hit
// carries its row in a (-1 on a grid) and its index in b; the card's details
// button carries -(index+1) in b, so the two can share one hit kind.
func mediaHubFocusHit(hit mediaHit) bool {
	row, idx := hit.a, hit.b
	if idx < 0 {
		idx = -idx - 1
	}
	mediaHub.zone = mediaZoneContent
	if mediaTabHasRows(mediaHub.tab) {
		if row < 0 || row >= len(mediaHub.rows) || idx >= len(mediaHub.rows[row].cards) {
			return false
		}
		mediaHub.row = row
		mediaHub.rows[row].focus = idx
		return true
	}
	if idx >= len(mediaHub.cards) {
		return false
	}
	mediaHub.chipFocus = -1
	mediaHub.index = idx
	return true
}

// mediaHubHover follows the pointer over the cards the way the D-pad ring does.
// It owns every motion on the Media screen, so the hidden results element can
// never take the focus (or start prefetching) under the pointer.
func mediaHubHover(config *Config, mx, my int32) bool {
	if !mediaHubIsCurrent(config) {
		return false
	}
	if virtualKeyboardActive || mediaHub.sheet.open || mediaHub.confirm.open || mediaHubPlayerOnScreen() {
		return true
	}
	for i := len(mediaHub.hits) - 1; i >= 0; i-- {
		hit := mediaHub.hits[i]
		if hit.kind != mediaHitCard || hit.b < 0 || !pointInRect(mx, my, hit.rect) {
			continue
		}
		if mediaHub.zone == mediaZoneContent {
			rows := mediaTabHasRows(mediaHub.tab)
			if rows && hit.a == mediaHub.row &&
				hit.a >= 0 && hit.a < len(mediaHub.rows) && mediaHub.rows[hit.a].focus == hit.b {
				return true
			}
			if !rows && mediaHub.index == hit.b && mediaHub.chipFocus < 0 {
				return true
			}
		}
		mediaHubFocusHit(hit)
		return true
	}
	return true
}

// mediaHubWheel scrolls the content a row per notch.
func mediaHubWheel(config *Config, dy int32) bool {
	if !mediaHubIsCurrent(config) {
		return false
	}
	if !mediaHubAcceptsInput(config) || mediaHub.sheet.open || mediaHub.confirm.open {
		return true
	}
	if dy == 0 {
		return true
	}
	mediaHubEnterContent()
	if dy > 0 {
		mediaHubNavigate(config, 0, -1)
	} else {
		mediaHubNavigate(config, 0, 1)
	}
	if mediaHub.zone != mediaZoneContent {
		// The wheel never walks into the tab strip.
		mediaHub.zone = mediaZoneContent
	}
	return true
}

// mediaHubSwipe maps touch swipes: sideways changes the tab, vertical scrolls.
// Without it a swipe changed the whole scene (handleSwipe's default).
func mediaHubSwipe(direction string, config *Config) bool {
	if !mediaHubIsCurrent(config) {
		return false
	}
	if !mediaHubAcceptsInput(config) || mediaHub.sheet.open || mediaHub.confirm.open {
		return true
	}
	switch direction {
	case "left":
		mediaHubStepTab(config, 1)
	case "right":
		mediaHubStepTab(config, -1)
	case "up":
		mediaHub.zone = mediaZoneContent
		mediaHubPage(config, 1)
	case "down":
		mediaHub.zone = mediaZoneContent
		mediaHubPage(config, -1)
	}
	return true
}
