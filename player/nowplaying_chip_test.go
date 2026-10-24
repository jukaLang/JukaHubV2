package main

// Tests for the now-playing chip as a button: what it opens, how the status bar
// and the home header measure it, and how the D-pad, A and a tap reach it.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// nowPlayingMusicConfig pins a playing track and returns a config with a Main
// scene, ready for a scene switch.
func nowPlayingMusicConfig(t *testing.T) *Config {
	t.Helper()
	pinMusicState(t, "Low Tide Radio")
	pinVideoState(t, "idle", "")
	isolateSceneSwitch(t)
	prevIndex := currentSceneIndex
	currentSceneIndex = 0
	t.Cleanup(func() { currentSceneIndex = prevIndex })
	return audioTestConfig()
}

func TestNowPlayingChipOpensMusicPlayer(t *testing.T) {
	config := nowPlayingMusicConfig(t)

	if !openNowPlaying(config) {
		t.Fatal("a playing track did not open the audio player")
	}
	landSceneTransition()
	if got := config.Scenes[currentSceneIndex].Name; got != "MusicPlayer" {
		t.Fatalf("landed on %q, want MusicPlayer", got)
	}
}

// The chip is a chrome button on every scene: A while it is focused opens the
// player, exactly like a click does.
func TestNowPlayingChromeActivationOpensMusicPlayer(t *testing.T) {
	config := nowPlayingMusicConfig(t)
	prevRect := statusMediaRect
	statusMediaRect = sdl.Rect{X: 980, Y: 8, W: 140, H: 32}
	t.Cleanup(func() { statusMediaRect = prevRect })

	focusEngine.Clear()
	focusEngine.SetGraph("Main", focusEngine.BuildGraph(SceneConfig{Name: "Main"}))
	if !focusEngine.SetByElementIndex(ChromeMediaIndex) {
		t.Fatal("the now-playing chip did not join the chrome focus graph")
	}
	if !activateChromeFocus(config) {
		t.Fatal("A on the chip was not consumed")
	}
	landSceneTransition()
	if got := config.Scenes[currentSceneIndex].Name; got != "MusicPlayer" {
		t.Fatalf("landed on %q, want MusicPlayer", got)
	}
}

func TestNowPlayingChipClickOpensPlayer(t *testing.T) {
	config := nowPlayingMusicConfig(t)
	prevRect := statusMediaRect
	statusMediaRect = sdl.Rect{X: 980, Y: 8, W: 140, H: 32}
	t.Cleanup(func() { statusMediaRect = prevRect })

	if !handleChromeBarClick(statusMediaRect.X+4, statusMediaRect.Y+4, config) {
		t.Fatal("the chip swallowed the click without acting")
	}
	landSceneTransition()
	if got := config.Scenes[currentSceneIndex].Name; got != "MusicPlayer" {
		t.Fatalf("landed on %q, want MusicPlayer", got)
	}
}

// A floated video goes back to its owner scene, not to the audio player.
func TestNowPlayingChipReturnsToTheFloatingVideo(t *testing.T) {
	config := setupVideoMiniTest(t, miniTestTube)
	pinVideoState(t, "playing", "https://videos.example/watch/abc")
	isolateSceneSwitch(t)

	currentSceneIndex = miniTestHome
	videoMiniShow()
	if !videoMiniActive() {
		t.Fatal("the video was not floated")
	}
	if !openNowPlaying(config) {
		t.Fatal("a floating video did not open its scene")
	}
	landSceneTransition()
	if currentSceneIndex != miniTestTube {
		t.Fatalf("landed on scene %d, want the video owner %d", currentSceneIndex, miniTestTube)
	}
	if videoMiniActive() {
		t.Error("the video stayed in its window after returning to the player")
	}

	// Already on the owner scene: the press is consumed and nothing moves.
	pendingSceneIndex = -1
	transitionPhase = "none"
	if !openNowPlaying(config) {
		t.Error("a video on screen was not reported as playing")
	}
	if pendingSceneIndex != -1 {
		t.Errorf("a press on the owner scene started a switch to %d", pendingSceneIndex)
	}
}

// The status bar measures the chip, and it sits right of the clock, inside the
// bar. Rendering is what makes the click and the focus node possible.
func TestNowPlayingChipIsMeasuredInTheStatusBar(t *testing.T) {
	pinStatusValues(t, 87, 3)
	pinMusicState(t, "Low Tide Radio")
	pinVideoState(t, "idle", "")

	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	idx := findSceneIndex(config, "Settings")
	if idx < 0 {
		t.Fatal("Settings scene missing")
	}

	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	withScreen(t, probeW, probeH)
	ClearTextCache()

	prevScene, prevHome := currentSceneIndex, homeLayoutActive
	prevBack, prevClock := sceneTitleBackRect, statusClockRect
	prevMedia := statusMediaRect
	prevAccent := accentColor
	currentSceneIndex = idx
	homeLayoutActive = false
	sceneTitleBackRect, statusClockRect = sdl.Rect{}, sdl.Rect{}
	statusMediaRect = sdl.Rect{}
	accentColor = ColorAccent
	t.Cleanup(func() {
		currentSceneIndex, homeLayoutActive = prevScene, prevHome
		sceneTitleBackRect, statusClockRect = prevBack, prevClock
		statusMediaRect = prevMedia
		accentColor = prevAccent
		ClearTextCache()
	})

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderStatusBar(renderer, config)

	if statusMediaRect.W <= 0 {
		t.Fatal("the now-playing chip was not measured")
	}
	if statusMediaRect.Y+statusMediaRect.H > HomeTopBarH {
		t.Fatalf("chip %+v is outside the top bar", statusMediaRect)
	}
	if statusMediaRect.X < screenWidth/2 {
		t.Fatalf("chip X=%d is not in the right-hand cluster", statusMediaRect.X)
	}
	if statusClockRect.X > statusMediaRect.X {
		t.Fatalf("chip %+v is left of the clock %+v", statusMediaRect, statusClockRect)
	}

	// The D-pad can reach the measured chip, and A opens the player.
	focusEngine.Clear()
	focusEngine.SetGraph("Settings", focusEngine.BuildGraph(config.Scenes[idx]))
	if !focusEngine.SetByElementIndex(ChromeMediaIndex) {
		t.Fatal("the measured chip did not join the focus graph")
	}

	isolateSceneSwitch(t)
	if !handleChromeBarClick(statusMediaRect.X+2, statusMediaRect.Y+2, config) {
		t.Fatal("a tap on the chip did nothing")
	}
	landSceneTransition()
	if got := config.Scenes[currentSceneIndex].Name; got != "MusicPlayer" {
		t.Fatalf("tap landed on %q, want MusicPlayer", got)
	}
}

// On Home the chip is a focusable header control: clock → Right reaches it, and
// A opens the player. It disappears from the focus order when nothing plays.
func TestHomeNowPlayingChipIsFocusable(t *testing.T) {
	snapshotHomeGlobals(t)
	pinStatusValues(t, 87, 3)
	pinMusicState(t, "Low Tide Radio")
	pinVideoState(t, "idle", "")
	prevMedia := statusMediaRect
	t.Cleanup(func() { statusMediaRect = prevMedia })

	renderHomeFrame(t, false)
	if homeLayout == nil {
		t.Fatal("the home layout was not built")
	}
	if statusMediaRect.W <= 0 {
		t.Fatal("the home header did not measure the chip")
	}
	item := homeLayout.Items[HomeFocusNowPlaying]
	if item.Rect != statusMediaRect {
		t.Fatalf("home item rect = %+v, want the drawn chip %+v", item.Rect, statusMediaRect)
	}
	if !homeLayout.Focus.IsEnabled(HomeFocusNowPlaying) {
		t.Fatal("the chip is drawn but not focusable")
	}
	if !homeLayout.Focus.Set(HomeFocusClock) {
		t.Fatal("could not focus the clock")
	}
	if got := homeLayout.Move(FocusRight); got != HomeFocusNowPlaying {
		t.Fatalf("Right from the clock = %q, want the now-playing chip", got)
	}

	// A click focuses it through the measured rect, and A opens the player.
	if !homeLayout.FocusAt(statusMediaRect.X+4, statusMediaRect.Y+4) {
		t.Fatal("a click on the chip did not focus it")
	}
	isolateSceneSwitch(t)
	prevIndex := currentSceneIndex
	currentSceneIndex = 0
	t.Cleanup(func() { currentSceneIndex = prevIndex })
	config := &Config{Scenes: []SceneConfig{{Name: "Main", Layout: "home"}}}
	homeActivate(config)
	landSceneTransition()
	if got := config.Scenes[currentSceneIndex].Name; got != "MusicPlayer" {
		t.Fatalf("A on the chip landed on %q, want MusicPlayer", got)
	}
}

func TestHomeNowPlayingChipIsSkippedWhenIdle(t *testing.T) {
	snapshotHomeGlobals(t)
	pinStatusValues(t, 87, 3)
	pinMusicState(t, "")
	pinVideoState(t, "idle", "")
	pinAudioOutput(t, "")
	prevMedia := statusMediaRect
	t.Cleanup(func() { statusMediaRect = prevMedia })

	renderHomeFrame(t, false)
	if homeLayout == nil {
		t.Fatal("the home layout was not built")
	}
	if statusMediaRect.W != 0 {
		t.Fatalf("idle header measured a chip at %+v", statusMediaRect)
	}
	if homeLayout.Focus.IsEnabled(HomeFocusNowPlaying) {
		t.Fatal("the chip stays focusable while nothing plays")
	}
	if !homeLayout.Focus.Set(HomeFocusClock) {
		t.Fatal("could not focus the clock")
	}
	if got := homeLayout.Move(FocusRight); got != HomeFocusBell {
		t.Fatalf("Right from the clock = %q, want the bell when no chip is drawn", got)
	}
}

// A connected headset fills the idle chip on Home too, drawn where the player
// chip lives. It is a reading: the D-pad still goes clock → bell, and A cannot
// open a player that is not running.
func TestHomeOutputChipIsDrawnButNotFocusable(t *testing.T) {
	snapshotHomeGlobals(t)
	pinStatusValues(t, 87, 3)
	pinMusicState(t, "")
	pinVideoState(t, "idle", "")
	pinAudioOutput(t, "WH-1000XM4")
	prevMedia := statusMediaRect
	t.Cleanup(func() { statusMediaRect = prevMedia })

	renderHomeFrame(t, false)
	if homeLayout == nil {
		t.Fatal("the home layout was not built")
	}
	if statusMediaRect.W <= 0 {
		t.Fatal("the home header dropped the connected output")
	}
	item := homeLayout.Items[HomeFocusNowPlaying]
	if item.Rect != statusMediaRect {
		t.Fatalf("home item rect = %+v, want the drawn output chip %+v", item.Rect, statusMediaRect)
	}
	if homeLayout.Focus.IsEnabled(HomeFocusNowPlaying) {
		t.Fatal("the idle output chip is focusable")
	}
	if !homeLayout.Focus.Set(HomeFocusClock) {
		t.Fatal("could not focus the clock")
	}
	if got := homeLayout.Move(FocusRight); got != HomeFocusBell {
		t.Fatalf("Right from the clock = %q, want the bell while the output chip is a reading", got)
	}
}
