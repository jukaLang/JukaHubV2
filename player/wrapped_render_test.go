package main

// The insights card is verified on pixels, not just on geometry: the stat rows
// and the grid share one 238px card, so the failure mode is text bleeding out of
// it or a grid that silently draws nothing. This renders the real Playtime scene
// offscreen (SDL dummy driver, software renderer) and counts what landed where.

import (
	"os"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// renderPlaytimeProbe draws the real Playtime screen over a flat background with
// the given snapshot installed and returns the captured frame.
func renderPlaytimeProbe(t *testing.T, data playtimeData) *probeFrame {
	t.Helper()
	if _, err := os.Stat("jukaconfig.json"); err != nil {
		t.Fatalf("jukaconfig.json ships next to the tests: %v", err)
	}
	config, _, err := LoadXMLConfigWithFallback("jukaconfig.json")
	cancelPendingUserConfigSave(t)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	withScreen(t, probeW, probeH)
	ClearTextCache()
	t.Cleanup(ClearTextCache)
	// The accent is a package token the app fills in from the theme at startup;
	// unset, the grid would be drawn black and every pixel check would be
	// meaningless for reasons that have nothing to do with the heatmap.
	prevAccent := accentColor
	SetAccentColor(ColorAccent)
	t.Cleanup(func() { SetAccentColor(prevAccent) })

	prevData, prevLoaded := playtime.data, playtime.loaded
	t.Cleanup(func() { playtime.data, playtime.loaded = prevData, prevLoaded })
	playtime.data = data
	playtime.loaded = true

	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderPlaytime(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v", err)
	}
	return f
}

// accentPixelsIn counts the grid's lit cells. Accent is cyan (high green/blue,
// low red) while the empty-cell track is a dim blue-grey, so the blue-minus-red
// difference separates "day played" from "day empty" without depending on the
// exact theme accent.
func accentPixelsIn(f *probeFrame, r sdl.Rect) int {
	n := 0
	for y := r.Y; y < r.Y+r.H && y < probeH; y++ {
		for x := r.X; x < r.X+r.W && x < probeW; x++ {
			c := f.at(x, y)
			if int(c.B)-int(c.R) > 90 {
				n++
			}
		}
	}
	return n
}

// fullWindowData fills every day the grid can show with play.
func fullWindowData(now time.Time) playtimeData {
	data := newPlaytimeData()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for i := 0; i < wrappedDays; i++ {
		data.Days[today.AddDate(0, 0, -i).Format(wrappedDayKey)] = 3600
	}
	return data
}

// A played window must light the grid, an empty one must stay on the track, and
// the grid must sit inside the card instead of running off it.
func TestPlaytimeInsightsGridRendersPlayedDays(t *testing.T) {
	const (
		cardX, cardY = int32(504), int32(194)
		cardW, cardH = int32(748), int32(238)
	)
	now := time.Now()

	played := renderPlaytimeProbe(t, fullWindowData(now))
	heat := playtimeHeatRect
	if heat.W <= 0 || heat.H <= 0 {
		t.Fatal("the insights card drew no grid")
	}
	if heat.X < cardX || heat.Y < cardY || heat.X+heat.W > cardX+cardW || heat.Y+heat.H > cardY+cardH {
		t.Fatalf("grid %+v escapes its card %+v", heat,
			sdl.Rect{X: cardX, Y: cardY, W: cardW, H: cardH})
	}
	lit := accentPixelsIn(played, heat)
	if lit < 500 {
		t.Fatalf("a fully played window lit only %d grid pixels", lit)
	}

	// Nothing in the gap between this card and the achievements card below it:
	// that band is what content overflow looked like the last time a card grew.
	gap := sdl.Rect{X: cardX, Y: cardY + cardH, W: cardW, H: 16}
	for y := gap.Y; y < gap.Y+gap.H; y++ {
		for x := gap.X; x < gap.X+gap.W; x++ {
			c := played.at(x, y)
			if int(c.R)+int(c.G)+int(c.B) > 420 {
				t.Fatalf("bright pixel %+v at (%d,%d) in the gap under the insights card", c, x, y)
			}
		}
	}
}

func TestPlaytimeInsightsGridStaysEmptyWithoutPlay(t *testing.T) {
	empty := renderPlaytimeProbe(t, newPlaytimeData())
	heat := playtimeHeatRect
	if heat.W <= 0 {
		t.Fatal("the insights card drew no grid")
	}
	if got := accentPixelsIn(empty, heat); got != 0 {
		t.Fatalf("an empty window lit %d grid pixels, want the plain track", got)
	}
}
