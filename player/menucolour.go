package main

// menucolour.go — Settings → Theme & look → Menu colour: one colour for focus
// rings, pills, progress bars and highlights, whatever theme is on.
//
// Every theme preset names its own accent (ThemePreset.Info), and until now the
// only way to change the colour of the highlight was to change the whole theme.
// Menu colour keeps the theme — its backgrounds, surfaces and text — and swaps
// only the accent for one of ten named colours, or leaves it alone ("Theme's
// own", the default).
//
// How it is applied: ApplyThemeColors (design.go) passes every preset through
// menuColourOverride first, which puts the chosen colour in the preset's Info
// field and clears a console look's own focus ring (FocusRing / FocusRing2,
// themeconsole.go), so the ring follows the pick too. Because the override sits
// inside ApplyThemeColors, everything that repaints the theme keeps it: picking
// another preset, the light & dark schedule (themeschedule.go), Battery Saver's
// OLED switch, plugins and experience packs (plugx.go, xpack.go), restoring a
// backup (bkup.go) and a config reload (hotreload.go). The readability nudges
// are untouched: glassAccent still lifts a colour that sits too close to the
// background (focusColorForAccent) and FocusInk still keeps rings at 3:1, so
// White on the Light theme is darkened for the ring instead of vanishing, and
// accent text goes through TextInk as before.
//
// The choice is a Custom string ("menuColour": "theme" or a colour's name),
// read from appConfig when the theme is applied. Startup and a config reload
// call menuColourSync once the config is in place, which repaints only when the
// colour on screen is not the stored one; the Settings row repaints at once.
// The ten colours are mid tones that read on dark and light themes alike.

import "strings"

// menuColourKey is the setting in Variables.Custom.
const menuColourKey = "menuColour"

// menuColourTheme is the stored value of "Theme's own".
const menuColourTheme = "theme"

// menuColourChoice is one colour the row offers.
type menuColourChoice struct {
	value, label, hex string
}

// menuColourChoices are the named colours, in the order the row steps them.
var menuColourChoices = []menuColourChoice{
	{"sky", "Sky", "#0ea5e9"},
	{"mint", "Mint", "#10b981"},
	{"lime", "Lime", "#84cc16"},
	{"amber", "Amber", "#f59e0b"},
	{"coral", "Coral", "#f97352"},
	{"rose", "Rose", "#ec4899"},
	{"violet", "Violet", "#8b5cf6"},
	{"indigo", "Indigo", "#6366f1"},
	{"teal", "Teal", "#14b8a6"},
	{"white", "White", "#ffffff"},
}

// menuColourApplied is the colour the last ApplyThemeColors used ("" for the
// theme's own). Main thread, like ApplyThemeColors.
var menuColourApplied string

// menuColourValue is the stored choice, normalised: a known colour's name, or
// menuColourTheme for anything else (unset, an old or misspelt value).
func menuColourValue(c *Config) string {
	if c == nil || c.Variables.Custom == nil {
		return menuColourTheme
	}
	v, _ := c.Variables.Custom[menuColourKey].(string)
	v = strings.ToLower(strings.TrimSpace(v))
	for _, ch := range menuColourChoices {
		if ch.value == v {
			return v
		}
	}
	return menuColourTheme
}

// menuColourHexFor is a choice's #rrggbb, "" for the theme's own.
func menuColourHexFor(value string) string {
	for _, ch := range menuColourChoices {
		if ch.value == value {
			return ch.hex
		}
	}
	return ""
}

// menuColourLabel is a choice's name as the row shows it.
func menuColourLabel(value string) string {
	for _, ch := range menuColourChoices {
		if ch.value == value {
			return ch.label
		}
	}
	return "Theme's own"
}

// menuColourOverride is the preset ApplyThemeColors paints: the same one with
// the chosen colour as its accent, or untouched with "Theme's own".
func menuColourOverride(p ThemePreset) ThemePreset {
	hex := menuColourHexFor(menuColourValue(appConfig))
	menuColourApplied = hex
	if hex == "" {
		return p
	}
	p.Info = hex
	// A console look's own ring (a Switch red, a PlayStation blue) would keep
	// the old colour on Home's focused tile; with it cleared the ring is the
	// accent (homeFocusRingColor).
	p.FocusRing, p.FocusRing2 = "", ""
	return p
}

// menuColourRepaint applies the theme in force again, so a new menu colour (or
// the theme's own, back) shows at once. A config with no preset is drawn in the
// built-in palette, which is the hub preset's (clearThemePreset).
func menuColourRepaint(c *Config) {
	if name := themePresetName(c); name != "" {
		ApplyThemeColors(GetThemePreset(name))
	} else {
		ApplyThemeColors(GetThemePreset("hub"))
	}
	SetAccentColor(themeAccentColor())
	// Text and shape textures carry the old accent in their pixels.
	ClearTextCache()
	ClearShapeCache()
	ClearIconAtlas()
	clearHomeTextCache()
}

// menuColourSync repaints when the stored menu colour is not the one on
// screen: at startup (where no preset may have been applied at all) and after
// a config reload. It must run once appConfig is the config passed in, because
// ApplyThemeColors reads the choice from appConfig.
func menuColourSync(c *Config) {
	if c == nil || c != appConfig {
		return
	}
	if menuColourHexFor(menuColourValue(c)) != menuColourApplied {
		menuColourRepaint(c)
	}
}

// menuColourOptions are the row's stops: the theme's own first, then the ten
// colours, each with its swatch. "Theme's own" shows the theme's colour.
func menuColourOptions(*Config) []setOption {
	opts := make([]setOption, 0, len(menuColourChoices)+1)
	themeHex := ""
	if p, ok := themePresets[themePresetName(appConfig)]; ok {
		themeHex = p.Info
	} else if p, ok := themePresets["hub"]; ok {
		themeHex = p.Info
	}
	opts = append(opts, setOption{Value: menuColourTheme, Label: "Theme's own",
		Note: "The colour the theme comes with", Swatch: themeHex})
	for _, ch := range menuColourChoices {
		opts = append(opts, setOption{Value: ch.value, Label: ch.label, Swatch: ch.hex})
	}
	return opts
}

// menuColourRow is Settings → Theme & look → Menu colour.
func menuColourRow() setRow {
	return setRow{
		ID: menuColourKey, Label: "Menu colour", Kind: setKindChoice,
		Detail:     setStatic("The colour of highlights, rings and bars; the theme stays"),
		Options:    menuColourOptions,
		Sheet:      menuColourOptions,
		SheetValue: menuColourValue,
		SheetTitle: "Menu colour",
		Current: func(c *Config) int {
			v := menuColourValue(c)
			for i, opt := range menuColourOptions(c) {
				if opt.Value == v {
					return i
				}
			}
			return 0
		},
		Value: func(c *Config) string { return menuColourLabel(menuColourValue(c)) },
		Choose: func(c *Config, value string) {
			if value != menuColourTheme && menuColourHexFor(value) == "" {
				value = menuColourTheme
			}
			setStoreString(c, menuColourKey, value)
			saveUserConfigDebounced(userConfigFromVariables(c))
			menuColourRepaint(c)
			setToast("Menu colour: " + menuColourLabel(value))
		},
	}
}
