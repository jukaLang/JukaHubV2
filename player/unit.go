package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// --- Unit Converter ---

var unitConversionTable = map[string]map[string]float64{
	"length": {
		"mm": 0.001, "cm": 0.01, "m": 1, "km": 1000,
		"inch": 0.0254, "foot": 0.3048, "yard": 0.9144, "mile": 1609.344,
	},
	"weight": {
		"mg": 0.001, "g": 1, "kg": 1000, "lb": 453.592, "oz": 28.3495,
	},
	"volume": {
		"ml": 0.001, "L": 1, "gallon": 3.78541, "quart": 0.946353,
		"pint": 0.473176, "cup": 0.236588, "floz": 0.0295735,
	},
	"speed": {
		"m/s": 1, "km/h": 0.277778, "mph": 0.44704, "knot": 0.514444,
	},
	"time": {
		"ms": 0.001, "s": 1, "min": 60, "hour": 3600, "day": 86400,
		"week": 604800, "month": 2592000, "year": 31536000,
	},
	"data": {
		"bit": 0.125, "byte": 1, "KB": 1024, "MB": 1048576,
		"GB": 1073741824, "TB": 1099511627776,
	},
	"area": {
		"mm2": 0.000001, "cm2": 0.0001, "m2": 1, "km2": 1000000,
		"inch2": 0.00064516, "foot2": 0.092903, "acre": 4046.86, "hectare": 10000,
	},
}

func convertTemperature(value float64, from, to string) (float64, bool) {
	var c float64
	switch strings.ToLower(from) {
	case "c":
		c = value
	case "f":
		c = (value - 32) * 5 / 9
	case "k":
		c = value - 273.15
	default:
		return 0, false
	}
	switch strings.ToLower(to) {
	case "c":
		return c, true
	case "f":
		return c*9/5 + 32, true
	case "k":
		return c + 273.15, true
	default:
		return 0, false
	}
}

func convertUnit(value float64, from, to, category string) (float64, bool) {
	if category == "temperature" {
		return convertTemperature(value, from, to)
	}
	table, ok := unitConversionTable[category]
	if !ok {
		return 0, false
	}
	fromFactor, ok1 := table[from]
	toFactor, ok2 := table[to]
	if !ok1 || !ok2 {
		return 0, false
	}
	base := value * fromFactor
	result := base / toFactor
	return result, true
}

func getUnitsForCategory(category string) []string {
	if category == "temperature" {
		return []string{"C", "F", "K"}
	}
	table, ok := unitConversionTable[category]
	if !ok {
		return []string{}
	}
	units := make([]string, 0, len(table))
	for u := range table {
		units = append(units, u)
	}
	return units
}

func renderUnitConverter(renderer *sdl.Renderer, config *Config, element Element) {
	titleFont, _ := getCachedFont(config, "medium")
	font, _ := getCachedFont(config, "small")
	if font == nil {
		font = titleFont
	}

	categories := []string{"length", "weight", "temperature", "volume", "speed", "time", "data", "area"}
	catLabels := map[string]string{
		"length": "Length", "weight": "Weight", "temperature": "Temp",
		"volume": "Volume", "speed": "Speed", "time": "Time", "data": "Data", "area": "Area",
	}

	// panel wrapper for consistency
	elemW := getElementWidth(element, 1080)
	elemH := getElementHeight(element, 500)
	unitInputRect = sdl.Rect{}
	drawPanel(renderer, element.X, element.Y, elemW, elemH, WithAlpha(ColorSurfacePanel, 220), accentColor)

	catStartX := element.X + 20
	catY := element.Y + 20
	catW := int32(120)
	catH := int32(34)
	catGap := int32(10)

	for i, cat := range categories {
		cx := catStartX + int32(i)*(catW+catGap)
		bg := ColorSurfaceRow
		if cat == unitCategory {
			bg = WithAlpha(accentColor, 90)
		}
		fillRoundedRect(renderer, cx+2, catY+2, catW, catH, 8, ShadowFill(30))
		fillRoundedRect(renderer, cx, catY, catW, catH, 8, bg)
		if cat == unitCategory {
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 220)
			renderer.FillRect(&sdl.Rect{X: cx + 8, Y: catY + catH - 2, W: catW - 16, H: 2})
		} else {
			renderer.SetDrawColor(ColorBorderSubtle.R, ColorBorderSubtle.G, ColorBorderSubtle.B, 40)
			renderer.DrawRect(&sdl.Rect{X: cx + 1, Y: catY + 1, W: catW - 2, H: 1})
			renderer.DrawRect(&sdl.Rect{X: cx + 1, Y: catY + catH - 2, W: catW - 2, H: 1})
			renderer.DrawRect(&sdl.Rect{X: cx + 1, Y: catY + 1, W: 1, H: catH - 2})
			renderer.DrawRect(&sdl.Rect{X: cx + catW - 2, Y: catY + 1, W: 1, H: catH - 2})
		}
		if font != nil {
			lw, _, _ := font.SizeUTF8(catLabels[cat])
			tc := ColorTextPrimary()
			if cat == unitCategory {
				tc = ColorTextPrimary()
			} else {
				tc = ColorTextTertiary()
			}
			renderText(renderer, config, font, catLabels[cat], tc, cx+(catW-int32(lw))/2, catY+8)
		}
	}

	units := getUnitsForCategory(unitCategory)
	if len(units) < 2 {
		units = []string{unitFrom, unitTo}
	}
	if unitFrom == "" {
		unitFrom = units[0]
	}
	if unitTo == "" {
		unitTo = units[len(units)-1]
	}

	rowY := catY + catH + 30
	rowH := int32(44)
	rowW := int32(200)
	gap := int32(20)

	fromX := element.X + 40
	toX := fromX + rowW + gap
	inputX := toX + rowW + gap
	inputW := int32(0)
	if w, err := strconv.Atoi(string(element.Width)); err == nil {
		inputW = int32(w) - 260
	}

	fillRoundedRect(renderer, fromX+1, rowY+1, rowW, rowH, 10, ShadowFill(30))
	fillRoundedRect(renderer, fromX, rowY, rowW, rowH, 10, ColorSurfaceRow)
	renderer.SetDrawColor(ColorBorderSubtle.R, ColorBorderSubtle.G, ColorBorderSubtle.B, 40)
	renderer.DrawRect(&sdl.Rect{X: fromX + 1, Y: rowY + 1, W: rowW - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: fromX + 1, Y: rowY + rowH - 2, W: rowW - 2, H: 1})
	fillRoundedRect(renderer, toX+1, rowY+1, rowW, rowH, 10, ShadowFill(30))
	fillRoundedRect(renderer, toX, rowY, rowW, rowH, 10, ColorSurfaceRow)
	renderer.SetDrawColor(ColorBorderSubtle.R, ColorBorderSubtle.G, ColorBorderSubtle.B, 40)
	renderer.DrawRect(&sdl.Rect{X: toX + 1, Y: rowY + 1, W: rowW - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: toX + 1, Y: rowY + rowH - 2, W: rowW - 2, H: 1})
	fillRoundedRect(renderer, inputX+1, rowY+1, inputW, rowH, 10, ShadowFill(30))
	fillRoundedRect(renderer, inputX, rowY, inputW, rowH, 10, ColorSurfaceRaised)
	unitInputRect = sdl.Rect{X: inputX, Y: rowY, W: inputW, H: rowH}
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 120)
	renderer.FillRect(&sdl.Rect{X: inputX + 8, Y: rowY + rowH - 2, W: inputW - 16, H: 2})

	if font != nil {
		fw, _, _ := font.SizeUTF8(unitFrom)
		renderText(renderer, config, font, unitFrom, ColorTextPrimary(), fromX+(rowW-int32(fw))/2, rowY+12)
		tw, _, _ := font.SizeUTF8(unitTo)
		renderText(renderer, config, font, unitTo, ColorTextPrimary(), toX+(rowW-int32(tw))/2, rowY+12)
		display := unitInputValue
		if display == "" {
			display = "Enter value..."
		}
		renderText(renderer, config, font, display, ColorTextSecondary(), inputX+16, rowY+12)
	}

	// swap indicator (chevron glyph in a circular button)
	arrowY := rowY + rowH/2
	circleR := int32(16)
	circleX := fromX + rowW + gap/2 - circleR
	fillRoundedRect(renderer, circleX+2, arrowY-circleR+2, circleR*2, circleR*2, circleR, ShadowFill(40))
	fillRoundedRect(renderer, circleX, arrowY-circleR, circleR*2, circleR*2, circleR, ColorButtonRaised)
	renderer.SetDrawColor(255, 255, 255, 30)
	renderer.FillRect(&sdl.Rect{X: circleX + 4, Y: arrowY - circleR + 3, W: circleR*2 - 8, H: 1})
	if font != nil {
		gw, gh, _ := font.SizeUTF8("<->")
		renderText(renderer, config, font, "<->", accentColor, circleX+(circleR*2-int32(gw))/2, arrowY-int32(gh)/2+1)
	}

	resultY := rowY + rowH + 40
	resultH := int32(60)
	fillRoundedRect(renderer, inputX, resultY, inputW, resultH, 12, ColorSurfaceCard)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 100)
	renderer.FillRect(&sdl.Rect{X: inputX, Y: resultY, W: inputW, H: 2})

	if font != nil {
		resultText := unitResult
		if resultText == "" {
			resultText = "Result will appear here"
		}
		rw, _, _ := font.SizeUTF8(resultText)
		renderText(renderer, config, font, resultText, ColorTextPrimary(), inputX+(inputW-int32(rw))/2, resultY+18)
	}

	swapY := resultY + resultH + 20
	swapW := int32(120)
	swapH := int32(40)
	swapX := inputX + inputW - swapW
	fillRoundedRect(renderer, swapX, swapY, swapW, swapH, 10, ColorButtonRaised)
	renderer.SetDrawColor(255, 255, 255, 30)
	renderer.FillRect(&sdl.Rect{X: swapX + 1, Y: swapY + 1, W: swapW - 2, H: 1})
	if font != nil {
		sw, _, _ := font.SizeUTF8("Swap")
		renderText(renderer, config, font, "Swap", ColorTextPrimary(), swapX+(swapW-int32(sw))/2, swapY+10)
	}
}

func handleUnitInput(e *sdl.KeyboardEvent, config *Config) {
	switch e.Keysym.Sym {
	case sdl.K_LEFT:
		units := getUnitsForCategory(unitCategory)
		for i, u := range units {
			if u == unitFrom && i > 0 {
				unitFrom = units[i-1]
				break
			}
		}
	case sdl.K_RIGHT:
		units := getUnitsForCategory(unitCategory)
		for i, u := range units {
			if u == unitFrom && i < len(units)-1 {
				unitFrom = units[i+1]
				break
			}
		}
	case sdl.K_UP:
		unitInputValue = ""
		unitResult = ""
	case sdl.K_DOWN:
		unitInputValue = ""
		unitResult = ""
	case sdl.K_RETURN:
		if unitInputValue != "" {
			var val float64
			if _, err := fmt.Sscanf(unitInputValue, "%f", &val); err == nil {
				if res, ok := convertUnit(val, unitFrom, unitTo, unitCategory); ok {
					if unitCategory == "temperature" {
						if unitTo == "C" || unitTo == "c" {
							unitResult = fmt.Sprintf("%.2f °C", res)
						} else if unitTo == "F" || unitTo == "f" {
							unitResult = fmt.Sprintf("%.2f °F", res)
						} else {
							unitResult = fmt.Sprintf("%.2f K", res)
						}
					} else {
						unitResult = fmt.Sprintf("%.4g", res)
					}
				} else {
					unitResult = "Error"
				}
			} else {
				unitResult = "Invalid number"
			}
		}
	case sdl.K_BACKSPACE:
		if len(unitInputValue) > 0 {
			unitInputValue = unitInputValue[:len(unitInputValue)-1]
		}
	default:
		if e.Type == sdl.KEYDOWN {
			ks := e.Keysym.Sym
			if (ks >= sdl.K_0 && ks <= sdl.K_9) || ks == sdl.K_PERIOD {
				unitInputValue += string(ks)
			}
		}
	}
}
