package main

// cmddeck_chips.go — whole-token chips above the on-screen keyboard while it
// types into the Terminal.
//
// The keyboard's suggestion strip normally offers English words, which is no
// help in a shell. For the Terminal it offers tokens instead, up to five:
//
//   - history continuations: the next word of a command run before that starts
//     with what is typed (on an empty line, whole recent commands);
//   - subcommands and flags from a small built-in table (ls -la, du -sh,
//     tail -n, ping -c, ip addr …);
//   - paths for commands that take one (cd, ls, cat, tail, du, find …), or as
//     soon as the word has a slash: folders end in "/" so the next chip
//     continues inside them.
//
// The candidates come from terminal_history.go's completion (termCompletions
// does the prefix matching for commands, history and paths); this file only
// orders them and adds the table. A chip replaces the word at the caret and
// adds a space. Select on the pad takes the first chip (Up from the keys
// walks into the strip to take any of them, like the word suggestions);
// a click or tap takes the one under it.

import (
	"os"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	termChipMax    = 5
	termChipDirTTL = 3 * time.Second
	// termChipHistoryMax is how many chips history may take, so the table
	// and paths still get a say.
	termChipHistoryMax = 2
)

// termChipTable is the built-in subcommand/flag table, per command.
var termChipTable = map[string][]string{
	"ls":       {"-la", "-lh", "-a", "-R"},
	"du":       {"-sh", "-h", "-a"},
	"df":       {"-h", "-m"},
	"tail":     {"-n", "-f", "50", "200"},
	"head":     {"-n", "20"},
	"ping":     {"-c", "4", "1.1.1.1", "google.com"},
	"ps":       {"aux", "w", "-ef"},
	"kill":     {"-9", "-15"},
	"find":     {"-iname", "-type f", "-type d", "-mtime -1", "-size +100M"},
	"grep":     {"-i", "-r", "-n", "-v", "error"},
	"ip":       {"addr", "route", "link", "-br addr"},
	"iw":       {"dev", "wlan0", "link", "scan"},
	"dmesg":    {"| tail -n 40", "-w"},
	"free":     {"-m", "-h"},
	"top":      {"-b -n 1", "-n 1"},
	"cat":      {"/proc/cpuinfo", "/proc/meminfo", "/etc/os-release"},
	"cd":       {"..", "~", "-"},
	"uname":    {"-a", "-r"},
	"netstat":  {"-tln", "-an"},
	"chmod":    {"+x", "755", "644"},
	"rm":       {"-r", "-f"},
	"cp":       {"-r"},
	"tar":      {"-xf", "-tf", "-czf"},
	"unzip":    {"-l", "-o"},
	"curl":     {"-I", "-L", "-o"},
	"wget":     {"-q", "-O"},
	"dir":      {"/s", "/b", "/a", "/o-d"},
	"ipconfig": {"/all"},
	"netsh":    {"wlan show interfaces", "interface show interface"},
	"tasklist": {"/v", "/fi"},
	"taskkill": {"/PID", "/IM", "/F"},
	"findstr":  {"/i", "/s", "/n"},
}

// termChipPathCommands take a path argument.
var termChipPathCommands = map[string]bool{
	"cd": true, "ls": true, "cat": true, "tail": true, "head": true, "du": true,
	"find": true, "rm": true, "cp": true, "mv": true, "less": true, "more": true,
	"grep": true, "type": true, "dir": true, "chmod": true, "tar": true,
	"unzip": true, "file": true, "stat": true, "md5sum": true, "sha256sum": true,
}

// termChipCommandOrder is the first-word list, commonest first.
func termChipCommandOrder() []string {
	if runtime.GOOS == "windows" {
		return []string{"dir", "cd", "type", "ping", "ipconfig", "tasklist", "taskkill", "netsh", "findstr", "cls", "help", "sysinfo"}
	}
	return []string{"ls", "cd", "cat", "tail", "df", "du", "ping", "ps", "free", "kill", "grep", "find", "ip", "iw", "dmesg", "top", "uname", "clear", "help", "sysinfo"}
}

// termChipCommandNames are the programs the probe looks up for the chips.
func termChipCommandNames() []string {
	return termChipCommandOrder()
}

// termChips is the strip's current row. Render thread only.
var termChips struct {
	valid bool
	text  string
	caret int
	n     int
	toks  [termChipMax]string
	disp  [termChipMax]string
	path  [termChipMax]bool // from the folder listing

	// What the row was built from besides the text: a new history entry or a
	// cd changes the chips for the same text.
	hist int
	cwd  string

	dirKey     string
	dirAt      time.Time
	dirNames   []string
	dirLoading bool
	dirGen     uint64
}

// termReadDirNames lists up to max names of dir, folders with a trailing
// slash. It reads at most max entries from the directory (a folder of 20 000
// ROMs costs one bounded read, not a full listing).
func termReadDirNames(dir string, max int) []string {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	entries, _ := f.ReadDir(max)
	_ = f.Close()
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		out = append(out, name)
	}
	return out
}

// termChipDirNames is termReadDirNames with a short cache, so typing a path
// does not read the folder on every key. The read runs on a goroutine (a card
// folder can stall the frame); until it lands the chips show what is cached,
// and the answer rebuilds the row.
func termChipDirNames(dir string) []string {
	if dir == termChips.dirKey {
		if termChips.dirLoading || time.Since(termChips.dirAt) < termChipDirTTL {
			return termChips.dirNames
		}
	} else {
		termChips.dirNames = nil
	}
	termChips.dirKey = dir
	termChips.dirAt = time.Now()
	termChips.dirLoading = true
	termChips.dirGen++
	gen := termChips.dirGen
	go func() {
		names := termReadDirNames(dir, termCompleteMaxEntries)
		tbxPost(func() {
			if termChips.dirGen != gen {
				return
			}
			termChips.dirNames = names
			termChips.dirAt = time.Now()
			termChips.dirLoading = false
			termChips.valid = false
		})
	}()
	return termChips.dirNames
}

// termChipHave reports whether a first-word command can run here.
func termChipHave(cmd string) bool {
	for _, b := range termBuiltinCommands {
		if b == cmd {
			return true
		}
	}
	if !cmdDeck.avail.ready {
		return true
	}
	v, known := cmdDeck.avail.progs[cmd]
	return !known || v
}

// termChipsSync refreshes the row when the text or caret changed. It runs
// once per frame from the keyboard; unchanged, it is two comparisons.
func termChipsSync() {
	text := vkCurrentText()
	caret := vkCaretIndex(text)
	if termChips.valid && termChips.text == text && termChips.caret == caret &&
		termChips.hist == len(term.history) && termChips.cwd == term.cwd {
		return
	}
	termChips.valid, termChips.text, termChips.caret = true, text, caret
	termChips.hist, termChips.cwd = len(term.history), term.cwd
	termChips.n = 0
	for i := range termChips.toks {
		termChips.toks[i], termChips.disp[i], termChips.path[i] = "", "", false
	}
	off := vkRuneOffset(text, caret)
	before := text[:off]
	head, word := termSplitForCompletion(before)
	add := func(tok, disp string, isPath bool) {
		if tok == "" || tok == word || termChips.n >= termChipMax {
			return
		}
		for i := 0; i < termChips.n; i++ {
			if termChips.toks[i] == tok {
				return
			}
		}
		if disp == "" {
			disp = tok
		}
		termChips.toks[termChips.n], termChips.disp[termChips.n] = tok, disp
		termChips.path[termChips.n] = isPath
		termChips.n++
	}
	history := term.history

	if head == "" {
		// First word: recent whole commands that start with it, then the
		// common commands, then the rest of what completion matches.
		_, matches := termCompletions(before, nil, history)
		inMatches := make(map[string]bool, len(matches))
		for _, m := range matches {
			inMatches[m] = true
		}
		fromHistory := 0
		for i := len(history) - 1; i >= 0 && fromHistory < termChipHistoryMax; i-- {
			h := history[i]
			if inMatches[h] && strings.Contains(h, " ") {
				add(h, "", false)
				fromHistory++
			}
		}
		lw := strings.ToLower(word)
		for _, c := range termChipCommandOrder() {
			if strings.HasPrefix(c, lw) && termChipHave(c) {
				add(c, "", false)
			}
		}
		for _, m := range matches {
			if !strings.Contains(m, " ") {
				add(m, "", false)
			}
		}
		return
	}

	fields := strings.Fields(head)
	if len(fields) == 0 {
		return // only spaces before the word: nothing to complete against
	}
	cmd := strings.ToLower(fields[0])
	// History: the next word of an earlier command that continues this one.
	fromHistory := 0
	for i := len(history) - 1; i >= 0 && fromHistory < termChipHistoryMax; i-- {
		h := history[i]
		if len(h) <= len(before) || !strings.HasPrefix(h, before) {
			continue
		}
		rest := h[len(before):]
		if j := strings.IndexByte(rest, ' '); j >= 0 {
			rest = rest[:j]
		}
		if rest != "" {
			add(word+rest, "", false)
			fromHistory++
		}
	}
	// The table's flags and subcommands not already on the line.
	for _, f := range termChipTable[cmd] {
		if strings.HasPrefix(f, word) && !strings.Contains(" "+head, " "+f+" ") {
			add(f, "", false)
		}
	}
	// Paths, through the completion's own prefix match.
	if termChipPathCommands[cmd] || strings.ContainsAny(word, `/\`) || strings.HasPrefix(word, "~") {
		dirPart := ""
		if i := strings.LastIndexAny(word, `/\`); i >= 0 {
			dirPart = word[:i+1]
		}
		dir := term.cwd
		if dirPart != "" {
			resolved, err := termPath(dirPart)
			if err != nil {
				return
			}
			dir = resolved
		}
		names := termChipDirNames(dir)
		prefixed := make([]string, 0, len(names))
		for _, n := range names {
			prefixed = append(prefixed, dirPart+n)
		}
		_, matches := termCompletions(head+word, prefixed, nil)
		for _, m := range matches {
			add(m, strings.TrimPrefix(m, dirPart), true)
		}
	}
}

// termChipText is chip i ("" when empty) and what its cell shows.
func termChipText(i int) (string, string) {
	if i < 0 || i >= termChips.n {
		return "", ""
	}
	return termChips.toks[i], termChips.disp[i]
}

// termChipApply puts chip i in place of the word at the caret, plus a space
// (none after a folder, so the next chip continues inside it). A name that
// needs quoting goes in quoted and is final.
func termChipApply(i int, config *Config) bool {
	tok, _ := termChipText(i)
	if tok == "" {
		return false
	}
	text := vkCurrentText()
	caret := vkCaretIndex(text)
	off := vkRuneOffset(text, caret)
	before, after := text[:off], text[off:]
	head, _ := termSplitForCompletion(before)
	isPath := strings.HasSuffix(tok, "/") || strings.HasSuffix(tok, `\`)
	// The built-in cd joins its words raw (termChangeDir), so a quoted name
	// would be looked up with its quotes; cd takes the name as it is.
	builtinCd := false
	if f := strings.Fields(head); len(f) > 0 && strings.EqualFold(f[0], "cd") {
		builtinCd = true
	}
	if termChips.path[i] && !builtinCd && strings.ContainsAny(tok, " '\"&;|()$`!*?[]{}<>^%,=") {
		style := cmdQuoteSh
		if runtime.GOOS == "windows" {
			style = cmdQuoteCmd
		}
		if q, ok := cmdDeckQuote(strings.TrimRight(tok, `/\`), style); ok {
			tok, isPath = q, false
		}
	}
	next := head + tok
	if !isPath && !strings.HasPrefix(after, " ") {
		next += " "
	}
	caretAt := utf8.RuneCountInString(next)
	next += after
	if len(next) > termMaxInput {
		return false
	}
	vkTextSelected = false
	vkSetText(next, config)
	vkSetCaret(next, caretAt)
	termChips.valid = false
	return true
}
