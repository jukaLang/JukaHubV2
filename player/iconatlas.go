package main

// ──────────────────────────────────────────────────────────────────────
// Icon atlas for the shared vector glyph set
//
// Home cards, Tools tiles, emulator tiles, list rows and page headers all draw
// their icons through iconDraw (icondraw.go). A glyph is rasterized once per
// (name, pixel size) by the vector rasterizer (iconlang.go / vecraster.go)
// into an 8-bit coverage mask, uploaded as white ink into one shelf-packed
// texture, and every later draw is a single Copy of its sub-rect tinted with
// the texture's colour and alpha modulation. So:
//
//   - a glyph is baked at the exact size it is drawn, never scaled: the 24px
//     badge glyph, the 17px Tools tile glyph and the 46px emulator glyph are
//     each crisp at their own size;
//   - colour costs nothing: white ink times the colour mod is exactly the ink
//     colour, so the focused (dark) and normal (light) variants, every tint
//     and every theme share one baked mask;
//   - a theme change does not need a rebake at all (ClearIconAtlas is still
//     called on theme changes and memory pressure, and simply frees the
//     texture until the next draw);
//   - the frame binds one texture for all of its icons.
//
// The texture starts at 512×512 (1 MB, room for ~340 glyphs at the common
// 24px), grows once to 1024×1024 if a screen ever needs more, and is rebuilt
// from empty if even that fills. Uploads go through Texture.UpdateRGBA, so
// render-target support is not needed; a renderer that cannot create or update
// the texture falls back to painting the same mask as horizontal runs, which
// draws the identical picture with more draw calls.
//
// The atlas is renderer-local, like shapeCache: a texture belongs to the
// renderer that created it.
// ──────────────────────────────────────────────────────────────────────

import (
	"fmt"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// iconAtlasMinSide / iconAtlasMaxSide bound the atlas texture: it is
	// created at the small size and grows once.
	iconAtlasMinSide = int32(512)
	iconAtlasMaxSide = int32(1024)
	// iconAtlasMaxGlyph caps the pixel size a glyph is baked at. Nothing in the
	// shell draws a glyph this large; a bigger request is served from the
	// capped bake, scaled, so a pathological size can never eat the atlas.
	iconAtlasMaxGlyph = int32(256)
	// iconAtlasRunCacheMax bounds the run lists the fallback painter keeps.
	iconAtlasRunCacheMax = 160
)

// iconAtlasKey identifies a baked glyph: its canonical name (iconResolve) and
// its pixel size. Colour is not part of the key; it is applied at draw time.
type iconAtlasKey struct {
	name string
	size int32
}

// iconAtlasShelf is one row of the shelf packer: glyph cells of one height
// placed left to right.
type iconAtlasShelf struct {
	y, h, x int32 // top, height, next free x
}

// iconRun is one horizontal run of equal coverage in a glyph mask, which is
// what the fallback painter draws.
type iconRun struct {
	x, y, w int16
	a       uint8
}

type iconAtlas struct {
	mu          sync.Mutex
	renderer    *sdl.Renderer // the renderer the texture belongs to
	texture     *sdl.Texture
	side        int32 // texture width and height
	slots       map[iconAtlasKey]sdl.Rect
	shelves     []iconAtlasShelf
	nextY       int32
	mod         sdl.Color // the colour and alpha mod last set on texture
	modSet      bool
	unsupported map[*sdl.Renderer]bool
	runs        map[iconAtlasKey][]iconRun
	copies      int64 // draws served from the atlas
	bakes       int64 // glyphs rasterized into the atlas
	direct      int64 // draws painted by the fallback

	// Rasterizer and draw scratch, reused by every bake and copy.
	canvas  vecCanvas
	path    vecPath
	mask    []uint8
	pixels  []uint32
	srcRect sdl.Rect
	dstRect sdl.Rect
}

var homeIconAtlas = &iconAtlas{
	slots:       map[iconAtlasKey]sdl.Rect{},
	unsupported: map[*sdl.Renderer]bool{},
	runs:        map[iconAtlasKey][]iconRun{},
}

// directPaint records a draw the atlas could not serve, which the caller
// painted with the fallback.
func (a *iconAtlas) directPaint() {
	a.mu.Lock()
	a.direct++
	a.mu.Unlock()
}

// forgetLocked drops the texture reference and every slot without destroying
// anything: the caller either destroyed the texture already or knows it died
// with its renderer.
func (a *iconAtlas) forgetLocked() {
	a.texture = nil
	a.side = 0
	a.slots = map[iconAtlasKey]sdl.Rect{}
	a.shelves = a.shelves[:0]
	a.nextY = 0
	a.modSet = false
}

// ClearIconAtlas destroys the atlas texture and forgets its slots. It runs
// wherever ClearShapeCache runs (theme changes, accessibility font changes,
// memory pressure, shutdown); glyphs re-bake on their next draw.
func ClearIconAtlas() {
	a := homeIconAtlas
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.texture != nil {
		a.texture.Destroy()
	}
	a.renderer = nil
	a.forgetLocked()
	a.runs = map[iconAtlasKey][]iconRun{}
	a.copies, a.bakes, a.direct = 0, 0, 0
}

// draw copies the named glyph (a canonical name from iconResolve) into dst,
// a square box, tinted with color, baking it first if this size is new. It
// returns false when the atlas cannot serve the draw, and the caller paints
// the fallback instead.
func (a *iconAtlas) draw(renderer *sdl.Renderer, name string, dst sdl.Rect, color sdl.Color) bool {
	size := dst.W
	if renderer == nil || size <= 0 || dst.H != size {
		return false
	}
	if size > iconAtlasMaxGlyph {
		size = iconAtlasMaxGlyph
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unsupported[renderer] {
		return false
	}
	if a.renderer != renderer {
		// A new renderer (or the first frame): a fresh atlas. Do not Destroy
		// a texture owned by the previous renderer; it died with it.
		a.renderer = renderer
		a.forgetLocked()
		a.copies, a.bakes, a.direct = 0, 0, 0
	}
	key := iconAtlasKey{name: name, size: size}
	src, ok := a.slots[key]
	if ok {
		a.copies++
	} else {
		var err error
		src, err = a.bakeLocked(renderer, key)
		if err != nil {
			a.unsupported[renderer] = true
			return false
		}
		a.bakes++
	}
	if !a.modSet || a.mod != color {
		_ = a.texture.SetColorMod(color.R, color.G, color.B)
		_ = a.texture.SetAlphaMod(color.A)
		a.mod, a.modSet = color, true
	}
	// The rects live in the atlas (heap) so handing their addresses to cgo
	// does not allocate per draw.
	a.srcRect, a.dstRect = src, dst
	if err := renderer.Copy(a.texture, &a.srcRect, &a.dstRect); err != nil {
		// Rare (a target out of range during a resize): keep the atlas and
		// let the caller paint this one draw.
		return false
	}
	return true
}

// createLocked makes an empty side×side atlas texture for renderer.
func (a *iconAtlas) createLocked(renderer *sdl.Renderer, side int32) error {
	tex, err := renderer.CreateTexture(uint32(sdl.PIXELFORMAT_ARGB8888), sdl.TEXTUREACCESS_STATIC, side, side)
	if err != nil {
		return fmt.Errorf("icon atlas: create %dx%d texture: %w", side, side, err)
	}
	if err := tex.SetBlendMode(sdl.BLENDMODE_BLEND); err != nil {
		tex.Destroy()
		return fmt.Errorf("icon atlas: blend mode: %w", err)
	}
	a.forgetLocked()
	a.texture = tex
	a.side = side
	return nil
}

// placeLocked finds room for a cell×cell square and returns its top-left.
// Shelves are cell heights rounded up to 4px, so neighbouring sizes share
// rows.
func (a *iconAtlas) placeLocked(cell int32) (int32, int32, bool) {
	h := (cell + 3) &^ 3
	for i := range a.shelves {
		s := &a.shelves[i]
		if s.h == h && s.x+cell <= a.side {
			x := s.x
			s.x += cell
			return x, s.y, true
		}
	}
	if cell > a.side || a.nextY+h > a.side {
		return 0, 0, false
	}
	y := a.nextY
	a.shelves = append(a.shelves, iconAtlasShelf{y: y, h: h, x: cell})
	a.nextY += h
	return 0, y, true
}

// bakeLocked rasterizes key into a free cell and returns the glyph's source
// rect. Each cell carries a one-pixel transparent gutter, so a draw scaled by
// the renderer's logical size never filters a neighbour's edge in.
func (a *iconAtlas) bakeLocked(renderer *sdl.Renderer, key iconAtlasKey) (sdl.Rect, error) {
	cell := key.size + 2
	if a.texture == nil {
		if err := a.createLocked(renderer, iconAtlasMinSide); err != nil {
			return sdl.Rect{}, err
		}
	}
	x, y, ok := a.placeLocked(cell)
	if !ok {
		// Full: grow once, otherwise start again from empty. The texture is
		// this renderer's own, so it is destroyed rather than dropped; SDL
		// flushes queued draws that still use it first.
		side := a.side * 2
		if side > iconAtlasMaxSide {
			side = iconAtlasMaxSide
		}
		a.texture.Destroy()
		a.forgetLocked()
		if err := a.createLocked(renderer, side); err != nil {
			return sdl.Rect{}, err
		}
		if x, y, ok = a.placeLocked(cell); !ok {
			return sdl.Rect{}, fmt.Errorf("icon atlas: no room for a %dpx glyph", key.size)
		}
	}

	size := int(key.size)
	if n := size * size; cap(a.mask) < n {
		a.mask = make([]uint8, n)
	} else {
		a.mask = a.mask[:n]
	}
	iconRasterize(key.name, size, a.mask, &a.canvas, &a.path)

	c := int(cell)
	if n := c * c; cap(a.pixels) < n {
		a.pixels = make([]uint32, n)
	} else {
		a.pixels = a.pixels[:n]
	}
	// White ink, coverage in alpha (ARGB8888 as a packed uint32). The gutter
	// and empty pixels are transparent white, so filtering never darkens an
	// edge.
	for i := range a.pixels {
		a.pixels[i] = 0x00FFFFFF
	}
	for gy := 0; gy < size; gy++ {
		row := (gy+1)*c + 1
		for gx := 0; gx < size; gx++ {
			if v := a.mask[gy*size+gx]; v != 0 {
				a.pixels[row+gx] = uint32(v)<<24 | 0x00FFFFFF
			}
		}
	}
	cellRect := sdl.Rect{X: x, Y: y, W: cell, H: cell}
	if err := a.texture.UpdateRGBA(&cellRect, a.pixels, c); err != nil {
		return sdl.Rect{}, fmt.Errorf("icon atlas: upload: %w", err)
	}
	src := sdl.Rect{X: x + 1, Y: y + 1, W: key.size, H: key.size}
	a.slots[key] = src
	return src, nil
}

// runsFor returns the fallback painter's run list for key, rasterizing it on
// first use. The list is cached (bounded) so the fallback does not rasterize
// every frame.
func (a *iconAtlas) runsFor(key iconAtlasKey) []iconRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	if runs, ok := a.runs[key]; ok {
		return runs
	}
	size := int(key.size)
	if n := size * size; cap(a.mask) < n {
		a.mask = make([]uint8, n)
	} else {
		a.mask = a.mask[:n]
	}
	iconRasterize(key.name, size, a.mask, &a.canvas, &a.path)
	var runs []iconRun
	for y := 0; y < size; y++ {
		for x := 0; x < size; {
			// Coverage is quantized to 32 levels so neighbouring pixels of
			// an anti-aliased edge merge into one run; full ink stays exact.
			v := iconRunLevel(a.mask[y*size+x])
			if v == 0 {
				x++
				continue
			}
			start := x
			for x < size && iconRunLevel(a.mask[y*size+x]) == v {
				x++
			}
			runs = append(runs, iconRun{x: int16(start), y: int16(y), w: int16(x - start), a: v})
		}
	}
	if len(a.runs) >= iconAtlasRunCacheMax {
		a.runs = map[iconAtlasKey][]iconRun{}
	}
	a.runs[key] = runs
	return runs
}

// iconRunLevel quantizes a coverage byte for the run painter to the middle of
// its 8-level bucket; 0 and 255 stay exact.
func iconRunLevel(v uint8) uint8 {
	if v == 0 || v == 255 {
		return v
	}
	return v>>3<<3 | 4
}

// iconPaintRuns draws a run list at (x, y) in color: the fallback painter for
// renderers the atlas cannot use. It needs no texture, only filled rects.
func iconPaintRuns(renderer *sdl.Renderer, runs []iconRun, x, y int32, color sdl.Color) {
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	last := -1
	var rc sdl.Rect
	for _, r := range runs {
		alpha := (int(r.a)*int(color.A) + 127) / 255
		if alpha == 0 {
			continue
		}
		if alpha != last {
			_ = renderer.SetDrawColor(color.R, color.G, color.B, uint8(alpha))
			last = alpha
		}
		rc = sdl.Rect{X: x + int32(r.x), Y: y + int32(r.y), W: int32(r.w), H: 1}
		_ = renderer.FillRect(&rc)
	}
}
