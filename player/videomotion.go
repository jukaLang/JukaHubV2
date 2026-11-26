package main

// videomotion.go — how the video player moves and answers (Juka, 2026-11-26:
// "improve media player section...make it smoother and improve UX and UI").
//
// The controls used to cut in and out: in full screen they vanished three
// seconds after the last press and snapped back on the next one, and in the
// window they never went away, so the glass bar sat over the bottom of every
// film. This file is the player's motion and feedback, kept apart from the
// drawing (videochrome.go) and the pipeline (videoplayer.go):
//
//   - The controls fade — and, with motion on, the bar sinks and the top row
//     rises — instead of cutting, in the window as in full screen. While a
//     video plays they step aside vcIdleHideMS after the last input and come
//     back on any press, a pointer moving over the picture, a pause, a seek or
//     the settings sheet. A first Left/Right while they are away only brings
//     them back, with the ring on Play, so the ring never moves unseen; A
//     still plays and pauses at once. Opacity is not motion — Reduce motion
//     keeps the fades, the way a phone's Reduce Motion swaps slides for
//     dissolves — and only Battery Saver snaps them.
//   - Seeks gather. Each press of L1/R1 (the rewind and fast-forward buttons,
//     Shift+arrows, J/L) moves the handle and the clock at once and adds up
//     ("+30 s"); the decoders start again once, vcSeekSettleMS after the last
//     press. Every press used to restart both ffmpeg processes. A drag on the
//     track scrubs with a time bubble and seeks once, on release.
//   - After a start or a seek the position waits for the first new frame
//     (embeddedPlayer.awaitFrame) instead of running ahead on the wall clock
//     and jumping back when the picture arrived; a spinner sits on the last
//     frame while the next one is on its way or a stream stalls.
//   - Feedback: a volume pill for Up/Down, the play glyph blooming out of the
//     paused button on resume, the paused button popping in, the settings
//     sheet rising in with a selection that glides, an "Up next" card over the
//     last seconds, and subtitles that ride up and down with the bar.
//   - Frames: the main loop wakes when the decoder publishes a frame
//     (videoFrameWake, saver_power.go's framePressSleep), so every frame is
//     shown once and on time rather than whenever the loop next came round.

import (
	"math"
	"strconv"
	"sync/atomic"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// Motion and feedback timings.
const (
	// vcIdleHideMS is how long a playing video's controls stay up after the
	// last input, in the window as in full screen.
	vcIdleHideMS = 3000
	// vcFadeInMS and vcFadeOutMS are the controls' fade: in fast, so a press
	// answers at once; out slower, so they leave without a jolt.
	vcFadeInMS  = 150
	vcFadeOutMS = 320
	// vcSinkPx is how far the bar sinks, and the top row rises, as the
	// controls fade (with motion on).
	vcSinkPx = 12.0
	// vcSubsInsetHidden is the subtitles' distance from the picture's foot
	// while the bar is away.
	vcSubsInsetHidden = int32(44)
	// vcSeekSettleMS is how long the player waits after the last seek press
	// before it starts the decoders at the gathered position.
	vcSeekSettleMS = 350
	// vcBufferShowMS is how long a new decoder may take to its first frame
	// before the spinner shows; vcStallShowMS the same for a stream that
	// stops delivering while it plays (or three frame times, if longer).
	vcBufferShowMS = 300
	vcStallShowMS  = 1500
	// vcVolumeHUDMS is how long the volume pill stays after the last change,
	// the last vcVolumeFadeMS of it fading.
	vcVolumeHUDMS  = 1100
	vcVolumeFadeMS = 280
	// vcPulseMS is the play glyph's bloom as a video resumes; vcPopMS the
	// paused button's entrance.
	vcPulseMS = 520
	vcPopMS   = 220
	// vcSheetEnterMS is the settings sheet's entrance.
	vcSheetEnterMS = 200
	// vcDoubleClickMS bounds a double click on the picture.
	vcDoubleClickMS = 350
	// vcUpNextLeadSec is how long before the end the Up Next card shows.
	vcUpNextLeadSec = 10.0
	// videoAwaitFrameMaxSec bounds how long the position waits for a new
	// decoder's first frame (videoAdvanceProgress): a picture that never
	// comes must not freeze the clock.
	videoAwaitFrameMaxSec = 4.0
	// videoFramePollMS is how often the frame's sleep looks for a new video
	// frame while a player is on screen (framePressSleep).
	videoFramePollMS = uint64(4)
)

// ── Settings that shape the motion ──────────────────────────────────────

// vcFadesOn reports whether the player's fades play. They are opacity, not
// motion, so Reduce motion keeps them; Battery Saver snaps them.
func vcFadesOn() bool {
	return currentPowerMode(appConfig) != PowerModeBatterySaver
}

// vcMotionOn reports whether the player's movements play: the bar sinking as
// it fades, the paused button's pop, the bloom's growth, the sheet's rise.
func vcMotionOn() bool {
	return uiMotionEnabled(appConfig)
}

// vcGlideOn reports whether the settings sheet's selection glides between
// rows: Settings → Focus bounce, like every focus ring (homefocusglide.go).
func vcGlideOn() bool {
	return focusBounceLevelOf(appConfig) != focusBounceOff
}

// vcSmooth is smoothstep: the fades' curve, gentle at both ends.
func vcSmooth(t float64) float64 {
	t = uiClamp01(t)
	return t * t * (3 - 2*t)
}

// vcInkFade is ink at a share of its alpha, in eighths: a label is a cached
// texture per colour, and a new alpha every frame would rasterise a new one.
func vcInkFade(c sdl.Color, share float64) sdl.Color {
	q := math.Round(uiClamp01(share)*8) / 8
	c.A = uint8(float64(c.A)*q + 0.5)
	return c
}

// vcScaleRect is r grown (or shrunk) by s around its centre.
func vcScaleRect(r sdl.Rect, s float64) sdl.Rect {
	w := int32(math.Round(float64(r.W) * s))
	h := int32(math.Round(float64(r.H) * s))
	return sdl.Rect{X: r.X + (r.W-w)/2, Y: r.Y + (r.H-h)/2, W: w, H: h}
}

// vcKeepClip runs draw and puts the renderer's clip back as it was:
// compositeUILayerIn ends with no clip at all, and a scene may draw the
// windowed player inside one.
func vcKeepClip(renderer *sdl.Renderer, draw func()) {
	old := renderer.GetClipRect()
	draw()
	if old.W > 0 && old.H > 0 {
		_ = renderer.SetClipRect(&old)
	} else {
		_ = renderer.SetClipRect(nil)
	}
}

// vcFillBar fills an opaque bar with round ends: two cached end discs and one
// plain rect between them. The played part of the seek track grows a pixel at
// a time, and as one rounded rect every new width was a new cached texture —
// a render-target switch every few frames of every video.
func vcFillBar(renderer *sdl.Renderer, x, y, w, h int32, c sdl.Color) {
	if w <= 0 || h <= 0 {
		return
	}
	if w <= h {
		fillRoundedRect(renderer, x, y, h, h, h/2, c)
		return
	}
	fillRoundedRect(renderer, x, y, h, h, h/2, c)
	fillRoundedRect(renderer, x+w-h, y, h, h, h/2, c)
	setDrawColor(renderer, c)
	_ = renderer.FillRect(&sdl.Rect{X: x + h/2, Y: y, W: w - h, H: h})
}

// ── The controls' visibility ────────────────────────────────────────────

// vcVis is the controls' visibility: shown runs from 0 (away) to 1 (up) in
// time and is eased where it is drawn; tick is when it last moved; phase and
// sess are the last frame's, to notice a change; mouseX/mouseY the pointer's.
// Render thread.
var vcVis struct {
	shown  float64
	tick   uint64
	phase  string
	sess   *playbackSession
	mouseX int32
	mouseY int32
}

// vcChromeWantedFor reports whether the controls should be up for a player in
// phase: always while it is not playing (paused, opening, failed), while the
// settings sheet is open or a seek is gathering, and for vcIdleHideMS after
// the last input while it plays.
func vcChromeWantedFor(phase string) bool {
	if videoSettingsOpen || vcSeekPend.active || phase != "playing" {
		return true
	}
	return sdl.GetTicks64() < videoChromeUntil
}

// videoChromeWanted is vcChromeWantedFor the player's phase now.
func videoChromeWanted() bool {
	return vcChromeWantedFor(playerPhase())
}

// vcChromeShown reports controls someone can see and press: wanted, or still
// more than half there as they fade out. A press while they are not wakes
// them instead of acting (vcWakeChrome, HandleVideoControlClick).
func vcChromeShown() bool {
	return videoChromeWanted() || vcVis.shown > 0.5
}

// vcChromeTick advances the controls' fade for this frame and returns how much
// of them to draw, eased (0 = none). bounds is the player on screen. A change
// of session or phase (a new video, a pause, a resume) brings them back for a
// while, as does the pointer moving over the picture. Once they are away the
// D-pad ring goes with them: A is play/pause again, and the next Left/Right
// starts from Play. Render thread.
func vcChromeTick(state videoStateSnapshot, bounds sdl.Rect) float64 {
	now := sdl.GetTicks64()
	sess := playbackCurrentSession()
	if state.phase != vcVis.phase || sess != vcVis.sess {
		vcVis.phase, vcVis.sess = state.phase, sess
		revealVideoChrome()
	}
	if mouseX != vcVis.mouseX || mouseY != vcVis.mouseY {
		if vcVis.tick != 0 && vcHit(mouseX, mouseY, bounds) {
			revealVideoChrome()
		}
		vcVis.mouseX, vcVis.mouseY = mouseX, mouseY
	}
	target := 0.0
	if vcChromeWantedFor(state.phase) {
		target = 1
	}
	switch {
	case vcVis.tick == 0 || !vcFadesOn() || now < vcVis.tick || now-vcVis.tick > 500:
		// The first frame, Battery Saver, or a player that was not drawn for
		// a while (the floating window, another screen): no fade from a
		// state nobody saw.
		vcVis.shown = target
	case target > vcVis.shown:
		step := float64(now-vcVis.tick) / float64(motionSpeedMS(vcFadeInMS))
		vcVis.shown = math.Min(target, vcVis.shown+step)
	case target < vcVis.shown:
		step := float64(now-vcVis.tick) / float64(motionSpeedMS(vcFadeOutMS))
		vcVis.shown = math.Max(target, vcVis.shown-step)
	}
	vcVis.tick = now
	if vcVis.shown > 0 && vcVis.shown < 1 {
		requestFrameEvery(1)
	}
	if vcVis.shown <= 0 && videoControlFocus != vcNone && !videoSettingsOpen {
		videoControlFocus = vcNone
	}
	return vcSmooth(vcVis.shown)
}

// vcWakeChrome is a Left/Right while the controls are away: they come back,
// with the ring on Play / Pause, and the press does nothing else — a ring
// that moved while nobody could see it would make the next A a surprise. It
// reports whether it took the press.
func vcWakeChrome() bool {
	if vcChromeShown() {
		return false
	}
	revealVideoChrome()
	if videoControlDrawn(vcPlayPause) {
		videoControlFocus = vcPlayPause
	}
	PlayNavSound()
	return true
}

// vcDrawScrimsFaded copies the cached readability ramps at the controls'
// strength. Without render targets the ramps are drawn live and cannot fade:
// they come and go at the halfway point.
func vcDrawScrimsFaded(renderer *sdl.Renderer, config *Config, x, y, w, h int32, vis float64) {
	if cached := vkEnsureVideoOverlayCache(renderer, config, w, h); cached != nil {
		_ = cached.SetAlphaMod(uint8(uiClamp01(vis)*255 + 0.5))
		_ = renderer.Copy(cached, &sdl.Rect{X: 0, Y: 0, W: w, H: h}, &sdl.Rect{X: x, Y: y, W: w, H: h})
		_ = cached.SetAlphaMod(255)
		return
	}
	if vis >= 0.5 {
		drawVideoOverlayStatic(renderer, x, y, w, h)
	}
}

// vcDrawChromeFaded draws the controls at strength vis: straight to the screen
// when they are up, else through the shared UI layer at that opacity, the bar
// sunk and the top row raised a little (with motion on). Text and glass fade
// together that way, with no new label texture per step.
func vcDrawChromeFaded(renderer *sdl.Renderer, config *Config, state videoStateSnapshot, x, y, w, h int32, vis float64) {
	if vis >= 0.995 {
		vcDrawChrome(renderer, config, state, x, y, w, h, 0)
		return
	}
	sink := int32(0)
	if vcMotionOn() {
		sink = int32(math.Round((1 - vis) * vcSinkPx))
	}
	area := sdl.Rect{X: x, Y: y, W: w, H: h}
	vcKeepClip(renderer, func() {
		compositeUILayerIn(renderer, vis, 0, area, func() {
			vcDrawChrome(renderer, config, state, x, y, w, h, sink)
		})
	})
}

// ── What sits over the picture ──────────────────────────────────────────

// vcDrawOverlays draws what sits over the picture besides the controls: the
// subtitles (riding with the bar), the paused button or the resume bloom, the
// seek flash, the buffering spinner, the Up Next card, the volume pill and, on
// top, the settings sheet. vis is how much of the controls was drawn this
// frame; hasPicture says a frame is on screen (before the first one the frame
// area draws its own spinner). Render thread.
func vcDrawOverlays(renderer *sdl.Renderer, config *Config, state videoStateSnapshot, x, y, w, h int32, vis float64, hasPicture bool) {
	if renderer == nil {
		return
	}
	g := vcLast.geom
	now := sdl.GetTicks64()
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	// Subtitles sit above the bar (and Up Next's line) while it is up and
	// near the foot while it is away, gliding between the two.
	inset := vcSubsInsetHidden
	if g.bar.W > 0 {
		up := g.h - g.bar.Y + 14 + vcLast.upNextH
		inset += int32(math.Round(float64(up-vcSubsInsetHidden) * uiClamp01(vis)))
	}
	renderVideoSubtitles(renderer, config, x, y, w, h, state.progress*state.duration, inset)

	big := vcOffset(g.bigPlay, x, y)
	if state.phase == "paused" {
		vcDrawPausedButton(renderer, big, now)
	} else {
		vcDrawResumeBloom(renderer, big, now)
	}
	vcDrawSeekFlash(renderer, vcFont(config, 14), g, x, y)
	if vcBuffering(state, now) && hasPicture {
		vcDrawBuffering(renderer, x+w/2, y+g.flashY)
	}
	vcDrawUpNextCard(renderer, config, state, x, y, w, h, vis)
	vcDrawVolumeHUD(renderer, config, x, y, w, h, state.volume, now)
	vcDrawSheetAnimated(renderer, config, g, x, y, now)
}

// ── Seeks that gather ───────────────────────────────────────────────────

// vcSeekPending is a seek asked for and not started yet: its target (a
// fraction of the length), what the presses added up to (the flash says it),
// when it last moved, whether a pointer drag owns it, and the session it is
// for.
type vcSeekPending struct {
	active bool
	scrub  bool
	target float64
	total  float64
	at     uint64
	sess   *playbackSession
}

// vcSeekPend is the seek gathering now. Main thread.
var vcSeekPend vcSeekPending

// vcSeekBy gathers a relative seek (seekVideoBySeconds): the target moves at
// once — the handle, the clock and the flash show it — and the decoders start
// there vcSeekSettleMS after the last press (vcSeekTick).
func vcSeekBy(state videoStateSnapshot, seconds float64) {
	if state.duration <= 0 {
		return
	}
	sess := playbackCurrentSession()
	base, total := state.progress, seconds
	if vcSeekPend.active && !vcSeekPend.scrub && vcSeekPend.sess == sess {
		base, total = vcSeekPend.target, vcSeekPend.total+seconds
	}
	vcSeekPend = vcSeekPending{
		active: true,
		target: clampSeekFraction(base, state.duration, seconds),
		total:  total,
		at:     sdl.GetTicks64(),
		sess:   sess,
	}
	vcNoteSeek(total)
}

// vcScrubBegin starts a drag on the seek track at fraction: a press on it
// (HandleVideoControlClick).
func vcScrubBegin(fraction float64) {
	vcSeekPend = vcSeekPending{active: true, scrub: true, sess: playbackCurrentSession()}
	vcScrubMove(fraction)
}

// vcScrubMove follows the drag (main.go's pointer motion): the handle and the
// bubble move, and nothing restarts until the release.
func vcScrubMove(fraction float64) {
	if !vcSeekPend.active || !vcSeekPend.scrub || math.IsNaN(fraction) || math.IsInf(fraction, 0) {
		return
	}
	vcSeekPend.target = uiClamp01(fraction)
	vcSeekPend.at = sdl.GetTicks64()
	revealVideoChrome()
}

// vcSeekTick starts a gathered seek once its presses have settled, or a drag
// once the pointer lets go, and drops one whose video is gone. Main loop,
// every frame (videoSyncPlaybackEnd).
func vcSeekTick() {
	p := vcSeekPend
	if !p.active {
		return
	}
	if p.sess == nil || p.sess != playbackCurrentSession() || playerPhase() == "idle" {
		vcSeekPend = vcSeekPending{}
		return
	}
	if p.scrub {
		if isDraggingSeekBar {
			return
		}
	} else if sdl.GetTicks64()-p.at < vcSeekSettleMS {
		return
	}
	vcSeekCommit()
}

// vcSeekCommit starts the gathered seek now.
func vcSeekCommit() {
	p := vcSeekPend
	vcSeekPend = vcSeekPending{}
	if p.active && p.sess != nil && p.sess == playbackCurrentSession() {
		SeekVideo(p.target)
		revealVideoChrome()
	}
}

// vcSeekFlush starts a gathered seek before a press that acts on the position
// (play/pause), so that press acts on the place asked for. A drag is left to
// its release.
func vcSeekFlush() {
	if vcSeekPend.active && !vcSeekPend.scrub {
		vcSeekCommit()
	}
}

// vcSeekTake hands a gathered seek's target to a press that starts the
// decoders again anyway (a speed, a quality), so they start once, there. ok
// is false when no seek gathers for the video on screen; a drag is left to
// its release.
func vcSeekTake() (float64, bool) {
	if !vcSeeking() || vcSeekPend.scrub {
		return 0, false
	}
	target := vcSeekPend.target
	vcSeekPend = vcSeekPending{}
	return target, true
}

// vcSeeking reports a gathered seek or a drag for the video on screen.
func vcSeeking() bool {
	return vcSeekPend.active && vcSeekPend.sess != nil && vcSeekPend.sess == playbackCurrentSession()
}

// vcShownProgress is the position the bar and the clock show: a gathered
// seek's target while it waits, else where the video is.
func vcShownProgress(state videoStateSnapshot) float64 {
	if vcSeeking() {
		return vcSeekPend.target
	}
	return state.progress
}

// vcDrawTimeBubble is a time over the seek track: a small glass pill centred
// on x (kept between minX and maxX) whose foot is at bottom — where a seek or
// a drag will land, or what the pointer points at.
func vcDrawTimeBubble(renderer *sdl.Renderer, font *ttf.Font, x, bottom, minX, maxX int32, seconds float64) {
	if font == nil {
		return
	}
	label := vcFormatClock(seconds)
	tw, th := measureText(font, label)
	w, h := tw+20, th+10
	bx := x - w/2
	if bx > maxX-w {
		bx = maxX - w
	}
	if bx < minX {
		bx = minX
	}
	by := bottom - h
	fillRoundedRect(renderer, bx+1, by+3, w, h, h/2, sdl.Color{R: 0, G: 0, B: 0, A: 90})
	fillRoundedRect(renderer, bx, by, w, h, h/2, sdl.Color{R: 10, G: 16, B: 24, A: 236})
	strokeRoundedRect(renderer, bx, by, w, h, h/2, 1, WithAlpha(vcAccent(), 150))
	drawText(renderer, font, label, bx+w/2, by+(h-th)/2, vcHandle, textAlignCenter)
}

// vcTrackHover is the pointer over the seek row: its x, kept on the track t,
// and whether a visible pointer rests there.
func vcTrackHover(t sdl.Rect) (int32, bool) {
	if mouseHide.hidden || !vcHovered(videoControlRects.progress) {
		return 0, false
	}
	x := mouseX
	if x < t.X {
		x = t.X
	}
	if x > t.X+t.W {
		x = t.X + t.W
	}
	return x, true
}

// ── Waiting for frames ──────────────────────────────────────────────────

// vcBuf follows the frames arriving, for the spinner: seq is the last frame
// number seen and at when it changed (or when the video last was not
// playing). Render thread.
var vcBuf struct {
	seq uint64
	at  uint64
}

// vcBuffering reports a playing video whose picture is waiting: a new decoder
// (a seek, a speed change) past vcBufferShowMS without its first frame, or a
// stream that has delivered nothing for vcStallShowMS (or three frame times).
func vcBuffering(state videoStateSnapshot, now uint64) bool {
	if state.phase != "playing" || state.frameSeq != vcBuf.seq || vcBuf.at == 0 {
		vcBuf.seq, vcBuf.at = state.frameSeq, now
		return false
	}
	wait := uint64(vcStallShowMS)
	if state.frameRate > 0 {
		if three := uint64(3000 / state.frameRate); three > wait {
			wait = three
		}
	}
	if state.awaitFrame {
		wait = vcBufferShowMS
	}
	return now-vcBuf.at >= wait
}

// vcDrawBuffering is the spinner on a waiting picture: the loader's ring of
// dots on a dark disc, so it reads on a bright frame.
func vcDrawBuffering(renderer *sdl.Renderer, cx, cy int32) {
	fillCircle(renderer, cx, cy, 36, sdl.Color{R: 0, G: 0, B: 0, A: 130})
	vcSpinner(renderer, cx, cy, 20)
	requestFrameEvery(40)
}

// ── Play / pause feedback ───────────────────────────────────────────────

// vcPlayPulse is the last play/pause pressed on the player: a resume blooms
// the play glyph out of where the paused button was; a pause pops it in.
var vcPlayPulse struct {
	at      uint64
	resumed bool
}

// vcTogglePlayback is play/pause pressed on the player — A with no ring, the
// play buttons, K, a click on the picture — with its pulse. A gathered seek
// starts first; when that seek already resumed a paused video, the press's
// "play" is done.
func vcTogglePlayback() {
	wasPaused := playerPhase() == "paused"
	vcSeekFlush()
	if !wasPaused || playerPhase() == "paused" {
		ToggleVideoPlayback()
	}
	vcPlayPulse.at = sdl.GetTicks64()
	vcPlayPulse.resumed = playerPhase() == "playing"
	revealVideoChrome()
}

// vcDrawPausedButton draws the paused picture's play button, popping in for a
// moment after the pause: it fades up and, with motion on, swells to its size
// with a little overshoot.
func vcDrawPausedButton(renderer *sdl.Renderer, r sdl.Rect, now uint64) {
	scale, alpha := 1.0, 1.0
	if vcPlayPulse.at != 0 && !vcPlayPulse.resumed && now >= vcPlayPulse.at && vcFadesOn() {
		if age := now - vcPlayPulse.at; age < vcPopMS {
			t := float64(age) / vcPopMS
			alpha = uiEaseOutCubic(t * 1.6)
			if vcMotionOn() {
				scale = 0.8 + 0.2*homeGlideEaseOutBack(t, 1.6)
			}
			requestFrameEvery(1)
		}
	}
	vcDrawBigPlay(renderer, r, scale, alpha)
}

// vcDrawResumeBloom is the resume's answer: the play glyph on a dark disc
// where the paused button was, growing (with motion on) and fading out.
func vcDrawResumeBloom(renderer *sdl.Renderer, r sdl.Rect, now uint64) {
	if vcPlayPulse.at == 0 || !vcPlayPulse.resumed || r.W <= 0 {
		return
	}
	if !vcFadesOn() || now < vcPlayPulse.at || now-vcPlayPulse.at >= vcPulseMS {
		vcPlayPulse.at = 0
		return
	}
	t := float64(now-vcPlayPulse.at) / vcPulseMS
	fade := 1 - uiEaseOutCubic(t)
	grow := 1.0
	if vcMotionOn() {
		grow = 1 + 0.45*uiEaseOutCubic(t)
	}
	// The disc grows from its resting size, so one cached texture scales up;
	// the glyph keeps its size, so the icon atlas bakes it once.
	d := vcScaleRect(r, grow)
	fillRoundedRectAt(renderer, r, d, r.W/2, sdl.Color{R: 8, G: 14, B: 22, A: uint8(160 * fade)})
	strokeRoundedRectAt(renderer, r, d, r.W/2, 2, WithAlpha(vcAccent(), uint8(170*fade)))
	g := r.W * 2 / 5
	ink := vcAccent()
	ink.A = uint8(255 * fade)
	cx, cy := r.X+r.W/2, r.Y+r.H/2
	drawHomeGlyphTinted(renderer, "play", sdl.Rect{X: cx - g/2 + r.W/30, Y: cy - g/2, W: g, H: g}, ink)
	requestFrameEvery(1)
}

// ── Volume ──────────────────────────────────────────────────────────────

// vcVol is the volume pill: when the level last changed, and the level it
// shows, easing to the real one. Main thread.
var vcVol struct {
	at    uint64
	shown float64
	tick  uint64
}

// vcStepVolume is Up/Down (and the wheel) on the player: the level moves by
// delta, on a whole percent, and the pill shows it without bringing the rest
// of the controls back.
func vcStepVolume(delta float64) {
	before := snapshotVideoState().volume
	videoSetVolume(math.Round((before+delta)*100) / 100)
	vcShowVolume(before)
}

// vcShowVolume shows the volume pill; from is the level before the change,
// where a pill that was not up starts its fill.
func vcShowVolume(from float64) {
	now := sdl.GetTicks64()
	if vcVol.at == 0 || now < vcVol.at || now-vcVol.at >= vcVolumeHUDMS {
		vcVol.shown = from
		vcVol.tick = now
	}
	vcVol.at = now
}

// vcPercent is a level as a whole percentage. formatPercentSimple truncates,
// so a level stepped down to 0.3 read 29%.
func vcPercent(v float64) string {
	return strconv.Itoa(int(math.Round(uiClamp01(v)*100))) + "%"
}

// vcDrawVolumeHUD is the volume pill at the picture's right edge: a speaker, a
// level that fills from the foot, and the percentage, fading out
// vcVolumeHUDMS after the last change.
func vcDrawVolumeHUD(renderer *sdl.Renderer, config *Config, x, y, w, h int32, volume float64, now uint64) {
	if vcVol.at == 0 {
		return
	}
	if now < vcVol.at || now-vcVol.at >= vcVolumeHUDMS {
		vcVol.at = 0
		return
	}
	alpha := 1.0
	if age, fadeFrom := now-vcVol.at, uint64(vcVolumeHUDMS-vcVolumeFadeMS); age > fadeFrom {
		alpha = 1 - float64(age-fadeFrom)/vcVolumeFadeMS
	}
	if !vcFadesOn() {
		vcVol.shown = volume
	} else if now > vcVol.tick {
		k := 1 - math.Exp(-float64(now-vcVol.tick)/70)
		vcVol.shown += (volume - vcVol.shown) * k
	}
	vcVol.tick = now
	if math.Abs(vcVol.shown-volume) < 0.002 {
		vcVol.shown = volume
	}
	requestFrameEvery(1)

	pillW := int32(46)
	pillH := min32(184, h*48/100)
	if pillH < 110 || w < 200 {
		return
	}
	px := x + w - pillW - 24
	py := y + (h-pillH)/2 - 16
	if py < y+64 {
		py = y + 64
	}
	fillRoundedRect(renderer, px+2, py+4, pillW, pillH, pillW/2, sdl.Color{R: 0, G: 0, B: 0, A: uint8(80 * alpha)})
	fillRoundedRect(renderer, px, py, pillW, pillH, pillW/2, sdl.Color{R: 12, G: 18, B: 26, A: uint8(214 * alpha)})
	strokeRoundedRect(renderer, px, py, pillW, pillH, pillW/2, 1, sdl.Color{R: 255, G: 255, B: 255, A: uint8(40 * alpha)})
	ink := vcInk
	ink.A = uint8(255 * alpha)
	drawHomeGlyphTinted(renderer, vcVolumeIcon(volume), sdl.Rect{X: px + (pillW-22)/2, Y: py + 12, W: 22, H: 22}, ink)
	track := sdl.Rect{X: px + pillW/2 - 3, Y: py + 44, W: 6, H: pillH - 44 - 36}
	if track.H > 12 {
		fillRoundedRect(renderer, track.X, track.Y, track.W, track.H, 3, sdl.Color{R: 255, G: 255, B: 255, A: uint8(46 * alpha)})
		if fh := int32(float64(track.H) * uiClamp01(vcVol.shown)); fh > 0 {
			fh = max32(fh, track.W)
			acc := vcAccent()
			acc.A = uint8(255 * alpha)
			fillRoundedRect(renderer, track.X, track.Y+track.H-fh, track.W, fh, track.W/2, acc)
		}
	}
	if font := vcFont(config, 13); font != nil {
		label := vcPercent(volume)
		_, th := measureText(font, label)
		drawText(renderer, font, label, px+pillW/2, py+pillH-14-th, vcInkFade(vcInk2, alpha), textAlignCenter)
	}
}

// videoPlayerWheel makes the wheel over the player its volume (main.go's wheel
// path), the pill showing the level. It reports whether it took the turn.
func videoPlayerWheel(dy int32) bool {
	if dy == 0 || playerPhase() == "idle" || videoMiniVisible() || videoSettingsOpen {
		return false
	}
	if mediaSessionOrphaned(appConfig) {
		return false
	}
	if !videoFullscreen && !vcHit(mouseX, mouseY, videoControlBounds) {
		return false
	}
	step := 0.05
	if dy < 0 {
		step = -step
	}
	vcStepVolume(step)
	return true
}

// ── The settings sheet's entrance ───────────────────────────────────────

// vcSheetIn is the settings sheet's entrance: open is whether it was drawn
// last frame. Render thread.
var vcSheetIn struct {
	open  bool
	enter uiEnter
}

// vcSheetSel is the sheet's selection highlight as drawn: y glides towards the
// selected row's top. Render thread.
var vcSheetSel struct {
	on   bool
	y    float64
	tick uint64
	page int
}

// vcDrawSheetAnimated draws the settings sheet while it is open: it fades in
// and (with motion on) rises into place, through the shared UI layer, then is
// drawn straight. Closed, it forgets its tap target and its selection.
func vcDrawSheetAnimated(renderer *sdl.Renderer, config *Config, g vcGeom, x, y int32, now uint64) {
	if !videoSettingsOpen {
		vcSheetIn.open = false
		vcSheetSel.on = false
		videoSettingsSheetDrawn = sdl.Rect{}
		return
	}
	if !vcSheetIn.open {
		vcSheetIn.open = true
		if vcFadesOn() {
			vcSheetIn.enter.begin(now, vcSheetEnterMS)
		} else {
			vcSheetIn.enter.cancel()
		}
	}
	p := vcSheetIn.enter.progress(now)
	if p >= 1 {
		vcDrawSettingsSheet(renderer, config, g, x, y)
		return
	}
	rows := videoSheetRowIDsFor(videoSettingsPage, videoSheetContextNow())
	area, _ := vcSheetRect(g, x, y, len(rows))
	rise := int32(0)
	if vcMotionOn() {
		rise = int32(math.Round(uiPanelRise.at(p)))
	}
	vcKeepClip(renderer, func() {
		compositeUILayerIn(renderer, uiFadeIn.at(p), rise, area, func() {
			vcDrawSettingsSheet(renderer, config, g, x, y)
		})
	})
	requestFrameEvery(1)
}

// vcSheetSelectionY is where to draw the selected row's highlight this frame:
// gliding towards target (the row's top) when the selection moved within a
// page, straight there on a new page, a fresh sheet or with Focus bounce off.
func vcSheetSelectionY(target int32, page int) int32 {
	now := sdl.GetTicks64()
	s := &vcSheetSel
	switch {
	case !s.on || s.page != page || !vcGlideOn() || now < s.tick || now-s.tick > 250:
		s.y = float64(target)
	case now > s.tick:
		k := 1 - math.Exp(-float64(now-s.tick)/45)
		s.y += (float64(target) - s.y) * k
	}
	if math.Abs(s.y-float64(target)) < 0.5 {
		s.y = float64(target)
	} else {
		requestFrameEvery(1)
	}
	s.on, s.page, s.tick = true, page, now
	return int32(math.Round(s.y))
}

// ── Clicks on the picture ───────────────────────────────────────────────

// vcClick is the last click on the picture, for a double click: when, where,
// and whether it played or paused the video.
var vcClick struct {
	at      uint64
	x, y    int32
	toggled bool
}

// vcNear reports two click coordinates close enough for one double click.
func vcNear(a, b int32) bool {
	d := a - b
	return d > -24 && d < 24
}

// vcPictureClick is a click on the picture away from the controls: play or
// pause, the way a desktop player answers it, and a second click within
// vcDoubleClickMS full screen instead (the first click's play or pause taken
// back). A click on the bar's glass between its buttons does nothing.
func vcPictureClick(mx, my int32, state videoStateSnapshot) {
	if state.phase != "playing" && state.phase != "paused" {
		return // the stream is still opening, or failed
	}
	if vcHit(mx, my, vcOffset(vcLast.geom.bar, vcLast.x, vcLast.y)) {
		return
	}
	now := sdl.GetTicks64()
	if vcClick.at != 0 && now >= vcClick.at && now-vcClick.at <= vcDoubleClickMS && vcNear(mx, vcClick.x) && vcNear(my, vcClick.y) {
		if vcClick.toggled {
			ToggleVideoPlayback()
			vcPlayPulse.at = 0
		}
		vcClick.at = 0
		PlayActivateSound()
		toggleVideoFullscreen()
		return
	}
	vcClick.at, vcClick.x, vcClick.y = now, mx, my
	PlayActivateSound()
	vcTogglePlayback()
	vcClick.toggled = true
}

// ── Up next ─────────────────────────────────────────────────────────────

// vcUpNextCountdown is the seconds (of the viewer's time) before the video on
// screen hands over to Up Next, while that is under vcUpNextLeadSec away: a
// playing video, long enough for a countdown, with something queued and no
// Shorts feed that keeps the hand-off for itself.
func vcUpNextCountdown(state videoStateSnapshot) (float64, bool) {
	if state.phase != "playing" || state.duration < 2*vcUpNextLeadSec || shortsAutoplay || upNextLen() == 0 {
		return 0, false
	}
	left := (1 - uiClamp01(state.progress)) * state.duration / avSpeedFactor(state.speed)
	if left < 0 || left > vcUpNextLeadSec {
		return 0, false
	}
	return left, true
}

// vcDrawUpNextCard is the last seconds' card: what starts when this video ends
// and in how long, with a line that runs down. It shows with the controls away
// too (the bar's Up Next line stands aside meanwhile), above the bar while it
// is up and near the foot while it is not, gliding between.
func vcDrawUpNextCard(renderer *sdl.Renderer, config *Config, state videoStateSnapshot, x, y, w, h int32, vis float64) {
	left, ok := vcUpNextCountdown(state)
	if !ok {
		return
	}
	items := upNextSnapshot()
	capFont, titleFont := vcFont(config, 12), vcFont(config, 16)
	if len(items) == 0 || capFont == nil || titleFont == nil {
		return
	}
	cw, ch := min32(380, w*2/5), int32(70)
	if cw < 200 || h < 3*ch {
		return
	}
	g := vcLast.geom
	cx := x + w - cw - 20
	low := y + h - 20 - ch
	cy := low
	if g.bar.W > 0 {
		high := y + g.bar.Y - 14 - ch
		cy = low + int32(math.Round(float64(high-low)*uiClamp01(vis)))
	}
	fillRoundedRect(renderer, cx+2, cy+5, cw, ch, 14, sdl.Color{R: 0, G: 0, B: 0, A: 90})
	fillRoundedRect(renderer, cx, cy, cw, ch, 14, sdl.Color{R: 12, G: 18, B: 26, A: 234})
	strokeRoundedRect(renderer, cx, cy, cw, ch, 14, 1, sdl.Color{R: 255, G: 255, B: 255, A: 36})
	head := "UP NEXT · " + strconv.Itoa(int(math.Ceil(left))) + " s"
	drawText(renderer, capFont, head, cx+16, cy+12, vcAccent(), textAlignLeft)
	title := ellipsize(titleFont, upNextTitle(items[0], 80), cw-32)
	drawText(renderer, titleFont, title, cx+16, cy+32, vcInk, textAlignLeft)
	if line := int32(float64(cw-28) * uiClamp01(left/vcUpNextLeadSec)); line > 0 {
		setDrawColor(renderer, vcAccent())
		_ = renderer.FillRect(&sdl.Rect{X: cx + 14, Y: cy + ch - 7, W: line, H: 2})
	}
}

// ── Frame-locked presentation ───────────────────────────────────────────

// The decoder publishes a frame at the moment the sound says it is due
// (avsync.go). The main loop used to pick it up whenever its own cadence came
// round — 30 fps on the handheld, 60 on a PC — so each frame waited anywhere
// up to a whole loop frame, and as the two clocks drifted apart one frame was
// now and then shown twice and the next one never: the small hitch every few
// seconds in a slow pan. The loop now wakes when a frame is published
// (framePressSleep) and shows it at once; the active cadence stays the
// ceiling, so a 60 fps video on the handheld is still drawn at 30.
var (
	// videoFrameFresh is a published frame no player has uploaded yet.
	videoFrameFresh atomic.Bool
	// videoUploadAt is when a player on screen last asked for its frame
	// (UpdateCurrentTexture): a video nothing draws never wakes the loop.
	videoUploadAt atomic.Uint64
	// videoWakeAt is when the loop last woke early for a frame.
	videoWakeAt atomic.Uint64
)

// videoFramePolling reports a player drawn within the last quarter second: the
// frame's sleep then looks for its frames every videoFramePollMS.
func videoFramePolling(now uint64) bool {
	at := videoUploadAt.Load()
	return at != 0 && now >= at && now-at <= 250
}

// videoFrameWake reports a published frame worth waking the main loop for: a
// player on screen asked for frames lately, and the last early wake is at
// least the active cadence ago, so the loop never runs faster than it would
// anyway. Main thread (framePressSleep).
func videoFrameWake(now uint64) bool {
	if !videoFrameFresh.Load() || !videoFramePolling(now) {
		return false
	}
	floor := uint64(14)
	if IsTrimuiDevice() {
		floor = 30
	}
	if last := videoWakeAt.Load(); now >= last && now-last < floor {
		return false
	}
	videoWakeAt.Store(now)
	return true
}
