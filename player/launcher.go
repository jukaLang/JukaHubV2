package main

import (
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// tileAnimMap eases per-tile focus progress (0..1) so the selected tile scales
// up smoothly (~120-160ms) instead of snapping.
var tileAnimMap = make(map[int]float64)

// tileIconKind maps a home-screen destination to a stable icon family so tiles
// use consistent line icons instead of monogram letters.
func tileIconKind(elem Element) string {
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

// drawTileIcon renders a monochrome line icon centered in a box of the given
// size using pure SDL primitives (no external art assets required).
func drawTileIcon(renderer *sdl.Renderer, cx, cy, size int32, kind string, c sdl.Color) {
	renderer.SetDrawColor(c.R, c.G, c.B, c.A)
	u := size / 12
	if u < 1 {
		u = 1
	}
	s := size / 3

	switch kind {
	case "play":
		// play triangle inside a rounded rect frame
		r := int32(4)
		fillRoundedRect(renderer, cx-size/2, cy-size/2, size, size, r, WithAlpha(c, 18))
		renderer.DrawRect(&sdl.Rect{X: cx - size/2, Y: cy - size/2, W: size, H: size})
		a := pt{cx - s/2, cy - s/2 + 2}
		b := pt{cx - s/2, cy + s/2 - 2}
		cc := pt{cx + s/2 - 1, cy}
		fillTriangleFilled(renderer, a, b, cc, c)
	case "chat":
		bw := size - size/4
		bh := size - size/4
		bx := cx - bw/2
		by := cy - bh/2 + size/10
		fillRoundedRect(renderer, bx, by, bw, bh, 8, WithAlpha(c, 18))
		renderer.DrawRect(&sdl.Rect{X: bx, Y: by, W: bw, H: bh})
		// tail
		renderer.DrawLine(bx+size/8, by+bh-1, bx+size/8+4, by+bh+size/8)
		renderer.DrawLine(bx+size/8+4, by+bh+size/8, bx+size/5, by+bh-2)
		// dots
		dotR := u
		fillCircle(renderer, bx+bw/2-4*u, by+bh/2, dotR, c)
		fillCircle(renderer, bx+bw/2, by+bh/2, dotR, c)
		fillCircle(renderer, bx+bw/2+4*u, by+bh/2, dotR, c)
	case "files":
		// folder
		top := cy - size/3
		bot := cy + size/3
		fillRoundedRect(renderer, cx-size/2, top, size, bot-top, 4, WithAlpha(c, 18))
		renderer.DrawRect(&sdl.Rect{X: cx - size/2, Y: top, W: size, H: bot - top})
		renderer.DrawLine(cx-size/2+2, top, cx-size/2+size/4, top-4)
		renderer.DrawLine(cx-size/2+size/4, top-4, cx-size/2+size/2, top-4)
	case "tv":
		w := size
		h := size - size/4
		x := cx - w/2
		y := cy - h/2 + size/12
		fillRoundedRect(renderer, x, y, w, h, 5, WithAlpha(c, 18))
		renderer.DrawRect(&sdl.Rect{X: x, Y: y, W: w, H: h})
		// stand
		renderer.DrawLine(cx, y+h, cx, y+h+size/6)
		renderer.DrawLine(cx-size/5, y+h+size/6+2, cx+size/5, y+h+size/6+2)
		// screen accent
		renderer.DrawRect(&sdl.Rect{X: x + 6, Y: y + 5, W: w - 12, H: h - 12})
	case "box":
		w := size - size/6
		h := size - size/6
		x := cx - w/2
		y := cy - h/2 + size/12
		fillRoundedRect(renderer, x, y, w, h, 3, WithAlpha(c, 18))
		renderer.DrawRect(&sdl.Rect{X: x, Y: y, W: w, H: h})
		renderer.DrawLine(x, y+h/3, x+w, y+h/3)
		renderer.DrawLine(cx, y, cx, y+h/3)
		// down arrow on the flap
		renderer.DrawLine(cx, y+h/3+2, cx-u, y+h/3+2+2*u)
		renderer.DrawLine(cx, y+h/3+2, cx+u, y+h/3+2+2*u)
	case "mic":
		// capsule body
		bw := size / 3
		bh := size - size/4
		bx := cx - bw/2
		by := cy - bh/2 + size/10
		fillRoundedRect(renderer, bx, by, bw, bh, bw/2, WithAlpha(c, 18))
		renderer.DrawRect(&sdl.Rect{X: bx, Y: by, W: bw, H: bh})
		// stand
		renderer.DrawLine(cx, by+bh, cx, by+bh+size/8)
		renderer.DrawLine(cx-size/4, by+bh+size/8, cx+size/4, by+bh+size/8)
	case "chart":
		// trending line
		renderer.DrawLine(cx-size/2+4, cy+size/3-4, cx-size/8, cy-size/12)
		renderer.DrawLine(cx-size/8, cy-size/12, cx+size/8, cy+size/6)
		renderer.DrawLine(cx+size/8, cy+size/6, cx+size/2-4, cy-size/3+4)
		// arrow head
		renderer.DrawLine(cx+size/2-4, cy-size/3+4, cx+size/2-size/6, cy-size/3+4)
		renderer.DrawLine(cx+size/2-4, cy-size/3+4, cx+size/2-4, cy-size/3+size/6)
	case "star":
		// 5-point star outline
		cx2, cy2 := float64(cx), float64(cy)
		outer := float64(size) / 2
		inner := outer * 0.45
		var pts []pt
		for i := 0; i < 10; i++ {
			r := outer
			if i%2 == 1 {
				r = inner
			}
			ang := -90.0 + float64(i)*36.0
			rad := ang * 3.14159265 / 180.0
			pts = append(pts, pt{x: int32(cx2 + r*cos64(rad)), y: int32(cy2 + r*sin64(rad))})
		}
		for i := 0; i < len(pts); i++ {
			n := (i + 1) % len(pts)
			renderer.DrawLine(pts[i].x, pts[i].y, pts[n].x, pts[n].y)
		}
	case "cloud":
		// cloud: overlapping circles + base
		baseY := cy + size/6
		renderer.DrawLine(cx-size/3, baseY, cx+size/3, baseY)
		fillCircle(renderer, cx-size/6, baseY-size/8, size/7, c)
		fillCircle(renderer, cx+size/8, baseY-size/6, size/6, c)
	case "gear":
		// gear: circle + spokes
		gr := size / 3
		fillCircle(renderer, cx, cy, gr, c)
		fillCircle(renderer, cx, cy, gr/2, ColorBackground)
		for i := 0; i < 8; i++ {
			ang := float64(i) * 45.0 * 3.14159265 / 180.0
			grf := float64(gr)
			gr2f := float64(gr + size/9)
			x1 := int32(float64(cx) + grf*cos64(ang))
			y1 := int32(float64(cy) + grf*sin64(ang))
			x2 := int32(float64(cx) + gr2f*cos64(ang))
			y2 := int32(float64(cy) + gr2f*sin64(ang))
			renderer.DrawLine(x1, y1, x2, y2)
		}
	case "chip":
		sq := size - size/3
		x := cx - sq/2
		y := cy - sq/2
		fillRoundedRect(renderer, x, y, sq, sq, 4, WithAlpha(c, 18))
		renderer.DrawRect(&sdl.Rect{X: x, Y: y, W: sq, H: sq})
		// pins
		pin := size / 8
		for i := 0; i < 3; i++ {
			py := y + sq/4 + int32(i)*sq/4
			renderer.DrawLine(x-pin/2, py, x, py)
			renderer.DrawLine(x+sq, py, x+sq+pin/2, py)
		}
		renderer.DrawRect(&sdl.Rect{X: x + 4, Y: y + 4, W: sq - 8, H: sq - 8})
	case "terminal":
		// command prompt box with >_
		bw := size - size/6
		bh := size - size/6
		bx := cx - bw/2
		by := cy - bh/2
		fillRoundedRect(renderer, bx, by, bw, bh, 4, WithAlpha(c, 18))
		renderer.DrawRect(&sdl.Rect{X: bx, Y: by, W: bw, H: bh})
		renderer.DrawLine(bx+size/6, by+bh/2, bx+size/3, by+bh/2)
		renderer.DrawLine(bx+size/3, by+bh/2, bx+size/3+size/8, by+bh/2-size/8)
		renderer.DrawLine(bx+size/2, by+bh/2, bx+size/2+size/6, by+bh/2)
	case "game":
		// gamepad silhouette
		w := size
		h := size - size/3
		x := cx - w/2
		y := cy - h/2 + size/8
		fillRoundedRect(renderer, x, y, w, h, h/2, WithAlpha(c, 18))
		renderer.DrawRect(&sdl.Rect{X: x, Y: y, W: w, H: h})
		fillCircle(renderer, cx-w/4, cy+size/10, u, c)
		fillCircle(renderer, cx+w/4, cy+size/10, u, c)
	case "resume":
		// play in circle
		fillCircle(renderer, cx, cy, size/2, WithAlpha(c, 18))
		fillCircle(renderer, cx, cy, size/2, c)
		a := pt{cx - size/8, cy - size/6}
		b := pt{cx - size/8, cy + size/6}
		cc := pt{cx + size/5, cy}
		fillTriangleFilled(renderer, a, b, cc, sdl.Color{R: 8, G: 10, B: 16, A: 255})
	default:
		// app grid
		g := size / 4
		for i := int32(0); i < 2; i++ {
			for j := int32(0); j < 2; j++ {
				gx := cx - g - 3 + i*(g+6)
				gy := cy - g - 3 + j*(g+6)
				fillRoundedRect(renderer, gx, gy, g, g, 3, WithAlpha(c, 18))
				renderer.DrawRect(&sdl.Rect{X: gx, Y: gy, W: g, H: g})
			}
		}
	}
}

func cos64(a float64) float64 { return math.Cos(a) }
func sin64(a float64) float64 { return math.Sin(a) }

// fillTriangleFilled draws a solid triangle by scan-conversion using a dense
// line fan so it reads as a filled shape (matching the renderCircleSector style).
func fillTriangleFilled(renderer *sdl.Renderer, a, b, c pt, col sdl.Color) {
	renderer.SetDrawColor(col.R, col.G, col.B, col.A)
	// rasterize per row between minY and maxY
	minY := a.y
	maxY := a.y
	for _, p := range []pt{a, b, c} {
		if p.y < minY {
			minY = p.y
		}
		if p.y > maxY {
			maxY = p.y
		}
	}
	for y := minY; y <= maxY; y++ {
		var xs []int32
		edges := [][2]pt{{a, b}, {b, c}, {c, a}}
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
