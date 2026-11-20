package main

// j2me.go — J2ME (Java ME) phone games: a console in the Games library,
// played in RetroArch with the FreeJ2ME-Plus core.
//
// Juka asked for Java on 2026-11-19 ("add J2ME and both"). Java ME games are
// the .jar files (some Japanese ones .kjx) feature phones ran. They go in
// Roms/J2ME like any console's games: romsetup.go counts a .jar as a game only
// in that folder, since anywhere else a .jar is a Java program — a PortMaster
// port's, Minecraft's, a tool's — never a phone game. FreeJ2ME-Plus plays them
// in RetroArch: its core starts a Java program (its -lr.jar) and RetroArch
// shows what it draws. So a J2ME game needs three things: Java (javart*.go
// installs Temurin), the core and its jar (j2me_install.go; on the handheld
// JukaHub's own aarch64 build of the pair).
//
// A start of a J2ME game that no emulator folder of the console's own plays
// (chooseGameLauncher → j2meLauncherForGame) looks at what is there off the
// main thread (j2meInspect), then:
//   - Java first (javaEnsure: with none, Tools → Java opens with an offer that
//     says why, and the start goes on once it is installed);
//   - on the handheld, the core and the jar when they are missing or are not
//     the pair JukaHub installed (the install card; the game starts when they
//     are in, if nothing else took the screen meanwhile);
//   - then the game, through the library's own launch with the core as its
//     emulator (launchLibraryGame: RetroArch -L, as for any core started
//     directly, so the game screen, the diagnosis card and play time all
//     apply), where j2mePrepareLaunch puts Java's bin folder first on
//     RetroArch's PATH and sets JAVA_HOME: the core runs `java` by name.
// On a PC (and a Linux desktop) JukaHub downloads nothing for it: a FreeJ2ME
// core in RetroArch's cores folder with its jar in RetroArch's system folder is
// used, and without one the message says where to get them (docs/J2ME.md).
//
// A MIDlet .jar opened in Files outside a J2ME folder comes here as well
// (javaMIDletOpener): it plays where it is, and the toast says where to put it
// so that Games lists it.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// j2meLibrarySystem is the Games library's name for the console
	// (romSystemAliases: Roms/J2ME).
	j2meLibrarySystem = "J2ME"
	// j2meJavaWhy is the reason Tools → Java's offer gives.
	j2meJavaWhy = "J2ME games run on Java"
	// j2mePlayAfterWithin is how long after the press a game still starts by
	// itself once its setup is done; later, a toast says it is ready.
	j2mePlayAfterWithin = 20 * time.Minute
)

// j2meBusy is what one start is doing: a second press meanwhile starts
// nothing (j2meIdle, j2meChecking, j2meInstalling).
var j2meBusy atomic.Int32

const (
	j2meIdle       int32 = iota
	j2meChecking         // j2meInspect looks at the core and the jar (a moment)
	j2meInstalling       // the core and the jar are being downloaded
)

func init() {
	javaMIDletOpener = j2meOpenMIDlet
}

// j2meIsCore reports whether launcher is a FreeJ2ME core file.
func j2meIsCore(launcher string) bool {
	base := filepath.Base(strings.TrimSpace(launcher))
	switch strings.ToLower(filepath.Ext(base)) {
	case ".so", ".dll":
	default:
		return false
	}
	name, _ := parseCoreFile(base)
	name = normalizeCoreName(name)
	return name == j2meCoreName || name == j2meOldCoreName
}

// j2meLauncherForGame is chooseGameLauncher's answer for a J2ME game that no
// emulator folder plays; false for every other game. It never blocks: the
// setup runs first when something is missing, and launch gets the core file
// once everything is there. Main thread.
func j2meLauncherForGame(game libraryGame, launch func(launcher string)) bool {
	if game.System != j2meLibrarySystem || launch == nil {
		return false
	}
	if !j2meBusy.CompareAndSwap(j2meIdle, j2meChecking) {
		// A press during the first one's quick look starts nothing more:
		// that one goes on. During a download, the toast says why.
		if j2meBusy.Load() == j2meInstalling {
			ShowToast("J2ME games are still being set up — start "+game.Label()+" again when that is done", ToastKindInfo)
		}
		return true
	}
	pressed := time.Now()
	romPath := game.Path
	// A start nobody pressed for (the resume at power-on, hakux_auto.go)
	// downloads nothing.
	unasked := hakuxUnasked
	go func() {
		// The answer always reaches the main thread, which frees j2meBusy.
		setup := j2meSetup{Why: "J2ME games could not be checked — see the log, then start the game again"}
		defer func() {
			if r := recover(); r != nil {
				logWarn("library", "J2ME: looking at FreeJ2ME-Plus failed: %v", r)
			}
			tbxPost(func() { j2meAfterInspect(game, launch, setup, pressed, unasked) })
		}()
		setup = j2meInspect(romPath)
	}()
	return true
}

// j2meAfterInspect goes on from what j2meInspect found. Main thread.
func j2meAfterInspect(game libraryGame, launch func(launcher string), setup j2meSetup, pressed time.Time, unasked bool) {
	j2meBusy.Store(j2meIdle)
	switch {
	case setup.Ready:
		if _, ok := j2meJavaNow(); !ok && unasked {
			// Nobody pressed for this start: no Java offer opens by itself.
			logInfo("library", "J2ME: %s not started by itself: no Java yet (a press offers it)", filepath.Base(game.Path))
			ShowToast("J2ME games need Java first — start "+game.Label()+" to set it up", ToastKindInfo)
			return
		}
		javaEnsure(appConfig, j2meJavaWhy, 8, false, func(javaRuntime) {
			j2meStart(game, launch, setup.Core, pressed)
		})
	case setup.Fetch:
		if unasked {
			logInfo("library", "J2ME: %s not started by itself: FreeJ2ME-Plus is not set up (a press sets it up)", filepath.Base(game.Path))
			ShowToast("J2ME games need FreeJ2ME-Plus first — start "+game.Label()+" to set it up", ToastKindInfo)
			return
		}
		if !IsOnline() {
			ShowToast("J2ME games need the FreeJ2ME-Plus core first (once, a few MB) — connect to the internet and start "+game.Label()+" again", ToastKindWarning)
			return
		}
		// Java first: its offer is a question, and the core is no use
		// without it.
		javaEnsure(appConfig, j2meJavaWhy, 8, false, func(javaRuntime) {
			j2meBeginInstall(game, launch, pressed)
		})
	default:
		logWarn("library", "J2ME: %s cannot start: %s", filepath.Base(game.Path), setup.Why)
		ShowToast(setup.Why, ToastKindWarning)
	}
}

// j2meBeginInstall installs JukaHub's core and jar with the install card up,
// then starts the game. Main thread.
func j2meBeginInstall(game libraryGame, launch func(launcher string), pressed time.Time) {
	if !j2meBusy.CompareAndSwap(j2meIdle, j2meInstalling) {
		ShowToast("J2ME games are already being set up", ToastKindInfo)
		return
	}
	// A new run takes the install card over and cancels the one on it.
	if installBarActive() || !tryBeginPatchOp() {
		j2meBusy.Store(j2meIdle)
		ShowToast("Another download or update is running — start "+game.Label()+" again when it is done", ToastKindInfo)
		return
	}
	gen, barCtx := installBarBeginRun("J2ME games")
	ShowToast("Getting FreeJ2ME-Plus for J2ME games (once) — "+game.Label()+" starts when it is in", ToastKindInfo)
	PlayActivateSound()
	romPath := game.Path
	go func() {
		ctx, cancel := context.WithTimeout(barCtx, j2meInstallTimeout)
		var core string
		var err error
		func() {
			// A panic still frees the patch slot, the card and j2meBusy below.
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("the J2ME setup stopped: %v", r)
				}
			}()
			core, err = j2meInstallPair(ctx, romPath,
				func(phase string) { installBarUpdateRun(gen, phase, 0, 0) },
				func(done, total int64) { installBarUpdateRun(gen, installPhaseDownload, done, total) })
		}()
		cancelled := barCtx.Err() != nil
		cancel()
		endPatchOp()
		if err != nil {
			logWarn("library", "J2ME setup: %v", err)
			installBarFailRun(gen, firstLine(emuFriendlyError(err)))
		} else {
			installBarDoneRun(gen)
		}
		tbxPost(func() {
			j2meBusy.Store(j2meIdle)
			switch {
			case err != nil && cancelled:
				// Stopped from the card: the player chose not to wait.
			case err != nil:
				ShowToast("J2ME games could not be set up: "+emuFriendlyError(err), ToastKindError)
			default:
				j2meStart(game, launch, core, pressed)
			}
		})
	}()
}

// j2meStart starts the game in core: at once after a press, and after a setup
// when nothing else took the screen since and the press is recent; otherwise
// a toast says it is ready. Main thread.
func j2meStart(game libraryGame, launch func(launcher string), core string, pressed time.Time) {
	if gameScreenSuspended() || videoHoldsScreenAwake() || emuPickOpen() || time.Since(pressed) > j2mePlayAfterWithin {
		ShowToast("J2ME games are ready — start "+game.Label()+" again to play", ToastKindSuccess)
		return
	}
	// The emulator the game ran in, for its save states, the diagnosis and
	// the game switcher (resolveGameLauncherFor).
	emuPickNoteRan(game, gameOptionsFor(game.Path), core)
	launch(core)
}

// j2mePrepareLaunch is launchLibraryGame's hook for a FreeJ2ME core: the core
// starts `java -jar …` by name, so RetroArch gets the Java JukaHub uses first
// on its PATH, and JAVA_HOME. With no Java known the launch stops and says
// what to do. Any other launcher is left alone.
func j2mePrepareLaunch(launcher string, cmd *exec.Cmd) error {
	if cmd == nil || !j2meIsCore(launcher) {
		return nil
	}
	rt, ok := j2meJavaNow()
	if !ok {
		if !javaScanFresh() {
			// Right after power-on (the resume, the game switcher) the first
			// look for Java may still be running: no reason to send anyone
			// to install one.
			return errors.New("JukaHub is still looking for Java — start the game again in a moment")
		}
		return errors.New("J2ME games need Java 8 or newer — install it in Tools → Java, then start the game again")
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = javaEnvFor(rt, env)
	logInfo("library", "J2ME: %s starts its jar on Java %d (%s)", filepath.Base(launcher), rt.Major, rt.Home)
	return nil
}

// j2meJavaNow is the Java a J2ME game runs on: the first known one that is 8
// or newer, else JukaHub's own (read from its install record, which starts
// nothing), so a start before the first look for Java has finished still
// finds it. Cheap enough for the main thread.
func j2meJavaNow() (javaRuntime, bool) {
	if rt, ok := javaFindFor(8, false); ok {
		return rt, true
	}
	if rt, ok := javaManagedRuntime(); ok && rt.Java != "" && rt.Major >= 8 {
		return rt, true
	}
	return javaRuntime{}, false
}

// ── Files ────────────────────────────────────────────────────────────────

// j2meOpenMIDlet plays a J2ME game Files found outside a J2ME folder
// (javaMIDletOpener): where it is, through the same start as Games. Main
// thread.
func j2meOpenMIDlet(config *Config, path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	game := libraryGame{Path: path, System: j2meLibrarySystem, Title: gameDisplayTitle(path)}
	chooseGameLauncher(game, func(launcher string) {
		if strings.TrimSpace(launcher) == "" {
			ShowToast("No emulator for J2ME games yet — see docs/J2ME.md", ToastKindWarning)
			return
		}
		if err := launchLibraryGame(game, launcher); err != nil {
			ShowToast("Cannot start "+game.Title+": "+err.Error(), ToastKindError)
			return
		}
		if gameLaunchWasHeld(game.Path) {
			return // a card asks first (gamelaunch_hooks.go)
		}
		recordPlayed(config, path)
		PlayActivateSound()
		msg := "Starting " + game.Title
		if folder := j2meGamesFolder(); folder != "" && !j2meInGamesFolder(path) {
			msg += " · put it in " + folder + " to see it in Games"
		}
		ShowToast(msg, ToastKindSuccess)
	})
	return true
}

// j2meGamesFolder is where J2ME games go so that Games lists them: J2ME in the
// library's first Roms folder.
func j2meGamesFolder() string {
	roots := romLibraryRoots()
	if len(roots) == 0 {
		return ""
	}
	return filepath.Join(roots[0], "J2ME")
}

// j2meInGamesFolder reports whether path sits in a J2ME folder (or a folder
// inside one), as the library's scan files it. It reads no disk.
func j2meInGamesFolder(path string) bool {
	dir := filepath.Dir(path)
	for i := 0; i < 3; i++ {
		if s, ok := romSystemAliases[strings.ToLower(filepath.Base(dir))]; ok {
			return s.Name == j2meLibrarySystem
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return false
}
