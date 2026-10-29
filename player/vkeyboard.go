package main

// On-screen keyboard.
//
// Design goals (Xbox-style):
//   * one bottom-anchored panel that never covers the whole screen,
//   * a real 10-column grid with a digits row, punctuation, shift and a
//     dedicated primary action key,
//   * flat keys on themed surfaces with a single high-contrast focus fill,
//   * a large readable text field with a blinking caret instead of the old
//     "text plus underscore" preview.
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
	"log"
	"strings"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
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
	vkTargetToolArgs   // toolbox: arguments for the highlighted tool
)

// vkNameBuffer holds the chat display name while it is being edited; it is
// committed to the user config when the keyboard submits.
var vkNameBuffer string

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

// vkFocusColor is the fill for the focused key and the primary action key.
// installHighlight() falls back to pure white on dark accents, which turned one
// key into a white blob in the middle of the keyboard; lifting the accent
// instead keeps the keyboard on-theme and still readable.
func vkFocusColor() sdl.Color {
	base := ColorIconSurface // the unfocused key cap
	lum := func(c sdl.Color) int { return int(colorLuma(c)) }
	h := accentColor
	if vkAbsDiff(lum(h), lum(base)) < 60 {
		// Blend toward the theme's info colour: a dark accent lifted straight
		// to grey made the focused key look disabled.
		h = lerpColor(accentColor, ColorInfo, 0.65)
	}
	if vkAbsDiff(lum(h), lum(base)) < 45 {
		h = ColorInfo
	}
	if vkAbsDiff(lum(h), lum(base)) < 45 {
		h = sdl.Color{R: 96, G: 165, B: 250, A: 255}
	}
	// The focus fill must be opaque: with alpha 0 the cap under the ring would
	// stay transparent and the whole key would read as the ring colour.
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
// actions in vkKeyValue / vkActivateFocused.
func vkRows() [][]string {
	chat := vkTarget == vkTargetChat
	action := "ENTER"
	if chat {
		action = "SEND"
	}
	bottom := []string{"123", ",", "SPACE", ".", action}
	if vkLayer == vkLayerSymbols {
		bottom[0] = "ABC"
	}
	if chat {
		bottom = append(bottom, "ASK_AI")
	}

	if vkLayer == vkLayerSymbols {
		return [][]string{
			{"1", "2", "3", "4", "5", "6", "7", "8", "9", "0"},
			{"!", "@", "#", "$", "%", "&", "*", "(", ")", "-"},
			{"+", "=", "/", "?", ":", ";", "_", "\"", "'", "`"},
			{"~", "<", ">", "[", "]", "{", "}", "\\", "|", "BACK"},
			bottom,
		}
	}

	letters := []string{"q", "w", "e", "r", "t", "y", "u", "i", "o", "p"}
	middle := []string{"a", "s", "d", "f", "g", "h", "j", "k", "l"}
	lower := []string{"z", "x", "c", "v", "b", "n", "m"}
	if keyboardUpper {
		for i := range letters {
			letters[i] = strings.ToUpper(letters[i])
		}
		for i := range middle {
			middle[i] = strings.ToUpper(middle[i])
		}
		for i := range lower {
			lower[i] = strings.ToUpper(lower[i])
		}
	}
	third := append([]string{"⇧"}, lower...)
	third = append(third, "BACK")

	return [][]string{
		{"1", "2", "3", "4", "5", "6", "7", "8", "9", "0"},
		letters,
		middle,
		third,
		bottom,
	}
}

// vkKeyUnits is the key width in grid units (1 unit = one letter key).
func vkKeyUnits(key string) float64 {
	switch key {
	case "SPACE":
		return 3
	case "⇧", "BACK", "123", "ABC":
		return 1.4
	case "ENTER":
		return 2.2
	case "SEND", "ASK_AI":
		return 2
	}
	return 1
}

// vkKeyLabel is the text drawn on a key. Action keys get friendly words
// instead of internal identifiers; glyph-only labels are avoided because not
// every bundled font has them.
func vkKeyLabel(key string) string {
	switch key {
	case "SPACE":
		return "space"
	case "BACK":
		return "DEL"
	case "ENTER":
		return "Done"
	case "SEND":
		return "Send"
	case "ASK_AI":
		return "Ask AI"
	case "⇧":
		return "SHIFT"
	}
	return key
}

// vkKeyValue returns the text inserted by a printable key.
func vkKeyValue(key string) (string, bool) {
	switch key {
	case "⇧", "BACK", "ENTER", "SEND", "ASK_AI", "123", "ABC":
		return "", false
	case "SPACE":
		return " ", true
	}
	return key, true
}

// vkIsActionKey reports whether activating the key submits and closes.
func vkIsActionKey(key string) bool {
	return key == "ENTER" || key == "SEND" || key == "ASK_AI"
}

// --- Geometry ---------------------------------------------------------------

func vkScreenSmall() bool { return screenHeight < 640 || screenWidth < 900 }

// vkEnsureLayout rebuilds the key grid and its rectangles when the layer, case,
// target or screen size changed. Geometry is font-independent, which keeps it
// testable without a renderer.
func vkEnsureLayout() {
	target := 0
	switch vkTarget {
	case vkTargetChat:
		target = 1
	case vkTargetUnit:
		target = 2
	case vkTargetTV:
		target = 3
	case vkTargetParty:
		target = 4
	case vkTargetRA:
		target = 5
	case vkTargetMusic:
		target = 6
	case vkTargetPSRemote:
		target = 7
	case vkTargetGameSearch:
		target = 8
	case vkTargetDownloads:
		target = 9
	case vkTargetGameArgs:
		target = 10
	case vkTargetToolArgs:
		target = 11
	}
	sig := fmt.Sprintf("%dx%d|%d|%d|%t|%d", screenWidth, screenHeight, int(vkLayer), target, keyboardUpper, len(keyboard))
	if sig == vkLayoutSig && len(keyboardRects) > 0 {
		return
	}
	keyboard = vkRows()
	vkLayoutSig = sig
	vkBuildGeometry()
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
	pad := int32(20)
	gap := int32(10)
	keyH := int32(52)
	fieldH := int32(50)
	headerH := int32(40)
	topGap := int32(14)
	footerGap := int32(14)
	footerH := int32(24)
	marginX := int32(34)
	marginY := int32(20)
	if small {
		pad = 14
		gap = 8
		keyH = 42
		fieldH = 42
		headerH = 32
		topGap = 10
		footerGap = 10
		footerH = 20
		marginX = 18
		marginY = 12
	}

	availW := screenWidth - 2*marginX
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

	panelW := innerW + 2*pad
	keysH := int32(len(rows))*keyH + int32(len(rows)-1)*gap
	panelH := pad + headerH + fieldH + topGap + keysH + footerGap + footerH + pad
	panelX := (screenWidth - panelW) / 2
	panelY := screenHeight - marginY - panelH
	if panelY < marginY {
		panelY = marginY
	}

	vkPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	vkFieldRect = sdl.Rect{
		X: panelX + pad,
		Y: panelY + pad + headerH,
		W: innerW,
		H: fieldH,
	}
	closeSize := headerH
	if closeSize < 32 {
		closeSize = 32
	}
	vkCloseRect = sdl.Rect{
		X: panelX + panelW - pad - closeSize,
		Y: panelY + pad + (headerH-closeSize)/2,
		W: closeSize,
		H: closeSize,
	}

	keyboardRects = make([]sdl.Rect, 0, 64)
	keyboardKeys = make([]string, 0, 64)
	keyboardRowCol = make([][2]int, 0, 64)
	keysTop := vkFieldRect.Y + fieldH + topGap
	for r, row := range rows {
		rx := panelX + pad + (innerW-rowWidths[r])/2
		ky := keysTop + int32(r)*(keyH+gap)
		for c := range row {
			w := widths[r][c]
			keyboardRects = append(keyboardRects, sdl.Rect{X: rx, Y: ky, W: w, H: keyH})
			keyboardKeys = append(keyboardKeys, row[c])
			keyboardRowCol = append(keyboardRowCol, [2]int{r, c})
			rx += w + gap
		}
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

// vkCloseHit reports whether the close button was pressed.
func vkCloseHit(mx, my int32) bool {
	vkEnsureLayout()
	if vkCloseRect.W <= 0 {
		return false
	}
	return pointInRect(mx, my-vkAnimOffset(), vkCloseRect)
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

// vkHandleTouchEvent turns a tap on the keyboard into the same action as a
// click. Touch used to be ignored entirely while the keyboard was open (and
// swipes changed the scene behind it), so the panel was unusable on touch-only
// devices and easy to dismiss by accident.
func vkHandleTouchEvent(e *sdl.TouchFingerEvent, config *Config) {
	if e == nil || e.Type != sdl.FINGERDOWN {
		return
	}
	mx := int32(float64(e.X) * float64(screenWidth))
	my := int32(float64(e.Y) * float64(screenHeight))
	if vkCloseHit(mx, my) {
		vkCloseKeyboard()
		return
	}
	if row, col, ok := vkHitTest(mx, my); ok {
		keyboardPosY, keyboardPosX = row, col
		vkActivateFocused(config)
		return
	}
	if vkOutsidePanel(mx, my) {
		vkCloseKeyboard()
	}
}

// --- Target / text routing --------------------------------------------------

// vkCurrentText is the buffer the keyboard is editing.
func vkCurrentText() string {
	switch vkTarget {
	case vkTargetChat:
		return chatInputText
	case vkTargetName:
		return vkNameBuffer
	case vkTargetUnit:
		return unitInputValue
	case vkTargetTerm:
		return term.input
	case vkTargetTV:
		return tvQuery()
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
		return gameLibraryQuery()
	case vkTargetDownloads:
		return downloadLinkInput
	case vkTargetGameArgs:
		return gameArgsBuffer
	case vkTargetToolArgs:
		return toolArgsBuffer
	}
	return inputTextBuffer
}

func vkSetText(text string, config *Config) {
	switch vkTarget {
	case vkTargetChat:
		chatInputText = text
	case vkTargetName:
		vkNameBuffer = text
	case vkTargetUnit:
		unitInputValue = text
	case vkTargetTerm:
		term.input = text
		term.cursor = len(text)
		term.histIdx = -1
	case vkTargetTV:
		// The list re-filters on every keystroke, so the user sees matches
		// appear while typing instead of after submitting.
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
		setGameLibraryQuery(text)
	case vkTargetDownloads:
		// The link is only used when the field is submitted, so a mistyped
		// character cannot start a download.
		downloadLinkInput = text
	case vkTargetGameArgs:
		// Edited in its own buffer; the game's options change only on submit.
		gameArgsBuffer = text
	case vkTargetToolArgs:
		// Same rule for the toolbox: the saved argument line changes on submit,
		// so a half-typed `--delete` can never reach the tool.
		toolArgsBuffer = text
	default:
		inputTextBuffer = text
		updateInputVariable(config)
	}
}

const vkMaxTextLen = 2000

// vkInsertText appends text to the active target exactly once. Control
// characters other than newline and tab are dropped: an SDL text event carries
// a fixed-size C buffer, and letting its NUL padding into the field made the
// renderer truncate at the first key (and backspace delete invisible bytes).
func vkInsertText(text string, config *Config) {
	if text == "" {
		return
	}
	if strings.IndexByte(text, 0) >= 0 || strings.IndexFunc(text, func(r rune) bool {
		return r < 0x20 && r != '\n' && r != '\t'
	}) >= 0 {
		text = strings.Map(func(r rune) rune {
			if r < 0x20 && r != '\n' && r != '\t' {
				return -1
			}
			return r
		}, text)
	}
	if text == "" {
		return
	}
	// A field that opened with its old value selected behaves like a browser
	// address bar: the first character typed replaces the whole thing.
	if vkTextSelected {
		vkSetText("", config)
		vkTextSelected = false
	}
	current := vkCurrentText()
	if len(current) >= vkMaxTextLen {
		return
	}
	vkSetText(current+text, config)
}

// vkBackspace removes the last rune (not byte) so multi-byte text survives.
func vkBackspace(config *Config) {
	if vkTextSelected {
		vkSetText("", config)
		vkTextSelected = false
		return
	}
	current := vkCurrentText()
	if current == "" {
		return
	}
	vkSetText(trimLastRune(current), config)
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
		return "MESSAGE"
	case vkTargetUnit:
		return "NUMBER"
	case vkTargetTerm:
		return "COMMAND"
	case vkTargetTV:
		return "CHANNEL SEARCH"
	case vkTargetParty:
		if partyUI.inputMode == partyInputQueue {
			return "SHARE A LINK"
		}
		return "PARTY INVITE"
	case vkTargetRA:
		if raPanel.edit == raEditAPIKey {
			return "WEB API KEY"
		}
		return "RETROACHIEVEMENTS USERNAME"
	case vkTargetMusic:
		return "SEARCH SONGS"
	case vkTargetGameSearch:
		return "SEARCH GAMES"
	case vkTargetDownloads:
		return "SAVE TO SD CARD"
	case vkTargetGameArgs:
		return "LAUNCH ARGUMENTS"
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
		}
		return "CONSOLE ADDRESS"
	case vkTargetWiFi:
		return "WI-FI PASSWORD"
	case vkTargetMemory:
		return "VALUE"
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
		return "Message Juka…"
	case vkTargetUnit:
		return "Enter a number"
	case vkTargetTerm:
		return "Type a command"
	case vkTargetTV:
		return "Search channels"
	case vkTargetParty:
		if partyUI.inputMode == partyInputQueue {
			return "https://…"
		}
		return "192.168.1.5:8318 ABCDE"
	case vkTargetRA:
		if raPanel.edit == raEditAPIKey {
			return "32 letters or digits"
		}
		return "Your RetroAchievements username"
	case vkTargetMusic:
		return "Type initials: iwyn = I Want You Now"
	case vkTargetGameSearch:
		return "Title, console or file: zelda"
	case vkTargetDownloads:
		return "https://www.youtube.com/watch?v=…"
	case vkTargetGameArgs:
		return "--profile \"My Game\""
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
		}
		return "192.168.1.51"
	case vkTargetWiFi:
		return "At least 8 characters"
	case vkTargetMemory:
		return "Decimal, or 0x for hex"
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

func vkHints() string {
	switch vkTarget {
	case vkTargetName, vkTargetRA, vkTargetPSRemote:
		if vkTarget == vkTargetPSRemote && psPanelEditField == psEditPIN {
			return hintLine(FooterHint{"A", "Type"}, FooterHint{"A", "Ready to Pair"}, FooterHint{"B / Esc", "Close"})
		}
		return hintLine(FooterHint{"A", "Type"}, FooterHint{"Y", "Save"}, FooterHint{"B / Esc", "Close"})
	case vkTargetChat:
		return hintLine(FooterHint{"A", "Type"}, FooterHint{"Y", "Ask AI"}, FooterHint{"B / Esc", "Close"})
	case vkTargetUnit:
		return hintLine(FooterHint{"A", "Type"}, FooterHint{"Y", "Convert"}, FooterHint{"B / Esc", "Close"})
	case vkTargetTerm:
		return hintLine(FooterHint{"A", "Type"}, FooterHint{"Done", "Run"}, FooterHint{"B / Esc", "Close"})
	case vkTargetTV:
		return hintLine(FooterHint{"A", "Type"}, FooterHint{Label: "Live Filter"}, FooterHint{"B / Esc", "Close"})
	case vkTargetMusic:
		return hintLine(FooterHint{Label: "Type Initials"}, FooterHint{"A / Y", "Search"}, FooterHint{"B / Esc", "Close"})
	case vkTargetNetplay:
		return hintLine(FooterHint{"A", "Type"}, FooterHint{"Start", "Join"}, FooterHint{"B / Esc", "Close"})
	case vkTargetMemory:
		return hintLine(FooterHint{"ENTER", "Use Value"}, FooterHint{"123", "Digits"}, FooterHint{"B / Esc", "Cancel"})
	case vkTargetGameSearch:
		return hintLine(FooterHint{Label: "Live Filter"}, FooterHint{"A / Enter", "Done"}, FooterHint{"B / Esc", "Close"})
	case vkTargetGameArgs:
		return hintLine(FooterHint{Label: "Quotes Group Words"}, FooterHint{"A / Enter", "Save"}, FooterHint{"B / Esc", "Close"})
	case vkTargetToolArgs:
		return hintLine(FooterHint{Label: "Quotes Group Words"}, FooterHint{"A / Enter", "Run"}, FooterHint{"B / Esc", "Cancel"})
	}
	return hintLine(FooterHint{"A", "Type"}, FooterHint{"Y", "Save"}, FooterHint{"B / Esc", "Close"})
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
	virtualKeyboardActive = false
	vkTextSelected = false
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	vkAnimStart = 0
	vkResetNavRepeat()
	vkClearPressFlash()
	if vkTarget == vkTargetWiFi {
		// A dismissed password field must not keep the secret around.
		wifiPassphraseBuffer = ""
		wifiPendingSSID = ""
	}
	if vkTarget == vkTargetScene {
		activeSceneIndex = -1
		activeElementIndex = -1
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

func vkActivateFocused(config *Config) {
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
	case "BACK":
		vkBackspace(config)
	case "ENTER", "SEND":
		vkSubmit(config, false)
	case "ASK_AI":
		vkSubmit(config, true)
	default:
		if value, ok := vkKeyValue(key); ok {
			vkInsertText(value, config)
		}
	}
	vkEnsureLayout()
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
	mx, my, _ := sdl.GetMouseState()
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
func vkHandleControllerButton(button uint8, config *Config) bool {
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
		vkActivateFocused(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		vkCloseKeyboard()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		toggleKeyboardCase()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		vkToggleLayer()
	case sdl.CONTROLLER_BUTTON_Y:
		// Y is the secondary submit: Ask AI in chat, Done everywhere else.
		vkSubmit(config, vkTarget == vkTargetChat)
	default:
		return false
	}
	return true
}

// vkMoveFocus moves the highlight by (dx, dy) with clamping.
func vkMoveFocus(dx, dy int) {
	if len(keyboard) == 0 {
		return
	}
	vkEnsureLayout()
	if keyboardPosY < 0 || keyboardPosY >= len(keyboard) {
		keyboardPosY, keyboardPosX = 0, 0
	}
	if dy != 0 {
		ny := keyboardPosY + dy
		if ny >= 0 && ny < len(keyboard) {
			keyboardPosY = ny
			if keyboardPosX >= len(keyboard[ny]) {
				keyboardPosX = len(keyboard[ny]) - 1
			}
		}
	}
	if dx != 0 {
		nx := keyboardPosX + dx
		if nx >= 0 && nx < len(keyboard[keyboardPosY]) {
			keyboardPosX = nx
		}
	}
	if keyboardPosX < 0 {
		keyboardPosX = 0
	}
}

// vkSubmit commits the active target and closes the keyboard.
func vkSubmit(config *Config, askAI bool) {
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
	case vkTargetDownloads:
		// Submit before closing: closing is what clears the field, and an
		// emptied link must not become a queued row.
		downloadLinkSubmit(text)
		vkCloseKeyboard()
	case vkTargetTerm:
		vkSetText("", config)
		vkCloseKeyboard()
		if text != "" {
			termExecute(text)
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
	case vkTargetChat:
		vkSetText("", config)
		// Close first: sending writes the chat log to the SD card (and may
		// queue a network call), and the panel used to stay on screen until
		// that finished, which looked like the keyboard ignoring the button.
		vkCloseKeyboard()
		if text != "" {
			if askAI {
				// sendGroqChatMessage records the user bubble itself.
				sendGroqChatMessage(text)
			} else {
				sendChatMessage(chatDisplayName(), text)
			}
		}
	default:
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
	executeYouTubeSearch(config, ytSearchCmd(config), "search_results", snapshotVars(config))
}

// toggleKeyboardCase flips letter case without losing the focused key.
func toggleKeyboardCase() {
	keyboardUpper = !keyboardUpper
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

// vkControllerNavDirection reads the D-pad that is held right now. Physical
// arrow keys are left to SDL's own key repeat; polling them too would double
// the rate of a desktop hold.
func vkControllerNavDirection() (int, int) {
	if gameController == nil {
		return 0, 0
	}
	dx, dy := 0, 0
	if gameController.Button(sdl.CONTROLLER_BUTTON_DPAD_LEFT) != 0 {
		dx = -1
	}
	if gameController.Button(sdl.CONTROLLER_BUTTON_DPAD_RIGHT) != 0 {
		dx = 1
	}
	if gameController.Button(sdl.CONTROLLER_BUTTON_DPAD_UP) != 0 {
		dy = -1
	}
	if gameController.Button(sdl.CONTROLLER_BUTTON_DPAD_DOWN) != 0 {
		dy = 1
	}
	return dx, dy
}

// vkTickNavRepeat advances the held-D-pad state. The main loop and the modal
// input loop call it once per frame while the keyboard is open.
func vkTickNavRepeat() {
	if !virtualKeyboardActive {
		vkNavRepeat = navRepeatTracker{}
		return
	}
	dx, dy := vkControllerNavDirection()
	if stepX, stepY := vkNavRepeat.step(sdl.GetTicks64(), dx, dy); stepX != 0 || stepY != 0 {
		vkMoveFocus(stepX, stepY)
	}
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
// Static layer cache (panel, header, footer, keys)
// ──────────────────────────────────────────────────────────────────────

// Only the focused key changes while navigating. Redrawing the panel, header,
// footer and all ~40 key caps every frame was the most expensive part of typing
// on the handheld, so the static layer is rendered once into a texture and
// reused until the layout, screen, theme or header/footer text changes. A
// full-screen texture is
// used so the cached pixels live in the same coordinate space as the renderer's
// logical size (a smaller target would be scaled by SDL's logical-size
// transform). The text field stays live because it follows typing.
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
	title     string
	hints     string
}

var (
	vkStaticTex    *sdl.Texture
	vkStaticOwner  *sdl.Renderer
	vkStaticState  vkStaticLayerState
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
		title:     vkTitle(),
		hints:     vkHints(),
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
		log.Printf("[vk] video overlay cache disabled: %v", err)
		return nil
	}
	if err := tex.SetBlendMode(sdl.BLENDMODE_BLEND); err != nil {
		tex.Destroy()
		vkVideoOverlayBroken = true
		log.Printf("[vk] video overlay cache blend mode: %v", err)
		return nil
	}

	prevTarget := renderer.GetRenderTarget()
	var prevBlend sdl.BlendMode
	_ = renderer.GetDrawBlendMode(&prevBlend)
	if err := renderer.SetRenderTarget(tex); err != nil {
		tex.Destroy()
		vkVideoOverlayBroken = true
		log.Printf("[vk] video overlay cache target: %v", err)
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

// vkEnsureChatCache returns the cached chat panel + status bar,
// rebuilding it only when the panel geometry, screen dimensions, theme colours
// or header/footer text changed. The cache is drawn once per frame and only the
// message bubbles (which change with each message) are redrawn on top.
func vkEnsureChatCache(renderer *sdl.Renderer, config *Config, listX, listY, listW, listH int32) *sdl.Texture {
	vkForgetForeignTexture(renderer, &vkChatStaticTex, &vkChatStaticOwner, &vkChatStaticValid)
	state := vkStaticLayerStateNow(sdl.Rect{X: listX, Y: listY, W: listW, H: listH})
	if vkChatStaticValid && vkChatStaticTex != nil && vkChatStaticState == state {
		return vkChatStaticTex
	}
	// Checked before the destroy below (which clears the flag): a renderer
	// without render-target support must not retry and log every frame.
	if vkChatStaticBroken || listW <= 0 || listH <= 0 {
		return nil
	}
	statusFont, _ := getCachedFont(config, "small")
	if statusFont == nil {
		return nil
	}
	DestroyVKChatCache()

	tex, err := renderer.CreateTexture(0, sdl.TEXTUREACCESS_TARGET, listW, listH)
	if err != nil || tex == nil {
		vkChatStaticBroken = true
		log.Printf("[vk] chat cache disabled: %v", err)
		return nil
	}
	if err := tex.SetBlendMode(sdl.BLENDMODE_BLEND); err != nil {
		tex.Destroy()
		vkChatStaticBroken = true
		log.Printf("[vk] chat cache blend mode: %v", err)
		return nil
	}
	prevTarget := renderer.GetRenderTarget()
	var prevBlend sdl.BlendMode
	_ = renderer.GetDrawBlendMode(&prevBlend)
	if err := renderer.SetRenderTarget(tex); err != nil {
		tex.Destroy()
		vkChatStaticBroken = true
		log.Printf("[vk] chat cache target: %v", err)
		return nil
	}
	// The chat is drawn last, so no clip should be active; make sure the
	// cached layer is not clipped by a leftover scene rect.
	_ = renderer.SetClipRect(nil)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 0)
	renderer.Clear()
	// Panel background with subtle border.
	fillRoundedRect(renderer, listX, listY, listW, listH, RadiusMD, ColorSurfaceRaised)
	renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 120)
	renderer.DrawRect(&sdl.Rect{X: listX + 1, Y: listY + 1, W: listW - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: listX + 1, Y: listY + 1, W: 1, H: listH - 2})
	renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 60)
	renderer.DrawRect(&sdl.Rect{X: listX + 1, Y: listY + listH - 2, W: listW - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: listX + listW - 2, Y: listY + 1, W: 1, H: listH - 2})
	// Header.
	const headerH = int32(44)
	vkDrawPanel(renderer, sdl.Rect{X: listX, Y: listY, W: listW, H: headerH})
	// Status bar area.
	statusY := listY + int32(8)
	statusRight := listX + listW - int32(12)
	if discordStatus != "" {
		dsw, _, _ := statusFont.SizeUTF8(discordStatus)
		statusRight -= int32(dsw) + int32(8)
		renderText(renderer, config, statusFont, discordStatus, ColorTextTertiary(), statusRight, statusY)
		statusRight -= int32(8)
	}
	if groqStatus != "" {
		gw, _, _ := statusFont.SizeUTF8(groqStatus)
		groqX := statusRight - int32(gw) - int32(8)
		if groqX < listX+int32(12) {
			groqX = listX + int32(12)
		}
		renderText(renderer, config, statusFont, groqStatus, groqStatusColor(), groqX, statusY)
	}
	// Divider under status.
	dividerY := statusY + int32(statusFont.Height()) + int32(2)
	renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 80)
	renderer.FillRect(&sdl.Rect{X: listX + int32(12), Y: dividerY, W: listW - 2*int32(12), H: 1})
	_ = renderer.SetRenderTarget(prevTarget)
	_ = renderer.SetDrawBlendMode(prevBlend)
	vkChatStaticState = state
	vkChatStaticTex = tex
	vkChatStaticOwner = renderer
	vkChatStaticValid = true
	vkChatStaticBuilds++
	return tex
}

func vkEnsureStaticLayer(renderer *sdl.Renderer, config *Config, font, smallFont *ttf.Font) *sdl.Texture {
	vkForgetForeignTexture(renderer, &vkStaticTex, &vkStaticOwner, &vkStaticValid)
	state := vkStaticLayerStateNow(vkPanelRect)
	if vkStaticValid && vkStaticTex != nil && vkStaticState == state {
		return vkStaticTex
	}
	// Checked before the destroy below (which clears the flag): a renderer
	// without render-target support must not retry and log every frame.
	if vkStaticBroken || screenWidth <= 0 || screenHeight <= 0 {
		return nil
	}
	DestroyVKStaticCache()

	tex, err := renderer.CreateTexture(0, sdl.TEXTUREACCESS_TARGET, screenWidth, screenHeight)
	if err != nil || tex == nil {
		vkStaticBroken = true
		log.Printf("[vk] keyboard cache disabled: %v", err)
		return nil
	}
	if err := tex.SetBlendMode(sdl.BLENDMODE_BLEND); err != nil {
		tex.Destroy()
		vkStaticBroken = true
		log.Printf("[vk] keyboard cache blend mode: %v", err)
		return nil
	}

	prevTarget := renderer.GetRenderTarget()
	var prevBlend sdl.BlendMode
	_ = renderer.GetDrawBlendMode(&prevBlend)
	if err := renderer.SetRenderTarget(tex); err != nil {
		tex.Destroy()
		vkStaticBroken = true
		log.Printf("[vk] keyboard cache target: %v", err)
		return nil
	}
	// The keyboard is drawn last, so no clip should be active; make sure the
	// cached layer is not clipped by a leftover scene rect.
	_ = renderer.SetClipRect(nil)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 0)
	renderer.Clear()
	// Panel, header and footer never change while navigating, and every cap is
	// rendered in its resting state: focus is an overlay.
	vkDrawPanel(renderer, vkPanelRect)
	vkDrawHeader(renderer, config, smallFont, vkPanelRect, vkCloseRect)
	vkDrawKeys(renderer, config, font, 0, false)
	vkDrawFooter(renderer, config, smallFont, vkPanelRect)

	_ = renderer.SetRenderTarget(prevTarget)
	_ = renderer.SetDrawBlendMode(prevBlend)
	vkStaticTex = tex
	vkStaticOwner = renderer
	vkStaticState = state
	vkStaticValid = true
	vkStaticBuilds++
	return tex
}

// renderKeyboard draws the panel when the keyboard is open.
func renderKeyboard(renderer *sdl.Renderer, config *Config) {
	if !virtualKeyboardActive {
		return
	}
	vkEnsureLayout()

	// Short rise-in animation (120ms) so the panel slides up from the edge.
	offset := vkAnimOffset()

	panel := vkPanelRect
	panel.Y += offset
	field := vkFieldRect
	field.Y += offset
	closeBtn := vkCloseRect
	closeBtn.Y += offset

	// Dim the content behind the panel, fading in with the slide so the screen
	// does not go dark a frame before the panel arrives.
	progress := vkOpenProgress()
	fillRoundedRect(renderer, 0, 0, screenWidth, screenHeight, 0, WithAlpha(ColorBackground, uint8(float64(190)*progress)))
	fillRoundedRect(renderer, 0, 0, screenWidth, panel.Y, 0, WithAlpha(ColorBackground, uint8(float64(60)*progress)))

	font, _ := getCachedFont(config, "medium")
	if font == nil {
		font, _ = getCachedFont(config, "small")
	}
	smallFont, _ := getCachedFont(config, "small")
	if smallFont == nil {
		smallFont = font
	}

	if cache := vkEnsureStaticLayer(renderer, config, font, smallFont); cache != nil {
		// Once per navigation frame: copy the cached layer, then draw only the
		// field (typing) and the cap that moved. The layer is offset as a whole
		// so the slide-in animation also moves panel, header and footer.
		src := sdl.Rect{X: vkPanelRect.X, Y: vkPanelRect.Y, W: vkPanelRect.W, H: vkPanelRect.H}
		dst := sdl.Rect{X: panel.X, Y: panel.Y, W: panel.W, H: panel.H}
		_ = renderer.Copy(cache, &src, &dst)
		vkDrawField(renderer, config, font, field)
		// Mouse affordance: show which cap a click would press. The focused cap
		// keeps its own fill, so hover never competes with it.
		if hover := vkHoverFromPointer(); hover >= 0 && hover != vkFocusedRectIndex() && hover < len(keyboardRects) {
			r := keyboardRects[hover]
			r.Y += offset
			vkDrawHoverKey(renderer, r)
		}
		vkDrawFocusedKey(renderer, config, font, offset)
		// Press feedback on top of everything: the cap the finger just touched
		// reads as pushed in even though the highlight already moved on.
		if pressed, ok := vkPressedRectAt(sdl.GetTicks64()); ok {
			pressed.Y += offset
			vkDrawPressedKey(renderer, pressed)
		}
	} else {
		vkDrawPanel(renderer, panel)
		vkDrawHeader(renderer, config, smallFont, panel, closeBtn)
		vkDrawField(renderer, config, font, field)
		vkDrawKeys(renderer, config, font, offset, true)
		vkDrawFooter(renderer, config, smallFont, panel)
	}
}

// vkDrawPanel paints the panel shadow, body, sheen and hairline borders. It is
// shared by the live path and the cached-layer build so both look identical.
func vkDrawPanel(renderer *sdl.Renderer, panel sdl.Rect) {
	for i := int32(1); i <= 3; i++ {
		fillRoundedRect(renderer, panel.X-i*2, panel.Y+i*3, panel.W, panel.H, RadiusXL+i, ShadowFill(uint8(10+i*6)))
	}
	fillRoundedRect(renderer, panel.X, panel.Y, panel.W, panel.H, RadiusXL, WithAlpha(ColorSurfacePanel, 250))
	renderer.SetDrawColor(255, 255, 255, 14)
	renderer.FillRect(&sdl.Rect{X: panel.X + RadiusXL, Y: panel.Y, W: panel.W - 2*RadiusXL, H: 1})
	renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 120)
	renderer.DrawRect(&sdl.Rect{X: panel.X, Y: panel.Y, W: panel.W, H: panel.H})
	renderer.SetDrawColor(ColorBorderSubtle.R, ColorBorderSubtle.G, ColorBorderSubtle.B, 60)
	renderer.DrawRect(&sdl.Rect{X: panel.X + 1, Y: panel.Y + 1, W: panel.W - 2, H: panel.H - 2})
}

func vkDrawHeader(renderer *sdl.Renderer, config *Config, font *ttf.Font, panel, closeBtn sdl.Rect) {
	if font == nil {
		return
	}
	title := vkTitle()
	x := panel.X + 20
	y := panel.Y + 20
	// Small accent bar + label, the same visual language as section headers.
	fillRoundedRect(renderer, x, y+3, 3, 12, 2, vkFocusColor())
	renderText(renderer, config, font, title, ColorTextTertiary(), x+10, y)

	// Layer/case indicator, right-aligned before the close button. It used to
	// share the footer with the hints and the two collided on small screens.
	mode := "ABC"
	if vkLayer == vkLayerSymbols {
		mode = "123"
	}
	if keyboardUpper {
		mode += " · SHIFT"
	}
	if mw, _, err := font.SizeUTF8(mode); err == nil {
		modeX := closeBtn.X - 12 - int32(mw)
		if modeX > x+10+int32(mustTextWidth(font, title))+12 {
			renderText(renderer, config, font, mode, WithAlpha(ColorTextTertiary(), 200), modeX, y+3)
		}
	}

	// Close button: a full-size tap target with a drawn X. The old 26px dotted
	// cross was both hard to hit and easy to mistake for decoration.
	UIButton(renderer, config, closeBtn, "", UIButtonStyle{
		Variant:  UIButtonCustom,
		Fill:     ColorSurfaceRaised,
		Border:   WithAlpha(ColorBorderSubtle, 140),
		Font:     font,
		Radius:   RadiusSM,
		NoShadow: true,
	}, UIButtonState{})
	cx := closeBtn.X + closeBtn.W/2
	cy := closeBtn.Y + closeBtn.H/2
	// Two passes give the X a 2px stroke so it reads at handheld distance.
	iconCol := WithAlpha(ColorTextSecondary(), 230)
	drawCloseIcon(renderer, cx, cy, closeBtn.W/3, iconCol)
	drawCloseIcon(renderer, cx+1, cy, closeBtn.W/3, iconCol)
}

func vkDrawField(renderer *sdl.Renderer, config *Config, font *ttf.Font, field sdl.Rect) {
	fillRoundedRect(renderer, field.X, field.Y, field.W, field.H, RadiusMD, ColorIconSurface)
	// Focus ring: accent hairline plus a soft outer glow.
	focus := vkFocusColor()
	renderer.SetDrawColor(focus.R, focus.G, focus.B, 70)
	renderer.DrawRect(&sdl.Rect{X: field.X - 1, Y: field.Y - 1, W: field.W + 2, H: field.H + 2})
	renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 160)
	renderer.DrawRect(&sdl.Rect{X: field.X, Y: field.Y, W: field.W, H: field.H})

	if font == nil {
		return
	}
	textH := int32(font.Height())
	padX := int32(16)
	textY := field.Y + (field.H-textH)/2
	maxW := field.W - 2*padX - 8
	text := vkCurrentText()

	if text == "" {
		// A steady caret marks the focused field before the first character;
		// once there is text the caret blinks behind it instead. The placeholder
		// moves aside so the two never overlap.
		caretY := field.Y + field.H/2 - textH/2 + 2
		fillRoundedRect(renderer, field.X+padX, caretY, 2, textH-4, 1, vkFocusColor())
		renderText(renderer, config, font, vkPlaceholder(), ColorTextTertiary(), field.X+padX+10, textY)
	} else {
		display := text
		if (vkTarget == vkTargetRA && raPanel.edit == raEditAPIKey) || vkTarget == vkTargetWiFi {
			// Do not leave the Web API key or a Wi-Fi password readable on the
			// handheld screen while it is being typed or corrected.
			display = strings.Repeat("•", utf8.RuneCountInString(text))
		}
		visible, w := vkFitTail(font, display, maxW)
		if vkTextSelected {
			// The whole value is selected: the first key typed replaces it. The
			// fill says so before the user finds out the hard way.
			fillRoundedRect(renderer, field.X+padX-3, textY-2, w+6, textH+2, 3, WithAlpha(vkFocusColor(), 110))
		}
		renderText(renderer, config, font, visible, ColorTextPrimary(), field.X+padX, textY)
		if !vkTextSelected && uint32(sdl.GetTicks64()/530)%2 == 0 {
			cx := field.X + padX + w + 2
			if cx > field.X+field.W-padX {
				cx = field.X + field.W - padX
			}
			fillRoundedRect(renderer, cx, field.Y+field.H/2-textH/2+2, 2, textH-4, 1, vkFocusColor())
		}
	}

	// Right-hand hint: remaining characters before the cap. A masked secret
	// gets no counter; its limit is the WPA rule, not the field cap.
	remaining := vkMaxTextLen - len(text)
	if vkTarget != vkTargetWiFi && remaining < 200 && remaining >= 0 {
		smallFont, _ := getCachedFont(config, "small")
		if smallFont != nil {
			label := fmt.Sprintf("%d", remaining)
			w, _, _ := smallFont.SizeUTF8(label)
			renderText(renderer, config, smallFont, label, ColorTextTertiary(),
				field.X+field.W-padX-int32(w), textY+(textH-int32(smallFont.Height()))/2)
		}
	}
}

// vkFitTail returns the longest suffix of text that fits maxW, so the caret
// and the newest characters stay visible while typing long queries.
func vkFitTail(font *ttf.Font, text string, maxW int32) (string, int32) {
	if font == nil {
		return text, 0
	}
	w, _, _ := font.SizeUTF8(text)
	if int32(w) <= maxW {
		return text, int32(w)
	}
	// Grow the suffix from the end until it stops fitting, so the cost is
	// proportional to the visible runes rather than the whole buffer.
	runes := []rune(text)
	start := len(runes)
	var lastW int32
	for start > 0 {
		cw, _, _ := font.SizeUTF8(string(runes[start-1:]))
		if int32(cw) > maxW {
			break
		}
		lastW = int32(cw)
		start--
	}
	if start >= len(runes) {
		// A single glyph is wider than the field: show its last rune.
		if len(runes) > 0 {
			return string(runes[len(runes)-1:]), 0
		}
		return "", 0
	}
	return string(runes[start:]), lastW
}

// vkKeyStyle is the per-key drawing state that depends only on the label and
// the current layer/shift state, not on focus.
type vkKeyStyle struct {
	primary   bool
	secondary bool
	shiftOn   bool
	layerOn   bool
	utility   bool
}

func vkStyleForKey(key string) vkKeyStyle {
	return vkKeyStyle{
		primary:   key == "ENTER" || key == "SEND",
		secondary: key == "ASK_AI",
		shiftOn:   key == "⇧" && keyboardUpper,
		layerOn:   (key == "123" && vkLayer == vkLayerSymbols) || (key == "ABC" && vkLayer == vkLayerLetters),
		utility:   key == "⇧" || key == "BACK" || key == "123" || key == "ABC",
	}
}

// vkDrawSingleKey draws one cap. focused selects the highlight fill; every
// other property comes from the current layout.
func vkDrawSingleKey(renderer *sdl.Renderer, config *Config, font *ttf.Font, r sdl.Rect, key, label string, focused bool) {
	highlight := vkFocusColor()
	highlightText := contrastText(highlight)
	ring := WithAlpha(highlight, 200)
	style := vkStyleForKey(key)

	radius := RadiusMD
	// Base surface.
	base := ColorIconSurface
	if style.utility {
		base = ColorSurfaceRaised
	}
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, base)

	// Top sheen and a shadowed bottom edge give keys a crisp, slightly raised
	// look without a gradient; both are cheap enough for the cached layer.
	renderer.SetDrawColor(255, 255, 255, 10)
	renderer.FillRect(&sdl.Rect{X: r.X + 6, Y: r.Y + 1, W: r.W - 12, H: 1})
	renderer.SetDrawColor(0, 0, 0, 44)
	renderer.FillRect(&sdl.Rect{X: r.X + 6, Y: r.Y + r.H - 2, W: r.W - 12, H: 1})
	renderer.SetDrawColor(ColorBorderSubtle.R, ColorBorderSubtle.G, ColorBorderSubtle.B, 110)
	renderer.DrawRect(&sdl.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H})

	textColor := ColorTextPrimary()
	switch {
	case focused || style.primary:
		// Focus ring behind the filled key.
		fillRoundedRect(renderer, r.X-2, r.Y-2, r.W+4, r.H+4, radius+2, ring)
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, highlight)
		textColor = highlightText
	case style.secondary:
		renderer.SetDrawColor(highlight.R, highlight.G, highlight.B, 160)
		renderer.DrawRect(&sdl.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H})
		textColor = highlight
	case style.shiftOn || style.layerOn:
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, WithAlpha(highlight, 70))
		renderer.SetDrawColor(highlight.R, highlight.G, highlight.B, 180)
		renderer.DrawRect(&sdl.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H})
		textColor = highlight
	case style.utility:
		textColor = ColorTextSecondary()
	}

	if font == nil || label == "" {
		return
	}
	// Multi-letter caps ("SHIFT", "space", "Done") must never spill over
	// the key edge, and single letters should use the larger face.
	labelFont, fitted := vkLabelFont(config, label, r.W-14)
	if labelFont == nil || fitted == "" {
		return
	}
	w, h, _ := labelFont.SizeUTF8(fitted)
	tx := r.X + (r.W-int32(w))/2
	ty := r.Y + (r.H-int32(h))/2
	renderText(renderer, config, labelFont, fitted, textColor, tx, ty)
}

// vkDrawKeys draws the key layer. With drawFocus=false every key is drawn in
// its resting state, which is the focus-independent layer that gets cached;
// the highlighted cap is then drawn on top each frame.
func vkDrawKeys(renderer *sdl.Renderer, config *Config, font *ttf.Font, offset int32, drawFocus bool) {
	for i, rect := range keyboardRects {
		row, col := 0, 0
		if i < len(keyboardRowCol) {
			row, col = keyboardRowCol[i][0], keyboardRowCol[i][1]
		}
		focused := drawFocus && row == keyboardPosY && col == keyboardPosX
		key := ""
		if i < len(keyboardKeys) {
			key = keyboardKeys[i]
		}
		r := rect
		r.Y += offset
		vkDrawSingleKey(renderer, config, font, r, key, vkKeyLabel(key), focused)
	}
}

// vkDrawHoverKey tints the cap under the mouse pointer.
func vkDrawHoverKey(renderer *sdl.Renderer, r sdl.Rect) {
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, RadiusMD, WithAlpha(vkFocusColor(), 30))
	renderer.SetDrawColor(vkFocusColor().R, vkFocusColor().G, vkFocusColor().B, 110)
	renderer.DrawRect(&sdl.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H})
}

// vkDrawPressedKey makes a cap look pushed in: the face darkens and an inner
// line takes the place of the top sheen.
func vkDrawPressedKey(renderer *sdl.Renderer, r sdl.Rect) {
	if r.W <= 4 || r.H <= 4 {
		return
	}
	fillRoundedRect(renderer, r.X+1, r.Y+1, r.W-2, r.H-2, RadiusMD-1, sdl.Color{R: 0, G: 0, B: 0, A: 52})
	renderer.SetDrawColor(0, 0, 0, 90)
	renderer.DrawRect(&sdl.Rect{X: r.X + 2, Y: r.Y + 2, W: r.W - 4, H: r.H - 4})
}

// vkDrawFocusedKey redraws only the highlighted cap on top of the cached layer.
func vkDrawFocusedKey(renderer *sdl.Renderer, config *Config, font *ttf.Font, offset int32) {
	for i, rect := range keyboardRects {
		row, col := 0, 0
		if i < len(keyboardRowCol) {
			row, col = keyboardRowCol[i][0], keyboardRowCol[i][1]
		}
		if row != keyboardPosY || col != keyboardPosX {
			continue
		}
		key := ""
		if i < len(keyboardKeys) {
			key = keyboardKeys[i]
		}
		r := rect
		r.Y += offset
		vkDrawSingleKey(renderer, config, font, r, key, vkKeyLabel(key), true)
		return
	}
}

// vkLabelFont picks a font whose label fits the key cap and ellipsizes only as
// a last resort, so keys stay legible at every supported screen size.
func vkLabelFont(config *Config, label string, maxW int32) (*ttf.Font, string) {
	if maxW < 8 {
		maxW = 8
	}
	for _, name := range []string{"medium", "small"} {
		f, _ := getCachedFont(config, name)
		if f == nil {
			continue
		}
		if w, _, err := f.SizeUTF8(label); err == nil && int32(w) <= maxW {
			return f, label
		}
	}
	f, _ := getCachedFont(config, "small")
	if f == nil {
		return nil, ""
	}
	return f, ellipsize(f, label, maxW)
}

func vkDrawFooter(renderer *sdl.Renderer, config *Config, font *ttf.Font, panel sdl.Rect) {
	if font == nil {
		return
	}
	hint := vkHints()
	maxW := panel.W - 40
	if w, _, _ := font.SizeUTF8(hint); int32(w) > maxW {
		hint = ellipsize(font, hint, maxW)
	}
	w, _, _ := font.SizeUTF8(hint)
	x := panel.X + (panel.W-int32(w))/2
	// Sit inside the reserved footer band instead of a fixed 20px offset, which
	// overlapped the last key row on 800x480.
	y := panel.Y + panel.H - int32(14) - int32(font.Height())
	renderText(renderer, config, font, hint, ColorTextTertiary(), x, y)
}

func mustTextWidth(font *ttf.Font, text string) int {
	if font == nil {
		return 0
	}
	w, _, err := font.SizeUTF8(text)
	if err != nil {
		return 0
	}
	return w
}
