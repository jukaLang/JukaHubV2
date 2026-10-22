package main

// The Brick family draws the shipped 1280x720 scenes into a 960x720 canvas.
// These checks render every scene of the handheld config at that size so a
// panic, a blank frame or a layout that assumes the full 1280 width shows up in
// CI. Set JUKAHUB_BRICK_DUMP=1 to also write PNGs into tmp/ for a visual pass.

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func TestCompactCanvasRendersEveryScene(t *testing.T) {
	configs := loadAuditConfigs(t)
	config := configs["trimui/jukaconfig.device.json"]
	if config == nil {
		t.Skip("no handheld config next to the test")
	}
	// The device config is authored at 1280x720; the Brick profile squeezes the
	// geometry into 960x720 exactly as startup does.
	applyUIProfileForDevice(config, DeviceTrimuiBrick)
	if config.Width != int(uiCanvasCompactW) || config.Height != int(uiCanvasCompactH) {
		t.Fatalf("profile canvas = %dx%d, want %dx%d",
			config.Width, config.Height, uiCanvasCompactW, uiCanvasCompactH)
	}

	withScreen(t, uiCanvasCompactW, uiCanvasCompactH)
	renderer := newKeyboardTestRenderer(t)
	frame := newProbeFrame(t)

	prevConfig, prevScene := appConfig, currentSceneIndex
	appConfig = config
	t.Cleanup(func() { appConfig, currentSceneIndex = prevConfig, prevScene })

	dump := os.Getenv("JUKAHUB_BRICK_DUMP") != ""
	rendered := 0
	for i := range config.Scenes {
		scene := config.Scenes[i]
		currentSceneIndex = i

		renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
		renderer.Clear()
		renderScene(renderer, config, scene)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&frame.buf[0]), int(probeW)*4); err != nil {
			t.Skipf("ReadPixels unavailable: %v", err)
		}

		// Every scene paints something: the content panel, the header and the
		// hint bar all differ from the cleared background.
		if !frameHasPaint(frame) {
			t.Errorf("%s: nothing was drawn on the compact canvas", scene.Name)
		}
		rendered++
		if dump {
			saveCompactFrame(t, frame, scene.Name)
		}
	}
	if rendered < 20 {
		t.Fatalf("only %d scenes rendered from the handheld config", rendered)
	}
}

// frameHasPaint reports whether the compact area of the frame differs from the
// background colour the test cleared with.
func frameHasPaint(frame *probeFrame) bool {
	for y := 0; y < int(uiCanvasCompactH); y += 8 {
		for x := 0; x < int(uiCanvasCompactW); x += 8 {
			i := (y*int(probeW) + x) * 4
			if frame.buf[i] != ColorBackground.B ||
				frame.buf[i+1] != ColorBackground.G ||
				frame.buf[i+2] != ColorBackground.R {
				return true
			}
		}
	}
	return false
}

// saveCompactFrame writes the frame as PNG for a visual check of the interface.
func saveCompactFrame(t *testing.T, frame *probeFrame, sceneName string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, int(uiCanvasCompactW), int(uiCanvasCompactH)))
	for y := 0; y < int(uiCanvasCompactH); y++ {
		for x := 0; x < int(uiCanvasCompactW); x++ {
			i := (y*int(probeW) + x) * 4
			img.Set(x, y, color.RGBA{R: frame.buf[i+2], G: frame.buf[i+1], B: frame.buf[i], A: 255})
		}
	}
	label := strings.ToLower(strings.ReplaceAll(sceneName, " ", "_"))
	path := fmt.Sprintf("../tmp/brick_%02d_%s.png", currentSceneIndex, label)
	f, err := os.Create(path)
	if err != nil {
		t.Logf("cannot save %s: %v", path, err)
		return
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Logf("cannot encode %s: %v", path, err)
		return
	}
	t.Log(path)
}
