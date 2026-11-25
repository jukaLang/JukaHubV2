package main

// instantresume.go — universal instant resume (PWR-3): press power, and the
// game is where it was, whatever runs it.
//
// That is the Switch's and the Steam Deck's promise, and JukaHub's own sleep
// keeps it for every game already (powersleep.go: the game stopped in RAM,
// the card synced, the device suspended, the game continued on the wake).
// What broke it was what ends a session without the player: the battery at
// its save level (lowbattery.go), the sleep's own power off (Power off after
// sleeping, quickresume.go) and Power off with a game running. Those saved
// and resumed RetroArch only; a PSP game in PPSSPP, a DS game in DraStic, a
// port, a Windows program under Wine or an Android game was closed, or the
// battery ran out under it.
//
// Each running game now belongs to a family (resumeFamilyOf), and the family
// decides what each of the three does to it (resumeActionFor; both pure and
// tested):
//
//   - RetroArch with its network commands: a state is saved (SAVE_STATE,
//     QUIT) and RetroArch's auto-load puts it back, at the next power-on or
//     the game's next start;
//   - JukaHub's own PPSSPP build: a state is saved on request (SIGUSR1, the
//     build's one change, player/native/build-ppsspp.sh) into JukaHub's data
//     folder, never into one of the player's slots, and the game's next start
//     loads it with PPSSPP's own --state= (ppsspp_resume.go);
//   - a standalone emulator whose own "save state on exit" is on
//     (sessionsafe.go): it saves on its way out, and resumes where it loads
//     that state by itself;
//   - whatever cannot save a state — a port, Wine, hakuX, an Android game,
//     Java, an emulator with that option off, RetroArch with its network
//     commands off — is held instead of closed: at the battery's save level
//     it stays stopped in RAM while JukaHub's sleep takes the device, with the
//     power-off time near (saveLedgerSleepCap: ten minutes at 5% or less). A
//     charger and a press bring it back exactly where it was; without them
//     the sleep's power off asks it to quit (its own saves flushed) and powers
//     the device off before the battery dies. It is never killed mid-write.
//     One hold per low-battery episode: woken without a charger, it has
//     lowBatteryHoldGrace to find one, then the guard closes it as before;
//   - a stream from another machine is closed: the game goes on over there;
//   - an app, not a game, is left as it is: closing it could lose its work.
//
// A state saved at the battery's save level now also writes the power-on
// resume marker (resume-at-power-on.json), as the two power offs did, so a
// battery that runs out after the save still brings the game back at the
// next power-on. A marker written in this boot is dropped when another game
// starts, so a power-on never brings back an older game over the last one.
//
// A game JukaHub closes for (an Xbox game, a PSP game that needs the panel to
// itself: hakux_handoff.go) plays while JukaHub, and its guard, are gone. Its
// hand-off script watches the battery itself and does the same at the save
// level — JukaHub's PPSSPP is asked for its state, the game asked to quit,
// the card synced — and JukaHub, started again after it, keeps the state for
// the next start and powers off unless a charger came (resumeHandoffBattery).
//
// Nothing here was run on a device from this desk.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// resumeFamily is what can be done for a running game.
type resumeFamily int

const (
	// resumeFamilyNone: nothing of JukaHub's is running.
	resumeFamilyNone resumeFamily = iota
	// resumeFamilyRetroArch: RetroArch with its network commands on.
	resumeFamilyRetroArch
	// resumeFamilyRetroArchOff: RetroArch with them off; no state can be
	// asked for.
	resumeFamilyRetroArchOff
	// resumeFamilyOnRequest: JukaHub's PPSSPP build, which saves on request.
	resumeFamilyOnRequest
	// resumeFamilyOnQuit: a standalone emulator whose own save state on exit
	// is on.
	resumeFamilyOnQuit
	// resumeFamilyHold: a game nothing can save a state for.
	resumeFamilyHold
	// resumeFamilyStream: a stream from another machine.
	resumeFamilyStream
	// resumeFamilyApp: an app on screen, not a game.
	resumeFamilyApp
)

// resumeCause is what ends a session without the player.
type resumeCause int

const (
	// resumeCauseBattery: the low-battery guard's save level.
	resumeCauseBattery resumeCause = iota
	// resumeCauseSleepPowerOff: Power off after sleeping's wake.
	resumeCauseSleepPowerOff
	// resumeCausePowerOff: Power off with a game running.
	resumeCausePowerOff
)

// resumeAction is what a cause does to a game.
type resumeAction int

const (
	// resumeLeave: nothing; the game is left as it is.
	resumeLeave resumeAction = iota
	// resumeSave: a state is saved, then the game quits.
	resumeSave
	// resumeHold: stopped in RAM under JukaHub's sleep, the power off near.
	resumeHold
	// resumeClose: asked to quit politely, so its own saves are flushed.
	resumeClose
	// resumeSleepAgain: the sleep goes on with the game held.
	resumeSleepAgain
)

// resumeFamilyOf sorts a running game: class and saveOnQuit as
// sessionSafeDetect read them, onRequest when JukaHub's PPSSPP build can save
// on request, raReady when RetroArch's network commands are on, game when it
// is a game and not an app. Pure.
func resumeFamilyOf(class sessionSafeClass, saveOnQuit, onRequest, raReady, game bool) resumeFamily {
	switch class {
	case sessionSafeNone:
		return resumeFamilyNone
	case sessionSafeRetroArch:
		if raReady {
			return resumeFamilyRetroArch
		}
		return resumeFamilyRetroArchOff
	}
	switch {
	case !game:
		return resumeFamilyApp
	case class == sessionSafeStream:
		return resumeFamilyStream
	case onRequest:
		return resumeFamilyOnRequest
	case class == sessionSafeOnQuit && saveOnQuit:
		return resumeFamilyOnQuit
	}
	return resumeFamilyHold
}

// resumeActionFor is the whole plan: what cause does to a game of family.
// percent is a fresh battery reading (-1: unknown); canHold reports that the
// battery's hold can work now (resumeCanHold, and this episode's hold not
// spent). Pure.
func resumeActionFor(cause resumeCause, fam resumeFamily, percent int, canHold bool) resumeAction {
	switch fam {
	case resumeFamilyNone:
		return resumeLeave
	case resumeFamilyRetroArch, resumeFamilyOnRequest, resumeFamilyOnQuit:
		return resumeSave
	case resumeFamilyStream:
		return resumeClose
	}
	switch cause {
	case resumeCauseBattery:
		switch {
		case fam == resumeFamilyApp:
			// Closing an app could lose its work; the suspend keeps it.
			return resumeLeave
		case canHold:
			return resumeHold
		case fam == resumeFamilyRetroArchOff:
			// RetroArch runs on through the suspend, as it always did.
			return resumeLeave
		}
		return resumeClose
	case resumeCauseSleepPowerOff:
		if fam == resumeFamilyRetroArchOff {
			// Progress is never traded for battery (quickSaveHeldGame).
			return resumeSleepAgain
		}
		if percent >= 0 && percent <= lowBatteryWarnPct {
			return resumeClose
		}
		return resumeSleepAgain
	}
	// Power off: the teardown would end it anyway; it is asked first.
	return resumeClose
}

// resumeCanHold reports whether the battery's hold can work on this device:
// JukaHub's sleep, its power off and an RTC that wakes the device for it —
// without that wake a held game would only ever end with the battery.
func resumeCanHold() bool {
	return powerSleepSupported() && devPowerSupported() && quickRTCWakeSupported() && !powerSleepActive()
}

// resumeBatteryPlan is the battery guard's plan for the game running now.
type resumeBatteryPlan struct {
	family  resumeFamily
	action  resumeAction
	percent int
	label   string
}

// resumeBatteryPlanNow reads what runs and plans the guard's step for it;
// holdUnused reports that this episode's hold is still there to use. Only the
// hold is acted on by the caller (protectLowBattery): every other action is
// what sessionSafeProtectGame does already. Off the main thread: it reads
// /proc and the emulator's config.
func resumeBatteryPlanNow(holdUnused bool) resumeBatteryPlan {
	p := resumeBatteryPlan{percent: batteryCalCorrect(batteryPercentIn(batterySysfsDir()))}
	cmds := liveScreenChildren()
	if runtime.GOOS != "linux" || len(cmds) == 0 {
		// Nothing of JukaHub's runs (a RetroArch something else started):
		// the guard's own path.
		return p
	}
	switch {
	case retroArchRunning():
		ready, _ := retroArchNetCmdReady()
		p.family, p.label = resumeFamilyOf(sessionSafeRetroArch, false, false, ready, true), "The RetroArch game"
	case sessionSafeEnabled(appConfig):
		run := sessionSafeDetect(cmds)
		p.family = resumeFamilyOf(run.class, run.saveOnQuit, run.saveTo != "", true, sessionSafeIsGame(run))
		p.label = run.label()
	default:
		// Protect standalone emulators is off: the guard's own path.
		return p
	}
	// The game by its name where the library started it.
	if l, ok := sessionSafeLaunchFor(cmds); ok && strings.TrimSpace(l.game.Title) != "" {
		p.label = l.game.Title
	}
	p.action = resumeActionFor(resumeCauseBattery, p.family, p.percent, holdUnused && resumeCanHold())
	return p
}

// resumeHoldForBattery is the guard's hold: the game stays where it is in RAM
// and JukaHub's own sleep takes the device (sleepNow: the game stopped, the
// card synced, the RTC armed for the near power off, quickSleepDeadline). The
// bell keeps what happened for after the wake; the buzz says it now, under
// the game. Off the main thread.
func resumeHoldForBattery(p resumeBatteryPlan) {
	lowBatteryNoteHold()
	within := "soon"
	if d := quickSleepDeadline(); d > 0 {
		within = "within " + sleepPowerOffLabel(d)
	}
	why := "cannot save a state"
	if p.family == resumeFamilyRetroArchOff {
		why = "runs in RetroArch with its network commands off, so no state can be asked for"
	}
	logInfo("power", "battery at %d%%: %s %s; holding it asleep where it is (stopped, card synced; closed cleanly and powered off %s unless a charger comes)",
		p.percent, p.label, why, within)
	playHaptic(hapticErrorPulse)
	notifRecord(fmt.Sprintf("Battery critical: %s was held asleep where it was — plug in and press power to carry on. Without a charger it is closed safely and the device powers off %s", p.label, within), notifWarning)
	// The battery history's last minutes, before the battery may run out
	// asleep (battgraph.go), as the guard's own suspend does.
	battGraphFlushNow()
	sleepNow("low-battery")
}

// resumeRetroArchSave is the battery's RetroArch save (sessionSafeProtectGame):
// retroArchSaveAndQuit, then, once the game has quit, the power-on resume
// marker, so a battery that runs out after the save still brings the game
// back at the next power-on. Off the main thread.
func resumeRetroArchSave(tag string, cmds []*exec.Cmd) (bool, string) {
	marker, named := quickRunningGameMarker(cmds)
	saved, why := retroArchSaveAndQuit(tag)
	if saved && named {
		quickAwaitGameCleanup()
		quickResumeWriteMarker(marker)
	}
	return saved, why
}

// ── The power-on marker against a newer game ─────────────────────────────

func init() {
	gameLaunchRegisterStarted(resumeMarkerStarted)
}

// resumeMarkerStarted drops a power-on resume marker written in this boot
// (the battery's save, a Power off that never happened) once a game starts:
// the next power-on brings back what was played last, and the saved game
// itself starts from its state through its own launch anyway. Main thread;
// the file work runs on its own.
func resumeMarkerStarted(libraryGame, string, *exec.Cmd) {
	go resumeDropSameBootMarker()
}

// resumeDropSameBootMarker removes the marker when this boot wrote it. A
// marker from another boot is the power-on's (read before the first frame);
// one without a boot id cannot be told apart and stays.
func resumeDropSameBootMarker() {
	path := quickResumeMarkerPath()
	boot := quickBootID()
	if path == "" || boot == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var m quickResumeMarker
	if json.Unmarshal(data, &m) != nil || m.BootID != boot {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		logWarn("power", "could not drop the resume marker of %s: %v", m.Path, err)
		return
	}
	logInfo("power", "another game started: the next power-on will not bring back the save of this boot (%s)", m.Path)
}

// ── Games JukaHub closes for ─────────────────────────────────────────────

// resumeHandoffBattery is the start after a game JukaHub closed for whose
// hand-off script ended it at the battery's save level (hakux_handoff.go):
// a state JukaHub's PPSSPP saved then is kept for the game's next start and
// the next power-on, and with the battery still that low and no charger the
// device powers off at once — the game is closed, and asleep the battery
// would only run out. Main thread (the deferred start-up queue).
func resumeHandoffBattery(note hakuxHandoffNote, run hakuxHandoffRun) {
	title := hakuxGameTitle(note.game())
	saved := run.Resume != "" && regularFile(run.Resume)
	if saved {
		m := sessionSafeMarker{Path: note.Path, System: note.System, Title: note.Title, Launcher: note.Launcher,
			Emulator: "ppsspp", Label: "PPSSPP", State: run.Resume, Resume: run.Resume, AutoLoad: true,
			Reason: "low battery", BootID: quickBootID()}
		sessionSafeKeepMarker(m)
		ppssppResumePrune(run.Resume)
		if qm, ok := sessionSafeQuickMarker(m); ok {
			quickResumeWriteMarker(qm)
		}
	}
	percent := batteryCalCorrect(batteryPercentIn(batterySysfsDir()))
	charging := batteryChargingIn(powerSupplyRoot)
	low := percent >= 0 && percent <= max(run.LowBatteryPct, batteryWarnNow().savePct())+1
	logInfo("power", "%s was closed by its battery watch at %d%% (state for the next start: %t); the battery is at %d%% now, charger %t",
		title, run.LowBatteryPct, saved, percent, charging)
	switch {
	case low && !charging && devPowerSupported():
		msg := "Battery critical: " + title + " was closed safely — powering off"
		if saved {
			msg = "Battery critical: " + title + " was saved — powering off; it resumes at the next power-on"
		}
		notifRecord(msg, notifWarning)
		ShowToast(msg, ToastKindWarning)
		devPowerRequest(devPowerPoweroff)
	case saved:
		ShowToast(title+" was saved before the battery ran out — it starts from there next time", ToastKindInfo)
	default:
		notifRecord("Battery critical: "+title+" was closed safely before the battery ran out", notifWarning)
	}
}
