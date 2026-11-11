package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// brwFSTestConfig is a config with a Home scene and whatever the custom block
// holds; the Browser scene is injected by the test that needs it.
func brwFSTestConfig(custom map[string]interface{}) *Config {
	if custom == nil {
		custom = map[string]interface{}{}
	}
	return &Config{
		Variables: Variables{Custom: custom},
		Scenes:    []SceneConfig{{Name: "Main"}},
	}
}

// brwFSKeepGlobals restores the globals the layout and the menu tests move.
func brwFSKeepGlobals(t *testing.T) {
	t.Helper()
	w, h := screenWidth, screenHeight
	on, menu, fs := brwFullScreenOn, brwMenu, brwFS
	scene, selected := currentSceneIndex, selectedButtonIndex
	t.Cleanup(func() {
		screenWidth, screenHeight = w, h
		brwFullScreenOn, brwMenu, brwFS = on, menu, fs
		currentSceneIndex, selectedButtonIndex = scene, selected
	})
}

// The layout switch is on unless a stored value says off, in every shape a
// hand-edited or builder-made config stores it.
func TestBrwFullScreenSettingShapes(t *testing.T) {
	if !brwFullScreenSetting(nil) || !brwFullScreenSetting(brwFSTestConfig(nil)) {
		t.Fatal("a config without the key must be full screen")
	}
	cases := []struct {
		value interface{}
		want  bool
	}{
		{true, true}, {false, false},
		{"true", true}, {"false", false}, {" Off ", false}, {"no", false}, {"0", false},
		{"yes", true}, {"", true}, {"garbage", true},
		{float64(0), false}, {float64(1), true}, {0, false}, {2, true},
	}
	for _, c := range cases {
		cfg := brwFSTestConfig(map[string]interface{}{brwFullScreenKey: c.value})
		if got := brwFullScreenSetting(cfg); got != c.want {
			t.Errorf("%s = %#v: full screen %v, want %v", brwFullScreenKey, c.value, got, c.want)
		}
	}
	// The web builder capitalises keys.
	if brwFullScreenSetting(brwFSTestConfig(map[string]interface{}{"BrowserFullScreen": "false"})) {
		t.Error("the builder's casing of the key was not read")
	}
}

// Full screen the Browser scene is the page alone, covering the canvas; with
// the switch off it is the classic page with its chip row, and nothing in the
// classic layout is measured the full-screen way.
func TestBrowserSceneLayouts(t *testing.T) {
	brwFSKeepGlobals(t)
	screenWidth, screenHeight = 1280, 720

	full := brwFSTestConfig(nil)
	ensureBrowserScene(full)
	idx := sceneIndexExact(full, browserSceneName)
	if idx < 0 {
		t.Fatal("no Browser scene was injected")
	}
	scene := full.Scenes[idx]
	if len(scene.Elements) != 1 {
		t.Fatalf("full screen: %d elements, want the page alone", len(scene.Elements))
	}
	page := scene.Elements[0]
	if page.Type != "textbrowser" || page.Variable != browserContentVariable || page.X != 0 || page.Y != 0 ||
		getElementWidth(page, 0) != 1280 || getElementHeight(page, 0) != 720 {
		t.Errorf("full screen: the page is %+v, want a web panel covering 1280x720", page)
	}
	if !brwFullScreenOn || !brwFullscreenElem(page) || !brwFullscreenScene(scene) {
		t.Error("the full-screen layout was not recorded")
	}
	// The geometry the hit tests and the scroll use is the whole element less
	// its margins, with no header band or toolbar above the page.
	if left, top, w, h := browserPageContentBox(page); left != 7 || top != 4 || w != 1266 || h != 712 {
		t.Errorf("full screen: page box %d,%d %dx%d, want 7,4 1266x712", left, top, w, h)
	}
	if rows := browserVisibleRows(page, &webLayout{RowH: 22}); rows != 712/22 {
		t.Errorf("full screen: %d visible rows, want %d", rows, 712/22)
	}

	classic := brwFSTestConfig(map[string]interface{}{brwFullScreenKey: false})
	ensureBrowserScene(classic)
	scene = classic.Scenes[sceneIndexExact(classic, browserSceneName)]
	if brwFullScreenOn || brwFullscreenScene(scene) {
		t.Error("the classic layout was recorded as full screen")
	}
	if len(scene.Elements) < 2 || scene.Elements[0].Type != "textbrowser" {
		t.Fatalf("classic: %d elements, want the page and its chips", len(scene.Elements))
	}
	if brwFullscreenElem(scene.Elements[0]) {
		t.Error("the classic page was measured as full screen")
	}
	for _, el := range scene.Elements[1:] {
		if el.Type != "button" || !strings.HasPrefix(el.Trigger, "browser_") {
			t.Errorf("classic: unexpected element %q (%s, %q)", el.Text, el.Type, el.Trigger)
		}
	}
	// A web panel on a screen of its own keeps its frame even full screen.
	brwFullScreenOn = true
	other := Element{Type: "textbrowser", Text: "web", Variable: "my_page"}
	if brwFullscreenElem(other) {
		t.Error("a config's own web panel was taken for the Browser's page")
	}
}

// Changing the switch rebuilds the scene in place, both ways.
func TestBrwApplyLayoutRebuildsTheScene(t *testing.T) {
	brwFSKeepGlobals(t)
	screenWidth, screenHeight = 960, 720
	currentSceneIndex = -1

	cfg := brwFSTestConfig(nil)
	ensureBrowserScene(cfg)
	idx := sceneIndexExact(cfg, browserSceneName)
	cfg.Variables.Custom[brwFullScreenKey] = "false"
	brwApplyLayout(cfg)
	if brwFullScreenOn || len(cfg.Scenes[idx].Elements) < 2 {
		t.Fatalf("switched off: full screen %v with %d elements", brwFullScreenOn, len(cfg.Scenes[idx].Elements))
	}
	cfg.Variables.Custom[brwFullScreenKey] = true
	brwApplyLayout(cfg)
	if !brwFullScreenOn || len(cfg.Scenes[idx].Elements) != 1 || getElementWidth(cfg.Scenes[idx].Elements[0], 0) != 960 {
		t.Fatalf("switched on: full screen %v, elements %+v", brwFullScreenOn, cfg.Scenes[idx].Elements)
	}
	if n := len(cfg.Scenes); n != 2 {
		t.Errorf("the rebuild added scenes: %d, want 2", n)
	}
	// Opening the Browser again (ensureBrowserScene) keeps the injected scene
	// in step with the switch too.
	cfg.Variables.Custom[brwFullScreenKey] = "off"
	ensureBrowserScene(cfg)
	if brwFullScreenOn || len(cfg.Scenes[idx].Elements) < 2 || len(cfg.Scenes) != 2 {
		t.Errorf("ensureBrowserScene left full screen %v with %d elements", brwFullScreenOn, len(cfg.Scenes[idx].Elements))
	}

	// A Browser scene a config ships itself keeps the layout it was written
	// with: it is neither drawn full screen nor rebuilt by the switch.
	own := brwFSTestConfig(nil)
	own.Scenes = append(own.Scenes, SceneConfig{Name: browserSceneName, Description: "my browser",
		Elements: []Element{{Type: "textbrowser", Text: "web", Variable: browserContentVariable}}})
	brwFullScreenOn = true
	ensureBrowserScene(own)
	if brwFullScreenOn || brwFullscreenScene(own.Scenes[1]) || brwFullscreenElem(own.Scenes[1].Elements[0]) {
		t.Error("a config's own Browser scene was taken for the full-screen layout")
	}
	own.Variables.Custom[brwFullScreenKey] = true
	brwApplyLayout(own)
	if len(own.Scenes[1].Elements) != 1 || own.Scenes[1].Elements[0].Width != "" {
		t.Errorf("the switch rebuilt a config's own Browser scene: %+v", own.Scenes[1].Elements)
	}
}

// The menu's entries keep their places whatever the page: the four buttons of
// the top row first, Close last, every entry labelled and drawn with a glyph
// the icon set has (the Forward arrow is new with the menu).
func TestBrwMenuItemsKeepTheirPlaces(t *testing.T) {
	defer browserBookmarksResetForTest(filepath.Join(t.TempDir(), "bookmarks.json"))()
	items := brwMenuItems(nil)
	if len(items) != int(brwMenuCount) {
		t.Fatalf("%d entries, want %d", len(items), brwMenuCount)
	}
	want := map[brwMenuID]string{
		brwMenuBack: "Back", brwMenuForward: "Forward", brwMenuReload: "Reload", brwMenuHome: "Home",
		brwMenuAddress: "Go to address", brwMenuClose: "Close browser",
	}
	seen := map[string]bool{}
	for i, it := range items {
		if it.ID != brwMenuID(i) {
			t.Errorf("entry %d carries id %d", i, it.ID)
		}
		if it.Label == "" || seen[it.Label] {
			t.Errorf("entry %d: empty or repeated label %q", i, it.Label)
		}
		seen[it.Label] = true
		if !iconKnown(it.Icon) {
			t.Errorf("%s: the icon set has no %q", it.Label, it.Icon)
		}
		if label, ok := want[it.ID]; ok && it.Label != label {
			t.Errorf("entry %d is %q, want %q", i, it.Label, label)
		}
	}
	for _, err := range iconLibGet().errs {
		if strings.Contains(err.Error(), `"forward"`) {
			t.Errorf("the forward arrow does not parse: %v", err)
		}
	}
	// With no page, the page's own actions are dimmed rather than removed.
	for _, id := range []brwMenuID{brwMenuSave, brwMenuInfo, brwMenuFilter, brwMenuFind} {
		if !items[id].Disabled {
			t.Errorf("%s is live with no page on screen", items[id].Label)
		}
	}
	if !items[brwMenuZoom].Adjust || items[brwMenuZoom].Value != browserZoomLabel() {
		t.Errorf("Zoom: %+v", items[brwMenuZoom])
	}
}

// The cursor walks the top row with Left/Right and the list with Up/Down,
// wraps round both ends, and comes back up to the column it left.
func TestBrwMenuMove(t *testing.T) {
	brwFSKeepGlobals(t)
	last := int(brwMenuCount) - 1
	brwMenu = brwMenuState{open: true}
	step := func(dx, dy, want int) {
		t.Helper()
		brwMenuMove(dx, dy)
		if brwMenu.cursor != want {
			t.Fatalf("move %d,%d: cursor %d, want %d", dx, dy, brwMenu.cursor, want)
		}
	}
	step(1, 0, 1)
	step(1, 0, 2)
	step(0, 1, int(brwMenuAddress))
	step(0, 1, int(brwMenuAddress)+1)
	step(0, -1, int(brwMenuAddress))
	step(0, -1, 2) // back to the column it left
	step(1, 0, 3)
	step(1, 0, 3) // the row's end is a stop
	step(0, -1, last)
	step(0, 1, 0)
	step(-1, 0, 0)
	step(0, -1, last)
	step(-1, 0, last) // Left on a list row that changes nothing does nothing

	brwMenu.cursor = int(brwMenuAddress)
	brwMenuJump(brwMenuPage)
	if want := int(brwMenuAddress) + brwMenuPage; brwMenu.cursor != want {
		t.Errorf("a page down: cursor %d, want %d", brwMenu.cursor, want)
	}
	brwMenuJump(1000)
	if brwMenu.cursor != last {
		t.Errorf("a long page down: cursor %d, want %d", brwMenu.cursor, last)
	}
	brwMenuJump(-1000)
	if brwMenu.cursor != int(brwMenuAddress) {
		t.Errorf("a long page up: cursor %d, want the first list row", brwMenu.cursor)
	}
	brwMenu.cursor = 1
	brwMenuJump(-brwMenuPage)
	if brwMenu.cursor != 1 {
		t.Errorf("a page up from the top row moved the cursor to %d", brwMenu.cursor)
	}
}

// Select (and M) opens the menu over the Browser and nowhere else, and B or
// Select again closes it; a keyboard on screen keeps it shut.
func TestBrwMenuOpensOverTheBrowserOnly(t *testing.T) {
	brwFSKeepGlobals(t)
	if reason := modalPanelBlockReason(); reason != "" {
		t.Skipf("another test left a panel open: %s", reason)
	}
	keyboard := virtualKeyboardActive
	t.Cleanup(func() { virtualKeyboardActive = keyboard })
	screenWidth, screenHeight = 1280, 720
	cfg := brwFSTestConfig(nil)
	ensureBrowserScene(cfg)
	brwMenu = brwMenuState{}

	pad := func(button uint8) bool {
		return brwMenuController(nil, &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: button}, cfg)
	}
	key := func(sym sdl.Keycode) bool {
		return brwMenuKey(nil, &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}}, cfg)
	}

	currentSceneIndex = 0
	if pad(sdl.CONTROLLER_BUTTON_BACK) || brwMenu.open {
		t.Fatal("Select opened the browser menu on Home")
	}
	currentSceneIndex = sceneIndexExact(cfg, browserSceneName)
	if !pad(sdl.CONTROLLER_BUTTON_BACK) || !brwMenu.open {
		t.Fatal("Select did not open the menu over the Browser")
	}
	wantCursor := int(brwMenuAddress)
	if browserHistoryAvailable(-1) {
		wantCursor = int(brwMenuBack)
	}
	if brwMenu.cursor != wantCursor {
		t.Errorf("the menu opened on entry %d, want %d", brwMenu.cursor, wantCursor)
	}
	if reason := modalPanelBlockReason(); !strings.Contains(reason, "browser menu") {
		t.Errorf("the open menu does not hold the shoulders: %q", reason)
	}
	if !pad(sdl.CONTROLLER_BUTTON_B) || brwMenu.open {
		t.Fatal("B did not close the menu")
	}
	if !key(sdl.K_m) || !brwMenu.open {
		t.Fatal("M did not open the menu")
	}
	if !key(sdl.K_m) || brwMenu.open {
		t.Fatal("M did not close the menu again")
	}
	if !pad(sdl.CONTROLLER_BUTTON_BACK) || !pad(sdl.CONTROLLER_BUTTON_BACK) || brwMenu.open {
		t.Fatal("Select did not toggle the menu")
	}
	// Other keys and buttons pass through while the menu is shut.
	if key(sdl.K_x) || pad(sdl.CONTROLLER_BUTTON_A) {
		t.Error("the closed menu took a key that is not its own")
	}

	virtualKeyboardActive = true
	if pad(sdl.CONTROLLER_BUTTON_BACK) || key(sdl.K_m) || brwMenu.open {
		t.Error("the menu opened over the on-screen keyboard")
	}
	virtualKeyboardActive = keyboard

	// Leaving the screen forgets the menu and the page's status pills.
	brwMenu = brwMenuState{open: true, cursor: 7}
	brwFS = brwFSState{url: "https://example.com/", shownAt: 1, pill: sdl.Rect{W: 10, H: 10}}
	brwMenuForget()
	if brwMenu.open || brwMenu.cursor != 0 || brwFS.url != "" || brwFS.pill.W != 0 {
		t.Errorf("brwMenuForget left %+v / %+v", brwMenu, brwFS)
	}
}

// Only the rows that change in place answer Left/Right, and a row's shortcut
// is named in the vocabulary of the hints on screen.
func TestBrwMenuAdjustAndBadges(t *testing.T) {
	brwFSKeepGlobals(t)
	brwMenu = brwMenuState{open: true, cursor: int(brwMenuSearch)}
	if brwMenuAdjust(nil, 1) {
		t.Error("Search changed with Left/Right")
	}
	brwMenu.cursor = int(brwMenuFind)
	find := browserFind
	t.Cleanup(func() { browserFind = find })
	browserFind = browserFindState{}
	if brwMenuAdjust(nil, 1) {
		t.Error("Find stepped with no search running")
	}
	if got := brwMenuBadge(brwMenuItem{Label: "Find on this page", Key: "F", KeyOnly: true}); got != "" && !hintsUseKeyboard() {
		t.Errorf("a keyboard-only shortcut was named on the pad: %q", got)
	}
	if got := brwMenuBadge(brwMenuItem{Label: "Links on this page", Key: "X"}); got != "X" {
		t.Errorf("Links' badge is %q, want X", got)
	}
	if got := brwMenuBadge(brwMenuItem{Label: "Close browser"}); got != "" {
		t.Errorf("a row without a shortcut got the badge %q", got)
	}
	if got := brwMenuDisabledNote(brwMenuBack); got != "No previous page" {
		t.Errorf("Back's note is %q", got)
	}
}

// The sheet stands at the right of the screen, inside it, on both canvases.
func TestBrwMenuCardRect(t *testing.T) {
	brwFSKeepGlobals(t)
	for _, size := range [][2]int32{{1280, 720}, {960, 720}} {
		screenWidth, screenHeight = size[0], size[1]
		card := brwMenuCardRect()
		if card.X < 0 || card.Y < 0 || card.X+card.W > screenWidth || card.Y+card.H > screenHeight {
			t.Errorf("%dx%d: the sheet %+v leaves the screen", size[0], size[1], card)
		}
		if card.W < 380 || card.X+card.W != screenWidth-12 {
			t.Errorf("%dx%d: the sheet %+v is not a right-hand sheet", size[0], size[1], card)
		}
	}
}
