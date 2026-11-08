package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// TestHomeAppIsBuiltin: the Juka badge goes on JukaHub's own apps — the
// scene- and panel-backed applets — and never on a program from the device.
func TestHomeAppIsBuiltin(t *testing.T) {
	cases := []struct {
		entry FileEntry
		want  bool
	}{
		{FileEntry{Name: "JukaLand (built-in)", Path: builtinScenePrefix + "JukaLand"}, true},
		{FileEntry{Name: "Browser", Path: builtinScenePrefix + "Browser"}, true},
		{FileEntry{Name: "PS Remote Play", Path: builtinActionPrefix + "chiaki"}, true},
		{FileEntry{Name: "Old row (built-in)", Path: "/apps/old.sh"}, true},
		{FileEntry{Name: "Notepad", Path: `C:\Apps\notepad.lnk`}, false},
		{FileEntry{Name: "Moonlight", Path: "/mnt/SDCARD/Apps/Moonlight/launch.sh"}, false},
	}
	for _, tc := range cases {
		if got := homeAppIsBuiltin(tc.entry); got != tc.want {
			t.Errorf("homeAppIsBuiltin(%q, %q) = %v, want %v", tc.entry.Name, tc.entry.Path, got, tc.want)
		}
	}
}

// TestHomeAppJukaBadgeFitsTheTile keeps the badge in the tile's top-right
// corner, clear of the artwork, on the launcher's narrowest tile and on a wide
// one; a tile too narrow for the word gets no badge rect (the dot instead).
func TestHomeAppJukaBadgeFitsTheTile(t *testing.T) {
	const textW, textH = int32(34), int32(14)
	for _, w := range []int32{appGridMinTileW, 200, 260} {
		cell := sdl.Rect{X: 40, Y: 100, W: w, H: appGridTileH}
		badge, glyph := homeAppJukaBadgeRect(cell, textW, textH)
		if badge.W <= 0 {
			t.Fatalf("tile %dpx wide: no badge", w)
		}
		art := appTileIconRect(cell)
		if badge.X < art.X+art.W {
			t.Errorf("tile %dpx: badge %+v overlaps the artwork %+v", w, badge, art)
		}
		if badge.X+badge.W > cell.X+cell.W || badge.Y < cell.Y || badge.Y+badge.H > cell.Y+cell.H {
			t.Errorf("tile %dpx: badge %+v escapes the tile %+v", w, badge, cell)
		}
		if w >= 200 && !glyph {
			t.Errorf("tile %dpx: a wide tile should carry the glyph too", w)
		}
	}
	if badge, _ := homeAppJukaBadgeRect(sdl.Rect{X: 0, Y: 0, W: 90, H: appGridTileH}, textW, textH); badge.W != 0 {
		t.Errorf("a 90px tile got badge %+v, want none", badge)
	}
}
