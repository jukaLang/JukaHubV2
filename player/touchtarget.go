package main

// touchtarget.go — one place that grows a drawn control's hit area to a finger.
//
// The drawn size and the tap size are different things. A compact 36px-tall
// Exit pill or a 32px toolbar chip looks right on a 1280-wide panel, but a
// fingertip is roughly 8–10 mm and a press that lands a few pixels above the
// pill does nothing — which reads as "the button is broken". The fix is not to
// make the control bigger (that fights the layout) but to make its *hit area*
// bigger, centred on what is drawn, so the miss is absorbed.
//
// The browser already grew its toolbar chips this way (textBrowserTouchTarget).
// This generalises it so the home header chips, the Exit pill and any future
// compact control share one definition of "big enough to tap".

import "github.com/veandco/go-sdl2/sdl"

// minTouchTarget is the smallest hit area a control gets, in pixels. 44 matches
// the platform HIG most handheld frontends land on and is the figure the browser
// chip audit already used.
const minTouchTarget int32 = 44

// growTouchTarget expands r so both sides are at least minTouchTarget, keeping
// its centre. The drawn geometry never moves — only the invisible tap area
// grows — so this is safe to apply on the hit-test path. A rect already big
// enough is returned unchanged, and a degenerate (zero) rect is left alone so a
// hidden control cannot grow into a phantom target.
func growTouchTarget(r sdl.Rect) sdl.Rect {
	if r.W <= 0 || r.H <= 0 {
		return r
	}
	if r.W < minTouchTarget {
		pad := (minTouchTarget - r.W) / 2
		r.X -= pad
		r.W = minTouchTarget
	}
	if r.H < minTouchTarget {
		pad := (minTouchTarget - r.H) / 2
		r.Y -= pad
		r.H = minTouchTarget
	}
	return r
}

// hitTouchTarget reports whether a tap at (x,y) lands inside the grown hit area
// of r. It is the read-only companion to growTouchTarget for callers that keep
// the drawn rect and only want the answer.
func hitTouchTarget(r sdl.Rect, x, y int32) bool {
	g := growTouchTarget(r)
	return x >= g.X && x < g.X+g.W && y >= g.Y && y < g.Y+g.H
}
