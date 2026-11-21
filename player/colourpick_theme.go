package main

// colourpick_theme.go — Settings → Theme & look's own colours on top of any
// theme: Background, Surface, Text and Focus ring colour
// rows, eight colour palettes that set them together with Menu colour, a
// Reset colours row, and Glass opacity.
//
// The colours are overrides, not a theme: each row's "Theme's own" (the
// default) stores "" and leaves the preset's colour alone. They are applied in
// ApplyThemeColors (design.go) right after Menu colour's accent
// (menucolour.go), so everything that repaints the theme keeps them: another
// preset, the light & dark schedule, Battery Saver's OLED switch, experience
// and look packs, a restored backup, a profile switch and a config reload.
// High contrast wins over them (its black-and-white palette is the point).
//
// One background colour has to carry the page with it, so a lone Background
// override also re-derives the theme's three surfaces and two borders from it
// (a light page under dark cards read as holes), and when the theme's text no
// longer reads on the new page (under 4.5:1) the text follows to black or
// white. A Text colour derives the secondary and tertiary text by mixing it
// toward the background. Focus ring colour paints every ring: Home's tiles
// through the preset's FocusRing (themeconsole.go) and every other screen's
// through glassFocusRingColour (drawGlassFocusRing, ui_glass.go).
//
// Accent colour is Menu colour itself (key menuColour), which now also keeps a
// custom #rrggbb from the picker: one stored accent, not two.
//
// Glass opacity scales the fills of the glass panes and the selection pill
// (glassFill*, homeCardFill*, the Tabs Home's pill) and the Realistic glass
// frost, from 10 % to 100 % (the default, today's look). Reduce transparency
// forces 100 %. The value is cached in glassOpacity, because the fills are
// read many times a frame.

import (
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// The override keys in Variables.Custom: "#rrggbb", or "" for the theme's own.
const (
	colourBackgroundKey = "colourBackground"
	colourSurfaceKey    = "colourSurface"
	colourTextKey       = "colourText"
	colourFocusKey      = "colourFocus"
	// glassOpacityKey is Glass opacity in percent, "10".."100".
	glassOpacityKey = "mainColourOpacity"
)

// The override slots, in row order.
const (
	colourSlotBackground = iota
	colourSlotSurface
	colourSlotText
	colourSlotFocus
	colourSlotCount
)

// colourOverrideSlot is one override row's facts.
type colourOverrideSlot struct {
	key, label, detail string
	// theme is the slot's colour in the preset in force.
	theme func(c *Config, p ThemePreset) string
	// of is the slot's colour in a palette.
	of func(p colourPalette) string
	// against is the colour the slot is read with, for the picker's contrast
	// line, and the ratio it should reach.
	against     func() (sdl.Color, string)
	minContrast float64
}

// colourSlot builds slot i. Built per call, not a package variable: the
// closures reach the theme tokens, and a package-level table of them is the
// kind of initializer Go reports as a cycle.
func colourSlot(i int) colourOverrideSlot {
	onText := func() (sdl.Color, string) { return ColorTextPrimary(), "the text" }
	onPage := func() (sdl.Color, string) { return ColorBackground, "the background" }
	switch i {
	case colourSlotBackground:
		return colourOverrideSlot{
			key:         colourBackgroundKey,
			label:       "Background colour",
			detail:      "The page under everything; cards and borders follow it",
			theme:       func(_ *Config, p ThemePreset) string { return p.Background },
			of:          func(p colourPalette) string { return p.bg },
			against:     onText,
			minContrast: textInkMinContrast,
		}
	case colourSlotSurface:
		return colourOverrideSlot{
			key:         colourSurfaceKey,
			label:       "Surface colour",
			detail:      "Panels, bars and raised cards",
			theme:       func(_ *Config, p ThemePreset) string { return p.Surface },
			of:          func(p colourPalette) string { return p.surface },
			against:     onText,
			minContrast: textInkMinContrast,
		}
	case colourSlotText:
		return colourOverrideSlot{
			key:         colourTextKey,
			label:       "Text colour",
			detail:      "Labels and lists; dimmer lines are mixed from it",
			theme:       func(_ *Config, p ThemePreset) string { return p.TextPrimary },
			of:          func(p colourPalette) string { return p.text },
			against:     onPage,
			minContrast: textInkMinContrast,
		}
	}
	return colourOverrideSlot{
		key:         colourFocusKey,
		label:       "Focus ring colour",
		detail:      "The ring around what is selected, on every screen",
		theme:       colourThemeFocus,
		of:          func(p colourPalette) string { return p.focus },
		against:     onPage,
		minContrast: 3,
	}
}

// colourThemeFocus is the ring a theme draws without the override: Menu
// colour's pick, else the preset's own ring (a console look), else its accent.
func colourThemeFocus(c *Config, p ThemePreset) string {
	if hex := menuColourHexOf(menuColourValue(c)); hex != "" {
		return hex
	}
	if p.FocusRing != "" {
		return p.FocusRing
	}
	return p.Info
}

// colourThemePreset is the preset in force, overrides not applied; a config
// with no preset is drawn in the hub preset's palette (menuColourRepaint).
func colourThemePreset(c *Config) ThemePreset {
	if name := themePresetName(c); name != "" {
		return GetThemePreset(name)
	}
	return GetThemePreset("hub")
}

// colourOverrideValue is a stored override as "#rrggbb", or "" for none.
func colourOverrideValue(c *Config, key string) string {
	if c == nil || c.Variables.Custom == nil {
		return ""
	}
	s, _ := c.Variables.Custom[key].(string)
	col, ok := parseHexColor(s)
	if !ok {
		return ""
	}
	return rgbHexLower(col.R, col.G, col.B)
}

// colourHexOf is c as "#rrggbb".
func colourHexOf(c sdl.Color) string { return rgbHexLower(c.R, c.G, c.B) }

// colourHexLabel is how a custom colour reads in a row: "Custom · #1A2B3C".
func colourHexLabel(hex string) string { return "Custom · " + strings.ToUpper(hex) }

// ── Applying ─────────────────────────────────────────────────────────────

// colourPickApplied is the overrides the last ApplyThemeColors painted, and
// colourFocusInk the focus ring it set (A 0 for the theme's). Main thread.
var (
	colourPickApplied string
	colourFocusInk    sdl.Color
)

// colourOverridesSig is the stored overrides as one comparable string.
func colourOverridesSig(c *Config) string {
	var b strings.Builder
	for i := 0; i < colourSlotCount; i++ {
		b.WriteString(colourOverrideValue(c, colourSlot(i).key))
		b.WriteByte('|')
	}
	return b.String()
}

// colourPickOverride is ApplyThemeColors' second pass (after Menu colour):
// the preset with the user's own colours in it.
func colourPickOverride(p ThemePreset) ThemePreset {
	c := appConfig
	colourPickApplied = colourOverridesSig(c)
	colourFocusInk = sdl.Color{}
	// The High contrast preset picked in the theme gallery wins too, not only
	// the Accessibility switch (which applies that same preset).
	if a11y.highContrast || themePresetName(c) == "highcontrast" {
		return p
	}
	bg := colourOverrideValue(c, colourBackgroundKey)
	surface := colourOverrideValue(c, colourSurfaceKey)
	text := colourOverrideValue(c, colourTextKey)
	focus := colourOverrideValue(c, colourFocusKey)
	if bg != "" {
		p.Background = bg
	}
	page := hexRGBA(p.Background)
	// The glass ink: white over a dark page, a deep slate over a light one
	// (glassInk's rule, read off the page about to be painted).
	ink := sdl.Color{R: 255, G: 255, B: 255, A: 255}
	if wcagLuminance(page) > 0.4 {
		ink = sdl.Color{R: 15, G: 23, B: 42, A: 255}
	}
	switch {
	case surface != "":
		s := hexRGBA(surface)
		p.Surface = surface
		p.SurfaceAlt = colourHexOf(glassMix(s, ink, 0.05))
		p.SurfaceRaised = colourHexOf(glassMix(s, ink, 0.10))
	case bg != "":
		p.Surface = colourHexOf(glassMix(page, ink, 0.04))
		p.SurfaceAlt = colourHexOf(glassMix(page, ink, 0.08))
		p.SurfaceRaised = colourHexOf(glassMix(page, ink, 0.13))
	}
	if bg != "" || surface != "" {
		p.BorderSubtle = colourHexOf(glassMix(page, ink, 0.10))
		p.BorderDefault = colourHexOf(glassMix(page, ink, 0.18))
		// Icon badges are drawn on the preset's input wash; a dark one on a
		// light page (or the reverse) read as a hole, so it follows too.
		p.InputColor = p.SurfaceAlt
	}
	if text == "" && bg != "" && wcagContrast(hexRGBA(p.TextPrimary), page) < textInkMinContrast {
		text = colourHexOf(contrastText(page))
	}
	if text != "" {
		t := hexRGBA(text)
		p.TextPrimary = text
		p.TextSecondary = colourHexOf(glassMix(t, page, 0.25))
		p.TextTertiary = colourHexOf(glassMix(t, page, 0.45))
	}
	if focus != "" {
		p.FocusRing, p.FocusRing2 = focus, ""
		p.BorderFocus = focus
		colourFocusInk = hexRGBA(focus)
	}
	return p
}

// glassFocusRingColour is the ring drawGlassFocusRing paints: Focus ring
// colour's pick (kept at 3:1 like the accent) or the accent.
func glassFocusRingColour() sdl.Color {
	if colourFocusInk.A != 0 && !a11y.highContrast {
		return FocusInk(colourFocusInk)
	}
	return glassAccent()
}

// colourPickStale reports overrides stored but not on screen (menuColourSync).
func colourPickStale(c *Config) bool {
	return colourOverridesSig(c) != colourPickApplied
}

// colourOverrideSet stores one override ("" for the theme's own), saves and
// repaints.
func colourOverrideSet(c *Config, s colourOverrideSlot, value string) {
	if c == nil {
		return
	}
	hex := ""
	if col, ok := parseHexColor(value); ok {
		hex = colourHexOf(col)
	}
	setStoreString(c, s.key, hex)
	saveUserConfigDebounced(userConfigFromVariables(c))
	menuColourRepaint(c)
	if hex == "" {
		setToast(s.label + ": Theme's own")
		return
	}
	setToast(s.label + ": " + strings.ToUpper(hex))
}

// ── Palettes ─────────────────────────────────────────────────────────────

// colourPalette is a named set: page, surface, text, accent (Menu colour) and
// focus ring. Every text colour reads at 7:1 or better on its page and every
// accent and ring at 3:1 or better.
type colourPalette struct {
	id, name, note                   string
	bg, surface, text, accent, focus string
}

// colourPalettes are the Colour palette row's stops after "Theme's own".
var colourPalettes = []colourPalette{
	{"classic", "Classic", "Black and white", "#000000", "#151517", "#f5f5f5", "#ffffff", "#ffffff"},
	{"night", "Night", "Deep navy with a soft blue", "#0b1020", "#141b2e", "#e6ebf5", "#7aa2f7", "#9ab8ff"},
	{"paper", "Paper", "Warm off-white with ink-dark text", "#f3efe6", "#fbf9f4", "#1f2328", "#2563eb", "#1d4ed8"},
	{"sakura", "Sakura", "Dark plum with blossom pink", "#22141d", "#311d2a", "#fbe8f0", "#f472b6", "#f9a8d4"},
	{"forest", "Forest", "Pine green with a fresh leaf", "#0e1a13", "#16261c", "#e2efe6", "#4ade80", "#86efac"},
	{"ocean", "Ocean", "Deep teal water with bright cyan", "#06171f", "#0c2430", "#dff3f9", "#22d3ee", "#67e8f9"},
	{"amber", "Amber", "Warm dark brown with a golden glow", "#16110a", "#221a0f", "#fbefd8", "#f59e0b", "#fcd34d"},
	{"mono", "Mono", "Greys only, no colour at all", "#1a1a1a", "#242424", "#e5e5e5", "#a3a3a3", "#d4d4d4"},
}

// colourPaletteTheme is the stop that clears every colour back to the theme's.
const colourPaletteTheme = "theme"

// colourPaletteCurrent is the palette the stored colours are, colourPaletteTheme
// when none is set, or "" for a mix of the user's own.
func colourPaletteCurrent(c *Config) string {
	bg := colourOverrideValue(c, colourBackgroundKey)
	surface := colourOverrideValue(c, colourSurfaceKey)
	text := colourOverrideValue(c, colourTextKey)
	focus := colourOverrideValue(c, colourFocusKey)
	// By colour, so White picked on Menu colour still matches Classic's #ffffff.
	accent := menuColourHexOf(menuColourValue(c))
	if bg == "" && surface == "" && text == "" && focus == "" && accent == "" {
		return colourPaletteTheme
	}
	for _, p := range colourPalettes {
		if bg == p.bg && surface == p.surface && text == p.text && focus == p.focus && accent == p.accent {
			return p.id
		}
	}
	return ""
}

// colourPaletteOptions are the row's stops: Theme's own, then the palettes,
// each with its accent as the swatch.
func colourPaletteOptions(c *Config) []setOption {
	themeAccent := colourThemePreset(c).Info
	out := make([]setOption, 0, len(colourPalettes)+1)
	out = append(out, setOption{Value: colourPaletteTheme, Label: "Theme's own",
		Note: "The theme's colours, no overrides", Swatch: themeAccent})
	for _, p := range colourPalettes {
		out = append(out, setOption{Value: p.id, Label: p.name, Note: p.note, Swatch: p.accent})
	}
	return out
}

// colourPaletteApply stores a palette's five colours (or clears them) and
// repaints once.
func colourPaletteApply(c *Config, id string) {
	if c == nil {
		return
	}
	var pal colourPalette
	found := false
	for _, p := range colourPalettes {
		if p.id == id {
			pal, found = p, true
			break
		}
	}
	if !found && id != colourPaletteTheme {
		return
	}
	accent := menuColourTheme
	if found {
		accent = pal.accent
	}
	setStoreString(c, colourBackgroundKey, pal.bg)
	setStoreString(c, colourSurfaceKey, pal.surface)
	setStoreString(c, colourTextKey, pal.text)
	setStoreString(c, colourFocusKey, pal.focus)
	setStoreString(c, menuColourKey, accent)
	saveUserConfigDebounced(userConfigFromVariables(c))
	menuColourRepaint(c)
	if found {
		setToast("Colour palette: " + pal.name)
	} else {
		setToast("Colour palette: Theme's own")
	}
}

// colourPaletteRow is Theme & look → Colour palette.
func colourPaletteRow() setRow {
	return setRow{
		ID: "colourPalette", Label: "Colour palette", Kind: setKindChoice,
		Detail: func(c *Config) string {
			if a11y.highContrast {
				return "High contrast keeps its own colours while it is on"
			}
			return "Background, surface, text, menu and ring colours in one go"
		},
		Options:    colourPaletteOptions,
		Sheet:      colourPaletteOptions,
		SheetValue: colourPaletteCurrent,
		SheetTitle: "Colour palette",
		Current: func(c *Config) int {
			v := colourPaletteCurrent(c)
			for i, o := range colourPaletteOptions(c) {
				if o.Value == v {
					return i
				}
			}
			return -1
		},
		Value:  setStatic("Your own mix"),
		Choose: colourPaletteApply,
	}
}

// ── Override rows ────────────────────────────────────────────────────────

// colourOverrideOptions are one slot's stops: Theme's own, the palettes'
// colours for the slot, and a custom colour when one is stored.
func colourOverrideOptions(c *Config, s colourOverrideSlot) []setOption {
	out := make([]setOption, 0, len(colourPalettes)+2)
	out = append(out, setOption{Value: "", Label: "Theme's own",
		Note: "The colour the theme comes with", Swatch: s.theme(c, colourThemePreset(c))})
	seen := map[string]bool{}
	for _, p := range colourPalettes {
		hex := s.of(p)
		if seen[hex] {
			continue
		}
		seen[hex] = true
		out = append(out, setOption{Value: hex, Label: p.name, Note: p.name + " palette", Swatch: hex})
	}
	if v := colourOverrideValue(c, s.key); v != "" && !seen[v] {
		out = append(out, setOption{Value: v, Label: colourHexLabel(v), Swatch: v})
	}
	return out
}

// colourOverridePickSlot is the picker for one override slot.
func colourOverridePickSlot(s colourOverrideSlot) colourPickSlot {
	return colourPickSlot{
		title: s.label, note: s.detail,
		current: func(c *Config) sdl.Color {
			if v := colourOverrideValue(c, s.key); v != "" {
				return hexRGBA(v)
			}
			return hexRGBA(s.theme(c, colourThemePreset(c)))
		},
		value:       func(c *Config) string { return colourOverrideValue(c, s.key) },
		presets:     func(c *Config) []setOption { return colourOverrideOptions(c, s) },
		apply:       func(c *Config, value string) { colourOverrideSet(c, s, value) },
		against:     s.against,
		minContrast: s.minContrast,
	}
}

// colourOverrideRow is one of Background / Surface / Text / Focus ring colour:
// Left/Right step the palettes' colours, A opens the picker.
func colourOverrideRow(i int) setRow {
	s := colourSlot(i)
	return setRow{
		ID: s.key, Label: s.label, Kind: setKindChoice,
		Detail: func(*Config) string {
			if a11y.highContrast {
				return "High contrast keeps its own colours while it is on"
			}
			return s.detail
		},
		Options: func(c *Config) []setOption { return colourOverrideOptions(c, s) },
		Current: func(c *Config) int {
			v := colourOverrideValue(c, s.key)
			for j, o := range colourOverrideOptions(c, s) {
				if o.Value == v {
					return j
				}
			}
			return -1
		},
		Value:  func(c *Config) string { return colourHexLabel(colourOverrideValue(c, s.key)) },
		Choose: func(c *Config, value string) { colourOverrideSet(c, s, value) },
		Pick:   func(c *Config) { colourPickOpen(c, colourOverridePickSlot(s)) },
	}
}

// ── The older colour rows on the picker ──────────────────────────────────

// setColourPickSlot is the picker for a setColourRow (button, label and input
// colour): its presets are the row's own stops.
func setColourPickSlot(name, label, detail string, options func(*Config) []setOption, choose func(*Config, string)) colourPickSlot {
	return colourPickSlot{
		title: label, note: detail,
		current: func(c *Config) sdl.Color {
			rgb := effectiveThemeColor(c, name)
			return sdl.Color{R: uint8(rgb.R), G: uint8(rgb.G), B: uint8(rgb.B), A: 255}
		},
		value: func(c *Config) string {
			hex := rgbHex(effectiveThemeColor(c, name))
			if strings.EqualFold(hex, rgbHex(themePresetColor(c, name))) {
				return ""
			}
			return strings.ToUpper(hex)
		},
		presets: options,
		apply:   choose,
	}
}

// menuColourPickSlot is the picker for Menu colour, the accent.
func menuColourPickSlot() colourPickSlot {
	return colourPickSlot{
		title: "Menu colour", note: "Highlights, pills and bars; the theme stays",
		current: func(c *Config) sdl.Color {
			if hex := menuColourHexOf(menuColourValue(c)); hex != "" {
				return hexRGBA(hex)
			}
			return hexRGBA(colourThemePreset(c).Info)
		},
		value: menuColourValue,
		presets: func(c *Config) []setOption {
			out := menuColourOptions(c)
			seen := map[string]bool{}
			for _, o := range out {
				seen[strings.ToLower(o.Swatch)] = true
			}
			for _, p := range colourPalettes {
				if !seen[p.accent] {
					seen[p.accent] = true
					out = append(out, setOption{Value: p.accent, Label: p.name, Note: p.name + " palette", Swatch: p.accent})
				}
			}
			return out
		},
		apply:       menuColourChoose,
		against:     func() (sdl.Color, string) { return ColorBackground, "the background" },
		minContrast: 3,
	}
}

// ── Reset ────────────────────────────────────────────────────────────────

// colourAnyChanged reports a colour or the opacity away from the theme's.
func colourAnyChanged(c *Config) bool {
	if colourOverridesSig(c) != colourOverridesSig(nil) || menuColourValue(c) != menuColourTheme {
		return true
	}
	if glassOpacityStored(c) != 100 {
		return true
	}
	for _, name := range themeColorVariables {
		if !strings.EqualFold(rgbHex(effectiveThemeColor(c, name)), rgbHex(themePresetColor(c, name))) {
			return true
		}
	}
	return false
}

// colourResetAll puts every colour row and Glass opacity back to the theme's.
func colourResetAll(c *Config) {
	if c == nil {
		return
	}
	for i := 0; i < colourSlotCount; i++ {
		setStoreString(c, colourSlot(i).key, "")
	}
	setStoreString(c, menuColourKey, menuColourTheme)
	setStoreString(c, glassOpacityKey, "100")
	glassOpacityLoad(c)
	for _, name := range themeColorVariables {
		applyColorVar(c, name, rgbHex(themePresetColor(c, name)))
	}
	saveUserConfigDebounced(userConfigFromVariables(c))
	menuColourRepaint(c)
	setToast("Colours: the theme's own")
}

// colourResetRow is Theme & look → Reset colours.
func colourResetRow() setRow {
	return setRow{
		ID: "colourReset", Label: "Reset colours", Kind: setKindAction,
		Detail: func(c *Config) string {
			if !colourAnyChanged(c) {
				return "Every colour is the theme's own"
			}
			return "Every colour row and Glass opacity back to the theme's"
		},
		Disabled: func(c *Config) bool { return !colourAnyChanged(c) },
		Open: func(c *Config) {
			setConfirm(c, "Reset colours?",
				"Background, surface, text, focus ring, menu, button, label and input colours go back to the theme's own, and Glass opacity to 100%.",
				"palette", "Reset", colourResetAll)
		},
	}
}

// ── Glass opacity ────────────────────────────────────────────────────────

// glassOpacity is Glass opacity as a factor (0.1..1), cached for the per-frame
// fills; glassOpacityLoad refreshes it.
var glassOpacity = 1.0

// glassOpacityStored is the stored percent, 10..100 in steps of 10 (100 when
// unset or unreadable).
func glassOpacityStored(c *Config) int {
	if c == nil || c.Variables.Custom == nil {
		return 100
	}
	v, ok := customValue(c.Variables.Custom, glassOpacityKey)
	if !ok {
		return 100
	}
	n, ok := coerceInt(v)
	if !ok || n >= 100 {
		return 100
	}
	if n < 10 {
		n = 10
	}
	return (n + 5) / 10 * 10
}

// glassOpacityLoad caches the stored opacity.
func glassOpacityLoad(c *Config) {
	glassOpacity = float64(glassOpacityStored(c)) / 100
}

// glassOpacityA scales a glass fill's alpha by Glass opacity. Reduce
// transparency keeps the token as it is: it makes glass solid, whatever the
// opacity says.
func glassOpacityA(alpha uint8) uint8 {
	if glassOpacity >= 1 || a11y.reduceTransparency {
		return alpha
	}
	a := float64(alpha)*glassOpacity + 0.5
	if a < 1 && alpha > 0 {
		a = 1
	}
	return uint8(a)
}

// glassOpacityRow is Theme & look → Glass opacity.
func glassOpacityRow() setRow {
	return setRow{
		ID: glassOpacityKey, Label: "Glass opacity", Kind: setKindChoice, Clamp: true,
		Detail: func(*Config) string {
			if a11y.reduceTransparency {
				return "Solid while Reduce transparency is on"
			}
			return "How solid glass panes and the selection pill are; lower lets the wallpaper show through"
		},
		Disabled: func(*Config) bool { return a11y.reduceTransparency },
		Options:  func(*Config) []setOption { return setLevelOptions(10) },
		Current: func(c *Config) int {
			return setNearestStop(setLevelOptions(10), glassOpacityStored(c))
		},
		Choose: func(c *Config, value string) {
			pct, err := strconv.Atoi(value)
			if err != nil {
				return
			}
			setStoreString(c, glassOpacityKey, strconv.Itoa(pct))
			glassOpacityLoad(c)
			saveUserConfigDebounced(userConfigFromVariables(c))
		},
	}
}

// colourPickSyncPrefs is menuColourSync's hook for the cached values: startup,
// a config reload and a profile switch.
func colourPickSyncPrefs(c *Config) {
	glassOpacityLoad(c)
}
