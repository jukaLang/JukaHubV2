package main

// Pixel checks for the colour swatch that Settings → Appearance now renders
// next to each hex field. They use the shared offscreen probe from
// installbar_render_test.go (dummy video driver + software renderer).

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// renderColorField draws one settings input over a flat background and returns
// the captured pixels.
func renderColorField(t *testing.T, cfg *Config, elem Element, sceneIdx, elemIdx int) *probeFrame {
	t.Helper()
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderInputField(renderer, cfg, elem, sceneIdx, elemIdx)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	return f
}

func colorFieldTestConfig() *Config {
	cfg := &Config{}
	cfg.Variables.Custom = map[string]interface{}{}
	cfg.Variables.ButtonColor = RGB{R: 52, G: 152, B: 219}
	cfg.Variables.LabelColor = RGB{R: 240, G: 240, B: 240}
	cfg.Variables.InputColor = RGB{R: 20, G: 24, B: 34}
	return cfg
}

func colorFieldTestElement() Element {
	return Element{
		Type:     "input",
		Variable: "buttonColor",
		X:        40,
		Y:        40,
		Width:    "420",
		Height:   "45",
		BgColor:  "$inputColor",
		Color:    "$labelColor",
	}
}

// pauseRenderGlobals makes sure the field renders in its resting state and
// restores whatever the rest of the suite was using.
func pauseRenderGlobals(t *testing.T) {
	t.Helper()
	prevActiveScene, prevActiveElem := activeSceneIndex, activeElementIndex
	prevBuffer := inputTextBuffer
	// Nothing is focused: activeSceneIndex/-1 is how the app represents that,
	// while the element under test still belongs to scene 0.
	activeSceneIndex, activeElementIndex = -1, -1
	inputTextBuffer = ""
	t.Cleanup(func() {
		activeSceneIndex, activeElementIndex = prevActiveScene, prevActiveElem
		inputTextBuffer = prevBuffer
	})
}

// The swatch must actually show the stored colour, not just the hex string.
func TestColorFieldRendersStoredSwatch(t *testing.T) {
	pauseRenderGlobals(t)
	cfg := colorFieldTestConfig()
	elem := colorFieldTestElement()

	f := renderColorField(t, cfg, elem, 0, 0)

	want := sdl.Color{R: 52, G: 152, B: 219, A: 255}
	// Swatch: 28px chip at (50, 48) inside a 45px field at (40, 40).
	chip := f.countColor(want, 50, 48, 78, 76)
	if chip < 150 {
		t.Fatalf("swatch does not show the stored colour: %d matching pixels", chip)
	}
	// The chip must sit inside the field, not bleed over its border.
	if f.countColor(want, 0, 0, 39, 719) > 0 || f.countColor(want, 0, 0, 1279, 39) > 0 {
		t.Fatal("swatch colour drawn outside the input field")
	}
}

// While typing, the chip previews the buffer instead of the saved value.
func TestColorFieldSwatchFollowsTypedValue(t *testing.T) {
	pauseRenderGlobals(t)
	cfg := colorFieldTestConfig()
	elem := colorFieldTestElement()

	activeSceneIndex, activeElementIndex = 0, 0
	inputTextBuffer = "#FF00FF"

	f := renderColorField(t, cfg, elem, 0, 0)
	magenta := sdl.Color{R: 255, G: 0, B: 255, A: 255}
	if got := f.countColor(magenta, 50, 48, 78, 76); got < 150 {
		t.Fatalf("typed colour is not previewed: %d matching pixels", got)
	}
	stored := sdl.Color{R: 52, G: 152, B: 219, A: 255}
	if got := f.countColor(stored, 50, 48, 78, 76); got > 0 {
		t.Fatalf("stale stored colour still drawn while editing: %d pixels", got)
	}
}

// A half-typed value must render an empty chip rather than a misleading colour.
func TestColorFieldSwatchEmptyForPartialHex(t *testing.T) {
	pauseRenderGlobals(t)
	cfg := colorFieldTestConfig()
	elem := colorFieldTestElement()

	activeSceneIndex, activeElementIndex = 0, 0
	inputTextBuffer = "#FF00"

	f := renderColorField(t, cfg, elem, 0, 0)
	for _, c := range []sdl.Color{
		{R: 255, G: 0, B: 255, A: 255},
		{R: 52, G: 152, B: 219, A: 255},
	} {
		if got := f.countColor(c, 50, 48, 78, 76); got > 0 {
			t.Fatalf("partial hex still draws colour %v (%d pixels)", c, got)
		}
	}
}

// The Appearance screen shows the saved palette on real controls, so the hex
// form is not the only thing on screen.
func TestAppearancePreviewShowsPalette(t *testing.T) {
	pauseRenderGlobals(t)
	cfg := colorFieldTestConfig()
	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = probeW, probeH
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })

	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderAppearancePreview(renderer, cfg)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}

	// Sample controls: the button fill and the input fill drawn on the card.
	btn := sdl.Color{R: 52, G: 152, B: 219, A: 255}
	if got := f.countColor(btn, 640, 184, 850, 238); got < 500 {
		t.Fatalf("preview button does not use the button colour: %d pixels", got)
	}
	inp := sdl.Color{R: 20, G: 24, B: 34, A: 255}
	if got := f.countColor(inp, 640, 290, 1230, 340); got < 500 {
		t.Fatalf("preview input does not use the input colour: %d pixels", got)
	}
}

// A value that previews must also save: both "#rrggbb" and "rrggbb" are
// accepted, and full-range channels survive the parse.
func TestApplyColorVarAcceptsHexForms(t *testing.T) {
	cfg := colorFieldTestConfig()
	applyColorVar(cfg, "buttonColor", "#FF8000")
	if got := cfg.Variables.ButtonColor; got != (RGB{R: 255, G: 128, B: 0}) {
		t.Fatalf("#hex not applied: %+v", got)
	}
	applyColorVar(cfg, "labelColor", "0a0b0c")
	if got := cfg.Variables.LabelColor; got != (RGB{R: 10, G: 11, B: 12}) {
		t.Fatalf("bare hex not applied: %+v", got)
	}
}

// resolveColor reads "r,g,b" from the struct path too; channels above 127 used
// to fail a signed 8-bit parse and fall back to the default colour.
func TestResolveColorAcceptsFullRangeRGB(t *testing.T) {
	cfg := colorFieldTestConfig()
	want := sdl.Color{R: 240, G: 240, B: 240, A: 255}
	got := resolveColor(cfg, "$labelColor", sdl.Color{R: 1, G: 2, B: 3, A: 255})
	if got != want {
		t.Fatalf("resolveColor($labelColor) = %v, want %v", got, want)
	}
}

// Empty colour fields are "unset", not black: they must inherit the active
// theme so Settings → Appearance and the Save button show the real palette.
func TestThemeColorsFallBackWhenUnset(t *testing.T) {
	cfg := &Config{}
	cfg.Variables.Custom = map[string]interface{}{}

	ensureThemeColors(cfg)
	assertThemeColor(t, cfg, "buttonColor", GetThemePreset("dark").ButtonColor)
	assertThemeColor(t, cfg, "labelColor", GetThemePreset("dark").LabelColor)
	assertThemeColor(t, cfg, "inputColor", GetThemePreset("dark").InputColor)

	// The Appearance form and every $color reference must agree with the healed
	// config, not print/render #000000 for an empty slot.
	if got := inputVariableValue(cfg, "buttonColor"); got != GetThemePreset("dark").ButtonColor {
		t.Fatalf("Appearance button field = %q, want the theme colour %q", got, GetThemePreset("dark").ButtonColor)
	}
	wantR, wantG, wantB := hexToRGB(GetThemePreset("dark").ButtonColor)
	if got := resolveColor(cfg, "$buttonColor", sdl.Color{R: 1, G: 2, B: 3, A: 255}); got != (sdl.Color{R: wantR, G: wantG, B: wantB, A: 255}) {
		t.Fatalf("resolveColor($buttonColor) = %v, want the theme colour", got)
	}
}

// The shipped config stores colours under capitalised keys (ButtonColor).
// Those must survive the user merge, whose zero struct fields used to hide them
// from the Appearance form and paint every swatch black.
func TestShippedCapitalisedColorsResolve(t *testing.T) {
	cfg := &Config{}
	cfg.Variables.Custom = map[string]interface{}{
		"ButtonColor": "#1E293B",
		"LabelColor":  "#F8FAFC",
		"InputColor":  "#0F1420",
	}

	mergeUserConfig(cfg, nil)

	for name, want := range map[string]string{
		"buttonColor": "#1e293b",
		"labelColor":  "#f8fafc",
		"inputColor":  "#0f1420",
	} {
		if got := inputVariableValue(cfg, name); got != want {
			t.Fatalf("Appearance %s field = %q, want %q", name, got, want)
		}
	}
}

// The rendered preview must use the recovered palette, not just the form text.
func TestAppearancePreviewShowsCapitalisedSavedColors(t *testing.T) {
	pauseRenderGlobals(t)
	cfg := &Config{}
	cfg.Variables.Custom = map[string]interface{}{
		"ButtonColor": "#1E293B",
		"LabelColor":  "#F8FAFC",
		"InputColor":  "#0F1420",
	}
	mergeUserConfig(cfg, nil)

	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = probeW, probeH
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })

	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderAppearancePreview(renderer, cfg)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}

	btn := sdl.Color{R: 30, G: 41, B: 59, A: 255}
	if got := f.countColor(btn, 640, 184, 850, 238); got < 500 {
		t.Fatalf("preview button still black: %d pixels of the saved colour", got)
	}
	inp := sdl.Color{R: 15, G: 20, B: 32, A: 255}
	if got := f.countColor(inp, 640, 290, 1230, 340); got < 500 {
		t.Fatalf("preview input still black: %d pixels of the saved colour", got)
	}
}

func TestThemeColorsUseSelectedPreset(t *testing.T) {
	cfg := &Config{}
	cfg.Variables.Custom = map[string]interface{}{"theme_preset": "light"}

	ensureThemeColors(cfg)
	assertThemeColor(t, cfg, "buttonColor", GetThemePreset("light").ButtonColor)
	assertThemeColor(t, cfg, "labelColor", GetThemePreset("light").LabelColor)
	assertThemeColor(t, cfg, "inputColor", GetThemePreset("light").InputColor)
}

// An explicitly saved black is a real choice and must survive the fallback.
func TestThemeColorsPreserveExplicitBlack(t *testing.T) {
	cfg := &Config{}
	cfg.Variables.Custom = map[string]interface{}{"buttonColor": "#000000"}

	ensureThemeColors(cfg)
	if got := effectiveThemeColor(cfg, "buttonColor"); !isZeroRGB(got) {
		t.Fatalf("explicit black became %+v; the fallback must not overwrite it", got)
	}
	if got := inputVariableValue(cfg, "buttonColor"); got != "#000000" {
		t.Fatalf("explicit black field = %q, want #000000", got)
	}
}

func assertThemeColor(t *testing.T, cfg *Config, name, wantHex string) {
	t.Helper()
	r, g, b := hexToRGB(wantHex)
	want := RGB{R: int(r), G: int(g), B: int(b)}
	if got := themeColorValue(cfg, name); got != want {
		t.Fatalf("%s = %+v, want %+v (%s)", name, got, want, wantHex)
	}
}

// Color values must parse strictly: the swatch helpers are what decide whether
// a value is a colour at all.
func TestParseHexColorStrict(t *testing.T) {
	cases := []struct {
		in   string
		want sdl.Color
		ok   bool
	}{
		{"#3498db", sdl.Color{R: 52, G: 152, B: 219, A: 255}, true},
		{"3498DB", sdl.Color{R: 52, G: 152, B: 219, A: 255}, true},
		{"  #FFFFFF  ", sdl.Color{R: 255, G: 255, B: 255, A: 255}, true},
		{"#ff00", sdl.Color{}, false},
		{"#gg0000", sdl.Color{}, false},
		{"", sdl.Color{}, false},
		{"none", sdl.Color{}, false},
	}
	for _, tc := range cases {
		got, ok := parseHexColor(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseHexColor(%q) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
