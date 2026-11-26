package main

// dolphin.go — GameCube and Wii games on the TrimUI handhelds, in RetroArch's
// Dolphin core.
//
// Juka asked on 2026-11-19 to improve GameCube support on the TrimUI devices.
// Until then both consoles sat in emuDesktopSystems: "not supported" on every
// handheld, with the Windows Dolphin the only answer. That is fair for the
// A133P (Smart Pro, Brick, Brick Pro: four Cortex-A53 cores, a PowerVR GE8300
// and 1 GB; reviews rate GameCube there between "runs poorly" and "not
// playable"), but not for the Smart Pro S, whose A523 (eight Cortex-A55 cores
// at 2 GHz, a Mali-G57) plays the lighter GameCube games with compromises —
// and the libretro buildbot publishes a linux/aarch64 Dolphin core. So:
//
//   - Settings → Games & emulators → "GameCube & Wii" (key gamecube: auto, on,
//     off). Automatic plays them on every model since 2026-11-21 (before, it
//     kept the A133P's "not supported" behind "Try anyway"). While
//     it is on, the two consoles are no longer desktop-only here
//     (emuDesktopSystemFor): the Emulators page lists their folders like any
//     other console's and the daily challenge may pick one of their games.
//     Get more cores leaves the core to the setup below (dolphinManagesCore).
//   - A game's start with no emulator for it (chooseGameLauncher) sets
//     Dolphin up — the core from JukaHub's own build for TrimUI, else the
//     buildbot's nightly (RetroArch then runs it on JukaHub's glibc, which
//     the buildbot's build needs, ra_glibc.go; dolphin_setup.go has both),
//     and Dolphin's Sys folder — and starts the game once both are in, as
//     J2ME's start does.
//   - The Emulators page's Dolphin tiles (the core's row, the GameCube and
//     Wii folders) run the same setup on a press and look for a newer build
//     that loads here (dolphinStartInstall): Juka asked on 2026-11-20, on a
//     Brick Pro, for "dolphin that works on Trimui". Since 2026-11-21 the card
//     gets Roms/GC and Roms/WII, and the page always lists a GameCube and a
//     Wii tile (dolphin_folders.go).
//   - Every start in the core (launchLibraryGame) first makes sure the Sys
//     folder is there and that the core is not a build from before the
//     October 2026 fix for libretro/dolphin#462, whose dual-core mode froze an
//     aarch64 RetroArch about ten seconds into a game: either is fetched
//     first and the launch runs again (dolphin_setup.go).
//   - The core's first start gets the handheld's settings and a start that
//     stops because RetroArch's video driver could not draw for Dolphin moves
//     the core to the other driver for the next one (dolphin_video.go).
//   - The game gets the whole handheld while it plays (xboxboost.go: every
//     core at its top clock, the GPU at full speed, JukaHub's memory handed
//     back, the wake word closed).
//
// On a PC nothing here runs: GameCube and Wii games start in the Windows
// Dolphin the Emulators page links to, as before.

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// dolphinCoreName is the libretro core, in the buildbot's spelling.
	dolphinCoreName = "dolphin"
	// dolphinSetting is the Settings choice: dolphinModeAuto, On or Off.
	dolphinSetting  = "gamecube"
	dolphinModeAuto = "auto"
	dolphinModeOn   = "on"
	dolphinModeOff  = "off"
	// dolphinSetupLabel names the install card's run.
	dolphinSetupLabel = "GameCube games"
	// dolphinPlayAfterWithin is how long after the press a game still starts
	// by itself once its setup is done; later, a toast says it is ready.
	dolphinPlayAfterWithin = 20 * time.Minute
)

// dolphinModes is the Settings row's order.
var dolphinModes = []string{dolphinModeAuto, dolphinModeOn, dolphinModeOff}

// dolphinModeNow mirrors the setting for any goroutine (the library scan, the
// more-cores pass, the reaper): 0 auto, 1 on, 2 off. The main thread syncs it
// from the config (dolphinSyncMode); until then it is Automatic, the default.
var dolphinModeNow atomic.Int32

// dolphinBusy is set while one setup runs: a second press waits for it.
var dolphinBusy atomic.Bool

// dolphinResume is the game whose launch waited for a setup and runs again:
// that run does not wait a second time. Main thread.
var dolphinResume string

// dolphinStaleTried holds the old cores (by file) whose replacement a start
// already tried this session. Main thread.
var dolphinStaleTried map[string]bool

// dolphinIsSystem reports whether system is one of Dolphin's consoles.
func dolphinIsSystem(system string) bool {
	switch canonicalCompatSystem(system) {
	case "GameCube", "Wii":
		return true
	}
	return false
}

// dolphinSoC is this handheld's chip (socA523, socA133P), or "" on a PC and on
// a board nobody named.
func dolphinSoC() string {
	if runtime.GOOS != "linux" || IsWindows() || !IsTrimuiDevice() {
		return ""
	}
	return deviceHardware(DetectDevice()).SoC
}

// dolphinModeFrom reads the setting.
func dolphinModeFrom(c *Config) string {
	if c == nil {
		return dolphinModeAuto
	}
	v, _ := customString(c.Variables.Custom, dolphinSetting)
	v = strings.ToLower(strings.TrimSpace(v))
	if slices.Contains(dolphinModes, v) {
		return v
	}
	return dolphinModeAuto
}

// dolphinSyncMode copies the setting into dolphinModeNow. Main thread.
func dolphinSyncMode(c *Config) {
	dolphinModeNow.Store(int32(slices.Index(dolphinModes, dolphinModeFrom(c))))
}

// dolphinMode is the setting as last synced.
func dolphinMode() string {
	i := int(dolphinModeNow.Load())
	if i < 0 || i >= len(dolphinModes) {
		return dolphinModeAuto
	}
	return dolphinModes[i]
}

// dolphinPlaysHere reports whether GameCube and Wii games play on this device:
// the handheld, with the setting anything but Off.
func dolphinPlaysHere() bool {
	soc := dolphinSoC()
	if soc == "" {
		return false
	}
	// Automatic is on on every model since 2026-11-21 (Juka: "GameCube, PS2,
	// and XBOX should all work on all Trimui devices (even if performance is
	// not great)"); the A133P plays them slowly, at GameCube speed Fastest.
	return dolphinMode() != dolphinModeOff
}

// dolphinPlaysSystemHere reports whether system is a Dolphin console this
// handheld plays now.
func dolphinPlaysSystemHere(system string) bool {
	return dolphinIsSystem(system) && dolphinPlaysHere()
}

// dolphinIsCore reports whether launcher is a Dolphin core file.
func dolphinIsCore(launcher string) bool {
	base := filepath.Base(strings.TrimSpace(launcher))
	switch strings.ToLower(filepath.Ext(base)) {
	case ".so", ".dll":
	default:
		return false
	}
	name, _ := parseCoreFile(base)
	return normalizeCoreName(name) == dolphinCoreName
}

// dolphinLauncherPlays reports whether launcher starts system's game in the
// Dolphin core: the core itself, or a RetroArch script that names it. A
// standalone Dolphin someone put on the card is its own business.
func dolphinLauncherPlays(launcher, system string) bool {
	if dolphinIsCore(launcher) {
		return true
	}
	if !dolphinIsSystem(system) || !retroArchLauncher(launcher) {
		return false
	}
	return normalizeCoreName(gameCoreFromLauncher(launcher, system)) == dolphinCoreName
}

// dolphinFetchSupported reports whether JukaHub installs Dolphin here: the
// 64-bit handheld with GameCube on (the buildbot's linux/aarch64 build).
func dolphinFetchSupported() bool {
	return coreFixSupported() && dolphinPlaysHere()
}

// dolphinNotHereHint is what a start of a GameCube or Wii game says on a
// handheld that does not play them now.
func dolphinNotHereHint(system string) string {
	name := canonicalCompatSystem(system)
	return name + " games are off · Settings → Games & emulators → GameCube & Wii turns them on"
}

// ── A game's start ───────────────────────────────────────────────────────

// dolphinLauncherForGame is chooseGameLauncher's answer for a GameCube or Wii
// game no emulator plays yet; false for every other game, on a PC, and on a
// handheld whose setting has nothing to say (the caller's hint). It never
// blocks: the setup runs first, and launch gets the core file once it is in.
// Main thread.
func dolphinLauncherForGame(game libraryGame, launch func(launcher string)) bool {
	if launch == nil || !dolphinIsSystem(game.System) || dolphinSoC() == "" {
		return false
	}
	dolphinSyncMode(appConfig)
	if !dolphinPlaysHere() {
		PlayNavSound()
		ShowToast(dolphinNotHereHint(game.System), ToastKindInfo)
		return true
	}
	if !coreFixSupported() {
		return false
	}
	if dolphinBusy.Load() {
		ShowToast("Dolphin is still being set up — start "+game.Label()+" again when that is done", ToastKindInfo)
		return true
	}
	// A start nobody pressed for (the resume at power-on, hakux_auto.go)
	// downloads nothing.
	if hakuxUnasked {
		logInfo("library", "GameCube: %s not started by itself: Dolphin is not set up (a press sets it up)", filepath.Base(game.Path))
		ShowToast(canonicalCompatSystem(game.System)+" games need the Dolphin core first — start "+game.Label()+" to set it up", ToastKindInfo)
		return true
	}
	if !IsOnline() {
		ShowToast(canonicalCompatSystem(game.System)+" games need the Dolphin core and its system files first (once, a few dozen MB) — connect to the internet and start "+game.Label()+" again", ToastKindWarning)
		return true
	}
	setting := "Setting up Dolphin for " + canonicalCompatSystem(game.System) + " games (once) — " + game.Label() + " starts when it is in"
	if why := dolphinBeginSetup(game.Label(), game.Path, "", time.Now(), dolphinFixStale, setting, func(core string, _ error) {
		dolphinStart(game, launch, core)
	}); why != "" {
		ShowToast(why, ToastKindInfo)
	}
	return true
}

// dolphinPrepareLibraryLaunch is launchLibraryGame's hook, before anything is
// written for the launch: a start in the Dolphin core whose Sys folder is
// missing, or whose core is a build from before the dual-core freeze fix,
// fetches what it lacks first and runs again from the main loop (deferred).
// A start that cannot go on now returns why (offline without the Sys folder,
// a setup already running); an old core whose update cannot run now only
// warns, and the game plays in it. Main thread.
//
// The caller toasts "Playing …" when the launch returns no error, so what this
// says while it defers or lets an old core play is posted to show after that.
func dolphinPrepareLibraryLaunch(game libraryGame, launcher string) (deferred bool, err error) {
	resumed := dolphinResume == game.Path
	dolphinResume = ""
	if resumed || !dolphinLauncherPlays(launcher, game.System) || dolphinSoC() == "" {
		return false, nil
	}
	dolphinSyncMode(appConfig)
	if !dolphinFetchSupported() {
		return false, nil
	}
	if dolphinBusy.Load() {
		return false, errors.New("Dolphin is still being set up — start it again when that is done")
	}
	need := dolphinInspect(launcher, game.Path)
	if need.Why != "" {
		return false, errors.New(need.Why)
	}
	// One try per session to replace an old core: when it failed (the
	// buildbot down, the card full) the game still plays in the one there.
	if need.CoreStale && dolphinStaleTried[need.Core] {
		need.CoreStale = false
	}
	if !dolphinNeedsSetup(need) {
		return false, nil
	}
	what := dolphinNeedLabel(need)
	staleOnly := dolphinStaleOnly(need)
	oldCore := func() {
		logWarn("library", "GameCube: %s starts in a Dolphin core from before 2026-10-23 (%s): its dual-core mode may freeze about ten seconds in", filepath.Base(game.Path), need.Core)
		if !hakuxUnasked {
			dolphinToastAfterCaller("This Dolphin core is older than the fix for its freeze ten seconds into a game — connect to update it", ToastKindWarning)
		}
	}
	if hakuxUnasked || !IsOnline() {
		if !staleOnly {
			return false, errors.New("Dolphin needs " + what + " first (once) — connect to the internet and start it again")
		}
		oldCore()
		return false, nil
	}
	setting := "Setting up Dolphin (once) — " + game.Label() + " starts when it is ready"
	switch {
	case staleOnly:
		setting = "Updating the Dolphin core (its old build froze ten seconds into a game) — " + game.Label() + " starts when it is in"
	case need.CoreStale:
		setting = "Updating the Dolphin core and getting its system files — " + game.Label() + " starts when they are in"
	case need.CoreMissing:
		setting = "Installing the Dolphin core — " + game.Label() + " starts when it is ready"
	case need.SysMissing && !need.X11Missing:
		setting = "Getting Dolphin's system files (once) — " + game.Label() + " starts when they are in"
	}
	want := dolphinKeepCore
	if need.CoreStale {
		want = dolphinFixStale
	}
	why := dolphinBeginSetup(game.Label(), game.Path, launcher, time.Now(), want, setting, func(core string, _ error) {
		if gameScreenSuspended() || videoHoldsScreenAwake() || emuPickOpen() {
			ShowToast("Dolphin is ready — start "+game.Label()+" again to play", ToastKindSuccess)
			return
		}
		// A core started directly runs again as the build the setup left
		// active: an update may have gone to another cores folder.
		next := launcher
		if core != "" && dolphinIsCore(launcher) && filepath.Clean(core) != filepath.Clean(launcher) {
			next = core
			emuPickNoteRan(game, gameOptionsFor(game.Path), core)
		}
		dolphinResume = game.Path
		if err := launchLibraryGame(game, next); err != nil {
			gameLaunchErrorToast(game.Title, err)
		}
		dolphinResume = ""
	})
	switch {
	case why == "":
		if need.CoreStale {
			if dolphinStaleTried == nil {
				dolphinStaleTried = map[string]bool{}
			}
			dolphinStaleTried[need.Core] = true
		}
		return true, nil
	case staleOnly:
		// Another download is running: the old core plays this time.
		oldCore()
		return false, nil
	}
	return false, errors.New(why)
}

// dolphinToastAfterCaller shows a toast once the main loop runs again: after
// the "Playing …" its caller shows for a launch that returned no error.
func dolphinToastAfterCaller(msg string, kind ToastKind) {
	tbxPost(func() { ShowToast(msg, kind) })
}

// dolphinBeginSetup installs what Dolphin lacks for a start with launcher (""
// for the core JukaHub sets up) with the install card up — the core when there
// is none, or the newer build want asks for, and the Sys folder — then runs
// then with the core file, and kept: why the newer build did not replace the
// core there (nil when nothing was kept). label and romPath are the game's
// ("" for a press on the Emulators page, whose then always runs and says what
// kept means; a game's start says it here); setting is the toast that says
// what happens. It returns why it did not start, "" when it did. Main thread.
func dolphinBeginSetup(label, romPath, launcher string, pressed time.Time, want dolphinCoreWant, setting string, then func(core string, kept error)) string {
	again := "try again when that is done"
	if label != "" {
		again = "start " + label + " again when that is done"
	}
	if !dolphinBusy.CompareAndSwap(false, true) {
		return "Dolphin is already being set up — " + again
	}
	// A run never takes the install card from another one: the player starts
	// the game again when that is done.
	if installBarActive() || !tryBeginPatchOp() {
		dolphinBusy.Store(false)
		return "Another download or update is running — " + again
	}
	gen, barCtx := installBarBeginRun(dolphinSetupLabel)
	ShowToast(setting, ToastKindInfo)
	dolphinToastAfterCaller(setting, ToastKindInfo)
	PlayActivateSound()
	go func() {
		ctx, cancel := context.WithTimeout(barCtx, dolphinSetupTimeout)
		core, kept, err := dolphinSetup(ctx, launcher, romPath, want,
			func(phase, detail string) {
				installBarUpdateRun(gen, phase, 0, 0)
				if detail != "" && installBarGeneration() == gen {
					installBarDetail(detail)
				}
			},
			func(done, total int64) { installBarUpdateRun(gen, installPhaseDownload, done, total) })
		cancelled := barCtx.Err() != nil
		cancel()
		endPatchOp()
		if err != nil {
			logWarn("library", "GameCube setup: %v", err)
			installBarFailRun(gen, firstLine(emuFriendlyError(err)))
		} else {
			installBarDoneRun(gen)
			// The games' folders, Roms/GC and Roms/WII, unless the player
			// deleted them (dolphin_folders.go).
			dolphinEnsureRomsFolders()
		}
		tbxPost(func() {
			dolphinBusy.Store(false)
			if err == nil && kept != nil && label != "" {
				ShowToast("The Dolphin core could not be updated ("+firstLine(emuFriendlyError(kept))+") — "+label+" plays in the one there", ToastKindWarning)
			}
			switch {
			case err != nil && cancelled:
				// Stopped from the card: the player chose not to wait.
			case err != nil:
				ShowToast("Dolphin could not be set up: "+emuFriendlyError(err), ToastKindError)
			case label != "" && time.Since(pressed) > dolphinPlayAfterWithin:
				ShowToast("Dolphin is ready — start "+label+" again to play", ToastKindSuccess)
			default:
				then(core, kept)
			}
		})
	}()
	return ""
}

// dolphinStart starts the game in core after a setup, when nothing else took
// the screen since; otherwise a toast says it is ready. Main thread.
func dolphinStart(game libraryGame, launch func(launcher string), core string) {
	if gameScreenSuspended() || videoHoldsScreenAwake() || emuPickOpen() || !regularFile(game.Path) {
		ShowToast("Dolphin is ready — start "+game.Label()+" again to play", ToastKindSuccess)
		return
	}
	// The emulator the game ran in, for its save states, the diagnosis and the
	// game switcher (resolveGameLauncherFor).
	emuPickNoteRan(game, gameOptionsFor(game.Path), core)
	// The launch that follows must not wait for the same setup again; a launch
	// that stopped before the hook must not let a later start skip it.
	dolphinResume = game.Path
	launch(core)
	dolphinResume = ""
}

// ── The Emulators page ───────────────────────────────────────────────────

// dolphinTile reports whether an Emulators page tile is Dolphin's on the
// handheld: the GameCube or Wii tile (dolphin_folders.go), a Roms/GC or
// Roms/Wii folder's, the installed core's, or the buildbot's "dolphin" row.
// X on it (A while the core is missing) runs dolphinStartInstall instead of
// the generic core install, so the core comes from the source whose build
// loads here, with Dolphin's Sys folder.
func dolphinTile(e emuEntry) bool {
	return !e.Pinned && dolphinSoC() != "" && coreFixSupported() && emuCoreForEntry(e) == dolphinCoreName
}

// dolphinStartInstall installs the Dolphin core, or brings it to the newest
// build this handheld can load, with Dolphin's Sys folder (a Dolphin tile on
// the Emulators page). Main thread.
func dolphinStartInstall() {
	dolphinSyncMode(appConfig)
	if dolphinMode() == dolphinModeOff {
		PlayNavSound()
		ShowToast("GameCube & Wii are off on this handheld · Settings → Games & emulators → GameCube & Wii turns them on", ToastKindInfo)
		return
	}
	if !IsOnline() {
		ShowToast("No internet connection — connect to set up Dolphin", ToastKindWarning)
		return
	}
	// A setting that does not play them here (none since Automatic is on
	// everywhere, kept for safety) turns On once the setup is under way.
	turnOn := !dolphinPlaysHere() && appConfig != nil
	before := dolphinCoreIdentity(dolphinFindCore())
	setting := "Setting up Dolphin for GameCube and Wii games"
	if before != "" {
		setting = "Looking for the newest Dolphin core that runs on this handheld"
	}
	onNote := ""
	if turnOn {
		onNote = " · GameCube & Wii set to On"
	}
	why := dolphinBeginSetup("", "", "", time.Now(), dolphinGetNewest, setting, func(core string, kept error) {
		if kept != nil {
			ShowToast("No newer Dolphin core could be installed ("+firstLine(emuFriendlyError(kept))+") — the one there stays"+onNote, ToastKindWarning)
			return
		}
		rec := dolphinRecordGet()
		msg := "Dolphin is ready · games go in " + dolphinFoldersLabel()
		if before != "" && dolphinCoreIdentity(core) == before {
			msg = "Dolphin is already the newest build that runs here"
		}
		switch {
		case rec.CorePath != filepath.Clean(core):
			// The record is about another file: nothing to add.
		case rec.CoreGlibc && rec.CoreFrom != "":
			msg += " · " + rec.CoreFrom + "'s build, started on JukaHub's glibc " + privateGlibcVersion
		case rec.CoreGlibc:
			msg += " · started on JukaHub's glibc " + privateGlibcVersion
		case rec.CoreFrom != "":
			msg += " · " + rec.CoreFrom
		}
		PlaySuccessSound()
		ShowToast(msg+onNote, ToastKindSuccess)
	})
	if why != "" {
		ShowToast(why, ToastKindInfo)
		return
	}
	if turnOn {
		setStoreString(appConfig, dolphinSetting, dolphinModeOn)
		dolphinSyncMode(appConfig)
		// The page's GameCube and Wii folders are no longer "not supported".
		invalidateEmulatorCache()
		logInfo("library", "GameCube: GameCube & Wii set to On by a press on a Dolphin tile")
	}
}

// ── Settings ─────────────────────────────────────────────────────────────

// dolphinSettingsRow is Settings → Games & emulators → "GameCube & Wii", on
// the handheld.
func dolphinSettingsRow() setRow {
	return setRow{
		ID: dolphinSetting, Label: "GameCube & Wii", Kind: setKindChoice,
		Visible: func(*Config) bool { return dolphinSoC() != "" },
		Detail:  dolphinSettingDetail,
		Options: func(*Config) []setOption {
			labels := map[string]string{dolphinModeAuto: "Automatic", dolphinModeOn: "On", dolphinModeOff: "Off"}
			notes := map[string]string{
				dolphinModeAuto: "On: the Smart Pro S plays the lighter games; on the A133P models most run well below full speed",
				dolphinModeOn:   "GameCube and Wii games start in RetroArch's Dolphin core, set up on the first start",
				dolphinModeOff:  "JukaHub does not set Dolphin up here; a Dolphin core you installed yourself still starts the games",
			}
			out := make([]setOption, 0, len(dolphinModes))
			for _, m := range dolphinModes {
				out = append(out, setOption{Value: m, Label: labels[m], Note: notes[m]})
			}
			return out
		},
		Current: func(c *Config) int { return slices.Index(dolphinModes, dolphinModeFrom(c)) },
		Choose: func(c *Config, value string) {
			setStoreString(c, dolphinSetting, value)
			dolphinSyncMode(c)
			// The Emulators page and the library's hints read the mode.
			invalidateEmulatorCache()
			if dolphinPlaysHere() {
				// Their folders on the card (dolphin_folders.go).
				dolphinEnsureRomsFolders("GameCube", "Wii")
				setToast("GameCube & Wii: on · games go in " + dolphinFoldersLabel() + "; the first start sets Dolphin up")
				return
			}
			setToast("GameCube & Wii: off on this handheld")
		},
	}
}

// dolphinSettingDetail is the row's second line. Cheap: drawn every frame.
func dolphinSettingDetail(c *Config) string {
	mode := dolphinModeFrom(c)
	switch {
	case mode == dolphinModeOff:
		return "Off · JukaHub does not set Dolphin up for GameCube and Wii games"
	case dolphinSoC() != socA523:
		return "In RetroArch's Dolphin core · on this chip expect most games well below full speed"
	}
	return "In RetroArch's Dolphin core · lighter games play; Wii games rarely reach full speed"
}
