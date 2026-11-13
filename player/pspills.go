package main

// pspills.go — the PS4 / PS5 Remote Play launchers in the home header.
//
// The design sheet puts them in one segmented pill group: a dot per console and
// its name, the dot lit when that console is paired. It replaces the pair of
// square PlayStation-blue tiles, which behaved like two more cards in a header
// that is meant to read as controls. Both pills stay in the D-pad focus graph
// (HomeFocusPS4 / HomeFocusPS5) and keep their one-press behaviour: A plays the
// console, or opens the pairing flow when nothing is paired yet.
//
// The group opens with its purpose, "Remote Play", in the capsule's muted ink:
// "● PS4  ● PS5" on its own read as two status lights, and nothing on screen
// said that pressing one streams the console to the handheld. The label is
// part of the capsule, not a control; it is dropped first when the band is too
// narrow for it. The group is not drawn at all where Remote Play cannot run
// (psRemoteSupported): on the desktop each pill only answered with a warning.

import (
	"github.com/veandco/go-sdl2/sdl"
)

// psPillGroupLabel names what the pills do.
const psPillGroupLabel = "Remote Play"

// psPillVisual is this frame's state for the whole group.
type psPillVisual struct {
	PS4On   bool
	PS5On   bool
	Focused HomeFocusID
	Pressed HomeFocusID
	// Label is the capsule's leading caption; empty draws the pills alone.
	Label string
}

// psPillRects lays the group out so its right edge is at `right` and its top at
// y, with room for label at its left end when one is given. A zero group rect
// means there is no font to measure with.
func psPillRects(fonts HomeFonts, right, y, height int32, label string) (group, ps4, ps5 sdl.Rect) {
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
	captionW := int32(0)
	if label != "" {
		lw, _ := measureText(fonts.Small, label)
		if lw > 0 {
			// The caption's own inset from the capsule's rounded end, and the
			// gap before the first pill.
			captionW = labelPad + lw + labelPad/2
		}
	}
	groupW := groupPad*2 + captionW + pill4W + pillGap + pill5W
	group = sdl.Rect{X: right - groupW, Y: y, W: groupW, H: height}
	ps4 = sdl.Rect{X: group.X + groupPad + captionW, Y: y + groupPad, W: pill4W, H: inner}
	ps5 = sdl.Rect{X: ps4.X + pill4W + pillGap, Y: y + groupPad, W: pill5W, H: inner}
	return group, ps4, ps5
}

// renderPSPillGroup draws the group and records both pill rects for focus and
// clicks. The container is the design's translucent capsule; a paired console
// lights its own pill, and the focused pill carries the home focus ring.
func renderPSPillGroup(renderer *sdl.Renderer, fonts HomeFonts, group, ps4, ps5 sdl.Rect, v psPillVisual) {
	if renderer == nil || fonts.Small == nil || group.W <= 0 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, group.X, group.Y, group.W, group.H, group.H/2, homeGlass(13))
	strokeRoundedRect(renderer, group.X, group.Y, group.W, group.H, group.H/2, 1, homeGlass(20))

	// The caption fills the capsule left of the first pill, centred in it.
	if v.Label != "" && ps4.X > group.X {
		lw, _ := measureText(fonts.Small, v.Label)
		slot := ps4.X - group.X
		if lw > 0 && lw <= slot {
			drawText(renderer, fonts.Small, v.Label, group.X+(slot-lw)/2,
				group.Y+(group.H-int32(fonts.Small.Height()))/2, ColorTextMuted(), textAlignLeft)
		}
	}

	pill := func(rect sdl.Rect, label string, on bool, id HomeFocusID) {
		if rect.W <= 0 {
			return
		}
		if v.Focused == id {
			// The header's focus ring: 3px of accent on the pill, over a soft
			// bloom, the same weight the header chips and the bell wear.
			drawHomeFocusGlow(renderer, rect)
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, homeGlass(26))
			strokeRoundedRectAt(renderer, rect, homeFocusGlide.apply(rect), rect.H/2, homeFocusRingWidth, ColorAccent)
		}
		if on {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, WithAlpha(ColorAccent, 38))
			if v.Focused != id {
				strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, 1, WithAlpha(ColorAccent, 115))
			}
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
		if on || v.Focused == id {
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
