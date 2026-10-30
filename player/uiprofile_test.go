package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// A config dimension is a JSON number, and the renderer works in int32. A value
// that does not fit has to fall back to the built-in canvas instead of wrapping
// negative, which would leave the UI drawing into an empty coordinate space.
func TestCanvasDimensionRejectsOutOfRangeValues(t *testing.T) {
	cases := map[int]int32{
		0: 0, -1: 0, 1280: 1280, 720: 720,
		maxCanvasDimension:     maxCanvasDimension,
		maxCanvasDimension + 1: 0,
		1 << 40:                0,
	}
	for in, want := range cases {
		if got := canvasDimension(in); got != want {
			t.Errorf("canvasDimension(%d) = %d, want %d", in, got, want)
		}
	}

	// A config with an absurd size keeps the profile canvas rather than
	// scaling into a negative design size.
	config := &Config{Width: 1 << 33, Height: 1 << 33}
	if !applyUIProfileForDevice(config, DeviceTrimuiBrick) {
		t.Fatal("profile was not applied")
	}
	if config.Width != int(uiCanvasCompactW) || config.Height != int(uiCanvasCompactH) {
		t.Fatalf("canvas = %dx%d, want %dx%d", config.Width, config.Height, uiCanvasCompactW, uiCanvasCompactH)
	}
}

func TestDeviceScreenProfiles(t *testing.T) {
	wide := []DeviceModel{DeviceTrimuiSmartPro, DeviceTrimuiSmartProS}
	for _, device := range wide {
		p, ok := deviceScreenProfileFor(device)
		if !ok || p.CanvasW != 1280 || p.CanvasH != 720 || p.Compact {
			t.Errorf("%s profile = %+v, want the wide 1280x720 canvas", device, p)
		}
	}
	compact := []DeviceModel{DeviceTrimuiBrick, DeviceTrimuiBrickPro, DeviceTrimuiHammer}
	for _, device := range compact {
		p, ok := deviceScreenProfileFor(device)
		if !ok || p.NativeW != 1024 || p.NativeH != 768 || p.CanvasW != 960 || p.CanvasH != 720 || !p.Compact {
			t.Errorf("%s profile = %+v, want 1024x768 native and a 960x720 canvas", device, p)
		}
	}
	if _, ok := deviceScreenProfileFor(DeviceUnknown); ok {
		t.Error("unknown device must keep the config canvas")
	}
}

func TestApplyUIProfileShrinksBrickSceneGeometry(t *testing.T) {
	cfg := &Config{
		Width:  1280,
		Height: 720,
		Scenes: []SceneConfig{{Name: "Main", Elements: []Element{
			{X: 956, Y: 454, Width: "292", Height: "158"},
			{X: 0, Y: 672, Width: "", Height: "36"},
		}}},
	}
	cfg.Variables.Custom = map[string]interface{}{
		"ScreenWidth":  1280,
		"ScreenHeight": 720,
	}
	if !applyUIProfileForDevice(cfg, DeviceTrimuiBrick) {
		t.Fatal("Brick profile was not applied")
	}
	if cfg.Variables.ScreenWidth != 960 || cfg.Variables.ScreenHeight != 720 {
		t.Fatalf("screen = %dx%d, want 960x720", cfg.Variables.ScreenWidth, cfg.Variables.ScreenHeight)
	}
	if cfg.Width != 960 || cfg.Height != 720 {
		t.Fatalf("design = %dx%d, want 960x720", cfg.Width, cfg.Height)
	}
	e := cfg.Scenes[0].Elements[0]
	if e.X != 717 || e.Y != 454 || e.Width != "219" || e.Height != "158" {
		t.Fatalf("scaled element = %+v, want x=717 y=454 w=219 h=158", e)
	}
	if got := cfg.Variables.Custom["ScreenWidth"]; got != 960 {
		t.Fatalf("Custom ScreenWidth = %v, want 960", got)
	}

	// Applying the profile again must not shrink a second time.
	applyUIProfileForDevice(cfg, DeviceTrimuiBrick)
	if got := cfg.Scenes[0].Elements[0].X; got != 717 {
		t.Fatalf("second apply moved x to %d", got)
	}
}

func TestApplyUIProfileLeavesWideDevicesAlone(t *testing.T) {
	cfg := &Config{
		Width:  1280,
		Height: 720,
		Scenes: []SceneConfig{{Elements: []Element{{X: 900, Y: 200, Width: "300", Height: "100"}}}},
	}
	if !applyUIProfileForDevice(cfg, DeviceTrimuiSmartProS) {
		t.Fatal("Smart Pro S profile was not applied")
	}
	e := cfg.Scenes[0].Elements[0]
	if e.X != 900 || e.Y != 200 || e.Width != "300" || e.Height != "100" {
		t.Fatalf("wide device was rescaled: %+v", e)
	}
	if cfg.Variables.ScreenWidth != 1280 || cfg.Variables.ScreenHeight != 720 {
		t.Fatalf("wide screen = %dx%d", cfg.Variables.ScreenWidth, cfg.Variables.ScreenHeight)
	}
}

func TestCompactCanvasMouseMapping(t *testing.T) {
	prevW, prevH := screenWidth, screenHeight
	prevScale := windowScaleFactor
	prevX, prevY := windowOffsetX, windowOffsetY
	prevPhysW, prevPhysH := windowPhysicalW, windowPhysicalH
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		windowScaleFactor = prevScale
		windowOffsetX, windowOffsetY = prevX, prevY
		windowPhysicalW, windowPhysicalH = prevPhysW, prevPhysH
	})
	screenWidth, screenHeight = 960, 720
	updateWindowScale(1024, 768)
	if windowOffsetX != 0 || windowOffsetY != 0 {
		t.Fatalf("offsets = %d,%d, want 0,0 on matching 4:3 aspects", windowOffsetX, windowOffsetY)
	}
	x, y := physicalToLogical(512, 384)
	if x != 480 || y != 360 {
		t.Fatalf("physical (512,384) -> logical (%d,%d), want (480,360)", x, y)
	}
}

// The panel's own aspect is the last resort for hardware the launcher cannot
// name: the shipped 16:9 design drawn into a 4:3 display is letterboxed into a
// strip with bars above and below, which is what a Brick whose model string
// names no TrimUI device showed. A zero size must never count as 4:3.
func TestPanelIsFourThree(t *testing.T) {
	cases := []struct {
		w, h int32
		want bool
	}{
		{1024, 768, true},
		{800, 600, true},
		{960, 720, true},
		{1280, 720, false},
		{1280, 1024, false}, // 5:4
		{480, 272, false},
		{0, 0, false},
		{1024, 0, false},
		{-1024, 768, false},
	}
	for _, tc := range cases {
		if got := panelIsFourThree(tc.w, tc.h); got != tc.want {
			t.Errorf("panelIsFourThree(%d, %d) = %v, want %v", tc.w, tc.h, got, tc.want)
		}
	}
}

func TestApplyPanelCanvasFillsAFourThreePanel(t *testing.T) {
	cfg := &Config{
		Width:  1280,
		Height: 720,
		Scenes: []SceneConfig{{Name: "Main", Elements: []Element{
			{Type: "button", X: 900, Y: 164, Width: "348", Height: "121"},
		}}},
	}
	if !applyPanelCanvas(cfg, 1024, 768) {
		t.Fatal("a 1024x768 panel did not get the compact canvas")
	}
	if cfg.Width != 960 || cfg.Height != 720 {
		t.Fatalf("canvas = %dx%d, want 960x720", cfg.Width, cfg.Height)
	}
	if cfg.Variables.ScreenWidth != 960 || cfg.Variables.ScreenHeight != 720 {
		t.Fatalf("screen variables = %dx%d, want 960x720",
			cfg.Variables.ScreenWidth, cfg.Variables.ScreenHeight)
	}
	e := cfg.Scenes[0].Elements[0]
	if e.X != 675 || e.Y != 164 || e.Width != "261" || e.Height != "121" {
		t.Fatalf("element = %+v, want the design squeezed into the compact canvas", e)
	}
}

func TestApplyPanelCanvasLeavesOtherPanelsAndDesignsAlone(t *testing.T) {
	wide := &Config{Width: 1280, Height: 720}
	if applyPanelCanvas(wide, 1280, 720) {
		t.Error("a 16:9 panel must keep the wide canvas")
	}
	if wide.Width != 1280 {
		t.Fatalf("a 16:9 panel was resized to %dx%d", wide.Width, wide.Height)
	}
	// A design that is already 4:3 is what its author drew; it is not remapped.
	compact := &Config{Width: 960, Height: 720}
	if applyPanelCanvas(compact, 1024, 768) {
		t.Error("a 4:3 design must be left as authored")
	}
	if compact.Width != 960 {
		t.Fatalf("a 4:3 design was resized to %dx%d", compact.Width, compact.Height)
	}
	if applyPanelCanvas(nil, 1024, 768) {
		t.Error("a nil config was accepted")
	}
}

// The compact canvas is a real viewport for the home layout too.
func TestHomeLayoutFitsCompactCanvas(t *testing.T) {
	h := NewHomeLayout(uiCanvasCompactW, uiCanvasCompactH)
	for _, problem := range h.BoundsViolations() {
		t.Errorf("%s", problem)
	}
	if footer := ScaleHomeRect(sdl.Rect{X: 0, Y: 672, W: 1280, H: 48}, h.Width, h.Height); footer.Y+footer.H > h.Height {
		t.Errorf("compact footer %+v falls below the canvas", footer)
	}
}
