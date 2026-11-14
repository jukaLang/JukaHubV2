package main

// raemu.go — RetroArch itself on the handheld's Emulators page.
//
// The page lists every core in RetroArch/.retroarch/cores as a tile of its
// own, but on the handheld a core is not something to start by itself:
// RetroArch runs it, from a system's launch script. A on a core row answered
// "start it from RetroArch" on a page that had no RetroArch to start, which a
// CrossMix tester read as RetroArch "being weird". Now a core row opens the
// Games library on the system that core plays, and RetroArch gets a tile of
// its own on the handheld: where it lives, how many cores it holds, the config
// it reads, and A opens RetroArch's own menu the way the firmware's launchers
// start it — from its folder, with HOME set there and the firmware's
// environment rather than JukaHub's (childenv.go).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// emuRetroArchTileName is the handheld's RetroArch tile.
const emuRetroArchTileName = "RetroArch"

// raConfigsIn lists the configs a RetroArch folder can hold, the one beside
// the binary first (raRetroArchConfigPaths gives the reason).
func raConfigsIn(dir string) []string {
	return []string{
		filepath.Join(dir, "retroarch.cfg"),
		filepath.Join(dir, ".retroarch", "retroarch.cfg"),
	}
}

// deviceRetroArchEntry is the RetroArch tile for the cores folder the catalog
// scans (RetroArch/.retroarch/cores), or ok=false when that RetroArch folder
// does not exist. A folder without a RetroArch binary is listed as broken
// rather than hidden: its cores are still on the page.
func deviceRetroArchEntry(coresDir string, coreCount int) (emuEntry, bool) {
	coresDir = strings.TrimSpace(coresDir)
	if coresDir == "" {
		return emuEntry{}, false
	}
	dir := filepath.Dir(filepath.Dir(coresDir))
	if !dirExists(dir) {
		return emuEntry{}, false
	}
	e := emuEntry{Name: emuRetroArchTileName, Glyph: emuGlyphFor(emuRetroArchTileName, "")}
	cores := fmt.Sprintf("%d cores", coreCount)
	if coreCount == 1 {
		cores = "1 core"
	}
	bin := ""
	for _, name := range netplayRetroArchNames {
		if p := filepath.Join(dir, name); regularFile(p) {
			bin = p
			break
		}
	}
	if bin == "" {
		e.Status = emuBroken
		e.Detail = dir + " — no RetroArch binary (ra64.trimui) · " + cores
		return e, true
	}
	settings := "no retroarch.cfg yet"
	if cfgs := raScopeTargets(raConfigsIn(dir)); len(cfgs) > 0 {
		if rel, err := filepath.Rel(dir, cfgs[0]); err == nil {
			settings = "settings in " + filepath.ToSlash(rel)
		}
	}
	e.Status = emuAvailable
	e.Launch = bin
	e.Detail = cores + " · " + dir + " · " + settings
	e.IconPath = sidecarIcon(bin)
	return e, true
}

// emuRetroArchMenuTile reports whether e is the handheld's RetroArch tile,
// which A opens with launchRetroArchMenu: its binary (ra64.trimui) is not a
// file type the generic launcher starts.
func emuRetroArchMenuTile(e emuEntry) bool {
	return !IsWindows() && !e.Pinned && e.Name == emuRetroArchTileName &&
		e.Status == emuAvailable && strings.TrimSpace(e.Launch) != "" &&
		!strings.EqualFold(filepath.Ext(e.Launch), ".sh")
}

// launchRetroArchMenu starts RetroArch with no game, which opens its own menu,
// exactly as a firmware launch script starts it apart from the core and ROM.
func launchRetroArchMenu(e emuEntry) {
	bin := strings.TrimSpace(e.Launch)
	if !regularFile(bin) {
		showLaunchFailureDialog(e.Name, fmt.Errorf("RetroArch is missing: %w", os.ErrNotExist), "")
		invalidateEmulatorCache()
		return
	}
	dir := filepath.Dir(bin)
	cmd := exec.Command(bin)
	cmd.Dir = dir
	// HOME is where TrimUI's build finds retroarch.cfg and .retroarch/; the rest
	// is the environment the firmware's menu would give it.
	cmd.Env = append(screenChildEnv(os.Environ()), "HOME="+dir)
	screenChildGroupConfigure(cmd)
	restoreProfile := beginGamePowerProfile(appConfig)
	if err := screenChildStart(cmd); err != nil {
		restoreProfile()
		logWarn("emulators", "launch failed for %s: %v", bin, err)
		showLaunchFailureDialog(e.Name, err, "")
		return
	}
	noteGameChildStarted(cmd)
	go func() {
		_ = cmd.Wait()
		screenChildReaped(cmd)
		restoreProfile()
	}()
	PlaySuccessSound()
	ShowToast("Opening RetroArch", ToastKindSuccess)
}

// emuCoreSystemQuery is the Games library search that shows a core's games:
// the system the core plays, in the library's own name for it ("Genesis" is
// the library's "Mega Drive"). "" when the core is not one the table knows.
func emuCoreSystemQuery(e emuEntry) string {
	system, _ := emuCoreInfo(e.Core)
	if system == "" {
		system, _ = emuCoreInfo(e.Name)
	}
	if system == "" {
		return ""
	}
	return canonicalCompatSystem(system)
}

// emuCoreRowActivate is A on a handheld core row: the games that core plays,
// in the Games library (the first Back there clears the search). A core the
// table does not know says where its games start instead.
func emuCoreRowActivate(e emuEntry) {
	query := emuCoreSystemQuery(e)
	if query == "" {
		PlayNavSound()
		ShowToast(e.Name+" runs inside RetroArch: its games start from their system in Games", ToastKindInfo)
		return
	}
	openGameLibraryPanel()
	setGameLibraryQuery(query)
	ShowToast(query+" games · "+e.Name+" runs them in RetroArch", ToastKindInfo)
}
