package main

// renderpipeline_test.go — the startup "render pipeline" diagnostics from
// main.go. The point is to verify the snapshot the device build prints before
// the actual A133P run: the fields must come from SDL's real answer (video
// driver, renderer name, gpu/software split, max texture size), not from
// assumptions, and the structured log line must carry all four keys. The
// test renderer here is SDL's dummy video driver with the software renderer —
// the same backends the headless render probes use — which exercises exactly
// the fallback path the checklist item worries about on the device.

import (
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

var (
	pipelineOnce sync.Once
	pipelineWin  *sdl.Window
	pipelineErr  error
)

// pipelineRenderer builds a hidden window plus a software renderer the same
// way the offscreen render probes do, so the diagnostics run against a real
// SDL renderer with a guaranteed software classification.
func pipelineRenderer(t *testing.T) *sdl.Renderer {
	t.Helper()
	pipelineOnce.Do(func() {
		os.Setenv("SDL_VIDEODRIVER", "dummy")
		os.Setenv("SDL_AUDIODRIVER", "dummy")
		if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
			pipelineErr = err
			return
		}
		if err := ttf.Init(); err != nil {
			pipelineErr = err
			return
		}
		pipelineWin, pipelineErr = sdl.CreateWindow("renderpipeline-probe", 0, 0, 320, 240, sdl.WINDOW_HIDDEN)
	})
	if pipelineErr != nil {
		t.Skipf("SDL video unavailable: %v", pipelineErr)
	}
	renderer, err := sdl.CreateRenderer(pipelineWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	t.Cleanup(func() { destroyTestRenderer(renderer) })
	return renderer
}

func TestRenderPipelineInfoUnderSoftwareFallback(t *testing.T) {
	r := pipelineRenderer(t)
	p := renderPipelineInfo(r)
	if p.VideoDriver != "dummy" {
		t.Errorf("video_driver = %q, want the dummy driver SDL was told to use", p.VideoDriver)
	}
	if !strings.Contains(strings.ToLower(p.Renderer), "software") {
		t.Errorf("renderer = %q, want SDL's software renderer name", p.Renderer)
	}
	if p.Acceleration != "software" {
		t.Errorf("acceleration = %q, want \"software\" for the fallback path", p.Acceleration)
	}
	if !isWxH(p.MaxTexture) {
		t.Errorf("max_texture = %q, want a WxH SDL reported (0x0, SDL's no-limit answer under the dummy software fallback, is allowed)", p.MaxTexture)
	}
}

// isWxH accepts any digit-pair including 0x0: with the software renderer on
// the dummy video driver SDL reports 0x0 for max texture size (no limit),
// which is exactly the value the device build would show on a software
// fallback and therefore worth accepting rather than failing on.
func isWxH(s string) bool {
	parts := strings.Split(s, "x")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
		if len(p) > 1 && p[0] == '0' {
			return false
		}
	}
	return true
}

func TestRenderPipelineInfoToleratesNilRenderer(t *testing.T) {
	// A broken display stack must still name the video driver and default the
	// rest, so the device boot log stays a one-line diagnostic either way.
	p := renderPipelineInfo(nil)
	if p.VideoDriver == "" {
		t.Error("video_driver empty even without a renderer; the line must always say something")
	}
	if p.Acceleration != "unknown" || p.MaxTexture != "unknown" {
		t.Errorf("renderer facts without a renderer = %q/%q, want the unknown defaults", p.Acceleration, p.MaxTexture)
	}
}

func TestLogRenderPipelineCarriesAllFourKeys(t *testing.T) {
	r := pipelineRenderer(t)
	var buf strings.Builder

	// Log() caches its `logger` global, not slog's default, so swap that
	// variable itself and restore it afterwards.
	prev := logger
	logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	defer func() { logger = prev }()

	logRenderPipeline(r)

	line := buf.String()
	if line == "" {
		t.Fatal("log line empty; the startup snapshot must actually be emitted")
	}
	if !strings.Contains(line, "video_driver=dummy") {
		t.Errorf("missing video_driver=dummy in %q for the dummy-driver probe", line)
	}
	for _, key := range []string{"renderer=", "acceleration=software", "max_texture="} {
		if !strings.Contains(line, key) {
			t.Errorf("missing %q in the captured line %q", key, line)
		}
	}
	if !strings.Contains(line, "msg=") {
		t.Errorf("the message itself ('render pipeline') is missing from %q", line)
	}
}
