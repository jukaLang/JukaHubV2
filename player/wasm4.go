package main

// wasm4.go — WASM-4 fantasy-console games: a console in the Games library,
// played in RetroArch with the WASM-4 libretro core.
//
// WASM-4 (wasm4.org, github.com/aduros/wasm4) is a fantasy console in the
// spirit of PICO-8 and TIC-80: a 160×160 screen in four colours, two buttons
// (X and Z) and a D-pad for up to four players, a mouse, and 1 KB of "disk"
// for saves. A game is one WebAssembly file of at most 64 KB, a .wasm, so the
// whole console is tiny work for the A133P. Juka asked on 2026-11-19 for "a
// tested launcher and homebrew shelf" for it; the Retro formats plugin only
// taught the library that a .wasm is a WASM-4 game, in every folder.
//
// The console is built in now:
//   - Library: Roms/WASM4 (the name Batocera and Knulli use; "wasm-4" and
//     "wasm 4" are aliases) is the "WASM-4" shelf, and a .wasm counts as a
//     game only in that folder (romShelfRegister): anywhere else a .wasm is a
//     web page's or a tool's module. The collection name is the libretro
//     database's ("WASM-4"), so covers come from its thumbnails when there is
//     no screenshot from the carts screen. The core table (emuCoreSystems)
//     learns that wasm4 plays WASM-4, so an installed core is offered like
//     any other (emuchoices.go) and the Emulators page names its console.
//   - Start: a game that no emulator plays yet (chooseGameLauncher →
//     wasm4LauncherForGame) installs the core first, with the install card
//     up, and then plays, the way a J2ME game sets up FreeJ2ME-Plus (j2me.go).
//     Where the core comes from is wasm4_install.go. A start nobody pressed
//     for (the resume at power-on) downloads nothing.
//   - Launch: RetroArch started directly with the core (-L), as for any core
//     started directly; the core has no options and no BIOS. Its picture is
//     square: for one launch (racfgscope.go) RetroArch scales it by whole
//     pixels, so the 160×160 screen stays crisp (4× on a 720-line screen),
//     unless the player chose a picture mode of their own for it (videofilter
//     .go), which wins.
//   - Saves: the console's 1 KB disk is the core's battery save, a .srm named
//     after the cart in RetroArch's save folder, which the save tools find by
//     its name like any other console's.
//   - Carts: "Get WASM-4 carts" (wasm4_carts*.go), from the game page's More
//     sheet of any WASM-4 game and from the Start menu.
//
// Mouse carts: the pad works as a mouse through Settings → Controls → Mouse
// in games and apps (the virtual cursor, vcursor*.go); nothing here changes
// that.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// wasm4System is the Games library's name for the console.
	wasm4System = "WASM-4"
	// wasm4Collection is the libretro database's name (thumbnails).
	wasm4Collection = "WASM-4"
	// wasm4Folder is the Roms folder a card without one gets.
	wasm4Folder = "WASM4"
	// wasm4CoreName is the libretro core's name (wasm4_libretro.so/.dll).
	wasm4CoreName = "wasm4"
	// wasm4Ext is a cart's extension.
	wasm4Ext = ".wasm"
	// wasm4PlayAfterWithin is how long after the press a game still starts by
	// itself once the core is in; later, a toast says it is ready.
	wasm4PlayAfterWithin = 20 * time.Minute
)

// wasm4FolderAliases are the Roms folder names of the shelf (lower case, as
// romSystemAliases keys them).
var wasm4FolderAliases = []string{"wasm4", "wasm-4", "wasm 4"}

// wasm4Busy is what one start is doing: a second press meanwhile starts
// nothing (wasm4Idle, wasm4Checking, wasm4Installing).
var wasm4Busy atomic.Int32

const (
	wasm4Idle       int32 = iota
	wasm4Checking         // wasm4Inspect looks for the core (a moment)
	wasm4Installing       // the core is being downloaded
)

func init() {
	for _, folder := range wasm4FolderAliases {
		if _, taken := romSystemAliases[folder]; !taken {
			romSystemAliases[folder] = romSystem{Name: wasm4System, Collection: wasm4Collection}
		}
	}
	if _, taken := systemLauncherAliases[wasm4System]; !taken {
		systemLauncherAliases[wasm4System] = []string{"wasm4"}
	}
	romShelfRegister(wasm4System, func(_, _, ext string) bool { return ext == wasm4Ext })
	if sys, _ := emuCoreInfo(wasm4CoreName); sys == "" {
		emuCoreSystems = append(emuCoreSystems, emuCoreSystem{prefix: wasm4CoreName, system: wasm4System, family: emuFamilyOther})
	}
	gameLaunchRegisterPrepare(wasm4PrepareLaunch)
	gamePageRegisterExtra(gamePageExtra{
		ID:    "wasm4-carts",
		Order: 80,
		Icon:  "rom",
		Row: func(game libraryGame) (string, string, bool) {
			if game.System != wasm4System {
				return "", "", false
			}
			return wasm4CartsEntryName, "More free WASM-4 games from wasm4.org", true
		},
		Open: func(libraryGame) { wasm4CartsOpen(appConfig) },
	})
}

// ── Starting a game ──────────────────────────────────────────────────────

// wasm4LauncherForGame is chooseGameLauncher's answer for a WASM-4 game that
// no emulator plays yet; false for every other game. It never blocks: the
// look for the core runs on a goroutine, the install (when needed) with the
// install card up, and launch gets the core file once it is there. Main
// thread.
func wasm4LauncherForGame(game libraryGame, launch func(launcher string)) bool {
	if game.System != wasm4System || launch == nil {
		return false
	}
	if !wasm4Busy.CompareAndSwap(wasm4Idle, wasm4Checking) {
		if wasm4Busy.Load() == wasm4Installing {
			ShowToast("The WASM-4 player is still being set up — start "+game.Label()+" again when that is done", ToastKindInfo)
		}
		return true
	}
	pressed := time.Now()
	// A start nobody pressed for (the resume at power-on, hakux_auto.go)
	// downloads nothing.
	unasked := hakuxUnasked
	go func() {
		// The answer always reaches the main thread, which frees wasm4Busy.
		setup := wasm4Setup{Why: "The WASM-4 player could not be checked — see the log, then start the game again"}
		defer func() {
			if r := recover(); r != nil {
				logWarn("library", "WASM-4: looking for the core failed: %v", r)
			}
			tbxPost(func() { wasm4AfterInspect(game, launch, setup, pressed, unasked) })
		}()
		setup = wasm4Inspect()
	}()
	return true
}

// wasm4Setup is what a WASM-4 game would start in here (wasm4Inspect).
type wasm4Setup struct {
	Core  string // the core file ("" when there is none)
	Fetch bool   // JukaHub installs the core here
	Why   string // neither: what is missing and what to do
}

// wasm4Inspect looks for the core. It lists folders, so never on the main
// thread.
func wasm4Inspect() wasm4Setup {
	if core := wasm4FindCore(); core != "" {
		return wasm4Setup{Core: core}
	}
	if wasm4CanInstall() {
		return wasm4Setup{Fetch: true}
	}
	return wasm4Setup{Why: wasm4NoCoreHelp()}
}

// wasm4AfterInspect goes on from what wasm4Inspect found. Main thread.
func wasm4AfterInspect(game libraryGame, launch func(launcher string), setup wasm4Setup, pressed time.Time, unasked bool) {
	wasm4Busy.Store(wasm4Idle)
	switch {
	case setup.Core != "":
		wasm4Start(game, launch, setup.Core, pressed)
	case setup.Fetch:
		if unasked {
			logInfo("library", "WASM-4: %s not started by itself: the core is not installed (a press installs it)", filepath.Base(game.Path))
			ShowToast("WASM-4 games need their player first — start "+game.Label()+" to set it up", ToastKindInfo)
			return
		}
		if !IsOnline() {
			ShowToast("WASM-4 games need the WASM-4 core first (once, under 1 MB) — connect to the internet and start "+game.Label()+" again", ToastKindWarning)
			return
		}
		wasm4BeginInstall(game, launch, pressed)
	default:
		logWarn("library", "WASM-4: %s cannot start: %s", filepath.Base(game.Path), setup.Why)
		ShowToast(setup.Why, ToastKindWarning)
	}
}

// wasm4BeginInstall installs the core with the install card up, then starts
// the game. Main thread.
func wasm4BeginInstall(game libraryGame, launch func(launcher string), pressed time.Time) {
	if !wasm4Busy.CompareAndSwap(wasm4Idle, wasm4Installing) {
		ShowToast("The WASM-4 player is already being set up", ToastKindInfo)
		return
	}
	if installBarActive() || !tryBeginPatchOp() {
		wasm4Busy.Store(wasm4Idle)
		ShowToast("Another download or update is running — start "+game.Label()+" again when it is done", ToastKindInfo)
		return
	}
	gen, barCtx := installBarBeginRun("WASM-4 player")
	ShowToast("Getting the WASM-4 player (once) — "+game.Label()+" starts when it is in", ToastKindInfo)
	PlayActivateSound()
	go func() {
		ctx, cancel := context.WithTimeout(barCtx, wasm4InstallTimeout)
		var core string
		var err error
		func() {
			// A panic still frees the patch slot, the card and wasm4Busy below.
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("the WASM-4 setup stopped: %v", r)
				}
			}()
			core, err = wasm4InstallCore(ctx, func(phase string) { installBarUpdateRun(gen, phase, 0, 0) })
		}()
		cancelled := barCtx.Err() != nil
		cancel()
		endPatchOp()
		if err != nil {
			logWarn("library", "WASM-4 setup: %v", err)
			installBarFailRun(gen, firstLine(emuFriendlyError(err)))
		} else {
			installBarDoneRun(gen)
		}
		tbxPost(func() {
			wasm4Busy.Store(wasm4Idle)
			switch {
			case err != nil && cancelled:
				// Stopped from the card: the player chose not to wait.
			case err != nil:
				ShowToast("The WASM-4 player could not be set up: "+emuFriendlyError(err), ToastKindError)
			default:
				wasm4Start(game, launch, core, pressed)
			}
		})
	}()
}

// wasm4Start starts the game in core: at once after a press, and after an
// install when nothing else took the screen since and the press is recent;
// otherwise a toast says it is ready. Main thread.
func wasm4Start(game libraryGame, launch func(launcher string), core string, pressed time.Time) {
	if gameScreenSuspended() || videoHoldsScreenAwake() || emuPickOpen() || time.Since(pressed) > wasm4PlayAfterWithin {
		ShowToast("WASM-4 games are ready — start "+game.Label()+" again to play", ToastKindSuccess)
		return
	}
	// The emulator the game ran in, for the save-state list, the diagnosis and
	// the game switcher (resolveGameLauncherFor).
	emuPickNoteRan(game, gameOptionsFor(game.Path), core)
	launch(core)
}

// ── The launch ───────────────────────────────────────────────────────────

// wasm4PrepareLaunch is the one-launch setting of a WASM-4 game started in
// RetroArch (gameLaunchRegisterPrepare): whole-pixel scaling for the square
// picture, unless the player picked a picture mode for it (or for every
// console) — then videofilter.go already wrote theirs. Never blocks or fails
// the launch.
func wasm4PrepareLaunch(launcher string, game libraryGame) {
	if !wasm4IsGame(game) || !retroArchLauncher(launcher) {
		return
	}
	// Only Auto with no look for this console: a mode the player picked —
	// "RetroArch's own" included — is theirs. The console is the one
	// prepareVideoFilter resolved this launch with (videofilter.go).
	mode, auto := videoFilterResolveAuto(appConfig, gameOptionsFor(game.Path).VideoFilter, lookSystemForROM(game.Path), nil)
	if !auto || mode != "unmanaged" {
		return
	}
	values := []raScopeValue{{Key: "video_scale_integer", Value: "true"}}
	for _, path := range raScopeTargets(raLaunchCandidates(launcher, videoFilterCandidates())) {
		if _, err := raScopeApply(path, values); err != nil {
			logWarn("library", "WASM-4: cannot turn on whole-pixel scaling in %s: %v", path, err)
		}
	}
}

// wasm4IsGame reports a WASM-4 cart: by the library's console, or by the
// folder it sits in (a start from the recents of an older scan). No disk.
func wasm4IsGame(game libraryGame) bool {
	if game.System == wasm4System {
		return true
	}
	return strings.EqualFold(filepath.Ext(game.Path), wasm4Ext) && lookSystemForROM(game.Path) == wasm4System
}
