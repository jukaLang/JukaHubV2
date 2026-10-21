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

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	entries, err := os.ReadDir(".")
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
