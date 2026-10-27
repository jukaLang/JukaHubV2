package main

// Tests for the keyboard's feel work: the held-D-pad repeat, the press flash,
// the hover tint, the fading backdrop and the caret in an empty field.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func TestVkRepeatTrackerWaitsThenWalks(t *testing.T) {
	var tracker navRepeatTracker
	const start = 1000

	// The press itself is handled by the button event, so the tracker's first
	// frame only latches the direction.
	if dx, dy := tracker.step(start, 1, 0); dx != 0 || dy != 0 {
		t.Fatalf("first frame moved by (%d,%d), want no step", dx, dy)
	}
	if dx, _ := tracker.step(start+navRepeatDelayMs-1, 1, 0); dx != 0 {
		t.Fatalf("repeated after %dms, want to wait %dms", navRepeatDelayMs-1, navRepeatDelayMs)
	}
	if dx, _ := tracker.step(start+navRepeatDelayMs, 1, 0); dx != 1 {
		t.Fatalf("no repeat at the delay boundary")
	}
	if dx, _ := tracker.step(start+navRepeatDelayMs+navRepeatIntervalMs-1, 1, 0); dx != 0 {
		t.Fatalf("repeated before the interval elapsed")
	}
	if dx, _ := tracker.step(start+navRepeatDelayMs+navRepeatIntervalMs, 1, 0); dx != 1 {
		t.Fatalf("no repeat after one interval")
	}
}

func TestVkRepeatTrackerRestartsForANewDirection(t *testing.T) {
	var tracker navRepeatTracker
	tracker.step(1000, 1, 0)
	tracker.step(1000+navRepeatDelayMs, 1, 0) // now walking right

	// Down is a fresh direction: a nudge is a normal press, not an instant
	// repeat inherited from the right hold.
	if _, dy := tracker.step(1000+navRepeatDelayMs+10, 0, 1); dy != 0 {
		t.Fatalf("a new direction repeated immediately")
	}
	if _, dy := tracker.step(1000+2*navRepeatDelayMs+10, 0, 1); dy != 1 {
		t.Fatalf("the new direction never repeated")
	}
}

func TestVkRepeatTrackerStopsOnRelease(t *testing.T) {
	var tracker navRepeatTracker
	tracker.step(1000, 0, 1)
	tracker.step(1000+navRepeatDelayMs, 0, 1)
	if _, dy := tracker.step(1000+navRepeatDelayMs+5, 0, 0); dy != 0 {
		t.Fatalf("moved after the D-pad was released")
	}
	// Re-pressing waits the full delay again.
	tracker.step(2000, 0, 1)
	if _, dy := tracker.step(2000+navRepeatDelayMs-1, 0, 1); dy != 0 {
		t.Fatalf("re-press skipped the initial delay")
	}
}

func TestVkTickRepeatWithoutAPadKeepsTheFocus(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, probeW, probeH)
	vkEnsureLayout()
	before := keyboardPosX
	gameController = nil
	vkTickNavRepeat()
	vkTickNavRepeat()
	if keyboardPosX != before {
		t.Fatalf("focus moved to col %d with no pad held", keyboardPosX)
	}
}

func TestVkToggleLayerSwapsTheKeyGrid(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, probeW, probeH)
	vkTarget = vkTargetScene
	vkEnsureLayout()

	if vkLayer != vkLayerLetters {
		t.Fatalf("keyboard did not start on the letters layer")
	}
	vkToggleLayer()
	if vkLayer != vkLayerSymbols {
		t.Fatal("the right shoulder did not switch to symbols")
	}
	if r, _ := rowIndex(keyboard, "!"); r < 0 {
		t.Fatal("symbols layer has no punctuation")
	}
	vkToggleLayer()
	if r, _ := rowIndex(keyboard, "q"); r < 0 {
		t.Fatal("switching back did not restore the letters")
	}
}

func TestKeyboardControllerButtonsDriveTheGrid(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, probeW, probeH)
	vkTarget = vkTargetUnit
	unitInputValue = ""
	vkEnsureLayout()
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}

	// R1 reaches the symbols layer without a walk to the bottom row.
	if !vkHandleControllerButton(uint8(sdl.CONTROLLER_BUTTON_RIGHTSHOULDER), cfg) {
		t.Fatal("the keyboard ignored the right shoulder")
	}
	if vkLayer != vkLayerSymbols {
		t.Fatal("R1 did not switch to the symbols layer")
	}
	vkHandleControllerButton(uint8(sdl.CONTROLLER_BUTTON_RIGHTSHOULDER), cfg)
	if vkLayer != vkLayerLetters {
		t.Fatal("a second R1 did not switch back to letters")
	}

	if !vkHandleControllerButton(uint8(sdl.CONTROLLER_BUTTON_LEFTSHOULDER), cfg) || !keyboardUpper {
		t.Fatal("L1 did not shift the letters")
	}
	vkHandleControllerButton(uint8(sdl.CONTROLLER_BUTTON_LEFTSHOULDER), cfg)

	vkEnsureLayout()
	keyboardPosY, keyboardPosX = rowIndex(vkRows(), "q")
	vkHandleControllerButton(uint8(sdl.CONTROLLER_BUTTON_A), cfg)
	if unitInputValue != "q" {
		t.Fatalf("A typed %q, want q", unitInputValue)
	}

	if vkHandleControllerButton(uint8(sdl.CONTROLLER_BUTTON_X), cfg) {
		t.Fatal("an unassigned button was reported as consumed")
	}
	if !vkHandleControllerButton(uint8(sdl.CONTROLLER_BUTTON_B), cfg) || virtualKeyboardActive {
		t.Fatal("B did not close the keyboard")
	}
}

func TestKeyboardPressFlashLastsOneMoment(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, probeW, probeH)
	vkEnsureLayout()

	r := keyboardRects[5]
	vkFlashPressedRect(r)
	if got, ok := vkPressedRectAt(sdl.GetTicks64()); !ok || got != r {
		t.Fatalf("press flash = %v, %t, want the pressed cap", got, ok)
	}
	if _, ok := vkPressedRectAt(sdl.GetTicks64() + vkPressFlashMs + 1); ok {
		t.Fatal("the press flash outlived its window")
	}
	vkClearPressFlash()
	if _, ok := vkPressedRectAt(sdl.GetTicks64()); ok {
		t.Fatal("closing did not clear the press flash")
	}
}

func TestVkHoverIndexLandsOnTheCapUnderThePointer(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, probeW, probeH)
	vkEnsureLayout()

	want := 12
	r := keyboardRects[want]
	if got := vkHoverIndex(r.X+r.W/2, r.Y+r.H/2); got != want {
		t.Fatalf("hover index = %d, want %d", got, want)
	}
	// The padding inside the panel is not a key.
	if got := vkHoverIndex(vkPanelRect.X+2, vkPanelRect.Y+2); got != -1 {
		t.Fatalf("hover over panel padding = %d, want -1", got)
	}
}

func TestVkHoverOnlyWhileThePointerMoves(t *testing.T) {
	prevMovedAt := vkPointerMovedAt
	t.Cleanup(func() { vkPointerMovedAt = prevMovedAt })

	vkPointerMovedAt = 0
	if vkHoverFresh(5000) {
		t.Fatal("an untouched pointer counted as hover")
	}
	vkPointerMovedAt = 1000
	if !vkHoverFresh(1000 + vkHoverWindowMs) {
		t.Fatal("a just-moved pointer should hover")
	}
	if vkHoverFresh(1000 + vkHoverWindowMs + 1) {
		t.Fatal("a parked pointer kept hovering")
	}
}

func TestVkOpenProgressFadesIn(t *testing.T) {
	prev := vkAnimStart
	t.Cleanup(func() { vkAnimStart = prev })

	vkAnimStart = 0
	if p := vkOpenProgress(); p != 1 {
		t.Fatalf("a keyboard that is already open reports progress %v", p)
	}
	vkAnimStart = 1000
	if p := vkOpenProgressAt(1000); p != 0 {
		t.Fatalf("progress on the opening frame = %v, want 0", p)
	}
	if p := vkOpenProgressAt(1000 + vkOpenDurationMs/2); p < 0.4 || p > 0.6 {
		t.Fatalf("progress halfway through = %v, want about 0.5", p)
	}
	if p := vkOpenProgressAt(1000 + vkOpenDurationMs); p != 1 {
		t.Fatalf("progress at the end = %v, want 1", p)
	}
	if p := vkOpenProgressAt(999); p != 0 {
		t.Fatalf("progress before the start = %v, want 0", p)
	}
}

// keyboardFrame renders the open keyboard and returns the captured pixels.
func keyboardFrame(t *testing.T, renderer *sdl.Renderer, cfg *Config) *probeFrame {
	t.Helper()
	frame := newProbeFrame(t)
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	renderKeyboard(renderer, cfg)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&frame.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
	return frame
}

func TestKeyboardPressedCapLooksPushedIn(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, probeW, probeH)
	DestroyVKStaticCache()
	t.Cleanup(DestroyVKStaticCache)
	vkTarget = vkTargetScene
	vkAnimStart = 0
	vkEnsureLayout()

	renderer := newKeyboardTestRenderer(t)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	// A key that is not the focused one, so the highlight does not mask the
	// press effect.
	idx := 15
	if idx == vkFocusedRectIndex() {
		idx++
	}
	r := keyboardRects[idx]
	sampleX, sampleY := r.X+r.W/2, r.Y+r.H/2+6

	resting := keyboardFrame(t, renderer, cfg).at(sampleX, sampleY)
	vkFlashPressedRect(r)
	pressed := keyboardFrame(t, renderer, cfg).at(sampleX, sampleY)

	if colorLuma(pressed) >= colorLuma(resting) {
		t.Fatalf("pressed cap %v is not darker than the resting cap %v", pressed, resting)
	}
}

func TestKeyboardEmptyFieldShowsACaret(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, probeW, probeH)
	DestroyVKStaticCache()
	t.Cleanup(DestroyVKStaticCache)
	vkTarget = vkTargetChat
	chatInputText = ""
	vkAnimStart = 0
	vkEnsureLayout()

	renderer := newKeyboardTestRenderer(t)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	frame := keyboardFrame(t, renderer, cfg)

	// The caret is a two-pixel bar at the left edge of the field, vertically
	// centred. Without it the empty field looked like it had lost focus.
	caret := frame.at(vkFieldRect.X+16, vkFieldRect.Y+vkFieldRect.H/2)
	if !closeColor(caret, vkFocusColor(), 10) {
		t.Fatalf("empty field caret pixel = %v, want the focus colour %v", caret, vkFocusColor())
	}
}
