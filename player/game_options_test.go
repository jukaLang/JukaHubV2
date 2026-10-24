package main

// Tests for the per-game launch options: the file format, the argument parser,
// the clock ladder, the launch plumbing and the sheet itself. The launch path is
// exercised through beginGamePowerProfileCap against a fake sysfs tree, so no
// emulator is ever started.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// resetGameOptionsStore points the store at a temporary file and forgets the
// cache, restoring both afterwards.
func resetGameOptionsStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "game-options.json")
	prevPath := gameOptionsPath
	gameOptionsPath = func() string { return path }
	gameOptionsMu.Lock()
	prevMap, prevLoaded := gameOptionsStore, gameOptionsLoaded
	gameOptionsStore, gameOptionsLoaded = nil, false
	gameOptionsMu.Unlock()
	t.Cleanup(func() {
		gameOptionsPath = prevPath
		gameOptionsMu.Lock()
		gameOptionsStore, gameOptionsLoaded = prevMap, prevLoaded
		gameOptionsMu.Unlock()
	})
	return path
}

func reloadGameOptionsFromDisk() {
	gameOptionsMu.Lock()
	gameOptionsLoaded = false
	gameOptionsStore = nil
	gameOptionsMu.Unlock()
}

func TestGameOptionsPersistAndClear(t *testing.T) {
	path := resetGameOptionsStore(t)
	const rom = "/roms/GBA/Zelda (USA).gba"

	if err := setGameOptions(rom, gameOptions{MaxFreqKHz: 1_800_000, Args: `--fast "My Game"`}); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), `"schemaVersion": 1`) {
		t.Errorf("file has no schema version:\n%s", raw)
	}

	// A fresh process must see the same overrides.
	reloadGameOptionsFromDisk()
	got := gameOptionsFor(rom)
	if got.MaxFreqKHz != 1_800_000 || got.Args != `--fast "My Game"` {
		t.Fatalf("reloaded = %+v", got)
	}

	// The path is normalised, so the same file reached another way shares them.
	if alias := gameOptionsFor("/roms/GBA/./Zelda (USA).gba"); alias.MaxFreqKHz != 1_800_000 {
		t.Errorf("normalised lookup = %+v", alias)
	}
	if other := gameOptionsFor("/roms/GBA/Metroid.gba"); !other.empty() {
		t.Errorf("unrelated ROM picked up %+v", other)
	}

	// Setting a game back to the defaults removes it, not writes a zero.
	if err := setGameOptions(rom, gameOptions{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	reloadGameOptionsFromDisk()
	if got := gameOptionsFor(rom); !got.empty() {
		t.Errorf("cleared options = %+v", got)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "Zelda") {
		t.Errorf("the cleared game is still in the file:\n%s", raw)
	}
}

func TestParseGameArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"--fast", []string{"--fast"}},
		{"--profile \"My Game\" --fast", []string{"--profile", "My Game", "--fast"}},
		{"--profile 'My Game'", []string{"--profile", "My Game"}},
		{"a\\ b", []string{"a b"}},
		{"--x \"y z", []string{"--x", "y z"}},
	}
	for _, tc := range cases {
		got := parseGameArgs(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("parse(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parse(%q) = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}

	// A field of 40 words cannot turn into 40 arguments.
	many := strings.Repeat("a ", gameOptionsMaxArgs+5)
	if got := parseGameArgs(many); len(got) != gameOptionsMaxArgs {
		t.Errorf("parsed %d arguments, want the cap of %d", len(got), gameOptionsMaxArgs)
	}
}

func TestGameCeilingLadder(t *testing.T) {
	if got := cycleGameCeiling(0, 1); got != 2_000_000 {
		t.Errorf("Auto + Right = %d, want 2 GHz", got)
	}
	if got := cycleGameCeiling(0, -1); got != 0 {
		t.Errorf("Auto + Left = %d, want Auto (no wrap to the slowest clock)", got)
	}
	if got := cycleGameCeiling(2_000_000, 1); got != 1_800_000 {
		t.Errorf("2 GHz + Right = %d, want one step cooler", got)
	}
	if got := cycleGameCeiling(2_000_000, -1); got != 0 {
		t.Errorf("2 GHz + Left = %d, want Auto at the top of the ladder", got)
	}
	if got := cycleGameCeiling(1_200_000, 1); got != 1_200_000 {
		t.Errorf("coolest + Right = %d, want the bottom clamped", got)
	}
	if got := cycleGameCeiling(1_800_000, 1); got != 1_600_000 {
		t.Errorf("1.8 GHz + Right = %d, want 1.6 GHz", got)
	}
	if got := cycleGameCeiling(1_800_000, -1); got != 2_000_000 {
		t.Errorf("1.8 GHz + Left = %d, want 2 GHz", got)
	}
	// A hand-edited clock between rungs lands on the nearest one.
	if got := gameOptionCeilingIndex(1_700_000); got != 2 {
		t.Errorf("index of 1.7 GHz = %d, want the 1.8 GHz rung", got)
	}
	if formatGameCeiling(0) != "Auto" || formatGameCeiling(1_800_000) != "1.8 GHz" {
		t.Errorf("labels = %q, %q", formatGameCeiling(0), formatGameCeiling(1_800_000))
	}
}

func TestGameLaunchCommandAppendsGameArgs(t *testing.T) {
	name, args := gameLaunchCommandArgs("/emus/GBA/launch.sh", "/roms/GBA/a.gba", []string{"--fast", "b c"})
	if name != "sh" {
		t.Errorf("command = %q, want sh", name)
	}
	want := []string{"/emus/GBA/launch.sh", "/roms/GBA/a.gba", "--fast", "b c"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %v, want %v", args, want)
		}
	}

	// No extra arguments keeps the shape the launcher tests pin.
	if _, base := gameLaunchCommand("/emus/GBA/launch.sh", "/roms/GBA/a.gba"); len(base) != 2 {
		t.Errorf("base args = %v, want the launcher and the ROM", base)
	}

	name, args = gameLaunchCommandArgs(`C:\Emus\PSP\PPSSPP.exe`, `C:\roms\a.iso`, []string{"--fullscreen"})
	if name != `C:\Emus\PSP\PPSSPP.exe` || len(args) != 2 || args[1] != "--fullscreen" {
		t.Errorf("exe launch = %q %v", name, args)
	}
}

// A per-game clock wins over the global cap for that launch only; the firmware
// ceiling comes back with the battery profile.
func TestPerGameCeilingAppliesAndRestores(t *testing.T) {
	root := t.TempDir()
	gov := fakeCPUWithFreq(t, root, 0, "1608000", "2000000", true)
	prevRoot := cpufreqRoot
	cpufreqRoot = root
	resetLaunchPowerProfile()
	t.Cleanup(func() {
		cpufreqRoot = prevRoot
		resetLaunchPowerProfile()
	})
	maxPath := filepath.Join(filepath.Dir(gov), "scaling_max_freq")

	release := beginGamePowerProfileCap(nil, 1_200_000)
	if got := readKHzFile(maxPath); got != 1_200_000 {
		t.Fatalf("ceiling = %d, want the game's 1.2 GHz", got)
	}
	if raw, _ := os.ReadFile(gov); strings.TrimSpace(string(raw)) != "performance" {
		t.Fatalf("governor = %q, want performance", raw)
	}
	release()
	if got := readKHzFile(maxPath); got != 1_608_000 {
		t.Errorf("restored ceiling = %d, want the firmware's 1.608 GHz", got)
	}

	// A game without its own clock still follows the global setting.
	global := &Config{Variables: Variables{Custom: map[string]interface{}{
		"powerProfileMaxFreq": "1800",
	}}}
	release = beginGamePowerProfileCap(global, 0)
	if got := readKHzFile(maxPath); got != 1_800_000 {
		t.Errorf("global cap = %d, want 1.8 GHz", got)
	}
	release()
	if got := readKHzFile(maxPath); got != 1_608_000 {
		t.Errorf("ceiling after the global launch = %d, want the firmware value", got)
	}
}

// The sheet is reachable from the library, edits the highlighted game and hands
// its arguments to the keyboard.
func TestGameOptionsSheetEditsSelectedGame(t *testing.T) {
	resetGameOptionsStore(t)
	searchTestPanel(t)
	resetKeyboardState(t)
	gameLib.focus = 1
	gameLib.system = 0
	gameLib.cursor = 1 // Metroid Fusion
	t.Cleanup(func() {
		gameOptsUI.open = false
		gameOptsUI.rowHits = nil
		gameArgsBuffer = ""
	})

	want, ok := selectedLibraryGame()
	if !ok || want.Title != "Metroid Fusion" {
		t.Fatalf("fixture selected %+v, %v", want, ok)
	}

	if !handleGameLibraryKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_o}}, nil) {
		t.Fatal("O was not consumed by the library")
	}
	if !gameOptsUI.open || gameOptsUI.game.Path != want.Path {
		t.Fatalf("sheet = %+v for %q, want it open on Metroid", gameOptsUI.open, gameOptsUI.game.Path)
	}

	// Right on the ceiling row turns Auto into an explicit 2 GHz and stores it.
	handleGameOptionsKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_RIGHT}})
	if got := gameOptionsFor(want.Path).MaxFreqKHz; got != 2_000_000 {
		t.Fatalf("ceiling after Right = %d, want 2 GHz", got)
	}
	// One more press cools the game to 1.8 GHz.
	handleGameOptionsKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_RIGHT}})
	if got := gameOptionsFor(want.Path).MaxFreqKHz; got != 1_800_000 {
		t.Fatalf("ceiling after two Right presses = %d, want 1.8 GHz", got)
	}
	// Left walks back up, and stepping past Auto removes the record entirely.
	handleGameOptionsKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_LEFT}})
	if got := gameOptionsFor(want.Path).MaxFreqKHz; got != 2_000_000 {
		t.Fatalf("ceiling after one Left = %d, want 2 GHz", got)
	}
	handleGameOptionsKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_LEFT}})
	if got := gameOptionsFor(want.Path); !got.empty() {
		t.Fatalf("ceiling after Left from 2 GHz = %+v, want the defaults", got)
	}

	// Down to the arguments row opens the keyboard, seeded with what is saved.
	handleGameOptionsKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_DOWN}})
	handleGameOptionsKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_RETURN}})
	if !virtualKeyboardActive || vkTarget != vkTargetGameArgs {
		t.Fatalf("arguments row opened target %v (active %v)", vkTarget, virtualKeyboardActive)
	}
	vkSetText(`--fast "My Game"`, nil)
	vkSubmit(nil, false)
	if virtualKeyboardActive {
		t.Error("submitting left the keyboard open")
	}
	stored := gameOptionsFor(want.Path)
	if stored.Args != `--fast "My Game"` {
		t.Fatalf("stored arguments = %q", stored.Args)
	}
	if got := parseGameArgs(stored.Args); len(got) != 2 || got[1] != "My Game" {
		t.Errorf("launch arguments = %v, want two arguments", got)
	}
	if !gameOptsUI.open {
		t.Error("saving the arguments closed the sheet")
	}

	// The reset row clears both fields.
	handleGameOptionsKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_DOWN}})
	handleGameOptionsKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_RETURN}})
	if got := gameOptionsFor(want.Path); !got.empty() {
		t.Fatalf("reset left %+v", got)
	}

	// B closes the sheet and keeps the library; the library's own Back then
	// leaves the library.
	handleGameOptionsKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_ESCAPE}})
	if gameOptsUI.open || !gameLib.open {
		t.Fatalf("after B: sheet %v, library %v", gameOptsUI.open, gameLib.open)
	}
	handleGameLibraryKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_ESCAPE}}, nil)
	if gameLib.open {
		t.Error("the library ignored the second Back")
	}
}

// The controller's Start opens the sheet and its D-pad drives it.
func TestGameOptionsSheetController(t *testing.T) {
	resetGameOptionsStore(t)
	searchTestPanel(t)
	gameLib.focus = 1
	gameLib.cursor = 0
	t.Cleanup(func() { gameOptsUI.open = false })

	press := func(button uint8) {
		t.Helper()
		ctrlGameLibrary(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: button}, nil)
	}
	press(sdl.CONTROLLER_BUTTON_START)
	if !gameOptsUI.open {
		t.Fatal("Start did not open the options sheet")
	}
	game, _ := selectedLibraryGame()
	press(sdl.CONTROLLER_BUTTON_DPAD_RIGHT)
	if got := gameOptionsFor(game.Path).MaxFreqKHz; got != 2_000_000 {
		t.Errorf("D-pad Right set %d, want 2 GHz", got)
	}
	press(sdl.CONTROLLER_BUTTON_DPAD_DOWN)
	if gameOptsUI.cursor != gameOptionsRowArgs {
		t.Errorf("cursor = %d, want the arguments row", gameOptsUI.cursor)
	}
	press(sdl.CONTROLLER_BUTTON_B)
	if gameOptsUI.open || !gameLib.open {
		t.Errorf("B left sheet %v, library %v", gameOptsUI.open, gameLib.open)
	}
}

// The sheet draws inside the screen and registers one hit per row, so touch and
// the pad address the same geometry.
func TestGameOptionsSheetRenders(t *testing.T) {
	resetGameOptionsStore(t)
	searchTestPanel(t)
	gameLib.focus = 1
	gameLib.cursor = 0

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	newProbeFrame(t)
	withScreen(t, probeW, probeH)
	ClearTextCache()
	prevAccent := accentColor
	accentColor = ColorAccent
	t.Cleanup(func() {
		closeGameLibraryArt()
		destroyTestRenderer(renderer)
		gameOptsUI.open = false
		gameOptsUI.rowHits = nil
		gameLib = gameLibraryPanel{}
		accentColor = prevAccent
	})

	game, _ := selectedLibraryGame()
	if err := setGameOptions(game.Path, gameOptions{MaxFreqKHz: 1_800_000, Args: "--fast"}); err != nil {
		t.Fatalf("seed options: %v", err)
	}
	if !openGameOptions() {
		t.Fatal("the sheet did not open")
	}
	renderGameLibraryPanel(renderer, &Config{})

	panel := gameOptsUI.panel
	if panel.W <= 0 || panel.H <= 0 {
		t.Fatalf("sheet rect = %+v", panel)
	}
	if panel.X < 0 || panel.Y < 0 || panel.X+panel.W > probeW || panel.Y+panel.H > probeH {
		t.Fatalf("sheet %+v is outside %dx%d", panel, probeW, probeH)
	}
	if len(gameOptsUI.rowHits) != gameOptionsRowCount {
		t.Fatalf("row hits = %d, want %d", len(gameOptsUI.rowHits), gameOptionsRowCount)
	}
	for _, hit := range gameOptsUI.rowHits {
		if !pointInRect(hit.rect.X+4, hit.rect.Y+4, panel) {
			t.Errorf("row %d %+v escapes the sheet %+v", hit.index, hit.rect, panel)
		}
	}
	if got := gameOptionsRowValue(gameOptionsRowCeiling, gameOptionsFor(game.Path)); got != "1.8 GHz" {
		t.Errorf("ceiling row = %q, want the stored 1.8 GHz", got)
	}
	if got := gameOptionsRowValue(gameOptionsRowArgs, gameOptionsFor(game.Path)); got != "--fast" {
		t.Errorf("arguments row = %q", got)
	}
	if got := gameOptionsRowValue(gameOptionsRowCeiling, gameOptions{}); got != "Auto" {
		t.Errorf("default ceiling row = %q", got)
	}
}
