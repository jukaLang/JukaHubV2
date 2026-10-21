package main

// a11y.go — Accessibility.
//
// Three things live here, all of them system-wide rather than per screen:
//
//   * Text scaling. getCachedFont is the single choke point every label in the
//     app goes through, so one multiplier there resizes scenes, overlays, the
//     on-screen keyboard and toast text together.
//   * High-contrast and colour-blind-safe palettes. They are ordinary theme
//     presets, which means the theme gallery, the config round-trip and the
//     renderer already understand them.
//   * Controller remapping. The physical button is rewritten once, on the way
//     into the event loop, so overlays and scenes cannot disagree about which
//     button means "select".
//
// Everything is stored in the config's Custom map with the same keys the panel
// writes, so a config edited by hand on the SD card behaves exactly like one
// changed from the UI.

import (
	"math"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	a11yKeyFontScale  = "a11y_font_scale"
	a11yKeyContrast   = "a11y_high_contrast"
	a11yKeyColorblind = "a11y_colorblind"
	a11yKeySwapAB     = "a11y_swap_ab"
	a11yKeySwapXY     = "a11y_swap_xy"
	a11yKeyPrevTheme  = "a11y_prev_theme"

	a11yPanelTitle = "Accessibility"

	a11yMinScale = 0.85
	a11yMaxScale = 1.45
)

// a11yFontScaleSteps are the sizes the left/right buttons walk through. Steps
// are discrete on purpose: a handheld's d-pad is a coarse input, and a
// continuous slider makes every press re-rasterise every cached label.
var a11yFontScaleSteps = []float64{0.85, 1.0, 1.15, 1.3, 1.45}

// a11yColorblindModes maps the stored key to the palette it applies. The empty
// key means "don't tint anything", which restores the user's own theme.
var a11yColorblindModes = []struct {
	key    string
	label  string
	preset string
}{
	{key: "", label: "Off", preset: ""},
	{key: "deuteranopia", label: "Deuteranopia", preset: "cb-deuteranopia"},
	{key: "protanopia", label: "Protanopia", preset: "cb-protanopia"},
	{key: "tritanopia", label: "Tritanopia", preset: "cb-tritanopia"},
}

type a11ySettings struct {
	open         bool
	cursor       int
	scroll       int
	fontScale    float64
	highContrast bool
	colorblind   string
	swapAB       bool
	swapXY       bool
}

var a11y a11ySettings

var a11yPanelRect sdl.Rect
var a11yRowHits []struct {
	index int
	rect  sdl.Rect
}

// ── Persistence ───────────────────────────────────────────────────────────

// a11yInit loads the saved settings and applies them before the first frame, so
// a user who needs large text never sees a launch at the default size.
func a11yInit(config *Config) {
	a11y = a11ySettings{fontScale: 1.0}
	a11yLoad(config)
	a11yRefreshFonts()
}

// a11yLoad reads the stored values. Numbers may arrive as float64 (JSON),
// strings (XML, or a hand-edited config) or be missing entirely; all three are
// treated as "no preference" rather than an error.
func a11yLoad(config *Config) {
	if config == nil {
		return
	}
	if s, ok := customString(config.Variables.Custom, a11yKeyFontScale); ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && f > 0 {
			a11y.fontScale = a11yClampScale(f)
		}
	}
	a11y.highContrast = a11yCustomBool(config, a11yKeyContrast)
	a11y.swapAB = a11yCustomBool(config, a11yKeySwapAB)
	a11y.swapXY = a11yCustomBool(config, a11yKeySwapXY)
	if s, ok := customString(config.Variables.Custom, a11yKeyColorblind); ok {
		if a11yColorblindPreset(s) != "" {
			a11y.colorblind = s
		}
	}
}

func a11yCustomBool(config *Config, key string) bool {
	if config == nil {
		return false
	}
	s, ok := customString(config.Variables.Custom, key)
	if !ok {
		return false
	}
	b, err := strconv.ParseBool(strings.TrimSpace(strings.ToLower(s)))
	return err == nil && b
}

// a11ySave writes the settings back through the same user-config writer the
// Settings scenes use, so nothing else in the snapshot is lost.
func a11ySave(config *Config) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[a11yKeyFontScale] = a11y.fontScale
	config.Variables.Custom[a11yKeyContrast] = a11y.highContrast
	config.Variables.Custom[a11yKeyColorblind] = a11y.colorblind
	config.Variables.Custom[a11yKeySwapAB] = a11y.swapAB
	config.Variables.Custom[a11yKeySwapXY] = a11y.swapXY
	saveUserConfig(userConfigFromVariables(config))
}

// ── Text scaling ──────────────────────────────────────────────────────────

func a11yClampScale(scale float64) float64 {
	if math.IsNaN(scale) || scale <= 0 {
		return 1.0
	}
	if scale < a11yMinScale {
		scale = a11yMinScale
	}
	if scale > a11yMaxScale {
		scale = a11yMaxScale
	}
	return math.Round(scale*100) / 100
}

// a11yScaleFontSize is called by getCachedFont for every logical font, which is
// what makes the setting global. The size is part of the font cache key, so a
// new scale simply opens new faces instead of reusing stale ones.
func a11yScaleFontSize(base int) int {
	if base <= 0 {
		return base
	}
	scale := a11y.fontScale
	if scale <= 0 || math.Abs(scale-1) < 0.001 {
		return base
	}
	size := int(math.Round(float64(base) * scale))
	if size < 8 {
		size = 8
	}
	return size
}

// a11yRefreshFonts drops every cached font and label texture so the new size is
// visible on the next frame. The faces are released by dropping the map rather
// than closing them: a texture cache elsewhere could still hold a pointer, and
// a closed face is a crash, while an unreferenced one is collected at exit.
func a11yRefreshFonts() {
	fontCache = make(map[string]*ttf.Font)
	ClearTextCache()
	clearHomeTextCache()
}

func a11yScaleIndex() int {
	idx := 0
	best := math.Abs(a11yFontScaleSteps[0] - a11y.fontScale)
	for i, step := range a11yFontScaleSteps {
		if d := math.Abs(step - a11y.fontScale); d < best {
			best = d
			idx = i
		}
	}
	return idx
}

func a11yScaleLabel() string {
	return strconv.Itoa(int(math.Round(a11y.fontScale*100))) + "%"
}

// ── Palettes ──────────────────────────────────────────────────────────────

func a11yColorblindPreset(key string) string {
	for _, mode := range a11yColorblindModes {
		if mode.key == key {
			return mode.preset
		}
	}
	return ""
}

func a11yColorblindLabel(key string) string {
	for _, mode := range a11yColorblindModes {
		if mode.key == key {
			return mode.label
		}
	}
	return "Off"
}

func a11yColorblindIndex(key string) int {
	for i, mode := range a11yColorblindModes {
		if mode.key == key {
			return i
		}
	}
	return 0
}

// a11yIsA11yPreset reports whether a preset name is one this panel applies, so
// the "previous theme" slot never records our own palette as the user's choice.
func a11yIsA11yPreset(name string) bool {
	if name == "" || name == "highcontrast" {
		return true
	}
	for _, mode := range a11yColorblindModes {
		if mode.preset != "" && mode.preset == name {
			return true
		}
	}
	return false
}

// a11yRememberTheme records the palette to return to when the accessibility
// palettes are switched off again.
func a11yRememberTheme(config *Config) {
	if config == nil {
		return
	}
	current, _ := customString(config.Variables.Custom, "theme_preset")
	if a11yIsA11yPreset(current) {
		return
	}
	config.Variables.Custom[a11yKeyPrevTheme] = current
}

// a11yPaletteName decides which theme the accessibility settings want right
// now. An empty result means "leave the current theme alone".
//
// A colour-blind palette wins over high contrast because it carries more
// information: the contrast boost is still reachable by choosing it once the
// colour-blind mode is off.
func a11yPaletteName(config *Config) string {
	if config == nil {
		return ""
	}
	if preset := a11yColorblindPreset(a11y.colorblind); preset != "" {
		return preset
	}
	if a11y.highContrast {
		return "highcontrast"
	}
	// Nothing of ours is on. Leave a theme the user picked after us alone, and
	// do nothing at all when we never replaced one: falling back to "dark" here
	// would silently wipe a palette chosen in the theme gallery.
	current, _ := customString(config.Variables.Custom, "theme_preset")
	if !a11yIsA11yPreset(current) {
		return ""
	}
	prev, ok := customString(config.Variables.Custom, a11yKeyPrevTheme)
	if !ok || prev == "" {
		return ""
	}
	return prev
}

// a11yApplyPalette puts the selected palette on screen.
func a11yApplyPalette(config *Config) {
	name := a11yPaletteName(config)
	if name == "" {
		return
	}
	if a11yColorblindPreset(a11y.colorblind) != "" || a11y.highContrast {
		a11yRememberTheme(config)
	}
	applyThemePreset(config, name, false)
}

// ── Controller remapping ──────────────────────────────────────────────────

// a11yRemapButton translates a physical button into the logical one the rest of
// the app expects. Only the four face buttons are remapped: the d-pad drives
// focus everywhere and swapping it would make navigation unlearnable.
func a11yRemapButton(b uint8) uint8 {
	if a11y.swapAB {
		switch b {
		case sdl.CONTROLLER_BUTTON_A:
			return sdl.CONTROLLER_BUTTON_B
		case sdl.CONTROLLER_BUTTON_B:
			return sdl.CONTROLLER_BUTTON_A
		}
	}
	if a11y.swapXY {
		switch b {
		case sdl.CONTROLLER_BUTTON_X:
			return sdl.CONTROLLER_BUTTON_Y
		case sdl.CONTROLLER_BUTTON_Y:
			return sdl.CONTROLLER_BUTTON_X
		}
	}
	return b
}

// a11yRemapEvent rewrites the event in place before any handler sees it. It is
// a no-op when the user has not swapped anything.
func a11yRemapEvent(e *sdl.ControllerButtonEvent) {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return
	}
	if !a11y.swapAB && !a11y.swapXY {
		return
	}
	e.Button = a11yRemapButton(e.Button)
}

// ── Panel ─────────────────────────────────────────────────────────────────

type a11yRowKind int

const (
	a11yRowTextSize a11yRowKind = iota
	a11yRowContrast
	a11yRowColorblind
	a11yRowSwapAB
	a11yRowSwapXY
	a11yRowReset
)

type a11yRow struct {
	label  string
	detail string
	value  string
	kind   a11yRowKind
}

func a11yRows() []a11yRow {
	contrast := "Off"
	if a11y.highContrast {
		contrast = "On"
	}
	swapAB := "Normal"
	if a11y.swapAB {
		swapAB = "Swapped"
	}
	swapXY := "Normal"
	if a11y.swapXY {
		swapXY = "Swapped"
	}
	return []a11yRow{
		{label: "Text size", detail: "Scales every menu, overlay and keyboard label.", value: a11yScaleLabel(), kind: a11yRowTextSize},
		{label: "High contrast", detail: "Stronger borders and brighter text on every screen.", value: contrast, kind: a11yRowContrast},
		{label: "Colour-blind palette", detail: "Replaces the theme colours with a safer set.", value: a11yColorblindLabel(a11y.colorblind), kind: a11yRowColorblind},
		{label: "Swap A / B", detail: "For players used to the Japanese button order.", value: swapAB, kind: a11yRowSwapAB},
		{label: "Swap X / Y", detail: "Moves the secondary and tertiary actions.", value: swapXY, kind: a11yRowSwapXY},
		{label: "Reset to defaults", detail: "Back to 100% text, default colours, standard buttons.", value: "", kind: a11yRowReset},
	}
}

// a11ySyncFromTheme makes the panel agree with the palette that is actually on
// screen. Choosing a theme in the gallery replaces our palette without touching
// these flags, and a panel that still said "Deuteranopia" would be lying.
func a11ySyncFromTheme(config *Config) {
	if config == nil {
		return
	}
	current, _ := customString(config.Variables.Custom, "theme_preset")
	if current == "highcontrast" {
		a11y.highContrast = true
		a11y.colorblind = ""
		return
	}
	a11y.highContrast = false
	a11y.colorblind = ""
	for _, mode := range a11yColorblindModes {
		if mode.preset != "" && mode.preset == current {
			a11y.colorblind = mode.key
			return
		}
	}
}

func openAccessibilityPanel(config *Config) {
	qs.open = false
	toolsPanel.open = false
	smartHomePanel.open = false
	emuPanel.open = false
	gpPanel.open = false
	winAppsPanel = windowsAppsPanelState{}
	portsPanel = portsPanelState{}
	chiakiPanel = chiakiPanelState{}
	raPanel.open = false
	a11ySyncFromTheme(config)
	a11y.open = true
	a11y.cursor = 0
	a11y.scroll = 0
	a11yPanelRect = sdl.Rect{}
	a11yRowHits = nil
	PlayActivateSound()
}

func closeAccessibilityPanel() {
	if !a11y.open {
		return
	}
	a11y.open = false
	a11yPanelRect = sdl.Rect{}
	a11yRowHits = nil
	PlayBackSound()
}

func a11yPanelMove(delta int) {
	rows := a11yRows()
	if len(rows) == 0 {
		return
	}
	a11y.cursor += delta
	if a11y.cursor < 0 {
		a11y.cursor = len(rows) - 1
	}
	if a11y.cursor >= len(rows) {
		a11y.cursor = 0
	}
	PlayNavSound()
}

// a11yAdjust moves the focused row's value. dir < 0 is left / previous,
// dir > 0 is right / next.
func a11yAdjust(dir int, config *Config) {
	rows := a11yRows()
	if a11y.cursor < 0 || a11y.cursor >= len(rows) {
		return
	}
	changed := false
	switch rows[a11y.cursor].kind {
	case a11yRowTextSize:
		idx := a11yScaleIndex() + dir
		if dir > 0 && idx >= len(a11yFontScaleSteps) {
			idx = len(a11yFontScaleSteps) - 1
		}
		if dir < 0 && idx < 0 {
			idx = 0
		}
		if a11yFontScaleSteps[idx] != a11y.fontScale {
			a11y.fontScale = a11yFontScaleSteps[idx]
			a11yRefreshFonts()
			changed = true
			ShowToast("Text size: "+a11yScaleLabel(), ToastKindInfo)
		}
	case a11yRowContrast:
		a11y.highContrast = !a11y.highContrast
		a11yApplyPalette(config)
		changed = true
	case a11yRowColorblind:
		idx := a11yColorblindIndex(a11y.colorblind) + dir
		if idx < 0 {
			idx = len(a11yColorblindModes) - 1
		}
		if idx >= len(a11yColorblindModes) {
			idx = 0
		}
		a11y.colorblind = a11yColorblindModes[idx].key
		a11yApplyPalette(config)
		changed = true
	case a11yRowSwapAB:
		a11y.swapAB = !a11y.swapAB
		changed = true
	case a11yRowSwapXY:
		a11y.swapXY = !a11y.swapXY
		changed = true
	case a11yRowReset:
		a11y.fontScale = 1.0
		a11y.highContrast = false
		a11y.colorblind = ""
		a11y.swapAB = false
		a11y.swapXY = false
		a11yRefreshFonts()
		a11yApplyPalette(config)
		changed = true
		ShowToast("Accessibility settings reset", ToastKindInfo)
	}
	if changed {
		PlayNavSound()
		a11ySave(config)
	}
}

func ctrlAccessibilityPanel(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		a11yPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		a11yPanelMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		a11yAdjust(-1, config)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_A:
		a11yAdjust(1, config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeAccessibilityPanel()
	}
	return true
}

func handleAccessibilityKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !a11y.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		a11yPanelMove(-1)
	case sdl.K_DOWN:
		a11yPanelMove(1)
	case sdl.K_LEFT:
		a11yAdjust(-1, config)
	case sdl.K_RIGHT, sdl.K_RETURN, sdl.K_SPACE:
		a11yAdjust(1, config)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		closeAccessibilityPanel()
	}
	return true
}

func handleAccessibilityMouse(mx, my int32, config *Config) bool {
	if !a11y.open {
		return false
	}
	for _, hit := range a11yRowHits {
		if pointInRect(mx, my, hit.rect) {
			a11y.cursor = hit.index
			a11yAdjust(1, config)
			return true
		}
	}
	if a11yPanelRect.W > 0 && pointInRect(mx, my, a11yPanelRect) {
		return true
	}
	closeAccessibilityPanel()
	return true
}

// renderAccessibilityPanel draws the card. It follows the Tools / Ports / Remote
// Play layout so it does not look bolted on.
func renderAccessibilityPanel(renderer *sdl.Renderer, config *Config) {
	if !a11y.open || renderer == nil {
		return
	}
	rows := a11yRows()
	if a11y.cursor >= len(rows) {
		a11y.cursor = len(rows) - 1
	}
	if a11y.cursor < 0 {
		a11y.cursor = 0
	}

	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	const padX = int32(24)
	// Rows are one line each (label left, value right) and the focused row's
	// explanation is drawn once underneath the list. Two-line rows wasted a
	// third of a 720p screen and left the last settings unreachable at 130%
	// text; this way every row fits at every scale.
	rowH := 10 + medH + 10
	headerH := 18 + medH + 4 + smallH + 14
	detailH := 10 + smallH + 10
	footerH := 20 + smallH

	panelW := int32(820)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	maxRows := (screenHeight - 40 - headerH - detailH - footerH) / rowH
	if maxRows < 3 {
		maxRows = 3
	}
	visible := int32(len(rows))
	if visible > maxRows {
		visible = maxRows
	}
	panelH := headerH + visible*rowH + detailH + footerH
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	a11yPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	if a11y.cursor < a11y.scroll {
		a11y.scroll = a11y.cursor
	}
	if a11y.cursor >= a11y.scroll+int(visible) {
		a11y.scroll = a11y.cursor - int(visible) + 1
	}
	if a11y.scroll > len(rows)-int(visible) {
		a11y.scroll = len(rows) - int(visible)
	}
	if a11y.scroll < 0 {
		a11y.scroll = 0
	}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	renderText(renderer, config, medium, a11yPanelTitle, ColorTextPrimary(), panelX+padX, panelY+18)
	renderText(renderer, config, font, "Applies to every screen · saved automatically", ColorTextTertiary(), panelX+padX, panelY+18+medH+4)

	focusColor := installHighlight()
	rowW := panelW - 2*padX
	a11yRowHits = a11yRowHits[:0]
	for i := int32(0); i < visible; i++ {
		idx := a11y.scroll + int(i)
		row := rows[idx]
		y := panelY + headerH + i*rowH
		r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 4}
		a11yRowHits = append(a11yRowHits, struct {
			index int
			rect  sdl.Rect
		}{index: idx, rect: r})

		focused := idx == a11y.cursor
		rowBg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			rowBg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}

		renderText(renderer, config, medium, row.label, ColorTextPrimary(), r.X+16, r.Y+10)
		if row.value != "" {
			color := ColorTextSecondary()
			if focused {
				color = focusColor
			}
			value := row.value
			if focused {
				value = "< " + value + " >"
			}
			vw, _, _ := font.SizeUTF8(value)
			renderText(renderer, config, font, value, color, r.X+r.W-16-int32(vw), r.Y+10+(medH-smallH)/2)
		}
	}

	// One explanation for the focused row, under the list.
	detail := rows[a11y.cursor].detail
	if detail == "" {
		detail = "Change the value with Left/Right, or press A."
	}
	detailY := panelY + headerH + visible*rowH + 10
	renderText(renderer, config, font, ellipsize(font, detail, rowW), ColorTextTertiary(), panelX+padX, detailY)

	hint := "Left/Right: change   A: next   B: close"
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+padX, panelY+panelH-footerH+8)
}
