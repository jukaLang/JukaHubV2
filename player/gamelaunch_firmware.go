package main

// gamelaunch_firmware.go — a firmware launch script started the way MainUI
// starts it, and what the script says when it stops.
//
// The ROM argument. MainUI hands a system's launch script the game as the
// emulator folder joined to its config.json "rompath":
// /mnt/SDCARD/Emus/GBA/../../Roms/GBA/<file>. JukaHub handed it the clean
// path, /mnt/SDCARD/Roms/GBA/<file>. The stock scripts do not care, but
// CrossMix's RetroArch launchers (every libretro system since CrossMix 1.2.1)
// source System/usr/trimui/scripts/common_launcher.sh, which works out the
// emulator and ROM folders from those arguments, and a CrossMix tester saw
// every RetroArch game from JukaHub stop with RetroArch "not found" while PSP,
// whose PPSSPP script does not use the common launcher, played. A script in a
// <card>/Emus/<system> folder now gets a game under the same card's Roms
// folder in MainUI's form. It names the same file, so nothing that opens it
// can tell the difference; only a script that reads the path's text can.
//
// The script's own words. A library launch had no stdout or stderr, so a
// line like "launch.sh: line 9: /ra64.trimui: not found" went to /dev/null
// and the launch card could only say the game closed. A shell launcher's
// output now goes to gamediag/launcher.log (emptied each launch), and when a
// game closes within seconds the card names what the script could not find
// and shows its last lines when RetroArch wrote none (gamediag.go, exited).
//
// The interpreter. MainUI's queued command runs the launch script itself, so
// the kernel starts what its "#!" line names: a "#!/bin/bash" script runs
// under bash, and a launcher that is a program runs as that program. JukaHub
// read every one with sh, where a bash script's arrays and [[ ]] fail and a
// program is read as text. firmwareScriptCommand now starts the interpreter
// the line names when it is not sh and is on the device; a plain sh script,
// a script with no such line and an interpreter the device lacks keep sh.

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// launcherLogName is the launch script's output, in gamediag/.
	launcherLogName = "launcher.log"
	// launcherLogCap bounds that file during a long session: a RetroArch
	// started with -v whose log could not be moved to a file writes its whole
	// session to stderr.
	launcherLogCap = 1 << 20
	// launcherLogCheckEvery is how often the size is checked.
	launcherLogCheckEvery = 30 * time.Second
	// launcherExcerptMax is how many of the script's lines the card shows.
	launcherExcerptMax = 8
)

// firmwareROMArg is the ROM path a launch script is given: MainUI's form,
// <card>/Emus/<system>/../../Roms/..., when launcher is a shell script in a
// <card>/Emus/<system> folder and romPath is under the same card's Roms
// folder; romPath unchanged otherwise (another card, a desktop emulator, a
// relative path). Slash paths only: the handheld's.
func firmwareROMArg(launcher, romPath string) string {
	l := filepath.ToSlash(strings.TrimSpace(launcher))
	r := filepath.ToSlash(strings.TrimSpace(romPath))
	if ext := strings.ToLower(path.Ext(l)); ext != ".sh" && ext != "" {
		return romPath
	}
	if !path.IsAbs(l) || !path.IsAbs(r) {
		return romPath
	}
	l, r = path.Clean(l), path.Clean(r)
	emuDir := path.Dir(l)
	emus := path.Dir(emuDir)
	if emus == emuDir || !strings.EqualFold(path.Base(emus), "Emus") {
		return romPath
	}
	card := path.Dir(emus)
	prefix := card + "/"
	if card == "/" {
		prefix = "/"
	}
	rel, ok := strings.CutPrefix(r, prefix)
	if !ok {
		return romPath
	}
	top, rest, ok := strings.Cut(rel, "/")
	if !ok || rest == "" || !strings.EqualFold(top, "Roms") {
		return romPath
	}
	return emuDir + "/../../" + rel
}

// launcherShellScript reports whether launcher is a script JukaHub starts
// with sh (gameLaunchCommandArgs): the ones whose output is kept.
func launcherShellScript(launcher string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(launcher))) {
	case ".sh", "":
		return strings.TrimSpace(launcher) != ""
	}
	return false
}

// launcherOutput is one launch's launcher.log while the game runs.
type launcherOutput struct {
	file *os.File
	path string
	done chan struct{}
}

// captureLauncherOutput points a shell launcher's stdout and stderr at
// gamediag/launcher.log, emptied first, and records the path on d for the
// diagnosis. The child is given the file itself, not a pipe, so cmd.Wait
// never waits on a helper the script left running with the file open. nil
// (and every method a no-op) when there is nothing to capture.
func (d *gameDiagLaunch) captureLauncherOutput(cmd *exec.Cmd) *launcherOutput {
	if d == nil || cmd == nil || IsWindows() || !launcherShellScript(d.launcher) ||
		cmd.Stdout != nil || cmd.Stderr != nil {
		return nil
	}
	dir := gameDiagLogDir()
	if dir == "" || os.MkdirAll(dir, 0o755) != nil {
		return nil
	}
	p := filepath.Join(dir, launcherLogName)
	// O_APPEND: the size watch can empty the file under a running game and
	// the child's next write still lands at the new end.
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0o644)
	if err != nil {
		logWarn("gamediag", "cannot keep the launch script's output in %s: %v", p, err)
		return nil
	}
	cmd.Stdout = f
	cmd.Stderr = f
	d.outPath = p
	return &launcherOutput{file: f, path: p, done: make(chan struct{})}
}

// started is called once cmd.Start has returned, either way: the child has
// its own copy of the file, so JukaHub's is closed, and a started game's log
// is kept under launcherLogCap until stopped is called.
func (o *launcherOutput) started(ok bool) {
	if o == nil {
		return
	}
	_ = o.file.Close()
	if ok {
		go o.watch()
	}
}

// stopped ends the size watch once the game's process group is gone.
func (o *launcherOutput) stopped() {
	if o == nil {
		return
	}
	select {
	case <-o.done:
	default:
		close(o.done)
	}
}

func (o *launcherOutput) watch() {
	t := time.NewTicker(launcherLogCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-o.done:
			return
		case <-t.C:
			if st, err := os.Stat(o.path); err == nil && st.Size() > launcherLogCap {
				_ = os.Truncate(o.path, 0)
			}
		}
	}
}

// launcherMissingRe finds what a shell could not run, open or enter:
// busybox ash's "x: not found", "can't open 'x'", "can't cd to x" and the
// kernel's "x: No such file or directory".
var launcherMissingRe = regexp.MustCompile(`(?i)(?:can't (?:open|cd to) '?([^'\s:]+)'?|([^\s:'"]+): (?:not found|no such file or directory))`)

// launcherOutputFindings reads a launch script's output: an issue for what
// it could not find (RetroArch when that is among them, else the first),
// and its last lines for the card. RetroArch's own log lines ("[ERROR] …",
// on stderr when its log could not go to a file) are shown but not read as
// the shell's: their "content: No such file" is not a missing program.
func launcherOutputFindings(lines []string, launcher string) (issues []launchIssue, excerpt []string) {
	script := filepath.Base(strings.TrimSpace(launcher))
	missing, missingLine := "", ""
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > 220 {
			line = string(r[:220]) + "…"
		}
		excerpt = append(excerpt, line)
		if strings.HasPrefix(line, "[") || (missing != "" && launcherRetroArchName(missing)) {
			continue
		}
		m := launcherMissingRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		found := m[1]
		if found == "" {
			found = m[2]
		}
		if missing == "" || launcherRetroArchName(found) {
			missing, missingLine = found, line
		}
	}
	if len(excerpt) > launcherExcerptMax {
		excerpt = excerpt[len(excerpt)-launcherExcerptMax:]
	}
	if missing == "" {
		return nil, excerpt
	}
	title := "The launch script could not find " + path.Base(missing)
	if launcherRetroArchName(missing) {
		title = "The launch script could not find RetroArch"
	}
	detail := script + " stopped with: " + missingLine
	if strings.HasPrefix(missing, "/") && strings.Count(missing, "/") == 1 {
		// "/ra64.trimui": the folder variable in front of it was empty.
		detail += " — the folder in front of it is empty, so the script did not get a setting it expected."
	}
	issues = append(issues, launchIssue{
		Level:  int(gameDiagSure),
		Title:  title,
		Files:  []string{missing},
		Where:  filepath.Dir(launcher),
		Detail: detail,
	})
	return issues, excerpt
}

// launcherRetroArchName reports whether a path names a RetroArch binary.
func launcherRetroArchName(p string) bool {
	base := strings.ToLower(path.Base(filepath.ToSlash(p)))
	switch base {
	case "retroarch", "retroarch64":
		return true
	}
	return strings.HasPrefix(base, "ra64.") || strings.HasPrefix(base, "ra32.")
}

// addLauncherOutput adds the launch script's findings to a quick-exit report,
// and its last lines when RetroArch's log gave none.
func (d *gameDiagLaunch) addLauncherOutput(report *gameDiagReport) {
	if d == nil || report == nil || d.outPath == "" {
		return
	}
	lines := gameDiagReadTail(d.outPath, time.Time{})
	if len(lines) == 0 {
		return
	}
	issues, excerpt := launcherOutputFindings(lines, d.launcher)
	if len(issues) > 0 {
		report.Issues = append(issues, report.Issues...)
	}
	if len(report.LogLines) == 0 && len(excerpt) > 0 {
		report.LogLines = excerpt
		report.LogPath = d.outPath
	}
}

// firmwareScriptHeadMax bounds the read of a launcher's first line.
const firmwareScriptHeadMax = 256

// firmwareScriptCommand is the command MainUI's queued line would run for a
// launcher gameLaunchCommandArgs starts as `sh <launcher> …`: the interpreter
// the script's "#!" line names, with the one argument the kernel would pass it,
// when that is not sh and is on the device; the launcher itself when it is a
// program (ELF). A plain sh script, a script with no "#!" line, an interpreter
// the device lacks and a file that cannot be read keep name and args as they
// are — sh then starts more scripts than MainUI would. Handheld only.
func firmwareScriptCommand(launcher, name string, args []string) (string, []string) {
	if name != "sh" || len(args) == 0 || args[0] != launcher {
		return name, args
	}
	head := firmwareScriptHead(launcher)
	if bytes.HasPrefix(head, []byte("\x7fELF")) {
		return launcher, args[1:]
	}
	interp, arg, ok := parseShebang(head)
	if !ok {
		return name, args
	}
	if path.Base(interp) == "env" {
		// "#!/usr/bin/env bash": the program is looked up on PATH. env's own
		// options (env -S …) are not followed.
		words := strings.Fields(arg)
		if len(words) == 0 || strings.HasPrefix(words[0], "-") || words[0] == "sh" {
			return name, args
		}
		found, err := exec.LookPath(words[0])
		if err != nil {
			return name, args
		}
		return found, args
	}
	if path.Base(interp) == "sh" || !path.IsAbs(interp) || !firmwareExecutable(interp) {
		return name, args
	}
	if arg != "" {
		return interp, append([]string{arg}, args...)
	}
	return interp, args
}

// firmwareScriptHead is the start of a launcher, nil when it cannot be read.
func firmwareScriptHead(p string) []byte {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, firmwareScriptHeadMax)
	n, _ := io.ReadFull(f, buf)
	return buf[:n]
}

// parseShebang reads a "#!" line as the kernel does: the interpreter is the
// first word after "#!" and the rest of the line, trimmed, is one argument.
// The CR a Windows editor leaves before the newline is dropped.
func parseShebang(head []byte) (interp, arg string, ok bool) {
	rest, found := bytes.CutPrefix(head, []byte("#!"))
	if !found {
		return "", "", false
	}
	if i := bytes.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[:i]
	}
	line := strings.TrimSpace(string(rest))
	if line == "" {
		return "", "", false
	}
	i := strings.IndexAny(line, " \t")
	if i < 0 {
		return line, "", true
	}
	return line[:i], strings.TrimSpace(line[i+1:]), true
}

// firmwareExecutable reports whether p is a regular file with an execute bit.
func firmwareExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}
