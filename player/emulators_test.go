package main

// Availability-list tests for the Emulators panel: catalog detection on both
// platforms (against real temp folders) and the home card wiring that used to
// dead-end on configs without an "Emulator" scene.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func writeFakeEmuFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func noLookPath(string) (string, error) { return "", os.ErrNotExist }

// TestWindowsEmulatorDetection pins the desktop fix: the catalog marks found
// installs (at the root or in their vendor folder) as available with a launch
// path, and everything else as not installed.
func TestWindowsEmulatorDetection(t *testing.T) {
	dir := t.TempDir()
	writeFakeEmuFile(t, filepath.Join(dir, "retroarch.exe"))
	writeFakeEmuFile(t, filepath.Join(dir, "PPSSPP", "PPSSPPWindows.exe"))

	entries := windowsEmulatorEntries([]string{dir}, noLookPath)
	byName := map[string]emuEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}

	ra := byName["RetroArch"]
	if ra.Status != emuAvailable || ra.Launch != filepath.Join(dir, "retroarch.exe") {
		t.Errorf("RetroArch = %+v, want available at %s", ra, filepath.Join(dir, "retroarch.exe"))
	}
	pp := byName["PPSSPP"]
	if pp.Status != emuAvailable || pp.Launch != filepath.Join(dir, "PPSSPP", "PPSSPPWindows.exe") {
		t.Errorf("PPSSPP = %+v, want available in its vendor folder", pp)
	}
	if d := byName["Dolphin"]; d.Status != emuMissing || d.Launch != "" {
		t.Errorf("Dolphin = %+v, want not installed with no launch path", d)
	}
	if len(entries) != len(winEmuCatalog) {
		t.Errorf("listed %d entries, want the full catalog of %d", len(entries), len(winEmuCatalog))
	}
}

// TestDeviceEmulatorCatalog pins the handheld list: emulator folders with
// their availability (deduped across roots), RetroArch cores, and ROM systems
// that have games but no emulator.
func TestDeviceEmulatorCatalog(t *testing.T) {
	root := t.TempDir()
	emuA := filepath.Join(root, "sd", "Emus")
	emuB := filepath.Join(root, "usr", "emus")
	roms := filepath.Join(root, "sd", "Roms")
	cores := filepath.Join(root, "sd", "RetroArch", ".retroarch", "cores")

	writeFakeEmuFile(t, filepath.Join(emuA, "GBA", "launch.sh"))
	writeFakeEmuFile(t, filepath.Join(emuB, "GBA", "launch.sh"))           // duplicate root
	if err := os.MkdirAll(filepath.Join(emuA, "PSP"), 0o755); err != nil { // no launcher
		t.Fatal(err)
	}
	writeFakeEmuFile(t, filepath.Join(roms, "FC", "game.nes"))
	writeFakeEmuFile(t, filepath.Join(roms, "GBA", "game.gba"))
	writeFakeEmuFile(t, filepath.Join(cores, "pcsx_rearmed_libretro.so"))

	entries := deviceEmulatorEntries([]string{emuA, emuB}, roms, cores)
	byName := map[string]emuEntry{}
	count := map[string]int{}
	for _, e := range entries {
		byName[e.Name] = e
		count[e.Name]++
	}

	if count["GBA"] != 1 {
		t.Errorf("GBA listed %d times, want 1 (roots must dedupe)", count["GBA"])
	}
	if g := byName["GBA"]; g.Status != emuAvailable || g.Launch == "" {
		t.Errorf("GBA = %+v, want available with a launcher", g)
	}
	if p := byName["PSP"]; p.Status != emuBroken {
		t.Errorf("PSP = %+v, want unavailable (folder without launcher)", p)
	}
	if f := byName["FC"]; f.Status != emuMissing || f.Launch != "" {
		t.Errorf("FC = %+v, want missing (ROMs but no emulator)", f)
	}
	if c := byName["pcsx_rearmed"]; c.Status != emuAvailable {
		t.Errorf("pcsx_rearmed = %+v, want available as a RetroArch core", c)
	}
}

// TestEmulatorCardOpensPanel pins the "doesn't work on Windows" bug: the card
// must open the availability panel whether or not the config has an Emulator
// scene (the desktop config has none; the handheld must get the listing too).
func TestEmulatorCardOpensPanel(t *testing.T) {
	snapshotHomeGlobals(t)
	emuPanel.open = false
	t.Cleanup(func() {
		emuPanel.open = false
		invalidateEmulatorCache()
	})

	for _, scenes := range [][]SceneConfig{
		nil,
		{{Name: "Main", Layout: "home"}, {Name: "Emulator"}},
	} {
		homeLayout = NewHomeLayout(probeW, probeH)
		if !homeLayout.Focus.Set(HomeFocusEmulator) {
			t.Fatal("Emulator card must be focusable")
		}
		homeActivate(&Config{Scenes: scenes})
		if !emuPanel.open {
			t.Fatalf("Emulator card did not open the panel (scenes=%v)", scenes)
		}
		emuPanel.open = false
	}
}

// TestEmuPanelRendersRows renders the panel offscreen and checks the click
// hit-rects cover every visible row inside the panel.
func TestEmuPanelRendersRows(t *testing.T) {
	snapshotHomeGlobals(t)
	invalidateEmulatorCache()
	emuPanel.open = true
	emuPanel.cursor = 0
	emuPanel.scroll = 0
	t.Cleanup(func() {
		emuPanel.open = false
		invalidateEmulatorCache()
	})

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	renderEmuPanel(renderer, &Config{Scenes: []SceneConfig{{Name: "Main", Layout: "home"}}})

	if emuPanelRect.W <= 0 || emuPanelRect.H <= 0 {
		t.Fatalf("panel rect not set: %+v", emuPanelRect)
	}
	if len(emuRowHits) == 0 {
		t.Fatal("panel rendered no rows")
	}
	for _, hit := range emuRowHits {
		r := hit.rect
		if r.X < emuPanelRect.X || r.Y < emuPanelRect.Y ||
			r.X+r.W > emuPanelRect.X+emuPanelRect.W ||
			r.Y+r.H > emuPanelRect.Y+emuPanelRect.H {
			t.Errorf("row rect %+v escapes the panel %+v", r, emuPanelRect)
		}
		if hit.index < 0 || hit.index >= len(winEmuCatalog)+16 {
			t.Errorf("row hit index %d out of plausible range", hit.index)
		}
	}
}
