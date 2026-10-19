package main

import (
	"fmt"
	"math"
	"regexp"
	"testing"
)

var themeColorPattern = regexp.MustCompile(`^#(?:[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

// relativeLuminance implements WCAG 2.1 for an sRGB colour.
func relativeLuminance(c rgbValue) float64 {
	channel := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(c.r) + 0.7152*channel(c.g) + 0.0722*channel(c.b)
}

type rgbValue struct{ r, g, b uint8 }

func parseRGB(t *testing.T, hex string) rgbValue {
	t.Helper()
	if !themeColorPattern.MatchString(hex) {
		t.Fatalf("colour %q is not #rrggbb or #rrggbbaa", hex)
	}
	var r, g, b uint8
	if _, err := fmt.Sscanf(hex[1:7], "%02x%02x%02x", &r, &g, &b); err != nil {
		t.Fatalf("colour %q: %v", hex, err)
	}
	return rgbValue{r, g, b}
}

// contrastRatio returns the WCAG contrast ratio between two colours.
func contrastRatio(a, b rgbValue) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// TestThemePresetCatalogue: the app ships a broad palette (at least 21
// themes), every preset is listed exactly once, and the requested looks exist
// by name.
func TestThemePresetCatalogue(t *testing.T) {
	names := ListThemePresets()
	if len(names) < 21 {
		t.Fatalf("only %d themes, want at least 21", len(names))
	}
	if len(themePresets) != len(names) {
		t.Fatalf("%d presets but %d listed: every preset must be in the gallery", len(themePresets), len(names))
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			t.Fatalf("theme %q listed twice", name)
		}
		seen[name] = true
		if _, ok := themePresets[name]; !ok {
			t.Fatalf("listed theme %q does not exist", name)
		}
		if themePresets[name].Name == "" {
			t.Fatalf("theme %q has no display name", name)
		}
	}
	for _, want := range []string{
		"light", "dark", "anime", "cute", "girly", "highcontrast",
		"red", "green", "blue", "fallout",
	} {
		if _, ok := themePresets[want]; !ok {
			t.Errorf("required theme %q is missing", want)
		}
	}
}

// TestThemePresetsAreReadable validates every colour field and the crucial
// text/background pairs, so a pretty palette can never ship unreadable text.
func TestThemePresetsAreReadable(t *testing.T) {
	for name, p := range themePresets {
		bg := parseRGB(t, p.Background)
		primary := parseRGB(t, p.TextPrimary)
		secondary := parseRGB(t, p.TextSecondary)
		if ratio := contrastRatio(primary, bg); ratio < 4.5 {
			t.Errorf("%s: primary text contrast %.2f on %s, want >= 4.5", name, ratio, p.Background)
		}
		if ratio := contrastRatio(secondary, bg); ratio < 3.0 {
			t.Errorf("%s: secondary text contrast %.2f on %s, want >= 3.0", name, ratio, p.Background)
		}
		surface := parseRGB(t, p.Surface)
		if ratio := contrastRatio(primary, surface); ratio < 3.5 {
			t.Errorf("%s: primary text contrast %.2f on its surface, want >= 3.5", name, ratio)
		}
		for field, value := range map[string]string{
			"background": p.Background, "button": p.ButtonColor, "label": p.LabelColor,
			"input": p.InputColor, "surface": p.Surface, "surfaceAlt": p.SurfaceAlt,
			"surfaceRaised": p.SurfaceRaised, "textPrimary": p.TextPrimary,
			"textSecondary": p.TextSecondary, "textTertiary": p.TextTertiary,
			"borderSubtle": p.BorderSubtle, "borderDefault": p.BorderDefault,
			"borderFocus": p.BorderFocus, "success": p.Success, "warning": p.Warning,
			"danger": p.Danger, "info": p.Info, "overlay": p.Overlay,
		} {
			if !themeColorPattern.MatchString(value) {
				t.Errorf("%s.%s = %q is not a valid colour", name, field, value)
			}
		}
		// The accent must be visible against the background: it drives focus
		// rings and the current-theme badge.
		accent := parseRGB(t, p.Info)
		if contrastRatio(accent, bg) < 2.0 {
			t.Errorf("%s: accent %s is nearly invisible on %s", name, p.Info, p.Background)
		}
	}
}

// TestThemeGalleryGridFitsAndScrolls: 25 presets no longer fit in one row, so
// the gallery is a grid that scrolls with the focus and hit-tests correctly.
func TestThemeGalleryGridFitsAndScrolls(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	idx := findSceneIndex(config, "ThemePresets")
	if idx < 0 {
		t.Fatal("ThemePresets scene missing")
	}
	var element Element
	for _, e := range config.Scenes[idx].Elements {
		if e.Type == "themegallery" {
			element = e
		}
	}
	if element.Type != "themegallery" {
		t.Fatal("ThemePresets has no themegallery element")
	}

	prevIndex, prevScroll := themeGalleryIndex, themeGalleryScroll
	t.Cleanup(func() { themeGalleryIndex, themeGalleryScroll = prevIndex, prevScroll })

	presets := ListThemePresets()
	cols, cardW, visibleRows := themeGalleryMetrics(element)
	if cols < 3 {
		t.Fatalf("gallery has %d columns, want at least 3", cols)
	}
	if cardW < themeGalleryMinW {
		t.Fatalf("card width %d is narrower than %d", cardW, themeGalleryMinW)
	}
	rows := (len(presets) + cols - 1) / cols
	if rows <= visibleRows {
		t.Fatalf("%d presets in %d visible rows: the gallery does not need to scroll", len(presets), visibleRows)
	}

	themeGalleryIndex, themeGalleryScroll = 0, 0
	if got := themeGalleryCardAt(element, element.X+8, element.Y+8); got != 0 {
		t.Fatalf("click on the first card hit %d, want 0", got)
	}
	// The gutter between two cards is not a card.
	if got := themeGalleryCardAt(element, element.X+cardW+4, element.Y+8); got != -1 {
		t.Fatalf("click in the column gutter hit %d, want -1", got)
	}
	// Hit-testing follows the focused row's scroll offset.
	last := len(presets) - 1
	themeGalleryIndex = last
	scroll := themeGalleryScrollFor(element, last)
	if scroll != rows-visibleRows {
		t.Fatalf("scroll row %d for the last card, want %d", scroll, rows-visibleRows)
	}
	lastRow, lastCol := last/cols, last%cols
	x := element.X + int32(lastCol)*(cardW+themeGalleryGap) + 8
	y := element.Y + int32(lastRow-scroll)*(themeGalleryH+themeGalleryGap) + 8
	if got := themeGalleryCardAt(element, x, y); got != last {
		t.Fatalf("click on the last card hit %d, want %d", got, last)
	}

	// Down moves a whole row and clamps at the end of the catalogue.
	themeGalleryIndex, themeGalleryScroll = 0, 0
	if !themeGalleryMove(element, cols) {
		t.Fatal("Down did not move the gallery focus")
	}
	if themeGalleryIndex != cols {
		t.Fatalf("after one row focus is %d, want %d", themeGalleryIndex, cols)
	}
	themeGalleryIndex = last
	if themeGalleryMove(element, cols) {
		t.Fatal("moving past the last theme should be a no-op")
	}
	if themeGalleryIndex != last {
		t.Fatalf("focus moved past the end: %d", themeGalleryIndex)
	}
	// Moving back up keeps the focused card inside the visible band, and a jump
	// to the first row returns the grid to the top.
	themeGalleryIndex, themeGalleryScroll = last, 0
	themeGalleryScrollFor(element, last)
	themeGalleryMove(element, -cols)
	focusedRow := themeGalleryIndex / cols
	if focusedRow < themeGalleryScroll || focusedRow >= themeGalleryScroll+visibleRows {
		t.Fatalf("focused row %d is outside the visible band starting at %d", focusedRow, themeGalleryScroll)
	}
	themeGalleryScrollFor(element, 0)
	if themeGalleryScroll != 0 {
		t.Fatalf("scroll row %d at the first card, want 0", themeGalleryScroll)
	}
}
