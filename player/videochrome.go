package main

// videochrome.go — the video player's on-screen controls.
//
// The controls follow the Media screen's design sheet (mockups/tube-16x9.html)
// in Home's glass language: a frosted bar along the bottom of the picture with
// a thick seek track, its handle and the time at both ends; a centred
// transport — rewind, a round accent play button, fast-forward, and previous /
// next when the video came from a list; the volume at the left; speed,
// captions, settings and full screen at the right. A state chip and the title
// sit top-left, minimise and close top-right. Every button is a vector icon
// from the shared set (icondefs.go) on a quiet glass well that lights up under
// the pointer and wears Home's accent ring under the D-pad, and a toggle that
// is on (captions, full screen) stays tinted.
//
// What it replaced: eight 36px slate squares whose icons were drawn out of
// lines and triangles, a 6px track, a "PLAYING" badge, a volume reading in the
// corner and two unlabelled speaker buttons. Their colours came from the
// theme, so a light preset drew dark icons over the video. The chrome is always
// light ink on dark glass now — it sits on a picture, not on the theme.
//
// One geometry per frame (computeVideoControlLayout) feeds the drawing, the tap
// targets and the D-pad ring, so a control is never drawn in one place and
// pressed in another. Only the two scrims go into the cached static layer
// (vkEnsureVideoOverlayCache): they are black ramps, which survive the
// render-target blend unchanged; every glass shape is a shape-cache copy drawn
// live, which costs no more than the cached bar did.

import (
	"math"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// Chrome metrics, in pixels of the player rect.
const (
	vcBarPadWide   = int32(16) // the bar's inset from the picture's edges
	vcBarPadNarrow = int32(12)
	vcBarH         = int32(108)
	vcBarRadius    = int32(16)
	vcSeekRowTop   = int32(12) // the seek row's offset inside the bar
	vcSeekRowH     = int32(28)
	vcRowGap       = int32(6)
	vcTransportH   = int32(52)
	vcBtn          = int32(44) // a tool button (volume, captions, settings, ...)
	vcSeekBtn      = int32(48) // rewind and fast-forward
	vcSkipBtn      = int32(44) // previous and next
	vcPlayBtn      = int32(60) // the round play button
	vcTopBtn       = int32(40) // the round corner buttons
	vcSpeedW       = int32(56)
	vcSpeedH       = int32(36)
	vcVolBarW      = int32(64)
	vcTrackH       = int32(6)
	vcHandleR      = int32(9)
	vcTopScrimH    = int32(120)
	vcBtnRadius    = int32(12)
	vcClusterGap   = int32(12) // the least space between the three clusters
	vcBigPlay      = int32(88) // the paused picture's play button
	vcFlashMS      = 700       // how long a seek's "−10 s" stays on the picture
)

// The chrome's inks: fixed, because the chrome is drawn over a picture.
var (
	vcInk      = sdl.Color{R: 234, G: 242, B: 247, A: 255}
	vcInk2     = sdl.Color{R: 199, G: 211, B: 220, A: 255}
	vcInk3     = sdl.Color{R: 140, G: 158, B: 172, A: 255}
	vcGlass    = sdl.Color{R: 18, G: 26, B: 35, A: 168}
	vcHandle   = sdl.Color{R: 234, G: 252, B: 255, A: 255}
	vcLiveInk  = sdl.Color{R: 255, G: 107, B: 120, A: 255}
	vcShadowed = sdl.Color{R: 0, G: 0, B: 0, A: 150}
)

// vcAccent is the theme's accent, the colour of focus everywhere in the app.
func vcAccent() sdl.Color { return glassAccent() }

// vcGeom is the chrome laid out for one player rect, relative to its top-left
// corner. A zero rect is a control this layout dropped for room.
type vcGeom struct {
	w, h       int32
	bar        sdl.Rect
	seekY      int32 // the seek row's centre line
	track      sdl.Rect
	timeL      int32 // left edge of the elapsed time
	timeR      int32 // right edge of the remaining time
	rowY       int32 // the transport row's centre line
	mute       sdl.Rect
	volBar     sdl.Rect
	volTextX   int32 // 0 when the reading was dropped
	prev       sdl.Rect
	rewind     sdl.Rect
	play       sdl.Rect
	forward    sdl.Rect
	next       sdl.Rect
	speed      sdl.Rect
	divider    sdl.Rect
	subs       sdl.Rect
	settings   sdl.Rect
	fullscreen sdl.Rect
	minimize   sdl.Rect
	close      sdl.Rect
	bigPlay    sdl.Rect
	flashY     int32 // centre line of the seek flash and the big play button
}

// vcNoRects is videoControlRects with nothing in it (it is initialised from the
// zero value), for a frame that draws no bar.
var vcNoRects = videoControlRects

// vcLast is this frame's layout and where it was placed, for the live layer.
var vcLast struct {
	geom vcGeom
	x, y int32
}

// vcFont is the chrome's type at a design size.
func vcFont(config *Config, size int) *ttf.Font {
	if config == nil {
		return nil
	}
	return getFontSized(config, "small", size)
}

// vcTimeSlot is the width the time labels are laid out in: a template of the
// widest digits, so the track does not shift as the clock ticks.
func vcTimeSlot(font *ttf.Font, duration float64) int32 {
	template := "88:88"
	if duration >= 3600 {
		template = "8:88:88"
	}
	if font == nil {
		return int32(len(template)) * 8
	}
	w, _ := measureText(font, template)
	return w
}

// vcLayoutFor lays the chrome out for a w×h player. skip says whether the
// video came from a list (previous / next are shown); slotL and slotR are the
// time labels' widths. When the bar is too narrow for everything centred, the
// transport first slides off centre into the room between the side clusters;
// failing that the clusters give up their least important parts — previous /
// next, then the volume reading or the speed pill, whichever side is short.
func vcLayoutFor(w, h int32, skip bool, slotL, slotR int32) vcGeom {
	g := vcGeom{w: w, h: h}
	pad := vcBarPadWide
	if w < 720 {
		pad = vcBarPadNarrow
	}
	g.bar = sdl.Rect{X: pad, Y: h - pad - vcBarH, W: w - 2*pad, H: vcBarH}
	if g.bar.W <= 0 || g.bar.Y < 0 {
		return g
	}

	// The seek row: elapsed time, the track, the time left.
	g.seekY = g.bar.Y + vcSeekRowTop + vcSeekRowH/2
	g.timeL = g.bar.X + 18
	g.timeR = g.bar.X + g.bar.W - 18
	trackX := g.timeL + slotL + 14
	trackW := g.timeR - slotR - 14 - trackX
	if trackW > 0 {
		g.track = sdl.Rect{X: trackX, Y: g.seekY - vcTrackH/2, W: trackW, H: vcTrackH}
	}

	// The transport row.
	g.rowY = g.bar.Y + vcSeekRowTop + vcSeekRowH + vcRowGap + vcTransportH/2
	square := func(x, size int32) sdl.Rect {
		return sdl.Rect{X: x, Y: g.rowY - size/2, W: size, H: size}
	}
	left := g.bar.X + 10
	right := g.bar.X + g.bar.W - 10
	centre := g.bar.X + g.bar.W/2

	reading, speed := true, true
	var cx int32
	for placed := false; !placed; {
		leftEnd := left + vcBtn
		if reading {
			leftEnd += 6 + vcVolBarW + 10 + 36
		}
		rightStart := right - 3*vcBtn - 2*6
		if speed {
			rightStart -= 8 + 1 + 8 + vcSpeedW
		}
		centreW := vcSeekBtn*2 + vcPlayBtn + 2*14
		if skip {
			centreW += 2 * (vcSkipBtn + 14)
		}
		cx = centre - centreW/2
		leftShort := cx < leftEnd+vcClusterGap
		rightShort := cx+centreW > rightStart-vcClusterGap
		switch {
		case !leftShort && !rightShort:
			placed = true
		case rightStart-leftEnd-2*vcClusterGap >= centreW:
			// Off the bar's centre is better than a missing control: the
			// transport slides into the room between the side clusters.
			if leftShort {
				cx = leftEnd + vcClusterGap
			} else {
				cx = rightStart - vcClusterGap - centreW
			}
			placed = true
		case skip:
			skip = false
		case leftShort && reading:
			reading = false
		case rightShort && speed:
			speed = false
		case reading:
			reading = false
		case speed:
			speed = false
		default:
			// Nothing left to give up: centre in whatever room there is.
			cx = leftEnd + (rightStart-leftEnd-centreW)/2
			placed = true
		}
	}

	g.mute = square(left, vcBtn)
	if reading {
		g.volBar = sdl.Rect{X: g.mute.X + g.mute.W + 6, Y: g.rowY - 10, W: vcVolBarW, H: 20}
		g.volTextX = g.volBar.X + g.volBar.W + 10
	}
	x := cx
	if skip {
		g.prev = square(x, vcSkipBtn)
		x += vcSkipBtn + 14
	}
	g.rewind = square(x, vcSeekBtn)
	x += vcSeekBtn + 14
	g.play = square(x, vcPlayBtn)
	x += vcPlayBtn + 14
	g.forward = square(x, vcSeekBtn)
	x += vcSeekBtn + 14
	if skip {
		g.next = square(x, vcSkipBtn)
	}
	rx := right - vcBtn
	g.fullscreen = square(rx, vcBtn)
	rx -= 6 + vcBtn
	g.settings = square(rx, vcBtn)
	rx -= 6 + vcBtn
	g.subs = square(rx, vcBtn)
	if speed {
		rx -= 8 + 1
		g.divider = sdl.Rect{X: rx, Y: g.rowY - 12, W: 1, H: 24}
		rx -= 8 + vcSpeedW
		g.speed = sdl.Rect{X: rx, Y: g.rowY - vcSpeedH/2, W: vcSpeedW, H: vcSpeedH}
	}

	// The corners: minimise (one size down, keep playing) and close.
	g.close = sdl.Rect{X: w - 20 - vcTopBtn, Y: 16, W: vcTopBtn, H: vcTopBtn}
	g.minimize = sdl.Rect{X: g.close.X - 10 - vcTopBtn, Y: 16, W: vcTopBtn, H: vcTopBtn}

	// The paused picture's play button and the seek flash share the middle of
	// the picture above the bar.
	g.flashY = (64 + g.bar.Y) / 2
	g.bigPlay = sdl.Rect{X: (w - vcBigPlay) / 2, Y: g.flashY - vcBigPlay/2, W: vcBigPlay, H: vcBigPlay}
	return g
}

// vcOffset moves a layout-relative rect onto the screen; empty stays empty.
func vcOffset(r sdl.Rect, x, y int32) sdl.Rect {
	if r.W <= 0 || r.H <= 0 {
		return sdl.Rect{}
	}
	return sdl.Rect{X: r.X + x, Y: r.Y + y, W: r.W, H: r.H}
}

// vcHit reports a tap on a control that is drawn. A dropped control's rect is
// empty, and pointInRect counts an empty rect at the origin as hit.
func vcHit(mx, my int32, r sdl.Rect) bool {
	return r.W > 0 && r.H > 0 && pointInRect(mx, my, r)
}

// vcHovered reports whether the pointer rests on r. The handheld has no
// pointer, so its stale coordinates are never on a control.
func vcHovered(r sdl.Rect) bool {
	return !videoSettingsOpen && vcHit(mouseX, mouseY, r)
}

// vcQueueHasList reports whether the playing video came from a list with
// somewhere to skip to: only then do previous and next have a job.
func vcQueueHasList() bool {
	return playbackQueue.index >= 0 && len(playbackQueue.videos) > 1
}

// vcLayout computes the chrome for the player at (x, y, w, h) and publishes it
// to the tap targets and the D-pad ring. RenderVideoOverlay calls it every
// frame before anything is drawn.
func vcLayout(config *Config, x, y, w, h int32, state videoStateSnapshot) {
	font := vcFont(config, 13)
	slotL := vcTimeSlot(font, state.duration)
	slotR := slotL
	if font != nil {
		mw, _ := measureText(font, "−")
		slotR += mw
	}
	g := vcLayoutFor(w, h, vcQueueHasList(), slotL, slotR)
	vcLast.geom, vcLast.x, vcLast.y = g, x, y

	videoControlBounds = sdl.Rect{X: x, Y: y, W: w, H: h}
	r := &videoControlRects
	r.mute = vcOffset(g.mute, x, y)
	r.volBar = vcOffset(g.volBar, x, y)
	r.prev = vcOffset(g.prev, x, y)
	r.seekBack = vcOffset(g.rewind, x, y)
	r.playPause = vcOffset(g.play, x, y)
	r.seekFwd = vcOffset(g.forward, x, y)
	r.next = vcOffset(g.next, x, y)
	r.speed = vcOffset(g.speed, x, y)
	r.speedCycle = r.speed
	r.subs = vcOffset(g.subs, x, y)
	r.settings = vcOffset(g.settings, x, y)
	r.fullscreen = vcOffset(g.fullscreen, x, y)
	r.back = vcOffset(g.minimize, x, y)
	r.close = vcOffset(g.close, x, y)
	// The old bar's controls that have no place in this one.
	r.stop, r.volDown, r.volUp = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	// The seek target is the whole row's height, so the thin track is easy to
	// grab; its x span stays the track's, which the drag maps to a fraction.
	r.progress = sdl.Rect{}
	if g.track.W > 0 && state.duration > 0 {
		r.progress = sdl.Rect{X: x + g.track.X, Y: y + g.seekY - vcSeekRowH/2, W: g.track.W, H: vcSeekRowH}
	}
	r.overlayPlay = sdl.Rect{}
	if state.phase == "paused" {
		r.overlayPlay = vcOffset(g.bigPlay, x, y)
	}
}

// ── Static layer ─────────────────────────────────────────────────────────

// vcDrawScrims paints the readability ramps at (ox, oy): dark at the top edge
// behind the title, and a deeper one rising from the bottom behind the bar.
// They are the cached part of the chrome.
func vcDrawScrims(renderer *sdl.Renderer, ox, oy, w, h int32) {
	if renderer == nil || w <= 0 || h <= 0 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	top := min32(vcTopScrimH, h/3)
	const step = 3
	for s := int32(0); s < top; s += step {
		t := 1 - float64(s)/float64(top)
		renderer.SetDrawColor(0, 0, 0, uint8(150*t*t))
		_ = renderer.FillRect(&sdl.Rect{X: ox, Y: oy + s, W: w, H: min32(step, top-s)})
	}
	bottom := min32(230, h*46/100)
	by := oy + h - bottom
	for s := int32(0); s < bottom; s += step {
		t := float64(s) / float64(bottom)
		renderer.SetDrawColor(4, 8, 12, uint8(225*math.Pow(t, 1.6)))
		_ = renderer.FillRect(&sdl.Rect{X: ox, Y: by + s, W: w, H: min32(step, bottom-s)})
	}
}

// ── Live layer ───────────────────────────────────────────────────────────

// vcRing is Home's focus ring standing off a control: 3px of accent with a
// 3px gap, the radius grown to stay concentric.
func vcRing(renderer *sdl.Renderer, r sdl.Rect, radius int32) {
	const gap, width = int32(3), int32(3)
	out := gap + width
	strokeRoundedRect(renderer, r.X-out, r.Y-out, r.W+2*out, r.H+2*out, radius+out, width, vcAccent())
}

// vcButton is one icon button: a glass well under the pointer or the ring,
// the accent tint for a toggle that is on, a dimmed icon when it can do
// nothing right now.
func vcButton(renderer *sdl.Renderer, r sdl.Rect, icon string, iconSize int32, focused, on, disabled bool) {
	if renderer == nil || r.W <= 0 {
		return
	}
	acc := vcAccent()
	hovered := vcHovered(r)
	switch {
	case on:
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, vcBtnRadius, WithAlpha(acc, 40))
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, vcBtnRadius, 1, WithAlpha(acc, 115))
	case focused:
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, vcBtnRadius, sdl.Color{R: 255, G: 255, B: 255, A: 32})
	case hovered && !disabled:
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, vcBtnRadius, sdl.Color{R: 255, G: 255, B: 255, A: 24})
	}
	if focused {
		vcRing(renderer, r, vcBtnRadius)
	}
	ink := vcInk2
	switch {
	case disabled:
		ink = WithAlpha(vcInk3, 140)
	case focused || on:
		ink = acc
	case hovered:
		ink = vcInk
	}
	g := min32(iconSize, r.W-8)
	drawHomeGlyphTinted(renderer, icon, sdl.Rect{X: r.X + (r.W-g)/2, Y: r.Y + (r.H-g)/2, W: g, H: g}, ink)
}

// vcRoundButton is a corner button: a small glass disc with its icon.
func vcRoundButton(renderer *sdl.Renderer, r sdl.Rect, icon string, focused bool) {
	if renderer == nil || r.W <= 0 {
		return
	}
	radius := r.W / 2
	fill := vcGlass
	if focused || vcHovered(r) {
		fill.A = 220
	}
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, fill)
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 1, sdl.Color{R: 255, G: 255, B: 255, A: 40})
	ink := vcInk
	if focused {
		vcRing(renderer, r, radius)
		ink = vcAccent()
	}
	g := r.W / 2
	drawHomeGlyphTinted(renderer, icon, sdl.Rect{X: r.X + (r.W-g)/2, Y: r.Y + (r.H-g)/2, W: g, H: g}, ink)
}

// vcPlayButton is the round accent play / pause button at the centre of the
// transport, the one control lit at rest.
func vcPlayButton(renderer *sdl.Renderer, r sdl.Rect, playing, focused bool) {
	if renderer == nil || r.W <= 0 {
		return
	}
	acc := vcAccent()
	cx, cy, rad := r.X+r.W/2, r.Y+r.H/2, r.W/2
	glow := uint8(56)
	if focused || vcHovered(r) {
		glow = 96
	}
	homeGlowBlob(renderer, sdl.Rect{X: cx - rad*2, Y: cy - rad*2, W: rad * 4, H: rad * 4}, acc, glow)
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, rad, sdl.Color{R: 8, G: 22, B: 30, A: 225})
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, rad, WithAlpha(acc, 70))
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, rad, 1, WithAlpha(acc, 170))
	if focused {
		vcRing(renderer, r, rad)
	}
	icon, nudge := "pause", int32(0)
	if !playing {
		// A play triangle's mass sits left of its box; nudge it to look centred.
		icon, nudge = "play", r.W/24
	}
	g := r.W * 7 / 15
	drawHomeGlyphTinted(renderer, icon, sdl.Rect{X: cx - g/2 + nudge, Y: cy - g/2, W: g, H: g}, vcHandle)
}

// vcSpeedLabel is the speed pill's text: "1×", "1.25×", "0.5×".
func vcSpeedLabel(speed float64) string {
	if speed <= 0 {
		speed = 1
	}
	return strconv.FormatFloat(math.Round(speed*100)/100, 'f', -1, 64) + "×"
}

// vcFormatClock is a playback time the way players write it: m:ss, or h:mm:ss
// past the hour.
func vcFormatClock(seconds float64) string {
	if seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		seconds = 0
	}
	s := int(seconds)
	h, m, sec := s/3600, (s%3600)/60, s%60
	if h > 0 {
		return strconv.Itoa(h) + ":" + pad2(m) + ":" + pad2(sec)
	}
	return strconv.Itoa(m) + ":" + pad2(sec)
}

// vcVolumeIcon is the speaker that shows the level at a glance.
func vcVolumeIcon(volume float64) string {
	switch {
	case volume <= 0.001:
		return "volume-mute"
	case volume < 0.5:
		return "volume-low"
	}
	return "volume"
}

// vcText draws a line with a soft shadow, so it stays legible over a bright
// frame without a plate behind it.
func vcText(renderer *sdl.Renderer, font *ttf.Font, text string, x, y int32, ink sdl.Color, align textAlignment) {
	if font == nil || text == "" {
		return
	}
	drawText(renderer, font, text, x+1, y+1, vcShadowed, align)
	drawText(renderer, font, text, x, y, ink, align)
}

// vcDrawChip is the state chip: a pill with a dot, naming what the player is
// doing. It returns the chip's bottom edge.
func vcDrawChip(renderer *sdl.Renderer, font *ttf.Font, x, y int32, state videoStateSnapshot) int32 {
	if font == nil {
		return y
	}
	label, ink, lit := "PLAYING", vcAccent(), true
	switch {
	case state.phase == "paused":
		label, ink, lit = "PAUSED", vcInk2, false
	case state.phase == "error":
		label, ink, lit = "ERROR", DangerInk(), false
	case state.duration <= 0:
		// A stream with no length is live: Live TV, a radio feed, a webcam.
		label, ink = "LIVE", vcLiveInk
	}
	tw, th := measureText(font, label)
	const h = int32(26)
	w := 24 + tw + 12
	fillRoundedRect(renderer, x, y, w, h, h/2, sdl.Color{R: 6, G: 20, B: 26, A: 170})
	strokeRoundedRect(renderer, x, y, w, h, h/2, 1, WithAlpha(ink, 110))
	if lit {
		homeGlowBlob(renderer, sdl.Rect{X: x + 13 - 9, Y: y + h/2 - 9, W: 18, H: 18}, ink, 150)
	}
	fillCircle(renderer, x+13, y+h/2, 3, ink)
	drawText(renderer, font, label, x+24, y+(h-th)/2, ink, textAlignLeft)
	return y + h
}

// vcDrawSeek is the seek row: the elapsed time, the track with its played
// part and handle, and the time left. A live stream has no position, so its
// row says LIVE over an empty track.
func vcDrawSeek(renderer *sdl.Renderer, font *ttf.Font, g vcGeom, ox, oy int32, state videoStateSnapshot) {
	t := vcOffset(g.track, ox, oy)
	if t.W <= 0 {
		return
	}
	acc := vcAccent()
	grab := isDraggingSeekBar || vcHovered(videoControlRects.progress)
	if grab {
		t.Y--
		t.H += 2
	}
	fillRoundedRect(renderer, t.X, t.Y, t.W, t.H, t.H/2, sdl.Color{R: 255, G: 255, B: 255, A: 42})
	cy := oy + g.seekY
	if state.duration <= 0 {
		if font != nil {
			_, th := measureText(font, "LIVE")
			drawText(renderer, font, "LIVE", ox+g.timeL, cy-th/2, vcLiveInk, textAlignLeft)
		}
		return
	}
	progress := uiClamp01(state.progress)
	fw := int32(float64(t.W) * progress)
	if fw > 0 {
		fillRoundedRect(renderer, t.X, t.Y, max32(fw, t.H), t.H, t.H/2, acc)
	}
	r := vcHandleR
	if grab {
		r += 2
	}
	hx := t.X + fw
	homeGlowBlob(renderer, sdl.Rect{X: hx - r*3, Y: cy - r*3, W: r * 6, H: r * 6}, acc, 120)
	fillCircle(renderer, hx, cy, r, acc)
	fillCircle(renderer, hx, cy, r-3, vcHandle)
	if font == nil {
		return
	}
	current := state.progress * state.duration
	elapsed := vcFormatClock(current)
	left := "−" + vcFormatClock(state.duration-current)
	_, th := measureText(font, elapsed)
	drawText(renderer, font, elapsed, ox+g.timeL, cy-th/2, vcInk2, textAlignLeft)
	drawText(renderer, font, left, ox+g.timeR, cy-th/2, vcInk3, textAlignRight)
}

// vcDrawVolume is the volume cluster: the speaker (a mute toggle) and, when
// there is room, the level bar and its percentage.
func vcDrawVolume(renderer *sdl.Renderer, font *ttf.Font, g vcGeom, ox, oy int32, volume float64) {
	vcButton(renderer, vcOffset(g.mute, ox, oy), vcVolumeIcon(volume), 24, videoControlFocus == vcMute, false, false)
	bar := vcOffset(g.volBar, ox, oy)
	if bar.W <= 0 {
		return
	}
	track := sdl.Rect{X: bar.X, Y: bar.Y + bar.H/2 - 2, W: bar.W, H: 5}
	fillRoundedRect(renderer, track.X, track.Y, track.W, track.H, 2, sdl.Color{R: 255, G: 255, B: 255, A: 42})
	if fw := int32(float64(track.W) * uiClamp01(volume)); fw > 0 {
		ink := vcInk2
		if vcHovered(bar) {
			ink = vcAccent()
		}
		fillRoundedRect(renderer, track.X, track.Y, max32(fw, track.H), track.H, 2, ink)
	}
	if font != nil && g.volTextX > 0 {
		label := formatPercentSimple(volume)
		_, th := measureText(font, label)
		drawText(renderer, font, label, ox+g.volTextX, oy+g.rowY-th/2, vcInk3, textAlignLeft)
	}
}

// vcDrawSpeed is the speed pill: the current rate, pressed to step it.
func vcDrawSpeed(renderer *sdl.Renderer, font *ttf.Font, r sdl.Rect, speed float64, focused bool) {
	if r.W <= 0 {
		return
	}
	fill := sdl.Color{R: 255, G: 255, B: 255, A: 18}
	if focused || vcHovered(r) {
		fill.A = 32
	}
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, vcBtnRadius, fill)
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, vcBtnRadius, 1, sdl.Color{R: 255, G: 255, B: 255, A: 34})
	ink := vcInk
	if focused {
		vcRing(renderer, r, vcBtnRadius)
		ink = vcAccent()
	}
	if font == nil {
		return
	}
	label := vcSpeedLabel(speed)
	if lw, _ := measureText(font, label); lw > r.W-12 {
		label = ellipsize(font, label, r.W-12)
	}
	_, th := measureText(font, label)
	drawText(renderer, font, label, r.X+r.W/2, r.Y+(r.H-th)/2, ink, textAlignCenter)
}

// vcDrawChrome draws everything that changes while a video plays, over the
// cached scrims: the chip, title and corner buttons, the bar with its seek row
// and transport, the subtitle cue, Up Next, the settings sheet, and the big
// play button and seek flash over the picture.
func vcDrawChrome(renderer *sdl.Renderer, config *Config, state videoStateSnapshot, x, y, w, h int32) {
	if renderer == nil {
		return
	}
	g := vcLast.geom
	if g.w != w || g.h != h || vcLast.x != x || vcLast.y != y {
		vcLayout(config, x, y, w, h, state)
		g = vcLast.geom
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	paused := state.phase == "paused"
	if paused {
		// A paused frame steps back a little, so the play button reads first.
		renderer.SetDrawColor(0, 0, 0, 56)
		_ = renderer.FillRect(&sdl.Rect{X: x, Y: y, W: w, H: h})
	}

	// Top-left: the state chip and the title.
	chipFont := vcFont(config, 12)
	chipBottom := vcDrawChip(renderer, chipFont, x+20, y+18, state)
	if titleFont := vcFont(config, 19); titleFont != nil {
		if title, ok := currentMediaTitle(); ok && strings.TrimSpace(title) != "" {
			maxW := vcOffset(g.minimize, x, y).X - 24 - (x + 20)
			if g.minimize.W <= 0 {
				maxW = w - 40
			}
			if maxW > 40 {
				vcText(renderer, titleFont, ellipsize(titleFont, strings.TrimSpace(title), maxW), x+20, chipBottom+10, vcInk, textAlignLeft)
			}
		}
	}
	// Top-right: minimise, then close.
	vcRoundButton(renderer, vcOffset(g.minimize, x, y), "chevron-down", videoControlFocus == vcBack)
	vcRoundButton(renderer, vcOffset(g.close, x, y), "close", videoControlFocus == vcClose)

	// The bar.
	bar := vcOffset(g.bar, x, y)
	if bar.W > 0 {
		fillRoundedRect(renderer, bar.X, bar.Y, bar.W, bar.H, vcBarRadius, vcGlass)
		strokeRoundedRect(renderer, bar.X, bar.Y, bar.W, bar.H, vcBarRadius, 1, sdl.Color{R: 255, G: 255, B: 255, A: 30})
		setDrawColor(renderer, sdl.Color{R: 255, G: 255, B: 255, A: 16})
		_ = renderer.DrawLine(bar.X+vcBarRadius, bar.Y+1, bar.X+bar.W-vcBarRadius, bar.Y+1)
	}
	timeFont := vcFont(config, 13)
	vcDrawSeek(renderer, timeFont, g, x, y, state)
	vcDrawVolume(renderer, timeFont, g, x, y, state.volume)

	// The transport. Seeking needs a length: a live stream dims its pair.
	live := state.duration <= 0
	vcButton(renderer, vcOffset(g.prev, x, y), "skip-back", 22, videoControlFocus == vcPrev, false, false)
	vcButton(renderer, vcOffset(g.rewind, x, y), "rewind", 26, videoControlFocus == vcSeekBack, false, live)
	vcPlayButton(renderer, vcOffset(g.play, x, y), !paused, videoControlFocus == vcPlayPause)
	vcButton(renderer, vcOffset(g.forward, x, y), "fast-forward", 26, videoControlFocus == vcSeekFwd, false, live)
	vcButton(renderer, vcOffset(g.next, x, y), "skip-forward", 22, videoControlFocus == vcNext, false, false)

	// The tools.
	vcDrawSpeed(renderer, vcFont(config, 14), vcOffset(g.speed, x, y), state.speed, videoControlFocus == vcSpeed)
	if d := vcOffset(g.divider, x, y); d.W > 0 {
		setDrawColor(renderer, sdl.Color{R: 255, G: 255, B: 255, A: 36})
		_ = renderer.FillRect(&d)
	}
	vcButton(renderer, vcOffset(g.subs, x, y), "subtitles", 24, videoControlFocus == vcSubs, subtitlesEnabled(), false)
	vcButton(renderer, vcOffset(g.settings, x, y), "settings", 24, videoControlFocus == vcSettings, videoSettingsOpen, false)
	fsIcon := "fullscreen"
	if videoFullscreen {
		fsIcon = "fullscreen-exit"
	}
	vcButton(renderer, vcOffset(g.fullscreen, x, y), fsIcon, 24, videoControlFocus == vcFullscreen, videoFullscreen, false)

	// The subtitle cue and Up Next sit above the bar, Up Next nearest to it.
	upNextH := int32(0)
	if line := upNextSummary(); line != "" && timeFont != nil && bar.W > 0 {
		_, lh := measureText(timeFont, line)
		upNextH = lh + 10
		vcText(renderer, timeFont, ellipsize(timeFont, line, bar.W-40), x+w/2, bar.Y-8-lh, vcInk2, textAlignCenter)
	}
	subInset := int32(28)
	if bar.W > 0 {
		subInset = (y + h) - bar.Y + 14 + upNextH
	}
	renderVideoSubtitles(renderer, config, x, y, w, h, state.progress*state.duration, subInset)

	if videoSettingsOpen {
		vcDrawSettingsSheet(renderer, config, g, x, y)
	} else {
		videoSettingsSheetDrawn = sdl.Rect{}
	}

	if paused {
		vcDrawBigPlay(renderer, vcOffset(g.bigPlay, x, y))
	}
	vcDrawSeekFlash(renderer, vcFont(config, 14), g, x, y)
}

// vcDrawBigPlay is the paused picture's play button: a large glass disc with
// the accent triangle, the whole picture's call to action.
func vcDrawBigPlay(renderer *sdl.Renderer, r sdl.Rect) {
	if r.W <= 0 {
		return
	}
	acc := vcAccent()
	cx, cy, rad := r.X+r.W/2, r.Y+r.H/2, r.W/2
	homeGlowBlob(renderer, sdl.Rect{X: cx - rad*2, Y: cy - rad*2, W: rad * 4, H: rad * 4}, acc, 70)
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, rad, sdl.Color{R: 8, G: 14, B: 22, A: 170})
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, rad, 2, WithAlpha(acc, 190))
	g := r.W * 2 / 5
	drawHomeGlyphTinted(renderer, "play", sdl.Rect{X: cx - g/2 + r.W/30, Y: cy - g/2, W: g, H: g}, acc)
}

// ── Seek flash ───────────────────────────────────────────────────────────

// vcSeekFlash is the last relative seek, shown on the picture as "−10 s" or
// "+10 s" on the side it went, the way a phone player answers a double tap.
var vcSeekFlash struct {
	dir     int
	seconds int
	at      uint64
}

// vcNoteSeek records a relative seek for the flash.
func vcNoteSeek(seconds float64) {
	vcSeekFlash.dir = 1
	if seconds < 0 {
		vcSeekFlash.dir = -1
	}
	vcSeekFlash.seconds = int(math.Abs(seconds) + 0.5)
	vcSeekFlash.at = sdl.GetTicks64()
}

// vcDrawSeekFlash draws the flash while it lasts, fading out.
func vcDrawSeekFlash(renderer *sdl.Renderer, font *ttf.Font, g vcGeom, ox, oy int32) {
	if vcSeekFlash.at == 0 {
		return
	}
	age := sdl.GetTicks64() - vcSeekFlash.at
	if age >= vcFlashMS {
		vcSeekFlash.at = 0
		return
	}
	// The fade steps in eighths: the label is a cached texture per colour, and
	// a fresh alpha every frame would rasterise a new one each time.
	fade := math.Round((1-uiEaseOutCubic(float64(age)/float64(vcFlashMS)))*8) / 8
	if fade <= 0 {
		return
	}
	cx := ox + g.w*18/100
	icon, sign := "rewind", "−"
	if vcSeekFlash.dir > 0 {
		cx = ox + g.w*82/100
		icon, sign = "fast-forward", "+"
	}
	cy := oy + g.flashY
	const r = int32(46)
	fillCircle(renderer, cx, cy, r, sdl.Color{R: 0, G: 0, B: 0, A: uint8(120 * fade)})
	ink := vcInk
	ink.A = uint8(255 * fade)
	drawHomeGlyphTinted(renderer, icon, sdl.Rect{X: cx - 14, Y: cy - 24, W: 28, H: 28}, ink)
	if font != nil {
		label := sign + strconv.Itoa(vcSeekFlash.seconds) + " s"
		drawText(renderer, font, label, cx, cy+8, ink, textAlignCenter)
	}
}

// ── Settings sheet ───────────────────────────────────────────────────────

// videoSettingsSheetDrawn is the sheet as last drawn, for the tap handler: the
// windowed player draws it inside its own rect, which the old hit test (laid
// out for the whole screen) missed.
var videoSettingsSheetDrawn sdl.Rect

// videoSettingsSheetRowsDrawn is how many rows the sheet last drew: its two
// pages have different lengths.
var videoSettingsSheetRowsDrawn int

// videoSettingsSheetRowH is the height the sheet last drew its rows at.
var videoSettingsSheetRowH = videoSettingsRowH

// vcSheetRect places the settings sheet above the bar's right end and returns
// it with the height of its rows. It is wide enough for the longest Quality
// row: a pinned ceiling and the resolution the stream actually got, side by
// side. A small window narrows the rows (down to videoSettingsRowMinH) before
// the sheet would cover the bar.
func vcSheetRect(g vcGeom, ox, oy int32, rows int) (sdl.Rect, int32) {
	w := min32(480, g.w-32)
	n := int32(rows)
	if n < 1 {
		n = 1
	}
	room := g.bar.Y - 24
	if g.bar.W <= 0 {
		room = g.h - 24
	}
	rowH := videoSettingsRowH
	for rowH > videoSettingsRowMinH && n*rowH+videoSettingsTopPad+40 > room {
		rowH--
	}
	h := n*rowH + videoSettingsTopPad + 40
	x := ox + g.bar.X + g.bar.W - w
	if x < ox+16 {
		x = ox + 16
	}
	y := oy + g.bar.Y - 12 - h
	if y < oy+12 {
		y = oy + 12
	}
	return sdl.Rect{X: x, Y: y, W: w, H: h}, rowH
}

// vcDrawSettingsSheet is the settings sheet: a glass card of rows, each a name
// and its value, the selected row tinted, a status line underneath (the
// subtitle source, or where saved files go). The rows are the page's
// (videosheet.go).
func vcDrawSettingsSheet(renderer *sdl.Renderer, config *Config, g vcGeom, ox, oy int32) {
	ctx := videoSheetContextNow()
	ids := videoSheetRowIDsFor(videoSettingsPage, ctx)
	if videoSettingsItem >= len(ids) {
		videoSettingsItem = len(ids) - 1
	}
	if videoSettingsItem < 0 {
		videoSettingsItem = 0
	}
	sheet, rowH := vcSheetRect(g, ox, oy, len(ids))
	videoSettingsSheetDrawn = sheet
	videoSettingsSheetRowsDrawn = len(ids)
	videoSettingsSheetRowH = rowH
	acc := vcAccent()
	fillRoundedRect(renderer, sheet.X+2, sheet.Y+6, sheet.W, sheet.H, 16, sdl.Color{R: 0, G: 0, B: 0, A: 90})
	fillRoundedRect(renderer, sheet.X, sheet.Y, sheet.W, sheet.H, 16, sdl.Color{R: 14, G: 20, B: 28, A: 238})
	strokeRoundedRect(renderer, sheet.X, sheet.Y, sheet.W, sheet.H, 16, 1, sdl.Color{R: 255, G: 255, B: 255, A: 34})
	font := vcFont(config, 16)
	if font == nil {
		return
	}
	for i, id := range ids {
		text := videoSheetRowLabel(id, ctx)
		rowY := sheet.Y + videoSettingsTopPad + int32(i)*rowH
		row := sdl.Rect{X: sheet.X + 10, Y: rowY, W: sheet.W - 20, H: rowH - 6}
		selected := i == videoSettingsItem
		if selected {
			fillRoundedRect(renderer, row.X, row.Y, row.W, row.H, 10, WithAlpha(acc, 46))
			strokeRoundedRect(renderer, row.X, row.Y, row.W, row.H, 10, 1, WithAlpha(acc, 120))
		} else if vcHit(mouseX, mouseY, row) {
			fillRoundedRect(renderer, row.X, row.Y, row.W, row.H, 10, sdl.Color{R: 255, G: 255, B: 255, A: 20})
		}
		name, value := text, ""
		if i := strings.Index(text, ": "); i > 0 {
			name, value = text[:i], text[i+2:]
		}
		// The name keeps its width (up to half the row); the value takes
		// the rest, right-aligned.
		name = ellipsize(font, name, (row.W-28)/2)
		nameW, th := measureText(font, name)
		ty := row.Y + (row.H-th)/2
		nameInk := vcInk
		if selected {
			nameInk = vcHandle
		}
		if value != "" {
			value = ellipsize(font, value, row.W-28-nameW-16)
			valueInk := vcInk3
			if selected {
				valueInk = acc
			}
			drawText(renderer, font, value, row.X+row.W-14, ty, valueInk, textAlignRight)
		}
		drawText(renderer, font, name, row.X+14, ty, nameInk, textAlignLeft)
	}
	if small := vcFont(config, 13); small != nil {
		line := subtitleStatusLine()
		if videoSettingsPage == videoSheetSave {
			line = "Saved videos and tracks are in Media → Downloads"
		}
		status := ellipsize(small, line, sheet.W-28)
		statusY := sheet.Y + videoSettingsTopPad + int32(len(ids))*rowH + 8
		drawText(renderer, small, status, sheet.X+14, statusY, vcInk3, textAlignLeft)
	}
}

// vcSheetMargin is vcSheetRow's answer for a tap on the sheet that is not on a
// row: the band above the rows or the status line under them.
const vcSheetMargin = -2

// vcSheetRow is the sheet row under a tap; vcSheetMargin on the sheet but off
// its rows; -1 beside the sheet — and -1 when the sheet is not on screen
// (opened from the keyboard while the stream was still opening), so a tap can
// never press a row nobody can see. The margins press nothing: on the Save
// offline page the first and last rows start a download or an extraction, and
// a tap that grazed the sheet's edge used to press them.
func vcSheetRow(mx, my int32) int {
	sheet := videoSettingsSheetDrawn
	rows := videoSettingsSheetRowsDrawn
	rowH := videoSettingsSheetRowH
	if sheet.W <= 0 || rows <= 0 || rowH <= 0 || !pointInRect(mx, my, sheet) {
		return -1
	}
	top := sheet.Y + videoSettingsTopPad
	if my < top || my >= top+int32(rows)*rowH {
		return vcSheetMargin
	}
	return int((my - top) / rowH)
}

// ── Loading ──────────────────────────────────────────────────────────────

// vcDrawLoading is the player while its stream is being opened: the picture's
// rect in near-black with a spinner and what is happening, inside the player —
// not a black sheet over the whole screen, which the windowed player used to
// throw over the Media screen around it.
func vcDrawLoading(renderer *sdl.Renderer, config *Config, state videoStateSnapshot, x, y, w, h int32) {
	if renderer == nil || w <= 0 || h <= 0 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(6, 9, 14, 235)
	_ = renderer.FillRect(&sdl.Rect{X: x, Y: y, W: w, H: h})
	cx, cy := x+w/2, y+h/2-16
	vcSpinner(renderer, cx, cy, 22)
	font := vcFont(config, 16)
	if font == nil {
		return
	}
	line := "Loading video…"
	if p := int(state.progress * 100); p > 0 && p < 100 {
		line = "Loading video… " + strconv.Itoa(p) + "%"
	}
	drawText(renderer, font, line, cx, cy+40, vcInk, textAlignCenter)
	if spd := formatDownloadSpeed(state.downloadSpeed); spd != "" {
		if small := vcFont(config, 13); small != nil {
			drawText(renderer, small, spd, cx, cy+66, vcInk3, textAlignCenter)
		}
	}
}

// vcSpinner is a ring of twelve dots fading round behind a bright head.
func vcSpinner(renderer *sdl.Renderer, cx, cy, radius int32) {
	acc := vcAccent()
	const dots = 12
	head := int(sdl.GetTicks64()/80) % dots
	for i := 0; i < dots; i++ {
		age := (head - i + dots) % dots
		a := 255 - age*20
		if a < 40 {
			a = 40
		}
		angle := float64(i) * 2 * math.Pi / dots
		px := cx + int32(math.Round(float64(radius)*math.Cos(angle)))
		py := cy + int32(math.Round(float64(radius)*math.Sin(angle)))
		fillCircle(renderer, px, py, 3, WithAlpha(acc, uint8(a)))
	}
}

// ── Volume ───────────────────────────────────────────────────────────────

// vcMutedVolume is the level a mute put aside, restored by unmuting.
var vcMutedVolume float64

// videoSetVolume sets the player's volume, 0..1.
func videoSetVolume(v float64) {
	if math.IsNaN(v) {
		return
	}
	embeddedPlayer.mu.Lock()
	embeddedPlayer.volume = uiClamp01(v)
	embeddedPlayer.mu.Unlock()
}

// videoToggleMute silences the player, or brings back the level it had.
func videoToggleMute() {
	volume := snapshotVideoState().volume
	if volume > 0.001 {
		vcMutedVolume = volume
		videoSetVolume(0)
		showToast("Muted", ToastInfo())
		return
	}
	restore := vcMutedVolume
	if restore <= 0.001 {
		restore = 0.5
	}
	videoSetVolume(restore)
	showToast("Volume "+formatPercentSimple(restore), ToastInfo())
}

// videoVolumeAt sets the volume from a tap on the level bar, in 5% steps.
func videoVolumeAt(mx int32, bar sdl.Rect) {
	if bar.W <= 0 {
		return
	}
	v := float64(mx-bar.X) / float64(bar.W)
	videoSetVolume(math.Round(uiClamp01(v)*20) / 20)
}
