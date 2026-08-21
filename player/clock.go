package main

import (
	"math"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Analog clock widget for the home screen
// ──────────────────────────────────────────────────────────────────────

const (
	clockRadius  = 90
	clockFaceR   = 88 // outer face radius
	clockInnerR  = 70 // inner glass area
	clockCenterX = 1100
	clockCenterY = 100
)

// renderAnalogClock draws a glass-morphism analog clock face with hour,
// minute, and second hands. It sits in the top-right corner of the home
// screen and is always animated — the second hand ticks smoothly.
func renderAnalogClock(renderer *sdl.Renderer, config *Config) {
	now := time.Now()
	hour, min, sec := now.Hour(), now.Minute(), now.Second()
	nsec := now.Nanosecond()
	cx, cy := int32(clockCenterX), int32(clockCenterY)
	r := int32(clockRadius)

	// Outer glow ring.
	for i := int32(0); i < 4; i++ {
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B,
			uint8(30-i*7))
		strokeCircle(renderer, cx, cy, r+i, sdl.Color{
			R: accentColor.R, G: accentColor.G, B: accentColor.B,
			A: uint8(30 - i*7),
		})
	}

	// Face: dark translucent fill with subtle border.
	fillCircle(renderer, cx, cy, r, sdl.Color{R: 12, G: 16, B: 28, A: 220})
	strokeCircle(renderer, cx, cy, r, sdl.Color{
		R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 120,
	})

	// Inner glass panel (lighter ring).
	fillCircle(renderer, cx, cy, clockInnerR, sdl.Color{R: 18, G: 22, B: 36, A: 200})

	// Hour markers (12 lines around the face).
	for i := 0; i < 12; i++ {
		angle := float64(i)*math.Pi/6 - math.Pi/2
		major := i%3 == 0
		innerFrac := 0.78
		outerFrac := 0.92
		if major {
			innerFrac = 0.72
		}
		x1 := cx + int32(float64(r)*innerFrac*math.Cos(angle))
		y1 := cy + int32(float64(r)*innerFrac*math.Sin(angle))
		x2 := cx + int32(float64(r)*outerFrac*math.Cos(angle))
		y2 := cy + int32(float64(r)*outerFrac*math.Sin(angle))
		markerCol := sdl.Color{R: 160, G: 170, B: 190, A: 180}
		if major {
			markerCol = sdl.Color{R: 220, G: 230, B: 245, A: 240}
		}
		renderer.SetDrawColor(markerCol.R, markerCol.G, markerCol.B, markerCol.A)
		renderer.DrawLine(x1, y1, x2, y2)
	}

	// Minute tick marks (small dots between hour markers).
	for i := 0; i < 60; i++ {
		if i%5 == 0 {
			continue
		}
		angle := float64(i)*math.Pi/30 - math.Pi/2
		tx := cx + int32(float64(r)*0.88*math.Cos(angle))
		ty := cy + int32(float64(r)*0.88*math.Sin(angle))
		renderer.SetDrawColor(80, 90, 110, 100)
		renderer.DrawPoint(tx, ty)
	}

	// Hour hand.
	hourAngle := (float64(hour%12)+float64(min)/60.0)*math.Pi/6 - math.Pi/2
	drawClockHand(renderer, cx, cy, hourAngle, 40, 5,
		sdl.Color{R: 220, G: 230, B: 245, A: 255})

	// Minute hand.
	minAngle := (float64(min)+float64(sec)/60.0)*math.Pi/30 - math.Pi/2
	drawClockHand(renderer, cx, cy, minAngle, 56, 3,
		sdl.Color{R: 200, G: 210, B: 230, A: 255})

	// Second hand: smooth sweep with centisecond precision.
	secAngle := (float64(sec)+float64(nsec)/1e9)*math.Pi/30 - math.Pi/2
	drawClockHand(renderer, cx, cy, secAngle, 62, 1,
		sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 255})

	// Center hub.
	fillCircle(renderer, cx, cy, 4, sdl.Color{R: 230, G: 240, B: 255, A: 255})

	// Digital time below the clock face.
	font, _ := getCachedFont(config, "small")
	if font != nil {
		timeStr := now.Format("15:04:05")
		tw, _, _ := font.SizeUTF8(timeStr)
		tx := cx - int32(tw)/2
		ty := cy + r + 10
		renderText(renderer, config, font, timeStr,
			sdl.Color{R: 160, G: 175, B: 200, A: 200}, tx, ty)

		dateStr := now.Format("Mon Jan 2")
		dw, _, _ := font.SizeUTF8(dateStr)
		dx := cx - int32(dw)/2
		dy := ty + 18
		renderText(renderer, config, font, dateStr,
			sdl.Color{R: 110, G: 125, B: 150, A: 160}, dx, dy)
	}
}

// drawClockHand draws a clock hand as a tapered line from center outward.
func drawClockHand(renderer *sdl.Renderer, cx, cy int32, angle float64,
	length, width int32, col sdl.Color) {
	tx := cx + int32(float64(length)*math.Cos(angle))
	ty := cy + int32(float64(length)*math.Sin(angle))

	// Draw the hand as a series of points for proper thickness.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	for dx := -width / 2; dx <= width/2; dx++ {
		// Perpendicular offset.
		px := float64(dx) * math.Cos(angle+math.Pi/2)
		py := float64(dx) * math.Sin(angle+math.Pi/2)
		renderer.SetDrawColor(col.R, col.G, col.B, col.A)
		renderer.DrawLine(cx+int32(px), cy+int32(py), tx+int32(px), ty+int32(py))
	}
}
