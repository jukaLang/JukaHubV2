package main

import (
	"fmt"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Quick Settings Panel — overlay with system toggles
// ──────────────────────────────────────────────────────────────────────

type qsToggle struct {
	Label    string
	Icon     string
	Value    bool
	OnColor  sdl.Color
	OffColor sdl.Color
}

type qsSlider struct {
	Label string
	Icon  string
	Value float64 // 0..1
	Min   float64
	Max   float64
}

type qsState struct {
	open    bool
	cursor  int
	toggles []qsToggle
	sliders []qsSlider
}

var qs qsState

func qsInit() {
	qs = qsState{
		toggles: []qsToggle{
			{Label: "Dark Mode", Icon: "[D]", Value: true,
				OnColor:  sdl.Color{R: 80, G: 160, B: 255, A: 255},
				OffColor: sdl.Color{R: 60, G: 65, B: 80, A: 200}},
			{Label: "Low Power", Icon: "[=]", Value: false,
				OnColor:  sdl.Color{R: 80, G: 200, B: 120, A: 255},
				OffColor: sdl.Color{R: 60, G: 65, B: 80, A: 200}},
			{Label: "Reduced Motion", Icon: "[M]", Value: false,
				OnColor:  sdl.Color{R: 200, G: 160, B: 80, A: 255},
				OffColor: sdl.Color{R: 60, G: 65, B: 80, A: 200}},
			{Label: "Fullscreen", Icon: "[F]", Value: false,
				OnColor:  sdl.Color{R: 180, G: 100, B: 255, A: 255},
				OffColor: sdl.Color{R: 60, G: 65, B: 80, A: 200}},
			{Label: "Weather", Icon: "[W]", Value: true,
				OnColor:  sdl.Color{R: 60, G: 200, B: 220, A: 255},
				OffColor: sdl.Color{R: 60, G: 65, B: 80, A: 200}},
		},
		sliders: []qsSlider{
			{Label: "Volume", Icon: "[V]", Value: 0.8, Min: 0, Max: 1},
			{Label: "Brightness", Icon: "[B]", Value: 1.0, Min: 0.2, Max: 1.0},
		},
	}
}

func qsSyncFromConfig(config *Config) {
	if len(qs.toggles) >= 5 {
		qs.toggles[0].Value = !config.Variables.ReducedMotion // Dark mode = not reduced motion
		qs.toggles[1].Value = config.Variables.LowPower
		qs.toggles[2].Value = config.Variables.ReducedMotion
		qs.toggles[3].Value = config.Variables.Fullscreen
		qs.toggles[4].Value = config.Variables.WeatherEnabled
	}
}

func qsApplyToConfig(config *Config) {
	if len(qs.toggles) >= 5 {
		config.Variables.ReducedMotion = qs.toggles[2].Value
		config.Variables.LowPower = qs.toggles[1].Value
		config.Variables.Fullscreen = qs.toggles[3].Value
		config.Variables.WeatherEnabled = qs.toggles[4].Value

		// Apply fullscreen immediately.
		if mainWindow != nil {
			if config.Variables.Fullscreen {
				mainWindow.SetFullscreen(sdl.WINDOW_FULLSCREEN_DESKTOP)
			} else {
				mainWindow.SetFullscreen(0)
			}
		}
	}
	syncVariableOverrides(config)
}

func qsToggleOpen() {
	qs.open = !qs.open
	if qs.open {
		qs.cursor = 0
		if appConfig != nil {
			qsSyncFromConfig(appConfig)
		}
	}
}

func qsNavigate(dr int) {
	total := len(qs.toggles) + len(qs.sliders)
	qs.cursor += dr
	if qs.cursor < 0 {
		qs.cursor = total - 1
	}
	if qs.cursor >= total {
		qs.cursor = 0
	}
	PlayNavSound()
}

func qsSelect() {
	if qs.cursor < len(qs.toggles) {
		qs.toggles[qs.cursor].Value = !qs.toggles[qs.cursor].Value
		if appConfig != nil {
			qsApplyToConfig(appConfig)
		}
		PlayToggleSound()
	} else {
		// Slider: increase by 10%.
		idx := qs.cursor - len(qs.toggles)
		if idx >= 0 && idx < len(qs.sliders) {
			s := &qs.sliders[idx]
			s.Value += 0.1
			if s.Value > s.Max {
				s.Value = s.Min
			}
			PlayNavSound()
		}
	}
}

func qsNavigateSlider(dr int) {
	if qs.cursor >= len(qs.toggles) {
		idx := qs.cursor - len(qs.toggles)
		if idx >= 0 && idx < len(qs.sliders) {
			s := &qs.sliders[idx]
			s.Value += float64(dr) * 0.05
			if s.Value < s.Min {
				s.Value = s.Min
			}
			if s.Value > s.Max {
				s.Value = s.Max
			}
			PlayNavSound()
		}
	}
}

func renderQuickSettings(renderer *sdl.Renderer, config *Config) {
	if !qs.open {
		return
	}

	font, _ := getCachedFont(config, "small")
	medFont, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}

	// Dark backdrop.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 160)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	// Panel card.
	panelW := int32(380)
	panelH := int32(500)
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20,
		sdl.Color{R: 14, G: 18, B: 28, A: 248})
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorder)

	// Title.
	if medFont != nil {
		title := "Quick Settings"
		tw, _, _ := medFont.SizeUTF8(title)
		renderText(renderer, config, medFont, title,
			sdl.Color{R: 220, G: 230, B: 245, A: 240},
			panelX+(panelW-int32(tw))/2, panelY+16)
	}

	// Items.
	itemY := panelY + 56
	itemH := int32(44)
	itemX := panelX + 16
	itemW := panelW - 32

	// Toggles.
	for i, t := range qs.toggles {
		iy := itemY + int32(i)*itemH
		focused := qs.cursor == i

		if focused {
			fillRoundedRect(renderer, itemX, iy, itemW, itemH-4, 10,
				sdl.Color{R: 30, G: 36, B: 50, A: 200})
		}

		// Icon.
		renderText(renderer, config, font, t.Icon,
			sdl.Color{R: 200, G: 210, B: 230, A: 220}, itemX+8, iy+10)

		// Label.
		renderText(renderer, config, font, t.Label,
			sdl.Color{R: 180, G: 190, B: 210, A: 220}, itemX+36, iy+10)

		// Toggle switch.
		swW := int32(44)
		swH := int32(24)
		swX := itemX + itemW - swW - 8
		swY := iy + 8

		swFill := t.OffColor
		if t.Value {
			swFill = t.OnColor
		}
		fillRoundedRect(renderer, swX, swY, swW, swH, swH/2, swFill)

		// Knob.
		knobR := int32(8)
		knobX := swX + knobR + 4
		if t.Value {
			knobX = swX + swW - knobR - 4
		}
		knobY := swY + swH/2
		fillCircle(renderer, knobX, knobY, knobR, sdl.Color{R: 255, G: 255, B: 255, A: 240})
	}

	// Sliders.
	sliderStart := itemY + int32(len(qs.toggles))*itemH + 8
	for i, s := range qs.sliders {
		si := len(qs.toggles) + i
		sy := sliderStart + int32(i)*itemH
		focused := qs.cursor == si

		if focused {
			fillRoundedRect(renderer, itemX, sy, itemW, itemH-4, 10,
				sdl.Color{R: 30, G: 36, B: 50, A: 200})
		}

		// Icon + Label.
		renderText(renderer, config, font, s.Icon,
			sdl.Color{R: 200, G: 210, B: 230, A: 220}, itemX+8, sy+6)
		renderText(renderer, config, font, s.Label,
			sdl.Color{R: 180, G: 190, B: 210, A: 220}, itemX+36, sy+6)

		// Value text.
		valStr := fmt.Sprintf("%d%%", int(s.Value*100))
		vw, _, _ := font.SizeUTF8(valStr)
		renderText(renderer, config, font, valStr,
			sdl.Color{R: 140, G: 155, B: 180, A: 200},
			itemX+itemW-int32(vw)-8, sy+6)

		// Slider track.
		trackW := itemW - 120
		trackX := itemX + 100
		trackY := sy + 28
		trackH := int32(6)
		fillRoundedRect(renderer, trackX, trackY, trackW, trackH, 3,
			sdl.Color{R: 30, G: 36, B: 50, A: 200})

		// Filled portion.
		frac := (s.Value - s.Min) / (s.Max - s.Min)
		fillW := int32(float64(trackW) * frac)
		if fillW > 0 {
			fillRoundedRect(renderer, trackX, trackY, fillW, trackH, 3,
				sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 200})
		}

		// Knob.
		knobX := trackX + fillW
		knobR := int32(6)
		fillCircle(renderer, knobX, trackY+trackH/2, knobR,
			sdl.Color{R: 240, G: 245, B: 255, A: 240})
	}

	// System info at bottom.
	infoY := panelY + panelH - 60
	if font != nil {
		// Battery.
		bat := getBatteryPercent()
		if bat >= 0 {
			batStr := fmt.Sprintf("[=] %d%%", bat)
			renderText(renderer, config, font, batStr,
				sdl.Color{R: 140, G: 155, B: 180, A: 200}, panelX+20, infoY)
		}

		// Network.
		netStr := "OK Online"
		if !IsOnline() {
			netStr = "[!] Offline"
		}
		nw, _, _ := font.SizeUTF8(netStr)
		renderText(renderer, config, font, netStr,
			sdl.Color{R: 140, G: 155, B: 180, A: 200},
			panelX+panelW-int32(nw)-20, infoY)
	}
}

func handleQSInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN || !qs.open {
		return
	}

	switch e.Keysym.Sym {
	case sdl.K_UP:
		qsNavigate(-1)
	case sdl.K_DOWN:
		qsNavigate(1)
	case sdl.K_LEFT:
		qsNavigateSlider(-1)
	case sdl.K_RIGHT:
		qsNavigateSlider(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		qsSelect()
	case sdl.K_ESCAPE, sdl.K_b:
		qs.open = false
		PlayBackSound()
	}
}
