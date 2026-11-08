package main

// Touch input used to be decoded as swipes only: a still tap produced no action
// at all, so on the handheld the top-bar back pill (and every other on-screen
// control) ignored the finger. These tests pin the tap-to-click dispatch and the
// back-nav reachability of the Logs scene that reported it.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// stubTaps replaces the tap sink for one test and returns a pointer to the
// recorded points.
func stubTaps(t *testing.T) *[]sdl.Point {
	t.Helper()
	got := &[]sdl.Point{}
	prev := touchTapSink
	touchTapSink = func(x, y int32) { *got = append(*got, sdl.Point{X: x, Y: y}) }
	t.Cleanup(func() { touchTapSink = prev })
	return got
}

func touchFinger(t *testing.T, typ uint32, nx, ny float32) *sdl.TouchFingerEvent {
	t.Helper()
	return &sdl.TouchFingerEvent{Type: typ, X: nx, Y: ny, FingerID: 1}
}

func TestTouchTapBecomesClick(t *testing.T) {
	got := stubTaps(t)
	withScreen(t, 1280, 720)

	cfg := &Config{Scenes: []SceneConfig{{Name: "Logs"}}}
	handleTouchGesture(touchFinger(t, sdl.FINGERDOWN, 0.25, 0.5), cfg)
	// A finger always drifts a pixel or two; anything under touchTapMaxDrift
	// stays a tap.
	handleTouchGesture(touchFinger(t, sdl.FINGERUP, 0.251, 0.5), cfg)

	if len(*got) != 1 {
		t.Fatalf("a still tap dispatched %d clicks, want 1", len(*got))
	}
	if p := (*got)[0]; p.X < 320 || p.X > 324 || p.Y != 360 {
		t.Fatalf("tap at (0.25, 0.5) dispatched at %v, want about (321,360)", p)
	}
}

func TestTouchSwipeIsNotAClick(t *testing.T) {
	got := stubTaps(t)
	withScreen(t, 1280, 720)

	prevSceneIdx := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prevSceneIdx })
	cfg := &Config{Scenes: []SceneConfig{{Name: "Logs"}, {Name: "Other"}}}

	// Left swipe across a third of the screen.
	handleTouchGesture(touchFinger(t, sdl.FINGERDOWN, 0.7, 0.5), cfg)
	handleTouchGesture(touchFinger(t, sdl.FINGERUP, 0.35, 0.5), cfg)

	if len(*got) != 0 {
		t.Fatalf("a swipe dispatched %d clicks, want 0", len(*got))
	}
}

// TestSyntheticTouchMouseEventsDisabled pins the input-level fix for the
// doubled video-card tap: SDL's built-in touch-to-mouse mapping must be off,
// otherwise a finger tap fires SDL's own mouse press at finger-down plus the
// app's replayed one at finger-up (two yt-dlp resolves for one card touch,
// two characters per virtual-keyboard tap).
func TestSyntheticTouchMouseEventsDisabled(t *testing.T) {
	disableSyntheticTouchMouse()
	if got := sdl.GetHint("SDL_TOUCH_MOUSE_EVENTS"); got != "0" {
		t.Fatalf("SDL_TOUCH_MOUSE_EVENTS = %q, want \"0\" so a touch tap is not\n"+
			"\talso handed to the mouse pipeline by SDL itself", got)
	}
}

// TestTouchPixelCoordinatesAccepted covers backends that report touch in pixels
// rather than SDL's normalized 0..1 range.
func TestTouchPixelCoordinatesAccepted(t *testing.T) {
	got := stubTaps(t)
	withScreen(t, 1280, 720)

	cfg := &Config{}
	handleTouchGesture(touchFinger(t, sdl.FINGERDOWN, 640, 300), cfg)
	handleTouchGesture(touchFinger(t, sdl.FINGERUP, 640, 300), cfg)

	if len(*got) != 1 {
		t.Fatalf("pixel-space tap dispatched %d clicks, want 1", len(*got))
	}
	if p := (*got)[0]; p.X != 640 || p.Y != 300 {
		t.Fatalf("pixel tap dispatched at %v, want (640,300)", p)
	}
}

// clearChromeRects forgets every measured top-bar button. The focus graph is
// built from the rects the last frame recorded, so a test that hand-builds a
// graph must start from an empty bar: a neighbouring test's render otherwise
// leaves its Apps pill (or clock, bell, Wi-Fi meter) in this test's graph,
// where the D-pad can land on it instead of the back pill.
func clearChromeRects() {
	sceneTitleBackRect = sdl.Rect{}
	statusClockRect = sdl.Rect{}
	tb.startRect = sdl.Rect{}
	statusWiFiRect = sdl.Rect{}
	weatherStatusRect = sdl.Rect{}
	statusMediaRect = sdl.Rect{}
	nc.bellRect = sdl.Rect{}
	updatePrompt.badge = sdl.Rect{}
}

// TestLogsBackPillIsSelectableAndLeaves drives the real Logs scene from
// jukaconfig.json. The screen draws its own controls (systool_logs.go), so its
// config carries none and nothing starts focused; Up from the top of its list
// must focus the top-bar back pill, and Enter there must start a transition
// back to the previous scene.
func TestLogsBackPillIsSelectableAndLeaves(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	idx := sceneIndexExact(config, "LogExporter")
	if idx < 0 {
		t.Fatal("LogExporter (Logs) scene not found")
	}
	scene := config.Scenes[idx]

	cfg := useNavScene(t, scene)
	prevHomeActive := homeLayoutActive
	homeLayoutActive = false
	t.Cleanup(func() { homeLayoutActive = prevHomeActive })
	clearChromeRects()
	t.Cleanup(clearChromeRects)
	prevLogs := sysToolLogs
	t.Cleanup(func() { sysToolLogs = prevLogs })
	sysToolLogs.view, sysToolLogs.cursor, sysToolLogs.sheet = nil, 0, false
	sceneTitleBackRect = sdl.Rect{X: 300, Y: 17, W: 30, H: 22}
	statusClockRect = sdl.Rect{X: 1150, Y: 10, W: 100, H: 36}
	focusEngine.SetGraph(scene.Name, focusEngine.BuildGraph(scene))
	if got := focusEngine.ElementIndex(); got != -1 {
		t.Fatalf("the Logs screen started with element %d focused; its config must carry no controls", got)
	}

	// Up with nothing above it belongs to the bar, and lands on the back pill.
	m, ok := lookupScene(scene.Name)
	if !ok {
		t.Fatal("the Logs screen is not a registered module")
	}
	if !m.handleKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_UP}}, cfg) {
		t.Fatal("Up at the top of the Logs list was not handled")
	}
	if got := focusEngine.ElementIndex(); got != ChromeBackIndex {
		t.Fatalf("Up from the top of the Logs list focused element %d, want the back pill (%d)", got, ChromeBackIndex)
	} // Enter on the pill is owned by the chrome handler and starts the exit.
	// A second scene gives the back navigation somewhere to go.
	cfg.Scenes = append(cfg.Scenes, SceneConfig{Name: "Misc"})
	prevPending, prevPhase := pendingSceneIndex, transitionPhase
	pendingSceneIndex, transitionPhase = -1, "none"
	t.Cleanup(func() { pendingSceneIndex, transitionPhase = prevPending, prevPhase })

	pushSceneHistory(1) // pretend Logs was opened from scene 1
	handled := handleChromeKey(&sdl.KeyboardEvent{
		Type:   sdl.KEYDOWN,
		Keysym: sdl.Keysym{Sym: sdl.K_RETURN},
	}, cfg)
	if !handled {
		t.Fatal("chrome bar did not consume Enter")
	}
	if pendingSceneIndex != 1 {
		t.Fatalf("Enter on the back pill targeted scene %d, want 1", pendingSceneIndex)
	}
}
