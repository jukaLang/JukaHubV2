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

func unitConverterInputWidth(width int) int32 {
	// The value field starts after the two 200 px unit rows and their gaps, at
	// element.X+480, and keeps a 20 px margin from the panel's right edge. The
	// old width-260 sized it as if it started at +260, so it ran 240 px past the
	// edge: over the panel border on the wide canvas and off the screen entirely
	// on the Brick's compact one.
	const fieldOffset = 500 // 480 + the 20 px right margin
	if width <= fieldOffset {
		return 0
	}
	if width >= fieldOffset+4000 {
		return 4000
	}
	return int32(width - fieldOffset)
}

// The converter draws its category row and both swap controls like buttons,
// so they keep the rects they were painted in and a tap can reach them. Before
// this they had no hit area: tapping a category did nothing, and the "Swap"
// button was purely decoration.
var (
	unitCategoryRects []sdl.Rect
	unitFromRect      sdl.Rect
	unitToRect        sdl.Rect
	unitSwapCircle    sdl.Rect
	unitSwapButton    sdl.Rect
)

// unitCategories is the chip row's order. It is the same slice renderUnitConverter
// draws in, so index i of unitCategoryRects always names unitCategories[i].
var unitCategories = []string{"length", "weight", "temperature", "volume", "speed", "time", "data", "area"}

// unitPickCategory selects a category and clears the working conversion, the
// same reset the unit_category action performs.
func unitPickCategory(cat string) bool {
	if cat == "" {
		return false
	}
	unitCategory = cat
	unitFrom = ""
	unitTo = ""
	unitInputValue = ""
	unitResult = ""
	return true
}

// unitSwapUnits mirrors the unit_swap action: the two sides trade places and
// the old result no longer describes the new pair.
func unitSwapUnits() {
	unitFrom, unitTo = unitTo, unitFrom
	unitResult = ""
}

// unitStepUnit advances one side of the conversion to the next unit in the
// category, wrapping at the end. Tapping a unit box used to do nothing; the
// box is the only place on the page that names the unit it would change.
func unitStepUnit(toSide bool) {
	units := getUnitsForCategory(unitCategory)
	if len(units) < 2 {
		return
	}
	cur := unitFrom
	if toSide {
		cur = unitTo
	}
	for i, u := range units {
		if u != cur {
			continue
		}
		next := units[(i+1)%len(units)]
		if toSide {
			unitTo = next
		} else {
			unitFrom = next
		}
		unitResult = ""
		return
	}
}

// handleUnitConverterMouse routes a tap on the converter's drawn controls. It
// only claims clicks while the converter is the open scene, so its rects can
// never swallow a click on the page behind it.
func handleUnitConverterMouse(mx, my int32, config *Config) bool {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) ||
		config.Scenes[currentSceneIndex].Name != "UnitConverter" {
		return false
	}
	for i, r := range unitCategoryRects {
		if mx < r.X || mx > r.X+r.W || my < r.Y || my > r.Y+r.H {
			continue
		}
		if i < len(unitCategories) {
			unitPickCategory(unitCategories[i])
		}
		PlayNavSound()
		return true
	}
	if pointInRect(mx, my, unitSwapCircle) || pointInRect(mx, my, unitSwapButton) {
		unitSwapUnits()
		PlayNavSound()
		return true
	}
	if pointInRect(mx, my, unitFromRect) {
		unitStepUnit(false)
		PlayNavSound()
		return true
	}
	if pointInRect(mx, my, unitToRect) {
		unitStepUnit(true)
		PlayNavSound()
		return true
	}
	return false
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

// unitCategoryOrder is the display order of each category's units. It is a
// slice, not a map iteration, because the order is user-visible twice: the
// renderer picks the first and last entry as the default from/to units when a
// category is selected, and the Left/Right keys walk the list. Go randomizes
// map iteration order per run, so deriving this from unitConversionTable made
// the default pair arbitrary and Left/Right jump unpredictably.
var unitCategoryOrder = map[string][]string{
	"length":      {"mm", "cm", "m", "km", "inch", "foot", "yard", "mile"},
	"weight":      {"mg", "g", "kg", "lb", "oz"},
	"temperature": {"C", "F", "K"},
	"volume":      {"ml", "L", "gallon", "quart", "pint", "cup", "floz"},
	"speed":       {"m/s", "km/h", "mph", "knot"},
	"time":        {"ms", "s", "min", "hour", "day", "week", "month", "year"},
	"data":        {"bit", "byte", "KB", "MB", "GB", "TB"},
	"area":        {"mm2", "cm2", "m2", "km2", "inch2", "foot2", "acre", "hectare"},
}

func getUnitsForCategory(category string) []string {
	if order, ok := unitCategoryOrder[category]; ok {
		return order
	}
	// Unknown category: fall back to the table's members so the converter still
	// shows something rather than nothing.
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

	categories := unitCategories
	catLabels := map[string]string{
		"length": "Length", "weight": "Weight", "temperature": "Temp",
		"volume": "Volume", "speed": "Speed", "time": "Time", "data": "Data", "area": "Area",
	}

	// panel wrapper for consistency
	elemW := getElementWidth(element, 1080)
	elemH := getElementHeight(element, 500)
	unitInputRect = sdl.Rect{}
	unitFromRect = sdl.Rect{}
	unitToRect = sdl.Rect{}
	unitSwapCircle = sdl.Rect{}
	unitSwapButton = sdl.Rect{}
	drawPanel(renderer, element.X, element.Y, elemW, elemH, WithAlpha(ColorSurfacePanel, 220), accentColor)

	catStartX := element.X + 20
	catY := element.Y + 20
	catH := int32(34)
	catGap := int32(10)
	// The eight category tabs share the panel's inner width instead of a fixed
	// 120 px each: at 120 they needed 1040 px, so on the Brick's compact canvas
	// (770 inside the panel) the last tabs were drawn over the border and off
	// the screen.
	catW := int32(120)
	if n := int32(len(categories)); n > 0 {
		if fit := (elemW - 40 - (n-1)*catGap) / n; fit < catW {
			catW = fit
		}
	}
	if catW < 48 {
		catW = 48
	}

	unitCategoryRects = unitCategoryRects[:0]
	for i, cat := range categories {
		cx := catStartX + int32(i)*(catW+catGap)
		if cx < catStartX {
			break
		}
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
			// The four 1px DrawRect edges drew a square outline over the
			// rounded fill, so an unselected tab read as a different shape from
			// the selected one. The shared rounding keeps them a single chip.
			strokeRoundedRect(renderer, cx, catY, catW, catH, 8, 1, WithAlpha(ColorBorderSubtle, 60))
		}
		// Every drawn tab is a tap target; the row used to be painted with no
		// hit area at all, so tapping a category did nothing.
		unitCategoryRects = append(unitCategoryRects, sdl.Rect{X: cx, Y: catY, W: catW, H: catH})
		if font != nil {
			label := ellipsize(font, catLabels[cat], catW-12)
			lw, _, _ := font.SizeUTF8(label)
			var tc sdl.Color
			if cat == unitCategory {
				tc = ColorTextPrimary()
			} else {
				tc = ColorTextTertiary()
			}
			renderText(renderer, config, font, label, tc, cx+(catW-int32(lw))/2, catY+8)
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
	inputX := toX + int32(int(rowW)+int(gap))
	inputW := int32(0)
	if w, err := strconv.Atoi(string(element.Width)); err == nil {
		inputW = unitConverterInputWidth(w)
	}

	// The unit boxes keep the card's rounding: two straight 1px lines drawn
	// across a 10px radius poked out past the curved corners and read as a
	// stray rule floating above each box.
	fillRoundedRect(renderer, fromX+1, rowY+1, rowW, rowH, 10, ShadowFill(30))
	fillRoundedRect(renderer, fromX, rowY, rowW, rowH, 10, ColorSurfaceRow)
	strokeRoundedRect(renderer, fromX, rowY, rowW, rowH, 10, 1, WithAlpha(ColorBorderSubtle, 60))
	fillRoundedRect(renderer, toX+1, rowY+1, rowW, rowH, 10, ShadowFill(30))
	fillRoundedRect(renderer, toX, rowY, rowW, rowH, 10, ColorSurfaceRow)
	strokeRoundedRect(renderer, toX, rowY, rowW, rowH, 10, 1, WithAlpha(ColorBorderSubtle, 60))
	unitFromRect = sdl.Rect{X: fromX, Y: rowY, W: rowW, H: rowH}
	unitToRect = sdl.Rect{X: toX, Y: rowY, W: rowW, H: rowH}
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
	unitSwapCircle = sdl.Rect{X: circleX, Y: arrowY - circleR, W: circleR * 2, H: circleR * 2}
	UIButton(renderer, config, sdl.Rect{X: circleX, Y: arrowY - circleR, W: circleR * 2, H: circleR * 2}, "<->", UIButtonStyle{
		Variant:    UIButtonSecondary,
		LabelColor: accentColor,
		Font:       font,
		Pill:       true,
	}, UIButtonState{})

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
	unitSwapButton = sdl.Rect{X: swapX, Y: swapY, W: swapW, H: swapH}
	UIButton(renderer, config, sdl.Rect{X: swapX, Y: swapY, W: swapW, H: swapH}, "Swap", UIButtonStyle{
		Variant: UIButtonSecondary,
		Font:    font,
		Radius:  RadiusMD,
	}, UIButtonState{})
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
		unitInputValue = trimLastRune(unitInputValue)
	default:
		if e.Type == sdl.KEYDOWN {
			ks := e.Keysym.Sym
			if (ks >= sdl.K_0 && ks <= sdl.K_9) || ks == sdl.K_PERIOD {
				unitInputValue += string(ks)
			}
		}
	}
}
