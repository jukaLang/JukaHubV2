package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
	"unsafe"
)

func TestZZOnboardingBrickProbe(t *testing.T) {
	configs := loadAuditConfigs(t)
	config := configs["trimui/jukaconfig.device.json"]
	if config == nil {
		t.Skip("no handheld config next to the test")
	}
	applyUIProfileForDevice(config, DeviceTrimuiBrick)
	withScreen(t, uiCanvasCompactW, uiCanvasCompactH)
	config.Variables.ReducedMotion = true

	prevActive, prevStep, prevInfo := onboarding.active, onboarding.step, onboarding.info
	prevConfig, prevScene := appConfig, currentSceneIndex
	t.Cleanup(func() {
		onboarding.active, onboarding.step, onboarding.info = prevActive, prevStep, prevInfo
		appConfig, currentSceneIndex = prevConfig, prevScene
	})
	appConfig = config
	onboarding.active = true
	onboarding.step = 0
	onboarding.info = ""

	renderer := newKeyboardTestRenderer(t)
	f := newProbeFrame(t)
	renderer.SetDrawColor(9, 14, 25, 255)
	renderer.Clear()
	renderOnboarding(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels: %v", err)
	}

	// Recompute the card geometry exactly as renderOnboarding clamps it.
	step := onboardingSteps[onboarding.step]
	cardW := int32(820)
	if cardW > screenWidth-80 {
		cardW = screenWidth - 80
	}
	if cardW < 420 {
		cardW = screenWidth - 32
	}
	cardH := int32(470)
	if step.kind == "setup" {
		cardH = 600
	}
	if cardH > screenHeight-100 {
		cardH = screenHeight - 72
	}
	cx := (screenWidth - cardW) / 2
	cy := (screenHeight - cardH) / 2
	if cy < 24 {
		cy = 24
	}
	smallFont, _ := getCachedFont(config, "small")
	bodyFont, _ := getCachedFont(config, "medium")
	footer := hintLine(FooterHint{"Left / Right", "Move"}, FooterHint{"A / Enter", "Next"},
		FooterHint{"F / X", "Action"}, FooterHint{"B", "Skip"})
	fw, fh, _ := smallFont.SizeUTF8(footer)
	t.Logf("screen %dx%d card %dx%d at %d,%d bottom=%d", screenWidth, screenHeight, cardW, cardH, cx, cy, cy+cardH)
	t.Logf("next  %+v", onboarding.next)
	t.Logf("skip  %+v", onboarding.skip)
	t.Logf("action %+v", onboarding.action)
	t.Logf("footer %q w=%d h=%d x=%d y=%d bottom=%d",
		footer, fw, fh, cx+(cardW-int32(fw))/2, cy+cardH-18, cy+cardH-18+int32(fh))
	t.Logf("dots y=%d actionY=%d btnBand=%d..%d", cy+cardH-92, cy+cardH-150,
		onboarding.next.Y, onboarding.next.Y+onboarding.next.H)
	_ = bodyFont
	_ = ttf.Font{}

	// Save the frame for a visual pass.
	img := image.NewRGBA(image.Rect(0, 0, int(uiCanvasCompactW), int(uiCanvasCompactH)))
	for y := 0; y < int(uiCanvasCompactH); y++ {
		for x := 0; x < int(uiCanvasCompactW); x++ {
			i := (y*int(probeW) + x) * 4
			img.Set(x, y, color.RGBA{R: f.buf[i+2], G: f.buf[i+1], B: f.buf[i], A: 255})
		}
	}
	path := fmt.Sprintf("../tmp/onboard_brick_step%d.png", onboarding.step+1)
	out, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer out.Close()
	if err := png.Encode(out, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	t.Log("wrote", path)
}
