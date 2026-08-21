package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Screenshot Capture — save current screen to file on F12
// ──────────────────────────────────────────────────────────────────────

var lastScreenshotTime time.Time

// captureScreenshot saves the current renderer output to a BMP file.
func captureScreenshot(renderer *sdl.Renderer) {
	now := time.Now()
	if now.Sub(lastScreenshotTime) < time.Second {
		return
	}
	lastScreenshotTime = now

	w, h, err := renderer.GetOutputSize()
	if err != nil {
		ShowToast("Screenshot failed: "+err.Error(), ToastKindError)
		return
	}

	surface, err := sdl.CreateRGBSurfaceWithFormat(0, w, h, 32, sdl.PIXELFORMAT_ARGB8888)
	if err != nil {
		ShowToast("Screenshot failed: "+err.Error(), ToastKindError)
		return
	}
	defer surface.Free()

	surface.Lock()
	renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&surface.Pixels()[0]), int(surface.Pitch))
	surface.Unlock()

	// Determine save path
	var dir string
	switch runtime.GOOS {
	case "windows":
		dir = filepath.Join(os.Getenv("USERPROFILE"), "Pictures", "JukaHub")
	default:
		dir = filepath.Join(os.Getenv("HOME"), "Pictures", "JukaHub")
	}
	os.MkdirAll(dir, 0755)

	filename := fmt.Sprintf("screenshot_%s.bmp", now.Format("2006-01-02_15-04-05"))
	path := filepath.Join(dir, filename)

	if err := surface.SaveBMP(path); err != nil {
		ShowToast("Screenshot save failed", ToastKindError)
		return
	}
	ShowToast(fmt.Sprintf("[Screenshot] %s", filename), ToastKindSuccess)
}
