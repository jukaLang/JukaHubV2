package main

// ps4tile.go — the PS4 Remote Play launcher in the home header.
//
// It is the PS4 twin of ps5tile.go: it sits immediately left of the PS5 tile
// (and therefore two slots left of the Phone Companion QR), in the same band
// above the action cards, and is part of the D-pad focus graph (HomeFocusPS4).
// Up from the card it sits over lands on the PS5 tile, Left reaches this one.
//
// One press opens JukaHub's own Remote Play client filtered to PS4: a paired
// console starts straight away, an unpaired one gets the PIN prompt.

import (
	"github.com/veandco/go-sdl2/sdl"
)

// ps4TileRect is this frame's launcher rectangle; it is cleared when the tile
// is not drawn (small screens), so a stale rect cannot swallow clicks.
var ps4TileRect sdl.Rect

type ps4TileState int

const (
	ps4TileBusy ps4TileState = iota
	ps4TileLaunch
	ps4TileInstall
)

// ps4TileCurrentState reports what a press would do right now.
func ps4TileCurrentState() ps4TileState {
	if !psRemoteSupported() {
		return ps4TileBusy
	}
	for _, c := range loadPSHosts().Hosts {
		if c.PS5 || c.Target >= psTargetPS5 {
			continue
		}
		if c.paired() {
			return ps4TileLaunch
		}
	}
	return ps4TileInstall
}

// ps4TileRectFor places the launcher in the slot left of the PS5 launcher. The
// second result is false when there is no room.
func ps4TileRectFor(neighbor sdl.Rect, gap, minX int32) (sdl.Rect, bool) {
	if neighbor.W <= 0 || neighbor.H <= 0 {
		return sdl.Rect{}, false
	}
	x := neighbor.X - gap - neighbor.W
	if x < minX {
		return sdl.Rect{}, false
	}
	if x+neighbor.W > neighbor.X {
		return sdl.Rect{}, false
	}
	return sdl.Rect{X: x, Y: neighbor.Y, W: neighbor.W, H: neighbor.H}, true
}

// ps4TileVisual is this frame's interaction state for the launcher.
type ps4TileVisual struct {
	Focused bool
	Pressed bool
	Depth   float64
}

// renderPS4HeaderTile draws the launcher and records its rect. It returns the
// rect it drew into, or an empty rect when the tile is hidden.
func renderPS4HeaderTile(renderer *sdl.Renderer, fonts HomeFonts, rect sdl.Rect, v ps4TileVisual) sdl.Rect {
	ps4TileRect = sdl.Rect{}
	if renderer == nil || fonts.Small == nil || rect.W < 72 || rect.H < 72 {
		return sdl.Rect{}
	}

	hit := rect // the press dip moves the paint, never the hit box
	state := ps4TileCurrentState()
	// Deliberately darker than the PS5 tile: two identical blue squares in one
	// header read as a mistake.
	fill := sdl.Color{R: 18, G: 62, B: 118, A: 255}
	caption := "PLAY"
	switch state {
	case ps4TileBusy:
		fill = sdl.Color{R: 38, G: 50, B: 66, A: 255}
		// "N/A" read as a broken value; the dash is the common dimmed-affordance
		// shorthand and matches the dimmed fill.
		caption = "·"
	case ps4TileInstall:
		caption = "PAIR"
	}
	border := WithAlpha(sdl.Color{R: 130, G: 180, B: 240, A: 255}, 200)
	if v.Focused {
		fill = sdl.Color{R: 32, G: 96, B: 176, A: 255}
		border = sdl.Color{R: 235, G: 245, B: 255, A: 255}
	}

	draw := rect
	if v.Pressed {
		if v.Depth > 0 && v.Depth < 1 {
			draw.Y += int32(2 * v.Depth)
		}
		fill = sdl.Color{R: 36, G: 104, B: 186, A: 255}
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
		strokeRoundedRect(renderer, draw.X+2, draw.Y+2, draw.W-4, draw.H-4, RadiusSM, 2,
			sdl.Color{R: 235, G: 245, B: 255, A: 255})
	}
	rect = draw

	labelFont := fonts.Card
	if labelFont == nil {
		labelFont = fonts.Small
	}
	label := "PS4"
	labelW, labelH, err := labelFont.SizeUTF8(label)
	if err != nil || int32(labelW) > rect.W-16 {
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
		caption = "A " + caption
	}
	drawText(renderer, fonts.Small, caption,
		rect.X+rect.W/2, top+int32(labelH)+2, sdl.Color{R: 226, G: 240, B: 255, A: 255}, textAlignCenter)

	ps4TileRect = hit
	return hit
}

// ps4TileActivate opens the client on a PS4.
func ps4TileActivate() {
	if ps4TileCurrentState() == ps4TileBusy {
		ShowToast("Remote Play needs the TrimUI Linux build", ToastKindWarning)
		return
	}
	PlayActivateSound()
	psOpenPanelFor("", psTargetPS4FW10)
}
