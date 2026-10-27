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
	"unsafe"

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

// TestGameLibraryCountsCoverCompleteness pins the figures behind the ring and
// the console meters: they describe covers that exist on disk right now.
func TestGameLibraryCountsCoverCompleteness(t *testing.T) {
	root := t.TempDir()
	art := filepath.Join(root, "art")
	gbaArt := filepath.Join(art, "Game_Boy_Advance")
	if err := os.MkdirAll(gbaArt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gbaArt, "Zelda.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := romScanReport{Games: []scannedROM{
		{Path: "/roms/GBA/Zelda.gba", System: "Game Boy Advance"},
		{Path: "/roms/GBA/Aladdin.gba", System: "Game Boy Advance"},
		{Path: "/roms/FC/Contra.nes", System: "NES"},
	}}
	lib := buildGameLibrary(report, art)
	if lib.Total != 3 || lib.WithArt != 1 {
		t.Fatalf("library = %d games, %d covers; want 3 and 1", lib.Total, lib.WithArt)
	}
	if got := lib.Systems[1].WithArt; got != 1 {
		t.Errorf("GBA covers = %d, want 1", got)
	}
	if got := lib.Systems[0].WithArt; got != 0 {
		t.Errorf("NES covers = %d, want 0", got)
	}
	for _, game := range lib.Systems[1].Games {
		want := game.Title == "Zelda"
		if game.HasArt != want {
			t.Errorf("%s HasArt = %v, want %v", game.Title, game.HasArt, want)
		}
	}

	// A directory at the cover path is not a cover.
	if err := os.MkdirAll(filepath.Join(art, "NES", "Contra.png"), 0o755); err != nil {
		t.Fatal(err)
	}
	if lib := buildGameLibrary(report, art); lib.WithArt != 1 {
		t.Errorf("a directory counted as a cover: WithArt = %d, want 1", lib.WithArt)
	}

	cases := []struct{ withArt, total, want int }{
		{0, 0, 0}, {0, 5, 0}, {1, 3, 33}, {2, 3, 67}, {5, 5, 100}, {7, 5, 100},
	}
	for _, c := range cases {
		if got := coverPercent(c.withArt, c.total); got != c.want {
			t.Errorf("coverPercent(%d, %d) = %d, want %d", c.withArt, c.total, got, c.want)
		}
	}
}

// TestRecountLibraryArtworkPicksUpNewCovers checks the refresh that runs after
// a fetch, without rescanning the card.
func TestRecountLibraryArtworkPicksUpNewCovers(t *testing.T) {
	root := t.TempDir()
	art := filepath.Join(root, "art")
	lib := buildGameLibrary(romScanReport{Games: []scannedROM{
		{Path: "/roms/GBA/Zelda.gba", System: "Game Boy Advance"},
		{Path: "/roms/GBA/Aladdin.gba", System: "Game Boy Advance"},
	}}, art)
	if lib.WithArt != 0 {
		t.Fatalf("fresh library has %d covers, want 0", lib.WithArt)
	}

	prevLib, prevSeen := gameLib.lib, gameLib.artSeen
	t.Cleanup(func() { gameLib.lib, gameLib.artSeen = prevLib, prevSeen })
	gameLib.lib = lib

	if err := os.MkdirAll(filepath.Join(art, "Game_Boy_Advance"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(art, "Game_Boy_Advance", "Zelda.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	recountLibraryArtwork()
	if gameLib.lib.WithArt != 1 || gameLib.lib.Total != 2 {
		t.Fatalf("after recount = %d/%d covers, want 1/2", gameLib.lib.WithArt, gameLib.lib.Total)
	}
	if gameLib.lib.Systems[0].WithArt != 1 {
		t.Errorf("console count did not move: %d", gameLib.lib.Systems[0].WithArt)
	}
	if coverPercent(gameLib.lib.WithArt, gameLib.lib.Total) != 50 {
		t.Errorf("status would report the wrong percentage")
	}
}

// TestCoverCompletenessRingDrawsItsArc pins that the ring is actually painted,
// that 100% closes the circle, and that the percentage sits inside it. A
// segment-based ring that silently drew nothing would still pass every
// non-pixel test.
func TestCoverCompletenessRingDrawsItsArc(t *testing.T) {
	renderer := probeRenderer(t)
	cx, cy, radius := probeW/2, probeH/2, int32(60)
	draw := func(percent int) []byte {
		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.Clear()
		drawCoverCompletenessRing(renderer, &Config{}, cx, cy, radius, percent)
		buf := make([]byte, probeW*probeH*4)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
			t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
		}
		return buf
	}

	full := draw(100)
	if litPixels(full, sdl.Rect{X: cx - radius, Y: cy - radius, W: 2 * radius, H: 2 * radius}, probeW) == 0 {
		t.Fatal("the completeness ring drew nothing")
	}
	bottom := sdl.Rect{X: cx - 6, Y: cy + radius - 6, W: 12, H: 12}
	fullBottom := litPixels(full, bottom, probeW)
	if fullBottom == 0 {
		t.Error("a full ring has a gap at the bottom")
	}
	// The arc stops at a quarter, so the bottom is only the faint track.
	if quarterBottom := litPixels(draw(25), bottom, probeW); quarterBottom >= fullBottom/2 {
		t.Errorf("a quarter ring lit %d pixels at the bottom versus %d at full; the arc did not stop", quarterBottom, fullBottom)
	}
	if litPixels(full, sdl.Rect{X: cx - 26, Y: cy - 12, W: 52, H: 24}, probeW) == 0 {
		t.Error("the percentage label was not drawn inside the ring")
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
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
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

// TestLibraryArtBudgetBranches pins the two ceilings and the seam the memory
// harness uses to exercise the handheld one on any host. The handheld budget is
// what ships on the card, so it must stay the smaller of the two and the harness
// must be able to select it regardless of the machine running the test.
func TestLibraryArtBudgetBranches(t *testing.T) {
	prev := libraryArtBudgetForce.Swap(0)
	t.Cleanup(func() { libraryArtBudgetForce.Store(prev) })

	if libraryArtHandheldBudget != 32<<20 {
		t.Fatalf("handheld ceiling = %d, want the documented 32 MB", libraryArtHandheldBudget)
	}
	if libraryArtDesktopBudget != 96<<20 {
		t.Fatalf("desktop ceiling = %d, want the documented 96 MB", libraryArtDesktopBudget)
	}
	if libraryArtHandheldBudget >= libraryArtDesktopBudget {
		t.Fatalf("handheld ceiling %d is not below the desktop %d", libraryArtHandheldBudget, libraryArtDesktopBudget)
	}

	// With no force the branch has to follow the device, not a stale value.
	want := libraryArtDesktopBudget
	if IsTrimuiDevice() || P().IsHandheld() {
		want = libraryArtHandheldBudget
	}
	if got := libraryArtBudgetBytes(); got != want {
		t.Fatalf("derived budget = %d, want %d for this device", got, want)
	}

	libraryArtBudgetForce.Store(1)
	if got := libraryArtBudgetBytes(); got != libraryArtHandheldBudget {
		t.Fatalf("forced handheld budget = %d, want %d", got, libraryArtHandheldBudget)
	}
	libraryArtBudgetForce.Store(-1)
	if got := libraryArtBudgetBytes(); got != libraryArtDesktopBudget {
		t.Fatalf("forced desktop budget = %d, want %d", got, libraryArtDesktopBudget)
	}
}

// The preview loads one cover per focused row. The cache used to keep every
// one of them for the life of the process: browsing a few hundred games pinned
// tens of megabytes of textures on a 1 GB handheld. Walking a library far
// larger than the budget must not push it past the cap.
func TestLibraryArtCacheStaysWithinItsBudget(t *testing.T) {
	prev := gameLib
	t.Cleanup(func() { gameLib = prev })
	gameLib.resetArtCache()

	// The size the preview draws a cover at, so the numbers are realistic.
	const w, h = int32(200), int32(270)
	budget := libraryArtBudgetBytes()
	covers := int(budget/(int64(w)*int64(h)*4)) + 40

	for i := 0; i < covers; i++ {
		// nil texture: this checks the accounting and eviction, not SDL.
		gameLib.rememberArt(fmt.Sprintf("cover-%d.png", i), nil, w, h)
		if gameLib.artBytes > budget {
			t.Fatalf("after %d covers the cache holds %d bytes, budget is %d",
				i+1, gameLib.artBytes, budget)
		}
		if len(gameLib.artCache) > libraryArtMaxTextures {
			t.Fatalf("after %d covers the cache holds %d entries, cap is %d",
				i+1, len(gameLib.artCache), libraryArtMaxTextures)
		}
	}
	// The newest cover is what the panel is drawing; it must survive.
	newest := fmt.Sprintf("cover-%d.png", covers-1)
	if _, ok := gameLib.artCache[newest]; !ok {
		t.Errorf("the newest cover %q was evicted", newest)
	}
	if _, ok := gameLib.artCache["cover-0.png"]; ok {
		t.Error("the first cover should have aged out of the cache")
	}
}

// A library whose artwork is missing caches a nil per game instead; the count
// cap has to hold for those too, or the map grows with the collection.
func TestLibraryArtCacheBoundsMisses(t *testing.T) {
	prev := gameLib
	t.Cleanup(func() { gameLib = prev })
	gameLib.resetArtCache()

	for i := 0; i < libraryArtMaxTextures*2; i++ {
		gameLib.rememberArt(fmt.Sprintf("missing-%d.png", i), nil, 0, 0)
	}
	if len(gameLib.artCache) > libraryArtMaxTextures {
		t.Fatalf("cache holds %d misses, cap is %d", len(gameLib.artCache), libraryArtMaxTextures)
	}
}
