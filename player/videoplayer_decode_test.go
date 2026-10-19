package main

// Integration check for the playback path that replaced ffplay: ffmpeg decodes
// a real file into RGBA frames, the player uploads them to an SDL texture and
// the overlay draws them. This is what runs on the TrimUI now that the bundled
// ffplay (X11-only) is no longer launched.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func makeTestClip(t *testing.T, ffmpeg, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "clip.mp4")
	cmd := exec.Command(ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "2", "-pix_fmt", "yuv420p", "-c:v", "mpeg4", "-c:a", "aac",
		out)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build a test clip (%v): %s", err, data)
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		t.Skipf("ffmpeg produced no clip: %v", err)
	}
	return out
}

// findLocalTool locates a helper next to the sources (required/) or on PATH.
// getToolPath cannot be used here: the test binary runs from Go's build cache,
// so the platform tools directory is not the repository's.
func findLocalTool(name string) string {
	exe := name
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	for _, candidate := range []string{filepath.Join("required", exe), exe} {
		if p, err := exec.LookPath(candidate); err == nil {
			if abs, err := filepath.Abs(p); err == nil {
				return abs
			}
			return p
		}
	}
	return ""
}

func TestEmbeddedPlayerDecodesFrames(t *testing.T) {
	ffmpeg := findLocalTool("ffmpeg")
	ffprobe := findLocalTool("ffprobe")
	if ffmpeg == "" || ffprobe == "" {
		t.Skip("ffmpeg/ffprobe are not installed on this machine")
	}

	f := newProbeFrame(t)
	if err := sdl.InitSubSystem(sdl.INIT_AUDIO); err != nil {
		t.Logf("audio subsystem unavailable, video-only run: %v", err)
	}
	t.Cleanup(func() {
		StopEmbeddedPlayback()
		sdl.QuitSubSystem(sdl.INIT_AUDIO)
	})

	// A real download can be passed in for a manual end-to-end check:
	//   JUKAHUB_TEST_VIDEO=/path/to/video.mp4 go test -run TestEmbeddedPlayerDecodesFrames
	clip := os.Getenv("JUKAHUB_TEST_VIDEO")
	if clip == "" {
		clip = makeTestClip(t, ffmpeg, t.TempDir())
	}
	config := &Config{}
	config.Variables.ToolsPath = filepath.Dir(ffmpeg)
	// Stopping playback persists a resume position, and saveUserConfig writes
	// jukauser.json in the working directory. Run from a temporary directory
	// so the developer's real config is never touched by the test.
	t.Chdir(t.TempDir())

	w, h, dur, fps := probeVideoInfo(ffprobe, clip)
	if w <= 0 || h <= 0 {
		t.Fatalf("probe = %dx%d, want a valid frame size", w, h)
	}
	if dur <= 0 || fps <= 0 {
		t.Fatalf("probe duration/fps = %.1f/%.1f, want positive values", dur, fps)
	}

	startEmbeddedPlayback(config, clip, 0, false)

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		embeddedPlayer.mu.Lock()
		seq, phase := embeddedPlayer.frameSeq, embeddedPlayer.phase
		embeddedPlayer.mu.Unlock()
		if seq > 0 && phase == "playing" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	embeddedPlayer.mu.Lock()
	seq := embeddedPlayer.frameSeq
	phase := embeddedPlayer.phase
	gotW, gotH := embeddedPlayer.width, embeddedPlayer.height
	embeddedPlayer.mu.Unlock()
	if seq == 0 || phase != "playing" {
		t.Fatalf("decoder produced no frames (phase=%q seq=%d)", phase, seq)
	}
	if gotW != w || gotH != h {
		t.Fatalf("decoder size = %dx%d, want %dx%d", gotW, gotH, w, h)
	}

	// The first decoded frame must reach the renderer as a texture.
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()
	ClearTextCache()
	UpdateCurrentTexture(renderer)

	embeddedPlayer.mu.Lock()
	tex := embeddedPlayer.current
	hasRaw := len(embeddedPlayer.currentRaw) == int(w*h*4)
	embeddedPlayer.mu.Unlock()
	if tex == nil {
		t.Fatal("decoded frame was not uploaded to a texture")
	}
	if !hasRaw {
		t.Fatal("decoded frame buffer has the wrong size")
	}

	// And it must paint the panel rect, not leave the background untouched.
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderer.Copy(tex, nil, &sdl.Rect{X: 0, Y: 0, W: probeW, H: probeH})
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	changed := 0
	for y := int32(100); y < 300; y += 4 {
		for x := int32(100); x < 400; x += 4 {
			if !f.isBG(x, y) {
				changed++
			}
		}
	}
	if changed < 500 {
		t.Fatalf("only %d sampled pixels changed; the frame was not drawn", changed)
	}

	StopEmbeddedPlayback()
	// The decoder goroutine outlives Stop for a moment. Let it exit before the
	// software renderer is destroyed: a later test creates its own renderer on
	// the same window, and a still-running ffmpeg reader made that swap flaky.
	if !waitForVideoDecoders(3 * time.Second) {
		t.Error("decoder goroutine did not exit after StopEmbeddedPlayback")
	}
	// Drop the last texture before the renderer that owns it is destroyed;
	// leaving it in the global player state gives the next test a dangling
	// SDL_Texture to overwrite.
	embeddedPlayer.mu.Lock()
	tex = embeddedPlayer.current
	embeddedPlayer.current = nil
	embeddedPlayer.uploadedSeq = 0
	embeddedPlayer.mu.Unlock()
	if tex != nil {
		tex.Destroy()
	}
	if got := playerPhase(); got != "idle" {
		t.Fatalf("phase after stop = %q, want idle", got)
	}
}
