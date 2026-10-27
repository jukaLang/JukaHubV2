package main

// Tests for searching the game library. The filter is a pure function over the
// scanned library, so most of this checks the match rules; the rest proves the
// search is actually reachable (key, trigger) and that typing narrows the list
// while the on-screen keyboard is open.

import (
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// gameSearchFixture is a small library with two consoles and a recents row.
func gameSearchFixture() []librarySystem {
	return []librarySystem{
		{
			Name: continueSystemName,
			Games: []libraryGame{
				{Path: "/roms/gba/Zelda - Minish Cap.gba", System: "Game Boy Advance", Title: "Zelda - Minish Cap"},
			},
		},
		{
			Name: "Game Boy Advance",
			Games: []libraryGame{
				{Path: "/roms/gba/Zelda - Minish Cap.gba", System: "Game Boy Advance", Title: "Zelda - Minish Cap", HasArt: true},
				{Path: "/roms/gba/Metroid Fusion.gba", System: "Game Boy Advance", Title: "Metroid Fusion"},
			},
		},
		{
			Name: "SNES",
			Games: []libraryGame{
				{Path: "/roms/snes/Alias (USA).sfc", System: "SNES", Title: "A Link to the Past"},
				{Path: "/roms/snes/Super Mario World.sfc", System: "SNES", Title: "Super Mario World", HasArt: true},
			},
		},
	}
}

func TestFilterLibrarySystemsMatchesTitleConsoleAndFile(t *testing.T) {
	systems := gameSearchFixture()

	// A title match keeps only that game, in its own console.
	got := filterLibrarySystems(systems, "zelda")
	if len(got) != 1 || got[0].Name != "Game Boy Advance" {
		t.Fatalf("zelda matched %+v", got)
	}
	if len(got[0].Games) != 1 || !strings.HasPrefix(got[0].Games[0].Title, "Zelda") {
		t.Fatalf("zelda games = %+v", got[0].Games)
	}
	if got[0].WithArt != 1 {
		t.Errorf("filtered console art count = %d, want the one covered game", got[0].WithArt)
	}

	// A console match keeps the console's whole list.
	got = filterLibrarySystems(systems, "snes")
	if len(got) != 1 || len(got[0].Games) != 2 {
		t.Fatalf("snes matched %+v", got)
	}

	// A file name matches even when the title differs from it.
	got = filterLibrarySystems(systems, "alias")
	if len(got) != 1 || len(got[0].Games) != 1 || got[0].Games[0].Title != "A Link to the Past" {
		t.Fatalf("alias matched %+v", got)
	}

	// Case and spacing do not matter.
	if got := filterLibrarySystems(systems, "  SUPER   MARIO  "); len(got) != 1 || len(got[0].Games) != 1 {
		t.Fatalf("normalised query matched %+v", got)
	}

	// The recents row is a view over the same games and must not duplicate hits.
	for _, system := range filterLibrarySystems(systems, "zelda") {
		if system.Name == continueSystemName {
			t.Fatal("the Continue row was listed in search results")
		}
	}

	// No match is empty, and an empty query returns the library untouched.
	if got := filterLibrarySystems(systems, "golden axe"); len(got) != 0 {
		t.Errorf("missing title matched %+v", got)
	}
	if got := filterLibrarySystems(systems, "   "); len(got) != len(systems) {
		t.Errorf("blank query returned %d systems, want all %d", len(got), len(systems))
	}
}

// searchTestPanel installs the fixture in the panel and restores it afterwards.
func searchTestPanel(t *testing.T) {
	t.Helper()
	prev := gameLib
	t.Cleanup(func() { gameLib = prev })
	gameLib.open = true
	gameLib.lib = gameLibraryReport{Systems: gameSearchFixture()[1:], Total: 4}
	gameLib.recents = nil
	gameLib.query = ""
	gameLib.system, gameLib.cursor, gameLib.scroll, gameLib.focus = 0, 1, 1, 0
}

func TestSetGameLibraryQueryKeepsTheHighlightOnAHit(t *testing.T) {
	searchTestPanel(t)

	setGameLibraryQuery("metroid")
	if gameLibraryQuery() != "metroid" {
		t.Fatalf("query = %q", gameLibraryQuery())
	}
	if gameLib.focus != 1 {
		t.Errorf("focus = %d, want the games column after a search", gameLib.focus)
	}
	game, ok := selectedLibraryGame()
	if !ok || game.Title != "Metroid Fusion" {
		t.Fatalf("selected game = %+v, %v; want the only hit", game, ok)
	}

	// Clearing restores the console browser and the console column.
	setGameLibraryQuery("")
	if gameLib.focus != 0 || gameLib.cursor != 0 || gameLib.system != 0 {
		t.Fatalf("cleared state = focus %d cursor %d system %d", gameLib.focus, gameLib.cursor, gameLib.system)
	}
	if n := len(currentGameSystems()); n != 2 {
		t.Fatalf("clearing the query left %d consoles, want 2", n)
	}
}

func TestGameLibrarySearchKeyOpensTheKeyboard(t *testing.T) {
	searchTestPanel(t)
	resetKeyboardState(t)
	withScreen(t, probeW, probeH)

	if !handleGameLibraryKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_s}}, nil) {
		t.Fatal("the library swallowed S without opening a search")
	}
	if !virtualKeyboardActive {
		t.Fatal("S did not open the on-screen keyboard")
	}
	if vkTarget != vkTargetGameSearch {
		t.Fatalf("keyboard target = %v, want the game search", vkTarget)
	}

	// The controller trigger opens the same field.
	vkCloseKeyboard()
	ctrlGameLibrary(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_RIGHTSHOULDER}, nil)
	if !virtualKeyboardActive || vkTarget != vkTargetGameSearch {
		t.Fatal("R1 did not open the game search")
	}
}

func TestGameSearchKeyboardFiltersWhileTyping(t *testing.T) {
	searchTestPanel(t)
	resetKeyboardState(t)
	withScreen(t, probeW, probeH)
	vkTarget = vkTargetGameSearch
	virtualKeyboardActive = true
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}

	vkInsertText("mario", config)
	if gameLibraryQuery() != "mario" {
		t.Fatalf("query after typing = %q", gameLibraryQuery())
	}
	systems := currentGameSystems()
	if len(systems) != 1 || len(systems[0].Games) != 1 || systems[0].Games[0].Title != "Super Mario World" {
		t.Fatalf("live filter = %+v", systems)
	}
	if vkCurrentText() != "mario" {
		t.Errorf("keyboard buffer = %q, want the query", vkCurrentText())
	}

	vkBackspace(config)
	if gameLibraryQuery() != "mari" {
		t.Fatalf("query after backspace = %q", gameLibraryQuery())
	}

	// Submitting keeps the filter and only dismisses the keyboard.
	vkSubmit(config, false)
	if virtualKeyboardActive {
		t.Error("submitting left the keyboard open")
	}
	if gameLibraryQuery() != "mari" {
		t.Errorf("submitting cleared the query: %q", gameLibraryQuery())
	}
}

// Back must clear the search before it leaves the library: a filtered list that
// looks like a broken library is worse than one extra press.
func TestGameLibraryBackClearsTheSearchFirst(t *testing.T) {
	searchTestPanel(t)
	setGameLibraryQuery("zelda")

	if !handleGameLibraryKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_ESCAPE}}, nil) {
		t.Fatal("Escape was not consumed")
	}
	if !gameLib.open {
		t.Fatal("Escape closed the library instead of clearing the search")
	}
	if gameLibraryQuery() != "" {
		t.Fatalf("query = %q, want it cleared", gameLibraryQuery())
	}

	// A second Back leaves the library.
	handleGameLibraryKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_ESCAPE}}, nil)
	if gameLib.open {
		t.Fatal("a second Escape did not close the library")
	}
}

// The real panel is drawn offscreen with a query active: the rows the pointer
// and the pad walk must be the filtered ones, and the search header must not
// push them out of the panel.
func TestGameLibrarySearchRendersOnlyHits(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
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

	searchTestPanel(t)
	setGameLibraryQuery("zelda")

	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	renderGameLibraryPanel(renderer, &Config{})

	if gameLib.panel.W <= 0 || gameLib.panel.H <= 0 {
		t.Fatal("panel rect was not measured")
	}
	if gameLib.panel.X < 0 || gameLib.panel.Y < 0 ||
		gameLib.panel.X+gameLib.panel.W > probeW || gameLib.panel.Y+gameLib.panel.H > probeH {
		t.Fatalf("panel %+v is outside %dx%d", gameLib.panel, probeW, probeH)
	}
	// One console and one game match, so exactly one row of each is drawn.
	if len(gameLib.systemHits) != 1 {
		t.Fatalf("console rows = %d, want only the console with a hit", len(gameLib.systemHits))
	}
	if len(gameLib.gameHits) != 1 {
		t.Fatalf("game rows = %d, want the single Zelda hit", len(gameLib.gameHits))
	}
	for _, hit := range gameLib.gameHits {
		if !pointInRect(hit.rect.X+4, hit.rect.Y+4, gameLib.panel) {
			t.Errorf("game row %+v escapes the panel %+v", hit.rect, gameLib.panel)
		}
	}

	// A query with no hits still draws the panel (the empty state), and turns
	// the search off again cleanly.
	setGameLibraryQuery("golden axe")
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	renderGameLibraryPanel(renderer, &Config{})
	if len(gameLib.gameHits) != 0 {
		t.Fatalf("a missing title produced %d rows", len(gameLib.gameHits))
	}
}

func TestGameLibraryStatusReportsMatches(t *testing.T) {
	searchTestPanel(t)
	setGameLibraryQuery("zelda")
	status := gameLibraryStatus()
	if !strings.Contains(status, "1 match") || !strings.Contains(status, "zelda") {
		t.Fatalf("status = %q, want the match count and the query", status)
	}

	setGameLibraryQuery("golden axe")
	if status := gameLibraryStatus(); !strings.Contains(status, "No match") {
		t.Fatalf("empty status = %q, want a no-match line", status)
	}
}
