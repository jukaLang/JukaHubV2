package main

// emucatalog_test.go — the catalog additions: Windows lists RetroArch's cores
// and the Roms/ systems nothing plays yet (offering the emulator a desktop-only
// console needs), core indexes from either buildbot tree parse, the automatic
// core pass picks its moment, and the footer offers what each tile can do.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWindowsSystemEntriesOfferWhatIsMissing: an Xbox folder with no xemu
// offers xemu, an SNES folder offers its core, and both rows disappear once the
// emulator or core is there.
func TestWindowsSystemEntriesOfferWhatIsMissing(t *testing.T) {
	root := t.TempDir()
	roms := filepath.Join(root, "Roms")
	cores := filepath.Join(root, "RetroArch", "cores")
	writeFakeEmuFile(t, filepath.Join(roms, "XBOX", "halo2.iso"))
	writeFakeEmuFile(t, filepath.Join(roms, "SNES", "chrono.sfc"))
	writeFakeEmuFile(t, filepath.Join(roms, "Banana", "x.bin"))
	if err := os.MkdirAll(cores, 0o755); err != nil {
		t.Fatal(err)
	}

	rows := windowsSystemEntries([]string{roms}, []string{cores}, nil)
	byName := map[string]emuEntry{}
	for _, e := range rows {
		byName[e.Name] = e
	}
	if xbox := byName["XBOX"]; xbox.Status != emuMissing || xbox.Via != "xemu" {
		t.Fatalf("XBOX row = %+v, want a missing system installed through xemu", xbox)
	}
	if got := emuWinStandaloneFor(byName["XBOX"]); got != "xemu" {
		t.Fatalf("Install on XBOX installs %q, want xemu", got)
	}
	if snes := byName["SNES"]; snes.Status != emuMissing || snes.Via != "" || emuCoreForEntry(snes) != "snes9x" {
		t.Fatalf("SNES row = %+v, want a missing system the snes9x core covers", snes)
	}
	if _, ok := byName["Banana"]; ok {
		t.Fatal("a folder nothing can play was offered an install")
	}

	if err := os.WriteFile(filepath.Join(cores, "snes9x_libretro.dll"), []byte("core"), 0o644); err != nil {
		t.Fatal(err)
	}
	xemu := []emuEntry{{Name: "xemu", Status: emuAvailable, Launch: `C:\Emulators\xemu\xemu.exe`}}
	rows = windowsSystemEntries([]string{roms}, []string{cores}, xemu)
	names := map[string]bool{}
	for _, e := range rows {
		names[e.Name] = true
	}
	if names["XBOX"] || names["SNES"] {
		t.Fatalf("covered systems still listed: %v", names)
	}
	if !names["snes9x"] {
		t.Fatalf("the installed DLL core is not listed: %v", names)
	}
}

// TestWindowsCoresLaunchThroughRetroArch: an installed Windows core is the
// launcher the Games library uses for its system, and it starts as the
// RetroArch beside it with -L.
func TestWindowsCoresLaunchThroughRetroArch(t *testing.T) {
	root := t.TempDir()
	retroArch := filepath.Join(root, "RetroArch", "retroarch.exe")
	cores := filepath.Join(root, "RetroArch", "cores")
	writeFakeEmuFile(t, retroArch)
	for _, core := range []string{"snes9x", "fceumm"} {
		writeFakeEmuFile(t, filepath.Join(cores, core+"_libretro.dll"))
	}
	rows := windowsSystemEntries(nil, []string{cores}, nil)
	snes := filepath.Join(cores, "snes9x_libretro.dll")
	if got := findGameLauncher("SNES", rows); got != snes {
		t.Fatalf("SNES launcher = %q, want the snes9x core", got)
	}
	if got := findGameLauncher("NES", rows); got != filepath.Join(cores, "fceumm_libretro.dll") {
		t.Fatalf("NES launcher = %q, want the fceumm core", got)
	}
	name, args := gameLaunchCommand(snes, filepath.Join(root, "Roms", "SNES", "chrono.sfc"))
	if name != retroArch {
		t.Fatalf("command = %q, want the RetroArch beside the core", name)
	}
	if len(args) != 3 || args[0] != "-L" || args[1] != snes {
		t.Fatalf("args = %v, want -L <core> <rom>", args)
	}
	if got := emuInstallHintLabel(rows[0]); got != "Launch" {
		t.Fatalf("a launchable core row offers A %q, want Launch", got)
	}
}

// TestWindowsCatalogReadsTheManifest: a folder JukaHub installed is found
// through its manifest even with the launcher one level down, and reports its
// release.
func TestWindowsCatalogReadsTheManifest(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Cemu")
	writeFakeEmuFile(t, filepath.Join(folder, "bin", "Cemu.exe"))
	if err := emuWinWriteManifest(folder, emuWinManifest{Name: "Cemu", Tag: "v2.6", Exe: filepath.Join("bin", "Cemu.exe")}); err != nil {
		t.Fatal(err)
	}
	for _, e := range windowsEmulatorEntries([]string{root}, noLookPath) {
		if e.Name != "Cemu" {
			continue
		}
		if e.Status != emuAvailable || !e.Managed || e.Version != "v2.6" || e.Launch != filepath.Join(folder, "bin", "Cemu.exe") {
			t.Fatalf("Cemu = %+v, want the managed v2.6 install", e)
		}
		if got := emuUpdateHintLabel(e); IsWindows() && got != "Update" {
			t.Fatalf("a managed install offers X %q, want Update", got)
		}
		return
	}
	t.Fatal("Cemu is missing from the catalog")
}

func TestParseCoreIndexReadsTheWindowsTree(t *testing.T) {
	entries := parseCoreIndex(strings.Join([]string{
		"2026-11-07 1a2b3c4d snes9x_libretro.dll.zip",
		"2026-11-07 5e6f7a8b mgba_libretro.dll.zip",
		"2026-11-07 00000000 RetroArch.7z",
	}, "\n"))
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want the two DLL cores", entries)
	}
	if entries[0].Core != "mgba" || entries[1].Core != "snes9x" {
		t.Fatalf("cores = %q, %q", entries[0].Core, entries[1].Core)
	}
	upd := coreUpdate{Core: "snes9x", URL: "https://buildbot.libretro.com/nightly/windows/x86_64/latest/" + entries[1].File}
	if got := coreUpdateBinary(upd); got != "snes9x_libretro.dll" {
		t.Fatalf("archive member = %q, want the DLL", got)
	}
	if got := coreUpdateBinary(coreUpdate{Core: "mgba", URL: "https://example.invalid/x.zip"}); got != "mgba_libretro.so" {
		t.Fatalf("archive member without a telling URL = %q, want the shared object", got)
	}
}

func TestCoreAutoUpdateMoment(t *testing.T) {
	cases := []struct {
		name                   string
		inGame, busy, charging bool
		idle                   time.Duration
		overdue, want          bool
	}{
		{"charging", false, false, true, 0, false, true},
		{"idle on battery", false, false, false, 3 * time.Minute, false, true},
		{"in use on battery", false, false, false, 10 * time.Second, false, false},
		{"long overdue in use", false, false, false, 10 * time.Second, true, true},
		{"never during a game", true, false, true, time.Hour, true, false},
		{"never over another install", false, true, true, time.Hour, true, false},
	}
	for _, tc := range cases {
		if got := coreAutoUpdateMoment(tc.inGame, tc.busy, tc.charging, tc.idle, tc.overdue); got != tc.want {
			t.Errorf("%s: moment = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCoreAutoUpdateOverdueFor(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	if coreAutoUpdateOverdueFor(now.Add(-24*time.Hour).Format(time.RFC3339), now) {
		t.Error("a day-old pass counts as overdue")
	}
	if !coreAutoUpdateOverdueFor(now.Add(-80*time.Hour).Format(time.RFC3339), now) {
		t.Error("a three-day-old pass does not count as overdue")
	}
	if coreAutoUpdateOverdueFor("", now) {
		t.Error("no stamp at all counts as overdue (the first pass should still wait for a quiet moment)")
	}
}

// TestEmuAutoUpdateIsOnByDefault: the handheld keeps its cores current unless
// the user turned it off, whatever shape the saved value has.
func TestEmuAutoUpdateIsOnByDefault(t *testing.T) {
	cases := []struct {
		value interface{}
		set   bool
		want  bool
	}{
		{nil, false, true},
		{true, true, true},
		{false, true, false},
		{"false", true, false},
		{"off", true, false},
		{"true", true, true},
		{float64(0), true, false},
	}
	for _, tc := range cases {
		config := &Config{}
		if tc.set {
			config.Variables.Custom = map[string]interface{}{emuAutoUpdateSetting: tc.value}
		}
		if got := emuAutoUpdateEnabled(config); got != tc.want {
			t.Errorf("setting %v: enabled = %v, want %v", tc.value, got, tc.want)
		}
	}
	if !emuAutoUpdateEnabled(nil) {
		t.Error("no config at all must mean on")
	}
}

func TestEmuAutoUpdateSummary(t *testing.T) {
	got := emuAutoUpdateSummary(emuInstallReport{Updated: []string{"mGBA", "Snes9x", "Flycast", "PPSSPP"}, Errors: []string{"x"}})
	for _, want := range []string{"Updated 4 emulator cores", "mGBA, Snes9x, Flycast", "+1 more", "1 failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q is missing %q", got, want)
		}
	}
	if got := emuAutoUpdateSummary(emuInstallReport{Updated: []string{"mGBA"}}); got != "Updated 1 emulator core (mGBA)" {
		t.Errorf("single summary = %q", got)
	}
}

// TestEmuFooterHintsOfferSetupOnXemu: on Windows the xemu tile's Y is the Xbox
// setup, never Achievements; elsewhere Y stays Achievements.
func TestEmuFooterHintsOfferSetupOnXemu(t *testing.T) {
	labels := func(e emuEntry) map[string]string {
		out := map[string]string{}
		for _, hint := range emuFooterHints(e, true) {
			out[hint.Button] = hint.Label
		}
		return out
	}
	xemu := labels(emuEntry{Name: "xemu", Status: emuAvailable, Launch: `C:\Emulators\xemu\xemu.exe`})
	want := "Achievements"
	if IsWindows() {
		want = "Setup"
	}
	if xemu["Y"] != want {
		t.Fatalf("xemu tile Y = %q, want %q", xemu["Y"], want)
	}
	if got := labels(emuEntry{Name: "PPSSPP", Status: emuAvailable, Launch: `C:\x\PPSSPPWindows64.exe`})["Y"]; got != "Achievements" {
		t.Fatalf("PPSSPP tile Y = %q, want Achievements", got)
	}
}

// TestEmuHintBadgesAreClickTargets: every badge the footer draws for a button
// is a click target, and the D-pad hint is not.
func TestEmuHintBadgesAreClickTargets(t *testing.T) {
	newProbeFrame(t)
	config := layoutTestConfig(t)
	renderer := probeRenderer(t)
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Install"}, {Button: "B", Label: "Back"}}
	hits := emuDrawHintRow(renderer, config, hints, 40, 600, 900, nil)
	if len(hits) != 2 || hits[0].button != "A" || hits[1].button != "B" {
		t.Fatalf("hits = %+v, want A and B", hits)
	}
	if hits[0].rect.X+hits[0].rect.W > hits[1].rect.X {
		t.Fatalf("badge targets overlap: %+v", hits)
	}
}
