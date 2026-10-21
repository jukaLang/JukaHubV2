package main

// ps4tile.go — the PS4 Remote Play launcher in the home header.
//
// It is the stable-build twin of ps5tile.go: it sits immediately left of the
// PS5 tile (and therefore two slots left of the Phone Companion QR), in the
// empty band above the action cards. It is part of the D-pad focus graph
// (HomeFocusPS4): Up from the card it sits over lands on the PS5 tile, Left
// reaches this one, and A installs or plays. While the tile is not drawn its
// focus entry stays disabled, so the D-pad can never stall on an invisible
// button.
//
// One press does the whole job: launch the installed PS4 build, otherwise
// download the latest release and launch it the moment it is ready. The launch
// itself runs on the event thread, never on the download goroutine, because it
// hides the main window and runs the child process.

import (
	"sync/atomic"

	"github.com/veandco/go-sdl2/sdl"
)

// ps4TileRect is this frame's launcher rectangle; it is cleared when the tile
// is not drawn (small screens), so a stale rect cannot swallow clicks.
var ps4TileRect sdl.Rect

// chiakiPS4LaunchAfterInstall remembers that the press came from "download and
// play", not from the installer panel.
var chiakiPS4LaunchAfterInstall atomic.Bool

// ps4LaunchPending is set by the install goroutine and consumed by the frame
// loop on the event thread.
var ps4LaunchPending atomic.Bool

type ps4TileState int

const (
	ps4TileBusy ps4TileState = iota
	ps4TileLaunch
	ps4TileInstall
)

// ps4TileCurrentState reports what a press would do right now.
func ps4TileCurrentState() ps4TileState {
	if chiakiInstalling() {
		return ps4TileBusy
	}
	if chiakiInstalled() {
		return ps4TileLaunch
	}
	return ps4TileInstall
}

// ps4TileRectFor places the launcher in the slot left of the PS5 launcher. The
// second result is false when the slot would start too far left, where it would
// collide with the brand text or the greeting column; the tile is then simply
// not drawn and its focus entry is disabled.
func ps4TileRectFor(neighbor sdl.Rect, gap, minX int32) (sdl.Rect, bool) {
	if neighbor.W <= 0 || neighbor.H <= 0 {
		return sdl.Rect{}, false
	}
	x := neighbor.X - gap - neighbor.W
	if x < minX {
		return sdl.Rect{}, false
	}
	// Never overlap the neighbour, even if a caller passes a negative gap.
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
	// header would be unreadable at arm's length.
	fill := sdl.Color{R: 0, G: 55, B: 145, A: 255}
	caption := "PLAY"
	switch state {
	case ps4TileBusy:
		fill = sdl.Color{R: 34, G: 48, B: 71, A: 255}
		caption = "WAIT"
	case ps4TileInstall:
		caption = "GET"
	}
	border := WithAlpha(sdl.Color{R: 150, G: 190, B: 255, A: 255}, 210)
	if v.Focused {
		fill = sdl.Color{R: 12, G: 79, B: 168, A: 255}
		border = sdl.Color{R: 235, G: 245, B: 255, A: 255}
	}

	draw := rect
	if v.Pressed {
		if v.Depth > 0 && v.Depth < 1 {
			draw.Y += int32(2 * v.Depth)
		}
		fill = sdl.Color{R: 20, G: 96, B: 186, A: 255}
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

// ps4TileActivate runs the single press action: play, or fetch and play.
func ps4TileActivate() {
	switch ps4TileCurrentState() {
	case ps4TileBusy:
		ShowToast("The Chiaki-ng installer is already running", ToastKindInfo)
		return
	case ps4TileLaunch:
		PlayActivateSound()
		launchChiaki()
		return
	}
	chiakiPS4LaunchAfterInstall.Store(true)
	if err := startChiakiInstall(); err != nil {
		// Nothing was queued, so do not leave a launch armed for a later,
		// unrelated install.
		chiakiPS4LaunchAfterInstall.Store(false)
		ShowToast(err.Error(), ToastKindWarning)
		return
	}
	PlayActivateSound()
	ShowToast("Downloading Chiaki-ng — it will start when ready", ToastKindInfo)
}

// chiakiPS4InstallSucceeded hands a finished install over to the event thread
// when the press came from the launcher tile.
func chiakiPS4InstallSucceeded() {
	if chiakiPS4LaunchAfterInstall.Swap(false) {
		ps4LaunchPending.Store(true)
	}
}

// ps4PollPendingLaunch is called once per frame on the event thread. It clears
// the flag even when the build vanished, so a launch can never be retried
// forever.
func ps4PollPendingLaunch() {
	if !ps4LaunchPending.Swap(false) {
		return
	}
	if !chiakiInstalled() {
		ShowToast("Chiaki-ng is not installed", ToastKindWarning)
		return
	}
	launchChiaki()
}
