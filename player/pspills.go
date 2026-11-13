package main

// pspills.go — the PS4 / PS5 / X1 Remote Play launchers in the home header.
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
// narrow for it. The PS pills are not drawn where PlayStation Remote Play
// cannot run (psRemoteSupported): on the desktop each only answered with a
// warning. X1 (Xbox Remote Play, xremote.go) closes the group on the right and
// is drawn wherever Home is, since a PC has the browser to fall back on.

import (
	"github.com/veandco/go-sdl2/sdl"
)

// psPillGroupLabel names what the pills do.
const psPillGroupLabel = "Remote Play"

// psPillVisual is this frame's state for the whole group.
type psPillVisual struct {
	PS4On   bool
	PS5On   bool
	X1On    bool
	Focused HomeFocusID
	Pressed HomeFocusID
	// Label is the capsule's leading caption; empty draws the pills alone.
	Label string
}

// psPillRects lays the group out so its right edge is at `right` and its top at
// y, with room for label at its left end when one is given. withPS draws the
// PS4 and PS5 pills (else they come back empty and the group is X1 alone),
// withX1 the X1 pill after them. A zero group rect means there is no font to
// measure with, or no pill to draw.
func psPillRects(fonts HomeFonts, right, y, height int32, label string, withPS, withX1 bool) (group, ps4, ps5, x1 sdl.Rect) {
	if fonts.Small == nil || right <= 0 || height <= 0 || (!withPS && !withX1) {
		return sdl.Rect{}, sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	}
	const groupPad = int32(4)
	const pillGap = int32(4)
	inner := height - groupPad*2
	if inner < 20 {
		return sdl.Rect{}, sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	}
	dot := int32(8)
	dotGap := int32(8)
	labelPad := int32(14)
	pillW := func(text string) int32 {
		w, _ := measureText(fonts.Small, text)
		return labelPad*2 + dot + dotGap + w
	}
	pill4W, pill5W, pillX1W := int32(0), int32(0), int32(0)
	if withPS {
		pill4W, pill5W = pillW("PS4"), pillW("PS5")
	}
	if withX1 {
		pillX1W = pillW(x1PillLabel)
	}
	captionW := int32(0)
	if label != "" {
		lw, _ := measureText(fonts.Small, label)
		if lw > 0 {
			// The caption's own inset from the capsule's rounded end, and the
			// gap before the first pill.
			captionW = labelPad + lw + labelPad/2
		}
	}
	gaps := int32(0)
	if withPS {
		gaps += pillGap // PS4 | PS5
		if withX1 {
			gaps += pillGap // PS5 | X1
		}
	}
	groupW := groupPad*2 + captionW + pill4W + pill5W + pillX1W + gaps
	group = sdl.Rect{X: right - groupW, Y: y, W: groupW, H: height}
	x := group.X + groupPad + captionW
	if withPS {
		ps4 = sdl.Rect{X: x, Y: y + groupPad, W: pill4W, H: inner}
		ps5 = sdl.Rect{X: ps4.X + pill4W + pillGap, Y: y + groupPad, W: pill5W, H: inner}
		x = ps5.X + pill5W + pillGap
	}
	if withX1 {
		x1 = sdl.Rect{X: x, Y: y + groupPad, W: pillX1W, H: inner}
	}
	return group, ps4, ps5, x1
}

// renderPSPillGroup draws the group. The container is the design's
// translucent capsule; a paired console (or, for X1, an installed client)
// lights its own pill, and the focused pill carries the home focus ring.
func renderPSPillGroup(renderer *sdl.Renderer, fonts HomeFonts, group, ps4, ps5, x1 sdl.Rect, v psPillVisual) {
	if renderer == nil || fonts.Small == nil || group.W <= 0 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, group.X, group.Y, group.W, group.H, group.H/2, homeGlass(13))
	strokeRoundedRect(renderer, group.X, group.Y, group.W, group.H, group.H/2, 1, homeGlass(20))

	// The caption fills the capsule left of the first pill, centred in it.
	first := ps4
	if first.W <= 0 {
		first = x1
	}
	if v.Label != "" && first.X > group.X {
		lw, _ := measureText(fonts.Small, v.Label)
		slot := first.X - group.X
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
	pill(x1, x1PillLabel, v.X1On, HomeFocusX1)
}
