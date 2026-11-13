package main

// gameoptions.go — per-game launch options for the library.
//
// Not every title wants the same clock. A demanding PSP game needs the full
// 2 GHz, while a Game Boy cart runs just as well at 1.2 GHz and leaves the
// handheld cooler and quieter; a Windows emulator may also need an argument the
// firmware launcher does not add on its own. Instead of four global settings
// nobody changes between games, a game can carry its own:
//
//   data/game-options.json
//   {"schemaVersion":1,"games":{
//      "/mnt/SDCARD/Roms/PSP/God of War.iso":{"max_freq_khz":1800000},
//      "/mnt/SDCARD/Roms/GBA/Zelda.gba":{"max_freq_khz":1200000,"args":"--fast"}
//   }}
//
// The file is keyed by the ROM path the scan reports, so moving a game to a
// different folder drops its options rather than applying them to a different
// title. Nothing is written for a game that only uses the defaults, and a
// missing or broken file reads as "no overrides".
//
// Two more optional fields came later, both read as "the default" when absent,
// so a file written by an older build means exactly what it meant then:
//
//   "cpu_profile":  "battery" | "balanced" | "performance" — the governor and
//                   clock a launch runs with (gameCPUProfiles below). The
//                   max_freq_khz ceiling stays its own field and, when set,
//                   wins over the profile's own ceiling.
//   "video_filter": a RetroArch picture mode (videofilter.go), or "unmanaged"
//                   to keep RetroArch's own even when Settings picks one.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	gameOptionsSchemaVersion = 1

	// gameOptionsMaxArgs caps what a mistyped field can turn into. Stock
	// launchers take the ROM path and ignore the rest, so a long tail of
	// arguments would be invisible anyway.
	gameOptionsMaxArgs   = 16
	gameOptionsMaxArgLen = 200
	gameOptionsMaxText   = 512
)

// gameOptions is what one ROM overrides. Both fields are optional: a zero
// clock means "use the global power profile", and empty arguments mean "launch
// the way the emulator screen does".
type gameOptions struct {
	MaxFreqKHz  int64  `json:"max_freq_khz,omitempty"`
	Args        string `json:"args,omitempty"`
	CPUProfile  string `json:"cpu_profile,omitempty"`
	VideoFilter string `json:"video_filter,omitempty"`

	// Emulator is the launcher this game always starts with ("Always use" in
	// the emulator picker, or the options sheet's Emulator row); "" asks each
	// time when more than one emulator fits. LastEmulator is the one picked
	// last, highlighted the next time the picker asks (emupicker.go).
	Emulator     string `json:"emulator,omitempty"`
	LastEmulator string `json:"last_emulator,omitempty"`
}

func (o gameOptions) empty() bool {
	return o.MaxFreqKHz <= 0 && strings.TrimSpace(o.Args) == "" &&
		o.CPUProfile == "" && o.VideoFilter == "" && o.Emulator == "" && o.LastEmulator == ""
}

// ── CPU profile per game ─────────────────────────────────────────────────
//
// The profile is the coarse choice and the ceiling ladder above the fine one.
// "" (Auto) is what every launch did before profiles existed: the performance
// governor under the global powerProfileMaxFreq. The others map onto the same
// machinery in powerprofile.go — a governor for the launch and a
// scaling_max_freq ceiling, both put back when the game exits.

// gameCPUProfiles is the order of the options-sheet ladder.
var gameCPUProfiles = []string{"", "battery", "balanced", "performance"}

// gameCPUBatteryCeilingKHz is Battery saver's clock cap: enough for the 8- and
// 16-bit systems, well under the A133P's 2 GHz top.
const gameCPUBatteryCeilingKHz = 1_200_000

// normaliseGameCPUProfile maps a stored value onto a known profile; anything
// else (a typo in a hand-edited file) is Auto.
func normaliseGameCPUProfile(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, p := range gameCPUProfiles {
		if v == p {
			return v
		}
	}
	return ""
}

// gameCPUProfileLabel is the word on the row and in the library list.
func gameCPUProfileLabel(v string) string {
	switch normaliseGameCPUProfile(v) {
	case "battery":
		return "Battery saver"
	case "balanced":
		return "Balanced"
	case "performance":
		return "Performance"
	}
	return "Auto"
}

// cycleGameCPUProfile moves one step along the ladder without wrapping, like
// the ceiling row.
func cycleGameCPUProfile(v string, delta int) string {
	cur := normaliseGameCPUProfile(v)
	idx := 0
	for i, p := range gameCPUProfiles {
		if p == cur {
			idx = i
		}
	}
	idx += delta
	if idx < 0 {
		idx = 0
	}
	if idx >= len(gameCPUProfiles) {
		idx = len(gameCPUProfiles) - 1
	}
	return gameCPUProfiles[idx]
}

// gameLaunchPowerConfig folds one game's options into the global profile:
//
//	Auto         performance governor, global ceiling (unchanged behaviour)
//	Performance  performance governor, the chip's top clock even when the
//	             global setting caps it
//	Balanced     a dynamic governor (schedutil, interactive, ondemand…), the
//	             global ceiling
//	Battery      a dynamic governor under a 1.2 GHz ceiling
//
// A per-game CPU limit (max_freq_khz) is applied last and wins over all of
// them, so a game that already had a cap keeps it.
func gameLaunchPowerConfig(cfg powerProfileConfig, opts gameOptions) powerProfileConfig {
	switch normaliseGameCPUProfile(opts.CPUProfile) {
	case "performance":
		cfg.Balanced = false
		cfg.MaxFreqKHz = 0
	case "balanced":
		cfg.Balanced = true
	case "battery":
		cfg.Balanced = true
		if cfg.MaxFreqKHz <= 0 || cfg.MaxFreqKHz > gameCPUBatteryCeilingKHz {
			cfg.MaxFreqKHz = gameCPUBatteryCeilingKHz
		}
	}
	if opts.MaxFreqKHz > 0 {
		cfg.MaxFreqKHz = opts.MaxFreqKHz
	}
	return cfg
}

// gameOptionsPowerSummary is the short line the library shows beside a game
// whose launch differs from the global profile: "Battery saver · 1.4 GHz",
// "1.6 GHz", or "" for a game on the defaults.
func gameOptionsPowerSummary(opts gameOptions) string {
	var parts []string
	if p := normaliseGameCPUProfile(opts.CPUProfile); p != "" {
		parts = append(parts, gameCPUProfileLabel(p))
	}
	if opts.MaxFreqKHz > 0 {
		parts = append(parts, formatGameCeiling(opts.MaxFreqKHz))
	}
	return strings.Join(parts, " · ")
}

// gameOptionsFile is the on-disk shape. The schema version is written so a
// future format can be recognised instead of misread.
type gameOptionsFile struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Games         map[string]gameOptions `json:"games"`
}

var (
	gameOptionsMu     sync.Mutex
	gameOptionsStore  map[string]gameOptions
	gameOptionsLoaded bool
	// Indirection for tests: the data directory is not writable there.
	gameOptionsPath = defaultGameOptionsPath
)

func defaultGameOptionsPath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "game-options.json")
}

// gameOptionsKey normalises a ROM path for lookups and storage, so the same
// file reached through "Roms/./GBA/x.gba" and "Roms/GBA/x.gba" shares options.
func gameOptionsKey(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

// gameOptionsCeilings is the ladder the panel cycles through. Zero is "Auto",
// which leaves the launch on the global powerProfileMaxFreq; the rest are the
// frequencies the A133P actually offers. The top entry is not special-cased
// here: raiseFrequencyCeiling never writes above the policy's own maximum, so a
// ladder entry the chip does not have simply resolves to the top OPP.
func gameOptionsCeilings() []int64 {
	return []int64{0, 2_000_000, 1_800_000, 1_600_000, 1_400_000, 1_200_000}
}

// gameOptionCeilingIndex is the ladder position of a clock, or the nearest
// entry when a hand-edited file names something in between.
func gameOptionCeilingIndex(khz int64) int {
	choices := gameOptionsCeilings()
	best := 0
	for i, c := range choices {
		if c == khz {
			return i
		}
		if c > 0 && khz > 0 && abs64(c-khz) < abs64(choices[best]-khz) {
			best = i
		}
	}
	return best
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// cycleGameCeiling moves one step along the ladder and stops at both ends. The
// ladder reads Auto, 2.0, 1.8 … 1.2 GHz, and a positive delta walks it towards
// the cooler end, so A and Right mean "less heat". It deliberately does not
// wrap: one press past "Auto" must not jump a game to the slowest clock.
func cycleGameCeiling(khz int64, delta int) int64 {
	choices := gameOptionsCeilings()
	idx := gameOptionCeilingIndex(khz) + delta
	if idx < 0 {
		idx = 0
	}
	if idx >= len(choices) {
		idx = len(choices) - 1
	}
	return choices[idx]
}

// formatGameCeiling renders a per-game cap for the panel: "Auto" for the global
// profile, otherwise the clock.
func formatGameCeiling(khz int64) string {
	if khz <= 0 {
		return "Auto"
	}
	return formatFrequencyKHz(khz)
}

// gameCeilingToast is the note shown when a game with its own CPU limit hands
// the screen back: which limit it ran under, and that the global profile is
// in charge again.
func gameCeilingToast(game libraryGame, khz int64) string {
	return game.Label() + " ran under its own CPU limit: " + formatGameCeiling(khz) +
		" · power profile restored"
}

// parseGameArgs splits a field into arguments, honouring double quotes so
// `--profile "My Game"` stays one argument. A lone backslash escapes the next
// character; an unterminated quote keeps the rest as a single argument rather
// than dropping it, because a half-typed field is more likely a mistake the user
// wants to see than a reason to launch with nothing.
func parseGameArgs(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var (
		out     []string
		current strings.Builder
		inWord  bool
		quote   rune
		escaped bool
	)
	flush := func() {
		if inWord {
			out = append(out, current.String())
			current.Reset()
			inWord = false
		}
	}
	for _, r := range text {
		switch {
		case escaped:
			current.WriteRune(r)
			inWord = true
			escaped = false
		case r == '\\' && quote != '\'':
			escaped = true
			inWord = true
		case quote != 0:
			if r == quote {
				quote = 0
				inWord = true
			} else {
				current.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
			inWord = true
		case r == ' ' || r == '\t':
			flush()
		default:
			current.WriteRune(r)
			inWord = true
		}
	}
	flush()
	if len(out) > gameOptionsMaxArgs {
		out = out[:gameOptionsMaxArgs]
	}
	for i, arg := range out {
		if len(arg) > gameOptionsMaxArgLen {
			out[i] = arg[:gameOptionsMaxArgLen]
		}
	}
	return out
}

// normaliseGameOptions trims and validates what came from a file or the panel.
func normaliseGameOptions(o gameOptions) gameOptions {
	o.Args = strings.TrimSpace(o.Args)
	if len(o.Args) > gameOptionsMaxText {
		// Cut on a rune boundary: half a UTF-8 sequence makes the whole file
		// unreadable to a later scan.
		runes := []rune(o.Args)
		limit := gameOptionsMaxText
		if limit > len(runes) {
			limit = len(runes)
		}
		o.Args = strings.TrimSpace(string(runes[:limit]))
	}
	if o.MaxFreqKHz < 0 {
		o.MaxFreqKHz = 0
	}
	o.CPUProfile = normaliseGameCPUProfile(o.CPUProfile)
	o.VideoFilter = normaliseGameVideoFilter(o.VideoFilter)
	o.Emulator = strings.TrimSpace(o.Emulator)
	o.LastEmulator = strings.TrimSpace(o.LastEmulator)
	return o
}

// loadGameOptionsFile reads the overrides. A missing file is empty; a broken
// one is logged and ignored so a bad edit cannot stop the library from opening.
// The atomic save leaves a .bak, so one damaged file costs the last change
// rather than every per-game setting on the card.
func loadGameOptionsFile(path string) map[string]gameOptions {
	out := map[string]gameOptions{}
	if strings.TrimSpace(path) == "" {
		return out
	}
	var file gameOptionsFile
	if _, err := readJSONWithBackup(path, &file); err != nil {
		if !os.IsNotExist(err) {
			logWarn("library", "ignoring %s: %v", path, err)
		}
		return out
	}
	for path, opts := range file.Games {
		key := gameOptionsKey(path)
		opts = normaliseGameOptions(opts)
		if key == "" || opts.empty() {
			continue
		}
		out[key] = opts
	}
	return out
}

// saveGameOptionsFile writes through AtomicWrite: temp file, fsync, rename and
// a .bak of the previous list. A power loss cannot leave half a document
// behind, and one damaged file costs the last change rather than every per-game
// setting on the card.
func saveGameOptionsFile(path string, games map[string]gameOptions) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	file := gameOptionsFile{SchemaVersion: gameOptionsSchemaVersion, Games: games}
	if file.Games == nil {
		file.Games = map[string]gameOptions{}
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := AtomicWrite(path, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return nil
}

// ensureGameOptionsLocked loads the overrides once per process. The caller
// holds gameOptionsMu.
func ensureGameOptionsLocked() {
	if gameOptionsLoaded {
		return
	}
	gameOptionsStore = loadGameOptionsFile(gameOptionsPath())
	gameOptionsLoaded = true
}

// gameOptionsFor returns the overrides for one ROM, or zero values. The panel
// calls this for every visible row, so it reads the map under the lock instead
// of copying it.
func gameOptionsFor(path string) gameOptions {
	key := gameOptionsKey(path)
	if key == "" {
		return gameOptions{}
	}
	gameOptionsMu.Lock()
	defer gameOptionsMu.Unlock()
	ensureGameOptionsLocked()
	return gameOptionsStore[key]
}

// setGameOptions stores or clears one game's overrides. An option set back to
// its default is removed from the file instead of written as a zero, so only
// the games that actually differ from the defaults are listed.
func setGameOptions(path string, opts gameOptions) error {
	key := gameOptionsKey(path)
	if key == "" {
		return nil
	}
	opts = normaliseGameOptions(opts)
	gameOptionsMu.Lock()
	ensureGameOptionsLocked()
	if opts.empty() {
		delete(gameOptionsStore, key)
	} else {
		gameOptionsStore[key] = opts
	}
	next := make(map[string]gameOptions, len(gameOptionsStore))
	for k, v := range gameOptionsStore {
		next[k] = v
	}
	gameOptionsMu.Unlock()

	if err := saveGameOptionsFile(gameOptionsPath(), next); err != nil {
		logWarn("library", "cannot save game options: %v", err)
		return err
	}
	return nil
}

// updateGameOptions applies a change to one game and keeps the panel in sync.
// It returns the stored result so the caller can show it after a failed write
// just as well as after a successful one.
func updateGameOptions(path string, change func(gameOptions) gameOptions) gameOptions {
	opts := normaliseGameOptions(change(gameOptionsFor(path)))
	if err := setGameOptions(path, opts); err != nil {
		ShowToast("Cannot save game options: "+err.Error(), ToastKindWarning)
	}
	return opts
}
