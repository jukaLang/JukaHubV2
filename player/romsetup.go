package main

import (
	"context"
	"io"
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
	".smc": true, ".z64": true, ".zip": true,
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

func fetchROMArtwork(ctx context.Context, roms []scannedROM, client *http.Client, baseURL, dest string) (fetched, skipped, missing int) {
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	for _, rom := range roms {
		path := romArtworkPath(rom, dest)
		if path == "" {
			missing++
			continue
		}
		if _, err := os.Stat(path); err == nil {
			skipped++
			continue
		}
		stem := strings.TrimSuffix(filepath.Base(rom.Path), filepath.Ext(rom.Path))
		if stem == "" {
			missing++
			continue
		}
		image, err := fetchROMArtworkImage(ctx, client, baseURL, rom.Collection, stem)
		if err != nil {
			missing++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			missing++
			continue
		}
		if err := os.WriteFile(path, image, 0o644); err != nil {
			missing++
			continue
		}
		fetched++
	}
	return fetched, skipped, missing
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
		if resp.StatusCode != http.StatusOK || len(data) < 8 || len(data) > 8<<20 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
			continue
		}
		return data, nil
	}
	return nil, os.ErrNotExist
}

var artworkFetchState = struct {
	sync.Mutex
	running bool
	total   int
	fetched int
	skipped int
	missing int
}{}

func startROMArtworkFetch(roms []scannedROM) bool {
	artworkFetchState.Lock()
	if artworkFetchState.running {
		artworkFetchState.Unlock()
		return false
	}
	artworkFetchState.running = true
	artworkFetchState.total = len(roms)
	artworkFetchState.fetched = 0
	artworkFetchState.skipped = 0
	artworkFetchState.missing = 0
	artworkFetchState.Unlock()
	go func() {
		fetched, skipped, missing := fetchROMArtwork(context.Background(), roms, nil,
			"https://thumbnails.libretro.com", romArtworkDir())
		artworkFetchState.Lock()
		artworkFetchState.running = false
		artworkFetchState.fetched = fetched
		artworkFetchState.skipped = skipped
		artworkFetchState.missing = missing
		artworkFetchState.Unlock()
	}()
	return true
}

func artworkFetchSummary() string {
	artworkFetchState.Lock()
	defer artworkFetchState.Unlock()
	if artworkFetchState.running {
		return "Artwork fetch is running for " + itoa(artworkFetchState.total) + " scanned game(s)."
	}
	if artworkFetchState.total == 0 {
		return "Scan ROM folders first to find games."
	}
	return "Saved " + itoa(artworkFetchState.fetched) + " image(s); " + itoa(artworkFetchState.skipped) + " already present; " + itoa(artworkFetchState.missing) + " not found."
}
