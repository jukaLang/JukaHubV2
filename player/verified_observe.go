package main

// verified_observe.go — what JukaHub saw work during real sessions, written to
// the Verified store (verified.go) as automatic ("auto") results.
//
// Every screen-taking child is a session: a library game or port, named by
// gameLaunchLogStart (gamelaunch_watch.go), or — when nothing names it within
// vfoClaimWindow — an app, named after the program or script it runs.
// PortMaster's own menu and JukaHub itself are neither. One goroutine watches
// a session from its start (noteGameChildStarted) to the end of its process
// group (screenChildReaped) and records what it saw:
//
//   - launch: pass once it has run vfoLaunchPass. Fail when it ended by itself
//     within gameDiagQuickExit the way the quick-exit card reads "it did not
//     start" (gamediag.go): a port whatever its status (not GreenOvercast,
//     which may be left at once on purpose), a game with an error status, a
//     signal JukaHub did not send, or over within gameDiagBriefRun, an app
//     with an error status or a signal.
//   - return: pass when a session that got going came back by itself with
//     status 0 (a port: anything but a crash, as its status proves nothing) or
//     quit from the in-game menu, and its whole process group was gone
//     without a kill. Fail when it needed Select + Start held, ignored the
//     in-game menu's Quit until it was killed, kept its group running past
//     screenGroupWatch, or crashed. A stop JukaHub sent for its own reasons
//     (sleep's power-off, low battery, quitting JukaHub) records nothing.
//   - sleep: after a suspend of at least vfoSleepMin, pass when it is still
//     running vfoWakeWatch after the wake; fail when it ended by itself in
//     that time.
//   - sound (Linux): pass when an ALSA playback stream of its own process
//     group is seen running (verified_observe_linux.go).
//   - controls: pass when the built-in pad sent at least vfoControlsPresses
//     presses (counted by the game-time pad reader, gameassist_linux.go) in a
//     session of at least vfoControlsAfter.
//
// Sound and controls never record a fail: not seeing a stream or presses
// proves nothing. A game's pass is also noted on its emulator's record
// ("seen with …"), so the Emulators list shows what works here.
//
// The hooks are one line each in shared files and never block: the store is
// written from the session's goroutine, which is also where the launch
// script and the core's checksum cache are read to name the engine. The
// session is counted (vfBeginSession) just before its first result, or when
// it ends: by then the launch's own core note (coreLaunchNote) has put the
// core's checksum in its cache, so naming the build reads nothing big.

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// vfoClaimWindow is how long a screen child waits to be named a game or
	// port before it counts as an app. Both launch paths name it right after
	// it started, on the same call, and before its reaper exists, so a game
	// that ends is always named first.
	vfoClaimWindow = 10 * time.Second
	// vfoLaunchPass is how long a session runs before it counts as started.
	vfoLaunchPass = 20 * time.Second
	// vfoTick paces the checks a session polls (launch, sound, controls).
	vfoTick = 5 * time.Second
	// vfoControlsAfter and vfoControlsPresses: a session this long in which
	// the built-in pad sent this many presses was played with it.
	vfoControlsAfter   = time.Minute
	vfoControlsPresses = 15
	// vfoSleepMin is the shortest suspend the sleep check counts, and
	// vfoWakeWatch how long after the wake the session must keep running.
	vfoSleepMin  = 5 * time.Second
	vfoWakeWatch = 10 * time.Second
	// vfoPortLogTail bounds the port log read for a Go program's crash.
	vfoPortLogTail = 16 << 10
)

// vfoPadPresses counts the built-in pad's presses while a game or app has the
// screen (vfoNotePadEvent); a session keeps the count it started at.
var vfoPadPresses atomic.Int64

// vfoSessions is every screen child being watched, by its command.
var vfoSessions struct {
	mu    sync.Mutex
	byCmd map[*exec.Cmd]*vfoSession
}

// vfoSession is one screen child. The fields up to woke are set when it is
// made; the rest are guarded by vfoSessions.mu.
type vfoSession struct {
	cmd     *exec.Cmd
	pid     int
	grouped bool
	started time.Time
	padBase int64
	claimed chan struct{} // closed when gameLaunchLogStart names it
	ended   chan struct{} // closed when its process group is gone
	woke    chan time.Duration

	isGame   bool
	game     libraryGame
	launcher string
	// decided: the goroutine settled what the session is; a later name is
	// ignored.
	decided bool
	// stopAsked: JukaHub asked it to stop; playerQuit: from the in-game menu.
	stopAsked  bool
	playerQuit bool
	// forced says why JukaHub had to end it ("" when it did not).
	forced string
	// over: its process group is gone; what follows is how it ended.
	over      bool
	endAt     time.Time
	lingered  bool
	exitKnown bool
	code      int
	sig       int
}

// vfoEnd is how a session ended, copied out of the session under the lock.
type vfoEnd struct {
	stopAsked  bool
	playerQuit bool
	forced     string
	endAt      time.Time
	lingered   bool
	exitKnown  bool
	code       int
	sig        int
}

// vfoWatch is the session's goroutine's own state.
type vfoWatch struct {
	s *vfoSession
	// What the results are recorded on, and the emulator a game ran on: its
	// name for the emulator's record, and with its version for the results.
	kind, id, title    string
	engineName, engine string
	// x1: GreenOvercast, which may be left at once on purpose.
	x1 bool
	// What the engine is read from, when the session is counted.
	launcher, system string
	args             []string
	begun            bool
	launched         bool
	sound            bool
	controls         bool
	// wakeAt is when the last wake that this session lived through came,
	// until vfoWakeWatch passed; slept is how long it slept.
	wakeAt time.Time
	slept  time.Duration
}

func init() {
	gameLaunchRegisterStarted(vfoLaunchStarted)
	sleepHookRegisterBefore(vfoBeforeSleep)
	sleepHookRegisterWake(vfoAfterWake)
}

// ── hooks ─────────────────────────────────────────────────────────────────

// vfoChildStarted: a screen child started (noteGameChildStarted).
func vfoChildStarted(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	vfoSessionFor(cmd)
}

// vfoLaunchStarted: a library game's process started; its launcher is what
// names the engine (gameLaunchRegisterStarted). Main thread.
func vfoLaunchStarted(_ libraryGame, launcher string, cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	s := vfoSessionFor(cmd)
	vfoSessions.mu.Lock()
	s.launcher = launcher
	vfoSessions.mu.Unlock()
}

// vfoGameStarted: the child is a library game or a port (gameLaunchLogStart).
func vfoGameStarted(game libraryGame, cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	s := vfoSessionFor(cmd)
	vfoSessions.mu.Lock()
	claim := !s.isGame && !s.decided
	if claim {
		s.isGame, s.game = true, game
	}
	late := !claim && !s.isGame
	vfoSessions.mu.Unlock()
	if claim {
		close(s.claimed)
	}
	if late {
		logInfo("verified", "%s was named after its session began; it is kept as an app", strconv.Quote(game.Label()))
	}
}

// vfoNoteStop: JukaHub asked cmd to stop (gameLaunchNoteStop). From the
// in-game menu's Quit or Select + Start held, gmQuitting is already set.
func vfoNoteStop(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	quit := gmQuitting.Load()
	vfoSessions.mu.Lock()
	if s, ok := vfoSessions.byCmd[cmd]; ok {
		s.noteStopLocked(quit)
	}
	vfoSessions.mu.Unlock()
}

// vfoNoteStopAll: JukaHub asked every running game to stop at once
// (gameLaunchNoteStopAll: low battery, the session-safe quit).
func vfoNoteStopAll() {
	quit := gmQuitting.Load()
	vfoSessions.mu.Lock()
	for _, s := range vfoSessions.byCmd {
		s.noteStopLocked(quit)
	}
	vfoSessions.mu.Unlock()
}

// noteStopLocked keeps the first stop's reason.
func (s *vfoSession) noteStopLocked(quit bool) {
	if s.over || s.stopAsked {
		return
	}
	s.stopAsked, s.playerQuit = true, quit
}

// vfoNoteForced: JukaHub had to end cmd — Select + Start held
// (gameForceQuit), or a Quit it ignored until the kill (gmQuitWatch). The
// first reason is kept.
func vfoNoteForced(cmd *exec.Cmd, why string) {
	if cmd == nil {
		return
	}
	vfoSessions.mu.Lock()
	if s, ok := vfoSessions.byCmd[cmd]; ok && !s.over && s.forced == "" {
		s.forced = why
	}
	vfoSessions.mu.Unlock()
}

// vfoChildReaped: cmd's process group is gone, or screenGroupWatch passed
// with it still running (screenChildReaped, on the reaper goroutine after
// cmd.Wait, so cmd.ProcessState is there to read). Never blocks.
func vfoChildReaped(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	lingered := screenChildGroupAlive(cmd)
	code, sig, known := 0, 0, false
	if ps := cmd.ProcessState; ps != nil {
		known = true
		if !ps.Success() {
			code, sig = gameLaunchExitStatus(&exec.ExitError{ProcessState: ps})
		}
	}
	vfoSessions.mu.Lock()
	s, ok := vfoSessions.byCmd[cmd]
	if ok && !s.over {
		s.over, s.endAt, s.lingered = true, time.Now(), lingered
		s.exitKnown, s.code, s.sig = known, code, sig
	} else {
		ok = false
	}
	vfoSessions.mu.Unlock()
	if ok {
		close(s.ended)
	}
}

// vfoNotePadEvent counts a press of the built-in pad while a game has the
// screen: a button going down or the D-pad leaving the middle. Presses while
// JukaHub holds the pad for its overlay are the overlay's. The pad watcher's
// goroutine (gameassist_linux.go).
func vfoNotePadEvent(ev evdevEvent, held bool) {
	if held {
		return
	}
	switch ev.Type {
	case evdevEvKey:
		if ev.Value == 1 {
			vfoPadPresses.Add(1)
		}
	case evdevEvAbs:
		if (ev.Code == evdevAbsHat0X || ev.Code == evdevAbsHat0Y) && ev.Value != 0 {
			vfoPadPresses.Add(1)
		}
	}
}

// vfoBeforeSleep writes what is waiting to be written, in case the handheld
// never wakes (the power-off deadline, a flat battery). The write runs beside
// the sleep, so the before-sleep budget is not spent on the card.
func vfoBeforeSleep(string) {
	go func() { _ = vfFlush() }()
}

// vfoAfterWake starts the sleep check of every running session after a real
// suspend; not when the wake is turning the handheld off.
func vfoAfterWake(_ string, slept time.Duration, suspended bool) {
	if !suspended || slept < vfoSleepMin || quickPowerOffSilent() || exitRequested.Load() {
		return
	}
	vfoSessions.mu.Lock()
	for _, s := range vfoSessions.byCmd {
		if s.over {
			continue
		}
		// Only this sends, under the lock: after the drain the send fits.
		select {
		case <-s.woke:
		default:
		}
		select {
		case s.woke <- slept:
		default:
		}
	}
	vfoSessions.mu.Unlock()
}

// ── the session ───────────────────────────────────────────────────────────

// vfoSessionFor finds cmd's session, or makes it and starts its goroutine.
func vfoSessionFor(cmd *exec.Cmd) *vfoSession {
	vfoSessions.mu.Lock()
	s, ok := vfoSessions.byCmd[cmd]
	if !ok {
		s = &vfoSession{
			cmd: cmd, pid: cmd.Process.Pid, grouped: screenChildGroupConfigured(cmd),
			started: time.Now(), padBase: vfoPadPresses.Load(),
			claimed: make(chan struct{}), ended: make(chan struct{}), woke: make(chan time.Duration, 1),
		}
		if vfoSessions.byCmd == nil {
			vfoSessions.byCmd = make(map[*exec.Cmd]*vfoSession, 2)
		}
		vfoSessions.byCmd[cmd] = s
	}
	vfoSessions.mu.Unlock()
	if !ok {
		// The store's first read starts now, off this thread, so the first
		// result rarely waits for it.
		vfEnsureLoaded()
		go s.run()
	}
	return s
}

// run is the session's goroutine: it settles what the session is, then
// watches it until its process group is gone.
func (s *vfoSession) run() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("verified", "watching a session failed: %v", r)
		}
		vfoSessions.mu.Lock()
		delete(vfoSessions.byCmd, s.cmd)
		vfoSessions.mu.Unlock()
	}()
	claimWait := time.NewTimer(vfoClaimWindow)
	select {
	case <-s.claimed:
	case <-s.ended:
	case <-claimWait.C:
	}
	claimWait.Stop()
	vfoSessions.mu.Lock()
	s.decided = true
	isGame, game, launcher := s.isGame, s.game, s.launcher
	vfoSessions.mu.Unlock()
	w := &vfoWatch{s: s}
	if !w.settle(isGame, game, launcher) {
		<-s.ended
		return
	}
	w.loop()
}

// settle names what the session is; false when it is not watched
// (PortMaster's own menu, JukaHub itself, a command with no program).
func (w *vfoWatch) settle(isGame bool, game libraryGame, launcher string) bool {
	if isGame {
		w.kind, w.id, w.title = vfGameRef(game)
		if w.kind == vfKindPort {
			w.x1 = x1PortName(filepath.Base(game.Path))
			return true
		}
		if launcher == "" {
			launcher = vfoAppPath(w.s.cmd)
		}
		w.launcher, w.system, w.args = launcher, game.System, w.s.cmd.Args
		return w.id != ""
	}
	path := vfoAppPath(w.s.cmd)
	if path == "" || vfoOwnProgram(path) || vfoPortMasterMenu(path) {
		return false
	}
	// PortMaster through the firmware's Ports launcher (`sh
	// Emus/PORTS/launch.sh …/PortMaster.sh`) is named by its argument.
	for _, a := range w.s.cmd.Args {
		if vfoPortMasterMenu(a) {
			return false
		}
	}
	if launcherRetroArchName(path) {
		// ra64.trimui is RetroArch to anyone reading the list.
		path = "RetroArch"
	}
	w.kind, w.id, w.title = vfAppRef(path)
	return w.id != ""
}

// loop polls the session's checks every vfoTick until they are all decided,
// runs the sleep check after each wake, and records the end.
func (w *vfoWatch) loop() {
	tick := time.NewTicker(vfoTick)
	defer tick.Stop()
	tickC := tick.C
	var wake *time.Timer
	var wakeC <-chan time.Time
	defer func() {
		if wake != nil {
			wake.Stop()
		}
	}()
	for {
		select {
		case <-w.s.ended:
			w.finish()
			return
		case slept := <-w.s.woke:
			w.wakeAt, w.slept = time.Now(), slept
			if wake != nil {
				wake.Stop()
			}
			wake = time.NewTimer(vfoWakeWatch)
			wakeC = wake.C
		case <-wakeC:
			wakeC = nil
			select {
			case <-w.s.ended:
				// Over in the same moment: the end decides the sleep check.
				w.finish()
				return
			default:
			}
			if !w.wakeAt.IsZero() && !powerSleepActive() {
				w.note(vfCheckSleep, vfPass, "kept running after a "+vfoSpan(w.slept)+" sleep")
			}
			w.wakeAt = time.Time{}
		case <-tickC:
			if powerSleepActive() {
				continue
			}
			if w.poll() {
				tick.Stop()
				tickC = nil
			}
		}
	}
}

// poll runs the checks that can pass while the session runs, and reports
// whether none is left.
func (w *vfoWatch) poll() bool {
	ran := time.Since(w.s.started)
	if !w.launched && ran >= vfoLaunchPass {
		w.launched = true
		w.note(vfCheckLaunch, vfPass, "started and kept running")
	}
	if !w.sound && vfoSoundSupported() && vfoSoundSeen(w.s.pid, w.s.grouped) {
		w.sound = true
		w.note(vfCheckSound, vfPass, "audio stream seen")
	}
	if !w.controls && gameAssistSupported() {
		w.checkControls(ran)
	}
	return w.launched && (w.sound || !vfoSoundSupported()) && (w.controls || !gameAssistSupported())
}

// checkControls passes controls once the session is long enough and the pad
// was pressed enough.
func (w *vfoWatch) checkControls(ran time.Duration) {
	presses := vfoPadPresses.Load() - w.s.padBase
	if ran < vfoControlsAfter || presses < vfoControlsPresses {
		return
	}
	w.controls = true
	w.note(vfCheckControls, vfPass, strconv.FormatInt(presses, 10)+" button presses in "+vfoSpan(ran))
}

// finish records what the end of the session says.
func (w *vfoWatch) finish() {
	vfoSessions.mu.Lock()
	s := w.s
	f := vfoEnd{
		stopAsked: s.stopAsked, playerQuit: s.playerQuit, forced: s.forced, endAt: s.endAt,
		lingered: s.lingered, exitKnown: s.exitKnown, code: s.code, sig: s.sig,
	}
	vfoSessions.mu.Unlock()
	w.ensureBegun()
	ran := f.endAt.Sub(s.started)
	if ran < 0 {
		ran = 0
	}
	crash, how := w.crashed(f)
	span := vfoSpan(ran)

	// launch
	switch {
	case w.launched || ran >= vfoLaunchPass:
		w.launched = true
		w.note(vfCheckLaunch, vfPass, "ran "+span)
	case !f.stopAsked && ran <= gameDiagQuickExit && w.quickFail(f, ran):
		w.note(vfCheckLaunch, vfFail, vfoEndedWords(crash, f, span))
	}

	// return
	switch {
	case f.forced != "":
		w.note(vfCheckReturn, vfFail, f.forced)
	case f.lingered:
		w.note(vfCheckReturn, vfFail, "left programs running after it ended")
	case crash && f.stopAsked:
		w.note(vfCheckReturn, vfFail, "crashed while quitting ("+how+")")
	case crash && ran > gameDiagQuickExit:
		w.note(vfCheckReturn, vfFail, "crashed after "+span+" ("+how+")")
	case crash, !w.launched:
		// It never got going: the launch result says so.
	case f.stopAsked && !f.playerQuit:
		// JukaHub stopped it for its own reasons: sleep's power-off, low
		// battery, quitting JukaHub.
	case f.playerQuit:
		w.note(vfCheckReturn, vfPass, "quit from the in-game menu after "+span)
	case f.exitKnown && ((f.code == 0 && f.sig == 0) || w.kind == vfKindPort):
		w.note(vfCheckReturn, vfPass, "came back to JukaHub after "+span)
	}

	// sleep: it ended by itself soon after a wake
	if !w.wakeAt.IsZero() && !f.stopAsked {
		if after := f.endAt.Sub(w.wakeAt); after >= 0 && after <= vfoWakeWatch {
			note := "stopped " + vfoSpan(after) + " after waking"
			if words := vfoExitWords(f.code, f.sig); words != "" {
				note += " (" + words + ")"
			}
			w.note(vfCheckSleep, vfFail, note)
		}
	}

	// controls, for a session that ended between two polls
	if !w.controls && gameAssistSupported() {
		w.checkControls(ran)
	}
}

// crashed reports whether the session died by a crash, and how in words. A
// signal JukaHub sends (an interrupt, a kill) is not a crash; a kill it did
// not send is the system's, on a handheld nearly always memory running out.
func (w *vfoWatch) crashed(f vfoEnd) (bool, string) {
	if !f.exitKnown {
		return false, ""
	}
	switch f.sig {
	case 4, 6, 7, 8, 11:
		return true, vfoExitWords(f.code, f.sig)
	case 9:
		if !f.stopAsked {
			return true, vfoExitWords(f.code, f.sig)
		}
	}
	// Windows reports a crash as an NTSTATUS error code (0xC0000005…).
	if IsWindows() && f.code > 0 && uint32(f.code)&0xC0000000 == 0xC0000000 {
		return true, "exit code 0x" + strconv.FormatUint(uint64(uint32(f.code)), 16)
	}
	// A Go program's panic exits with status 2 and says so on stderr: for a
	// port that is its log (ports_env.go).
	if f.code == 2 && w.kind == vfKindPort && !f.stopAsked && vfoGoPanicLogged(w.s) {
		return true, "a Go panic, exit code 2"
	}
	return false, ""
}

// quickFail reports whether a session over within gameDiagQuickExit that
// JukaHub did not stop never got going, by gamediag.go's rule.
func (w *vfoWatch) quickFail(f vfoEnd, ran time.Duration) bool {
	if !f.exitKnown {
		return false
	}
	switch w.kind {
	case vfKindPort:
		// A port's status proves nothing; one gone this soon never started.
		return !w.x1
	case vfKindGame:
		return f.sig > 0 || f.code > 0 || (!IsWindows() && ran <= gameDiagBriefRun)
	}
	// An app may be a quick tool that is done at once: only an error counts.
	return f.sig > 0 || f.code > 0
}

// note records one result on the session's record and, for a game's pass, on
// its emulator's; one log line says what was recorded.
func (w *vfoWatch) note(check, state, note string) {
	w.ensureBegun()
	changed := vfNote(w.kind, w.id, w.title, check, state, vfByAuto, note, w.engine)
	also := ""
	if state == vfPass && w.kind == vfKindGame && w.engineName != "" {
		ek, eid, etitle := vfEmulatorRef(w.engineName)
		if vfNote(ek, eid, etitle, check, vfPass, vfByAuto, "seen with "+w.title, w.engine) {
			also = "; also on " + w.engineName
		}
	}
	if changed || also != "" {
		logInfo("verified", "%s %s: %s %s (%s)%s", w.kind, strconv.Quote(w.title), check, state, note, also)
	}
}

// ensureBegun counts the session once, with its engine named.
func (w *vfoWatch) ensureBegun() {
	if w.begun {
		return
	}
	w.begun = true
	if w.kind == vfKindGame {
		w.engineName, w.engine = vfoEngineOf(w.launcher, w.system, w.args)
	}
	vfBeginSession(w.kind, w.id, w.title, w.engine)
}

// ── naming ────────────────────────────────────────────────────────────────

// vfoEngineOf names what a game ran on: the libretro core ("mGBA", with the
// build JukaHub installed when it knows it), RetroArch when the core is not
// known, or the emulator a launcher starts ("PSP" for Emus/PSP/launch.sh,
// "Mupen64plus standalone" for mupen64plus_standalone.sh, "mGBA" for
// mGBA.exe). It returns the name alone (the emulator's record) and with the
// version (the results). Reads the launch script: off the main thread.
func vfoEngineOf(launcher, system string, args []string) (name, full string) {
	corePath := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-L" {
			corePath = args[i+1]
			break
		}
	}
	ext := strings.ToLower(filepath.Ext(launcher))
	core, version := "", ""
	switch {
	case corePath != "":
		core, version = parseCoreFile(filepath.Base(corePath))
	case launcher == "":
		return "", ""
	case ext == ".so" || ext == ".dll":
		corePath = launcher
		core, version = parseCoreFile(filepath.Base(launcher))
	case retroArchLauncher(launcher):
		core = gameCoreFromLauncher(launcher, system)
	default:
		name = vfoLauncherName(launcher)
		return name, name
	}
	core = normalizeCoreName(core)
	if core == "" {
		return "RetroArch", "RetroArch"
	}
	name = coreConfigDisplayName(core)
	if version == "" || version == "unversioned" {
		version = vfoCoreBuild(core, corePath)
	}
	if version == "" {
		return name, name
	}
	return name, name + " (" + version + ")"
}

// vfoLauncherName names a launcher that is not RetroArch's: a firmware
// launch.sh by its folder (a NextUI pak without ".pak"), another script by its
// file name, a program by its own name.
func vfoLauncherName(launcher string) string {
	base := filepath.Base(launcher)
	if strings.EqualFold(base, "launch.sh") {
		dir := filepath.Base(filepath.Dir(launcher))
		if dir != "" && dir != "." && dir != string(filepath.Separator) {
			return strings.TrimSuffix(dir, ".pak")
		}
	}
	if strings.EqualFold(filepath.Ext(base), ".sh") {
		return emuAltNameFromScript(base)
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// vfoCoreBuild is the build of core JukaHub installed (its core history's
// label for the active binary's checksum), "" for the card's own build or
// when it is not known. The checksum comes from the cache coreLaunchNote
// filled at the launch.
func vfoCoreBuild(core, corePath string) string {
	history := coreHistoryFor(core)
	if len(history) == 0 {
		return ""
	}
	if corePath == "" {
		corePath = coreActiveBinary(core, retroArchCoreDirs())
	}
	if corePath == "" {
		return ""
	}
	crc, err := emuCachedCRC32(corePath)
	if err != nil || crc == "" {
		return ""
	}
	for i := len(history) - 1; i >= 0; i-- {
		if strings.EqualFold(history[i].CRC32, crc) {
			return history[i].Build
		}
	}
	return ""
}

// vfoWrappers are programs that run another file: an app is named after the
// file they were given.
var vfoWrappers = map[string]bool{
	"sh": true, "bash": true, "ash": true, "dash": true, "zsh": true, "busybox": true,
	"env": true, "nice": true, "ionice": true, "taskset": true, "chrt": true, "setsid": true,
	"timeout": true, "rundll32": true, "cmd": true, "wine": true, "wine64": true,
	"box64": true, "box86": true, "python": true, "python3": true, "java": true,
}

func vfoWrapper(path string) bool {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(path, "\\", "/")))
	return vfoWrappers[strings.TrimSuffix(base, ".exe")]
}

// vfoAppPath is what an app is named after: for a wrapper (`sh -- launch.sh`,
// `box64 wine64 game.exe`), the first operand that is a file and not another
// wrapper; otherwise the program itself. A wrapper given no file (`sh -c …`,
// a Windows batch file, whose path cmd.exe reads from its own command line)
// names nothing: every such app would share the wrapper's one record. It
// stats a few paths.
func vfoAppPath(cmd *exec.Cmd) string {
	if cmd == nil {
		return ""
	}
	prog, args := cmd.Path, cmd.Args
	if prog == "" && len(args) > 0 {
		prog = args[0]
	}
	// JukaHub's own mount-namespace start (ra_glibc.go, portmaster_binbash.go)
	// is `sh <setup script> mnt:[…] <program> <its arguments>`: the app is
	// that program, not the setup script.
	if vfoWrapper(prog) && len(args) >= 4 && strings.HasPrefix(args[2], "mnt:[") {
		prog, args = args[3], args[3:]
	}
	if !vfoWrapper(prog) {
		return prog
	}
	for _, a := range args[min(1, len(args)):] {
		if a == "" || strings.HasPrefix(a, "-") || vfoWrapper(a) {
			continue
		}
		p := a
		if !filepath.IsAbs(p) && cmd.Dir != "" {
			p = filepath.Join(cmd.Dir, p)
		}
		if regularFile(p) {
			return p
		}
	}
	return ""
}

// vfoOwnProgram reports whether path is JukaHub's own program.
func vfoOwnProgram(path string) bool {
	self, err := os.Executable()
	if err != nil || self == "" {
		return false
	}
	a, b := filepath.Clean(self), filepath.Clean(path)
	if IsWindows() {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// vfoPortMasterMenu reports whether path is PortMaster's own menu (or the
// copy of its launcher JukaHub runs): not an app, and not a port.
func vfoPortMasterMenu(path string) bool {
	p := strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	stem := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	return stem == "portmaster" || strings.Contains(p, "/portmaster/") ||
		filepath.Base(p) == pmShadowLauncherName // pmMasterLauncher's copy
}

// vfoGoPanicLogged reports whether a port's log ends with a Go program's
// crash report.
func vfoGoPanicLogged(s *vfoSession) bool {
	vfoSessions.mu.Lock()
	game := s.game
	vfoSessions.mu.Unlock()
	path := portLogPath(game.Title, game.Path)
	if path == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > vfoPortLogTail {
		if _, err := f.Seek(info.Size()-vfoPortLogTail, io.SeekStart); err != nil {
			return false
		}
	}
	tail, err := io.ReadAll(io.LimitReader(f, vfoPortLogTail))
	if err != nil {
		return false
	}
	text := string(tail)
	return strings.Contains(text, "goroutine ") &&
		(strings.Contains(text, "panic: ") || strings.Contains(text, "fatal error: "))
}

// ── words ─────────────────────────────────────────────────────────────────

// vfoSpan is a duration for a note: "3 s", "12 min", "1 h 5 min".
func vfoSpan(d time.Duration) string {
	switch {
	case d < time.Second:
		return "1 s"
	case d < time.Minute:
		return strconv.Itoa(int(d.Round(time.Second)/time.Second)) + " s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Round(time.Minute)/time.Minute)) + " min"
	}
	d = d.Round(time.Minute)
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	if m == 0 {
		return strconv.Itoa(h) + " h"
	}
	return strconv.Itoa(h) + " h " + strconv.Itoa(m) + " min"
}

// vfoExitWords is how a process ended, for a note: "exit code 139",
// "signal 11, SIGSEGV"; "" for a clean or unknown end.
func vfoExitWords(code, sig int) string {
	switch {
	case sig > 0 && code < 0:
		return "signal " + strconv.Itoa(sig) + ", " + gameDiagSignalName(sig)
	case code > 0:
		return "exit code " + strconv.Itoa(code)
	}
	return ""
}

// vfoEndedWords is a launch fail's note: "crashed after 2 s (signal 11,
// SIGSEGV)", "stopped after 2 s (exit code 1)", "ended after 1 s".
func vfoEndedWords(crash bool, f vfoEnd, span string) string {
	words := vfoExitWords(f.code, f.sig)
	switch {
	case crash && words != "":
		return "crashed after " + span + " (" + words + ")"
	case words != "":
		return "stopped after " + span + " (" + words + ")"
	}
	return "ended by itself after " + span
}
