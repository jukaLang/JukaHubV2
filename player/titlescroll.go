package main

// titlescroll.go — long titles scroll on the focused item.
//
// A list row, a cover or a Home card fits its title with an ellipsis, which
// makes "The Legend of Zelda: Oracle of Seasons" and "… Oracle of Ages" the
// same row. Now the focused item's title, when it does not fit, waits a second
// on the ellipsized form (so stepping through a list does not set every row in
// motion), then slides left at 30 px/s inside a clip rect until its end shows,
// holds there, snaps back and goes round again. Every other item keeps its
// ellipsis, and a title that fits is drawn as it always was.
//
// Only one title scrolls at a time: the state is one key (the text) and the
// moment it took the focus. A key the screen stops drawing for longer than
// titleScrollForgetMS starts over when it comes back, so leaving a row and
// returning to it begins with the pause again.
//
// Frames: the main loop draws at the idle cadence unless something asks for
// more (adaptive.go). A moving title asks for a frame every titleScrollFrameMS
// (about a pixel per frame — smooth at 30 fps, never the 60 fps burst), a title
// holding still asks only for titleScrollHoldFrameMS so the next move starts
// on time; nothing is asked while no long title has the focus. Reduce motion
// (and Battery Saver, which implies it — effectiveReducedMotion) keeps the
// ellipsis and asks for nothing.
//
// Nothing here allocates per frame: the full title is one cached text texture
// (renderText / drawText), measured through the shared measure cache, and the
// clip is pushed and popped inline.

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// titleScrollDelayMS is the pause on the ellipsized title before it moves.
	titleScrollDelayMS = 1000
	// titleScrollSpeed is the scrolling speed in pixels per second.
	titleScrollSpeed = 30
	// titleScrollHoldMS is how long the end of the title stays up before it
	// snaps back to the start.
	titleScrollHoldMS = 1500
	// titleScrollFrameMS is the frame interval asked for while the title
	// moves; titleScrollHoldFrameMS while it holds still between moves.
	titleScrollFrameMS     = 33
	titleScrollHoldFrameMS = 250
	// titleScrollForgetMS is how long a title may go undrawn before its
	// scrolling starts over from the pause.
	titleScrollForgetMS = 600
)

// titleScroll is the one title that scrolls: its text, when it took the focus
// and when it was last drawn (sdl.GetTicks64 milliseconds, which a clock change
// does not move). Main thread only.
var titleScroll struct {
	key   string
	start uint64
	seen  uint64
	// last is the focused title drawn last in frame lastFrame (at lastAt),
	// above the one drawn last in the frame before. When two lists draw a
	// focused title in one frame (a panel over Home's carousel) only the one
	// on top, drawn last, may scroll; the other keeps its ellipsis instead
	// of the two taking the state from each other every frame.
	last      string
	lastFrame uint64
	lastAt    uint64
	above     string
}

// titleScrollEnabled reports whether titles may move: not under Reduce motion
// or Battery Saver.
func titleScrollEnabled(config *Config) bool {
	if config == nil {
		config = appConfig
	}
	return !effectiveReducedMotion(config)
}

// titleScrollPhase is where a title overflowing its box by overflow pixels
// stands elapsed milliseconds after it took the focus: the offset to draw it
// at (0 is the ellipsized start) and whether it is moving. One cycle is the
// pause, the move, the hold at the end; then it repeats. Pure.
func titleScrollPhase(elapsed uint64, overflow int32) (offset int32, moving bool) {
	if overflow <= 0 {
		return 0, false
	}
	move := uint64(overflow) * 1000 / titleScrollSpeed
	cycle := titleScrollDelayMS + move + titleScrollHoldMS
	t := elapsed % cycle
	switch {
	case t < titleScrollDelayMS:
		return 0, false
	case t < titleScrollDelayMS+move:
		return int32((t - titleScrollDelayMS) * titleScrollSpeed / 1000), true
	}
	return overflow, false
}

// titleScrollStep notes that key is drawn focused now and returns its offset,
// asking the frame-rate governor for the frames the next step needs. A title
// that fits (overflow <= 0) still claims the top, and gets 0.
func titleScrollStep(key string, overflow int32) int32 {
	now := sdl.GetTicks64()
	if titleScroll.lastFrame != frameCount {
		titleScroll.above = ""
		if now-titleScroll.lastAt <= titleScrollForgetMS {
			titleScroll.above = titleScroll.last
		}
		titleScroll.lastFrame = frameCount
	}
	titleScroll.last, titleScroll.lastAt = key, now
	if overflow <= 0 || (titleScroll.above != "" && key != titleScroll.above) {
		// Fits, or another focused title is drawn over this one (or the
		// focus just moved here: it moves from the next frame on).
		return 0
	}
	if key != titleScroll.key || now-titleScroll.seen > titleScrollForgetMS {
		titleScroll.key, titleScroll.start = key, now
	}
	titleScroll.seen = now
	offset, moving := titleScrollPhase(now-titleScroll.start, overflow)
	if moving {
		requestFrameEvery(titleScrollFrameMS)
	} else {
		requestFrameEvery(titleScrollHoldFrameMS)
	}
	return offset
}

// titleScrollOverflow is how many pixels text overflows maxW by, measured the
// way it is drawn (characters the font lacks are substituted first, fonts.go).
func titleScrollOverflow(font *ttf.Font, text string, maxW int32) (overflow, height int32) {
	w, h := measureText(font, sanitizeCached(font, text))
	if fh := int32(font.Height()); fh > h {
		h = fh
	}
	return w - maxW, h
}

// titleScrollClip narrows the renderer's clip to box (inside any clip already
// set) and returns what to restore. ok is false when nothing of box is visible.
func titleScrollClip(renderer *sdl.Renderer, box sdl.Rect) (prev sdl.Rect, hadClip, ok bool) {
	hadClip = renderer.IsClipEnabled()
	if hadClip {
		prev = renderer.GetClipRect()
		inter, visible := box.Intersect(&prev)
		if !visible {
			return prev, hadClip, false
		}
		box = inter
	}
	renderer.SetClipRect(&box)
	return prev, hadClip, true
}

// titleScrollUnclip restores the clip titleScrollClip replaced.
func titleScrollUnclip(renderer *sdl.Renderer, prev sdl.Rect, hadClip bool) {
	if hadClip {
		renderer.SetClipRect(&prev)
		return
	}
	renderer.SetClipRect(nil)
}

// titleScrollText draws a title left-aligned at (x, y) in at most maxW pixels,
// for callers of renderText: ellipsized as before unless focused, and then
// scrolling when it does not fit.
func titleScrollText(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, color sdl.Color, x, y, maxW int32, focused bool) {
	if renderer == nil || font == nil || text == "" {
		return
	}
	if !focused || maxW <= 0 || !titleScrollEnabled(config) {
		renderText(renderer, config, font, ellipsize(font, text, maxW), color, x, y)
		return
	}
	overflow, h := titleScrollOverflow(font, text, maxW)
	offset := titleScrollStep(text, overflow)
	if overflow <= 0 {
		renderText(renderer, config, font, text, color, x, y)
		return
	}
	if offset <= 0 {
		renderText(renderer, config, font, ellipsize(font, text, maxW), color, x, y)
		return
	}
	prev, hadClip, ok := titleScrollClip(renderer, sdl.Rect{X: x, Y: y, W: maxW, H: h})
	if !ok {
		return
	}
	renderText(renderer, config, font, text, color, x-offset, y)
	titleScrollUnclip(renderer, prev, hadClip)
}

// titleScrollDrawText is titleScrollText for callers of drawText (the Home
// cards and carousels), with drawText's alignment: x is the left edge, the
// centre or the right edge of the maxW-wide box. A scrolling title is drawn
// left-aligned inside that box.
func titleScrollDrawText(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, x, y, maxW int32, color sdl.Color, align textAlignment, focused bool) {
	if renderer == nil || font == nil || text == "" {
		return
	}
	if !focused || maxW <= 0 || !titleScrollEnabled(config) {
		drawText(renderer, font, ellipsize(font, text, maxW), x, y, color, align)
		return
	}
	overflow, h := titleScrollOverflow(font, text, maxW)
	offset := titleScrollStep(text, overflow)
	if overflow <= 0 {
		drawText(renderer, font, text, x, y, color, align)
		return
	}
	if offset <= 0 {
		drawText(renderer, font, ellipsize(font, text, maxW), x, y, color, align)
		return
	}
	left := x
	switch align {
	case textAlignCenter:
		left = x - maxW/2
	case textAlignRight:
		left = x - maxW
	}
	prev, hadClip, ok := titleScrollClip(renderer, sdl.Rect{X: left, Y: y, W: maxW, H: h})
	if !ok {
		return
	}
	drawText(renderer, font, text, left-offset, y, color, textAlignLeft)
	titleScrollUnclip(renderer, prev, hadClip)
}
