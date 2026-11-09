package main

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// explicitTileIcon returns the glyph name an element asks for by name, or ""
// when Icon is empty or a monogram letter. Elements that carry a glyph name
// (the Tools tiles do) must get exactly that glyph: the substring mapping below
// would otherwise pick a different family for, say, "Disk Space".
func explicitTileIcon(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	switch key {
	case "play", "chat", "files", "tv", "box", "mic", "chart", "star", "cloud",
		"gear", "palette", "chip", "terminal", "game", "resume",
		"clock", "speed", "disk", "logs", "cache", "update", "network":
		return key
	}
	// Any name of the shared icon set (icondefs.go) is a glyph name too. One
	// letter is a monogram, never a glyph, even where an alias would match.
	if len(key) > 1 && iconKnown(key) {
		return key
	}
	return ""
}

// tileIconKind maps a home-screen destination to a stable icon family so tiles
// use consistent line icons instead of monogram letters.
func tileIconKind(elem Element) string {
	if kind := explicitTileIcon(elem.Icon); kind != "" {
		return kind
	}
	key := strings.ToLower(elem.Text + " " + elem.Icon + " " + elem.Trigger)
	switch {
	case strings.Contains(key, "youtube"), strings.Contains(key, "media"), strings.Contains(key, "play"):
		return "play"
	case strings.Contains(key, "chat"), strings.Contains(key, "discord"):
		return "chat"
	case strings.Contains(key, "file"), strings.Contains(key, "explorer"):
		return "files"
	case strings.Contains(key, "live"), strings.Contains(key, "tv"), strings.Contains(key, "stream"):
		return "tv"
	case strings.Contains(key, "package"), strings.Contains(key, "store"):
		return "box"
	case strings.Contains(key, "podcast"), strings.Contains(key, "shorts"):
		return "mic"
	case strings.Contains(key, "ticker"):
		return "chart"
	case strings.Contains(key, "favorite"):
		return "star"
	case strings.Contains(key, "weather"):
		return "cloud"
	case strings.Contains(key, "setting"):
		return "gear"
	case strings.Contains(key, "misc"), strings.Contains(key, "apps"), strings.Contains(key, "tools"):
		return "apps"
	case strings.Contains(key, "hardware"), strings.Contains(key, "device"), strings.Contains(key, "system"):
		return "chip"
	case strings.Contains(key, "terminal"), strings.Contains(key, "cron"), strings.Contains(key, "task"):
		return "terminal"
	case strings.Contains(key, "jukaland"), strings.Contains(key, "game"):
		return "game"
	case strings.Contains(key, "continue"), strings.Contains(key, "recent"):
		return "resume"
	default:
		return "apps"
	}
}

// drawTileIcon renders a tile's glyph from the shared vector icon set
// (icondefs.go), centred on (cx, cy) in a size×size box, in fg. kind is any
// icon name or one of the launcher's legacy kinds ("box", "chart", "gear",
// "chip", "game", "resume", "cache"), which icondefs.go aliases; an unknown
// kind draws the Apps grid, as it always has. bg used to back the old line
// drawings; the vector glyphs carry their own translucent body instead.
func drawTileIcon(renderer *sdl.Renderer, cx, cy, size int32, kind string, fg, bg sdl.Color) {
	if size <= 0 {
		return
	}
	if !iconKnown(kind) {
		kind = "apps"
	}
	iconDraw(renderer, kind, sdl.Rect{X: cx - size/2, Y: cy - size/2, W: size, H: size}, fg)
}

// fillTriangleFilled draws a solid triangle by scan-conversion using a dense
// line fan so it reads as a filled shape (matching the renderCircleSector style).
func fillTriangleFilled(renderer *sdl.Renderer, a, b, fg pt, col sdl.Color) {
	renderer.SetDrawColor(col.R, col.G, col.B, col.A)
	// rasterize per row between minY and maxY
	minY := a.y
	maxY := a.y
	for _, p := range []pt{a, b, fg} {
		if p.y < minY {
			minY = p.y
		}
		if p.y > maxY {
			maxY = p.y
		}
	}
	for y := minY; y <= maxY; y++ {
		var xs []int32
		edges := [][2]pt{{a, b}, {b, fg}, {fg, a}}
		for _, e := range edges {
			if e[0].y == e[1].y {
				continue
			}
			t := float64(y-e[0].y) / float64(e[1].y-e[0].y)
			if t < 0 || t > 1 {
				continue
			}
			xs = append(xs, int32(float64(e[0].x)+t*float64(e[1].x-e[0].x)))
		}
		if len(xs) < 2 {
			continue
		}
		lo, hi := xs[0], xs[1]
		if lo > hi {
			lo, hi = hi, lo
		}
		if hi >= lo {
			renderer.DrawLine(lo, y, hi, y)
		}
	}
}
