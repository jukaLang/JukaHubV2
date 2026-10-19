package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// isolateSceneSwitch resets the fade-transition globals so a test can switch
// scenes without a render loop to complete the fade, and restores them after.
func isolateSceneSwitch(t *testing.T) {
	t.Helper()
	prevPhase, prevPending := transitionPhase, pendingSceneIndex
	transitionPhase, pendingSceneIndex = "none", 0
	t.Cleanup(func() {
		transitionPhase, pendingSceneIndex = prevPhase, prevPending
	})
}

// useChromeBar measures a plausible top bar so BuildGraph includes the back
// pill and status-clock chrome nodes. Must run after useNavScene (which zeroes
// the rects for deterministic element-only navigation tests). The Apps pill and
// bell are cleared so older two-button expectations stay exact.
func useChromeBar(t *testing.T) {
	t.Helper()
	sceneTitleBackRect = sdl.Rect{X: 300, Y: 14, W: 44, H: 22}
	statusClockRect = sdl.Rect{X: 1100, Y: 10, W: 64, H: 28}
	prevStart, prevBell := tb.startRect, nc.bellRect
	tb.startRect, nc.bellRect = sdl.Rect{}, sdl.Rect{}
	t.Cleanup(func() { tb.startRect, nc.bellRect = prevStart, prevBell })
}

// useTopChrome measures the complete top bar: back pill, Apps pill, status
// clock and notification bell, and clears any overlay state left by another
// test.
func useTopChrome(t *testing.T) {
	t.Helper()
	prevTB, prevNC := tb, nc
	prevBack, prevClock := sceneTitleBackRect, statusClockRect
	sceneTitleBackRect = sdl.Rect{X: 300, Y: 14, W: 44, H: 22}
	tb.startRect = sdl.Rect{X: 352, Y: 14, W: 64, H: 22}
	statusClockRect = sdl.Rect{X: 1100, Y: 10, W: 64, H: 28}
	nc = notifCenter{}
	nc.bellRect = sdl.Rect{X: 1200, Y: 10, W: 44, H: 28}
	tb.startOpen = false
	t.Cleanup(func() {
		tb, nc = prevTB, prevNC
		sceneTitleBackRect, statusClockRect = prevBack, prevClock
	})
}

// installGraph focuses the given element of the scene so moveSelection starts
// the walk there.
func installGraph(t *testing.T, config *Config, start int) {
	t.Helper()
	focusEngine.Clear()
	focusEngine.SetGraph(config.Scenes[0].Name, focusEngine.BuildGraph(config.Scenes[0]))
	if !focusEngine.SetByElementIndex(start) {
		t.Fatalf("cannot focus element %d", start)
	}
	selectedButtonIndex = start
}

// TestChromeButtonsFocusableWithoutHijackingGrid covers the top-bar back pill
// and status clock: Up from the top grid row steps onto the bar, Down steps
// back, and Left/Right along rows must not jump up to the bar.
func TestChromeButtonsFocusableWithoutHijackingGrid(t *testing.T) {
	config := useNavScene(t, makeNavGridScene("ChromeScene", 3, 3))
	useChromeBar(t)

	// Right along the top row still moves along the row.
	installGraph(t, config, 0)
	moveSelection(config, ActionNavigateRight)
	if selectedButtonIndex != 1 {
		t.Fatalf("Right from the first button selected %d, want 1 (chrome must not hijack rows)", selectedButtonIndex)
	}

	// Up from the top-left reaches the back pill.
	installGraph(t, config, 0)
	moveSelection(config, ActionNavigateUp)
	if selectedButtonIndex != ChromeBackIndex {
		t.Fatalf("Up from the top-left button selected %d, want the back pill (%d)", selectedButtonIndex, ChromeBackIndex)
	}

	// Up from the top-right reaches the status clock.
	installGraph(t, config, 2)
	moveSelection(config, ActionNavigateUp)
	if selectedButtonIndex != ChromeClockIndex {
		t.Fatalf("Up from the top-right button selected %d, want the clock (%d)", selectedButtonIndex, ChromeClockIndex)
	}

	// Down from the bar steps back into the grid.
	moveSelection(config, ActionNavigateDown)
	if selectedButtonIndex < 0 {
		t.Fatalf("Down from the clock selected %d, want a grid button", selectedButtonIndex)
	}

	// Left/Right on the bar moves between the two bar buttons.
	installGraph(t, config, 0)
	moveSelection(config, ActionNavigateUp)
	moveSelection(config, ActionNavigateRight)
	if selectedButtonIndex != ChromeClockIndex {
		t.Fatalf("Right from the back pill selected %d, want the clock", selectedButtonIndex)
	}

	// Home scenes keep their own clock card: no chrome nodes join the graph.
	home := makeNavGridScene("Homeish", 3, 3)
	home.Layout = "home"
	if n := len(focusEngine.BuildGraph(home).Nodes); n != 9 {
		t.Fatalf("home graph has %d nodes, want 9 (no chrome)", n)
	}
}

// TestHandleChromeKeyOwnsInputWhileChromeFocused: while a bar button holds the
// focus the key handler consumes input instead of letting the element dispatch
// steal the focus back.
func TestHandleChromeKeyOwnsInputWhileChromeFocused(t *testing.T) {
	config := useNavScene(t, makeNavGridScene("ChromeKeys", 2, 2))
	useChromeBar(t)
	homeLayoutActive = false

	installGraph(t, config, 0)
	if !focusEngine.SetByElementIndex(ChromeBackIndex) {
		t.Fatal("cannot focus the back pill")
	}
	e := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_DOWN}}
	if !handleChromeKey(e, config) {
		t.Fatal("chrome handler did not consume a key while the pill was focused")
	}
	if selectedButtonIndex < 0 {
		t.Fatalf("Down from chrome selected %d, want a grid button", selectedButtonIndex)
	}
	if handleChromeKey(e, config) {
		t.Fatal("chrome handler consumed a key without chrome focus")
	}
}

// TestChromeBarClick: tapping the back pill or the status clock behaves like
// pressing the focused button (the TrimUI Smart Pro is a touch device).
func TestChromeBarClick(t *testing.T) {
	config := useNavScene(t, makeNavGridScene("ClickScene", 2, 2))
	useChromeBar(t)
	isolateSceneSwitch(t)

	// Clicking the clock opens the Alarm Clock UI.
	if !handleChromeBarClick(statusClockRect.X+2, statusClockRect.Y+2, config) {
		t.Fatal("click on the status clock was not consumed")
	}
	idx := sceneIndexExact(config, "Alarm")
	if idx < 0 || (currentSceneIndex != idx && pendingSceneIndex != idx) {
		t.Fatalf("clock click targeted current=%d pending=%d, want Alarm scene %d", currentSceneIndex, pendingSceneIndex, idx)
	}

	// Clicking the pill is consumed as back.
	if !handleChromeBarClick(sceneTitleBackRect.X+2, sceneTitleBackRect.Y+2, config) {
		t.Fatal("click on the back pill was not consumed")
	}

	// A click in the content area hits nothing.
	if handleChromeBarClick(640, 400, config) {
		t.Fatal("click in the content area was consumed by chrome")
	}
}

// pressKey sends one synthetic key through the taskbar overlay router.
func pressKey(sym sdl.Keycode) *sdl.KeyboardEvent {
	return &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}}
}

// TestTopBarButtonsFocusableWithoutHijackingGrid covers the Apps pill and
// notification bell now that they live in the top bar: Up from the top grid
// row steps onto the bar, Down steps back, Left/Right walks the bar in screen
// order, and the bar must not pull focus off a grid row in the middle.
func TestTopBarButtonsFocusableWithoutHijackingGrid(t *testing.T) {
	config := useNavScene(t, makeNavGridScene("TopBarScene", 3, 3))
	useTopChrome(t)

	// Down inside the grid still moves one row down.
	installGraph(t, config, 0)
	moveSelection(config, ActionNavigateDown)
	if selectedButtonIndex != 3 {
		t.Fatalf("Down from the top row selected %d, want 3 (top bar must not hijack rows)", selectedButtonIndex)
	}
	moveSelection(config, ActionNavigateDown)
	if selectedButtonIndex != 6 {
		t.Fatalf("second Down selected %d, want 6", selectedButtonIndex)
	}
	// Down at the last row has no bar below it any more: focus stays put.
	moveSelection(config, ActionNavigateDown)
	if chromeTier(selectedButtonIndex) != 0 {
		t.Fatalf("Down from the bottom row selected %d, want to stay in the content", selectedButtonIndex)
	}

	// Up from the top row lands on the top bar.
	installGraph(t, config, 0)
	moveSelection(config, ActionNavigateUp)
	if chromeTier(selectedButtonIndex) != 1 {
		t.Fatalf("Up from the top row selected %d, want a top-bar button", selectedButtonIndex)
	}

	// Down from the top bar returns to the content grid.
	moveSelection(config, ActionNavigateDown)
	if selectedButtonIndex < 0 || chromeTier(selectedButtonIndex) != 0 {
		t.Fatalf("Down from the top bar selected %d, want a grid button", selectedButtonIndex)
	}

	// Left/Right on the bar walks back → Apps → clock → bell by screen order.
	focusEngine.SetGraph(config.Scenes[0].Name, focusEngine.BuildGraph(config.Scenes[0]))
	if !focusEngine.SetByElementIndex(ChromeStartIndex) {
		t.Fatal("cannot focus the Apps pill")
	}
	selectedButtonIndex = ChromeStartIndex
	moveSelection(config, ActionNavigateRight)
	if selectedButtonIndex != ChromeClockIndex {
		t.Fatalf("Right from Apps selected %d, want the clock (%d)", selectedButtonIndex, ChromeClockIndex)
	}
	moveSelection(config, ActionNavigateRight)
	if selectedButtonIndex != ChromeBellIndex {
		t.Fatalf("Right from the clock selected %d, want the bell (%d)", selectedButtonIndex, ChromeBellIndex)
	}
	moveSelection(config, ActionNavigateLeft)
	if selectedButtonIndex != ChromeClockIndex {
		t.Fatalf("Left from the bell selected %d, want the clock (%d)", selectedButtonIndex, ChromeClockIndex)
	}
}

// TestTopBarChromeOnEveryMenuScene: every menu scene gets the same four
// top-bar buttons; the home screen keeps its own focus model.
func TestTopBarChromeOnEveryMenuScene(t *testing.T) {
	useTopChrome(t)
	for _, name := range []string{"Calculator", "Misc", "Settings"} {
		scene := makeNavGridScene(name, 2, 2)
		if n := len(focusEngine.BuildGraph(scene).Nodes); n != 8 {
			t.Fatalf("%s graph has %d nodes, want 8 (4 buttons + 4 top-bar)", name, n)
		}
	}
	home := makeNavGridScene("Homeish", 2, 2)
	home.Layout = "home"
	if n := len(focusEngine.BuildGraph(home).Nodes); n != 4 {
		t.Fatalf("home graph has %d nodes, want 4 (no top-bar nodes)", n)
	}
}

// TestHandleChromeKeyActivatesTopBarButtons: A/Enter on the focused Apps pill
// toggles the app menu, Enter on the bell toggles notification center, and
// B/Escape closes whichever overlay is open.
func TestHandleChromeKeyActivatesTopBarButtons(t *testing.T) {
	config := useNavScene(t, makeNavGridScene("TaskbarKeys", 2, 2))
	useTopChrome(t)
	homeLayoutActive = false

	focusChrome := func(idx int) {
		focusEngine.SetGraph(config.Scenes[0].Name, focusEngine.BuildGraph(config.Scenes[0]))
		if !focusEngine.SetByElementIndex(idx) {
			t.Fatalf("cannot focus chrome %d", idx)
		}
		selectedButtonIndex = idx
	}

	focusChrome(ChromeStartIndex)
	if !handleChromeKey(pressKey(sdl.K_RETURN), config) {
		t.Fatal("chrome handler did not consume Enter on Start")
	}
	if !tb.startOpen {
		t.Fatal("Enter on the Start button did not open the app menu")
	}
	if !handleChromeKey(pressKey(sdl.K_RETURN), config) || tb.startOpen {
		t.Fatal("Enter on Start did not close the app menu again")
	}

	focusChrome(ChromeBellIndex)
	if !handleChromeKey(pressKey(sdl.K_RETURN), config) {
		t.Fatal("chrome handler did not consume Enter on the bell")
	}
	if !nc.open {
		t.Fatal("Enter on the bell did not open the notification center")
	}
	if !handleChromeKey(pressKey(sdl.K_ESCAPE), config) || nc.open {
		t.Fatal("Escape on the bell did not close the notification center")
	}
}

// TestTaskbarOverlayKeyDrivesStartMenu: once the Start menu is open the D-pad
// scrolls its rows and A opens the focused app, so the taskbar is usable
// without a mouse.
func TestTaskbarOverlayKeyDrivesStartMenu(t *testing.T) {
	config := useNavScene(t, makeNavGridScene("TaskbarScene", 2, 2))
	useTopChrome(t)
	isolateSceneSwitch(t)
	config.Scenes = append(config.Scenes,
		SceneConfig{Name: "Misc", Icon: "T"},
		SceneConfig{Name: "Notes", Icon: "N"},
	)

	focusEngine.SetGraph(config.Scenes[0].Name, focusEngine.BuildGraph(config.Scenes[0]))
	if !focusEngine.SetByElementIndex(ChromeStartIndex) {
		t.Fatal("cannot focus the Start button")
	}
	selectedButtonIndex = ChromeStartIndex
	activateChromeFocus(config)
	if !tb.startOpen {
		t.Fatal("activating Start did not open the menu")
	}

	apps := getStartMenuApps(config)
	if len(apps) < 2 {
		t.Fatalf("start menu has %d apps, want at least 2", len(apps))
	}
	if !handleChromeOverlayKey(pressKey(sdl.K_DOWN), config) {
		t.Fatal("open Start menu did not consume Down")
	}
	if tb.cursor != 1 {
		t.Fatalf("Down in the Start menu moved cursor to %d, want 1", tb.cursor)
	}
	if !handleChromeOverlayKey(pressKey(sdl.K_UP), config) || tb.cursor != 0 {
		t.Fatalf("Up in the Start menu left cursor at %d, want 0", tb.cursor)
	}

	wantScene := apps[0].scene
	target := findSceneIndex(config, wantScene)
	if target < 0 {
		t.Fatalf("first menu entry %q has no scene", wantScene)
	}
	if !handleChromeOverlayKey(pressKey(sdl.K_RETURN), config) {
		t.Fatal("open Start menu did not consume Enter")
	}
	if tb.startOpen {
		t.Fatal("Enter left the Start menu open")
	}
	if pendingSceneIndex != target {
		t.Fatalf("Enter targeted scene %d, want %d", pendingSceneIndex, target)
	}

	// Back closes the menu without switching scenes.
	tb.startOpen = true
	if !handleChromeOverlayKey(pressKey(sdl.K_ESCAPE), config) || tb.startOpen {
		t.Fatal("Escape did not close the Start menu")
	}
	if handleChromeOverlayKey(pressKey(sdl.K_DOWN), config) {
		t.Fatal("overlay router consumed input with no overlay open")
	}
}

// TestTaskbarOverlayKeyDrivesNotifCenter: with the notification center open the
// D-pad moves the row cursor and Back closes it; the two taskbar overlays stay
// mutually exclusive.
func TestTaskbarOverlayKeyDrivesNotifCenter(t *testing.T) {
	config := useNavScene(t, makeNavGridScene("TaskbarScene", 2, 2))
	useTopChrome(t)
	notifPush("one", notifInfo)
	notifPush("two", notifSuccess)
	nc.history[0].Read, nc.history[1].Read = false, false
	if notifUnreadCount() != 2 {
		t.Fatalf("unread count %d, want 2", notifUnreadCount())
	}

	focusEngine.SetGraph(config.Scenes[0].Name, focusEngine.BuildGraph(config.Scenes[0]))
	if !focusEngine.SetByElementIndex(ChromeBellIndex) {
		t.Fatal("cannot focus the bell")
	}
	selectedButtonIndex = ChromeBellIndex
	activateChromeFocus(config)
	if !nc.open {
		t.Fatal("activating the bell did not open the notification center")
	}
	if notifUnreadCount() != 0 {
		t.Fatalf("opening the bell left %d unread, want 0", notifUnreadCount())
	}
	if !handleChromeOverlayKey(pressKey(sdl.K_DOWN), config) || nc.cursor != 1 {
		t.Fatalf("Down in the notification center left cursor at %d, want 1", nc.cursor)
	}
	if !handleChromeOverlayKey(pressKey(sdl.K_UP), config) || nc.cursor != 0 {
		t.Fatalf("Up in the notification center left cursor at %d, want 0", nc.cursor)
	}
	if !handleChromeOverlayKey(pressKey(sdl.K_ESCAPE), config) || nc.open {
		t.Fatal("Escape did not close the notification center")
	}

	// Opening the Start menu closes the notification center and vice versa.
	notifToggle()
	if !nc.open {
		t.Fatal("notification center did not open")
	}
	startMenuToggle()
	if !tb.startOpen || nc.open {
		t.Fatalf("startOpen=%v nc.open=%v, want the Start menu open and the notification center closed", tb.startOpen, nc.open)
	}
	notifToggle()
	if nc.open && tb.startOpen {
		t.Fatal("both taskbar overlays are open at once")
	}
}

// TestSetGraphPreservesFocusOnReinstall is the engine-level trap guard: key
// handlers rebuild the graph on every press, which used to reset focus to the
// first node and trap navigation on the first buttons.
func TestSetGraphPreservesFocusOnReinstall(t *testing.T) {
	scene := makeNavGridScene("PreserveScene", 3, 3)
	fe := NewFocusEngine()
	fe.SetGraph(scene.Name, fe.BuildGraph(scene))
	if got := fe.ElementIndex(); got != 0 {
		t.Fatalf("initial focus %d, want 0", got)
	}
	fe.Navigate(ActionNavigateRight)
	// Reinstalling the same scene's graph must keep the user's place.
	fe.SetGraph(scene.Name, fe.BuildGraph(scene))
	if got := fe.ElementIndex(); got != 1 {
		t.Fatalf("focus reset to %d on graph reinstall, want 1", got)
	}
	// A different scene starts at its own first node.
	other := makeNavGridScene("OtherScene", 2, 2)
	fe.SetGraph(other.Name, fe.BuildGraph(other))
	if got := fe.ElementIndex(); got != 0 {
		t.Fatalf("scene change focused %d, want 0", got)
	}
}

// TestAlarmSceneGetsAnEntryPoint: the alarm UI renders through the scene
// pipeline but no shipped config defines the scene, so ensureAlarmScene gives
// it one in-memory and openAlarmScene reaches it.
func TestAlarmSceneGetsAnEntryPoint(t *testing.T) {
	prevIdx := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prevIdx })
	isolateSceneSwitch(t)

	config := &Config{Scenes: []SceneConfig{{Name: "Main", Layout: "home"}}}
	ensureAlarmScene(config)
	idx := sceneIndexExact(config, "Alarm")
	if idx < 0 {
		t.Fatal("ensureAlarmScene did not add the Alarm scene")
	}
	ensureAlarmScene(config)
	if len(config.Scenes) != 2 {
		t.Fatalf("ensureAlarmScene is not idempotent: %d scenes", len(config.Scenes))
	}
	if !openAlarmScene(config) {
		t.Fatal("openAlarmScene failed")
	}
	// The scene switch fades: accept the target as either the live scene or
	// the pending one the render loop will complete.
	if currentSceneIndex != idx && pendingSceneIndex != idx {
		t.Fatalf("openAlarmScene targeted current=%d pending=%d, want %d", currentSceneIndex, pendingSceneIndex, idx)
	}
}
