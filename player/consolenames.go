package main

// consolenames.go — console names by region or style.
//
// JukaHub names every console one way inside: the library's canonical names
// ("Mega Drive", "PC Engine", "SNES"), which folders, config keys, the
// compatibility table and every per-console setting are keyed by. This file
// only changes the words a player reads: Settings → Theme & look → Console
// names picks International (those names, unchanged), North America
// (Genesis, TurboGrafx-16, Super Nintendo), Japan (Super Famicom, Famicom,
// Mega-CD) or Short (MD, PCE, GBA). Draw sites pass the canonical name through
// consoleDisplayName and nothing else changes; a name the table does not know
// (a plugin's console, "Continue", "Built-in") is shown as it is.
//
// The style lives in an atomic so a list built off the render thread (search)
// reads it without touching the config map; consoleNamesSync refreshes it
// from the config on the main thread (the row, a pack, startup).

import (
	"strings"
	"sync/atomic"
)

// consoleNamesKey is the setting in Variables.Custom.
const consoleNamesKey = "consoleNames"

// The stored values of the four styles.
const (
	consoleNamesIntl  = "international"
	consoleNamesNA    = "na"
	consoleNamesJapan = "japan"
	consoleNamesShort = "short"
)

// consoleNamesStyles are the row's stops, in order, with a note of what each
// one reads like.
var consoleNamesStyles = []setOption{
	{Value: consoleNamesIntl, Label: "International", Note: "Mega Drive, PC Engine, SNES"},
	{Value: consoleNamesNA, Label: "North America", Note: "Genesis, TurboGrafx-16, Super Nintendo"},
	{Value: consoleNamesJapan, Label: "Japan", Note: "Super Famicom, Famicom, Mega-CD"},
	{Value: consoleNamesShort, Label: "Short", Note: "MD, PCE, SNES, GBA"},
}

// consoleNamesNATable is North America's names where they differ.
var consoleNamesNATable = map[string]string{
	"Mega Drive":   "Genesis",
	"PC Engine":    "TurboGrafx-16",
	"PC Engine CD": "TurboGrafx-CD",
	"SNES":         "Super Nintendo",
	"32X":          "Sega 32X",
}

// consoleNamesJapanTable is Japan's names where they differ.
var consoleNamesJapanTable = map[string]string{
	"NES":     "Famicom",
	"SNES":    "Super Famicom",
	"Sega CD": "Mega-CD",
	"32X":     "Super 32X",
}

// consoleNamesShortTable is the short names: the tags handheld folders use.
var consoleNamesShortTable = map[string]string{
	"Game Boy":             "GB",
	"Game Boy Color":       "GBC",
	"Game Boy Advance":     "GBA",
	"Nintendo DS":          "NDS",
	"Nintendo 3DS":         "3DS",
	"Nintendo 64":          "N64",
	"Famicom Disk System":  "FDS",
	"Virtual Boy":          "VB",
	"Pokemon Mini":         "PKM",
	"Pokémon Mini":         "PKM",
	"Mega Drive":           "MD",
	"Sega CD":              "SCD",
	"Master System":        "SMS",
	"Game Gear":            "GG",
	"Saturn":               "SAT",
	"Dreamcast":            "DC",
	"PlayStation":          "PS1",
	"PlayStation Portable": "PSP",
	"PlayStation 2":        "PS2",
	"PC Engine":            "PCE",
	"PC Engine CD":         "PCE-CD",
	"Neo Geo":              "NEOGEO",
	"Neo Geo CD":           "NEOCD",
	"Neo Geo Pocket":       "NGP",
	"Neo Geo Pocket Color": "NGPC",
	"WonderSwan":           "WS",
	"WonderSwan Color":     "WSC",
	"Atari 2600":           "2600",
	"Atari 5200":           "5200",
	"Atari 7800":           "7800",
	"Atari Lynx":           "LYNX",
	"Atari Jaguar":         "JAGUAR",
	"GameCube":             "GC",
	"Xbox 360":             "X360",
	"ColecoVision":         "COLECO",
	"Commodore 64":         "C64",
	"Amstrad CPC":          "CPC",
	"Game & Watch":         "G&W",
}

// consoleNamesActive is the index in consoleNamesStyles of the style in force
// (0: International), set by consoleNamesSync on the main thread and read from
// anywhere.
var consoleNamesActive atomic.Int32

// consoleNamesValue is the stored style, normalised: unset or unknown reads
// as International, which is how every console was named before the setting.
func consoleNamesValue(c *Config) string {
	if c == nil || c.Variables.Custom == nil {
		return consoleNamesIntl
	}
	v, _ := c.Variables.Custom[consoleNamesKey].(string)
	return consoleNamesNormalise(v)
}

// consoleNamesNormalise maps a stored or pack-given style to one of the four,
// accepting the obvious spellings ("us", "jp", "ntsc-j", "eu").
func consoleNamesNormalise(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case consoleNamesNA, "us", "usa", "america", "north america", "northamerica", "ntsc-u":
		return consoleNamesNA
	case consoleNamesJapan, "jp", "jpn", "ja", "ntsc-j":
		return consoleNamesJapan
	case consoleNamesShort, "tags", "abbreviated":
		return consoleNamesShort
	}
	return consoleNamesIntl
}

// consoleNamesKnown reports whether v names a style (for a pack's field).
func consoleNamesKnown(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v != "" && (consoleNamesNormalise(v) != consoleNamesIntl ||
		v == consoleNamesIntl || v == "intl" || v == "eu" || v == "europe" || v == "pal")
}

// consoleNamesTableFor is a style's table, nil for International.
func consoleNamesTableFor(style string) map[string]string {
	switch style {
	case consoleNamesNA:
		return consoleNamesNATable
	case consoleNamesJapan:
		return consoleNamesJapanTable
	case consoleNamesShort:
		return consoleNamesShortTable
	}
	return nil
}

// consoleNamesSync puts the config's style in force (main thread). Lists that
// were built with the old names (Home's tabs) are told to build again.
func consoleNamesSync(c *Config) {
	v := consoleNamesValue(c)
	idx := int32(0)
	for i, opt := range consoleNamesStyles {
		if opt.Value == v {
			idx = int32(i)
		}
	}
	if consoleNamesActive.Swap(idx) != idx {
		homeTabsContentGen.Add(1)
	}
}

// consoleDisplayName is the name a console is shown under in the style in
// force; canon is the library's canonical name, which stays the key
// everywhere else.
func consoleDisplayName(canon string) string {
	idx := consoleNamesActive.Load()
	if idx <= 0 || int(idx) >= len(consoleNamesStyles) {
		return canon
	}
	if name, ok := consoleNamesTableFor(consoleNamesStyles[idx].Value)[canon]; ok {
		return name
	}
	return canon
}

// consoleNamesLabel is a style's name as the row shows it.
func consoleNamesLabel(value string) string {
	for _, opt := range consoleNamesStyles {
		if opt.Value == value {
			return opt.Label
		}
	}
	return "International"
}

// consoleNamesRow is Settings → Theme & look → Console names.
func consoleNamesRow() setRow {
	opts := func(*Config) []setOption { return consoleNamesStyles }
	return setRow{
		ID: consoleNamesKey, Label: "Console names", Kind: setKindChoice,
		Detail:     setStatic("How consoles are named on Games, Home, search and game options: International (Mega Drive, PC Engine), North America (Genesis, TurboGrafx-16, Super Nintendo), Japan (Super Famicom, Famicom, Mega-CD) or Short (MD, PCE, GBA). Folders and per-console settings keep their names."),
		Options:    opts,
		Sheet:      opts,
		SheetValue: consoleNamesValue,
		SheetTitle: "Console names",
		Current: func(c *Config) int {
			v := consoleNamesValue(c)
			for i, opt := range consoleNamesStyles {
				if opt.Value == v {
					return i
				}
			}
			return 0
		},
		Value: func(c *Config) string { return consoleNamesLabel(consoleNamesValue(c)) },
		Choose: func(c *Config, value string) {
			value = consoleNamesNormalise(value)
			setStoreString(c, consoleNamesKey, value)
			saveUserConfigDebounced(userConfigFromVariables(c))
			consoleNamesSync(c)
			setToast("Console names: " + consoleNamesLabel(value))
		},
	}
}
