package main

// Tests for the on-screen keyboard and the Groq assistant request builder.
// The render checks reuse the offscreen SDL probe from
// installbar_render_test.go so they verify real pixels, not just geometry.

import (
	"encoding/json"
	"strings"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func withScreen(t *testing.T, w, h int32) {
	t.Helper()
	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = w, h
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })
}

func resetKeyboardState(t *testing.T) {
	t.Helper()
	prevTarget, prevLayer := vkTarget, vkLayer
	prevUpper := keyboardUpper
	prevFocusX, prevFocusY := keyboardPosX, keyboardPosY
	prevInput := inputTextBuffer
	prevChat, prevChatActive := chatInputText, chatInputActive
	prevUnit, prevUnitActive := unitInputValue, unitInputActive
	prevActive := virtualKeyboardActive
	prevAnim := vkAnimStart
	t.Cleanup(func() {
		vkTarget, vkLayer = prevTarget, prevLayer
		keyboardUpper = prevUpper
		keyboardPosX, keyboardPosY = prevFocusX, prevFocusY
		inputTextBuffer = prevInput
		chatInputText, chatInputActive = prevChat, prevChatActive
		unitInputValue, unitInputActive = prevUnit, prevUnitActive
		virtualKeyboardActive = prevActive
		vkAnimStart = prevAnim
		vkLayoutSig = ""
	})
	vkLayoutSig = ""
	keyboardUpper = false
	vkLayer = vkLayerLetters
	keyboardPosX, keyboardPosY = 0, 0
	inputTextBuffer = ""
	chatInputText = ""
	unitInputValue = ""
	unitInputActive = false
	virtualKeyboardActive = true
	vkAnimStart = 0
}

func rowIndex(rows [][]string, label string) (int, int) {
	for r, row := range rows {
		for c, key := range row {
			if key == label {
				return r, c
			}
		}
	}
	return -1, -1
}

func TestKeyboardLayoutCoversTypingNeeds(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	chatInputActive = true
	vkTarget = vkTargetChat
	rows := vkRows()

	if len(rows) != 5 {
		t.Fatalf("expected 5 rows, got %d", len(rows))
	}
	// Digits row, so queries like "top 10" and ports/addresses are typeable.
	digits := rows[0]
	if len(digits) != 10 || digits[0] != "1" || digits[9] != "0" {
		t.Fatalf("digits row wrong: %v", digits)
	}
	for _, want := range []string{"q", "p", "a", "l", "⇧", "z", "m", "BACK", "123", ",", ".", "SPACE", "SEND", "ASK_AI"} {
		if r, _ := rowIndex(rows, want); r < 0 {
			t.Errorf("letters layer missing key %q", want)
		}
	}
	if r, _ := rowIndex(rows, "ENTER"); r >= 0 {
		t.Error("chat keyboard should show Send, not Done")
	}

	// Scene inputs use Done and have no Ask AI key.
	chatInputActive = false
	vkTarget = vkTargetScene
	rows = vkRows()
	if r, _ := rowIndex(rows, "ENTER"); r < 0 {
		t.Error("scene keyboard missing Done key")
	}
	if r, _ := rowIndex(rows, "ASK_AI"); r >= 0 {
		t.Error("scene keyboard should not show Ask AI")
	}

	// Symbols layer provides punctuation that used to require Shift tricks.
	vkLayer = vkLayerSymbols
	rows = vkRows()
	for _, want := range []string{"@", "#", "/", "?", ":", "'", "\"", "ABC", "BACK"} {
		if r, _ := rowIndex(rows, want); r < 0 {
			t.Errorf("symbols layer missing key %q", want)
		}
	}
}

func TestKeyboardShiftKeepsFocusAndUppercases(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	vkTarget = vkTargetScene
	vkEnsureLayout()
	keyboardPosY, keyboardPosX = 1, 4
	if got := vkFocusedKey(); got != "t" {
		t.Fatalf("expected focus on t, got %q", got)
	}
	toggleKeyboardCase()
	if got := vkFocusedKey(); got != "T" {
		t.Fatalf("shift should uppercase the focused letter, got %q", got)
	}
	if keyboardPosY != 1 || keyboardPosX != 4 {
		t.Fatalf("focus moved during shift: (%d,%d)", keyboardPosY, keyboardPosX)
	}
	if value, ok := vkKeyValue("T"); !ok || value != "T" {
		t.Fatalf("uppercase key should insert T, got %q ok=%v", value, ok)
	}
}

func TestKeyboardGeometryStaysInsidePanel(t *testing.T) {
	for _, size := range [][2]int32{{1280, 720}, {1024, 600}, {800, 480}} {
		t.Run("", func(t *testing.T) {
			resetKeyboardState(t)
			withScreen(t, size[0], size[1])
			vkTarget = vkTargetChat
			chatInputActive = true
			vkEnsureLayout()

			p := vkPanelRect
			if p.W <= 0 || p.H <= 0 {
				t.Fatal("panel not laid out")
			}
			if p.X < 0 || p.X+p.W > size[0] {
				t.Fatalf("panel %v outside screen width %d", p, size[0])
			}
			if p.Y+p.H > size[1] || p.Y < 0 {
				t.Fatalf("panel %v outside screen height %d", p, size[1])
			}
			if size[1]-(p.Y+p.H) > 40 {
				t.Fatalf("panel should hug the bottom edge, gap=%d", size[1]-(p.Y+p.H))
			}
			if len(keyboardRects) != len(keyboardKeys) {
				t.Fatalf("rect/label mismatch: %d vs %d", len(keyboardRects), len(keyboardKeys))
			}
			for i, r := range keyboardRects {
				if r.W < 30 || r.H < 30 {
					t.Fatalf("key %q too small: %v", keyboardKeys[i], r)
				}
				if r.X < p.X || r.Y < p.Y || r.X+r.W > p.X+p.W || r.Y+r.H > p.Y+p.H {
					t.Fatalf("key %q %v escapes panel %v", keyboardKeys[i], r, p)
				}
				if vkKeyLabel(keyboardKeys[i]) == "" {
					t.Fatalf("key %q has no label", keyboardKeys[i])
				}
				for _, other := range keyboardRects[i+1:] {
					if rectsOverlap(r, other) {
						t.Fatalf("keys overlap: %v and %v", r, other)
					}
				}
			}
			// The text field must sit above the keys and inside the panel.
			if vkFieldRect.Y < p.Y || vkFieldRect.Y+vkFieldRect.H > keyboardRects[0].Y {
				t.Fatalf("field %v does not sit above the keys", vkFieldRect)
			}
			if !vkCloseHit(vkCloseRect.X+vkCloseRect.W/2, vkCloseRect.Y+vkCloseRect.H/2) {
				t.Fatal("close button not hittable")
			}
		})
	}
}

func rectsOverlap(a, b sdl.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

func TestKeyboardHitTestMapsEveryKey(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	vkTarget = vkTargetScene
	vkEnsureLayout()
	for i, r := range keyboardRects {
		row, col, ok := vkHitTest(r.X+r.W/2, r.Y+r.H/2)
		if !ok {
			t.Fatalf("no hit for key %q", keyboardKeys[i])
		}
		wantRow, wantCol := keyboardRowCol[i][0], keyboardRowCol[i][1]
		if row != wantRow || col != wantCol {
			t.Fatalf("key %q hit (%d,%d), want (%d,%d)", keyboardKeys[i], row, col, wantRow, wantCol)
		}
	}
	if _, _, ok := vkHitTest(1, 1); ok {
		t.Fatal("point outside the keyboard should not hit a key")
	}
}

func TestKeyboardTypingRoutesToExactlyOneTarget(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	cfg := &Config{}
	prevScene := activeSceneIndex
	prevElem := activeElementIndex
	activeSceneIndex, activeElementIndex = -1, -1
	t.Cleanup(func() { activeSceneIndex, activeElementIndex = prevScene, prevElem })

	vkTarget = vkTargetScene
	vkInsertText("a", cfg)
	vkInsertText("b", cfg)
	if inputTextBuffer != "ab" {
		t.Fatalf("scene buffer = %q", inputTextBuffer)
	}
	vkBackspace(cfg)
	if inputTextBuffer != "a" {
		t.Fatalf("backspace left %q", inputTextBuffer)
	}
	if chatInputText != "" || unitInputValue != "" {
		t.Fatal("scene typing leaked into chat/unit buffers")
	}

	vkTarget = vkTargetChat
	vkInsertText("hi", cfg)
	if chatInputText != "hi" || inputTextBuffer != "a" {
		t.Fatalf("chat routing wrong: chat=%q scene=%q", chatInputText, inputTextBuffer)
	}

	vkTarget = vkTargetUnit
	vkInsertText("12", cfg)
	if unitInputValue != "12" || chatInputText != "hi" {
		t.Fatalf("unit routing wrong: unit=%q chat=%q", unitInputValue, chatInputText)
	}
	// Backspace is rune aware, not byte aware.
	vkInsertText("é", cfg)
	vkBackspace(cfg)
	if unitInputValue != "12" {
		t.Fatalf("rune backspace failed: %q", unitInputValue)
	}
}

func TestKeyboardActivateFocusedTypesAndSubmits(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	cfg := &Config{}
	prevScene := activeSceneIndex
	prevElem := activeElementIndex
	activeSceneIndex, activeElementIndex = -1, -1
	t.Cleanup(func() { activeSceneIndex, activeElementIndex = prevScene, prevElem })

	vkTarget = vkTargetUnit
	vkEnsureLayout()
	row, col := rowIndex(keyboard, "7")
	keyboardPosY, keyboardPosX = row, col
	vkActivateFocused(cfg)
	if unitInputValue != "7" {
		t.Fatalf("expected 7, got %q", unitInputValue)
	}
	// Space key inserts a space rather than activating.
	row, col = rowIndex(keyboard, "SPACE")
	keyboardPosY, keyboardPosX = row, col
	vkActivateFocused(cfg)
	if unitInputValue != "7 " {
		t.Fatalf("space failed: %q", unitInputValue)
	}
	// Done submits and closes.
	row, col = rowIndex(keyboard, "ENTER")
	keyboardPosY, keyboardPosX = row, col
	vkActivateFocused(cfg)
	if virtualKeyboardActive {
		t.Fatal("Done should close the keyboard")
	}
}

// paddedTextEvent mimics what SDL hands the app: a NUL-terminated string in a
// fixed 32-byte array whose tail is zero padding. Converting the whole array
// to a Go string used to append that padding to the edited field, which made
// the field look stuck after the first key because the renderer stops at the
// first NUL.
func paddedTextEvent(text string) *sdl.TextInputEvent {
	e := &sdl.TextInputEvent{Type: sdl.TEXTINPUT}
	copy(e.Text[:], text)
	return e
}

func TestPhysicalTypingKeepsTextAfterTheFirstKey(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	vkTarget = vkTargetScene
	prevScene, prevElem := activeSceneIndex, activeElementIndex
	activeSceneIndex, activeElementIndex = -1, -1
	t.Cleanup(func() { activeSceneIndex, activeElementIndex = prevScene, prevElem })
	inputTextBuffer = ""
	vkEnsureLayout()

	press := func(sym sdl.Keycode, text string) {
		handleVirtualKeyboardInput(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}}, cfg)
		handleTextInputEvent(paddedTextEvent(text), cfg)
	}
	press(sdl.K_h, "h")
	press(sdl.K_i, "i")
	press(sdl.K_EXCLAIM, "!")

	if inputTextBuffer != "hi!" {
		t.Fatalf("typed %q, want %q", inputTextBuffer, "hi!")
	}
	if strings.IndexByte(inputTextBuffer, 0) >= 0 {
		t.Fatalf("buffer contains NUL padding: %q", inputTextBuffer)
	}

	// Backspace has to remove the visible character, not invisible padding.
	handleVirtualKeyboardInput(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_BACKSPACE}}, cfg)
	if inputTextBuffer != "hi" {
		t.Fatalf("after backspace %q, want %q", inputTextBuffer, "hi")
	}

	// The sanitizer also protects other producers (e.g. the phone companion).
	vkInsertText("a\x00b", cfg)
	if inputTextBuffer != "hiab" {
		t.Fatalf("NUL was not stripped: %q", inputTextBuffer)
	}
}

func TestNotesTypingDropsSDLTextPadding(t *testing.T) {
	prevTab, prevContent := notes.tab, notes.editContent
	t.Cleanup(func() { notes.tab, notes.editContent = prevTab, prevContent })
	notes.tab = 2
	notes.editContent = ""

	handleNotesTextInput(paddedTextEvent("ab"))
	if notes.editContent != "ab" {
		t.Fatalf("notes content %q, want %q", notes.editContent, "ab")
	}
	if strings.IndexByte(notes.editContent, 0) >= 0 {
		t.Fatalf("notes content contains NUL padding: %q", notes.editContent)
	}
}

func TestGroqModelNormalization(t *testing.T) {
	cases := map[string]string{
		"":                        groqDefaultModel,
		"llama-3.3-70b-versatile": groqDefaultModel,
		"llama-3.1-8b-instant":    "openai/gpt-oss-20b",
		"qwen/qwen3-32b":          "qwen/qwen3.8-27b",
		"openai/gpt-oss-120b":     "openai/gpt-oss-120b",
		"custom/model":            "custom/model",
	}
	for input, want := range cases {
		if got := normalizeGroqModel(input); got != want {
			t.Errorf("normalizeGroqModel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGroqCandidatesFallBack(t *testing.T) {
	prev := appConfig
	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{
		"groq_model": "llama-3.3-70b-versatile",
	}}}
	t.Cleanup(func() { appConfig = prev })

	chain := groqCandidates()
	if len(chain) < 2 {
		t.Fatalf("expected fallbacks, got %v", chain)
	}
	if chain[0] != groqDefaultModel {
		t.Fatalf("retired model should resolve first, got %q", chain[0])
	}
	seen := map[string]bool{}
	for _, m := range chain {
		if seen[m] {
			t.Fatalf("duplicate model in chain: %v", chain)
		}
		seen[m] = true
	}
	for _, want := range []string{groqFallbackModel, groqThirdModel} {
		if !seen[want] {
			t.Errorf("fallback %q missing from %v", want, chain)
		}
	}
}

func TestGroqMessagesCarryContext(t *testing.T) {
	prevMessages := chatMessages
	t.Cleanup(func() { chatMessages = prevMessages })
	chatMessages = []ChatMessage{
		{Sender: "System", Text: "Discord: connected"},
		{Sender: "You", Text: "what is jukahub"},
		{Sender: "Juka AI", Text: "A handheld media launcher."},
		{Sender: "someuser", Text: "hello from discord"},
		{Sender: "You", Text: "and music?"},
	}

	messages := buildGroqMessages("and music?")
	if len(messages) < 4 {
		t.Fatalf("expected system + history + prompt, got %v", messages)
	}
	if messages[0]["role"] != "system" {
		t.Fatalf("first message must be the system prompt, got %v", messages[0])
	}
	last := messages[len(messages)-1]
	if last["role"] != "user" || last["content"] != "and music?" {
		t.Fatalf("prompt not appended correctly: %v", last)
	}
	// The trailing prompt must not be duplicated from history.
	users := 0
	for _, m := range messages[1:] {
		if m["role"] == "user" && m["content"] == "and music?" {
			users++
		}
		if m["content"] == "hello from discord" {
			t.Fatal("Discord messages must not be sent as assistant history")
		}
	}
	if users != 1 {
		t.Fatalf("prompt sent %d times", users)
	}
	if messages[2]["role"] != "assistant" || messages[1]["role"] != "user" {
		t.Fatalf("history roles wrong: %v", messages)
	}
}

func TestGroqErrorMessagesAreActionable(t *testing.T) {
	body := func(msg string) []byte {
		b, _ := json.Marshal(map[string]interface{}{"error": map[string]string{"message": msg}})
		return b
	}
	cases := []struct {
		name      string
		status    int
		body      []byte
		wantModel bool
		wantText  string
	}{
		{"auth", 401, body("invalid api key"), false, "API key"},
		{"rate", 429, body("rate limit"), false, "rate limit"},
		{"retired", 400, body("The model `llama-3.3-70b-versatile` has been decommissioned"), true, "model unavailable"},
		{"server", 500, body("oops"), false, "unavailable"},
	}
	for _, tc := range cases {
		err := groqErrorFromResponse(tc.status, tc.body)
		if err.model != tc.wantModel {
			t.Errorf("%s: model flag = %v, want %v", tc.name, err.model, tc.wantModel)
		}
		if !containsFold(err.Error(), tc.wantText) {
			t.Errorf("%s: message %q missing %q", tc.name, err.Error(), tc.wantText)
		}
	}
}

func containsFold(haystack, needle string) bool {
	h := []rune(haystack)
	n := []rune(needle)
	for i := 0; i+len(n) <= len(h); i++ {
		match := true
		for j := range n {
			a, b := h[i+j], n[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// TestKeyboardRendersLikeTheTheme checks the panel, keys and focus fill on real
// pixels: it must use theme surfaces (not the old neon palette) and the
// focused key must be the only accent-filled one.
func TestKeyboardRendersLikeTheTheme(t *testing.T) {
	f := newProbeFrame(t)
	resetKeyboardState(t)
	withScreen(t, probeW, probeH)
	vkTarget = vkTargetChat
	chatInputActive = true
	currentSceneIndex = -1
	chatInputText = "hello world"
	vkEnsureLayout()

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderKeyboard(renderer, &Config{})
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}

	if vkPanelRect.W == 0 {
		t.Fatal("panel was not laid out")
	}
	// Panel body uses the themed panel surface (alpha 250 over the content).
	got := f.at(vkPanelRect.X+8, vkPanelRect.Y+8)
	if !closeColor(got, ColorSurfacePanel, 16) {
		t.Fatalf("panel pixel %v is not the themed surface %v", got, ColorSurfacePanel)
	}
	// A normal key is the input surface, the focused key is the accent fill.
	row, col := rowIndex(keyboard, "q")
	keyRect := keyRectFor(row, col)
	if keyRect == nil {
		t.Fatal("q key missing")
	}
	// Sample near the key corner so the label glyph does not skew the colour.
	keyPixel := f.at(keyRect.X+6, keyRect.Y+6)
	if !closeColor(keyPixel, ColorIconSurface, 16) {
		t.Fatalf("unfocused key pixel %v is not the input surface %v", keyPixel, ColorIconSurface)
	}
	focusRect := keyRectFor(0, 0)
	focusPixel := f.at(focusRect.X+6, focusRect.Y+6)
	if !closeColor(focusPixel, vkFocusColor(), 24) {
		t.Fatalf("focused key pixel %v is not the keyboard highlight %v", focusPixel, vkFocusColor())
	}
	// Every key must draw a label (catches missing glyphs and blank keys).
	for i, r := range keyboardRects {
		if r.W < 24 || r.H < 24 {
			continue
		}
		nonFill := 0
		for y := r.Y + r.H/4; y < r.Y+3*r.H/4; y++ {
			for x := r.X + r.W/4; x < r.X+3*r.W/4; x++ {
				c := f.at(x, y)
				if !closeColor(c, ColorIconSurface, 18) &&
					!closeColor(c, ColorSurfaceRaised, 18) &&
					!closeColor(c, vkFocusColor(), 30) {
					nonFill++
				}
			}
		}
		if nonFill < 2 {
			t.Errorf("key %q appears unlabelled (nonFill=%d)", keyboardKeys[i], nonFill)
		}
	}

	// The entered text and caret are visible in the field.
	bright := 0
	for y := vkFieldRect.Y + 6; y < vkFieldRect.Y+vkFieldRect.H-6; y += 2 {
		for x := vkFieldRect.X + 6; x < vkFieldRect.X+vkFieldRect.W-6; x += 2 {
			c := f.at(x, y)
			if c.R > 150 || c.G > 150 {
				bright++
			}
		}
	}
	if bright < 5 {
		t.Fatalf("text field appears empty (bright pixels=%d)", bright)
	}

	// The old Miami palette (0,220,255 / 255,0,200 / saturated purple) must be
	// gone. Detect saturated neon by chroma, not brightness, so white text and
	// the theme accent do not count.
	neon := 0
	for y := int32(0); y < probeH; y += 3 {
		for x := int32(0); x < probeW; x += 3 {
			c := f.at(x, y)
			r, g, b := int(c.R), int(c.G), int(c.B)
			max, min := r, r
			for _, v := range []int{g, b} {
				if v > max {
					max = v
				}
				if v < min {
					min = v
				}
			}
			if max >= 190 && max-min >= 150 && min <= 90 {
				neon++
			}
		}
	}
	if neon > 0 {
		t.Fatalf("found %d neon pixels; keyboard should use theme colours", neon)
	}
}

func keyRectFor(row, col int) *sdl.Rect {
	for i, rc := range keyboardRowCol {
		if rc[0] == row && rc[1] == col {
			return &keyboardRects[i]
		}
	}
	return nil
}

func closeColor(a, b sdl.Color, tolerance int) bool {
	return absInt(int(a.R)-int(b.R)) <= tolerance &&
		absInt(int(a.G)-int(b.G)) <= tolerance &&
		absInt(int(a.B)-int(b.B)) <= tolerance
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
