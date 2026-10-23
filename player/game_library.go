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
	"fmt"
	"log"
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
	Title  string
	Art    string
	// HasArt is true when the cover file exists right now. It is filled at
	// scan time and recounted after a download, so the completeness figures
	// describe the card rather than the intent.
	HasArt bool
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
		sort.SliceStable(games, func(i, j int) bool {
			left, right := strings.ToLower(games[i].Title), strings.ToLower(games[j].Title)
			if left == right {
				return games[i].Path < games[j].Path
			}
			return left < right
		})
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
	"NES":                  {"nes", "fc", "fceux", "famicom"},
	"SNES":                 {"snes", "sfc", "snes9x", "bsnes"},
	"Game Boy":             {"gb", "gameboy", "gambatte"},
	"Game Boy Color":       {"gbc", "gameboycolor"},
	"Game Boy Advance":     {"gba", "mgba"},
	"Nintendo DS":          {"nds", "melonds", "drastic", "desmume"},
	"Nintendo 64":          {"n64", "mupen64plus", "mupen64"},
	"Mega Drive":           {"md", "megadrive", "genesis", "genesisplusgx"},
	"Saturn":               {"sat", "saturn", "yabause", "kronos"},
	"Dreamcast":            {"dc", "dreamcast", "flycast"},
	"PlayStation":          {"ps", "psx", "pcsxrearmed", "duckstation", "pcsx"},
	"PlayStation Portable": {"psp", "ppsspp"},
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

// findGameLauncher returns the launcher path for a system from the emulator
// catalog. Exact alias matches win; a row whose name contains the alias is the
// fallback so "RetroArch GBA" or "GBA (mGBA)" still resolve.
func findGameLauncher(system string, entries []emuEntry) string {
	candidates := gameLauncherCandidates(system)
	if len(candidates) == 0 {
		return ""
	}
	usable := func(e emuEntry) bool {
		return e.Status == emuAvailable && strings.TrimSpace(e.Launch) != ""
	}
	for _, want := range candidates {
		for _, e := range entries {
			if usable(e) && normaliseLauncherName(e.Name) == want {
				return e.Launch
			}
		}
	}
	for _, want := range candidates {
		for _, e := range entries {
			if usable(e) && strings.Contains(normaliseLauncherName(e.Name), want) {
				return e.Launch
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
		log.Printf("[library] ignoring %s: %v", path, err)
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
	ext := strings.ToLower(filepath.Ext(launcher))
	switch ext {
	case ".sh", "":
		// Stock and CrossMix launchers take the ROM path as their first
		// argument and work out the core themselves.
		return "sh", []string{launcher, romPath}
	case ".bat", ".cmd":
		return "cmd", []string{"/c", launcher, romPath}
	default:
		return launcher, []string{romPath}
	}
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
		return fmt.Errorf("no launcher for %s", game.System)
	}
	if !regularFile(launcher) {
		return fmt.Errorf("launcher is missing: %s", filepath.Base(launcher))
	}
	// A RetroArch launcher gets the two cfg switches that make the next boot
	// continue from the auto state. Standalone emulators are left alone.
	prepareSavestateResume(launcher)
	name, args := gameLaunchCommand(launcher, game.Path)
	cmd := exec.Command(name, args...)
	cmd.Dir = filepath.Dir(launcher)
	restoreProfile := beginGamePowerProfile(appConfig)
	if err := cmd.Start(); err != nil {
		restoreProfile()
		return err
	}
	noteGameChildStarted()
	// The time-lapse covers exactly this child's life: it starts when the game
	// does and is assembled when the game exits. A machine with no readable
	// framebuffer returns nil here and the launch is unchanged.
	recorder := startTimelapse(appConfig, game.Title)
	go func() {
		_ = cmd.Wait()
		noteGameChildExited()
		restoreProfile()
		recorder.Stop()
	}()
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
// an unreadable one is logged and replaced on the next launch.
func loadGameRecentsFile(path string) []recentGame {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var items []recentGame
	if err := json.Unmarshal(raw, &items); err != nil {
		log.Printf("[library] ignoring %s: %v", path, err)
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

// saveGameRecentsFile writes through a temporary file so a power loss cannot
// leave half a JSON document behind.
func saveGameRecentsFile(path string, items []recentGame) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
		log.Printf("[library] cannot save recents: %v", err)
	}
	return append([]recentGame(nil), next...)
}
