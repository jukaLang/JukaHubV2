package main

// On-screen keyboard.
//
// Design goals (a phone keyboard in Home's glass, driven by a pad):
//   * one bottom sheet flush with the screen edge that never covers the whole
//     screen: a header with the field's name and a tool row (Copy, Paste, Cut,
//     Select all, Clear, the language globe, the mic, Close), the text field
//     with a real caret, a suggestion strip and the key grid,
//   * a digits row, the letter rows of the chosen language (vklayouts.go),
//     punctuation, Shift (tap = one letter, double tap = caps lock) and an
//     accent-coloured action key named for what it does (Send, Search, Done),
//   * rounded glass keys with Home's 3px accent focus ring and a short press
//     animation; holding a letter offers its accents,
//   * word suggestions from an embedded English list plus the words the user
//     taught it (vkdict.go), and voice typing through Groq (vkvoice.go).
//
// Behaviour goals:
//   * typing always lands in exactly one target (scene input, chat or unit
//     converter) - the previous code routed on three overlapping booleans and
//     appended characters twice when SDL text input was active,
//   * mouse hit-testing uses logical coordinates so clicks work when the
//     window is scaled,
//   * physical keyboard support (arrows, Enter, Backspace, Esc) and controller
//     support (D-pad, A, B, Y, left shoulder) share the same activation path.

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

type vkLayerKind int

const (
	vkLayerLetters vkLayerKind = iota
	vkLayerSymbols
)

// vkTargetKind names the buffer the keyboard writes into. A single explicit
// target removes the ambiguity between chat/unit/scene routing.
type vkTargetKind int

const (
	vkTargetScene vkTargetKind = iota
	vkTargetChat
	vkTargetUnit
	vkTargetTerm       // terminal command line
	vkTargetName       // chat display name
	vkTargetTV         // Live TV channel search
	vkTargetParty      // Watch Party invite / shared link
	vkTargetRA         // RetroAchievements username / Web API key
	vkTargetMusic      // audio player initialism search
	vkTargetNetplay    // LAN play host address
	vkTargetPSRemote   // PS Remote Play pairing / account / address entry
	vkTargetWiFi       // Wi-Fi passphrase for the network picker
	vkTargetMemory     // Memory panel value field (scan / write value)
	vkTargetGameSearch // game library search
	vkTargetDownloads  // Downloads panel: a link to save for offline use
	vkTargetGameArgs   // game library: extra launch arguments for one game
	vkTargetGameTitle  // game library: display title for one game
	vkTargetToolArgs   // toolbox: arguments for the highlighted tool
	vkTargetPodcast    // podcasts panel: an RSS feed URL to subscribe to
	vkTargetFiles      // Files screen: address, password, new folder, rename
	vkTargetDNS        // Tools: host name or IP for the DNS lookup
	vkTargetFileLine   // text file editor: the line under the cursor
	vkTargetToolsURL   // Tools: URL or host for the HTTP check
	vkTargetNetwork    // Tools: host:port or domain for the port/whois rows
	vkTargetIPStream   // stream menu: the Find row's filter
	vkTargetPlaylist   // audio player: name for a saved queue
	vkTargetGroqKey    // Keyboard settings: the Groq API key voice typing uses
	vkTargetBrowserURL // Settings → Browser: the home page or a custom search address
)

// podcastFeedInput holds the feed URL while it is being typed. It is only read
// on submit, so a half-typed URL never reaches the subscription list.
var podcastFeedInput string

// fileEditorLineBuffer holds one line of a text file while it is being edited.
// Like the other buffers nothing is committed until submit.
var fileEditorLineBuffer string

// dnsQueryBuffer holds the host being typed for the Tools DNS lookup. Like the
// podcast feed it is only read on submit, so a half-typed name is never
// resolved.
var dnsQueryBuffer string

// vkNameBuffer holds the chat display name while it is being edited; it is
// committed to the user config when the keyboard submits.
var vkNameBuffer string

// vkPlaylistNameBuffer holds a playlist name while it is typed; the playlist is
// only written when the keyboard submits.
var vkPlaylistNameBuffer string

// vkGameTitleBuffer holds a library display title while it is typed. The ROM
// file is never renamed; the submitted text goes to game-library.json.
var vkGameTitleBuffer string

// wifiPassphraseBuffer holds a Wi-Fi password while it is typed. It is cleared
// when the keyboard closes or submits, so the secret does not outlive the
// dialog that needed it.
var wifiPassphraseBuffer string

var (
	vkLayer     = vkLayerLetters
	vkTarget    = vkTargetScene
	vkLayoutSig string

	vkPanelRect sdl.Rect
	vkFieldRect sdl.Rect
	vkCloseRect sdl.Rect

	// vkAnimStart is the tick when the keyboard opened, used for a short
	// slide/fade so the panel does not pop in.
	vkAnimStart uint64

	// vkPressedRect is the cap that was just actuated and vkPressedUntil is when
	// its "pushed in" look ends, so a tap is acknowledged on the very next frame.
	// The rect (not an index) is stored because a key like 123 swaps the grid
	// under the press: the flash must stay on the cap the finger touched.
	vkPressedRect  sdl.Rect
	vkPressedUntil uint64

	// gameController is the pad opened at startup. Presses stay event-driven;
	// this handle exists so the keyboard can poll a held D-pad and repeat.
	gameController *sdl.GameController
)

const (
	// vkOpenDurationMs is the length of the slide/fade when the panel appears.
	vkOpenDurationMs = 120
	// vkPressFlashMs is how long a pressed key stays visibly pushed in.
	vkPressFlashMs = 130
)

// uiHighlightColor is the shared "filled accent" colour for focused keys and
// primary actions. Some themes use a near-black info colour, so it always
// resolves to something that contrasts the panel surface.
func uiHighlightColor() sdl.Color { return installHighlight() }

// vkFocusColor is the keyboard's accent: Home's focus ring, the caret, the
// action key and an active Shift. It is glassAccent, which already falls back
// to a visible focus colour when a theme's accent is as dark as its
// background, made opaque so a filled key never turns translucent.
func vkFocusColor() sdl.Color {
	h := glassAccent()
	h.A = 255
	return h
}

func vkAbsDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

// vkAnimOffset is the vertical offset of the slide-in animation. Render and hit
// testing share it so a tap during the first 120ms still lands on the key the
// user sees, not the one an offset higher.
func vkAnimOffset() int32 {
	if vkAnimStart == 0 {
		return 0
	}
	return int32((1 - vkEaseOut(vkOpenProgress())) * 26)
}

// vkOpenProgress is the 0..1 progress of the open animation. The slide and the
// dimmed backdrop share it, so the screen darkens with the panel instead of
// snapping dark a frame before it arrives.
func vkOpenProgress() float64 {
	if vkAnimStart == 0 {
		return 1
	}
	return vkOpenProgressAt(sdl.GetTicks64())
}

// vkOpenProgressAt is the open progress for a given tick, split out so the fade
// can be tested without waiting on the clock.
func vkOpenProgressAt(now uint64) float64 {
	if now <= vkAnimStart {
		return 0
	}
	progress := float64(now-vkAnimStart) / float64(vkOpenDurationMs)
	if progress >= 1 {
		return 1
	}
	return progress
}

// --- Layout model -----------------------------------------------------------

// vkRows returns the labels for the current layer. Labels double as the
// inserted value for printable keys; named labels (SPACE, BACK, ...) map to
// actions in vkKeyValue / vkActivateFocused. The letter rows come from the
// current layout (vklayouts.go); the digits row and the bottom row are shared.
func vkRows() [][]string {
	chat := vkTarget == vkTargetChat
	action := "ENTER"
	if chat {
		action = "SEND"
	}
	layout := vkCurrentLayout()
	bottom := []string{"123"}
	if vkLayer == vkLayerSymbols {
		bottom[0] = "ABC"
	}
	// The globe joins the bottom row once there is a second language to
	// switch to, beside 123 the way a phone keyboard has it.
	if len(vkEnabledLayouts()) > 1 {
		bottom = append(bottom, "GLOBE")
	}
	bottom = append(bottom, ",", "SPACE", ".", action)
	if chat {
		bottom = append(bottom, "ASK_AI")
	}

	digits := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "0"}
	if vkLayer == vkLayerSymbols {
		currency := "$"
		if layout != nil && layout.Currency != "" {
			currency = layout.Currency
		}
		return [][]string{
			digits,
			{"!", "@", "#", currency, "%", "&", "*", "(", ")", "-"},
			{"+", "=", "/", "?", ":", ";", "_", "\"", "'", "`"},
			{"~", "<", ">", "[", "]", "{", "}", "\\", "|", "BACK"},
			bottom,
		}
	}

	letterRows := vkLayoutRows(layout)
	rows := make([][]string, 0, 5)
	rows = append(rows, digits)
	for i, src := range letterRows {
		row := make([]string, 0, len(src)+2)
		if i == 2 {
			row = append(row, "⇧")
		}
		for _, k := range src {
			if keyboardUpper {
				k = vkUpperKey(layout, k)
			}
			row = append(row, k)
		}
		if i == 2 {
			row = append(row, "BACK")
		}
		rows = append(rows, row)
	}
	return append(rows, bottom)
}

// vkKeyUnits is the key width in grid units (1 unit = one letter key).
func vkKeyUnits(key string) float64 {
	switch key {
	case "SPACE":
		return 3
	case "⇧", "BACK", "123", "ABC":
		return 1.4
	case "GLOBE":
		return 1.2
	case "ENTER":
		return 2.2
	case "SEND", "ASK_AI":
		return 2
	}
	return 1
}

// vkKeyLabel is the text drawn on a key. Shift and delete use Inter's own
// arrow glyphs (the bundled font draws them); the action key says what it
// does for this field; the space bar names the language once there is more
// than one.
func vkKeyLabel(key string) string {
	switch key {
	case "SPACE":
		if len(vkEnabledLayouts()) > 1 {
			return vkCurrentLayout().Native
		}
		return "space"
	case "BACK":
		return "⌫"
	case "ENTER":
		return vkActionLabel()
	case "SEND":
		return "Send"
	case "ASK_AI":
		return "Ask AI"
	case "⇧":
		if vkCapsLock {
			return "⇪"
		}
		return "⇧"
	case "GLOBE":
		return vkCurrentLayout().Short
	}
	return key
}

// vkActionLabel names the action key for the field it submits, the way a
// phone keyboard shows Search, Go or Join instead of a generic Done.
func vkActionLabel() string {
	switch vkTarget {
	case vkTargetTerm:
		return "Run"
	case vkTargetTV, vkTargetGameSearch, vkTargetMusic, vkTargetIPStream, homeSearchVKTarget, tzPickerVKTarget:
		return "Search"
	case vkTargetWiFi, vkTargetNetplay:
		return "Join"
	case vkTargetDNS:
		return "Look up"
	case vkTargetToolsURL, vkTargetNetwork:
		return "Check"
	case vkTargetPSRemote:
		switch {
		case psPanelEditField == psEditPIN:
			return "Pair"
		case psPanelEditField == psEditHost, psPanelPairAfterAccount:
			// A typed address, and an Account-ID asked for by a pairing, both
			// lead straight on to the PIN prompt rather than ending there.
			return "Next"
		}
		return "Save"
	case vkTargetName, vkTargetPlaylist, vkTargetRA, vkTargetGameArgs, vkTargetGameTitle, vkTargetFileLine,
		vkTargetPodcast, vkTargetDownloads, vkTargetFiles, vkTargetGroqKey, vkTargetBrowserURL:
		return "Save"
	case vkTargetScene:
		switch {
		case textBrowserAddressEditing:
			return "Go"
		case textBrowserSearchEditing, textBrowserFindEditing:
			return "Search"
		}
		if appConfig != nil && currentSceneIndex >= 0 && currentSceneIndex < len(appConfig.Scenes) &&
			sceneHasSearchResults(appConfig.Scenes[currentSceneIndex]) {
			return "Search"
		}
	}
	return "Done"
}

// vkKeyValue returns the text inserted by a printable key.
func vkKeyValue(key string) (string, bool) {
	switch key {
	case "⇧", "BACK", "ENTER", "SEND", "ASK_AI", "123", "ABC", "GLOBE":
		return "", false
	case "SPACE":
		return " ", true
	}
	return key, true
}

// vkHandleKeyClick activates a key and reports whether the keyboard closed.
// Submitting can open a follow-up prompt, such as the PS Remote pairing PIN.
func vkHandleKeyClick(row, col int, config *Config) bool {
	keyboardPosY, keyboardPosX = row, col
	vkFocusZone = vkZoneKeys
	vkActivateFocused(config)
	return !virtualKeyboardActive
}

// --- Geometry ---------------------------------------------------------------

func vkScreenSmall() bool { return screenHeight < 640 || screenWidth < 900 }

// vkLayoutKey is everything the key grid and the sheet's bands depend on. It is
// compared every frame, so it is a plain struct rather than a formatted string
// (the old signature allocated a string per frame).
type vkLayoutKey struct {
	w, h   int32
	layer  vkLayerKind
	target vkTargetKind
	upper  bool
	caps   bool
	layout *vkLayout
	langs  int
	strip  vkStripMode
}

var vkLayoutKeyBuilt vkLayoutKey

// vkGeom holds the sheet's measured bands. The renderer, the hit tests and the
// D-pad all read it, so a tap always lands on what is drawn.
var vkGeom struct {
	content sdl.Rect // the column every band is aligned to
	header  sdl.Rect
	title   sdl.Rect // room left of the tool row for the field's name
	tools   [vkToolCount]sdl.Rect
	compact bool // short tool labels on a narrow sheet
	strip   sdl.Rect
	cells   [5]sdl.Rect
	nCells  int
	footer  sdl.Rect
	keyH    int32
}

// vkEnsureLayout rebuilds the key grid and its rectangles when the layer, case,
// layout, target or screen size changed. Geometry is font-independent, which
// keeps it testable without a renderer. Setting vkLayoutSig to "" (several
// open paths do) forces a rebuild.
func vkEnsureLayout() {
	key := vkLayoutKey{
		w: screenWidth, h: screenHeight,
		layer: vkLayer, target: vkTarget,
		upper: keyboardUpper, caps: vkCapsLock,
		layout: vkCurrentLayout(), langs: len(vkEnabledLayouts()),
		strip: vkStripModeNow(),
	}
	if vkLayoutSig != "" && key == vkLayoutKeyBuilt && len(keyboardRects) > 0 {
		return
	}
	keyboard = vkRows()
	vkLayoutKeyBuilt = key
	vkLayoutSig = "built"
	vkBuildGeometry()
}

// vkToolDesignWidth is a tool chip's width in design pixels; the globe carries
// its icon and the layout code, the mic and Close are square.
func vkToolDesignWidth(t vkTool, compact bool) int32 {
	wide := [vkToolCount]int32{72, 76, 60, 100, 70, 78, 44, 0}
	narrow := [vkToolCount]int32{58, 62, 48, 46, 56, 64, 36, 0}
	if compact {
		return narrow[t]
	}
	return wide[t]
}

// vkToolRowWidth is the width the chips (not Close) need.
func vkToolRowWidth(compact bool, gap int32) int32 {
	w := int32(0)
	for t := vkTool(0); t < vkToolClose; t++ {
		w += vkToolDesignWidth(t, compact) + gap
	}
	// A wider gap separates the edit tools from the globe and the mic.
	return w + gap
}

func vkBuildGeometry() {
	rows := keyboard
	if len(rows) == 0 {
		vkPanelRect, vkFieldRect, vkCloseRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
		keyboardRects = nil
		keyboardRowCol = nil
		keyboardKeys = nil
		return
	}

	small := vkScreenSmall()
	pad := int32(16)
	gap := int32(8)
	keyH := int32(50)
	headerH := int32(40)
	fieldH := int32(48)
	stripH := int32(40)
	footerH := int32(26)
	vgap := int32(10)
	marginX := scaleX(homeSideMargin, screenWidth)
	if small {
		pad = 12
		gap = 6
		keyH = 40
		headerH = 32
		fieldH = 38
		stripH = 32
		footerH = 20
		vgap = 6
		marginX = 12
	}
	stripMode := vkStripModeNow()
	if stripMode == vkStripHidden {
		stripH = 0
	}

	sheetW := screenWidth - 2*marginX
	availW := sheetW - 2*pad
	if availW > 1080 {
		availW = 1080
	}

	// Choose a key unit from the widest row (chat bottom row with Ask AI is
	// 10.4 units), then size the panel to the widest resulting row.
	maxUnits := 0.0
	maxGaps := 0
	for _, row := range rows {
		units := 0.0
		for _, k := range row {
			units += vkKeyUnits(k)
		}
		if units > maxUnits {
			maxUnits = units
		}
		if len(row)-1 > maxGaps {
			maxGaps = len(row) - 1
		}
	}
	unit := (float64(availW) - float64(maxGaps)*float64(gap)) / maxUnits
	if unit > 92 {
		unit = 92
	}
	if unit < 34 {
		unit = 34
	}

	widths := make([][]int32, len(rows))
	rowWidths := make([]int32, len(rows))
	innerW := int32(0)
	for i, row := range rows {
		widths[i] = make([]int32, len(row))
		var w int32
		for j, k := range row {
			kw := int32(vkKeyUnits(k) * unit)
			if kw < 30 {
				kw = 30
			}
			widths[i][j] = kw
			w += kw
		}
		w += int32(len(row)-1) * gap
		rowWidths[i] = w
		if w > innerW {
			innerW = w
		}
	}

	// Every band shares one centred column: the key rows' width, but never so
	// narrow that the header cannot hold the field's name beside the tools.
	toolGap := int32(6)
	compact := small
	contentW := innerW
	if need := vkToolRowWidth(false, toolGap) + headerH + 220; !compact && contentW < need {
		if need <= availW {
			contentW = need
		} else {
			compact = true
		}
	}
	if need := vkToolRowWidth(true, toolGap) + headerH + 120; compact && contentW < need {
		contentW = need
	}
	if contentW > availW {
		contentW = availW
	}

	keysH := int32(len(rows))*keyH + int32(len(rows)-1)*gap
	sheetH := pad + headerH + vgap + fieldH + vgap + keysH + vgap + footerH + pad/2
	if stripH > 0 {
		sheetH += stripH + vgap
	}
	sheetY := screenHeight - sheetH
	if sheetY < 0 {
		sheetY = 0
	}
	vkPanelRect = sdl.Rect{X: marginX, Y: sheetY, W: sheetW, H: sheetH}
	contentX := marginX + (sheetW-contentW)/2
	vkGeom.content = sdl.Rect{X: contentX, Y: sheetY, W: contentW, H: sheetH}
	vkGeom.compact = compact
	vkGeom.keyH = keyH

	y := sheetY + pad
	vkGeom.header = sdl.Rect{X: contentX, Y: y, W: contentW, H: headerH}
	// The tool row is right-aligned: Close at the edge, then the mic and the
	// globe, then (after a wider gap) the edit tools.
	right := contentX + contentW
	vkGeom.tools[vkToolClose] = sdl.Rect{X: right - headerH, Y: y, W: headerH, H: headerH}
	vkCloseRect = vkGeom.tools[vkToolClose]
	x := right - headerH - toolGap*2
	chipH := headerH - 6
	for t := vkToolMic; t >= vkToolCopy; t-- {
		w := vkToolDesignWidth(t, compact)
		if small {
			w = w * 9 / 10
		}
		x -= w
		vkGeom.tools[t] = sdl.Rect{X: x, Y: y + (headerH-chipH)/2, W: w, H: chipH}
		x -= toolGap
		if t == vkToolGlobe {
			x -= toolGap // the separator between the edit tools and globe/mic
		}
	}
	vkGeom.title = sdl.Rect{X: contentX, Y: y, W: vkGeom.tools[vkToolCopy].X - 12 - contentX, H: headerH}
	if vkGeom.title.W < 0 {
		vkGeom.title.W = 0
	}
	y += headerH + vgap

	vkFieldRect = sdl.Rect{X: contentX, Y: y, W: contentW, H: fieldH}
	y += fieldH + vgap

	vkGeom.strip = sdl.Rect{}
	vkGeom.nCells = 0
	if stripH > 0 {
		vkGeom.strip = sdl.Rect{X: contentX, Y: y, W: contentW, H: stripH}
		n := 3
		if stripMode == vkStripQuickURL || stripMode == vkStripQuickHost {
			n = len(vkQuickChips(stripMode))
		}
		if n > len(vkGeom.cells) {
			n = len(vkGeom.cells)
		}
		cellGap := int32(6)
		cellW := (contentW - int32(n-1)*cellGap) / int32(n)
		for i := 0; i < n; i++ {
			vkGeom.cells[i] = sdl.Rect{X: contentX + int32(i)*(cellW+cellGap), Y: y, W: cellW, H: stripH}
		}
		vkGeom.nCells = n
		y += stripH + vgap
	}

	keyboardRects = make([]sdl.Rect, 0, 64)
	keyboardKeys = make([]string, 0, 64)
	keyboardRowCol = make([][2]int, 0, 64)
	keysTop := y
	for r, row := range rows {
		rx := contentX + (contentW-rowWidths[r])/2
		ky := keysTop + int32(r)*(keyH+gap)
		for c := range row {
			w := widths[r][c]
			keyboardRects = append(keyboardRects, sdl.Rect{X: rx, Y: ky, W: w, H: keyH})
			keyboardKeys = append(keyboardKeys, row[c])
			keyboardRowCol = append(keyboardRowCol, [2]int{r, c})
			rx += w + gap
		}
	}
	vkGeom.footer = sdl.Rect{X: contentX, Y: keysTop + keysH + vgap, W: contentW, H: footerH}

	if vkPopup.open {
		vkLayoutPopup()
	}

	// Keep the focus inside the new grid.
	if keyboardPosY >= len(keyboard) {
		keyboardPosY = len(keyboard) - 1
	}
	if keyboardPosY < 0 {
		keyboardPosY = 0
	}
	if row := keyboard[keyboardPosY]; keyboardPosX >= len(row) {
		keyboardPosX = len(row) - 1
	}
	if keyboardPosX < 0 {
		keyboardPosX = 0
	}
}

// vkHitTest maps logical screen coordinates to a (row, col) key.
func vkHitTest(mx, my int32) (int, int, bool) {
	vkEnsureLayout()
	my -= vkAnimOffset()
	for i := len(keyboardRects) - 1; i >= 0; i-- {
		r := keyboardRects[i]
		if mx >= r.X && mx < r.X+r.W && my >= r.Y && my < r.Y+r.H {
			if i < len(keyboardRowCol) {
				return keyboardRowCol[i][0], keyboardRowCol[i][1], true
			}
		}
	}
	return 0, 0, false
}

// vkOutsidePanel reports whether a logical point is outside the keyboard, used
// to dismiss it by tapping the dimmed content behind it.
func vkOutsidePanel(mx, my int32) bool {
	vkEnsureLayout()
	if vkPanelRect.W <= 0 {
		return false
	}
	return !pointInRect(mx, my, vkPanelRect)
}

// vkToolHit returns the tool chip under a logical point, or -1.
func vkToolHit(mx, my int32) int {
	vkEnsureLayout()
	my -= vkAnimOffset()
	for t := vkTool(0); t < vkToolCount; t++ {
		if r := vkGeom.tools[t]; r.W > 0 && pointInRect(mx, my, r) {
			return int(t)
		}
	}
	return -1
}

// vkStripHit returns the strip cell under a logical point, or -1.
func vkStripHit(mx, my int32) int {
	vkEnsureLayout()
	my -= vkAnimOffset()
	for i := 0; i < vkGeom.nCells; i++ {
		if pointInRect(mx, my, vkGeom.cells[i]) {
			return i
		}
	}
	return -1
}

// vkFieldHit reports whether a logical point is on the text field.
func vkFieldHit(mx, my int32) bool {
	vkEnsureLayout()
	return vkFieldRect.W > 0 && pointInRect(mx, my-vkAnimOffset(), vkFieldRect)
}

// vkHandlePointerDown routes a click or a tap at a logical point: the popup,
// the tool row (Close included), the strip, the field (places the caret), a
// key, or the dimmed screen outside the sheet (dismiss). source says what is
// pressing, so a long press can be followed. It reports whether the keyboard
// closed. The main loop, the modal input loop and touch all come through here.
func vkHandlePointerDown(mx, my int32, source vkHoldSource, config *Config) bool {
	vkSyncSession()
	vkEnsureLayout()
	if vkPopup.open {
		if i := vkPopupHit(mx, my); i >= 0 {
			vkPopupPick(i, config)
		} else {
			vkPopupClose()
		}
		return !virtualKeyboardActive
	}
	if vkVoiceHandlePointer(mx, my, config) {
		return !virtualKeyboardActive
	}
	if t := vkToolHit(mx, my); t >= 0 {
		tool := vkTool(t)
		vkFocusZone, vkZoneIndex = vkZoneTools, t
		vkFlashPressedRect(vkGeom.tools[t])
		if tool == vkToolGlobe {
			vkGlobeAction(source, vkGeom.tools[t], config)
		} else if vkToolEnabled(tool) {
			vkActivateTool(tool, config)
		}
		return !virtualKeyboardActive
	}
	if i := vkStripHit(mx, my); i >= 0 {
		if text, _ := vkStripCellText(i); text != "" {
			vkFocusZone, vkZoneIndex = vkZoneStrip, i
			vkFlashPressedRect(vkGeom.cells[i])
			vkActivateStrip(i, config)
		}
		return !virtualKeyboardActive
	}
	if vkFieldHit(mx, my) {
		vkFocusZone = vkZoneField
		vkTextSelected = false
		vkSetCaret(vkCurrentText(), vkFieldIndexAt(mx))
		vkAfterCaretMove()
		return false
	}
	if row, col, ok := vkHitTest(mx, my); ok {
		vkFocusZone = vkZoneKeys
		keyboardPosY, keyboardPosX = row, col
		key := vkFocusedKey()
		rect := sdl.Rect{}
		if i := vkFocusedRectIndex(); i >= 0 && i < len(keyboardRects) {
			rect = keyboardRects[i]
		}
		if key == "GLOBE" {
			vkFlashPressedRect(rect)
			vkGlobeAction(source, rect, config)
			return false
		}
		closed := vkHandleKeyClick(row, col, config)
		if !closed {
			vkHoldBegin(source, key, rect)
		}
		return closed
	}
	if vkOutsidePanel(mx, my) {
		vkCloseKeyboard()
		return true
	}
	return false
}

// vkGlobeAction is a press on the globe: a tap switches language, a long press
// lists them. With no way to follow the press (a keyboard Enter, a phone's
// synthetic button) it switches at once.
func vkGlobeAction(source vkHoldSource, rect sdl.Rect, config *Config) {
	if source == vkHoldNone {
		vkGlobePressed(config)
		return
	}
	vkHold.source = source
	vkHold.start = sdl.GetTicks64()
	vkHold.key = "GLOBE"
	vkHold.rect = rect
	vkHold.fired = false
}

// vkHandleTouchEvent turns a tap on the keyboard into the same action as a
// click. Touch used to be ignored entirely while the keyboard was open (and
// swipes changed the scene behind it), so the panel was unusable on touch-only
// devices and easy to dismiss by accident. The finger's lift is tracked too,
// so a long press can open accents and a quick tap on the globe can switch
// language.
func vkHandleTouchEvent(e *sdl.TouchFingerEvent, config *Config) {
	if e == nil {
		return
	}
	switch e.Type {
	case sdl.FINGERUP:
		vkTouchDown = false
		return
	case sdl.FINGERDOWN:
	default:
		return
	}
	vkTouchDown = true
	mx := int32(float64(e.X) * float64(screenWidth))
	my := int32(float64(e.Y) * float64(screenHeight))
	vkHandlePointerDown(mx, my, vkHoldTouch, config)
}

// --- Target / text routing --------------------------------------------------

// vkCurrentText is the buffer the keyboard is editing.
func vkCurrentText() string {
	switch vkTarget {
	case vkTargetChat:
		return chatInputText
	case vkTargetName:
		return vkNameBuffer
	case vkTargetPlaylist:
		return vkPlaylistNameBuffer
	case vkTargetUnit:
		return unitInputValue
	case vkTargetTerm:
		return term.input
	case vkTargetTV:
		return vkLiveQuery(vkTVSearchRaw, tvQuery())
	case vkTargetParty:
		return partyUI.input
	case vkTargetRA:
		return raPanel.input
	case vkTargetMusic:
		return musicSearchQueryValue()
	case vkTargetNetplay:
		return netplayPanel.address
	case vkTargetPSRemote:
		return vkPSRemoteBuffer
	case vkTargetWiFi:
		return wifiPassphraseBuffer
	case vkTargetMemory:
		return memValueBuffer
	case vkTargetGameSearch:
		return vkLiveQuery(vkGameSearchRaw, gameLibraryQuery())
	case vkTargetIPStream:
		return ipPanelQuery
	case vkTargetDownloads:
		return downloadLinkInput
	case vkTargetGameArgs:
		return gameArgsBuffer
	case vkTargetGameTitle:
		return vkGameTitleBuffer
	case vkTargetToolArgs:
		return toolArgsBuffer
	case vkTargetPodcast:
		return podcastFeedInput
	case vkTargetFiles:
		return filesInputBuffer
	case vkTargetDNS:
		return dnsQueryBuffer
	case vkTargetFileLine:
		return fileEditorLineBuffer
	case vkTargetToolsURL:
		return toolsURLBuffer
	case vkTargetNetwork:
		return netPromptBuffer
	case homeSearchVKTarget:
		return homeSearchText()
	case tzPickerVKTarget:
		return tzPicker.query
	case vkTargetGroqKey:
		return vkGroqKeyBuffer
	case vkTargetBrowserURL:
		return brwPref.buffer
	}
	return inputTextBuffer
}

func vkSetText(text string, config *Config) {
	switch vkTarget {
	case vkTargetChat:
		chatInputText = text
	case vkTargetName:
		vkNameBuffer = text
	case vkTargetPlaylist:
		vkPlaylistNameBuffer = text
	case vkTargetUnit:
		unitInputValue = text
	case vkTargetTerm:
		term.input = text
		term.cursor = len(text)
		term.histIdx = -1
	case vkTargetTV:
		// The list re-filters on every keystroke, so the user sees matches
		// appear while typing instead of after submitting.
		vkTVSearchRaw = text
		tvSetQuery(text)
	case vkTargetParty:
		partyUI.input = text
	case vkTargetRA:
		raPanel.input = text
	case vkTargetMusic:
		// The result list filters on every keystroke, so the panel behind the
		// keyboard updates while the user types the initials.
		musicSetSearchQuery(text)
	case vkTargetNetplay:
		netplayPanel.address = text
	case vkTargetPSRemote:
		vkPSRemoteBuffer = text
	case vkTargetWiFi:
		wifiPassphraseBuffer = text
	case vkTargetMemory:
		memValueBuffer = text
	case vkTargetGameSearch:
		// The library re-filters on every keystroke, so matches appear while
		// the query is typed instead of after submitting.
		vkGameSearchRaw = text
		setGameLibraryQuery(text)
	case vkTargetIPStream:
		// The stream menu stays open behind the keyboard and narrows on every
		// keystroke, so matches appear while the query is typed. A new query
		// starts from All: searching inside a country tab would hide most hits.
		ipPanelQuery = text
		ipPanelTab = 0
		ipPanelCursorToFirstChannel()
	case homeSearchVKTarget:
		// The Home search sheet re-runs its query on every key, so results
		// fill in above the keyboard while the words are typed.
		homeSearchSetQuery(text)
	case tzPickerVKTarget:
		// The zone list behind the keyboard narrows on every key.
		tzPickerSetQuery(text)
	case vkTargetDownloads:
		// The link is only used when the field is submitted, so a mistyped
		// character cannot start a download.
		downloadLinkInput = text
	case vkTargetGameArgs:
		// Edited in its own buffer; the game's options change only on submit.
		gameArgsBuffer = text
	case vkTargetGameTitle:
		// Only the library label changes, and only when the field is submitted.
		vkGameTitleBuffer = text
	case vkTargetToolArgs:
		// Same rule for the toolbox: the saved argument line changes on submit,
		// so a half-typed `--delete` can never reach the tool.
		toolArgsBuffer = text
	case vkTargetPodcast:
		// The feed is subscribed on submit only, so a mistyped host never
		// becomes a refresh that runs on every launch.
		podcastFeedInput = text
	case vkTargetDNS:
		// Resolved only on submit: a half-typed name must never reach the
		// resolver.
		dnsQueryBuffer = text
	case vkTargetFileLine:
		// The file's buffer changes only when the line is submitted, so a
		// half-typed line never becomes part of the document.
		fileEditorLineBuffer = text
	case vkTargetToolsURL:
		// Checked only on submit: a half-typed host must never be requested.
		toolsURLBuffer = text
	case vkTargetNetwork:
		// Same rule: nothing is dialled or looked up until submit.
		netPromptBuffer = text
	case vkTargetFiles:
		// One buffer for all four Files fields; filesKeyboard says which one is
		// being edited, and nothing reaches the disk or the network until submit.
		filesInputBuffer = text
	case vkTargetGroqKey:
		// The key is saved on submit only, so a half-pasted key never replaces
		// a working one.
		vkGroqKeyBuffer = text
	case vkTargetBrowserURL:
		// Same rule for the home page and a custom search address: a
		// half-typed host never becomes either (browser_engines.go).
		brwPref.buffer = text
	default:
		inputTextBuffer = text
		updateInputVariable(config)
	}
}

const vkMaxTextLen = 2000

// vkGameSearchRaw and vkTVSearchRaw hold what was typed into the game library
// and Live TV searches. Both libraries trim their query, so a typed space used
// to vanish at once and glue the next word on ("super mario" became
// "supermario"); the keyboard keeps the raw text for as long as it still is
// the library's query.
var vkGameSearchRaw, vkTVSearchRaw string

// vkLiveQuery is raw when it is query with its spaces, else query itself (the
// search was changed from elsewhere, or a new session started).
func vkLiveQuery(raw, query string) string {
	if raw == "" || strings.TrimSpace(raw) != query {
		return query
	}
	return raw
}

// vkInsertText inserts text at the caret of the active target exactly once.
// Control characters other than newline and tab are dropped: an SDL text event
// carries a fixed-size C buffer, and letting its NUL padding into the field
// made the renderer truncate at the first key (and backspace delete invisible
// bytes). An insert never grows the field past vkMaxTextLen.
func vkInsertText(text string, config *Config) {
	if text == "" {
		return
	}
	text = vkSanitizeInsert(text)
	if text == "" {
		return
	}
	// A field that opened with its old value selected behaves like a browser
	// address bar: the first character typed replaces the whole thing.
	if vkTextSelected {
		vkSetText("", config)
		vkTextSelected = false
		vkCaret = -1
	}
	current := vkCurrentText()
	room := vkMaxTextLen - len(current)
	if room <= 0 {
		return
	}
	text = vkTruncateUTF8(text, room)
	if text == "" {
		return
	}
	pos := vkCaretIndex(current)
	off := vkRuneOffset(current, pos)
	if off == len(current) {
		vkSetText(current+text, config)
	} else {
		vkSetText(current[:off]+text+current[off:], config)
		if vkCaret >= 0 {
			vkCaret = pos + utf8.RuneCountInString(text)
		}
	}
	vkAfterEdit(text)
}

// vkBackspace removes the rune (not byte) before the caret, so multi-byte
// text survives; a selection is removed whole.
func vkBackspace(config *Config) {
	if vkTextSelected {
		vkSetText("", config)
		vkTextSelected = false
		vkCaret = -1
		vkAfterEdit("")
		return
	}
	current := vkCurrentText()
	if current == "" {
		return
	}
	pos := vkCaretIndex(current)
	if pos == 0 {
		return
	}
	if pos >= utf8.RuneCountInString(current) {
		vkSetText(trimLastRune(current), config)
	} else {
		start := vkRuneOffset(current, pos-1)
		end := vkRuneOffset(current, pos)
		next := current[:start] + current[end:]
		vkSetText(next, config)
		vkSetCaret(next, pos-1)
	}
	vkAfterEdit("")
}

// vkTitle / vkPlaceholder describe the field for the active target.
func vkTitle() string {
	switch vkTarget {
	case vkTargetName:
		return "DISPLAY NAME"
	case vkTargetChat:
		if isTerminalScene() {
			return "TERMINAL"
		}
		// The keyboard covers the tray: the title says files go along.
		switch n := chatStagedCount(chatActiveTabID()); {
		case n == 1:
			return "MESSAGE · 1 FILE"
		case n > 1:
			return vkChatFilesTitle.get(n, func(n int) string { return fmt.Sprintf("MESSAGE · %d FILES", n) })
		}
		return "MESSAGE"
	case vkTargetUnit:
		return "NUMBER"
	case vkTargetTerm:
		return "COMMAND"
	case vkTargetTV:
		return "CHANNEL SEARCH"
	case vkTargetParty:
		// The party target also carries the tool screens' text fields (a bug
		// report's title and description); partyui.go names the field.
		return partyVKTitle()
	case vkTargetRA:
		if raPanel.edit == raEditAPIKey {
			return "WEB API KEY"
		}
		return "RETROACHIEVEMENTS USERNAME"
	case vkTargetMusic:
		return "SEARCH SONGS"
	case vkTargetGameSearch:
		return "SEARCH GAMES"
	case vkTargetIPStream:
		return "FIND STREAMS"
	case homeSearchVKTarget:
		return "SEARCH JUKAHUB"
	case tzPickerVKTarget:
		return "SEARCH TIME ZONES"
	case vkTargetDownloads:
		return "SAVE TO SD CARD"
	case vkTargetGameArgs:
		return "LAUNCH ARGUMENTS"
	case vkTargetGameTitle:
		return "GAME TITLE"
	case vkTargetToolArgs:
		if t, ok := toolboxFind(toolArgsToolID); ok {
			return strings.ToUpper(t.Binary) + " ARGUMENTS"
		}
		return "TOOL ARGUMENTS"
	case vkTargetNetplay:
		return "HOST ADDRESS"
	case vkTargetPSRemote:
		switch psPanelEditField {
		case psEditPIN:
			return "PAIRING PIN"
		case psEditAccount:
			return "PSN ACCOUNT-ID"
		case psEditRemoteHost:
			return "INTERNET ADDRESS"
		case psEditLoginPIN:
			return "LOGIN PASSCODE"
		}
		return "CONSOLE ADDRESS"
	case vkTargetWiFi:
		return "WI-FI PASSWORD"
	case vkTargetMemory:
		return "VALUE"
	case vkTargetPodcast:
		return "PODCAST FEED"
	case vkTargetDNS:
		return "DNS LOOKUP"
	case vkTargetFileLine:
		if fileEditor.pending == fileEditorInsertLine {
			return "INSERT LINE"
		}
		return fmt.Sprintf("LINE %d", fileEditor.cursor+1)
	case vkTargetToolsURL:
		return "HTTP CHECK"
	case vkTargetNetwork:
		switch netPrompt {
		case netPromptWhois:
			return "WHOIS LOOKUP"
		case netPromptCert:
			return "TLS CERTIFICATE"
		case netPromptBanner:
			return "SERVICE BANNER"
		case netPromptHeaders:
			return "SECURITY HEADERS"
		case netPromptCodec:
			return "ENCODE / DECODE"
		}
		return "PORT CHECK"
	case vkTargetFiles:
		switch filesKeyboard {
		case filesEditPassword:
			return "DEVICE PASSWORD"
		case filesEditNewFolder:
			return "NEW FOLDER"
		case filesEditRename:
			return "RENAME"
		}
		return "DEVICE ADDRESS"
	case vkTargetGroqKey:
		return "GROQ API KEY"
	case vkTargetBrowserURL:
		return brwPrefTitle()
	}
	if textBrowserSearchEditing {
		return "SEARCH WEB"
	}
	if textBrowserFindEditing {
		return "FIND ON PAGE"
	}
	if textBrowserAddressEditing {
		return "ADDRESS"
	}
	if appConfig != nil && currentSceneIndex >= 0 && currentSceneIndex < len(appConfig.Scenes) &&
		sceneHasSearchResults(appConfig.Scenes[currentSceneIndex]) {
		return "SEARCH"
	}
	return "INPUT"
}

func vkPlaceholder() string {
	switch vkTarget {
	case vkTargetName:
		return "Enter a display name"
	case vkTargetChat:
		if isTerminalScene() {
			return "Enter a command"
		}
		if chatStagedCount(chatActiveTabID()) > 0 {
			return "Add a message (optional)…"
		}
		return "Message Juka…"
	case vkTargetUnit:
		return "Enter a number"
	case vkTargetTerm:
		return "Type a command"
	case vkTargetTV:
		return "Search channels"
	case vkTargetParty:
		return partyVKPlaceholder()
	case vkTargetRA:
		if raPanel.edit == raEditAPIKey {
			return "32 letters or digits"
		}
		return "Your RetroAchievements username"
	case vkTargetMusic:
		return "Type initials: iwyn = I Want You Now"
	case vkTargetGameSearch:
		return "Title, console or file: zelda"
	case vkTargetIPStream:
		return "Name, group or link: news"
	case homeSearchVKTarget:
		return "Apps, settings, files or the web"
	case tzPickerVKTarget:
		return "City or country: berlin"
	case vkTargetDownloads:
		return "https://www.youtube.com/watch?v=…"
	case vkTargetGameArgs:
		return "--profile \"My Game\""
	case vkTargetGameTitle:
		return "Final Fantasy VI"
	case vkTargetToolArgs:
		if t, ok := toolboxFind(toolArgsToolID); ok && len(t.Example) > 0 {
			return strings.Join(t.Example, " ")
		}
		return "--version"
	case vkTargetNetplay:
		return "192.168.1.5"
	case vkTargetPSRemote:
		switch psPanelEditField {
		case psEditPIN:
			return "8 digits from Link Device"
		case psEditAccount:
			return "Base64 Account-ID (12 chars)"
		case psEditRemoteHost:
			return "myhome.duckdns.org or 203.0.113.7"
		case psEditLoginPIN:
			return "4 digits"
		}
		return "192.168.1.51"
	case vkTargetWiFi:
		return "At least 8 characters"
	case vkTargetMemory:
		return "Decimal, or 0x for hex"
	case vkTargetPodcast:
		return "https://example.com/feed.xml"
	case vkTargetDNS:
		return "github.com or 8.8.8.8"
	case vkTargetFileLine:
		if fileEditor.pending == fileEditorInsertLine {
			return "New line"
		}
		return "The line's text"
	case vkTargetToolsURL:
		return "example.com or https://…"
	case vkTargetNetwork:
		switch netPrompt {
		case netPromptWhois:
			return "example.com"
		case netPromptCert:
			return "example.com or example.com:8443"
		case netPromptBanner:
			return "192.168.1.7:22"
		case netPromptHeaders:
			return "example.com or https://…"
		case netPromptCodec:
			return "paste a token, a URL or hex"
		}
		return "192.168.1.7:22"
	case vkTargetFiles:
		switch filesKeyboard {
		case filesEditPassword:
			// The password is per run and random, so where to read it is the
			// only useful hint: the other console shows it under Tools.
			if feRemotePanel.tgt.Host != "" {
				return "Password for " + feRemotePanel.tgt.User + "@" + feRemotePanel.tgt.Host + " — see Tools on it"
			}
			return "The other console's SFTP password (Tools)"
		case filesEditNewFolder:
			return "Folder name"
		case filesEditRename:
			return "New name"
		}
		return "user@192.168.1.7:2222/roms"
	case vkTargetGroqKey:
		return "Paste your key from console.groq.com"
	case vkTargetBrowserURL:
		return brwPrefPlaceholder()
	}
	if textBrowserSearchEditing {
		return browserSearchPrompt
	}
	if textBrowserFindEditing {
		return browserFindPrompt
	}
	if textBrowserAddressEditing {
		return "https://example.com"
	}
	return "Type here"
}

// --- Opening / closing ------------------------------------------------------

// vkOpenChat opens the keyboard aimed at the chat composer.
func vkOpenChat() {
	chatInputActive = true
	unitInputActive = false
	vkTarget = vkTargetChat
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenName opens the keyboard aimed at the chat display-name field. The
// current name seeds the buffer so it can be edited rather than retyped.
func vkOpenName() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetName
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	vkNameBuffer = chatDisplayName()
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenPlaylistName opens the keyboard for a playlist name. It is seeded with
// the next free default so a single Return saves the queue, and the name can be
// edited into "Road Trip" without retyping.
// vkOpenGameTitle opens the keyboard for a library display title. It is seeded
// with the current label so a small correction does not start from an empty
// field; submitting it unchanged keeps the file-derived title.
func vkOpenGameTitle() {
	if !gameOptsUI.open {
		return
	}
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetGameTitle
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	vkGameTitleBuffer = gameOptsUI.game.Label()
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

func vkOpenPlaylistName() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetPlaylist
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	vkPlaylistNameBuffer = fmt.Sprintf("Playlist %d", len(musicPlaylistsSnapshot())+1)
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenUnit opens the keyboard aimed at the unit converter input.
func vkOpenUnit() {
	chatInputActive = false
	unitInputActive = true
	vkTarget = vkTargetUnit
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenTerminal opens the keyboard aimed at the terminal command line, so the
// terminal is usable from a controller or touch screen where there is no
// hardware keyboard.
func vkOpenTerminal() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetTerm
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenNetplay opens the keyboard for the host address of a LAN game. Any
// remembered address seeds the field so a second session is one correction
// away instead of a full retype.
func vkOpenNetplay() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetNetplay
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenTVSearch opens the keyboard aimed at the Live TV channel search. The
// current query seeds the buffer so a typo can be corrected instead of retyped.
func vkOpenTVSearch() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetTV
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenPartyInput opens the keyboard for the Watch Party invite or link
// field. The caller seeds partyUI.input first so an invite can be corrected
// instead of retyped.
func vkOpenPartyInput() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetParty
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenRetroAchievementsInput opens the keyboard for the RA username or Web
// API key field. raPanel.input seeds it so a typo can be corrected.
func vkOpenRetroAchievementsInput() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetRA
	vkLayer = vkLayerLetters
	// Web API keys are case-sensitive and conventionally uppercase; start
	// there so a handheld user does not have to toggle Shift for every letter.
	keyboardUpper = raPanel.edit == raEditAPIKey
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenWiFi opens the keyboard for the password of the network recorded in
// wifiPendingSSID. The picker stays behind it and receives the passphrase
// through wifiSubmitPassphrase, so the secret lives in this buffer only for as
// long as it is being typed.
func vkOpenWiFi() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetWiFi
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	wifiPassphraseBuffer = ""
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenMusicSearch opens the keyboard for the audio player's initialism
// search. The panel is already open behind it, so the hits update live.
func vkOpenMusicSearch() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetMusic
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenIPStreamSearch opens the keyboard for the stream menu's Find row. The
// menu stays open behind it and re-filters on every keystroke, and the current
// query seeds the field so a typo can be corrected instead of retyped.
func vkOpenIPStreamSearch() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetIPStream
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenGameSearch opens the keyboard for the game library query. The library
// stays behind it and re-filters on every keystroke, so the hits appear while
// the query is typed. The current query seeds the field so a typo can be
// corrected instead of retyped.
func vkOpenGameSearch() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetGameSearch
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenFilesInput opens the keyboard for one of the Files screen's fields.
// The kind decides the title and what submitting does; the seed fills the line,
// so a rename starts from the current name instead of an empty field.
func vkOpenFilesInput(kind filesEditKind, seed string) {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetFiles
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	filesKeyboard = kind
	filesInputBuffer = seed
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// toolArgsBuffer and toolArgsToolID carry the toolbox argument editor: the
// keyboard writes into the buffer and the submit handler applies it to the tool
// it was opened for. Both are touched on the UI thread only.
var (
	toolArgsBuffer string
	toolArgsToolID string
)

// vkOpenToolArgs opens the keyboard for the arguments of one toolbox tool. The
// remembered line (or the catalog example) seeds the field so a repeat run is a
// correction instead of a retype.
func vkOpenToolArgs(id string) {
	if _, ok := toolboxFind(id); !ok {
		return
	}
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetToolArgs
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	toolArgsToolID = id
	toolArgsBuffer = toolboxArgsFor(id)
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenDNSQuery opens the keyboard for the Tools DNS lookup. The remembered
// host (or the text a failed submit kept) seeds the field, so looking up the
// same name again or fixing a typo is an edit instead of a retype.
func vkOpenDNSQuery(seed string) {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetDNS
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	if seed == "" {
		seed = powerDNSRowHost()
	}
	dnsQueryBuffer = seed
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenNetPrompt opens the keyboard for the port check or the domain lookup.
// The remembered value seeds the field, so checking again is an edit.
func vkOpenNetPrompt(kind netPromptKind, seed string) {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetNetwork
	netPrompt = kind
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	if seed == "" {
		// Each row remembers its own last target, so a correction is an edit
		// rather than a retype.
		switch kind {
		case netPromptWhois:
			seed = netLastDomain
		case netPromptCert:
			seed = netLastCert
		case netPromptBanner:
			seed = netLastBanner
		case netPromptHeaders:
			seed = netLastHeaders
		case netPromptCodec:
			seed = netLastCodec
		default:
			seed = netLastPort
		}
	}
	netPromptBuffer = seed
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenToolsURL opens the keyboard for the Tools HTTP check. The remembered
// URL (or the text a rejected submit kept) seeds the field.
func vkOpenToolsURL(seed string) {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetToolsURL
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	if seed == "" {
		seed = toolsLastURL
	}
	toolsURLBuffer = seed
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenFileLine opens the keyboard for one line of the text editor. The line
// is seeded so an edit is a correction instead of a retype; an insert starts
// empty.
func vkOpenFileLine(seed string) {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetFileLine
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	fileEditorLineBuffer = seed
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenGameArgs opens the keyboard for the arguments of the game highlighted in
// the options sheet. The saved value seeds the field so an option can be
// corrected instead of retyped.
func vkOpenGameArgs() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetGameArgs
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	gameArgsBuffer = gameOptsUI.opts.Args
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenDownloadLink opens the keyboard for a link to save for offline
// listening. The Downloads panel stays open behind it.
func vkOpenDownloadLink() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetDownloads
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkOpenSceneInput opens the keyboard for a scene input element.
func vkOpenSceneInput(config *Config, sceneIdx, elemIdx int) {
	if sceneIdx < 0 || sceneIdx >= len(config.Scenes) {
		return
	}
	if elemIdx < 0 || elemIdx >= len(config.Scenes[sceneIdx].Elements) {
		return
	}
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetScene
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	activeSceneIndex = sceneIdx
	activeElementIndex = elemIdx
	inputTextBuffer = ""
	variable := config.Scenes[sceneIdx].Elements[elemIdx].Variable
	if textBrowserSearchEditing {
		variable = browserSearchVariable
	} else if textBrowserFindEditing {
		variable = browserFindVariable
	} else if textBrowserAddressEditing {
		variable = textBrowserURLVariable
	} else if browserFormEditing {
		// A field on the page: its own buffer, so the address bar and the web
		// search keep what they were holding.
		variable = browserFormVariable
	}
	if variable != "" {
		inputTextBuffer = inputVariableValue(config, variable)
	}
	// The address bar opens with the current URL selected so a new address
	// replaces it, instead of being appended to the old one.
	vkTextSelected = textBrowserAddressEditing && inputTextBuffer != ""
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkCloseKeyboard dismisses the keyboard. Only scene inputs reset the modal
// element indices; chat and unit input keep their state.
func vkCloseKeyboard() {
	if vkTarget == vkTargetFiles {
		// A dismissed password field must not keep the secret around, and no
		// Files field outlives the dialog that asked for it.
		filesInputBuffer = ""
	}
	virtualKeyboardActive = false
	// A dismissed prompt must not leave "play this next" armed for the next
	// keyboard session, which may be an ordinary search. The stream menu's
	// "add a link" prompt is the same one-shot, so it is disarmed here too.
	mediaPlayLinkPending = false
	ipPromptPending = false
	vkTextSelected = false
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	vkAnimStart = 0
	vkResetNavRepeat()
	vkClearPressFlash()
	vkEndSession()
	if vkTarget == vkTargetWiFi {
		// A dismissed password field must not keep the secret around.
		wifiPassphraseBuffer = ""
		wifiPendingSSID = ""
	}
	if vkTarget == vkTargetScene {
		activeSceneIndex = -1
		activeElementIndex = -1
	}
	if vkTarget == vkTargetDNS {
		dnsQueryBuffer = ""
	}
	if vkTarget == vkTargetFileLine {
		fileEditorLineBuffer = ""
	}
	if vkTarget == vkTargetGameTitle {
		vkGameTitleBuffer = ""
	}
	if vkTarget == vkTargetToolsURL {
		toolsURLBuffer = ""
	}
	if vkTarget == vkTargetNetwork {
		netPromptBuffer = ""
	}
	if vkTarget == vkTargetGroqKey {
		// A pasted key must not outlive the field that asked for it.
		vkGroqKeyBuffer = ""
	}
	if vkTarget == vkTargetPSRemote && psPanelEditField == psEditLoginPIN {
		// Nor a typed login passcode; a submit has read it already.
		vkPSRemoteBuffer = ""
	}
	if vkTarget == vkTargetBrowserURL {
		brwPref.buffer = ""
	}
	if vkTarget == vkTargetRA {
		// The API-key field starts in uppercase; do not leak that into the
		// next keyboard target.
		keyboardUpper = false
	}
	chatInputActive = false
	unitInputActive = false
}

// --- Activation -------------------------------------------------------------

// vkFocusedKey returns the label under the highlight ("" when out of range).
func vkFocusedKey() string {
	if keyboardPosY < 0 || keyboardPosY >= len(keyboard) {
		return ""
	}
	row := keyboard[keyboardPosY]
	if keyboardPosX < 0 || keyboardPosX >= len(row) {
		return ""
	}
	return row[keyboardPosX]
}

// vkActivateFocused presses whatever the highlight is on: a popup cell, a
// strip cell, the field (select all), a tool chip or a key.
func vkActivateFocused(config *Config) {
	vkSyncSession()
	if vkPopup.open {
		vkPopupPick(vkPopup.focus, config)
		return
	}
	switch vkFocusZone {
	case vkZoneStrip:
		if vkVoiceActive() {
			// The voice bar covers the strip: A answers the bar, never a
			// suggestion hidden under it.
			switch vkVoice.phase {
			case vkVoiceRecording:
				vkVoiceStop(config)
			case vkVoiceMessage:
				if vkVoice.action != vkVoiceNoAction {
					vkVoiceRunAction(config)
				} else {
					vkVoice.phase = vkVoiceIdle
				}
			}
			return
		}
		if vkZoneIndex >= 0 && vkZoneIndex < vkGeom.nCells {
			vkFlashPressedRect(vkGeom.cells[vkZoneIndex])
		}
		vkActivateStrip(vkZoneIndex, config)
		return
	case vkZoneField:
		vkToggleSelectAll()
		return
	case vkZoneTools:
		t := vkTool(vkZoneIndex)
		if t >= 0 && t < vkToolCount {
			vkFlashPressedRect(vkGeom.tools[t])
			if vkToolEnabled(t) {
				vkActivateTool(t, config)
			}
		}
		return
	}
	if keyboardPosY < 0 || keyboardPosY >= len(keyboard) {
		return
	}
	row := keyboard[keyboardPosY]
	if keyboardPosX < 0 || keyboardPosX >= len(row) {
		return
	}
	key := row[keyboardPosX]
	// Acknowledge the press before it changes anything, so a key that also
	// rebuilds the layout (123, SHIFT) still flashes where it was.
	if i := vkFocusedRectIndex(); i >= 0 && i < len(keyboardRects) {
		vkFlashPressedRect(keyboardRects[i])
	}

	switch key {
	case "⇧":
		toggleKeyboardCase()
	case "123", "ABC":
		vkToggleLayer()
	case "GLOBE":
		vkGlobePressed(config)
	case "BACK":
		vkBackspace(config)
	case "ENTER", "SEND":
		vkPlay(PlayActivateSound)
		vkSubmit(config, false)
	case "ASK_AI":
		vkPlay(PlayActivateSound)
		vkSubmit(config, true)
	default:
		if value, ok := vkKeyValue(key); ok {
			trophyEvent(tkKeyChars) // one on-screen key typed (not paste, suggestions or a hardware keyboard)
			vkInsertText(value, config)
		}
	}
	switch key {
	case "ENTER", "SEND", "ASK_AI", "GLOBE":
	default:
		vkKeySound()
	}
	if virtualKeyboardActive {
		vkEnsureLayout()
	}
}

// vkFocusedRectIndex returns the index of the highlighted cap in keyboardRects.
func vkFocusedRectIndex() int {
	for i := range keyboardRowCol {
		if keyboardRowCol[i][0] == keyboardPosY && keyboardRowCol[i][1] == keyboardPosX {
			return i
		}
	}
	return -1
}

// vkHoverIndex maps a logical pointer position to a cap index, or -1 when the
// pointer is not over the key grid.
func vkHoverIndex(mx, my int32) int {
	row, col, ok := vkHitTest(mx, my)
	if !ok {
		return -1
	}
	for i := range keyboardRowCol {
		if keyboardRowCol[i][0] == row && keyboardRowCol[i][1] == col {
			return i
		}
	}
	return -1
}

// vkHoverWindowMs is how long a pointer stays "live" after it last moved.
const vkHoverWindowMs = 400

var (
	vkPointerX, vkPointerY int32
	vkPointerMovedAt       uint64
)

// vkHoverFresh reports whether the pointer moved recently enough to count as a
// mouse. Touch backends leave SDL's emulated pointer parked on the last tap, and
// a parked pointer would keep a stale cap lit while the D-pad moves.
func vkHoverFresh(now uint64) bool {
	if vkPointerMovedAt == 0 || now < vkPointerMovedAt {
		return false
	}
	return now-vkPointerMovedAt <= vkHoverWindowMs
}

// vkHoverFromPointer is vkHoverIndex for the current mouse position, while the
// pointer is being moved. Without it a mouse gave no hint which key a click
// would press.
func vkHoverFromPointer() int {
	if vkPanelRect.W <= 0 || len(keyboardRects) == 0 {
		return -1
	}
	mx, my, _ := vcursorMouseState()
	now := sdl.GetTicks64()
	if mx != vkPointerX || my != vkPointerY {
		vkPointerX, vkPointerY = mx, my
		vkPointerMovedAt = now
	}
	if !vkHoverFresh(now) {
		return -1
	}
	lx, ly := physicalToLogical(mx, my)
	if !pointInRect(lx, ly, vkPanelRect) {
		return -1
	}
	return vkHoverIndex(lx, ly)
}

// vkHandleControllerButton applies one controller button to the open keyboard,
// so the main loop and the modal input loop (the text browser's address bar)
// behave identically. It reports whether the keyboard consumed the button.
//
//	D-pad      move (in the field: move the caret)
//	A          press; hold on a letter for its accents, on the globe for languages
//	B / Back   close (an open popup or a recording first)
//	Start      close
//	X          delete (hold to repeat)
//	Y          submit (Ask AI in chat)
//	L1         Shift (twice quickly: caps lock)
//	R1         letters / symbols; with L1 held: next language
//	L1 + X/Y   copy / paste
func vkHandleControllerButton(button uint8, config *Config) bool {
	vkSyncSession()
	if vkVoiceHandleButton(button, config) {
		return true
	}
	if vkPopup.open {
		switch sdl.GameControllerButton(button) {
		case sdl.CONTROLLER_BUTTON_DPAD_UP, sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			vkPopupMove(-1, 0)
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			vkPopupMove(1, 0)
		case sdl.CONTROLLER_BUTTON_A:
			vkPopupPick(vkPopup.focus, config)
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
			vkPopupClose()
			vkPlay(PlayBackSound)
		}
		return true
	}
	if vkHandleChord(button, config) {
		return true
	}
	switch sdl.GameControllerButton(button) {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		vkMoveFocus(0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		vkMoveFocus(0, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		vkMoveFocus(-1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		vkMoveFocus(1, 0)
	case sdl.CONTROLLER_BUTTON_A:
		vkPadPress(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		vkCloseKeyboard()
		vkPlay(PlayBackSound)
	case sdl.CONTROLLER_BUTTON_X:
		vkBackspace(config)
		vkKeySound()
		if i := vkKeyIndex("BACK"); i >= 0 {
			vkFlashPressedRect(keyboardRects[i])
			vkHoldBegin(vkHoldPadX, "BACK", keyboardRects[i])
		} else {
			vkHoldBegin(vkHoldPadX, "BACK", sdl.Rect{})
		}
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		vkL1ShiftBefore, vkL1CapsBefore = keyboardUpper, vkCapsLock
		vkL1ChordArmed = true
		vkL1DownAt = sdl.GetTicks64()
		toggleKeyboardCase()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		if vkPadHeld(sdl.CONTROLLER_BUTTON_LEFTSHOULDER) {
			if vkL1ChordArmed {
				vkL1ChordArmed = false
				keyboardUpper, vkCapsLock = vkL1ShiftBefore, vkL1CapsBefore
				vkLayoutSig = ""
			}
			vkGlobePressed(config)
		} else {
			vkToggleLayer()
		}
	case sdl.CONTROLLER_BUTTON_Y:
		// Y is the secondary submit: Ask AI in chat, Done everywhere else.
		vkPlay(PlayActivateSound)
		vkSubmit(config, vkTarget == vkTargetChat)
	default:
		return false
	}
	return true
}

// vkPadPress is A: press the focused element and follow the button for a long
// press (accents on a letter, the language list on the globe).
func vkPadPress(config *Config) {
	if vkFocusZone == vkZoneTools && vkTool(vkZoneIndex) == vkToolGlobe {
		vkFlashPressedRect(vkGeom.tools[vkToolGlobe])
		vkGlobeAction(vkHoldPadA, vkGeom.tools[vkToolGlobe], config)
		return
	}
	if vkFocusZone == vkZoneKeys && !vkPopup.open {
		key := vkFocusedKey()
		rect := sdl.Rect{}
		if i := vkFocusedRectIndex(); i >= 0 && i < len(keyboardRects) {
			rect = keyboardRects[i]
		}
		if key == "GLOBE" {
			vkFlashPressedRect(rect)
			vkGlobeAction(vkHoldPadA, rect, config)
			return
		}
		vkActivateFocused(config)
		if virtualKeyboardActive {
			// A letter with accents waits for a long press, delete repeats.
			vkHoldBegin(vkHoldPadA, key, rect)
		}
		return
	}
	vkActivateFocused(config)
}

// vkKeyIndex is the index of a key label in the current grid, or -1.
func vkKeyIndex(key string) int {
	for i, k := range keyboardKeys {
		if k == key {
			return i
		}
	}
	return -1
}

// vkMoveFocus moves the highlight by (dx, dy). Up from the top key row walks
// into the strip, the field and the tool row; in the field Left/Right move the
// caret. Moving between key rows keeps to the key under the old one's centre,
// so Up from the wide space bar lands above it, not at its column index.
func vkMoveFocus(dx, dy int) {
	if vkPopup.open {
		vkPopupMove(dx, dy)
		return
	}
	if len(keyboard) == 0 {
		return
	}
	vkEnsureLayout()
	switch vkFocusZone {
	case vkZoneStrip:
		switch {
		case dy > 0:
			vkFocusKeysFrom(vkGeom.cells[vkClampIndex(vkZoneIndex, vkGeom.nCells)], 0)
		case dy < 0:
			vkFocusZone = vkZoneField
		case dx != 0 && !vkVoiceActive():
			// While the voice bar covers the strip there are no cells to walk.
			vkZoneIndex = vkNextStripCell(vkZoneIndex, dx)
		}
		return
	case vkZoneField:
		switch {
		case dx != 0:
			vkMoveCaret(dx)
		case dy > 0:
			if vkGeom.nCells > 0 && vkStripHasContent() {
				vkFocusZone = vkZoneStrip
				vkZoneIndex = vkNextStripCell(-1, 1)
			} else {
				vkFocusKeysFrom(vkFieldRect, 0)
			}
		case dy < 0:
			vkFocusZone = vkZoneTools
			vkZoneIndex = int(vkToolClose)
			for t := vkToolCopy; t < vkToolClose; t++ {
				if vkToolEnabled(t) {
					vkZoneIndex = int(t)
					break
				}
			}
		}
		return
	case vkZoneTools:
		switch {
		case dy > 0:
			vkFocusZone = vkZoneField
		case dx != 0:
			vkZoneIndex = vkNextTool(vkZoneIndex, dx)
		}
		return
	}

	if keyboardPosY < 0 || keyboardPosY >= len(keyboard) {
		keyboardPosY, keyboardPosX = 0, 0
	}
	if dy < 0 && keyboardPosY == 0 {
		if vkGeom.nCells > 0 && vkStripHasContent() {
			vkFocusZone = vkZoneStrip
			vkZoneIndex = vkNearestCell(vkFocusedKeyCenterX())
		} else {
			vkFocusZone = vkZoneField
		}
		return
	}
	if dy != 0 {
		ny := keyboardPosY + dy
		if ny >= 0 && ny < len(keyboard) {
			cx := vkFocusedKeyCenterX()
			keyboardPosY = ny
			keyboardPosX = vkNearestKeyInRow(ny, cx)
		}
	}
	if dx != 0 {
		nx := keyboardPosX + dx
		if nx >= 0 && nx < len(keyboard[keyboardPosY]) {
			keyboardPosX = nx
		}
	}
	if keyboardPosX >= len(keyboard[keyboardPosY]) {
		keyboardPosX = len(keyboard[keyboardPosY]) - 1
	}
	if keyboardPosX < 0 {
		keyboardPosX = 0
	}
}

func vkClampIndex(i, n int) int {
	if i >= n {
		i = n - 1
	}
	if i < 0 {
		i = 0
	}
	return i
}

// vkFocusedKeyCenterX is the horizontal centre of the highlighted key.
func vkFocusedKeyCenterX() int32 {
	if i := vkFocusedRectIndex(); i >= 0 && i < len(keyboardRects) {
		r := keyboardRects[i]
		return r.X + r.W/2
	}
	return vkFieldRect.X + vkFieldRect.W/2
}

// vkNearestKeyInRow is the column of the key in row whose centre is nearest x.
func vkNearestKeyInRow(row int, x int32) int {
	best, bestD := 0, int32(1<<30)
	for i, rc := range keyboardRowCol {
		if rc[0] != row || i >= len(keyboardRects) {
			continue
		}
		r := keyboardRects[i]
		d := r.X + r.W/2 - x
		if d < 0 {
			d = -d
		}
		if d < bestD {
			best, bestD = rc[1], d
		}
	}
	return best
}

// vkFocusKeysFrom moves the highlight onto key row `row`, under rect's centre.
func vkFocusKeysFrom(rect sdl.Rect, row int) {
	vkFocusZone = vkZoneKeys
	if row >= len(keyboard) {
		row = len(keyboard) - 1
	}
	keyboardPosY = row
	keyboardPosX = vkNearestKeyInRow(row, rect.X+rect.W/2)
}

// vkNearestCell is the strip cell nearest x that has something in it.
func vkNearestCell(x int32) int {
	best, bestD := 0, int32(1<<30)
	for i := 0; i < vkGeom.nCells; i++ {
		if t, _ := vkStripCellText(i); t == "" {
			continue
		}
		r := vkGeom.cells[i]
		d := r.X + r.W/2 - x
		if d < 0 {
			d = -d
		}
		if d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// vkNextStripCell steps from cell i in direction dir to the next filled cell
// (staying put at either end).
func vkNextStripCell(i, dir int) int {
	for j := i + dir; j >= 0 && j < vkGeom.nCells; j += dir {
		if t, _ := vkStripCellText(j); t != "" {
			return j
		}
	}
	if i < 0 {
		return 0
	}
	return i
}

// vkNextTool steps the tool-row highlight, skipping chips that cannot act.
func vkNextTool(i, dir int) int {
	for j := i + dir; j >= 0 && j < int(vkToolCount); j += dir {
		if vkToolEnabled(vkTool(j)) {
			return j
		}
	}
	return i
}

// vkSubmit commits the active target and closes the keyboard.
func vkSubmit(config *Config, askAI bool) {
	// The last word has no space after it to commit it; learn it before the
	// field is cleared.
	vkLearnLastWord()
	text := strings.TrimSpace(vkCurrentText())
	switch vkTarget {
	case vkTargetUnit:
		vkCloseKeyboard()
	case vkTargetTV:
		// The filter already applied while typing; submitting just dismisses.
		vkCloseKeyboard()
	case vkTargetGameSearch:
		// Same here: the library narrowed as the query was typed.
		vkCloseKeyboard()
	case vkTargetGroqKey:
		// Read before closing: closing clears the buffer.
		vkCloseKeyboard()
		vkSaveGroqKey(config, text)
	case vkTargetBrowserURL:
		// The text was read above; the address applies only here, so B leaves
		// the home page and the search engine as they were.
		vkCloseKeyboard()
		brwPrefSubmit(config, text)
	case homeSearchVKTarget:
		// The Home search results are already live: Done drops the keyboard
		// and hands the highlight to the first result.
		vkCloseKeyboard()
		homeSearchKeyboardDone()
	case tzPickerVKTarget:
		// The zone list already narrowed while typing; Search drops the
		// keyboard with the cursor on the first match.
		vkCloseKeyboard()
	case vkTargetDownloads:
		// Submit before closing: closing is what clears the field, and an
		// emptied link must not become a queued row.
		downloadLinkSubmit(text)
		vkCloseKeyboard()
	case vkTargetIPStream:
		// The filter already applied while typing: submitting trims it and says
		// how much of the library it kept.
		ipPanelSubmitQuery(text)
		vkCloseKeyboard()
	case vkTargetTerm:
		vkSetText("", config)
		vkCloseKeyboard()
		if text != "" {
			termExecute(text)
		}
	case vkTargetPlaylist:
		vkCloseKeyboard()
		if pl, err := musicSaveQueueAsPlaylist(text); err != nil {
			ShowToast("Playlist: "+err.Error(), ToastKindWarning)
		} else {
			ShowToast(fmt.Sprintf("Saved %s · %d tracks", pl.Name, len(pl.Tracks)), ToastKindSuccess)
		}
	case vkTargetName:
		// Empty input resets to the default name ("You").
		name := setChatDisplayName(text)
		vkCloseKeyboard()
		showToast("Chat name set to "+name, ToastInfo())
	case vkTargetParty:
		vkCloseKeyboard()
		partySubmitInput(text)
	case vkTargetMusic:
		vkCloseKeyboard()
		if !musicSearchPlay() {
			showToast("No song matches those initials", ToastInfo())
		}
	case vkTargetNetplay:
		vkCloseKeyboard()
		netplayJoinSubmit(text)
	case vkTargetPSRemote:
		vkCloseKeyboard()
		psPanelApplyEdit(text)
	case vkTargetRA:
		// Save the username or Web API key being edited (raPanel.edit says
		// which). This case had gone missing, so Save fell through to the
		// scene-search default and the credential was never stored.
		vkCloseKeyboard()
		raSubmitCredential(text)
	case vkTargetWiFi:
		// Passphrases are not trimmed: a leading or trailing space is part of
		// the WPA key, and quietly removing it would fail with "wrong password".
		// Submit before closing: closing clears the pending network and buffer.
		wifiSubmitPassphrase(vkCurrentText())
		vkCloseKeyboard()
	case vkTargetGameArgs:
		// The sheet stays open behind the keyboard, so saving just updates its
		// row and the game's record; the next launch picks it up.
		vkCloseKeyboard()
		applyGameArgs()
	case vkTargetGameTitle:
		// Rename only the library label; the ROM file and its metadata keys keep
		// their names. Closing first clears the keyboard buffer.
		vkCloseKeyboard()
		applyGameDisplayTitle(text)
	case vkTargetPodcast:
		// Subscribe before closing: closing clears the buffer, and the panel is
		// already open behind the keyboard so the new show can be refreshed
		// into place.
		podcastSubmitFeed(text)
		vkCloseKeyboard()
	case vkTargetToolArgs:
		// Save first: the toolbox page reads the remembered line, and the run
		// itself happens on the page so its output lands in the viewer.
		toolboxSetArgs(toolArgsToolID, text)
		id := toolArgsToolID
		vkCloseKeyboard()
		powerRunToolFromArgs(id)
	case vkTargetMemory:
		// The buffer is committed before closing, and an empty field clears the
		// value so the panel shows "not set" instead of holding a stale number.
		vkCloseKeyboard()
		memApplyValueInput(text)
	case vkTargetDNS:
		// Read before closing: closing clears the buffer, and the lookup is
		// started from the text the user actually submitted.
		value := vkCurrentText()
		vkCloseKeyboard()
		powerDNSLookupSubmit(value)
	case vkTargetFileLine:
		// Read before closing: closing clears the buffer, and the line is
		// written into the document from what was submitted.
		value := vkCurrentText()
		vkCloseKeyboard()
		fileEditorApplyLine(value)
	case vkTargetToolsURL:
		// Read before closing, like the DNS field: the check runs from what was
		// submitted, and a rejected URL reopens with that text.
		value := vkCurrentText()
		vkCloseKeyboard()
		toolsHTTPCheckSubmit(value)
	case vkTargetNetwork:
		// One target for two rows: netPromptSubmit knows which row opened it and
		// reopens the field if the text is refused.
		value := vkCurrentText()
		vkCloseKeyboard()
		netPromptSubmit(value)
	case vkTargetFiles:
		// Read before closing: a password field is cleared on close, so submitting
		// the trimmed buffer afterwards would send an empty secret.
		value := vkCurrentText()
		vkCloseKeyboard()
		filesSubmitInput(value)
	case vkTargetChat:
		vkSetText("", config)
		// Close first: sending writes the chat log to the SD card (and may
		// queue a network call), and the panel used to stay on screen until
		// that finished, which looked like the keyboard ignoring the button.
		vkCloseKeyboard()
		// Files waiting in the tray go even without words (chatfiles.go).
		if text != "" || (chatSceneActive(config) && chatStagedCount(chatActiveTabID()) > 0) {
			// The chat sends to the tab on screen (community, Juka AI or
			// Saved); Ask AI hands the text to Juka AI instead (chatstate.go).
			chatSubmitComposer(text, askAI)
		}
	default:
		if ipPromptPending {
			// The stream menu asked for a channel link: save it as a channel and
			// play it, and never treat it as a search or a Play Link.
			ipPromptPending = false
			vkSetText(text, config)
			vkCloseKeyboard()
			submitIPStreamLink(config, text)
			return
		}
		if mediaPlayLinkPending {
			// Play Link asked for a link: play what was typed, and never run
			// the scene's search on it.
			mediaPlayLinkPending = false
			vkSetText(text, config)
			vkCloseKeyboard()
			submitMediaPlayLink(config, text)
			return
		}
		if textBrowserAddressEditing {
			textBrowserAddressSubmitted = true
		}
		if textBrowserSearchEditing {
			textBrowserSearchSubmitted = true
		}
		if textBrowserFindEditing {
			textBrowserFindSubmitted = true
		}
		if browserFormEditing {
			browserFormSubmitted = true
		}
		vkSetText(text, config)
		vkCloseKeyboard()
		vkTriggerSceneSearch(config)
	}
}

// vkTriggerSceneSearch runs the search a search scene declares when the
// keyboard is submitted. It used to look for a button whose trigger was
// literally "yt_search", which no media scene uses: the Tube search button is
// "youtube_smart", so typing a query and pressing Save closed the keyboard and
// started nothing. It now dispatches whatever search trigger the scene has and
// falls back to a plain YouTube search.
func vkTriggerSceneSearch(config *Config) {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return
	}
	scene := config.Scenes[currentSceneIndex]
	if !sceneHasSearchResults(scene) {
		return
	}
	if elem, ok := sceneMediaSearchButton(scene); ok {
		if runMediaSearchTrigger(config, elem.Trigger, elem.TriggerTarget, elem.TriggerValue) {
			return
		}
	}
	if q, _ := config.Variables.Custom["search_query"].(string); strings.TrimSpace(q) == "" {
		return
	}
	trophyEvent(tkVideoSearch)
	executeYouTubeSearch(config, ytSearchCmd(config), "search_results", snapshotVars(config))
}

// toggleKeyboardCase is Shift, without losing the focused key: a tap shifts
// the next letter, a second tap within vkCapsDoubleTapMs locks caps, and a tap
// with caps locked turns it off.
func toggleKeyboardCase() {
	now := sdl.GetTicks64()
	switch {
	case vkCapsLock:
		vkCapsLock = false
		keyboardUpper = false
	case keyboardUpper && vkShiftArmed && now-vkLastShiftAt <= vkCapsDoubleTapMs:
		vkCapsLock = true
	default:
		keyboardUpper = !keyboardUpper
	}
	// Two Shift taps in a row (nothing typed between them) can lock caps.
	vkShiftArmed = !vkCapsLock
	vkLastShiftAt = now
	vkLayoutSig = ""
	vkEnsureLayout()
}

// vkToggleLayer switches between the letters and symbols layer. The 123/ABC key
// and the right shoulder both use it, so punctuation is one press away instead
// of a walk down to the bottom row.
func vkToggleLayer() {
	if vkLayer == vkLayerSymbols {
		vkLayer = vkLayerLetters
	} else {
		vkLayer = vkLayerSymbols
	}
	vkLayoutSig = ""
	vkEnsureLayout()
}

// --- Press feedback ---------------------------------------------------------

// vkFlashPressedRect marks a cap as just pressed. The flash is drawn on top of
// the cached layer for a couple of frames, which is what makes a tap feel
// acknowledged immediately - most of all on touch, where the finger covers the
// cap that would otherwise show the new focus.
func vkFlashPressedRect(r sdl.Rect) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	vkPressedRect = r
	vkPressedUntil = sdl.GetTicks64() + vkPressFlashMs
}

// vkPressedRectAt returns the cap that is still showing its press flash.
func vkPressedRectAt(now uint64) (sdl.Rect, bool) {
	if vkPressedUntil == 0 || now >= vkPressedUntil {
		return sdl.Rect{}, false
	}
	return vkPressedRect, true
}

// vkClearPressFlash drops the flash, e.g. when the keyboard closes.
func vkClearPressFlash() {
	vkPressedRect = sdl.Rect{}
	vkPressedUntil = 0
}

// --- Hold-to-repeat ---------------------------------------------------------
//
// The tracker and its cadence are shared with menu navigation (navrepeat.go):
// the keyboard moves its own highlight, while other screens re-queue a press.

var vkNavRepeat navRepeatTracker

// vkControllerNavDirection reads the D-pad direction held right now, as the
// screens see it after accessibility remapping (a11yHeldNavDirection).
// Physical arrow keys are left to SDL's own key repeat; polling them too would
// double the rate of a desktop hold.
func vkControllerNavDirection() (int, int) {
	pad := gameController
	if pad == nil {
		return 0, 0
	}
	return a11yHeldNavDirection(func(b uint8) bool {
		return pad.Button(sdl.GameControllerButton(b)) != 0
	})
}

// vkTickNavRepeat advances the held-D-pad state. The main loop and the modal
// input loop call it once per frame while the keyboard is open, so it is also
// the keyboard's frame tick: long presses, the voice recorder and the deferred
// save of learned words run from here.
func vkTickNavRepeat() {
	if !virtualKeyboardActive {
		vkNavRepeat = navRepeatTracker{}
		return
	}
	vkSyncSession()
	now := sdl.GetTicks64()
	dx, dy := vkControllerNavDirection()
	if stepX, stepY := vkNavRepeat.step(now, dx, dy); stepX != 0 || stepY != 0 {
		vkMoveFocus(stepX, stepY)
	}
	vkTickHold(now, appConfig)
	vkVoiceTick(appConfig)
	vkLearnedMaybeSave(time.Now())
}

// vkResetNavRepeat forgets the held direction, so a repeat does not survive the
// keyboard being closed and reopened.
func vkResetNavRepeat() {
	vkNavRepeat = navRepeatTracker{}
}

func initKeyboard() {
	keyboardUpper = false
	vkLayer = vkLayerLetters
	keyboard = vkRows()
	vkLayoutSig = ""
	vkEnsureLayout()
}

// --- Rendering --------------------------------------------------------------

func vkEaseOut(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	return 1 - (1-t)*(1-t)*(1-t)
}

// ──────────────────────────────────────────────────────────────────────
// Static layer caches (keyboard sheet, chat panel, video overlay)
// ──────────────────────────────────────────────────────────────────────

// Only the focused key changes while navigating. Redrawing the sheet, header,
// footer and all ~40 key caps every frame was the most expensive part of typing
// on the handheld, so the static layer is rendered once into a texture and
// reused until the layout, screen, theme or header text changes (vkui.go). A
// full-screen texture is used so the cached pixels live in the same coordinate
// space as the renderer's logical size (a smaller target would be scaled by
// SDL's logical-size transform). The field, the strip and the focus stay live
// because they follow typing.
//
// vkStaticLayerState keys the chat panel and video overlay caches below. It
// is compared every frame, so it holds only colours and a rect: the keyboard's
// title and hint strings it used to carry were rebuilt (and allocated) on
// every frame of video playback for no reason.
type vkStaticLayerState struct {
	layout    string
	panel     sdl.Rect
	panelFill sdl.Color
	shadow    sdl.Color
	icon      sdl.Color
	raised    sdl.Color
	text      sdl.Color
	text2     sdl.Color
	text3     sdl.Color
	border    sdl.Color
	highlight sdl.Color
}

var (
	vkStaticTex    *sdl.Texture
	vkStaticOwner  *sdl.Renderer
	vkStaticState  vkSheetState
	vkStaticValid  bool
	vkStaticBroken bool // renderer has no render-target support: draw live
	vkStaticBuilds int  // rebuild count, used by tests and diagnostics
)

func vkStaticLayerStateNow(panel sdl.Rect) vkStaticLayerState {
	return vkStaticLayerState{
		layout:    vkLayoutSig,
		panel:     panel,
		panelFill: ColorSurfacePanel,
		shadow:    ColorShadow,
		icon:      ColorIconSurface,
		raised:    ColorSurfaceRaised,
		text:      ColorTextPrimary(),
		text2:     ColorTextSecondary(),
		text3:     ColorTextTertiary(),
		border:    ColorBorderSubtle,
		highlight: vkFocusColor(),
	}
}

// DestroyVKStaticCache frees the cached static layer. It is safe to call when
// no cache exists and is used on shutdown and on layout/theme changes.
func DestroyVKStaticCache() {
	if vkStaticTex != nil {
		vkStaticTex.Destroy()
		vkStaticTex = nil
	}
	vkStaticOwner = nil
	vkStaticValid = false
	vkStaticBroken = false
}

// vkForgetForeignTexture drops a cached texture that belongs to a different
// renderer without destroying it.
//
// SDL frees every texture with the renderer that created it. Once that
// renderer is gone the pointer is dangling: calling Destroy on it corrupts the
// allocator, and drawing with it fails later inside an unrelated SDL call (the
// intermittent SDL_RenderReadPixels crash the test suite kept hitting). A
// rebuild is cheap by comparison, so a cache from another renderer is simply
// forgotten.
func vkForgetForeignTexture(renderer *sdl.Renderer, tex **sdl.Texture, owner **sdl.Renderer, valid *bool) {
	if renderer == nil || tex == nil || *tex == nil || owner == nil || *owner == nil || *owner == renderer {
		return
	}
	*tex = nil
	*owner = nil
	if valid != nil {
		*valid = false
	}
}

// Chat cache state and ensure function
var (
	vkChatStaticTex    *sdl.Texture
	vkChatStaticOwner  *sdl.Renderer
	vkChatStaticState  vkStaticLayerState
	vkChatStaticValid  bool
	vkChatStaticBroken bool // renderer has no render-target support: draw live
	vkChatStaticBuilds int  // rebuild count, used by tests and diagnostics
)

func DestroyVKChatCache() {
	if vkChatStaticTex != nil {
		vkChatStaticTex.Destroy()
		vkChatStaticTex = nil
	}
	vkChatStaticOwner = nil
	vkChatStaticValid = false
	vkChatStaticBroken = false
}

// Video overlay cache state and ensure function
var (
	vkVideoOverlayTex    *sdl.Texture
	vkVideoOverlayOwner  *sdl.Renderer
	vkVideoOverlayState  vkStaticLayerState
	vkVideoOverlayValid  bool
	vkVideoOverlayBroken bool // renderer has no render-target support: draw live
	vkVideoOverlayBuilds int  // rebuild count, used by tests and diagnostics
	vkVideoOverlayW      int32
	vkVideoOverlayH      int32
)

func DestroyVKVideoOverlayCache() {
	if vkVideoOverlayTex != nil {
		vkVideoOverlayTex.Destroy()
		vkVideoOverlayTex = nil
	}
	vkVideoOverlayOwner = nil
	vkVideoOverlayValid = false
	vkVideoOverlayBroken = false
	vkVideoOverlayW, vkVideoOverlayH = 0, 0
}

// cachedVideoOverlayBytes reports the memory held by the cached static video
// overlay layer, for the texture-accounting log.
func cachedVideoOverlayBytes() int64 {
	if vkVideoOverlayTex == nil {
		return 0
	}
	return int64(vkVideoOverlayW) * int64(vkVideoOverlayH) * 4
}

// vkEnsureVideoOverlayCache returns the cached static layer of the video
// overlay (gradients, control bar, seek track) sized to the video element,
// rebuilding it only when the element size or theme changes. The parts that
// change while a video plays (badge, progress, transport buttons, time) are
// drawn every frame by drawVideoOverlayLive on top of this layer, so the cache
// must never bake them in.
func vkEnsureVideoOverlayCache(renderer *sdl.Renderer, config *Config, w, h int32) *sdl.Texture {
	vkForgetForeignTexture(renderer, &vkVideoOverlayTex, &vkVideoOverlayOwner, &vkVideoOverlayValid)
	state := vkStaticLayerStateNow(sdl.Rect{X: 0, Y: 0, W: w, H: h})
	if vkVideoOverlayValid && vkVideoOverlayTex != nil && vkVideoOverlayState == state {
		return vkVideoOverlayTex
	}
	// Checked before the destroy below (which clears the flag): a renderer
	// without render-target support must not retry and log every frame.
	if vkVideoOverlayBroken || w <= 0 || h <= 0 {
		return nil
	}
	DestroyVKVideoOverlayCache()

	tex, err := renderer.CreateTexture(0, sdl.TEXTUREACCESS_TARGET, w, h)
	if err != nil || tex == nil {
		vkVideoOverlayBroken = true
		logWarn("vk", "video overlay cache disabled: %v", err)
		return nil
	}
	if err := tex.SetBlendMode(sdl.BLENDMODE_BLEND); err != nil {
		tex.Destroy()
		vkVideoOverlayBroken = true
		logWarn("vk", "video overlay cache blend mode: %v", err)
		return nil
	}

	prevTarget := renderer.GetRenderTarget()
	var prevBlend sdl.BlendMode
	_ = renderer.GetDrawBlendMode(&prevBlend)
	if err := renderer.SetRenderTarget(tex); err != nil {
		tex.Destroy()
		vkVideoOverlayBroken = true
		logWarn("vk", "video overlay cache target: %v", err)
		return nil
	}
	// The overlay is drawn last, so no clip should be active; make sure the
	// cached layer is not clipped by a leftover scene rect.
	_ = renderer.SetClipRect(nil)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 0)
	renderer.Clear()
	// Only the unchanging layer goes in the cache, in local coordinates
	// because the texture is exactly the size of the video element.
	drawVideoOverlayStatic(renderer, 0, 0, w, h)
	_ = renderer.SetRenderTarget(prevTarget)
	_ = renderer.SetDrawBlendMode(prevBlend)
	vkVideoOverlayTex = tex
	vkVideoOverlayOwner = renderer
	vkVideoOverlayState = state
	vkVideoOverlayW, vkVideoOverlayH = w, h
	vkVideoOverlayValid = true
	vkVideoOverlayBuilds++
	return tex
}
