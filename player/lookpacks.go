package main

// lookpacks.go — theme packs, as part of the Experience packs.
//
// A theme pack applies, with one press, a theme, a boot logo, an icon
// pack, a background pack, the emulator naming and the message style. JukaHub
// already had one-press packs with an Undo — the Experience packs (xpack.go,
// on the Themes gallery's Experience tab) set the theme, wallpaper, sounds,
// Home layout and start screen — so the theme-pack parts join the same pack.json
// rather than a second kind of pack:
//
//	"iconPack":     a folder of console pictures in <SD>/Icons (iconpacks.go),
//	                or "jukahub" for the vector glyphs;
//	"consoleNames": international, na, japan or short (consolenames.go);
//	"menuColour":   one of Menu colour's names, or "theme" (menucolour.go);
//	"startupSound": true or false, the start-up and shutdown chime;
//	"bootLogo":     a PNG or JPEG in the pack, staged for Settings → Boot logo,
//	                which writes it only after its own two-press confirm;
//	"preview":      a picture the gallery card shows instead of the composed
//	                miniature (preview.png beside pack.json is found alone).
//
// Every field goes through the setting's own key, the keys are part of the
// pack's Undo record (xpackStateKeys), and a field that does not validate is
// dropped and named on the card, the way xpackValidate treats its own. Packs
// are read from <data>/packs as before and also from <SD>/Themes/Packs. Three
// built-in packs are made only from built-in themes and settings.

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// lookpackBootLogoKey is the boot logo the pack in force carries, staged
	// for the Boot logo row (bookkeeping: cfgdocRuntimeKeys).
	lookpackBootLogoKey = "lookpackBootLogo"
	// lookpackMaxBootLogoBytes and lookpackMaxPreviewBytes bound a pack's
	// pictures.
	lookpackMaxBootLogoBytes = 8 << 20
	lookpackMaxPreviewBytes  = 4 << 20
	// lookpackIconVector is a pack's way of asking for the vector glyphs.
	lookpackIconVector = "jukahub"
)

func init() {
	cfgdocRuntimeKeys[lookpackBootLogoKey] = true
}

// lookpackFields are the theme-pack parts of pack.json, embedded in
// xpackManifest so they are read, kept and exported with the rest. Every one
// is optional: empty leaves that setting as it is.
type lookpackFields struct {
	BootLogo     string `json:"bootLogo,omitempty"`
	IconPack     string `json:"iconPack,omitempty"`
	ConsoleNames string `json:"consoleNames,omitempty"`
	MenuColour   string `json:"menuColour,omitempty"`
	MenuColor    string `json:"menuColor,omitempty"`
	StartupSound *bool  `json:"startupSound,omitempty"`
	Preview      string `json:"preview,omitempty"`

	// bootLogoPath and previewPath are the validated files, absolute.
	bootLogoPath string
	previewPath  string
}

// lookpackValidate checks the theme-pack fields of e (xpackValidate calls it
// last). Pure: no global state is read or written.
func lookpackValidate(e *xpackEntry) {
	l := &e.Manifest.lookpackFields
	drop := func(what, value, why string) {
		e.Problems = append(e.Problems, what+" "+xpackClip(value, 60)+": "+why)
	}
	asset := func(rel string) {
		e.Assets = append(e.Assets, filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel))))
	}
	l.bootLogoPath, l.previewPath = "", ""
	if b := strings.TrimSpace(l.BootLogo); b != "" {
		path, err := xpackResolveAsset(e.Dir, b, lookImageExts, lookpackMaxBootLogoBytes)
		if err != nil {
			drop("boot logo", b, err.Error())
			l.BootLogo = ""
		} else {
			l.BootLogo, l.bootLogoPath = b, path
			asset(b)
		}
	} else {
		l.BootLogo = ""
	}
	if v := strings.TrimSpace(l.IconPack); v != "" {
		switch name := iconPackCleanName(v); {
		case name != "":
			l.IconPack = name
		case iconPackVectorName(v):
			l.IconPack = lookpackIconVector
		default:
			drop("icon pack", v, "is not a folder name in Icons")
			l.IconPack = ""
		}
	} else {
		l.IconPack = ""
	}
	if v := strings.TrimSpace(l.ConsoleNames); v != "" {
		if consoleNamesKnown(v) {
			l.ConsoleNames = consoleNamesNormalise(v)
		} else {
			drop("console names", v, "is not international, na, japan or short")
			l.ConsoleNames = ""
		}
	} else {
		l.ConsoleNames = ""
	}
	colour := strings.ToLower(strings.TrimSpace(l.MenuColour))
	if colour == "" {
		colour = strings.ToLower(strings.TrimSpace(l.MenuColor))
	}
	l.MenuColour, l.MenuColor = "", ""
	if colour != "" {
		if colour == menuColourTheme || menuColourHexFor(colour) != "" {
			l.MenuColour = colour
		} else {
			drop("menu colour", colour, "is not one of Menu colour's names or theme")
		}
	}
	if e.Dir == "" {
		l.Preview = ""
		return
	}
	named := strings.TrimSpace(l.Preview)
	rel := named
	if rel == "" {
		rel = "preview.png"
	}
	if path, err := xpackResolveAsset(e.Dir, rel, lookImageExts, lookpackMaxPreviewBytes); err == nil {
		l.previewPath = path
		asset(rel)
	} else if named != "" {
		drop("preview", named, err.Error())
		l.Preview = ""
	}
}

// iconPackVectorName reports whether v is a pack's word for the vector glyphs.
func iconPackVectorName(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case lookpackIconVector, "vector", "none", "default":
		return true
	}
	return false
}

// lookpackStateKeys are the settings the theme-pack fields change, for the
// pack's Undo record.
func lookpackStateKeys() []string {
	return []string{consoleNamesKey, iconPackKey, menuColourKey, bootChimeEnabledKey, lookpackBootLogoKey}
}

// lookpackApply sets the theme-pack fields of e (xpackApply calls it after the
// theme and wallpaper, before the Undo record is stored) and returns what it
// could not apply. The built-in Default pack also puts the shipped names,
// glyphs and menu colour back, since it promises JukaHub as it ships.
func lookpackApply(config *Config, e xpackEntry) []string {
	l := e.Manifest.lookpackFields
	if e.Source == xpackSourceBuiltin && e.Manifest.ID == xpackThemeDefault {
		l.IconPack, l.ConsoleNames, l.MenuColour = lookpackIconVector, consoleNamesIntl, menuColourTheme
	}
	var skipped []string
	switch l.IconPack {
	case "":
	case lookpackIconVector:
		setStoreString(config, iconPackKey, "")
	default:
		if dirExists(filepath.Join(iconPacksRoot(), l.IconPack)) {
			setStoreString(config, iconPackKey, l.IconPack)
		} else {
			skipped = append(skipped, "icon pack "+l.IconPack+" (not in Icons on the card)")
		}
	}
	if l.ConsoleNames != "" {
		setStoreString(config, consoleNamesKey, l.ConsoleNames)
	}
	if l.MenuColour != "" {
		setStoreString(config, menuColourKey, l.MenuColour)
	}
	if l.StartupSound != nil {
		setStoreBool(config, bootChimeEnabledKey, *l.StartupSound)
	}
	if l.bootLogoPath != "" {
		setStoreString(config, lookpackBootLogoKey, l.bootLogoPath)
	} else if config.Variables.Custom != nil {
		delete(config.Variables.Custom, lookpackBootLogoKey)
	}
	consoleNamesSync(config)
	iconPackSync(config)
	if l.MenuColour != "" {
		menuColourRepaint(config)
	}
	return skipped
}

// lookpackAfterUndo follows an Undo that put the settings back (xpackUndo
// repaints the menu colour itself).
func lookpackAfterUndo(config *Config) {
	consoleNamesSync(config)
	iconPackSync(config)
}

// lookpackEnsure puts the stored names and icon pack in force at startup and
// after a config reload (xpackEnsure calls it).
func lookpackEnsure(config *Config) {
	consoleNamesSync(config)
	iconPackSync(config)
}

// lookpackDescribeLines is what the theme-pack fields of e change, for the
// gallery's card.
func lookpackDescribeLines(e xpackEntry) []string {
	l := e.Manifest.lookpackFields
	var lines []string
	switch l.IconPack {
	case "":
	case lookpackIconVector:
		lines = append(lines, "Console icons: JukaHub's vector glyphs")
	default:
		lines = append(lines, "Console icons: "+l.IconPack+" (from Icons on the card)")
	}
	if l.ConsoleNames != "" {
		for _, opt := range consoleNamesStyles {
			if opt.Value == l.ConsoleNames {
				lines = append(lines, "Console names: "+opt.Label+" ("+opt.Note+")")
			}
		}
	}
	if l.MenuColour != "" {
		lines = append(lines, "Menu colour: "+menuColourLabel(l.MenuColour))
	}
	if l.StartupSound != nil {
		lines = append(lines, "Startup & shutdown sound: "+strings.ToLower(setOnOff(*l.StartupSound)))
	}
	if l.bootLogoPath != "" {
		lines = append(lines, "Boot logo: "+filepath.Base(l.bootLogoPath)+" · ready in Settings → Boot logo, written only when confirmed there")
	}
	return lines
}

// lookpackBuiltinSource are the theme packs that ship, made only of
// built-in themes and settings, parsed with the Experience packs' built-ins.
const lookpackBuiltinSource = `[
  {
    "id": "super-famicom",
    "name": "Super Famicom",
    "description": "The Super Nintendo look with the names Japan knew: Super Famicom, Famicom and Mega-CD.",
    "author": "JukaHub",
    "theme": "convert-snes",
    "consoleNames": "japan"
  },
  {
    "id": "genesis",
    "name": "Genesis",
    "description": "The Mega Drive look, named the North American way: Genesis, TurboGrafx-16 and Super Nintendo.",
    "author": "JukaHub",
    "theme": "convert-megadrive",
    "consoleNames": "na"
  },
  {
    "id": "pocket-tags",
    "name": "Pocket Tags",
    "description": "True black, a lime accent and short console tags (GBA, MD, PCE) that fit small screens.",
    "author": "JukaHub",
    "theme": "oled",
    "consoleNames": "short",
    "menuColour": "lime"
  }
]`

// lookpackBuiltinRaw is lookpackBuiltinSource as one raw message per pack.
func lookpackBuiltinRaw() []json.RawMessage {
	var list []json.RawMessage
	if err := json.Unmarshal([]byte(lookpackBuiltinSource), &list); err != nil {
		LogScene("xpack").Error("built-in look packs do not parse", "err", err)
		return nil
	}
	return list
}

// lookpackSDDir is <SD>/Themes/Packs, the card's theme-pack folder.
func lookpackSDDir() string {
	return filepath.Join(saveManagerSDRoot(), "Themes", "Packs")
}

// lookpackScanSD adds the packs in <SD>/Themes/Packs to a scan of
// <data>/packs (xpackScanAsync calls it on its worker). A pack whose id is
// already taken is named in problems instead of shadowing the first.
func lookpackScanSD(entries []xpackEntry, problems []string) ([]xpackEntry, []string) {
	dir := lookpackSDDir()
	if xpackDirOverride != "" || filepath.Clean(dir) == filepath.Clean(xpackDir()) || !dirExists(dir) {
		return entries, problems
	}
	more, moreProblems := xpackScanUserPacks(dir)
	taken := map[string]bool{}
	for _, b := range xpackBuiltinEntries() {
		taken[b.Manifest.ID] = true
	}
	for _, e := range entries {
		taken[e.Manifest.ID] = true
	}
	added := false
	for _, e := range more {
		if taken[e.Manifest.ID] {
			problems = append(problems, "Themes/Packs/"+filepath.Base(e.Dir)+": another pack already uses the id "+e.Manifest.ID)
			continue
		}
		taken[e.Manifest.ID] = true
		entries = append(entries, e)
		added = true
	}
	for _, p := range moreProblems {
		problems = append(problems, "Themes/Packs/"+p)
	}
	if added {
		sort.SliceStable(entries, func(i, j int) bool {
			return strings.ToLower(entries[i].Manifest.Name) < strings.ToLower(entries[j].Manifest.Name)
		})
	}
	return entries, problems
}
