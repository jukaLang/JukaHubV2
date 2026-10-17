package main

import "testing"

// resetTerminal gives each test a clean terminal without leaking state into
// the rest of the suite (the terminal state is global, like the rest of the UI).
func resetTerminal(t *testing.T) {
	t.Helper()
	prev := term
	t.Cleanup(func() { term = prev })
	term = terminalState{histIdx: -1}
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

	// New output snaps back to the live end.
	termPushLine("newest", false)
	if term.scrollY != 0 {
		t.Fatalf("scrollY = %d after new output, want 0", term.scrollY)
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
