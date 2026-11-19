package main

import "testing"

func retroTestLayout(width, height int32, count, focus int) *HomeLayout {
	h := &HomeLayout{Width: width, Height: height, Arrangement: homeArrangementCarousel}
	h.dyn.active = true
	h.dyn.tiles = make([]homeDynTile, count)
	for i := range h.dyn.tiles {
		h.dyn.tiles[i].ID = HomeFocusID(string(rune('a' + i)))
	}
	h.dyn.carIdx = focus
	return h
}

func TestEarlyPlayStationStylesUseDistinctHomeCompositions(t *testing.T) {
	want := map[string]string{
		"convert-ps1": homeCarouselPS1,
		"convert-ps2": homeCarouselPS2,
		"convert-psp": homeCarouselPSP,
	}
	for id, style := range want {
		p := GetThemePreset(id)
		if p.Arrangement != homeArrangementCarousel || p.CarouselStyle != style || normalizeCarouselStyle(p.CarouselStyle) != style {
			t.Errorf("%s: arrangement %q, style %q", id, p.Arrangement, p.CarouselStyle)
		}
		colour := GetThemePreset(style)
		if colour.Kind != themeKindColour || colour.Arrangement != "" || colour.CarouselStyle != "" {
			t.Errorf("%s Colour preset unexpectedly changes Home: %+v", style, colour)
		}
	}
}

func TestEarlyPlayStationMenusKeepVisibleTilesOnScreen(t *testing.T) {
	previous := homeConsoleLook.carousel
	defer func() { homeConsoleLook.carousel = previous }()
	for _, style := range []string{homeCarouselPS1, homeCarouselPS2, homeCarouselPSP} {
		homeConsoleLook.carousel = style
		for _, size := range [][2]int32{{1280, 720}, {960, 720}} {
			h := retroTestLayout(size[0], size[1], 13, 7)
			shown := 0
			for i := range h.dyn.tiles {
				r, ok := h.retroTileRect(i)
				if !ok {
					continue
				}
				shown++
				if r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > h.Width || r.Y < 148 || r.Y+r.H >= 650*h.Height/HomeBaseHeight {
					t.Errorf("%s %dx%d tile %d escaped the body: %+v", style, h.Width, h.Height, i, r)
				}
			}
			if shown < 4 {
				t.Errorf("%s %dx%d shows only %d tiles", style, h.Width, h.Height, shown)
			}
		}
	}
}

func TestEarlyPlayStationMenuNavigation(t *testing.T) {
	previous := homeConsoleLook.carousel
	defer func() { homeConsoleLook.carousel = previous }()
	h := retroTestLayout(1280, 720, 13, 5)
	homeConsoleLook.carousel = homeCarouselPS1
	if next, ok := h.retroStepFrom(h.dyn.tiles[5].ID, 5, FocusRight); !ok || next != h.dyn.tiles[6].ID {
		t.Fatalf("PS1 right edge did not open the next page: %q, %v", next, ok)
	}
	if next, ok := h.retroStepFrom(h.dyn.tiles[4].ID, 4, FocusUp); !ok || next != h.dyn.tiles[1].ID {
		t.Fatalf("PS1 up did not reach the tile above: %q, %v", next, ok)
	}
	if next, ok := h.retroStepFrom(h.dyn.tiles[2].ID, 2, FocusRight); !ok || next != h.dyn.tiles[2].ID {
		t.Fatalf("PS1 right from the top row crossed into the next row: %q, %v", next, ok)
	}
	homeConsoleLook.carousel = homeCarouselPS2
	if next, ok := h.retroStepFrom(h.dyn.tiles[3].ID, 3, FocusDown); !ok || next != h.dyn.tiles[4].ID {
		t.Fatalf("PS2 down did not advance the browser: %q, %v", next, ok)
	}
	if _, ok := h.retroStepFrom(h.dyn.tiles[0].ID, 0, FocusUp); ok {
		t.Fatal("PS2 up at the first item should reach the top bar")
	}
	homeConsoleLook.carousel = homeCarouselPSP
	if next, ok := h.retroStepFrom(h.dyn.tiles[3].ID, 3, FocusLeft); !ok || next != h.dyn.tiles[2].ID {
		t.Fatalf("PSP left did not move along the crossbar: %q, %v", next, ok)
	}
	if _, ok := h.retroStepFrom(h.dyn.tiles[3].ID, 3, FocusDown); ok {
		t.Fatal("PSP down should reach the footer")
	}
}
