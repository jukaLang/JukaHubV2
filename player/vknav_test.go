package main

import "testing"

// The keyboard's wrap setting is off by default, so existing edge behaviour
// stays exactly as it was, and on it carries the highlight to the other end.
func TestVKFocusWraparound(t *testing.T) {
	oldConfig := appConfig
	oldW, oldH := screenWidth, screenHeight
	oldSig, oldLayer, oldTarget := vkLayoutSig, vkLayer, vkTarget
	oldUpper, oldCaps := keyboardUpper, vkCapsLock
	oldZone, oldY, oldX := vkFocusZone, keyboardPosY, keyboardPosX
	oldPopup := vkPopup.open
	defer func() {
		appConfig = oldConfig
		screenWidth, screenHeight = oldW, oldH
		vkLayoutSig, vkLayer, vkTarget = oldSig, oldLayer, oldTarget
		keyboardUpper, vkCapsLock = oldUpper, oldCaps
		vkFocusZone, keyboardPosY, keyboardPosX = oldZone, oldY, oldX
		vkPopup.open = oldPopup
	}()

	c := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	appConfig = c
	screenWidth, screenHeight = 1280, 720
	vkPopup.open = false
	vkLayer, vkTarget = vkLayerLetters, vkTargetScene
	keyboardUpper, vkCapsLock = false, false
	vkFocusZone = vkZoneKeys
	vkLayoutSig = ""
	vkEnsureLayout()
	if len(keyboard) == 0 || len(keyboard[0]) == 0 {
		t.Fatal("no keyboard rows to navigate")
	}
	lastRow := len(keyboard) - 1

	// Default: pressing Right at the end of a row does nothing.
	c.Variables.Custom[vkKeyWrap] = false
	keyboardPosY, keyboardPosX = 0, len(keyboard[0])-1
	vkMoveFocus(1, 0)
	if keyboardPosX != len(keyboard[0])-1 {
		t.Fatalf("off: Right at the row end moved to %d", keyboardPosX)
	}

	c.Variables.Custom[vkKeyWrap] = true
	keyboardPosY, keyboardPosX = 0, len(keyboard[0])-1
	vkMoveFocus(1, 0)
	if keyboardPosX != 0 {
		t.Fatalf("Right at the row end went to %d, want the first key", keyboardPosX)
	}
	vkMoveFocus(-1, 0)
	if keyboardPosX != len(keyboard[0])-1 {
		t.Fatalf("Left at the row start went to %d, want the last key", keyboardPosX)
	}

	// Down from the last row returns to the first; Up from the first returns
	// to the last. Both keep the focus inside the key grid.
	keyboardPosY, keyboardPosX = lastRow, 0
	vkMoveFocus(0, 1)
	if keyboardPosY != 0 || vkFocusZone != vkZoneKeys {
		t.Fatalf("Down from the last row went to row %d zone %d", keyboardPosY, vkFocusZone)
	}
	keyboardPosY, keyboardPosX = 0, 0
	vkMoveFocus(0, -1)
	if keyboardPosY != lastRow || vkFocusZone != vkZoneKeys {
		t.Fatalf("Up from the first row went to row %d zone %d", keyboardPosY, vkFocusZone)
	}

	// Without the setting, Up from the first row still leaves for the field.
	c.Variables.Custom[vkKeyWrap] = false
	keyboardPosY, keyboardPosX = 0, 0
	vkMoveFocus(0, -1)
	if vkFocusZone == vkZoneKeys {
		t.Fatal("off: Up from the first key row stayed in the grid")
	}
}
