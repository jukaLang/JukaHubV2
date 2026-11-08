package main

import (
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// resetTerminal gives each test a clean terminal without leaking state into
// the rest of the suite (the terminal state is global, like the rest of the UI).
func resetTerminal(t *testing.T) {
	t.Helper()
	prev, prevPath, prevPending := term, termHistoryPathFn, termPendingCommand
	t.Cleanup(func() { term, termHistoryPathFn, termPendingCommand = prev, prevPath, prevPending })
	term = terminalState{histIdx: -1}
	// A command a test runs must not land in the user's real history file.
	termHistoryPathFn = func() string { return "" }
	termPendingCommand = ""
}

func TestTerminalScrollClamps(t *testing.T) {
	resetTerminal(t)

	for i := 0; i < 30; i++ {
		termPushLine("line", false)
	}

	termScroll(-5)
	if term.scrollY != 0 {
		t.Fatalf("scrollY = %d, want 0 (cannot scroll past the newest line)", term.scrollY)
	}

	termScroll(7)
	if term.scrollY != 7 {
		t.Fatalf("scrollY = %d, want 7", term.scrollY)
	}

	termScroll(1000)
	maxScroll := len(term.lines) - 12
	if term.scrollY != maxScroll {
		t.Fatalf("scrollY = %d, want the clamp %d", term.scrollY, maxScroll)
	}

	// New output while reading older lines keeps the view where it is (the
	// lines on screen do not jump away) and is counted for the "new" pill.
	termPushLine("newest", false)
	if term.scrollY != maxScroll+1 || term.unseen != 1 {
		t.Fatalf("scrollY = %d, unseen = %d after new output, want %d and 1 (view anchored)",
			term.scrollY, term.unseen, maxScroll+1)
	}

	// Following again snaps to the live end, and at the end the view follows.
	termFollow()
	termPushLine("live", false)
	if term.scrollY != 0 || term.unseen != 0 {
		t.Fatalf("scrollY = %d, unseen = %d at the live end, want 0 and 0", term.scrollY, term.unseen)
	}
}

// Running a command is the moment the user wants its output: it snaps the view
// back to the end even when they had scrolled up.
func TestTerminalRunFollowsOutput(t *testing.T) {
	resetTerminal(t)
	prevPath := termHistoryPathFn
	termHistoryPathFn = func() string { return "" }
	t.Cleanup(func() { termHistoryPathFn = prevPath })
	for i := 0; i < 40; i++ {
		termPushLine("line", false)
	}
	termScroll(10)
	termExecute("echo hi")
	if term.scrollY != 0 {
		t.Fatalf("scrollY = %d after running a command, want 0", term.scrollY)
	}
	if got := term.lines[len(term.lines)-1].text; got != "hi" {
		t.Fatalf("last line = %q, want the echo output", got)
	}
}

// The caret and the editing keys work on characters, not bytes: "é" and "ё"
// are two bytes each and must never be cut in half.
func TestTerminalCaretIsRuneAware(t *testing.T) {
	resetTerminal(t)
	termInsertText("café")
	if term.input != "café" || term.cursor != len("café") {
		t.Fatalf("input %q cursor %d, want \"café\" at its end", term.input, term.cursor)
	}
	termCursorLeft()
	if term.cursor != len("caf") {
		t.Fatalf("Left moved the caret to byte %d, want %d (before the é)", term.cursor, len("caf"))
	}
	termInsertText("ё")
	if term.input != "cafёé" {
		t.Fatalf("insert mid-line = %q, want \"cafёé\"", term.input)
	}
	termBackspace()
	termBackspace()
	if term.input != "caé" {
		t.Fatalf("two backspaces = %q, want \"caé\"", term.input)
	}
	termSetCursor(len("ca") + 1) // inside the é: snaps back to its start
	if term.cursor != len("ca") {
		t.Fatalf("a cursor inside a rune was not snapped back: %d", term.cursor)
	}
	termDeleteRight()
	if term.input != "ca" {
		t.Fatalf("Delete = %q, want \"ca\"", term.input)
	}
	termInsertText("t\x00\n\tx")
	if term.input != "catx" {
		t.Fatalf("control characters were inserted: %q", term.input)
	}
}

// Word jumps and Ctrl+Backspace stop at spaces.
func TestTerminalWordEditing(t *testing.T) {
	resetTerminal(t)
	termInsertText("ls -la  roms")
	if got := termWordStart(term.input, len(term.input)); got != len("ls -la  ") {
		t.Fatalf("word start = %d, want %d", got, len("ls -la  "))
	}
	if got := termWordEnd(term.input, 0); got != len("ls") {
		t.Fatalf("word end = %d, want %d", got, len("ls"))
	}
	termDeleteWordLeft()
	if term.input != "ls -la  " {
		t.Fatalf("Ctrl+Backspace = %q", term.input)
	}
	termClearLine()
	if term.input != "" || term.cursor != 0 {
		t.Fatalf("clear line left %q at %d", term.input, term.cursor)
	}
}

// The caret blinks 530 ms on / 530 ms off, is solid right after an edit and is
// hidden while the history picker has focus.
func TestTerminalCaretBlink(t *testing.T) {
	resetTerminal(t)
	prevHist, prevEngine := termHist, focusEngine
	t.Cleanup(func() { termHist, focusEngine = prevHist, prevEngine })
	termHist = termHistorySheet{}
	focusEngine = NewFocusEngine()
	now := time.Now()
	term.caretAt = now
	if !termCaretOn(now.Add(100 * time.Millisecond)) {
		t.Fatal("the caret is not solid right after an edit")
	}
	if termCaretOn(now.Add(termCaretBlink + 10*time.Millisecond)) {
		t.Fatal("the caret did not blink off after 530 ms")
	}
	if !termCaretOn(now.Add(2*termCaretBlink + 10*time.Millisecond)) {
		t.Fatal("the caret did not come back on")
	}
	termHist.open = true
	if termCaretOn(now) {
		t.Fatal("the caret is drawn while the history picker has focus")
	}
}

// Program output is cleaned for the screen: colours stripped, a \r progress
// bar keeps its last frame, tabs become spaces.
func TestTermCleanOutput(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"\x1b[1;32mgreen\x1b[0m text", "green text"},
		{"10%\r50%\r100%", "100%"},
		{"line\r", "line"},
		{"a\tb", "a    b"},
		{"\x1b]0;title\x07after", "after"},
		{"bad \xff byte", "bad ? byte"},
	}
	for _, c := range cases {
		if got := termCleanOutput(c.in); got != c.want {
			t.Errorf("termCleanOutput(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// B leaves the Terminal (with nothing else open), and with the history picker
// open B closes the picker first. Y opens it and A puts the command back in
// the prompt without running it.
func TestTerminalPadControls(t *testing.T) {
	resetTerminal(t)
	prevHist, prevEngine, prevHistory := termHist, focusEngine, sceneHistory
	t.Cleanup(func() { termHist, focusEngine, sceneHistory = prevHist, prevEngine, prevHistory })
	termHist = termHistorySheet{}
	focusEngine = NewFocusEngine()
	sceneHistory = nil
	isolateSceneSwitch(t)
	config := &Config{Scenes: []SceneConfig{{Name: "Home", Layout: "home"}, {Name: "Terminal"}}}
	prevIdx := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prevIdx })
	currentSceneIndex = 1

	term.history = []string{"ls", "uname -a"}
	press := func(b uint8) bool {
		return ctrlTerminal(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: b}, config)
	}
	press(sdl.CONTROLLER_BUTTON_Y)
	if !termHist.open {
		t.Fatal("Y did not open the history picker")
	}
	press(sdl.CONTROLLER_BUTTON_A)
	if termHist.open || term.input != "uname -a" {
		t.Fatalf("A in the picker: open=%v input=%q, want closed with the newest command typed", termHist.open, term.input)
	}
	if len(term.lines) != 0 {
		t.Fatalf("A in the picker ran the command: %v", term.lines)
	}

	press(sdl.CONTROLLER_BUTTON_X)
	if term.input != "" {
		t.Fatalf("X did not clear the line: %q", term.input)
	}

	press(sdl.CONTROLLER_BUTTON_Y)
	press(sdl.CONTROLLER_BUTTON_B)
	if termHist.open {
		t.Fatal("B did not close the picker")
	}
	if transitionPhase != "none" {
		t.Fatal("B with the picker open left the screen instead of closing the picker")
	}

	press(sdl.CONTROLLER_BUTTON_B)
	if transitionPhase != "fade-out" || pendingSceneIndex != 0 {
		t.Fatalf("B did not leave the Terminal (phase %q, pending %d)", transitionPhase, pendingSceneIndex)
	}
}

func TestTerminalHistoryNavigation(t *testing.T) {
	resetTerminal(t)
	term.history = []string{"ls", "pwd", "neofetch"}

	term.input = "half typed"
	termHistoryStep(-1)
	if term.input != "neofetch" {
		t.Fatalf("first Up = %q, want neofetch", term.input)
	}
	if term.cursor != len(term.input) {
		t.Fatalf("cursor = %d, want the end of the recalled command", term.cursor)
	}

	termHistoryStep(-1)
	termHistoryStep(-1)
	if term.input != "ls" {
		t.Fatalf("Up x3 = %q, want the oldest command", term.input)
	}
	termHistoryStep(-1)
	if term.input != "ls" {
		t.Fatalf("Up past the oldest command = %q, want it to stay put", term.input)
	}

	termHistoryStep(1)
	if term.input != "pwd" {
		t.Fatalf("Down = %q, want pwd", term.input)
	}
	termHistoryStep(1)
	termHistoryStep(1)
	if term.input != "" || term.histIdx != -1 {
		t.Fatalf("Down past the newest command = %q (idx %d), want an empty fresh line", term.input, term.histIdx)
	}
}

// The on-screen keyboard must edit the terminal's own input buffer; before the
// dedicated target existed it could not type a command at all.
func TestKeyboardTerminalTargetEditsCommandLine(t *testing.T) {
	resetTerminal(t)
	prevTarget := vkTarget
	t.Cleanup(func() { vkTarget = prevTarget })

	vkTarget = vkTargetTerm
	term.input = "ls"
	term.cursor = 2

	if got := vkCurrentText(); got != "ls" {
		t.Fatalf("vkCurrentText = %q, want the terminal input", got)
	}

	vkInsertText(" -la", nil)
	if term.input != "ls -la" {
		t.Fatalf("after typing: %q, want %q", term.input, "ls -la")
	}
	if term.cursor != len("ls -la") {
		t.Fatalf("cursor = %d, want %d", term.cursor, len("ls -la"))
	}

	vkBackspace(nil)
	if term.input != "ls -l" {
		t.Fatalf("after backspace: %q, want %q", term.input, "ls -l")
	}

	if vkTitle() != "COMMAND" {
		t.Fatalf("vkTitle = %q, want COMMAND", vkTitle())
	}
}

// termOpenWithCommand (the contract the Tools hub and the Toolbox use) types a
// command into the prompt and never runs it: on the Terminal itself it
// replaces the line at once; from another screen the Terminal's entry hook
// types it.
func TestTermOpenWithCommandTypesWithoutRunning(t *testing.T) {
	resetTerminal(t)
	prevIdx, prevHist, prevEngine, prevSelected := currentSceneIndex, termHist, focusEngine, selectedButtonIndex
	t.Cleanup(func() {
		currentSceneIndex, termHist, focusEngine, selectedButtonIndex = prevIdx, prevHist, prevEngine, prevSelected
	})
	termHist = termHistorySheet{}
	focusEngine = NewFocusEngine()
	config := &Config{Scenes: []SceneConfig{{Name: "Home"}, {Name: "Terminal"}}}
	currentSceneIndex = 1
	term.input, term.cursor = "old line", len("old line")

	termOpenWithCommand(config, "  ls -la\n/mnt/SDCARD\t")
	if term.input != "ls -la /mnt/SDCARD" || term.cursor != len(term.input) {
		t.Fatalf("prompt = %q (caret %d), want the command typed with the caret at its end", term.input, term.cursor)
	}
	if len(term.lines) != 0 || term.job != nil {
		t.Fatalf("the command ran: %v", term.lines)
	}

	term.history = []string{"ls"} // nothing to load from disk
	termPendingCommand = "cat notes.txt"
	termInit()
	if term.input != "cat notes.txt" || termPendingCommand != "" {
		t.Fatalf("after entry the prompt is %q (pending %q), want the command typed", term.input, termPendingCommand)
	}
	for _, l := range term.lines {
		if l.kind == termKindEcho {
			t.Fatalf("entering the Terminal ran the pending command: %q", l.text)
		}
	}
}

// Submitting the terminal keyboard runs the command and clears the line.
func TestKeyboardTerminalSubmitRunsCommand(t *testing.T) {
	resetTerminal(t)
	prevTarget := vkTarget
	t.Cleanup(func() { vkTarget = prevTarget })

	vkTarget = vkTargetTerm
	term.input = "echo hello"
	vkSubmit(nil, false)

	if term.input != "" {
		t.Fatalf("input = %q after submit, want an empty line", term.input)
	}
	foundPrompt, foundOutput := false, false
	for _, l := range term.lines {
		if l.text == termPrompt+"echo hello" {
			foundPrompt = true
		}
		if l.text == "hello" {
			foundOutput = true
		}
	}
	if !foundPrompt || !foundOutput {
		t.Fatalf("command did not run (prompt=%v output=%v), lines=%v", foundPrompt, foundOutput, term.lines)
	}
}
