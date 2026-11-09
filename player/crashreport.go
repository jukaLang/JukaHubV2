package main

// crashreport.go — what a crash leaves behind, and how the process ends after
// one.
//
// Before: a panic on the main goroutine was logged and main returned, so the
// process exited 0 and launch.sh could not tell a crash from a quit; a panic on
// any other goroutine went to stderr only; Windows kept no log file at all.
//
// Now every session opens crash-session-<pid>.txt in the log folder, writes one
// header line and hands it to debug.SetCrashOutput: whatever kills the process
// from inside Go — a panic on any goroutine, a fatal runtime error, a signal in
// C code — is printed there as well as on stderr. A clean session leaves only
// the header. The next start turns a session file with anything after its
// header into crash-<yyyymmdd-hhmmss>.txt (keeping the newest crashKeep),
// deletes the others, and the recovery card offers "JukaHub closed
// unexpectedly" with Send report and Restart in safe mode (recoverycard.go).
//
// A panic on the main goroutine still runs main's deferred cleanup (the SDL
// teardown, the chime, the servers), but no longer quietly: crashGuardMain,
// deferred just before the loop so it runs first, records the panic and its
// stack in the session file at once, stops the game that may own the screen,
// puts the LEDs and the CPU back, and arms a hard deadline, so a cleanup step
// that blocks after a panic cannot leave a frozen frame. The top-level handler
// in main then re-panics: the runtime prints the stack to errors.txt and the
// crash file and exits with status 2, as it does for any other goroutine.
//
// The log folder is where launch.sh writes errors.txt on the handheld (the
// app's own folder), %LOCALAPPDATA%\JukaHub\logs on Windows (created on
// demand), and <data>/logs on a Linux desktop.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	crashSessionPrefix = "crash-session-"
	crashFilePrefix    = "crash-"
	crashFileExt       = ".txt"
	// crashKeep is how many crash reports stay in the log folder.
	crashKeep = 5
	// crashProbeBytes is how much of a session file is read to tell a crash
	// from a clean session; crashReadMax bounds what a report reads.
	crashProbeBytes = 64 << 10
	crashReadMax    = 256 << 10
	// crashSummaryMax bounds the excerpt a bug report carries.
	crashSummaryMax = 3000
	// crashTeardownGrace bounds the cleanup that still runs after a panic on
	// the main goroutine. It is shorter than the exit watchdog's grace, so a
	// crash during the exit teardown also ends on the crash path.
	crashTeardownGrace = 5 * time.Second
	// crashPingWait and crashCleanupWait bound what the top-level handler
	// waits for before it re-panics: the opt-in crash ping, then the child
	// stop. Together well under crashTeardownGrace.
	crashPingWait    = 1 * time.Second
	crashCleanupWait = 2 * time.Second
	// crashExitCode is the status the Go runtime exits with after an
	// unrecovered panic; launch.sh reads it as "crashed".
	crashExitCode = 2
)

// crashState is this process's crash bookkeeping.
var crashState struct {
	mu      sync.Mutex
	dir     string    // the log folder
	session string    // this session's crash-session-<pid>.txt, "" when off
	last    string    // the report archived at this start, "" when the last session ended well
	lastAt  time.Time // when that report was written (the device's clock then)
}

// crashInProgress is raised by the first sign of a crash on the main
// goroutine; forced exits use it to leave with crashExitCode.
var crashInProgress atomic.Bool

// crashCleanupDone is closed when the crash cleanup (children, LEDs, CPU) is
// over, so the top-level handler can wait for it before the process ends.
var crashCleanupDone = make(chan struct{})

// processExitCode is the status a forced exit leaves: 0 for a quit that took
// too long, crashExitCode once a crash is in progress.
func processExitCode() int {
	if crashInProgress.Load() {
		return crashExitCode
	}
	return 0
}

// crashLogDir is the folder crash reports live in, created on demand.
func crashLogDir() (string, error) {
	if IsWindows() {
		base := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil || strings.TrimSpace(home) == "" {
				return "", errors.New("no LOCALAPPDATA and no home folder")
			}
			base = filepath.Join(home, "AppData", "Local")
		}
		dir := filepath.Join(base, "JukaHub", "logs")
		return dir, os.MkdirAll(dir, 0o755)
	}
	if DetectDevice().IsTrimui() {
		// launch.sh appends the app's stderr to errors.txt in its own folder,
		// which is the folder the binary is in.
		return P().ExecutableDir()
	}
	data, err := P().DataDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(data, "logs")
	return dir, os.MkdirAll(dir, 0o755)
}

// crashSessionHeader is the first line of a session file. Anything after it
// means the session crashed.
func crashSessionHeader(now time.Time) string {
	build := strings.TrimSpace(buildTime)
	if build == "" {
		build = strings.TrimSpace(buildDate)
	}
	if build == "" {
		build = "unstamped build"
	}
	release := strings.TrimSpace(readInstalledReleaseInfo().Tag)
	if release == "" {
		release = "no release stamp"
	}
	return fmt.Sprintf("JukaHub session started %s · %s · %s · %s/%s · pid %d\n",
		now.Format("2006-01-02 15:04:05 -0700"), release, build, runtime.GOOS, runtime.GOARCH, os.Getpid())
}

// initCrashReports archives what the last session left and opens this
// session's crash output. main calls it right after InitLogging.
func initCrashReports() {
	dir, err := crashLogDir()
	if err != nil || dir == "" {
		if err == nil {
			err = errors.New("the log folder is unknown")
		}
		logWarn("crash", "no folder for crash reports: %v", err)
		return
	}
	archived, at := crashCollect(dir)
	crashPrune(dir, crashKeep, archived)
	if archived != "" {
		logWarn("crash", "the last session crashed; its report is %s", archived)
	}
	session := filepath.Join(dir, crashSessionPrefix+strconv.Itoa(os.Getpid())+crashFileExt)
	_ = os.Remove(session)
	f, err := os.OpenFile(session, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		logWarn("crash", "crash output is off: %v", err)
		session = ""
	} else {
		_, _ = f.WriteString(crashSessionHeader(time.Now()))
		if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
			logWarn("crash", "crash output is off: %v", err)
			session = ""
		}
		// SetCrashOutput keeps its own duplicate of the descriptor.
		_ = f.Close()
	}
	crashState.mu.Lock()
	crashState.dir, crashState.session = dir, session
	crashState.last, crashState.lastAt = archived, at
	crashState.mu.Unlock()
}

// crashHasReport reports whether a session file holds more than its header.
func crashHasReport(data []byte) bool {
	i := bytes.IndexByte(data, '\n')
	if i < 0 {
		return false
	}
	return len(bytes.TrimSpace(data[i+1:])) > 0
}

// crashReadHead reads at most limit bytes of path.
func crashReadHead(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

// crashArchiveName is a free crash-<time>.txt name in dir.
func crashArchiveName(dir string, at time.Time) string {
	base := crashFilePrefix + at.Format("20060102-150405")
	path := filepath.Join(dir, base+crashFileExt)
	for n := 2; pathExists(path); n++ {
		path = filepath.Join(dir, base+"-"+strconv.Itoa(n)+crashFileExt)
	}
	return path
}

// crashCollect turns the session files earlier processes left in dir into
// crash reports (those that crashed) or removes them (those that did not). It
// returns the newest report it made and when it was written.
func crashCollect(dir string) (string, time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", time.Time{}
	}
	var newest string
	var newestAt time.Time
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, crashSessionPrefix) || !strings.HasSuffix(name, crashFileExt) {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		data, err := crashReadHead(path, crashProbeBytes)
		if err != nil {
			continue
		}
		if !crashHasReport(data) {
			// A session that ended well, or one still running elsewhere: a
			// running process's file cannot be removed on Windows, and on Linux
			// only its header is lost.
			_ = os.Remove(path)
			continue
		}
		dest := crashArchiveName(dir, info.ModTime())
		if err := os.Rename(path, dest); err != nil {
			continue
		}
		if newest == "" || info.ModTime().After(newestAt) {
			newest, newestAt = dest, info.ModTime()
		}
	}
	return newest, newestAt
}

// crashReports lists the crash reports in dir, newest first: by modification
// time, then by name.
func crashReports(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type report struct {
		path string
		mod  time.Time
	}
	var list []report
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, crashFilePrefix) || strings.HasPrefix(name, crashSessionPrefix) ||
			!strings.HasSuffix(name, crashFileExt) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, report{path: filepath.Join(dir, name), mod: info.ModTime()})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].mod.Equal(list[j].mod) {
			return list[i].mod.After(list[j].mod)
		}
		return list[i].path > list[j].path
	})
	out := make([]string, len(list))
	for i, r := range list {
		out[i] = r.path
	}
	return out
}

// crashPrune keeps the newest keep reports in dir. keepAlso (the report this
// start made) is never removed: on a handheld without a clock its time can sort
// before reports written while the clock was right.
func crashPrune(dir string, keep int, keepAlso string) {
	kept := 0
	for _, path := range crashReports(dir) {
		if keepAlso != "" && sameFilePath(path, keepAlso) {
			continue
		}
		limit := keep
		if keepAlso != "" {
			limit--
		}
		if kept < limit {
			kept++
			continue
		}
		_ = os.Remove(path)
	}
}

// crashSummary is the part of a crash report a bug report carries: the panic
// or fatal error line and the stack of the goroutine that raised it, bounded.
func crashSummary(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	start := -1
	for i, line := range lines {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "panic: ") || strings.HasPrefix(l, "fatal error: ") ||
			strings.HasPrefix(l, "SIGSEGV") || strings.HasPrefix(l, "SIGBUS") || strings.HasPrefix(l, "SIGABRT") ||
			strings.HasPrefix(l, "unexpected signal") {
			start = i
			break
		}
	}
	if start < 0 {
		// No marker: the session file's own text after its header.
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
		return bugrepClip(strings.TrimSpace(text), crashSummaryMax)
	}
	var b strings.Builder
	stack := false
	frames := 0
	for _, line := range lines[start:] {
		if strings.HasPrefix(line, "goroutine ") {
			if stack {
				break // the next goroutine: the one that crashed is enough
			}
			stack = true
		}
		if stack && strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "goroutine ") {
			frames++
			if frames > 40 {
				b.WriteString("…\n")
				break
			}
		}
		b.WriteString(line)
		b.WriteByte('\n')
		if b.Len() > crashSummaryMax {
			break
		}
	}
	return bugrepClip(strings.TrimSpace(b.String()), crashSummaryMax)
}

// crashLastReport is the report archived at this start, when the last session
// crashed.
func crashLastReport() (string, time.Time) {
	crashState.mu.Lock()
	defer crashState.mu.Unlock()
	return crashState.last, crashState.lastAt
}

// crashRecord appends a panic and its stack to this session's file, so the
// report exists even if the cleanup that follows never finishes.
func crashRecord(r any, stack []byte) {
	crashState.mu.Lock()
	session := crashState.session
	crashState.mu.Unlock()
	if session == "" {
		return
	}
	f, err := os.OpenFile(session, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "\n=== panic on the main goroutine, recorded before the cleanup ran ===\npanic: %v\n\n%s\n", r, stack)
	_ = f.Sync()
	_ = f.Close()
}

// crashBegin is the first response to a panic on the main goroutine: write it
// down, give the screen and the hardware back off the unwinding goroutine, and
// make sure the process ends within crashTeardownGrace whatever the deferred
// cleanup does. Only the first call acts.
func crashBegin(r any, stack []byte) {
	if !crashInProgress.CompareAndSwap(false, true) {
		return
	}
	// The deadline first: writing the report syncs a file, and a dying card
	// can hold that up for as long as it likes.
	time.AfterFunc(crashTeardownGrace, func() {
		logError("exit", "the cleanup after a crash did not finish within %s; leaving now", crashTeardownGrace)
		killScreenChildren()
		os.Exit(crashExitCode)
	})
	crashRecord(r, stack)
	go func() {
		defer close(crashCleanupDone)
		// A game the crash left running owns the framebuffer: launch.sh would
		// hand the screen back to MainUI underneath it.
		stopScreenChildren(exitChildGrace)
		_ = SetLEDSync(false)
		exitLastChanceRestore(exitLastChanceGrace)
	}()
}

// crashGuardMain is deferred just before the main loop, so it is the first
// deferred call to run when the main goroutine panics. It does not stop the
// panic: the rest of main's cleanup runs next, then the top-level handler.
func crashGuardMain() {
	if r := recover(); r != nil {
		crashBegin(r, debug.Stack())
		panic(r)
	}
}

// crashOnMainPanic is main's top-level handler, run after every other deferred
// call. It re-panics, so the runtime prints the panic and the stack of the
// frames that raised it (they are still on the stack) to stderr and the crash
// file, and the process exits with crashExitCode — never 0.
func crashOnMainPanic(r any, stack []byte) {
	crashBegin(r, stack) // a panic before the loop: the guard was not deferred yet
	logError("main", "panic: %v — JukaHub is closing; the crash report is in the log folder", r)
	// Only the fact that a panic happened leaves the device, and only when the
	// user opted in. Bounded: the process is on its way out, and both waits
	// together stay well inside crashTeardownGrace, so the re-panic below (the
	// stack on stderr, exit 2) normally wins over the hard deadline.
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		TelemetryCrash(appConfig)
	}()
	deadline := time.NewTimer(crashPingWait)
	select {
	case <-sent:
	case <-deadline.C:
	}
	deadline.Reset(crashCleanupWait)
	select {
	case <-crashCleanupDone:
	case <-deadline.C:
	}
	deadline.Stop()
	panic(r)
}
