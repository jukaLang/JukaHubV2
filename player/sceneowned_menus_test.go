package main

// sceneowned_menus_test.go — the overlays that belong to one screen.
//
// Several menus name rows on the screen that opened them: the browser's saved
// site picker, its saved-page picker, the Files action sheet and the Files
// remote transfer browser. Each is drawn, and its key, pad and tap handlers run,
// on the strength of its open flag alone. Left up by a scene switch, the ones
// whose input is dispatched globally float over the next screen with the last
// frame's row rects still live, so a press meant for the new screen lands in a
// menu whose rows point at a page, a folder or a remote host nobody can see.
//
// These tests drive a real switch and pin that the menus are gone (or, for the
// remote browser, that it is off screen while its transfer pump keeps working).

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// switchAwayFromScene starts the transition the Start menu and the shoulder
// buttons use, from scene 0 to scene 1, with the scene globals restored after
// the test.
func switchAwayFromScene(t *testing.T, from, to string) *Config {
	t.Helper()
	config := &Config{Scenes: []SceneConfig{{Name: from}, {Name: to}}}
	prevScene, prevPhase, prevPending := currentSceneIndex, transitionPhase, pendingSceneIndex
	prevEngine := focusEngine
	currentSceneIndex, transitionPhase, pendingSceneIndex = 0, "none", -1
	t.Cleanup(func() {
		currentSceneIndex, transitionPhase, pendingSceneIndex = prevScene, prevPhase, prevPending
		focusEngine = prevEngine
	})
	switchScene(config, 1, false)
	if transitionPhase != "fade-out" || pendingSceneIndex != 1 {
		t.Fatalf("the scene switch did not start: phase=%q pending=%d", transitionPhase, pendingSceneIndex)
	}
	return config
}

// completeSwitchAway runs the rest of the transition the render loop runs, so a
// test can assert what the destination scene actually shows. The helper above
// deliberately stops at the fade: departure is where a menu has to be dropped,
// but a panel that belongs to a screen is still drawn while that screen is the
// one on display, so "gone" can only be asked once the destination has arrived.
func completeSwitchAway(t *testing.T, config *Config) {
	t.Helper()
	prevPhase := transitionPhase
	transitionPhase = "fade-in"
	t.Cleanup(func() { transitionPhase = prevPhase })
	completeSceneTransition(config)
}

func cookieKey() *sdl.KeyboardEvent {
	return &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_a}}
}

func TestBrowserCookiePickerClosesWithItsScene(t *testing.T) {
	prevPanel, prevHits := browserCookiePanel, browserCookieRowHits
	prevRect, prevStrip := browserCookiePanelRect, browserCookieStripHit
	t.Cleanup(func() {
		browserCookiePanel, browserCookieRowHits = prevPanel, prevHits
		browserCookiePanelRect, browserCookieStripHit = prevRect, prevStrip
	})

	browserCookiePanel = browserCookiePanelState{open: true, cursor: 0}
	browserCookieRowHits = []struct {
		index int
		rect  sdl.Rect
	}{{index: 0, rect: sdl.Rect{X: 40, Y: 120, W: 400, H: 44}}}
	browserCookiePanelRect = sdl.Rect{X: 20, Y: 60, W: 500, H: 320}
	browserCookieStripHit = sdl.Rect{X: 40, Y: 500, W: 300, H: 16}

	// While the page is on screen the picker owns the keys — otherwise the
	// checks below would pass for the wrong reason.
	if !handleBrowserCookiesKey(cookieKey(), &Config{}) {
		t.Fatal("the open cookie picker did not consume a key")
	}

	config := switchAwayFromScene(t, "Browser", "Main")

	if browserCookiePanel.open {
		t.Error("the cookie picker outlived the browser scene")
	}
	if len(browserCookieRowHits) != 0 {
		t.Errorf("the picker kept %d row hits after leaving the scene", len(browserCookieRowHits))
	}
	if browserCookiePanelRect != (sdl.Rect{}) {
		t.Errorf("the picker kept its panel rect %+v", browserCookiePanelRect)
	}
	if browserCookieStripHit != (sdl.Rect{}) {
		t.Errorf("the page's cookie strip tap target survived the switch: %+v", browserCookieStripHit)
	}

	// And the presses that used to reach it now belong to the new screen.
	if handleBrowserCookiesKey(cookieKey(), config) {
		t.Error("a key was still eaten by the dismissed picker")
	}
	if handleBrowserCookiesMouse(24, 64, config) {
		t.Error("a tap was still eaten by the dismissed picker")
	}
}

// Closing the picker by hand is not the same thing: the page is still on
// screen, so the strip that opens it again must stay tappable.
func TestClosingTheCookiePickerByHandKeepsTheStripTappable(t *testing.T) {
	prevPanel, prevHits := browserCookiePanel, browserCookieRowHits
	prevRect, prevStrip := browserCookiePanelRect, browserCookieStripHit
	t.Cleanup(func() {
		browserCookiePanel, browserCookieRowHits = prevPanel, prevHits
		browserCookiePanelRect, browserCookieStripHit = prevRect, prevStrip
	})

	browserCookiePanel = browserCookiePanelState{open: true}
	browserCookieRowHits = []struct {
		index int
		rect  sdl.Rect
	}{{index: 0, rect: sdl.Rect{X: 40, Y: 120, W: 400, H: 44}}}
	browserCookiePanelRect = sdl.Rect{X: 20, Y: 60, W: 500, H: 320}
	browserCookieStripHit = sdl.Rect{X: 40, Y: 500, W: 300, H: 16}

	closeBrowserCookiePanel()

	if browserCookiePanel.open || len(browserCookieRowHits) != 0 || browserCookiePanelRect != (sdl.Rect{}) {
		t.Error("closing by hand left picker state behind")
	}
	if browserCookieStripHit.W <= 0 {
		t.Fatal("closing by hand took the page's strip tap target with it")
	}
}

func TestBrowserBookmarkPickerClosesWithItsScene(t *testing.T) {
	prevPanel, prevHits, prevRect := browserBookmarkPanel, browserBookmarkRowHits, browserBookmarkPanelRect
	t.Cleanup(func() {
		browserBookmarkPanel, browserBookmarkRowHits, browserBookmarkPanelRect = prevPanel, prevHits, prevRect
	})

	browserBookmarkPanel = browserBookmarkPanelState{open: true, cursor: 0}
	browserBookmarkRowHits = []struct {
		index int
		rect  sdl.Rect
	}{{index: 0, rect: sdl.Rect{X: 40, Y: 120, W: 400, H: 44}}}
	browserBookmarkPanelRect = sdl.Rect{X: 20, Y: 60, W: 500, H: 320}

	// A key the picker has no use for is still consumed while it is up.
	key := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_a}}
	if !handleBrowserBookmarksKey(key, &Config{}) {
		t.Fatal("the open saved-page picker did not consume a key")
	}

	config := switchAwayFromScene(t, "Browser", "Main")

	if browserBookmarkPanel.open {
		t.Error("the saved-page picker outlived the browser scene")
	}
	if len(browserBookmarkRowHits) != 0 {
		t.Errorf("the picker kept %d row hits after leaving the scene", len(browserBookmarkRowHits))
	}
	if browserBookmarkPanelRect != (sdl.Rect{}) {
		t.Errorf("the picker kept its panel rect %+v", browserBookmarkPanelRect)
	}
	if handleBrowserBookmarksKey(key, config) {
		t.Error("a key was still eaten by the dismissed picker")
	}
	if handleBrowserBookmarksMouse(24, 64, config) {
		t.Error("a tap was still eaten by the dismissed picker")
	}
}

func TestFilesActionSheetClosesWithItsScene(t *testing.T) {
	prevSheet, prevHits, prevRect := feSheet, feSheetRowHits, feSheetRect
	prevKeyboard := virtualKeyboardActive
	t.Cleanup(func() {
		feSheet, feSheetRowHits, feSheetRect = prevSheet, prevHits, prevRect
		virtualKeyboardActive = prevKeyboard
	})

	virtualKeyboardActive = false
	feSheet = feSheetState{open: true, cursor: 0}
	feSheetRowHits = []struct {
		index int
		rect  sdl.Rect
	}{{index: 0, rect: sdl.Rect{X: 40, Y: 200, W: 400, H: 48}}}
	feSheetRect = sdl.Rect{X: 20, Y: 100, W: 500, H: 340}

	down := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_DOWN}}
	if !handleFeSheetKey(down, &Config{}) {
		t.Fatal("the open action sheet did not consume a key")
	}

	config := switchAwayFromScene(t, "FileExplorer", "Main")

	if feSheet.open {
		t.Error("the Files action sheet outlived its scene")
	}
	if len(feSheetRowHits) != 0 {
		t.Errorf("the sheet kept %d row hits after leaving the scene", len(feSheetRowHits))
	}
	if feSheetRect != (sdl.Rect{}) {
		t.Errorf("the sheet kept its panel rect %+v", feSheetRect)
	}
	if handleFeSheetKey(down, config) {
		t.Error("a key was still eaten by the dismissed sheet")
	}
	if handleFeSheetMouse(24, 124, config) {
		t.Error("a tap was still eaten by the dismissed sheet")
	}
	if ctrlFeSheet(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A}, config) {
		t.Error("a pad press was still routed to the dismissed sheet")
	}
}

// The remote transfer browser is drawn and click-tested by the overlay pass
// that runs for every screen, so it needs the scene check its neighbours get
// from being drawn inside renderScene.
func TestFilesRemoteBrowserIsNotOnScreenOutsideFiles(t *testing.T) {
	prevPanel, prevHits, prevRect := feRemotePanel, feRemoteRowHits, feRemoteRect
	t.Cleanup(func() {
		feRemotePanel, feRemoteRowHits, feRemoteRect = prevPanel, prevHits, prevRect
	})

	feRemotePanel = feRemotePanelState{open: true, path: "/roms"}
	feRemoteRowHits = []struct {
		index int
		rect  sdl.Rect
	}{{index: 0, rect: sdl.Rect{X: 40, Y: 120, W: 400, H: 44}}}
	feRemoteRect = sdl.Rect{X: 20, Y: 60, W: 500, H: 320}

	files := &Config{Scenes: []SceneConfig{{Name: "FileExplorer"}}}
	prevScene := currentSceneIndex
	currentSceneIndex = 0
	t.Cleanup(func() { currentSceneIndex = prevScene })

	if !feRemotePanelOnScreen(files) {
		t.Fatal("the remote browser is not on screen on the Files screen that owns it")
	}

	config := switchAwayFromScene(t, "FileExplorer", "Main")
	// Files is still the screen on display for the whole fade-out, so the
	// session's browser must stay drawn until the destination arrives.
	if !feRemotePanelOnScreen(config) {
		t.Error("the remote browser vanished while its own screen was still fading out")
	}
	completeSwitchAway(t, config)
	if feRemotePanelOnScreen(config) {
		t.Error("the remote browser is still on screen after leaving Files")
	}
	// Its session is kept, not dropped: an upload started just before the
	// switch has to finish and be reported when the user comes back.
	if !feRemotePanel.open {
		t.Error("leaving Files threw the remote session away instead of hiding it")
	}
}

// Hiding the panel must not stall the connection: finished work is still
// applied, so the next connect is not refused for ever by a busy flag nobody
// cleared.
func TestRemoteTransferPumpRunsWithThePanelHidden(t *testing.T) {
	prevPanel := feRemotePanel
	t.Cleanup(func() {
		feRemotePanel = prevPanel
		feRemoteSetProgress("", 0, 0)
		for {
			select {
			case <-feRemoteEvents:
			default:
				return
			}
		}
	})

	feRemotePanel = feRemotePanelState{open: false, busy: true, status: "Uploading"}
	feRemoteEvents <- feRemoteEvent{kind: feRemoteFinished, message: "Uploaded 3 files"}

	feRemoteDrain(&Config{})

	if feRemotePanel.busy {
		t.Error("a finished transfer left the remote browser busy while it was off screen")
	}
	if feRemotePanel.status != "Uploaded 3 files" {
		t.Errorf("finished transfer reported %q, want its own message", feRemotePanel.status)
	}
}

// TestBrowserLinkPickerClosesWithItsScene: the third of the browser's
// scene-owned pickers. The transition already forgot it; this pins it beside
// the cookie and saved-page cases so the set cannot drift again.
func TestBrowserLinkPickerClosesWithItsScene(t *testing.T) {
	prevPanel, prevHits, prevRect := browserLinkPanel, browserLinkRowHits, browserLinkPanelRect
	t.Cleanup(func() {
		browserLinkPanel, browserLinkRowHits, browserLinkPanelRect = prevPanel, prevHits, prevRect
	})

	browserLinkPanel = browserLinkPanelState{open: true, variable: "browser_text", cursor: 0}
	browserLinkRowHits = []struct {
		index int
		rect  sdl.Rect
	}{{index: 0, rect: sdl.Rect{X: 40, Y: 120, W: 400, H: 44}}}
	browserLinkPanelRect = sdl.Rect{X: 20, Y: 60, W: 500, H: 320}

	key := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_a}}
	if !handleBrowserLinksKey(key, &Config{}) {
		t.Fatal("the open link picker did not consume a key")
	}

	config := switchAwayFromScene(t, "Browser", "Main")

	if browserLinkPanel.open {
		t.Error("the link picker outlived the browser scene")
	}
	if len(browserLinkRowHits) != 0 {
		t.Errorf("the picker kept %d row hits after leaving the scene", len(browserLinkRowHits))
	}
	if browserLinkPanelRect != (sdl.Rect{}) {
		t.Errorf("the picker kept its panel rect %+v", browserLinkPanelRect)
	}
	if handleBrowserLinksKey(key, config) {
		t.Error("a key was still eaten by the dismissed picker")
	}
	if handleBrowserLinksMouse(24, 64, config) {
		t.Error("a tap was still eaten by the dismissed picker")
	}
}

// TestPackageRowMenuClosesWithItsScene: the Reinstall/Remove menu acts on the
// package that opened it, and it used to be cleared only once the next scene
// arrived. Input is not gated during the fade, so a press in that window still
// reached the menu; the departure has to take it down.
func TestPackageRowMenuClosesWithItsScene(t *testing.T) {
	prevOpen, prevFocus, prevPkg := packageActionsOpen, packageActionsFocus, packageActionsPkg
	t.Cleanup(func() {
		packageActionsOpen, packageActionsFocus, packageActionsPkg = prevOpen, prevFocus, prevPkg
	})

	openPackageActions(Package{Name: "RetroArch"})
	if !packageActionsOpen {
		t.Fatal("the row menu did not open")
	}

	config := switchAwayFromScene(t, "Packages", "Main")

	if packageActionsOpen {
		t.Error("the package row menu outlived its scene")
	}
	if packageActionsPkg.Name != "" {
		t.Errorf("the menu still held %q after leaving the scene", packageActionsPkg.Name)
	}
	if packageActionsController(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A}, config) {
		t.Error("a pad press meant for the next screen still reached the row menu")
	}
}

// TestResultActionsCloseWithTheirScene: the focused pill and the hit rects the
// list recorded belong to rows on the screen being left. Both must be gone, or
// a tap during the fade presses a pill on a row nobody can see any more.
func TestResultActionsCloseWithTheirScene(t *testing.T) {
	prevFocus, prevHits := focusedResultAction, resultActionHits
	t.Cleanup(func() { focusedResultAction, resultActionHits = prevFocus, prevHits })

	focusedResultAction = resultActionAudio
	resultActionHits = []resultActionHit{
		{action: resultActionAudio, index: 3, rect: sdl.Rect{X: 400, Y: 200, W: 70, H: 30}},
	}

	switchAwayFromScene(t, "Tube", "Main")

	if focusedResultAction != resultActionNone {
		t.Errorf("the focused pill survived the scene switch: %d", focusedResultAction)
	}
	if len(resultActionHits) != 0 {
		t.Errorf("the pills kept %d hit rects after leaving the scene", len(resultActionHits))
	}
	if action, _ := resultActionHitAt(410, 210); action != resultActionNone {
		t.Errorf("a tap on the dismissed pills still pressed action %d", action)
	}
}
