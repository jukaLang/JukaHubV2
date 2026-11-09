package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setCompatTestDirs points every compatibility path at a temp tree. The real
// data folder is never touched by the suite.
func setCompatTestDirs(t *testing.T, dir string, retroArchRoots, coreDirs []string, pool string) {
	t.Helper()
	prevDir, prevRA := compatDirOverride, compatRetroArchRoots
	prevDirs, prevPool := compatCoreDirsOverride, compatCorePoolDirOverride
	compatDirOverride, compatRetroArchRoots = dir, retroArchRoots
	compatCoreDirsOverride, compatCorePoolDirOverride = coreDirs, pool
	invalidateCompatCache()
	resetCompatScanForTest()
	t.Cleanup(func() {
		compatDirOverride, compatRetroArchRoots = prevDir, prevRA
		compatCoreDirsOverride, compatCorePoolDirOverride = prevDirs, prevPool
		invalidateCompatCache()
		resetCompatScanForTest()
	})
}

func resetCompatScanForTest() {
	compatScan.mu.Lock()
	compatScan.scanning = false
	compatScan.built = false
	compatScan.games = nil
	compatScan.roots = nil
	compatScan.issues = nil
	compatScan.mu.Unlock()
}

func writeCompatTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCompatSeedCatalogIsValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, compatSeedJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	entries, issues, err := readCompatBundle(path)
	if err != nil {
		t.Fatalf("seed catalog does not parse: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("seed catalog has issues: %v", issues)
	}
	if len(entries) < 10 {
		t.Fatalf("seed catalog has %d entries, want a usable starter set", len(entries))
	}
	for _, entry := range entries {
		if canonicalCompatSystem(entry.System) != entry.System {
			t.Errorf("%s system %q is not canonical", entry.ID, entry.System)
		}
		if entry.Core != "" && normalizeCoreName(entry.Core) != entry.Core {
			t.Errorf("%s core %q is not normalised", entry.ID, entry.Core)
		}
		if entry.Status == compatStatusPlayable && entry.Verified {
			// Verified is allowed; this branch documents that the two are not
			// mutually exclusive and only guards against an empty title.
			if strings.TrimSpace(entry.Title) == "" {
				t.Errorf("%s has an empty title", entry.ID)
			}
		}
	}
}

func TestApplyAndRevertBoostWritesOnlyJukaHubOverride(t *testing.T) {
	dir := t.TempDir()
	raConfig := filepath.Join(dir, "retroarch", "config")
	romPath := filepath.Join(dir, "roms", "God of War (USA).iso")
	writeCompatTestFile(t, romPath, "fake rom")
	setCompatTestDirs(t, dir, []string{raConfig}, nil, filepath.Join(dir, "pool"))

	assessment := gameAssessment{
		ROM:         scannedROM{Path: romPath, System: "PlayStation Portable"},
		Key:         gameKey(scannedROM{Path: romPath, System: "PlayStation Portable"}),
		Title:       "God of War (USA)",
		Core:        "ppsspp",
		CoreVersion: "1.17.0",
		Plan: boostPlan{
			FrameSkip:       1,
			ResolutionScale: 75,
			AudioQuality:    "low",
			Options: map[string]string{
				"ppsspp_frameskip":           "1",
				"ppsspp_auto_frameskip":      "enabled",
				"ppsspp_internal_resolution": "480x272",
			},
			Recommended: true,
		},
	}
	result, err := applyBoost(assessment)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Targets) != 1 {
		t.Fatalf("apply wrote %d targets, want 1: %+v", len(result.Targets), result)
	}
	target := filepath.Join(raConfig, "PPSSPP", "God of War (USA).opt")
	if result.Targets[0] != target {
		t.Fatalf("target = %q, want %q", result.Targets[0], target)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ppsspp_frameskip = \"1\"") ||
		!strings.Contains(string(data), "ppsspp_internal_resolution = \"480x272\"") {
		t.Errorf("override = %q", data)
	}
	if !boostRecipeExists(assessment.Key) {
		t.Fatal("boost recipe was not written")
	}

	// A user-authored .opt without the JukaHub header must survive revert.
	userFile := filepath.Join(raConfig, "PPSSPP", "other.opt")
	writeCompatTestFile(t, userFile, "ppsspp_frameskip = \"0\"\n")
	removed, err := revertBoost(assessment)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) == 0 {
		t.Fatal("revert removed nothing")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("JukaHub override still exists: %v", err)
	}
	if _, err := os.Stat(userFile); err != nil {
		t.Errorf("user override was touched: %v", err)
	}
	if boostRecipeExists(assessment.Key) {
		t.Error("boost recipe still exists after revert")
	}
}

// The per-game file may be the user's own: boosting keeps their other options
// in effect, and revert puts their original file back byte for byte instead of
// deleting it.
func TestBoostKeepsAndRestoresTheUsersOwnGameOptions(t *testing.T) {
	dir := t.TempDir()
	raConfig := filepath.Join(dir, "retroarch", "config")
	romPath := filepath.Join(dir, "roms", "Crisis Core.iso")
	writeCompatTestFile(t, romPath, "fake rom")
	setCompatTestDirs(t, dir, []string{raConfig}, nil, filepath.Join(dir, "pool"))
	target := filepath.Join(raConfig, "PPSSPP", "Crisis Core.opt")
	original := "ppsspp_frameskip = \"0\"\nppsspp_button_preference = \"Circle\"\n"
	writeCompatTestFile(t, target, original)

	assessment := gameAssessment{
		ROM:   scannedROM{Path: romPath, System: "PlayStation Portable"},
		Key:   gameKey(scannedROM{Path: romPath, System: "PlayStation Portable"}),
		Title: "Crisis Core",
		Core:  "ppsspp",
		Plan:  boostPlan{Options: map[string]string{"ppsspp_frameskip": "1"}},
	}
	if _, err := applyBoost(assessment); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	boosted := string(data)
	if !strings.HasPrefix(boosted, compatBoostHeader) || !strings.Contains(boosted, `ppsspp_frameskip = "1"`) {
		t.Fatalf("boosted file = %q", boosted)
	}
	if strings.Contains(boosted, `ppsspp_frameskip = "0"`) {
		t.Fatalf("the user's value for a boosted key survived: %q", boosted)
	}
	if !strings.Contains(boosted, `ppsspp_button_preference = "Circle"`) {
		t.Fatalf("the user's other option was dropped: %q", boosted)
	}

	if _, err := revertBoost(assessment); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the user's file was not put back: %v", err)
	}
	if string(restored) != original {
		t.Fatalf("restored file = %q, want the original %q", restored, original)
	}
	if _, err := os.Stat(target + compatOptBackupSuffix); !os.IsNotExist(err) {
		t.Fatalf("the backup is still there after the restore: %v", err)
	}
}
