package main

// ps2.go — PlayStation 2 games on the TrimUI handhelds, in RetroArch's ARMSX2
// core.
//
// Juka asked on 2026-11-20 to add PS2 support, pointing at a developer who
// runs ARMSX2 (a PCSX2 fork with ARM64 recompilers for the EE, the VUs and the
// IOP) on a TrimUI Brick Hammer — the Smart Pro's chip: "some low-intensity
// games (Road Trip, VF 10th anniversary) are already running at full speed" —
// and then to focus on the TrimUI Smart Pro, not on Windows. Until then PS2
// sat in emuDesktopSystems: "not supported" on every handheld, PCSX2 on a PC
// the only answer. ARMSX2 now publishes a libretro core for Linux arm64 with
// its dependencies linked in (ps2_setup.go), which the Smart Pro's RetroArch
// can load on JukaHub's private glibc (ra_glibc.go). So:
//
//   - Settings → Games & emulators → "PlayStation 2" (key ps2, on by default
//     on every TrimUI model). While it is on, PS2 is no longer desktop-only
//     here (emuDesktopSystemFor): Roms/PS2 lists its games, the Emulators page
//     lists the folder, and A there (or Settings → "PS2 emulator") installs
//     or updates ARMSX2.
//   - A game's start with no emulator for it (chooseGameLauncher) sets ARMSX2
//     up — the core, its resources folder, the player's BIOS copied where the
//     core reads it — and starts the game once that is done, as GameCube's
//     start does (dolphin.go). With no BIOS anywhere it says where to put one.
//   - Every start in the core (launchLibraryGame) first makes sure the core
//     can load, the resources folder is there and a BIOS is in place; what is
//     missing is fetched or copied first and the launch runs again.
//   - The core's first start gets the handheld's settings: the OpenGL ES
//     renderer where the firmware has no Vulkan driver (the Smart Pro's
//     PowerVR GE8300 has none; ARMSX2's default is Vulkan) and the BIOS
//     intro skipped. A start that stops because the renderer could not draw
//     moves the core to the next one, Software last (ps2_video.go).
//   - The game gets the whole handheld while it plays (xboxboost.go).
//
// On a PC nothing here runs: PS2 games start in the Windows PCSX2 the
// Emulators page installs, as before.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// ps2CoreName is the libretro core, as its file is named
	// (armsx2_libretro.so); ps2CoreFile is that file.
	ps2CoreName = "armsx2"
	ps2CoreFile = ps2CoreName + "_libretro.so"
	// ps2CoreConfigName is the core's library name: RetroArch's folder for its
	// options (config/ARMSX2/ARMSX2.opt).
	ps2CoreConfigName = "ARMSX2"
	// ps2SystemName is the console, as the library names it.
	ps2SystemName = "PlayStation 2"
	// ps2Setting is the Settings switch (bool, on by default).
	ps2Setting = "ps2"
	// ps2SetupLabel names the install card's run.
	ps2SetupLabel = "PlayStation 2 games"
	// ps2PlayAfterWithin is how long after the press a game still starts by
	// itself once its setup is done; later, a toast says it is ready.
	ps2PlayAfterWithin = 20 * time.Minute
)

// ps2Off mirrors the setting for any goroutine (the library scan, the
// reaper): true when it is off. The main thread syncs it from the config
// (ps2SyncMode); until then PS2 is on, the default.
var ps2Off atomic.Bool

// ps2Busy is set while one setup runs: a second press waits for it.
var ps2Busy atomic.Bool

// ps2Resume is the game whose launch waited for a setup and runs again: that
// run does not wait a second time. Main thread.
var ps2Resume string

// ps2IsSystem reports whether system is the PlayStation 2.
func ps2IsSystem(system string) bool {
	return canonicalCompatSystem(system) == ps2SystemName
}

// ps2SoC is this handheld's chip (socA133P on the Smart Pro, Brick and
// Hammer; socA523 on the Smart Pro S), or "" on a PC, on a board nobody named,
// and on a build that is not 64-bit ARM (ARMSX2 is arm64 only).
func ps2SoC() string {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" || IsWindows() || !IsTrimuiDevice() {
		return ""
	}
	return deviceHardware(DetectDevice()).SoC
}

// ps2Enabled reads the setting.
func ps2Enabled(c *Config) bool {
	if c == nil {
		return true
	}
	return customBool(c.Variables.Custom, ps2Setting, true)
}

// ps2SyncMode copies the setting into ps2Off. Main thread.
func ps2SyncMode(c *Config) {
	ps2Off.Store(!ps2Enabled(c))
}

// ps2PlaysHere reports whether PS2 games play on this device: a TrimUI
// handheld with the setting on.
func ps2PlaysHere() bool {
	return ps2SoC() != "" && !ps2Off.Load()
}

// ps2PlaysSystemHere reports whether system is the PS2 and this handheld
// plays it now.
func ps2PlaysSystemHere(system string) bool {
	return ps2IsSystem(system) && ps2PlaysHere()
}

// ps2IsCore reports whether launcher is an ARMSX2 core file.
func ps2IsCore(launcher string) bool {
	base := filepath.Base(strings.TrimSpace(launcher))
	switch strings.ToLower(filepath.Ext(base)) {
	case ".so", ".dll":
	default:
		return false
	}
	name, _ := parseCoreFile(base)
	return normalizeCoreName(name) == ps2CoreName
}

// ps2LauncherPlays reports whether launcher starts system's game in the
// ARMSX2 core: the core itself, or a RetroArch script that names it.
func ps2LauncherPlays(launcher, system string) bool {
	if ps2IsCore(launcher) {
		return true
	}
	if !ps2IsSystem(system) || !retroArchLauncher(launcher) {
		return false
	}
	return normalizeCoreName(gameCoreFromLauncher(launcher, system)) == ps2CoreName
}

// ps2FetchSupported reports whether JukaHub installs ARMSX2 here: the 64-bit
// handheld with PS2 on.
func ps2FetchSupported() bool {
	return coreFixSupported() && ps2PlaysHere()
}

// ps2NotHereHint is what a start of a PS2 game says while PS2 is off.
func ps2NotHereHint() string {
	return "PlayStation 2 is off · Settings → Games & emulators → PlayStation 2 turns it on"
}

// ps2LauncherHint is the library's answer for a PS2 game while no emulator
// plays it yet, on a handheld that plays PS2 (emuLibraryLauncherHint); ""
// for any other console and elsewhere.
func ps2LauncherHint(system string) string {
	if !ps2IsSystem(system) || !ps2PlaysHere() {
		return ""
	}
	return "PlayStation 2 games start in ARMSX2 (RetroArch), which the first start sets up; the BIOS must be dumped from your own console"
}

// ps2ChipNote is what to expect on this chip, for the Settings line.
func ps2ChipNote() string {
	if ps2SoC() == socA523 {
		return "lighter games play at full speed; heavy 3D games run slow"
	}
	return "light games play at full speed; most 3D games run slow on this chip"
}

// ── A game's start ───────────────────────────────────────────────────────

// ps2LauncherForGame is chooseGameLauncher's answer for a PS2 game no
// emulator plays yet; false for every other game, on a PC, and on a handheld
// whose setting has nothing to say (the caller's hint). It never blocks: the
// setup runs first, and launch gets the core file once it is in. Main thread.
func ps2LauncherForGame(game libraryGame, launch func(launcher string)) bool {
	if launch == nil || !ps2IsSystem(game.System) || ps2SoC() == "" {
		return false
	}
	ps2SyncMode(appConfig)
	if !ps2PlaysHere() {
		PlayNavSound()
		ShowToast(ps2NotHereHint(), ToastKindInfo)
		return true
	}
	if !coreFixSupported() {
		return false
	}
	if ps2Busy.Load() {
		ShowToast("ARMSX2 is still being set up — start "+game.Label()+" again when that is done", ToastKindInfo)
		return true
	}
	need := ps2Inspect("", game.Path)
	if need.Why != "" {
		PlayNavSound()
		ShowToast(need.Why, ToastKindWarning)
		return true
	}
	stale := ps2StaleWanted(&need)
	if err := ps2CopyBIOSNow(&need); err != nil {
		ShowToast(err.Error(), ToastKindError)
		return true
	}
	if !need.ps2NeedsAnything() && need.Core != "" {
		// Set up already (the catalog had not listed the core yet).
		ps2Start(game, launch, need.Core)
		return true
	}
	if need.ps2NeedsDownload() {
		// A start nobody pressed for (the resume at power-on, hakux_auto.go)
		// downloads nothing.
		if hakuxUnasked {
			logInfo("library", "PS2: %s not started by itself: ARMSX2 is not set up (a press sets it up)", filepath.Base(game.Path))
			ShowToast("PlayStation 2 games need ARMSX2 first — start "+game.Label()+" to set it up", ToastKindInfo)
			return true
		}
		if !IsOnline() {
			ShowToast("PlayStation 2 games need the ARMSX2 emulator first (once) — connect to the internet and start "+game.Label()+" again", ToastKindWarning)
			return true
		}
	}
	setting := "Setting up ARMSX2 for PlayStation 2 games (once) — " + game.Label() + " starts when it is in"
	if why := ps2BeginSetup(game.Label(), "", game.Path, time.Now(), false, stale, setting, func(core string) {
		ps2Start(game, launch, core)
	}); why != "" {
		ShowToast(why, ToastKindInfo)
	}
	return true
}

// ps2StaleTried is set once a start tried to replace JukaHub's resources from
// another release than its core this session: that is worth one try, never a
// download at every start. Main thread.
var ps2StaleTried bool

// ps2StaleWanted reports whether need's out-of-date resources are to be
// replaced now (online, a start someone pressed, the first try this session),
// and otherwise drops them from need: they rarely stop a game. Main thread.
func ps2StaleWanted(need *ps2Needs) bool {
	if !need.ResStale {
		return false
	}
	if hakuxUnasked || !IsOnline() || ps2StaleTried {
		need.ResStale = false
		return false
	}
	ps2StaleTried = true
	return true
}

// ps2CopyBIOSNow copies a BIOS found outside the folder the core reads into
// it, on the spot (a few MB on the same card: no download, no install card,
// so nothing else running holds a game up for it), and marks need as having
// one. Main thread.
func ps2CopyBIOSNow(need *ps2Needs) error {
	if need.BIOS != "" || need.BIOSFound == "" {
		return nil
	}
	copied, err := ps2CopyBIOS(need.BIOSFound, need.SysDir)
	if err != nil {
		return fmt.Errorf("could not copy the PS2 BIOS %s into %s: %v", filepath.Base(need.BIOSFound), ps2CardPath(ps2BIOSDir(need.SysDir)), err)
	}
	need.BIOS, need.BIOSFound = copied, ""
	return nil
}

// ps2PrepareLibraryLaunch is launchLibraryGame's hook, before anything is
// written for the launch: a start in the ARMSX2 core that lacks something
// (a core that cannot load, the resources folder, the BIOS where the core
// reads it) gets it first and runs again from the main loop (deferred). A
// start that cannot go on now returns why: no BIOS anywhere, offline with
// something to download, a setup already running. Main thread.
//
// The caller toasts "Playing …" when the launch returns no error, so what this
// says while it defers is posted to show after that.
func ps2PrepareLibraryLaunch(game libraryGame, launcher string) (deferred bool, err error) {
	resumed := ps2Resume == game.Path
	ps2Resume = ""
	if resumed || !ps2LauncherPlays(launcher, game.System) || ps2SoC() == "" {
		return false, nil
	}
	ps2SyncMode(appConfig)
	if !ps2FetchSupported() {
		// Off: a core installed by hand still starts the game as it is.
		return false, nil
	}
	if ps2Busy.Load() {
		return false, errors.New("ARMSX2 is still being set up — start it again when that is done")
	}
	need := ps2Inspect(launcher, game.Path)
	if need.Why != "" {
		return false, errors.New(need.Why)
	}
	// JukaHub's resources from another release than its core: worth fixing
	// when it can be, not worth stopping a game for.
	stale := ps2StaleWanted(&need)
	if err := ps2CopyBIOSNow(&need); err != nil {
		return false, err
	}
	if !need.ps2NeedsAnything() {
		return false, nil
	}
	if need.ps2NeedsDownload() && (hakuxUnasked || !IsOnline()) {
		return false, errors.New("ARMSX2 needs " + ps2NeedLabel(need) + " first (once) — connect to the internet and start it again")
	}
	staleOnly := stale && !need.CoreMissing && need.CoreBroken == "" && !need.ResMissing
	if staleOnly && installBarActive() {
		// Another download has the card: the game plays with the resources
		// there.
		return false, nil
	}
	setting := "Getting " + ps2NeedLabel(need) + " — " + game.Label() + " starts when it is in"
	why := ps2BeginSetup(game.Label(), launcher, game.Path, time.Now(), false, stale, setting, func(core string) {
		if gameScreenSuspended() || videoHoldsScreenAwake() || emuPickOpen() {
			ShowToast("ARMSX2 is ready — start "+game.Label()+" again to play", ToastKindSuccess)
			return
		}
		// A core started directly runs again as the build the setup left
		// active.
		next := launcher
		if core != "" && ps2IsCore(launcher) && filepath.Clean(core) != filepath.Clean(launcher) {
			next = core
			emuPickNoteRan(game, gameOptionsFor(game.Path), core)
		}
		ps2Resume = game.Path
		if err := launchLibraryGame(game, next); err != nil {
			ShowToast("Cannot launch "+game.Title+": "+err.Error(), ToastKindError)
		}
		ps2Resume = ""
	})
	switch {
	case why == "":
		return true, nil
	case staleOnly:
		return false, nil // the resources there will do this time
	}
	return false, errors.New(why)
}

// ps2BeginSetup runs ps2Setup with the install card up, then runs then with
// the core file. label is the game's name ("" from the Emulators page or
// Settings), update asks for the newest release, stale lets JukaHub's
// resources from another release than its core be replaced, setting is the
// toast that says what happens. It returns why it did not start, "" when it
// did. Main thread.
func ps2BeginSetup(label, launcher, romPath string, pressed time.Time, update, stale bool, setting string, then func(core string)) string {
	again := "try again when that is done"
	if label != "" {
		again = "start " + label + " again when that is done"
	}
	if !ps2Busy.CompareAndSwap(false, true) {
		return "ARMSX2 is already being set up — " + again
	}
	// A run never takes the install card from another one.
	if installBarActive() || !tryBeginPatchOp() {
		ps2Busy.Store(false)
		return "Another download or update is running — " + again
	}
	gen, barCtx := installBarBeginRun(ps2SetupLabel)
	ShowToast(setting, ToastKindInfo)
	tbxPost(func() { ShowToast(setting, ToastKindInfo) })
	PlayActivateSound()
	go func() {
		ctx, cancel := context.WithTimeout(barCtx, ps2SetupTimeout)
		core, err := ps2Setup(ctx, launcher, romPath, update, stale,
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
			logWarn("library", "PS2 setup: %v", err)
			installBarFailRun(gen, firstLine(emuFriendlyError(err)))
		} else {
			installBarDoneRun(gen)
		}
		tbxPost(func() {
			ps2Busy.Store(false)
			switch {
			case err != nil && cancelled:
				// Stopped from the card: the player chose not to wait.
			case err != nil:
				ShowToast("ARMSX2 could not be set up: "+emuFriendlyError(err), ToastKindError)
			case label != "" && time.Since(pressed) > ps2PlayAfterWithin:
				ShowToast("ARMSX2 is ready — start "+label+" again to play", ToastKindSuccess)
			default:
				then(core)
			}
		})
	}()
	return ""
}

// ps2Start starts the game in core after a setup, when nothing else took the
// screen since; otherwise a toast says it is ready. Main thread.
func ps2Start(game libraryGame, launch func(launcher string), core string) {
	if gameScreenSuspended() || videoHoldsScreenAwake() || emuPickOpen() || !regularFile(game.Path) {
		ShowToast("ARMSX2 is ready — start "+game.Label()+" again to play", ToastKindSuccess)
		return
	}
	// The emulator the game ran in, for its save states, the diagnosis and
	// the game switcher (resolveGameLauncherFor).
	emuPickNoteRan(game, gameOptionsFor(game.Path), core)
	// The launch that follows must not wait for the same setup again; a
	// launch that stopped before the hook must not let a later start skip it.
	ps2Resume = game.Path
	launch(core)
	ps2Resume = ""
}

// ── The Emulators page and Settings ──────────────────────────────────────

// ps2Tile reports whether an Emulators page tile is ARMSX2's: the Roms/PS2
// folder's, or the installed core's, on the handheld. A press on it runs
// ps2StartInstall (which says how to turn PS2 on while it is off) instead of
// the buildbot install, whose build of the core needs libraries this
// firmware lacks.
func ps2Tile(e emuEntry) bool {
	return ps2SoC() != "" && coreFixSupported() && emuCoreForEntry(e) == ps2CoreName
}

// ps2StartInstall installs ARMSX2 or updates it to the newest release (the
// Emulators page's tile, Settings → PS2 emulator). Main thread.
func ps2StartInstall() {
	ps2SyncMode(appConfig)
	if !ps2FetchSupported() {
		PlayNavSound()
		ShowToast(ps2NotHereHint(), ToastKindInfo)
		return
	}
	if !IsOnline() {
		ShowToast("No internet connection — connect to set up ARMSX2", ToastKindWarning)
		return
	}
	before := ps2RecordGet().Release
	setting := "Looking for the newest ARMSX2 for PlayStation 2 games"
	if why := ps2BeginSetup("", "", "", time.Now(), true, true, setting, func(core string) {
		rec := ps2RecordGet()
		msg := "ARMSX2 is ready for PlayStation 2 games"
		switch {
		case rec.Release != "" && rec.Release == before:
			msg = "ARMSX2 is already the newest build (" + rec.Release + ")"
		case rec.Release != "":
			msg = "ARMSX2 " + rec.Release + " is ready for PlayStation 2 games"
		}
		if need := ps2Inspect(core, ""); need.BIOS == "" && need.SysDir != "" {
			msg += " · copy your PS2 BIOS to " + ps2CardPath(ps2BIOSDir(need.SysDir))
		}
		ShowToast(msg, ToastKindSuccess)
	}); why != "" {
		ShowToast(why, ToastKindInfo)
	}
}

// ps2SettingsRow is Settings → Games & emulators → "PlayStation 2", on the
// handheld.
func ps2SettingsRow() setRow {
	return setRow{
		ID: ps2Setting, Label: "PlayStation 2", Kind: setKindToggle,
		Visible: func(*Config) bool { return ps2SoC() != "" },
		Detail:  ps2SettingDetail,
		Get:     ps2Enabled,
		Set: func(c *Config, on bool) {
			setStoreBool(c, ps2Setting, on)
			ps2SyncMode(c)
			// The Emulators page and the library's hints read the switch.
			invalidateEmulatorCache()
			if on {
				setToast("PlayStation 2: on · a game's first start sets ARMSX2 up")
				return
			}
			setToast("PlayStation 2: off on this handheld")
		},
	}
}

// ps2SettingDetail is the row's second line. Cheap: drawn every frame.
func ps2SettingDetail(c *Config) string {
	if !ps2Enabled(c) {
		return "Off · JukaHub does not set ARMSX2 up; a PS2 core you installed yourself still starts the games"
	}
	return "In RetroArch's ARMSX2 core · " + ps2ChipNote()
}

// ps2EmulatorRow is Settings → Games & emulators → "PS2 emulator": install
// ARMSX2, or update it to the newest release.
func ps2EmulatorRow() setRow {
	return setRow{
		ID: "ps2Emulator", Label: "PS2 emulator", Kind: setKindAction,
		Visible: func(c *Config) bool { return ps2SoC() != "" && ps2Enabled(c) },
		Detail:  ps2EmulatorDetail,
		Open:    func(*Config) { ps2StartInstall() },
	}
}

// ps2EmulatorDetail is that row's second line, from armsx2.json (read once).
func ps2EmulatorDetail(*Config) string {
	if ps2Busy.Load() {
		return "Setting ARMSX2 up…"
	}
	if rel := ps2RecordGet().Release; rel != "" {
		return "ARMSX2 " + rel + " · A looks for a newer build"
	}
	return "Not set up yet · A downloads ARMSX2; the BIOS must come from your own console"
}

// ── The core's first settings ────────────────────────────────────────────

// The renderer core option and its values (libretro docs, ARMSX2).
const (
	ps2RendererKey      = "armsx2_renderer"
	ps2RendererVulkan   = "Vulkan"
	ps2RendererGL       = "OpenGL"
	ps2RendererSoftware = "Software"
)

// ps2BoostOptions adds the handheld's ARMSX2 settings to options
// (addSystemBoostOptions, compat.go): the BIOS intro skipped, and OpenGL ES
// where the firmware has no Vulkan driver. Native resolution and full EE
// speed are the core's defaults and stay: the speed hacks that remain break
// games outright.
func ps2BoostOptions(options map[string]string) {
	options["armsx2_fast_boot"] = "enabled"
	if !dolphinVulkanPresent() {
		options[ps2RendererKey] = ps2RendererGL
	}
}
