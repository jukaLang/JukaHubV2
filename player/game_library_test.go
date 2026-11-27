package main

// Tests for the game library and the per-game launch options: the launch
// command shape (launcher, ROM path and extra arguments stay separate
// arguments), the argument parser, and the recents list that survives a
// restart.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestGameLaunchCommand(t *testing.T) {
	cases := []struct {
		launcher string
		wantName string
		wantArgs []string
	}{
		{"/emus/GBA/launch.sh", "sh", []string{"/emus/GBA/launch.sh", "/roms/GBA/a.gba"}},
		{"/emus/GBA/launch", "sh", []string{"/emus/GBA/launch", "/roms/GBA/a.gba"}},
		// A batch file is named itself: gameLaunchExec quotes it for cmd.exe
		// (winbatch_test.go pins that line).
		{`C:\Emus\SNES\launch.bat`, `C:\Emus\SNES\launch.bat`, []string{"/roms/GBA/a.gba"}},
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
	// A system's Roms folder names find its Emus folder: Roms/WS → Emus/WS.
	emus := []emuEntry{
		{Name: "WS", Status: emuAvailable, Launch: "/e/WS/launch.sh"},
		{Name: "FBNEO", Status: emuAvailable, Launch: "/e/FBNEO/launch.sh"},
		{Name: "GBA", Status: emuAvailable, Launch: "/e/GBA/launch.sh"},
	}
	for system, want := range map[string]string{"WonderSwan": "/e/WS/launch.sh", "Arcade": "/e/FBNEO/launch.sh", "Game Boy": ""} {
		if got := findGameLauncher(system, emus); got != want {
			t.Errorf("findGameLauncher(%q) = %q, want %q", system, got, want)
		}
	}
	// An arcade set starts in the Emus folder named like its own Roms folder,
	// with no question; the other arcade folders are offered only when no
	// alias matches; NES never takes the SNES folder.
	emus = append(emus,
		emuEntry{Name: "CPS1", Status: emuAvailable, Launch: "/e/CPS1/launch.sh"},
		emuEntry{Name: "CPS2", Status: emuAvailable, Launch: "/e/CPS2/launch.sh"},
		emuEntry{Name: "SFC", Status: emuAvailable, Launch: "/e/SFC/launch.sh"})
	if got := findGameLaunchersFor("Arcade", "/mnt/SDCARD/Roms/CPS2/sf2.zip", emus); len(got) != 1 || got[0].Launch != "/e/CPS2/launch.sh" {
		t.Errorf("Roms/CPS2 game: %+v", got)
	}
	if got := findGameLaunchersFor("Arcade", "/mnt/SDCARD/Roms/MAME2003/x.zip", emus); len(got) != 3 || got[0].Launch != "/e/CPS1/launch.sh" {
		t.Errorf("Roms/MAME2003 game without its folder: %+v", got)
	}
	arcade := append(emus, emuEntry{Name: "ARCADE", Status: emuAvailable, Launch: "/e/ARCADE/launch.sh"})
	if got := findGameLaunchersFor("Arcade", "/mnt/SDCARD/Roms/MAME2003/x.zip", arcade); len(got) != 1 || got[0].Launch != "/e/ARCADE/launch.sh" {
		t.Errorf("Arcade with Emus/ARCADE: %+v", got)
	}
	if got := findGameLaunchersFor("NES", "/mnt/SDCARD/Roms/NES/a.nes", emus); len(got) != 0 {
		t.Errorf("NES game took %+v", got)
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

// Reset game takes a ROM's states by its exact stem only: the screenshots and
// JukaHub's ".jukahub-prev" copy go with them, another game's never do.
func TestGameResetStateFile(t *testing.T) {
	for name, want := range map[string]bool{
		"Zelda.state":                   true,
		"Zelda.state3":                  true,
		"Zelda.state.auto":              true,
		"Zelda.state3.png":              true,
		"Zelda.state.auto.png":          true,
		"Zelda.state.auto.jukahub-prev": true,
		"Zelda.srm":                     false,
		"Zelda II.state":                false,
		"zelda.state":                   false,
		"Zelda.state03":                 false,
		"Zelda.statex":                  false,
	} {
		if got := gameResetStateFile(name, "Zelda"); got != want {
			t.Errorf("gameResetStateFile(%q) = %v, want %v", name, got, want)
		}
	}
}

// Rename file (game_rename.go) takes only the ROM's own companions, refuses
// names a FAT card cannot hold, and rewrites only the sheet lines that name a
// renamed file in the sheet's own folder.
func TestGameRenameNamesAndSheets(t *testing.T) {
	for name, want := range map[string]int{
		"Zelda.srm":        5,
		"zelda.BIN":        5,
		"Zelda.state1.png": 5,
		"Zelda.gba.sav":    9,
		"Zelda.gba":        -1,
		"Zelda II.srm":     -1,
		"Zelda.v2.gba":     -1,
		"Zelda":            -1,
	} {
		cut, ok := gameCompanionCut(name, "Zelda.gba", "Zelda")
		if !ok {
			cut = -1
		}
		if cut != want {
			t.Errorf("gameCompanionCut(%q) = %d, want %d", name, cut, want)
		}
	}
	for text, want := range map[string]string{
		"Final Fantasy VI (USA)": "Final Fantasy VI (USA)",
		"Tetris.gb":              "Tetris",
		"  Tetris.  ":            "Tetris",
		"Pokémon":                "Pokémon",
		"a/b":                    "",
		"Star*":                  "",
		"con.txt":                "",
		".hidden":                "",
		"..":                     "",
	} {
		got, err := gameRenameCleanName(text, ".gb")
		if err != nil {
			got = ""
		}
		if got != want {
			t.Errorf("gameRenameCleanName(%q) = %q (%v), want %q", text, got, err, want)
		}
	}
	names := map[string]string{"zelda.bin": "Link.bin", "zelda (disc 1).cue": "Link (Disc 1).cue", "track02.bin": "t2.bin"}
	for _, c := range []struct{ ext, in, want string }{
		{".cue", "FILE \"Zelda.bin\" BINARY\r\n  TRACK 01 MODE1/2352\r\n", "FILE \"Link.bin\" BINARY\r\n  TRACK 01 MODE1/2352\r\n"},
		{".cue", "FILE Zelda.bin BINARY\n", "FILE Link.bin BINARY\n"},
		{".cue", "\ufeffFILE \"Zelda.bin\" BINARY\n", "\ufeffFILE \"Link.bin\" BINARY\n"},
		{".m3u", "./Zelda (Disc 1).cue|Disc 1\r\n", "./Link (Disc 1).cue|Disc 1\r\n"},
		{".m3u", "#EXTM3U\nZelda (Disc 1).cue\nsub/Zelda.bin\nOther.cue", "#EXTM3U\nLink (Disc 1).cue\nsub/Zelda.bin\nOther.cue"},
		{".gdi", "2\n1 0 4 2352 \"Zelda.bin\" 0\n2 450 0 2352 track02.bin 0\n", "2\n1 0 4 2352 \"Link.bin\" 0\n2 450 0 2352 t2.bin 0\n"},
	} {
		if got, _ := gameRenameSheetText(c.in, c.ext, names); got != c.want {
			t.Errorf("gameRenameSheetText(%s %q) = %q, want %q", c.ext, c.in, got, c.want)
		}
	}
}

// A ROM whose name matches no cover exactly still gets the closest one: the
// same game under its full title, or at least the same series, never an
// unrelated title.
func TestArtworkClosestCover(t *testing.T) {
	idx := newArtworkIndex([]string{
		"Legend of Zelda, The - The Minish Cap (USA)",
		"Pokemon - Sapphire Version (USA)",
		"Super Mario World 2 - Yoshi's Island (USA)",
		"Super Metroid (Japan, USA)",
		"Sonic The Hedgehog 2 (World)",
		"Sonic The Hedgehog (USA)",
		"Street Fighter Alpha 2 (USA)",
		"Game Boy Gallery (Europe)",
		"Castlevania - Aria of Sorrow (USA)",
	})
	for stem, want := range map[string]string{
		"Zelda Minish Cap":       "Legend of Zelda, The - The Minish Cap (USA)",
		"Pokemon Saphire (U)":    "Pokemon - Sapphire Version (USA)",
		"SuperMarioWorld2":       "Super Mario World 2 - Yoshi's Island (USA)",
		"Sonic 2":                "Sonic The Hedgehog 2 (World)",
		"Street Fighter Alpha 3": "Street Fighter Alpha 2 (USA)",
		"Aria of Sorrow":         "Castlevania - Aria of Sorrow (USA)",
		"My Homebrew Game":       "",
		// More pictures beat fewer (2026-11-17): the series' cover for
		// another number, and a title clearly ahead of the rest.
		"Zelda 3":               "Legend of Zelda, The - The Minish Cap (USA)",
		"Castlevania Dracula X": "Castlevania - Aria of Sorrow (USA)",
		"Of the Game":           "",
	} {
		if _, exact := idx.match(stem); exact {
			t.Errorf("%q matched exactly; pick a stem only the closest step finds", stem)
		}
		got, ok := idx.closest(stem)
		if got != want || ok != (want != "") {
			t.Errorf("closest(%q) = %q, %v; want %q", stem, got, ok, want)
		}
	}

	// Scene PSP/DS sets put a release number in front of the title.
	for in, want := range map[string]string{
		"0381 - 007 - From Russia with Love": "007 - From Russia with Love",
		"1617_-_Ben 10 - Alien Force":        "Ben 10 - Alien Force",
		"1942 (USA)":                         "",
		"12 - Thing":                         "",
	} {
		got, ok := artworkStripReleaseNumber(in)
		if ok != (want != "") || (ok && got != want) {
			t.Errorf("artworkStripReleaseNumber(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if got := libraryArtInitials("0381 - 007 - From Russia with Love"); got != "0F" {
		t.Errorf("initials = %q, want 0F", got)
	}

	// A fetch takes the shown console first, then the others in turns.
	var roms []scannedROM
	for i := 0; i < 30; i++ {
		roms = append(roms, scannedROM{System: "NES"})
	}
	roms = append(roms, scannedROM{System: "SNES"}, scannedROM{System: "PSP"})
	order := artworkFetchOrder(roms, "PSP")
	if len(order) != len(roms) || order[0].System != "PSP" || order[1].System != "NES" || order[artworkOrderChunk+1].System != "SNES" {
		t.Errorf("artworkFetchOrder: %d games, head %q %q", len(order), order[0].System, order[1].System)
	}
}

// data/artwork-aliases.json is the user's file (GAME-7): keys are normalised
// like the built-in table's, bad entries are dropped rather than failing the
// file, a newer layout is refused, and the alias with the file's tags is what
// a fetch looks the game up by.
func TestArtworkUserAliases(t *testing.T) {
	got, err := parseArtworkAliases([]byte(`{"aliases": {
		"Zelda Minish (USA)": "  Legend of Zelda, The - The Minish Cap ",
		"empty": "   ",
		"control": "bell\u0007",
		"": "no key"
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["zelda minish"] != "Legend of Zelda, The - The Minish Cap" {
		t.Fatalf("parsed %q", got)
	}
	if _, err := parseArtworkAliases([]byte(`{"version": 2, "aliases": {}}`)); err == nil {
		t.Error("a newer layout must be refused")
	}
	if _, err := parseArtworkAliases([]byte(`[1, 2]`)); err == nil {
		t.Error("a file that is not an object must be refused")
	}

	path := filepath.Join(t.TempDir(), artworkAliasFileName)
	oldPath := artworkAliasPath
	artworkAliasPath = func() string { return path }
	reset := func() {
		artworkUserAliases.Lock()
		artworkUserAliases.loaded, artworkUserAliases.broken, artworkUserAliases.byKey = false, false, nil
		artworkUserAliases.Unlock()
	}
	reset()
	t.Cleanup(func() { artworkAliasPath = oldPath; reset() })
	if err := os.WriteFile(path, []byte(`{"version": 1, "aliases": {"Pokemon Saphire": "Pokemon - Sapphire Version"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if name := artworkLookupName("Pokemon Saphire (U) [!]"); name != "Pokemon - Sapphire Version (U) [!]" {
		t.Errorf("lookup name = %q", name)
	}
	if name := artworkLookupName("Tetris (USA)"); name != "Tetris (USA)" {
		t.Errorf("a game with no alias is looked up as %q", name)
	}
	if alias, ok := artworkAliasFor("pokemon saphire"); !ok || alias != "Pokemon - Sapphire Version" {
		t.Errorf("the user's alias is not read before the built-in table: %q %v", alias, ok)
	}

	// A file that cannot be read is the user's: nothing is written over it.
	reset()
	if err := os.WriteFile(path, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if artworkSetUserAlias("Tetris", "Tetris DX") {
		t.Error("an alias was stored over a file that could not be read")
	}
	if data, _ := os.ReadFile(path); string(data) != `{not json` {
		t.Errorf("the unreadable file was changed: %q", data)
	}
}

// A firmware script gets the game in MainUI's form (CrossMix's
// common_launcher.sh reads its folders from it); anything else is untouched.
// Its output names what it could not find, RetroArch first.
func TestFirmwareROMArgAndLauncherOutput(t *testing.T) {
	cases := []struct{ launcher, rom, want string }{
		{"/mnt/SDCARD/Emus/GBA/launch.sh", "/mnt/SDCARD/Roms/GBA/a b.gba", "/mnt/SDCARD/Emus/GBA/../../Roms/GBA/a b.gba"},
		{"/mnt/SDCARD/Emus/SFC/snes9x.sh", "/mnt/SDCARD/Roms/SFC/Sub/m.sfc", "/mnt/SDCARD/Emus/SFC/../../Roms/SFC/Sub/m.sfc"},
		{"/mnt/SDCARD/Emus/GBA/launch.sh", "/mnt/sdcard2/Roms/GBA/a.gba", "/mnt/sdcard2/Roms/GBA/a.gba"},
		{"/usr/trimui/emus/GBA/launch.sh", "/mnt/SDCARD/Roms/GBA/a.gba", "/mnt/SDCARD/Roms/GBA/a.gba"},
		{"/mnt/SDCARD/Apps/X/launch.sh", "/mnt/SDCARD/Roms/GBA/a.gba", "/mnt/SDCARD/Roms/GBA/a.gba"},
		{"/mnt/SDCARD/Emus/PSP/PPSSPPSDL.bin", "/mnt/SDCARD/Roms/PSP/a.iso", "/mnt/SDCARD/Roms/PSP/a.iso"},
		{"/mnt/SDCARD/Emus/GBA/launch.sh", "/mnt/SDCARD/Roms", "/mnt/SDCARD/Roms"},
	}
	for _, c := range cases {
		if got := firmwareROMArg(c.launcher, c.rom); got != c.want {
			t.Errorf("firmwareROMArg(%q, %q) = %q, want %q", c.launcher, c.rom, got, c.want)
		}
	}
	out := []string{
		"/mnt/SDCARD/Emus/GBA/launch.sh /mnt/SDCARD/Emus/GBA/../../Roms/GBA/a.gba",
		"/mnt/SDCARD/Emus/GBA/launch.sh: line 4: cpufreq.sh: not found",
		"[ERROR] [Content] Failed to load content: No such file or directory",
		"/mnt/SDCARD/Emus/GBA/launch.sh: line 9: /ra64.trimui: not found",
	}
	issues, excerpt := launcherOutputFindings(out, "/mnt/SDCARD/Emus/GBA/launch.sh")
	if len(issues) != 1 || issues[0].Title != "The launch script could not find RetroArch" ||
		len(issues[0].Files) != 1 || issues[0].Files[0] != "/ra64.trimui" {
		t.Fatalf("issues = %+v", issues)
	}
	if len(excerpt) != len(out) {
		t.Errorf("excerpt = %q", excerpt)
	}
	if issues, _ := launcherOutputFindings(out[:3], "/x/launch.sh"); len(issues) != 1 || issues[0].Files[0] != "cpufreq.sh" {
		t.Errorf("without RetroArch: %+v", issues)
	}
	loader := []string{"./ra64.trimui: error while loading shared libraries: libEGL.so: cannot open shared object file: No such file or directory"}
	if issues, _ := launcherOutputFindings(loader, "/x/launch.sh"); len(issues) != 1 || issues[0].Files[0] != "libEGL.so" {
		t.Errorf("missing library: %+v", issues)
	}
	// JH-FRF8NW: the firmware SDL's setterm is no reason; PPSSPP quitting on
	// the one empty argument "$*" makes of no game is.
	ppsspp := []string{"sh: setterm: not found", "File not found: ", "VulkanMayBeAvailable: Device allowed ('SDL:Linux')"}
	if issues, _ := launcherOutputFindings(ppsspp, "/mnt/SDCARD/Emus/PPSSPP/launch.sh"); len(issues) != 1 ||
		issues[0].Title != "The launch script gave the emulator an empty game" || len(issues[0].Files) != 0 {
		t.Errorf("PPSSPP with no game: %+v", issues)
	}
	if issues, _ := launcherOutputFindings(ppsspp[:1], "/x/launch.sh"); len(issues) != 0 {
		t.Errorf("setterm alone: %+v", issues)
	}
	if issues, _ := launcherOutputFindings([]string{"File not found: /mnt/SDCARD/Roms/PSP/a.iso"}, "/x/launch.sh"); len(issues) != 1 ||
		len(issues[0].Files) != 1 || issues[0].Files[0] != "/mnt/SDCARD/Roms/PSP/a.iso" {
		t.Errorf("a game PPSSPP did not find: %+v", issues)
	}
	// JH-DMFR6X: the ARCADE tile's script gave RetroArch no game, and RetroArch
	// said so itself instead of leaving "nothing JukaHub can check".
	ra := []string{
		`[ERROR] Couldn't find any input driver named "sdl"`,
		"[ERROR] [Content]: Libretro core requires content, but nothing was provided.",
		"TRIMUI Build:Apr 30 2025 10:42:38",
	}
	if issues, _ := launcherOutputFindings(ra, "/mnt/SDCARD/Emus/ARCADE/launch.sh"); len(issues) != 1 ||
		issues[0].Title != "The launch script gave RetroArch no game" {
		t.Errorf("RetroArch with no game: %+v", issues)
	}

	// A launcher's "#!" line, read the way the kernel reads it (MainUI runs
	// the script itself): one interpreter, the rest of the line one argument.
	for _, c := range []struct{ head, interp, arg string }{
		{"#!/bin/bash\r\necho $0", "/bin/bash", ""},
		{"#! /usr/bin/env  bash -e\n", "/usr/bin/env", "bash -e"},
		{"#!/bin/sh", "/bin/sh", ""},
		{"echo no line\n", "", ""},
		{"#!\n", "", ""},
	} {
		interp, arg, ok := parseShebang([]byte(c.head))
		if interp != c.interp || arg != c.arg || ok != (c.interp != "") {
			t.Errorf("parseShebang(%q) = %q, %q, %v", c.head, interp, arg, ok)
		}
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "launch.sh")
	elf := filepath.Join(dir, "launch")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho $0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(elf, []byte("\x7fELF\x02\x01\x01"), 0o755); err != nil {
		t.Fatal(err)
	}
	if name, args := firmwareScriptCommand(script, "sh", []string{script, "/r.gba"}); name != "sh" || len(args) != 2 {
		t.Errorf("sh script: %q %q", name, args)
	}
	if name, args := firmwareScriptCommand(elf, "sh", []string{elf, "/r.gba"}); name != elf || len(args) != 1 || args[0] != "/r.gba" {
		t.Errorf("program launcher: %q %q", name, args)
	}
	if runtime.GOOS != "windows" {
		bash := filepath.Join(dir, "bash")
		if err := os.WriteFile(bash, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(script, []byte("#!"+bash+" -e\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if name, args := firmwareScriptCommand(script, "sh", []string{script, "/r.gba"}); name != bash ||
			len(args) != 3 || args[0] != "-e" || args[1] != script {
			t.Errorf("bash script: %q %q", name, args)
		}
		if err := os.WriteFile(script, []byte("#!"+filepath.Join(dir, "missing")+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if name, _ := firmwareScriptCommand(script, "sh", []string{script, "/r.gba"}); name != "sh" {
			t.Errorf("missing interpreter: %q, want sh", name)
		}
	}
}

// TestNextUIFolderParse pins how NextUI's outside data is read: "Name (TAG)"
// folder names the way MinUI's getEmuName reads them, the console a folder
// holds, the files a TAG folder lists, map.txt names and hiding, recent.txt.
func TestNextUIFolderParse(t *testing.T) {
	for name, want := range map[string]string{
		"Game Boy Advance (GBA)": "GBA",
		"Game Boy (gb)":          "gb",
		"Game Boy (GB) Hacks":    "GB",
		"Arcade (FBN) (2024)":    "2024",
		"Odd (A (B))":            "B",
		"Hacks (2024 set)":       "",
		"GBA":                    "",
		"Broken (GBA":            "",
	} {
		if _, tag, _ := nextuiFolderTag(name); tag != want {
			t.Errorf("nextuiFolderTag(%q) = %q, want %q", name, tag, want)
		}
	}
	for folder, want := range map[string]string{
		"Game Boy Advance (GBA)": "Game Boy Advance",
		"GBA Extra (MGBA)":       "Game Boy Advance",
		"Virtual Boy (VB)":       "Virtual Boy",
		"Odd Thing (ZZQ)":        "Odd Thing",
		"Hacks (2024 set)":       "",
	} {
		if system, _, _ := romFolderSystem(folder, nil); system.Name != want {
			t.Errorf("romFolderSystem(%q) = %q, want %q", folder, system.Name, want)
		}
	}
	for name, want := range map[string]bool{
		"Wario Land.vb": true, "game.qqq": true, "notes.txt": false,
		"map.txt": false, "Golden Sun.png": false, "Golden Sun.gba.st9": false, "noext": false,
	} {
		if got := nextuiListsFile(name); got != want {
			t.Errorf("nextuiListsFile(%q) = %v, want %v", name, got, want)
		}
	}
	m := nextuiParseMap([]byte("\ufeffneogeo.zip\t.Neo Geo BIOS\r\nMSlug.zip\tMetal Slug\nno tab here\n"))
	if len(m) != 2 || m["mslug.zip"] != "Metal Slug" || m["neogeo.zip"] != ".Neo Geo BIOS" {
		t.Errorf("map.txt parsed as %q", m)
	}
	if paths := nextuiParseRecents([]byte("/Roms/Game Boy (GB)/Tetris.gb\tTetris\r\n\n/Roms/PS (PS)/FF7.m3u\n"), 12); len(paths) != 2 || paths[0] != "/Roms/Game Boy (GB)/Tetris.gb" {
		t.Errorf("recent.txt parsed as %q", paths)
	}
	// A NextUI collection (collections_nextui.go): CRLF kept, a missing game's
	// line kept at its place, a line leaving the card refused but not lost.
	lines, crlf, trailing := colNextuiParse([]byte("\ufeff/Roms/Game Boy (GB)/Tetris.gb\r\n\r\n/Roms/GBA (GBA)/Gone.gba\r\n/Roms/../../etc/passwd\r\n"))
	if len(lines) != 3 || !crlf || !trailing || lines[0] != "/Roms/Game Boy (GB)/Tetris.gb" {
		t.Errorf("collection parsed as %q crlf=%v trailing=%v", lines, crlf, trailing)
	}
	card := t.TempDir()
	tetris := filepath.Join(card, "Roms", "Game Boy (GB)", "Tetris.gb")
	if err := os.MkdirAll(filepath.Dir(tetris), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tetris, []byte("rom"), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(lines) == 3 {
		if _, ok := colNextuiLineFile(card, lines[2]); ok {
			t.Errorf("a collection line leaving the card was accepted")
		}
	}
	items, from, kept, _ := colNextuiItemsFrom(card, lines, nil, true, false)
	if len(items) != 1 || items[0].Ref != homeTabRefGame+tetris || len(kept) != 2 || kept[0].At != 1 {
		t.Errorf("collection items %v kept %v", items, kept)
	}
	link := &colNextuiLink{CRLF: crlf, Lines: from, Kept: kept}
	if out, _ := colNextuiRender(card, link, items); string(out) != "/Roms/Game Boy (GB)/Tetris.gb\r\n/Roms/GBA (GBA)/Gone.gba\r\n/Roms/../../etc/passwd\r\n" {
		t.Errorf("collection written back as %q", out)
	}
}

// A console folder's config.json comes off the card: its launchlist is read
// with the names it carries, odd entries are skipped, and an alternative must
// be a .sh inside the folder that is not the folder's own launcher.
func TestEmuFolderAlternatives(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "N64")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"launch.sh", "m64p.sh", "parallel_n64.sh", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte("#!/bin/sh\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "evil.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := `{"launch":"launch.sh","launchlist":[
		{"name":"Mupen64Plus-Next","launch":"launch.sh"},
		{"name":"Mupen64Plus Standalone","launch":"m64p.sh"},
		"parallel_n64.sh", {"label":"no launch"}, 7, "notes.txt", "../evil.sh", "m64p.sh"]}`
	if err := os.WriteFile(filepath.Join(folder, "config.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	own := emuEntry{Name: "N64", Launch: filepath.Join(folder, "launch.sh"), Folder: folder, Status: emuAvailable}
	alts := emuFolderAlternatives(own)
	if len(alts) != 2 {
		t.Fatalf("alternatives = %+v, want m64p.sh and parallel_n64.sh", alts)
	}
	if alts[0].Name != "N64 · Mupen64Plus Standalone" || filepath.Base(alts[0].Launch) != "m64p.sh" || alts[0].Extra != emuChoiceAlt {
		t.Errorf("first alternative = %+v", alts[0])
	}
	if alts[1].Name != "N64 · Parallel n64" || filepath.Base(alts[1].Launch) != "parallel_n64.sh" {
		t.Errorf("second alternative = %+v", alts[1])
	}
}

// The library index (libindex.go) is a cache in front of the scan: it gives
// the scan's own answer, reads only the folders that changed after a restart,
// drops a damaged index or one of another schema without a word, and keeps
// the last library for the same folders only.
func TestLibIndexPersistsAndRebuilds(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "library-index.db")
	prev, prevList := libIndexPathFn, libIndexAlwaysList
	libIndexPathFn = func() string { return db }
	libIndexAlwaysList = false // folder times trusted, as on the handheld
	libIndexClose()
	t.Cleanup(func() {
		libIndexClose()
		libIndexPathFn, libIndexAlwaysList = prev, prevList
	})
	root := filepath.Join(dir, "Roms")
	gba := filepath.Join(root, "GBA")
	if err := os.MkdirAll(filepath.Join(gba, "Hacks"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Alpha.gba", "Beta.gba", filepath.Join("Hacks", "Gamma.gba"), "notes.txt"} {
		if err := os.WriteFile(filepath.Join(gba, name), []byte("rom"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Folder times well in the past: no listing is too close to a change.
	old := time.Now().Add(-time.Hour)
	for _, d := range []string{root, gba, filepath.Join(gba, "Hacks")} {
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
	}
	roots := []string{root}
	names := func(r romScanReport) string {
		var out []string
		for _, g := range r.Games {
			out = append(out, filepath.Base(g.Path)+"@"+g.System)
		}
		return strings.Join(out, ",")
	}
	want := names(scanROMLibrary(roots))
	if want != "Alpha.gba@Game Boy Advance,Beta.gba@Game Boy Advance,Gamma.gba@Game Boy Advance" {
		t.Fatalf("plain scan = %s", want)
	}
	got, listed, ok := libIndexReconcile(roots, false)
	if !ok || names(got) != want || listed != 3 {
		t.Fatalf("first look: ok=%v listed=%d games=%s, want %s from 3 folders", ok, listed, names(got), want)
	}

	// A restart: the index is read back, and no unchanged folder is listed.
	libIndexClose()
	libIdx.mu.Lock()
	libIdx.reconciled = true // past the run's first, full look
	libIdx.mu.Unlock()
	if got, listed, _ = libIndexReconcile(roots, false); names(got) != want || listed != 0 {
		t.Fatalf("after a restart: listed=%d games=%s, want %s from the index", listed, names(got), want)
	}

	// A game copied in moves its folder's time: that folder alone is read.
	if err := os.WriteFile(filepath.Join(gba, "Delta.gba"), []byte("rom"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, listed, _ = libIndexReconcile(roots, false); !strings.Contains(names(got), "Delta.gba") || listed != 1 {
		t.Fatalf("after a copy: listed=%d games=%s", listed, names(got))
	}

	// A damaged index is dropped and built again.
	libIndexClose()
	if err := os.WriteFile(db, []byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, ok = libIndexReconcile(roots, false); !ok || !strings.Contains(names(got), "Delta.gba") {
		t.Fatalf("damaged index: ok=%v games=%s", ok, names(got))
	}

	// So is one written by another schema.
	libIndexClose()
	stale, err := libIndexOpenDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucketIfNotExists(libIndexBucketMeta)
		if err != nil {
			return err
		}
		return meta.Put(libIndexKeySchema, []byte("0"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	libIdx.mu.Lock()
	ok = libIdx.ensureLocked()
	kept := len(libIdx.dirs)
	libIdx.mu.Unlock()
	if !ok || kept != 0 {
		t.Fatalf("another schema: ok=%v, %d folders kept", ok, kept)
	}

	// The last library comes back after a restart, for the same folders only.
	lib := gameLibraryReport{Total: 1, Systems: []librarySystem{{Name: "Game Boy Advance",
		Games: []libraryGame{{Path: filepath.Join(gba, "Alpha.gba"), System: "Game Boy Advance", Title: "Alpha"}}}}}
	libIndexSaveLibrary(roots, lib)
	libIndexClose()
	if back, ok := libIndexLoadLibrary(roots); !ok || back.Total != 1 || back.Systems[0].Games[0].Title != "Alpha" {
		t.Errorf("kept library = %+v (ok=%v)", back, ok)
	}
	if _, ok := libIndexLoadLibrary([]string{gba}); ok {
		t.Error("a library kept for other folders was offered")
	}
}
