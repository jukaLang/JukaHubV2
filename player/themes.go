package main

import (
	"fmt"
)

// ThemePreset represents a complete visual theme.
type ThemePreset struct {
	Name           string
	Background     string
	ButtonColor    string
	LabelColor     string
	InputColor     string
	Surface        string
	SurfaceAlt     string
	SurfaceRaised  string
	TextPrimary    string
	TextSecondary  string
	TextTertiary   string
	BorderSubtle   string
	BorderDefault  string
	BorderFocus    string
	Success        string
	Warning        string
	Danger         string
	Info           string
	Overlay        string
}

var themePresets = map[string]ThemePreset{
	"dark": {
		Name:           "Dark",
		Background:     "#0f1115",
		ButtonColor:    "#1c2230",
		LabelColor:     "#e8eefc",
		InputColor:     "#141a26",
		Surface:        "#12141a",
		SurfaceAlt:     "#1a1d26",
		SurfaceRaised:  "#222633",
		TextPrimary:    "#f5f8ff",
		TextSecondary:  "#c3d0e6",
		TextTertiary:   "#919cab",
		BorderSubtle:   "#ffffff12",
		BorderDefault:  "#ffffff23",
		BorderFocus:    "#ffffff78",
		Success:        "#2ecc71",
		Warning:        "#f1c40f",
		Danger:         "#e74c3c",
		Info:           "#3498db",
		Overlay:        "#0c0e1440",
	},
	"light": {
		Name:           "Light",
		Background:     "#f0f2f5",
		ButtonColor:    "#ffffff",
		LabelColor:     "#1a1d26",
		InputColor:     "#e8eaef",
		Surface:        "#ffffff",
		SurfaceAlt:     "#f8f9fb",
		SurfaceRaised:  "#ffffff",
		TextPrimary:    "#1a1d26",
		TextSecondary:  "#3a3f4b",
		TextTertiary:   "#6b7280",
		BorderSubtle:   "#00000010",
		BorderDefault:  "#00000020",
		BorderFocus:    "#00000050",
		Success:        "#27ae60",
		Warning:        "#f39c12",
		Danger:         "#c0392b",
		Info:           "#2980b9",
		Overlay:        "#f0f2f599",
	},
	"oled": {
		Name:           "OLED",
		Background:     "#000000",
		ButtonColor:    "#0a0a0a",
		LabelColor:     "#ffffff",
		InputColor:     "#050505",
		Surface:        "#000000",
		SurfaceAlt:     "#050505",
		SurfaceRaised:  "#0a0a0a",
		TextPrimary:    "#ffffff",
		TextSecondary:  "#cccccc",
		TextTertiary:   "#888888",
		BorderSubtle:   "#ffffff15",
		BorderDefault:  "#ffffff30",
		BorderFocus:    "#ffffff80",
		Success:        "#00ff88",
		Warning:        "#ffdd00",
		Danger:         "#ff3333",
		Info:           "#00aaff",
		Overlay:        "#000000cc",
	},
}

// GetThemePreset returns a theme preset by name, or the dark preset if not found.
func GetThemePreset(name string) ThemePreset {
	if p, ok := themePresets[name]; ok {
		return p
	}
	return themePresets["dark"]
}

// ApplyThemePreset applies a theme preset to the config variables.
func ApplyThemePreset(config *Config, presetName string) {
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
	user := &UserConfig{
		Variables: UserVariables{
			ButtonColor:        config.Variables.ButtonColor,
			LabelColor:         config.Variables.LabelColor,
			InputColor:         config.Variables.InputColor,
			Fullscreen:         config.Variables.Fullscreen,
			FileExplorerRoot:   config.Variables.FileExplorerRoot,
			WeatherEnabled:     config.Variables.WeatherEnabled,
			WeatherUnit:        config.Variables.WeatherUnit,
			TSPUsername:        config.Variables.TSPUsername,
			PlaybackResolution: config.Variables.PlaybackResolution,
			AudioBackend:       config.Variables.AudioBackend,
			Custom:             config.Variables.Custom,
		},
	}
	saveUserConfig(user)
	showToast(fmt.Sprintf("Theme: %s", preset.Name), ToastInfo())
}

// ListThemePresets returns the names of all available theme presets.
func ListThemePresets() []string {
	names := make([]string, 0, len(themePresets))
	for name := range themePresets {
		names = append(names, name)
	}
	return names
}
