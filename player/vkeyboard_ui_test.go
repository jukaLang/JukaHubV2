package main

// Interaction and legibility checks for the on-screen keyboard: touch taps,
// dismiss paths, the slide-in animation, key cap labels and the focus colour.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// sceneInputConfig returns a config with one text input so the scene-input
// keyboard target can be exercised without loading a real config file.
func sceneInputConfig() *Config {
	cfg := &Config{}
	cfg.Variables.Custom = map[string]interface{}{"stream_url": "saved"}
	cfg.Scenes = []SceneConfig{{
		Name: "TestInput",
		Elements: []Element{
			{Type: "input", Variable: "stream_url", X: 40, Y: 80, Width: "400", Height: "48"},
			{Type: "button", Text: "Search", X: 40, Y: 160},
		},
	}}
	return cfg
}

// touchAt builds the normalized touch event SDL would send for a logical point.
func touchAt(x, y int32) *sdl.TouchFingerEvent {
	return &sdl.TouchFingerEvent{
		Type: sdl.FINGERDOWN,
		X:    float32(x) / float32(screenWidth),
		Y:    float32(y) / float32(screenHeight),
	}
}

func TestKeyboardTouchTapsKeys(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	vkTarget = vkTargetScene
	activeSceneIndex, activeElementIndex = -1, -1
	t.Cleanup(func() { activeSceneIndex, activeElementIndex = -1, -1 })
	vkEnsureLayout()

	row, col := rowIndex(keyboard, "q")
	rect := keyRectFor(row, col)
	if rect == nil {
		t.Fatal("q key missing")
	}
	vkHandleTouchEvent(touchAt(rect.X+rect.W/2, rect.Y+rect.H/2), cfg)
	if inputTextBuffer != "q" {
		t.Fatalf("tap on q produced %q", inputTextBuffer)
	}

	// A tap outside the panel dismisses the keyboard.
	vkHandleTouchEvent(touchAt(8, 8), cfg)
	if virtualKeyboardActive {
		t.Fatal("tap outside the panel should close the keyboard")
	}
}

func TestKeyboardTouchCloseButton(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	vkTarget = vkTargetScene
	activeSceneIndex, activeElementIndex = -1, -1
	t.Cleanup(func() { activeSceneIndex, activeElementIndex = -1, -1 })
	vkEnsureLayout()

	vkHandleTouchEvent(touchAt(vkCloseRect.X+vkCloseRect.W/2, vkCloseRect.Y+vkCloseRect.H/2), cfg)
	if virtualKeyboardActive {
		t.Fatal("tapping the close button should close the keyboard")
	}
}

// The panel slides up over 120ms; taps during that window must still hit the
// key that is drawn under the finger.
func TestKeyboardHitTestFollowsSlideIn(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	vkTarget = vkTargetScene
	vkEnsureLayout()

	if off := vkAnimOffset(); off != 0 {
		t.Fatalf("closed keyboard should have no offset, got %d", off)
	}
	vkAnimStart = sdl.GetTicks64()
	off := vkAnimOffset()
	if off <= 0 || off > 26 {
		t.Fatalf("unexpected slide-in offset %d", off)
	}

	row, col := rowIndex(keyboard, "g")
	rect := keyRectFor(row, col)
	gotRow, gotCol, ok := vkHitTest(rect.X+rect.W/2, rect.Y+rect.H/2+off)
	if !ok || gotRow != row || gotCol != col {
		t.Fatalf("tap at drawn position hit (%d,%d) ok=%v, want (%d,%d)", gotRow, gotCol, ok, row, col)
	}
	if vkCloseHit(vkCloseRect.X+vkCloseRect.W/2, vkCloseRect.Y+vkCloseRect.H/2) {
		t.Fatal("close button should move with the panel too")
	}

	vkAnimStart = 0
}

func TestKeyboardCloseButtonIsTapSized(t *testing.T) {
	for _, size := range [][2]int32{{1280, 720}, {800, 480}} {
		t.Run("", func(t *testing.T) {
			resetKeyboardState(t)
			withScreen(t, size[0], size[1])
			vkTarget = vkTargetScene
			vkEnsureLayout()

			want := int32(38)
			if size[0] < 900 {
				want = 30
			}
			if vkCloseRect.W < want || vkCloseRect.H < want {
				t.Fatalf("close button %v smaller than %d on %dx%d", vkCloseRect, want, size[0], size[1])
			}
			if vkCloseRect.X+vkCloseRect.W > vkPanelRect.X+vkPanelRect.W {
				t.Fatalf("close button %v escapes the panel %v", vkCloseRect, vkPanelRect)
			}
			// It must not overlap the text field either.
			if vkCloseRect.Y >= vkFieldRect.Y && vkCloseRect.Y < vkFieldRect.Y+vkFieldRect.H {
				t.Fatalf("close button %v overlaps the field %v", vkCloseRect, vkFieldRect)
			}
		})
	}
}

// Every cap label must fit its key at the smallest supported screen.
func TestKeyboardLabelsFitKeys(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, 800, 480)
	vkTarget = vkTargetChat
	chatInputActive = true
	vkEnsureLayout()

	cfg := &Config{}
	for i, key := range keyboardKeys {
		label := vkKeyLabel(key)
		r := keyboardRects[i]
		font, fitted := vkLabelFont(cfg, label, r.W-14)
		if font == nil || fitted == "" {
			t.Fatalf("key %q has no renderable label", key)
		}
		w, _, _ := font.SizeUTF8(fitted)
		if int32(w) > r.W-8 {
			t.Fatalf("label %q (%dpx) overflows key %v", fitted, w, r)
		}
	}
}

// A dark theme must not turn the focused key into a plain white block.
func TestKeyboardFocusColorStaysOnTheme(t *testing.T) {
	prevAccent := accentColor
	t.Cleanup(func() { accentColor = prevAccent })
	accentColor = sdl.Color{R: 30, G: 41, B: 59, A: 255}

	got := vkFocusColor()
	lum := func(c sdl.Color) int { return int(colorLuma(c)) }
	if vkAbsDiff(lum(got), lum(ColorPanelRaised)) < 45 {
		t.Fatalf("focus colour %v is too close to the panel", got)
	}
	if got.R == 255 && got.G == 255 && got.B == 255 {
		t.Fatal("focus colour fell back to pure white")
	}
}

// Closing the keyboard must end the edit cleanly: no lingering modal state and
// the typed value committed to the config variable.
func TestKeyboardCloseResetsSceneEditing(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	cfg := sceneInputConfig()

	vkTarget = vkTargetScene
	activeSceneIndex, activeElementIndex = 0, 0
	virtualKeyboardActive = true
	inputTextBuffer = "https://example.com/stream"
	vkSetText(inputTextBuffer, cfg)
	t.Cleanup(func() { activeSceneIndex, activeElementIndex = -1, -1 })

	vkCloseKeyboard()

	if virtualKeyboardActive {
		t.Fatal("keyboard still active after close")
	}
	if activeSceneIndex != -1 || activeElementIndex != -1 {
		t.Fatalf("scene edit state leaked: %d/%d", activeSceneIndex, activeElementIndex)
	}
	if got, _ := cfg.Variables.Custom["stream_url"].(string); got != "https://example.com/stream" {
		t.Fatalf("typed value not committed: %q", got)
	}
}
