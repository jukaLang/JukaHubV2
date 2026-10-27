package main

// compat.go — the Performance & Compatibility Intelligence layer.
//
// The A133P and its 1 GB of RAM are the hard limits: PS1 and older systems are
// comfortable, while N64, Dreamcast and PSP are game-dependent. Instead of
// pretending every ROM will run equally well, this file combines three sources
// of truth:
//
//   * a community catalog (data/performance/catalog.json plus import/local
//     overrides) that records per-game reports and optional core suggestions;
//   * a platform tier derived from the device's known limits, used when no
//     report exists yet;
//   * the core builds that are actually installed, so a recommendation never
//     points at a core the user does not have.
//
// A "boost plan" turns a report or the platform tier into concrete settings.
// The plan is always written as a reviewable JSON recipe under
// data/performance/boost; when RetroArch's config folder is present the plan
// also becomes a per-game core-option override (config/<Core>/<Game>.opt), so
// the next launch already uses the faster settings. Nothing is changed inside
// an emulator's global config, and applying a plan never touches the ROM.
//
// The catalog file is deliberately plain JSON: a community report can be
// copied to the import folder over FTP or the SD card, reviewed and removed
// without rebuilding JukaHub.

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	compatSchemaVersion = 1
	compatEntryName     = "Smart Pro Compatibility"

	compatMaxEntries   = 2000
	compatMaxFileBytes = 4 << 20
	compatMaxAliases   = 32
	compatMaxOptions   = 48
)

// compatSeedJSON is the starter catalog shipped with JukaHub. It is copied to
// the data folder on first boot; the seed is not re-applied afterwards, so a
// community pack or a local edit is never overwritten.
//
//go:embed compat_seed.json
var compatSeedJSON []byte

var (
	compatDirOverride          string
	compatCoreDirsOverride     []string
	compatRetroArchRoots       []string
	compatCorePoolDirOverride  string
	compatIDPattern            = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	compatCorePattern          = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	compatOptionKeyPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	compatSystemCanonicalNames = map[string]string{
		"nes":                           "NES",
		"famicom":                       "NES",
		"nintendo entertainment system": "NES",
		"snes":                          "SNES",
		"sfc":                           "SNES",
		"super nintendo":                "SNES",
		"super famicom":                 "SNES",
		"gb":                            "Game Boy",
		"game boy":                      "Game Boy",
		"gbc":                           "Game Boy Color",
		"game boy color":                "Game Boy Color",
		"gba":                           "Game Boy Advance",
		"game boy advance":              "Game Boy Advance",
		"genesis":                       "Mega Drive",
		"megadrive":                     "Mega Drive",
		"mega drive":                    "Mega Drive",
		"md":                            "Mega Drive",
		"ps1":                           "PlayStation",
		"psx":                           "PlayStation",
		"ps":                            "PlayStation",
		"playstation":                   "PlayStation",
		"n64":                           "Nintendo 64",
		"nintendo 64":                   "Nintendo 64",
		"dc":                            "Dreamcast",
		"dreamcast":                     "Dreamcast",
		"psp":                           "PlayStation Portable",
		"playstation portable":          "PlayStation Portable",
		"nds":                           "Nintendo DS",
		"nintendo ds":                   "Nintendo DS",
		"saturn":                        "Saturn",
		"sega saturn":                   "Saturn",
		"pce":                           "PC Engine",
		"pc engine":                     "PC Engine",
		"tg16":                          "PC Engine",
		"turbografx-16":                 "PC Engine",
		"neogeo":                        "Neo Geo",
		"neo geo":                       "Neo Geo",
		"arcade":                        "Arcade",
		"mame":                          "Arcade",
	}
)

// compatStatus is the per-game verdict in the community catalog.
const (
	compatStatusPlayable   = "playable"
	compatStatusAudio      = "audio-issues"
	compatStatusSlow       = "performance-issues"
	compatStatusUnplayable = "unplayable"
	compatStatusUnknown    = "unknown"
)

var compatStatuses = map[string]bool{
	compatStatusPlayable:   true,
	compatStatusAudio:      true,
	compatStatusSlow:       true,
	compatStatusUnplayable: true,
	compatStatusUnknown:    true,
}

// compatSettings is the portable part of a boost plan. A value of zero means
// "use the platform default", which keeps community entries short.
type compatSettings struct {
	FrameSkip       int               `json:"frameSkip,omitempty"`
	ResolutionScale int               `json:"resolutionScale,omitempty"`
	AudioQuality    string            `json:"audioQuality,omitempty"`
	RetroArch       map[string]string `json:"retroArch,omitempty"`
}

// compatEntry is one game report. The catalog can hold one of these directly
// or a {"schemaVersion":1,"entries":[...]} bundle.
type compatEntry struct {
	SchemaVersion int            `json:"schemaVersion,omitempty"`
	ID            string         `json:"id"`
	Title         string         `json:"title"`
	System        string         `json:"system"`
	Aliases       []string       `json:"aliases,omitempty"`
	Status        string         `json:"status"`
	Verified      bool           `json:"verifiedOnSmartPro,omitempty"`
	Core          string         `json:"core,omitempty"`
	CoreVersion   string         `json:"coreVersion,omitempty"`
	Notes         string         `json:"notes,omitempty"`
	Settings      compatSettings `json:"settings,omitempty"`
	Source        string         `json:"-"`
}

type compatCatalogFile struct {
	SchemaVersion int           `json:"schemaVersion"`
	Updated       string        `json:"updated,omitempty"`
	Entries       []compatEntry `json:"entries"`
}

// compatTier is the expectation set by the hardware alone, before a per-game
// report is considered. Solid covers PS1 and lighter 2D systems; Mixed covers
// the three systems the community consistently reports as game-dependent.
type compatTier int

const (
	compatTierUnknown compatTier = iota
	compatTierMixed
	compatTierSolid
)

func (t compatTier) label() string {
	switch t {
	case compatTierSolid:
		return "Solid on Smart Pro"
	case compatTierMixed:
		return "Game-dependent"
	default:
		return "No device data"
	}
}

// compatCatalogCache keeps the merged catalog between frames. The panel
// invalidates it on open and on Y so an imported pack appears immediately.
var compatCatalogCache struct {
	mu      sync.Mutex
	built   bool
	entries []compatEntry
	issues  []string
}

func invalidateCompatCache() {
	compatCatalogCache.mu.Lock()
	compatCatalogCache.built = false
	compatCatalogCache.entries = nil
	compatCatalogCache.issues = nil
	compatCatalogCache.mu.Unlock()
}

func compatDir() string {
	if compatDirOverride != "" {
		return compatDirOverride
	}
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return filepath.Join("data", "performance")
	}
	return filepath.Join(dir, "performance")
}

func compatImportDir() string { return filepath.Join(compatDir(), "import") }
func compatLocalDir() string  { return filepath.Join(compatDir(), "local") }
func compatBoostDir() string  { return filepath.Join(compatDir(), "boost") }
func compatCorePoolDir() string {
	if compatCorePoolDirOverride != "" {
		return compatCorePoolDirOverride
	}
	return filepath.Join(compatDir(), "cores")
}

// InitCompatibility creates the data-folder skeleton and the starter catalog.
// It is best effort: a read-only SD card must not stop JukaHub from booting.
func InitCompatibility() {
	if err := ensureCompatLayout(); err != nil {
		log.Printf("[compat] setup failed: %v", err)
	}
}

func ensureCompatLayout() error {
	base := compatDir()
	for _, sub := range []string{"", "import", "local", "boost", "cores"} {
		if err := os.MkdirAll(filepath.Join(base, sub), 0o755); err != nil {
			return err
		}
	}
	catalogPath := filepath.Join(base, "catalog.json")
	if _, err := os.Stat(catalogPath); errors.Is(err, os.ErrNotExist) {
		data := compatSeedJSON
		if len(strings.TrimSpace(string(data))) == 0 {
			data = []byte("{\n  \"schemaVersion\": 1,\n  \"entries\": []\n}\n")
		}
		if err := os.WriteFile(catalogPath, data, 0o644); err != nil {
			return err
		}
	}
	readme := filepath.Join(base, "README.txt")
	if _, err := os.Stat(readme); errors.Is(err, os.ErrNotExist) {
		text := "JukaHub performance & compatibility\n" +
			"====================================\n\n" +
			"catalog.json     community reports (starter catalog)\n" +
			"import/*.json    community packs, loaded automatically\n" +
			"local/*.json     your overrides (a matching id wins)\n" +
			"boost/*.json     reviewable per-game boost recipes\n" +
			"cores/           optional core-build pool, one folder per build:\n" +
			"                 cores/<core>/<version>/<core>_libretro.so\n\n" +
			"Reports are plain JSON. A report never launches anything; it only\n" +
			"describes how a game behaves and which settings are worth trying.\n" +
			"Only mark verifiedOnSmartPro when the game was actually tested on\n" +
			"this hardware.\n"
		if err := os.WriteFile(readme, []byte(text), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// compatEntries returns the merged catalog, loading it on first use.
func compatEntries() []compatEntry {
	compatCatalogCache.mu.Lock()
	defer compatCatalogCache.mu.Unlock()
	if !compatCatalogCache.built {
		entries, issues := loadCompatCatalog(compatDir())
		compatCatalogCache.entries = entries
		compatCatalogCache.issues = issues
		compatCatalogCache.built = true
		for _, issue := range issues {
			log.Printf("[compat] %s", issue)
		}
	}
	out := make([]compatEntry, len(compatCatalogCache.entries))
	copy(out, compatCatalogCache.entries)
	return out
}

func compatIssues() []string {
	compatEntries()
	compatCatalogCache.mu.Lock()
	defer compatCatalogCache.mu.Unlock()
	out := make([]string, len(compatCatalogCache.issues))
	copy(out, compatCatalogCache.issues)
	return out
}

// compatStats feeds the pinned Emulators row without scanning the ROM tree.
func compatStats() (reports, verified, systems int) {
	entries := compatEntries()
	systemSet := map[string]bool{}
	for _, entry := range entries {
		if entry.Verified {
			verified++
		}
		systemSet[entry.System] = true
	}
	return len(entries), verified, len(systemSet)
}

// loadCompatCatalog merges catalog.json, import/*.json and local/*.json. Later
// layers replace earlier entries with the same id. A malformed entry is
// reported and skipped instead of hiding the rest of a community pack.
func loadCompatCatalog(dir string) ([]compatEntry, []string) {
	var issues []string
	byID := map[string]compatEntry{}
	rank := map[string]int{"database": 0, "import": 1, "local": 2}

	merge := func(entries []compatEntry, source string) {
		for _, entry := range entries {
			if old, exists := byID[entry.ID]; exists && rank[source] <= rank[old.Source] {
				issues = append(issues, fmt.Sprintf("%s: duplicate id %q ignored", source, entry.ID))
				continue
			}
			entry.Source = source
			byID[entry.ID] = entry
		}
	}

	catalogPath := filepath.Join(dir, "catalog.json")
	entries, fileIssues, err := readCompatBundle(catalogPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			issues = append(issues, "catalog.json: "+err.Error())
		}
	} else {
		for _, issue := range fileIssues {
			issues = append(issues, "catalog.json: "+issue)
		}
		merge(entries, "database")
	}

	for _, layer := range []string{"import", "local"} {
		layerEntries, layerIssues := readCompatDir(filepath.Join(dir, layer))
		issues = append(issues, layerIssues...)
		merge(layerEntries, layer)
	}

	out := make([]compatEntry, 0, len(byID))
	for _, entry := range byID {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		left := strings.ToLower(out[i].Title)
		right := strings.ToLower(out[j].Title)
		if left != right {
			return left < right
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > compatMaxEntries {
		issues = append(issues, fmt.Sprintf("more than %d reports, remainder ignored", compatMaxEntries))
		out = out[:compatMaxEntries]
	}
	return out, issues
}

func readCompatDir(dir string) ([]compatEntry, []string) {
	items, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []string{dir + ": " + err.Error()}
	}
	var out []compatEntry
	var issues []string
	for _, item := range items {
		if item.IsDir() || !strings.HasSuffix(strings.ToLower(item.Name()), ".json") {
			continue
		}
		entries, fileIssues, err := readCompatBundle(filepath.Join(dir, item.Name()))
		if err != nil {
			issues = append(issues, item.Name()+": "+err.Error())
			continue
		}
		for _, issue := range fileIssues {
			issues = append(issues, item.Name()+": "+issue)
		}
		out = append(out, entries...)
	}
	return out, issues
}

// readCompatBundle accepts one report or a catalog bundle. The schema version
// is checked before any field is trusted, so an older build refuses a newer
// report instead of guessing at unknown fields.
func readCompatBundle(path string) ([]compatEntry, []string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if info.IsDir() {
		return nil, nil, errors.New("is a directory")
	}
	if info.Size() > compatMaxFileBytes {
		return nil, nil, fmt.Errorf("file is larger than %d bytes", compatMaxFileBytes)
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
		Entries       json.RawMessage `json:"entries"`
		ID            string          `json:"id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, nil, err
	}

	if probe.Entries != nil {
		var bundle compatCatalogFile
		if err := json.Unmarshal(raw, &bundle); err != nil {
			return nil, nil, err
		}
		if bundle.SchemaVersion != compatSchemaVersion {
			return nil, nil, fmt.Errorf("unsupported schemaVersion %d (this build supports %d)", bundle.SchemaVersion, compatSchemaVersion)
		}
		var issues []string
		valid := make([]compatEntry, 0, len(bundle.Entries))
		for i := range bundle.Entries {
			if err := validateCompatEntry(&bundle.Entries[i]); err != nil {
				issues = append(issues, fmt.Sprintf("report %d: %v", i+1, err))
				continue
			}
			valid = append(valid, bundle.Entries[i])
		}
		return valid, issues, nil
	}

	var entry compatEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, nil, err
	}
	if probe.ID == "" && entry.ID == "" {
		return nil, nil, errors.New("missing report id")
	}
	if entry.SchemaVersion == 0 {
		entry.SchemaVersion = compatSchemaVersion
	}
	if err := validateCompatEntry(&entry); err != nil {
		return nil, nil, err
	}
	return []compatEntry{entry}, nil, nil
}

func validateCompatEntry(entry *compatEntry) error {
	if entry.SchemaVersion != 0 && entry.SchemaVersion != compatSchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %d (this build supports %d)", entry.SchemaVersion, compatSchemaVersion)
	}
	entry.SchemaVersion = compatSchemaVersion
	entry.ID = strings.ToLower(strings.TrimSpace(entry.ID))
	if !compatIDPattern.MatchString(entry.ID) {
		return fmt.Errorf("invalid id %q (use lowercase letters, digits, dot, dash or underscore)", entry.ID)
	}
	entry.Title = strings.TrimSpace(entry.Title)
	if entry.Title == "" {
		entry.Title = entry.ID
	}
	if len(entry.Title) > 120 {
		return errors.New("title is longer than 120 characters")
	}
	entry.System = canonicalCompatSystem(entry.System)
	if entry.System == "" {
		return errors.New("system is required")
	}
	entry.Status = strings.ToLower(strings.TrimSpace(entry.Status))
	if entry.Status == "" {
		entry.Status = compatStatusUnknown
	}
	if !compatStatuses[entry.Status] {
		return fmt.Errorf("invalid status %q (use playable, audio-issues, performance-issues, unplayable or unknown)", entry.Status)
	}
	if len(entry.Aliases) > compatMaxAliases {
		return fmt.Errorf("more than %d aliases", compatMaxAliases)
	}
	seen := map[string]bool{}
	aliases := entry.Aliases[:0]
	for _, alias := range entry.Aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		if len(alias) > 120 {
			return errors.New("an alias is longer than 120 characters")
		}
		key := normalizeGameTitle(alias)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		aliases = append(aliases, alias)
	}
	entry.Aliases = aliases
	entry.Core = normalizeCoreName(entry.Core)
	if entry.Core != "" && !compatCorePattern.MatchString(entry.Core) {
		return fmt.Errorf("invalid core %q", entry.Core)
	}
	entry.CoreVersion = strings.TrimSpace(entry.CoreVersion)
	if len(entry.CoreVersion) > 64 {
		return errors.New("coreVersion is longer than 64 characters")
	}
	if len(entry.Notes) > 2000 {
		return errors.New("notes are longer than 2000 characters")
	}
	if entry.Settings.FrameSkip < 0 || entry.Settings.FrameSkip > 9 {
		return errors.New("frameSkip must be between 0 and 9")
	}
	switch entry.Settings.ResolutionScale {
	case 0, 50, 75, 100, 150, 200:
	default:
		return errors.New("resolutionScale must be 50, 75, 100, 150 or 200")
	}
	switch strings.ToLower(entry.Settings.AudioQuality) {
	case "", "high", "low", "mute":
	default:
		return errors.New("audioQuality must be high, low or mute")
	}
	entry.Settings.AudioQuality = strings.ToLower(entry.Settings.AudioQuality)
	if len(entry.Settings.RetroArch) > compatMaxOptions {
		return fmt.Errorf("more than %d RetroArch options", compatMaxOptions)
	}
	for key, value := range entry.Settings.RetroArch {
		if !compatOptionKeyPattern.MatchString(key) {
			return fmt.Errorf("invalid RetroArch option key %q", key)
		}
		if len(value) > 96 {
			return fmt.Errorf("RetroArch option %s is longer than 96 characters", key)
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("RetroArch option %s contains a control character", key)
		}
	}
	return nil
}

func canonicalCompatSystem(system string) string {
	key := strings.ToLower(strings.TrimSpace(system))
	key = strings.ReplaceAll(key, "_", " ")
	key = strings.Join(strings.Fields(key), " ")
	if canonical, ok := compatSystemCanonicalNames[key]; ok {
		return canonical
	}
	return strings.TrimSpace(system)
}

// normalizeGameTitle strips the extension, region/revision tags and
// punctuation. It intentionally does not guess at alternate titles: aliases in
// the catalog are explicit so a match is never a fuzzy surprise.
func normalizeGameTitle(name string) string {
	stem := filepath.Base(strings.TrimSpace(name))
	if ext := filepath.Ext(stem); ext != "" {
		stem = strings.TrimSuffix(stem, ext)
	}
	var b strings.Builder
	depthParen, depthBracket := 0, 0
	for _, r := range stem {
		switch r {
		case '(':
			depthParen++
			continue
		case ')':
			if depthParen > 0 {
				depthParen--
			}
			continue
		case '[':
			depthBracket++
			continue
		case ']':
			if depthBracket > 0 {
				depthBracket--
			}
			continue
		}
		if depthParen > 0 || depthBracket > 0 {
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// compatIndex maps a normalized system + title to a report. Aliases are added
// as separate keys; a later report with the same key does not silently replace
// the first one.
func compatIndex(entries []compatEntry) map[string]compatEntry {
	index := make(map[string]compatEntry, len(entries)*2)
	add := func(entry compatEntry, title string) {
		key := compatLookupKey(entry.System, title)
		if key == "" {
			return
		}
		if _, exists := index[key]; !exists {
			index[key] = entry
		}
	}
	for _, entry := range entries {
		add(entry, entry.Title)
		for _, alias := range entry.Aliases {
			add(entry, alias)
		}
	}
	return index
}

func compatLookupKey(system, title string) string {
	normSystem := normalizeGameTitle(canonicalCompatSystem(system))
	normTitle := normalizeGameTitle(title)
	if normSystem == "" || normTitle == "" {
		return ""
	}
	return normSystem + "\x00" + normTitle
}

func matchCompatEntry(rom scannedROM, index map[string]compatEntry) (compatEntry, bool) {
	entry, ok := index[compatLookupKey(rom.System, filepath.Base(rom.Path))]
	return entry, ok
}

// systemCompatTier encodes the hardware expectation. PS1 and lighter systems
// are the "solid" tier (the same group that gets the Smart Pro badge); N64,
// Dreamcast and PSP are explicitly game-dependent on the A133P.
func systemCompatTier(system string) compatTier {
	switch canonicalCompatSystem(system) {
	case "NES", "SNES", "Game Boy", "Game Boy Color", "Game Boy Advance",
		"Mega Drive", "PlayStation", "PC Engine", "Neo Geo", "Arcade":
		return compatTierSolid
	case "Nintendo 64", "Dreamcast", "Saturn", "PlayStation Portable", "Nintendo DS":
		return compatTierMixed
	default:
		return compatTierUnknown
	}
}

func defaultCoreForSystem(system string) string {
	switch canonicalCompatSystem(system) {
	case "PlayStation":
		return "pcsx_rearmed"
	case "Nintendo 64":
		return "mupen64plus_next"
	case "Dreamcast":
		return "flycast"
	case "PlayStation Portable":
		return "ppsspp"
	case "Nintendo DS":
		return "melonds"
	case "SNES":
		return "snes9x"
	case "NES":
		return "fceumm"
	case "Game Boy", "Game Boy Color":
		return "gambatte"
	case "Game Boy Advance":
		return "mgba"
	case "Mega Drive":
		return "genesis_plus_gx"
	case "Saturn":
		return "beetle_saturn"
	default:
		return ""
	}
}

func normalizeCoreName(core string) string {
	core = strings.ToLower(strings.TrimSpace(core))
	core = strings.TrimSuffix(core, "_libretro")
	core = strings.TrimSuffix(core, ".so")
	core = strings.TrimSuffix(core, ".dll")
	return core
}

// gameKey is stable across restarts and is used for the boost recipe name. The
// short path hash keeps two regional dumps with the same title from sharing one
// recipe, while the RetroArch override remains named after the ROM as expected.
func gameKey(rom scannedROM) string {
	slug := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, normalizeGameTitle(rom.Path))
	slug = strings.Trim(strings.Join(strings.FieldsFunc(slug, func(r rune) bool { return r == '-' }), "-"), "-")
	if slug == "" {
		slug = "game"
	}
	if len(slug) > 48 {
		slug = slug[:48]
	}
	h := 2166136261
	for _, r := range rom.Path {
		h ^= int(r)
		h *= 16777619
	}
	return fmt.Sprintf("%s-%08x", slug, uint32(h))
}

// boostPlan is the resolved, displayable answer for one game. Plan.Options are
// RetroArch core-option keys, not JukaHub settings.
type boostPlan struct {
	FrameSkip       int
	ResolutionScale int
	AudioQuality    string
	Options         map[string]string
	Recommended     bool
	Reason          string
}

func (p boostPlan) summary() string {
	parts := []string{}
	if p.FrameSkip > 0 {
		parts = append(parts, fmt.Sprintf("frameskip %d", p.FrameSkip))
	}
	if p.ResolutionScale > 0 && p.ResolutionScale != 100 {
		parts = append(parts, fmt.Sprintf("%d%% resolution", p.ResolutionScale))
	}
	if p.AudioQuality != "" {
		parts = append(parts, p.AudioQuality+" audio")
	}
	if len(parts) == 0 {
		return "Baseline settings"
	}
	return strings.Join(parts, " · ")
}

func buildBoostPlan(rom scannedROM, entry compatEntry, matched bool) boostPlan {
	tier := systemCompatTier(rom.System)
	plan := boostPlan{
		FrameSkip:       0,
		ResolutionScale: 100,
		AudioQuality:    "high",
		Options:         map[string]string{},
		Recommended:     tier == compatTierMixed,
		Reason:          "No boost needed for this system tier.",
	}
	if matched {
		if entry.Settings.FrameSkip > 0 {
			plan.FrameSkip = entry.Settings.FrameSkip
		}
		if entry.Settings.ResolutionScale > 0 {
			plan.ResolutionScale = entry.Settings.ResolutionScale
		}
		if entry.Settings.AudioQuality != "" {
			plan.AudioQuality = entry.Settings.AudioQuality
		}
		switch entry.Status {
		case compatStatusPlayable:
			plan.Recommended = false
			plan.Reason = "Community report says this runs at the baseline."
		case compatStatusAudio:
			plan.Recommended = true
			plan.Reason = "Community report notes audio issues; lower audio quality is worth trying."
		case compatStatusSlow:
			plan.Recommended = true
			plan.Reason = "Community report notes slowdowns; the boost plan trades some fidelity for speed."
		case compatStatusUnplayable:
			plan.Recommended = false
			plan.Reason = "Community report says this is not playable on the device; the plan is a best-effort experiment."
		}
	} else if tier == compatTierMixed {
		plan.Reason = "No community report yet. Mixed-platform defaults use safe speed settings."
	} else if tier == compatTierSolid {
		plan.Reason = "No community report yet. This platform tier normally runs at baseline."
	} else {
		plan.Reason = "No community report and no hardware tier for this system."
	}

	core := entry.Core
	if core == "" {
		core = defaultCoreForSystem(rom.System)
	}
	// Defaults are only for games without a report, or for reports that say
	// the game stutters. A playable report on a mixed system keeps the
	// emulator's own defaults.
	needsSpeed := (!matched && tier == compatTierMixed) || (matched && entry.Status == compatStatusSlow)
	if plan.FrameSkip == 0 && needsSpeed {
		plan.FrameSkip = 1
	}
	// Mupen64Plus-Next and melonDS do not expose a frameskip option. Do not
	// print a recommendation the emulator cannot apply; their plans use the
	// render size instead.
	if !coreSupportsFrameSkip(core) {
		plan.FrameSkip = 0
	}
	if plan.ResolutionScale == 100 && needsSpeed {
		plan.ResolutionScale = 75
	}
	if plan.AudioQuality == "high" && (needsSpeed || (matched && entry.Status == compatStatusAudio)) {
		plan.AudioQuality = "low"
	}

	if plan.Recommended || plan.FrameSkip > 0 || plan.ResolutionScale != 100 || plan.AudioQuality != "high" {
		addSystemBoostOptions(plan.Options, core, plan)
	}
	for key, value := range entry.Settings.RetroArch {
		plan.Options[key] = value
	}
	if len(plan.Options) == 0 {
		plan.Reason += " No core-level override is available; the recipe records the recommendation."
	}
	return plan
}

func coreSupportsFrameSkip(core string) bool {
	switch normalizeCoreName(core) {
	case "ppsspp", "flycast", "pcsx_rearmed":
		return true
	default:
		return false
	}
}

// addSystemBoostOptions uses documented libretro core-option keys. Values are
// deliberately conservative: resolution and frame skipping first, never a
// setting known to break a game outright.
func addSystemBoostOptions(options map[string]string, core string, plan boostPlan) {
	switch normalizeCoreName(core) {
	case "ppsspp":
		if plan.FrameSkip > 0 {
			options["ppsspp_frameskip"] = strconv.Itoa(plan.FrameSkip)
			options["ppsspp_auto_frameskip"] = "enabled"
		}
		if plan.ResolutionScale <= 100 {
			options["ppsspp_internal_resolution"] = "480x272"
		} else if plan.ResolutionScale >= 150 {
			options["ppsspp_internal_resolution"] = "960x544"
		}
	case "flycast":
		if plan.FrameSkip > 0 {
			options["flycast_frame_skipping"] = strconv.Itoa(minInt(plan.FrameSkip, 6))
			options["flycast_skip_frame"] = "enabled"
		}
		options["flycast_internal_resolution"] = "640x480"
		options["flycast_threaded_rendering"] = "enabled"
		options["flycast_delay_frame_swapping"] = "disabled"
		if plan.AudioQuality == "low" || plan.AudioQuality == "mute" {
			options["flycast_enable_dsp"] = "disabled"
		}
	case "mupen64plus_next":
		if plan.ResolutionScale <= 75 {
			options["mupen64plus-169screensize"] = "640x360"
			options["mupen64plus-43screensize"] = "320x240"
		} else {
			options["mupen64plus-169screensize"] = "960x540"
			options["mupen64plus-43screensize"] = "640x480"
		}
	case "pcsx_rearmed":
		if plan.FrameSkip > 0 {
			options["pcsx_rearmed_frameskip"] = strconv.Itoa(plan.FrameSkip)
		}
	}
}

// coreBuild is one libretro core binary that JukaHub can reason about. Active
// builds live in RetroArch's cores folder; pool builds live under
// data/performance/cores/<core>/<version>/ and can be activated with one click.
type coreBuild struct {
	Core    string `json:"core"`
	Version string `json:"version"`
	File    string `json:"file"`
	Path    string `json:"path"`
	Active  bool   `json:"active"`
	Pool    bool   `json:"pool"`
	Size    int64  `json:"size"`
}

func (b coreBuild) label() string {
	if b.Version == "" {
		return b.Core
	}
	return b.Core + " " + b.Version
}

type coreInventory struct {
	Builds   []coreBuild
	Active   map[string]coreBuild
	Pool     []coreBuild
	CoresDir string
	Problem  string
}

func retroArchCoreDirs() []string {
	if len(compatCoreDirsOverride) > 0 {
		return append([]string{}, compatCoreDirsOverride...)
	}
	candidates := []string{
		filepath.Join("/mnt/SDCARD/RetroArch/.retroarch", "cores"),
		filepath.Join("/mnt/SDCARD/RetroArch", "cores"),
		filepath.Join("/mnt/SDCARD/Apps/RetroArch/.retroarch", "cores"),
		filepath.Join("/usr/trimui/RetroArch/.retroarch", "cores"),
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		candidates = append(candidates,
			filepath.Join(dir, "RetroArch", ".retroarch", "cores"),
			filepath.Join(dir, "retroarch", "cores"),
		)
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates,
			filepath.Join(filepath.Dir(exe), "RetroArch", ".retroarch", "cores"),
			filepath.Join(filepath.Dir(exe), "retroarch", "cores"),
		)
	}
	seen := map[string]bool{}
	var out []string
	for _, candidate := range candidates {
		clean := filepath.Clean(candidate)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		if info, err := os.Stat(clean); err == nil && info.IsDir() {
			out = append(out, clean)
		}
	}
	return out
}

func scanCoreInventory() coreInventory {
	inv := coreInventory{Active: map[string]coreBuild{}}
	dirs := retroArchCoreDirs()
	if len(dirs) > 0 {
		inv.CoresDir = dirs[0]
	} else {
		inv.Problem = "RetroArch cores folder not found"
	}
	for _, dir := range dirs {
		items, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, item := range items {
			if item.IsDir() {
				continue
			}
			build, ok := coreBuildFromPath(filepath.Join(dir, item.Name()), false)
			if !ok {
				continue
			}
			build.Active = true
			if _, exists := inv.Active[build.Core]; exists {
				continue
			}
			inv.Active[build.Core] = build
			inv.Builds = append(inv.Builds, build)
		}
	}

	poolRoot := compatCorePoolDir()
	_ = filepath.WalkDir(poolRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		build, ok := coreBuildFromPath(path, true)
		if !ok {
			return nil
		}
		build.Pool = true
		inv.Pool = append(inv.Pool, build)
		return nil
	})

	sort.Slice(inv.Builds, func(i, j int) bool { return inv.Builds[i].Core < inv.Builds[j].Core })
	sort.Slice(inv.Pool, func(i, j int) bool {
		if inv.Pool[i].Core != inv.Pool[j].Core {
			return inv.Pool[i].Core < inv.Pool[j].Core
		}
		return compareCoreVersions(inv.Pool[i].Version, inv.Pool[j].Version) > 0
	})
	return inv
}

func coreBuildFromPath(path string, pool bool) (coreBuild, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".so" && ext != ".dll" {
		return coreBuild{}, false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return coreBuild{}, false
	}
	core, version := parseCoreFile(filepath.Base(path))
	if core == "" {
		return coreBuild{}, false
	}
	if pool {
		if parent := filepath.Base(filepath.Dir(path)); parent != "" && parent != "." && parent != core && parent != "cores" && parent != "builds" {
			version = parent
		}
		if version == "" || version == "unversioned" {
			version = versionFromMetadata(path)
		}
	}
	return coreBuild{
		Core:    core,
		Version: version,
		File:    filepath.Base(path),
		Path:    path,
		Pool:    pool,
		Size:    info.Size(),
	}, true
}

func parseCoreFile(name string) (string, string) {
	base := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(name, ".so"), ".dll"))
	version := ""
	if idx := strings.Index(base, "_libretro"); idx >= 0 {
		core := base[:idx]
		rest := strings.TrimPrefix(base[idx:], "_libretro")
		rest = strings.TrimLeft(rest, "_-")
		if rest != "" {
			version = strings.TrimPrefix(rest, "v")
		}
		if core == "" {
			return "", ""
		}
		return core, version
	}
	return normalizeCoreName(base), ""
}

func coreBuildFileName(core string) string {
	if IsWindows() {
		return core + "_libretro.dll"
	}
	return core + "_libretro.so"
}

// compareCoreVersions compares dotted/numeric versions. A non-numeric version
// sorts below a numeric one so an explicit release is preferred over "dev".
func compareCoreVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	if len(pa) == 0 && len(pb) == 0 {
		return strings.Compare(a, b)
	}
	if len(pa) == 0 {
		return -1
	}
	if len(pb) == 0 {
		return 1
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var left, right int
		if i < len(pa) {
			left = pa[i]
		}
		if i < len(pb) {
			right = pb[i]
		}
		if left != right {
			if left < right {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(version string) []int {
	var out []int
	current := 0
	have := false
	for _, r := range version {
		if r >= '0' && r <= '9' {
			current = current*10 + int(r-'0')
			have = true
			continue
		}
		if have {
			out = append(out, current)
			current, have = 0, false
		}
	}
	if have {
		out = append(out, current)
	}
	return out
}

func versionFromMetadata(path string) string {
	dir := filepath.Dir(path)
	for _, name := range []string{"version.txt", "VERSION", "version"} {
		if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			if value := strings.TrimSpace(string(data)); value != "" && len(value) <= 64 {
				return value
			}
		}
	}
	return ""
}

func coreStateFor(core, required string, inv coreInventory) (state, detail string) {
	if core == "" {
		return "none", "No core suggestion for this system"
	}
	if active, ok := inv.Active[core]; ok {
		// A binary whose filename carries no version is still a usable
		// active core; without a version there is no honest way to call it
		// outdated.
		if required != "" && active.Version != "" && compareCoreVersions(active.Version, required) < 0 {
			return "outdated", fmt.Sprintf("Active %s is older than the recommended %s", active.Version, required)
		}
		if active.Version == "" {
			return "active", "Active core · version not reported"
		}
		return "active", "Active core " + active.Version
	}
	if len(inv.Pool) > 0 {
		for _, build := range inv.Pool {
			if build.Core == core {
				return "pool", "Build " + build.Version + " is in the core pool"
			}
		}
	}
	return "missing", "Not installed"
}

// activateCoreBuild copies a pool build into RetroArch's cores folder and
// keeps the previous active binary next to it as a timestamped backup.
func activateCoreBuild(build coreBuild) (string, string, error) {
	if !build.Pool {
		return "", "", errors.New("only builds from the core pool can be activated")
	}
	if build.Active {
		return build.Path, "", nil
	}
	source, err := existingRegularFile(build.Path)
	if err != nil {
		return "", "", err
	}
	dirs := retroArchCoreDirs()
	if len(dirs) == 0 {
		return "", "", errors.New("RetroArch cores folder not found")
	}
	destDir := dirs[0]
	targetName := coreBuildFileName(build.Core)
	if ext := strings.ToLower(filepath.Ext(build.File)); ext == ".so" || ext == ".dll" {
		targetName = build.Core + "_libretro" + ext
	}
	// Prefer the extension of the active binary in this folder: that is the
	// file the emulator actually loads. Fall back to the build's extension
	// for a first-time install (a Linux card mounted on Windows, or vice versa).
	for _, ext := range []string{".so", ".dll"} {
		if regularFile(filepath.Join(destDir, build.Core+"_libretro"+ext)) {
			targetName = build.Core + "_libretro" + ext
			break
		}
	}
	target := filepath.Join(destDir, targetName)
	backup, err := installCoreFile(source, target)
	if err != nil {
		return "", backup, err
	}
	log.Printf("[compat] activated core %s %s from %s (backup %s)", build.Core, build.Version, source, backup)
	return target, backup, nil
}

// installCoreFile puts source in place of target, keeping the file it replaces
// as a timestamped .bak beside it. It is the one path that changes a core
// binary — pool activation and the Emulators page's rollback both use it — so
// the staging rule and the backup naming cannot drift apart.
//
// The write goes source → .jukahub.tmp → rename, so a card pulled mid-copy
// leaves the old core in place instead of a half-written one. The previous
// binary is copied before anything is removed, and a second replacement in the
// same second gets its own name rather than overwriting the first build.
func installCoreFile(source, target string) (string, error) {
	backup := ""
	if regularFile(target) {
		backup = nextCoreBackupPath(target)
		if err := copyCompatFile(target, backup); err != nil {
			return "", fmt.Errorf("backup current core: %w", err)
		}
	}
	tmp := target + ".jukahub.tmp"
	if err := copyCompatFile(source, tmp); err != nil {
		_ = os.Remove(tmp)
		return backup, fmt.Errorf("stage core build: %w", err)
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(tmp)
		return backup, fmt.Errorf("replace core binary: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return backup, fmt.Errorf("activate core build: %w", err)
	}
	return backup, nil
}

// nextCoreBackupPath is a free `.jukahub-<stamp>.bak` name beside target. The
// collision counter matters: a rollback and a re-update inside the same second
// would otherwise overwrite the only copy of the previous build.
func nextCoreBackupPath(target string) string {
	base := target + ".jukahub-" + time.Now().Format("20060102-150405")
	for i := 2; i < 100; i++ {
		candidate := base + ".bak"
		if i > 2 {
			candidate = base + "-" + strconv.Itoa(i) + ".bak"
		}
		if !regularFile(candidate) {
			return candidate
		}
	}
	return base + "-" + strconv.Itoa(time.Now().Nanosecond()) + ".bak"
}

func copyCompatFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, info.Mode().Perm()); err != nil {
		return err
	}
	return nil
}

// retroArchConfigDirs returns existing RetroArch config folders. A missing
// folder is not an error: the recipe is still saved and the panel says the
// override was not installed.
func retroArchConfigDirs() []string {
	if len(compatRetroArchRoots) > 0 {
		return append([]string{}, compatRetroArchRoots...)
	}
	candidates := []string{
		"/mnt/SDCARD/RetroArch/.retroarch/config",
		"/mnt/SDCARD/RetroArch/config",
		"/mnt/SDCARD/Apps/RetroArch/.retroarch/config",
		"/usr/trimui/RetroArch/.retroarch/config",
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		candidates = append(candidates,
			filepath.Join(dir, "RetroArch", ".retroarch", "config"),
			filepath.Join(dir, "retroarch", "config"),
		)
	}
	seen := map[string]bool{}
	var out []string
	for _, candidate := range candidates {
		clean := filepath.Clean(candidate)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		if info, err := os.Stat(clean); err == nil && info.IsDir() {
			out = append(out, clean)
		}
	}
	return out
}

func coreConfigDisplayName(core string) string {
	switch normalizeCoreName(core) {
	case "ppsspp":
		return "PPSSPP"
	case "mupen64plus_next":
		return "Mupen64Plus-Next"
	case "flycast":
		return "Flycast"
	case "pcsx_rearmed":
		return "PCSX-ReARMed"
	case "beetle_psx", "beetle_psx_hw":
		return "Beetle PSX"
	case "melonds":
		return "melonDS"
	case "snes9x":
		return "Snes9x"
	case "gambatte":
		return "Gambatte"
	case "mgba":
		return "mGBA"
	case "genesis_plus_gx":
		return "Genesis Plus GX"
	default:
		return core
	}
}

type boostRecipe struct {
	SchemaVersion   int               `json:"schemaVersion"`
	Key             string            `json:"key"`
	Title           string            `json:"title"`
	System          string            `json:"system"`
	ROM             string            `json:"rom"`
	Core            string            `json:"core,omitempty"`
	CoreVersion     string            `json:"coreVersion,omitempty"`
	FrameSkip       int               `json:"frameSkip,omitempty"`
	ResolutionScale int               `json:"resolutionScale,omitempty"`
	AudioQuality    string            `json:"audioQuality,omitempty"`
	RetroArch       map[string]string `json:"retroArch,omitempty"`
	Targets         []string          `json:"targets,omitempty"`
	AppliedAt       string            `json:"appliedAt"`
}

type boostResult struct {
	Recipe   string
	Targets  []string
	Warnings []string
}

func boostRecipePath(key string) string {
	return filepath.Join(compatBoostDir(), key+".json")
}

func boostRecipeExists(key string) bool { return regularFile(boostRecipePath(key)) }

func writeCompatAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if f, err := os.Open(tmp); err == nil {
		_ = f.Sync()
		f.Close()
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// applyBoost writes the reviewable recipe and, when available, a RetroArch
// per-game core-option override. It never edits a global emulator config.
func applyBoost(assessment gameAssessment) (boostResult, error) {
	result := boostResult{}
	if strings.TrimSpace(assessment.ROM.Path) == "" {
		return result, errors.New("this game has no ROM path")
	}
	if _, err := existingRegularFile(assessment.ROM.Path); err != nil {
		return result, fmt.Errorf("ROM is missing: %w", err)
	}
	core := assessment.Core
	recipe := boostRecipe{
		SchemaVersion:   compatSchemaVersion,
		Key:             assessment.Key,
		Title:           assessment.Title,
		System:          assessment.ROM.System,
		ROM:             assessment.ROM.Path,
		Core:            core,
		CoreVersion:     assessment.CoreVersion,
		FrameSkip:       assessment.Plan.FrameSkip,
		ResolutionScale: assessment.Plan.ResolutionScale,
		AudioQuality:    assessment.Plan.AudioQuality,
		RetroArch:       assessment.Plan.Options,
		AppliedAt:       time.Now().Format(time.RFC3339),
	}

	if len(assessment.Plan.Options) > 0 && core != "" {
		configDirs := retroArchConfigDirs()
		if len(configDirs) == 0 {
			result.Warnings = append(result.Warnings, "RetroArch config folder not found; only the recipe was saved")
		} else {
			display := coreConfigDisplayName(core)
			stem := strings.TrimSuffix(filepath.Base(assessment.ROM.Path), filepath.Ext(assessment.ROM.Path))
			dir := filepath.Join(configDirs[0], display)
			target := filepath.Join(dir, stem+".opt")
			if err := writeCompatAtomic(target, compatOptionFile(assessment), 0o644); err != nil {
				return result, fmt.Errorf("write RetroArch override: %w", err)
			}
			result.Targets = append(result.Targets, target)
			recipe.Targets = append(recipe.Targets, target)
		}
	} else if len(assessment.Plan.Options) == 0 {
		result.Warnings = append(result.Warnings, "no core-level override is available for this system")
	}

	data, err := json.MarshalIndent(recipe, "", "  ")
	if err != nil {
		return result, err
	}
	if err := writeCompatAtomic(boostRecipePath(recipe.Key), append(data, '\n'), 0o644); err != nil {
		return result, err
	}
	result.Recipe = boostRecipePath(recipe.Key)
	log.Printf("[compat] boost applied for %s (%s): %d override(s)", recipe.Title, recipe.Key, len(result.Targets))
	return result, nil
}

func compatOptionFile(assessment gameAssessment) []byte {
	var b strings.Builder
	b.WriteString("# JukaHub boost mode\n")
	b.WriteString("# Game: " + assessment.Title + "\n")
	b.WriteString("# Generated " + time.Now().Format("2006-01-02 15:04") + " — remove with X in the compatibility panel.\n")
	keys := make([]string, 0, len(assessment.Plan.Options))
	for key := range assessment.Plan.Options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := strings.ReplaceAll(assessment.Plan.Options[key], `"`, `\"`)
		b.WriteString(key + " = \"" + value + "\"\n")
	}
	return []byte(b.String())
}

// revertBoost removes the recipe and only the overrides that JukaHub wrote.
// A user-authored .opt without the header is left untouched.
func revertBoost(assessment gameAssessment) ([]string, error) {
	var removed []string
	recipePath := boostRecipePath(assessment.Key)
	var recipe boostRecipe
	if data, err := os.ReadFile(recipePath); err == nil {
		if json.Unmarshal(data, &recipe) == nil {
			for _, target := range recipe.Targets {
				if !underAnyDir(target, retroArchConfigDirs()) {
					continue
				}
				data, readErr := os.ReadFile(target)
				if readErr != nil || !strings.HasPrefix(string(data), "# JukaHub boost mode") {
					continue
				}
				if err := os.Remove(target); err == nil {
					removed = append(removed, target)
				}
			}
		}
	}
	if err := os.Remove(recipePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return removed, err
	}
	if len(removed) == 0 {
		removed = append(removed, recipePath)
	}
	return removed, nil
}

func underAnyDir(path string, dirs []string) bool {
	for _, dir := range dirs {
		if isPathWithin(path, dir) {
			return true
		}
	}
	return false
}

// gameAssessment is the panel's complete answer for one scanned ROM.
type gameAssessment struct {
	ROM         scannedROM
	Key         string
	Title       string
	Entry       compatEntry
	Matched     bool
	Status      string
	Verified    bool
	Tier        compatTier
	Core        string
	CoreVersion string
	CoreState   string
	CoreDetail  string
	Plan        boostPlan
	Boosted     bool
}

func (a gameAssessment) statusLabel() string {
	switch a.Status {
	case compatStatusPlayable:
		return "Playable"
	case compatStatusAudio:
		return "Audio issues"
	case compatStatusSlow:
		return "Slowdowns"
	case compatStatusUnplayable:
		return "Not playable"
	default:
		return a.Tier.label()
	}
}

func (a gameAssessment) detailLine() string {
	parts := []string{a.ROM.System}
	if a.Core != "" {
		parts = append(parts, a.Core)
	}
	if a.CoreVersion != "" {
		parts = append(parts, "needs "+a.CoreVersion)
	}
	if a.Boosted {
		parts = append(parts, "boost applied")
	}
	return strings.Join(parts, " · ")
}

func assessROM(rom scannedROM, index map[string]compatEntry, inv coreInventory) gameAssessment {
	entry, matched := matchCompatEntry(rom, index)
	assessment := gameAssessment{
		ROM:     rom,
		Key:     gameKey(rom),
		Title:   displayROMTitle(rom),
		Entry:   entry,
		Matched: matched,
		Status:  compatStatusUnknown,
		Tier:    systemCompatTier(rom.System),
		Boosted: false,
	}
	if matched {
		assessment.Status = entry.Status
		assessment.Verified = entry.Verified
		assessment.Core = entry.Core
		assessment.CoreVersion = entry.CoreVersion
	}
	if assessment.Core == "" {
		assessment.Core = defaultCoreForSystem(rom.System)
	}
	assessment.CoreState, assessment.CoreDetail = coreStateFor(assessment.Core, assessment.CoreVersion, inv)
	assessment.Plan = buildBoostPlan(rom, entry, matched)
	assessment.Boosted = boostRecipeExists(assessment.Key)
	return assessment
}

func displayROMTitle(rom scannedROM) string {
	stem := strings.TrimSuffix(filepath.Base(rom.Path), filepath.Ext(rom.Path))
	if stem == "" {
		return filepath.Base(rom.Path)
	}
	return stem
}

// buildGameAssessments is pure apart from the boost-recipe existence check,
// which lets tests exercise matching and ordering without a device.
func buildGameAssessments(roms []scannedROM, entries []compatEntry, inv coreInventory) []gameAssessment {
	index := compatIndex(entries)
	out := make([]gameAssessment, 0, len(roms))
	seen := map[string]bool{}
	for _, rom := range roms {
		key := gameKey(rom)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, assessROM(rom, index, inv))
	}
	rank := func(a gameAssessment) int {
		switch a.Status {
		case compatStatusPlayable:
			if a.Verified {
				return 0
			}
			return 1
		case compatStatusUnknown:
			if a.Tier == compatTierSolid {
				return 2
			}
			return 4
		case compatStatusAudio, compatStatusSlow:
			return 3
		case compatStatusUnplayable:
			return 5
		default:
			return 4
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if rank(out[i]) != rank(out[j]) {
			return rank(out[i]) < rank(out[j])
		}
		if out[i].ROM.System != out[j].ROM.System {
			return out[i].ROM.System < out[j].ROM.System
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out
}

// compatibilityScan keeps the potentially slow ROM walk off the render thread.
type compatibilityScan struct {
	mu       sync.Mutex
	scanning bool
	built    bool
	games    []gameAssessment
	roots    []string
	issues   []string
}

var compatScan compatibilityScan

func startCompatibilityScan() {
	compatScan.mu.Lock()
	if compatScan.scanning {
		compatScan.mu.Unlock()
		return
	}
	compatScan.scanning = true
	compatScan.built = false
	compatScan.mu.Unlock()
	go func() {
		roots := romLibraryRoots()
		report := scanROMLibrary(roots)
		entries := compatEntries()
		inv := scanCoreInventory()
		games := buildGameAssessments(report.Games, entries, inv)
		compatScan.mu.Lock()
		compatScan.games = games
		compatScan.roots = roots
		compatScan.scanning = false
		compatScan.built = true
		compatScan.issues = compatIssues()
		compatScan.mu.Unlock()
	}()
}

func compatibilitySnapshot() (games []gameAssessment, roots []string, issues []string, scanning, built bool) {
	compatScan.mu.Lock()
	defer compatScan.mu.Unlock()
	out := make([]gameAssessment, len(compatScan.games))
	copy(out, compatScan.games)
	return out, append([]string{}, compatScan.roots...), append([]string{}, compatScan.issues...), compatScan.scanning, compatScan.built
}

func compatibilityFind(key string) (gameAssessment, bool) {
	games, _, _, _, _ := compatibilitySnapshot()
	for _, game := range games {
		if game.Key == key {
			return game, true
		}
	}
	return gameAssessment{}, false
}

// setCompatibilityBoostState updates the cached row immediately after apply
// or revert, so the panel does not need a full ROM rescan for a one-file
// change.
func setCompatibilityBoostState(key string, boosted bool) {
	compatScan.mu.Lock()
	defer compatScan.mu.Unlock()
	for i := range compatScan.games {
		if compatScan.games[i].Key == key {
			compatScan.games[i].Boosted = boosted
			return
		}
	}
}

// compatibilitySummary is the one-line header used by the panel and the pinned
// Emulators row.
func compatibilitySummary(games []gameAssessment) string {
	if len(games) == 0 {
		return "No ROMs found yet"
	}
	var playable, mixed, unknown, boosted int
	for _, game := range games {
		switch game.Status {
		case compatStatusPlayable:
			playable++
		case compatStatusAudio, compatStatusSlow:
			mixed++
		case compatStatusUnknown:
			unknown++
		default:
			mixed++
		}
		if game.Boosted {
			boosted++
		}
	}
	parts := []string{fmt.Sprintf("%d game(s)", len(games)), fmt.Sprintf("%d playable", playable)}
	if mixed > 0 {
		parts = append(parts, fmt.Sprintf("%d with caveats", mixed))
	}
	if unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d unreported", unknown))
	}
	if boosted > 0 {
		parts = append(parts, fmt.Sprintf("%d boosted", boosted))
	}
	return strings.Join(parts, " · ")
}
