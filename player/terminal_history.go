package main

// terminal_history.go — persistent command history and completion for the
// built-in terminal.
//
// The terminal used to keep its history in memory only: leave the scene (or
// restart JukaHub) and the commands someone typed to fix their Wi-Fi were gone.
// On a handheld that is the worst place to lose them, because typing them again
// means driving the on-screen keyboard with a D-pad.
//
// So history is written to one small file in the app's data directory, newest
// last, with a hard cap. Writes are best-effort: a read-only card must not stop
// a command from running, and a failed write is a log line, not a dialog.
//
// Completion is deliberately simple and local. It never touches the network and
// never executes anything: it reads the built-in command list, the current
// directory and the saved history.
//
// The history picker (Y on the pad, Ctrl+R or F7 on a keyboard) lists the saved
// commands newest first, so a long command typed once with the on-screen
// keyboard runs again with two presses. Its drawing is in terminal_ui.go.

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// termHistoryFileName is one command per line in the data directory.
	termHistoryFileName = "terminal_history"
	// termHistoryMax bounds both the file and the in-memory list. A shell
	// history bigger than this is not useful on a 5-inch screen.
	termHistoryMax = 200
	// termCompleteMaxEntries bounds the directory read: a folder with 20k ROMs
	// must not stall the terminal on every Tab.
	termCompleteMaxEntries = 200
	// termCompleteMaxListed is how many candidates are printed before the
	// terminal summarises them as a count.
	termCompleteMaxListed = 12
)

// termHistoryPathFn is a variable so tests can use a temporary directory.
var termHistoryPathFn = func() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, termHistoryFileName)
}

// parseTermHistory turns the history file into a clean list: blank lines and
// CRLF endings removed, duplicates collapsed, capped to the newest entries.
func parseTermHistory(data string) []string {
	var out []string
	for _, line := range strings.Split(data, "\n") {
		cmd := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if cmd == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1] == cmd {
			continue
		}
		out = append(out, cmd)
	}
	if len(out) > termHistoryMax {
		out = out[len(out)-termHistoryMax:]
	}
	return out
}

// formatTermHistory renders the list for the file, newest last.
func formatTermHistory(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > termHistoryMax {
		lines = lines[len(lines)-termHistoryMax:]
	}
	return strings.Join(lines, "\n") + "\n"
}

// termLoadHistory reads the saved history. A missing or unreadable file is an
// empty history, never an error the user has to deal with.
func termLoadHistory() []string {
	path := termHistoryPathFn()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseTermHistory(string(data))
}

// termSaveHistory writes the history atomically, so a power cut mid-write
// cannot leave a truncated command that the next session would try to replay.
func termSaveHistory(lines []string) error {
	path := termHistoryPathFn()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return AtomicWrite(path, []byte(formatTermHistory(lines)), 0o644)
}

// termRecordHistory appends a command to the in-memory history and persists it.
// A repeat of the previous command is not stored twice; that is what makes
// pressing Up useful after running the same command on purpose.
func termRecordHistory(cmd string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return
	}
	if n := len(term.history); n > 0 && term.history[n-1] == cmd {
		return
	}
	term.history = append(term.history, cmd)
	if len(term.history) > termHistoryMax {
		term.history = term.history[len(term.history)-termHistoryMax:]
	}
	if err := termSaveHistory(term.history); err != nil {
		Log().Warn("terminal: could not save history", "err", err)
	}
}

// termBuiltinCommands is the command list completion offers. It mirrors the
// help text; a command that is not here can still be typed and run.
var termBuiltinCommands = []string{
	"calc", "cat", "cd", "clear", "date", "dir", "echo", "exit",
	"help", "history", "hostname", "ls", "neofetch", "pwd", "ssh",
	"sysinfo", "uptime", "whoami",
}

// termSplitForCompletion splits input into the part that stays and the word
// being completed, keeping the trailing space the user typed.
func termSplitForCompletion(input string) (head, word string) {
	if i := strings.LastIndexByte(input, ' '); i >= 0 {
		return input[:i+1], input[i+1:]
	}
	return "", input
}

// termCompletions returns the completed input (or the input unchanged when
// there is nothing to add) and the matching candidates, sorted.
//
// The first word completes against the built-in commands and the saved history.
// Any later word completes against the current directory, so `cat sav` finishes
// `saves/` and `cd rom` finishes `roms/`. A single match is inserted with a
// trailing space for commands, or as-is for paths (leaving a directory's slash
// so the next Tab continues inside it).
func termCompletions(input string, dirEntries, history []string) (string, []string) {
	head, word := termSplitForCompletion(input)
	var pool []string
	firstWord := head == ""
	if firstWord {
		pool = append(pool, termBuiltinCommands...)
		pool = append(pool, history...)
	} else {
		pool = append(pool, dirEntries...)
	}

	seen := map[string]bool{}
	var matches []string
	for _, candidate := range pool {
		if candidate == "" || seen[candidate] {
			continue
		}
		match := strings.HasPrefix(candidate, word)
		if firstWord {
			// Commands and history are typed in lower case, but a user who
			// starts with a capital still expects a completion.
			match = match || strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(word))
		}
		if !match {
			continue
		}
		seen[candidate] = true
		matches = append(matches, candidate)
	}
	sort.Strings(matches)

	switch len(matches) {
	case 0:
		return input, nil
	case 1:
		completed := head + matches[0]
		if firstWord && !strings.Contains(matches[0], " ") {
			completed += " "
		}
		return completed, matches
	}
	// Several matches: extend to the longest common prefix so Tab keeps making
	// progress, and let the caller print the list.
	prefix := matches[0]
	for _, m := range matches[1:] {
		for !strings.HasPrefix(m, prefix) && prefix != "" {
			prefix = trimLastRune(prefix)
		}
	}
	if len(prefix) > len(word) {
		return head + prefix, matches
	}
	return input, matches
}

// termDirectoryEntries lists the current directory for completion, with a
// trailing slash on directories. It is bounded and silent on error.
func termDirectoryEntries() []string {
	dir := term.cwd
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for i, e := range entries {
		if i >= termCompleteMaxEntries {
			break
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		out = append(out, name)
	}
	return out
}

// termComplete is the Tab handler: complete the current input, or open the
// on-screen keyboard when the line is empty (the handheld has no hardware
// keyboard, so an empty Tab is still the way to type).
func termComplete() {
	if strings.TrimSpace(term.input) == "" {
		vkOpenTerminal()
		return
	}
	completed, matches := termCompletions(term.input, termDirectoryEntries(), term.history)
	if completed != term.input {
		term.input = completed
		term.cursor = len(completed)
		term.histIdx = -1
		return
	}
	if len(matches) == 0 {
		return
	}
	if len(matches) > termCompleteMaxListed {
		termPushLine("  "+strings.Join(matches[:termCompleteMaxListed], "  ")+"  …", false)
		termPushLine("  "+strconv.Itoa(len(matches))+" matches", false)
		return
	}
	termPushLine("  "+strings.Join(matches, "  "), false)
}

// ── History picker ───────────────────────────────────────────────────────

// termHistoryFilterMax bounds the picker's search text.
const termHistoryFilterMax = 120

// termHistorySheet is the picker's state. Render thread only.
type termHistorySheet struct {
	open bool
	// cursor indexes matches; first is the first row drawn (the renderer
	// keeps the cursor in view and writes it back).
	cursor int
	first  int
	filter string
	// matches are indices into term.history, newest first, that contain the
	// filter (case-insensitive). Rebuilt when either changes.
	matches []int
	// rows are the drawn rows' hit boxes; rowsFirst is the list index of
	// rows[0]. card is the sheet itself; a click outside it closes the picker.
	rows      []sdl.Rect
	rowsFirst int
	card      sdl.Rect
	// visible is how many rows the last frame had room for (L1/R1 paging).
	visible int
}

var termHist termHistorySheet

// termHistoryRefilter rebuilds the match list and keeps the cursor on it.
func termHistoryRefilter() {
	termHist.matches = termHist.matches[:0]
	needle := strings.ToLower(termHist.filter)
	for i := len(term.history) - 1; i >= 0; i-- {
		if needle == "" || strings.Contains(strings.ToLower(term.history[i]), needle) {
			termHist.matches = append(termHist.matches, i)
		}
	}
	if termHist.cursor >= len(termHist.matches) {
		termHist.cursor = len(termHist.matches) - 1
	}
	if termHist.cursor < 0 {
		termHist.cursor = 0
	}
}

// termHistoryOpen shows the picker on the newest command.
func termHistoryOpen() {
	termHist.open = true
	termHist.cursor = 0
	termHist.first = 0
	termHist.filter = ""
	termHist.rows = nil
	termHistoryRefilter()
	PlayActivateSound()
}

// termHistoryClose hides the picker without touching the prompt.
func termHistoryClose() {
	termHist.open = false
	termHist.filter = ""
	termHist.rows = nil
	termHist.card = sdl.Rect{}
}

// termHistorySelected returns the highlighted command and its history index.
func termHistorySelected() (string, int, bool) {
	if termHist.cursor < 0 || termHist.cursor >= len(termHist.matches) {
		return "", -1, false
	}
	idx := termHist.matches[termHist.cursor]
	if idx < 0 || idx >= len(term.history) {
		return "", -1, false
	}
	return term.history[idx], idx, true
}

// termHistoryInsert puts the highlighted command in the prompt to be read and
// edited (A, Enter). It is not run.
func termHistoryInsert() {
	cmd, _, ok := termHistorySelected()
	termHistoryClose()
	if !ok {
		PlayBackSound()
		return
	}
	termClearLine()
	termInsertText(cmd)
	termFocusPrompt()
	PlayActivateSound()
}

// termHistoryRun runs the highlighted command straight away (Start, Ctrl+Enter).
func termHistoryRun() {
	cmd, _, ok := termHistorySelected()
	termHistoryClose()
	if !ok {
		PlayBackSound()
		return
	}
	PlayActivateSound()
	termClearLine()
	termFocusPrompt()
	termExecute(cmd)
}

// termHistoryDelete forgets the highlighted command, in memory and on disk
// (X, Delete), so a typo never comes back in completion either.
func termHistoryDelete() {
	_, idx, ok := termHistorySelected()
	if !ok {
		return
	}
	term.history = append(term.history[:idx], term.history[idx+1:]...)
	term.histIdx = -1
	if err := termSaveHistory(term.history); err != nil {
		Log().Warn("terminal: could not save history", "err", err)
	}
	termHistoryRefilter()
	PlayBackSound()
}

// termHistoryMove moves the highlight, clamped to the list.
func termHistoryMove(delta int) {
	n := len(termHist.matches)
	if n == 0 {
		return
	}
	next := termHist.cursor + delta
	if next < 0 {
		next = 0
	}
	if next > n-1 {
		next = n - 1
	}
	if next != termHist.cursor {
		termHist.cursor = next
		PlayNavSound()
	}
}

// termHistoryPage is one L1/R1 or PgUp/PgDn step in the picker.
func termHistoryPage() int {
	if termHist.visible > 1 {
		return termHist.visible - 1
	}
	return 5
}

// termHistoryKey is the keyboard while the picker is open: arrows choose,
// Enter inserts, Ctrl+Enter runs, Delete forgets, typing filters, Escape closes.
func termHistoryKey(e *sdl.KeyboardEvent) {
	ctrl := e.Keysym.Mod&sdl.KMOD_CTRL != 0
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_F7:
		termHistoryClose()
		PlayBackSound()
	case sdl.K_r:
		if ctrl {
			termHistoryClose()
			PlayBackSound()
		}
	case sdl.K_UP:
		termHistoryMove(-1)
	case sdl.K_DOWN:
		termHistoryMove(1)
	case sdl.K_PAGEUP:
		termHistoryMove(-termHistoryPage())
	case sdl.K_PAGEDOWN:
		termHistoryMove(termHistoryPage())
	case sdl.K_HOME:
		termHistoryMove(-len(termHist.matches))
	case sdl.K_END:
		termHistoryMove(len(termHist.matches))
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		if ctrl {
			termHistoryRun()
		} else {
			termHistoryInsert()
		}
	case sdl.K_TAB, sdl.K_RIGHT:
		termHistoryInsert()
	case sdl.K_DELETE:
		termHistoryDelete()
	case sdl.K_BACKSPACE:
		if termHist.filter != "" {
			termHist.filter = trimLastRune(termHist.filter)
			termHistoryRefilter()
		}
	}
}

// termHistoryType adds typed text to the picker's filter.
func termHistoryType(text string) {
	var b strings.Builder
	for _, r := range text {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 || len(termHist.filter)+b.Len() > termHistoryFilterMax {
		return
	}
	termHist.filter += b.String()
	termHist.cursor = 0
	termHist.first = 0
	termHistoryRefilter()
}

// termHistoryPad is the pad while the picker is open. It is modal: every
// button is consumed so nothing reaches the terminal behind it.
func termHistoryPad(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_Y:
		termHistoryClose()
		PlayBackSound()
	case sdl.CONTROLLER_BUTTON_A:
		termHistoryInsert()
	case sdl.CONTROLLER_BUTTON_START:
		termHistoryRun()
	case sdl.CONTROLLER_BUTTON_X:
		termHistoryDelete()
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		termHistoryMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		termHistoryMove(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		termHistoryMove(-termHistoryPage())
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		termHistoryMove(termHistoryPage())
	}
	return true
}

// termHistoryClick selects a row, inserts the row that is already selected,
// and closes the picker on a click outside it.
func termHistoryClick(mx, my int32) bool {
	if termHist.card.W <= 0 || !pointInRect(mx, my, termHist.card) {
		termHistoryClose()
		PlayBackSound()
		return true
	}
	for i, r := range termHist.rows {
		if !pointInRect(mx, my, r) {
			continue
		}
		idx := termHist.rowsFirst + i
		if idx == termHist.cursor {
			termHistoryInsert()
		} else if idx >= 0 && idx < len(termHist.matches) {
			termHist.cursor = idx
			PlayNavSound()
		}
		return true
	}
	return true
}
