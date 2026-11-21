package main

// lookpacks_image.go — the picture loading the look packs, console icon packs
// and the power-off picture share: a file on the card decoded with its header
// checked first, then scaled once to the size it is drawn at.
//
// Every image here comes from the user's card or a downloaded pack, so the
// limits are the point: the file size is checked before it is opened, the
// header before a pixel is allocated, and decoding runs under the shared
// decode semaphore (bounded.go) so a pack of forty icons cannot crowd out the
// covers. The scaled result is what is kept; the full-size decode is dropped
// as soon as it has been resampled.

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"math"
	"os"
	"strings"
)

// lookImageMaxSide is the largest width or height a pack picture may declare.
const lookImageMaxSide = 4096

// lookImageExts are the formats Go decodes here (WebP has no Go decoder).
var lookImageExts = []string{".png", ".jpg", ".jpeg"}

// lookImageExtOK reports whether name has one of lookImageExts.
func lookImageExtOK(name string) bool {
	lower := strings.ToLower(name)
	for _, ext := range lookImageExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// lookDecodeFit decodes the picture at path and scales it to fit inside w×h,
// keeping its shape (the result may be narrower or shorter than the box).
// maxBytes bounds the file. ctx cancels a decode nobody waits for any more.
func lookDecodeFit(ctx context.Context, path string, w, h int, maxBytes int64) (*image.NRGBA, error) {
	if w <= 0 || h <= 0 {
		return nil, errors.New("nothing to draw")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("file not found")
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return nil, fmt.Errorf("is larger than %s", humanBytes(maxBytes))
	}
	if !imageDecodeSem.Acquire(ctx, 1) {
		return nil, ctx.Err()
	}
	defer imageDecodeSem.Release(1)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, errors.New("is not a PNG or JPEG picture")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > lookImageMaxSide || cfg.Height > lookImageMaxSide {
		return nil, fmt.Errorf("is %dx%d; pictures up to %dx%d are read", cfg.Width, cfg.Height, lookImageMaxSide, lookImageMaxSide)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, errors.New("could not be decoded")
	}
	b := img.Bounds()
	src, ok := img.(*image.NRGBA)
	if !ok || b.Min != (image.Point{}) {
		src = image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	}
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	scale := math.Min(float64(w)/float64(sw), float64(h)/float64(sh))
	tw := max(1, min(w, int(math.Round(float64(sw)*scale))))
	th := max(1, min(h, int(math.Round(float64(sh)*scale))))
	if tw == sw && th == sh {
		return src, nil
	}
	return lookResample(src, tw, th), nil
}

// lookResample scales src (bounds at the origin) to dw×dh with the
// area-weighted box filter brandResample uses for squares: colour is summed
// weighted by alpha and divided back out, so transparent edges do not darken.
// It works both ways, shrinking a poster and enlarging a small icon.
func lookResample(src *image.NRGBA, dw, dh int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	if dw <= 0 || dh <= 0 || sw <= 0 || sh <= 0 {
		return dst
	}
	sx, sy := float64(sw)/float64(dw), float64(sh)/float64(dh)
	area := sx * sy
	// One source row is resampled across at a time and weighted into the
	// output row it falls in: the scratch is two rows of dw, not a buffer as
	// tall as the source (~90 MB of floats for a 4096-row picture).
	row := make([]float64, dw*4)
	acc := make([]float64, dw*4)
	for y := 0; y < dh; y++ {
		y0, y1 := float64(y)*sy, float64(y+1)*sy
		clear(acc)
		for j := int(y0); j < sh && float64(j) < y1; j++ {
			wy := math.Min(y1, float64(j+1)) - math.Max(y0, float64(j))
			if wy <= 0 {
				continue
			}
			lookResampleRow(src.Pix[j*src.Stride:], sw, sx, row)
			for i, v := range row {
				acc[i] += v * wy
			}
		}
		out := dst.Pix[y*dst.Stride:]
		for x := 0; x < dw; x++ {
			c := acc[x*4:][:4]
			o := x * 4
			if c[3] > 0 {
				out[o] = uint8(math.Min(255, math.Round(c[0]/c[3])))
				out[o+1] = uint8(math.Min(255, math.Round(c[1]/c[3])))
				out[o+2] = uint8(math.Min(255, math.Round(c[2]/c[3])))
			}
			out[o+3] = uint8(math.Min(255, math.Round(c[3]/area)))
		}
	}
	return dst
}

// lookResampleRow is one source row's pass across: each output column's
// alpha-weighted colour and alpha, written over out (len(out)/4 columns).
func lookResampleRow(pix []uint8, sw int, sx float64, out []float64) {
	clear(out)
	for x := 0; x < len(out)/4; x++ {
		x0, x1 := float64(x)*sx, float64(x+1)*sx
		acc := out[x*4:][:4]
		for i := int(x0); i < sw && float64(i) < x1; i++ {
			wt := math.Min(x1, float64(i+1)) - math.Max(x0, float64(i))
			if wt <= 0 {
				continue
			}
			p := pix[i*4:][:4]
			a := float64(p[3]) * wt
			acc[0] += float64(p[0]) * a
			acc[1] += float64(p[1]) * a
			acc[2] += float64(p[2]) * a
			acc[3] += a
		}
	}
}
