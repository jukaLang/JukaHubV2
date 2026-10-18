package main

// Regression probe for the video overlay. While a video plays, the transport
// controls (stop, rewind, play/pause, forward, speed, volume), the seek fill
// and the phase badge must be visible on both render paths: the cached static
// layer plus the live layer, and the all-in-one fallback. An earlier version
// baked the overlay into a screen-sized cache texture and never drew the live
// layer, so playback showed a dark shade scaled over the video with no
// buttons, no seek feedback and no way to pause or stop.

import (
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func overlayAbs(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// overlayCountNear counts pixels within tol (per channel) of c inside the box.
func overlayCountNear(f *probeFrame, c sdl.Color, x0, y0, x1, y1, tol int32) int {
	n := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			p := f.at(x, y)
			if overlayAbs(int32(p.R)-int32(c.R)) <= tol &&
				overlayAbs(int32(p.G)-int32(c.G)) <= tol &&
				overlayAbs(int32(p.B)-int32(c.B)) <= tol {
				n++
			}
		}
	}
	return n
}

// videoOverlayProbe renders RenderVideoOverlay for the Tube scene's video
// element and returns the captured pixels. With frame=true the decoder has
// produced a green "video frame"; with frame=false playback is still
// buffering and only the loader may appear inside the frame area.
// forceNoCache routes through the all-in-one fallback instead of the cached
// static layer.
func videoOverlayProbe(t *testing.T, forceNoCache, frame bool) *probeFrame {
	t.Helper()
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()
	// Text textures are bound to the renderer that created them; each probe
	// owns a fresh renderer, so start from an empty text cache.
	ClearTextCache()

	var tex *sdl.Texture
	if frame {
		// A solid green "video frame" so overlay pixels can never be
		// confused with the backdrop.
		tex, err = renderer.CreateTexture(sdl.PIXELFORMAT_RGB24, sdl.TEXTUREACCESS_STATIC, 4, 4)
		if err != nil {
			t.Skipf("texture unavailable: %v", err)
		}
		defer tex.Destroy()
		green := make([]byte, 4*4*3)
		for i := 0; i < len(green); i += 3 {
			green[i+1] = 255
		}
		if err := tex.Update(nil, unsafe.Pointer(&green[0]), 4*3); err != nil {
			t.Skipf("texture update unavailable: %v", err)
		}
	}

	DestroyVKVideoOverlayCache()
	prevBroken := vkVideoOverlayBroken
	vkVideoOverlayBroken = forceNoCache
	defer func() {
		DestroyVKVideoOverlayCache()
		vkVideoOverlayBroken = prevBroken
	}()

	// Play the middle of a long video from a known wall-clock start so the
	// progress recompute keeps the seek fill at ~50%.
	embeddedPlayer.mu.Lock()
	prevPhase := embeddedPlayer.phase
	prevProgress := embeddedPlayer.progress
	prevBase := embeddedPlayer.progressBase
	prevDuration := embeddedPlayer.duration
	prevVolume := embeddedPlayer.volume
	prevSpeed := embeddedPlayer.speed
	prevStart := embeddedPlayer.decoderStartTime
	prevPauseStart := embeddedPlayer.pauseStartTime
	prevPaused := embeddedPlayer.totalPausedDuration
	prevCurrent := embeddedPlayer.current
	prevRaw := embeddedPlayer.currentRaw
	embeddedPlayer.phase = "playing"
	embeddedPlayer.progress = 0.5
	embeddedPlayer.progressBase = 0.5
	embeddedPlayer.duration = 1000
	embeddedPlayer.volume = 0.8
	embeddedPlayer.speed = 1.0
	embeddedPlayer.decoderStartTime = time.Now()
	embeddedPlayer.pauseStartTime = time.Time{}
	embeddedPlayer.totalPausedDuration = 0
	embeddedPlayer.current = tex
	embeddedPlayer.currentRaw = nil
	embeddedPlayer.mu.Unlock()
	defer func() {
		embeddedPlayer.mu.Lock()
		embeddedPlayer.phase = prevPhase
		embeddedPlayer.progress = prevProgress
		embeddedPlayer.progressBase = prevBase
		embeddedPlayer.duration = prevDuration
		embeddedPlayer.volume = prevVolume
		embeddedPlayer.speed = prevSpeed
		embeddedPlayer.decoderStartTime = prevStart
		embeddedPlayer.pauseStartTime = prevPauseStart
		embeddedPlayer.totalPausedDuration = prevPaused
		embeddedPlayer.current = prevCurrent
		embeddedPlayer.currentRaw = prevRaw
		embeddedPlayer.mu.Unlock()
	}()

	elem := Element{Type: "video", X: 24, Y: 136, Width: "1232", Height: "520"}
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	RenderVideoOverlay(renderer, &Config{}, elem)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	return f
}

// assertVideoOverlayControls checks that the overlay chrome is really drawn:
// every transport button carries its glyph, the seek fill tracks progress and
// the phase badge shows its text.
func assertVideoOverlayControls(t *testing.T, f *probeFrame, label string) {
	t.Helper()
	glyph := ColorTextPrimary()
	rects := map[string]sdl.Rect{
		"stop":      videoControlRects.stop,
		"seekBack":  videoControlRects.seekBack,
		"playPause": videoControlRects.playPause,
		"seekFwd":   videoControlRects.seekFwd,
		"speed":     videoControlRects.speed,
		"volDown":   videoControlRects.volDown,
		"volUp":     videoControlRects.volUp,
	}
	for name, r := range rects {
		if r.W <= 0 || r.H <= 0 {
			t.Errorf("%s: %s button has no layout (rect %v)", label, name, r)
			continue
		}
		if overlayCountNear(f, glyph, r.X+2, r.Y+2, r.X+r.W-2, r.Y+r.H-2, 45) == 0 {
			t.Errorf("%s: %s button not drawn (no glyph pixels in %v)", label, name, r)
		}
	}

	// Seek fill: at 50% progress the track is filled accent up to the middle
	// and shows no accent past the thumb.
	seekBarY := int32(136) + int32(520) - 72 - 16
	seekBarX := int32(24) + 24
	seekBarW := int32(1232) - 48
	filled := f.at(seekBarX+seekBarW/4, seekBarY+3)
	if overlayAbs(int32(filled.R)-int32(ColorInfo.R)) > 30 ||
		overlayAbs(int32(filled.G)-int32(ColorInfo.G)) > 30 ||
		overlayAbs(int32(filled.B)-int32(ColorInfo.B)) > 30 {
		t.Errorf("%s: seek fill missing at 25%% of the track, got %+v want %+v", label, filled, ColorInfo)
	}
	if n := overlayCountNear(f, ColorInfo, seekBarX+seekBarW*3/4, seekBarY, seekBarX+seekBarW-2, seekBarY+6, 30); n != 0 {
		t.Errorf("%s: seek fill drawn past the thumb (%d pixels)", label, n)
	}

	// Phase badge text.
	if overlayCountNear(f, glyph, 24+12, 136+8, 24+220, 136+42, 60) == 0 {
		t.Errorf("%s: phase badge text not drawn", label)
	}
}

func TestVideoOverlayShowsTransportControls(t *testing.T) {
	for _, tc := range []struct {
		name    string
		noCache bool
	}{
		{"cached", false},
		{"fallback", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := vkVideoOverlayBuilds
			f := videoOverlayProbe(t, tc.noCache, true)
			if !tc.noCache && vkVideoOverlayBuilds == before {
				t.Logf("%s: renderer has no render-target support; cached branch degraded to fallback", tc.name)
			}
			assertVideoOverlayControls(t, f, tc.name)
		})
	}
}

func TestVideoOverlayShowsLoaderWhileBuffering(t *testing.T) {
	f := videoOverlayProbe(t, true, false)
	// The frame area is a flat dark backdrop plus the spinner ring around the
	// centre of the video element (24,136,1232,520).
	cx := int32(24) + 1232/2
	cy := int32(136) + 520/2
	if overlayCountNear(f, ColorTextPrimary(), cx-56, cy-56, cx+56, cy+56, 60) == 0 {
		t.Errorf("buffering spinner not drawn around (%d,%d)", cx, cy)
	}
}
