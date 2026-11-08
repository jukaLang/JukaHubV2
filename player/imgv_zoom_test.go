package main

// imgv_zoom_test.go — the picture viewer's zoom and pan: zooming keeps the
// point under the pointer where it was, the picture can never be panned off
// the screen, Fit/Fill/1:1 mean what they say, and the D-pad pans a zoomed
// picture but browses an unzoomed one. Also the shrink that keeps a camera
// photo's texture within what the handheld's GPU should hold, and the EXIF
// turn that stands a phone's portrait photo upright.

import (
	"image"
	"image/color"
	"math"
	"path/filepath"
	"testing"
)

// withImgv sets up a viewer showing an origW×origH picture on a 1280x720
// screen, restoring the globals afterwards.
func withImgv(t *testing.T, origW, origH int32) {
	t.Helper()
	withScreen(t, 1280, 720)
	prevState := imgv
	prevPath, prevZoom := imageViewerPath, imageViewerZoom
	prevPanX, prevPanY := imageViewerPanX, imageViewerPanY
	prevFiles, prevIndex := imageViewerFiles, imageViewerIndex
	t.Cleanup(func() {
		imgv = prevState
		imageViewerPath, imageViewerZoom = prevPath, prevZoom
		imageViewerPanX, imageViewerPanY = prevPanX, prevPanY
		imageViewerFiles, imageViewerIndex = prevFiles, prevIndex
	})
	imgv = imgvState{origW: origW, origH: origH}
	imageViewerPath = "/photos/test.png"
	imageViewerFiles = []string{"/photos/a.png", "/photos/test.png"}
	imageViewerIndex = 1
	imgvSetMode(imgvFit)
}

func TestImgvFitFillAndActualSize(t *testing.T) {
	withImgv(t, 4000, 3000)
	fit := imageViewerZoom
	if want := math.Min(1248.0/4000, 688.0/3000); math.Abs(fit-want) > 1e-9 {
		t.Fatalf("fit scale = %v, want %v", fit, want)
	}
	if imgvZoomed() {
		t.Fatal("a fitted picture counts as zoomed")
	}
	imgvCycleMode()
	if imgv.mode != imgvFill || math.Abs(imageViewerZoom-math.Max(1280.0/4000, 720.0/3000)) > 1e-9 {
		t.Fatalf("fill = mode %v scale %v", imgv.mode, imageViewerZoom)
	}
	imgvCycleMode()
	if imgv.mode != imgvActual || imageViewerZoom != 1 {
		t.Fatalf("1:1 = mode %v scale %v", imgv.mode, imageViewerZoom)
	}
	if !imgvZoomed() {
		t.Fatal("a 4000-pixel picture at 1:1 is not reported as zoomed")
	}
	imgvCycleMode()
	if imgv.mode != imgvFit || imageViewerZoom != fit {
		t.Fatalf("the cycle did not come back to fit: mode %v scale %v", imgv.mode, imageViewerZoom)
	}
}

func TestImgvZoomKeepsThePointUnderTheAnchor(t *testing.T) {
	withImgv(t, 2000, 1000)
	// The picture point under (900, 400) before the zoom...
	before := imageViewerZoom
	ax, ay := int32(900), int32(400)
	px := (float64(ax) - 640 - float64(imageViewerPanX)) / before
	py := (float64(ay) - 360 - float64(imageViewerPanY)) / before
	imgvZoomBy(2, ax, ay)
	if imgv.mode != imgvCustom || imageViewerZoom <= before {
		t.Fatalf("zoom in left mode %v scale %v", imgv.mode, imageViewerZoom)
	}
	// ...is still under it after (to the pixel the pan clamp allows).
	gotX := 640 + float64(imageViewerPanX) + px*imageViewerZoom
	gotY := 360 + float64(imageViewerPanY) + py*imageViewerZoom
	if math.Abs(gotX-float64(ax)) > 1.5 || math.Abs(gotY-float64(ay)) > 1.5 {
		t.Fatalf("the anchored point moved to %.1f,%.1f, want %d,%d", gotX, gotY, ax, ay)
	}
	// Zooming back out lands exactly on fit, centred.
	imgvZoomBy(-2, ax, ay)
	if imgv.mode != imgvFit || imageViewerPanX != 0 || imageViewerPanY != 0 {
		t.Fatalf("zooming out ended at mode %v pan %d,%d", imgv.mode, imageViewerPanX, imageViewerPanY)
	}
	// The zoom is bounded at both ends.
	for i := 0; i < 40; i++ {
		imgvZoomBy(1, 640, 360)
	}
	_, hi := imgvScaleLimits()
	if imageViewerZoom > hi+1e-9 {
		t.Fatalf("zoom %v passed the limit %v", imageViewerZoom, hi)
	}
	for i := 0; i < 80; i++ {
		imgvZoomBy(-1, 640, 360)
	}
	lo, _ := imgvScaleLimits()
	if imageViewerZoom < lo-1e-9 {
		t.Fatalf("zoom %v went under the limit %v", imageViewerZoom, lo)
	}
}

func TestImgvPanStaysOnThePicture(t *testing.T) {
	withImgv(t, 2000, 1000)
	imgvSetMode(imgvActual)
	for i := 0; i < 20; i++ {
		imgvDirection(1, 0)
	}
	dw, _ := imgvDisplaySize()
	limit := int32((dw - 1280) / 2)
	if imageViewerPanX != -limit {
		t.Fatalf("panning right stopped at %d, want the edge %d", imageViewerPanX, -limit)
	}
	// The picture is shorter than the screen at 1:1? No: 1000 > 720, so it
	// pans vertically too, but never past its edge.
	for i := 0; i < 20; i++ {
		imgvDirection(0, -1)
	}
	if want := int32((1000 - 720) / 2); imageViewerPanY != want {
		t.Fatalf("panning up stopped at %d, want %d", imageViewerPanY, want)
	}
	// Unzoomed, Left/Right browse instead of panning.
	imgvSetMode(imgvFit)
	imgvDirection(-1, 0)
	if imageViewerIndex != 0 || imageViewerPath != "/photos/a.png" {
		t.Fatalf("Left at fit did not step back a picture: %d %q", imageViewerIndex, imageViewerPath)
	}
}

func TestImgvScaleShrinksAndAverages(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 8, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			v := uint8(0)
			if x%2 == 1 {
				v = 200
			}
			src.Set(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	out := imgvScale(src, 4, 2)
	if out.Rect.Dx() != 4 || out.Rect.Dy() != 2 {
		t.Fatalf("scaled to %v", out.Rect)
	}
	// Each output pixel averages a dark and a light column.
	if got := out.Pix[0]; got < 90 || got > 110 {
		t.Fatalf("averaged pixel = %d, want about 100", got)
	}
	if w, h := imgvFitInside(4000, 3000, 2048, 2048); w != 2048 || h != 1536 {
		t.Fatalf("fit inside = %dx%d, want 2048x1536", w, h)
	}
	if w, h := imgvFitInside(300, 200, 2048, 2048); w != 300 || h != 200 {
		t.Fatalf("a small picture was resized to %dx%d", w, h)
	}
}

// A phone stores a portrait photo as landscape pixels plus an EXIF turn; the
// viewer turns it upright the way the lock screen does.
func TestImgvOrientTurnsAPhoneShotUpright(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			src.Set(x, y, color.NRGBA{R: uint8(x + 10*y), A: 255})
		}
	}
	if got := imgvOrient(src, 1); got != src {
		t.Fatal("orientation 1 copied the picture")
	}
	out := imgvOrient(src, 6)
	if out.Rect.Dx() != 2 || out.Rect.Dy() != 3 {
		t.Fatalf("a quarter turn gave %v, want 2x3", out.Rect)
	}
	// Turned clockwise: the stored bottom-left pixel is the new top-left, the
	// stored top-left is the new top-right, the stored bottom-right is the new
	// bottom-left.
	red := func(x, y int) uint8 { return out.Pix[out.PixOffset(x, y)] }
	if red(0, 0) != 10 || red(1, 0) != 0 || red(0, 2) != 12 {
		t.Fatalf("turned pixels = %d %d %d, want 10 0 12", red(0, 0), red(1, 0), red(0, 2))
	}
	if imgvOrient(nil, 6) != nil {
		t.Fatal("a missing screen-sized copy came back as a picture")
	}
	if got := imgvFileOrientation(filepath.Join(t.TempDir(), "photo.png")); got != 1 {
		t.Fatalf("a PNG has orientation %d, want 1", got)
	}
}
