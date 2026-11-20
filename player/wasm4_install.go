package main

// wasm4_install.go — where the WASM-4 core comes from, and its install.
//
// The handheld. TrimUI's firmware has glibc 2.33 (its libm stops at 2.32),
// and the libretro buildbot builds its linux/aarch64 nightlies on a newer
// Ubuntu, so a nightly may ask for a glibc the firmware does not have
// (corefix.go has the story). Two sources, in this order:
//
//  1. JukaHub's own build: the rolling release "cores-aarch64", built from
//     aduros/wasm4's runtimes/native (CMake, target wasm4_libretro) in the
//     Ubuntu 20.04 (glibc 2.31) root by .github/workflows/retroarch-cores.yml
//     through player/native/build-cores.sh, and checked there to load with
//     nothing newer than glibc 2.31. It loads on the stock firmware as it is.
//  2. The buildbot's nightly, when JukaHub's release has no wasm4 line yet.
//     It goes through the same door as every core install (fetchCoreUpdate:
//     allowlisted hosts, the index's CRC32, the size cap; activateCoreBuild:
//     the load check against this firmware). With RetroArch's C library on
//     Automatic (ra_glibc.go) that check counts JukaHub's private glibc 2.39,
//     so a nightly that only needs a newer glibc installs, and the game's
//     launch starts RetroArch on that runtime (fetched first when it is not
//     there yet). A build that cannot load either way is refused and nothing
//     is replaced.
//
// The corefix look that runs a minute after start-up treats the WASM-4 core
// like every other: a build that stops loading after a firmware change is
// replaced from JukaHub's release.
//
// A Windows PC. The buildbot's windows/x86_64 DLL, into the cores folder of
// the RetroArch the PC already has (the Emulators page finds it, or installs
// one: emuWinRetroArchCoreDirs), through the same fetch and activation. A PC
// with no RetroArch gets told where to get one; JukaHub never downloads a
// RetroArch just for this.
//
// A Linux desktop: nothing is downloaded; RetroArch's own Online Updater has
// the core (wasm4NoCoreHelp).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// wasm4InstallTimeout bounds one install: an index or two and a core under
// 1 MB.
const wasm4InstallTimeout = 5 * time.Minute

// errWASM4NotPublished is no source with a WASM-4 core for this device.
var errWASM4NotPublished = errors.New("no WASM-4 core is published for this device yet — try again in a few days")

// wasm4Source is one place the core can come from.
type wasm4Source struct {
	name  string // for the log
	index string // the buildbot-format index
	base  string // the folder its files are served from
}

// wasm4Sources lists the places to try, in order (see the file comment).
func wasm4Sources() []wasm4Source {
	buildbot := wasm4Source{name: "the libretro buildbot", index: emuCoreIndexURL(), base: emuCoreBaseURL()}
	if IsWindows() {
		return []wasm4Source{buildbot}
	}
	return []wasm4Source{{name: "JukaHub's builds for this device", index: coreFixIndexURL, base: coreFixBaseURL}, buildbot}
}

// wasm4CoreHasRetroArch reports whether a RetroArch that can load the core
// file sits where the library's launch looks for it (gameLaunchCommandArgs).
func wasm4CoreHasRetroArch(core string) bool {
	switch strings.ToLower(filepath.Ext(core)) {
	case ".dll":
		return IsWindows() && emuRetroArchForCore(core) != ""
	case ".so":
		return !IsWindows() && coreRetroArchBinary(filepath.Dir(core)) != ""
	}
	return false
}

// wasm4FindCore is the installed WASM-4 core a game starts in, only with a
// RetroArch to start it; "" when there is none. Lists folders.
func wasm4FindCore() string {
	if p := coreActiveBinary(wasm4CoreName, retroArchCoreDirs()); p != "" && wasm4CoreHasRetroArch(p) {
		return p
	}
	return ""
}

// wasm4CanInstall reports whether JukaHub installs the core here: the
// handheld (coreFixSupported: the platform JukaHub's release builds for), or
// a PC that has a RetroArch.
func wasm4CanInstall() bool {
	if coreFixSupported() {
		return true
	}
	return IsWindows() && emuCoreInstallSupported() && len(emuWinRetroArchCoreDirs()) > 0
}

// wasm4NoCoreHelp says what to do where JukaHub installs nothing.
func wasm4NoCoreHelp() string {
	if IsWindows() {
		return "WASM-4 games play in RetroArch — install RetroArch on the Emulators page, then start the game again"
	}
	return "WASM-4 games need RetroArch's WASM-4 core — get it in RetroArch's Online Updater (Core Downloader), then start the game again (docs/WASM4.md)"
}

// wasm4InstallCore installs the core from the first source that has a build
// this device can load, and returns the core file. Off the main thread; the
// caller holds the patch slot. phase names each step for the install card.
func wasm4InstallCore(ctx context.Context, phase func(string)) (string, error) {
	if IsWindows() {
		// The cores folder of the PC's RetroArch, made if it is not there yet,
		// so the activation puts the core beside that RetroArch.
		dirs := emuWinRetroArchCoreDirs()
		if len(dirs) == 0 {
			return "", errors.New(wasm4NoCoreHelp())
		}
		if err := os.MkdirAll(dirs[0], 0o755); err != nil {
			return "", err
		}
	}
	client := newEmulatorUpdateClient()
	pool := compatCorePoolDir()
	var lastErr error
	published := false
	for _, src := range wasm4Sources() {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		phase(installPhaseResolving)
		data, err := fetchUpdateBytes(ctx, client, src.index, patchMaxMetadata)
		if err != nil {
			if !strings.Contains(err.Error(), "HTTP 404") {
				lastErr = fmt.Errorf("%s: %w", src.name, err)
			}
			logInfo("library", "WASM-4: %s cannot be read: %v", src.name, err)
			continue
		}
		entry, ok := coreFixEntry(parseCoreIndex(string(data)), wasm4CoreName)
		if !ok {
			logInfo("library", "WASM-4: %s has no wasm4 core", src.name)
			continue
		}
		published = true
		if reason, refused := coreBuildRefused(wasm4CoreName, entry.CRC32); refused {
			logInfo("library", "WASM-4: %s's %s build was refused here before: %s", src.name, entry.Date, reason)
			lastErr = &coreRefusedError{Core: wasm4CoreName, Build: entry.Date, Reason: "was refused here before: " + reason}
			continue
		}
		phase(installPhaseDownload)
		build, err := fetchCoreUpdate(ctx, client, coreUpdate{
			Core:   wasm4CoreName,
			Date:   entry.Date,
			CRC32:  entry.CRC32,
			URL:    src.base + entry.File,
			Active: coreActiveBinary(wasm4CoreName, retroArchCoreDirs()),
		}, pool)
		if err != nil {
			logInfo("library", "WASM-4: %s's %s build: %v", src.name, entry.Date, err)
			lastErr = err
			continue
		}
		phase(installPhaseExtract)
		target, _, err := activateCoreBuild(build)
		emuCRCForget(target)
		if err != nil || !coreKeepActivatedInPool() {
			if dropErr := coreDropPoolBuild(pool, build); dropErr != nil {
				logDebug("library", "WASM-4: kept the pool copy of the core: %v", dropErr)
			}
		}
		if err != nil {
			// Refused (it cannot load here) or not written: the next source.
			logInfo("library", "WASM-4: %s's %s build not installed: %v", src.name, entry.Date, err)
			lastErr = err
			continue
		}
		invalidateEmulatorCache()
		invalidateCompatCache()
		coreBuildsRequestReload()
		if !wasm4CoreHasRetroArch(target) {
			return "", fmt.Errorf("there is no RetroArch beside %s to start the WASM-4 core", filepath.Dir(target))
		}
		logInfo("library", "WASM-4: core %s from %s is in: %s", entry.Date, src.name, target)
		return target, nil
	}
	if !published && lastErr == nil {
		return "", errWASM4NotPublished
	}
	if lastErr == nil {
		lastErr = errWASM4NotPublished
	}
	return "", lastErr
}
