package main

// theme_conversion_test.go — the "Total conversion" tab: presets that rearrange
// the home screen, not just repaint it.
//
// Three things have to hold for the tab to be worth having: the conversions are
// their own list (a colour pick must not move a card), a conversion really does
// install a different arrangement, and the scene's own geometry survives the
// round trip so switching back to Custom restores what the user authored.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestTotalConversionPresetsAreTheirOwnTab(t *testing.T) {
	conversions := ListThemePresetsFor(themeKindConversion)
	if len(conversions) != len(themeConversionSpecs) {
		t.Fatalf("conversion tab has %d presets, want %d", len(conversions), len(themeConversionSpecs))
	}
	for _, spec := range themeConversionSpecs {
		found := false
		for _, name := range conversions {
			if name == spec.key {
				found = true
			}
		}
		if !found {
			t.Fatalf("conversion %q is not in the total-conversion tab", spec.key)
		}
		preset := GetThemePreset(spec.key)
		if preset.Kind != themeKindConversion {
			t.Fatalf("%s: kind = %v, want a conversion", spec.key, preset.Kind)
		}
		if preset.Name == "" || preset.Summary == "" {
			t.Fatalf("%s: a conversion needs a name and a summary of the layout it installs", spec.key)
		}
		if normalizeHomeArrangement(preset.Arrangement) != preset.Arrangement {
			t.Fatalf("%s: arrangement %q is not a real one", spec.key, preset.Arrangement)
		}
		// A conversion is a palette plus an arrangement, so its colours must be
		// exactly the ones it claims to use: a big preview that does not match
		// the result is worse than none.
		base := GetThemePreset(spec.palette)
		if preset.Background != base.Background || preset.TextPrimary != base.TextPrimary {
			t.Fatalf("%s: colours drifted from its palette %q", spec.key, spec.palette)
		}
	}

	// The colour tab holds the palettes and nothing else.
	colours := ListThemePresetsFor(themeKindColour)
	if len(colours) == 0 {
		t.Fatal("the colour tab is empty")
	}
	for _, name := range colours {
		if GetThemePreset(name).Kind != themeKindColour {
			t.Fatalf("%q leaked into the colour tab", name)
		}
	}
	if len(colours)+len(conversions) != len(ListThemePresets()) {
		t.Fatalf("%d colour + %d conversion presets, but the catalogue lists %d",
			len(colours), len(conversions), len(ListThemePresets()))
	}
}

func TestApplyingAConversionInstallsItsArrangement(t *testing.T) {
	t.Chdir(t.TempDir())
	// Applying a preset rewrites the package's colour tokens and the live home
	// layout, so both are handed back afterwards: a leaked palette turns the
	// fixed-order suite order-dependent (see palette_test.go).
	palette := capturePalette()
	prevHome := homeLayout
	homeLayout = nil
	t.Cleanup(func() {
		restorePalette(palette)
		homeLayout = prevHome
	})
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}

	ApplyThemePreset(config, "convert-grid")
	if got := homeArrangement(config); got != homeArrangementGrid {
		t.Fatalf("arrangement = %q, want the grid the conversion installs", got)
	}
	if got, _ := customString(config.Variables.Custom, "theme_preset"); got != "convert-grid" {
		t.Fatalf("theme_preset = %q, want convert-grid", got)
	}
	if preset := GetThemePreset("convert-grid"); preset.Background != config.Variables.Custom["theme_background"] {
		t.Fatal("the conversion did not write its palette")
	}

	// A colour preset is a repaint: it must leave the arrangement alone.
	ApplyThemePreset(config, "oled")
	if got := homeArrangement(config); got != homeArrangementGrid {
		t.Fatalf("a colour preset changed the arrangement to %q", got)
	}

	ApplyThemePreset(config, "convert-dashboard")
	if got := homeArrangement(config); got != homeArrangementDashboard {
		t.Fatalf("arrangement = %q, want the dashboard", got)
	}
}

func TestHomeGridArrangementLaysAHeroMosaic(t *testing.T) {
	layout := NewHomeLayout(1280, 720)
	layout.SetArrangement(homeArrangementGrid)
	if layout.Arrangement != homeArrangementGrid {
		t.Fatalf("arrangement = %q", layout.Arrangement)
	}

	// The ten ordinary cards are one cell each, and every cell is the same
	// size; the Continue card is the hero tile, two rows tall in column one.
	cells := []HomeFocusID{
		HomeFocusMedia, HomeFocusFiles, HomeFocusPackages,
		HomeFocusApps, HomeFocusFavorites, HomeFocusBrowser,
		HomeFocusChat, HomeFocusTools, HomeFocusEmulator, HomeFocusSettings,
	}
	cell := layout.Items[cells[0]].Rect
	if cell.W <= 0 || cell.H <= 0 {
		t.Fatalf("first cell is %dx%d", cell.W, cell.H)
	}
	for _, id := range cells {
		if got := layout.Items[id].Rect; got.W != cell.W || got.H != cell.H {
			t.Fatalf("%s is %dx%d, want the common cell %dx%d", id, got.W, got.H, cell.W, cell.H)
		}
	}
	hero := layout.Items[HomeFocusContinue].Rect
	if hero.W != cell.W {
		t.Fatalf("hero tile is %d wide, want one column (%d)", hero.W, cell.W)
	}
	if hero.H < 2*cell.H {
		t.Fatalf("hero tile is %d tall, want at least two rows (%d): the resume card needs the room",
			hero.H, 2*cell.H)
	}
	if hero.Y != cell.Y {
		t.Fatalf("hero tile starts at y=%d, want it in the first row (y=%d)", hero.Y, cell.Y)
	}

	// Every card inside the canvas, clear of the header band, and none sharing
	// area with another: an overlap would land a press on the wrong screen.
	body := append([]HomeFocusID{HomeFocusContinue}, cells...)
	for _, id := range body {
		rect := layout.Items[id].Rect
		if rect.X < 0 || rect.Y < 0 || rect.X+rect.W > 1280 || rect.Y+rect.H > 720 {
			t.Fatalf("%s sits outside the canvas: %+v", id, rect)
		}
		if rect.Y < 164 {
			t.Fatalf("%s overlaps the header band: %+v", id, rect)
		}
	}
	for i := 0; i < len(body); i++ {
		for j := i + 1; j < len(body); j++ {
			if rectsOverlap(layout.Items[body[i]].Rect, layout.Items[body[j]].Rect) {
				t.Fatalf("%s overlaps %s", body[i], body[j])
			}
		}
	}

	// The last row is full: ten cards plus a two-row hero tile fill the mosaic
	// exactly, so there is no half-empty row to centre.
	bottom := layout.Items[HomeFocusSettings].Rect
	for _, id := range []HomeFocusID{HomeFocusChat, HomeFocusTools, HomeFocusEmulator, HomeFocusSettings} {
		if got := layout.Items[id].Rect.Y; got != bottom.Y {
			t.Fatalf("%s is on row y=%d, want it beside the rest of the last row (y=%d)", id, got, bottom.Y)
		}
	}

	// The header chips and Exit are not part of the mosaic.
	if got := layout.Items[HomeFocusExit].Rect; got.X != 8 || got.Y != 678 {
		t.Fatalf("Exit moved to %+v under the grid arrangement", got)
	}
	if got := layout.Items[HomeFocusBell].Rect; got.Y >= 164 {
		t.Fatalf("the bell left the header band: %+v", got)
	}

	// The dashboard composition stays the default, and is not the mosaic.
	layout.SetArrangement(homeArrangementDashboard)
	if got := layout.Items[HomeFocusContinue].Rect; got != (sdl.Rect{X: 32, Y: 164, W: 480, H: 258}) {
		t.Fatalf("dashboard Continue = %+v, want the built-in card", got)
	}
}

func TestHomeArrangementKeepsTheAuthoredGeometry(t *testing.T) {
	elements := []Element{{
		Type: "button", Style: "tile", Text: "Media", Icon: "media",
		X: 100, Y: 300, Width: "400", Height: "200",
	}}

	// Custom is the default: jukaconfig.json decides where the cards are.
	layout := NewHomeLayout(1280, 720)
	layout.ApplyConfig(elements)
	if got := layout.Items[HomeFocusMedia].Rect; got != (sdl.Rect{X: 100, Y: 300, W: 400, H: 200}) {
		t.Fatalf("custom arrangement ignored the config geometry: %+v", got)
	}

	// A conversion owns the layout while it is in force, and does not touch
	// the elements it was given: the authored rect is still there afterwards.
	layout.SetArrangement(homeArrangementGrid)
	before := elements[0]
	layout.ApplyConfig(elements)
	if got := layout.Items[HomeFocusMedia].Rect; got == (sdl.Rect{X: 100, Y: 300, W: 400, H: 200}) {
		t.Fatalf("grid arrangement let the config geometry win: %+v", got)
	}
	if elements[0] != before {
		t.Fatal("ApplyConfig rewrote the scene's elements: the conversion must be reversible")
	}
	layout.SetArrangement(homeArrangementCustom)
	layout.ApplyConfig(elements)
	if got := layout.Items[HomeFocusMedia].Rect; got != (sdl.Rect{X: 100, Y: 300, W: 400, H: 200}) {
		t.Fatalf("returning to Custom did not restore the authored card: %+v", got)
	}
}

func TestThemeGalleryTabsFilterAndReset(t *testing.T) {
	prevTab, prevIndex, prevScroll := themeGalleryTab, themeGalleryIndex, themeGalleryScroll
	t.Cleanup(func() { themeGalleryTab, themeGalleryIndex, themeGalleryScroll = prevTab, prevIndex, prevScroll })

	themeGalleryTab, themeGalleryIndex, themeGalleryScroll = 0, 3, 1
	if got := themeGalleryPresets(); len(got) != len(ListThemePresetsFor(themeKindColour)) {
		t.Fatalf("colour tab lists %d presets, want %d", len(got), len(ListThemePresetsFor(themeKindColour)))
	}
	if !themeGallerySwitchTab(1) {
		t.Fatal("Tab did not switch to the total-conversion list")
	}
	if got := themeGalleryCurrentTab().Kind; got != themeKindConversion {
		t.Fatalf("open tab = %v, want the conversions", got)
	}
	if themeGalleryIndex != 0 || themeGalleryScroll != 0 {
		t.Fatalf("switching tabs kept index %d scroll %d: the card under the cursor is from another list",
			themeGalleryIndex, themeGalleryScroll)
	}
	if got := themeGalleryPresets(); len(got) != len(themeConversionSpecs) {
		t.Fatalf("conversion tab lists %d presets, want %d", len(got), len(themeConversionSpecs))
	}

	// The tab strip is clickable, and the grid starts below it.
	newProbeFrame(t)
	element := Element{Type: "themegallery", X: 80, Y: 100, Width: "1160", Height: "560"}
	for i := range themeGalleryTabs {
		rect := themeGalleryTabRect(element, i)
		if got := themeGalleryTabAt(element, rect.X+rect.W/2, rect.Y+rect.H/2); got != i {
			t.Fatalf("a click in tab %d resolved to %d", i, got)
		}
	}
	if got := themeGalleryTabAt(element, element.X, element.Y+themeGalleryGridOffset()+40); got != -1 {
		t.Fatalf("a click in the card grid resolved to tab %d", got)
	}
	_, _, visibleRows := themeGalleryMetrics(element)
	if strip := themeGalleryGridOffset(); strip <= 0 || visibleRows <= 0 {
		t.Fatalf("strip %d, visible rows %d", strip, visibleRows)
	}
}
