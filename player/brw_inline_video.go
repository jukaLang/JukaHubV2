package main

// brw_inline_video.go — a page's video plays in its place on the page.
//
// A play card on a web page (a <video>, a YouTube or Vimeo embed, a script
// player's video) used to take the reader to the player screen, full size,
// and the page was gone until Back. Now the video plays where its card is:
// the picture, its subtitles and a progress line fill the card, it scrolls
// with the page, and when the card leaves the screen the video keeps playing
// in the floating window in the corner (videomini.go), as one the player was
// left with does. The card's expand button, a tap on the picture or A on the
// card open it in the media player (videoMiniExpand); Back from the player
// brings it back to the page, still playing. Its close button, B or the
// player's Stop end it.
//
// The session is the ordinary one: playSmartURL with the scene change left
// out (videoInlineStart, read once by ensureVideoScene), the loading spinner
// is the global one, and the floating window's switch (videoMiniShow) is what
// keeps the frames moving while the page draws them.

import (
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// videoInlineStart asks the next ensureVideoScene to leave the screen as it
// is: the video starts on the page that asked for it. Render thread.
var videoInlineStart bool

// brwInline is the video playing in its card. Render thread.
var brwInline struct {
	active  bool
	src     string    // the card's address (webVideoPos.Src)
	since   time.Time // when it was asked for
	rect    sdl.Rect  // where the card was drawn last, screen coordinates
	shown   uint64    // ticks of the last frame that drew it in place
	frame   bool      // drawn in place this frame: the corner window stays away
	drawnAt uint64    // frameCount of that frame
	close   sdl.Rect
	expand  sdl.Rect
	taught  bool // the how-to toast was shown this run
}

// brwInlinePlay starts a play card's video in its place on the page — start
// is the ordinary way to play it (a YouTube link, a stream, playSmartURL),
// run with the scene change left out — or opens it in the player when that
// card is already playing.
func brwInlinePlay(config *Config, src string, start func()) {
	if brwInlinePlaying(src) {
		PlayActivateSound()
		videoMiniExpand(config)
		return
	}
	videoInlineStart = true
	start()
	if !videoInlineStart {
		// ensureVideoScene took the request: a session is starting for this
		// card. A play turned away before it (Kid mode, a background download
		// of the same video) leaves any other card or session alone.
		brwInline.active, brwInline.src, brwInline.since = true, src, time.Now()
		brwInline.shown, brwInline.frame = 0, false
	}
	videoInlineStart = false
}

// brwInlineForget lets the card go when a video starts any other way: the
// session is no longer the one its card asked for. ensureVideoScene, which
// every Play passes through, calls it. Render thread.
func brwInlineForget() {
	brwInline.active = false
}

// brwInlinePlaying reports whether the card for src is playing in place.
func brwInlinePlaying(src string) bool {
	return brwInline.active && brwInline.src == src && playerPhase() != "idle"
}

// brwInlineShown reports whether the card was drawn in place a moment ago, so
// a tap there belongs to it rather than to the corner window.
func brwInlineShown() bool {
	return brwInline.active && brwInline.shown != 0 && sdl.GetTicks64()-brwInline.shown < 250 && videoMiniActive()
}

// brwInlineTakeFrame reports, once, that the card drew the video this frame:
// renderVideoMini then leaves its corner window out.
func brwInlineTakeFrame() bool {
	drawn := brwInline.frame
	brwInline.frame = false
	return drawn
}

// drawWebVideoInline draws the playing video over its card at rect and reports
// whether it did; a card that is not the one playing (or still loading) keeps
// its picture. Render thread, inside the page's clip.
func drawWebVideoInline(r *sdl.Renderer, config *Config, v webVideoPos, rect sdl.Rect) bool {
	if !brwInline.active || v.Src != brwInline.src || r == nil || webTiles.painting {
		// A page tile keeps the card's still picture; the video goes over the
		// tiles every frame (drawWebVideoInlineOver).
		return false
	}
	phase := playerPhase()
	if phase == "idle" {
		if !videoPrepareActive() && time.Since(brwInline.since) > 3*time.Second {
			// It ended, failed or was cancelled: the card is a card again.
			brwInline.active = false
		}
		return false
	}
	if videoFullscreen || videoPrepareActive() {
		// Full screen draws it; and while it loads (the loading card is up) a
		// session from before can still be the one playing: the card keeps
		// its picture until this one plays.
		return false
	}
	if brwInline.frame && brwInline.drawnAt == frameCount {
		// Two cards with one address (script players that all hand the page
		// to yt-dlp): the first one drawn shows the video, once.
		return false
	}
	if !videoMiniActive() {
		// The page draws the session now: the switch that keeps it running
		// away from the player screen, and that puts it in the corner when
		// the card scrolls off.
		videoMiniShow()
	}
	state := snapshotVideoState()
	videoAdvanceProgress(&state)
	UpdateCurrentTexture(r)
	embeddedPlayer.mu.Lock()
	tex := embeddedPlayer.current
	embeddedPlayer.mu.Unlock()

	r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	r.SetDrawColor(0, 0, 0, 255)
	r.FillRect(&rect)
	drawVideoFrameArea(r, tex, rect.X, rect.Y, rect.W, rect.H)
	renderVideoSubtitles(r, config, rect.X, rect.Y, rect.W, rect.H, state.progress*state.duration, 10)
	// The progress line along the card's foot.
	r.SetDrawColor(255, 255, 255, 70)
	r.FillRect(&sdl.Rect{X: rect.X, Y: rect.Y + rect.H - 3, W: rect.W, H: 3})
	if state.progress > 0 {
		r.SetDrawColor(ColorInfo.R, ColorInfo.G, ColorInfo.B, 255)
		r.FillRect(&sdl.Rect{X: rect.X, Y: rect.Y + rect.H - 3, W: int32(float64(rect.W) * state.progress), H: 3})
	}
	if phase == "paused" {
		cx, cy := rect.X+rect.W/2, rect.Y+rect.H/2
		fillCircle(r, cx, cy, 18, sdl.Color{R: 8, G: 14, B: 22, A: 190})
		drawHomeGlyphTinted(r, "play", sdl.Rect{X: cx - 8, Y: cy - 9, W: 18, H: 18}, vcAccent())
	}
	// Close (top right) and expand to the player (bottom right): round dark
	// buttons, as the player's own corner buttons.
	d := min(max(rect.H/7, 22), 34)
	pad := int32(6)
	brwInline.close = sdl.Rect{X: rect.X + rect.W - d - pad, Y: rect.Y + pad, W: d, H: d}
	brwInline.expand = sdl.Rect{X: rect.X + rect.W - d - pad, Y: rect.Y + rect.H - d - pad - 3, W: d, H: d}
	for _, b := range []struct {
		box   sdl.Rect
		glyph string
	}{{brwInline.close, "close"}, {brwInline.expand, "fullscreen"}} {
		fillRoundedRect(r, b.box.X, b.box.Y, b.box.W, b.box.H, b.box.W/2, sdl.Color{R: 8, G: 14, B: 22, A: 200})
		g := b.box.W * 11 / 20
		drawHomeGlyphTinted(r, b.glyph, sdl.Rect{X: b.box.X + (b.box.W-g)/2, Y: b.box.Y + (b.box.H-g)/2, W: g, H: g}, ColorTextPrimary())
	}
	if brwInline.shown == 0 && !brwInline.taught {
		// Once a run, when the picture first shows (the loading card is gone):
		// the card is no longer a doorway to the player.
		brwInline.taught = true
		ShowToast("Plays on the page · A again opens the player · B stops", ToastKindInfo)
	}
	brwInline.rect = rect
	brwInline.shown = sdl.GetTicks64()
	brwInline.frame, brwInline.drawnAt = true, frameCount
	return true
}

// drawWebVideoInlineOver draws the video playing in its card over the page
// tiles, which hold the card's still picture (webpage_tiles.go). rect is the
// page on screen and offset its scroll. Render thread, inside the page's clip.
func drawWebVideoInlineOver(r *sdl.Renderer, config *Config, layout *webLayout, rect sdl.Rect, offset int32) {
	if !brwInline.active || layout == nil {
		return
	}
	for i := range layout.Videos {
		v := layout.Videos[i]
		if v.Invisible || v.Src != brwInline.src {
			continue
		}
		y := rect.Y + v.Rect.Y - offset
		if v.Rect.H <= 0 || y >= rect.Y+rect.H || y+v.Rect.H <= rect.Y {
			continue
		}
		restore := webClipPaint(r, rect, v.Clip, v.HasClip, rect.X, rect.Y-offset)
		drawn := drawWebVideoInline(r, config, v, sdl.Rect{X: rect.X + v.Rect.X, Y: y, W: v.Rect.W, H: v.Rect.H})
		restore()
		if drawn {
			return
		}
	}
}

// brwInlineClick consumes a tap on the card playing in place: its close button
// ends the video, anywhere else on it opens the player. A tap elsewhere is
// left to the page.
func brwInlineClick(mx, my int32, config *Config) bool {
	if !pointInRect(mx, my, brwInline.rect) {
		return false
	}
	if pointInRect(mx, my, brwInline.close) {
		PlayBackSound()
		StopEmbeddedPlayback()
		brwInline.active = false
		return true
	}
	PlayActivateSound()
	videoMiniExpand(config)
	return true
}
