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
	videoMiniHintH     = int32(78)
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
	// mediaMiniOnOwner keeps the window up on its owner scene too: the Media
	// screen floats its own video over the browser instead of leaving the
	// screen, the way a phone's video app minimises the player.
	mediaMiniOnOwner bool
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
	mediaMiniOnOwner = false
	videoMiniMu.Unlock()

	videoFullscreen = false
	videoSettingsOpen = false
	videoControlFocus = vcNone
}

// mediaMiniShowInPlace floats the video over the scene that owns it: the Media
// screen's own Back, which returns to the browser with the video still
// playing in the corner rather than leaving Media.
func mediaMiniShowInPlace() {
	videoMiniShow()
	if !videoMiniActive() {
		return
	}
	videoMiniMu.Lock()
	videoMiniScene = currentSceneIndex
	mediaMiniOnOwner = true
	videoMiniMu.Unlock()
}

// videoMiniClear forgets the window. Playback ending or a new session starting
// must not inherit the previous video's scene.
func videoMiniClear() {
	videoMiniMu.Lock()
	videoMiniOn = false
	videoMiniScene = -1
	mediaMiniOnOwner = false
	videoMiniMu.Unlock()
	videoMiniHits = videoMiniHitRects{}
}

// videoMiniVisible reports whether the floating window should be drawn. While
// the scene that owns the video element is on screen that element already
// draws the same frames, so the window would be a second copy — unless that
// scene floated the video itself (mediaMiniShowInPlace).
func videoMiniVisible() bool {
	if !videoMiniActive() || videoFullscreen || playerPhase() == "idle" {
		return false
	}
	videoMiniMu.Lock()
	onOwner := mediaMiniOnOwner
	videoMiniMu.Unlock()
	if onOwner {
		return true
	}
	return currentSceneIndex != videoMiniOwnerScene()
}

// mediaSceneHasPlayer reports whether a scene draws the embedded player: it
// carries a video element.
func mediaSceneHasPlayer(scene SceneConfig) bool {
	for _, el := range scene.Elements {
		if el.Type == "video" {
			return true
		}
	}
	return false
}

// mediaSessionOrphaned reports a session nothing on screen draws: not in full
// screen, not floating in the mini window, and the scene on screen has no
// video element. That is the state the Files bug left behind — the audio went
// on with no picture while the invisible player kept answering the D-pad.
// Only a real session counts (tests that only set the phase are left alone),
// and nothing is decided while a scene change is still fading.
func mediaSessionOrphaned(config *Config) bool {
	if config == nil || playbackCurrentSession() == nil || playerPhase() == "idle" {
		return false
	}
	if videoFullscreen || videoMiniActive() || transitionPhase != "none" {
		return false
	}
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	return !mediaSceneHasPlayer(config.Scenes[currentSceneIndex])
}

// mediaResolveOrphan gives an orphaned session a visible home or ends it. A
// file the user was watching is closed — they left it, the way leaving the
// picture viewer closes the picture — and a stream keeps playing in the
// floating window over the screen they went to, exactly as Back does.
func mediaResolveOrphan(config *Config) {
	sess := playbackCurrentSession()
	if sess == nil {
		return
	}
	if sess.local && sess.shown.Load() {
		logDebug("video", "left the player without Back; closing %s", sanitizeLogURL(sess.pageURL))
		StopEmbeddedPlayback()
		return
	}
	if playbackQueue.shorts {
		shortsAutoplay = false
	}
	videoMiniShow()
}

// mediaReturnsToOpener reports whether ending the current session should also
// leave the Media scene: a file opened from another screen (Files, a dropped
// file) is shown there only because Media owns the video element, so closing
// it returns to the screen it came from. Playback started inside Media stays.
func mediaReturnsToOpener(config *Config) bool {
	if config == nil || !mediaSessionIsLocal() || mediaHubOwnsPlayback() || videoMiniActive() {
		return false
	}
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	return mediaSceneHasPlayer(config.Scenes[currentSceneIndex])
}

// mediaEndSession is what Close and Stop do: playback ends where it stands,
// and a file opened from another screen also returns there (see
// mediaReturnsToOpener), so closing a clip from Files lands back in Files
// instead of on the Media browser.
func mediaEndSession(config *Config) {
	back := mediaReturnsToOpener(config)
	StopEmbeddedPlayback()
	if back {
		goBackScene(config)
	}
}

// videoBackOut is what Back means while a video plays: full screen steps back
// to the scene's window, and the scene's window steps back to the floating
// window over the previous screen. Only the Stop control ends playback.
func videoBackOut(config *Config) {
	if playerPhase() == "idle" {
		return
	}
	// A file from the card is closed by Back, like a picture in the viewer,
	// even from full screen: there is nothing to keep watching "behind" a clip
	// the user opened from Files, and a floating window over the file list was
	// how a video kept playing out of sight.
	if mediaSessionIsLocal() {
		videoFullscreen = false
		videoSettingsOpen = false
		if mediaReturnsToOpener(config) {
			StopEmbeddedPlayback()
			// goBackScene plays the back sound itself.
			goBackScene(config)
			return
		}
		PlayBackSound()
		StopEmbeddedPlayback()
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
	// Media's own player: Back returns to the Media browser with the video in
	// the corner, instead of leaving the screen the user was browsing.
	if mediaHubOwnsPlayback() && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		mediaHubOwnsScene(config.Scenes[currentSceneIndex].Name) {
		mediaMiniShowInPlace()
		PlayBackSound()
		return
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
	if config != nil && (owner < 0 || owner >= len(config.Scenes)) {
		// No recorded owner (a session that started away from any player
		// scene): the scene with the video element is where it can be shown.
		owner = sceneWithVideoElement(config)
	}
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
	h := videoMiniHeaderH + videoMiniProgressH + videoMiniHintH + w*9/16
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
			W: r.W, H: max32(0, r.H-videoMiniHeaderH-videoMiniProgressH-videoMiniHintH),
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
		renderVideoSubtitles(rr, config, frame.X, frame.Y, frame.W, frame.H, state.progress*state.duration, 12)
	})

	barY := frame.Y + frame.H + 1
	renderer.SetDrawColor(ColorBorderSubtle.R, ColorBorderSubtle.G, ColorBorderSubtle.B, 120)
	renderer.FillRect(&sdl.Rect{X: frame.X + 2, Y: barY, W: max32(0, frame.W-4), H: 3})
	if state.progress > 0 {
		fw := int32(float64(frame.W-4) * state.progress)
		renderer.SetDrawColor(ColorInfo.R, ColorInfo.G, ColorInfo.B, 255)
		renderer.FillRect(&sdl.Rect{X: frame.X + 2, Y: barY, W: fw, H: 3})
	}

	if state.phase == "paused" {
		// The player's paused badge in small: a dark glass disc with the
		// accent play glyph (videochrome.go).
		cx, cy := frame.X+frame.W/2, frame.Y+frame.H/2
		fillCircle(renderer, cx, cy, 18, sdl.Color{R: 8, G: 14, B: 22, A: 190})
		fillCircle(renderer, cx, cy, 17, WithAlpha(vcAccent(), 40))
		drawHomeGlyphTinted(renderer, "play", sdl.Rect{X: cx - 8, Y: cy - 9, W: 18, H: 18}, vcAccent())
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

	// Keep touch and pad instructions on the window itself rather than stealing
	// the underlying scene's footer. A/X apply only while Now Playing is focused.
	if hintFont := getFontSized(config, "small", 16); hintFont != nil {
		for i, hint := range videoMiniHints() {
			hint = ellipsize(hintFont, hint, r.W-16)
			tw, _, _ := hintFont.SizeUTF8(hint)
			renderText(renderer, config, hintFont, hint, ColorTextPrimary(),
				r.X+(r.W-int32(tw))/2, barY+videoMiniProgressH+int32(i)*17)
		}
	}

	// Close: the player's round corner button in small, with the shared icon.
	close := videoMiniHits.close
	d := min32(close.W, close.H) - 6
	if d > 0 {
		bx, by := close.X+(close.W-d)/2, close.Y+(close.H-d)/2
		fill := WithAlpha(ColorSurfaceRaised, 220)
		if vcHit(mouseX, mouseY, close) {
			fill = WithAlpha(DangerInk(), 200)
		}
		fillRoundedRect(renderer, bx, by, d, d, d/2, fill)
		g := d * 11 / 20
		drawHomeGlyphTinted(renderer, "close", sdl.Rect{X: bx + (d-g)/2, Y: by + (d-g)/2, W: g, H: g}, ColorTextPrimary())
	}

	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, RadiusMD, 1, WithAlpha(ColorBorder, 220))
}

func videoMiniHints() []string {
	return []string{
		"Tap video: open",
		"Tap X icon: close",
		"Focus Now Playing",
		hintLine(FooterHint{"A", "Open"}, FooterHint{"X", "Close"}),
	}
}

// closeFocusedMiniVideo lets the now-playing focus target close only the
// floating video, without taking X away from the scene's other controls.
func closeFocusedMiniVideo() bool {
	if !videoMiniVisible() {
		return false
	}
	PlayBackSound()
	StopEmbeddedPlayback()
	return true
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
