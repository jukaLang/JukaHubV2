package main

// shader_looks.go — which look a game gets, and what a look's files say.
//
// A look is a shader (or a software filter) over the game: scanlines, an LCD
// grid, a CRT, or SkyWalker541's PT ("pixel transparency": the Game Boy
// screen's see-through pixels over its backing, made for the TrimUI Brick).
// videofilter.go finds the files and writes the one-launch preset; this file
// decides which look a launch uses and reads the presets.
//
// Which look: the game's own choice (its options sheet) wins, then the
// console's (Settings → Emulation with a console picked, stored as
// raTuning.<console>.look like the console's overlay), then the all-consoles
// choice ("videoFilter", also on the Games page as Video filter). That last
// one may be Auto, which picks by console:
//
//	PT                 Game Boy, Game Boy Color, Game Boy Advance
//	CRT                the TV consoles: NES, SNES, Mega Drive, Master System,
//	                   PC Engine, the Ataris, PlayStation, Sega CD, 32X, Neo
//	                   Geo, arcade
//	LCD grid           the other handhelds: Game Gear, Lynx, WonderSwan, Neo
//	                   Geo Pocket
//	nothing            PSP, N64, DS, Dreamcast, Saturn, Jaguar: the A133's GPU
//	                   has no time left over for a shader on those
//
// A look whose files are not on the card falls back to RetroArch's own
// picture for that launch (logged, no message): Auto never asks anything of
// a card that has no shaders. A console the library does not name (a plain
// folder of ROMs) gets the all-consoles choice, with Auto meaning nothing.
//
// What a preset says: a .glslp names its passes (shaderN = path), its
// textures (textures = "a;b" then a = path) and, for RetroArch's "simple
// presets" (1.9.1+), the full preset it builds on (#reference "path") plus
// parameter values that override that preset's (NAME = "value"). A pass's
// .glsl declares the knobs it has: `#pragma parameter NAME "Label" default
// min max [step]`; RetroArch takes a missing step as a tenth of the range
// (video_shader_parse.c). Both parsers are pure and bounded; the readers only
// ever read a few small files (lookFileMax each, lookRefDepth presets deep).
//
// Tuned values (shader_looks_tune.go) go into the one-launch preset as
// `NAME = "value"` lines after its #reference line, which RetroArch applies
// on top of the referenced preset (libretro docs, "Simple Presets"). Before
// any tuning, PT starts from the right Game system for the two Game Boys
// (lookSeedParams).

import (
	"math"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	// videoFilterAuto is the all-consoles choice that picks a look by console.
	videoFilterAuto = "auto"

	// lookFileMax bounds one preset or shader file read from the card (the
	// largest .glsl in libretro's pack is well under 100 KB).
	lookFileMax = 1 << 20
	// lookRefDepth bounds a chain of #reference presets.
	lookRefDepth = 4
	// lookPassMax bounds the passes read for parameters.
	lookPassMax = 16
	// lookParamMax bounds the parameters one look lists.
	lookParamMax = 64
)

// lookAutoByConsole is Auto's look per console (raTuneSystems' names). A
// console not listed gets none.
var lookAutoByConsole = map[string]string{
	"Game Boy":             "pt",
	"Game Boy Color":       "pt",
	"Game Boy Advance":     "pt",
	"NES":                  "crt",
	"SNES":                 "crt",
	"Mega Drive":           "crt",
	"Master System":        "crt",
	"PC Engine":            "crt",
	"Atari 2600":           "crt",
	"Atari 5200":           "crt",
	"Atari 7800":           "crt",
	"PlayStation":          "crt",
	"Sega CD":              "crt",
	"32X":                  "crt",
	"Arcade":               "crt",
	"Neo Geo":              "crt",
	"Neo Geo CD":           "crt",
	"Game Gear":            "lcd",
	"Atari Lynx":           "lcd",
	"WonderSwan":           "lcd",
	"WonderSwan Color":     "lcd",
	"Neo Geo Pocket":       "lcd",
	"Neo Geo Pocket Color": "lcd",
}

// videoFilterAutoNote is the line under Auto in the choice lists.
const videoFilterAutoNote = "PT on Game Boys, CRT on TV consoles, LCD on other handhelds, none on PSP, N64, DS, Saturn and Dreamcast"

// lookAutoFor is Auto's look for a console, "" for none.
func lookAutoFor(system string) string {
	if strings.TrimSpace(system) == "" {
		return ""
	}
	sys, _ := raTuneSystemFor(system)
	return lookAutoByConsole[sys.Name]
}

// lookSystemForROM names the console a ROM belongs to from the folders it
// sits in (Roms/GBA/x.gba, a NextUI "Game Boy Advance (GBA)" folder,
// Roms/PS/Game/disc.cue), else from an extension only one console uses. It
// reads no disk. "" when neither says. Only a console name or a TAG the
// tables know counts: a game's own folder, "Crash Bandicoot (USA)", reads
// like a NextUI "Name (TAG)" folder, and the search goes on above it.
func lookSystemForROM(romPath string) string {
	romPath = strings.TrimSpace(romPath)
	if romPath == "" {
		return ""
	}
	dir := filepath.Dir(romPath)
	for i := 0; i < 3; i++ {
		base := filepath.Base(dir)
		if s, ok := romSystemAliases[strings.ToLower(base)]; ok && s.Name != "" {
			return s.Name
		}
		if _, tag, tagged := nextuiFolderTag(base); tagged {
			if s, ok := nextuiTagSystem(tag); ok && s.Name != "" {
				return s.Name
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if key, ok := filesROMExtSystem[strings.ToLower(filepath.Ext(romPath))]; ok {
		if s, ok := romSystemAliases[key]; ok {
			return s.Name
		}
	}
	return ""
}

// ── Choosing the look ────────────────────────────────────────────────────

// videoFilterConsoleLook is one console's own look, "" when it follows the
// all-consoles choice. Auto is not a per-console choice (it already is one).
func videoFilterConsoleLook(c *Config, system string) string {
	if strings.TrimSpace(system) == "" {
		return ""
	}
	sys, _ := raTuneSystemFor(system)
	v := normaliseGameVideoFilter(raTuneRaw(c, raTuneConsoleKey(sys.Name, "look")))
	if v == videoFilterAuto {
		return ""
	}
	return v
}

// videoFilterSystemMode is the look a console's games get without a choice
// of their own: the console's, else the all-consoles one (possibly Auto).
func videoFilterSystemMode(c *Config, system string) string {
	if own := videoFilterConsoleLook(c, system); own != "" {
		return own
	}
	return videoFilterForConfig(c)
}

// videoFilterAutoMode resolves Auto for a console: its look when has says the
// files are there (nil: assume they are), else RetroArch's own picture.
func videoFilterAutoMode(system string, has func(look string) bool) string {
	look := lookAutoFor(system)
	if look == "" || (has != nil && !has(look)) {
		return "unmanaged"
	}
	return look
}

// videoFilterResolve is the mode a launch uses: the game's own choice (own,
// as stored), else the console's, else the all-consoles one, with Auto
// resolved. Never "auto".
func videoFilterResolve(c *Config, own, system string, has func(look string) bool) string {
	mode, _ := videoFilterResolveAuto(c, own, system, has)
	return mode
}

// videoFilterResolveAuto is videoFilterResolve, and whether Auto picked the
// mode. Auto only ever uses a look's shader: a card with only RetroArch's
// CPU filter for a look (Blargg NTSC for the CRT…) keeps its own picture, as
// a filter would slow a PlayStation or arcade game nobody chose it for.
func videoFilterResolveAuto(c *Config, own, system string, has func(look string) bool) (string, bool) {
	mode := normaliseGameVideoFilter(own)
	if mode == "" {
		mode = videoFilterSystemMode(c, system)
	}
	if mode == videoFilterAuto {
		return videoFilterAutoMode(system, has), true
	}
	return mode, false
}

// videoFilterUIHas reports whether the menus' probe found a look's shader
// (Auto's test); while the probe runs, every look counts as there.
func videoFilterUIHas(look string) bool {
	probe, ok := videoFilterUIProbe()
	if !ok {
		return true
	}
	return probe.Looks[look].Shader != ""
}

// videoFilterApplied is the look the last library launch applied, for the
// speed check (shader_speed.go), which runs right after.
var videoFilterApplied struct {
	mu   sync.Mutex
	rom  string
	mode string
}

func videoFilterNoteApplied(rom, mode string) {
	videoFilterApplied.mu.Lock()
	videoFilterApplied.rom, videoFilterApplied.mode = rom, mode
	videoFilterApplied.mu.Unlock()
}

// videoFilterLaunchMode is the look rom's launch applied, else the one it
// would get now.
func videoFilterLaunchMode(rom string) string {
	videoFilterApplied.mu.Lock()
	r, m := videoFilterApplied.rom, videoFilterApplied.mode
	videoFilterApplied.mu.Unlock()
	if r == rom && m != "" {
		return m
	}
	return videoFilterForGame(rom)
}

// ── Presets and parameters ───────────────────────────────────────────────

// lookPreset is what a .glslp says.
type lookPreset struct {
	Reference string   // #reference path as written ("" for a full preset)
	Shaders   []string // shaderN paths as written, in pass order
	Textures  []string // texture paths as written
	// Values are the other key = value lines: pass settings, and in a simple
	// preset the parameter values it sets.
	Values map[string]string
}

// lookPresetValue unquotes a preset value; an unquoted one ends at a '#'.
func lookPresetValue(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, `"`) {
		if end := strings.IndexByte(v[1:], '"'); end >= 0 {
			return v[1 : end+1]
		}
		return strings.TrimSpace(v[1:])
	}
	if i := strings.IndexByte(v, '#'); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// lookParsePreset reads a .glslp (or .slangp) preset. Pure.
func lookParsePreset(text string) lookPreset {
	p := lookPreset{Values: map[string]string{}}
	passes := map[int]string{}
	count := -1
	var textureNames []string
	for _, line := range strings.Split(strings.TrimPrefix(text, "\ufeff"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			// "#reference path", not a comment that starts with the word.
			if rest, ok := strings.CutPrefix(line, "#reference"); ok && p.Reference == "" && rest != "" && strings.ContainsRune(" \t\"", rune(rest[0])) {
				p.Reference = lookPresetValue(rest)
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = lookPresetValue(value)
		switch {
		case key == "":
			continue
		case key == "shaders":
			if n, err := strconv.Atoi(value); err == nil && n >= 0 {
				count = n
			}
		case key == "textures":
			for _, name := range strings.Split(value, ";") {
				if name = strings.TrimSpace(name); name != "" {
					textureNames = append(textureNames, name)
				}
			}
		case strings.HasPrefix(key, "shader") && len(key) > len("shader"):
			n, err := strconv.Atoi(key[len("shader"):])
			if err != nil || n < 0 || n >= lookPassMax || value == "" {
				p.Values[key] = value
				continue
			}
			passes[n] = value
		default:
			p.Values[key] = value
		}
	}
	for n := 0; n < lookPassMax; n++ {
		if count >= 0 && n >= count {
			break
		}
		s, ok := passes[n]
		if !ok {
			if count >= 0 {
				continue
			}
			break
		}
		p.Shaders = append(p.Shaders, s)
	}
	for _, name := range textureNames {
		if v := p.Values[name]; v != "" {
			p.Textures = append(p.Textures, v)
		}
	}
	return p
}

// lookParam is one knob a shader declares.
type lookParam struct {
	Name    string
	Label   string
	Default float64
	Min     float64
	Max     float64
	Step    float64
}

// clamp keeps v inside the knob's range.
func (p lookParam) clamp(v float64) float64 {
	if v < p.Min {
		return p.Min
	}
	if v > p.Max {
		return p.Max
	}
	return v
}

// lookParamNameOK is a name that can be written back as a preset key.
func lookParamNameOK(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	for _, r := range name {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// lookParseFloat reads a number as shaders write them ("1.0", "0.5f").
func lookParseFloat(s string) (float64, bool) {
	s = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(s), "f"), "F")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// lookParseParams lists the `#pragma parameter` lines of a shader source:
// NAME "Label" default min max [step]. A name seen twice keeps its first
// declaration (RetroArch's rule); a line it cannot read is skipped. Pure.
func lookParseParams(text string) []lookParam {
	var out []lookParam
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "#pragma")
		if !ok {
			continue
		}
		rest = strings.TrimLeft(rest, " \t")
		if rest, ok = strings.CutPrefix(rest, "parameter"); !ok || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
			continue
		}
		rest = strings.TrimSpace(rest)
		end := strings.IndexAny(rest, " \t")
		if end < 0 {
			continue
		}
		name := rest[:end]
		rest = strings.TrimSpace(rest[end:])
		if !strings.HasPrefix(rest, `"`) {
			continue
		}
		q := strings.IndexByte(rest[1:], '"')
		if q < 0 {
			continue
		}
		label := strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, rest[1:q+1])
		nums := strings.Fields(rest[q+2:])
		if len(nums) < 3 || !lookParamNameOK(name) || seen[name] {
			continue
		}
		var vals [4]float64
		good := true
		for i := 0; i < 4 && i < len(nums); i++ {
			v, ok := lookParseFloat(nums[i])
			if !ok {
				good = i == 3 // a step that is not a number: as if it were missing
				nums = nums[:i]
				break
			}
			vals[i] = v
		}
		if !good {
			continue
		}
		p := lookParam{Name: name, Label: strings.TrimSpace(label), Default: vals[0], Min: vals[1], Max: vals[2]}
		if p.Max < p.Min {
			continue
		}
		if len(nums) >= 4 {
			p.Step = vals[3]
		}
		if p.Step <= 0 {
			// A tenth of the range, as RetroArch takes it, without the
			// float noise (0.30000000000000004).
			p.Step, _ = strconv.ParseFloat(strconv.FormatFloat(0.1*(p.Max-p.Min), 'g', 6, 64), 64)
		}
		if math.IsInf(p.Max-p.Min, 0) || math.IsInf(p.Step, 0) || math.IsNaN(p.Step) {
			continue // a range no row can step through
		}
		if p.Label == "" {
			p.Label = name
		}
		p.Default = p.clamp(p.Default)
		seen[name] = true
		out = append(out, p)
		if len(out) >= lookParamMax {
			break
		}
	}
	return out
}

// lookResolve is a preset's path to another file: relative to the preset's
// folder unless absolute.
func lookResolve(dir, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return filepath.Clean(filepath.FromSlash(p))
	}
	return filepath.Join(dir, filepath.FromSlash(p))
}

// lookReadSmall reads a preset or shader file, bounded.
func lookReadSmall(p string) (string, bool) {
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() || info.Size() > lookFileMax {
		return "", false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// lookLoadParams reads the knobs of the look a preset gives: the passes of
// the full preset at the end of its #reference chain, with the values each
// preset on the way sets as their defaults (the outermost wins). It reads the
// card.
func lookLoadParams(preset string) []lookParam {
	overrides := map[string]string{}
	at := preset
	for depth := 0; depth <= lookRefDepth && at != ""; depth++ {
		text, ok := lookReadSmall(at)
		if !ok {
			logDebug("video", "cannot read the shader preset %s", at)
			return nil
		}
		p := lookParsePreset(text)
		for k, v := range p.Values {
			if _, set := overrides[k]; !set {
				overrides[k] = v
			}
		}
		if p.Reference != "" {
			at = lookResolve(filepath.Dir(at), p.Reference)
			continue
		}
		var params []lookParam
		seen := map[string]bool{}
		for _, s := range p.Shaders {
			src, ok := lookReadSmall(lookResolve(filepath.Dir(at), s))
			if !ok {
				continue
			}
			for _, prm := range lookParseParams(src) {
				if !seen[prm.Name] && len(params) < lookParamMax {
					seen[prm.Name] = true
					params = append(params, prm)
				}
			}
		}
		for i := range params {
			if v, ok := lookParseFloat(overrides[params[i].Name]); ok {
				params[i].Default = params[i].clamp(v)
			}
		}
		return params
	}
	return nil
}

// lookParamCache is the knobs of each preset the menus asked about, read in
// the background so the Emulation page never waits for the card.
var lookParamCache struct {
	mu      sync.Mutex
	gen     int
	byPath  map[string][]lookParam
	running map[string]bool
}

// lookParamsPeek is the menus' look at a preset's knobs; ok is false while
// they are read (the first call starts that).
func lookParamsPeek(preset string) ([]lookParam, bool) {
	if preset == "" {
		return nil, true
	}
	lookParamCache.mu.Lock()
	defer lookParamCache.mu.Unlock()
	if p, ok := lookParamCache.byPath[preset]; ok {
		return p, true
	}
	if lookParamCache.running == nil {
		lookParamCache.running = map[string]bool{}
	}
	if !lookParamCache.running[preset] {
		lookParamCache.running[preset] = true
		gen := lookParamCache.gen
		go func() {
			params := lookLoadParams(preset)
			lookParamCache.mu.Lock()
			defer lookParamCache.mu.Unlock()
			delete(lookParamCache.running, preset)
			if gen != lookParamCache.gen {
				return
			}
			if lookParamCache.byPath == nil {
				lookParamCache.byPath = map[string][]lookParam{}
			}
			lookParamCache.byPath[preset] = params
		}()
	}
	return nil, false
}

// lookParamsForget drops what the menus read: new files arrived.
func lookParamsForget() {
	lookParamCache.mu.Lock()
	lookParamCache.gen++
	lookParamCache.byPath = nil
	lookParamCache.running = nil
	lookParamCache.mu.Unlock()
}

// lookSeedParams are the values a console's look starts from before any
// tuning. PT's Game system: 1 is Game Boy / Pocket and 2 Game Boy Color (the
// shader's default), per SkyWalker541's README; the Advance's number was not
// documented where it could be checked, so it keeps the shader's default.
// A seed is used only when the shader declares the knob and the value is in
// its range.
var lookSeedParams = map[string]map[string]map[string]float64{
	"pt": {
		"Game Boy":       {"PT_GAME_SYSTEM": 1},
		"Game Boy Color": {"PT_GAME_SYSTEM": 2},
	},
}

// lookParamValue is one knob's value written for a launch.
type lookParamValue struct {
	Name  string
	Value float64
}

// lookFormatValue is a value as a preset line holds it.
func lookFormatValue(v float64) string {
	return strconv.FormatFloat(v, 'f', 6, 64)
}

// lookSameValue compares two knob values.
func lookSameValue(a, b float64) bool {
	return math.Abs(a-b) < 1e-6
}

// lookInherited is what a knob is for a console before the console's own
// tuning: the seed, else the all-consoles tuning, else the shader's default.
// sysName "" is the all-consoles scope itself.
func lookInherited(look, sysName string, all map[string]float64, p lookParam) float64 {
	if sysName != "" {
		if v, ok := lookSeedParams[look][sysName][p.Name]; ok && v >= p.Min && v <= p.Max {
			return v
		}
		if v, ok := all[p.Name]; ok {
			return p.clamp(v)
		}
	}
	return p.Default
}

// lookLaunchParams are the knob values a launch writes for a look on a
// console: the console's own tuning, else the seed, else the all-consoles
// tuning; only knobs the shader declares, clamped, and only those that differ
// from the preset's own value. It reads the card.
func lookLaunchParams(system, look, preset string) []lookParamValue {
	params := lookLoadParams(preset)
	if len(params) == 0 {
		return nil
	}
	sysName := ""
	if strings.TrimSpace(system) != "" {
		s, _ := raTuneSystemFor(system)
		sysName = s.Name
	}
	all := lookTuneValues(lookTuneAllScope, look)
	// A game whose console nobody can tell gets the All consoles tuning
	// (lookInherited gives only the preset's value without a console).
	own := all
	if sysName != "" {
		own = lookTuneValues(raTuneSlug(sysName), look)
	}
	var out []lookParamValue
	for _, p := range params {
		v, ok := own[p.Name]
		if ok {
			v = p.clamp(v)
		} else {
			v = lookInherited(look, sysName, all, p)
		}
		if !lookSameValue(v, p.Default) {
			out = append(out, lookParamValue{Name: p.Name, Value: v})
		}
	}
	return out
}

// lookRepoJoin joins a path a preset names onto the preset's folder inside a
// shader pack (slash paths). It refuses absolute paths and anything that
// leaves the pack.
func lookRepoJoin(dir, p string) (string, bool) {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	if p == "" || strings.HasPrefix(p, "/") || (len(p) >= 2 && p[1] == ':') {
		return "", false
	}
	joined := path.Clean(path.Join(dir, p))
	if joined == "." || joined == ".." || strings.HasPrefix(joined, "../") || unsafeArchiveName(joined) {
		return "", false
	}
	return joined, true
}
