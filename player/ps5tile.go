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
// renderPS5HeaderTile draws the launcher and records its rect. It returns the
// rect it drew into, or an empty rect when the tile is hidden.
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
