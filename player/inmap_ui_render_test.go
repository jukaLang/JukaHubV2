package main

// inmap_ui_render_test.go — Control profiles and Local play drawn offscreen on
// the Smart Pro's and the Brick's canvases: every row, button, slot and card
// stays on the screen, in every view.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// inmapRectOnScreen reports whether r lies inside a w×h screen.
func inmapRectOnScreen(r sdl.Rect, w, h int32) bool {
	return r.W > 0 && r.H > 0 && r.X >= 0 && r.Y >= 0 && r.X+r.W <= w && r.Y+r.H <= h
}

func TestInmapScreensRender(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	inmapTestRegistry(t)
	hotseatTestReset(t)
	clearToastAfterTest(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	inmapEnsureScenes(config)
	renderer := probeRenderer(t)
	prevEngine, prevScene, prevUI, prevHot := focusEngine, currentSceneIndex, inmapUI, hotseatUI
	t.Cleanup(func() {
		focusEngine, currentSceneIndex, inmapUI, hotseatUI = prevEngine, prevScene, prevUI, prevHot
	})
	focusEngine = NewFocusEngine()

	for _, size := range []struct{ w, h int32 }{{1280, 720}, {960, 720}} {
		withScreen(t, size.w, size.h)

		currentSceneIndex = sceneIndexExact(config, inmapProfilesScene)
		inmapUI = inmapUIState{gen: -1}
		inmapUIRender(renderer, config)
		if len(inmapUI.rowRects) != len(inmapUI.list)+1 || len(inmapUI.list) < 5 {
			t.Fatalf("%dx%d: %d rows for %d profiles", size.w, size.h, len(inmapUI.rowRects), len(inmapUI.list))
		}
		if !inmapRectOnScreen(inmapUI.listRect, size.w, size.h) {
			t.Errorf("%dx%d: the list %+v leaves the screen", size.w, size.h, inmapUI.listRect)
		}
		if len(inmapUI.buttonRects) == 0 {
			t.Errorf("%dx%d: the card drew no buttons", size.w, size.h)
		}
		for i, r := range inmapUI.buttonRects {
			if !inmapRectOnScreen(r, size.w, size.h) {
				t.Errorf("%dx%d: card button %d %+v leaves the screen", size.w, size.h, i, r)
			}
		}
		// A profile with mappings and problems draws its card too.
		inmapUI.cursor = 1
		inmapUIRender(renderer, config)
		// The import row and its sheet.
		inmapUI.cursor = len(inmapUI.list)
		inmapUIRender(renderer, config)
		inmapImportUI.open = true
		inmapUIRender(renderer, config)
		if !inmapRectOnScreen(inmapImportUI.card, size.w, size.h) {
			t.Errorf("%dx%d: the import sheet %+v leaves the screen", size.w, size.h, inmapImportUI.card)
		}
		inmapImportUI.open = false

		currentSceneIndex = sceneIndexExact(config, hotseatScene)
		hotseatUI.view = hotseatViewLobby
		hotseat.players = []hotseatPlayer{{id: 1, name: "A controller with a long name that must be cut"}}
		hotseatUIRender(renderer, config)
		for i, r := range hotseatUI.slotRects {
			if !inmapRectOnScreen(r, size.w, size.h) {
				t.Errorf("%dx%d: slot %d %+v leaves the screen", size.w, size.h, i, r)
			}
		}
		for i, r := range hotseatUI.buttonRects {
			if !inmapRectOnScreen(r, size.w, size.h) {
				t.Errorf("%dx%d: lobby button %d %+v leaves the screen", size.w, size.h, i, r)
			}
		}
		hotseatUI.view = hotseatViewSetup
		hotseatUI.setupSeats = 2
		hotseatUIRender(renderer, config)
		for i, r := range hotseatUI.setupRects {
			if !inmapRectOnScreen(r, size.w, size.h) {
				t.Errorf("%dx%d: setup row %d %+v leaves the screen", size.w, size.h, i, r)
			}
		}
		hotseat.turns.start(2, 60)
		hotseatUI.view = hotseatViewTurns
		hotseatUIRender(renderer, config)
		hotseat.turns.begin(hotseatNow())
		hotseatUIRender(renderer, config)
		for i, r := range hotseatUI.buttonRects {
			if !inmapRectOnScreen(r, size.w, size.h) {
				t.Errorf("%dx%d: turn button %d %+v leaves the screen", size.w, size.h, i, r)
			}
		}
		hotseat.turns = hotseatTurns{}
	}
}
