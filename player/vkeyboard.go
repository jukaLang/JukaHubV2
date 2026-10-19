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
	vkTargetTerm  // terminal command line
	vkTargetName  // chat display name
	vkTargetTV    // Live TV channel search
	vkTargetParty // Watch Party invite / shared link
)

// vkNameBuffer holds the chat display name while it is being edited; it is
// committed to the user config when the keyboard submits.
var vkNameBuffer string

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
	now := sdl.GetTicks64()
	if now <= vkAnimStart {
		return 26
	}
	elapsed := float64(now-vkAnimStart) / 120.0
	if elapsed >= 1 {
		return 0
	}
	return int32((1 - vkEaseOut(elapsed)) * 26)
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
	current := vkCurrentText()
	if len(current) >= vkMaxTextLen {
		return
	}
	vkSetText(current+text, config)
}

// vkBackspace removes the last rune (not byte) so multi-byte text survives.
func vkBackspace(config *Config) {
	current := vkCurrentText()
	if current == "" {
		return
	}
	_, size := utf8.DecodeLastRuneInString(current)
	if size <= 0 {
		return
	}
	vkSetText(current[:len(current)-size], config)
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
	}
	if textBrowserAddressEditing {
		return "https://example.com"
	}
	return "Type here"
}

func vkHints() string {
	switch vkTarget {
	case vkTargetName:
		return "A type    Y save    B / Esc close"
	case vkTargetChat:
		return "A type    Y ask AI    B / Esc close"
	case vkTargetUnit:
		return "A type    Y convert    B / Esc close"
	case vkTargetTerm:
		return "A type    Done run    B / Esc close"
	case vkTargetTV:
		return "A type    live filter    B / Esc close"
	}
	return "A type    Y save    B / Esc close"
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
	if textBrowserAddressEditing {
		variable = textBrowserURLVariable
	}
	if variable != "" {
		inputTextBuffer = inputVariableValue(config, variable)
	}
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkCloseKeyboard dismisses the keyboard. Only scene inputs reset the modal
// element indices; chat and unit input keep their state.
func vkCloseKeyboard() {
	virtualKeyboardActive = false
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	vkAnimStart = 0
	if vkTarget == vkTargetScene {
		activeSceneIndex = -1
		activeElementIndex = -1
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

	switch key {
	case "⇧":
		toggleKeyboardCase()
	case "123":
		vkLayer = vkLayerSymbols
		vkLayoutSig = ""
	case "ABC":
		vkLayer = vkLayerLetters
		vkLayoutSig = ""
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
		vkSetText(text, config)
		vkCloseKeyboard()
		vkTriggerSceneSearch(config)
	}
}

// vkTriggerSceneSearch clicks the search button of a search scene on submit.
func vkTriggerSceneSearch(config *Config) {
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return
	}
	if !sceneHasSearchResults(config.Scenes[currentSceneIndex]) {
		return
	}
	for _, elem := range config.Scenes[currentSceneIndex].Elements {
		if elem.Type == "button" && elem.Trigger == "yt_search" {
			go executeYouTubeSearch(config, elem.TriggerTarget, elem.TriggerValue, snapshotVars(config))
			return
		}
	}
}

// toggleKeyboardCase flips letter case without losing the focused key.
func toggleKeyboardCase() {
	keyboardUpper = !keyboardUpper
	vkLayoutSig = ""
	vkEnsureLayout()
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
	vkStaticValid = false
	vkStaticBroken = false
}

// Chat cache state and ensure function
var (
	vkChatStaticTex    *sdl.Texture
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
	vkChatStaticValid = false
	vkChatStaticBroken = false
}

// Video overlay cache state and ensure function
var (
	vkVideoOverlayTex    *sdl.Texture
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
	vkChatStaticValid = true
	vkChatStaticBuilds++
	return tex
}

func vkEnsureStaticLayer(renderer *sdl.Renderer, config *Config, font, smallFont *ttf.Font) *sdl.Texture {
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

	// Dim the content behind the panel.
	fillRoundedRect(renderer, 0, 0, screenWidth, screenHeight, 0, WithAlpha(ColorBackground, 190))
	fillRoundedRect(renderer, 0, 0, screenWidth, panel.Y, 0, WithAlpha(ColorBackground, 60))

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
		vkDrawFocusedKey(renderer, config, font, offset)
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
	fillRoundedRect(renderer, closeBtn.X, closeBtn.Y, closeBtn.W, closeBtn.H, RadiusSM, ColorSurfaceRaised)
	strokeRoundedRect(renderer, closeBtn.X, closeBtn.Y, closeBtn.W, closeBtn.H, RadiusSM, 1,
		WithAlpha(ColorBorderSubtle, 140))
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
		renderText(renderer, config, font, vkPlaceholder(), ColorTextTertiary(), field.X+padX, textY)
	} else {
		visible, w := vkFitTail(font, text, maxW)
		renderText(renderer, config, font, visible, ColorTextPrimary(), field.X+padX, textY)
		if uint32(sdl.GetTicks64()/530)%2 == 0 {
			cx := field.X + padX + w + 2
			if cx > field.X+field.W-padX {
				cx = field.X + field.W - padX
			}
			fillRoundedRect(renderer, cx, field.Y+field.H/2-textH/2+2, 2, textH-4, 1, vkFocusColor())
		}
	}

	// Right-hand hint: remaining characters before the cap.
	remaining := vkMaxTextLen - len(text)
	if remaining < 200 && remaining >= 0 {
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

	// Top sheen + border give keys a crisp, modern edge.
	renderer.SetDrawColor(255, 255, 255, 10)
	renderer.FillRect(&sdl.Rect{X: r.X + 6, Y: r.Y + 1, W: r.W - 12, H: 1})
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
