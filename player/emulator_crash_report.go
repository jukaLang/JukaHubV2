package main

// emulator_crash_report.go — durable crash reports for games and emulators.
//
// JukaHub's own panics already leave crash-<time>.txt files (crashreport.go),
// and a game that stops within seconds gets a diagnosis card (gamediag.go).
// What was missing was a record for a game that crashed after playing for a
// while: the RetroArch and launcher logs are emptied on the next start, so the
// reason was gone by the time anyone looked. This writes a small, capped text
// report beside the app's crash reports on every launch that ends with a
// signal or a non-zero status, whether or not a card was shown.
//
// The reaper already has the diag record, the wait status and the launch
// record, so nothing here starts another process or reads the card twice.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	emuCrashPrefix    = "emulator-crash-"
	emuCrashKeep      = 10
	emuCrashTailLines = 40
	emuCrashReadMax   = 2 << 20
)

// emulatorCrashDir is a seam: tests point it at a temporary folder instead of
// the device's crash log folder.
var emulatorCrashDir = crashLogDir

// emulatorCrashClassify says what a launch's exit meant. A signal is the
// useful case: SIGSEGV/SIGABRT/SIGILL/SIGBUS/SIGFPE are a crash, SIGKILL on a
// handheld is almost always memory running out, and anything else is a stop
// from outside. A non-zero status without a signal is reported as a failure,
// not blamed on the core.
func emulatorCrashClassify(code, sig int, stopped bool) (kind, reason string) {
	if stopped {
		return "", ""
	}
	switch {
	case sig == 9:
		return "out of memory", gameDiagSignalNote(sig)
	case sig == 4 || sig == 6 || sig == 7 || sig == 8 || sig == 11:
		return "crash", gameDiagSignalNote(sig)
	case sig > 0:
		return "stopped", gameDiagSignalNote(sig)
	case code != 0:
		return "failed", "the launcher exited with status " + strconv.Itoa(code)
	}
	return "", ""
}

// emulatorCrashTail is the last lines of a log, bounded so a game that wrote
// for hours cannot block the reaper.
func emulatorCrashTail(path string) []string {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	if info.Size() > emuCrashReadMax {
		if _, err := f.Seek(info.Size()-emuCrashReadMax, 0); err != nil {
			return nil
		}
	}
	lines := make([]string, 0, emuCrashTailLines)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		lines = append(lines, line)
		if len(lines) > emuCrashTailLines {
			copy(lines, lines[len(lines)-emuCrashTailLines:])
			lines = lines[:emuCrashTailLines]
		}
	}
	return lines
}

// emulatorCrashSave writes one report and prunes the oldest ones.
func emulatorCrashSave(diag *gameDiagLaunch, waitErr error) {
	if diag == nil {
		return
	}
	code, sig := gameLaunchExitStatus(waitErr)
	kind, reason := emulatorCrashClassify(code, sig, diag.stopped)
	if kind == "" {
		return
	}
	dir, err := emulatorCrashDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	now := time.Now()
	path := filepath.Join(dir, emuCrashPrefix+now.Format("20060102-150405")+".txt")
	if _, err := os.Stat(path); err == nil {
		path = filepath.Join(dir, emuCrashPrefix+now.Format("20060102-15040500")+".txt")
	}

	var lines []string
	lines = append(lines,
		"JukaHub emulator crash report — "+now.Format("2006-01-02 15:04:05"),
		"Kind: "+kind,
		"Reason: "+reason,
		"")
	if rec := diag.rec; rec != nil {
		lines = append(lines,
			"Game: "+rec.Title,
			"System: "+rec.System,
			"ROM: "+rec.ROM,
			"Launcher: "+rec.Launcher,
			"Program: "+rec.Program,
			"Arguments: "+strings.Join(rec.Args, " "),
			"Started: "+rec.Started.Format(time.RFC3339),
			"Memory free at start: "+strconv.Itoa(rec.MemMB)+" MB",
		)
	} else {
		lines = append(lines,
			"Game: "+diag.game.Title,
			"System: "+diag.game.System,
			"Launcher: "+diag.launcher,
		)
	}
	ranFor := time.Since(diag.started)
	if !diag.ended.IsZero() {
		ranFor = diag.ended.Sub(diag.started)
	}
	lines = append(lines,
		"Ran for: "+ranFor.Round(time.Second).String(),
		"Ended: "+diag.ended.Format(time.RFC3339),
		"Exit status: "+strconv.Itoa(code),
		"Signal: "+strconv.Itoa(sig),
		"Stopped by JukaHub: "+strconv.FormatBool(diag.stopped),
	)
	// A Wine program's own runtime is part of the story: which Wine build,
	// which prefix, and which display it was using.
	if rec := diag.rec; rec != nil && strings.Contains(strings.ToLower(rec.Launcher+" "+rec.Program), "wine") {
		status := currentWindowsRuntimeStatus()
		lines = append(lines, "", "Wine runtime:")
		lines = append(lines,
			"  Flavor: "+status.Flavor,
			"  Prefix: "+status.Prefix,
			"  Display: "+status.displayLabel(),
			"  Problem: "+stringOrNone(status.Problem),
		)
		if missing := wineComponentsMissing(); len(missing) > 0 {
			var names []string
			for _, c := range missing {
				names = append(names, c.Name)
			}
			lines = append(lines, "  Components not installed: "+strings.Join(names, ", "))
		}
	}
	logPath := diag.outPath
	if logPath == "" {
		logPath = diag.logPath
	}
	tail := emulatorCrashTail(logPath)
	if len(tail) > 0 {
		lines = append(lines, "", "Last lines of "+filepath.Base(logPath)+":")
		for _, line := range tail {
			lines = append(lines, "  "+line)
		}
	}
	if err := AtomicWrite(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		logWarn("crash", "could not save the emulator crash report: %v", err)
		return
	}
	logWarn("crash", "emulator %s: saved %s", kind, filepath.Base(path))
	emulatorCrashPrune(dir)
}

// stringOrNone keeps a blank field readable in a saved report.
func stringOrNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return value
}

// emulatorCrashPrune keeps the newest reports. A pair of reports a second in
// the same second is fine: the suffix makes the name unique.
func emulatorCrashPrune(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && strings.HasPrefix(name, emuCrashPrefix) && strings.HasSuffix(name, crashFileExt) {
			names = append(names, name)
		}
	}
	if len(names) <= emuCrashKeep {
		return
	}
	sort.Strings(names)
	for _, name := range names[:len(names)-emuCrashKeep] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// emulatorCrashReports is the saved reports, newest first, for a card or a
// phone page that wants to list them.
func emulatorCrashReports() []string {
	dir, err := emulatorCrashDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), emuCrashPrefix) && strings.HasSuffix(e.Name(), crashFileExt) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

// emulatorCrashSummary is the first lines of the newest report, for a toast
// or a bug report where the whole file would be too much.
func emulatorCrashSummary(max int) string {
	reports := emulatorCrashReports()
	if len(reports) == 0 {
		return ""
	}
	data, err := os.ReadFile(reports[0])
	if err != nil {
		return ""
	}
	text := string(data)
	if max > 0 && len(text) > max {
		text = text[:max] + "\n…"
	}
	return fmt.Sprintf("%s\n\n%s", filepath.Base(reports[0]), text)
}
