package main

// Tests for the Games Library random picker: the two scopes, the jump it makes
// into the list, the no-repeat roll, the input routing, and the sheet's
// geometry on the real panel.

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// randomTestPanel installs the search fixture in the library and restores the
// panel and the dice afterwards.
func randomTestPanel(t *testing.T) {
	t.Helper()
	prev := gameLib
	t.Cleanup(func() {
		gameLib = prev
		resetGameRandomPicker()
		gameRandomIntn = rand.Intn
	})
	gameLib.open = true
	gameLib.lib = gameLibraryReport{Systems: gameSearchFixture()[1:], Total: 4}
	gameLib.recents = nil
	gameLib.query = ""
	gameLib.focus = 0
	gameLib.system = 0 // Game Boy Advance
	gameLib.cursor = 1 // Metroid Fusion
	gameLib.scroll = 0
	resetGameRandomPicker()
	gameRandomIntn = rand.Intn
}

func randomKey(sym sdl.Keycode) *sdl.KeyboardEvent {
	return &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}}
}

func randomButton(button uint8) *sdl.ControllerButtonEvent {
	return &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: button}
}

func TestRandomPickerScopesUseConsoleAndWholeLibrary(t *testing.T) {
	randomTestPanel(t)

	scopes := buildGameRandomScopes()
	if len(scopes) != 2 {
		t.Fatalf("scopes = %d, want this console and the whole library", len(scopes))
	}
	if scopes[0].title != "This console" || scopes[0].console != "Game Boy Advance" {
		t.Fatalf("first scope = %+v, want this console = Game Boy Advance", scopes[0])
	}
	if len(scopes[0].games) != 2 || !strings.Contains(scopes[0].detail, "Game Boy Advance") {
		t.Errorf("console scope = %+v (%d games)", scopes[0], len(scopes[0].games))
	}
	if scopes[1].title != "Whole library" || scopes[1].console != "" {
		t.Fatalf("second scope = %+v, want the whole library", scopes[1])
	}
	if len(scopes[1].games) != 4 {
		t.Errorf("whole-library games = %d, want 4", len(scopes[1].games))
	}
	if !strings.Contains(scopes[1].detail, "4 game(s)") || !strings.Contains(scopes[1].detail, "2 console(s)") {
		t.Errorf("whole-library detail = %q", scopes[1].detail)
	}
}

// The Continue row has no console of its own: "this console" means the console
// of the highlighted recent, which is where the jump will land.
func TestRandomPickerContinueUsesTheRecentConsole(t *testing.T) {
	randomTestPanel(t)
	gameLib.recents = []recentGame{{
		Path:   "/roms/snes/Alias (USA).sfc",
		System: "SNES",
		Title:  "A Link to the Past",
	}}
	gameLib.system = 0 // Continue is first once recents exist
	gameLib.cursor = 0

	scopes := buildGameRandomScopes()
	if len(scopes) == 0 || scopes[0].console != "SNES" {
		t.Fatalf("scopes = %+v, want this console = SNES", scopes)
	}
	if len(scopes[0].games) != 2 {
		t.Errorf("SNES games in scope = %d, want 2", len(scopes[0].games))
	}
}

func TestRandomPickJumpsAndRerolls(t *testing.T) {
	randomTestPanel(t)
	gameRandomIntn = func(int) int { return 0 }

	if !openGameRandomPicker() {
		t.Fatal("the picker did not open on a full library")
	}
	if len(gameRandomUI.scopes) != 2 || gameRandomUI.cursor != 0 {
		t.Fatalf("sheet state = %+v", gameRandomUI)
	}

	// Whole library, avoiding the highlighted Metroid: the first candidate is
	// Zelda in the same console.
	chooseGameRandomScope(1)
	if gameRandomUI.open {
		t.Fatal("picking left the sheet open")
	}
	if gameLib.system != 0 || gameLib.focus != 1 || gameLib.cursor != 0 {
		t.Fatalf("jump landed on system %d focus %d cursor %d", gameLib.system, gameLib.focus, gameLib.cursor)
	}
	first, ok := selectedLibraryGame()
	if !ok || first.Title != "Zelda - Minish Cap" {
		t.Fatalf("picked %+v, want Zelda", first)
	}

	// Rolling again must not pick the game already highlighted.
	if !openGameRandomPicker() {
		t.Fatal("the picker did not open for the second roll")
	}
	chooseGameRandomScope(1)
	second, _ := selectedLibraryGame()
	if second.Path == first.Path {
		t.Fatalf("the second roll repeated %q", second.Title)
	}
	if second.Title != "Metroid Fusion" {
		t.Fatalf("second roll = %q, want the next candidate", second.Title)
	}
}

func TestRandomPickFromTheCurrentConsoleStaysThere(t *testing.T) {
	randomTestPanel(t)
	gameLib.system = 1 // SNES
	gameLib.cursor = 0 // A Link to the Past, excluded from the roll
	gameRandomIntn = func(int) int { return 0 }

	if !openGameRandomPicker() {
		t.Fatal("the picker did not open")
	}
	chooseGameRandomScope(0)
	game, _ := selectedLibraryGame()
	if gameLib.system != 1 || gameLib.focus != 1 || game.Title != "Super Mario World" {
		t.Fatalf("jump = system %d focus %d game %q, want SNES/Super Mario World",
			gameLib.system, gameLib.focus, game.Title)
	}
}

// A search narrows what can be picked, or the jump would land on a row that is
// filtered out.
func TestRandomPickerFollowsTheSearch(t *testing.T) {
	randomTestPanel(t)
	setGameLibraryQuery("metroid")

	scopes := buildGameRandomScopes()
	if len(scopes) != 2 {
		t.Fatalf("scopes = %+v, want both scopes over the match", scopes)
	}
	if len(scopes[0].games) != 1 || len(scopes[1].games) != 1 {
		t.Fatalf("scope sizes = %d/%d, want 1/1", len(scopes[0].games), len(scopes[1].games))
	}
	if !strings.Contains(scopes[1].detail, "match(es)") {
		t.Errorf("whole-library detail = %q, want it to say matches", scopes[1].detail)
	}

	if !openGameRandomPicker() {
		t.Fatal("the picker did not open while searching")
	}
	chooseGameRandomScope(1)
	game, _ := selectedLibraryGame()
	if game.Title != "Metroid Fusion" {
		t.Fatalf("picked %q, want the only match", game.Title)
	}
	if gameLibraryQuery() != "metroid" {
		t.Fatalf("query = %q, want it kept", gameLibraryQuery())
	}
}

func TestRandomPickerWithoutGamesDoesNotOpen(t *testing.T) {
	randomTestPanel(t)
	gameLib.lib = gameLibraryReport{}
	gameLib.recents = nil

	if openGameRandomPicker() {
		t.Fatal("the picker opened with no games")
	}
	if gameRandomUI.open {
		t.Fatal("the sheet is open after an empty picker")
	}
}

// Continue entries can outlive the console folder they came from. The picker
// then offers only the library instead of a console scope that cannot be found.
func TestRandomPickerSkipsAConsoleThatIsGone(t *testing.T) {
	randomTestPanel(t)
	gameLib.lib = gameLibraryReport{Systems: []librarySystem{{
		Name:  "NES",
		Games: []libraryGame{{Path: "/roms/FC/Contra.nes", System: "NES", Title: "Contra"}},
	}}, Total: 1}
	gameLib.recents = []recentGame{{
		Path:   "/roms/GBA/Missing.gba",
		System: "Game Boy Advance",
		Title:  "Missing",
	}}
	gameLib.system = 0 // Continue
	gameLib.cursor = 0

	scopes := buildGameRandomScopes()
	if len(scopes) != 1 || scopes[0].title != "Whole library" {
		t.Fatalf("scopes = %+v, want only the whole library", scopes)
	}
}

func TestRandomPickerInputRouting(t *testing.T) {
	randomTestPanel(t)

	// R opens the sheet and the library stays open behind it.
	if !handleGameLibraryKey(randomKey(sdl.K_r), nil) {
		t.Fatal("R was not consumed")
	}
	if !gameRandomUI.open || !gameLib.open {
		t.Fatalf("after R: sheet %v library %v", gameRandomUI.open, gameLib.open)
	}

	// Down moves the sheet, not the game list.
	before := gameLib.cursor
	if !ctrlGameLibrary(randomButton(sdl.CONTROLLER_BUTTON_DPAD_DOWN), nil) {
		t.Fatal("Down was not consumed by the sheet")
	}
	if gameRandomUI.cursor != 1 {
		t.Fatalf("sheet cursor = %d, want 1", gameRandomUI.cursor)
	}
	if gameLib.cursor != before {
		t.Fatal("the sheet let the library move underneath it")
	}

	// B closes the sheet first.
	ctrlGameLibrary(randomButton(sdl.CONTROLLER_BUTTON_B), nil)
	if gameRandomUI.open || !gameLib.open {
		t.Fatalf("after B: sheet %v library %v", gameRandomUI.open, gameLib.open)
	}

	// R3 opens it, and A picks from the highlighted scope.
	ctrlGameLibrary(randomButton(sdl.CONTROLLER_BUTTON_RIGHTSTICK), nil)
	if !gameRandomUI.open {
		t.Fatal("R3 did not open the picker")
	}
	gameRandomIntn = func(int) int { return 0 }
	ctrlGameLibrary(randomButton(sdl.CONTROLLER_BUTTON_A), nil)
	if gameRandomUI.open || !gameLib.open {
		t.Fatalf("after A: sheet %v library %v", gameRandomUI.open, gameLib.open)
	}
	if game, ok := selectedLibraryGame(); !ok || game.Title == "" {
		t.Fatal("A on a scope did not land on a game")
	}

	// Escape closes the reopened sheet without leaving the library.
	handleGameLibraryKey(randomKey(sdl.K_r), nil)
	if !handleGameRandomKey(randomKey(sdl.K_ESCAPE)) {
		t.Fatal("Escape was not consumed by the sheet")
	}
	if gameRandomUI.open || !gameLib.open {
		t.Fatalf("after Escape: sheet %v library %v", gameRandomUI.open, gameLib.open)
	}

	// Closing the library also drops the sheet.
	openGameRandomPicker()
	closeGameLibraryPanel()
	if gameRandomUI.open {
		t.Fatal("closing the library left the random sheet open")
	}
}

func TestRandomPickerRendersInsideItsPanel(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	withScreen(t, probeW, probeH)
	ClearTextCache()
	prevAccent := accentColor
	accentColor = ColorAccent
	t.Cleanup(func() {
		closeGameLibraryArt()
		destroyTestRenderer(renderer)
		gameLib = gameLibraryPanel{}
		accentColor = prevAccent
	})

	randomTestPanel(t)
	if !openGameRandomPicker() {
		t.Fatal("the picker did not open")
	}
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	renderGameLibraryPanel(renderer, &Config{})

	if gameLib.panel.W <= 0 || gameLib.panel.H <= 0 {
		t.Fatal("the library panel was not measured under the sheet")
	}
	panel := gameRandomUI.panel
	if panel.W <= 0 || panel.H <= 0 {
		t.Fatal("the random sheet was not measured")
	}
	if panel.X < 0 || panel.Y < 0 || panel.X+panel.W > probeW || panel.Y+panel.H > probeH {
		t.Fatalf("sheet %+v is outside %dx%d", panel, probeW, probeH)
	}
	if len(gameRandomUI.rowHits) != 2 {
		t.Fatalf("scope rows = %d, want 2", len(gameRandomUI.rowHits))
	}
	for _, hit := range gameRandomUI.rowHits {
		if !pointInRect(hit.rect.X+4, hit.rect.Y+4, panel) {
			t.Errorf("scope row %+v escapes the sheet %+v", hit.rect, panel)
		}
	}

	// A click inside the card but outside a row is swallowed, not passed to the
	// library beneath; a click outside dismisses the sheet.
	if !handleGameRandomMouse(panel.X+4, panel.Y+panel.H-4) {
		t.Error("a click on the sheet was not consumed")
	}
	if !gameRandomUI.open {
		t.Fatal("clicking the sheet closed it")
	}
	handleGameRandomMouse(2, 2)
	if gameRandomUI.open {
		t.Fatal("clicking outside did not close the sheet")
	}
}

// The picker must never read the same game twice in a row when the scope has
// another candidate, whatever index the dice return.
func TestRandomLibraryGameAvoidsTheHighlightedTitle(t *testing.T) {
	games := []libraryGame{
		{Path: "/a", Title: "A"},
		{Path: "/b", Title: "B"},
		{Path: "/c", Title: "C"},
	}
	for _, roll := range []int{0, 1, 2} {
		gameRandomIntn = func(int) int { return roll }
		got, ok := randomLibraryGame(games, "/b")
		if !ok {
			t.Fatal("no game picked")
		}
		if got.Path == "/b" {
			t.Fatalf("roll %d repeated the highlighted game", roll)
		}
	}
	gameRandomIntn = rand.Intn

	// A single-game scope has to repeat: there is nothing else to offer.
	if got, ok := randomLibraryGame([]libraryGame{{Path: "/only"}}, "/only"); !ok || got.Path != "/only" {
		t.Fatalf("single game pick = %+v, %v", got, ok)
	}
	if _, ok := randomLibraryGame(nil, ""); ok {
		t.Fatal("an empty scope produced a game")
	}
}
