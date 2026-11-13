package main

// gamelaunch_watch.go — what JukaHub writes down about each library launch,
// and how a stop JukaHub asked for is told apart from a game that ended by
// itself.
//
// A TrimUI Brick Pro on CrossMix answered every game with the "Playing …"
// toast and then the game page again: no game, no card, and not a line in
// errors.txt. The launch script had ended within a second or two with exit
// status 0 — or 137, the status a shell reports for a program the system
// killed — and the quick-exit check (gamediag.go, exited) read both as the
// player quitting. Now:
//
//   - JukaHub's log gets one line when a library game starts (the command, its
//     folder, the pid and the memory free) and one when it ends (how long it
//     ran, its exit status or signal), so errors.txt says what happened;
//   - every stop JukaHub sends a game — the in-game menu's Quit, quitting
//     JukaHub, the low-battery save, a group kill after a grace — is recorded
//     against the game's process (gameLaunchNoteStop, called from
//     screenChildSignal, gmQuitGame and protectLowBattery);
//   - so the quick-exit card also opens on the handheld for a game that was
//     over within gameDiagBriefRun and that JukaHub did not stop, whatever its
//     status, and for one ended by a signal JukaHub did not send, saying which
//     signal and how much memory was free when it started.

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// gameDiagBriefRun is how short a run reads as "it never got going": a
	// launch script that gave up, an emulator that could not open the screen
	// or the game. Nobody plays a game for less; quitting one that fast goes
	// through the in-game menu, which JukaHub records.
	gameDiagBriefRun = 3 * time.Second
	// gameDiagLowMemMB is the free memory, in MB, below which the card says how
	// much there was when the game started.
	gameDiagLowMemMB = 160
	// gameLaunchLogArgsMax bounds the command line in the log, in runes.
	gameLaunchLogArgsMax = 400
)

// gameLaunchWatch is every library game process that is running, and whether
// JukaHub has asked it to stop.
var gameLaunchWatch struct {
	mu   sync.Mutex
	cmds map[*exec.Cmd]bool
}

// gameLaunchWatchStart registers a library game's process once it started.
func gameLaunchWatchStart(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	gameLaunchWatch.mu.Lock()
	if gameLaunchWatch.cmds == nil {
		gameLaunchWatch.cmds = make(map[*exec.Cmd]bool, 1)
	}
	gameLaunchWatch.cmds[cmd] = false
	gameLaunchWatch.mu.Unlock()
}

// gameLaunchNoteStop records that JukaHub asked cmd to stop. Any process may be
// passed (screenChildSignal also stops downloads and helpers): only a watched
// game is marked.
func gameLaunchNoteStop(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	gameLaunchWatch.mu.Lock()
	if _, ok := gameLaunchWatch.cmds[cmd]; ok {
		gameLaunchWatch.cmds[cmd] = true
	}
	gameLaunchWatch.mu.Unlock()
}

// gameLaunchNoteStopAll marks every running library game as stopped by
// JukaHub, for a stop that does not go through its process (RetroArch's own
// QUIT command).
func gameLaunchNoteStopAll() {
	gameLaunchWatch.mu.Lock()
	for cmd := range gameLaunchWatch.cmds {
		gameLaunchWatch.cmds[cmd] = true
	}
	gameLaunchWatch.mu.Unlock()
}

// gameLaunchWatchEnd forgets a game whose process group is gone and reports
// whether JukaHub had asked it to stop.
func gameLaunchWatchEnd(cmd *exec.Cmd) bool {
	if cmd == nil {
		return false
	}
	gameLaunchWatch.mu.Lock()
	stopped := gameLaunchWatch.cmds[cmd]
	delete(gameLaunchWatch.cmds, cmd)
	gameLaunchWatch.mu.Unlock()
	return stopped
}

// noteEnd records, on the reaper goroutine before exited, when the game's
// process group was gone and whether JukaHub had asked it to stop.
func (d *gameDiagLaunch) noteEnd(at time.Time, stopped bool) {
	if d == nil {
		return
	}
	d.ended = at
	d.stopped = stopped
}

// memFreeMB is the memory that was free when the game started, in MB; 0 when
// unknown.
func (d *gameDiagLaunch) memFreeMB() int {
	if d == nil {
		return 0
	}
	return d.memMB
}

// gameLaunchMemFreeMB is the memory the kernel reports as available
// (MemAvailable), in MB; 0 where /proc/meminfo is not there (Windows).
func gameLaunchMemFreeMB() int {
	return int(readProcMeminfo().MemAvail / 1024)
}

// gameLaunchLogStart is the start line, for example: starting "Manhunt 2"
// (PlayStation Portable): sh /mnt/SDCARD/Emus/PSP/launch.sh "/mnt/…" in
// /mnt/SDCARD/Emus/PSP, pid 812, 214 MB free.
func gameLaunchLogStart(game libraryGame, cmd *exec.Cmd, freeMB int) {
	if cmd == nil {
		return
	}
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	free := ""
	if freeMB > 0 {
		free = ", " + strconv.Itoa(freeMB) + " MB free"
	}
	logInfo("library", "starting %s (%s): %s in %s, pid %d%s",
		strconv.Quote(game.Label()), game.System, gameLaunchCommandLine(cmd.Args), cmd.Dir, pid, free)
}

// gameLaunchLogEnd is the end line: how long the game ran and how it ended.
func gameLaunchLogEnd(game libraryGame, waitErr error, ran time.Duration, stopped bool) {
	how := gameLaunchEndWords(waitErr)
	if stopped {
		how += "; JukaHub had asked it to stop"
	}
	logInfo("library", "%s ended after %s: %s", strconv.Quote(game.Label()), ran.Round(100*time.Millisecond), how)
}

// gameLaunchCommandLine is a command for the log: arguments with spaces or
// quotes are quoted, and a very long line is cut.
func gameLaunchCommandLine(args []string) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\"'\\$`") {
			a = strconv.Quote(a)
		}
		parts = append(parts, a)
	}
	line := strings.Join(parts, " ")
	if r := []rune(line); len(r) > gameLaunchLogArgsMax {
		line = string(r[:gameLaunchLogArgsMax]) + "…"
	}
	return line
}

// gameLaunchExitStatus is how a launcher ended: its exit code (-1 when a
// signal ended it, or the status is unknown) and the signal that ended it — the
// launcher itself, or on the handheld, by the shell's 128+N convention, the
// program the script ran last (0 for none).
func gameLaunchExitStatus(err error) (code, sig int) {
	if err == nil {
		return 0, 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return -1, 0
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -1, int(ws.Signal())
	}
	code = exitErr.ExitCode()
	if !IsWindows() && code > 128 && code <= 128+64 {
		sig = code - 128
	}
	return code, sig
}

// gameLaunchEndWords is the end line's account of a launcher's status.
func gameLaunchEndWords(err error) string {
	code, sig := gameLaunchExitStatus(err)
	switch {
	case sig > 0 && code < 0:
		return "ended by signal " + strconv.Itoa(sig) + " (" + gameDiagSignalName(sig) + ")"
	case sig > 0:
		return "exit status " + strconv.Itoa(code) + " (what it ran was ended by signal " +
			strconv.Itoa(sig) + ", " + gameDiagSignalName(sig) + ")"
	case code < 0:
		return "status unknown (" + firstLine(err.Error()) + ")"
	}
	return "exit status " + strconv.Itoa(code)
}

// gameDiagSignalName is a signal's usual name. The numbers are Linux's, which
// is where a signal reaches this (the handheld).
func gameDiagSignalName(sig int) string {
	switch sig {
	case 1:
		return "SIGHUP"
	case 2:
		return "SIGINT"
	case 4:
		return "SIGILL"
	case 6:
		return "SIGABRT"
	case 7:
		return "SIGBUS"
	case 8:
		return "SIGFPE"
	case 9:
		return "SIGKILL"
	case 11:
		return "SIGSEGV"
	case 13:
		return "SIGPIPE"
	case 15:
		return "SIGTERM"
	}
	return "signal " + strconv.Itoa(sig)
}

// gameDiagSignalNote is the card's words for a signal that ended a game which
// JukaHub had not asked to stop.
func gameDiagSignalNote(sig int) string {
	n := strconv.Itoa(sig)
	switch sig {
	case 9:
		return "the system stopped it (signal 9, SIGKILL); on a handheld that is almost always memory running out"
	case 4, 7, 8, 11:
		return "it crashed (signal " + n + ", " + gameDiagSignalName(sig) + ")"
	case 6:
		return "it gave up on itself (signal 6, SIGABRT)"
	}
	return "something other than JukaHub stopped it (signal " + n + ", " + gameDiagSignalName(sig) + ")"
}
