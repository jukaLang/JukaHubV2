package main

// home_fonts.go — the one UI typeface.
//
// JukaHub draws every label in the bundled Inter (Inter-Regular.ttf, shipped
// next to the executable). The font paths used to fall back through DejaVu,
// Arial and Roboto, none of which JukaHub ships: on a card where Inter failed
// to open, the UI quietly switched to whatever face the system path happened
// to offer, a different one per device, and a config's per-role "fonts" map
// could point a role at any file at all. Home, the status bar and every screen
// that resolves its type through getCachedFont now render in Inter only.

import "strings"

// homeUIFontFile is the bundled UI typeface, resolved like every other asset
// (resolvePath) from the executable's folder.
const homeUIFontFile = "Inter-Regular.ttf"

// homeUIFontPath maps a configured font path to the face the UI may draw
// with. An Inter file is kept — a bundled weight such as Inter-Bold.ttf is
// still Inter — and anything else (DejaVu, Arial, Roboto, a system font) is
// replaced by the bundled Inter, so a stale config or theme cannot bring
// another typeface back onto Home or the status bar.
func homeUIFontPath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return homeUIFontFile
	}
	base := p
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if homeIsInterFile(base) {
		return p
	}
	return homeUIFontFile
}

// homeIsInterFile reports whether a font file name is a cut of Inter: Inter.ttf,
// Inter-Bold.ttf, Inter_18pt-Regular.ttf, InterVariable.ttf. A name that only
// starts with the same letters ("Interstate.ttf") is a different typeface.
func homeIsInterFile(name string) bool {
	base := strings.ToLower(strings.TrimSpace(name))
	ext := ""
	switch {
	case strings.HasSuffix(base, ".ttf"):
		ext = ".ttf"
	case strings.HasSuffix(base, ".otf"):
		ext = ".otf"
	default:
		return false
	}
	stem := strings.TrimSuffix(base, ext)
	if stem == "inter" {
		return true
	}
	for _, prefix := range []string{"inter-", "inter_", "inter ", "intervariable", "interdisplay"} {
		if strings.HasPrefix(stem, prefix) {
			return true
		}
	}
	return false
}
