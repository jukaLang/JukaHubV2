package main

// ftp_server_locks.go — which folders FTP may read but not change, and why.
//
// FTP is plaintext: a password read off the network must not be enough to
// change a program the handheld runs. The shares never include the card's
// program folders (Apps, Emus, System…), but some folders inside them hold
// programs too, and those are read-only over FTP (and the Phone Companion's
// Files card); SFTP can change them.
//
// What counts is what actually runs (ftpRunDirs):
//   - PortMaster's folders (ports.go portRoots), whose scripts the Ports panel
//     starts with bash;
//   - the game profiles' folders (gameprofiles.go), each of which runs the
//     program in it;
//   - the Windows programs' folders (windowsapps.go windowsAppRoots), whose
//     .exe files run through Wine with the whole card in reach;
//   - the ROM folders of every system whose launcher executes the game it is
//     handed (ftpSystemRunsGames): a "scripts" or "apps" system on some
//     firmwares, whose games are shell scripts. A launcher is read for a
//     command that runs its argument; a system whose config.json lists script
//     extensions, or whose name every firmware gives such a system, counts
//     without that.
//
// The running server's set follows the card (ftpLiveLocks): it is derived
// again when the game-profile cache has been rebuilt since the last look, and
// at least every ftpLiveLockTTL otherwise (an Emus folder edited over SFTP).
//
// Symlinks. Every share is an os.Root, so no link can leave it, but a link on
// an ext4 card can lead from one folder of a share into a locked one ("Roms/x"
// → "Ports"), and a write through it would land there. resolvePath therefore
// also resolves the path it was given through every link it crosses
// (ftpRealPath) and refuses a write whose real target is locked, lies outside
// every share, or cannot be worked out.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// ftpLiveLockTTL bounds how old a running server's locked set (and the
	// Files card's) can be when the profile cache has not changed. The set and
	// the cached ftpRunDirs answer it is derived from (ftpRunDirsCached) are
	// each trusted for half of it, so a new launcher counts within a minute.
	ftpLiveLockTTL = time.Minute
	// ftpLauncherReadMax bounds the read of one launch script: its first 64
	// KiB are read, however long its lines.
	ftpLauncherReadMax = 64 << 10
	// ftpEmuFoldersMax bounds the folders read in one Emus root.
	ftpEmuFoldersMax = 512
	// ftpRealPathSteps bounds ftpRealPath's walk: parents peeled off plus
	// dangling links followed.
	ftpRealPathSteps = 64
)

// ftpRunSystemNames are the system folders (Emus/<name>, Roms/<name>) that run
// their games on every firmware that has them, whatever their launcher says.
var ftpRunSystemNames = []string{"PORTS", "PORT", "SCRIPTS", "SCRIPT", "SH", "SHELL", "APPS", "APP"}

// ftpRunExtensions are game extensions that are programs, not data: a system
// whose config.json "extlist" names one runs what it is given.
var ftpRunExtensions = map[string]bool{"sh": true, "bash": true, "py": true, "pl": true, "lua": true, "love": true, "run": true}

// ftpLiveLocks re-derives a share set's locked folders while a server runs.
type ftpLiveLocks struct {
	mu     sync.Mutex
	derive func() []string
	mark   ftpProfileMark
	at     time.Time
}

// ftpProfileMark identifies one build of the game-profile cache. A rebuild
// replaces the entries slice, so the address of its first element changes;
// the mark holds that element, so the old array cannot be freed and its
// address handed to the next build. An invalidated cache reads as not built.
type ftpProfileMark struct {
	built bool
	n     int
	first *gameProfileEntry
}

// ftpProfileCacheMark reads the profile cache's current mark.
func ftpProfileCacheMark() ftpProfileMark {
	gameProfileCache.mu.Lock()
	defer gameProfileCache.mu.Unlock()
	m := ftpProfileMark{built: gameProfileCache.built, n: len(gameProfileCache.entries)}
	if m.n > 0 {
		m.first = &gameProfileCache.entries[0]
	}
	return m
}

// lockedDirs is the set's locked folders now. A set without live locks (the
// Files card's per-request set, the tests') keeps what it was given; the
// running server's set is derived again when the profile cache was rebuilt
// or the last derivation is older than half of ftpLiveLockTTL. The slice is
// replaced, never changed, so a caller may read it after the lock is let go.
func (set *ftpShareSet) lockedDirs() []string {
	if set == nil {
		return nil
	}
	l := set.live
	if l == nil {
		return set.locked
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.at.IsZero() && time.Since(l.at) < ftpLiveLockTTL/2 && ftpProfileCacheMark() == l.mark {
		return set.locked
	}
	set.locked = set.keepInside(l.derive())
	// Taken after the derivation, which may itself have built the cache.
	l.mark = ftpProfileCacheMark()
	l.at = time.Now()
	return set.locked
}

// runDirs is ftpRunDirs for the cards this set shares from: the folder that
// holds each share named Roms. The running server derives its locks with it,
// and the Files card for each request; both get the shared cached answer
// (ftpRunDirsCached).
func (set *ftpShareSet) runDirs() []string {
	var cards []string
	for _, sh := range set.list {
		if strings.EqualFold(sh.name, "Roms") {
			cards = append(cards, filepath.Dir(sh.dir))
		}
	}
	return ftpRunDirsCached(cards)
}

// ftpRunDirsCache holds ftpRunDirs answers by card list, so the Phone
// Companion's Files card — which opens its shares for every list, download,
// upload, mkdir, rename and delete — does not read every Emus folder,
// config.json and launcher each time. An answer is used again until the
// game-profile cache changes or it is older than half of ftpLiveLockTTL, the
// rule the running FTP server's set keeps too: stacked, the two stay within
// ftpLiveLockTTL. A handful of card lists at most are in use;
// past ftpRunDirsCacheMax the cache starts over.
var ftpRunDirsCache struct {
	mu      sync.Mutex
	entries map[string]ftpRunDirsEntry
}

// ftpRunDirsEntry is one cached ftpRunDirs answer.
type ftpRunDirsEntry struct {
	dirs []string
	mark ftpProfileMark
	at   time.Time
}

// ftpRunDirsCacheMax bounds the card lists ftpRunDirsCache remembers.
const ftpRunDirsCacheMax = 8

// ftpRunDirsCached is ftpRunDirs through ftpRunDirsCache. The slice is shared:
// callers must not change it (keepInside builds a new one). It reads the card
// when the answer is stale: never on the render thread.
func ftpRunDirsCached(cards []string) []string {
	key := strings.Join(cards, "\x00")
	c := &ftpRunDirsCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok && time.Since(e.at) < ftpLiveLockTTL/2 && ftpProfileCacheMark() == e.mark {
		return e.dirs
	}
	dirs := ftpRunDirs(cards)
	if c.entries == nil || len(c.entries) >= ftpRunDirsCacheMax {
		c.entries = map[string]ftpRunDirsEntry{}
	}
	// The mark is taken after the derivation, which may itself have built the
	// profile cache.
	c.entries[key] = ftpRunDirsEntry{dirs: dirs, mark: ftpProfileCacheMark(), at: time.Now()}
	return dirs
}

// ftpRunDirs lists, folded (ftpFoldPath), every folder whose files are run as
// programs (see the file comment), for the given card folders and, on the
// handheld, the card the firmware runs from. Each folder counts as declared
// and, when it resolves, through its symlinks too, so a link elsewhere that
// leads into one still lands on a locked name. The caller keeps those inside
// its shares (keepInside). It reads the card: never on the render thread.
func ftpRunDirs(cards []string) []string {
	var raw []string
	raw = append(raw, ftpProfileDirList()...)
	raw = append(raw, windowsAppRoots()...)
	for _, card := range cards {
		raw = append(raw, portRoots(card)...)
		raw = append(raw, filepath.Join(card, "Roms", "PC"))
	}
	if !IsWindows() {
		raw = append(raw, portRoots(portMasterSDRoot)...)
	}
	raw = append(raw, ftpLauncherRunDirs(cards)...)
	seen := map[string]bool{}
	out := make([]string, 0, 2*len(raw))
	add := func(p string) {
		if k := ftpFoldPath(p); !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, dir := range raw {
		if strings.TrimSpace(dir) == "" || dir == "." {
			continue
		}
		add(dir)
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			add(real)
		}
	}
	return out
}

// ftpProfileDirList lists the game profiles' folders as they are spelled: a
// profile runs the program in its folder wherever on the card that is. The
// declared folder counts as well as the resolved one: a folder that is missing
// now would be run from once someone made it.
func ftpProfileDirList() []string {
	var out []string
	add := func(dir string) {
		if strings.TrimSpace(dir) != "" && dir != "." {
			out = append(out, dir)
		}
	}
	roots := gameProfileRoots()
	for _, e := range gameProfileEntries() {
		add(e.GameDir)
		if e.Executable != "" {
			add(filepath.Dir(e.Executable))
		}
		dir := filepath.FromSlash(strings.TrimSpace(e.Profile.Dir))
		switch {
		case dir == "":
		case filepath.IsAbs(dir):
			add(dir)
		default:
			for _, root := range roots {
				add(filepath.Join(root, dir))
			}
		}
	}
	return out
}

// ftpLauncherRunDirs lists the ROM folders of the systems that run their games
// (ftpSystemRunsGames): Roms/<system> on each card and, on the handheld,
// under deviceRomsRoot, and the folder config.json's "rompath" names. The
// firmware's own system names (ftpRunSystemNames) are listed whether or not
// an Emus folder exists for them.
func ftpLauncherRunDirs(cards []string) []string {
	var emuRoots, romsRoots []string
	for _, card := range cards {
		if strings.TrimSpace(card) == "" {
			continue
		}
		emuRoots = append(emuRoots, filepath.Join(card, "Emus"))
		romsRoots = append(romsRoots, filepath.Join(card, "Roms"))
	}
	if !IsWindows() {
		emuRoots = append(emuRoots, deviceEmuRoots...)
		romsRoots = append(romsRoots, deviceRomsRoot)
	}
	var out []string
	system := func(name string) {
		for _, roms := range romsRoots {
			out = append(out, filepath.Join(roms, name))
		}
	}
	for _, name := range ftpRunSystemNames {
		system(name)
	}
	for _, root := range emuRoots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		if len(entries) > ftpEmuFoldersMax {
			entries = entries[:ftpEmuFoldersMax]
		}
		for _, de := range entries {
			if skipScanEntryName(de.Name()) {
				continue
			}
			folder := filepath.Join(root, de.Name())
			if !dirExists(folder) {
				continue
			}
			fields := emuFolderConfig(folder)
			if !ftpSystemRunsGames(folder, fields) {
				continue
			}
			system(de.Name())
			var rompath string
			if json.Unmarshal(emuConfigField(fields, "rompath"), &rompath) == nil {
				if rompath = filepath.FromSlash(strings.TrimSpace(rompath)); rompath != "" {
					if !filepath.IsAbs(rompath) {
						rompath = filepath.Join(folder, rompath)
					}
					out = append(out, filepath.Clean(rompath))
				}
			}
		}
	}
	return out
}

// ftpSystemRunsGames reports whether an emulator folder's system runs the games
// it is given: its name is one of ftpRunSystemNames, its config.json lists a
// program extension, or one of its launchers runs its argument.
func ftpSystemRunsGames(folder string, fields map[string]json.RawMessage) bool {
	name := filepath.Base(folder)
	for _, n := range ftpRunSystemNames {
		if strings.EqualFold(name, n) {
			return true
		}
	}
	var extlist string
	if json.Unmarshal(emuConfigField(fields, "extlist"), &extlist) == nil {
		for _, ext := range strings.FieldsFunc(strings.ToLower(extlist), func(r rune) bool {
			return r == '|' || r == ',' || r == ';' || r == ' '
		}) {
			if ftpRunExtensions[strings.TrimPrefix(ext, ".")] {
				return true
			}
		}
	}
	launchers := []string{}
	for _, n := range emuConfigLaunchNames(fields) {
		if p, err := resolveWithinRoot(folder, n); err == nil {
			launchers = append(launchers, p)
		}
	}
	launchers = append(launchers, filepath.Join(folder, "launch.sh"), filepath.Join(folder, "launch"))
	for _, l := range launchers {
		if regularFile(l) && ftpLauncherRunsArg(l) {
			return true
		}
	}
	return false
}

// ftpLauncherRunsArg reads a launcher and reports whether it runs its
// argument (ftpScriptRunsArg). A program (ELF) or a file that cannot be read
// says nothing either way: it is not counted.
func ftpLauncherRunsArg(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, ftpLauncherReadMax))
	if err != nil || bytes.HasPrefix(data, []byte("\x7fELF")) {
		return false
	}
	return ftpScriptRunsArg(string(data))
}

// ftpScriptRunsArg reports whether a shell script runs the game it is given:
// a command whose program, after the wrappers and interpreters in front of it
// (exec, nohup, timeout 5, env X=1, sh -x, bash -c, source, ., python3…), is
// "$1", "$@" or "$*" in any quoting, or a variable the script set to one of
// them; or a chmod that marks it executable. It reads lines, not shell
// grammar: a launcher that hides its argument better (cd "$(dirname "$1")"
// and ./"$(basename "$1")") is not caught, which is why a system's name and
// its extensions count on their own (ftpSystemRunsGames).
//
// Inside a shell function "$1" and "$@" are the function's own arguments, not
// the launcher's: run() { "$@" 2>&1 | tee -a "$LOG"; } is a logging helper,
// and run ./ra64.trimui -L "$CORE" "$1" hands the game to RetroArch as data.
// So function bodies are read apart (ftpShellSplitFunctions): a call counts
// when the function runs its own argument and is handed the launcher's, and a
// function that runs a variable the script set from the launcher's argument
// (ROM="$1"; play() { exec "$ROM"; }) counts outright.
func ftpScriptRunsArg(script string) bool {
	return ftpShellRuns(script, map[string]bool{"1": true, "@": true, "*": true}, nil, 0)
}

// ftpShellRuns is ftpScriptRunsArg for a script (or a function body nested
// level functions deep) in which the parameters named in names hold the game:
// "1", "@" and "*", and variables set from them. runners are the functions
// known to run their own first argument. Past ftpShellFuncNestMax a body is
// read as plain commands, which can only lock more.
func ftpShellRuns(script string, names, runners map[string]bool, level int) bool {
	lines, bodies := ftpShellSplitFunctions(script)
	if level >= ftpShellFuncNestMax {
		for _, body := range bodies {
			lines = append(lines, strings.Split(body, "\n")...)
		}
		bodies = nil
	}
	args := make(map[string]bool, len(names)+4)
	for n := range names {
		args[n] = true
	}
	isArg := func(word string) bool {
		w := strings.Trim(word, `"'`)
		name, ok := strings.CutPrefix(w, "$")
		if !ok {
			return false
		}
		if strings.HasPrefix(name, "{") && strings.HasSuffix(name, "}") {
			name = name[1 : len(name)-1]
		}
		return args[name]
	}
	var commands [][]string
	for _, line := range lines {
		for _, cmd := range ftpShellCommands(line) {
			if f := strings.Fields(cmd); len(f) > 0 {
				commands = append(commands, f)
			}
		}
	}
	// Aliases: ROM="$1", then GAME="$ROM". Two passes follow one level of
	// copying either way round.
	for pass := 0; pass < 2; pass++ {
		for _, f := range commands {
			for _, w := range f {
				name, val, ok := strings.Cut(w, "=")
				if ok && ftpShellName(name) && isArg(val) {
					args[name] = true
				}
			}
		}
	}
	if len(bodies) > 0 {
		// Which functions run their own first argument; a function may call
		// one defined after it, so repeat until nothing changes.
		own := map[string]bool{"1": true, "@": true, "*": true}
		known := make(map[string]bool, len(runners)+len(bodies))
		for n := range runners {
			known[n] = true
		}
		for changed := true; changed; {
			changed = false
			for fn, body := range bodies {
				if !known[fn] && ftpShellRuns(body, own, known, level+1) {
					known[fn], changed = true, true
				}
			}
		}
		runners = known
		// A body that runs a variable holding the game runs it when called.
		vars := map[string]bool{}
		for n := range args {
			if !own[n] {
				vars[n] = true
			}
		}
		if len(vars) > 0 {
			for _, body := range bodies {
				if ftpShellRuns(body, vars, runners, level+1) {
					return true
				}
			}
		}
	}
	for _, f := range commands {
		if f[0] == "chmod" {
			for _, w := range f[1:] {
				if isArg(w) {
					return true
				}
			}
			continue
		}
		rest := ftpShellProgram(f)
		if len(rest) > 0 && isArg(rest[0]) {
			return true
		}
		// run "$1", where run() { "$@"; }: the function runs the game.
		if len(rest) > 1 && runners[rest[0]] && isArg(rest[1]) {
			return true
		}
	}
	return false
}

// ftpShellSplitFunctions separates a script's shell functions from the rest.
// lines is the script, comments dropped, with every function definition cut
// out; bodies holds each function's body ("{ … }") by name, a function defined
// twice under both. A header is "name()", "name ( )" or "function name" at the
// start of a line or right after an earlier definition on it, and the body is
// the brace group that follows on that line or a later one; its end is found
// by counting "{" and "}" words outside quotes and ${…} (ftpShellBraceScan). A
// subshell body ("name() ( … )") that closes on its line is a body too
// (ftpShellParenScan); one that does not is read as commands. A body still
// open at the end of the script is handed back as lines: unbalanced braces
// must not hide a command. The script is split at newlines with no limit on a
// line (the caller caps the whole read), so a long first line — a
// self-extracting script's — cannot stop the reading.
func ftpShellSplitFunctions(script string) (lines []string, bodies map[string]string) {
	bodies = map[string]string{}
	var (
		name  string // the function being read; "" outside one
		open  bool   // its "{" has been seen
		depth int
		body  strings.Builder
	)
	for _, raw := range strings.Split(script, "\n") {
		rest := ftpShellStripComment(strings.TrimSuffix(raw, "\r"))
		for {
			if name == "" {
				fn, after, ok := ftpShellFunctionHeader(rest)
				if !ok {
					lines = append(lines, rest)
					break
				}
				name, open, depth = fn, false, 0
				body.Reset()
				rest = after
			}
			if !open {
				t := strings.TrimLeft(rest, " \t")
				if t == "" {
					break // "{" comes on a later line
				}
				if t[0] != '{' {
					end := -1
					if t[0] == '(' {
						end = ftpShellParenScan(t)
					}
					if end < 0 {
						// Not a brace group after all, or a subshell body
						// that does not close on this line: read it as
						// commands, which can only lock more.
						lines = append(lines, rest)
						name = ""
						break
					}
					// A one-line subshell body ("name() ( … )") is the
					// function's, like a brace group; what follows it on
					// the line is read on.
					bodies[name] += t[:end] + "\n"
					name, rest = "", t[end:]
					continue
				}
				open, rest = true, t
			}
			end, d := ftpShellBraceScan(rest, depth)
			if end < 0 {
				body.WriteString(rest)
				body.WriteByte('\n')
				depth = d
				break
			}
			body.WriteString(rest[:end])
			bodies[name] += body.String() + "\n"
			name, rest = "", rest[end:]
		}
	}
	if name != "" && open {
		lines = append(lines, strings.Split(body.String(), "\n")...)
	}
	return lines, bodies
}

// ftpShellFuncNestMax is how many functions deep ftpScriptRunsArg reads a
// body as a function's own; past it the body counts as top-level commands.
const ftpShellFuncNestMax = 4

// ftpShellFunctionHeader reads a function definition's header at the start of
// s ("name()", "name ( )", "function name" with or without the "()"; leading
// blanks and the ";" after an earlier definition skipped) and returns the
// function's name and what follows the header.
func ftpShellFunctionHeader(s string) (name, after string, ok bool) {
	t := strings.TrimLeft(s, " \t;")
	keyword := false
	if r, cut := strings.CutPrefix(t, "function"); cut && r != "" && (r[0] == ' ' || r[0] == '\t') {
		keyword, t = true, strings.TrimLeft(r, " \t")
	}
	n := 0
	for n < len(t) {
		c := t[n]
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(n > 0 && ((c >= '0' && c <= '9') || c == '-' || c == '.' || c == ':')) {
			n++
			continue
		}
		break
	}
	if n == 0 {
		return "", "", false
	}
	name, t = t[:n], strings.TrimLeft(t[n:], " \t")
	if r, cut := strings.CutPrefix(t, "("); cut {
		if r, cut = strings.CutPrefix(strings.TrimLeft(r, " \t"), ")"); cut {
			return name, r, true
		}
		return "", "", false
	}
	if keyword && (t == "" || t[0] == '{') {
		return name, t, true
	}
	return "", "", false
}

// ftpShellBraceScan counts the "{" and "}" words of s on top of depth, leaving
// out those in quotes and in ${…}, and returns the index just past the "}"
// that brings the count to zero, or -1 and the count at the end of s.
func ftpShellBraceScan(s string, depth int) (int, int) {
	sep := func(i int) bool {
		return i < 0 || i >= len(s) || strings.IndexByte(" \t;&|()", s[i]) >= 0
	}
	param := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			i++
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return -1, depth
			}
			i += j + 1
		case c == '"':
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' {
					i++
				}
				i++
			}
		case c == '$' && i+1 < len(s) && s[i+1] == '{':
			param++
			i++
		case c == '}' && param > 0:
			param--
		case c == '{' && sep(i-1) && sep(i+1):
			depth++
		case c == '}' && sep(i-1) && sep(i+1):
			depth--
			if depth <= 0 {
				return i + 1, 0
			}
		}
	}
	return -1, depth
}

// ftpShellParenScan returns the index just past the ")" that closes the "("
// s starts with, counting parentheses outside quotes and ${…} as
// ftpShellBraceScan counts braces, or -1 when s ends first. A case pattern's
// lone ")" ends the body early; the rest of the line is then read as
// commands, which can only lock more.
func ftpShellParenScan(s string) int {
	depth, param := 0, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			i++
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return -1
			}
			i += j + 1
		case c == '"':
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' {
					i++
				}
				i++
			}
		case c == '$' && i+1 < len(s) && s[i+1] == '{':
			param++
			i++
		case c == '}' && param > 0:
			param--
		case c == '(' && param == 0:
			depth++
		case c == ')' && param == 0:
			depth--
			if depth <= 0 {
				return i + 1
			}
		}
	}
	return -1
}

// ftpShellProgram drops what stands in front of a command's program: shell
// keywords, variable assignments, wrappers (with their options and, for
// timeout, taskset and chrt, their value) and interpreters (with their
// options). What is left starts with the program, or with the script an
// interpreter was given.
func ftpShellProgram(f []string) []string {
	dropOptions := func(f []string) []string {
		for len(f) > 0 && strings.HasPrefix(f[0], "-") {
			f = f[1:]
		}
		return f
	}
	for len(f) > 0 {
		w := f[0]
		if name, _, ok := strings.Cut(w, "="); ok && ftpShellName(name) {
			f = f[1:]
			continue
		}
		switch filepath.Base(w) {
		case "then", "do", "else", "elif", "if", "while", "until", "!", "{", "time":
			f = f[1:]
		case "exec", "nohup", "setsid", "command", "builtin", "eval", "sudo", "env":
			f = dropOptions(f[1:])
		case "timeout", "taskset", "chrt":
			f = dropOptions(f[1:])
			if len(f) > 0 {
				f = f[1:]
			}
		case "nice", "ionice", "stdbuf":
			f = f[1:]
			for len(f) > 0 && (strings.HasPrefix(f[0], "-") || strings.Trim(f[0], "0123456789") == "") {
				f = f[1:]
			}
		case "sh", "bash", "ash", "dash", "ksh", "mksh", "zsh", "busybox", "source", ".",
			"python", "python2", "python3", "perl", "lua", "luajit", "love", "node", "ruby", "php":
			f = dropOptions(f[1:])
		default:
			return f
		}
	}
	return f
}

// ftpShellCommands splits a line into the commands it runs: at ; & | and
// around the parentheses of a subshell or a command substitution.
func ftpShellCommands(line string) []string {
	r := strings.NewReplacer("$(", ";", "(", ";", ")", ";", "`", ";", "&&", ";", "||", ";", "|", ";", "&", ";")
	return strings.Split(r.Replace(line), ";")
}

// ftpShellStripComment drops a "#" comment: one at the start of the line or
// after a blank. "${#x}" and "a#b" are not comments.
func ftpShellStripComment(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return line[:i]
		}
	}
	return line
}

// ftpShellName reports whether s is a shell variable name.
func ftpShellName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (i > 0 && c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}

// ftpRealPath resolves abs through every symlink it crosses, the last name
// included (a STOR through a link writes where the link points). A name that
// does not exist yet resolves its deepest existing parent and keeps the rest
// as given; a dangling link is followed to where it points, since creating a
// file through it creates the target. ok is false when the path cannot be
// worked out (a loop, no permission, too many steps): the caller refuses.
func ftpRealPath(abs string) (string, bool) {
	cur := filepath.Clean(abs)
	var tail []string
	for step := 0; step < ftpRealPathSteps; step++ {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				real = filepath.Join(real, tail[i])
			}
			return real, true
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", false
		}
		if info, lerr := os.Lstat(cur); lerr == nil && info.Mode()&fs.ModeSymlink != 0 {
			target, rerr := os.Readlink(cur)
			if rerr != nil {
				return "", false
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(cur), target)
			}
			cur = filepath.Clean(target)
			continue
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
	return "", false
}

// lockedThroughLinks reports whether a write to rel inside share must be
// refused once symlinks are followed: its real target is locked (a profile's
// or a run folder, or Ports — ftpProtected — in whichever share holds it), is
// a share itself, lies outside every share, or cannot be worked out. A share
// that is not on disk (the resolve tests' made-up folders) has nothing to
// follow.
func (set *ftpShareSet) lockedThroughLinks(share *ftpShare, rel string) bool {
	if share == nil || share.real == "" {
		return false
	}
	real, ok := ftpRealPath(filepath.Join(share.dir, rel))
	if !ok {
		return true
	}
	key := ftpFoldPath(real)
	sep := string(filepath.Separator)
	for _, sh := range set.list {
		if sh.real == "" {
			continue
		}
		base := ftpFoldPath(sh.real)
		if key == base {
			return true
		}
		rest, inside := strings.CutPrefix(key, base+sep)
		if !inside {
			continue
		}
		return ftpProtected(sh.name, filepath.ToSlash(rest)) || set.lockedPath(filepath.Join(sh.dir, rest))
	}
	return true
}
