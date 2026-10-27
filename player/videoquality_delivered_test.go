package main

// videoquality_delivered_test.go — the Quality row names the *ceiling* (Auto
// 720p, or a pin for this video). A ceiling is a request: YouTube happily hands
// over a 480p rendition when no 720p one exists, and the row then described a
// stream that was never on screen. The row now also names the resolution the
// player actually got, when the two differ, so a capped stream is visible
// instead of implied.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// withPlayingStream parks the player in a state that reports one delivered
// height, and restores whatever the previous test left behind.
func withPlayingStream(t *testing.T, phase string, sourceHeight int32) {
	t.Helper()
	prevPhase, prevSrcH := embeddedPlayer.phase, embeddedPlayer.sourceHeight
	embeddedPlayer.mu.Lock()
	embeddedPlayer.phase = phase
	embeddedPlayer.sourceHeight = sourceHeight
	embeddedPlayer.mu.Unlock()
	t.Cleanup(func() {
		embeddedPlayer.mu.Lock()
		embeddedPlayer.phase = prevPhase
		embeddedPlayer.sourceHeight = prevSrcH
		embeddedPlayer.mu.Unlock()
	})
}

func deliveredTestConfig(t *testing.T, resolution string) *Config {
	t.Helper()
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	config.Variables.PlaybackResolution = resolution
	prevURL, prevConfig := currentPlaybackURL, appConfig
	currentPlaybackURL, appConfig = "https://example.test/watch?v=1", config
	setVideoQualityOverride(currentPlaybackURL, 0)
	t.Cleanup(func() {
		currentPlaybackURL, appConfig = prevURL, prevConfig
		setVideoQualityOverride(currentPlaybackURL, 0)
	})
	return config
}

// TestDeliveredQualityRowNamesACappedStream: the row carries the delivered
// height, and only when it is not the ceiling.
func TestDeliveredQualityRowNamesACappedStream(t *testing.T) {
	config := deliveredTestConfig(t, "720")

	withPlayingStream(t, "playing", 480)
	if got := videoQualityRowLabel(config); got != "Auto (720p) · got 480p" {
		t.Fatalf("row = %q, want the delivered 480p beside the 720p ceiling", got)
	}

	// The ceiling was met: repeating it would be noise.
	withPlayingStream(t, "playing", 720)
	if got := videoQualityRowLabel(config); got != "Auto (720p)" {
		t.Fatalf("row = %q, want the plain ceiling when it was met", got)
	}

	// Paused is still the stream on screen.
	withPlayingStream(t, "paused", 360)
	if got := videoQualityRowLabel(config); got != "Auto (720p) · got 360p" {
		t.Fatalf("row while paused = %q, want got 360p", got)
	}

	// Nothing playing: no claim about a picture that is not there.
	withPlayingStream(t, "idle", 480)
	if got := videoQualityRowLabel(config); got != "Auto (720p)" {
		t.Fatalf("row while idle = %q, want the plain ceiling", got)
	}

	// A per-video pin moves the ceiling the delivered figure is compared against.
	withPlayingStream(t, "playing", 720)
	setVideoQualityOverride(currentPlaybackURL, 1080)
	if got := videoQualityRowLabel(config); got != "1080p (this video) · got 720p" {
		t.Fatalf("row under a 1080p pin = %q, want got 720p", got)
	}
	// The pin met, so the row is the pin alone.
	withPlayingStream(t, "playing", 1080)
	if got := videoQualityRowLabel(config); got != "1080p (this video)" {
		t.Fatalf("row with the pin met = %q, want the plain pin", got)
	}
	setVideoQualityOverride(currentPlaybackURL, 0)
}

// TestQualityRowFitsTheShippingCanvases: the sheet has to hold its longest row
// on both canvases — 1280x720 for the Smart Pro and 960x720 for the Brick — and
// stay on screen.
func TestQualityRowFitsTheShippingCanvases(t *testing.T) {
	config := deliveredTestConfig(t, "720")
	_ = newProbeFrame(t)

	canvases := []struct{ w, h int32 }{
		{uiCanvasWideWidth, uiCanvasWideHeight},
		{uiCanvasCompactW, uiCanvasCompactH},
		{uiNativeBrickWidth, uiNativeBrickHeight},
	}
	// The widest row the sheet can ever be asked to draw.
	const widest = "Quality: 1080p (this video) · got 720p"

	font, _ := getCachedFont(config, "small")
	if font == nil {
		t.Fatal("no small font for the measurement")
	}
	wantW, _, _ := font.SizeUTF8(widest)
	for _, canvas := range canvases {
		sheet := videoSettingsSheetRect(0, 0, canvas.w, canvas.h)
		if sheet.X < 0 || sheet.Y < 0 || sheet.X+sheet.W > canvas.w || sheet.Y+sheet.H > canvas.h {
			t.Errorf("sheet %+v does not fit the %dx%d canvas", sheet, canvas.w, canvas.h)
		}
		rowW := sheet.W - 20
		if int32(wantW) > rowW-28 {
			t.Errorf("%q is %dpx, wider than the row's %dpx of text space", widest, wantW, rowW-28)
		}
	}
}

// TestQualityRowDrawsTheDeliveredValue: the sheet has to paint that number, and
// paint all of it — a formula that fits on paper but is clipped or ellipsized on
// the panel is not showing anything.
func TestQualityRowDrawsTheDeliveredValue(t *testing.T) {
	config := deliveredTestConfig(t, "720")
	withScreen(t, probeW, probeH)
	_ = newProbeFrame(t)

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("offscreen renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)

	prevItem, prevFullscreen := videoSettingsItem, videoFullscreen
	videoSettingsItem, videoFullscreen = 0, false
	t.Cleanup(func() { videoSettingsItem, videoFullscreen = prevItem, prevFullscreen })

	sheet := videoSettingsSheetRect(80, 60, probeW-160, probeH-120)
	rowY := sheet.Y + videoSettingsTopPad + 3*videoSettingsRowH
	row := sdl.Rect{X: sheet.X + 10, Y: rowY, W: sheet.W - 20, H: videoSettingsRowH - 6}

	// rightmostText is how far right the row's glyphs reach: the label is drawn
	// in the primary color, so its extent is measurable without knowing the words.
	rightmostText := func() int32 {
		f := &probeFrame{buf: make([]byte, probeW*probeH*4), bg: sdl.Color{R: 9, G: 14, B: 25, A: 255}}
		renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
		renderer.Clear()
		drawVideoSettingsSheet(renderer, config, 80, 60, probeW-160, probeH-120)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
			t.Fatalf("ReadPixels: %v", err)
		}
		right := int32(-1)
		col := ColorTextPrimary()
		for y := row.Y; y < row.Y+row.H; y++ {
			for x := row.X; x < row.X+row.W; x++ {
				if f.at(x, y) == col && x > right {
					right = x
				}
			}
		}
		if right < 0 {
			t.Fatal("the Quality row drew no text at all")
		}
		return right
	}

	withPlayingStream(t, "playing", 720)
	met := rightmostText()

	withPlayingStream(t, "playing", 480)
	capped := rightmostText()

	if capped-met < 80 {
		t.Fatalf("capped row ends at x=%d, met row at x=%d: the delivered 480p was not drawn", capped, met)
	}
	if capped > row.X+row.W-28 {
		t.Fatalf("the Quality row reaches x=%d, past its text limit x=%d — it is being clipped", capped, row.X+row.W-28)
	}
}
