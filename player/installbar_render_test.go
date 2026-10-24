package main

// Offscreen rendering checks for the install overlay. These run with SDL's
// dummy video driver and the software renderer, so they verify the card's real
// pixels (position, size, accent bar coverage, no bleed outside the card)
// without a window or a network download.

import (
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

var (
	renderOnce sync.Once
	renderWin  *sdl.Window
	renderErr  error
)

// Frame size vars so probe tests can vary the resolution (all probes except
// the dumps use the default 1280x720).
var probeW = int32(1280)
var probeH = int32(720)

// probeFrame renders one frame of the install card over a known background and
// returns the RGBA buffer plus a pixel accessor.
type probeFrame struct {
	buf []byte
	bg  sdl.Color
}

func newProbeFrame(t *testing.T) *probeFrame {
	t.Helper()
	renderOnce.Do(func() {
		os.Setenv("SDL_VIDEODRIVER", "dummy")
		os.Setenv("SDL_AUDIODRIVER", "dummy")
		if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
			renderErr = err
			return
		}
		if err := ttf.Init(); err != nil {
			renderErr = err
			return
		}
		renderWin, renderErr = sdl.CreateWindow("installbar-probe", 0, 0, probeW, probeH, sdl.WINDOW_HIDDEN)
	})
	if renderErr != nil {
		t.Skipf("SDL video unavailable: %v", renderErr)
	}
	return &probeFrame{
		buf: make([]byte, probeW*probeH*4),
		bg:  sdl.Color{R: 9, G: 14, B: 25, A: 255},
	}
}

// renderInstallCard draws installBar over a flat background and captures the
// result. The returned frame owns the pixels.
func renderInstallCard(t *testing.T, f *probeFrame) *probeFrame {
	t.Helper()
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderInstallBar(renderer, &Config{})
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	return f
}

// at returns the colour at (x, y) in the captured frame (ARGB8888 is stored as
// B,G,R,A on little-endian hosts).
func (f *probeFrame) at(x, y int32) sdl.Color {
	i := (y*probeW + x) * 4
	return sdl.Color{B: f.buf[i], G: f.buf[i+1], R: f.buf[i+2], A: f.buf[i+3]}
}

func (f *probeFrame) isBG(x, y int32) bool {
	c := f.at(x, y)
	return c.R == f.bg.R && c.G == f.bg.G && c.B == f.bg.B
}

// bounds returns the bounding box of every pixel that differs from the
// background colour.
func (f *probeFrame) bounds() (x0, y0, x1, y1 int32, found bool) {
	x0, y0, x1, y1 = probeW, probeH, -1, -1
	for y := int32(0); y < probeH; y++ {
		for x := int32(0); x < probeW; x++ {
			if f.isBG(x, y) {
				continue
			}
			found = true
			if x < x0 {
				x0 = x
			}
			if y < y0 {
				y0 = y
			}
			if x > x1 {
				x1 = x
			}
			if y > y1 {
				y1 = y
			}
		}
	}
	return
}

// countColor counts exact matches for c inside the rect.
func (f *probeFrame) countColor(c sdl.Color, x0, y0, x1, y1 int32) int {
	n := 0
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			p := f.at(x, y)
			if p.R == c.R && p.G == c.G && p.B == c.B {
				n++
			}
		}
	}
	return n
}

// longestRun returns the longest horizontal run of exact colour c in a row.
func (f *probeFrame) longestRun(c sdl.Color, y, x0, x1 int32) (start, length int32) {
	run := int32(0)
	for x := x0; x <= x1; x++ {
		p := f.at(x, y)
		if p.R == c.R && p.G == c.G && p.B == c.B {
			run++
			if run > length {
				length = run
				start = x - run + 1
			}
		} else {
			run = 0
		}
	}
	return
}

func (f *probeFrame) mapCard(t *testing.T, box [4]int32) {
	t.Helper()
	const step = 4
	var out strings.Builder
	for y := box[1]; y <= box[3]; y += step {
		for x := box[0]; x <= box[2]; x += step {
			c := f.at(x, y)
			switch {
			case c.R == f.bg.R && c.G == f.bg.G && c.B == f.bg.B:
				out.WriteByte(' ')
			case c.R < 60 && c.G < 80 && c.B < 110:
				out.WriteByte('.')
			case c.B > 150 && c.G > 140:
				out.WriteByte('#')
			default:
				out.WriteByte('o')
			}
		}
		out.WriteByte('\n')
	}
	t.Logf("install card (%dx%d at %d,%d):\n%s", box[2]-box[0]+1, box[3]-box[1]+1, box[0], box[1], out.String())
}

// settleInstallBarAnimation parks the slide-in timer so frames are captured at
// the card's resting position.
func settleInstallBarAnimation() {
	installBar.mu.Lock()
	installBar.animAt = time.Now().Add(-2 * time.Second)
	installBar.mu.Unlock()
}

func TestRenderInstallBarPanelGeometry(t *testing.T) {
	defer resetInstallBar()
	prevW, prevH := screenWidth, screenHeight
	prevAccent := accentColor
	screenWidth, screenHeight = probeW, probeH
	accentColor = ColorAccent
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		accentColor = prevAccent
	})

	installBarBegin("ffmpeg")
	installBarPhase(installPhaseDownload)
	installBarProgress(50<<20, 100<<20)
	settleInstallBarAnimation()

	f := renderInstallCard(t, newProbeFrame(t))
	x0, y0, x1, y1, found := f.bounds()
	if !found {
		t.Fatal("install card rendered nothing")
	}
	w := x1 - x0 + 1
	h := y1 - y0 + 1
	// Card is 560 wide, centred; the shadow extends 2px right and 4px down.
	if w < 558 || w > 572 {
		t.Errorf("card+shadow width = %d, want ~560-566", w)
	}
	if h < 90 || h > 150 {
		t.Errorf("card+shadow height = %d, want 90..150", h)
	}
	if y0 != 70 {
		t.Errorf("card top = %d, want 70 (just below the 56px status bar)", y0)
	}
	center := x0 + w/2
	if center < probeW/2-3 || center > probeW/2+3 {
		t.Errorf("card centre x = %d, want %d", center, probeW/2)
	}

	// Accent tile: a solid accent square in the card's top-left corner.
	tileX, tileY := x0+22, y0+20
	if got := f.countColor(accentColor, tileX, tileY, tileX+30, tileY+30); got < 500 {
		t.Errorf("accent glyph tile has %d accent pixels, want >= 500 (42x42 tile)", got)
	}

	// Progress bar: 50% of the track should be accent, and it must not bleed
	// outside the card.
	fillStart, fillLen := int32(0), int32(0)
	for y := y0; y <= y1; y++ {
		s, l := f.longestRun(accentColor, y, 0, probeW-1)
		if l > fillLen {
			fillStart, fillLen = s, l
		}
	}
	if fillLen < 200 || fillLen > 280 {
		t.Errorf("progress fill run = %d px at x=%d, want ~232 px for 50%%", fillLen, fillStart)
	}
	if fillStart < x0+70 {
		t.Errorf("progress fill starts at x=%d, inside the glyph tile (card x=%d)", fillStart, x0)
	}
	f.mapCard(t, [4]int32{x0, y0, x1, y1})
}

func TestRenderInstallBarDoneAndErrorStates(t *testing.T) {
	defer resetInstallBar()
	prevW, prevH := screenWidth, screenHeight
	prevAccent := accentColor
	screenWidth, screenHeight = probeW, probeH
	accentColor = ColorAccent
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		accentColor = prevAccent
	})

	// Done: green tile, full bar.
	installBarBegin("ffplay")
	installBarPhase(installPhaseDownload)
	installBarProgress(10, 10)
	installBarDone()
	settleInstallBarAnimation()
	f := renderInstallCard(t, newProbeFrame(t))
	x0, y0, _, y1, found := f.bounds()
	if !found {
		t.Fatal("done card rendered nothing")
	}
	if got := f.countColor(ColorToastSuccess, x0+18, y0+16, x0+70, y0+70); got < 500 {
		t.Errorf("done tile has %d success pixels, want >= 500", got)
	}
	_, fillLen := int32(0), int32(0)
	for y := y0; y <= y1; y++ {
		if _, l := f.longestRun(ColorToastSuccess, y, 0, probeW-1); l > fillLen {
			fillLen = l
		}
	}
	if fillLen < 440 {
		t.Errorf("completed progress fill = %d px, want the full track (~464)", fillLen)
	}

	// Error: red tile, empty bar.
	resetInstallBar()
	installBarBegin("ffmpeg")
	installBarPhase(installPhaseDownload)
	installBarProgress(1<<20, 100<<20)
	installBarFail("download timed out")
	settleInstallBarAnimation()
	f = renderInstallCard(t, newProbeFrame(t))
	x0, y0, _, _, found = f.bounds()
	if !found {
		t.Fatal("error card rendered nothing")
	}
	if got := f.countColor(ColorToastError, x0+18, y0+16, x0+70, y0+70); got < 500 {
		t.Errorf("error tile has %d danger pixels, want >= 500", got)
	}
	if got := f.countColor(accentColor, x0+70, y0, probeW-1, probeH-1); got > 200 {
		t.Errorf("error state shows %d accent pixels; progress should be empty", got)
	}
}

func TestRenderInstallBarLegibleWithDarkAccent(t *testing.T) {
	defer resetInstallBar()
	prevW, prevH := screenWidth, screenHeight
	prevAccent := accentColor
	screenWidth, screenHeight = probeW, probeH
	// The slate theme's "info" colour is this dark; the card must not vanish.
	accentColor = sdl.Color{R: 30, G: 41, B: 59, A: 255}
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		accentColor = prevAccent
	})

	highlight := installHighlight()
	if math.Abs(colorLuma(highlight)-colorLuma(ColorPanelRaised)) < 55 {
		t.Fatalf("highlight %v is too close to the panel %v", highlight, ColorPanelRaised)
	}

	installBarBegin("ffmpeg")
	installBarPhase(installPhaseDownload)
	installBarProgress(25<<20, 100<<20)
	settleInstallBarAnimation()

	f := renderInstallCard(t, newProbeFrame(t))
	x0, y0, _, y1, found := f.bounds()
	if !found {
		t.Fatal("install card rendered nothing")
	}
	if n := f.countColor(highlight, x0+18, y0+16, x0+70, y0+70); n < 500 {
		t.Errorf("glyph tile has %d highlight pixels, want >= 500", n)
	}
	_, fillLen := int32(0), int32(0)
	for y := y0; y <= y1; y++ {
		if _, l := f.longestRun(highlight, y, 0, probeW-1); l > fillLen {
			fillLen = l
		}
	}
	if fillLen < 100 {
		t.Errorf("progress fill = %d px, want ~116 px for 25%% and visible", fillLen)
	}
}

func TestRenderInstallBarShimmerVisibleWithDarkAccent(t *testing.T) {
	defer resetInstallBar()
	prevW, prevH := screenWidth, screenHeight
	prevAccent := accentColor
	screenWidth, screenHeight = probeW, probeH
	accentColor = sdl.Color{R: 30, G: 41, B: 59, A: 255}
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		accentColor = prevAccent
	})

	// No Content-Length yet: the bar is indeterminate and must still read as
	// "working" even when the theme accent is nearly the panel colour.
	installBarBegin("ffmpeg")
	installBarPhase(installPhaseDownload)
	settleInstallBarAnimation()

	f := renderInstallCard(t, newProbeFrame(t))
	x0, y0, _, _, found := f.bounds()
	if !found {
		t.Fatal("install card rendered nothing")
	}
	titleFont, _ := getCachedFont(&Config{}, "medium")
	if titleFont == nil {
		t.Skip("no font available to compute the bar band")
	}
	barTop := y0 + 18 + int32(titleFont.Height()) + 16
	panelLuma := colorLuma(ColorPanelRaised)
	bright := 0
	for y := barTop; y < barTop+8; y++ {
		for x := x0 + 70; x < x0+540; x++ {
			if colorLuma(f.at(x, y)) > panelLuma+25 {
				bright++
			}
		}
	}
	if bright < 200 {
		t.Errorf("indeterminate bar has %d bright pixels, want >= 200 (shimmer invisible?)", bright)
	}
}

func TestRenderInstallBarHiddenWhenIdle(t *testing.T) {
	defer resetInstallBar()
	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = probeW, probeH
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })

	resetInstallBar()
	f := renderInstallCard(t, newProbeFrame(t))
	if _, _, _, _, found := f.bounds(); found {
		t.Errorf("idle state drew %s outside the background", describeNonBG(f))
	}
}

func describeNonBG(f *probeFrame) string {
	x0, y0, x1, y1, _ := f.bounds()
	return fmt.Sprintf("pixels in %d,%d..%d,%d", x0, y0, x1, y1)
}
