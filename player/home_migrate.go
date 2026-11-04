package main

// home_migrate.go — bring an unedited 0.4.0 home layout forward.
//
// jukaconfig.json is a user file: an update keeps it (patch_repo.go marks it
// KeepUser), and Home applies its Main-scene tiles card by card
// (HomeLayout.ApplyConfig). The home redesign moved every card — a 480x320
// Continue card beside a 720px Media card over a row of three, then a full-width
// row of six 152px tiles — so a device that kept its 0.4.0 file drew the new
// design into the old rectangles: a short Continue card, a Media tile a third
// too narrow, Settings floating in the top-right corner and Exit hanging off the
// footer band. The handheld config shipped in trimui/ had the same old layout.
//
// A Main scene whose twelve tiles are exactly the old shipped layout — every
// title and every rectangle — was never edited by its owner, so it is moved to
// the current layout in memory at load. Subtitles move only where they are
// still the old shipped wording. Any layout somebody changed is left alone.

import (
	"strconv"
	"strings"
)

// homeTileGeometry is one Main-scene tile in design coordinates (1280x720).
type homeTileGeometry struct {
	text        string
	x, y, w, h  int32
	placeholder string
}

// legacyHomeTiles is the home layout jukaconfig.json shipped with in 0.4.0.
var legacyHomeTiles = []homeTileGeometry{
	{"Continue", 32, 164, 480, 258, "Resume your latest activity"},
	{"Media", 536, 164, 348, 121, "Watch and listen"},
	{"Files", 900, 164, 162, 121, "Browse storage"},
	{"Packages", 536, 301, 348, 121, "Discover content"},
	{"Chat", 340, 454, 292, 158, "Chat with others"},
	{"Favorites", 32, 454, 138, 158, "Saved"},
	{"Apps", 900, 301, 348, 121, "Launch installed apps"},
	{"Settings", 1082, 164, 166, 121, "Preferences"},
	{"Tools", 648, 454, 292, 158, "Network and utilities"},
	{"Browser", 186, 454, 138, 158, "Internet"},
	{"Emulator", 956, 454, 292, 158, "Play games"},
	{"Exit", 8, 678, 116, 36, "Power down"},
}

// currentHomeTiles is the redesigned layout (the design sheet's grid, and what
// player/jukaconfig.json ships now).
var currentHomeTiles = []homeTileGeometry{
	{"Continue", 32, 160, 480, 320, "Resume your latest activity"},
	{"Media", 528, 160, 720, 152, "Watch and listen"},
	{"Files", 528, 328, 230, 152, "Browse storage"},
	{"Packages", 774, 328, 229, 152, "Install and update"},
	{"Chat", 444, 496, 189, 152, "Messages and servers"},
	{"Favorites", 32, 496, 190, 152, "Pinned for later"},
	{"Apps", 1019, 328, 229, 152, "Portable and desktop"},
	{"Settings", 1059, 496, 189, 152, "Device and design"},
	{"Tools", 649, 496, 189, 152, "Network and system"},
	{"Browser", 238, 496, 190, 152, "The open web"},
	{"Emulator", 854, 496, 189, 152, "Retro library"},
	{"Exit", 48, 674, 116, 36, "Power down"},
}

// homeTileIndex maps each tile button's title to its element index. A title
// that appears twice makes the layout ambiguous, which reports ok=false.
func homeTileIndex(scene *SceneConfig) (map[string]int, bool) {
	out := make(map[string]int, len(scene.Elements))
	for i, el := range scene.Elements {
		if el.Type != "button" || !strings.EqualFold(el.Style, "tile") {
			continue
		}
		title := strings.TrimSpace(el.Text)
		if _, dup := out[title]; dup {
			return nil, false
		}
		out[title] = i
	}
	return out, true
}

// homeTileIs reports whether an element sits exactly at a tile's rectangle.
func homeTileIs(el Element, want homeTileGeometry) bool {
	w, okW := parsePosInt(strings.TrimSpace(string(el.Width)))
	h, okH := parsePosInt(strings.TrimSpace(string(el.Height)))
	return okW && okH && el.X == want.x && el.Y == want.y && w == want.w && h == want.h
}

// migrateLegacyHomeLayout moves an unedited 0.4.0 home layout to the current
// one in memory. It reports whether it changed anything. It must run before
// the device profile scales scene geometry, which finalizeConfig guarantees.
func migrateLegacyHomeLayout(config *Config) bool {
	if config == nil {
		return false
	}
	changed := false
	for si := range config.Scenes {
		scene := &config.Scenes[si]
		if !strings.EqualFold(strings.TrimSpace(scene.Layout), "home") {
			continue
		}
		tiles, ok := homeTileIndex(scene)
		if !ok || len(tiles) != len(legacyHomeTiles) {
			continue
		}
		matches := true
		for _, old := range legacyHomeTiles {
			idx, found := tiles[old.text]
			if !found || !homeTileIs(scene.Elements[idx], old) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		for i, want := range currentHomeTiles {
			el := &scene.Elements[tiles[want.text]]
			el.X, el.Y = want.x, want.y
			el.Width = StringOrInt(strconv.Itoa(int(want.w)))
			el.Height = StringOrInt(strconv.Itoa(int(want.h)))
			if strings.TrimSpace(el.Placeholder) == legacyHomeTiles[i].placeholder {
				el.Placeholder = want.placeholder
			}
		}
		changed = true
	}
	return changed
}
