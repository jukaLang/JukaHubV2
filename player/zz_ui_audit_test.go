package main

// zz_ui_audit_test.go — a gated visual audit. Run with
//
//	JUKAHUB_UI_AUDIT=1 go test -run TestUIAudit $$BUILDCY bs
//
// and it renders the full scene sweep plus staged background-work states to
// ../tmp/audit_*.png. Tests elsewhere pin contracts; this file exists to give
// the pixels a human review, so it is a no-op in the normal test flow.

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// auditStage places staged background work on the strip: one labelled installer
// run downloading with a known fraction, one media download active without a
// Content-Length, and one row queued.
func auditStage() {
	installBarBeginLabelled("cores", "Updating emulator cores")
	installBarPhase(installPhaseDownload)
	installBarProgress(300, 900)

	downloads.mu.Lock()
	downloads.items = []*downloadItem{
		{ID: 1, Status: downloadActive, Title: "A remarkably long video title that must be truncated by the strip", Done: 0, Total: 0, URL: "https://example.invalid/long"},
		{ID: 2, Status: downloadQueued, Title: "queued.mp4", URL: "https://example.invalid/queued"},
	}
	downloads.nextID = 3
	downloads.mu.Unlock()
}

func auditClear(t *testing.T) func() {
	resetInstallBar()
	downloads.mu.Lock()
	downloads.items = nil
	downloads.mu.Unlock()
	return func() {
		resetInstallBar()
		downloads.mu.Lock()
		downloads.items = nil
		downloads.mu.Unlock()
	}
}

// auditSave writes the captured frame as a PNG next to the test.
func auditSave(t *testing.T, renderer *sdl.Renderer, frame *probeFrame, name string) {
	t.Helper()
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&frame.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, int(screenWidth), int(screenHeight)))
	for y := 0; y < int(screenHeight) && y < int(probeH); y++ {
		for x := 0; x < int(screenWidth) && x < int(probeW); x++ {
			i := (y*int(probeW) + x) * 4
			img.Set(x, y, color.RGBA{R: frame.buf[i+2], G: frame.buf[i+1], B: frame.buf[i], A: 255})
		}
	}
	path := fmt.Sprintf("uiaudit/%s_%dx%d.png", name, screenWidth, screenHeight)
	_ = os.MkdirAll("uiaudit", 0o755)
	f, err := os.Create(path)
	if err != nil {
		t.Logf("cannot save %s: %v", path, err)
		return
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Logf("cannot write %s: %v", path, err)
	}
}

// auditSweep renders every scene of the given config (plus optional staged work)
// and dumps one PNG per scene.
func auditSweep(t *testing.T, config *Config, prefix string, renderer *sdl.Renderer, frame *probeFrame) {
	t.Helper()

	prevConfig, prevScene := appConfig, currentSceneIndex
	appConfig = config
	t.Cleanup(func() { appConfig, currentSceneIndex = prevConfig, prevScene })

	for i := range config.Scenes {
		scene := config.Scenes[i]
		currentSceneIndex = i
		menuTilesApply(config, i)
		scene = config.Scenes[i]

		renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
		renderer.Clear()
		renderScene(renderer, config, scene)
		auditSave(t, renderer, frame, fmt.Sprintf("%s%02d_%s", prefix, i, scene.Name))
	}
}

func TestUIAudit(t *testing.T) {
	if os.Getenv("JUKAHUB_UI_AUDIT") == "" {
		t.Skip("visual audit; run with JUKAHUB_UI_AUDIT=1")
	}
	// probeW/probeH and screenWidth/screenHeight stay at 720p defaults, which the
	// device config is authored for.

	configs := loadAuditConfigs(t)
	config := configs["trimui/jukaconfig.device.json"]
	if config == nil {
		t.Fatalf("the handheld config ships in player/trimui; it could not be loaded")
	}

	renderer := newKeyboardTestRenderer(t)
	frame := newProbeFrame(t)
	auditSweep(t, config, "idle_", renderer, frame)

	// Staged work on a handful of representative scenes.
	prevConfig, prevScene := appConfig, currentSceneIndex
	appConfig = config
	t.Cleanup(func() { appConfig, currentSceneIndex = prevConfig, prevScene })

	for _, name := range []string{"Game Library", "Terminal", "Home"} {
		idx := sceneIndexByName(config, name)
		if idx < 0 {
			continue
		}
		unset := auditClear(t)
		currentSceneIndex = idx
		menuTilesApply(config, idx)
		auditStage()
		scene := config.Scenes[idx]
		renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
		renderer.Clear()
		renderScene(renderer, config, scene)
		// The main loop composes overlays over the scene; mirror the tail so the
		// chips sit exactly where the app puts them.
		if virtualKeyboardActive {
			renderKeyboard(renderer, config)
		}
		renderToast(renderer, config)
		renderInstallBar(renderer, config)
		renderActivityStrip(renderer, config)
		auditSave(t, renderer, frame, fmt.Sprintf("strip_%s", name))

		// And again with the installer card up, alongside/parked-below it.
		unset2 := auditClear(t)
		installBarBeginLabelled("cores", "Updating emulator cores")
		installBarPhase(installPhaseDownload)
		installBarProgress(55, 100)
		downloads.mu.Lock()
		downloads.items = []*downloadItem{
			{ID: 1, Status: downloadQueued, Title: "queued.mp4", URL: "https://example.invalid/queued"},
		}
		downloads.mu.Unlock()
		renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
		renderer.Clear()
		renderScene(renderer, config, scene)
		renderInstallBar(renderer, config)
		renderActivityStrip(renderer, config)
		auditSave(t, renderer, frame, fmt.Sprintf("strip_card_%s", name))
		unset2()
		unset()
	}
	_ = renderer
}

// TestUIAuditCompact renders the same sweep on the Brick's 960x720 compact
// canvas, which is where the strip's parking logic and the small header differ.
// It runs in its own go test process so the probe window is created at 960x720.
func TestUIAuditCompact(t *testing.T) {
	if os.Getenv("JUKAHUB_UI_AUDIT_COMPACT") == "" {
		t.Skip("visual audit; run with JUKAHUB_UI_AUDIT_COMPACT=1")
	}
	configs := loadAuditConfigs(t)
	config := configs["trimui/jukaconfig.device.json"]
	if config == nil {
		t.Fatalf("the handheld config could not be loaded")
	}
	applyUIProfileForDevice(config, DeviceTrimuiBrick)
	withScreen(t, int32(config.Width), int32(config.Height))

	renderer := newKeyboardTestRenderer(t)
	frame := newProbeFrame(t)
	auditSweep(t, config, "compact_", renderer, frame)

	prevConfig, prevScene := appConfig, currentSceneIndex
	appConfig = config
	t.Cleanup(func() { appConfig, currentSceneIndex = prevConfig, prevScene })

	for _, name := range []string{"Tube", "Main"} {
		idx := sceneIndexByName(config, name)
		if idx < 0 {
			continue
		}
		unset := auditClear(t)
		currentSceneIndex = idx
		menuTilesApply(config, idx)
		auditStage()
		scene := config.Scenes[idx]
		renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
		renderer.Clear()
		renderScene(renderer, config, scene)
		renderInstallBar(renderer, config)
		renderActivityStrip(renderer, config)
		auditSave(t, renderer, frame, fmt.Sprintf("compact_strip_%s", name))
		unset()

		// And the parked variant: on the compact canvas the card's right half
		// reaches the strip's column, so the chips must sit below the card.
		unsetCard := auditClear(t)
		installBarBeginLabelled("cores", "Updating emulator cores")
		installBarPhase(installPhaseDownload)
		installBarProgress(55, 100)
		downloads.mu.Lock()
		downloads.items = []*downloadItem{
			{ID: 1, Status: downloadQueued, Title: "queued.mp4", URL: "https://example.invalid/queued"},
		}
		downloads.mu.Unlock()
		renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
		renderer.Clear()
		renderScene(renderer, config, scene)
		renderInstallBar(renderer, config)
		renderActivityStrip(renderer, config)
		auditSave(t, renderer, frame, fmt.Sprintf("compact_strip_card_%s", name))
		unsetCard()
	}
}

func sceneIndexByName(config *Config, name string) int {
	for i := range config.Scenes {
		if config.Scenes[i].Name == name {
			return i
		}
	}
	return -1
}
