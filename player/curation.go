package main

// curation.go — the Community Curation Hub.
//
// The Store lists whatever the package registry contains. This hub is the
// curated layer on top of it, built around three questions a new owner actually
// has: "make this device safe and nice in one press", "which UI setup suits this
// panel", and "how do I tell the maintainers what is broken?".
//
// Everything is local-first. The built-in essentials bundle and profiles work
// with no network at all, profiles are plain JSON that can be shared as files,
// and votes are kept in a readable JSON store. A remote feed and a vote
// collector can be configured at build time or with environment variables; an
// unconfigured build never pretends to reach a server.

import (
	"bytes"
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

const (
	curationDirName  = "curation"
	curationFeedVer  = 1
	curationVoteVer  = 1
	curationPanelTtl = "Curation Hub"

	curationDeviceAny = "any"
)

// Set by release builds, e.g.
// -ldflags "-X main.curationFeedEndpoint=https://…/curation.json
//
//	-X main.curationVotesEndpoint=https://…/votes".
//
// Empty values keep the hub offline: the built-in content is used and votes are
// exported as files.
var (
	curationFeedEndpoint  string
	curationVotesEndpoint string

	curationDirOverride string
)

func curationFeedURL() string {
	if v := strings.TrimSpace(os.Getenv("JUKAHUB_CURATION_FEED")); v != "" {
		return v
	}
	return strings.TrimSpace(curationFeedEndpoint)
}

func curationVotesURL() string {
	if v := strings.TrimSpace(os.Getenv("JUKAHUB_CURATION_VOTES_URL")); v != "" {
		return v
	}
	return strings.TrimSpace(curationVotesEndpoint)
}

// ── Layout ────────────────────────────────────────────────────────────────

func curationDir() string {
	if curationDirOverride != "" {
		return curationDirOverride
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, curationDirName)
	}
	return filepath.Join("data", curationDirName)
}

func curationProfilesDir() string { return filepath.Join(curationDir(), "profiles") }
func curationImportDir() string   { return filepath.Join(curationDir(), "import") }
func curationExportDir() string   { return filepath.Join(curationDir(), "export") }
func curationVotesPath() string   { return filepath.Join(curationDir(), "votes.json") }

// InitCuration creates the hub's folders once. Best effort: a read-only card
// must not stop JukaHub from booting.
func InitCuration() {
	if err := ensureCurationLayout(); err != nil {
		log.Printf("[curation] setup failed: %v", err)
	}
}

func ensureCurationLayout() error {
	for _, dir := range []string{curationDir(), curationProfilesDir(), curationImportDir(), curationExportDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	readme := filepath.Join(curationDir(), "README.txt")
	if _, err := os.Stat(readme); errors.Is(err, os.ErrNotExist) {
		text := "JukaHub curation hub\n" +
			"=====================\n\n" +
			"profiles/*.json   UI profiles saved here (Capture this setup)\n" +
			"import/*.json     profiles or bundles dropped in from another device\n" +
			"export/*.json     profiles and vote bundles ready to share or submit\n" +
			"votes.json        your local compatibility votes\n\n" +
			"A profile is {id, name, author, device, theme, textScale, notes}.\n" +
			"Theme must be one of JukaHub's built-in theme keys and textScale is\n" +
			"clamped to 85-145%, so an imported file can never break the UI.\n\n" +
			"Everything here is plain files. Nothing is uploaded unless a vote\n" +
			"collector or curation feed is configured in this build.\n"
		if err := os.WriteFile(readme, []byte(text), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ── Device ────────────────────────────────────────────────────────────────

// curationDevice is the panel family a bundle or profile was written for. It is
// informational for profiles: applying a Brick-tuned profile on a Smart Pro is
// allowed, with a warning, because the theme and text scale are portable.
type curationDevice struct {
	Key     string
	Label   string
	Family  string // "wide" or "compact"
	Compact bool
}

func curationDeviceFor(device DeviceModel) curationDevice {
	switch device {
	case DeviceTrimuiSmartPro:
		return curationDevice{Key: "smartpro", Label: "Smart Pro", Family: "wide"}
	case DeviceTrimuiSmartProS:
		return curationDevice{Key: "smartpro-s", Label: "Smart Pro S", Family: "wide"}
	case DeviceTrimuiBrick:
		return curationDevice{Key: "brick", Label: "Brick", Family: "compact", Compact: true}
	case DeviceTrimuiBrickPro:
		return curationDevice{Key: "brick-pro", Label: "Brick Pro", Family: "compact", Compact: true}
	case DeviceTrimuiHammer:
		return curationDevice{Key: "hammer", Label: "Hammer", Family: "compact", Compact: true}
	}
	label := deviceDisplayName(device)
	if strings.TrimSpace(label) == "" {
		label = "This device"
	}
	return curationDevice{Key: "generic", Label: label, Family: "generic"}
}

func currentCurationDevice() curationDevice { return curationDeviceFor(DetectDevice()) }

// ── Essentials bundle ─────────────────────────────────────────────────────

// curationStep is one action or check in a bundle. Kinds are intentionally few:
// a feeder cannot make the hub write arbitrary files.
type curationStep struct {
	Kind   string `json:"kind"`
	Value  string `json:"value,omitempty"`
	Detail string `json:"detail,omitempty"`
}

const (
	curationStepSaveSync     = "saveSync"
	curationStepSaveSnapshot = "saveSnapshot"
	curationStepCores        = "cores"
	curationStepDisplay      = "display"
	curationStepBattery      = "battery"
)

type curationBundle struct {
	SchemaVersion int            `json:"schemaVersion,omitempty"`
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Device        string         `json:"device,omitempty"`
	Summary       string         `json:"summary,omitempty"`
	Steps         []curationStep `json:"steps"`
}

// curationEnv is the snapshot of the device state the checklist is judged
// against. Keeping it a value makes the checklist pure and testable.
type curationEnv struct {
	Roots       int
	SyncReady   bool
	HasSnapshot bool
	Cores       int
	CoresDir    string
	Theme       string
	Battery     bool
}

type curationStepStatus struct {
	Step     curationStep
	Done     bool
	Value    string
	Line     string
	CanApply bool
}

type curationEssentials struct {
	Bundle curationBundle
	Steps  []curationStepStatus
	Ready  int
}

// builtinCurationBundles returns one essentials bundle per panel family. They
// fill gaps and never overwrite an explicit choice: the display step only acts
// when no theme has been chosen, and every step is idempotent.
func builtinCurationBundles() []curationBundle {
	return []curationBundle{
		{
			SchemaVersion: 1,
			ID:            "essentials-smartpro",
			Name:          "Smart Pro Essentials",
			Device:        "smartpro",
			Summary:       "Save safety and a neutral 720p setup for the 16:9 panels.",
			Steps: []curationStep{
				{Kind: curationStepSaveSync, Detail: "Mark every save folder for Syncthing."},
				{Kind: curationStepSaveSnapshot, Detail: "Keep a first snapshot of the save folders."},
				{Kind: curationStepCores, Detail: "Check that RetroArch has cores installed."},
				{Kind: curationStepDisplay, Value: "hub", Detail: "Apply the neutral palette only when no theme is chosen."},
				{Kind: curationStepBattery, Detail: "Keep save-and-suspend at 3% enabled."},
			},
		},
		{
			SchemaVersion: 1,
			ID:            "essentials-compact",
			Name:          "Compact Essentials",
			Device:        "brick",
			Summary:       "Save safety and a higher-contrast setup for the 4:3 panels.",
			Steps: []curationStep{
				{Kind: curationStepSaveSync, Detail: "Mark every save folder for Syncthing."},
				{Kind: curationStepSaveSnapshot, Detail: "Keep a first snapshot of the save folders."},
				{Kind: curationStepCores, Detail: "Check that RetroArch has cores installed."},
				{Kind: curationStepDisplay, Value: "midnight", Detail: "Apply the midnight palette only when no theme is chosen."},
				{Kind: curationStepBattery, Detail: "Keep save-and-suspend at 3% enabled."},
			},
		},
	}
}

// curationBundleForDevice picks the bundle whose device matches the panel, then
// the family, then the first bundle as a fallback.
func curationBundleForDevice(device curationDevice, bundles []curationBundle) (curationBundle, bool) {
	for _, bundle := range bundles {
		if bundle.Device == device.Key {
			return bundle, true
		}
	}
	family := "smartpro"
	if device.Compact {
		family = "brick"
	}
	for _, bundle := range bundles {
		if bundle.Device == family {
			return bundle, true
		}
	}
	if device.Family == "generic" && len(bundles) > 0 {
		return bundles[0], true
	}
	if len(bundles) > 0 {
		return bundles[0], true
	}
	return curationBundle{}, false
}

func curationThemeExists(name string) bool {
	_, ok := themePresets[strings.TrimSpace(name)]
	return ok
}

// curationThemeApplier is a seam for tests: applying a profile changes the
// process-wide palette, which would leak into render tests. Production always
// goes through applyThemePreset.
var curationThemeApplier = func(config *Config, name string, notify bool) {
	applyThemePreset(config, name, notify)
}

func curationStepLabel(kind string) string {
	switch kind {
	case curationStepSaveSync:
		return "Save folders ready for sync"
	case curationStepSaveSnapshot:
		return "A save snapshot exists"
	case curationStepCores:
		return "RetroArch cores present"
	case curationStepDisplay:
		return "Display profile"
	case curationStepBattery:
		return "Save & suspend at 3%"
	}
	return kind
}

// buildCurationEssentials judges each step against the environment. Nothing here
// touches the disk, so the panel can render every frame without rescanning.
func buildCurationEssentials(bundle curationBundle, env curationEnv) curationEssentials {
	out := curationEssentials{Bundle: bundle}
	for _, step := range bundle.Steps {
		status := curationStepStatus{Step: step, Line: step.Detail}
		switch step.Kind {
		case curationStepSaveSync:
			status.Done = env.SyncReady
			status.Value = "Not set up"
			if env.SyncReady {
				status.Value = "Prepared"
			}
			status.CanApply = env.Roots > 0 && !env.SyncReady
			if env.Roots == 0 {
				status.Line = "No save folders found yet — start a game once."
			}
		case curationStepSaveSnapshot:
			status.Done = env.HasSnapshot
			status.Value = "None yet"
			if env.HasSnapshot {
				status.Value = "Saved"
			}
			status.CanApply = env.Roots > 0 && !env.HasSnapshot
			if env.Roots == 0 {
				status.Line = "No save folders found yet — start a game once."
			}
		case curationStepCores:
			status.Done = env.Cores > 0
			status.Value = fmt.Sprintf("%d build(s)", env.Cores)
			if env.Cores == 0 {
				status.Value = "Not found"
				status.Line = "No cores in " + curationCoresLocation(env) + " — install RetroArch first."
			} else {
				status.Line = curationCoresLocation(env)
			}
		case curationStepDisplay:
			theme := strings.TrimSpace(step.Value)
			if theme == "" {
				theme = "hub"
			}
			if !curationThemeExists(theme) {
				theme = "hub"
			}
			status.Done = strings.TrimSpace(env.Theme) != ""
			status.Value = "Theme chosen"
			if !status.Done {
				status.Value = theme
				status.CanApply = true
			}
		case curationStepBattery:
			status.Done = env.Battery
			status.Value = "Off"
			if env.Battery {
				status.Value = "On"
			}
			status.CanApply = !env.Battery
		default:
			status.Line = "Unknown step — ignored."
		}
		if status.Done {
			out.Ready++
		}
		out.Steps = append(out.Steps, status)
	}
	return out
}

func curationCoresLocation(env curationEnv) string {
	if strings.TrimSpace(env.CoresDir) != "" {
		return env.CoresDir
	}
	return "RetroArch/.retroarch/cores"
}

// liveCurationEnv gathers the checklist inputs from the device.
func liveCurationEnv(config *Config) curationEnv {
	env := curationEnv{}
	env.Roots = len(saveManagerRoots())
	if _, ok := saveSyncStatus(); ok {
		env.SyncReady = true
	}
	if _, ok := latestSaveSnapshot(); ok {
		env.HasSnapshot = true
	}
	inv := scanCoreInventory()
	env.Cores = len(inv.Builds)
	env.CoresDir = inv.CoresDir
	if config != nil {
		if theme, ok := customString(config.Variables.Custom, "theme_preset"); ok {
			env.Theme = strings.TrimSpace(theme)
		}
	}
	env.Battery = lowBatteryGuardEnabled(config)
	return env
}

func curationSetCustomBool(config *Config, key string, value bool) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[key] = value
	saveUserConfig(userConfigFromVariables(config))
}

type curationApplyResult struct {
	Applied []string
	Skipped []string
	Failed  []string
}

func (r curationApplyResult) summary() string {
	parts := []string{fmt.Sprintf("%d applied", len(r.Applied))}
	if len(r.Skipped) > 0 {
		parts = append(parts, fmt.Sprintf("%d already set", len(r.Skipped)))
	}
	if len(r.Failed) > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", len(r.Failed)))
	}
	return strings.Join(parts, " · ")
}

// applyCurationStep performs one step. It is idempotent: a step that is already
// satisfied is reported as skipped, not applied twice.
func applyCurationStep(config *Config, step curationStep) (string, error) {
	switch step.Kind {
	case curationStepSaveSync:
		if _, ok := saveSyncStatus(); ok {
			return "already prepared", nil
		}
		roots := saveManagerRoots()
		if len(roots) == 0 {
			return "", errors.New("no save folders found yet")
		}
		manifest, err := prepareSaveSync(roots)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d folder(s) prepared", len(manifest.Folders)), nil
	case curationStepSaveSnapshot:
		if _, ok := latestSaveSnapshot(); ok {
			return "snapshot already exists", nil
		}
		roots := saveManagerRoots()
		if len(roots) == 0 {
			return "", errors.New("no save folders found yet")
		}
		snap, err := createSaveSnapshot(roots, "essentials")
		if err != nil {
			return "", err
		}
		return "snapshot " + snap.Manifest.ID, nil
	case curationStepCores:
		inv := scanCoreInventory()
		if len(inv.Builds) == 0 {
			return "", errors.New("no RetroArch cores found — install RetroArch first")
		}
		return fmt.Sprintf("%d core build(s)", len(inv.Builds)), nil
	case curationStepDisplay:
		if config == nil {
			return "", errors.New("no config")
		}
		if theme, ok := customString(config.Variables.Custom, "theme_preset"); ok && strings.TrimSpace(theme) != "" {
			return "theme already chosen", nil
		}
		theme := strings.TrimSpace(step.Value)
		if !curationThemeExists(theme) {
			theme = "hub"
		}
		curationThemeApplier(config, theme, false)
		return "theme " + theme, nil
	case curationStepBattery:
		if lowBatteryGuardEnabled(config) {
			return "already on", nil
		}
		curationSetCustomBool(config, "lowBatteryGuard", true)
		return "enabled", nil
	}
	return "", fmt.Errorf("unknown step %q", step.Kind)
}

func applyCurationBundle(config *Config, bundle curationBundle) curationApplyResult {
	var result curationApplyResult
	for _, step := range bundle.Steps {
		label := curationStepLabel(step.Kind)
		message, err := applyCurationStep(config, step)
		if err != nil {
			result.Failed = append(result.Failed, label+": "+firstLine(err.Error()))
			log.Printf("[curation] %s: %v", step.Kind, err)
			continue
		}
		if strings.HasPrefix(message, "already") {
			result.Skipped = append(result.Skipped, label)
			continue
		}
		result.Applied = append(result.Applied, label+" ("+message+")")
	}
	return result
}

// ── UI profiles ───────────────────────────────────────────────────────────

type curationProfile struct {
	SchemaVersion int     `json:"schemaVersion,omitempty"`
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Author        string  `json:"author,omitempty"`
	Device        string  `json:"device,omitempty"`
	Theme         string  `json:"theme"`
	TextScale     float64 `json:"textScale,omitempty"`
	Notes         string  `json:"notes,omitempty"`

	Source string `json:"-"` // builtin, local or imported
}

// builtinCurationProfiles are the curated starters. They exist so the exchange
// is useful before anyone uploads a profile, and they double as format examples.
func builtinCurationProfiles() []curationProfile {
	return []curationProfile{
		{
			SchemaVersion: 1, ID: "smartpro-crisp", Name: "Smart Pro · Crisp",
			Author: "JukaHub", Device: "smartpro", Theme: "hub", TextScale: 1.0,
			Notes: "Neutral palette at 100% text for the 1280×720 panel.", Source: "builtin",
		},
		{
			SchemaVersion: 1, ID: "brick-roomier", Name: "Brick · Roomier text",
			Author: "JukaHub", Device: "brick", Theme: "midnight", TextScale: 1.15,
			Notes: "Larger labels and a darker palette for the 4:3 compact canvas.", Source: "builtin",
		},
		{
			SchemaVersion: 1, ID: "contrast-large", Name: "High contrast · Large",
			Author: "JukaHub", Device: curationDeviceAny, Theme: "highcontrast", TextScale: 1.3,
			Notes: "Bright borders and 130% text, for daylight play.", Source: "builtin",
		},
	}
}

func curationSlug(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "profile"
	}
	return out
}

// normalizeCurationProfile fills defaults, clamps the scale and refuses a theme
// the app does not ship. An imported profile can carry bad numbers, but it must
// never be able to set an arbitrary palette or a 10× font.
func normalizeCurationProfile(p *curationProfile) error {
	if p == nil {
		return errors.New("empty profile")
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return errors.New("profile has no name")
	}
	if strings.TrimSpace(p.ID) == "" {
		p.ID = curationSlug(p.Name)
	}
	p.ID = curationSlug(p.ID)
	p.Author = strings.TrimSpace(p.Author)
	p.Device = strings.ToLower(strings.TrimSpace(p.Device))
	p.Notes = strings.TrimSpace(p.Notes)
	p.Theme = strings.TrimSpace(p.Theme)
	if p.Theme == "" {
		p.Theme = "hub"
	}
	if !curationThemeExists(p.Theme) {
		return fmt.Errorf("unknown theme %q", p.Theme)
	}
	if p.TextScale <= 0 {
		p.TextScale = 1.0
	}
	p.TextScale = a11yClampScale(p.TextScale)
	if p.SchemaVersion <= 0 {
		p.SchemaVersion = 1
	}
	return nil
}

// decodeCurationProfiles accepts a single profile, an array, or an object with
// a profiles array, because those are the three shapes people paste into a
// sharing thread.
func decodeCurationProfiles(data []byte) ([]curationProfile, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, errors.New("empty file")
	}
	var many []curationProfile
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &many); err != nil {
			return nil, err
		}
		return many, nil
	}
	var one curationProfile
	if err := json.Unmarshal(trimmed, &one); err == nil && one.Name != "" {
		return []curationProfile{one}, nil
	}
	var wrapper struct {
		Profiles []curationProfile `json:"profiles"`
	}
	if err := json.Unmarshal(trimmed, &wrapper); err != nil {
		return nil, err
	}
	if len(wrapper.Profiles) == 0 {
		return nil, errors.New("no profiles in file")
	}
	return wrapper.Profiles, nil
}

func readCurationProfilesIn(dir, source string) ([]curationProfile, []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	var profiles []curationProfile
	var issues []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			issues = append(issues, entry.Name()+": "+firstLine(err.Error()))
			continue
		}
		loaded, err := decodeCurationProfiles(data)
		if err != nil {
			issues = append(issues, entry.Name()+": "+firstLine(err.Error()))
			continue
		}
		for _, profile := range loaded {
			if err := normalizeCurationProfile(&profile); err != nil {
				issues = append(issues, entry.Name()+": "+firstLine(err.Error()))
				continue
			}
			profile.Source = source
			profiles = append(profiles, profile)
		}
	}
	return profiles, issues
}

// loadCurationProfiles merges built-ins, saved profiles and imported files.
// Later layers replace an entry with the same id, so a local tweak wins.
func loadCurationProfiles() ([]curationProfile, []string) {
	out := builtinCurationProfiles()
	issues := []string{}
	local, localIssues := readCurationProfilesIn(curationProfilesDir(), "local")
	imported, importIssues := readCurationProfilesIn(curationImportDir(), "imported")
	issues = append(issues, localIssues...)
	issues = append(issues, importIssues...)

	order := make([]string, 0, len(out)+len(local)+len(imported))
	byID := map[string]curationProfile{}
	add := func(profiles []curationProfile) {
		for _, profile := range profiles {
			if _, seen := byID[profile.ID]; !seen {
				order = append(order, profile.ID)
			}
			byID[profile.ID] = profile
		}
	}
	add(out)
	add(local)
	add(imported)

	result := make([]curationProfile, 0, len(order))
	for _, id := range order {
		result = append(result, byID[id])
	}
	return result, issues
}

func captureCurationProfile(config *Config, device curationDevice) (curationProfile, error) {
	theme := "hub"
	if config != nil {
		if v, ok := customString(config.Variables.Custom, "theme_preset"); ok && strings.TrimSpace(v) != "" {
			theme = strings.TrimSpace(v)
		}
	}
	scale := a11y.fontScale
	if scale <= 0 {
		scale = 1.0
	}
	name := "My " + device.Label + " setup"
	profile := curationProfile{
		SchemaVersion: 1,
		ID:            curationSlug(name) + "-" + time.Now().Format("20060102-150405"),
		Name:          name,
		Author:        "you",
		Device:        device.Key,
		Theme:         theme,
		TextScale:     scale,
		Source:        "local",
	}
	if err := normalizeCurationProfile(&profile); err != nil {
		return curationProfile{}, err
	}
	if _, err := writeCurationProfile(profile, curationProfilesDir()); err != nil {
		return curationProfile{}, err
	}
	return profile, nil
}

func writeCurationProfile(profile curationProfile, dir string) (string, error) {
	if err := normalizeCurationProfile(&profile); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, profile.ID+".json")
	// A profile the user built in the hub is theirs to keep: write it through
	// the same temp+fsync+rename path as the config, keeping the previous
	// version as a .bak, instead of truncating it in place.
	if err := AtomicWrite(path, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func exportCurationProfile(profile curationProfile) (string, error) {
	return writeCurationProfile(profile, curationExportDir())
}

// deleteCurationProfile removes only a profile saved in the hub's own folder.
// Built-ins and imports are left alone: deleting them would look permanent and
// then come back on the next refresh.
func deleteCurationProfile(profile curationProfile) error {
	if profile.Source != "local" {
		return errors.New("only saved profiles can be deleted")
	}
	path := filepath.Join(curationProfilesDir(), profile.ID+".json")
	if !isPathWithin(path, curationProfilesDir()) {
		return errors.New("profile path is outside the hub")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	// The atomic write keeps the previous version as .bak. A profile the user
	// deleted must not come back the first time a read has to fall back, so the
	// backup goes with it.
	if err := os.Remove(path + ".bak"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// applyCurationProfile writes the theme and text scale through the same paths
// the Appearance and Accessibility panels use, so every cached colour and font
// is refreshed exactly as if the user had changed them by hand.
func applyCurationProfile(config *Config, profile curationProfile) error {
	if config == nil {
		return errors.New("no config")
	}
	if err := normalizeCurationProfile(&profile); err != nil {
		return err
	}
	if !curationThemeExists(profile.Theme) {
		return fmt.Errorf("unknown theme %q", profile.Theme)
	}
	curationThemeApplier(config, profile.Theme, false)
	a11y.fontScale = a11yClampScale(profile.TextScale)
	a11yRefreshFonts()
	a11ySave(config)
	return nil
}

func curationProfileMatchesDevice(profile curationProfile, device curationDevice) bool {
	if profile.Device == "" || profile.Device == curationDeviceAny {
		return true
	}
	return profile.Device == device.Key
}

// ── Votes ─────────────────────────────────────────────────────────────────

type curationVote struct {
	Kind   string    `json:"kind"`
	Key    string    `json:"key"`
	Title  string    `json:"title"`
	System string    `json:"system,omitempty"`
	At     time.Time `json:"at"`
}

type curationVotesFile struct {
	SchemaVersion int            `json:"schemaVersion"`
	Device        string         `json:"device,omitempty"`
	Updated       time.Time      `json:"updated"`
	Votes         []curationVote `json:"votes"`
}

type curationTarget struct {
	Kind   string
	Key    string
	Title  string
	System string
	Detail string
}

func loadCurationVotes() curationVotesFile {
	store := curationVotesFile{SchemaVersion: curationVoteVer}
	// A damaged votes file comes back from its .bak rather than reading as "no
	// votes yet", which looked to the user like the app had forgotten them.
	if _, err := readJSONWithBackup(curationVotesPath(), &store); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[curation] votes.json: %v", err)
		}
		return curationVotesFile{SchemaVersion: curationVoteVer}
	}
	if store.SchemaVersion <= 0 {
		store.SchemaVersion = curationVoteVer
	}
	return store
}

func saveCurationVotes(store curationVotesFile) error {
	if err := ensureCurationLayout(); err != nil {
		return err
	}
	store.SchemaVersion = curationVoteVer
	store.Updated = time.Now().UTC()
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	// Votes are user input and cannot be regenerated: same crash-safe write as
	// the config, so a power cut cannot truncate the file mid-save.
	return AtomicWrite(curationVotesPath(), append(data, '\n'), 0o644)
}

func curationVoteIndex(store curationVotesFile) map[string]bool {
	index := make(map[string]bool, len(store.Votes))
	for _, vote := range store.Votes {
		index[vote.Kind+":"+vote.Key] = true
	}
	return index
}

func curationVoteKey(target curationTarget) string { return target.Kind + ":" + target.Key }

// toggleCurationVote adds or removes one vote and persists the store. It
// returns true when the vote was added.
func toggleCurationVote(store *curationVotesFile, target curationTarget) (bool, error) {
	if store == nil {
		return false, errors.New("no vote store")
	}
	key := curationVoteKey(target)
	for i, vote := range store.Votes {
		if vote.Kind+":"+vote.Key == key {
			store.Votes = append(store.Votes[:i], store.Votes[i+1:]...)
			return false, saveCurationVotes(*store)
		}
	}
	store.Votes = append(store.Votes, curationVote{
		Kind: target.Kind, Key: target.Key, Title: target.Title,
		System: target.System, At: time.Now().UTC(),
	})
	return true, saveCurationVotes(*store)
}

// curationTargets turns the compatibility scan and installed ports into the
// vote list. It is pure so the ordering can be tested without a ROM library.
func curationTargets(games []gameAssessment, ports []portEntry) []curationTarget {
	var out []curationTarget
	for _, game := range games {
		out = append(out, curationTarget{
			Kind:   "game",
			Key:    game.Key,
			Title:  game.Title,
			System: game.ROM.System,
			Detail: game.statusLabel(),
		})
	}
	for _, port := range ports {
		if port.Master || port.Install || port.Info || strings.TrimSpace(port.Launch) == "" {
			continue
		}
		name := strings.TrimSpace(port.Name)
		if name == "" {
			continue
		}
		out = append(out, curationTarget{
			Kind:   "port",
			Key:    strings.ToLower(filepath.Base(port.Launch)),
			Title:  name,
			System: "PortMaster",
			Detail: "Testing priority",
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "game"
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out
}

func curationVotePayload(store curationVotesFile, device curationDevice, version string) map[string]interface{} {
	return map[string]interface{}{
		"schemaVersion": curationVoteVer,
		"device":        device.Key,
		"platform":      P().Name(),
		"version":       version,
		"generatedAt":   time.Now().UTC().Format(time.RFC3339),
		"votes":         store.Votes,
	}
}

func exportCurationVotes(store curationVotesFile) (string, error) {
	if err := ensureCurationLayout(); err != nil {
		return "", err
	}
	payload := curationVotePayload(store, currentCurationDevice(), versionString(appConfig))
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(curationExportDir(), "votes-"+time.Now().Format("20060102-150405")+".json")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// submitCurationVotes posts the votes when a collector is configured and always
// leaves a local copy in export/. A missing collector is not an error: it is an
// offline build, and the panel says so instead of failing silently.
func submitCurationVotes(store curationVotesFile) (path string, sent bool, err error) {
	if len(store.Votes) == 0 {
		return "", false, errors.New("no votes yet")
	}
	endpoint := curationVotesURL()
	exported, exportErr := exportCurationVotes(store)
	if endpoint == "" {
		if exportErr != nil {
			return "", false, exportErr
		}
		return exported, false, nil
	}
	parsed, parseErr := url.Parse(endpoint)
	if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		if exportErr == nil {
			return exported, false, fmt.Errorf("vote collector URL is invalid — saved locally instead")
		}
		return "", false, fmt.Errorf("vote collector URL is invalid")
	}
	body, err := json.Marshal(curationVotePayload(store, currentCurationDevice(), versionString(appConfig)))
	if err != nil {
		return exported, false, err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		if exportErr == nil {
			return exported, false, fmt.Errorf("could not reach the collector — saved locally instead")
		}
		return "", false, fmt.Errorf("could not reach the collector")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if exportErr == nil {
			return exported, false, fmt.Errorf("collector returned %s — saved locally instead", resp.Status)
		}
		return "", false, fmt.Errorf("collector returned %s", resp.Status)
	}
	return exported, true, nil
}

// ── Remote feed ───────────────────────────────────────────────────────────

type curationFeed struct {
	SchemaVersion int               `json:"schemaVersion"`
	Updated       string            `json:"updated,omitempty"`
	Bundles       []curationBundle  `json:"bundles,omitempty"`
	Profiles      []curationProfile `json:"profiles,omitempty"`
}

func validateCurationBundle(bundle *curationBundle) error {
	if bundle == nil {
		return errors.New("empty bundle")
	}
	bundle.ID = curationSlug(bundle.ID)
	if bundle.ID == "" {
		bundle.ID = curationSlug(bundle.Name)
	}
	bundle.Name = strings.TrimSpace(bundle.Name)
	if bundle.Name == "" {
		return errors.New("bundle has no name")
	}
	if len(bundle.Steps) == 0 {
		return errors.New("bundle has no steps")
	}
	for _, step := range bundle.Steps {
		switch step.Kind {
		case curationStepSaveSync, curationStepSaveSnapshot, curationStepCores, curationStepBattery:
		case curationStepDisplay:
			if step.Value != "" && !curationThemeExists(step.Value) {
				return fmt.Errorf("bundle %q: unknown theme %q", bundle.Name, step.Value)
			}
		default:
			return fmt.Errorf("bundle %q: unknown step %q", bundle.Name, step.Kind)
		}
	}
	if bundle.SchemaVersion <= 0 {
		bundle.SchemaVersion = 1
	}
	return nil
}

func parseCurationFeed(data []byte) (curationFeed, error) {
	var feed curationFeed
	if err := json.Unmarshal(data, &feed); err != nil {
		return curationFeed{}, err
	}
	if feed.SchemaVersion > curationFeedVer && feed.SchemaVersion > 0 {
		return curationFeed{}, fmt.Errorf("feed schema %d is newer than this build", feed.SchemaVersion)
	}
	var bundles []curationBundle
	for _, bundle := range feed.Bundles {
		if err := validateCurationBundle(&bundle); err != nil {
			log.Printf("[curation] feed bundle skipped: %v", err)
			continue
		}
		bundles = append(bundles, bundle)
	}
	var profiles []curationProfile
	for _, profile := range feed.Profiles {
		if err := normalizeCurationProfile(&profile); err != nil {
			log.Printf("[curation] feed profile skipped: %v", err)
			continue
		}
		profile.Source = "feed"
		profiles = append(profiles, profile)
	}
	feed.Bundles = bundles
	feed.Profiles = profiles
	return feed, nil
}

func fetchCurationFeed() (curationFeed, error) {
	endpoint := curationFeedURL()
	if endpoint == "" {
		return curationFeed{}, errors.New("no curation feed configured")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return curationFeed{}, errors.New("curation feed URL is invalid")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return curationFeed{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return curationFeed{}, fmt.Errorf("feed returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return curationFeed{}, err
	}
	return parseCurationFeed(data)
}

// curationFeedCache keeps the fetch off the render thread. It is process-wide
// and refreshed at most once per panel open.
type curationFeedCacheState struct {
	mu      sync.Mutex
	feed    curationFeed
	err     string
	loading bool
	loaded  bool
	at      time.Time
}

var curationFeedCache curationFeedCacheState

func curationFeedSnapshot() (curationFeed, string, bool) {
	curationFeedCache.mu.Lock()
	defer curationFeedCache.mu.Unlock()
	return curationFeedCache.feed, curationFeedCache.err, curationFeedCache.loading
}

func refreshCurationFeedAsync() {
	if curationFeedURL() == "" {
		return
	}
	curationFeedCache.mu.Lock()
	if curationFeedCache.loading {
		curationFeedCache.mu.Unlock()
		return
	}
	curationFeedCache.loading = true
	curationFeedCache.mu.Unlock()
	go func() {
		feed, err := fetchCurationFeed()
		curationFeedCache.mu.Lock()
		curationFeedCache.loading = false
		curationFeedCache.loaded = true
		curationFeedCache.at = time.Now()
		if err != nil {
			curationFeedCache.err = firstLine(err.Error())
			log.Printf("[curation] feed: %v", err)
		} else {
			curationFeedCache.feed = feed
			curationFeedCache.err = ""
		}
		curationFeedCache.mu.Unlock()
	}()
}

// allCurationBundles combines the built-ins with the remote feed. Built-ins win
// on an id collision so a feed cannot replace the safety baseline.
func allCurationBundles() []curationBundle {
	bundles := builtinCurationBundles()
	feed, _, _ := curationFeedSnapshot()
	seen := map[string]bool{}
	for _, bundle := range bundles {
		seen[bundle.ID] = true
	}
	for _, bundle := range feed.Bundles {
		if seen[bundle.ID] {
			continue
		}
		seen[bundle.ID] = true
		bundles = append(bundles, bundle)
	}
	return bundles
}

// allCurationProfiles adds feed profiles after local ones. A local file with the
// same id still wins, because it was chosen on this device.
func allCurationProfiles() ([]curationProfile, []string) {
	profiles, issues := loadCurationProfiles()
	feed, _, _ := curationFeedSnapshot()
	seen := map[string]bool{}
	for _, profile := range profiles {
		seen[profile.ID] = true
	}
	for _, profile := range feed.Profiles {
		if seen[profile.ID] {
			continue
		}
		seen[profile.ID] = true
		profiles = append(profiles, profile)
	}
	return profiles, issues
}

// curationFeedLabel is the short status shown on the hub's home page.
func curationFeedLabel() string {
	if curationFeedURL() == "" {
		return "Built-in only"
	}
	feed, err, loading := curationFeedSnapshot()
	if loading {
		return "Refreshing…"
	}
	if err != "" {
		return "Offline"
	}
	if feed.Updated != "" {
		return "Updated " + feed.Updated
	}
	return "Community feed"
}
