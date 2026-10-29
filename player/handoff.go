package main

// handoff.go — the Game library "Open in…" handoff.
//
// The library hands its selected game straight to the tool that owns the next
// step for that title: a ready Game Profile covering the game folder, the
// exact Smart Pro Compatibility row, or the Emulators page with the console's
// tile highlighted. Whichever tool leaves the chosen target highlighted, and
// closing that tool (B) reopens the library exactly where the handoff left
// it — same console, cursor, scroll and active search — instead of falling
// back to the panel's own default return rule.

import (
	"path/filepath"
	"strings"
)

// handoffReturn remembers how to put the library back after a tool closes.
// One handoff at a time: the library is hidden while the tool is out, so no
// second handoff can start over the first.
var handoffReturn struct {
	active bool
	system int
	cursor int
	scroll int
	focus  int
	query  string
}

type handoffKind int

const (
	handoffProfiles handoffKind = iota
	handoffCompat
	handoffEmulators
)

func (k handoffKind) String() string {
	switch k {
	case handoffProfiles:
		return "Game Profiles"
	case handoffCompat:
		return "Compatibility"
	default:
		return "Emulators"
	}
}

// handoffForGame decides where a game belongs. It returns the kind and the
// row index to highlight inside that panel (-1 when the panel can only offer
// its default). Pure apart from reading the passed state, so tests can drive
// every branch without a device.
//
//   - A ready profile whose Dir covers the game folder wins: that is a PC or
//     port game, and the profile is the launcher that runs it.
//   - An exact Compatibility row (by key) is next: the catalog already says
//     something specific about this file.
//   - An emulator tile matching the console is after that, with the first
//     available non-pinned tile as a fallback so a handoff still lands
//     somewhere useful when folder naming does not match the console.
//   - Anything else falls back to Compatibility, the screen that can start a
//     scan for a title it cannot name yet.
func handoffForGame(game libraryGame, profiles []gameProfileEntry, games []gameAssessment, entries []emuEntry) (handoffKind, int) {
	for i := len(profiles) - 1; i >= 0; i-- {
		if profiles[i].ready() && profileCoversGame(profiles[i].Profile.Dir, game.Path) {
			return handoffProfiles, i
		}
	}
	key := gameKey(scannedROM{Path: game.Path, System: game.System})
	for i, a := range games {
		if a.Key == key {
			return handoffCompat, i
		}
	}
	if idx := emulatorEntryForSystem(entries, game.System); idx >= 0 {
		return handoffEmulators, idx
	}
	if idx := firstAvailableEmulatorEntry(entries); idx >= 0 {
		return handoffEmulators, idx
	}
	return handoffCompat, -1
}

// profileCoversGame reports whether a profile's game root contains the ROM
// path: an exact match or a subfolder of it. Both sides are cleaned and
// slash-normalised first, so a profile written with "/" matches a scan that
// reported "\" on Windows (and the other way around).
func profileCoversGame(profileDir, gamePath string) bool {
	dir := slashPath(strings.TrimSpace(profileDir))
	game := slashPath(strings.TrimSpace(gamePath))
	if dir == "" || game == "" {
		return false
	}
	if dir == game {
		return true
	}
	return strings.HasPrefix(game, dir+"/")
}

// slashPath cleans a path and normalises its separators to "/".
func slashPath(p string) string {
	if p == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// handoffAliases maps a folded console name to the folder/logical names
// emulator tiles use for it. Both sides go through foldName first, so the map
// keys are the folded system names the ROM scan reports.
var handoffAliases = map[string][]string{
	"gameboyadvance":      {"gba", "gameboyadvance", "mgba", "vba"},
	"gameboycolor":        {"gbc", "gameboy", "gameboycolor"},
	"gameboy":             {"gb", "gameboy"},
	"megadrive":           {"genesis", "segagenesis", "md", "megadrive", "smd"},
	"playstation":         {"psx", "ps1", "playstation", "sony", "ps"},
	"playstationportable": {"psp", "ppsspp"},
	"nintendo64":          {"n64", "mupen"},
	"dreamcast":           {"dc", "dreamcast", "redream"},
	"saturn":              {"saturn", "yabause"},
	"nintendods":          {"nds", "drastic", "melonds"},
	"pcengine":            {"pce", "tg16", "pcengine", "mednafen"},
	"neogeo":              {"neogeo", "fba", "fbneo", "mame"},
	"arcade":              {"mame", "fba", "fbneo"},
	"snes":                {"snes", "supernes", "sfc", "supernintendo"},
	"nes":                 {"nes", "famicom", "fc"},
}

// emulatorEntryForSystem scans the tile catalog for the tile that belongs to
// a console. Names on the device are folder names ("GBA", "PS", "PORTS"), so
// exact folding first, then aliases. Installed tiles win over missing ones:
// a handoff should land on the tool that works.
func emulatorEntryForSystem(entries []emuEntry, system string) int {
	best := -1
	bestScore := 0
	for i, e := range entries {
		if e.Pinned {
			continue
		}
		rank := emuEntryNameRank(e.Name, system)
		if rank < 0 {
			continue
		}
		score := rank
		if e.Status == emuAvailable {
			score = rank - 100
		}
		if best == -1 || score < bestScore {
			bestScore = score
			best = i
		}
	}
	return best
}

// emuEntryNameRank scores how well an emulator tile's name matches a console:
// 0 is a folded exact hit, 1 an aliased name, -1 no match. The rank stays raw
// so the caller can combine it with the install status.
func emuEntryNameRank(name, system string) int {
	fold := foldName(name)
	sys := foldName(system)
	if fold == "" || sys == "" {
		return -1
	}
	if fold == sys {
		return 0
	}
	for _, alias := range handoffAliases[sys] {
		if fold == alias {
			return 1
		}
	}
	return -1
}

// firstAvailableEmulatorEntry is the fallback tile: the first non-pinned,
// installed emulator in catalog order.
func firstAvailableEmulatorEntry(entries []emuEntry) int {
	for i, e := range entries {
		if !e.Pinned && e.Status == emuAvailable {
			return i
		}
	}
	return -1
}

// foldName reduces a name to lowercase letters and digits, so "Game Boy
// Advance", "game-boy-advance" and "GAMEBOY ADVANCE" all fold the same way.
func foldName(s string) string {
	var out strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			out.WriteRune(r + 'a' - 'A')
		}
	}
	return out.String()
}

// openGameLibraryHandoff sends the given game to its best tool and marks the
// library for the return trip. The library is hidden (not closed), so its box
// art cache and scanned report survive the detour.
func openGameLibraryHandoff(game libraryGame) {
	if !gameLib.open {
		return
	}
	// One snapshot drives both the decision and the cursor placement: the
	// destination panels invalidate and rebuild their caches on open, so
	// re-reading them after that could disagree with the decision.
	profiles := gameProfileEntries()
	entries := emulatorEntries()
	kind, idx := handoffForGame(game, profiles, compatGames(), entries)
	handoffReturn.active = true
	handoffReturn.system = gameLib.system
	handoffReturn.cursor = gameLib.cursor
	handoffReturn.scroll = gameLib.scroll
	handoffReturn.focus = gameLib.focus
	handoffReturn.query = gameLib.query
	// The library goes under the handoff before the destination opens: the
	// panels close each other, and an open library would fight that.
	gameLib.open = false
	switch kind {
	case handoffProfiles:
		openGameProfilePanel(false)
		gpPanel.cursor = clampPanelIndex(idx, len(profiles))
		if gpPanel.cursor < len(profiles) && profiles[gpPanel.cursor].ready() {
			title := profiles[gpPanel.cursor].Profile.Title
			ShowToast("Opening "+game.Title+" in "+title, ToastKindInfo)
			return
		}
	case handoffCompat:
		openCompatPanel(false)
		cpPanel.tab = compatTabGames
		cpPanel.cursor = clampPanelIndex(idx, compatRowCount())
	default:
		openEmulatorPanel()
		emuPanel.cursor = clampPanelIndex(idx, len(entries))
	}
	ShowToast("Opening "+game.Title+" in "+kind.String(), ToastKindInfo)
}

// clampPanelIndex keeps a row index inside a live list.
func clampPanelIndex(idx, size int) int {
	if idx < 0 {
		return 0
	}
	if idx >= size {
		if size == 0 {
			return 0
		}
		return size - 1
	}
	return idx
}

// restoreLibraryFromHandoff reopens the library exactly where the handoff
// left it. It reports whether a restore happened, so the closing panel keeps
// its own sound and skips its default return path.
func restoreLibraryFromHandoff() bool {
	if !handoffReturn.active {
		return false
	}
	handoffReturn.active = false
	gameLib.open = true
	gameLib.system = handoffReturn.system
	gameLib.cursor = handoffReturn.cursor
	gameLib.scroll = handoffReturn.scroll
	gameLib.focus = handoffReturn.focus
	gameLib.query = handoffReturn.query
	return true
}

// openGameLibraryHandoffFromSheet runs the handoff for the game the options
// sheet was opened for, so starting it from Start → Options targets the
// highlighted title rather than whichever column holds the focus.
func openGameLibraryHandoffFromSheet() {
	game := gameOptsUI.game
	closeGameOptions()
	openGameLibraryHandoff(game)
}
