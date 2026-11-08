package main

// themegallery.go — small pieces of the theme presets screen and of the theme
// look that live outside the palette table in themes.go.

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// themeGalleryTabFont is the type on the gallery's tab pills and the hint beside
// them: Home's 16px chip size. The theme's "small" role is 24px on a config that
// does not set it, which filled the 32px pills from edge to edge.
func themeGalleryTabFont(config *Config) *ttf.Font {
	if config == nil {
		return nil
	}
	if f := getFontSized(config, "small", 16); f != nil {
		return f
	}
	f, _ := getCachedFont(config, "small")
	return f
}

// themeDefaultBloom2 is the backdrop's second bloom when a theme names none:
// Home's violet in the bottom-right corner.
var themeDefaultBloom2 = sdl.Color{R: 124, G: 58, B: 237, A: 255}

// themeBloomCache remembers the parsed bloom pair of the last preset asked for,
// so the backdrop — drawn every frame on every screen — does not parse hex
// strings per frame. It is keyed by the preset's id and its two hex values, so
// a theme file reinstalled under the same id is picked up.
var themeBloomCache struct {
	name, hex1, hex2 string
	first, second    sdl.Color
	has1, has2       bool
}

// themeBackdropBlooms is the colour pair of the backdrop's two blooms for the
// active theme: the preset's own pair when it names one (the total conversions
// do — a synthwave conversion glows pink and orange, the Game Boy one olive),
// otherwise the shipped look: the theme's accent behind the content and Home's
// violet in the far corner.
func themeBackdropBlooms(config *Config) (sdl.Color, sdl.Color) {
	first, second := ColorAccent, themeDefaultBloom2
	if config == nil {
		config = appConfig
	}
	if config == nil || config.Variables.Custom == nil {
		return first, second
	}
	name, _ := config.Variables.Custom["theme_preset"].(string)
	if name == "" {
		return first, second
	}
	preset, ok := themePresets[name]
	if !ok {
		return first, second
	}
	c := &themeBloomCache
	if c.name != name || c.hex1 != preset.Bloom || c.hex2 != preset.Bloom2 {
		c.name, c.hex1, c.hex2 = name, preset.Bloom, preset.Bloom2
		c.first, c.has1 = parseHexColor(preset.Bloom)
		c.second, c.has2 = parseHexColor(preset.Bloom2)
	}
	if c.has1 {
		first = c.first
		first.A = 255
	}
	if c.has2 {
		second = c.second
		second.A = 255
	}
	return first, second
}
