package main

// listfade.go — the soft fade a scrolling list or grid shows at an edge with
// more content past it (part of the main menu look): the tiles melt into
// the page instead of being cut by the clip. Settings → Display & theme →
// List edge fade (homelook.go) switches it off.
//
// One ramp per axis — white, opaque at the edge and clear at the inner end,
// 64 steps — is made once per renderer, colour-modded to the page colour and
// stretched over the band, so a frame costs one copy per faded edge: no
// allocation and no texture is made while drawing. Over a wallpaper or the
// liquid background the page is not one flat colour, so the fade stops short
// of opaque and reads as a vignette rather than a band.

import (
	"math"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// listFadeSteps is the ramps' length: plenty for a band of a few dozen
// pixels, stretched.
const listFadeSteps = 64

// listFadeRamps holds the two ramps of one renderer: v is 1×steps (opaque at
// the top row), h is steps×1 (opaque at the left column). failed stops a
// renderer that cannot make them from trying on every frame.
var listFadeRamps struct {
	owner  *sdl.Renderer
	v, h   *sdl.Texture
	failed bool
}

// listFadeTextures returns the renderer's ramps, made on first use.
func listFadeTextures(renderer *sdl.Renderer) (v, h *sdl.Texture) {
	c := &listFadeRamps
	if c.owner != renderer {
		// Textures die with their renderer: forget them, never destroy them.
		c.owner, c.v, c.h, c.failed = renderer, nil, nil, false
	}
	if c.v != nil && c.h != nil {
		return c.v, c.h
	}
	if c.failed || renderer == nil {
		return nil, nil
	}
	var pix [listFadeSteps * 4]byte
	for i := 0; i < listFadeSteps; i++ {
		t := float64(i) / float64(listFadeSteps-1)
		a := 1 - t*t*(3-2*t)
		pix[i*4], pix[i*4+1], pix[i*4+2], pix[i*4+3] = 255, 255, 255, uint8(math.Round(a*255))
	}
	make1 := func(w, h int32) *sdl.Texture {
		tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, w, h)
		if err != nil || tex == nil {
			return nil
		}
		// The same bytes serve both shapes: one row of steps, or one column.
		if err := tex.Update(nil, unsafe.Pointer(&pix[0]), int(w)*4); err != nil {
			_ = tex.Destroy()
			return nil
		}
		_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
		return tex
	}
	c.v = make1(1, listFadeSteps)
	c.h = make1(listFadeSteps, 1)
	if c.v == nil || c.h == nil {
		if c.v != nil {
			_ = c.v.Destroy()
		}
		if c.h != nil {
			_ = c.h.Destroy()
		}
		c.v, c.h, c.failed = nil, nil, true
	}
	return c.v, c.h
}

// listFadeSize is the default depth of a fade band for an area span pixels
// long along the fade's axis: 36 px of the 720-high design canvas, never more
// than a quarter of the area.
func listFadeSize(span int32) int32 {
	return min32(scaleY(36, screenHeight), span/4)
}

// listFadeTone is the colour the fade runs into and its strongest alpha: the
// page colour, enough to melt what scrolls under it, and lighter over a
// wallpaper or the water so it darkens the picture instead of covering it.
// Even the plain page is not one flat colour (drawAppBackdrop lays soft
// colour blooms over it), so a near-opaque band would read as a stripe of
// flat colour across a bloom: the edge stays a little see-through.
func listFadeTone() sdl.Color {
	c := ColorBackground
	c.A = 195
	if appWallpaperActive(appConfig) || liquidLevelOf(appConfig) != liquidLevelOff {
		c.A = 175
	}
	return c
}

// drawListEdgeFadeSized fades area's top and/or bottom edge into the page —
// the sign that the list goes on past it — in bands of the given depths (0
// leaves that edge alone; listFadeSize is the usual depth). Every list sizes
// its own bands, because it shortens one that would lie over its focused row.
// Call it after the list is drawn, inside the list's clip.
func drawListEdgeFadeSized(renderer *sdl.Renderer, area sdl.Rect, top, bottom int32) {
	if renderer == nil || area.W <= 0 || area.H <= 0 || (top <= 0 && bottom <= 0) || !listEdgeFadeOn(appConfig) {
		return
	}
	v, _ := listFadeTextures(renderer)
	if v == nil {
		return
	}
	tone := listFadeTone()
	_ = v.SetColorMod(tone.R, tone.G, tone.B)
	_ = v.SetAlphaMod(tone.A)
	if top > 0 {
		dst := sdl.Rect{X: area.X, Y: area.Y, W: area.W, H: min32(top, area.H)}
		_ = renderer.Copy(v, nil, &dst)
	}
	if bottom > 0 {
		h := min32(bottom, area.H)
		dst := sdl.Rect{X: area.X, Y: area.Y + area.H - h, W: area.W, H: h}
		_ = renderer.CopyEx(v, nil, &dst, 0, nil, sdl.FLIP_VERTICAL)
	}
}

// drawListEdgeFadeSides is the same for a list that scrolls sideways: left
// and right bands of the given depths (0 leaves that edge alone).
func drawListEdgeFadeSides(renderer *sdl.Renderer, area sdl.Rect, left, right int32) {
	if renderer == nil || area.W <= 0 || area.H <= 0 || (left <= 0 && right <= 0) || !listEdgeFadeOn(appConfig) {
		return
	}
	_, h := listFadeTextures(renderer)
	if h == nil {
		return
	}
	tone := listFadeTone()
	_ = h.SetColorMod(tone.R, tone.G, tone.B)
	_ = h.SetAlphaMod(tone.A)
	if left > 0 {
		dst := sdl.Rect{X: area.X, Y: area.Y, W: min32(left, area.W), H: area.H}
		_ = renderer.Copy(h, nil, &dst)
	}
	if right > 0 {
		w := min32(right, area.W)
		dst := sdl.Rect{X: area.X + area.W - w, Y: area.Y, W: w, H: area.H}
		_ = renderer.CopyEx(h, nil, &dst, 0, nil, sdl.FLIP_HORIZONTAL)
	}
}
