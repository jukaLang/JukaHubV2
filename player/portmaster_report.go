package main

// portmaster_report.go — what a bug report and "Why didn't it open?" say about
// PortMaster.
//
// Report JH-QCKJ9M (TrimUI Brick, 2026-11-20) was sent right after PortMaster
// failed, with no description, and held nothing about PortMaster at all: the
// session log covered only the 20 seconds since JukaHub had started again, and
// the launch logs that said what happened stayed on the card. So the
// diagnostics report (DiagnosticsText) now carries a PortMaster section on a
// handheld that has PortMaster or has launched a port: what PortMaster gets
// (bash, /bin/bash, python3), the last launch's end and the last lines of its
// launch log and of PortMaster's own log.txt, where PortMaster's launcher
// sends its output (a Python traceback from pugwash lands there). The reporter
// still sees all of it before sending, and it is scrubbed like the rest.
//
// None of it starts a program: what was not checked this session is read from
// the files alone, so building a report never waits on the card.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// pmReportLines and pmReportLineMax bound each log excerpt in a report.
	pmReportLines   = 24
	pmReportLineMax = 160
	// pmReportTailBytes is how much of a log's end is read.
	pmReportTailBytes = 16 << 10
	// pmReportRecent is how old a launch may be for its logs to be quoted.
	pmReportRecent = 24 * time.Hour
)

// pmOwnLogPath is PortMaster's own log.txt (its launcher's output), or "".
func pmOwnLogPath() string {
	control := portRealControlFolder()
	if control == "" {
		return ""
	}
	p := filepath.Join(control, "log.txt")
	if !regularFile(p) {
		return ""
	}
	return p
}

// pmTailLines is the last n non-empty lines of the last pmReportTailBytes of
// path, each cut to pmReportLineMax bytes.
func pmTailLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return nil
	}
	start := info.Size() - pmReportTailBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, pmReportTailBytes))
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r", ""), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	out := portLastLines(lines, n)
	for i, line := range out {
		if len(line) > pmReportLineMax {
			cut := pmReportLineMax
			for cut > 0 && (line[cut]&0xC0) == 0x80 {
				cut--
			}
			out[i] = line[:cut] + "…"
		}
	}
	return out
}

// pmNewestPortLog is the newest launch log in data/gamediag/ports, or "".
func pmNewestPortLog() (string, time.Time) {
	dir := gameDiagLogDir()
	if dir == "" {
		return "", time.Time{}
	}
	entries, err := os.ReadDir(filepath.Join(dir, portLogDirName))
	if err != nil {
		return "", time.Time{}
	}
	best, bestAt := "", time.Time{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if best == "" || info.ModTime().After(bestAt) {
			best, bestAt = filepath.Join(dir, portLogDirName, e.Name()), info.ModTime()
		}
	}
	return best, bestAt
}

// pmLaunchEnd is the end line portLogNoteEnd wrote into a launch log, without
// its prefix ("after 2.1s (exit status 1)"), or "".
func pmLaunchEnd(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if rest, ok := strings.CutPrefix(lines[i], portLogEndPrefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// pmBinBashState describes /bin/bash from the file alone (no program is run).
func pmBinBashState() string {
	info, err := os.Lstat(pmBinBashSystem)
	if err != nil {
		return "missing"
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(pmBinBashSystem)
		switch {
		case err != nil:
			return "a broken link"
		case strings.Contains(filepath.Base(resolved), "busybox"):
			return "BusyBox's sh (a link)"
		default:
			return "a link to " + filepath.Base(resolved)
		}
	}
	if !info.Mode().IsRegular() {
		return "not a file"
	}
	return fmt.Sprintf("a program (%d KB, not run)", info.Size()>>10)
}

// pmPythonInstalledName is the archive JukaHub's Python came from, or "".
func pmPythonInstalledName() string {
	bin := pmPythonBinDir()
	if bin == "" {
		return ""
	}
	var m pmPythonInstalled
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(bin), pmPythonMarker))
	if err != nil || len(raw) > 4<<10 || json.Unmarshal(raw, &m) != nil || m.Name == "" {
		return "installed"
	}
	name := strings.TrimSuffix(strings.TrimSuffix(m.Name, ".tar.gz"), "-aarch64-unknown-linux-gnu-install_only_stripped")
	return strings.TrimSuffix(name, "-aarch64-unknown-linux-gnu-install_only")
}

// portMasterDiagnosticsText is the PortMaster section of the diagnostics
// report, "" on a desktop or a handheld without PortMaster and port launches.
// Any thread: it reads files only, and config only for portBinBash. The text
// is scrubbed here (bugrepScrub), since Export diagnostics and the phone link
// send DiagnosticsText without the bug report's own pass.
func portMasterDiagnosticsText(config *Config) string {
	return bugrepScrub(portMasterDiagnosticsRaw(config))
}

func portMasterDiagnosticsRaw(config *Config) string {
	if runtime.GOOS != "linux" || !P().IsHandheld() {
		return ""
	}
	installed := portMasterInstalled()
	newest, newestAt := pmNewestPortLog()
	if !installed && newest == "" {
		return ""
	}
	var sb strings.Builder
	state := "not installed"
	if installed {
		state = "installed"
		if v := portMasterLocalVersion(); v != "" {
			state += ", version " + v
		}
		launcher := portMasterLauncher(portMasterSDRoot)
		switch {
		case pmExConfigPresent():
			state += "; TRIMUI_EX: yes"
		case pmLauncherReadsExConfig(launcher):
			state += "; TRIMUI_EX: no (its launcher reads ex_config: JukaHub runs a patched copy)"
		default:
			state += "; TRIMUI_EX: no"
		}
	}
	sb.WriteString("PortMaster   : " + state + "\n")
	if pmRuntimeApplies() {
		c := pmRuntimeSnapshot()
		bash := "JukaHub's: not installed"
		if privateGlibcBashReady() {
			bash = "JukaHub's: ready (Ubuntu's on the private glibc " + privateGlibcVersion + ")"
		}
		binBash := pmBinBashState()
		if c.Done {
			switch {
			case c.BashOK:
				bash = "the firmware's first bash works; " + bash
			case c.BashGood != "":
				bash = "a firmware bash further down PATH works; " + bash
			default:
				bash = setOr(c.BashNote, "the firmware has none") + "; " + bash
			}
			if c.BinBashOK {
				binBash = "bash (checked)"
			} else {
				binBash = setOr(c.BinBashNote, binBash) + " (checked)"
			}
		}
		if pmBinBashOff(config) {
			binBash += "; portBinBash is off"
		}
		sb.WriteString("PM bash      : " + bash + "\n")
		sb.WriteString("PM /bin/bash : " + binBash + "\n")
		py := "JukaHub's: not installed"
		if name := pmPythonInstalledName(); name != "" {
			py = "JukaHub's: " + name
		}
		switch {
		case !c.Done:
		case c.PythonByEx:
			py = "TRIMUI_EX's; " + py
		case c.PythonOK:
			py = "the firmware's works; " + py
		default:
			py = setOr(c.PythonNote, "the firmware has none") + "; " + py
		}
		sb.WriteString("PM python3   : " + py + "\n")
		checked := "not yet this session"
		if c.Done {
			checked = "this session"
		}
		if e := pmRuntimeErrSuffix(); e != "" {
			checked += e
		}
		sb.WriteString("PM checked   : " + checked + "\n")
	}
	if _, note, at := pmSplashLastNote(pmReportRecent); note != "" {
		sb.WriteString("PM splash    : " + note + ", " + timeAgo(at) + "\n")
	}
	if newest == "" {
		return sb.String()
	}
	age := time.Since(newestAt)
	lines := pmTailLines(newest, pmReportLines+2)
	launch := strings.TrimSuffix(filepath.Base(newest), ".log") + ", " + timeAgo(newestAt)
	if end := pmLaunchEnd(lines); end != "" {
		launch += ", ended " + end
	} else if age < time.Minute {
		launch += ", may still run"
	}
	sb.WriteString("Port launch  : " + launch + "\n")
	if age > pmReportRecent || age < -time.Hour {
		return sb.String()
	}
	quote := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		if len(lines) > pmReportLines {
			lines = lines[len(lines)-pmReportLines:]
		}
		sb.WriteString("  " + title + ":\n")
		for _, line := range lines {
			sb.WriteString("  | " + line + "\n")
		}
	}
	var keep []string
	for _, line := range lines {
		if !strings.HasPrefix(line, portLogEndPrefix) {
			keep = append(keep, line)
		}
	}
	quote("JukaHub's launch log (last lines)", keep)
	if own := pmOwnLogPath(); own != "" {
		if info, err := os.Stat(own); err == nil && time.Since(info.ModTime()) <= pmReportRecent {
			quote("PortMaster's log.txt, "+timeAgo(info.ModTime())+" (last lines)", pmTailLines(own, pmReportLines))
		}
	}
	return sb.String()
}

// pmOwnLogQuickExit is the cause PortMaster's own log.txt names for a
// PortMaster that just closed at once, or "". Off the main thread.
func pmOwnLogQuickExit() string {
	own := pmOwnLogPath()
	if own == "" {
		return ""
	}
	if info, err := os.Stat(own); err != nil || time.Since(info.ModTime()) > 2*time.Minute {
		return ""
	}
	issues := pmOwnLogFindings(pmTailLines(own, 40))
	if len(issues) == 0 {
		return ""
	}
	return "PortMaster closed at once: " + issues[0].Title + " — " + hintKeyFor("Y", "Options") + " on its row, then Why didn't it open?"
}

// pmOwnLogFindings are the causes PortMaster's own log.txt names, for "Why
// didn't it open?".
func pmOwnLogFindings(lines []string) []launchIssue {
	var issues []launchIssue
	seen := map[string]bool{}
	add := func(level gameDiagLevel, title, detail string) {
		if !seen[title] {
			seen[title] = true
			issues = append(issues, launchIssue{Level: int(level), Title: title, Detail: detail})
		}
	}
	for _, line := range lines {
		low := strings.ToLower(line)
		switch {
		case strings.Contains(low, "could not find any library for sdl2"):
			add(gameDiagSure, "PortMaster's Python could not find SDL2",
				"pysdl2 looked where PYSDL2_DLL_PATH pointed and found no SDL2 there: "+line+
					". On JukaHub's Python a PYSDL2_DLL_PATH without SDL2 is now corrected as Python starts; open PortMaster again.")
		case strings.Contains(low, "syntax error") && (strings.Contains(line, `unexpected "("`) || strings.Contains(low, "bad substitution")):
			add(gameDiagLikely, "A PortMaster script ran under BusyBox's sh",
				line+". JukaHub now gives PortMaster's launches a real /bin/bash in their own mount namespace; if this stays, the launch log's first lines say why that did not work.")
		case strings.Contains(low, "python3") && (strings.Contains(low, "not found") || strings.Contains(low, "no such file")):
			add(gameDiagLikely, "PortMaster could not start Python 3", line)
		case strings.Contains(low, "importerror") || strings.Contains(low, "modulenotfounderror"):
			add(gameDiagLikely, "PortMaster's Python is missing a module", line)
		case strings.Contains(low, "keyerror") && strings.Contains(low, "primary_arch"):
			// ArkOS #1281: pugwash dies at start when its device check
			// (device_info.txt) gave no CPU architecture, with the splash up.
			add(gameDiagLikely, "PortMaster could not tell what handheld this is",
				line+". Its device check (device_info.txt) gave no CPU architecture; that check runs under bash, so the launch log's first lines say which bash it got.")
		case strings.Contains(low, "kmsdrm not available") || strings.Contains(low, "no available video device") ||
			strings.Contains(low, "couldn't initialize video") || strings.Contains(low, "could not initialize egl"):
			add(gameDiagLikely, "PortMaster's menu could not open the screen",
				line+". Another program had the screen — on a DRM display that is PortMaster's own splash (REG-Linux PortMaster-GUI #2).")
		}
	}
	return issues
}
