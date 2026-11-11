package main

// themecatalog.go — the community theme repository.
//
// The builder site publishes gallery/community.json: one index of complete theme
// files (and layouts for the web gallery). JukaHub fetches it in the background,
// keeps a cached copy beside its other data so the library still browses
// offline, and installs an entry by writing themes/<id>.json — exactly the
// format a hand-made theme file uses — then applying it. Nothing here can
// rewrite a shipped config or a built-in palette.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	themeCatalogCacheName = "theme-catalog.json"
	themeCatalogMaxBytes  = 512 << 10
	// themeCatalogTimeout bounds the fetch: the library opens on a press, and a
	// stuck socket must not hold the panel's loading line forever.
	themeCatalogTimeout = 8 * time.Second
)

// themeCatalogURL is a variable so a test (or a fork) can point the library at
// another index without rebuilding the app name into the code.
var themeCatalogURL = "https://generate.jukalang.com/gallery/community.json"

// communityTheme is one entry of the index. The theme itself is the same
// themeFile the loader reads from themes/*.json, so validation is shared and a
// contribution cannot describe a colour the app would refuse on disk.
type communityTheme struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Author      string    `json:"author"`
	Description string    `json:"description"`
	Tags        []string  `json:"tags"`
	Theme       themeFile `json:"theme"`
}

// communityCatalogFile is the part of gallery/community.json JukaHub reads.
// layouts are for the web gallery and are ignored here.
type communityCatalogFile struct {
	Version int              `json:"version"`
	Themes  []communityTheme `json:"themes"`
}

var themeCatalogState struct {
	mu      sync.Mutex
	entries []communityTheme
	err     string
	loaded  bool
	loading bool
}

var communityThemeIDPattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}$`) })

// communityThemeSlug normalises the id an entry is installed under. The id is a
// file name, so it stays lowercase, short and free of path separators; an entry
// whose id cannot be one is skipped rather than written somewhere surprising.
func communityThemeSlug(raw string) (string, error) {
	id := strings.ToLower(strings.TrimSpace(raw))
	id = strings.Join(strings.Fields(id), "-")
	if !communityThemeIDPattern().MatchString(id) {
		return "", fmt.Errorf("id %q is not a slug (lowercase letters, digits and dashes)", raw)
	}
	return id, nil
}

// parseCommunityCatalog validates an index. Bad entries are reported and
// skipped so one contributor's typo cannot make the whole library unreadable;
// err is reserved for a document that is not the index at all.
func parseCommunityCatalog(data []byte) (entries []communityTheme, skipped []string, err error) {
	var file communityCatalogFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, nil, fmt.Errorf("gallery index: %w", err)
	}
	seen := make(map[string]bool, len(file.Themes))
	for i, entry := range file.Themes {
		id, idErr := communityThemeSlug(entry.ID)
		if idErr != nil {
			skipped = append(skipped, fmt.Sprintf("entry %d: %v", i+1, idErr))
			continue
		}
		if seen[id] {
			skipped = append(skipped, id+": duplicate id")
			continue
		}
		if strings.TrimSpace(entry.Theme.Name) == "" {
			entry.Theme.Name = strings.TrimSpace(entry.Name)
		}
		if _, themeErr := entry.Theme.toPreset(); themeErr != nil {
			skipped = append(skipped, id+": "+themeErr.Error())
			continue
		}
		entry.ID = id
		seen[id] = true
		entries = append(entries, entry)
	}
	return entries, skipped, nil
}

// fetchCommunityCatalog downloads and validates the index.
func fetchCommunityCatalog(client *http.Client, url string) ([]communityTheme, []string, error) {
	if strings.TrimSpace(url) == "" {
		return nil, nil, errors.New("no gallery address is configured")
	}
	if client == nil {
		client = &http.Client{Timeout: themeCatalogTimeout}
	}
	resp, err := client.Get(url)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("gallery replied %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, themeCatalogMaxBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(body)) > themeCatalogMaxBytes {
		return nil, nil, errors.New("gallery index is too large")
	}
	return parseCommunityCatalog(body)
}

// communityThemeEntries is the panel's snapshot: the entries, the reason there
// are none, and whether a fetch is still running.
func communityThemeEntries() ([]communityTheme, string, bool) {
	themeCatalogState.mu.Lock()
	defer themeCatalogState.mu.Unlock()
	entries := make([]communityTheme, len(themeCatalogState.entries))
	copy(entries, themeCatalogState.entries)
	return entries, themeCatalogState.err, themeCatalogState.loading
}

// startThemeCatalogLoad loads the cached index once and then refreshes it in
// the background. force asks for a fresh fetch even when one already finished;
// a concurrent call is a no-op, so pressing Refresh twice does not double the
// traffic.
func startThemeCatalogLoad(force bool) {
	themeCatalogState.mu.Lock()
	if themeCatalogState.loading || (themeCatalogState.loaded && !force) {
		themeCatalogState.mu.Unlock()
		return
	}
	loadingCached := !themeCatalogState.loaded
	themeCatalogState.loading = true
	themeCatalogState.mu.Unlock()

	if loadingCached {
		if cached, err := readThemeCatalogCache(); err == nil && len(cached) > 0 {
			themeCatalogState.mu.Lock()
			themeCatalogState.entries = cached
			themeCatalogState.mu.Unlock()
		}
	}

	go func() {
		entries, skipped, err := fetchCommunityCatalog(nil, themeCatalogURL)
		themeCatalogState.mu.Lock()
		defer themeCatalogState.mu.Unlock()
		themeCatalogState.loading = false
		themeCatalogState.loaded = true
		if err != nil {
			themeCatalogState.err = err.Error()
			return
		}
		themeCatalogState.entries = entries
		themeCatalogState.err = ""
		if len(skipped) > 0 {
			logPatch("community themes: %d entries skipped: %s", len(skipped), strings.Join(skipped, "; "))
		}
		if len(entries) > 0 {
			writeThemeCatalogCache(entries)
		}
	}()
}

func themeCatalogCachePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, themeCatalogCacheName)
}

func readThemeCatalogCache() ([]communityTheme, error) {
	path := themeCatalogCachePath()
	if path == "" {
		return nil, errors.New("no data directory")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	entries, _, err := parseCommunityCatalog(data)
	return entries, err
}

func writeThemeCatalogCache(entries []communityTheme) {
	path := themeCatalogCachePath()
	if path == "" {
		return
	}
	data, err := json.Marshal(communityCatalogFile{Version: 1, Themes: entries})
	if err != nil {
		return
	}
	if err := AtomicWrite(path, data, 0o644); err != nil {
		logPatch("community themes: cache write failed: %v", err)
	}
}

// forgetThemePreset drops a registered preset so the loader can register the
// same id again after an install. themePresetOrder is rebuilt rather than
// appended, or a reinstall would list the theme twice.
func forgetThemePreset(id string) {
	delete(themePresets, id)
	kept := themePresetOrder[:0]
	for _, name := range themePresetOrder {
		if name != id {
			kept = append(kept, name)
		}
	}
	themePresetOrder = kept
}

// installCommunityTheme writes one index entry to themes/<id>.json, registers
// it and applies it. The entry is re-validated here: the index came off the
// network, and a theme is only written when the loader would accept the file.
func installCommunityTheme(config *Config, entry communityTheme) error {
	if config == nil {
		return errors.New("no configuration is loaded")
	}
	id, err := communityThemeSlug(entry.ID)
	if err != nil {
		return err
	}
	file := entry.Theme
	if strings.TrimSpace(file.Name) == "" {
		file.Name = strings.TrimSpace(entry.Name)
	}
	if _, err := file.toPreset(); err != nil {
		return err
	}
	if isBuiltinThemeID(id) {
		// Writing themes/<id>.json would shadow a shipped palette for this
		// session and for every config that names it; refuse instead. A theme
		// installed on an earlier run is not in the compiled set, so it can
		// still be updated.
		return fmt.Errorf("%q is a built-in theme", id)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(themeDir, id+".json")
	if err := AtomicWrite(path, data, 0o644); err != nil {
		return err
	}
	forgetThemePreset(id)
	if _, failed := LoadThemeFiles(themeDir); failed[path] != nil {
		return failed[path]
	}
	if _, ok := themePresets[id]; !ok {
		return errors.New("the theme file did not load")
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = make(map[string]interface{})
	}
	applyThemePreset(config, id, true)
	// A theme installed from the gallery is a pick like any other: with light &
	// dark by time of day on it becomes this part of the day's theme
	// (themeschedule.go) instead of being undone at the next switch.
	themeScheduleNoteManualPick(config, id)
	return nil
}

// isBuiltinThemeID reports whether id belongs to a compiled palette. Startup
// captures the set before any theme file loads; tests (and an install that runs
// before a load) capture it on demand.
func isBuiltinThemeID(id string) bool {
	captureBuiltinThemeIDs()
	return builtinThemeIDs[id]
}
