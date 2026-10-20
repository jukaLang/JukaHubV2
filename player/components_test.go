package main

// Render-level checks for the shared component primitives: each one must put
// pixels where its contract says, so scenes can trust them without snapshot
// tests of their own.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// componentFrame renders a draw callback over a flat background and captures
// the result.
func componentFrame(t *testing.T, draw func(*sdl.Renderer)) *probeFrame {
	t.Helper()
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()
	withScreen(t, probeW, probeH)
	ClearTextCache()
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	draw(renderer)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}
	return f
}

func TestUIProgressBarFillsFraction(t *testing.T) {
	fill := sdl.Color{R: 200, G: 60, B: 60, A: 255}
	track := sdl.Color{R: 40, G: 44, B: 52, A: 255}
	f := componentFrame(t, func(r *sdl.Renderer) {
		UIProgressBar(r, sdl.Rect{X: 100, Y: 100, W: 200, H: 12}, 0.5, track, fill)
	})
	if got := f.at(150, 106); got.R < 180 || got.G > 90 {
		t.Errorf("progress fill pixel = %+v, want the fill colour", got)
	}
	if got := f.at(250, 106); got.R > 60 {
		t.Errorf("progress track pixel = %+v, want the track colour", got)
	}
	// Clamping: a fraction over 1 fills the whole bar.
	f = componentFrame(t, func(r *sdl.Renderer) {
		UIProgressBar(r, sdl.Rect{X: 100, Y: 100, W: 200, H: 12}, 4, track, fill)
	})
	if got := f.at(296, 106); got.R < 180 {
		t.Errorf("clamped progress left the right edge empty: %+v", got)
	}
}

func TestUICardDrawsSurface(t *testing.T) {
	fill := sdl.Color{R: 12, G: 90, B: 160, A: 255}
	f := componentFrame(t, func(r *sdl.Renderer) {
		UICard(r, sdl.Rect{X: 60, Y: 60, W: 300, H: 180}, UICardStyle{
			Fill:   fill,
			Border: ColorBorder,
			Radius: RadiusLG,
		})
	})
	if got := f.at(210, 150); got.B < 140 || got.R > 40 {
		t.Errorf("card centre = %+v, want the fill colour", got)
	}
	if f.at(210, 60) == f.bg {
		t.Error("card border was not drawn along the top edge")
	}
}

func TestUIButtonDrawsSharedStates(t *testing.T) {
	config := &Config{}
	font := FontForRole(config, TypeBody)
	if font == nil {
		t.Skip("body font unavailable")
	}
	rect := sdl.Rect{X: 100, Y: 100, W: 200, H: 60}
	fill := sdl.Color{R: 20, G: 120, B: 220, A: 255}
	rest := componentFrame(t, func(r *sdl.Renderer) {
		UIButton(r, config, rect, "Save", UIButtonStyle{Variant: UIButtonCustom, Fill: fill, Font: font, Radius: RadiusMD}, UIButtonState{})
	})
	if got := rest.at(110, 130); got.B < 180 || got.R > 60 {
		t.Errorf("resting button pixel = %+v, want the custom fill", got)
	}
	// The label must sit on top of the fill. contrastText picks dark ink on
	// this bright blue, so count pixels darker than both the fill and shadow.
	ink := 0
	for y := int32(110); y < 150; y++ {
		for x := int32(120); x < 280; x++ {
			if got := rest.at(x, y); got.R < 90 && got.G < 90 && got.B < 90 {
				ink++
			}
		}
	}
	if ink < 20 {
		t.Errorf("button label drew only %d ink pixels", ink)
	}

	focused := componentFrame(t, func(r *sdl.Renderer) {
		UIButton(r, config, rect, "Save", UIButtonStyle{
			Variant:   UIButtonCustom,
			Fill:      fill,
			FocusFill: sdl.Color{R: 220, G: 60, B: 60, A: 255},
			Font:      font,
			Radius:    RadiusMD,
		}, UIButtonState{Focused: true})
	})
	if got := focused.at(110, 130); got.R < 180 || got.B > 120 {
		t.Errorf("focused button pixel = %+v, want the focus fill", got)
	}
	if focused.at(rect.X-FocusRing, 130) == focused.bg {
		t.Error("focused button did not draw its outer focus ring")
	}

	disabled := componentFrame(t, func(r *sdl.Renderer) {
		UIButton(r, config, rect, "Save", UIButtonStyle{Variant: UIButtonCustom, Fill: fill, Font: font, Radius: RadiusMD}, UIButtonState{Disabled: true})
	})
	if disabled.at(110, 130) == rest.at(110, 130) {
		t.Error("disabled button looks identical to its resting state")
	}

	pressed := componentFrame(t, func(r *sdl.Renderer) {
		UIButton(r, config, rect, "Save", UIButtonStyle{Variant: UIButtonCustom, Fill: fill, Font: font, Radius: RadiusMD}, UIButtonState{Pressed: true})
	})
	if pressed.at(110, 130).B >= rest.at(110, 130).B {
		t.Error("pressed button was not darkened")
	}
}

func TestUIBadgeMeasuresAndDraws(t *testing.T) {
	config := &Config{}
	w, h := UIBadgeSize(config, "3")
	if w <= 0 || h <= 0 {
		t.Fatalf("badge size = %dx%d", w, h)
	}
	if emptyW, _ := UIBadgeSize(config, ""); emptyW != 0 {
		t.Errorf("empty badge width = %d, want 0", emptyW)
	}
	f := componentFrame(t, func(r *sdl.Renderer) {
		UIBadge(r, config, 100, 100, "3", sdl.Color{R: 220, G: 60, B: 60, A: 255}, sdl.Color{R: 255, G: 255, B: 255, A: 255})
	})
	// The pill's middle is red regardless of the digit glyph on top.
	if got := f.at(100+4, 100+h/2); got.R < 150 {
		t.Errorf("badge pixel = %+v, want the badge background", got)
	}
}

func TestUIEmptyStateDrawsText(t *testing.T) {
	config := &Config{}
	f := componentFrame(t, func(r *sdl.Renderer) {
		UIEmptyState(r, config, sdl.Rect{X: 100, Y: 100, W: 400, H: 220}, "(*)", "Nothing here", "Check back later.")
	})
	ink := 0
	for y := int32(100); y < 320; y++ {
		for x := int32(100); x < 500; x++ {
			if !f.isBG(x, y) {
				ink++
			}
		}
	}
	if ink < 40 {
		t.Errorf("empty-state block drew only %d non-background pixels", ink)
	}
}

func TestUILoadingSkeletonPulses(t *testing.T) {
	first := componentFrame(t, func(r *sdl.Renderer) {
		UILoadingSkeleton(r, sdl.Rect{X: 100, Y: 100, W: 300, H: 24}, 0)
	})
	second := componentFrame(t, func(r *sdl.Renderer) {
		UILoadingSkeleton(r, sdl.Rect{X: 100, Y: 100, W: 300, H: 24}, 0.25)
	})
	if first.at(250, 112) == second.at(250, 112) {
		t.Error("skeleton did not change with its phase")
	}
}
