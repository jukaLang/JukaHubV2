package main

// gamediag_port.go — what a PortMaster port printed, read for why it stopped
// at once.
//
// On a TrimUI Brick, Play on SuperTuxKart said "Launching SuperTuxKart" and
// the details page came straight back: no game and not a word. A port's exit
// status proves nothing (PortMaster scripts often end non-zero after a normal
// quit), and the quick-exit check spoke only when the port's log.txt had a
// line saying loading failed. A script that dies before it gets that far —
// under BusyBox's sh, sourcing a control.txt it cannot find, or on a program
// the firmware cannot load — writes nothing there.
//
// So a port JukaHub did not stop that is gone within gameDiagQuickExit always
// gets the card (gamediag.go, exited); one gone within gameDiagPortQuickExit,
// or a cloud-gaming client (GreenOvercast, which may be left at once on
// purpose), gets it when its own output names a cause. Both of its logs are
// read: JukaHub's data/gamediag/ports/<script>.log (portLogOpen: the launch
// notes, then everything the script printed, including what it printed
// before it redirected its output) and the port's own log.txt. Their lines
// are sorted into the causes seen on handheld firmware, the surest first:
//
//   - bash syntax refused by BusyBox's sh (the firmware's /bin/bash) — in a
//     PortMaster file the script sourced, or in a script a port started by its
//     own #!/bin/bash line (under a working bash only the latter counts, and
//     bash's own arithmetic errors never do);
//   - PortMaster's control.txt not found, or an empty PortMaster folder that
//     wins the script's search, or a file of PortMaster's own that
//     control.txt loads missing. An error control.txt reports about one of
//     its own lines proves it was found: PortMaster's TrimUI control.txt
//     sources TRIMUI_EX's ex_config and starts sdl2imgshow, which a card
//     without TRIMUI_EX lacks, and bash goes on past both (a Smart Pro S
//     report, JH-AT4EEE, blamed PortMaster for that);
//   - a script stopped by a variable nothing had set: with `set -u`
//     (nounset) bash ends the script at its first `$1` and says "$1: unbound
//     variable", and a port is started with no arguments — a Smart Pro
//     report, JH-ZBPUTA, was told only that JukaHub found nothing it could
//     check;
//   - the game's own data the script checks for itself and does not find
//     (gameDiagPortNeededFile: Half-Life prints "Missing game files" on the
//     screen, which no log holds, and exits — a Smart Pro report, JH-AGE2UH,
//     was told only that the firmware lacks lscpu);
//   - the program saying it could not read the sign-in it saved
//     (GreenOvercast, which then closes: greenovercast_store.go);
//   - a program built for a newer glibc or libstdc++, a library the handheld
//     lacks, or a program built for another processor (or missing);
//   - JukaHub's own bash (Ubuntu's, on the private C runtime) not starting;
//   - the graphics (EGL, OpenGL ES, SDL video) failing to start;
//   - a crash, the system stopping it for memory (not PortMaster's own key
//     mapper, which pm_finish stops as the port ends), a runtime (.squashfs)
//     that is missing or did not mount, a file it may not run;
//   - commands the script ran that are not on this firmware (a "maybe": some
//     are harmless).

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// gameDiagPortQuickExit is how soon after the launch a port that ended
	// by itself is diagnosed. Longer than a library game's: a port mounts its
	// runtime and loads its data before the first frame. Between
	// gameDiagQuickExit and this, the card opens only for a cause in its logs.
	gameDiagPortQuickExit = 20 * time.Second
	// portLogOutputMarker ends the header portLogOpen writes; the script's
	// output follows it.
	portLogOutputMarker = "# ---- the script's own output follows ----"
	// gameDiagPortIssueMax caps the findings on one card.
	gameDiagPortIssueMax = 4
)

var (
	// BusyBox ash and dash report bash syntax as `file: line 5: syntax error:
	// unexpected "("` and `file: 5: Syntax error: "(" unexpected`; bash's own
	// form is "syntax error near unexpected token", which does not match.
	gameDiagPortShRe = regexp.MustCompile(`(?i)syntax error: |bad substitution|(?:^|[\s:])(?:\[\[|declare|shopt|mapfile|readarray|pushd|popd|typeset|compgen): (?:command )?not found`)
	// gameDiagPortLineFileRe is the file a shell error names first.
	gameDiagPortLineFileRe = regexp.MustCompile(`^([^\s:]+):\s*(?:line\s+)?\d+:`)
	// The loader's "version `GLIBC_2.34' not found (required by ./game)".
	gameDiagPortGlibcRe = regexp.MustCompile("(GLIBC(?:XX)?_[0-9][0-9.]*)['`]? not found(?: \\(required by ([^)]+)\\))?")
	gameDiagPortLibRe   = regexp.MustCompile(`error while loading shared libraries: ([^:\s]+):`)
	// gameDiagPortCmdRe is `name: not found` (BusyBox) or `name: command not
	// found` (bash).
	gameDiagPortCmdRe = regexp.MustCompile(`(?:^|[\s:])([A-Za-z0-9_.+\-/]+): (?:command )?not found`)
	// gameDiagPortKilledRe is the shell's report of a program the system
	// killed ("line 40:  1234 Killed  ./game").
	gameDiagPortKilledRe = regexp.MustCompile(`(?:^|\s)Killed(?:\s|$)`)
	// gameDiagPortKilledKeyMapperRe is that report about PortMaster's own key
	// mapper, which its pm_finish stops as the port ends (the shell prints the
	// command as written: "$GPTOKEYB \"game.aarch64\" -c ./game.gptk"). A
	// port that crashed still cleans up, so that line says nothing about why
	// the game stopped (JH-7RKB39: a segfault, and the card chose memory).
	gameDiagPortKilledKeyMapperRe = regexp.MustCompile(`(?i)\bKilled\b.*gptokeyb\b`)
	// gameDiagPortNoProgRe is bash failing to start a program the script runs
	// from its folder ("line 38: ./game: No such file or directory"): the
	// file, or its loader (a 32-bit one), is not there.
	gameDiagPortNoProgRe = regexp.MustCompile(`line \d+: (\./[^\s:]+): No such file or directory`)
	// gameDiagPortUnsetRe is a shell's own report of a variable nothing had
	// set: bash's `script: line 5: $1: unbound variable` (the script runs
	// with `set -u`) and BusyBox's or dash's `script: 5: $1: parameter not
	// set`. Bash names a positional parameter with its `$` ($1) and any other
	// variable without one (XDG_DATA_HOME); the group is the name it used.
	gameDiagPortUnsetRe = regexp.MustCompile(`(?:^|[\s:])(\$[0-9@*#?$!-]+|\$?[A-Za-z_][A-Za-z0-9_]*): (?:unbound variable|parameter not set)`)
	// gameDiagPortMissingObjRe is a file's line that could not open
	// something: "control.txt: line 9: /x/ex_config: No such file or
	// directory" (the file, then the thing its line wanted).
	gameDiagPortMissingObjRe = regexp.MustCompile(`(?i)^([^\s:]+):\s*(?:line\s+)?\d+:\s*(.+?):\s*(?:no such file|cannot open|can't open)`)
)

// gameDiagPortBashOwnErrors are bash's own syntax errors that also say
// "syntax error:" — an arithmetic slip in a script, not BusyBox.
var gameDiagPortBashOwnErrors = []string{"operand expected", "arithmetic", "error token is"}

var (
	// gameDiagPortFailWords say something did not work.
	gameDiagPortFailWords = []string{"fail", "error", "could not", "couldn't", "cannot", "can't", "unable",
		"no available", "not available", "not supported", "unsupported", "invalid", "no such file", "not found"}
	// gameDiagPortOwnBashWords are the fail words plus the loader's own.
	gameDiagPortOwnBashWords = []string{"fail", "error", "could not", "cannot", "can't", "no such file",
		"not found", "permission denied", "exec format"}
	// gameDiagPortGfxWords name the screen and graphics start.
	gameDiagPortGfxWords = []string{"egl", "gles", "opengl", "glx", "gl context", "glcontext", "video device",
		"video mode", "video driver", "window", "kmsdrm", "framebuffer", "/dev/fb", "/dev/dri", "open display",
		"renderer", "sdl_init_video", "init video"}
	// gameDiagPortAudioWords keep a sound failure ("ALC: could not create
	// context") out of the graphics finding.
	gameDiagPortAudioWords = []string{"audio", "alsa", "openal", "alc_", "alc:", "sound", "pulse", "mixer"}
	// gameDiagPortCrashWords are a program's own fatal errors.
	gameDiagPortCrashWords = []string{"segmentation fault", "illegal instruction", "bus error", "aborted",
		"core dumped", "terminate called", "stack smashing", "double free", "fatal signal"}
	// gameDiagPortMemWords are memory running out.
	gameDiagPortMemWords = []string{"out of memory", "cannot allocate memory", "bad_alloc", "oom-killer"}
	// gameDiagPortExecWords are a program the kernel or shell would not run.
	gameDiagPortExecWords = []string{"exec format error", "cannot execute binary file", "cannot execute: required file not found"}
	// gameDiagPortHarmlessCmds are commands port scripts call that a TrimUI
	// lacks without any effect on the game (stock SDL's fbdev back end calls
	// setterm on every start; PortMaster's TrimUI control.txt shows its
	// splash with TRIMUI_EX's sdl2imgshow).
	gameDiagPortHarmlessCmds = map[string]bool{"setterm": true, "systemctl": true, "sudo": true, "dbus-send": true,
		"nmcli": true, "udevadm": true, "fbset": true, "loadkeys": true, "sdl2imgshow": true,
		// PortMaster's own device probe (device_info.txt) asks lscpu for the
		// CPU name; no port needs it.
		"lscpu": true}
	// gameDiagPortControlMissWords say a file could not be opened.
	gameDiagPortControlMissWords = []string{"no such file", "not found", "can't open", "cannot open"}
	// gameDiagPortStoreWords say a saved sign-in could not be used.
	gameDiagPortStoreWords = []string{"could not", "couldn't", "cannot", "can't", "unable", "failed", "unreadable", "corrupt"}
)

// gameDiagPortJukaLog splits JukaHub's log of a port launch into its notes
// ("# note:" lines) and the script's output. A log older than since (when
// set) is an earlier launch's. When the header was cut off (the log grew), the
// lines that are not header lines are the output.
func gameDiagPortJukaLog(path string, since time.Time) (notes, output []string) {
	if path == "" {
		return nil, nil
	}
	lines := gameDiagReadTail(path, since)
	in := false
	for _, line := range lines {
		if strings.HasPrefix(line, portLogEndPrefix) {
			continue // JukaHub's own end line (portLogNoteEnd)
		}
		if in {
			output = append(output, line)
			continue
		}
		if note, ok := strings.CutPrefix(line, "# note: "); ok {
			notes = append(notes, strings.TrimSpace(note))
		}
		if strings.TrimSpace(line) == portLogOutputMarker {
			in = true
		}
	}
	if !in {
		for _, line := range lines {
			if !strings.HasPrefix(line, "# ") {
				output = append(output, line)
			}
		}
	}
	return notes, output
}

// gameDiagPortMergeOutput adds the port's log.txt lines to the script output
// JukaHub caught; a port that tees its output into log.txt has printed them
// there already.
func gameDiagPortMergeOutput(caught, logTxt []string) []string {
	if len(caught) == 0 {
		return logTxt
	}
	seen := make(map[string]bool, len(caught))
	for _, line := range caught {
		seen[strings.TrimSpace(line)] = true
	}
	out := append([]string(nil), caught...)
	for _, line := range logTxt {
		if s := strings.TrimSpace(line); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, line)
		}
	}
	return out
}

// gameDiagPortLineFile is the file a shell error line names first ("" for a
// bare "sh:" or "bash:").
func gameDiagPortLineFile(line string) string {
	m := gameDiagPortLineFileRe.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	switch base := filepath.Base(m[1]); base {
	case "sh", "bash", "ash", "dash", "-sh":
		return ""
	}
	return m[1]
}

// gameDiagPortProgramName reports whether a "<name>: not found" names a
// program or command (no extension, or a build suffix such as .aarch64) rather
// than a data file.
func gameDiagPortProgramName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case "", ".aarch64", ".arm64", ".armhf", ".arm", ".bin", ".elf", ".sh", ".run", ".x86_64":
		return true
	}
	return false
}

// gameDiagPortFound is one cause found in a port's output: the issue and the
// output line that shows it ("" for one from the launch notes).
type gameDiagPortFound struct {
	issue launchIssue
	line  string
}

// gameDiagPortBashMissing reports a TrimUI where no full bash can be given to
// a port right now: the firmware has none and JukaHub's is not there yet.
func gameDiagPortBashMissing() bool {
	if !pmRuntimeApplies() {
		return false
	}
	c := pmRuntimeSnapshot()
	return !c.BashOK && c.BashGood == "" && !privateGlibcBashReady()
}

// gameDiagPortLogFindings sorts a port's output (and JukaHub's launch notes)
// into the causes listed at the top of this file. It returns the issues, the
// surest first and at most gameDiagPortIssueMax, the output lines that show
// them, and whether a cause stronger than a "maybe" rests on an output line.
// Off the main thread: it reads the PortMaster runtime state, and the glibc
// finding reads the firmware's libc.
func gameDiagPortLogFindings(notes, lines []string, script string) ([]launchIssue, []string, bool) {
	// The bash the launch used, from its notes: a "syntax error:" under a
	// working bash is the script's own, not BusyBox's.
	goodBash := false
	for _, n := range notes {
		if strings.HasPrefix(n, "bash: the firmware's") || strings.HasPrefix(n, "bash: JukaHub's") {
			goodBash = true
		}
	}
	// The first line of each cause, and what the lines name.
	var sh, control, pmBroken, store, glibc, lib, execLine, ownBash, gfx, crash, mem, rt, perm, cmd string
	var unset, unsetVar string
	var shFile, glibcBy string
	var glibcNeeds, libs, progs, cmds []string
	addOnce := func(list []string, s string) []string {
		for _, have := range list {
			if strings.EqualFold(have, s) {
				return list
			}
		}
		return append(list, s)
	}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || gameDiagPortHarmless(line) {
			continue
		}
		lower := strings.ToLower(line)
		missing, broken := gameDiagPortControlLine(line, lower)
		switch {
		case gameDiagPortShRe.MatchString(line) && !gameDiagHasAny(lower, gameDiagPortBashOwnErrors):
			file := gameDiagPortLineFile(line)
			// Under a working bash only a script started by its own
			// #!/bin/bash line (not the port's script, not a sourced *.txt)
			// can have met BusyBox.
			if goodBash && !gameDiagPortNestedScript(file, script) {
				continue
			}
			if sh == "" {
				sh, shFile = line, file
			}
		case missing:
			if control == "" {
				control = line
			}
		case broken:
			if pmBroken == "" {
				pmBroken = line
			}
		case gameDiagPortUnsetRe.MatchString(line):
			if unset == "" {
				unset, unsetVar = line, gameDiagPortUnsetRe.FindStringSubmatch(line)[1]
			}
		case strings.Contains(lower, "credential store") && gameDiagHasAny(lower, gameDiagPortStoreWords):
			if store == "" {
				store = line
			}
		case gameDiagPortGlibcRe.MatchString(line):
			m := gameDiagPortGlibcRe.FindStringSubmatch(line)
			glibcNeeds = addOnce(glibcNeeds, m[1])
			if glibc == "" {
				glibc = line
				if m[2] != "" {
					glibcBy = filepath.Base(strings.TrimSpace(m[2]))
				}
			}
		case gameDiagPortLibRe.MatchString(line):
			libs = addOnce(libs, gameDiagPortLibRe.FindStringSubmatch(line)[1])
			if lib == "" {
				lib = line
			}
		case strings.Contains(lower, "private-glibc") && strings.Contains(lower, "bash") &&
			gameDiagHasAny(lower, gameDiagPortOwnBashWords):
			if ownBash == "" {
				ownBash = line
			}
		case gameDiagHasAny(lower, gameDiagPortExecWords) || gameDiagPortNoProgRe.MatchString(line):
			if m := gameDiagPortNoProgRe.FindStringSubmatch(line); m != nil {
				progs = addOnce(progs, filepath.Base(m[1]))
			}
			if execLine == "" {
				execLine = line
			}
		case gameDiagHasAny(lower, gameDiagPortCrashWords):
			if crash == "" {
				crash = line
			}
		case gameDiagHasAny(lower, gameDiagPortMemWords) || gameDiagPortKilledRe.MatchString(line):
			// PortMaster's key mapper stopped by its own pm_finish is no
			// cause: the game was already over.
			if mem == "" && !gameDiagPortKilledKeyMapperRe.MatchString(line) {
				mem = line
			}
		case strings.Contains(lower, "squashfs") && gameDiagHasAny(lower, gameDiagPortFailWords),
			strings.Contains(lower, "runtime") && gameDiagHasAny(lower, []string{"missing", "not found", "no such file"}):
			if rt == "" {
				rt = line
			}
		case gameDiagHasAny(lower, gameDiagPortGfxWords) && gameDiagHasAny(lower, gameDiagPortFailWords) &&
			!gameDiagHasAny(lower, gameDiagPortAudioWords):
			if gfx == "" {
				gfx = line
			}
		case strings.Contains(lower, "permission denied") && !gameDiagHasAny(lower, []string{"/sys/", "/proc/", "/dev/"}):
			if perm == "" {
				perm = line
			}
		default:
			for _, m := range gameDiagPortCmdRe.FindAllStringSubmatch(line, -1) {
				name := m[1]
				if !gameDiagPortProgramName(name) {
					// "data/level1.pak: not found" is a data file, left to
					// diagnosePort's missing-files finding.
					continue
				}
				if strings.Contains(name, "/") {
					// BusyBox says "./game: not found" for a program that is
					// missing or whose loader (a 32-bit one) is.
					progs = addOnce(progs, filepath.Base(name))
					if execLine == "" {
						execLine = line
					}
					continue
				}
				if gameDiagPortHarmlessCmds[strings.ToLower(name)] || strings.HasPrefix(name, "GLIBC") {
					continue
				}
				cmds = addOnce(cmds, name)
				if cmd == "" {
					cmd = line
				}
			}
		}
	}
	// The launch notes say what JukaHub found before the script ran. A note
	// read later (Why didn't it start?) may be days old: the bash one counts
	// only while no bash can be given now.
	noBash, emptyControl, controlNote := "", "", false
	for _, n := range notes {
		switch {
		case strings.Contains(n, "no PortMaster control.txt found"):
			controlNote = true
		case strings.Contains(n, "exists without control.txt"):
			emptyControl = n
		case strings.HasPrefix(n, "bash: none usable"):
			if gameDiagPortBashMissing() {
				noBash = strings.TrimSpace(strings.TrimPrefix(n, "bash: none usable"))
			}
		}
	}

	var found []gameDiagPortFound
	add := func(level gameDiagLevel, title, detail, line string, files []string) {
		found = append(found, gameDiagPortFound{issue: launchIssue{Level: int(level), Title: title, Files: files, Detail: detail}, line: line})
	}
	if sh != "" {
		add(gameDiagSure, "It ran under BusyBox's sh, not bash", gameDiagPortShDetail(shFile, script), sh, nil)
	} else if noBash != "" {
		why := strings.TrimSpace(strings.TrimPrefix(noBash, "—"))
		add(gameDiagLikely, "This firmware has no full bash", "The launch found no bash that runs PortMaster's scripts ("+why+
			"). JukaHub adds Ubuntu's bash on its private C runtime when a port starts with Wi-Fi on"+pmRuntimeErrSuffix()+".", "", nil)
	}
	switch {
	case emptyControl != "":
		add(gameDiagSure, "An empty PortMaster folder hides the real one", emptyControl+".", "", nil)
	case control != "" || controlNote:
		add(gameDiagSure, "The script did not find PortMaster",
			"Every port starts by loading PortMaster's control.txt. Open PortMaster once, or press Y on the PortMaster row of the Ports panel → Repair PortMaster.", control, nil)
	case pmBroken != "":
		add(gameDiagSure, "PortMaster is missing some of its own files",
			"The script found PortMaster's control.txt, but a file it loads from PortMaster's folder is not there. Press Y on the PortMaster row of the Ports panel → Repair PortMaster.", pmBroken, nil)
	}
	if unset != "" {
		add(gameDiagSure, "A script stopped on a variable that was not set", gameDiagPortUnsetDetail(unsetVar, unset), unset, nil)
	}
	if store != "" {
		// The folder this launch kept the sign-in in, from its notes
		// (greenovercast_store.go).
		folder := goSettingsFolderFromNotes(notes)
		found = append(found, gameDiagPortFound{issue: launchIssue{Level: int(gameDiagLikely), Title: "Its saved sign-in could not be read",
			Where: folder, Detail: goStoreFailDetail(script, folder)}, line: store})
	}
	if glibc != "" {
		hasC := false
		for _, need := range glibcNeeds {
			if strings.HasPrefix(need, "GLIBC_") {
				hasC = true
			}
		}
		detail := setOr(glibcBy, "The game's program") + " needs " + strings.Join(glibcNeeds, ", ")
		title := "Built for a newer C++ library than this firmware's"
		if hasC {
			title = "Built for a newer C library than this firmware's"
			if host := portCatalogHostGlibc(); host != "" {
				detail += "; this firmware has " + host
			}
		}
		add(gameDiagSure, title, detail+". This build cannot load here — look for an update of the port, or a firmware with newer libraries.", glibc, nil)
	}
	if lib != "" {
		add(gameDiagSure, "A library it needs is not on this handheld",
			"Neither the port's own libs folder nor the firmware has it. Reinstall the port (Update in its details); if it stays, this build does not suit this firmware.", lib, gameDiagListFiles(libs, 6))
	}
	if execLine != "" {
		detail := "The program is missing from the port's folder, or it is built for another processor or C library (a 32-bit ARM build, for one), which this firmware cannot run. Reinstall the port; if it stays, this build does not suit this handheld."
		add(gameDiagSure, "Its game program could not be run", detail, execLine, gameDiagListFiles(progs, 4))
	}
	if ownBash != "" {
		add(gameDiagLikely, "JukaHub's full bash did not start",
			"The bash JukaHub adds for PortMaster (Ubuntu's, on its private C runtime) could not run. Delete the private-glibc folder in JukaHub's data folder and start the port again with Wi-Fi on to download it afresh.", ownBash, nil)
	}
	if gfx != "" {
		add(gameDiagLikely, "Its graphics did not start",
			"The game could not open the screen or start OpenGL ES. Some 3D ports need a GPU driver or display mode this firmware does not have; the line below is the game's own error.", gfx, nil)
	}
	if crash != "" {
		add(gameDiagLikely, "The game crashed",
			"The game's program stopped with an error of its own. A newer build of the port may fix it; PortMaster's Discord (#port-help) can read the log.", crash, nil)
	}
	if mem != "" {
		add(gameDiagLikely, "The system stopped it, likely for memory",
			"The game asked for more memory than was free. Close what else is running (a video, downloads) or turn on Settings → Games & emulators → Extra memory (swap), then try again.", mem, nil)
	}
	if rt != "" {
		add(gameDiagLikely, "A runtime it needs is missing or did not mount",
			"PortMaster downloads a port's runtime (Godot, Mono, LÖVE …) into its libs folder when it installs the port. Open PortMaster with Wi-Fi on, or press Update on the port.", rt, nil)
	}
	if perm != "" {
		add(gameDiagLikely, "A file it needs could not be run",
			"Permission denied: a program in the port's folder lost its executable mark (copying through a PC does that). Reinstall the port.", perm, nil)
	}
	if cmd != "" {
		add(gameDiagMaybe, "Commands this firmware does not have",
			"The script ran these and the firmware has no such command. Some are harmless; one the game needs stops it.", cmd, gameDiagListFiles(cmds, 6))
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].issue.Level < found[j].issue.Level })
	if len(found) > gameDiagPortIssueMax {
		found = found[:gameDiagPortIssueMax]
	}
	issues := make([]launchIssue, 0, len(found))
	var evidence []string
	fromOutput := false
	for _, f := range found {
		issues = append(issues, f.issue)
		if f.line != "" {
			evidence = append(evidence, f.line)
			if f.issue.Level < int(gameDiagMaybe) {
				fromOutput = true
			}
		}
	}
	return issues, evidence, fromOutput
}

// gameDiagPortGuardRe is a port script's own check that a file of the game's
// is there: `if [[ ! -f "${GAMEDIR}/valve/halflife.wad" ]]; then`.
var gameDiagPortGuardRe = regexp.MustCompile(`^\s*if\s+(?:\[\[|\[)\s*!\s+-([fde])\s+(\S+?)\s*(?:\]\]|\])\s*(?:;.*)?$`)

// gameDiagPortGameDataWords say what a script prints when such a check fails
// and the message is about the game's own files.
var gameDiagPortGameDataWords = []string{"missing", "not found", "no such", "copy", "need", "required",
	"install", "download", "extract", "files"}

// gameDiagPortNeededFile reads a port's script for a check it makes itself and
// stops on: an `if [[ ! -f "$GAMEDIR/valve/halflife.wad" ]]` block that prints
// a message about the game's files and exits. Half-Life checks its data that
// way, prints "Missing game files, see README for more info." on the screen
// (which JukaHub's log never sees), sleeps five seconds and exits 1 — a Smart
// Pro report (JH-AGE2UH) was told only that the firmware lacks lscpu. It
// returns the missing file's path inside the port's folder, or "".
func gameDiagPortNeededFile(script, dir string) string {
	if dir == "" {
		return ""
	}
	head, err := gameDiagReadHead(script)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(string(head), "\r\n", "\n"), "\n")
	for i, line := range lines {
		m := gameDiagPortGuardRe.FindStringSubmatch(line)
		if m == nil || !gameDiagPortGuardStops(lines[i+1:]) {
			continue
		}
		kind := m[1]
		target := strings.Trim(strings.TrimSpace(m[2]), `"'`)
		trimmed := strings.TrimPrefix(strings.TrimPrefix(target, "${GAMEDIR}"), "$GAMEDIR")
		if trimmed == target || !strings.HasPrefix(trimmed, "/") {
			continue // not the port's own folder: another program's file
		}
		path := dir + trimmed
		if kind == "d" {
			if dirExists(path) {
				continue
			}
		} else if _, err := os.Stat(path); err == nil {
			continue
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		return filepath.ToSlash(rel)
	}
	return ""
}

// gameDiagPortGuardStops reports whether an if block's own body prints a
// message about the game's files and exits. A block that only makes a folder
// or prints a note goes on and is no check for the game's data; neither is an
// exit in the block's else branch.
func gameDiagPortGuardStops(body []string) bool {
	depth := 1
	message, exited := false, false
	for _, raw := range body {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case line == "fi" || strings.HasPrefix(line, "fi;") || strings.HasPrefix(line, "fi ") || strings.HasPrefix(line, "fi\t"):
			depth--
			if depth == 0 {
				return message && exited
			}
		case depth == 1 && (line == "else" || strings.HasPrefix(line, "else ") || strings.HasPrefix(line, "elif ")):
			return false
		case line == "if" || strings.HasPrefix(line, "if ") || strings.HasPrefix(line, "if\t") || strings.HasPrefix(line, "if("):
			depth++
		case depth == 1 && (strings.HasPrefix(line, "echo ") || strings.HasPrefix(line, "printf ")):
			if gameDiagHasAny(strings.ToLower(line), gameDiagPortGameDataWords) {
				message = true
			}
		case depth == 1 && (line == "exit" || strings.HasPrefix(line, "exit ") || strings.HasPrefix(line, "exit;")):
			exited = true
		}
	}
	return false
}

// gameDiagPortControlLine sorts a line that names control.txt. missing: the
// script could not open control.txt itself (bash's "GreenOvercast.sh: line
// 18: /roms/ports/PortMaster/control.txt: No such file or directory",
// BusyBox's "source: can't open '…/control.txt'"). broken: control.txt was
// read, and one of its own lines could not open a file in PortMaster's folder
// (device_info.txt, funcs.txt). A line of control.txt that could not open
// anything else — TRIMUI_EX's ex_config — is neither: PortMaster was found.
func gameDiagPortControlLine(line, lower string) (missing, broken bool) {
	if !strings.Contains(lower, "control.txt") || !gameDiagHasAny(lower, gameDiagPortControlMissWords) {
		return false, false
	}
	if !strings.EqualFold(filepath.Base(gameDiagPortLineFile(line)), "control.txt") {
		return true, false
	}
	m := gameDiagPortMissingObjRe.FindStringSubmatch(line)
	if m == nil {
		return false, false
	}
	// Shell paths, so slashes on every OS.
	obj := strings.Trim(strings.TrimSpace(m[2]), `'"`)
	i := strings.LastIndexByte(m[1], '/')
	if i < 0 || !strings.HasPrefix(obj, "/") {
		return false, false
	}
	return false, strings.HasPrefix(obj, m[1][:i+1])
}

// gameDiagPortHarmless reports a line that changes nothing for the game: on
// a card without TRIMUI_EX, under a working bash, PortMaster's TrimUI
// control.txt cannot source ex_config and goes on; and commands no port
// needs (gameDiagPortHarmlessCmds: its splash tool, setterm). Such a line is
// no cause, no missing file and nothing the card shows.
func gameDiagPortHarmless(line string) bool {
	if strings.Contains(strings.ToLower(line), "ex_config") &&
		strings.EqualFold(filepath.Base(gameDiagPortLineFile(line)), "control.txt") {
		return true
	}
	ms := gameDiagPortCmdRe.FindAllStringSubmatch(line, -1)
	if len(ms) == 0 {
		return false
	}
	for _, m := range ms {
		if !gameDiagPortHarmlessCmds[strings.ToLower(m[1])] {
			return false
		}
	}
	return true
}

// gameDiagPortDropHarmless is lines without the gameDiagPortHarmless ones.
func gameDiagPortDropHarmless(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if !gameDiagPortHarmless(strings.TrimSpace(line)) {
			out = append(out, line)
		}
	}
	return out
}

// gameDiagPortNestedScript reports whether file — the one a shell error
// names — is a script the port started by its own #!/bin/bash line: not the
// port's script (nor JukaHub's temporary copy of it, jukahub-*.sh) and not a
// PortMaster *.txt the script sourced, which runs in the script's own shell.
func gameDiagPortNestedScript(file, script string) bool {
	base := filepath.Base(file)
	return file != "" && !strings.EqualFold(base, filepath.Base(script)) &&
		!strings.HasPrefix(base, "jukahub-") && !strings.HasSuffix(strings.ToLower(base), ".txt")
}

// gameDiagPortShDetail says what to do about a script that ran under BusyBox.
// file is the one the shell's error names.
func gameDiagPortShDetail(file, script string) string {
	if gameDiagPortBashMissing() {
		return "PortMaster's scripts need a full bash, and this firmware's /bin/bash is BusyBox. JukaHub adds one (Ubuntu's bash on its own C library) the next time a port starts with Wi-Fi on" +
			pmRuntimeErrSuffix() + "."
	}
	if gameDiagPortNestedScript(file, script) {
		return filepath.Base(file) + " was started by its own #!/bin/bash line, which on this firmware is BusyBox's sh. JukaHub gives each launch a real /bin/bash of its own when it can; this time it could not (the launch log's notes say why: portBinBash off, no mount namespace, or a failed bind). Send the log below with a bug report."
	}
	return "PortMaster's scripts need a full bash, but this one ran under BusyBox's sh (the firmware's /bin/bash). If portLaunchVia is set to \"firmware\", set it back to \"auto\"; otherwise send the log below with a bug report."
}

// gameDiagPortUnsetDetail says what a script that stopped on a variable
// nothing had set needs: variable as the shell named it, line the shell's own
// report. A port's script with `set -u` ends at its first `$1`, which no
// launcher gives it (JH-ZBPUTA), and a variable PortMaster's control.txt fills
// is empty when the script never found that file.
func gameDiagPortUnsetDetail(variable, line string) string {
	name := strings.TrimPrefix(variable, "$")
	file := filepath.Base(gameDiagPortLineFile(line))
	who := "The script"
	if file != "" && !strings.EqualFold(file, "control.txt") {
		who = file
	}
	detail := who + " stopped on " + variable + ": it runs with `set -u` (nounset), which ends the script at the first variable nothing has given it. "
	switch name {
	case "1", "2", "3", "@", "*":
		detail += "A port is started the way the firmware's Ports tab starts it — with no arguments — so a script that reads its game, disc or folder as \"$1\" gets none. "
	default:
		detail += "A variable PortMaster's control.txt sets stays empty when the script never found that file. "
	}
	if name == "1" {
		return detail + "If the port is meant to be started with a game, start it from there; otherwise its script has to fill the gap (\"${1:-}\")."
	}
	return detail + "The port's own script has to fill the gap (\"${" + name + ":-}\") before it runs with `set -u`."
}

// gameDiagPortExcerpt is the card's log lines: the lines behind the findings
// first, then the other lines worth reading, at most gameDiagExcerptMax; with
// neither, the last lines the port printed.
func gameDiagPortExcerpt(evidence, excerpt, output []string) []string {
	out := make([]string, 0, gameDiagExcerptMax)
	seen := map[string]bool{}
	for _, list := range [][]string{evidence, excerpt} {
		for _, line := range list {
			line = strings.TrimSpace(line)
			// gameDiagExcerpt has cut its lines already: compare cut forms.
			if r := []rune(line); len(r) > 220 {
				line = string(r[:220]) + "…"
			}
			if line == "" || seen[line] || len(out) >= gameDiagExcerptMax {
				continue
			}
			seen[line] = true
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		out = portLastLines(output, gameDiagExcerptMax)
	}
	return out
}
