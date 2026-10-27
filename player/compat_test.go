package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
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

func TestCompatCatalogMergesImportAndLocalOverrides(t *testing.T) {
	dir := t.TempDir()
	writeCompatTestFile(t, filepath.Join(dir, "catalog.json"), `{
  "schemaVersion": 1,
  "entries": [
    {"id": "one", "title": "One", "system": "psp", "status": "playable", "core": "PPSSPP_libretro.so"},
    {"id": "two", "title": "Two", "system": "PlayStation", "status": "performance-issues"}
  ]
}`)
	writeCompatTestFile(t, filepath.Join(dir, "import", "pack.json"), `{
  "schemaVersion": 1,
  "entries": [
    {"id": "three", "title": "Three", "system": "n64", "status": "playable"},
    {"id": "bad", "title": "Bad", "system": "n64", "status": "excellent"},
    {"id": "one", "title": "Imported One", "system": "psp", "status": "playable"}
  ]
}`)
	writeCompatTestFile(t, filepath.Join(dir, "local", "one.json"), `{
  "schemaVersion": 1,
  "id": "one",
  "title": "Local One",
  "system": "psp",
  "core": "ppsspp",
  "status": "audio-issues",
  "notes": "local override"
}`)

	entries, issues := loadCompatCatalog(dir)
	if len(entries) != 3 {
		t.Fatalf("loaded %d reports, want 3: %+v", len(entries), entries)
	}
	byID := map[string]compatEntry{}
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	if got := byID["one"]; got.Title != "Local One" || got.Source != "local" || got.Status != compatStatusAudio {
		t.Errorf("one = %+v, want the local override", got)
	}
	if got := byID["one"]; got.System != "PlayStation Portable" || got.Core != "ppsspp" {
		t.Errorf("one normalization = %q/%q, want PlayStation Portable/ppsspp", got.System, got.Core)
	}
	if got := byID["three"]; got.Source != "import" || got.System != "Nintendo 64" {
		t.Errorf("three = %+v, want imported into Nintendo 64", got)
	}
	if _, exists := byID["bad"]; exists {
		t.Error("invalid status report was loaded")
	}
	joined := strings.Join(issues, "\n")
	if !strings.Contains(joined, "bad status") && !strings.Contains(joined, "invalid status") {
		t.Errorf("issues = %v, want the invalid report reported", issues)
	}
}

func TestCompatMatchingIgnoresTagsAndUsesExplicitAliases(t *testing.T) {
	entry := compatEntry{
		SchemaVersion: 1,
		ID:            "goldeneye",
		Title:         "GoldenEye 007",
		System:        "Nintendo 64",
		Aliases:       []string{"Golden Eye", "007 GoldenEye"},
		Status:        compatStatusPlayable,
	}
	if err := validateCompatEntry(&entry); err != nil {
		t.Fatal(err)
	}
	index := compatIndex([]compatEntry{entry})
	for _, name := range []string{
		"GoldenEye 007 (USA).n64",
		"GoldenEye 007 (Europe) (Rev 2).z64",
		"Golden Eye [!].n64",
		"007 GoldenEye (Japan).v64",
	} {
		rom := scannedROM{Path: name, System: "Nintendo 64"}
		if _, ok := matchCompatEntry(rom, index); !ok {
			t.Errorf("%q did not match", name)
		}
	}
	rom := scannedROM{Path: "GoldenEye 007 (USA).n64", System: "PlayStation"}
	if _, ok := matchCompatEntry(rom, index); ok {
		t.Error("a PlayStation ROM matched an N64 report")
	}
}

func TestCompatBoostPlansRespectReportsAndTiers(t *testing.T) {
	n64 := scannedROM{Path: "Super Mario 64.z64", System: "Nintendo 64"}
	playable := compatEntry{
		SchemaVersion: 1,
		ID:            "sm64",
		Title:         "Super Mario 64",
		System:        "Nintendo 64",
		Status:        compatStatusPlayable,
		Core:          "mupen64plus_next",
	}
	if err := validateCompatEntry(&playable); err != nil {
		t.Fatal(err)
	}
	plan := buildBoostPlan(n64, playable, true)
	if plan.Recommended {
		t.Error("a playable report on a mixed system should not be auto-recommended")
	}
	if plan.FrameSkip != 0 || plan.ResolutionScale != 100 || len(plan.Options) != 0 {
		t.Errorf("playable plan = %+v, want baseline with no override", plan)
	}

	unreported := buildBoostPlan(n64, compatEntry{}, false)
	if !unreported.Recommended || unreported.FrameSkip != 0 || unreported.ResolutionScale != 75 || unreported.AudioQuality != "low" {
		t.Errorf("unreported N64 plan = %+v, want a resolution/audio boost without frameskip", unreported)
	}
	if len(unreported.Options) == 0 {
		t.Error("unreported N64 plan has no core options")
	}

	psp := scannedROM{Path: "God of War.iso", System: "PlayStation Portable"}
	slow := compatEntry{
		SchemaVersion: 1,
		ID:            "gow",
		Title:         "God of War",
		System:        "PlayStation Portable",
		Status:        compatStatusSlow,
		Core:          "ppsspp",
		Settings: compatSettings{
			FrameSkip:       2,
			ResolutionScale: 75,
			AudioQuality:    "low",
		},
	}
	if err := validateCompatEntry(&slow); err != nil {
		t.Fatal(err)
	}
	pspPlan := buildBoostPlan(psp, slow, true)
	if pspPlan.FrameSkip != 2 || pspPlan.Options["ppsspp_frameskip"] != "2" {
		t.Errorf("PSP slowdown plan = %+v, want frameskip 2 core option", pspPlan)
	}
	if pspPlan.Options["ppsspp_auto_frameskip"] != "enabled" {
		t.Errorf("PSP slowdown plan did not enable auto frameskip: %+v", pspPlan.Options)
	}

	ps1 := scannedROM{Path: "Final Fantasy VII (Disc 1).chd", System: "PlayStation"}
	solid := buildBoostPlan(ps1, compatEntry{}, false)
	if solid.Recommended || solid.FrameSkip != 0 || solid.ResolutionScale != 100 {
		t.Errorf("unreported PS1 plan = %+v, want baseline", solid)
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

func TestCoreBuildPoolScanAndActivation(t *testing.T) {
	dir := t.TempDir()
	activeDir := filepath.Join(dir, "RetroArch", ".retroarch", "cores")
	pool := filepath.Join(dir, "performance", "cores")
	oldPath := filepath.Join(activeDir, "ppsspp_libretro.so")
	newPath := filepath.Join(pool, "ppsspp", "1.18.0", "ppsspp_libretro.so")
	writeCompatTestFile(t, oldPath, "old core")
	writeCompatTestFile(t, newPath, "new core")
	setCompatTestDirs(t, filepath.Join(dir, "performance"), nil, []string{activeDir}, pool)
	// The temp tree above is what the manager sees.
	compatCorePoolDirOverride = pool

	inv := scanCoreInventory()
	if len(inv.Builds) != 1 || inv.Builds[0].Core != "ppsspp" || !inv.Builds[0].Active {
		t.Fatalf("active builds = %+v", inv.Builds)
	}
	if len(inv.Pool) != 1 || inv.Pool[0].Core != "ppsspp" || inv.Pool[0].Version != "1.18.0" {
		t.Fatalf("pool builds = %+v", inv.Pool)
	}
	if compareCoreVersions("1.18.0", "1.17.9") <= 0 {
		t.Fatal("version comparison did not see 1.18.0 as newer")
	}
	state, detail := coreStateFor("ppsspp", "1.17.0", inv)
	if state != "active" {
		t.Errorf("state with an unversioned active binary = %q (%s), want active", state, detail)
	}
	versioned := coreInventory{Active: map[string]coreBuild{"ppsspp": {Core: "ppsspp", Version: "1.16.0", Active: true}}}
	state, detail = coreStateFor("ppsspp", "1.17.0", versioned)
	if state != "outdated" {
		t.Errorf("state with active 1.16.0 = %q (%s), want outdated", state, detail)
	}

	target, backup, err := activateCoreBuild(inv.Pool[0])
	if err != nil {
		t.Fatal(err)
	}
	if target != oldPath {
		t.Fatalf("target = %q, want %q", target, oldPath)
	}
	if backup == "" || !regularFile(backup) {
		t.Fatalf("backup missing: %q", backup)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "new core" {
		t.Fatalf("target after activation = %q, %v", data, err)
	}
	if data, err := os.ReadFile(backup); err != nil || string(data) != "old core" {
		t.Fatalf("backup content = %q, %v", data, err)
	}

	inv = scanCoreInventory()
	state, detail = coreStateFor("ppsspp", "1.17.0", inv)
	if state != "active" {
		t.Errorf("after activation state = %q (%s), want active", state, detail)
	}
}

func TestCompatibilityPanelRendersGameAndCoreDetail(t *testing.T) {
	snapshotHomeGlobals(t)
	screenWidth, screenHeight = probeW, probeH
	setCompatTestDirs(t, t.TempDir(), nil, nil, "")
	romPath := filepath.Join(t.TempDir(), "God of War (USA).iso")
	writeCompatTestFile(t, romPath, "fake rom")
	game := gameAssessment{
		ROM:         scannedROM{Path: romPath, System: "PlayStation Portable"},
		Key:         gameKey(scannedROM{Path: romPath, System: "PlayStation Portable"}),
		Title:       "God of War (USA)",
		Matched:     true,
		Status:      compatStatusSlow,
		Tier:        compatTierMixed,
		Core:        "ppsspp",
		CoreVersion: "1.17.0",
		CoreState:   "active",
		CoreDetail:  "Active core · version not reported",
		Entry: compatEntry{
			SchemaVersion: 1,
			ID:            "gow",
			Title:         "God of War",
			System:        "PlayStation Portable",
			Status:        compatStatusSlow,
			Notes:         "Native resolution with frameskip 1 is the usual compromise.",
			Source:        "import",
		},
		Plan: boostPlan{
			FrameSkip:       1,
			ResolutionScale: 75,
			AudioQuality:    "low",
			Recommended:     true,
			Reason:          "Community report notes slowdowns.",
		},
	}
	compatScan.mu.Lock()
	compatScan.games = []gameAssessment{game}
	compatScan.built = true
	compatScan.mu.Unlock()
	cpPanel = compatPanelState{
		open:   true,
		tab:    compatTabGames,
		cursor: 0,
		cores: coreInventory{
			Builds: []coreBuild{{Core: "ppsspp", Version: "1.18.0", Active: true, Size: 2 << 20}},
			Pool:   []coreBuild{{Core: "ppsspp", Version: "1.19.0", Pool: true, Size: 2 << 20, Path: "/tmp/pool/ppsspp_libretro.so"}},
		},
	}
	t.Cleanup(func() { cpPanel = compatPanelState{} })

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)

	renderCompatibilityPanel(renderer, &Config{})
	if len(compatRowHits) != 1 || compatRowHits[0].index != 0 {
		t.Fatalf("game row hits = %+v, want one row", compatRowHits)
	}

	cpPanel.tab = compatTabCores
	cpPanel.cursor = 0
	renderCompatibilityPanel(renderer, &Config{})
	if len(compatRowHits) != 2 {
		t.Fatalf("core row hits = %+v, want the active and pool builds", compatRowHits)
	}
}

func TestCompatibilityPanelRendersEmptyState(t *testing.T) {
	snapshotHomeGlobals(t)
	screenWidth, screenHeight = probeW, probeH
	setCompatTestDirs(t, t.TempDir(), nil, nil, "")
	cpPanel = compatPanelState{open: true, tab: compatTabGames}
	t.Cleanup(func() { cpPanel = compatPanelState{} })

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	renderCompatibilityPanel(renderer, &Config{})
	if compatPanelRect.W <= 0 || compatPanelRect.H <= 0 {
		t.Fatalf("panel rect not set: %+v", compatPanelRect)
	}
	if len(compatTabHits) != 2 {
		t.Fatalf("tab hits = %d, want 2", len(compatTabHits))
	}
}
