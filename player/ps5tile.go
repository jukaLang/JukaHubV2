package main

// ps5tile.go — the PS5 Remote Play launcher in the home header.
//
// It sits immediately left of the Phone Companion QR tile, in the empty band
// above the action cards, and never overlaps it: the QR keeps its own rectangle
// and the launcher is only drawn when there is room for both. Unlike the QR it
// is part of the D-pad focus graph (HomeFocusPS5): Up from the card it sits over
// lands on it and A opens the client on the PS5, so the handheld can use it
// without a touchscreen. Which card that is depends on the width of the status
// cluster in the header, so the focus edge is derived from the drawn rect each
// frame rather than hard-coded. While the tile is not drawn its focus entry
// stays disabled, so the D-pad can never stall on an invisible button.

import (
	"github.com/veandco/go-sdl2/sdl"
)

// ps5TileRect is this frame's launcher rectangle; it is cleared when the tile
// is not drawn (small screens), so a stale rect cannot swallow clicks.
var ps5TileRect sdl.Rect

type ps5TileState int

const (
	ps5TileBusy ps5TileState = iota
	ps5TileLaunch
	ps5TileInstall
)

// ps5TileCurrentState reports what a press would do right now: play a paired
// console, or open the client to pair one.
func ps5TileCurrentState() ps5TileState {
	if !psRemoteSupported() {
		return ps5TileBusy
	}
	for _, c := range loadPSHosts().Hosts {
		if (c.PS5 || c.Target >= psTargetPS5) && c.paired() {
			return ps5TileLaunch
		}
	}
	return ps5TileInstall
}

// ps5TileRectFor places the launcher in the slot left of the QR tile. The
// second result is false when the slot would start too far left, where it would
// collide with the brand text or the greeting column; the tile is then simply
// not drawn.
func ps5TileRectFor(qr sdl.Rect, gap, minX int32) (sdl.Rect, bool) {
	if qr.W <= 0 || qr.H <= 0 {
		return sdl.Rect{}, false
	}
	x := qr.X - gap - qr.W
	if x < minX {
		return sdl.Rect{}, false
	}
	// Never overlap the QR, even if a caller passes a negative gap.
	if x+qr.W > qr.X {
		return sdl.Rect{}, false
	}
	return sdl.Rect{X: x, Y: qr.Y, W: qr.W, H: qr.H}, true
}

// ps5TileVisual is this frame's interaction state for the launcher.
type ps5TileVisual struct {
	Focused bool
	Pressed bool
	Depth   float64
}

// renderPS5HeaderTile draws the launcher and records its rect. It returns the
// rect it drew into, or an empty rect when the tile is hidden.
func renderPS5HeaderTile(renderer *sdl.Renderer, fonts HomeFonts, rect sdl.Rect, v ps5TileVisual) sdl.Rect {
	ps5TileRect = sdl.Rect{}
	if renderer == nil || fonts.Small == nil || rect.W < 72 || rect.H < 72 {
		return sdl.Rect{}
	}

	hit := rect // the press dip moves the paint, never the hit box
	state := ps5TileCurrentState()
	fill := sdl.Color{R: 0, G: 112, B: 209, A: 255} // PlayStation blue
	caption := "PLAY"
	switch state {
	case ps5TileBusy:
		fill = sdl.Color{R: 38, G: 66, B: 102, A: 255}
		// "N/A" read as a broken value; the dash is the common dimmed-affordance
		// shorthand and matches the dimmed fill.
		caption = "·"
	case ps5TileInstall:
		caption = "PAIR"
	}
	border := WithAlpha(sdl.Color{R: 150, G: 205, B: 255, A: 255}, 210)
	if v.Focused {
		// A focused tile is the one the A button will press: lift the fill and
		// give it a bright border so the D-pad position is unmistakable on a
		// small handheld screen.
		fill = sdl.Color{R: 16, G: 128, B: 232, A: 255}
		border = sdl.Color{R: 235, G: 245, B: 255, A: 255}
	}

	// Press feedback: the tile dips a couple of pixels and brightens while the
	// button is held, so a press from the D-pad reads as a press and not a miss.
	draw := rect
	if v.Pressed {
		if v.Depth > 0 && v.Depth < 1 {
			draw.Y += int32(2 * v.Depth)
		}
		fill = sdl.Color{R: 26, G: 132, B: 232, A: 255}
		border = ColorAccent
	}

	UIButton(renderer, nil, draw, "", UIButtonStyle{
		Variant:  UIButtonCustom,
		Fill:     fill,
		Border:   border,
		Radius:   RadiusSM,
		NoShadow: true,
	}, UIButtonState{})
	if v.Focused {
		// The header draws an accent ring around the tile as well; this inner
		// stroke keeps the highlight readable against the blue fill.
		strokeRoundedRect(renderer, draw.X+2, draw.Y+2, draw.W-4, draw.H-4, RadiusSM, 2,
			sdl.Color{R: 235, G: 245, B: 255, A: 255})
	}
	rect = draw

	labelFont := fonts.Card
	if labelFont == nil {
		labelFont = fonts.Small
	}
	label := "PS5"
	labelW, labelH, err := labelFont.SizeUTF8(label)
	if err != nil || int32(labelW) > rect.W-16 {
		// Fall back to the small face rather than clipping the wordmark.
		labelFont = fonts.Small
		labelW, labelH, _ = labelFont.SizeUTF8(label)
	}
	captionH := companionFontLine(fonts.Small) + 2
	blockH := int32(labelH) + 2 + captionH
	top := rect.Y + (rect.H-blockH)/2
	if top < rect.Y+4 {
		top = rect.Y + 4
	}
	drawText(renderer, labelFont, label,
		rect.X+(rect.W-int32(labelW))/2, top, sdl.Color{R: 255, G: 255, B: 255, A: 255}, textAlignLeft)
	if v.Focused {
		// Selected: name the button that presses it. The handheld has no
		// touchscreen, so the caption doubles as the affordance.
		caption = "A " + caption
	}
	drawText(renderer, fonts.Small, caption,
		rect.X+rect.W/2, top+int32(labelH)+2, sdl.Color{R: 226, G: 240, B: 255, A: 255}, textAlignCenter)

	ps5TileRect = hit
	return hit
}

// ps5TileActivate opens the client on the PS5. A paired console starts
// immediately; otherwise the panel asks for the PIN, which is exactly the step
// the old flow buried behind an installer.
func ps5TileActivate() {
	if ps5TileCurrentState() == ps5TileBusy {
		ShowToast("Remote Play needs the TrimUI Linux build", ToastKindWarning)
		return
	}
	PlayActivateSound()
	psOpenPanelFor("", psTargetPS5_1)
}
