package main

import (
	"encoding/json"
	"testing"
)

// TestChip8ParsePrograms reads a programs.json shaped like the chip8Archive's,
// with the variations it really has (a string or a number tickrate, colour
// names, authors as a list or a string) and entries that must be skipped.
func TestChip8ParsePrograms(t *testing.T) {
	raw := []byte(`{
		"zeta": {"title": "Zeta", "authors": "One Author", "platform": "schip",
			"options": {"tickrate": "200", "shiftQuirks": true, "fillColor": "lavender"}},
		"alpha": {"title": "Alpha\u0007 Game", "authors": ["A", "B"], "desc": "line one\nline two",
			"event": "Octojam 5", "release": "2018-10-01", "platform": "xochip",
			"options": {"tickrate": 1000, "backgroundColor": "#123", "buzzColor": "#FF0000", "screenRotation": 90}},
		"noTitle": {"platform": "something-else"},
		"../evil": {"title": "Path"},
		"broken": "not an object",
		"badopts": {"title": "Bad", "options": "nope"}
	}`)
	progs, err := chip8ParsePrograms(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(progs) != 4 {
		t.Fatalf("got %d programs, want 4: %+v", len(progs), progs)
	}
	byKey := map[string]chip8Program{}
	for _, p := range progs {
		byKey[p.Key] = p
	}
	if _, ok := byKey["../evil"]; ok {
		t.Fatal("a key with a path in it was listed")
	}
	a := byKey["alpha"]
	if a.Title != "Alpha  Game" || a.Authors != "A, B" || a.Desc != "line one\nline two" || a.Platform != "xochip" {
		t.Fatalf("alpha parsed as %+v", a)
	}
	o := a.options()
	if o.Tickrate != 1000 || o.Colors[chip8ColBG] != 0x112233 || !o.Buzz || o.Rotation != 90 || o.Platform != chip8PlatXO {
		t.Fatalf("alpha options = %+v", o)
	}
	z := byKey["zeta"].options()
	if z.Tickrate != 200 || !z.Quirks.Shift || z.Colors[chip8ColFill] != 0xE6E6FA || z.Buzz {
		t.Fatalf("zeta options = %+v", z)
	}
	if n := byKey["noTitle"]; n.Title != "noTitle" || n.Platform != "chip8" {
		t.Fatalf("noTitle parsed as %+v", n)
	}
	if b := byKey["badopts"]; b.Options != nil {
		t.Fatalf("a non-object options was kept: %s", b.Options)
	}
	if progs[0].Key != "alpha" {
		t.Fatalf("not sorted by title: first is %s", progs[0].Key)
	}
	if _, err := chip8ParsePrograms([]byte(`[1, 2]`)); err == nil {
		t.Fatal("a list instead of an object parsed")
	}
	if _, err := chip8ParsePrograms([]byte(`{}`)); err == nil {
		t.Fatal("an empty list parsed")
	}
}

// TestChip8ApplyOptionsDefensive reads key information and odd option values.
func TestChip8ApplyOptionsDefensive(t *testing.T) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"tickrate": "fast", "clipQuirks": "true", "logicQuirks": 1,
		"fillColor": "not-a-colour", "screenRotation": 45, "keys": {"up": 2, "a": "F", "z": 3, "b": 99}}`), &raw); err != nil {
		t.Fatal(err)
	}
	o := chip8ApplyOptions(chip8OctoDefaults(chip8PlatChip8), raw)
	if o.Tickrate != 20 || !o.Quirks.Clip || !o.Quirks.Logic || o.Colors[chip8ColFill] != chip8OctoPalette[chip8ColFill] || o.Rotation != 0 {
		t.Fatalf("options = %+v", o)
	}
	if len(o.Keys) != 2 || o.Keys["up"] != 2 || o.Keys["a"] != 15 {
		t.Fatalf("keys = %v", o.Keys)
	}
}
