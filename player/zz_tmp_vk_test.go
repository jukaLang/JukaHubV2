package main

import "testing"

func TestTmpVkMultiTap(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	vkTarget = vkTargetScene
	activeSceneIndex, activeElementIndex = -1, -1
	t.Cleanup(func() { activeSceneIndex, activeElementIndex = -1, -1 })
	vkEnsureLayout()

	for _, key := range []string{"q", "w", "e", "r"} {
		row, col := rowIndex(keyboard, key)
		rect := keyRectFor(row, col)
		if rect == nil {
			t.Fatalf("key %q missing", key)
		}
		vkHandleTouchEvent(touchAt(rect.X+rect.W/2, rect.Y+rect.H/2), cfg)
		t.Logf("after %q: buffer=%q active=%v pos=(%d,%d)", key, inputTextBuffer, virtualKeyboardActive, keyboardPosX, keyboardPosY)
	}
}
