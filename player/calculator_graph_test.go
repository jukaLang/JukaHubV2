package main

import (
	"math"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func withCalcState(t *testing.T) {
	t.Helper()
	previous := calc
	calc = calcState{display: "0", hasResult: true}
	t.Cleanup(func() { calc = previous })
}

func TestCalcEvalExprAtX(t *testing.T) {
	for _, tc := range []struct {
		name string
		expr string
		x    float64
		want float64
	}{
		{"square", "x^2", 3, 9},
		{"implicit coefficient", "2x+1", 3, 7},
		{"trig function", "sin(x)", math.Pi / 2, 1},
		{"nested function", "sqrt(x^2)", -4, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := calcEvalExprAtX(tc.expr, tc.x)
			if err != nil {
				t.Fatalf("calcEvalExprAtX(%q, %g): %v", tc.expr, tc.x, err)
			}
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("calcEvalExprAtX(%q, %g) = %g, want %g", tc.expr, tc.x, got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name string
		expr string
		x    float64
	}{
		{"empty", "", 1},
		{"unsupported identifier", "max(x,1)", 1},
		{"outside sqrt domain", "sqrt(x)", -1},
		{"non-finite input", "x", math.Inf(1)},
		{"oversized expression", string(make([]byte, 513)), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := calcEvalExprAtX(tc.expr, tc.x); err == nil {
				t.Errorf("calcEvalExprAtX(%q, %g) unexpectedly succeeded", tc.expr, tc.x)
			}
		})
	}
}

func TestCalcGraphSamplesBoundsAndMapping(t *testing.T) {
	samples := calcGraphSamples("x^2", -2, 2, 5)
	if len(samples) != 5 {
		t.Fatalf("got %d samples, want 5", len(samples))
	}
	for i, want := range []float64{4, 1, 0, 1, 4} {
		if !samples[i].valid || math.Abs(samples[i].y-want) > 1e-9 {
			t.Errorf("sample %d = %+v, want y=%g valid", i, samples[i], want)
		}
	}
	minY, maxY, ok := calcGraphYBounds(samples)
	if !ok || minY >= 0 || maxY <= 4 {
		t.Fatalf("auto y bounds = (%g, %g, %v), want padded bounds around 0..4", minY, maxY, ok)
	}
	plot := sdl.Rect{X: 10, Y: 20, W: 101, H: 81}
	x, y, ok := calcGraphMapPoint(samples[2], -2, 2, minY, maxY, plot)
	if !ok || x != 60 || y < plot.Y || y >= plot.Y+plot.H {
		t.Fatalf("mapped origin = (%d,%d,%v), want centered and inside %+v", x, y, ok, plot)
	}

	invalid := calcGraphSamples("1/x", -1, 1, 3)
	if len(invalid) != 3 || !invalid[0].valid || invalid[1].valid || !invalid[2].valid {
		t.Fatalf("division-by-zero samples = %+v, want invalid center sample", invalid)
	}
	if got := calcGraphSamples("x", -1, 1, 513); got != nil {
		t.Fatalf("oversized graph returned %d samples", len(got))
	}
}

func TestCalculatorGraphRendersAndButtonsFit(t *testing.T) {
	for _, size := range []struct {
		name string
		w, h int32
	}{
		{"SmartPro", uiCanvasWideWidth, uiCanvasWideHeight},
		{"CompactHandheld", uiCanvasCompactW, uiCanvasCompactH},
	} {
		t.Run(size.name, func(t *testing.T) {
			withScreen(t, size.w, size.h)
			config, err := loadConfig("jukaconfig.json")
			if err != nil {
				t.Fatalf("load calculator config: %v", err)
			}
			idx := len(config.Scenes)
			config.Scenes = append(config.Scenes, SceneConfig{Name: "Calculator"})
			previousCalc, previousScene, previousConfig := calc, currentSceneIndex, appConfig
			calc = calcState{expression: "x^2", display: "x^2", graphMode: true}
			currentSceneIndex, appConfig = idx, config
			t.Cleanup(func() { calc, currentSceneIndex, appConfig = previousCalc, previousScene, previousConfig })

			renderer := newKeyboardTestRenderer(t)
			renderCalculator(renderer, config)
			for name, rect := range map[string]sdl.Rect{"Graph": calcGraphButtonRect, "x": calcXButtonRect} {
				if rect.W <= 0 || rect.X < 0 || rect.X+rect.W > size.w || rect.Y < 0 || rect.Y+rect.H > size.h {
					t.Errorf("%s button leaves %dx%d canvas: %+v", name, size.w, size.h, rect)
				}
			}
			for row := range calcButtonRects {
				for col, rect := range calcButtonRects[row] {
					if rect.X < 0 || rect.X+rect.W > size.w || rect.Y < 0 || rect.Y+rect.H > size.h {
						t.Errorf("key[%d][%d] leaves %dx%d canvas: %+v", row, col, size.w, size.h, rect)
					}
				}
			}

			calc.graphMode = false
			mx, my := calcXButtonRect.X+calcXButtonRect.W/2, calcXButtonRect.Y+calcXButtonRect.H/2
			calc.expression, calc.display, calc.hasResult = "", "0", true
			if !handleCalculatorMouse(mx, my, config) || calc.expression != "x" {
				t.Errorf("tap on x button did not insert variable: expression=%q", calc.expression)
			}
			mx, my = calcGraphButtonRect.X+calcGraphButtonRect.W/2, calcGraphButtonRect.Y+calcGraphButtonRect.H/2
			if !handleCalculatorMouse(mx, my, config) || !calc.graphMode {
				t.Error("tap on Graph button did not open graph view")
			}
		})
	}
}

func TestCalculatorGraphInputShortcuts(t *testing.T) {
	withCalcState(t)
	if !ctrlCalculator(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_X}) {
		t.Fatal("controller X was not consumed")
	}
	if calc.expression != "x" || calc.hasResult {
		t.Fatalf("controller X state = expression %q, hasResult %v", calc.expression, calc.hasResult)
	}
	if !ctrlCalculator(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_Y}) || !calc.graphMode {
		t.Fatal("controller Y did not open graph mode")
	}
	handleCalcInput(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_g}})
	if calc.graphMode {
		t.Fatal("keyboard G did not toggle graph mode off")
	}
	handleCalcInput(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_x}})
	if calc.expression != "xx" {
		t.Fatalf("keyboard X expression = %q, want xx", calc.expression)
	}
}
