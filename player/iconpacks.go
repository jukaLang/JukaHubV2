package main

// iconpacks.go — console icon packs.
//
// A pack is a folder of pictures on the card, <SD>/Icons/<Pack name>/, one per
// console: CrossMix's own layout keeps them in an Emus/ folder inside
// (Icons/Burst!/Emus/MD.png) and that is read first; a flat folder of PNGs
// works too. A picture may be named the way anything else names the console —
// the canonical name ("Mega Drive.png"), a TrimUI Emus folder (FC, SFC, MD,
// GBA, PS), a short name (gba.png) or an emulator ("mupen64plus.png") — in any
// case, because iconEmuMatch (iconemu.go) reads the file name the way it reads
// every emulator tile's. An optional pack.json gives a display name and an
// author.
//
// Settings → Theme & look → Console icons picks JukaHub's vector glyphs or an
// installed pack. With a pack on, every place that draws a console's emu-*
// glyph through iconDraw (icondraw.go) draws the pack's picture instead,
// scaled once to the size it is drawn at (iconpacks_draw.go); a console the
// pack has no picture for keeps its vector glyph. Consoles that share a glyph
// (Mega Drive, Sega CD and 32X) share the pack's picture for the main console.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// iconPackKey is the setting in Variables.Custom: a folder name under
	// <SD>/Icons, or "" for JukaHub's vector glyphs.
	iconPackKey = "iconPack"
	// iconPackManifestName is the optional description file.
	iconPackManifestName = "pack.json"
	// iconPackMaxManifest bounds pack.json.
	iconPackMaxManifest = 16 << 10
	// iconPackMaxPacks and iconPackMaxFiles bound a scan of the card.
	iconPackMaxPacks = 64
	iconPackMaxFiles = 1024
	// iconPackMaxFileBytes bounds one picture.
	iconPackMaxFileBytes = 4 << 20
	// iconPackRescanEvery is how stale the list of packs may get while the
	// Settings row is on screen.
	iconPackRescanEvery = 10 * time.Second
)

// iconPackInfo is one pack on the card as the Settings row lists it.
type iconPackInfo struct {
	Folder  string // the folder's name under Icons/, the stored value
	Name    string // pack.json's name, else the folder's
	Author  string
	Count   int    // consoles it has a picture for
	Preview string // preview.png beside the icons, "" when there is none
}

// iconPackLoaded is the pack in force: each emu-* glyph's picture.
type iconPackLoaded struct {
	folder string
	files  map[string]string
}

// iconPackCurrent is the pack the draw uses; nil draws the vector glyphs.
var iconPackCurrent atomic.Pointer[iconPackLoaded]

// iconPackList is the scan of <SD>/Icons the Settings row shows.
var iconPackList struct {
	mu       sync.Mutex
	packs    []iconPackInfo
	at       time.Time
	scanning bool
}

// iconPackLoad tracks the loads of the chosen pack (main thread + workers).
var iconPackLoad struct {
	mu   sync.Mutex
	want string // the folder asked for last
	gen  int
}

// iconPackRootOverride redirects the Icons folder for tests.
var iconPackRootOverride string

// iconPacksRoot is <SD>/Icons.
func iconPacksRoot() string {
	if iconPackRootOverride != "" {
		return iconPackRootOverride
	}
	return filepath.Join(saveManagerSDRoot(), "Icons")
}

// iconPackValue is the stored choice, "" for the vector glyphs.
func iconPackValue(c *Config) string {
	if c == nil || c.Variables.Custom == nil {
		return ""
	}
	v, _ := c.Variables.Custom[iconPackKey].(string)
	return iconPackCleanName(v)
}

// iconPackCleanName accepts a pack folder's bare name and nothing that walks
// out of Icons/; the names that mean "JukaHub's own" read as "".
func iconPackCleanName(v string) string {
	v = strings.TrimSpace(v)
	switch strings.ToLower(v) {
	case "", "jukahub", "vector", "none", "default":
		return ""
	}
	if len(v) > 80 || strings.ContainsAny(v, `/\`) || v == "." || v == ".." || strings.HasPrefix(v, ".") {
		return ""
	}
	return v
}

// iconPackPreferred are the file names that speak for a glyph's main console
// when a pack has pictures for several of its consoles (MD over SEGACD, FC
// over FDS, ARCADE over MAME).
var iconPackPreferred = map[string][]string{
	"emu-genesis": {"md", "megadrive", "genesis"},
	"emu-nes":     {"fc", "nes", "famicom"},
	"emu-snes":    {"sfc", "snes", "supernintendo", "superfamicom"},
	"emu-pce":     {"pce", "pcengine", "tg16", "turbografx16"},
	"emu-ps1":     {"ps", "ps1", "psx", "playstation"},
	"emu-arcade":  {"arcade", "fbneo", "mame"},
	"emu-neogeo":  {"neogeo"},
	"emu-atari":   {"atari2600", "a2600", "atari"},
	"emu-gb":      {"gb", "gameboy"},
	"emu-dc":      {"dc", "dreamcast"},
	"emu-saturn":  {"saturn", "ss", "segasaturn"},
}

// iconPackRank orders a picture among the ones that map to the same glyph:
// lower is the better match for the glyph's main console.
func iconPackRank(glyph, compact string) int {
	for i, name := range iconPackPreferred[glyph] {
		if name == compact {
			return i
		}
	}
	for _, rule := range iconEmuRules {
		if rule.glyph != glyph {
			continue
		}
		for i, key := range rule.keys {
			if key == compact {
				return 20 + i
			}
		}
	}
	return 1000
}

// iconPackCompact is a file stem reduced the way iconEmuMatch compares.
func iconPackCompact(stem string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(stem) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// iconPackIconDir is where a pack keeps its pictures: an Emus folder inside
// (CrossMix), matched in any case, or the pack folder itself.
func iconPackIconDir(dir string) string {
	items, err := os.ReadDir(dir)
	if err != nil {
		return dir
	}
	for _, it := range items {
		if it.IsDir() && strings.EqualFold(it.Name(), "Emus") {
			return filepath.Join(dir, it.Name())
		}
	}
	return dir
}

// iconPackRead reads one pack folder: its description and each glyph's
// picture. It touches no global state.
func iconPackRead(dir, folder string) (iconPackInfo, map[string]string) {
	info := iconPackInfo{Folder: folder, Name: folder}
	if data, ok := iconPackReadManifest(filepath.Join(dir, iconPackManifestName)); ok {
		var m struct {
			Name   string `json:"name"`
			Author string `json:"author"`
		}
		if json.Unmarshal(data, &m) == nil {
			if n := xpackClip(strings.TrimSpace(m.Name), 40); n != "" {
				info.Name = n
			}
			info.Author = xpackClip(strings.TrimSpace(m.Author), 60)
		}
	}
	for _, name := range []string{"preview.png", "preview.jpg", "Preview.png"} {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && fi.Mode().IsRegular() {
			info.Preview = filepath.Join(dir, name)
			break
		}
	}
	iconDir := iconPackIconDir(dir)
	items, err := os.ReadDir(iconDir)
	if err != nil {
		return info, nil
	}
	files := map[string]string{}
	ranks := map[string]int{}
	for i, it := range items {
		if i >= iconPackMaxFiles {
			break
		}
		name := it.Name()
		if it.IsDir() || strings.HasPrefix(name, ".") || !lookImageExtOK(name) {
			continue
		}
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		if strings.EqualFold(stem, "preview") {
			continue
		}
		glyph := iconEmuMatch(stem)
		if !strings.HasPrefix(glyph, "emu-") || glyph == "emu-generic" || glyph == "emu-retroarch" {
			continue
		}
		rank := iconPackRank(glyph, iconPackCompact(stem))
		if old, ok := ranks[glyph]; ok && old <= rank {
			continue
		}
		ranks[glyph] = rank
		files[glyph] = filepath.Join(iconDir, name)
	}
	info.Count = len(files)
	return info, files
}

// iconPackReadManifest reads a small pack.json, refusing a big one.
func iconPackReadManifest(path string) ([]byte, bool) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > iconPackMaxManifest {
		return nil, false
	}
	data, err := os.ReadFile(path)
	return data, err == nil
}

// iconPackScan lists the packs in root: folders with at least one console
// picture, by name.
func iconPackScan(root string) []iconPackInfo {
	items, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []iconPackInfo
	for _, it := range items {
		if len(out) >= iconPackMaxPacks {
			break
		}
		name := it.Name()
		if !it.IsDir() || iconPackCleanName(name) == "" {
			continue
		}
		info, _ := iconPackRead(filepath.Join(root, name), name)
		if info.Count == 0 {
			continue
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// iconPackPacks is the last scan, starting a fresh one in the background when
// it is older than iconPackRescanEvery (the row asks every frame).
func iconPackPacks() []iconPackInfo {
	iconPackList.mu.Lock()
	defer iconPackList.mu.Unlock()
	if !iconPackList.scanning && (iconPackList.at.IsZero() || time.Since(iconPackList.at) > iconPackRescanEvery) {
		iconPackList.scanning = true
		go func() {
			packs := iconPackScan(iconPacksRoot())
			iconPackList.mu.Lock()
			iconPackList.packs, iconPackList.at, iconPackList.scanning = packs, time.Now(), false
			iconPackList.mu.Unlock()
		}()
	}
	return iconPackList.packs
}

// iconPackSync loads the config's pack when it is not the one in force (main
// thread; the folder is read on a worker). The vector glyphs come back at
// once.
func iconPackSync(c *Config) {
	want := iconPackValue(c)
	iconPackLoad.mu.Lock()
	if want == iconPackLoad.want {
		iconPackLoad.mu.Unlock()
		return
	}
	iconPackLoad.want = want
	iconPackLoad.gen++
	gen := iconPackLoad.gen
	iconPackLoad.mu.Unlock()
	if want == "" {
		iconPackCurrent.Store(nil)
		return
	}
	go func() {
		_, files := iconPackRead(filepath.Join(iconPacksRoot(), want), want)
		iconPackLoad.mu.Lock()
		defer iconPackLoad.mu.Unlock()
		if gen != iconPackLoad.gen {
			return
		}
		if len(files) == 0 {
			iconPackCurrent.Store(nil)
			return
		}
		iconPackCurrent.Store(&iconPackLoaded{folder: want, files: files})
	}()
}

// iconPackOptions are the row's stops: the vector glyphs, then each pack.
func iconPackOptions(c *Config) []setOption {
	packs := iconPackPacks()
	opts := make([]setOption, 0, len(packs)+2)
	opts = append(opts, setOption{Value: "", Label: "JukaHub (vector)", Note: "The built-in console glyphs, in the theme's colours"})
	cur := iconPackValue(c)
	listed := cur == ""
	for _, p := range packs {
		note := pluralizeCount(p.Count, "console", "consoles")
		if p.Author != "" {
			note += " · by " + p.Author
		}
		opts = append(opts, setOption{Value: p.Folder, Label: p.Name, Note: note})
		if p.Folder == cur {
			listed = true
		}
	}
	if !listed {
		// The chosen pack's folder is gone or not scanned yet: still a stop,
		// so the row does not jump to another pack on the next press.
		opts = append(opts, setOption{Value: cur, Label: cur, Note: "Not found in Icons on the card"})
	}
	return opts
}

// iconPackLabel is the row's read-out.
func iconPackLabel(c *Config) string {
	cur := iconPackValue(c)
	if cur == "" {
		return "JukaHub (vector)"
	}
	for _, p := range iconPackPacks() {
		if p.Folder == cur {
			return p.Name
		}
	}
	return cur
}

// iconPackRow is Settings → Theme & look → Console icons.
func iconPackRow() setRow {
	return setRow{
		ID: iconPackKey, Label: "Console icons", Kind: setKindChoice,
		Detail:     setStatic("The pictures consoles wear on Games, Home and Emulators: JukaHub's vector glyphs, or an icon pack from the card's Icons folder (Icons/<pack>/Emus/MD.png, the CrossMix layout, or a flat folder of PNGs named after each console). A console the pack has no picture for keeps its glyph."),
		Options:    iconPackOptions,
		Sheet:      iconPackOptions,
		SheetValue: iconPackValue,
		SheetTitle: "Console icons",
		Current: func(c *Config) int {
			cur := iconPackValue(c)
			for i, opt := range iconPackOptions(c) {
				if opt.Value == cur {
					return i
				}
			}
			return 0
		},
		Value: iconPackLabel,
		Choose: func(c *Config, value string) {
			value = iconPackCleanName(value)
			setStoreString(c, iconPackKey, value)
			saveUserConfigDebounced(userConfigFromVariables(c))
			iconPackSync(c)
			setToast("Console icons: " + iconPackLabel(c))
		},
	}
}
