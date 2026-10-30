package main

// Regression tests for the video control bar as a touch UI: every transport
// button must be a real, separate tap target (they were reported as "not
// selectable" on the handheld), the CC / settings / full screen buttons must
// act on a tap, and full screen must really cover the whole panel while the
// chrome hides.

import (
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// tubeVideoRect is the video element the Tube (Media) scene uses.
const (
	tubeVideoX = int32(24)
	tubeVideoY = int32(136)
	tubeVideoW = int32(1232)
	tubeVideoH = int32(520)
)

// playingSnapshot is the state the control layout is computed for.
func playingSnapshot() videoStateSnapshot {
	return videoStateSnapshot{
		phase:    "playing",
		progress: 0.5,
		duration: 100,
		volume:   1,
		speed:    1,
	}
}

// controlRectsByName returns every control hitbox the overlay publishes.
func controlRectsByName() map[string]sdl.Rect {
	return map[string]sdl.Rect{
		"playPause":  videoControlRects.playPause,
		"stop":       videoControlRects.stop,
		"seekBack":   videoControlRects.seekBack,
		"seekFwd":    videoControlRects.seekFwd,
		"speed":      videoControlRects.speed,
		"volDown":    videoControlRects.volDown,
		"volUp":      videoControlRects.volUp,
		"back":       videoControlRects.back,
		"close":      videoControlRects.close,
		"subs":       videoControlRects.subs,
		"settings":   videoControlRects.settings,
		"fullscreen": videoControlRects.fullscreen,
	}
}

func resetVideoUIControls(t *testing.T) {
	t.Helper()
	prevFull, prevUntil, prevOpen, prevItem := videoFullscreen, videoChromeUntil, videoSettingsOpen, videoSettingsItem
	prevEnabled := subtitlesEnabled()
	videoSubs.mu.Lock()
	prevCues := videoSubs.cues
	videoSubs.mu.Unlock()
	t.Cleanup(func() {
		videoFullscreen, videoChromeUntil = prevFull, prevUntil
		videoSettingsOpen, videoSettingsItem = prevOpen, prevItem
		setSubtitlesEnabled(prevEnabled)
		videoSubs.mu.Lock()
		videoSubs.cues = prevCues
		videoSubs.mu.Unlock()
	})
}

// setVideoPlayingForTest puts the embedded player in the playing phase, which
// the overlay requires before it accepts taps or keys.
func setVideoPlayingForTest(t *testing.T) {
	t.Helper()
	embeddedPlayer.mu.Lock()
	prevPhase, prevDur := embeddedPlayer.phase, embeddedPlayer.duration
	embeddedPlayer.phase = "playing"
	embeddedPlayer.duration = 100
	embeddedPlayer.mu.Unlock()
	t.Cleanup(func() {
		embeddedPlayer.mu.Lock()
		embeddedPlayer.phase, embeddedPlayer.duration = prevPhase, prevDur
		embeddedPlayer.mu.Unlock()
	})
}

// loadTestCues gives the player one subtitle track so CC can be switched on.
func loadTestCues(t *testing.T) {
	t.Helper()
	videoSubs.mu.Lock()
	videoSubs.cues = parseSubtitles("1\n00:00:00,000 --> 00:00:20,000\nHello\n")
	videoSubs.enabled = false
	videoSubs.status = "test.srt"
	videoSubs.mu.Unlock()
}

// Every control must be a tap-sized, non-overlapping box inside the video.
func TestVideoControlsAreSeparateTapTargets(t *testing.T) {
	withScreen(t, 1280, 720)
	computeVideoControlLayout(tubeVideoX, tubeVideoY, tubeVideoW, tubeVideoH, playingSnapshot())

	rects := controlRectsByName()
	for name, r := range rects {
		if r.W < 30 || r.H < 30 {
			t.Errorf("%s: hitbox %v is smaller than a fingertip", name, r)
		}
		if r.X < tubeVideoX || r.Y < tubeVideoY ||
			r.X+r.W > tubeVideoX+tubeVideoW || r.Y+r.H > tubeVideoY+tubeVideoH {
			t.Errorf("%s: hitbox %v is outside the video area", name, r)
		}
	}

	names := make([]string, 0, len(rects))
	for name := range rects {
		names = append(names, name)
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			a, b := rects[names[i]], rects[names[j]]
			if a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
				t.Errorf("%s %v overlaps %s %v: a tap lands on both", names[i], a, names[j], b)
			}
		}
	}
}

// A tap on the CC button turns subtitles on, and the settings sheet can turn
// them back off.
func TestVideoSubtitleButtonAndSettingsSheet(t *testing.T) {
	withScreen(t, 1280, 720)
	resetVideoUIControls(t)
	loadTestCues(t)
	setVideoPlayingForTest(t)
	computeVideoControlLayout(tubeVideoX, tubeVideoY, tubeVideoW, tubeVideoH, playingSnapshot())

	subs := videoControlRects.subs
	if !HandleVideoControlClick(subs.X+subs.W/2, subs.Y+subs.H/2) {
		t.Fatal("tap on CC was not consumed")
	}
	if !subtitlesEnabled() {
		t.Fatal("tapping CC did not enable subtitles")
	}

	settings := videoControlRects.settings
	if !HandleVideoControlClick(settings.X+settings.W/2, settings.Y+settings.H/2) {
		t.Fatal("tap on settings was not consumed")
	}
	if !videoSettingsOpen {
		t.Fatal("tapping settings did not open the sheet")
	}

	// Row 0 of the sheet toggles subtitles.
	sheet := videoSettingsSheetRect(0, 0, screenWidth, screenHeight)
	rowY := sheet.Y + videoSettingsTopPad + videoSettingsRowH/2
	if !HandleVideoControlClick(sheet.X+20, rowY) {
		t.Fatal("tap on the subtitle row was not consumed")
	}
	if subtitlesEnabled() {
		t.Fatal("subtitle row did not turn subtitles off")
	}

	// A tap outside the sheet closes it instead of reaching the scene.
	if !HandleVideoControlClick(40, 40) {
		t.Fatal("tap outside the open sheet was not consumed")
	}
	if videoSettingsOpen {
		t.Fatal("tap outside the sheet should close it")
	}
}

// The full screen button switches the player to full screen and the chrome
// hides until the next tap.
func TestVideoFullscreenButtonHidesChrome(t *testing.T) {
	withScreen(t, 1280, 720)
	resetVideoUIControls(t)
	setVideoPlayingForTest(t)
	videoFullscreen = false
	computeVideoControlLayout(tubeVideoX, tubeVideoY, tubeVideoW, tubeVideoH, playingSnapshot())

	fs := videoControlRects.fullscreen
	if !HandleVideoControlClick(fs.X+fs.W/2, fs.Y+fs.H/2) {
		t.Fatal("tap on full screen was not consumed")
	}
	if !videoFullscreen {
		t.Fatal("full screen button did not switch to full screen")
	}
	if !videoChromeVisible() {
		t.Fatal("chrome should be visible right after entering full screen")
	}

	// After the timeout the chrome hides, and a tap brings it back.
	videoChromeUntil = 0
	if videoChromeVisible() {
		t.Fatal("chrome should hide after the timeout")
	}
	if !HandleVideoControlClick(30, 400) {
		t.Fatal("tap in full screen was not consumed")
	}
	if !videoChromeVisible() {
		t.Fatal("a tap in full screen should reveal the chrome again")
	}
}

// renderingFullscreenProbe renders the player full screen over a green "video
// frame" and returns the captured pixels.
func renderingFullscreenProbe(t *testing.T, chrome bool) *probeFrame {
	t.Helper()
	f := newProbeFrame(t)
	// A decoder from the playback integration test can still be exiting. Its
	// finish path calls StopEmbeddedPlayback, which resets the player to idle
	// mid-render and turned the probe frame black depending on scheduling.
	// Drain the decoder goroutines before setting up this frame's state.
	if !waitForVideoDecoders(5 * time.Second) {
		// A decoder that outlives its test resets the player to idle mid-render,
		// which is what made this probe draw a black frame. Failing here names
		// the leak instead of skipping the assertions below.
		t.Fatal("a video decoder from an earlier test did not exit within 5s")
	}
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()

	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_RGB24, sdl.TEXTUREACCESS_STATIC, 4, 4)
	if err != nil {
		t.Fatalf("could not create the probe texture: %v (the software renderer supports textures, so this is a defect)", err)
	}
	defer tex.Destroy()
	green := make([]byte, 4*4*3)
	for i := 0; i < len(green); i += 3 {
		green[i+1] = 255
	}
	if err := tex.Update(nil, unsafe.Pointer(&green[0]), 4*3); err != nil {
		t.Fatalf("could not update the probe texture: %v (the software renderer supports texture uploads, so this is a defect)", err)
	}

	embeddedPlayer.mu.Lock()
	prevPhase, prevProg, prevBase := embeddedPlayer.phase, embeddedPlayer.progress, embeddedPlayer.progressBase
	prevDur, prevStart := embeddedPlayer.duration, embeddedPlayer.decoderStartTime
	// No decoder is running now, but park any raw frame a finished one left
	// behind: UpdateCurrentTexture would replace the probe texture with it.
	prevRaw, prevW, prevH := embeddedPlayer.currentRaw, embeddedPlayer.width, embeddedPlayer.height
	prevSeq, prevUploaded := embeddedPlayer.frameSeq, embeddedPlayer.uploadedSeq
	embeddedPlayer.phase = "playing"
	embeddedPlayer.progress = 0.5
	embeddedPlayer.progressBase = 0.5
	embeddedPlayer.duration = 100
	embeddedPlayer.decoderStartTime = time.Now()
	embeddedPlayer.current = tex
	embeddedPlayer.currentRaw = nil
	embeddedPlayer.width, embeddedPlayer.height = 0, 0
	embeddedPlayer.frameSeq, embeddedPlayer.uploadedSeq = 0, 0
	embeddedPlayer.mu.Unlock()
	// Restore as this probe returns, not in t.Cleanup. Two probes in one test
	// must each start from the same state, and the texture this probe created is
	// destroyed with its renderer right after: leaving it (or a previous
	// texture, whose renderer is already gone) in the global player state gave a
	// later test a dangling SDL_Texture and crashed SDL_RenderReadPixels.
	defer func() {
		embeddedPlayer.mu.Lock()
		embeddedPlayer.phase, embeddedPlayer.progress, embeddedPlayer.progressBase = prevPhase, prevProg, prevBase
		embeddedPlayer.duration, embeddedPlayer.decoderStartTime = prevDur, prevStart
		embeddedPlayer.current = nil
		embeddedPlayer.currentRaw, embeddedPlayer.width, embeddedPlayer.height = prevRaw, prevW, prevH
		embeddedPlayer.frameSeq, embeddedPlayer.uploadedSeq = prevSeq, prevUploaded
		embeddedPlayer.mu.Unlock()
	}()

	DestroyVKVideoOverlayCache()
	// The cached static control layer is a texture owned by this probe's
	// renderer, so it must go before that renderer does. Releasing it in
	// t.Cleanup kept it alive past the renderer and the next probe destroyed a
	// dangling texture, which surfaced as a crash inside SDL_RenderReadPixels.
	defer DestroyVKVideoOverlayCache()

	videoFullscreen = true
	if chrome {
		revealVideoChrome()
	} else {
		videoChromeUntil = 0
	}

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderVideoFullscreen(renderer, &Config{})
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
	return f
}

// isGreenPixel reports a pixel that came from the video texture (the overlay
// gradients darken it but leave it green-dominant).
func isGreenPixel(c sdl.Color) bool {
	return c.G > 110 && c.R < 90 && c.B < 90
}

// Full screen must cover the entire panel, not just the element rectangle.
func TestVideoFullscreenCoversPanel(t *testing.T) {
	withScreen(t, 1280, 720)
	resetVideoUIControls(t)
	f := renderingFullscreenProbe(t, true)

	for _, p := range []sdl.Point{{X: 6, Y: 6}, {X: probeW - 6, Y: 6}, {X: probeW / 2, Y: 200}} {
		if c := f.at(p.X, p.Y); !isGreenPixel(c) {
			t.Errorf("pixel (%d,%d) is %+v, not the video frame: full screen did not cover the panel", p.X, p.Y, c)
		}
	}
}

// With the chrome hidden only the picture and subtitles remain.
func TestVideoFullscreenHidesControlBar(t *testing.T) {
	withScreen(t, 1280, 720)
	resetVideoUIControls(t)

	visible := renderingFullscreenProbe(t, true)
	hidden := renderingFullscreenProbe(t, false)

	// The bar sits above the bottom edge of the screen in full screen.
	barY := probeH - 36
	if c := hidden.at(probeW/2, barY); !isGreenPixel(c) {
		t.Errorf("hidden chrome: bottom bar still painted (%+v)", c)
	}

	// And the transport glyphs must be gone with it: count bright pixels in
	// the bar band and compare.
	brightVisible := overlayCountNear(visible, ColorTextPrimary(), 0, probeH-72, probeW, probeH, 60)
	brightHidden := overlayCountNear(hidden, ColorTextPrimary(), 0, probeH-72, probeW, probeH, 60)
	if brightVisible == 0 {
		t.Fatal("chrome visible: no control pixels drawn at all")
	}
	if brightHidden != 0 {
		t.Errorf("hidden chrome: %d control pixels still drawn", brightHidden)
	}
}
