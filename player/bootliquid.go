package main

// bootliquid.go — the liquid glass both ends of a session are drawn with.
//
// The boot splash and the shutdown teardown share one look, after the PS5's
// start screen: a living backdrop of slowly flowing, blurred colour blobs (the
// cyan and violet of Home's backdrop, with a deep blue and a lilac between
// them), and over it a sheet of refractive glass — the blobs seen through it
// are magnified and shifted the way a lens bends what is behind it, soft
// caustic light drifts inside it, a specular sheen sweeps across it, and the
// JukaHub wordmark comes into focus through it.
//
// Everything is pre-rendered once, on the CPU, into small textures: the blobs
// are analytic metaball fields (a sum of gaussians, so there is no blur pass to
// pay for), the caustic net and the sheen are closed-form patterns, the sheet's
// frost and its lit edges are a rounded-rectangle distance field at the sheet's
// own size, and the wordmark's out-of-focus copy is three box blurs of the real
// glyphs. The textures are created with linear filtering, so a 128 px blob
// scaled to fill half the screen stays smooth. Per frame only transforms,
// alpha and colour modulation change: about sixteen draw calls for the whole
// liquid layer, no per-frame allocation (the destination rects are package
// scratch values), and the software renderer skips the two refraction and two
// caustic layers it would pay for in fill rate.
//
// The kit belongs to the renderer that built it. The splash and the teardown
// build it on entry and release it on the way out; tests drop it with their
// renderer (bootLiquidDropForRenderer).

import (
	"math"
	"sync"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	bootBlobTexSize    = 128 // metaball blob texture, square
	bootCausticTexSize = 192 // caustic net texture, square
	bootSheenTexW      = 96  // slanted sheen band
	bootSheenTexH      = 128
	bootVignetteTexW   = 64 // full-screen vignette, scaled up
	bootVignetteTexH   = 36
	bootShadowScale    = 4  // the sheet's shadow is built at a quarter size
	bootShadowPadQ     = 12 // shadow padding at quarter size (48 px on screen)
	bootWordBlurRadius = 7  // box radius of the wordmark's out-of-focus copy
)

// bootLiquidKit is the set of textures one renderer draws the liquid glass
// with. It is only touched on the render thread.
type bootLiquidKit struct {
	renderer *sdl.Renderer
	ready    bool
	light    bool
	software bool
	sheetW   int32
	sheetH   int32
	radius   int32
	blob     [2]*sdl.Texture
	caustic  *sdl.Texture
	sheen    *sdl.Texture
	vignette *sdl.Texture
	shadow   *sdl.Texture
	body     *sdl.Texture
	edge     *sdl.Texture
	word     *sdl.Texture
	wordBlur *sdl.Texture
	wordW    int32
	wordH    int32
	wordPad  int32
}

var bootLiquid bootLiquidKit

// Scratch destinations for the per-frame draws: package values, so passing
// their address to SDL never allocates.
var (
	bootLiquidF    sdl.FRect
	bootLiquidR    sdl.Rect
	bootLiquidClip sdl.Rect
)

// bootBlob is one flowing light in the backdrop.
type bootBlob struct {
	ox, oy float64 // resting centre, fraction of the screen
	rx, ry float64 // drift radius, fraction of the screen
	speed  float64 // radians per second along the drift
	phase  float64
	size   float64 // diameter, fraction of the screen height
	spin   float64 // degrees per second the shape turns
	tex    int     // which blob shape
	alpha  uint8   // peak alpha on a dark theme
	accent bool    // tinted with the theme accent instead of col
	col    sdl.Color
}

// bootBlobs is the backdrop: the accent bloom upper-left and the violet one
// lower-right sit where Home's two blooms are, so the hand-over into Home keeps
// its light where it was; the blue and lilac between them make it liquid.
var bootBlobs = [4]bootBlob{
	{ox: 0.30, oy: 0.36, rx: 0.10, ry: 0.08, speed: 0.21, phase: 0.0, size: 1.00, spin: 7, tex: 0, alpha: 120, accent: true},
	{ox: 0.71, oy: 0.64, rx: 0.12, ry: 0.09, speed: -0.17, phase: 2.1, size: 1.08, spin: -6, tex: 1, alpha: 118, col: sdl.Color{R: 124, G: 58, B: 237, A: 255}},
	{ox: 0.58, oy: 0.24, rx: 0.09, ry: 0.07, speed: 0.13, phase: 4.2, size: 0.74, spin: 9, tex: 1, alpha: 92, col: sdl.Color{R: 37, G: 99, B: 235, A: 255}},
	{ox: 0.24, oy: 0.76, rx: 0.08, ry: 0.10, speed: -0.24, phase: 1.2, size: 0.68, spin: -8, tex: 0, alpha: 84, col: sdl.Color{R: 192, G: 132, B: 252, A: 255}},
}

// bootBlobPlace is where blob i is at time t: centre and diameter in screen
// pixels, and its rotation. With motion off the blobs rest at their centres.
func bootBlobPlace(i int, t float64, motion bool, w, h float64) (cx, cy, d, angle float64) {
	b := bootBlobs[i]
	cx, cy = w*b.ox, h*b.oy
	d = h * b.size
	angle = b.phase * 30
	if motion {
		cx += w * b.rx * math.Sin(t*b.speed+b.phase)
		cy += h * b.ry * math.Cos(t*b.speed*0.8+b.phase)
		d *= 1 + 0.06*math.Sin(t*0.35+b.phase)
		angle += t * b.spin
	}
	return cx, cy, d, angle
}

// bootLiquidParallax is the slow camera drift the layers move against: the
// backdrop by the full amount, the sheet a little the other way, which is what
// gives the glass its depth on a device with no tilt sensor.
func bootLiquidParallax(t float64, motion bool) (float64, float64) {
	if !motion {
		return 0, 0
	}
	return 10 * math.Sin(t*0.23), 6 * math.Sin(t*0.17+1.3)
}

// ── Building the kit ─────────────────────────────────────────────────────

// bootLiquidPrepare builds (or keeps) the kit for renderer and a sheet of the
// given size. It reports whether the liquid layer is available; on false the
// callers draw the flat glass card instead. word is the wordmark font (nil
// skips the wordmark textures).
func bootLiquidPrepare(renderer *sdl.Renderer, sheetW, sheetH, radius int32, word *ttf.Font, text string) bool {
	if renderer == nil || sheetW <= 0 || sheetH <= 0 {
		return false
	}
	k := &bootLiquid
	light := glassLightTheme()
	if k.ready && k.renderer == renderer && k.sheetW == sheetW && k.sheetH == sheetH && k.radius == radius && k.light == light {
		return true
	}
	if k.renderer == renderer {
		bootLiquidRelease()
	} else {
		// Another renderer's textures died with it: forget, do not destroy.
		*k = bootLiquidKit{}
	}
	k.renderer = renderer
	k.light = light
	k.sheetW, k.sheetH, k.radius = sheetW, sheetH, radius
	if info, err := renderer.GetInfo(); err == nil && info.Flags&sdl.RENDERER_SOFTWARE != 0 {
		k.software = true
	}

	sw, sh, sr := int(sheetW), int(sheetH), float64(radius)
	shadowW := sw/bootShadowScale + 2*bootShadowPadQ
	shadowH := sh/bootShadowScale + 2*bootShadowPadQ
	// The theme is read here, on the render thread, and handed to the workers.
	sheetBase, sheetInk, sheetAccent := ColorBackground, glassInk(), glassAccent()
	// Glyph rasterisation is a TTF call: it stays on this thread too.
	wordAlpha, wordAlphaW, wordAlphaH, wordAlphaOK := bootLiquidTextAlpha(word, text)
	wordPad := bootWordBlurRadius * 3

	// The pixel work is independent per texture; on the handheld's four cores
	// it runs side by side.
	var wg sync.WaitGroup
	var blob0, blob1, caustic, sheen, vignette, body, edge, shadow, wordPix, wordBlurPix []byte
	var wordW, wordH int
	wg.Add(4)
	go func() {
		defer wg.Done()
		blob0 = bootLiquidBlobPixels(bootBlobTexSize, 0)
		blob1 = bootLiquidBlobPixels(bootBlobTexSize, 1)
	}()
	go func() {
		defer wg.Done()
		caustic = bootLiquidCausticPixels(bootCausticTexSize)
		sheen = bootLiquidSheenPixels(bootSheenTexW, bootSheenTexH)
		vignette = bootLiquidVignettePixels(bootVignetteTexW, bootVignetteTexH)
	}()
	go func() {
		defer wg.Done()
		body, edge = bootLiquidSheetPixels(sw, sh, sr, sheetBase, sheetInk, sheetAccent, light)
	}()
	go func() {
		defer wg.Done()
		shadow = bootLiquidShadowPixels(shadowW, shadowH, sw/bootShadowScale, sh/bootShadowScale, sr/bootShadowScale)
		if wordAlphaOK {
			wordPix, wordBlurPix, wordW, wordH = bootLiquidWordPixels(wordAlpha, wordAlphaW, wordAlphaH, wordPad)
		}
	}()
	wg.Wait()
	haveWord := wordPix != nil && wordBlurPix != nil

	// Linear filtering is what keeps a 128 px blob smooth at 700 px; the hint
	// is read when a texture is created, and put back for everyone else.
	prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "linear")
	k.blob[0] = bootLiquidTexture(renderer, blob0, bootBlobTexSize, bootBlobTexSize)
	k.blob[1] = bootLiquidTexture(renderer, blob1, bootBlobTexSize, bootBlobTexSize)
	k.caustic = bootLiquidTexture(renderer, caustic, bootCausticTexSize, bootCausticTexSize)
	k.sheen = bootLiquidTexture(renderer, sheen, bootSheenTexW, bootSheenTexH)
	k.vignette = bootLiquidTexture(renderer, vignette, bootVignetteTexW, bootVignetteTexH)
	k.shadow = bootLiquidTexture(renderer, shadow, shadowW, shadowH)
	k.body = bootLiquidTexture(renderer, body, sw, sh)
	k.edge = bootLiquidTexture(renderer, edge, sw, sh)
	if haveWord {
		k.word = bootLiquidTexture(renderer, wordPix, wordW, wordH)
		k.wordBlur = bootLiquidTexture(renderer, wordBlurPix, wordW, wordH)
		k.wordW, k.wordH, k.wordPad = int32(wordW), int32(wordH), int32(wordPad)
	}
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
	if k.caustic != nil {
		_ = k.caustic.SetBlendMode(sdl.BLENDMODE_ADD)
	}
	k.ready = k.blob[0] != nil && k.blob[1] != nil && k.body != nil && k.edge != nil
	return k.ready
}

// bootLiquidRelease destroys the kit's textures. It must run on the render
// thread while the kit's renderer is alive.
func bootLiquidRelease() {
	k := &bootLiquid
	for _, tex := range []*sdl.Texture{k.blob[0], k.blob[1], k.caustic, k.sheen, k.vignette, k.shadow, k.body, k.edge, k.word, k.wordBlur} {
		if tex != nil {
			_ = tex.Destroy()
		}
	}
	*k = bootLiquidKit{}
}

// bootLiquidTexture uploads straight-alpha bytes in R,G,B,A order — the
// memory layout of ABGR8888 on the little-endian targets the app runs on — as
// a blended static texture.
func bootLiquidTexture(renderer *sdl.Renderer, pix []byte, w, h int) *sdl.Texture {
	if renderer == nil || w <= 0 || h <= 0 || len(pix) < w*h*4 {
		return nil
	}
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, int32(w), int32(h))
	if err != nil || tex == nil {
		return nil
	}
	if err := tex.Update(nil, unsafe.Pointer(&pix[0]), w*4); err != nil {
		_ = tex.Destroy()
		return nil
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	return tex
}

// ── Pixel generators (pure: no SDL, unit-tested) ─────────────────────────

func bootClamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func bootSmoothstep(e0, e1, x float64) float64 {
	if e1 == e0 {
		if x < e0 {
			return 0
		}
		return 1
	}
	t := bootClamp01((x - e0) / (e1 - e0))
	return t * t * (3 - 2*t)
}

// bootPutPixel writes one straight-alpha pixel.
func bootPutPixel(pix []byte, i int, r, g, b uint8, a float64) {
	pix[i] = r
	pix[i+1] = g
	pix[i+2] = b
	pix[i+3] = uint8(math.Round(255 * bootClamp01(a)))
}

// bootBlobLobes are the two blob shapes: three gaussians each (x, y, sigma,
// weight, in texture fractions), so the light is an organic, slightly lopsided
// body rather than a disc — turning it is what makes the backdrop look liquid.
var bootBlobLobes = [2][3][4]float64{
	{{0.50, 0.50, 0.17, 1.00}, {0.37, 0.42, 0.12, 0.72}, {0.63, 0.60, 0.13, 0.62}},
	{{0.50, 0.50, 0.16, 1.00}, {0.61, 0.38, 0.11, 0.66}, {0.40, 0.64, 0.14, 0.56}},
}

// bootLiquidBlobPixels renders one blob shape: white, with the metaball field
// as alpha, windowed so the texture's border is fully transparent (a scaled
// copy must never show its square edge).
func bootLiquidBlobPixels(size, shape int) []byte {
	if size <= 0 {
		return nil
	}
	lobes := bootBlobLobes[shape%len(bootBlobLobes)]
	pix := make([]byte, size*size*4)
	peak := 0.0
	field := make([]float64, size*size)
	for y := 0; y < size; y++ {
		v := (float64(y) + 0.5) / float64(size)
		for x := 0; x < size; x++ {
			u := (float64(x) + 0.5) / float64(size)
			f := 0.0
			for _, l := range lobes {
				dx, dy := u-l[0], v-l[1]
				f += l[3] * math.Exp(-(dx*dx+dy*dy)/(2*l[2]*l[2]))
			}
			a := 1 - math.Exp(-1.8*f)
			a *= math.Pow(math.Sin(math.Pi*u)*math.Sin(math.Pi*v), 0.5)
			field[y*size+x] = a
			if a > peak {
				peak = a
			}
		}
	}
	if peak <= 0 {
		peak = 1
	}
	for i, a := range field {
		bootPutPixel(pix, i*4, 255, 255, 255, math.Pow(a/peak, 1.3))
	}
	return pix
}

// bootLiquidCausticPixels renders the caustic net: three families of warped,
// thin bright lines (1-|sin| raised to a high power), the light pattern a
// rippling surface throws. It is drawn additively, tinted, at low alpha.
func bootLiquidCausticPixels(size int) []byte {
	if size <= 0 {
		return nil
	}
	pix := make([]byte, size*size*4)
	const tau = 2 * math.Pi
	for y := 0; y < size; y++ {
		v := (float64(y) + 0.5) / float64(size)
		for x := 0; x < size; x++ {
			u := (float64(x) + 0.5) / float64(size)
			p := tau * (u*1.6 + 0.35*math.Sin(tau*(v*1.1+0.15)))
			q := tau * (v*1.4 + 0.30*math.Sin(tau*(u*0.9+0.55)))
			r := tau * ((u+v)*1.2 + 0.25*math.Sin(tau*(u-v)*0.8))
			a := math.Pow(1-math.Abs(math.Sin(p)), 8) +
				math.Pow(1-math.Abs(math.Sin(q)), 8) +
				0.6*math.Pow(1-math.Abs(math.Sin(r)), 10)
			a *= math.Pow(math.Sin(math.Pi*u)*math.Sin(math.Pi*v), 0.6)
			bootPutPixel(pix, (y*size+x)*4, 255, 255, 255, a)
		}
	}
	return pix
}

// bootLiquidSheenPixels renders the specular band: a soft slanted stripe with
// a brighter core, fading out at its top and bottom.
func bootLiquidSheenPixels(w, h int) []byte {
	if w <= 0 || h <= 0 {
		return nil
	}
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		cx := float64(w)/2 + (float64(y)-float64(h)/2)*0.25
		fade := math.Pow(math.Sin(math.Pi*(float64(y)+0.5)/float64(h)), 0.7)
		for x := 0; x < w; x++ {
			dx := float64(x) + 0.5 - cx
			a := math.Exp(-(dx/11)*(dx/11)) + 0.6*math.Exp(-(dx/3)*(dx/3))
			bootPutPixel(pix, (y*w+x)*4, 255, 255, 255, a*0.62*fade)
		}
	}
	return pix
}

// bootLiquidVignettePixels renders the edge darkening that keeps the eye on the
// sheet: black, transparent in the middle, deeper toward the corners.
func bootLiquidVignettePixels(w, h int) []byte {
	if w <= 0 || h <= 0 {
		return nil
	}
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		ny := (float64(y)+0.5)/float64(h)*2 - 1
		for x := 0; x < w; x++ {
			nx := (float64(x)+0.5)/float64(w)*2 - 1
			r := math.Sqrt(nx*nx+ny*ny) / math.Sqrt2
			bootPutPixel(pix, (y*w+x)*4, 0, 0, 0, math.Pow(bootSmoothstep(0.35, 1.0, r), 1.2)*0.92)
		}
	}
	return pix
}

// bootRoundRectSDF is the signed distance from (px, py) — relative to the
// rectangle's centre — to a rounded rectangle of half size (hw, hh) and corner
// radius r: negative inside, positive outside, in pixels.
func bootRoundRectSDF(px, py, hw, hh, r float64) float64 {
	qx := math.Abs(px) - (hw - r)
	qy := math.Abs(py) - (hh - r)
	ox, oy := math.Max(qx, 0), math.Max(qy, 0)
	return math.Hypot(ox, oy) + math.Min(math.Max(qx, qy), 0) - r
}

// bootLiquidShadowPixels renders the sheet's shadow at a quarter size: a
// rounded rectangle whose edge fades over a wide band, scaled up 4x on screen.
func bootLiquidShadowPixels(w, h, rectW, rectH int, radius float64) []byte {
	if w <= 0 || h <= 0 || rectW <= 0 || rectH <= 0 {
		return nil
	}
	pix := make([]byte, w*h*4)
	hw, hh := float64(rectW)/2, float64(rectH)/2
	const soft = 9.0
	for y := 0; y < h; y++ {
		py := float64(y) + 0.5 - float64(h)/2
		for x := 0; x < w; x++ {
			px := float64(x) + 0.5 - float64(w)/2
			d := bootRoundRectSDF(px, py, hw, hh, radius)
			a := 1 - bootSmoothstep(-soft*0.6, soft, d)
			bootPutPixel(pix, (y*w+x)*4, 0, 0, 0, a*0.62)
		}
	}
	return pix
}

// bootLiquidSheetPixels renders the sheet at its own size, in two layers. The
// body is the frost: the theme's background lifted toward the glass ink (and a
// hint of the accent, like every overlay sheet), lighter at the top, mostly
// opaque so the step rows stay readable over moving light. The edge layer goes
// above the refraction: a hairline, a bright lip along the top, a soft glow
// under it and a fainter light along the bottom — the edges a thick pane of
// glass catches. Both are anti-aliased by the distance field.
func bootLiquidSheetPixels(w, h int, radius float64, base, ink, accent sdl.Color, light bool) (body, edge []byte) {
	if w <= 0 || h <= 0 {
		return nil, nil
	}
	body = make([]byte, w*h*4)
	edge = make([]byte, w*h*4)
	hw, hh := float64(w)/2, float64(h)/2
	white := sdl.Color{R: 255, G: 255, B: 255, A: 255}
	top := glassMix(glassMix(base, ink, 0.11), accent, 0.04)
	bottom := glassMix(glassMix(base, ink, 0.05), accent, 0.03)
	bodyAlpha := 0.84
	hairInk := white
	if light {
		top = glassMix(base, white, 0.55)
		bottom = glassMix(base, white, 0.35)
		bodyAlpha = 0.80
		hairInk = ink
	}
	for y := 0; y < h; y++ {
		fy := (float64(y) + 0.5) / float64(h)
		row := glassMix(top, bottom, fy)
		py := float64(y) + 0.5 - hh
		topW := math.Pow(bootClamp01(1-fy/0.30), 1.5)
		rim := 0.10 * math.Pow(bootClamp01(1-fy/0.42), 2)
		bottomW := math.Pow(bootClamp01((fy-0.70)/0.30), 1.5)
		for x := 0; x < w; x++ {
			px := float64(x) + 0.5 - hw
			d := bootRoundRectSDF(px, py, hw, hh, radius)
			cover := bootClamp01(0.5 - d)
			i := (y*w + x) * 4
			if cover <= 0 {
				continue // both layers start zeroed: fully transparent
			}
			bootPutPixel(body, i, row.R, row.G, row.B, bodyAlpha*cover)
			hair, lip, low := 0.0, 0.0, 0.0
			if d > -8 {
				hair = 0.30 * math.Exp(-((d+1.0)/0.9)*((d+1.0)/0.9))
				if topW > 0 {
					lip = 0.62 * topW * math.Exp(-((d+1.6)/1.4)*((d+1.6)/1.4))
				}
				if bottomW > 0 {
					low = 0.24 * bottomW * math.Exp(-((d+1.2)/1.1)*((d+1.2)/1.1))
				}
			}
			a := hair + lip + low + rim
			if a <= 0 {
				continue
			}
			// The hairline takes the glass ink (slate on a light theme); the
			// light along the lip, the rim and the bottom is always white.
			share := hair / a
			c := glassMix(white, hairInk, share)
			bootPutPixel(edge, i, c.R, c.G, c.B, bootClamp01(a)*cover)
		}
	}
	return body, edge
}

// bootLiquidTextAlpha rasterises text with font and returns its coverage as
// one byte per pixel. It is a TTF call, so it runs on the render thread.
func bootLiquidTextAlpha(font *ttf.Font, text string) ([]uint8, int, int, bool) {
	if font == nil || text == "" {
		return nil, 0, 0, false
	}
	surface, err := font.RenderUTF8Blended(text, sdl.Color{R: 255, G: 255, B: 255, A: 255})
	if err != nil || surface == nil {
		return nil, 0, 0, false
	}
	defer surface.Free()
	rgba, err := surface.ConvertFormat(uint32(sdl.PIXELFORMAT_RGBA32), 0)
	if err != nil || rgba == nil {
		return nil, 0, 0, false
	}
	defer rgba.Free()
	w, h, pitch := int(rgba.W), int(rgba.H), int(rgba.Pitch)
	src := rgba.Pixels()
	if w <= 0 || h <= 0 || pitch < w*4 || len(src) < pitch*h {
		return nil, 0, 0, false
	}
	alpha := make([]uint8, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			alpha[y*w+x] = src[y*pitch+x*4+3]
		}
	}
	return alpha, w, h, true
}

// bootLiquidWordPixels turns the wordmark's coverage into its two textures,
// both pad pixels larger on every side so they line up when drawn at the same
// rect: the sharp glyphs, and an out-of-focus copy (three box blurs approach a
// gaussian) brightened a little, since blurring spreads the same ink thinner.
func bootLiquidWordPixels(alpha []uint8, w, h, pad int) (sharp, blurred []byte, outW, outH int) {
	if w <= 0 || h <= 0 || len(alpha) < w*h || pad < 0 {
		return nil, nil, 0, 0
	}
	outW, outH = w+2*pad, h+2*pad
	field := make([]float64, outW*outH)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			field[(y+pad)*outW+x+pad] = float64(alpha[y*w+x]) / 255
		}
	}
	sharp = make([]byte, outW*outH*4)
	for i, a := range field {
		bootPutPixel(sharp, i*4, 255, 255, 255, a)
	}
	tmp := make([]float64, len(field))
	blur := append([]float64(nil), field...)
	for pass := 0; pass < 3; pass++ {
		bootBoxBlur(blur, tmp, outW, outH, bootWordBlurRadius, true)
		bootBoxBlur(tmp, blur, outW, outH, bootWordBlurRadius, false)
	}
	blurred = make([]byte, outW*outH*4)
	for i, a := range blur {
		bootPutPixel(blurred, i*4, 255, 255, 255, a*1.6)
	}
	return sharp, blurred, outW, outH
}

// bootBoxBlur averages src over a window of 2r+1 pixels along one axis into
// dst, with a running sum (cost independent of r) and clamped edges.
func bootBoxBlur(src, dst []float64, w, h, r int, horizontal bool) {
	if r <= 0 {
		copy(dst, src)
		return
	}
	norm := 1 / float64(2*r+1)
	lines, length := h, w
	if !horizontal {
		lines, length = w, h
	}
	at := func(line, i int) int {
		if i < 0 {
			i = 0
		} else if i >= length {
			i = length - 1
		}
		if horizontal {
			return line*w + i
		}
		return i*w + line
	}
	for line := 0; line < lines; line++ {
		sum := 0.0
		for i := -r; i <= r; i++ {
			sum += src[at(line, i)]
		}
		for i := 0; i < length; i++ {
			dst[at(line, i)] = sum * norm
			sum += src[at(line, i+r+1)] - src[at(line, i-r)]
		}
	}
}

// ── Drawing ──────────────────────────────────────────────────────────────

// bootLiquidDrawBackdrop paints the living backdrop over the whole canvas: the
// theme's base, the four flowing blobs and the vignette. presence fades the
// light in and out (0..1); the base is always opaque.
func bootLiquidDrawBackdrop(renderer *sdl.Renderer, t, presence float64, motion bool) {
	k := &bootLiquid
	if renderer == nil {
		return
	}
	w, h := float64(screenWidth), float64(screenHeight)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	base := ColorBackground
	if !k.light {
		// A shade under Home's base, so the blobs have depth to glow in.
		base = glassMix(base, sdl.Color{R: 0, G: 0, B: 0, A: 255}, 0.28)
	}
	base.A = 255
	setDrawColor(renderer, base)
	bootLiquidR = sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}
	_ = renderer.FillRect(&bootLiquidR)
	if !k.ready {
		return
	}
	presence = bootClamp01(presence)
	px, py := bootLiquidParallax(t, motion)
	accent := glassAccent()
	for i := range bootBlobs {
		b := &bootBlobs[i]
		tex := k.blob[b.tex]
		cx, cy, d, angle := bootBlobPlace(i, t, motion, w, h)
		col := b.col
		if b.accent {
			col = accent
		}
		alpha := float64(b.alpha)
		if k.light {
			alpha *= 0.55
		}
		if !motion {
			// Reduced motion keeps the light alive with a slow breath of
			// brightness instead of movement.
			alpha *= 0.92 + 0.08*math.Sin(t*0.9+b.phase)
		}
		_ = tex.SetColorMod(col.R, col.G, col.B)
		_ = tex.SetAlphaMod(uint8(math.Round(alpha * presence)))
		bootLiquidF = sdl.FRect{X: float32(cx + px - d/2), Y: float32(cy + py - d/2), W: float32(d), H: float32(d)}
		_ = renderer.CopyExF(tex, nil, &bootLiquidF, angle, nil, sdl.FLIP_NONE)
	}
	for i := range k.blob {
		_ = k.blob[i].SetColorMod(255, 255, 255)
		_ = k.blob[i].SetAlphaMod(255)
	}
	if k.vignette != nil {
		va := 255.0
		if k.light {
			va = 70
		}
		_ = k.vignette.SetAlphaMod(uint8(va))
		_ = renderer.Copy(k.vignette, nil, &bootLiquidR)
		_ = k.vignette.SetAlphaMod(255)
	}
}

// bootLiquidInnerClip is the part of the sheet the refraction and the caustics
// may paint: the rect inset just past where the rounded corner curves away, so
// a rectangular clip never shows outside the rounded glass.
func bootLiquidInnerClip(sheet sdl.Rect, radius int32) sdl.Rect {
	inset := int32(math.Ceil(float64(radius)*(1-1/math.Sqrt2))) + 1
	return sdl.Rect{X: sheet.X + inset, Y: sheet.Y + inset, W: sheet.W - 2*inset, H: sheet.H - 2*inset}
}

// bootLiquidDrawSheet paints the glass sheet at rect (the size the kit was
// built for; scale grows it about its centre for the hand-off): shadow, frost,
// the refracted blobs, the caustics, the sheen and the lit edges. alpha fades
// the whole pane.
func bootLiquidDrawSheet(renderer *sdl.Renderer, rect sdl.Rect, alpha, scale, t float64, motion bool) {
	k := &bootLiquid
	if renderer == nil || !k.ready || alpha <= 0 {
		return
	}
	alpha = bootClamp01(alpha)
	fw, fh := float64(rect.W)*scale, float64(rect.H)*scale
	fx := float64(rect.X) + (float64(rect.W)-fw)/2
	fy := float64(rect.Y) + (float64(rect.H)-fh)/2
	px, py := bootLiquidParallax(t, motion)
	// The pane drifts a third of the way against the backdrop.
	fx -= px * 0.3
	fy -= py * 0.3
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	if k.shadow != nil {
		pad := float64(bootShadowPadQ*bootShadowScale) * scale
		_ = k.shadow.SetAlphaMod(uint8(math.Round(230 * alpha)))
		bootLiquidF = sdl.FRect{X: float32(fx - pad), Y: float32(fy - pad + 14*scale), W: float32(fw + 2*pad), H: float32(fh + 2*pad)}
		_ = renderer.CopyF(k.shadow, nil, &bootLiquidF)
		_ = k.shadow.SetAlphaMod(255)
	}

	bootLiquidF = sdl.FRect{X: float32(fx), Y: float32(fy), W: float32(fw), H: float32(fh)}
	_ = k.body.SetAlphaMod(uint8(math.Round(255 * alpha)))
	_ = renderer.CopyF(k.body, nil, &bootLiquidF)
	_ = k.body.SetAlphaMod(255)

	inner := bootLiquidInnerClip(sdl.Rect{X: int32(fx), Y: int32(fy), W: int32(fw), H: int32(fh)}, int32(float64(k.radius)*scale))
	if inner.W > 0 && inner.H > 0 {
		bootLiquidClip = inner
		_ = renderer.SetClipRect(&bootLiquidClip)
		w, h := float64(screenWidth), float64(screenHeight)
		cx, cy := fx+fw/2, fy+fh/2
		if !k.software {
			// Refraction: the two big blobs again, magnified about the pane's
			// centre and shifted with the parallax, as a lens would show them.
			accent := glassAccent()
			for i := 0; i < 2; i++ {
				b := &bootBlobs[i]
				tex := k.blob[b.tex]
				bx, by, d, angle := bootBlobPlace(i, t, motion, w, h)
				bx += px
				by += py
				const mag = 1.22
				bx = cx + (bx-cx)*mag + px*0.6
				by = cy + (by-cy)*mag + py*0.6
				d *= mag
				col := b.col
				if b.accent {
					col = accent
				}
				ra := 54.0
				if k.light {
					ra = 34
				}
				_ = tex.SetColorMod(col.R, col.G, col.B)
				_ = tex.SetAlphaMod(uint8(math.Round(ra * alpha)))
				bootLiquidF = sdl.FRect{X: float32(bx - d/2), Y: float32(by - d/2), W: float32(d), H: float32(d)}
				_ = renderer.CopyExF(tex, nil, &bootLiquidF, angle, nil, sdl.FLIP_NONE)
				_ = tex.SetColorMod(255, 255, 255)
				_ = tex.SetAlphaMod(255)
			}
			// Caustics: two copies of the net drifting against each other.
			if k.caustic != nil {
				size := fw * 1.5
				tint := glassMix(accent, sdl.Color{R: 255, G: 255, B: 255, A: 255}, 0.55)
				ca := 34.0
				if k.light {
					ca = 18
				}
				_ = k.caustic.SetColorMod(tint.R, tint.G, tint.B)
				_ = k.caustic.SetAlphaMod(uint8(math.Round(ca * alpha)))
				for i := 0; i < 2; i++ {
					sign := 1.0 - 2.0*float64(i)
					dx, dy, angle := 0.0, 0.0, 37.0*float64(i)
					if motion {
						dx = sign * 46 * math.Sin(t*0.11+float64(i))
						dy = sign * 30 * math.Cos(t*0.09+float64(i)*2)
						angle += sign * t * 3
					}
					bootLiquidF = sdl.FRect{X: float32(cx - size/2 + dx), Y: float32(cy - size/2 + dy), W: float32(size), H: float32(size)}
					_ = renderer.CopyExF(k.caustic, nil, &bootLiquidF, angle, nil, sdl.FLIP_NONE)
				}
				_ = k.caustic.SetColorMod(255, 255, 255)
				_ = k.caustic.SetAlphaMod(255)
			}
		}
		// The specular sweep: a band of light crossing the pane every few
		// seconds. Motion only — it is movement, not light.
		if motion && k.sheen != nil {
			const period, travel = 3.4, 0.42
			phase := math.Mod(t, period) / period
			if phase < travel {
				p := bootSmoothstep(0, 1, phase/travel)
				band := fw * 0.34
				x := fx - band + (fw+2*band)*p
				sa := 54.0
				if k.light {
					sa = 70
				}
				_ = k.sheen.SetAlphaMod(uint8(math.Round(sa * alpha)))
				bootLiquidF = sdl.FRect{X: float32(x), Y: float32(fy), W: float32(band), H: float32(fh)}
				_ = renderer.CopyF(k.sheen, nil, &bootLiquidF)
				_ = k.sheen.SetAlphaMod(255)
			}
		}
		_ = renderer.SetClipRect(nil)
	}

	bootLiquidF = sdl.FRect{X: float32(fx), Y: float32(fy), W: float32(fw), H: float32(fh)}
	_ = k.edge.SetAlphaMod(uint8(math.Round(255 * alpha)))
	_ = renderer.CopyF(k.edge, nil, &bootLiquidF)
	_ = k.edge.SetAlphaMod(255)
}

// bootLiquidDrawWordmark draws the wordmark centred on cx with its glyph box's
// top at top. appear runs 0 → 1 as it materialises (out-of-focus light first,
// then the sharp glyphs settling in, a touch larger to begin with) and 1 → 0
// as it dissolves at shutdown; glint (0..1, 0 = none) runs a band of light
// across the sharp glyphs once. ink is the text colour.
func bootLiquidDrawWordmark(renderer *sdl.Renderer, cx, top int32, appear, alpha, glint float64, ink sdl.Color, motion bool) (sdl.Rect, bool) {
	k := &bootLiquid
	if renderer == nil || k.word == nil || k.wordBlur == nil {
		return sdl.Rect{}, false
	}
	appear = bootClamp01(appear)
	alpha = bootClamp01(alpha)
	scale := 1.0
	if motion {
		u := 1 - appear
		scale = 1 + 0.05*u*u
	}
	// The textures carry wordPad of margin on every side (room for the blur);
	// the glyph box keeps its centre while the scale settles.
	glyphW, glyphH := float64(k.wordW-2*k.wordPad), float64(k.wordH-2*k.wordPad)
	fw, fh := float64(k.wordW)*scale, float64(k.wordH)*scale
	pad := float64(k.wordPad) * scale
	x := float64(cx) - fw/2
	y := float64(top) + glyphH/2 - fh/2
	bootLiquidF = sdl.FRect{X: float32(x), Y: float32(y), W: float32(fw), H: float32(fh)}

	blurA := alpha * 0.9 * bootSmoothstep(0, 0.35, appear) * (1 - bootSmoothstep(0.55, 1, appear))
	sharpA := alpha * bootSmoothstep(0.3, 0.95, appear)
	if blurA > 0 {
		_ = k.wordBlur.SetColorMod(ink.R, ink.G, ink.B)
		_ = k.wordBlur.SetAlphaMod(uint8(math.Round(255 * blurA)))
		_ = renderer.CopyF(k.wordBlur, nil, &bootLiquidF)
	}
	if sharpA > 0 {
		_ = k.word.SetColorMod(ink.R, ink.G, ink.B)
		_ = k.word.SetAlphaMod(uint8(math.Round(255 * sharpA)))
		_ = renderer.CopyF(k.word, nil, &bootLiquidF)
	}
	glyphs := sdl.Rect{
		X: int32(x + pad), Y: int32(y + pad),
		W: int32(glyphW * scale), H: int32(glyphH * scale),
	}
	if glint > 0 && glint < 1 && sharpA > 0.5 && glyphs.W > 0 {
		band := glyphs.W / 5
		bx := glyphs.X - band + int32(float64(glyphs.W+band)*glint)
		bootLiquidClip = glassIntersect(sdl.Rect{X: bx, Y: glyphs.Y - 4, W: band, H: glyphs.H + 8}, sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
		if bootLiquidClip.W > 0 && bootLiquidClip.H > 0 {
			_ = renderer.SetClipRect(&bootLiquidClip)
			_ = k.word.SetBlendMode(sdl.BLENDMODE_ADD)
			_ = k.word.SetColorMod(255, 255, 255)
			_ = k.word.SetAlphaMod(uint8(math.Round(150 * math.Sin(math.Pi*glint) * alpha)))
			_ = renderer.CopyF(k.word, nil, &bootLiquidF)
			_ = k.word.SetBlendMode(sdl.BLENDMODE_BLEND)
			_ = renderer.SetClipRect(nil)
		}
	}
	_ = k.word.SetColorMod(255, 255, 255)
	_ = k.word.SetAlphaMod(255)
	_ = k.wordBlur.SetColorMod(255, 255, 255)
	_ = k.wordBlur.SetAlphaMod(255)
	return glyphs, true
}

// bootLiquidWordHeight is the wordmark's glyph-box height (0 without one).
func bootLiquidWordHeight() int32 {
	if bootLiquid.word == nil {
		return 0
	}
	return bootLiquid.wordH - 2*bootLiquid.wordPad
}
