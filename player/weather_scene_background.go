package main

// The full Weather page uses today's forecast to paint quiet condition motion
// behind its text. It runs only while that page is visible and respects Reduce
// Motion and Low Power through uiMotionEnabled.

import (
	"math"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func renderWeatherSceneBackground(renderer *sdl.Renderer, config *Config, area sdl.Rect) {
	if renderer == nil || area.W <= 0 || area.H <= 0 || !weatherIsEnabled() {
		return
	}
	wxMutex.Lock()
	day, ok := weatherDayFor(weatherDaily, time.Now())
	ready := weatherReady
	wxMutex.Unlock()
	if !ready || !ok {
		return
	}

	wasClipped := renderer.IsClipEnabled()
	oldClip := renderer.GetClipRect()
	_ = renderer.SetClipRect(&area)
	defer func() {
		if wasClipped {
			_ = renderer.SetClipRect(&oldClip)
		} else {
			_ = renderer.SetClipRect(nil)
		}
	}()
	oldR, oldG, oldB, oldA, _ := renderer.GetDrawColor()
	defer renderer.SetDrawColor(oldR, oldG, oldB, oldA)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	animate := uiMotionEnabled(config)
	tick := uint64(0)
	if animate {
		tick = sdl.GetTicks64()
	}
	switch weatherSceneCondition(day.Code) {
	case "sun":
		weatherSceneSun(renderer, area, tick, animate)
	case "rain":
		weatherSceneRain(renderer, area, tick)
	case "snow":
		weatherSceneSnow(renderer, area, tick)
	default:
		// A soft sky tint keeps cloudy and foggy pages calm.
		fillRoundedRect(renderer, area.X, area.Y, area.W, area.H, 12,
			sdl.Color{R: 119, G: 158, B: 198, A: 18})
	}
}

func weatherSceneCondition(code int) string {
	switch {
	case code >= 0 && code <= 2:
		return "sun"
	case code >= 51 && code <= 67, code >= 80 && code <= 82, code >= 95 && code <= 99:
		return "rain"
	case code >= 71 && code <= 77, code == 85 || code == 86:
		return "snow"
	default:
		return "cloud"
	}
}

func weatherSceneSun(renderer *sdl.Renderer, area sdl.Rect, tick uint64, animate bool) {
	fillRoundedRect(renderer, area.X, area.Y, area.W, area.H, 12,
		sdl.Color{R: 250, G: 180, B: 74, A: 18})
	cx, cy := area.X+area.W-170, area.Y+150
	if area.W < 650 {
		cx = area.X + area.W*3/4
	}
	pulse := int32(0)
	if animate {
		pulse = int32(6 * math.Sin(float64(tick%3200)/3200*2*math.Pi))
	}
	fillCircle(renderer, cx, cy, 118+pulse, sdl.Color{R: 255, G: 185, B: 67, A: 15})
	fillCircle(renderer, cx, cy, 86+pulse/2, sdl.Color{R: 255, G: 192, B: 78, A: 32})
	for i := 0; i < 12; i++ {
		angle := float64(i)*math.Pi/6 + float64(tick%24000)/24000*math.Pi/3
		inner, outer := float64(76), float64(102)
		x1 := cx + int32(math.Cos(angle)*inner)
		y1 := cy + int32(math.Sin(angle)*inner)
		x2 := cx + int32(math.Cos(angle)*outer)
		y2 := cy + int32(math.Sin(angle)*outer)
		_ = renderer.SetDrawColor(255, 211, 111, 112)
		_ = renderer.DrawLine(x1, y1, x2, y2)
	}
	fillCircle(renderer, cx, cy, 64, sdl.Color{R: 255, G: 212, B: 116, A: 155})
}

func weatherSceneRain(renderer *sdl.Renderer, area sdl.Rect, tick uint64) {
	fillRoundedRect(renderer, area.X, area.Y, area.W, area.H, 12,
		sdl.Color{R: 57, G: 110, B: 170, A: 25})
	for i := int32(0); i < 72; i++ {
		x := area.X + (i*179+37)%area.W
		speed := uint64(7 + i%5)
		y := area.Y + int32((uint64(i*83)+tick*speed/20)%uint64(area.H))
		alpha := uint8(46 + i%4*11)
		_ = renderer.SetDrawColor(140, 195, 255, alpha)
		_ = renderer.DrawLine(x, y, x-5, y+17)
	}
}

func weatherSceneSnow(renderer *sdl.Renderer, area sdl.Rect, tick uint64) {
	fillRoundedRect(renderer, area.X, area.Y, area.W, area.H, 12,
		sdl.Color{R: 126, G: 183, B: 225, A: 20})
	for i := int32(0); i < 50; i++ {
		x := area.X + (i*211+57+int32(tick/90))%area.W
		y := area.Y + int32((uint64(i*97)+tick/28)%uint64(area.H))
		radius := int32(1 + i%3)
		fillCircle(renderer, x, y, radius, sdl.Color{R: 218, G: 240, B: 255, A: 78})
	}
}
