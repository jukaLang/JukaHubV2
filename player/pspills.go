package main

// pspills.go — the PS4 / PS5 Remote Play launchers in the home header.
//
// The design sheet puts them in one segmented pill group: a dot per console and
// its name, the dot lit when that console is paired. It replaces the pair of
// square PlayStation-blue tiles, which behaved like two more cards in a header
// that is meant to read as controls. Both pills stay in the D-pad focus graph
// (HomeFocusPS4 / HomeFocusPS5) and keep their one-press behaviour: A plays the
// console, or opens the pairing flow when nothing is paired yet.

import (
	"github.com/veandco/go-sdl2/sdl"
)

// psPillVisual is this frame's state for the whole group.
type psPillVisual struct {
	PS4On   bool
	PS5On   bool
	Focused HomeFocusID
	Pressed HomeFocusID
}

// psPillRects lays the group out so its right edge is at `right` and its top at
// y. A zero group rect means there is no font to measure with.
func psPillRects(fonts HomeFonts, right, y, height int32) (group, ps4, ps5 sdl.Rect) {
	if fonts.Small == nil || right <= 0 || height <= 0 {
		return sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	}
	const groupPad = int32(4)
	const pillGap = int32(4)
	inner := height - groupPad*2
	if inner < 20 {
		return sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	}
	dot := int32(8)
	dotGap := int32(8)
	labelPad := int32(14)
	w4, _ := measureText(fonts.Small, "PS4")
	w5, _ := measureText(fonts.Small, "PS5")
	pill4W := labelPad*2 + dot + dotGap + w4
	pill5W := labelPad*2 + dot + dotGap + w5
	groupW := groupPad*2 + pill4W + pillGap + pill5W
	group = sdl.Rect{X: right - groupW, Y: y, W: groupW, H: height}
	ps4 = sdl.Rect{X: group.X + groupPad, Y: y + groupPad, W: pill4W, H: inner}
	ps5 = sdl.Rect{X: ps4.X + pill4W + pillGap, Y: y + groupPad, W: pill5W, H: inner}
	return group, ps4, ps5
}

// renderPSPillGroup draws the group and records both pill rects for focus and
// clicks. The container is the design's translucent capsule; a paired console
// lights its own pill, and the focused pill carries the standard cyan ring.
func renderPSPillGroup(renderer *sdl.Renderer, fonts HomeFonts, group, ps4, ps5 sdl.Rect, v psPillVisual) {
	if renderer == nil || fonts.Small == nil || group.W <= 0 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, group.X, group.Y, group.W, group.H, group.H/2, homeGlass(13))
	strokeRoundedRect(renderer, group.X, group.Y, group.W, group.H, group.H/2, 1, homeGlass(20))

	pill := func(rect sdl.Rect, label string, on bool, id HomeFocusID) {
		if rect.W <= 0 {
			return
		}
		if v.Focused == id {
			drawHomeFocusGlow(renderer, rect)
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, 2, ColorAccent)
		}
		if on {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, WithAlpha(ColorAccent, 38))
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, 1, WithAlpha(ColorAccent, 115))
		}
		dotColor := homeGlass(90)
		if on {
			dotColor = sdl.Color{R: 52, G: 211, B: 153, A: 255}
			// A lit console gets the design's green dot glow.
			homeGlowBlob(renderer, sdl.Rect{
				X: rect.X + rect.H/2 - rect.H, Y: rect.Y,
				W: rect.H * 2, H: rect.H,
			}, dotColor, 90)
		}
		dotR := max32(3, rect.H/9)
		dotCX := rect.X + int32(14) + dotR
		fillCircle(renderer, dotCX, rect.Y+rect.H/2, dotR, dotColor)

		text := ColorTextMuted()
		if on {
			text = ColorTextPrimary()
		}
		if v.Pressed == id {
			text = ColorAccent
		}
		drawText(renderer, fonts.Small, label, dotCX+dotR+int32(8),
			rect.Y+(rect.H-int32(fonts.Small.Height()))/2, text, textAlignLeft)
	}
	pill(ps4, "PS4", v.PS4On, HomeFocusPS4)
	pill(ps5, "PS5", v.PS5On, HomeFocusPS5)
}
