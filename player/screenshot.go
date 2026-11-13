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
// Screenshot Capture — save the current screen as a PNG: F12, the Start
// menu's Screenshot, a control profile, or Menu held (menushot.go)
// ──────────────────────────────────────────────────────────────────────

var lastScreenshotTime time.Time

// screenshotRequested is set from the menu, which runs on the input path and
// has no renderer; the frame loop consumes it so the captured image is the
// frame the user was looking at.
var screenshotRequested bool

// requestScreenshot asks the next rendered frame to save a copy of itself.
func requestScreenshot() {
	screenshotRequested = true
}

// capturePendingScreenshot saves the frame drawn this tick. It reports whether
// a request was handled. It also runs screenshot mode's tick, the capture tour
// included (screenshotmode.go), since this is where a finished frame is read,
// and shows what the screenshot savers finished meanwhile (menushot.go).
func capturePendingScreenshot(renderer *sdl.Renderer) bool {
	menuShotDrainNotices()
	shotTick(renderer)
	if !screenshotRequested || renderer == nil {
		return false
	}
	screenshotRequested = false
	if shotModeOn() {
		shotSaveManual(renderer)
		return true
	}
	captureScreenshot(renderer)
	return true
}

// screenshotDir is where screenshots are saved: the SD card's Screenshots
// folder on a TrimUI (where RetroArch saves its own, and where Files and the
// wallpaper picker look), Pictures\JukaHub on a PC. Any goroutine.
func screenshotDir() string {
	if IsTrimuiDevice() {
		if st, err := os.Stat("/mnt/SDCARD"); err == nil && st.IsDir() {
			return "/mnt/SDCARD/Screenshots"
		}
	}
	home := os.Getenv("HOME")
	if runtime.GOOS == "windows" {
		home = os.Getenv("USERPROFILE")
	}
	return filepath.Join(home, "Pictures", "JukaHub")
}

// screenshotPath is a new file in screenshotDir named prefix and the time,
// with -2, -3… when two land in the same second. Any goroutine.
func screenshotPath(prefix string, at time.Time) (string, error) {
	dir := screenshotDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := prefix + "_" + at.Format("2006-01-02_15-04-05")
	path := filepath.Join(dir, base+".png")
	for n := 2; n < 100; n++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.png", base, n))
	}
	return path, nil
}

// captureScreenshot reads the current renderer output and saves it as a PNG;
// the encode and the write run on a goroutine, so the frame does not wait on
// the SD card. In screenshot mode it asks for the next finished frame
// instead (capturePendingScreenshot).
func captureScreenshot(renderer *sdl.Renderer) {
	if shotModeOn() {
		requestScreenshot()
		return
	}
	now := time.Now()
	if now.Sub(lastScreenshotTime) < time.Second {
		return
	}
	lastScreenshotTime = now

	w, h, err := renderer.GetOutputSize()
	if err != nil || w <= 0 || h <= 0 {
		ShowToast("Screenshot failed: the screen could not be read", ToastKindError)
		return
	}
	stride := int(w) * 4
	pix := make([]byte, stride*int(h))
	// ABGR8888 is R, G, B, A in memory on these little-endian machines: the
	// order castPNG takes (castcap.go reads the screen the same way).
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ABGR8888, unsafe.Pointer(&pix[0]), stride); err != nil {
		ShowToast("Screenshot failed: "+err.Error(), ToastKindError)
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				menuShotNotice("Screenshot failed", ToastKindError)
			}
		}()
		data, err := castPNG(pix, stride, int(w), int(h))
		if err != nil {
			menuShotNotice("Screenshot failed: "+err.Error(), ToastKindError)
			return
		}
		path, err := screenshotPath("screenshot", now)
		if err == nil {
			err = os.WriteFile(path, data, 0o644)
		}
		if err != nil {
			logWarn("screenshot", "could not save the screenshot: %v", err)
			menuShotNotice("Screenshot save failed", ToastKindError)
			return
		}
		trophyEvent(tkScreenshot)
		playHaptic(hapticSuccessPulse)
		logInfo("screenshot", "saved %s", path)
		menuShotNotice("[Screenshot] "+filepath.Base(path), ToastKindSuccess)
	}()
}
