package main

// xpack.go — experience packs: one switch for a whole way of using JukaHub.
//
// A pack is a small JSON manifest (pack.json), optionally with a few files
// beside it, that bundles the settings which together make a mood — a "Retro
// Arcade" or a "Movie Night":
//
//   - the theme: a preset or total conversion by id, or a theme file of its own;
//   - the Home arrangement (your own layout, the dashboard or the hero grid);
//   - a wallpaper, or "none" for Home's own backdrop;
//   - the interface sounds (on or off, and how loud);
//   - the screen JukaHub opens on;
//   - pinned screens and favourites;
//   - the input profile to use (by name — the input profiles own the rest);
//   - the idle screen;
//   - extra config scenes, validated and added in memory the way the
//     ensureXScene functions add the app's own screens (xpackscene.go), which is
//     what lets the community make scenes and not just colour schemes.
//
// Applying a pack is instant and remembers exactly what it replaced, so Undo
// puts the previous look back. Packs live in <data>/packs as folders or .jkpack
// archives (a zip of the same folder); five ship built in and plugins can add
// more (plugx.go). Nothing here writes jukaconfig.json: every choice is a user
// setting saved through the normal user-config path, which is why switching a
// pack can never damage the design the config ships with.

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	xpackManifestName      = "pack.json"
	xpackArchiveExt        = ".jkpack"
	xpackSchema            = 1
	xpackMaxManifestBytes  = 64 << 10
	xpackMaxArchiveBytes   = 32 << 20
	xpackMaxArchiveEntries = 64
	xpackMaxWallpaperBytes = 12 << 20
	xpackMaxScenes         = 4
	xpackMaxPinned         = 8
	xpackMaxFavorites      = 16
	// xpackThemeDefault is the theme value that returns to JukaHub's own
	// palette with no preset chosen, the state a fresh install starts in.
	xpackThemeDefault = "default"
	// xpackWallpaperNone clears the wallpaper back to Home's backdrop.
	xpackWallpaperNone = "none"
	// xpackDefaultWallpaper is the shipped background's name, which every
	// screen reads as "no wallpaper" (appWallpaperIsShippedDefault).
	xpackDefaultWallpaper = "background.jpg"
)

// Where a pack came from.
const (
	xpackSourceBuiltin = "builtin"
	xpackSourceUser    = "user"
	xpackSourcePlugin  = "plugin:"
)

// The user-config keys the packs keep.
const (
	xpackActiveKey     = "xpackActive"
	xpackActiveFileKey = "xpackActiveFile"
	xpackStartSceneKey = "xpackStartScene"
	xpackWallpaperKey  = "xpackWallpaper"
	xpackUndoKey       = "xpackUndo"
	xpackInputKey      = "xpackInputProfile"
)

// xpackSounds is a pack's interface-sound choice. A nil Enabled keeps the
// switch as it is; Level is a percentage, 10–100.
type xpackSounds struct {
	Enabled *bool `json:"enabled,omitempty"`
	Level   int   `json:"level,omitempty"`
}

// xpackFavorite is a favourite a pack pins: a stream channel ("iptv", listed
// under Live TV) or a file the pack ships ("file", a path inside the pack).
type xpackFavorite struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// xpackManifest is pack.json. Every setting is optional: an empty value leaves
// that part of the setup as it is, so a pack can be as small as one theme.
type xpackManifest struct {
	Schema       int               `json:"schema,omitempty"`
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	Author       string            `json:"author,omitempty"`
	Version      string            `json:"version,omitempty"`
	Theme        string            `json:"theme,omitempty"`
	ThemeFile    string            `json:"themeFile,omitempty"`
	HomeLayout   string            `json:"homeLayout,omitempty"`
	Wallpaper    string            `json:"wallpaper,omitempty"`
	Sounds       *xpackSounds      `json:"sounds,omitempty"`
	StartScene   string            `json:"startScene,omitempty"`
	Pinned       []string          `json:"pinned,omitempty"`
	Favorites    []xpackFavorite   `json:"favorites,omitempty"`
	InputProfile string            `json:"inputProfile,omitempty"`
	Screensaver  string            `json:"screensaver,omitempty"`
	Scenes       []json.RawMessage `json:"scenes,omitempty"`
}

// xpackEntry is a pack as the app knows it: the manifest, where it came from,
// and what validation made of it.
type xpackEntry struct {
	Manifest xpackManifest
	Source   string // xpackSourceBuiltin, xpackSourceUser or "plugin:<folder>"
	Dir      string // the folder its files are relative to; "" for a built-in
	File     string // the manifest it was read from; "" for a built-in
	// Problems are the parts validation dropped, each with its reason.
	Problems []string
	// ThemePreset is the pack's own theme file, validated; nil when the pack
	// names a preset or no theme at all.
	ThemePreset *ThemePreset
	// WallpaperPath is the wallpaper as an absolute path; "" when the pack
	// does not set one (or clears it).
	WallpaperPath string
	// Scenes are the extra screens, validated, at the 1280x720 design size.
	Scenes []SceneConfig
	// Assets are the pack's own files (slash paths relative to Dir) that an
	// export has to carry: the theme file, the wallpaper, scene pictures.
	Assets []string
}

// xpackUndoRecord is what applying a pack replaced, so Undo can put it back.
// It is kept in the user config (xpackUndoKey) as plain JSON, so an Undo still
// works after a restart.
type xpackUndoRecord struct {
	Pack            string                 `json:"pack"`
	PackID          string                 `json:"packId"`
	Prev            string                 `json:"prev,omitempty"`
	Keys            map[string]interface{} `json:"keys"`
	Absent          []string               `json:"absent,omitempty"`
	Button          RGB                    `json:"button"`
	Label           RGB                    `json:"label"`
	Input           RGB                    `json:"input"`
	Wallpaper       string                 `json:"wallpaper"`
	InputProfile    string                 `json:"inputProfile,omitempty"`
	HadInputProfile bool                   `json:"hadInputProfile,omitempty"`
	AddedFavorites  []xpackFavorite        `json:"addedFavorites,omitempty"`
	At              string                 `json:"at"`
}

// xpackPluginRef is a pack a plugin provides (plugx.go).
type xpackPluginRef struct {
	Plugin string
	Path   string
}

// xpackActivateInputProfile switches the chosen control profile by name, and
// xpackCurrentInputProfile names the one chosen now so Undo can switch back to
// it. init points them at the input profiles; tests replace them.
var (
	xpackActivateInputProfile func(config *Config, name string) error
	xpackCurrentInputProfile  func(config *Config) string
)

// xpackInputProfileKey is the user-config key the input profiles keep the
// chosen profile under; missing or "" is their Default.
const xpackInputProfileKey = "inputProfile"

func init() {
	// Assigned here rather than by the declarations: a variable initialiser
	// that reaches into the input profiles (and through their actions most of
	// the app) would make an initialisation cycle of any path back to the packs.
	xpackActivateInputProfile = xpackSwitchInputProfile
	xpackCurrentInputProfile = xpackChosenInputProfile
}

// xpackSwitchInputProfile switches through the input profiles' own switch
// (inmapActivateByName, which also remembers the choice in the user config);
// "" goes back to their Default.
func xpackSwitchInputProfile(_ *Config, name string) error {
	if !inmapActivateByName(name) {
		return fmt.Errorf("no control profile is called %q", name)
	}
	return nil
}

// xpackChosenInputProfile reads the chosen control profile from the config.
func xpackChosenInputProfile(config *Config) string {
	if config == nil || config.Variables.Custom == nil {
		return ""
	}
	name, _ := config.Variables.Custom[xpackInputProfileKey].(string)
	return strings.TrimSpace(name)
}

// xpackDirOverride redirects the packs folder for tests.
var xpackDirOverride string

// xpackDir is <data>/packs, where the user's packs live as folders or .jkpack
// archives.
func xpackDir() string {
	if xpackDirOverride != "" {
		return xpackDirOverride
	}
	if dir, err := P().DataDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "packs")
	}
	return filepath.Join("data", "packs")
}

// ── Built-in packs ────────────────────────────────────────────────────────

// xpackBuiltinSource is the packs that ship with JukaHub, written exactly as a
// pack.json would be so they go through the same parser and validation as a
// community pack. They only name presets themes.go defines and screens every
// shipped config has; a start screen that is missing leaves JukaHub on Home.
const xpackBuiltinSource = `[
  {
    "id": "default",
    "name": "Default",
    "description": "JukaHub as it ships: the hub palette, your own Home layout, soft clicks and Home first.",
    "author": "JukaHub",
    "theme": "default",
    "homeLayout": "custom",
    "wallpaper": "none",
    "sounds": {"enabled": true, "level": 100},
    "screensaver": "dock"
  },
  {
    "id": "retro-arcade",
    "name": "Retro Arcade",
    "description": "Neon synthwave colours, the hero-tile Home, crisp clicks and straight into your games.",
    "author": "JukaHub",
    "theme": "synthwave",
    "homeLayout": "grid",
    "wallpaper": "none",
    "sounds": {"enabled": true, "level": 100},
    "startScene": "Games",
    "pinned": ["Games", "MusicPlayer", "Playtime"],
    "screensaver": "stars"
  },
  {
    "id": "movie-night",
    "name": "Movie Night",
    "description": "A deep midnight palette, silent menus and Media first, with the clock dock when idle.",
    "author": "JukaHub",
    "theme": "midnight",
    "homeLayout": "dashboard",
    "wallpaper": "none",
    "sounds": {"enabled": false, "level": 40},
    "startScene": "Tube",
    "pinned": ["Tube", "MusicPlayer"],
    "screensaver": "dock"
  },
  {
    "id": "focus-reading",
    "name": "Focus / Reading",
    "description": "Paper-white e-ink colours, no clicks and your own calm Home for reading and notes.",
    "author": "JukaHub",
    "theme": "paper",
    "homeLayout": "custom",
    "wallpaper": "none",
    "sounds": {"enabled": false, "level": 40},
    "pinned": ["PDFReader", "Notes", "Dictionary"],
    "screensaver": "dock"
  },
  {
    "id": "night-owl",
    "name": "Night Owl",
    "description": "True-black OLED palette, dimmed clicks and the clock dock: easy on the eyes after dark.",
    "author": "JukaHub",
    "theme": "oled",
    "homeLayout": "custom",
    "wallpaper": "none",
    "sounds": {"enabled": true, "level": 30},
    "screensaver": "dock"
  }
]`

// xpackBuiltinCache parses the built-in packs once.
var xpackBuiltinCache struct {
	once    sync.Once
	entries []xpackEntry
}

// xpackBuiltinEntries returns the built-in packs in their fixed order. The
// slice is shared: callers must not modify it.
func xpackBuiltinEntries() []xpackEntry {
	xpackBuiltinCache.once.Do(func() {
		var list []json.RawMessage
		if err := json.Unmarshal([]byte(xpackBuiltinSource), &list); err != nil {
			LogScene("xpack").Error("built-in packs do not parse", "err", err)
			return
		}
		for _, raw := range list {
			m, err := xpackParseManifest(raw, "")
			if err != nil {
				LogScene("xpack").Error("built-in pack skipped", "err", err)
				continue
			}
			e := xpackEntry{Manifest: m, Source: xpackSourceBuiltin}
			xpackValidate(&e)
			xpackBuiltinCache.entries = append(xpackBuiltinCache.entries, e)
		}
	})
	return xpackBuiltinCache.entries
}

// ── Reading and validating ────────────────────────────────────────────────

// xpackParseManifest decodes pack.json. fallbackID is used when the manifest
// names no id (a folder written by hand is named after its pack).
func xpackParseManifest(data []byte, fallbackID string) (xpackManifest, error) {
	var m xpackManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return xpackManifest{}, fmt.Errorf("%s is not valid JSON: %v", xpackManifestName, err)
	}
	if m.Schema > xpackSchema {
		return xpackManifest{}, fmt.Errorf("made for a newer JukaHub (pack schema %d)", m.Schema)
	}
	m.Schema = xpackSchema
	m.ID = plugxSlug(m.ID)
	if m.ID == "" {
		m.ID = plugxSlug(fallbackID)
	}
	if m.ID == "" {
		return xpackManifest{}, errors.New("the pack has no id")
	}
	m.Name = xpackClip(strings.TrimSpace(m.Name), 40)
	if m.Name == "" {
		m.Name = m.ID
	}
	m.Description = xpackClip(strings.TrimSpace(m.Description), 240)
	m.Author = xpackClip(strings.TrimSpace(m.Author), 60)
	m.Version = xpackClip(strings.TrimSpace(m.Version), 20)
	return m, nil
}

// xpackReadManifest reads a pack's manifest from a folder (its pack.json) or a
// .json file, and returns it with the folder its files are relative to and the
// manifest file itself.
func xpackReadManifest(path string) (xpackManifest, string, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return xpackManifest{}, "", "", errors.New("not found")
	}
	file, dir := path, filepath.Dir(path)
	fallback := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if info.IsDir() {
		file, dir, fallback = filepath.Join(path, xpackManifestName), path, filepath.Base(path)
		if info, err = os.Stat(file); err != nil {
			return xpackManifest{}, "", "", fmt.Errorf("the folder has no %s", xpackManifestName)
		}
	}
	if info.Size() > xpackMaxManifestBytes {
		return xpackManifest{}, "", "", fmt.Errorf("%s is larger than %s", filepath.Base(file), humanBytes(xpackMaxManifestBytes))
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return xpackManifest{}, "", "", err
	}
	m, err := xpackParseManifest(data, fallback)
	if err != nil {
		return xpackManifest{}, "", "", err
	}
	return m, dir, file, nil
}

// xpackLoadEntry reads and validates the pack at path (a folder or a manifest).
// It touches no global state, so the gallery can scan off the render thread.
func xpackLoadEntry(path, source string) (xpackEntry, error) {
	m, dir, file, err := xpackReadManifest(path)
	if err != nil {
		return xpackEntry{}, err
	}
	e := xpackEntry{Manifest: m, Source: source, Dir: dir, File: file}
	xpackValidate(&e)
	return e, nil
}

// xpackPeekName is a pack's display name, or "" when it cannot be read.
func xpackPeekName(path string) string {
	m, _, _, err := xpackReadManifest(path)
	if err != nil {
		return ""
	}
	return m.Name
}

// xpackImageExts are the wallpaper formats the backdrop loader takes.
var xpackImageExts = []string{".png", ".jpg", ".jpeg", ".webp", ".bmp"}

// xpackValidate checks every part of a pack against what JukaHub can do with
// it. A part that does not pass is dropped and named in Problems; the rest of
// the pack still applies. Pure: no global state is read or written.
func xpackValidate(e *xpackEntry) {
	m := &e.Manifest
	drop := func(format string, args ...interface{}) {
		e.Problems = append(e.Problems, fmt.Sprintf(format, args...))
	}
	asset := func(rel string) {
		e.Assets = append(e.Assets, filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel))))
	}
	m.Theme = strings.TrimSpace(m.Theme)
	if m.ThemeFile = strings.TrimSpace(m.ThemeFile); m.ThemeFile != "" {
		preset, err := xpackLoadThemeFile(e.Dir, m.ThemeFile)
		if err != nil {
			drop("theme file %s: %v", m.ThemeFile, err)
			m.ThemeFile = ""
		} else {
			e.ThemePreset = &preset
			asset(m.ThemeFile)
		}
	}
	if layout := strings.ToLower(strings.TrimSpace(m.HomeLayout)); layout != "" {
		switch layout {
		case homeArrangementCustom, homeArrangementDashboard, homeArrangementGrid:
			m.HomeLayout = layout
		default:
			drop("Home layout %q is not custom, dashboard or grid", m.HomeLayout)
			m.HomeLayout = ""
		}
	}
	switch w := strings.TrimSpace(m.Wallpaper); {
	case w == "":
		m.Wallpaper = ""
	case strings.EqualFold(w, xpackWallpaperNone):
		m.Wallpaper = xpackWallpaperNone
	default:
		path, err := xpackResolveAsset(e.Dir, w, xpackImageExts, xpackMaxWallpaperBytes)
		if err != nil {
			drop("wallpaper %s: %v", w, err)
			m.Wallpaper = ""
		} else {
			e.WallpaperPath = path
			asset(w)
		}
	}
	if s := m.Sounds; s != nil {
		if s.Level == 0 {
			s.Level = 100
		}
		s.Level = max(10, min(100, s.Level))
	}
	m.StartScene = xpackClip(strings.TrimSpace(m.StartScene), 40)
	m.Pinned = xpackCleanNames(m.Pinned, xpackMaxPinned)
	m.InputProfile = xpackClip(strings.TrimSpace(m.InputProfile), 60)
	switch style := strings.ToLower(strings.TrimSpace(m.Screensaver)); style {
	case "", "dock", "stars":
		m.Screensaver = style
	default:
		drop("idle screen %q is not dock or stars", m.Screensaver)
		m.Screensaver = ""
	}
	favorites := make([]xpackFavorite, 0, len(m.Favorites))
	for _, f := range m.Favorites {
		if len(favorites) >= xpackMaxFavorites {
			drop("only the first %d favourites are used", xpackMaxFavorites)
			break
		}
		f.Type = strings.ToLower(strings.TrimSpace(f.Type))
		f.Name = xpackClip(strings.TrimSpace(f.Name), 80)
		f.Path = strings.TrimSpace(f.Path)
		switch f.Type {
		case "iptv":
			if !xpackStreamURL(f.Path) {
				drop("favourite %q: %q is not a stream address", f.Name, f.Path)
				continue
			}
		case "file":
			if _, err := xpackResolveAsset(e.Dir, f.Path, nil, 0); err != nil {
				drop("favourite %q: %v", f.Name, err)
				continue
			}
			asset(f.Path)
		default:
			drop("favourite %q: type %q is not iptv or file", f.Name, f.Type)
			continue
		}
		if f.Name == "" {
			f.Name = filepath.Base(f.Path)
		}
		favorites = append(favorites, f)
	}
	m.Favorites = favorites
	e.Scenes = nil
	for i, raw := range m.Scenes {
		if i >= xpackMaxScenes {
			drop("only the first %d screens are used", xpackMaxScenes)
			break
		}
		scene, assets, err := xpackValidateScene(raw, e.Dir)
		if err != nil {
			drop("screen %d: %v", i+1, err)
			continue
		}
		e.Scenes = append(e.Scenes, scene)
		for _, rel := range assets {
			asset(rel)
		}
	}
}

// xpackResolveAsset resolves a file a pack ships: it must sit inside the pack's
// folder, be a regular file, have one of exts (when given) and be at most
// maxBytes (when positive). The result is absolute.
func xpackResolveAsset(dir, rel string, exts []string, maxBytes int64) (string, error) {
	if dir == "" {
		return "", errors.New("a built-in pack carries no files")
	}
	path, err := resolveWithinRoot(dir, rel)
	if err != nil {
		return "", errors.New("is outside the pack")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("file not found")
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return "", fmt.Errorf("is larger than %s", humanBytes(maxBytes))
	}
	if len(exts) > 0 {
		ext := strings.ToLower(filepath.Ext(path))
		ok := false
		for _, want := range exts {
			if ext == want {
				ok = true
				break
			}
		}
		if !ok {
			return "", fmt.Errorf("must be a %s file", strings.Join(exts, ", "))
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return path, nil
}

// xpackLoadThemeFile reads a pack's own theme file (the themes/*.json format).
func xpackLoadThemeFile(dir, rel string) (ThemePreset, error) {
	path, err := xpackResolveAsset(dir, rel, []string{".json"}, plugxMaxFileBytes)
	if err != nil {
		return ThemePreset{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ThemePreset{}, errors.New("cannot be read")
	}
	var file themeFile
	if err := json.Unmarshal(data, &file); err != nil {
		return ThemePreset{}, fmt.Errorf("is not a theme file (%v)", err)
	}
	return file.toPreset()
}

// xpackStreamURL reports whether s is a stream address a favourite may pin.
func xpackStreamURL(s string) bool {
	if s == "" || len(s) > 1024 || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	lower := strings.ToLower(s)
	for _, scheme := range []string{"http://", "https://", "rtsp://", "rtmp://"} {
		if strings.HasPrefix(lower, scheme) && len(lower) > len(scheme) {
			return true
		}
	}
	return false
}

// xpackCleanNames trims, de-duplicates and caps a list of scene names.
func xpackCleanNames(names []string, limit int) []string {
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		n = xpackClip(strings.TrimSpace(n), 40)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// xpackClip shortens s to at most n characters.
func xpackClip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n]))
}

// ── The store ─────────────────────────────────────────────────────────────

// xpackStore holds the packs found on disk and in plugins. The user packs are
// scanned off the render thread; the plugin packs arrive from the plugin scan
// on the main thread. gen changes with either, so the gallery knows when to
// rebuild its cards.
var xpackStore struct {
	mu       sync.Mutex
	user     []xpackEntry
	plugin   []xpackEntry
	loaded   map[string]xpackEntry // packs read straight from their file before a scan
	problems []string              // files in packs/ that are not packs, and why
	scanned  bool
	scanning bool
	gen      int
}

// xpackEntries lists every pack: the built-ins in their fixed order, then the
// user's by name, then the plugins'. The slice is a fresh copy.
func xpackEntries() []xpackEntry {
	builtins := xpackBuiltinEntries()
	xpackStore.mu.Lock()
	defer xpackStore.mu.Unlock()
	out := make([]xpackEntry, 0, len(builtins)+len(xpackStore.user)+len(xpackStore.plugin))
	out = append(out, builtins...)
	out = append(out, xpackStore.user...)
	return append(out, xpackStore.plugin...)
}

// xpackStoreGen changes whenever the pack list does.
func xpackStoreGen() int {
	xpackStore.mu.Lock()
	defer xpackStore.mu.Unlock()
	return xpackStore.gen
}

// xpackScanState reports whether packs/ has been scanned, whether a scan is
// running, and the files the last scan could not use.
func xpackScanState() (bool, bool, []string) {
	xpackStore.mu.Lock()
	defer xpackStore.mu.Unlock()
	return xpackStore.scanned, xpackStore.scanning, append([]string(nil), xpackStore.problems...)
}

// xpackFind looks a pack up by id.
func xpackFind(id string) (xpackEntry, bool) {
	if id == "" {
		return xpackEntry{}, false
	}
	for _, e := range xpackEntries() {
		if e.Manifest.ID == id {
			return e, true
		}
	}
	xpackStore.mu.Lock()
	defer xpackStore.mu.Unlock()
	e, ok := xpackStore.loaded[id]
	return e, ok
}

// xpackScanAsync rescans <data>/packs in the background. A scan that is already
// running is not started twice.
func xpackScanAsync() {
	xpackStore.mu.Lock()
	if xpackStore.scanning {
		xpackStore.mu.Unlock()
		return
	}
	xpackStore.scanning = true
	xpackStore.mu.Unlock()
	go func() {
		entries, problems := xpackScanUserPacks(xpackDir())
		xpackStore.mu.Lock()
		xpackStore.user, xpackStore.problems = entries, problems
		xpackStore.scanned, xpackStore.scanning = true, false
		xpackStore.gen++
		xpackStore.mu.Unlock()
	}()
}

// xpackScanUserPacks reads every pack folder in dir. A .jkpack dropped there by
// hand is unpacked once into a folder beside it (an existing folder of the same
// pack wins). Packs that reuse a built-in's id, or an earlier pack's, are
// reported instead of shadowing it.
func xpackScanUserPacks(dir string) ([]xpackEntry, []string) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	var problems []string
	for _, it := range items {
		name := it.Name()
		if it.IsDir() || !strings.EqualFold(filepath.Ext(name), xpackArchiveExt) {
			continue
		}
		if _, err := xpackUnpack(filepath.Join(dir, name), dir, false); err != nil {
			problems = append(problems, name+": "+err.Error())
		}
	}
	if items, err = os.ReadDir(dir); err != nil {
		return nil, problems
	}
	reserved := map[string]bool{}
	for _, b := range xpackBuiltinEntries() {
		reserved[b.Manifest.ID] = true
	}
	var out []xpackEntry
	for _, it := range items {
		name := it.Name()
		if !it.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		e, err := xpackLoadEntry(filepath.Join(dir, name), xpackSourceUser)
		if err != nil {
			problems = append(problems, name+": "+err.Error())
			continue
		}
		if reserved[e.Manifest.ID] {
			problems = append(problems, name+": another pack already uses the id "+e.Manifest.ID)
			continue
		}
		reserved[e.Manifest.ID] = true
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Manifest.Name) < strings.ToLower(out[j].Manifest.Name)
	})
	return out, problems
}

// xpackSetPluginPacks replaces the packs the switched-on plugins provide. The
// plugin scan calls it on the main thread (plugx.go). A plugin pack never
// shadows a built-in or a user pack of the same id.
func xpackSetPluginPacks(config *Config, refs []xpackPluginRef) {
	reserved := map[string]bool{}
	for _, b := range xpackBuiltinEntries() {
		reserved[b.Manifest.ID] = true
	}
	xpackStore.mu.Lock()
	for _, u := range xpackStore.user {
		reserved[u.Manifest.ID] = true
	}
	xpackStore.mu.Unlock()
	var out []xpackEntry
	for _, ref := range refs {
		e, err := xpackLoadEntry(ref.Path, xpackSourcePlugin+ref.Plugin)
		if err != nil {
			LogScene("xpack").Warn("plugin pack skipped", "plugin", ref.Plugin, "path", ref.Path, "err", err)
			continue
		}
		if reserved[e.Manifest.ID] {
			LogScene("xpack").Warn("plugin pack skipped: id in use", "plugin", ref.Plugin, "id", e.Manifest.ID)
			continue
		}
		reserved[e.Manifest.ID] = true
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Manifest.Name) < strings.ToLower(out[j].Manifest.Name)
	})
	xpackStore.mu.Lock()
	changed := !xpackSameEntries(xpackStore.plugin, out)
	xpackStore.plugin = out
	if changed {
		xpackStore.gen++
	}
	xpackStore.mu.Unlock()
	if config == nil {
		return
	}
	if active, ok := xpackActiveEntry(config); ok && strings.HasPrefix(active.Source, xpackSourcePlugin) {
		xpackRegisterTheme(active)
		xpackSyncScenes(config)
	}
	xpackRefreshLabel(config)
}

// xpackSameEntries reports whether two pack lists are the same packs.
func xpackSameEntries(a, b []xpackEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Manifest.ID != b[i].Manifest.ID || a[i].File != b[i].File || a[i].Manifest.Name != b[i].Manifest.Name {
			return false
		}
	}
	return true
}

// ── The pack in force ─────────────────────────────────────────────────────

// xpackActiveID is the id of the pack in force, or "".
func xpackActiveID(config *Config) string {
	if config == nil || config.Variables.Custom == nil {
		return ""
	}
	id, _ := config.Variables.Custom[xpackActiveKey].(string)
	return strings.TrimSpace(id)
}

// xpackActiveEntry is the pack in force. Before packs/ has been scanned (at
// startup) a user or plugin pack is read straight from the manifest it was
// applied from, once, and remembered.
func xpackActiveEntry(config *Config) (xpackEntry, bool) {
	id := xpackActiveID(config)
	if id == "" {
		return xpackEntry{}, false
	}
	if e, ok := xpackFind(id); ok {
		return e, true
	}
	file, _ := config.Variables.Custom[xpackActiveFileKey].(string)
	if strings.TrimSpace(file) == "" {
		return xpackEntry{}, false
	}
	e, err := xpackLoadEntry(file, xpackSourceUser)
	if err != nil || e.Manifest.ID != id {
		return xpackEntry{}, false
	}
	xpackStore.mu.Lock()
	if xpackStore.loaded == nil {
		xpackStore.loaded = map[string]xpackEntry{}
	}
	xpackStore.loaded[id] = e
	xpackStore.mu.Unlock()
	return e, true
}

// xpackActiveName is the name of the pack in force, or "" when none is.
func xpackActiveName(config *Config) string {
	if e, ok := xpackActiveEntry(config); ok {
		return e.Manifest.Name
	}
	return xpackActiveID(config)
}

// xpackLabelCache is the name of the pack in force and xpackPinCache the
// screens it pins, refreshed whenever it changes, so Quick Settings, the Tools
// hub and the Start menu can read them every frame without a lookup.
var (
	xpackLabelCache string
	xpackPinCache   []string
)

// xpackRefreshLabel recomputes xpackLabelCache and xpackPinCache.
func xpackRefreshLabel(config *Config) {
	e, ok := xpackActiveEntry(config)
	if !ok {
		xpackLabelCache, xpackPinCache = xpackActiveID(config), nil
		return
	}
	xpackLabelCache, xpackPinCache = e.Manifest.Name, e.Manifest.Pinned
}

// xpackPinnedScenes is the screens the pack in force pins, which the Start
// menu lists before every other screen. The slice is shared: do not modify it.
func xpackPinnedScenes() []string { return xpackPinCache }

// xpackQSLabel is the Quick Settings row's value: the pack in force.
func xpackQSLabel() string {
	if xpackLabelCache == "" {
		return "None"
	}
	return xpackLabelCache
}

// xpackThemeID is the preset id a pack's own theme file is registered under.
func xpackThemeID(packID string) string { return "xpack-" + packID }

// xpackRegisterTheme puts a pack's own theme file into the theme catalogue
// (main thread) and returns its preset id, or "" when the pack has none.
func xpackRegisterTheme(e xpackEntry) string {
	if e.ThemePreset == nil {
		return ""
	}
	id := xpackThemeID(e.Manifest.ID)
	if isBuiltinThemeID(id) {
		return ""
	}
	if _, exists := themePresets[id]; !exists {
		themePresetOrder = append(themePresetOrder, id)
	}
	themePresets[id] = *e.ThemePreset
	return id
}

// ── Applying and undoing ──────────────────────────────────────────────────

// xpackStateKeys are the user-config keys a pack can change. Undo restores each
// one exactly: its old value, or its absence.
func xpackStateKeys() []string {
	keys := make([]string, 0, len(themePresetKeys)+len(themeColorVariables)+9)
	keys = append(keys, themePresetKeys...)
	keys = append(keys, themeColorVariables...)
	return append(keys, "homeArrangement", bootSfxEnabledKey, bootSfxVolumeKey, "ambientMode",
		xpackActiveKey, xpackActiveFileKey, xpackStartSceneKey, xpackWallpaperKey, xpackInputKey)
}

// xpackApply switches to pack e. Everything it sets changes now, and what it
// replaced is remembered for Undo. The returned note names what could not be
// applied here (a theme or a control profile that is not installed); it is
// empty when everything took.
func xpackApply(config *Config, e xpackEntry) (string, error) {
	if config == nil {
		return "", errors.New("no configuration is loaded")
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	m := e.Manifest
	undo := xpackSnapshot(config, e)
	custom := config.Variables.Custom
	var skipped []string

	// The theme first: a total conversion also sets the Home arrangement, which
	// the pack's own layout choice below then has the last word on.
	switch {
	case e.ThemePreset != nil:
		if id := xpackRegisterTheme(e); id != "" {
			applyThemePreset(config, id, false)
		} else {
			skipped = append(skipped, "its theme file")
		}
	case m.Theme == xpackThemeDefault:
		clearThemePreset(config)
	case m.Theme != "":
		if _, ok := themePresets[m.Theme]; ok {
			applyThemePreset(config, m.Theme, false)
		} else {
			skipped = append(skipped, "theme "+m.Theme+" (not installed)")
		}
	}
	if m.HomeLayout != "" {
		arrangement := normalizeHomeArrangement(m.HomeLayout)
		custom["homeArrangement"] = arrangement
		if homeLayout != nil {
			homeLayout.SetArrangement(arrangement)
		}
	}
	switch {
	case e.WallpaperPath != "":
		config.Variables.BackgroundImage = e.WallpaperPath
		custom[xpackWallpaperKey] = e.WallpaperPath
		xpackDropDynamicWallpaper()
	case m.Wallpaper == xpackWallpaperNone:
		config.Variables.BackgroundImage = xpackDefaultWallpaper
		custom[xpackWallpaperKey] = xpackWallpaperNone
		xpackDropDynamicWallpaper()
	}
	if s := m.Sounds; s != nil {
		on := setUISoundsOn(config)
		if s.Enabled != nil {
			on = *s.Enabled
		}
		bootSfxSetPrefs(config, on, s.Level)
	}
	if m.Screensaver != "" {
		custom["ambientMode"] = m.Screensaver == "dock"
	}
	if m.StartScene != "" {
		custom[xpackStartSceneKey] = m.StartScene
	} else {
		delete(custom, xpackStartSceneKey)
	}
	if m.InputProfile != "" {
		custom[xpackInputKey] = m.InputProfile
		switch {
		case xpackActivateInputProfile == nil:
			skipped = append(skipped, "control profile "+m.InputProfile+" (control profiles are not available)")
		default:
			if err := xpackActivateInputProfile(config, m.InputProfile); err != nil {
				skipped = append(skipped, "control profile "+m.InputProfile+" (not installed)")
				LogScene("xpack").Warn("control profile not switched", "profile", m.InputProfile, "err", err)
			}
		}
	}
	undo.AddedFavorites = xpackAddFavorites(e)
	custom[xpackActiveKey] = m.ID
	if e.File != "" && e.Source != xpackSourceBuiltin {
		custom[xpackActiveFileKey] = e.File
	} else {
		delete(custom, xpackActiveFileKey)
	}
	custom[xpackUndoKey] = xpackPlain(undo)
	xpackSyncScenes(config)
	xpackRefreshLabel(config)
	if len(undo.AddedFavorites) > 0 {
		saveFavorites()
	}
	saveUserConfigDebounced(userConfigFromVariables(config))
	return strings.Join(skipped, "; "), nil
}

// xpackSnapshot records everything applying e can change.
func xpackSnapshot(config *Config, e xpackEntry) xpackUndoRecord {
	rec := xpackUndoRecord{
		Pack:      e.Manifest.Name,
		PackID:    e.Manifest.ID,
		Prev:      xpackActiveName(config),
		Keys:      map[string]interface{}{},
		Button:    config.Variables.ButtonColor,
		Label:     config.Variables.LabelColor,
		Input:     config.Variables.InputColor,
		Wallpaper: config.Variables.BackgroundImage,
		At:        time.Now().Format(time.RFC3339),
	}
	for _, k := range xpackStateKeys() {
		if v, ok := config.Variables.Custom[k]; ok {
			rec.Keys[k] = xpackPlain(v)
		} else {
			rec.Absent = append(rec.Absent, k)
		}
	}
	if e.Manifest.InputProfile != "" && xpackCurrentInputProfile != nil {
		rec.InputProfile, rec.HadInputProfile = xpackCurrentInputProfile(config), true
	}
	return rec
}

// xpackUndoRecordFrom reads the stored undo record.
func xpackUndoRecordFrom(config *Config) (xpackUndoRecord, bool) {
	if config == nil || config.Variables.Custom == nil {
		return xpackUndoRecord{}, false
	}
	raw, ok := config.Variables.Custom[xpackUndoKey]
	if !ok || raw == nil {
		return xpackUndoRecord{}, false
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return xpackUndoRecord{}, false
	}
	var rec xpackUndoRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.PackID == "" {
		return xpackUndoRecord{}, false
	}
	if rec.Keys == nil {
		rec.Keys = map[string]interface{}{}
	}
	return rec, true
}

// xpackUndoLabel is the name of the pack Undo would take back, if any.
func xpackUndoLabel(config *Config) (string, bool) {
	rec, ok := xpackUndoRecordFrom(config)
	return rec.Pack, ok
}

// xpackUndo puts back everything the last pack change replaced and returns the
// name of the pack it took back.
func xpackUndo(config *Config) (string, error) {
	rec, ok := xpackUndoRecordFrom(config)
	if !ok {
		return "", errors.New("there is nothing to undo")
	}
	custom := config.Variables.Custom
	for k, v := range rec.Keys {
		custom[k] = v
	}
	for _, k := range rec.Absent {
		delete(custom, k)
	}
	delete(custom, xpackUndoKey)
	config.Variables.ButtonColor = rec.Button
	config.Variables.LabelColor = rec.Label
	config.Variables.InputColor = rec.Input
	if config.Variables.BackgroundImage != rec.Wallpaper {
		config.Variables.BackgroundImage = rec.Wallpaper
		xpackDropDynamicWallpaper()
	}
	xpackRepaint(config)
	bootSfxBind(config)
	if rec.HadInputProfile && xpackActivateInputProfile != nil {
		if err := xpackActivateInputProfile(config, rec.InputProfile); err != nil {
			LogScene("xpack").Warn("input profile not restored", "profile", rec.InputProfile, "err", err)
		}
	}
	if xpackRemoveFavorites(rec.AddedFavorites) {
		saveFavorites()
	}
	xpackSyncScenes(config)
	xpackRefreshLabel(config)
	saveUserConfig(userConfigFromVariables(config))
	return rec.Pack, nil
}

// xpackRepaint redraws the app in whatever theme and arrangement the config
// now names.
func xpackRepaint(config *Config) {
	ensureThemeColors(config)
	if name := themePresetName(config); name != "" {
		ApplyThemeColors(GetThemePreset(name))
	} else {
		ApplyThemeColors(GetThemePreset("hub"))
	}
	SetAccentColor(themeAccentColor())
	ClearTextCache()
	ClearShapeCache()
	ClearIconAtlas()
	clearHomeTextCache()
	if homeLayout != nil {
		homeLayout.SetArrangement(homeArrangement(config))
	}
}

// xpackDropDynamicWallpaper releases the living background's copy of the old
// wallpaper so it reloads the new one, the way the wallpaper picker does.
func xpackDropDynamicWallpaper() {
	if dynbg.wallpaper != nil {
		_ = dynbg.wallpaper.Destroy()
		dynbg.wallpaper = nil
	}
}

// xpackPlain turns v into plain JSON values (maps, slices, strings, numbers),
// detached from anything live, so it can sit in the user config.
func xpackPlain(v interface{}) interface{} {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out interface{}
	if json.Unmarshal(data, &out) != nil {
		return nil
	}
	return out
}

// xpackAddFavorites pins the pack's favourites and returns the ones it added; a
// favourite the user already had is left alone, and Undo leaves it too.
func xpackAddFavorites(e xpackEntry) []xpackFavorite {
	if len(e.Manifest.Favorites) == 0 {
		return nil
	}
	resolved := make([]xpackFavorite, 0, len(e.Manifest.Favorites))
	for _, f := range e.Manifest.Favorites {
		if f.Type == "file" {
			path, err := xpackResolveAsset(e.Dir, f.Path, nil, 0)
			if err != nil {
				continue
			}
			f.Path = path
		}
		resolved = append(resolved, f)
	}
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	var added []xpackFavorite
	for _, f := range resolved {
		list := &favoritesStore.IPTV
		if f.Type == "file" {
			list = &favoritesStore.Files
		}
		if xpackFavoriteListed(*list, f.Type, f.Path) {
			continue
		}
		item := FavoriteItem{Type: f.Type, Data: FileEntry{Name: f.Name, Path: f.Path}, Timestamp: time.Now()}
		*list = append([]FavoriteItem{item}, *list...)
		added = append(added, f)
	}
	return added
}

// xpackFavoriteListed reports whether list already holds a favourite of kind
// typ for path.
func xpackFavoriteListed(list []FavoriteItem, typ, path string) bool {
	for _, item := range list {
		if item.Type != typ {
			continue
		}
		if fe, ok := item.Data.(FileEntry); ok && fe.Path == path {
			return true
		}
	}
	return false
}

// xpackRemoveFavorites takes back the favourites a pack added and reports
// whether there were any.
func xpackRemoveFavorites(added []xpackFavorite) bool {
	if len(added) == 0 {
		return false
	}
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	for _, f := range added {
		entry := FileEntry{Name: f.Name, Path: f.Path}
		if f.Type == "file" {
			favoritesStore.Files = removeDuplicateFavorite(favoritesStore.Files, f.Path, "file", entry)
		} else {
			favoritesStore.IPTV = removeDuplicateFavorite(favoritesStore.IPTV, f.Path, "iptv", entry)
		}
	}
	return true
}

// ── Startup, Quick Settings ───────────────────────────────────────────────

// xpackEnsure prepares a freshly loaded config at startup and after every
// config reload, like the ensureXScene functions: it adds the platform screens
// (packs, config, backup) and re-applies what the pack in force needs — its
// extra scenes, its own theme file, its wallpaper. Everything else a pack sets
// is a saved setting the config already carries.
func xpackEnsure(config *Config) {
	if config == nil {
		return
	}
	xpackEnsurePlatformScenes(config)
	// A fresh config carries none of the scenes an earlier one was given.
	xpackInjectedScenes = map[string]string{}
	if e, ok := xpackActiveEntry(config); ok {
		xpackRegisterTheme(e)
	}
	// The pack's wallpaper comes back unless the config names one of its own
	// (a wallpaper set in the design file after the pack was applied wins).
	if config.Variables.Custom != nil && appWallpaperIsShippedDefault(config.Variables.BackgroundImage) {
		if path, _ := config.Variables.Custom[xpackWallpaperKey].(string); path != "" && path != xpackWallpaperNone {
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				config.Variables.BackgroundImage = path
			}
		}
	}
	xpackSyncScenes(config)
	xpackRefreshLabel(config)
}

// xpackOpenStartScene opens the pack's start screen once the first frame is up
// (main.go defers it). It stands down when anything else already holds the
// screen — the welcome tour, a resumed game — or the screen does not exist.
func xpackOpenStartScene(config *Config) {
	if config == nil || config.Variables.Custom == nil || onboarding.active {
		return
	}
	name, _ := config.Variables.Custom[xpackStartSceneKey].(string)
	name = strings.TrimSpace(name)
	if name == "" || currentSceneIndex != homeSceneIndex(config) {
		return
	}
	idx := sceneIndexExact(config, name)
	if idx < 0 || idx == currentSceneIndex {
		return
	}
	if _, blocked := offlineSceneBlock(name); blocked {
		return
	}
	changeSceneTo(config, idx)
}

// xpackQSStep applies the next (dir > 0) or previous pack from Quick Settings.
func xpackQSStep(config *Config, dir int) {
	if config == nil || dir == 0 {
		return
	}
	entries := xpackEntries()
	if len(entries) == 0 {
		return
	}
	cur := -1
	id := xpackActiveID(config)
	for i, e := range entries {
		if e.Manifest.ID == id {
			cur = i
			break
		}
	}
	next := 0
	switch {
	case cur < 0 && dir < 0:
		next = len(entries) - 1
	case cur >= 0:
		next = setCycleIndex(cur, dir, len(entries))
	}
	note, err := xpackApply(config, entries[next])
	if err != nil {
		ShowToast("Experience pack: "+err.Error(), ToastKindWarning)
		return
	}
	msg := entries[next].Manifest.Name + " is on · undo it in Experience packs"
	if note != "" {
		msg += " · skipped " + note
	}
	ShowToast(msg, ToastKindInfo)
	if scanned, _, _ := xpackScanState(); !scanned {
		xpackScanAsync()
	}
}

// ── Import and export ─────────────────────────────────────────────────────

// xpackExport writes pack e as <id>.jkpack into dir — its manifest and the
// files it ships — and returns the archive's path.
func xpackExport(e xpackEntry, dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("there is no folder to export to")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	manifest, err := json.MarshalIndent(e.Manifest, "", "  ")
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".export-*"+xpackArchiveExt)
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = os.Remove(tmpName)
		}
	}()
	zw := zip.NewWriter(tmp)
	fail := func(err error) (string, error) {
		_ = zw.Close()
		_ = tmp.Close()
		return "", err
	}
	add := func(name string, data []byte) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	if err := add(xpackManifestName, manifest); err != nil {
		return fail(err)
	}
	seen := map[string]bool{xpackManifestName: true}
	for _, rel := range e.Assets {
		if seen[rel] || e.Dir == "" {
			continue
		}
		seen[rel] = true
		data, err := os.ReadFile(filepath.Join(e.Dir, filepath.FromSlash(rel)))
		if err != nil {
			return fail(fmt.Errorf("%s: %w", rel, err))
		}
		if err := add(rel, data); err != nil {
			return fail(err)
		}
	}
	if err := zw.Close(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	final := filepath.Join(dir, e.Manifest.ID+xpackArchiveExt)
	if err := os.Rename(tmpName, final); err != nil {
		return "", err
	}
	done = true
	return final, nil
}

// xpackImport copies a .jkpack into the packs folder, replacing an earlier copy
// of the same pack, and returns the pack as loaded from there.
func xpackImport(archive string) (xpackEntry, error) {
	if !strings.EqualFold(filepath.Ext(archive), xpackArchiveExt) {
		return xpackEntry{}, errors.New("choose a .jkpack file")
	}
	dir := xpackDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return xpackEntry{}, err
	}
	target, err := xpackUnpack(archive, dir, true)
	if err != nil {
		return xpackEntry{}, err
	}
	e, err := xpackLoadEntry(target, xpackSourceUser)
	if err != nil {
		return xpackEntry{}, err
	}
	xpackScanAsync()
	return e, nil
}

// xpackImportItem is a .jkpack found where people keep files.
type xpackImportItem struct {
	Path    string
	Name    string
	Size    int64
	ModTime time.Time
	Problem string
}

// xpackFindImports lists the .jkpack files in the usual places: the card's
// backup folder and its top level, the packs folder, and Downloads on a
// computer. Newest first.
func xpackFindImports() []xpackImportItem {
	var out []xpackImportItem
	seen := map[string]bool{}
	for _, dir := range bkupSearchDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, de := range entries {
			name := de.Name()
			if de.IsDir() || !strings.EqualFold(filepath.Ext(name), xpackArchiveExt) || strings.HasPrefix(name, ".") {
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
			item := xpackImportItem{Path: full, Name: strings.TrimSuffix(name, filepath.Ext(name)), Size: info.Size(), ModTime: info.ModTime()}
			if m, err := xpackArchiveManifest(full); err != nil {
				item.Problem = err.Error()
			} else {
				item.Name = m.Name
			}
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out
}

// xpackArchiveManifest reads the manifest out of a .jkpack without unpacking it.
func xpackArchiveManifest(archive string) (xpackManifest, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return xpackManifest{}, errors.New("is not a .jkpack archive")
	}
	defer zr.Close()
	_, file := xpackArchiveRoot(zr.File)
	if file == nil {
		return xpackManifest{}, fmt.Errorf("has no %s", xpackManifestName)
	}
	data, err := xpackReadZipFile(file, xpackMaxManifestBytes)
	if err != nil {
		return xpackManifest{}, err
	}
	return xpackParseManifest(data, strings.TrimSuffix(filepath.Base(archive), filepath.Ext(archive)))
}

// xpackArchiveRoot finds pack.json in an archive: at the top, or inside the one
// folder a zipped pack folder puts everything in. It returns the prefix every
// pack file has and the manifest's entry (nil when there is none).
func xpackArchiveRoot(files []*zip.File) (string, *zip.File) {
	for _, f := range files {
		if f.Name == xpackManifestName {
			return "", f
		}
	}
	for _, f := range files {
		if strings.Count(f.Name, "/") == 1 && strings.HasSuffix(f.Name, "/"+xpackManifestName) {
			return strings.TrimSuffix(f.Name, xpackManifestName), f
		}
	}
	return "", nil
}

// xpackReadZipFile reads one archive entry, refusing more than limit bytes
// whatever the entry's header claims.
func xpackReadZipFile(f *zip.File, limit int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("%s is too large", f.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is too large", f.Name)
	}
	return data, nil
}

// xpackZipEntryName turns an archive entry's name into a safe slash path below
// the pack folder: "" for a folder entry or one outside prefix, an error for a
// name that would escape (absolute, "..", a drive or a backslash).
func xpackZipEntryName(name, prefix string) (string, error) {
	if strings.ContainsAny(name, "\\:\x00") {
		return "", fmt.Errorf("unsafe file name %q", name)
	}
	if !strings.HasPrefix(name, prefix) {
		return "", nil
	}
	rel := strings.TrimPrefix(name, prefix)
	if rel == "" || strings.HasSuffix(rel, "/") {
		return "", nil
	}
	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("unsafe file name %q", name)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || filepath.IsAbs(filepath.FromSlash(clean)) {
		return "", fmt.Errorf("unsafe file name %q", name)
	}
	return clean, nil
}

// xpackUnpack extracts a .jkpack into packsDir/<id>. With replace false an
// existing folder of that id wins and nothing is written; with replace true the
// folder is swapped for the archive's contents once they have extracted and
// validated, so a bad archive never leaves a half-written pack behind.
func xpackUnpack(archive, packsDir string, replace bool) (string, error) {
	info, err := os.Stat(archive)
	if err != nil {
		return "", err
	}
	if info.Size() > xpackMaxArchiveBytes {
		return "", fmt.Errorf("is larger than %s", humanBytes(xpackMaxArchiveBytes))
	}
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return "", errors.New("is not a .jkpack archive")
	}
	defer zr.Close()
	if len(zr.File) > xpackMaxArchiveEntries {
		return "", fmt.Errorf("holds more than %d files", xpackMaxArchiveEntries)
	}
	prefix, manifestFile := xpackArchiveRoot(zr.File)
	if manifestFile == nil {
		return "", fmt.Errorf("has no %s", xpackManifestName)
	}
	data, err := xpackReadZipFile(manifestFile, xpackMaxManifestBytes)
	if err != nil {
		return "", err
	}
	m, err := xpackParseManifest(data, strings.TrimSuffix(filepath.Base(archive), filepath.Ext(archive)))
	if err != nil {
		return "", err
	}
	for _, b := range xpackBuiltinEntries() {
		if b.Manifest.ID == m.ID {
			return "", fmt.Errorf("uses the id of the built-in %s pack", b.Manifest.Name)
		}
	}
	target := filepath.Join(packsDir, m.ID)
	if _, err := os.Stat(target); err == nil && !replace {
		return target, nil
	}
	if err := os.MkdirAll(packsDir, 0o755); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(packsDir, ".unpack-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	budget := int64(xpackMaxArchiveBytes)
	for _, f := range zr.File {
		rel, err := xpackZipEntryName(f.Name, prefix)
		if err != nil {
			return "", err
		}
		if rel == "" || f.FileInfo().IsDir() {
			continue
		}
		content, err := xpackReadZipFile(f, budget)
		if err != nil {
			return "", err
		}
		budget -= int64(len(content))
		dst := filepath.Join(staging, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, content, 0o644); err != nil {
			return "", err
		}
	}
	if _, err := xpackLoadEntry(staging, xpackSourceUser); err != nil {
		return "", err
	}
	if replace {
		if err := os.RemoveAll(target); err != nil {
			return "", err
		}
	}
	if err := os.Rename(staging, target); err != nil {
		return "", err
	}
	return target, nil
}
