package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

// screenshotDirName is the folder inside JukaHub's own folder that holds
// screenshots (Juka, 2026-11-17: "the handheld should save to
// JukaHub/screenshots"): Apps/JukaHub/screenshots on a TrimUI, the same folder
// beside JukaHub.exe on a PC.
const screenshotDirName = "screenshots"

// screenshotAppDir is <JukaHub's folder>/screenshots, whether or not it exists.
func screenshotAppDir() string {
	return filepath.Join(exeDir(), screenshotDirName)
}

// screenshotDir is where screenshots are saved. JUKAHUB_SCREENSHOT_DIR wins
// when set. Otherwise it is the screenshots folder inside JukaHub's own folder,
// on the handheld and on a PC alike, so the pictures stay with the app (on a PC
// not in Pictures, which Windows often syncs to OneDrive). When that folder
// cannot be written (a read-only system image, Program Files) it falls back to
// the SD card's Screenshots folder on a TrimUI and Pictures\JukaHub on a PC.
// Any goroutine; the write probe runs once.
func screenshotDir() string {
	if d := strings.TrimSpace(os.Getenv("JUKAHUB_SCREENSHOT_DIR")); d != "" {
		return d
	}
	screenshotLocal.once.Do(func() {
		dir := screenshotAppDir()
		if screenshotDirWritable(dir) {
			screenshotLocal.dir = dir
		}
	})
	if screenshotLocal.dir != "" {
		return screenshotLocal.dir
	}
	if IsTrimuiDevice() {
		if st, err := os.Stat("/mnt/SDCARD"); err == nil && st.IsDir() {
			return "/mnt/SDCARD/Screenshots"
		}
	}
	return screenshotPicturesDir()
}

// screenshotLocal caches the app-folder answer: the probe writes a file, and
// the folder does not move while JukaHub runs.
var screenshotLocal struct {
	once sync.Once
	dir  string
}

// screenshotPicturesDir is Pictures\JukaHub (~/Pictures/JukaHub elsewhere),
// the PC fallback and where older versions saved.
func screenshotPicturesDir() string {
	home := os.Getenv("HOME")
	if runtime.GOOS == "windows" || home == "" {
		if up := os.Getenv("USERPROFILE"); up != "" {
			home = up
		}
	}
	return filepath.Join(home, "Pictures", "JukaHub")
}

// screenshotDirWritable makes dir and proves a file can be created in it.
func screenshotDirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".jukahub-write-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
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
	// The camera flash, drawn after this capture, and a preview card of the
	// picture instead of a text toast (shotpreview.go).
	shotFlashStart()
	maxW, maxH := shotThumbLimits(screenWidth, screenHeight)
	go func() {
		var gen uint64
		defer func() {
			if r := recover(); r != nil {
				shotPreviewFailed(gen)
				menuShotNotice("Screenshot failed", ToastKindError)
			}
		}()
		gen = shotPreviewPark(shotThumb(pix, stride, int(w), int(h), maxW, maxH))
		data, err := castPNG(pix, stride, int(w), int(h))
		if err != nil {
			shotPreviewFailed(gen)
			menuShotNotice("Screenshot failed: "+err.Error(), ToastKindError)
			return
		}
		path, err := screenshotPath("screenshot", now)
		if err == nil {
			err = os.WriteFile(path, data, 0o644)
		}
		if err != nil {
			logWarn("screenshot", "could not save the screenshot: %v", err)
			shotPreviewFailed(gen)
			menuShotNotice("Screenshot save failed", ToastKindError)
			return
		}
		trophyEvent(tkScreenshot)
		playHaptic(hapticSuccessPulse)
		logInfo("screenshot", "saved %s", path)
		if gen == 0 {
			// No preview could be made: say it in words.
			menuShotNotice("Screenshot saved to "+path, ToastKindSuccess)
			return
		}
		shotPreviewSaved(gen, path)
	}()
}
