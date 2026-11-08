package main

// Tests for Keyboard settings (vksettings.go): the scene exists on every
// config, the language list adds, removes and reorders through the user
// config, the switches write their settings, and the page draws headless.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func TestVKSettingsSceneIsAddedOnce(t *testing.T) {
	cfg := &Config{Scenes: []SceneConfig{{Name: "Main"}}}
	vkEnsureSettingsScene(cfg)
	vkEnsureSettingsScene(cfg)
	n := 0
	for _, s := range cfg.Scenes {
		if s.Name == vkSettingsSceneName {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("keyboard settings scene added %d times", n)
	}
	if _, ok := lookupScene(vkSettingsSceneName); !ok {
		t.Fatal("the scene has no registered module")
	}
	if sceneDisplayName(vkSettingsSceneName) != "Keyboard" {
		t.Fatalf("header names the scene %q", sceneDisplayName(vkSettingsSceneName))
	}
}

// withSettingsRows resets the screen's state around a fresh config.
func withSettingsRows(t *testing.T) *Config {
	t.Helper()
	cfg := withKeyboardConfig(t)
	withLearnedStore(t)
	prevFocus, prevSheet := vkSet.focus, vkSet.sheet
	t.Cleanup(func() {
		vkSet.focus, vkSet.sheet = prevFocus, prevSheet
		vkSet.rows = nil
	})
	vkSet.focus, vkSet.sheet = -1, vkSetSheetNone
	vkSetRefresh()
	return cfg
}

func TestVKSettingsLanguagesAddRemoveAndReorder(t *testing.T) {
	cfg := withSettingsRows(t)

	// Add Russian from the picker.
	vkSetActivate(cfg, vkSetRowIndex(vkSetAddLanguage))
	if vkSet.sheet != vkSetSheetAdd {
		t.Fatal("Add a language did not open the picker")
	}
	pick := -1
	for i, l := range vkSet.sheetItems {
		if l.ID == "ru" {
			pick = i
		}
	}
	if pick < 0 {
		t.Fatal("Russian is not offered")
	}
	vkSetPick(cfg, pick)
	if got := vkFormatLayoutIDs(vkEnabledLayouts()); got != "en-US,ru" {
		t.Fatalf("enabled after adding: %q", got)
	}

	// Move it to the top, make it current, then remove English.
	ru := -1
	for i, r := range vkSet.rows {
		if r.layout != nil && r.layout.ID == "ru" {
			ru = i
		}
	}
	if !vkSetMoveLanguage(cfg, ru, -1) {
		t.Fatal("could not move Russian up")
	}
	if got := vkFormatLayoutIDs(vkEnabledLayouts()); got != "ru,en-US" {
		t.Fatalf("enabled after moving: %q", got)
	}
	vkSetActivate(cfg, vkSet.focus)
	if vkCurrentLayout().ID != "ru" {
		t.Fatalf("A on a language did not make it current: %s", vkCurrentLayout().ID)
	}
	en := -1
	for i, r := range vkSet.rows {
		if r.layout != nil && r.layout.ID == "en-US" {
			en = i
		}
	}
	if !vkSetRemoveLanguage(cfg, en) {
		t.Fatal("could not remove English")
	}
	if got := vkFormatLayoutIDs(vkEnabledLayouts()); got != "ru" {
		t.Fatalf("enabled after removing: %q", got)
	}
	// The last language stays.
	if vkSetRemoveLanguage(cfg, vkSetRowIndex(vkSetLanguage)) {
		t.Fatal("the last language was removed")
	}
}

func TestVKSettingsSwitchesWriteTheConfig(t *testing.T) {
	cfg := withSettingsRows(t)
	row := -1
	for i, r := range vkSet.rows {
		if r.kind == vkSetToggle && r.key == vkKeySuggestions {
			row = i
		}
	}
	if row < 0 || !vkSet.rows[row].on {
		t.Fatal("word suggestions should start on")
	}
	vkSetActivate(cfg, row)
	if vkSuggestionsEnabled() {
		t.Fatal("toggling did not turn suggestions off")
	}
	if v, _ := cfg.Variables.Custom[vkKeySuggestions].(string); v != "false" {
		t.Fatalf("stored %q", v)
	}
	vkSetActivate(cfg, vkSetRowIndex(vkSetVoiceLang))
	if vkVoiceFollowsLayout() {
		t.Fatal("speech language did not switch to auto-detect")
	}

	// Forgetting learned words takes two presses. (No path: the save it
	// schedules must not race the temp dir's removal.)
	vkLearnedPath = func() string { return "" }
	vkLearnWord("Zorblax", "de", 2)
	clear := vkSetRowIndex(vkSetClearWords)
	vkSetActivate(cfg, clear)
	if vkLearnedCount() == 0 {
		t.Fatal("one press already forgot the words")
	}
	vkSetActivate(cfg, clear)
	if vkLearnedCount() != 0 {
		t.Fatal("the second press did not forget the words")
	}

	// The Groq key typed on the keyboard lands in the setting Settings uses.
	vkSaveGroqKey(cfg, "  gsk_live_1234  ")
	if v, _ := cfg.Variables.Custom["groq_api_key"].(string); v != "gsk_live_1234" {
		t.Fatalf("stored key %q", v)
	}
	if hint := vkGroqKeyHint(); hint != "••••1234" {
		t.Fatalf("key hint %q", hint)
	}
	vkSaveGroqKey(cfg, "   ")
	if v, _ := cfg.Variables.Custom["groq_api_key"].(string); v != "gsk_live_1234" {
		t.Fatal("an empty submit erased the key")
	}
}

func TestVKSettingsPageRenders(t *testing.T) {
	f := newProbeFrame(t)
	withScreen(t, probeW, probeH)
	cfg := withSettingsRows(t)
	cfg.Scenes = []SceneConfig{{Name: vkSettingsSceneName}}
	prevScene := currentSceneIndex
	currentSceneIndex = 0
	t.Cleanup(func() { currentSceneIndex = prevScene })

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)

	for _, sheet := range []vkSetSheet{vkSetSheetNone, vkSetSheetAdd, vkSetSheetLicence} {
		switch sheet {
		case vkSetSheetAdd:
			vkSetOpenPicker()
		case vkSetSheetLicence:
			vkSetOpenLicence()
		default:
			vkSet.sheet = vkSetSheetNone
		}
		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.Clear()
		renderKeyboardSettings(renderer, cfg)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
			t.Fatalf("ReadPixels failed: %v", err)
		}
		if len(vkSet.rowRects) != len(vkSet.rows) {
			t.Fatalf("sheet %d: %d row rects for %d rows", sheet, len(vkSet.rowRects), len(vkSet.rows))
		}
		// The focused row is inside the visible list.
		if fr := vkSet.rowRects[vkSet.focus]; fr.Y < vkSet.view.Y || fr.Y+fr.H > vkSet.view.Y+vkSet.view.H {
			t.Fatalf("focused row %v is outside the list %v", fr, vkSet.view)
		}
	}
	if len(vkSet.licence) < 10 {
		t.Fatal("the licence sheet has no text")
	}
}
