package main

// uiprofile.go — per-device UI canvas selection.
//
// The shipped design is authored at 1280x720. The Smart Pro and Smart Pro S
// are 16:9, so that canvas maps 1:1. The Brick, Brick Pro and Hammer are 4:3
// (1024x768); rendering the 16:9 canvas there either letterboxes the UI into a
// short strip or, when a config declares 1024x768, clips elements authored
// past x=1024.
//
// For the 4:3 devices JukaHub uses a 960x720 canvas instead: same height, same
// aspect as the panel, and 75% of the design width. Scene geometry is scaled
// horizontally into that canvas once at load. SDL then maps 960x720 onto the
// 1024x768 panel, so the UI fills the screen and stays uniformly sized instead
// of being clipped or squeezed into a letterbox.

import (
	"log"
	"math"
	"strconv"
	"strings"
)

const (
	uiCanvasWideWidth   int32 = 1280
	uiCanvasWideHeight  int32 = 720
	uiCanvasCompactW    int32 = 960
	uiCanvasCompactH    int32 = 720
	uiNativeBrickWidth  int32 = 1024
	uiNativeBrickHeight int32 = 768
)

// deviceScreenProfile describes a supported panel and the logical canvas the
// UI is drawn into. ok is false for development/unknown devices, which keep
// whatever canvas the config declares.
type deviceScreenProfile struct {
	NativeW int32
	NativeH int32
	CanvasW int32
	CanvasH int32
	Compact bool // 4:3 compact canvas
}

func deviceScreenProfileFor(device DeviceModel) (deviceScreenProfile, bool) {
	switch device {
	case DeviceTrimuiSmartPro, DeviceTrimuiSmartProS:
		return deviceScreenProfile{
			NativeW: uiCanvasWideWidth, NativeH: uiCanvasWideHeight,
			CanvasW: uiCanvasWideWidth, CanvasH: uiCanvasWideHeight,
		}, true
	case DeviceTrimuiBrick, DeviceTrimuiBrickPro, DeviceTrimuiHammer:
		return deviceScreenProfile{
			NativeW: uiNativeBrickWidth, NativeH: uiNativeBrickHeight,
			CanvasW: uiCanvasCompactW, CanvasH: uiCanvasCompactH,
			Compact: true,
		}, true
	default:
		return deviceScreenProfile{}, false
	}
}

// applyDeviceUIProfile applies the detected device's canvas. It is a no-op on
// development builds and unknown hardware.
func applyDeviceUIProfile(config *Config) bool {
	return applyUIProfileForDevice(config, DetectDevice())
}

// applyUIProfileForDevice is the testable core of applyDeviceUIProfile.
func applyUIProfileForDevice(config *Config, device DeviceModel) bool {
	if config == nil {
		return false
	}
	profile, ok := deviceScreenProfileFor(device)
	if !ok {
		return false
	}

	designW, designH := canvasDimension(config.Width), canvasDimension(config.Height)
	if designW <= 0 {
		designW = canvasDimension(config.Variables.ScreenWidth)
	}
	if designH <= 0 {
		designH = canvasDimension(config.Variables.ScreenHeight)
	}
	if designW <= 0 {
		designW = uiCanvasWideWidth
	}
	if designH <= 0 {
		designH = uiCanvasWideHeight
	}

	if designW != profile.CanvasW || designH != profile.CanvasH {
		scaleSceneGeometry(config, designW, designH, profile.CanvasW, profile.CanvasH)
	}
	config.Width = int(profile.CanvasW)
	config.Height = int(profile.CanvasH)
	config.Variables.ScreenWidth = int(profile.CanvasW)
	config.Variables.ScreenHeight = int(profile.CanvasH)
	setConfigDimension(config, "screenWidth", int(profile.CanvasW))
	setConfigDimension(config, "screenHeight", int(profile.CanvasH))

	mode := "wide"
	if profile.Compact {
		mode = "compact"
	}
	log.Printf("[ui] %s: %dx%d panel, %s canvas %dx%d",
		deviceDisplayName(device), profile.NativeW, profile.NativeH,
		mode, profile.CanvasW, profile.CanvasH)
	return true
}

// maxCanvasDimension bounds a canvas dimension read from the config. The
// renderer works in int32, so a larger JSON number would wrap negative — and a
// negative design size is what scaleSceneGeometry then refuses to use at all.
const maxCanvasDimension = 8192

// canvasDimension converts a config-declared dimension to the int32 the
// renderer uses. Values that are absent, negative or absurd return 0 so the
// caller falls back to the built-in canvas instead of drawing into a wrapped
// (and therefore negative) coordinate space.
func canvasDimension(v int) int32 {
	if v <= 0 || v > maxCanvasDimension {
		return 0
	}
	return int32(v)
}

// scaleSceneGeometry maps the authored scene coordinates into the target
// canvas. Heights stay at the 720 design height on compact devices; widths and
// x positions shrink by 25% so nothing runs off the 4:3 panel.
func scaleSceneGeometry(config *Config, designW, designH, targetW, targetH int32) {
	if config == nil || designW <= 0 || designH <= 0 || targetW <= 0 || targetH <= 0 {
		return
	}
	sx := float64(targetW) / float64(designW)
	sy := float64(targetH) / float64(designH)
	scale := func(v int32, factor float64) int32 {
		return int32(math.Round(float64(v) * factor))
	}
	scaleSize := func(raw StringOrInt, factor float64) StringOrInt {
		text := strings.TrimSpace(string(raw))
		if text == "" {
			return raw
		}
		v, ok := parsePosInt(text)
		if !ok || v == 0 {
			return raw
		}
		n := int32(math.Round(float64(v) * factor))
		if n < 1 {
			n = 1
		}
		return StringOrInt(strconv.Itoa(int(n)))
	}
	for si := range config.Scenes {
		for ei := range config.Scenes[si].Elements {
			e := &config.Scenes[si].Elements[ei]
			e.X = scale(e.X, sx)
			e.Y = scale(e.Y, sy)
			e.Width = scaleSize(e.Width, sx)
			e.Height = scaleSize(e.Height, sy)
		}
	}
}

// uiTextScaleFactor returns how far logical font sizes shrink when the
// authored 1280-wide design is squeezed into a narrower canvas. The compact
// panels keep the 720 height and lose a quarter of the width, so text has to
// give the same 25% or every row of labels overflows: at the 1280 type scale
// a 960-wide panel truncates "Play Link" to "Play …" and clips the whole
// right end of the controller hint bar. Wide canvases are unscaled.
func uiTextScaleFactor() float64 {
	if screenWidth <= 0 || screenWidth >= uiCanvasWideWidth {
		return 1
	}
	scale := float64(screenWidth) / float64(uiCanvasWideWidth)
	if scale < 0.6 {
		scale = 0.6
	}
	return scale
}

// uiScaleFontSize applies uiTextScaleFactor to a logical font size. The
// result never grows past the requested size and never drops below a legible
// floor, so tiny captions stay readable on the 4:3 panels.
func uiScaleFontSize(size int) int {
	if size <= 0 {
		return size
	}
	scale := uiTextScaleFactor()
	if scale >= 0.999 {
		return size
	}
	scaled := int(math.Round(float64(size) * scale))
	if scaled < 9 {
		scaled = 9
	}
	if scaled > size {
		scaled = size
	}
	return scaled
}

// setConfigDimension updates the typed screen size and every Custom spelling
// of it. syncVariableOverrides reads Custom case-insensitively, so leaving the
// authored 1280 there would let a later settings save restore the wrong canvas.
func setConfigDimension(config *Config, name string, value int) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = make(map[string]interface{})
	}
	found := false
	for key := range config.Variables.Custom {
		if strings.EqualFold(key, name) {
			config.Variables.Custom[key] = value
			found = true
		}
	}
	if !found {
		config.Variables.Custom[name] = value
	}
}
