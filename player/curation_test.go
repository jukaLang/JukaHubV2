package main

// curation_test.go — the curation hub promises that a bundle only fills gaps
// and that a shared profile can never write an unknown theme or an unsafe text
// size, so both are exercised against real temp folders.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func setCurationTestDirs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prevDir := curationDirOverride
	prevProfiles, prevSaves := saveManagerDirOverride, saveManagerSDRootOverride
	curationDirOverride = dir
	saveManagerDirOverride = filepath.Join(dir, "save-manager")
	saveManagerSDRootOverride = filepath.Join(dir, "sdcard")
	// No collector or feed may leak in from the developer's environment.
	t.Setenv("JUKAHUB_CURATION_VOTES_URL", "")
	t.Setenv("JUKAHUB_CURATION_FEED", "")
	if err := ensureCurationLayout(); err != nil {
		t.Fatalf("ensureCurationLayout: %v", err)
	}
	if err := ensureSaveManagerLayout(); err != nil {
		t.Fatalf("ensureSaveManagerLayout: %v", err)
	}
	t.Cleanup(func() {
		curationDirOverride = prevDir
		saveManagerDirOverride = prevProfiles
		saveManagerSDRootOverride = prevSaves
	})
	return dir
}

func testCurationConfig() *Config {
	return &Config{Variables: Variables{Custom: map[string]interface{}{}}}
}

// stubCurationTheme keeps palette changes out of the process-wide design
// tokens: render tests later in the suite must not inherit a test theme.
func stubCurationTheme(t *testing.T) *string {
	t.Helper()
	applied := ""
	prev := curationThemeApplier
	curationThemeApplier = func(config *Config, name string, notify bool) {
		applied = name
		if config != nil {
			if config.Variables.Custom == nil {
				config.Variables.Custom = map[string]interface{}{}
			}
			config.Variables.Custom["theme_preset"] = name
		}
	}
	t.Cleanup(func() {
		curationThemeApplier = prev
		a11yRefreshFonts()
	})
	return &applied
}

func TestCurationDeviceFor(t *testing.T) {
	cases := []struct {
		device  DeviceModel
		key     string
		compact bool
	}{
		{DeviceTrimuiSmartPro, "smartpro", false},
		{DeviceTrimuiSmartProS, "smartpro-s", false},
		{DeviceTrimuiBrick, "brick", true},
		{DeviceTrimuiBrickPro, "brick-pro", true},
		{DeviceTrimuiHammer, "hammer", true},
	}
	for _, tc := range cases {
		got := curationDeviceFor(tc.device)
		if got.Key != tc.key || got.Compact != tc.compact {
			t.Fatalf("curationDeviceFor(%s) = %+v", tc.device, got)
		}
	}
	if got := curationDeviceFor(DeviceModel("unknown")); got.Key != "generic" {
		t.Fatalf("unknown device key = %q", got.Key)
	}
}

func TestCurationBundleForDevice(t *testing.T) {
	bundles := builtinCurationBundles()
	wide, ok := curationBundleForDevice(curationDeviceFor(DeviceTrimuiSmartPro), bundles)
	if !ok || wide.ID != "essentials-smartpro" {
		t.Fatalf("Smart Pro bundle = %+v", wide)
	}
	compact, ok := curationBundleForDevice(curationDeviceFor(DeviceTrimuiBrickPro), bundles)
	if !ok || compact.ID != "essentials-compact" {
		t.Fatalf("Brick bundle = %+v", compact)
	}
}

func TestBuildCurationEssentials(t *testing.T) {
	bundle, _ := curationBundleForDevice(curationDeviceFor(DeviceTrimuiSmartPro), builtinCurationBundles())

	empty := buildCurationEssentials(bundle, curationEnv{Theme: "", Battery: true})
	if empty.Ready != 1 {
		t.Fatalf("want only the battery step ready, got %d (%+v)", empty.Ready, empty.Steps)
	}
	for _, step := range empty.Steps {
		switch step.Step.Kind {
		case curationStepSaveSync, curationStepSaveSnapshot:
			if step.CanApply {
				t.Fatalf("%s must not be applicable with no save folders", step.Step.Kind)
			}
			if step.Done {
				t.Fatalf("%s cannot be done with no save folders", step.Step.Kind)
			}
		case curationStepDisplay:
			if step.Done || !step.CanApply || step.Value != "hub" {
				t.Fatalf("display step = %+v", step)
			}
		}
	}

	full := buildCurationEssentials(bundle, curationEnv{
		Roots: 2, SyncReady: true, HasSnapshot: true, Cores: 4, CoresDir: "/cores",
		Theme: "midnight", Battery: true,
	})
	if full.Ready != len(full.Steps) {
		t.Fatalf("want every step ready, got %d/%d (%+v)", full.Ready, len(full.Steps), full.Steps)
	}
	for _, step := range full.Steps {
		if step.CanApply {
			t.Fatalf("%s should not offer an action when done", step.Step.Kind)
		}
	}
}

func TestApplyCurationStepFillsGapsOnly(t *testing.T) {
	setCurationTestDirs(t)
	t.Chdir(t.TempDir())
	applied := stubCurationTheme(t)
	prev := a11y
	t.Cleanup(func() {
		a11y = prev
		a11yRefreshFonts()
	})
	config := testCurationConfig()

	// Save folders with one save file, so sync and snapshot have work to do.
	saves := filepath.Join(saveManagerSDRootOverride, "RetroArch", "saves")
	if err := os.MkdirAll(saves, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(saves, "Mario.srm"), []byte("save"), 0o644); err != nil {
		t.Fatal(err)
	}

	if msg, err := applyCurationStep(config, curationStep{Kind: curationStepSaveSync}); err != nil {
		t.Fatalf("saveSync: %v", err)
	} else if !strings.Contains(msg, "prepared") {
		t.Fatalf("saveSync message = %q", msg)
	}
	if _, err := os.Stat(filepath.Join(saves, ".stfolder")); err != nil {
		t.Fatalf("Syncthing marker missing: %v", err)
	}

	if msg, err := applyCurationStep(config, curationStep{Kind: curationStepSaveSnapshot}); err != nil {
		t.Fatalf("saveSnapshot: %v", err)
	} else if !strings.Contains(msg, "snapshot") {
		t.Fatalf("snapshot message = %q", msg)
	}
	if _, ok := latestSaveSnapshot(); !ok {
		t.Fatal("no snapshot after applying the step")
	}

	// The display step applies a theme only when none was chosen.
	if _, err := applyCurationStep(config, curationStep{Kind: curationStepDisplay, Value: "hub"}); err != nil {
		t.Fatalf("display: %v", err)
	}
	if got, _ := customString(config.Variables.Custom, "theme_preset"); got != "hub" {
		t.Fatalf("theme = %q, want hub", got)
	}
	if *applied != "hub" {
		t.Fatalf("applied theme = %q, want hub", *applied)
	}
	if msg, err := applyCurationStep(config, curationStep{Kind: curationStepDisplay, Value: "matrix"}); err != nil {
		t.Fatalf("display second run: %v", err)
	} else if !strings.Contains(msg, "already") {
		t.Fatalf("second display apply = %q, want skipped", msg)
	}
	if got, _ := customString(config.Variables.Custom, "theme_preset"); got != "hub" {
		t.Fatalf("an explicit theme was overwritten: %q", got)
	}

	// Battery: defaults to on, so the step must be a no-op until it is off.
	config.Variables.Custom["lowBatteryGuard"] = false
	if _, err := applyCurationStep(config, curationStep{Kind: curationStepBattery}); err != nil {
		t.Fatalf("battery: %v", err)
	}
	if !lowBatteryGuardEnabled(config) {
		t.Fatal("battery guard was not enabled")
	}
}

func TestApplyCurationBundleReportsFailures(t *testing.T) {
	setCurationTestDirs(t)
	t.Chdir(t.TempDir())
	prev := a11y
	t.Cleanup(func() { a11y = prev })
	config := testCurationConfig()

	// No save folders and no cores: the actionable steps fail honestly.
	result := applyCurationBundle(config, curationBundle{
		ID: "test", Name: "test",
		Steps: []curationStep{
			{Kind: curationStepSaveSync},
			{Kind: curationStepSaveSnapshot},
			{Kind: curationStepCores},
			{Kind: curationStepBattery},
		},
	})
	if len(result.Failed) < 3 {
		t.Fatalf("want failures for sync, snapshot and cores, got %+v", result)
	}
	if len(result.Applied) != 0 {
		t.Fatalf("nothing should apply with no folders: %+v", result)
	}
	if !strings.Contains(result.summary(), "failed") {
		t.Fatalf("summary does not mention failures: %q", result.summary())
	}
}

func TestNormalizeCurationProfile(t *testing.T) {
	profile := curationProfile{Name: "  My Profile  ", Theme: "matrix", TextScale: 9}
	if err := normalizeCurationProfile(&profile); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if profile.ID != "my-profile" || profile.Name != "My Profile" {
		t.Fatalf("id/name = %q/%q", profile.ID, profile.Name)
	}
	if profile.TextScale != a11yMaxScale {
		t.Fatalf("text scale = %v, want clamped to %v", profile.TextScale, a11yMaxScale)
	}
	if err := normalizeCurationProfile(&curationProfile{Name: "x", Theme: "does-not-exist"}); err == nil {
		t.Fatal("unknown theme must be rejected")
	}
	if err := normalizeCurationProfile(&curationProfile{Theme: "hub"}); err == nil {
		t.Fatal("nameless profile must be rejected")
	}
	zero := curationProfile{Name: "zero", Theme: "hub"}
	if err := normalizeCurationProfile(&zero); err != nil || zero.TextScale != 1 {
		t.Fatalf("zero scale = %v, %v", zero.TextScale, err)
	}
}

func TestDecodeCurationProfilesShapes(t *testing.T) {
	single := `{"id":"a","name":"A","theme":"hub","textScale":1}`
	profiles, err := decodeCurationProfiles([]byte(single))
	if err != nil || len(profiles) != 1 || profiles[0].ID != "a" {
		t.Fatalf("single: %+v, %v", profiles, err)
	}
	array := `[{"id":"b","name":"B","theme":"matrix"},{"id":"c","name":"C","theme":"oled"}]`
	profiles, err = decodeCurationProfiles([]byte(array))
	if err != nil || len(profiles) != 2 {
		t.Fatalf("array: %+v, %v", profiles, err)
	}
	wrapper := `{"profiles":[{"id":"d","name":"D","theme":"hub"}]}`
	profiles, err = decodeCurationProfiles([]byte(wrapper))
	if err != nil || len(profiles) != 1 || profiles[0].ID != "d" {
		t.Fatalf("wrapper: %+v, %v", profiles, err)
	}
	if _, err := decodeCurationProfiles([]byte("{}")); err == nil {
		t.Fatal("an empty object must be rejected")
	}
}

func TestCurationProfileCaptureAndApply(t *testing.T) {
	dir := setCurationTestDirs(t)
	t.Chdir(t.TempDir())
	applied := stubCurationTheme(t)
	prev := a11y
	t.Cleanup(func() {
		a11y = prev
		a11yRefreshFonts()
	})
	config := testCurationConfig()
	config.Variables.Custom["theme_preset"] = "oled"
	a11y.fontScale = 1.15

	profile, err := captureCurationProfile(config, curationDeviceFor(DeviceTrimuiSmartPro))
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if profile.Theme != "oled" || profile.TextScale != 1.15 || profile.Device != "smartpro" {
		t.Fatalf("captured profile = %+v", profile)
	}
	if _, err := os.Stat(filepath.Join(dir, "profiles", profile.ID+".json")); err != nil {
		t.Fatalf("profile file missing: %v", err)
	}

	// Change the live setup, then apply the saved one.
	config.Variables.Custom["theme_preset"] = "light"
	a11y.fontScale = 0.85
	if err := applyCurationProfile(config, profile); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got, _ := customString(config.Variables.Custom, "theme_preset"); got != "oled" {
		t.Fatalf("theme after apply = %q", got)
	}
	if *applied != "oled" {
		t.Fatalf("applied theme = %q, want oled", *applied)
	}
	if a11y.fontScale != 1.15 {
		t.Fatalf("text scale after apply = %v", a11y.fontScale)
	}
}

func TestLoadCurationProfilesLayering(t *testing.T) {
	setCurationTestDirs(t)
	// A local profile with a built-in id must win; an imported one with a new
	// id must be listed.
	local := curationProfile{ID: "smartpro-crisp", Name: "Tweaked Crisp", Theme: "oled", TextScale: 1.0}
	if _, err := writeCurationProfile(local, curationProfilesDir()); err != nil {
		t.Fatal(err)
	}
	imported := curationProfile{ID: "shared-one", Name: "Shared", Theme: "matrix", TextScale: 1.15}
	if _, err := writeCurationProfile(imported, curationImportDir()); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(curationImportDir(), "broken.json")
	if err := os.WriteFile(broken, []byte(`{"name":"Broken","theme":"nope"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	profiles, issues := loadCurationProfiles()
	if len(issues) != 1 {
		t.Fatalf("want 1 import issue, got %v", issues)
	}
	var foundTweaked, foundShared bool
	for _, profile := range profiles {
		if profile.ID == "smartpro-crisp" {
			foundTweaked = profile.Theme == "oled" && profile.Source == "local"
		}
		if profile.ID == "shared-one" {
			foundShared = profile.Source == "imported"
		}
	}
	if !foundTweaked || !foundShared {
		t.Fatalf("layering failed: tweaked=%v shared=%v profiles=%+v", foundTweaked, foundShared, profiles)
	}

	// Only saved profiles can be deleted.
	for _, profile := range profiles {
		if profile.ID == "smartpro-crisp" {
			if err := deleteCurationProfile(profile); err != nil {
				t.Fatalf("delete local: %v", err)
			}
		}
		if profile.ID == "shared-one" {
			if err := deleteCurationProfile(profile); err == nil {
				t.Fatal("imported profiles must not be deletable")
			}
		}
	}
}

func TestCurationTargetsAndVotes(t *testing.T) {
	setCurationTestDirs(t)
	games := []gameAssessment{
		{Key: "snes|mario", Title: "Mario", ROM: scannedROM{System: "SNES"}, Status: compatStatusSlow},
	}
	ports := []portEntry{
		{Name: "Portal", Launch: "/sd/Ports/Portal.sh"},
		{Name: "PortMaster", Launch: "/sd/Apps/PortMaster/launch.sh", Master: true},
		{Name: "Hint", Info: true},
	}
	targets := curationTargets(games, ports)
	if len(targets) != 2 {
		t.Fatalf("want the game and one port, got %+v", targets)
	}
	if targets[0].Kind != "game" || targets[1].Kind != "port" || targets[1].Key != "portal.sh" {
		t.Fatalf("ordering/keys = %+v", targets)
	}
	if targets[0].Detail != "Slowdowns" {
		t.Fatalf("game detail = %q", targets[0].Detail)
	}

	store := loadCurationVotes()
	if len(store.Votes) != 0 {
		t.Fatalf("fresh store has votes: %+v", store.Votes)
	}
	added, err := toggleCurationVote(&store, targets[0])
	if err != nil || !added {
		t.Fatalf("toggle on = %v, %v", added, err)
	}
	if !curationVoteIndex(store)["game:snes|mario"] {
		t.Fatal("vote index does not contain the game")
	}
	added, err = toggleCurationVote(&store, targets[0])
	if err != nil || added {
		t.Fatalf("toggle off = %v, %v", added, err)
	}
	if len(loadCurationVotes().Votes) != 0 {
		t.Fatal("vote removal was not persisted")
	}

	if _, err := toggleCurationVote(&store, targets[1]); err != nil {
		t.Fatal(err)
	}
	path, sent, err := submitCurationVotes(loadCurationVotes())
	if err != nil {
		t.Fatalf("submit without a collector: %v", err)
	}
	if sent {
		t.Fatal("an unconfigured build must not claim it sent votes")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("export missing: %v", err)
	}
	var payload struct {
		SchemaVersion int            `json:"schemaVersion"`
		Device        string         `json:"device"`
		Votes         []curationVote `json:"votes"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("export is not JSON: %v", err)
	}
	if payload.SchemaVersion != curationVoteVer || len(payload.Votes) != 1 {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestParseCurationFeed(t *testing.T) {
	feed := `{
	  "schemaVersion": 1,
	  "updated": "2026-10-21",
	  "bundles": [
	    {"id":"retro-safety","name":"Retro safety","steps":[{"kind":"saveSync"},{"kind":"display","value":"matrix"}]},
	    {"id":"bad","name":"Bad","steps":[{"kind":"rm -rf"}]}
	  ],
	  "profiles": [
	    {"id":"community-one","name":"Community","theme":"oled","textScale":1.3,"device":"smartpro"},
	    {"id":"community-bad","name":"Bad","theme":"not-a-theme"}
	  ]
	}`
	parsed, err := parseCurationFeed([]byte(feed))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.Bundles) != 1 || parsed.Bundles[0].ID != "retro-safety" {
		t.Fatalf("bundles = %+v", parsed.Bundles)
	}
	if len(parsed.Profiles) != 1 || parsed.Profiles[0].Source != "feed" {
		t.Fatalf("profiles = %+v", parsed.Profiles)
	}
	if _, err := parseCurationFeed([]byte(`{"schemaVersion":99,"bundles":[]}`)); err == nil {
		t.Fatal("a newer feed schema must be refused")
	}
}

func TestBuiltinCurationBundlesAreValid(t *testing.T) {
	for _, bundle := range builtinCurationBundles() {
		copied := bundle
		if err := validateCurationBundle(&copied); err != nil {
			t.Fatalf("%s: %v", bundle.ID, err)
		}
	}
}

// TestRenderCurationPanelPages renders every page offscreen. It catches layout
// arithmetic and nil-font mistakes that the row builders alone cannot.
func TestRenderCurationPanelPages(t *testing.T) {
	setCurationTestDirs(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("jukaconfig.json ships next to the tests: %v", err)
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	frame := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	withScreen(t, probeW, probeH)

	prevPanel, prevHits := curationPanel, curationRowHits
	t.Cleanup(func() {
		curationPanel = prevPanel
		curationRowHits = prevHits
		ClearTextCache()
	})

	device := curationDeviceFor(DeviceTrimuiSmartPro)
	bundle, _ := curationBundleForDevice(device, builtinCurationBundles())
	curationPanel = curationPanelState{
		open:   true,
		device: device,
		bundle: bundle,
		essentials: buildCurationEssentials(bundle, curationEnv{
			Roots: 1, SyncReady: true, HasSnapshot: true, Cores: 3,
			CoresDir: "/cores", Theme: "hub", Battery: true,
		}),
		profiles:      builtinCurationProfiles(),
		votes:         curationVotesFile{SchemaVersion: 1, Votes: []curationVote{{Kind: "game", Key: "snes|mario", Title: "Mario"}}},
		voteIndex:     map[string]bool{"game:snes|mario": true},
		targets:       curationTargets(nil, []portEntry{{Name: "Portal", Launch: "/sd/Ports/Portal.sh"}}),
		lastScanBuilt: true,
	}

	for _, page := range []curationPage{curationPageHome, curationPageEssentials, curationPageProfiles, curationPageVotes} {
		curationPanel.page = page
		curationPanel.cursor = 0
		curationPanel.scroll = 0
		renderer.SetDrawColor(frame.bg.R, frame.bg.G, frame.bg.B, 255)
		renderer.Clear()
		renderCurationPanel(renderer, config)

		panel := curationPanel.rect
		if panel.W <= 0 || panel.H <= 0 {
			t.Fatalf("page %d measured a %dx%d panel", page, panel.W, panel.H)
		}
		if panel.X < 0 || panel.Y < 0 || panel.X+panel.W > screenWidth || panel.Y+panel.H > screenHeight {
			t.Fatalf("page %d panel %+v is outside the %dx%d screen", page, panel, screenWidth, screenHeight)
		}
		if len(curationRowHits) == 0 {
			t.Fatalf("page %d drew no interactive rows", page)
		}
		for _, hit := range curationRowHits {
			inside := hit.rect.X >= panel.X && hit.rect.Y >= panel.Y &&
				hit.rect.X+hit.rect.W <= panel.X+panel.W && hit.rect.Y+hit.rect.H <= panel.Y+panel.H
			if !inside {
				t.Fatalf("page %d row %+v escapes panel %+v", page, hit.rect, panel)
			}
		}
	}
}
