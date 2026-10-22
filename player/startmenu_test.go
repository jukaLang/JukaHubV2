package main

// The Start menu groups scenes, games, tools and device actions into one
// submenu. These tests pin the grouping rules, the navigation model (rail vs
// entries), the controller and keyboard routes, and that a click on a rendered
// row lands on the entry it covers.

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// startMenuTestConfig builds the scenes the menu groups depend on: ordinary
// scenes, settings sub-screens and the utility scenes that must stay hidden.
func startMenuTestConfig() *Config {
	return &Config{Scenes: []SceneConfig{
		{Name: "Main", Icon: "H"},
		{Name: "Tube", Icon: "T"},
		{Name: "Chat", Icon: "C"},
		{Name: "Notes", Icon: "N"},
		{Name: "Games", Icon: "G"},
		{Name: "Settings", Icon: "S"},
		{Name: "SettingsAppearance", Icon: "A"},
		{Name: "ThemeEditor", Icon: "E"},
		{Name: "About", Icon: "i"},
		{Name: "Patch", Icon: "P"},
		{Name: "Exit", Icon: "X"},
		// A hand-made scene for something the menu already offers as an action:
		// the action row wins so the label is not listed twice.
		{Name: "Emulators", Icon: "E"},
	}}
}

// useStartMenuTestState isolates the menu globals for a test that drives them
// directly, restoring whatever the rest of the package had.
func useStartMenuTestState(t *testing.T) {
	t.Helper()
	prev := tb
	t.Cleanup(func() { tb = prev })
	tb = startMenuState{startOpen: true, focus: 1}
}

// useCleanControllerOverlays closes the global overlays that legitimately eat a
// Start press before the router reaches the menu. Another test in the package
// may have left one open, and the router's own precedence is intentional, so a
// test that drives the real controller path starts from an empty screen.
func useCleanControllerOverlays(t *testing.T) {
	t.Helper()
	prevShortcuts, prevClip, prevGlobal := shortcutsOpen, clipSearchOpen, globalSearchOpen
	prevQS, prevWP := qs.open, wpOpen
	prevViewer, prevKeyboard := imageViewerPath, virtualKeyboardActive
	prevWeather, prevSocial := weatherPopoverOpen, socialNotifOpen
	prevChatSel, prevChatProfile, prevChatMembers := chatSelectionBar, chatProfile, chatMembers
	shortcutsOpen, clipSearchOpen, globalSearchOpen = false, false, false
	qs.open, wpOpen = false, false
	imageViewerPath, virtualKeyboardActive = "", false
	weatherPopoverOpen, socialNotifOpen = false, false
	chatSelectionBar = chatSelection{}
	chatProfile = chatProfileCard{}
	chatMembers = chatMembersPanel{}
	t.Cleanup(func() {
		shortcutsOpen, clipSearchOpen, globalSearchOpen = prevShortcuts, prevClip, prevGlobal
		qs.open, wpOpen = prevQS, prevWP
		imageViewerPath, virtualKeyboardActive = prevViewer, prevKeyboard
		weatherPopoverOpen, socialNotifOpen = prevWeather, prevSocial
		chatSelectionBar, chatProfile, chatMembers = prevChatSel, prevChatProfile, prevChatMembers
	})
}

// ctrlButton builds the press event the controller router expects.
func ctrlButton(b uint8) *sdl.ControllerButtonEvent {
	return &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: b}
}

// startMenuHitFor returns the recorded hit rect for an index.
func startMenuHitFor(hits []startMenuHit, index int) *startMenuHit {
	for i := range hits {
		if hits[i].index == index {
			return &hits[i]
		}
	}
	return nil
}

// startMenuEntryNamed finds a row by its label across every group.
func startMenuEntryNamed(groups []startMenuGroup, name string) (startMenuEntry, bool) {
	for _, group := range groups {
		for _, entry := range group.items {
			if entry.name == name {
				return entry, true
			}
		}
	}
	return startMenuEntry{}, false
}

// TestStartMenuGroupsCoverScenesAndActions checks the submenu's shape: Scenes
// first, every row reachable once, settings sub-screens and utility scenes out,
// and no row without a destination.
func TestStartMenuGroupsCoverScenesAndActions(t *testing.T) {
	config := startMenuTestConfig()
	groups := startMenuGroups(config)
	if len(groups) < 4 {
		t.Fatalf("menu has %d groups, want at least 4", len(groups))
	}
	if groups[0].name != "Scenes" {
		t.Errorf("first group is %q, want Scenes", groups[0].name)
	}
	if len(groups[0].items) == 0 || groups[0].items[0].scene != "Main" {
		t.Errorf("Scenes should open on Main, got %+v", groups[0].items)
	}

	wantScenes := map[string]bool{"Main": true, "Tube": true, "Chat": true, "Notes": true, "Games": true, "Settings": true}
	gotScenes := map[string]bool{}
	for _, entry := range groups[0].items {
		if entry.scene == "" {
			t.Errorf("Scenes group holds a non-scene row %q", entry.name)
		}
		gotScenes[entry.scene] = true
	}
	for name := range wantScenes {
		if !gotScenes[name] {
			t.Errorf("Scenes group is missing %q", name)
		}
	}
	for _, hidden := range []string{"SettingsAppearance", "ThemeEditor", "About", "Patch", "Exit"} {
		if gotScenes[hidden] {
			t.Errorf("Scenes group lists %q, which belongs elsewhere or nowhere", hidden)
		}
	}
	if gotScenes["Emulators"] {
		t.Error("Scenes group repeats the Emulators action row")
	}

	// Every row in the whole menu opens exactly one thing, and no name repeats
	// across groups (a repeated name would look like a duplicate to the user).
	seen := map[string]bool{}
	for _, group := range groups {
		if len(group.items) == 0 {
			t.Errorf("group %q is empty", group.name)
		}
		for _, entry := range group.items {
			if entry.name == "" {
				t.Errorf("group %q has a nameless row", group.name)
			}
			if seen[entry.name] {
				t.Errorf("row %q appears in more than one place", entry.name)
			}
			seen[entry.name] = true
			if entry.scene == "" && entry.action == nil {
				t.Errorf("row %q has no scene and no action", entry.name)
			}
			if entry.scene != "" && entry.action != nil {
				t.Errorf("row %q has both a scene and an action", entry.name)
			}
			if entry.scene != "" && findSceneIndex(config, entry.scene) < 0 {
				t.Errorf("row %q points at missing scene %q", entry.name, entry.scene)
			}
		}
	}
}

// TestStartMenuNavigationStaysInsideGroup: Down/Up walk the highlighted group,
// the rail changes group and resets the entry cursor, and neither clamps loose.
func TestStartMenuNavigationStaysInsideGroup(t *testing.T) {
	useStartMenuTestState(t)
	config := startMenuTestConfig()
	groups := startMenuGroups(config)

	startMenuNavigate(1, config)
	if tb.cursor != 1 {
		t.Fatalf("Down moved the cursor to %d, want 1", tb.cursor)
	}
	for i := 0; i < len(groups[0].items)+2; i++ {
		startMenuNavigate(1, config)
	}
	// The cursor stops on the last row instead of running past it.
	if want := len(groups[0].items) - 1; tb.cursor != want {
		t.Fatalf("cursor reached %d, want the last row %d", tb.cursor, want)
	}
	for i := 0; i < 5; i++ {
		startMenuNavigate(-1, config)
	}
	if tb.cursor != 0 {
		t.Fatalf("Up left the cursor at %d, want 0", tb.cursor)
	}

	startMenuNavigate(2, config)
	startMenuMoveGroup(1, config)
	if tb.group != 1 {
		t.Fatalf("rail stayed on group %d, want 1", tb.group)
	}
	if tb.cursor != 0 || tb.scroll != 0 {
		t.Fatalf("changing group kept cursor=%d scroll=%d, want 0/0", tb.cursor, tb.scroll)
	}
	for i := 0; i < len(groups)+3; i++ {
		startMenuMoveGroup(1, config)
	}
	if tb.group != len(groups)-1 {
		t.Fatalf("rail ran past the last group: %d", tb.group)
	}
	for i := 0; i < len(groups)+3; i++ {
		startMenuMoveGroup(-1, config)
	}
	if tb.group != 0 {
		t.Fatalf("rail ran before the first group: %d", tb.group)
	}

	// Left parks on the rail without moving the entries; Right returns.
	startMenuNavigate(1, config)
	startMenuFocusRail()
	if tb.focus != 0 || tb.cursor != 1 {
		t.Fatalf("Left gave focus=%d cursor=%d, want 0/1", tb.focus, tb.cursor)
	}
	startMenuFocusItems(config)
	if tb.focus != 1 {
		t.Fatalf("Right left focus at %d, want the entries", tb.focus)
	}
}

// TestStartMenuControllerDrivesMenu: with the menu open the pad walks the list,
// A activates, B closes, and the scene underneath never sees the presses.
func TestStartMenuControllerDrivesMenu(t *testing.T) {
	config := useNavScene(t, makeNavGridScene("StartPadScene", 2, 2))
	useTopChrome(t)
	useCleanControllerOverlays(t)
	isolateSceneSwitch(t)
	config.Scenes = append(config.Scenes,
		SceneConfig{Name: "HubScene", Icon: "H"},
		SceneConfig{Name: "Notes", Icon: "N"},
	)
	useStartMenuTestState(t)

	// A Start press with the menu closed opens it.
	tb.startOpen = false
	if !handleControllerEvent(ctrlButton(sdl.CONTROLLER_BUTTON_START), config) {
		t.Fatal("Start press was not consumed")
	}
	if !tb.startOpen {
		t.Fatal("Start press did not open the menu")
	}

	// Down walks the Scenes group; the event is consumed.
	if !handleControllerEvent(ctrlButton(sdl.CONTROLLER_BUTTON_DPAD_DOWN), config) {
		t.Fatal("Down was not consumed by the menu")
	}
	if tb.cursor != 1 {
		t.Fatalf("Down left the cursor at %d, want 1", tb.cursor)
	}
	// Left parks on the rail, Right comes back to the entries.
	handleControllerEvent(ctrlButton(sdl.CONTROLLER_BUTTON_DPAD_LEFT), config)
	if tb.focus != 0 {
		t.Fatalf("Left left focus at %d, want the rail", tb.focus)
	}
	handleControllerEvent(ctrlButton(sdl.CONTROLLER_BUTTON_DPAD_RIGHT), config)
	if tb.focus != 1 {
		t.Fatalf("Right left focus at %d, want the entries", tb.focus)
	}

	apps := getStartMenuApps(config)
	want := findSceneIndex(config, apps[0].scene)
	if want < 0 {
		t.Fatalf("first menu row %q has no scene", apps[0].scene)
	}
	if want == currentSceneIndex {
		t.Fatalf("the test needs a menu row that leaves the current scene (%q)", apps[0].scene)
	}
	if !handleControllerEvent(ctrlButton(sdl.CONTROLLER_BUTTON_A), config) {
		t.Fatal("A was not consumed by the menu")
	}
	if tb.startOpen {
		t.Fatal("A left the menu open")
	}
	if pendingSceneIndex != want {
		t.Fatalf("A targeted scene %d, want %d", pendingSceneIndex, want)
	}

	// B closes without changing scenes.
	pendingSceneIndex = -1
	tb.startOpen = true
	handleControllerEvent(ctrlButton(sdl.CONTROLLER_BUTTON_B), config)
	if tb.startOpen {
		t.Fatal("B did not close the menu")
	}
	if pendingSceneIndex != -1 {
		t.Fatalf("B switched to scene %d", pendingSceneIndex)
	}
}

// TestStartMenuStartKeyLeavesPauseScenesAlone: the arcade scene binds Start to
// its own pause menu, so the global shortcut must not steal it there.
func TestStartMenuStartKeyLeavesPauseScenesAlone(t *testing.T) {
	useTopChrome(t)
	useCleanControllerOverlays(t)
	useStartMenuTestState(t)
	tb.startOpen = false

	prevScene := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prevScene })

	config := &Config{Scenes: []SceneConfig{{Name: "Games", Icon: "G"}}}
	currentSceneIndex = 0
	if handleControllerEvent(ctrlButton(sdl.CONTROLLER_BUTTON_START), config) {
		t.Fatal("Start was consumed in the arcade scene, which owns it")
	}
	if tb.startOpen {
		t.Fatal("Start opened the menu over the arcade pause menu")
	}

	config.Scenes = []SceneConfig{{Name: "Main", Icon: "H"}}
	if !handleControllerEvent(ctrlButton(sdl.CONTROLLER_BUTTON_START), config) || !tb.startOpen {
		t.Fatal("Start did not open the menu on an ordinary scene")
	}
}

// TestStartMenuKeyboardNavigation: the arrow keys share the controller model,
// so Left/Right reach the rail and Enter activates the highlighted row.
func TestStartMenuKeyboardNavigation(t *testing.T) {
	config := useNavScene(t, makeNavGridScene("StartKeyScene", 2, 2))
	useTopChrome(t)
	isolateSceneSwitch(t)
	config.Scenes = append(config.Scenes,
		SceneConfig{Name: "HubScene", Icon: "H"},
		SceneConfig{Name: "Notes", Icon: "N"},
	)
	useStartMenuTestState(t)

	handleChromeOverlayKey(pressKey(sdl.K_DOWN), config)
	if tb.cursor != 1 {
		t.Fatalf("Down left the cursor at %d, want 1", tb.cursor)
	}
	handleChromeOverlayKey(pressKey(sdl.K_LEFT), config)
	if tb.focus != 0 {
		t.Fatalf("Left left focus at %d, want the rail", tb.focus)
	}
	handleChromeOverlayKey(pressKey(sdl.K_DOWN), config)
	if tb.group != 1 {
		t.Fatalf("Down on the rail selected group %d, want 1", tb.group)
	}
	handleChromeOverlayKey(pressKey(sdl.K_RIGHT), config)
	if tb.focus != 1 || tb.cursor != 0 {
		t.Fatalf("Right gave focus=%d cursor=%d, want 1/0", tb.focus, tb.cursor)
	}
}

// TestStartMenuRailClickSelectsGroup renders the menu and clicks the rendered
// rail rows, so the click geometry is the geometry the user sees.
func TestStartMenuRailClickSelectsGroup(t *testing.T) {
	config := startMenuTestConfig()
	useTopChrome(t)
	useStartMenuTestState(t)
	prevMouseX, prevMouseY := mouseX, mouseY
	t.Cleanup(func() { mouseX, mouseY = prevMouseX, prevMouseY })
	ClearTextCache()
	t.Cleanup(ClearTextCache)
	// The menu reads a measured Apps pill; without one it anchors itself.
	tb.startRect = sdl.Rect{X: 352, Y: 14, W: 64, H: 22}
	tb.enter.cancel()

	newProbeFrame(t) // initialises SDL and the hidden probe window
	withScreen(t, probeW, probeH)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	mouseX, mouseY = -100, -100

	renderStartMenu(renderer, config)
	if len(startMenuGroupHits) < 2 {
		t.Fatalf("menu exposed %d rail rows, want at least 2", len(startMenuGroupHits))
	}
	groups := startMenuGroups(config)
	target := 1
	hit := startMenuHitFor(startMenuGroupHits, target)
	if hit == nil {
		t.Fatalf("no rail hit rect for group %d", target)
	}
	startMenuHandleClick(hit.rect.X+4, hit.rect.Y+4, config)
	if tb.group != target {
		t.Fatalf("clicking rail row %d selected group %d", target, tb.group)
	}
	if tb.focus != 1 {
		t.Errorf("choosing a group should hand the focus to its entries")
	}
	if want := groups[target].items[0].name; want == "" {
		t.Errorf("group %d has no first entry", target)
	}
}

// TestStartMenuFitsCanvas keeps the two-pane card inside both the desktop and
// the compact handheld canvas, with every group and a useful list visible.
func TestStartMenuFitsCanvas(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	useStartMenuTestState(t)
	prevW, prevH := screenWidth, screenHeight
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })

	for _, c := range []struct{ w, h int32 }{{1280, 720}, {960, 720}} {
		screenWidth, screenHeight = c.w, c.h
		menuW, menuH := startMenuSize()
		x, y := startMenuOrigin()
		if menuW <= 0 || menuH <= 0 {
			t.Fatalf("%dx%d: menu has no size (%d,%d)", c.w, c.h, menuW, menuH)
		}
		if x < 0 || y < 0 || x+menuW > c.w || y+menuH > c.h {
			t.Fatalf("%dx%d: menu at (%d,%d) %dx%d leaves the canvas", c.w, c.h, x, y, menuW, menuH)
		}
		groups := startMenuGroups(config)
		if rows := startMenuGroupRows(); rows < len(groups) {
			t.Errorf("%dx%d: rail fits %d of %d groups", c.w, c.h, rows, len(groups))
		}
		// The shipped config must not produce two rows with the same label: a
		// scene and a tool can easily end up describing the same screen.
		seen := map[string]bool{}
		for _, group := range groups {
			for _, entry := range group.items {
				if seen[entry.name] {
					t.Errorf("%dx%d: row %q is listed twice", c.w, c.h, entry.name)
				}
				seen[entry.name] = true
			}
		}
		biggest := 0
		for _, g := range groups {
			if len(g.items) > biggest {
				biggest = len(g.items)
			}
		}
		if rows := startMenuRows(); rows < 8 {
			t.Errorf("%dx%d: list fits only %d rows, the largest group has %d entries",
				c.w, c.h, rows, biggest)
		}
	}
}

// TestStartMenuScreenshotActionCapturesNextFrame: the Device group queues a
// capture instead of screenshotting the menu, and the queued request is spent
// on the next rendered frame.
func TestStartMenuScreenshotActionCapturesNextFrame(t *testing.T) {
	config := startMenuTestConfig()
	useTopChrome(t)
	useStartMenuTestState(t)
	tb.startOpen = false

	prevRequested := screenshotRequested
	t.Cleanup(func() { screenshotRequested = prevRequested })

	entry, ok := startMenuEntryNamed(startMenuGroups(config), "Screenshot")
	if !ok {
		t.Fatal("Device group has no Screenshot row")
	}
	entry.action(config)
	if !screenshotRequested {
		t.Fatal("Screenshot row did not queue a capture")
	}
	if capturePendingScreenshot(nil) {
		t.Fatal("a nil renderer must not claim the capture")
	}
	if !screenshotRequested {
		t.Fatal("the capture was dropped instead of staying queued")
	}

	// A real frame consumes the request and writes one file.
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	} else {
		t.Setenv("HOME", dir)
	}
	prevCapture := lastScreenshotTime
	t.Cleanup(func() { lastScreenshotTime = prevCapture })
	lastScreenshotTime = time.Time{}
	newProbeFrame(t) // initialises SDL and the hidden probe window
	withScreen(t, probeW, probeH)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	renderer.SetDrawColor(10, 20, 30, 255)
	renderer.Clear()
	if !capturePendingScreenshot(renderer) {
		t.Fatal("the queued capture was not taken")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "Pictures", "JukaHub", "*.bmp"))
	if len(matches) != 1 {
		t.Fatalf("screenshot wrote %d files under %s, want 1", len(matches), dir)
	}
	if screenshotRequested {
		t.Fatal("the request survived the frame that used it")
	}
}
