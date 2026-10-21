package main

// Tests for the game library: filename cleanup, console grouping, launcher
// resolution, the launch command shape, recents persistence and the modal
// panel's geometry and navigation.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func TestGameDisplayTitle(t *testing.T) {
	cases := map[string]string{
		"/roms/GBA/Pokemon Emerald.gba":            "Pokemon Emerald",
		"/roms/GBA/Pokemon_Emerald_(USA).gba":      "Pokemon Emerald",
		"/roms/SNES/Yoshi's Island (USA) [!].sfc":  "Yoshi's Island",
		"/roms/FC/Contra (Japan) (Rev 1).nes":      "Contra",
		"/roms/PSX/Final Fantasy VII (Disc 1).chd": "Final Fantasy VII",
		"/roms/GBA/.gba":                           "",
		"/roms/GBA/Chrono Trigger (USA).gba":       "Chrono Trigger",
	}
	for in, want := range cases {
		if got := gameDisplayTitle(in); got != want {
			t.Errorf("gameDisplayTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildGameLibraryGroupsSortsAndOrdersConsoles(t *testing.T) {
	report := romScanReport{
		Roots: []string{"/roms"},
		Games: []scannedROM{
			{Path: "/roms/GBA/Zelda.gba", System: "Game Boy Advance"},
			{Path: "/roms/FC/Contra.nes", System: "NES"},
			{Path: "/roms/GBA/Aladdin.gba", System: "Game Boy Advance"},
			{Path: "/roms/Arcade/Pac-Man.zip", System: "Arcade"},
		},
	}
	lib := buildGameLibrary(report, "/art")

	if lib.Total != 4 {
		t.Fatalf("Total = %d, want 4", lib.Total)
	}
	if len(lib.Systems) != 3 {
		t.Fatalf("systems = %d, want 3", len(lib.Systems))
	}
	// Known consoles come first in consoleOrder, unknown systems last.
	if lib.Systems[0].Name != "NES" || lib.Systems[1].Name != "Game Boy Advance" || lib.Systems[2].Name != "Arcade" {
		t.Fatalf("system order = %v, want NES, Game Boy Advance, Arcade",
			[]string{lib.Systems[0].Name, lib.Systems[1].Name, lib.Systems[2].Name})
	}
	gba := lib.Systems[1]
	if gba.Games[0].Title != "Aladdin" || gba.Games[1].Title != "Zelda" {
		t.Errorf("GBA order = %q, %q; want Aladdin then Zelda", gba.Games[0].Title, gba.Games[1].Title)
	}
	wantArt := filepath.Join("/art", "Game_Boy_Advance", "Zelda.png")
	if gba.Games[1].Art != wantArt {
		t.Errorf("art path = %q, want %q", gba.Games[1].Art, wantArt)
	}
	if lib.Roots[0] != "/roms" {
		t.Errorf("roots = %v, want the scanned root", lib.Roots)
	}
}

func TestFindGameLauncherResolvesAliases(t *testing.T) {
	entries := []emuEntry{
		{Name: "RetroAchievements", Status: emuAvailable, Pinned: true}, // no launcher
		{Name: "GBA", Status: emuAvailable, Launch: "/emus/GBA/launch.sh"},
		{Name: "mGBA", Status: emuAvailable, Launch: "/emus/mGBA/launch.sh"},
		{Name: "PSP", Status: emuBroken},
	}
	if got := findGameLauncher("Game Boy Advance", entries); got != "/emus/GBA/launch.sh" {
		t.Errorf("GBA launcher = %q, want the exact alias", got)
	}
	if got := findGameLauncher("PlayStation Portable", entries); got != "" {
		t.Errorf("broken PSP entry resolved to %q, want no launcher", got)
	}
	if got := findGameLauncher("Dreamcast", entries); got != "" {
		t.Errorf("unknown system resolved to %q, want none", got)
	}

	// A launcher whose name merely contains the alias is the fallback.
	fallback := []emuEntry{{Name: "RetroArch SNES", Status: emuAvailable, Launch: "/emus/RA/launch.sh"}}
	if got := findGameLauncher("SNES", fallback); got != "/emus/RA/launch.sh" {
		t.Errorf("contains-match launcher = %q, want the RetroArch row", got)
	}
}

func TestGameLauncherOverrides(t *testing.T) {
	dir := t.TempDir()
	launcher := filepath.Join(dir, "my-snes.sh")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	overrides := filepath.Join(dir, "game-launchers.json")
	body := fmt.Sprintf(`{"SNES": %q, "dreamcast": "/missing/launch.sh", "NES": ""}`, launcher)
	if err := os.WriteFile(overrides, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	parsed := loadGameLauncherOverrides(overrides)
	if len(parsed) != 2 {
		t.Fatalf("parsed %d overrides, want 2 (blank values are dropped): %v", len(parsed), parsed)
	}
	if got := configuredGameLauncher("snes", parsed); got != launcher {
		t.Errorf("SNES override = %q, want %q", got, launcher)
	}
	// A stale path falls back to detection instead of launching nothing.
	if got := configuredGameLauncher("Dreamcast", parsed); got != "" {
		t.Errorf("missing-file override = %q, want empty", got)
	}
	if got := loadGameLauncherOverrides(filepath.Join(dir, "absent.json")); len(got) != 0 {
		t.Errorf("absent override file parsed %v, want empty", got)
	}
}

func TestGameLaunchCommand(t *testing.T) {
	cases := []struct {
		launcher string
		wantName string
		wantArgs []string
	}{
		{"/emus/GBA/launch.sh", "sh", []string{"/emus/GBA/launch.sh", "/roms/GBA/a.gba"}},
		{"/emus/GBA/launch", "sh", []string{"/emus/GBA/launch", "/roms/GBA/a.gba"}},
		{`C:\Emus\SNES\launch.bat`, "cmd", []string{"/c", `C:\Emus\SNES\launch.bat`, "/roms/GBA/a.gba"}},
		{`C:\Emus\PSP\PPSSPP.exe`, `C:\Emus\PSP\PPSSPP.exe`, []string{"/roms/GBA/a.gba"}},
	}
	for _, c := range cases {
		name, args := gameLaunchCommand(c.launcher, "/roms/GBA/a.gba")
		if name != c.wantName {
			t.Errorf("%s: command = %q, want %q", c.launcher, name, c.wantName)
		}
		if len(args) != len(c.wantArgs) {
			t.Fatalf("%s: args = %v, want %v", c.launcher, args, c.wantArgs)
		}
		for i := range args {
			if args[i] != c.wantArgs[i] {
				t.Errorf("%s: args = %v, want %v", c.launcher, args, c.wantArgs)
				break
			}
		}
	}
}

// resetGameRecentsState clears the package-level recents cache between tests.
func resetGameRecentsState(t *testing.T) {
	t.Helper()
	prevFile, prevClock := gameRecentsFile, gameClock
	gameRecentsMu.Lock()
	prevItems, prevLoaded := gameRecents, gameRecentsLoaded
	gameRecents, gameRecentsLoaded = nil, false
	gameRecentsMu.Unlock()
	t.Cleanup(func() {
		gameRecentsFile, gameClock = prevFile, prevClock
		gameRecentsMu.Lock()
		gameRecents, gameRecentsLoaded = prevItems, prevLoaded
		gameRecentsMu.Unlock()
	})
}

func TestRecordGameLaunchDedupesAndPersists(t *testing.T) {
	resetGameRecentsState(t)
	path := filepath.Join(t.TempDir(), "game-recents.json")
	gameRecentsFile = func() string { return path }
	base := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	gameClock = func() time.Time { return base }

	zelda := libraryGame{Path: "/roms/GBA/Zelda.gba", System: "Game Boy Advance", Title: "Zelda"}
	contra := libraryGame{Path: "/roms/FC/Contra.nes", System: "NES", Title: "Contra"}

	recordGameLaunch(zelda)
	gameClock = func() time.Time { return base.Add(time.Minute) }
	recordGameLaunch(contra)
	gameClock = func() time.Time { return base.Add(2 * time.Minute) }
	recordGameLaunch(zelda) // moves back to the top, no duplicate

	got := recentGames()
	if len(got) != 2 {
		t.Fatalf("recents = %d entries, want 2: %+v", len(got), got)
	}
	if got[0].Path != zelda.Path || got[1].Path != contra.Path {
		t.Errorf("recents order = %q, %q; want the last played first", got[0].Path, got[1].Path)
	}

	// The file must survive a restart: reload it from disk.
	reloaded := loadGameRecentsFile(path)
	if len(reloaded) != 2 || reloaded[0].Title != "Zelda" {
		t.Fatalf("reloaded recents = %+v, want Zelda first", reloaded)
	}
	if !reloaded[0].PlayedAt.After(reloaded[1].PlayedAt) {
		t.Errorf("timestamps not ordered: %v then %v", reloaded[0].PlayedAt, reloaded[1].PlayedAt)
	}

	// The cap keeps the file small on a card that lasts years.
	for i := 0; i < maxRecentGames+4; i++ {
		gameClock = func() time.Time { return base.Add(time.Duration(i+3) * time.Minute) }
		recordGameLaunch(libraryGame{
			Path:   fmt.Sprintf("/roms/GBA/game-%d.gba", i),
			System: "Game Boy Advance",
			Title:  fmt.Sprintf("Game %d", i),
		})
	}
	if got := recentGames(); len(got) != maxRecentGames {
		t.Errorf("recents after cap = %d, want %d", len(got), maxRecentGames)
	}
}

// TestLaunchLibraryGamePassesROMAndRecords checks the whole launch path: the
// launcher really runs, the ROM path arrives as its first argument, and the
// game lands at the top of the recents list.
func TestLaunchLibraryGamePassesROMAndRecords(t *testing.T) {
	resetGameRecentsState(t)
	dir := t.TempDir()
	gameRecentsFile = func() string { return filepath.Join(dir, "game-recents.json") }

	rom := filepath.Join(dir, "Zelda.gba")
	if err := os.WriteFile(rom, []byte("rom"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "args.txt")
	launcher := filepath.Join(dir, "launch.sh")
	script := "#!/bin/sh\nprintf '%s' \"$1\" > \"" + marker + "\"\n"
	if IsWindows() {
		// Windows still runs the .bat branch, which is the one desktop users
		// hit; the POSIX argument shape is covered by the command test.
		launcher = filepath.Join(dir, "launch.bat")
		script = "@echo off\r\n(echo %~1)> \"" + marker + "\"\r\n"
	}
	if err := os.WriteFile(launcher, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	game := libraryGame{Path: rom, System: "Game Boy Advance", Title: "Zelda"}
	if err := launchLibraryGame(game, launcher); err != nil {
		t.Fatalf("launchLibraryGame: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	var written string
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(marker); err == nil && len(data) > 0 {
			written = strings.TrimSpace(string(data))
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if written != rom {
		t.Fatalf("launcher got %q, want the ROM path %q", written, rom)
	}

	recents := recentGames()
	if len(recents) != 1 || recents[0].Path != rom || recents[0].Title != "Zelda" {
		t.Fatalf("recents = %+v, want the launched game", recents)
	}
}

func TestLaunchLibraryGameRejectsMissingPieces(t *testing.T) {
	dir := t.TempDir()
	rom := filepath.Join(dir, "game.gba")
	if err := os.WriteFile(rom, []byte("rom"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := launchLibraryGame(libraryGame{Path: filepath.Join(dir, "absent.gba")}, rom); err == nil {
		t.Error("missing ROM launched without an error")
	}
	if err := launchLibraryGame(libraryGame{Path: rom}, ""); err == nil {
		t.Error("empty launcher launched without an error")
	}
}

func TestRecordGameLaunchSurvivesUnwritableFile(t *testing.T) {
	resetGameRecentsState(t)
	// A directory path is not a writable file: recording must still update the
	// in-memory list so the Continue row works for this session.
	gameRecentsFile = func() string { return t.TempDir() }
	recordGameLaunch(libraryGame{Path: "/roms/FC/Contra.nes", System: "NES", Title: "Contra"})
	if got := recentGames(); len(got) != 1 {
		t.Fatalf("recents = %v, want the in-memory entry", got)
	}
}

// TestGameLibraryPanelRendersAndNavigates draws the real panel offscreen and
// checks the geometry the pointer uses, then walks the list with the pad.
func TestGameLibraryPanelRendersAndNavigates(t *testing.T) {
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	withScreen(t, probeW, probeH)
	ClearTextCache()

	prevAccent := accentColor
	accentColor = ColorAccent
	// Art textures belong to this renderer, so they are released before it.
	t.Cleanup(func() {
		closeGameLibraryArt()
		destroyTestRenderer(renderer)
		gameLib = gameLibraryPanel{}
		accentColor = prevAccent
	})

	root := t.TempDir()
	roms := filepath.Join(root, "Roms")
	for _, rel := range []string{
		"GBA/Zelda (USA).gba",
		"GBA/Aladdin.gba",
		"FC/Contra.nes",
	} {
		p := filepath.Join(roms, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("rom"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gameLib.open = true
	gameLib.lib = buildGameLibrary(scanROMLibrary([]string{roms}), filepath.Join(root, "art"))
	gameLib.recents = nil
	gameLib.focus = 1
	gameLib.system = 1 // Game Boy Advance (NES is first, GBA second)
	gameLib.cursor = 0
	gameLib.scroll = 0

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderGameLibraryPanel(renderer, &Config{})

	if gameLib.panel.W <= 0 || gameLib.panel.H <= 0 {
		t.Fatal("panel rect was not measured")
	}
	if gameLib.panel.X < 0 || gameLib.panel.Y < 0 ||
		gameLib.panel.X+gameLib.panel.W > probeW || gameLib.panel.Y+gameLib.panel.H > probeH {
		t.Fatalf("panel %+v is outside %dx%d", gameLib.panel, probeW, probeH)
	}
	if len(gameLib.systemHits) != 2 {
		t.Errorf("console rows = %d, want 2 (NES, GBA)", len(gameLib.systemHits))
	}
	if len(gameLib.gameHits) != 2 {
		t.Fatalf("game rows = %d, want the two GBA games", len(gameLib.gameHits))
	}
	if gameLib.artRect.W <= 0 {
		t.Fatal("art preview was not laid out")
	}
	for _, hit := range gameLib.gameHits {
		if !pointInRect(hit.rect.X+4, hit.rect.Y+4, gameLib.panel) {
			t.Errorf("game row %+v escapes the panel %+v", hit.rect, gameLib.panel)
		}
	}
	// The three columns must not overlap, and the preview stays inside the panel.
	sysRight := gameLib.systemHits[0].rect.X + gameLib.systemHits[0].rect.W
	gameLeft := gameLib.gameHits[0].rect.X
	gameRight := gameLib.gameHits[0].rect.X + gameLib.gameHits[0].rect.W
	if sysRight > gameLeft {
		t.Errorf("console column ends at %d, past the game column at %d", sysRight, gameLeft)
	}
	if gameLib.artRect.W > 0 && gameRight > gameLib.artRect.X {
		t.Errorf("game column ends at %d, past the art preview at %d", gameRight, gameLib.artRect.X)
	}
	if gameLib.artRect.W > 0 && gameLib.artRect.X+gameLib.artRect.W > gameLib.panel.X+gameLib.panel.W {
		t.Errorf("art preview %+v escapes the panel %+v", gameLib.artRect, gameLib.panel)
	}

	// D-pad navigation stays inside the list and the columns.
	gameLibraryMove(0, 1)
	if gameLib.cursor != 1 {
		t.Errorf("cursor after Down = %d, want 1", gameLib.cursor)
	}
	gameLibraryMove(0, 1)
	if gameLib.cursor != 0 {
		t.Errorf("cursor wrapped to %d, want 0", gameLib.cursor)
	}
	gameLibraryMove(-1, 0)
	if gameLib.focus != 0 {
		t.Errorf("focus after Left = %d, want the console column", gameLib.focus)
	}
	gameLibraryMove(0, 1)
	if gameLib.system != 0 {
		t.Errorf("system after Down = %d, want NES", gameLib.system)
	}
	if got := gameLibraryStatus(); got == "" {
		t.Error("status line is empty")
	}
}

// TestLibrarySystemsForUILeadsWithContinue pins the Game Switcher behaviour:
// the recents row is first and disappears when nothing has been played.
func TestLibrarySystemsForUILeadsWithContinue(t *testing.T) {
	if got := librarySystemsForUI(gameLibraryReport{}, nil); len(got) != 0 {
		t.Fatalf("empty library produced %d rows, want 0", len(got))
	}
	lib := gameLibraryReport{Systems: []librarySystem{{Name: "NES", Games: []libraryGame{{Path: "/roms/FC/Contra.nes"}}}}}
	recents := []recentGame{{Path: "/roms/GBA/Zelda.gba", System: "Game Boy Advance", Title: "Zelda"}}
	got := librarySystemsForUI(lib, recents)
	if len(got) != 2 || got[0].Name != continueSystemName {
		t.Fatalf("rows = %+v, want Continue first", got)
	}
	if got[0].Games[0].Title != "Zelda" {
		t.Errorf("Continue game = %q, want Zelda", got[0].Games[0].Title)
	}
}
