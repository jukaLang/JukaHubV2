package main

// videofilter.go — the picture RetroArch draws for games started from JukaHub:
// sharp pixels, smoothed, pixel perfect, or a look (scanlines, an LCD grid, a
// CRT) when the files for it are on the card.
//
// Settings → the games page has the global choice ("videoFilter" in the custom
// map); the game options sheet (game_options_ui.go) can override it per game,
// stored in data/game-options.json as "video_filter". Nothing is written for
// good: like the OSD preset (osdpreset.go), every value goes into
// retroarch.cfg through raScopeApply (racfgscope.go) just before a library
// launch, and the user's own values come back when RetroArch exits. RetroArch
// started from the firmware's menu keeps its own picture.
//
// The three plain modes are ordinary retroarch.cfg keys that RetroArch reads
// at start:
//
//	Sharp          video_smooth = false, video_scale_integer = false
//	Smooth         video_smooth = true,  video_scale_integer = false
//	Pixel perfect  video_smooth = false, video_scale_integer = true
//
// and each turns shaders off for the launch (video_shader_enable = false) and
// clears a software filter the user had set, so the picture really is the one
// named on the row.
//
// The looks need files, and are offered only when the files exist:
//
//   - A shader preset. RetroArch has IGNORED the video_shader key in
//     retroarch.cfg since 1.7.8 (libretro forum "CFG file parameter
//     video_shader = not working", RetroArch issue #9904): a shader is loaded at
//     start only from an automatic preset — game, content folder, core, then
//     global — in the menu config directory (rgui_config_directory, by default
//     "config" beside retroarch.cfg). So a look writes
//     <config dir>/global.glslp (global.slangp on the glcore/vulkan/d3d
//     drivers) for this launch only: a marker line and `#reference "<preset>"`
//     (RetroArch's "simple preset" form), with video_shader_enable and
//     auto_shaders_enable set to true through the scoped edit. The file is
//     removed once RetroArch has exited, and at the next start if JukaHub died
//     mid-game; only a file still carrying the marker is ever removed, and a
//     global preset the user saved themselves is never overwritten (the look
//     is skipped for that launch, and logged). A core or game preset the user
//     saved wins over the global one, which is RetroArch's own rule and the
//     right one.
//   - A software filter (video_filter = …/filters/video/<name>.filt), a plain
//     config key that RetroArch loads at start. Filters run on the CPU and only
//     with software-rendered cores; a shader is preferred when both exist.
//
// TrimUI's ra64.trimui drives GLES, so its shaders are .glslp (shaders_glsl).
// The install is probed once — the shader folders (video_shader_dir, shaders/,
// .retroarch/shaders, …) and the filter folders (video_filter_dir,
// filters/video) — and cached per config; the Settings row and the sheet use a
// background probe so the first open never stalls a frame.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	videoFilterConfigKey = "videoFilter"

	// videoFilterPresetMarker is the first line of every auto preset JukaHub
	// writes; only a file that still starts with it is removed.
	videoFilterPresetMarker = "# JukaHub: written for one game started from JukaHub; removed when RetroArch exits"
	// videoFilterPendingName lists the presets written and not yet removed, in
	// JukaHub's data folder: the record a crash leaves for the next start.
	videoFilterPendingName = "video-filter-presets.json"

	// The shader scan is bounded: a full shaders_glsl tree is a few thousand
	// files, and a card full of something else must not stall a launch.
	videoFilterScanDepth = 4
	videoFilterScanMax   = 8000

	// videoFilterStartWait is how long the exit watcher waits for the game to
	// take the screen before deciding the launch never started.
	videoFilterStartWait = 30 * time.Second
	videoFilterPoll      = 2 * time.Second
	videoFilterWatchMax  = 8 * time.Hour
)

// videoFilterBasicModes need no files; videoFilterLookModes need a shader or a
// filter on the card.
var (
	videoFilterBasicModes = []string{"unmanaged", "sharp", "smooth", "pixel"}
	videoFilterLookModes  = []string{"scanlines", "lcd", "crt"}
)

// videoFilterCandidates is a variable so tests could point at a temporary
// config instead of the developer's own RetroArch.
var videoFilterCandidates = retroArchConfigCandidates

func videoFilterIsLook(mode string) bool {
	for _, m := range videoFilterLookModes {
		if m == mode {
			return true
		}
	}
	return false
}

// normaliseVideoFilterMode maps a stored value onto a known mode; anything
// else leaves RetroArch alone.
func normaliseVideoFilterMode(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, m := range videoFilterBasicModes {
		if v == m {
			return v
		}
	}
	if videoFilterIsLook(v) {
		return v
	}
	return "unmanaged"
}

// normaliseGameVideoFilter is the per-game field: "" follows Settings, any
// known mode (including "unmanaged") overrides it, and a value nobody knows (a
// hand edit, a newer build's mode) follows Settings too.
func normaliseGameVideoFilter(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" || (v != "unmanaged" && normaliseVideoFilterMode(v) == "unmanaged") {
		return ""
	}
	return v
}

func videoFilterModeLabel(mode string) string {
	switch normaliseVideoFilterMode(mode) {
	case "sharp":
		return "Sharp"
	case "smooth":
		return "Smooth"
	case "pixel":
		return "Pixel perfect"
	case "scanlines":
		return "Scanlines"
	case "lcd":
		return "LCD grid"
	case "crt":
		return "CRT"
	}
	return "RetroArch's own"
}

// videoFilterForConfig is the global choice; a missing key leaves RetroArch's
// picture alone, as every build before this one did.
func videoFilterForConfig(config *Config) string {
	if config == nil {
		return "unmanaged"
	}
	if v, ok := customValue(config.Variables.Custom, videoFilterConfigKey); ok {
		return normaliseVideoFilterMode(coerceString(v))
	}
	return "unmanaged"
}

// videoFilterForGame is the mode a launch of romPath uses: the game's own
// choice, else the global one.
func videoFilterForGame(romPath string) string {
	if own := gameOptionsFor(romPath).VideoFilter; own != "" {
		return normaliseVideoFilterMode(own)
	}
	return videoFilterForConfig(appConfig)
}

// ── Probe: which looks this RetroArch has ────────────────────────────────

// videoFilterLook is the file one look uses: a shader preset for the auto
// preset, or a software filter for video_filter.
type videoFilterLook struct {
	Shader string
	Filter string
}

// note is the second line the choice list shows under a look.
func (l videoFilterLook) note() string {
	if l.Shader != "" {
		return strings.TrimSuffix(filepath.Base(l.Shader), filepath.Ext(l.Shader)) + " shader"
	}
	return strings.TrimSuffix(filepath.Base(l.Filter), filepath.Ext(l.Filter)) + " filter"
}

// videoFilterProbe is what one RetroArch install offers.
type videoFilterProbe struct {
	ConfigDir string // the menu config directory the auto preset goes in
	Ext       string // "glslp" or "slangp", from video_driver
	Looks     map[string]videoFilterLook
}

var videoFilterProbes struct {
	mu   sync.Mutex
	byCf map[string]videoFilterProbe
	// ui is the probe of the default install for the Settings row and the
	// sheet, filled in the background.
	ui        videoFilterProbe
	uiDone    bool
	uiRunning bool
}

// videoFilterExpandDir resolves a retroarch.cfg directory value: ":" is the
// RetroArch folder, "~" is HOME (which the TrimUI launchers point at the
// RetroArch folder), "default" or "" is unset.
func videoFilterExpandDir(value, cfgPath string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "default") {
		return ""
	}
	install := raInstallDir(cfgPath)
	switch {
	case strings.HasPrefix(value, ":"):
		value = filepath.Join(install, strings.TrimLeft(value[1:], `/\`))
	case value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`):
		home, _ := os.UserHomeDir()
		if home == "" || !strings.HasPrefix(filepath.Clean(cfgPath), filepath.Clean(home)+string(filepath.Separator)) {
			home = install
		}
		value = filepath.Join(home, strings.TrimLeft(value[1:], `/\`))
	case !filepath.IsAbs(value):
		value = filepath.Join(install, value)
	}
	return filepath.Clean(value)
}

// videoFilterShaderExt is the preset type the configured driver loads: GLSL for
// gl/gles, slang for the newer drivers.
func videoFilterShaderExt(driver string) string {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "glcore", "vulkan", "d3d10", "d3d11", "d3d12", "metal":
		return "slangp"
	}
	return "glslp"
}

// videoFilterShaderPrefs and videoFilterFilterPrefs are the files picked first
// for each look (lower-case base names without extension): the light shaders
// a handheld GPU runs at full speed before the generic matches.
var (
	videoFilterShaderPrefs = map[string][]string{
		"scanlines": {"scanline", "scanlines", "zfast-scanlines", "scanline-fract"},
		"lcd":       {"zfast-lcd", "zfast_lcd", "lcd3x", "lcd-grid-v2", "lcd-grid", "lcd1x", "dot"},
		"crt":       {"zfast-crt", "zfast_crt", "crt-pi", "fakelottes", "crt-easymode", "crt-aperture", "crt-geom"},
	}
	videoFilterFilterPrefs = map[string][]string{
		"scanlines": {"scanline2x"},
		"lcd":       {"lcd3x", "grid3x", "grid2x", "gameboy3x", "dot_matrix_3x"},
		"crt":       {"phosphor2x", "blargg_ntsc_snes_composite"},
	}
)

// videoFilterGenericMatch is the fallback when no preferred file exists. CRT
// shaders that need a desktop GPU are left out: on the A133P they turn a game
// into a slide show.
func videoFilterGenericMatch(look, base string, filter bool) bool {
	switch look {
	case "scanlines":
		return strings.Contains(base, "scanline")
	case "lcd":
		if strings.Contains(base, "lcd") || strings.Contains(base, "dot-matrix") || strings.Contains(base, "dot_matrix") {
			return true
		}
		return filter && (strings.Contains(base, "grid") || strings.Contains(base, "gameboy"))
	case "crt":
		if filter {
			return strings.Contains(base, "ntsc") || strings.Contains(base, "phosphor") || strings.Contains(base, "crt")
		}
		if !strings.Contains(base, "crt") {
			return false
		}
		for _, heavy := range []string{"royale", "guest", "mega", "deluxe", "hyllian", "geom-mini-hd"} {
			if strings.Contains(base, heavy) {
				return false
			}
		}
		return true
	}
	return false
}

// videoFilterPick chooses a file for a look from files (sorted): a preferred
// name first, then the shortest generic match.
func videoFilterPick(look string, files []string, prefs []string, filter bool) string {
	byBase := map[string]string{}
	for _, f := range files {
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(f), filepath.Ext(f)))
		if _, seen := byBase[base]; !seen {
			byBase[base] = f
		}
	}
	for _, p := range prefs {
		if f, ok := byBase[p]; ok {
			return f
		}
	}
	best := ""
	for _, f := range files {
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(f), filepath.Ext(f)))
		if videoFilterGenericMatch(look, base, filter) && (best == "" || len(f) < len(best)) {
			best = f
		}
	}
	return best
}

// videoFilterScan lists files with ext under the roots, at most depth folders
// deep. Shader folders for another language (shaders_cg, shaders_slang when
// GLSL is wanted) are skipped rather than walked.
func videoFilterScan(roots []string, ext string, depth int) []string {
	var out []string
	seen := map[string]bool{}
	wantDir := "shaders_glsl"
	if ext == ".slangp" {
		wantDir = "shaders_slang"
	}
	count := 0
	var walk func(dir string, level int)
	walk = func(dir string, level int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			count++
			if count > videoFilterScanMax {
				return
			}
			name := e.Name()
			full := filepath.Join(dir, name)
			if e.IsDir() {
				lower := strings.ToLower(name)
				if level >= depth || (strings.HasPrefix(name, ".") && lower != ".retroarch") {
					continue
				}
				if strings.HasPrefix(lower, "shaders_") && lower != wantDir {
					continue
				}
				walk(full, level+1)
				continue
			}
			if strings.EqualFold(filepath.Ext(name), ext) {
				out = append(out, full)
			}
		}
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		key := strings.ToLower(filepath.Clean(root))
		if seen[key] {
			continue
		}
		seen[key] = true
		walk(root, 0)
	}
	sort.Strings(out)
	return out
}

// videoFilterProbeConfig probes the install a config belongs to. It reads the
// card and nothing else.
func videoFilterProbeConfig(cfgPath string) videoFilterProbe {
	probe := videoFilterProbe{Ext: "glslp", Looks: map[string]videoFilterLook{}}
	if cfgPath == "" {
		return probe
	}
	raw, _ := os.ReadFile(cfgPath)
	cfg := string(raw)
	install := raInstallDir(cfgPath)
	cfgDir := filepath.Dir(cfgPath)
	get := func(key string) string {
		v, _ := raConfigGet(cfg, key)
		return videoFilterExpandDir(v, cfgPath)
	}
	if driver, ok := raConfigGet(cfg, "video_driver"); ok {
		probe.Ext = videoFilterShaderExt(driver)
	}
	probe.ConfigDir = get("rgui_config_directory")
	if probe.ConfigDir == "" {
		probe.ConfigDir = filepath.Join(cfgDir, "config")
	}

	shaderRoots := []string{
		get("video_shader_dir"),
		filepath.Join(install, "shaders"),
		filepath.Join(install, ".retroarch", "shaders"),
		filepath.Join(cfgDir, "shaders"),
	}
	filterRoots := []string{
		get("video_filter_dir"),
		filepath.Join(install, "filters", "video"),
		filepath.Join(install, ".retroarch", "filters", "video"),
		filepath.Join(cfgDir, "filters", "video"),
	}
	shaders := videoFilterScan(shaderRoots, "."+probe.Ext, videoFilterScanDepth)
	filters := videoFilterScan(filterRoots, ".filt", 1)
	for _, look := range videoFilterLookModes {
		if s := videoFilterPick(look, shaders, videoFilterShaderPrefs[look], false); s != "" {
			probe.Looks[look] = videoFilterLook{Shader: s}
		} else if f := videoFilterPick(look, filters, videoFilterFilterPrefs[look], true); f != "" {
			probe.Looks[look] = videoFilterLook{Filter: f}
		}
	}
	logDebug("video", "%s: %d %s presets, %d filters, %d looks", install, len(shaders), probe.Ext, len(filters), len(probe.Looks))
	return probe
}

// videoFilterProbeFor is the cached probe of one config, for the launch path
// (which may wait for the scan).
func videoFilterProbeFor(cfgPath string) videoFilterProbe {
	videoFilterProbes.mu.Lock()
	if p, ok := videoFilterProbes.byCf[cfgPath]; ok {
		videoFilterProbes.mu.Unlock()
		return p
	}
	videoFilterProbes.mu.Unlock()
	p := videoFilterProbeConfig(cfgPath)
	videoFilterProbes.mu.Lock()
	if videoFilterProbes.byCf == nil {
		videoFilterProbes.byCf = map[string]videoFilterProbe{}
	}
	videoFilterProbes.byCf[cfgPath] = p
	videoFilterProbes.mu.Unlock()
	return p
}

// videoFilterUIProbe is the default install's probe for the menus. The first
// call starts the scan in the background and answers "not ready"; the rows
// show the plain modes until it is done.
func videoFilterUIProbe() (videoFilterProbe, bool) {
	videoFilterProbes.mu.Lock()
	defer videoFilterProbes.mu.Unlock()
	if videoFilterProbes.uiDone {
		return videoFilterProbes.ui, true
	}
	if !videoFilterProbes.uiRunning {
		videoFilterProbes.uiRunning = true
		go func() {
			p := videoFilterProbe{Ext: "glslp", Looks: map[string]videoFilterLook{}}
			if targets := raScopeTargets(videoFilterCandidates()); len(targets) > 0 {
				p = videoFilterProbeFor(targets[0])
			}
			videoFilterProbes.mu.Lock()
			videoFilterProbes.ui = p
			videoFilterProbes.uiDone = true
			videoFilterProbes.uiRunning = false
			videoFilterProbes.mu.Unlock()
		}()
	}
	return videoFilterProbe{}, false
}

// videoFilterChoices is the modes a menu offers: the plain ones always, a look
// only when its files were found.
func videoFilterChoices() []string {
	out := append([]string(nil), videoFilterBasicModes...)
	if probe, ok := videoFilterUIProbe(); ok {
		for _, look := range videoFilterLookModes {
			if _, has := probe.Looks[look]; has {
				out = append(out, look)
			}
		}
	}
	return out
}

// videoFilterChoiceNote is the line under a mode in the choice list.
func videoFilterChoiceNote(mode string) string {
	switch mode {
	case "unmanaged":
		return "Leave RetroArch's own picture settings alone"
	case "sharp":
		return "Nearest pixels, no smoothing or shader"
	case "smooth":
		return "Bilinear smoothing, no shader"
	case "pixel":
		return "Whole-number scaling with sharp pixels"
	}
	if probe, ok := videoFilterUIProbe(); ok {
		if look, has := probe.Looks[mode]; has {
			return look.note()
		}
	}
	return ""
}

// videoFilterMissing reports a stored look whose files the probe did not find
// (deleted since, or chosen on another card), so a row can say so.
func videoFilterMissing(mode string) bool {
	if !videoFilterIsLook(mode) {
		return false
	}
	probe, ok := videoFilterUIProbe()
	if !ok {
		return false
	}
	_, has := probe.Looks[mode]
	return !has
}

// ── Settings row (setmodel.go) ───────────────────────────────────────────

func videoFilterSettingDetail(*Config) string {
	const what = "RetroArch's picture for games started from JukaHub"
	probe, ok := videoFilterUIProbe()
	switch {
	case !ok:
		return what + " · looking for shaders…"
	case len(probe.Looks) == 0:
		return what + " · no shaders or filters found for looks"
	}
	return what
}

func videoFilterSettingOptions(*Config) []setOption {
	modes := videoFilterChoices()
	out := make([]setOption, 0, len(modes))
	for _, m := range modes {
		out = append(out, setOption{Value: m, Label: videoFilterModeLabel(m), Note: videoFilterChoiceNote(m)})
	}
	return out
}

func videoFilterSettingIndex(c *Config) int {
	cur := videoFilterForConfig(c)
	for i, m := range videoFilterChoices() {
		if m == cur {
			return i
		}
	}
	return -1
}

// videoFilterSettingChoose stores the global choice and returns the toast.
func videoFilterSettingChoose(c *Config, value string) string {
	mode := normaliseVideoFilterMode(value)
	if c == nil {
		return "Video filter: " + videoFilterModeLabel(mode)
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom[videoFilterConfigKey] = mode
	syncVariableOverrides(c)
	return "Video filter: " + videoFilterModeLabel(mode)
}

// ── Game options sheet (game_options_ui.go) ──────────────────────────────

// videoFilterGameChoices is the per-game ladder: "" (follow Settings), then
// every mode the menus offer.
func videoFilterGameChoices() []string {
	return append([]string{""}, videoFilterChoices()...)
}

// cycleGameVideoFilter steps the per-game ladder without wrapping. A stored
// look that is not on the ladder (its files are gone) steps from the start.
func cycleGameVideoFilter(cur string, delta int) string {
	choices := videoFilterGameChoices()
	idx := -1
	for i, m := range choices {
		if m == cur {
			idx = i
		}
	}
	if idx < 0 {
		idx = 0
	}
	idx += delta
	if idx < 0 {
		idx = 0
	}
	if idx >= len(choices) {
		idx = len(choices) - 1
	}
	return choices[idx]
}

// gameVideoFilterValue is the sheet's right-hand value.
func gameVideoFilterValue(own string) string {
	if own == "" {
		return "Default · " + videoFilterModeLabel(videoFilterForConfig(appConfig))
	}
	label := videoFilterModeLabel(own)
	if videoFilterMissing(own) {
		label += " · not installed"
	}
	return label
}

// ── Launch ───────────────────────────────────────────────────────────────

// videoFilterPlainValues are the keys a plain mode writes.
func videoFilterPlainValues(mode string) []raScopeValue {
	smooth, integer := "false", "false"
	switch mode {
	case "smooth":
		smooth = "true"
	case "pixel":
		integer = "true"
	}
	return []raScopeValue{
		{Key: "video_smooth", Value: smooth},
		{Key: "video_scale_integer", Value: integer},
		{Key: "video_shader_enable", Value: "false"},
	}
}

// prepareVideoFilter is the launch-time entry point, beside prepareOSDPreset.
// It never blocks a launch: a missing file or a read-only card is logged and
// the game starts with RetroArch's own picture. Standalone emulators are
// skipped.
func prepareVideoFilter(launcher, romPath string) bool {
	if !retroArchLauncher(launcher) {
		return false
	}
	mode := videoFilterForGame(romPath)
	if mode == "unmanaged" {
		return false
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, videoFilterCandidates()))
	if len(targets) == 0 {
		return false
	}
	var values []raScopeValue
	keepFilter := false
	if videoFilterIsLook(mode) {
		probe := videoFilterProbeFor(targets[0])
		look, ok := probe.Looks[mode]
		switch {
		case !ok:
			logWarn("video", "%s look chosen, but this RetroArch has no shader or filter for it; launching with its own picture", mode)
			return false
		case look.Shader != "":
			if !videoFilterWritePreset(probe, look.Shader) {
				return false
			}
			values = []raScopeValue{
				{Key: "video_shader_enable", Value: "true"},
				{Key: "auto_shaders_enable", Value: "true"},
			}
		default:
			values = []raScopeValue{
				{Key: "video_filter", Value: look.Filter},
				{Key: "video_shader_enable", Value: "false"},
			}
			keepFilter = true
		}
	} else {
		values = videoFilterPlainValues(mode)
	}
	configured := false
	for _, path := range targets {
		vals := append([]raScopeValue(nil), values...)
		if !keepFilter {
			// A software filter the user set would sit under the chosen
			// picture: cleared for this launch only, and put back after.
			if raw, err := os.ReadFile(path); err == nil {
				if v, had := raConfigGet(string(raw), "video_filter"); had && strings.TrimSpace(v) != "" {
					vals = append(vals, raScopeValue{Key: "video_filter", Value: ""})
				}
			}
		}
		changed, err := raScopeApply(path, vals)
		if err != nil {
			logWarn("video", "cannot apply the %s picture in %s: %v", mode, path, err)
			continue
		}
		configured = true
		if changed {
			Log().Info("video filter applied for this launch", "mode", mode, "config", path)
		}
	}
	return configured
}

// ── The one-launch auto preset ───────────────────────────────────────────

// videoFilterPending guards the record of written presets, and the watcher
// that removes them once RetroArch is gone. gen counts writes, so a watcher
// that finished waiting while a new launch wrote a preset waits again instead
// of removing the file the new game is about to read.
var videoFilterPending struct {
	mu      sync.Mutex
	gen     int
	running bool
}

// videoFilterPendingPath is the record in JukaHub's data folder; a variable
// for tests.
var videoFilterPendingPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, videoFilterPendingName)
}

type videoFilterPendingFile struct {
	Presets []string `json:"presets"`
}

func videoFilterPendingLoadLocked() []string {
	path := videoFilterPendingPath()
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 64<<10 {
		return nil
	}
	var file videoFilterPendingFile
	if json.Unmarshal(raw, &file) != nil {
		return nil
	}
	return file.Presets
}

func videoFilterPendingSaveLocked(presets []string) error {
	path := videoFilterPendingPath()
	if path == "" {
		if len(presets) == 0 {
			return nil
		}
		return os.ErrNotExist
	}
	if len(presets) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(videoFilterPendingFile{Presets: presets}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := writeFileSynced(tmp, append(data, '\n'), 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// videoFilterOurPreset reports whether a preset file is one JukaHub wrote.
func videoFilterOurPreset(raw []byte) bool {
	return strings.HasPrefix(strings.TrimPrefix(string(raw), "\ufeff"), videoFilterPresetMarker)
}

// videoFilterPresetText is the auto preset for a shader: RetroArch's simple
// preset form, which loads the referenced preset as if it were this one.
func videoFilterPresetText(shader string) string {
	return videoFilterPresetMarker + "\n#reference \"" + filepath.ToSlash(shader) + "\"\n"
}

// videoFilterWritePreset writes the global auto preset for one launch. It is
// recorded before it is written, so a power cut in between leaves something
// to clean up rather than a shader on every firmware launch after.
func videoFilterWritePreset(probe videoFilterProbe, shader string) bool {
	if probe.ConfigDir == "" || shader == "" {
		return false
	}
	path := filepath.Join(probe.ConfigDir, "global."+probe.Ext)
	if raw, err := os.ReadFile(path); err == nil && !videoFilterOurPreset(raw) {
		logInfo("video", "%s is the user's own global shader preset; it is kept and the look is skipped for this launch", path)
		return false
	}
	videoFilterPending.mu.Lock()
	defer videoFilterPending.mu.Unlock()
	presets := videoFilterPendingLoadLocked()
	listed := false
	for _, p := range presets {
		if p == path {
			listed = true
		}
	}
	if !listed {
		if err := videoFilterPendingSaveLocked(append(presets, path)); err != nil {
			logWarn("video", "cannot record the one-launch shader preset (%v); launching without the look", err)
			return false
		}
	}
	if err := os.MkdirAll(probe.ConfigDir, 0o755); err != nil {
		logWarn("video", "cannot create %s: %v", probe.ConfigDir, err)
		return false
	}
	tmp := path + ".jukahub-tmp"
	if err := writeFileSynced(tmp, []byte(videoFilterPresetText(shader)), 0o644); err != nil {
		_ = os.Remove(tmp)
		logWarn("video", "cannot write %s: %v", path, err)
		return false
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		logWarn("video", "cannot write %s: %v", path, err)
		return false
	}
	videoFilterPending.gen++
	if !videoFilterPending.running {
		videoFilterPending.running = true
		go videoFilterWatchExit()
	}
	return true
}

// videoFilterCleanupLocked removes every recorded preset that still carries the
// marker. One the user replaced (saved their own global preset over it in the
// game) is theirs now and stays.
func videoFilterCleanupLocked() {
	presets := videoFilterPendingLoadLocked()
	if len(presets) == 0 {
		return
	}
	var keep []string
	for _, path := range presets {
		raw, err := os.ReadFile(path)
		if err != nil || !videoFilterOurPreset(raw) {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			logWarn("video", "cannot remove the one-launch shader preset %s: %v", path, err)
			keep = append(keep, path)
			continue
		}
		logDebug("video", "removed the one-launch shader preset %s", path)
	}
	if err := videoFilterPendingSaveLocked(keep); err != nil {
		logWarn("video", "cannot update the shader preset record: %v", err)
	}
}

// videoFilterCleanup is the startup repair: presets a crash or power cut left
// behind go, so RetroArch started from the firmware has its own picture again.
func videoFilterCleanup() {
	videoFilterPending.mu.Lock()
	defer videoFilterPending.mu.Unlock()
	if videoFilterPending.running {
		return // a launch is in flight; its watcher cleans up
	}
	videoFilterCleanupLocked()
}

// videoFilterWatchExit waits for the game to take the screen and then for it,
// and RetroArch itself, to be gone, then removes the preset. A launch that
// never takes the screen is over after videoFilterStartWait.
func videoFilterWatchExit() {
	for {
		videoFilterPending.mu.Lock()
		gen := videoFilterPending.gen
		videoFilterPending.mu.Unlock()

		start := time.Now()
		for !gameScreenSuspended() && !retroArchRunning() && time.Since(start) < videoFilterStartWait {
			time.Sleep(videoFilterPoll / 4)
		}
		for (gameScreenSuspended() || retroArchRunning()) && time.Since(start) < videoFilterWatchMax {
			time.Sleep(videoFilterPoll)
		}

		videoFilterPending.mu.Lock()
		if gen != videoFilterPending.gen {
			videoFilterPending.mu.Unlock()
			continue // a newer launch wrote a preset meanwhile: wait for that one
		}
		videoFilterCleanupLocked()
		videoFilterPending.running = false
		videoFilterPending.mu.Unlock()
		return
	}
}

func init() {
	// After the first frame, beside the retroarch.cfg scope repair.
	deferStartupTask("video-filter", func() { go videoFilterCleanup() })
}
