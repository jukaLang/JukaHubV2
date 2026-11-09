package main

// brandlogo.go — the JukaHub logo inside the app.
//
// The About screen shows the icon the TrimUI launcher, the Windows taskbar and
// the web builder show (branding/mklogo.py renders all of them). It is not a
// second copy in the binary: the window icon already embeds the artwork
// (windowicon.go, JukaPlayer.png), so this decodes those bytes once.
//
// The artwork is 300 px and the About block draws it at about a third of that,
// so it is resampled to the exact size it is drawn at with an area-weighted box
// filter over premultiplied colour. A linear-filtered downscale reads 4 of the
// 9 pixels each output pixel covers, which makes the glass rim shimmer, and
// averaging straight colour pulls the transparent corners' black into the
// tile's edge. The texture is owned by its renderer
// (docs/adr/0002-renderer-owned-caches.md): one built for another renderer is
// forgotten, never destroyed.

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"math"
	"sync"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// brandLogo is the decoded artwork and the one texture drawn from it. It is
// only touched on the render thread.
var brandLogo struct {
	once  sync.Once
	src   *image.NRGBA // nil when the artwork could not be decoded
	owner *sdl.Renderer
	size  int32
	tex   *sdl.Texture
	// failOwner and failSize remember an upload the renderer refused, so a
	// renderer that cannot take the texture is not asked again every frame.
	failOwner *sdl.Renderer
	failSize  int32
}

// brandLogoSource decodes the embedded artwork once, into an NRGBA whose
// bounds start at the origin.
func brandLogoSource() *image.NRGBA {
	brandLogo.once.Do(func() {
		img, err := png.Decode(bytes.NewReader(windowIconPNG))
		if err != nil {
			return
		}
		if n, ok := img.(*image.NRGBA); ok && n.Rect.Min == (image.Point{}) {
			brandLogo.src = n
			return
		}
		b := img.Bounds()
		dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
		brandLogo.src = dst
	})
	return brandLogo.src
}

// brandResample scales src (bounds at the origin) to a size×size NRGBA with an
// area-weighted box filter. Colour is summed weighted by alpha and divided back
// out, so a transparent pixel adds coverage but no colour.
func brandResample(src *image.NRGBA, size int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	if size <= 0 || sw <= 0 || sh <= 0 {
		return dst
	}
	// Horizontal pass: every source row becomes size premultiplied columns.
	cols := make([]float64, sh*size*4)
	sx := float64(sw) / float64(size)
	for y := 0; y < sh; y++ {
		row := src.Pix[y*src.Stride:]
		for x := 0; x < size; x++ {
			x0, x1 := float64(x)*sx, float64(x+1)*sx
			acc := cols[(y*size+x)*4:][:4]
			for i := int(x0); i < sw && float64(i) < x1; i++ {
				w := math.Min(x1, float64(i+1)) - math.Max(x0, float64(i))
				if w <= 0 {
					continue
				}
				p := row[i*4:][:4]
				a := float64(p[3]) * w
				acc[0] += float64(p[0]) * a
				acc[1] += float64(p[1]) * a
				acc[2] += float64(p[2]) * a
				acc[3] += a
			}
		}
	}
	// Vertical pass, then the colour divided back out of the coverage.
	sy := float64(sh) / float64(size)
	area := sx * sy
	for y := 0; y < size; y++ {
		y0, y1 := float64(y)*sy, float64(y+1)*sy
		out := dst.Pix[y*dst.Stride:]
		for x := 0; x < size; x++ {
			var r, g, b, a float64
			for j := int(y0); j < sh && float64(j) < y1; j++ {
				w := math.Min(y1, float64(j+1)) - math.Max(y0, float64(j))
				if w <= 0 {
					continue
				}
				c := cols[(j*size+x)*4:][:4]
				r += c[0] * w
				g += c[1] * w
				b += c[2] * w
				a += c[3] * w
			}
			o := x * 4
			if a > 0 {
				out[o] = uint8(math.Min(255, math.Round(r/a)))
				out[o+1] = uint8(math.Min(255, math.Round(g/a)))
				out[o+2] = uint8(math.Min(255, math.Round(b/a)))
			}
			out[o+3] = uint8(math.Min(255, math.Round(a/area)))
		}
	}
	return dst
}

// brandLogoUpload makes a blended static texture of pic. NRGBA's R,G,B,A bytes
// are ABGR8888 on the little-endian targets the app runs on.
func brandLogoUpload(renderer *sdl.Renderer, pic *image.NRGBA) *sdl.Texture {
	w, h := int32(pic.Rect.Dx()), int32(pic.Rect.Dy())
	if w <= 0 || h <= 0 || len(pic.Pix) == 0 {
		return nil
	}
	// Linear filtering for the one scale left: a canvas drawn at a logical
	// size (the Brick's 960x720 on its 1024x768 panel), where nearest sampling
	// doubles every fifteenth row of the rim. At 1:1 it reads the same pixel.
	// The hint is read when a texture is created, and put back for the rest.
	prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "linear")
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, w, h)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
	if err != nil || tex == nil {
		return nil
	}
	if err := tex.Update(nil, unsafe.Pointer(&pic.Pix[0]), pic.Stride); err != nil {
		_ = tex.Destroy()
		return nil
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	return tex
}

// brandLogoTexture is the logo at size×size pixels for renderer, built on first
// use and rebuilt when the renderer or the size changes. Nil when the artwork
// cannot be decoded or the renderer refuses the texture.
func brandLogoTexture(renderer *sdl.Renderer, size int32) *sdl.Texture {
	if renderer == nil || size <= 0 {
		return nil
	}
	l := &brandLogo
	if l.tex != nil && l.owner == renderer && l.size == size {
		return l.tex
	}
	if l.failOwner == renderer && l.failSize == size {
		return nil
	}
	if l.tex != nil && l.owner == renderer {
		// Ours, on a live renderer: the new size replaces it.
		_ = l.tex.Destroy()
	}
	// A texture built for another renderer is forgotten, not destroyed: SDL
	// freed it along with that renderer.
	l.tex, l.owner, l.size = nil, nil, 0
	src := brandLogoSource()
	if src == nil {
		return nil
	}
	tex := brandLogoUpload(renderer, brandResample(src, int(size)))
	if tex == nil {
		l.failOwner, l.failSize = renderer, size
		return nil
	}
	l.tex, l.owner, l.size = tex, renderer, size
	return tex
}

// drawBrandLogo draws the logo into a square rect (its width is the size) and
// reports whether it could.
func drawBrandLogo(renderer *sdl.Renderer, rect sdl.Rect) bool {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return false
	}
	tex := brandLogoTexture(renderer, rect.W)
	if tex == nil {
		return false
	}
	return renderer.Copy(tex, nil, &rect) == nil
}
