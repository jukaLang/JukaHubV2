package main

// themeconversions_test.go — the total-conversion tab: enough distinct looks to
// be worth a tab of their own, each one readable (the focus ring included), and
// each tinting the backdrop's glows the way its card promises.
// TestThemePresetsAreReadable (themes_test.go) already holds every preset to
// WCAG AA for text; these are the conversions' own stricter floors.

import "testing"

// TestTotalConversionsAreDistinct: the original two plus at least eight more,
// no two built on the same palette or sharing a name, and both home
// arrangements well represented, so the tab is not one layout in ten colours.
func TestTotalConversionsAreDistinct(t *testing.T) {
	if n := len(themeConversionSpecs); n < 10 {
		t.Fatalf("%d total conversions, want the first two and at least eight more", n)
	}
	palettes := map[string]string{}
	names := map[string]string{}
	arrangements := map[string]int{}
	for _, spec := range themeConversionSpecs {
		if other, dup := palettes[spec.palette]; dup {
			t.Errorf("%s and %s are both the %q palette", other, spec.key, spec.palette)
		}
		palettes[spec.palette] = spec.key
		if other, dup := names[spec.name]; dup {
			t.Errorf("%s and %s are both called %q", other, spec.key, spec.name)
		}
		names[spec.name] = spec.key
		if _, ok := themePresets[spec.palette]; !ok {
			t.Errorf("%s: its palette %q is not in the catalogue", spec.key, spec.palette)
		}
		arrangements[spec.arrangement]++
	}
	for _, arrangement := range []string{homeArrangementDashboard, homeArrangementGrid} {
		if arrangements[arrangement] < 3 {
			t.Errorf("only %d conversions use the %q arrangement", arrangements[arrangement], arrangement)
		}
	}
}

// TestTotalConversionsAreReadable holds each conversion to the floors its
// screens need: body text on the page and on raised rows, labels on buttons,
// and an accent that stands off the page, because the accent is the focus ring.
func TestTotalConversionsAreReadable(t *testing.T) {
	for _, spec := range themeConversionSpecs {
		p := GetThemePreset(spec.key)
		for _, c := range []struct {
			what string
			a, b string
			min  float64
		}{
			{"primary text on the page", p.TextPrimary, p.Background, 7},
			{"secondary text on the page", p.TextSecondary, p.Background, 4.5},
			{"tertiary text on the page", p.TextTertiary, p.Background, 4.5},
			{"primary text on a raised row", p.TextPrimary, p.SurfaceRaised, 4.5},
			{"button labels on buttons", p.LabelColor, p.ButtonColor, 4.5},
			{"the focus accent on the page", p.Info, p.Background, 3},
		} {
			if got := contrastRatio(parseRGB(t, c.a), parseRGB(t, c.b)); got < c.min {
				t.Errorf("%s: %s is %.2f:1 (%s on %s), want >= %.1f:1", spec.key, c.what, got, c.a, c.b, c.min)
			}
		}
	}
}

// TestTotalConversionsTintTheBackdrop: every conversion added after the first
// two names its own glow pair, each a plain colour, and applying it is what the
// backdrop draws; a preset without a pair keeps the shipped glows.
func TestTotalConversionsTintTheBackdrop(t *testing.T) {
	for i, spec := range themeConversionSpecs {
		p := GetThemePreset(spec.key)
		if i >= 2 && (p.Bloom == "" || p.Bloom2 == "") {
			t.Errorf("%s: names no glow pair (bloom %q, bloom2 %q)", spec.key, p.Bloom, p.Bloom2)
		}
		for _, hex := range []string{p.Bloom, p.Bloom2} {
			if hex == "" {
				continue
			}
			if _, ok := parseHexColor(hex); !ok || hex[0] != '#' {
				t.Errorf("%s: glow %q is not #rrggbb", spec.key, hex)
			}
		}
		config := &Config{}
		config.Variables.Custom = map[string]interface{}{"theme_preset": spec.key}
		first, second := themeBackdropBlooms(config)
		if want, ok := parseHexColor(p.Bloom); ok {
			want.A = 255
			if first != want {
				t.Errorf("%s: the backdrop's first glow is %v, want %v", spec.key, first, want)
			}
		}
		if want, ok := parseHexColor(p.Bloom2); ok {
			want.A = 255
			if second != want {
				t.Errorf("%s: the backdrop's second glow is %v, want %v", spec.key, second, want)
			}
		}
	}

	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"theme_preset": "dark"}
	if first, second := themeBackdropBlooms(config); first != ColorAccent || second != themeDefaultBloom2 {
		t.Errorf("a preset without glows drew %v and %v, want the accent and Home's violet", first, second)
	}
}

// TestThemeFileGlowsAreValidated: a theme file may name its glows, and one that
// is not a colour is refused with the field named rather than drawn black.
func TestThemeFileGlowsAreValidated(t *testing.T) {
	p := GetThemePreset("aurora")
	file := themeFile{
		Name: "Glow test", Background: p.Background, ButtonColor: p.ButtonColor,
		LabelColor: p.LabelColor, InputColor: p.InputColor, Surface: p.Surface,
		SurfaceAlt: p.SurfaceAlt, SurfaceRaised: p.SurfaceRaised,
		TextPrimary: p.TextPrimary, TextSecondary: p.TextSecondary, TextTertiary: p.TextTertiary,
		BorderSubtle: p.BorderSubtle, BorderDefault: p.BorderDefault, BorderFocus: p.BorderFocus,
		Success: p.Success, Warning: p.Warning, Danger: p.Danger, Info: p.Info, Overlay: p.Overlay,
	}
	if _, err := file.toPreset(); err != nil {
		t.Fatalf("a file without glows was refused: %v", err)
	}
	file.Bloom, file.Bloom2 = "#ff4fd8", "#ff9e3d"
	preset, err := file.toPreset()
	if err != nil {
		t.Fatalf("a file with two colour glows was refused: %v", err)
	}
	if preset.Bloom != "#ff4fd8" || preset.Bloom2 != "#ff9e3d" {
		t.Fatalf("the glows were not carried over: %q, %q", preset.Bloom, preset.Bloom2)
	}
	for _, bad := range []string{"pink", "#12345", "ff4fd8"} {
		file.Bloom = bad
		if _, err := file.toPreset(); err == nil {
			t.Errorf("glow %q was accepted", bad)
		}
	}
}
