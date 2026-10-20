package main

// gameprofiles.go — versioned game profiles for Windows games run through
// Box64 or Wine, plus native and script launch targets.
//
// Profiles are plain JSON so they can be reviewed, diffed and shared. The
// catalog lives in the JukaHub data folder:
//
//   game-profiles/profiles.json   shared database
//   game-profiles/import/*.json   community inbox (loaded automatically)
//   game-profiles/local/*.json    local overrides (win over import/database)
//   game-profiles/export/*.json   files written by X in the panel
//
// Relative game folders are resolved against the platform's game roots (the
// SD card on TrimUI, a few user folders on desktop). Every resolved path is
// checked again after symlink evaluation, so an imported profile cannot use
// ".." or a link to launch something outside those roots.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	gameProfileSchemaVersion = 1

	gameRuntimeNative = "native"
	gameRuntimeBox64  = "box64"
	gameRuntimeWine   = "wine"
	gameRuntimeScript = "script"

	gameProfilesEntryName = "Game Profiles"

	gameProfileMaxFileBytes = 1 << 20 // 1 MiB per profile file
	gameProfileMaxProfiles  = 500
	gameProfileMaxArgs      = 128
	gameProfileMaxEnv       = 64
)

var gameProfileIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var gameProfileEnvKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// GameProfile is one entry in the catalog. SchemaVersion is required in every
// file so an older build refuses a newer profile instead of misreading it.
type GameProfile struct {
	SchemaVersion  int               `json:"schemaVersion"`
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	Runtime        string            `json:"runtime"`
	Dir            string            `json:"dir"`
	Executable     string            `json:"executable"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Controller     string            `json:"controller,omitempty"`
	TestedFirmware string            `json:"testedFirmware,omitempty"`
	Notes          string            `json:"notes,omitempty"`
}

// gameProfileCatalogFile is the bundle form used by profiles.json and by
// multi-game community exports.
type gameProfileCatalogFile struct {
	SchemaVersion int           `json:"schemaVersion"`
	Profiles      []GameProfile `json:"profiles"`
}

// gameProfileEntry is a loaded profile plus its resolved state. Issue is set
// when the profile is valid JSON but cannot be launched right now (missing
// game folder, executable, Wine/Box64, or a path outside the allowed roots).
type gameProfileEntry struct {
	Profile    GameProfile
	Source     string
	GameDir    string
	Executable string
	Issue      string
}

func (e gameProfileEntry) ready() bool { return e.Issue == "" }

// gameProfileCache keeps the catalog between frames. The panel rebuilds it
// whenever it opens, and Y forces a rebuild, so dropping a JSON file over FTP
// does not require restarting JukaHub.
var gameProfileCache struct {
	mu      sync.Mutex
	built   bool
	entries []gameProfileEntry
	issues  []string
}

var (
	gameProfilesDirOverride  string
	gameProfileRootsOverride []string
)

func invalidateGameProfileCache() {
	gameProfileCache.mu.Lock()
	gameProfileCache.built = false
	gameProfileCache.entries = nil
	gameProfileCache.issues = nil
	gameProfileCache.mu.Unlock()
}

// gameProfilesDir returns the data-folder directory holding the catalog.
func gameProfilesDir() string {
	if gameProfilesDirOverride != "" {
		return gameProfilesDirOverride
	}
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return filepath.Join("data", "game-profiles")
	}
	return filepath.Join(dir, "game-profiles")
}

func gameProfilesImportDir() string { return filepath.Join(gameProfilesDir(), "import") }
func gameProfilesLocalDir() string  { return filepath.Join(gameProfilesDir(), "local") }
func gameProfilesExportDir() string { return filepath.Join(gameProfilesDir(), "export") }
func gameProfilesLogsDir() string   { return filepath.Join(gameProfilesDir(), "logs") }
func gameProfilesGamesDir() string  { return filepath.Join(gameProfilesDir(), "games") }

// gameProfileRoots lists where game folders may live. The data-folder games
// directory is always allowed (portable/test installs); TrimUI adds the SD
// card, and desktop adds the executable folder plus the user's game folders.
func gameProfileRoots() []string {
	if len(gameProfileRootsOverride) > 0 {
		return dedupeCleanPaths(gameProfileRootsOverride)
	}
	roots := []string{gameProfilesGamesDir()}
	if IsWindows() {
		if exe, err := os.Executable(); err == nil {
			roots = append(roots, filepath.Dir(exe))
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			roots = append(roots, filepath.Join(home, "Games"), filepath.Join(home, "Saved Games"))
		}
	} else {
		roots = append(roots, "/mnt/SDCARD")
	}
	return dedupeCleanPaths(roots)
}

func dedupeCleanPaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		clean, err := cleanUserPath(path)
		if err != nil {
			continue
		}
		key := filepath.Clean(clean)
		if IsWindows() {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, clean)
	}
	return out
}

// ensureGameProfilesLayout creates the folder skeleton and a starter catalog.
// It never overwrites an existing file, so it is safe on every boot.
func ensureGameProfilesLayout() error {
	dir := gameProfilesDir()
	if err := ensureGameProfilesLayoutAt(dir); err != nil {
		return err
	}
	catalogPath := filepath.Join(dir, "profiles.json")
	if _, err := os.Stat(catalogPath); errors.Is(err, os.ErrNotExist) {
		starter := gameProfileCatalogFile{SchemaVersion: gameProfileSchemaVersion, Profiles: []GameProfile{}}
		data, _ := json.MarshalIndent(starter, "", "  ")
		if err := os.WriteFile(catalogPath, append(data, '\n'), 0o644); err != nil {
			return err
		}
	}
	readmePath := filepath.Join(dir, "README.txt")
	if _, err := os.Stat(readmePath); errors.Is(err, os.ErrNotExist) {
		text := "JukaHub game profiles\n" +
			"=====================\n\n" +
			"profiles.json   shared catalog\n" +
			"import/*.json   community profiles (loaded automatically)\n" +
			"local/*.json    your overrides (a matching id wins)\n" +
			"export/*.json   files written by the Game Profiles panel (X)\n" +
			"logs/           one launch log per game\n" +
			"games/          optional portable game folders\n\n" +
			"Only import profiles from people you trust: a profile launches a\n" +
			"program and can set its environment.\n"
		if err := os.WriteFile(readmePath, []byte(text), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// InitGameProfiles prepares the profile folders once during startup. It is
// best-effort: a read-only data folder must not stop JukaHub from booting.
func InitGameProfiles() {
	if err := ensureGameProfilesLayout(); err != nil {
		log.Printf("[gameprofiles] setup failed: %v", err)
	}
}

// gameProfileEntries returns the loaded catalog, building it on first use.
func gameProfileEntries() []gameProfileEntry {
	gameProfileCache.mu.Lock()
	defer gameProfileCache.mu.Unlock()
	if !gameProfileCache.built {
		entries, issues := loadGameProfiles(gameProfilesDir(), gameProfileRoots())
		gameProfileCache.entries = entries
		gameProfileCache.issues = issues
		gameProfileCache.built = true
		for _, issue := range issues {
			log.Printf("[gameprofiles] %s", issue)
		}
	}
	out := make([]gameProfileEntry, len(gameProfileCache.entries))
	copy(out, gameProfileCache.entries)
	return out
}

func gameProfileIssues() []string {
	gameProfileEntries()
	gameProfileCache.mu.Lock()
	defer gameProfileCache.mu.Unlock()
	out := make([]string, len(gameProfileCache.issues))
	copy(out, gameProfileCache.issues)
	return out
}

// gameProfileStats returns totals for the Emulators row and the panel header.
func gameProfileStats() (total, ready, issues int) {
	entries := gameProfileEntries()
	for _, entry := range entries {
		if entry.ready() {
			ready++
		}
	}
	return len(entries), ready, len(gameProfileIssues())
}

// loadGameProfiles merges the database, import inbox and local overrides.
// Later layers replace earlier entries with the same id; duplicates inside one
// layer are reported and ignored.
func loadGameProfiles(dir string, roots []string) ([]gameProfileEntry, []string) {
	var issues []string
	if err := ensureGameProfilesLayoutAt(dir); err != nil {
		issues = append(issues, "folder setup: "+err.Error())
	}

	byID := make(map[string]gameProfileEntry)
	sourceRank := map[string]int{"database": 0, "import": 1, "local": 2}

	merge := func(profiles []GameProfile, source string) {
		for _, profile := range profiles {
			old, exists := byID[profile.ID]
			if exists && sourceRank[source] <= sourceRank[old.Source] {
				issues = append(issues, fmt.Sprintf("%s: duplicate id %q ignored", source, profile.ID))
				continue
			}
			if !exists && len(byID) >= gameProfileMaxProfiles {
				issues = append(issues, fmt.Sprintf("%s: more than %d profiles, remainder ignored", source, gameProfileMaxProfiles))
				return
			}
			entry := resolveGameProfileEntry(profile, source, roots)
			// An override may rescue a profile that used to be broken.
			byID[profile.ID] = entry
		}
	}

	if profiles, bundleIssues, err := readGameProfileBundle(filepath.Join(dir, "profiles.json")); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			issues = append(issues, "profiles.json: "+err.Error())
		}
	} else {
		for _, issue := range bundleIssues {
			issues = append(issues, "profiles.json: "+issue)
		}
		merge(profiles, "database")
	}

	for _, layer := range []string{"import", "local"} {
		layerProfiles, layerIssues := readGameProfileDir(filepath.Join(dir, layer))
		issues = append(issues, layerIssues...)
		merge(layerProfiles, layer)
	}

	entries := make([]gameProfileEntry, 0, len(byID))
	for _, entry := range byID {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(entries[i].Profile.Title))
		right := strings.ToLower(strings.TrimSpace(entries[j].Profile.Title))
		if left != right {
			return left < right
		}
		return entries[i].Profile.ID < entries[j].Profile.ID
	})
	return entries, issues
}

func ensureGameProfilesLayoutAt(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("empty game-profile folder")
	}
	for _, sub := range []string{"", "import", "local", "export", "logs", "games"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// readGameProfileDir reads every .json file in dir (non-recursively). A file
// may hold one profile or a bundle, which makes community packs easy to drop
// in without splitting them.
func readGameProfileDir(dir string) ([]GameProfile, []string) {
	items, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []string{dir + ": " + err.Error()}
	}
	var profiles []GameProfile
	var issues []string
	for _, item := range items {
		if item.IsDir() || !strings.HasSuffix(strings.ToLower(item.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, item.Name())
		loaded, fileIssues, err := readGameProfileBundle(path)
		if err != nil {
			issues = append(issues, item.Name()+": "+err.Error())
			continue
		}
		for _, issue := range fileIssues {
			issues = append(issues, item.Name()+": "+issue)
		}
		profiles = append(profiles, loaded...)
	}
	return profiles, issues
}

// readGameProfileBundle accepts both a single profile object and a
// {"schemaVersion":1,"profiles":[...]} catalog. A bundle keeps its valid
// profiles when one entry is malformed; the bad entry is returned as an issue
// so the panel can report it without hiding the rest of a community pack.
func readGameProfileBundle(path string) ([]GameProfile, []string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if info.IsDir() {
		return nil, nil, errors.New("is a directory")
	}
	if info.Size() > gameProfileMaxFileBytes {
		return nil, nil, fmt.Errorf("file is larger than %d bytes", gameProfileMaxFileBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	raw = stripUTF8BOM(raw)
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil, nil
	}

	var probe struct {
		SchemaVersion int             `json:"schemaVersion"`
		Profiles      json.RawMessage `json:"profiles"`
		ID            string          `json:"id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, nil, err
	}

	if probe.Profiles != nil {
		var bundle gameProfileCatalogFile
		if err := json.Unmarshal(raw, &bundle); err != nil {
			return nil, nil, err
		}
		if bundle.SchemaVersion != gameProfileSchemaVersion {
			return nil, nil, fmt.Errorf("unsupported schemaVersion %d (this build supports %d)", bundle.SchemaVersion, gameProfileSchemaVersion)
		}
		var issues []string
		valid := make([]GameProfile, 0, len(bundle.Profiles))
		for i := range bundle.Profiles {
			if err := validateGameProfile(&bundle.Profiles[i]); err != nil {
				issues = append(issues, fmt.Sprintf("profile %d: %v", i+1, err))
				continue
			}
			valid = append(valid, bundle.Profiles[i])
		}
		return valid, issues, nil
	}

	var profile GameProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return nil, nil, err
	}
	if probe.ID == "" && profile.ID == "" {
		return nil, nil, errors.New("missing profile id")
	}
	if err := validateGameProfile(&profile); err != nil {
		return nil, nil, err
	}
	return []GameProfile{profile}, nil, nil
}

func stripUTF8BOM(raw []byte) []byte {
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		return raw[3:]
	}
	return raw
}

// validateGameProfile checks the fields that must be right before a profile
// is offered at all. Missing files and missing runtimes are a launch issue,
// not a schema issue, so an exported profile still lists on another device.
func validateGameProfile(p *GameProfile) error {
	if p.SchemaVersion != gameProfileSchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %d (this build supports %d)", p.SchemaVersion, gameProfileSchemaVersion)
	}
	p.ID = strings.ToLower(strings.TrimSpace(p.ID))
	if !gameProfileIDPattern.MatchString(p.ID) {
		return fmt.Errorf("invalid id %q (use lowercase letters, digits, dot, dash or underscore)", p.ID)
	}
	p.Title = strings.TrimSpace(p.Title)
	if p.Title == "" {
		p.Title = p.ID
	}
	if len(p.Title) > 120 {
		return errors.New("title is longer than 120 characters")
	}
	p.Runtime = strings.ToLower(strings.TrimSpace(p.Runtime))
	switch p.Runtime {
	case gameRuntimeNative, gameRuntimeBox64, gameRuntimeWine, gameRuntimeScript:
	default:
		return fmt.Errorf("invalid runtime %q (use native, box64, wine or script)", p.Runtime)
	}
	if strings.ContainsAny(p.Dir, "\x00\r\n") {
		return errors.New("dir contains a control character")
	}
	if strings.TrimSpace(p.Dir) == "" {
		return errors.New("dir is required")
	}
	if filepath.VolumeName(filepath.FromSlash(p.Dir)) != "" && !filepath.IsAbs(filepath.FromSlash(p.Dir)) {
		return errors.New("dir must be absolute or relative, not drive-relative")
	}
	exe := filepath.FromSlash(strings.TrimSpace(p.Executable))
	if exe == "" {
		return errors.New("executable is required")
	}
	if filepath.IsAbs(exe) || filepath.VolumeName(exe) != "" {
		return errors.New("executable must be relative to dir")
	}
	if strings.ContainsAny(exe, "\x00\r\n") {
		return errors.New("executable contains a control character")
	}
	p.Executable = filepath.ToSlash(filepath.Clean(exe))
	if p.Executable == "." || p.Executable == ".." || strings.HasPrefix(p.Executable, "../") {
		return errors.New("executable must name a file inside dir")
	}

	ext := strings.ToLower(filepath.Ext(p.Executable))
	switch p.Runtime {
	case gameRuntimeWine:
		switch ext {
		case ".exe", ".bat", ".cmd", ".com":
		default:
			return errors.New("wine profiles must point at .exe, .bat, .cmd or .com")
		}
	case gameRuntimeScript:
		if ext != ".sh" {
			return errors.New("script profiles must point at a .sh file")
		}
	case gameRuntimeBox64:
		switch ext {
		case ".sh", ".bat", ".cmd", ".lnk", ".url":
			return fmt.Errorf("box64 cannot launch %s files", ext)
		}
	}

	if len(p.Args) > gameProfileMaxArgs {
		return fmt.Errorf("more than %d arguments", gameProfileMaxArgs)
	}
	for i, arg := range p.Args {
		if strings.ContainsRune(arg, '\x00') {
			return fmt.Errorf("argument %d contains a NUL byte", i+1)
		}
		if len(arg) > 4096 {
			return fmt.Errorf("argument %d is longer than 4096 bytes", i+1)
		}
	}
	if len(p.Env) > gameProfileMaxEnv {
		return fmt.Errorf("more than %d environment variables", gameProfileMaxEnv)
	}
	for key, value := range p.Env {
		if !gameProfileEnvKeyPattern.MatchString(key) {
			return fmt.Errorf("invalid environment key %q", key)
		}
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("environment value for %s contains a NUL byte", key)
		}
		if len(value) > 8192 {
			return fmt.Errorf("environment value for %s is longer than 8192 bytes", key)
		}
	}
	if len(p.Notes) > 2000 {
		return errors.New("notes are longer than 2000 characters")
	}
	if len(p.TestedFirmware) > 120 {
		return errors.New("testedFirmware is longer than 120 characters")
	}
	if len(p.Controller) > 64 {
		return errors.New("controller is longer than 64 characters")
	}
	return nil
}

// resolveGameProfileEntry validates paths and the runtime once at load time so
// the panel can show "Ready" or the exact reason it cannot launch.
func resolveGameProfileEntry(p GameProfile, source string, roots []string) gameProfileEntry {
	entry := gameProfileEntry{Profile: p, Source: source}
	gameDir, exe, err := resolveGameProfilePaths(p, roots)
	if err != nil {
		entry.Issue = err.Error()
		return entry
	}
	entry.GameDir = gameDir
	entry.Executable = exe
	if _, _, err := planGameRuntime(p, exe, exec.LookPath); err != nil {
		entry.Issue = err.Error()
	}
	return entry
}

// resolveGameProfilePaths resolves dir against the allowed roots and checks
// the executable stays inside the game folder after symlinks are evaluated.
func resolveGameProfilePaths(p GameProfile, roots []string) (string, string, error) {
	dir := filepath.FromSlash(strings.TrimSpace(p.Dir))
	if dir == "" {
		return "", "", errors.New("dir is required")
	}
	var gameDir string
	var err error
	if filepath.IsAbs(dir) {
		gameDir, err = containedDir(dir, roots)
		if err != nil {
			return "", "", fmt.Errorf("game folder: %w", err)
		}
	} else {
		for _, root := range roots {
			candidate := filepath.Join(root, dir)
			if found, walkErr := containedDir(candidate, []string{root}); walkErr == nil {
				gameDir = found
				break
			}
		}
		if gameDir == "" {
			return "", "", fmt.Errorf("game folder %q not found under %s", dir, strings.Join(roots, ", "))
		}
	}

	exe, err := resolveProfileFile(gameDir, p.Executable)
	if err != nil {
		return "", "", fmt.Errorf("executable: %w", err)
	}
	return gameDir, exe, nil
}

func containedDir(path string, roots []string) (string, error) {
	clean, err := existingDir(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", err
	}
	for _, root := range roots {
		realRoot, rootErr := realPath(root)
		if rootErr != nil {
			continue
		}
		if isPathWithin(real, realRoot) {
			return real, nil
		}
	}
	return "", fmt.Errorf("%q is outside the allowed game folders", path)
}

func existingDir(path string) (string, error) {
	clean, err := cleanUserPath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", clean)
	}
	return clean, nil
}

func realPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return real, nil
}

// resolveProfileFile joins a relative name to base, then rejects anything that
// leaves base either syntactically or through a symlink.
func resolveProfileFile(base, name string) (string, error) {
	resolved, err := resolveWithinRoot(base, filepath.FromSlash(name))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", resolved)
	}
	real, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	realBase, err := realPath(base)
	if err != nil {
		return "", err
	}
	if !isPathWithin(real, realBase) {
		return "", fmt.Errorf("%q escapes %q", resolved, base)
	}
	return real, nil
}

// planGameRuntime builds the command that actually starts the game. Wine is
// run directly when it is a native ARM binary or wrapper script; an x86 Wine
// ELF is prefixed with Box64.
func planGameRuntime(p GameProfile, exe string, lookPath func(string) (string, error)) (string, []string, error) {
	args := append([]string{}, p.Args...)
	switch p.Runtime {
	case gameRuntimeNative:
		return exe, args, nil
	case gameRuntimeScript:
		sh, err := lookPath("sh")
		if err != nil || sh == "" {
			return "", nil, errors.New("script runtime needs sh, but it was not found")
		}
		return sh, append([]string{exe}, args...), nil
	case gameRuntimeBox64:
		box, err := resolveBox64(lookPath)
		if err != nil {
			return "", nil, err
		}
		return box, append([]string{exe}, args...), nil
	case gameRuntimeWine:
		wine, err := resolveWine(lookPath)
		if err != nil {
			return "", nil, err
		}
		if needsBox64(wine) {
			box, boxErr := resolveBox64(lookPath)
			if boxErr != nil {
				return "", nil, fmt.Errorf("wine is x86 and needs box64: %w", boxErr)
			}
			return box, append([]string{wine, exe}, args...), nil
		}
		return wine, append([]string{exe}, args...), nil
	default:
		return "", nil, fmt.Errorf("unknown runtime %q", p.Runtime)
	}
}

func resolveBox64(lookPath func(string) (string, error)) (string, error) {
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_BOX64")); override != "" {
		return absoluteRegularFile(override)
	}
	candidates := []string{
		"/usr/trimui/bin/box64",
		"/mnt/SDCARD/Apps/Box64/box64",
		"/mnt/SDCARD/Apps/box64/box64",
		"/mnt/SDCARD/PortMaster/box64",
		"/mnt/SDCARD/Apps/PortMaster/box64",
		"/mnt/SDCARD/Emus/PortMaster/box64",
	}
	for _, candidate := range candidates {
		if regularFile(candidate) {
			return candidate, nil
		}
	}
	if lookPath != nil {
		if path, err := lookPath("box64"); err == nil && path != "" {
			return path, nil
		}
	}
	return "", errors.New("box64 not found (install Box64 or set JUKAHUB_BOX64)")
}

func resolveWine(lookPath func(string) (string, error)) (string, error) {
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_WINE")); override != "" {
		return absoluteRegularFile(override)
	}
	candidates := []string{
		"/usr/trimui/bin/wine",
		"/usr/trimui/bin/wine64",
		"/mnt/SDCARD/Apps/Wine/bin/wine",
		"/mnt/SDCARD/Apps/wine/bin/wine",
		"/mnt/SDCARD/PortMaster/wine/bin/wine",
		"/mnt/SDCARD/Apps/PortMaster/wine/bin/wine",
		"/mnt/SDCARD/Emus/PortMaster/wine/bin/wine",
	}
	for _, candidate := range candidates {
		if regularFile(candidate) {
			return candidate, nil
		}
	}
	if lookPath != nil {
		for _, name := range []string{"wine", "wine64"} {
			if path, err := lookPath(name); err == nil && path != "" {
				return path, nil
			}
		}
	}
	return "", errors.New("wine not found (install Wine or set JUKAHUB_WINE)")
}

// absoluteRegularFile validates an environment override and anchors it to an
// absolute path, so cmd.Dir cannot change what a relative override means.
func absoluteRegularFile(path string) (string, error) {
	clean, err := existingRegularFile(path)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return "", err
	}
	return abs, nil
}

// needsBox64 reports whether path is an x86 ELF, which an ARM device cannot
// execute directly. Scripts and ARM binaries return false.
func needsBox64(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	header := make([]byte, 20)
	if _, err := f.Read(header); err != nil {
		return false
	}
	if len(header) < 20 || header[0] != 0x7F || header[1] != 'E' || header[2] != 'L' || header[3] != 'F' {
		return false
	}
	// e_machine is little-endian for the x86 ELFs we care about.
	machine := uint16(header[18]) | uint16(header[19])<<8
	return machine == 0x03 || machine == 0x3E
}

// launchGameProfile re-resolves the profile (the SD card may have changed since
// the panel was built), starts it in its own process group, and reaps it in a
// goroutine so JukaHub stays responsive. A per-game log captures the child's
// output without exposing environment values.
func launchGameProfile(entry gameProfileEntry) error {
	id := entry.Profile.ID
	if pid := gameProfileRunningPID(id); pid > 0 {
		return fmt.Errorf("%s is already running (pid %d)", entry.Profile.Title, pid)
	}

	gameDir, exe, err := resolveGameProfilePaths(entry.Profile, gameProfileRoots())
	if err != nil {
		return err
	}
	command, args, err := planGameRuntime(entry.Profile, exe, exec.LookPath)
	if err != nil {
		return err
	}

	env := cloneGameProfileEnv(entry.Profile.Env)
	if entry.Profile.Runtime == gameRuntimeWine {
		if !hasEnvKey(env, "WINEPREFIX") {
			prefix := filepath.Join(gameProfilesDir(), "wineprefix")
			if err := os.MkdirAll(prefix, 0o755); err != nil {
				return fmt.Errorf("create wine prefix: %w", err)
			}
			env["WINEPREFIX"] = prefix
		}
	}

	cmd := exec.Command(command, args...)
	cmd.Dir = gameDir
	cmd.Env = mergeGameProfileEnv(os.Environ(), env)
	configureGameChild(cmd)

	var logFile *os.File
	if err := os.MkdirAll(gameProfilesLogsDir(), 0o755); err == nil {
		logPath := filepath.Join(gameProfilesLogsDir(), id+".log")
		if opened, logErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); logErr == nil {
			logFile = opened
			fmt.Fprintf(logFile, "\n=== %s start %s ===\ncommand: %s (args: %d)\nworking dir: %s\n",
				id, time.Now().Format(time.RFC3339), command, len(args), gameDir)
			cmd.Stdout = logFile
			cmd.Stderr = logFile
		}
	}

	return startGameProfileCommand(cmd, id, gameDir, logFile)
}

// startGameProfileCommand starts cmd, records its PID, and waits in the
// background. logFile is closed after the child exits.
func startGameProfileCommand(cmd *exec.Cmd, id, gameDir string, logFile *os.File) error {
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			fmt.Fprintf(logFile, "start failed: %v\n", err)
			logFile.Close()
		}
		return fmt.Errorf("start %s: %w", filepath.Base(cmd.Path), err)
	}
	pid := cmd.Process.Pid
	gameProfileSetRunning(id, pid)
	log.Printf("[gameprofiles] launched %s (pid %d, dir %s)", id, pid, gameDir)
	go func() {
		err := cmd.Wait()
		gameProfileSetRunning(id, 0)
		if logFile != nil {
			if err != nil {
				fmt.Fprintf(logFile, "exit: %v\n", err)
			} else {
				fmt.Fprint(logFile, "exit: 0\n")
			}
			logFile.Close()
		}
		if err != nil {
			log.Printf("[gameprofiles] %s exited: %v", id, err)
		} else {
			log.Printf("[gameprofiles] %s exited cleanly", id)
		}
	}()
	return nil
}

// gameProfileRunning tracks launched games by profile id.
var gameProfileRunning struct {
	mu   sync.Mutex
	pids map[string]int
}

func gameProfileRunningPID(id string) int {
	gameProfileRunning.mu.Lock()
	defer gameProfileRunning.mu.Unlock()
	return gameProfileRunning.pids[id]
}

func gameProfileSetRunning(id string, pid int) {
	gameProfileRunning.mu.Lock()
	defer gameProfileRunning.mu.Unlock()
	if gameProfileRunning.pids == nil {
		gameProfileRunning.pids = make(map[string]int)
	}
	if pid <= 0 {
		delete(gameProfileRunning.pids, id)
		return
	}
	gameProfileRunning.pids[id] = pid
}

func cloneGameProfileEnv(src map[string]string) map[string]string {
	out := make(map[string]string, len(src)+1)
	for key, value := range src {
		out[key] = value
	}
	return out
}

func hasEnvKey(env map[string]string, key string) bool {
	for existing := range env {
		if strings.EqualFold(existing, key) {
			return true
		}
	}
	return false
}

// mergeGameProfileEnv replaces inherited variables by key and appends profile
// overrides in sorted order for deterministic child environments.
func mergeGameProfileEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return append([]string{}, base...)
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(base)+len(keys))
	for _, item := range base {
		key := item
		if idx := strings.IndexByte(item, '='); idx >= 0 {
			key = item[:idx]
		}
		skip := false
		for _, override := range keys {
			if strings.EqualFold(key, override) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, item)
		}
	}
	for _, key := range keys {
		out = append(out, key+"="+overrides[key])
	}
	return out
}

// exportGameProfile writes the focused profile as a standalone JSON file that
// can be dropped into another device's import folder.
func exportGameProfile(entry gameProfileEntry) (string, error) {
	destDir := gameProfilesExportDir()
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	name, err := safeJoinFile(destDir, entry.Profile.ID+".json")
	if err != nil {
		return "", err
	}
	profile := entry.Profile
	profile.SchemaVersion = gameProfileSchemaVersion
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := destDir + string(filepath.Separator) + "." + entry.Profile.ID + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	// Windows refuses to rename over an existing file, so remove the previous
	// export first. The export folder is disposable, not a database.
	_ = os.Remove(name)
	if err := os.Rename(tmp, name); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return name, nil
}

// gameProfileRuntimeLabel is the short uppercase badge in the panel.
func gameProfileRuntimeLabel(runtime string) string {
	switch runtime {
	case gameRuntimeWine:
		return "WINE"
	case gameRuntimeBox64:
		return "BOX64"
	case gameRuntimeScript:
		return "SCRIPT"
	default:
		return "NATIVE"
	}
}
