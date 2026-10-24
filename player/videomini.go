package main

// videomini.go keeps a video playing when the user backs out of the player.
//
// Back used to end the session: one press of B or Escape on the Media screen
// stopped the decoder, so there was no way to keep watching while browsing.
// Back now steps down one size instead of stopping: full screen -> the scene's
// video element -> a small window over the scene the user returns to. The
// playback session itself is never restarted, so the position and the audio
// continue seamlessly.
//
// The window floats in the bottom-right corner above the footer. Tapping the
// picture returns to the player; its close button (and the player's Stop
// control) still end the session.

import (
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	videoMiniMargin  = int32(16)
	videoMiniHeaderH = int32(26)
	videoMiniMinW    = int32(200)
	videoMiniMaxW    = int32(360)
	videoMiniCloseSz = int32(30)
	// The picture stops short of the panel's bottom edge so the visible
	// progress bar sits inside the rounded corner instead of squaring it off.
	videoMiniProgressH = int32(6)
)

// videoMiniHitRects is the layout of the last drawn window. A click falls back
// to computing it on demand, so a tap before the first window frame still
// lands where the next frame will be drawn.
type videoMiniHitRects struct {
	panel sdl.Rect
	body  sdl.Rect
	close sdl.Rect
}

var (
	videoMiniMu    sync.Mutex
	videoMiniOn    bool
	videoMiniScene = -1
	videoMiniHits  videoMiniHitRects
)

// videoMiniActive reports whether the video is being drawn in the floating
// window rather than in its scene element.
func videoMiniActive() bool {
	videoMiniMu.Lock()
	defer videoMiniMu.Unlock()
	return videoMiniOn
}

// videoMiniOwnerScene is the scene whose video element shows this session.
// Expanding the window returns there.
func videoMiniOwnerScene() int {
	videoMiniMu.Lock()
	defer videoMiniMu.Unlock()
	return videoMiniScene
}

// videoMiniShow moves the playing video into the floating window. The owner
// scene is resolved from the layout, not from the scene index: a video can be
// started from the Shorts feed or Home's Continue card while the frames are
// drawn by the Media scene's video element.
func videoMiniShow() {
	if playerPhase() == "idle" {
		return
	}
	owner := sceneWithVideoElement(appConfig)
	if owner < 0 {
		owner = currentSceneIndex
	}
	videoMiniMu.Lock()
	videoMiniOn = true
	videoMiniScene = owner
	videoMiniMu.Unlock()

	videoFullscreen = false
	videoSettingsOpen = false
	videoControlFocus = vcNone
}

// videoMiniClear forgets the window. Playback ending or a new session starting
// must not inherit the previous video's scene.
func videoMiniClear() {
	videoMiniMu.Lock()
	videoMiniOn = false
	videoMiniScene = -1
	videoMiniMu.Unlock()
	videoMiniHits = videoMiniHitRects{}
}

// videoMiniVisible reports whether the floating window should be drawn. While
// the scene that owns the video element is on screen that element already
// draws the same frames, so the window would be a second copy.
func videoMiniVisible() bool {
	if !videoMiniActive() || videoFullscreen || playerPhase() == "idle" {
		return false
	}
	return currentSceneIndex != videoMiniOwnerScene()
}

// videoBackOut is what Back means while a video plays: full screen steps back
// to the scene's window, and the scene's window steps back to the floating
// window over the previous screen. Only the Stop control ends playback.
func videoBackOut(config *Config) {
	if playerPhase() == "idle" {
		return
	}
	if videoFullscreen {
		videoFullscreen = false
		videoSettingsOpen = false
		PlayBackSound()
		return
	}
	if config == nil {
		return
	}
	// A floating short is no longer being watched in the feed: keeping the
	// autoplay armed would start the next short, and jump back to the video
	// scene, while the user is browsing somewhere else.
	if playbackQueue.shorts {
		shortsAutoplay = false
	}
	videoMiniShow()
	// goBackScene plays the back sound itself and leaves the window visible on
	// whatever screen it lands on.
	goBackScene(config)
}

// videoMiniExpand returns to the player: the owner scene draws the frames in
// its own video element again.
func videoMiniExpand(config *Config) {
	owner := videoMiniOwnerScene()
	if config == nil || owner < 0 || owner >= len(config.Scenes) {
		return
	}
	if owner == currentSceneIndex {
		videoMiniClear()
		return
	}
	// A scene change is already running. Keep the window until it can be acted
	// on, so the picture is never missing while the transition covers the
	// screen.
	if transitionPhase != "none" {
		return
	}
	videoMiniClear()
	changeSceneTo(config, owner)
}

// videoMiniPanelRect lays the window out for the current panel size: a 16:9
// picture under a title strip, in the bottom-right corner above the footer.
func videoMiniPanelRect() sdl.Rect {
	w := screenWidth / 4
	if w > videoMiniMaxW {
		w = videoMiniMaxW
	}
	if w < videoMiniMinW {
		w = videoMiniMinW
	}
	if max := screenWidth - 2*videoMiniMargin; w > max {
		w = max
	}
	if w < 1 {
		w = 1
	}
	h := videoMiniHeaderH + videoMiniProgressH + w*9/16
	if h > screenHeight-videoMiniMargin*2 {
		h = screenHeight - videoMiniMargin*2
	}
	footer := bottomBarHeight("")
	y := screenHeight - footer - videoMiniMargin - h
	if y < HomeTopBarH+videoMiniMargin {
		y = HomeTopBarH + videoMiniMargin
	}
	if y < videoMiniMargin {
		y = videoMiniMargin
	}
	return sdl.Rect{X: screenWidth - videoMiniMargin - w, Y: y, W: w, H: h}
}

// computeVideoMiniLayout fills the tap targets for the given window rect.
func computeVideoMiniLayout(r sdl.Rect) {
	videoMiniHits = videoMiniHitRects{
		panel: r,
		body: sdl.Rect{
			X: r.X, Y: r.Y + videoMiniHeaderH,
			W: r.W, H: max32(0, r.H-videoMiniHeaderH-videoMiniProgressH),
		},
		close: sdl.Rect{X: r.X + r.W - videoMiniCloseSz, Y: r.Y, W: videoMiniCloseSz, H: videoMiniHeaderH},
	}
}

// renderVideoMini draws the floating window: the title strip with its close
// button, the live picture, a progress line and a play badge while paused.
func renderVideoMini(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !videoMiniVisible() {
		return
	}
	state := snapshotVideoState()
	if state.phase == "idle" {
		return
	}
	// The window is the only thing drawing the session, so it also keeps the
	// position moving and the throttled resume save running.
	videoAdvanceProgress(&state)
	UpdateCurrentTexture(renderer)

	embeddedPlayer.mu.Lock()
	tex := embeddedPlayer.current
	embeddedPlayer.mu.Unlock()

	r := videoMiniPanelRect()
	computeVideoMiniLayout(r)
	frame := videoMiniHits.body

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, r.X+3, r.Y+5, r.W, r.H, RadiusMD, sdl.Color{R: 0, G: 0, B: 0, A: 110})
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, RadiusMD, WithAlpha(ColorSurfacePanel, 245))

	renderWithClip(renderer, frame.X, frame.Y, frame.W, frame.H, func(rr *sdl.Renderer) {
		drawVideoFrameArea(rr, tex, frame.X, frame.Y, frame.W, frame.H)
	})

	barY := r.Y + r.H - videoMiniProgressH + 1
	renderer.SetDrawColor(ColorBorderSubtle.R, ColorBorderSubtle.G, ColorBorderSubtle.B, 120)
	renderer.FillRect(&sdl.Rect{X: frame.X + 2, Y: barY, W: max32(0, frame.W-4), H: 3})
	if state.progress > 0 {
		fw := int32(float64(frame.W-4) * state.progress)
		renderer.SetDrawColor(ColorInfo.R, ColorInfo.G, ColorInfo.B, 255)
		renderer.FillRect(&sdl.Rect{X: frame.X + 2, Y: barY, W: fw, H: 3})
	}

	if state.phase == "paused" {
		cx, cy := frame.X+frame.W/2, frame.Y+frame.H/2
		fillCircle(renderer, cx, cy, 16, WithAlpha(ColorSurfacePanel, 210))
		s := int32(7)
		fillTriangleFilled(renderer, pt{x: cx - s + 1, y: cy - s}, pt{x: cx - s + 1, y: cy + s}, pt{x: cx + s - 1, y: cy}, ColorTextPrimary())
	}

	if font, _ := getCachedFont(config, "small"); font != nil {
		title, ok := currentMediaTitle()
		if !ok || title == "" {
			title = "Video"
		}
		title = ellipsize(font, title, r.W-videoMiniCloseSz-16)
		_, th, _ := font.SizeUTF8(title)
		renderText(renderer, config, font, title, ColorTextPrimary(),
			r.X+10, r.Y+(videoMiniHeaderH-int32(th))/2)
	}

	close := videoMiniHits.close
	fillRoundedRect(renderer, close.X+4, close.Y+4, close.W-8, close.H-8, 6,
		WithAlpha(ColorSurfaceRaised, 210))
	drawCloseIcon(renderer, close.X+close.W/2, close.Y+close.H/2, 5, ColorTextPrimary())

	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, RadiusMD, 1, WithAlpha(ColorBorder, 220))
}

// handleVideoMiniClick consumes a tap on the floating window: the picture goes
// back to the player, the close button ends the session. It reports whether the
// tap belonged to the window so the scene underneath never sees it.
func handleVideoMiniClick(mx, my int32, config *Config) bool {
	if !videoMiniVisible() {
		return false
	}
	r := videoMiniHits.panel
	if r.W <= 0 || r.H <= 0 {
		r = videoMiniPanelRect()
		computeVideoMiniLayout(r)
	}
	if !pointInRect(mx, my, r) {
		return false
	}
	if pointInRect(mx, my, videoMiniHits.close) {
		PlayBackSound()
		StopEmbeddedPlayback()
		return true
	}
	PlayActivateSound()
	videoMiniExpand(config)
	return true
}
