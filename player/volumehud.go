package main

// The stock volume overlay belongs to trimui_osdd, which also opens the
// firmware quick menu over OS mode. Draw a small, non-interactive level card
// ourselves while the hardware keys are used.

import (
	"strconv"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const volumeHUDHold = 1900 * time.Millisecond

var volumeHUDUntil atomic.Int64

func volumeHUDNoteKey() {
	volumeHUDUntil.Store(time.Now().Add(volumeHUDHold).UnixNano())
}

func volumeHUDVisible() bool {
	return time.Now().UnixNano() < volumeHUDUntil.Load()
}

func renderVolumeHUD(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !IsTrimuiDevice() || !volumeHUDVisible() || shotModeOn() {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	pct, known := volumeCurrent()
	if !known {
		pct = volumeBase()
	}
	const w, h int32 = 244, 74
	x := (screenWidth - w) / 2
	y := HomeTopBarH + 12
	if x < 8 {
		x = 8
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, x, y, w, h, 12, glassSheetFill())
	strokeRoundedRect(renderer, x, y, w, h, 12, 1, glassSheetEdge())
	ink := ColorTextPrimary()
	iconDraw(renderer, statusVolumeGlyph(pct, known), sdl.Rect{X: x + 16, Y: y + 14, W: 24, H: 24}, ink)
	renderText(renderer, config, font, "Volume", ink, x+52, y+14)
	label := "…"
	if known {
		label = strconv.Itoa(pct) + "%"
	}
	lw, _ := measureText(font, label)
	renderText(renderer, config, font, label, ink, x+w-16-lw, y+14)
	barX, barY, barW := x+16, y+53, w-32
	fillRoundedRect(renderer, barX, barY, barW, 7, 3, glassA(60))
	if known && pct > 0 {
		fill := barW * int32(pct) / 100
		fillRoundedRect(renderer, barX, barY, max32(fill, 7), 7, 3, glassAccent())
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE)
}
