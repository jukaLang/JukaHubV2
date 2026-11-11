package main

// game_library.go — the console-by-console game front end.
//
// romsetup.go already scans Roms/<system>/ folders and can download box art
// from the libretro thumbnail set. This file turns that scan into a library:
// games grouped by console, sorted by title, with the emulator that owns each
// system resolved from the same catalog the Emulators panel shows. Launching a
// game runs the system's launcher with the ROM path, which is the convention
// stock TrimUI and CrossMix launchers follow, and records the game in a small
// recents file so "Continue" survives a restart.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// gamesLibraryEntryName is the pinned Emulators row that opens the library.
const gamesLibraryEntryName = "Games Library"

// libraryGame is one playable file.
type libraryGame struct {
	Path   string
	System string
	// Title is the title derived from the ROM's file name. It stays the key
	// for metadata and playtime even when the user gives the game a display
	// title, so a rename here never moves its tags or hours.
	Title string
	Art   string
	// DisplayTitle is what the user asked the library to call the game. Empty
	// means Title. ContentKey keys the view state by the cartridge's bytes, not
	// its path, so renaming the file keeps the display title and hidden flag.
	DisplayTitle string
	ContentKey   string
	Hidden       bool
	LastPlayed   time.Time
	Playtime     int64
	Rating       int
	Year         int
	// HasArt is true when the cover file exists right now. It is filled at
	// scan time and recounted after a download, so the completeness figures
	// describe the card rather than the intent.
	HasArt bool
}

// Label is the name every library surface shows: the display title when the
// user set one, otherwise the title derived from the file.
func (g libraryGame) Label() string {
	if title := strings.TrimSpace(g.DisplayTitle); title != "" {
		return title
	}
	return g.Title
}

// librarySystem is one console row with its games.
type librarySystem struct {
	Name    string
	Games   []libraryGame
	WithArt int
}

// gameLibraryReport is the built library. Total counts every game, including
// systems that only appear in the recents list.
type gameLibraryReport struct {
	Systems []librarySystem
	Total   int
	WithArt int
	Roots   []string
}

// consoleOrder keeps the list in the order people usually look for consoles
// rather than alphabetical, which would put "Dreamcast" first.
var consoleOrder = map[string]int{
	"NES":                  0,
	"SNES":                 1,
	"Game Boy":             2,
	"Game Boy Color":       3,
	"Game Boy Advance":     4,
	"Nintendo DS":          5,
	"Nintendo 64":          6,
	"Mega Drive":           7,
	"Saturn":               8,
	"Dreamcast":            9,
	"PlayStation":          10,
	"PlayStation Portable": 11,
}

// gameDisplayTitle turns a ROM filename into something readable: the extension
// goes, underscores become spaces, and trailing release tags such as (USA) or
// [!] are dropped so the same game does not appear under four names.
func gameDisplayTitle(path string) string {
	base := filepath.Base(strings.TrimSpace(path))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return ""
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	stem = strings.ReplaceAll(stem, "_", " ")
	stem = strings.TrimSpace(stem)
	for {
		trimmed := strings.TrimSpace(stem)
		if trimmed == "" {
			return ""
		}
		switch trimmed[len(trimmed)-1] {
		case ')', ']':
			open := strings.LastIndexAny(trimmed, "([")
			if open < 0 {
				return trimmed
			}
			stem = strings.TrimSpace(trimmed[:open])
			continue
		}
		return trimmed
	}
}

// buildGameLibrary groups a scan by system, sorts it for browsing and attaches
// the artwork path romsetup.go uses for downloaded box art.
func buildGameLibrary(report romScanReport, artRoot string) gameLibraryReport {
	return buildGameLibraryWith(report, artRoot, nil)
}

// buildGameLibraryWith is buildGameLibrary with a playtime snapshot. A
// background scan passes a copy taken on the render thread; nil reads the live
// ledger (the main-thread path used by tests and non-panel callers).
func buildGameLibraryWith(report romScanReport, artRoot string, playtimeSeconds map[string]int64) gameLibraryReport {
	lib := gameLibraryReport{Roots: append([]string(nil), report.Roots...)}
	grouped := make(map[string][]libraryGame)
	for _, rom := range report.Games {
		title := gameDisplayTitle(rom.Path)
		if title == "" || strings.TrimSpace(rom.System) == "" {
			continue
		}
		art := romArtworkPath(rom, artRoot)
		grouped[rom.System] = append(grouped[rom.System], libraryGame{
			Path:   rom.Path,
			System: rom.System,
			Title:  title,
			Art:    art,
			HasArt: artworkCoverPresent(art),
		})
	}
	for name, games := range grouped {
		// Sort, display title, hidden flag and the rating/year/playtime the
		// chosen sort reads all come from the existing stores (game_library_view.go).
		libraryDecorateGames(games, playtimeSeconds)
		withArt := countCoveredGames(games)
		lib.Systems = append(lib.Systems, librarySystem{Name: name, Games: games, WithArt: withArt})
		lib.Total += len(games)
		lib.WithArt += withArt
	}
	sort.SliceStable(lib.Systems, func(i, j int) bool {
		ri, iKnown := consoleOrder[lib.Systems[i].Name]
		rj, jKnown := consoleOrder[lib.Systems[j].Name]
		switch {
		case iKnown && jKnown:
			return ri < rj
		case iKnown != jKnown:
			return iKnown
		default:
			return strings.ToLower(lib.Systems[i].Name) < strings.ToLower(lib.Systems[j].Name)
		}
	})
	return lib
}

// countCoveredGames is the number of games in a list whose cover exists.
func countCoveredGames(games []libraryGame) int {
	n := 0
	for _, game := range games {
		if game.HasArt {
			n++
		}
	}
	return n
}

// coverPercent is the share of a list that has box art, rounded to the nearest
// whole percent. An empty list reads as 0 rather than dividing by zero.
func coverPercent(withArt, total int) int {
	if total <= 0 || withArt <= 0 {
		return 0
	}
	if withArt >= total {
		return 100
	}
	return (withArt*200 + total) / (2 * total)
}

// ── Emulator resolution ──────────────────────────────────────────────────

// systemLauncherAliases maps a library system to the folder or core names the
// firmware gives its launchers. Names are compared after normalisation, so
// "mGBA" and "mgba" are the same candidate.
var systemLauncherAliases = map[string][]string{
	"NES":                  {"nes", "fc", "fceux", "fceumm", "famicom"},
	"SNES":                 {"snes", "sfc", "snes9x", "bsnes"},
	"Game Boy":             {"gb", "gameboy", "gambatte"},
	"Game Boy Color":       {"gbc", "gameboycolor"},
	"Game Boy Advance":     {"gba", "mgba"},
	"Nintendo DS":          {"nds", "melonds", "drastic", "desmume"},
	"Nintendo 64":          {"n64", "mupen64plus", "mupen64"},
	"Mega Drive":           {"md", "megadrive", "genesis", "genesisplusgx"},
	"Saturn":               {"sat", "saturn", "yabause", "kronos", "beetlesaturn"},
	"Dreamcast":            {"dc", "dreamcast", "flycast"},
	"PlayStation":          {"ps", "psx", "pcsxrearmed", "duckstation", "pcsx"},
	"PlayStation Portable": {"psp", "ppsspp"},
	"Xbox":                 {"xbox", "xemu"},
	"Master System":        {"ms", "sms", "mastersystem", "smsplus", "gearsystem"},
	"Game Gear":            {"gg", "gamegear"},
	"PC Engine":            {"pce", "pcengine", "tg16", "turbografx16"},
	"PlayStation 2":        {"ps2", "pcsx2", "aethersx2", "nethersx2"},
	"Neo Geo":              {"neogeo", "fbneo", "fbalpha"},
	"GameCube":             {"gc", "gamecube", "ngc", "dolphin"},
	"Wii":                  {"wii", "dolphin"},
}

func normaliseLauncherName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// gameLauncherCandidates returns the normalised names worth matching for a
// system, most specific first.
func gameLauncherCandidates(system string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		n := normaliseLauncherName(s)
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
	}
	for _, alias := range systemLauncherAliases[system] {
		add(alias)
	}
	add(system)
	return out
}

// launcherNameGroups splits a catalog name at brackets and separators and
// normalises each part: "Game Boy (Gambatte)" is "gameboy" and "gambatte".
func launcherNameGroups(name string) []string {
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return strings.ContainsRune("()[]{}-–—:/|,+", r)
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if n := normaliseLauncherName(p); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// launcherNameTokens is every word of a catalog name, normalised.
func launcherNameTokens(name string) []string {
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
	return words
}

// findGameLauncher returns the launcher path for a system from the emulator
// catalog. An exact alias match wins; then a bracketed part of the name ("GBA
// (mGBA)", "Game Boy (Gambatte)"), then a whole word ("RetroArch GBA").
//
// The fallback used to be a plain substring test on the squashed name, and the
// aliases are short: "nes" is inside "snes", "gb" inside "gba", "ps" inside
// "psp", and "gameboy" inside "Game Boy Color" — so NES games went to the SNES
// core and PlayStation games to PPSSPP whenever the exact name was missing.
func findGameLauncher(system string, entries []emuEntry) string {
	candidates := gameLauncherCandidates(system)
	if len(candidates) == 0 {
		return ""
	}
	usable := func(e emuEntry) bool {
		return e.Status == emuAvailable && strings.TrimSpace(e.Launch) != ""
	}
	contains := func(list []string, want string) bool {
		for _, v := range list {
			if v == want {
				return true
			}
		}
		return false
	}
	passes := []func(e emuEntry, want string) bool{
		func(e emuEntry, want string) bool { return normaliseLauncherName(e.Name) == want },
		func(e emuEntry, want string) bool { return contains(launcherNameGroups(e.Name), want) },
		func(e emuEntry, want string) bool { return contains(launcherNameTokens(e.Name), want) },
	}
	for _, match := range passes {
		for _, want := range candidates {
			for _, e := range entries {
				if usable(e) && match(e, want) {
					return e.Launch
				}
			}
		}
	}
	return ""
}

// gameLauncherOverridesPath is where a user can pin a launcher per system when
// the folder names on the card do not match the catalog.
func gameLauncherOverridesPath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "game-launchers.json")
}

// loadGameLauncherOverrides reads {"SNES": "/mnt/SDCARD/Emus/SNES/launch.sh"}.
// Missing or broken files are not an error: the catalog fallback still works.
func loadGameLauncherOverrides(path string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(path) == "" {
		return out
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	parsed := map[string]string{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		logWarn("library", "ignoring %s: %v", path, err)
		return out
	}
	for system, launcher := range parsed {
		key := strings.ToLower(strings.TrimSpace(system))
		if key == "" || strings.TrimSpace(launcher) == "" {
			continue
		}
		out[key] = strings.TrimSpace(launcher)
	}
	return out
}

// configuredGameLauncher returns an override only when the file it names still
// exists, so a stale entry falls back to the detected launcher.
func configuredGameLauncher(system string, overrides map[string]string) string {
	if len(overrides) == 0 {
		return ""
	}
	path := overrides[strings.ToLower(strings.TrimSpace(system))]
	if path == "" || !regularFile(path) {
		return ""
	}
	return path
}

// resolveGameLauncher picks the launcher for a system exactly the way the
// library screen does: an explicit override from the launcher file wins, then a
// detected emulator. Shared so Continue and the library cannot disagree about
// which emulator a system should use.
func resolveGameLauncher(system string) string {
	if launcher := configuredGameLauncher(system, loadGameLauncherOverrides(gameLauncherOverridesPath())); launcher != "" {
		return launcher
	}
	return findGameLauncher(system, emulatorEntries())
}

// ── Launching ────────────────────────────────────────────────────────────

// gameLaunchCommand builds the command and arguments used to start a game.
// Tested separately from exec so the handheld/Windows differences are pinned.
func gameLaunchCommand(launcher, romPath string) (string, []string) {
	return gameLaunchCommandArgs(launcher, romPath, nil)
}

// gameLaunchCommandArgs is gameLaunchCommand plus a game's own arguments. They
// go after the ROM path, which is where a stock launch.sh stops reading and a
// direct emulator binary (PPSSPP, RetroArch) starts parsing its options.
func gameLaunchCommandArgs(launcher, romPath string, extra []string) (string, []string) {
	ext := strings.ToLower(filepath.Ext(launcher))
	var name string
	var args []string
	switch ext {
	case ".sh", "":
		// Stock and CrossMix launchers take the ROM path as their first
		// argument and work out the core themselves.
		name, args = "sh", []string{launcher, romPath}
	case ".bat", ".cmd":
		// The batch file itself, with the ROM as its argument: gameLaunchExec
		// starts it through cmd.exe with every piece quoted (winBatchCommand).
		name, args = launcher, []string{romPath}
	case ".dll":
		// A libretro core on Windows (the Emulators page installs RetroArch
		// and its cores): the RetroArch beside it loads it with -L.
		name, args = emuRetroArchForCore(launcher), []string{"-L", launcher, romPath}
	default:
		name, args = launcher, []string{romPath}
		// xemu boots a disc image only through -dvd_path (its documented
		// command line); a bare path is handed to QEMU and ignored.
		if emuIsXemuLauncher(launcher) {
			args = []string{"-dvd_path", romPath}
		}
	}
	if len(extra) == 0 {
		return name, args
	}
	return name, append(args, extra...)
}

// gameLauncherIsBatch reports whether launcher is a Windows batch file.
func gameLauncherIsBatch(launcher string) bool {
	switch strings.ToLower(filepath.Ext(launcher)) {
	case ".bat", ".cmd":
		return true
	}
	return false
}

// gameLaunchExec is the process for a gameLaunchCommandArgs result. A batch
// launcher runs through winBatchCommand (SEC-12): exec.Command("cmd", "/c", …)
// quoted the ROM path and the game's own arguments for CommandLineToArgvW, not
// for cmd.exe, so a ROM named "Tom&calc&.sfc" started calc (BatBadBut), a
// launcher path with spaces made cmd.exe strip the quotes that remained, and
// RetroArch's "--appendconfig=a|b" became a pipe. A name or option cmd.exe
// cannot take literally (% or ") is refused with a message instead.
func gameLaunchExec(launcher, name string, args []string) (*exec.Cmd, error) {
	if !gameLauncherIsBatch(launcher) {
		return exec.Command(name, args...), nil
	}
	cmd, err := winBatchCommand(name, args...)
	if errors.Is(err, errWinBatchUnsafe) {
		return nil, fmt.Errorf("%s cannot be given a %% or a quote safely — rename the game or its folder, or change its options", filepath.Base(launcher))
	}
	if err != nil {
		return nil, fmt.Errorf("cannot start %s: %w", filepath.Base(launcher), err)
	}
	return cmd, nil
}

// launchLibraryGame starts the game detached, records it in the recents list
// and returns an error instead of toasting so the caller can pick the message.
func launchLibraryGame(game libraryGame, launcher string) error {
	if strings.TrimSpace(game.Path) == "" {
		return fmt.Errorf("no ROM selected")
	}
	if !regularFile(game.Path) {
		return fmt.Errorf("ROM is missing: %s", filepath.Base(game.Path))
	}
	if strings.TrimSpace(launcher) == "" {
		// The Xbox row has a specific, actionable answer: install hakuX, or the
		// reason this device cannot run it.
		if strings.EqualFold(strings.TrimSpace(game.System), emuXboxEntryName) {
			return fmt.Errorf("%s", xboxLibraryLauncherHintFor(hakuxDevice()))
		}
		return fmt.Errorf("no launcher for %s", game.System)
	}
	if !regularFile(launcher) {
		return fmt.Errorf("launcher is missing: %s", filepath.Base(launcher))
	}
	// A RetroArch launcher gets the two cfg switches that make the next boot
	// continue from the auto state, plus the folder its own record toggle
	// writes captures to, so the Recordings screen can list them. Standalone
	// emulators are left alone.
	prepareSavestateResume(launcher)
	// The in-game OSD is the emulator's, so its preset is written to the same
	// config just before the launch, beside the save-state switches.
	prepareOSDPreset(launcher)
	prepareRecordings(launcher)
	// Spatial audio in games: the DSP preset for this launch (spatial_games.go).
	prepareSpatialAudio(launcher)
	// Per-game options are read here, at launch time, so an edit made in the
	// options sheet applies to the very next start without a rescan.
	options := gameOptionsFor(game.Path)
	// With two or more players in Local play, a RetroArch launch gets their
	// controller order as a one-launch --appendconfig (hotseat.go).
	extra := hotseatLaunchArgs(launcher, parseGameArgs(options.Args))
	if len(extra) > 0 && !hotseatLauncherForwards(launcher) {
		// The stock and CrossMix scripts pass "$*", which joins the ROM path and
		// every option into one argument: an option there makes RetroArch look
		// for a ROM that does not exist. A script reading only "$1" ignores them
		// anyway, so they are dropped rather than passed.
		logWarn("library", "%s does not forward options; ignoring %q", filepath.Base(launcher), extra)
		extra = nil
	}
	name, args := gameLaunchCommandArgs(launcher, game.Path, extra)
	cmd, err := gameLaunchExec(launcher, name, args)
	if err != nil {
		return err
	}
	cmd.Dir = filepath.Dir(launcher)
	if strings.EqualFold(filepath.Ext(launcher), ".dll") && name != "" {
		// A Windows core starts the RetroArch beside it: run from its folder.
		cmd.Dir = filepath.Dir(name)
	}
	// Own process group: a stock launch.sh that starts RetroArch and returns
	// must not hand the framebuffer back while the game is still drawing.
	screenChildGroupConfigure(cmd)
	restoreProfile := beginGamePowerProfileCap(appConfig, options.MaxFreqKHz)
	if err := cmd.Start(); err != nil {
		restoreProfile()
		return err
	}
	noteGameChildStarted(cmd)
	// The time-lapse covers exactly this child's life: it starts when the game
	// does and is assembled when the game exits. A machine with no readable
	// framebuffer returns nil here and the launch is unchanged.
	recorder := startTimelapse(appConfig, game.Title)
	go func() {
		_ = cmd.Wait()
		screenChildReaped(cmd)
		restoreProfile()
		recorder.Stop()
	}()
	// Before the recents learn about it: a game that is not there yet is
	// starting for the first time, which is a milestone (gmeta_store.go).
	gmetaNoteLaunch(game)
	recordGameLaunch(game)
	return nil
}

// ── Recents ──────────────────────────────────────────────────────────────

const maxRecentGames = 12

// recentGame is one entry of game-recents.json.
type recentGame struct {
	Path     string    `json:"path"`
	System   string    `json:"system"`
	Title    string    `json:"title"`
	PlayedAt time.Time `json:"played_at"`
}

var (
	gameRecentsMu     sync.Mutex
	gameRecents       []recentGame
	gameRecentsLoaded bool
	// Indirection for tests: the data directory is not available in a unit
	// test, and the clock has to be pinned to compare ordering.
	gameRecentsFile = defaultGameRecentsFile
	gameClock       = time.Now
)

func defaultGameRecentsFile() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "game-recents.json")
}

// loadGameRecentsFile reads the recents list. A missing file is an empty list;
// an unreadable one is logged and replaced on the next launch. The atomic save
// leaves a .bak, so a damaged list comes back from it instead of starting over.
func loadGameRecentsFile(path string) []recentGame {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	var items []recentGame
	if _, err := readJSONWithBackup(path, &items); err != nil {
		if !os.IsNotExist(err) {
			logWarn("library", "ignoring %s: %v", path, err)
		}
		return nil
	}
	clean := items[:0]
	for _, it := range items {
		if strings.TrimSpace(it.Path) == "" {
			continue
		}
		clean = append(clean, it)
	}
	return clean
}

// saveGameRecentsFile writes through AtomicWrite: temp file, fsync, rename and
// a .bak of the previous list, so a power loss leaves the previous recents
// rather than half a document.
func saveGameRecentsFile(path string, items []recentGame) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(data, '\n'), 0o644)
}

// recentGames returns the recents list, loading it once.
func recentGames() []recentGame {
	gameRecentsMu.Lock()
	defer gameRecentsMu.Unlock()
	if !gameRecentsLoaded {
		gameRecents = loadGameRecentsFile(gameRecentsFile())
		gameRecentsLoaded = true
	}
	return append([]recentGame(nil), gameRecents...)
}

// recordGameLaunch puts a game at the top of the recents list, removes the
// older duplicate and caps the list, then persists it.
func recordGameLaunch(game libraryGame) []recentGame {
	gameRecentsMu.Lock()
	defer gameRecentsMu.Unlock()
	if !gameRecentsLoaded {
		gameRecents = loadGameRecentsFile(gameRecentsFile())
		gameRecentsLoaded = true
	}
	entry := recentGame{
		Path:     game.Path,
		System:   game.System,
		Title:    game.Title,
		PlayedAt: gameClock(),
	}
	next := make([]recentGame, 0, maxRecentGames)
	next = append(next, entry)
	for _, it := range gameRecents {
		if it.Path == entry.Path {
			continue
		}
		next = append(next, it)
		if len(next) == maxRecentGames {
			break
		}
	}
	gameRecents = next
	if err := saveGameRecentsFile(gameRecentsFile(), next); err != nil {
		logWarn("library", "cannot save recents: %v", err)
	}
	return append([]recentGame(nil), next...)
}
