package main

// iconbutton_test.go covers the shared icon-chip component: the surface, the
// outside focus ring and the corner badge are measured on real pixels, and the
// notification bell is checked to draw through it.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func TestIconButtonPaintsSurfaceFocusRingAndBadge(t *testing.T) {
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	withScreen(t, probeW, probeH)
	ClearTextCache()
	t.Cleanup(ClearTextCache)

	font, _ := getCachedFont(&Config{}, "small")
	if font == nil {
		t.Fatal("the committed player font did not load")
	}

	accent := sdl.Color{R: 90, G: 210, B: 255, A: 255}
	rect := sdl.Rect{X: 400, Y: 200, W: 32, H: 32}
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	got := UIIconButtonDraw(renderer, UIIconButton{
		Rect: rect,
		Style: UIIconButtonStyle{
			Surface: ColorIconSurface,
			Ring:    accent,
			Icon:    ColorTextPrimary(),
			Glyph: func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
				fillCircle(r, box.X+box.W/2, box.Y+box.H/2, 4, tint)
			},
		},
		State:     UIIconButtonState{Focused: true},
		Badge:     "3",
		BadgeFont: font,
	})
	if got != rect {
		t.Fatalf("UIIconButtonDraw returned %+v, want the caller's rect %+v", got, rect)
	}
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}

	// The pill itself is the theme's input surface.
	if c := f.at(rect.X+6, rect.Y+rect.H/2); c != ColorIconSurface {
		t.Fatalf("pill interior = %+v, want the icon surface %+v", c, ColorIconSurface)
	}
	// The focus ring is drawn outside the chip, on its own edge.
	if c := f.at(rect.X-1, rect.Y+rect.H/2); c != accent {
		t.Fatalf("focus ring at x-1 = %+v, want the accent %+v", c, accent)
	}
	// The unread count hangs on the top-right corner.
	badge := uiIconButtonBadgeRect(font, rect, "3", 0, 0)
	if badge.W <= 0 || badge.H <= 0 {
		t.Fatalf("badge rect = %+v, want a measured pill", badge)
	}
	// The badge fill is translucent, so its readback is the blend and not the
	// literal recipe colour; count the red pixels inside the badge instead.
	painted := 0
	for y := badge.Y; y < badge.Y+badge.H; y++ {
		for x := badge.X; x < badge.X+badge.W; x++ {
			if c := f.at(x, y); c.R > 150 && c.G < 110 && c.B < 110 {
				painted++
			}
		}
	}
	if painted == 0 {
		t.Fatalf("badge %+v painted no count pill", badge)
	}
}

func TestNotifBellDrawsThroughTheSharedIconButton(t *testing.T) {
	resetNotifCenter(t)
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	withScreen(t, probeW, probeH)
	ClearTextCache()
	t.Cleanup(ClearTextCache)

	config := &Config{}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		t.Fatal("the committed player font did not load")
	}
	if _, h := notifBellSize(config); h <= 0 {
		t.Fatal("notifBellSize did not measure the chip")
	}
	rect := sdl.Rect{X: 1200, Y: 10, W: 32, H: 32}
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	drawNotifBell(renderer, config, font, rect)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
	if nc.bellRect != rect {
		t.Fatalf("bell rect = %+v, want %+v", nc.bellRect, rect)
	}
	if c := f.at(rect.X+4, rect.Y+rect.H/2); c != ColorIconSurface {
		t.Fatalf("bell pill interior = %+v, want the icon surface %+v", c, ColorIconSurface)
	}
}
