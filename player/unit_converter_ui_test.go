package main

// The Unit Converter draws its category row, its two unit boxes and both swap
// controls as buttons, but only the text field ever had a hit area: on the
// handheld's touchscreen a tap on "Temp" did nothing, the "<->" circle between
// the boxes did nothing, and the "Swap" button below the result was pure
// decoration. These checks pin the tap routing and the geometry the taps are
// tested against, so a re-layout cannot quietly orphan a control again.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// renderConverterPage draws the converter scene on a canvas of the given size
// and returns the config plus the element that was drawn.
func renderConverterPage(t *testing.T, w, h int32) (*Config, Element, int32, int32) {
	t.Helper()
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	idx := findSceneIndex(config, "UnitConverter")
	if idx < 0 {
		t.Fatal("no UnitConverter scene in the shipped config")
	}
	var elem Element
	for _, e := range config.Scenes[idx].Elements {
		if e.Type == "unitconverter" {
			elem = e
			break
		}
	}
	if elem.Type == "" {
		t.Fatal("no unitconverter element in the UnitConverter scene")
	}

	withScreen(t, w, h)
	renderer := newKeyboardTestRenderer(t)
	prevScene := currentSceneIndex
	currentSceneIndex = idx
	t.Cleanup(func() { currentSceneIndex = prevScene })

	// The element's absolute position is what the hit rects are built from.
	clearUnitState(t)
	renderUnitConverter(renderer, config, elem)
	return config, elem, getElementWidth(elem, 1080), getElementHeight(elem, 500)
}

// clearUnitState puts the converter back to its opening state and restores it
// afterwards, so the shared package globals do not leak between tests.
func clearUnitState(t *testing.T) {
	t.Helper()
	prevCat, prevFrom, prevTo := unitCategory, unitFrom, unitTo
	prevIn, prevResult := unitInputValue, unitResult
	unitCategory, unitFrom, unitTo, unitInputValue, unitResult = "length", "", "", "", ""
	t.Cleanup(func() {
		unitCategory, unitFrom, unitTo = prevCat, prevFrom, prevTo
		unitInputValue, unitResult = prevIn, prevResult
	})
}

func centre(r sdl.Rect) (int32, int32) {
	return r.X + r.W/2, r.Y + r.H/2
}

// TestUnitConverterTabsAreTapTargets walks every drawn chip and checks a tap on
// it selects that category, and that the chip row still fits inside the panel
// on the compact canvas.
func TestUnitConverterTabsAreTapTargets(t *testing.T) {
	for _, size := range []struct {
		name string
		w, h int32
	}{
		{"SmartPro 1280x720", uiCanvasWideWidth, uiCanvasWideHeight},
		{"Brick 960x720", uiCanvasCompactW, uiCanvasCompactH},
	} {
		t.Run(size.name, func(t *testing.T) {
			config, elem, elemW, elemH := renderConverterPage(t, size.w, size.h)
			if len(unitCategoryRects) != len(unitCategories) {
				t.Fatalf("%d chips drawn for %d categories", len(unitCategoryRects), len(unitCategories))
			}
			right := elem.X + elemW
			bottom := elem.Y + elemH
			for i, r := range unitCategoryRects {
				if r.X < elem.X || r.X+r.W > right || r.Y < elem.Y || r.Y+r.H > bottom {
					t.Errorf("chip %q %+v leaves the panel %d,%d %dx%d", unitCategories[i], r, elem.X, elem.Y, elemW, elemH)
				}
				mx, my := centre(r)
				unitCategory = "length"
				if !handleUnitConverterMouse(mx, my, config) {
					t.Errorf("tap on %q was not claimed", unitCategories[i])
					continue
				}
				if unitCategory != unitCategories[i] {
					t.Errorf("tap on %q selected %q", unitCategories[i], unitCategory)
				}
			}
		})
	}
}

// TestUnitConverterSwapControlsWork covers both swap affordances: the round
// "<->" button between the boxes and the "Swap" button under the result.
func TestUnitConverterSwapControlsWork(t *testing.T) {
	config, _, _, _ := renderConverterPage(t, uiCanvasWideWidth, uiCanvasWideHeight)
	if unitFrom == "" || unitTo == "" {
		t.Fatalf("converter has no starting unit pair: %q -> %q", unitFrom, unitTo)
	}
	if unitSwapCircle.W <= 0 || unitSwapButton.W <= 0 {
		t.Fatal("a swap control was not drawn")
	}

	for _, c := range []struct {
		name string
		rect sdl.Rect
	}{
		{"circle", unitSwapCircle},
		{"button", unitSwapButton},
	} {
		unitFrom, unitTo = "m", "ft"
		unitResult = "3.28"
		mx, my := centre(c.rect)
		if !handleUnitConverterMouse(mx, my, config) {
			t.Errorf("%s swap tap was not claimed", c.name)
			continue
		}
		if unitFrom != "ft" || unitTo != "m" {
			t.Errorf("%s swap left %q -> %q", c.name, unitFrom, unitTo)
		}
		if unitResult != "" {
			t.Errorf("%s swap kept the stale result %q", c.name, unitResult)
		}
	}
}

// TestUnitConverterUnitBoxStepsUnit checks the boxes name the unit and change
// it when tapped, wrapping at the end of the category instead of dead-ending.
func TestUnitConverterUnitBoxStepsUnit(t *testing.T) {
	config, _, _, _ := renderConverterPage(t, uiCanvasWideWidth, uiCanvasWideHeight)
	if unitFromRect.W <= 0 || unitToRect.W <= 0 {
		t.Fatal("the unit boxes were not drawn")
	}
	units := getUnitsForCategory(unitCategory)
	if len(units) < 2 {
		// Stepping is the behaviour under test; a one-unit category cannot
		// show it. That is a fixture problem (the default category changed),
		// and a skip here would leave the wrap unchecked.
		t.Fatalf("category %q has %d units; the step test needs one with at least two", unitCategory, len(units))
	}

	unitFrom = units[0]
	mx, my := centre(unitFromRect)
	if !handleUnitConverterMouse(mx, my, config) {
		t.Fatal("tap on the from box was not claimed")
	}
	if unitFrom != units[1] {
		t.Errorf("from box stepped to %q, want %q", unitFrom, units[1])
	}

	unitTo = units[len(units)-1]
	mx, my = centre(unitToRect)
	if !handleUnitConverterMouse(mx, my, config) {
		t.Fatal("tap on the to box was not claimed")
	}
	if unitTo != units[0] {
		t.Errorf("to box wrapped to %q, want %q", unitTo, units[0])
	}
}

// TestUnitConverterIgnoresTapsOnOtherScenes guards the global rects: the
// converter's controls must not eat a click while another page is open.
func TestUnitConverterIgnoresTapsOnOtherScenes(t *testing.T) {
	config, _, _, _ := renderConverterPage(t, uiCanvasWideWidth, uiCanvasWideHeight)
	home := findSceneIndex(config, "Main")
	if home < 0 {
		t.Fatal("the shipped config has a Main scene; it went missing, so the cross-scene guard cannot run")
	}
	prev := currentSceneIndex
	currentSceneIndex = home
	t.Cleanup(func() { currentSceneIndex = prev })

	mx, my := centre(unitSwapButton)
	before := unitFrom
	if handleUnitConverterMouse(mx, my, config) {
		t.Fatal("the converter claimed a click while another scene was open")
	}
	if unitFrom != before {
		t.Fatalf("units changed behind another scene: %q", unitFrom)
	}
}
