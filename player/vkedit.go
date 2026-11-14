package main

// vkedit.go — editing on the on-screen keyboard: the caret, selection,
// clipboard tools, suggestion strip, accent popup, long presses and the
// controller chords. vkeyboard.go owns the targets and the key grid; vkui.go
// draws all of this.
//
// Focus has four zones, top to bottom: the tool row in the header (Copy, Paste,
// Cut, Select all, Clear, the language globe, the mic and Close), the text
// field (Left/Right move the caret there), the suggestion strip and the keys.
// Up from the top key row walks into them, Down walks back.

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

// vkZone is the part of the keyboard the highlight is in.
type vkZone int

const (
	vkZoneKeys vkZone = iota
	vkZoneStrip
	vkZoneField
	vkZoneTools
)

// vkTool is one chip of the header's tool row, left to right.
type vkTool int

const (
	vkToolCopy vkTool = iota
	vkToolPaste
	vkToolCut
	vkToolSelectAll
	vkToolClear
	vkToolGlobe
	vkToolMic
	vkToolClose
	vkToolCount
)

// vkStripMode is what the strip above the keys offers for the active target.
type vkStripMode int

const (
	vkStripHidden    vkStripMode = iota
	vkStripWords                 // word suggestions
	vkStripQuickURL              // ".com", "https://" ...
	vkStripQuickHost             // ".", ":", "192.168." ...
)

const (
	// vkLongPressMs is how long a press must last to open the accent popup.
	vkLongPressMs = 450
	// vkDeleteRepeatDelayMs / vkDeleteRepeatMs pace a held delete.
	vkDeleteRepeatDelayMs = 420
	vkDeleteRepeatMs      = 70
	// vkCapsDoubleTapMs is the window in which a second Shift locks caps.
	vkCapsDoubleTapMs = 400
	// vkLangFlashMs is how long a layout switch names the new language.
	vkLangFlashMs = 900
)

var (
	vkFocusZone vkZone
	vkZoneIndex int

	// vkCaret is the caret's rune index in the edited text; -1 means "at the
	// end", which keeps typing at the end even when a target rewrites its
	// buffer behind the keyboard (a live filter, a paste from the history).
	vkCaret = -1

	// vkCapsLock is Shift locked on (a double tap, or a field that opened in
	// upper case). Plain Shift is one-shot: keyboardUpper drops after a key.
	// vkShiftArmed says the last action was a Shift tap, so a second one
	// within vkCapsDoubleTapMs locks caps.
	vkCapsLock    bool
	vkShiftArmed  bool
	vkLastShiftAt uint64

	// vkSessionAnim is the vkAnimStart of the session whose state is live; a
	// new value means the keyboard was opened again (vkSyncSession).
	vkSessionAnim uint64

	// vkUndoText is the text before the last Clear or Cut (Ctrl+Z).
	vkUndoText string

	// vkLangFlashUntil shows the new layout's name over the keys briefly.
	vkLangFlashUntil uint64

	// vkTouchDown tracks a finger on the panel, for long presses on touch.
	vkTouchDown bool

	// vkL1ChordArmed is set by an L1 press; the first chord while L1 is held
	// undoes the Shift that press toggled.
	vkL1ChordArmed  bool
	vkL1ShiftBefore bool
	vkL1CapsBefore  bool
)

// vkPadHeld reports whether the logical pad button is held right now. Tests
// replace it; the real one reads the pad (vkLogicalHeld).
var vkPadHeld = vkLogicalHeld

// vkLogicalHeld reports whether any physical button that maps to the logical
// button want (after the handheld's A/B and X/Y swaps, the Back folding and the
// accessibility keymap) is held. Long presses and chords poll it, because the
// main loop only hands the keyboard button-down events.
func vkLogicalHeld(want uint8) bool {
	pad := gameController
	if pad == nil {
		return false
	}
	handheld := P().IsHandheld()
	for phys := uint8(0); phys < uint8(sdl.CONTROLLER_BUTTON_MAX); phys++ {
		if pad.Button(sdl.GameControllerButton(phys)) == 0 {
			continue
		}
		b := padFaceButton(appConfig, normalizeHandheldBackButton(phys, handheld))
		if logical, dropped := a11yResolveButton(b); !dropped && logical == want {
			return true
		}
	}
	return false
}

// ── Session ──────────────────────────────────────────────────────────────

// vkSyncSession resets the per-session editing state the first time a freshly
// opened keyboard is seen. Every open path sets vkAnimStart (some live in other
// files), so a new value is the one reliable "it opened" signal.
func vkSyncSession() {
	if !virtualKeyboardActive || vkAnimStart == 0 || vkAnimStart == vkSessionAnim {
		return
	}
	vkSessionAnim = vkAnimStart
	vkResetEditState()
	// A field that opened in upper case (the RetroAchievements API key) means
	// caps, not one shifted letter.
	vkCapsLock = keyboardUpper
	vkDictStartLoading()
	vkMicProbeStart()
	vkApplyAutoShift()
}

// vkResetEditState drops everything that belongs to one keyboard session.
func vkResetEditState() {
	vkFocusZone, vkZoneIndex = vkZoneKeys, 0
	vkCaret = -1
	vkCapsLock = false
	vkShiftArmed = false
	vkUndoText = ""
	vkLangFlashUntil = 0
	vkTouchDown = false
	vkL1ChordArmed = false
	vkPopupClose()
	vkHoldEnd()
	vkSuggestState = vkSuggestCache{}
	vkFieldCache = vkFieldLayout{}
	vkGameSearchRaw, vkTVSearchRaw = "", ""
	vkVoiceCancel()
}

// vkEndSession is the keyboard closing: recording stops, popups go, the case
// resets for the next field, and learned words are written out.
func vkEndSession() {
	vkVoiceCancel()
	vkPopupClose()
	vkHoldEnd()
	vkCaret = -1
	vkFocusZone, vkZoneIndex = vkZoneKeys, 0
	vkCapsLock = false
	keyboardUpper = false
	vkTouchDown = false
	vkSessionAnim = 0
	vkLearnedSaveAsync()
}

// ── Caret and text ───────────────────────────────────────────────────────

// vkRuneOffset is the byte offset of rune index i in s (len(s) past the end).
func vkRuneOffset(s string, i int) int {
	if i <= 0 {
		return 0
	}
	n := 0
	for off := range s {
		if n == i {
			return off
		}
		n++
	}
	return len(s)
}

// vkCaretIndex is the caret's rune index in text, clamped.
func vkCaretIndex(text string) int {
	n := utf8.RuneCountInString(text)
	if vkCaret < 0 || vkCaret > n {
		return n
	}
	return vkCaret
}

// vkSetCaret places the caret at rune index pos of text; the end is stored as
// -1 so it keeps following appended text.
func vkSetCaret(text string, pos int) {
	n := utf8.RuneCountInString(text)
	if pos < 0 {
		pos = 0
	}
	if pos >= n {
		vkCaret = -1
		return
	}
	vkCaret = pos
}

// vkMoveCaret moves the caret by delta runes. A selection collapses to its
// start (left) or end (right), the way text fields do.
func vkMoveCaret(delta int) {
	text := vkCurrentText()
	n := utf8.RuneCountInString(text)
	pos := vkCaretIndex(text)
	if vkTextSelected {
		vkTextSelected = false
		if delta < 0 {
			pos = 0
		} else {
			pos = n
		}
		vkSetCaret(text, pos)
		vkAfterCaretMove()
		return
	}
	vkSetCaret(text, pos+delta)
	vkAfterCaretMove()
}

// vkCaretHome / vkCaretEnd jump to either end of the field.
func vkCaretHome() {
	vkTextSelected = false
	vkCaret = 0
	if vkCurrentText() == "" {
		vkCaret = -1
	}
	vkAfterCaretMove()
}

func vkCaretEnd() {
	vkTextSelected = false
	vkCaret = -1
	vkAfterCaretMove()
}

func vkAfterCaretMove() {
	vkApplyAutoShift()
}

// vkSanitizeInsert drops control characters other than newline and tab (an SDL
// text event is a fixed C buffer, and its NUL padding once reached the field).
func vkSanitizeInsert(text string) string {
	if strings.IndexByte(text, 0) < 0 && strings.IndexFunc(text, func(r rune) bool {
		return r < 0x20 && r != '\n' && r != '\t'
	}) < 0 {
		return text
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
}

// vkTruncateUTF8 cuts s to at most max bytes without splitting a rune.
func vkTruncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 0 {
		return ""
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// vkForwardDelete removes the rune after the caret (the Delete key).
func vkForwardDelete(config *Config) {
	if vkTextSelected {
		vkBackspace(config)
		return
	}
	current := vkCurrentText()
	pos := vkCaretIndex(current)
	if pos >= utf8.RuneCountInString(current) {
		return
	}
	start := vkRuneOffset(current, pos)
	end := vkRuneOffset(current, pos+1)
	next := current[:start] + current[end:]
	vkSetText(next, config)
	vkSetCaret(next, pos)
	vkAfterEdit("")
}

// vkAfterEdit runs after every change to the text: a finished word is learned,
// and Shift follows the sentence (one-shot, or on at a sentence start).
func vkAfterEdit(inserted string) {
	vkShiftArmed = false
	if inserted != "" {
		if r, _ := utf8.DecodeLastRuneInString(inserted); vkWordBreak(r) {
			vkCommitWordBeforeCaret(utf8.RuneCountInString(inserted))
		}
	}
	vkApplyAutoShift()
}

// vkWordBreak reports whether r ends a word for learning purposes.
func vkWordBreak(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(".,!?;:)\"", r)
}

// vkIsWordRune reports whether r belongs to a word for suggestions.
func vkIsWordRune(r rune) bool {
	return unicode.IsLetter(r) || r == '\'' || r == '’'
}

// vkWordBeforeCaret returns the word the caret is at the end of and where it
// starts, or "" when the caret sits inside a word or after a space.
func vkWordBeforeCaret(text string, pos int) (string, int) {
	runes := []rune(text)
	if pos > len(runes) {
		pos = len(runes)
	}
	if pos < len(runes) && vkIsWordRune(runes[pos]) {
		return "", pos // editing mid-word: a suggestion would split it
	}
	start := pos
	for start > 0 && vkIsWordRune(runes[start-1]) {
		start--
	}
	if start == pos {
		return "", pos
	}
	return string(runes[start:pos]), start
}

// vkCommitWordBeforeCaret learns the word that ends skip runes before the
// caret (the break character just typed).
func vkCommitWordBeforeCaret(skip int) {
	if !vkLearnEnabled() || vkStripModeNow() != vkStripWords || vkSecretTarget() {
		return
	}
	text := vkCurrentText()
	pos := vkCaretIndex(text) - skip
	if pos <= 0 {
		return
	}
	word, _ := vkWordBeforeCaret(text, pos)
	if word != "" {
		vkLearnWord(word, vkCurrentLayout().Lang, 1)
	}
}

// vkLearnLastWord learns the final word of the text (on submit).
func vkLearnLastWord() {
	if !vkLearnEnabled() || vkStripModeNow() != vkStripWords || vkSecretTarget() {
		return
	}
	text := strings.TrimRightFunc(vkCurrentText(), func(r rune) bool { return !vkIsWordRune(r) })
	if word, _ := vkWordBeforeCaret(text, utf8.RuneCountInString(text)); word != "" {
		vkLearnWord(word, vkCurrentLayout().Lang, 1)
	}
}

// ── Shift ────────────────────────────────────────────────────────────────

// vkSentenceTarget is a field of prose, where a sentence starts capitalised.
func vkSentenceTarget() bool {
	switch vkTarget {
	case vkTargetChat, vkTargetFileLine, vkTargetName, vkTargetPlaylist:
		return true
	}
	return false
}

// vkAutoShiftWanted reports whether Shift should be on at the caret: at the
// start of a sentence in a prose field.
func vkAutoShiftWanted() bool {
	if !vkAutoCapEnabled() || !vkSentenceTarget() {
		return false
	}
	text := vkCurrentText()
	before := text[:vkRuneOffset(text, vkCaretIndex(text))]
	trimmed := strings.TrimRight(before, " ")
	if trimmed == "" {
		return true
	}
	if strings.HasSuffix(before, "\n") {
		return true
	}
	if len(trimmed) < len(before) {
		switch trimmed[len(trimmed)-1] {
		case '.', '!', '?':
			return true
		}
	}
	return false
}

// vkApplyAutoShift sets one-shot Shift from the caret's position. Caps lock is
// left alone.
func vkApplyAutoShift() {
	if vkCapsLock {
		return
	}
	if want := vkAutoShiftWanted(); keyboardUpper != want {
		keyboardUpper = want
		vkLayoutSig = ""
	}
}

// ── Targets ──────────────────────────────────────────────────────────────

// vkSecretTarget is a field whose text must not be shown, copied or learned.
func vkSecretTarget() bool {
	switch vkTarget {
	case vkTargetWiFi, vkTargetGroqKey:
		return true
	case vkTargetRommServer:
		return rommCloud.editToken
	case vkTargetRA:
		return raPanel.edit == raEditAPIKey
	case vkTargetFiles:
		return filesKeyboard == filesEditPassword
	case vkTargetPSRemote:
		// The PlayStation profile's login passcode (psremote_console_opts.go).
		return psPanelEditField == psEditLoginPIN
	}
	return false
}

// vkStripModeNow is what the strip offers for the active target: words for
// prose and searches, quick inserts for links and hosts, nothing for secrets,
// numbers, commands and initials.
func vkStripModeNow() vkStripMode {
	switch vkTarget {
	case vkTargetWiFi, vkTargetPSRemote, vkTargetMemory, vkTargetUnit, vkTargetTerm,
		vkTargetRA, vkTargetMusic, vkTargetGameArgs, vkTargetToolArgs, vkTargetGroqKey:
		return vkStripHidden
	case vkTargetRommServer:
		if rommCloud.editToken {
			return vkStripHidden
		}
		return vkStripQuickURL
	case vkTargetDownloads, vkTargetPodcast, vkTargetToolsURL, vkTargetParty, vkTargetBrowserURL:
		return vkStripQuickURL
	case vkTargetDNS, vkTargetNetwork, vkTargetNetplay:
		return vkStripQuickHost
	case vkTargetFiles:
		switch filesKeyboard {
		case filesEditPassword:
			return vkStripHidden
		case filesEditNewFolder, filesEditRename:
			return vkStripWords
		}
		return vkStripQuickHost
	case vkTargetScene:
		if textBrowserAddressEditing {
			return vkStripQuickURL
		}
	}
	return vkStripWords
}

var (
	vkQuickURLChips  = []string{"https://", "www.", ".com", ".org", "/"}
	vkQuickHostChips = []string{".com", ".", ":", "192.168.", "/"}
)

// vkQuickChips are the strip's inserts in the quick modes.
func vkQuickChips(mode vkStripMode) []string {
	switch mode {
	case vkStripQuickURL:
		return vkQuickURLChips
	case vkStripQuickHost:
		return vkQuickHostChips
	}
	return nil
}

// ── Suggestions ──────────────────────────────────────────────────────────

// vkSuggestCache is the strip's current row and what it was computed from;
// the strip recomputes only when the text, caret or layout changes.
type vkSuggestCache struct {
	valid bool
	text  string
	caret int
	lang  string
	on    bool
	row   vkSuggestRow
	disp  [3]string // what each cell shows (a literal in quotes)
}

var vkSuggestState vkSuggestCache

// vkSyncSuggestions refreshes the strip's row when its inputs changed. It runs
// once per frame; with nothing changed it is two comparisons.
func vkSyncSuggestions() {
	if vkStripModeNow() != vkStripWords {
		return
	}
	text := vkCurrentText()
	caret := vkCaretIndex(text)
	lang := vkCurrentLayout().Lang
	on := vkSuggestionsEnabled() && !vkTextSelected
	st := &vkSuggestState
	if st.valid && st.text == text && st.caret == caret && st.lang == lang && st.on == on {
		return
	}
	st.valid, st.text, st.caret, st.lang, st.on = true, text, caret, lang, on
	st.row = vkSuggestRow{}
	st.disp = [3]string{}
	if !on {
		return
	}
	if word, _ := vkWordBeforeCaret(text, caret); word != "" {
		st.row = vkSuggestFor(word, lang)
	}
	for i, s := range st.row {
		st.disp[i] = s.Text
		if s.Literal && s.Text != "" {
			st.disp[i] = "“" + s.Text + "”"
		}
	}
}

// vkStripDisplay is what strip cell i shows ("" for an empty cell).
func vkStripDisplay(i int) string {
	if vkStripModeNow() == vkStripWords {
		if i < 0 || i >= len(vkSuggestState.disp) {
			return ""
		}
		return vkSuggestState.disp[i]
	}
	text, _ := vkStripCellText(i)
	return text
}

// vkStripCellText is what strip cell i inserts in the current mode ("" for an
// empty cell) and whether it is the quoted literal.
func vkStripCellText(i int) (string, bool) {
	switch mode := vkStripModeNow(); mode {
	case vkStripWords:
		if i < 0 || i >= len(vkSuggestState.row) {
			return "", false
		}
		s := vkSuggestState.row[i]
		return s.Text, s.Literal
	case vkStripQuickURL, vkStripQuickHost:
		chips := vkQuickChips(mode)
		if i < 0 || i >= len(chips) {
			return "", false
		}
		return chips[i], false
	}
	return "", false
}

// vkStripCount is how many cells the strip has in the current mode.
func vkStripCount() int {
	switch mode := vkStripModeNow(); mode {
	case vkStripWords:
		return len(vkSuggestRow{})
	case vkStripQuickURL, vkStripQuickHost:
		return len(vkQuickChips(mode))
	}
	return 0
}

// vkStripHasContent reports whether any strip cell has something to press.
func vkStripHasContent() bool {
	for i := 0; i < vkStripCount(); i++ {
		if t, _ := vkStripCellText(i); t != "" {
			return true
		}
	}
	return false
}

// vkActivateStrip presses strip cell i.
func vkActivateStrip(i int, config *Config) {
	text, literal := vkStripCellText(i)
	if text == "" {
		return
	}
	if vkStripModeNow() == vkStripWords {
		vkApplySuggestion(vkSuggestion{Text: text, Literal: literal}, config)
	} else {
		vkInsertText(text, config)
	}
	vkKeySound()
}

// vkApplySuggestion replaces the word before the caret with s and adds a
// space, the way a phone keyboard does; a quoted literal is also learned.
func vkApplySuggestion(s vkSuggestion, config *Config) {
	if s.Text == "" {
		return
	}
	text := vkCurrentText()
	pos := vkCaretIndex(text)
	_, start := vkWordBeforeCaret(text, pos)
	startOff := vkRuneOffset(text, start)
	posOff := vkRuneOffset(text, pos)
	after := text[posOff:]
	var b strings.Builder
	b.Grow(len(text) + len(s.Text) + 1)
	b.WriteString(text[:startOff])
	b.WriteString(s.Text)
	if !strings.HasPrefix(after, " ") {
		b.WriteByte(' ')
	}
	b.WriteString(after)
	next := b.String()
	if len(next) > vkMaxTextLen {
		return
	}
	vkTextSelected = false
	vkSetText(next, config)
	vkSetCaret(next, start+utf8.RuneCountInString(s.Text)+1)
	if vkLearnEnabled() && !vkSecretTarget() {
		weight := 1
		if s.Literal {
			weight = 2
		}
		vkLearnWord(s.Text, vkCurrentLayout().Lang, weight)
	}
	vkApplyAutoShift()
}

// ── Tools ────────────────────────────────────────────────────────────────

// vkToolLabel is a tool chip's label; compact is the narrow-screen form.
func vkToolLabel(t vkTool, compact bool) string {
	switch t {
	case vkToolCopy:
		return "Copy"
	case vkToolPaste:
		return "Paste"
	case vkToolCut:
		return "Cut"
	case vkToolSelectAll:
		if compact {
			return "All"
		}
		return "Select all"
	case vkToolClear:
		return "Clear"
	case vkToolGlobe:
		return vkCurrentLayout().Short
	}
	return ""
}

// vkToolEnabled reports whether a chip can act right now.
func vkToolEnabled(t vkTool) bool {
	empty := vkCurrentText() == ""
	switch t {
	case vkToolCopy, vkToolCut:
		return !empty && !vkSecretTarget()
	case vkToolSelectAll, vkToolClear:
		return !empty
	case vkToolMic:
		return vkVoiceAvailable()
	}
	return true
}

// vkActivateTool runs a header chip.
func vkActivateTool(t vkTool, config *Config) {
	switch t {
	case vkToolCopy:
		vkCopy()
	case vkToolPaste:
		vkPaste(config)
	case vkToolCut:
		vkCut(config)
	case vkToolSelectAll:
		vkSelectAll()
	case vkToolClear:
		vkClear(config)
	case vkToolGlobe:
		vkGlobePressed(config)
	case vkToolMic:
		vkVoiceToggle(config)
	case vkToolClose:
		vkCloseKeyboard()
		vkPlay(PlayBackSound)
	}
}

// vkClipSource names the field in the clipboard history.
func vkClipSource() string {
	switch vkTarget {
	case vkTargetChat:
		return "Chat"
	case vkTargetTerm:
		return "Terminal"
	}
	return "Input"
}

// vkCopyText is what Copy and Cut take: the selection, else the whole field.
// The keyboard's selection is all-or-nothing, so both are the field's text.
func vkCopyText() string {
	return vkCurrentText()
}

// vkCopy copies the field (Copy chip, L1+X, Ctrl+C).
func vkCopy() {
	text := vkCopyText()
	switch {
	case vkSecretTarget():
		ShowToast("Passwords are not copied", ToastKindInfo)
	case strings.TrimSpace(text) == "":
		ShowToast("Nothing to copy", ToastKindInfo)
	default:
		copyWithToast(text, vkClipSource(), "Copied")
	}
}

// vkCut copies the field and clears it, keeping the old text for Ctrl+Z.
func vkCut(config *Config) {
	text := vkCopyText()
	if vkSecretTarget() {
		ShowToast("Passwords are not copied", ToastKindInfo)
		return
	}
	if strings.TrimSpace(text) == "" {
		ShowToast("Nothing to cut", ToastKindInfo)
		return
	}
	if err := copyTextToClipboard(text, vkClipSource()); err != nil {
		ShowToast("Could not copy: "+err.Error(), ToastKindWarning)
		return
	}
	vkUndoText = text
	vkTextSelected = false
	vkSetText("", config)
	vkCaret = -1
	vkAfterEdit("")
	ShowToast("Cut to clipboard", ToastKindSuccess)
}

// vkPasteReplacer folds line breaks and tabs into spaces: every keyboard field
// is one line, and a pasted newline used to submit or vanish half a link.
var vkPasteReplacer = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ")

// vkPaste inserts the system clipboard at the caret (Paste chip, L1+Y,
// Ctrl+V). It reports whether anything was inserted.
func vkPaste(config *Config) bool {
	text, err := pasteTextFromClipboard()
	if err != nil {
		ShowToast("Clipboard is unavailable: "+err.Error(), ToastKindWarning)
		return false
	}
	text = vkPasteReplacer.Replace(text)
	if strings.TrimSpace(text) == "" {
		ShowToast("Nothing to paste", ToastKindInfo)
		return false
	}
	vkInsertText(text, config)
	return true
}

// vkSelectAll selects the whole field: the next key replaces it, Copy and Cut
// take it.
func vkSelectAll() {
	if vkCurrentText() == "" {
		return
	}
	vkTextSelected = true
	vkCaret = -1
}

// vkToggleSelectAll is A on the field: select all, or drop the selection.
func vkToggleSelectAll() {
	if vkTextSelected {
		vkTextSelected = false
		return
	}
	vkSelectAll()
}

// vkClear empties the field; Ctrl+Z brings it back.
func vkClear(config *Config) {
	text := vkCurrentText()
	if text == "" {
		return
	}
	vkUndoText = text
	vkTextSelected = false
	vkSetText("", config)
	vkCaret = -1
	vkAfterEdit("")
}

// vkUndo restores the text a Clear or Cut removed.
func vkUndo(config *Config) bool {
	if vkUndoText == "" {
		return false
	}
	text := vkUndoText
	vkUndoText = ""
	vkTextSelected = false
	vkSetText(text, config)
	vkCaret = -1
	vkAfterEdit("")
	return true
}

// vkGlobePressed switches to the next enabled layout, or explains where to add
// one.
func vkGlobePressed(config *Config) {
	if len(vkEnabledLayouts()) < 2 {
		ShowToast("Add keyboard languages in Settings → Controls → On-screen keyboard (hold the globe)", ToastKindInfo)
		return
	}
	vkCycleLayout(config)
	vkLangFlashUntil = sdl.GetTicks64() + vkLangFlashMs
	vkSuggestState.valid = false
	vkPlay(PlayToggleSound)
}

// ── Sounds ───────────────────────────────────────────────────────────────

// vkPlay plays a UI sound when keyboard sounds are on.
func vkPlay(sound func()) {
	if sound != nil && vkSoundsEnabled() {
		sound()
	}
}

// vkKeySound is the tick of a key press.
func vkKeySound() { vkPlay(PlayNavSound) }

// ── Accent and language popup ────────────────────────────────────────────

// vkPopupKind says what the popup lists.
type vkPopupKind int

const (
	vkPopupAccents vkPopupKind = iota
	vkPopupLanguages
)

var vkPopup struct {
	open    bool
	kind    vkPopupKind
	anchor  sdl.Rect
	items   []string
	rects   []sdl.Rect
	card    sdl.Rect
	focus   int
	replace bool   // the press already typed base: a pick replaces it
	base    string // the key the popup belongs to
}

// vkPopupSettingsItem is the language popup's last row.
const vkPopupSettingsItem = "Keyboard settings…"

func vkPopupClose() {
	vkPopup.open = false
	vkPopup.items = vkPopup.items[:0]
	vkPopup.rects = vkPopup.rects[:0]
}

// vkOpenAccentPopup shows the variants of key above the cap at anchor.
func vkOpenAccentPopup(anchor sdl.Rect, key string, replace bool) bool {
	items := vkAccentsFor(vkCurrentLayout(), key)
	if len(items) == 0 {
		return false
	}
	vkPopup.open = true
	vkPopup.kind = vkPopupAccents
	vkPopup.anchor = anchor
	vkPopup.items = append(vkPopup.items[:0], items...)
	vkPopup.focus = 0
	vkPopup.replace = replace
	vkPopup.base = key
	vkLayoutPopup()
	vkPlay(PlayNavSound)
	return true
}

// vkOpenLanguagePopup lists the enabled layouts and a way to Keyboard settings.
func vkOpenLanguagePopup(anchor sdl.Rect) {
	vkPopup.open = true
	vkPopup.kind = vkPopupLanguages
	vkPopup.anchor = anchor
	vkPopup.items = vkPopup.items[:0]
	cur := vkCurrentLayout()
	vkPopup.focus = 0
	for i, l := range vkEnabledLayouts() {
		vkPopup.items = append(vkPopup.items, l.Native)
		if l == cur {
			vkPopup.focus = i
		}
	}
	vkPopup.items = append(vkPopup.items, vkPopupSettingsItem)
	vkPopup.replace = false
	vkPopup.base = "GLOBE"
	vkLayoutPopup()
	vkPlay(PlayNavSound)
}

// vkLayoutPopup places the popup's cells. Accents sit in one row above the
// key, as wide as a key each; languages stack in a list above the anchor.
// Both are kept inside the keyboard sheet.
func vkLayoutPopup() {
	vkPopup.rects = vkPopup.rects[:0]
	n := len(vkPopup.items)
	if n == 0 {
		vkPopup.open = false
		return
	}
	a := vkPopup.anchor
	pad := int32(8)
	sheet := vkPanelRect
	if vkPopup.kind == vkPopupAccents {
		cw := a.W
		if cw < 44 {
			cw = 44
		}
		ch := a.H
		w := int32(n)*cw + int32(n-1)*4 + 2*pad
		h := ch + 2*pad
		x := a.X + a.W/2 - w/2
		y := a.Y - h - 6
		if x < sheet.X+4 {
			x = sheet.X + 4
		}
		if x+w > sheet.X+sheet.W-4 {
			x = sheet.X + sheet.W - 4 - w
		}
		if y < sheet.Y+4 {
			y = sheet.Y + 4
		}
		vkPopup.card = sdl.Rect{X: x, Y: y, W: w, H: h}
		for i := 0; i < n; i++ {
			vkPopup.rects = append(vkPopup.rects, sdl.Rect{X: x + pad + int32(i)*(cw+4), Y: y + pad, W: cw, H: ch})
		}
		return
	}
	rowH := a.H
	if rowH < 40 {
		rowH = 40
	}
	w := int32(280)
	if vkScreenSmall() {
		w = 220
	}
	h := int32(n)*rowH + int32(n-1)*4 + 2*pad
	x := a.X + a.W/2 - w/2
	y := a.Y - h - 6
	if x < sheet.X+4 {
		x = sheet.X + 4
	}
	if x+w > sheet.X+sheet.W-4 {
		x = sheet.X + sheet.W - 4 - w
	}
	if y < 4 {
		y = 4
	}
	vkPopup.card = sdl.Rect{X: x, Y: y, W: w, H: h}
	for i := 0; i < n; i++ {
		vkPopup.rects = append(vkPopup.rects, sdl.Rect{X: x + pad, Y: y + pad + int32(i)*(rowH+4), W: w - 2*pad, H: rowH})
	}
}

// vkPopupMove walks the popup with the D-pad (accents run left/right, the
// language list up/down; either direction works on either).
func vkPopupMove(dx, dy int) {
	n := len(vkPopup.items)
	if n == 0 {
		return
	}
	step := dx
	if step == 0 {
		step = dy
	}
	vkPopup.focus += step
	if vkPopup.focus < 0 {
		vkPopup.focus = 0
	}
	if vkPopup.focus >= n {
		vkPopup.focus = n - 1
	}
}

// vkPopupPick chooses item i.
func vkPopupPick(i int, config *Config) {
	if !vkPopup.open || i < 0 || i >= len(vkPopup.items) {
		vkPopupClose()
		return
	}
	item := vkPopup.items[i]
	kind, replace, base := vkPopup.kind, vkPopup.replace, vkPopup.base
	vkPopupClose()
	if kind == vkPopupLanguages {
		if item == vkPopupSettingsItem {
			vkCloseKeyboard()
			vkOpenSettings(config)
			return
		}
		for _, l := range vkEnabledLayouts() {
			if l.Native == item {
				vkSetCurrentLayout(config, l.ID)
				vkLangFlashUntil = sdl.GetTicks64() + vkLangFlashMs
				vkSuggestState.valid = false
			}
		}
		return
	}
	if replace {
		// The press already typed the base key: swap it for the accent.
		text := vkCurrentText()
		pos := vkCaretIndex(text)
		if pos > 0 {
			start := vkRuneOffset(text, pos-1)
			end := vkRuneOffset(text, pos)
			if text[start:end] == base {
				next := text[:start] + text[end:]
				vkSetText(next, config)
				vkSetCaret(next, pos-1)
			}
		}
	}
	vkInsertText(item, config)
	vkKeySound()
}

// vkPopupHit returns the popup cell under a point, or -1.
func vkPopupHit(mx, my int32) int {
	if !vkPopup.open {
		return -1
	}
	for i, r := range vkPopup.rects {
		if pointInRect(mx, my, r) {
			return i
		}
	}
	return -1
}

// ── Long presses ─────────────────────────────────────────────────────────

// vkHoldSource is what is holding a key down.
type vkHoldSource int

const (
	vkHoldNone vkHoldSource = iota
	vkHoldPadA
	vkHoldPadX
	vkHoldMouse
	vkHoldTouch
)

var vkHold struct {
	source   vkHoldSource
	start    uint64
	key      string
	rect     sdl.Rect
	fired    bool
	repeatAt uint64
}

// vkHoldBegin starts watching a press that may become a long press: a letter
// or symbol with accents, the globe (language list) or delete (repeat).
func vkHoldBegin(source vkHoldSource, key string, rect sdl.Rect) {
	if key != "BACK" && key != "GLOBE" && len(vkAccentsFor(vkCurrentLayout(), key)) == 0 {
		return
	}
	vkHold.source = source
	vkHold.start = sdl.GetTicks64()
	vkHold.key = key
	vkHold.rect = rect
	vkHold.fired = false
	vkHold.repeatAt = vkHold.start + uint64(a11yRepeatDelayMs())
}

func vkHoldEnd() {
	vkHold.source = vkHoldNone
	vkHold.fired = false
}

// vkHoldStillDown reports whether the source of the current hold is still
// pressed.
func vkHoldStillDown() bool {
	switch vkHold.source {
	case vkHoldPadA:
		return vkPadHeld(sdl.CONTROLLER_BUTTON_A)
	case vkHoldPadX:
		return vkPadHeld(sdl.CONTROLLER_BUTTON_X)
	case vkHoldMouse:
		// The virtual cursor's A counts as the mouse (virtualcursor.go).
		_, _, state := vcursorMouseState()
		return state&sdl.ButtonLMask() != 0
	case vkHoldTouch:
		return vkTouchDown
	}
	return false
}

// vkTickHold turns a held press into its long-press action. It runs once per
// frame while the keyboard is open.
func vkTickHold(now uint64, config *Config) {
	if vkHold.source == vkHoldNone {
		return
	}
	if !vkHoldStillDown() {
		// The globe acts on release: a quick tap switches language, and only
		// a press that outlasts vkLongPressMs lists them instead.
		if vkHold.key == "GLOBE" && !vkHold.fired {
			vkGlobePressed(config)
		}
		vkHoldEnd()
		return
	}
	if vkHold.key == "BACK" {
		if now >= vkHold.repeatAt {
			vkBackspace(config)
			vkFlashPressedRect(vkHold.rect)
			vkHold.repeatAt = now + uint64(a11yRepeatRateMs())
		}
		return
	}
	if vkHold.fired || now < vkHold.start+a11yLongPressMs() {
		return
	}
	vkHold.fired = true
	if vkHold.key == "GLOBE" {
		vkOpenLanguagePopup(vkHold.rect)
		return
	}
	vkOpenAccentPopup(vkHold.rect, vkHold.key, true)
}

// ── Controller ───────────────────────────────────────────────────────────

// vkHandleChord runs L1+X (Copy) and L1+Y (Paste). The L1 press that started
// the chord toggled Shift on its way down; the first chord undoes that, so the
// chord leaves the case as it was.
func vkHandleChord(button uint8, config *Config) bool {
	if button != sdl.CONTROLLER_BUTTON_X && button != sdl.CONTROLLER_BUTTON_Y {
		return false
	}
	if !vkPadHeld(sdl.CONTROLLER_BUTTON_LEFTSHOULDER) {
		return false
	}
	if vkL1ChordArmed {
		vkL1ChordArmed = false
		if keyboardUpper != vkL1ShiftBefore || vkCapsLock != vkL1CapsBefore {
			keyboardUpper, vkCapsLock = vkL1ShiftBefore, vkL1CapsBefore
			vkLayoutSig = ""
		}
	}
	if button == sdl.CONTROLLER_BUTTON_X {
		vkCopy()
	} else {
		vkPaste(config)
	}
	return true
}

// ── Physical keyboard ────────────────────────────────────────────────────

// vkHandlePhysicalKey handles the text-editing keys of a real keyboard while
// the on-screen keyboard is open: Left/Right/Home/End move the caret, Delete
// deletes forward, Ctrl+A/C/X/V/Z edit, and an open popup takes the arrows,
// Enter and Esc. It reports whether the key was consumed; everything else
// keeps its existing meaning (Up/Down/Tab move the highlight, Enter submits).
func vkHandlePhysicalKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	vkSyncSession()
	sym := e.Keysym.Sym
	ctrl := e.Keysym.Mod&sdl.KMOD_CTRL != 0
	if vkPopup.open {
		switch sym {
		case sdl.K_LEFT, sdl.K_UP:
			vkPopupMove(-1, 0)
		case sdl.K_RIGHT, sdl.K_DOWN, sdl.K_TAB:
			vkPopupMove(1, 0)
		case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
			vkPopupPick(vkPopup.focus, config)
		case sdl.K_ESCAPE, sdl.K_BACKSPACE:
			vkPopupClose()
		default:
			return false
		}
		return true
	}
	if vkVoiceHandleKey(sym, config) {
		return true
	}
	if ctrl {
		switch sym {
		case sdl.K_a:
			vkSelectAll()
		case sdl.K_c:
			vkCopy()
		case sdl.K_x:
			vkCut(config)
		case sdl.K_v:
			vkPaste(config)
		case sdl.K_z:
			vkUndo(config)
		case sdl.K_LEFT:
			vkCaretWordLeft()
		case sdl.K_RIGHT:
			vkCaretWordRight()
		default:
			return false
		}
		return true
	}
	switch sym {
	case sdl.K_LEFT:
		vkMoveCaret(-1)
	case sdl.K_RIGHT:
		vkMoveCaret(1)
	case sdl.K_HOME:
		vkCaretHome()
	case sdl.K_END:
		vkCaretEnd()
	case sdl.K_DELETE:
		vkForwardDelete(config)
	default:
		return false
	}
	return true
}

// vkCaretWordLeft / vkCaretWordRight jump the caret a word (Ctrl+arrows).
func vkCaretWordLeft() {
	text := vkCurrentText()
	runes := []rune(text)
	pos := vkCaretIndex(text)
	for pos > 0 && !vkIsWordRune(runes[pos-1]) {
		pos--
	}
	for pos > 0 && vkIsWordRune(runes[pos-1]) {
		pos--
	}
	vkTextSelected = false
	vkSetCaret(text, pos)
	vkAfterCaretMove()
}

func vkCaretWordRight() {
	text := vkCurrentText()
	runes := []rune(text)
	pos := vkCaretIndex(text)
	for pos < len(runes) && !vkIsWordRune(runes[pos]) {
		pos++
	}
	for pos < len(runes) && vkIsWordRune(runes[pos]) {
		pos++
	}
	vkTextSelected = false
	vkSetCaret(text, pos)
	vkAfterCaretMove()
}
