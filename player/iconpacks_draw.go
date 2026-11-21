package main

// iconpacks_draw.go — drawing a console icon pack's pictures in place of the
// vector glyphs.
//
// iconDraw (icondraw.go) asks iconPackDraw first for every emu-* glyph. The
// picture for that glyph is decoded and scaled to the drawn size, rounded up
// to a multiple of 16, on a worker (lookDecodeFit, under the shared decode
// semaphore); the render thread only uploads the finished pixels and copies
// the texture, so a pack of big pictures never stalls a frame. Until a
// picture is ready another size of it, else the vector glyph, is drawn, and a
// picture that cannot be read keeps the glyph for good. Textures are keyed by
// file and size, created on the render thread, trimmed least-recently-used
// past a cap, and all freed when the pack changes and when the icon atlas is
// cleared (a theme repaint, the shutdown); a new renderer drops them unfreed,
// since they died with the old one.

import (
	"context"
	"image"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

// iconPackTexCap is how many scaled pictures stay uploaded.
const iconPackTexCap = 160

// iconPackTexKey names one scaled picture.
type iconPackTexKey struct {
	path string
	w, h int32
}

// iconPackTexEntry is one picture's state: decoding, decoded (pic waiting for
// the render thread), uploaded, or failed.
type iconPackTexEntry struct {
	tex     *sdl.Texture
	pic     *image.NRGBA
	pending bool
	failed  bool
	used    uint64
}

// iconPackTex is the cache. Workers fill pic/pending/failed under mu; the
// render thread does everything else.
var iconPackTex struct {
	mu       sync.Mutex
	entries  map[iconPackTexKey]*iconPackTexEntry
	renderer *sdl.Renderer
	pack     *iconPackLoaded
	frame    uint64
	synced   bool
	ctx      context.Context
	cancel   context.CancelFunc
}

// iconPackFrameSync follows the settings once per frame: the chosen icon pack
// and the console-name style (consolenames.go), so a config reload, a profile
// switch or a pack's Undo shows without a restart. Render thread.
func iconPackFrameSync() {
	if iconPackTex.synced && iconPackTex.frame == frameCount {
		return
	}
	iconPackTex.frame, iconPackTex.synced = frameCount, true
	if appConfig == nil {
		return
	}
	iconPackSync(appConfig)
	consoleNamesSync(appConfig)
}

// iconPackDraw draws the icon pack's picture for glyph key (already resolved)
// centred in dst and reports whether it did; false means draw the vector
// glyph. color's alpha fades the picture like the glyph's ink would.
func iconPackDraw(renderer *sdl.Renderer, key string, dst sdl.Rect, color sdl.Color) bool {
	iconPackFrameSync()
	if !strings.HasPrefix(key, "emu-") {
		return false
	}
	iconPackRendererCheck(renderer)
	pack := iconPackCurrent.Load()
	if pack != iconPackTex.pack {
		iconPackClearTextures()
		iconPackTex.pack = pack
	}
	if pack == nil {
		return false
	}
	path := pack.files[key]
	if path == "" || dst.W <= 0 || dst.H <= 0 {
		return false
	}
	// Decoded at a bucketed size and scaled the last few pixels: a tile that
	// grows on focus (Home's lift) asks for a new size every frame.
	side := iconPackBucket(max(dst.W, dst.H))
	tex, w, h := iconPackTexture(renderer, path, side, side)
	if tex == nil {
		// While this size decodes, another size of the same picture stands
		// in, so a growing tile does not blink back to the vector glyph.
		if tex, w, h, side = iconPackTextureNearest(path); tex == nil {
			return false
		}
	}
	_ = tex.SetAlphaMod(color.A)
	dw, dh := w*dst.W/side, h*dst.H/side
	r := sdl.Rect{X: dst.X + (dst.W-dw)/2, Y: dst.Y + (dst.H-dh)/2, W: dw, H: dh}
	_ = renderer.Copy(tex, nil, &r)
	return true
}

// iconPackBucket is the box side a picture is decoded at for a drawn side n:
// the next multiple of 16, so an animated size costs one or two decodes.
func iconPackBucket(n int32) int32 {
	const step = 16
	return (n + step - 1) / step * step
}

// iconPackTextureNearest is the largest uploaded square size of path's
// picture, with its drawn width and height and the side it was fitted to; nil
// when none is uploaded yet. Render thread.
func iconPackTextureNearest(path string) (*sdl.Texture, int32, int32, int32) {
	iconPackTex.mu.Lock()
	defer iconPackTex.mu.Unlock()
	var best *sdl.Texture
	var side int32
	for k, e := range iconPackTex.entries {
		if k.path == path && k.w == k.h && e.tex != nil && k.w > side {
			best, side = e.tex, k.w
		}
	}
	if best == nil {
		return nil, 0, 0, 0
	}
	_, _, tw, th, err := best.Query()
	if err != nil {
		return nil, 0, 0, 0
	}
	return best, tw, th, side
}

// iconPackRendererCheck drops the cache when the renderer is a new one (or on
// the first draw). The old textures died with their renderer, so they are
// forgotten, not destroyed: the icon atlas's rule (iconatlas.go). Render
// thread.
func iconPackRendererCheck(renderer *sdl.Renderer) {
	if renderer == iconPackTex.renderer {
		return
	}
	iconPackTex.mu.Lock()
	iconPackResetLocked()
	iconPackTex.mu.Unlock()
	iconPackTex.renderer = renderer
}

// iconPackTexture is the uploaded picture for path fitted inside w×h, with its
// drawn width and height, or nil while it is decoding or when it cannot be
// read. The look packs' card previews share it (lookpacks_ui.go).
func iconPackTexture(renderer *sdl.Renderer, path string, w, h int32) (*sdl.Texture, int32, int32) {
	iconPackRendererCheck(renderer)
	k := iconPackTexKey{path: path, w: w, h: h}
	iconPackTex.mu.Lock()
	if iconPackTex.entries == nil {
		iconPackTex.entries = map[iconPackTexKey]*iconPackTexEntry{}
	}
	if iconPackTex.ctx == nil {
		iconPackTex.ctx, iconPackTex.cancel = context.WithCancel(context.Background())
	}
	e := iconPackTex.entries[k]
	if e == nil {
		e = &iconPackTexEntry{pending: true}
		iconPackTex.entries[k] = e
		ctx := iconPackTex.ctx
		iconPackTex.mu.Unlock()
		iconPackTrim()
		go iconPackDecode(ctx, k, e)
		requestFrameEvery(100)
		return nil, 0, 0
	}
	e.used = frameCount
	if e.tex != nil {
		iconPackTex.mu.Unlock()
		_, _, tw, th, err := e.tex.Query()
		if err != nil {
			return nil, 0, 0
		}
		return e.tex, tw, th
	}
	pic, pending, failed := e.pic, e.pending, e.failed
	e.pic = nil
	iconPackTex.mu.Unlock()
	switch {
	case failed:
		return nil, 0, 0
	case pending:
		// Keep frames coming until the worker hands the picture over.
		requestFrameEvery(100)
		return nil, 0, 0
	case pic == nil:
		return nil, 0, 0
	}
	tex := brandLogoUpload(renderer, pic)
	iconPackTex.mu.Lock()
	if tex == nil {
		e.failed = true
	} else {
		e.tex = tex
	}
	iconPackTex.mu.Unlock()
	if tex == nil {
		return nil, 0, 0
	}
	return tex, int32(pic.Rect.Dx()), int32(pic.Rect.Dy())
}

// iconPackDecode decodes one picture at its drawn size on a worker.
func iconPackDecode(ctx context.Context, k iconPackTexKey, e *iconPackTexEntry) {
	pic, err := lookDecodeFit(ctx, k.path, int(k.w), int(k.h), iconPackMaxFileBytes)
	iconPackTex.mu.Lock()
	defer iconPackTex.mu.Unlock()
	e.pending = false
	if err != nil || pic == nil {
		e.failed = true
		if ctx.Err() == nil {
			logWarn("iconpack", "%s: %v", k.path, err)
		}
		return
	}
	e.pic = pic
}

// iconPackTrim drops the least recently drawn pictures past the cap. Render
// thread.
func iconPackTrim() {
	iconPackTex.mu.Lock()
	defer iconPackTex.mu.Unlock()
	for len(iconPackTex.entries) > iconPackTexCap {
		var oldK iconPackTexKey
		var old *iconPackTexEntry
		for k, e := range iconPackTex.entries {
			if e.pending {
				continue
			}
			if old == nil || e.used < old.used {
				oldK, old = k, e
			}
		}
		if old == nil {
			return
		}
		if old.tex != nil {
			_ = old.tex.Destroy()
		}
		delete(iconPackTex.entries, oldK)
	}
}

// iconPackClearTextures frees every uploaded picture and forgets the decodes
// (render thread). ClearIconAtlas calls it, so a theme repaint and the
// shutdown's texture release cover the pack too.
func iconPackClearTextures() {
	iconPackTex.mu.Lock()
	defer iconPackTex.mu.Unlock()
	for _, e := range iconPackTex.entries {
		if e.tex != nil {
			_ = e.tex.Destroy()
			e.tex = nil
		}
	}
	iconPackResetLocked()
}

// iconPackResetLocked forgets every entry and cancels the decodes in flight;
// the caller holds iconPackTex.mu and has dealt with the textures.
func iconPackResetLocked() {
	iconPackTex.entries = nil
	if iconPackTex.cancel != nil {
		iconPackTex.cancel()
	}
	iconPackTex.ctx, iconPackTex.cancel = nil, nil
}
