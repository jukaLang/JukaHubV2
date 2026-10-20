package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func setGameProfileTestDirs(t *testing.T, dir string, roots []string) {
	t.Helper()
	prevDir, prevRoots := gameProfilesDirOverride, gameProfileRootsOverride
	gameProfilesDirOverride, gameProfileRootsOverride = dir, roots
	invalidateGameProfileCache()
	gameProfileRunning.mu.Lock()
	gameProfileRunning.pids = nil
	gameProfileRunning.mu.Unlock()
	t.Cleanup(func() {
		gameProfilesDirOverride, gameProfileRootsOverride = prevDir, prevRoots
		invalidateGameProfileCache()
		gameProfileRunning.mu.Lock()
		gameProfileRunning.pids = nil
		gameProfileRunning.mu.Unlock()
	})
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFakeGame(t *testing.T, dir, exe string) string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(exe))
	writeTestFile(t, path, "fake game")
	return path
}

func nativeProfile(id, title, dir, exe string) string {
	return `{
  "schemaVersion": 1,
  "id": "` + id + `",
  "title": "` + title + `",
  "runtime": "native",
  "dir": "` + dir + `",
  "executable": "` + exe + `"
}`
}

func TestGameProfilesCatalogMergesDatabaseImportAndLocalOverrides(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	writeFakeGame(t, filepath.Join(root, "Games", "One"), "one.exe")
	writeFakeGame(t, filepath.Join(root, "Games", "Two"), "two.exe")
	writeFakeGame(t, filepath.Join(root, "Games", "Three"), "three.exe")

	writeTestFile(t, filepath.Join(dir, "profiles.json"), `{
  "schemaVersion": 1,
  "profiles": [
    `+nativeProfile("one", "Database One", "Games/One", "one.exe")+`,
    `+nativeProfile("two", "Database Two", "Games/Two", "two.exe")+`
  ]
}`)
	writeTestFile(t, filepath.Join(dir, "import", "pack.json"), `{
  "schemaVersion": 1,
  "profiles": [`+nativeProfile("three", "Imported Three", "Games/Three", "three.exe")+`]
}`)
	writeTestFile(t, filepath.Join(dir, "import", "broken.json"), `{"schemaVersion":1,`)
	writeTestFile(t, filepath.Join(dir, "local", "one.json"), `{
  "schemaVersion": 1,
  "id": "one",
  "title": "Local One",
  "runtime": "native",
  "dir": "Games/One",
  "executable": "one.exe",
  "notes": "local override"
}`)

	entries, issues := loadGameProfiles(dir, []string{root})
	if len(entries) != 3 {
		t.Fatalf("loaded %d profiles, want 3: %+v", len(entries), entries)
	}
	byID := map[string]gameProfileEntry{}
	for _, entry := range entries {
		byID[entry.Profile.ID] = entry
	}
	if got := byID["one"]; got.Profile.Title != "Local One" || got.Source != "local" {
		t.Errorf("one = %+v, want the local override", got)
	}
	if got := byID["three"]; got.Source != "import" || got.Profile.Title != "Imported Three" {
		t.Errorf("three = %+v, want the imported profile", got)
	}
	if got := byID["two"]; got.Source != "database" {
		t.Errorf("two source = %q, want database", got.Source)
	}
	for _, entry := range entries {
		if !entry.ready() {
			t.Errorf("%s should be ready: %s", entry.Profile.ID, entry.Issue)
		}
	}
	if len(issues) == 0 || !strings.Contains(strings.Join(issues, "\n"), "broken.json") {
		t.Errorf("issues = %v, want the broken import file reported", issues)
	}

	// The panel cache must see the same result.
	setGameProfileTestDirs(t, dir, []string{root})
	if total, ready, _ := gameProfileStats(); total != 3 || ready != 3 {
		t.Errorf("stats = %d total, %d ready; want 3/3", total, ready)
	}
}

func TestGameProfilesRejectEscapingPaths(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFakeGame(t, filepath.Join(outside, "Game"), "game.exe")

	if err := validateGameProfile(&GameProfile{
		SchemaVersion: 1, ID: "escape", Title: "Escape", Runtime: gameRuntimeNative,
		Dir: "Game", Executable: "../outside.exe",
	}); err == nil {
		t.Error("validateGameProfile accepted an executable that leaves dir")
	}

	profile := GameProfile{
		SchemaVersion: 1, ID: "absolute", Title: "Absolute", Runtime: gameRuntimeNative,
		Dir: outside, Executable: "Game/game.exe",
	}
	if _, _, err := resolveGameProfilePaths(profile, []string{root}); err == nil {
		t.Error("resolveGameProfilePaths accepted an absolute folder outside the roots")
	}

	profile.Dir = "../outside"
	if _, _, err := resolveGameProfilePaths(profile, []string{root}); err == nil {
		t.Error("resolveGameProfilePaths accepted a relative folder that escapes")
	}

	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	profile.Dir = "linked"
	if _, _, err := resolveGameProfilePaths(profile, []string{root}); err == nil {
		t.Error("resolveGameProfilePaths followed a symlink outside the roots")
	}
}

func TestGameProfilesPlanWineAndBox64(t *testing.T) {
	tmp := t.TempDir()
	wineScript := filepath.Join(tmp, "wine")
	box64 := filepath.Join(tmp, "box64")
	wineX86 := filepath.Join(tmp, "wine-x86")
	writeTestFile(t, wineScript, "#!/bin/sh\nexit 0\n")
	writeTestFile(t, box64, "#!/bin/sh\nexit 0\n")
	// Minimal ELF header with e_machine = 0x3E (x86-64).
	elf := make([]byte, 20)
	copy(elf, []byte{0x7F, 'E', 'L', 'F', 1, 1, 1, 0})
	elf[18], elf[19] = 0x3E, 0x00
	if err := os.WriteFile(wineX86, elf, 0o755); err != nil {
		t.Fatal(err)
	}

	lookPath := func(name string) (string, error) {
		switch name {
		case "wine", "wine64":
			return wineScript, nil
		case "box64":
			return box64, nil
		case "sh":
			return "/bin/sh", nil
		}
		return "", os.ErrNotExist
	}

	t.Setenv("JUKAHUB_WINE", wineScript)
	t.Setenv("JUKAHUB_BOX64", box64)
	profile := GameProfile{Runtime: gameRuntimeWine, Args: []string{"-windowed"}}
	command, args, err := planGameRuntime(profile, "/games/game.exe", lookPath)
	if err != nil {
		t.Fatal(err)
	}
	if command != wineScript || len(args) != 2 || args[0] != "/games/game.exe" || args[1] != "-windowed" {
		t.Errorf("wine plan = %q %v, want the wine wrapper and game args", command, args)
	}

	t.Setenv("JUKAHUB_WINE", wineX86)
	command, args, err = planGameRuntime(profile, "/games/game.exe", lookPath)
	if err != nil {
		t.Fatal(err)
	}
	if command != box64 || len(args) != 3 || args[0] != wineX86 || args[1] != "/games/game.exe" {
		t.Errorf("x86 wine plan = %q %v, want box64 + wine + game", command, args)
	}

	command, args, err = planGameRuntime(GameProfile{Runtime: gameRuntimeBox64}, "/games/game.exe", lookPath)
	if err != nil {
		t.Fatal(err)
	}
	if command != box64 || len(args) != 1 || args[0] != "/games/game.exe" {
		t.Errorf("box64 plan = %q %v", command, args)
	}

	command, args, err = planGameRuntime(GameProfile{Runtime: gameRuntimeScript}, "/games/run.sh", lookPath)
	if err != nil {
		t.Fatal(err)
	}
	if command != "/bin/sh" || len(args) != 1 || args[0] != "/games/run.sh" {
		t.Errorf("script plan = %q %v", command, args)
	}
}

func TestGameProfilesMergeEnvironmentDeterministically(t *testing.T) {
	base := []string{"PATH=/old", "HOME=/home/player", "WINEPREFIX=/old/prefix"}
	env := mergeGameProfileEnv(base, map[string]string{
		"PATH":       "/new",
		"WINEPREFIX": "/new/prefix",
		"BOX64_LOG":  "1",
	})
	want := map[string]string{"PATH": "/new", "HOME": "/home/player", "WINEPREFIX": "/new/prefix", "BOX64_LOG": "1"}
	got := map[string]string{}
	for _, item := range env {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			t.Fatalf("malformed env item %q", item)
		}
		if _, exists := got[parts[0]]; exists {
			t.Errorf("duplicate env key %q", parts[0])
		}
		got[parts[0]] = parts[1]
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
}

func TestGameProfileExportRoundTrips(t *testing.T) {
	dir := t.TempDir()
	setGameProfileTestDirs(t, dir, nil)
	entry := gameProfileEntry{Profile: GameProfile{
		SchemaVersion:  1,
		ID:             "export-me",
		Title:          "Export Me",
		Runtime:        gameRuntimeBox64,
		Dir:            "Games/Export",
		Executable:     "game.exe",
		Args:           []string{"-fullscreen"},
		Env:            map[string]string{"BOX64_LOG": "1"},
		TestedFirmware: "TrimUI Smart Pro 1.1",
		Notes:          "test",
	}, Source: "local"}

	path, err := exportGameProfile(entry)
	if err != nil {
		t.Fatal(err)
	}
	// A second export replaces the previous file instead of failing (Windows
	// rename semantics used to make repeat exports fail).
	if _, err := exportGameProfile(entry); err != nil {
		t.Fatalf("re-export failed: %v", err)
	}
	if filepath.Dir(path) != gameProfilesExportDir() {
		t.Errorf("exported to %s, want %s", path, gameProfilesExportDir())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"source"`) {
		t.Error("export leaked the internal source field")
	}
	profiles, issues, err := readGameProfileBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("round-trip issues = %v", issues)
	}
	if len(profiles) != 1 || profiles[0].ID != entry.Profile.ID || profiles[0].Title != entry.Profile.Title {
		t.Errorf("round trip = %+v", profiles)
	}
	if len(profiles[0].Args) != 1 || profiles[0].Args[0] != "-fullscreen" {
		t.Errorf("args = %v", profiles[0].Args)
	}
}

func TestLaunchGameProfileRunsAndReaps(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not available")
	}
	dir := t.TempDir()
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "launched.txt")
	gameDir := filepath.Join(root, "Games", "ScriptGame")
	script := "#!/bin/sh\nprintf launched > " + strings.ReplaceAll(marker, "\\", "/") + "\n"
	writeTestFile(t, filepath.Join(gameDir, "run.sh"), script)
	if err := os.Chmod(filepath.Join(gameDir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	setGameProfileTestDirs(t, dir, []string{root})

	profile := GameProfile{
		SchemaVersion: 1, ID: "script-game", Title: "Script Game",
		Runtime: gameRuntimeScript, Dir: "Games/ScriptGame", Executable: "run.sh",
	}
	entry := resolveGameProfileEntry(profile, "local", []string{root})
	if !entry.ready() {
		t.Fatalf("profile is not ready: %s", entry.Issue)
	}
	if err := launchGameProfile(entry); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("launched script did not write its marker")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for {
		if gameProfileRunningPID(profile.ID) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("finished script was not reaped")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "logs", profile.ID+".log")); err != nil {
		t.Errorf("launch log missing: %v", err)
	}
}

func TestGameProfilesPanelRendersRows(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	writeFakeGame(t, filepath.Join(root, "Games", "One"), "one.exe")
	writeFakeGame(t, filepath.Join(root, "Games", "Two"), "two.exe")
	writeTestFile(t, filepath.Join(dir, "profiles.json"), `{
  "schemaVersion": 1,
  "profiles": [
    `+nativeProfile("one", "One", "Games/One", "one.exe")+`,
    `+nativeProfile("two", "Two", "Games/Two", "two.exe")+`
  ]
}`)
	setGameProfileTestDirs(t, dir, []string{root})

	snapshotHomeGlobals(t)
	gpPanel = gameProfilePanelState{open: true}
	t.Cleanup(func() {
		gpPanel = gameProfilePanelState{}
		gameProfileRowHits = nil
		gameProfilePanelRect = sdl.Rect{}
	})

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()
	renderGameProfilesPanel(renderer, &Config{})

	if gameProfilePanelRect.W <= 0 || gameProfilePanelRect.H <= 0 {
		t.Fatalf("panel rect not set: %+v", gameProfilePanelRect)
	}
	if len(gameProfileRowHits) != 2 {
		t.Fatalf("rendered %d row hits, want 2", len(gameProfileRowHits))
	}
	for _, hit := range gameProfileRowHits {
		r := hit.rect
		if r.X < gameProfilePanelRect.X || r.Y < gameProfilePanelRect.Y ||
			r.X+r.W > gameProfilePanelRect.X+gameProfilePanelRect.W ||
			r.Y+r.H > gameProfilePanelRect.Y+gameProfilePanelRect.H {
			t.Errorf("row rect %+v escapes the panel %+v", r, gameProfilePanelRect)
		}
	}
}

func TestGameProfilesPanelScrollsOnSmallScreen(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	var bundle strings.Builder
	bundle.WriteString(`{"schemaVersion": 1, "profiles": [`)
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("game-%02d", i)
		dirName := "Games/" + id
		writeFakeGame(t, filepath.Join(root, filepath.FromSlash(dirName)), "game.exe")
		if i > 0 {
			bundle.WriteString(",")
		}
		bundle.WriteString(nativeProfile(id, "Game "+id, dirName, "game.exe"))
	}
	bundle.WriteString(`]}`)
	writeTestFile(t, filepath.Join(dir, "profiles.json"), bundle.String())
	setGameProfileTestDirs(t, dir, []string{root})

	snapshotHomeGlobals(t)
	screenWidth, screenHeight = probeW, probeH
	gpPanel = gameProfilePanelState{open: true, cursor: 19}
	t.Cleanup(func() {
		gpPanel = gameProfilePanelState{}
		gameProfileRowHits = nil
		gameProfilePanelRect = sdl.Rect{}
	})

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()
	renderGameProfilesPanel(renderer, &Config{})

	if gameProfilePanelRect.Y < 0 || gameProfilePanelRect.Y+gameProfilePanelRect.H > screenHeight {
		t.Fatalf("panel %+v leaves the %dx%d screen", gameProfilePanelRect, screenWidth, screenHeight)
	}
	if len(gameProfileRowHits) == 0 || len(gameProfileRowHits) >= 20 {
		t.Fatalf("rendered %d rows, want a scrolled window smaller than 20", len(gameProfileRowHits))
	}
	last := gameProfileRowHits[len(gameProfileRowHits)-1]
	if last.index != 19 {
		t.Errorf("last visible row index = %d, want the focused profile 19", last.index)
	}
}

func TestGameProfilesPanelEmptyStateRenders(t *testing.T) {
	dir := t.TempDir()
	setGameProfileTestDirs(t, dir, nil)
	snapshotHomeGlobals(t)
	gpPanel = gameProfilePanelState{open: true}
	t.Cleanup(func() {
		gpPanel = gameProfilePanelState{}
		gameProfileRowHits = nil
		gameProfilePanelRect = sdl.Rect{}
	})

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()
	renderGameProfilesPanel(renderer, &Config{})
	if gameProfilePanelRect.W <= 0 {
		t.Fatalf("empty panel rect not set: %+v", gameProfilePanelRect)
	}
	if len(gameProfileRowHits) != 0 {
		t.Errorf("empty catalog rendered %d row hits", len(gameProfileRowHits))
	}
}

func TestGameProfilePanelReturnsToEmulators(t *testing.T) {
	dir := t.TempDir()
	setGameProfileTestDirs(t, dir, nil)
	emuPanel = emuPanelState{open: true, cursor: 4, scroll: 2}
	t.Cleanup(func() { emuPanel = emuPanelState{} })

	openGameProfilePanel(true)
	if !gpPanel.open || emuPanel.open {
		t.Fatal("opening Game Profiles did not replace the emulator list")
	}
	closeGameProfilePanel()
	if gpPanel.open || !emuPanel.open || emuPanel.cursor != 4 || emuPanel.scroll != 2 {
		t.Fatalf("closing did not restore the emulator list: panel=%+v emu=%+v", gpPanel, emuPanel)
	}
}

func TestGameProfilesEmulatorEntryIsPinned(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	writeFakeGame(t, filepath.Join(root, "Games", "One"), "one.exe")
	writeTestFile(t, filepath.Join(dir, "profiles.json"), `{
  "schemaVersion": 1,
  "profiles": [`+nativeProfile("one", "One", "Games/One", "one.exe")+`]
}`)
	setGameProfileTestDirs(t, dir, []string{root})
	invalidateEmulatorCache()
	t.Cleanup(invalidateEmulatorCache)

	found := false
	for _, entry := range emulatorEntries() {
		if entry.Name == gameProfilesEntryName {
			found = true
			if !entry.Pinned || entry.Status != emuAvailable {
				t.Errorf("Game Profiles entry = %+v, want pinned and available", entry)
			}
			if !strings.Contains(entry.Detail, "1 profile(s)") || !strings.Contains(entry.Detail, "1 ready") {
				t.Errorf("Game Profiles detail = %q, want catalog totals", entry.Detail)
			}
		}
	}
	if !found {
		t.Fatal("Emulators list has no Game Profiles row")
	}
}

func TestGameProfileBundleKeepsValidEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pack.json")
	writeTestFile(t, path, `{
  "schemaVersion": 1,
  "profiles": [
    `+nativeProfile("good", "Good", "Games/Good", "good.exe")+`,
    {
      "schemaVersion": 1,
      "id": "bad",
      "title": "Bad",
      "runtime": "native",
      "dir": "Games/Bad"
    }
  ]
}`)
	profiles, issues, err := readGameProfileBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].ID != "good" {
		t.Fatalf("profiles = %+v, want only good", profiles)
	}
	if len(issues) != 1 || !strings.Contains(issues[0], "executable") {
		t.Fatalf("issues = %v, want the malformed entry reported", issues)
	}
}

func TestGameProfilesRoundTripBundleJSON(t *testing.T) {
	// Guard the exported shape against accidental internal fields and against
	// the empty starter catalog being mistaken for a single profile.
	raw, err := json.Marshal(gameProfileCatalogFile{SchemaVersion: 1, Profiles: []GameProfile{}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	profiles, issues, err := readGameProfileBundle(path)
	if err != nil {
		t.Fatalf("empty starter catalog failed to load: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("empty catalog reported issues: %v", issues)
	}
	if len(profiles) != 0 {
		t.Errorf("empty catalog loaded %d profiles", len(profiles))
	}
}
