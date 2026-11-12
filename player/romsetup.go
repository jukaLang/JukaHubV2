package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type romSystem struct {
	Name       string
	Collection string
}

type scannedROM struct {
	Path       string
	System     string
	Collection string
}

type romScanReport struct {
	Roots  []string
	Games  []scannedROM
	Counts map[string]int
}

var romSystemAliases = map[string]romSystem{
	"fc":      {"NES", "Nintendo - Nintendo Entertainment System"},
	"nes":     {"NES", "Nintendo - Nintendo Entertainment System"},
	"famicom": {"NES", "Nintendo - Nintendo Entertainment System"},
	"nintendo - nintendo entertainment system": {"NES", "Nintendo - Nintendo Entertainment System"},
	"sfc":  {"SNES", "Nintendo - Super Nintendo Entertainment System"},
	"snes": {"SNES", "Nintendo - Super Nintendo Entertainment System"},
	"nintendo - super nintendo entertainment system": {"SNES", "Nintendo - Super Nintendo Entertainment System"},
	"gb":                          {"Game Boy", "Nintendo - Game Boy"},
	"nintendo - game boy":         {"Game Boy", "Nintendo - Game Boy"},
	"gbc":                         {"Game Boy Color", "Nintendo - Game Boy Color"},
	"gba":                         {"Game Boy Advance", "Nintendo - Game Boy Advance"},
	"nintendo - game boy advance": {"Game Boy Advance", "Nintendo - Game Boy Advance"},
	"md":                          {"Mega Drive", "Sega - Mega Drive - Genesis"},
	"megadrive":                   {"Mega Drive", "Sega - Mega Drive - Genesis"},
	"genesis":                     {"Mega Drive", "Sega - Mega Drive - Genesis"},
	"n64":                         {"Nintendo 64", "Nintendo - Nintendo 64"},
	"nintendo - nintendo 64":      {"Nintendo 64", "Nintendo - Nintendo 64"},
	"ps":                          {"PlayStation", "Sony - PlayStation"},
	"ps1":                         {"PlayStation", "Sony - PlayStation"},
	"psx":                         {"PlayStation", "Sony - PlayStation"},
	"sony - playstation":          {"PlayStation", "Sony - PlayStation"},
	"psp":                         {"PlayStation Portable", "Sony - PlayStation Portable"},
	"ppsspp":                      {"PlayStation Portable", "Sony - PlayStation Portable"}, // the stock firmware's Roms/PPSSPP
	"playstation portable":        {"PlayStation Portable", "Sony - PlayStation Portable"},
	"nds":                         {"Nintendo DS", "Nintendo - Nintendo DS"},
	"nintendo ds":                 {"Nintendo DS", "Nintendo - Nintendo DS"},
	"sat":                         {"Saturn", "Sega - Saturn"},
	"saturn":                      {"Saturn", "Sega - Saturn"},
	"sega - saturn":               {"Saturn", "Sega - Saturn"},
	"dc":                          {"Dreamcast", "Sega - Dreamcast"},
	"dreamcast":                   {"Dreamcast", "Sega - Dreamcast"},
	"sega - dreamcast":            {"Dreamcast", "Sega - Dreamcast"},
	"xbox":                        {"Xbox", "Microsoft - Xbox"},
	"xbox1":                       {"Xbox", "Microsoft - Xbox"},
	"microsoft - xbox":            {"Xbox", "Microsoft - Xbox"},
	// Consoles the ROM manager (romm_detect.go) files games for, under the
	// folder names TrimUI's firmware and CrossMix use.
	"ms":                              {"Master System", "Sega - Master System - Mark III"},
	"sms":                             {"Master System", "Sega - Master System - Mark III"},
	"mastersystem":                    {"Master System", "Sega - Master System - Mark III"},
	"sega - master system - mark iii": {"Master System", "Sega - Master System - Mark III"},
	"gg":                              {"Game Gear", "Sega - Game Gear"},
	"gamegear":                        {"Game Gear", "Sega - Game Gear"},
	"sega - game gear":                {"Game Gear", "Sega - Game Gear"},
	"pce":                             {"PC Engine", "NEC - PC Engine - TurboGrafx 16"},
	"pcengine":                        {"PC Engine", "NEC - PC Engine - TurboGrafx 16"},
	"tg16":                            {"PC Engine", "NEC - PC Engine - TurboGrafx 16"},
	"nec - pc engine - turbografx 16": {"PC Engine", "NEC - PC Engine - TurboGrafx 16"},
	"ps2":                             {"PlayStation 2", "Sony - PlayStation 2"},
	"sony - playstation 2":            {"PlayStation 2", "Sony - PlayStation 2"},
	"neogeo":                          {"Neo Geo", "SNK - Neo Geo"},
	"snk - neo geo":                   {"Neo Geo", "SNK - Neo Geo"},
	"gc":                              {"GameCube", "Nintendo - GameCube"},
	"gamecube":                        {"GameCube", "Nintendo - GameCube"},
	"ngc":                             {"GameCube", "Nintendo - GameCube"},
	"nintendo - gamecube":             {"GameCube", "Nintendo - GameCube"},
	"wii":                             {"Wii", "Nintendo - Wii"},
	"nintendo - wii":                  {"Wii", "Nintendo - Wii"},
	// The Xbox 360, which Xenia Canary plays on Windows (emusystems.go).
	"xbox360":              {"Xbox 360", "Microsoft - Xbox 360"},
	"x360":                 {"Xbox 360", "Microsoft - Xbox 360"},
	"xbox 360":             {"Xbox 360", "Microsoft - Xbox 360"},
	"microsoft - xbox 360": {"Xbox 360", "Microsoft - Xbox 360"},
	// The rest of the TrimUI core inventory (emusystems.go): the folder names
	// the firmware, CrossMix and No-Intro sets use for systems the library
	// already had a core for but no scan alias.
	"arcade":   {"Arcade", "MAME"},
	"mame":     {"Arcade", "MAME"},
	"fbneo":    {"Arcade", "MAME"},
	"fba":      {"Arcade", "MAME"},
	"mame2003": {"Arcade", "MAME"},
	"mame2010": {"Arcade", "MAME"},
	"cps1":     {"Arcade", "MAME"},
	"cps2":     {"Arcade", "MAME"},
	"cps3":     {"Arcade", "MAME"},

	"a2600":              {"Atari 2600", "Atari - 2600"},
	"atari2600":          {"Atari 2600", "Atari - 2600"},
	"atari - atari 2600": {"Atari 2600", "Atari - 2600"},
	"a5200":              {"Atari 5200", "Atari - 5200"},
	"atari5200":          {"Atari 5200", "Atari - 5200"},
	"a7800":              {"Atari 7800", "Atari - 7800"},
	"atari7800":          {"Atari 7800", "Atari - 7800"},
	"atari - atari 7800": {"Atari 7800", "Atari - 7800"},

	// CrossMix files Atari 2600 games under Roms/ATARI; the libretro names
	// ("Atari - 2600") are what its thumbnail and database folders are called.
	"atari":        {"Atari 2600", "Atari - 2600"},
	"atari - 2600": {"Atari 2600", "Atari - 2600"},
	"atari - 7800": {"Atari 7800", "Atari - 7800"},
	"atari - lynx": {"Atari Lynx", "Atari - Lynx"},

	"jaguar":             {"Atari Jaguar", "Atari - Jaguar"},
	"atarijaguar":        {"Atari Jaguar", "Atari - Jaguar"},
	"lynx":               {"Atari Lynx", "Atari - Lynx"},
	"atarilynx":          {"Atari Lynx", "Atari - Lynx"},
	"atari - atari lynx": {"Atari Lynx", "Atari - Lynx"},

	"ws":                        {"WonderSwan", "Bandai - WonderSwan"},
	"wonderswan":                {"WonderSwan", "Bandai - WonderSwan"},
	"wsc":                       {"WonderSwan Color", "Bandai - WonderSwan Color"},
	"wonderswan color":          {"WonderSwan Color", "Bandai - WonderSwan Color"},
	"bandai - wonderswan":       {"WonderSwan", "Bandai - WonderSwan"},
	"bandai - wonderswan color": {"WonderSwan Color", "Bandai - WonderSwan Color"},

	"ngp":                        {"Neo Geo Pocket", "SNK - Neo Geo Pocket"},
	"neogeopocket":               {"Neo Geo Pocket", "SNK - Neo Geo Pocket"},
	"neogeo pocket":              {"Neo Geo Pocket", "SNK - Neo Geo Pocket"},
	"ngpc":                       {"Neo Geo Pocket Color", "SNK - Neo Geo Pocket Color"},
	"neogeo pocket color":        {"Neo Geo Pocket Color", "SNK - Neo Geo Pocket Color"},
	"snk - neo geo pocket":       {"Neo Geo Pocket", "SNK - Neo Geo Pocket"},
	"snk - neo geo pocket color": {"Neo Geo Pocket Color", "SNK - Neo Geo Pocket Color"},
	"neocd":                      {"Neo Geo CD", "SNK - Neo Geo CD"},

	"segacd":         {"Sega CD", "Sega - Mega-CD - Sega CD"},
	"sega cd":        {"Sega CD", "Sega - Mega-CD - Sega CD"},
	"scd":            {"Sega CD", "Sega - Mega-CD - Sega CD"},
	"sega - sega cd": {"Sega CD", "Sega - Mega-CD - Sega CD"},
	"megacd":         {"Sega CD", "Sega - Mega-CD - Sega CD"},
	"32x":            {"32X", "Sega - 32X"},
	"sega32x":        {"32X", "Sega - 32X"},
	"sega - 32x":     {"32X", "Sega - 32X"},

	// Systems with no libretro core: their own program plays the folder.
	"pico8":  {"PICO-8", "PICO-8"},
	"pico-8": {"PICO-8", "PICO-8"},
	"pico":   {"PICO-8", "PICO-8"}, // CrossMix's Roms/PICO
	"dos":    {"DOS", "DOS"},
	"dosbox": {"DOS", "DOS"},
}

var romFileExtensions = map[string]bool{
	".3ds": true, ".bin": true, ".chd": true, ".ciso": true, ".cue": true,
	".gb": true, ".gba": true, ".gbc": true, ".gcm": true, ".gcz": true,
	".7z": true, ".gen": true, ".iso": true, ".md": true, ".nds": true, ".nes": true,
	".n64": true, ".pbp": true, ".pce": true, ".rom": true, ".sfc": true,
	".smc": true, ".z64": true, ".zip": true, ".v64": true, ".ndd": true,
	".cdi": true, ".gdi": true, ".m3u": true, ".xiso": true,
	".sms": true, ".gg": true, ".smd": true, ".fds": true, ".cso": true, ".rvz": true, ".wbfs": true,
	".unf": true, ".unif": true, ".swc": true, ".fig": true,
	// Atari, WonderSwan, Neo Geo Pocket, 32X and PICO-8 cart images. (.p8
	// only: a .p8.png is also a picture the image viewer opens.)
	".a26": true, ".a52": true, ".a78": true, ".lnx": true,
	".ws": true, ".wsc": true, ".ngp": true, ".32x": true, ".p8": true,
}

// romSheetMaxBytes bounds how much of a .cue, .gdi or .m3u is read: real sheets
// are a few hundred bytes, and a mislabelled multi-megabyte file must not be
// slurped by a library scan.
const romSheetMaxBytes = 256 << 10

// romSheetReferences returns the files a .cue, .gdi or .m3u names, as cleaned
// paths beside the sheet. Those files are parts of the game the sheet starts —
// the tracks of a CD, the discs of a multi-disc set — not games of their own.
func romSheetReferences(sheet string) []string {
	ext := strings.ToLower(filepath.Ext(sheet))
	if ext != ".cue" && ext != ".gdi" && ext != ".m3u" {
		return nil
	}
	f, err := os.Open(sheet)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, romSheetMaxBytes))
	if err != nil {
		return nil
	}
	dir := filepath.Dir(sheet)
	var out []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		// Sheets written on Windows separate folders with backslashes.
		name = filepath.FromSlash(strings.ReplaceAll(name, "\\", "/"))
		if !filepath.IsAbs(name) {
			name = filepath.Join(dir, name)
		}
		out = append(out, filepath.Clean(name))
	}
	quoted := func(s string) (string, bool) {
		start := strings.IndexByte(s, '"')
		if start < 0 {
			return "", false
		}
		end := strings.IndexByte(s[start+1:], '"')
		if end < 0 {
			return "", false
		}
		return s[start+1 : start+1+end], true
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch ext {
		case ".m3u":
			if !strings.HasPrefix(line, "#") {
				add(line)
			}
		case ".cue":
			// FILE "Game (Track 1).bin" BINARY
			if len(line) < 5 || !strings.EqualFold(line[:5], "FILE ") {
				continue
			}
			if name, ok := quoted(line); ok {
				add(name)
			} else if fields := strings.Fields(line); len(fields) > 1 {
				add(fields[1])
			}
		case ".gdi":
			// <track> <lba> <type> <sector size> <file> <offset>
			if name, ok := quoted(line); ok {
				add(name)
			} else if fields := strings.Fields(line); len(fields) >= 6 {
				add(fields[4])
			}
		}
	}
	return out
}

// dropSheetParts removes the files a sheet in the same scan refers to, so a CD
// game is one row (its .cue, or its .m3u for a multi-disc set) instead of the
// sheet plus one row per track or disc, and recounts the systems. Paths are
// compared without case: sheets are written by hand and the cards are FAT.
func dropSheetParts(games []scannedROM) ([]scannedROM, map[string]int) {
	referenced := map[string]bool{}
	for _, g := range games {
		for _, ref := range romSheetReferences(g.Path) {
			if !strings.EqualFold(ref, g.Path) {
				referenced[strings.ToLower(ref)] = true
			}
		}
	}
	counts := make(map[string]int)
	kept := games[:0]
	for _, g := range games {
		if referenced[strings.ToLower(g.Path)] {
			continue
		}
		kept = append(kept, g)
		counts[g.System]++
	}
	return kept, counts
}

func romLibraryRoots() []string {
	var roots []string
	if !IsWindows() {
		roots = append(roots, deviceRomsRoot)
	}
	roots = append(roots, resolvePath("Roms"))
	if dir, err := P().DataDir(); err == nil && dir != "" {
		roots = append(roots, filepath.Join(dir, "Roms"))
	}
	seen := make(map[string]bool, len(roots))
	unique := roots[:0]
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "." || root == "" || seen[root] {
			continue
		}
		seen[root] = true
		unique = append(unique, root)
	}
	return unique
}

// romPluginFolderKey folds a folder name to the key plugins are matched by:
// lower case with the separators people put in console folder names removed.
func romPluginFolderKey(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		switch r {
		case ' ', '-', '_', '.', '(', ')', '[', ']', '/', '\\':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// romPluginSystems turns the switched-on plugins' ROM parsers into the same
// lookup tables the built-in catalogue uses. Extensions a built-in already
// handles, and folder names the built-in aliases already name, are left to the
// catalogue: a plugin must not take over NES because it also reads .nes.
func romPluginSystems(parsers []plugxRomParser) (byExt, byFolder map[string]romSystem) {
	byExt = make(map[string]romSystem)
	byFolder = make(map[string]romSystem)
	for _, p := range parsers {
		system := romSystem{Name: p.Name, Collection: "Plugins"}
		for _, ext := range p.Extensions {
			if ext == "" || romFileExtensions[ext] {
				continue
			}
			if _, taken := byExt[ext]; !taken {
				byExt[ext] = system
			}
		}
		for _, key := range []string{strings.ToLower(p.System), romPluginFolderKey(p.Name)} {
			if key == "" {
				continue
			}
			if _, builtin := romSystemAliases[key]; builtin {
				continue
			}
			if _, taken := byFolder[key]; !taken {
				byFolder[key] = system
			}
		}
	}
	return byExt, byFolder
}

// plugxROMFormats snapshots the registered ROM parsers under the registry lock.
// The scan runs on a background goroutine while a plugin can be switched on or
// off on the main thread.
func plugxROMFormats() (byExt, byFolder map[string]romSystem) {
	plugxRegistry.mu.RLock()
	parsers := append([]plugxRomParser(nil), plugxRegistry.parsers...)
	plugxRegistry.mu.RUnlock()
	return romPluginSystems(parsers)
}

func scanROMLibrary(roots []string) romScanReport {
	report := romScanReport{Roots: append([]string(nil), roots...), Counts: make(map[string]int)}
	pluginByExt, pluginByFolder := plugxROMFormats()
	seen := make(map[string]bool)
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, systemDir := range entries {
			if !systemDir.IsDir() || skipScanEntryName(systemDir.Name()) {
				continue
			}
			system, ok := romSystemAliases[strings.ToLower(systemDir.Name())]
			if !ok {
				system, ok = pluginByFolder[romPluginFolderKey(systemDir.Name())]
			}
			if !ok {
				continue
			}
			_ = filepath.WalkDir(filepath.Join(root, systemDir.Name()), func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil || entry == nil {
					return nil
				}
				// A Mac-copied ROM folder carries __MACOSX and "._" companions beside
				// the real files; they parsed as games with no artwork.
				if skipScanEntryName(entry.Name()) {
					if entry.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				ext := strings.ToLower(filepath.Ext(entry.Name()))
				pluginSystem, pluginExt := pluginByExt[ext]
				if entry.IsDir() || (!romFileExtensions[ext] && !pluginExt) {
					return nil
				}
				clean := filepath.Clean(path)
				if seen[clean] {
					return nil
				}
				seen[clean] = true
				fileSystem := system
				if pluginExt {
					fileSystem = pluginSystem
				}
				report.Games = append(report.Games, scannedROM{Path: clean, System: fileSystem.Name, Collection: fileSystem.Collection})
				report.Counts[fileSystem.Name]++
				return nil
			})
		}
	}
	report.Games, report.Counts = dropSheetParts(report.Games)
	sort.Slice(report.Games, func(i, j int) bool {
		return strings.ToLower(report.Games[i].Path) < strings.ToLower(report.Games[j].Path)
	})
	return report
}

func romArtworkDir() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "game-art")
}

func romArtworkPath(rom scannedROM, root string) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	stem := strings.TrimSuffix(filepath.Base(rom.Path), filepath.Ext(rom.Path))
	if stem == "" || stem == "." || stem == string(filepath.Separator) {
		return ""
	}
	return filepath.Join(root, strings.ReplaceAll(rom.System, " ", "_"), stem+".png")
}

// artworkCoverPresent reports whether a cover file is there right now. A
// library scan uses it to count completeness; a directory at that path is not a
// cover, so it counts as missing.
func artworkCoverPresent(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// artworkMissTTL is how long a "not found" answer is trusted. The thumbnail
// set gains covers over time, so a miss is retried after a month instead of
// being permanent — but a library with 200 unmatched homebrew ROMs does not hit
// the network for all of them on every fetch.
const artworkMissTTL = 30 * 24 * time.Hour

const artworkMissFile = ".missing-art.json"

// artworkMissVersion stamps the not-found cache. It is bumped whenever title
// matching changes: a title that was "not found" under the old lookup deserves
// one more try instead of waiting out the 30-day window.
const artworkMissVersion = 3

// artworkVariantLimit caps how many names one ROM asks the thumbnail server
// for. A library fetch walks every ROM, so a title that generates ten lookups
// would multiply the whole job; eight covers the shapes the matcher builds
// (exact, tags off, subtitle off, article off, then spelling guesses) without
// turning a miss into a sweep. It only applies when the server gives no cover
// list to match against (artwork_match.go).
const artworkVariantLimit = 8

// artworkBaseURL is a variable so the tests can point at an httptest server.
var artworkBaseURL = "https://thumbnails.libretro.com"

// artworkMissStore is the on-disk shape of the not-found cache: title keys
// mapped to the time the lookup failed. It lives beside the images so deleting
// data/game-art also resets the cache.
type artworkMissStore struct {
	Version int                  `json:"version"`
	Missing map[string]time.Time `json:"missing"`
}

func artworkMissPath(dest string) string {
	if strings.TrimSpace(dest) == "" {
		return ""
	}
	return filepath.Join(dest, artworkMissFile)
}

// artworkMissKey identifies a lookup: collection plus the stem sent to the
// thumbnail server. The stem is what the match actually depends on.
func artworkMissKey(collection, stem string) string {
	return strings.TrimSpace(collection) + "/" + strings.TrimSpace(stem)
}

// loadArtworkMisses never fails the caller. A corrupt cache is treated as empty
// and rewritten on the next fetch.
func loadArtworkMisses(dest string) map[string]time.Time {
	out := map[string]time.Time{}
	path := artworkMissPath(dest)
	if path == "" {
		return out
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var store artworkMissStore
	if err := json.Unmarshal(raw, &store); err != nil {
		logWarn("art", "ignoring %s: %v", path, err)
		return out
	}
	if store.Version != artworkMissVersion {
		// Written by an older matcher, so these misses answer a question the
		// downloader no longer asks. Rewriting the file on the next fetch also
		// migrates it.
		logDebug("art", "%s was written by matcher v%d, retrying its titles once", path, store.Version)
		return out
	}
	now := time.Now()
	for key, at := range store.Missing {
		key = strings.TrimSpace(key)
		if key == "" || at.IsZero() || at.After(now) {
			continue
		}
		out[key] = at
	}
	return out
}

// saveArtworkMisses prunes entries that have already expired several times
// over, so the file stays a few kilobytes even after years of scanning.
func saveArtworkMisses(dest string, misses map[string]time.Time) error {
	path := artworkMissPath(dest)
	if path == "" {
		return nil
	}
	now := time.Now()
	kept := make(map[string]time.Time, len(misses))
	for key, at := range misses {
		if key == "" || at.IsZero() || now.Sub(at) > 4*artworkMissTTL {
			continue
		}
		kept[key] = at
	}
	if len(kept) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	payload, err := json.MarshalIndent(artworkMissStore{Version: artworkMissVersion, Missing: kept}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func artworkRecentlyMissing(misses map[string]time.Time, key string, now time.Time, ttl time.Duration) bool {
	at, ok := misses[key]
	if !ok || at.IsZero() {
		return false
	}
	if ttl <= 0 {
		ttl = artworkMissTTL
	}
	age := now.Sub(at)
	return age >= 0 && age < ttl
}

// fetchROMArtworkTracked is the loop behind every artwork download.
//
// progress is called once per ROM with the number handled so far, so the library
// can move a counter while the fetch runs instead of waiting for the end. A
// cancelled ctx stops between games and returns stopped=true: the covers already
// written stay on the card, which is what makes the job resumable — running it
// again scans the disk and only asks about what is still missing.
func fetchROMArtworkTracked(ctx context.Context, roms []scannedROM, client *http.Client, baseURL, dest string, misses map[string]time.Time, now time.Time, ttl time.Duration, progress func(done int)) (fetched, skipped, missing int, stopped bool) {
	if client == nil {
		client = artworkHTTPClient()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	// classify handles one ROM and reports which bucket it landed in. Keeping
	// it separate is what lets the loop count progress for skipped games too.
	const (
		artFetched = iota
		artSkipped
		artMissing
	)
	classify := func(rom scannedROM) int {
		path := romArtworkPath(rom, dest)
		if path == "" {
			return artMissing
		}
		// A cover another front end left on the card (Imgs, .res) is shown
		// as it is; asking the server would only replace the user's choice.
		if artworkCoverPresent(path) || romLocalCoverPath(rom) != "" {
			return artSkipped
		}
		stem := strings.TrimSuffix(filepath.Base(rom.Path), filepath.Ext(rom.Path))
		if stem == "" {
			return artMissing
		}
		key := artworkMissKey(rom.Collection, stem)
		if misses != nil && artworkRecentlyMissing(misses, key, now, ttl) {
			return artMissing
		}
		image, err := fetchROMArtworkFor(ctx, client, baseURL, rom, stem)
		if err != nil {
			// A 404 is a real answer worth remembering. A timeout or a dropped
			// connection is not: retrying that next time is the whole point.
			if misses != nil && errors.Is(err, os.ErrNotExist) {
				misses[key] = now
			}
			return artMissing
		}
		// Through a temporary file: a cover cut short by a power cut would
		// otherwise count as present for ever.
		if err := gmetaWriteCache(path, image); err != nil {
			return artMissing
		}
		if misses != nil {
			delete(misses, key)
		}
		return artFetched
	}
	for i, rom := range roms {
		if ctx != nil && ctx.Err() != nil {
			return fetched, skipped, missing, true
		}
		switch classify(rom) {
		case artFetched:
			fetched++
		case artSkipped:
			skipped++
		default:
			missing++
		}
		if progress != nil {
			progress(i + 1)
		}
	}
	return fetched, skipped, missing, false
}

// artworkTitleVariants lists the names to try for one ROM stem, best first.
//
// The thumbnail set names a file exactly as the reference title reads, and the
// ROM in the folder often does not:
//
//   - "(USA)", "(Rev 1)", "[!]" and friends are dump tags, not part of a title;
//   - a reference title carries the subtitle ("Zelda II - The Adventure of
//     Link") while a trimmed ROM name may only say "Zelda II";
//   - a reference title may open with an article the local name drops, or the
//     other way round;
//   - the server writes & * / : ` < > ? \ | as "_" (artworkServerName).
//
// Ordering matters more than reach. Every step below is a guess, so the exact
// name is asked first and the guesses only run after it 404s: a truncated title
// can land on a different game's cover, and that is only acceptable as a last
// resort. The list is capped so one odd title cannot turn a library fetch into a
// sweep.
func artworkTitleVariants(title string) []string {
	base := strings.TrimSpace(title)
	if base == "" {
		return nil
	}

	// Build the names in widening order of guesswork, then write each the way
	// the server names its files. (An earlier version also asked for every name
	// with its spaces as underscores; the server has no such files, and those
	// guesses used up half of the limit.)
	spaced := []string{base}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		for _, have := range spaced {
			if have == name {
				return
			}
		}
		spaced = append(spaced, name)
	}

	tagged := stripArtworkTagGroups(base)
	add(tagged)
	add(artworkSubtitleCut(tagged))
	add(artworkSubtitleCut(base))
	// Articles last: dropping "The" is a bigger change to the name than
	// dropping a dump tag, so it is asked for last.
	for _, name := range append([]string(nil), spaced...) {
		add(trimArtworkArticle(name))
	}

	// Spelling guesses come after every structural one: a title that only
	// differs by accent, numeral or punctuation still names the same game, but
	// it is a guess, and a truncated or reordered title is a worse one.
	for _, name := range append([]string(nil), spaced...) {
		if alias, ok := artworkAliasFor(name); ok {
			add(alias)
		}
		for _, variant := range artworkSpellingVariants(name) {
			add(variant)
		}
	}

	out := make([]string, 0, len(spaced)*2)
	seen := make(map[string]bool, len(spaced)*2)
	push := func(name string) {
		if name == "" || seen[name] || len(out) >= artworkVariantLimit {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, name := range spaced {
		push(artworkServerName(name))
	}
	return out
}

// ── Title normalisation ──
//
// The reference set spells a title its own way, and a ROM on the card spells it
// the user's. Before this table the only guesses were "drop the tags", "cut the
// subtitle" and "drop the article", so a title that differed by an accent, a
// Roman numeral, "&" instead of "and", or a dash where the reference has a
// colon was recorded as not found for a month.
//
// These guesses are appended after the structural ones and are all single-step
// rewrites, so a wrong guess asks for one extra file that 404s; it never picks a
// different game's cover unless that game happens to carry the exact rewritten
// name.

// artworkAliasTitles is the manual half: a normalised local name mapped to the
// reference title when the two differ by more than spelling. Keys go through
// artworkNormalizeTitle, so "Zelda II (USA)" and "zelda ii" share the entry.
// Keep entries factual — one wrong alias costs a request and can pick a cover
// for another game.
var artworkAliasTitles = map[string]string{
	// The reference set keeps the subtitle; a ROM trimmed to "Zelda II" cannot
	// grow one back by rewriting, which is why this needs a table at all.
	"zelda ii": "Zelda II - The Adventure of Link",
	"zelda 2":  "Zelda II - The Adventure of Link",
}

// artworkAliasFor looks a name up in the alias table, ignoring dump tags,
// punctuation and case.
func artworkAliasFor(name string) (string, bool) {
	alias, ok := artworkAliasTitles[artworkNormalizeTitle(name)]
	return alias, ok && alias != ""
}

// artworkNormalizeTitle reduces a title to a comparable key: no dump tags, no
// accents, lower case, and punctuation folded to spaces (apostrophes dropped).
func artworkNormalizeTitle(s string) string {
	s = stripArtworkTagGroups(s)
	s = artworkFoldDiacritics(s)
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\'', '\u2019', '\u2018':
			// Dropped, not spaced: "Kirby's" and "Kirbys" are one title.
			continue
		case '-', '_', ':', '.', ',', '!', '?':
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(strings.ToLower(b.String())), " ")
}

// artworkDiacritics folds the accented letters the reference set uses into the
// plain letters a ROM folder usually has. The reverse (adding an accent back) is
// not a rewrite a program can make safely, so a title that needs its accent
// restored belongs in the alias table instead.
var artworkDiacritics = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a",
	'ç': "c", 'è': "e", 'é': "e", 'ê': "e", 'ë': "e",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ñ': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ý': "y", 'ÿ': "y",
	'À': "A", 'Á': "A", 'Â': "A", 'Ã': "A", 'Ä': "A", 'Å': "A",
	'Ç': "C", 'È': "E", 'É': "E", 'Ê': "E", 'Ë': "E",
	'Ì': "I", 'Í': "I", 'Î': "I", 'Ï': "I", 'Ñ': "N",
	'Ò': "O", 'Ó': "O", 'Ô': "O", 'Õ': "O", 'Ö': "O", 'Ø': "O",
	'Ù': "U", 'Ú': "U", 'Û': "U", 'Ü': "U", 'Ý': "Y",
}

func artworkFoldDiacritics(s string) string {
	needsFold := false
	for _, r := range s {
		if _, ok := artworkDiacritics[r]; ok {
			needsFold = true
			break
		}
	}
	if !needsFold {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if folded, ok := artworkDiacritics[r]; ok {
			b.WriteString(folded)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// artworkRomanValues covers the numerals a game title realistically carries.
// "I" is included even though "I, Robot" would be rewritten: the guess costs a
// 404 and is only asked for after the real name.
var artworkRomanValues = map[string]int{
	"I": 1, "II": 2, "III": 3, "IV": 4, "V": 5, "VI": 6, "VII": 7,
	"VIII": 8, "IX": 9, "X": 10, "XI": 11, "XII": 12, "XIII": 13,
	"XIV": 14, "XV": 15, "XVI": 16, "XVII": 17, "XVIII": 18,
	"XIX": 19, "XX": 20,
}

var artworkArabicNumerals = map[int]string{
	2: "II", 3: "III", 4: "IV", 5: "V", 6: "VI", 7: "VII", 8: "VIII",
	9: "IX", 10: "X", 11: "XI", 12: "XII", 13: "XIII", 14: "XIV",
	15: "XV", 16: "XVI", 17: "XVII", 18: "XVIII", 19: "XIX", 20: "XX",
}

// artworkSwapNumerals rewrites a standalone Roman numeral as its number and a
// standalone number 2–20 as its Roman numeral: "Zelda II" ↔ "Zelda 2", the
// documented mismatch between a trimmed folder name and the reference title.
func artworkSwapNumerals(name string) string {
	words := strings.Fields(name)
	changed := false
	for i, word := range words {
		if value, ok := artworkRomanValues[strings.ToUpper(word)]; ok {
			words[i] = strconv.Itoa(value)
			changed = true
			continue
		}
		if n, err := strconv.Atoi(word); err == nil {
			if roman, ok := artworkArabicNumerals[n]; ok {
				words[i] = roman
				changed = true
			}
		}
	}
	if !changed {
		return name
	}
	return strings.Join(words, " ")
}

// artworkSwapAnd rewrites "&" as "and" and a standalone "and" as "&", the two
// spellings the reference set and a ROM folder disagree about.
func artworkSwapAnd(name string) string {
	if strings.Contains(name, "&") {
		return strings.Join(strings.Fields(strings.ReplaceAll(name, "&", "and")), " ")
	}
	words := strings.Fields(name)
	changed := false
	for i, word := range words {
		if strings.EqualFold(word, "and") {
			words[i] = "&"
			changed = true
		}
	}
	if !changed {
		return name
	}
	return strings.Join(words, " ")
}

// artworkSwapSubtitleSeparator rewrites the first subtitle separator: the
// reference set writes "Series - Subtitle" while a local name often says
// "Series: Subtitle" (and the other way round).
func artworkSwapSubtitleSeparator(name string) string {
	if i := strings.Index(name, " - "); i > 0 {
		return name[:i] + ": " + name[i+3:]
	}
	if i := strings.Index(name, ": "); i > 0 {
		return name[:i] + " - " + name[i+2:]
	}
	return name
}

// artworkSpellingVariants lists the same title written differently. Each entry
// is one rewrite of the whole name, not a chain, so the list stays small and
// every guess is attributable.
func artworkSpellingVariants(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	var out []string
	add := func(v string) {
		v = strings.Join(strings.Fields(v), " ")
		if v == "" || v == name {
			return
		}
		for _, have := range out {
			if have == v {
				return
			}
		}
		out = append(out, v)
	}
	add(artworkFoldDiacritics(name))
	add(artworkSwapNumerals(name))
	add(artworkSwapAnd(name))
	add(artworkSwapSubtitleSeparator(name))
	if trimmed := strings.TrimRight(name, " .,:;!"); trimmed != name {
		add(trimmed)
	}
	return out
}

// stripArtworkTagGroups removes bracketed dump tags: "(USA)", "(Rev 1)",
// "[!]". Nested brackets are dropped whole instead of leaving a stray half
// behind, and the surrounding spaces are tidied so the result is a real title.
func stripArtworkTagGroups(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(', '[':
			depth++
			continue
		case ')', ']':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth == 0 {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// artworkSubtitleCut keeps the part before the first " - ", the separator the
// reference sets use between a series name and its subtitle.
func artworkSubtitleCut(s string) string {
	if i := strings.Index(s, " - "); i > 0 {
		if head := strings.TrimSpace(s[:i]); head != "" {
			return head
		}
	}
	return s
}

// trimArtworkArticle drops a leading "The", "A" or "An". A name that is only
// the article is left alone rather than becoming empty.
func trimArtworkArticle(s string) string {
	lower := strings.ToLower(strings.TrimSpace(s))
	for _, article := range []string{"the ", "an ", "a "} {
		if strings.HasPrefix(lower, article) {
			rest := strings.TrimSpace(s[len(article):])
			if rest != "" {
				return rest
			}
		}
	}
	return s
}

var artworkFetchState = struct {
	sync.Mutex
	running  bool
	stopping bool
	total    int
	done     int
	fetched  int
	skipped  int
	missing  int
	stopped  bool
	finished uint64             // bumped on every completion; the library redraws art when it moves
	cancel   context.CancelFunc // stops the loop between two games
}{}

func startROMArtworkFetch(roms []scannedROM) bool {
	return startArtworkFetch(roms, nil, romArtworkDir(), true)
}

// startLibraryArtworkFetch downloads the covers for a scanned list and keeps the
// not-found cache, so pressing the action again only asks about new titles.
func startLibraryArtworkFetch(roms []scannedROM) bool {
	return startLibraryArtworkFetchAs(roms, true)
}

// startLibraryArtworkFetchAs is startLibraryArtworkFetch for a caller that says
// whether the user asked for it: the automatic pass (boxart_auto.go) does not
// count toward the box-art trophies.
func startLibraryArtworkFetchAs(roms []scannedROM, byUser bool) bool {
	dest := romArtworkDir()
	if strings.TrimSpace(dest) == "" {
		return false
	}
	return startArtworkFetch(roms, loadArtworkMisses(dest), dest, byUser)
}

func startArtworkFetch(roms []scannedROM, misses map[string]time.Time, dest string, byUser bool) bool {
	artworkFetchState.Lock()
	if artworkFetchState.running {
		artworkFetchState.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	artworkFetchState.running = true
	artworkFetchState.stopping = false
	artworkFetchState.stopped = false
	artworkFetchState.total = len(roms)
	artworkFetchState.done = 0
	artworkFetchState.fetched = 0
	artworkFetchState.skipped = 0
	artworkFetchState.missing = 0
	artworkFetchState.cancel = cancel
	artworkFetchState.Unlock()
	go func() {
		fetched, skipped, missing, stopped := fetchROMArtworkTracked(ctx, roms, nil,
			artworkBaseURL, dest, misses, time.Now(), artworkMissTTL, func(done int) {
				artworkFetchState.Lock()
				artworkFetchState.done = done
				artworkFetchState.Unlock()
			})
		// Covers written to the card: the library's fetch, onboarding's and the
		// automatic pass (boxart_auto.go) all run through here; only the ones
		// the user asked for count toward the box-art trophies.
		if byUser {
			trophyEventN(tkBoxArt, int64(fetched))
		}
		cancel()
		if misses != nil {
			if err := saveArtworkMisses(dest, misses); err != nil {
				logWarn("art", "cannot save the not-found cache: %v", err)
			}
		}
		artworkFetchState.Lock()
		artworkFetchState.running = false
		artworkFetchState.stopping = false
		artworkFetchState.stopped = stopped
		artworkFetchState.fetched = fetched
		artworkFetchState.skipped = skipped
		artworkFetchState.missing = missing
		artworkFetchState.cancel = nil
		artworkFetchState.finished++
		artworkFetchState.Unlock()
	}()
	return true
}

// stopArtworkFetch asks a running fetch to stop after the cover it is on. It
// returns false when nothing is running, so the caller does not promise a stop
// that never happens.
func stopArtworkFetch() bool {
	artworkFetchState.Lock()
	defer artworkFetchState.Unlock()
	if !artworkFetchState.running || artworkFetchState.cancel == nil || artworkFetchState.stopping {
		return false
	}
	artworkFetchState.stopping = true
	artworkFetchState.cancel()
	return true
}

// artworkFetchProgress returns how far the running fetch has got. The pair is
// zero when nothing has run this session.
func artworkFetchProgress() (done, total int, running bool) {
	artworkFetchState.Lock()
	defer artworkFetchState.Unlock()
	return artworkFetchState.done, artworkFetchState.total, artworkFetchState.running
}

func artworkFetchRunning() bool {
	artworkFetchState.Lock()
	defer artworkFetchState.Unlock()
	return artworkFetchState.running
}

// artworkFetchGeneration changes once per completed fetch, on any thread, so
// the renderer can drop its texture cache without touching it from here.
func artworkFetchGeneration() uint64 {
	artworkFetchState.Lock()
	defer artworkFetchState.Unlock()
	return artworkFetchState.finished
}

// romsMissingArtwork filters a scan down to the games with no local image:
// neither a downloaded cover nor one another front end left (romLocalCoverPath).
func romsMissingArtwork(roms []scannedROM, artRoot string) []scannedROM {
	missing := make([]scannedROM, 0, len(roms))
	for _, rom := range roms {
		path := romArtworkPath(rom, artRoot)
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if romLocalCoverPath(rom) != "" {
			continue
		}
		missing = append(missing, rom)
	}
	return missing
}

func artworkFetchSummary() string {
	artworkFetchState.Lock()
	defer artworkFetchState.Unlock()
	if artworkFetchState.running {
		line := "Fetching covers: " + itoa(artworkFetchState.done) + " of " + itoa(artworkFetchState.total)
		if artworkFetchState.stopping {
			return line + " — stopping after this one…"
		}
		return line + ". Press L1 / F to stop."
	}
	if artworkFetchState.total == 0 {
		return "Scan ROM folders first to find games."
	}
	if artworkFetchState.stopped {
		return "Stopped after " + itoa(artworkFetchState.done) + " of " + itoa(artworkFetchState.total) +
			": " + itoa(artworkFetchState.fetched) + " saved. Press Fill Gaps to resume."
	}
	return pluralizeCount(artworkFetchState.fetched, "image", "images") + " saved; " + itoa(artworkFetchState.skipped) + " already present; " + itoa(artworkFetchState.missing) + " not found."
}
