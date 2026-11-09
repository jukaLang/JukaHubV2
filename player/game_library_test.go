package main

// Tests for the game library and the per-game launch options: the launch
// command shape (launcher, ROM path and extra arguments stay separate
// arguments), the argument parser, and the recents list that survives a
// restart.

import (
	"fmt"
	"path/filepath"
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
