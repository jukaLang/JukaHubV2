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
		"Zelda 3":                "",
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
