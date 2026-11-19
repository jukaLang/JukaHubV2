package main

// shader_looks_tune.go — Settings → Emulation → Tune look: the knobs of the
// look a console (or every console) uses.
//
// A look's shader declares its knobs (`#pragma parameter`, read by
// shader_looks.go): a CRT's scanline strength and mask, an LCD grid's
// darkness, PT's Game system, pixel effect (Off, Grid or LCD Dot), grid
// lines, backing colour… RetroArch lets you change them only from its own
// menu while a game runs, and keeps them only if you save a preset. Here
// they are three rows under the Look row, following "Adjust for" like the
// rest of the page:
//
//	Tune look   which knob (Left/Right steps through them, A lists them all
//	            with their values)
//	Value       its value, Left/Right in the knob's own steps, clamped at its
//	            ends; the shader's default is marked
//	Reset look  back to the look's own values for this console
//
// The page is built from fixed rows rather than one row per knob: the
// Emulation section stays a static list, which keeps its rows reachable by
// voice and search (voicesettings.go skips dynamic sections).
//
// Values are kept per console and look in data/look-settings.json
// ({"values": {"game-boy|pt": {"PT_GRID_OPACITY": 0.4}}}, "all" for the
// All consoles scope) and written for one launch into the preset
// videofilter.go writes (lookLaunchParams). A console's own value wins over
// the all-consoles one; a knob nobody tuned keeps the preset's value. A knob
// a later shader version no longer declares is simply not written.

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	// lookTuneAllScope is the All consoles scope's key.
	lookTuneAllScope = "all"
	lookTuneFileName = "look-settings.json"
	lookTuneFileMax  = 256 << 10
	// lookTuneStopsMax bounds the stops of one knob's Value row: a knob with
	// finer steps than that is stepped in multiples of its step.
	lookTuneStopsMax = 400
)

// lookTuneStore holds the tuned values; loaded on first use (a small file).
var lookTuneStore struct {
	sync.Mutex
	loaded bool
	values map[string]map[string]float64 // "scope|look" → knob → value
}

// lookTuneWriteMu keeps the file writes in order.
var lookTuneWriteMu sync.Mutex

// lookTunePath is the file in JukaHub's data folder; a variable for tests.
var lookTunePath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, lookTuneFileName)
}

type lookTuneFile struct {
	Values map[string]map[string]float64 `json:"values"`
}

// lookTuneKey is a scope and look's key in the file.
func lookTuneKey(scope, look string) string { return scope + "|" + look }

// lookTuneLoadLocked reads the file once. The store's lock is held.
func lookTuneLoadLocked() {
	if lookTuneStore.loaded {
		return
	}
	lookTuneStore.loaded = true
	lookTuneStore.values = map[string]map[string]float64{}
	path := lookTunePath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > lookTuneFileMax {
		return
	}
	var file lookTuneFile
	if json.Unmarshal(raw, &file) != nil {
		logWarn("video", "%s is not readable; look tuning starts empty", path)
		return
	}
	for key, knobs := range file.Values {
		clean := map[string]float64{}
		for name, v := range knobs {
			if lookParamNameOK(name) && !math.IsNaN(v) && !math.IsInf(v, 0) {
				clean[name] = v
			}
		}
		if len(clean) > 0 && strings.Contains(key, "|") {
			lookTuneStore.values[key] = clean
		}
	}
}

// lookTuneValues is a copy of one scope and look's tuned values.
func lookTuneValues(scope, look string) map[string]float64 {
	lookTuneStore.Lock()
	defer lookTuneStore.Unlock()
	lookTuneLoadLocked()
	out := map[string]float64{}
	for k, v := range lookTuneStore.values[lookTuneKey(scope, look)] {
		out[k] = v
	}
	return out
}

// lookTuneHas reports whether a scope and look have any tuned value.
func lookTuneHas(scope, look string) bool {
	lookTuneStore.Lock()
	defer lookTuneStore.Unlock()
	lookTuneLoadLocked()
	return len(lookTuneStore.values[lookTuneKey(scope, look)]) > 0
}

// lookTuneSet stores one knob's value (drop: forget it instead) and saves.
func lookTuneSet(scope, look, name string, v float64, drop bool) {
	lookTuneStore.Lock()
	lookTuneLoadLocked()
	key := lookTuneKey(scope, look)
	knobs := lookTuneStore.values[key]
	if drop {
		delete(knobs, name)
		if len(knobs) == 0 {
			delete(lookTuneStore.values, key)
		}
	} else {
		if knobs == nil {
			knobs = map[string]float64{}
			lookTuneStore.values[key] = knobs
		}
		knobs[name] = v
	}
	lookTuneStore.Unlock()
	go lookTuneSave()
}

// lookTuneClear forgets every value of a scope and look, and saves.
func lookTuneClear(scope, look string) {
	lookTuneStore.Lock()
	lookTuneLoadLocked()
	delete(lookTuneStore.values, lookTuneKey(scope, look))
	lookTuneStore.Unlock()
	go lookTuneSave()
}

// lookTuneSave writes the file. Its own goroutine; the state is read under
// the write lock, so the last save always writes the latest values.
func lookTuneSave() {
	lookTuneWriteMu.Lock()
	defer lookTuneWriteMu.Unlock()
	path := lookTunePath()
	if path == "" {
		return
	}
	lookTuneStore.Lock()
	data, err := json.MarshalIndent(lookTuneFile{Values: lookTuneStore.values}, "", "  ")
	empty := len(lookTuneStore.values) == 0
	lookTuneStore.Unlock()
	if err != nil {
		return
	}
	if empty {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			logWarn("video", "cannot clear the look settings: %v", err)
		}
		return
	}
	if err := AtomicWrite(path, append(data, '\n'), 0o644); err != nil {
		logWarn("video", "cannot save the look settings: %v", err)
	}
}

// ── The rows ─────────────────────────────────────────────────────────────

// lookTuneSel is the knob the Value row edits ("" the first). Main thread.
var lookTuneSel string

// lookTuneScopeKey is the page's scope as the file keys it.
func lookTuneScopeKey() string {
	if raTuneScope == "" {
		return lookTuneAllScope
	}
	return raTuneSlug(raTuneScope)
}

// lookTuneTarget is the look the page's scope uses and its preset, or why
// there is nothing to tune. Main thread; reads no disk.
func lookTuneTarget(c *Config) (look, preset, why string) {
	var mode string
	auto := false
	if raTuneScope == "" {
		mode = videoFilterForConfig(c)
		if mode == videoFilterAuto {
			return "", "", "Auto picks a look per console: pick one in Adjust for to tune its look"
		}
	} else {
		mode = videoFilterSystemMode(c, raTuneScope)
		if mode == videoFilterAuto {
			auto = true
			mode = lookAutoFor(raTuneScope)
			if mode == "" {
				return "", "", "Auto gives " + raTuneScope + " no look: the GPU has no time for one"
			}
		}
	}
	if !videoFilterIsLook(mode) {
		return "", "", "Choose a look above to tune it"
	}
	probe, ok := videoFilterUIProbe()
	if !ok {
		return "", "", "Looking for shaders…"
	}
	found, has := probe.Looks[mode]
	switch {
	case !has, auto && found.Shader == "":
		// Auto uses a look's shader only, never a CPU filter (videoFilterResolveAuto).
		return "", "", videoFilterModeLabel(mode) + " is not installed: Download looks below"
	case found.Shader == "":
		return "", "", "The " + videoFilterModeLabel(mode) + " filter has no settings"
	}
	return mode, found.Shader, ""
}

// lookTuneParams is the target's knobs, or why there are none to show.
func lookTuneParams(c *Config) (look string, params []lookParam, why string) {
	look, preset, why := lookTuneTarget(c)
	if why != "" {
		return "", nil, why
	}
	params, ok := lookParamsPeek(preset)
	switch {
	case !ok:
		return "", nil, "Reading the look's settings…"
	case len(params) == 0:
		return "", nil, videoFilterModeLabel(look) + " has no settings to tune"
	}
	return look, params, ""
}

// lookTuneSelected is the knob the Value row edits.
func lookTuneSelected(params []lookParam) (lookParam, bool) {
	for _, p := range params {
		if p.Name == lookTuneSel {
			return p, true
		}
	}
	if len(params) > 0 {
		return params[0], true
	}
	return lookParam{}, false
}

// lookTuneConsoleName is the scope's console name, "" for All consoles.
func lookTuneConsoleName() string {
	if raTuneScope == "" {
		return ""
	}
	s, _ := raTuneSystemFor(raTuneScope)
	return s.Name
}

// lookTuneCurrent is a knob's value in the page's scope, and the value it
// would have without the scope's own tuning.
func lookTuneCurrent(look string, p lookParam) (cur, inherited float64) {
	sysName := lookTuneConsoleName()
	all := lookTuneValues(lookTuneAllScope, look)
	inherited = lookInherited(look, sysName, all, p)
	own := all
	if sysName != "" {
		own = lookTuneValues(raTuneSlug(sysName), look)
	}
	if v, ok := own[p.Name]; ok {
		return p.clamp(v), inherited
	}
	return inherited, inherited
}

// lookStepDecimals is how many decimals a knob's values show.
func lookStepDecimals(step float64) int {
	s := strconv.FormatFloat(step, 'f', -1, 64)
	i := strings.IndexByte(s, '.')
	if i < 0 {
		return 0
	}
	if d := len(s) - i - 1; d < 4 {
		return d
	}
	return 4
}

// lookParamDecimals is how many decimals a knob's values show: enough for its
// step, its minimum and its default (a range from 0.25 in steps of 0.5).
func lookParamDecimals(p lookParam) int {
	return max(lookStepDecimals(p.Step), lookStepDecimals(p.Min), lookStepDecimals(p.Default))
}

// lookParamStops are the values the Value row steps through: the range in the
// knob's steps (coarser when there would be too many), its ends, and the
// default.
func lookParamStops(p lookParam) []float64 {
	step := p.Step
	span := p.Max - p.Min
	if !(step > 0) || !(span > 0) || math.IsInf(span, 0) {
		return []float64{p.Default}
	}
	if r := span / step; !(r <= lookTuneStopsMax) {
		step = span / lookTuneStopsMax
	}
	n := int(math.Floor(span/step + 1e-6))
	decimals := lookParamDecimals(p)
	round := func(v float64) float64 {
		r, err := strconv.ParseFloat(strconv.FormatFloat(v, 'f', decimals, 64), 64)
		if err != nil {
			return v
		}
		return r
	}
	stops := make([]float64, 0, n+3)
	for i := 0; i <= n; i++ {
		stops = append(stops, round(p.Min+float64(i)*step))
	}
	if !lookSameValue(stops[len(stops)-1], p.Max) {
		stops = append(stops, p.Max)
	}
	hasDefault := false
	for _, s := range stops {
		if lookSameValue(s, p.Default) {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		stops = append(stops, p.Default)
		sort.Float64s(stops)
	}
	return stops
}

// lookFormatStop is a value as the row shows it.
func lookFormatStop(p lookParam, v float64) string {
	return strconv.FormatFloat(v, 'f', lookParamDecimals(p), 64)
}

func lookTuneParamOptions(c *Config) []setOption {
	look, params, why := lookTuneParams(c)
	if why != "" {
		return nil
	}
	out := make([]setOption, 0, len(params))
	for _, p := range params {
		cur, _ := lookTuneCurrent(look, p)
		note := lookFormatStop(p, cur)
		if lookSameValue(cur, p.Default) {
			note += " · default"
		}
		out = append(out, setOption{Value: p.Name, Label: p.Label, Note: note})
	}
	return out
}

func lookTuneValueOptions(c *Config) []setOption {
	_, params, why := lookTuneParams(c)
	if why != "" {
		return nil
	}
	p, ok := lookTuneSelected(params)
	if !ok {
		return nil
	}
	stops := lookParamStops(p)
	out := make([]setOption, 0, len(stops))
	for _, v := range stops {
		label := lookFormatStop(p, v)
		if lookSameValue(v, p.Default) {
			label += " · default"
		}
		out = append(out, setOption{Value: strconv.FormatFloat(v, 'g', -1, 64), Label: label})
	}
	return out
}

// lookTuneValueIndex is the stop nearest the knob's value.
func lookTuneValueIndex(c *Config) int {
	look, params, why := lookTuneParams(c)
	if why != "" {
		return -1
	}
	p, ok := lookTuneSelected(params)
	if !ok {
		return -1
	}
	cur, _ := lookTuneCurrent(look, p)
	best, bestD := -1, math.Inf(1)
	for i, v := range lookParamStops(p) {
		if d := math.Abs(v - cur); d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// lookTuneRows are the three rows under the Look row.
func lookTuneRows() []setRow {
	return []setRow{
		{
			ID: "raTuneLookParam", Label: "Tune look", Kind: setKindChoice,
			Detail: func(c *Config) string {
				look, params, why := lookTuneParams(c)
				if why != "" {
					return why
				}
				return videoFilterModeLabel(look) + " · " + pluralizeCount(len(params), "setting", "settings") + " · A lists them, Value below changes one"
			},
			Disabled: func(c *Config) bool {
				_, _, why := lookTuneParams(c)
				return why != ""
			},
			Options:    lookTuneParamOptions,
			Sheet:      lookTuneParamOptions,
			SheetTitle: "Tune look",
			SheetValue: func(c *Config) string {
				_, params, _ := lookTuneParams(c)
				p, _ := lookTuneSelected(params)
				return p.Name
			},
			Current: func(c *Config) int {
				_, params, why := lookTuneParams(c)
				if why != "" {
					return -1
				}
				p, _ := lookTuneSelected(params)
				for i, q := range params {
					if q.Name == p.Name {
						return i
					}
				}
				return -1
			},
			Value: func(c *Config) string {
				_, _, why := lookTuneParams(c)
				switch {
				case strings.HasSuffix(why, "…"):
					return "…"
				case why != "":
					return "Nothing to tune"
				}
				return "—"
			},
			Choose: func(_ *Config, value string) { lookTuneSel = value },
		},
		{
			ID: "raTuneLookValue", Label: "Value", Kind: setKindChoice, Clamp: true,
			Visible: func(c *Config) bool {
				_, _, why := lookTuneParams(c)
				return why == ""
			},
			Detail: func(c *Config) string {
				_, params, why := lookTuneParams(c)
				if why != "" {
					return why
				}
				p, _ := lookTuneSelected(params)
				what := p.Label + " · " + lookFormatStop(p, p.Min) + " to " + lookFormatStop(p, p.Max)
				if raTuneScope != "" {
					what += " · " + raTuneScope + " only"
				}
				return what
			},
			Options: lookTuneValueOptions,
			Current: lookTuneValueIndex,
			Choose: func(c *Config, value string) {
				look, params, why := lookTuneParams(c)
				if why != "" {
					return
				}
				p, ok := lookTuneSelected(params)
				v, err := strconv.ParseFloat(value, 64)
				if !ok || err != nil {
					return
				}
				v = p.clamp(v)
				_, inherited := lookTuneCurrent(look, p)
				lookTuneSet(lookTuneScopeKey(), look, p.Name, v, lookSameValue(v, inherited))
			},
		},
		{
			ID: "raTuneLookReset", Label: "Reset look", Kind: setKindAction,
			Visible: func(c *Config) bool {
				look, _, why := lookTuneTarget(c)
				return why == "" && lookTuneHas(lookTuneScopeKey(), look)
			},
			Detail: func(c *Config) string {
				look, _, _ := lookTuneTarget(c)
				return "Put " + videoFilterModeLabel(look) + "'s settings back for " + raTuneScopeName()
			},
			Open: func(c *Config) {
				look, _, why := lookTuneTarget(c)
				if why != "" {
					return
				}
				lookTuneClear(lookTuneScopeKey(), look)
				setToast(videoFilterModeLabel(look) + " settings reset for " + raTuneScopeName())
			},
		},
	}
}
