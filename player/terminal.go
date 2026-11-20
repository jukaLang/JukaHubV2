package main

// terminal.go — the built-in terminal: its session, the commands it runs and
// the way a pad, a keyboard and a mouse drive it. Drawing lives in
// terminal_ui.go, history and completion in terminal_history.go, and the
// platform shell in terminal_windows.go / terminal_other.go.
//
// The controls are the ones every other screen has. B (pad) and Escape
// (keyboard) leave whenever the on-screen keyboard is closed — B closes the
// keyboard first when it is open — and no arrow can strand the user: on the pad
// Up/Down scroll the output and Up at the oldest line steps onto the top bar,
// exactly like any other page. Left/Right move the caret, A opens the keyboard,
// Start runs the line, X clears it (or stops a running command), Y opens the
// command history, Select opens the Command Deck and L1/R1 page the output. A
// keyboard types straight into the prompt: Enter runs, Up/Down recall
// history, PgUp/PgDn page, Tab completes, Ctrl+R opens the history, Ctrl+P
// the Command Deck, Ctrl+U clears the line, Ctrl+C stops a command and Ctrl+L
// clears the screen. The mouse wheel scrolls and a click puts the caret where
// it lands.
//
// Interactive programs — `shell`, htop, vi, nano, less, a REPL, ssh to a host —
// run on a real terminal instead (termpty.go, termpty_ui.go, termvt.go), with
// controls of their own; leaving one keeps it running and Start resumes it.
//
// Typing on a handheld is slow, so three things spare it: the Command Deck
// (cmddeck.go — ready-made commands by category with pickers for their
// blanks, Favorites and macros, Ask Juka), token chips above the on-screen
// keyboard (cmddeck_chips.go — Select takes the first) and the history picker,
// where Y pins a command to the deck's Favorites.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// termMaxLines bounds the scrollback. Lines keep their wrapped rows
	// cached, so a few hundred is a few hundred kilobytes at most.
	termMaxLines = 400
	termPrompt   = "juka> "
	// termCaretBlink is the caret's half period: 530 ms on, 530 ms off, the
	// rhythm desktop text fields use. Every edit restarts it, so the caret
	// stays solid while someone is typing.
	termCaretBlink = 530 * time.Millisecond
	// termMaxInput bounds the command line (a pasted log must not become a
	// 2 MB prompt that is measured on every frame).
	termMaxInput = 4096
	// termScrollStep is how many lines one D-pad press or wheel notch moves.
	termScrollStep = 3
	// termListMax caps the built-in ls: a folder of 20k ROMs must not flood
	// the scrollback.
	termListMax = 500
)

const (
	// termCommandTimeout bounds an external command. It runs off the event
	// thread, can be stopped at any time with X / Ctrl+C / the Stop button,
	// and ends when the user leaves the screen; the timeout only catches the
	// command nobody is watching any more.
	termCommandTimeout = 5 * time.Minute
	// termJobMaxPending bounds the output a command may queue between frames.
	termJobMaxPending = 2000
	// termCatMaxBytes bounds `cat`: os.ReadFile on /dev/zero or a disc image
	// read until the handheld ran out of memory.
	termCatMaxBytes = 256 << 10
)

// termLineKind is what a scrollback line is, which is what its colour says.
type termLineKind uint8

const (
	termKindOutput termLineKind = iota
	termKindError
	termKindEcho   // the prompt line a command was typed on
	termKindSystem // banners, help text and status notes from the terminal itself
)

type termLine struct {
	text  string
	isErr bool
	kind  termLineKind
	// rows is text wrapped to the console for one font and width; the
	// renderer re-wraps a line only when either changes.
	rows     []string
	rowsFont *ttf.Font
	rowsW    int32
}

type terminalState struct {
	lines []termLine
	input string
	// cursor is a byte offset into input, always on a rune boundary, so a
	// caret after "café" sits after the é and not inside it.
	cursor int
	// scrollY counts lines back from the newest; 0 follows new output.
	scrollY int
	started time.Time
	history []string
	histIdx int // -1 = editing a fresh line, otherwise history index
	// cwd is the terminal's own working directory. `cd` used to call
	// os.Chdir, which moved the whole app: every relative path JukaHub writes
	// (jukauser.json, alarms, notes, history) then landed in whatever folder
	// the user had wandered into, and the settings seemed to vanish.
	cwd     string
	prevCwd string
	// job is the external command running in the background, if any.
	job *termJob
	// unseen counts lines that arrived while the user was reading older
	// output; the console offers a jump back down to them.
	unseen int
	// fitTop is how many lines from the top of the buffer fill the console,
	// as measured by the last frame (0 = never drawn).
	fitTop int
	// pageLines is how many lines the last frame showed, for L1/R1 paging.
	pageLines int
	// caretAt is the last edit: the caret is solid for one blink after it.
	caretAt time.Time
	// liveLine is the unfinished last line of a running command — a progress
	// bar redrawn with \r — shown under the output until it completes.
	liveLine string
	// inputScroll is the prompt's horizontal scroll in pixels, so the caret
	// stays visible in a command longer than the field.
	inputScroll int32
}

var term terminalState

// termJob is one external command running off the event thread. Its output is
// collected here and moved into the terminal by termDrainJob on the render
// thread; the writer never blocks, so a command cannot stall on a full pipe.
type termJob struct {
	mu      sync.Mutex
	pending []termLine
	partial string
	dropped int
	done    bool
	err     error
	cancel  context.CancelFunc
	// started and command describe the run for the header read-out.
	started time.Time
	command string
	// stopped is set by Stop; left when leaving the screen ended the run;
	// timedOut when termCommandTimeout did.
	stopped  bool
	left     bool
	timedOut bool
	exitCode int
	// limit is the run's own timeout when it is not termCommandTimeout (the
	// apt built-in, linuxpkg_cli.go).
	limit time.Duration
	// input feeds a program's standard input (a Java program asking for a
	// name, javaterm.go): lines typed while it runs are queued here and a
	// goroutine writes them to the pipe. inputOpen goes false on Ctrl+D or
	// when the program ends. nil for a command that gets no input.
	input     chan []byte
	inputOpen bool
}

// termInputQueue is how many typed lines may wait for a program that has not
// read the ones before.
const termInputQueue = 64

// termInputPrompt marks a line typed to a running program in the scrollback.
const termInputPrompt = "> "

// takesInput reports whether the running program reads what is typed.
func (j *termJob) takesInput() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.inputOpen && !j.done
}

// sendInput queues b for the program; full is true when it has not read
// enough of what was typed before.
func (j *termJob) sendInput(b []byte) (sent, full bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.inputOpen || j.done {
		return false, false
	}
	select {
	case j.input <- b:
		return true, false
	default:
		return false, true
	}
}

// closeInput ends the program's input (Ctrl+D, or the program ended).
func (j *termJob) closeInput() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.inputOpen {
		return false
	}
	j.inputOpen = false
	close(j.input)
	return true
}

// termFeedInput writes queued lines to a program's input until the queue is
// closed; a program that stopped reading only makes the rest go nowhere.
func termFeedInput(w io.WriteCloser, ch <-chan []byte) {
	defer w.Close()
	for b := range ch {
		if _, err := w.Write(b); err != nil {
			for range ch {
			}
			return
		}
	}
}

func (j *termJob) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	text := j.partial + string(p)
	parts := strings.Split(text, "\n")
	j.partial = parts[len(parts)-1]
	if len(j.partial) > 4096 {
		parts = append(parts, "")
		j.partial = ""
	}
	for _, line := range parts[:len(parts)-1] {
		if len(j.pending) >= termJobMaxPending {
			j.dropped++
			continue
		}
		j.pending = append(j.pending, termLine{text: termCleanOutput(line)})
	}
	return len(p), nil
}

// termCleanOutput turns one line of program output into something the screen
// can draw: a carriage-return progress bar keeps only its last frame, colour
// escape codes are removed, tabs become spaces and invalid UTF-8 (a Windows
// tool writing in its OEM code page) becomes "?" instead of breaking the font.
func termCleanOutput(s string) string {
	s = strings.ToValidUTF8(s, "?")
	s = strings.TrimRight(s, "\r")
	if i := strings.LastIndexByte(s, '\r'); i >= 0 {
		s = s[i+1:]
	}
	if strings.IndexByte(s, 0x1b) >= 0 {
		s = termStripANSI(s)
	}
	if strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20 || r == 0x7f:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// termStripANSI removes terminal escape sequences (colours, cursor moves and
// window titles) that tools print when they think they own a real terminal.
func termStripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != 0x1b {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(s) {
			break
		}
		switch s[i+1] {
		case '[': // CSI: parameters, then one final byte in 0x40..0x7e
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j
		case ']': // OSC: ends with BEL or ESC \
			j := i + 2
			for j < len(s) {
				if s[j] == 0x07 {
					break
				}
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					j++
					break
				}
				j++
			}
			i = j
		default: // a two-byte escape
			i++
		}
	}
	return b.String()
}

// termDrainJob moves a running command's output into the terminal and reports
// its end. Render thread.
func termDrainJob() {
	j := term.job
	if j == nil {
		return
	}
	j.mu.Lock()
	lines := j.pending
	j.pending = nil
	dropped := j.dropped
	j.dropped = 0
	done := j.done
	partial := j.partial
	if done {
		j.partial = ""
	}
	err, stopped, left, timedOut, code := j.err, j.stopped, j.left, j.timedOut, j.exitCode
	limit := j.limit
	j.mu.Unlock()
	if limit <= 0 {
		limit = termCommandTimeout
	}
	for _, l := range lines {
		termAppend(l.text, termKindOutput)
	}
	if dropped > 0 {
		termAppend("… "+pluralizeCount(dropped, "line", "lines")+" of output skipped", termKindSystem)
	}
	if !done {
		term.liveLine = termCleanOutput(partial)
		return
	}
	term.liveLine = ""
	if s := termCleanOutput(partial); strings.TrimSpace(s) != "" {
		termAppend(s, termKindOutput)
	}
	switch {
	case left:
		termAppend("Stopped: the Terminal was closed while the command ran.", termKindSystem)
	case stopped:
		// Stop already said so when it was pressed.
	case timedOut:
		termAppend("Stopped after "+termDurationText(limit)+".", termKindError)
	case err != nil && code > 0:
		termAppend(fmt.Sprintf("Exited with code %d.", code), termKindError)
	case err != nil:
		termAppend("Error: "+firstLine(err.Error()), termKindError)
	}
	term.job = nil
}

// termDurationText says a duration the way a person would.
func termDurationText(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		if m := int(d / time.Minute); m != 1 {
			return fmt.Sprintf("%d minutes", m)
		}
		return "1 minute"
	}
	return d.Round(time.Second).String()
}

// termStopJob ends the running command, if there is one. It is Ctrl+C, the
// pad's X while a command runs and the prompt's Stop button.
func termStopJob() bool {
	j := term.job
	if j == nil {
		return false
	}
	j.mu.Lock()
	already := j.stopped || j.done
	j.stopped = true
	j.mu.Unlock()
	if !already {
		termAppend("Stopped.", termKindError)
	}
	j.cancel()
	return true
}

// termTakesInput reports whether the prompt feeds a running program now: a
// line job's input, or the on-screen keyboard typing into an interactive
// session (termpty_ui.go).
func termTakesInput() bool {
	return termPtyVKActive() || (term.job != nil && term.job.takesInput())
}

// termSendInput sends one typed line to the running program. What it printed
// so far on its current line (its question) and the answer join into one
// line of the scrollback, as on a real terminal.
func termSendInput(j *termJob, text string) {
	termFollow()
	termDrainJob()
	j.mu.Lock()
	partial := j.partial
	j.partial = ""
	j.mu.Unlock()
	term.liveLine = ""
	if p := termCleanOutput(partial); p != "" {
		termAppend(p+text, termKindOutput)
	} else {
		termAppend(termInputPrompt+text, termKindEcho)
	}
	switch sent, full := j.sendInput([]byte(text + "\n")); {
	case full:
		termAppend("The program has not read what you typed before — wait a moment.", termKindError)
	case !sent:
		termAppend("The program no longer reads input.", termKindSystem)
	}
}

// termCloseInput is Ctrl+D: the running program's input ends, which is how
// a program that reads until the end learns there is no more.
func termCloseInput() {
	if j := term.job; j != nil && j.closeInput() {
		termAppend("^D (end of input)", termKindSystem)
	}
}

// termJobParent is the context a new command runs under: the Terminal's scene
// scope while the Terminal is the screen, so leaving it stops the command the
// way leaving any screen stops its work (scenetasks.go).
func termJobParent() context.Context {
	if currentSceneWorkName() == "Terminal" {
		if ctx := sceneWorkContext(); ctx.Err() == nil {
			return ctx
		}
	}
	return context.Background()
}

// termLaunch starts cmd in the background and makes it the terminal's job.
func termLaunch(ctx context.Context, cancel context.CancelFunc, cmd *exec.Cmd, label string) {
	termLaunchCmd(ctx, cancel, cmd, label, 0, false)
}

// termLaunchInput is termLaunch for a program that reads what the user types
// (javaterm.go): its standard input is a pipe the prompt feeds, and limit is
// its own timeout.
func termLaunchInput(ctx context.Context, cancel context.CancelFunc, cmd *exec.Cmd, label string, limit time.Duration) {
	termLaunchCmd(ctx, cancel, cmd, label, limit, true)
}

func termLaunchCmd(ctx context.Context, cancel context.CancelFunc, cmd *exec.Cmd, label string, limit time.Duration, withInput bool) {
	job := &termJob{cancel: cancel, started: time.Now(), command: label, limit: limit}
	cmd.Stdout = job
	cmd.Stderr = job
	if withInput && cmd.Stdin == nil {
		if w, err := cmd.StdinPipe(); err == nil {
			job.input = make(chan []byte, termInputQueue)
			job.inputOpen = true
			go termFeedInput(w, job.input)
		}
	}
	// Its own process group (POSIX) or a process tree (Windows), stopped whole
	// on Stop, on leave or on timeout, and a bounded wait for output pipes a
	// stray grandchild still holds.
	screenChildGroupConfigure(cmd)
	cmd.Cancel = func() error { return termKillTree(cmd) }
	cmd.WaitDelay = 2 * time.Second
	term.job = job
	term.liveLine = ""
	go func() {
		defer cancel()
		err := cmd.Run()
		// The input goroutine ends with the program.
		job.closeInput()
		job.mu.Lock()
		defer job.mu.Unlock()
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			job.timedOut = true
		case errors.Is(ctx.Err(), context.Canceled):
			if !job.stopped {
				job.left = true
			}
			err = nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			job.exitCode = exitErr.ExitCode()
		}
		job.done, job.err = true, err
	}()
}

// termLaunchFunc runs fn as the terminal's job: work done inside JukaHub (the
// apt built-in) with the same streamed output, Stop and leave handling as a
// program, and its own timeout limit.
func termLaunchFunc(ctx context.Context, cancel context.CancelFunc, label string, limit time.Duration, fn func(ctx context.Context, w io.Writer) error) {
	job := &termJob{cancel: cancel, started: time.Now(), command: label, limit: limit}
	term.job = job
	term.liveLine = ""
	go func() {
		defer cancel()
		err := fn(ctx, job)
		job.mu.Lock()
		defer job.mu.Unlock()
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			job.timedOut = true
		case errors.Is(ctx.Err(), context.Canceled):
			if !job.stopped {
				job.left = true
			}
			err = nil
		}
		job.done, job.err = true, err
	}()
}

// termRunExternal runs a command line outside the app: through the platform
// shell (sh / cmd.exe), so pipes, quotes, wildcards and the shell's own
// commands work, or directly when the system has no shell to ask.
func termRunExternal(line string) {
	if term.job != nil {
		termAppend("A command is still running — press X (or Ctrl+C) to stop it first.", termKindError)
		return
	}
	ctx, cancel := context.WithTimeout(termJobParent(), termCommandTimeout)
	cmd := termShellCommand(ctx, line)
	if cmd == nil {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			cancel()
			return
		}
		// A name with a slash (./run.sh, bin/tool) is a path, and paths are
		// the terminal's: resolved against the directory `cd` chose, not the
		// app's, which LookPath would otherwise check.
		lookup := fields[0]
		if strings.ContainsAny(lookup, `/\`) {
			resolved, err := termPath(lookup)
			if err != nil {
				cancel()
				termAppend("Error: "+err.Error(), termKindError)
				return
			}
			lookup = resolved
		}
		binPath, err := exec.LookPath(lookup)
		if err != nil {
			cancel()
			termAppend("Command not found: "+fields[0], termKindError)
			return
		}
		cmd = exec.CommandContext(ctx, binPath, fields[1:]...)
	}
	cmd.Dir = term.cwd
	// On the handheld, the programs Linux packages installed come first
	// (linuxpkg_runtime.go); nothing changes anywhere else. JukaHub's Java
	// (javart.go) is first on PATH, with JAVA_HOME, when there is one.
	cmd.Env = javaShellEnv(linuxPkgShellEnv(append(os.Environ(), "TERM=dumb")))
	termLaunch(ctx, cancel, cmd, line)
}

// termShellMetaChars are the characters that only mean something to a shell:
// a line containing one goes to the shell even when its first word is one of
// the terminal's own commands (`ls | grep sav`, `echo $HOME > f`).
func termShellMetaChars() string {
	if runtime.GOOS == "windows" {
		return "|&<>^%\""
	}
	return "|&;<>()$`*?\"'\\"
}

// termHasProgram reports whether name is a real program on PATH.
func termHasProgram(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// termWantsShell decides whether one of the terminal's convenience commands
// (ls, cat, date, ...) should run as the real program instead: when the line
// uses shell syntax, or passes flags the built-in does not know and the system
// has the real tool. A plain `ls` or `cat notes.txt` stays built in, because
// the built-ins work the same on Windows and on the handheld.
func termWantsShell(command string, args []string, line string) bool {
	if strings.ContainsAny(line, termShellMetaChars()) {
		return true
	}
	switch command {
	case "echo":
		return false
	case "date", "whoami", "hostname", "uptime":
		return len(args) > 0 && termHasProgram(command)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return termHasProgram(command)
		}
		// cmd.exe's own switches (dir /b, dir /o-d — the Command Deck uses
		// both): dir and type are cmd built-ins, not programs on PATH.
		if runtime.GOOS == "windows" && (command == "dir" || command == "type") && strings.HasPrefix(a, "/") {
			return true
		}
	}
	return false
}

// termPath resolves a path the user typed against the terminal's directory.
func termPath(p string) (string, error) {
	clean, err := cleanUserPath(p)
	if err != nil {
		return "", err
	}
	if clean == "~" || strings.HasPrefix(clean, "~"+string(filepath.Separator)) {
		if home, herr := os.UserHomeDir(); herr == nil {
			clean = filepath.Join(home, strings.TrimPrefix(clean, "~"))
		}
	}
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(term.cwd, clean)
	}
	return filepath.Clean(clean), nil
}

// ── Scrollback ───────────────────────────────────────────────────────────

// termMaxScroll is how far the output can scroll back: until the oldest line
// is at the top of the console. fitTop is measured by the last frame; before
// the first frame a screenful is taken to be 12 lines.
func termMaxScroll() int {
	fit := term.fitTop
	if fit <= 0 {
		fit = 12
	}
	if m := len(term.lines) - fit; m > 0 {
		return m
	}
	return 0
}

// termAppend adds one line to the scrollback. At the bottom the view follows
// new output; while the user is reading older output the view stays exactly
// where it is and the new lines are counted for the "new output" pill.
func termAppend(text string, kind termLineKind) {
	term.lines = append(term.lines, termLine{text: text, isErr: kind == termKindError, kind: kind})
	if term.scrollY > 0 {
		term.scrollY++
		term.unseen++
	}
	if over := len(term.lines) - termMaxLines; over > 0 {
		copy(term.lines, term.lines[over:])
		for i := len(term.lines) - over; i < len(term.lines); i++ {
			term.lines[i] = termLine{}
		}
		term.lines = term.lines[:len(term.lines)-over]
	}
	if max := termMaxScroll(); term.scrollY > max {
		term.scrollY = max
	}
}

// termPushLine appends plain output, or an error line.
func termPushLine(text string, isErr bool) {
	kind := termKindOutput
	if isErr {
		kind = termKindError
	}
	termAppend(text, kind)
}

// termScroll moves the output viewport by delta lines (positive = older
// output), clamped to the available history.
func termScroll(delta int) {
	term.scrollY += delta
	if term.scrollY < 0 {
		term.scrollY = 0
	}
	if max := termMaxScroll(); term.scrollY > max {
		term.scrollY = max
	}
	if term.scrollY == 0 {
		term.unseen = 0
	}
}

// termFollow jumps back to the newest output and keeps following it.
func termFollow() {
	term.scrollY = 0
	term.unseen = 0
}

// termPageSize is one L1/R1 or PgUp/PgDn step: a screenful less one line, so
// the line at the edge stays in view as context.
func termPageSize() int {
	if term.pageLines > 2 {
		return term.pageLines - 1
	}
	return 10
}

// termClearScreen empties the scrollback (clear, cls, Ctrl+L).
func termClearScreen() {
	term.lines = nil
	term.liveLine = ""
	termFollow()
}

// ── Prompt editing ───────────────────────────────────────────────────────

// termClampCursor keeps a byte offset inside the input and on a rune boundary.
func termClampCursor(pos int) int {
	if pos < 0 {
		return 0
	}
	if pos > len(term.input) {
		return len(term.input)
	}
	for pos > 0 && pos < len(term.input) && !utf8.RuneStart(term.input[pos]) {
		pos--
	}
	return pos
}

// termSetCursor moves the caret and restarts its blink.
func termSetCursor(pos int) {
	term.cursor = termClampCursor(pos)
	term.caretAt = time.Now()
}

func termCursorLeft() {
	term.cursor = termClampCursor(term.cursor)
	if term.cursor > 0 {
		_, size := utf8.DecodeLastRuneInString(term.input[:term.cursor])
		term.cursor -= size
	}
	term.caretAt = time.Now()
}

func termCursorRight() {
	term.cursor = termClampCursor(term.cursor)
	if term.cursor < len(term.input) {
		_, size := utf8.DecodeRuneInString(term.input[term.cursor:])
		term.cursor += size
	}
	term.caretAt = time.Now()
}

// termWordStart is where the word before pos begins (Ctrl+Left, Ctrl+Backspace).
func termWordStart(s string, pos int) int {
	for pos > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:pos])
		if !unicode.IsSpace(r) {
			break
		}
		pos -= size
	}
	for pos > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:pos])
		if unicode.IsSpace(r) {
			break
		}
		pos -= size
	}
	return pos
}

// termWordEnd is where the word after pos ends (Ctrl+Right).
func termWordEnd(s string, pos int) int {
	for pos < len(s) {
		r, size := utf8.DecodeRuneInString(s[pos:])
		if !unicode.IsSpace(r) {
			break
		}
		pos += size
	}
	for pos < len(s) {
		r, size := utf8.DecodeRuneInString(s[pos:])
		if unicode.IsSpace(r) {
			break
		}
		pos += size
	}
	return pos
}

func termBackspace() {
	term.cursor = termClampCursor(term.cursor)
	if term.cursor == 0 {
		return
	}
	_, size := utf8.DecodeLastRuneInString(term.input[:term.cursor])
	term.input = term.input[:term.cursor-size] + term.input[term.cursor:]
	term.cursor -= size
	term.caretAt = time.Now()
}

func termDeleteRight() {
	term.cursor = termClampCursor(term.cursor)
	if term.cursor >= len(term.input) {
		return
	}
	_, size := utf8.DecodeRuneInString(term.input[term.cursor:])
	term.input = term.input[:term.cursor] + term.input[term.cursor+size:]
	term.caretAt = time.Now()
}

func termDeleteWordLeft() {
	term.cursor = termClampCursor(term.cursor)
	start := termWordStart(term.input, term.cursor)
	term.input = term.input[:start] + term.input[term.cursor:]
	term.cursor = start
	term.caretAt = time.Now()
}

// termClearLine empties the prompt (the pad's X, Ctrl+U).
func termClearLine() {
	term.input = ""
	term.cursor = 0
	term.inputScroll = 0
	term.histIdx = -1
	term.caretAt = time.Now()
}

// termInsertText inserts text at the caret. Typed input (SDL text events),
// clipboard paste (Ctrl+V) and termOpenWithCommand all take this one path:
// control characters are dropped, any printable character — "café", "Música",
// "ё" — is kept whole, and the caret moves past what was inserted rather than
// the text landing at the end of the line.
func termInsertText(text string) {
	if text == "" {
		return
	}
	var b strings.Builder
	for _, r := range text {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			continue
		}
		b.WriteRune(r)
	}
	add := b.String()
	room := termMaxInput - len(term.input)
	if room <= 0 || add == "" {
		return
	}
	if len(add) > room {
		cut := room
		for cut > 0 && !utf8.RuneStart(add[cut]) {
			cut--
		}
		add = add[:cut]
	}
	term.cursor = termClampCursor(term.cursor)
	term.input = term.input[:term.cursor] + add + term.input[term.cursor:]
	term.cursor += len(add)
	term.caretAt = time.Now()
}

func termHistoryStep(delta int) {
	if len(term.history) == 0 {
		return
	}
	if term.histIdx == -1 {
		term.histIdx = len(term.history)
	}
	term.histIdx += delta
	if term.histIdx < 0 {
		term.histIdx = 0
	}
	if term.histIdx >= len(term.history) {
		term.histIdx = -1
		term.input = ""
		term.cursor = 0
		term.caretAt = time.Now()
		return
	}
	term.input = term.history[term.histIdx]
	term.cursor = len(term.input)
	term.caretAt = time.Now()
}

// termRunInput runs what is in the prompt and clears it.
func termRunInput() {
	line := term.input
	term.input = ""
	term.cursor = 0
	term.inputScroll = 0
	term.caretAt = time.Now()
	termExecute(line)
}

// ── Opening ──────────────────────────────────────────────────────────────

// termPendingCommand is the command termOpenWithCommand pre-types. The scene's
// entry hook (termInit) moves it into the prompt; it is never run on its own.
var termPendingCommand string

// termOpenWithCommand opens the Terminal with command typed into the prompt but
// not run, so the user reads (and can edit) it before running it. Tools and the
// Toolbox call it (cross-area contract).
func termOpenWithCommand(config *Config, command string) {
	command = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, command))
	idx := sceneIndexExact(config, "Terminal")
	if idx < 0 {
		showToast("Terminal is not available", ToastWarn())
		return
	}
	if idx == currentSceneIndex {
		termClearLine()
		termInsertText(command)
		termFocusPrompt()
		return
	}
	termPendingCommand = command
	changeSceneTo(config, idx)
	if pendingSceneIndex != idx {
		// Another transition was already running; do not leave the command
		// waiting for some later visit.
		termPendingCommand = ""
	}
}

// termPendingRun is what termOpenAndRun runs once the Terminal is entered,
// if that is soon (termPendingRunAt): a switch that never arrived must not
// start a program on some later visit.
var (
	termPendingRun   func()
	termPendingRunAt time.Time
)

// termOpenAndRun opens the Terminal and runs fn there on the main thread: a
// Java program Files started (javarun.go) runs as the Terminal's job, so its
// output shows and what the user types reaches it.
func termOpenAndRun(config *Config, fn func()) {
	if fn == nil {
		return
	}
	idx := sceneIndexExact(config, "Terminal")
	if idx < 0 {
		showToast("Terminal is not available", ToastWarn())
		return
	}
	if idx == currentSceneIndex {
		termFocusPrompt()
		fn()
		return
	}
	termPendingRun, termPendingRunAt = fn, time.Now()
	changeSceneTo(config, idx)
	if pendingSceneIndex != idx {
		termPendingRun = nil
		showToast("One moment — try again", ToastInfo())
	}
}

// termInit runs when the Terminal scene is entered. The session — scrollback,
// directory, history — survives leaving and coming back.
func termInit() {
	term.input = ""
	term.cursor = 0
	term.inputScroll = 0
	term.histIdx = -1
	term.caretAt = time.Now()
	termFollow()
	termHistoryClose()
	// Opens the deck when a "Commands" entry asked for it, and starts the
	// device probe so the deck is ready by the time Select is pressed.
	cmdDeckTermEntered()
	// A command handed over from elsewhere is for the line terminal; an
	// interactive session left on screen waits behind it (termpty.go).
	if termPendingCommand != "" || termPendingRun != nil {
		termPty.view = false
	}
	if termPendingCommand != "" {
		termInsertText(termPendingCommand)
		termPendingCommand = ""
	}
	if term.cwd == "" {
		term.cwd, _ = os.Getwd()
	}
	// Commands survive a restart: the history file is small, read once, and
	// shared with Tab completion and the history picker.
	if len(term.history) == 0 {
		term.history = termLoadHistory()
	}
	if len(term.lines) == 0 {
		term.started = time.Now()
		termAppend("JukaHub Terminal", termKindSystem)
		termAppend(fmt.Sprintf("%s · %s/%s · shell: %s", P().Name(), runtime.GOOS, runtime.GOARCH, termShellName()), termKindSystem)
		if hintsUseKeyboard() {
			termAppend("Type help for the commands. Ctrl+P picks a ready-made command, Ctrl+R opens the history, Esc leaves.", termKindSystem)
		} else {
			termAppend("Type help for the commands. Select picks a ready-made command, A opens the keyboard, Y the history, B leaves.", termKindSystem)
		}
		termAppend("", termKindSystem)
	}
	if run := termPendingRun; run != nil {
		termPendingRun = nil
		if time.Since(termPendingRunAt) <= 15*time.Second {
			run()
		}
	}
}

// termShellName names the shell external commands run in.
func termShellName() string {
	if runtime.GOOS == "windows" {
		return "cmd.exe"
	}
	if termHasProgram("sh") {
		return "sh"
	}
	return "none (programs run directly)"
}

// ── Commands ─────────────────────────────────────────────────────────────

func termExecute(input string) {
	// The on-screen keyboard typing into an interactive session sends its
	// Enter through here (termpty_ui.go).
	if termPtyVKSubmit(input) {
		return
	}
	// A running program that reads its input gets the line instead of the
	// shell (javaterm.go); it is not a command, so it stays out of history.
	if j := term.job; j != nil && j.takesInput() {
		termSendInput(j, input)
		return
	}
	termRecordHistory(input)
	term.histIdx = -1
	// Running something is the moment the user wants to see its output.
	termFollow()
	termAppend(termPrompt+input, termKindEcho)
	cmdline := strings.TrimSpace(input)
	if cmdline == "" {
		return
	}
	trophyEvent(tkTermCmd)

	parts := strings.Fields(cmdline)
	command := strings.ToLower(parts[0])
	args := parts[1:]

	// A shell, htop, vi, nano, less, a REPL … get a real terminal
	// (termpty.go) instead of a pipe they would refuse.
	if termPtyWants(cmdline) {
		termPtyRunLine(cmdline)
		return
	}

	switch command {
	case "help", "?":
		termHelp()

	case "ls", "dir":
		if termWantsShell(command, args, cmdline) {
			termRunExternal(cmdline)
			return
		}
		termList(args)

	case "pwd":
		termAppend(term.cwd, termKindOutput)

	case "cd":
		termChangeDir(args)

	case "cat", "type":
		if command == "type" && runtime.GOOS != "windows" {
			termRunExternal(cmdline)
			return
		}
		if termWantsShell(command, args, cmdline) {
			termRunExternal(cmdline)
			return
		}
		termCat(args)

	case "echo":
		if termWantsShell(command, args, cmdline) {
			termRunExternal(cmdline)
			return
		}
		termAppend(strings.Join(args, " "), termKindOutput)

	case "date":
		if termWantsShell(command, args, cmdline) {
			termRunExternal(cmdline)
			return
		}
		termAppend(time.Now().Format("Mon Jan 2 15:04:05 MST 2006"), termKindOutput)

	case "uptime":
		if termWantsShell(command, args, cmdline) {
			termRunExternal(cmdline)
			return
		}
		termAppend(termUptimeText(), termKindOutput)

	case "whoami":
		if termWantsShell(command, args, cmdline) {
			termRunExternal(cmdline)
			return
		}
		termAppend(termUserName(), termKindOutput)

	case "hostname":
		if termWantsShell(command, args, cmdline) {
			termRunExternal(cmdline)
			return
		}
		name, _ := os.Hostname()
		termAppend(name, termKindOutput)

	case "history":
		if len(term.history) == 0 {
			termAppend("No commands yet.", termKindSystem)
			return
		}
		for i, h := range term.history {
			termAppend(fmt.Sprintf("%4d  %s", i+1, h), termKindOutput)
		}
		termAppend("Y (or Ctrl+R) opens the history picker.", termKindSystem)

	case "clear", "cls":
		termClearScreen()

	case "sysinfo":
		termSysInfo()

	case "neofetch":
		termNeofetch()

	case "calc":
		if len(args) == 0 {
			termAppend("Usage: calc <expression> (e.g. calc 2+2*3)", termKindError)
			return
		}
		val, err := calcEvalExpr(strings.Join(args, ""))
		if err != nil {
			termAppend(fmt.Sprintf("Error: %v", err), termKindError)
		} else {
			termAppend(fmt.Sprintf("= %g", val), termKindOutput)
		}

	case "ssh":
		// on|off|status is the SFTP server; a host connects (termpty_ssh.go).
		termPtySSHCommand(args, cmdline)

	case "apt", "apt-get", "jpkg":
		// Linux packages on the handheld (linuxpkg_cli.go); a PC's own apt
		// runs as a program.
		termLinuxPkg(command, cmdline, args)

	case "java", "javac", "jshell", "jar", "javaw":
		// JukaHub's Java or the computer's (javaterm.go), and java install.
		termJavaCommand(command, cmdline, args)

	case "exit", "quit":
		termLeave(appConfig)

	default:
		termRunExternal(cmdline)
	}
}

func termHelp() {
	for _, line := range []string{
		"Built-in commands:",
		"  help               this list",
		"  ls / dir [folder]  list a folder (folders end in /)",
		"  cd <folder>        change folder (cd - goes back, cd alone goes home)",
		"  pwd                print the current folder",
		"  cat <file>         show a text file",
		"  echo <text>        print text",
		"  history            recent commands (Y opens the picker)",
		"  clear              clear the screen (Ctrl+L)",
		"  date · uptime · whoami · hostname · sysinfo · neofetch",
		"  calc <expression>  evaluate maths, e.g. calc 2+2*3",
		"  ssh [on|off|status]  SFTP server for PC file access",
		"  ssh user@host      connect to another computer (ssh forget <host> drops its key)",
		"  shell              an interactive shell; htop, vi, nano, less, man, top and",
		"                     python with no arguments open the same way (" + termPtyLeaveWords() + ")",
		"  apt <command>      Linux packages on the handheld: update, install, remove, search",
		"  java <args>        Java programs: java -jar app.jar, java Hello.java, java -version",
		"  java install [8|11|17|21|25] [jre|jdk] · java update · java remove · java status",
		"  javac · jshell · jar   the JDK's tools (java install 25 jdk)",
		"  exit               leave the Terminal",
		"Anything else runs in the system shell (" + termShellName() + ") for up to " + termDurationText(termCommandTimeout) + ".",
		"While a Java program runs, the line you type goes to it (Enter / Start); Ctrl+D ends its input.",
		"Pad: A keyboard · Select commands · Start run · X clear / stop · Y history · L1/R1 page · B back",
		"Keyboard: Enter run · Ctrl+P commands · Up/Down history · Tab complete · PgUp/PgDn scroll · Esc back",
		"Commands (Select / Ctrl+P) runs ready-made commands with no typing; Y in the history pins one there.",
	} {
		termAppend(line, termKindSystem)
	}
}

// termList is the built-in ls: folders first, each with a trailing slash.
func termList(args []string) {
	target := term.cwd
	if len(args) > 0 {
		dir, err := termPath(args[0])
		if err != nil {
			termAppend("Error: "+err.Error(), termKindError)
			return
		}
		target = dir
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		termAppend(fmt.Sprintf("Error: %v", err), termKindError)
		return
	}
	if len(entries) == 0 {
		termAppend("(empty folder)", termKindSystem)
		return
	}
	type item struct {
		name string
		dir  bool
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		dir := e.IsDir()
		if !dir && e.Type()&os.ModeSymlink != 0 {
			// A link to a folder is listed as a folder, like ls -p does.
			if root, rootErr := os.OpenRoot(target); rootErr == nil {
				info, statErr := root.Stat(e.Name())
				root.Close()
				if statErr == nil && info.IsDir() {
					dir = true
				}
			}
		}
		items = append(items, item{name: e.Name(), dir: dir})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].dir != items[j].dir {
			return items[i].dir
		}
		return strings.ToLower(items[i].name) < strings.ToLower(items[j].name)
	})
	for i, it := range items {
		if i >= termListMax {
			termAppend(fmt.Sprintf("… and %d more", len(items)-termListMax), termKindSystem)
			break
		}
		if it.dir {
			termAppend(it.name+"/", termKindOutput)
		} else {
			termAppend(it.name, termKindOutput)
		}
	}
}

// termChangeDir moves the terminal's own directory; the app's stays put.
func termChangeDir(args []string) {
	target := ""
	switch {
	case len(args) == 0:
		home, err := os.UserHomeDir()
		if err != nil {
			termAppend(fmt.Sprintf("Error: %v", err), termKindError)
			return
		}
		target = home
	case args[0] == "-":
		if term.prevCwd == "" {
			termAppend("No previous folder yet.", termKindError)
			return
		}
		target = term.prevCwd
	default:
		dir, err := termPath(strings.Join(args, " "))
		if err != nil {
			termAppend("Error: "+err.Error(), termKindError)
			return
		}
		target = dir
	}
	info, err := os.Stat(target)
	if err != nil {
		termAppend(fmt.Sprintf("Error: %v", err), termKindError)
		return
	}
	if !info.IsDir() {
		termAppend("Error: not a directory: "+target, termKindError)
		return
	}
	if target != term.cwd {
		term.prevCwd = term.cwd
	}
	term.cwd = target
}

// termCat is the built-in cat: one bounded read of a file the user named.
func termCat(args []string) {
	if len(args) == 0 {
		termAppend("Usage: cat <filename>", termKindError)
		return
	}
	// The terminal is an intentional user shell: `cat` reads the path the
	// user typed. Reject malformed paths (empty/NUL) before the OS call.
	file, pathErr := termPath(strings.Join(args, " "))
	if pathErr != nil {
		termAppend("Error: "+pathErr.Error(), termKindError)
		return
	}
	f, err := os.Open(file)
	if err != nil {
		termAppend(fmt.Sprintf("Error: %v", err), termKindError)
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, termCatMaxBytes+1))
	f.Close()
	if err != nil {
		termAppend(fmt.Sprintf("Error: %v", err), termKindError)
		return
	}
	truncated := len(data) > termCatMaxBytes
	if truncated {
		data = data[:termCatMaxBytes]
	}
	for _, line := range strings.Split(string(data), "\n") {
		termAppend(termCleanOutput(line), termKindOutput)
	}
	if truncated {
		termAppend(fmt.Sprintf("… stopped after %d KB", termCatMaxBytes>>10), termKindSystem)
	}
}

// termUptimeText is the system's uptime, or the app's when the system will
// not say.
func termUptimeText() (out string) {
	defer func() {
		// gopsutil can panic on odd kernels; uptime is never worth a crash.
		if r := recover(); r != nil {
			out = "JukaHub has been running for " + termDurationText(time.Since(term.started).Round(time.Second))
		}
	}()
	secs, err := host.Uptime()
	if err != nil || secs == 0 {
		return "JukaHub has been running for " + termDurationText(time.Since(term.started).Round(time.Second))
	}
	up := time.Duration(secs) * time.Second
	days := int(up.Hours()) / 24
	return fmt.Sprintf("up %dd %dh %dm", days, int(up.Hours())%24, int(up.Minutes())%60)
}

// termUserName is the account the app runs as.
func termUserName() string {
	for _, key := range []string{"USER", "USERNAME", "LOGNAME"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return "unknown"
}

// termMemoryText is "used / total" RAM, or "" when the system will not say.
func termMemoryText() (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = ""
		}
	}()
	vm, err := mem.VirtualMemory()
	if err != nil || vm == nil || vm.Total == 0 {
		return ""
	}
	return fmt.Sprintf("%s / %s (%.0f%%)", formatBytes(vm.Used), formatBytes(vm.Total), vm.UsedPercent)
}

func termSysInfo() {
	termAppend("System", termKindSystem)
	termAppend("  OS:       "+runtime.GOOS+"/"+runtime.GOARCH, termKindOutput)
	termAppend(fmt.Sprintf("  CPUs:     %d", runtime.NumCPU()), termKindOutput)
	termAppend("  Platform: "+P().Name(), termKindOutput)
	if m := termMemoryText(); m != "" {
		termAppend("  Memory:   "+m, termKindOutput)
	}
	termAppend("  Uptime:   "+termUptimeText(), termKindOutput)
}

func termNeofetch() {
	hostname, _ := os.Hostname()
	memory := termMemoryText()
	if memory == "" {
		memory = "n/a"
	}
	art := []string{
		"     .--.    ",
		"    |o_o |   ",
		"    |:_/ |   ",
		"   //   \\ \\  ",
		"  (|     | ) ",
		" /'\\_   _/`\\ ",
		" \\___)=(___/ ",
	}
	info := []string{
		termUserName() + "@" + hostname,
		"OS:       " + runtime.GOOS + "/" + runtime.GOARCH,
		"Platform: " + P().Name(),
		fmt.Sprintf("CPUs:     %d", runtime.NumCPU()),
		"Memory:   " + memory,
		"Uptime:   " + termUptimeText(),
		"Shell:    " + termShellName(),
	}
	for i := range art {
		termAppend(art[i]+"  "+info[i], termKindOutput)
	}
}

// termSSHCommand reports or toggles the SSH/SFTP server. It is the console-side
// equivalent of the Tools row: the random password is printed here because the
// terminal is the device's own screen and there is no other way to read it
// without leaving the terminal.
func termSSHCommand(args []string) {
	action := "status"
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}
	switch action {
	case "on", "start":
		if sshSrv.Running() {
			termPushLine("SSH/SFTP is already running.", false)
			break
		}
		if toolsIPAddress() == "" {
			termPushLine("No WiFi address — connect to a network before starting SFTP.", true)
			return
		}
		if err := sshSrv.Start(); err != nil {
			termPushLine("Error: "+err.Error(), true)
			return
		}
	case "off", "stop":
		if !sshSrv.Running() {
			termPushLine("SSH/SFTP is not running.", false)
			break
		}
		sshSrv.Stop()
		termPushLine("SSH/SFTP server stopped.", false)
		return
	case "status":
	case "help", "-h", "--help":
		termPushLine("Usage: ssh [on|off|status]", false)
		return
	default:
		termPushLine("Usage: ssh [on|off|status]", true)
		return
	}

	if !sshSrv.Running() {
		termPushLine("SSH/SFTP server: stopped", false)
		termPushLine(fmt.Sprintf("  Root: %s   Port: %d", sshRootDir(), sshPort()), false)
		return
	}
	termPushLine("SSH/SFTP server: running", false)
	addr := sshSrv.Address()
	if addr == "" {
		addr = "no WiFi address"
	}
	termPushLine("  Address:  "+addr, false)
	termPushLine("  User:     "+sshSrv.User(), false)
	termPushLine("  Password: "+sshSrv.Password(), false)
	termPushLine("  Root:     "+sshSrv.Root(), false)
	termPushLine("  Host key: "+sshSrv.Fingerprint(), false)
	if sshSrv.ReadOnly() {
		termPushLine("  Mode:     read-only", false)
	}
	if n := len(sshSrv.Clients()); n > 0 {
		termPushLine(fmt.Sprintf("  Clients:  %d connected", n), false)
	}
	termPushLine("  Accepts the sftp subsystem only (no shell or tunnels).", false)
	termPushLine("  Stops by itself after 15 idle minutes or when the network changes.", false)
}

// ── Input ────────────────────────────────────────────────────────────────

// termPromptFocused reports whether typing lands in the prompt: the history
// picker is closed and the pad has not stepped up onto the top bar. The caret
// is only drawn while this holds.
func termPromptFocused() bool {
	return !termHist.open && !cmdDeck.open && chromeTier(focusEngine.ElementIndex()) == 0
}

// termFocusPrompt brings focus back to the prompt from the top bar.
func termFocusPrompt() {
	if chromeTier(focusEngine.ElementIndex()) != 0 {
		focusEngine.ClearFocus()
		selectedButtonIndex = -1
	}
	term.caretAt = time.Now()
}

// termLeave is B / Escape / exit: a running command stops with the screen.
func termLeave(config *Config) {
	termHistoryClose()
	cmdDeckClose()
	if term.job != nil {
		term.job.cancel()
	}
	goBackScene(config)
}

// handleTermInput processes physical keyboard input for the terminal.
func handleTermInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	if cmdDeck.open {
		cmdDeckKey(e, config)
		return
	}
	if termHist.open {
		termHistoryKey(e)
		return
	}
	ctrl := e.Keysym.Mod&sdl.KMOD_CTRL != 0
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE:
		termLeave(config)
	case sdl.K_p:
		if ctrl {
			cmdDeckOpen()
		}
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		termFocusPrompt()
		termRunInput()
	case sdl.K_BACKSPACE:
		if ctrl {
			termDeleteWordLeft()
		} else {
			termBackspace()
		}
	case sdl.K_DELETE:
		termDeleteRight()
	case sdl.K_LEFT:
		if ctrl {
			termSetCursor(termWordStart(term.input, termClampCursor(term.cursor)))
		} else {
			termCursorLeft()
		}
	case sdl.K_RIGHT:
		if ctrl {
			termSetCursor(termWordEnd(term.input, termClampCursor(term.cursor)))
		} else {
			termCursorRight()
		}
	case sdl.K_HOME:
		if ctrl {
			term.scrollY = termMaxScroll()
		} else {
			termSetCursor(0)
		}
	case sdl.K_END:
		if ctrl {
			termFollow()
		} else {
			termSetCursor(len(term.input))
		}
	case sdl.K_UP:
		if ctrl {
			termScroll(1)
		} else {
			termHistoryStep(-1)
		}
	case sdl.K_DOWN:
		if ctrl {
			termScroll(-1)
		} else {
			termHistoryStep(1)
		}
	case sdl.K_PAGEUP:
		termScroll(termPageSize())
	case sdl.K_PAGEDOWN:
		termScroll(-termPageSize())
	case sdl.K_TAB:
		// Tab completes what is typed. On an empty line it still opens the
		// on-screen keyboard: the handheld has no hardware keyboard, so that
		// remains a way to start typing.
		termComplete()
	case sdl.K_F7:
		termHistoryOpen()
	case sdl.K_r:
		if ctrl {
			termHistoryOpen()
		}
	case sdl.K_l:
		if ctrl {
			termClearScreen()
		}
	case sdl.K_u:
		if ctrl {
			termClearLine()
		}
	case sdl.K_c:
		if ctrl {
			termStopJob()
		}
	case sdl.K_d:
		if ctrl {
			termCloseInput()
		}
	}
}

// handleTermTextInput processes text input for the terminal.
func handleTermTextInput(e *sdl.TextInputEvent) {
	text := e.GetText()
	// An interactive session on screen gets what is typed (termpty_ui.go).
	if termPtyText(text) {
		return
	}
	if cmdDeck.open {
		cmdDeckType(text)
		return
	}
	if termHist.open {
		termHistoryType(text)
		return
	}
	termFocusPrompt()
	termInsertText(text)
}

// handleTermMouseClick routes clicks on the terminal's body: the Run/Stop and
// keyboard buttons, the "new output" pill, the prompt (which takes the caret to
// the click) and the console (which focuses the prompt). Clicks elsewhere —
// the top bar above all — are left to the shared handlers.
func handleTermMouseClick(mx, my int32, config *Config) bool {
	// The session screen, or the line terminal's "resumes" pill. A click the
	// session leaves (the footer) is not the line terminal's either.
	if termPtyClick(mx, my) {
		return true
	}
	if termPtyShowing() {
		return false
	}
	if cmdDeck.open {
		return cmdDeckClick(mx, my, config)
	}
	if termHist.open {
		return termHistoryClick(mx, my)
	}
	switch {
	case termRunRect.W > 0 && pointInRect(mx, my, termRunRect):
		termFocusPrompt()
		if term.job != nil {
			if termStopJob() {
				PlayBackSound()
			}
		} else if strings.TrimSpace(term.input) != "" {
			PlayActivateSound()
			termRunInput()
		}
	case termKeyboardRect.W > 0 && pointInRect(mx, my, termKeyboardRect):
		termFocusPrompt()
		PlayActivateSound()
		vkOpenTerminal()
	case termDeckRect.W > 0 && pointInRect(mx, my, termDeckRect):
		cmdDeckOpen()
	case termNewRect.W > 0 && pointInRect(mx, my, termNewRect):
		termFollow()
		PlayNavSound()
	case termInputRect.W > 0 && pointInRect(mx, my, termInputRect):
		termFocusPrompt()
		termSetCursor(termCursorAtX(mx))
		if P().IsHandheld() {
			// No hardware keyboard on the handheld: a tap on the prompt is
			// a request to type.
			vkOpenTerminal()
		}
	case termConsoleRect.W > 0 && pointInRect(mx, my, termConsoleRect):
		termFocusPrompt()
	default:
		return false
	}
	return true
}

// handleTermWheel scrolls the output (or the history picker) with the wheel.
func handleTermWheel(dy int32) bool {
	if dy == 0 {
		return false
	}
	if termPtyShowing() {
		return termPtyWheel(dy)
	}
	if cmdDeck.open {
		cmdDeckWheel(dy)
		return true
	}
	if termHist.open {
		termHistoryMove(-int(dy))
		return true
	}
	termScroll(int(dy) * termScrollStep)
	return true
}

// ctrlTerminal is the pad. B always leaves (the on-screen keyboard, when it is
// open, takes the pad before this runs and closes on B), and Up at the oldest
// line hands the press to the top bar instead of doing nothing.
func ctrlTerminal(e *sdl.ControllerButtonEvent, config *Config) bool {
	// An interactive session on screen has a pad map of its own
	// (termpty_ui.go).
	if termPtyShowing() {
		return termPtyPad(e, config)
	}
	if cmdDeck.open {
		return cmdDeckPad(e, config)
	}
	if termHist.open {
		return termHistoryPad(e)
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B:
		termLeave(config)
	case sdl.CONTROLLER_BUTTON_BACK:
		// Select is the Command Deck: ready-made commands, no typing.
		cmdDeckOpen()
	case sdl.CONTROLLER_BUTTON_A:
		// A is how a pad types: without the on-screen keyboard a
		// controller-only device cannot enter a command at all.
		termFocusPrompt()
		PlayActivateSound()
		vkOpenTerminal()
	case sdl.CONTROLLER_BUTTON_START:
		termFocusPrompt()
		// On an empty line Start resumes a session left running — unless a
		// program here waits for input, which the empty line answers.
		if strings.TrimSpace(term.input) == "" && termPtyAlive() && !termTakesInput() {
			PlayActivateSound()
			termPtyResume()
			return true
		}
		// An empty line is still an answer to a program that asked.
		if strings.TrimSpace(term.input) == "" && !termTakesInput() {
			return true
		}
		PlayActivateSound()
		termRunInput()
	case sdl.CONTROLLER_BUTTON_X:
		// X is Stop while a command runs (the pad's Ctrl+C) and clears the
		// line otherwise; the footer says which.
		if term.job != nil {
			if termStopJob() {
				PlayBackSound()
			}
		} else if term.input != "" {
			termClearLine()
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_Y:
		termHistoryOpen()
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		// scrollY counts lines back from the newest, so "nothing older" is
		// the maximum. At the oldest line Up belongs to the top bar, the way
		// it does on every screen, so the arrows always lead somewhere.
		if term.scrollY >= termMaxScroll() {
			return focusChromeBar()
		}
		termScroll(termScrollStep)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		termScroll(-termScrollStep)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		termFocusPrompt()
		termCursorLeft()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		termFocusPrompt()
		termCursorRight()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		termScroll(termPageSize())
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		termScroll(-termPageSize())
	default:
		return false
	}
	return true
}

// Scene registration. The Terminal draws its whole screen and owns its keys,
// pad and clicks.
type terminalScene struct{ sceneBase }

func (terminalScene) render(r *sdl.Renderer, c *Config) bool { renderTerminal(r, c); return true }

func (terminalScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	handleTermInput(e, c)
	return true
}

func (terminalScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return ctrlTerminal(e, c)
}

func init() { registerScene(terminalScene{sceneBase{name: "Terminal"}}) }
