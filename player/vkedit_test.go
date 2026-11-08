package main

// Tests for editing on the on-screen keyboard (vkedit.go): the caret, the
// selection, the clipboard tools and chords, the suggestion strip, the accent
// popup and the focus zones above the keys.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// sceneKeyboard opens a scene-target keyboard over an empty buffer.
func sceneKeyboard(t *testing.T) *Config {
	t.Helper()
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	prevScene, prevElem := activeSceneIndex, activeElementIndex
	activeSceneIndex, activeElementIndex = -1, -1
	t.Cleanup(func() { activeSceneIndex, activeElementIndex = prevScene, prevElem })
	vkTarget = vkTargetScene
	vkEnsureLayout()
	return cfg
}

func TestVKCaretInsertsAndDeletesInTheMiddle(t *testing.T) {
	cfg := sceneKeyboard(t)
	vkInsertText("hllo", cfg)
	vkSetCaret(vkCurrentText(), 1)
	vkInsertText("e", cfg)
	if inputTextBuffer != "hello" {
		t.Fatalf("insert at the caret gave %q", inputTextBuffer)
	}
	if vkCaretIndex(inputTextBuffer) != 2 {
		t.Fatalf("caret at %d after the insert, want 2", vkCaretIndex(inputTextBuffer))
	}
	vkBackspace(cfg)
	if inputTextBuffer != "hllo" || vkCaretIndex(inputTextBuffer) != 1 {
		t.Fatalf("backspace at the caret gave %q (caret %d)", inputTextBuffer, vkCaretIndex(inputTextBuffer))
	}
	vkForwardDelete(cfg)
	if inputTextBuffer != "hlo" {
		t.Fatalf("forward delete gave %q", inputTextBuffer)
	}
	vkCaretEnd()
	vkInsertText("!", cfg)
	if inputTextBuffer != "hlo!" {
		t.Fatalf("typing at the end gave %q", inputTextBuffer)
	}
	// Multi-byte text keeps whole runes on both sides of the caret.
	vkSetText("añb", cfg)
	vkSetCaret("añb", 2)
	vkBackspace(cfg)
	if inputTextBuffer != "ab" {
		t.Fatalf("rune-aware backspace gave %q", inputTextBuffer)
	}
}

func TestVKSelectAllCutAndUndo(t *testing.T) {
	cfg := sceneKeyboard(t)
	spy := &clipboardSpy{}
	installClipboardSpy(t, spy)
	resetClipboardHistory(t)

	vkInsertText("old query", cfg)
	vkSelectAll()
	vkInsertText("n", cfg)
	if inputTextBuffer != "n" {
		t.Fatalf("typing over a selection gave %q", inputTextBuffer)
	}
	vkInsertText("ew", cfg)
	vkCut(cfg)
	if spy.set != "new" || inputTextBuffer != "" {
		t.Fatalf("cut: clipboard %q, field %q", spy.set, inputTextBuffer)
	}
	if !vkUndo(cfg) || inputTextBuffer != "new" {
		t.Fatalf("undo after cut gave %q", inputTextBuffer)
	}
	vkClear(cfg)
	if inputTextBuffer != "" || !vkUndo(cfg) || inputTextBuffer != "new" {
		t.Fatalf("clear and undo gave %q", inputTextBuffer)
	}
}

func TestVKPasteFoldsLinesAndKeepsSecretsOffTheClipboard(t *testing.T) {
	cfg := sceneKeyboard(t)
	spy := &clipboardSpy{get: "line one\nline two\ttabbed"}
	installClipboardSpy(t, spy)
	resetClipboardHistory(t)

	vkInsertText("> ", cfg)
	if !vkPaste(cfg) {
		t.Fatal("paste reported nothing inserted")
	}
	if inputTextBuffer != "> line one line two tabbed" {
		t.Fatalf("paste gave %q", inputTextBuffer)
	}

	prevPass, prevSSID := wifiPassphraseBuffer, wifiPendingSSID
	t.Cleanup(func() { wifiPassphraseBuffer, wifiPendingSSID = prevPass, prevSSID })
	vkTarget = vkTargetWiFi
	wifiPassphraseBuffer = "hunter22"
	spy.set = "unchanged"
	vkCopy()
	if spy.set != "unchanged" {
		t.Fatal("a Wi-Fi password was copied to the clipboard")
	}
	if vkToolEnabled(vkToolCopy) || vkToolEnabled(vkToolCut) {
		t.Fatal("Copy and Cut should be off for a password")
	}
	if !vkToolEnabled(vkToolPaste) {
		t.Fatal("pasting a password must stay possible")
	}
}

func TestVKControllerChordsCopyAndPaste(t *testing.T) {
	cfg := sceneKeyboard(t)
	spy := &clipboardSpy{get: "pasted"}
	installClipboardSpy(t, spy)
	resetClipboardHistory(t)
	vkInsertText("typed", cfg)

	l1Held := false
	vkPadHeld = func(b uint8) bool { return b == sdl.CONTROLLER_BUTTON_LEFTSHOULDER && l1Held }

	// L1 goes down (Shift toggles), then Y while it is held: paste, and the
	// Shift that L1 toggled is undone.
	vkHandleControllerButton(uint8(sdl.CONTROLLER_BUTTON_LEFTSHOULDER), cfg)
	if !keyboardUpper {
		t.Fatal("L1 alone should shift")
	}
	l1Held = true
	if !vkHandleControllerButton(uint8(sdl.CONTROLLER_BUTTON_Y), cfg) {
		t.Fatal("L1+Y was not consumed")
	}
	if inputTextBuffer != "typedpasted" {
		t.Fatalf("L1+Y gave %q", inputTextBuffer)
	}
	if keyboardUpper {
		t.Fatal("the chord left Shift on")
	}
	if !vkHandleControllerButton(uint8(sdl.CONTROLLER_BUTTON_X), cfg) || spy.set != "typedpasted" {
		t.Fatalf("L1+X copied %q", spy.set)
	}
	if inputTextBuffer != "typedpasted" {
		t.Fatal("L1+X must copy, not delete")
	}
	l1Held = false
	vkHandleControllerButton(uint8(sdl.CONTROLLER_BUTTON_X), cfg)
	if inputTextBuffer != "typedpaste" {
		t.Fatalf("X alone should delete, got %q", inputTextBuffer)
	}
}

func TestVKPhysicalKeysEditTheField(t *testing.T) {
	cfg := sceneKeyboard(t)
	spy := &clipboardSpy{}
	installClipboardSpy(t, spy)
	resetClipboardHistory(t)
	key := func(sym sdl.Keycode, mod uint16) bool {
		return vkHandlePhysicalKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym, Mod: mod}}, cfg)
	}
	vkInsertText("word", cfg)
	if !key(sdl.K_LEFT, 0) || vkCaretIndex(inputTextBuffer) != 3 {
		t.Fatalf("Left did not move the caret: %d", vkCaretIndex(inputTextBuffer))
	}
	if !key(sdl.K_HOME, 0) || vkCaretIndex(inputTextBuffer) != 0 {
		t.Fatal("Home did not move the caret to the start")
	}
	if !key(sdl.K_a, sdl.KMOD_LCTRL) || !vkTextSelected {
		t.Fatal("Ctrl+A did not select the field")
	}
	if !key(sdl.K_x, sdl.KMOD_LCTRL) || spy.set != "word" || inputTextBuffer != "" {
		t.Fatalf("Ctrl+X: clipboard %q, field %q", spy.set, inputTextBuffer)
	}
	if !key(sdl.K_z, sdl.KMOD_LCTRL) || inputTextBuffer != "word" {
		t.Fatalf("Ctrl+Z gave %q", inputTextBuffer)
	}
	// Keys the field does not use keep their old meaning.
	if key(sdl.K_UP, 0) || key(sdl.K_RETURN, 0) || key(sdl.K_h, 0) {
		t.Fatal("Up, Enter and letters must fall through to the keyboard's own handling")
	}
}

func TestVKSuggestionReplacesTheWordAndAddsASpace(t *testing.T) {
	if vkDictLoadSync() == nil {
		t.Fatal("word list unavailable")
	}
	withLearnedStore(t)
	cfg := sceneKeyboard(t)
	withKeyboardConfig(t)
	vkInsertText("I said teh", cfg)
	vkSyncSuggestions()
	if vkSuggestState.row[1].Text != "the" {
		t.Fatalf("best guess for teh = %+v", vkSuggestState.row)
	}
	vkActivateStrip(1, cfg)
	if inputTextBuffer != "I said the " {
		t.Fatalf("applying the suggestion gave %q", inputTextBuffer)
	}
	// Mid-word, the strip stays quiet: a suggestion would split the word.
	vkSetCaret(inputTextBuffer, 3)
	vkSyncSuggestions()
	if vkStripHasContent() {
		t.Fatalf("suggestions offered mid-word: %+v", vkSuggestState.row)
	}
	// The quoted literal keeps the word and teaches it.
	vkCaretEnd()
	vkInsertText("Zorbly", cfg)
	vkSyncSuggestions()
	if !vkSuggestState.row[0].Literal {
		t.Fatalf("no literal cell for an unknown word: %+v", vkSuggestState.row)
	}
	vkActivateStrip(0, cfg)
	if !vkDictKnows("zorbly", "en") {
		t.Fatal("tapping the literal did not learn the word")
	}
}

func TestVKAutoCapitalizesSentencesInChat(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	withKeyboardConfig(t)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	vkTarget = vkTargetChat
	chatInputText = ""
	vkApplyAutoShift()
	if !keyboardUpper {
		t.Fatal("an empty message should start capitalised")
	}
	vkInsertText("H", cfg)
	if keyboardUpper {
		t.Fatal("Shift stayed on mid-sentence")
	}
	vkInsertText("i. ", cfg)
	if !keyboardUpper {
		t.Fatal("a new sentence did not capitalise")
	}
	// Searches are not prose: no automatic capitals.
	vkTarget = vkTargetGameSearch
	vkApplyAutoShift()
	if keyboardUpper {
		t.Fatal("a search field capitalised itself")
	}
}

func TestVKStripModesFollowTheField(t *testing.T) {
	resetKeyboardState(t)
	cases := []struct {
		target vkTargetKind
		mode   vkStripMode
	}{
		{vkTargetChat, vkStripWords},
		{vkTargetGameSearch, vkStripWords},
		{vkTargetWiFi, vkStripHidden},
		{vkTargetTerm, vkStripHidden},
		{vkTargetDownloads, vkStripQuickURL},
		{vkTargetDNS, vkStripQuickHost},
		{vkTargetGroqKey, vkStripHidden},
	}
	for _, tc := range cases {
		vkTarget = tc.target
		if got := vkStripModeNow(); got != tc.mode {
			t.Errorf("target %d: strip mode %d, want %d", tc.target, got, tc.mode)
		}
	}
}

func TestVKFocusWalksIntoTheStripFieldAndTools(t *testing.T) {
	cfg := sceneKeyboard(t)
	spy := &clipboardSpy{get: "x"}
	installClipboardSpy(t, spy)
	vkInsertText("abc ", cfg)

	keyboardPosY, keyboardPosX = 0, 4
	vkMoveFocus(0, -1) // no suggestions after a space: straight to the field
	if vkFocusZone != vkZoneField {
		t.Fatalf("Up from the top row went to zone %d, want the field", vkFocusZone)
	}
	vkMoveFocus(-1, 0)
	if vkCaretIndex(inputTextBuffer) != 3 {
		t.Fatalf("Left in the field put the caret at %d", vkCaretIndex(inputTextBuffer))
	}
	vkMoveFocus(0, -1)
	if vkFocusZone != vkZoneTools || vkTool(vkZoneIndex) != vkToolCopy {
		t.Fatalf("Up from the field went to zone %d index %d, want the Copy chip", vkFocusZone, vkZoneIndex)
	}
	vkMoveFocus(1, 0)
	vkActivateFocused(cfg) // Paste
	if inputTextBuffer != "abcx " {
		t.Fatalf("Paste from the tool row gave %q", inputTextBuffer)
	}
	vkMoveFocus(0, 1)
	vkMoveFocus(0, 1)
	if vkFocusZone != vkZoneKeys || keyboardPosY != 0 {
		t.Fatalf("Down did not return to the keys: zone %d row %d", vkFocusZone, keyboardPosY)
	}
	// Close sits at the end of the tool row and closes the keyboard.
	vkFocusTool(vkToolClose)
	vkActivateFocused(cfg)
	if virtualKeyboardActive {
		t.Fatal("the Close chip did not close the keyboard")
	}
}

func TestVKVerticalMovesKeepToTheColumn(t *testing.T) {
	sceneKeyboard(t)
	sr, sc := rowIndex(keyboard, "SPACE")
	keyboardPosY, keyboardPosX = sr, sc
	vkMoveFocus(0, -1)
	if got := vkFocusedKey(); got != "c" && got != "v" && got != "b" {
		t.Fatalf("Up from the space bar landed on %q, want a key above it", got)
	}
}

func TestVKAccentPopupReplacesTheTypedLetter(t *testing.T) {
	cfg := sceneKeyboard(t)
	vkInsertText("cafe", cfg)
	i := vkKeyIndex("e")
	if i < 0 {
		t.Fatal("e key missing")
	}
	if !vkOpenAccentPopup(keyboardRects[i], "e", true) {
		t.Fatal("no accent popup for e")
	}
	// Walk to é and pick it.
	for n := 0; n < len(vkPopup.items); n++ {
		if vkPopup.items[vkPopup.focus] == "é" {
			break
		}
		vkPopupMove(1, 0)
	}
	vkHandleControllerButton(uint8(sdl.CONTROLLER_BUTTON_A), cfg)
	if inputTextBuffer != "café" {
		t.Fatalf("picking an accent gave %q", inputTextBuffer)
	}
	if vkPopup.open {
		t.Fatal("the popup stayed open after a pick")
	}
	// B closes the popup, not the keyboard.
	vkOpenAccentPopup(keyboardRects[i], "e", false)
	vkHandleControllerButton(uint8(sdl.CONTROLLER_BUTTON_B), cfg)
	if vkPopup.open || !virtualKeyboardActive {
		t.Fatal("B should close only the popup")
	}
}

func TestVKLongPressOpensAccentsAndTheGlobeSwitches(t *testing.T) {
	cfg := sceneKeyboard(t)
	appCfg := withKeyboardConfig(t)
	held := true
	vkPadHeld = func(b uint8) bool { return b == sdl.CONTROLLER_BUTTON_A && held }

	r, c := rowIndex(keyboard, "a")
	keyboardPosY, keyboardPosX = r, c
	vkHandleControllerButton(uint8(sdl.CONTROLLER_BUTTON_A), cfg)
	if inputTextBuffer != "a" {
		t.Fatalf("the press typed %q", inputTextBuffer)
	}
	vkTickHold(vkHold.start+vkLongPressMs-1, cfg)
	if vkPopup.open {
		t.Fatal("the popup opened before the long-press time")
	}
	vkTickHold(vkHold.start+vkLongPressMs, cfg)
	if !vkPopup.open {
		t.Fatal("holding A did not open the accents")
	}
	held = false
	vkPopupClose()

	// The globe: a quick tap switches language on release.
	vkSetEnabledLayouts(appCfg, []*vkLayout{vkLayoutByID("en-US"), vkLayoutByID("fr")})
	vkEnsureLayout()
	g := vkKeyIndex("GLOBE")
	if g < 0 {
		t.Fatal("no globe key with two languages")
	}
	keyboardPosY, keyboardPosX = keyboardRowCol[g][0], keyboardRowCol[g][1]
	held = true
	vkHandleControllerButton(uint8(sdl.CONTROLLER_BUTTON_A), cfg)
	if vkCurrentLayout().ID != "en-US" {
		t.Fatal("the globe switched on the press instead of the release")
	}
	held = false
	vkTickHold(sdl.GetTicks64(), appCfg)
	if vkCurrentLayout().ID != "fr" {
		t.Fatalf("a tap on the globe left the layout on %s", vkCurrentLayout().ID)
	}
}

func TestVKLiveSearchKeepsItsSpaces(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	prevQuery := gameLibraryQuery()
	t.Cleanup(func() { setGameLibraryQuery(prevQuery) })
	setGameLibraryQuery("")
	vkTarget = vkTargetGameSearch

	// The library trims its query; the typed space must survive anyway.
	vkInsertText("super", cfg)
	vkInsertText(" ", cfg)
	vkInsertText("mario", cfg)
	if got := vkCurrentText(); got != "super mario" {
		t.Fatalf("the field holds %q, want \"super mario\"", got)
	}
	if got := gameLibraryQuery(); got != "super mario" {
		t.Fatalf("the library searches for %q", got)
	}
	// A query changed from elsewhere wins over the keyboard's copy.
	setGameLibraryQuery("zelda")
	if got := vkCurrentText(); got != "zelda" {
		t.Fatalf("after an outside change the field holds %q", got)
	}
}

func TestVKHoldingL1ShowsTheChordHints(t *testing.T) {
	sceneKeyboard(t)
	appCfg := withKeyboardConfig(t)
	prevDown := vkL1DownAt
	t.Cleanup(func() { vkL1DownAt = prevDown })

	if vkChordHintsWanted(10_000) {
		t.Fatal("chord hints without L1 held")
	}
	vkPadHeld = func(b uint8) bool { return b == sdl.CONTROLLER_BUTTON_LEFTSHOULDER }
	vkL1DownAt = 10_000
	if vkChordHintsWanted(10_000 + vkChordDelayMs - 1) {
		t.Fatal("a quick tap on Shift flashed the chord hints")
	}
	if !vkChordHintsWanted(10_000 + vkChordDelayMs) {
		t.Fatal("holding L1 should show the L1 combinations")
	}
	hints := vkChordHintList()
	if len(hints) != 2 || hints[0].Button != "L1+X" || hints[1].Button != "L1+Y" {
		t.Fatalf("one language: chord hints %+v", hints)
	}
	vkSetEnabledLayouts(appCfg, []*vkLayout{vkLayoutByID("en-US"), vkLayoutByID("de")})
	if hints := vkChordHintList(); len(hints) != 3 || hints[2].Button != "L1+R1" {
		t.Fatalf("two languages: chord hints %+v", hints)
	}
}
