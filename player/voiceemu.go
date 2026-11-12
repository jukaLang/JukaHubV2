package main

// voiceemu.go — the emulators, by voice.
//
// "Open the emulators" used to land on Settings: Home search lists Settings
// rows before its own catalog, and the Games section's "Emulators" row (an
// action that opens the page) took the title from the page itself, so the
// voice catalog only had the row — which a voice command opens by focusing it,
// not by pressing it. The page's tiles were not in the catalog at all: "open
// PPSSPP", "start Dolphin" or "the GBA emulator" found nothing unless the Apps
// launcher happened to hold a program of that name.
//
// voiceEmulatorTargets adds them to what a voice command — and a voice
// shortcut (voiceshortcuts.go) — can open, after the screens and the apps:
//
//   - the pages around the emulators: the Emulators page itself, Control
//     profiles, Emulator updates (its Settings row), Core builds, and the
//     page's own pinned tools (RetroAchievements, BIOS Doctor, Game Profiles,
//     Compatibility, Windows Apps, PortMaster), each opened the way its tile
//     opens it;
//   - every tile on the Emulators page, under its own name and the names of
//     the systems it plays ("GBA", "Game Boy Advance emulator", "Dolphin",
//     "GameCube emulator"). A program that opens to its own menu when started
//     without a game starts that way — RetroArch (its menu holds the settings,
//     the cores and their options), a PC emulator, JukaHub's own PPSSPP — and
//     anything else (a firmware launcher, which may do nothing useful without
//     a game; an emulator that is missing or broken) opens the Emulators page
//     with its tile selected, where A starts it and X installs it. A spoken
//     command never installs anything.
//
// The tile list is emulatorEntries(), which is cached; nothing here scans the
// card or drops that cache, so a command costs one walk over a short list.
// Main thread only, like the rest of the voice catalog.

import (
	"path/filepath"
	"sort"
	"strings"
)

// The subtitles of the targets from this file. The voice shortcut picker files
// everything under them in its Emulators group (voiceIsEmuTarget).
const (
	voiceEmuWherePage = "Emulators"
	voiceEmuWhereTile = "Emulator"
)

// voiceIsEmuTarget reports whether t came from voiceEmulatorTargets.
func voiceIsEmuTarget(t voiceTarget) bool {
	return strings.HasPrefix(t.Where, voiceEmuWhereTile)
}

// voiceEmuPage is one page around the emulators.
type voiceEmuPage struct {
	title, icon, keys string
	open              func(*Config)
}

// voiceEmuPages are the pages that are not tiles of the Emulators page. The
// keys carry the words people say for them; "menu", "screen" and "settings"
// need not be listed (the grammar drops the first two, and the ranking reads
// the third as "a settings page, please").
func voiceEmuPages() []voiceEmuPage {
	return []voiceEmuPage{
		{"Emulators", "emulator", "emulators emulator emulation consoles systems cores",
			func(*Config) { openEmulatorPanel() }},
		{"Control profiles", "controller", "control profiles controls controller profiles button mapping remap buttons input profiles key mapping",
			inmapOpenProfiles},
		{"Emulator updates", "update", "update emulators emulator updates update cores core updates",
			func(c *Config) { setOpenSettingRow(c, "games", "coreUpdate", "Emulator updates") }},
		{"Core builds", "rom", "core builds cores kept builds rollback retroarch cores",
			func(*Config) { openCoreBuildsPanel(false) }},
	}
}

// voiceEmuPinnedOpens reports whether a pinned tile of the Emulators page is a
// page its A opens (and so a voice command may press A on it). Anything else
// pinned is only selected.
func voiceEmuPinnedOpens(name string) bool {
	switch name {
	case biosDoctorEntryName, gamesLibraryEntryName, gameProfilesEntryName, compatEntryName,
		windowsAppsEntryName, portMasterEntryName, retroAchievementsEntryName:
		return true
	}
	return false
}

// voiceEmulatorTargets lists the pages and tiles above. A title the catalog
// already has as a screen or an app is left out: the same program under the
// same name twice made "open RetroArch" ask which of two identical rows was
// meant.
func voiceEmulatorTargets(existing []voiceTarget) []voiceTarget {
	taken := make(map[string]bool, 64)
	for _, t := range existing {
		if t.Kind == voiceTargetScreen || t.Kind == voiceTargetApp {
			taken[homeSearchFold(t.Title)] = true
		}
	}
	out := make([]voiceTarget, 0, 32)
	add := func(t voiceTarget) {
		key := homeSearchFold(t.Title)
		if key == "" || taken[key] {
			return
		}
		taken[key] = true
		out = append(out, t)
	}
	for _, p := range voiceEmuPages() {
		add(voiceTarget{Title: p.title, Where: voiceEmuWherePage, Icon: p.icon, Keys: p.keys,
			Kind: voiceTargetScreen, Action: p.open})
	}
	// JukaHub's own PPSSPP before the tiles: on a card whose firmware calls
	// its PSP folder "PPSSPP", the tile would otherwise take the name and only
	// select itself.
	if t, ok := voiceEmuManagedPPSSPP(); ok {
		add(t)
	}
	for _, e := range emulatorEntries() {
		if t, ok := voiceEmuTileTarget(e); ok {
			add(t)
		}
	}
	return out
}

// voiceEmuTileTarget is one tile of the Emulators page as a voice target.
func voiceEmuTileTarget(e emuEntry) (voiceTarget, bool) {
	name := strings.TrimSpace(e.Name)
	if name == "" {
		return voiceTarget{}, false
	}
	icon := strings.TrimSpace(e.Glyph)
	if icon == "" {
		icon = "emulator"
	}
	t := voiceTarget{Title: name, Icon: icon, Kind: voiceTargetScreen}
	if e.Pinned {
		opens := voiceEmuPinnedOpens(name)
		t.Where, t.Keys = voiceEmuWherePage, "emulators"
		t.Action = func(c *Config) { voiceEmuOpenTile(c, name, opens) }
		return t, true
	}
	t.Where = voiceEmuWhereTile
	switch e.Status {
	case emuMissing:
		t.Where += " · not installed"
	case emuBroken:
		t.Where += " · not set up"
	case emuUnsupported:
		t.Where += " · not supported here"
	}
	t.Keys = voiceEmuKeys(e)
	if emuRetroArchMenuTile(e) || voiceEmuStarts(e) {
		t.Kind = voiceTargetApp
	}
	t.Action = func(c *Config) { voiceEmuRunTile(c, name) }
	return t, true
}

// voiceEmuKeys are the words a tile goes by besides its name: "emulator" and
// the systems it plays, from the ROM folder table, the launcher aliases the
// Games library resolves by, and the core table. Sorted, so a list rebuilt for
// every command reads the same each time.
func voiceEmuKeys(e emuEntry) string {
	seen := map[string]bool{}
	var systems []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			return
		}
		seen[strings.ToLower(s)] = true
		systems = append(systems, s)
	}
	if sys, ok := romSystemAliases[strings.ToLower(strings.TrimSpace(e.Name))]; ok {
		add(sys.Name)
	}
	if norm := normaliseLauncherName(e.Name); norm != "" {
		for system, aliases := range systemLauncherAliases {
			for _, a := range aliases {
				if a == norm {
					add(system)
					break
				}
			}
		}
	}
	if system, _ := emuCoreInfo(e.Core); system != "" {
		add(system)
	}
	add(e.Via)
	sort.Strings(systems)
	return strings.TrimSpace("emulator emulators " + strings.Join(systems, " "))
}

// voiceEmuStarts reports whether a tile is a program that starts on its own
// and opens its own menu: a PC emulator, or a libretro core RetroArch loads
// with no game. (The handheld's RetroArch tile is emuRetroArchMenuTile.)
func voiceEmuStarts(e emuEntry) bool {
	if !IsWindows() || e.Pinned || e.Status != emuAvailable {
		return false
	}
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(e.Launch))) {
	case ".exe", ".bat", ".cmd", ".lnk", ".url", ".dll":
		return true
	}
	return false
}

// voiceEmuEntry finds a tile by name in the current catalog.
func voiceEmuEntry(name string) (emuEntry, bool) {
	for _, e := range emulatorEntries() {
		if e.Name == name {
			return e, true
		}
	}
	return emuEntry{}, false
}

// voiceEmuRunTile opens one tile, from the catalog as it is now: an emulator
// may have been installed or removed since the command was understood.
func voiceEmuRunTile(config *Config, name string) {
	e, ok := voiceEmuEntry(name)
	if !ok {
		ShowToast(name+" is no longer on the Emulators page", ToastKindWarning)
		return
	}
	switch {
	case emuRetroArchMenuTile(e):
		// RetroArch with no game: its own menu (raemu.go).
		launchRetroArchMenu(e)
	case hakuxTile(e):
		// The handheld's Xbox tile: its games, or its setup sheet — which is
		// drawn only on the Emulators page, so A is pressed there.
		voiceEmuOpenTile(config, name, true)
	case voiceEmuStarts(e):
		launchEmulatorEntry(e)
	case e.Status == emuAvailable && strings.TrimSpace(e.Launch) == "":
		// A core row: the games it plays, in the Games library.
		emuCoreRowActivate(e)
	default:
		voiceEmuOpenTile(config, name, false)
	}
}

// voiceEmuOpenTile opens the Emulators page with the named tile selected and,
// when press is set, presses A on it. A tile that has gone leaves the page at
// its first tile.
func voiceEmuOpenTile(config *Config, name string, press bool) {
	openEmulatorPanel()
	entries := emulatorEntries()
	for i := range entries {
		if entries[i].Name != name {
			continue
		}
		emuPanel.cursor = i
		if press {
			emuPanelActivate(config)
		}
		return
	}
}

// voiceEmuManagedPPSSPP is JukaHub's own PPSSPP on the handheld, when one is
// installed: started with no game, its script opens PPSSPP's own menu
// (ppsspp_managed.go).
func voiceEmuManagedPPSSPP() (voiceTarget, bool) {
	if !ppssppManagedEnabled() {
		return voiceTarget{}, false
	}
	if _, ok := voiceEmuPPSSPPScript(); !ok {
		return voiceTarget{}, false
	}
	return voiceTarget{Title: "PPSSPP", Where: voiceEmuWhereTile + " · JukaHub's", Icon: "emulator",
		Keys: "ppsspp psp playstation portable", Kind: voiceTargetApp,
		Action: func(*Config) { voiceEmuStartPPSSPP() }}, true
}

// voiceEmuPPSSPPScript is the active PPSSPP build's launch script.
func voiceEmuPPSSPPScript() (string, bool) {
	st := ppssppSnapshot()
	b, ok := st.build(st.Active)
	if !ok {
		return "", false
	}
	script := b.script()
	if !regularFile(script) {
		return "", false
	}
	return script, true
}

// voiceEmuStartPPSSPP starts JukaHub's PPSSPP with no game.
func voiceEmuStartPPSSPP() {
	script, ok := voiceEmuPPSSPPScript()
	if !ok {
		ShowToast("JukaHub's PPSSPP is not installed — the Emulators page installs it", ToastKindWarning)
		return
	}
	launchEmulatorEntry(emuEntry{Name: "PPSSPP", Launch: script, Status: emuAvailable})
}
