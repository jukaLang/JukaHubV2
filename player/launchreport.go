package main

// launchreport.go — a launch that failed, written up for whoever fixes it,
// and filed as a GitHub issue when the player presses Submit.
//
// A game or an emulator that could not start used to end in the launch card
// (launch_errors.go) — or, for a failure before the program ran, in a toast —
// and the evidence went with it: what was run, with which arguments and
// environment, what the launch script and the emulator printed, which files
// were there. A tester then had to find Report a bug and describe it, and
// "[path] not found" was all a report could say (JH-X6HRVE).
//
// Every launch failure now opens the card with a report attached and two
// buttons, Cancel and Submit report; Submit is focused, so A files it. The
// report is written here:
//
//   - launchRecord is one launch as JukaHub ran it: what (kind, title,
//     system, game file, emulator), how (the program, every argument, its
//     folder, the environment variables that decide which program, libraries,
//     config, display and sound device the emulator gets, which C library),
//     and how it ended (pid, exit status or signal, how long it ran, whether
//     JukaHub had asked it to stop, the memory free at the start). The
//     library's launch (game_library.go), a port's (ports.go), the Emulators
//     page's tiles (emulators.go, raemu.go) and the Xbox/PSP hand-off
//     (hakux_handoff.go) fill one in; a launch that failed before its program
//     ran carries the error instead.
//   - launchReportDetails writes it out, off the main thread: the record, what
//     JukaHub's checks found, every file the launch needed (there or not, its
//     size, mode and time, a script's interpreter, a program's processor,
//     loader and libraries and whether this device can load it —
//     elfcompat.go), the emulator's folder, the launch script itself, what it
//     printed, RetroArch's log for the launch, and JukaHub's own log from just
//     before the launch on. Verbose on purpose: one report should be enough.
//   - Submit (launchReportSubmit) builds a bug report whose title and
//     description say what failed and whose "launch" field carries the
//     details, with the device diagnostics and the recent log, and sends it to
//     the JukaHub report service (bugrepsend.go), which files it as a public
//     issue. A report that cannot go now waits in Report a bug's outbox and is
//     sent later on its own.
//
// Privacy. Unlike Report a bug's free text, the details keep folder and file
// names: which file was not there, or would not load, is what a launch bug
// turns on. The account name in a home folder, credentials, the path and query
// of links, and e-mail, IP and MAC addresses are removed
// (bugrepScrubKeepPaths); environment variables other than the ones a launch
// depends on are named without their values; and the card shows every line of
// the details before Submit.

import (
	"bytes"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// The parts of the details are bounded one by one, so the whole stays
	// under bugrepLaunchMax: the launch script's first lines (and a long
	// one's last, within half as much again), what it printed, RetroArch's
	// log, another log the failure names, and JukaHub's own log.
	launchReportScriptLines = 60
	launchReportScriptMax   = 3 << 10
	launchReportScriptTail  = 30
	launchReportScriptRead  = 64 << 10
	launchReportOutputLines = 60
	launchReportOutputMax   = 6 << 10
	launchReportRALines     = 80
	launchReportRAMax       = 7 << 10
	launchReportLogLines    = 120
	launchReportLogMax      = 7 << 10
	// launchReportLogBefore is how many of JukaHub's log lines from just
	// before the launch began are kept, for what led up to it.
	launchReportLogBefore = 15
	// launchReportFolderMax bounds the listing of the emulator's folder.
	launchReportFolderMax = 30
	// launchReportArgsMax bounds the arguments listed one by one.
	launchReportArgsMax = 24
	// launchReportRecentLog bounds the recent log a launch report carries
	// beside its details, which hold the launch's own lines already.
	launchReportRecentLog = 16 << 10
	// launchReportRepeatWindow is how long a failure that was reported is
	// remembered, for the card's "you reported this at …" line.
	launchReportRepeatWindow = 30 * time.Minute
)

// launchRecord is one launch as JukaHub ran it. The thread that starts the
// launch fills in the start; the goroutine that reaps its program fills in the
// end once the start is done; launchReportDetails reads it after both.
type launchRecord struct {
	// What was started: "Library game", "Emulator", "PortMaster port", …
	Kind     string
	Title    string
	System   string
	ROM      string
	Launcher string
	// How: the program, its arguments and folder, the environment it was
	// given (launchReportEnv) and the C library it ran on.
	Program string
	Args    []string
	Dir     string
	Env     []string
	Glibc   string
	// When, and how it ended (gameLaunchEndWords).
	Started time.Time
	Pid     int
	MemMB   int
	Ended   time.Time
	Exit    string
	Stopped bool
	// Reason is why a launch failed before its program ran ("" when it ran).
	Reason string
	// The logs: the launcher's own output, RetroArch's for this launch, and
	// another one the failure names (a port's, a profile's).
	OutPath string
	RALog   string
	LogPath string
	// Notes are facts a launch path adds (the hand-off script's steps).
	Notes []string
	// logMark is JukaHub's log line count when the launch began, and
	// logTitle names the log section ("" for "JukaHub log since the launch").
	logMark  uint64
	logTitle string
}

// launchRecordNew starts the record of a launch.
func launchRecordNew(kind, title, launcher string) *launchRecord {
	return &launchRecord{
		Kind:     kind,
		Title:    strings.TrimSpace(title),
		Launcher: strings.TrimSpace(launcher),
		Started:  time.Now(),
		logMark:  appLog.Mark(),
	}
}

// launchRecordForGame starts the record of a library game's launch.
func launchRecordForGame(game libraryGame, launcher string) *launchRecord {
	rec := launchRecordNew("Library game", game.Label(), launcher)
	rec.System = game.System
	rec.ROM = game.Path
	return rec
}

// noteCommand records the program about to start: its path, arguments,
// folder and environment. Call it last before the start, once every hook has
// changed cmd.
func (r *launchRecord) noteCommand(cmd *exec.Cmd) {
	if r == nil || cmd == nil {
		return
	}
	r.Program = cmd.Path
	r.Args = append([]string(nil), cmd.Args...)
	r.Dir = cmd.Dir
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	r.Env = launchReportEnv(env)
	if r.Glibc == "" && runtime.GOOS == "linux" {
		r.Glibc = "the firmware's"
	}
}

// noteStarted records a program that started: when, its pid, and the memory
// free (MB) as it did.
func (r *launchRecord) noteStarted(cmd *exec.Cmd, memMB int) {
	if r == nil {
		return
	}
	r.Started = time.Now()
	if cmd != nil && cmd.Process != nil {
		r.Pid = cmd.Process.Pid
	}
	r.MemMB = memMB
}

// noteEnd records how the program ended: when its process group was gone,
// its status, and whether JukaHub had asked it to stop.
func (r *launchRecord) noteEnd(at time.Time, waitErr error, stopped bool) {
	if r == nil {
		return
	}
	if !at.IsZero() {
		r.Ended = at
	}
	r.Exit = gameLaunchEndWords(waitErr)
	r.Stopped = stopped
}

// ── Environment ──────────────────────────────────────────────────────────

// launchReportEnvShown are the variables whose values a report shows: the ones
// that decide which program, libraries, config, display and sound device an
// emulator ends up with. launchReportEnvPrefixes add families of them.
var launchReportEnvShown = map[string]bool{
	"HOME": true, "PATH": true, "LD_LIBRARY_PATH": true, "LD_PRELOAD": true, "PWD": true, "SHELL": true,
	"LANG": true, "LC_ALL": true, "TZ": true, "AUDIODEV": true, "DISPLAY": true, "WAYLAND_DISPLAY": true,
	"XDG_RUNTIME_DIR": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true,
}

var launchReportEnvPrefixes = []string{
	"SDL_", "EGL_", "MESA_", "LIBGL_", "GALLIUM_", "ALSA_", "PULSE_", "JUKAHUB_", "WINE", "BOX64_", "BOX86_",
	"DXVK_", "VKD3D_",
}

// launchReportEnvSecret reports a name whose value is never shown, whatever
// list it is on (JUKAHUB_GITHUB_TOKEN).
func launchReportEnvSecret(name string) bool {
	upper := strings.ToUpper(name)
	for _, word := range []string{"TOKEN", "SECRET", "PASSW", "APIKEY", "API_KEY", "CREDENTIAL", "COOKIE", "PRIVATE"} {
		if strings.Contains(upper, word) {
			return true
		}
	}
	for _, part := range strings.Split(upper, "_") {
		switch part {
		case "KEY", "PASS", "PIN", "AUTH", "SESSION", "SIG", "PSK":
			return true
		}
	}
	return false
}

// launchReportEnvVisible reports a variable whose value a report shows.
func launchReportEnvVisible(name string) bool {
	if launchReportEnvSecret(name) {
		return false
	}
	if launchReportEnvShown[name] {
		return true
	}
	for _, prefix := range launchReportEnvPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// launchReportEnv is environ as a report shows it: the variables a launch
// depends on as NAME=value, sorted (the last of a repeated name, as os/exec
// passes it), then one line naming the others, whose values are left out.
func launchReportEnv(environ []string) []string {
	last := map[string]string{}
	for _, kv := range environ {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			continue
		}
		last[key] = value
	}
	names := make([]string, 0, len(last))
	for key := range last {
		names = append(names, key)
	}
	sort.Strings(names)
	var shown, other []string
	for _, key := range names {
		if launchReportEnvVisible(key) {
			shown = append(shown, key+"="+last[key])
		} else {
			other = append(other, key)
		}
	}
	if len(other) > 0 {
		if len(other) > 40 {
			other = append(other[:40:40], "…")
		}
		shown = append(shown, "(also set, values left out: "+strings.Join(other, " ")+")")
	}
	return shown
}

// ── The details ──────────────────────────────────────────────────────────

// launchReportDetails is the report's text for rec (see the file comment),
// scrubbed and bounded to bugrepLaunchMax. summary is the card's line on how
// it ended and issues what JukaHub's checks found. It reads files and the
// card: never on the main thread.
func launchReportDetails(rec *launchRecord, summary string, issues []launchIssue) string {
	return launchReportCompose(rec, summary, issues, true)
}

// launchReportCompose writes the details; with full false it reads nothing
// from the card — the record, the device, the findings and JukaHub's log only
// — for a Submit whose full details stalled (launchReportWaitTick).
func launchReportCompose(rec *launchRecord, summary string, issues []launchIssue, full bool) string {
	if rec == nil {
		rec = &launchRecord{}
	}
	var b strings.Builder
	line := func(key, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		b.WriteString(fmt.Sprintf("%-12s: %s\n", key, value))
	}
	section := func(title string) {
		b.WriteString("\n" + title + "\n" + strings.Repeat("-", utf8.RuneCountInString(title)) + "\n")
	}

	b.WriteString("JukaHub launch report\n=====================\n")
	line("What", strings.TrimSpace(rec.Kind+" "+strconv.Quote(rec.Title)))
	line("System", rec.System)
	line("Game file", rec.ROM)
	line("Emulator", rec.Launcher)
	if full {
		if core, corePath, how := launchReportCore(rec); core != "" {
			if corePath != "" {
				core += " (" + corePath + ")"
			} else {
				core += " (in none of RetroArch's cores folders)"
			}
			line("Core", core+" — "+how)
		}
	}
	line("Started", launchReportWhen(rec.Started))
	line("Outcome", launchReportOutcome(rec))
	line("Card says", summary)

	section("This device")
	line("Device", deviceDisplayName(DetectDevice())+" · "+P().Name()+" · "+runtime.GOOS+"/"+runtime.GOARCH)
	if IsTrimuiDevice() {
		firmware := fwStockVersion()
		if firmware == "" {
			firmware = "unknown (no version file)"
		}
		line("Firmware", firmware)
		// Which OS is on the card is read from the card.
		if full {
			card := acFirmwareName(acDetectFirmware())
			if jukaosNativeActive() {
				card += " · a JukaHub OS session"
			}
			line("Card OS", card)
		}
	}
	line("Kernel", launchReportKernel())
	memory := strconv.Itoa(gameLaunchMemFreeMB()) + " MB free now"
	if rec.MemMB > 0 {
		memory = strconv.Itoa(rec.MemMB) + " MB free at the start, " + memory
	}
	line("Memory", memory)
	if full {
		line("C library", launchReportLibc())
		line("Free space", launchReportFreeSpace(rec))
	}

	section("Command")
	line("Not started", rec.Reason)
	if rec.Program == "" && len(rec.Args) == 0 {
		b.WriteString("JukaHub did not get as far as a command.\n")
	} else {
		line("Program", rec.Program)
		for i, arg := range rec.Args {
			if i == launchReportArgsMax {
				b.WriteString(fmt.Sprintf("  … and %d more arguments\n", len(rec.Args)-i))
				break
			}
			b.WriteString(fmt.Sprintf("  arg[%d] = %s\n", i, strconv.Quote(arg)))
		}
		line("Folder", rec.Dir)
		if rec.Pid > 0 {
			line("Process", "pid "+strconv.Itoa(rec.Pid)+", leading a process group of its own")
		}
		line("C library", rec.Glibc)
		if len(rec.Env) > 0 {
			b.WriteString("Environment :\n")
			for _, kv := range rec.Env {
				b.WriteString("  " + kv + "\n")
			}
		}
	}
	for _, note := range rec.Notes {
		line("Note", note)
	}

	section("What JukaHub found")
	if len(issues) == 0 {
		b.WriteString("Nothing JukaHub can check was missing or broken.\n")
	}
	for i, is := range issues {
		b.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, launchReportLevel(is.Level), is.Title))
		if len(is.Files) > 0 {
			b.WriteString("   Missing : " + strings.Join(is.Files, ", ") + "\n")
		}
		if is.Where != "" {
			b.WriteString("   Where   : " + is.Where + "\n")
		}
		if is.Detail != "" {
			b.WriteString("   Detail  : " + is.Detail + "\n")
		}
	}

	if full {
		launchReportFiles(&b, rec, line, section)
	} else {
		section("Files and logs")
		b.WriteString("Not read: reading the card took too long (a card with errors stalls reads for tens of " +
			"seconds), so this report holds what JukaHub knew without it.\n")
	}

	title := rec.logTitle
	if title == "" {
		title = "JukaHub log since the launch"
	}
	section(title)
	mark := rec.logMark
	if mark > launchReportLogBefore {
		mark -= launchReportLogBefore
	} else {
		mark = 0
	}
	if lines := appLog.LinesSince(mark, launchReportLogLines); len(lines) > 0 {
		b.WriteString(bugrepClipTail(strings.Join(lines, "\n"), launchReportLogMax) + "\n")
	} else {
		b.WriteString("(nothing)\n")
	}
	return bugrepClip(bugrepScrubKeepPaths(b.String()), bugrepLaunchMax)
}

// launchReportFiles is the part of the details that reads the card: every file
// the launch needed, the RetroArch the launch scripts call, the emulator's
// folder, the launch script and the logs the launch wrote.
func launchReportFiles(b *strings.Builder, rec *launchRecord, line func(key, value string), section func(title string)) {
	section("Files")
	if rec.ROM != "" {
		line("Game file", launchReportFileFacts(rec.ROM, rec.Env))
	}
	if rec.Launcher != "" {
		line("Emulator", launchReportFileFacts(rec.Launcher, rec.Env))
	}
	if filepath.IsAbs(rec.Program) && !launchReportSamePath(rec.Program, rec.Launcher) {
		line("Program", launchReportFileFacts(rec.Program, rec.Env))
	}
	if _, corePath, _ := launchReportCore(rec); corePath != "" && !launchReportSamePath(corePath, rec.Launcher) {
		line("Core file", launchReportFileFacts(corePath, rec.Env))
	}
	if retroArchLauncher(rec.Launcher) && raGetSupported() {
		st := raCardCheck()
		ra := st.Need
		switch {
		case ra == "":
			ra = "none found"
		case st.Problem == "":
			ra += " — there, and it can start"
		default:
			ra += " — " + st.Problem
		}
		if st.Calls != "" {
			ra += " (" + pluralizeCount(st.Scripts, "launch script calls", "launch scripts call") + " it)"
		}
		if st.Found != "" {
			ra += "; " + st.Found + " does instead"
		}
		if !st.Lost.IsZero() {
			ra += "; it was there at " + st.Lost.Format("15:04:05")
		}
		line("RetroArch", ra)
		if st.Need != "" && !launchReportSamePath(st.Need, rec.Program) {
			line("RA file", launchReportFileFacts(st.Need, rec.Env))
		}
	}
	if rec.Launcher != "" {
		line("Its folder", launchReportFolder(filepath.Dir(rec.Launcher)))
	}

	if script := launchReportScript(rec.Launcher); script != "" {
		section("Launch script " + filepath.Base(rec.Launcher))
		b.WriteString(script)
	}
	launchReportLog(b, section, "What the launcher printed", rec.OutPath, launchReportOutputLines, launchReportOutputMax)
	launchReportLog(b, section, "RetroArch's log for this launch", rec.RALog, launchReportRALines, launchReportRAMax)
	launchReportLog(b, section, "Log "+filepath.Base(rec.LogPath), rec.LogPath, launchReportOutputLines, launchReportOutputMax)
}

// launchReportLevel names a finding's certainty (gameDiagLevel).
func launchReportLevel(level int) string {
	switch gameDiagLevel(level) {
	case gameDiagSure:
		return "certain"
	case gameDiagLikely:
		return "likely"
	}
	return "maybe"
}

// launchReportWhen is a time as the device's clock had it.
func launchReportWhen(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05 -0700") + " (the device's clock)"
}

// launchReportOutcome is how the launch ended, in words.
func launchReportOutcome(rec *launchRecord) string {
	switch {
	case rec.Reason != "":
		return "did not start: " + firstLine(rec.Reason)
	case rec.Exit == "":
		return "no end seen yet"
	}
	out := "ended"
	if !rec.Ended.IsZero() && !rec.Started.IsZero() && !rec.Ended.Before(rec.Started) {
		out += " after " + rec.Ended.Sub(rec.Started).Round(100*time.Millisecond).String()
	}
	out += ": " + rec.Exit
	if rec.Stopped {
		return out + " (JukaHub had asked it to stop)"
	}
	return out + " (by itself: JukaHub had not asked it to stop)"
}

// launchReportCore is the core a RetroArch launch runs, its file in
// RetroArch's cores folders when it is there, and how the report knows: the
// core file itself, the launch script naming it, or — when the script names
// none or several — a guess (the system's usual core), said as one.
func launchReportCore(rec *launchRecord) (core, path, how string) {
	if rec == nil || rec.Launcher == "" || !retroArchLauncher(rec.Launcher) {
		return "", "", ""
	}
	switch strings.ToLower(filepath.Ext(rec.Launcher)) {
	case ".so", ".dll":
		name, _ := parseCoreFile(filepath.Base(rec.Launcher))
		return name, rec.Launcher, "the core file itself"
	}
	core = gameCoreFromLauncher(rec.Launcher, rec.System)
	if core == "" {
		return "", "", ""
	}
	how = "the system's usual core: the launch script names none (RetroArch's log below says which it loaded)"
	if head, err := gameDiagReadHead(rec.Launcher); err == nil {
		switch names := coreNamesIn(string(head)); {
		case len(names) == 1:
			how = "named by the launch script"
		case len(names) > 1:
			how = "a guess: the launch script names " + strings.Join(names, ", ") + " (RetroArch's log below says which it loaded)"
		}
	}
	if dirs := retroArchCoreDirs(); len(dirs) > 0 {
		path = coreActiveBinary(core, dirs)
	}
	return core, path, how
}

// launchReportSamePath reports two names of one file.
func launchReportSamePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(sa, sb)
}

// launchReportKernel is the running kernel's release ("" off Linux).
func launchReportKernel() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	release := strings.TrimSpace(fwReadSmall("/proc/sys/kernel/osrelease"))
	version := strings.TrimSpace(fwReadSmall("/proc/sys/kernel/version"))
	return strings.TrimSpace(release + " " + version)
}

// launchReportLibc is the firmware's C library, by the newest glibc version
// its libc.so.6 defines — what a core or an emulator built elsewhere is
// checked against (elfcompat.go). Read once.
var launchReportLibc = sync.OnceValue(func() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	for _, p := range []string{"/lib/libc.so.6", "/lib64/libc.so.6", "/usr/lib/libc.so.6",
		"/lib/aarch64-linux-gnu/libc.so.6", "/usr/lib/aarch64-linux-gnu/libc.so.6", "/lib/x86_64-linux-gnu/libc.so.6"} {
		info := elfLibraryInfo(p)
		if !info.exists {
			continue
		}
		if v := elfHighestInFamily(info.defs, "GLIBC"); v != "" {
			return p + " defines up to " + v
		}
		return p + " (its versions could not be read)"
	}
	return "no glibc libc.so.6 in the usual folders"
})

// launchReportFreeSpace is the free space where the launch reads and writes:
// the game's folder, the emulator's and JukaHub's data folder (one figure per
// file system).
func launchReportFreeSpace(rec *launchRecord) string {
	var parts []string
	seen := map[int64]bool{}
	add := func(label, dir string) {
		if strings.TrimSpace(dir) == "" || dir == "." {
			return
		}
		free, err := freeBytes(dir)
		if err != nil || seen[free] {
			return
		}
		seen[free] = true
		parts = append(parts, label+" "+launchReportSize(free))
	}
	if rec.ROM != "" {
		add("game's folder", filepath.Dir(rec.ROM))
	}
	if rec.Launcher != "" {
		add("emulator's folder", filepath.Dir(rec.Launcher))
	}
	if dir, err := P().DataDir(); err == nil {
		add("JukaHub's data", dir)
	}
	return strings.Join(parts, " · ")
}

// launchReportSize is a size in bytes, and in KB, MB or GB beside it.
func launchReportSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB (%d bytes)", float64(n)/(1<<10), n)
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

// launchReportFileFacts describes the file at p: whether it is there, its
// size, mode and time, and what it is — a script and the interpreter its #!
// line names, a program (launchReportELF), a Windows program, or data and its
// first bytes. env is the launch's environment, whose LD_LIBRARY_PATH a
// program's libraries are looked up on.
func launchReportFileFacts(p string, env []string) string {
	st, err := os.Stat(p)
	switch {
	case err != nil && errors.Is(err, os.ErrNotExist):
		return p + " — MISSING"
	case err != nil:
		return p + " — cannot be read: " + raCardErrWords(err)
	case st.IsDir():
		return p + " — a folder"
	}
	facts := []string{launchReportSize(st.Size()), st.Mode().String(), "changed " + st.ModTime().Format("2006-01-02 15:04")}
	head := firmwareScriptHead(p)
	switch {
	case st.Size() == 0:
		facts = append(facts, "EMPTY")
	case len(head) == 0:
		facts = append(facts, "its first bytes cannot be read")
	case bytes.HasPrefix(head, []byte("#!")):
		interp, arg, _ := parseShebang(head)
		what := "a script for " + strings.TrimSpace(interp+" "+arg)
		if filepath.IsAbs(interp) {
			if firmwareExecutable(interp) {
				what += " (there)"
			} else {
				what += " (MISSING on this device, or not executable)"
			}
		}
		if bytes.Contains(head, []byte("\r\n")) {
			what += "; it has Windows line endings, which a shell reads as part of each command"
		}
		facts = append(facts, what)
	case bytes.HasPrefix(head, []byte("\x7fELF")):
		facts = append(facts, launchReportELF(p, env))
	case bytes.HasPrefix(head, []byte("MZ")):
		facts = append(facts, "a Windows program or library")
	default:
		n := len(head)
		if n > 8 {
			n = 8
		}
		facts = append(facts, fmt.Sprintf("data, first bytes % X", head[:n]))
	}
	if runtime.GOOS != "windows" && st.Mode().IsRegular() && st.Mode().Perm()&0o111 == 0 &&
		(bytes.HasPrefix(head, []byte("#!")) || bytes.HasPrefix(head, []byte("\x7fELF"))) {
		facts = append(facts, "not marked executable")
	}
	return p + " — " + strings.Join(facts, ", ")
}

// launchReportELF describes a program or a library: its processor, the loader
// it names and whether that is on this device, the libraries it needs (each
// looked up where the loader would look: its own run path, the launch's
// LD_LIBRARY_PATH, the system folders) and what elfcompat.go says about
// loading it here.
func launchReportELF(p string, env []string) string {
	f, err := elf.Open(p)
	if err != nil {
		return "a damaged program (" + firstLine(err.Error()) + ")"
	}
	defer f.Close()
	out := "an ELF " + elfArchName(f.Class, f.Machine)
	switch f.Type {
	case elf.ET_EXEC:
		out += " program"
	case elf.ET_DYN:
		out += " program or library"
	}
	for _, prog := range f.Progs {
		if prog.Type != elf.PT_INTERP {
			continue
		}
		if prog.Filesz == 0 || prog.Filesz > 4096 {
			out += ", the name of its loader is damaged"
			break
		}
		buf := make([]byte, prog.Filesz)
		if _, err := prog.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
			out += ", the name of its loader cannot be read"
			break
		}
		interp := strings.TrimRight(string(buf), "\x00")
		if _, err := os.Stat(interp); err != nil {
			out += ", loader " + interp + " MISSING on this device"
		} else {
			out += ", loader " + interp + " (there)"
		}
		break
	}
	if needed, err := f.ImportedLibraries(); err == nil && len(needed) > 0 {
		ld, _ := childEnvLookup(env, "LD_LIBRARY_PATH")
		dirs := append(elfRunPaths(f, filepath.Dir(p)), elfLibrarySearchDirs(nil, ld, "")...)
		var missing []string
		for _, lib := range needed {
			if _, state := elfFindLibrary(lib, f.Class, f.Machine, dirs); state == elfLibMissing {
				missing = append(missing, lib)
			}
		}
		out += "; needs " + strings.Join(needed, " ")
		if len(missing) > 0 {
			// A launch script may export folders of its own on top.
			out += "; NOT FOUND on the library path it was given (its run path, LD_LIBRARY_PATH, the system " +
				"folders): " + strings.Join(missing, " ")
		}
	}
	if runtime.GOOS == "linux" {
		if problem := nativeLoadProblem(p); problem != "" {
			out += "; load check: " + problem
		} else {
			out += "; load check: the versions it needs are there"
		}
	}
	return out
}

// launchReportFolder lists a folder's entries, sub-folders marked with a
// slash ("" when it cannot be read).
func launchReportFolder(dir string) string {
	if strings.TrimSpace(dir) == "" || dir == "." {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return dir + " — cannot be read: " + raCardErrWords(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	more := ""
	if len(names) > launchReportFolderMax {
		more = " … and " + strconv.Itoa(len(names)-launchReportFolderMax) + " more"
		names = names[:launchReportFolderMax]
	}
	if len(names) == 0 {
		return dir + " — empty"
	}
	return dir + ": " + strings.Join(names, ", ") + more
}

// launchReportScript is a launch script's first lines, numbered, and a long
// one's last lines too — where a port script starts its program, which the
// first lines of GreenOvercast's never reached (JH-AT4EEE) — or "" when the
// launcher is not a script (a program, a core).
func launchReportScript(launcher string) string {
	if !launcherShellScript(launcher) && !strings.EqualFold(filepath.Ext(launcher), ".bat") &&
		!strings.EqualFold(filepath.Ext(launcher), ".cmd") {
		return ""
	}
	f, err := os.Open(launcher)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, launchReportScriptRead))
	if len(data) == 0 || bytes.HasPrefix(data, []byte("\x7fELF")) || bytes.IndexByte(data, 0) >= 0 {
		return ""
	}
	head := data[:min(len(data), launchReportScriptMax)]
	lines := strings.Split(strings.ReplaceAll(string(head), "\r", ""), "\n")
	if len(lines) > launchReportScriptLines {
		lines = lines[:launchReportScriptLines]
	}
	var b strings.Builder
	for i, text := range lines {
		b.WriteString(fmt.Sprintf("%3d| %s\n", i+1, text))
	}
	if len(data) >= launchReportScriptRead {
		return b.String() // too long to number its last lines
	}
	all := strings.Split(strings.TrimRight(strings.ReplaceAll(string(data), "\r", ""), "\n"), "\n")
	from, size := len(all), 0
	for from > len(lines) && len(all)-from < launchReportScriptTail && size+len(all[from-1])+1 <= launchReportScriptMax/2 {
		from--
		size += len(all[from]) + 1
	}
	if from >= len(all) {
		return b.String()
	}
	if from > len(lines) {
		b.WriteString(fmt.Sprintf("   | … lines %d–%d left out …\n", len(lines)+1, from))
	}
	for i := from; i < len(all); i++ {
		b.WriteString(fmt.Sprintf("%3d| %s\n", i+1, all[i]))
	}
	return b.String()
}

// launchReportLog adds the end of the log at path as a section ("" path: no
// section at all).
func launchReportLog(b *strings.Builder, section func(string), title, path string, maxLines, maxBytes int) {
	if strings.TrimSpace(path) == "" {
		return
	}
	section(title + " (" + path + ")")
	st, err := os.Stat(path)
	switch {
	case err != nil:
		b.WriteString("(no such file: " + raCardErrWords(err) + ")\n")
		return
	case st.Size() == 0:
		b.WriteString("(empty: nothing was written)\n")
		return
	}
	lines := gameDiagReadTail(path, time.Time{})
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	b.WriteString(bugrepClipTail(strings.Join(lines, "\n"), maxBytes) + "\n")
}

// ── The report ───────────────────────────────────────────────────────────

// launchReportDraft is the report a launch-failure card offers. The card owns
// it on the main thread; details written on a goroutine reach it through
// tbxPost (launchReportDetailsReady).
type launchReportDraft struct {
	rec     *launchRecord
	title   string
	summary string
	details string
	ready   bool
	// waiting is set by a Submit pressed before the details were written:
	// the report goes as soon as they are, or at waitAt+launchReportWaitMax
	// without what the card holds (launchReportWaitTick).
	waiting bool
	waitAt  time.Time
	ref     string
	created time.Time
	// seenRef and seenAt are this same failure's report earlier this session.
	seenRef string
	seenAt  time.Time
	// cardSummary and issues are what the card said, for those details.
	cardSummary string
	issues      []launchIssue
}

// launchReportWaitMax is how long a Submit waits for details still being
// written. A card with errors stalls reads for tens of seconds (JH-X6HRVE:
// a 21 s statfs), and that is a report that must still go.
const launchReportWaitMax = 12 * time.Second

// launchReportSeen remembers the failures reported this session by their
// issue title: the card says so when the same one comes back. Main thread.
var launchReportSeen = map[string]launchReportSeenAt{}

type launchReportSeenAt struct {
	ref string
	at  time.Time
}

// launchReportNewDraft is the report for the failure a card shows: the issue's
// title and description from rec and the card's words, and the details —
// given (written on the reaper goroutine already) or written now on a
// goroutine of its own. Main thread.
func launchReportNewDraft(rec *launchRecord, cardTitle, summary string, issues []launchIssue, details string) *launchReportDraft {
	d := &launchReportDraft{rec: rec, ref: bugrepNewRef(), created: time.Now(), cardSummary: summary}
	d.issues = append([]launchIssue(nil), issues...)
	d.title = launchReportIssueTitle(rec, cardTitle, issues)
	d.summary = launchReportSummary(rec, cardTitle, summary, issues)
	if seen, ok := launchReportSeen[d.title]; ok && time.Since(seen.at) < launchReportRepeatWindow {
		d.seenRef, d.seenAt = seen.ref, seen.at
	}
	// The diagnostics Submit adds hash JukaHub's own files: started now in the
	// background, as Report a bug does, so Submit finds them done (or says
	// "still being hashed") instead of hashing tens of megabytes on the UI
	// thread — 7.5 s frozen on 2026-11-25 (JH-CA8ZMU's log).
	componentVersionsWarm(appConfig)
	if details != "" {
		d.details, d.ready = details, true
		return d
	}
	own := append([]launchIssue(nil), issues...)
	go func() {
		text := ""
		func() {
			defer func() {
				if r := recover(); r != nil {
					logWarn("launch", "writing the launch report failed: %v", r)
					text = "(JukaHub could not write the launch details: " + fmt.Sprint(r) + ")"
				}
			}()
			text = launchReportDetails(rec, summary, own)
		}()
		tbxPost(func() { launchReportDetailsReady(d, text) })
	}()
	return d
}

// launchReportDetailsReady hands written details to their draft, and sends the
// report when Submit was pressed while they were being written. Main thread.
func launchReportDetailsReady(d *launchReportDraft, text string) {
	if d == nil {
		return
	}
	d.details, d.ready = text, true
	if launchFailure.report != d {
		return // the card was closed meanwhile
	}
	launchFailure.lines = nil // wrap again, with the details
	if d.waiting {
		launchReportSubmit()
	}
}

// launchReportWaitTick sends a report whose Submit has waited
// launchReportWaitMax for its details with the details that need no card
// (launchReportCompose, full false). The card calls it every frame it draws.
// Main thread.
func launchReportWaitTick() {
	d := launchFailure.report
	if d == nil || d.ready || !d.waiting || time.Since(d.waitAt) < launchReportWaitMax {
		return
	}
	logWarn("launch", "the launch details for %s took longer than %s; sending what is known without the card", d.ref, launchReportWaitMax)
	d.details = launchReportCompose(d.rec, d.cardSummary, d.issues, false)
	d.ready = true
	launchFailure.lines = nil
	launchReportSubmit()
}

// launchReportIssueTitle is the issue's title: what failed and the first thing
// found, "Couldn't launch Sonic CD (Sega CD): The Sega CD BIOS is missing".
func launchReportIssueTitle(rec *launchRecord, cardTitle string, issues []launchIssue) string {
	what := ""
	if rec != nil {
		what = strings.TrimSpace(rec.Title)
		if what != "" && rec.System != "" {
			what += " (" + rec.System + ")"
		}
	}
	if what == "" {
		what = strings.TrimSpace(cardTitle)
	}
	title := "Couldn't launch " + what
	why := ""
	switch {
	case len(issues) > 0:
		why = issues[0].Title
	case rec != nil && rec.Reason != "":
		why = firstLine(rec.Reason)
	case rec != nil && rec.Exit != "":
		why = rec.Exit
	}
	if why != "" {
		title += ": " + why
	}
	title = strings.Join(strings.Fields(title), " ")
	if len(title) > bugrepTitleMax-4 {
		title = strings.TrimSpace(bugrepClipRunes(title, bugrepTitleMax-4)) + "…"
	}
	return title
}

// launchReportSummary is the issue's description: what was launched and how it
// failed, what the card found, and what follows in the issue.
func launchReportSummary(rec *launchRecord, cardTitle, summary string, issues []launchIssue) string {
	var b strings.Builder
	b.WriteString(strings.TrimSuffix(strings.TrimSpace(cardTitle), ".") + ".")
	if summary = strings.TrimSpace(summary); summary != "" {
		b.WriteString(" " + summary)
	}
	b.WriteString("\n")
	if rec != nil && rec.Reason != "" {
		b.WriteString("\nJukaHub could not start it: " + rec.Reason + "\n")
	}
	if len(issues) > 0 {
		b.WriteString("\n")
	}
	for i, is := range issues {
		b.WriteString(fmt.Sprintf("%d. %s", i+1, is.Title))
		if len(is.Files) > 0 {
			b.WriteString(" — missing: " + strings.Join(is.Files, ", "))
		}
		if is.Where != "" {
			b.WriteString(" — in " + is.Where)
		}
		if is.Detail != "" {
			b.WriteString(" — " + is.Detail)
		}
		b.WriteString("\n")
	}
	if rec != nil {
		b.WriteString("\n")
		if rec.ROM != "" {
			b.WriteString("Game file: " + rec.ROM + "\n")
		}
		if rec.Launcher != "" {
			b.WriteString("Emulator: " + rec.Launcher + "\n")
		}
		if outcome := launchReportOutcome(rec); outcome != "" {
			b.WriteString("Outcome: " + outcome + "\n")
		}
	}
	b.WriteString("\nFiled from the launch-failure card. The launch details (command, environment, files, what the " +
		"emulator printed and JukaHub's log around the launch), the device diagnostics and the recent log follow.")
	return b.String()
}

// launchReportBuild is the bug report Submit sends for d. Main thread: the
// diagnostics read the config.
func launchReportBuild(config *Config, d *launchReportDraft) bugrepReport {
	version := "unknown"
	if config != nil {
		version = versionString(config)
	}
	release := installedReleaseTag()
	if release == "" {
		release = readInstalledReleaseInfo().Version
	}
	tail := diagnosticLogTail(appLog.String())
	if strings.TrimSpace(tail) == "" {
		tail = "(the log is empty)"
	}
	details := d.details
	if !d.ready || strings.TrimSpace(details) == "" {
		details = "(the launch details were not written)"
	}
	return bugrepReport{
		Schema:      1,
		Kind:        "bug_report",
		App:         "JukaHub",
		ID:          d.ref,
		Category:    "crash",
		Title:       bugrepClip(bugrepScrubKeepPaths(d.title), bugrepTitleMax),
		Description: bugrepClip(bugrepScrubKeepPaths(d.summary), bugrepDescriptionMax),
		Version:     version,
		Release:     release,
		Channel:     updateChannelNow(),
		Device:      deviceDisplayName(DetectDevice()),
		Platform:    P().Name(),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Diagnostics: bugrepClip(bugrepScrub(DiagnosticsText(config)), bugrepDiagnosticsMax),
		Log:         bugrepClipTail(bugrepScrub(tail), launchReportRecentLog),
		Launch:      bugrepClip(details, bugrepLaunchMax),
		Created:     d.created.UTC().Format(time.RFC3339),
	}
}

// launchReportSubmit is the card's Submit: the report goes, and the card
// closes; how the send went comes as a notification. Details still being
// written are waited for (launchReportDetailsReady sends then). Main thread.
func launchReportSubmit() {
	d := launchFailure.report
	if d == nil {
		closeLaunchFailureDialog()
		return
	}
	if !d.ready {
		if !d.waiting {
			d.waiting, d.waitAt = true, time.Now()
			PlayActivateSound()
		}
		return
	}
	r := launchReportBuild(appConfig, d)
	launchReportSeen[d.title] = launchReportSeenAt{ref: r.ID, at: time.Now()}
	logInfo("launch", "launch report %s: %s", r.ID, d.title)
	launchFailure = launchFailureState{}
	PlayActivateSound()
	launchReportSend(r)
}

// launchReportSend sends r to the report service, or keeps it in the outbox
// for later, with the same rules as Report a bug's Submit (bugrep.go,
// bugrepSend) — but nothing here touches that screen's form. Main thread.
func launchReportSend(r bugrepReport) {
	endpoint := bugrepEndpointURL()
	if endpoint == "" {
		msg := "This build has no report service"
		if path, err := bugrepSaveCopy(r); err == nil {
			msg += " — the report is saved as " + bugrepBaseName(path) + " in Patch reports"
		}
		notifPush(msg, notifWarning)
		return
	}
	if bugrepClockWrong(time.Now()) {
		launchReportHold(r, bugrepHoldClock)
		return
	}
	if !IsOnline() {
		launchReportHold(r, bugrepHoldOffline)
		return
	}
	ShowToast("Sending bug report "+r.ID+"…", ToastKindInfo)
	go func() {
		sent, kind, err := bugrepPostFunc(endpoint, r)
		tbxPost(func() {
			switch {
			case err == nil:
				trophyEvent(tkBugReport)
				// The collector works, so anything that waited for it goes now.
				bugrepCollectorAnswered(endpoint)
				notifPush(bugrepSentToast(sent), notifSuccess)
				logInfo("launch", "launch report %s sent (issue %d)", r.ID, sent.Issue)
			case kind == bugrepErrClock:
				if !bugrepClockWrong(time.Now()) {
					bugrepNoteSendFailed(time.Now())
				}
				launchReportHold(r, bugrepHoldClock)
			case kind == bugrepErrUnavailable:
				bugrepCollectorUnavailable()
				launchReportHold(r, bugrepHoldService)
			case kind == bugrepErrSetup:
				bugrepNoteServiceSetupPending(time.Now())
				launchReportHold(r, bugrepHoldSetup)
			case kind == bugrepErrTrust:
				bugrepCollectorUnavailable()
				launchReportHold(r, bugrepHoldTrust)
			case kind == bugrepErrRejected:
				// Sending it again cannot help: a copy is kept on the device.
				msg := "Bug report " + r.ID + " not sent: " + firstLine(err.Error())
				if path, cerr := bugrepSaveCopy(r); cerr == nil {
					msg += " — a copy is saved as " + bugrepBaseName(path) + " in Patch reports"
				}
				logWarn("launch", "launch report %s refused: %v", r.ID, err)
				notifPush(msg, notifError)
			case kind == bugrepErrServer:
				bugrepNoteSendFailed(time.Now())
				launchReportHold(r, bugrepHoldBusy)
			default:
				bugrepNoteSendFailed(time.Now())
				launchReportHold(r, bugrepHoldOffline)
			}
		})
	}()
}

// launchReportHold keeps a report that cannot go now in the outbox, which sends
// it on its own later (bugrepOutboxTick), and says why it waits. A report the
// outbox cannot take is saved as a readable copy instead, when it can be.
func launchReportHold(r bugrepReport, why bugrepHoldWhy) {
	if err := bugrepQueue(r); err != nil {
		logPatch("[bugreport] cannot queue launch report %s: %v", r.ID, err)
		if path, cerr := bugrepSaveCopy(r); cerr == nil {
			notifPush("Bug report "+r.ID+" could not be queued — it is saved as "+bugrepBaseName(path)+
				" in Patch reports", notifWarning)
			return
		}
		notifPush("Bug report "+r.ID+" could not be saved on this device", notifError)
		return
	}
	copied := false
	if why == bugrepHoldTrust {
		_, cerr := bugrepSaveCopy(r)
		copied = cerr == nil
	}
	trophyEvent(tkBugReport)
	msg, priority := bugrepQueuedNotice(why, copied)
	notifPush(msg+" (reference "+r.ID+")", priority)
}

// ── A launch that failed before its program ran ──────────────────────────

// launchAttempt is the record of the library launch under way (main thread):
// the launch path adds its command to it, and a failure before the start is
// reported with it.
var launchAttempt *launchRecord

// launchReportedError is a launch error the card shows already, with its
// report: a caller's toast for it is left out (gameLaunchErrorToast).
type launchReportedError struct{ err error }

func (e *launchReportedError) Error() string { return e.err.Error() }
func (e *launchReportedError) Unwrap() error { return e.err }

// launchRefusedError is an answer that is not a launch failing — a time limit,
// a check that is busy, "one moment", a console this device cannot run — so it
// stays a toast and is not reported.
type launchRefusedError struct{ err error }

func (e *launchRefusedError) Error() string { return e.err.Error() }
func (e *launchRefusedError) Unwrap() error { return e.err }

// launchRefusal marks err as a refusal (nil stays nil).
func launchRefusal(err error) error {
	if err == nil {
		return nil
	}
	return &launchRefusedError{err: err}
}

// launchErrorShown reports whether err is on the launch-failure card already.
func launchErrorShown(err error) bool {
	var shown *launchReportedError
	return errors.As(err, &shown)
}

// gameLaunchErrorToast is the toast a caller of launchLibraryGame shows for a
// game that did not start — unless the card shows it already, with the report.
func gameLaunchErrorToast(title string, err error) {
	if err == nil || launchErrorShown(err) {
		return
	}
	ShowToast("Cannot launch "+title+": "+firstLine(err.Error()), ToastKindError)
}

// launchLibraryFailed is a library launch that failed before its program ran:
// the card opens with the reason and the report, and the error comes back
// marked as shown. A refusal, a time limit and an error some other card shows
// are passed through untouched. Main thread.
func launchLibraryFailed(rec *launchRecord, err error) error {
	if err == nil || rec == nil || launchErrorShown(err) || errors.Is(err, errPlaytimeDailyLimit) {
		return err
	}
	var refused *launchRefusedError
	if errors.As(err, &refused) {
		return err
	}
	rec.Reason = err.Error()
	logWarn("launch", "%s did not start: %v", strconv.Quote(rec.Title), err)
	if launchFailure.open {
		// One card at a time: this one stays a toast and a log line.
		return err
	}
	showLaunchFailureRecord(rec, err, "")
	return &launchReportedError{err: err}
}

// ── The Emulators page ───────────────────────────────────────────────────

// The Emulators page's two kinds of start, as a report names them.
const (
	emuTileLaunchKind = "Emulator (Emulators page)"
	raMenuLaunchKind  = "RetroArch's own menu (Emulators page)"
)

// emuTileWatch is one start from the Emulators page — an emulator's tile, or
// RetroArch's own menu — watched like a library game (gamediag.go, exited):
// one that is over by itself within gameDiagBriefRun, or within
// gameDiagQuickExit with an error or a signal JukaHub did not send, gets the
// launch card with its report. Before this, the page threw the exit status
// away: an emulator that could not open showed "Launching …" and the page
// again.
type emuTileWatch struct {
	rec    *launchRecord
	diag   *gameDiagLaunch
	output *launcherOutput
	cmd    *exec.Cmd
}

// emuTileWatchBegin records cmd and keeps its output (gamediag/launcher.log),
// just before it starts. Main thread.
func emuTileWatchBegin(kind, name, path string, cmd *exec.Cmd) *emuTileWatch {
	rec := launchRecordNew(kind, name, path)
	d := &gameDiagLaunch{launcher: path, started: time.Now(), memMB: gameLaunchMemFreeMB(), captureAny: true}
	w := &emuTileWatch{rec: rec, diag: d, cmd: cmd}
	w.output = d.captureLauncherOutput(cmd)
	rec.OutPath = d.outPath
	rec.noteCommand(cmd)
	return w
}

// failed is a start that did not happen: the output file is closed and the
// card opens with the report. Main thread.
func (w *emuTileWatch) failed(err error) {
	if w == nil {
		return
	}
	w.output.started(false)
	w.rec.Reason = err.Error()
	logWarn("emulators", "launch failed for %s: %v", w.rec.Launcher, err)
	showLaunchFailureRecord(w.rec, err, "")
}

// started notes a program that is running. Main thread.
func (w *emuTileWatch) started() {
	if w == nil {
		return
	}
	w.output.started(true)
	w.rec.noteStarted(w.cmd, w.diag.memMB)
	w.diag.started = w.rec.Started
	gameLaunchWatchStart(w.cmd)
	logInfo("emulators", "starting %s: %s in %s, pid %d, %d MB free", strconv.Quote(w.rec.Title),
		gameLaunchCommandLine(w.cmd.Args), w.cmd.Dir, w.rec.Pid, w.diag.memMB)
}

// ended runs on the reaper goroutine once the program's process group is gone
// (after screenChildReaped): a start that did not get going is diagnosed from
// what it printed and handed to the card (gameDiagPost).
func (w *emuTileWatch) ended(waitErr error) {
	if w == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logWarn("emulators", "the launch check for %s failed: %v", w.rec.Title, r)
		}
	}()
	w.output.stopped()
	stopped := gameLaunchWatchEnd(w.cmd)
	w.rec.noteEnd(time.Now(), waitErr, stopped)
	ran := w.rec.Ended.Sub(w.rec.Started)
	how := w.rec.Exit
	if stopped {
		how += "; JukaHub had asked it to stop"
	}
	logInfo("emulators", "%s ended after %s: %s", strconv.Quote(w.rec.Title), ran.Round(100*time.Millisecond), how)
	if stopped || ran > gameDiagQuickExit+screenSettleDelay {
		return
	}
	code, sig := gameLaunchExitStatus(waitErr)
	brief := !IsWindows() && ran <= gameDiagBriefRun+screenSettleDelay
	if !brief && code == 0 && sig == 0 {
		return // a quick, clean exit: the player closed it
	}
	report := gameDiagReport{Title: w.rec.Title + " didn't start"}
	if lines := gameDiagReadTail(w.diag.outPath, time.Time{}); len(lines) > 0 {
		issues, excerpt := launcherOutputFindings(lines, w.rec.Launcher)
		if is, ok := raGlibcLauncherIssue(lines); ok {
			issues = append([]launchIssue{is}, issues...)
		}
		report.Issues = issues
		report.LogLines = excerpt
		report.LogPath = w.diag.outPath
	}
	secs := int(ran.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	when := "It closed after " + pluralizeCount(secs, "second", "seconds")
	switch {
	case sig > 0 && code < 0:
		when += ": " + gameDiagSignalNote(sig)
	case code != 0:
		when += " (" + w.rec.Exit + ")"
	}
	switch {
	case len(report.Issues) > 0:
		report.Summary = when + ". This is what stops it:"
	case len(report.LogLines) > 0:
		report.Summary = when + ". Its last lines are below."
	default:
		report.Summary = when + ", and it printed nothing."
	}
	report.Launch = w.rec
	report.Details = launchReportDetails(w.rec, report.Summary, report.Issues)
	logInfoDiag(report)
	gameDiagPost(report)
}
