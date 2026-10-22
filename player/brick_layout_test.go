package main

// The Brick family draws the 1280x720 design into a 960x720 canvas. These
// checks pin the two things that squeeze breaks if type does not follow the
// geometry: logical font sizes scale with the canvas, and the controller hint
// row is planned to fit inside it instead of running off both edges.

import (
	"strings"
	"testing"
)

func TestCompactCanvasScalesLogicalFontSizes(t *testing.T) {
	withScreen(t, uiCanvasWideWidth, uiCanvasWideHeight)
	if got := uiTextScaleFactor(); got != 1 {
		t.Fatalf("wide canvas text scale = %v, want 1", got)
	}
	if got := uiScaleFontSize(24); got != 24 {
		t.Fatalf("wide canvas font size = %d, want 24 (unchanged)", got)
	}

	withScreen(t, uiCanvasCompactW, uiCanvasCompactH)
	if got := uiTextScaleFactor(); got != 0.75 {
		t.Fatalf("compact text scale = %v, want 0.75", got)
	}
	cases := map[int]int{24: 18, 16: 12, 52: 39}
	for size, want := range cases {
		if got := uiScaleFontSize(size); got != want {
			t.Errorf("uiScaleFontSize(%d) = %d, want %d", size, got, want)
		}
	}
	// Tiny captions keep a legible floor rather than shrinking below it.
	if got := uiScaleFontSize(10); got != 9 {
		t.Errorf("uiScaleFontSize(10) = %d, want the 9px floor", got)
	}
	// Scaling never makes a size larger than the caller asked for.
	if got := uiScaleFontSize(6); got != 6 {
		t.Errorf("uiScaleFontSize(6) = %d, want 6", got)
	}
}

func TestCompactCanvasShrinksResolvedFont(t *testing.T) {
	newKeyboardTestRenderer(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	withScreen(t, uiCanvasWideWidth, uiCanvasWideHeight)
	ClearTextCache()
	_, wideSize := getCachedFont(config, "medium")

	withScreen(t, uiCanvasCompactW, uiCanvasCompactH)
	ClearTextCache()
	font, compactSize := getCachedFont(config, "medium")
	if font == nil {
		t.Fatal("no font resolved on the compact canvas")
	}
	if compactSize >= wideSize {
		t.Fatalf("compact font size = %d, want smaller than the wide size %d", compactSize, wideSize)
	}
}

// Every scene's hint row must fit the brick panel. Tube carries the longest row
// (seven groups) and used to be clipped to "d Navigate" … "R2".
func TestFooterHintsFitCompactCanvas(t *testing.T) {
	newKeyboardTestRenderer(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	withScreen(t, uiCanvasCompactW, uiCanvasCompactH)
	t.Cleanup(ClearTextCache)

	avail := screenWidth - 24
	mustKeepEveryHint := map[string]bool{"Tube": true, "Chat": true, "Apps": true, "Settings": true}
	longest := 0
	for i := range config.Scenes {
		scene := config.Scenes[i]
		hints := footerHints(config, scene)
		plan := planFooterHints(config, hints, avail)
		if plan.font == nil {
			t.Fatalf("%s: no font for the hint row", scene.Name)
		}
		if plan.width > avail {
			t.Errorf("%s: hint row is %dpx wide, only %dpx available", scene.Name, plan.width, avail)
		}
		if len(plan.hints) < len(hints) && mustKeepEveryHint[scene.Name] {
			t.Errorf("%s: dropped %d hints on the compact canvas (%v kept)",
				scene.Name, len(hints)-len(plan.hints), plan.hints)
		}
		if len(hints) > longest {
			longest = len(hints)
		}
	}
	if longest < 7 {
		t.Fatalf("longest hint row has only %d groups; the Tube row was not exercised", longest)
	}

	// The row is centered, so a width that fits cannot start left of the panel.
	tube := findSceneIndex(config, "Tube")
	if tube < 0 {
		t.Fatal("Tube scene missing")
	}
	plan := planFooterHints(config, footerHints(config, config.Scenes[tube]), avail)
	if start := (screenWidth - plan.width) / 2; start < 0 {
		t.Fatalf("Tube hint row starts at x=%d", start)
	}
}

// A row that cannot fit at any type size still keeps the navigation hints.
func TestFooterPlanDropsTrailingHintsLast(t *testing.T) {
	newKeyboardTestRenderer(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	withScreen(t, uiCanvasCompactW, uiCanvasCompactH)
	t.Cleanup(ClearTextCache)

	hints := []FooterHint{
		{Button: "D-Pad", Label: "Navigate"},
		{Button: "A", Label: "Open"},
		{Button: "B", Label: "Back"},
		{Button: "R2", Label: "More"},
	}
	plan := planFooterHints(config, hints, 200)
	if len(plan.hints) == 0 || len(plan.hints) >= len(hints) {
		t.Fatalf("narrow row kept %d of %d hints", len(plan.hints), len(hints))
	}
	if plan.hints[0].Button != "D-Pad" {
		t.Fatalf("the first hint was dropped: %v", plan.hints)
	}
	if plan.width > 200 {
		t.Fatalf("planned row is %dpx wide, avail 200", plan.width)
	}
}

// The narrow home cards stack their title above the subtitle. That text column
// is only ~90px wide on the brick canvas, so the title has to step down a type
// tier rather than ellipsize the word ("Favo…").
func TestHomeStackedTitlesKeepWholeWordsOnCompactCanvas(t *testing.T) {
	newKeyboardTestRenderer(t)
	withScreen(t, uiCanvasCompactW, uiCanvasCompactH)

	// The home fonts are opened once per process at the canvas' type scale, so
	// reopen them for the compact viewport this test renders.
	resetHomeFonts()
	t.Cleanup(func() {
		resetHomeFonts()
		ClearTextCache()
		clearHomeTextCache()
	})

	fonts := loadHomeFonts()
	if fonts.Card == nil || fonts.Helper == nil {
		t.Fatal("home fonts were not opened")
	}

	layout := NewHomeLayout(uiCanvasCompactW, uiCanvasCompactH)
	item := layout.Items[HomeFocusFavorites]
	if item.Rect.W <= 0 {
		t.Fatalf("Favorites card rect = %+v", item.Rect)
	}
	// Same padding rule the renderer uses for a narrow stacked card.
	pad := scaleX(8, layout.Width)
	textW := item.Rect.W - pad*2

	font, title := fitHomeTitle(fonts, item.Title, textW)
	if font == nil {
		t.Fatal("no font picked for the Favorites title")
	}
	if strings.ContainsRune(title, '…') {
		t.Fatalf("Favorites title was truncated to %q (text width %d)", title, textW)
	}
	if w, _, _ := font.SizeUTF8(title); int32(w) > textW {
		t.Fatalf("%q is %dpx wide, only %dpx fit", title, w, textW)
	}

	// A title that cannot fit at any tier is still ellipsized.
	if _, got := fitHomeTitle(fonts, "An implausibly long card title", textW); !strings.ContainsRune(got, '…') {
		t.Fatalf("an overlong title was not ellipsized: %q", got)
	}
}

// The About screen must credit the hardware it is running on, not the Smart Pro
// on every device.
func TestAboutCreditsDetectedDevice(t *testing.T) {
	cases := map[DeviceModel]string{
		DeviceTrimuiBrick:     "Trimui Brick",
		DeviceTrimuiBrickPro:  "Trimui Brick Pro",
		DeviceTrimuiHammer:    "Trimui Hammer",
		DeviceTrimuiSmartProS: "Trimui Smart Pro",
		DeviceDevBuild:        "Trimui Smart Pro",
	}
	for device, want := range cases {
		if got := deviceCreditName(device); got != want {
			t.Errorf("deviceCreditName(%s) = %q, want %q", device, got, want)
		}
	}
}

// The IPStream button is injected while the config loads, before the window
// exists. Clamping it against the global screenWidth (still 0) parked it off the
// left edge; it has to use the config's own canvas instead.
func TestInjectedIPStreamButtonStaysOnCanvas(t *testing.T) {
	prevW := screenWidth
	screenWidth = 0
	t.Cleanup(func() { screenWidth = prevW })

	config := &Config{
		Width:  1280,
		Height: 720,
		Scenes: []SceneConfig{{Name: "Tube", Elements: []Element{
			{Type: "button", Text: "Shorts", X: 960, Y: 72, Width: "116", Height: "48", Trigger: "shorts_open"},
		}}},
	}
	config.Variables.ScreenWidth = 1280
	ensureIPStreamEntries(config)

	var injected *Element
	for i := range config.Scenes[0].Elements {
		if config.Scenes[0].Elements[i].Trigger == "ip_open" {
			injected = &config.Scenes[0].Elements[i]
		}
	}
	if injected == nil {
		t.Fatal("the IPStream button was not injected")
	}
	if injected.X < 0 {
		t.Fatalf("IPStream button X=%d is off the left edge", injected.X)
	}
	if right := injected.X + getElementWidth(*injected, 116); right > 1280-16 {
		t.Fatalf("IPStream button ends at %d, past the 1280 canvas margin", right)
	}
	if injected.BgColor != "$buttonColor" || injected.Color != "$labelColor" {
		t.Fatalf("injected button colors = %q/%q, want the shared theme variables", injected.BgColor, injected.Color)
	}
}
