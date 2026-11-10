package main

// shoulders.go routes the four shoulder/trigger buttons (L1/R1/L2/R2).
//
// They mean the same thing everywhere, so the pad does not have to be relearned
// per screen:
//
//	L1 / R1  previous / next page of whatever is focused, or a -10/+10 s skip
//	         while a video is playing
//	L2 / R2  bigger jump: previous / next results page (fetching when needed),
//	         previous / next video while playing, first / last row in a list
//
// Analog triggers report a stream of axis values, so they are latched into
// discrete presses here instead of paging on every sample.

type padShoulder int

const (
	padL1 padShoulder = iota
	padR1
	padL2
	padR2
)

func (s padShoulder) String() string {
	switch s {
	case padL1:
		return "L1"
	case padR1:
		return "R1"
	case padL2:
		return "L2"
	case padR2:
		return "R2"
	}
	return "?"
}

const (
	padTriggerDownThreshold = 16000
	padTriggerUpThreshold   = 6000
)

// padTriggerHeld is the latch state per trigger (0 = L2, 1 = R2).
var padTriggerHeld [2]bool

// triggerPressedEdge turns a continuous trigger axis into one press: true on
// the rising edge only, so holding a trigger does not page a dozen times.
func triggerPressedEdge(side int, value int16) bool {
	if side < 0 || side >= len(padTriggerHeld) {
		return false
	}
	if padTriggerHeld[side] {
		if value < padTriggerUpThreshold {
			padTriggerHeld[side] = false
		}
		return false
	}
	if value >= padTriggerDownThreshold {
		padTriggerHeld[side] = true
		return true
	}
	return false
}

// handleShoulderPress runs one shoulder/trigger press. It reports whether the
// press was used by the screen.
func handleShoulderPress(s padShoulder, config *Config) bool {
	if config == nil {
		return false
	}
	// The editor's one destructive key lives on R2. A/X/Y/B are already editing
	// keys there and DELETE exists only on a keyboard the handheld does not
	// have, so a line could be added but never removed. The footer advertises
	// the binding, so it is not a hidden combination; the buffer is unsaved
	// until Y, which is the same safety the keyboard's Delete key has.
	if fileEditor.open && !virtualKeyboardActive {
		// The text page owns all four: R2 deletes a line of an editable file
		// (fileeditor.go), and on a read-only one L2/R2 jump to the top/end.
		fileEditorShoulder(s)
		return true
	}
	if handleVideoShoulder(s, config) {
		return true
	}
	if handleChromeShoulder(s, config) {
		return true
	}
	// The Emulators page gives R2 to the kept-builds list: its own grid is
	// walked with the d-pad, so the trigger is free there, and a page that
	// lists builds to restore should be able to list the rest of them.
	if emuPanel.open && s == padR2 && emuKeptBuildsHint() != "" {
		openCoreBuildsPanel(true)
		return true
	}
	// Panels that draw their own modal list own the pad; a trigger must not
	// page the scene behind them.
	if modalPanelBlocksShoulders() {
		return true
	}
	// The reader has no list element: its triggers zoom and its shoulders turn
	// pages (pdfreader.go).
	if pdfvSceneActive(config) && !virtualKeyboardActive {
		pdfvShoulder(s)
		return true
	}
	if handleListShoulder(s, config) {
		return true
	}
	// On Home the triggers keep their long-standing meaning: previous / next
	// section, which is what the footer advertises there.
	if homeLayoutActive && (s == padL2 || s == padR2) {
		if s == padL2 {
			changeScene(config, -1)
		} else {
			changeScene(config, 1)
		}
		return true
	}
	return false
}

// handleVideoShoulder maps the pad onto playback: shoulders seek, triggers
// change video. Every finished feed and queue stays in the player.
func handleVideoShoulder(s padShoulder, config *Config) bool {
	state := snapshotVideoState()
	if state.phase == "idle" {
		return false
	}
	// The settings sheet is modal over the player.
	if videoSettingsOpen {
		return true
	}
	switch s {
	case padL1:
		seekVideoBySeconds(state, -10)
	case padR1:
		seekVideoBySeconds(state, 10)
	case padL2:
		return skipPlayback(config, -1)
	case padR2:
		return skipPlayback(config, 1)
	}
	revealVideoChrome()
	PlayNavSound()
	return true
}

// handleChromeShoulder pages the overlays that do not have their own shoulder
// handling: the app menu, the notification centre, clipboard history, global
// search and the wallpaper picker.
func handleChromeShoulder(s padShoulder, config *Config) bool {
	switch {
	case tb.startOpen:
		return startMenuShoulder(s, config)
	case nc.open:
		notifShoulder(s)
		return true
	case clipSearchOpen:
		clipShoulder(s)
		return true
	case globalSearchOpen:
		globalSearchShoulder(s)
		return true
	case qs.open:
		qsShoulder(s)
		return true
	case wpOpen:
		wallpaperShoulder(s)
		return true
	case imageViewerPath != "":
		// The viewer's shoulders zoom; the triggers step to the previous or
		// next picture at any zoom (imageviewer.go).
		imgvShoulder(s)
		return true
	case shortcutsOpen:
		return true
	}
	return false
}

// triggerOwnedByOverlay reports whether something drawn above the scene owns
// the pad while having no trigger binding of its own. A pad button reaches the
// scene only after the dock, the launch-failure card, the tour and the overlay
// router (handleControllerEvent) have passed on it; the triggers arrive as axis
// motion and went straight to handleShoulderPress, so L2/R2 under Quick
// Settings, the dock, the podcast or LED panel, the browser's bookmark and
// cookie pickers or the controls legend skipped the video or paged the list
// hidden behind them. Those overlays now swallow the trigger instead.
func triggerOwnedByOverlay() bool {
	switch {
	case ambientIsActive(), launchFailure.open, videoPrepareActive(),
		onboarding.active && !onboardingCoveredByOverlay():
		return true
	case qrZoom.open, qs.open, podPanel.open, ledPanel.open, controlsLegend.open,
		browserCookiePanel.open, browserBookmarkPanel.open, brwInfo.open, homeCalendarOpen,
		homePowerConfirm.open, volumePopoverOpen:
		return true
	}
	return false
}

// modalPanelBlocksShoulders reports whether a panel that owns its own list is
// open. Those panels already bind L1/R1 where it makes sense; the triggers are
// consumed there instead of leaking into the scene underneath.
func modalPanelBlocksShoulders() bool {
	return modalPanelBlockReason() != ""
}

// modalPanelBlockReason names the modal that owns the keys, or "" when the
// shoulders are free. The audits that report "a modal from another test is
// still open" print it, so a leaked panel names itself instead of costing a
// debug session.
func modalPanelBlockReason() string {
	open := []struct {
		name string
		yes  bool
	}{
		{"IP panel", ipPanelOpen},
		{"process panel", processPanel.open},
		{"Wi-Fi panel", wifiPanel.open},
		{"update prompt", updatePrompt.open},
		{"browser link panel", browserLinkPanel.open},
		{"companion", companion.open},
		{"community panel", communityPanel.open},
		{"power tools", powerTools.open},
		{"achievements panel", raPanel.open},
		{"game profiles panel", gpPanel.open},
		{"curation picker", cpPanel.open},
		{"Windows apps panel", winAppsPanel.open},
		{"cast panel", castPanel.open},
		{"music settings", musicSettings.open},
		{"accessibility panel", a11y.open},
		{"smart home panel", smartHomePanel.open},
		{"calibration panel", calibrationPanel.open},
		{"remote play panel", chiakiPanel.open},
		{"save manager", savePanel.open},
		{"curation panel", curationPanel.open},
		{"ports panel", portsPanel.open},
		{"Tools panel", toolsPanel.open},
		{"netplay panel", netplayPanel.open},
		{"game library", gameLib.open},
		{"emulator panel", emuPanel.open},
		{"on-screen keyboard", kbPanel.open},
		{"downloads panel", dlPanel.open},
		{"weather popover", weatherPopoverOpen},
		{"volume popover", volumePopoverOpen},
		{"image viewer", imageViewerPath != ""},
		{"keyboard", virtualKeyboardActive},
		{"memory panel", memPanel.open},
		{"text editor", fileEditor.open},
		{"controller diagnostics", ctrldiag.open},
	}
	for _, p := range open {
		if p.yes {
			return p.name + " is open"
		}
	}
	return ""
}

// startMenuShoulder pages the entries of the open group; the triggers jump to
// the ends of that group. The rail keeps its selection, so paging never loses
// the user in another part of the menu.
func startMenuShoulder(s padShoulder, config *Config) bool {
	group, _ := startMenuCurrentGroup(config)
	items := group.items
	if len(items) == 0 {
		return true
	}
	rows := startMenuRows()
	step := 0
	switch s {
	case padL1:
		step = -rows
	case padR1:
		step = rows
	case padL2:
		tb.cursor = 0
	case padR2:
		tb.cursor = len(items) - 1
	}
	if step != 0 {
		tb.cursor += step
	}
	tb.focus = 1
	tb.cursor = clampIndex(tb.cursor, len(items))
	startMenuClampScroll(len(items))
	PlayNavSound()
	return true
}

// notifShoulder pages the notification centre, whose row count comes from the
// last frame it drew.
func notifShoulder(s padShoulder) {
	rows := nc.visibleRows
	if rows < 1 {
		rows = notifMaxRows
	}
	// The history is filed from goroutines: its length is read under its lock.
	n := notifHistoryLen()
	switch s {
	case padL1:
		nc.cursor -= rows
	case padR1:
		nc.cursor += rows
	case padL2:
		nc.cursor = 0
	case padR2:
		nc.cursor = n - 1
	}
	nc.cursor = clampIndex(nc.cursor, n)
	PlayNavSound()
}

func clipShoulder(s padShoulder) {
	rows := 5
	switch s {
	case padL1:
		clipCursorIdx -= rows
	case padR1:
		clipCursorIdx += rows
	case padL2:
		clipCursorIdx = 0
	case padR2:
		clipCursorIdx = len(vkClipVisible()) - 1
	}
	clipCursorIdx = clampIndex(clipCursorIdx, len(vkClipVisible()))
	PlayNavSound()
}

// qsShoulder pages Quick Settings. On a slider L1/R1 nudge the value, which is
// what they have always done there; on every other row they move a window of
// rows, and the triggers jump to the first and last row.
func qsShoulder(s padShoulder) {
	total := qsRowCount()
	if total == 0 {
		return
	}
	if idx := qs.cursor - len(qs.toggles); idx >= 0 && idx < len(qs.sliders) {
		switch s {
		case padL1:
			qsNavigateSlider(-5)
			return
		case padR1:
			qsNavigateSlider(5)
			return
		}
	}
	switch s {
	case padL1:
		qs.cursor -= qsPageRows
	case padR1:
		qs.cursor += qsPageRows
	case padL2:
		qs.cursor = 0
	case padR2:
		qs.cursor = total - 1
	}
	qs.cursor = clampIndex(qs.cursor, total)
	PlayNavSound()
}

// qsPageRows is how many rows one shoulder press moves in Quick Settings.
const qsPageRows = 5

func globalSearchShoulder(s padShoulder) {
	rows := 5
	switch s {
	case padL1:
		gsCursor -= rows
	case padR1:
		gsCursor += rows
	case padL2:
		gsCursor = 0
	case padR2:
		gsCursor = len(gsResults) - 1
	}
	gsCursor = clampIndex(gsCursor, len(gsResults))
	PlayNavSound()
}

func wallpaperShoulder(s padShoulder) {
	switch s {
	case padL1:
		wpCursor -= 5
	case padR1:
		wpCursor += 5
	case padL2:
		wpCursor = 0
	case padR2:
		wpCursor = len(wpEntries) - 1
	}
	wpCursor = clampIndex(wpCursor, len(wpEntries))
	PlayNavSound()
}

// handleListShoulder pages the focused scene element. Media results are the
// special case: R2 grows the list by fetching the next page of the query.
func handleListShoulder(s padShoulder, config *Config) bool {
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	scene := config.Scenes[currentSceneIndex]

	// The Media screen pages its own tabs and grids (media_hub_input.go); its
	// results element is data only.
	if mediaHubOwnsScene(scene.Name) {
		return mediaHubShoulder(s, config)
	}
	// Chat draws its own tabs and history (chatinput.go); its chat element is
	// data only, so the shoulders do not depend on which element has focus.
	if scene.Name == chatSceneName {
		return chatSceneShoulder(s, config)
	}

	if sr, ok := sceneSearchResultsElement(scene); ok {
		if videos, ok := config.Variables.Custom[sr.Variable].([]VideoInfo); ok && len(videos) > 0 {
			return searchResultsShoulder(s, config, sr, videos)
		}
	}

	if selectedButtonIndex < 0 || selectedButtonIndex >= len(scene.Elements) {
		return false
	}
	elem := scene.Elements[selectedButtonIndex]
	switch elem.Type {
	case "dynamiclist":
		return dynamicListShoulder(s, config, scene, elem)
	case "packagelist":
		return packageListShoulder(s)
	case "favorites":
		return favoritesShoulder(s)
	case "shortslist":
		return shortsListShoulder(s, config)
	case "textbrowser":
		return textBrowserShoulder(s, elem)
	case "chat":
		return chatShoulder(s, config)
	}
	return false
}

// searchResultsShoulder pages the Media grid. Shoulders move a screenful
// inside what is loaded; triggers move a whole page and fetch the next one
// when it does not exist yet.
func searchResultsShoulder(s padShoulder, config *Config, element Element, videos []VideoInfo) bool {
	focusMediaResults(config)
	pageSize := mediaPageSize(element, getElementHeight(element, 456))
	switch s {
	case padL2:
		return mediaResultsPageJump(config, -1)
	case padR2:
		return mediaResultsPageJump(config, 1)
	}
	step := pageSize
	if s == padL1 {
		step = -pageSize
	}
	// Paging moves to a different row, so the button selection goes with it.
	resetResultActions()
	focusedResultIndex += step
	if focusedResultIndex >= len(videos) {
		// Paging past the end asks for the next page, exactly like the chip.
		focusedResultIndex = len(videos) - 1
		loadMoreMediaResults(config)
	}
	focusedResultIndex = clampIndex(focusedResultIndex, len(videos))
	prefetchFocusedResult(config, videos)
	PlayNavSound()
	return true
}

func dynamicListShoulder(s padShoulder, config *Config, scene SceneConfig, element Element) bool {
	if element.Variable == appsEntriesVar {
		entries := appGridEntries(config, element)
		if len(entries) == 0 {
			return false
		}
		rows := appGridRows(element, getElementHeight(element, 500), newAppGridLayout(element, appGridElementWidth(element)))
		switch s {
		case padL1:
			appGridMove(config, scene, 0, -rows)
		case padR1:
			appGridMove(config, scene, 0, rows)
		case padL2:
			focusedFileIndex = 0
		case padR2:
			focusedFileIndex = len(entries) - 1
		}
		PlayNavSound()
		return true
	}
	entries, ok := config.Variables.Custom[element.Variable].([]FileEntry)
	if !ok || len(entries) == 0 {
		return false
	}
	rows := dynamicListMaxVisible(element)
	switch s {
	case padL1:
		focusedFileIndex -= rows
	case padR1:
		focusedFileIndex += rows
	case padL2:
		focusedFileIndex = 0
	case padR2:
		focusedFileIndex = len(entries) - 1
	}
	focusedFileIndex = clampIndex(focusedFileIndex, len(entries))
	PlayNavSound()
	return true
}

// packageListShoulder pages the package store: shelves, grid rows or list
// rows, or the details sheet's text while it is open (pkginput.go).
func packageListShoulder(s padShoulder) bool {
	return pkgShoulder(s)
}

func favoritesShoulder(s padShoulder) bool {
	items := getCurrentFavorites()
	if len(items) == 0 {
		return false
	}
	switch s {
	case padL1:
		favoritesFocusIndex -= 5
	case padR1:
		favoritesFocusIndex += 5
	case padL2:
		favoritesFocusIndex = 0
	case padR2:
		favoritesFocusIndex = len(items) - 1
	}
	favoritesFocusIndex = clampIndex(favoritesFocusIndex, len(items))
	PlayNavSound()
	return true
}

func shortsListShoulder(s padShoulder, config *Config) bool {
	shortsMutex.Lock()
	videos := shortsList
	shortsMutex.Unlock()
	if len(videos) == 0 {
		return false
	}
	cols := shortsCols
	if cols < 1 {
		cols = 1
	}
	switch s {
	case padL1:
		currentShortIdx -= cols * 3
	case padR1:
		currentShortIdx += cols * 3
	case padL2:
		currentShortIdx = 0
	case padR2:
		currentShortIdx = len(videos) - 1
	}
	currentShortIdx = clampIndex(currentShortIdx, len(videos))
	prefetchShortsFocused(config, videos)
	PlayNavSound()
	return true
}

// chatShoulder maps the shoulders on the Chat scene (chatinput.go): the
// bumpers switch tabs, L2 jumps to the oldest message and R2 back to the
// newest.
func chatShoulder(s padShoulder, config *Config) bool {
	return chatSceneShoulder(s, config)
}

func textBrowserShoulder(s padShoulder, element Element) bool {
	if textBrowserElementSource(element) == textBrowserSourceWeb {
		// A web page scrolls by styled layout rows, and its L1/R1 are handled
		// in browser.go; applying the plain-text page jump here as well
		// scrolled twice per press. The triggers instead scale the reader
		// text — on a 5-inch panel the size of the words is a first-order
		// control, while jump-to-top/bottom is still one held D-pad away.
		switch s {
		case padL2:
			return browserZoomStep(appConfig, element, -1)
		case padR2:
			return browserZoomStep(appConfig, element, 1)
		default:
			return false
		}
	}
	page := int32(20)
	switch s {
	case padL1:
		textBrowserScrollY -= page
	case padR1:
		textBrowserScrollY += page
	case padL2:
		textBrowserScrollY = 0
	case padR2:
		textBrowserScrollY = 1 << 30
	}
	if textBrowserScrollY < 0 {
		textBrowserScrollY = 0
	}
	textBrowserScrollVelocity = 0
	textBrowserScrollCooldown = 0
	PlayNavSound()
	return true
}

// clampIndex keeps a cursor inside [0, count-1].
func clampIndex(index, count int) int {
	if count <= 0 {
		return 0
	}
	if index < 0 {
		return 0
	}
	if index > count-1 {
		return count - 1
	}
	return index
}
