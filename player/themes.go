package main

import (
	"fmt"
)

// ThemePreset represents a complete visual theme.
type ThemePreset struct {
	Name          string
	Background    string
	ButtonColor   string
	LabelColor    string
	InputColor    string
	Surface       string
	SurfaceAlt    string
	SurfaceRaised string
	TextPrimary   string
	TextSecondary string
	TextTertiary  string
	BorderSubtle  string
	BorderDefault string
	BorderFocus   string
	Success       string
	Warning       string
	Danger        string
	Info          string
	Overlay       string
}

var themePresets = map[string]ThemePreset{
	"dark": {
		Name:          "Dark",
		Background:    "#0f1115",
		ButtonColor:   "#1c2230",
		LabelColor:    "#e8eefc",
		InputColor:    "#141a26",
		Surface:       "#12141a",
		SurfaceAlt:    "#1a1d26",
		SurfaceRaised: "#222633",
		TextPrimary:   "#f5f8ff",
		TextSecondary: "#c3d0e6",
		TextTertiary:  "#919cab",
		BorderSubtle:  "#ffffff12",
		BorderDefault: "#ffffff23",
		BorderFocus:   "#ffffff78",
		Success:       "#2ecc71",
		Warning:       "#f1c40f",
		Danger:        "#e74c3c",
		Info:          "#3498db",
		Overlay:       "#0c0e1440",
	},
	"light": {
		Name:          "Light",
		Background:    "#f0f2f5",
		ButtonColor:   "#ffffff",
		LabelColor:    "#1a1d26",
		InputColor:    "#e8eaef",
		Surface:       "#ffffff",
		SurfaceAlt:    "#f8f9fb",
		SurfaceRaised: "#ffffff",
		TextPrimary:   "#1a1d26",
		TextSecondary: "#3a3f4b",
		TextTertiary:  "#6b7280",
		BorderSubtle:  "#00000010",
		BorderDefault: "#00000020",
		BorderFocus:   "#00000050",
		Success:       "#27ae60",
		Warning:       "#f39c12",
		Danger:        "#c0392b",
		Info:          "#2980b9",
		Overlay:       "#f0f2f599",
	},
	"oled": {
		Name:          "OLED",
		Background:    "#000000",
		ButtonColor:   "#0a0a0a",
		LabelColor:    "#ffffff",
		InputColor:    "#050505",
		Surface:       "#000000",
		SurfaceAlt:    "#050505",
		SurfaceRaised: "#0a0a0a",
		TextPrimary:   "#ffffff",
		TextSecondary: "#cccccc",
		TextTertiary:  "#888888",
		BorderSubtle:  "#ffffff15",
		BorderDefault: "#ffffff30",
		BorderFocus:   "#ffffff80",
		Success:       "#00ff88",
		Warning:       "#ffdd00",
		Danger:        "#ff3333",
		Info:          "#00aaff",
		Overlay:       "#000000cc",
	},
	"midnight": {
		Name:          "Midnight",
		Background:    "#090b14",
		ButtonColor:   "#151927",
		LabelColor:    "#f5f7ff",
		InputColor:    "#0f1320",
		Surface:       "#0d1018",
		SurfaceAlt:    "#151927",
		SurfaceRaised: "#1c2130",
		TextPrimary:   "#f5f7ff",
		TextSecondary: "#9aa4b8",
		TextTertiary:  "#5f6b82",
		BorderSubtle:  "#ffffff0c",
		BorderDefault: "#ffffff1a",
		BorderFocus:   "#6ee7ff66",
		Success:       "#48d597",
		Warning:       "#ffbf69",
		Danger:        "#ff647c",
		Info:          "#6ee7ff",
		Overlay:       "#05060bcc",
	},
	// hub matches the shipped home-screen palette so the whole app stays
	// coherent with the TSP home design (#090E19 background, cyan accent).
	"hub": {
		Name:          "Hub",
		Background:    "#090E19",
		ButtonColor:   "#1E293B",
		LabelColor:    "#F6F8FC",
		InputColor:    "#0F1420",
		Surface:       "#121A2A",
		SurfaceAlt:    "#172033",
		SurfaceRaised: "#22314B",
		TextPrimary:   "#F6F8FC",
		TextSecondary: "#9CA9BD",
		TextTertiary:  "#66758B",
		BorderSubtle:  "#ffffff10",
		BorderDefault: "#2A3953",
		BorderFocus:   "#55D6FF",
		Success:       "#57D68D",
		Warning:       "#FFCB66",
		Danger:        "#FF6B7A",
		Info:          "#55D6FF",
		Overlay:       "#090E19cc",
	},
	// ocean is a deep blue palette with a cyan accent, built for long
	// reading sessions on the TSP screen.
	"ocean": {
		Name:          "Ocean",
		Background:    "#0B1526",
		ButtonColor:   "#142740",
		LabelColor:    "#E8F1FF",
		InputColor:    "#0E1C31",
		Surface:       "#0E1B30",
		SurfaceAlt:    "#152943",
		SurfaceRaised: "#1C3558",
		TextPrimary:   "#F0F6FF",
		TextSecondary: "#A9BFDD",
		TextTertiary:  "#6E86A8",
		BorderSubtle:  "#ffffff0e",
		BorderDefault: "#2E4A73",
		BorderFocus:   "#4FC3FF",
		Success:       "#3DDC97",
		Warning:       "#FFC864",
		Danger:        "#FF6B7A",
		Info:          "#4FC3FF",
		Overlay:       "#0B1526cc",
	},
	// sunset is a warm dark palette with a coral accent; it keeps the deep
	// background for contrast while giving the UI a warmer personality.
	"sunset": {
		Name:          "Sunset",
		Background:    "#1A0F14",
		ButtonColor:   "#2B1A24",
		LabelColor:    "#FFF0F3",
		InputColor:    "#201219",
		Surface:       "#201219",
		SurfaceAlt:    "#2B1A24",
		SurfaceRaised: "#3A2531",
		TextPrimary:   "#FFF5F7",
		TextSecondary: "#E0B8C2",
		TextTertiary:  "#A97E8B",
		BorderSubtle:  "#ffffff12",
		BorderDefault: "#4A2F3C",
		BorderFocus:   "#FF9E7D",
		Success:       "#6EE7A8",
		Warning:       "#FFCB66",
		Danger:        "#FF6B7A",
		Info:          "#FF9E7D",
		Overlay:       "#1A0F14cc",
	},
	// highcontrast is the accessibility preset: pure black, pure white text
	// and a yellow accent, so every edge clears WCAG AAA at the TSP's size.
	"highcontrast": {
		Name:          "High Contrast",
		Background:    "#000000",
		ButtonColor:   "#000000",
		LabelColor:    "#ffffff",
		InputColor:    "#000000",
		Surface:       "#000000",
		SurfaceAlt:    "#0a0a0a",
		SurfaceRaised: "#141414",
		TextPrimary:   "#ffffff",
		TextSecondary: "#ffffff",
		TextTertiary:  "#e6e6e6",
		BorderSubtle:  "#ffffff80",
		BorderDefault: "#ffffff",
		BorderFocus:   "#ffee00",
		Success:       "#00ff5a",
		Warning:       "#ffee00",
		Danger:        "#ff2d2d",
		Info:          "#ffee00",
		Overlay:       "#000000e6",
	},
	// Colour-first presets keep the dark shell but tint every surface, so the
	// theme reads as "red" or "green" without losing legibility.
	"red": {
		Name:          "Red",
		Background:    "#140708",
		ButtonColor:   "#241014",
		LabelColor:    "#ffecec",
		InputColor:    "#1b0a0c",
		Surface:       "#1b0a0c",
		SurfaceAlt:    "#241014",
		SurfaceRaised: "#32161b",
		TextPrimary:   "#fff0f0",
		TextSecondary: "#e8b4b8",
		TextTertiary:  "#b07a80",
		BorderSubtle:  "#ff6b6b18",
		BorderDefault: "#3d1d22",
		BorderFocus:   "#ff6b6b",
		Success:       "#6ee7a8",
		Warning:       "#ffcb66",
		Danger:        "#ff3b3b",
		Info:          "#ff6b6b",
		Overlay:       "#140708cc",
	},
	"green": {
		Name:          "Green",
		Background:    "#07120c",
		ButtonColor:   "#0e2418",
		LabelColor:    "#e9fff1",
		InputColor:    "#0a1a11",
		Surface:       "#0a1a11",
		SurfaceAlt:    "#0e2418",
		SurfaceRaised: "#143020",
		TextPrimary:   "#effff5",
		TextSecondary: "#a8d8bc",
		TextTertiary:  "#6fa384",
		BorderSubtle:  "#4ade8018",
		BorderDefault: "#1d4530",
		BorderFocus:   "#4ade80",
		Success:       "#34d399",
		Warning:       "#fbbf24",
		Danger:        "#f87171",
		Info:          "#4ade80",
		Overlay:       "#07120ccc",
	},
	"blue": {
		Name:          "Blue",
		Background:    "#071426",
		ButtonColor:   "#0f2542",
		LabelColor:    "#eaf4ff",
		InputColor:    "#0a1c33",
		Surface:       "#0a1c33",
		SurfaceAlt:    "#0f2542",
		SurfaceRaised: "#153152",
		TextPrimary:   "#f2f8ff",
		TextSecondary: "#a9c6e8",
		TextTertiary:  "#6f8fb5",
		BorderSubtle:  "#60a5fa18",
		BorderDefault: "#1d4166",
		BorderFocus:   "#60a5fa",
		Success:       "#34d399",
		Warning:       "#fbbf24",
		Danger:        "#fb7185",
		Info:          "#60a5fa",
		Overlay:       "#071426cc",
	},
	// anime: deep indigo shell with a hot-pink accent, the look of a late-night
	// anime OP title card.
	"anime": {
		Name:          "Anime",
		Background:    "#12091f",
		ButtonColor:   "#2e1d52",
		LabelColor:    "#f7ecff",
		InputColor:    "#1a1030",
		Surface:       "#1a1030",
		SurfaceAlt:    "#221640",
		SurfaceRaised: "#2e1d52",
		TextPrimary:   "#fbf3ff",
		TextSecondary: "#cbb3e8",
		TextTertiary:  "#937fba",
		BorderSubtle:  "#ff5fa21c",
		BorderDefault: "#3a2760",
		BorderFocus:   "#ff5fa2",
		Success:       "#5eead4",
		Warning:       "#ffd166",
		Danger:        "#ff6b81",
		Info:          "#ff5fa2",
		Overlay:       "#12091fcc",
	},
	// cute: a light pastel shell with strawberry-milk accents.
	"cute": {
		Name:          "Cute",
		Background:    "#fff7fb",
		ButtonColor:   "#ffd6e8",
		LabelColor:    "#3d2230",
		InputColor:    "#fff0f7",
		Surface:       "#ffffff",
		SurfaceAlt:    "#ffeef6",
		SurfaceRaised: "#ffe3f0",
		TextPrimary:   "#3d2230",
		TextSecondary: "#7a5568",
		TextTertiary:  "#a98a9a",
		BorderSubtle:  "#00000010",
		BorderDefault: "#f0c4d8",
		BorderFocus:   "#ff8fb8",
		Success:       "#3fbf8f",
		Warning:       "#e0a800",
		Danger:        "#e05a72",
		Info:          "#ff7aa8",
		Overlay:       "#fff7fb99",
	},
	// girly: hot pink and rose on a warm white shell.
	"girly": {
		Name:          "Girly",
		Background:    "#fff0f7",
		ButtonColor:   "#ffc7df",
		LabelColor:    "#3a1226",
		InputColor:    "#ffe6f2",
		Surface:       "#fff6fa",
		SurfaceAlt:    "#ffe6f2",
		SurfaceRaised: "#ffd9ea",
		TextPrimary:   "#3a1226",
		TextSecondary: "#7d4a63",
		TextTertiary:  "#b07f96",
		BorderSubtle:  "#00000012",
		BorderDefault: "#ffb3d1",
		BorderFocus:   "#e0457b",
		Success:       "#2eb67d",
		Warning:       "#d99a00",
		Danger:        "#d7263d",
		Info:          "#e0457b",
		Overlay:       "#fff0f799",
	},
	// sakura: soft blossom tones for long reading sessions in daylight.
	"sakura": {
		Name:          "Sakura",
		Background:    "#fff6f4",
		ButtonColor:   "#ffdcd6",
		LabelColor:    "#3b2523",
		InputColor:    "#ffefec",
		Surface:       "#fffdfc",
		SurfaceAlt:    "#ffeae6",
		SurfaceRaised: "#ffdcd6",
		TextPrimary:   "#3b2523",
		TextSecondary: "#7a5652",
		TextTertiary:  "#a9857f",
		BorderSubtle:  "#00000010",
		BorderDefault: "#f3ccc4",
		BorderFocus:   "#ff7f9c",
		Success:       "#4fbf8b",
		Warning:       "#d99b2b",
		Danger:        "#d94f5c",
		Info:          "#ff7f9c",
		Overlay:       "#fff6f499",
	},
	// lavender: gentle violet in the light range, easier on the eyes than white.
	"lavender": {
		Name:          "Lavender",
		Background:    "#f4f1fb",
		ButtonColor:   "#e3daf7",
		LabelColor:    "#241a3a",
		InputColor:    "#efe9fa",
		Surface:       "#fbf9ff",
		SurfaceAlt:    "#efe9fa",
		SurfaceRaised: "#e3daf7",
		TextPrimary:   "#241a3a",
		TextSecondary: "#5b4a7d",
		TextTertiary:  "#8a79ac",
		BorderSubtle:  "#00000010",
		BorderDefault: "#d6c8f0",
		BorderFocus:   "#7c5cff",
		Success:       "#2fa37a",
		Warning:       "#c98f1e",
		Danger:        "#d64560",
		Info:          "#7c5cff",
		Overlay:       "#f4f1fb99",
	},
	// sand: warm paper tones, built for daylight reading.
	"sand": {
		Name:          "Sand",
		Background:    "#f6efe4",
		ButtonColor:   "#e6d6bd",
		LabelColor:    "#2f2618",
		InputColor:    "#f0e5d3",
		Surface:       "#fdf8f0",
		SurfaceAlt:    "#f0e5d3",
		SurfaceRaised: "#e6d6bd",
		TextPrimary:   "#2f2618",
		TextSecondary: "#6b5a42",
		TextTertiary:  "#9b8767",
		BorderSubtle:  "#00000010",
		BorderDefault: "#d9c7a9",
		BorderFocus:   "#b4762e",
		Success:       "#3f8f63",
		Warning:       "#b57a12",
		Danger:        "#b23f38",
		Info:          "#b4762e",
		Overlay:       "#f6efe499",
	},
	// nord: the muted arctic blue-grey palette.
	"nord": {
		Name:          "Nord",
		Background:    "#2e3440",
		ButtonColor:   "#434c5e",
		LabelColor:    "#eceff4",
		InputColor:    "#3b4252",
		Surface:       "#3b4252",
		SurfaceAlt:    "#434c5e",
		SurfaceRaised: "#4c566a",
		TextPrimary:   "#eceff4",
		TextSecondary: "#d8dee9",
		TextTertiary:  "#a3abbd",
		BorderSubtle:  "#eceff418",
		BorderDefault: "#4c566a",
		BorderFocus:   "#88c0d0",
		Success:       "#a3be8c",
		Warning:       "#ebcb8b",
		Danger:        "#bf616a",
		Info:          "#88c0d0",
		Overlay:       "#2e3440cc",
	},
	// gruvbox: the warm, high-contrast retro terminal palette.
	"gruvbox": {
		Name:          "Gruvbox",
		Background:    "#282828",
		ButtonColor:   "#3c3836",
		LabelColor:    "#fbf1c7",
		InputColor:    "#32302f",
		Surface:       "#32302f",
		SurfaceAlt:    "#3c3836",
		SurfaceRaised: "#504945",
		TextPrimary:   "#fbf1c7",
		TextSecondary: "#d5c4a1",
		TextTertiary:  "#a89984",
		BorderSubtle:  "#fbf1c718",
		BorderDefault: "#665c54",
		BorderFocus:   "#fabd2f",
		Success:       "#b8bb26",
		Warning:       "#fabd2f",
		Danger:        "#fb4934",
		Info:          "#83a598",
		Overlay:       "#282828cc",
	},
	"dracula": {
		Name:          "Dracula",
		Background:    "#282a36",
		ButtonColor:   "#44475a",
		LabelColor:    "#f8f8f2",
		InputColor:    "#21222c",
		Surface:       "#21222c",
		SurfaceAlt:    "#343746",
		SurfaceRaised: "#44475a",
		TextPrimary:   "#f8f8f2",
		TextSecondary: "#c8cad8",
		TextTertiary:  "#6272a4",
		BorderSubtle:  "#f8f8f218",
		BorderDefault: "#44475a",
		BorderFocus:   "#bd93f9",
		Success:       "#50fa7b",
		Warning:       "#f1fa8c",
		Danger:        "#ff5555",
		Info:          "#8be9fd",
		Overlay:       "#282a36cc",
	},
	// matrix: green phosphor on black, for people who want the terminal look.
	"matrix": {
		Name:          "Matrix",
		Background:    "#000600",
		ButtonColor:   "#06240c",
		LabelColor:    "#b6ffb6",
		InputColor:    "#021004",
		Surface:       "#021004",
		SurfaceAlt:    "#041a08",
		SurfaceRaised: "#06240c",
		TextPrimary:   "#c8ffc8",
		TextSecondary: "#64d364",
		TextTertiary:  "#2f913f",
		BorderSubtle:  "#00ff6620",
		BorderDefault: "#0f3d18",
		BorderFocus:   "#00ff66",
		Success:       "#00ff66",
		Warning:       "#b3ff00",
		Danger:        "#ff3d3d",
		Info:          "#00ff66",
		Overlay:       "#000600cc",
	},
	// fallout: a Vault-Tec terminal — olive-black glass with amber phosphor.
	"fallout": {
		Name:          "Fallout",
		Background:    "#0d0f07",
		ButtonColor:   "#242a12",
		LabelColor:    "#ffd27a",
		InputColor:    "#141709",
		Surface:       "#141709",
		SurfaceAlt:    "#1b1f0d",
		SurfaceRaised: "#242a12",
		TextPrimary:   "#ffd98f",
		TextSecondary: "#d9b25c",
		TextTertiary:  "#9c8340",
		BorderSubtle:  "#ffb64220",
		BorderDefault: "#3a3f1d",
		BorderFocus:   "#ffb642",
		Success:       "#9acd32",
		Warning:       "#ffcc00",
		Danger:        "#e05252",
		Info:          "#ffb642",
		Overlay:       "#0d0f07cc",
	},
	// cyberpunk: neon magenta and cyan over a violet-black shell.
	"cyberpunk": {
		Name:          "Cyberpunk",
		Background:    "#0a0416",
		ButtonColor:   "#26164a",
		LabelColor:    "#f5f0ff",
		InputColor:    "#120a24",
		Surface:       "#120a24",
		SurfaceAlt:    "#1a0f33",
		SurfaceRaised: "#26164a",
		TextPrimary:   "#f7f2ff",
		TextSecondary: "#c3a8ff",
		TextTertiary:  "#8d76b8",
		BorderSubtle:  "#00f0ff1c",
		BorderDefault: "#3a2166",
		BorderFocus:   "#00f0ff",
		Success:       "#00ffa3",
		Warning:       "#ffd166",
		Danger:        "#ff4d6d",
		Info:          "#ff2e97",
		Overlay:       "#0a0416cc",
	},
	// mono: paper-grey monochrome, no hue at all.
	"mono": {
		Name:          "Monochrome",
		Background:    "#101010",
		ButtonColor:   "#282828",
		LabelColor:    "#f2f2f2",
		InputColor:    "#161616",
		Surface:       "#161616",
		SurfaceAlt:    "#1e1e1e",
		SurfaceRaised: "#282828",
		TextPrimary:   "#f2f2f2",
		TextSecondary: "#bdbdbd",
		TextTertiary:  "#8a8a8a",
		BorderSubtle:  "#ffffff18",
		BorderDefault: "#333333",
		BorderFocus:   "#ffffff",
		Success:       "#d0d0d0",
		Warning:       "#efefef",
		Danger:        "#ff6b6b",
		Info:          "#e0e0e0",
		Overlay:       "#101010cc",
	},
	// vaporwave: sunset gradients in neon pink, cyan and lilac.
	"vaporwave": {
		Name:          "Vaporwave",
		Background:    "#140b26",
		ButtonColor:   "#371d66",
		LabelColor:    "#ffe9ff",
		InputColor:    "#1e1038",
		Surface:       "#1e1038",
		SurfaceAlt:    "#2a1650",
		SurfaceRaised: "#371d66",
		TextPrimary:   "#fff0ff",
		TextSecondary: "#ffb8e6",
		TextTertiary:  "#b98fd6",
		BorderSubtle:  "#ff71ce1c",
		BorderDefault: "#46267a",
		BorderFocus:   "#ff71ce",
		Success:       "#01cdfe",
		Warning:       "#fffb96",
		Danger:        "#ff4d6d",
		Info:          "#ff71ce",
		Overlay:       "#140b26cc",
	},
}

// GetThemePreset returns a theme preset by name, or the dark preset if not found.
func GetThemePreset(name string) ThemePreset {
	if p, ok := themePresets[name]; ok {
		return p
	}
	return themePresets["dark"]
}

// ApplyThemePreset applies a theme preset the user picked, including the
// confirmation toast.
func ApplyThemePreset(config *Config, presetName string) {
	applyThemePreset(config, presetName, true)
}

// applyThemePreset writes the preset into the config variables. notify controls
// the toast: automatic switches (battery saver, time-of-day theme) must not
// announce themselves as if the user had chosen them.
func applyThemePreset(config *Config, presetName string, notify bool) {
	preset := GetThemePreset(presetName)
	br, bg, bb := hexToRGB(preset.ButtonColor)
	config.Variables.ButtonColor = RGB{R: int(br), G: int(bg), B: int(bb)}
	lr, lg, lb := hexToRGB(preset.LabelColor)
	config.Variables.LabelColor = RGB{R: int(lr), G: int(lg), B: int(lb)}
	ir, ig, ib := hexToRGB(preset.InputColor)
	config.Variables.InputColor = RGB{R: int(ir), G: int(ig), B: int(ib)}
	config.Variables.Custom["theme_preset"] = presetName
	config.Variables.Custom["theme_background"] = preset.Background
	config.Variables.Custom["theme_surface"] = preset.Surface
	config.Variables.Custom["theme_surface_alt"] = preset.SurfaceAlt
	config.Variables.Custom["theme_surface_raised"] = preset.SurfaceRaised
	config.Variables.Custom["theme_text_primary"] = preset.TextPrimary
	config.Variables.Custom["theme_text_secondary"] = preset.TextSecondary
	config.Variables.Custom["theme_text_tertiary"] = preset.TextTertiary
	config.Variables.Custom["theme_border_subtle"] = preset.BorderSubtle
	config.Variables.Custom["theme_border_default"] = preset.BorderDefault
	config.Variables.Custom["theme_border_focus"] = preset.BorderFocus
	config.Variables.Custom["theme_success"] = preset.Success
	config.Variables.Custom["theme_warning"] = preset.Warning
	config.Variables.Custom["theme_danger"] = preset.Danger
	config.Variables.Custom["theme_info"] = preset.Info
	config.Variables.Custom["theme_overlay"] = preset.Overlay
	ApplyThemeColors(preset)
	// Cached text textures carry the old palette in their pixels; drop them so
	// the new theme is visible at once instead of after the LRU ages them out.
	ClearTextCache()
	clearHomeTextCache()
	// The shared snapshot keeps Quick Settings levels and every other persisted
	// field intact when the theme changes.
	saveUserConfig(userConfigFromVariables(config))
	if notify {
		showToast(fmt.Sprintf("Theme: %s", preset.Name), ToastInfo())
	}
}

// themePresetOrder is the canonical, stable display order for the theme
// gallery. Never derive this from map iteration: Go randomizes map order, so
// iterating themePresets would make the gallery cards shuffle every frame.
var themePresetOrder = []string{
	// Neutrals first.
	"dark", "light", "oled", "midnight", "hub", "ocean", "sunset",
	// Colour-first and character themes.
	"red", "green", "blue", "highcontrast", "anime", "cute", "girly",
	"sakura", "lavender", "sand", "nord", "gruvbox", "dracula", "matrix",
	"fallout", "cyberpunk", "mono", "vaporwave",
}

// ListThemePresets returns the names of all available theme presets in a
// stable, deterministic order (the order shown in the gallery).
func ListThemePresets() []string {
	names := make([]string, 0, len(themePresetOrder))
	for _, name := range themePresetOrder {
		if _, ok := themePresets[name]; ok {
			names = append(names, name)
		}
	}
	// Safety net: include any preset not in the canonical order (e.g. new
	// themes added without updating themePresetOrder), still deterministically.
	for name := range themePresets {
		found := false
		for _, n := range names {
			if n == name {
				found = true
				break
			}
		}
		if !found {
			names = append(names, name)
		}
	}
	return names
}
