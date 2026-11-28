package main

// hakux_handoff.go — an Xbox game gets the handheld to itself: JukaHub closes
// while hakuX plays and comes back when the game ends.
//
// A tester, 2026-11-21 (relayed by Juka): the first Xbox game "stuck in the
// menu" — JukaHub's screen stayed on the panel while the game's sound played
// — and "the controls for hotkeys only opened RetroArch menu and not the
// standalone hakuX emulator".
//
// The hakuX package (hakux.go) is a launcher made for the stock menu. There,
// MainUI queues the game and quits, and the firmware's loop runs
// Emus/XBOX/launch.sh with nothing else alive on the display; the launcher
// stops the firmware's quick menu and music service for the game and brings
// them back afterwards. Started from JukaHub, the same launcher ran beside a
// JukaHub that stays alive: its window still open on the framebuffer (it only
// stops presenting, gamesuspend.go), its pad watcher reading the buttons
// beside the game, and its memory taken from a 1 GB handheld on which hakuX
// wants all of it. hakuX does not draw through SDL's framebuffer path like
// RetroArch and the other emulators here — its own platform layer presents
// through the PowerVR's Vulkan driver — so the last picture JukaHub drew could
// stay on the panel over a game that was running, and Menu met the firmware's
// quick menu or JukaHub's RetroArch-minded in-game layer instead of hakuX's
// own settings.
//
// So an Xbox game started from JukaHub now goes the way PS Remote Play's and
// Moonlight's streams go (psremote.go; the screen-handoff protocol in
// launch.sh's header): JukaHub writes a short script, starts it in a session
// of its own, leaves the script's pid in .screen-handoff and quits. The script
// waits for JukaHub to be gone and runs the Xbox launcher exactly as MainUI's
// queue would — the firmware's environment (screenChildEnv), the game in
// MainUI's form (firmwareROMArg), the interpreter its #! line names
// (firmwareScriptCommand) — with its output in gamediag/launcher.log, kept
// under launcherLogCap. launch.sh keeps the stock menu stopped meanwhile and
// starts JukaHub again when the script ends: JukaHub's quick start, since a
// start in the same boot plays no boot animation. In the game hakuX's own
// controls are the only ones: Menu opens hakuX's settings, and holding Start
// + Select about 1.2 s quits back to JukaHub.
//
// Heavy-game boost (xboxboost.go) still applies, in two halves: before JukaHub
// quits it brings parked cores online and raises the GPU and memory clocks and
// the card's read-ahead — values that outlive it, and the crash marker it
// writes is what the next start reads to put them back (xboxBoostRecover) —
// and the script gives the CPU the performance governor once launch.sh has put
// JukaHub's CPU settings back, and the previous governor after the game.
//
// The return (hakuxHandoffReturn): JukaHub leaves a note in the temp folder
// before it quits — the game, its launcher, when it was started, whether from
// the Games Library — and the script adds when the game started and ended by
// the boot clock, its exit status and the steps it took. The next start reads
// them once: the time played goes into the game's playtime and history like a
// game played beside JukaHub, a game that ended within seconds with an error
// (or one the script never started) gets the launch card with the launcher's
// last lines (gamediag.go), and a game started from the Games Library opens
// it on the game again. A MainUI that came up with the game, or a quick menu
// where none ran before it, is put away by the script, so JukaHub comes back
// alone.
//
// Only where it can work: a TrimUI under a launch.sh that waits for a screen
// helper (JUKAHUB_SCREEN_HANDOFF). Anywhere else — a JukaHub started by hand,
// an older launch.sh, a script that cannot be written or started — the game
// starts beside JukaHub as before.
//
// PSP games take the same way once their picture did not reach the panel
// beside JukaHub (ppsspp_picture.go), or when the player chose it on the
// PPSSPP card: the note's Kind says which, for the log and the card. Their
// one-launch PPSSPP settings are written before JukaHub quits and put back at
// the return (emuspeed_ppsspp.go). PPSSPP's own menu, started with no game
// from the PSP tile, takes it too while they do (hakuxHandoffMenuLaunch): the
// note's Menu names the tile, the return opens the Emulators page on it, and
// nothing of a game's is kept.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// The hand-off's files, in the temp folder: RAM, so a power-off during
	// the game leaves nothing for the next boot to take for a return.
	hakuxHandoffScriptName = "jukahub-xbox-handoff.sh"
	hakuxHandoffNoteName   = "jukahub-xbox-handoff.json"
	hakuxHandoffStatusName = "jukahub-xbox-handoff.status"
	// hakuxHandoffNoteVersion is the note's layout.
	hakuxHandoffNoteVersion = 1
	// hakuxHandoffSignalled is the status the script records when a stop
	// signal ended it (the power key: launch.sh stops the helper's session).
	hakuxHandoffSignalled = 143
	// hakuxHandoffWaitFrames bounds how many posted looks the return waits
	// for the playtime ledger and the start-up questions (the lock, "Who is
	// playing?") before it lets go.
	hakuxHandoffWaitFrames = 600
)

// The kinds of game a hand-off is for: an Xbox game in hakuX, or a PSP game
// whose picture needs the panel to itself (ppsspp_picture.go). A note without
// a kind is an Xbox game's (the only kind before 2026-11-23).
const (
	handoffKindXbox = "xbox"
	handoffKindPSP  = "psp"
)

// handoffKindTag is the log tag of a kind.
func handoffKindTag(kind string) string {
	if kind == handoffKindPSP {
		return "ppsspp"
	}
	return "xbox"
}

// handoffKindEmulator names the emulator of a kind for the launch card.
func handoffKindEmulator(kind string) string {
	if kind == handoffKindPSP {
		return "PPSSPP"
	}
	return "hakuX"
}

// hakuxHandoffBegun is set once a hand-off has started this run: JukaHub is
// on its way out, and a second press must not start the game twice. Main
// thread.
var hakuxHandoffBegun bool

// hakuxHandoffPath is one of the hand-off's files.
func hakuxHandoffPath(name string) string {
	return filepath.Join(os.TempDir(), name)
}

// hakuxHandoffNote is what JukaHub leaves for its next start.
type hakuxHandoffNote struct {
	Version      int    `json:"version"`
	Path         string `json:"path"`
	System       string `json:"system"`
	Title        string `json:"title"`
	DisplayTitle string `json:"display_title,omitempty"`
	Launcher     string `json:"launcher"`
	// Log is the launcher's output (gamediag/launcher.log), "" when none.
	Log string `json:"log,omitempty"`
	// PlayedAt is the start the recents recorded: what the time played is
	// credited from (playtimeCreditGameGap matches it).
	PlayedAt time.Time `json:"played_at"`
	// FromLibrary: the Games Library was open at the start.
	FromLibrary bool `json:"from_library,omitempty"`
	// Script is the hand-off script's pid.
	Script int `json:"script_pid,omitempty"`
	// Kind is handoffKindXbox or handoffKindPSP ("" from before PSP games
	// took this way: an Xbox game).
	Kind string `json:"kind,omitempty"`
	// Menu is the Emulators page tile whose emulator's own menu this was,
	// started with no game (hakuxHandoffMenuLaunch); "" for a game.
	Menu string `json:"menu,omitempty"`
}

// game is the note's game, as the library names it.
func (n hakuxHandoffNote) game() libraryGame {
	return libraryGame{Path: n.Path, System: n.System, Title: n.Title, DisplayTitle: n.DisplayTitle}
}

// hakuxHandoffRun is what the script recorded: the boot clock (seconds) when
// the game started and ended, the launcher's exit status and the steps it
// took, in order.
type hakuxHandoffRun struct {
	Started, Ended   float64
	HasStart, HasEnd bool
	RC               int
	HasRC            bool
	Steps            []string
	// LowBattery: the battery watch closed the game, at LowBatteryPct;
	// Resume is the state JukaHub's PPSSPP build saved for it then.
	LowBattery    bool
	LowBatteryPct int
	Resume        string
}

// ran is how long the game ran, 0 when that is not known.
func (r hakuxHandoffRun) ran() time.Duration {
	if !r.HasStart || !r.HasEnd || r.Ended < r.Started {
		return 0
	}
	return time.Duration((r.Ended - r.Started) * float64(time.Second))
}

// lastStep is the last step the script recorded ("" when none).
func (r hakuxHandoffRun) lastStep() string {
	if len(r.Steps) == 0 {
		return ""
	}
	return r.Steps[len(r.Steps)-1]
}

// hakuxHandoffParseStatus reads the script's key=value lines: a later value
// wins, unknown keys and lines are skipped. Pure.
func hakuxHandoffParseStatus(data []byte) hakuxHandoffRun {
	var r hakuxHandoffRun
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "started", "ended":
			f, err := strconv.ParseFloat(value, 64)
			if err != nil || f < 0 {
				continue
			}
			if key == "started" {
				r.Started, r.HasStart = f, true
			} else {
				r.Ended, r.HasEnd = f, true
			}
		case "rc":
			if n, err := strconv.Atoi(value); err == nil {
				r.RC, r.HasRC = n, true
			}
		case "step":
			if value != "" && len(r.Steps) < 32 {
				r.Steps = append(r.Steps, value)
			}
		case "lowbattery":
			if n, err := strconv.Atoi(value); err == nil && n >= 0 && n <= 100 {
				r.LowBattery, r.LowBatteryPct = true, n
			}
		case "resume":
			if filepath.IsAbs(value) {
				r.Resume = filepath.Clean(value)
			}
		}
	}
	return r
}

// hakuxHandoffFailed reports a start that did not get going, by gamediag.go's
// rule for a library game JukaHub did not stop: the game never started, or it
// ended within gameDiagQuickExit with an error, or within gameDiagBriefRun
// whatever its status. One a stop signal ended is the power key, not a
// failure; one whose end was not recorded is not judged. Pure.
func hakuxHandoffFailed(r hakuxHandoffRun) bool {
	if !r.HasStart {
		return true
	}
	if !r.HasEnd || !r.HasRC || r.RC == hakuxHandoffSignalled {
		return false
	}
	ran := r.ran()
	if ran > gameDiagQuickExit {
		return false
	}
	return r.RC != 0 || ran <= gameDiagBriefRun
}

// ── The hand-off ────────────────────────────────────────────────────────────

// hakuxHandoffWanted reports whether game, started with launcher, is an Xbox
// game for the hand-off: the Xbox row's game, started by the shell launcher of
// an Xbox launcher folder (hakuxIsXboxFolder), on a TrimUI. Whether this
// launch.sh can take it is asked separately.
func hakuxHandoffWanted(game libraryGame, launcher string) bool {
	if IsWindows() || !IsTrimuiDevice() || !strings.EqualFold(strings.TrimSpace(game.System), emuXboxEntryName) {
		return false
	}
	launcher = strings.TrimSpace(launcher)
	if !launcherShellScript(launcher) {
		return false
	}
	dir := filepath.Dir(launcher)
	return hakuxIsXboxFolder(filepath.Base(dir), dir)
}

// hakuxHandoffKindFor is the kind of hand-off a start takes: an Xbox game's,
// a PSP game's that needs the panel to itself (ppssppHandoffWanted), or ""
// for a start beside JukaHub.
func hakuxHandoffKindFor(game libraryGame, launcher string) string {
	switch {
	case hakuxHandoffWanted(game, launcher):
		return handoffKindXbox
	case ppssppHandoffWanted(game, launcher):
		return handoffKindPSP
	}
	return ""
}

// hakuxHandoffLaunch hands the screen to an Xbox game, or to a PSP game that
// needs the panel to itself, and asks JukaHub to quit; launchLibraryGame calls
// it once every check before a start has passed. It reports whether it took
// the start (the caller returns then); false starts the game beside JukaHub as
// before, the reason in the log. Main thread.
func hakuxHandoffLaunch(game libraryGame, launcher string) bool {
	return hakuxHandoffStart(game, launcher, nil)
}

// hakuxHandoffMenu is a hand-off with no game: PPSSPP's own menu, from the PSP
// tile on the Emulators page (ppssppTileStart). Tile is that tile, which the
// return opens the page on; Args are what the launcher gets in place of a game.
type hakuxHandoffMenu struct {
	Tile string
	Args []string
}

// hakuxHandoffMenuLaunch is hakuxHandoffLaunch for PPSSPP's own menu, started
// with no game from the PSP tile: while PSP games get the panel to themselves,
// so does the menu, whose picture reaches the panel beside JukaHub no more
// than a game's. Nothing of a game's is kept for it — recents, playtime, save
// sync, resume. Main thread.
func hakuxHandoffMenuLaunch(tile, launcher string, args []string) bool {
	game := libraryGame{System: ppssppLibrarySystem, Title: tile}
	return hakuxHandoffStart(game, launcher, &hakuxHandoffMenu{Tile: tile, Args: args})
}

// hakuxHandoffStart is hakuxHandoffLaunch, and with menu set
// hakuxHandoffMenuLaunch. Main thread.
func hakuxHandoffStart(game libraryGame, launcher string, menu *hakuxHandoffMenu) bool {
	kind := hakuxHandoffKindFor(game, launcher)
	if kind == "" {
		return false
	}
	tag := handoffKindTag(kind)
	title := hakuxGameTitle(game)
	if hakuxHandoffBegun {
		// JukaHub is already closing for a game: a second press is spent.
		return true
	}
	if !psLauncherWaitsForHandoff() {
		logInfo(tag, "%s starts beside JukaHub: this launch.sh does not wait for a screen helper", title)
		return false
	}
	marker := psScreenHandoffMarkerPath()
	if marker == "" {
		logInfo(tag, "%s starts beside JukaHub: JukaHub's folder is unknown", title)
		return false
	}
	saveTo := ""
	if kind == handoffKindPSP {
		// What a start beside JukaHub does to JukaHub's PPSSPP first: the
		// shared HOME seeded (the opaque picture shader too), the launcher
		// current, the PSP's speed level in its ini. That level goes back at
		// the return (hakuxHandoffReturn).
		ppssppReadyManagedLaunch(launcher)
		if menu == nil {
			emuSpeedPrepare(launcher, game)
			// Where JukaHub's PPSSPP build saves on request, for the battery
			// watch below (ppsspp_resume.go).
			saveTo = ppssppResumeTargetFor(launcher, game.Path)
		}
	}
	// The command MainUI's queue would run, as launchLibraryGame builds it; a
	// menu's launcher gets what its tile gives in place of a game.
	var name string
	var args []string
	if menu != nil {
		name, args = "sh", append([]string{launcher}, menu.Args...)
	} else {
		romArg := firmwareROMArg(launcher, game.Path)
		extra := hotseatLaunchArgs(launcher, parseGameArgs(gameOptionsFor(game.Path).Args))
		if len(extra) > 0 && !hotseatLauncherForwards(launcher) {
			extra = nil
		}
		name, args = gameLaunchCommandArgs(launcher, romArg, extra)
	}
	name, args = firmwareScriptCommand(launcher, name, args)
	logPath := ""
	if dir := gameDiagLogDir(); dir != "" && os.MkdirAll(dir, 0o755) == nil {
		logPath = filepath.Join(dir, launcherLogName)
	}
	boost := menu == nil && xboxBoostSupported() && xboxBoostEnabled(appConfig) && xboxBoostKindForGame(game) != xboxBoostNone
	scriptPath := hakuxHandoffPath(hakuxHandoffScriptName)
	statusPath := hakuxHandoffPath(hakuxHandoffStatusName)
	notePath := hakuxHandoffPath(hakuxHandoffNoteName)
	spec := hakuxHandoffSpec{
		PID:     os.Getpid(),
		Dir:     filepath.Dir(launcher),
		Command: append([]string{name}, args...),
		Status:  statusPath,
		Log:     logPath,
		Title:   title,
		Boost:   boost,
		SaveTo:  saveTo,
	}
	// The battery watch stands in for the low-battery guard, which closes
	// with JukaHub (instantresume.go): at the guard's save level, read from
	// the battery's own capacity file.
	if lowBatteryGuardEnabled(appConfig) {
		if dir := batterySysfsDir(); dir != "" && regularFile(filepath.Join(dir, "capacity")) {
			spec.Battery, spec.Supplies, spec.SavePct = dir, powerSupplyRoot, batteryWarnNow().savePct()
		}
	}
	script := hakuxHandoffScript(spec)
	// An earlier hand-off's files are not this one's.
	_ = os.Remove(statusPath)
	_ = os.Remove(notePath)
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		logWarn(tag, "%s starts beside JukaHub: the hand-off script could not be written (%v)", title, err)
		return false
	}
	cmd := hiddenCommand("sh", "--", scriptPath)
	cmd.Dir = filepath.Dir(launcher)
	// The firmware's environment, as screenChildGroupConfigure gives a game
	// started beside JukaHub; with a Bluetooth headset routed, the game's
	// sound goes there too (JukaHub, the PCM's one client, is gone by then).
	cmd.Env = screenChildEnv(os.Environ())
	if pcm := audioRouteChildPCM(); pcm != "" {
		if _, set := childEnvLookup(cmd.Env, "AUDIODEV"); !set {
			cmd.Env = append(cmd.Env, "AUDIODEV="+pcm)
		}
	}
	if kind == handoffKindPSP {
		// The file a save request writes, and the state JukaHub saved for
		// this game, as a start beside JukaHub gets them (ppsspp_resume.go).
		cmd.Env, _ = ppssppResumeEnv(launcher, game.Path, cmd.Env)
	}
	configureChiakiDetached(cmd)
	if err := cmd.Start(); err != nil {
		_ = os.Remove(scriptPath)
		logWarn(tag, "%s starts beside JukaHub: the hand-off script did not start (%v)", title, err)
		return false
	}
	pid := cmd.Process.Pid
	// Without the marker launch.sh would give the screen to the stock menu as
	// JukaHub quits, and never bring JukaHub back: no marker, no hand-off.
	if err := os.WriteFile(marker, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = os.Remove(scriptPath)
		logWarn(tag, "%s starts beside JukaHub: the screen hand-off could not be recorded (%v)", title, err)
		return false
	}
	go func() { _ = cmd.Wait() }()
	hakuxHandoffBegun = true
	note := hakuxHandoffNote{
		Version:      hakuxHandoffNoteVersion,
		Path:         game.Path,
		System:       game.System,
		Title:        game.Title,
		DisplayTitle: game.DisplayTitle,
		Launcher:     launcher,
		Log:          logPath,
		Script:       pid,
		Kind:         kind,
	}
	if menu != nil {
		note.Menu = menu.Tile
	} else {
		// A record of an earlier save of this game is this start's now, and a
		// power-on resume marker written in this boot is superseded, as at a
		// start beside JukaHub (sessionsafe.go, instantresume.go). Before
		// JukaHub quits, not on a goroutine.
		sessionSafeNoteStart(game.Path)
		resumeDropSameBootMarker()

		// The start counts now — the recents, the game's launches, the
		// Continue card — and the next start credits the time played
		// (hakuxHandoffReturn).
		gmetaNoteLaunch(game)
		if recents := recordGameLaunch(game); len(recents) > 0 && recents[0].Path == game.Path {
			note.PlayedAt = recents[0].PlayedAt
		}
		note.FromLibrary = gameLib.open
	}
	if data, err := json.Marshal(note); err != nil {
		logWarn(tag, "the note for the return from %s was not written: %v", title, err)
	} else if err := os.WriteFile(notePath, data, 0o600); err != nil {
		logWarn(tag, "the note for the return from %s was not written: %v", title, err)
	}
	if boost {
		hakuxHandoffBoost(game)
	}
	logInfo(tag, "%s gets the screen to itself: hand-off script pid %d runs %s once JukaHub has closed, and launch.sh starts JukaHub again when the game ends",
		title, pid, strings.Join(append([]string{name}, args...), " "))
	if menu != nil {
		ShowToast(title+" is opening — JukaHub closes while it is open and comes back after", ToastKindInfo)
	} else {
		ShowToast(title+" is starting — JukaHub closes while it plays and comes back after", ToastKindInfo)
	}
	requestAppExit()
	return true
}

// hakuxHandoffBoost is Heavy-game boost's hardware half for a game JukaHub
// quits for, made before it quits: parked cores online, the GPU's and the
// memory controller's clocks up, the card's read-ahead raised
// (xboxBoostHardwarePlan). Nothing puts them back at the exit — the session is
// never the active one (xboxBoostShutdown) — and the crash marker applyLocked
// writes is what the next start, after the game, reads to put back what is
// still as written (xboxBoostRecover). Main thread.
func hakuxHandoffBoost(game libraryGame) {
	label := hakuxGameTitle(game)
	xboxBoost.mu.Lock()
	busy := xboxBoost.active != nil
	prev := xboxBoost.pending
	if !busy {
		xboxBoost.pending = nil
	}
	xboxBoost.mu.Unlock()
	if busy {
		return
	}
	s := &xboxBoostSession{kind: xboxBoostGame, target: game.Path, label: label, begun: time.Now()}
	xboxBoostOps.Lock()
	// The last boosted game's restore first: it would remove this marker.
	prev.restoreLocked()
	s.applyLocked(xboxBoostHardwarePlan(game.Path))
	xboxBoostOps.Unlock()
	s.mu.Lock()
	line := strings.Join(s.details, "; ")
	s.mu.Unlock()
	if line != "" {
		logInfo("xbox", "boost for %s while JukaHub is closed: %s; the next start puts it back", label, line)
	}
}

// ── The script ──────────────────────────────────────────────────────────────

// hakuxHandoffSpec is what one hand-off script is written for.
type hakuxHandoffSpec struct {
	PID     int      // JukaHub, which the script waits for
	Dir     string   // the launcher's folder, where it runs
	Command []string // the launcher's command line, as MainUI's queue runs it
	Status  string   // the status file the next start reads
	Log     string   // the launcher's output ("" for none)
	Title   string   // the game, for the status lines
	Boost   bool     // the performance governor while the game runs
	// The battery watch (instantresume.go): the battery's sysfs folder, the
	// power-supply class it sits in and the save level (0: no watch), and
	// the file JukaHub's PPSSPP build saves on request ("" for none).
	Battery  string
	Supplies string
	SavePct  int
	SaveTo   string
}

// hakuxHandoffScript writes the hand-off script: the values, quoted for sh,
// then hakuxHandoffShell. The launcher's command line becomes the script's
// positional parameters, one quoted word each, so no name on the card is ever
// read as shell.
func hakuxHandoffScript(spec hakuxHandoffSpec) string {
	logPath := spec.Log
	if strings.TrimSpace(logPath) == "" {
		logPath = "/dev/null"
	}
	// The title goes into status lines: no line breaks in it.
	title := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, spec.Title)
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# Generated by JukaHub — hands the screen to a game (hakux_handoff.go). Do not edit.\n")
	fmt.Fprintf(&b, "JUKAHUB_PID=%d\n", spec.PID)
	fmt.Fprintf(&b, "LAUNCH_DIR=%s\n", psShellQuote(spec.Dir))
	fmt.Fprintf(&b, "STATUS=%s\n", psShellQuote(spec.Status))
	fmt.Fprintf(&b, "LOG=%s\n", psShellQuote(logPath))
	fmt.Fprintf(&b, "LOG_CAP=%d\n", launcherLogCap)
	fmt.Fprintf(&b, "TITLE=%s\n", psShellQuote(title))
	if spec.Boost {
		b.WriteString("BOOST=1\n")
	} else {
		b.WriteString("BOOST=0\n")
	}
	savePct := spec.SavePct
	if spec.Battery == "" || savePct < 0 {
		savePct = 0
	}
	fmt.Fprintf(&b, "BATTERY=%s\n", psShellQuote(spec.Battery))
	fmt.Fprintf(&b, "SUPPLIES=%s\n", psShellQuote(spec.Supplies))
	fmt.Fprintf(&b, "SAVE_PCT=%d\n", savePct)
	fmt.Fprintf(&b, "SAVE_TO=%s\n", psShellQuote(spec.SaveTo))
	fmt.Fprintf(&b, "RESUME_FLAG=%s\n", psShellQuote(ppssppResumeFlagName))
	fmt.Fprintf(&b, "RESUME_CAP=%s\n", psShellQuote(ppssppResumeCapability))
	b.WriteString("set --")
	for _, a := range spec.Command {
		b.WriteString(" " + psShellQuote(a))
	}
	b.WriteString("\n")
	b.WriteString(hakuxHandoffShell)
	return b.String()
}

// hakuxHandoffShell is the script's body (POSIX sh, BusyBox's as well). Its
// steps go to the status file only, so the launcher's log holds the
// launcher's own lines for the card; the next start writes them into
// JukaHub's log.
const hakuxHandoffShell = `
step() {
    echo "step=$*" >> "$STATUS" 2>/dev/null
}
# The boot clock, which the network cannot move: what the game is measured by.
uptime_now() {
    UN_UP=""
    read -r UN_UP UN_REST < /proc/uptime 2>/dev/null
    echo "${UN_UP:-0}"
}
uptime_secs() {
    US_UP="$(uptime_now)"
    US_UP="${US_UP%.*}"
    case "$US_UP" in "" | *[!0-9]*) US_UP=0 ;; esac
    echo "$US_UP"
}
# Heavy-game boost's governor: every cpufreq policy that offers performance,
# put back after the game exactly as it was.
TUNE_CPU=""
tune_cpu_dirs() {
    TD_ANY=""
    for TD_D in /sys/devices/system/cpu/cpufreq/policy[0-9]*; do
        [ -d "$TD_D" ] && { echo "$TD_D"; TD_ANY=1; }
    done
    [ -n "$TD_ANY" ] && return 0
    for TD_D in /sys/devices/system/cpu/cpu[0-9]*/cpufreq; do
        [ -d "$TD_D" ] && echo "$TD_D"
    done
    return 0
}
tune_apply() {
    for TA_D in $(tune_cpu_dirs); do
        TA_G="$(cat "$TA_D/scaling_governor" 2>/dev/null)"
        case "$TA_G" in "" | performance | *[!A-Za-z0-9_]*) continue ;; esac
        case " $(cat "$TA_D/scaling_available_governors" 2>/dev/null) " in
            *" performance "*) ;;
            *) continue ;;
        esac
        # Noted before the write: a signal between the two still puts it back.
        TUNE_CPU="$TUNE_CPU$TA_D $TA_G
"
        { echo performance > "$TA_D/scaling_governor"; } 2>/dev/null
        [ "$(cat "$TA_D/scaling_governor" 2>/dev/null)" = performance ] || continue
        step "CPU ${TA_D#/sys/devices/system/cpu/}: governor $TA_G -> performance"
    done
    return 0
}
tune_restore() {
    while read -r TR_D TR_G; do
        [ -n "$TR_D" ] || continue
        { echo "$TR_G" > "$TR_D/scaling_governor"; } 2>/dev/null
        step "CPU ${TR_D#/sys/devices/system/cpu/}: governor back to $TR_G"
    done <<EOF
$TUNE_CPU
EOF
    TUNE_CPU=""
    return 0
}
# The firmware's quick menu, by its own binary (the list launch.sh checks).
osd_pids() {
    for OP_P in $(pidof trimui_osdd 2>/dev/null || pgrep -x trimui_osdd 2>/dev/null); do
        case "$(readlink "/proc/$OP_P/exe" 2>/dev/null)" in
            /usr/trimui/osd/trimui_osdd | /mnt/SDCARD/trimui/osd/trimui_osdd | /mnt/UDISK/trimui/osd/trimui_osdd)
                echo "$OP_P"
                ;;
        esac
    done
}
# Never the stock menu on the panel with the game, or with JukaHub coming back.
# pidof can name several MainUIs: one argument per pid.
stop_mainui() {
    SM_PID="$(pidof MainUI 2>/dev/null)"
    [ -n "$SM_PID" ] || return 0
    step "a MainUI was running (pid $SM_PID); closed it"
    kill $SM_PID 2>/dev/null
    sleep 1
    SM_PID="$(pidof MainUI 2>/dev/null)"
    [ -n "$SM_PID" ] && kill -9 $SM_PID 2>/dev/null
    return 0
}
# ── The battery watch (instantresume.go) ──────────────────────────────────
# JukaHub's low-battery guard closed with JukaHub, so this stands in for it: at
# the guard's save level, off the charger, two looks running (20 s apart),
# JukaHub's PPSSPP build is asked for a save state (SIGUSR1, only a program
# with the flag file beside it), then the game is asked to quit — the emulator
# first, its launch scripts after — and the card is synced. JukaHub, started
# again, keeps the state for the next start and powers off unless a charger
# came (hakuxHandoffReturn). A game that will not quit is never killed: the
# handheld is suspended under it instead.
BATT_PID=""
BATT_BUSY="$STATUS.battery"
battery_pct() {
    BP_V=""
    { read -r BP_V < "$BATTERY/capacity"; } 2>/dev/null
    case "$BP_V" in "" | *[!0-9]*) BP_V="" ;; esac
    echo "$BP_V"
}
# On external power: the battery says Charging or Full, or a charger of the
# power-supply class is online (batteryChargingIn's rule, simplified).
on_charger() {
    OC_S=""
    { read -r OC_S < "$BATTERY/status"; } 2>/dev/null
    case "$OC_S" in Charging | Full) return 0 ;; esac
    for OC_D in "$SUPPLIES"/*; do
        [ "$OC_D" = "$BATTERY" ] && continue
        OC_T=""
        { read -r OC_T < "$OC_D/type"; } 2>/dev/null
        [ "$OC_T" = Battery ] && continue
        OC_O=""
        { read -r OC_O < "$OC_D/online"; } 2>/dev/null
        [ "$OC_O" = 1 ] && return 0
    done
    return 1
}
# The game's processes: every live descendant of this script but the watches
# (GP_SKIP), from /proc/*/stat — "pid (comm) state ppid ...", where the comm
# may hold spaces, so the fields after its last ")" are read.
game_pids() {
    GP_ALL=""
    for GP_F in /proc/[0-9]*/stat; do
        GP_L=""
        { read -r GP_L < "$GP_F"; } 2>/dev/null
        [ -n "$GP_L" ] || continue
        GP_PID="${GP_L%% *}"
        set -- ${GP_L##*) }
        [ "$#" -ge 2 ] || continue
        [ "$1" = Z ] && continue
        GP_ALL="$GP_ALL $GP_PID:$2"
    done
    GP_IN=" $$ "
    GP_OUT=""
    GP_MORE=1
    while [ "$GP_MORE" = 1 ]; do
        GP_MORE=0
        for GP_E in $GP_ALL; do
            GP_P="${GP_E%%:*}"
            case "$GP_IN" in *" $GP_P "*) continue ;; esac
            case "$GP_IN" in *" ${GP_E#*:} "*) ;; *) continue ;; esac
            case " $GP_SKIP " in *" $GP_P "*) continue ;; esac
            GP_IN="$GP_IN$GP_P "
            GP_OUT="$GP_OUT $GP_P"
            GP_MORE=1
        done
    done
    echo "$GP_OUT"
}
# A shell runs a launch script, never the game itself.
is_shell() {
    IS_E="$(readlink "/proc/$1/exe" 2>/dev/null)"
    case "${IS_E##*/}" in sh | bash | ash | dash | busybox) return 0 ;; esac
    return 1
}
# JukaHub's PPSSPP build among the game's processes: a PPSSPP program with the
# save-on-request flag file beside it (SIGUSR1 ends any other).
saver_pid() {
    for SV_P in $(game_pids); do
        SV_E="$(readlink "/proc/$SV_P/exe" 2>/dev/null)"
        case "${SV_E##*/}" in PPSSPP* | ppsspp*) ;; *) continue ;; esac
        if grep -qxF -- "$RESUME_CAP" "${SV_E%/*}/$RESUME_FLAG" 2>/dev/null; then
            echo "$SV_P"
            return 0
        fi
    done
    return 1
}
# wait_gone SECONDS: until none of the game's processes is left.
wait_gone() {
    WG_I=0
    while [ -n "$(game_pids)" ]; do
        [ "$WG_I" -ge "$1" ] && return 1
        sleep 1
        WG_I=$((WG_I + 1))
    done
    return 0
}
# battery_stop PERCENT: save where the build can, close the game, sync.
battery_stop() {
    : > "$BATT_BUSY" 2>/dev/null
    echo "lowbattery=$1" >> "$STATUS" 2>/dev/null
    step "battery at $1% off the charger: saving and closing $TITLE"
    BS_P=""
    [ -n "$SAVE_TO" ] && BS_P="$(saver_pid)"
    if [ -n "$BS_P" ]; then
        rm -f "$SAVE_TO.done" 2>/dev/null
        kill -CONT "$BS_P" 2>/dev/null
        if kill -USR1 "$BS_P" 2>/dev/null; then
            BS_I=0
            BS_MAX=80
            [ "$NAP" = 1 ] && BS_MAX=8
            while [ "$BS_I" -lt "$BS_MAX" ] && [ ! -s "$SAVE_TO.done" ]; do
                sleep "$NAP"
                BS_I=$((BS_I + 1))
            done
            BS_A=""
            { read -r BS_A < "$SAVE_TO.done"; } 2>/dev/null
            BS_S1="$(wc -c < "$SAVE_TO" 2>/dev/null)"
            sleep 1
            BS_S2="$(wc -c < "$SAVE_TO" 2>/dev/null)"
            if [ "$BS_A" = ok ] && [ -s "$SAVE_TO" ] && [ -n "$BS_S1" ] && [ "$BS_S1" = "$BS_S2" ]; then
                echo "resume=$SAVE_TO" >> "$STATUS" 2>/dev/null
                step "PPSSPP saved $TITLE for its next start"
            else
                step "PPSSPP did not save $TITLE (${BS_A:-no answer})"
            fi
        fi
    fi
    # The emulator first: its launch script then ends by itself.
    for BS_P in $(game_pids); do
        is_shell "$BS_P" && continue
        kill -TERM "$BS_P" 2>/dev/null
        kill -CONT "$BS_P" 2>/dev/null
    done
    if ! wait_gone 8; then
        for BS_P in $(game_pids); do
            kill -TERM "$BS_P" 2>/dev/null
            kill -CONT "$BS_P" 2>/dev/null
        done
        wait_gone 4
    fi
    sync
    if [ -n "$(game_pids)" ]; then
        step "$TITLE did not quit: the card is synced and the handheld suspended under it"
        { echo mem > /sys/power/state; } 2>/dev/null || { echo freeze > /sys/power/state; } 2>/dev/null
    else
        step "$TITLE closed and the card synced"
    fi
    rm -f "$BATT_BUSY" 2>/dev/null
    return 0
}
battery_watch() {
    SELF=""
    { read -r SELF BW_REST < /proc/self/stat; } 2>/dev/null
    GP_SKIP="$SELF $WATCH_PID"
    BW_LOW=0
    while kill -0 "$$" 2>/dev/null; do
        sleep 20
        BW_PCT="$(battery_pct)"
        if [ -z "$BW_PCT" ] || [ "$BW_PCT" -gt "$SAVE_PCT" ] || on_charger; then
            BW_LOW=0
            continue
        fi
        BW_LOW=$((BW_LOW + 1))
        [ "$BW_LOW" -ge 2 ] || continue
        battery_stop "$BW_PCT"
        return 0
    done
    return 0
}
# The watch ends with the game; one still closing it is waited for, so its
# sync is not cut short.
battery_watch_end() {
    [ -n "$BATT_PID" ] || return 0
    if [ -f "$BATT_BUSY" ]; then
        wait "$BATT_PID" 2>/dev/null
    else
        kill "$BATT_PID" 2>/dev/null
    fi
    BATT_PID=""
    rm -f "$BATT_BUSY" 2>/dev/null
    return 0
}
WATCH_PID=""
finish() {
    [ -n "$WATCH_PID" ] && kill "$WATCH_PID" 2>/dev/null
    WATCH_PID=""
    battery_watch_end
    tune_restore
}
trap '' HUP
trap 'echo "ended=$(uptime_now)" >> "$STATUS" 2>/dev/null; echo "rc=143" >> "$STATUS" 2>/dev/null; finish; step "stopped by a signal"; rm -f "$0" 2>/dev/null; exit 143' TERM INT

echo "version=1" > "$STATUS" 2>/dev/null
# A log that can be written, emptied for this game; else none. The probe runs
# in a subshell: a failed redirect on ":" ends a POSIX shell outright.
mkdir -p "$(dirname "$LOG")" 2>/dev/null
if ! (: > "$LOG") 2>/dev/null; then
    LOG=/dev/null
fi
step "waiting for JukaHub (pid $JUKAHUB_PID) to close"
NAP=0.1
LIMIT=600
sleep 0.1 2>/dev/null || { NAP=1; LIMIT=60; }
i=0
while kill -0 "$JUKAHUB_PID" 2>/dev/null; do
    i=$((i + 1))
    [ "$i" -ge "$LIMIT" ] && break
    sleep "$NAP"
done
if kill -0 "$JUKAHUB_PID" 2>/dev/null; then
    step "JukaHub did not close within a minute; the game was not started over it"
    rm -f "$0" 2>/dev/null
    exit 0
fi
CLOSED="$(uptime_secs)"
# launch.sh puts back the CPU settings JukaHub found as it exits; the governor
# goes on after that.
sleep 1
stop_mainui
OSD_BEFORE="$(osd_pids)"
[ "$BOOST" = 1 ] && tune_apply
step "starting $TITLE"
echo "started=$(uptime_now)" >> "$STATUS" 2>/dev/null
# The launcher's output stays under LOG_CAP; the watch ends with this script.
if [ "$LOG" != /dev/null ]; then
    (
        while kill -0 "$$" 2>/dev/null; do
            sleep 30
            LW_SIZE="$(wc -c < "$LOG" 2>/dev/null)"
            case "$LW_SIZE" in "" | *[!0-9]*) continue ;; esac
            [ "$LW_SIZE" -gt "$LOG_CAP" ] && : > "$LOG"
        done
    ) </dev/null >/dev/null 2>&1 &
    WATCH_PID=$!
fi
if [ "$SAVE_PCT" -gt 0 ] 2>/dev/null && [ -n "$BATTERY" ]; then
    rm -f "$BATT_BUSY" 2>/dev/null
    battery_watch </dev/null >/dev/null 2>&1 &
    BATT_PID=$!
fi
(
    cd "$LAUNCH_DIR" 2>/dev/null
    exec "$@"
) >> "$LOG" 2>&1
RC=$?
echo "ended=$(uptime_now)" >> "$STATUS" 2>/dev/null
echo "rc=$RC" >> "$STATUS" 2>/dev/null
finish
step "$TITLE ended (exit code $RC)"
stop_mainui
if [ -z "$OSD_BEFORE" ]; then
    for P in $(osd_pids); do
        kill -KILL "$P" 2>/dev/null && step "closed the quick menu the game left running (pid $P)"
    done
fi
# launch.sh looks for this script right after JukaHub exits: a game that ended
# at once must not take the script with it before launch.sh is waiting.
NOW="$(uptime_secs)"
if [ $((NOW - CLOSED)) -lt 3 ]; then
    sleep 3
fi
rm -f "$0" 2>/dev/null
exit 0
`

// ── The return ──────────────────────────────────────────────────────────────

func init() {
	// After the first frame, like PS Remote Play's last-run note: the card and
	// the library need the render loop (startup.go).
	deferStartupTask("xbox-handoff-return", hakuxHandoffReturn)
}

// hakuxHandoffScriptRunning reports whether pid is a hand-off script still
// running: the game still is (a JukaHub started by hand beside it).
func hakuxHandoffScriptRunning(pid int) bool {
	if pid <= 1 {
		return false
	}
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	return err == nil && bytes.Contains(raw, []byte(hakuxHandoffScriptName))
}

// hakuxHandoffClear removes the hand-off's files.
func hakuxHandoffClear() {
	for _, name := range []string{hakuxHandoffNoteName, hakuxHandoffStatusName, hakuxHandoffScriptName} {
		_ = os.Remove(hakuxHandoffPath(name))
	}
}

// hakuxHandoffReturn reads what the last hand-off left, once, at the start
// after it (the package header). Main thread (the deferred start-up queue).
func hakuxHandoffReturn() {
	if IsWindows() {
		return
	}
	data, err := os.ReadFile(hakuxHandoffPath(hakuxHandoffNoteName))
	if err != nil {
		return
	}
	var note hakuxHandoffNote
	if json.Unmarshal(data, &note) != nil || note.Version != hakuxHandoffNoteVersion ||
		(strings.TrimSpace(note.Path) == "" && note.Menu == "") {
		hakuxHandoffClear()
		return
	}
	tag := handoffKindTag(note.Kind)
	if hakuxHandoffScriptRunning(note.Script) {
		logInfo(tag, "the game JukaHub closed for is still running (hand-off script pid %d); its note waits", note.Script)
		return
	}
	status, _ := os.ReadFile(hakuxHandoffPath(hakuxHandoffStatusName))
	run := hakuxHandoffParseStatus(status)
	hakuxHandoffClear()
	game := note.game()
	title := hakuxGameTitle(game)
	steps := strings.Join(run.Steps, " · ")
	ran := run.ran()
	switch {
	case !run.HasStart:
		logWarn(tag, "%s was not started after JukaHub closed for it: %s", title, steps)
	case !run.HasEnd:
		logWarn(tag, "%s started, but its end was not recorded: %s", title, steps)
	default:
		logInfo(tag, "back from %s after %s (exit code %d): %s", title, ran.Round(time.Second), run.RC, steps)
	}
	if note.Kind == handoffKindPSP {
		// The PSP's one-launch PPSSPP settings go back, as the Exited hook
		// puts them back after a game beside JukaHub (emuspeed_ppsspp.go).
		go emuSpeedPPSSPPRestore()
	}
	if run.HasStart && note.Menu == "" {
		// Its saves go up to Google Drive / RomM, as after any game
		// (savesync_auto.go).
		saveSyncAfterExit(game.Path, game.System)
	}
	if ran > 0 {
		hakuxHandoffCredit(note, ran, 0)
	}
	if hakuxHandoffFailed(run) {
		go hakuxHandoffDiagnose(note, run)
	}
	switch {
	case note.FromLibrary:
		hakuxHandoffReopen(game, 0)
	case note.Menu != "":
		hakuxHandoffReopenTile(note.Menu, 0)
	}
	if run.LowBattery {
		// The battery watch ended the game: its state is kept for the next
		// start, and the device powers off unless a charger came
		// (instantresume.go).
		resumeHandoffBattery(note, run)
	}
}

// hakuxHandoffCredit counts the time played like a game played beside JukaHub
// (playtimeCreditGameGap, gameHistoryFinishHandoff): from the start the
// recents recorded, for as long as the script measured, when that start is
// still the newest. It waits a few frames for the playtime ledger to load.
// Main thread.
func hakuxHandoffCredit(note hakuxHandoffNote, ran time.Duration, tries int) {
	if note.PlayedAt.IsZero() {
		return
	}
	if !playtime.loaded {
		if tries < hakuxHandoffWaitFrames {
			requestFrameEvery(50)
			tbxPost(func() { hakuxHandoffCredit(note, ran, tries+1) })
		}
		return
	}
	recents := recentGames()
	if len(recents) == 0 || recents[0].Path != note.Path || !recents[0].PlayedAt.Equal(note.PlayedAt) {
		logInfo(handoffKindTag(note.Kind), "time in %s not counted: another game was started since", filepath.Base(note.Path))
		return
	}
	start := recents[0].PlayedAt
	end := start.Add(ran)
	gameHistoryFinishHandoff(recents, start, end)
	playtimeCreditGameGap(start, end)
}

// hakuxHandoffDiagnose builds the launch card for a start that did not get
// going: the game's own checks (gamediag.go), the launcher's last lines and
// what the script saw. Its own goroutine: it reads the card.
func hakuxHandoffDiagnose(note hakuxHandoffNote, run hakuxHandoffRun) {
	defer func() {
		if r := recover(); r != nil {
			logWarn(handoffKindTag(note.Kind), "the card for the last game JukaHub closed for failed: %v", r)
		}
	}()
	emu := handoffKindEmulator(note.Kind)
	game := note.game()
	// An emulator's own menu had no game to check (hakuxHandoffMenuLaunch).
	report := gameDiagReport{Title: game.Label() + " didn't start"}
	what := "Hand-off game (" + emu + ")"
	if note.Menu == "" {
		report, _ = diagnoseLibraryGame(game, note.Launcher, "")
	} else {
		what = emuTileLaunchKind + ", with the screen to itself"
	}
	if strings.TrimSpace(note.Log) != "" {
		d := &gameDiagLaunch{game: game, launcher: note.Launcher, outPath: note.Log}
		d.addLauncherOutput(&report)
	}
	// The launch as the hand-off script recorded it, for the card's bug report
	// (launchreport.go). JukaHub was closed while it ran, so its log is this
	// start's.
	rec := launchRecordNew(what, game.Label(), note.Launcher)
	rec.System, rec.ROM, rec.OutPath = game.System, game.Path, note.Log
	if !note.PlayedAt.IsZero() {
		rec.Started = note.PlayedAt
	}
	rec.logMark = 0
	rec.logTitle = "JukaHub log since it started again (it was closed while the game ran)"
	if note.Script > 0 {
		rec.Notes = append(rec.Notes, "hand-off script pid "+strconv.Itoa(note.Script))
	}
	if len(run.Steps) > 0 {
		rec.Notes = append(rec.Notes, "hand-off steps: "+strings.Join(run.Steps, " → "))
	}
	if !run.HasStart {
		why := run.lastStep()
		if why == "" {
			why = "the hand-off script never ran"
		}
		report.Summary = "JukaHub closed to give the game the screen, but the game was not started: " + why + "."
		rec.Reason = "the game was not started: " + why
		report.Launch = rec
		report.Details = launchReportDetails(rec, report.Summary, report.Issues)
		logInfoDiag(report)
		gameDiagPost(report)
		return
	}
	if run.HasRC {
		rec.Exit = "exit status " + strconv.Itoa(run.RC)
	} else {
		rec.Exit = "status unknown (the hand-off script recorded none)"
	}
	if ran := run.ran(); ran > 0 && !rec.Started.IsZero() {
		rec.Ended = rec.Started.Add(ran)
	}
	secs := int(run.ran().Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	when := "It closed after " + pluralizeCount(secs, "second", "seconds")
	if run.RC > 0 {
		when += " (exit code " + strconv.Itoa(run.RC) + ")"
	}
	switch {
	case len(report.Issues) > 0:
		report.Summary = when + ". This is what stops it:"
	case len(report.LogLines) > 0:
		report.Summary = when + ". JukaHub found nothing missing that it can check; " + emu + "'s launcher's last lines are below."
	default:
		report.Summary = when + ". JukaHub found nothing missing that it can check, and " + emu + "'s launcher printed nothing."
	}
	report.Launch = rec
	report.Details = launchReportDetails(rec, report.Summary, report.Issues)
	logInfoDiag(report)
	gameDiagPost(report)
}

// hakuxHandoffReopen opens the Games Library on the game again for a game
// started from it, once the start-up questions are answered (the lock, the
// first-run tour, "Who is playing?"). Main thread.
func hakuxHandoffReopen(game libraryGame, tries int) {
	if exitRequested.Load() || gameScreenSuspended() {
		return
	}
	if startupLocked() || onboarding.active || profilesPicker.sheet.open {
		if tries < hakuxHandoffWaitFrames {
			requestFrameEvery(50)
			tbxPost(func() { hakuxHandoffReopen(game, tries+1) })
		}
		return
	}
	openGameLibraryOnSystem(game.System, "")
	if !gameLib.scanning {
		jumpToLibraryGame(game)
	}
}

// hakuxHandoffReopenTile opens the Emulators page on the tile an emulator's
// own menu was started from (hakuxHandoffMenuLaunch), once the start-up
// questions are answered. Main thread.
func hakuxHandoffReopenTile(tile string, tries int) {
	if exitRequested.Load() || gameScreenSuspended() {
		return
	}
	if startupLocked() || onboarding.active || profilesPicker.sheet.open {
		if tries < hakuxHandoffWaitFrames {
			requestFrameEvery(50)
			tbxPost(func() { hakuxHandoffReopenTile(tile, tries+1) })
		}
		return
	}
	openEmulatorPanel()
	entries := emulatorEntries()
	for i, e := range entries {
		if e.Name == tile {
			emuPanelSelect(entries, i)
			return
		}
	}
}
