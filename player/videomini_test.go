package main

// Backing out of the player must keep the video running. These checks pin the
// three-step ladder (full screen -> the scene's video element -> a floating
// mini window over the previous screen) and the window's geometry and taps.
// The decoder itself is not exercised: the tests only need the player phase.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	miniTestHome = 0
	miniTestTube = 1
)

func miniTestConfig() *Config {
	return &Config{Scenes: []SceneConfig{
		{Name: "Main", Layout: "home"},
		{Name: "Tube", Elements: []Element{{Type: "video"}}},
		{Name: "Settings"},
	}}
}

// setupVideoMiniTest puts the player on the Media screen with a video playing
// and one scene of history behind it, then restores every global it touched.
func setupVideoMiniTest(t *testing.T, scene int) *Config {
	t.Helper()
	withScreen(t, 1280, 720)

	config := miniTestConfig()
	prevCfg, prevIdx, prevHistory := appConfig, currentSceneIndex, sceneHistory
	prevPhase, prevFull, prevOpen, prevFocus := playerPhase(), videoFullscreen, videoSettingsOpen, videoControlFocus
	prevTransition, prevPending := transitionPhase, pendingSceneIndex

	embeddedPlayer.mu.Lock()
	prevDur, prevBase := embeddedPlayer.duration, embeddedPlayer.progressBase
	embeddedPlayer.phase = "playing"
	embeddedPlayer.duration = 100
	embeddedPlayer.progressBase = 0
	embeddedPlayer.mu.Unlock()

	appConfig = config
	currentSceneIndex = scene
	sceneHistory = nil
	if scene != miniTestHome {
		pushSceneHistory(miniTestHome)
	}
	transitionPhase = "none"
	pendingSceneIndex = -1
	videoFullscreen = false
	videoSettingsOpen = false
	videoControlFocus = vcNone
	videoMiniClear()

	t.Cleanup(func() {
		videoMiniClear()
		embeddedPlayer.mu.Lock()
		embeddedPlayer.phase = prevPhase
		embeddedPlayer.duration = prevDur
		embeddedPlayer.progressBase = prevBase
		embeddedPlayer.mu.Unlock()
		appConfig, currentSceneIndex, sceneHistory = prevCfg, prevIdx, prevHistory
		transitionPhase, pendingSceneIndex = prevTransition, prevPending
		videoFullscreen, videoSettingsOpen, videoControlFocus = prevFull, prevOpen, prevFocus
	})
	return config
}

// landSceneTransition applies the pending scene the way the main loop does, so
// the checks can look at the screen the user actually lands on.
func landSceneTransition() {
	if pendingSceneIndex >= 0 {
		currentSceneIndex = pendingSceneIndex
		pendingSceneIndex = -1
	}
	transitionPhase = "none"
}

// backButton is the pad press that leaves the player.
func backButton() *sdl.ControllerButtonEvent {
	return &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_B}
}

// Back no longer stops the decoder: it floats the video over the scene it
// returns to, and the scene then owns the keys again.
func TestVideoBackKeepsPlayingInMiniWindow(t *testing.T) {
	setupVideoMiniTest(t, miniTestTube)

	if !HandleVideoControllerInput(backButton()) {
		t.Fatal("B on the player was not consumed")
	}
	if got := playerPhase(); got != "playing" {
		t.Fatalf("phase after Back = %q, want playing", got)
	}
	if !videoMiniActive() {
		t.Fatal("Back did not arm the mini window")
	}
	if pendingSceneIndex != miniTestHome {
		t.Fatalf("Back did not leave for the previous scene: pending=%d", pendingSceneIndex)
	}

	landSceneTransition()
	if !videoMiniVisible() {
		t.Fatal("mini window is not visible on the scene the user returned to")
	}
	// The scene must get its input back: the old global player handler would
	// have swallowed every arrow and Escape while a video existed.
	if HandleVideoKeyInput(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_RIGHT}}) {
		t.Error("the player swallowed a key while the video floats in its window")
	}
	if HandleVideoControllerInput(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_RIGHT}) {
		t.Error("the player swallowed a pad press while the video floats in its window")
	}
	if HandleVideoControlClick(screenWidth/2, screenHeight/2) {
		t.Error("stale player hitboxes consumed a click on the returned-to scene")
	}

	// Escape does the same thing from the keyboard.
	videoMiniClear()
	currentSceneIndex = miniTestTube
	pushSceneHistory(miniTestHome)
	if !HandleVideoKeyInput(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_ESCAPE}}) {
		t.Fatal("Escape on the player was not consumed")
	}
	if got := playerPhase(); got != "playing" || !videoMiniActive() {
		t.Fatalf("Escape ended the session: phase=%q mini=%v", got, videoMiniActive())
	}

	// And the overlay's back pill follows the same rule.
	landSceneTransition()
	videoMiniClear()
	currentSceneIndex = miniTestTube
	pushSceneHistory(miniTestHome)
	computeVideoControlLayout(tubeVideoX, tubeVideoY, tubeVideoW, tubeVideoH, playingSnapshot())
	back := videoControlRects.back
	if !HandleVideoControlClick(back.X+back.W/2, back.Y+back.H/2) {
		t.Fatal("tap on the player's back button was not consumed")
	}
	if got := playerPhase(); got != "playing" || !videoMiniActive() {
		t.Fatalf("back button ended the session: phase=%q mini=%v", got, videoMiniActive())
	}
}

// A short floated out of the feed must not keep the feed armed: the next short
// would start on its own and jump back to the video scene.
func TestFloatingAShortDisarmsAutoplay(t *testing.T) {
	setupVideoMiniTest(t, miniTestTube)
	prevQueue := playbackQueue
	playbackQueue = playbackQueueState{shorts: true, index: 0}
	shortsAutoplay = true
	t.Cleanup(func() {
		playbackQueue = prevQueue
		shortsAutoplay = false
	})

	HandleVideoControllerInput(backButton())
	if shortsAutoplay {
		t.Error("floating a short left the feed armed")
	}
	if got := playerPhase(); got != "playing" {
		t.Fatalf("floating a short stopped it: phase=%q", got)
	}
}

// Full screen has its own step: the first Back returns to the scene's window,
// and only the second one floats the video.
func TestVideoFullscreenBackStepsDownInsteadOfStopping(t *testing.T) {
	setupVideoMiniTest(t, miniTestTube)
	videoFullscreen = true

	HandleVideoControllerInput(backButton())
	if videoFullscreen {
		t.Error("B left full screen on")
	}
	if got := playerPhase(); got != "playing" {
		t.Fatalf("phase after leaving full screen = %q, want playing", got)
	}
	if videoMiniActive() {
		t.Error("one press should not both leave full screen and leave the scene")
	}

	HandleVideoControllerInput(backButton())
	if !videoMiniActive() {
		t.Fatal("second B did not float the video")
	}
	if pendingSceneIndex != miniTestHome {
		t.Fatalf("second B did not leave the scene: pending=%d", pendingSceneIndex)
	}
}

// The window must fit above the footer and be tappable on both panel sizes.
func TestVideoMiniWindowFitsAndHandlesTaps(t *testing.T) {
	for _, size := range []struct {
		name string
		w, h int32
	}{
		{"1280x720", 1280, 720},
		{"800x480", 800, 480},
	} {
		t.Run(size.name, func(t *testing.T) {
			config := setupVideoMiniTest(t, miniTestTube)
			withScreen(t, size.w, size.h)

			videoMiniShow()
			if owner := videoMiniOwnerScene(); owner != miniTestTube {
				t.Fatalf("mini window owner = %d, want the Media scene %d", owner, miniTestTube)
			}
			currentSceneIndex = miniTestHome

			r := videoMiniPanelRect()
			if r.W < videoMiniMinW || r.H < videoMiniHeaderH+videoMiniMinW*9/16 {
				t.Errorf("window %+v is too small to watch", r)
			}
			if r.X < 0 || r.Y < 0 || r.X+r.W > size.w || r.Y+r.H > size.h {
				t.Fatalf("window %+v does not fit in %dx%d", r, size.w, size.h)
			}
			if limit := size.h - bottomBarHeight(""); r.Y+r.H > limit {
				t.Errorf("window bottom %d covers the footer at %d", r.Y+r.H, limit)
			}

			computeVideoMiniLayout(r)
			if c := videoMiniHits.close; c.W < 24 || c.H < 24 {
				t.Errorf("close target %+v is smaller than a fingertip", c)
			}

			// A tap on the picture returns to the player.
			if !handleVideoMiniClick(r.X+r.W/2, r.Y+r.H/2, config) {
				t.Fatal("tap inside the window was not consumed")
			}
			if videoMiniActive() {
				t.Error("tapping the picture did not hand the video back to its scene")
			}
			if pendingSceneIndex != miniTestTube {
				t.Fatalf("tapping the picture did not open the player: pending=%d", pendingSceneIndex)
			}

			// The close button ends the session.
			landSceneTransition()
			currentSceneIndex = miniTestHome
			videoMiniShow()
			computeVideoMiniLayout(videoMiniPanelRect())
			closeRect := videoMiniHits.close
			if !handleVideoMiniClick(closeRect.X+closeRect.W/2, closeRect.Y+closeRect.H/2, config) {
				t.Fatal("tap on the close button was not consumed")
			}
			if got := playerPhase(); got != "idle" {
				t.Fatalf("close left the player in phase %q", got)
			}
			if videoMiniActive() {
				t.Error("close left the window state armed")
			}
		})
	}
}

// The window is a second renderer for the same session, so it has to draw the
// picture and title inside its own rect and nothing outside it.
func TestVideoMiniWindowDrawsOnlyInsideItsRect(t *testing.T) {
	config := setupVideoMiniTest(t, miniTestTube)
	f := newProbeFrame(t)
	withScreen(t, probeW, probeH)

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)

	videoMiniShow()
	currentSceneIndex = miniTestHome

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderVideoMini(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}

	r := videoMiniPanelRect()
	if got := brightPixelsInRect(f, r.X, r.Y, r.X+r.W, r.Y+r.H); got == 0 {
		t.Error("nothing legible inside the window: the title strip is missing")
	}
	// The panel is opaque, so the frame interior must differ from the flat
	// background even while the decoder has no frame yet (dark backdrop plus
	// spinner).
	frame := videoMiniHits.body
	if f.at(frame.X+4, frame.Y+4) == f.bg {
		t.Error("the picture area shows the scene background through the window")
	}
	if got := brightPixelsInRect(f, 0, 0, r.X-4, probeH); got != 0 {
		t.Errorf("%d bright pixels are drawn left of the window", got)
	}
	if got := brightPixelsInRect(f, 0, r.Y+r.H+6, probeW, probeH); got != 0 {
		t.Errorf("%d bright pixels are drawn below the window", got)
	}

	// While the owner scene is on screen its video element draws the frames,
	// so the window must stay away.
	currentSceneIndex = miniTestTube
	if videoMiniVisible() {
		t.Error("mini window is still drawn over the scene that owns the video")
	}
}
