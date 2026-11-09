package main

// plugx.go — typed extension points for plugins.
//
// A plugin used to be a script with buttons: plugin.json named an entry and
// some actions, and that was all the rest of JukaHub could learn from it. The
// manifest now also declares what the plugin adds to the app itself, so the
// ecosystem can grow through contributions instead of only through updates:
//
//	"romParsers":    [{"name": "Pokémon mini", "system": "pokemini",
//	                   "extensions": [".min"], "magic": "4D494E", "offset": 0}]
//	"themes":        ["neon-grid.json"]           theme files, as in themes/
//	"inputProfiles": ["arcade-stick.jkinput"]     control profiles (inmap*.go)
//	"packs":         ["pack.json", "my-pack/"]    experience packs (xpack.go)
//
// Every key is optional, so every manifest written before them still loads
// exactly as it did. Each entry is validated on its own: a bad one is listed on
// the Plugins page as skipped and the rest of the plugin still works, and
// nothing here can stop JukaHub from starting — the scan runs after the first
// frame, safe mode skips it, and every file it reads is size-capped and must
// sit inside the plugin's own folder.
//
// Only plugins that are switched on contribute; switching one off withdraws
// its themes, ROM formats and packs on the next scan.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	// plugxMaxFileBytes caps a theme or input-profile file. Real ones are a
	// few kilobytes; a plugin that names a video by mistake must not be read
	// into memory on a 1 GB handheld.
	plugxMaxFileBytes = 256 << 10
	plugxMaxParsers   = 32
	plugxMaxThemes    = 16
	plugxMaxProfiles  = 16
	plugxMaxPacks     = 8
	// plugxMaxMagic and plugxMaxOffset bound a ROM header check: the longest
	// signature any cartridge format uses is well under 32 bytes, and the
	// deepest sits a few kilobytes in (the SNES internal header).
	plugxMaxMagic  = 32
	plugxMaxOffset = 1 << 20
	// plugxProfileExt is the control-profile file format the input profiles
	// read (inmap_profiles.go).
	plugxProfileExt = ".jkinput"
)

// plugxRomParser is one ROM format a plugin teaches the library: files with one
// of Extensions belong to System, optionally only when Magic appears at Offset.
// The game library (LIBRARY's ROM manager) consults plugxRomParsers or
// plugxRomParserFor for files its own catalogue does not know.
type plugxRomParser struct {
	Plugin     string   // folder name of the plugin that provides it
	Name       string   // display name ("Pokémon mini")
	System     string   // system id, a lower-case slug ("pokemini")
	Extensions []string // lower case, with the dot (".min")
	Magic      []byte   // optional header signature
	Offset     int64    // where Magic starts in the file
}

// plugxRomParserSpec is a romParsers entry as it is written in plugin.json.
type plugxRomParserSpec struct {
	Name       string   `json:"name"`
	System     string   `json:"system"`
	Extensions []string `json:"extensions"`
	Magic      string   `json:"magic"`
	Offset     int64    `json:"offset"`
}

// plugxTheme is one theme file a plugin ships, already validated.
type plugxTheme struct {
	Stem   string // the file name without extension, as a slug
	File   string // absolute path
	Preset ThemePreset
}

// plugxExtensions is everything one manifest adds beyond its actions.
type plugxExtensions struct {
	Plugin        string
	Dir           string
	RomParsers    []plugxRomParser
	Themes        []plugxTheme
	InputProfiles []string // absolute paths
	Packs         []string // absolute paths: a folder, a pack.json or another .json manifest
	Problems      []string // entries that were skipped, and why
}

// hasAny reports whether the manifest declares any working extension.
func (e plugxExtensions) hasAny() bool {
	return len(e.RomParsers) > 0 || len(e.Themes) > 0 || len(e.InputProfiles) > 0 || len(e.Packs) > 0
}

var (
	plugxSystemPattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`) })
	plugxExtPattern    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^\.[a-z0-9][a-z0-9_+-]{0,11}$`) })
)

// plugxParse reads the typed extension points out of a manifest. dir is the
// plugin's folder; every file an entry names must resolve inside it. A manifest
// that is not JSON was already reported by the loader, so it adds nothing here.
func plugxParse(plugin, dir string, data []byte) plugxExtensions {
	ext := plugxExtensions{Plugin: plugin, Dir: dir}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return ext
	}
	if v, ok := raw["romParsers"]; ok {
		ext.RomParsers = plugxParseRomParsers(plugin, v, &ext.Problems)
	}
	if v, ok := raw["themes"]; ok {
		for i, rel := range plugxStringList(v, "themes", &ext.Problems) {
			if i >= plugxMaxThemes {
				ext.Problems = append(ext.Problems, fmt.Sprintf("themes: only the first %d are used", plugxMaxThemes))
				break
			}
			theme, err := plugxLoadTheme(dir, rel)
			if err != nil {
				ext.Problems = append(ext.Problems, "theme "+rel+": "+err.Error())
				continue
			}
			ext.Themes = append(ext.Themes, theme)
		}
	}
	if v, ok := raw["inputProfiles"]; ok {
		for i, rel := range plugxStringList(v, "inputProfiles", &ext.Problems) {
			if i >= plugxMaxProfiles {
				ext.Problems = append(ext.Problems, fmt.Sprintf("inputProfiles: only the first %d are used", plugxMaxProfiles))
				break
			}
			path, err := plugxResolveFile(dir, rel, []string{plugxProfileExt})
			if err != nil {
				ext.Problems = append(ext.Problems, "control profile "+rel+": "+err.Error())
				continue
			}
			ext.InputProfiles = append(ext.InputProfiles, path)
		}
	}
	if v, ok := raw["packs"]; ok {
		for i, rel := range plugxStringList(v, "packs", &ext.Problems) {
			if i >= plugxMaxPacks {
				ext.Problems = append(ext.Problems, fmt.Sprintf("packs: only the first %d are used", plugxMaxPacks))
				break
			}
			path, err := plugxResolvePack(dir, rel)
			if err != nil {
				ext.Problems = append(ext.Problems, "pack "+rel+": "+err.Error())
				continue
			}
			ext.Packs = append(ext.Packs, path)
		}
	}
	return ext
}

// plugxStringList accepts a list of strings, or a single string for a list of
// one. Anything else is reported as a problem and contributes nothing.
func plugxStringList(raw json.RawMessage, field string, problems *[]string) []string {
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		var one string
		if err := json.Unmarshal(raw, &one); err != nil {
			*problems = append(*problems, field+": expected a list of file names")
			return nil
		}
		list = []string{one}
	}
	out := make([]string, 0, len(list))
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// plugxParseRomParsers validates the romParsers entries.
func plugxParseRomParsers(plugin string, raw json.RawMessage, problems *[]string) []plugxRomParser {
	var specs []plugxRomParserSpec
	if err := json.Unmarshal(raw, &specs); err != nil {
		*problems = append(*problems, "romParsers: expected a list of {name, system, extensions}")
		return nil
	}
	out := make([]plugxRomParser, 0, len(specs))
	for i, spec := range specs {
		if len(out) >= plugxMaxParsers {
			*problems = append(*problems, fmt.Sprintf("romParsers: only the first %d are used", plugxMaxParsers))
			break
		}
		p, err := plugxValidateRomParser(plugin, spec)
		if err != nil {
			label := strings.TrimSpace(spec.Name)
			if label == "" {
				label = "#" + strconv.Itoa(i+1)
			}
			*problems = append(*problems, "ROM format "+label+": "+err.Error())
			continue
		}
		out = append(out, p)
	}
	return out
}

// plugxValidateRomParser checks one romParsers entry and normalises it.
func plugxValidateRomParser(plugin string, spec plugxRomParserSpec) (plugxRomParser, error) {
	system := strings.ToLower(strings.TrimSpace(spec.System))
	if !plugxSystemPattern().MatchString(system) {
		return plugxRomParser{}, fmt.Errorf("system %q is not a short lower-case id", spec.System)
	}
	p := plugxRomParser{
		Plugin: plugin,
		Name:   strings.TrimSpace(spec.Name),
		System: system,
		Offset: spec.Offset,
	}
	if p.Name == "" {
		p.Name = system
	}
	if len([]rune(p.Name)) > 40 {
		p.Name = string([]rune(p.Name)[:40])
	}
	seen := map[string]bool{}
	for _, e := range spec.Extensions {
		e = strings.ToLower(strings.TrimSpace(e))
		if e != "" && !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		if !plugxExtPattern().MatchString(e) {
			return plugxRomParser{}, fmt.Errorf("extension %q is not like \".min\"", e)
		}
		if !seen[e] {
			seen[e] = true
			p.Extensions = append(p.Extensions, e)
		}
	}
	if len(p.Extensions) == 0 {
		return plugxRomParser{}, fmt.Errorf("names no file extensions")
	}
	if magic := strings.ReplaceAll(strings.TrimSpace(spec.Magic), " ", ""); magic != "" {
		b, err := hex.DecodeString(magic)
		if err != nil || len(b) == 0 {
			return plugxRomParser{}, fmt.Errorf("magic %q is not hex bytes", spec.Magic)
		}
		if len(b) > plugxMaxMagic {
			return plugxRomParser{}, fmt.Errorf("magic is longer than %d bytes", plugxMaxMagic)
		}
		p.Magic = b
	}
	if p.Offset < 0 || p.Offset > plugxMaxOffset {
		return plugxRomParser{}, fmt.Errorf("offset %d is outside 0..%d", p.Offset, plugxMaxOffset)
	}
	return p, nil
}

// plugxResolveFile resolves rel inside dir and requires a small regular file
// with one of the allowed extensions.
func plugxResolveFile(dir, rel string, exts []string) (string, error) {
	path, err := resolveWithinRoot(dir, rel)
	if err != nil {
		return "", fmt.Errorf("is outside the plugin folder")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("file not found")
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("is not a file")
	}
	if info.Size() > plugxMaxFileBytes {
		return "", fmt.Errorf("is larger than %s", humanBytes(plugxMaxFileBytes))
	}
	ok := false
	got := strings.ToLower(filepath.Ext(path))
	for _, e := range exts {
		if got == e {
			ok = true
			break
		}
	}
	if !ok {
		return "", fmt.Errorf("must be a %s file", strings.Join(exts, " or "))
	}
	return path, nil
}

// plugxResolvePack resolves a packs entry: a folder holding pack.json, or a
// .json manifest. A .jkpack archive has to be unpacked by the author first —
// unzipping on every scan is exactly the work a plugin scan must not do.
func plugxResolvePack(dir, rel string) (string, error) {
	path, err := resolveWithinRoot(dir, rel)
	if err != nil {
		return "", fmt.Errorf("is outside the plugin folder")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("not found")
	}
	if info.IsDir() {
		if _, err := os.Stat(filepath.Join(path, xpackManifestName)); err != nil {
			return "", fmt.Errorf("the folder has no %s", xpackManifestName)
		}
		return path, nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		if info.Size() > xpackMaxManifestBytes {
			return "", fmt.Errorf("is larger than %s", humanBytes(xpackMaxManifestBytes))
		}
		return path, nil
	case xpackArchiveExt:
		return "", fmt.Errorf("unpack the .jkpack into a folder inside the plugin")
	}
	return "", fmt.Errorf("must be a pack folder or a .json manifest")
}

// plugxLoadTheme reads and validates one theme file of a plugin.
func plugxLoadTheme(dir, rel string) (plugxTheme, error) {
	path, err := plugxResolveFile(dir, rel, []string{".json"})
	if err != nil {
		return plugxTheme{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return plugxTheme{}, fmt.Errorf("cannot be read")
	}
	var file themeFile
	if err := json.Unmarshal(data, &file); err != nil {
		return plugxTheme{}, fmt.Errorf("is not a theme file (%v)", err)
	}
	preset, err := file.toPreset()
	if err != nil {
		return plugxTheme{}, err
	}
	stem := plugxSlug(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	if stem == "" {
		return plugxTheme{}, fmt.Errorf("the file name cannot be a theme id")
	}
	return plugxTheme{Stem: stem, File: path, Preset: preset}, nil
}

// plugxSlug turns a file stem into a theme id: lower case, letters, digits and
// dashes only.
func plugxSlug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	return out
}

// plugxDescribe builds the Plugins page's summary of what a plugin adds and the
// card's lines about each extension and each skipped entry.
func plugxDescribe(ext plugxExtensions) (string, []string) {
	var parts, lines []string
	plural := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return strconv.Itoa(n) + " " + many
	}
	if n := len(ext.Themes); n > 0 {
		parts = append(parts, plural(n, "theme", "themes"))
		for _, t := range ext.Themes {
			kind := "Theme"
			if t.Preset.Kind == themeKindConversion {
				kind = "Total conversion"
			}
			lines = append(lines, kind+": "+t.Preset.Name)
		}
	}
	if n := len(ext.RomParsers); n > 0 {
		parts = append(parts, plural(n, "ROM format", "ROM formats"))
		for _, p := range ext.RomParsers {
			line := "ROM format: " + p.Name + " (" + strings.Join(p.Extensions, ", ") + ")"
			if len(p.Magic) > 0 {
				line += " · checks the header"
			}
			lines = append(lines, line)
		}
	}
	if n := len(ext.InputProfiles); n > 0 {
		parts = append(parts, plural(n, "control profile", "control profiles"))
		for _, path := range ext.InputProfiles {
			lines = append(lines, "Control profile: "+strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
		}
	}
	if n := len(ext.Packs); n > 0 {
		parts = append(parts, plural(n, "experience pack", "experience packs"))
		for _, path := range ext.Packs {
			name := xpackPeekName(path)
			if name == "" {
				name = filepath.Base(path)
			}
			lines = append(lines, "Experience pack: "+name)
		}
	}
	for _, p := range ext.Problems {
		lines = append(lines, "Skipped "+p)
	}
	return strings.Join(parts, " · "), lines
}

// plugxDecorate adds a manifest's extensions to its Plugins page row. A plugin
// that only adds things (a theme, a ROM format) needs no script, so a missing
// entry is not a problem for it.
func plugxDecorate(row *plugEntry, info PluginInfo, ext plugxExtensions) {
	if row == nil {
		return
	}
	row.Provides, row.ExtLines = plugxDescribe(ext)
	if strings.TrimSpace(info.Entry) == "" && ext.hasAny() {
		row.Problem = ""
	}
}

// ── Registry ──────────────────────────────────────────────────────────────

// plugxRegistry holds what the switched-on plugins add. The ROM formats are
// read from the library's scan goroutine, so they sit behind a lock; themes are
// registered into the theme catalogue on the main thread, where it is read.
var plugxRegistry struct {
	mu      sync.RWMutex
	parsers []plugxRomParser
}

// plugxThemeOwners maps every theme id plugx registered to its plugin, so a
// rescan can withdraw the themes of a plugin that was switched off and never
// touches a built-in or a theme file of the user's own. Main thread only.
var plugxThemeOwners = map[string]string{}

// plugxInputRegistered remembers the profile files already handed over this
// session, so a rescan (every visit to the Plugins page) does not register the
// same file again.
var plugxInputRegistered = map[string]bool{}

// plugxRegisterProfile hands one control-profile file to the input profiles
// (INPUT's inmapRegisterProfileFile). init sets it; tests replace it.
var plugxRegisterProfile func(path string) error

func init() {
	// Set here rather than by the declaration, so no variable initialiser
	// reaches into the input profiles (see inmap_actions.go on why).
	plugxRegisterProfile = inmapRegisterProfileFile
}

// plugxInstall makes the switched-on plugins' extensions live, and adds what
// went wrong while installing them to their rows in catalog.
func plugxInstall(config *Config, found []plugxExtensions, catalog []plugEntry) {
	var parsers []plugxRomParser
	var packs []xpackPluginRef
	for _, ext := range found {
		parsers = append(parsers, ext.RomParsers...)
		for _, path := range ext.Packs {
			packs = append(packs, xpackPluginRef{Plugin: ext.Plugin, Path: path})
		}
	}
	sort.SliceStable(parsers, func(i, j int) bool {
		if parsers[i].Plugin != parsers[j].Plugin {
			return parsers[i].Plugin < parsers[j].Plugin
		}
		return parsers[i].Name < parsers[j].Name
	})
	plugxRegistry.mu.Lock()
	plugxRegistry.parsers = parsers
	plugxRegistry.mu.Unlock()

	plugxSyncThemes(config, found)
	notes := plugxSyncInputProfiles(found)
	for i := range catalog {
		catalog[i].ExtLines = append(catalog[i].ExtLines, notes[catalog[i].Name]...)
	}
	xpackSetPluginPacks(config, packs)
}

// plugxSyncThemes registers the themes of the switched-on plugins in the theme
// catalogue and withdraws the ones no plugin provides any more. The theme in
// use is kept until the next start even if its plugin was switched off: pulling
// the palette out from under the screen would leave the app half repainted.
func plugxSyncThemes(config *Config, found []plugxExtensions) {
	captureBuiltinThemeIDs()
	want := map[string]plugxTheme{}
	owner := map[string]string{}
	for _, ext := range found {
		for _, th := range ext.Themes {
			id := th.Stem
			if plugxThemeTaken(id, ext.Plugin) || want[id].File != "" {
				id = plugxSlug(ext.Plugin + "-" + th.Stem)
			}
			if id == "" || plugxThemeTaken(id, ext.Plugin) || want[id].File != "" {
				continue
			}
			want[id] = th
			owner[id] = ext.Plugin
		}
	}
	active := themePresetName(config)
	for id := range plugxThemeOwners {
		if _, keep := want[id]; keep || id == active {
			continue
		}
		forgetThemePreset(id)
		delete(plugxThemeOwners, id)
	}
	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	repaint := false
	for _, id := range ids {
		_, existed := themePresets[id]
		themePresets[id] = want[id].Preset
		if !existed {
			themePresetOrder = append(themePresetOrder, id)
			// A theme chosen on an earlier run comes back here, after the
			// boot-time repaint already fell back to the default palette.
			if id == active {
				repaint = true
			}
		}
		plugxThemeOwners[id] = owner[id]
	}
	if repaint {
		ApplyThemeColors(GetThemePreset(active))
		SetAccentColor(themeAccentColor())
		ClearTextCache()
		ClearShapeCache()
		ClearIconAtlas()
		clearHomeTextCache()
	}
}

// plugxThemeTaken reports whether id belongs to something other than plugin: a
// built-in palette, a theme file in themes/, or another plugin's theme.
func plugxThemeTaken(id, plugin string) bool {
	if isBuiltinThemeID(id) {
		return true
	}
	if _, exists := themePresets[id]; !exists {
		return false
	}
	return plugxThemeOwners[id] != plugin
}

// plugxSyncInputProfiles hands new profile files to the input-profile store
// (INPUT's inmapRegisterProfileFile) and returns, per plugin, the files it
// refused. There is no way to take a profile back, so a profile of a plugin
// switched off stays available until JukaHub restarts.
func plugxSyncInputProfiles(found []plugxExtensions) map[string][]string {
	notes := map[string][]string{}
	for _, ext := range found {
		for _, path := range ext.InputProfiles {
			if plugxInputRegistered[path] {
				continue
			}
			if err := plugxRegisterProfile(path); err != nil {
				notes[ext.Plugin] = append(notes[ext.Plugin], "Skipped control profile "+filepath.Base(path)+": "+firstLine(err.Error()))
				continue
			}
			plugxInputRegistered[path] = true
		}
	}
	return notes
}

// plugxReadHeader reads up to n bytes from the start of a file.
func plugxReadHeader(path string, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, n)
	got, _ := io.ReadFull(f, buf)
	return buf[:got]
}
