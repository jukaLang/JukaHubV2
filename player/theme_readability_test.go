package main

// theme_readability_test.go — the shipped palette catalogue, checked as text.
//
// The theme gallery ships many palettes, and the accessibility variants are
// not just tints: they substitute the semantic colours too. These tests apply
// every catalogue entry at all three supported canvases and assert the values
// the UI actually paints. The player test is the real-font half: it draws the
// two music layouts and the Start menu, then checks the measured hint and row
// geometry, so a longer label at 145% cannot hide behind a different colour.

import (
	"math"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func themeReadabilityContrast(a, b sdl.Color) float64 {
	la, lb := wcagLuminance(a), wcagLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func themeReadabilitySurfaces() []struct {
	name string
	c    sdl.Color
} {
	return []struct {
		name string
		c    sdl.Color
	}{
		{"background", ColorBackground},
		{"surface", ColorSurface},
		{"surface-alt", ColorSurfaceAlt},
		{"surface-raised", ColorSurfaceRaised},
	}
}

func themeReadabilityApply(t *testing.T, preset string) {
	t.Helper()
	if _, ok := themePresets[preset]; !ok {
		t.Fatalf("theme preset %q is missing", preset)
	}
	a11y.increaseContrast = false
	a11y.highContrast = false
	a11y.colorblind = ""
	a11y.reduceTransparency = false
	ApplyThemeColors(themePresets[preset])
	glassThemeCache.valid = false
	ClearTextCache()
	ClearShapeCache()
	ClearIconAtlas()
	clearHomeTextCache()
}

func themeReadabilityProfile(c containmentProfile) {
	screenWidth, screenHeight = c.width, c.height
	a11y.fontScale = c.scale
	a11yRefreshFonts()
	ClearTextCache()
	ClearShapeCache()
	ClearIconAtlas()
	clearHomeTextCache()
}

func themeReadabilityRestore(t *testing.T) {
	t.Helper()
	prev := capturePalette()
	oldA11y := a11y
	oldW, oldH, oldScene := screenWidth, screenHeight, currentSceneIndex
	oldMenu := tb
	t.Cleanup(func() {
		restorePalette(prev)
		a11y = oldA11y
		screenWidth, screenHeight, currentSceneIndex = oldW, oldH, oldScene
		tb = oldMenu
		a11yRefreshFonts()
		ClearTextCache()
		ClearShapeCache()
		ClearIconAtlas()
		clearHomeTextCache()
	})
}

func themeReadabilityConfig() *Config {
	return &Config{Scenes: []SceneConfig{
		{Name: "Main", Icon: "H"}, {Name: "Media", Icon: "M"}, {Name: "Tube", Icon: "Y"},
		{Name: "LiveTV", Icon: "L"}, {Name: "FileExplorer", Icon: "F"}, {Name: "Chat", Icon: "C"},
		{Name: "Tools", Icon: "T"}, {Name: "Calculator", Icon: "C"}, {Name: "Pomodoro", Icon: "P"},
		{Name: "Alarm", Icon: "A"}, {Name: "Dashboard", Icon: "D"}, {Name: "Cron", Icon: "R"},
	}}
}

func themeReadabilitySeedMenu(cfg *Config) {
	tb.startOpen = true
	tb.startRect = sdl.Rect{X: 80, Y: 8, W: 84, H: 34}
	tb.group, tb.focus, tb.cursor, tb.scroll = 0, 1, 0, 0
	tb.enter.cancel()
}

func themeReadabilitySeedMusic(t *testing.T, cfg *Config, layout musicLayout) {
	t.Helper()
	musicSetLayout(cfg, layout)
	music.mu.Lock()
	music.tracks = musicContainmentTracks()
	music.current, music.cursor = 0, 0
	music.playing, music.paused, music.ready = true, false, true
	music.position, music.duration = 62, 3723
	music.visualizerFull = false
	music.searchOpen = false
	music.mu.Unlock()
}

// TestThemeTextTokensReadOnEveryPresetAndCanvas applies every palette in the
// catalogue (colours and their total conversions) at the wide canvas, the
// compact canvas and the compact canvas at 145%, and checks the text tokens
// against every surface they are painted on.
func TestThemeTextTokensReadOnEveryPresetAndCanvas(t *testing.T) {
	themeReadabilityRestore(t)
	oldW, oldH, oldScale := screenWidth, screenHeight, a11y.fontScale
	t.Cleanup(func() { screenWidth, screenHeight, a11y.fontScale = oldW, oldH, oldScale })

	profiles := containmentProfiles(t)
	for _, preset := range ListThemePresets() {
		themeReadabilityApply(t, preset)
		for _, profile := range profiles {
			screenWidth, screenHeight = profile.width, profile.height
			for _, ink := range []struct {
				name string
				c    sdl.Color
			}{
				{"primary", ColorTextPrimary()},
				{"secondary", ColorTextSecondary()},
				{"tertiary", ColorTextTertiary()},
				{"muted", ColorTextMuted()},
				{"accent", ColorTextAccent()},
				{"success", SuccessInk()},
				{"warning", WarningInk()},
				{"danger", DangerInk()},
				{"info", InfoInk()},
				{"disabled", ColorTextTertiary()},
			} {
				for _, surface := range themeReadabilitySurfaces() {
					if got := themeReadabilityContrast(ink.c, surface.c); got < textInkMinContrast {
						t.Fatalf("%s/%s: %s ink %v on %s %v = %.2f:1, want >= %.1f:1",
							preset, profile.name, ink.name, ink.c, surface.name, surface.c, got, textInkMinContrast)
					}
				}
			}
			for _, surface := range themeReadabilitySurfaces() {
				if got := themeReadabilityContrast(glassAccent(), surface.c); got < 3.0 {
					t.Fatalf("%s/%s: focus ring %v on %s %v = %.2f:1, want >= 3:1",
						preset, profile.name, glassAccent(), surface.name, surface.c, got)
				}
				if got := themeReadabilityContrast(testBlendOver(glassA(scrollbarThumbAlpha), surface.c), surface.c); got < 3.0 {
					t.Fatalf("%s/%s: scrollbar thumb on %s = %.2f:1, want >= 3:1",
						preset, profile.name, surface.name, got)
				}
				if got := themeReadabilityContrast(testBlendOver(glassA(22), surface.c), surface.c); got < 1.15 {
					t.Fatalf("%s/%s: scrollbar track on %s = %.2f:1, want >= 1.15:1",
						preset, profile.name, surface.name, got)
				}
			}
		}
	}
}

// TestThemePanelRaisedFollowsPreset locks the legacy alias that the player's
// Search/Exit pills and artwork placeholders still paint with. It stayed at
// the shipped #2D3644 for every theme, so Light drew dark labels on a dark
// slate pill.
func TestThemePanelRaisedFollowsPreset(t *testing.T) {
	themeReadabilityRestore(t)
	for _, preset := range ListThemePresets() {
		themeReadabilityApply(t, preset)
		if ColorPanelRaised != ColorSurfaceRaised {
			t.Fatalf("%s: ColorPanelRaised = %v, want the preset's SurfaceRaised %v",
				preset, ColorPanelRaised, ColorSurfaceRaised)
		}
	}
}

// TestThemeSemanticInksStayDistinct checks that an adjusted semantic ink keeps
// the hue it was authored with instead of collapsing into body white/black. A
// pair may share a hue only when its luminance still separates the states.
func TestThemeSemanticInksStayDistinct(t *testing.T) {
	themeReadabilityRestore(t)
	for _, preset := range ListThemePresets() {
		themeReadabilityApply(t, preset)
		raw := []struct {
			name string
			c    sdl.Color
			ink  sdl.Color
		}{
			{"danger", ColorDanger, DangerInk()},
			{"warning", ColorWarning, WarningInk()},
			{"success", ColorSuccess, SuccessInk()},
			{"accent", ColorInfo, ColorTextAccent()},
		}
		for _, v := range raw {
			rh, rs, _ := rgbToHSL(v.c)
			ih, is, _ := rgbToHSL(v.ink)
			if rs < 0.2 {
				continue
			}
			if is < rs*0.45 {
				t.Fatalf("%s: %s ink %v lost its saturation (%.2f -> %.2f) from %v",
					preset, v.name, v.ink, rs, is, v.c)
			}
			dh := math.Abs(rh - ih)
			if dh > 0.5 {
				dh = 1 - dh
			}
			if dh > 0.04 {
				t.Fatalf("%s: %s ink %v changed hue from %v (%.3f -> %.3f)",
					preset, v.name, v.ink, v.c, rh, ih)
			}
		}
		for i := 0; i < len(raw); i++ {
			for j := i + 1; j < len(raw); j++ {
				if raw[i].c == raw[j].c {
					continue // a deliberately monochrome palette
				}
				h1, s1, _ := rgbToHSL(raw[i].ink)
				h2, s2, _ := rgbToHSL(raw[j].ink)
				dh := math.Abs(h1 - h2)
				if dh > 0.5 {
					dh = 1 - dh
				}
				if s1 < 0.2 || s2 < 0.2 {
					continue // a grey semantic is the palette's choice
				}
				if dh*360 < 10 && themeReadabilityContrast(raw[i].ink, raw[j].ink) < 1.25 {
					t.Fatalf("%s: %s %v and %s %v are neither hue- nor luminance-separated",
						preset, raw[i].name, raw[i].ink, raw[j].name, raw[j].ink)
				}
			}
		}
	}
}

// TestThemePlayerHintsAndStartMenuAtEveryPalette draws the two music layouts
// and the Start menu with the real fonts at every palette and profile. The
// hint ink is carried by the layout so this checks what was painted, not a
// duplicate constant.
func TestThemePlayerHintsAndStartMenuAtEveryPalette(t *testing.T) {
	themeReadabilityRestore(t)
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	musicContainmentSeed(t)
	cfg := themeReadabilityConfig()

	for _, preset := range ListThemePresetsFor(themeKindColour) {
		themeReadabilityApply(t, preset)
		for _, profile := range containmentProfiles(t) {
			t.Run(preset+"/"+profile.name, func(t *testing.T) {
				themeReadabilityProfile(profile)
				themeReadabilityApply(t, preset)

				// Start menu: its measured header and first row must stay
				// apart after the palette changes.
				themeReadabilitySeedMenu(cfg)
				ClearTextCache()
				ClearShapeCache()
				ClearIconAtlas()
				renderer := menuContainmentRenderer(t, profile.width, profile.height)
				renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
				renderer.Clear()
				renderStartMenu(renderer, cfg)
				l := startMenuLayout
				if l.firstRow.W <= 0 || l.subtitle.W <= 0 {
					t.Fatalf("Start menu geometry missing: subtitle=%+v firstRow=%+v", l.subtitle, l.firstRow)
				}
				if l.subtitle.Y+l.subtitle.H+2 > l.firstRow.Y {
					t.Fatalf("Start menu subtitle %+v runs into first row %+v", l.subtitle, l.firstRow)
				}

				for _, layout := range []struct {
					name   string
					layout musicLayout
				}{{"auralis", musicLayoutAuralis}, {"stockos", musicLayoutStockOS}} {
					themeReadabilitySeedMusic(t, cfg, layout.layout)
					ClearTextCache()
					ClearShapeCache()
					ClearIconAtlas()
					r := menuContainmentRenderer(t, profile.width, profile.height)
					r.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
					r.Clear()
					renderMusicPlayer(r, cfg)

					var hintInk sdl.Color
					if layout.layout == musicLayoutAuralis {
						hintInk = musicAuralisLayout.hintInk
						if len(musicAuralisLayout.hints) != 2 || len(musicAuralisLayout.hintLines) != 2 {
							t.Fatalf("%s/%s: Auralis hints = %d/%d, want 2/2", preset, profile.name,
								len(musicAuralisLayout.hints), len(musicAuralisLayout.hintLines))
						}
						for i, rect := range musicAuralisLayout.hints {
							if rect.X < 0 || rect.Y < 0 || rect.X+rect.W > profile.width || rect.Y+rect.H > profile.height {
								t.Fatalf("%s/%s: Auralis hint %d = %+v, outside %dx%d", preset, profile.name,
									i, rect, profile.width, profile.height)
							}
							if i > 0 && musicAuralisLayout.hints[i-1].Y+musicAuralisLayout.hints[i-1].H > rect.Y {
								t.Fatalf("%s/%s: Auralis hints overlap: %+v and %+v", preset, profile.name,
									musicAuralisLayout.hints[i-1], rect)
							}
						}
						if full := musicPlayerHintLines(); musicAuralisLayout.hintLines[0] != full[0] ||
							musicAuralisLayout.hintLines[1] != full[1] {
							t.Fatalf("%s/%s: Auralis hints truncated: %q / %q", preset, profile.name,
								musicAuralisLayout.hintLines[0], musicAuralisLayout.hintLines[1])
						}
					} else {
						hintInk = musicStockOSLayout.hintInk
						if musicStockOSLayout.hintY < 0 || musicStockOSLayout.hintY > profile.height-20 {
							t.Fatalf("%s/%s: StockOS hint y = %d, outside %dx%d", preset, profile.name,
								musicStockOSLayout.hintY, profile.width, profile.height)
						}
						if musicStockOSLayout.rowName.W <= 0 || musicStockOSLayout.rowDuration.W <= 0 {
							t.Fatalf("%s/%s: StockOS row geometry missing", preset, profile.name)
						}
					}
					for _, surface := range themeReadabilitySurfaces() {
						if got := themeReadabilityContrast(hintInk, surface.c); got < textInkMinContrast {
							t.Fatalf("%s/%s: %s hint ink %v on %s = %.2f:1, want >= %.1f:1",
								preset, profile.name, layout.name, hintInk, surface.name, got, textInkMinContrast)
						}
					}
				}
			})
		}
	}
}

// TestThemeErrorStatePaintsItsTone checks the failure title itself, not just
// its glyph. The block used to paint every title in body ink, so once DangerInk
// was lightened or darkened for contrast the failure read as an ordinary empty
// state. The title must contain the danger ink on every palette and canvas.
func TestThemeErrorStatePaintsItsTone(t *testing.T) {
	themeReadabilityRestore(t)
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	cfg := &Config{}
	for _, preset := range ListThemePresetsFor(themeKindColour) {
		for _, profile := range containmentProfiles(t) {
			t.Run(preset+"/"+profile.name, func(t *testing.T) {
				themeReadabilityProfile(profile)
				themeReadabilityApply(t, preset)
				surface, err := sdl.CreateRGBSurface(0, profile.width, profile.height, 32, 0, 0, 0, 0)
				if err != nil {
					t.Fatalf("create surface: %v", err)
				}
				defer surface.Free()
				renderer, err := sdl.CreateSoftwareRenderer(surface)
				if err != nil {
					t.Fatalf("create renderer: %v", err)
				}
				defer renderer.Destroy()
				renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
				renderer.Clear()
				rect := sdl.Rect{X: 40, Y: 40, W: profile.width - 80, H: profile.height - 80}
				UIErrorState(renderer, cfg, rect, "@warning", "Couldn't load this show", "feed could not be reached")

				glyphH, titleH, _, _ := uiStateBlockMetrics(cfg, "@warning", "Couldn't load this show", "feed could not be reached")
				titleFont := FontForRole(cfg, TypeHeading)
				if titleFont == nil {
					t.Fatal("heading font did not load")
				}
				tw, _, _ := titleFont.SizeUTF8("Couldn't load this show")
				stackH := glyphH + titleH
				y := rect.Y + (rect.H-stackH)/2 + glyphH
				x := rect.X + rect.W/2 - int32(tw)/2
				w, h := int32(tw)+4, titleH
				if x < 0 || y < 0 || x+w > profile.width || y+h > profile.height {
					t.Fatalf("title box %d,%d,%d,%d outside canvas", x, y, w, h)
				}
				pixels := make([]byte, int(profile.width*profile.height*4))
				if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&pixels[0]), int(profile.width*4)); err != nil {
					t.Fatalf("read pixels: %v", err)
				}
				tone := DangerInk()
				found := false
				for yy := y; yy < y+h && !found; yy++ {
					for xx := x; xx < x+w; xx++ {
						off := (int(yy)*int(profile.width) + int(xx)) * 4
						// ARGB8888 is B,G,R,A in memory.
						if abs8(pixels[off+2], tone.R) <= 2 && abs8(pixels[off+1], tone.G) <= 2 && abs8(pixels[off], tone.B) <= 2 {
							found = true
							break
						}
					}
				}
				if !found {
					t.Fatalf("%s/%s: no %v pixels in the title box; the error title is not danger-coloured",
						preset, profile.name, tone)
				}
			})
		}
	}
}

func abs8(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}
