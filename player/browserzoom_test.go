package main

// browserzoom_test.go — the reader's text zoom.
//
// Zoom is only worth having if it really changes what is drawn (not just a
// number in a variable), survives a relaunch, and cannot walk off its own step
// list. These three pin exactly that.

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func resetZoomForTest() {
	browserZoomReset()
}

func TestBrowserZoomScaleStepsAndClamps(t *testing.T) {
	resetZoomForTest()
	t.Cleanup(browserZoomReset)

	if got := browserZoomScale(); got != 1.0 {
		t.Fatalf("default scale = %v, want 1.0", got)
	}
	if got := browserZoomLabel(); got != "100%" {
		t.Fatalf("default label = %q, want 100%%", got)
	}

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	elem := browserCursorTestElement("zoom_step")

	browserZoomIndex = browserZoomDefault
	if !browserZoomStep(config, elem, 1) {
		t.Fatal("zoom-in was not handled")
	}
	if got := browserZoomLabel(); got != "115%" {
		t.Fatalf("one step in = %q, want 115%%", got)
	}
	if !browserZoomStep(config, elem, -1) || !browserZoomStep(config, elem, -1) {
		t.Fatal("zoom-out was not handled")
	}
	if got := browserZoomLabel(); got != "85%" {
		t.Fatalf("two steps out = %q, want 85%%", got)
	}

	// A step past either end is still handled (the reader gets a toast) but
	// the index does not move off the list.
	browserZoomIndex = 0
	browserZoomStep(config, elem, -1)
	if browserZoomIndex != 0 {
		t.Fatalf("zoom-out walked past the smallest step to %d", browserZoomIndex)
	}
	browserZoomIndex = len(browserZoomSteps) - 1
	browserZoomStep(config, elem, 1)
	if browserZoomIndex != len(browserZoomSteps)-1 {
		t.Fatalf("zoom-in walked past the largest step to %d", browserZoomIndex)
	}
}

func TestBrowserZoomPersistsAndReloads(t *testing.T) {
	resetZoomForTest()
	t.Cleanup(browserZoomReset)

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	browserZoomIndex = browserZoomDefault + 2
	browserZoomStore(config)
	want := browserZoomSteps[browserZoomDefault+2]
	if got, ok := config.Variables.Custom[browserZoomKey].(float64); !ok || got != want {
		t.Fatalf("stored zoom = %v (%v), want %v", got, ok, want)
	}

	// A fresh launch reads the same step back.
	browserZoomIndex = 0
	browserZoomLoad(config)
	if browserZoomIndex != browserZoomDefault+2 {
		t.Fatalf("reloaded step = %d, want %d", browserZoomIndex, browserZoomDefault+2)
	}

	// A hand-edited out-of-range value lands on the nearest real step rather
	// than erroring or sizing a font off the list.
	browserZoomLoad(&Config{Variables: Variables{Custom: map[string]interface{}{browserZoomKey: 99.0}}})
	if browserZoomIndex != len(browserZoomSteps)-1 {
		t.Fatalf("huge scale clamped to step %d, want the top step", browserZoomIndex)
	}
	// A missing or nonsense value means "normal".
	browserZoomLoad(&Config{Variables: Variables{Custom: map[string]interface{}{}}})
	if browserZoomIndex != browserZoomDefault {
		t.Fatalf("absent zoom = %d, want the default", browserZoomIndex)
	}
}

// The whole point is that the page on screen actually gets bigger: a zoomed
// layout must measure taller rows and fit fewer of them than at 100%.
func TestBrowserZoomScalesTheStyledLayout(t *testing.T) {
	resetZoomForTest()
	t.Cleanup(browserZoomReset)

	config := layoutTestConfig(t)
	variable := "browser_zoom_layout"
	doc := parseWebPage("<body>"+strings.Repeat("<p>a line of page text to wrap and measure</p>", 40)+"</body>", "", "https://example.com/zoom")
	setBrowserDocument(variable, doc)
	t.Cleanup(func() { setBrowserDocument(variable, nil) })

	elem := browserCursorTestElement(variable)

	browserZoomIndex = browserZoomDefault // 100%
	normal := browserPageLayout(config, elem)
	if normal == nil || normal.RowH <= 0 {
		t.Fatalf("normal layout = %+v", normal)
	}

	browserZoomIndex = browserZoomDefault + 2 // 135%
	zoomed := browserPageLayout(config, elem)
	if zoomed == nil || zoomed.RowH <= 0 {
		t.Fatalf("zoomed layout = %+v", zoomed)
	}
	if zoomed.RowH <= normal.RowH {
		t.Fatalf("zoom-in did not grow the rows: normal %d, zoomed %d", normal.RowH, zoomed.RowH)
	}
	if browserVisibleRows(elem, zoomed) >= browserVisibleRows(elem, normal) {
		t.Fatal("zoom-in did not reduce the number of rows that fit")
	}
}

// The toolbar chip cycles the scale and wraps, so a tap always does something.
func TestBrowserZoomCycleWraps(t *testing.T) {
	resetZoomForTest()
	t.Cleanup(browserZoomReset)
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	elem := browserCursorTestElement("zoom_cycle")

	// Walk up to the largest step, then one more cycle wraps to the smallest.
	browserZoomIndex = 0
	for i := 0; i < len(browserZoomSteps)-1; i++ {
		browserZoomCycle(config, elem)
	}
	if browserZoomIndex != len(browserZoomSteps)-1 {
		t.Fatalf("after %d cycles the index = %d, want the largest step", len(browserZoomSteps)-1, browserZoomIndex)
	}
	browserZoomCycle(config, elem)
	if browserZoomIndex != 0 {
		t.Fatalf("a cycle past the largest step = %d, want a wrap to 0", browserZoomIndex)
	}
}

// The zoom chip is painted on the toolbar, not only tracked: scaling the text
// must change the pixels inside the chip's own rect, or the control a touch
// user taps would not visibly respond.
func TestBrowserZoomChipIsDrawn(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	resetZoomForTest()
	t.Cleanup(browserZoomReset)

	config := layoutTestConfig(t)
	variable := "browser_zoom_chip"
	doc := parseWebPage(`<body><p>a quiet page with nothing to see</p></body>`, "", "https://example.com/zoomchip")
	setBrowserDocument(variable, doc)
	t.Cleanup(func() { setBrowserDocument(variable, nil) })
	elem := browserCursorTestElement(variable)

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	capture := func() []byte {
		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.Clear()
		renderTextBrowser(renderer, config, elem)
		buf := make([]byte, probeW*probeH*4)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
			t.Skipf("ReadPixels unavailable: %v", err)
		}
		return buf
	}

	browserZoomIndex = browserZoomDefault // 100%
	normal := capture()
	browserZoomIndex = browserZoomDefault + 2 // 135%
	zoomed := capture()

	// The chip is drawn at its rect, and both frames changed pixels inside it
	// (the label and the fill differ), so the control visibly responds.
	chip := textBrowserZoomRect(config, elem)
	if chip.W <= 0 || chip.H <= 0 {
		t.Fatalf("zoom chip rect = %+v", chip)
	}
	w, h := int(probeW), int(probeH)
	changed := 0
	for y := int(chip.Y); y < int(chip.Y+chip.H) && y < h; y++ {
		for x := int(chip.X); x < int(chip.X+chip.W) && x < w; x++ {
			off := (y*w + x) * 4
			if normal[off] != zoomed[off] || normal[off+1] != zoomed[off+1] || normal[off+2] != zoomed[off+2] {
				changed++
			}
		}
	}
	if changed < 20 {
		t.Fatalf("scaling the text changed only %d pixels inside the zoom chip", changed)
	}
}
