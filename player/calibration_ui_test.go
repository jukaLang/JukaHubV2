package main

// calibration_ui_test.go — the PPSSPP stick wizard: the numbers it writes, the
// ini edit, discovery, and a frame of the card.

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func TestCalibrationValues(t *testing.T) {
	cases := []struct {
		name       string
		idle, max  float64
		wantDZ     float64
		wantLimits float64
	}{
		// A stick that cannot reach half range: limiter off, deadzone at the floor.
		{"limited stick", 0.02, 0.35, 0.02, 0},
		// Healthy stick with a little drift: deadzone just above the noise.
		{"drifting stick", 0.05, 0.95, 0.1, 0},
		// Heavy drift is capped rather than swallowing the stick.
		{"heavy drift", 0.2, 0.9, 0.12, 0},
		// No drift at all still gets the floor, never a zero deadzone.
		{"clean stick", 0, 1, 0.02, 0},
		{"garbage", -1, -1, 0.02, 0},
	}
	for _, c := range cases {
		dz, limiter := calibrationValues(c.idle, c.max)
		if dz != c.wantDZ || limiter != c.wantLimits {
			t.Errorf("%s: values(%v, %v) = %v/%v, want %v/%v",
				c.name, c.idle, c.max, dz, limiter, c.wantDZ, c.wantLimits)
		}
		if c.max >= 0 {
			if dz > 0.5*c.max+1e-9 && c.max >= 0.5 {
				t.Errorf("%s: deadzone %v leaves less than half the stick", c.name, dz)
			}
		}
	}
	// NaN must not propagate into the ini.
	if dz, _ := calibrationValues(math.NaN(), math.NaN()); dz != 0.02 {
		t.Errorf("NaN deadzone = %v", dz)
	}
}

func TestCalibrationAxisMagnitude(t *testing.T) {
	if got := calibrationAxisMagnitude(0, 0); got != 0 {
		t.Errorf("centred = %v", got)
	}
	if got := calibrationAxisMagnitude(300, -200); got != 0 {
		t.Errorf("noise = %v, want 0", got)
	}
	if got := calibrationAxisMagnitude(calAxisFull, 0); got != 1 {
		t.Errorf("full = %v", got)
	}
	// A diagonal push is longer than one axis but never above full range.
	if got := calibrationAxisMagnitude(20000, 20000); got < 0.8 || got > 1 {
		t.Errorf("diagonal push = %v, want 0.8..1", got)
	}
	if got := calibrationAxisMagnitude(40000, 40000); got != 1 {
		t.Errorf("over-range = %v, want 1", got)
	}
}

func TestWritePPSSPPStickFix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ppsspp.ini")
	body := "[General]\r\nFirstRun = False\r\n\r\n[ControlMapping]\r\nAnalogDeadzone = 0.150\r\nAnalogLimiter = 0.600\r\nLeftStickDeadzone = 0.150\r\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writePPSSPPStickFix(path, 0.04, 0); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "AnalogDeadzone = 0.040") {
		t.Errorf("deadzone not written:\n%s", text)
	}
	if !strings.Contains(text, "AnalogLimiter = 0.000") {
		t.Errorf("limiter not written:\n%s", text)
	}
	// The pass-through key belongs to another option and must not move.
	if !strings.Contains(text, "LeftStickDeadzone = 0.150") {
		t.Errorf("an unrelated key was rewritten:\n%s", text)
	}
	if !strings.Contains(text, "[General]") || !strings.Contains(text, "FirstRun = False") {
		t.Errorf("another section was damaged:\n%s", text)
	}
	if !strings.Contains(text, "\r\n") {
		t.Error("CRLF line endings were not preserved")
	}
	// Exactly one backup, holding the original values.
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if !strings.Contains(string(backup), "AnalogDeadzone = 0.150") {
		t.Errorf("backup does not hold the original:\n%s", backup)
	}
	if err := writePPSSPPStickFix(path, 0.03, 0); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if again, _ := os.ReadFile(path + ".bak"); string(again) != string(backup) {
		t.Error("the second write overwrote the original backup")
	}
}

func TestFindPPSSPPConfigAndInstall(t *testing.T) {
	resetPPSSPPInstallCache()
	t.Cleanup(resetPPSSPPInstallCache)
	root := t.TempDir()
	if findPPSSPPConfig([]string{root}) != "" {
		t.Error("found a config in an empty tree")
	}
	dir := filepath.Join(root, "PSP", "SYSTEM")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ini := filepath.Join(dir, "ppsspp.ini")
	if err := os.WriteFile(ini, []byte("[ControlMapping]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findPPSSPPConfig([]string{root}); got != ini {
		t.Errorf("found %q, want %q", got, ini)
	}
	if !hasPPSSPPInstall([]string{root}) {
		t.Error("hasPPSSPPInstall missed an existing install")
	}
	if hasPPSSPPInstall([]string{t.TempDir()}) {
		t.Error("hasPPSSPPInstall reported a missing install")
	}
}

// The wizard must draw without a controller attached (the error state) and
// report a sane card rect, since a panic here would take the app down mid-game.
func TestCalibrationPanelRenders(t *testing.T) {
	f := newProbeFrame(t)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	if font, _ := getCachedFont(cfg, "small"); font == nil {
		t.Skip("no usable font in this environment")
	}

	prevW, prevH := screenWidth, screenHeight
	prev := calibrationPanel
	screenWidth, screenHeight = probeW, probeH
	calibrationPanel = calibrationState{
		open:     true,
		phase:    calPhaseDone,
		path:     "/mnt/SDCARD/PSP/SYSTEM/ppsspp.ini",
		result:   "deadzone 0.04 · limiter off · backup saved",
		idlePeak: 0.03, maxDeflect: 0.94,
	}
	calibrationPanel.enter.cancel()
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		calibrationPanel = prev
	})

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderStickCalibrationPanel(renderer, cfg)

	card := calibrationPanel.rect
	if card.W <= 0 || card.H <= 0 || card.X < 0 || card.Y < 0 ||
		card.X+card.W > probeW || card.Y+card.H > probeH {
		t.Fatalf("card rect = %+v does not fit %dx%d", card, probeW, probeH)
	}
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}
	mid := sdl.Rect{X: card.X + card.W/4, Y: card.Y + card.H/2, W: card.W / 2, H: card.H / 6}
	if bandLuma(f, mid) == 0 {
		t.Error("the card body is indistinguishable from the background")
	}
}
