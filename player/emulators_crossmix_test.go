package main

// emulators_crossmix_test.go — the CrossMix-OS card layout: emulator folders
// whose config.json names their own launcher, PPSSPP builds kept in
// Emus/PSP/PPSSPP_<version>/, and TrimUI's ra64.trimui RetroArch binary.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// crossmixWrite creates path, and the folders above it, holding body.
func crossmixWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// crossmixEntry is the catalog row called name.
func crossmixEntry(t *testing.T, entries []emuEntry, name string) emuEntry {
	t.Helper()
	for _, e := range entries {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no %q row in %+v", name, entries)
	return emuEntry{}
}

// An Emus folder whose config.json names its own script (PSP's PPSSPP, NDS's
// DraStic) is available with that script, the one MainUI starts; launch.sh is
// only the fallback. Only launch.sh used to count, so these read "no launcher".
func TestDeviceEmulatorEntriesUsesConfigLauncher(t *testing.T) {
	emus := filepath.Join(t.TempDir(), "Emus")
	psp := filepath.Join(emus, "PSP")
	crossmixWrite(t, filepath.Join(psp, "config.json"), `{
	"label": "PSP",
	"launch": "ppsspp_1.17.1_gl.sh",
	"rompath": "../../Roms/PSP",
	"launchlist": [
		{"name": "PPSSPP 1.17.1 (GL)", "launch": "ppsspp_1.17.1_gl.sh"},
		{"name": "PPSSPP 1.17.1 (Vulkan)", "launch": "ppsspp_1.17.1_vulkan.sh"}
	]
}`)
	crossmixWrite(t, filepath.Join(psp, "ppsspp_1.17.1_gl.sh"), "#!/bin/sh\n")
	crossmixWrite(t, filepath.Join(psp, "ppsspp_1.17.1_vulkan.sh"), "#!/bin/sh\n")
	// NDS: the default script is gone, but the launchlist still has one.
	nds := filepath.Join(emus, "NDS")
	crossmixWrite(t, filepath.Join(nds, "config.json"), `{"launch": "missing.sh", "launchlist": [{"name": "DraStic", "launch": "drastic.sh"}]}`)
	crossmixWrite(t, filepath.Join(nds, "drastic.sh"), "#!/bin/sh\n")
	// SFC: the stock convention, unchanged.
	sfc := filepath.Join(emus, "SFC")
	crossmixWrite(t, filepath.Join(sfc, "config.json"), `{"launch": "launch.sh"}`)
	crossmixWrite(t, filepath.Join(sfc, "launch.sh"), "#!/bin/sh\n")
	// PS: a byte-order mark and a capitalised key; the icon still counts.
	ps := filepath.Join(emus, "PS")
	crossmixWrite(t, filepath.Join(ps, "config.json"), "\xef\xbb\xbf{\"Launch\": \"duckstation.sh\", \"icon\": \"ps.png\"}")
	crossmixWrite(t, filepath.Join(ps, "duckstation.sh"), "#!/bin/sh\n")
	crossmixWrite(t, filepath.Join(ps, "ps.png"), "png")
	// GBA: a declared launcher outside the folder is never used.
	gba := filepath.Join(emus, "GBA")
	crossmixWrite(t, filepath.Join(gba, "config.json"), `{"launch": "../escape.sh"}`)
	crossmixWrite(t, filepath.Join(emus, "escape.sh"), "#!/bin/sh\n")
	// MD: a declared file that is not a script falls back to launch.sh.
	md := filepath.Join(emus, "MD")
	crossmixWrite(t, filepath.Join(md, "config.json"), `{"launch": "picodrive"}`)
	crossmixWrite(t, filepath.Join(md, "picodrive"), "binary")
	crossmixWrite(t, filepath.Join(md, "launch.sh"), "#!/bin/sh\n")
	// Hidden and Mac metadata folders are not emulators.
	crossmixWrite(t, filepath.Join(emus, ".hidden", "launch.sh"), "#!/bin/sh\n")
	crossmixWrite(t, filepath.Join(emus, "__MACOSX", "launch.sh"), "#!/bin/sh\n")

	entries := deviceEmulatorEntries([]string{emus}, "", "")

	for _, tc := range []struct{ name, launch string }{
		{"PSP", filepath.Join(psp, "ppsspp_1.17.1_gl.sh")},
		{"NDS", filepath.Join(nds, "drastic.sh")},
		{"SFC", filepath.Join(sfc, "launch.sh")},
		{"PS", filepath.Join(ps, "duckstation.sh")},
		{"MD", filepath.Join(md, "launch.sh")},
	} {
		e := crossmixEntry(t, entries, tc.name)
		if e.Status != emuAvailable || e.Launch != tc.launch {
			t.Errorf("%s: status %d, launch %q; want available, %q", tc.name, e.Status, e.Launch, tc.launch)
		}
	}
	if e := crossmixEntry(t, entries, "PSP"); !strings.Contains(e.Detail, "ppsspp_1.17.1_gl.sh") {
		t.Errorf("PSP detail %q does not name the script that runs", e.Detail)
	}
	if e := crossmixEntry(t, entries, "SFC"); e.Detail != sfc {
		t.Errorf("SFC detail = %q, want the folder alone", e.Detail)
	}
	if e := crossmixEntry(t, entries, "PS"); e.IconPath != filepath.Join(ps, "ps.png") {
		t.Errorf("PS icon = %q, want the one config.json declares", e.IconPath)
	}
	if e := crossmixEntry(t, entries, "GBA"); e.Status != emuBroken || e.Launch != "" {
		t.Errorf("GBA used a launcher outside its folder: status %d, launch %q", e.Status, e.Launch)
	}
	for _, e := range entries {
		if e.Name == ".hidden" || e.Name == "__MACOSX" {
			t.Errorf("junk folder listed as an emulator: %+v", e)
		}
	}
	// The Games library starts PSP games with the same script.
	if got := findGameLauncher("PlayStation Portable", entries); got != filepath.Join(psp, "ppsspp_1.17.1_gl.sh") {
		t.Errorf("PSP games launch %q", got)
	}
}

// One field of an unexpected shape must not hide the others, and an oversized
// config.json is not read at all.
func TestEmuConfigLaunchNamesSkipsOddShapes(t *testing.T) {
	dir := t.TempDir()
	crossmixWrite(t, filepath.Join(dir, "config.json"),
		`{"launch": 5, "launchlist": ["a.sh", {"launch": "b.sh"}, {"launch": 7}, null, {"name": "no launcher"}]}`)
	if got := strings.Join(emuConfigLaunchNames(emuFolderConfig(dir)), ","); got != "a.sh,b.sh" {
		t.Fatalf("launch names = %q, want a.sh,b.sh", got)
	}
	crossmixWrite(t, filepath.Join(dir, "config.json"),
		`{"launch": "x.sh", "pad": "`+strings.Repeat("a", emuFolderConfigMax)+`"}`)
	if fields := emuFolderConfig(dir); fields != nil {
		t.Fatalf("oversized config.json was read: %v", fields)
	}
}

// CrossMix keeps each PPSSPP build in Emus/PSP/PPSSPP_<version>/ and runs it
// with HOME there. With several builds, the ini written last is the build that
// is played, so the stick fix lands in it.
func TestFindPPSSPPConfigInCrossMixBuildFolders(t *testing.T) {
	sd := t.TempDir()
	rel := filepath.Join(".config", "ppsspp", "PSP", "SYSTEM", "ppsspp.ini")
	older := filepath.Join(sd, "Emus", "PSP", "PPSSPP_1.15.4", rel)
	newer := filepath.Join(sd, "Emus", "PSP", "PPSSPP_1.17.1", rel)
	crossmixWrite(t, older, "[ControlMapping]\n")
	crossmixWrite(t, newer, "[ControlMapping]\n")
	base := time.Now().Add(-time.Hour)
	stamp := func(path string, at time.Time) {
		t.Helper()
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	// Written at the same moment: the newer version.
	stamp(older, base)
	stamp(newer, base)
	if got := findPPSSPPConfig([]string{sd}); got != newer {
		t.Fatalf("tie picked %q, want %q", got, newer)
	}
	// The build played last wins, whatever its version.
	stamp(older, base.Add(time.Minute))
	if got := findPPSSPPConfig([]string{sd}); got != older {
		t.Fatalf("picked %q, want the newest ini %q", got, older)
	}
	// A file another layout left behind loses to the live one...
	legacy := filepath.Join(sd, "PSP", "SYSTEM", "ppsspp.ini")
	crossmixWrite(t, legacy, "[ControlMapping]\n")
	stamp(legacy, base.Add(-time.Hour))
	if got := findPPSSPPConfig([]string{sd}); got != older {
		t.Fatalf("picked %q, want the live ini %q over a stale %q", got, older, legacy)
	}
	// ...and wins a tie, being an explicit layout.
	stamp(legacy, base.Add(time.Minute))
	if got := findPPSSPPConfig([]string{sd}); got != legacy {
		t.Fatalf("tie picked %q, want the fixed layout %q", got, legacy)
	}
}

// The first CrossMix images ran PPSSPP with the card itself as HOME.
func TestFindPPSSPPConfigCardHome(t *testing.T) {
	sd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sd, "Emus"), 0o755); err != nil {
		t.Fatal(err)
	}
	ini := filepath.Join(sd, ".config", "ppsspp", "PSP", "SYSTEM", "ppsspp.ini")
	crossmixWrite(t, ini, "")
	if got := findPPSSPPConfig([]string{sd}); got != ini {
		t.Fatalf("picked %q, want %q", got, ini)
	}
}

// A build folder without an ini is an install that has not run yet: the
// wizard says to play a game first instead of "not installed".
func TestPPSSPPEmuBuildsWithoutIni(t *testing.T) {
	sd := t.TempDir()
	build := filepath.Join(sd, "Emus", "PSP", "PPSSPP_1.19.0")
	if err := os.MkdirAll(build, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findPPSSPPConfig([]string{sd}); got != "" {
		t.Fatalf("found an ini that does not exist: %q", got)
	}
	if builds := ppssppEmuBuilds([]string{sd}); len(builds) != 1 || builds[0] != build {
		t.Fatalf("builds = %q, want [%s]", builds, build)
	}
}

// CrossMix's PSP saves live under the PPSSPP build folder; without them a
// CrossMix card's PSP saves were in no snapshot, backup or sync set. The plain
// names the sync keys are made of go to the newest build that holds saves,
// whichever build ran last, and never away from a folder that already had them.
func TestSaveRootsForFindsCrossMixPPSSPPSaves(t *testing.T) {
	// A desktop PPSSPP in the real home folder would take the plain names.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	_, sdRoot := setSaveManagerTestDirs(t)
	memstickOf := func(build string) string {
		return filepath.Join(sdRoot, "Emus", "PSP", build, ".config", "ppsspp", "PSP")
	}
	newer, older := memstickOf("PPSSPP_1.17.1"), memstickOf("PPSSPP_1.15.4")
	for _, memstick := range []string{newer, older} {
		for _, dir := range []string{"SAVEDATA", "PPSSPP_STATE"} {
			if err := os.MkdirAll(filepath.Join(memstick, dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The older build ran last; that must not move the keys.
	crossmixWrite(t, filepath.Join(older, "SYSTEM", "ppsspp.ini"), "")
	roots := saveRootsFor(sdRoot, "", "")
	if !hasSaveRoot(roots, "PPSSPP savedata", filepath.Join(newer, "SAVEDATA")) ||
		!hasSaveRoot(roots, "PPSSPP states", filepath.Join(newer, "PPSSPP_STATE")) {
		t.Fatalf("CrossMix PSP saves missing or misnamed in %+v", roots)
	}
	if !hasSaveRoot(roots, "PPSSPP savedata (Emus/PSP/PPSSPP_1.15.4)", filepath.Join(older, "SAVEDATA")) {
		t.Fatalf("older build's saves missing or misnamed in %+v", roots)
	}

	// A root that already has the plain name keeps it.
	legacy := filepath.Join(sdRoot, "PSP", "SAVEDATA")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	roots = saveRootsFor(sdRoot, "", "")
	if !hasSaveRoot(roots, "PPSSPP savedata", legacy) {
		t.Fatalf("existing PSP save folder lost its name: %+v", roots)
	}
	if !hasSaveRoot(roots, "PPSSPP savedata (Emus/PSP/PPSSPP_1.17.1)", filepath.Join(newer, "SAVEDATA")) {
		t.Fatalf("a build took the plain name from an existing folder: %+v", roots)
	}
}

// TrimUI's stock and CrossMix images ship RetroArch as ra64.trimui only; LAN
// play looked for "retroarch" and reported it missing on every handheld.
func TestNetplayFindsTrimuiRetroArch(t *testing.T) {
	sd := t.TempDir()
	ra := filepath.Join(sd, "RetroArch", "ra64.trimui")
	crossmixWrite(t, ra, "binary")
	if got := netplayCardRetroArch([]string{"", sd}); got != ra {
		t.Fatalf("found %q, want %q", got, ra)
	}
	if !netplayTrimuiBuild(ra) || netplayTrimuiBuild(filepath.Join(sd, "RetroArch", "retroarch")) {
		t.Fatal("netplayTrimuiBuild should match ra64.trimui and nothing else")
	}
	prev := netplayRetroArchOverride
	netplayRetroArchOverride = ra
	t.Cleanup(func() { netplayRetroArchOverride = prev })
	want := filepath.Join(sd, "RetroArch", ".retroarch", "cores")
	for _, dir := range netplayCoreDirs() {
		if dir == want {
			return
		}
	}
	t.Fatalf("netplay core folders %q miss %s", netplayCoreDirs(), want)
}

// The stock firmware names the PSP's ROM folder PPSSPP.
func TestScanROMLibraryReadsStockPPSSPPFolder(t *testing.T) {
	root := t.TempDir()
	crossmixWrite(t, filepath.Join(root, "PPSSPP", "Game.iso"), "iso")
	report := scanROMLibrary([]string{root})
	if len(report.Games) != 1 || report.Games[0].System != "PlayStation Portable" {
		t.Fatalf("games = %+v, want one PlayStation Portable game", report.Games)
	}
}
