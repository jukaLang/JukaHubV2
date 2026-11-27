package main

// windowstate.go — the persisted desktop window geometry (DESK-3). The SDL
// calls stay in main.go; this file is the SDL-free decision and encoding, so
// parsing and clamping are testable without a display.

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	windowMinW = 480
	windowMinH = 320
)

// windowRect is one desktop window: origin and size in physical pixels, the
// monitor it belongs to, and the two window modes that matter to a player.
type windowRect struct {
	X, Y, W, H int
	Display    int
	Maximized  bool
	Borderless bool
}

// displayBounds is one monitor in SDL's virtual desktop (X and Y can be
// negative when a monitor sits left of or above the primary one).
type displayBounds struct {
	X, Y, W, H int
}

// formatWindowRect is the one Custom value stored in jukauser.json.
func formatWindowRect(r windowRect) string {
	return fmt.Sprintf("%d,%d,%d,%d,%d,%t,%t", r.X, r.Y, r.W, r.H, r.Display, r.Maximized, r.Borderless)
}

// parseWindowRect reads that value. A missing, short, malformed or tiny
// rectangle is rejected so the caller falls back to the config's canvas.
func parseWindowRect(text string) (windowRect, bool) {
	parts := strings.Split(strings.TrimSpace(text), ",")
	if len(parts) != 7 {
		return windowRect{}, false
	}
	nums := make([]int, 5)
	for i := 0; i < 5; i++ {
		n, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err != nil || n < -1<<20 || n > 1<<20 {
			return windowRect{}, false
		}
		nums[i] = n
	}
	max, errMax := strconv.ParseBool(strings.TrimSpace(parts[5]))
	borderless, errBorder := strconv.ParseBool(strings.TrimSpace(parts[6]))
	if errMax != nil || errBorder != nil || nums[2] < windowMinW || nums[3] < windowMinH || nums[4] < -1 {
		return windowRect{}, false
	}
	return windowRect{
		X: nums[0], Y: nums[1], W: nums[2], H: nums[3], Display: nums[4],
		Maximized: max, Borderless: borderless,
	}, true
}

// clampWindowRect keeps a remembered window usable after a monitor was
// unplugged or resized. The chosen display wins when it still exists; a
// missing one falls back to the first display, and a missing display list
// leaves the caller's own defaults in charge (valid stays false).
func clampWindowRect(r windowRect, displays []displayBounds) (windowRect, bool) {
	if len(displays) == 0 {
		return r, false
	}
	d := r.Display
	if d < 0 || d >= len(displays) {
		d = 0
	}
	b := displays[d]
	if b.W < windowMinW || b.H < windowMinH {
		for i := range displays {
			if displays[i].W >= windowMinW && displays[i].H >= windowMinH {
				d, b = i, displays[i]
				break
			}
		}
	}
	if b.W < windowMinW || b.H < windowMinH {
		return r, false
	}
	r.Display = d
	if r.W <= 0 || r.W > b.W {
		r.W = b.W * 3 / 4
	}
	if r.H <= 0 || r.H > b.H {
		r.H = b.H * 3 / 4
	}
	if r.W < windowMinW {
		r.W = windowMinW
	}
	if r.H < windowMinH {
		r.H = windowMinH
	}
	if r.Maximized {
		r.X, r.Y = b.X, b.Y
		r.W, r.H = b.W, b.H
		return r, true
	}
	// Keep at least 64 px of the title bar and a side visible on this monitor.
	if r.X < b.X-64 || r.X > b.X+b.W-64 || r.Y < b.Y || r.Y > b.Y+b.H-64 {
		r.X = b.X + (b.W-r.W)/2
		r.Y = b.Y + (b.H-r.H)/2
		if r.X < b.X {
			r.X = b.X
		}
		if r.Y < b.Y {
			r.Y = b.Y
		}
	}
	return r, true
}
