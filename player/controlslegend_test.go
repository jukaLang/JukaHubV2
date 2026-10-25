package main

// Controls for the controller legend (controlslegend.go). The card's content
// is generated from the footerHints registry, so the tests pin the contract
// from both ends: the rows must equal what the scene's own footer would draw,
// and the entry points (Quick Settings "?", the first-run tour) must open it.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// isolateLegend saves and restores the legend modal and the app config so a
// test cannot leak its state into the package's other probes.
func isolateLegend(t *testing.T) {
	t.Helper()
	prevLegend := controlsLegend
	prevConfig := appConfig
	prevShortcuts := shortcutsOpen
	t.Cleanup(func() {
		controlsLegend = prevLegend
		appConfig = prevConfig
		shortcutsOpen = prevShortcuts
	})
	controlsLegend = controlsLegendState{}
	shortcutsOpen = false
}

// TestControlsLegendMirrorsSceneHints pins the content contract: section one
// is exactly the scene's own footer explanation (the same registry the
// auditable footers render), and the global rows name the real owners — the
// Start menu, the Select+Start quit chord and the keyboard reference.
func TestControlsLegendMirrorsSceneHints(t *testing.T) {
	isolateLegend(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	appConfig = config

	tube := findSceneIndex(config, "Tube")
	if tube < 0 {
		t.Fatal("the config has no Tube (media) scene")
	}
	controlsLegend.scene = config.Scenes[tube]

	sections := controlsLegendSections(config)
	if len(sections) != 2 {
		t.Fatalf("legend has %d sections, want 2 (this screen + every screen)", len(sections))
	}
	want := footerHints(config, config.Scenes[tube])
	if len(sections[0].Rows) != len(want) {
		t.Fatalf("scene section has %d rows, want the footer's %d", len(sections[0].Rows), len(want))
	}
	for i, h := range want {
		if sections[0].Rows[i] != h {
			t.Errorf("row %d = %+v, want the footer's %+v", i, sections[0].Rows[i], h)
		}
	}
	if label, ok := hintLabel(sections[1].Rows, "Select + Start"); !ok || label != "Quit JukaHub" {
		t.Errorf("global Select+Start row = %q (found %v), want \"Quit JukaHub\"", label, ok)
	}
	if label, ok := hintLabel(sections[1].Rows, "Start"); !ok || label != "Start menu" {
		t.Errorf("global Start row = %q (found %v), want \"Start menu\"", label, ok)
	}
	if label, ok := hintLabel(sections[1].Rows, "F1"); !ok || label != "Keyboard shortcuts" {
		t.Errorf("global F1 row = %q (found %v), want \"Keyboard shortcuts\"", label, ok)
	}
}

// TestQuickSettingsQuestionMarkOpensLegend drives the "?" link row the way
// the panel does: A on the row closes Quick Settings and puts the legend up,
// never stacked on top of it.
func TestQuickSettingsQuestionMarkOpensLegend(t *testing.T) {
	isolateLegend(t)
	withScreen(t, probeW, probeH)
	newProbeFrame(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	appConfig = config
	currentSceneIndex = findSceneIndex(config, "Tube")
	if currentSceneIndex < 0 {
		t.Fatal("the config has no Tube (media) scene")
	}
	t.Cleanup(func() { currentSceneIndex = -1 })

	qsInit()
	t.Cleanup(func() { qs = qsState{} })

	// The row exists and is always offerable: it needs no backend.
	linkIdx := -1
	for i, l := range qs.links {
		if l.Action == qsLinkControls {
			linkIdx = i
		}
	}
	if linkIdx < 0 {
		t.Fatal("Quick Settings has no Controller Legend link row")
	}
	qs.cursor = len(qs.toggles) + len(qs.sliders) + linkIdx
	qs.open = true

	qsSelect()
	if controlsLegend.open != true {
		t.Fatal("A on the Controller Legend row did not open the legend")
	}
	if qs.open {
		t.Fatal("the legend opened on top of Quick Settings instead of replacing it")
	}
	// And the legend content matches the scene that was live at open time.
	sections := controlsLegendSections(appConfig)
	if sections[0].Title != "Media" {
		t.Fatalf("legend section title = %q, want the media scene's display name", sections[0].Title)
	}
	want := footerHints(appConfig, appConfig.Scenes[currentSceneIndex])
	for i, h := range want {
		if sections[0].Rows[i] != h {
			t.Errorf("row %d = %+v, want the live scene's footer row %+v", i, sections[0].Rows[i], h)
		}
	}
}

// TestCompletedTourRevealsControls: the legend is the first-run payoff — it
// appears once when the tour is finished (not skipped) and stays closed when
// the user skips it.
func TestCompletedTourRevealsControls(t *testing.T) {
	isolateLegend(t)
	withScreen(t, probeW, probeH)
	newProbeFrame(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	appConfig = config
	currentSceneIndex = findSceneIndex(config, "Tube")
	if currentSceneIndex < 0 {
		t.Fatal("the config has no Tube (media) scene")
	}
	t.Cleanup(func() { currentSceneIndex = -1 })

	onboarding.active = true
	onboardingFinish(config, true)
	if onboarding.active {
		t.Fatal("finishing the tour left it active")
	}
	if controlsLegend.open {
		t.Fatal("a skipped tour must not open the legend")
	}

	// A completed tour opens it on the scene the user is standing on.
	onboarding.active = true
	onboardingFinish(config, false)
	if !controlsLegend.open {
		t.Fatal("completing the tour did not open the controller legend")
	}
	if controlsLegend.scene.Name != "Tube" {
		t.Fatalf("legend captured scene %q, want the live Tube scene", controlsLegend.scene.Name)
	}
}

// TestLegendKeyClosesAndHandsOverToF1: B (and Escape) close the legend; F1
// closes it and opens the full keyboard shortcuts overlay, which owns the
// next key.
func TestLegendKeyClosesAndHandsOverToF1(t *testing.T) {
	isolateLegend(t)
	controlsLegend.open = true

	key := func(sym sdl.Keycode) *sdl.KeyboardEvent {
		return &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}}
	}
	if !handleControlsLegendKey(key(sdl.K_b)) {
		t.Fatal("the legend did not consume B")
	}
	if controlsLegend.open {
		t.Fatal("B did not close the legend")
	}

	controlsLegend.open = true
	handleControlsLegendKey(key(sdl.K_ESCAPE))
	if controlsLegend.open {
		t.Fatal("Escape did not close the legend")
	}

	controlsLegend.open = true
	handleControlsLegendKey(key(sdl.K_F1))
	if controlsLegend.open {
		t.Fatal("F1 did not hand over from the legend")
	}
	if !shortcutsOpen {
		t.Fatal("F1 did not open the keyboard shortcuts overlay")
	}

	// Closed legend does not consume keys: the chain behind it must run.
	if handleControlsLegendKey(key(sdl.K_a)) {
		t.Fatal("the legend consumed a key while closed")
	}
}

// TestControlsLegendRendersLikeQuickSettings is an offscreen smoke check on
// real pixels: the card draws without a panic and fits its measured height.
func TestControlsLegendRendersLikeQuickSettings(t *testing.T) {
	withScreen(t, probeW, probeH)
	newProbeFrame(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	controlsLegend.scene = config.Scenes[0]

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	defer renderer.Clear()
	renderer.SetDrawColor(9, 14, 25, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: probeW, H: probeH})

	sections := controlsLegendSections(config)
	bindings := 0
	for _, s := range sections {
		bindings += len(s.Rows)
	}
	if bindings < 6 {
		t.Fatalf("the card would show %d rows, want a useful reference", bindings)
	}

	renderControlsLegend(renderer, config)
}
