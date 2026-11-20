package main

// termpty.go — interactive sessions in the Terminal: a shell, htop, vi, nano,
// less, a REPL or an SSH connection running on a real terminal (a PTY on the
// handheld, a ConPTY on Windows — termpty_linux.go / termpty_windows.go)
// instead of the line terminal's one-command-per-line pipes, which those
// programs refuse. Their output goes through the VT emulator (termvt.go); the
// screen and the pad/keyboard mapping live in termpty_ui.go, the built-in SSH
// client in termpty_ssh.go.
//
// One session at a time. Leaving it (hold B, Ctrl+Shift+Q, Start → Leave)
// keeps it running and the line terminal offers to resume it; it ends when
// its program exits, from Start → End session, or with JukaHub (the teardown
// hook below, and on Linux the kernel's hang-up when the PTY closes).
//
// The program is not a screen-taking child: JukaHub keeps drawing and owns the
// display the whole time, so screenChildStart is not involved.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// termPtyProc is a program on a terminal: a PTY child, a ConPTY child or the
// built-in SSH client. Read returns the program's output (an error once it
// is gone for good), Kill ends it and everything it started — asking first
// and forcing after grace where the system can ask (idempotent, any
// goroutine, a later call with a shorter grace still forces sooner) — and
// Wait blocks until it ended. EchoOff reports a program reading what is
// typed without showing it (a password prompt), so the on-screen keyboard
// hides it too.
type termPtyProc interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
	Kill(grace time.Duration)
	Wait() (int, error)
	EchoOff() bool
}

// termPtyCommand is what to start: argv, and on Windows the exact command
// line when cmd.exe has to read one (cmd /s /c "…").
type termPtyCommand struct {
	argv       []string
	winCmdLine string
}

const (
	// termPtyInQueue bounds the writes waiting for a program that does not
	// read (each is one key or one paste).
	termPtyInQueue = 512
	// termPtyReadBuf is one read of the program's output.
	termPtyReadBuf = 32 << 10
	// termPtyDrainAfterExit is how long output may still arrive after the
	// program exited, before its leftovers are stopped and the PTY closed.
	termPtyDrainAfterExit = 400 * time.Millisecond
	// termPtyKillGrace is how long a hung-up session may take to exit before
	// it is killed; termPtyQuitGrace the same while JukaHub quits, short
	// enough that the kill happens inside the teardown's budget.
	termPtyKillGrace = 1500 * time.Millisecond
	termPtyQuitGrace = 300 * time.Millisecond
)

// termPtySession is the one running session.
type termPtySession struct {
	vt      *vtTerm
	proc    termPtyProc
	label   string
	started time.Time

	in   chan []byte
	quit chan struct{}
	stop sync.Once

	readDone atomic.Bool
	waitDone atomic.Bool
	lastOut  atomic.Int64 // UnixNano of the last output

	mu       sync.Mutex
	exitCode int
	exitErr  error
	killed   bool

	// cols, rows are the size last given to the program (main thread).
	cols, rows int
}

// termPty is the session and the screen's own state (main thread).
var termPty struct {
	sess *termPtySession
	// view is true while the Terminal shows the session (false: the line
	// terminal, with "Resume shell").
	view bool
}

var termPtyShutdownOnce sync.Once

// termPtyLive is the running session for JukaHub's teardown, which runs off
// the main thread and must not read termPty (main thread only).
var termPtyLive atomic.Pointer[termPtySession]

// termPtyAlive reports a session that has not ended.
func termPtyAlive() bool {
	s := termPty.sess
	return s != nil && !s.finished()
}

// termPtyShowing reports whether the Terminal is showing the session now.
func termPtyShowing() bool {
	return termPty.view && termPty.sess != nil && isTerminalScene()
}

func (s *termPtySession) finished() bool {
	return s.readDone.Load() && s.waitDone.Load()
}

// queueReply sends the emulator's answers (cursor position, attributes) to
// the program. Called with the emulator's lock held: it never blocks.
func (s *termPtySession) queueReply(b []byte) {
	cp := append([]byte(nil), b...)
	select {
	case s.in <- cp:
	default:
	}
}

// send queues bytes for the program; false when it has not read the ones
// before (the queue is full) or has ended.
func (s *termPtySession) send(b []byte) bool {
	if len(b) == 0 || s.finished() {
		return false
	}
	select {
	case s.in <- append([]byte(nil), b...):
		return true
	default:
		return false
	}
}

func (s *termPtySession) readLoop() {
	defer func() {
		if r := recover(); r != nil {
			logError("terminal", "session reader panicked: %v", r)
		}
		s.readDone.Store(true)
		s.stopIO()
	}()
	buf := make([]byte, termPtyReadBuf)
	for {
		n, err := s.proc.Read(buf)
		if n > 0 {
			s.vt.Feed(buf[:n])
			s.lastOut.Store(time.Now().UnixNano())
		}
		if err != nil {
			return
		}
	}
}

func (s *termPtySession) writeLoop() {
	for {
		select {
		case b := <-s.in:
			if _, err := s.proc.Write(b); err != nil {
				// The program is gone; what is queued goes nowhere.
				<-s.quit
				return
			}
		case <-s.quit:
			return
		}
	}
}

func (s *termPtySession) waitLoop() {
	code, err := s.proc.Wait()
	s.mu.Lock()
	s.exitCode, s.exitErr = code, err
	s.mu.Unlock()
	s.waitDone.Store(true)
	// What the program printed last may still be in the PTY; then whatever it
	// left running is stopped and the PTY closed, which ends the reader.
	time.Sleep(termPtyDrainAfterExit)
	s.proc.Kill(termPtyKillGrace)
}

func (s *termPtySession) stopIO() {
	s.stop.Do(func() { close(s.quit) })
}

// end stops the program (Start → End session, JukaHub quitting).
func (s *termPtySession) end() {
	s.mu.Lock()
	s.killed = true
	s.mu.Unlock()
	go s.proc.Kill(termPtyKillGrace)
}

// resize gives the program a new size when the grid changed (main thread).
func (s *termPtySession) resize(cols, rows int) {
	cols, rows = vtClampSize(cols, rows)
	if cols == s.cols && rows == s.rows {
		return
	}
	s.cols, s.rows = cols, rows
	s.vt.Resize(cols, rows)
	if err := s.proc.Resize(cols, rows); err != nil {
		logDebug("terminal", "resize to %dx%d: %v", cols, rows, err)
	}
}

// termPtyStartSession starts a session around the program start gives it.
func termPtyStartSession(label string, cols, rows int, start func(cols, rows int, s *termPtySession) (termPtyProc, error)) error {
	// A session that ended unseen hands its screen over first.
	if old := termPty.sess; old != nil && old.finished() {
		termPtyFinish()
	}
	cols, rows = vtClampSize(cols, rows)
	s := &termPtySession{
		label:   label,
		started: time.Now(),
		in:      make(chan []byte, termPtyInQueue),
		quit:    make(chan struct{}),
		cols:    cols,
		rows:    rows,
	}
	s.vt = newVTTerm(cols, rows, s.queueReply)
	proc, err := start(cols, rows, s)
	if err != nil {
		return err
	}
	s.proc = proc
	termPtyShutdownOnce.Do(func() {
		// JukaHub's teardown ends a session still running (shutdown.go).
		bootOnShutdown(termPtyShutdown)
	})
	go s.readLoop()
	go s.writeLoop()
	go s.waitLoop()
	termPty.sess = s
	termPtyLive.Store(s)
	termPty.view = true
	termPtyEntered()
	return nil
}

// termPtyShutdown ends a running session when JukaHub quits (teardown hook,
// off the render thread): a short grace, then the kill, inside the
// teardown's budget.
func termPtyShutdown() {
	if s := termPtyLive.Load(); s != nil && !s.finished() {
		s.proc.Kill(termPtyQuitGrace)
	}
}

// ── What runs in a session ───────────────────────────────────────────────

// termPtyFullScreen are programs that draw on the whole terminal.
var termPtyFullScreen = map[string]bool{
	"htop": true, "top": true, "btop": true, "atop": true, "vi": true, "vim": true, "nvim": true,
	"view": true, "nano": true, "pico": true, "micro": true, "emacs": true, "joe": true, "mg": true,
	"less": true, "more": true, "most": true, "man": true, "mc": true, "tmux": true, "screen": true,
	"watch": true, "nload": true, "iftop": true, "nmtui": true, "alsamixer": true, "ncdu": true,
	"ranger": true, "nnn": true, "w3m": true, "lynx": true, "links": true, "elinks": true,
	"mutt": true, "tig": true, "telnet": true, "cfdisk": true, "dialog": true, "whiptail": true,
	"edit": true,
}

// termPtyREPLs are interactive when started with no arguments.
var termPtyREPLs = map[string]bool{
	"python": true, "python3": true, "lua": true, "luajit": true, "node": true, "irb": true,
	"bc": true, "sqlite3": true, "guile": true, "tclsh": true, "ghci": true,
}

// termPtyShells open an interactive shell when typed alone.
var termPtyShells = map[string]bool{
	"shell": true, "sh": true, "bash": true, "ash": true, "zsh": true, "fish": true, "dash": true,
	"cmd": true, "powershell": true, "pwsh": true, "wsl": true,
}

// termPtyPlainShort are each program's own short (or single-dash long) flags
// that print and exit. They differ per program: nano -v and mc -v view a
// file, vim -V is verbose, tmux -v logs — those stay interactive.
var termPtyPlainShort = map[string][]string{
	"htop": {"-h", "-V"}, "top": {"-h", "-v"}, "btop": {"-h", "-v"}, "atop": {"-V"},
	"vi": {"-h"}, "vim": {"-h"}, "view": {"-h"}, "nvim": {"-h", "-v"},
	"nano": {"-h", "-V"}, "pico": {"-h", "-V"}, "micro": {"-help", "-version"},
	"less": {"-V", "-?"}, "more": {"-h", "-V"}, "man": {"-h", "-V"}, "mc": {"-h", "-V"},
	"tmux": {"-V"}, "screen": {"-v"}, "watch": {"-h", "-v"}, "iftop": {"-h"}, "nmtui": {"-h"},
	"alsamixer": {"-h"}, "ncdu": {"-h", "-v", "-V"}, "ranger": {"-h"}, "nnn": {"-h", "-V"},
	"w3m": {"-h", "-help", "-version"}, "lynx": {"-help", "-version"}, "links": {"-help", "-version"},
	"elinks": {"-h"}, "mutt": {"-h", "-v"}, "tig": {"-h", "-v"}, "cfdisk": {"-h", "-V"},
}

// termPtyPlainFlags make a full-screen program print and exit (top -b,
// vim --version, htop -V), so the line terminal keeps it.
func termPtyPlainFlags(command string, args []string) bool {
	for _, a := range args {
		if a == "--version" || a == "--help" {
			return true
		}
		for _, f := range termPtyPlainShort[command] {
			if a == f {
				return true
			}
		}
		if command == "top" && strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "b") {
			return true
		}
	}
	return false
}

// termPtyBuiltins are the line terminal's own commands (termExecute): a
// pipeline starting with one (help | less, history | less) is not a shell
// line, so it stays with them.
var termPtyBuiltins = map[string]bool{
	"help": true, "?": true, "history": true, "sysinfo": true, "neofetch": true, "calc": true,
	"clear": true, "cls": true, "cd": true, "pwd": true, "exit": true, "quit": true,
	"apt": true, "apt-get": true, "jpkg": true, "java": true, "javac": true, "jshell": true,
	"jar": true, "javaw": true,
}

// termPtyCommandName is a typed word as a program name: lower case, without
// a path or .exe.
func termPtyCommandName(word string) string {
	word = strings.ToLower(filepath.Base(strings.Trim(word, `"'`)))
	return strings.TrimSuffix(word, ".exe")
}

// termPtyWants decides whether a typed line runs in a session: a shell typed
// alone, a full-screen program, a REPL with no arguments, or a pipeline
// ending in a pager (dmesg | less). A line that redirects or backgrounds
// stays in the line terminal.
func termPtyWants(line string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	first := termPtyCommandName(fields[0])
	args := fields[1:]
	if first == "ssh" {
		return false // termPtySSHCommand decides
	}
	if termPtyBuiltins[first] {
		return false // JukaHub's own command, pipe or not
	}
	if termPtyShells[first] && len(args) == 0 {
		return true
	}
	if strings.ContainsAny(line, "><") || strings.HasSuffix(strings.TrimSpace(line), "&") {
		return false
	}
	if strings.Contains(line, "|") {
		segs := strings.Split(line, "|")
		last := strings.Fields(segs[len(segs)-1])
		if len(last) == 0 {
			return false
		}
		name := termPtyCommandName(last[0])
		return (name == "less" || name == "more" || name == "most" || name == "vi" || name == "vim" || name == "view") &&
			!termPtyPlainFlags(name, last[1:])
	}
	if termPtyFullScreen[first] {
		return !termPtyPlainFlags(first, args)
	}
	return termPtyREPLs[first] && len(args) == 0
}

// termPtyRunLine starts a session for a line termPtyWants took.
func termPtyRunLine(line string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return
	}
	first := termPtyCommandName(fields[0])
	if termPtyShells[first] && len(fields) == 1 {
		cmd, label, err := termPtyShellCommand(first)
		if err != nil {
			termAppend(err.Error(), termKindError)
			return
		}
		termPtyLaunch(cmd, label)
		return
	}
	// The program the line runs, checked first so a missing one says how to
	// get it instead of failing inside a blank session.
	prog := first
	if strings.Contains(line, "|") {
		segs := strings.Split(line, "|")
		if last := strings.Fields(segs[len(segs)-1]); len(last) > 0 {
			prog = termPtyCommandName(last[0])
		}
	}
	if !termPtyHasProgram(prog) {
		if linuxPkgSupported() {
			termAppend(prog+" is not installed. apt install "+prog+" adds it.", termKindError)
		} else {
			termAppend("Command not found: "+prog, termKindError)
		}
		return
	}
	termPtyLaunch(termPtyLineCommand(line), first)
}

// termPtyHasProgram reports a program on PATH, Linux packages' included.
func termPtyHasProgram(name string) bool {
	if name == "" {
		return false
	}
	if linuxPkgWrapperPath(name) != "" {
		return true
	}
	_, err := exec.LookPath(name)
	return err == nil
}

// termPtyLaunch starts cmd in a session sized to the Terminal's screen.
func termPtyLaunch(cmd termPtyCommand, label string) {
	if termPtyResumeIfAlive() {
		return
	}
	cols, rows := termPtyGridSize(appConfig)
	err := termPtyStartSession(label, cols, rows, func(cols, rows int, s *termPtySession) (termPtyProc, error) {
		return termPtySpawn(cmd, termPtyEnv(), term.cwd, cols, rows)
	})
	if err != nil {
		termAppend("Could not start "+label+": "+firstLine(err.Error()), termKindError)
		if errors.Is(err, errTermPtyUnsupported) {
			termAppend("Interactive programs need the handheld, or Windows 10 (1809) or newer.", termKindSystem)
		}
		return
	}
	logInfo("terminal", "interactive session: %s (%dx%d)", label, cols, rows)
}

// termPtyResumeIfAlive shows the running session instead of starting a
// second one, and says so.
func termPtyResumeIfAlive() bool {
	if !termPtyAlive() {
		return false
	}
	how := hintSentence("End it from its menu (%s) to start another.", "Start", "Menu")
	if hintsUseKeyboard() {
		how = "End it from its menu (Ctrl+Shift+M) to start another."
	}
	termAppend(termPty.sess.label+" is still running — back to it. "+how, termKindSystem)
	termPtyResume()
	return true
}

// termPtyResume shows the running session again.
func termPtyResume() {
	if !termPtyAlive() {
		return
	}
	termPty.view = true
	termPtyEntered()
}

// termPtyLeave goes back to the line terminal; the session keeps running.
func termPtyLeave() {
	termPtyCloseOverlays()
	termPty.view = false
	termFocusPrompt()
}

// termPtyShellCommand is an interactive shell. `shell` is the best there is
// — bash (Linux packages' too), else sh; cmd.exe on Windows. Any other name
// (bash, zsh, powershell, cmd …) is that shell or "not found", never a
// different one in its place.
func termPtyShellCommand(name string) (termPtyCommand, string, error) {
	cmdExe := func() termPtyCommand {
		comspec := os.Getenv("ComSpec")
		if comspec == "" {
			comspec = "cmd.exe"
		}
		// UTF-8 output from programs that use the console code page.
		return termPtyCommand{argv: []string{comspec}, winCmdLine: `"` + comspec + `" /k chcp 65001>nul`}
	}
	find := func(n string) string {
		if p := linuxPkgWrapperPath(n); p != "" {
			return p
		}
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
		return ""
	}
	onWindows := runtime.GOOS == "windows"
	switch {
	case name == "shell" && onWindows, name == "cmd" && onWindows:
		return cmdExe(), "cmd.exe", nil
	case name == "shell":
		for _, n := range []string{"bash", "sh"} {
			if p := find(n); p != "" {
				return termPtyCommand{argv: []string{p}}, n, nil
			}
		}
		if regularFile("/bin/sh") {
			return termPtyCommand{argv: []string{"/bin/sh"}}, "sh", nil
		}
		return termPtyCommand{}, "", errors.New("No shell found (bash, sh)")
	}
	p := find(name)
	if p == "" && name == "sh" && !onWindows && regularFile("/bin/sh") {
		p = "/bin/sh"
	}
	if p == "" {
		switch name {
		case "bash", "zsh", "fish", "dash":
			if !onWindows && linuxPkgSupported() {
				return termPtyCommand{}, "", fmt.Errorf("%s is not installed. apt install %s adds it.", name, name)
			}
		}
		return termPtyCommand{}, "", fmt.Errorf("Command not found: %s", name)
	}
	args := []string{p}
	if name == "powershell" || name == "pwsh" {
		args = append(args, "-NoLogo")
	}
	return termPtyCommand{argv: args}, name, nil
}

// termPtyLineCommand runs a typed line through the system shell, so quotes,
// pipes and $VARIABLES mean what they do at any prompt. Trust boundary
// (SEC-12): the line is what the user typed at the Terminal prompt.
func termPtyLineCommand(line string) termPtyCommand {
	if runtime.GOOS == "windows" {
		comspec := os.Getenv("ComSpec")
		if comspec == "" {
			comspec = "cmd.exe"
		}
		return termPtyCommand{argv: []string{comspec}, winCmdLine: `"` + comspec + `" /d /s /c "` + line + `"`}
	}
	sh := "/bin/sh"
	if p, err := exec.LookPath("sh"); err == nil {
		sh = p
	}
	return termPtyCommand{argv: []string{sh, "-c", line}}
}

// ── Environment ──────────────────────────────────────────────────────────

// termPtyEnv is the session's environment: the line terminal's (JukaHub's,
// with Linux packages and Java first on PATH) without JukaHub's launcher
// markers, plus a TERM this device has a description for, a UTF-8 locale
// and a prompt that shows the folder. No COLUMNS / LINES: ncurses and less
// would trust them over the terminal's real size, which changes with the
// text size; programs ask the PTY (TIOCGWINSZ) instead.
func termPtyEnv() []string {
	drop := map[string]bool{"TERM": true, "COLUMNS": true, "LINES": true, "TERM_PROGRAM": true}
	for _, k := range childEnvMarkers {
		drop[k] = true
	}
	var env []string
	have := map[string]bool{}
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" || drop[strings.ToUpper(k)] {
			continue
		}
		have[strings.ToUpper(k)] = true
		env = append(env, kv)
	}
	env = javaShellEnv(linuxPkgShellEnv(env))
	if runtime.GOOS != "windows" {
		env = append(env, "TERM="+termPtyTermName())
		if !have["LANG"] && !have["LC_ALL"] && !have["LC_CTYPE"] {
			env = append(env, "LANG=C.UTF-8")
		}
		if !have["HOME"] {
			if home := linuxPkgPasswdHome(); home != "" {
				env = append(env, "HOME="+home)
			}
		}
		if !have["PS1"] {
			env = append(env, `PS1=\u@\h:\w\$ `)
		}
	}
	env = append(env, "TERM_PROGRAM=JukaHub")
	return env
}

// termPtyTermName is the richest TERM this device can describe:
// xterm-256color, xterm, else vt100 (BusyBox's tools need no description).
func termPtyTermName() string {
	dirs := []string{"/usr/share/terminfo", "/etc/terminfo", "/lib/terminfo", "/usr/lib/terminfo"}
	if v := os.Getenv("TERMINFO"); v != "" {
		dirs = append(dirs, v)
	}
	if linuxPkgSupported() {
		if lay, err := linuxPkgDirs(); err == nil {
			for _, rel := range []string{"usr/share/terminfo", "usr/lib/terminfo", "etc/terminfo", "lib/terminfo"} {
				dirs = append(dirs, filepath.Join(lay.root, filepath.FromSlash(rel)))
			}
		}
	}
	for _, name := range []string{"xterm-256color", "xterm"} {
		for _, d := range dirs {
			for _, sub := range []string{name[:1], fmt.Sprintf("%x", name[0])} {
				if regularFile(filepath.Join(d, sub, name)) {
					return name
				}
			}
		}
	}
	return "vt100"
}

// errTermPtyUnsupported is a system with no terminal to give a program.
var errTermPtyUnsupported = errors.New("this system has no pseudo-terminal support")

// ── Keys ─────────────────────────────────────────────────────────────────

// termPtyKey is a key that is not a character.
type termPtyKey int

const (
	tpkUp termPtyKey = iota
	tpkDown
	tpkRight
	tpkLeft
	tpkHome
	tpkEnd
	tpkPgUp
	tpkPgDn
	tpkInsert
	tpkDelete
	tpkEnter
	tpkTab
	tpkBackTab
	tpkBackspace
	tpkEsc
	tpkF1 // F1..F12 follow
)

// termPtyMods is xterm's modifier parameter: 1 + Shift + 2·Alt + 4·Ctrl.
func termPtyMods(shift, alt, ctrl bool) int {
	m := 1
	if shift {
		m++
	}
	if alt {
		m += 2
	}
	if ctrl {
		m += 4
	}
	return m
}

// termPtyKeyBytes is what xterm sends for a key: arrows and Home/End in
// cursor-key mode as the program asked (DECCKM), modifiers as ;m.
func termPtyKeyBytes(k termPtyKey, mods int, appCursor bool) []byte {
	alt := mods > 1 && (mods-1)&2 != 0
	ctrl := mods > 1 && (mods-1)&4 != 0
	letter := func(c byte) []byte {
		if mods > 1 {
			return []byte("\x1b[1;" + strconv.Itoa(mods) + string(c))
		}
		if appCursor {
			return []byte{0x1b, 'O', c}
		}
		return []byte{0x1b, '[', c}
	}
	tilde := func(n int) []byte {
		if mods > 1 {
			return []byte("\x1b[" + strconv.Itoa(n) + ";" + strconv.Itoa(mods) + "~")
		}
		return []byte("\x1b[" + strconv.Itoa(n) + "~")
	}
	prefix := func(b ...byte) []byte {
		if alt {
			return append([]byte{0x1b}, b...)
		}
		return b
	}
	switch k {
	case tpkUp:
		return letter('A')
	case tpkDown:
		return letter('B')
	case tpkRight:
		return letter('C')
	case tpkLeft:
		return letter('D')
	case tpkHome:
		return letter('H')
	case tpkEnd:
		return letter('F')
	case tpkInsert:
		return tilde(2)
	case tpkDelete:
		return tilde(3)
	case tpkPgUp:
		return tilde(5)
	case tpkPgDn:
		return tilde(6)
	case tpkEnter:
		return prefix('\r')
	case tpkTab:
		return prefix('\t')
	case tpkBackTab:
		return []byte("\x1b[Z")
	case tpkBackspace:
		if ctrl {
			return prefix(0x08)
		}
		return prefix(0x7f)
	case tpkEsc:
		return []byte{0x1b}
	}
	if k >= tpkF1 && k < tpkF1+12 {
		n := int(k - tpkF1)
		if n < 4 {
			if mods > 1 {
				return []byte("\x1b[1;" + strconv.Itoa(mods) + string(rune('P'+n)))
			}
			return []byte{0x1b, 'O', byte('P' + n)}
		}
		return tilde([]int{15, 17, 18, 19, 20, 21, 23, 24}[n-4])
	}
	return nil
}

// termPtyCtrlByte is Ctrl plus a character: letters and the punctuation
// xterm maps (Ctrl+[ is Esc, Ctrl+Space NUL, Ctrl+/ the undo of nano and
// emacs).
func termPtyCtrlByte(r rune) (byte, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return byte(r - 'a' + 1), true
	case r >= 'A' && r <= 'Z':
		return byte(r - 'A' + 1), true
	}
	switch r {
	case '@', ' ', '2', '`':
		return 0, true
	case '[', '3':
		return 0x1b, true
	case '\\', '4':
		return 0x1c, true
	case ']', '5':
		return 0x1d, true
	case '^', '6', '~':
		return 0x1e, true
	case '_', '-', '7', '/':
		return 0x1f, true
	case '8', '?':
		return 0x7f, true
	}
	return 0, false
}

// termPtyPasteBytes is pasted text as the program should get it: line breaks
// as Enter, and inside bracketed-paste markers when the program asked for
// them (so an editor does not auto-indent every pasted line).
func termPtyPasteBytes(text string, bracketed bool) []byte {
	text = strings.ToValidUTF8(text, "")
	text = strings.ReplaceAll(text, "\r\n", "\r")
	text = strings.ReplaceAll(text, "\n", "\r")
	// The end marker inside the text would end the paste early.
	text = strings.ReplaceAll(text, "\x1b[201~", "")
	if bracketed {
		return []byte("\x1b[200~" + text + "\x1b[201~")
	}
	return []byte(text)
}

// termPtySortedEnv sorts an environment the way Windows wants its block.
func termPtySortedEnv(env []string) []string {
	out := append([]string(nil), env...)
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToUpper(out[i]) < strings.ToUpper(out[j])
	})
	return out
}
