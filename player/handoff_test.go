package main

// handoff_test.go — the "Open in…" contract from the library side.
//
// The decision is a pure function over the catalogs, so every priority rule
// is tested without a device; the wiring tests then prove the library really
// hides, the destination panel really opens, and closing the tool puts the
// library back where the handoff left it — including an active search. Which
// tile a rebuilt catalog highlights is left to the pure test: the panel
// rescans the real system on open, so an exact index there would be a
// machine-dependent assertion.

import (
	"path/filepath"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// handoffEmuFixture is the tile catalog shape: two pinned doors first, then
// console folders with mixed install states.
func handoffEmuFixture() []emuEntry {
	return []emuEntry{
		{Name: gamesLibraryEntryName, Status: emuAvailable, Pinned: true},
		{Name: compatEntryName, Status: emuAvailable, Pinned: true},
		{Name: "GBA", Status: emuMissing, Glyph: "gamepad"},
		{Name: "PS", Status: emuAvailable, Glyph: "gamepad"},
		{Name: "PORTS", Status: emuBroken, Glyph: "gamepad"},
	}
}

func handoffGame(path, system string) libraryGame {
	return libraryGame{Path: path, System: system, Title: "Metal Gear"}
}

func TestHandoffForGamePrefersCoveringProfile(t *testing.T) {
	profiles := []gameProfileEntry{
		{Profile: GameProfile{ID: "other", Dir: "Games/Other"}, GameDir: "Games/Other"},
		{Profile: GameProfile{ID: "quake", Title: "Quake", Dir: "Games/Quake"}, GameDir: "Games/Quake"},
		// Not ready: a profile with a problem must not claim the game.
		{Profile: GameProfile{ID: "broken", Title: "Broken", Dir: "Games/Quake"}, Issue: "executable missing"},
	}
	game := handoffGame(filepath.Join("Games", "Quake", "pak0.pak"), "Ports")

	kind, idx := handoffForGame(game, profiles, nil, nil)
	if kind != handoffProfiles {
		t.Fatalf("kind = %v, want profiles", kind)
	}
	// The newest ready match wins, so the loop runs from the back.
	if idx != 1 {
		t.Fatalf("idx = %d, want the ready quake profile", idx)
	}

	// Without a covering profile the same game falls through to compat.
	kind, _ = handoffForGame(game, profiles[:1], nil, nil)
	if kind != handoffCompat {
		t.Fatalf("kind = %v, want the compat fallback", kind)
	}
}

func TestHandoffForGameMatchesExactCompatRow(t *testing.T) {
	game := handoffGame("/roms/psp/God of War.iso", "PlayStation Portable")
	key := gameKey(scannedROM{Path: game.Path, System: game.System})
	games := []gameAssessment{
		{Key: "other-game", Title: "Other"},
		{Key: key, Title: "God of War"},
	}

	kind, idx := handoffForGame(game, nil, games, handoffEmuFixture())
	if kind != handoffCompat {
		t.Fatalf("kind = %v, want compat", kind)
	}
	if idx != 1 {
		t.Fatalf("idx = %d, want the exact row", idx)
	}

	// A near-miss title must not match: the key is over the path.
	other := handoffGame("/roms/psp/God of War 2.iso", "PlayStation Portable")
	if _, idx := handoffForGame(other, nil, games, nil); idx == 1 {
		t.Fatalf("the wrong row matched: idx=%d", idx)
	}
}

func TestHandoffForGameFindsConsoleEmulatorTile(t *testing.T) {
	entries := handoffEmuFixture()

	kind, idx := handoffForGame(handoffGame("/roms/ps1/Metal Gear.iso", "PlayStation"), nil, nil, entries)
	if kind != handoffEmulators || entries[idx].Name != "PS" {
		t.Fatalf("kind=%v idx=%d, want the PS tile", kind, idx)
	}

	kind, idx = handoffForGame(handoffGame("/roms/gba/Zelda.gba", "Game Boy Advance"), nil, nil, entries)
	if kind != handoffEmulators || entries[idx].Name != "GBA" {
		t.Fatalf("kind=%v idx=%d, want the GBA tile even when it needs an install", kind, idx)
	}

	// No tile matches the console at all → the first available non-pinned
	// tile is the fallback landing place.
	kind, idx = handoffForGame(handoffGame("/roms/nds/Castlevania.nds", "Nintendo DS"), nil, nil, entries)
	if kind != handoffEmulators || entries[idx].Name != "PS" {
		t.Fatalf("kind=%v idx=%d, want the available fallback tile", kind, idx)
	}

	// Everything pinned → the compat fallback.
	pinned := []emuEntry{{Name: gamesLibraryEntryName, Status: emuAvailable, Pinned: true}}
	if kind, _ := handoffForGame(handoffGame("/roms/gba/Zelda.gba", "Game Boy Advance"), nil, nil, pinned); kind != handoffCompat {
		t.Fatalf("kind = %v, want the compat fallback", kind)
	}
}

func TestEmuEntryNameRankFoldsAndAliases(t *testing.T) {
	cases := []struct {
		name, system string
		want         int
	}{
		{"GBA", "Game Boy Advance", 1},
		{"Game Boy Advance", "Game Boy Advance", 0},
		{"game-boy-advance", "Game Boy Advance", 0},
		{"PS", "PlayStation", 1},
		{"Genesis", "Mega Drive", 1},
		{"PORTS", "PlayStation", -1},
		{"", "SNES", -1},
		{"RetroArch", "Nintendo 64", -1},
	}
	for _, tc := range cases {
		if got := emuEntryNameRank(tc.name, tc.system); got != tc.want {
			t.Errorf("rank(%q, %q) = %d, want %d", tc.name, tc.system, got, tc.want)
		}
	}
	// Game Boy must not claim the Advance.
	if emuEntryNameRank("GB", "Game Boy Advance") != -1 {
		t.Error("GB matched Game Boy Advance")
	}
	if emuEntryNameRank("GBA", "Game Boy") != -1 {
		t.Error("GBA matched plain Game Boy")
	}
}

func TestProfileCoversGameExactAndChildren(t *testing.T) {
	if !profileCoversGame("Games/Quake", "Games/Quake/pak0.pak") {
		t.Error("a subfolder of the profile dir did not match")
	}
	if !profileCoversGame("Games/Quake", "Games/Quake") {
		t.Error("the profile dir itself did not match")
	}
	// Windows separators on either side.
	if !profileCoversGame(`Games\Quake`, `Games\Quake\pak0.pak`) {
		t.Error("backslash paths did not match")
	}
	if !profileCoversGame("Games/Quake", `Games\Quake\pak0.pak`) {
		t.Error("mixed separators did not match")
	}
	if profileCoversGame("Games/Quake", "Games/QuakeRemastered/pak0.pak") {
		t.Error("a differently-named folder matched by prefix")
	}
	if profileCoversGame(" ", "Games/Quake/x") || profileCoversGame("Games/Quake", " ") {
		t.Error("a blank side matched")
	}
}

// handoffLibraryState puts the library on screen with one game and hands it
// back after the test, together with anything the handoff touched: the emu
// cache is stubbed with a known catalog, and the destination panels start
// closed so one test's panel cannot leak into the next.
func handoffLibraryState(t *testing.T) {
	t.Helper()
	prevLib := gameLib
	gameLib = gameLibraryPanel{
		open: true,
		lib: gameLibraryReport{
			Systems: []librarySystem{{
				Name:  "PlayStation",
				Games: []libraryGame{handoffGame("/roms/ps1/Metal Gear.iso", "PlayStation")},
			}},
			Total: 1,
		},
		focus:  1,
		cursor: 0,
		system: 0,
	}
	prevEmuCacheMu := &emuCache.mu
	prevEmuCacheMu.Lock()
	prevEmuEntries, prevEmuBuilt := emuCache.entries, emuCache.built
	prevEmuCacheMu.Unlock()
	emuCache.mu.Lock()
	emuCache.built = true
	emuCache.entries = handoffEmuFixture()
	emuCache.mu.Unlock()
	prevEmu, prevCp, prevGp := emuPanel, cpPanel, gpPanel
	emuPanel, cpPanel, gpPanel = emuPanelState{}, compatPanelState{}, gameProfilePanelState{}
	t.Cleanup(func() {
		gameLib = prevLib
		emuCache.mu.Lock()
		emuCache.entries, emuCache.built = prevEmuEntries, prevEmuBuilt
		emuCache.mu.Unlock()
		emuPanel, cpPanel, gpPanel = prevEmu, prevCp, prevGp
		handoffReturn.active = false
	})
}

// handoffWithTempProfile opens a real one-profile catalog in a temp dir, the
// same way the Game Profiles tests do, so the panel reads it back after its
// own cache invalidation.
func handoffWithTempProfile(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	root := t.TempDir()
	writeFakeGame(t, filepath.Join(root, "Games", "Quake"), "pak0.pak")
	writeTestFile(t, filepath.Join(dir, "profiles.json"), `{
  "schemaVersion": 1,
  "profiles": [`+nativeProfile("quake", "Quake", "Games/Quake", "pak0.pak")+`]
}`)
	setGameProfileTestDirs(t, dir, []string{root})
}

func TestOpenInHandoffOpensEmulatorsAndReturnsToTheLibrary(t *testing.T) {
	handoffLibraryState(t)

	game, ok := selectedLibraryGame()
	if !ok {
		t.Fatal("no game selected in the fixture")
	}
	openGameLibraryHandoff(game)

	if gameLib.open {
		t.Fatal("the library stayed open over the handoff")
	}
	if !handoffReturn.active {
		t.Fatal("the return trip was not armed")
	}
	if !emuPanel.open {
		t.Fatal("the Emulators page did not open")
	}
	if emuPanel.cursor < 0 {
		t.Fatalf("cursor = %d, want a tile", emuPanel.cursor)
	}

	// Closing the tool is the back again leg.
	closeEmulatorPanel()
	if emuPanel.open {
		t.Fatal("the Emulators page stayed open")
	}
	if !gameLib.open {
		t.Fatal("the library did not come back")
	}
	if handoffReturn.active {
		t.Fatal("the handoff stayed armed after returning")
	}
	if gameLib.focus != 1 || gameLib.system != 0 || gameLib.cursor != 0 || gameLib.query != "" {
		t.Fatalf("restored state = focus %d system %d cursor %d query %q",
			gameLib.focus, gameLib.system, gameLib.cursor, gameLib.query)
	}
}

func TestOpenInHandoffOpensTheProfilesPanelForAProfileGame(t *testing.T) {
	handoffLibraryState(t)
	handoffWithTempProfile(t)

	game := handoffGame(filepath.Join("Games", "Quake", "pak0.pak"), "Ports")
	openGameLibraryHandoff(game)

	if gameLib.open || !handoffReturn.active {
		t.Fatal("the library was not hidden under the handoff")
	}
	if !gpPanel.open {
		t.Fatal("the Game Profiles panel did not open")
	}
	// The panel rebuilt the catalog from the temp dir, so the cursor must sit
	// on the one covering profile.
	entries := gameProfileEntries()
	if gpPanel.cursor >= len(entries) || entries[gpPanel.cursor].Profile.ID != "quake" {
		t.Fatalf("cursor = %d over %d entries, want quake", gpPanel.cursor, len(entries))
	}

	closeGameProfilePanel()
	if gpPanel.open || !gameLib.open || handoffReturn.active {
		t.Fatal("closing the profile panel did not restore the library")
	}
}

func TestOpenInHandoffFromTheOptionsSheetTargetsTheSheetGame(t *testing.T) {
	handoffLibraryState(t)
	handoffWithTempProfile(t)

	prevSheet := gameOptsUI
	t.Cleanup(func() { gameOptsUI = prevSheet })
	gameOptsUI.open = true
	gameOptsUI.game = handoffGame(filepath.Join("Games", "Quake", "pak0.pak"), "Ports")

	// The row is driven the way the user reaches it: cursor onto "Open in…"
	// and press Return on the sheet.
	gameOptsUI.cursor = gameOptionsRowOpenIn
	press := keyPress(sdl.K_RETURN)
	handleGameOptionsKey(&press)
	if gameOptsUI.open {
		t.Fatal("the sheet stayed open under the handoff")
	}
	if !gpPanel.open {
		t.Fatal("the sheet handoff did not open the profile panel")
	}
	closeGameProfilePanel()
	if !gameLib.open {
		t.Fatal("the library did not come back")
	}
}

// keyPress builds a KEYDOWN event for a key.
func keyPress(sym sdl.Keycode) sdl.KeyboardEvent {
	return sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}}
}

func TestOpenInHandoffOpensTheCompatRowForAReportedGame(t *testing.T) {
	handoffLibraryState(t)
	game := handoffGame("/roms/psp/God of War.iso", "PlayStation Portable")

	// Seed the scan state with the exact row (the pure test covers matching);
	// the scan otherwise runs in the background, so the cursor would be
	// whatever the live list allows — but the tab must always start on the
	// games view.
	key := gameKey(scannedROM{Path: game.Path, System: game.System})
	compatScan.mu.Lock()
	prevGames := compatScan.games
	compatScan.games = []gameAssessment{{Key: key, Title: "God of War"}}
	compatScan.mu.Unlock()
	t.Cleanup(func() {
		compatScan.mu.Lock()
		compatScan.games = prevGames
		compatScan.mu.Unlock()
	})

	openGameLibraryHandoff(game)
	if !cpPanel.open || cpPanel.tab != compatTabGames {
		t.Fatalf("compat panel open=%v tab=%d, want open games view", cpPanel.open, cpPanel.tab)
	}
	closeCompatPanel()
	if cpPanel.open || !gameLib.open || handoffReturn.active {
		t.Fatal("closing the compat panel did not restore the library")
	}
}

func TestRestoreLibraryFromHandoffIsNoopWithoutOne(t *testing.T) {
	handoffLibraryState(t)
	// Something else closed the library and nothing armed a handoff.
	gameLib.open = false
	if restoreLibraryFromHandoff() {
		t.Fatal("a restore ran with no handoff armed")
	}
	if gameLib.open {
		t.Fatal("the library was reopened out of nowhere")
	}
}

func TestOpenInHandoffIgnoredWhenLibraryClosed(t *testing.T) {
	handoffLibraryState(t)
	gameLib.open = false

	openGameLibraryHandoff(handoffGame("/roms/ps1/Metal Gear.iso", "PlayStation"))
	if emuPanel.open || handoffReturn.active {
		t.Fatal("a handoff ran while the library was closed")
	}
	if gameLib.open {
		t.Fatal("the library was reopened by the ignored handoff")
	}
}
