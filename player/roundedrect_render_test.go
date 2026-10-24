package main

// fillRoundedRect and strokeRoundedRect merge neighbouring scanlines into one
// rectangle so a panel fill costs ~30 renderer calls instead of one per pixel
// row. These checks render the real shapes offscreen and compare every pixel
// with the original one-scanline-at-a-time result, so the cheaper drawing path
// cannot quietly change the outline.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

type pixelMask map[[2]int32]bool

// referenceFillMask is the shape the per-scanline fill always produced.
func referenceFillMask(x, y, w, h, r int32) pixelMask {
	if r > w/2 {
		r = w / 2
	}
	if r > h/2 {
		r = h / 2
	}
	mask := pixelMask{}
	for dy := int32(0); dy < h; dy++ {
		var inset int32
		if dy < r {
			inset = roundedCornerInset(r, dy)
		} else if dy >= h-r {
			inset = roundedCornerInset(r, h-1-dy)
		}
		for px := x + inset; px < x+w-inset; px++ {
			mask[[2]int32{px, y + dy}] = true
		}
	}
	return mask
}

// referenceStrokeMask is the ring the per-scanline outline always produced.
func referenceStrokeMask(x, y, w, h, r, thickness int32) pixelMask {
	mask := pixelMask{}
	if r > w/2 {
		r = w / 2
	}
	if r > h/2 {
		r = h / 2
	}
	if thickness > r {
		thickness = r
	}
	innerR := r - thickness
	if innerR < 0 {
		innerR = 0
	}
	ih := h - 2*thickness
	for dy := int32(0); dy < h; dy++ {
		var oInset int32
		if dy < r {
			oInset = roundedCornerInset(r, dy)
		} else if dy >= h-r {
			oInset = roundedCornerInset(r, h-1-dy)
		}
		left, right := x+oInset, x+w-oInset
		if right <= left {
			continue
		}
		if dy >= thickness && dy < h-thickness && ih > 0 {
			id := dy - thickness
			var iInset int32
			if id < innerR {
				iInset = roundedCornerInset(innerR, id)
			} else if id >= ih-innerR {
				iInset = roundedCornerInset(innerR, ih-1-id)
			}
			innerLeft := x + thickness + iInset
			innerRight := x + w - thickness - iInset
			for px := left; px < innerLeft; px++ {
				mask[[2]int32{px, y + dy}] = true
			}
			for px := innerRight; px < right; px++ {
				mask[[2]int32{px, y + dy}] = true
			}
			continue
		}
		for px := left; px < right; px++ {
			mask[[2]int32{px, y + dy}] = true
		}
	}
	return mask
}

// renderShapeMask draws one shape over a flat background and returns the pixels
// it changed inside area.
func renderShapeMask(t *testing.T, area sdl.Rect, draw func(*sdl.Renderer)) pixelMask {
	t.Helper()
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	bg := sdl.Color{R: 7, G: 9, B: 13, A: 255}
	renderer.SetDrawColor(bg.R, bg.G, bg.B, 255)
	renderer.Clear()
	draw(renderer)

	buf := make([]byte, probeW*probeH*4)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	mask := pixelMask{}
	for y := area.Y; y < area.Y+area.H; y++ {
		for x := area.X; x < area.X+area.W; x++ {
			i := (y*probeW + x) * 4
			if buf[i] != bg.B || buf[i+1] != bg.G || buf[i+2] != bg.R {
				mask[[2]int32{x, y}] = true
			}
		}
	}
	return mask
}

// compareMasks reports the first pixel where got and want disagree.
func compareMasks(t *testing.T, name string, got, want pixelMask) {
	t.Helper()
	if len(want) == 0 {
		t.Fatalf("%s: reference shape is empty, the check would prove nothing", name)
	}
	for p := range want {
		if !got[p] {
			t.Fatalf("%s: pixel %v is missing (drawn shape is smaller than the reference)", name, p)
		}
	}
	for p := range got {
		if !want[p] {
			t.Fatalf("%s: pixel %v was drawn outside the reference shape", name, p)
		}
	}
}

func TestFillRoundedRectPixelsMatchScanlineReference(t *testing.T) {
	shape := sdl.Color{R: 230, G: 60, B: 90, A: 255}
	cases := []struct {
		name          string
		x, y, w, h, r int32
	}{
		{"rounded", 40, 30, 180, 120, 18},
		{"square corners", 40, 30, 180, 120, 0},
		{"radius larger than the box", 40, 30, 60, 40, 100},
		{"very wide and thin", 40, 30, 400, 9, 12},
		{"one pixel tall", 40, 30, 120, 1, 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			area := sdl.Rect{X: tc.x - 2, Y: tc.y - 2, W: tc.w + 4, H: tc.h + 4}
			got := renderShapeMask(t, area, func(r *sdl.Renderer) {
				fillRoundedRect(r, tc.x, tc.y, tc.w, tc.h, tc.r, shape)
			})
			compareMasks(t, tc.name, got, referenceFillMask(tc.x, tc.y, tc.w, tc.h, tc.r))
		})
	}
}

func TestStrokeRoundedRectPixelsMatchScanlineReference(t *testing.T) {
	shape := sdl.Color{R: 90, G: 200, B: 230, A: 255}
	cases := []struct {
		name                 string
		x, y, w, h, r, thick int32
	}{
		{"ring", 40, 30, 180, 120, 18, 3},
		{"thick ring, small radius", 40, 30, 120, 90, 4, 6},
		{"radius larger than the box", 40, 30, 60, 40, 100, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			area := sdl.Rect{X: tc.x - 2, Y: tc.y - 2, W: tc.w + 4, H: tc.h + 4}
			got := renderShapeMask(t, area, func(r *sdl.Renderer) {
				strokeRoundedRect(r, tc.x, tc.y, tc.w, tc.h, tc.r, tc.thick, shape)
			})
			compareMasks(t, tc.name, got, referenceStrokeMask(tc.x, tc.y, tc.w, tc.h, tc.r, tc.thick))
		})
	}
}
