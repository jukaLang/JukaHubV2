package main

// browserzoom.go — text zoom for the reader.
//
// The handheld screens are small and the a11y text-size setting is global: a
// reader who only wants the current web page bigger should not have to grow
// the whole launcher to get there. This is a page-scoped zoom on top of that —
// it scales the body size the styled page is measured with, so the wrap, the
// row height and every heading scale together, exactly like a browser's own
// zoom. It persists across pages and sessions the way a reader expects.
//
// Zoom is stepped rather than continuous: a D-pad has no fine control, steps
// make "back to normal" a real stop at 100%, and the discrete sizes keep the
// layout cache honest (one measured layout per step, not one per pixel).
//
// The scale is applied in webDocument's layout (computeLayout), which every
// styled page goes through; the default step is 1.0 so nothing else that draws
// a web document changes until a reader actually zooms.

import (
	"math"
	"strconv"
	"strings"
)

// browserZoomKey is where the current scale is stored in the user config.
const browserZoomKey = "browser_zoom"

// browserZoomSteps are the zoom levels, as a multiplier on the body size. The
// middle step is 1.0 (normal); the ends are the useful limits on a 5-inch
// panel before a line fits only a word or the text stops being legible.
var browserZoomSteps = []float64{0.75, 0.85, 1.0, 1.15, 1.35, 1.6}

// browserZoomDefault is the index of 1.0 — the page as the site drew it.
const browserZoomDefault = 2

// browserZoomIndex is the current step. It is package state the same way
// browserNav is: one browser at a time, mutated on the render/event thread.
var browserZoomIndex = browserZoomDefault

// browserZoomReset returns to 100%. It runs on a test reset so a zoom set by
// one test cannot leak into the layout another test measures.
func browserZoomReset() { browserZoomIndex = browserZoomDefault }

// browserZoomScale is the multiplier for the current step, clamped so a stale
// index can never size a font out of range.
func browserZoomScale() float64 {
	i := browserZoomIndex
	if i < 0 {
		i = 0
	}
	if i >= len(browserZoomSteps) {
		i = len(browserZoomSteps) - 1
	}
	return browserZoomSteps[i]
}

// browserZoomLabel is the step as the reader sees it ("115%").
func browserZoomLabel() string {
	return strconv.Itoa(int(math.Round(browserZoomScale()*100))) + "%"
}

// browserZoomAtEnds reports that a step in dir had nowhere to go, so the caller
// can say so instead of silently doing nothing.
func browserZoomAtEnd(dir int) bool {
	if dir > 0 {
		return browserZoomIndex >= len(browserZoomSteps)-1
	}
	return browserZoomIndex <= 0
}

// browserZoomStep moves one step in dir and re-clamps the reading position to
// the newly measured page. It always reports handled: zoom is a dedicated
// control, and a press at an end still gets an honest "that's as far as it
// goes" rather than falling through to the scene.
func browserZoomStep(config *Config, elem Element, dir int) bool {
	if browserZoomAtEnd(dir) {
		if dir > 0 {
			ShowToast("Zoom is as large as it goes ("+browserZoomLabel()+")", ToastKindInfo)
		} else {
			ShowToast("Zoom is as small as it goes ("+browserZoomLabel()+")", ToastKindInfo)
		}
		return true
	}
	browserZoomIndex += dir
	browserZoomApplyChange(config, elem)
	return true
}

// browserZoomCycle steps the scale up one and wraps to the smallest at the top.
// It is what the toolbar chip's tap calls: unlike a key press, a tap should
// always do something visible, so running off the largest size loops back to
// the smallest instead of stalling with a toast.
func browserZoomCycle(config *Config, elem Element) {
	if browserZoomIndex >= len(browserZoomSteps)-1 {
		browserZoomIndex = 0
	} else {
		browserZoomIndex++
	}
	browserZoomApplyChange(config, elem)
}

// browserZoomApplyChange re-clamps the reading position to the newly measured
// page, announces the new size and persists it. It runs after the step index
// has moved, however it moved.
func browserZoomApplyChange(config *Config, elem Element) {
	// The page is re-measured on the next frame at the new size; keep the
	// reader near where they were instead of snapping to the top.
	if layout := browserPageLayout(config, elem); layout != nil {
		if max := browserScrollMax(elem, layout); textBrowserScrollY > max {
			textBrowserScrollY = max
		}
	}
	if textBrowserScrollY < 0 {
		textBrowserScrollY = 0
	}
	textBrowserScrollVelocity = 0
	textBrowserScrollCooldown = 0
	ShowToast("Zoom "+browserZoomLabel(), ToastKindInfo)
	PlayNavSound()
	// Persisted so the size a reader chose is waiting for them next launch;
	// debounced because a D-pad held on the zoom key would otherwise write the
	// config once per step to the SD card.
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// browserZoomNearestStep maps an arbitrary stored scale onto the closest real
// step, so a hand-edited config or a step list that changed between releases
// still lands on a usable size instead of an error.
func browserZoomNearestStep(scale float64) int {
	if math.IsNaN(scale) || scale <= 0 {
		return browserZoomDefault
	}
	best, bestIdx := math.Abs(browserZoomSteps[0]-scale), 0
	for i, step := range browserZoomSteps {
		if d := math.Abs(step - scale); d < best {
			best, bestIdx = d, i
		}
	}
	return bestIdx
}

// browserZoomLoad reads the persisted scale. It accepts a float64 (JSON) or a
// string (XML / hand-edited), and an absent or nonsense value means "normal".
func browserZoomLoad(config *Config) {
	if config == nil || config.Variables.Custom == nil {
		browserZoomIndex = browserZoomDefault
		return
	}
	if f, ok := config.Variables.Custom[browserZoomKey].(float64); ok {
		browserZoomIndex = browserZoomNearestStep(f)
		return
	}
	if s, ok := customString(config.Variables.Custom, browserZoomKey); ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			browserZoomIndex = browserZoomNearestStep(f)
			return
		}
	}
	browserZoomIndex = browserZoomDefault
}

// browserZoomStore writes the current scale into the config map only.
func browserZoomStore(config *Config) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[browserZoomKey] = browserZoomScale()
}

// browserZoomSave persists through the same user-config writer the Settings
// scenes use, so a zoom survives a relaunch without losing anything else.
func browserZoomSave(config *Config) {
	browserZoomStore(config)
	if config == nil {
		return
	}
	saveUserConfigDebounced(userConfigFromVariables(config))
}
