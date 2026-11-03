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
// renderPS4HeaderTile draws the launcher and records its rect. It returns the
// rect it drew into, or an empty rect when the tile is hidden.
// ps4TileActivate opens the client on a PS4.
func ps4TileActivate() {
	if ps4TileCurrentState() == ps4TileBusy {
		ShowToast("Remote Play needs the TrimUI Linux build", ToastKindWarning)
		return
	}
	PlayActivateSound()
	psOpenPanelFor("", psTargetPS4FW10)
}
