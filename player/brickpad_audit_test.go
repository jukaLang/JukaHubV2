package main

// brickpad_audit_test.go — the Brick's own keys reach and press every control,
// the top bar included.
//
// The menu audit proves a control is wired, in bounds and drawn. The D-pad
// audit proves a config scene's buttons can be highlighted. Neither covers the
// thing the handheld does differently: the TrimUI Brick has no touchscreen, so
// a button that only answers a tap does not exist there. The top bar is drawn on
// every screen, and on a screen whose module binds the same pad buttons (the
// music player's A plays a track, the terminal's arrows scroll) it used to be
// reachable only by tapping it — the bar even drew its focus ring while the
// scene underneath took the press.
//
// These tests walk the real routing (screenPadPress) and the real focus engine
// on the compact canvas the Brick renders, and require, for every shipped
// screen: the bar's buttons are in the graph, none of them holds the focus
// before the user asks for it, Up from a screen with nothing focusable steps
// onto the bar, A on a focused bar button is not stolen by the screen, and Down
// hands the pad back to the screen.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// brickPadProbe is a stand-in for a screen module that binds the pad itself:
// it records the presses it saw and consumes A and the D-pad the way the music
// player and the terminal do.
type brickPadProbe struct {
	sceneBase
	seen      []uint8
	releaseUp bool // hands Up back to the app, like an "at the top" module
}

func (p *brickPadProbe) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	p.seen = append(p.seen, e.Button)
	if e.Button == sdl.CONTROLLER_BUTTON_DPAD_UP && p.releaseUp {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_DPAD_UP, sdl.CONTROLLER_BUTTON_DPAD_DOWN,
		sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return true
	}
	return false
}

func (p *brickPadProbe) saw(button uint8) bool {
	for _, b := range p.seen {
		if b == button {
			return true
		}
	}
	return false
}

// usePadProbeScene installs a module under name for the duration of a test and
// points the package at a config whose only scene is that module's.
func usePadProbeScene(t *testing.T, name string, releaseUp bool) (*Config, *brickPadProbe) {
	t.Helper()
	probe := &brickPadProbe{sceneBase: sceneBase{name: name}, releaseUp: releaseUp}
	sceneRegistry[name] = probe
	prevSceneIdx, prevSel := currentSceneIndex, selectedButtonIndex
	prevEngine := focusEngine
	prevHome := homeLayoutActive
	currentSceneIndex, selectedButtonIndex = 0, -1
	focusEngine = NewFocusEngine()
	homeLayoutActive = false
	t.Cleanup(func() {
		delete(sceneRegistry, name)
		currentSceneIndex, selectedButtonIndex = prevSceneIdx, prevSel
		focusEngine = prevEngine
		homeLayoutActive = prevHome
	})
	return &Config{Scenes: []SceneConfig{{Name: name}}}, probe
}

// padPress sends one Brick button press through the app's real screen routing.
func padPress(config *Config, button uint8) bool {
	return screenPadPress(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: button}, config)
}

// TestBarIsInTheGraphButNeverStartsFocused: a screen with no focusable elements
// keeps the bar's nodes (it is the only Back, clock and bell on the handheld)
// but must not start with one of them focused, because the ring would then say
// the bar owns a press the screen's own module takes.
func TestBarIsInTheGraphButNeverStartsFocused(t *testing.T) {
	withScreen(t, uiCanvasCompactW, uiCanvasCompactH)
	useTopChrome(t)

	empty := SceneConfig{Name: "NoControls"}
	graph := focusEngine.BuildGraph(empty)
	chromeNodes := 0
	for _, n := range graph.Nodes {
		if chromeTier(n.Index) == 1 {
			chromeNodes++
		}
	}
	if chromeNodes < 4 {
		t.Fatalf("a screen with no controls has %d top-bar nodes, want the Back/Menu/Clock/Bell bar", chromeNodes)
	}
	focusEngine.Clear()
	focusEngine.SetGraph(empty.Name, graph)
	if got := focusEngine.ElementIndex(); got != -1 {
		t.Fatalf("focus started on %d; a screen with nothing focusable must start unfocused", got)
	}

	// A screen with controls still starts on its first control, never the bar.
	withControls := makeNavGridScene("WithControls", 2, 2)
	focusEngine.Clear()
	focusEngine.SetGraph(withControls.Name, focusEngine.BuildGraph(withControls))
	if got := focusEngine.ElementIndex(); got != 0 {
		t.Fatalf("focus started on %d, want the first content button", got)
	}
}

// TestUpOnAnUnfocusedScreenStepsOntoTheBar is the route onto the bar for every
// screen that draws no controls of its own: Press Up, and the bar takes the
// focus. The same press must still reach a screen whose module binds Up until
// that screen hands it back.
func TestUpOnAnUnfocusedScreenStepsOntoTheBar(t *testing.T) {
	withScreen(t, uiCanvasCompactW, uiCanvasCompactH)
	useTopChrome(t)

	config, probe := usePadProbeScene(t, "PadAuditAtTop", true)
	focusEngine.SetGraph(config.Scenes[0].Name, focusEngine.BuildGraph(config.Scenes[0]))
	if got := focusEngine.ElementIndex(); got != -1 {
		t.Fatalf("focus started on %d, want none", got)
	}
	if !padPress(config, sdl.CONTROLLER_BUTTON_DPAD_UP) {
		t.Fatal("Up on a screen with nothing focusable was not used by the top bar")
	}
	if tier := chromeTier(focusEngine.ElementIndex()); tier != 1 {
		t.Fatalf("Up focused element %d (tier %d), want a top-bar button", focusEngine.ElementIndex(), tier)
	}
	if selectedButtonIndex != focusEngine.ElementIndex() {
		t.Fatalf("selectedButtonIndex=%d, engine=%d: the render path would draw a different control",
			selectedButtonIndex, focusEngine.ElementIndex())
	}
	if probe.saw(sdl.CONTROLLER_BUTTON_A) {
		t.Fatal("the probe module saw A before the user pressed it")
	}

	// Down hands the pad back to the screen: with nothing below the bar the
	// focus is dropped instead of stranding the ring on a bar button.
	if !padPress(config, sdl.CONTROLLER_BUTTON_DPAD_DOWN) {
		t.Fatal("Down from the bar was not consumed")
	}
	if got := focusEngine.ElementIndex(); got != -1 {
		t.Fatalf("Down from the bar left focus on %d, want none", got)
	}
	if selectedButtonIndex != -1 {
		t.Fatalf("selectedButtonIndex=%d after leaving the bar, want -1", selectedButtonIndex)
	}

	// A now reaches the screen's own module, exactly as before the bar existed.
	if !padPress(config, sdl.CONTROLLER_BUTTON_A) {
		t.Fatal("A was not used by the screen's module after leaving the bar")
	}
	if !probe.saw(sdl.CONTROLLER_BUTTON_A) {
		t.Fatal("the screen's module never received A")
	}
}

// TestBarOutranksTheScreenUnderIt: a focused bar button takes the press before
// the screen's module. A module that also binds A must not play a track (or run
// a command) while the ring sits on the bar's Back button.
func TestBarOutranksTheScreenUnderIt(t *testing.T) {
	withScreen(t, uiCanvasCompactW, uiCanvasCompactH)
	useTopChrome(t)
	prevTB := tb
	t.Cleanup(func() { tb = prevTB; tb.startOpen = false })

	config, probe := usePadProbeScene(t, "PadAuditBusy", false)
	focusEngine.SetGraph(config.Scenes[0].Name, focusEngine.BuildGraph(config.Scenes[0]))
	if !focusEngine.SetByElementIndex(ChromeStartIndex) {
		t.Fatal("cannot focus the Menu pill")
	}
	selectedButtonIndex = ChromeStartIndex

	if !padPress(config, sdl.CONTROLLER_BUTTON_A) {
		t.Fatal("A on the focused Menu pill was not consumed")
	}
	if probe.saw(sdl.CONTROLLER_BUTTON_A) {
		t.Fatal("the screen's module took A while a top-bar button held the focus")
	}
	if !tb.startOpen {
		t.Fatal("A on the focused Menu pill did not open the app menu")
	}
	startMenuDismiss()

	// The Back pill is the control the whole request turns on: pressing it with
	// the pad must press it.
	prevHistory := sceneHistory
	sceneHistory = nil
	t.Cleanup(func() { sceneHistory = prevHistory })
	isolateSceneSwitch(t)
	if !focusEngine.SetByElementIndex(ChromeBackIndex) {
		t.Fatal("cannot focus the Back pill")
	}
	if !padPress(config, sdl.CONTROLLER_BUTTON_A) {
		t.Fatal("A on the focused Back pill was not consumed")
	}
	if probe.saw(sdl.CONTROLLER_BUTTON_A) {
		t.Fatal("the screen's module took A instead of the Back pill going back")
	}
}

// TestModuleScreensStepOntoTheBarAndPressBack runs the real modules that bind
// the pad themselves (the terminal's arrows scroll, the audio player's A plays a
// track, Playtime's arrows scroll the page) through the app's routing: at the
// top of their content they must hand Up back, so Up lands on the bar, and **A**
// on the back pill must leave the screen.
func TestModuleScreensStepOntoTheBarAndPressBack(t *testing.T) {
	withScreen(t, uiCanvasCompactW, uiCanvasCompactH)
	useTopChrome(t)

	prevIdx, prevSel, prevHome := currentSceneIndex, selectedButtonIndex, homeLayoutActive
	prevEngine := focusEngine
	prevHistory := sceneHistory
	prevScroll, prevScrollFor := playtimeScroll, playtimeScrollFor
	prevTermY := term.scrollY
	music.mu.Lock()
	prevCursor, prevTracks := music.cursor, music.tracks
	music.cursor, music.tracks = 0, nil
	music.mu.Unlock()
	t.Cleanup(func() {
		currentSceneIndex, selectedButtonIndex, homeLayoutActive = prevIdx, prevSel, prevHome
		focusEngine, sceneHistory = prevEngine, prevHistory
		playtimeScroll, playtimeScrollFor = prevScroll, prevScrollFor
		term.scrollY = prevTermY
		music.mu.Lock()
		music.cursor, music.tracks = prevCursor, prevTracks
		music.mu.Unlock()
	})
	playtimeScroll, playtimeScrollFor = 0, -2
	term.scrollY = 0

	for _, name := range []string{"Terminal", "MusicPlayer", playtimeSceneName} {
		if _, ok := lookupScene(name); !ok {
			t.Fatalf("%s is not a registered module; the audit would test nothing", name)
		}
		config := &Config{Scenes: []SceneConfig{
			{Name: "Home", Layout: "home"},
			{Name: name},
		}}
		currentSceneIndex, homeLayoutActive = 1, false
		selectedButtonIndex = -1
		focusEngine = NewFocusEngine()
		focusEngine.SetGraph(name, focusEngine.BuildGraph(config.Scenes[1]))
		if got := focusEngine.ElementIndex(); got != -1 {
			t.Fatalf("%s: focus started on %d; the page must start unfocused", name, got)
		}

		// Up at the top of the content belongs to the bar, not the page.
		if !padPress(config, sdl.CONTROLLER_BUTTON_DPAD_UP) {
			t.Fatalf("%s: Up at the top of the page did not reach the top bar", name)
		}
		if tier := chromeTier(focusEngine.ElementIndex()); tier != 1 {
			t.Fatalf("%s: Up focused %d (tier %d), want a top-bar button", name, focusEngine.ElementIndex(), tier)
		}

		// A on the back pill leaves the screen, exactly like B.
		isolateSceneSwitch(t)
		if !focusEngine.SetByElementIndex(ChromeBackIndex) {
			t.Fatalf("%s: cannot focus the back pill", name)
		}
		selectedButtonIndex = ChromeBackIndex
		if !padPress(config, sdl.CONTROLLER_BUTTON_A) {
			t.Fatalf("%s: A on the back pill was not consumed", name)
		}
		if transitionPhase != "fade-out" || pendingSceneIndex != 0 {
			t.Fatalf("%s: A on the back pill did not leave the screen (phase %q, pending %d)",
				name, transitionPhase, pendingSceneIndex)
		}
	}
}

// TestEveryShippedScreenKeepsItsTopBarOnTheBrick walks both shipped configs on
// the compact Brick canvas with the real bar measured for each screen, and
// requires every bar button that screen draws to be reachable with the pad:
// straight from the content grid where the screen has buttons, and through the
// Up entry where it has none.
func TestEveryShippedScreenKeepsItsTopBarOnTheBrick(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	withScreen(t, uiCanvasCompactW, uiCanvasCompactH)
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)

	prevSceneIdx, prevSel, prevHome := currentSceneIndex, selectedButtonIndex, homeLayoutActive
	prevEngine := focusEngine
	t.Cleanup(func() {
		currentSceneIndex, selectedButtonIndex, homeLayoutActive = prevSceneIdx, prevSel, prevHome
		focusEngine = prevEngine
		ClearTextCache()
	})

	checked := 0
	for name, config := range loadAuditConfigs(t) {
		applyUIProfileForDevice(config, DeviceTrimuiBrick)
		startupInjectScenes(config)
		for i, scene := range config.Scenes {
			if scene.Layout == "home" {
				// Home draws its own focusable header chips and has no Back.
				continue
			}
			currentSceneIndex, homeLayoutActive = i, false
			ClearTextCache()
			renderStatusBar(renderer, config)

			focusEngine = NewFocusEngine()
			graph := focusEngine.BuildGraph(scene)
			want := map[int]bool{}
			for _, n := range graph.Nodes {
				if chromeTier(n.Index) == 1 {
					want[n.Index] = true
				}
			}
			if len(want) == 0 {
				t.Fatalf("%s / %s: the bar drew no focusable button for this screen", name, scene.Name)
			}
			focusEngine.SetGraph(scene.Name, graph)
			if focusEngine.ElementIndex() < 0 {
				// Nothing focusable of its own: the bar is entered with Up.
				if !focusChromeBar() {
					t.Fatalf("%s / %s: Up could not focus the top bar", name, scene.Name)
				}
			}

			reached := walkBarFromFocus(config)
			for index := range want {
				if !reached[index] {
					t.Errorf("%s / %s: the top-bar button %s (%d) cannot be reached with the Brick's keys",
						name, scene.Name, chromeLabel(index), index)
				}
			}
			checked++
		}
	}
	// A handful of scenes would mean the walk stopped early, not that the app is
	// fine: both configs ship dozens of screens.
	if checked < 40 {
		t.Fatalf("only %d screens were walked; the audit is not covering the shipped configs", checked)
	}
}

// walkBarFromFocus is a breadth-first walk over the real moveSelection handler,
// started wherever the focus sits, recording every node the pad can reach.
func walkBarFromFocus(config *Config) map[int]bool {
	reached := map[int]bool{}
	// -1 is "nothing focused"; the bar's own sentinels are negative too, so
	// they must not be mistaken for it.
	start := focusEngine.ElementIndex()
	if start == -1 {
		return reached
	}
	reached[start] = true
	queue := []int{start}
	actions := []Action{ActionNavigateUp, ActionNavigateDown, ActionNavigateLeft, ActionNavigateRight}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, action := range actions {
			if !focusEngine.SetByElementIndex(cur) {
				continue
			}
			moveSelection(config, action)
			next := selectedButtonIndex
			if next == cur || reached[next] {
				continue
			}
			reached[next] = true
			queue = append(queue, next)
		}
	}
	return reached
}

// chromeLabel names a top-bar sentinel for a failure message.
func chromeLabel(index int) string {
	switch index {
	case ChromeBackIndex:
		return "Back"
	case ChromeStartIndex:
		return "Menu"
	case ChromeClockIndex:
		return "Clock"
	case ChromeWeatherIndex:
		return "Weather"
	case ChromeWiFiIndex:
		return "Wi-Fi"
	case ChromeBellIndex:
		return "Notifications"
	case ChromeMediaIndex:
		return "Now playing"
	case ChromeUpdateIndex:
		return "Update"
	}
	return "unknown"
}
