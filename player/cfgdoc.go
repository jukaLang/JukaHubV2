package main

// cfgdoc.go — config as code: every setting you changed, as one versioned file.
//
// JukaHub's settings live in two places — the design (jukaconfig.json, which an
// update keeps) and your overrides (jukauser.json) — so "what did I change?"
// had no answer short of reading both. The config document answers it:
//
//	{
//	  "schemaVersion": 1,
//	  "generatedBy": "JukaHub v0.4.0",
//	  "device":   {"model": "trimui-smart-pro", "platform": "trimui-linux", …},
//	  "defaults": {"source": "trimui/jukaconfig.device.json", "stamp": "…", …},
//	  "sections": {
//	    "input_profiles":     {"swapABButtons": true},
//	    "ui_layouts":         {"home": {"homeArrangement": "grid"}, "scenes": […]},
//	    "theme":              {"theme_preset": "nord", …},
//	    "behavior_overrides": {"weatherUnit": "F", "groq_api_key": "<redacted>"}
//	  }
//	}
//
// Every key is one that differs from what this build ships — the shipped
// configs are compiled in (below), because the jukaconfig.json on the card is a
// user file that updates keep and so cannot say what the current version ships.
// Secrets (API keys, tokens, passwords) never leave the device: their key is
// listed so you can see you set one, with "<redacted>" for the value, and an
// import never writes a "<redacted>" over the real one.
//
// The Config inspector (cfgdocui.go) shows the same diff, resets a key to its
// default, exports the file, imports one (validated, previewed, accepted key by
// key, merged through the normal save path, with a backup first), and says
// which defaults a new version changed since you last looked.

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cfgdocShippedDesktop is the config the desktop build ships, compiled in.
//
//go:embed jukaconfig.json
var cfgdocShippedDesktop []byte

// cfgdocShippedDevice is the handheld's: package-sdcard.sh copies it to
// jukaconfig.json, so it is what every TrimUI install started from.
//
//go:embed trimui/jukaconfig.device.json
var cfgdocShippedDevice []byte

const (
	cfgdocSchemaVersion = 1
	cfgdocFileName      = "jukahub-config.json"
	cfgdocRedacted      = "<redacted>"
	cfgdocMaxDocBytes   = 1 << 20
	// cfgdocDefaultsFile remembers the shipped defaults this install last
	// looked at, so a new version can say which defaults it changed.
	cfgdocDefaultsFile = "cfgdoc-defaults.json"
)

// The document's sections, in the order the inspector lists them.
const (
	cfgdocSectionInput    = "input_profiles"
	cfgdocSectionLayouts  = "ui_layouts"
	cfgdocSectionTheme    = "theme"
	cfgdocSectionBehavior = "behavior_overrides"
)

var cfgdocSectionOrder = []string{cfgdocSectionInput, cfgdocSectionLayouts, cfgdocSectionTheme, cfgdocSectionBehavior}

// cfgdocSectionTitle is a section's heading in the inspector.
func cfgdocSectionTitle(section string) string {
	switch section {
	case cfgdocSectionInput:
		return "Input profiles"
	case cfgdocSectionLayouts:
		return "Layouts"
	case cfgdocSectionTheme:
		return "Theme"
	}
	return "Behaviour"
}

// What a change is, compared with the shipped defaults.
const (
	cfgdocAdded   = "added"
	cfgdocChanged = "changed"
	cfgdocRemoved = "removed"
)

// cfgdocDocument is jukahub-config.json.
type cfgdocDocument struct {
	SchemaVersion int                 `json:"schemaVersion"`
	GeneratedBy   string              `json:"generatedBy"`
	GeneratedAt   string              `json:"generatedAt"`
	Device        cfgdocDevice        `json:"device"`
	Defaults      cfgdocDefaultsStamp `json:"defaults"`
	Sections      cfgdocSections      `json:"sections"`
	Removed       []string            `json:"removed,omitempty"`
}

// cfgdocDevice says where a document was made.
type cfgdocDevice struct {
	Model    string `json:"model"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

// cfgdocDefaultsStamp names the shipped defaults a document was compared with:
// the build, a short hash of them, and the default of every key it mentions.
type cfgdocDefaultsStamp struct {
	Source  string                 `json:"source"`
	Version string                 `json:"version"`
	Stamp   string                 `json:"stamp"`
	Values  map[string]interface{} `json:"values,omitempty"`
}

// cfgdocSections holds the changed keys, sorted into the four sections.
type cfgdocSections struct {
	InputProfiles     map[string]interface{} `json:"input_profiles"`
	UILayouts         cfgdocLayouts          `json:"ui_layouts"`
	Theme             map[string]interface{} `json:"theme"`
	BehaviorOverrides map[string]interface{} `json:"behavior_overrides"`
}

// cfgdocLayouts is the ui_layouts section: the Home settings, and the scenes of
// the design file that differ from the shipped ones.
type cfgdocLayouts struct {
	Home          map[string]interface{} `json:"home"`
	Scenes        []json.RawMessage      `json:"scenes,omitempty"`
	RemovedScenes []string               `json:"removedScenes,omitempty"`
}

// cfgdocChange is one line of the diff.
type cfgdocChange struct {
	Key     string
	Section string
	Kind    string      // cfgdocAdded, cfgdocChanged or cfgdocRemoved
	Old     interface{} // the shipped default; nil when added
	New     interface{} // the value now; nil when removed
	Secret  bool
}

// ── What this build ships ─────────────────────────────────────────────────

// cfgdocShipped is the shipped defaults, flattened the way cfgdocFlatten
// flattens the live config, so the two compare key by key.
type cfgdocShipped struct {
	Source    string
	Version   string
	Stamp     string
	Values    map[string]interface{} // read-only once built
	InputVars map[string]bool        // Custom variables the config's text fields write
	Raw       []byte
	Err       error
}

var cfgdocShippedCache struct {
	once sync.Once
	v    cfgdocShipped
}

// cfgdocShippedBytes is the config this platform ships, and its name.
func cfgdocShippedBytes() (string, []byte) {
	if IsWindows() {
		return "jukaconfig.json", cfgdocShippedDesktop
	}
	return "trimui/jukaconfig.device.json", cfgdocShippedDevice
}

// cfgdocShippedDefaults decodes the shipped config once. Safe from any
// goroutine: it reads no live state.
func cfgdocShippedDefaults() cfgdocShipped {
	cfgdocShippedCache.once.Do(func() {
		source, data := cfgdocShippedBytes()
		cfgdocShippedCache.v = cfgdocDecodeShipped(source, data)
	})
	return cfgdocShippedCache.v
}

// cfgdocDecodeShipped flattens one shipped config: its variables, normalised the
// way a load normalises them, and the variables its text fields write.
func cfgdocDecodeShipped(source string, data []byte) cfgdocShipped {
	out := cfgdocShipped{Source: source, Raw: data, InputVars: map[string]bool{}}
	var file struct {
		Version   string    `json:"Version"`
		Variables Variables `json:"variables"`
		Scenes    []struct {
			Elements []struct {
				Type     string `json:"type"`
				Variable string `json:"variable"`
			} `json:"elements"`
		} `json:"scenes"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		out.Err = fmt.Errorf("%s: %w", source, err)
		return out
	}
	for _, sc := range file.Scenes {
		for _, e := range sc.Elements {
			if e.Type == "input" && e.Variable != "" {
				out.InputVars[e.Variable] = true
			}
		}
	}
	cfg := &Config{Variables: file.Variables}
	cfgdocNormalize(cfg)
	out.Version = strings.TrimPrefix(strings.TrimSpace(file.Version), "v")
	out.Values = cfgdocFlatten(cfg.Variables, out.InputVars)
	out.Stamp = cfgdocStamp(out.Values)
	return out
}

// cfgdocNormalize applies the defaults and normalisations a load gives every
// config (config_health.go's Validate, motion.go), so the shipped values are
// what the app would really run with.
func cfgdocNormalize(cfg *Config) {
	if cfg.Variables.Custom == nil {
		cfg.Variables.Custom = map[string]interface{}{}
	}
	_ = NewConfigValidator().Validate(cfg)
	normalizeMotionConfig(cfg)
	if cfg.Variables.BackgroundImage == "" {
		if v, ok := customValue(cfg.Variables.Custom, "backgroundImage"); ok {
			cfg.Variables.BackgroundImage = coerceString(v)
		}
	}
}

// cfgdocStamp is a short hash of a flattened config.
func cfgdocStamp(values map[string]interface{}) string {
	data, err := json.Marshal(values)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// ── Flattening ────────────────────────────────────────────────────────────

// cfgdocJSONName is a Variables field's JSON name, "" for one that is not a
// setting (the Custom map, the runtime-only fields).
func cfgdocJSONName(f reflect.StructField) string {
	name := strings.Split(f.Tag.Get("json"), ",")[0]
	if name == "-" {
		return ""
	}
	return name
}

// cfgdocTypedIndex maps a typed setting's lower-case name to its JSON name, so a
// capitalised mirror in Custom ("Fullscreen") is recognised as that setting.
var cfgdocTypedIndex = func() map[string]string {
	idx := map[string]string{}
	rt := reflect.TypeOf(Variables{})
	for i := 0; i < rt.NumField(); i++ {
		if name := cfgdocJSONName(rt.Field(i)); name != "" {
			idx[strings.ToLower(name)] = name
		}
	}
	return idx
}()

// cfgdocDeviceKeys are facts about the device rather than choices: the canvas a
// device profile sets, and the hardware hints. They are never exported.
var cfgdocDeviceKeys = map[string]bool{
	"screenwidth": true, "screenheight": true, "devicemodel": true,
	"targetplatform": true, "defaultorientation": true,
}

// cfgdocRuntimeKeys are Custom entries the app writes as it runs — counters,
// caches, status lines, bookkeeping — which are not settings anyone chose.
var cfgdocRuntimeKeys = map[string]bool{
	"playtime_v1": true, "recently_played": true, plugSeededKey: true, "weatherUnitAuto": true,
	"screensaverTimeoutMigrated": true, xpackUndoKey: true, xpackActiveFileKey: true,
	xpackWallpaperKey: true, "chat_input": true, "local_ip": true, "plugin_list": true,
	"batterySaverRestore": true, "media_recent_searches": true, "weather_screen_text": true,
	// The TrimUI first-start autostart default was considered on THIS device;
	// carried to another one it would stop the default there (autostart_firstrun.go).
	autoStartDefaultedKey: true,
}

// cfgdocIgnoredKey reports whether a Custom key is runtime state rather than a
// setting. inputVars are the variables the config's text fields write (a
// search box, a chat draft), which are typing, not configuration.
func cfgdocIgnoredKey(key string, inputVars map[string]bool) bool {
	if key == "" || volatileCustomVars[key] || cfgdocRuntimeKeys[key] || inputVars[key] {
		return true
	}
	if strings.HasPrefix(key, "_") || strings.HasPrefix(key, "tool_") ||
		strings.HasPrefix(key, "ftp_") || strings.HasPrefix(key, "ssh_") {
		return true
	}
	for _, suffix := range []string{"_text", "_status", "_address", "_entries", "_results", "_list", "_data", "_error", "Label"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

// cfgdocFlatten turns a config's variables into one map of setting → value: the
// typed settings under their JSON names (colours as #rrggbb), then the Custom
// keys that are settings. A Custom key that only mirrors a typed setting is
// left out — the typed value is the one in force.
func cfgdocFlatten(v Variables, inputVars map[string]bool) map[string]interface{} {
	out := map[string]interface{}{}
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		name := cfgdocJSONName(rt.Field(i))
		if name == "" || cfgdocDeviceKeys[strings.ToLower(name)] {
			continue
		}
		val := rv.Field(i).Interface()
		if c, ok := val.(RGB); ok {
			out[name] = rgbHex(c)
			continue
		}
		out[name] = xpackPlain(val)
	}
	for k, val := range v.Custom {
		lower := strings.ToLower(k)
		if _, typed := cfgdocTypedIndex[lower]; typed || cfgdocDeviceKeys[lower] || cfgdocIgnoredKey(k, inputVars) {
			continue
		}
		out[k] = xpackPlain(val)
	}
	return out
}

// cfgdocInputVars lists the variables a config's text fields write.
func cfgdocInputVars(config *Config, extra map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k := range extra {
		out[k] = true
	}
	if config == nil {
		return out
	}
	for _, sc := range config.Scenes {
		for _, e := range sc.Elements {
			if e.Type == "input" && e.Variable != "" {
				out[e.Variable] = true
			}
		}
	}
	return out
}

// cfgdocSnapshot copies a live config's variables for use off the main thread:
// the Custom map is detached, value by value.
func cfgdocSnapshot(config *Config) Variables {
	if config == nil {
		return Variables{}
	}
	v := config.Variables
	v.Custom = make(map[string]interface{}, len(config.Variables.Custom))
	for k, val := range config.Variables.Custom {
		v.Custom[k] = xpackPlain(val)
	}
	return v
}

// ── Secrets and sections ──────────────────────────────────────────────────

// cfgdocSecretParts are the pieces of a setting's name that mark a credential.
var cfgdocSecretParts = []string{
	"token", "password", "passwd", "passphrase", "secret", "apikey", "api_key",
	"accesskey", "access_key", "privatekey", "private_key", "credential",
}

// cfgdocIsSecretKey reports whether a setting holds a credential, by its name.
func cfgdocIsSecretKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" {
		return false
	}
	for _, part := range cfgdocSecretParts {
		if strings.Contains(k, part) {
			return true
		}
	}
	return strings.HasSuffix(k, "key") && !strings.HasSuffix(k, "hotkey")
}

// cfgdocIsSecret reports whether a setting's value must not leave the device:
// its name marks a credential, or the value is one stored encrypted.
func cfgdocIsSecret(key string, value interface{}) bool {
	if cfgdocIsSecretKey(key) {
		return true
	}
	s, ok := value.(string)
	return ok && strings.HasPrefix(s, "ENC:")
}

// cfgdocSectionOf sorts a setting into one of the four sections.
func cfgdocSectionOf(key string) string {
	k := strings.ToLower(key)
	switch {
	case k == "swapabbuttons", k == "swapxybuttons", k == strings.ToLower(xpackInputKey), strings.HasPrefix(k, "inmap"),
		strings.Contains(k, "keymap"), strings.Contains(k, "remap"), strings.Contains(k, "inputprofile"),
		strings.Contains(k, "controller"), strings.Contains(k, "gamepad"),
		k == a11yKeySwapAB, k == a11yKeySwapXY, k == a11yKeyOneHanded, k == a11yKeyChordBtn,
		k == a11yKeyKeymap, k == a11yKeySlowRepeat:
		return cfgdocSectionInput
	case strings.HasPrefix(k, "home"), k == "gridcolumns", k == "searchwidth", strings.Contains(k, "layout"):
		return cfgdocSectionLayouts
	case strings.HasPrefix(k, "theme_"), isThemeColorVariable(key), k == "accentcolor", k == "backgroundimage",
		k == "dynamicbg", k == "timeofdaytheme", strings.HasPrefix(k, "themeday"), strings.HasPrefix(k, "themenight"),
		k == "fonts", k == "fontsizes", k == a11yKeyFontScale,
		k == a11yKeyContrast, k == a11yKeyColorblind, k == a11yKeyPrevTheme:
		return cfgdocSectionTheme
	}
	return cfgdocSectionBehavior
}

// ── Comparing ─────────────────────────────────────────────────────────────

// cfgdocCanon normalises a value for comparison: "true"/"false" written as
// text by older setters equal the booleans, and numbers compare by value.
func cfgdocCanon(v interface{}) interface{} {
	switch t := v.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true":
			return true
		case "false":
			return false
		}
		return t
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			out[k] = cfgdocCanon(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = cfgdocCanon(val)
		}
		return out
	}
	return v
}

// cfgdocEqual reports whether two setting values mean the same thing.
func cfgdocEqual(a, b interface{}) bool {
	ja, errA := json.Marshal(cfgdocCanon(a))
	jb, errB := json.Marshal(cfgdocCanon(b))
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// cfgdocDiff compares settings with the shipped defaults: keys only you have
// (added), keys whose value differs (changed), and shipped keys your config no
// longer has (removed), sorted by section and then by name.
func cfgdocDiff(current, defaults map[string]interface{}) []cfgdocChange {
	var out []cfgdocChange
	for k, v := range current {
		d, shipped := defaults[k]
		switch {
		case !shipped:
			out = append(out, cfgdocChange{Key: k, Kind: cfgdocAdded, New: v})
		case !cfgdocEqual(d, v):
			out = append(out, cfgdocChange{Key: k, Kind: cfgdocChanged, Old: d, New: v})
		}
	}
	for k, d := range defaults {
		if _, ok := current[k]; !ok {
			out = append(out, cfgdocChange{Key: k, Kind: cfgdocRemoved, Old: d})
		}
	}
	for i := range out {
		out[i].Section = cfgdocSectionOf(out[i].Key)
		out[i].Secret = cfgdocIsSecret(out[i].Key, out[i].New) || cfgdocIsSecret(out[i].Key, out[i].Old)
	}
	cfgdocSortChanges(out)
	return out
}

// cfgdocSortChanges orders changes by section, then by key.
func cfgdocSortChanges(list []cfgdocChange) {
	rank := map[string]int{}
	for i, s := range cfgdocSectionOrder {
		rank[s] = i
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Section != list[j].Section {
			return rank[list[i].Section] < rank[list[j].Section]
		}
		return strings.ToLower(list[i].Key) < strings.ToLower(list[j].Key)
	})
}

// cfgdocShow is a value as the inspector shows it.
func cfgdocShow(v interface{}, secret bool) string {
	if secret {
		if s, ok := v.(string); ok && (s == "" || s == cfgdocRedacted) {
			return "(not set)"
		}
		return "•••• (hidden)"
	}
	switch t := v.(type) {
	case nil:
		return "—"
	case bool:
		if t {
			return "on"
		}
		return "off"
	case string:
		if t == "" {
			return "(empty)"
		}
		return t
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', 6, 64)
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return xpackClip(string(data), 80)
}

// ── Scenes ────────────────────────────────────────────────────────────────

// cfgdocSceneDelta is how a design file differs from the shipped one, scene by
// scene.
type cfgdocSceneDelta struct {
	Changed []json.RawMessage // scenes edited or added, as they are in the file
	Names   []string          // their names, in the same order
	Added   map[string]bool   // which of those the shipped config does not have
	Removed []string          // shipped scenes the file no longer has
}

// cfgdocSceneMap reads a config's scenes by name, in file order.
func cfgdocSceneMap(data []byte) ([]string, map[string]json.RawMessage, error) {
	var file struct {
		Scenes []json.RawMessage `json:"scenes"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, len(file.Scenes))
	byName := make(map[string]json.RawMessage, len(file.Scenes))
	for _, raw := range file.Scenes {
		var head struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &head) != nil || head.Name == "" {
			continue
		}
		if _, dup := byName[head.Name]; dup {
			continue
		}
		names = append(names, head.Name)
		byName[head.Name] = raw
	}
	return names, byName, nil
}

// cfgdocCanonical re-encodes JSON with sorted keys and no spacing, so two
// scenes compare by content rather than by formatting.
func cfgdocCanonical(raw json.RawMessage) []byte {
	var v interface{}
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

// cfgdocDiffScenes compares a design file's scenes with the shipped ones.
func cfgdocDiffScenes(disk, shipped []byte) (cfgdocSceneDelta, error) {
	delta := cfgdocSceneDelta{Added: map[string]bool{}}
	diskNames, diskScenes, err := cfgdocSceneMap(disk)
	if err != nil {
		return delta, err
	}
	shippedNames, shippedScenes, err := cfgdocSceneMap(shipped)
	if err != nil {
		return delta, err
	}
	for _, name := range diskNames {
		raw := diskScenes[name]
		ref, ok := shippedScenes[name]
		if ok && bytes.Equal(cfgdocCanonical(raw), cfgdocCanonical(ref)) {
			continue
		}
		delta.Changed = append(delta.Changed, json.RawMessage(cfgdocCanonical(raw)))
		delta.Names = append(delta.Names, name)
		if !ok {
			delta.Added[name] = true
		}
	}
	for _, name := range shippedNames {
		if _, ok := diskScenes[name]; !ok {
			delta.Removed = append(delta.Removed, name)
		}
	}
	return delta, nil
}

// ── The document ──────────────────────────────────────────────────────────

// cfgdocDesignPath is the design file the scenes are compared from, relative
// to the folder JukaHub runs in like every other config read.
var cfgdocDesignPath = "jukaconfig.json"

// cfgdocReport is everything the inspector and an export need: the shipped
// defaults, the settings diff, the scene delta and the document built from
// them.
type cfgdocReport struct {
	Shipped cfgdocShipped
	Current map[string]interface{}
	Changes []cfgdocChange
	Scenes  cfgdocSceneDelta
	Doc     cfgdocDocument
	Err     error
}

// cfgdocReportFor compares a snapshot of the live variables (cfgdocSnapshot,
// taken on the main thread) with what this build ships. It reads the design
// file and touches no live state, so it runs off the render thread.
func cfgdocReportFor(vars Variables, inputVars map[string]bool, version string) cfgdocReport {
	shipped := cfgdocShippedDefaults()
	rep := cfgdocReport{Shipped: shipped}
	if shipped.Err != nil {
		rep.Err = shipped.Err
		return rep
	}
	all := map[string]bool{}
	for k := range shipped.InputVars {
		all[k] = true
	}
	for k := range inputVars {
		all[k] = true
	}
	rep.Current = cfgdocFlatten(vars, all)
	rep.Changes = cfgdocDiff(rep.Current, shipped.Values)
	if disk, err := os.ReadFile(cfgdocDesignPath); err == nil {
		if delta, err := cfgdocDiffScenes(disk, shipped.Raw); err == nil {
			rep.Scenes = delta
		}
	}
	rep.Doc = cfgdocBuild(version, shipped, rep.Changes, rep.Scenes)
	return rep
}

// cfgdocBuild assembles the document from the diff and the scene delta.
func cfgdocBuild(version string, shipped cfgdocShipped, changes []cfgdocChange, scenes cfgdocSceneDelta) cfgdocDocument {
	doc := cfgdocDocument{
		SchemaVersion: cfgdocSchemaVersion,
		GeneratedBy:   "JukaHub " + version,
		GeneratedAt:   time.Now().Format(time.RFC3339),
		Device: cfgdocDevice{
			Model:    string(DetectDevice()),
			Name:     deviceDisplayName(DetectDevice()),
			Platform: P().Name(),
		},
		Defaults: cfgdocDefaultsStamp{
			Source:  shipped.Source,
			Version: shipped.Version,
			Stamp:   shipped.Stamp,
			Values:  map[string]interface{}{},
		},
		Sections: cfgdocSections{
			InputProfiles:     map[string]interface{}{},
			UILayouts:         cfgdocLayouts{Home: map[string]interface{}{}},
			Theme:             map[string]interface{}{},
			BehaviorOverrides: map[string]interface{}{},
		},
	}
	for _, ch := range changes {
		if ch.Kind != cfgdocAdded {
			old := ch.Old
			if ch.Secret {
				old = cfgdocRedacted
			}
			doc.Defaults.Values[ch.Key] = old
		}
		if ch.Kind == cfgdocRemoved {
			doc.Removed = append(doc.Removed, ch.Key)
			continue
		}
		value := ch.New
		if ch.Secret {
			value = cfgdocRedacted
		}
		switch ch.Section {
		case cfgdocSectionInput:
			doc.Sections.InputProfiles[ch.Key] = value
		case cfgdocSectionLayouts:
			doc.Sections.UILayouts.Home[ch.Key] = value
		case cfgdocSectionTheme:
			doc.Sections.Theme[ch.Key] = value
		default:
			doc.Sections.BehaviorOverrides[ch.Key] = value
		}
	}
	doc.Sections.UILayouts.Scenes = scenes.Changed
	doc.Sections.UILayouts.RemovedScenes = scenes.Removed
	return doc
}

// cfgdocDocValues is every setting a document carries, by key.
func cfgdocDocValues(doc cfgdocDocument) map[string]interface{} {
	out := map[string]interface{}{}
	for _, m := range []map[string]interface{}{
		doc.Sections.InputProfiles, doc.Sections.UILayouts.Home,
		doc.Sections.Theme, doc.Sections.BehaviorOverrides,
	} {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// cfgdocMarshal is the document as written: indented, keys sorted, and with
// "<redacted>" and "&" left readable rather than escaped for HTML.
func cfgdocMarshal(doc cfgdocDocument) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// cfgdocWrite writes the document into dir as jukahub-config.json and returns
// its path.
func cfgdocWrite(doc cfgdocDocument, dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("there is no folder to export to")
	}
	data, err := cfgdocMarshal(doc)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, cfgdocFileName)
	if err := AtomicWrite(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// cfgdocParse reads and validates a document.
func cfgdocParse(data []byte) (cfgdocDocument, error) {
	if len(data) > cfgdocMaxDocBytes {
		return cfgdocDocument{}, fmt.Errorf("the file is larger than %s", humanBytes(cfgdocMaxDocBytes))
	}
	var doc cfgdocDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return cfgdocDocument{}, fmt.Errorf("not a JukaHub config file (%v)", err)
	}
	switch {
	case doc.SchemaVersion <= 0:
		return cfgdocDocument{}, errors.New("not a JukaHub config file (no schemaVersion)")
	case doc.SchemaVersion > cfgdocSchemaVersion:
		return cfgdocDocument{}, fmt.Errorf("made by a newer JukaHub (schema %d)", doc.SchemaVersion)
	}
	for k := range cfgdocDocValues(doc) {
		if !cfgdocValidKey(k) {
			return cfgdocDocument{}, fmt.Errorf("the setting name %q is not valid", k)
		}
	}
	for _, k := range doc.Removed {
		if !cfgdocValidKey(k) {
			return cfgdocDocument{}, fmt.Errorf("the setting name %q is not valid", k)
		}
	}
	return doc, nil
}

// cfgdocValidKey reports whether a setting name is one a config could hold.
func cfgdocValidKey(k string) bool {
	if k == "" || len(k) > 80 {
		return false
	}
	for _, r := range k {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// cfgdocDocFile is a config document found on the card or in Downloads.
type cfgdocDocFile struct {
	Path    string
	Name    string
	ModTime time.Time
	Doc     cfgdocDocument
	Problem string
}

// cfgdocFindDocs lists the config documents in the usual places, newest first.
// Only files named like an export are read, so a Downloads folder full of other
// JSON is left alone.
func cfgdocFindDocs() []cfgdocDocFile {
	var out []cfgdocDocFile
	seen := map[string]bool{}
	for _, dir := range bkupSearchDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, de := range entries {
			name := de.Name()
			lower := strings.ToLower(name)
			if de.IsDir() || !strings.HasSuffix(lower, ".json") || !strings.Contains(lower, "jukahub-config") {
				continue
			}
			full := filepath.Join(dir, name)
			key := strings.ToLower(full)
			if seen[key] {
				continue
			}
			seen[key] = true
			info, err := de.Info()
			if err != nil {
				continue
			}
			f := cfgdocDocFile{Path: full, Name: name, ModTime: info.ModTime()}
			if info.Size() > cfgdocMaxDocBytes {
				f.Problem = "larger than " + humanBytes(cfgdocMaxDocBytes)
			} else if data, err := os.ReadFile(full); err != nil {
				f.Problem = "cannot be read"
			} else if doc, err := cfgdocParse(data); err != nil {
				f.Problem = err.Error()
			} else {
				f.Doc = doc
			}
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out
}

// ── Changing settings ─────────────────────────────────────────────────────

// cfgdocPersistedTyped are the typed settings the user config keeps; any other
// typed field is a design value of jukaconfig.json, which an import does not
// write (it would not survive the next start).
var cfgdocPersistedTyped = map[string]bool{
	"buttonColor": true, "labelColor": true, "inputColor": true, "fullscreen": true,
	"fileExplorerRoot": true, "weatherEnabled": true, "weatherUnit": true, "tspUsername": true,
	"playbackResolution": true, "subtitleLanguage": true, "reducedMotion": true, "lowPower": true,
	"powerMode": true, "ledSync": true, "gridColumns": true, "searchWidth": true, "dynamicBg": true,
	"screensaverTimeout": true, "clock12": true, "netPlayEnabled": true,
	"tools_path": true,
}

// cfgdocImportable reports whether a setting can be written by an import or a
// reset, and why not.
func cfgdocImportable(key string) (bool, string) {
	lower := strings.ToLower(key)
	if cfgdocDeviceKeys[lower] {
		return false, "a fact about the device"
	}
	if canonical, typed := cfgdocTypedIndex[lower]; typed && !cfgdocPersistedTyped[canonical] {
		return false, "a design setting of jukaconfig.json"
	}
	return true, ""
}

// cfgdocSetValue writes one setting the way the Settings rows do: a typed
// setting gets its typed field and its exact-case Custom key (which wins over
// the shipped config's capitalised mirror on the next load), a colour goes
// through applyColorVar, and anything else is a Custom key. The caller saves
// once afterwards (syncVariableOverrides).
func cfgdocSetValue(config *Config, key string, value interface{}) error {
	if config == nil {
		return errors.New("no configuration is loaded")
	}
	if s, ok := value.(string); ok && s == cfgdocRedacted {
		return errors.New("the file does not carry this secret")
	}
	if ok, why := cfgdocImportable(key); !ok {
		return fmt.Errorf("%s is %s", key, why)
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	canonical, typed := cfgdocTypedIndex[strings.ToLower(key)]
	if !typed {
		config.Variables.Custom[key] = value
		return nil
	}
	switch {
	case isThemeColorVariable(canonical):
		applyColorVar(config, canonical, coerceString(value))
		return nil
	case canonical == "netPlayEnabled":
		setNetplayEnabled(config, coerceBool(value))
		return nil
	case canonical == "screensaverTimeout":
		n, ok := coerceInt(value)
		if !ok {
			return fmt.Errorf("%s: %v is not a number of seconds", key, value)
		}
		applyScreensaverTimeout(config, n)
		return nil
	}
	if err := cfgdocSetTyped(&config.Variables, canonical, value); err != nil {
		return err
	}
	dropCustomMirror(config, canonical)
	config.Variables.Custom[canonical] = value
	if canonical == "powerMode" {
		config.Variables.PowerMode = normalizePowerMode(config.Variables.PowerMode)
		applyPowerMode(config)
	}
	return nil
}

// cfgdocSetTyped sets the typed Variables field named key (its JSON name) from a
// JSON value. A value an older setter wrote as text ("true", "5") still counts.
func cfgdocSetTyped(v *Variables, key string, value interface{}) error {
	rv := reflect.ValueOf(v).Elem()
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		if cfgdocJSONName(rt.Field(i)) != key {
			continue
		}
		field := rv.Field(i)
		switch field.Kind() {
		case reflect.Bool:
			field.SetBool(coerceBool(value))
			return nil
		case reflect.Int, reflect.Int32, reflect.Int64:
			n, ok := coerceInt(value)
			if !ok {
				return fmt.Errorf("%s: %v is not a number", key, value)
			}
			field.SetInt(int64(n))
			return nil
		case reflect.String:
			field.SetString(coerceString(value))
			return nil
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		target := reflect.New(field.Type())
		if err := json.Unmarshal(raw, target.Interface()); err != nil {
			return fmt.Errorf("%s: %v", key, err)
		}
		field.Set(target.Elem())
		return nil
	}
	return fmt.Errorf("%s is not a setting", key)
}

// cfgdocResetKey puts one setting back to what this build ships; a setting the
// build does not ship is forgotten. The caller saves afterwards.
func cfgdocResetKey(config *Config, key string, defaults map[string]interface{}) error {
	if config == nil {
		return errors.New("no configuration is loaded")
	}
	if d, ok := defaults[key]; ok {
		return cfgdocSetValue(config, key, d)
	}
	if ok, why := cfgdocImportable(key); !ok {
		return fmt.Errorf("%s is %s", key, why)
	}
	dropCustomMirror(config, key)
	return nil
}

// cfgdocRefresh makes a batch of setting changes visible and saves them: the
// theme and arrangement are repainted, the interface sounds and accessibility
// settings re-read, and everything goes through the normal save path.
func cfgdocRefresh(config *Config) {
	syncVariableOverrides(config)
	xpackRepaint(config)
	bootSfxBind(config)
	a11yInit(config)
}

// ── Importing ─────────────────────────────────────────────────────────────

// cfgdocImportItem is one setting an imported document would change.
type cfgdocImportItem struct {
	Key     string
	Section string
	Current interface{}
	Value   interface{}
	Remove  bool // the document resets this key to the shipped default
	Secret  bool
	Accept  bool
	Blocked bool   // cannot be applied here at all
	Note    string // why it is blocked or not accepted by default
}

// cfgdocImportScene is one scene an imported document carries.
type cfgdocImportScene struct {
	Name   string
	Raw    json.RawMessage
	Accept bool
}

// cfgdocImportPlan is what importing a document would do, item by item.
type cfgdocImportPlan struct {
	Path       string
	Doc        cfgdocDocument
	Items      []cfgdocImportItem
	Scenes     []cfgdocImportScene
	SameDevice bool
	// Upstream lists the defaults that differ between the build the document
	// was made with and this one, for the keys it mentions.
	Upstream []cfgdocChange
}

// cfgdocDeviceSpecific are settings that belong to one kind of device: fine to
// import from the same platform, off by default from another.
var cfgdocDeviceSpecific = map[string]bool{
	"fullscreen": true, "swapABButtons": true, "swapXYButtons": true, "tools_path": true,
	"fileExplorerRoot": true, "powerMode": true, "lowPower": true, "reducedMotion": true,
	"ledSync": true,
}

// cfgdocForeignPath reports whether a value is an absolute path written on the
// other kind of system (a drive letter here on Linux, a /mnt path on Windows).
func cfgdocForeignPath(v interface{}) bool {
	s, ok := v.(string)
	if !ok || s == "" {
		return false
	}
	windowsPath := len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/')
	unixPath := strings.HasPrefix(s, "/")
	if IsWindows() {
		return unixPath
	}
	return windowsPath
}

// cfgdocPlanImport works out what importing doc would change. current is the
// live config flattened, shipped the defaults of this build.
func cfgdocPlanImport(path string, doc cfgdocDocument, current map[string]interface{}, shipped cfgdocShipped) *cfgdocImportPlan {
	plan := &cfgdocImportPlan{Path: path, Doc: doc, SameDevice: doc.Device.Platform == P().Name()}
	values := cfgdocDocValues(doc)
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := values[k]
		if cfgdocIgnoredKey(k, shipped.InputVars) {
			continue
		}
		cur := current[k]
		if cfgdocEqual(cur, v) {
			continue
		}
		it := cfgdocImportItem{Key: k, Section: cfgdocSectionOf(k), Current: cur, Value: v, Secret: cfgdocIsSecret(k, cur) || cfgdocIsSecret(k, v), Accept: true}
		cfgdocJudge(&it, plan.SameDevice, doc.Device.Name)
		plan.Items = append(plan.Items, it)
	}
	for _, k := range doc.Removed {
		d, shippedKey := shipped.Values[k]
		if !shippedKey || cfgdocIgnoredKey(k, shipped.InputVars) || cfgdocEqual(current[k], d) {
			continue
		}
		it := cfgdocImportItem{Key: k, Section: cfgdocSectionOf(k), Current: current[k], Value: d, Remove: true, Secret: cfgdocIsSecretKey(k), Accept: true}
		cfgdocJudge(&it, plan.SameDevice, doc.Device.Name)
		plan.Items = append(plan.Items, it)
	}
	sort.SliceStable(plan.Items, func(i, j int) bool {
		a, b := plan.Items[i], plan.Items[j]
		if a.Section != b.Section {
			return cfgdocSectionRank(a.Section) < cfgdocSectionRank(b.Section)
		}
		return strings.ToLower(a.Key) < strings.ToLower(b.Key)
	})
	for _, raw := range doc.Sections.UILayouts.Scenes {
		var head struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &head) != nil || strings.TrimSpace(head.Name) == "" {
			continue
		}
		plan.Scenes = append(plan.Scenes, cfgdocImportScene{Name: head.Name, Raw: raw, Accept: plan.SameDevice})
	}
	if doc.Defaults.Stamp != "" && doc.Defaults.Stamp != shipped.Stamp {
		then := map[string]interface{}{}
		now := map[string]interface{}{}
		for k, v := range doc.Defaults.Values {
			if s, ok := v.(string); ok && s == cfgdocRedacted {
				continue
			}
			then[k] = v
			if d, ok := shipped.Values[k]; ok {
				now[k] = d
			}
		}
		plan.Upstream = cfgdocDiff(now, then)
	}
	return plan
}

// cfgdocJudge decides whether an import item can be applied, and whether it is
// accepted until the user says otherwise.
func cfgdocJudge(it *cfgdocImportItem, sameDevice bool, from string) {
	if s, ok := it.Value.(string); ok && s == cfgdocRedacted {
		it.Blocked, it.Accept, it.Note = true, false, "secret — the file does not carry it"
		return
	}
	if ok, why := cfgdocImportable(it.Key); !ok {
		it.Blocked, it.Accept, it.Note = true, false, why
		return
	}
	if cfgdocForeignPath(it.Value) {
		it.Blocked, it.Accept, it.Note = true, false, "a folder on another kind of device"
		return
	}
	if !sameDevice && cfgdocDeviceSpecific[it.Key] {
		if from == "" {
			from = "another device"
		}
		it.Accept, it.Note = false, "made on "+from+"; off unless you turn it on"
	}
}

// cfgdocSectionRank is a section's place in cfgdocSectionOrder.
func cfgdocSectionRank(section string) int {
	for i, s := range cfgdocSectionOrder {
		if s == section {
			return i
		}
	}
	return len(cfgdocSectionOrder)
}

// cfgdocApplyImport writes the accepted items through the normal setting path
// and returns how many it applied. Scenes are written by cfgdocWriteScenes.
func cfgdocApplyImport(config *Config, plan *cfgdocImportPlan, shipped cfgdocShipped) (int, []string) {
	applied := 0
	var failed []string
	for _, it := range plan.Items {
		if !it.Accept || it.Blocked {
			continue
		}
		var err error
		if it.Remove {
			err = cfgdocResetKey(config, it.Key, shipped.Values)
		} else {
			err = cfgdocSetValue(config, it.Key, it.Value)
		}
		if err != nil {
			failed = append(failed, it.Key+": "+err.Error())
			continue
		}
		applied++
	}
	if applied > 0 {
		cfgdocRefresh(config)
	}
	return applied, failed
}

// cfgdocWriteScenes replaces (or adds) scenes in the design file at path and
// asks the config watcher to reload it, the path the web builder's live push
// uses. Scenes keep their order; new ones go at the end.
func cfgdocWriteScenes(path string, scenes []cfgdocImportScene) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(data, &file); err != nil {
		return 0, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	var list []json.RawMessage
	if raw, ok := file["scenes"]; ok {
		if err := json.Unmarshal(raw, &list); err != nil {
			return 0, fmt.Errorf("%s: scenes: %w", filepath.Base(path), err)
		}
	}
	index := map[string]int{}
	for i, raw := range list {
		var head struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &head) == nil && head.Name != "" {
			index[head.Name] = i
		}
	}
	written := 0
	for _, sc := range scenes {
		if !sc.Accept {
			continue
		}
		var probe SceneConfig
		if err := json.Unmarshal(sc.Raw, &probe); err != nil || probe.Name != sc.Name {
			continue
		}
		if i, ok := index[sc.Name]; ok {
			list[i] = sc.Raw
		} else {
			index[sc.Name] = len(list)
			list = append(list, sc.Raw)
		}
		written++
	}
	if written == 0 {
		return 0, nil
	}
	encoded, err := json.Marshal(list)
	if err != nil {
		return 0, err
	}
	file["scenes"] = encoded
	out, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := AtomicWrite(path, out, 0o644); err != nil {
		return 0, err
	}
	if globalConfigWatcher != nil {
		globalConfigWatcher.NotifyWritten(path, XMLSidecarPath(path))
	}
	return written, nil
}

// ── Upstream defaults ─────────────────────────────────────────────────────

// cfgdocDefaultsRecord is the shipped defaults as this install last saw them.
type cfgdocDefaultsRecord struct {
	Version string                 `json:"version"`
	Stamp   string                 `json:"stamp"`
	Source  string                 `json:"source"`
	SeenAt  string                 `json:"seenAt"`
	Values  map[string]interface{} `json:"values"`
}

// cfgdocDefaultsPathOverride redirects the record for tests.
var cfgdocDefaultsPathOverride string

// cfgdocDefaultsPath is where the record lives (the data folder).
func cfgdocDefaultsPath() string {
	if cfgdocDefaultsPathOverride != "" {
		return cfgdocDefaultsPathOverride
	}
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, cfgdocDefaultsFile)
}

// cfgdocLoadRecord reads the record; false on a first run.
func cfgdocLoadRecord() (cfgdocDefaultsRecord, bool) {
	path := cfgdocDefaultsPath()
	if path == "" {
		return cfgdocDefaultsRecord{}, false
	}
	var rec cfgdocDefaultsRecord
	if _, err := readJSONWithBackup(path, &rec); err != nil || rec.Stamp == "" {
		return cfgdocDefaultsRecord{}, false
	}
	return rec, true
}

// cfgdocSaveRecord remembers shipped as the defaults the user has seen.
func cfgdocSaveRecord(shipped cfgdocShipped) error {
	path := cfgdocDefaultsPath()
	if path == "" {
		return errors.New("no data folder")
	}
	data, err := json.MarshalIndent(cfgdocDefaultsRecord{
		Version: shipped.Version,
		Stamp:   shipped.Stamp,
		Source:  shipped.Source,
		SeenAt:  time.Now().Format(time.RFC3339),
		Values:  shipped.Values,
	}, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, data, 0o644)
}

// cfgdocUpstream compares the defaults recorded last time with this build's.
// A first run records them and reports nothing; so does a record from the
// other platform's config, whose differences are not an update's.
func cfgdocUpstream(shipped cfgdocShipped) (cfgdocDefaultsRecord, []cfgdocChange) {
	rec, ok := cfgdocLoadRecord()
	if !ok || rec.Source != shipped.Source {
		if shipped.Err == nil && shipped.Stamp != "" {
			if err := cfgdocSaveRecord(shipped); err != nil {
				LogScene("cfgdoc").Warn("defaults record not saved", "err", err)
			}
		}
		return rec, nil
	}
	if rec.Stamp == shipped.Stamp {
		return rec, nil
	}
	return rec, cfgdocDiff(shipped.Values, rec.Values)
}

// cfgdocUpstreamPending is how many defaults changed since the user last looked,
// counted once after startup (the Tools hub tile reads it every rebuild).
var cfgdocUpstreamPending struct {
	mu sync.Mutex
	n  int
}

// cfgdocCheckUpstreamAsync counts the changed defaults off the render thread.
func cfgdocCheckUpstreamAsync() {
	go func() {
		shipped := cfgdocShippedDefaults()
		if shipped.Err != nil {
			return
		}
		_, changes := cfgdocUpstream(shipped)
		cfgdocUpstreamPending.mu.Lock()
		cfgdocUpstreamPending.n = len(changes)
		cfgdocUpstreamPending.mu.Unlock()
	}()
}

// cfgdocUpstreamCount is the last count of changed defaults.
func cfgdocUpstreamCount() int {
	cfgdocUpstreamPending.mu.Lock()
	defer cfgdocUpstreamPending.mu.Unlock()
	return cfgdocUpstreamPending.n
}

// cfgdocUpstreamLabelCache keeps the read-out cfgdocUpstreamLabel builds, so a
// row drawn every frame builds no string. Main thread only.
var cfgdocUpstreamLabelCache struct {
	n     int
	label string
}

// cfgdocUpstreamLabel is the Config row's and tile's read-out: "2 new defaults"
// while an update changed defaults nobody has looked at yet, else "".
func cfgdocUpstreamLabel(*Config) string {
	n := cfgdocUpstreamCount()
	if n <= 0 {
		return ""
	}
	if n != cfgdocUpstreamLabelCache.n || cfgdocUpstreamLabelCache.label == "" {
		cfgdocUpstreamLabelCache.n = n
		cfgdocUpstreamLabelCache.label = xpackKitCount(n, "new default", "new defaults")
	}
	return cfgdocUpstreamLabelCache.label
}

// cfgdocAcknowledgeUpstream records this build's defaults as seen.
func cfgdocAcknowledgeUpstream() error {
	shipped := cfgdocShippedDefaults()
	if shipped.Err != nil {
		return shipped.Err
	}
	if err := cfgdocSaveRecord(shipped); err != nil {
		return err
	}
	cfgdocUpstreamPending.mu.Lock()
	cfgdocUpstreamPending.n = 0
	cfgdocUpstreamPending.mu.Unlock()
	return nil
}
