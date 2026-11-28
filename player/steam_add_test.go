package main

import (
	"bytes"
	"testing"
)

func TestSteamShortcutBlock(t *testing.T) {
	block := steamShortcutBlock(3, 0x81234567, "JukaHub", "C:\\Games\\JukaHub.exe", "C:\\Games\\JukaHub.exe")
	for _, want := range []string{
		"\x00" + "3\x00", "appid\x00", "appname\x00", "JukaHub\x00",
		"exe\x00", "C:\\Games\\JukaHub.exe\x00", "icon\x00", "LastPlayTime\x00",
		"tags\x00", "JukaHub",
	} {
		if !bytes.Contains(block, []byte(want)) {
			t.Fatalf("block is missing %q", want)
		}
	}
	if block[len(block)-1] != 0x08 {
		t.Fatal("the entry does not end with a block terminator")
	}
}

func TestSteamAppendShortcutKeepsExistingGames(t *testing.T) {
	// One existing shortcut with an unusual value type (int32) that a
	// parse-and-rewrite would turn into a string.
	old := steamShortcutBlock(7, 0x8aaaaaaa, "Other game", "/old/game", "/old/icon")
	old = append(old, 0x08) // root terminator
	data, err := steamAppendShortcut(old, "/new/jukahub", "/new/jukahub")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, old[:len(old)-1]) {
		t.Fatal("the existing shortcut's bytes changed")
	}
	if !bytes.Contains(data, []byte("Other game\x00")) || !bytes.Contains(data, []byte("/new/jukahub\x00")) {
		t.Fatal("one of the two shortcuts is missing")
	}
	if got := steamNextShortcutIndex(data); got != 9 {
		t.Fatalf("next index = %d, want 9", got)
	}
	if data[len(data)-1] != 0x08 {
		t.Fatal("the file does not end with a root terminator")
	}

	fresh, err := steamAppendShortcut(nil, "/new/jukahub", "/new/jukahub")
	if err != nil || !bytes.Contains(fresh, []byte("JukaHub\x00")) {
		t.Fatalf("fresh file = %q, %v", fresh, err)
	}
}
