package main

// themes_retro.go — the classic consoles as themes: PlayStation (PS1, PS2,
// PSP), Nintendo (Super Nintendo, Nintendo 64, GameCube) and Sega (Mega Drive,
// Dreamcast). Each is a palette in the Colour tab — like Sega Blue and Game
// Boy — and a "… Style" total conversion in the Consoles tab that adds the
// home arrangement, the glows and, where it helps, the console look
// (CarouselStyle, TileRadius, FocusRing; themeconsole.go). Colour, shape and
// arrangement only: no logos, fonts or artwork.
//
// The text steps keep the catalogue's contrast: primary text at least 9:1 on
// every surface, secondary 6.5:1, tertiary 4.5:1, and the accent (Info, which
// also draws rings and fills) at least 3:1 — the light palettes use a darkened
// accent, as Switch Light does.
//
// themes.go's init calls registerRetroThemes, so nothing here depends on the
// order the package's init functions run in.

import "strings"

// themeRetroPalettes are the console palettes, keyed by preset id.
var themeRetroPalettes = map[string]ThemePreset{
	// ps1: the grey of the original console's shell, the controller's four
	// symbol colours as the status colours, the cross's blue as the accent.
	"ps1": {
		Name:          "PS1 Grey",
		Bloom:         "#5a8be8",
		Bloom2:        "#e86a9a",
		Background:    "#d4d4d6",
		ButtonColor:   "#ececee",
		LabelColor:    "#26262b",
		InputColor:    "#c8c8cc",
		Surface:       "#e6e6e8",
		SurfaceAlt:    "#dedee0",
		SurfaceRaised: "#f0f0f2",
		TextPrimary:   "#1f1f24",
		TextSecondary: "#3d3d45",
		TextTertiary:  "#55555e",
		BorderSubtle:  "#00000012",
		BorderDefault: "#00000026",
		BorderFocus:   "#2f5fc4",
		Success:       "#1a7049",
		Warning:       "#7a5000",
		Danger:        "#c0263a",
		Info:          "#2f5fc4",
		Overlay:       "#d4d4d6cc",
	},
	// ps2: the black console under a deep-indigo night and its blue glow.
	"ps2": {
		Name:          "PS2 Black",
		Bloom:         "#2a4dff",
		Bloom2:        "#1a1f6e",
		Background:    "#05060c",
		ButtonColor:   "#151a30",
		LabelColor:    "#eef1ff",
		InputColor:    "#0a0c18",
		Surface:       "#0b0d18",
		SurfaceAlt:    "#111426",
		SurfaceRaised: "#181c33",
		TextPrimary:   "#f2f4ff",
		TextSecondary: "#b5bce0",
		TextTertiary:  "#8890b8",
		BorderSubtle:  "#ffffff10",
		BorderDefault: "#2a3160",
		BorderFocus:   "#4f7dff",
		Success:       "#3ddc97",
		Warning:       "#f5c451",
		Danger:        "#ff5a6a",
		Info:          "#4f7dff",
		Overlay:       "#05060ccc",
	},
	// psp: the handheld's menu blue, a lighter wave of it as the second glow
	// and white type over it.
	"psp": {
		Name:          "PSP Waves",
		Bloom:         "#3d8bff",
		Bloom2:        "#8fd3ff",
		Background:    "#0b2350",
		ButtonColor:   "#1a3f80",
		LabelColor:    "#ffffff",
		InputColor:    "#0d2858",
		Surface:       "#0f2c62",
		SurfaceAlt:    "#143672",
		SurfaceRaised: "#1b4386",
		TextPrimary:   "#ffffff",
		TextSecondary: "#c8d8f4",
		TextTertiary:  "#a6bce2",
		BorderSubtle:  "#ffffff14",
		BorderDefault: "#ffffff2a",
		BorderFocus:   "#ffffff",
		Success:       "#5ee0a0",
		Warning:       "#ffd166",
		Danger:        "#ff8f99",
		Info:          "#6cc4ff",
		Overlay:       "#071a3ccc",
	},
	// snes: the 16-bit console's lavender-tinged grey and its purple buttons;
	// the four face-button colours of the Japanese pad are the status colours.
	"snes": {
		Name:          "Super Nintendo",
		Bloom:         "#8b7fd6",
		Bloom2:        "#6f8fe0",
		Background:    "#d6d4de",
		ButtonColor:   "#ebe9f2",
		LabelColor:    "#2a2540",
		InputColor:    "#cac7d6",
		Surface:       "#e8e6ef",
		SurfaceAlt:    "#dfdde8",
		SurfaceRaised: "#f2f1f7",
		TextPrimary:   "#221e36",
		TextSecondary: "#3f3958",
		TextTertiary:  "#575073",
		BorderSubtle:  "#00000012",
		BorderDefault: "#00000026",
		BorderFocus:   "#5b47a8",
		Success:       "#237a3c",
		Warning:       "#7a5800",
		Danger:        "#b82a35",
		Info:          "#5b47a8",
		Overlay:       "#d6d4decc",
	},
	// n64: charcoal plastic with the four colours of the console's emblem —
	// blue as the accent, red and green as the glows, yellow as the warning.
	"n64": {
		Name:          "Nintendo 64",
		Bloom:         "#e4002b",
		Bloom2:        "#009a44",
		Background:    "#141416",
		ButtonColor:   "#2a2a31",
		LabelColor:    "#ffffff",
		InputColor:    "#18181c",
		Surface:       "#1c1c20",
		SurfaceAlt:    "#232328",
		SurfaceRaised: "#2c2c33",
		TextPrimary:   "#f7f7f9",
		TextSecondary: "#cfcfd6",
		TextTertiary:  "#9a9aa6",
		BorderSubtle:  "#ffffff10",
		BorderDefault: "#ffffff22",
		BorderFocus:   "#ffc72c",
		Success:       "#2fbf5a",
		Warning:       "#ffc72c",
		Danger:        "#ff5a5a",
		Info:          "#4a8dff",
		Overlay:       "#0c0c0ecc",
	},
	// gamecube: the cube's indigo, deep for the page and bright for the accent.
	"gamecube": {
		Name:          "GameCube Indigo",
		Bloom:         "#5b46d6",
		Bloom2:        "#2f2490",
		Background:    "#100c2a",
		ButtonColor:   "#2a2160",
		LabelColor:    "#f1eeff",
		InputColor:    "#140f33",
		Surface:       "#18133a",
		SurfaceAlt:    "#1f1948",
		SurfaceRaised: "#282058",
		TextPrimary:   "#f3f0ff",
		TextSecondary: "#c3bce8",
		TextTertiary:  "#9a91c8",
		BorderSubtle:  "#ffffff12",
		BorderDefault: "#3a3080",
		BorderFocus:   "#9d8cff",
		Success:       "#45d49a",
		Warning:       "#ffc24b",
		Danger:        "#ff6b7a",
		Info:          "#9d8cff",
		Overlay:       "#100c2acc",
	},
	// megadrive: the black console, its red and the gold of the lettering on
	// its lid; danger is orange so it never reads as the accent.
	"megadrive": {
		Name:          "Mega Drive",
		Bloom:         "#d4141e",
		Bloom2:        "#c9a227",
		Background:    "#0a0a0a",
		ButtonColor:   "#262626",
		LabelColor:    "#ffffff",
		InputColor:    "#141414",
		Surface:       "#121212",
		SurfaceAlt:    "#1a1a1a",
		SurfaceRaised: "#232323",
		TextPrimary:   "#ffffff",
		TextSecondary: "#d2d2d2",
		TextTertiary:  "#9c9c9c",
		BorderSubtle:  "#ffffff10",
		BorderDefault: "#ffffff22",
		BorderFocus:   "#ff3b3b",
		Success:       "#4ccf6a",
		Warning:       "#e8c04a",
		Danger:        "#ff8a4a",
		Info:          "#ff3b3b",
		Overlay:       "#0a0a0acc",
	},
	// dreamcast: the white console and the orange of its swirl, darkened as an
	// accent so a ring on white is still plain to see.
	"dreamcast": {
		Name:          "Dreamcast",
		Bloom:         "#ff8a3d",
		Bloom2:        "#ffc49a",
		Background:    "#f2f2f0",
		ButtonColor:   "#ffffff",
		LabelColor:    "#2b2b2b",
		InputColor:    "#e6e6e3",
		Surface:       "#ffffff",
		SurfaceAlt:    "#f7f7f5",
		SurfaceRaised: "#ffffff",
		TextPrimary:   "#242424",
		TextSecondary: "#454545",
		TextTertiary:  "#5e5e5e",
		BorderSubtle:  "#00000012",
		BorderDefault: "#00000024",
		BorderFocus:   "#c94a08",
		Success:       "#2b7a3a",
		Warning:       "#7d5200",
		Danger:        "#c01c1c",
		Info:          "#c94a08",
		Overlay:       "#f2f2f0cc",
	},
}

// themeRetroPaletteOrder is the palettes' place in the Colour tab, by maker.
var themeRetroPaletteOrder = []string{
	"ps1", "ps2", "psp", "snes", "n64", "gamecube", "megadrive", "dreamcast",
}

// themeRetroConversion is one console's total conversion: a palette above,
// the home arrangement, optional glows (empty keeps the palette's) and the
// console look. carousel takes the theme-file values ("shelf", "strip"; empty
// is the centred carousel) and only matters on the Carousel arrangement.
type themeRetroConversion struct {
	key, name, palette   string
	arrangement, summary string
	bloom, bloom2        string
	carousel             string
	radius               int32
	ring, ring2          string
}

// themeRetroConversions are the console conversions, in the Consoles tab's
// order within each maker (themeConsoleTabOrder).
var themeRetroConversions = []themeRetroConversion{
	{key: "convert-ps1", name: "PS1 Style", palette: "ps1", arrangement: homeArrangementGrid,
		summary: "Hero tile · grey shell, four symbols", radius: 6, ring: "#2f5fc4", ring2: "#c8407a"},
	{key: "convert-ps2", name: "PS2 Style", palette: "ps2", arrangement: homeArrangementCarousel,
		summary: "Carousel · black and blue glow", radius: 10, ring: "#6f9bff"},
	{key: "convert-psp", name: "PSP Style", palette: "psp", arrangement: homeArrangementCarousel,
		summary: "Icon row · wave blue", carousel: homeCarouselShelf, radius: 12, ring: "#ffffff"},
	{key: "convert-snes", name: "Super Nintendo Style", palette: "snes", arrangement: homeArrangementGrid,
		summary: "Hero tile · grey and purple", radius: 10, ring: "#5b47a8"},
	{key: "convert-n64", name: "Nintendo 64 Style", palette: "n64", arrangement: homeArrangementWidgets,
		summary: "Widgets · charcoal, four colours", radius: 6, ring: "#ffc72c", ring2: "#2fbf5a"},
	{key: "convert-gamecube", name: "GameCube Style", palette: "gamecube", arrangement: homeArrangementDashboard,
		summary: "Dashboard · indigo cube", radius: 14, ring: "#b8acff"},
	{key: "convert-megadrive", name: "Mega Drive Style", palette: "megadrive", arrangement: homeArrangementDashboard,
		summary: "Dashboard · black, red and gold", radius: 4, ring: "#ff3b3b"},
	{key: "convert-dreamcast", name: "Dreamcast Style", palette: "dreamcast", arrangement: homeArrangementCarousel,
		summary: "Tile row · white and swirl orange", carousel: homeCarouselStrip, radius: 16, ring: "#c94a08", ring2: "#e0600c"},
}

// themeConsoleTabOrder is the Consoles tab, grouped by maker: PlayStation,
// Nintendo, Xbox, Sega. A console conversion missing here (one added later)
// still shows, after these, in the catalogue's order (themeConsolePresets).
var themeConsoleTabOrder = []string{
	"convert-ps5", "convert-ps4", "convert-ps1", "convert-ps2", "convert-psp",
	"convert-switch", "convert-switch-dark", "convert-snes", "convert-n64",
	"convert-gamecube", "convert-dmg",
	"convert-xbox", "convert-xboxone",
	"convert-megadrive", "convert-dreamcast", "convert-segablue",
}

// themeConsoleExtras are the console conversions that carry no console look
// of their own (themeConsoleSpecs) but belong in the Consoles tab.
var themeConsoleExtras = map[string]bool{
	"convert-segablue": true,
	"convert-dmg":      true,
}

// themePresetIsConsole reports whether a preset id is a console look: the
// Consoles tab's, and so not the Total conversion tab's.
func themePresetIsConsole(name string) bool {
	if name == "convert-minimal" {
		return false // a look, not a console (thememinimal.go)
	}
	if _, ok := themeConsoleSpecs[name]; ok {
		return true
	}
	if themeConsoleExtras[name] {
		return true
	}
	for _, c := range themeRetroConversions {
		if c.key == name {
			return true
		}
	}
	return false
}

// registerRetroThemes adds the console palettes and their conversions to the
// catalogue and places them in themePresetOrder: the palettes after the other
// console palettes, the conversions after the other console conversions. A
// built-in id already taken is left alone, and a second call adds nothing.
func registerRetroThemes() {
	for key, p := range themeRetroPalettes {
		if _, taken := themePresets[key]; !taken {
			themePresets[key] = p
		}
	}
	keys := make([]string, 0, len(themeRetroConversions))
	for _, c := range themeRetroConversions {
		base, ok := themeRetroPalettes[c.palette]
		if !ok {
			continue
		}
		if _, taken := themePresets[c.key]; taken {
			continue
		}
		base.Name = c.name
		base.Kind = themeKindConversion
		base.Arrangement = c.arrangement
		base.Summary = c.summary
		if c.bloom != "" {
			base.Bloom = c.bloom
		}
		if c.bloom2 != "" {
			base.Bloom2 = c.bloom2
		}
		base.CarouselStyle = c.carousel
		base.TileRadius = c.radius
		base.FocusRing = c.ring
		base.FocusRing2 = c.ring2
		themePresets[c.key] = base
		keys = append(keys, c.key)
	}
	themePresetOrder = themeOrderInsertAfter(themePresetOrder, "xbox", themeRetroPaletteOrder)
	themePresetOrder = themeOrderInsertAfter(themePresetOrder, "convert-xbox", keys)
}

// themeOrderInsertAfter returns order with the ids of add that it lacks placed
// right after anchor. Without the anchor, palettes go before the first
// conversion and conversions at the end, so the catalogue still reads colours
// first.
func themeOrderInsertAfter(order []string, anchor string, add []string) []string {
	have := make(map[string]bool, len(order))
	for _, id := range order {
		have[id] = true
	}
	fresh := make([]string, 0, len(add))
	for _, id := range add {
		if !have[id] {
			fresh = append(fresh, id)
			have[id] = true
		}
	}
	if len(fresh) == 0 {
		return order
	}
	at := -1
	for i, id := range order {
		if id == anchor {
			at = i + 1
			break
		}
	}
	if at < 0 {
		at = len(order)
		if !themeIsConversionID(fresh[0]) {
			for i, id := range order {
				if themeIsConversionID(id) {
					at = i
					break
				}
			}
		}
	}
	out := make([]string, 0, len(order)+len(fresh))
	out = append(out, order[:at]...)
	out = append(out, fresh...)
	return append(out, order[at:]...)
}

// themeIsConversionID reports whether a built-in id names a total conversion.
func themeIsConversionID(id string) bool {
	return strings.HasPrefix(id, "convert-")
}
