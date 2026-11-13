package main

// Home's focus glide: when the focus steps from one control to the next, the
// highlight (the accent ring and its soft glow) travels there instead of
// jumping, runs a little past the new control in the direction it was moving
// and swings back onto it — the bounce Juka asked for on 2026-11-12.
//
// Only the highlight moves. The card's own lighter fill and its lift change on
// the frame the focus does, so the eye sees where focus landed at once and the
// ring arrives a beat later.
//
// The glide is scoped to HomeLayout.Render: beginFrame/endFrame bracket it, and
// a ring drawn outside that window (an overlay over Home, any other screen)
// draws where it is asked to. Inside it, the first highlight drawn after the
// focus changed starts the glide; the glow and the ring of the same control
// (same centre) share that frame's offset, and anything else draws in place.

import (
	"math"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// homeGlideMS is the whole move: travel, overshoot and settle.
	homeGlideMS = 300
	// homeGlideBack is easeOutBack's constant; 1.70158 crosses the target by
	// 10% of the distance at about 60% of the time.
	homeGlideBack = 1.70158
	// homeGlideMaxOver caps that crossing (at the 1280-wide canvas, scaled): a
	// long jump across Home would otherwise overshoot by a whole gutter.
	homeGlideMaxOver = 14
)

type homeFocusGlideState struct {
	open    bool // inside HomeLayout.Render
	enabled bool // motion allowed this frame
	frameMS uint64

	focusID HomeFocusID
	moved   bool // the focus changed this frame; the first claim starts a glide

	have bool // to holds the last frame's highlight
	to   homeGlideRect

	active bool
	from   homeGlideRect
	start  uint64

	// This frame's first claim and the offset it settled on.
	claimed bool
	claimCX float64
	claimCY float64
	dx, dy  float64
	dw, dh  float64
}

// homeGlideRect is a rect as centre and size, the space the glide moves in.
type homeGlideRect struct{ cx, cy, w, h float64 }

func homeGlideRectOf(r sdl.Rect) homeGlideRect {
	return homeGlideRect{
		cx: float64(r.X) + float64(r.W)/2, cy: float64(r.Y) + float64(r.H)/2,
		w: float64(r.W), h: float64(r.H),
	}
}

var homeFocusGlide homeFocusGlideState

// homeGlideEaseOutBack runs from 0 to 1, crossing 1 on the way.
func homeGlideEaseOutBack(t float64) float64 {
	t = uiClamp01(t)
	c1 := homeGlideBack
	c3 := c1 + 1
	u := t - 1
	return 1 + c3*u*u*u + c1*u*u
}

// beginFrame opens Home's glide window for one Render. enabled false (reduced
// motion, focus motion "none") snaps: the next highlight draws where it is.
func (g *homeFocusGlideState) beginFrame(now uint64, focused HomeFocusID, enabled bool) {
	// A Home that was not drawn for a while (another screen, a game) comes back
	// with the highlight already in place rather than flying in from where it
	// was left.
	if g.have && now > g.frameMS+250 {
		g.have, g.active = false, false
	}
	if !enabled {
		g.have, g.active = false, false
	}
	g.open, g.enabled, g.frameMS = true, enabled, now
	g.claimed = false
	g.moved = focused != g.focusID
	g.focusID = focused
}

// endFrame closes the window: rings drawn after it (overlays) are left alone.
func (g *homeFocusGlideState) endFrame() {
	g.open = false
	if g.active && g.frameMS >= g.start+homeGlideMS {
		g.active = false
	}
}

// animating reports whether the highlight is still travelling, so the frame
// rate keeps its interactive cadence until it lands.
func (g *homeFocusGlideState) animating() bool {
	return g.active && uiClockMS() < g.start+homeGlideMS+20
}

// at is where the glide stands at now, given the target (the current frame's
// highlight), in centre/size space.
func (g *homeFocusGlideState) at(now uint64, target homeGlideRect) homeGlideRect {
	if !g.active {
		return target
	}
	t := float64(now-g.start) / float64(homeGlideMS)
	if now < g.start {
		t = 0
	}
	if t >= 1 {
		return target
	}
	e := homeGlideEaseOutBack(t)
	maxOver := float64(max32(4, scaleX(homeGlideMaxOver, screenWidth)))
	axis := func(from, to float64) float64 {
		v := from + (to-from)*e
		// Past the target: keep the crossing small whatever the distance.
		if over := v - to; (to-from)*over > 0 && math.Abs(over) > maxOver {
			v = to + math.Copysign(maxOver, over)
		}
		return v
	}
	// The size eases without overshoot: a ring that balloons past a tile's
	// size reads as a glitch, a ring that slides past it reads as momentum.
	s := uiEaseOutCubic(t)
	return homeGlideRect{
		cx: axis(g.from.cx, target.cx),
		cy: axis(g.from.cy, target.cy),
		w:  g.from.w + (target.w-g.from.w)*s,
		h:  g.from.h + (target.h-g.from.h)*s,
	}
}

// apply returns where to draw a focus highlight Home asked for at rect. The
// result is rect itself when nothing is gliding.
func (g *homeFocusGlideState) apply(rect sdl.Rect) sdl.Rect {
	if !g.open || !g.enabled || rect.W <= 0 || rect.H <= 0 {
		return rect
	}
	r := homeGlideRectOf(rect)
	if g.claimed {
		// The same control's other layer (glow, then ring): same centre, same
		// offset. A different centre is some other highlight; leave it be.
		if math.Abs(r.cx-g.claimCX) > 3 || math.Abs(r.cy-g.claimCY) > 3 {
			return rect
		}
		return g.offset(rect)
	}
	g.claimed, g.claimCX, g.claimCY = true, r.cx, r.cy
	now := g.frameMS
	if g.moved && g.have {
		// Start from wherever the highlight is now — mid-flight included — so a
		// quick second step carries on smoothly instead of restarting.
		g.from = g.at(now, g.to)
		g.start, g.active = now, true
	}
	g.moved = false
	g.to, g.have = r, true
	cur := g.at(now, r)
	g.dx, g.dy = cur.cx-r.cx, cur.cy-r.cy
	g.dw, g.dh = cur.w-r.w, cur.h-r.h
	return g.offset(rect)
}

func (g *homeFocusGlideState) offset(rect sdl.Rect) sdl.Rect {
	if g.dx == 0 && g.dy == 0 && g.dw == 0 && g.dh == 0 {
		return rect
	}
	w := math.Max(1, float64(rect.W)+g.dw)
	h := math.Max(1, float64(rect.H)+g.dh)
	cx := float64(rect.X) + float64(rect.W)/2 + g.dx
	cy := float64(rect.Y) + float64(rect.H)/2 + g.dy
	return sdl.Rect{
		X: int32(math.Round(cx - w/2)), Y: int32(math.Round(cy - h/2)),
		W: int32(math.Round(w)), H: int32(math.Round(h)),
	}
}
