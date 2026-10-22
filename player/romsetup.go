package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
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
	"playstation portable":        {"PlayStation Portable", "Sony - PlayStation Portable"},
	"nds":                         {"Nintendo DS", "Nintendo - Nintendo DS"},
	"nintendo ds":                 {"Nintendo DS", "Nintendo - Nintendo DS"},
	"sat":                         {"Saturn", "Sega - Saturn"},
	"saturn":                      {"Saturn", "Sega - Saturn"},
	"sega - saturn":               {"Saturn", "Sega - Saturn"},
	"dc":                          {"Dreamcast", "Sega - Dreamcast"},
	"dreamcast":                   {"Dreamcast", "Sega - Dreamcast"},
	"sega - dreamcast":            {"Dreamcast", "Sega - Dreamcast"},
}

var romFileExtensions = map[string]bool{
	".3ds": true, ".bin": true, ".chd": true, ".ciso": true, ".cue": true,
	".gb": true, ".gba": true, ".gbc": true, ".gcm": true, ".gcz": true,
	".7z": true, ".gen": true, ".iso": true, ".md": true, ".nds": true, ".nes": true,
	".n64": true, ".pbp": true, ".pce": true, ".rom": true, ".sfc": true,
	".smc": true, ".z64": true, ".zip": true, ".v64": true, ".ndd": true,
	".cdi": true, ".gdi": true,
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

func scanROMLibrary(roots []string) romScanReport {
	report := romScanReport{Roots: append([]string(nil), roots...), Counts: make(map[string]int)}
	seen := make(map[string]bool)
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, systemDir := range entries {
			if !systemDir.IsDir() {
				continue
			}
			system, ok := romSystemAliases[strings.ToLower(systemDir.Name())]
			if !ok {
				continue
			}
			_ = filepath.WalkDir(filepath.Join(root, systemDir.Name()), func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil || entry == nil || entry.IsDir() {
					return nil
				}
				if !romFileExtensions[strings.ToLower(filepath.Ext(entry.Name()))] {
					return nil
				}
				clean := filepath.Clean(path)
				if seen[clean] {
					return nil
				}
				seen[clean] = true
				report.Games = append(report.Games, scannedROM{Path: clean, System: system.Name, Collection: system.Collection})
				report.Counts[system.Name]++
				return nil
			})
		}
	}
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
		log.Printf("[art] ignoring %s: %v", path, err)
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
	payload, err := json.MarshalIndent(artworkMissStore{Version: 1, Missing: kept}, "", "  ")
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

// fetchROMArtworkWithMisses is the shared downloader. A nil misses map keeps
// the old behaviour (always retry); a map both skips recent misses and records
// new ones, which is what the library action uses.
func fetchROMArtworkWithMisses(ctx context.Context, roms []scannedROM, client *http.Client, baseURL, dest string, misses map[string]time.Time, now time.Time, ttl time.Duration) (fetched, skipped, missing int) {
	fetched, skipped, missing, _ = fetchROMArtworkTracked(ctx, roms, client, baseURL, dest, misses, now, ttl, nil)
	return fetched, skipped, missing
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
		client = &http.Client{Timeout: 8 * time.Second}
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
		if artworkCoverPresent(path) {
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
		image, err := fetchROMArtworkImage(ctx, client, baseURL, rom.Collection, stem)
		if err != nil {
			// A 404 is a real answer worth remembering. A timeout or a dropped
			// connection is not: retrying that next time is the whole point.
			if misses != nil && errors.Is(err, os.ErrNotExist) {
				misses[key] = now
			}
			return artMissing
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return artMissing
		}
		if err := os.WriteFile(path, image, 0o644); err != nil {
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

func fetchROMArtwork(ctx context.Context, roms []scannedROM, client *http.Client, baseURL, dest string) (fetched, skipped, missing int) {
	return fetchROMArtworkWithMisses(ctx, roms, client, baseURL, dest, nil, time.Now(), artworkMissTTL)
}

func fetchROMArtworkImage(ctx context.Context, client *http.Client, baseURL, collection, title string) ([]byte, error) {
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	base := strings.TrimRight(baseURL, "/")
	if base == "" || collection == "" || title == "" {
		return nil, os.ErrNotExist
	}
	variants := []string{title, strings.ReplaceAll(title, " ", "_")}
	for i, variant := range variants {
		if i > 0 && variant == variants[0] {
			continue
		}
		u := base + "/" + url.PathEscape(collection) + "/Named_Boxarts/" + url.PathEscape(variant) + ".png"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode == http.StatusNotFound {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			// A 5xx or a throttling response is not a missing cover; the caller
			// must not cache it as "not found" and give up for a month.
			return nil, fmt.Errorf("thumbnail server returned %s", resp.Status)
		}
		if len(data) < 8 || len(data) > 8<<20 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
			continue
		}
		return data, nil
	}
	return nil, os.ErrNotExist
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
	return startArtworkFetch(roms, nil, romArtworkDir())
}

// startLibraryArtworkFetch downloads the covers for a scanned list and keeps the
// not-found cache, so pressing the action again only asks about new titles.
func startLibraryArtworkFetch(roms []scannedROM) bool {
	dest := romArtworkDir()
	if strings.TrimSpace(dest) == "" {
		return false
	}
	return startArtworkFetch(roms, loadArtworkMisses(dest), dest)
}

func startArtworkFetch(roms []scannedROM, misses map[string]time.Time, dest string) bool {
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
		cancel()
		if misses != nil {
			if err := saveArtworkMisses(dest, misses); err != nil {
				log.Printf("[art] cannot save the not-found cache: %v", err)
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

// romsMissingArtwork filters a scan down to the games with no local image.
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
	return "Saved " + itoa(artworkFetchState.fetched) + " image(s); " + itoa(artworkFetchState.skipped) + " already present; " + itoa(artworkFetchState.missing) + " not found."
}
