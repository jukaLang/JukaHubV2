package main

// Footer hints and the X-search shortcut are advertised controls, so they are
// tested like buttons: the label a scene shows has to describe behaviour the
// input handlers really have.

import "testing"

// hintLabel returns the label advertised for a button, and whether the button
// is listed at all.
func hintLabel(hints []FooterHint, button string) (string, bool) {
	for _, h := range hints {
		if h.Button == button {
			return h.Label, true
		}
	}
	return "", false
}

// TestFooterHintsMatchTheHandlers checks the labels the shared footer draws on
// every scene. "A Send" in Chat was wrong (A opens the keyboard), and Tube's
// "X Search" pointed at a key that did nothing on Tube itself.
func TestFooterHintsMatchTheHandlers(t *testing.T) {
	withScreen(t, probeW, probeH)
	for name, config := range loadAuditConfigs(t) {
		for _, scene := range config.Scenes {
			if scene.Layout == "home" {
				continue // Home draws its own footer inside HomeLayout
			}
			hints := footerHints(config, scene)
			if label, ok := hintLabel(hints, "B"); !ok || label == "" {
				t.Errorf("%s / %s: the footer offers no way back", name, scene.Name)
			}
			switch scene.Name {
			case "Chat":
				if label, _ := hintLabel(hints, "A"); label != "Type" {
					t.Errorf("%s / %s: A hint is %q, but A opens the keyboard", name, scene.Name, label)
				}
				if label, ok := hintLabel(hints, "Y"); !ok || label != "Ask AI" {
					t.Errorf("%s / %s: Y hint is %q, want \"Ask AI\"", name, scene.Name, label)
				}
				if label, ok := hintLabel(hints, "X"); !ok || label != "Rename" {
					t.Errorf("%s / %s: X hint is %q, want \"Rename\"", name, scene.Name, label)
				}
			case "Tube":
				if label, ok := hintLabel(hints, "X"); !ok || label != "Search" {
					t.Errorf("%s / %s: X hint is %q, want \"Search\"", name, scene.Name, label)
				}
			}
		}
	}
}

// TestSearchShortcutReachesTheTubeField checks both halves of the X shortcut:
// it navigates from another scene, and on Tube it opens the keyboard for the
// search field instead of doing nothing.
func TestSearchShortcutReachesTheTubeField(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, probeW, probeH)
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	config.Scenes = []SceneConfig{
		{Name: "Main", Layout: "home"},
		{Name: "Tube", Elements: []Element{
			{Type: "input", Variable: "search_query", X: 24, Y: 72, Width: "700", Height: "48"},
			{Type: "button", Text: "Search", Trigger: "youtube_search", X: 744, Y: 72},
		}},
	}
	prevScene, prevPhase, prevPending := currentSceneIndex, transitionPhase, pendingSceneIndex
	prevActive, prevSceneIdx, prevElemIdx := virtualKeyboardActive, activeSceneIndex, activeElementIndex
	t.Cleanup(func() {
		currentSceneIndex, transitionPhase, pendingSceneIndex = prevScene, prevPhase, prevPending
		virtualKeyboardActive = prevActive
		activeSceneIndex, activeElementIndex = prevSceneIdx, prevElemIdx
		resetKeyboardState(t)
	})
	virtualKeyboardActive = false
	activeSceneIndex, activeElementIndex = -1, -1

	currentSceneIndex = 0
	if !openSearchShortcut(config) {
		t.Fatal("the X shortcut did nothing from Home")
	}
	if pendingSceneIndex != 1 || transitionPhase != "fade-out" {
		t.Fatalf("X from Home queued scene %d (phase %q), want a fade to Tube",
			pendingSceneIndex, transitionPhase)
	}
	if virtualKeyboardActive {
		t.Fatal("X should not open the keyboard before Tube is on screen")
	}
	// The main loop finishes the fade; the test does the same step by hand.
	currentSceneIndex, transitionPhase = pendingSceneIndex, "none"

	if !openSearchShortcut(config) {
		t.Fatal("the X shortcut did nothing on Tube")
	}
	if !virtualKeyboardActive {
		t.Fatal("X on Tube must open the search keyboard")
	}
	if vkTarget != vkTargetScene || activeSceneIndex != 1 || activeElementIndex != 0 {
		t.Fatalf("keyboard target = %v scene=%d element=%d, want the Tube search field",
			vkTarget, activeSceneIndex, activeElementIndex)
	}
}
