package main

// gamediag_crash.go — the card's answer when a fatal signal stopped the
// emulator itself, and nothing JukaHub can check is missing.
//
// Two reports from one Smart Pro (JH-N8NGPX, JH-RW63TU) ended the same way:
// 塞爾達傳說 時之笛 quit after six seconds, once through Emus/N64/default.sh and
// once through Emus/N64/launch.sh with mupen64plus_libretro.so, both with exit
// status 132 — signal 4, SIGILL — and both cards said the same thing: "JukaHub
// found no missing BIOS, core or game file", with no finding at all. The
// emulator's own words held the answer: "mupen64plus: Starting R4300 emulator:
// Dynamic Recompiler / Init new dynarec", then "Illegal instruction". A dynamic
// recompiler writes machine code for the CPU as the game runs; an illegal
// instruction the moment it starts is what a build carrying instructions this
// CPU does not have does, and no game file, BIOS or missing library explains it.
//
// The diagnosis could not say any of that before. Its findings are about files:
// a ROM that is gone, a core that will not load, a BIOS in the wrong folder. A
// launch that ended by a signal with all of those in place produced an empty
// list, and the card fell through to "nothing is missing" — true, and useless
// to a player staring at a game that will not start.
//
// So a library launch JukaHub did not stop, ended by SIGILL, SIGABRT, SIGBUS,
// SIGFPE or SIGSEGV within the quick-exit window, now gets one finding that says
// the emulator's own program ended, names the signal and, when the log shows a
// recompiler starting, says that this build cannot run on this device. The
// finding is gameDiagSure: it is not a guess about a file, it is what the
// process did. Its title carries one of gameDiagCoreIssueTitle's marks
// (gamediag.go), so a core build JukaHub just installed that ends this way is
// still put back by its probation (corebuilds_probation.go) instead of the
// finding counting as something else that explains the exit.

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// gameDiagFatalSignal reports whether a signal is a program's own crash: the
// ones gameDiagSignalNote (gamelaunch_watch.go) and gameDiagCoreOutcome
// (gamediag.go) already read as "it crashed". SIGKILL is not one of them — the
// system sends it, most often for memory, and the card has its own words for
// that. SIGTERM is a stop, not a crash.
func gameDiagFatalSignal(sig int) bool {
	switch sig {
	case 4, 6, 7, 8, 11:
		return true
	}
	return false
}

// gameDiagCrashSignalName names a signal the way the card's title does:
// "SIGILL" for 4, "signal 12" for one Linux's list here has no name for.
func gameDiagCrashSignalName(sig int) string {
	if name := gameDiagSignalName(sig); strings.HasPrefix(name, "SIG") {
		return name
	}
	return "signal " + strconv.Itoa(sig)
}

// gameDiagCrashDynarecWords are the lines a core prints as it starts
// recompiling the game's code for this CPU: mupen64plus's, whose "Init new
// dynarec" is the last line either N64 report held before the illegal
// instruction. A build that stops here never drew a game's first frame.
var gameDiagCrashDynarecWords = []string{
	"init new dynarec",
	"starting r4300 emulator: dynamic recompiler",
}

// gameDiagCrashCore is the core the launch ran, for the finding's words: the
// one its log names, else the one core a launch script names, else "".
func (d *gameDiagLaunch) gameDiagCrashCore() string {
	if core, _ := d.coreUsed(); core != "" {
		return core
	}
	return gameCoreFromLauncher(d.launcher, d.game.System)
}

// gameDiagCrashLines is what the launch wrote: the RetroArch log, and the
// launcher's own output, which is where a standalone emulator's words land
// (JH-N8NGPX had no retroarch.log at all: default.sh ran the firmware's
// Mupen64Plus itself).
func (d *gameDiagLaunch) gameDiagCrashLines() []string {
	var lines []string
	for _, path := range []string{d.logPath, d.outPath} {
		if strings.TrimSpace(path) == "" {
			continue
		}
		lines = append(lines, gameDiagReadTail(path, time.Time{})...)
	}
	return lines
}

// gameDiagCrashDynarec reports whether lines show a core's dynamic recompiler
// starting.
func gameDiagCrashDynarec(lines []string) bool {
	for _, line := range lines {
		lower := strings.ToLower(line)
		for _, word := range gameDiagCrashDynarecWords {
			if strings.Contains(lower, word) {
				return true
			}
		}
	}
	return false
}

// gameDiagCrashIssue is the finding for a launch a fatal signal ended: what
// stopped, and — when the log shows a recompiler starting under SIGILL — why
// nothing on the card makes that build run. ok is false when sig is not one of
// gameDiagFatalSignal's, so a caller adds nothing.
func (d *gameDiagLaunch) gameDiagCrashIssue(sig int) (launchIssue, bool) {
	if d == nil || !gameDiagFatalSignal(sig) {
		return launchIssue{}, false
	}
	name := gameDiagCrashSignalName(sig)
	core := d.gameDiagCrashCore()
	what := "The emulator"
	if core != "" {
		what = "The " + coreConfigDisplayName(core) + " core"
	}
	issue := launchIssue{
		Level: int(gameDiagSure),
		Title: what + " crashed (signal " + strconv.Itoa(sig) + ", " + name + ")",
	}
	if sig == 4 && gameDiagCrashDynarec(d.gameDiagCrashLines()) {
		// A recompiler that traps on the code it writes: the build is the
		// fault, and nothing on the card is.
		console := coreConsoleShort(core)
		if console == "" {
			console = d.game.System
		}
		game := "this game"
		if console != "" {
			game = "this " + console + " game"
		}
		issue.Detail = "It started its dynamic recompiler while opening " + game + " — the part that turns the game's " +
			"code into this device's instructions — and the CPU then refused one of them. That is what a build made " +
			"for another CPU does, so no game runs in this build, and the game file is not what stopped it. Start this " +
			"game in another emulator (Game options → Emulator), or put a build of this core made for this device on " +
			"the card (Emulators → Core builds)."
		return issue, true
	}
	issue.Detail = "The emulator's own program ended on this signal after it started; nothing JukaHub checks — the game " +
		"file, the emulator's files, the BIOS — is what stopped it. Try this game in another emulator (Game options → " +
		"Emulator). If every emulator on the card ends the same way, a build made for this device is what fixes it."
	return issue, true
}

// gameDiagCrashAdd adds the finding to a quick exit's report before the card
// reads it (gamediag.go, exited): a crash the player can act on must not be
// reported as "nothing is missing". Issues stay sorted by level, so a certain
// missing file still comes first.
func (d *gameDiagLaunch) gameDiagCrashAdd(report *gameDiagReport, sig int) bool {
	issue, ok := d.gameDiagCrashIssue(sig)
	if !ok || report == nil {
		return false
	}
	report.Issues = append(report.Issues, issue)
	sort.SliceStable(report.Issues, func(i, j int) bool { return report.Issues[i].Level < report.Issues[j].Level })
	return true
}
