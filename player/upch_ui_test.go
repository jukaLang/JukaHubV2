package main

// upch_ui_test.go draws the update channel's sheets offscreen and drives them
// the way a hand does: the rows land inside the card, the focused version
// carries its Notes and Delete buttons, a click asks before rolling back, B
// closes, and an empty file list shows the sideload instructions.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestUpchSheetsDrawAndAnswer(t *testing.T) {
	newProbeFrame(t)
	config := &Config{}
	if font := getFontSized(config, "small", 18); font == nil {
		t.Fatal("no usable font: the shipped Inter-Regular.ttf did not load, so this check cannot run")
	}
	upchTestResetState(t)
	prevW, prevH := screenWidth, screenHeight
	prevUI, prevModal := upchUI, patchModal
	screenWidth, screenHeight = probeW, probeH
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		upchUI, patchModal = prevUI, prevModal
	})
	uhistCache.mu.Lock()
	uhistCache.entries = []uhistEntry{
		{ID: "a", Tag: "JukaHub-20261105120000", Version: "v0.5.0", SavedAt: "2026-11-08T12:00:00Z", Bytes: 40 << 20, Notes: "• Faster boot"},
		{ID: "b", Tag: "JukaHub-20261020120000", SavedAt: "2026-11-01T12:00:00Z", Bytes: 39 << 20},
	}
	uhistCache.version++
	uhistCache.mu.Unlock()

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)

	upchOpenHistory(config)
	renderer.Clear()
	upchRenderSheet(renderer, config)
	card := upchUI.card
	if card.W <= 0 || card.X < 0 || card.Y < 0 || card.X+card.W > probeW || card.Y+card.H > probeH {
		t.Fatalf("the sheet's card %+v is not on the %dx%d screen", card, probeW, probeH)
	}
	if len(upchUI.rows) != 2 {
		t.Fatalf("drew %d rows for 2 saved versions", len(upchUI.rows))
	}
	for _, r := range upchUI.rows {
		if r.X < card.X || r.Y < card.Y || r.X+r.W > card.X+card.W || r.Y+r.H > card.Y+card.H {
			t.Errorf("row %+v leaves the card %+v", r, card)
		}
	}
	if upchUI.notesBtn.W == 0 || upchUI.deleteBtn.W == 0 {
		t.Error("the focused version has no Notes and Delete buttons")
	}

	// A click on the second version asks — with a hold — before rolling back.
	r := upchUI.rows[1]
	upchSheetMouse(r.X+r.W/4, r.Y+r.H/2)
	if patchModal == nil || patchModal.ID != "uhist-rollback:b" || !patchModal.HighRisk {
		t.Fatalf("clicking a saved version opened %+v", patchModal)
	}
	patchModal = nil

	// B leaves the sheet.
	upchSheetController(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_B})
	if upchSheetOpen() {
		t.Fatal("B did not close the version history")
	}

	// Install from file with nothing found shows how to sideload, not rows,
	// and keeps "Look again" in reach of a mouse.
	upchUI.sheet = upchSheetFiles
	upchUI.scanning = false
	upchUI.headLine = ""
	upchSetFiles(nil)
	renderer.Clear()
	upchRenderSheet(renderer, config)
	if len(upchUI.rows) != 0 || upchUI.rescanBtn.W == 0 || len(upchUI.helpLines) == 0 {
		t.Fatalf("an empty file list drew %d rows, button %+v, %d help lines", len(upchUI.rows), upchUI.rescanBtn, len(upchUI.helpLines))
	}
	if hints, ok := upchSheetFooterHints(); !ok || len(hints) == 0 || hints[len(hints)-1].Button != "B" {
		t.Fatalf("the file sheet's footer hints are %v", hints)
	}
	upchCloseSheet()
	if _, ok := upchSheetFooterHints(); ok {
		t.Fatal("a closed sheet still owns the footer")
	}
}
