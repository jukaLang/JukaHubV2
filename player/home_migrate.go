package main

// home_migrate.go — bring an unedited shipped home layout forward.
//
// jukaconfig.json is a user file: an update keeps it (patch_repo.go marks it
// KeepUser), and Home applies its Main-scene tiles card by card
// (HomeLayout.ApplyConfig). So when the shipped layout changes, a device that
// kept its file draws the new design into the old rectangles unless the file
// is moved forward. Two layouts have shipped before the current one:
//
//   - 0.4.0: a short Continue card, a Media tile a third too narrow, Settings
//     floating in the top-right corner and Exit hanging off the footer band;
//   - the 2026-11-03 redesign: the design sheet's grid, with the game library
//     as "Emulator — Retro library", the fifth tile of the bottom row.
//
// A Main scene whose twelve tiles are exactly one of those layouts — every
// title and every rectangle — was never edited by its owner, so it is moved
// to the current layout in memory at load: Games beside the hero under Media,
// Files in the bottom row. Titles and subtitles move only where they are still
// the shipped wording. Any layout somebody changed is left alone (the Games
// card still answers to "Emulator" there: homeTitleAlias).

import (
	"strconv"
	"strings"
)

// homeTileGeometry is one Main-scene tile in design coordinates (1280x720).
// id ties the same card together across layouts whose titles differ.
type homeTileGeometry struct {
	id          HomeFocusID
	text        string
	x, y, w, h  int32
	placeholder string
}

// legacyHomeTiles is the home layout jukaconfig.json shipped with in 0.4.0.
var legacyHomeTiles = []homeTileGeometry{
	{HomeFocusContinue, "Continue", 32, 164, 480, 258, "Resume your latest activity"},
	{HomeFocusMedia, "Media", 536, 164, 348, 121, "Watch and listen"},
	{HomeFocusFiles, "Files", 900, 164, 162, 121, "Browse storage"},
	{HomeFocusPackages, "Packages", 536, 301, 348, 121, "Discover content"},
	{HomeFocusChat, "Chat", 340, 454, 292, 158, "Chat with others"},
	{HomeFocusFavorites, "Favorites", 32, 454, 138, 158, "Saved"},
	{HomeFocusApps, "Apps", 900, 301, 348, 121, "Launch installed apps"},
	{HomeFocusSettings, "Settings", 1082, 164, 166, 121, "Preferences"},
	{HomeFocusTools, "Tools", 648, 454, 292, 158, "Network and utilities"},
	{HomeFocusBrowser, "Browser", 186, 454, 138, 158, "Internet"},
	{HomeFocusEmulator, "Emulator", 956, 454, 292, 158, "Play games"},
	{HomeFocusExit, "Exit", 8, 678, 116, 36, "Power down"},
}

// redesignHomeTiles is the 2026-11-03 redesign: the design sheet's grid with
// the game library at the far end of the bottom row.
var redesignHomeTiles = []homeTileGeometry{
	{HomeFocusContinue, "Continue", 32, 160, 480, 320, "Resume your latest activity"},
	{HomeFocusMedia, "Media", 528, 160, 720, 152, "Watch and listen"},
	{HomeFocusFiles, "Files", 528, 328, 230, 152, "Browse storage"},
	{HomeFocusPackages, "Packages", 774, 328, 229, 152, "Install and update"},
	{HomeFocusChat, "Chat", 444, 496, 189, 152, "Messages and servers"},
	{HomeFocusFavorites, "Favorites", 32, 496, 190, 152, "Pinned for later"},
	{HomeFocusApps, "Apps", 1019, 328, 229, 152, "Portable and desktop"},
	{HomeFocusSettings, "Settings", 1059, 496, 189, 152, "Device and design"},
	{HomeFocusTools, "Tools", 649, 496, 189, 152, "Network and system"},
	{HomeFocusBrowser, "Browser", 238, 496, 190, 152, "The open web"},
	{HomeFocusEmulator, "Emulator", 854, 496, 189, 152, "Retro library"},
	{HomeFocusExit, "Exit", 48, 674, 116, 36, "Power down"},
}

// currentHomeTiles is the layout player/jukaconfig.json (and the handheld twin
// in trimui/) ships now: the redesign's grid with Games and Files swapped, so
// the game library sits beside the hero, under Media.
var currentHomeTiles = []homeTileGeometry{
	{HomeFocusContinue, "Continue", 32, 160, 480, 320, "Resume your latest activity"},
	{HomeFocusMedia, "Media", 528, 160, 720, 152, "Watch and listen"},
	{HomeFocusFiles, "Files", 854, 496, 189, 152, "Browse storage"},
	{HomeFocusPackages, "Packages", 774, 328, 229, 152, "Install and update"},
	{HomeFocusChat, "Chat", 444, 496, 189, 152, "Messages and servers"},
	{HomeFocusFavorites, "Favorites", 32, 496, 190, 152, "Pinned for later"},
	{HomeFocusApps, "Apps", 1019, 328, 229, 152, "Portable and desktop"},
	{HomeFocusSettings, "Settings", 1059, 496, 189, 152, "Device and design"},
	{HomeFocusTools, "Tools", 649, 496, 189, 152, "Network and system"},
	{HomeFocusBrowser, "Browser", 238, 496, 190, 152, "The open web"},
	{HomeFocusEmulator, "Games", 528, 328, 230, 152, "Play your library"},
	{HomeFocusExit, "Exit", 48, 674, 116, 36, "Power down"},
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

// homeLayoutIs reports whether a scene's tiles are exactly one shipped layout:
// the same count, every title present, every rectangle where it shipped.
func homeLayoutIs(scene *SceneConfig, tiles map[string]int, layout []homeTileGeometry) bool {
	if len(tiles) != len(layout) {
		return false
	}
	for _, old := range layout {
		idx, found := tiles[old.text]
		if !found || !homeTileIs(scene.Elements[idx], old) {
			return false
		}
	}
	return true
}

// homeTileByID finds a card's entry in a layout table.
func homeTileByID(layout []homeTileGeometry, id HomeFocusID) (homeTileGeometry, bool) {
	for _, t := range layout {
		if t.id == id {
			return t, true
		}
	}
	return homeTileGeometry{}, false
}

// migrateLegacyHomeLayout moves an unedited shipped home layout (0.4.0 or the
// 2026-11-03 redesign) to the current one in memory. It reports whether it
// changed anything. It must run before the device profile scales scene
// geometry, which finalizeConfig guarantees.
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
		if !ok {
			continue
		}
		var from []homeTileGeometry
		switch {
		case homeLayoutIs(scene, tiles, legacyHomeTiles):
			from = legacyHomeTiles
		case homeLayoutIs(scene, tiles, redesignHomeTiles):
			from = redesignHomeTiles
		default:
			continue
		}
		for _, old := range from {
			want, found := homeTileByID(currentHomeTiles, old.id)
			if !found {
				continue
			}
			el := &scene.Elements[tiles[old.text]]
			el.X, el.Y = want.x, want.y
			el.Width = StringOrInt(strconv.Itoa(int(want.w)))
			el.Height = StringOrInt(strconv.Itoa(int(want.h)))
			// The title matched the shipped one to get here; it follows the
			// card's current name ("Emulator" became "Games").
			el.Text = want.text
			if p := strings.TrimSpace(el.Placeholder); p == old.placeholder || p == "" {
				el.Placeholder = want.placeholder
			}
		}
		changed = true
	}
	return changed
}
