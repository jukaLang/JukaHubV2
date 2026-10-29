package main

// The clipboard unification contract: every copy in the app walks
// copyTextToClipboard (system clipboard + the Ctrl+Shift+V history), and one
// pair of Ctrl+V / Ctrl+C chords routes to whatever text surface owns the
// keyboard — the on-screen keyboard targets (Browser address and form fields,
// Chat composer, unit converter...), the Terminal prompt cursor and the Notes
// open field. The SDL calls are swapped out here so the routing is checked
// without a real system clipboard.

import (
	"errors"
	"testing"
)

type clipboardSpy struct {
	set string
	get string
	err error
}

func installClipboardSpy(t *testing.T, spy *clipboardSpy) {
	t.Helper()
	prevSet, prevGet := clipboardSetText, clipboardGetText
	clipboardSetText = func(text string) error {
		if spy.err != nil {
			return spy.err
		}
		spy.set = text
		return nil
	}
	clipboardGetText = func() (string, error) {
		return spy.get, spy.err
	}
	t.Cleanup(func() { clipboardSetText, clipboardGetText = prevSet, prevGet })
}

func resetClipboardHistory(t *testing.T) {
	t.Helper()
	prev := clipHistory
	clipHistory = nil
	t.Cleanup(func() { clipHistory = prev })
}

// withScenes points the scene table at plain named scenes, so the chord
// routers' scene checks (isTerminalScene / isNotesScene) see them.
func withScenes(t *testing.T, names ...string) *Config {
	t.Helper()
	prevConfig, prevIndex := appConfig, currentSceneIndex
	scenes := make([]SceneConfig, len(names))
	for i, name := range names {
		scenes[i] = SceneConfig{Name: name}
	}
	cfg := &Config{Scenes: scenes}
	appConfig, currentSceneIndex = cfg, 0
	t.Cleanup(func() { appConfig, currentSceneIndex = prevConfig, prevIndex })
	return cfg
}

func resetTerm(t *testing.T) {
	t.Helper()
	prevInput, prevCursor := term.input, term.cursor
	t.Cleanup(func() { term.input, term.cursor = prevInput, prevCursor })
}

func resetNotesEditor(t *testing.T) {
	t.Helper()
	prev := notes
	notes = notesState{editID: -1}
	t.Cleanup(func() { notes = prev })
}

func resetClipboardEditingState(t *testing.T) {
	t.Helper()
	prevVK, prevChat, prevChatText, prevUnit, prevScene, prevElem := virtualKeyboardActive, chatInputActive, chatInputText, unitInputActive, activeSceneIndex, activeElementIndex
	virtualKeyboardActive, chatInputActive, chatInputText, unitInputActive = false, false, "", false
	activeSceneIndex, activeElementIndex = -1, -1
	t.Cleanup(func() {
		virtualKeyboardActive, chatInputActive, chatInputText, unitInputActive = prevVK, prevChat, prevChatText, prevUnit
		activeSceneIndex, activeElementIndex = prevScene, prevElem
	})
}

// Every copy must land in the clipboard manager's history — before the
// unification the history (Ctrl+Shift+V) never received an entry at all.
func TestCopyTextToClipboardRecordsHistory(t *testing.T) {
	spy := &clipboardSpy{}
	installClipboardSpy(t, spy)
	resetClipboardHistory(t)

	if err := copyTextToClipboard("hello world", "Chat"); err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if spy.set != "hello world" {
		t.Errorf("system clipboard got %q, want %q", spy.set, "hello world")
	}
	if len(clipHistory) != 1 || clipHistory[0].text != "hello world" || clipHistory[0].source != "Chat" {
		t.Fatalf("history = %+v, want one \"Chat\" entry with the copied text", clipHistory)
	}

	// Nothing to copy is an error that changes nothing.
	spy.set = "unchanged"
	if err := copyTextToClipboard("   ", "Chat"); err == nil {
		t.Fatal("copying whitespace should report nothing to copy")
	}
	if spy.set != "unchanged" {
		t.Errorf("empty copy overwrote the clipboard with %q", spy.set)
	}
	if len(clipHistory) != 1 {
		t.Errorf("whitespace copy pushed %+v", clipHistory)
	}

	// A failing write must not fabricate a history entry.
	spy.err = errors.New("no clipboard")
	if err := copyTextToClipboard("second", "Notes"); err == nil {
		t.Fatal("expected the write error to surface")
	}
	if len(clipHistory) != 1 {
		t.Errorf("failed write pushed %+v", clipHistory)
	}
}

// A paste in the Terminal takes the prompt-cursor path, exactly like typed
// keys: inserted characters land at the cursor, not at the end, and the
// terminal filter keeps them printable ASCII.
func TestClipboardPasteTypedIntoTerminalPrompt(t *testing.T) {
	cfg := withScenes(t, "Terminal")
	resetTerm(t)
	resetClipboardEditingState(t)
	spy := &clipboardSpy{get: "rm -rf /tmp/cache"}
	installClipboardSpy(t, spy)

	term.input = "mk "
	term.cursor = 2 // cursor mid-command, after "mk"
	if !clipboardPasteIntoActiveField(cfg) {
		t.Fatal("the terminal should consume the paste chord")
	}
	want := "mkrm -rf /tmp/cache "
	if term.input != want {
		t.Fatalf("term.input = %q, want the paste at the cursor: %q", term.input, want)
	}
	// The cursor sits after the pasted text; "mk " kept its trailing space
	// after the cursor, so the position is 2 + len("rm -rf /tmp/cache").
	if wantCursor := len("mkrm -rf /tmp/cache"); term.cursor != wantCursor {
		t.Errorf("cursor = %d, want %d", term.cursor, wantCursor)
	}

	// An empty clipboard is a consumed no-op with a hint, not a fall-through
	// into the scene's ordinary 'v' handling.
	spy.get = "   "
	before := term.input
	if !clipboardPasteIntoActiveField(cfg) {
		t.Fatal("an empty clipboard is still a consumed paste attempt")
	}
	if term.input != before {
		t.Errorf("empty paste changed the command line to %q", term.input)
	}

	// Nothing editing: ordinary key handling proceeds (off the Terminal scene
	// so isTerminalScene does not claim it).
	appConfig.Scenes[0].Name = "Home"
	if clipboardPasteIntoActiveField(cfg) {
		t.Fatal("with no text surface active the paste chord must fall through")
	}
}

// A paste in Notes lands in the field that is open, under the same limits a
// keystroke obeys.
func TestClipboardPasteIntoOpenNotesField(t *testing.T) {
	cfg := withScenes(t, "Notes")
	resetNotesEditor(t)
	resetClipboardEditingState(t)
	spy := &clipboardSpy{get: "shopping list"}
	installClipboardSpy(t, spy)

	notes.tab = 2
	notes.editContent = "milk\n"
	if !clipboardPasteIntoActiveField(cfg) {
		t.Fatal("Notes should consume the paste chord")
	}
	if notes.editContent != "milk\nshopping list" {
		t.Errorf("editContent = %q", notes.editContent)
	}
	if notes.editTitle != "" {
		t.Errorf("paste leaked into the title: %q", notes.editTitle)
	}

	notes.tab = 1
	if !clipboardPasteIntoActiveField(cfg) {
		t.Fatal("Notes title tab should consume the paste chord")
	}
	if notes.editTitle != "shopping list" {
		t.Errorf("editTitle = %q", notes.editTitle)
	}
}

// The on-screen keyboard targets route through vkInsertText, which is how
// typed text lands: control bytes stripped, the Browser address bar's
// selected-start replacement honoured, and the variable written back.
func TestClipboardPasteIntoVKTargets(t *testing.T) {
	spy := &clipboardSpy{get: "http://example.com/next"}
	installClipboardSpy(t, spy)
	resetClipboardHistory(t)
	resetKeyboardState(t)
	resetClipboardEditingState(t)

	// Chat composer, no modal: the buffer the funnelling uses.
	vkTarget = vkTargetChat
	chatInputActive = true
	if !clipboardPasteIntoActiveField(&Config{}) {
		t.Fatal("the chat composer should consume the paste chord")
	}
	if chatInputText != "http://example.com/next" {
		t.Errorf("composer = %q", chatInputText)
	}
	if len(clipHistory) != 0 {
		t.Errorf("a paste pushed a history entry: %+v", clipHistory)
	}

	// Browser address bar: opens with the old URL selected, so a paste
	// replaces it instead of appending.
	prevConfig, prevIndex := appConfig, currentSceneIndex
	resetKeyboardState(t)
	resetClipboardEditingState(t)
	spy.get = "http://example.com/next"
	vkTarget = vkTargetScene
	textBrowserAddressEditing = true
	vkTextSelected = true
	inputTextBuffer = "http://old.example.com"
	t.Cleanup(func() { textBrowserAddressEditing, vkTextSelected = false, false })
	config := &Config{
		Scenes:    []SceneConfig{{Name: "Tube", Elements: []Element{{Type: "input", Variable: textBrowserURLVariable}}}},
		Variables: Variables{Custom: map[string]interface{}{}},
	}
	appConfig, currentSceneIndex = config, 0
	activeSceneIndex, activeElementIndex = 0, 0
	t.Cleanup(func() {
		inputTextBuffer = ""
		appConfig, currentSceneIndex = prevConfig, prevIndex
	})

	if !clipboardPasteIntoActiveField(config) {
		t.Fatal("the address bar should consume the paste chord")
	}
	if inputTextBuffer != "http://example.com/next" {
		t.Errorf("address buffer = %q, want the replacement not an append", inputTextBuffer)
	}
	if got := config.Variables.Custom[textBrowserURLVariable]; got != "http://example.com/next" {
		t.Errorf("URL variable = %v, want the pasted address", got)
	}
}

// The copy chord copies what the active surface holds: the Notes open field,
// the Terminal command line, the Chat draft, or the buffer an open keyboard
// is editing — and every one of them lands in the history.
func TestClipboardCopyFromActiveSurfaces(t *testing.T) {
	resetClipboardHistory(t)

	// Notes content field.
	spy := &clipboardSpy{}
	installClipboardSpy(t, spy)
	withScenes(t, "Notes")
	resetNotesEditor(t)
	resetClipboardEditingState(t)
	notes.tab = 2
	notes.editContent = "body text"
	if !clipboardCopyFromActiveField(&Config{}) {
		t.Fatal("Notes copy chord should be consumed")
	}
	if spy.set != "body text" {
		t.Errorf("system clipboard = %q, want the open field", spy.set)
	}
	if len(clipHistory) != 1 || clipHistory[0].text != "body text" || clipHistory[0].source != "Notes" {
		t.Fatalf("history = %+v, want the copied note field", clipHistory)
	}

	// An empty open field stays silent: the chord is consumed but copies
	// nothing, so a stray Ctrl+C on an untouched note cannot fabricate a
	// history entry.
	spy.set = "unchanged"
	notes.editContent = ""
	if !clipboardCopyFromActiveField(&Config{}) {
		t.Fatal("an empty open field is still the copy target")
	}
	if spy.set != "unchanged" {
		t.Errorf("empty field copy overwrote the clipboard with %q", spy.set)
	}
	if len(clipHistory) != 1 {
		t.Errorf("empty copy pushed: %+v", clipHistory)
	}

	// Terminal command line.
	withScenes(t, "Terminal")
	resetTerm(t)
	resetClipboardEditingState(t)
	term.input = "ls -la"
	if !clipboardCopyFromActiveField(&Config{}) {
		t.Fatal("the terminal should consume the copy chord")
	}
	if spy.set != "ls -la" {
		t.Errorf("system clipboard = %q, want the command line", spy.set)
	}
	if clipHistory[0].source != "Terminal" {
		t.Errorf("history source = %q, want Terminal", clipHistory[0].source)
	}

	// Chat composer draft.
	resetClipboardEditingState(t)
	chatInputActive = true
	chatInputText = "draft message"
	if !clipboardCopyFromActiveField(&Config{}) {
		t.Fatal("the chat draft should consume the copy chord")
	}
	if spy.set != "draft message" || clipHistory[0].source != "Chat" {
		t.Errorf("clipboard %q source %q, want \"draft message\" from Chat", spy.set, clipHistory[0].source)
	}

	// Nothing being edited: fall through so Ctrl+C keeps its ordinary
	// meaning (there is none in menus, but the chord must not claim keys).
	appConfig.Scenes[0].Name = "Home"
	resetClipboardEditingState(t)
	if clipboardCopyFromActiveField(&Config{}) {
		t.Fatal("with no text surface active the copy chord must fall through")
	}
}

// While an on-screen keyboard is open the chord copies its buffer with a
// source tag that names the surface.
func TestClipboardCopyFromOpenKeyboard(t *testing.T) {
	resetClipboardHistory(t)
	spy := &clipboardSpy{}
	installClipboardSpy(t, spy)
	resetClipboardEditingState(t)
	resetTerm(t)

	virtualKeyboardActive = true
	vkTarget = vkTargetTerm
	term.input = "docker pull"
	if !clipboardCopyFromActiveField(&Config{}) {
		t.Fatal("an open keyboard should consume the copy chord")
	}
	if spy.set != "docker pull" || clipHistory[0].source != "Terminal" {
		t.Errorf("clipboard %q source %q, want the keyboard buffer from Terminal", spy.set, clipHistory[0].source)
	}

	// An empty keyboard buffer is consumed silently.
	term.input = ""
	if !clipboardCopyFromActiveField(&Config{}) {
		t.Fatal("the open keyboard owns the chord even when the buffer is empty")
	}
	if spy.set != "docker pull" {
		t.Errorf("empty buffer copy overwrote the clipboard with %q", spy.set)
	}
	if len(clipHistory) != 1 {
		t.Errorf("empty copy pushed: %+v", clipHistory)
	}
}

// pasted text in the terminal obeys the terminal's printable-ASCII typing
// rule, whatever the system clipboard held.
func TestClipboardPasteRespectsTerminalTypingRule(t *testing.T) {
	resetTerm(t)
	termInsertText("café\n")
	if term.input != "caf" || term.cursor != 3 {
		t.Fatalf("termInsertText produced %q (cursor %d), want \"caf\"", term.input, term.cursor)
	}
}
